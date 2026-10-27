package eino_test

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/eino"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/turn"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
	"github.com/Judgment-Pack/judgment-pack-agent/go/relay"
	"io"
	"net/http"
	"strings"
	"testing"
)

const numericSource = `{"id":"p","facts":{"amount":1.20,"zero":-0,"nested":{"amount":2.500},"exponent":1e2}}`

func numericReply(family, args, text string) *http.Response {
	quoted, _ := json.Marshal(text)
	var body string
	switch family {
	case "anthropic":
		content := `{"type":"text","text":` + string(quoted) + `}`
		stop := "end_turn"
		if args != "" {
			content = `{"type":"tool_use","id":"p1","name":"propose_pack","input":` + args + `}`
			stop = "tool_use"
		}
		body = `{"id":"a","type":"message","role":"assistant","model":"fixture","content":[` + content + `],"stop_reason":"` + stop + `","usage":{"input_tokens":1,"output_tokens":1}}`
	case "gemini":
		part := `{"text":` + string(quoted) + `}`
		if args != "" {
			part = `{"functionCall":{"name":"propose_pack","args":` + args + `}}`
		}
		body = `{"candidates":[{"content":{"role":"model","parts":[` + part + `]},"finishReason":"STOP"}]}`
	default:
		message := `{"role":"assistant","content":` + string(quoted) + `}`
		finish := "stop"
		if args != "" {
			encoded, _ := json.Marshal(args)
			message = `{"role":"assistant","content":null,"tool_calls":[{"id":"p1","type":"function","function":{"name":"propose_pack","arguments":` + string(encoded) + `}}]}`
			finish = "tool_calls"
		}
		body = `{"id":"a","object":"chat.completion","model":"fixture","choices":[{"index":0,"message":` + message + `,"finish_reason":"` + finish + `"}]}`
	}
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
}
func numericRun(t *testing.T, family, args, text string) ([]contract.RunEvent, int) {
	t.Helper()
	return numericRunReplies(t, family, args, func(int) string { return text })
}
func numericRunReplies(t *testing.T, family, args string, answer func(int) string) ([]contract.RunEvent, int) {
	t.Helper()
	var events []contract.RunEvent
	runtime := func(_ context.Context, raw json.RawMessage) (json.RawMessage, error) {
		var frame struct {
			ID     json.RawMessage
			Method string
		}
		_ = json.Unmarshal(raw, &frame)
		if frame.Method == "notifications/initialized" {
			return nil, nil
		}
		return json.RawMessage(`{"jsonrpc":"2.0","id":` + string(frame.ID) + `,"result":{"tools":[]}}`), nil
	}
	emit := func(event contract.RunEvent) { events = append(events, event) }
	g := gate.New(gate.Options{Runtime: runtime, Granted: gate.Ceiling, Emit: emit})
	tool := runcontext.HostTool{Name: "propose_pack", Taxonomy: &runcontext.ToolTaxonomy{Operation: "propose", Effect: "propose"}, Description: "Propose.", Effect: "proposal", InputSchema: turn.ProposePackSchema, Execute: func(context.Context, json.RawMessage) (runcontext.ToolResult, error) {
		t.Error("host execute reached")
		return runcontext.Answer(runcontext.Object()), nil
	}}
	declared, err := runcontext.OfferedHostTools([]runcontext.HostTool{tool}, gate.Ceiling, runcontext.Defaults)
	if err != nil {
		t.Fatal(err)
	}
	offer, err := g.Open(context.Background(), declared)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	model := func(_ context.Context, _ string, _ relay.Request) (*http.Response, error) {
		calls++
		if args != "" && calls == 1 {
			return numericReply(family, args, ""), nil
		}
		return numericReply(family, "", answer(calls)), nil
	}
	run := &adapter.Run{Wire: family, ModelName: "fixture", Model: model, Thinking: "off", Prompt: "Draft a pack.", Critic: "off", Interactive: true, AllowConversation: true, Gate: g.Calls(), Tools: offer.Tools, HostTools: []runcontext.HostTool{tool}, Emit: emit, Context: &runcontext.Input{CurrentRequest: &runcontext.CurrentRequest{Message: "Draft a pack.", Input: "Draft a pack."}}}
	if err := eino.New().Run(legContext(t), run); err != nil {
		t.Fatal(err)
	}
	return events, calls
}
func TestProposalNumericActualAPIAndNative(t *testing.T) {
	for _, family := range []string{"openai-compatible", "anthropic", "gemini"} {
		t.Run(family, func(t *testing.T) {
			args, _ := json.Marshal(map[string]string{"documentJson": numericSource})
			events, calls := numericRun(t, family, string(args), "Done.")
			if calls != 2 {
				t.Fatalf("native calls %d", calls)
			}
			count, retained := 0, false
			for _, event := range events {
				if event.Proposal != nil {
					count++
					var compact bytes.Buffer
					_ = json.Compact(&compact, event.Proposal.Document["facts"])
					facts := compact.String()
					if facts != `{"amount":1.2,"zero":0,"nested":{"amount":2.5},"exponent":100}` {
						t.Fatalf("accepted values: %s", facts)
					}
				}
				if event.ExecutionCheckpoint != nil {
					for _, entry := range event.ExecutionCheckpoint.Checkpoint.Context.Entries {
						var v struct{ Text string }
						if json.Unmarshal(entry.Value, &v) == nil && v.Text != "" {
							canonical, err := turn.ReadProposalDocument(json.RawMessage(v.Text))
							if err != nil {
								t.Fatal(err)
							}
							if strings.Contains(string(canonical), `"facts":{"amount":1.2,"zero":0,"nested":{"amount":2.5},"exponent":100}`) {
								retained = true
							}
						}
					}
				}
			}
			if count != 1 || !retained {
				t.Fatalf("native custody count=%d retained=%v", count, retained)
			}
			for _, value := range []string{"9007199254740993", "9007199254740991.1", "1e400"} {
				events, _ := numericRun(t, family, `{"document":{"id":"p","n":`+value+`}}`, "Done.")
				refused := false
				for _, event := range events {
					if event.Proposal != nil {
						t.Fatal("legacy native source admitted")
					}
					if event.ToolResult != nil && event.ToolResult.IsError && strings.Contains(event.ToolResult.Text, "documentJson") {
						refused = true
					}
				}
				if !refused {
					t.Fatalf("legacy migration refusal missing for %s", value)
				}
			}
			for _, test := range []struct {
				source   string
				accepted bool
			}{{numericSource, true}, {`{"id":"p","n":9007199254740993}`, false}} {
				events, _ := numericRun(t, family, "", "```json\n{\"proposal\":{\"document\":"+test.source+"}}\n```")
				count := 0
				refused := false
				for _, event := range events {
					if event.Proposal != nil {
						count++
					}
					if event.Error != nil && strings.Contains(event.Error.Message, "numerical value changes") {
						refused = true
					}
				}
				if test.accepted && count != 1 || !test.accepted && (count != 0 || !refused) {
					t.Fatalf("fenced acceptance=%v count=%d refused=%v", test.accepted, count, refused)
				}
			}
		})
	}
}

