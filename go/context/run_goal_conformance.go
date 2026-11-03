package runcontext

// #230: the goal conformance kit, for a host store kind's goal port beside its lease, wait and job ports. Every
// guarantee of an objective lives in the store's own transactions: the kit drives the port directly, compares the
// whole record after each refusal, and names the first property a store breaks. The TypeScript twin is
// runStoreGoalConformance in packages/harness/storage/goalConformance.ts.
import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

// GoalStoreFactory builds the store under test. The kit hands it the store clock it moves; with a non-nil restart,
// the factory returns a new instance over the same durable state, as after the store's process restarted.
type GoalStoreFactory func(now func() time.Time, restart GoalRunStore) GoalRunStore

var goalKitKeys = []string{"kit.goal-a.Q7z2Xm9LpV4cR8tNb3Yh6Df1", "kit.goal-b.Hs8Pq3Lz6Wd1Rm9Tc4Vb7Nx2", "kit.goal-c.Ka5Ue2Jy8Gt3Wo6Bf9Mn1Sv4", "kit.goal-d.Zc3Ri7Oq1Xe5Pk9Ls2Gu6Ty8"}

const (
	goalKitWake   = "kit.goal-wake.Vx4Hd9Tn2Bq7Ym5Rc1Pz8Lf"
	goalKitQKey   = "kit.goal-q.Mb6Sj1Wg8Ku3Ne7Ax2Qo5Di"
	goalKitQWake  = "kit.goal-qw.Ct9Fh4Lp7Xr2Gv6Jz1Ys3Em"
	goalKitHour   = int64(3600000)
	goalKitMinute = time.Minute
)

type goalKit struct {
	ctx    context.Context
	at     time.Time
	create GoalStoreFactory
	store  GoalRunStore
	n      int
}

func (k *goalKit) now() time.Time { return k.at }
func (k *goalKit) iso() string    { return k.at.UTC().Format("2006-01-02T15:04:05.000Z") }
func (k *goalKit) check(property string, ok bool) {
	if !ok {
		panic(&StorageConformanceFailure{Property: property})
	}
}
func goalKitReason(err error) string {
	var goal *GoalRefused
	if errors.As(err, &goal) {
		return goal.Reason
	}
	return jobKitReason(err)
}
func (k *goalKit) refuses(property string, err error, reason string) {
	k.check(property, err != nil && goalKitReason(err) == reason)
}
func (k *goalKit) answers(property string, err error) { k.check(property, err == nil) }
func goalKitSame(a, b any) bool {
	x, errX := canonicalRaw(a)
	y, errY := canonicalRaw(b)
	return errX == nil && errY == nil && string(x) == string(y)
}
func goalKitRevision(revision int64) contract.Nullable[int64] {
	if revision == 0 {
		return contract.Nullable[int64]{Set: true, Null: true}
	}
	return contract.Nullable[int64]{Set: true, Value: revision}
}
func (k *goalKit) command(run, kind string, revision int64) GoalCommand {
	k.n++
	return GoalCommand{Version: 1, Resource: run, Command: fmt.Sprintf("%s-command-%d", run, k.n), Actor: "kit-person", ExpectedRevision: goalKitRevision(revision), Kind: kind}
}
func goalKitWith(c GoalCommand, edit func(*GoalCommand)) GoalCommand { edit(&c); return c }
func goalKitString(text string) *string                              { return &text }
func (k *goalKit) send(c GoalCommand, plan *GoalPlanSteps, refused *GoalFinding) (GoalAnswer, error) {
	return k.store.CommandGoal(k.ctx, GoalTransition{Version: 1, Command: c, Plan: plan, Refused: refused})
}
func (k *goalKit) read(run string) *RunGoal {
	held, err := k.store.ReadGoal(k.ctx, run)
	if err != nil {
		panic(err)
	}
	return held
}
func (k *goalKit) head(run string) GoalHead {
	held, err := k.store.Load(k.ctx, run)
	if err != nil || held == nil {
		panic(fmt.Errorf("no head: %w", err))
	}
	return GoalHead{Revision: held.Revision, Digest: CheckpointBytesDigest(held.Checkpoint)}
}
func (k *goalKit) open(run string) jobKitRun {
	head, err := k.store.Commit(k.ctx, RunCommit{RunID: run, CommitID: run + "-open", Checkpoint: []byte(`{"kit":"` + run + `"}`), Events: [][]byte{}})
	if err != nil {
		panic(err)
	}
	lease, err := k.store.AcquireLease(k.ctx, LeaseAcquire{Resource: run, Owner: "worker-a", ExpectedRevision: head.Revision, DurationMs: goalKitHour})
	if err != nil {
		panic(err)
	}
	return jobKitRun{revision: head.Revision, fence: FenceOf(lease)}
}

