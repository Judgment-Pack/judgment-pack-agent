package adapter_test

// ADR-0029 with #226: a run under a delegation that parks for re-authorization, through the real eino and agent
// adapters, the writer and the reference effect host. A credential refusal is not a delegation refusal and counts
// nothing; a re-send must still pass the delegation's admission, and is counted once at its physical dispatch. The
// twin of packages/harness/principalReauth.test.ts.

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
)

func delegatedWriteTool(executed *atomic.Int32) runcontext.HostTool {
	tool := runcontext.HostTool{Name: "write_note", Description: "Write one note.", Taxonomy: &runcontext.ToolTaxonomy{Operation: "act", Effect: "act"}, EffectClass: "act", Approval: "required", InputSchema: json.RawMessage(`{"type":"object"}`),
		Approve: func(runcontext.ApprovalRequest) string { return "allow" }, Execute: hostExecute(executed, nil)}
	digest := runcontext.ToolListDigest([]runcontext.ListedTool{{Name: tool.Name, Description: tool.Description, InputSchema: tool.InputSchema, Taxonomy: tool.Taxonomy}})
	tool.Source = &runcontext.ToolSource{Kind: "custom", ID: "notes", Trust: "person", Version: "1", ToolListDigest: digest, CurrentDigest: func(context.Context) (string, error) { return digest, nil }}
	return tool
}

func delegatedReauthSetup(t *testing.T, engine string, port *runtimePrincipalPort) *hostSetup {
	t.Helper()
	s := &hostSetup{ctx: context.Background(), clock: &waitClock{}, refuse: &atomic.Bool{}, executed: &atomic.Int32{}}
	s.refuse.Store(true)
	s.clock.at.Store(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli())
	base := waitStore{runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{Now: s.clock.now})}
	s.store = base
	s.host = runcontext.NewReferenceEffectHostWith(s.store, runcontext.ReferenceEffectHostOptions{Credentials: func(context.Context, runcontext.EffectOperation) (*runcontext.CredentialRefusal, error) {
		if s.refuse.Load() {
			return hostRefusal(), nil
		}
		return nil, nil
	}})
	hostDelegation = runtimePrincipal(t, 0, runtimeNow, port, func(runcontext.PrincipalAdmissionRead) string { return runtimeAdmit })
	t.Cleanup(func() { hostDelegation = nil })
	a, err := s.store.AcquireLease(s.ctx, runcontext.LeaseAcquire{Resource: "run-r", Owner: "worker-a", DurationMs: hostHour, Principal: bindingOf(t, hostDelegation)})
	if err != nil {
		t.Fatal(err)
	}
	s.fence = runcontext.FenceOf(a)
	if s.view, err = runcontext.UnattendedRunStore(s.store, s.fence, s.host); err != nil {
		t.Fatal(err)
	}
	s.tool = delegatedWriteTool(s.executed)
	s.first = runHostTool(t, s.view, "run-r", engine, false, nil, s.tool, []string{hostArgs})
	return s
}

// bindingOf is the principals a delegated run's lease, fence and wait hold.
func bindingOf(t *testing.T, given *runcontext.RunPrincipal) *runcontext.PrincipalBinding {
	t.Helper()
	opened, err := runcontext.OpenPrincipal(given)
	if err != nil {
		t.Fatal(err)
	}
	return opened.Record.Binding()
}

func countedReads(port *runtimePrincipalPort) int {
	counted := 0
	for _, count := range port.counts() {
		if count {
			counted++
		}
	}
	return counted
}

