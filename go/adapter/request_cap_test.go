package adapter_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	native "github.com/Judgment-Pack/judgment-pack-agent/go/adapter/agent"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/eino"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
	"github.com/Judgment-Pack/judgment-pack-agent/go/relay"
)

func TestRequestCapEinoDispatchAndInterruptedResume(t *testing.T) {
	for _, kind := range []string{"normal", "empty", "truncated", "tool", "text-tool", "retry", "reformat", "critic", "storage", "unknown", "two"} {
		t.Run(kind, func(t *testing.T) {
			cap := 1
			if kind == "two" {
				cap = 2
			}
			calls, tools := 0, 0
			var saved []contract.Checkpoint
			var bodies []map[string]any
			host := runcontext.HostTool{Name: "read_policy", Description: "Read policy", Effect: "read", Taxonomy: &runcontext.ToolTaxonomy{Operation: "read", Effect: "read"}, InputSchema: json.RawMessage(`{"type":"object","properties":{}}`), Execute: func(context.Context, json.RawMessage) (runcontext.ToolResult, error) {
				tools++
				return runcontext.ResultOf([]runcontext.Content{runcontext.TextContent("Observed policy.")}, nil), nil
			}}
			g := gate.New(gate.Options{})
			g.SetOffer(contract.ToolOffer{Tools: []contract.ToolOfferRuntimeTool{}, HostTools: []contract.ToolOfferHostTool{host.Declaration()}})
			input := &runcontext.Input{RequestCap: &cap, CurrentRequest: &runcontext.CurrentRequest{Message: "One admitted operation", Input: "Trusted reservation"}, Checkpoint: func(_ context.Context, cp contract.Checkpoint) error {
				saved = append(saved, cp)
				if kind == "storage" && cp.Used == 1 {
					return errors.New("durable handoff interrupted")
				}
				return nil
			}}
			run := &adapter.Run{Wire: "openai-compatible", ModelName: "fixture", Prompt: "Return the admitted operation result.", AllowConversation: kind != "reformat", Critic: "off", Gate: g.Calls(), HostTools: []runcontext.HostTool{host}, Context: input, Emit: func(contract.RunEvent) {}}
			if kind == "critic" {
				run.Critic = "on"
			}
			if kind == "text-tool" {
				native := false
				run.Capabilities = &contract.CapabilityEndpointCapabilities{NativeToolCalls: &native}
			}
			run.Model = func(_ context.Context, _ string, request relay.Request) (*http.Response, error) {
				calls++
				var body map[string]any
				if err := json.Unmarshal(request.Body, &body); err != nil {
					t.Fatal(err)
				}
				bodies = append(bodies, body)
				if len(saved) == 0 || saved[len(saved)-1].Used != int64(calls) || saved[len(saved)-1].RequestCap == nil || *saved[len(saved)-1].RequestCap != int64(cap) {
					t.Fatal("cap claim was not durable before capability")
				}
				if kind == "unknown" {
					return nil, errors.New("delivery unknown")
				}
				status, reason, text := 200, "stop", "Observed answer."
				if kind == "retry" {
					status = 503
				}
				if kind == "empty" {
					text = ""
				}
				if kind == "truncated" {
					reason = "length"
				}
				if kind == "reformat" {
					text = "not a proposal"
				}
				if kind == "text-tool" {
					text = "```json\n{\"tool\":\"read_policy\",\"arguments\":{}}\n```"
				}
				message := map[string]any{"role": "assistant", "content": text}
				if kind == "tool" || kind == "two" && calls == 1 {
					message["tool_calls"] = []any{map[string]any{"id": "one", "type": "function", "function": map[string]any{"name": "read_policy", "arguments": "{}"}}}
					reason = "tool_calls"
				}
				raw, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": reason}}})
				return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}, "Retry-After": []string{"0"}}, Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
			}
			if err := eino.New().Run(context.Background(), run); err != nil {
				t.Fatal(err)
			}
			want := 1
			if kind == "storage" {
				want = 0
			}
			if kind == "two" {
				want = 2
			}
			if calls != want {
				t.Fatalf("dispatches %d, want %d", calls, want)
			}
			if kind == "two" {
				if tools != 1 {
					t.Fatalf("offered tool positive control: %d", tools)
				}
			} else if tools != 0 {
				t.Fatal("final tool executed")
			}
			if len(bodies) > 0 {
				last := bodies[len(bodies)-1]
				if list, ok := last["tools"].([]any); ok && len(list) > 0 {
					t.Fatal("final request tools remained open")
				}
			}
			var claimed *contract.Checkpoint
			for i := range saved {
				raw, _ := json.Marshal(saved[i])
				value, _ := runcontext.ParseJSON(raw)
				if _, err := runcontext.ReadExecution(value); err != nil {
					t.Fatal(err)
				}
				if saved[i].Used == int64(cap) {
					claimed = &saved[i]
				}
			}
			if claimed == nil {
				t.Fatal("missing consumed durable checkpoint")
			}
			if kind == "storage" || kind == "unknown" {
				cp := *claimed
				cp.Status = "stopped"
				run.Context = &runcontext.Input{RequestCap: &cap, CurrentRequest: input.CurrentRequest, Execution: &cp}
				before := calls
				if err := eino.New().Run(context.Background(), run); err != nil {
					t.Fatal(err)
				}
				if calls != before {
					t.Fatal("interrupted claim replayed")
				}
			}
		})
	}
}

type refusedNativeCap struct{}

func (refusedNativeCap) Attach(c *runcontext.SessionContext) { c.Execution.LimitTo(1) }
func TestRequestCapNativeIgnoredHelperWithoutStorage(t *testing.T) {
	for _, refused := range []bool{false, true} {
		calls := 0
		var events []contract.RunEvent
		g := gate.New(gate.Options{})
		g.SetOffer(contract.ToolOffer{Tools: []contract.ToolOfferRuntimeTool{}, HostTools: []contract.ToolOfferHostTool{}})
		input := &runcontext.Input{}
		if refused {
			input.Link = refusedNativeCap{}
		}
		run := &adapter.Run{Prompt: "Return a native answer.", AllowConversation: true, Critic: "off", Context: input, Gate: g.Calls(), Emit: func(event contract.RunEvent) { events = append(events, event) }, Agent: &adapter.Agent{Model: "fixture", Run: func(ctx context.Context, _ adapter.AgentRequest, callbacks adapter.AgentCallbacks) error {
			calls++
			return callbacks.Event(ctx, adapter.AgentEvent{Type: "message", ID: "native", Text: "Native answer.", Phase: "final"})
		}}}
		if err := native.New().Run(context.Background(), run); err != nil {
			t.Fatal(err)
		}
		want := 1
		if refused {
			want = 0
		}
		if calls != want {
			t.Fatalf("native invocation calls %d, want %d after ignored helper failure", calls, want)
		}
		if refused {
			found := false
			for _, event := range events {
				if event.Error != nil && event.Error.Failure != nil && event.Error.Failure.Code == "configuration" {
					found = true
				}
			}
			if !found {
				t.Fatal("native refusal did not reach error event")
			}
		}
	}
}
