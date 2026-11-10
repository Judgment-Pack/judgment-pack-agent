package runcontext

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
)

// #315: what a worker's view may command, and the abandon's remaining edges. L-3: a command sent through a fenced
// view presents that view's fence, and a worker a takeover displaced is refused *LeaseRefused with nothing
// journaled, seized, sent or changed. L-2: a retry from a new process after a crash between the abandon's sends and
// their journal hands cancel the same operation. L-6: a terminal objective's jobs are the host's AbandonJobs to
// stop. #321 L-3: M-4 against a second abandon. The TypeScript twin is packages/harness/storage/goalFence.test.ts.
const (
	fenceRun   = "run-f"
	fenceOther = "run-o"
	fenceKey   = "fence.job-a.Q7z2Xm9LpV4cR8tNb3Yh6Df1"
	fenceHour  = int64(3600000)
)

// fenceStore is the memory kind held as a durable one, whose next RecordGoalDeliveries ends the process while
// crash is set.
type fenceStore struct {
	durableGoalStore
	crash bool
}

func (s *fenceStore) RecordGoalDeliveries(ctx context.Context, input GoalDeliveries) (RunGoal, error) {
	if s.crash {
		s.crash = false
		return RunGoal{}, errors.New("the process ended")
	}
	return s.durableGoalStore.RecordGoalDeliveries(ctx, input)
}

type fenceFlow struct {
	t        *testing.T
	ctx      context.Context
	clock    *time.Time
	store    *fenceStore
	host     *ReferenceEffectHost
	a, other LeaseFence
	n        int
}

func newFenceFlow(t *testing.T) *fenceFlow {
	t.Helper()
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	f := &fenceFlow{t: t, ctx: context.Background(), clock: &at}
	f.store = &fenceStore{durableGoalStore: durableGoalStore{NewMemoryRunStore(MemoryRunStoreOptions{Now: func() time.Time { return *f.clock }})}}
	f.host = NewReferenceEffectHost(f.store)
	open := func(run, owner string) LeaseFence {
		head, err := f.store.Commit(f.ctx, RunCommit{RunID: run, CommitID: run + "-open", Checkpoint: []byte(`{"t":"` + run + `"}`), Events: [][]byte{}})
		if err != nil {
			t.Fatal(err)
		}
		lease, err := f.store.AcquireLease(f.ctx, LeaseAcquire{Resource: run, Owner: owner, ExpectedRevision: head.Revision, DurationMs: fenceHour})
		if err != nil {
			t.Fatal(err)
		}
		return FenceOf(lease)
	}
	f.a, f.other = open(fenceRun, "worker-a"), open(fenceOther, "worker-o")
	return f
}
func (f *fenceFlow) view(fence LeaseFence) RunStore {
	f.t.Helper()
	view, err := UnattendedRunStore(f.store, fence, f.host)
	if err != nil {
		f.t.Fatal(err)
	}
	return view
}
func (f *fenceFlow) command(kind string, revision int64, edit func(*GoalCommand)) GoalCommand {
	f.n++
	c := GoalCommand{Version: 1, Resource: fenceRun, Command: fmt.Sprintf("cmd-%d", f.n), Actor: "person-1", ExpectedRevision: goalKitRevision(revision), Kind: kind}
	if edit != nil {
		edit(&c)
	}
	return c
}
func (f *fenceFlow) create(store RunStore, objective string, revision int64) GoalAnswer {
	f.t.Helper()
	answer, err := RunGoalCommand(f.ctx, store, f.command("create", revision, func(c *GoalCommand) { c.Objective = goalKitString(objective) }))
	if err != nil {
		f.t.Fatal(err)
	}
	return answer
}

// started is one job of the run, started under fence: a running job an abandon must ask to stop.
func (f *fenceFlow) started(fence LeaseFence) string {
	f.t.Helper()
	head, _ := f.store.Load(f.ctx, fenceRun)
	digest, _ := JobKeyDigest(fenceKey)
	job := JobIdentity(fenceRun, "op-1")
	if _, err := f.store.CommitFenced(f.ctx, RunCommit{RunID: fenceRun, ExpectedRevision: head.Revision, CommitID: "run-f-op-1", Checkpoint: []byte(`{"t":"op-1"}`), Events: [][]byte{}, Job: &JobRegistration{Job: job, Operation: "op-1", Source: "test-source", Tool: "test-start", RequestDigest: "sha256:" + strings.Repeat("a", 64), KeyDigest: digest}}, fence); err != nil {
		f.t.Fatal(err)
	}
	handle := "test-handle"
	if _, err := f.store.AnswerJob(f.ctx, JobAnswer{Version: 1, Resource: fenceRun, Job: job, Generation: 1, Answer: "started", Handle: &handle}, fence); err != nil {
		f.t.Fatal(err)
	}
	return job
}