func TestReauthorizationUnderADelegation(t *testing.T) {
	for _, engine := range hostEngines {
		t.Run(engine+"/a credential refusal is not a delegation refusal, and counts nothing", func(t *testing.T) {
			port := &runtimePrincipalPort{}
			s := delegatedReauthSetup(t, engine, port)
			if s.executed.Load() != 0 || runcontext.ReauthorizationOf(s.view) == nil {
				t.Fatalf("executed %d", s.executed.Load())
			}
			if got := runtimeRefusals(s.first.events); len(got) != 0 {
				t.Fatalf("delegation refusals %v", got)
			}
			if len(port.counts()) == 0 || countedReads(port) != 0 {
				t.Fatalf("counts %v", port.counts())
			}
			called := false
			for _, event := range s.first.events {
				if event.ToolCall != nil {
					called = true
					if event.ToolCall.Principal == nil {
						t.Fatalf("call %+v", event.ToolCall)
					}
				}
			}
			if !called {
				t.Fatal("no tool call")
			}
		})
		t.Run(engine+"/the re-send passes the admission, and is counted once at its dispatch", func(t *testing.T) {
			port := &runtimePrincipalPort{}
			s := delegatedReauthSetup(t, engine, port)
			resumed := s.claimed(t)
			before := len(port.counts())
			run := runHostTool(t, s.viewOf(t, resumed, s.host), "run-r", engine, true, nil, s.tool, []string{hostArgs})
			if s.executed.Load() != 1 || len(runtimeRefusals(run.events)) != 0 {
				t.Fatalf("executed %d, refusals %v", s.executed.Load(), runtimeRefusals(run.events))
			}
			counts := port.counts()
			if countedReads(port) != 1 || len(counts) <= before || !counts[len(counts)-1] {
				t.Fatalf("counts %v", counts)
			}
		})
		for label, row := range map[string]struct {
			now    string
			answer func(runcontext.PrincipalAdmissionRead) string
			name   string
		}{
			"revoked meanwhile": {runtimeNow, func(runcontext.PrincipalAdmissionRead) string { return runtimeRevoked }, "revoked"},
			"expired meanwhile": {runtimeExpired, func(runcontext.PrincipalAdmissionRead) string { return runtimeAdmit }, "expired"},
			"revoked as it dispatches": {runtimeNow, func(read runcontext.PrincipalAdmissionRead) string {
				if read.Call.Count {
					return runtimeRevoked
				}
				return runtimeAdmit
			}, "revoked"},
		} {
			t.Run(engine+"/the re-send is refused by name when the delegation is "+label, func(t *testing.T) {
				port := &runtimePrincipalPort{}
				s := delegatedReauthSetup(t, engine, port)
				resumed := s.claimed(t)
				hostDelegation = runtimePrincipal(t, 0, row.now, port, row.answer)
				run := runHostTool(t, s.viewOf(t, resumed, s.host), "run-r", engine, true, nil, s.tool, []string{hostArgs})
				if s.executed.Load() != 0 {
					t.Fatalf("executed %d", s.executed.Load())
				}
				if got := runtimeRefusals(run.events); len(got) != 1 || got[0] != row.name {
					raw, _ := json.Marshal(run.events)
					t.Fatalf("refusals %v in %.1200s", got, raw)
				}
			})
		}
		t.Run(engine+"/the re-send refused again for its credentials counts nothing", func(t *testing.T) {
			port := &runtimePrincipalPort{}
			s := delegatedReauthSetup(t, engine, port)
			resumed := s.claimed(t)
			s.refuse.Store(true)
			runHostTool(t, s.viewOf(t, resumed, s.host), "run-r", engine, true, nil, s.tool, []string{hostArgs})
			if s.executed.Load() != 0 || countedReads(port) != 0 {
				t.Fatalf("executed %d, counts %v", s.executed.Load(), port.counts())
			}
		})
	}
}

