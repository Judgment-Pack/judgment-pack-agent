package eino

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/wire"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
	"github.com/cloudwego/eino/schema"
)

// Uses the already-open context, existing prompt/wire assembler and canonical model call.
// The private classifier stage selects a single physical attempt below retry/failover.
func (r *runState) classifyTurnRoute(ctx context.Context, tier string, config runcontext.RouteClassifier) (runcontext.RouteClassifierRecord, error) {
	record := runcontext.RouteClassifierRecord{Binding: runcontext.RouteDigest(map[string]any{"provider": r.sc.Provider("author"), "model": r.run.ModelID()}), PromptDigest: runcontext.RouteDigest("not-dispatched"), Status: "refused"}
	labels := []string{}
	for label := range config.Labels {
		labels = append(labels, label)
	}
	sort.Strings(labels)
	schemaRaw, _ := json.Marshal(map[string]any{"type": "object", "properties": map[string]any{"label": map[string]any{"type": "string", "enum": labels}}, "required": []string{"label"}, "additionalProperties": false})
	params, err := wire.Parse(schemaRaw)
	if err != nil {
		return record, err
	}
	tools := []wire.WireTool{{Name: "choose_turn_route", Description: "Choose one declared route label.", Schema: params}}
	instructions := r.sc.System("Choose exactly one declared route label for the current request. Return a choose_turn_route call or exactly {\"label\":\"<declared label>\"}. This selects cost and focus only, never authority.", "")
	shown, _ := runcontext.ParseJSON(schemaRaw)
	var current *runcontext.CurrentRequest
	if r.run.RequestContext() != nil {
		current = r.run.RequestContext().CurrentRequest
	}
	opening, err := r.sc.Opening(r.session.Prompt, instructions, shown, current)
	if err != nil {
		return record, err
	}
	slot := wire.OpenThinking(wire.ThinkingTier(tier), r.family, &r.capabilities.Reasoning, r.capabilities.Tiers, r.capabilities.FallbackTiers)
	oldPlacement := r.placement
	r.placement = wire.TrailingUser
	defer func() { r.placement = oldPlacement }()
	r.sc.Execution.UseConnection(r.sc.Provider("author").Connection, "classifier")
	r.sc.Execution.BeginAttempt(1)
	r.call = r.modelCall(func() string { return "work" })
	var answer wire.Answer
	step := &stepRequest{system: wire.SystemText{Head: instructions}, tools: tools, tier: slot.Members(), answered: func(value wire.Answer) { answer = value }}
	_, err = (&relayModel{run: r, model: r.run.ModelID()}).Generate(context.WithValue(ctx, stepKey{}, step), []*schema.Message{userMessage(opening...)})
	prompts := r.sc.Execution.State().Prompts
	if len(prompts) > 0 {
		record.PromptDigest = "sha256:" + prompts[len(prompts)-1].Sha256
	}
	if err != nil {
		if ctx.Err() != nil || r.sc.Persistence("classifier").Writer.Failed() {
			return record, err
		}
		r.sc.Execution.RecordCompletion(contract.ProvenanceCompletion{FinishReason: "error"}, "work")
		f := failure.Normalize(err, "model", time.Now())
		if f.Code == "configuration" {
			record.Status = "unsupported"
		}
		return record, nil
	}
	completion := completionOf(answer)
	r.sc.Execution.RecordCompletion(completion, "work")
	record.Usage = map[string]int64{}
	if completion.InputTokens != nil {
		record.Usage["inputTokens"] = *completion.InputTokens
	}
	if completion.OutputTokens != nil {
		record.Usage["outputTokens"] = *completion.OutputTokens
	}
	raw := []byte(strings.TrimSpace(answer.Text))
	if len(answer.ToolCalls) == 1 && answer.ToolCalls[0].Name == "choose_turn_route" {
		raw = []byte(answer.ToolCalls[0].Arguments)
	} else if len(answer.ToolCalls) != 0 {
		record.Status = "unknown-label"
		return record, nil
	}
	var output map[string]json.RawMessage
	var label string
	if len(raw) > 512 || json.Unmarshal(raw, &output) != nil || len(output) != 1 || json.Unmarshal(output["label"], &label) != nil {
		record.Status = "unknown-label"
		return record, nil
	}
	if _, ok := config.Labels[label]; !ok || answer.FinishReason != wire.FinishStop && answer.FinishReason != wire.FinishToolCalls {
		record.Status = "unknown-label"
		return record, nil
	}
	record.Status = "label"
	record.Label = label
	return record, nil
}
