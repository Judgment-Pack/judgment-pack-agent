package runcontext

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

// The Go twin of packages/harness/storage/wait.test.ts (#224, #228).

func readWaitVector(t *testing.T, name string) map[string]json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile("../../contract/vectors/wait/" + name)
	if err != nil {
		t.Fatal(err)
	}
	var file map[string]json.RawMessage
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	return file
}
func waitList(t *testing.T, raw json.RawMessage) []json.RawMessage {
	t.Helper()
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		t.Fatal(err)
	}
	return items
}
func waitMember(t *testing.T, raw json.RawMessage, name string) json.RawMessage {
	t.Helper()
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	return object[name]
}
func waitReasonOf(err error) string {
	var refused *WaitRefused
	if errors.As(err, &refused) {
		return refused.Reason
	}
	var capability *WaitCapabilityRefused
	if errors.As(err, &capability) {
		return "capability:" + capability.Reason
	}
	return leaseReason(err)
}

// sameWaitJSON compares a decoded value with the vector's JSON as data.
func sameWaitJSON(t *testing.T, value any, raw json.RawMessage) bool {
	t.Helper()
	got, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var a, b any
	_ = json.Unmarshal(got, &a)
	_ = json.Unmarshal(raw, &b)
	return reflect.DeepEqual(a, b)
}

func TestWaitSharedVectorsBothReaders(t *testing.T) {
	file := readWaitVector(t, "wait.json")
	for i, item := range waitList(t, file["items"]) {
		wait, err := ReadRunWait(item)
		if err != nil || !sameWaitJSON(t, wait, item) {
			t.Fatalf("item %d: %v", i, err)
		}
	}
	for _, name := range []string{"refused", "readerRefused"} {
		for _, row := range waitList(t, file[name]) {
			if _, err := ReadRunWait(waitMember(t, row, "record")); waitReasonOf(err) != "invalid" {
				t.Errorf("%s: %s accepted (%v)", name, waitMember(t, row, "problem"), err)
			}
		}
	}
}

func TestWaitEvidenceAttentionAndRefusalVectors(t *testing.T) {
	evidence := readWaitVector(t, "evidence.json")
	var digests []string
	_ = json.Unmarshal(evidence["digests"], &digests)
	for i, item := range waitList(t, evidence["items"]) {
		held, err := ReadWakeEvidence(item)
		if err != nil || !sameWaitJSON(t, held, item) {
			t.Fatalf("evidence %d: %v", i, err)
		}
		if digest, _ := WakeEvidenceDigest(held); digest != digests[i] {
			t.Errorf("evidence %d digest %s, want %s", i, digest, digests[i])
		}
	}
	var keys struct{ Digests [][2]string }
	_ = json.Unmarshal(evidence["keys"], &keys)
	for _, pair := range keys.Digests {
		if digest, _ := WakeKeyDigest(pair[0]); digest != pair[1] {
			t.Errorf("key digest %s", digest)
		}
	}
	for _, name := range []string{"refused", "readerRefused"} {
		for _, row := range waitList(t, evidence[name]) {
			if _, err := ReadWakeEvidence(waitMember(t, row, "evidence")); waitReasonOf(err) != "invalid" {
				t.Errorf("evidence %s: %s accepted", name, waitMember(t, row, "problem"))
			}
		}
	}
	waits := waitList(t, readWaitVector(t, "wait.json")["items"])
	attention := readWaitVector(t, "attention.json")
	items := waitList(t, attention["items"])
	if len(items) != len(waits) {
		t.Fatal("one attention event per wait")
	}
	for i, item := range items {
		event, err := ReadAttentionEvent(item)
		if err != nil || !sameWaitJSON(t, event, item) {
			t.Fatalf("attention %d: %v", i, err)
		}
		wait, _ := ReadRunWait(waits[i])
		if !sameWaitJSON(t, AttentionFor(wait), item) {
			t.Errorf("attention %d is not the event its park records", i)
		}
	}
	for _, item := range waitList(t, attention["deliveries"]) {
		if delivery, err := ReadAttentionDelivery(item); err != nil || !sameWaitJSON(t, delivery, item) {
			t.Errorf("delivery: %v", err)
		}
	}
	for _, name := range []string{"refused", "readerRefused"} {
		for _, row := range waitList(t, attention[name]) {
			if _, err := ReadAttentionEvent(waitMember(t, row, "event")); waitReasonOf(err) != "invalid" {
				t.Errorf("attention %s: %s accepted", name, waitMember(t, row, "problem"))
			}
		}
	}
	for _, row := range waitList(t, attention["deliveriesRefused"]) {
		if _, err := ReadAttentionDelivery(waitMember(t, row, "delivery")); waitReasonOf(err) != "invalid" {
			t.Errorf("delivery %s accepted", waitMember(t, row, "problem"))
		}
	}
	refusals := readWaitVector(t, "refusal.json")
	var reasons []string
	for _, item := range waitList(t, refusals["items"]) {
		record, err := ReadWaitRefusal(item)
		if err != nil || !sameWaitJSON(t, (&WaitRefused{Reason: record.Reason}).Record(), item) {
			t.Fatal(err)
		}
		reasons = append(reasons, record.Reason)
	}
	if !reflect.DeepEqual(reasons, WaitRefusalReasons) {
		t.Errorf("refusal reasons %v", reasons)
	}
	for _, item := range waitList(t, refusals["refused"]) {
		if _, err := ReadWaitRefusal(item); err == nil {
			t.Errorf("refusal %s accepted", item)
		}
	}
	capability := readWaitVector(t, "capability.json")
	reasons = nil
	for _, item := range waitList(t, capability["items"]) {
		record, err := ReadWaitCapabilityRefusal(item)
		if err != nil || !sameWaitJSON(t, (&WaitCapabilityRefused{Reason: record.Reason}).Record(), item) {
			t.Fatal(err)
		}
		reasons = append(reasons, record.Reason)
	}
	if !reflect.DeepEqual(reasons, WaitCapabilityReasons) {
		t.Errorf("capability reasons %v", reasons)
	}
	for _, item := range waitList(t, capability["refused"]) {
		if _, err := ReadWaitCapabilityRefusal(item); err == nil {
			t.Errorf("capability %s accepted", item)
		}
	}
	if WaitAttentionID("run-approval", "wait-1") != func() string { w, _ := ReadRunWait(waits[0]); return w.Attention }() {
		t.Error("attention id")
	}
}

