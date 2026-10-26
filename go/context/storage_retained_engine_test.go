package runcontext_test

import (
	"context"
	"encoding/json"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/eino"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
	"github.com/Judgment-Pack/judgment-pack-agent/go/relay"
	"io"
	"net/http"
	"strings"
	"testing"
)

type retainedReadLink struct{ held **runcontext.SessionContext }

func (l retainedReadLink) Attach(c *runcontext.SessionContext) { *l.held = c }
func TestRetainedRepresentationActualEinoRequestBytesAfterResume(t *testing.T) {
	for _, profile := range []string{"strict", "callback"} {
		t.Run(profile, func(t *testing.T) {
			for _, original := range []string{`{"z":2,"a":{"last":1,"first":0}}`, `{"z":"é\n\\\"","a":{"last":1,"first":0}}`} {
				t.Run(original, func(t *testing.T) {
					var saved *contract.Checkpoint
					memory := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
					var observed []string
					for _, resume := range []bool{false, true} {
						var held *runcontext.SessionContext
						calls := 0
						var bodies [][]byte
						effect := "read"
						g := gate.New(gate.Options{})
						g.SetOffer(contract.ToolOffer{Tools: []contract.ToolOfferRuntimeTool{}, HostTools: []contract.ToolOfferHostTool{{Name: "read_fixture", Description: "Read retained source", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`), Effect: &effect}}})
						checkpoint := func(_ context.Context, cp contract.Checkpoint) error {
							raw, e := json.Marshal(cp)
							if e != nil {
								return e
							}
							var copy contract.Checkpoint
							if e = json.Unmarshal(raw, &copy); e != nil {
								return e
							}
							saved = &copy
							return nil
						}
						run := &adapter.Run{Wire: "openai-compatible", ModelName: "fixture", Thinking: "off", Prompt: "Read the fixture then answer.", AllowConversation: true, Critic: "off", Gate: g.Calls(), Tools: []contract.ToolOfferRuntimeTool{}, Context: &runcontext.Input{CurrentRequest: &runcontext.CurrentRequest{Message: "Read fixture", Input: "Read fixture"}, RunStore: &runcontext.DurableRunInput{Store: memory, ID: "retained-engine", Resume: resume}, Documents: []runcontext.Document{{ID: "opaque", Kind: "artifact", Label: "Ordered source", Value: runcontext.MustParseJSON(original)}}, Link: retainedReadLink{&held}}, HostTools: []runcontext.HostTool{{Name: "read_fixture", Taxonomy: &runcontext.ToolTaxonomy{Operation: "read", Effect: "read"}, Description: "Read retained source", Effect: "read", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`), Execute: func(context.Context, json.RawMessage) (runcontext.ToolResult, error) {
							var id string
							for _, entry := range held.Ledger.List(0).Get("items").Items() {
								if entry.Get("label").Str() == "Ordered source" {
									id = entry.Get("contextId").Str()
								}
							}
							read, err := held.Ledger.Read(runcontext.ObjectOf("id", runcontext.String(id)))
							if err != nil {
								return runcontext.ToolResult{}, err
							}
							text := read.Get("value").Text()
							if text != original {
								t.Fatal("read source bytes changed", text)
							}
							return runcontext.ResultOf([]runcontext.Content{runcontext.TextContent(text)}, nil), nil
						}}}, Emit: func(event contract.RunEvent) {
							if event.Error != nil {
								t.Errorf("engine refused retained resume: %s", event.Error.Message)
							}
						}, Model: func(_ context.Context, _ string, request relay.Request) (*http.Response, error) {
							bodies = append(bodies, append([]byte{}, request.Body...))
							calls++
							body := durableFinalResponse
							if calls == 1 {
								body = durableToolResponse
							}
							return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
						}}
						if profile == "callback" {
							run.Context.RunStore = nil
							run.Context.Checkpoint = checkpoint
							if resume {
								run.Context.Execution = saved
							}
						}
						if err := eino.New().Run(context.Background(), run); err != nil {
							t.Fatal(err)
						}
						if len(bodies) != 2 {
							t.Fatalf("expected2 actual provider requests, got%d", len(bodies))
						}
						body, err := runcontext.ParseJSON(bodies[1])
						if err != nil {
							t.Fatal(err)
						}
						var text string
						for _, message := range body.Get("messages").Items() {
							if message.Get("role").IsString() && message.Get("role").Str() == "tool" {
								data, ok := runcontext.UnframeData(message.Get("content").Str(), held.Marker)
								if !ok {
									t.Fatal("actual tool result lost trusted data frame")
								}
								result, e := runcontext.ParseJSON([]byte(data))
								if e != nil {
									t.Fatal(e)
								}
								for _, part := range result.Get("content").Items() {
									if part.Get("type").Str() == "text" {
										text = part.Get("text").Str()
									}
								}
							}
						}
						if text != original {
							t.Fatalf("actual provider tool-result source changed: %q", text)
						}
						observed = append(observed, text)
					}
					if observed[0] != observed[1] {
						t.Fatal("resumed provider request changed retained tool-result bytes")
					}
				})
			}
		})
	}
}
