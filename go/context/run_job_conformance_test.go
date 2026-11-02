package runcontext

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// #229: the job conformance kit over the memory kind, a real restart over a fresh store instance, and for every
// property a store that leaves that one guarantee out. The TypeScript twin is
// packages/harness/storage/jobConformance.test.ts.
type durableJobStore struct{ *MemoryRunStore }

func (durableJobStore) LeaseLifetime() string { return LeaseLifetimeDurable }
func (durableJobStore) WaitLifetime() string  { return WaitLifetimeDurable }
func (durableJobStore) JobLifetime() string   { return JobLifetimeDurable }

func jobRestarted(old *MemoryRunStore, now func() time.Time, then func(*MemoryRunStore)) *MemoryRunStore {
	fresh := restartedStore(old, now)
	old.mu.Lock()
	defer old.mu.Unlock()
	fresh.jobs.rows = deepCopied(old.jobs.rows)
	if then != nil {
		then(fresh)
	}
	return fresh
}
func memoryJobKind(wrap func(*MemoryRunStore) JobRunStore, then func(*MemoryRunStore)) JobStoreFactory {
	held := map[JobRunStore]*MemoryRunStore{}
	return func(now func() time.Time, from JobRunStore) JobRunStore {
		store := NewMemoryRunStore(MemoryRunStoreOptions{Now: now})
		if from != nil {
			store = jobRestarted(held[from], now, then)
		}
		out := wrap(store)
		held[out] = store
		return out
	}
}
func plainJobKind(store *MemoryRunStore) JobRunStore { return durableJobStore{store} }

// jobFault breaks one thing: each field left nil forwards to the memory kind.
type jobFault struct {
	durableJobStore
	version   int
	commit    func(s jobFault, ctx context.Context, input RunCommit) (RunReceipt, error)
	fenced    func(s jobFault, ctx context.Context, input RunCommit, fence LeaseFence) (RunReceipt, error)
	history   func(s jobFault, ctx context.Context, input RunHistoryCommit, fence LeaseFence) (RunHistoryReceipt, error)
	read      func(s jobFault, ctx context.Context, resource, job string) (*RunJob, error)
	list      func(s jobFault, ctx context.Context, resource, after string, limit int) (JobPage, error)
	answer    func(s jobFault, ctx context.Context, input JobAnswer, fence LeaseFence) (RunJob, error)
	reconcile func(s jobFault, ctx context.Context, input JobReconciliation) (RunJob, error)
	complete  func(s jobFault, ctx context.Context, input JobCompletionEvidence) (RunJob, error)
	reclaim   func(s jobFault, ctx context.Context, resource, job string, fence LeaseFence) (RunJob, error)
	export    func(s jobFault, ctx context.Context, resource string) (JobExport, error)
	// opened is told of each instance the kit builds: the third is the one the last restart builds.
	opened func()
	park   func(s jobFault, ctx context.Context, park RunWait, fence LeaseFence) (RunWait, AttentionEvent, error)
	wake   func(s jobFault, ctx context.Context, evidence WakeEvidence) (WakeDisposition, error)
	resume func(s jobFault, ctx context.Context, claim WaitClaim) (RunWait, RunLease, error)
}

func (s jobFault) JobVersion() int {
	if s.version != 0 {
		return s.version
	}
	return s.durableJobStore.JobVersion()
}
func (s jobFault) Commit(ctx context.Context, input RunCommit) (RunReceipt, error) {
	if s.commit != nil {
		return s.commit(s, ctx, input)
	}
	return s.durableJobStore.Commit(ctx, input)
}
func (s jobFault) CommitFenced(ctx context.Context, input RunCommit, fence LeaseFence) (RunReceipt, error) {
	if s.fenced != nil {
		return s.fenced(s, ctx, input, fence)
	}
	return s.durableJobStore.CommitFenced(ctx, input, fence)
}
func (s jobFault) CommitHistoryFenced(ctx context.Context, input RunHistoryCommit, fence LeaseFence) (RunHistoryReceipt, error) {
	if s.history != nil {
		return s.history(s, ctx, input, fence)
	}
	return s.durableJobStore.CommitHistoryFenced(ctx, input, fence)
}
func (s jobFault) ReadJob(ctx context.Context, resource, job string) (*RunJob, error) {
	if s.read != nil {
		return s.read(s, ctx, resource, job)
	}
	return s.durableJobStore.ReadJob(ctx, resource, job)
}
func (s jobFault) ListJobs(ctx context.Context, resource, after string, limit int) (JobPage, error) {
	if s.list != nil {
		return s.list(s, ctx, resource, after, limit)
	}
	return s.durableJobStore.ListJobs(ctx, resource, after, limit)
}
func (s jobFault) AnswerJob(ctx context.Context, input JobAnswer, fence LeaseFence) (RunJob, error) {
	if s.answer != nil {
		return s.answer(s, ctx, input, fence)
	}
	return s.durableJobStore.AnswerJob(ctx, input, fence)
}
func (s jobFault) ReconcileJob(ctx context.Context, input JobReconciliation) (RunJob, error) {
	if s.reconcile != nil {
		return s.reconcile(s, ctx, input)
	}
	return s.durableJobStore.ReconcileJob(ctx, input)
}
func (s jobFault) CompleteJob(ctx context.Context, input JobCompletionEvidence) (RunJob, error) {
	if s.complete != nil {
		return s.complete(s, ctx, input)
	}
	return s.durableJobStore.CompleteJob(ctx, input)
}
func (s jobFault) ReclaimJob(ctx context.Context, resource, job string, fence LeaseFence) (RunJob, error) {
	if s.reclaim != nil {
		return s.reclaim(s, ctx, resource, job, fence)
	}
	return s.durableJobStore.ReclaimJob(ctx, resource, job, fence)
}
func (s jobFault) ParkWait(ctx context.Context, park RunWait, fence LeaseFence) (RunWait, AttentionEvent, error) {
	if s.park != nil {
		return s.park(s, ctx, park, fence)
	}
	return s.durableJobStore.ParkWait(ctx, park, fence)
}
func (s jobFault) WakeWait(ctx context.Context, evidence WakeEvidence) (WakeDisposition, error) {
	if s.wake != nil {
		return s.wake(s, ctx, evidence)
	}
	return s.durableJobStore.WakeWait(ctx, evidence)
}
func (s jobFault) ResumeWait(ctx context.Context, claim WaitClaim) (RunWait, RunLease, error) {
	if s.resume != nil {
		return s.resume(s, ctx, claim)
	}
	return s.durableJobStore.ResumeWait(ctx, claim)
}

