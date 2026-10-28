package runcontext

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Judgment-Pack/judgment-pack-agent/go/internal/providerrelease"
	"sync"
	"testing"
)

func nativeRouteContext(t *testing.T, id string, given ...RunStore) *SessionContext {
	t.Helper()
	tier := "low"
	var store RunStore = NewMemoryRunStore(MemoryRunStoreOptions{})
	if len(given) > 0 {
		store = given[0]
	}
	session := Session{Prompt: "Question", AllowConversation: true, Critic: "off", Policy: PolicyLayer{"version": 2, "routing": map[string]any{"mode": "rules", "tiers": []string{"low"}}}, Route: &RouteSelection{Route: "agent", Tier: &tier, Skills: []string{}, Tools: []string{}, Operations: "all", Planning: "withheld"}, Agent: &AgentSession{Model: "native"}, PolicyFamilies: map[string]string{"native": "codex"}, Context: &Input{RunStore: &DurableRunInput{Store: store, ID: id}, CurrentRequest: &CurrentRequest{Message: "Question", Input: "Fixed"}}, HostTools: []HostTool{{Name: "lookup", Description: "Read.", Effect: "read", Taxonomy: &ToolTaxonomy{Operation: "read", Effect: "read"}, InputSchema: json.RawMessage(`{"type":"object","properties":{}}`), Execute: func(context.Context, json.RawMessage) (ToolResult, error) {
		t.Fatal("tool ran")
		return ToolResult{}, nil
	}}}}
	run, err := ContextualSession(context.Background(), session)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(run.Context.Close)
	return run.Context
}
func TestNativeRoutePrivateAttemptForeignLateConcurrent(t *testing.T) {
	c := nativeRouteContext(t, "one")
	foreign := nativeRouteContext(t, "two")
	ctx := context.Background()
	entered := make(chan context.Context, 1)
	resume := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- c.InvokeNativeRoute(ctx, "author", "provider", "native", []byte(`{"question":"one"}`), func(private context.Context, _ []byte, release *providerrelease.Capability) error {
			entered <- private
			<-resume
			err := c.NativeRouteCapability(private, "tool", "lookup")
			release.Acknowledge(release.Invocation)
			return err
		})
	}()
	private := <-entered
	if err := foreign.NativeRouteCapability(private, "tool", "lookup"); err == nil || IsRouteCapabilityNeeded(err) || foreign.RoutePending() {
		t.Fatal("foreign owner accepted private attempt")
	}
	sends := 0
	if err := c.InvokeNativeRoute(ctx, "author", "provider", "native", []byte(`{"question":"two"}`), func(context.Context, []byte, *providerrelease.Capability) error { sends++; return nil }); err == nil || sends != 0 {
		t.Fatal("concurrent attempt entered host")
	}
	close(resume)
	if err := <-done; !IsRouteCapabilityNeeded(err) {
		t.Fatal("exact live callback refused", err)
	}
	if err := c.NativeRouteCapability(private, "tool", "lookup"); err == nil || IsRouteCapabilityNeeded(err) {
		t.Fatal("late private attempt accepted")
	}
	if err := c.WidenTurnRoute(ctx); err != nil {
		t.Fatal("acknowledged live attempt refused widening", err)
	}
}
func TestNativeRoutePrivateAttemptClosesOrdinaryReturn(t *testing.T) {
	c := nativeRouteContext(t, "closed")
	var private context.Context
	if err := c.InvokeNativeRoute(context.Background(), "author", "provider", "native", []byte(`{"question":"one"}`), func(ctx context.Context, _ []byte, _ *providerrelease.Capability) error { private = ctx; return nil }); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := c.NativeRouteCapability(private, "tool", "lookup"); err == nil || IsRouteCapabilityNeeded(err) {
				t.Error("late concurrent callback accepted")
			}
		}()
	}
	wg.Wait()
	if c.RoutePending() {
		t.Fatal("late concurrent callback changed route state")
	}
}

func nativeRouteNeed(c *SessionContext, ctx context.Context, _ []byte, release *providerrelease.Capability) error {
	err := c.NativeRouteCapability(ctx, "tool", "lookup")
	release.Acknowledge(release.Invocation)
	return err
}
func TestNativeRouteUnrelatedUnknownStillRefuses(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryRunStore(MemoryRunStoreOptions{})
	c := nativeRouteContext(t, "uncertain", store)
	if err := c.InvokeNativeRoute(ctx, "author", "provider", "native", []byte(`{"question":"unrelated"}`), func(context.Context, []byte, *providerrelease.Capability) error {
		return errors.New("fixture unknown outcome")
	}); err == nil {
		t.Fatal("fixture uncertainty missing")
	}
	if err := c.InvokeNativeRoute(ctx, "author", "provider", "native", []byte(`{"question":"needed"}`), func(ctx context.Context, raw []byte, release *providerrelease.Capability) error {
		return nativeRouteNeed(c, ctx, raw, release)
	}); !IsRouteCapabilityNeeded(err) {
		t.Fatal(err)
	}
	if err := c.WidenTurnRoute(ctx); err != nil {
		t.Fatal(err)
	}
	head, _ := store.Load(ctx, "uncertain")
	w, err := NewRunWriter(store, "uncertain", head, nil)
	if err != nil {
		t.Fatal(err)
	}
	binding := RunBinding{Writer: w, Scope: DurableScope{Execution: "uncertain", Role: "author"}, Root: true}
	c.turnRoute.mu.RLock()
	route := *c.turnRoute.route
	c.turnRoute.mu.RUnlock()
	if err = w.restoreNativeRoute(ctx, binding, route); err != nil {
		t.Fatal(err)
	}
	if _, err = PrepareDurableRequest(&binding, "agent", "provider", "native", []byte(`{"question":"full"}`)); err == nil {
		t.Fatal("unrelated unknown native claim bypassed")
	}
}
func TestNativeRouteLostWidenAcknowledgementRefusesActiveWriter(t *testing.T) {
	ctx := context.Background()
	store := &historyWriterFaultStore{MemoryRunStore: NewMemoryRunStore(MemoryRunStoreOptions{}), armed: true}
	store.mutate = func(_ context.Context, input RunHistoryCommit, _ *RunHistoryReceipt) error {
		for _, raw := range input.Run.Events {
			v, _ := ParseJSON(raw)
			if v.Get("event").Get("type").Str() == "route_widened" {
				return errors.New("fixture lost widening ACK")
			}
		}
		return nil
	}
	c := nativeRouteContext(t, "lost", store)
	if err := c.InvokeNativeRoute(ctx, "author", "provider", "native", []byte(`{"question":"needed"}`), func(ctx context.Context, raw []byte, release *providerrelease.Capability) error {
		return nativeRouteNeed(c, ctx, raw, release)
	}); !IsRouteCapabilityNeeded(err) {
		t.Fatal(err)
	}
	if err := c.WidenTurnRoute(ctx); err == nil {
		t.Fatal("lost widening ACK exposed route")
	}
	sends := 0
	if err := c.InvokeNativeRoute(ctx, "author", "provider", "native", []byte(`{"question":"full"}`), func(context.Context, []byte, *providerrelease.Capability) error { sends++; return nil }); err == nil || sends != 0 {
		t.Fatal("lost widening ACK dispatched full request")
	}
}

