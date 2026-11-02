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
		{"an integer past 2^53, its neighbour first", []string{near, big}, []string{near, big}, []string{ran(near), ran(big)}},
		{"zero, then an underflow", []string{`{"q":0}`, `{"q":1e-400}`}, []string{`{"q":0}`, `{"q":1e-400}`}, []string{ran(`{"q":0}`), ran(`{"q":1e-400}`)}},
		{"a consumer's nested limit: zero, then an underflow its decoder must see", []string{`{"limits":{"searches":0}}`, `{"limits":{"searches":1e-400}}`}, []string{`{"limits":{"searches":0}}`, `{"limits":{"searches":1e-400}}`}, []string{ran(`{"limits":{"searches":0}}`), ran(`{"limits":{"searches":1e-400}}`)}},
		// What a text the parsed form does not say reads as: it may be a copy from the model's history, so it is
		// refused, and a write run for it would be a second write on another id.
		{"an integer past 2^53, then its neighbour", []string{big, near}, []string{big}, []string{ran(big), conflictRefusal}},
		{"an underflow, then zero", []string{`{"q":1e-400}`, `{"q":0}`}, []string{`{"q":1e-400}`}, []string{ran(`{"q":1e-400}`), conflictRefusal}},
		{"past a float64's precision, then what it reads as", []string{`{"id":7.0000000000000000001}`, `{"id":7}`}, []string{`{"id":7.0000000000000000001}`}, []string{ran(`{"id":7.0000000000000000001}`), conflictRefusal}},
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
		{"the first of two neighbours again", []string{near, big, near, big}, []string{near, big}, []string{ran(near), ran(big), ran(near), ran(big)}},
	} {
		wires := durableTableWires
		if durableFamilyRows[row.name] {
			wires = append(append([]string{}, durableTableWires...), durableFamilyWires...)
		}
		for _, engine := range wires {
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
	for _, engine := range durableTableWires {
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
		// The reader comes before all of this, so each wire runs the two ways furthest apart.
		for _, way := range []struct{ write, stored bool }{{true, true}, {false, false}} {
			write, stored := way.write, way.stored
			t.Run(fmt.Sprintf("%s/write=%v/stored=%v", engine, write, stored), func(t *testing.T) {
				calls := []string{`{"id":9007199254740993}`, `{"limits":{"searches":1e-400}}`, `{"q":1.0}`, `{"q":"a` + string(rune(0x5c)) + `u00e9"}`, `{"b":2,"a":1}`, `{ "c" : 3 }`}
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

// durableAPIWays are the ways an API family writes a call back to the model on its next request.
var durableAPIWays = []string{"api", "api/openai-compatible/whole", "api/anthropic/sse", "api/anthropic/whole", "api/gemini/sse", "api/gemini/whole"}

// lastCallIn is the last call in a request body, as the model reads it: the text it has to copy.
func lastCallIn(body string) string {
	last := ""
	for _, key := range []string{`"arguments":"`, `"input":`, `"args":`} {
		at := strings.LastIndex(body, key)
		if at < 0 {
			continue
		}
		rest := body[at+len(key):]
		if key == `"arguments":"` {
			if end := strings.Index(rest, `"}`); end > 0 {
				var text string
				if json.Unmarshal([]byte(`"`+rest[:end]+`"`), &text) == nil {
					last = text
				}
			}
			continue
		}
		depth := 0
		for i, c := range rest {
			if c == '{' {
				depth++
			}
			if c == '}' {
				depth--
				if depth == 0 {
					last = rest[:i+1]
					break
				}
			}
		}
	}
	return last
}

// A model that repeats its last call exactly as its own request shows it makes one write, and is answered
// from the record. Every family used to write a call back from its parsed form, so the model read
// 9007199254740992 after 9007199254740993 ran: the writer took that for another write, and it ran.
func TestAModelThatCopiesItsHistoryMakesOneWrite(t *testing.T) {
	for _, way := range durableAPIWays {
		rows := []string{`{"id":9007199254740993}`, `{"amount":10.50}`}
		if way == "api" { // These run in the race job, and the reader is the same past these two.
			rows = append(rows, `{"id":12345678901234567}`, `{"o":[{"id":9007199254740993}]}`, `{"id":7}`)
		}
		for _, first := range rows {
			t.Run(way+"/"+first, func(t *testing.T) {
				executed, answered, failures := durableRunWith(t, way, true, true, nil, func(n int, body string) string {
					switch n {
					case 1:
						return first
					case 2, 3:
						return lastCallIn(body)
					}
					return ""
				})
				if len(failures) != 0 || fmt.Sprint(executed) != fmt.Sprint([]string{first}) {
					t.Fatalf("the tool ran %q, failures %q", executed, failures)
				}
				for _, text := range answered {
					if text != "ran "+first {
						t.Fatalf("answered %q", answered)
					}
				}
				if len(answered) != 3 {
					t.Fatalf("answered %q", answered)
				}
			})
		}
	}
}

// What the parsed form says is written back as it always was, so every other call's body is unchanged.
func TestWhatTheParsedFormSaysIsWrittenBackAsItWas(t *testing.T) {
	for _, way := range durableAPIWays {
		t.Run(way, func(t *testing.T) {
			var second string
			durableRunWith(t, way, true, true, nil, func(n int, body string) string {
				if n == 1 {
					return ` { "b" : 2 , "a" : 1 } `
				}
				second = body
				return ""
			})
			if strings.Contains(second, `"b" : 2`) || !strings.Contains(strings.ReplaceAll(second, `\"`, `"`), `{"b":2,"a":1}`) {
				t.Fatalf("the next request holds %s", second)
			}
		})
	}
}

// A resumed run's first request tells the model what it wrote, and a copy of that is answered from the record.
func TestAResumedRunsFirstRequestHoldsTheModelsText(t *testing.T) {
	big := `{"id":9007199254740993}`
	for _, way := range durableAPIWays {
		t.Run(way, func(t *testing.T) {
			store := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
			ran := []string{}
			family := "openai-compatible"
			for _, name := range []string{"anthropic", "gemini"} {
				if strings.Contains(way, name) {
					family = name
				}
			}
			once := func(resume bool, model func(int, string) (*http.Response, error)) {
				tool := runcontext.HostTool{Name: "change_record", Description: "change_record: change one record", Effect: "workflow", InputSchema: json.RawMessage(`{"type":"object"}`), Taxonomy: &runcontext.ToolTaxonomy{Operation: "own-state", Effect: "pure"},
					Execute: func(_ context.Context, raw json.RawMessage) (runcontext.ToolResult, error) {
						ran = append(ran, string(raw))
						return runcontext.ResultOf([]runcontext.Content{runcontext.TextContent("ran " + string(raw))}, nil), nil
					}}
				g := gate.New(gate.Options{})
				g.SetOffer(contract.ToolOffer{})
				input := &runcontext.Input{CurrentRequest: &runcontext.CurrentRequest{Message: "Change it.", Input: "Change it."}, Connection: "provider", ConnectionRevisions: map[string]string{"provider": "one"}, RunStore: &runcontext.DurableRunInput{Store: store, ID: "resume", Resume: resume}}
				n := 0
				r := &adapter.Run{Prompt: "Change it.", Wire: family, ModelName: "fixture", Thinking: "off", Critic: "off", AllowConversation: true, Interactive: true, Gate: g.Calls(), HostTools: []runcontext.HostTool{tool}, Context: input,
					Emit: func(contract.RunEvent) {},
					EnrollContext: func(_ context.Context, offer adapter.ContextOffer) error {
						return g.EnrollContext(offer.ID, offer.HostTools)
					},
					Model: func(_ context.Context, _ string, request relay.Request) (*http.Response, error) {
						n++
						return model(n, string(request.Body))
					}}
				_ = eino.New().Run(context.Background(), r)
			}
			once(false, func(n int, _ string) (*http.Response, error) {
				if n == 1 {
					return wireReply(way, "call-1", "change_record", big), nil
				}
				return nil, fmt.Errorf("the connection was lost")
			})
			var resumed string
			once(true, func(n int, body string) (*http.Response, error) {
				switch n {
				case 1:
					resumed = body
					return wireReply(way, "call-2", "change_record", lastCallIn(body)), nil
				}
				return wireReply(way, "", "", ""), nil
			})
			if !strings.Contains(resumed, "9007199254740993") || strings.Contains(resumed, "9007199254740992") {
				t.Fatalf("the resumed request holds %s", resumed)
			}
			if fmt.Sprint(ran) != fmt.Sprint([]string{big}) {
				t.Fatalf("the tool ran %q", ran)
			}
		})
	}
}
