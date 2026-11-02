package runcontext

// #229: the job conformance kit, for a host store kind's job port beside its lease and wait ports. Every guarantee
// of a job lives in the store's own transactions, and the package re-checks only what it reads back: the kit drives
// the port directly and names the first property a store breaks. The TypeScript twin is runStoreJobConformance in
// packages/harness/storage/jobConformance.ts.
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

// JobStoreFactory builds the store under test. The kit hands it the store clock it moves; with a non-nil restart,
// the factory returns a new instance over the same durable state, as after the store's process restarted.
type JobStoreFactory func(now func() time.Time, restart JobRunStore) JobRunStore

var jobKitKeys = []string{"kit.job-a.Q7z2Xm9LpV4cR8tNb3Yh6Df1", "kit.job-b.Hs8Pq3Lz6Wd1Rm9Tc4Vb7Nx2", "kit.job-c.Ka5Ue2Jy8Gt3Wo6Bf9Mn1Sv4", "kit.job-d.Zc3Ri7Oq1Xe5Pk9Ls2Gu6Ty8", "kit.job-e.Vx4Hd9Tn2Bq7Ym5Rc1Pz8Lf", "kit.job-f.Mb6Sj1Wg8Ku3Ne7Ax2Qo5Di", "kit.job-g.Ct9Fh4Lp7Xr2Gv6Jz1Ys3Em", "kit.job-h.Ur5Ib8Oa3Tk1Nw6Pd9Hc2Vl", "kit.job-i.Ge7Zq2My5Rf4Xs9Ku1Bn6Ta", "kit.job-j.Ld3Pv8Hj6Cw1Oi4Se7Gx9Fm"}
var jobKitWakes = []string{"kit.wake-a.Q7z2Xm9LpV4cR8tNb3Yh6Df1", "kit.wake-b.Hs8Pq3Lz6Wd1Rm9Tc4Vb7Nx2", "kit.wake-c.Ka5Ue2Jy8Gt3Wo6Bf9Mn1Sv4", "kit.wake-d.Zc3Ri7Oq1Xe5Pk9Ls2Gu6Ty8", "kit.wake-f.Mb6Jt1Cw8Qa3Ne5Xk2Hr9Ug7"}

const jobKitWrong = "kit.job-wrong.Ge7Zq2My5Rf4Xs9Ku1Bn6Ta"

type jobKit struct {
	ctx     context.Context
	at      time.Time
	create  JobStoreFactory
	store   JobRunStore
	touched []string
}

func (k *jobKit) check(property string, ok bool) {
	if !ok {
		panic(&StorageConformanceFailure{Property: property})
	}
}
func jobKitReason(err error) string {
	var job *JobRefused
	var lease *LeaseRefused
	var wait *WaitRefused
	switch {
	case errors.As(err, &job):
		return job.Reason
	case errors.As(err, &lease):
		return lease.Reason
	case errors.As(err, &wait):
		return wait.Reason
	}
	return ""
}
func (k *jobKit) refuses(property string, err error, reason string) {
	k.check(property, err != nil && jobKitReason(err) == reason)
}

// answers: what must answer: a refusal or an error here is that property broken, whatever it says.
func (k *jobKit) answers(property string, err error) { k.check(property, err == nil) }

func (k *jobKit) sameJob(a, b RunJob) bool {
	x, _ := canonicalRaw(a)
	y, _ := canonicalRaw(b)
	return string(x) == string(y)
}

// jobKitKey is a fresh printable secret for its number.
func jobKitKey(n int) string { return fmt.Sprintf("kit.limit.%03d.Q7z2Xm9LpV4cR8tNb3Yh6Df1", n) }

type jobKitRun struct {
	revision string
	fence    LeaseFence
}

func (k *jobKit) open(run string) jobKitRun {
	k.touched = append(k.touched, run)
	head, err := k.store.Commit(k.ctx, RunCommit{RunID: run, CommitID: run + "-open", Checkpoint: []byte(`{"kit":"job"}`), Events: [][]byte{}})
	if err != nil {
		panic(err)
	}
	lease, err := k.store.AcquireLease(k.ctx, LeaseAcquire{Resource: run, Owner: "worker-a", ExpectedRevision: head.Revision, DurationMs: 3600000})
	if err != nil {
		panic(err)
	}
	return jobKitRun{revision: head.Revision, fence: FenceOf(lease)}
}
func (k *jobKit) claim(run, revision, operation, key string) RunCommit {
	digest, _ := JobKeyDigest(key)
	return RunCommit{RunID: run, ExpectedRevision: revision, CommitID: run + "-" + operation, Checkpoint: []byte(`{"kit":"` + operation + `"}`), Events: [][]byte{}, Job: &JobRegistration{Job: JobIdentity(run, operation), Operation: operation, Source: "kit-source", Tool: "kit-start", RequestDigest: "sha256:" + strings.Repeat("a", 64), KeyDigest: digest}}
}
func (k *jobKit) register(run, operation, key string, held jobKitRun) (string, string) {
	receipt, err := k.store.CommitFenced(k.ctx, k.claim(run, held.revision, operation, key), held.fence)
	k.answers("job.register.commits", err)
	return JobIdentity(run, operation), receipt.Revision
}
func jobKitAnswer(run, job, handle string) JobAnswer {
	return JobAnswer{Version: 1, Resource: run, Job: job, Generation: 1, Answer: "started", Handle: &handle}
}
func jobKitComplete(run, job, key, evidence, outcome string) JobCompletionEvidence {
	return JobCompletionEvidence{Version: 1, Resource: run, Job: job, Generation: 1, Evidence: evidence, Outcome: outcome, Key: key}
}
func jobKitReconcile(run, job, key, evidence, finding string) JobReconciliation {
	return JobReconciliation{Version: 1, Resource: run, Job: job, Generation: 1, Evidence: evidence, Finding: finding, Key: key}
}