func withoutJob(input RunCommit) RunCommit { input.Job = nil; return input }
func refusedAs(err error, reason string) bool {
	refused := asJobRefusal(err)
	return refused != nil && refused.Reason == reason
}

// jobFaults: for each property of the kit, a store that breaks exactly it.
var jobFaults = map[string]jobFault{
	"job.declared": {version: 2},
	"job.register.commits": {fenced: func(s jobFault, ctx context.Context, input RunCommit, fence LeaseFence) (RunReceipt, error) {
		return s.durableJobStore.CommitFenced(ctx, withoutJob(input), fence)
	}},
	"job.register.unfenced": {commit: func(s jobFault, ctx context.Context, input RunCommit) (RunReceipt, error) {
		return s.durableJobStore.Commit(ctx, withoutJob(input))
	}},
	"job.register.fence": {fenced: func(s jobFault, ctx context.Context, input RunCommit, fence LeaseFence) (RunReceipt, error) {
		if input.Job != nil && fence.Owner == "worker-x" {
			s.jobs.Register(input.RunID, *input.Job, fence)
		}
		return s.durableJobStore.CommitFenced(ctx, input, fence)
	}},
	"job.register.identity": {fenced: func(s jobFault, ctx context.Context, input RunCommit, fence LeaseFence) (RunReceipt, error) {
		if input.Job != nil && input.Job.Operation == "op-bad" {
			input = withoutJob(input)
		}
		return s.durableJobStore.CommitFenced(ctx, input, fence)
	}},
	"job.register.atomic": {fenced: func(s jobFault, ctx context.Context, input RunCommit, fence LeaseFence) (RunReceipt, error) {
		receipt, err := s.durableJobStore.CommitFenced(ctx, input, fence)
		if refusedAs(err, "key-reused") {
			_, _ = s.durableJobStore.CommitFenced(ctx, withoutJob(input), fence)
		}
		return receipt, err
	}},
	"job.register.history": {history: func(s jobFault, ctx context.Context, input RunHistoryCommit, fence LeaseFence) (RunHistoryReceipt, error) {
		input.Run = withoutJob(input.Run)
		return s.durableJobStore.CommitHistoryFenced(ctx, input, fence)
	}},
	"job.key.job": {fenced: func(s jobFault, ctx context.Context, input RunCommit, fence LeaseFence) (RunReceipt, error) {
		if input.RunID == "run-a" && input.Job != nil && input.Job.Operation == "op-2" {
			input = withoutJob(input)
		}
		return s.durableJobStore.CommitFenced(ctx, input, fence)
	}},
	"job.key.wait": {fenced: func(s jobFault, ctx context.Context, input RunCommit, fence LeaseFence) (RunReceipt, error) {
		if input.RunID == "run-g" && input.Job != nil && input.Job.Operation == "op-2" {
			input = withoutJob(input)
		}
		return s.durableJobStore.CommitFenced(ctx, input, fence)
	}},
	"job.wait.key": {park: func(s jobFault, ctx context.Context, park RunWait, fence LeaseFence) (RunWait, AttentionEvent, error) {
		if park.Resource == "run-w" {
			park.WakeKeyDigest, _ = WakeKeyDigest("kit.wake-elsewhere.Q7z2Xm9LpV4cR8tNb3Yh6Df1")
		}
		return s.durableJobStore.ParkWait(ctx, park, fence)
	}},
	"job.owner.read": {read: func(s jobFault, ctx context.Context, resource, job string) (*RunJob, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, rows := range s.jobs.rows {
			for _, row := range rows {
				if row.Job == job {
					out := cloneRunJob(*row)
					return &out, nil
				}
			}
		}
		return nil, nil
	}},
	"job.owner.list": {list: func(s jobFault, ctx context.Context, resource, after string, limit int) (JobPage, error) {
		return s.durableJobStore.ListJobs(ctx, "run-a", "", limit)
	}},
	"job.owner.complete": {complete: func(s jobFault, ctx context.Context, input JobCompletionEvidence) (RunJob, error) {
		if input.Resource == "run-b" {
			input.Resource = "run-a"
		}
		return s.durableJobStore.CompleteJob(ctx, input)
	}},
	"job.owner.reconcile": {reconcile: func(s jobFault, ctx context.Context, input JobReconciliation) (RunJob, error) {
		if input.Resource == "run-b" {
			input.Resource = "run-a"
		}
		return s.durableJobStore.ReconcileJob(ctx, input)
	}},
	"job.answer.other": {answer: func(s jobFault, ctx context.Context, input JobAnswer, fence LeaseFence) (RunJob, error) {
		fence.Owner = "worker-a"
		return s.durableJobStore.AnswerJob(ctx, input, fence)
	}},
	"job.answer.late": {answer: func(s jobFault, ctx context.Context, input JobAnswer, fence LeaseFence) (RunJob, error) {
		held, _ := s.durableJobStore.ReadJob(ctx, input.Resource, input.Job)
		if fence.Owner == "worker-a" && fence.Resource == input.Resource && held != nil && held.Registered.Epoch == fence.Epoch {
			if err := s.CheckFence(ctx, fence); err != nil {
				return RunJob{}, err
			}
		}
		return s.durableJobStore.AnswerJob(ctx, input, fence)
	}},
	"job.answer.conflict": {answer: func(s jobFault, ctx context.Context, input JobAnswer, fence LeaseFence) (RunJob, error) {
		job, err := s.durableJobStore.AnswerJob(ctx, input, fence)
		if refusedAs(err, "conflict") {
			held, _ := s.durableJobStore.ReadJob(ctx, input.Resource, input.Job)
			return *held, nil
		}
		return job, err
	}},
	"job.answer.same": func() jobFault {
		seen := map[string]bool{}
		return jobFault{answer: func(s jobFault, ctx context.Context, input JobAnswer, fence LeaseFence) (RunJob, error) {
			key := input.Job + *input.Handle
			if seen[key] {
				return RunJob{}, jobRefused("stale")
			}
			job, err := s.durableJobStore.AnswerJob(ctx, input, fence)
			if err == nil {
				seen[key] = true
			}
			return job, err
		}}
	}(),
	"job.complete.key": {complete: func(s jobFault, ctx context.Context, input JobCompletionEvidence) (RunJob, error) {
		if input.Resource == "run-c" {
			input.Key = jobKitKeys[1]
		}
		return s.durableJobStore.CompleteJob(ctx, input)
	}},
	"job.complete.settles": {complete: func(s jobFault, ctx context.Context, input JobCompletionEvidence) (RunJob, error) {
		input.Outcome = "unknown"
		return s.durableJobStore.CompleteJob(ctx, input)
	}},
	"job.complete.duplicate": func() jobFault {
		seen := map[string]bool{}
		return jobFault{complete: func(s jobFault, ctx context.Context, input JobCompletionEvidence) (RunJob, error) {
			if seen[input.Job+input.Evidence] {
				return RunJob{}, jobRefused("stale")
			}
			job, err := s.durableJobStore.CompleteJob(ctx, input)
			if err == nil {
				seen[input.Job+input.Evidence] = true
			}
			return job, err
		}}
	}(),
	"job.complete.conflict": {complete: func(s jobFault, ctx context.Context, input JobCompletionEvidence) (RunJob, error) {
		job, err := s.durableJobStore.CompleteJob(ctx, input)
		if refusedAs(err, "conflict") {
			held, _ := s.durableJobStore.ReadJob(ctx, input.Resource, input.Job)
			return *held, nil
		}
		return job, err
	}},
	"job.complete.stale": {complete: func(s jobFault, ctx context.Context, input JobCompletionEvidence) (RunJob, error) {
		job, err := s.durableJobStore.CompleteJob(ctx, input)
		if refusedAs(err, "stale") {
			held, _ := s.durableJobStore.ReadJob(ctx, input.Resource, input.Job)
			return *held, nil
		}
		return job, err
	}},
	"job.reconcile.unknown": {reconcile: func(s jobFault, ctx context.Context, input JobReconciliation) (RunJob, error) {
		if input.Finding == "unknown" {
			input.Finding = "not-started"
		}
		return s.durableJobStore.ReconcileJob(ctx, input)
	}},
	"job.reconcile.key": {reconcile: func(s jobFault, ctx context.Context, input JobReconciliation) (RunJob, error) {
		if input.Resource == "run-d" {
			input.Key = jobKitKeys[2]
		}
		return s.durableJobStore.ReconcileJob(ctx, input)
	}},
	"job.reconcile.no-lease": {reconcile: func(s jobFault, ctx context.Context, input JobReconciliation) (RunJob, error) {
		if input.Resource == "run-d" {
			held, _ := s.ReadLease(ctx, input.Resource)
			if held == nil || held.State != "held" {
				return RunJob{}, &LeaseRefused{Reason: "no-lease"}
			}
		}
		return s.durableJobStore.ReconcileJob(ctx, input)
	}},
	"job.reconcile.same": func() jobFault {
		seen := map[string]bool{}
		return jobFault{reconcile: func(s jobFault, ctx context.Context, input JobReconciliation) (RunJob, error) {
			if input.Finding == "not-started" && seen[input.Evidence] {
				return RunJob{}, jobRefused("stale")
			}
			job, err := s.durableJobStore.ReconcileJob(ctx, input)
			if err == nil {
				seen[input.Evidence] = true
			}
			return job, err
		}}
	}(),
	"job.reconcile.conflict": {reconcile: func(s jobFault, ctx context.Context, input JobReconciliation) (RunJob, error) {
		job, err := s.durableJobStore.ReconcileJob(ctx, input)
		if refusedAs(err, "conflict") {
			held, _ := s.durableJobStore.ReadJob(ctx, input.Resource, input.Job)
			return *held, nil
		}
		return job, err
	}},
	"job.contradiction.completion": {complete: func(s jobFault, ctx context.Context, input JobCompletionEvidence) (RunJob, error) {
		if held, _ := s.durableJobStore.ReadJob(ctx, input.Resource, input.Job); held != nil && held.State == "not-started" {
			return RunJob{}, jobRefused("stale")
		}
		return s.durableJobStore.CompleteJob(ctx, input)
	}},
	"job.contradiction.start": {reconcile: func(s jobFault, ctx context.Context, input JobReconciliation) (RunJob, error) {
		if held, _ := s.durableJobStore.ReadJob(ctx, input.Resource, input.Job); held != nil && held.State == "not-started" && input.Finding == "started" && input.Evidence != "find-1" {
			return *held, nil
		}
		return s.durableJobStore.ReconcileJob(ctx, input)
	}},
	"job.reclaim.fence": {reclaim: func(s jobFault, ctx context.Context, resource, job string, fence LeaseFence) (RunJob, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.jobs.Reclaim(resource, job, fence)
	}},
	"job.reclaim.records": {reclaim: func(s jobFault, ctx context.Context, resource, job string, fence LeaseFence) (RunJob, error) {
		if err := s.CheckFence(ctx, fence); err != nil {
			return RunJob{}, err
		}
		held, _ := s.durableJobStore.ReadJob(ctx, resource, job)
		return *held, nil
	}},
	"job.reclaim.settled": {reclaim: func(s jobFault, ctx context.Context, resource, job string, fence LeaseFence) (RunJob, error) {
		out, err := s.durableJobStore.ReclaimJob(ctx, resource, job, fence)
		if refusedAs(err, "job-settled") {
			held, _ := s.durableJobStore.ReadJob(ctx, resource, job)
			return *held, nil
		}
		return out, err
	}},
	"job.reclaim.answer": {reclaim: func(s jobFault, ctx context.Context, resource, job string, fence LeaseFence) (RunJob, error) {
		if err := s.CheckFence(ctx, fence); err != nil {
			return RunJob{}, err
		}
		held, _ := s.durableJobStore.ReadJob(ctx, resource, job)
		if held.State != "unproven" {
			return RunJob{}, jobRefused("job-settled")
		}
		out := *held
		out.Reclaimed = &JobActor{Owner: fence.Owner, Epoch: fence.Epoch, At: "2026-01-01T00:00:00.000Z"}
		return out, nil
	}},
	"job.park.unproven": {park: func(s jobFault, ctx context.Context, park RunWait, fence LeaseFence) (RunWait, AttentionEvent, error) {
		if row := s.jobs.find(park.Resource, park.Pending.Job.Job); row != nil && row.State == "unproven" {
			row.State = "started"
			defer func() { row.State = "unproven" }()
		}
		return s.durableJobStore.ParkWait(ctx, park, fence)
	}},
	"job.park.settled": {park: func(s jobFault, ctx context.Context, park RunWait, fence LeaseFence) (RunWait, AttentionEvent, error) {
		if row := s.jobs.find(park.Resource, park.Pending.Job.Job); row != nil && row.State == "completed" {
			row.State = "started"
			defer func() { row.State = "completed" }()
		}
		return s.durableJobStore.ParkWait(ctx, park, fence)
	}},
	"job.park.absent": {park: func(s jobFault, ctx context.Context, park RunWait, fence LeaseFence) (RunWait, AttentionEvent, error) {
		if s.jobs.find(park.Resource, park.Pending.Job.Job) == nil {
			rows := s.jobs.rows[park.Resource]
			s.jobs.rows[park.Resource] = append(rows, &RunJob{Job: park.Pending.Job.Job, State: "started"})
			defer func() { s.jobs.rows[park.Resource] = rows }()
		}
		return s.durableJobStore.ParkWait(ctx, park, fence)
	}},
	"job.wake.event": {wake: func(s jobFault, ctx context.Context, evidence WakeEvidence) (WakeDisposition, error) {
		if evidence.Kind == "event" && evidence.Evidence == "forged" {
			_ = s.waits.WakeJob(evidence.Resource, evidence.Event.Subject, JobWake{Evidence: evidence.Evidence, Digest: "sha256:" + "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"})
			return WakeDisposition{}, nil
		}
		return s.durableJobStore.WakeWait(ctx, evidence)
	}},
	"job.wake.completion": {complete: func(s jobFault, ctx context.Context, input JobCompletionEvidence) (RunJob, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		job, _, err := s.jobs.Complete(input)
		return job, err
	}},
	"job.wake.duplicate": {complete: func(s jobFault, ctx context.Context, input JobCompletionEvidence) (RunJob, error) {
		job, err := s.durableJobStore.CompleteJob(ctx, input)
		if row := s.waits.find(input.Resource, "wait-1"); row != nil && row.State == "resumed" {
			row.State = "woken"
		}
		return job, err
	}},
	"job.wake.resumed": {complete: func(s jobFault, ctx context.Context, input JobCompletionEvidence) (RunJob, error) {
		job, err := s.durableJobStore.CompleteJob(ctx, input)
		if row := s.waits.find(input.Resource, "wait-1"); input.Resource == "run-m" && row != nil && row.State == "resumed" {
			row.State = "woken"
		}
		return job, err
	}},
	"job.wake.again": {complete: func(s jobFault, ctx context.Context, input JobCompletionEvidence) (RunJob, error) {
		job, err := s.durableJobStore.CompleteJob(ctx, input)
		if row := s.waits.find(input.Resource, "wait-2"); input.Resource == "run-m" && row != nil && row.State == "woken" {
			row.State, row.Disposition = "parked", nil
		}
		return job, err
	}},
	"job.wake.late": {complete: func(s jobFault, ctx context.Context, input JobCompletionEvidence) (RunJob, error) {
		job, err := s.durableJobStore.CompleteJob(ctx, input)
		if row := s.waits.find(input.Resource, "wait-1"); err == nil && row != nil && row.State == "parked" && row.Pending.Job != nil {
			row.State = "woken"
			row.Disposition = &WakeDisposition{Evidence: input.Evidence, Kind: "event", At: row.Parked.At, Digest: job.Completion.Digest}
		}
		return job, err
	}},
	"job.timeout.state": {wake: func(s jobFault, ctx context.Context, evidence WakeEvidence) (WakeDisposition, error) {
		out, err := s.durableJobStore.WakeWait(ctx, evidence)
		if row := s.waits.find(evidence.Resource, evidence.Wait); row != nil && evidence.Kind == "timeout" {
			row.State = "parked"
		}
		return out, err
	}},
	"job.timeout.continues": {resume: func(s jobFault, ctx context.Context, claim WaitClaim) (RunWait, RunLease, error) {
		if claim.Evidence == "timeout-1" {
			return RunWait{}, RunLease{}, waitRefused("timed-out")
		}
		return s.durableJobStore.ResumeWait(ctx, claim)
	}},
	"job.restart.key": {fenced: func(s jobFault, ctx context.Context, input RunCommit, fence LeaseFence) (RunReceipt, error) {
		if input.RunID == "run-r" {
			input = withoutJob(input)
		}
		return s.durableJobStore.CommitFenced(ctx, input, fence)
	}},
	"job.limit": {fenced: func(s jobFault, ctx context.Context, input RunCommit, fence LeaseFence) (RunReceipt, error) {
		if input.Job != nil && input.Job.Operation == "op-64" {
			input = withoutJob(input)
		}
		return s.durableJobStore.CommitFenced(ctx, input, fence)
	}},
	"job.list.page": {list: func(s jobFault, ctx context.Context, resource, after string, limit int) (JobPage, error) {
		if resource != "run-k" {
			return s.durableJobStore.ListJobs(ctx, resource, after, limit)
		}
		if after != "" {
			return JobPage{Jobs: []RunJob{}}, nil
		}
		page, err := s.durableJobStore.ListJobs(ctx, resource, after, JobPageLimit)
		page.Next = ""
		return page, err
	}},
}

