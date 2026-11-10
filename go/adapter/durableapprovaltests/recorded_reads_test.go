package durableapprovaltests

// #352: a Runtime read in a durable run is no dispatch of its own, and it is given the form the run's writer
// records a call's result in (#343): the tool_result event and what the model is given hold that form, on the
// native agent as on the API engine. Before, the native agent was given the Runtime's text as it came, and the
// API engine's next request could not be recorded. recordedReads.test.ts holds the TypeScript.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// readRuntime is a Runtime that offers get_schema, a read, and answers it with the witness's pack.
type readRuntime struct{ calls int }

func (r *readRuntime) transport(_ context.Context, frame json.RawMessage) (json.RawMessage, error) {
	var request struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	if err := json.Unmarshal(frame, &request); err != nil {
		return nil, err
	}
	if len(request.ID) == 0 {
		return nil, nil
	}
	var result any
	switch request.Method {
	case "initialize":
		result = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "read-runtime", "version": "fixture"}}
	case "tools/list":
		description := "Read a schema."
		result = map[string]any{"tools": []contract.ToolOfferRuntimeTool{{Name: "get_schema", Description: &description, InputSchema: json.RawMessage(`{"type":"object"}`), Meta: contract.ToolTaxonomy{Operation: "read", Effect: "read"}}}}
	case "tools/call":
		r.calls++
		text := `{"packId":"` + recordedRaw + `","status":"evaluated"}`
		result = map[string]any{"content": []map[string]any{{"type": "text", "text": text}}, "structuredContent": json.RawMessage(text)}
	default:
		return nil, errors.New("unexpected method")
	}
	return json.Marshal(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
}

func TestADurableRunsRuntimeReadIsWhatTheModelIsGiven(t *testing.T) {
	want := `{"packId":"` + recordedRedacted + `","status":"evaluated"}`
	for _, engine := range []string{"api", "native"} {
		t.Run(engine, func(t *testing.T) {
			runtime := &readRuntime{}
			g := gate.New(gate.Options{Runtime: runtime.transport, Granted: gate.Ceiling, Emit: func(contract.RunEvent) {}})
			offer, err := g.Open(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			store := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
			var events []contract.RunEvent
			var given, requests []string
			run := &adapter.Run{
				Wire: "openai-compatible", ModelName: "fixture", Gate: g.Calls(), Tools: offer.Tools, Thinking: "off", Policy: runcontext.PolicyLayer{"records": map[string]any{"retainDays": 36500}},
				Prompt: "Read the schema.", AllowConversation: true, Interactive: true, Critic: "off",
				Context: &runcontext.Input{
					CurrentRequest: &runcontext.CurrentRequest{Message: "Read the schema.", Input: "Read the schema."},
					Connection:     "provider", ConnectionRevisions: map[string]string{"provider": "one"},
					RunStore: &runcontext.DurableRunInput{Store: store, ID: "run-read"},
				},
				Emit: func(event contract.RunEvent) { events = append(events, event) },
			}
			var implementation adapter.Adapter
			if engine == "native" {
				implementation = native.New()
				run.Agent = &adapter.Agent{Model: "fixture", Run: func(ctx context.Context, request adapter.AgentRequest, cb adapter.AgentCallbacks) error {
					alias := ""
					for _, offered := range request.Tools {
						if strings.Contains(offered.Description, "Read a schema.") {
							alias = offered.Name
						}
					}
					answer, err := cb.Tool(ctx, adapter.AgentToolCall{ID: "call-read", Name: alias, Arguments: json.RawMessage(`{}`)})
					if err != nil {
						return err
					}
					given = append(given, answer.Text)
					return cb.Event(ctx, adapter.AgentEvent{Type: "message", ID: "final", Text: "Done.", Phase: "final"})
				}}
			} else {
				implementation = eino.New()
				run.Model = func(_ context.Context, _ string, request relay.Request) (*http.Response, error) {
					requests = append(requests, string(request.Body))
					if len(requests) == 1 {
						return readKeyCall("call_read", "get_schema", `{}`), nil
					}
					return quarantineFinal(), nil
				}
			}
			if err := implementation.Run(legContext(t), run); err != nil {
				t.Fatal(err)
			}
			if runtime.calls != 1 {
				t.Fatalf("the Runtime was called %d times", runtime.calls)
			}
			results := 0
			for _, event := range events {
				if event.ToolResult == nil {
					continue
				}
				results++
				text, _ := json.Marshal(event.ToolResult)
				if strings.Contains(string(text), recordedRawPath) || !strings.Contains(string(text), fmt.Sprintf("%q", want)[1:len(fmt.Sprintf("%q", want))-1]) {
					t.Fatalf("a tool_result event gave %s", text)
				}
			}
			if results != 1 {
				t.Fatalf("%d tool_result events", results)
			}
			if engine == "native" {
				// The agent is given the structured content, framed as data under the run's marker.
				lines := strings.Split(strings.Join(given, ""), "\n")
				if len(given) != 1 || len(lines) != 3 || lines[1] != `{"structuredContent":`+want+`}` || strings.Contains(given[0], recordedRawPath) {
					t.Fatalf("the agent was given %q", given)
				}
			} else if len(requests) != 2 || strings.Contains(requests[1], recordedRawPath) || !strings.Contains(requests[1], "[redacted: local-temp-path]") {
				t.Fatalf("the model was given %q", requests)
			}
			head, err := store.Load(context.Background(), "run-read")
			if err != nil || head == nil || strings.Contains(string(head.Checkpoint), recordedRawPath) {
				t.Fatalf("the stored run: %v", err)
			}
		})
	}
}