// takenOver: the worker's lease lapses and worker-b takes the run; worker-a's view is the displaced one.
func (f *fenceFlow) takenOver() LeaseFence {
	f.t.Helper()
	*f.clock = f.clock.Add(time.Hour + time.Millisecond)
	head, _ := f.store.Load(f.ctx, fenceRun)
	lease, err := f.store.AcquireLease(f.ctx, LeaseAcquire{Resource: fenceRun, Owner: "worker-b", ExpectedRevision: head.Revision, DurationMs: fenceHour})
	if err != nil {
		f.t.Fatal(err)
	}
	return FenceOf(lease)
}

// snapshot is everything a refused command must leave as it was.
func (f *fenceFlow) snapshot() string {
	f.t.Helper()
	goal, err1 := f.store.ReadGoal(f.ctx, fenceRun)
	lease, err2 := f.store.ReadLease(f.ctx, fenceRun)
	jobs, err3 := f.store.ExportJobs(f.ctx, fenceRun)
	waits, err4 := f.store.ExportWaits(f.ctx, fenceRun)
	head, err5 := f.store.Load(f.ctx, fenceRun)
	if err := errors.Join(err1, err2, err3, err4, err5); err != nil {
		f.t.Fatal(err)
	}
	out, err := json.Marshal(map[string]any{"goal": goal, "lease": lease, "jobs": jobs, "waits": waits, "head": head})
	if err != nil {
		f.t.Fatal(err)
	}
	return string(out)
}

// refusedStale is the exact refusal a stale view gets: the lease refusal, its record's bytes and its failure.
func refusedStale(t *testing.T, err error, reason string) {
	t.Helper()
	var refused *LeaseRefused
	var goal *GoalRefused
	if !errors.As(err, &refused) || errors.As(err, &goal) {
		t.Fatalf("%v: want LeaseRefused %s", err, reason)
	}
	record, _ := json.Marshal(refused.Record())
	var held *failure.Error
	if string(record) != `{"version":1,"reason":"`+reason+`"}` || err.Error() != "LeaseRefused: "+reason+"." || !errors.As(err, &held) {
		t.Fatalf("%s %q", record, err.Error())
	}
	detail := "LeaseRefused: " + reason + "."
	if held.Failure.Version != 1 || held.Failure.Code != "stale-state" || held.Failure.Source != "storage" || held.Failure.Outcome != "not-started" || held.Failure.Detail == nil || *held.Failure.Detail != detail {
		t.Fatalf("%+v", held.Failure)
	}
}

// idempotentCancel is a host whose cancel is idempotent by operation, as the guide asks: what reached the job's own
// system, by operation.
type idempotentCancel struct {
	calls     []string
	delivered map[string]bool
}

func (c *idempotentCancel) cancel(_ context.Context, input JobCancelInput) error {
	c.calls = append(c.calls, input.Operation)
	if c.delivered == nil {
		c.delivered = map[string]bool{}
	}
	c.delivered[input.Operation] = true
	return nil
}

