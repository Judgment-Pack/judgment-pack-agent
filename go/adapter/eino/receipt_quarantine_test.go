package eino_test

// Receipt quarantine through the actual adapters (agent#242): the issue's six-request sequence, an
// in-process model peer that calls a custom connected source whose receipt does not verify and then
// tries every context reader on what it called, run through the native adapter for each case of
// contract/vectors/tool-result/receipt-quarantine.json and through the API adapter (eino) for the
// issue's case and the deadline case. The API runs cost the most under the race detector; the other
// cases reach the same check below the adapter, and go/context/receipt_quarantine_test.go holds the
// boundary itself for every case. The peer learns a reference only from what the readers answer;
// where none is listed it uses the id the result would have been retained under. It lives beside the
// API adapter rather than in package adapter, whose race run is already near its package timeout.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	native "github.com/Judgment-Pack/judgment-pack-agent/go/adapter/agent"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/eino"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
	"github.com/Judgment-Pack/judgment-pack-agent/go/relay"
)

type quarantineVector struct {
	Fixture struct {
		Scope                  string          `json:"scope"`
		Sentinel               string          `json:"sentinel"`
		SourceCallMilliseconds int             `json:"sourceCallMilliseconds"`
		Declaration            json.RawMessage `json:"declaration"`
		WireName               string          `json:"wireName"`
		Arguments              json.RawMessage `json:"arguments"`
		Cases                  []struct {
			Name          string          `json:"name"`
			Result        json.RawMessage `json:"result"`
			Receipt       string          `json:"receipt"`
			Verifier      string          `json:"verifier"`
			VerifierCalls int32           `json:"verifierCalls"`
		} `json:"cases"`
		Readers []struct {
			Tool      string         `json:"tool"`
			Arguments map[string]any `json:"arguments"`
		} `json:"readers"`
	} `json:"fixture"`
	Items []json.RawMessage `json:"items"`
}

// The issue's sequence: the source call, list_context, read_context, read_window and a search of
// every retained item (the vector's readers 0, 1, 2 and 4, so its items 0, 1, 2, 3 and 5), then the
// final answer: six requests.
var quarantineSequence = []int{0, 1, 2, 4}

func quarantineDeclaration(t *testing.T, v quarantineVector, result json.RawMessage, receipt, verifier string, executions, verifications *atomic.Int32) runcontext.HostTool {
	t.Helper()
	var declared struct {
		Name, Description string
		InputSchema       json.RawMessage
		Taxonomy          *runcontext.ToolTaxonomy
		Source            struct{ Kind, ID, Trust, Version string }
	}
	if err := json.Unmarshal(v.Fixture.Declaration, &declared); err != nil {
		t.Fatal(err)
	}
	tool := runcontext.HostTool{Name: declared.Name, Description: declared.Description, InputSchema: declared.InputSchema, Taxonomy: declared.Taxonomy}
	digest := runcontext.ToolListDigest([]runcontext.ListedTool{{Name: tool.Name, Description: tool.Description, InputSchema: tool.InputSchema, Taxonomy: tool.Taxonomy}})
	tool.Source = &runcontext.ToolSource{Kind: declared.Source.Kind, ID: declared.Source.ID, Trust: declared.Source.Trust, Version: declared.Source.Version, ToolListDigest: digest}
	tool.Execute = func(context.Context, json.RawMessage) (runcontext.ToolResult, error) {
		executions.Add(1)
		held := runcontext.ReadToolResult(runcontext.MustParseJSON(string(result)))
		held.Receipt = receipt
		return held, nil
	}
	tool.Source.VerifyReceipt = func(ctx context.Context, _ runcontext.ReceiptEvidence) (bool, error) {
		verifications.Add(1)
		if verifier == "answers true after the deadline" {
			<-ctx.Done()
			return true, nil
		}
		return false, nil
	}
	return tool
}

// quarantineFinal is one streamed completion that answers "Done." and stops.
func quarantineFinal() *http.Response {
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: {\"id\":\"fixture\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Done.\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"fixture\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))}
}

// quarantineToolCall is one streamed completion that calls name with args.
func quarantineToolCall(id, name string, args any) *http.Response {
	raw, _ := json.Marshal(args)
	chunk := map[string]any{"id": "fixture", "object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": id, "type": "function", "function": map[string]any{"name": name, "arguments": string(raw)}}}}, "finish_reason": "tool_calls"}}}
	body, _ := json.Marshal(chunk)
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: " + string(body) + "\n\ndata: [DONE]\n\n"))}
}