func (s jobFault) ExportJobs(ctx context.Context, resource string) (JobExport, error) {
	if s.export != nil {
		return s.export(s, ctx, resource)
	}
	return s.durableJobStore.ExportJobs(ctx, resource)
}

// bypassHistory: on the history path, where a store's claims really commit, a job registers while the check `when`
// selects is skipped.
func bypassHistory(when func(s jobFault, reg JobRegistration, run string) bool) jobFault {
	return jobFault{history: func(s jobFault, ctx context.Context, input RunHistoryCommit, fence LeaseFence) (RunHistoryReceipt, error) {
		reg := input.Run.Job
		if reg == nil || !when(s, *reg, input.Run.RunID) {
			return s.durableJobStore.CommitHistoryFenced(ctx, input, fence)
		}
		bare := input
		bare.Run = withoutJob(input.Run)
		receipt, err := s.durableJobStore.CommitHistoryFenced(ctx, bare, fence)
		if err == nil {
			s.jobs.Register(input.Run.RunID, *reg, fence)
		}
		return receipt, err
	}}
}

// staleRegister: a compare-and-set that fails still registers the job.
func staleRegister(s jobFault, ctx context.Context, input RunCommit, fence LeaseFence, err error) {
	if err == nil || input.Job == nil {
		return
	}
	if head, _ := s.durableJobStore.Load(ctx, input.RunID); head != nil && head.Revision != input.ExpectedRevision {
		s.jobs.Register(input.RunID, *input.Job, fence)
	}
}

