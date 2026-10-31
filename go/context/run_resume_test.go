package runcontext

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

// The Go twin of packages/harness/storage/resume.test.ts (#225, #227, #265 M2).

func readResumeVector(t *testing.T, name string) map[string]json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile("../../contract/vectors/resume/" + name)
	if err != nil {
		t.Fatal(err)
	}
	var file map[string]json.RawMessage
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	return file
}
func resumeReasonOf(err error) string {
	var refused *ResumeRefused
	if errors.As(err, &refused) {
		return "resume:" + refused.Reason
	}
	return waitReasonOf(err)
}
func rawMember(row json.RawMessage, name string) json.RawMessage {
	var object map[string]json.RawMessage
	_ = json.Unmarshal(row, &object)
	return object[name]
}

func TestResumeLimitsVectorsBothReaders(t *testing.T) {
	file := readResumeVector(t, "limits.json")
	for i, item := range waitList(t, file["items"]) {
		if held, err := ReadWaitLimits(item); err != nil || !sameWaitJSON(t, held, item) {
			t.Fatalf("limits %d: %v", i, err)
		}
	}
	for _, row := range waitList(t, file["refused"]) {
		if _, err := ReadWaitLimits(rawMember(row, "limits")); resumeReasonOf(err) != "resume:invalid" {
			t.Errorf("%s accepted", rawMember(row, "problem"))
		}
	}
	runLimits := rawMember(file["runLimits"], "items")
	for _, item := range waitList(t, runLimits) {
		if held, err := ReadRunLimit(item); err != nil || !sameWaitJSON(t, held, item) {
			t.Fatalf("run limit: %v", err)
		}
	}
	for _, row := range waitList(t, rawMember(file["runLimits"], "refused")) {
		if _, err := ReadRunLimit(rawMember(row, "limit")); resumeReasonOf(err) != "resume:invalid" {
			t.Errorf("%s accepted", rawMember(row, "problem"))
		}
	}
	for _, row := range waitList(t, file["stamps"]) {
		var stamp struct {
			Case     string        `json:"case"`
			Limits   *WaitLimits   `json:"limits"`
			ParkedAt string        `json:"parkedAt"`
			Ceiling  *string       `json:"ceiling"`
			Deadline *WaitDeadline `json:"deadline"`
		}
		if err := json.Unmarshal(row, &stamp); err != nil {
			t.Fatal(err)
		}
		var ceiling *int64
		if stamp.Ceiling != nil {
			at := waitTimeMillis(*stamp.Ceiling)
			ceiling = &at
		}
		got, err := StampDeadline(stamp.Limits, waitTimeMillis(stamp.ParkedAt), ceiling)
		if err != nil || !reflect.DeepEqual(got, stamp.Deadline) {
			t.Errorf("%s: %+v %v", stamp.Case, got, err)
		}
		if stamp.Deadline != nil {
			if held, err := readWaitDeadline(rawMember(row, "deadline"), stamp.Limits, stamp.ParkedAt); err != nil || !reflect.DeepEqual(held, *stamp.Deadline) {
				t.Errorf("%s: read %v", stamp.Case, err)
			}
		}
	}
	for _, row := range waitList(t, file["deadlinesRefused"]) {
		var refused struct {
			Limits   *WaitLimits `json:"limits"`
			ParkedAt string      `json:"parkedAt"`
		}
		_ = json.Unmarshal(row, &refused)
		if _, err := readWaitDeadline(rawMember(row, "deadline"), refused.Limits, refused.ParkedAt); resumeReasonOf(err) != "resume:invalid" {
			t.Errorf("%s accepted", rawMember(row, "problem"))
		}
	}
}

func TestResumePinsVectorsBothReaders(t *testing.T) {
	file := readResumeVector(t, "pins.json")
	var digests []string
	_ = json.Unmarshal(file["digests"], &digests)
	for i, item := range waitList(t, file["items"]) {
		pins, err := ReadResumePins(item)
		if err != nil || !sameWaitJSON(t, pins, item) {
			t.Fatalf("pins %d: %v", i, err)
		}
		if digest, _ := PinsDigest(pins); digest != digests[i] {
			t.Errorf("pins %d digest %s", i, digest)
		}
	}
	var policies [][2]json.RawMessage
	_ = json.Unmarshal(file["policies"], &policies)
	for _, pair := range policies {
		var members map[string]contract.PolicyMember
		var digest string
		_ = json.Unmarshal(pair[0], &members)
		_ = json.Unmarshal(pair[1], &digest)
		if got := PolicyIdentity(members); got != digest {
			t.Errorf("policy identity %s, want %s", got, digest)
		}
	}
	for _, row := range waitList(t, file["checkpoints"]) {
		var checkpoint contract.Checkpoint
		if err := json.Unmarshal(rawMember(row, "checkpoint"), &checkpoint); err != nil {
			t.Fatal(err)
		}
		var model PinnedModel
		_ = json.Unmarshal(rawMember(row, "model"), &model)
		pins, err := PinsFrom(&checkpoint, model)
		if err != nil || !sameWaitJSON(t, pins, rawMember(row, "pins")) {
			got, _ := json.Marshal(pins)
			t.Errorf("%s: %s %v", rawMember(row, "case"), got, err)
		}
		if want := rawMember(row, "approval"); want != nil {
			if binding, err := ApprovalBindingOf(*checkpoint.Approval); err != nil || !sameWaitJSON(t, binding, want) {
				t.Errorf("%s: binding %+v %v", rawMember(row, "case"), binding, err)
			}
		}
	}
	for _, row := range waitList(t, file["checkpointsRefused"]) {
		var checkpoint contract.Checkpoint
		if err := json.Unmarshal(rawMember(row, "checkpoint"), &checkpoint); err != nil {
			t.Fatal(err)
		}
		if _, err := PinsFrom(&checkpoint, waitModel); resumeReasonOf(err) != "resume:invalid" {
			t.Errorf("%s accepted", rawMember(row, "problem"))
		}
	}
	for _, name := range []string{"refused", "readerRefused"} {
		for _, row := range waitList(t, file[name]) {
			if _, err := ReadResumePins(rawMember(row, "pins")); resumeReasonOf(err) != "resume:invalid" {
				t.Errorf("%s: %s accepted", name, rawMember(row, "problem"))
			}
		}
	}
}

