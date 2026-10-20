package eino_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/eino"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
	"github.com/Judgment-Pack/judgment-pack-agent/go/relay"
)

// On eino an act call carries an id even on a run whose events carry none (ADR-0008, #83 row 1), so
// its decision is its own: the policy allows the first call once, the host's Authorize stops it before
// the source gate, and the second call, with the same arguments, which the policy refuses, is never
// run under the first call's allow.
func TestAnEinoActCallsDecisionIsItsOwnOnARunWithoutIDs(t *testing.T) {
	runtime := func(_ context.Context, frame json.RawMessage) (json.RawMessage, error) {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.Unmarshal(frame, &request); err != nil {
			return nil, err
		}
		if request.Method == "notifications/initialized" {
			return nil, nil
		}
		if request.Method == "tools/list" {
			return json.RawMessage(`{"jsonrpc":"2.0","id":` + string(request.ID) + `,"result":{"tools":[]}}`), nil
		}
		return json.RawMessage(`{"jsonrpc":"2.0","id":` + string(request.ID) + `,"result":{}}`), nil
	}
	allowed, failNext, ran := 0, false, 0
	tool := runcontext.HostTool{Name: "update_work_item", Description: "Update a work item.",
		InputSchema: json.RawMessage(`{"type":"object"}`),
		Source:      &runcontext.ToolSource{Kind: "gateway", ID: "ado", Trust: "connected", Version: "2026-10"},
		Approve: func(runcontext.ApprovalRequest) string {
			if allowed > 0 {
				return "refuse"
			}
			allowed++
			failNext = true
			return "allow"
		},
		Execute: func(context.Context, json.RawMessage) (runcontext.ToolResult, error) {
			ran++
			return runcontext.ResultOf(nil, nil), nil
		}}
	declared, err := runcontext.OfferedHostTools([]runcontext.HostTool{tool}, gate.Ceiling, runcontext.Defaults)
	if err != nil {
		t.Fatal(err)
	}
	g := gate.New(gate.Options{Runtime: runtime, Granted: gate.Ceiling, Emit: func(contract.RunEvent) {}})
	offer, err := g.Open(context.Background(), declared)
	if err != nil {
		t.Fatal(err)
	}
	call := func(id string) map[string]any {
		return map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": id, "type": "function",
			"function": map[string]any{"name": "ado__update_work_item", "arguments": `{"id":7,"state":"Done"}`}}}}
	}
	requests := 0
	model := func(_ context.Context, _ string, _ relay.Request) (*http.Response, error) {
		requests++
		switch requests {
		case 1:
			return openAIStream(call("call_1"), "tool_calls"), nil
		case 2:
			return openAIStream(call("call_2"), "tool_calls"), nil
		}
		return openAIStream(map[string]any{"content": "Neither was changed."}, "stop"), nil
	}
	var events []contract.RunEvent
	emit := func(event contract.RunEvent) { events = append(events, event) }
	off := false
	run := &adapter.Run{
		Wire: "openai-compatible", ModelName: "fixture", Model: model, Gate: g.Calls(), Tools: offer.Tools, Thinking: "off",
		Prompt: "Update the item.", AllowConversation: true, Interactive: false, Review: &off, HostTools: []runcontext.HostTool{tool},
		Context: &runcontext.Input{
			CurrentRequest: &runcontext.CurrentRequest{Message: "Update the item.", Input: "Update the item."},
			// The host's authorization fails once, for the first call, after its decision and before the source gate.
			Authorize: func(context.Context) error {
				if failNext {
					failNext = false
					return errors.New("not authorized this time")
				}
				return nil
			},
		},
		Emit: emit,
	}
	if err := eino.New().Run(legContext(t), run); err != nil {
		t.Fatal(err)
	}
	if ran != 0 {
		t.Fatalf("the host ran %d calls; the second ran under the first call's allow", ran)
	}
	var ids, refused []string
	failedResults := 0
	for _, event := range events {
		switch {
		case event.ToolCall != nil && event.ToolCall.Name == "ado__update_work_item":
			if event.ToolCall.CallID == nil || *event.ToolCall.CallID == "" {
				t.Fatal("an act call on a run without ids carried none")
			}
			ids = append(ids, *event.ToolCall.CallID)
		case event.ToolResult != nil && event.ToolResult.Name == "ado__update_work_item" && event.ToolResult.IsError:
			failedResults++
		case event.Guardrail != nil && event.Guardrail.Tool == "ado__update_work_item" && event.Guardrail.Reason != nil:
			refused = append(refused, *event.Guardrail.Reason)
		}
	}
	if len(ids) != 2 || ids[0] == ids[1] || failedResults != 2 {
		t.Fatalf("the calls' ids %v; %d failed results", ids, failedResults)
	}
	if len(refused) != 1 || refused[0] != "approval refused" {
		t.Fatalf("the refusals said %v", refused)
	}
	if requests != 3 {
		t.Fatalf("%d requests", requests)
	}
}
