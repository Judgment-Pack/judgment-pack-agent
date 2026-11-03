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
	"strconv"
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

// restartWhole restarts the store, and every job and wait must read as it did, all of each.
func (k *jobKit) restartWhole(now func() time.Time) {
	type keptRun struct {
		jobs  []RunJob
		waits WaitExport
	}
	before := map[string]keptRun{}
	for _, run := range k.touched {
		waits, err := k.store.ExportWaits(k.ctx, run)
		if err != nil {
			panic(err)
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
		waits, err := k.store.ExportWaits(k.ctx, run)
		x, _ := canonicalRaw(waits)
		y, _ := canonicalRaw(held.waits)
		k.check("job.restart.wait", err == nil && string(x) == string(y))
	}
}

// unchanged: a refusal changes nothing: the job reads as it did.
func (k *jobKit) unchanged(run, job, property, reason string, attempt func() error) {
	before := k.job(run, job)
	k.refuses(property, attempt(), reason)
	after := k.job(run, job)
	k.check(property, (before == nil) == (after == nil) && (before == nil || k.sameJob(*before, *after)))
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

// jobKitSameMarker: the same handleDigested marker, present or not.
func jobKitSameMarker(a, b *bool) bool { return (a == nil) == (b == nil) && (a == nil || *a == *b) }

// jobKitEpochAfter: a lease epoch is a decimal string with no leading zero; a is the later epoch.
func jobKitEpochAfter(a, b string) bool { return len(a) > len(b) || len(a) == len(b) && a > b }
func (k *jobKit) stamp() string         { return k.at.UTC().Format("2006-01-02T15:04:05.000Z") }
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
	k.unchanged("run-c", late, "job.answer.other", "unauthenticated", func() error {
		_, err := store().AnswerJob(ctx, jobKitAnswer("run-c", late, "kit-handle"), otherC)
		return err
	})
	otherRun := c.fence
	otherRun.Resource = "run-other"
	otherEpoch := c.fence
	nextEpoch, _ := strconv.Atoi(c.fence.Epoch)
	otherEpoch.Epoch = strconv.Itoa(nextEpoch + 1)
	k.unchanged("run-c", late, "job.answer.epoch", "unauthenticated", func() error {
		_, err := store().AnswerJob(ctx, jobKitAnswer("run-c", late, "kit-handle"), otherEpoch)
		return err
	})
	k.unchanged("run-c", late, "job.answer.run", "unauthenticated", func() error {
		_, err := store().AnswerJob(ctx, jobKitAnswer("run-c", late, "kit-handle"), otherRun)
		return err
	})
	k.unchanged("run-c", late, "job.answer.across", "no-job", func() error {
		_, err := store().AnswerJob(ctx, jobKitAnswer("run-other", late, "kit-handle"), c.fence)
		return err
	})
	k.at = k.at.Add(2 * time.Hour)
	answered, err := store().AnswerJob(ctx, jobKitAnswer("run-c", late, "kit-handle"), c.fence)
	k.answers("job.answer.late", err)
	k.check("job.answer.late", answered.State == "started" && answered.Handle != nil && *answered.Handle == "kit-handle" && answered.Started != nil && answered.Started.By == "answer")
	freshRead := k.job("run-c", late)
	k.check("job.read.fresh", freshRead != nil && k.sameJob(*freshRead, answered))
	freshList, err := store().ListJobs(ctx, "run-c", "", 10)
	k.check("job.list.fresh", err == nil && len(freshList.Jobs) == 1 && k.sameJob(freshList.Jobs[0], answered))
	k.unchanged("run-c", late, "job.answer.conflict", "conflict", func() error {
		_, err := store().AnswerJob(ctx, jobKitAnswer("run-c", late, "kit-other"), c.fence)
		return err
	})
	again, err := store().AnswerJob(ctx, jobKitAnswer("run-c", late, "kit-handle"), c.fence)
	k.answers("job.answer.same", err)
	k.check("job.answer.same", k.sameJob(again, answered))
	// Completion and its refusals, with no lease at all.
	k.unchanged("run-c", late, "job.complete.key", "unauthenticated", func() error {
		_, err := store().CompleteJob(ctx, jobKitComplete("run-c", late, jobKitWrong, "done-1", "succeeded"))
		return err
	})
	done, err := store().CompleteJob(ctx, jobKitComplete("run-c", late, jobKitKeys[1], "done-1", "succeeded"))
	k.answers("job.complete.settles", err)
	k.check("job.complete.settles", done.State == "completed" && done.Completion != nil && done.Completion.Outcome == "succeeded" && done.Completion.At == k.stamp())
	readDone := k.job("run-c", late)
	k.check("job.read.completed", readDone != nil && k.sameJob(*readDone, done))
	repeat, err := store().CompleteJob(ctx, jobKitComplete("run-c", late, jobKitKeys[1], "done-1", "succeeded"))
	k.answers("job.complete.duplicate", err)
	k.check("job.complete.duplicate", k.sameJob(repeat, done))
	k.unchanged("run-c", late, "job.complete.conflict", "conflict", func() error {
		_, err := store().CompleteJob(ctx, jobKitComplete("run-c", late, jobKitKeys[1], "done-1", "failed"))
		return err
	})
	k.unchanged("run-c", late, "job.complete.stale", "stale", func() error {
		_, err := store().CompleteJob(ctx, jobKitComplete("run-c", late, jobKitKeys[1], "done-2", "succeeded"))
		return err
	})
	// Reconciliation: the key and no lease; first write wins; a contradiction is kept. H1, M1.
	d := k.open("run-d")
	closing, _ := k.register("run-d", "op-1", jobKitKeys[2], d)
	if _, err = store().ReleaseLease(ctx, d.fence); err != nil {
		panic(err)
	}
	unknown, err := store().ReconcileJob(ctx, jobKitReconcile("run-d", closing, jobKitKeys[2], "find-0", "unknown"))
	k.answers("job.reconcile.no-lease", err)
	k.check("job.reconcile.unknown", unknown.State == "unproven")
	k.unchanged("run-d", closing, "job.reconcile.key", "unauthenticated", func() error {
		_, err := store().ReconcileJob(ctx, jobKitReconcile("run-d", closing, jobKitWrong, "find-1", "not-started"))
		return err
	})
	closed, err := store().ReconcileJob(ctx, jobKitReconcile("run-d", closing, jobKitKeys[2], "find-1", "not-started"))
	k.answers("job.reconcile.no-lease", err)
	k.check("job.reconcile.no-lease", closed.State == "not-started" && closed.Closed != nil && closed.Closed.Evidence != nil && *closed.Closed.Evidence == "find-1")
	readClosed := k.job("run-d", closing)
	k.check("job.read.closed", readClosed != nil && k.sameJob(*readClosed, closed))
	same, err := store().ReconcileJob(ctx, jobKitReconcile("run-d", closing, jobKitKeys[2], "find-1", "not-started"))
	k.answers("job.reconcile.same", err)
	k.check("job.reconcile.same", k.sameJob(same, closed))
	handle := "h"
	conflicting := jobKitReconcile("run-d", closing, jobKitKeys[2], "find-1", "started")
	conflicting.Handle = &handle
	k.unchanged("run-d", closing, "job.reconcile.conflict", "conflict", func() error {
		_, err := store().ReconcileJob(ctx, conflicting)
		return err
	})
	contradicted, err := store().CompleteJob(ctx, jobKitComplete("run-d", closing, jobKitKeys[2], "done-1", "succeeded"))
	k.answers("job.contradiction.completion", err)
	k.unchanged("run-d", closing, "job.contradiction.conflict", "conflict", func() error {
		_, err := store().CompleteJob(ctx, jobKitComplete("run-d", closing, jobKitKeys[2], "done-1", "failed"))
		return err
	})
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
	k.unchanged("run-e", reclaim, "job.reclaim.fence", "other-owner", func() error {
		_, err := store().ReclaimJob(ctx, "run-e", reclaim, otherE)
		return err
	})
	k.check("job.reclaim.fence", k.job("run-e", reclaim).Reclaimed == nil)
	taken, err := store().ReclaimJob(ctx, "run-e", reclaim, e.fence)
	k.answers("job.reclaim.records", err)
	k.check("job.reclaim.records", taken.Reclaimed != nil && taken.Reclaimed.Owner == "worker-a" && taken.Reclaimed.Epoch == e.fence.Epoch && taken.State == "unproven")
	if _, err = store().ReconcileJob(ctx, jobKitReconcile("run-e", reclaim, jobKitKeys[3], "find-1", "not-started")); err != nil {
		panic(err)
	}
	k.unchanged("run-e", reclaim, "job.reclaim.settled", "job-settled", func() error {
		_, err := store().ReclaimJob(ctx, "run-e", reclaim, e.fence)
		return err
	})
	// Under an expired and a stale fence of the same owner.
	ex := k.open("run-expired")
	lapsed, _ := k.register("run-expired", "op-1", jobKitKey(130), ex)
	k.at = k.at.Add(2 * time.Hour)
	k.unchanged("run-expired", lapsed, "job.reclaim.expired", "expired", func() error {
		_, err := store().ReclaimJob(ctx, "run-expired", lapsed, ex.fence)
		return err
	})
	k.check("job.reclaim.expired", k.job("run-expired", lapsed) != nil && k.job("run-expired", lapsed).Reclaimed == nil)
	st := k.open("run-stale")
	staled, staledRevision := k.register("run-stale", "op-1", jobKitKey(131), st)
	if _, err = store().ReleaseLease(ctx, st.fence); err != nil {
		panic(err)
	}
	if _, err = store().AcquireLease(ctx, LeaseAcquire{Resource: "run-stale", Owner: "worker-a", ExpectedRevision: staledRevision, DurationMs: 3600000}); err != nil {
		panic(err)
	}
	k.unchanged("run-stale", staled, "job.reclaim.stale", "stale-epoch", func() error {
		_, err := store().ReclaimJob(ctx, "run-stale", staled, st.fence)
		return err
	})
	k.check("job.reclaim.stale", k.job("run-stale", staled) != nil && k.job("run-stale", staled).Reclaimed == nil)
	// A cancel: asked for under a current fence, of a started job, moving no state. A stale fence asks nothing: unlike a
	// start's answer.
	askOf := func(run, job, operation string) JobCancelAsk {
		return JobCancelAsk{Version: 1, Resource: run, Job: job, Generation: 1, By: "tool", Operation: operation}
	}
	wordOf := func(run, job, key, evidence, outcome string) JobCancelAcknowledgement {
		receipt := "kit-word-receipt"
		return JobCancelAcknowledgement{Version: 1, Resource: run, Job: job, Generation: 1, Evidence: evidence, Outcome: outcome, Receipt: &receipt, Key: key}
	}
	can := k.open("run-cancel")
	canJob, _ := k.register("run-cancel", "op-1", jobKitKey(150), can)
	k.unchanged("run-cancel", canJob, "job.cancel.unproven", "job-unproven", func() error {
		_, err := store().RequestJobCancel(ctx, askOf("run-cancel", canJob, "cancel-1"), can.fence)
		return err
	})
	if _, err = store().AnswerJob(ctx, jobKitAnswer("run-cancel", canJob, "kit-handle"), can.fence); err != nil {
		panic(err)
	}
	k.unchanged("run-cancel", canJob, "job.cancel.none", "no-cancel", func() error {
		_, err := store().AcknowledgeJobCancel(ctx, wordOf("run-cancel", canJob, jobKitKey(150), "word-1", "accepted"))
		return err
	})
	otherCan := can.fence
	otherCan.Owner = "worker-x"
	k.unchanged("run-cancel", canJob, "job.cancel.fence", "other-owner", func() error {
		_, err := store().RequestJobCancel(ctx, askOf("run-cancel", canJob, "cancel-1"), otherCan)
		return err
	})
	// A second job of the run: the answer must be the job asked for.
	otherCanJob, _ := k.register("run-cancel", "op-2", jobKitKey(159), jobKitRun{revision: k.revision("run-cancel"), fence: can.fence})
	if _, err = store().AnswerJob(ctx, jobKitAnswer("run-cancel", otherCanJob, "kit-handle-2"), can.fence); err != nil {
		panic(err)
	}
	beforeAsk, otherBefore := k.job("run-cancel", canJob), k.job("run-cancel", otherCanJob)
	// C6: a fence of another run asks nothing of this run's job.
	elsewhere := k.open("run-cancel-elsewhere")
	k.unchanged("run-cancel", canJob, "job.cancel.run", "resource-mismatch", func() error {
		_, err := store().RequestJobCancel(ctx, askOf("run-cancel", canJob, "cancel-1"), elsewhere.fence)
		return err
	})
	asked, err := store().RequestJobCancel(ctx, askOf("run-cancel", canJob, "cancel-1"), can.fence)
	k.answers("job.cancel.records", err)
	// F-2b, F-21: the answer is the record asked of, with its handle, marker, source and tool as they were, and the
	// other job is untouched: its record reads whole as before.
	otherAfter := k.job("run-cancel", otherCanJob)
	k.check("job.cancel.which", asked.Job == canJob && asked.Resource == "run-cancel" && beforeAsk != nil && sameJobOptional(asked.Handle, beforeAsk.Handle) && jobKitSameMarker(asked.HandleDigested, beforeAsk.HandleDigested) && asked.Source == beforeAsk.Source && asked.Tool == beforeAsk.Tool && otherAfter != nil && otherBefore != nil && k.sameJob(*otherAfter, *otherBefore))
	k.check("job.cancel.records", asked.State == "started" && asked.Cancel != nil && asked.Cancel.Requested.By == "tool" && asked.Cancel.Requested.Operation == "cancel-1" && asked.Cancel.Requested.Owner != nil && *asked.Cancel.Requested.Owner == "worker-a" && asked.Cancel.Requested.Epoch != nil && *asked.Cancel.Requested.Epoch == can.fence.Epoch && asked.Cancel.Requested.At == k.stamp())
	askedAgain, err := store().RequestJobCancel(ctx, askOf("run-cancel", canJob, "cancel-2"), can.fence)
	k.answers("job.cancel.first", err)
	k.check("job.cancel.first", k.sameJob(askedAgain, asked))
	k.unchanged("run-cancel", canJob, "job.cancel.key", "unauthenticated", func() error {
		_, err := store().AcknowledgeJobCancel(ctx, wordOf("run-cancel", canJob, jobKitWrong, "word-1", "accepted"))
		return err
	})
	worded, err := store().AcknowledgeJobCancel(ctx, wordOf("run-cancel", canJob, jobKitKey(150), "word-1", "accepted"))
	k.answers("job.cancel.word", err)
	k.check("job.cancel.word", worded.State == "started" && worded.Cancel != nil && worded.Cancel.Acknowledged != nil && worded.Cancel.Acknowledged.Outcome == "accepted" && worded.Cancel.Acknowledged.Evidence == "word-1" && worded.Cancel.Acknowledged.At == k.stamp() && worded.Cancel.Acknowledged.Receipt != nil && *worded.Cancel.Acknowledged.Receipt == "kit-word-receipt")
	wordedAgain, err := store().AcknowledgeJobCancel(ctx, wordOf("run-cancel", canJob, jobKitKey(150), "word-1", "accepted"))
	k.answers("job.cancel.same", err)
	k.check("job.cancel.same", k.sameJob(wordedAgain, worded))
	k.unchanged("run-cancel", canJob, "job.cancel.conflict", "conflict", func() error {
		_, err := store().AcknowledgeJobCancel(ctx, wordOf("run-cancel", canJob, jobKitKey(150), "word-1", "refused"))
		return err
	})
	k.unchanged("run-cancel", canJob, "job.cancel.again", "stale", func() error {
		_, err := store().AcknowledgeJobCancel(ctx, wordOf("run-cancel", canJob, jobKitKey(150), "word-2", "accepted"))
		return err
	})
	stopped, err := store().CompleteJob(ctx, jobKitComplete("run-cancel", canJob, jobKitKey(150), "done-1", "cancelled"))
	if err != nil {
		panic(err)
	}
	stoppedCancel, _ := canonicalRaw(stopped.Cancel)
	wordedCancel, _ := canonicalRaw(worded.Cancel)
	k.check("job.cancel.completes", stopped.State == "completed" && string(stoppedCancel) == string(wordedCancel))
	k.unchanged("run-cancel", canJob, "job.cancel.settled", "job-settled", func() error {
		_, err := store().RequestJobCancel(ctx, askOf("run-cancel", canJob, "cancel-3"), can.fence)
		return err
	})
	closedCan := k.open("run-cancel-closed")
	closedJob, _ := k.register("run-cancel-closed", "op-1", jobKitKey(151), closedCan)
	if _, err = store().ReconcileJob(ctx, jobKitReconcile("run-cancel-closed", closedJob, jobKitKey(151), "find-1", "not-started")); err != nil {
		panic(err)
	}
	k.unchanged("run-cancel-closed", closedJob, "job.cancel.closed", "job-settled", func() error {
		_, err := store().RequestJobCancel(ctx, askOf("run-cancel-closed", closedJob, "cancel-1"), closedCan.fence)
		return err
	})
	staleCan := k.open("run-cancel-stale")
	staleJob, staleRevision := k.register("run-cancel-stale", "op-1", jobKitKey(152), staleCan)
	if _, err = store().AnswerJob(ctx, jobKitAnswer("run-cancel-stale", staleJob, "kit-handle"), staleCan.fence); err != nil {
		panic(err)
	}
	if _, err = store().ReleaseLease(ctx, staleCan.fence); err != nil {
		panic(err)
	}
	newLease, err := store().AcquireLease(ctx, LeaseAcquire{Resource: "run-cancel-stale", Owner: "worker-a", ExpectedRevision: staleRevision, DurationMs: 3600000})
	if err != nil {
		panic(err)
	}
	k.unchanged("run-cancel-stale", staleJob, "job.cancel.stale", "stale-epoch", func() error {
		_, err := store().RequestJobCancel(ctx, askOf("run-cancel-stale", staleJob, "cancel-1"), staleCan.fence)
		return err
	})
	k.at = k.at.Add(2 * time.Hour)
	k.unchanged("run-cancel-stale", staleJob, "job.cancel.expired", "expired", func() error {
		_, err := store().RequestJobCancel(ctx, askOf("run-cancel-stale", staleJob, "cancel-1"), FenceOf(newLease))
		return err
	})
	// C7: a handle kept as its digest keeps its marker, on the answer and on every read (and across the restart, below).
	dig := k.open("run-digested")
	digJob, _ := k.register("run-digested", "op-1", jobKitKey(160), dig)
	digAnswer, marked := jobKitAnswer("run-digested", digJob, "sha256:"+strings.Repeat("d", 64)), true
	digAnswer.HandleDigested = &marked
	digAnswered, err := store().AnswerJob(ctx, digAnswer, dig.fence)
	k.answers("job.answer.digested", err)
	digRead, digListed := k.job("run-digested", digJob), k.allJobs("run-digested")
	k.check("job.answer.digested", jobKitSameMarker(digAnswered.HandleDigested, &marked) && digRead != nil && jobKitSameMarker(digRead.HandleDigested, &marked) && len(digListed) == 1 && jobKitSameMarker(digListed[0].HandleDigested, &marked))
	// #316 review, MEDIUM-1: a reconciliation's digest keeps its marker as an answer's does (and across the restart, below),
	// so a cancel is never handed the digest as the host's reference.
	rdig := k.open("run-reconciled-digested")
	rdigJob, _ := k.register("run-reconciled-digested", "op-1", jobKitKey(168), rdig)
	rdigFinding, rdigDigest := jobKitReconcile("run-reconciled-digested", rdigJob, jobKitKey(168), "find-1", "started"), "sha256:"+strings.Repeat("d", 64)
	rdigFinding.Handle, rdigFinding.HandleDigested = &rdigDigest, &marked
	rdigFound, err := store().ReconcileJob(ctx, rdigFinding)
	k.answers("job.reconcile.digested", err)
	rdigRead, rdigListed := k.job("run-reconciled-digested", rdigJob), k.allJobs("run-reconciled-digested")
	k.check("job.reconcile.digested", rdigFound.State == "started" && rdigFound.Handle != nil && *rdigFound.Handle == rdigDigest && jobKitSameMarker(rdigFound.HandleDigested, &marked) && rdigRead != nil && jobKitSameMarker(rdigRead.HandleDigested, &marked) && len(rdigListed) == 1 && jobKitSameMarker(rdigListed[0].HandleDigested, &marked))
	// C3: an abandon's command never rewrites a tool's first request.
	fr := k.open("run-cancel-first")
	frJob, _ := k.register("run-cancel-first", "op-1", jobKitKey(161), fr)
	if _, err = store().AnswerJob(ctx, jobKitAnswer("run-cancel-first", frJob, "kit-handle"), fr.fence); err != nil {
		panic(err)
	}
	toolAsk, err := store().RequestJobCancel(ctx, askOf("run-cancel-first", frJob, "cancel-tool"), fr.fence)
	if err != nil {
		panic(err)
	}
	_, err = store().AbandonJobs(ctx, JobAbandon{Resource: "run-cancel-first", Command: "cmd-first", DurationMs: 3600000})
	k.answers("job.abandon.first", err)
	frRead := k.job("run-cancel-first", frJob)
	k.check("job.abandon.first", frRead != nil && k.sameJob(RunJob{Cancel: frRead.Cancel}, RunJob{Cancel: toolAsk.Cancel}))
	// An abandon's own transaction: the lease under the reserved owner at a new epoch, and a cancel asked for each
	// started job.
	ab := k.open("run-abandon")
	abStarted, abRevision := k.register("run-abandon", "op-1", jobKitKey(153), ab)
	abUnproven, abRevision := k.register("run-abandon", "op-2", jobKitKey(154), jobKitRun{revision: abRevision, fence: ab.fence})
	abDone, abRevision := k.register("run-abandon", "op-3", jobKitKey(155), jobKitRun{revision: abRevision, fence: ab.fence})
	abSecond, abRevision := k.register("run-abandon", "op-4", jobKitKey(167), jobKitRun{revision: abRevision, fence: ab.fence})
	for _, held := range [][2]string{{abStarted, "kit-handle"}, {abDone, "kit-handle"}} {
		if _, err = store().AnswerJob(ctx, jobKitAnswer("run-abandon", held[0], held[1]), ab.fence); err != nil {
			panic(err)
		}
	}
	// #325: one started job's handle is a marked digest, so an abandon's answer that drops the marker is not the record
	// (job.abandon.jobs).
	abSecondAnswer, abSecondMarked := jobKitAnswer("run-abandon", abSecond, "sha256:"+strings.Repeat("e", 64)), true
	abSecondAnswer.HandleDigested = &abSecondMarked
	if _, err = store().AnswerJob(ctx, abSecondAnswer, ab.fence); err != nil {
		panic(err)
	}
	if _, err = store().CompleteJob(ctx, jobKitComplete("run-abandon", abDone, jobKitKey(155), "done-1", "succeeded")); err != nil {
		panic(err)
	}
	abTaken, err := store().AbandonJobs(ctx, JobAbandon{Resource: "run-abandon", Command: "cmd-1", DurationMs: 3600000})
	k.answers("job.abandon.lease", err)
	k.check("job.abandon.lease", abTaken.Lease != nil && abTaken.Lease.Owner == JobAbandonOwner && abTaken.Lease.State == "held" && abTaken.Lease.Epoch != ab.fence.Epoch)
	currentLease, err := store().ReadLease(ctx, "run-abandon")
	k.check("job.abandon.lease", err == nil && currentLease != nil && currentLease.Epoch == abTaken.Lease.Epoch)
	k.check("job.abandon.nocommit", k.revision("run-abandon") == abRevision)
	_, err = store().CommitFenced(ctx, k.claim("run-abandon", abRevision, "op-late", jobKitWrong), ab.fence)
	k.refuses("job.abandon.epoch", err, "stale-epoch")
	for _, started := range []string{abStarted, abSecond} {
		abAsked := k.job("run-abandon", started)
		k.check("job.abandon.asks", abAsked != nil && abAsked.State == "started" && abAsked.Cancel != nil && abAsked.Cancel.Requested.By == "command" && abAsked.Cancel.Requested.Command != nil && *abAsked.Cancel.Requested.Command == "cmd-1" && abAsked.Cancel.Requested.Owner != nil && *abAsked.Cancel.Requested.Owner == JobAbandonOwner && abAsked.Cancel.Requested.Epoch != nil && *abAsked.Cancel.Requested.Epoch == abTaken.Lease.Epoch)
		k.check("job.abandon.operation", abAsked.Cancel.Requested.Operation == JobCancelOperation("cmd-1", started))
	}
	// C14: another abandon is another new epoch: the first abandon's fence goes stale too.
	abAgain, err := store().AbandonJobs(ctx, JobAbandon{Resource: "run-abandon", Command: "cmd-again", DurationMs: 3600000})
	k.answers("job.abandon.again", err)
	k.check("job.abandon.again", abAgain.Lease != nil && abTaken.Lease != nil && jobKitEpochAfter(abAgain.Lease.Epoch, abTaken.Lease.Epoch))
	k.check("job.abandon.only", k.job("run-abandon", abUnproven).Cancel == nil && k.job("run-abandon", abDone).Cancel == nil)
	abAgainJobs, _ := canonicalRaw(abAgain.Jobs)
	abRead, _ := canonicalRaw(k.allJobs("run-abandon"))
	k.check("job.abandon.jobs", string(abAgainJobs) == string(abRead))
	// F-9: an abandon the store refuses has moved nothing. F-10: one on a run the store has never seen takes no lease.
	invalidBefore, _ := store().ReadLease(ctx, "run-abandon")
	_, err = store().AbandonJobs(ctx, JobAbandon{Resource: "run-abandon", Command: "", DurationMs: 3600000})
	k.refuses("job.abandon.invalid", err, "invalid")
	invalidAfter, _ := store().ReadLease(ctx, "run-abandon")
	leaseBefore, _ := canonicalRaw(invalidBefore)
	leaseAfter, _ := canonicalRaw(invalidAfter)
	k.check("job.abandon.invalid", string(leaseAfter) == string(leaseBefore))
	unseen, err := store().AbandonJobs(ctx, JobAbandon{Resource: "run-abandon-never", Command: "cmd-never", DurationMs: 3600000})
	k.answers("job.abandon.unknown", err)
	unseenLease, _ := store().ReadLease(ctx, "run-abandon-never")
	k.check("job.abandon.unknown", unseen.Lease == nil && len(unseen.Jobs) == 0 && unseenLease == nil)
	// #316 review, MEDIUM-3: a run leased before its first commit is a run the store has seen. Its abandon takes the lease at
	// a new epoch, and the worker's first claim, under the fence it held, is refused.
	k.touched = append(k.touched, "run-abandon-leased")
	freshLease, err := store().AcquireLease(ctx, LeaseAcquire{Resource: "run-abandon-leased", Owner: "worker-a", DurationMs: 3600000})
	if err != nil {
		panic(err)
	}
	freshTaken, err := store().AbandonJobs(ctx, JobAbandon{Resource: "run-abandon-leased", Command: "cmd-leased", DurationMs: 3600000})
	k.answers("job.abandon.leased", err)
	freshHeld, _ := store().ReadLease(ctx, "run-abandon-leased")
	k.check("job.abandon.leased", freshTaken.Lease != nil && freshTaken.Lease.Owner == JobAbandonOwner && jobKitEpochAfter(freshTaken.Lease.Epoch, freshLease.Epoch) && len(freshTaken.Jobs) == 0 && freshHeld != nil && freshHeld.Epoch == freshTaken.Lease.Epoch)
	_, err = store().CommitFenced(ctx, k.claim("run-abandon-leased", "", "op-1", jobKitKey(169)), FenceOf(freshLease))
	k.refuses("job.abandon.leased", err, "stale-epoch")
	freshHead, _ := store().Load(ctx, "run-abandon-leased")
	k.check("job.abandon.leased", freshHead == nil && len(k.allJobs("run-abandon-leased")) == 0)
	// A delegated run's principals stay on the abandon's lease: a takeover is a new epoch of the same principals.
	if keeps, ok := store().(LeasePrincipalStore); ok && keeps.LeasePrincipals() {
		principal := &PrincipalBinding{Sub: contract.RunPrincipalPrincipalReference{Kind: "person", Issuer: "https://host.example/", Subject: "kit-subject"}, RootActor: strings.Repeat("b1", 16), Host: "kit-host"}
		head, err := store().Commit(ctx, RunCommit{RunID: "run-abandon-principal", CommitID: "run-abandon-principal-open", Checkpoint: []byte(`{"kit":"job"}`), Events: [][]byte{}})
		if err != nil {
			panic(err)
		}
		if _, err = store().AcquireLease(ctx, LeaseAcquire{Resource: "run-abandon-principal", Owner: "worker-a", ExpectedRevision: head.Revision, DurationMs: 3600000, Principal: principal}); err != nil {
			panic(err)
		}
		held, err := store().AbandonJobs(ctx, JobAbandon{Resource: "run-abandon-principal", Command: "cmd-3", DurationMs: 3600000})
		k.answers("job.abandon.principal", err)
		k.check("job.abandon.principal", held.Lease != nil && held.Lease.Version == 2 && held.Lease.Principal != nil && SamePrincipalBinding(held.Lease.Principal, principal))
	}
	// C15: an abandon's lease ends by the run's deadline.
	abDl := k.open("run-abandon-deadline")
	dlJob, _ := k.register("run-abandon-deadline", "op-1", jobKitKey(162), abDl)
	if _, err = store().AnswerJob(ctx, jobKitAnswer("run-abandon-deadline", dlJob, "kit-handle"), abDl.fence); err != nil {
		panic(err)
	}
	dlLimit := k.at.Add(10 * time.Minute)
	if _, err = store().LimitRun(ctx, RunLimit{Version: 1, Resource: "run-abandon-deadline", Run: dlLimit.UTC().Format("2006-01-02T15:04:05.000Z")}); err != nil {
		panic(err)
	}
	dlTaken, err := store().AbandonJobs(ctx, JobAbandon{Resource: "run-abandon-deadline", Command: "cmd-abDl", DurationMs: 3600000})
	k.answers("job.abandon.deadline", err)
	k.check("job.abandon.deadline", dlTaken.Lease != nil && func() bool {
		expiry, parseErr := time.Parse(time.RFC3339Nano, dlTaken.Lease.Expiry)
		return parseErr == nil && !expiry.After(dlLimit)
	}())
	// C17, C18: a parked run is abandoned like any other, and its wait stands.
	pk := k.open("run-abandon-parked")
	pkJob, _ := k.register("run-abandon-parked", "op-1", jobKitKey(163), pk)
	if _, err = store().AnswerJob(ctx, jobKitAnswer("run-abandon-parked", pkJob, "kit-handle"), pk.fence); err != nil {
		panic(err)
	}
	if _, _, err = store().ParkWait(ctx, k.park("run-abandon-parked", pkJob, jobKitKey(164), nil), pk.fence); err != nil {
		panic(err)
	}
	parkedWait, _ := canonicalRaw(k.wait("run-abandon-parked"))
	pkTaken, err := store().AbandonJobs(ctx, JobAbandon{Resource: "run-abandon-parked", Command: "cmd-pk", DurationMs: 3600000})
	k.answers("job.abandon.parked", err)
	pkRead := k.job("run-abandon-parked", pkJob)
	k.check("job.abandon.parked", pkTaken.Lease != nil && pkTaken.Lease.Owner == JobAbandonOwner && pkRead != nil && pkRead.Cancel != nil && pkRead.Cancel.Requested.Command != nil && *pkRead.Cancel.Requested.Command == "cmd-pk")
	waitAfter, _ := canonicalRaw(k.wait("run-abandon-parked"))
	k.check("job.abandon.wait", string(waitAfter) == string(parkedWait))
	// C16: a run its deadline closed grants no lease: the cancel stays undelivered.
	cl := k.open("run-abandon-closed")
	clJob, _ := k.register("run-abandon-closed", "op-1", jobKitKey(165), cl)
	if _, err = store().AnswerJob(ctx, jobKitAnswer("run-abandon-closed", clJob, "kit-handle"), cl.fence); err != nil {
		panic(err)
	}
	if _, _, err = store().ParkWait(ctx, k.park("run-abandon-closed", clJob, jobKitKey(166), nil), cl.fence); err != nil {
		panic(err)
	}
	if _, err = store().LimitRun(ctx, RunLimit{Version: 1, Resource: "run-abandon-closed", Run: k.at.Add(time.Minute).UTC().Format("2006-01-02T15:04:05.000Z")}); err != nil {
		panic(err)
	}
	k.at = k.at.Add(time.Minute)
	if _, err = store().WakeWait(ctx, WakeEvidence{Version: 1, Resource: "run-abandon-closed", Wait: "wait-1", Evidence: "deadline-1", Kind: "deadline", Key: jobKitKey(166)}); err != nil {
		panic(err)
	}
	clTaken, err := store().AbandonJobs(ctx, JobAbandon{Resource: "run-abandon-closed", Command: "cmd-cl", DurationMs: 3600000})
	k.answers("job.abandon.closed", err)
	clRead := k.job("run-abandon-closed", clJob)
	k.check("job.abandon.closed", clTaken.Lease == nil && clRead != nil && clRead.Cancel != nil && clRead.Cancel.Requested.Undelivered != nil)
	// Past a deadline no lease can be granted: each cancel stays undelivered, and says so.
	lost := k.open("run-abandon-late")
	lostJob, _ := k.register("run-abandon-late", "op-1", jobKitKey(156), lost)
	if _, err = store().AnswerJob(ctx, jobKitAnswer("run-abandon-late", lostJob, "kit-handle"), lost.fence); err != nil {
		panic(err)
	}
	limitAt := k.at.Add(time.Minute).UTC().Format("2006-01-02T15:04:05.000Z")
	if _, err = store().LimitRun(ctx, RunLimit{Version: 1, Resource: "run-abandon-late", Run: limitAt}); err != nil {
		panic(err)
	}
	k.at = k.at.Add(time.Minute)
	lateTaken, err := store().AbandonJobs(ctx, JobAbandon{Resource: "run-abandon-late", Command: "cmd-2", DurationMs: 3600000})
	k.answers("job.abandon.past", err)
	lateHeld := k.job("run-abandon-late", lostJob)
	k.check("job.abandon.past", lateTaken.Lease == nil && lateHeld != nil && lateHeld.Cancel != nil && lateHeld.Cancel.Requested.Undelivered != nil && lateHeld.Cancel.Requested.Owner == nil)
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
	k.unchanged("run-f", moved, "job.reclaim.answer", "unauthenticated", func() error {
		_, err := store().AnswerJob(ctx, jobKitAnswer("run-f", moved, "kit-handle"), f.fence)
		return err
	})
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
	k.restartWhole(now)
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
	// Neither a cancel nor the host's word on it wakes a wait.
	quiet := k.open("run-cancel-wait")
	quietJob, _ := k.register("run-cancel-wait", "op-1", jobKitKey(157), quiet)
	if _, err = store().AnswerJob(ctx, jobKitAnswer("run-cancel-wait", quietJob, "kit-handle"), quiet.fence); err != nil {
		panic(err)
	}
	if _, err = store().RequestJobCancel(ctx, JobCancelAsk{Version: 1, Resource: "run-cancel-wait", Job: quietJob, Generation: 1, By: "tool", Operation: "cancel-1"}, quiet.fence); err != nil {
		panic(err)
	}
	if _, _, err = store().ParkWait(ctx, k.park("run-cancel-wait", quietJob, jobKitKey(158), nil), quiet.fence); err != nil {
		panic(err)
	}
	if _, err = store().AcknowledgeJobCancel(ctx, JobCancelAcknowledgement{Version: 1, Resource: "run-cancel-wait", Job: quietJob, Generation: 1, Evidence: "word-1", Outcome: "accepted", Key: jobKitKey(157)}); err != nil {
		panic(err)
	}
	k.check("job.cancel.nowake", k.wait("run-cancel-wait").State == "parked")
	// A completion wakes the wait on its own job, not the run's parked wait whichever job it is for.
	two := k.open("run-two")
	oneJob, oneRevision := k.register("run-two", "op-1", jobKitKey(135), two)
	anotherJob, _ := k.register("run-two", "op-2", jobKitKey(136), jobKitRun{revision: oneRevision, fence: two.fence})
	for _, held := range []string{oneJob, anotherJob} {
		if _, err = store().AnswerJob(ctx, jobKitAnswer("run-two", held, "kit-handle"), two.fence); err != nil {
			panic(err)
		}
	}
	if _, _, err = store().ParkWait(ctx, k.park("run-two", oneJob, jobKitKey(137), nil), two.fence); err != nil {
		panic(err)
	}
	if _, err = store().CompleteJob(ctx, jobKitComplete("run-two", anotherJob, jobKitKey(136), "done-1", "succeeded")); err != nil {
		panic(err)
	}
	k.check("job.wake.other", k.wait("run-two").State == "parked")
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
	// One more restart, with waits woken, resumed and timed out by now: all read as they were, and every key and commit
	// id holds.
	timedLast := k.open("run-timed")
	lastJob, _ := k.register("run-timed", "op-1", jobKitKey(140), timedLast)
	if _, err = store().AnswerJob(ctx, jobKitAnswer("run-timed", lastJob, "kit-handle"), timedLast.fence); err != nil {
		panic(err)
	}
	timeoutLast := k.at.Add(time.Minute).UTC().Format("2006-01-02T15:04:05.000Z")
	if _, _, err = store().ParkWait(ctx, k.park("run-timed", lastJob, jobKitKey(141), &timeoutLast), timedLast.fence); err != nil {
		panic(err)
	}
	k.at = k.at.Add(time.Minute)
	if _, err = store().WakeWait(ctx, WakeEvidence{Version: 1, Resource: "run-timed", Wait: "wait-1", Evidence: "timeout-1", Kind: "timeout", Key: jobKitKey(141)}); err != nil {
		panic(err)
	}
	k.check("job.timeout.state", k.wait("run-timed").State == "timed-out")
	again2 := k.open("run-after")
	kept2, _ := k.register("run-after", "op-1", jobKitKey(142), again2)
	if _, err = store().AnswerJob(ctx, jobKitAnswer("run-after", kept2, "kit-handle"), again2.fence); err != nil {
		panic(err)
	}
	k.restartWhole(now)
	headAfter := k.revision("run-after")
	repeatAfter := k.claim("run-after", headAfter, "op-other", jobKitKey(143))
	repeatAfter.CommitID = "run-after-op-1"
	_, repeatErr := store().CommitFenced(ctx, repeatAfter, again2.fence)
	k.check("job.restart.commit", repeatErr != nil && k.job("run-after", JobIdentity("run-after", "op-other")) == nil && k.revision("run-after") == headAfter)
	_, err = store().CommitFenced(ctx, k.claim("run-after", headAfter, "op-wake", jobKitKey(141)), again2.fence)
	k.refuses("job.restart.wakekey", err, "key-reused")
	_, _, err = store().ParkWait(ctx, k.park("run-after", kept2, jobKitKey(142), nil), again2.fence)
	k.refuses("job.restart.jobkey", err, "key-reused")
	k.unchanged("run-after", kept2, "job.restart.auth", "unauthenticated", func() error {
		_, err := store().CompleteJob(ctx, jobKitComplete("run-after", kept2, jobKitWrong, "done-1", "succeeded"))
		return err
	})
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
