package adapter_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	native "github.com/Judgment-Pack/judgment-pack-agent/go/adapter/agent"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/delegate"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/eino"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/turn"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
	"github.com/Judgment-Pack/judgment-pack-agent/go/relay"
)

func usageScope(limits runcontext.UsageAmounts) runcontext.UsageBudgetScope {
	return runcontext.UsageBudgetScope{Resource: "usage-project", ID: "shared-scope", Revision: "policy-r1", Limits: limits}
}
func usageStore(t *testing.T, options runcontext.MemoryUsageBudgetOptions) *runcontext.MemoryUsageBudgetStore {
	t.Helper()
	store, err := runcontext.NewMemoryUsageBudgetStore("usage-project", options)
	if err != nil {
		t.Fatal(err)
	}
	return store
}
func usageRun(store runcontext.RunStore, id string, budget runcontext.UsageBudgetStore, limits runcontext.UsageAmounts) *adapter.Run {
	return &adapter.Run{Wire: "openai-compatible", ModelName: "fixture", Thinking: "off", Prompt: "Answer.", AllowConversation: true, Critic: "off", Context: &runcontext.Input{CurrentRequest: &runcontext.CurrentRequest{Message: "Answer", Input: "Answer"}, Connection: "provider-1", ConnectionRevisions: map[string]string{"provider-1": "revision-1"}, RunStore: &runcontext.DurableRunInput{Store: store, ID: id}, UsageBudget: &runcontext.UsageBudgetInput{Store: budget, Scope: usageScope(limits), Bounder: func(_ context.Context, request runcontext.UsageBoundRequest) (runcontext.UsageAmounts, error) {
		out := runcontext.UsageAmounts{"inputTokens": 10}
		if request.EncodedOutputMaximum != nil {
			out["outputTokens"] = *request.EncodedOutputMaximum
		}
		return out, nil
	}}}, Emit: func(contract.RunEvent) {}}
}
func usageHead(t *testing.T, store *runcontext.MemoryRunStore, id string) *runcontext.Value {
	t.Helper()
	head, err := store.Load(context.Background(), id)
	if err != nil || head == nil {
		t.Fatal("missing real run head", err)
	}
	value, err := runcontext.ParseJSON(head.Checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	return value
}
func usageReply() *http.Response {
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: {\"id\":\"fixture\",\"object\":\"chat.completion.chunk\",\"model\":\"fixture\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Done.\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":4,\"completion_tokens\":2}}\n\ndata: [DONE]\n\n"))}
}
func TestUsageBudgetActualEinoAdmissionAndFeatureReport(t *testing.T) {
	memory := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
	budget := usageStore(t, runcontext.MemoryUsageBudgetOptions{})
	run := usageRun(memory, "one", budget, runcontext.UsageAmounts{"modelRequests": 1, "inputTokens": 10, "outputTokens": 100000})
	calls := 0
	run.Model = func(context.Context, string, relay.Request) (*http.Response, error) {
		calls++
		return usageReply(), nil
	}
	if err := eino.New().Run(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("physical calls=%d want1", calls)
	}
	value := usageHead(t, memory, "one")
	journal := value.Get("usageBudget")
	if value.Get("version").Num() != 3 || journal.Get("entries").Len() != 1 || journal.Get("entries").Items()[0].Get("reservation").Get("id").Str() != value.Get("durable").Get("dispatches").Items()[0].Get("id").Str() {
		t.Fatal("exact original writer reservation/dispatch identity missing", value.Text())
	}
	cp, err := runcontext.ReadExecution(value)
	if err != nil {
		t.Fatal(err)
	}
	report, err := runcontext.ProjectRunReport(*cp)
	if err != nil || report.Get("version").Num() != 2 || report.Get("usageTotals").Get("inputTokens").Get("known").Num() != 4 || !report.Get("usageTotals").Get("outputTokens").Get("complete").Truth() {
		t.Fatal("actual usage report2 not projected", err)
	}
	if _, err = runcontext.ReadRunReport(report); err != nil {
		t.Fatal("produced report2 not strict-reader valid", err)
	}
}

type usageLostRunStore struct {
	*runcontext.MemoryRunStore
	armed bool
}

