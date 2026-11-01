package eino_test

// #275: a write under a run store is answered from its record only where it is that write. This drives
// both Go adapters the way a provider does: the model peer writes each call's argument text and the
// host tool records the bytes it was run with. Before the fix the second call of each pair below was
// not run and was answered with the first's result, while the caller was told it succeeded; a repeated
// name or an out-of-range number ended the whole run with a storage failure instead.
// go/context/durable_writes_test.go holds the writer itself and a resume over a published checkpoint.

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

// durableWires is every way a model's call reaches the Go boundary: the native adapter, whose host hands on
// what the agent wrote; the API adapter on each family's stream and whole answer; and the text path, on an
// endpoint that takes no native tool calls.
var durableWires = []string{"native", "api", "api/openai-compatible/whole", "api/anthropic/sse", "api/anthropic/whole", "api/gemini/sse", "api/gemini/whole", "api/text"}

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
		r.Model = func(_ context.Context, _ string, _ relay.Request) (*http.Response, error) {
			requests++
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

func TestADurableWriteIsNeverAnsweredFromAnotherWritesRecord(t *testing.T) {
	kelvin := string(rune(0x212a))
	ran := func(call string) string { return "ran " + call }
	big, near := `{"id":9007199254740993}`, `{"id":9007199254740992}`
	for _, row := range []struct {
		name         string
		calls        []string
		ran, answers []string
	}{
		// Different numbers: each is its own write, a legitimate integer past 2^53 among them.
		{"an integer past 2^53, then its neighbour", []string{big, near}, []string{big, near}, []string{ran(big), ran(near)}},
		{"an integer past 2^53, its neighbour first", []string{near, big}, []string{near, big}, []string{ran(near), ran(big)}},
		{"an underflow, then zero", []string{`{"q":1e-400}`, `{"q":0}`}, []string{`{"q":1e-400}`, `{"q":0}`}, []string{ran(`{"q":1e-400}`), ran(`{"q":0}`)}},
		// The same value written differently: it may be that write and may not be. Neither run nor answered.
		{"1.0 then 1", []string{`{"q":1.0}`, `{"q":1}`}, []string{`{"q":1.0}`}, []string{ran(`{"q":1.0}`), conflictRefusal}},
		{"1 then 1.0", []string{`{"q":1}`, `{"q":1.0}`}, []string{`{"q":1}`}, []string{ran(`{"q":1}`), conflictRefusal}},
		{"-0 then 0", []string{`{"q":-0}`, `{"q":0}`}, []string{`{"q":-0}`}, []string{ran(`{"q":-0}`), conflictRefusal}},
		{"0 then -0", []string{`{"q":0}`, `{"q":-0}`}, []string{`{"q":0}`}, []string{ran(`{"q":0}`), conflictRefusal}},
		{"names that fold together", []string{`{"ID":1,"id":2}`, `{"id":2,"ID":1}`}, []string{`{"ID":1,"id":2}`}, []string{ran(`{"ID":1,"id":2}`), conflictRefusal}},
		{"names that fold together, the other order first", []string{`{"id":2,"ID":1}`, `{"ID":1,"id":2}`}, []string{`{"id":2,"ID":1}`}, []string{ran(`{"id":2,"ID":1}`), conflictRefusal}},
		{"the Kelvin sign and k", []string{`{"` + kelvin + `":1,"k":2}`, `{"k":2,"` + kelvin + `":1}`}, []string{`{"` + kelvin + `":1,"k":2}`}, []string{ran(`{"` + kelvin + `":1,"k":2}`), conflictRefusal}},
		// Not recordable as written: refused, and the run goes on. The next call is its own write.
		{"a repeated name, then the name it reads as", []string{`{"a":{"x":1},"a":{"y":2}}`, `{"a":{"y":2}}`}, []string{`{"a":{"y":2}}`}, []string{unrecordable, ran(`{"a":{"y":2}}`)}},
		{"a number out of range, twice", []string{`{"q":1e400}`, `{"q":2e400}`}, nil, []string{unrecordable, unrecordable}},
		// A true repeat is answered from its record, and not run.
		{"the same bytes", []string{`{"id":7}`, `{"id":7}`}, []string{`{"id":7}`}, []string{ran(`{"id":7}`), ran(`{"id":7}`)}},
		{"the same bytes, past 2^53", []string{big, big}, []string{big}, []string{ran(big), ran(big)}},
		{"the same bytes, a spelling JSON.stringify does not write", []string{`{"q":1.0}`, `{"q":1.0}`}, []string{`{"q":1.0}`}, []string{ran(`{"q":1.0}`), ran(`{"q":1.0}`)}},
		{"the same bytes, names that fold together", []string{`{"ID":1,"id":2}`, `{"ID":1,"id":2}`}, []string{`{"ID":1,"id":2}`}, []string{ran(`{"ID":1,"id":2}`), ran(`{"ID":1,"id":2}`)}},
		{"another order and whitespace, which the fingerprint leaves out", []string{`{"a":1,"b":2}`, ` { "b" : 2 , "a" : 1 } `}, []string{`{"a":1,"b":2}`}, []string{ran(`{"a":1,"b":2}`), ran(`{"a":1,"b":2}`)}},
		// Each row is looked at: the record that is this write is found past one that only reads alike.
		{"the first of two neighbours again", []string{big, near, big, near}, []string{big, near}, []string{ran(big), ran(near), ran(big), ran(near)}},
	} {
		for _, engine := range durableWires {
			t.Run(engine+"/"+row.name, func(t *testing.T) {
				executed, answered, failures := durableRun(t, engine, true, row.calls)
				checkDurableRun(t, executed, answered, failures, row.ran, row.answers)
			})
		}
	}
}

// A read is never answered from a record: it runs each time it is asked. What cannot be recorded used
// to end the run here too; now the model reads the refusal and the run goes on.
func TestADurableReadIsRefusedOnlyWhereItCannotBeRecorded(t *testing.T) {
	ran := func(call string) string { return "ran " + call }
	for _, engine := range durableWires {
		t.Run(engine, func(t *testing.T) {
			calls := []string{`{"q":1.0}`, `{"q":1}`, `{"id":9007199254740993}`, `{"id":9007199254740992}`, `{"q":1e400}`, `{"a":1,"a":2}`, `{"id":7}`}
			executed, answered, failures := durableRun(t, engine, false, calls)
			checkDurableRun(t, executed, answered, failures, []string{calls[0], calls[1], calls[2], calls[3], calls[6]},
				[]string{ran(calls[0]), ran(calls[1]), ran(calls[2]), ran(calls[3]), unrecordable, unrecordable, ran(calls[6])})
		})
	}
}

// The model's text reaches the tool on every wire, read or write, with a run store or without one. The readers
// used to rewrite Anthropic's and Gemini's arguments from float64s, so the tool ran 9007199254740992 for
// 9007199254740993, null for 1e400, and 0 for 1e-400, which is the consumer's nested limit reaching its
// decoder as a value it accepts.
func TestEveryWireHandsTheToolTheModelsText(t *testing.T) {
	for _, engine := range durableWires {
		for _, write := range []bool{true, false} {
			for _, stored := range []bool{true, false} {
				t.Run(fmt.Sprintf("%s/write=%v/stored=%v", engine, write, stored), func(t *testing.T) {
					calls := []string{`{"id":9007199254740993}`, `{"limits":{"searches":1e-400}}`, `{"q":1.0}`, `{"q":"a\u00e9"}`, `{"b":2,"a":1}`, `{ "c" : 3 }`}
					if !stored {
						calls = append(calls, `{"q":1e400}`, `{"a":1,"a":2}`)
					}
					executed, answered, failures := durableRunOn(t, engine, write, stored, calls)
					if len(failures) != 0 || len(answered) != len(calls) {
						t.Fatalf("answered %q, failures %q", answered, failures)
					}
					if fmt.Sprint(executed) != fmt.Sprint(calls) {
						t.Fatalf("the tool ran %q, the model wrote %q", executed, calls)
					}
				})
			}
		}
	}
}
