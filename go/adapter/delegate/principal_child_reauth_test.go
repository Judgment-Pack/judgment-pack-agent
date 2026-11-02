package delegate_test

// ADR-0029 with #226 (#283): which of a child's calls reach the effect host's credential check, and what a refusal of one
// does. A child's source read never does. The package's own context and plan tools do, and so does a sourceless tool
// with delegation and a non-read effect. The refusal stops the whole run and parks it on the child's proof, and it fails
// closed: nothing is dispatched or counted, no delegation refusal is recorded, and nobody takes the grant. The twin of
// principalReauth.test.ts.

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/agent"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/delegate"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
)

const (
	childAdmit   = `{"admission":"admit"}`
	childWakeKey = "wk.delegate.child.Q7z2Xm9LpV4cR8tNb3Yh6Df1G"
)

type childHost struct {
	*resumeFixture
	store  reauthStore
	host   *runcontext.ReferenceEffectHost
	view   runcontext.RunStore
	fence  runcontext.LeaseFence
	checks atomic.Int32
	refuse atomic.Bool
	at     atomic.Int64
}

func newChildHost(t *testing.T) *childHost {
	t.Helper()
	f := &childHost{resumeFixture: newResumeFixture(t)}
	f.at.Store(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli())
	f.store = reauthStore{runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{Now: func() time.Time { return time.UnixMilli(f.at.Load()).UTC() }})}
	f.refuse.Store(true)
	f.host = runcontext.NewReferenceEffectHostWith(f.store, runcontext.ReferenceEffectHostOptions{Credentials: func(context.Context, runcontext.EffectOperation) (*runcontext.CredentialRefusal, error) {
		f.checks.Add(1)
		if f.refuse.Load() {
			return &runcontext.CredentialRefusal{Cause: "expired", Connection: "notes-conn", Principal: "sha256:" + strings.Repeat("7", 64), Permissions: []string{"notes:write"}, Requires: []string{"notes:write"}}, nil
		}
		return nil, nil
	}})
	opened, err := runcontext.OpenPrincipal(f.principal(0, childAdmit))
	if err != nil {
		t.Fatal(err)
	}
	lease, err := f.store.AcquireLease(context.Background(), runcontext.LeaseAcquire{Resource: "handoff", Owner: "worker-a", DurationMs: 3_600_000, Principal: opened.Record.Binding()})
	if err != nil {
		t.Fatal(err)
	}
	f.fence = runcontext.FenceOf(lease)
	if f.view, err = runcontext.UnattendedRunStore(f.store, f.fence, f.host); err != nil {
		t.Fatal(err)
	}
	return f
}