func TestProposalNumericTerminalRecovery(t *testing.T) {
	fence := func(document string) string { return "```json\n{\"proposal\":{\"document\":" + document + "}}\n```" }
	for _, family := range []string{"openai-compatible", "anthropic", "gemini"} {
		for _, test := range []struct{ name, source, detail string }{
			{"numeric loss", `{"id":"p","n":9007199254740993}`, "numerical value changes"},
			{"exponent bound", `{"id":"p","n":0e10000000}`, "work bound"},
			{"depth bound", `{"id":"p","n":` + strings.Repeat("[", 128) + "0" + strings.Repeat("]", 128) + "}", "work bound"},
			{"depth beyond generic parser limit", `{"id":"p","n":` + strings.Repeat("[", 10001) + "0" + strings.Repeat("]", 10001) + "}", "work bound"},
		} {
			t.Run(family+"/"+test.name, func(t *testing.T) {
				events, calls := numericRunReplies(t, family, "", func(at int) string {
					if at == 1 {
						return fence(test.source)
					}
					return fence(`{"id":"p","n":2}`)
				})
				if calls != 1 {
					t.Fatalf("terminal refusal made %d model requests; want one", calls)
				}
				refused := false
				for _, event := range events {
					if event.Proposal != nil {
						t.Fatal("terminal source refusal installed a replacement proposal")
					}
					if event.ResponseRecovery != nil {
						t.Fatal("terminal source refusal started automatic reformat")
					}
					if event.Error != nil && strings.Contains(event.Error.Message, test.detail) {
						refused = true
					}
				}
				if !refused {
					t.Fatal("typed source refusal missing")
				}
			})
		}
		t.Run(family+"/malformed positive control", func(t *testing.T) {
			events, calls := numericRunReplies(t, family, "", func(at int) string {
				if at == 1 {
					return "```json\n{\"proposal\":{\"document\":oops}}\n```"
				}
				return fence(`{"id":"p","n":2}`)
			})
			count := 0
			for _, event := range events {
				if event.Proposal != nil {
					count++
				}
			}
			if calls != 2 || count != 1 {
				t.Fatalf("malformed reformat calls=%d proposals=%d", calls, count)
			}
		})
	}
}
