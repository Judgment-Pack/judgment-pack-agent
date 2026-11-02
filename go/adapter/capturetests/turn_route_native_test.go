package capturetests

import (
	"context"
	"encoding/json"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	native "github.com/Judgment-Pack/judgment-pack-agent/go/adapter/agent"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
	"strings"
	"testing"
)

func nativeRouteRun(t *testing.T, store runcontext.RunStore, run adapter.AgentRun, resume bool) *adapter.Run {
	t.Helper()
	tier := "low"
	host := runcontext.HostTool{Name: "lookup", Description: "Read one value.", Effect: "read", Taxonomy: &runcontext.ToolTaxonomy{Operation: "read", Effect: "read"}, InputSchema: json.RawMessage(`{"type":"object","properties":{}}`), Execute: func(context.Context, json.RawMessage) (runcontext.ToolResult, error) {
		t.Fatal("withheld tool executed")
		return runcontext.ToolResult{}, nil
	}}
	g := gate.New(gate.Options{})
	g.SetOffer(contract.ToolOffer{Tools: []contract.ToolOfferRuntimeTool{}, HostTools: []contract.ToolOfferHostTool{host.Declaration()}})
	return &adapter.Run{ModelName: "native", Prompt: "Answer.", AllowConversation: true, Critic: "off", Gate: g.Calls(), HostTools: []runcontext.HostTool{host}, Policy: runcontext.PolicyLayer{"version": 2, "routing": map[string]any{"mode": "rules", "tiers": []string{"low"}}}, Route: &runcontext.RouteSelection{Route: "agent", Tier: &tier, Skills: []string{}, Tools: []string{}, Operations: "all", Planning: "withheld"}, Context: &runcontext.Input{CurrentRequest: &runcontext.CurrentRequest{Message: "Answer", Input: "Fixed"}, Connection: "provider", ConnectionRevisions: map[string]string{"provider": "one"}, RunStore: &runcontext.DurableRunInput{Store: store, ID: "native-route", Resume: resume}}, Agent: &adapter.Agent{Model: "native", Run: run}, Emit: func(contract.RunEvent) {}}
}
func TestNativeRouteActualWidenAndRestoredFullRequest(t *testing.T) {
	ctx := context.Background()
	store := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
	var requests []adapter.AgentRequest
	var prior adapter.AgentCallbacks
	send := func(ctx context.Context, request adapter.AgentRequest, callbacks adapter.AgentCallbacks) error {
		requests = append(requests, request)
		if len(requests) == 1 {
			prior = callbacks
			err := callbacks.CapabilityNeeded(ctx, adapter.RouteCapabilityNeed{Kind: "tool", Name: "lookup"})
			if !callbacks.ProviderRelease.Acknowledge(callbacks.ProviderRelease.Invocation) {
				t.Fatal("release refused")
			}
			return err
		}
		return callbacks.Event(ctx, adapter.AgentEvent{Type: "message", ID: "done", Text: "Done.", Phase: "final"})
	}
	// A process interruption leaves the original running checkpoint. Close the owning context
	// after acknowledged widening, before a full physical invocation is admitted.
	first := nativeRouteRun(t, store, send, false)
	named := runcontext.RunModels{Critic: "native", Child: "native"}
	opened, err := runcontext.ContextualSession(ctx, runcontext.Session{Prompt: first.Prompt, TestPrompt: first.TestPrompt, AllowConversation: first.AllowConversation, Critic: first.Critic, HostTools: first.HostTools, Context: first.Context, Policy: first.Policy, Route: first.Route, Agent: &runcontext.AgentSession{Model: "native"}, PolicyLegacyModels: &named, PolicyFamilies: adapter.PolicyFamilies(first, named), PolicyConnections: append(adapter.PolicyConnectionIDs(first), named.Critic, named.Child)})
	if err != nil {
		t.Fatal(err)
	}
	c := opened.Context
	narrow := adapter.AgentRequest{Prompt: "Question", Instructions: "Answer.", Tools: []adapter.AgentTool{}, Phase: "author", Model: "native"}
	body, _ := json.Marshal(narrow)
	err = c.InvokeNativeRoute(ctx, "author", c.ProviderStorageIdentity("author"), "native", body, func(private context.Context, raw []byte, release *adapter.ProviderRelease) error {
		var request adapter.AgentRequest
		if err := json.Unmarshal(raw, &request); err != nil {
			return err
		}
		return send(ctx, request, adapter.AgentCallbacks{ProviderRelease: release, CapabilityNeeded: func(_ context.Context, need adapter.RouteCapabilityNeed) error {
			return c.NativeRouteCapability(private, need.Kind, need.Name)
		}, Event: func(context.Context, adapter.AgentEvent) error { return nil }})
	})
	if !runcontext.IsRouteCapabilityNeeded(err) {
		t.Fatal("live native capability request refused", err)
	}
	if err = c.WidenTurnRoute(ctx); err != nil {
		t.Fatal(err)
	}
	c.Close()
	if len(requests) != 1 {
		t.Fatalf("physical calls before resume=%d", len(requests))
	}
	head := usageHead(t, store, "native-route")
	marker := head.Get("turnRoute").Get("widenedInvocation")
	if !head.Get("turnRoute").Get("widened").Truth() || marker.Get("dispatchId").Str() == "" {
		t.Fatal("exact widened claim missing", head.Text())
	}
	old := head.Get("durable").Get("dispatches").Items()[0]
	if old.Get("id").Str() != marker.Get("dispatchId").Str() || old.Get("requestDigest").Str() != marker.Get("requestDigest").Str() || old.Get("state").Str() != "unknown" {
		t.Fatal("superseded audit row changed")
	}
	if err := prior.CapabilityNeeded(ctx, adapter.RouteCapabilityNeed{Kind: "tool", Name: "lookup"}); err == nil {
		t.Fatal("late callback accepted")
	}
	if prior.ProviderRelease.Acknowledge(prior.ProviderRelease.Invocation) {
		t.Fatal("late release accepted")
	}
	history, e := store.ReadHistoryHead(ctx, "native-route")
	if e != nil {
		t.Fatal(e)
	}
	feedback := runcontext.RunFeedback{Version: 1, ID: "rating", RunID: "native-route", Sequence: 1, Target: runcontext.FeedbackTarget{Kind: "run"}, Rating: "good", Actor: "user", RecordedAt: "2026-10-25T01:02:03.000Z"}
	if _, e = store.CommitHistoryMetadata(ctx, runcontext.HistoryMetadataCommit{RunID: "native-route", ExpectedHistoryRevision: history.Revision, CommitID: "feedback-1", Record: runcontext.HistoryMetadata{Version: 1, Kind: "feedback", Feedback: &feedback}}); e != nil {
		t.Fatal(e)
	}
	resumed := nativeRouteRun(t, store, send, true)
	resumed.Emit = func(e contract.RunEvent) {
		if e.Error != nil {
			raw, _ := json.Marshal(e)
			t.Errorf("supported restart refused: %s", raw)
		}
	}
	if err := native.New().Run(ctx, resumed); err != nil {
		t.Fatal("supported restart refused", err)
	}
	if len(requests) != 2 {
		t.Fatalf("physical calls=%d want2", len(requests))
	}
	raw0, _ := json.Marshal(requests[0])
	raw1, _ := json.Marshal(requests[1])
	if strings.Contains(string(raw0), "lookup") || !strings.Contains(string(raw1), "lookup") {
		t.Fatal("old narrow request redispatched or full offer missing", string(raw0), string(raw1))
	}
	final := usageHead(t, store, "native-route")
	rows := final.Get("durable").Get("dispatches").Items()
	if len(rows) != 2 || rows[1].Get("id").Str() == old.Get("id").Str() || rows[0].Get("state").Str() != "unknown" {
		t.Fatal("new full claim did not preserve old audit identity")
	}
}