func TestGoalAStaleViewIsRefusedForCreatePauseAndAbandonAndTheHolderIsDecided(t *testing.T) {
	f := newFenceFlow(t)
	job := f.started(f.a)
	stale := f.view(f.a)
	b := f.takenOver()
	current := f.view(b)
	sent := &idempotentCancel{}
	// Create: the stale view writes no record.
	before := f.snapshot()
	_, err := RunGoalCommand(f.ctx, stale, f.command("create", 0, func(c *GoalCommand) { c.Objective = goalKitString("objective-1") }))
	refusedStale(t, err, "stale-epoch")
	if held, _ := f.store.ReadGoal(f.ctx, fenceRun); held != nil || f.snapshot() != before {
		t.Fatal("a stale create wrote", held)
	}
	created := f.create(current, "objective-1", 0)
	// Pause and abandon: refused before the record is read for a repeat, journaled nowhere; the abandon seizes and
	// sends nothing.
	pause := f.command("pause", created.Goal.Revision, nil)
	abandon := f.command("abandon", created.Goal.Revision, nil)
	made := f.snapshot()
	_, err = RunGoalCommand(f.ctx, stale, pause)
	refusedStale(t, err, "stale-epoch")
	_, err = AbandonGoal(f.ctx, stale, f.host, abandon, fenceHour, sent.cancel)
	refusedStale(t, err, "stale-epoch")
	if f.snapshot() != made || len(sent.calls) != 0 {
		t.Fatal("a stale pause or abandon changed something", sent.calls)
	}
	if lease, _ := f.store.ReadLease(f.ctx, fenceRun); lease == nil || lease.Owner != "worker-b" || lease.Epoch != b.Epoch || lease.State != "held" {
		t.Fatal(lease)
	}
	// The holder's commands, the same ones, are decided: the refusals journaled nothing a repeat would answer.
	paused, err := RunGoalCommand(f.ctx, current, pause)
	if err != nil || paused.Entry.Command != pause.Command || paused.Entry.Outcome != "accepted" {
		t.Fatal(err, paused.Entry)
	}
	// The host's own commands, through the store, carry no fence and stay the host's.
	resumed, err := RunGoalCommand(f.ctx, f.store, f.command("resume", paused.Goal.Revision, nil))
	if err != nil {
		t.Fatal(err)
	}
	abandon.ExpectedRevision = goalKitRevision(resumed.Goal.Revision)
	gone, err := AbandonGoal(f.ctx, current, f.host, abandon, fenceHour, sent.cancel)
	if err != nil || gone.Goal.Objectives[len(gone.Goal.Objectives)-1].State != "abandoned" || gone.Fence == nil || gone.Fence.Owner != JobAbandonOwner {
		t.Fatal(err, gone.Fence)
	}
	if len(gone.Jobs) != 1 || gone.Jobs[0] != (JobAbandonDelivery{Job: job, Delivery: "sent"}) || len(sent.calls) != 1 {
		t.Fatal(gone.Jobs, sent.calls)
	}
	// The abandon took the lease: the view it came through is stale now, and its retry goes through the store.
	_, err = AbandonGoal(f.ctx, current, f.host, abandon, fenceHour, sent.cancel)
	refusedStale(t, err, "stale-epoch")
	retry, err := AbandonGoal(f.ctx, f.store, f.host, abandon, fenceHour, sent.cancel)
	if err != nil || !goalKitSame(retry.Entry, gone.Entry) || len(sent.calls) != 1 || len(retry.Jobs) != 1 || retry.Jobs[0].Delivery != "already-admitted" {
		t.Fatal(err, sent.calls, retry.Jobs)
	}
}

func TestGoalAFenceOfAnotherRunIsRefusedResourceMismatch(t *testing.T) {
	f := newFenceFlow(t)
	created := f.create(f.view(f.a), "objective-1", 0)
	before := f.snapshot()
	_, err := RunGoalCommand(f.ctx, f.view(f.other), f.command("pause", created.Goal.Revision, nil))
	refusedStale(t, err, "resource-mismatch")
	if f.snapshot() != before {
		t.Fatal("a fence of another run changed the record")
	}
}

func TestGoalTheTableReadsAndChecksATransitionsFence(t *testing.T) {
	f := newFenceFlow(t)
	now := func() time.Time { return *f.clock }
	context := func(string) GoalContext { return GoalContext{Waits: []RunWait{}, Jobs: []RunJob{}} }
	create := f.command("create", 0, func(c *GoalCommand) { c.Objective = goalKitString("objective-1") })
	fence := f.a
	var capability *GoalCapabilityRefused
	if _, err := NewGoalTable(now).Command(GoalTransition{Version: 1, Command: create, Fence: &fence}, context); !errors.As(err, &capability) || capability.Reason != "store-unsupported" {
		t.Fatal(err)
	}
	for _, raw := range []string{`{"version":1,"resource":"run-f","owner":"worker-a","epoch":"0"}`, `{"version":2,"resource":"run-f","owner":"worker-a","epoch":"1"}`, `{"version":1,"resource":"run-f","owner":"worker-a","epoch":"1","extra":true}`, `"fence"`, `null`} {
		command, _ := json.Marshal(create)
		_, err := ReadGoalTransition([]byte(`{"version":1,"command":` + string(command) + `,"fence":` + raw + `}`))
		if !goalRefusedWith(err, "invalid") {
			t.Fatal(raw, err)
		}
	}
	checked := []LeaseFence{}
	table := NewFencedGoalTable(now, func(fence LeaseFence) error { checked = append(checked, fence); return nil })
	if answer, err := table.Command(GoalTransition{Version: 1, Command: create, Fence: &fence}, context); err != nil || answer.Entry.Outcome != "accepted" || len(checked) != 1 || checked[0] != fence {
		t.Fatal(err, checked)
	}
	other := f.other
	_, err := table.Command(GoalTransition{Version: 1, Command: f.command("pause", 1, nil), Fence: &other}, context)
	var lease *LeaseRefused
	if !errors.As(err, &lease) || lease.Reason != "resource-mismatch" || len(checked) != 1 {
		t.Fatal(err, checked)
	}
	if held, _ := table.Read(fenceRun); held == nil || len(held.Journal) != 1 {
		t.Fatal(held)
	}
}

