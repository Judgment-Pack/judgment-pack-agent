package eino_test

// #271: a read's cached answer belongs to the bytes that were run. This drives both Go adapters the
// way a provider does: the model peer writes each call's argument text and the host tool records the
// bytes it was run with. Before the fix each call of a pair below was served the first one's answer,
// from the run's cache and from the chat's, without being run. go/context/read_arguments_test.go holds
// the wrapper itself; this holds that the arguments reach it, and how, on each adapter.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
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

// readKeyCall is one streamed completion whose call carries exactly the argument text given.
func readKeyCall(id, name, arguments string) *http.Response {
	chunk := map[string]any{"id": "fixture", "object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": id, "type": "function", "function": map[string]any{"name": name, "arguments": arguments}}}}, "finish_reason": "tool_calls"}}}
	body, _ := json.Marshal(chunk)
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: " + string(body) + "\n\ndata: [DONE]\n\n"))}
}

// readKeyRun runs one session whose model makes the calls given in order, and answers what the tool
// was run with and what each call was answered.
func readKeyRun(t *testing.T, engine string, chat *runcontext.ChatContext, policy runcontext.PolicyLayer, calls []string) (executed, answered []string) {
	t.Helper()
	tool := runcontext.HostTool{
		Name: "lookup", Description: "lookup: read one record", Effect: "read",
		InputSchema:   json.RawMessage(`{"type":"object"}`),
		Taxonomy:      &runcontext.ToolTaxonomy{Operation: "read", Effect: "read"},
		ContextPolicy: &runcontext.ToolContextPolicy{Immutable: true},
		Execute: func(_ context.Context, raw json.RawMessage) (runcontext.ToolResult, error) {
			executed = append(executed, string(raw))
			return runcontext.ResultOf([]runcontext.Content{runcontext.TextContent("answer to " + string(raw))}, nil), nil
		},
	}
	g := gate.New(gate.Options{})
	g.SetOffer(contract.ToolOffer{})
	input := &runcontext.Input{CurrentRequest: &runcontext.CurrentRequest{Message: "Read.", Input: "Read."}}
	if chat != nil {
		input.Chat = chat
	}
	r := &adapter.Run{Prompt: "Read.", Wire: "openai-compatible", ModelName: "fixture", Thinking: "off", Critic: "off", AllowConversation: true, Interactive: true, Gate: g.Calls(), HostTools: []runcontext.HostTool{tool}, Policy: policy, Context: input,
		Emit: func(e contract.RunEvent) {
			if e.ToolResult != nil {
				answered = append(answered, e.ToolResult.Text)
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
				return quarantineFinal(), nil
			}
			return readKeyCall(fmt.Sprintf("call-%d", requests), tool.Name, calls[requests-1]), nil
		}
	}
	if err := implementation.Run(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	return executed, answered
}

// readKeyChats is each cache a run's reads can be served from.
func readKeyChats() map[string]*runcontext.ChatContext {
	return map[string]*runcontext.ChatContext{"run": nil, "chat": runcontext.NewChatContext("read-keys")}
}

// Every pair here is an object, so a model tool call delivers it on both adapters, and each call of
// it is run and answered for itself.
func TestAModelToolCallIsAnsweredForItsOwnArguments(t *testing.T) {
	pairs := []struct{ name, first, second string }{
		{"out of range", `{"q":1e400}`, `{"q":2e400}`},
		{"past a float64", `{"id":9007199254740993}`, `{"id":9007199254740992}`},
		{"repeated member", `{"a":{"x":1},"a":{"y":2}}`, `{"a":{"y":2}}`},
		{"repeated member, escaped", `{"a":1,"` + string(rune(0x5c)) + `u0061":2}`, `{"a":2}`},
		{"spelling", `{"q":1.0}`, `{"q":1}`},
		{"negative zero", `{"q":-0}`, `{"q":0}`},
		{"underflow", `{"q":1e-400}`, `{"q":0}`},
		{"upper-case exponent", `{"q":1E+21}`, `{"q":1e+21}`},
		{"names that fold together, as a struct reader reads them", `{"ID":1,"id":2}`, `{"id":2,"ID":1}`},
		{"Kelvin sign and k", `{"` + string(rune(0x212a)) + `":1,"k":2}`, `{"k":2,"` + string(rune(0x212a)) + `":1}`},
		{"long s and s", `{"` + string(rune(0x017f)) + `":1,"s":2}`, `{"s":2,"` + string(rune(0x017f)) + `":1}`},
	}
	for _, engine := range []string{"native", "api"} {
		for scope, chat := range readKeyChats() {
			for _, pair := range pairs {
				t.Run(engine+"/"+scope+"/"+pair.name, func(t *testing.T) {
					if chat != nil {
						chat = runcontext.NewChatContext("read-keys")
					}
					executed, answered := readKeyRun(t, engine, chat, nil, []string{pair.first, pair.second})
					want := []string{"answer to " + pair.first, "answer to " + pair.second}
					if fmt.Sprint(answered) != fmt.Sprint(want) {
						t.Fatalf("answered %q", answered)
					}
					if fmt.Sprint(executed) != fmt.Sprint([]string{pair.first, pair.second}) {
						t.Fatalf("executed %q", executed)
					}
				})
			}
		}
	}
}

// The native adapter hands on what the agent wrote, with no check that it is an object, so the issue's
// [1] and [2] reach the wrapper there. The API adapter refuses them first and runs nothing.
func TestArgumentsThatAreNotAnObjectAreAnsweredForThemselves(t *testing.T) {
	calls := []string{`[1]`, `[2]`, `[1]`}
	for scope, chat := range readKeyChats() {
		t.Run("native/"+scope, func(t *testing.T) {
			executed, answered := readKeyRun(t, "native", chat, nil, calls)
			if fmt.Sprint(executed) != fmt.Sprint(calls) || fmt.Sprint(answered) != fmt.Sprint([]string{"answer to [1]", "answer to [2]", "answer to [1]"}) {
				t.Fatalf("executed %q, answered %q", executed, answered)
			}
		})
		t.Run("api/"+scope, func(t *testing.T) {
			executed, answered := readKeyRun(t, "api", chat, nil, calls)
			if len(executed) != 0 || len(answered) != len(calls) || !strings.Contains(answered[1], "not a JSON object") {
				t.Fatalf("executed %q, answered %q", executed, answered)
			}
		})
	}
}

// Only with the host's output guard off does text that is not one JSON value reach the wrapper, on the
// native adapter; it used to read as {}, so it was answered for an ordinary {}.
func TestTextThatIsNotJSONIsNotAnsweredForAnEmptyObject(t *testing.T) {
	for scope, chat := range readKeyChats() {
		t.Run(scope, func(t *testing.T) {
			calls := []string{`{}`, `{bad`, `{"q":1} {"q":2}`, `{"q":1} ]`, `{"q":2} ]`}
			executed, answered := readKeyRun(t, "native", chat, runcontext.PolicyLayer{"outputGuard": "off"}, calls)
			if fmt.Sprint(executed) != fmt.Sprint(calls) {
				t.Fatalf("executed %q, answered %q", executed, answered)
			}
		})
	}
}

// The fix turns no cache off: ordinary arguments, the same again, are served without a call.
func TestAnOrdinaryReadIsStillServedFromTheCache(t *testing.T) {
	calls := []string{`{"id":7,"ratio":1.5}`, `{ "id": 7, "ratio": 1.5 }`, "\n" + `{"id":7,"ratio":1.5}`, "\t" + `{"id":7,"ratio":1.5}`}
	for _, engine := range []string{"native", "api"} {
		for scope, chat := range readKeyChats() {
			t.Run(engine+"/"+scope, func(t *testing.T) {
				if chat != nil {
					chat = runcontext.NewChatContext("read-keys")
				}
				executed, answered := readKeyRun(t, engine, chat, nil, calls)
				if len(executed) != 1 || len(answered) != len(calls) {
					t.Fatalf("executed %q, answered %q", executed, answered)
				}
				for _, answer := range answered {
					if answer != answered[0] {
						t.Fatalf("executed %q, answered %q", executed, answered)
					}
				}
			})
		}
	}
}

// A stored chat a published release wrote (#271): contract/vectors/chat/context-snapshot-v1-cache.json
// is byte for byte what the v0.12.0 tag's code writes. Its cache rows hold other arguments' answers
// under the keys of these calls, so restored, each call runs and is answered for itself; the snapshot
// written next is the current version, and its rows are then served.
func TestAChatStoredBeforeTheFixServesNoOldAnswer(t *testing.T) {
	raw, err := os.ReadFile("../../../contract/vectors/chat/context-snapshot-v1-cache.json")
	if err != nil {
		t.Fatal(err)
	}
	vector := runcontext.MustParseJSON(string(raw))
	calls, answers := []string{}, []string{}
	for _, row := range vector.Get("reads").Items() {
		calls = append(calls, row.Get("call").Str())
		answers = append(answers, "answer to "+row.Get("call").Str())
	}
	written := []byte(runcontext.CanonicalJSON(vector.Get("items").Items()[0]))
	for _, engine := range []string{"native", "api"} {
		t.Run(engine, func(t *testing.T) {
			chat, err := runcontext.RestoreChatContext(written)
			if err != nil {
				t.Fatal(err)
			}
			executed, answered := readKeyRun(t, engine, chat, nil, calls)
			if fmt.Sprint(executed) != fmt.Sprint(calls) || fmt.Sprint(answered) != fmt.Sprint(answers) {
				t.Fatalf("restored from the published snapshot: executed %q, answered %q", executed, answered)
			}
			next, err := chat.Snapshot()
			if err != nil {
				t.Fatal(err)
			}
			if version := runcontext.MustParseJSON(string(next)).Get("version").Num(); version != 2 {
				t.Fatalf("the next snapshot is version %v", version)
			}
			again, err := runcontext.RestoreChatContext(next)
			if err != nil {
				t.Fatal(err)
			}
			executed, answered = readKeyRun(t, engine, again, nil, calls)
			if len(executed) != 0 || fmt.Sprint(answered) != fmt.Sprint(answers) {
				t.Fatalf("restored from the current snapshot: executed %q, answered %q", executed, answered)
			}
		})
	}
}
