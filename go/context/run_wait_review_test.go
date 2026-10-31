package runcontext

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

// #265: the pre-release batch for durable waits, deadlines and revalidation (the MEDIUM findings of the
// #264 and #266 security reviews). The TypeScript twin is packages/harness/storage/waitReview.test.ts.

// reviewKey is a fresh, high-entropy key per wait (#265 M1).
func reviewKey(name string) string { return "wk." + name + ".Q7z2Xm9LpV4cR8tNb3Yh6Df1Gj5K" }

// reviewPaused commits checkpoint as run's head under a fresh lease.
func reviewPaused(t *testing.T, store durableWaitStore, run string, checkpoint []byte) LeaseFence {
	t.Helper()
	lease := leaseAcquire(t, store.MemoryRunStore, run, "worker-a")
	if _, err := store.CommitFenced(context.Background(), RunCommit{RunID: run, CommitID: "pause", Checkpoint: checkpoint}, FenceOf(lease)); err != nil {
		t.Fatal(err)
	}
	return FenceOf(lease)
}
func reviewStore() (*leaseClock, durableWaitStore) {
	clock := newLeaseClock()
	return clock, durableWaitStore{NewMemoryRunStore(MemoryRunStoreOptions{Now: clock.now})}
}
func reviewBudget(t *testing.T, used, limit int, grants ...map[string]any) []byte {
	return pausedCheckpoint(t, func(c map[string]any) {
		delete(c, "approval")
		c["used"], c["limit"] = used, limit
		if len(grants) > 0 {
			c["grants"] = grants
		}
	})
}
func reviewApprovalPark(store durableWaitStore, run string, fence LeaseFence, key string, ttl int64, runLimit *string) (RunWait, AttentionEvent, error) {
	digest, _ := WakeKeyDigest(key)
	return ParkRun(context.Background(), ParkInput{Store: store, Fence: fence, Wait: "wait-1", Continuation: "k-1", Condition: WaitCondition{Event: &contract.RunWaitEventCondition{Kind: "approval-decision", Subject: "call_2_1"}}, Model: waitModel, Limits: &WaitLimits{ApprovalTtlMs: &ttl, Run: runLimit}, WakeKeyDigest: digest, Release: []WaitRelease{}})
}
func reviewDecision(run, key string) WakeEvidence {
	return WakeEvidence{Version: 1, Resource: run, Wait: "wait-1", Evidence: "decision-1", Kind: "event", Event: &contract.RunWaitEventCondition{Kind: "approval-decision", Subject: "call_2_1"}, Key: key}
}
func reviewApprove(t *testing.T, store RunStore, run string) ResumeInput {
	return withCurrent(t, ResumeInput{Store: store, Resource: run, Wait: "wait-1", Evidence: "decision-1", Owner: "worker-b", DurationMs: 24 * waitHour, Approval: &contract.CheckpointApprovalDecision{CallID: "call_2_1", Approved: true}})
}

func TestWaitReviewKeysAreStrongAndBelongToOneWait(t *testing.T) {
	ctx := context.Background()
	for _, key := range []string{strings.Repeat("a", 36), strings.Repeat("ab", 16), "wk.short.Q7z2Xm9LpV4cR8tNb3Yh6D"[:31]} {
		if _, err := WakeKeyDigest(key); waitReasonOf(err) != "invalid" {
			t.Errorf("key %q accepted", key)
		}
		if _, err := ReadWakeEvidence([]byte(`{"version":1,"resource":"r","wait":"w","evidence":"e","kind":"time","key":"` + key + `"}`)); waitReasonOf(err) != "invalid" {
			t.Errorf("evidence with key %q accepted", key)
		}
	}
	clock, store := reviewStore()
	if _, _, err := reviewApprovalPark(store, "run-a", reviewPaused(t, store, "run-a", pausedCheckpoint(t, nil)), reviewKey("a"), waitHour, nil); err != nil {
		t.Fatal(err)
	}
	b := reviewPaused(t, store, "run-b", pausedCheckpoint(t, nil))
	if _, _, err := reviewApprovalPark(store, "run-b", b, reviewKey("a"), waitHour, nil); waitReasonOf(err) != "key-reused" {
		t.Fatalf("another run's key: %v", err)
	}
	if lease, _ := store.ReadLease(ctx, "run-b"); lease.State != "held" || lease.Epoch != "1" {
		t.Fatalf("a refused park released the lease: %+v", lease)
	}
	if held, _ := store.ReadWait(ctx, "run-b", "wait-1"); held != nil {
		t.Fatal("a refused park left a wait")
	}
	if _, _, err := reviewApprovalPark(store, "run-b", b, reviewKey("b"), waitHour, nil); err != nil {
		t.Fatal(err)
	}
	// Run A's evidence re-addressed to run B is no longer B's: B's key is its own.
	if _, err := WakeRun(ctx, store, reviewDecision("run-b", reviewKey("a"))); waitReasonOf(err) != "unauthenticated" {
		t.Fatalf("re-addressed evidence: %v", err)
	}
	// The same run: a later wait of a run never takes an earlier wait's key.
	c := reviewPaused(t, store, "run-c", reviewBudget(t, 20, 20))
	wakeAt := waitTime(clock, leaseMinute)
	if _, _, err := ParkRun(ctx, ParkInput{Store: store, Fence: c, Wait: "wait-1", Continuation: "k-1", Condition: WaitCondition{WakeAt: &wakeAt}, Model: waitModel, WakeKeyDigest: waitKeyDigest(t, reviewKey("c")), Release: []WaitRelease{}}); err != nil {
		t.Fatal(err)
	}
	clock.advance(leaseMinute)
	if _, err := WakeRun(ctx, store, WakeEvidence{Version: 1, Resource: "run-c", Wait: "wait-1", Evidence: "tick-1", Kind: "time", Key: reviewKey("c")}); err != nil {
		t.Fatal(err)
	}
	resumed, err := ResumeParked(ctx, withCurrent(t, ResumeInput{Store: store, Resource: "run-c", Wait: "wait-1", Evidence: "tick-1", Owner: "worker-b", DurationMs: waitHour, Grant: &contract.CheckpointGrant{From: 20, To: 30, At: waitTime(clock, 0)}}))
	if err != nil {
		t.Fatal(err)
	}
	head, _ := store.Load(ctx, "run-c")
	if _, err := store.CommitFenced(ctx, RunCommit{RunID: "run-c", ExpectedRevision: head.Revision, CommitID: "pause-2", Checkpoint: reviewBudget(t, 30, 30, map[string]any{"from": 20, "to": 30, "at": waitTime(clock, 0)})}, resumed.Fence); err != nil {
		t.Fatal(err)
	}
	later := waitTime(clock, waitHour)
	again := ParkInput{Store: store, Fence: resumed.Fence, Wait: "wait-2", Continuation: "k-2", Condition: WaitCondition{WakeAt: &later}, Model: waitModel, WakeKeyDigest: waitKeyDigest(t, reviewKey("c")), Release: []WaitRelease{}}
	if _, _, err := ParkRun(ctx, again); waitReasonOf(err) != "key-reused" {
		t.Fatalf("an earlier wait's key: %v", err)
	}
	if lease, _ := store.ReadLease(ctx, "run-c"); lease.State != "held" || lease.Epoch != "2" {
		t.Fatalf("a refused park released the lease: %+v", lease)
	}
	again.WakeKeyDigest = waitKeyDigest(t, reviewKey("c2"))
	if wait, _, err := ParkRun(ctx, again); err != nil || wait.State != "parked" {
		t.Fatalf("its own key: %v", err)
	}
}

