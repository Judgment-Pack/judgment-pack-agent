package runcontext

// #265 M6: the wait conformance kit, for a host store kind's wait port beside its lease port (desk #609
// and #670), as #223's kit is for its lease port. Every guarantee of durable waits lives in the store's
// own transitions, and the package re-checks none of them: the kit drives the port directly and names the
// first property a store breaks. The TypeScript twin is runStoreWaitConformance in
// packages/harness/storage/waitConformance.ts.
import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

// WaitStoreFactory builds the store under test. The kit hands it the store clock it moves; with a
// non-nil restart, the factory returns a new instance over the same durable state, as after the store's
// process restarted (a store whose waits end with its process cannot pass). Fixtures are hand authored.
type WaitStoreFactory func(now func() time.Time, restart WaitRunStore) WaitRunStore

// The kit's asked act call, as the writer records it.
const kitApproval = `{"callId":"call-1","name":"tracker__create_issue","tool":"create_issue","source":{"kind":"mcp","id":"tracker","trust":"public","toolListDigest":"sha256:71a65542f0fc25b85f4458bef0b54a2b11d921be4d6ca8cf3c9dc215cd790820","tool":"create_issue"},"args":{"title":"Mileage"},"state":"asked"}`

// kitKeys: one key per wait (#265 M1).
var kitKeys = map[string]string{"a": "kit.wait-a.Q7z2Xm9LpV4cR8tNb3Yh6Df1", "b": "kit.wait-b.Hs8Pq3Lz6Wd1Rm9Tc4Vb7Nx2", "c": "kit.wait-c.Ka5Ue2Jy8Gt3Wo6Bf9Mn1Sv4", "d": "kit.wait-d.Zc3Ri7Oq1Xe5Pk9Ls2Gu6Ty8"}

func kitReason(err error) string {
	var wait *WaitRefused
	var lease *LeaseRefused
	var resume *ResumeRefused
	var reauth *ReauthorizationRefused
	switch {
	case errors.As(err, &wait):
		return wait.Reason
	case errors.As(err, &lease):
		return lease.Reason
	case errors.As(err, &resume):
		return resume.Reason
	case errors.As(err, &reauth):
		return reauth.Reason
	}
	return ""
}

// kitCheckpoint is a checkpoint carrying the approval, with one member changed.
func kitCheckpoint(change func(map[string]any)) []byte {
	var approval map[string]any
	_ = json.Unmarshal([]byte(kitApproval), &approval)
	if change != nil {
		change(approval)
	}
	raw, _ := canonicalRaw(map[string]any{"kit": "wait-conformance", "approval": approval})
	return raw
}

// CheckRunStoreWaitConformance (#265 M6) certifies a host store kind's wait port. Properties: the key
// check (a key belongs to one wait; a wake presents its own wait's key), the single winner, the lease
// release inside a committed park (a refused one changes nothing), claim-once, the run closure (lease and
// unfenced commit, wait-pending then closed), the deadline and approval ceilings (acquire, renew, the
// claim, a later run limit reaching a parked wait, the deadline wake), the commit-time approval binding
// and its settlement (#266 N3), and every one of them across restarts of the store. #226 adds the
// re-authorization properties (reauth.*): the disposition bound to the lease that parks it and to the
// head's never-admitted dispatch (a stale fence, and a head failing each way, refused with nothing changed),
// the claim's refresh (required; the parked principal; permissions after the refresh that cover the operation;
// taken after the park and not after the store's time), the run's unsettled approval binding and expiry the
// store attaches (the claim's lease bounded by it, refused from it, and LimitRun keeping it), the timeout and
// the deadline wake of a reauthorization wait (each terminal), and each across restarts; with LeasePrincipals
// (ADR-0029), the principals a delegated run's wait pins, its claim and a later takeover keep. It returns a
// StorageConformanceFailure naming the first property broken.
func CheckRunStoreWaitConformance(ctx context.Context, create WaitStoreFactory) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			if failed, ok := recovered.(kitFailure); ok {
				err = &StorageConformanceFailure{Property: failed.property}
				return
			}
			err = &StorageConformanceFailure{Property: "wait.operation"}
		}
	}()
	if err := waitConformance(ctx, create); err != nil {
		return err
	}
	return reauthConformance(ctx, create)
}

// kitFailure carries the first broken property out of the kit's checks.
type kitFailure struct{ property string }

