package runcontext

import (
	"context"
	"errors"
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
	park      func(s jobFault, ctx context.Context, park RunWait, fence LeaseFence) (RunWait, AttentionEvent, error)
	wake      func(s jobFault, ctx context.Context, evidence WakeEvidence) (WakeDisposition, error)
	resume    func(s jobFault, ctx context.Context, claim WaitClaim) (RunWait, RunLease, error)
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
		if fence.Owner == "worker-a" {
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