// run drives the parent's delegation through the real coordinator, with the child's gate holding the tools the
// boundary enrolls. parentFirst is the parent's first turn, and call is what the child calls.
func (f *childHost) run(t *testing.T, view runcontext.RunStore, resume bool, parentFirst func(context.Context, adapter.AgentRequest, adapter.AgentCallbacks) error, tools []runcontext.HostTool, call, args string) ([]contract.RunEvent, error) {
	t.Helper()
	var events []contract.RunEvent
	parents := 0
	var childGate *gate.Gate
	var parentHosts []contract.ToolOfferHostTool
	run := lifecycleRun(view, "handoff", resume, func(ctx context.Context, request adapter.AgentRequest, cb adapter.AgentCallbacks) error {
		if cb.ProviderRelease != nil {
			defer cb.ProviderRelease.Acknowledge(cb.ProviderRelease.Invocation)
		}
		parent := false
		for _, held := range request.Tools {
			parent = parent || strings.HasPrefix(held.Description, "delegate_tasks:")
		}
		if parent {
			parents++
			if parents == 1 && parentFirst != nil {
				if err := parentFirst(ctx, request, cb); err != nil {
					return err
				}
			}
			return cb.Event(ctx, adapter.AgentEvent{Type: "message", ID: "reply", Text: "Done.", Phase: "final"})
		}
		f.mu.Lock()
		f.childRuns++
		f.mu.Unlock()
		for _, tool := range request.Tools {
			if strings.HasPrefix(tool.Description, call+":") {
				if _, err := cb.Tool(ctx, adapter.AgentToolCall{ID: "child-wire", Name: tool.Name, Arguments: json.RawMessage(args)}); err != nil {
					return err
				}
				return cb.Event(ctx, adapter.AgentEvent{Type: "message", ID: "child", Text: "One finding.", Phase: "final"})
			}
		}
		t.Errorf("the child was not offered %s", call)
		return nil
	}, func(event contract.RunEvent) {
		f.mu.Lock()
		events = append(events, event)
		f.mu.Unlock()
	})
	run.Context.Principal = f.principal(0, childAdmit)
	run.HostTools = tools
	parentGate := gate.New(gate.Options{})
	parentGate.SetOffer(contract.ToolOffer{})
	run.Gate = parentGate.Calls()
	run.Children = func(_ context.Context, profile contract.DelegatedTaskProfile, emit func(contract.RunEvent)) (*gate.Calls, []contract.ToolOfferRuntimeTool, error) {
		childGate = gate.New(gate.Options{Granted: gate.Ceiling, Emit: emit})
		childGate.SetOffer(contract.ToolOffer{})
		childGate.Enter(gate.ChildPhase(profile, contract.ToolOffer{}, gate.LoopPhase(contract.ToolOffer{HostTools: parentHosts})))
		return childGate.Calls(), nil, nil
	}
	run.EnrollContext = func(_ context.Context, offer adapter.ContextOffer) error {
		if offer.Mode == gate.PhaseChild {
			return childGate.EnrollContext(offer.ID, offer.HostTools)
		}
		parentHosts = offer.HostTools
		return parentGate.EnrollContext(offer.ID, offer.HostTools)
	}
	err := delegate.With(agent.New(), delegate.Options{}).Run(context.Background(), run)
	return events, err
}

func (f *childHost) park(t *testing.T, proof *runcontext.ReauthorizationRequired) runcontext.RunWait {
	t.Helper()
	digest, _ := runcontext.WakeKeyDigest(childWakeKey)
	wait, _, err := runcontext.ParkRun(context.Background(), runcontext.ParkInput{Store: f.store, Fence: f.fence, Wait: "wait-r", Continuation: "continue-r", Condition: runcontext.WaitCondition{Event: &contract.RunWaitEventCondition{Kind: "reauthorized", Subject: "notes-conn"}}, Model: runcontext.PinnedModel{Provider: "openai", Model: "fixture"}, WakeKeyDigest: digest, Release: []runcontext.WaitRelease{}, Reauthorization: proof})
	if err != nil {
		t.Fatal(err)
	}
	return wait
}

func childTaskStates(events []contract.RunEvent) []string {
	out := []string{}
	for _, event := range events {
		if event.DelegatedTask != nil {
			out = append(out, string(event.DelegatedTask.Task.State))
		}
	}
	return out
}

func childDelegationRefusals(events []contract.RunEvent) int {
	n := 0
	for _, event := range events {
		if event.Guardrail != nil && event.Guardrail.Reason != nil && *event.Guardrail.Reason == "delegation refused" {
			n++
		}
	}
	return n
}

func (f *childHost) counted() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, read := range f.reads {
		if read.Call.Count {
			n++
		}
	}
	return n
}

func (f *childHost) proposeTool(ran *atomic.Int32) runcontext.HostTool {
	return runcontext.HostTool{Name: "local_propose", Description: "local_propose", InputSchema: json.RawMessage(`{"type":"object"}`), Delegation: "read", Taxonomy: &runcontext.ToolTaxonomy{Operation: "propose", Effect: "propose"}, Execute: func(context.Context, json.RawMessage) (runcontext.ToolResult, error) {
		ran.Add(1)
		return runcontext.ResultOf([]runcontext.Content{runcontext.TextContent("proposed")}, nil), nil
	}}
}