var jobKitPins = func() ResumePins {
	var pins ResumePins
	_ = json.Unmarshal([]byte(`{"model":{"provider":"kit","model":"kit"},"policy":"sha256:`+strings.Repeat("b", 64)+`","sources":[],"artifacts":[]}`), &pins)
	return pins
}()

func (k *jobKit) park(run, job, wake string, timeoutAt *string) RunWait {
	head, err := k.store.Load(k.ctx, run)
	if err != nil || head == nil {
		panic(fmt.Errorf("no head: %w", err))
	}
	digest, _ := WakeKeyDigest(wake)
	return RunWait{Version: 1, Resource: run, Wait: "wait-1", Reason: "job", Pending: contract.RunWaitPending{Job: &contract.RunWaitPendingJob{Kind: "job", Job: job, Generation: 1}}, Condition: WaitCondition{Event: &contract.RunWaitEventCondition{Kind: JobWaitEvent, Subject: job}}, TimeoutAt: timeoutAt, Continuation: "k", Checkpoint: contract.RunWaitCheckpointBinding{Revision: head.Revision, Digest: CheckpointBytesDigest(head.Checkpoint)}, Pins: jobKitPins, WakeKeyDigest: digest, Release: []WaitRelease{}}
}
func (k *jobKit) allJobs(run string) []RunJob {
	out := []RunJob{}
	for after := "__first__"; ; {
		if after == "__first__" {
			after = ""
		}
		page, err := k.store.ListJobs(k.ctx, run, after, 100)
		if err != nil {
			panic(err)
		}
		out = append(out, page.Jobs...)
		if page.Next == "" {
			return out
		}
		after = page.Next
	}
}
func (k *jobKit) waitOf(run string) *RunWait {
	held, err := k.store.ReadWait(k.ctx, run, "wait-1")
	if err != nil {
		panic(err)
	}
	return held
}
func (k *jobKit) wait(run string) RunWait {
	held, err := k.store.ReadWait(k.ctx, run, "wait-1")
	if err != nil || held == nil {
		panic(fmt.Errorf("no wait: %w", err))
	}
	return *held
}
func (k *jobKit) job(run, job string) *RunJob {
	held, err := k.store.ReadJob(k.ctx, run, job)
	if err != nil {
		panic(err)
	}
	return held
}
func (k *jobKit) revision(run string) string {
	head, err := k.store.Load(k.ctx, run)
	if err != nil || head == nil {
		panic(fmt.Errorf("no head: %w", err))
	}
	return head.Revision
}
func (k *jobKit) stamp() string { return k.at.UTC().Format("2006-01-02T15:04:05.000Z") }
func (k *jobKit) claimOf(run, evidence, owner, revision string) WaitClaim {
	digest, _ := PinsDigest(jobKitPins)
	return WaitClaim{Resource: run, Wait: "wait-1", Evidence: evidence, Lease: LeaseAcquire{Resource: run, Owner: owner, ExpectedRevision: revision, DurationMs: 3600000}, Reconciled: []WaitRelease{}, Revalidation: Revalidation{Version: 1, Resource: run, Wait: "wait-1", Pins: digest, Evidence: "sha256:" + strings.Repeat("c", 64), Changes: []contract.RunResumeChange{}}}
}