func TestResumeEvidenceRevalidationAndRefusalVectors(t *testing.T) {
	file := readResumeVector(t, "evidence.json")
	var digests []string
	_ = json.Unmarshal(file["digests"], &digests)
	for i, item := range waitList(t, file["items"]) {
		evidence, err := ReadResumeEvidence(item)
		if err != nil || !sameWaitJSON(t, evidence, item) {
			t.Fatalf("evidence %d: %v", i, err)
		}
		if digest, _ := ResumeEvidenceDigest(evidence); digest != digests[i] {
			t.Errorf("evidence %d digest %s", i, digest)
		}
	}
	for _, name := range []string{"refused", "readerRefused"} {
		for _, row := range waitList(t, file[name]) {
			if _, err := ReadResumeEvidence(rawMember(row, "evidence")); resumeReasonOf(err) != "resume:invalid" {
				t.Errorf("evidence %s: %s accepted", name, rawMember(row, "problem"))
			}
		}
	}
	reval := readResumeVector(t, "revalidation.json")
	items := waitList(t, reval["items"])
	for i, item := range items {
		if held, err := ReadRevalidation(item); err != nil || !sameWaitJSON(t, held, item) {
			t.Fatalf("revalidation %d: %v", i, err)
		}
	}
	for _, row := range waitList(t, reval["cases"]) {
		var c struct {
			Case string `json:"case"`
			Wait struct {
				Resource string           `json:"resource"`
				Wait     string           `json:"wait"`
				Reason   string           `json:"reason"`
				Pins     ResumePins       `json:"pins"`
				Approval *ApprovalBinding `json:"approval"`
			} `json:"wait"`
			Outcome struct {
				Revalidation *int   `json:"revalidation"`
				Refused      string `json:"refused"`
			} `json:"outcome"`
		}
		if err := json.Unmarshal(row, &c); err != nil {
			t.Fatal(err)
		}
		evidence, err := ReadResumeEvidence(rawMember(row, "evidence"))
		if err != nil {
			t.Fatalf("%s: %v", c.Case, err)
		}
		got, err := RevalidateResume(RevalidatedWait{Resource: c.Wait.Resource, Wait: c.Wait.Wait, Reason: c.Wait.Reason, Pins: c.Wait.Pins, Approval: c.Wait.Approval}, evidence)
		if c.Outcome.Revalidation != nil {
			if err != nil || !sameWaitJSON(t, got, items[*c.Outcome.Revalidation]) {
				raw, _ := json.Marshal(got)
				t.Errorf("%s: %s %v", c.Case, raw, err)
			}
		} else if resumeReasonOf(err) != "resume:"+c.Outcome.Refused {
			t.Errorf("%s: %v, want %s", c.Case, err, c.Outcome.Refused)
		}
	}
	for _, name := range []string{"refused", "readerRefused"} {
		for _, row := range waitList(t, reval[name]) {
			if _, err := ReadRevalidation(rawMember(row, "revalidation")); resumeReasonOf(err) != "resume:invalid" {
				t.Errorf("revalidation %s: %s accepted", name, rawMember(row, "problem"))
			}
		}
	}
	refusals := readResumeVector(t, "refusal.json")
	var reasons []string
	for _, item := range waitList(t, refusals["items"]) {
		record, err := ReadResumeRefusal(item)
		if err != nil || !sameWaitJSON(t, (&ResumeRefused{Reason: record.Reason}).Record(), item) {
			t.Fatal(err)
		}
		reasons = append(reasons, record.Reason)
	}
	if !reflect.DeepEqual(reasons, ResumeRefusalReasons) {
		t.Errorf("reasons %v", reasons)
	}
	for _, item := range waitList(t, refusals["refused"]) {
		if _, err := ReadResumeRefusal(item); err == nil {
			t.Errorf("refusal %s accepted", item)
		}
	}
}

// resumeApprovalStore parks the published paused approval with an approval TTL and limits.
func resumeApprovalStore(t *testing.T, ttl int64, run *string) (*leaseClock, durableWaitStore, LeaseFence, RunWait) {
	t.Helper()
	ctx := context.Background()
	clock := newLeaseClock()
	store := durableWaitStore{NewMemoryRunStore(MemoryRunStoreOptions{Now: clock.now})}
	lease := leaseAcquire(t, store.MemoryRunStore, "run-approval", "worker-a")
	if _, err := store.CommitFenced(ctx, RunCommit{RunID: "run-approval", CommitID: "pause", Checkpoint: pausedCheckpoint(t, nil)}, FenceOf(lease)); err != nil {
		t.Fatal(err)
	}
	wait, _, err := ParkRun(ctx, ParkInput{Store: store, Fence: FenceOf(lease), Wait: "wait-1", Continuation: "continue-1", Condition: WaitCondition{Event: &contract.RunWaitEventCondition{Kind: "approval-decision", Subject: "call_2_1"}}, Model: waitModel, Limits: &WaitLimits{ApprovalTtlMs: &ttl, Run: run}, WakeKeyDigest: waitKeyDigest(t, waitKey), Release: []WaitRelease{}})
	if err != nil {
		t.Fatal(err)
	}
	return clock, store, FenceOf(lease), wait
}
func approvedResume(t *testing.T, store RunStore) ResumeInput {
	return withCurrent(t, ResumeInput{Store: store, Resource: "run-approval", Wait: "wait-1", Evidence: "decision-1", Owner: "worker-b", DurationMs: waitHour, Approval: &contract.CheckpointApprovalDecision{CallID: "call_2_1", Approved: true}})
}

