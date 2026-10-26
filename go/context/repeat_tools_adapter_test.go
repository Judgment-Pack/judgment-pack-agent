package runcontext_test

import (
	"context"
	"encoding/json"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	native "github.com/Judgment-Pack/judgment-pack-agent/go/adapter/agent"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/eino"
	rc "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
	"github.com/Judgment-Pack/judgment-pack-agent/go/relay"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

func repeat204AdapterFixture(store rc.RunStore, executes *int, events *[]contract.RunEvent) *adapter.Run {
	host := rc.HostTool{Name: "lookup", Description: "Read one value.", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`), Effect: "read", Taxonomy: &rc.ToolTaxonomy{Operation: "read", Effect: "read"}, Execute: func(context.Context, json.RawMessage) (rc.ToolResult, error) {
		*executes++
		return rc.ResultOf([]rc.Content{rc.TextContent("Value.")}, nil), nil
	}}
	g := gate.New(gate.Options{})
	g.SetOffer(contract.ToolOffer{HostTools: []contract.ToolOfferHostTool{host.Declaration()}})
	return &adapter.Run{Wire: "openai-compatible", ModelName: "fixture", Thinking: "off", Prompt: "Read the value.", AllowConversation: true, Critic: "off", HostTools: []rc.HostTool{host}, Gate: g.Calls(), EnrollContext: func(_ context.Context, offer adapter.ContextOffer) error {
		return g.EnrollContext(offer.ID, offer.HostTools)
	}, Context: &rc.Input{RunStore: &rc.DurableRunInput{Store: store, ID: "repeat-adapter"}, CurrentRequest: &rc.CurrentRequest{Message: "Read", Input: "Fixed"}}, Policy: rc.PolicyLayer{"version": 2, "repeatTools": map[string]any{"mode": "on", "stopAt": 2}}, Emit: func(event contract.RunEvent) { *events = append(*events, event) }}
}
func TestRepeat204ActualEinoAndNativeAdapters(t *testing.T) {
	for _, engine := range []string{"eino", "native"} {
		t.Run(engine, func(t *testing.T) {
			store := rc.NewMemoryRunStore(rc.MemoryRunStoreOptions{})
			executes, sends := 0, 0
			events := []contract.RunEvent{}
			run := repeat204AdapterFixture(store, &executes, &events)
			bodies := []string{}
			var implementation adapter.Adapter
			if engine == "eino" {
				implementation = eino.New()
				run.Model = func(_ context.Context, _ string, request relay.Request) (*http.Response, error) {
					sends++
					bodies = append(bodies, string(request.Body))
					calls := []any{map[string]any{"index": 0, "id": "wire-" + strconv.Itoa(sends), "type": "function", "function": map[string]any{"name": "lookup", "arguments": "{}"}}}
					chunk := map[string]any{"id": "answer", "object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": calls}, "finish_reason": "tool_calls"}}, "usage": map[string]any{"prompt_tokens": 2, "completion_tokens": 1}}
					raw, _ := json.Marshal(chunk)
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: " + string(raw) + "\n\ndata: [DONE]\n\n"))}, nil
				}
			} else {
				implementation = native.New()
				run.Agent = &adapter.Agent{Model: "fixture", Run: func(ctx context.Context, request adapter.AgentRequest, callbacks adapter.AgentCallbacks) error {
					sends++
					name := ""
					for _, tool := range request.Tools {
						if strings.HasPrefix(tool.Description, "lookup:") {
							name = tool.Name
						}
					}
					if name == "" {
						t.Fatal("actual native offer omitted fixture")
					}
					for i := 0; i < 4; i++ {
						if _, err := callbacks.Tool(ctx, adapter.AgentToolCall{ID: "wire-" + strconv.Itoa(i), Name: name, Arguments: json.RawMessage(`{}`)}); err != nil {
							return err
						}
					}
					return callbacks.Event(ctx, adapter.AgentEvent{Type: "message", Text: "Done.", Phase: "final"})
				}}
			}
			_ = implementation.Run(context.Background(), run)
			want := 3
			if engine == "native" {
				want = 1
			}
			if sends != want || executes != 3 {
				raw, _ := json.Marshal(events)
				t.Fatalf("physical model/tool counts=%d/%d want%d/3 events%s", sends, executes, want, raw)
			}
			if engine == "eino" {
				reminders := 0
				for _, body := range bodies {
					if strings.Contains(body, rc.RepeatReminder) {
						reminders++
					}
				}
				if reminders != 1 || !strings.Contains(bodies[2], "package repeat reminder") {
					t.Fatal("assembler reminder missing or repeated")
				}
			}
			head, err := store.Load(context.Background(), "repeat-adapter")
			if err != nil || head == nil {
				t.Fatal(err)
			}
			cp, err := rc.ReadExecution(rc.MustParseJSON(string(head.Checkpoint)))
			if err != nil || cp.Version != 3 || cp.Status != "stopped" || cp.Reason == nil || *cp.Reason != "RepeatedToolLimit" || cp.RepeatTools.Executions[0].Count != 2 {
				t.Fatal("actual adapter lost stop", err, string(head.Checkpoint))
			}
			repeats := 0
			for _, event := range events {
				if event.RepeatDetected != nil {
					repeats++
				}
			}
			if repeats != 2 {
				t.Fatalf("delivered repeats=%d", repeats)
			}
			run.Context.RunStore.Resume = true
			_ = implementation.Run(context.Background(), run)
			if sends != want || executes != 3 {
				t.Fatal("resume dispatched past retained stop")
			}
		})
	}
}