func TestWaitReviewNoPauseParksThatNothingCanResume(t *testing.T) {
	ctx := context.Background()
	item := func(name string, index int) map[string]any {
		raw, err := os.ReadFile("../../contract/vectors/plan/" + name)
		if err != nil {
			t.Fatal(err)
		}
		var file struct{ Items []map[string]any }
		if err := json.Unmarshal(raw, &file); err != nil {
			t.Fatal(err)
		}
		return file.Items[index]
	}
	budgetOnly := item("native-checkpoints.json", 1)
	planState := budgetOnly["planState"].(map[string]any)
	delete(planState, "pending")
	delete(planState, "driftPolicy")
	for name, checkpoint := range map[string]map[string]any{"plan-drift": item("extended-checkpoints.json", 5), "provider-managed plan-drift": item("native-checkpoints.json", 1), "provider-managed budget": budgetOnly} {
		clock, store := reviewStore()
		raw, err := json.Marshal(checkpoint)
		if err != nil {
			t.Fatal(err)
		}
		fence := reviewPaused(t, store, "run-p", raw)
		wakeAt := waitTime(clock, waitHour)
		if _, _, err := ParkRun(ctx, ParkInput{Store: store, Fence: fence, Wait: "wait-1", Continuation: "k", Condition: WaitCondition{WakeAt: &wakeAt}, Model: waitModel, WakeKeyDigest: waitKeyDigest(t, reviewKey("p")), Release: []WaitRelease{}}); waitReasonOf(err) != "not-resumable" {
			t.Errorf("%s: %v", name, err)
		}
		if held, _ := store.ReadWait(ctx, "run-p", "wait-1"); held != nil {
			t.Errorf("%s parked", name)
		}
		if lease, _ := store.ReadLease(ctx, "run-p"); lease.State != "held" {
			t.Errorf("%s: lease %+v", name, lease)
		}
	}
}

func TestWaitReviewTheGrantTimeIsReadByOneRule(t *testing.T) {
	ctx := context.Background()
	raw, err := os.ReadFile("../../contract/vectors/wait/decision.json")
	if err != nil {
		t.Fatal(err)
	}
	var vector struct {
		Items         []contract.CheckpointGrant
		ReaderRefused []struct {
			Problem string
			Grant   contract.CheckpointGrant
		} `json:"readerRefused"`
	}
	if err := json.Unmarshal(raw, &vector); err != nil || len(vector.Items) == 0 || len(vector.ReaderRefused) == 0 {
		t.Fatal(err)
	}
	parked := func() durableWaitStore {
		clock, store := reviewStore()
		wakeAt := waitTime(clock, leaseMinute)
		if _, _, err := ParkRun(ctx, ParkInput{Store: store, Fence: reviewPaused(t, store, "run-g", reviewBudget(t, 20, 20)), Wait: "wait-1", Continuation: "k", Condition: WaitCondition{WakeAt: &wakeAt}, Model: waitModel, WakeKeyDigest: waitKeyDigest(t, reviewKey("g")), Release: []WaitRelease{}}); err != nil {
			t.Fatal(err)
		}
		clock.advance(leaseMinute)
		if _, err := WakeRun(ctx, store, WakeEvidence{Version: 1, Resource: "run-g", Wait: "wait-1", Evidence: "tick-1", Kind: "time", Key: reviewKey("g")}); err != nil {
			t.Fatal(err)
		}
		return store
	}
	grant := func(store durableWaitStore, at string) (ResumedWait, error) {
		return ResumeParked(ctx, withCurrent(t, ResumeInput{Store: store, Resource: "run-g", Wait: "wait-1", Evidence: "tick-1", Owner: "worker-b", DurationMs: waitHour, Grant: &contract.CheckpointGrant{From: 20, To: 30, At: at}}))
	}
	refused := parked()
	for _, row := range vector.ReaderRefused {
		if _, err := grant(refused, row.Grant.At); waitReasonOf(err) != "decision-required" {
			t.Errorf("%s: %v", row.Problem, err)
		}
	}
	for _, item := range vector.Items {
		if resumed, err := grant(parked(), item.At); err != nil || resumed.Grant.At != item.At {
			t.Errorf("grant at %q: %v", item.At, err)
		}
	}
}

