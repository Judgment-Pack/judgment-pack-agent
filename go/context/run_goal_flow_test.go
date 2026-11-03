package runcontext

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

// #230: one durable goal through a real run of the package's writer over the memory kind held as a durable one. Every
// command is the host's: no model is called. The TypeScript twin is packages/harness/storage/goal.test.ts.
const goalRun = "run-g"

type goalFlow struct {
	t     *testing.T
	clock *time.Time
	store durableGoalStore
	host  *ReferenceEffectHost
	fence LeaseFence
	view  RunStore
	run   *Run
	n     int
}

func goalAcceptedPlan(steps ...string) *Value {
	items := []*Value{}
	for _, step := range steps {
		items = append(items, String(step))
	}
	return ObjectOf("id", String("accepted-plan"), "revision", Number(1), "objective", String("Inspect and report"), "steps", Array(items...), "questions", Array(), "references", Array(), "status", String("accepted"), "createdAt", String("2026-01-01T00:00:00.000Z"), "acceptedAt", String("2026-01-01T00:00:00.000Z"))
}

// newGoalFlow is a leased run over an accepted Plan, with a read tool to cite.
func newGoalFlow(t *testing.T) *goalFlow {
	t.Helper()
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	f := &goalFlow{t: t, clock: &at}
	f.store = durableGoalStore{NewMemoryRunStore(MemoryRunStoreOptions{Now: func() time.Time { return *f.clock }})}
	f.host = NewReferenceEffectHost(f.store)
	lease, err := f.store.AcquireLease(context.Background(), LeaseAcquire{Resource: goalRun, Owner: "worker-a", DurationMs: 3600000})
	if err != nil {
		t.Fatal(err)
	}
	f.fence = FenceOf(lease)
	if f.view, err = UnattendedRunStore(f.store, f.fence, f.host); err != nil {
		t.Fatal(err)
	}
	f.open()
	return f
}
func (f *goalFlow) open() {
	session, _ := trackedSession(f.t)
	session.Context.RunStore = &DurableRunInput{Store: f.view, ID: goalRun, Resume: f.run != nil}
	session.Context.Plan = goalAcceptedPlan("Inspect", "Report")
	session.Context.Phase = "work"
	session.HostTools = []HostTool{{Name: "lookup", Effect: "read", Taxonomy: &ToolTaxonomy{Operation: "read", Effect: "read"}, Description: "Read supplied evidence.", InputSchema: json.RawMessage(`{"type":"object"}`), Execute: func(context.Context, json.RawMessage) (ToolResult, error) {
		return ResultOf([]Content{TextContent("Evidence.")}, nil), nil
	}}}
	if f.run != nil {
		f.run.Context.Close()
	}
	f.run = openRun(f.t, session)
}
func (f *goalFlow) tool(name, args string) {
	f.t.Helper()
	if _, err := hostTool(f.t, f.run, name).Execute(context.Background(), json.RawMessage(args)); err != nil {
		f.t.Fatal(name, err)
	}
}
func (f *goalFlow) saved() *Value {
	head, err := f.store.Load(context.Background(), goalRun)
	if err != nil || head == nil {
		f.t.Fatal(err)
	}
	return MustParseJSON(string(head.Checkpoint))
}
func (f *goalFlow) head() GoalHead {
	head, _ := f.store.Load(context.Background(), goalRun)
	return GoalHead{Revision: head.Revision, Digest: CheckpointBytesDigest(head.Checkpoint)}
}
func (f *goalFlow) lookups() []string {
	out := []string{}
	for _, row := range f.saved().Get("durable").Get("dispatches").Items() {
		if planText(row.Get("tool")) == "lookup" {
			out = append(out, planText(row.Get("id")))
		}
	}
	return out
}
func (f *goalFlow) command(kind string, revision int64, edit func(*GoalCommand)) GoalCommand {
	f.n++
	c := GoalCommand{Version: 1, Resource: goalRun, Command: fmt.Sprintf("cmd-%d", f.n), Actor: "person-1", ExpectedRevision: goalKitRevision(revision), Kind: kind}
	if edit != nil {
		edit(&c)
	}
	return c
}
func (f *goalFlow) send(c GoalCommand) (GoalAnswer, error) {
	return RunGoalCommand(context.Background(), f.store, c)
}
func (f *goalFlow) must(c GoalCommand) GoalAnswer {
	f.t.Helper()
	answer, err := f.send(c)
	if err != nil {
		f.t.Fatal(c.Kind, err)
	}
	return answer
}
func (f *goalFlow) refused(c GoalCommand, reason string) {
	f.t.Helper()
	if _, err := f.send(c); !goalRefusedWith(err, reason) {
		f.t.Fatalf("%s: %v, want %s", c.Kind, err, reason)
	}
}
func (f *goalFlow) last() GoalEntry {
	held, err := ReadGoal(context.Background(), f.store, goalRun)
	if err != nil || held == nil {
		f.t.Fatal(err)
	}
	return held.Journal[len(held.Journal)-1]
}
func (f *goalFlow) revisionOf() string { return planText(f.saved().Get("planState").Get("revision")) }
func (f *goalFlow) close(step, operation string) {
	f.tool("complete_plan_step", `{"id":"`+step+`","expectedRevision":"`+f.revisionOf()+`","outcome":"completed","evidence":{"operations":["`+operation+`"],"artifacts":[]}}`)
}