func TestAChildsSourceReadNeverReachesTheCredentialCheck(t *testing.T) {
	f := newChildHost(t)
	events, err := f.run(t, f.view, false, lifecycleDelegate, []runcontext.HostTool{f.resumeFixture.host}, "notes__read_note", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	states := childTaskStates(events)
	if f.checks.Load() != 0 || runcontext.ReauthorizationOf(f.view) != nil || f.dispatched != 1 || states[len(states)-1] != "complete" {
		t.Fatalf("checks %d, proof %v, dispatched %d, tasks %v", f.checks.Load(), runcontext.ReauthorizationOf(f.view), f.dispatched, states)
	}
}

func TestAChildRefusedForItsCredentialsStopsTheRunAndFailsClosed(t *testing.T) {
	ran := &atomic.Int32{}
	for _, row := range []struct{ name, args, proofTool string }{
		{"list_context", `{}`, "list_context"},
		{"update_plan", `{"plan":[{"step":"One","status":"in_progress"}],"explanation":"x"}`, "update_plan"},
		{"complete_plan_step", `{"step":"One"}`, "complete_plan_step"},
		{"request_budget_increase", `{"requests":1,"reason":"x"}`, "request_budget_increase"},
		{"local_propose", `{}`, "local_propose"},
	} {
		t.Run(row.name, func(t *testing.T) {
			ran.Store(0)
			f := newChildHost(t)
			tools := []runcontext.HostTool{f.resumeFixture.host}
			if row.name == "local_propose" {
				tools = append(tools, f.proposeTool(ran))
			}
			events, err := f.run(t, f.view, false, lifecycleDelegate, tools, row.name, row.args)
			if err != nil {
				t.Fatal(err)
			}
			proof := runcontext.ReauthorizationOf(f.view)
			if f.checks.Load() != 1 || proof == nil {
				t.Fatalf("checks %d, proof %v", f.checks.Load(), proof)
			}
			wait := f.park(t, proof)
			if operation := wait.Reauthorization.Operation; operation.Source != "host" || operation.Tool != row.proofTool {
				t.Fatalf("the wait names %+v", operation)
			}
			if ran.Load() != 0 || f.dispatched != 0 || f.counted() != 0 || childDelegationRefusals(events) != 0 {
				t.Fatalf("ran %d, dispatched %d, counted %d, refusals %d", ran.Load(), f.dispatched, f.counted(), childDelegationRefusals(events))
			}
			states := childTaskStates(events)
			if len(states) == 0 || states[len(states)-1] != "failed" {
				t.Fatalf("the child's task: %v", states)
			}
			for _, event := range events {
				if event.Message != nil && event.Message.Text == "Done." {
					t.Fatal("the parent went on past its child's refusal")
				}
			}
		})
	}
}

func TestAChildsGrantIsNotTakenAfterItsResume(t *testing.T) {
	ctx := context.Background()
	f := newChildHost(t)
	tools := []runcontext.HostTool{f.resumeFixture.host}
	if _, err := f.run(t, f.view, false, lifecycleDelegate, tools, "list_context", `{}`); err != nil {
		t.Fatal(err)
	}
	proof := runcontext.ReauthorizationOf(f.view)
	if proof == nil {
		t.Fatal("no proof")
	}
	wait := f.park(t, proof)
	if wait.Reauthorization.Operation.Tool != "list_context" {
		t.Fatalf("the wait's operation: %+v", wait.Reauthorization.Operation)
	}
	f.at.Add(60_000)
	if _, err := runcontext.WakeRun(ctx, f.store, runcontext.WakeEvidence{Version: 1, Resource: "handoff", Wait: "wait-r", Evidence: "refreshed-1", Kind: "event", Event: &contract.RunWaitEventCondition{Kind: "reauthorized", Subject: "notes-conn"}, Key: childWakeKey}); err != nil {
		t.Fatal(err)
	}
	evidence := runcontext.ResumeEvidence{Version: 1, Resource: "handoff", Wait: "wait-r", Model: contract.RunResumeCurrentModel{Provider: "openai", Model: "fixture", Status: "available"}, Policy: contract.RunResumeCurrentPolicy{Digest: wait.Pins.Policy}, Plan: wait.Pins.Plan, Sources: wait.Pins.Sources, Artifacts: wait.Pins.Artifacts, Principal: wait.Pins.Principal}
	action := runcontext.ReauthAction{Version: 1, Resource: "handoff", Wait: "wait-r", Connection: "notes-conn", Action: "refresh", Principal: "sha256:" + strings.Repeat("7", 64), Permissions: []string{"notes:write"}, Actor: "person-owner", At: "2026-01-01T00:00:30.000Z"}
	resumed, err := runcontext.ResumeParked(ctx, runcontext.ResumeInput{Store: f.store, Resource: "handoff", Wait: "wait-r", Evidence: "refreshed-1", Owner: "worker-b", DurationMs: 3_600_000, Current: &evidence, Reauthorization: &action})
	if err != nil {
		t.Fatal(err)
	}
	f.refuse.Store(false)
	view, err := runcontext.ReauthorizedRunStore(ctx, f.store, f.host, resumed)
	if err != nil {
		t.Fatal(err)
	}
	childRuns, asked, checks := f.childRuns, f.asked, f.checks.Load()
	parentCalls := func(ctx context.Context, request adapter.AgentRequest, cb adapter.AgentCallbacks) error {
		for _, tool := range request.Tools {
			if strings.HasPrefix(tool.Description, "list_context:") {
				_, err := cb.Tool(ctx, adapter.AgentToolCall{ID: "parent-wire", Name: tool.Name, Arguments: json.RawMessage(`{}`)})
				return err
			}
		}
		t.Error("the parent was not offered list_context")
		return nil
	}
	events, err := f.run(t, view, true, parentCalls, tools, "list_context", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if f.childRuns != childRuns || f.asked != asked || f.checks.Load() != checks+1 {
		t.Fatalf("the child ran again (%d), asked for a link (%d), or the checks moved by %d and not the parent's one", f.childRuns-childRuns, f.asked-asked, f.checks.Load()-checks)
	}
	if states := childTaskStates(events); len(states) != 1 || states[0] != "interrupted" {
		t.Fatalf("the restored child is reported %v", states)
	}
	head, err := f.store.Load(ctx, "handoff")
	if err != nil || head == nil {
		t.Fatal(head, err)
	}
	var saved struct {
		Durable struct {
			Dispatches []struct {
				Kind, Tool, State string
				Scope             struct{ Task string }
				Failure           struct{ Outcome string }
			}
		}
	}
	if err := json.Unmarshal(head.Checkpoint, &saved); err != nil {
		t.Fatal(err)
	}
	type row struct {
		tool, state string
		child       bool
	}
	got := []row{}
	for _, held := range saved.Durable.Dispatches {
		if held.Kind == "host" && held.Tool == "list_context" {
			got = append(got, row{held.Tool, held.State, held.Scope.Task != ""})
			if held.Scope.Task != "" && held.Failure.Outcome != "not-started" {
				t.Fatalf("the child's row: %+v", held)
			}
		}
	}
	// The grant goes to nobody: the child's row stays unknown for good, and the parent's own call is a new row.
	if len(got) != 2 || got[0] != (row{"list_context", "unknown", true}) || got[1] != (row{"list_context", "result", false}) {
		t.Fatalf("the rows: %+v", got)
	}
	if f.counted() != 0 {
		t.Fatalf("counted %d", f.counted())
	}
}