// quarantineProjection is a tool_result event as the vector's items state it. A listing's own
// operation records (kind history, "<tool> · failed": the run records every call it observes, and
// the rejected call stays inspectable that way, its result the gate's refusal) are taken out, and
// each is returned, so the rest is the boundary's listing.
func quarantineProjection(t *testing.T, event *contract.ToolResult, scope string) (string, []*runcontext.Value) {
	t.Helper()
	raw, _ := json.Marshal(event)
	held := runcontext.MustParseJSON(string(raw))
	out := runcontext.Object()
	for _, key := range held.Keys() {
		if key != "callId" && key != "source" && key != "version" {
			out.Set(key, held.Get(key))
		}
	}
	operations := []*runcontext.Value{}
	if event.Name == "list_context" && !event.IsError {
		listed := runcontext.Array()
		for _, item := range out.Get("structured").Get("items").Items() {
			if item.Get("kind").Str() == "history" && strings.HasPrefix(item.Get("contextId").Str(), scope+":operation-") {
				operations = append(operations, item)
				continue
			}
			listed.Append(item)
		}
		if runcontext.CanonicalJSON(runcontext.MustParseJSON(event.Text)) != runcontext.CanonicalJSON(out.Get("structured")) {
			t.Fatal("the listing's text is not its structured content")
		}
		out.Get("structured").Set("items", listed)
		out.Set("text", runcontext.String(out.Get("structured").Text()))
	}
	return runcontext.CanonicalJSON(out), operations
}