func TestNativeRouteActualFreshUsageTickets(t *testing.T) {
	ctx := context.Background()
	store := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
	budget := usageStore(t, runcontext.MemoryUsageBudgetOptions{})
	sends := 0
	run := nativeRouteRun(t, store, func(ctx context.Context, _ adapter.AgentRequest, cb adapter.AgentCallbacks) error {
		sends++
		if sends == 1 {
			err := cb.CapabilityNeeded(ctx, adapter.RouteCapabilityNeed{Kind: "tool", Name: "lookup"})
			cb.ProviderRelease.Acknowledge(cb.ProviderRelease.Invocation)
			return err
		}
		return cb.Event(ctx, adapter.AgentEvent{Type: "message", ID: "done", Text: "Done.", Phase: "final"})
	}, false)
	run.Context.UsageBudget = &runcontext.UsageBudgetInput{Store: budget, Scope: usageScope(runcontext.UsageAmounts{"providerInvocations": 3})}
	run.Emit = func(e contract.RunEvent) {
		if e.Error != nil {
			raw, _ := json.Marshal(e)
			t.Error(string(raw))
		}
	}
	if err := native.New().Run(ctx, run); err != nil {
		t.Fatal(err)
	}
	if sends != 2 {
		t.Fatalf("physical sends=%d want2", sends)
	}
	cp := usageHead(t, store, "native-route")
	rows := cp.Get("durable").Get("dispatches").Items()
	entries := cp.Get("usageBudget").Get("entries").Items()
	if len(rows) != 2 || len(entries) != 2 || rows[0].Get("state").Str() != "unknown" || rows[0].Get("id").Str() == rows[1].Get("id").Str() {
		t.Fatal("fresh full claim or old unknown audit missing", cp.Text())
	}
	for i, row := range rows {
		if row.Get("id").Str() != entries[i].Get("reservation").Get("id").Str() || entries[i].Get("observed").Get("providerInvocations").Num() != 1 {
			t.Fatal("exact actual physical reservation/count missing", cp.Text())
		}
	}
}

func TestNativeRouteOffExactRequestBytesAndNilTools(t *testing.T) {
	var bodies [][]byte
	for _, mode := range []string{"absent", "off"} {
		store := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
		run := nativeRouteRun(t, store, func(ctx context.Context, request adapter.AgentRequest, cb adapter.AgentCallbacks) error {
			raw, _ := json.Marshal(request)
			bodies = append(bodies, raw)
			return cb.Event(ctx, adapter.AgentEvent{Type: "message", ID: "done", Text: "Done.", Phase: "final"})
		}, false)
		run.Route = nil
		run.Policy = nil
		run.Tools = nil
		run.HostTools = nil
		if mode == "off" {
			run.Policy = runcontext.PolicyLayer{"version": 2, "routing": map[string]any{"mode": "off", "tiers": []string{"low"}}}
		}
		run.Emit = func(e contract.RunEvent) {
			if e.Error != nil {
				raw, _ := json.Marshal(e)
				t.Error(string(raw))
			}
		}
		if err := native.New().Run(context.Background(), run); err != nil {
			t.Fatal(err)
		}
	}
	if len(bodies) != 2 || string(bodies[0]) != string(bodies[1]) {
		t.Fatalf("off request bytes changed: %s / %s", bodies[0], bodies[1])
	}
}