// bound: the model began the first step and read twice; the objective is created and both steps bound.
func goalBound(t *testing.T) (*goalFlow, GoalAnswer) {
	f := newGoalFlow(t)
	f.tool("update_plan", `{"explanation":"work","plan":[{"id":"step-1","step":"Inspect","status":"in_progress"},{"id":"step-2","step":"Report","status":"pending"}]}`)
	f.tool("lookup", `{"n":1}`)
	f.tool("lookup", `{"n":2}`)
	created := f.must(f.command("create", 0, func(c *GoalCommand) { c.Objective = goalKitString("objective-1") }))
	head := f.head()
	bound := f.must(f.command("bind", created.Goal.Revision, func(c *GoalCommand) { c.Steps = []string{"step-2", "step-1"}; c.Head = &head }))
	return f, bound
}

// finish: the model closes both required steps with its reads; moves is its plan between them.
func (f *goalFlow) finish(moves string) {
	reads := f.lookups()
	f.close("step-1", reads[0])
	f.tool("update_plan", `{"explanation":"next","plan":`+moves+`}`)
	step := ""
	for _, row := range f.saved().Get("plan").Items() {
		if row.Get("origin") != nil && row.Get("origin").Get("index").Num() == 1 {
			step = planText(row.Get("id"))
		}
	}
	f.close(step, reads[1])
}

func TestGoalAchievedByTheStepsOwnEvidenceAndAHelperStepIsNoDeparture(t *testing.T) {
	f, bound := goalBound(t)
	defer f.run.Context.Close()
	if plan := bound.Goal.Objectives[0].Plan; plan == nil || plan.ID != "accepted-plan" || len(plan.Steps) != 2 || plan.Steps[0] != (GoalStep{Step: "step-1", Index: 0}) || plan.Steps[1] != (GoalStep{Step: "step-2", Index: 1}) {
		t.Fatal(plan)
	}
	// The model inserts a helper step before the second required one: added and reordered, and no departure here.
	f.finish(`[{"id":"step-1","step":"Inspect","status":"completed"},{"step":"Check the inputs","status":"pending"},{"id":"step-2","step":"Report","status":"in_progress"}]`)
	head := f.head()
	done := f.must(f.command("achieve", bound.Goal.Revision, func(c *GoalCommand) { c.Head = &head }))
	// No command made a request or wrote the run: the head is the one the achievement names, and no model row is in it.
	if f.head() != head {
		t.Fatal("a command wrote the run")
	}
	for _, row := range f.saved().Get("durable").Get("dispatches").Items() {
		if kind := planText(row.Get("kind")); kind == "model" || kind == "agent" {
			t.Fatal("a model was called")
		}
	}
	if done.Goal.Objectives[0].State != "achieved" || done.Goal.Objectives[0].Achieved.Head != head || done.Goal.Objectives[0].Achieved.Unsuccessful != nil {
		t.Fatal(done.Goal.Objectives[0])
	}
	report, err := GoalReportFor(context.Background(), f.store, goalRun)
	if err != nil || report == nil || len(report.Entries) != len(done.Goal.Journal) {
		t.Fatal(report, err)
	}
	// A terminal objective has no disposition, and a later one cannot reuse the steps' proof (M11).
	f.refused(f.command("pause", done.Goal.Revision, nil), "objective-terminal")
	next := f.must(f.command("create", done.Goal.Revision, func(c *GoalCommand) { c.Objective = goalKitString("objective-2") }))
	f.refused(f.command("bind", next.Goal.Revision, func(c *GoalCommand) { c.Steps = []string{"step-1"}; c.Head = &head }), "step-settled")
	if last := f.last(); last.Outcome != "step-settled" || last.Step == nil || *last.Step != "step-1" || last.Objective != "objective-2" {
		t.Fatal(last)
	}
}

func TestGoalAChangedOrMovedRequiredStepOrAPendingDecisionStrandsTheAchievement(t *testing.T) {
	for _, row := range []struct{ moves, reason string }{
		{`[{"id":"step-1","step":"Inspect","status":"in_progress"},{"id":"step-2","step":"Report twice","status":"pending"}]`, "lineage-changed"},
		{`[{"id":"step-2","step":"Report","status":"pending"},{"id":"step-1","step":"Inspect","status":"in_progress"}]`, "lineage-changed"},
		{`[{"id":"step-1","step":"Inspect","status":"in_progress"}]`, "decision-pending"},
	} {
		f, bound := goalBound(t)
		f.tool("update_plan", `{"explanation":"Change the plan.","plan":`+row.moves+`}`)
		head := f.head()
		f.refused(f.command("achieve", bound.Goal.Revision, func(c *GoalCommand) { c.Head = &head }), row.reason)
		if last := f.last(); last.Kind != "achieve" || last.Outcome != row.reason {
			t.Fatal(last)
		}
		f.run.Context.Close()
	}
}

