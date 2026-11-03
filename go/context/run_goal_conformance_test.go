package runcontext

import (
	"context"
	"errors"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

// #230: the goal conformance kit over the memory kind, a real restart over a fresh store instance, and for every
// property a store that leaves that one guarantee out. The TypeScript twin is
// packages/harness/storage/goalConformance.test.ts.
type durableGoalStore struct{ *MemoryRunStore }

func (durableGoalStore) LeaseLifetime() string { return LeaseLifetimeDurable }
func (durableGoalStore) WaitLifetime() string  { return WaitLifetimeDurable }
func (durableGoalStore) JobLifetime() string   { return JobLifetimeDurable }
func (durableGoalStore) GoalLifetime() string  { return GoalLifetimeDurable }

func goalRestarted(old *MemoryRunStore, now func() time.Time) *MemoryRunStore {
	fresh := restartedStore(old, now)
	old.mu.Lock()
	defer old.mu.Unlock()
	fresh.jobs.rows = deepCopied(old.jobs.rows)
	fresh.goals.rows = deepCopied(old.goals.rows)
	return fresh
}

// goalKind builds the memory kind, wrapped by fault; then changes what a restart keeps, and same answers the old
// instance as a restart.
func goalKind(fault func(*goalFault), then func(*MemoryRunStore), same bool) GoalStoreFactory {
	held := map[GoalRunStore]*MemoryRunStore{}
	return func(now func() time.Time, from GoalRunStore) GoalRunStore {
		if from != nil && same {
			return from
		}
		store := NewMemoryRunStore(MemoryRunStoreOptions{Now: now})
		if from != nil {
			store = goalRestarted(held[from], now)
			if then != nil {
				then(store)
			}
		}
		out := &goalFault{durableGoalStore: durableGoalStore{store}}
		if fault != nil {
			fault(out)
		}
		held[out] = store
		return out
	}
}

// goalFault breaks one thing: each field left nil forwards to the memory kind.
type goalFault struct {
	durableGoalStore
	version int
	command func(s *goalFault, ctx context.Context, input GoalTransition) (GoalAnswer, error)
	inspect func(s *goalFault, ctx context.Context, input GoalInspect) (*RunGoal, error)
	deliver func(s *goalFault, ctx context.Context, input GoalDeliveries) (RunGoal, error)
	read    func(s *goalFault, ctx context.Context, resource string) (*RunGoal, error)
	renew   func(s *goalFault, ctx context.Context, fence LeaseFence, ms int64) (RunLease, error)
	acquire func(s *goalFault, ctx context.Context, request LeaseAcquire) (RunLease, error)
	resume  func(s *goalFault, ctx context.Context, claim WaitClaim) (RunWait, RunLease, error)
	commit  func(s *goalFault, ctx context.Context, input RunCommit) (RunReceipt, error)
	fenced  func(s *goalFault, ctx context.Context, input RunCommit, fence LeaseFence) (RunReceipt, error)
	history func(s *goalFault, ctx context.Context, input RunHistoryCommit) (RunHistoryReceipt, error)
	abandon func(s *goalFault, ctx context.Context, input JobAbandon) (JobAbandonTaken, error)
}

func (s *goalFault) AbandonJobs(ctx context.Context, input JobAbandon) (JobAbandonTaken, error) {
	if s.abandon != nil {
		return s.abandon(s, ctx, input)
	}
	return s.durableGoalStore.AbandonJobs(ctx, input)
}

func (s *goalFault) CommitHistory(ctx context.Context, input RunHistoryCommit) (RunHistoryReceipt, error) {
	if s.history != nil {
		return s.history(s, ctx, input)
	}
	return s.durableGoalStore.CommitHistory(ctx, input)
}

// withoutGoal runs do with the run's goal record out of the table: a store that does not know the run has one.
func (s *goalFault) withoutGoal(resource string, do func()) {
	row := s.rowOf(resource)
	delete(s.MemoryRunStore.goals.rows, resource)
	defer func() {
		if row != nil {
			s.MemoryRunStore.goals.rows[resource] = row
		}
	}()
	do()
}

// reading: a goal transaction of these kinds reads the run's waits or jobs as edit leaves them for its length, and
// then the store holds them as they were: a store that reads less than it holds.
func reading(kinds []string, edit func(s *goalFault, input GoalTransition) func()) func(*goalFault) {
	return func(f *goalFault) {
		f.command = func(s *goalFault, ctx context.Context, input GoalTransition) (GoalAnswer, error) {
			if slices.Contains(kinds, input.Command.Kind) {
				defer edit(s, input)()
			}
			return s.durableGoalStore.CommandGoal(ctx, input)
		}
	}
}

// goalWokenAsResolved reads a woken wait of the run as resumed.
func goalWokenAsResolved(s *goalFault, input GoalTransition) func() {
	changed := []*RunWait{}
	for _, wait := range s.MemoryRunStore.waits.rows[input.Command.Resource] {
		if wait.State == "woken" {
			wait.State = "resumed"
			changed = append(changed, wait)
		}
	}
	return func() {
		for _, wait := range changed {
			wait.State = "woken"
		}
	}
}

func (s *goalFault) GoalVersion() int {
	if s.version != 0 {
		return s.version
	}
	return s.durableGoalStore.GoalVersion()
}
func (s *goalFault) CommandGoal(ctx context.Context, input GoalTransition) (GoalAnswer, error) {
	if s.command != nil {
		return s.command(s, ctx, input)
	}
	return s.durableGoalStore.CommandGoal(ctx, input)
}
func (s *goalFault) InspectGoal(ctx context.Context, input GoalInspect) (*RunGoal, error) {
	if s.inspect != nil {
		return s.inspect(s, ctx, input)
	}
	return s.durableGoalStore.InspectGoal(ctx, input)
}
func (s *goalFault) RecordGoalDeliveries(ctx context.Context, input GoalDeliveries) (RunGoal, error) {
	if s.deliver != nil {
		return s.deliver(s, ctx, input)
	}
	return s.durableGoalStore.RecordGoalDeliveries(ctx, input)
}
func (s *goalFault) ReadGoal(ctx context.Context, resource string) (*RunGoal, error) {
	if s.read != nil {
		return s.read(s, ctx, resource)
	}
	return s.durableGoalStore.ReadGoal(ctx, resource)
}
func (s *goalFault) RenewLease(ctx context.Context, fence LeaseFence, ms int64) (RunLease, error) {
	if s.renew != nil {
		return s.renew(s, ctx, fence, ms)
	}
	return s.durableGoalStore.RenewLease(ctx, fence, ms)
}
func (s *goalFault) AcquireLease(ctx context.Context, request LeaseAcquire) (RunLease, error) {
	if s.acquire != nil {
		return s.acquire(s, ctx, request)
	}
	return s.durableGoalStore.AcquireLease(ctx, request)
}
func (s *goalFault) ResumeWait(ctx context.Context, claim WaitClaim) (RunWait, RunLease, error) {
	if s.resume != nil {
		return s.resume(s, ctx, claim)
	}
	return s.durableGoalStore.ResumeWait(ctx, claim)
}
func (s *goalFault) Commit(ctx context.Context, input RunCommit) (RunReceipt, error) {
	if s.commit != nil {
		return s.commit(s, ctx, input)
	}
	return s.durableGoalStore.Commit(ctx, input)
}
func (s *goalFault) CommitFenced(ctx context.Context, input RunCommit, fence LeaseFence) (RunReceipt, error) {
	if s.fenced != nil {
		return s.fenced(s, ctx, input, fence)
	}
	return s.durableGoalStore.CommitFenced(ctx, input, fence)
}

// rowOf is the run's record as the table holds it, to break one rule in place.
func (s *goalFault) rowOf(resource string) *RunGoal { return s.MemoryRunStore.goals.rows[resource] }
func (s *goalFault) currentOf(resource string) *GoalObjective {
	row := s.rowOf(resource)
	if row == nil || len(row.Objectives) == 0 {
		return nil
	}
	return &row.Objectives[len(row.Objectives)-1]
}

var goalFaultStamp = GoalStamp{Command: "fault", Actor: "fault", At: "2026-01-01T00:00:00.000Z"}

// ahead: before the command reaches the table, before changes the record or the input; a fault of some kinds only.
func ahead(kinds []string, before func(s *goalFault, input *GoalTransition)) func(*goalFault) {
	return func(f *goalFault) {
		f.command = func(s *goalFault, ctx context.Context, input GoalTransition) (GoalAnswer, error) {
			if slices.Contains(kinds, input.Command.Kind) {
				before(s, &input)
			}
			return s.durableGoalStore.CommandGoal(ctx, input)
		}
	}
}

// after: the answer as the store hands it back, changed.
func after(kinds []string, edit func(answer *GoalAnswer)) func(*goalFault) {
	return func(f *goalFault) {
		f.command = func(s *goalFault, ctx context.Context, input GoalTransition) (GoalAnswer, error) {
			answer, err := s.durableGoalStore.CommandGoal(ctx, input)
			if err == nil && slices.Contains(kinds, input.Command.Kind) {
				edit(&answer)
			}
			return answer, err
		}
	}
}
func goalCurrent(answer *GoalAnswer) *GoalObjective {
	return &answer.Goal.Objectives[len(answer.Goal.Objectives)-1]
}
func goalHeadNow(s *goalFault, ctx context.Context, resource string) *GoalHead {
	head, _ := s.Load(ctx, resource)
	return &GoalHead{Revision: head.Revision, Digest: CheckpointBytesDigest(head.Checkpoint)}
}
func goalOpenJobs(s *goalFault, resource string) []string {
	open := []string{}
	for _, job := range s.MemoryRunStore.jobs.rows[resource] {
		if goalUnsettledJob(*job) {
			open = append(open, job.Job)
		}
	}
	return open
}

// goalFaults: for each property of the kit, a store that breaks exactly it.
var goalFaults = map[string]func(*goalFault){
	// #313's review: H-1, H-2 and the seven stores of M-2, with M-1.
	"goal.achieve.read": ahead([]string{"achieve"}, func(s *goalFault, input *GoalTransition) {
		if !input.Command.ExpectedRevision.Null {
			read := input.Command.ExpectedRevision.Value
			input.Read = &read
		}
	}),
	// #325 LOW-6: an abandon that counts only a commit, a lease, a wait or a job as a run seen.
	"goal.abandon.unleased": func(f *goalFault) {
		f.abandon = func(s *goalFault, ctx context.Context, input JobAbandon) (JobAbandonTaken, error) {
			head, err := s.Load(ctx, input.Resource)
			lease, _ := s.ReadLease(ctx, input.Resource)
			if err == nil && head == nil && lease == nil {
				return JobAbandonTaken{Jobs: []RunJob{}}, nil
			}
			return s.durableGoalStore.AbandonJobs(ctx, input)
		}
	},
	"goal.repeat.current": func(f *goalFault) {
		seen := map[string]GoalAnswer{}
		f.command = func(s *goalFault, ctx context.Context, input GoalTransition) (GoalAnswer, error) {
			key := string(goalJSON(input.Command))
			if answer, ok := seen[key]; ok {
				answer.Goal = deepCloneGoal(answer.Goal)
				return answer, nil
			}
			answer, err := s.durableGoalStore.CommandGoal(ctx, input)
			if err == nil {
				answer.Goal = deepCloneGoal(answer.Goal)
				seen[key] = answer
			}
			return answer, err
		}
	},
	"goal.unfenced.history": func(f *goalFault) {
		f.history = func(s *goalFault, ctx context.Context, input RunHistoryCommit) (receipt RunHistoryReceipt, err error) {
			s.withoutGoal(input.Run.RunID, func() { receipt, err = s.durableGoalStore.CommitHistory(ctx, input) })
			return receipt, err
		}
	},
	"goal.unfenced.terminal": func(f *goalFault) {
		f.commit = func(s *goalFault, ctx context.Context, input RunCommit) (receipt RunReceipt, err error) {
			if current := s.currentOf(input.RunID); current != nil && goalTerminal(current.State) {
				s.withoutGoal(input.RunID, func() { receipt, err = s.durableGoalStore.Commit(ctx, input) })
				return receipt, err
			}
			return s.durableGoalStore.Commit(ctx, input)
		}
	},
	"goal.achieve.unproven": reading([]string{"achieve"}, func(s *goalFault, input GoalTransition) func() {
		held := s.MemoryRunStore.jobs.rows[input.Command.Resource]
		s.MemoryRunStore.jobs.rows[input.Command.Resource] = slices.DeleteFunc(slices.Clone(held), func(job *RunJob) bool { return job.State == "unproven" })
		return func() { s.MemoryRunStore.jobs.rows[input.Command.Resource] = held }
	}),
	"goal.achieve.woken": reading([]string{"achieve"}, goalWokenAsResolved),
	"goal.abandon.woken": reading([]string{"abandon"}, goalWokenAsResolved),
	"goal.create.woken":  reading([]string{"create"}, goalWokenAsResolved),
	"goal.create.later": reading([]string{"create"}, func(s *goalFault, input GoalTransition) func() {
		left := map[string]bool{}
		if current := s.currentOf(input.Command.Resource); current != nil && current.Abandoned != nil {
			for _, wait := range current.Abandoned.Waits {
				left[wait.Wait] = true
			}
		}
		changed := []*RunWait{}
		for _, wait := range s.MemoryRunStore.waits.rows[input.Command.Resource] {
			if !left[wait.Wait] && wait.State == "parked" {
				wait.State = "resumed"
				changed = append(changed, wait)
			}
		}
		return func() {
			for _, wait := range changed {
				wait.State = "parked"
			}
		}
	}),
	"goal.terminal.pause": func(f *goalFault) {
		f.command = func(s *goalFault, ctx context.Context, input GoalTransition) (GoalAnswer, error) {
			var paused *GoalStamp
			if current := s.currentOf(input.Command.Resource); current != nil && current.Paused != nil {
				held := *current.Paused
				paused = &held
			}
			answer, err := s.durableGoalStore.CommandGoal(ctx, input)
			if current := s.currentOf(input.Command.Resource); err == nil && paused != nil && current != nil && goalTerminal(current.State) {
				current.Paused = paused
			}
			return answer, err
		}
	},
	"goal.declared": func(f *goalFault) { f.version = 2 },
	"goal.create.commits": func(f *goalFault) {
		f.read = func(*goalFault, context.Context, string) (*RunGoal, error) { return nil, nil }
	},
	"goal.create.cas": ahead([]string{"create"}, func(s *goalFault, input *GoalTransition) {
		if row := s.rowOf(input.Command.Resource); row != nil {
			input.Command.ExpectedRevision = goalKitRevision(row.Revision)
		}
	}),
	"goal.create.exists": ahead([]string{"create"}, func(s *goalFault, input *GoalTransition) {
		row, current := s.rowOf(input.Command.Resource), s.currentOf(input.Command.Resource)
		if current != nil && !goalTerminal(current.State) && !input.Command.ExpectedRevision.Null && row.Revision == input.Command.ExpectedRevision.Value {
			current.State, current.Paused, current.Blocked = "abandoned", nil, nil
			current.Abandoned = &GoalAbandonment{Command: "fault", Actor: "fault", At: goalFaultStamp.At, Waits: []GoalLeftWait{}, Jobs: []GoalJob{}}
		}
	}),
	"goal.repeat": func(f *goalFault) {
		f.command = func(s *goalFault, ctx context.Context, input GoalTransition) (GoalAnswer, error) {
			if row := s.rowOf(input.Command.Resource); row != nil && slices.ContainsFunc(row.Journal, func(e GoalEntry) bool { return e.Command == input.Command.Command }) {
				input.Command.Command += "-again"
			}
			return s.durableGoalStore.CommandGoal(ctx, input)
		}
	},
	"goal.conflict": func(f *goalFault) {
		f.command = func(s *goalFault, ctx context.Context, input GoalTransition) (GoalAnswer, error) {
			answer, err := s.durableGoalStore.CommandGoal(ctx, input)
			if goalRefusedWith(err, "conflict") {
				row := s.rowOf(input.Command.Resource)
				at := slices.IndexFunc(row.Journal, func(e GoalEntry) bool { return e.Command == input.Command.Command })
				return GoalAnswer{Goal: deepCloneGoal(*row), Entry: row.Journal[at]}, nil
			}
			return answer, err
		}
	},
	"goal.refusal.repeat": func(f *goalFault) {
		f.command = func(s *goalFault, ctx context.Context, input GoalTransition) (GoalAnswer, error) {
			if row := s.rowOf(input.Command.Resource); row != nil {
				row.Journal = slices.DeleteFunc(row.Journal, func(e GoalEntry) bool { return e.Command == input.Command.Command && e.Outcome != "accepted" })
			}
			return s.durableGoalStore.CommandGoal(ctx, input)
		}
	},
	"goal.unfenced": func(f *goalFault) {
		f.commit = func(s *goalFault, ctx context.Context, input RunCommit) (RunReceipt, error) {
			row := s.rowOf(input.RunID)
			delete(s.MemoryRunStore.goals.rows, input.RunID)
			defer func() {
				if row != nil {
					s.MemoryRunStore.goals.rows[input.RunID] = row
				}
			}()
			return s.durableGoalStore.Commit(ctx, input)
		}
	},
	"goal.pause.commits": after([]string{"pause"}, func(answer *GoalAnswer) { answer.Lease = nil }),
	"goal.pause.twice": ahead([]string{"pause"}, func(s *goalFault, input *GoalTransition) {
		if current := s.currentOf(input.Command.Resource); current != nil {
			current.Paused = nil
		}
	}),
	"goal.pause.held": func(f *goalFault) {
		f.renew = func(s *goalFault, ctx context.Context, fence LeaseFence, ms int64) (RunLease, error) {
			if current := s.currentOf(fence.Resource); current != nil && current.Paused != nil {
				return RunLease{}, goalRefused("objective-paused")
			}
			return s.durableGoalStore.RenewLease(ctx, fence, ms)
		}
	},
	"goal.pause.acquire": func(f *goalFault) {
		f.acquire = func(s *goalFault, ctx context.Context, request LeaseAcquire) (RunLease, error) {
			if current := s.currentOf(request.Resource); current != nil && current.Paused != nil {
				paused := current.Paused
				current.Paused = nil
				defer func() { current.Paused = paused }()
			}
			return s.durableGoalStore.AcquireLease(ctx, request)
		}
	},
	"goal.resume.commits": func(f *goalFault) {
		f.command = func(s *goalFault, ctx context.Context, input GoalTransition) (GoalAnswer, error) {
			answer, err := s.durableGoalStore.CommandGoal(ctx, input)
			if err == nil && input.Command.Kind == "resume" {
				if row, ok := s.MemoryRunStore.leases.rows[input.Command.Resource]; ok {
					row.State = "held"
					s.MemoryRunStore.leases.rows[input.Command.Resource] = row
				}
			}
			return answer, err
		}
	},
	"goal.resume.none": ahead([]string{"resume"}, func(s *goalFault, input *GoalTransition) {
		if current := s.currentOf(input.Command.Resource); current != nil && current.Paused == nil {
			stamp := goalFaultStamp
			current.Paused = &stamp
		}
	}),
	"goal.block": after([]string{"block"}, func(answer *GoalAnswer) { goalCurrent(answer).Blocked.Condition = "" }),
	"goal.block.twice": ahead([]string{"block"}, func(s *goalFault, input *GoalTransition) {
		if current := s.currentOf(input.Command.Resource); current != nil && current.State == "blocked" {
			current.State, current.Blocked = "active", nil
		}
	}),
	"goal.block.achieve": ahead([]string{"achieve"}, func(s *goalFault, input *GoalTransition) {
		if current := s.currentOf(input.Command.Resource); current != nil && current.State == "blocked" {
			current.State, current.Blocked = "active", nil
		}
	}),
	"goal.unblock": after([]string{"unblock"}, func(answer *GoalAnswer) { goalCurrent(answer).State = "blocked" }),
	"goal.unblock.none": ahead([]string{"unblock"}, func(s *goalFault, input *GoalTransition) {
		if current := s.currentOf(input.Command.Resource); current != nil && current.State == "active" {
			current.State = "blocked"
			current.Blocked = &GoalBlock{Command: "fault", Actor: "fault", At: goalFaultStamp.At, Condition: "fault"}
		}
	}),
	"goal.achieve.unbound": ahead([]string{"achieve"}, func(s *goalFault, input *GoalTransition) {
		if current := s.currentOf(input.Command.Resource); current != nil && current.State == "active" && current.Plan == nil {
			current.Plan = &GoalPlan{ID: "fault-plan", Revision: "sha256:" + strings.Repeat("d", 64), Steps: []GoalStep{{Step: "step-1", Index: 0}}, Bound: goalFaultStamp}
		}
	}),
	"goal.bind.head": func(f *goalFault) {
		f.command = func(s *goalFault, ctx context.Context, input GoalTransition) (GoalAnswer, error) {
			if input.Command.Kind == "bind" {
				input.Command.Head = goalHeadNow(s, ctx, input.Command.Resource)
			}
			return s.durableGoalStore.CommandGoal(ctx, input)
		}
	},
	"goal.bind.finding": ahead([]string{"bind"}, func(s *goalFault, input *GoalTransition) {
		if input.Refused != nil {
			input.Refused = nil
			input.Plan = &GoalPlanSteps{ID: "fault-plan", Revision: "sha256:" + strings.Repeat("d", 64), Steps: []GoalStep{{Step: "step-1", Index: 0}}}
		}
	}),
	"goal.bind.commits": after([]string{"bind"}, func(answer *GoalAnswer) { goalCurrent(answer).Plan.Bound = GoalStamp{} }),
	"goal.bind.twice": ahead([]string{"bind"}, func(s *goalFault, input *GoalTransition) {
		if current := s.currentOf(input.Command.Resource); current != nil {
			current.Plan = nil
		}
	}),
	"goal.achieve.head": func(f *goalFault) {
		f.command = func(s *goalFault, ctx context.Context, input GoalTransition) (GoalAnswer, error) {
			if input.Command.Kind == "achieve" {
				input.Command.Head = goalHeadNow(s, ctx, input.Command.Resource)
			}
			return s.durableGoalStore.CommandGoal(ctx, input)
		}
	},
	"goal.achieve.finding": ahead([]string{"achieve"}, func(s *goalFault, input *GoalTransition) { input.Refused = nil }),
	"goal.achieve.commits": after([]string{"achieve"}, func(answer *GoalAnswer) {
		if done := goalCurrent(answer).Achieved; done != nil {
			done.Head = GoalHead{}
		}
	}),
	"goal.terminal": ahead([]string{"pause"}, func(s *goalFault, input *GoalTransition) {
		if current := s.currentOf(input.Command.Resource); current != nil && current.State == "achieved" {
			current.State, current.Achieved = "active", nil
		}
	}),
	"goal.create.reused": ahead([]string{"create"}, func(s *goalFault, input *GoalTransition) {
		if row := s.rowOf(input.Command.Resource); row != nil {
			for i := range row.Objectives {
				if row.Objectives[i].ID == *input.Command.Objective {
					row.Objectives[i].ID += "-renamed"
				}
			}
		}
	}),
	"goal.create.terminal": func(f *goalFault) {
		f.command = func(s *goalFault, ctx context.Context, input GoalTransition) (GoalAnswer, error) {
			if row, current := s.rowOf(input.Command.Resource), s.currentOf(input.Command.Resource); input.Command.Kind == "create" && current != nil && current.State == "achieved" && !slices.ContainsFunc(row.Objectives, func(o GoalObjective) bool { return o.ID == *input.Command.Objective }) {
				return GoalAnswer{}, goalRefused("objective-exists")
			}
			return s.durableGoalStore.CommandGoal(ctx, input)
		}
	},
	"goal.bind.reused": ahead([]string{"bind"}, func(s *goalFault, input *GoalTransition) {
		if row := s.rowOf(input.Command.Resource); row != nil {
			for i := 0; i < len(row.Objectives)-1; i++ {
				if row.Objectives[i].Plan != nil {
					row.Objectives[i].Plan.ID += "-renamed"
				}
			}
		}
	}),
	"goal.achieve.jobs": ahead([]string{"achieve"}, func(s *goalFault, input *GoalTransition) {
		if open := goalOpenJobs(s, input.Command.Resource); len(open) > 0 {
			input.Command.Waive = open
		}
	}),
	"goal.achieve.waiver": ahead([]string{"achieve"}, func(s *goalFault, input *GoalTransition) {
		if input.Command.Waive != nil {
			open := goalOpenJobs(s, input.Command.Resource)
			input.Command.Waive = slices.DeleteFunc(slices.Clone(input.Command.Waive), func(job string) bool { return !slices.Contains(open, job) })
			if len(input.Command.Waive) == 0 {
				input.Command.Waive = nil
			}
		}
	}),
	"goal.achieve.wait": func(f *goalFault) {
		f.command = func(s *goalFault, ctx context.Context, input GoalTransition) (GoalAnswer, error) {
			if input.Command.Kind == "achieve" {
				rows := s.MemoryRunStore.waits.rows[input.Command.Resource]
				held := []string{}
				for _, row := range rows {
					held = append(held, row.State)
					row.State = "resumed"
				}
				defer func() {
					for i, row := range rows {
						row.State = held[i]
					}
				}()
			}
			return s.durableGoalStore.CommandGoal(ctx, input)
		}
	},
	"goal.abandon.left": after([]string{"abandon"}, func(answer *GoalAnswer) { goalCurrent(answer).Abandoned.Waits = []GoalLeftWait{} }),
	"goal.create.unresolved": ahead([]string{"create"}, func(s *goalFault, input *GoalTransition) {
		if current := s.currentOf(input.Command.Resource); current != nil && current.Abandoned != nil && goalDelivered(s.rowOf(input.Command.Resource), current.Abandoned.Command) {
			current.Abandoned.Waits = []GoalLeftWait{}
		}
	}),
	// #318: M-4, a create over an abandon whose deliveries were never journaled; L-8, a create over an achieved
	// objective.
	"goal.create.unfinished": ahead([]string{"create"}, func(s *goalFault, input *GoalTransition) {
		row, current := s.rowOf(input.Command.Resource), s.currentOf(input.Command.Resource)
		if current != nil && current.State == "abandoned" && current.Abandoned != nil && !goalDelivered(row, current.Abandoned.Command) {
			row.Journal = append(row.Journal, GoalEntry{Command: current.Abandoned.Command, Kind: "abandon", Actor: "fault", At: goalFaultStamp.At, Objective: current.ID, Outcome: "delivered", Revision: row.Revision, Digest: "sha256:" + strings.Repeat("f", 64), Deliveries: []GoalDelivery{}})
		}
	}),
	"goal.create.achieved": reading([]string{"create"}, func(s *goalFault, input GoalTransition) func() {
		current := s.currentOf(input.Command.Resource)
		if current == nil || current.State != "achieved" {
			return func() {}
		}
		changed := []*RunWait{}
		for _, wait := range s.MemoryRunStore.waits.rows[input.Command.Resource] {
			if wait.State == "parked" {
				wait.State = "resumed"
				changed = append(changed, wait)
			}
		}
		return func() {
			for _, wait := range changed {
				wait.State = "parked"
			}
		}
	}),
	"goal.deliveries": func(f *goalFault) {
		f.deliver = func(s *goalFault, _ context.Context, input GoalDeliveries) (RunGoal, error) {
			return deepCloneGoal(*s.rowOf(input.Resource)), nil
		}
	},
	"goal.deliveries.other": func(f *goalFault) {
		f.deliver = func(s *goalFault, ctx context.Context, input GoalDeliveries) (RunGoal, error) {
			if current := s.currentOf(input.Resource); current != nil && current.Abandoned != nil {
				input.Command = current.Abandoned.Command
			}
			return s.durableGoalStore.RecordGoalDeliveries(ctx, input)
		}
	},
	"goal.pause.resume-wait": func(f *goalFault) {
		f.resume = func(s *goalFault, ctx context.Context, claim WaitClaim) (RunWait, RunLease, error) {
			if current := s.currentOf(claim.Resource); current != nil && current.Paused != nil {
				paused := current.Paused
				current.Paused = nil
				defer func() { current.Paused = paused }()
			}
			return s.durableGoalStore.ResumeWait(ctx, claim)
		}
	},
	"goal.none": func(f *goalFault) {
		f.command = func(s *goalFault, ctx context.Context, input GoalTransition) (GoalAnswer, error) {
			answer, err := s.durableGoalStore.CommandGoal(ctx, input)
			if err != nil && s.rowOf(input.Command.Resource) == nil {
				s.MemoryRunStore.goals.rows[input.Command.Resource] = &RunGoal{Version: 1, Kind: "goal", Resource: input.Command.Resource, Revision: 1, Objectives: []GoalObjective{}, Journal: []GoalEntry{}}
			}
			return answer, err
		}
	},
	"goal.inspect.none": func(f *goalFault) {
		f.inspect = func(s *goalFault, ctx context.Context, input GoalInspect) (*RunGoal, error) {
			held, err := s.durableGoalStore.InspectGoal(ctx, input)
			if held == nil && err == nil {
				return &RunGoal{}, nil
			}
			return held, err
		}
	},
	"goal.inspect.count": func(f *goalFault) {
		f.inspect = func(s *goalFault, ctx context.Context, input GoalInspect) (*RunGoal, error) {
			if row := s.rowOf(input.Resource); row != nil {
				row.Revision++
			}
			return s.durableGoalStore.InspectGoal(ctx, input)
		}
	},
	"goal.journal.bound": func(f *goalFault) {
		f.command = func(s *goalFault, ctx context.Context, input GoalTransition) (GoalAnswer, error) {
			if row := s.rowOf(input.Command.Resource); row != nil && len(row.Journal) >= GoalJournalLimit {
				row.Journal = row.Journal[1:]
				discarded := int64(1)
				if row.Discarded != nil {
					discarded += *row.Discarded
				}
				row.Discarded = &discarded
			}
			return s.durableGoalStore.CommandGoal(ctx, input)
		}
	},
	"goal.exhausted": ahead([]string{"create"}, func(s *goalFault, input *GoalTransition) {
		if row := s.rowOf(input.Command.Resource); row != nil && len(row.Objectives) >= GoalObjectiveLimit {
			row.Objectives = row.Objectives[1:]
		}
	}),
	"goal.owner": func(f *goalFault) {
		f.read = func(s *goalFault, ctx context.Context, resource string) (*RunGoal, error) {
			held, err := s.durableGoalStore.ReadGoal(ctx, resource)
			if held == nil && err == nil && resource == "run-other" {
				for _, row := range s.MemoryRunStore.goals.rows {
					copied := deepCloneGoal(*row)
					return &copied, nil
				}
			}
			return held, err
		}
	},
}

// goalRestarts: what a restart loses, each naming the property the kit must fail it on.
var goalRestarts = map[string]func(*goalFault){
	"goal.restart.paused": func(f *goalFault) {
		f.acquire = func(s *goalFault, ctx context.Context, request LeaseAcquire) (RunLease, error) {
			if current := s.currentOf(request.Resource); current != nil && current.Paused != nil {
				paused := current.Paused
				current.Paused = nil
				defer func() { current.Paused = paused }()
			}
			return s.durableGoalStore.AcquireLease(ctx, request)
		}
	},
	"goal.restart.unfenced": goalFaults["goal.unfenced"],
	"goal.restart.repeat": func(f *goalFault) {
		f.command = func(s *goalFault, ctx context.Context, input GoalTransition) (GoalAnswer, error) {
			input.Command.Command += "-forgotten"
			return s.durableGoalStore.CommandGoal(ctx, input)
		}
	},
	"goal.restart.conflict": goalFaults["goal.conflict"],
	"goal.restart.commits": func(f *goalFault) {
		f.command = func(s *goalFault, ctx context.Context, input GoalTransition) (GoalAnswer, error) {
			if input.Command.Kind == "resume" {
				return GoalAnswer{}, goalRefused("not-paused")
			}
			return s.durableGoalStore.CommandGoal(ctx, input)
		}
	},
}

// goalAfterRestart applies a fault only to the instances a restart builds.
func goalAfterRestart(fault func(*goalFault)) GoalStoreFactory {
	held := map[GoalRunStore]*MemoryRunStore{}
	return func(now func() time.Time, from GoalRunStore) GoalRunStore {
		store := NewMemoryRunStore(MemoryRunStoreOptions{Now: now})
		if from != nil {
			store = goalRestarted(held[from], now)
		}
		out := &goalFault{durableGoalStore: durableGoalStore{store}}
		if from != nil {
			fault(out)
		}
		held[out] = store
		return out
	}
}

// goalStores: what a store does wrong beyond a single property, each naming the property the kit must fail it on.
var goalStores = []struct {
	name, property string
	fault          func(*goalFault)
}{
	{"a store that does not journal refusals", "goal.create.cas", func(f *goalFault) {
		f.command = func(s *goalFault, ctx context.Context, input GoalTransition) (GoalAnswer, error) {
			row := s.rowOf(input.Command.Resource)
			length := -1
			if row != nil {
				length = len(row.Journal)
			}
			answer, err := s.durableGoalStore.CommandGoal(ctx, input)
			if err != nil && row != nil {
				row.Journal = row.Journal[:length]
			}
			return answer, err
		}
	}},
	{"a store that stamps a refusal with the caller's revision", "goal.create.exists", func(f *goalFault) {
		f.command = func(s *goalFault, ctx context.Context, input GoalTransition) (GoalAnswer, error) {
			answer, err := s.durableGoalStore.CommandGoal(ctx, input)
			if row := s.rowOf(input.Command.Resource); err != nil && row != nil && len(row.Journal) > 0 && row.Journal[len(row.Journal)-1].Command == input.Command.Command {
				row.Journal[len(row.Journal)-1].Revision = input.Command.ExpectedRevision.Value + 1
			}
			return answer, err
		}
	}},
	{"a store that drops a failed job from an achievement", "goal.achieve.jobs", after([]string{"achieve"}, func(answer *GoalAnswer) {
		if done := goalCurrent(answer).Achieved; done != nil {
			done.Unsuccessful = slices.DeleteFunc(done.Unsuccessful, func(job GoalJob) bool { return job.State == "completed" })
		}
	})},
	{"a store that counts a contradiction as success", "goal.achieve.jobs", after([]string{"achieve"}, func(answer *GoalAnswer) {
		if done := goalCurrent(answer).Achieved; done != nil {
			done.Unsuccessful = slices.DeleteFunc(done.Unsuccessful, func(job GoalJob) bool { return job.Contradicted != nil })
		}
	})},
	{"a store that counts a repeated inspection", "goal.inspect.count", func(f *goalFault) {
		f.inspect = func(s *goalFault, ctx context.Context, input GoalInspect) (*RunGoal, error) {
			if row := s.rowOf(input.Resource); row != nil && row.Inspections != nil {
				row.Inspections.Last.Command = "forgotten"
			}
			return s.durableGoalStore.InspectGoal(ctx, input)
		}
	}},
	{"a store that refuses for size", "goal.journal.bound", func(f *goalFault) {
		f.command = func(s *goalFault, ctx context.Context, input GoalTransition) (GoalAnswer, error) {
			if row := s.rowOf(input.Command.Resource); row != nil && len(row.Journal) >= GoalJournalLimit {
				return GoalAnswer{}, goalInvalid()
			}
			return s.durableGoalStore.CommandGoal(ctx, input)
		}
	}},
	{"a store whose pause also stops a held lease from committing", "goal.pause.held", func(f *goalFault) {
		f.fenced = func(s *goalFault, ctx context.Context, input RunCommit, fence LeaseFence) (RunReceipt, error) {
			if current := s.currentOf(input.RunID); current != nil && current.Paused != nil {
				return RunReceipt{}, goalRefused("objective-paused")
			}
			return s.durableGoalStore.CommitFenced(ctx, input, fence)
		}
	}},
	{"a store that answers a refusal of the head", "goal.bind.head", func(f *goalFault) {
		f.command = func(s *goalFault, ctx context.Context, input GoalTransition) (GoalAnswer, error) {
			answer, err := s.durableGoalStore.CommandGoal(ctx, input)
			if goalRefusedWith(err, "head-changed") {
				return GoalAnswer{Goal: deepCloneGoal(*s.rowOf(input.Command.Resource)), Entry: GoalEntry{Command: input.Command.Command, Outcome: "accepted"}}, nil
			}
			return answer, err
		}
	}},
}

func goalKitFails(t *testing.T, create GoalStoreFactory) string {
	t.Helper()
	err := CheckRunStoreGoalConformance(context.Background(), create)
	if err == nil {
		return ""
	}
	var failure *StorageConformanceFailure
	if !errors.As(err, &failure) {
		t.Fatal(err)
	}
	return failure.Property
}

func TestGoalConformancePassesTheMemoryKindAcrossARestart(t *testing.T) {
	if property := goalKitFails(t, goalKind(nil, nil, false)); property != "" {
		t.Fatal(property)
	}
}

func TestGoalConformanceFailsEachBrokenStore(t *testing.T) {
	names := []string{}
	for name := range goalFaults {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, property := range names {
		t.Run(property, func(t *testing.T) {
			if got := goalKitFails(t, goalKind(goalFaults[property], nil, false)); got != property {
				t.Fatalf("failed on %q", got)
			}
		})
	}
	t.Run("goal.restart.instance", func(t *testing.T) {
		if got := goalKitFails(t, goalKind(nil, nil, true)); got != "goal.restart.instance" {
			t.Fatalf("failed on %q", got)
		}
	})
	t.Run("goal.restart.record", func(t *testing.T) {
		if got := goalKitFails(t, goalKind(nil, func(fresh *MemoryRunStore) { fresh.goals.rows = map[string]*RunGoal{} }, false)); got != "goal.restart.record" {
			t.Fatalf("failed on %q", got)
		}
	})
	for property, fault := range goalRestarts {
		t.Run(property, func(t *testing.T) {
			if got := goalKitFails(t, goalAfterRestart(fault)); got != property {
				t.Fatalf("failed on %q", got)
			}
		})
	}
	for _, row := range goalStores {
		t.Run(row.name, func(t *testing.T) {
			if got := goalKitFails(t, goalKind(row.fault, nil, false)); got != row.property {
				t.Fatalf("failed on %q, want %q", got, row.property)
			}
		})
	}
}

func TestGoalConformanceNamesEveryPropertyWithABrokenStore(t *testing.T) {
	raw, err := os.ReadFile("run_goal_conformance.go")
	if err != nil {
		t.Fatal(err)
	}
	covered := map[string]bool{"goal.operation": true, "goal.restart.instance": true, "goal.restart.record": true}
	for name := range goalFaults {
		covered[name] = true
	}
	for name := range goalRestarts {
		covered[name] = true
	}
	for _, match := range regexp.MustCompile(`"(goal\.[a-z.-]+)"`).FindAllStringSubmatch(string(raw), -1) {
		if !covered[match[1]] {
			t.Fatal("no broken store for", match[1])
		}
	}
}

// goalDelivered: the record journals deliveries of that abandon command.
func goalDelivered(row *RunGoal, command string) bool {
	return row != nil && slices.ContainsFunc(row.Journal, func(entry GoalEntry) bool { return entry.Command == command && entry.Outcome == "delivered" })
}