// journaled: a refusal journals the command and changes nothing else, the whole record compared. With no record,
// nothing is written.
func (k *goalKit) journaled(property string, c GoalCommand, err func() error, reason string, step string) {
	before := k.read(c.Resource)
	k.refuses(property, err(), reason)
	after := k.read(c.Resource)
	if before == nil {
		k.check(property, after == nil)
		return
	}
	k.check(property, after != nil && len(after.Journal) > 0)
	last := after.Journal[len(after.Journal)-1]
	rest := *after
	rest.Journal = after.Journal[:len(after.Journal)-1]
	held := ""
	if last.Step != nil {
		held = *last.Step
	}
	k.check(property, goalKitSame(rest, *before) && last.Command == c.Command && last.Kind == c.Kind && last.Outcome == reason && last.Revision == before.Revision && last.At == k.iso() && held == step)
}

// untouched: a refusal that writes nothing at all.
func (k *goalKit) untouched(property, run string, err func() error, reason string) {
	before := k.read(run)
	k.refuses(property, err(), reason)
	k.check(property, goalKitSame(k.read(run), before))
}
func (k *goalKit) current(run string) GoalObjective {
	held := k.read(run)
	return held.Objectives[len(held.Objectives)-1]
}
func (k *goalKit) register(run string, revision *string, fence LeaseFence, operation, key string) string {
	digest, _ := JobKeyDigest(key)
	receipt, err := k.store.CommitFenced(k.ctx, RunCommit{RunID: run, ExpectedRevision: *revision, CommitID: run + "-" + operation, Checkpoint: []byte(`{"kit":"` + operation + `"}`), Events: [][]byte{}, Job: &JobRegistration{Job: JobIdentity(run, operation), Operation: operation, Source: "kit-source", Tool: "kit-start", RequestDigest: "sha256:" + strings.Repeat("a", 64), KeyDigest: digest}}, fence)
	if err != nil {
		panic(err)
	}
	*revision = receipt.Revision
	return JobIdentity(run, operation)
}
func (k *goalKit) started(run, job string, fence LeaseFence) {
	handle := "kit-handle"
	if _, err := k.store.AnswerJob(k.ctx, JobAnswer{Version: 1, Resource: run, Job: job, Generation: 1, Answer: "started", Handle: &handle}, fence); err != nil {
		panic(err)
	}
}
func (k *goalKit) parkOn(run, job, wake, revision string) {
	digest, _ := WakeKeyDigest(wake)
	park := RunWait{Version: 1, Resource: run, Wait: "wait-1", Reason: "job", Pending: contract.RunWaitPending{Job: &contract.RunWaitPendingJob{Kind: "job", Job: job, Generation: 1}}, Condition: WaitCondition{Event: &contract.RunWaitEventCondition{Kind: JobWaitEvent, Subject: job}}, Continuation: "k", Checkpoint: contract.RunWaitCheckpointBinding{Revision: revision, Digest: CheckpointBytesDigest([]byte(`{"kit":"op-1"}`))}, Pins: jobKitPins, WakeKeyDigest: digest, Release: []WaitRelease{}}
	if _, _, err := k.store.ParkWait(k.ctx, park, k.fenceOf(run)); err != nil {
		panic(err)
	}
}
func (k *goalKit) fenceOf(run string) LeaseFence {
	lease, err := k.store.ReadLease(k.ctx, run)
	if err != nil || lease == nil {
		panic(fmt.Errorf("no lease: %w", err))
	}
	return FenceOf(*lease)
}
func goalKitClaim(run, revision string) WaitClaim {
	pins, _ := PinsDigest(jobKitPins)
	return WaitClaim{Resource: run, Wait: "wait-1", Evidence: "done-1", Lease: LeaseAcquire{Resource: run, Owner: "worker-b", ExpectedRevision: revision, DurationMs: goalKitHour}, Reconciled: []WaitRelease{}, Revalidation: Revalidation{Version: 1, Resource: run, Wait: "wait-1", Pins: pins, Evidence: "sha256:" + strings.Repeat("e", 64), Changes: []contract.RunResumeChange{}}}
}

var goalKitPlan = GoalPlanSteps{ID: "kit-plan", Revision: "sha256:" + strings.Repeat("c", 64), Steps: []GoalStep{{Step: "step-1", Index: 0}, {Step: "step-2", Index: 1}}}

func goalKitFirstStep(plan GoalPlanSteps) *GoalPlanSteps {
	plan.Steps = plan.Steps[:1]
	return &plan
}