// repeatRegisters: a repeated commit id with another registration is answered, and registers again.
func repeatRegisters(history bool) jobFault {
	seen := map[string]bool{}
	if history {
		return jobFault{history: func(s jobFault, ctx context.Context, input RunHistoryCommit, fence LeaseFence) (RunHistoryReceipt, error) {
			if input.Run.Job != nil && seen[input.Run.CommitID] {
				s.jobs.Register(input.Run.RunID, *input.Run.Job, fence)
				return RunHistoryReceipt{}, nil
			}
			receipt, err := s.durableJobStore.CommitHistoryFenced(ctx, input, fence)
			seen[input.Run.CommitID] = err == nil
			return receipt, err
		}}
	}
	return jobFault{fenced: func(s jobFault, ctx context.Context, input RunCommit, fence LeaseFence) (RunReceipt, error) {
		if input.Job != nil && seen[input.CommitID] {
			s.jobs.Register(input.RunID, *input.Job, fence)
			return RunReceipt{}, nil
		}
		receipt, err := s.durableJobStore.CommitFenced(ctx, input, fence)
		seen[input.CommitID] = err == nil
		return receipt, err
	}}
}

func staleOnceSettled(settled string) jobFault {
	last := map[string]*RunJob{}
	return jobFault{read: func(s jobFault, ctx context.Context, resource, job string) (*RunJob, error) {
		held, err := s.durableJobStore.ReadJob(ctx, resource, job)
		if held == nil || held.State != settled {
			last[job] = held
			return held, err
		}
		if stale, ok := last[job]; ok && stale != nil {
			return stale, nil
		}
		return held, err
	}}
}

