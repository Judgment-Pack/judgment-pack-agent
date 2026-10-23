package eino_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/eino"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
	"github.com/Judgment-Pack/judgment-pack-agent/go/relay"
)

// A host tool the run declares and the gate's offer does not hold is held to the phase by the
// gate's handle (AdmitHost) before the host runs it: the model's call of it reads the gate's
// refusal, a guardrail is written between the call and its result, and the host's function is
// never reached.
func TestAHostToolTheGateDoesNotAdmitIsNotRun(t *testing.T) {
	runtime := func(_ context.Context, frame json.RawMessage) (json.RawMessage, error) {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.Unmarshal(frame, &request); err != nil {
			return nil, err
		}
		switch request.Method {
		case "notifications/initialized":
			return nil, nil
		case "tools/list":
			return json.RawMessage(`{"jsonrpc":"2.0","id":` + string(request.ID) + `,"result":{"tools":[{"name":"get_schema","inputSchema":{"type":"object"}}]}}`), nil
		}
		return json.RawMessage(`{"jsonrpc":"2.0","id":` + string(request.ID) + `,"result":{}}`), nil
	}
	var mu sync.Mutex
	var events []contract.RunEvent
	emit := func(event contract.RunEvent) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, event)
	}
	g := gate.New(gate.Options{Runtime: runtime, Granted: gate.Ceiling, Emit: emit})
	offer, err := g.Open(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ran := false
	note := runcontext.HostTool{
		Name: "write_note", Description: "Writes a note.", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		Execute: func(context.Context, json.RawMessage) (runcontext.ToolResult, error) {
			ran = true
			return runcontext.ToolResult{}, nil
		},
	}
	var bodies []string
	model := func(_ context.Context, _ string, request relay.Request) (*http.Response, error) {
		bodies = append(bodies, string(request.Body))
		if len(bodies) == 1 {
			return openAIStream(map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "call_note", "type": "function",
				"function": map[string]any{"name": "write_note", "arguments": "{}"}}}}, "tool_calls"), nil
		}
		return openAIStream(map[string]any{"content": "Done."}, "stop"), nil
	}

	run := &adapter.Run{
		Wire: "openai-compatible", ModelName: "fixture", Model: model, Gate: g.Calls(), Tools: offer.Tools, Thinking: "off",
		Prompt: "Write a note.", AllowConversation: true, Interactive: true, Critic: "off", HostTools: []runcontext.HostTool{note},
		Context: &runcontext.Input{CurrentRequest: &runcontext.CurrentRequest{Message: "Write a note.", Input: "Write a note."}}, Emit: emit,
	}
	if err := eino.New().Run(legContext(t), run); err != nil {
		t.Fatal(err)
	}
	if ran {
		t.Fatal("the host ran a tool the gate does not admit")
	}
	if len(bodies) != 2 || !strings.Contains(bodies[0], `"write_note"`) {
		t.Fatalf("%d requests; the first offered write_note: %t", len(bodies), len(bodies) > 0 && strings.Contains(bodies[0], `"write_note"`))
	}
	var order []string
	refused := ""
	for _, event := range events {
		switch {
		case event.ToolCall != nil && event.ToolCall.Name == "write_note":
			order = append(order, "call")
		case event.Guardrail != nil && event.Guardrail.Tool == "write_note":
			order = append(order, "guardrail "+event.Guardrail.Action)
		case event.ToolResult != nil && event.ToolResult.Name == "write_note":
			order = append(order, "result")
			refused = string(mustMarshal(t, event.ToolResult))
		}
	}
	if strings.Join(order, ", ") != "call, guardrail refused, result" {
		t.Fatalf("the events were %v", order)
	}
	if !strings.Contains(bodies[1], "write_note") || !strings.Contains(refused, "write_note") {
		t.Fatalf("the refusal the model read: %s", refused)
	}
}

func mustMarshal(t *testing.T, value any) []byte {
	t.Helper()
	out, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// openAIStream is one streamed answer on the OpenAI-compatible wire: a delta and its finish.
func openAIStream(delta map[string]any, finish string) *http.Response {
	frame := func(delta map[string]any, finish any) string {
		encoded, _ := json.Marshal(map[string]any{"id": "c", "object": "chat.completion.chunk", "model": "m",
			"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}})
		return "data: " + string(encoded) + "\n\n"
	}
	body := frame(map[string]any{"role": "assistant", "content": nil}, nil) + frame(delta, nil) + frame(map[string]any{}, finish) + "data: [DONE]\n\n"
	return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}
}

// Run.Review is the native spelling of the retired adversarialReview input.
func TestRetiredReviewRefusesPresence(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		events := []contract.RunEvent{}
		emit := func(event contract.RunEvent) { events = append(events, event) }
		g := gate.New(gate.Options{Runtime: func(_ context.Context, raw json.RawMessage) (json.RawMessage, error) {
			var request struct {
				ID json.RawMessage `json:"id"`
			}
			json.Unmarshal(raw, &request)
			return json.Marshal(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"tools": []any{}}})
		}, Granted: gate.Ceiling, Emit: emit})
		offer, err := g.Open(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		calls := 0
		run := &adapter.Run{Wire: "openai-compatible", ModelName: "fixture", Thinking: "off", Critic: "on", Review: &legacy, Prompt: "Current request", Gate: g.Calls(), Tools: offer.Tools, Emit: emit, Model: func(context.Context, string, relay.Request) (*http.Response, error) { calls++; return nil, nil }}
		if err = eino.New().Run(context.Background(), run); err != nil {
			t.Fatal(err)
		}
		refused := 0
		guided := false
		for _, event := range events {
			if event.Guardrail != nil && strings.Contains(event.Guardrail.Detail, "use critic") {
				guided = true
			}
			if event.Error != nil {
				refused++
				if event.Error.Failure == nil || event.Error.Failure.Code != "configuration" {
					t.Fatal("wrong alias refusal")
				}

			}
		}
		if calls != 0 || refused != 1 || !guided {
			t.Fatal("retired Review dispatched")
		}
	}
}