func TestResumeAParkedRunIsClosedAndTheResumeIsTheOnlyContinuation(t *testing.T) {
	ctx := context.Background()
	clock, store, fence, wait := resumeApprovalStore(t, waitHour, nil)
	// The reviewer's case (#265 M2): a worker leases the parked run against its parked revision.
	if _, err := store.AcquireLease(ctx, LeaseAcquire{Resource: "run-approval", Owner: "worker-x", ExpectedRevision: wait.Checkpoint.Revision, DurationMs: waitHour}); waitReasonOf(err) != "wait-pending" {
		t.Fatalf("a fresh lease on a parked run: %v", err)
	}
	if _, err := store.CommitFenced(ctx, RunCommit{RunID: "run-approval", ExpectedRevision: wait.Checkpoint.Revision, CommitID: "late", Checkpoint: pausedCheckpoint(t, nil)}, fence); leaseReason(err) != "released" {
		t.Fatalf("the parking fence: %v", err)
	}
	clock.advance(leaseMinute)
	if _, err := WakeRun(ctx, store, decisionEvidence()); err != nil {
		t.Fatal(err)
	}
	// Woken but unclaimed still holds the run.
	if _, err := store.AcquireLease(ctx, LeaseAcquire{Resource: "run-approval", Owner: "worker-x", ExpectedRevision: wait.Checkpoint.Revision, DurationMs: waitHour}); waitReasonOf(err) != "wait-pending" {
		t.Fatalf("a fresh lease on a woken run: %v", err)
	}
	resumed, err := ResumeParked(ctx, approvedResume(t, store))
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Lease.Epoch != "2" || resumed.Wait.Revalidation == nil || !reflect.DeepEqual(*resumed.Wait.Revalidation, resumed.Revalidation) {
		t.Fatalf("resumed %+v", resumed)
	}
	if _, err := store.AcquireLease(ctx, LeaseAcquire{Resource: "run-approval", Owner: "worker-x", ExpectedRevision: wait.Checkpoint.Revision, DurationMs: waitHour}); leaseReason(err) != "held" {
		t.Fatalf("the claim's lease: %v", err)
	}
	if _, err := store.ReleaseLease(ctx, resumed.Fence); err != nil {
		t.Fatal(err)
	}
	if lease, err := store.AcquireLease(ctx, LeaseAcquire{Resource: "run-approval", Owner: "worker-c", ExpectedRevision: wait.Checkpoint.Revision, DurationMs: waitHour}); err != nil || lease.Epoch != "3" {
		t.Fatalf("a resumed wait still holds the run: %v", err)
	}
}

func TestResumeApprovalExpiryAtTheExactBoundary(t *testing.T) {
	ctx := context.Background()
	clock, store, _, wait := resumeApprovalStore(t, waitHour, nil)
	if wait.Deadline == nil || wait.Deadline.By != "approval" || wait.Deadline.At != waitTime(clock, waitHour) || wait.Approval == nil || wait.Approval.CallID != "call_2_1" {
		t.Fatalf("deadline %+v binding %+v", wait.Deadline, wait.Approval)
	}
	clock.advance(waitHour - 2)
	if _, err := WakeRun(ctx, store, decisionEvidence()); err != nil {
		t.Fatal(err)
	}
	clock.advance(1)
	resumed, err := ResumeParked(ctx, approvedResume(t, store))
	if err != nil {
		t.Fatal(err)
	}
	// The approval's expiry bounds the claim's lease; renewing the lease never renews the approval.
	if resumed.Lease.Expiry != waitTime(clock, waitHour) {
		t.Fatalf("claim lease %+v", resumed.Lease)
	}
	if renewed, err := store.RenewLease(ctx, resumed.Fence, waitHour); err != nil || renewed.Expiry != waitTime(clock, waitHour) {
		t.Fatalf("renewal %+v %v", renewed, err)
	}
	clock.advance(1)
	if _, err := store.RenewLease(ctx, resumed.Fence, waitHour); leaseReason(err) != "expired" {
		t.Fatalf("renewal at the expiry: %v", err)
	}
	host := NewReferenceEffectHost(store)
	admission, _ := NewEffectAdmission(resumed.Fence, EffectOperation{Run: "run-approval", Operation: "op-1", Source: "tracker", Tool: "create_issue", RequestDigest: "sha256:" + strings.Repeat("2", 64)})
	performed := 0
	if err := host.Dispatch(ctx, admission, func(context.Context) error { performed++; return nil }); leaseReason(err) != "expired" || performed != 0 {
		t.Fatalf("the approved call at its expiry: %v", err)
	}
	// At the expiry nothing claims a second approval wait's continuation either.
	clock2, store2, _, _ := resumeApprovalStore(t, waitHour, nil)
	clock2.advance(waitHour - 1)
	if _, err := WakeRun(ctx, store2, decisionEvidence()); err != nil {
		t.Fatal(err)
	}
	clock2.advance(1)
	if _, err := ResumeParked(ctx, approvedResume(t, store2)); waitReasonOf(err) != "deadline-passed" {
		t.Fatalf("a claim at the expiry: %v", err)
	}
	if lease, _ := store2.ReadLease(ctx, "run-approval"); lease.State != "released" || lease.Epoch != "1" {
		t.Fatalf("a refused claim took a lease: %+v", lease)
	}
}

func TestResumeLateEvidenceAndTheTerminalDeadline(t *testing.T) {
	ctx := context.Background()
	clock, store, _, wait := resumeApprovalStore(t, waitHour, nil)
	deadline := WakeEvidence{Version: 1, Resource: "run-approval", Wait: "wait-1", Evidence: "deadline-1", Kind: "deadline", Key: waitKey}
	clock.advance(waitHour - 1)
	if _, err := WakeRun(ctx, store, deadline); waitReasonOf(err) != "not-due" {
		t.Fatalf("early deadline: %v", err)
	}
	clock.advance(1)
	held, _ := store.ReadWait(ctx, "run-approval", "wait-1")
	if _, err := WakeRun(ctx, store, decisionEvidence()); waitReasonOf(err) != "deadline-passed" {
		t.Fatalf("a late decision: %v", err)
	}
	if after, _ := store.ReadWait(ctx, "run-approval", "wait-1"); !reflect.DeepEqual(held, after) {
		t.Fatal("a late decision changed the wait")
	}
	expired, err := WakeRun(ctx, store, deadline)
	if err != nil || expired.Kind != "deadline" || expired.At != waitTime(clock, waitHour) {
		t.Fatalf("deadline wake %+v %v", expired, err)
	}
	if again, err := WakeRun(ctx, store, deadline); err != nil || again != expired {
		t.Fatalf("duplicate deadline: %v", err)
	}
	if _, err := store.AcquireLease(ctx, LeaseAcquire{Resource: "run-approval", Owner: "worker-x", ExpectedRevision: wait.Checkpoint.Revision, DurationMs: waitHour}); waitReasonOf(err) != "closed" {
		t.Fatalf("a closed run leased: %v", err)
	}
	report, err := WaitReportOf(ctx, store, "run-approval", clock.now())
	if err != nil || len(report.Entries) != 2 || report.Entries[1].Decision != "deadline" || *report.Entries[0].Deadline != waitTime(clock, waitHour) {
		t.Fatalf("report %+v %v", report, err)
	}
	// A decision delivered at its wait's timeout is refused; only the timeout ends the wait.
	clock3 := newLeaseClock()
	store3 := durableWaitStore{NewMemoryRunStore(MemoryRunStoreOptions{Now: clock3.now})}
	lease := leaseAcquire(t, store3.MemoryRunStore, "run-approval", "worker-a")
	if _, err := store3.CommitFenced(ctx, RunCommit{RunID: "run-approval", CommitID: "pause", Checkpoint: pausedCheckpoint(t, nil)}, FenceOf(lease)); err != nil {
		t.Fatal(err)
	}
	timeout := waitTime(clock3, waitHour)
	if _, _, err := ParkRun(ctx, withWaitPolicy(ParkInput{Store: store3, Fence: FenceOf(lease), Wait: "wait-1", Continuation: "k", Condition: WaitCondition{Event: &contract.RunWaitEventCondition{Kind: "approval-decision", Subject: "call_2_1"}}, TimeoutAt: &timeout, WakeKeyDigest: waitKeyDigest(t, waitKey), Release: []WaitRelease{}})); err != nil {
		t.Fatal(err)
	}
	clock3.advance(waitHour)
	if _, err := WakeRun(ctx, store3, decisionEvidence()); waitReasonOf(err) != "timed-out" {
		t.Fatalf("a decision at the timeout: %v", err)
	}
}