// reviewTwoWaits: wait-1 of run-m resumed with a grant, wait-2 parked over the run's next pause.
func reviewTwoWaits(t *testing.T) (*leaseClock, durableWaitStore) {
	t.Helper()
	ctx := context.Background()
	clock, store := reviewStore()
	wakeAt := waitTime(clock, leaseMinute)
	if _, _, err := ParkRun(ctx, ParkInput{Store: store, Fence: reviewPaused(t, store, "run-m", reviewBudget(t, 20, 20)), Wait: "wait-1", Continuation: "k-1", Condition: WaitCondition{WakeAt: &wakeAt}, Model: waitModel, WakeKeyDigest: waitKeyDigest(t, reviewKey("m1")), Release: []WaitRelease{}}); err != nil {
		t.Fatal(err)
	}
	clock.advance(leaseMinute)
	if _, err := WakeRun(ctx, store, WakeEvidence{Version: 1, Resource: "run-m", Wait: "wait-1", Evidence: "tick-1", Kind: "time", Key: reviewKey("m1")}); err != nil {
		t.Fatal(err)
	}
	resumed, err := ResumeParked(ctx, withCurrent(t, ResumeInput{Store: store, Resource: "run-m", Wait: "wait-1", Evidence: "tick-1", Owner: "worker-b", DurationMs: waitHour, Grant: &contract.CheckpointGrant{From: 20, To: 30, At: waitTime(clock, 0)}}))
	if err != nil {
		t.Fatal(err)
	}
	head, _ := store.Load(ctx, "run-m")
	if _, err := store.CommitFenced(ctx, RunCommit{RunID: "run-m", ExpectedRevision: head.Revision, CommitID: "pause-2", Checkpoint: reviewBudget(t, 30, 30, map[string]any{"from": 20, "to": 30, "at": waitTime(clock, 0)})}, resumed.Fence); err != nil {
		t.Fatal(err)
	}
	second := waitTime(clock, 2*leaseMinute)
	if _, _, err := ParkRun(ctx, ParkInput{Store: store, Fence: resumed.Fence, Wait: "wait-2", Continuation: "k-2", Condition: WaitCondition{WakeAt: &second}, Model: waitModel, WakeKeyDigest: waitKeyDigest(t, reviewKey("m2")), Release: []WaitRelease{}}); err != nil {
		t.Fatal(err)
	}
	return clock, store
}

func TestWaitReviewAWakeReachesTheWaitItNames(t *testing.T) {
	ctx := context.Background()
	clock, store := reviewTwoWaits(t)
	clock.advance(leaseMinute)
	parked, _ := store.ReadWait(ctx, "run-m", "wait-2")
	if _, err := WakeRun(ctx, store, WakeEvidence{Version: 1, Resource: "run-m", Wait: "wait-1", Evidence: "tick-2", Kind: "time", Key: reviewKey("m1")}); waitReasonOf(err) != "stale" {
		t.Fatalf("a wake naming the resumed wait: %v", err)
	}
	if after, _ := store.ReadWait(ctx, "run-m", "wait-2"); !sameWaitJSON(t, after, mustJSON(t, parked)) {
		t.Fatal("a wake naming wait-1 changed wait-2")
	}
	if d, err := WakeRun(ctx, store, WakeEvidence{Version: 1, Resource: "run-m", Wait: "wait-2", Evidence: "tick-2", Kind: "time", Key: reviewKey("m2")}); err != nil || d.Evidence != "tick-2" {
		t.Fatalf("its own wake: %v", err)
	}
	one, _ := store.ReadWait(ctx, "run-m", "wait-1")
	two, _ := store.ReadWait(ctx, "run-m", "wait-2")
	if one.State != "resumed" || two.State != "woken" {
		t.Fatalf("states %s %s", one.State, two.State)
	}
}