func (k *jobKit) properties() {
	now := func() time.Time { return k.at }
	k.store = k.create(now, nil)
	store := func() JobRunStore { return k.store }
	ctx := k.ctx
	k.check("job.declared", store().JobVersion() == 1 && (store().JobLifetime() == JobLifetimeDurable || store().JobLifetime() == JobLifetimeProcess) && store().WaitVersion() == 1 && store().LeaseVersion() == 1)

	// Registration: with its claim or not at all, only under a fence.
	a := k.open("run-a")
	first, firstRevision := k.register("run-a", "op-1", jobKitKeys[0], a)
	key0, _ := JobKeyDigest(jobKitKeys[0])
	job1 := k.job("run-a", first)
	k.check("job.register.commits", job1 != nil && job1.State == "unproven" && job1.Operation == "op-1" && job1.KeyDigest == key0 && job1.Registered.Owner == "worker-a" && job1.Registered.Epoch == a.fence.Epoch && job1.Registered.At == k.stamp() && k.revision("run-a") == firstRevision)
	head := firstRevision
	_, err := store().Commit(ctx, k.claim("run-a", head, "op-unfenced", jobKitWrong))
	k.refuses("job.register.unfenced", err, "invalid")
	k.check("job.register.unfenced", k.job("run-a", JobIdentity("run-a", "op-unfenced")) == nil && k.revision("run-a") == head)
	other := a.fence
	other.Owner = "worker-x"
	_, err = store().CommitFenced(ctx, k.claim("run-a", head, "op-stale", jobKitWrong), other)
	k.refuses("job.register.fence", err, "other-owner")
	k.check("job.register.fence", k.job("run-a", JobIdentity("run-a", "op-stale")) == nil)
	bad := k.claim("run-a", head, "op-bad", jobKitWrong)
	bad.Job.Job = JobIdentity("run-a", "op-other")
	_, err = store().CommitFenced(ctx, bad, a.fence)
	k.refuses("job.register.identity", err, "invalid")
	k.check("job.register.identity", k.revision("run-a") == head)
	// A digest another job holds: refused, and the claim did not commit either.
	_, err = store().CommitFenced(ctx, k.claim("run-a", head, "op-2", jobKitKeys[0]), a.fence)
	k.refuses("job.key.job", err, "key-reused")
	k.check("job.register.atomic", k.revision("run-a") == head && k.job("run-a", JobIdentity("run-a", "op-2")) == nil)
	// A store with the history capability registers on that path too.
	if history, ok := store().(LeaseHistoryRunStore); ok && history.HistoryVersion() == 1 {
		_, err = history.CommitHistoryFenced(ctx, RunHistoryCommit{Run: k.claim("run-a", head, "op-history", jobKitKeys[8]), Observations: []HistoryObjectRef{}}, a.fence)
		k.answers("job.register.history", err)
		held := k.job("run-a", JobIdentity("run-a", "op-history"))
		k.check("job.register.history", held != nil && held.State == "unproven")
	}
	// The history path, where a store with that capability really commits its claims (the writer's path): every check
	// holds there. Through the store of the moment: a restart replaces it.
	viaHistory := func(input RunCommit, fence LeaseFence) error {
		_, err := store().(LeaseHistoryRunStore).CommitHistoryFenced(ctx, RunHistoryCommit{Run: input, Observations: []HistoryObjectRef{}}, fence)
		return err
	}
	historyPath := func() bool {
		held, ok := store().(LeaseHistoryRunStore)
		return ok && held.HistoryVersion() == 1
	}
	hist := k.open("run-history")
	if historyPath() {
		if err = viaHistory(k.claim("run-history", hist.revision, "op-1", jobKitKey(100)), hist.fence); err != nil {
			panic(err)
		}
		moved := k.revision("run-history")
		held := k.job("run-history", JobIdentity("run-history", "op-1"))
		k.check("job.history.head", moved != hist.revision && held != nil && held.State == "unproven")
		otherHist := hist.fence
		otherHist.Owner = "worker-x"
		k.refuses("job.history.fence", viaHistory(k.claim("run-history", moved, "op-other", jobKitWrong), otherHist), "other-owner")
		k.check("job.history.fence", k.job("run-history", JobIdentity("run-history", "op-other")) == nil)
		badHist := k.claim("run-history", moved, "op-bad", jobKitWrong)
		badHist.Job.Job = JobIdentity("run-history", "op-other")
		k.refuses("job.history.identity", viaHistory(badHist, hist.fence), "invalid")
		k.refuses("job.history.key", viaHistory(k.claim("run-history", moved, "op-2", jobKitKey(100)), hist.fence), "key-reused")
		k.check("job.history.key", k.revision("run-history") == moved && k.job("run-history", JobIdentity("run-history", "op-2")) == nil)
	}
	// On both paths: a compare-and-set that fails registers nothing, and a repeated commit id must carry the same
	// registration.
	for i, path := range []struct {
		name   string
		commit func(RunCommit, LeaseFence) error
	}{
		{"fenced", func(input RunCommit, fence LeaseFence) error {
			_, err := store().CommitFenced(ctx, input, fence)
			return err
		}},
		{"history", viaHistory},
	} {
		if path.name == "history" && !historyPath() {
			continue
		}
		run := "run-cas-" + path.name
		cas := k.open(run)
		if err = path.commit(k.claim(run, cas.revision, "op-1", jobKitKey(110+10*i)), cas.fence); err != nil {
			panic(err)
		}
		after := k.revision(run)
		staleErr := path.commit(k.claim(run, cas.revision, "op-stale", jobKitKey(111+10*i)), cas.fence)
		k.check("job.stale."+path.name, staleErr != nil && k.job(run, JobIdentity(run, "op-stale")) == nil && k.revision(run) == after)
		repeat := k.claim(run, cas.revision, "op-2", jobKitKey(111+10*i))
		repeat.CommitID = run + "-op-1"
		repeatErr := path.commit(repeat, cas.fence)
		all, exportErr := store().ExportJobs(ctx, run)
		k.check("job.repeat."+path.name, repeatErr != nil && k.job(run, JobIdentity(run, "op-2")) == nil && exportErr == nil && len(all.Jobs) == 1)
	}
	// Owner: a job is looked up by its run and its identity together.
	k.check("job.owner.read", k.job("run-b", first) == nil)
	empty, err := store().ListJobs(ctx, "run-b", "", 10)
	listed, err2 := store().ListJobs(ctx, "run-a", "", 10)
	k.check("job.owner.list", err == nil && err2 == nil && len(empty.Jobs) == 0 && len(listed.Jobs) > 0 && listed.Jobs[0].Job == first)
	_, err = store().CompleteJob(ctx, jobKitComplete("run-b", first, jobKitKeys[0], "done-1", "succeeded"))
	k.refuses("job.owner.complete", err, "no-job")
	_, err = store().ReconcileJob(ctx, jobKitReconcile("run-b", first, jobKitKeys[0], "find-1", "not-started"))
	k.refuses("job.owner.reconcile", err, "no-job")
	// The typed answer: the fence that admitted it, current or not. M2.
	c := k.open("run-c")
	late, _ := k.register("run-c", "op-1", jobKitKeys[1], c)
	_ = k.job("run-c", late)
	if _, err = store().ListJobs(ctx, "run-c", "", 10); err != nil {
		panic(err)
	}
	otherC := c.fence
	otherC.Owner = "worker-x"
	_, err = store().AnswerJob(ctx, jobKitAnswer("run-c", late, "kit-handle"), otherC)
	k.refuses("job.answer.other", err, "unauthenticated")
	otherRun := c.fence
	otherRun.Resource = "run-other"
	_, err = store().AnswerJob(ctx, jobKitAnswer("run-c", late, "kit-handle"), otherRun)
	k.refuses("job.answer.run", err, "unauthenticated")
	_, err = store().AnswerJob(ctx, jobKitAnswer("run-other", late, "kit-handle"), c.fence)
	k.refuses("job.answer.across", err, "no-job")
	k.at = k.at.Add(2 * time.Hour)
	answered, err := store().AnswerJob(ctx, jobKitAnswer("run-c", late, "kit-handle"), c.fence)
	k.answers("job.answer.late", err)
	k.check("job.answer.late", answered.State == "started" && answered.Handle != nil && *answered.Handle == "kit-handle" && answered.Started != nil && answered.Started.By == "answer")
	freshRead := k.job("run-c", late)
	k.check("job.read.fresh", freshRead != nil && k.sameJob(*freshRead, answered))
	freshList, err := store().ListJobs(ctx, "run-c", "", 10)
	k.check("job.list.fresh", err == nil && len(freshList.Jobs) == 1 && k.sameJob(freshList.Jobs[0], answered))
	_, err = store().AnswerJob(ctx, jobKitAnswer("run-c", late, "kit-other"), c.fence)
	k.refuses("job.answer.conflict", err, "conflict")
	again, err := store().AnswerJob(ctx, jobKitAnswer("run-c", late, "kit-handle"), c.fence)
	k.answers("job.answer.same", err)
	k.check("job.answer.same", k.sameJob(again, answered))
	// Completion and its refusals, with no lease at all.
	_, err = store().CompleteJob(ctx, jobKitComplete("run-c", late, jobKitWrong, "done-1", "succeeded"))
	k.refuses("job.complete.key", err, "unauthenticated")
	done, err := store().CompleteJob(ctx, jobKitComplete("run-c", late, jobKitKeys[1], "done-1", "succeeded"))
	k.answers("job.complete.settles", err)
	k.check("job.complete.settles", done.State == "completed" && done.Completion != nil && done.Completion.Outcome == "succeeded" && done.Completion.At == k.stamp())
	repeat, err := store().CompleteJob(ctx, jobKitComplete("run-c", late, jobKitKeys[1], "done-1", "succeeded"))
	k.answers("job.complete.duplicate", err)
	k.check("job.complete.duplicate", k.sameJob(repeat, done))
	_, err = store().CompleteJob(ctx, jobKitComplete("run-c", late, jobKitKeys[1], "done-1", "failed"))
	k.refuses("job.complete.conflict", err, "conflict")
	_, err = store().CompleteJob(ctx, jobKitComplete("run-c", late, jobKitKeys[1], "done-2", "succeeded"))
	k.refuses("job.complete.stale", err, "stale")
	// Reconciliation: the key and no lease; first write wins; a contradiction is kept. H1, M1.
	d := k.open("run-d")
	closing, _ := k.register("run-d", "op-1", jobKitKeys[2], d)
	if _, err = store().ReleaseLease(ctx, d.fence); err != nil {
		panic(err)
	}
	unknown, err := store().ReconcileJob(ctx, jobKitReconcile("run-d", closing, jobKitKeys[2], "find-0", "unknown"))
	k.answers("job.reconcile.no-lease", err)
	k.check("job.reconcile.unknown", unknown.State == "unproven")
	_, err = store().ReconcileJob(ctx, jobKitReconcile("run-d", closing, jobKitWrong, "find-1", "not-started"))
	k.refuses("job.reconcile.key", err, "unauthenticated")
	closed, err := store().ReconcileJob(ctx, jobKitReconcile("run-d", closing, jobKitKeys[2], "find-1", "not-started"))
	k.answers("job.reconcile.no-lease", err)
	k.check("job.reconcile.no-lease", closed.State == "not-started" && closed.Closed != nil && closed.Closed.Evidence != nil && *closed.Closed.Evidence == "find-1")
	same, err := store().ReconcileJob(ctx, jobKitReconcile("run-d", closing, jobKitKeys[2], "find-1", "not-started"))
	k.answers("job.reconcile.same", err)
	k.check("job.reconcile.same", k.sameJob(same, closed))
	handle := "h"
	conflicting := jobKitReconcile("run-d", closing, jobKitKeys[2], "find-1", "started")
	conflicting.Handle = &handle
	_, err = store().ReconcileJob(ctx, conflicting)
	k.refuses("job.reconcile.conflict", err, "conflict")
	contradicted, err := store().CompleteJob(ctx, jobKitComplete("run-d", closing, jobKitKeys[2], "done-1", "succeeded"))
	k.answers("job.contradiction.completion", err)
	_, conflictErr := store().CompleteJob(ctx, jobKitComplete("run-d", closing, jobKitKeys[2], "done-1", "failed"))
	k.refuses("job.contradiction.conflict", conflictErr, "conflict")
	k.check("job.contradiction.completion", contradicted.State == "not-started" && len(contradicted.Contradictions) == 1 && contradicted.Contradictions[0].Kind == "completed" && contradicted.Contradictions[0].Outcome != nil && *contradicted.Contradictions[0].Outcome == "succeeded")
	lateHandle := "late"
	startedAfter := jobKitReconcile("run-d", closing, jobKitKeys[2], "find-2", "started")
	startedAfter.Handle = &lateHandle
	twice, err := store().ReconcileJob(ctx, startedAfter)
	k.answers("job.contradiction.start", err)
	k.check("job.contradiction.start", twice.State == "not-started" && len(twice.Contradictions) == 2 && twice.Contradictions[1].Handle != nil && *twice.Contradictions[1].Handle == "late")
	// A key belongs to one job or one wait, whichever came first.
	w := k.open("run-w")
	_, _, err = store().ParkWait(ctx, k.park("run-w", first, jobKitKeys[0], nil), w.fence)
	k.refuses("job.wait.key", err, "key-reused")
	held, err := store().ReadLease(ctx, "run-w")
	k.check("job.wait.key", err == nil && held != nil && held.State == "held")
	// The re-claim of a grant. H2.
	e := k.open("run-e")
	reclaim, _ := k.register("run-e", "op-1", jobKitKeys[3], e)
	otherE := e.fence
	otherE.Owner = "worker-x"
	_, err = store().ReclaimJob(ctx, "run-e", reclaim, otherE)
	k.refuses("job.reclaim.fence", err, "other-owner")
	k.check("job.reclaim.fence", k.job("run-e", reclaim).Reclaimed == nil)
	taken, err := store().ReclaimJob(ctx, "run-e", reclaim, e.fence)
	k.answers("job.reclaim.records", err)
	k.check("job.reclaim.records", taken.Reclaimed != nil && taken.Reclaimed.Owner == "worker-a" && taken.Reclaimed.Epoch == e.fence.Epoch && taken.State == "unproven")
	if _, err = store().ReconcileJob(ctx, jobKitReconcile("run-e", reclaim, jobKitKeys[3], "find-1", "not-started")); err != nil {
		panic(err)
	}
	_, err = store().ReclaimJob(ctx, "run-e", reclaim, e.fence)
	k.refuses("job.reclaim.settled", err, "job-settled")
	// Under an expired and a stale fence of the same owner.
	ex := k.open("run-expired")
	lapsed, _ := k.register("run-expired", "op-1", jobKitKey(130), ex)
	k.at = k.at.Add(2 * time.Hour)
	_, err = store().ReclaimJob(ctx, "run-expired", lapsed, ex.fence)
	k.refuses("job.reclaim.expired", err, "expired")
	k.check("job.reclaim.expired", k.job("run-expired", lapsed) != nil && k.job("run-expired", lapsed).Reclaimed == nil)
	st := k.open("run-stale")
	staled, staledRevision := k.register("run-stale", "op-1", jobKitKey(131), st)
	if _, err = store().ReleaseLease(ctx, st.fence); err != nil {
		panic(err)
	}
	if _, err = store().AcquireLease(ctx, LeaseAcquire{Resource: "run-stale", Owner: "worker-a", ExpectedRevision: staledRevision, DurationMs: 3600000}); err != nil {
		panic(err)
	}
	_, err = store().ReclaimJob(ctx, "run-stale", staled, st.fence)
	k.refuses("job.reclaim.stale", err, "stale-epoch")
	k.check("job.reclaim.stale", k.job("run-stale", staled) != nil && k.job("run-stale", staled).Reclaimed == nil)
	// The answer follows the re-claim: the fence that re-sent it answers, and the first one no longer does.
	f := k.open("run-f")
	moved, movedRevision := k.register("run-f", "op-1", jobKitKeys[4], f)
	if _, err = store().ReleaseLease(ctx, f.fence); err != nil {
		panic(err)
	}
	lease, err := store().AcquireLease(ctx, LeaseAcquire{Resource: "run-f", Owner: "worker-b", ExpectedRevision: movedRevision, DurationMs: 3600000})
	if err != nil {
		panic(err)
	}
	later := FenceOf(lease)
	if _, err = store().ReclaimJob(ctx, "run-f", moved, later); err != nil {
		panic(err)
	}
	_, err = store().AnswerJob(ctx, jobKitAnswer("run-f", moved, "kit-handle"), f.fence)
	k.refuses("job.reclaim.answer", err, "unauthenticated")
	movedAnswer, err := store().AnswerJob(ctx, jobKitAnswer("run-f", moved, "kit-handle"), later)
	k.check("job.reclaim.answer", err == nil && movedAnswer.State == "started")
	// The park: the job in the park's own transaction. The lost wake-up.
	g := k.open("run-g")
	unproven, _ := k.register("run-g", "op-1", jobKitKeys[5], g)
	_, _, err = store().ParkWait(ctx, k.park("run-g", unproven, jobKitWakes[0], nil), g.fence)
	k.refuses("job.park.unproven", err, "job-unproven")
	heldG, _ := store().ReadLease(ctx, "run-g")
	noWait, _ := store().ReadWait(ctx, "run-g", "wait-1")
	k.check("job.park.unproven", heldG != nil && heldG.State == "held" && noWait == nil)
	if _, err = store().AnswerJob(ctx, jobKitAnswer("run-g", unproven, "kit-handle"), g.fence); err != nil {
		panic(err)
	}
	if _, err = store().CompleteJob(ctx, jobKitComplete("run-g", unproven, jobKitKeys[5], "done-1", "succeeded")); err != nil {
		panic(err)
	}
	_, _, err = store().ParkWait(ctx, k.park("run-g", unproven, jobKitWakes[0], nil), g.fence)
	k.refuses("job.park.settled", err, "job-settled")
	heldG, _ = store().ReadLease(ctx, "run-g")
	noWait, _ = store().ReadWait(ctx, "run-g", "wait-1")
	k.check("job.park.settled", heldG != nil && heldG.State == "held" && noWait == nil)
	_, _, err = store().ParkWait(ctx, k.park("run-g", JobIdentity("run-g", "no-such"), jobKitWakes[0], nil), g.fence)
	k.refuses("job.park.absent", err, "no-job")
	// The wake: a completion, in its own transaction; nothing else.
	i := k.open("run-i")
	waited, _ := k.register("run-i", "op-1", jobKitKeys[6], i)
	if _, err = store().AnswerJob(ctx, jobKitAnswer("run-i", waited, "kit-handle"), i.fence); err != nil {
		panic(err)
	}
	if _, _, err = store().ParkWait(ctx, k.park("run-i", waited, jobKitWakes[2], nil), i.fence); err != nil {
		panic(err)
	}
	otherPark := k.open("run-other-park")
	_, _, err = store().ParkWait(ctx, k.park("run-other-park", waited, jobKitKey(132), nil), otherPark.fence)
	k.refuses("job.park.other", err, "no-job")
	k.check("job.park.other", k.waitOf("run-other-park") == nil)
	// A digest a wait holds: refused, and the claim did not commit either.
	headG := k.revision("run-g")
	_, err = store().CommitFenced(ctx, k.claim("run-g", headG, "op-2", jobKitWakes[2]), g.fence)
	k.refuses("job.key.wait", err, "key-reused")
	k.check("job.key.wait", k.revision("run-g") == headG)
	_, err = store().WakeWait(ctx, WakeEvidence{Version: 1, Resource: "run-i", Wait: "wait-1", Evidence: "forged", Kind: "event", Event: &contract.RunWaitEventCondition{Kind: JobWaitEvent, Subject: waited}, Key: jobKitWakes[2]})
	k.refuses("job.wake.event", err, "mismatch")
	k.check("job.wake.event", k.wait("run-i").State == "parked")
	// A restart: everything after it is decided by what the store kept. It keeps every job and every wait as they were,
	// all of each: a job completed before it is read after it.
	type keptRun struct {
		jobs  []RunJob
		waits WaitExport
	}
	before := map[string]keptRun{}
	for _, run := range k.touched {
		waits, waitErr := store().ExportWaits(ctx, run)
		if waitErr != nil {
			panic(waitErr)
		}
		before[run] = keptRun{jobs: k.allJobs(run), waits: waits}
	}
	next := k.create(now, k.store)
	k.check("job.restart.instance", next != k.store)
	k.store = next
	for run, held := range before {
		for _, job := range held.jobs {
			after := k.job(run, job.Job)
			k.check("job.restart.job", after != nil)
			a, _ := canonicalRaw(after.Contradictions)
			b, _ := canonicalRaw(job.Contradictions)
			k.check("job.restart.contradiction", string(a) == string(b))
			k.check("job.restart.record", k.sameJob(*after, job))
		}
		waits, waitErr := store().ExportWaits(ctx, run)
		x, _ := canonicalRaw(waits)
		y, _ := canonicalRaw(held.waits)
		k.check("job.restart.wait", waitErr == nil && string(x) == string(y))
	}
	_ = key0
	kept := k.job("run-d", closing)
	k.check("job.restart.contradiction", kept != nil && len(kept.Contradictions) == 2)
	r := k.open("run-r")
	_, err = store().CommitFenced(ctx, k.claim("run-r", r.revision, "op-after", jobKitKeys[0]), r.fence)
	k.refuses("job.restart.key", err, "key-reused")
	wokenJob, err := store().CompleteJob(ctx, jobKitComplete("run-i", waited, jobKitKeys[6], "done-1", "failed"))
	k.answers("job.wake.completion", err)
	woken := k.wait("run-i")
	k.check("job.wake.completion", woken.State == "woken" && woken.Disposition != nil && woken.Disposition.Evidence == "done-1" && woken.Disposition.Kind == "event" && wokenJob.Completion != nil && woken.Disposition.Digest == wokenJob.Completion.Digest)
	// A replay wakes nothing: the claimed wait stays claimed.
	if _, _, err = store().ResumeWait(ctx, k.claimOf("run-i", "done-1", "worker-b", k.revision("run-i"))); err != nil {
		panic(err)
	}
	if _, err = store().CompleteJob(ctx, jobKitComplete("run-i", waited, jobKitKeys[6], "done-1", "failed")); err != nil {
		panic(err)
	}
	k.check("job.wake.duplicate", k.wait("run-i").State == "resumed")
	// Late evidence: from the timeout only the timeout ends it, and that does not close the run.
	j := k.open("run-j")
	timed, _ := k.register("run-j", "op-1", jobKitKeys[7], j)
	if _, err = store().AnswerJob(ctx, jobKitAnswer("run-j", timed, "kit-handle"), j.fence); err != nil {
		panic(err)
	}
	timeoutAt := k.at.Add(time.Minute).UTC().Format("2006-01-02T15:04:05.000Z")
	if _, _, err = store().ParkWait(ctx, k.park("run-j", timed, jobKitWakes[3], &timeoutAt), j.fence); err != nil {
		panic(err)
	}
	k.at = k.at.Add(time.Minute)
	if _, err = store().CompleteJob(ctx, jobKitComplete("run-j", timed, jobKitKeys[7], "done-1", "succeeded")); err != nil {
		panic(err)
	}
	k.check("job.wake.late", k.wait("run-j").State == "parked")
	if _, err = store().WakeWait(ctx, WakeEvidence{Version: 1, Resource: "run-j", Wait: "wait-1", Evidence: "timeout-1", Kind: "timeout", Key: jobKitWakes[3]}); err != nil {
		panic(err)
	}
	k.check("job.timeout.state", k.wait("run-j").State == "timed-out")
	headJ := k.revision("run-j")
	claimed, claimedLease, err := store().ResumeWait(ctx, k.claimOf("run-j", "timeout-1", "worker-b", headJ))
	k.answers("job.timeout.continues", err)
	k.check("job.timeout.continues", claimed.State == "resumed")
	if _, err = store().ReleaseLease(ctx, FenceOf(claimedLease)); err != nil {
		panic(err)
	}
	reacquired, err := store().AcquireLease(ctx, LeaseAcquire{Resource: "run-j", Owner: "worker-c", ExpectedRevision: headJ, DurationMs: 3600000})
	k.answers("job.timeout.continues", err)
	k.check("job.timeout.continues", reacquired.State == "held")
	// A completion after the wait ended reopens nothing.
	m := k.open("run-m")
	after, _ := k.register("run-m", "op-1", jobKitKeys[9], m)
	if _, err = store().AnswerJob(ctx, jobKitAnswer("run-m", after, "kit-handle"), m.fence); err != nil {
		panic(err)
	}
	timeoutM := k.at.Add(time.Minute).UTC().Format("2006-01-02T15:04:05.000Z")
	if _, _, err = store().ParkWait(ctx, k.park("run-m", after, jobKitWakes[1], &timeoutM), m.fence); err != nil {
		panic(err)
	}
	k.at = k.at.Add(time.Minute)
	if _, err = store().WakeWait(ctx, WakeEvidence{Version: 1, Resource: "run-m", Wait: "wait-1", Evidence: "timeout-1", Kind: "timeout", Key: jobKitWakes[1]}); err != nil {
		panic(err)
	}
	_, resumedM, err := store().ResumeWait(ctx, k.claimOf("run-m", "timeout-1", "worker-b", k.revision("run-m")))
	if err != nil {
		panic(err)
	}
	// The run waits on the same job again: the completion wakes that wait, not the one that ended.
	parkedAgain := k.park("run-m", after, jobKitWakes[4], nil)
	parkedAgain.Wait = "wait-2"
	if _, _, err = store().ParkWait(ctx, parkedAgain, FenceOf(resumedM)); err != nil {
		panic(err)
	}
	if _, err = store().CompleteJob(ctx, jobKitComplete("run-m", after, jobKitKeys[9], "done-1", "succeeded")); err != nil {
		panic(err)
	}
	k.check("job.wake.resumed", k.wait("run-m").State == "resumed")
	wokenAgain, err := store().ReadWait(ctx, "run-m", "wait-2")
	k.check("job.wake.again", err == nil && wokenAgain != nil && wokenAgain.State == "woken")
	// From its deadline a job wait wakes for nothing either.
	dl := k.open("run-deadline")
	dated, _ := k.register("run-deadline", "op-1", jobKitKey(133), dl)
	if _, err = store().AnswerJob(ctx, jobKitAnswer("run-deadline", dated, "kit-handle"), dl.fence); err != nil {
		panic(err)
	}
	minute := int64(60000)
	datedPark := k.park("run-deadline", dated, jobKitKey(134), nil)
	datedPark.Limits = &WaitLimits{WaitMs: &minute}
	if _, _, err = store().ParkWait(ctx, datedPark, dl.fence); err != nil {
		panic(err)
	}
	k.at = k.at.Add(time.Minute)
	if _, err = store().CompleteJob(ctx, jobKitComplete("run-deadline", dated, jobKitKey(133), "done-1", "succeeded")); err != nil {
		panic(err)
	}
	k.check("job.wake.deadline", k.wait("run-deadline").State == "parked")
	// The run's limit: its sixty-fifth job is refused, with its claim.
	limit := k.open("run-k")
	moving := limit.revision
	for n := range 64 {
		receipt, err := store().CommitFenced(ctx, k.claim("run-k", moving, fmt.Sprintf("op-%d", n), fmt.Sprintf("kit.limit.%03d.Q7z2Xm9LpV4cR8tNb3Yh6Df1", n)), limit.fence)
		if err != nil {
			panic(err)
		}
		moving = receipt.Revision
	}
	_, err = store().CommitFenced(ctx, k.claim("run-k", moving, "op-64", "kit.limit.064.Q7z2Xm9LpV4cR8tNb3Yh6Df1"), limit.fence)
	k.refuses("job.limit", err, "jobs-exhausted")
	all, err := store().ExportJobs(ctx, "run-k")
	k.check("job.limit", err == nil && k.revision("run-k") == moving && len(all.Jobs) == 64)
	page, err := store().ListJobs(ctx, "run-k", "", 10)
	rest, err2 := store().ListJobs(ctx, "run-k", page.Next, 100)
	k.check("job.list.page", err == nil && err2 == nil && len(page.Jobs) == 10 && page.Next == page.Jobs[9].Job && len(rest.Jobs) == 54)
	if historyPath() {
		limit := k.open("run-limit-history")
		rolling := limit.revision
		for n := range 64 {
			if err = viaHistory(k.claim("run-limit-history", rolling, fmt.Sprintf("op-%d", n), jobKitKey(200+n)), limit.fence); err != nil {
				panic(err)
			}
			rolling = k.revision("run-limit-history")
		}
		k.refuses("job.history.limit", viaHistory(k.claim("run-limit-history", rolling, "op-64", jobKitKey(264)), limit.fence), "jobs-exhausted")
		all, exportErr := store().ExportJobs(ctx, "run-limit-history")
		k.check("job.history.limit", exportErr == nil && k.revision("run-limit-history") == rolling && len(all.Jobs) == 64)
	}
	// What an export says is what the reads say, run by run.
	for _, run := range k.touched {
		exported, exportErr := store().ExportJobs(ctx, run)
		x, _ := canonicalRaw(exported.Jobs)
		y, _ := canonicalRaw(k.allJobs(run))
		k.check("job.export.reads", exportErr == nil && string(x) == string(y))
	}
}

// CheckRunStoreJobConformance certifies a store kind's job port (#229). It returns StorageConformanceFailure naming
// the first property the store breaks.
func CheckRunStoreJobConformance(ctx context.Context, create JobStoreFactory) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			if failure, ok := recovered.(*StorageConformanceFailure); ok {
				err = failure
				return
			}
			err = &StorageConformanceFailure{Property: "job.operation"}
		}
	}()
	kit := &jobKit{ctx: ctx, at: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), create: create}
	kit.properties()
	return nil
}