func (s *usageLostRunStore) Commit(ctx context.Context, input runcontext.RunCommit) (runcontext.RunReceipt, error) {
	receipt, err := s.MemoryRunStore.Commit(ctx, input)
	if err != nil {
		return receipt, err
	}
	var value struct {
		UsageBudget *struct {
			Entries []json.RawMessage `json:"entries"`
		} `json:"usageBudget"`
	}
	_ = json.Unmarshal(input.Checkpoint, &value)
	if s.armed && value.UsageBudget != nil && len(value.UsageBudget.Entries) > 0 {
		s.armed = false
		return runcontext.RunReceipt{}, errors.New("fixture lost journal acknowledgment")
	}
	return receipt, nil
}
func TestUsageBudgetActualEinoLostAcknowledgmentNeverDispatches(t *testing.T) {
	for _, fault := range []string{"reserve", "journal"} {
		t.Run(fault, func(t *testing.T) {
			memory := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
			armed := true
			budget := usageStore(t, runcontext.MemoryUsageBudgetOptions{AfterChange: func() error {
				if fault == "reserve" && armed {
					armed = false
					return errors.New("fixture lost Reserve acknowledgment")
				}
				return nil
			}})
			var store runcontext.RunStore = memory
			if fault == "journal" {
				store = &usageLostRunStore{memory, true}
			}
			calls := 0
			dispatch := func(context.Context, string, relay.Request) (*http.Response, error) {
				calls++
				return usageReply(), nil
			}
			run := usageRun(store, "lost-"+fault, budget, runcontext.UsageAmounts{"modelRequests": 1})
			run.Model = dispatch
			_ = eino.New().Run(context.Background(), run)
			if calls != 0 {
				t.Fatal("unknown acknowledgment physically dispatched")
			}
			if usageHead(t, memory, "lost-"+fault).Get("usageBudget").Get("entries").Len() != 1 {
				t.Fatal("unknown reservation forgotten")
			}
			resumed := usageRun(store, "lost-"+fault, budget, runcontext.UsageAmounts{"modelRequests": 1})
			resumed.Context.RunStore.Resume = true
			resumed.Model = dispatch
			_ = eino.New().Run(context.Background(), resumed)
			if calls != 0 {
				t.Fatal("explicit resume replayed unknown physical admission")
			}
			refusals := usageHead(t, memory, "lost-"+fault).Get("usageBudget").Get("refusals")
			if refusals.Len() == 0 || refusals.Items()[refusals.Len()-1].Get("name").Str() != "UsageBudgetUnknown" {
				t.Fatal("unknown refusal not retained")
			}
		})
	}
}
func TestUsageBudgetActualNativeInvocationAndOpaqueRefusal(t *testing.T) {
	for _, dimension := range []string{"providerInvocations", "modelRequests", "inputTokens"} {
		t.Run(dimension, func(t *testing.T) {
			memory := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
			budget := usageStore(t, runcontext.MemoryUsageBudgetOptions{})
			run := usageRun(memory, "native", budget, runcontext.UsageAmounts{dimension: 10})
			calls := 0
			run.Agent = &adapter.Agent{Model: "fixture", Run: func(ctx context.Context, _ adapter.AgentRequest, cb adapter.AgentCallbacks) error {
				calls++
				return cb.Event(ctx, adapter.AgentEvent{Type: "message", ID: "final", Text: "Done.", Phase: "final"})
			}}
			_ = native.New().Run(context.Background(), run)
			journal := usageHead(t, memory, "native").Get("usageBudget")
			if dimension == "providerInvocations" {
				if calls != 1 || journal.Get("entries").Items()[0].Get("observed").Get("providerInvocations").Num() != 1 {
					t.Fatal("native invocation was not distinctly counted")
				}
				cp, err := runcontext.ReadExecution(usageHead(t, memory, "native"))
				if err != nil {
					t.Fatal(err)
				}
				report, err := runcontext.ProjectRunReport(*cp)
				if err != nil {
					t.Fatal(err)
				}
				if report.Get("usageTotals").Get("modelRequests").Get("complete").Truth() {
					t.Fatal("opaque native physical requests reported complete zero")
				}
			} else {
				if calls != 0 || journal.Get("refusals").Len() == 0 || journal.Get("refusals").Items()[0].Get("name").Str() != "UsageBudgetUnsupported" {
					t.Fatal("opaque native route claimed physical guarantee")
				}
			}
		})
	}
}

type usageRetryBudget struct {
	runcontext.UsageBudgetStore
	calls int
	lose  bool
}