func TestWaitReviewAWakeAfterTheClaimNeverReopensTheWait(t *testing.T) {
	ctx := context.Background()
	_, store := reviewTwoWaits(t)
	resumed, _ := store.ReadWait(ctx, "run-m", "wait-1")
	if d, err := WakeRun(ctx, store, WakeEvidence{Version: 1, Resource: "run-m", Wait: "wait-1", Evidence: "tick-1", Kind: "time", Key: reviewKey("m1")}); err != nil || d != *resumed.Disposition {
		t.Fatalf("the duplicate: %+v %v", d, err)
	}
	if _, err := WakeRun(ctx, store, WakeEvidence{Version: 1, Resource: "run-m", Wait: "wait-1", Evidence: "tick-9", Kind: "time", Key: reviewKey("m1")}); waitReasonOf(err) != "stale" {
		t.Fatalf("a later wake: %v", err)
	}
	if after, _ := store.ReadWait(ctx, "run-m", "wait-1"); !sameWaitJSON(t, after, mustJSON(t, resumed)) {
		t.Fatal("a wake after the claim changed the wait")
	}
	if _, err := ResumeParked(ctx, withCurrent(t, ResumeInput{Store: store, Resource: "run-m", Wait: "wait-1", Evidence: "tick-1", Owner: "worker-c", DurationMs: waitHour, Grant: &contract.CheckpointGrant{From: 20, To: 30, At: "2026-01-01T00:00:00.000Z"}})); waitReasonOf(err) != "already-resumed" {
		t.Fatalf("a second claim: %v", err)
	}
}

func TestWaitReviewARefusedParkLeavesTheLeaseHeld(t *testing.T) {
	ctx := context.Background()
	clock, store := reviewStore()
	if _, _, err := reviewApprovalPark(store, "run-a", reviewPaused(t, store, "run-a", pausedCheckpoint(t, nil)), reviewKey("a"), waitHour, nil); err != nil {
		t.Fatal(err)
	}
	b := reviewPaused(t, store, "run-b", pausedCheckpoint(t, nil))
	if _, _, err := reviewApprovalPark(store, "run-b", b, reviewKey("a"), waitHour, nil); waitReasonOf(err) != "key-reused" {
		t.Fatalf("key-reused: %v", err)
	}
	past := waitTime(clock, 0)
	if _, _, err := reviewApprovalPark(store, "run-b", b, reviewKey("b"), waitHour, &past); waitReasonOf(err) != "deadline-passed" {
		t.Fatalf("deadline-passed: %v", err)
	}
	if lease, _ := store.ReadLease(ctx, "run-b"); lease.State != "held" || lease.Epoch != "1" {
		t.Fatalf("lease %+v", lease)
	}
	head, _ := store.Load(ctx, "run-b")
	if _, err := store.CommitFenced(ctx, RunCommit{RunID: "run-b", ExpectedRevision: head.Revision, CommitID: "after", Events: [][]byte{[]byte(`{"version":1}`)}}, b); err != nil {
		t.Fatalf("a commit under the kept lease: %v", err)
	}
}

func TestWaitReviewTheSameParkUnderAnotherFenceIsAConflict(t *testing.T) {
	ctx := context.Background()
	_, store := reviewStore()
	fence := reviewPaused(t, store, "run-a", pausedCheckpoint(t, nil))
	parked, attention, err := reviewApprovalPark(store, "run-a", fence, reviewKey("a"), waitHour, nil)
	if err != nil {
		t.Fatal(err)
	}
	park := parked
	park.Parked, park.Attention, park.State, park.Deadline = contract.RunWaitActor{}, "", "", nil
	if again, event, err := store.ParkWait(ctx, park, fence); err != nil || !sameWaitJSON(t, again, mustJSON(t, parked)) || event != attention {
		t.Fatalf("the same park: %v", err)
	}
	for _, edit := range []func(*LeaseFence){func(f *LeaseFence) { f.Owner = "worker-x" }, func(f *LeaseFence) { f.Epoch = "9" }} {
		other := fence
		edit(&other)
		if _, _, err := store.ParkWait(ctx, park, other); waitReasonOf(err) != "conflict" {
			t.Fatalf("another fence %+v: %v", other, err)
		}
	}
}

// forgedWaitStore returns what its edits make of the store's own answers.
type forgedWaitStore struct {
	durableWaitStore
	read  func(RunWait) RunWait
	claim func(RunWait, RunLease) (RunWait, RunLease)
}

func (f forgedWaitStore) ReadWait(ctx context.Context, resource, wait string) (*RunWait, error) {
	held, err := f.durableWaitStore.ReadWait(ctx, resource, wait)
	if held == nil || err != nil || f.read == nil {
		return held, err
	}
	forged := f.read(*held)
	return &forged, nil
}
func (f forgedWaitStore) ResumeWait(ctx context.Context, claim WaitClaim) (RunWait, RunLease, error) {
	wait, lease, err := f.durableWaitStore.ResumeWait(ctx, claim)
	if err != nil || f.claim == nil {
		return wait, lease, err
	}
	wait, lease = f.claim(wait, lease)
	return wait, lease, nil
}