func TestWaitReportVectorsBothLanguages(t *testing.T) {
	file := readWaitVector(t, "report.json")
	exports, reports := waitList(t, file["exports"]), waitList(t, file["items"])
	for i, item := range exports {
		if source, err := ReadWaitExport(item); err != nil || !sameWaitJSON(t, source, item) {
			t.Fatalf("export %d: %v", i, err)
		}
	}
	for i, item := range reports {
		if held, err := ReadWaitReport(item); err != nil || !sameWaitJSON(t, held, item) {
			t.Fatalf("report %d: %v", i, err)
		}
	}
	var derivations []struct {
		Export int    `json:"export"`
		Now    string `json:"now"`
		Report int    `json:"report"`
	}
	if err := json.Unmarshal(file["derivations"], &derivations); err != nil || len(derivations) != len(reports) {
		t.Fatal("one derivation per report")
	}
	for i, d := range derivations {
		source, _ := ReadWaitExport(exports[d.Export])
		at, _ := time.Parse(time.RFC3339Nano, d.Now)
		report, err := ReportFromExport(source, at)
		if err != nil || d.Report != i || !sameWaitJSON(t, report, reports[d.Report]) {
			got, _ := json.Marshal(report)
			t.Fatalf("derivation %d gave %s (%v)", i, got, err)
		}
	}
	for _, row := range waitList(t, file["refused"]) {
		if _, err := ReadWaitReport(waitMember(t, row, "report")); waitReasonOf(err) != "invalid" {
			t.Errorf("report %s accepted", waitMember(t, row, "problem"))
		}
	}
	for _, row := range waitList(t, file["readerRefused"]) {
		var err error
		if exported := waitMember(t, row, "export"); exported != nil {
			_, err = ReadWaitExport(exported)
		} else {
			_, err = ReadWaitReport(waitMember(t, row, "report"))
		}
		if waitReasonOf(err) != "invalid" {
			t.Errorf("%s accepted", waitMember(t, row, "problem"))
		}
	}
}

// durableWaitStore stands for a host store kind whose leases and waits outlive its process.
type durableWaitStore struct{ *MemoryRunStore }

func (durableWaitStore) LeaseLifetime() string { return LeaseLifetimeDurable }
func (durableWaitStore) WaitLifetime() string  { return WaitLifetimeDurable }

const waitHour = 60 * leaseMinute
const waitKey = "host-wake-key-for-tests-0001"
const waitOtherKey = "host-wake-key-for-other-run-02"