func TestGoalAnAbandonRetriedFromANewProcessAfterACrashCancelsUnderTheSameOperation(t *testing.T) {
	f := newFenceFlow(t)
	job := f.started(f.a)
	host := &idempotentCancel{}
	created := f.create(f.store, "objective-1", 0)
	abandon := f.command("abandon", created.Goal.Revision, nil)
	// The process ends between the cancel's send and the journal of its delivery.
	f.store.crash = true
	if _, err := AbandonGoal(f.ctx, f.store, f.host, abandon, fenceHour, host.cancel); err == nil || err.Error() != "the process ended" {
		t.Fatal(err)
	}
	crashed, _ := f.store.ReadLease(f.ctx, fenceRun)
	held, _ := f.store.ReadJob(f.ctx, fenceRun, job)
	if len(host.calls) != 1 || crashed == nil || crashed.Owner != JobAbandonOwner || held == nil || held.Cancel == nil {
		t.Fatal(host.calls, crashed, held)
	}
	requested := held.Cancel.Requested
	if requested.By != "command" || requested.Operation != host.calls[0] || requested.Command == nil || *requested.Command != abandon.Command || requested.Owner == nil || *requested.Owner != JobAbandonOwner || requested.Epoch == nil || *requested.Epoch != crashed.Epoch {
		t.Fatal(requested)
	}
	if goal, _ := f.store.ReadGoal(f.ctx, fenceRun); goal.Journal[len(goal.Journal)-1].Outcome == "delivered" {
		t.Fatal("the attempt that crashed journaled its deliveries")
	}
	// A new process: a new effect host, whose admissions began empty, over the same durable store.
	retry, err := AbandonGoal(f.ctx, f.store, NewReferenceEffectHost(f.store), abandon, fenceHour, host.cancel)
	if err != nil {
		t.Fatal(err)
	}
	if len(host.calls) != 2 || host.calls[1] != host.calls[0] || len(host.delivered) != 1 || retry.Fence == nil || retry.Fence.Epoch == crashed.Epoch {
		t.Fatal(host.calls, retry.Fence)
	}
	delivered := []GoalEntry{}
	for _, entry := range retry.Goal.Journal {
		if entry.Outcome == "delivered" {
			delivered = append(delivered, entry)
		}
	}
	if len(retry.Jobs) != 1 || retry.Jobs[0] != (JobAbandonDelivery{Job: job, Delivery: "sent"}) || len(delivered) != 1 || delivered[0].Command != abandon.Command || len(delivered[0].Deliveries) != 1 || delivered[0].Deliveries[0] != (GoalDelivery{Job: job, Delivery: "sent"}) {
		t.Fatal(retry.Jobs, delivered)
	}
	// The job's record holds the request of the attempt that crashed, under its own epoch: the first request stands.
	if now, _ := f.store.ReadJob(f.ctx, fenceRun, job); now == nil || now.Cancel == nil || !goalKitSame(now.Cancel.Requested, requested) {
		t.Fatal(now)
	}
}

func TestGoalAnAbandonRetriedWithAHostWhoseAdmissionsOutliveItsProcessSendsOnce(t *testing.T) {
	f := newFenceFlow(t)
	job := f.started(f.a)
	host := &idempotentCancel{}
	created := f.create(f.store, "objective-1", 0)
	abandon := f.command("abandon", created.Goal.Revision, nil)
	f.store.crash = true
	if _, err := AbandonGoal(f.ctx, f.store, f.host, abandon, fenceHour, host.cancel); err == nil {
		t.Fatal("no crash")
	}
	retry, err := AbandonGoal(f.ctx, f.store, f.host, abandon, fenceHour, host.cancel)
	if err != nil || len(host.calls) != 1 || len(retry.Jobs) != 1 || retry.Jobs[0] != (JobAbandonDelivery{Job: job, Delivery: "already-admitted"}) {
		t.Fatal(err, host.calls, retry.Jobs)
	}
}