// afterLastRestart counts the instances the kit builds: the third is the one its second restart builds.
func afterLastRestart(by func(*int) jobFault) jobFault {
	n := 0
	fault := by(&n)
	fault.opened = func() { n++ }
	return fault
}

func firstJobReads() jobFault {
	first := map[string]*RunJob{}
	return jobFault{read: func(s jobFault, ctx context.Context, resource, job string) (*RunJob, error) {
		key := resource + "/" + job
		if held, ok := first[key]; ok {
			return held, nil
		}
		held, err := s.durableJobStore.ReadJob(ctx, resource, job)
		first[key] = held
		return held, err
	}}
}

func firstJobLists() jobFault {
	first := map[string]JobPage{}
	return jobFault{list: func(s jobFault, ctx context.Context, resource, after string, limit int) (JobPage, error) {
		key := resource + "/" + after
		if held, ok := first[key]; ok {
			return held, nil
		}
		held, err := s.durableJobStore.ListJobs(ctx, resource, after, limit)
		first[key] = held
		return held, err
	}}
}

// jobStores: what a store does wrong beyond a single property, each with the property the kit must fail it on.
func jobStores() map[string]struct {
	property string
	fault    jobFault
} {
	return map[string]struct {
		property string
		fault    jobFault
	}{
		"E3 identity": {"job.history.identity", bypassHistory(func(_ jobFault, reg JobRegistration, run string) bool {
			return reg.Job != JobIdentity(run, reg.Operation)
		})},
		"E3 key":   {"job.history.key", bypassHistory(func(s jobFault, reg JobRegistration, _ string) bool { return s.jobs.KeyHeld(reg.KeyDigest) })},
		"E3 limit": {"job.history.limit", bypassHistory(func(s jobFault, _ JobRegistration, run string) bool { return len(s.jobs.rows[run]) >= JobRunLimit })},
		"E3b fence": {"job.history.fence", jobFault{history: func(s jobFault, ctx context.Context, input RunHistoryCommit, fence LeaseFence) (RunHistoryReceipt, error) {
			if input.Run.Job != nil {
				if lease, err := s.ReadLease(ctx, input.Run.RunID); err == nil && lease != nil {
					fence = FenceOf(*lease)
				}
			}
			return s.durableJobStore.CommitHistoryFenced(ctx, input, fence)
		}}},
		"E4 two heads": {"job.history.head", jobFault{history: func(s jobFault, ctx context.Context, input RunHistoryCommit, fence LeaseFence) (RunHistoryReceipt, error) {
			if input.Run.Job == nil {
				return s.durableJobStore.CommitHistoryFenced(ctx, input, fence)
			}
			s.jobs.Register(input.Run.RunID, *input.Run.Job, fence)
			return RunHistoryReceipt{}, nil
		}}},
		"E5 fenced": {"job.stale.fenced", jobFault{fenced: func(s jobFault, ctx context.Context, input RunCommit, fence LeaseFence) (RunReceipt, error) {
			receipt, err := s.durableJobStore.CommitFenced(ctx, input, fence)
			staleRegister(s, ctx, input, fence, err)
			return receipt, err
		}}},
		"E5 history": {"job.stale.history", jobFault{history: func(s jobFault, ctx context.Context, input RunHistoryCommit, fence LeaseFence) (RunHistoryReceipt, error) {
			receipt, err := s.durableJobStore.CommitHistoryFenced(ctx, input, fence)
			staleRegister(s, ctx, input.Run, fence, err)
			return receipt, err
		}}},
		"E6 export": {"job.export.reads", jobFault{export: func(s jobFault, ctx context.Context, resource string) (JobExport, error) {
			held, err := s.durableJobStore.ExportJobs(ctx, resource)
			for i := range held.Jobs {
				held.Jobs[i].Contradictions = nil
			}
			return held, err
		}}},
		"E7 expired": {"job.reclaim.expired", jobFault{reclaim: func(s jobFault, ctx context.Context, resource, job string, fence LeaseFence) (RunJob, error) {
			if lease, err := s.ReadLease(ctx, resource); err == nil && lease != nil && lease.Owner == fence.Owner && lease.Epoch == fence.Epoch {
				return s.jobs.Reclaim(resource, job, fence)
			}
			return s.durableJobStore.ReclaimJob(ctx, resource, job, fence)
		}}},
		"E7 stale": {"job.reclaim.stale", jobFault{reclaim: func(s jobFault, ctx context.Context, resource, job string, fence LeaseFence) (RunJob, error) {
			if lease, err := s.ReadLease(ctx, resource); err == nil && lease != nil && lease.Owner == fence.Owner && lease.Epoch != fence.Epoch {
				return s.jobs.Reclaim(resource, job, fence)
			}
			return s.durableJobStore.ReclaimJob(ctx, resource, job, fence)
		}}},
		"E8 fenced":  {"job.repeat.fenced", repeatRegisters(false)},
		"E8 history": {"job.repeat.history", repeatRegisters(true)},
		"E9 across": {"job.answer.across", jobFault{answer: func(s jobFault, ctx context.Context, input JobAnswer, fence LeaseFence) (RunJob, error) {
			for resource, rows := range s.jobs.rows {
				for _, row := range rows {
					if row.Job == input.Job {
						input.Resource = resource
					}
				}
			}
			return s.durableJobStore.AnswerJob(ctx, input, fence)
		}}},
		"the fence's run": {"job.answer.run", jobFault{answer: func(s jobFault, ctx context.Context, input JobAnswer, fence LeaseFence) (RunJob, error) {
			fence.Resource = input.Resource
			return s.durableJobStore.AnswerJob(ctx, input, fence)
		}}},
		"E10 deadline": {"job.wake.deadline", jobFault{complete: func(s jobFault, ctx context.Context, input JobCompletionEvidence) (RunJob, error) {
			job, err := s.durableJobStore.CompleteJob(ctx, input)
			for _, row := range s.waits.rows[input.Resource] {
				if row.State == "parked" && row.Deadline != nil && row.Pending.Job != nil && row.Pending.Job.Job == input.Job && job.Completion != nil {
					row.State, row.Disposition = "woken", &WakeDisposition{Evidence: input.Evidence, Kind: "event", At: job.Completion.At, Digest: job.Completion.Digest}
				}
			}
			return job, err
		}}},
		"E11 another run": {"job.park.other", jobFault{park: func(s jobFault, ctx context.Context, park RunWait, fence LeaseFence) (RunWait, AttentionEvent, error) {
			if park.Pending.Job != nil {
				for resource, rows := range s.jobs.rows {
					for _, row := range rows {
						if resource != park.Resource && row.Job == park.Pending.Job.Job {
							s.jobs.rows[park.Resource] = append(s.jobs.rows[park.Resource], row)
						}
					}
				}
			}
			return s.durableJobStore.ParkWait(ctx, park, fence)
		}}},
		"R-3 a completion refused took effect": {"job.complete.key", jobFault{complete: func(s jobFault, ctx context.Context, input JobCompletionEvidence) (RunJob, error) {
			job, err := s.durableJobStore.CompleteJob(ctx, input)
			var refused *JobRefused
			if errors.As(err, &refused) && refused.Reason == "unauthenticated" {
				for _, row := range s.jobs.rows[input.Resource] {
					if row.Job == input.Job {
						row.State, row.Completion = "completed", &JobCompletion{Evidence: input.Evidence, Outcome: input.Outcome, At: "2026-01-01T00:00:00.000Z", Digest: "sha256:" + strings.Repeat("a", 64)}
					}
				}
			}
			return job, err
		}}},
		"R-3 a reconciliation refused took effect": {"job.reconcile.key", jobFault{reconcile: func(s jobFault, ctx context.Context, input JobReconciliation) (RunJob, error) {
			job, err := s.durableJobStore.ReconcileJob(ctx, input)
			var refused *JobRefused
			if errors.As(err, &refused) && refused.Reason == "unauthenticated" {
				for _, row := range s.jobs.rows[input.Resource] {
					if row.Job == input.Job {
						evidence := input.Evidence
						row.State, row.Closed = "not-started", &JobFact{By: "reconcile", At: "2026-01-01T00:00:00.000Z", Digest: "sha256:" + strings.Repeat("a", 64), Evidence: &evidence}
					}
				}
			}
			return job, err
		}}},
		"R-3 an answer refused took effect": {"job.answer.other", jobFault{answer: func(s jobFault, ctx context.Context, input JobAnswer, fence LeaseFence) (RunJob, error) {
			job, err := s.durableJobStore.AnswerJob(ctx, input, fence)
			if err != nil && fence.Owner == "worker-x" {
				for _, row := range s.jobs.rows[input.Resource] {
					if row.Job == input.Job {
						row.State, row.Handle, row.Started = "started", input.Handle, &JobFact{By: "answer", At: "2026-01-01T00:00:00.000Z", Digest: "sha256:" + strings.Repeat("a", 64)}
					}
				}
			}
			return job, err
		}}},
		"R-3 a completion wakes whichever": {"job.wake.other", jobFault{complete: func(s jobFault, ctx context.Context, input JobCompletionEvidence) (RunJob, error) {
			job, err := s.durableJobStore.CompleteJob(ctx, input)
			for _, row := range s.waits.rows[input.Resource] {
				if input.Resource == "run-two" && row.State == "parked" && job.Completion != nil {
					row.State, row.Disposition = "woken", &WakeDisposition{Evidence: input.Evidence, Kind: "event", At: job.Completion.At, Digest: job.Completion.Digest}
				}
			}
			return job, err
		}}},
		"R-3 read stale once completed": {"job.read.completed", staleOnceSettled("completed")},
		"R-3 read stale once closed":    {"job.read.closed", staleOnceSettled("not-started")},
		"R-3 an answer ignores the epoch": {"job.answer.epoch", jobFault{answer: func(s jobFault, ctx context.Context, input JobAnswer, fence LeaseFence) (RunJob, error) {
			for _, row := range s.jobs.rows[input.Resource] {
				if row.Job == input.Job && row.Registered.Owner == fence.Owner {
					fence.Epoch = row.Registered.Epoch
					if row.Reclaimed != nil {
						fence.Epoch = row.Reclaimed.Epoch
					}
				}
			}
			return s.durableJobStore.AnswerJob(ctx, input, fence)
		}}},
		"R-3 a commit id forgotten": {"job.restart.commit", afterLastRestart(func(n *int) jobFault {
			return jobFault{fenced: func(s jobFault, ctx context.Context, input RunCommit, fence LeaseFence) (RunReceipt, error) {
				if *n >= 3 && input.CommitID == "run-after-op-1" {
					return RunReceipt{}, nil
				}
				return s.durableJobStore.CommitFenced(ctx, input, fence)
			}}
		})},
		"R-3 a wake key free for a job": {"job.restart.wakekey", afterLastRestart(func(n *int) jobFault {
			return jobFault{fenced: func(s jobFault, ctx context.Context, input RunCommit, fence LeaseFence) (RunReceipt, error) {
				if *n >= 3 && input.CommitID == "run-after-op-wake" {
					s.jobs.Register(input.RunID, *input.Job, fence)
					return RunReceipt{}, nil
				}
				return s.durableJobStore.CommitFenced(ctx, input, fence)
			}}
		})},
		"R-3 a job key free for a wait": {"job.restart.jobkey", afterLastRestart(func(n *int) jobFault {
			return jobFault{park: func(s jobFault, ctx context.Context, park RunWait, fence LeaseFence) (RunWait, AttentionEvent, error) {
				if *n >= 3 && park.Resource == "run-after" {
					return park, AttentionEvent{}, nil
				}
				return s.durableJobStore.ParkWait(ctx, park, fence)
			}}
		})},
		"R-3 a completion unauthenticated": {"job.restart.auth", afterLastRestart(func(n *int) jobFault {
			return jobFault{complete: func(s jobFault, ctx context.Context, input JobCompletionEvidence) (RunJob, error) {
				if *n >= 3 && input.Resource == "run-after" {
					held, err := s.durableJobStore.ReadJob(ctx, input.Resource, input.Job)
					if err != nil || held == nil {
						return RunJob{}, err
					}
					return *held, nil
				}
				return s.durableJobStore.CompleteJob(ctx, input)
			}}
		})},
		"E12 list": {"job.list.fresh", firstJobLists()},
		"E13 read": {"job.read.fresh", firstJobReads()},
		"a repeated contradiction id": {"job.contradiction.conflict", jobFault{complete: func(s jobFault, ctx context.Context, input JobCompletionEvidence) (RunJob, error) {
			job, err := s.durableJobStore.CompleteJob(ctx, input)
			var refused *JobRefused
			if input.Resource == "run-d" && errors.As(err, &refused) && refused.Reason == "conflict" {
				held, readErr := s.durableJobStore.ReadJob(ctx, input.Resource, input.Job)
				if readErr == nil && held != nil {
					return *held, nil
				}
			}
			return job, err
		}}},
	}
}