func (s *usageRetryBudget) Reserve(ctx context.Context, in runcontext.UsageReservation) (runcontext.UsageReserveResult, error) {
	receipt, err := s.UsageBudgetStore.Reserve(ctx, in)
	if err != nil {
		return receipt, err
	}
	s.calls++
	if s.lose && s.calls == 3 {
		return runcontext.UsageReserveResult{}, errors.New("fixture lost retry Reserve ack")
	}
	return receipt, nil
}

type usageRetryJournal struct {
	runcontext.RunStore
	armed bool
}

func (s *usageRetryJournal) Commit(ctx context.Context, in runcontext.RunCommit) (runcontext.RunReceipt, error) {
	receipt, err := s.RunStore.Commit(ctx, in)
	if err != nil {
		return receipt, err
	}
	cp, _ := runcontext.ParseJSON(in.Checkpoint)
	entries := cp.Get("usageBudget").Get("entries")
	if s.armed && entries.Len() == 3 && entries.Items()[2].Get("state").Str() == "reserved" {
		s.armed = false
		return runcontext.RunReceipt{}, errors.New("fixture lost retry journal ack")
	}
	return receipt, nil
}
func TestUsageBudgetActualDelegatedChildRetry(t *testing.T) {
	for _, mode := range []string{"complete", "exhausted", "reserve-ack", "journal-ack"} {
		t.Run(mode, func(t *testing.T) {
			memory := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
			budget := &usageRetryBudget{UsageBudgetStore: usageStore(t, runcontext.MemoryUsageBudgetOptions{}), lose: mode == "reserve-ack"}
			var store runcontext.RunStore = memory
			if mode == "journal-ack" {
				store = &usageRetryJournal{RunStore: memory, armed: true}
			}
			limit := int64(8)
			if mode == "exhausted" {
				limit = 2
			}
			id := "delegation-" + mode
			run := usageRun(store, id, budget, runcontext.UsageAmounts{"modelRequests": limit})
			run.Interactive = true
			run.Context.ConnectionRevisions["fixture"] = "revision-1"
			g := gate.New(gate.Options{})
			g.SetOffer(contract.ToolOffer{Tools: []contract.ToolOfferRuntimeTool{}, HostTools: []contract.ToolOfferHostTool{turn.DelegationTool(turn.TaskProfiles)}})
			run.Gate = g.Calls()
			run.Children = func(context.Context, contract.DelegatedTaskProfile, func(contract.RunEvent)) (*gate.Calls, []contract.ToolOfferRuntimeTool, error) {
				return nil, nil, nil
			}
			sends := 0
			run.Model = func(ctx context.Context, _ string, request relay.Request) (*http.Response, error) {
				sends++
				if sends == 1 {
					args := `{"tasks":[{"title":"Check evidence","objective":"Check supplied evidence","profile":"review"}]}`
					body, _ := json.Marshal(map[string]any{"id": "parent", "object": "chat.completion.chunk", "model": "fixture", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "delegate-1", "type": "function", "function": map[string]any{"name": "delegate_tasks", "arguments": args}}}}, "finish_reason": "tool_calls"}}, "usage": map[string]any{"prompt_tokens": 4, "completion_tokens": 2}})
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: " + string(body) + "\n\ndata: [DONE]\n\n"))}, nil
				}
				if sends == 2 {
					return &http.Response{StatusCode: 429, Header: http.Header{"Content-Type": []string{"application/json"}, "Retry-After": []string{"0"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"code":"rate_limit_exceeded","message":"fixture retry"}}`))}, nil
				}
				return usageReply(), nil
			}
			var captured *adapter.Run
			probed := false
			inner := adapter.Func(func(ctx context.Context, given *adapter.Run) error {
				if given.Context != nil && given.Context.Objective != "" {
					copy := *given
					captured = &copy
					if mode == "complete" && !probed {
						probed = true
						fresh := usageRun(memory, "transplanted-live", budget, runcontext.UsageAmounts{"modelRequests": 8})
						fresh.Context.Connection = "fixture"
						fresh.Context.ConnectionRevisions["fixture"] = "revision-1"
						stale := *given
						stale.Context = fresh.Context
						before := sends
						_ = eino.New().Run(ctx, &stale)
						if sends != before {
							t.Fatal("cross-run child wrapper widened dispatch authority")
						}
					}
				}
				return eino.New().Run(ctx, given)
			})
			_ = delegate.With(inner, delegate.Options{}).Run(context.Background(), run)
			cp := usageHead(t, memory, id)
			entries := cp.Get("usageBudget").Get("entries")
			if mode == "complete" {
				if sends != 4 || entries.Len() != 4 {
					t.Fatalf("each actual child retry needs distinct reservation: sends=%d entries=%d refusals=%s", sends, entries.Len(), cp.Get("usageBudget").Get("refusals").Text())
				}
				ids := map[string]bool{}
				for _, row := range entries.Items() {
					id := row.Get("reservation").Get("id").Str()
					if ids[id] || row.Get("observed").Get("modelRequests").Num() != 1 {
						t.Fatal("duplicate or uncounted child dispatch")
					}
					ids[id] = true
				}
				if entries.Items()[1].Get("reservation").Get("task").Str() == "" || entries.Items()[1].Get("reservation").Get("task").Str() != entries.Items()[2].Get("reservation").Get("task").Str() {
					t.Fatal("retry escaped exact child grant")
				}
				if captured == nil {
					t.Fatal("actual child wrapper not captured")
				}
				fresh := usageRun(memory, "transplanted", budget, runcontext.UsageAmounts{"modelRequests": 8})
				fresh.Context.Connection = "fixture"
				fresh.Context.ConnectionRevisions["fixture"] = "revision-1"
				stale := *captured
				stale.Context = fresh.Context
				_ = eino.New().Run(context.Background(), &stale)
				if sends != 4 {
					t.Fatal("cross-run child wrapper widened dispatch authority")
				}
			} else {
				if sends != 2 {
					t.Fatalf("budget or unknown ack allowed forbidden physical dispatch: sends=%d refusals=%s", sends, cp.Get("usageBudget").Get("refusals").Text())
				}
				if mode != "exhausted" {
					run.Context.RunStore.Resume = true
					_ = delegate.With(eino.New(), delegate.Options{}).Run(context.Background(), run)
					if sends != 2 {
						t.Fatal("unknown retry ack dispatched on resume")
					}
				}
			}
		})
	}
}

func TestUsageBudgetActualEinoRefusesUnprovableBoundsBeforeDispatch(t *testing.T) {
	for _, mode := range []string{"absent", "below-ceiling", "mutated-ceiling", "missing-revision"} {
		t.Run(mode, func(t *testing.T) {
			memory := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
			budget := usageStore(t, runcontext.MemoryUsageBudgetOptions{})
			run := usageRun(memory, "bound-"+mode, budget, runcontext.UsageAmounts{"outputTokens": 100000})
			calls := 0
			run.Model = func(context.Context, string, relay.Request) (*http.Response, error) {
				calls++
				return usageReply(), nil
			}
			switch mode {
			case "absent":
				run.Context.UsageBudget.Bounder = nil
			case "below-ceiling":
				run.Context.UsageBudget.Bounder = func(context.Context, runcontext.UsageBoundRequest) (runcontext.UsageAmounts, error) {
					return runcontext.UsageAmounts{"outputTokens": 1}, nil
				}
			case "mutated-ceiling":
				run.Context.UsageBudget.Bounder = func(_ context.Context, in runcontext.UsageBoundRequest) (runcontext.UsageAmounts, error) {
					*in.EncodedOutputMaximum = 0
					return runcontext.UsageAmounts{"outputTokens": 0}, nil
				}
			case "missing-revision":
				run.Context.ConnectionRevisions = nil
			}
			_ = eino.New().Run(context.Background(), run)
			if calls != 0 {
				t.Fatal("unprovable token bound allowed physical dispatch")
			}
			refusals := usageHead(t, memory, "bound-"+mode).Get("usageBudget").Get("refusals")
			if refusals.Len() != 1 || refusals.Items()[0].Get("name").Str() != "UsageBudgetUnsupported" {
				t.Fatal("named unsupported bounds refusal missing")
			}
		})
	}
}
func TestUsageBudgetFeatureFreeGoDefaultsRemainUnchanged(t *testing.T) {
	memory := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
	run := usageRun(memory, "plain", usageStore(t, runcontext.MemoryUsageBudgetOptions{}), runcontext.UsageAmounts{"modelRequests": 1})
	run.Context.UsageBudget = nil
	run.Model = func(context.Context, string, relay.Request) (*http.Response, error) { return usageReply(), nil }
	var events []contract.RunEvent
	run.Emit = func(e contract.RunEvent) { events = append(events, e) }
	if err := eino.New().Run(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	value := usageHead(t, memory, "plain")
	if value.Get("version").Num() != 2 || value.Has("usageBudget") {
		t.Fatal("feature-free producer changed checkpoint bytes")
	}
	for _, e := range events {
		raw, _ := json.Marshal(e)
		v, _ := runcontext.ParseJSON(raw)
		if v.Has("version") && v.Get("version").Num() != 1 {
			t.Fatal("feature-free event version changed")
		}
	}
	cp, err := runcontext.ReadExecution(value)
	if err != nil {
		t.Fatal(err)
	}
	report, err := runcontext.ProjectRunReport(*cp)
	if err != nil || report.Get("version").Num() != 1 {
		t.Fatal("feature-free report version changed", err)
	}
}

type usageNativeSettle struct {
	runcontext.UsageBudgetStore
	calls int
	armed bool
}

func (s *usageNativeSettle) Settle(ctx context.Context, in runcontext.UsageSettlement) (runcontext.UsageBudgetReceipt, error) {
	s.calls++
	receipt, err := s.UsageBudgetStore.Settle(ctx, in)
	if err == nil && s.armed {
		s.armed = false
		return runcontext.UsageBudgetReceipt{}, errors.New("fixture lost native settlement ack")
	}
	return receipt, err
}

type usageNativeOutcome struct {
	runcontext.RunStore
	armed bool
}

func (s *usageNativeOutcome) Commit(ctx context.Context, in runcontext.RunCommit) (runcontext.RunReceipt, error) {
	receipt, err := s.RunStore.Commit(ctx, in)
	if err != nil {
		return receipt, err
	}
	cp, _ := runcontext.ParseJSON(in.Checkpoint)
	for _, row := range cp.Get("durable").Get("dispatches").Items() {
		if s.armed && row.Get("kind").Str() == "agent" && row.Get("state").Str() == "unknown" {
			s.armed = false
			return runcontext.RunReceipt{}, errors.New("fixture lost native outcome ack")
		}
	}
	return receipt, nil
}
func TestUsageBudgetActualNativeHandoffCallbackCounts(t *testing.T) {
	for _, mode := range []string{"complete", "no-callback", "startup-only", "outcome-ack", "settle-ack", "missing-release"} {
		t.Run(mode, func(t *testing.T) {
			memory := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
			monitored := &usageNativeSettle{UsageBudgetStore: usageStore(t, runcontext.MemoryUsageBudgetOptions{}), armed: mode == "settle-ack"}
			var budget runcontext.UsageBudgetStore = monitored
			var store runcontext.RunStore = memory
			if mode == "outcome-ack" {
				store = &usageNativeOutcome{RunStore: memory, armed: true}
			}
			id := "native-handoff-" + mode
			run := usageRun(store, id, budget, runcontext.UsageAmounts{"providerInvocations": 5})
			run.Interactive = true
			run.Context.ConnectionRevisions["fixture"] = "revision-1"
			g := gate.New(gate.Options{})
			g.SetOffer(contract.ToolOffer{Tools: []contract.ToolOfferRuntimeTool{}, HostTools: []contract.ToolOfferHostTool{turn.DelegationTool(turn.TaskProfiles)}})
			run.Gate = g.Calls()
			run.Children = func(context.Context, contract.DelegatedTaskProfile, func(contract.RunEvent)) (*gate.Calls, []contract.ToolOfferRuntimeTool, error) {
				return nil, nil, nil
			}
			calls := 0
			run.Agent = &adapter.Agent{Model: "fixture", Run: func(ctx context.Context, request adapter.AgentRequest, cb adapter.AgentCallbacks) error {
				calls++
				defer func() {
					if !(mode == "missing-release" && calls == 2) && cb.ProviderRelease != nil {
						cb.ProviderRelease.Acknowledge(cb.ProviderRelease.Invocation)
					}
				}()
				if mode == "no-callback" || mode == "startup-only" {
					if mode == "startup-only" {
						_ = cb.Event(ctx, adapter.AgentEvent{Type: "retry"})
					}
					return failure.Err("busy", "agent", "not-started")
				}
				if calls == 1 {
					for _, tool := range request.Tools {
						if strings.HasPrefix(tool.Description, "delegate_tasks:") {
							_, err := cb.Tool(ctx, adapter.AgentToolCall{ID: "handoff", Name: tool.Name, Arguments: json.RawMessage(`{"tasks":[{"title":"Native child","objective":"Check evidence","profile":"review"}]}`)})
							return err
						}
					}
					t.Fatal("native delegation offer missing")
				}
				return cb.Event(ctx, adapter.AgentEvent{Type: "message", ID: "answer", Text: "Done.", Phase: "final"})
			}}
			_ = delegate.With(native.New(), delegate.Options{}).Run(context.Background(), run)
			cp := usageHead(t, memory, id)
			entries := cp.Get("usageBudget").Get("entries")
			switch mode {
			case "complete":
				if calls != 3 || entries.Len() != 3 {
					t.Fatalf("acknowledged native handoff did not progress: calls=%d entries=%d refusals=%s", calls, entries.Len(), cp.Get("usageBudget").Get("refusals").Text())
				}
				for _, row := range entries.Items() {
					if row.Get("observed").Get("providerInvocations").Num() != 1 {
						t.Fatal("actual native callback did not count exact invocation")
					}
				}
				for _, row := range cp.Get("durable").Get("dispatches").Items() {
					if row.Get("kind").Str() == "agent" {
						if row.Get("state").Str() != "unknown" {
							t.Fatal("known count overwrote unknown model outcome")
						}
						break
					}
				}
			case "missing-release":
				if calls != 2 {
					t.Fatalf("missing release allowed continuation: calls=%d", calls)
				}
				var child string
				for _, row := range entries.Items() {
					if row.Get("reservation").Has("task") {
						child = row.Get("reservation").Get("id").Str()
					}
				}
				if child == "" {
					t.Fatal("native child not dispatched")
				}
				for _, row := range cp.Get("durable").Get("dispatches").Items() {
					if row.Get("id").Str() == child && row.Get("state").Str() != "unknown" {
						t.Fatal("native outcome acknowledged before release validation")
					}
				}
			default:
				if mode == "outcome-ack" && monitored.calls != 0 {
					t.Fatal("native settlement preceded original outcome acknowledgement")
				}
				if calls != 1 || entries.Len() != 1 {
					t.Fatalf("unknown native admission/ack allowed extra dispatch: calls=%d entries=%d", calls, entries.Len())
				}
				row := entries.Items()[0]

				if mode == "settle-ack" && row.Get("observed").Get("providerInvocations").Num() != 1 {
					t.Fatal("lost settlement erased known actual native count")
				}
				if (mode == "no-callback" || mode == "startup-only") && row.Has("observed") {
					t.Fatal("startup/status/error prose fabricated native admission")
				}
				if row.Get("state").Str() != "unknown" && row.Get("state").Str() != "claimed" {
					t.Fatal("unknown native acknowledgement released hold")
				}
				run.Context.RunStore.Resume = true
				_ = delegate.With(native.New(), delegate.Options{}).Run(context.Background(), run)
				if calls != 1 {
					t.Fatal("unknown native reservation replayed on resume")
				}
			}
		})
	}
}

func TestUsageBudgetActualModelLostSettlementCannotDispatchAgain(t *testing.T) {
	memory := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
	budget := &usageNativeSettle{UsageBudgetStore: usageStore(t, runcontext.MemoryUsageBudgetOptions{}), armed: true}
	run := usageRun(memory, "model-settle", budget, runcontext.UsageAmounts{"modelRequests": 3})
	calls := 0
	run.Model = func(context.Context, string, relay.Request) (*http.Response, error) {
		calls++
		return usageReply(), nil
	}
	_ = eino.New().Run(context.Background(), run)
	row := usageHead(t, memory, "model-settle").Get("usageBudget").Get("entries").Items()[0]
	if calls != 1 || row.Get("observed").Get("modelRequests").Num() != 1 {
		t.Fatal("lost settlement acknowledgement did not hold actual usage")
	}
	run.Context.RunStore.Resume = true
	_ = eino.New().Run(context.Background(), run)
	if calls != 1 {
		t.Fatal("lost settlement acknowledgement allowed forbidden physical dispatch")
	}
	if row.Get("state").Str() != "unknown" {
		t.Fatal("lost settlement acknowledgement did not hold actual usage")
	}
}