func TestWaitReviewResumeParkedReadsWhatTheStoreReturns(t *testing.T) {
	ctx := context.Background()
	woken := func(store durableWaitStore, clock *leaseClock) {
		if _, _, err := reviewApprovalPark(store, "run-a", reviewPaused(t, store, "run-a", pausedCheckpoint(t, nil)), reviewKey("a"), waitHour, nil); err != nil {
			t.Fatal(err)
		}
		clock.advance(leaseMinute)
		if _, err := WakeRun(ctx, store, reviewDecision("run-a", reviewKey("a"))); err != nil {
			t.Fatal(err)
		}
	}
	// #265 M6: a malformed wait is refused invalid, never dereferenced.
	// In this order: the identity first, so the dereferencing forgeries come after an assertion.
	for _, c := range []struct {
		name string
		read func(RunWait) RunWait
	}{
		{"another wait", func(w RunWait) RunWait { w.Wait = "wait-9"; return w }},
		{"budget pending on an approval wait", func(w RunWait) RunWait {
			w.Pending = contract.RunWaitPending{Budget: &contract.RunWaitPendingBudget{Kind: "budget", Unit: "model-requests", Used: 1, Limit: 2}}
			return w
		}},
		{"no pending decision", func(w RunWait) RunWait { w.Pending = contract.RunWaitPending{}; return w }},
	} {
		name, read := c.name, c.read
		clock, store := reviewStore()
		woken(store, clock)
		input := reviewApprove(t, store, "run-a")
		input.Store = forgedWaitStore{durableWaitStore: store, read: read}
		if _, err := ResumeParked(ctx, input); waitReasonOf(err) != "invalid" {
			t.Errorf("%s: %v", name, err)
		}
		if lease, _ := store.ReadLease(ctx, "run-a"); lease.State != "released" || lease.Epoch != "1" {
			t.Errorf("%s: lease %+v", name, lease)
		}
	}
	// #266 M6: a claim result that is not this wait resumed under the returned lease, within the run's
	// deadline and the approval's expiry, is refused invalid.
	for name, forge := range map[string]func(RunWait, RunLease) (RunWait, RunLease){
		"a lease past the approval expiry": func(w RunWait, l RunLease) (RunWait, RunLease) {
			l.Expiry = "2026-01-02T00:00:00.000Z"
			return w, l
		},
		"a wait that is not resumed": func(w RunWait, l RunLease) (RunWait, RunLease) {
			w.State, w.Resumed, w.Revalidation = "woken", nil, nil
			return w, l
		},
		"another owner's lease":          func(w RunWait, l RunLease) (RunWait, RunLease) { l.Owner = "worker-x"; return w, l },
		"another epoch than the claim's": func(w RunWait, l RunLease) (RunWait, RunLease) { l.Epoch = "7"; return w, l },
		"another wait":                   func(w RunWait, l RunLease) (RunWait, RunLease) { w.Wait = "wait-9"; return w, l },
		"a released lease":               func(w RunWait, l RunLease) (RunWait, RunLease) { l.State = "released"; return w, l },
		"a malformed lease":              func(w RunWait, l RunLease) (RunWait, RunLease) { return w, RunLease{} },
		"a revalidation of other evidence": func(w RunWait, l RunLease) (RunWait, RunLease) {
			w.Revalidation.Evidence = "sha256:" + strings.Repeat("0", 64)
			return w, l
		},
	} {
		clock, store := reviewStore()
		woken(store, clock)
		input := reviewApprove(t, store, "run-a")
		input.Store = forgedWaitStore{durableWaitStore: store, claim: forge}
		if _, err := ResumeParked(ctx, input); waitReasonOf(err) != "invalid" {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestWaitReviewTheClaimIsRefusedAtTheWaitsOwnDeadline(t *testing.T) {
	ctx := context.Background()
	clock, store := reviewStore()
	wakeAt, waitMs := waitTime(clock, leaseMinute), int64(waitHour)
	if _, _, err := ParkRun(ctx, ParkInput{Store: store, Fence: reviewPaused(t, store, "run-w", reviewBudget(t, 20, 20)), Wait: "wait-1", Continuation: "k", Condition: WaitCondition{WakeAt: &wakeAt}, Model: waitModel, Limits: &WaitLimits{WaitMs: &waitMs}, WakeKeyDigest: waitKeyDigest(t, reviewKey("w")), Release: []WaitRelease{}}); err != nil {
		t.Fatal(err)
	}
	clock.advance(leaseMinute)
	if _, err := WakeRun(ctx, store, WakeEvidence{Version: 1, Resource: "run-w", Wait: "wait-1", Evidence: "tick-1", Kind: "time", Key: reviewKey("w")}); err != nil {
		t.Fatal(err)
	}
	clock.advance(waitHour - leaseMinute)
	if _, err := ResumeParked(ctx, withCurrent(t, ResumeInput{Store: store, Resource: "run-w", Wait: "wait-1", Evidence: "tick-1", Owner: "worker-b", DurationMs: waitHour, Grant: &contract.CheckpointGrant{From: 20, To: 30, At: waitTime(clock, 0)}})); waitReasonOf(err) != "deadline-passed" {
		t.Fatalf("a claim at the wait's deadline: %v", err)
	}
	if lease, _ := store.ReadLease(ctx, "run-w"); lease.State != "released" || lease.Epoch != "1" {
		t.Fatalf("lease %+v", lease)
	}
}

func TestWaitReviewALaterRunDeadlineReachesTheWait(t *testing.T) {
	ctx := context.Background()
	clock, store := reviewStore()
	if _, _, err := reviewApprovalPark(store, "run-a", reviewPaused(t, store, "run-a", pausedCheckpoint(t, nil)), reviewKey("a"), 4*waitHour, nil); err != nil {
		t.Fatal(err)
	}
	clock.advance(leaseMinute)
	if _, err := LimitRun(ctx, store, RunLimit{Version: 1, Resource: "run-a", Run: waitTime(clock, 10*leaseMinute)}); err != nil {
		t.Fatal(err)
	}
	held, _ := store.ReadWait(ctx, "run-a", "wait-1")
	run, approval := waitTime(clock, 10*leaseMinute), waitTime(clock, 4*waitHour)
	if !sameWaitJSON(t, held.Deadline, mustJSON(t, WaitDeadline{At: run, By: "run", Run: &run, Approval: &approval})) {
		t.Fatalf("deadline %+v", held.Deadline)
	}
	clock.advance(19 * leaseMinute)
	if _, err := WakeRun(ctx, store, reviewDecision("run-a", reviewKey("a"))); waitReasonOf(err) != "deadline-passed" {
		t.Fatalf("a wake past the run's deadline: %v", err)
	}
	if d, err := WakeRun(ctx, store, WakeEvidence{Version: 1, Resource: "run-a", Wait: "wait-1", Evidence: "deadline-1", Kind: "deadline", Key: reviewKey("a")}); err != nil || d.At != waitTime(clock, 20*leaseMinute) {
		t.Fatalf("the deadline wake: %+v %v", d, err)
	}
	report, err := WaitReportOf(ctx, store, "run-a", clock.now())
	if err != nil || len(report.Entries) != 2 || report.Entries[1].Decision != "deadline" {
		t.Fatalf("report %+v %v", report, err)
	}
	head, _ := store.Load(ctx, "run-a")
	if _, err := store.AcquireLease(ctx, LeaseAcquire{Resource: "run-a", Owner: "worker-x", ExpectedRevision: head.Revision, DurationMs: waitHour}); waitReasonOf(err) != "closed" {
		t.Fatalf("a closed run leased: %v", err)
	}

	// A woken wait is stamped again too, and its claim is refused at the run's deadline.
	clock2, store2 := reviewStore()
	if _, _, err := reviewApprovalPark(store2, "run-a", reviewPaused(t, store2, "run-a", pausedCheckpoint(t, nil)), reviewKey("a"), 4*waitHour, nil); err != nil {
		t.Fatal(err)
	}
	clock2.advance(5 * leaseMinute)
	if _, err := WakeRun(ctx, store2, reviewDecision("run-a", reviewKey("a"))); err != nil {
		t.Fatal(err)
	}
	if _, err := LimitRun(ctx, store2, RunLimit{Version: 1, Resource: "run-a", Run: waitTime(clock2, 6*leaseMinute)}); err != nil {
		t.Fatal(err)
	}
	clock2.advance(2 * leaseMinute)
	if _, err := ResumeParked(ctx, reviewApprove(t, store2, "run-a")); waitReasonOf(err) != "deadline-passed" {
		t.Fatalf("a claim past the run's deadline: %v", err)
	}
	if lease, _ := store2.ReadLease(ctx, "run-a"); lease.State != "released" || lease.Epoch != "1" {
		t.Fatalf("lease %+v", lease)
	}

	// A run limit before the park: the wait carries the earliest deadline it can, and the store's own
	// deadline refuses a claim and a wake at once.
	_, store3 := reviewStore()
	if _, _, err := reviewApprovalPark(store3, "run-a", reviewPaused(t, store3, "run-a", pausedCheckpoint(t, nil)), reviewKey("a"), 4*waitHour, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := WakeRun(ctx, store3, reviewDecision("run-a", reviewKey("a"))); err != nil {
		t.Fatal(err)
	}
	if _, err := LimitRun(ctx, store3, RunLimit{Version: 1, Resource: "run-a", Run: "1970-01-01T00:00:00.000Z"}); err != nil {
		t.Fatal(err)
	}
	held, _ = store3.ReadWait(ctx, "run-a", "wait-1")
	early := waitTime(clock, 1)
	if !sameWaitJSON(t, held.Deadline, mustJSON(t, WaitDeadline{At: early, By: "run", Run: &early, Approval: &approval})) {
		t.Fatalf("deadline %+v", held.Deadline)
	}
	if _, err := ResumeParked(ctx, reviewApprove(t, store3, "run-a")); waitReasonOf(err) != "deadline-passed" {
		t.Fatalf("a claim at the park's instant: %v", err)
	}
	_, store4 := reviewStore()
	if _, _, err := reviewApprovalPark(store4, "run-a", reviewPaused(t, store4, "run-a", pausedCheckpoint(t, nil)), reviewKey("a"), 4*waitHour, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := LimitRun(ctx, store4, RunLimit{Version: 1, Resource: "run-a", Run: "1970-01-01T00:00:00.000Z"}); err != nil {
		t.Fatal(err)
	}
	if _, err := WakeRun(ctx, store4, reviewDecision("run-a", reviewKey("a"))); waitReasonOf(err) != "deadline-passed" {
		t.Fatalf("a wake at the park's instant: %v", err)
	}
}

func TestWaitReviewNoUnfencedCommitContinuesAHeldRun(t *testing.T) {
	ctx := context.Background()
	clock, store, _, parked := parkedApprovalStore(t, 0)
	outside := func(id string) error {
		_, err := store.Commit(ctx, RunCommit{RunID: "run-approval", ExpectedRevision: parked.Checkpoint.Revision, CommitID: id, Events: [][]byte{[]byte(`{"version":1}`)}})
		return err
	}
	if err := outside("outside-1"); waitReasonOf(err) != "wait-pending" {
		t.Fatalf("parked: %v", err)
	}
	if _, err := store.CommitHistory(ctx, RunHistoryCommit{Run: RunCommit{RunID: "run-approval", ExpectedRevision: parked.Checkpoint.Revision, CommitID: "outside-history", Checkpoint: pausedCheckpoint(t, nil)}, Observations: []HistoryObjectRef{}}); waitReasonOf(err) != "wait-pending" {
		t.Fatalf("parked, through the history port: %v", err)
	}
	clock.advance(leaseMinute)
	if _, err := WakeRun(ctx, store, decisionEvidence()); err != nil {
		t.Fatal(err)
	}
	if err := outside("outside-2"); waitReasonOf(err) != "wait-pending" {
		t.Fatalf("woken: %v", err)
	}
	clock.advance(waitTTL)
	if _, err := WakeRun(ctx, store, WakeEvidence{Version: 1, Resource: "run-approval", Wait: "wait-1", Evidence: "deadline-1", Kind: "deadline", Key: waitKey}); err != nil {
		t.Fatal(err)
	}
	if err := outside("outside-3"); waitReasonOf(err) != "closed" {
		t.Fatalf("closed: %v", err)
	}
	if head, _ := store.Load(ctx, "run-approval"); head.Revision != parked.Checkpoint.Revision {
		t.Fatal("the head moved")
	}
}

func TestWaitReviewAnUnsettledApprovedCallEndsItsWaitAtTheExpiry(t *testing.T) {
	ctx := context.Background()
	clock, store, _, _ := resumeApprovalStore(t, waitHour, nil)
	clock.advance(leaseMinute)
	if _, err := WakeRun(ctx, store, decisionEvidence()); err != nil {
		t.Fatal(err)
	}
	resumed, err := ResumeParked(ctx, approvedResume(t, store))
	if err != nil {
		t.Fatal(err)
	}
	// The claimer records the decision and stops before it dispatches: the call is approved and unsettled.
	head, _ := store.Load(ctx, "run-approval")
	approved, err := store.CommitFenced(ctx, RunCommit{RunID: "run-approval", ExpectedRevision: head.Revision, CommitID: "approved", Checkpoint: pausedCheckpoint(t, func(c map[string]any) { c["approval"].(map[string]any)["state"] = "approved" })}, resumed.Fence)
	if err != nil {
		t.Fatal(err)
	}
	deadline := WakeEvidence{Version: 1, Resource: "run-approval", Wait: "wait-1", Evidence: "deadline-1", Kind: "deadline", Key: waitKey}
	clock.advance(waitHour - leaseMinute - 1)
	if _, err := WakeRun(ctx, store, deadline); waitReasonOf(err) != "not-due" {
		t.Fatalf("before the expiry: %v", err)
	}
	clock.advance(1)
	if _, err := store.AcquireLease(ctx, LeaseAcquire{Resource: "run-approval", Owner: "worker-c", ExpectedRevision: approved.Revision, DurationMs: waitHour}); waitReasonOf(err) != "deadline-passed" {
		t.Fatalf("a lease at the expiry: %v", err)
	}
	ended, err := WakeRun(ctx, store, deadline)
	if err != nil || ended.Kind != "deadline" || ended.At != waitTime(clock, waitHour) {
		t.Fatalf("the deadline wake: %+v %v", ended, err)
	}
	if again, err := WakeRun(ctx, store, deadline); err != nil || again != ended {
		t.Fatalf("its duplicate: %v", err)
	}
	other := deadline
	other.Evidence = "deadline-2"
	if _, err := WakeRun(ctx, store, other); waitReasonOf(err) != "stale" {
		t.Fatalf("another deadline wake: %v", err)
	}
	held, _ := store.ReadWait(ctx, "run-approval", "wait-1")
	if held.State != "expired" || held.Resumed == nil || held.Resumed.Owner != "worker-b" || *held.Expired != ended {
		t.Fatalf("wait %+v", held)
	}
	report, err := WaitReportOf(ctx, store, "run-approval", clock.now())
	decisions := []string{}
	for _, entry := range report.Entries {
		decisions = append(decisions, entry.Decision)
	}
	if err != nil || !slices.Equal(decisions, []string{"park", "wake", "resume", "deadline"}) {
		t.Fatalf("report %v %v", decisions, err)
	}
	clock.advance(30 * 24 * waitHour)
	if _, err := store.AcquireLease(ctx, LeaseAcquire{Resource: "run-approval", Owner: "worker-c", ExpectedRevision: approved.Revision, DurationMs: waitHour}); waitReasonOf(err) != "closed" {
		t.Fatalf("closed for good: %v", err)
	}
	if _, err := store.Commit(ctx, RunCommit{RunID: "run-approval", ExpectedRevision: approved.Revision, CommitID: "late", Events: [][]byte{[]byte(`{"version":1}`)}}); waitReasonOf(err) != "closed" {
		t.Fatalf("an unfenced commit: %v", err)
	}

	// A settled call is never ended by it: the deadline wake on its resumed wait stays stale.
	clock2, settledStore, _, _ := resumeApprovalStore(t, waitHour, nil)
	clock2.advance(leaseMinute)
	if _, err := WakeRun(ctx, settledStore, decisionEvidence()); err != nil {
		t.Fatal(err)
	}
	claimed, err := ResumeParked(ctx, approvedResume(t, settledStore))
	if err != nil {
		t.Fatal(err)
	}
	head, _ = settledStore.Load(ctx, "run-approval")
	if _, err := settledStore.CommitFenced(ctx, RunCommit{RunID: "run-approval", ExpectedRevision: head.Revision, CommitID: "executed", Checkpoint: pausedCheckpoint(t, func(c map[string]any) { c["approval"].(map[string]any)["state"] = "executed" })}, claimed.Fence); err != nil {
		t.Fatal(err)
	}
	clock2.advance(waitHour)
	if _, err := WakeRun(ctx, settledStore, deadline); waitReasonOf(err) != "stale" {
		t.Fatalf("a settled call's wait: %v", err)
	}
}

func TestWaitReviewALaterWaitsClaimIsBoundedByAnUnsettledApproval(t *testing.T) {
	clock := newLeaseClock()
	leases, table := NewLeaseTable(clock.now), NewWaitTable(clock.now)
	first := pausedCheckpoint(t, nil)
	value, _ := ParseJSON(first)
	paused, _ := ReadExecution(value)
	pins, _ := PinsFrom(paused, waitModel)
	binding, _ := ApprovalBindingOf(*paused.Approval)
	ttl := int64(waitHour)
	a, _ := leases.Acquire(LeaseAcquire{Resource: "run-t", Owner: "worker-a", ExpectedRevision: "rev-1", DurationMs: waitHour}, "rev-1")
	if _, _, err := table.Park(RunWait{Version: 1, Resource: "run-t", Wait: "wait-1", Reason: "approval", Pending: contract.RunWaitPending{Approval: &contract.RunWaitPendingApproval{Kind: "approval", CallID: "call_2_1", Name: "tracker__create_issue", Tool: "create_issue"}}, Condition: WaitCondition{Event: &contract.RunWaitEventCondition{Kind: "approval-decision", Subject: "call_2_1"}}, Limits: &WaitLimits{ApprovalTtlMs: &ttl}, Approval: &binding, Continuation: "k-1", Checkpoint: contract.RunWaitCheckpointBinding{Revision: "rev-1", Digest: CheckpointBytesDigest(first)}, Pins: pins, WakeKeyDigest: waitKeyDigest(t, reviewKey("t1")), Release: []WaitRelease{}}, FenceOf(a), &RunHead{RunReceipt: RunReceipt{Revision: "rev-1"}, RunID: "run-t", Checkpoint: first}, leases); err != nil {
		t.Fatal(err)
	}
	clock.advance(leaseMinute)
	if _, err := table.Wake(WakeEvidence{Version: 1, Resource: "run-t", Wait: "wait-1", Evidence: "decision-1", Kind: "event", Event: &contract.RunWaitEventCondition{Kind: "approval-decision", Subject: "call_2_1"}, Key: reviewKey("t1")}); err != nil {
		t.Fatal(err)
	}
	claim := func(wait, evidence, revision string, pins ResumePins) WaitClaim {
		digest, _ := PinsDigest(pins)
		return WaitClaim{Resource: "run-t", Wait: wait, Evidence: evidence, Lease: LeaseAcquire{Resource: "run-t", Owner: "worker-b", ExpectedRevision: revision, DurationMs: 24 * waitHour}, Revalidation: Revalidation{Version: 1, Resource: "run-t", Wait: wait, Pins: digest, Evidence: "sha256:" + strings.Repeat("e", 64), Changes: []ResumeChange{}}}
	}
	_, claimed, err := table.Resume(claim("wait-1", "decision-1", "rev-1", pins), &RunHead{RunReceipt: RunReceipt{Revision: "rev-1"}, RunID: "run-t", Checkpoint: first}, leases)
	if err != nil || claimed.Expiry != waitTime(clock, waitHour) {
		t.Fatalf("the approval's claim %+v %v", claimed, err)
	}
	// A budget pause over the unsettled approval: a checkpoint the package's writer does not produce,
	// driven directly.
	second := pausedCheckpoint(t, func(c map[string]any) {
		c["approval"].(map[string]any)["state"] = "approved"
		c["used"], c["limit"] = 20, 20
	})
	value, _ = ParseJSON(second)
	later, _ := ReadExecution(value)
	laterPins, _ := PinsFrom(later, waitModel)
	wakeAt := waitTime(clock, 2*leaseMinute)
	if _, _, err := table.Park(RunWait{Version: 1, Resource: "run-t", Wait: "wait-2", Reason: "budget", Pending: contract.RunWaitPending{Budget: &contract.RunWaitPendingBudget{Kind: "budget", Unit: "model-requests", Used: 20, Limit: 20}}, Condition: WaitCondition{WakeAt: &wakeAt}, Continuation: "k-2", Checkpoint: contract.RunWaitCheckpointBinding{Revision: "rev-2", Digest: CheckpointBytesDigest(second)}, Pins: laterPins, WakeKeyDigest: waitKeyDigest(t, reviewKey("t2")), Release: []WaitRelease{}}, FenceOf(claimed), &RunHead{RunReceipt: RunReceipt{Revision: "rev-2"}, RunID: "run-t", Checkpoint: second}, leases); err != nil {
		t.Fatal(err)
	}
	clock.advance(leaseMinute)
	if _, err := table.Wake(WakeEvidence{Version: 1, Resource: "run-t", Wait: "wait-2", Evidence: "tick-1", Kind: "time", Key: reviewKey("t2")}); err != nil {
		t.Fatal(err)
	}
	if _, budget, err := table.Resume(claim("wait-2", "tick-1", "rev-2", laterPins), &RunHead{RunReceipt: RunReceipt{Revision: "rev-2"}, RunID: "run-t", Checkpoint: second}, leases); err != nil || budget.Expiry != waitTime(clock, waitHour) {
		t.Fatalf("the budget wait's claim %+v %v", budget, err)
	}
}

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