func waitConformance(ctx context.Context, create WaitStoreFactory) (err error) {
	check := func(property string, ok bool) {
		if !ok {
			panic(kitFailure{property})
		}
	}
	refuses := func(property string, err error, reason string) {
		check(property, err != nil && kitReason(err) == reason)
	}
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	at := t0
	now := func() time.Time { return at }
	iso := func(offset time.Duration) string { text, _ := leaseExpiryAt(t0.Add(offset).UnixMilli()); return text }
	store := create(now, nil)
	check("wait.declared", store != nil && store.WaitVersion() == WaitRecordVersion && (store.WaitLifetime() == WaitLifetimeDurable || store.WaitLifetime() == WaitLifetimeProcess) && store.LeaseVersion() == LeaseRecordVersion && store.Durability() != RunStoreSingleWriterCallback)
	// Each restart is a new instance; everything after it is decided by what the store kept.
	restart := func(property string) {
		next := create(now, store)
		check(property, next != nil && any(next) != any(store))
		store = next
	}
	asked := kitCheckpoint(nil)
	var approval contract.CheckpointApproval
	_ = json.Unmarshal([]byte(kitApproval), &approval)
	binding, err := ApprovalBindingOf(approval)
	check("wait.fixture", err == nil)
	descriptor := approval.Source.ToolListDigest
	pins := ResumePins{Model: PinnedModel{Provider: "openai", Model: "kit"}, Policy: "sha256:44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a", Sources: []SourcePin{{Source: "tracker", DescriptorDigest: descriptor}}, Artifacts: []ArtifactPin{}}
	pinsDigest, err := PinsDigest(pins)
	check("wait.fixture", err == nil)
	type opened struct {
		revision string
		fence    LeaseFence
	}
	open := func(run string) opened {
		receipt, err := store.Commit(ctx, RunCommit{RunID: run, CommitID: run + "-open", Checkpoint: asked})
		check("wait.open", err == nil)
		lease, err := store.AcquireLease(ctx, LeaseAcquire{Resource: run, Owner: "worker-a", ExpectedRevision: receipt.Revision, DurationMs: int64(time.Hour / time.Millisecond)})
		check("wait.open", err == nil)
		return opened{receipt.Revision, FenceOf(lease)}
	}
	hour := int64(time.Hour / time.Millisecond)
	parkOf := func(run, revision, key string, ttl int64, wakeAt bool) RunWait {
		digest, _ := WakeKeyDigest(key)
		condition := WaitCondition{Event: &contract.RunWaitEventCondition{Kind: "approval-decision", Subject: "call-1"}}
		if wakeAt {
			at := iso(time.Minute)
			condition.WakeAt = &at
		}
		held := binding
		return RunWait{Version: WaitRecordVersion, Resource: run, Wait: "wait-1", Reason: "approval", Pending: contract.RunWaitPending{Approval: &contract.RunWaitPendingApproval{Kind: "approval", CallID: "call-1", Name: approval.Name, Tool: approval.Tool}}, Condition: condition, Limits: &WaitLimits{ApprovalTtlMs: &ttl}, Approval: &held, Continuation: run + "-continue", Checkpoint: contract.RunWaitCheckpointBinding{Revision: revision, Digest: CheckpointBytesDigest(asked)}, Pins: pins, WakeKeyDigest: digest, Release: []WaitRelease{}}
	}
	decision := func(run, key, evidence string) WakeEvidence {
		return WakeEvidence{Version: 1, Resource: run, Wait: "wait-1", Evidence: evidence, Kind: "event", Event: &contract.RunWaitEventCondition{Kind: "approval-decision", Subject: "call-1"}, Key: key}
	}
	deadline := func(run, key string) WakeEvidence {
		return WakeEvidence{Version: 1, Resource: run, Wait: "wait-1", Evidence: "deadline-1", Kind: "deadline", Key: key}
	}
	claim := func(run, evidence, owner, revision string) WaitClaim {
		return WaitClaim{Resource: run, Wait: "wait-1", Evidence: evidence, Lease: LeaseAcquire{Resource: run, Owner: owner, ExpectedRevision: revision, DurationMs: 24 * hour}, Reconciled: []WaitRelease{}, Revalidation: Revalidation{Version: 1, Resource: run, Wait: "wait-1", Pins: pinsDigest, Evidence: "sha256:" + strings.Repeat("e", 64), Changes: []ResumeChange{}}}
	}
	commitWith := func(run, expected, id string, change func(map[string]any), fence *LeaseFence) (RunReceipt, error) {
		input := RunCommit{RunID: run, ExpectedRevision: expected, CommitID: id, Checkpoint: kitCheckpoint(change)}
		if fence != nil {
			return store.CommitFenced(ctx, input, *fence)
		}
		return store.Commit(ctx, input)
	}
	same := func(a, b any) bool { x, _ := canonicalRaw(a); y, _ := canonicalRaw(b); return string(x) == string(y) }
	readWait := func(run string) *RunWait { held, _ := store.ReadWait(ctx, run, "wait-1"); return held }
	readLease := func(run string) *RunLease { held, _ := store.ReadLease(ctx, run); return held }

	// The lease release: a park the store refuses changes nothing and leaves the lease held; a committed
	// one releases it in the same transaction, so the parking driver's fence commits nothing more.
	a, b := open("wait-a"), open("wait-b")
	forged := a.fence
	forged.Owner = "worker-x"
	_, _, err = store.ParkWait(ctx, parkOf("wait-a", a.revision, kitKeys["a"], hour, true), forged)
	refuses("wait.park.fence", err, "other-owner")
	moved := parkOf("wait-a", a.revision, kitKeys["a"], hour, true)
	moved.Checkpoint.Digest = "sha256:" + strings.Repeat("0", 64)
	_, _, err = store.ParkWait(ctx, moved, a.fence)
	refuses("wait.park.head", err, "checkpoint-changed")
	check("wait.park.refused-unchanged", readWait("wait-a") == nil && readLease("wait-a") != nil && readLease("wait-a").State == "held")
	parked, attention, err := store.ParkWait(ctx, parkOf("wait-a", a.revision, kitKeys["a"], hour, true), a.fence)
	check("wait.park.committed", err == nil && parked.State == "parked" && parked.Parked.Owner == "worker-a" && parked.Parked.Epoch == a.fence.Epoch && parked.Parked.At == iso(0) && parked.Deadline != nil && parked.Deadline.Approval != nil && *parked.Deadline.Approval == iso(time.Hour) && attention.ID == parked.Attention && attention.Needs == "approval-decision")
	check("wait.park.lease-released", readLease("wait-a") != nil && readLease("wait-a").State == "released")
	_, err = store.CommitFenced(ctx, RunCommit{RunID: "wait-a", ExpectedRevision: a.revision, CommitID: "late", Checkpoint: asked}, a.fence)
	refuses("wait.park.fence-ends", err, "released")
	again, againAttention, err := store.ParkWait(ctx, parkOf("wait-a", a.revision, kitKeys["a"], hour, true), a.fence)
	check("wait.park.repeat", err == nil && same(again, parked) && same(againAttention, attention))
	other := a.fence
	other.Epoch = "9"
	_, _, err = store.ParkWait(ctx, parkOf("wait-a", a.revision, kitKeys["a"], hour, true), other)
	refuses("wait.park.repeat-other-fence", err, "conflict")

	// The run closure: until its wait is resumed nothing leases the run or commits to it unfenced.
	_, err = store.AcquireLease(ctx, LeaseAcquire{Resource: "wait-a", Owner: "worker-x", ExpectedRevision: a.revision, DurationMs: hour})
	refuses("wait.closure.lease", err, "wait-pending")
	_, err = store.Commit(ctx, RunCommit{RunID: "wait-a", ExpectedRevision: a.revision, CommitID: "outside", Events: [][]byte{[]byte(`{}`)}})
	refuses("wait.closure.commit", err, "wait-pending")

	// The key check: a key belongs to one wait, and a wake presents its own wait's key.
	_, _, err = store.ParkWait(ctx, parkOf("wait-b", b.revision, kitKeys["a"], 4*hour, true), b.fence)
	refuses("wait.key.reused", err, "key-reused")
	check("wait.key.reused-unchanged", readWait("wait-b") == nil && readLease("wait-b") != nil && readLease("wait-b").State == "held")
	_, _, err = store.ParkWait(ctx, parkOf("wait-b", b.revision, kitKeys["b"], 4*hour, true), b.fence)
	check("wait.key.own", err == nil)
	_, err = store.WakeWait(ctx, decision("wait-b", kitKeys["a"], "decision-1"))
	refuses("wait.key.other-wait", err, "unauthenticated")
	check("wait.key.unchanged", readWait("wait-b") != nil && readWait("wait-b").State == "parked")

	restart("wait.restart.parked")
	exported, err := store.ExportWaits(ctx, "wait-a")
	check("wait.restart.parked", readWait("wait-a") != nil && readWait("wait-a").State == "parked" && err == nil && same(exported.Attention, []AttentionEvent{attention}))
	_, err = store.AcquireLease(ctx, LeaseAcquire{Resource: "wait-a", Owner: "worker-x", ExpectedRevision: a.revision, DurationMs: hour})
	refuses("wait.restart.closure", err, "wait-pending")

	// The single winner: an event and a time wake at once, exactly one wins; a duplicate returns it,
	// anything else is stale.
	at = t0.Add(time.Minute)
	type woke struct {
		disposition WakeDisposition
		err         error
	}
	tick := WakeEvidence{Version: 1, Resource: "wait-a", Wait: "wait-1", Evidence: "tick-1", Kind: "time", Key: kitKeys["a"]}
	results := make(chan woke, 2)
	for _, evidence := range []WakeEvidence{decision("wait-a", kitKeys["a"], "decision-1"), tick} {
		go func() { d, err := store.WakeWait(ctx, evidence); results <- woke{d, err} }()
	}
	var winner *WakeDisposition
	stale := 0
	for range 2 {
		result := <-results
		if result.err == nil {
			held := result.disposition
			winner = &held
		} else if kitReason(result.err) == "stale" {
			stale++
		}
	}
	check("wait.wake.one-winner", winner != nil && stale == 1)
	duplicate := tick
	if winner.Kind == "event" {
		duplicate = decision("wait-a", kitKeys["a"], "decision-1")
	}
	repeated, err := store.WakeWait(ctx, duplicate)
	check("wait.wake.duplicate", err == nil && repeated == *winner)
	_, err = store.WakeWait(ctx, decision("wait-a", kitKeys["a"], "decision-9"))
	refuses("wait.wake.stale", err, "stale")

	// Claim once: two claims at once, one is granted the run's lease; the claim's lease ends at the
	// approval's expiry.
	type claimed struct {
		wait  RunWait
		lease RunLease
		err   error
	}
	claims := make(chan claimed, 2)
	for _, owner := range []string{"worker-b", "worker-c"} {
		go func() {
			w, l, err := store.ResumeWait(ctx, claim("wait-a", winner.Evidence, owner, a.revision))
			claims <- claimed{w, l, err}
		}()
	}
	var granted *claimed
	resumedTwice := 0
	for range 2 {
		result := <-claims
		if result.err == nil {
			held := result
			granted = &held
		} else if kitReason(result.err) == "already-resumed" {
			resumedTwice++
		}
	}
	check("wait.claim.once", granted != nil && resumedTwice == 1)
	check("wait.claim.result", granted.wait.State == "resumed" && granted.wait.Resumed != nil && granted.wait.Resumed.Owner == granted.lease.Owner && granted.wait.Resumed.Epoch == granted.lease.Epoch && granted.lease.Epoch == "2" && granted.lease.State == "held")
	_, _, err = store.ResumeWait(ctx, claim("wait-a", winner.Evidence, "worker-d", a.revision))
	refuses("wait.claim.again", err, "already-resumed")
	fence := FenceOf(granted.lease)
	check("wait.approval.claim-bounded", granted.lease.Expiry == iso(time.Hour))
	renewed, err := store.RenewLease(ctx, fence, 24*hour)
	check("wait.approval.renew-bounded", err == nil && renewed.Expiry == iso(time.Hour))

	// The commit-time approval binding (#266 N3): fenced or not, a checkpoint that is not the reviewed
	// call is refused.
	head, _ := store.Load(ctx, "wait-a")
	check("wait.load", head != nil)
	_, err = commitWith("wait-a", head.Revision, "rewrite", func(a map[string]any) { a["args"] = map[string]any{"title": "Something else"} }, &fence)
	refuses("wait.approval.binding", err, "approval-changed")
	_, err = commitWith("wait-a", head.Revision, "rewrite-unfenced", func(a map[string]any) { a["callId"] = "call-9" }, nil)
	refuses("wait.approval.binding-unfenced", err, "approval-changed")
	approved, err := commitWith("wait-a", head.Revision, "approved", func(a map[string]any) { a["state"] = "approved" }, &fence)
	check("wait.approval.approved", err == nil)
	_, err = store.ReleaseLease(ctx, fence)
	check("wait.approval.release", err == nil)

	// The unsettled approval and its binding survive a restart.
	restart("wait.restart.approval")
	reacquired, err := store.AcquireLease(ctx, LeaseAcquire{Resource: "wait-a", Owner: "worker-b", ExpectedRevision: approved.Revision, DurationMs: 24 * hour})
	check("wait.restart.approval-ceiling", err == nil && reacquired.Expiry == iso(time.Hour))
	refence := FenceOf(reacquired)
	_, err = commitWith("wait-a", approved.Revision, "rewrite-after", func(a map[string]any) { a["args"] = map[string]any{"title": "Something else"} }, &refence)
	refuses("wait.restart.approval-binding", err, "approval-changed")
	_, err = store.ReleaseLease(ctx, refence)
	check("wait.restart.release", err == nil)

	// The approval's ceiling (#266 N1): at the expiry nothing leases the run, and the deadline wake, due at
	// it, ends the resumed wait with the call's outcome unknown; the run is closed for good.
	at = t0.Add(time.Hour - time.Millisecond)
	_, err = store.WakeWait(ctx, deadline("wait-a", kitKeys["a"]))
	refuses("wait.unsettled.not-due", err, "not-due")
	at = t0.Add(time.Hour)
	_, err = store.AcquireLease(ctx, LeaseAcquire{Resource: "wait-a", Owner: "worker-c", ExpectedRevision: approved.Revision, DurationMs: hour})
	refuses("wait.approval.expiry", err, "deadline-passed")
	ended, err := store.WakeWait(ctx, deadline("wait-a", kitKeys["a"]))
	check("wait.unsettled.ended", err == nil && ended.Kind == "deadline" && ended.At == iso(time.Hour) && readWait("wait-a") != nil && readWait("wait-a").State == "expired")
	_, err = store.AcquireLease(ctx, LeaseAcquire{Resource: "wait-a", Owner: "worker-c", ExpectedRevision: approved.Revision, DurationMs: hour})
	refuses("wait.closure.closed", err, "closed")
	_, err = store.Commit(ctx, RunCommit{RunID: "wait-a", ExpectedRevision: approved.Revision, CommitID: "closed", Events: [][]byte{[]byte(`{}`)}})
	refuses("wait.closure.closed-commit", err, "closed")

	// Settlement: the commit that records the call executed lifts the approval's ceiling, and that
	// survives a restart.
	_, err = store.WakeWait(ctx, decision("wait-b", kitKeys["b"], "decision-1"))
	check("wait.approval.second-wake", err == nil)
	_, second, err := store.ResumeWait(ctx, claim("wait-b", "decision-1", "worker-b", b.revision))
	check("wait.approval.second-claim", err == nil && second.Expiry == iso(4*time.Hour))
	secondFence := FenceOf(second)
	executed, err := commitWith("wait-b", b.revision, "executed", func(a map[string]any) { a["state"] = "executed" }, &secondFence)
	check("wait.approval.executed", err == nil)
	_, err = store.ReleaseLease(ctx, secondFence)
	check("wait.approval.release-second", err == nil)
	restart("wait.restart.settled")
	at = t0.Add(5 * time.Hour)
	settled, err := store.AcquireLease(ctx, LeaseAcquire{Resource: "wait-b", Owner: "worker-c", ExpectedRevision: executed.Revision, DurationMs: hour})
	check("wait.approval.settled", err == nil && settled.Expiry == iso(6*time.Hour))
	_, err = store.ReleaseLease(ctx, FenceOf(settled))
	check("wait.approval.release-settled", err == nil)

	// The run's deadline: it lowers a held lease and bounds a renewal, a park stamps it, a later and
	// earlier one reaches the wait already parked (#266 M1), the claim is refused at it, and its deadline
	// wake ends the wait and the run.
	c := open("wait-c")
	limited, err := store.LimitRun(ctx, RunLimit{Version: 1, Resource: "wait-c", Run: iso(5*time.Hour + 30*time.Minute)})
	check("wait.deadline.limit", err == nil && limited.Run == iso(5*time.Hour+30*time.Minute))
	check("wait.deadline.lowered", readLease("wait-c") != nil && readLease("wait-c").Expiry == iso(5*time.Hour+30*time.Minute))
	renewed, err = store.RenewLease(ctx, c.fence, hour)
	check("wait.deadline.renew", err == nil && renewed.Expiry == iso(5*time.Hour+30*time.Minute))
	third, _, err := store.ParkWait(ctx, parkOf("wait-c", c.revision, kitKeys["c"], 4*hour, false), c.fence)
	check("wait.deadline.stamped", err == nil && third.Deadline != nil && third.Deadline.At == iso(5*time.Hour+30*time.Minute) && third.Deadline.By == "run")
	_, err = store.WakeWait(ctx, decision("wait-c", kitKeys["c"], "decision-1"))
	check("wait.deadline.woken", err == nil)
	limited, err = store.LimitRun(ctx, RunLimit{Version: 1, Resource: "wait-c", Run: iso(9 * time.Hour)})
	check("wait.deadline.earliest", err == nil && limited.Run == iso(5*time.Hour+30*time.Minute))
	_, err = store.LimitRun(ctx, RunLimit{Version: 1, Resource: "wait-c", Run: iso(5*time.Hour + 10*time.Minute)})
	check("wait.deadline.restamped", err == nil && readWait("wait-c") != nil && readWait("wait-c").Deadline != nil && readWait("wait-c").Deadline.At == iso(5*time.Hour+10*time.Minute))
	at = t0.Add(5*time.Hour + 10*time.Minute)
	_, _, err = store.ResumeWait(ctx, claim("wait-c", "decision-1", "worker-b", c.revision))
	refuses("wait.deadline.claim", err, "deadline-passed")
	_, err = store.AcquireLease(ctx, LeaseAcquire{Resource: "wait-c", Owner: "worker-b", ExpectedRevision: c.revision, DurationMs: hour})
	refuses("wait.deadline.acquire", err, "deadline-passed")
	expired, err := store.WakeWait(ctx, deadline("wait-c", kitKeys["c"]))
	check("wait.deadline.expired", err == nil && expired.At == iso(5*time.Hour+10*time.Minute) && readWait("wait-c") != nil && readWait("wait-c").State == "expired")
	restart("wait.restart.deadline")
	_, err = store.AcquireLease(ctx, LeaseAcquire{Resource: "wait-c", Owner: "worker-b", ExpectedRevision: c.revision, DurationMs: hour})
	refuses("wait.restart.closed", err, "closed")
	limited, err = store.LimitRun(ctx, RunLimit{Version: 1, Resource: "wait-c", Run: iso(9 * time.Hour)})
	check("wait.restart.earliest", err == nil && limited.Run == iso(5*time.Hour+10*time.Minute))
	_, err = store.WakeWait(ctx, decision("wait-b", kitKeys["a"], "decision-x"))
	refuses("wait.restart.key", err, "unauthenticated")

	// ADR-0029: a store that declares it keeps a delegated run's principals with the wait and the lease,
	// across a restart: the wait's pins hold them, its claim is a lease of the same principals, and a
	// later takeover keeps them.
	if keeps, ok := any(store).(LeasePrincipalStore); ok && keeps.LeasePrincipals() {
		principal := PrincipalBinding{Sub: contract.RunPrincipalPrincipalReference{Kind: "person", Issuer: "https://kit.example/", Subject: "kit-subject"}, RootActor: strings.Repeat("b1", 16), Host: "kit-host"}
		otherActor, otherSubject := principal, principal
		otherActor.RootActor = strings.Repeat("b2", 16)
		otherSubject.Sub.Subject = "other"
		same := func(held *PrincipalBinding) bool { return SamePrincipalBinding(held, &principal) }
		receipt, err := store.Commit(ctx, RunCommit{RunID: "wait-p", CommitID: "wait-p-open", Checkpoint: asked})
		check("wait.open", err == nil)
		held, err := store.AcquireLease(ctx, LeaseAcquire{Resource: "wait-p", Owner: "worker-a", ExpectedRevision: receipt.Revision, DurationMs: hour, Principal: &principal})
		check("wait.principal.lease", err == nil && held.Version == 2 && same(held.Principal))
		delegatedPins := pins
		delegatedPins.Principal = &principal
		delegatedDigest, err := PinsDigest(delegatedPins)
		check("wait.fixture", err == nil)
		park := parkOf("wait-p", receipt.Revision, kitKeys["d"], 4*hour, true)
		park.Pins = delegatedPins
		_, _, err = store.ParkWait(ctx, park, FenceOf(held))
		check("wait.principal.park", err == nil)
		parked := readWait("wait-p")
		check("wait.principal.pinned", parked != nil && same(parked.Pins.Principal))
		restart("wait.restart.principal")
		parked = readWait("wait-p")
		check("wait.restart.principal", parked != nil && same(parked.Pins.Principal))
		_, err = store.WakeWait(ctx, decision("wait-p", kitKeys["d"], "decision-1"))
		check("wait.principal.wake", err == nil)
		claimP := func(owner string, binding *PrincipalBinding) WaitClaim {
			base := claim("wait-p", "decision-1", owner, receipt.Revision)
			base.Lease.Principal = binding
			base.Revalidation.Pins = delegatedDigest
			return base
		}
		_, _, err = store.ResumeWait(ctx, claimP("worker-b", &otherActor))
		refuses("wait.principal.claim-actor", err, "principal-changed")
		_, _, err = store.ResumeWait(ctx, claimP("worker-b", &otherSubject))
		refuses("wait.principal.claim-subject", err, "principal-changed")
		_, _, err = store.ResumeWait(ctx, claimP("worker-b", nil))
		refuses("wait.principal.claim-none", err, "principal-changed")
		parked = readWait("wait-p")
		check("wait.principal.claim-unchanged", parked != nil && parked.State == "woken")
		resumed, lease, err := store.ResumeWait(ctx, claimP("worker-b", &principal))
		check("wait.principal.claim", err == nil && resumed.State == "resumed" && lease.Version == 2 && same(lease.Principal) && lease.Epoch == "2")
		fence := FenceOf(lease)
		forged := fence
		forged.Principal = &otherActor
		refuses("wait.principal.fence", store.CheckFence(ctx, forged), "principal-changed")
		done, err := commitWith("wait-p", receipt.Revision, "wait-p-done", func(m map[string]any) { m["state"] = "executed" }, &fence)
		check("wait.principal.commit", err == nil)
		_, err = store.ReleaseLease(ctx, fence)
		check("wait.principal.release", err == nil)
		restart("wait.restart.principal-lease")
		_, err = store.AcquireLease(ctx, LeaseAcquire{Resource: "wait-p", Owner: "worker-c", ExpectedRevision: done.Revision, DurationMs: hour, Principal: &otherActor})
		refuses("wait.restart.principal-takeover", err, "principal-changed")
		kept, err := store.AcquireLease(ctx, LeaseAcquire{Resource: "wait-p", Owner: "worker-c", ExpectedRevision: done.Revision, DurationMs: hour, Principal: &principal})
		check("wait.restart.principal-kept", err == nil && kept.Epoch == "3")
	}
	return nil
}

