package eino

import (
	"bytes"
	"context"
	"encoding/json"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/wire"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
	"github.com/cloudwego/eino/schema"
)

type observationState struct {
	tools  []contract.ToolOfferHostTool
	result adapter.ObservationResult
}

func observationRefused() error {
	return failure.Authored("configuration", "Observation requires one fresh durable model generation with fixed bounded declarations and no executable tool registry.", "context")
}

// Observe runs one fresh canonical model generation. It never executes returned calls,
// retries, finishes, fails over, or resumes/replays an observation. Native agent loops are opaque
// and unsupported. The host relay capability must send each invocation as one physical request.
func Observe(ctx context.Context, run *adapter.Run, options adapter.ObservationOptions) (adapter.ObservationResult, error) {
	var zero adapter.ObservationResult
	if run == nil || run.Model == nil || run.Agent != nil || run.Context == nil || run.Context.RunStore == nil || run.Context.RunStore.Store == nil || run.Context.RunStore.Store.Durability() == runcontext.RunStoreSingleWriterCallback || run.Context.RunStore.Resume || run.Context.Execution != nil || run.Context.Persistence != nil || run.Context.Checkpoint != nil || run.Context.Approval != nil || len(run.Tools) != 0 || len(run.HostTools) != 0 || len(run.Skills) != 0 || len(run.Subagents) != 0 || run.Children != nil || run.Context.Objective != "" || len(options.Tools) > adapter.ObservationMaxTools {
		return zero, observationRefused()
	}
	if cap := run.Context.RequestCap; cap != nil && *cap != 1 {
		return zero, observationRefused()
	}
	raw, err := json.Marshal(options.Tools)
	if err != nil || len(raw) > 64*1024 {
		return zero, observationRefused()
	}
	held := &observationState{}
	if err = json.Unmarshal(raw, &held.tools); err != nil {
		return zero, observationRefused()
	}
	seen := map[string]bool{}
	for _, tool := range held.tools {
		var object map[string]json.RawMessage
		if tool.Name == "" || len(tool.Name) > 64 || seen[tool.Name] || tool.Description == "" || json.Unmarshal(tool.InputSchema, &object) != nil || object == nil || wire.TaxonomyProblem(wire.TaxonomyFromContract(tool.Taxonomy), false) != "" {
			return zero, observationRefused()
		}
		seen[tool.Name] = true
	}
	input := *run.Context
	cap := 1
	input.RequestCap = &cap
	local := *run
	local.Context = &input
	var delivered error
	emit := local.Emit
	local.Emit = func(event contract.RunEvent) {
		if event.Error != nil && event.Error.Failure != nil {
			delivered = failure.NewError(*event.Error.Failure)
		}
		if emit != nil {
			emit(event)
		}
	}
	if err = execute(ctx, &local, held); err != nil {
		return zero, err
	}
	if delivered != nil {
		return zero, delivered
	}
	head, err := input.RunStore.Store.Load(ctx, input.RunStore.ID)
	if err != nil {
		return zero, err
	}
	if head == nil {
		return zero, &runcontext.StorageAcknowledgementInvalid{}
	}
	value, err := runcontext.ParseJSON(head.Checkpoint)
	if err != nil {
		return zero, err
	}
	retained, err := runcontext.ReadExecution(value)
	if err != nil {
		return zero, err
	}
	if retained == nil || head.RunID != input.RunStore.ID || head.Revision == "" || head.CommitID == "" {
		return zero, &runcontext.StorageAcknowledgementInvalid{}
	}
	checkpoint := *retained
	if checkpoint.ID != input.RunStore.ID || checkpoint.RequestCap == nil || *checkpoint.RequestCap != 1 || checkpoint.Used != 1 || checkpoint.Limit != 1 {
		return zero, &runcontext.ContinuationIdentityChanged{}
	}
	if ctx.Err() != nil {
		return zero, failure.Cancelled
	}
	held.result.Checkpoint = checkpoint
	held.result.Receipt = head.RunReceipt
	return held.result, nil
}

func (r *runState) observeOnce(ctx context.Context, held *observationState) error {
	if len(held.tools) > 0 && !r.nativeTools {
		return observationRefused()
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(r.sc.Policy.TurnMilliseconds)*time.Millisecond)
	defer cancel()
	tools := []wire.WireTool{}
	schemas := runcontext.Array()
	names := map[string]bool{}
	for _, tool := range held.tools {
		value, err := wire.Parse(tool.InputSchema)
		if err != nil {
			return observationRefused()
		}
		tools = append(tools, wire.WireTool{Name: tool.Name, Description: tool.Description, Schema: value})
		names[tool.Name] = true
		raw, _ := json.Marshal(tool)
		parsed, err := runcontext.ParseJSON(raw)
		if err != nil {
			return err
		}
		schemas.Append(parsed)
	}
	instructions := r.sc.System(r.systemFor()+runcontext.DataRule+r.run.LanguageInstructions, "")
	state := stateText(r.sc.Execution.RequestState())
	var current *runcontext.CurrentRequest
	if r.run.RequestContext() != nil {
		current = r.run.RequestContext().CurrentRequest
	}
	opening, err := r.sc.OpeningProjected(ctx, r.session.Prompt, instructions+"\n"+state, schemas, current, r.run.OpeningProjection())
	if err != nil {
		return err
	}
	system, messages := placeState(r.placement, []*schema.Message{userMessage(opening...)}, instructions, state)
	r.call = r.modelCall(func() string { return "work" })
	r.sc.Execution.BeginAttempt(1)
	var answer wire.Answer
	step := &stepRequest{system: system, tools: tools, tier: r.slot.Members(), answered: func(value wire.Answer) { answer = value }}
	if _, err = (&relayModel{run: r, model: r.run.ModelID()}).Generate(context.WithValue(ctx, stepKey{}, step), messages); err != nil {
		return err
	}
	completion := completionOf(answer)
	r.sc.Execution.RecordCompletion(completion, "work")
	if len(answer.Text) > adapter.ObservationMaxText || len(answer.ToolCalls) > adapter.ObservationMaxCalls {
		return failure.Err("invalid-response", "model", "unknown")
	}
	result := adapter.ObservationResult{Text: answer.Text, Calls: []adapter.ObservedToolCall{}, Completion: completion}
	seen := map[string]bool{}
	for _, call := range answer.ToolCalls {
		var args map[string]json.RawMessage
		if call.ID == "" || len(call.ID) > 256 || seen[call.ID] || !names[call.Name] || len(call.Arguments) > adapter.ObservationMaxArguments || json.Unmarshal([]byte(call.Arguments), &args) != nil || args == nil {
			return failure.Err("invalid-response", "model", "unknown")
		}
		seen[call.ID] = true
		result.Calls = append(result.Calls, adapter.ObservedToolCall{ID: call.ID, Name: call.Name, Arguments: bytes.Clone([]byte(call.Arguments))})
	}
	raw, err := json.Marshal(result)
	if err != nil || len(raw) > adapter.ObservationMaxBytes {
		return failure.Err("invalid-response", "model", "unknown")
	}
	guarded, err := r.sc.Output.JSON(raw, "message", false)
	if err != nil {
		return err
	}
	if !bytes.Equal(bytes.TrimSpace(guarded), bytes.TrimSpace(raw)) {
		return failure.Err("invalid-response", "model", "unknown")
	}
	if answer.FinishReason != wire.FinishStop && answer.FinishReason != wire.FinishToolCalls {
		return failure.Err("invalid-response", "model", "unknown")
	}
	held.result = result
	return nil
}