func TestJobConformanceKitFailsAStoreThatLosesAJobOrACompletion(t *testing.T) {
	for name, c := range jobStores() {
		t.Run(name, func(t *testing.T) {
			create := memoryJobKind(func(store *MemoryRunStore) JobRunStore {
				held := c.fault
				held.durableJobStore = durableJobStore{store}
				if held.opened != nil {
					held.opened()
				}
				return &held
			}, nil)
			var failure *StorageConformanceFailure
			if err := CheckRunStoreJobConformance(context.Background(), create); !errors.As(err, &failure) || failure.Property != c.property {
				t.Fatalf("got %v, want the property %s", err, c.property)
			}
		})
	}
}

func TestJobConformanceKitCertifiesTheMemoryKindRestartedOverACopy(t *testing.T) {
	if err := CheckRunStoreJobConformance(context.Background(), memoryJobKind(plainJobKind, nil)); err != nil {
		t.Fatal(err)
	}
}

func TestJobConformanceKitNamesTheGuaranteeAStoreLeavesOut(t *testing.T) {
	for property, fault := range jobFaults {
		t.Run(property, func(t *testing.T) {
			create := memoryJobKind(func(store *MemoryRunStore) JobRunStore {
				held := fault
				held.durableJobStore = durableJobStore{store}
				return &held
			}, nil)
			var failure *StorageConformanceFailure
			if err := CheckRunStoreJobConformance(context.Background(), create); !errors.As(err, &failure) || failure.Property != property {
				t.Fatalf("got %v, want the property %s", err, property)
			}
		})
	}
}

