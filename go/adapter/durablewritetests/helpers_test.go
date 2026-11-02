package durablewritetests

// Copied from go/adapter/durablewiretests and go/adapter/eino, whose tests use the same ones: a test binary
// cannot import another's test files (#254).

import (
	"context"
	"encoding/json"
	"fmt"
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

// durableRun runs one session under a run store whose model makes the calls given, in order, of a tool
// that writes or reads, and answers what the tool was run with, what each call was answered, and the
// failures the run itself ended with.
func durableRun(t *testing.T, engine string, write bool, calls []string) (executed, answered, failures []string) {
	t.Helper()
	return durableRunOn(t, engine, write, true, calls)
}

// durableTableWires run the whole table of pairs: the decision is the writer's, and each family's reader is
// held to the bytes by TestEveryWireHandsTheToolTheModelsText. These run in the race job, so the rest of the
// wires run only the pairs that went wrong on them (durableFamilyRows).
var durableTableWires = []string{"native", "api"}

// durableFamilyWires are one way of each other family, for the pairs a reader that rewrites gets wrong.
var durableFamilyWires = []string{"api/anthropic/sse", "api/gemini/whole", "api/text"}

var durableFamilyRows = map[string]bool{"an integer past 2^53, its neighbour first": true, "1.0 then 1": true, "a consumer's nested limit: zero, then an underflow its decoder must see": true,
	"a number out of range, twice": true, "a repeated name, then the name it reads as": true}

// wireReply is one answer of the wire named: a call with exactly the argument text given, or, with none, the
// final answer. Anthropic's stream splits the text in two, as a provider does.
func wireReply(wire, id, name, arguments string) *http.Response {
	sse := func(body string) *http.Response {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}
	}
	whole := func(body string) *http.Response {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
	}
	quoted := func(text string) string {
		raw, _ := json.Marshal(text)
		return string(raw)
	}
	switch wire {
	case "api/openai-compatible/whole":
		if arguments == "" {
			return whole(`{"id":"c","object":"chat.completion","model":"fixture","choices":[{"index":0,"message":{"role":"assistant","content":"Done."},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
		}
		return whole(`{"id":"c","object":"chat.completion","model":"fixture","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"` + id + `","type":"function","function":{"name":"` + name + `","arguments":` + quoted(arguments) + `}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	case "api/anthropic/whole":
		if arguments == "" {
			return whole(`{"id":"a","type":"message","role":"assistant","model":"fixture","content":[{"type":"text","text":"Done."}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
		}
		return whole(`{"id":"a","type":"message","role":"assistant","model":"fixture","content":[{"type":"tool_use","id":"` + id + `","name":"` + name + `","input":` + arguments + `}],"stop_reason":"tool_use","usage":{"input_tokens":1,"output_tokens":1}}`)
	case "api/anthropic/sse":
		event := func(name, data string) string { return "event: " + name + "\ndata: " + data + "\n\n" }
		head := event("message_start", `{"type":"message_start","message":{"id":"a","usage":{"input_tokens":1,"output_tokens":0}}}`)
		tail := func(reason string) string {
			return event("message_delta", `{"type":"message_delta","delta":{"stop_reason":"`+reason+`"},"usage":{"output_tokens":1}}`) + event("message_stop", `{"type":"message_stop"}`)
		}
		if arguments == "" {
			return sse(head + event("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`) + event("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Done."}}`) + event("content_block_stop", `{"type":"content_block_stop","index":0}`) + tail("end_turn"))
		}
		half := len(arguments) / 2
		delta := func(text string) string {
			return event("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":`+quoted(text)+`}}`)
		}
		return sse(head + event("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"`+id+`","name":"`+name+`","input":{}}}`) + delta(arguments[:half]) + delta(arguments[half:]) + event("content_block_stop", `{"type":"content_block_stop","index":0}`) + tail("tool_use"))
	case "api/gemini/whole", "api/gemini/sse":
		body := `{"candidates":[{"content":{"role":"model","parts":[{"text":"Done."}]},"finishReason":"STOP"}]}`
		if arguments != "" {
			body = `{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"id":"` + id + `","name":"` + name + `","args":` + arguments + `}}]},"finishReason":"STOP"}]}`
		}
		if wire == "api/gemini/sse" {
			return sse("data: " + body + "\n\n")
		}
		return whole(body)
	case "api/text":
		if arguments == "" {
			return quarantineFinal()
		}
		fence := "```json\n{\"tool\":\"" + name + "\",\"arguments\":" + arguments + "}\n```"
		return sse(`data: {"id":"fixture","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":` + quoted(fence) + `},"finish_reason":"stop"}]}` + "\n\ndata: [DONE]\n\n")
	}
	if arguments == "" {
		return quarantineFinal()
	}
	return readKeyCall(id, name, arguments)
}

// durableRunOn is durableRun on the wire named, with a run store or without one.
func durableRunOn(t *testing.T, engine string, write, stored bool, calls []string) (executed, answered, failures []string) {
	t.Helper()
	return durableRunWith(t, engine, write, stored, calls, nil)
}

// durableRunWith is durableRunOn, with what the next call writes decided from the request the model reads:
// next is given the request number and body and answers the call's text, or "" for the final answer.
func durableRunWith(t *testing.T, engine string, write, stored bool, calls []string, next func(int, string) string) (executed, answered, failures []string) {
	t.Helper()
	tool := runcontext.HostTool{
		Name: "change_record", Description: "change_record: change one record", Effect: "workflow",
		InputSchema: json.RawMessage(`{"type":"object"}`),
		Taxonomy:    &runcontext.ToolTaxonomy{Operation: "own-state", Effect: "pure"},
		Execute: func(_ context.Context, raw json.RawMessage) (runcontext.ToolResult, error) {
			executed = append(executed, string(raw))
			return runcontext.ResultOf([]runcontext.Content{runcontext.TextContent("ran " + string(raw))}, nil), nil
		},
	}
	if !write {
		tool.Effect = "read"
		tool.Taxonomy = &runcontext.ToolTaxonomy{Operation: "read", Effect: "read"}
	}
	g := gate.New(gate.Options{})
	g.SetOffer(contract.ToolOffer{})
	store := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
	input := &runcontext.Input{CurrentRequest: &runcontext.CurrentRequest{Message: "Change it.", Input: "Change it."}}
	if stored {
		input.Connection, input.ConnectionRevisions, input.RunStore = "provider", map[string]string{"provider": "one"}, &runcontext.DurableRunInput{Store: store, ID: "durable-writes"}
	}
	family := "openai-compatible"
	for _, name := range []string{"anthropic", "gemini"} {
		if strings.Contains(engine, name) {
			family = name
		}
	}
	r := &adapter.Run{Prompt: "Change it.", Wire: family, ModelName: "fixture", Thinking: "off", Critic: "off", AllowConversation: true, Interactive: true, Gate: g.Calls(), HostTools: []runcontext.HostTool{tool}, Context: input,
		Emit: func(e contract.RunEvent) {
			if e.ToolResult != nil {
				answered = append(answered, e.ToolResult.Text)
			}
			if e.Error != nil {
				raw, _ := json.Marshal(e.Error)
				failures = append(failures, string(raw))
			}
		},
		EnrollContext: func(_ context.Context, offer adapter.ContextOffer) error {
			return g.EnrollContext(offer.ID, offer.HostTools)
		}}
	var implementation adapter.Adapter
	if engine == "native" {
		implementation = native.New()
		r.Agent = &adapter.Agent{Model: "fixture", Run: func(ctx context.Context, request adapter.AgentRequest, cb adapter.AgentCallbacks) error {
			alias := ""
			for _, offered := range request.Tools {
				if strings.HasPrefix(offered.Description, tool.Name+": ") {
					alias = offered.Name
				}
			}
			if alias == "" {
				return fmt.Errorf("the native offer has no %s", tool.Name)
			}
			for n, arguments := range calls {
				if _, err := cb.Tool(ctx, adapter.AgentToolCall{ID: fmt.Sprintf("call-%d", n), Name: alias, Arguments: json.RawMessage(arguments)}); err != nil {
					return err
				}
			}
			return cb.Event(ctx, adapter.AgentEvent{Type: "message", ID: "final", Text: "Done.", Phase: "final"})
		}}
	} else {
		implementation = eino.New()
		requests := 0
		r.Model = func(_ context.Context, _ string, request relay.Request) (*http.Response, error) {
			requests++
			if next != nil {
				if text := next(requests, string(request.Body)); text != "" {
					return wireReply(engine, fmt.Sprintf("call-%d", requests), tool.Name, text), nil
				}
				return wireReply(engine, "", "", ""), nil
			}
			if requests > len(calls) {
				return wireReply(engine, "", "", ""), nil
			}
			return wireReply(engine, fmt.Sprintf("call-%d", requests), tool.Name, calls[requests-1]), nil
		}
		if engine == "api/text" {
			off := false
			r.Capabilities = &contract.CapabilityEndpointCapabilities{NativeToolCalls: &off}
		}
	}
	if err := implementation.Run(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	return executed, answered, failures
}

const (
	conflictRefusal = "DurableRequestConflict"
	unrecordable    = "DurableRequestUnrecordable"
)

// checkDurableRun holds one run: exactly these bytes were run, in order; each call was answered as
// wanted, its own answer, an earlier call's, or a named refusal; and the run went on to its end.
func checkDurableRun(t *testing.T, executed, answered, failures, ran, want []string) {
	t.Helper()
	if fmt.Sprint(executed) != fmt.Sprint(ran) {
		t.Fatalf("ran %q, want %q (answered %q, failures %q)", executed, ran, answered, failures)
	}
	if len(failures) != 0 {
		t.Fatalf("the run ended with %q", failures)
	}
	if len(answered) != len(want) {
		t.Fatalf("answered %q, want %q", answered, want)
	}
	for at := range want {
		if want[at] == conflictRefusal || want[at] == unrecordable {
			if !strings.Contains(answered[at], want[at]) {
				t.Fatalf("call %d was answered %q, want the refusal %s", at+1, answered[at], want[at])
			}
			continue
		}
		if answered[at] != want[at] {
			t.Fatalf("call %d was answered %q, want %q", at+1, answered[at], want[at])
		}
	}
}

// readKeyCall is one streamed completion whose call carries exactly the argument text given.
func readKeyCall(id, name, arguments string) *http.Response {
	chunk := map[string]any{"id": "fixture", "object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": id, "type": "function", "function": map[string]any{"name": name, "arguments": arguments}}}}, "finish_reason": "tool_calls"}}}
	body, _ := json.Marshal(chunk)
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: " + string(body) + "\n\ndata: [DONE]\n\n"))}
}

// quarantineFinal is one streamed completion that answers "Done." and stops.
func quarantineFinal() *http.Response {
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: {\"id\":\"fixture\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Done.\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"fixture\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))}
}
