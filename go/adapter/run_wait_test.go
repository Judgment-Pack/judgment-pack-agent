package adapter_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

// waitStore stands for a host store kind whose leases and waits outlive its process (#224).
type waitStore struct{ *runcontext.MemoryRunStore }

func (waitStore) LeaseLifetime() string { return runcontext.LeaseLifetimeDurable }
func (waitStore) WaitLifetime() string  { return runcontext.WaitLifetimeDurable }

type waitClock struct{ at atomic.Int64 }

func (c *waitClock) now() time.Time { return time.UnixMilli(c.at.Load()).UTC() }

// The Go half of "a real store restart with park/wake and no held provider": an act call paused by the
// host's policy on the real eino and agent adapters, parked, woken and resumed under a new lease by a
// fresh adapter run that shares nothing but the store and the effect host.
func TestDurableApprovalWaitAcrossAStoreRestart(t *testing.T) {
	const key = "host-wake-key-for-tests-0001"
	for _, engine := range []string{"eino", "native"} {
		t.Run(engine, func(t *testing.T) {
			ctx := context.Background()
			clock := &waitClock{}
			clock.at.Store(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli())
			store := waitStore{runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{Now: clock.now})}
			host := runcontext.NewReferenceEffectHost(store)
			a, err := store.AcquireLease(ctx, runcontext.LeaseAcquire{Resource: "runtime-action-run", Owner: "worker-a", DurationMs: 3_600_000})
			if err != nil {
				t.Fatal(err)
			}
			view, err := runcontext.FencedRunStore(store, runcontext.FenceOf(a), host)
			if err != nil {
				t.Fatal(err)
			}
			f := newRuntimeActionFixture(t)
			events, err := f.run(t, view, engine, false, nil, true)
			if err != nil {
				t.Fatal(err)
			}
			asked := runtimeActionAsked(t, events)
			// Nothing is held open for the person: the adapter run returned (the agent adapter acknowledged its
			// provider release, the model adapter made its one request) and nothing was dispatched.
			if len(f.calls) != 0 {
				t.Fatal("the paused call was dispatched")
			}
			keyDigest, _ := runcontext.WakeKeyDigest(key)
			park := runcontext.ParkInput{Store: store, Fence: runcontext.FenceOf(a), Wait: "wait-1", Continuation: "continue-1", Condition: runcontext.WaitCondition{Event: &contract.RunWaitEventCondition{Kind: "approval-decision", Subject: asked.CallID}}, Model: runcontext.PinnedModel{Provider: "openai", Model: "fixture"}, WakeKeyDigest: keyDigest, Release: []runcontext.WaitRelease{{Resource: "provider-account", Outcome: "released"}}}
			// #225: an unattended approval never waits without a bounded TTL.
			var refused *runcontext.WaitRefused
			if _, _, err := runcontext.ParkRun(ctx, park); !errors.As(err, &refused) || refused.Reason != "approval-ttl-required" {
				t.Fatalf("an approval without a TTL: %v", err)
			}
			ttl := int64(3_600_000)
			park.Limits = &runcontext.WaitLimits{ApprovalTtlMs: &ttl}
			wait, attention, err := runcontext.ParkRun(ctx, park)
			if err != nil {
				t.Fatal(err)
			}
			if wait.Reason != "approval" || wait.Pending.Approval.CallID != asked.CallID || attention.Needs != "approval-decision" || attention.ReleaseUncertain {
				t.Fatalf("wait %+v attention %+v", wait, attention)
			}
			if lease, _ := store.ReadLease(ctx, "runtime-action-run"); lease.State != "released" {
				t.Fatalf("lease %+v", lease)
			}
			// #265 M2: the parked run is closed; no fresh lease continues it.
			if _, err := store.AcquireLease(ctx, runcontext.LeaseAcquire{Resource: "runtime-action-run", Owner: "worker-x", ExpectedRevision: wait.Checkpoint.Revision, DurationMs: 3_600_000}); !errors.As(err, &refused) || refused.Reason != "wait-pending" {
				t.Fatalf("a fresh lease on a parked run: %v", err)
			}
			// A new process: wake with the host's evidence, then resume with the person's decision.
			clock.at.Add(60_000)
			wake := runcontext.WakeEvidence{Version: 1, Resource: "runtime-action-run", Wait: "wait-1", Evidence: "decision-1", Kind: "event", Event: &contract.RunWaitEventCondition{Kind: "approval-decision", Subject: asked.CallID}, Key: key}
			first, err := runcontext.WakeRun(ctx, store, wake)
			if err != nil {
				t.Fatal(err)
			}
			if again, err := runcontext.WakeRun(ctx, store, wake); err != nil || again != first {
				t.Fatalf("duplicate wake: %v", err)
			}
			// #227: the host's current evidence: nothing pinned changed.
			current := runcontext.ResumeEvidence{Version: 1, Resource: "runtime-action-run", Wait: "wait-1", Model: contract.RunResumeCurrentModel{Provider: "openai", Model: "fixture", Status: "available"}, Policy: contract.RunResumeCurrentPolicy{Digest: wait.Pins.Policy}, Plan: wait.Pins.Plan, Sources: wait.Pins.Sources, Artifacts: wait.Pins.Artifacts}
			input := runcontext.ResumeInput{Store: store, Resource: "runtime-action-run", Wait: "wait-1", Evidence: "decision-1", Owner: "worker-b", DurationMs: 3_600_000, Current: &current}
			if _, err := runcontext.ResumeParked(ctx, input); !errors.As(err, &refused) || refused.Reason != "decision-required" {
				t.Fatalf("a wake is not the approval: %v", err)
			}
			input.Approval = &contract.CheckpointApprovalDecision{CallID: asked.CallID, Approved: true}
			resumed, err := runcontext.ResumeParked(ctx, input)
			if err != nil {
				t.Fatal(err)
			}
			// The approval's expiry bounds the claim's lease.
			if resumed.Lease.Epoch != "2" || resumed.Lease.Expiry != wait.Deadline.At {
				t.Fatalf("lease %+v", resumed.Lease)
			}
			continued, err := runcontext.FencedRunStore(store, resumed.Fence, host)
			if err != nil {
				t.Fatal(err)
			}
			fresh := newRuntimeActionFixture(t)
			if _, err := fresh.run(t, continued, engine, true, resumed.Approval, true); err != nil {
				t.Fatal(err)
			}
			if len(fresh.calls) != 1 {
				t.Fatalf("the approved call ran %d times", len(fresh.calls))
			}
			if _, err := runcontext.ResumeParked(ctx, input); !errors.As(err, &refused) || refused.Reason != "already-resumed" {
				t.Fatalf("second resume: %v", err)
			}
			report, err := runcontext.WaitReportOf(ctx, store, "runtime-action-run", time.Time{})
			if err != nil || len(report.Entries) != 3 || report.Entries[2].Decision != "resume" {
				t.Fatalf("report %+v %v", report, err)
			}
		})
	}
}