func TestRuntimeActionReauthorizationUnderADelegation(t *testing.T) {
	const key = "wk.adapter.delegated.Q7z2Xm9LpV4cR8tNb3Yh6Df1G"
	principal := "sha256:" + strings.Repeat("7", 64)
	admit := func(runcontext.PrincipalAdmissionRead) string { return runtimeAdmit }
	for _, engine := range []string{"eino", "native"} {
		t.Run(engine, func(t *testing.T) {
			ctx := context.Background()
			clock := &waitClock{}
			clock.at.Store(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli())
			store := waitStore{runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{Now: clock.now})}
			var current atomic.Bool
			host := runcontext.NewReferenceEffectHostWith(store, runcontext.ReferenceEffectHostOptions{Credentials: func(context.Context, runcontext.EffectOperation) (*runcontext.CredentialRefusal, error) {
				if current.Load() {
					return nil, nil
				}
				return &runcontext.CredentialRefusal{Cause: "revoked", Connection: "runtime-conn", Principal: principal, Permissions: []string{"evaluate:write"}, Requires: []string{"evaluate:write"}}, nil
			}})
			port := &runtimePrincipalPort{}
			f := newRuntimeActionFixture(t)
			f.approve = func(gate.RuntimeApprovalRequest) string { return "allow" }
			f.principal = runtimePrincipal(t, runtimeScoped, runtimeNow, port, admit)
			a, err := store.AcquireLease(ctx, runcontext.LeaseAcquire{Resource: "runtime-action-run", Owner: "worker-a", DurationMs: 3_600_000, Principal: bindingOf(t, f.principal)})
			if err != nil {
				t.Fatal(err)
			}
			view, err := runcontext.UnattendedRunStore(store, runcontext.FenceOf(a), host)
			if err != nil {
				t.Fatal(err)
			}
			var requests atomic.Int32
			events, _ := reauthRun(t, f, view, engine, false, &requests)
			if len(f.calls) != 0 || countedReads(port) != 0 || len(runtimeRefusals(events)) != 0 {
				t.Fatalf("calls %d, counts %v, refusals %v", len(f.calls), port.counts(), runtimeRefusals(events))
			}
			proof := runcontext.ReauthorizationOf(view)
			if proof == nil {
				t.Fatal("no proof")
			}
			digest, _ := runcontext.WakeKeyDigest(key)
			wait, _, err := runcontext.ParkRun(ctx, runcontext.ParkInput{Store: store, Fence: runcontext.FenceOf(a), Wait: "wait-r", Continuation: "continue-r", Condition: runcontext.WaitCondition{Event: &contract.RunWaitEventCondition{Kind: "reauthorized", Subject: "runtime-conn"}}, Model: runcontext.PinnedModel{Provider: "openai", Model: "fixture"}, WakeKeyDigest: digest, Release: []runcontext.WaitRelease{}, Reauthorization: proof})
			if err != nil {
				t.Fatal(err)
			}
			clock.at.Add(60_000)
			if _, err := runcontext.WakeRun(ctx, store, runcontext.WakeEvidence{Version: 1, Resource: "runtime-action-run", Wait: "wait-r", Evidence: "refreshed-1", Kind: "event", Event: &contract.RunWaitEventCondition{Kind: "reauthorized", Subject: "runtime-conn"}, Key: key}); err != nil {
				t.Fatal(err)
			}
			evidence := runcontext.ResumeEvidence{Version: 1, Resource: "runtime-action-run", Wait: "wait-r", Model: contract.RunResumeCurrentModel{Provider: "openai", Model: "fixture", Status: "available"}, Policy: contract.RunResumeCurrentPolicy{Digest: wait.Pins.Policy}, Plan: wait.Pins.Plan, Sources: wait.Pins.Sources, Artifacts: wait.Pins.Artifacts, Principal: wait.Pins.Principal}
			action := runcontext.ReauthAction{Version: 1, Resource: "runtime-action-run", Wait: "wait-r", Connection: "runtime-conn", Action: "refresh", Principal: principal, Permissions: []string{"evaluate:write"}, Actor: "person-owner", At: "2026-01-01T00:00:30.000Z"}
			resumed, err := runcontext.ResumeParked(ctx, runcontext.ResumeInput{Store: store, Resource: "runtime-action-run", Wait: "wait-r", Evidence: "refreshed-1", Owner: "worker-b", DurationMs: 3_600_000, Current: &evidence, Reauthorization: &action})
			if err != nil {
				t.Fatal(err)
			}
			current.Store(true)
			continued, err := runcontext.ReauthorizedRunStore(ctx, store, host, resumed)
			if err != nil {
				t.Fatal(err)
			}
			fresh := newRuntimeActionFixture(t)
			fresh.approve = func(gate.RuntimeApprovalRequest) string { return "allow" }
			fresh.principal = runtimePrincipal(t, runtimeScoped, runtimeNow, port, admit)
			if _, err := reauthRun(t, fresh, continued, engine, true, &requests); err != nil {
				t.Fatal(err)
			}
			if len(fresh.calls) != 1 || countedReads(port) != 1 {
				t.Fatalf("calls %d, counts %v", len(fresh.calls), port.counts())
			}
		})
	}
}