func TestGoalRefusesAnIncompleteStepAMovedHeadAndABookThatNoLongerProvesAStep(t *testing.T) {
	f, bound := goalBound(t)
	defer f.run.Context.Close()
	head := f.head()
	f.refused(f.command("achieve", bound.Goal.Revision, func(c *GoalCommand) { c.Head = &head }), "step-incomplete")
	if last := f.last(); last.Step == nil || *last.Step != "step-1" {
		t.Fatal(last)
	}
	other := GoalHead{Revision: "another", Digest: head.Digest}
	f.refused(f.command("achieve", bound.Goal.Revision, func(c *GoalCommand) { c.Head = &other }), "head-changed")
	f.finish(`[{"id":"step-1","step":"Inspect","status":"completed"},{"id":"step-2","step":"Report","status":"in_progress"}]`)
	// The evidence is read again from the head's book: a book whose read failed since proves nothing.
	state := f.saved()
	first := f.lookups()[0]
	observations := Array()
	for _, row := range state.Get("durable").Get("observations").Items() {
		if planText(row.Get("dispatchId")) != first {
			observations.a = append(observations.a, row)
		}
	}
	state.Get("durable").Set("observations", observations)
	held, _ := f.store.Load(context.Background(), goalRun)
	if _, err := f.store.CommitFenced(context.Background(), RunCommit{RunID: goalRun, ExpectedRevision: held.Revision, CommitID: "tampered", Checkpoint: []byte(CanonicalJSON(state)), Events: [][]byte{}}, f.fence); err != nil {
		t.Fatal(err)
	}
	tampered := f.head()
	f.refused(f.command("achieve", bound.Goal.Revision, func(c *GoalCommand) { c.Head = &tampered }), "evidence-refused")
	if last := f.last(); last.Step == nil || *last.Step != "step-1" {
		t.Fatal(last)
	}
}

func TestGoalCompetingAndRepeatedCommands(t *testing.T) {
	f, bound := goalBound(t)
	defer f.run.Context.Close()
	revision := bound.Goal.Revision
	a := f.command("pause", revision, nil)
	b := f.command("block", revision, func(c *GoalCommand) { c.Condition = goalKitString("waiting-on-data") })
	type result struct {
		answer GoalAnswer
		err    error
	}
	results := make(chan result, 2)
	for _, c := range []GoalCommand{a, b} {
		go func(c GoalCommand) { answer, err := f.send(c); results <- result{answer, err} }(c)
	}
	won, lost := 0, 0
	var winner GoalAnswer
	for range 2 {
		r := <-results
		switch {
		case r.err == nil:
			won++
			winner = r.answer
		case goalRefusedWith(r.err, "revision-changed"):
			lost++
		default:
			t.Fatal(r.err)
		}
	}
	if won != 1 || lost != 1 {
		t.Fatal(won, lost)
	}
	record, _ := ReadGoal(context.Background(), f.store, goalRun)
	if record.Revision != revision+1 {
		t.Fatal(record.Revision)
	}
	repeat := a
	if winner.Entry.Kind == "block" {
		repeat = b
	}
	again := f.must(repeat)
	after, _ := ReadGoal(context.Background(), f.store, goalRun)
	if !goalKitSame(again.Entry, winner.Entry) || len(after.Journal) != len(record.Journal) {
		t.Fatal("a repeat was decided again")
	}
	changed := repeat
	changed.Actor = "person-2"
	f.refused(changed, "conflict")
}