func TestResumeApprovalTTLIsRequiredAndBounded(t *testing.T) {
	ctx := context.Background()
	clock := newLeaseClock()
	store := durableWaitStore{NewMemoryRunStore(MemoryRunStoreOptions{Now: clock.now})}
	lease := leaseAcquire(t, store.MemoryRunStore, "run-approval", "worker-a")
	if _, err := store.CommitFenced(ctx, RunCommit{RunID: "run-approval", CommitID: "pause", Checkpoint: pausedCheckpoint(t, nil)}, FenceOf(lease)); err != nil {
		t.Fatal(err)
	}
	input := ParkInput{Store: store, Fence: FenceOf(lease), Wait: "wait-1", Continuation: "k", Condition: WaitCondition{Event: &contract.RunWaitEventCondition{Kind: "approval-decision", Subject: "call_2_1"}}, Model: waitModel, WakeKeyDigest: waitKeyDigest(t, waitKey), Release: []WaitRelease{}}
	if _, _, err := ParkRun(ctx, input); waitReasonOf(err) != "approval-ttl-required" {
		t.Fatalf("no TTL: %v", err)
	}
	waitMs := int64(waitHour)
	input.Limits = &WaitLimits{WaitMs: &waitMs}
	if _, _, err := ParkRun(ctx, input); waitReasonOf(err) != "approval-ttl-required" {
		t.Fatalf("a wait limit is no TTL: %v", err)
	}
	for _, ttl := range []int64{0, -1, DeadlineDurationMaxMs + 1} {
		input.Limits = &WaitLimits{ApprovalTtlMs: &ttl}
		if _, _, err := ParkRun(ctx, input); waitReasonOf(err) != "invalid" {
			t.Fatalf("TTL %d: %v", ttl, err)
		}
	}
	if held, _ := store.ReadWait(ctx, "run-approval", "wait-1"); held != nil {
		t.Fatal("a refused park left a wait")
	}
	ttl := int64(1)
	input.Limits = &WaitLimits{ApprovalTtlMs: &ttl}
	wait, _, err := ParkRun(ctx, input)
	if err != nil || wait.Deadline.At != waitTime(clock, 1) || wait.Deadline.By != "approval" {
		t.Fatalf("one millisecond: %+v %v", wait.Deadline, err)
	}
}

func TestResumeRunDeadlineBoundsEveryLease(t *testing.T) {
	ctx := context.Background()
	clock := newLeaseClock()
	store := durableWaitStore{NewMemoryRunStore(MemoryRunStoreOptions{Now: clock.now})}
	lease, err := store.AcquireLease(ctx, LeaseAcquire{Resource: "run-r", Owner: "worker-a", DurationMs: waitHour})
	if err != nil {
		t.Fatal(err)
	}
	limit, err := LimitRun(ctx, store, RunLimit{Version: 1, Resource: "run-r", Run: waitTime(clock, 30*leaseMinute)})
	if err != nil || limit.Run != waitTime(clock, 30*leaseMinute) {
		t.Fatalf("limit %+v %v", limit, err)
	}
	if held, _ := store.ReadLease(ctx, "run-r"); held.Expiry != waitTime(clock, 30*leaseMinute) {
		t.Fatalf("the held lease was not shortened: %+v", held)
	}
	if renewed, err := store.RenewLease(ctx, FenceOf(lease), waitHour); err != nil || renewed.Expiry != waitTime(clock, 30*leaseMinute) {
		t.Fatalf("renewal %+v %v", renewed, err)
	}
	if later, err := LimitRun(ctx, store, RunLimit{Version: 1, Resource: "run-r", Run: waitTime(clock, 2*waitHour)}); err != nil || later.Run != waitTime(clock, 30*leaseMinute) {
		t.Fatalf("a later limit extended the run: %+v %v", later, err)
	}
	clock.advance(30*leaseMinute - 1)
	if _, err := store.CommitFenced(ctx, RunCommit{RunID: "run-r", CommitID: "c-1", Checkpoint: []byte(`{}`)}, FenceOf(lease)); err != nil {
		t.Fatal(err)
	}
	clock.advance(1)
	head, _ := store.Load(ctx, "run-r")
	if _, err := store.CommitFenced(ctx, RunCommit{RunID: "run-r", ExpectedRevision: head.Revision, CommitID: "c-2", Checkpoint: []byte(`{}`)}, FenceOf(lease)); leaseReason(err) != "expired" {
		t.Fatalf("a commit at the deadline: %v", err)
	}
	if _, err := store.AcquireLease(ctx, LeaseAcquire{Resource: "run-r", Owner: "worker-b", ExpectedRevision: head.Revision, DurationMs: waitHour}); waitReasonOf(err) != "deadline-passed" {
		t.Fatalf("a lease at the deadline: %v", err)
	}
	// A park inherits the run deadline the store holds; its later run limit never extends it.
	run := waitTime(newLeaseClock(), 5*waitHour)
	clock2, store2, _, _ := func() (*leaseClock, durableWaitStore, LeaseFence, RunWait) {
		c := newLeaseClock()
		s := durableWaitStore{NewMemoryRunStore(MemoryRunStoreOptions{Now: c.now})}
		if _, err := LimitRun(ctx, s, RunLimit{Version: 1, Resource: "run-approval", Run: waitTime(c, 2*waitHour)}); err != nil {
			t.Fatal(err)
		}
		l := leaseAcquire(t, s.MemoryRunStore, "run-approval", "worker-a")
		if _, err := s.CommitFenced(ctx, RunCommit{RunID: "run-approval", CommitID: "pause", Checkpoint: pausedCheckpoint(t, nil)}, FenceOf(l)); err != nil {
			t.Fatal(err)
		}
		ttl := int64(4 * waitHour)
		w, attention, err := ParkRun(ctx, ParkInput{Store: s, Fence: FenceOf(l), Wait: "wait-1", Continuation: "k", Condition: WaitCondition{Event: &contract.RunWaitEventCondition{Kind: "approval-decision", Subject: "call_2_1"}}, Model: waitModel, Limits: &WaitLimits{Run: &run, ApprovalTtlMs: &ttl}, WakeKeyDigest: waitKeyDigest(t, waitKey), Release: []WaitRelease{}})
		if err != nil || w.Deadline.At != waitTime(c, 2*waitHour) || w.Deadline.By != "run" || *w.Deadline.Run != waitTime(c, 2*waitHour) || *attention.Deadline != waitTime(c, 2*waitHour) {
			t.Fatalf("inherited deadline %+v %v", w.Deadline, err)
		}
		return c, s, FenceOf(l), w
	}()
	_, _ = clock2, store2
}