// reauthConformance (#226): the re-authorization properties. The run's head holds the never-admitted
// dispatch as the writer records it; the disposition names it, under the lease that parks it.
func reauthConformance(ctx context.Context, create WaitStoreFactory) error {
	check := func(property string, ok bool) {
		if !ok {
			panic(kitFailure{property})
		}
	}
	refuses := func(property string, err error, reason string) {
		check(property, err != nil && kitReason(err) == reason)
	}
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	at := t0
	now := func() time.Time { return at }
	iso := func(instant time.Time) string { text, _ := leaseExpiryAt(instant.UnixMilli()); return text }
	hour := int64(time.Hour / time.Millisecond)
	store := create(now, nil)
	restart := func(property string) {
		next := create(now, store)
		check(property, next != nil && any(next) != any(store))
		store = next
	}
	same := func(a, b any) bool { x, _ := canonicalRaw(a); y, _ := canonicalRaw(b); return string(x) == string(y) }
	source, request := "sha256:"+strings.Repeat("5", 64), "sha256:"+strings.Repeat("6", 64)
	operation := func(run string) EffectOperation {
		return EffectOperation{Run: run, Operation: "op-1", Source: source, Tool: "create_issue", RequestDigest: request}
	}
	neverAdmitted := func() map[string]any {
		return map[string]any{"id": "op-1", "kind": "host", "read": false, "state": "unknown", "source": source, "tool": "create_issue", "requestDigest": request, "failure": map[string]any{"version": 1, "code": "authentication", "source": "tool", "outcome": "not-started"}}
	}
	headOf := func(approval map[string]any, rows ...map[string]any) []byte {
		checkpoint := map[string]any{"kit": "reauth-conformance", "durable": map[string]any{"dispatches": rows}}
		if approval != nil {
			checkpoint["approval"] = approval
		}
		raw, _ := canonicalRaw(checkpoint)
		return raw
	}
	head := func(approval map[string]any) []byte { return headOf(approval, neverAdmitted()) }
	// Every way a head fails to hold the operation as the never-admitted dispatch.
	changedRow := func(change func(map[string]any)) []map[string]any {
		row := neverAdmitted()
		change(row)
		return []map[string]any{row}
	}
	failureWith := func(member, value string) map[string]any {
		failed := map[string]any{"version": 1, "code": "authentication", "source": "tool", "outcome": "not-started"}
		failed[member] = value
		return failed
	}
	unboundHeads := []struct {
		name string
		rows []map[string]any
	}{
		{"result", changedRow(func(r map[string]any) { r["state"] = "result"; delete(r, "failure") })},
		{"claimed", changedRow(func(r map[string]any) { r["state"] = "claimed"; delete(r, "failure") })},
		{"not-last", []map[string]any{neverAdmitted(), {"id": "model-2", "kind": "model", "read": true, "state": "result", "source": "model", "tool": "chat", "requestDigest": "sha256:" + strings.Repeat("9", 64)}}},
		{"outcome", changedRow(func(r map[string]any) { r["failure"] = failureWith("outcome", "unknown") })},
		{"code", changedRow(func(r map[string]any) { r["failure"] = failureWith("code", "unknown") })},
		{"no-failure", changedRow(func(r map[string]any) { delete(r, "failure") })},
		{"read", changedRow(func(r map[string]any) { r["read"] = true })},
		{"kind", changedRow(func(r map[string]any) { r["kind"] = "model" })},
		{"id", changedRow(func(r map[string]any) { r["id"] = "op-2" })},
		{"source", changedRow(func(r map[string]any) { r["source"] = "sha256:" + strings.Repeat("4", 64) })},
		{"tool", changedRow(func(r map[string]any) { r["tool"] = "close_issue" })},
		{"request", changedRow(func(r map[string]any) { r["requestDigest"] = "sha256:" + strings.Repeat("3", 64) })},
	}
	type opened struct {
		revision   string
		checkpoint []byte
		fence      LeaseFence
	}
	open := func(run string, checkpoint []byte) opened {
		receipt, err := store.Commit(ctx, RunCommit{RunID: run, CommitID: run + "-open", Checkpoint: checkpoint})
		check("reauth.open", err == nil)
		lease, err := store.AcquireLease(ctx, LeaseAcquire{Resource: run, Owner: "worker-a", ExpectedRevision: receipt.Revision, DurationMs: hour})
		check("reauth.open", err == nil)
		return opened{receipt.Revision, checkpoint, FenceOf(lease)}
	}
	principal := "sha256:" + strings.Repeat("7", 64)
	disposition := func(run string, fence LeaseFence) ReauthDisposition {
		key, _ := EffectIdempotencyKey(operation(run))
		return ReauthDisposition{Version: 1, Resource: run, Operation: operation(run), IdempotencyKey: key, Fence: fence, Cause: "expired", Connection: "kit-conn", Principal: principal, Permissions: []string{"issues:read", "issues:write"}, Requires: []string{"issues:write"}}
	}
	var approval contract.CheckpointApproval
	_ = json.Unmarshal([]byte(kitApproval), &approval)
	binding, err := ApprovalBindingOf(approval)
	check("reauth.fixture", err == nil)
	pins := ResumePins{Model: PinnedModel{Provider: "openai", Model: "kit"}, Policy: "sha256:44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a", Sources: []SourcePin{{Source: "tracker", DescriptorDigest: approval.Source.ToolListDigest}}, Artifacts: []ArtifactPin{}}
	pinsDigest, err := PinsDigest(pins)
	check("reauth.fixture", err == nil)
	parkOf := func(run string, held opened, proof ReauthDisposition, key string) RunWait {
		digest, _ := WakeKeyDigest(key)
		return RunWait{Version: WaitRecordVersion, Resource: run, Wait: "wait-r", Reason: "reauthorization", Pending: contract.RunWaitPending{Reauthorization: &contract.RunWaitPendingReauthorization{Kind: "reauthorization", Operation: "op-1", Source: source, Tool: "create_issue", Connection: "kit-conn", Cause: "expired"}}, Condition: WaitCondition{Event: &contract.RunWaitEventCondition{Kind: "reauthorized", Subject: "kit-conn"}}, Continuation: run + "-continue", Checkpoint: contract.RunWaitCheckpointBinding{Revision: held.revision, Digest: CheckpointBytesDigest(held.checkpoint)}, Pins: pins, WakeKeyDigest: digest, Release: []WaitRelease{}, Reauthorization: &proof}
	}
	refreshed := func(change func(*Reauthorized)) *Reauthorized {
		r := Reauthorized{Actor: "person-owner", At: iso(at), Principal: principal, Permissions: []string{"issues:write"}, Digest: "sha256:" + strings.Repeat("8", 64)}
		if change != nil {
			change(&r)
		}
		return &r
	}
	claim := func(run, revision string, refresh *Reauthorized) WaitClaim {
		return WaitClaim{Resource: run, Wait: "wait-r", Evidence: "refreshed-1", Lease: LeaseAcquire{Resource: run, Owner: "worker-b", ExpectedRevision: revision, DurationMs: 24 * hour}, Reconciled: []WaitRelease{}, Revalidation: Revalidation{Version: 1, Resource: run, Wait: "wait-r", Pins: pinsDigest, Evidence: "sha256:" + strings.Repeat("e", 64), Changes: []ResumeChange{}}, Reauthorized: refresh}
	}
	wake := func(run, key string) WakeEvidence {
		return WakeEvidence{Version: 1, Resource: run, Wait: "wait-r", Evidence: "refreshed-1", Kind: "event", Event: &contract.RunWaitEventCondition{Kind: "reauthorized", Subject: "kit-conn"}, Key: key}
	}
	keys := map[string]string{"r": "kit.reauth-r.Q7z2Xm9LpV4cR8tNb3Yh6Df1", "s": "kit.reauth-s.Hs8Pq3Lz6Wd1Rm9Tc4Vb7Nx2"}
	keyOf := func(name string) string { return "kit." + name + ".Zx4Cv7Bn1Mq8Wp3Lk6Jh9Gf2" }
	readWait := func(run string) *RunWait { held, _ := store.ReadWait(ctx, run, "wait-r"); return held }
	readLease := func(run string) *RunLease { held, _ := store.ReadLease(ctx, run); return held }

	// Binding the evidence to the invocation: the disposition parks only under the lease it was minted
	// under, over a head that holds its operation as the never-admitted dispatch. A refused park changes
	// nothing and leaves the lease held.
	r := open("reauth-r", head(nil))
	proof := disposition("reauth-r", r.fence)
	stale := proof
	stale.Fence.Epoch = "9"
	_, _, err = store.ParkWait(ctx, parkOf("reauth-r", r, stale, keys["r"]), r.fence)
	refuses("reauth.park.stale", err, "stale")
	stale = proof
	stale.Fence.Owner = "worker-x"
	_, _, err = store.ParkWait(ctx, parkOf("reauth-r", r, stale, keys["r"]), r.fence)
	refuses("reauth.park.stale-owner", err, "stale")
	check("reauth.park.stale-unchanged", readWait("reauth-r") == nil && readLease("reauth-r") != nil && readLease("reauth-r").State == "held")
	for _, unbound := range unboundHeads {
		run := "reauth-u-" + unbound.name
		held := open(run, headOf(nil, unbound.rows...))
		_, _, err = store.ParkWait(ctx, parkOf(run, held, disposition(run, held.fence), keys["s"]), held.fence)
		refuses("reauth.park.unbound."+unbound.name, err, "unbound")
		check("reauth.park.unbound-unchanged", readWait(run) == nil && readLease(run) != nil && readLease(run).State == "held")
	}
	parked, attention, err := store.ParkWait(ctx, parkOf("reauth-r", r, proof, keys["r"]), r.fence)
	check("reauth.park.committed", err == nil && parked.Reason == "reauthorization" && same(parked.Reauthorization, proof) && parked.Approval == nil && attention.Needs == "reauthorization" && attention.Subject != nil && *attention.Subject == "kit-conn" && readLease("reauth-r") != nil && readLease("reauth-r").State == "released")
	_, err = store.AcquireLease(ctx, LeaseAcquire{Resource: "reauth-r", Owner: "worker-x", ExpectedRevision: r.revision, DurationMs: hour})
	refuses("reauth.closure.lease", err, "wait-pending")
	restart("reauth.restart.parked")
	check("reauth.restart.parked", same(readWait("reauth-r"), parked))

	// The claim: a wake is not the refresh; another principal is a changed identity; narrower permissions
	// after the refresh that no longer cover the operation make it inadmissible; a refresh before the park
	// or after the store's time is stale.
	at = t0.Add(time.Minute)
	_, err = store.WakeWait(ctx, wake("reauth-r", keys["r"]))
	check("reauth.wake", err == nil)
	_, _, err = store.ResumeWait(ctx, claim("reauth-r", r.revision, nil))
	refuses("reauth.claim.required", err, "decision-required")
	_, _, err = store.ResumeWait(ctx, claim("reauth-r", r.revision, refreshed(func(x *Reauthorized) { x.Principal = "sha256:" + strings.Repeat("9", 64) })))
	refuses("reauth.claim.identity", err, "identity-changed")
	_, _, err = store.ResumeWait(ctx, claim("reauth-r", r.revision, refreshed(func(x *Reauthorized) { x.Permissions = []string{"issues:read"} })))
	refuses("reauth.claim.narrower", err, "permission-insufficient")
	_, _, err = store.ResumeWait(ctx, claim("reauth-r", r.revision, refreshed(func(x *Reauthorized) { x.At = iso(t0.Add(-time.Millisecond)) })))
	refuses("reauth.claim.before-park", err, "stale")
	_, _, err = store.ResumeWait(ctx, claim("reauth-r", r.revision, refreshed(func(x *Reauthorized) { x.At = iso(at.Add(time.Millisecond)) })))
	refuses("reauth.claim.after-now", err, "stale")
	check("reauth.claim.refused-unchanged", readWait("reauth-r") != nil && readWait("reauth-r").State == "woken" && readLease("reauth-r") != nil && readLease("reauth-r").State == "released")
	resumed, lease, err := store.ResumeWait(ctx, claim("reauth-r", r.revision, refreshed(nil)))
	check("reauth.claim.committed", err == nil && resumed.State == "resumed" && same(resumed.Reauthorized, refreshed(nil)) && lease.State == "held" && lease.Epoch == "2")
	_, _, err = store.ResumeWait(ctx, claim("reauth-r", r.revision, refreshed(nil)))
	refuses("reauth.claim.once", err, "already-resumed")
	restart("reauth.restart.claimed")
	check("reauth.restart.claimed", readWait("reauth-r") != nil && same(readWait("reauth-r").Reauthorized, refreshed(func(x *Reauthorized) { x.At = iso(t0.Add(time.Minute)) })) && readLease("reauth-r") != nil && readLease("reauth-r").Epoch == "2")
	check("reauth.restart.disposition", readWait("reauth-r") != nil && same(readWait("reauth-r").Reauthorization, proof))

	// The approval: a reauthorization wait parked while the run's approved call is unsettled carries that
	// approval's binding and expiry, the store's own, and they survive a restart; its claim is bounded by the
	// expiry and refused from it; and a later run limit keeps the approval time.
	asked := kitCheckpoint(nil)
	type boundWait struct {
		revision, expiry string
		fence            LeaseFence
	}
	bound := func(run string) boundWait {
		openedRun, err := store.Commit(ctx, RunCommit{RunID: run, CommitID: run + "-open", Checkpoint: asked})
		check("reauth.approval.open", err == nil)
		leased, err := store.AcquireLease(ctx, LeaseAcquire{Resource: run, Owner: "worker-a", ExpectedRevision: openedRun.Revision, DurationMs: hour})
		check("reauth.approval.open", err == nil)
		keyDigest, _ := WakeKeyDigest(keyOf(run + "-1"))
		held := binding
		_, _, err = store.ParkWait(ctx, RunWait{Version: WaitRecordVersion, Resource: run, Wait: "wait-1", Reason: "approval", Pending: contract.RunWaitPending{Approval: &contract.RunWaitPendingApproval{Kind: "approval", CallID: "call-1", Name: approval.Name, Tool: approval.Tool}}, Condition: WaitCondition{Event: &contract.RunWaitEventCondition{Kind: "approval-decision", Subject: "call-1"}}, Limits: &WaitLimits{ApprovalTtlMs: &hour}, Approval: &held, Continuation: run + "-approve", Checkpoint: contract.RunWaitCheckpointBinding{Revision: openedRun.Revision, Digest: CheckpointBytesDigest(asked)}, Pins: pins, WakeKeyDigest: keyDigest, Release: []WaitRelease{}}, FenceOf(leased))
		check("reauth.approval.parked", err == nil)
		_, err = store.WakeWait(ctx, WakeEvidence{Version: 1, Resource: run, Wait: "wait-1", Evidence: "decision-1", Kind: "event", Event: &contract.RunWaitEventCondition{Kind: "approval-decision", Subject: "call-1"}, Key: keyOf(run + "-1")})
		check("reauth.approval.woken", err == nil)
		_, approved, err := store.ResumeWait(ctx, WaitClaim{Resource: run, Wait: "wait-1", Evidence: "decision-1", Lease: LeaseAcquire{Resource: run, Owner: "worker-b", ExpectedRevision: openedRun.Revision, DurationMs: 24 * hour}, Reconciled: []WaitRelease{}, Revalidation: Revalidation{Version: 1, Resource: run, Wait: "wait-1", Pins: pinsDigest, Evidence: "sha256:" + strings.Repeat("e", 64), Changes: []ResumeChange{}}})
		check("reauth.approval.claimed", err == nil)
		var approvedRecord map[string]any
		_ = json.Unmarshal([]byte(kitApproval), &approvedRecord)
		approvedRecord["state"] = "approved"
		refusedHead := head(approvedRecord)
		committed, err := store.CommitFenced(ctx, RunCommit{RunID: run, ExpectedRevision: openedRun.Revision, CommitID: "approved-refused", Checkpoint: refusedHead}, FenceOf(approved))
		check("reauth.approval.committed", err == nil)
		expiry := iso(at.Add(time.Hour))
		parked, _, err := store.ParkWait(ctx, parkOf(run, opened{committed.Revision, refusedHead, FenceOf(approved)}, disposition(run, FenceOf(approved)), keyOf(run+"-2")), FenceOf(approved))
		check("reauth.approval.bound", err == nil && same(parked.Approval, binding) && parked.Deadline != nil && parked.Deadline.Approval != nil && *parked.Deadline.Approval == expiry && parked.Deadline.At == expiry)
		return boundWait{committed.Revision, expiry, FenceOf(approved)}
	}
	at = t0.Add(2 * time.Minute)
	a := bound("reauth-a")
	at = t0.Add(3 * time.Minute)
	_, err = store.WakeWait(ctx, wake("reauth-a", keyOf("reauth-a-2")))
	check("reauth.approval.wake", err == nil)
	restart("reauth.restart.bound")
	carried := readWait("reauth-a")
	check("reauth.restart.bound", carried != nil && carried.State == "woken" && same(carried.Approval, binding) && carried.Deadline != nil && carried.Deadline.Approval != nil && *carried.Deadline.Approval == a.expiry && same(carried.Reauthorization, disposition("reauth-a", a.fence)))
	expiryAt := time.UnixMilli(waitTimeMillis(a.expiry)).UTC()
	at = expiryAt
	expired := claim("reauth-a", a.revision, refreshed(func(x *Reauthorized) { x.At = iso(t0.Add(3 * time.Minute)) }))
	expired.Lease.Owner = "worker-c"
	_, _, err = store.ResumeWait(ctx, expired)
	refuses("reauth.approval.expiry", err, "deadline-passed")
	b := bound("reauth-b")
	at = at.Add(time.Minute)
	_, err = store.WakeWait(ctx, wake("reauth-b", keyOf("reauth-b-2")))
	check("reauth.approval.wake", err == nil)
	boundClaim := claim("reauth-b", b.revision, refreshed(nil))
	boundClaim.Lease.Owner = "worker-c"
	_, boundLease, err := store.ResumeWait(ctx, boundClaim)
	check("reauth.approval.claim-bounded", err == nil && boundLease.Expiry == b.expiry)
	c := bound("reauth-c")
	cExpiry := time.UnixMilli(waitTimeMillis(c.expiry)).UTC()
	_, err = store.LimitRun(ctx, RunLimit{Version: 1, Resource: "reauth-c", Run: iso(cExpiry.Add(time.Hour))})
	check("reauth.limit.later", err == nil)
	later := readWait("reauth-c")
	check("reauth.limit.later", later != nil && later.Deadline != nil && later.Deadline.Approval != nil && *later.Deadline.Approval == c.expiry && later.Deadline.At == c.expiry && later.Deadline.By == "approval")
	earlier := iso(cExpiry.Add(-10 * time.Minute))
	_, err = store.LimitRun(ctx, RunLimit{Version: 1, Resource: "reauth-c", Run: earlier})
	check("reauth.limit.earlier", err == nil)
	limited := readWait("reauth-c")
	if limited != nil {
		_, readErr := readStoreWait(*limited)
		check("reauth.limit.earlier", readErr == nil && limited.Deadline != nil && limited.Deadline.Approval != nil && *limited.Deadline.Approval == c.expiry && limited.Deadline.At == earlier && limited.Deadline.By == "run")
	} else {
		check("reauth.limit.earlier", false)
	}

	// The timeout: an event after it is refused, and its wake ends the wait; a timed-out reauthorization wait
	// is terminal, as a timed-out approval wait is: not claimed, and its run closed for good, also after a
	// restart.
	timed := open("reauth-t", head(nil))
	timeoutAt := at.Add(time.Hour)
	timeoutText := iso(timeoutAt)
	timedPark := parkOf("reauth-t", timed, disposition("reauth-t", timed.fence), keyOf("reauth-t"))
	timedPark.TimeoutAt = &timeoutText
	_, _, err = store.ParkWait(ctx, timedPark, timed.fence)
	check("reauth.timeout.parked", err == nil)
	at = timeoutAt
	_, err = store.WakeWait(ctx, wake("reauth-t", keyOf("reauth-t")))
	refuses("reauth.timeout.late", err, "timed-out")
	timedOut, err := store.WakeWait(ctx, WakeEvidence{Version: 1, Resource: "reauth-t", Wait: "wait-r", Evidence: "timeout-1", Kind: "timeout", Key: keyOf("reauth-t")})
	check("reauth.timeout.wake", err == nil && timedOut.Kind == "timeout" && readWait("reauth-t") != nil && readWait("reauth-t").State == "timed-out")
	timedClaim := claim("reauth-t", timed.revision, refreshed(nil))
	timedClaim.Evidence = "timeout-1"
	_, _, err = store.ResumeWait(ctx, timedClaim)
	refuses("reauth.timeout.claim", err, "timed-out")
	_, err = store.AcquireLease(ctx, LeaseAcquire{Resource: "reauth-t", Owner: "worker-x", ExpectedRevision: timed.revision, DurationMs: hour})
	refuses("reauth.timeout.closed", err, "closed")
	restart("reauth.restart.timeout")
	_, err = store.AcquireLease(ctx, LeaseAcquire{Resource: "reauth-t", Owner: "worker-x", ExpectedRevision: timed.revision, DurationMs: hour})
	refuses("reauth.restart.timeout", err, "closed")

	// The deadline: from it no event wake is accepted, and its deadline wake ends the wait and closes the run
	// for good.
	dated := open("reauth-d", head(nil))
	datedPark := parkOf("reauth-d", dated, disposition("reauth-d", dated.fence), keyOf("reauth-d"))
	datedPark.Limits = &WaitLimits{WaitMs: &hour}
	parkedDated, _, err := store.ParkWait(ctx, datedPark, dated.fence)
	check("reauth.deadline.stamped", err == nil && parkedDated.Deadline != nil && parkedDated.Deadline.At == iso(at.Add(time.Hour)) && parkedDated.Deadline.By == "wait")
	at = at.Add(time.Hour)
	_, err = store.WakeWait(ctx, wake("reauth-d", keyOf("reauth-d")))
	refuses("reauth.deadline.late", err, "deadline-passed")
	ended, err := store.WakeWait(ctx, WakeEvidence{Version: 1, Resource: "reauth-d", Wait: "wait-r", Evidence: "deadline-1", Kind: "deadline", Key: keyOf("reauth-d")})
	check("reauth.deadline.wake", err == nil && ended.Kind == "deadline" && readWait("reauth-d") != nil && readWait("reauth-d").State == "expired")
	_, err = store.AcquireLease(ctx, LeaseAcquire{Resource: "reauth-d", Owner: "worker-x", ExpectedRevision: dated.revision, DurationMs: hour})
	refuses("reauth.deadline.closed", err, "closed")
	restart("reauth.restart.deadline")
	_, err = store.AcquireLease(ctx, LeaseAcquire{Resource: "reauth-d", Owner: "worker-x", ExpectedRevision: dated.revision, DurationMs: hour})
	refuses("reauth.restart.deadline", err, "closed")
	return nil
}