func TestGoalPauseRefusesOnlyAnAcquisitionAndAResumeWritesNothingElse(t *testing.T) {
	f, bound := goalBound(t)
	defer f.run.Context.Close()
	paused := f.must(f.command("pause", bound.Goal.Revision, nil))
	if paused.Lease == nil || paused.Lease.Owner != "worker-a" || paused.Lease.Epoch != f.fence.Epoch || paused.Lease.State != "held" {
		t.Fatal(paused.Lease)
	}
	// The held lease runs on: the run's own worker still commits.
	f.tool("lookup", `{"n":3}`)
	if _, err := f.store.ReleaseLease(context.Background(), f.fence); err != nil {
		t.Fatal(err)
	}
	head, _ := f.store.Load(context.Background(), goalRun)
	if _, err := f.store.AcquireLease(context.Background(), LeaseAcquire{Resource: goalRun, Owner: "worker-b", ExpectedRevision: head.Revision, DurationMs: 3600000}); !goalRefusedWith(err, "objective-paused") {
		t.Fatal(err)
	}
	lease, _ := f.store.ReadLease(context.Background(), goalRun)
	waits, _ := f.store.ExportWaits(context.Background(), goalRun)
	jobs, _ := f.store.ExportJobs(context.Background(), goalRun)
	resumed := f.must(f.command("resume", paused.Goal.Revision, nil))
	headAfter, _ := f.store.Load(context.Background(), goalRun)
	leaseAfter, _ := f.store.ReadLease(context.Background(), goalRun)
	waitsAfter, _ := f.store.ExportWaits(context.Background(), goalRun)
	jobsAfter, _ := f.store.ExportJobs(context.Background(), goalRun)
	if resumed.Goal.Objectives[0].Paused != nil || headAfter.Revision != head.Revision || !goalKitSame(leaseAfter, lease) || !goalKitSame(waitsAfter, waits) || !goalKitSame(jobsAfter, jobs) {
		t.Fatal("a resume wrote more than its disposition")
	}
	if _, err := f.store.AcquireLease(context.Background(), LeaseAcquire{Resource: goalRun, Owner: "worker-b", ExpectedRevision: head.Revision, DurationMs: 3600000}); err != nil {
		t.Fatal(err)
	}
	// L2: an achievement under a pause is independent of it.
	again := f.must(f.command("pause", resumed.Goal.Revision, nil))
	now := f.head()
	f.refused(f.command("achieve", again.Goal.Revision, func(c *GoalCommand) { c.Head = &now }), "step-incomplete")
}

func TestGoalAnObjectivesRunIsLeased(t *testing.T) {
	f, _ := goalBound(t)
	defer f.run.Context.Close()
	head, _ := f.store.Load(context.Background(), goalRun)
	if _, err := f.store.Commit(context.Background(), RunCommit{RunID: goalRun, ExpectedRevision: head.Revision, CommitID: "unfenced", Checkpoint: head.Checkpoint, Events: [][]byte{}}); !goalRefusedWith(err, "lease-required") {
		t.Fatal(err)
	}
}