func TestResumeAnUnknownEffectIsNeverPerformedAgain(t *testing.T) {
	ctx := context.Background()
	clock, store, _, wait := resumeApprovalStore(t, waitHour, nil)
	clock.advance(leaseMinute)
	if _, err := WakeRun(ctx, store, decisionEvidence()); err != nil {
		t.Fatal(err)
	}
	resumed, err := ResumeParked(ctx, approvedResume(t, store))
	if err != nil {
		t.Fatal(err)
	}
	host := NewReferenceEffectHost(store)
	operation := EffectOperation{Run: "run-approval", Operation: "op-1", Source: "tracker", Tool: "create_issue", RequestDigest: "sha256:" + strings.Repeat("2", 64)}
	performed := 0
	admission, _ := NewEffectAdmission(resumed.Fence, operation)
	if err := host.Dispatch(ctx, admission, func(context.Context) error { performed++; return errors.New("the connection dropped") }); err == nil {
		t.Fatal("the dropped call succeeded")
	}
	// The claimer gives up its lease; another worker leases the run before the approval expires.
	if _, err := store.ReleaseLease(ctx, resumed.Fence); err != nil {
		t.Fatal(err)
	}
	later, err := store.AcquireLease(ctx, LeaseAcquire{Resource: "run-approval", Owner: "worker-c", ExpectedRevision: wait.Checkpoint.Revision, DurationMs: waitHour})
	if err != nil {
		t.Fatal(err)
	}
	again, _ := NewEffectAdmission(FenceOf(later), operation)
	if err := host.Dispatch(ctx, again, func(context.Context) error { performed++; return nil }); leaseReason(err) != "already-admitted" || performed != 1 {
		t.Fatalf("an unknown effect performed again: %v (%d)", err, performed)
	}
}