func TestReauthorizationWaitResumeUnderADelegation(t *testing.T) {
	for _, engine := range hostEngines {
		setup := func(t *testing.T) (*hostSetup, runcontext.RunWait, runcontext.ResumeInput, *runtimePrincipalPort) {
			t.Helper()
			port := &runtimePrincipalPort{}
			s := delegatedReauthSetup(t, engine, port)
			held, err := s.park(runcontext.ReauthorizationOf(s.view), "wait-r", hostKey, s.fence)
			if err != nil {
				t.Fatal(refusalReason(err))
			}
			return s, held, s.wakeInput(t, "wait-r", hostKey, held, s.store), port
		}
		t.Run(engine+"/the wait pins the principals, and its proof keeps the version 2 fence it was parked under", func(t *testing.T) {
			s, held, _, _ := setup(t)
			want := bindingOf(t, hostDelegation)
			if !runcontext.SamePrincipalBinding(held.Pins.Principal, want) || held.Reauthorization.Fence.Version != 2 || !runcontext.SamePrincipalBinding(held.Reauthorization.Fence.Principal, want) {
				t.Fatalf("pins %+v, fence %+v", held.Pins.Principal, held.Reauthorization.Fence)
			}
			_ = s
		})
		t.Run(engine+"/evidence of other principals is principal-changed, of none is evidence-missing, and neither claims anything", func(t *testing.T) {
			s, _, input, _ := setup(t)
			good := *input.Current
			other := *good.Principal
			other.RootActor = strings.Repeat("d1", 16)
			changed := good
			changed.Principal = &other
			input.Current = &changed
			if _, err := runcontext.ResumeParked(s.ctx, input); refusalReason(err) != "resume:principal-changed" {
				t.Fatalf("other: %v", err)
			}
			none := good
			none.Principal = nil
			input.Current = &none
			if _, err := runcontext.ResumeParked(s.ctx, input); refusalReason(err) != "resume:evidence-missing" {
				t.Fatalf("none: %v", err)
			}
			lease, err := s.store.ReadLease(s.ctx, "run-r")
			if err != nil || lease.State != "released" || lease.Epoch != "1" {
				t.Fatalf("a refused resume took a lease: %+v %v", lease, err)
			}
		})
		t.Run(engine+"/a changed credential identity stays identity-changed, and the delegation's refusal comes first when both changed", func(t *testing.T) {
			s, _, input, _ := setup(t)
			replacement := *input.Reauthorization
			replacement.Action = "replacement"
			input.Reauthorization = &replacement
			if _, err := runcontext.ResumeParked(s.ctx, input); refusalReason(err) != "resume:identity-changed" {
				t.Fatalf("replacement: %v", err)
			}
			other := *input.Current.Principal
			other.RootActor = strings.Repeat("d1", 16)
			changed := *input.Current
			changed.Principal = &other
			input.Current = &changed
			if _, err := runcontext.ResumeParked(s.ctx, input); refusalReason(err) != "resume:principal-changed" {
				t.Fatalf("both: %v", err)
			}
		})
		t.Run(engine+"/the claim's lease and fence keep the principals, and the re-send's view accepts them", func(t *testing.T) {
			s, _, input, _ := setup(t)
			resumed, err := runcontext.ResumeParked(s.ctx, input)
			if err != nil {
				t.Fatal(refusalReason(err))
			}
			want := bindingOf(t, hostDelegation)
			if resumed.Lease.Version != 2 || resumed.Lease.Epoch != "2" || !runcontext.SamePrincipalBinding(resumed.Fence.Principal, want) {
				t.Fatalf("resumed %+v", resumed.Lease)
			}
			s.refuse.Store(false)
			runHostTool(t, s.viewOf(t, resumed, s.host), "run-r", engine, true, nil, s.tool, []string{hostArgs})
			if s.executed.Load() != 1 {
				t.Fatalf("executed %d", s.executed.Load())
			}
		})
		for label, chain := range map[string]int{"another subject": 4, "no delegation at all": -1} {
			t.Run(engine+"/a continuation under "+label+" sends nothing, though the claim was held", func(t *testing.T) {
				s, _, input, port := setup(t)
				resumed, err := runcontext.ResumeParked(s.ctx, input)
				if err != nil {
					t.Fatal(refusalReason(err))
				}
				s.refuse.Store(false)
				hostDelegation = nil
				if chain >= 0 {
					hostDelegation = runtimePrincipal(t, chain, runtimeNow, port, func(runcontext.PrincipalAdmissionRead) string { return runtimeAdmit })
				}
				run := runHostTool(t, s.viewOf(t, resumed, s.host), "run-r", engine, true, nil, s.tool, []string{hostArgs})
				if s.executed.Load() != 0 {
					t.Fatalf("executed %d", s.executed.Load())
				}
				named := false
				for _, event := range run.events {
					named = named || event.Error != nil && strings.Contains(event.Error.Message, "principal-changed")
				}
				if !named && (run.err == nil || !strings.Contains(run.err.Error(), "principal-changed")) {
					raw, _ := json.Marshal(run.events)
					t.Fatalf("not named: %v %.800s", run.err, raw)
				}
			})
		}
	}
}