func TestGoalAnAbandonOfATerminalObjectiveSendsNothingAndAbandonJobsStopsItsJobs(t *testing.T) {
	f := newFenceFlow(t)
	job := f.started(f.a)
	host := &idempotentCancel{}
	created := f.create(f.store, "objective-1", 0)
	head, _ := f.store.Load(f.ctx, fenceRun)
	at := GoalHead{Revision: head.Revision, Digest: CheckpointBytesDigest(head.Checkpoint)}
	plan := GoalPlanSteps{ID: "plan-1", Revision: "sha256:" + strings.Repeat("c", 64), Steps: []GoalStep{{Step: "step-1", Index: 0}}}
	bound, err := f.store.CommandGoal(f.ctx, GoalTransition{Version: 1, Command: f.command("bind", created.Goal.Revision, func(c *GoalCommand) { c.Steps = []string{"step-1"}; c.Head = &at }), Plan: &plan})
	if err != nil {
		t.Fatal(err)
	}
	read := bound.Goal.Revision
	achieved, err := f.store.CommandGoal(f.ctx, GoalTransition{Version: 1, Command: f.command("achieve", bound.Goal.Revision, func(c *GoalCommand) { c.Head = &at; c.Waive = []string{job} }), Read: &read})
	if err != nil || achieved.Goal.Objectives[len(achieved.Goal.Objectives)-1].State != "achieved" {
		t.Fatal(err)
	}
	lease, _ := f.store.ReadLease(f.ctx, fenceRun)
	_, err = AbandonGoal(f.ctx, f.store, f.host, f.command("abandon", achieved.Goal.Revision, nil), fenceHour, host.cancel)
	if !goalRefusedWith(err, "objective-terminal") || len(host.calls) != 0 {
		t.Fatal(err, host.calls)
	}
	if now, _ := f.store.ReadLease(f.ctx, fenceRun); !goalKitSame(now, lease) {
		t.Fatal(now, lease)
	}
	stopped, err := AbandonJobs(f.ctx, f.store, f.host, fenceRun, "host-stops-the-jobs", fenceHour, host.cancel)
	if err != nil || len(stopped.Jobs) != 1 || stopped.Jobs[0] != (JobAbandonDelivery{Job: job, Delivery: "sent"}) || len(host.calls) != 1 {
		t.Fatal(err, stopped.Jobs, host.calls)
	}
}

func TestGoalTheFirstAbandonsDeliveryDoesNotLetACreateOverTheSecondThrough(t *testing.T) {
	f := newFenceFlow(t)
	created := f.create(f.store, "objective-1", 0)
	first, err := AbandonGoal(f.ctx, f.store, f.host, f.command("abandon", created.Goal.Revision, nil), fenceHour, func(context.Context, JobCancelInput) error { return nil })
	if err != nil || first.Goal.Journal[len(first.Goal.Journal)-1].Outcome != "delivered" {
		t.Fatal(err)
	}
	second := f.create(f.store, "objective-2", first.Goal.Revision)
	// The second abandon's goal transaction is journaled, and the process ends before its deliveries are.
	gone, err := f.store.CommandGoal(f.ctx, GoalTransition{Version: 1, Command: f.command("abandon", second.Goal.Revision, nil)})
	if err != nil || gone.Goal.Objectives[len(gone.Goal.Objectives)-1].State != "abandoned" {
		t.Fatal(err)
	}
	next := f.command("create", gone.Goal.Revision, func(c *GoalCommand) { c.Objective = goalKitString("objective-3") })
	if _, err := RunGoalCommand(f.ctx, f.store, next); !goalRefusedWith(err, "abandon-unfinished") {
		t.Fatal(err)
	}
	if goal, _ := f.store.ReadGoal(f.ctx, fenceRun); goal.Journal[len(goal.Journal)-1].Command != next.Command || goal.Journal[len(goal.Journal)-1].Outcome != "abandon-unfinished" {
		t.Fatal(goal.Journal[len(goal.Journal)-1])
	}
}
