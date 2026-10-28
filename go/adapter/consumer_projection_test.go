package adapter_test

import (
	"bytes"
	"context"
	"encoding/json"
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

func TestActualAdaptersProtectStructuredOpeningProjection(t *testing.T) {
	for _, engine := range []string{"eino", "native"} {
		for _, mode := range []string{"fits", "protected-oversize", "missing-path", "duplicate-path", "followup"} {
			t.Run(engine+"/"+mode, func(t *testing.T) {
				if engine == "native" && mode == "followup" {
					t.Skip("native provider loop remains inside one invocation")
				}
				original := `{"reviewed":{"request":"exact request","number":9007199254740993},"profiles":["review"],"searchBindings":[{"connection":"chosen","revision":"r1","model":"fixture"}],"limits":{"used":2,"maximum":4},"sourceIndex":["source-1"],"retained":{"id":"source-1","section":"selected"},"sources":[{"preview":"` + strings.Repeat("source text ", 30000) + `"}]}`
				if mode == "protected-oversize" {
					original = strings.Replace(original, "exact request", strings.Repeat("protected ", 30000), 1)
				}
				var baseline map[string]json.RawMessage
				if err := json.Unmarshal([]byte(original), &baseline); err != nil {
					t.Fatal(err)
				}
				paths := []string{"/sources/0/preview"}
				if mode == "missing-path" {
					paths = []string{"/sources/0/missing"}
				}
				if mode == "duplicate-path" {
					paths = append(paths, paths[0])
				}
				projected, physical, hostReads, executed := 0, 0, 0, 0
				var failures []contract.RunEvent
				g := gate.New(gate.Options{})
				g.SetOffer(contract.ToolOffer{})
				cap := 1
				run := &adapter.Run{Prompt: original, Wire: "openai-compatible", ModelName: "fixture", Thinking: "off", Critic: "off", AllowConversation: true, Gate: g.Calls(), Context: &runcontext.Input{RequestCap: &cap, RunStore: &runcontext.DurableRunInput{Store: runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{}), ID: "projection"}, CurrentRequest: &runcontext.CurrentRequest{Message: "Reviewed background operation", Input: original}}, Policy: runcontext.PolicyLayer{"budget": map[string]any{"contextWindowTokens": 16384, "outputTokens": 1024}}, Host: runcontext.HostFuncs{ProjectInstructionsFunc: func(context.Context) (string, error) { hostReads++; return "Preserve reviewed background fields.", nil }}, OpeningProjection: &runcontext.OpeningProjection{PreviewPaths: paths, Project: func(_ context.Context, input runcontext.OpeningProjectionInput) (map[string]string, error) {
					projected++
					if !bytes.Equal(input.Input, []byte(original)) {
						t.Fatal("projection did not receive complete original JSON")
					}
					if _, err := input.Fit(map[string]string{"/reviewed/request": "changed"}); err == nil {
						t.Fatal("protected replacement accepted")
					}
					fit, err := input.Fit(map[string]string{"/sources/0/preview": "source text "})
					if err != nil {
						return nil, err
					}
					if (mode == "fits" || mode == "followup") && !fit.Fits {
						t.Fatalf("complete candidate did not fit canonical allowance: %+v", fit)
					}
					input.Input[0] = '['
					return map[string]string{"/sources/0/preview": "source text "}, nil
				}}, Emit: func(event contract.RunEvent) { failures = append(failures, event) }}
				if mode == "followup" {
					cap = 2
					host := runcontext.HostTool{Name: "read_probe", Description: "Read the fixed probe.", Effect: "read", Taxonomy: &runcontext.ToolTaxonomy{Operation: "read", Effect: "read"}, InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`), Execute: func(context.Context, json.RawMessage) (runcontext.ToolResult, error) {
						executed++
						return runcontext.ResultOf([]runcontext.Content{runcontext.TextContent("Actual new tool result retained.")}, nil), nil
					}}
					run.HostTools = []runcontext.HostTool{host}
					g.SetOffer(contract.ToolOffer{HostTools: []contract.ToolOfferHostTool{host.Declaration()}})
					run.EnrollContext = func(_ context.Context, offer adapter.ContextOffer) error {
						return g.EnrollContext(offer.ID, offer.HostTools)
					}
				}
				check := func(text string) {
					physical++
					decoder := json.NewDecoder(strings.NewReader(text))
					var actual map[string]json.RawMessage
					if err := decoder.Decode(&actual); err != nil {
						t.Fatalf("provider received incomplete structured JSON: %v", err)
					}
					for _, key := range []string{"reviewed", "profiles", "searchBindings", "limits", "sourceIndex", "retained"} {
						if !bytes.Equal(actual[key], baseline[key]) {
							t.Fatalf("protected field changed: %s original=%s actual=%s", key, baseline[key], actual[key])
						}
					}
					if !strings.Contains(string(actual["sources"]), `"preview":"source text "`) {
						t.Fatal("permitted preview reduction absent")
					}
					if run.Prompt != original || run.Context.CurrentRequest.Input != original {
						t.Fatal("projection mutated approved host input")
					}
				}
				var implementation adapter.Adapter
				if engine == "native" {
					run.Context.RequestCap = nil
					implementation = native.New()
					run.Agent = &adapter.Agent{Model: "fixture", Run: func(ctx context.Context, request adapter.AgentRequest, cb adapter.AgentCallbacks) error {
						check(request.Prompt)
						return cb.Event(ctx, adapter.AgentEvent{Type: "message", Text: "Done.", Phase: "final"})
					}}
				} else {
					implementation = eino.New()
					run.Model = func(_ context.Context, _ string, request relay.Request) (*http.Response, error) {
						var body struct {
							Messages []struct {
								Role    string
								Content json.RawMessage
							}
						}
						if err := json.Unmarshal(request.Body, &body); err != nil {
							t.Fatal(err)
						}
						found := false
						for _, message := range body.Messages {
							if message.Role != "user" {
								continue
							}
							var text string
							if json.Unmarshal(message.Content, &text) == nil && strings.HasPrefix(text, "{") {
								check(text)
								found = true
							}
							var parts []struct{ Text string }
							if json.Unmarshal(message.Content, &parts) == nil {
								for _, part := range parts {
									if strings.HasPrefix(part.Text, "{") {
										check(part.Text)
										found = true
									}
								}
							}
						}
						if !found {
							t.Fatal("provider received no complete structured envelope")
						}
						if mode == "followup" {
							if physical == 1 {
								return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"actual-probe\",\"type\":\"function\",\"function\":{\"name\":\"read_probe\",\"arguments\":\"{}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n"))}, nil
							}
							if !strings.Contains(string(request.Body), "Actual new tool result retained.") || !strings.Contains(string(request.Body), "actual-probe") {
								t.Fatal("protected opening replacement dropped actual new tool messages")
							}
						}
						return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Done.\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))}, nil
					}
				}
				if err := implementation.Run(context.Background(), run); err != nil {
					t.Fatal(err)
				}
				if mode == "fits" || mode == "followup" {
					want := 1
					if mode == "followup" {
						want = 2
						if executed != 1 {
							t.Fatalf("actual follow-up tool executions=%d", executed)
						}
					}
					if physical != want || projected != 1 || hostReads != 1 {
						raw, _ := json.Marshal(failures)
						t.Fatalf("projection duplicated context/host/model work: physical=%d projections=%d hostReads=%d events=%s", physical, projected, hostReads, raw)
					}
				} else {
					if physical != 0 {
						t.Fatal("non-reducible/invalid opening dispatched")
					}
					failed := false
					for _, event := range failures {
						if event.Error != nil {
							failed = true
						}
					}
					if !failed {
						t.Fatal("non-fit opening did not produce typed context refusal")
					}
				}
			})
		}
	}
}
