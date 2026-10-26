package eino_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/eino"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/relay"
)

func observationReply(text string, calls []any) *http.Response {
	delta := map[string]any{"content": text}
	finish := "stop"
	if len(calls) > 0 {
		delta["tool_calls"] = calls
		finish = "tool_calls"
	}
	chunk := map[string]any{"id": "fixture", "object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}, "usage": map[string]any{"prompt_tokens": 12, "completion_tokens": 4}}
	raw, _ := json.Marshal(chunk)
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: " + string(raw) + "\n\ndata: [DONE]\n\n"))}
}
func probeCalls() []any {
	return []any{map[string]any{"index": 0, "id": "probe-1", "type": "function", "function": map[string]any{"name": "probe_echo", "arguments": `{"text":"one"}`}}, map[string]any{"index": 1, "id": "probe-2", "type": "function", "function": map[string]any{"name": "probe_echo", "arguments": `{"text":"two"}`}}}
}
func probeOptions() adapter.ObservationOptions {
	return adapter.ObservationOptions{Tools: []contract.ToolOfferHostTool{{Name: "probe_echo", Description: "Return the supplied text.", InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"],"additionalProperties":false}`), Taxonomy: contract.ToolTaxonomy{Operation: "transform", Effect: "pure"}}}}
}
func observationRun(store runcontext.RunStore) *adapter.Run {
	return &adapter.Run{Wire: "openai-compatible", ModelName: "fixture", Thinking: "off", Prompt: "Make two probe_echo calls.", AllowConversation: true, Critic: "off", Context: &runcontext.Input{RunStore: &runcontext.DurableRunInput{Store: store, ID: "observation"}, CurrentRequest: &runcontext.CurrentRequest{Message: "Observe", Input: "fixed"}, Connection: "owner-connection", ConnectionRevisions: map[string]string{"owner-connection": "revision-1"}}, Emit: func(contract.RunEvent) {}}
}
func TestObservationOnePhysicalGenerationFixedOfferAndTypedCalls(t *testing.T) {
	store := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
	run := observationRun(store)
	calls := 0
	run.Model = func(_ context.Context, _ string, request relay.Request) (*http.Response, error) {
		calls++
		var body struct {
			Tools []struct {
				Function struct {
					Name       string
					Parameters json.RawMessage
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.Unmarshal(request.Body, &body); err != nil {
			t.Fatal(err)
		}
		if len(body.Tools) != 1 || body.Tools[0].Function.Name != "probe_echo" || !strings.Contains(string(body.Tools[0].Function.Parameters), `"text"`) {
			t.Fatalf("fixed observation offer absent: %s", request.Body)
		}
		return observationReply("", probeCalls()), nil
	}
	result, err := eino.Observe(context.Background(), run, probeOptions())
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || len(result.Calls) != 2 || result.Calls[0].Name != "probe_echo" || string(result.Calls[1].Arguments) != `{"text":"two"}` {
		t.Fatalf("observation did not preserve two returned calls with one generation: %+v", result)
	}
	if result.Checkpoint.Used != 1 || result.Checkpoint.Limit != 1 || result.Checkpoint.RequestCap == nil || *result.Checkpoint.RequestCap != 1 || result.Receipt.CommitID == "" || len(result.Checkpoint.Durable.Dispatches) != 1 || result.Checkpoint.Durable.Dispatches[0].Kind != "model" {
		t.Fatal("observation lost canonical one-request receipt/checkpoint")
	}
	if len(result.Checkpoint.Completions) != 1 || result.Completion.InputTokens == nil || *result.Completion.InputTokens != 12 {
		t.Fatal("shared completion usage absent")
	}
	if _, err = eino.Observe(context.Background(), run, probeOptions()); err == nil || calls != 1 {
		t.Fatal("observation replayed under reused durable identity")
	}
}
func TestObservationRefusalAndUnknownNeverRetryOrFailOver(t *testing.T) {
	for _, kind := range []string{"retryable", "unknown", "malformed", "oversized", "unoffered"} {
		t.Run(kind, func(t *testing.T) {
			run := observationRun(runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{}))
			calls, switches := 0, 0
			run.Failover = func(string, string) (string, string, bool) { switches++; return "a", "b", true }
			run.Model = func(context.Context, string, relay.Request) (*http.Response, error) {
				calls++
				switch kind {
				case "retryable":
					return &http.Response{StatusCode: 429, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"busy","type":"rate_limit_error"}}`))}, nil
				case "unknown":
					return nil, errors.New("connection lost after dispatch")
				case "malformed":
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: {\n\n"))}, nil
				case "oversized":
					return observationReply(strings.Repeat("x", adapter.ObservationMaxText+1), nil), nil
				default:
					held := probeCalls()
					held[0].(map[string]any)["function"].(map[string]any)["name"] = "unoffered"
					return observationReply("", held), nil
				}
			}
			if _, err := eino.Observe(context.Background(), run, probeOptions()); err == nil || calls != 1 || switches != 0 {
				t.Fatalf("failed observation retried/finished/fell back: calls=%d switches=%d err=%v", calls, switches, err)
			}
		})
	}
}
func TestObservationEncodedOneTokenCeilingKeepsPolicyReserve(t *testing.T) {
	run := observationRun(runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{}))
	run.Wire = "anthropic"
	run.RequestOutputCeiling = 1
	run.Policy = runcontext.PolicyLayer{"budget": map[string]any{"outputTokens": 1024}}
	run.Model = func(_ context.Context, _ string, request relay.Request) (*http.Response, error) {
		var body struct {
			MaxTokens int `json:"max_tokens"`
		}
		_ = json.Unmarshal(request.Body, &body)
		if body.MaxTokens != 1 {
			t.Fatalf("one-token ping expanded to %d", body.MaxTokens)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"fixture\",\"usage\":{\"input_tokens\":2,\"output_tokens\":0}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"P\"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))}, nil
	}
	result, err := eino.Observe(context.Background(), run, adapter.ObservationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(result.Checkpoint.Policy["budget.outputTokens"].Value)
	if string(raw) != "1024" {
		t.Fatalf("model/context output reserve changed: %s", raw)
	}
}

type observationStore struct {
	runcontext.RunStore
	commit func(context.Context, runcontext.RunCommit) (runcontext.RunReceipt, error)
}

func (s observationStore) Commit(ctx context.Context, in runcontext.RunCommit) (runcontext.RunReceipt, error) {
	return s.commit(ctx, in)
}

func TestObservationCanonicalClaimCancellationCurrentnessAndLostAcknowledgement(t *testing.T) {
	for _, mode := range []string{"cancel-before-ack", "cancel-after-ack", "stale-after-claim", "lost-claim-ack", "lost-result-ack"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			memory := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
			injected, claimed := false, false
			store := observationStore{RunStore: memory}
			store.commit = func(ctx context.Context, in runcontext.RunCommit) (runcontext.RunReceipt, error) {
				var cp contract.Checkpoint
				if err := json.Unmarshal(in.Checkpoint, &cp); err != nil {
					t.Fatal(err)
				}
				claim, result := false, false
				if cp.Durable != nil && len(cp.Durable.Dispatches) > 0 {
					row := cp.Durable.Dispatches[len(cp.Durable.Dispatches)-1]
					claim = row.Kind == "model" && row.State == "claimed"
					result = row.Kind == "model" && row.State == "result"
				}
				if claim {
					claimed = true
				}
				if claim && !injected && mode == "cancel-before-ack" {
					injected = true
					cancel()
				}
				receipt, err := memory.Commit(ctx, in)
				if err != nil {
					return receipt, err
				}
				if !injected && ((claim && (mode == "cancel-after-ack" || mode == "lost-claim-ack")) || (result && mode == "lost-result-ack")) {
					injected = true
					if mode == "cancel-after-ack" {
						cancel()
					} else {
						return runcontext.RunReceipt{}, errors.New("fixture lost storage acknowledgment")
					}
				}
				return receipt, nil
			}
			run := observationRun(store)
			calls := 0
			run.Model = func(context.Context, string, relay.Request) (*http.Response, error) {
				calls++
				return observationReply("ok", nil), nil
			}
			run.Context.Authorize = func(context.Context) error {
				if claimed && mode == "stale-after-claim" {
					injected = true
					return errors.New("fixture connection revision changed")
				}
				return nil
			}
			result, err := eino.Observe(ctx, run, probeOptions())
			want := 0
			if mode == "lost-result-ack" {
				want = 1
			}
			if !injected || err == nil || calls != want || result.Receipt.CommitID != "" {
				t.Fatalf("unsafe claim outcome: injected=%v calls=%d err=%v", injected, calls, err)
			}
			before := calls
			run.Context.RunStore.Resume = true
			if _, err = eino.Observe(context.Background(), run, probeOptions()); err == nil || calls != before {
				t.Fatal("uncertain observation replayed")
			}
		})
	}
}

func TestObservationRefusesExecutableRegistryAndIncompleteResult(t *testing.T) {
	for _, mode := range []string{"registry", "native", "bad-schema", "bad-arguments", "length"} {
		t.Run(mode, func(t *testing.T) {
			run := observationRun(runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{}))
			options := probeOptions()
			calls, executions := 0, 0
			if mode == "registry" {
				run.HostTools = []runcontext.HostTool{{Name: "probe_echo", Description: "fixed probe", Effect: "read", Taxonomy: &runcontext.ToolTaxonomy{Operation: "read", Effect: "read"}, InputSchema: json.RawMessage(`{"type":"object"}`), Execute: func(context.Context, json.RawMessage) (runcontext.ToolResult, error) {
					executions++
					return runcontext.ToolResult{}, nil
				}}}
			}
			if mode == "native" {
				run.Agent = &adapter.Agent{Run: func(context.Context, adapter.AgentRequest, adapter.AgentCallbacks) error { executions++; return nil }}
			}
			if mode == "bad-schema" {
				options.Tools[0].InputSchema = json.RawMessage(`[]`)
			}
			run.Model = func(context.Context, string, relay.Request) (*http.Response, error) {
				calls++
				if mode == "bad-arguments" {
					held := probeCalls()
					held[0].(map[string]any)["function"].(map[string]any)["arguments"] = `[]`
					return observationReply("", held), nil
				}
				response := observationReply("truncated", nil)
				if mode == "length" {
					raw, _ := io.ReadAll(response.Body)
					response.Body = io.NopCloser(strings.NewReader(strings.ReplaceAll(string(raw), `"finish_reason":"stop"`, `"finish_reason":"length"`)))
				}
				return response, nil
			}
			if _, err := eino.Observe(context.Background(), run, options); err == nil {
				t.Fatal("invalid observation accepted")
			}
			want := 0
			if mode == "bad-arguments" || mode == "length" {
				want = 1
			}
			if calls != want || executions != 0 {
				t.Fatalf("invalid observation dispatched/executed: calls=%d executions=%d", calls, executions)
			}
		})
	}
}

func TestActualModelFallbackPreservesEncodedOutputCeiling(t *testing.T) {
	for _, family := range []string{"openai-compatible", "anthropic", "gemini"} {
		t.Run(family, func(t *testing.T) {
			var events []contract.RunEvent
			primary, backup := 0, 0
			run := &adapter.Run{Wire: family, ModelName: "primary-model", Thinking: "off", Prompt: "Answer briefly.", AllowConversation: true, Critic: "off", RequestOutputCeiling: 1, Context: &runcontext.Input{Connection: "primary", CurrentRequest: &runcontext.CurrentRequest{Message: "fixed", Input: "fixed"}}, Policy: runcontext.PolicyLayer{"models": map[string]any{"author": map[string]any{"fallback": []any{"backup"}}}}, Emit: func(e contract.RunEvent) { events = append(events, e) }}
			check := func(body []byte) {
				var v map[string]json.RawMessage
				if err := json.Unmarshal(body, &v); err != nil {
					t.Fatal(err)
				}
				key := "max_tokens"
				if family == "gemini" {
					_ = json.Unmarshal(v["generationConfig"], &v)
					key = "maxOutputTokens"
				}
				if string(v[key]) != "1" {
					t.Fatalf("actual outbound rewritten output ceiling raised: %s", body)
				}
			}
			run.Model = func(_ context.Context, _ string, r relay.Request) (*http.Response, error) {
				primary++
				check(r.Body)
				return &http.Response{StatusCode: 503, Header: http.Header{"Retry-After": []string{"0"}}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
			}
			run.PolicyConnections = map[string]adapter.PolicyConnection{"backup": {Wire: family, ModelName: "backup-model", Model: func(_ context.Context, _ string, r relay.Request) (*http.Response, error) {
				backup++
				check(r.Body)
				if family == "gemini" {
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"candidates":[{"content":{"role":"model","parts":[{"text":"P"}]},"finishReason":"STOP"}]}`))}, nil
				}
				if family == "anthropic" {
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"id":"fixed","type":"message","role":"assistant","content":[{"type":"text","text":"P"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":1}}`))}, nil
				}
				return observationReply("P", nil), nil
			}}}
			if err := eino.New().Run(context.Background(), run); err != nil {
				t.Fatal(err)
			}
			if primary == 0 || backup != 1 {
				t.Fatalf("actual fallback not exercised primary=%d backup=%d", primary, backup)
			}
			for _, e := range events {
				if e.Error != nil {
					t.Fatalf("fallback failed: %s", e.Error.Message)
				}
			}
		})
	}
}

func TestObservationUnknownProtocolDoesNotClaimOrDispatch(t *testing.T) {
	store := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
	run := observationRun(store)
	run.Wire = "opaque"
	run.RequestOutputCeiling = 1
	calls := 0
	run.Model = func(context.Context, string, relay.Request) (*http.Response, error) {
		calls++
		return observationReply("P", nil), nil
	}
	if _, err := eino.Observe(context.Background(), run, probeOptions()); err == nil || calls != 0 {
		t.Fatal("unknown physical protocol dispatched")
	}
	head, err := store.Load(context.Background(), "observation")
	if err != nil {
		t.Fatal(err)
	}
	if head != nil {
		var cp contract.Checkpoint
		_ = json.Unmarshal(head.Checkpoint, &cp)
		if cp.Durable != nil && len(cp.Durable.Dispatches) != 0 {
			t.Fatal("unknown physical protocol gained durable claim")
		}
	}
}

func TestObservationBypassesProviderRetryWrapperDespiteAvailableFallback(t *testing.T) {
	store := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
	run := observationRun(store)
	primary, backup, switches := 0, 0, 0
	run.Policy = runcontext.PolicyLayer{"models": map[string]any{"author": map[string]any{"fallback": []any{"backup"}}}}
	run.Model = func(context.Context, string, relay.Request) (*http.Response, error) {
		primary++
		return &http.Response{StatusCode: 429, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":{"type":"rate_limit_error","message":"fixture"}}`))}, nil
	}
	run.PolicyConnections = map[string]adapter.PolicyConnection{"backup": {Wire: run.Wire, ModelName: "backup-model", Model: func(context.Context, string, relay.Request) (*http.Response, error) {
		backup++
		return observationReply("must not run", nil), nil
	}}}
	run.Emit = func(e contract.RunEvent) {
		if e.ConnectionSwitched != nil || e.Retry != nil {
			switches++
		}
	}
	if _, err := eino.Observe(context.Background(), run, probeOptions()); err == nil {
		t.Fatal("refused observation accepted")
	}
	if primary != 1 || backup != 0 || switches != 0 {
		t.Fatalf("observation entered provider retry/fallback wrapper: primary=%d backup=%d switches=%d", primary, backup, switches)
	}
	head, err := store.Load(context.Background(), "observation")
	if err != nil || head == nil {
		t.Fatal("missing refused observation receipt")
	}
	var cp contract.Checkpoint
	_ = json.Unmarshal(head.Checkpoint, &cp)
	if len(cp.Failover) != 0 {
		t.Fatal("observation consumed fallback policy despite one physical cap")
	}
}