func TestResumeRevalidatesThePinnedAuthorityBeforeAnythingIsClaimed(t *testing.T) {
	ctx := context.Background()
	clock, store, _, wait := resumeApprovalStore(t, waitHour, nil)
	clock.advance(leaseMinute)
	if _, err := WakeRun(ctx, store, decisionEvidence()); err != nil {
		t.Fatal(err)
	}
	input := approvedResume(t, store)
	current := *input.Current
	missing := input
	missing.Current = nil
	cases := map[string]ResumeInput{"resume:evidence-missing": missing}
	changed := current
	changed.Model.Model = "other"
	cases["resume:model-changed"] = func() ResumeInput { i := input; c := changed; i.Current = &c; return i }()
	retired := current
	retired.Model.Status = "retired"
	cases["resume:model-retired"] = func() ResumeInput { i := input; c := retired; i.Current = &c; return i }()
	layer := current
	digest, newLayer := "sha256:"+strings.Repeat("b", 64), "sha256:"+strings.Repeat("c", 64)
	layer.Policy = contract.RunResumeCurrentPolicy{Digest: digest, Layer: &newLayer}
	cases["resume:approval-changed"] = func() ResumeInput { i := input; c := layer; i.Current = &c; return i }()
	noSources := current
	noSources.Sources = []SourcePin{}
	cases["resume:evidence-missing "] = func() ResumeInput { i := input; c := noSources; i.Current = &c; return i }()
	for want, bad := range cases {
		if _, err := ResumeParked(ctx, bad); resumeReasonOf(err) != strings.TrimSpace(want) {
			t.Errorf("%s: %v", want, err)
		}
	}
	if held, _ := store.ReadWait(ctx, "run-approval", "wait-1"); held.State != "woken" {
		t.Fatalf("a refused revalidation changed the wait: %s", held.State)
	}
	if lease, _ := store.ReadLease(ctx, "run-approval"); lease.State != "released" || lease.Epoch != "1" {
		t.Fatalf("a refused revalidation took a lease: %+v", lease)
	}
	admitted := current
	admitted.Model = contract.RunResumeCurrentModel{Provider: "openai", Model: "fixture-2", Status: "available"}
	admitted.Admissions = []ResumeAdmission{{Kind: "model", Subject: "model", From: "openai/fixture", To: "openai/fixture-2", Actor: "person-owner", At: waitTime(clock, 0)}}
	input.Current = &admitted
	resumed, err := ResumeParked(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	pins, _ := PinsDigest(wait.Pins)
	if len(resumed.Revalidation.Changes) != 1 || resumed.Revalidation.Changes[0].From != "openai/fixture" || resumed.Revalidation.Changes[0].To != "openai/fixture-2" || resumed.Revalidation.Pins != pins {
		t.Fatalf("revalidation %+v", resumed.Revalidation)
	}
}

func TestResumeEveryUnresumedWaitHoldsTheRunInTheTableItself(t *testing.T) {
	// A host store kind implements the same transitions: the table is driven directly, with a lease
	// taken around the store's own guard, as a store that forgot it would grant one.
	clock := newLeaseClock()
	leases, table := NewLeaseTable(clock.now), NewWaitTable(clock.now)
	checkpoint := pausedCheckpoint(t, nil)
	head := &RunHead{RunReceipt: RunReceipt{Revision: "rev-1"}, RunID: "run-t", Checkpoint: checkpoint}
	value, _ := ParseJSON(checkpoint)
	paused, _ := ReadExecution(value)
	pins, _ := PinsFrom(paused, waitModel)
	binding, _ := ApprovalBindingOf(*paused.Approval)
	ttl := int64(waitHour)
	park := func(wait string, fence LeaseFence) error {
		_, _, err := table.Park(RunWait{Version: 1, Resource: "run-t", Wait: wait, Reason: "approval", Pending: contract.RunWaitPending{Approval: &contract.RunWaitPendingApproval{Kind: "approval", CallID: "call_2_1", Name: "tracker__create_issue", Tool: "create_issue"}}, Condition: WaitCondition{Event: &contract.RunWaitEventCondition{Kind: "approval-decision", Subject: "call_2_1"}}, Limits: &WaitLimits{ApprovalTtlMs: &ttl}, Approval: &binding, Continuation: "k", Checkpoint: contract.RunWaitCheckpointBinding{Revision: "rev-1", Digest: CheckpointBytesDigest(checkpoint)}, Pins: pins, WakeKeyDigest: waitKeyDigest(t, waitKey), Release: []WaitRelease{}}, fence, head, leases)
		return err
	}
	a, _ := leases.Acquire(LeaseAcquire{Resource: "run-t", Owner: "worker-a", ExpectedRevision: "rev-1", DurationMs: waitHour}, "rev-1")
	if err := park("wait-1", FenceOf(a)); err != nil {
		t.Fatal(err)
	}
	if _, err := table.Wake(WakeEvidence{Version: 1, Resource: "run-t", Wait: "wait-1", Evidence: "decision-1", Kind: "event", Event: &contract.RunWaitEventCondition{Kind: "approval-decision", Subject: "call_2_1"}, Key: waitKey}); err != nil {
		t.Fatal(err)
	}
	x, _ := leases.Acquire(LeaseAcquire{Resource: "run-t", Owner: "worker-x", ExpectedRevision: "rev-1", DurationMs: waitHour}, "rev-1")
	if err := park("wait-2", FenceOf(x)); waitReasonOf(err) != "wait-pending" {
		t.Fatalf("a second park of a woken pause: %v", err)
	}
	if err := table.AdmitAcquire("run-t"); waitReasonOf(err) != "wait-pending" {
		t.Fatalf("admit: %v", err)
	}
}

func TestResumeARunLimitSetByAParkBoundsTheClaim(t *testing.T) {
	ctx := context.Background()
	clock := newLeaseClock()
	run := waitTime(clock, 30*leaseMinute)
	clock, store, _, _ := resumeApprovalStore(t, 4*waitHour, &run)
	clock.advance(leaseMinute)
	if _, err := WakeRun(ctx, store, decisionEvidence()); err != nil {
		t.Fatal(err)
	}
	resumed, err := ResumeParked(ctx, approvedResume(t, store))
	if err != nil || resumed.Lease.Expiry != run {
		t.Fatalf("claim lease %+v %v", resumed.Lease, err)
	}
	clock.advance(29 * leaseMinute)
	// #266 N1: the approved call has not settled, so only the approval's expiry ends the resumed wait.
	if _, err := WakeRun(ctx, store, WakeEvidence{Version: 1, Resource: "run-approval", Wait: "wait-1", Evidence: "deadline-1", Kind: "deadline", Key: waitKey}); waitReasonOf(err) != "not-due" {
		t.Fatalf("a deadline after the claim: %v", err)
	}
	if held, _ := store.ReadWait(ctx, "run-approval", "wait-1"); held.State != "resumed" {
		t.Fatalf("state %s", held.State)
	}
}

func TestResumeAQuestionItsDeadlineEndsIsUnanswered(t *testing.T) {
	ctx := context.Background()
	clock, store, fence, _ := questionStore(t)
	waitMs, ttl := int64(waitHour), int64(leaseMinute)
	wait, _, err := ParkRun(ctx, ParkInput{Store: store, Fence: fence, Wait: "wait-q", Continuation: "k", Condition: WaitCondition{Event: &contract.RunWaitEventCondition{Kind: "question-answer", Subject: "question-1"}}, Model: waitModel, Limits: &WaitLimits{WaitMs: &waitMs, ApprovalTtlMs: &ttl}, WakeKeyDigest: waitKeyDigest(t, waitKey), Release: []WaitRelease{}, Question: "question-1"})
	if err != nil {
		t.Fatal(err)
	}
	// An approval TTL is no question's: only the wait's limit applies.
	if wait.Limits.ApprovalTtlMs != nil || wait.Deadline.By != "wait" || wait.Deadline.At != waitTime(clock, waitHour) {
		t.Fatalf("limits %+v deadline %+v", wait.Limits, wait.Deadline)
	}
	clock.advance(waitHour)
	if _, err := WakeRun(ctx, store, WakeEvidence{Version: 1, Resource: "run-question", Wait: "wait-q", Evidence: "deadline-q", Kind: "deadline", Key: waitKey}); err != nil {
		t.Fatal(err)
	}
	ended, _ := store.ReadWait(ctx, "run-question", "wait-q")
	if ended.State != "expired" || ended.Resolution == nil || ended.Resolution.Kind != "unanswered" || ended.Resolution.At != waitTime(clock, waitHour) {
		t.Fatalf("ended %+v", ended)
	}
	report, err := WaitReportOf(ctx, store, "run-question", time.Time{})
	if err != nil || len(report.Entries) != 3 || report.Entries[1].Decision != "deadline" || report.Entries[2].Decision != "unanswered" {
		t.Fatalf("report %+v %v", report, err)
	}
}

func TestResumeParkPastTheRunDeadline(t *testing.T) {
	ctx := context.Background()
	clock := newLeaseClock()
	store := durableWaitStore{NewMemoryRunStore(MemoryRunStoreOptions{Now: clock.now})}
	lease := leaseAcquire(t, store.MemoryRunStore, "run-approval", "worker-a")
	if _, err := store.CommitFenced(ctx, RunCommit{RunID: "run-approval", CommitID: "pause", Checkpoint: pausedCheckpoint(t, nil)}, FenceOf(lease)); err != nil {
		t.Fatal(err)
	}
	clock.advance(30_000)
	run, ttl := waitTime(clock, 30_000), int64(waitHour)
	if _, _, err := ParkRun(ctx, ParkInput{Store: store, Fence: FenceOf(lease), Wait: "wait-1", Continuation: "k", Condition: WaitCondition{Event: &contract.RunWaitEventCondition{Kind: "approval-decision", Subject: "call_2_1"}}, Model: waitModel, Limits: &WaitLimits{Run: &run, ApprovalTtlMs: &ttl}, WakeKeyDigest: waitKeyDigest(t, waitKey), Release: []WaitRelease{}}); waitReasonOf(err) != "deadline-passed" {
		t.Fatalf("a park at its run deadline: %v", err)
	}
	if held, _ := store.ReadWait(ctx, "run-approval", "wait-1"); held != nil {
		t.Fatal("a refused park left a wait")
	}
	if held, _ := store.ReadLease(ctx, "run-approval"); held.State != "held" {
		t.Fatalf("a refused park released the lease: %+v", held)
	}
}

func TestResumeAnUnseenWakeIsUnacknowledgedBeforeTheDecision(t *testing.T) {
	ctx := context.Background()
	clock, store, _, _ := resumeApprovalStore(t, waitHour, nil)
	input := withCurrent(t, ResumeInput{Store: store, Resource: "run-approval", Wait: "wait-1", Evidence: "decision-unseen", Owner: "worker-b", DurationMs: waitHour})
	if _, err := ResumeParked(ctx, input); waitReasonOf(err) != "unacknowledged" {
		t.Fatalf("a parked wait: %v", err)
	}
	clock.advance(leaseMinute)
	if _, err := WakeRun(ctx, store, decisionEvidence()); err != nil {
		t.Fatal(err)
	}
	if _, err := ResumeParked(ctx, input); waitReasonOf(err) != "unacknowledged" {
		t.Fatalf("another wake: %v", err)
	}
}

// resumedApproval parks the published paused approval with a one-hour TTL, wakes it and claims it at
// one minute.
func resumedApproval(t *testing.T) (*leaseClock, durableWaitStore, RunWait, ResumedWait) {
	t.Helper()
	ctx := context.Background()
	clock, store, _, wait := resumeApprovalStore(t, waitHour, nil)
	clock.advance(leaseMinute)
	if _, err := WakeRun(ctx, store, decisionEvidence()); err != nil {
		t.Fatal(err)
	}
	resumed, err := ResumeParked(ctx, approvedResume(t, store))
	if err != nil {
		t.Fatal(err)
	}
	return clock, store, wait, resumed
}

func TestResumeTheApprovalBoundsEveryLeaseUntilItsCallSettles(t *testing.T) {
	ctx := context.Background()
	clock, store, wait, resumed := resumedApproval(t)
	expiry := waitTime(clock, waitHour)
	// The claimer records the decision (approved, not yet run: unsettled) and gives its lease up; a
	// takeover before the expiry is bounded by it, renewal included.
	approved := pausedCheckpoint(t, func(c map[string]any) { c["approval"].(map[string]any)["state"] = "approved" })
	receipt, err := store.CommitFenced(ctx, RunCommit{RunID: "run-approval", ExpectedRevision: wait.Checkpoint.Revision, CommitID: "approved", Checkpoint: approved}, resumed.Fence)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReleaseLease(ctx, resumed.Fence); err != nil {
		t.Fatal(err)
	}
	clock.advance(29 * leaseMinute)
	takeover, err := store.AcquireLease(ctx, LeaseAcquire{Resource: "run-approval", Owner: "worker-c", ExpectedRevision: receipt.Revision, DurationMs: 24 * waitHour})
	if err != nil || takeover.Expiry != expiry {
		t.Fatalf("takeover %+v %v", takeover, err)
	}
	if renewed, err := store.RenewLease(ctx, FenceOf(takeover), 24*waitHour); err != nil || renewed.Expiry != expiry {
		t.Fatalf("renewal %+v %v", renewed, err)
	}
	// At the expiry nothing leases the run, and the takeover's fence commits and admits nothing.
	clock.advance(waitHour)
	if _, err := store.AcquireLease(ctx, LeaseAcquire{Resource: "run-approval", Owner: "worker-d", ExpectedRevision: receipt.Revision, DurationMs: waitHour}); waitReasonOf(err) != "deadline-passed" {
		t.Fatalf("a lease after the expiry: %v", err)
	}
	if _, err := store.CommitFenced(ctx, RunCommit{RunID: "run-approval", ExpectedRevision: receipt.Revision, CommitID: "late", Checkpoint: approved}, FenceOf(takeover)); leaseReason(err) != "expired" {
		t.Fatalf("a commit after the expiry: %v", err)
	}
	admission, _ := NewEffectAdmission(FenceOf(takeover), EffectOperation{Run: "run-approval", Operation: "op-late", Source: "tracker", Tool: "create_issue", RequestDigest: "sha256:" + strings.Repeat("3", 64)})
	performed := 0
	if err := NewReferenceEffectHost(store).Dispatch(ctx, admission, func(context.Context) error { performed++; return nil }); leaseReason(err) != "expired" || performed != 0 {
		t.Fatalf("an admission after the expiry: %v", err)
	}
}

func TestResumeASettledApprovalStopsBoundingTheRun(t *testing.T) {
	ctx := context.Background()
	clock, store, wait, resumed := resumedApproval(t)
	executed := pausedCheckpoint(t, func(c map[string]any) {
		c["status"] = "running"
		c["approval"].(map[string]any)["state"] = "executed"
	})
	receipt, err := store.CommitFenced(ctx, RunCommit{RunID: "run-approval", ExpectedRevision: wait.Checkpoint.Revision, CommitID: "executed", Checkpoint: executed}, resumed.Fence)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReleaseLease(ctx, resumed.Fence); err != nil {
		t.Fatal(err)
	}
	clock.advance(2 * waitHour)
	if lease, err := store.AcquireLease(ctx, LeaseAcquire{Resource: "run-approval", Owner: "worker-c", ExpectedRevision: receipt.Revision, DurationMs: waitHour}); err != nil || lease.Expiry != waitTime(clock, 3*waitHour+leaseMinute) {
		t.Fatalf("a lease after a settled approval: %+v %v", lease, err)
	}
}

func TestResumeACommitThatRewritesTheReviewedCallIsRefused(t *testing.T) {
	ctx := context.Background()
	_, store, wait, resumed := resumedApproval(t)
	for name, edit := range map[string]func(map[string]any){
		"arguments": func(c map[string]any) {
			c["approval"].(map[string]any)["args"] = map[string]any{"title": "Something else entirely"}
		},
		"call id": func(c map[string]any) { c["approval"].(map[string]any)["callId"] = "call-other" },
		"source": func(c map[string]any) {
			c["approval"].(map[string]any)["source"].(map[string]any)["id"] = "ledger"
		},
		"no approval": func(c map[string]any) { delete(c, "approval") },
	} {
		if _, err := store.CommitFenced(ctx, RunCommit{RunID: "run-approval", ExpectedRevision: wait.Checkpoint.Revision, CommitID: "rewrite", Checkpoint: pausedCheckpoint(t, edit)}, resumed.Fence); resumeReasonOf(err) != "resume:approval-changed" {
			t.Errorf("%s rewritten: %v", name, err)
		}
	}
	if head, _ := store.Load(ctx, "run-approval"); head.Revision != wait.Checkpoint.Revision {
		t.Fatal("a refused rewrite moved the head")
	}
	if _, err := store.CommitFenced(ctx, RunCommit{RunID: "run-approval", ExpectedRevision: wait.Checkpoint.Revision, CommitID: "same", Checkpoint: pausedCheckpoint(t, nil)}, resumed.Fence); err != nil {
		t.Fatalf("the reviewed call: %v", err)
	}
}

func TestResumeARunDeadlineAtTheUnixEpochBoundsTheClaim(t *testing.T) {
	ctx := context.Background()
	clock, store, _, wait := resumeApprovalStore(t, 4*waitHour, nil)
	clock.advance(leaseMinute)
	if _, err := WakeRun(ctx, store, decisionEvidence()); err != nil {
		t.Fatal(err)
	}
	if _, err := LimitRun(ctx, store, RunLimit{Version: 1, Resource: "run-approval", Run: "1970-01-01T00:00:00.000Z"}); err != nil {
		t.Fatal(err)
	}
	// #266 M1: a ceiling not after the store's time refuses the claim itself; nothing is leased.
	if _, err := ResumeParked(ctx, approvedResume(t, store)); waitReasonOf(err) != "deadline-passed" {
		t.Fatalf("a claim past the deadline: %v", err)
	}
	if lease, _ := store.ReadLease(ctx, wait.Resource); lease.State != "released" || lease.Epoch != "1" {
		t.Fatalf("a refused claim took a lease: %+v", lease)
	}
	// The lease table bounds a lease by a ceiling at the epoch like any other.
	zero := int64(0)
	if lease, err := NewLeaseTable(clock.now).acquireWithin(LeaseAcquire{Resource: "run-z", Owner: "worker-z", DurationMs: waitHour}, "", &zero); err != nil || lease.Expiry != "1970-01-01T00:00:00.000Z" {
		t.Fatalf("a ceiling at the epoch: %+v %v", lease, err)
	}
}

func TestResumeTheClaimChecksTheHeadDigestAsWellAsItsRevision(t *testing.T) {
	clock := newLeaseClock()
	leases, table := NewLeaseTable(clock.now), NewWaitTable(clock.now)
	checkpoint := pausedCheckpoint(t, nil)
	value, _ := ParseJSON(checkpoint)
	paused, _ := ReadExecution(value)
	pins, _ := PinsFrom(paused, waitModel)
	binding, _ := ApprovalBindingOf(*paused.Approval)
	ttl := int64(waitHour)
	a, _ := leases.Acquire(LeaseAcquire{Resource: "run-t", Owner: "worker-a", ExpectedRevision: "rev-1", DurationMs: waitHour}, "rev-1")
	head := &RunHead{RunReceipt: RunReceipt{Revision: "rev-1"}, RunID: "run-t", Checkpoint: checkpoint}
	wait, _, err := table.Park(RunWait{Version: 1, Resource: "run-t", Wait: "w", Reason: "approval", Pending: contract.RunWaitPending{Approval: &contract.RunWaitPendingApproval{Kind: "approval", CallID: "call_2_1", Name: "tracker__create_issue", Tool: "create_issue"}}, Condition: WaitCondition{Event: &contract.RunWaitEventCondition{Kind: "approval-decision", Subject: "call_2_1"}}, Limits: &WaitLimits{ApprovalTtlMs: &ttl}, Approval: &binding, Continuation: "k", Checkpoint: contract.RunWaitCheckpointBinding{Revision: "rev-1", Digest: CheckpointBytesDigest(checkpoint)}, Pins: pins, WakeKeyDigest: waitKeyDigest(t, waitKey), Release: []WaitRelease{}}, FenceOf(a), head, leases)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := table.Wake(WakeEvidence{Version: 1, Resource: "run-t", Wait: "w", Evidence: "decision-1", Kind: "event", Event: &contract.RunWaitEventCondition{Kind: "approval-decision", Subject: "call_2_1"}, Key: waitKey}); err != nil {
		t.Fatal(err)
	}
	digest, _ := PinsDigest(wait.Pins)
	claim := WaitClaim{Resource: "run-t", Wait: "w", Evidence: "decision-1", Lease: LeaseAcquire{Resource: "run-t", Owner: "worker-b", ExpectedRevision: "rev-1", DurationMs: waitHour}, Revalidation: Revalidation{Version: 1, Resource: "run-t", Wait: "w", Pins: digest, Evidence: "sha256:" + strings.Repeat("e", 64), Changes: []ResumeChange{}}}
	// A store that can repeat a revision: the same revision over other bytes is another head.
	other := &RunHead{RunReceipt: RunReceipt{Revision: "rev-1"}, RunID: "run-t", Checkpoint: pausedCheckpoint(t, func(c map[string]any) { c["explanation"] = "moved" })}
	if _, _, err := table.Resume(claim, other, leases); waitReasonOf(err) != "checkpoint-changed" {
		t.Fatalf("same revision, other bytes: %v", err)
	}
	if held, _, err := table.Resume(claim, head, leases); err != nil || held.State != "resumed" {
		t.Fatalf("the parked head: %v", err)
	}
}