func TestReceiptQuarantineThroughTheActualAdapters(t *testing.T) {
	raw, err := os.ReadFile("../../../contract/vectors/tool-result/receipt-quarantine.json")
	if err != nil {
		t.Fatal(err)
	}
	var v quarantineVector
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	referenced := regexp.MustCompile(regexp.QuoteMeta(v.Fixture.Scope) + `:result-[A-Za-z0-9_]+-[0-9a-f]{64}:[0-9a-f]{16}`)
	through := map[string]map[string]bool{"api": {"rejected-receipt": true, "expired-receipt": true}}
	for _, engine := range []string{"api", "native"} {
		for _, held := range v.Fixture.Cases {
			if through[engine] != nil && !through[engine][held.Name] {
				continue
			}
			t.Run(engine+"/"+held.Name, func(t *testing.T) {
				ctx := context.Background()
				var executions, verifications atomic.Int32
				tool := quarantineDeclaration(t, v, held.Result, held.Receipt, held.Verifier, &executions, &verifications)
				text := string(runcontext.ReadToolResult(runcontext.MustParseJSON(string(held.Result))).JSON())
				digest := sha256.Sum256([]byte(text))
				retained := v.Fixture.Scope + ":result-" + v.Fixture.WireName + "-" + hex.EncodeToString(digest[:]) + ":" + hex.EncodeToString(digest[:])[:16]
				chat := runcontext.NewChatContext(v.Fixture.Scope)
				store := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
				run := func(id string, peer func(n int, seen string) (string, map[string]any)) ([]string, []*contract.ToolResult) {
					g := gate.New(gate.Options{})
					g.SetOffer(contract.ToolOffer{})
					requests := []string{}
					results := []*contract.ToolResult{}
					policy := runcontext.PolicyLayer{}
					if held.Name == "expired-receipt" {
						policy["budget"] = map[string]any{"sourceCallMilliseconds": v.Fixture.SourceCallMilliseconds}
					}
					r := &adapter.Run{Prompt: "Read record e0.", Wire: "openai-compatible", ModelName: "fixture", Thinking: "off", Critic: "off", AllowConversation: true, Interactive: true, Gate: g.Calls(), HostTools: []runcontext.HostTool{tool}, Policy: policy,
						Context: &runcontext.Input{CurrentRequest: &runcontext.CurrentRequest{Message: "Read record e0.", Input: "e0"}, Chat: chat, Connection: "provider", ConnectionRevisions: map[string]string{"provider": "one"}, RunStore: &runcontext.DurableRunInput{Store: store, ID: id}},
						Emit: func(e contract.RunEvent) {
							if e.Error != nil {
								raw, _ := json.Marshal(e)
								t.Logf("event %s", raw)
							}
							if e.ToolResult != nil {
								results = append(results, e.ToolResult)
							}
						},
						EnrollContext: func(_ context.Context, offer adapter.ContextOffer) error {
							return g.EnrollContext(offer.ID, offer.HostTools)
						}}
					var implementation adapter.Adapter
					if engine == "native" {
						implementation = native.New()
						r.Agent = &adapter.Agent{Model: "fixture", Run: func(ctx context.Context, request adapter.AgentRequest, cb adapter.AgentCallbacks) error {
							body, _ := json.Marshal(request)
							requests = append(requests, string(body))
							// The native offer names each host tool by its description's prefix.
							named := map[string]string{}
							for _, offered := range request.Tools {
								if at := strings.Index(offered.Description, ": "); at > 0 {
									named[offered.Description[:at]] = offered.Name
								}
							}
							for n := 1; ; n++ {
								name, args := peer(n, strings.Join(requests, "\n"))
								if name == "" {
									return cb.Event(ctx, adapter.AgentEvent{Type: "message", ID: "final", Text: "Done.", Phase: "final"})
								}
								raw, _ := json.Marshal(args)
								if named[name] == "" {
									t.Errorf("the native offer has no %s", name)
									return fmt.Errorf("no %s", name)
								}
								answer, err := cb.Tool(ctx, adapter.AgentToolCall{ID: fmt.Sprintf("call-%d", n), Name: named[name], Arguments: raw})
								if err != nil {
									return err
								}
								body, _ := json.Marshal(answer)
								requests = append(requests, string(body))
							}
						}}
					} else {
						implementation = eino.New()
						r.Model = func(_ context.Context, _ string, request relay.Request) (*http.Response, error) {
							requests = append(requests, string(request.Body))
							name, args := peer(len(requests), string(request.Body))
							if name == "" {
								return quarantineFinal(), nil
							}
							return quarantineToolCall(fmt.Sprintf("call-%d", len(requests)), name, args), nil
						}
					}
					if err := implementation.Run(ctx, r); err != nil {
						t.Fatal(err)
					}
					return requests, results
				}
				// The peer: the source call, then every reader on what it learned, then its answer.
				learned := retained
				requests, results := run("receipt-quarantine", func(n int, seen string) (string, map[string]any) {
					if n == 1 {
						var args map[string]any
						_ = json.Unmarshal(v.Fixture.Arguments, &args)
						return v.Fixture.WireName, args
					}
					if n > len(quarantineSequence)+1 {
						return "", nil
					}
					if found := referenced.FindString(seen); found != "" {
						if found != retained {
							t.Errorf("the listing names %s, not the id the result would be retained under", found)
						}
						learned = found
					}
					reader := v.Fixture.Readers[quarantineSequence[n-2]]
					args := map[string]any{}
					for key, value := range reader.Arguments {
						if value == "$retained" {
							value = learned
						}
						args[key] = value
					}
					return reader.Tool, args
				})
				if len(requests) != 6 {
					t.Fatalf("%d model requests", len(requests))
				}
				for at, request := range requests {
					if strings.Contains(request, v.Fixture.Sentinel) {
						t.Fatalf("model request %d carries the unverified result", at+1)
					}
				}
				want := []int{0, 1, 2, 3, 5}
				if len(results) != len(want) {
					t.Fatalf("%d tool results", len(results))
				}
				for at, item := range want {
					got, operations := quarantineProjection(t, results[at], v.Fixture.Scope)
					if got != runcontext.CanonicalJSON(runcontext.MustParseJSON(string(v.Items[item]))) {
						t.Fatalf("tool result %d is\n%s\nnot\n%s", at, got, v.Items[item])
					}
					for _, operation := range operations {
						if operation.Get("label").Str() != v.Fixture.WireName+" · failed" {
							t.Fatalf("the listing holds the operation %s", operation.Text())
						}
					}
					// Through the API adapter the rejected call stays listed as the failed operation
					// the run records for its requests; the native run's one invocation records none.
					if want := map[string]int{"api": 1, "native": 0}[engine]; results[at].Name == "list_context" && len(operations) != want {
						t.Fatalf("the listing holds %d operation records", len(operations))
					}
				}
				if executions.Load() != 1 || verifications.Load() != held.VerifierCalls {
					t.Fatalf("executions %d, verifier calls %d", executions.Load(), verifications.Load())
				}
				head, err := store.Load(ctx, "receipt-quarantine")
				if err != nil || head == nil {
					t.Fatal("no stored run", err)
				}
				if strings.Contains(string(head.Checkpoint), v.Fixture.Sentinel) {
					t.Fatal("the stored checkpoint carries the unverified result")
				}
				rows := 0
				for _, row := range runcontext.MustParseJSON(string(head.Checkpoint)).Get("durable").Get("dispatches").Items() {
					if row.Get("tool").Str() != v.Fixture.WireName {
						continue
					}
					rows++
					failed := row.Get("failure")
					if row.Get("state").Str() != "unknown" || row.Has("result") || failed.Get("code").Str() != "tool-failed" || failed.Get("outcome").Str() != "unknown" {
						t.Fatalf("the rejected call is recorded as %s", row.Text())
					}
				}
				if rows != 1 {
					t.Fatalf("the call is recorded %d times", rows)
				}
				// The chat's next run, once per adapter (its index is the chat's, whatever the case): its
				// first request carries neither the result nor its reference.
				if held.Name == "rejected-receipt" {
					next, _ := run("receipt-quarantine-next", func(int, string) (string, map[string]any) { return "", nil })
					if len(next) != 1 || strings.Contains(next[0], v.Fixture.Sentinel) || strings.Contains(next[0], retained) {
						t.Fatalf("the next run's request carries it: %d", len(next))
					}
				}
				if executions.Load() != 1 {
					t.Fatal("the source was called again")
				}
			})
		}
	}
}