func TestGoalInspectionsAreCountedAndTheJournalNeverRefusesForSize(t *testing.T) {
	f, bound := goalBound(t)
	defer f.run.Context.Close()
	revision := bound.Goal.Revision
	for _, id := range []string{"look-1", "look-1", "look-2"} {
		if _, err := InspectGoal(context.Background(), f.store, goalRun, id, "dashboard"); err != nil {
			t.Fatal(err)
		}
	}
	looked, _ := ReadGoal(context.Background(), f.store, goalRun)
	if looked.Inspections == nil || looked.Inspections.Count != 2 || looked.Inspections.Last.Command != "look-2" || looked.Revision != revision {
		t.Fatal(looked.Inspections)
	}
	for range GoalJournalLimit + 3 {
		f.refused(f.command("resume", revision, nil), "not-paused")
	}
	full, _ := ReadGoal(context.Background(), f.store, goalRun)
	if len(full.Journal) != GoalJournalLimit || full.Journal[0].Kind != "create" || full.Journal[1].Kind != "bind" {
		t.Fatal(len(full.Journal))
	}
	abandoned, err := AbandonGoal(context.Background(), f.store, f.host, f.command("abandon", revision, nil), 3600000, func(context.Context, JobCancelInput) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	journal := abandoned.Goal.Journal
	if abandoned.Goal.Objectives[0].State != "abandoned" || len(journal) != GoalJournalLimit || journal[len(journal)-2].Outcome != "accepted" || journal[len(journal)-1].Outcome != "delivered" || abandoned.Goal.Discarded == nil || *abandoned.Goal.Discarded != 7 {
		t.Fatal(abandoned.Goal.Discarded)
	}
}

func TestGoalSurvivesARestartWhole(t *testing.T) {
	f, bound := goalBound(t)
	defer f.run.Context.Close()
	f.must(f.command("pause", bound.Goal.Revision, nil))
	before, _ := ReadGoal(context.Background(), f.store, goalRun)
	report, _ := GoalReportFor(context.Background(), f.store, goalRun)
	restarted := durableGoalStore{goalRestarted(f.store.MemoryRunStore, func() time.Time { return *f.clock })}
	after, _ := ReadGoal(context.Background(), restarted, goalRun)
	reportAfter, _ := GoalReportFor(context.Background(), restarted, goalRun)
	if !goalKitSame(after, before) || !goalKitSame(reportAfter, report) {
		t.Fatal("a restart lost the record")
	}
	if _, err := restarted.ReleaseLease(context.Background(), f.fence); err != nil {
		t.Fatal(err)
	}
	head, _ := restarted.Load(context.Background(), goalRun)
	if _, err := restarted.AcquireLease(context.Background(), LeaseAcquire{Resource: goalRun, Owner: "worker-b", ExpectedRevision: head.Revision, DurationMs: 3600000}); !goalRefusedWith(err, "objective-paused") {
		t.Fatal(err)
	}
}

func TestGoalRefusesAStoreThatCannotHoldAnObjective(t *testing.T) {
	var refused *GoalCapabilityRefused
	if err := AdmitGoals(NewMemoryRunStore(MemoryRunStoreOptions{})); err == nil || !strings.Contains(err.Error(), "store-lifetime") {
		t.Fatal(err)
	}
	callback := NewCheckpointCallbackRunStore(func(context.Context, *Value) error { return nil }, func(raw []byte) (*Value, error) { return ParseJSON(raw) })
	if err := AdmitGoals(callback); err == nil || !strings.Contains(err.Error(), "store-unsupported") {
		t.Fatal(err)
	}
	_ = refused
	f := newGoalFlow(t)
	defer f.run.Context.Close()
	if err := AdmitGoals(f.view); err != nil {
		t.Fatal(err)
	}
}

// goalStartedJob registers a started job with a claim of its own, as a start's dispatch would, and answers it.
func (f *goalFlow) goalStartedJob(operation, key string) string {
	f.t.Helper()
	head, _ := f.store.Load(context.Background(), goalRun)
	digest, _ := JobKeyDigest(key)
	if _, err := f.store.CommitFenced(context.Background(), RunCommit{RunID: goalRun, ExpectedRevision: head.Revision, CommitID: "claim-" + operation, Checkpoint: head.Checkpoint, Events: [][]byte{}, Job: &JobRegistration{Job: JobIdentity(goalRun, operation), Operation: operation, Source: "runner", Tool: "start_job", RequestDigest: "sha256:" + strings.Repeat("a", 64), KeyDigest: digest}}, f.fence); err != nil {
		f.t.Fatal(err)
	}
	job := JobIdentity(goalRun, operation)
	handle := "runner-handle"
	if _, err := f.store.AnswerJob(context.Background(), JobAnswer{Version: 1, Resource: goalRun, Job: job, Generation: 1, Answer: "started", Handle: &handle}, f.fence); err != nil {
		f.t.Fatal(err)
	}
	return job
}

func TestGoalAbandonAsksACancelOfEveryStartedJobAndJournalsItsDeliveries(t *testing.T) {
	f, bound := goalBound(t)
	defer f.run.Context.Close()
	job := f.goalStartedJob("op-1", "goal.job.0001.Q7z2Xm9LpV4cR8tNb3Yh6Df1Gj5K")
	sent := []string{}
	cancel := func(_ context.Context, input JobCancelInput) error { sent = append(sent, input.Job); return nil }
	command := f.command("abandon", bound.Goal.Revision, nil)
	first, err := AbandonGoal(context.Background(), f.store, f.host, command, 3600000, cancel)
	if err != nil {
		t.Fatal(err)
	}
	left := first.Goal.Objectives[0].Abandoned
	if first.Goal.Objectives[0].State != "abandoned" || left == nil || len(left.Waits) != 0 || len(left.Jobs) != 1 || left.Jobs[0] != (GoalJob{Job: job, State: "started"}) {
		t.Fatal(first.Goal.Objectives[0])
	}
	last := first.Goal.Journal[len(first.Goal.Journal)-1]
	if len(sent) != 1 || sent[0] != job || first.Fence == nil || first.Fence.Owner != JobAbandonOwner || last.Outcome != "delivered" || len(last.Deliveries) != 1 || last.Deliveries[0] != (GoalDelivery{Job: job, Delivery: "sent"}) {
		t.Fatal(sent, first.Fence, last)
	}
	// A retry is answered from the record and sends nothing the effect host has admitted.
	retry, err := AbandonGoal(context.Background(), f.store, f.host, command, 3600000, cancel)
	if err != nil {
		t.Fatal(err)
	}
	again := retry.Goal.Journal[len(retry.Goal.Journal)-1]
	if len(sent) != 1 || !goalKitSame(retry.Entry, first.Entry) || len(again.Deliveries) != 1 || again.Deliveries[0].Delivery != "already-admitted" {
		t.Fatal(sent, again)
	}
}

func TestGoalAWaitParkedBeforeTheAbandonHoldsTheNextCreate(t *testing.T) {
	f, bound := goalBound(t)
	defer f.run.Context.Close()
	job := f.goalStartedJob("op-1", "goal.job.0002.Hs8Pq3Lz6Wd1Rm9Tc4Vb7Nx2Ka5U")
	head, _ := f.store.Load(context.Background(), goalRun)
	digest, _ := WakeKeyDigest("wk.goal.0001.Hs8Pq3Lz6Wd1Rm9Tc4Vb7Nx2Ka5U")
	park := RunWait{Version: 1, Resource: goalRun, Wait: "wait-g", Reason: "job", Pending: contract.RunWaitPending{Job: &contract.RunWaitPendingJob{Kind: "job", Job: job, Generation: 1}}, Condition: WaitCondition{Event: &contract.RunWaitEventCondition{Kind: JobWaitEvent, Subject: job}}, Continuation: "k", Checkpoint: contract.RunWaitCheckpointBinding{Revision: head.Revision, Digest: CheckpointBytesDigest(head.Checkpoint)}, Pins: jobKitPins, WakeKeyDigest: digest, Release: []WaitRelease{}}
	if _, _, err := f.store.ParkWait(context.Background(), park, f.fence); err != nil {
		t.Fatal(err)
	}
	gone, err := AbandonGoal(context.Background(), f.store, f.host, f.command("abandon", bound.Goal.Revision, nil), 3600000, func(context.Context, JobCancelInput) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if waits := gone.Goal.Objectives[0].Abandoned.Waits; len(waits) != 1 || waits[0] != (GoalLeftWait{Wait: "wait-g", Reason: "job", State: "parked"}) {
		t.Fatal(waits)
	}
	f.refused(f.command("create", gone.Goal.Revision, func(c *GoalCommand) { c.Objective = goalKitString("objective-k") }), "wait-unresolved")
	if last := f.last(); last.Kind != "create" || last.Outcome != "wait-unresolved" || last.Objective != "objective-k" {
		t.Fatal(last)
	}
}

func TestGoalARequiredStepRemovedWhereRemovalsGoThroughStrandsTheAchievement(t *testing.T) {
	f := newGoalFlowWith(t, goalAcceptedPlan("Inspect", "Report"), []string{"added"})
	defer f.run.Context.Close()
	f.tool("update_plan", `{"explanation":"work","plan":[{"id":"step-1","step":"Inspect","status":"pending"},{"id":"step-2","step":"Report","status":"pending"}]}`)
	created := f.must(f.command("create", 0, func(c *GoalCommand) { c.Objective = goalKitString("objective-r") }))
	head := f.head()
	bound := f.must(f.command("bind", created.Goal.Revision, func(c *GoalCommand) { c.Steps = []string{"step-1", "step-2"}; c.Head = &head }))
	// The first required step goes: no other check stands in for this one.
	f.tool("update_plan", `{"explanation":"Drop the inspection.","plan":[{"id":"step-2","step":"Report","status":"in_progress"}]}`)
	if f.saved().Get("plan").Len() != 1 {
		t.Fatal("the removal paused")
	}
	now := f.head()
	f.refused(f.command("achieve", bound.Goal.Revision, func(c *GoalCommand) { c.Head = &now }), "lineage-changed")
}

func TestGoalBindsOnlyStepsOfTheAcceptedPlanAndAchievesOnlyThePlanItBound(t *testing.T) {
	f := newGoalFlow(t)
	defer f.run.Context.Close()
	f.tool("update_plan", `{"explanation":"work","plan":[{"id":"step-1","step":"Inspect","status":"in_progress"},{"step":"Check the inputs","status":"pending"},{"id":"step-2","step":"Report","status":"pending"}]}`)
	helper := ""
	for _, row := range f.saved().Get("plan").Items() {
		if row.Get("origin") == nil {
			helper = planText(row.Get("id"))
		}
	}
	created := f.must(f.command("create", 0, func(c *GoalCommand) { c.Objective = goalKitString("objective-p") }))
	head := f.head()
	f.refused(f.command("bind", created.Goal.Revision, func(c *GoalCommand) { c.Steps = []string{"step-1", helper}; c.Head = &head }), "plan-invalid")
	bound := f.must(f.command("bind", created.Goal.Revision, func(c *GoalCommand) { c.Steps = []string{"step-1"}; c.Head = &head }))
	// Another accepted Plan's head under this run: the objective bound this Plan, not that one.
	plan := goalAcceptedPlan("Inspect", "Report")
	plan.Set("id", String("other-plan"))
	fresh := newGoalFlowWith(t, plan, nil)
	defer fresh.run.Context.Close()
	fresh.tool("update_plan", `{"explanation":"work","plan":[{"id":"step-1","step":"Inspect","status":"in_progress"},{"id":"step-2","step":"Report","status":"pending"}]}`)
	foreign, _ := fresh.store.Load(context.Background(), goalRun)
	held, _ := f.store.Load(context.Background(), goalRun)
	if _, err := f.store.CommitFenced(context.Background(), RunCommit{RunID: goalRun, ExpectedRevision: held.Revision, CommitID: "other-plan-head", Checkpoint: foreign.Checkpoint, Events: [][]byte{}}, f.fence); err != nil {
		t.Fatal(err)
	}
	now := f.head()
	f.refused(f.command("achieve", bound.Goal.Revision, func(c *GoalCommand) { c.Head = &now }), "lineage-changed")
}

// newGoalFlowWith is newGoalFlow over another accepted Plan and departure policy, on a store of its own.
func newGoalFlowWith(t *testing.T, plan *Value, drift []string) *goalFlow {
	t.Helper()
	f := newGoalFlow(t)
	f.run.Context.Close()
	session, _ := trackedSession(t)
	session.Context.RunStore = &DurableRunInput{Store: f.view, ID: goalRun}
	session.Context.Plan = plan
	session.Context.Phase = "work"
	session.Context.PlanDrift = drift
	fresh := durableGoalStore{NewMemoryRunStore(MemoryRunStoreOptions{Now: func() time.Time { return *f.clock }})}
	lease, err := fresh.AcquireLease(context.Background(), LeaseAcquire{Resource: goalRun, Owner: "worker-a", DurationMs: 3600000})
	if err != nil {
		t.Fatal(err)
	}
	f.store, f.fence, f.host = fresh, FenceOf(lease), NewReferenceEffectHost(fresh)
	if f.view, err = UnattendedRunStore(f.store, f.fence, f.host); err != nil {
		t.Fatal(err)
	}
	session.Context.RunStore = &DurableRunInput{Store: f.view, ID: goalRun}
	f.run = openRun(t, session)
	return f
}

// goalWrongEntry answers each command with the record's first entry.
type goalWrongEntry struct{ durableGoalStore }

func (s goalWrongEntry) CommandGoal(ctx context.Context, input GoalTransition) (GoalAnswer, error) {
	answer, err := s.durableGoalStore.CommandGoal(ctx, input)
	if err == nil {
		answer.Entry = answer.Goal.Journal[0]
	}
	return answer, err
}

func TestGoalReadsTheStoresAnswerBack(t *testing.T) {
	f, bound := goalBound(t)
	defer f.run.Context.Close()
	if _, err := RunGoalCommand(context.Background(), goalWrongEntry{f.store}, f.command("pause", bound.Goal.Revision, nil)); !goalRefusedWith(err, "invalid") {
		t.Fatal(err)
	}
}

// goalReadsBefore runs once, just after the achievement's read of the record: the bind it follows lands there.
type goalReadsBefore struct {
	durableGoalStore
	mu   *sync.Mutex
	once *func()
}

func (s goalReadsBefore) ReadGoal(ctx context.Context, resource string) (*RunGoal, error) {
	out, err := s.durableGoalStore.ReadGoal(ctx, resource)
	s.mu.Lock()
	f := *s.once
	*s.once = nil
	s.mu.Unlock()
	if f != nil {
		f()
	}
	return out, err
}

// goalIgnoresRead is a host store that does not compare the revision an achievement's finding was read from.
type goalIgnoresRead struct{ goalReadsBefore }

func (s goalIgnoresRead) CommandGoal(ctx context.Context, input GoalTransition) (GoalAnswer, error) {
	if input.Command.Kind == "achieve" && !input.Command.ExpectedRevision.Null {
		read := input.Command.ExpectedRevision.Value
		input.Read = &read
	}
	return s.goalReadsBefore.CommandGoal(ctx, input)
}

// H-1 (#313): an achievement sent with the bind it follows is never accepted, whichever lands first. Sent together,
// either order may happen: read before the bind it is revision-changed, read after it the bound Plan's steps are
// unfinished (step-incomplete). Each order is also a case of its own: read just before the bind lands it is strictly
// revision-changed, and with the bind landed first it is step-incomplete. Under a store that does not compare the
// revision read, the package's own finding, plan-unbound, still refuses it. Nothing of the objective is achieved, and
// the steps stay as the model left them.
func TestGoalAnAchievementReadBeforeItsBindIsRefused(t *testing.T) {
	reasons := map[string][]string{
		"together":             {"revision-changed", "step-incomplete"},
		"the bind lands first": {"step-incomplete"},
		"read before the bind": {"revision-changed"},
		"a store that does not compare what was read": {"plan-unbound"},
	}
	for _, how := range []string{"together", "the bind lands first", "read before the bind", "a store that does not compare what was read"} {
		t.Run(how, func(t *testing.T) {
			f := newGoalFlow(t)
			defer f.run.Context.Close()
			f.tool("update_plan", `{"explanation":"work","plan":[{"id":"step-1","step":"Inspect","status":"in_progress"},{"id":"step-2","step":"Report","status":"pending"}]}`)
			f.tool("lookup", `{"n":1}`)
			created := f.must(f.command("create", 0, func(c *GoalCommand) { c.Objective = goalKitString("objective-1") }))
			head := f.head()
			bind := f.command("bind", created.Goal.Revision, func(c *GoalCommand) { c.Steps = []string{"step-1", "step-2"}; c.Head = &head })
			achieve := f.command("achieve", created.Goal.Revision+1, func(c *GoalCommand) { c.Head = &head })
			var bindErr, achieveErr error
			switch how {
			case "together":
				var wg sync.WaitGroup
				wg.Add(2)
				go func() { defer wg.Done(); _, bindErr = f.send(bind) }()
				go func() { defer wg.Done(); _, achieveErr = f.send(achieve) }()
				wg.Wait()
			case "the bind lands first":
				_, bindErr = f.send(bind)
				_, achieveErr = f.send(achieve)
			default:
				once := func() { _, bindErr = f.send(bind) }
				var store RunStore = goalReadsBefore{durableGoalStore: f.store, mu: &sync.Mutex{}, once: &once}
				if how != "read before the bind" {
					store = goalIgnoresRead{goalReadsBefore{durableGoalStore: f.store, mu: &sync.Mutex{}, once: &once}}
				}
				_, achieveErr = RunGoalCommand(context.Background(), store, achieve)
			}
			reason := ""
			for _, allowed := range reasons[how] {
				if goalRefusedWith(achieveErr, allowed) {
					reason = allowed
				}
			}
			if bindErr != nil || reason == "" {
				t.Fatal(bindErr, achieveErr)
			}
			held, _ := ReadGoal(context.Background(), f.store, goalRun)
			last := held.Objectives[len(held.Objectives)-1]
			outcomes := []string{}
			for _, entry := range held.Journal {
				outcomes = append(outcomes, entry.Kind+":"+entry.Outcome)
			}
			sort.Strings(outcomes)
			steps := []string{}
			for _, row := range f.saved().Get("plan").Items() {
				steps = append(steps, planText(row.Get("status")))
			}
			if last.State != "active" || last.Plan == nil || last.Achieved != nil || !slices.Equal(outcomes, []string{"achieve:" + reason, "bind:accepted", "create:accepted"}) || !slices.Equal(steps, []string{"in_progress", "pending"}) {
				t.Fatal(last, outcomes, steps)
			}
		})
	}
}

// H-2 (#313): a repeat of an old objective's abandon, after a new objective's run started job B, sends B nothing,
// moves no lease and writes nothing: the journal and objective 2 say what happened.
func TestGoalARepeatOfAnOldAbandonActsOnNoNewObjective(t *testing.T) {
	ctx := context.Background()
	f, bound := goalBound(t)
	defer f.run.Context.Close()
	jobA := f.goalStartedJob("op-1", "goal.job.0001.Q7z2Xm9LpV4cR8tNb3Yh6Df1Gj5K")
	sent := []string{}
	cancel := func(_ context.Context, input JobCancelInput) error { sent = append(sent, input.Job); return nil }
	abandon := f.command("abandon", bound.Goal.Revision, nil)
	first, err := AbandonGoal(ctx, f.store, f.host, abandon, 3600000, cancel)
	if err != nil || first.Fence == nil || !slices.Equal(sent, []string{jobA}) {
		t.Fatal(err, sent)
	}
	created := f.must(f.command("create", first.Goal.Revision, func(c *GoalCommand) { c.Objective = goalKitString("objective-2") }))
	f.fence = *first.Fence
	jobB := f.goalStartedJob("op-2", "goal.job.0002.Hs8Pq3Lz6Wd1Rm9Tc4Vb7Nx2Ka5U")
	lease, _ := f.store.ReadLease(ctx, goalRun)
	before, _ := ReadGoal(ctx, f.store, goalRun)
	if _, err := AbandonGoal(ctx, f.store, f.host, abandon, 3600000, cancel); !goalRefusedWith(err, "revision-changed") {
		t.Fatal(err)
	}
	leaseAfter, _ := f.store.ReadLease(ctx, goalRun)
	after, _ := ReadGoal(ctx, f.store, goalRun)
	if !slices.Equal(sent, []string{jobA}) || slices.Contains(sent, jobB) || !goalKitSame(leaseAfter, lease) || !goalKitSame(after, before) {
		t.Fatal(sent, leaseAfter, after)
	}
	last := after.Objectives[len(after.Objectives)-1]
	tail := []string{}
	for _, entry := range after.Journal[len(after.Journal)-3:] {
		tail = append(tail, entry.Kind+":"+entry.Outcome+":"+entry.Objective)
	}
	if last.ID != "objective-2" || last.State != "active" || after.Revision != created.Goal.Revision || !slices.Equal(tail, []string{"abandon:accepted:objective-1", "abandon:delivered:objective-1", "create:accepted:objective-2"}) {
		t.Fatal(last, tail)
	}
	// #318 L-9: objective 2 abandoned in its turn, by a command of its own; a replay of objective 1's abandon still acts
	// on nothing.
	own, err := AbandonGoal(ctx, f.store, f.host, f.command("abandon", after.Revision, nil), 3600000, cancel)
	if err != nil || own.Goal.Objectives[len(own.Goal.Objectives)-1].State != "abandoned" || !slices.Contains(sent, jobB) {
		t.Fatal(err, sent)
	}
	sends := len(sent)
	leaseNow, _ := f.store.ReadLease(ctx, goalRun)
	goalNow, _ := ReadGoal(ctx, f.store, goalRun)
	if _, err := AbandonGoal(ctx, f.store, f.host, abandon, 3600000, cancel); !goalRefusedWith(err, "revision-changed") {
		t.Fatal(err)
	}
	leaseLater, _ := f.store.ReadLease(ctx, goalRun)
	goalLater, _ := ReadGoal(ctx, f.store, goalRun)
	if len(sent) != sends || !goalKitSame(leaseLater, leaseNow) || !goalKitSame(goalLater, goalNow) {
		t.Fatal(sent, leaseLater, goalLater)
	}
}