// sameInstance answers a restart with the store it was asked to restart.
func TestJobConformanceKitHoldsARestart(t *testing.T) {
	for _, c := range []struct {
		property string
		then     func(*MemoryRunStore)
	}{
		{"job.restart.job", func(fresh *MemoryRunStore) { fresh.jobs.rows = map[string][]*RunJob{} }},
		{"job.restart.job", func(fresh *MemoryRunStore) { delete(fresh.jobs.rows, "run-history") }},
		{"job.restart.record", func(fresh *MemoryRunStore) {
			for _, rows := range fresh.jobs.rows {
				for _, row := range rows {
					if row.State == "completed" && row.Started != nil {
						row.State, row.Completion = "started", nil
					}
				}
			}
		}},
		{"job.restart.record", func(fresh *MemoryRunStore) {
			for _, rows := range fresh.jobs.rows {
				for _, row := range rows {
					if row.State == "started" {
						other := "another-handle"
						row.Handle = &other
					}
				}
			}
		}},
		{"job.restart.record", func(fresh *MemoryRunStore) {
			for _, rows := range fresh.jobs.rows {
				for _, row := range rows {
					row.Reclaimed = nil
				}
			}
		}},
		{"job.restart.wait", func(fresh *MemoryRunStore) { fresh.waits.rows = map[string][]*RunWait{} }},
		{"job.restart.wait", func(fresh *MemoryRunStore) {
			for _, rows := range fresh.waits.rows {
				for _, row := range rows {
					if row.State != "parked" {
						row.State, row.Disposition = "parked", nil
					}
				}
			}
		}},
		{"job.restart.contradiction", func(fresh *MemoryRunStore) {
			for _, rows := range fresh.jobs.rows {
				for _, row := range rows {
					row.Contradictions = nil
				}
			}
		}},
	} {
		t.Run(c.property, func(t *testing.T) {
			var failure *StorageConformanceFailure
			if err := CheckRunStoreJobConformance(context.Background(), memoryJobKind(plainJobKind, c.then)); !errors.As(err, &failure) || failure.Property != c.property {
				t.Fatalf("got %v, want the property %s", err, c.property)
			}
		})
	}
	t.Run("job.restart.instance", func(t *testing.T) {
		kind := memoryJobKind(plainJobKind, nil)
		create := func(now func() time.Time, from JobRunStore) JobRunStore {
			if from != nil {
				return from
			}
			return kind(now, nil)
		}
		var failure *StorageConformanceFailure
		if err := CheckRunStoreJobConformance(context.Background(), create); !errors.As(err, &failure) || failure.Property != "job.restart.instance" {
			t.Fatalf("got %v", err)
		}
	})
}