func waitTime(clock *leaseClock, offset int64) string {
	at, _ := leaseExpiryAt(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli() + offset)
	return at
}
func waitKeyDigest(t *testing.T, key string) string {
	t.Helper()
	digest, err := WakeKeyDigest(key)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

// pausedCheckpoint is the handwritten paused approval of contract/vectors/approval/pause.json (a
// published version 1 checkpoint), as its canonical bytes.
func pausedCheckpoint(t *testing.T, edit func(map[string]any)) []byte {
	t.Helper()
	raw, err := os.ReadFile("../../contract/vectors/approval/pause.json")
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	checkpoint := file.Items[1]["checkpoint"].(map[string]any)
	if edit != nil {
		edit(checkpoint)
	}
	out, err := canonicalRaw(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func parkedApprovalStore(t *testing.T, timeout int64) (*leaseClock, durableWaitStore, LeaseFence, RunWait) {
	t.Helper()
	ctx := context.Background()
	clock := newLeaseClock()
	store := durableWaitStore{NewMemoryRunStore(MemoryRunStoreOptions{Now: clock.now})}
	lease := leaseAcquire(t, store.MemoryRunStore, "run-approval", "worker-a")
	if _, err := store.CommitFenced(ctx, RunCommit{RunID: "run-approval", CommitID: "pause", Checkpoint: pausedCheckpoint(t, nil)}, FenceOf(lease)); err != nil {
		t.Fatal(err)
	}
	wakeAt := waitTime(clock, waitHour)
	input := ParkInput{Store: store, Fence: FenceOf(lease), Wait: "wait-1", Continuation: "continue-1", Condition: WaitCondition{Event: &contract.RunWaitEventCondition{Kind: "approval-decision", Subject: "call_2_1"}, WakeAt: &wakeAt}, WakeKeyDigest: waitKeyDigest(t, waitKey), Release: []WaitRelease{}}
	if timeout > 0 {
		at := waitTime(clock, timeout)
		input.TimeoutAt = &at
	}
	wait, attention, err := ParkRun(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(attention, AttentionFor(wait)) || attention.Needs != "approval-decision" || *attention.Subject != "call_2_1" {
		t.Fatalf("attention %+v", attention)
	}
	return clock, store, FenceOf(lease), wait
}
func decisionEvidence() WakeEvidence {
	return WakeEvidence{Version: 1, Resource: "run-approval", Wait: "wait-1", Evidence: "decision-1", Kind: "event", Event: &contract.RunWaitEventCondition{Kind: "approval-decision", Subject: "call_2_1"}, Key: waitKey}
}

func TestWaitParkReleasesTheLeaseAndLeavesTheRunBytes(t *testing.T) {
	ctx := context.Background()
	_, store, fence, wait := parkedApprovalStore(t, 0)
	if wait.State != "parked" || wait.Pending.Approval.CallID != "call_2_1" || wait.Parked.Owner != "worker-a" || wait.Parked.Epoch != "1" {
		t.Fatalf("wait %+v", wait)
	}
	lease, _ := store.ReadLease(ctx, "run-approval")
	if lease.State != "released" {
		t.Fatalf("lease %+v", lease)
	}
	head, _ := store.Load(ctx, "run-approval")
	if string(head.Checkpoint) != string(pausedCheckpoint(t, nil)) || head.Revision != wait.Checkpoint.Revision {
		t.Fatal("the park changed the run's checkpoint")
	}
	if _, err := store.CommitFenced(ctx, RunCommit{RunID: "run-approval", ExpectedRevision: head.Revision, CommitID: "late", Checkpoint: head.Checkpoint}, fence); leaseReason(err) != "released" {
		t.Fatalf("late commit: %v", err)
	}
	// A repeat of the same park returns it; a different park under the same id is a conflict.
	again := ParkInput{Store: store, Fence: fence, Wait: "wait-1", Continuation: "continue-1", Condition: wait.Condition, WakeKeyDigest: wait.WakeKeyDigest, Release: []WaitRelease{}}
	if held, _, err := ParkRun(ctx, again); err != nil || !reflect.DeepEqual(held, wait) {
		t.Fatalf("repeat park: %v", err)
	}
	again.Continuation = "continue-other"
	if _, _, err := ParkRun(ctx, again); waitReasonOf(err) != "conflict" {
		t.Fatalf("conflicting park: %v", err)
	}
	// The parking driver's released fence parks nothing more.
	if _, _, err := ParkRun(ctx, ParkInput{Store: store, Fence: fence, Wait: "wait-2", Continuation: "k", Condition: wait.Condition, WakeKeyDigest: wait.WakeKeyDigest}); leaseReason(err) != "released" {
		t.Fatalf("released fence: %v", err)
	}
	b, err := store.AcquireLease(ctx, LeaseAcquire{Resource: "run-approval", Owner: "worker-b", ExpectedRevision: head.Revision, DurationMs: waitHour})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ParkRun(ctx, ParkInput{Store: store, Fence: FenceOf(b), Wait: "wait-2", Continuation: "k", Condition: wait.Condition, WakeKeyDigest: wait.WakeKeyDigest}); waitReasonOf(err) != "wait-pending" {
		t.Fatalf("second park: %v", err)
	}
}

func TestWaitTheStoreRefusesAParkWhoseHeadMoved(t *testing.T) {
	ctx := context.Background()
	clock := newLeaseClock()
	store := durableWaitStore{NewMemoryRunStore(MemoryRunStoreOptions{Now: clock.now})}
	lease := leaseAcquire(t, store.MemoryRunStore, "run-moved", "worker-a")
	checkpoint := pausedCheckpoint(t, nil)
	receipt, err := store.CommitFenced(ctx, RunCommit{RunID: "run-moved", CommitID: "pause", Checkpoint: checkpoint}, FenceOf(lease))
	if err != nil {
		t.Fatal(err)
	}
	wakeAt := waitTime(clock, 0)
	park := RunWait{Version: 1, Resource: "run-moved", Wait: "w", Reason: "approval", Pending: contract.RunWaitPending{Approval: &contract.RunWaitPendingApproval{Kind: "approval", CallID: "call_2_1", Name: "tracker__create_issue", Tool: "create_issue"}}, Condition: WaitCondition{WakeAt: &wakeAt}, Continuation: "k", Checkpoint: contract.RunWaitCheckpointBinding{Revision: receipt.Revision, Digest: CheckpointBytesDigest(checkpoint)}, WakeKeyDigest: waitKeyDigest(t, waitKey), Release: []WaitRelease{}}
	if _, err := store.CommitFenced(ctx, RunCommit{RunID: "run-moved", ExpectedRevision: receipt.Revision, CommitID: "moved", Events: [][]byte{[]byte(`{"version":1}`)}}, FenceOf(lease)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.ParkWait(ctx, park, FenceOf(lease)); waitReasonOf(err) != "checkpoint-changed" {
		t.Fatalf("moved head: %v", err)
	}
	head, _ := store.Load(ctx, "run-moved")
	park.Checkpoint.Revision = head.Revision
	forged := FenceOf(lease)
	forged.Owner = "worker-x"
	if _, _, err := store.ParkWait(ctx, park, forged); leaseReason(err) != "other-owner" {
		t.Fatalf("forged fence: %v", err)
	}
	if held, _ := store.ReadLease(ctx, "run-moved"); held.State != "held" {
		t.Fatal("a refused park released the lease")
	}
	if held, _ := store.ReadWait(ctx, "run-moved", "w"); held != nil {
		t.Fatal("a refused park left a wait")
	}
}

func TestWaitEventOrTimeOneAtomicWinner(t *testing.T) {
	ctx := context.Background()
	for _, order := range []string{"event-first", "time-first"} {
		t.Run(order, func(t *testing.T) {
			clock, store, _, _ := parkedApprovalStore(t, 0)
			clock.advance(waitHour)
			wakes := []WakeEvidence{decisionEvidence(), {Version: 1, Resource: "run-approval", Wait: "wait-1", Evidence: "tick-1", Kind: "time", Key: waitKey}}
			if order == "time-first" {
				wakes[0], wakes[1] = wakes[1], wakes[0]
			}
			results := make([]error, 2)
			winners := make([]string, 2)
			var wg sync.WaitGroup
			for i, wake := range wakes {
				wg.Add(1)
				go func() {
					defer wg.Done()
					d, err := WakeRun(ctx, store, wake)
					results[i], winners[i] = err, d.Evidence
				}()
			}
			wg.Wait()
			won, stale := 0, 0
			for i, err := range results {
				switch {
				case err == nil:
					won++
					held, _ := store.ReadWait(ctx, "run-approval", "wait-1")
					if held.Disposition.Evidence != winners[i] {
						t.Errorf("retained %s, won %s", held.Disposition.Evidence, winners[i])
					}
				case waitReasonOf(err) == "stale":
					stale++
				default:
					t.Errorf("unexpected %v", err)
				}
			}
			if won != 1 || stale != 1 {
				t.Fatalf("won %d stale %d", won, stale)
			}
		})
	}
}

func TestWaitDuplicateStaleConflictingAndCrossRunWakesChangeNothing(t *testing.T) {
	ctx := context.Background()
	clock, store, _, _ := parkedApprovalStore(t, 3*waitHour)
	wake := decisionEvidence()
	mismatched := wake
	mismatched.Event = &contract.RunWaitEventCondition{Kind: "approval-decision", Subject: "call-9"}
	if _, err := WakeRun(ctx, store, mismatched); waitReasonOf(err) != "mismatch" {
		t.Fatalf("mismatch: %v", err)
	}
	answered := wake
	answer, actor := contract.QuestionDraft{Selected: []string{}, Text: "yes"}, "person"
	answered.Answer, answered.Actor = &answer, &actor
	if _, err := WakeRun(ctx, store, answered); waitReasonOf(err) != "mismatch" {
		t.Fatalf("an answer is no approval: %v", err)
	}
	if _, err := WakeRun(ctx, store, WakeEvidence{Version: 1, Resource: "run-approval", Wait: "wait-1", Evidence: "tick-0", Kind: "time", Key: waitKey}); waitReasonOf(err) != "not-due" {
		t.Fatalf("not due: %v", err)
	}
	first, err := WakeRun(ctx, store, wake)
	if err != nil {
		t.Fatal(err)
	}
	clock.advance(leaseMinute)
	if again, err := WakeRun(ctx, store, wake); err != nil || again != first {
		t.Fatalf("duplicate: %v", err)
	}
	held, _ := store.ReadWait(ctx, "run-approval", "wait-1")
	if _, err := WakeRun(ctx, store, mismatched); waitReasonOf(err) != "conflict" {
		t.Fatalf("conflicting duplicate: %v", err)
	}
	other := wake
	other.Evidence = "decision-2"
	if _, err := WakeRun(ctx, store, other); waitReasonOf(err) != "stale" {
		t.Fatalf("stale: %v", err)
	}
	foreign := other
	foreign.Key = waitOtherKey
	if _, err := WakeRun(ctx, store, foreign); waitReasonOf(err) != "unauthenticated" {
		t.Fatalf("another run's key: %v", err)
	}
	foreign = wake
	foreign.Resource = "run-other"
	if _, err := WakeRun(ctx, store, foreign); waitReasonOf(err) != "no-wait" {
		t.Fatalf("another run: %v", err)
	}
	after, _ := store.ReadWait(ctx, "run-approval", "wait-1")
	if !reflect.DeepEqual(held, after) {
		t.Fatal("a refused wake changed the wait")
	}
}

func TestWaitResumeNeedsTheDecisionAndANewLease(t *testing.T) {
	ctx := context.Background()
	clock, store, oldFence, wait := parkedApprovalStore(t, 0)
	clock.advance(leaseMinute)
	if _, err := WakeRun(ctx, store, decisionEvidence()); err != nil {
		t.Fatal(err)
	}
	input := ResumeInput{Store: store, Resource: "run-approval", Wait: "wait-1", Evidence: "decision-1", Owner: "worker-b", DurationMs: waitHour}
	// A wake is not the approval, a grant is not one either, and a decision for another call is none.
	for _, bad := range []ResumeInput{input, func() ResumeInput {
		i := input
		i.Approval = &contract.CheckpointApprovalDecision{CallID: "another", Approved: true}
		return i
	}(), func() ResumeInput {
		i := input
		i.Grant = &contract.CheckpointGrant{From: 20, To: 30, At: waitTime(clock, 0)}
		return i
	}()} {
		if _, err := ResumeParked(ctx, bad); waitReasonOf(err) != "decision-required" {
			t.Fatalf("decision: %v", err)
		}
	}
	unseen := input
	unseen.Evidence = "decision-unseen"
	unseen.Approval = &contract.CheckpointApprovalDecision{CallID: "call_2_1", Approved: true}
	if _, err := ResumeParked(ctx, unseen); waitReasonOf(err) != "unacknowledged" {
		t.Fatalf("unacknowledged: %v", err)
	}
	if lease, _ := store.ReadLease(ctx, "run-approval"); lease.State != "released" || lease.Epoch != "1" {
		t.Fatalf("a refused resume took a lease: %+v", lease)
	}
	input.Approval = &contract.CheckpointApprovalDecision{CallID: "call_2_1", Approved: true}
	resumed, err := ResumeParked(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Lease.Epoch != "2" || resumed.Wait.State != "resumed" || resumed.Wait.Resumed.Owner != "worker-b" || resumed.Approval.CallID != "call_2_1" {
		t.Fatalf("resumed %+v", resumed)
	}
	if _, err := ResumeParked(ctx, input); waitReasonOf(err) != "already-resumed" {
		t.Fatalf("second resume: %v", err)
	}
	if _, err := store.ResumeWait(ctx, "run-approval", "wait-1", "decision-1", resumed.Fence, nil); waitReasonOf(err) != "already-resumed" {
		t.Fatalf("the store claims twice: %v", err)
	}
	if _, err := store.CommitFenced(ctx, RunCommit{RunID: "run-approval", ExpectedRevision: wait.Checkpoint.Revision, CommitID: "stale", Checkpoint: pausedCheckpoint(t, nil)}, oldFence); leaseReason(err) != "stale-epoch" {
		t.Fatalf("stale fence: %v", err)
	}
	report, err := WaitReportOf(ctx, store, "run-approval", time.Time{})
	if err != nil || len(report.Entries) != 3 || report.Entries[0].Decision != "park" || report.Entries[1].Decision != "wake" || report.Entries[2].Decision != "resume" || *report.Entries[2].Epoch != "2" {
		t.Fatalf("report %+v %v", report, err)
	}
}

func TestWaitTimeoutAndMovedRunRefusals(t *testing.T) {
	ctx := context.Background()
	clock, store, _, wait := parkedApprovalStore(t, 3*waitHour)
	clock.advance(3*waitHour - 1)
	timeout := WakeEvidence{Version: 1, Resource: "run-approval", Wait: "wait-1", Evidence: "deadline-1", Kind: "timeout", Key: waitKey}
	if _, err := WakeRun(ctx, store, timeout); waitReasonOf(err) != "not-due" {
		t.Fatalf("not due: %v", err)
	}
	clock.advance(1)
	if d, err := WakeRun(ctx, store, timeout); err != nil || d.Kind != "timeout" {
		t.Fatalf("equal time is due: %v", err)
	}
	// A timed-out approval is never approved by its timeout.
	if _, err := ResumeParked(ctx, ResumeInput{Store: store, Resource: "run-approval", Wait: "wait-1", Evidence: "deadline-1", Owner: "worker-b", DurationMs: waitHour, Approval: &contract.CheckpointApprovalDecision{CallID: "call_2_1", Approved: true}}); waitReasonOf(err) != "timed-out" {
		t.Fatalf("timed out: %v", err)
	}
	b, err := store.AcquireLease(ctx, LeaseAcquire{Resource: "run-approval", Owner: "worker-b", ExpectedRevision: wait.Checkpoint.Revision, DurationMs: waitHour})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ResumeWait(ctx, "run-approval", "wait-1", "deadline-1", FenceOf(b), nil); waitReasonOf(err) != "timed-out" {
		t.Fatalf("the store claims a timed-out approval: %v", err)
	}
	_, _ = store.ReleaseLease(ctx, FenceOf(b))
	clock2, moved, _, parked := parkedApprovalStore(t, 0)
	clock2.advance(leaseMinute)
	if _, err := WakeRun(ctx, moved, decisionEvidence()); err != nil {
		t.Fatal(err)
	}
	if _, err := moved.Commit(ctx, RunCommit{RunID: "run-approval", ExpectedRevision: parked.Checkpoint.Revision, CommitID: "outside", Events: [][]byte{[]byte(`{"version":1}`)}}); err != nil {
		t.Fatal(err)
	}
	if _, err := ResumeParked(ctx, ResumeInput{Store: moved, Resource: "run-approval", Wait: "wait-1", Evidence: "decision-1", Owner: "worker-b", DurationMs: waitHour, Approval: &contract.CheckpointApprovalDecision{CallID: "call_2_1", Approved: true}}); leaseReason(err) != "revision-changed" {
		t.Fatalf("moved run: %v", err)
	}
	_ = wait
}

func TestWaitStoreRevalidatesTheHeadAndTheFenceAtTheClaim(t *testing.T) {
	ctx := context.Background()
	clock, store, _, wait := parkedApprovalStore(t, 0)
	clock.advance(leaseMinute)
	if _, err := WakeRun(ctx, store, decisionEvidence()); err != nil {
		t.Fatal(err)
	}
	b, err := store.AcquireLease(ctx, LeaseAcquire{Resource: "run-approval", Owner: "worker-b", ExpectedRevision: wait.Checkpoint.Revision, DurationMs: waitHour})
	if err != nil {
		t.Fatal(err)
	}
	forged := FenceOf(b)
	forged.Owner = "worker-x"
	if _, err := store.ResumeWait(ctx, "run-approval", "wait-1", "decision-unseen", FenceOf(b), nil); waitReasonOf(err) != "unacknowledged" {
		t.Fatalf("unacknowledged claim: %v", err)
	}
	if _, err := store.ResumeWait(ctx, "run-approval", "wait-1", "decision-1", forged, nil); leaseReason(err) != "other-owner" {
		t.Fatalf("forged fence: %v", err)
	}
	if _, err := store.CommitFenced(ctx, RunCommit{RunID: "run-approval", ExpectedRevision: wait.Checkpoint.Revision, CommitID: "moved", Events: [][]byte{[]byte(`{"version":1}`)}}, FenceOf(b)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ResumeWait(ctx, "run-approval", "wait-1", "decision-1", FenceOf(b), nil); waitReasonOf(err) != "checkpoint-changed" {
		t.Fatalf("moved head: %v", err)
	}
	if held, _ := store.ReadWait(ctx, "run-approval", "wait-1"); held.State != "woken" {
		t.Fatalf("state %s", held.State)
	}
}

// questionStore commits a completed run whose stored log ends with a user_question envelope.
func questionStore(t *testing.T) (*leaseClock, durableWaitStore, LeaseFence, contract.Question) {
	t.Helper()
	ctx := context.Background()
	clock := newLeaseClock()
	store := durableWaitStore{NewMemoryRunStore(MemoryRunStoreOptions{Now: clock.now})}
	lease := leaseAcquire(t, store.MemoryRunStore, "run-question", "worker-a")
	question := contract.Question{Title: "Buyer", Question: "Who should we focus on as the first buyer?", Selection: "single", Options: []contract.QuestionOption{{ID: "operations", Label: "Head of operations", Description: "Owns recurring exception approvals."}, {ID: "risk", Label: "Risk or compliance lead", Description: "Owns oversight."}}}
	question.RecommendedOptionID = contract.Nullable[string]{Set: true, Value: "operations"}
	question.Recommendation = contract.Nullable[string]{Set: true, Value: "A starting hypothesis."}
	envelope := func(id string, q contract.Question) []byte {
		raw, err := json.Marshal(map[string]any{"version": 1, "scope": map[string]any{"execution": "run-question", "role": "author"}, "event": map[string]any{"type": "user_question", "id": id, "question": q}})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	earlier := question
	earlier.Title = "Earlier"
	checkpoint := pausedCheckpoint(t, func(c map[string]any) { c["status"] = "complete"; delete(c, "approval") })
	if _, err := store.CommitFenced(ctx, RunCommit{RunID: "run-question", CommitID: "asked", Checkpoint: checkpoint, Events: [][]byte{envelope("question-0", earlier), envelope("question-1", question)}}, FenceOf(lease)); err != nil {
		t.Fatal(err)
	}
	return clock, store, FenceOf(lease), question
}

func TestWaitQuestionRetainsTheQuestionAndRecordsARealAnswerOrUnanswered(t *testing.T) {
	ctx := context.Background()
	for _, ending := range []string{"answer", "timeout"} {
		t.Run(ending, func(t *testing.T) {
			clock, store, fence, question := questionStore(t)
			timeout := waitTime(clock, 2*waitHour)
			input := ParkInput{Store: store, Fence: fence, Wait: "wait-q", Continuation: "continue-q", Condition: WaitCondition{Event: &contract.RunWaitEventCondition{Kind: "question-answer", Subject: "question-1"}}, TimeoutAt: &timeout, WakeKeyDigest: waitKeyDigest(t, waitKey), Release: []WaitRelease{{Resource: "provider-account", Outcome: "uncertain"}}, Question: "question-0"}
			// Only the latest question is pending.
			if _, _, err := ParkRun(ctx, input); waitReasonOf(err) != "nothing-pending" {
				t.Fatalf("earlier question: %v", err)
			}
			input.Question = "question-1"
			wait, attention, err := ParkRun(ctx, input)
			if err != nil {
				t.Fatal(err)
			}
			if wait.Pending.Question == nil || !reflect.DeepEqual(*wait.Pending.Question.Question, question) || wait.Pending.Question.QuestionDigest != WaitQuestionDigest(question) || !attention.ReleaseUncertain || attention.Needs != "question-answer" {
				t.Fatalf("wait %+v attention %+v", wait, attention)
			}
			evidence := WakeEvidence{Version: 1, Resource: "run-question", Wait: "wait-q", Evidence: "answer-1", Kind: "event", Event: &contract.RunWaitEventCondition{Kind: "question-answer", Subject: "question-1"}, Key: waitKey}
			actor := "person-reviewer"
			for _, bad := range []contract.QuestionDraft{{Selected: []string{}, Text: "   "}, {Selected: []string{"it"}, Text: ""}, {Selected: []string{"risk", "operations"}, Text: ""}} {
				e := evidence
				e.Answer, e.Actor = &bad, &actor
				if _, err := WakeRun(ctx, store, e); waitReasonOf(err) != "mismatch" {
					t.Fatalf("not an answer %+v: %v", bad, err)
				}
			}
			if _, err := WakeRun(ctx, store, evidence); waitReasonOf(err) != "mismatch" {
				t.Fatalf("no answer: %v", err)
			}
			var evidenceID string
			if ending == "answer" {
				clock.advance(leaseMinute)
				answer := contract.QuestionDraft{Selected: []string{"risk"}, Text: ""}
				evidence.Answer, evidence.Actor = &answer, &actor
				evidenceID = "answer-1"
				if _, err := WakeRun(ctx, store, evidence); err != nil {
					t.Fatal(err)
				}
			} else {
				clock.advance(2 * waitHour)
				evidenceID = "deadline-1"
				if _, err := WakeRun(ctx, store, WakeEvidence{Version: 1, Resource: "run-question", Wait: "wait-q", Evidence: evidenceID, Kind: "timeout", Key: waitKey}); err != nil {
					t.Fatal(err)
				}
			}
			held, _ := store.ReadWait(ctx, "run-question", "wait-q")
			if ending == "answer" && (held.Resolution.Kind != "answered" || *held.Resolution.Actor != actor || held.Resolution.At != waitTime(clock, leaseMinute) || held.Resolution.Answer.Selected[0] != "risk") {
				t.Fatalf("answered %+v", held.Resolution)
			}
			if ending == "timeout" {
				raw, _ := json.Marshal(held.Resolution)
				if held.State != "timed-out" || held.Resolution.Kind != "unanswered" || strings.Contains(string(raw), "operations") {
					t.Fatalf("unanswered %s", raw)
				}
			}
			resume := ResumeInput{Store: store, Resource: "run-question", Wait: "wait-q", Evidence: evidenceID, Owner: "worker-b", DurationMs: waitHour}
			if _, err := ResumeParked(ctx, resume); waitReasonOf(err) != "release-uncertain" {
				t.Fatalf("uncertain release: %v", err)
			}
			resume.Reconciled = []WaitRelease{{Resource: "provider-account", Outcome: "released"}}
			resumed, err := ResumeParked(ctx, resume)
			if err != nil || !reflect.DeepEqual(resumed.Resolution, held.Resolution) {
				t.Fatalf("resume: %v", err)
			}
			report, err := WaitReportOf(ctx, store, "run-question", time.Time{})
			if err != nil {
				t.Fatal(err)
			}
			decisions := []string{}
			for _, entry := range report.Entries {
				decisions = append(decisions, entry.Decision)
			}
			want := []string{"park", "wake", "answer", "resume"}
			if ending == "timeout" {
				want = []string{"park", "timeout", "unanswered", "resume"}
			}
			if !reflect.DeepEqual(decisions, want) || *report.Entries[0].Release != "uncertain" {
				t.Fatalf("decisions %v", decisions)
			}
		})
	}
}

func TestWaitAttentionDeliveryAndTheReportSources(t *testing.T) {
	ctx := context.Background()
	clock, store, fence, _ := questionStore(t)
	retain := waitTime(clock, 10*waitHour)
	wait, attention, err := ParkRun(ctx, ParkInput{Store: store, Fence: fence, Wait: "wait-q", Continuation: "continue-q", Condition: WaitCondition{Event: &contract.RunWaitEventCondition{Kind: "question-answer", Subject: "question-1"}}, WakeKeyDigest: waitKeyDigest(t, waitKey), Release: []WaitRelease{}, Question: "question-1", RetainUntil: &retain})
	if err != nil {
		t.Fatal(err)
	}
	failed, err := store.RecordDelivery(ctx, attention.ID, "mail-1", "failed")
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := store.RecordDelivery(ctx, attention.ID, "mail-1", "failed"); again != failed {
		t.Fatal("a repeated delivery is the first record")
	}
	if _, err := store.RecordDelivery(ctx, attention.ID, "mail-1", "sent"); waitReasonOf(err) != "conflict" {
		t.Fatalf("delivery conflict: %v", err)
	}
	if _, err := store.RecordDelivery(ctx, WaitAttentionID("run-question", "wait-9"), "mail-2", "sent"); waitReasonOf(err) != "no-wait" {
		t.Fatalf("unknown attention: %v", err)
	}
	exported, err := ExportWaits(ctx, store, "run-question")
	if err != nil || len(exported.Attention) != 1 || len(exported.Deliveries) != 1 || exported.Waits[0].State != "parked" || !reflect.DeepEqual(exported.Waits[0], wait) {
		t.Fatalf("export %+v %v", exported, err)
	}
	clock.advance(leaseMinute)
	answer, actor := contract.QuestionDraft{Selected: []string{"risk"}, Text: "Closer to the team."}, "person-lead"
	if _, err := WakeRun(ctx, store, WakeEvidence{Version: 1, Resource: "run-question", Wait: "wait-q", Evidence: "answer-1", Kind: "event", Event: &contract.RunWaitEventCondition{Kind: "question-answer", Subject: "question-1"}, Answer: &answer, Actor: &actor, Key: waitKey}); err != nil {
		t.Fatal(err)
	}
	resumed, err := ResumeParked(ctx, ResumeInput{Store: store, Resource: "run-question", Wait: "wait-q", Evidence: "answer-1", Owner: "worker-b", DurationMs: waitHour})
	if err != nil {
		t.Fatal(err)
	}
	at := time.UnixMilli(waitTimeMillis(waitTime(clock, waitHour))).UTC()
	report, err := WaitReportOf(ctx, store, "run-question", at)
	if err != nil {
		t.Fatal(err)
	}
	// The run goes on and its retained context is compacted: the report reads the waits, not the prompt.
	head, _ := store.Load(ctx, "run-question")
	compacted := pausedCheckpoint(t, func(c map[string]any) {
		c["status"] = "complete"
		delete(c, "approval")
		c["context"] = map[string]any{"scope": "run-8", "entries": []any{}}
	})
	if _, err := store.CommitFenced(ctx, RunCommit{RunID: "run-question", ExpectedRevision: head.Revision, CommitID: "compacted", Checkpoint: compacted}, resumed.Fence); err != nil {
		t.Fatal(err)
	}
	if again, _ := WaitReportOf(ctx, store, "run-question", at); !reflect.DeepEqual(again, report) {
		t.Fatal("the report changed with the run's checkpoint")
	}
	source, _ := ExportWaits(ctx, store, "run-question")
	text, _ := json.Marshal(source)
	read, err := ReadWaitExport(text)
	if err != nil {
		t.Fatal(err)
	}
	if roundTrip, _ := ReportFromExport(read, at); !reflect.DeepEqual(roundTrip, report) {
		t.Fatal("a store round trip changed the report")
	}
	reportText, _ := json.Marshal(report)
	for _, word := range []string{waitKey, "wakeKeyDigest", "fence", "approved", "grant"} {
		if strings.Contains(string(reportText), word) || word == waitKey && strings.Contains(string(text), word) {
			t.Fatalf("the report carries %s", word)
		}
	}
	expired, _ := WaitReportOf(ctx, store, "run-question", time.UnixMilli(waitTimeMillis(retain)).UTC())
	if expired.Entries[0].Content.State != "expired" || expired.Entries[2].Content.State != "expired" || expired.Entries[0].Content.Digest != report.Entries[0].Content.Digest {
		t.Fatalf("expired %+v", expired.Entries[0].Content)
	}
	redacted, err := store.RedactWait(ctx, "run-question", "wait-q")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(redacted)
	if strings.Contains(string(raw), "Closer to the team") || strings.Contains(string(raw), "first buyer") || redacted.Redacted == nil {
		t.Fatalf("redacted %s", raw)
	}
	after, err := WaitReportOf(ctx, store, "run-question", at)
	if err != nil || after.Entries[0].Content.State != "redacted" || after.Entries[2].Content.State != "redacted" || after.Entries[2].Content.Digest != report.Entries[2].Content.Digest {
		t.Fatalf("redacted report %+v %v", after, err)
	}
}

func TestWaitCapabilityRefusalsByName(t *testing.T) {
	ctx := context.Background()
	memory := NewMemoryRunStore(MemoryRunStoreOptions{})
	callback := NewCheckpointCallbackRunStore(func(context.Context, []byte) error { return nil }, func(raw []byte) ([]byte, error) { return raw, nil })
	type plain struct{ RunStore }
	for store, want := range map[RunStore]string{memory: "capability:store-lifetime", callback: "capability:store-unsupported", plain{memory}: "capability:store-unsupported", durableLeaseFixture{memory}: "capability:store-lifetime"} {
		if got := waitReasonOf(AdmitWaits(store)); got != want {
			t.Errorf("admit %T: %s, want %s", store, got, want)
		}
		if _, _, err := ParkRun(ctx, ParkInput{Store: store, Fence: LeaseFence{Version: 1, Resource: "r", Owner: "o", Epoch: "1"}, Wait: "w", Continuation: "k", WakeKeyDigest: waitKeyDigest(t, waitKey)}); waitReasonOf(err) != want {
			t.Errorf("park %T: %v", store, err)
		}
	}
	if err := AdmitWaits(durableWaitStore{memory}); err != nil {
		t.Fatal(err)
	}
	if _, err := ExportWaits(ctx, plain{memory}, "r"); waitReasonOf(err) != "capability:store-unsupported" {
		t.Fatal(err)
	}
}

func TestWaitOffByteParityWithNothingConfigured(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryRunStore(MemoryRunStoreOptions{})
	lease := leaseAcquire(t, store, "parity", "worker-a")
	if _, err := store.CommitFenced(ctx, RunCommit{RunID: "parity", CommitID: "pause", Checkpoint: pausedCheckpoint(t, nil)}, FenceOf(lease)); err != nil {
		t.Fatal(err)
	}
	head, _ := store.Load(ctx, "parity")
	if string(head.Checkpoint) != string(pausedCheckpoint(t, nil)) {
		t.Fatal("bytes changed")
	}
	for _, word := range []string{"needs_attention", "wakeKeyDigest", "\"wait\""} {
		if strings.Contains(string(head.Checkpoint), word) {
			t.Fatalf("the checkpoint carries %s", word)
		}
	}
	if exported, _ := store.ExportWaits(ctx, "parity"); len(exported.Waits) != 0 {
		t.Fatal("a store with nothing configured keeps a wait")
	}
}