func TestNativeRouteReleaseProofRefusals(t *testing.T) {
	for _, mode := range []string{"missing", "foreign", "duplicate"} {
		t.Run(mode, func(t *testing.T) {
			c := nativeRouteContext(t, "release-"+mode)
			ctx := context.Background()
			err := c.InvokeNativeRoute(ctx, "author", "provider", "native", []byte(`{"question":"needed"}`), func(ctx context.Context, _ []byte, release *providerrelease.Capability) error {
				err := c.NativeRouteCapability(ctx, "tool", "lookup")
				switch mode {
				case "foreign":
					other, _ := providerrelease.NewReleaseAttempt()
					release.Acknowledge(other.Invocation)
				case "duplicate":
					release.Acknowledge(release.Invocation)
					release.Acknowledge(release.Invocation)
				}
				return err
			})
			if err == nil {
				t.Fatal("invalid release accepted")
			}
			if err = c.WidenTurnRoute(ctx); err == nil {
				t.Fatal("invalid release widened catalogue")
			}
		})
	}
}
func TestNativeRouteSelectedSkillPreloadOnStartupAndResume(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryRunStore(MemoryRunStoreOptions{})
	skill := vectorSkills(t)[1]
	tier := "low"
	input := func(resume bool) Session {
		return Session{Prompt: "Question", AllowConversation: true, Critic: "off", Agent: &AgentSession{Model: "native"}, PolicyFamilies: map[string]string{"native": "codex"}, Policy: PolicyLayer{"version": 2, "routing": map[string]any{"mode": "rules", "tiers": []string{"low"}}}, Route: &RouteSelection{Route: "agent", Tier: &tier, Skills: []string{skill.ID}, Tools: []string{}, Operations: "all", Planning: "withheld"}, Skills: []Skill{skill}, Context: &Input{CurrentRequest: &CurrentRequest{Message: "Question", Input: "Fixed"}, RunStore: &DurableRunInput{Store: store, ID: "skill-route", Resume: resume}}}
	}
	for _, resume := range []bool{false, true} {
		run, err := ContextualSession(ctx, input(resume))
		if err != nil {
			t.Fatal(err)
		}
		loads := run.Context.Execution.State().Skills
		if len(loads) != 1 || loads[0].ID != skill.ID {
			t.Fatal("selected skill lost across interrupted startup", loads)
		}
		run.Context.Close()
	}
}

func TestNativeRouteTransplantedCheckpointCannotGrantDisposition(t *testing.T) {
	ctx := context.Background()
	source := NewMemoryRunStore(MemoryRunStoreOptions{})
	c := nativeRouteContext(t, "transplanted", source)
	if err := c.InvokeNativeRoute(ctx, "author", "provider", "native", []byte(`{"question":"needed"}`), func(ctx context.Context, raw []byte, release *providerrelease.Capability) error {
		return nativeRouteNeed(c, ctx, raw, release)
	}); !IsRouteCapabilityNeeded(err) {
		t.Fatal(err)
	}
	if err := c.WidenTurnRoute(ctx); err != nil {
		t.Fatal(err)
	}
	head, _ := source.Load(ctx, "transplanted")
	foreign := NewMemoryRunStore(MemoryRunStoreOptions{})
	if _, err := foreign.Commit(ctx, RunCommit{RunID: "transplanted", CommitID: "transplanted-checkpoint", Checkpoint: head.Checkpoint}); err != nil {
		t.Fatal(err)
	}
	retained, _ := foreign.Load(ctx, "transplanted")
	w, err := NewRunWriter(foreign, "transplanted", retained, nil)
	if err != nil {
		t.Fatal(err)
	}
	binding := RunBinding{Writer: w, Scope: DurableScope{Execution: "transplanted", Role: "author"}, Root: true}
	c.turnRoute.mu.RLock()
	route := *c.turnRoute.route
	c.turnRoute.mu.RUnlock()
	if err = w.restoreNativeRoute(ctx, binding, route); err == nil {
		t.Fatal("transplanted marker without execution transaction granted disposition")
	}
}