func (k *goalKit) properties() {
	k.store = k.create(k.now, nil)
	k.check("goal.declared", k.store.GoalVersion() == 1 && (k.store.GoalLifetime() == GoalLifetimeDurable || k.store.GoalLifetime() == GoalLifetimeProcess) && k.store.JobVersion() == 1 && k.store.WaitVersion() == 1 && k.store.LeaseVersion() == 1)
	restart := func(property string) {
		next := k.create(k.now, k.store)
		k.check(property, next != k.store)
		k.store = next
	}
	stamp := func(c GoalCommand) GoalStamp { return GoalStamp{Command: c.Command, Actor: "kit-person", At: k.iso()} }

	// Create: from none, under the store's clock and the command's actor; the record and the answer agree.
	p := k.open("run-p")
	first := goalKitWith(k.command("run-p", "create", 0), func(c *GoalCommand) { c.Objective = goalKitString("objective-1") })
	created, err := k.send(first, nil, nil)
	k.answers("goal.create.commits", err)
	read := k.read("run-p")
	k.check("goal.create.commits", created.Goal.Revision == 1 && len(created.Goal.Objectives) == 1 && goalKitSame(created.Goal.Objectives[0], GoalObjective{ID: "objective-1", State: "active", Created: stamp(first)}) && created.Entry.Outcome == "accepted" && created.Entry.Revision == 1 && read != nil && goalKitSame(*read, created.Goal) && goalKitSame(read.Journal, []GoalEntry{created.Entry}))
	stale := goalKitWith(k.command("run-p", "create", 0), func(c *GoalCommand) { c.Objective = goalKitString("objective-2") })
	k.journaled("goal.create.cas", stale, func() error { _, err := k.send(stale, nil, nil); return err }, "revision-changed", "")
	twice := goalKitWith(k.command("run-p", "create", 1), func(c *GoalCommand) { c.Objective = goalKitString("objective-2") })
	k.journaled("goal.create.exists", twice, func() error { _, err := k.send(twice, nil, nil); return err }, "objective-exists", "")
	// A repeat answers as the first did; another command under its id is conflict; a refused one is refused again.
	// Neither grows the journal.
	length := len(k.read("run-p").Journal)
	again, err := k.send(first, nil, nil)
	k.answers("goal.repeat", err)
	k.check("goal.repeat", goalKitSame(again.Entry, created.Entry) && len(k.read("run-p").Journal) == length)
	k.untouched("goal.conflict", "run-p", func() error {
		_, err := k.send(goalKitWith(first, func(c *GoalCommand) { c.Objective = goalKitString("objective-9") }), nil, nil)
		return err
	}, "conflict")
	k.untouched("goal.refusal.repeat", "run-p", func() error { _, err := k.send(stale, nil, nil); return err }, "revision-changed")
	// An objective's run is leased: an unfenced commit is refused once it has one, and only then.
	before, _ := k.store.Load(k.ctx, "run-p")
	_, err = k.store.Commit(k.ctx, RunCommit{RunID: "run-p", ExpectedRevision: before.Revision, CommitID: "run-p-unfenced", Checkpoint: []byte(`{"kit":"unfenced"}`), Events: [][]byte{}})
	k.refuses("goal.unfenced", err, "lease-required")
	still, _ := k.store.Load(k.ctx, "run-p")
	k.check("goal.unfenced", still.Revision == before.Revision)
	_, err = k.store.Commit(k.ctx, RunCommit{RunID: "run-plain", CommitID: "run-plain-1", Checkpoint: []byte(`{"kit":"plain"}`), Events: [][]byte{}})
	k.check("goal.unfenced", err == nil)

	// M8: a pause moves a disposition; it refuses only an acquisition and a resume, and its answer reports the held
	// lease.
	pause := k.command("run-p", "pause", 1)
	paused, err := k.send(pause, nil, nil)
	k.answers("goal.pause.commits", err)
	pausedStamp := stamp(pause)
	k.check("goal.pause.commits", paused.Goal.Revision == 2 && paused.Goal.Objectives[0].State == "active" && paused.Goal.Objectives[0].Paused != nil && goalKitSame(*paused.Goal.Objectives[0].Paused, pausedStamp) && paused.Lease != nil && paused.Lease.Owner == "worker-a" && paused.Lease.Epoch == p.fence.Epoch && paused.Lease.State == "held")
	pauseTwice := k.command("run-p", "pause", 2)
	k.journaled("goal.pause.twice", pauseTwice, func() error { _, err := k.send(pauseTwice, nil, nil); return err }, "objective-paused", "")
	held, err := k.store.CommitFenced(k.ctx, RunCommit{RunID: "run-p", ExpectedRevision: before.Revision, CommitID: "run-p-paused", Checkpoint: []byte(`{"kit":"paused"}`), Events: [][]byte{}}, p.fence)
	k.answers("goal.pause.held", err)
	_, err = k.store.RenewLease(k.ctx, p.fence, goalKitHour)
	k.answers("goal.pause.held", err)
	if _, err := k.store.ReleaseLease(k.ctx, p.fence); err != nil {
		panic(err)
	}
	lease, _ := k.store.ReadLease(k.ctx, "run-p")
	_, err = k.store.AcquireLease(k.ctx, LeaseAcquire{Resource: "run-p", Owner: "worker-b", ExpectedRevision: held.Revision, DurationMs: goalKitHour})
	k.refuses("goal.pause.acquire", err, "objective-paused")
	after, _ := k.store.ReadLease(k.ctx, "run-p")
	k.check("goal.pause.acquire", goalKitSame(after, lease))
	// A resume writes the disposition and nothing else: no head, lease, wait or job moves.
	resume := k.command("run-p", "resume", 2)
	heads, _ := k.store.Load(k.ctx, "run-p")
	jobs, _ := k.store.ExportJobs(k.ctx, "run-p")
	waits, _ := k.store.ExportWaits(k.ctx, "run-p")
	resumed, err := k.send(resume, nil, nil)
	k.answers("goal.resume.commits", err)
	headsAfter, _ := k.store.Load(k.ctx, "run-p")
	leaseAfter, _ := k.store.ReadLease(k.ctx, "run-p")
	jobsAfter, _ := k.store.ExportJobs(k.ctx, "run-p")
	waitsAfter, _ := k.store.ExportWaits(k.ctx, "run-p")
	k.check("goal.resume.commits", resumed.Goal.Revision == 3 && resumed.Goal.Objectives[0].Paused == nil && headsAfter.Revision == heads.Revision && goalKitSame(leaseAfter, lease) && goalKitSame(jobsAfter, jobs) && goalKitSame(waitsAfter, waits))
	notPaused := k.command("run-p", "resume", 3)
	k.journaled("goal.resume.none", notPaused, func() error { _, err := k.send(notPaused, nil, nil); return err }, "not-paused", "")
	_, err = k.store.AcquireLease(k.ctx, LeaseAcquire{Resource: "run-p", Owner: "worker-b", ExpectedRevision: held.Revision, DurationMs: goalKitHour})
	k.answers("goal.resume.commits", err)
	// Blocked is a state, and the only way back is unblock.
	block := goalKitWith(k.command("run-p", "block", 3), func(c *GoalCommand) { c.Condition = goalKitString("kit-condition") })
	blocked, err := k.send(block, nil, nil)
	k.answers("goal.block", err)
	blockStamp := stamp(block)
	k.check("goal.block", blocked.Goal.Objectives[0].State == "blocked" && blocked.Goal.Objectives[0].Blocked != nil && goalKitSame(*blocked.Goal.Objectives[0].Blocked, GoalBlock{Command: blockStamp.Command, Actor: blockStamp.Actor, At: blockStamp.At, Condition: "kit-condition"}))
	blockTwice := goalKitWith(k.command("run-p", "block", 4), func(c *GoalCommand) { c.Condition = goalKitString("kit-condition") })
	k.journaled("goal.block.twice", blockTwice, func() error { _, err := k.send(blockTwice, nil, nil); return err }, "objective-blocked", "")
	headP := k.head("run-p")
	blockedAchieve := goalKitWith(k.command("run-p", "achieve", 4), func(c *GoalCommand) { c.Head = &headP })
	k.journaled("goal.block.achieve", blockedAchieve, func() error { _, err := k.send(blockedAchieve, nil, nil); return err }, "objective-blocked", "")
	unblocked, err := k.send(k.command("run-p", "unblock", 4), nil, nil)
	k.answers("goal.unblock", err)
	k.check("goal.unblock", unblocked.Goal.Objectives[0].State == "active" && unblocked.Goal.Objectives[0].Blocked == nil)
	unblockTwice := k.command("run-p", "unblock", 5)
	k.journaled("goal.unblock.none", unblockTwice, func() error { _, err := k.send(unblockTwice, nil, nil); return err }, "not-blocked", "")
	unbound := goalKitWith(k.command("run-p", "achieve", 5), func(c *GoalCommand) { c.Head = &headP })
	k.journaled("goal.achieve.unbound", unbound, func() error { _, err := k.send(unbound, nil, nil); return err }, "plan-unbound", "")
	// Bind: against the head as the store holds it; a finding of the package is journaled as the refusal; once only.
	oldHead := GoalHead{Revision: before.Revision, Digest: CheckpointBytesDigest([]byte(`{"kit":"run-p"}`))}
	staleHead := goalKitWith(k.command("run-p", "bind", 5), func(c *GoalCommand) { c.Steps = []string{"step-1", "step-2"}; c.Head = &oldHead })
	k.journaled("goal.bind.head", staleHead, func() error { _, err := k.send(staleHead, &goalKitPlan, nil); return err }, "head-changed", "")
	settled := goalKitWith(k.command("run-p", "bind", 5), func(c *GoalCommand) { c.Steps = []string{"step-1", "step-2"}; c.Head = &headP })
	k.journaled("goal.bind.finding", settled, func() error {
		_, err := k.send(settled, nil, &GoalFinding{Reason: "step-settled", Step: goalKitString("step-1")})
		return err
	}, "step-settled", "step-1")
	bind := goalKitWith(k.command("run-p", "bind", 5), func(c *GoalCommand) { c.Steps = []string{"step-1", "step-2"}; c.Head = &headP })
	bound, err := k.send(bind, &goalKitPlan, nil)
	k.answers("goal.bind.commits", err)
	k.check("goal.bind.commits", bound.Goal.Revision == 6 && bound.Goal.Objectives[0].Plan != nil && goalKitSame(*bound.Goal.Objectives[0].Plan, GoalPlan{ID: goalKitPlan.ID, Revision: goalKitPlan.Revision, Steps: goalKitPlan.Steps, Bound: stamp(bind)}))
	bindTwice := goalKitWith(k.command("run-p", "bind", 6), func(c *GoalCommand) { c.Steps = []string{"step-1"}; c.Head = &headP })
	k.journaled("goal.bind.twice", bindTwice, func() error { _, err := k.send(bindTwice, &goalKitPlan, nil); return err }, "plan-bound", "")
	// Achieve: against the head, after the package's finding, with nothing waiting and every job settled.
	achieveStale := goalKitWith(k.command("run-p", "achieve", 6), func(c *GoalCommand) { c.Head = &oldHead })
	k.journaled("goal.achieve.head", achieveStale, func() error { _, err := k.send(achieveStale, nil, nil); return err }, "head-changed", "")
	achieveFound := goalKitWith(k.command("run-p", "achieve", 6), func(c *GoalCommand) { c.Head = &headP })
	k.journaled("goal.achieve.finding", achieveFound, func() error {
		_, err := k.send(achieveFound, nil, &GoalFinding{Reason: "evidence-refused", Step: goalKitString("step-2")})
		return err
	}, "evidence-refused", "step-2")
	achieve := goalKitWith(k.command("run-p", "achieve", 6), func(c *GoalCommand) { c.Head = &headP })
	achieved, err := k.send(achieve, nil, nil)
	k.answers("goal.achieve.commits", err)
	achieveStamp := stamp(achieve)
	k.check("goal.achieve.commits", achieved.Goal.Objectives[0].State == "achieved" && achieved.Goal.Objectives[0].Achieved != nil && goalKitSame(*achieved.Goal.Objectives[0].Achieved, GoalAchievement{Command: achieveStamp.Command, Actor: achieveStamp.Actor, At: achieveStamp.At, Head: headP}))
	terminal := k.command("run-p", "pause", 7)
	k.journaled("goal.terminal", terminal, func() error { _, err := k.send(terminal, nil, nil); return err }, "objective-terminal", "")
	// M11: a later objective of the run never binds a Plan an earlier one bound.
	reused := goalKitWith(k.command("run-p", "create", 7), func(c *GoalCommand) { c.Objective = goalKitString("objective-1") })
	k.journaled("goal.create.reused", reused, func() error { _, err := k.send(reused, nil, nil); return err }, "objective-reused", "")
	second, err := k.send(goalKitWith(k.command("run-p", "create", 7), func(c *GoalCommand) { c.Objective = goalKitString("objective-2") }), nil, nil)
	k.answers("goal.create.terminal", err)
	k.check("goal.create.terminal", len(second.Goal.Objectives) == 2 && second.Goal.Objectives[0].State == "achieved" && second.Goal.Objectives[1].State == "active")
	rebind := goalKitWith(k.command("run-p", "bind", 8), func(c *GoalCommand) { c.Steps = []string{"step-1"}; c.Head = &headP })
	k.journaled("goal.bind.reused", rebind, func() error { _, err := k.send(rebind, goalKitFirstStep(goalKitPlan), nil); return err }, "plan-reused", "")

	// Jobs at an achievement, two of them and a contradiction: unsettled ones block unless waived; failed ones are
	// listed.
	j := k.open("run-j")
	_, err = k.send(goalKitWith(k.command("run-j", "create", 0), func(c *GoalCommand) { c.Objective = goalKitString("objective-j") }), nil, nil)
	k.answers("goal.achieve.jobs", err)
	revision := j.revision
	running := k.register("run-j", &revision, j.fence, "op-1", goalKitKeys[0])
	failed := k.register("run-j", &revision, j.fence, "op-2", goalKitKeys[1])
	contradicted := k.register("run-j", &revision, j.fence, "op-3", goalKitKeys[2])
	k.started("run-j", running, j.fence)
	k.started("run-j", failed, j.fence)
	if _, err := k.store.CompleteJob(k.ctx, jobKitComplete("run-j", failed, goalKitKeys[1], "done-1", "failed")); err != nil {
		panic(err)
	}
	if _, err := k.store.ReconcileJob(k.ctx, jobKitReconcile("run-j", contradicted, goalKitKeys[2], "find-1", "not-started")); err != nil {
		panic(err)
	}
	if _, err := k.store.CompleteJob(k.ctx, jobKitComplete("run-j", contradicted, goalKitKeys[2], "done-1", "succeeded")); err != nil {
		panic(err)
	}
	jobHead := k.head("run-j")
	_, err = k.send(goalKitWith(k.command("run-j", "bind", 1), func(c *GoalCommand) { c.Steps = []string{"step-1"}; c.Head = &jobHead }), goalKitFirstStep(goalKitPlan), nil)
	k.answers("goal.achieve.jobs", err)
	unsettled := goalKitWith(k.command("run-j", "achieve", 2), func(c *GoalCommand) { c.Head = &jobHead })
	k.journaled("goal.achieve.jobs", unsettled, func() error { _, err := k.send(unsettled, nil, nil); return err }, "jobs-unsettled", "")
	waivedSettled := goalKitWith(k.command("run-j", "achieve", 2), func(c *GoalCommand) { c.Head = &jobHead; c.Waive = []string{failed} })
	k.journaled("goal.achieve.waiver", waivedSettled, func() error { _, err := k.send(waivedSettled, nil, nil); return err }, "waiver-invalid", "")
	waived := goalKitWith(k.command("run-j", "achieve", 2), func(c *GoalCommand) { c.Head = &jobHead; c.Waive = []string{running} })
	done, err := k.send(waived, nil, nil)
	k.answers("goal.achieve.jobs", err)
	waivedStamp := stamp(waived)
	yes, failedOutcome := true, "failed"
	k.check("goal.achieve.jobs", done.Goal.Objectives[0].Achieved != nil && goalKitSame(*done.Goal.Objectives[0].Achieved, GoalAchievement{Command: waivedStamp.Command, Actor: waivedStamp.Actor, At: waivedStamp.At, Head: jobHead, Waived: []string{running}, Unsuccessful: []GoalJob{{Job: failed, State: "completed", Outcome: &failedOutcome}, {Job: contradicted, State: "not-started", Contradicted: &yes}}}))
	// Owner: a record is looked up by its run.
	k.check("goal.owner", k.read("run-other") == nil && k.read("run-j").Objectives[0].ID == "objective-j")

	// Waits: an achievement waits for none; an abandon records what it left; a later create is held to it (M10).
	w := k.open("run-w")
	_, err = k.send(goalKitWith(k.command("run-w", "create", 0), func(c *GoalCommand) { c.Objective = goalKitString("objective-w") }), nil, nil)
	k.answers("goal.achieve.wait", err)
	wRevision := w.revision
	waitJob := k.register("run-w", &wRevision, w.fence, "op-1", goalKitKeys[3])
	k.started("run-w", waitJob, w.fence)
	k.parkOn("run-w", waitJob, goalKitWake, wRevision)
	wHead := k.head("run-w")
	wPlan := goalKitPlan
	wPlan.ID = "kit-plan-w"
	_, err = k.send(goalKitWith(k.command("run-w", "bind", 1), func(c *GoalCommand) { c.Steps = []string{"step-1"}; c.Head = &wHead }), &wPlan, nil)
	k.answers("goal.achieve.wait", err)
	waiting := goalKitWith(k.command("run-w", "achieve", 2), func(c *GoalCommand) { c.Head = &wHead; c.Waive = []string{waitJob} })
	k.journaled("goal.achieve.wait", waiting, func() error { _, err := k.send(waiting, nil, nil); return err }, "wait-pending", "")
	abandon := k.command("run-w", "abandon", 2)
	abandoned, err := k.send(abandon, nil, nil)
	k.answers("goal.abandon.left", err)
	abandonStamp := stamp(abandon)
	k.check("goal.abandon.left", abandoned.Goal.Objectives[0].State == "abandoned" && abandoned.Goal.Objectives[0].Abandoned != nil && goalKitSame(*abandoned.Goal.Objectives[0].Abandoned, GoalAbandonment{Command: abandonStamp.Command, Actor: abandonStamp.Actor, At: abandonStamp.At, Waits: []GoalLeftWait{{Wait: "wait-1", Reason: "job", State: "parked"}}, Jobs: []GoalJob{{Job: waitJob, State: "started"}}}))
	unresolved := goalKitWith(k.command("run-w", "create", 3), func(c *GoalCommand) { c.Objective = goalKitString("objective-w2") })
	k.journaled("goal.create.unresolved", unresolved, func() error { _, err := k.send(unresolved, nil, nil); return err }, "wait-unresolved", "")
	// The abandon's deliveries are journaled on the objective that command abandoned, and only there.
	deliveries := []GoalDelivery{{Job: waitJob, Delivery: "sent"}}
	delivered, err := k.store.RecordGoalDeliveries(k.ctx, GoalDeliveries{Version: 1, Resource: "run-w", Command: abandon.Command, Deliveries: deliveries})
	k.answers("goal.deliveries", err)
	k.check("goal.deliveries", len(delivered.Journal) > 0)
	last := delivered.Journal[len(delivered.Journal)-1]
	k.check("goal.deliveries", last.Outcome == "delivered" && last.Kind == "abandon" && last.Command == abandon.Command && goalKitSame(last.Deliveries, deliveries) && delivered.Revision == 3)
	k.untouched("goal.deliveries.other", "run-w", func() error {
		_, err := k.store.RecordGoalDeliveries(k.ctx, GoalDeliveries{Version: 1, Resource: "run-w", Command: "run-w-other", Deliveries: deliveries})
		return err
	}, "invalid")
	// The wait resolved, the create goes through.
	if _, err := k.store.CompleteJob(k.ctx, jobKitComplete("run-w", waitJob, goalKitKeys[3], "done-1", "succeeded")); err != nil {
		panic(err)
	}
	_, _, err = k.store.ResumeWait(k.ctx, goalKitClaim("run-w", wRevision))
	k.answers("goal.create.unresolved", err)
	recreated, err := k.send(goalKitWith(k.command("run-w", "create", 3), func(c *GoalCommand) { c.Objective = goalKitString("objective-w2") }), nil, nil)
	k.answers("goal.create.unresolved", err)
	k.check("goal.create.unresolved", len(recreated.Goal.Objectives) == 2 && recreated.Goal.Objectives[1].State == "active")

	// A paused run is resumed by no claim either.
	q := k.open("run-q")
	_, err = k.send(goalKitWith(k.command("run-q", "create", 0), func(c *GoalCommand) { c.Objective = goalKitString("objective-q") }), nil, nil)
	k.answers("goal.pause.resume-wait", err)
	qRevision := q.revision
	qJob := k.register("run-q", &qRevision, q.fence, "op-1", goalKitQKey)
	k.started("run-q", qJob, q.fence)
	k.parkOn("run-q", qJob, goalKitQWake, qRevision)
	if _, err := k.store.CompleteJob(k.ctx, jobKitComplete("run-q", qJob, goalKitQKey, "done-1", "succeeded")); err != nil {
		panic(err)
	}
	_, err = k.send(k.command("run-q", "pause", 1), nil, nil)
	k.answers("goal.pause.resume-wait", err)
	wait, _ := k.store.ReadWait(k.ctx, "run-q", "wait-1")
	_, _, err = k.store.ResumeWait(k.ctx, goalKitClaim("run-q", qRevision))
	k.refuses("goal.pause.resume-wait", err, "objective-paused")
	waitAfter, _ := k.store.ReadWait(k.ctx, "run-q", "wait-1")
	k.check("goal.pause.resume-wait", goalKitSame(waitAfter, wait) && wait != nil && wait.State == "woken")

	// No record: a command is refused and nothing is written; an inspection reads nothing.
	k.untouched("goal.none", "run-none", func() error { _, err := k.send(k.command("run-none", "pause", 0), nil, nil); return err }, "no-objective")
	k.untouched("goal.none", "run-none", func() error { _, err := k.send(k.command("run-none", "pause", 1), nil, nil); return err }, "revision-changed")
	none, err := k.store.InspectGoal(k.ctx, GoalInspect{Version: 1, Resource: "run-none", Command: "look-0", Actor: "kit-viewer"})
	k.check("goal.inspect.none", err == nil && none == nil && k.read("run-none") == nil)
	// M12: an inspection is counted, coalesced into its last row, and moves no revision.
	looked := k.read("run-p")
	for _, id := range []string{"look-1", "look-1"} {
		if _, err := k.store.InspectGoal(k.ctx, GoalInspect{Version: 1, Resource: "run-p", Command: id, Actor: "kit-viewer"}); err != nil {
			panic(err)
		}
	}
	k.at = k.at.Add(goalKitMinute)
	inspected, err := k.store.InspectGoal(k.ctx, GoalInspect{Version: 1, Resource: "run-p", Command: "look-2", Actor: "kit-viewer"})
	k.answers("goal.inspect.count", err)
	k.check("goal.inspect.count", inspected != nil && inspected.Inspections != nil && goalKitSame(*inspected.Inspections, GoalInspections{Count: 2, Last: contract.RunGoalInspectionsLast{Command: "look-2", Actor: "kit-viewer", At: k.iso()}}) && inspected.Revision == looked.Revision && goalKitSame(inspected.Journal, looked.Journal) && goalKitSame(*k.read("run-p"), *inspected))
	// M12: past the journal's bound, refusals go first, and no command is refused for size.
	m := k.open("run-m")
	mFirst, err := k.send(goalKitWith(k.command("run-m", "create", 0), func(c *GoalCommand) { c.Objective = goalKitString("objective-m") }), nil, nil)
	k.answers("goal.journal.bound", err)
	for i := 0; i < GoalJournalLimit+6; i++ {
		_, err := k.send(k.command("run-m", "resume", 1), nil, nil)
		k.refuses("goal.journal.bound", err, "not-paused")
	}
	full := k.read("run-m")
	k.check("goal.journal.bound", len(full.Journal) == GoalJournalLimit && full.Discarded != nil && *full.Discarded == 7 && goalKitSame(full.Journal[0], mFirst.Entry))
	mPause, err := k.send(k.command("run-m", "pause", 1), nil, nil)
	k.answers("goal.journal.bound", err)
	k.check("goal.journal.bound", len(mPause.Goal.Journal) == GoalJournalLimit && mPause.Goal.Discarded != nil && *mPause.Goal.Discarded == 8 && goalKitSame(mPause.Goal.Journal[0], mFirst.Entry) && goalKitSame(mPause.Goal.Journal[len(mPause.Goal.Journal)-1], mPause.Entry))
	if _, err := k.store.ReleaseLease(k.ctx, m.fence); err != nil {
		panic(err)
	}
	// A run's objectives are bounded, and the bound is a named refusal.
	k.open("run-x")
	xRevision := int64(0)
	for i := 0; i < GoalObjectiveLimit; i++ {
		made, err := k.send(goalKitWith(k.command("run-x", "create", xRevision), func(c *GoalCommand) { c.Objective = goalKitString(fmt.Sprintf("objective-x%d", i)) }), nil, nil)
		k.answers("goal.exhausted", err)
		gone, err := k.send(k.command("run-x", "abandon", made.Goal.Revision), nil, nil)
		k.answers("goal.exhausted", err)
		xRevision = gone.Goal.Revision
	}
	exhausted := goalKitWith(k.command("run-x", "create", xRevision), func(c *GoalCommand) { c.Objective = goalKitString("objective-x-last") })
	k.journaled("goal.exhausted", exhausted, func() error { _, err := k.send(exhausted, nil, nil); return err }, "goals-exhausted", "")

	// A restart keeps every record, every disposition and every rule.
	records := map[string]*RunGoal{}
	for _, run := range []string{"run-p", "run-j", "run-w", "run-q", "run-m", "run-x"} {
		records[run] = k.read(run)
	}
	restart("goal.restart.instance")
	for run, record := range records {
		k.check("goal.restart.record", goalKitSame(k.read(run), record))
	}
	mHead, _ := k.store.Load(k.ctx, "run-m")
	_, err = k.store.AcquireLease(k.ctx, LeaseAcquire{Resource: "run-m", Owner: "worker-z", ExpectedRevision: mHead.Revision, DurationMs: goalKitHour})
	k.refuses("goal.restart.paused", err, "objective-paused")
	_, err = k.store.Commit(k.ctx, RunCommit{RunID: "run-m", ExpectedRevision: mHead.Revision, CommitID: "run-m-unfenced", Checkpoint: []byte(`{"kit":"unfenced"}`), Events: [][]byte{}})
	k.refuses("goal.restart.unfenced", err, "lease-required")
	repeated, err := k.send(waived, nil, nil)
	k.answers("goal.restart.repeat", err)
	k.check("goal.restart.repeat", goalKitSame(repeated.Entry, done.Entry))
	k.untouched("goal.restart.conflict", "run-j", func() error {
		_, err := k.send(goalKitWith(waived, func(c *GoalCommand) { c.Waive = []string{failed} }), nil, nil)
		return err
	}, "conflict")
	resumedAfter, err := k.send(k.command("run-m", "resume", 2), nil, nil)
	k.answers("goal.restart.commits", err)
	k.check("goal.restart.commits", resumedAfter.Goal.Revision == 3 && resumedAfter.Goal.Objectives[0].Paused == nil)
	_, err = k.store.AcquireLease(k.ctx, LeaseAcquire{Resource: "run-m", Owner: "worker-z", ExpectedRevision: mHead.Revision, DurationMs: goalKitHour})
	k.answers("goal.restart.commits", err)
}

// CheckRunStoreGoalConformance runs the goal properties against a store kind. A store whose records end with its
// process fails the restart properties.
func CheckRunStoreGoalConformance(ctx context.Context, create GoalStoreFactory) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			if failure, ok := recovered.(*StorageConformanceFailure); ok {
				err = failure
				return
			}
			err = &StorageConformanceFailure{Property: "goal.operation"}
		}
	}()
	kit := &goalKit{ctx: ctx, at: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), create: create}
	kit.properties()
	return nil
}
