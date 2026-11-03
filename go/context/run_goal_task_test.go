package runcontext

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

// #230 M16: a delegated required step is re-verified by the writer's history check. The TypeScript twin is the M16
// case in packages/harness/storage/goal.test.ts.

// goalNoHistory is the store with its history capability hidden.
type goalNoHistory struct{ GoalRunStore }

// goalAlteredHistory answers every history object with one byte changed.
type goalAlteredHistory struct{ durableGoalStore }

func (s goalAlteredHistory) ReadHistoryObject(ctx context.Context, resource HistoryResource, revision, digest string, offset, limit int) (HistoryObjectPage, error) {
	page, err := s.durableGoalStore.ReadHistoryObject(ctx, resource, revision, digest, offset, limit)
	if err == nil && len(page.Value) > 0 {
		page.Value = append([]byte{page.Value[0] ^ 1}, page.Value[1:]...)
	}
	return page, err
}

func TestGoalADelegatedStepIsReverifiedByTheWritersHistoryCheck(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store := durableGoalStore{NewMemoryRunStore(MemoryRunStoreOptions{Now: func() time.Time { return at }})}
	host := NewReferenceEffectHost(store)
	lease, err := store.AcquireLease(ctx, LeaseAcquire{Resource: "task-steps-183", Owner: "worker-a", DurationMs: 3600000})
	if err != nil {
		t.Fatal(err)
	}
	view, err := UnattendedRunStore(store, FenceOf(lease), host)
	if err != nil {
		t.Fatal(err)
	}
	session := task183PackageSession(t, view)
	session.Context.Plan = goalAcceptedPlan("Inspect first", "Inspect next")
	session.Context.Phase = "work"
	input, err := PrepareDurableInput(ctx, session.Context)
	if err != nil {
		t.Fatal(err)
	}
	session.Context = input
	binding := input.Persistence
	tasks := []*contract.DelegatedTask{}
	action, err := binding.Writer.TaskDelegationTool(*binding, func(ctx context.Context, request contract.DelegatedTaskRequest) (ToolResult, error) {
		started := []*contract.DelegatedTask{}
		for _, item := range request.Tasks {
			started = append(started, &contract.DelegatedTask{ID: newStorageIdentity(), ParentRunID: newStorageIdentity(), Title: item.Title, Objective: item.Objective, Profile: item.Profile, Subagent: item.Subagent, State: "queued", CreatedAt: "2026-10-26T00:00:00Z", Steps: []contract.DelegatedTaskStepsItem{}})
		}
		if err := binding.Writer.BindTaskDelegation(ctx, *binding, request, started); err != nil {
			return ToolResult{}, err
		}
		tasks = append(tasks, started...)
		return ToolResult{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	session.HostTools = append(session.HostTools, action)
	run := openRun(t, session)
	defer run.Context.Close()
	pin, _ := json.Marshal(SubagentRecord(run.Context.Subagents[0]))
	if err = task183Update(t, run, `[{"id":"step-1","step":"Inspect first","status":"pending","delegation":`+string(pin)+`,"budget":{"requests":1}},{"id":"step-2","step":"Inspect next","status":"pending","delegation":`+string(pin)+`,"budget":{"requests":1}}]`); err != nil {
		t.Fatal(err)
	}
	revision := planText(task183Saved(t, store).Get("planState").Get("revision"))
	if _, err = hostTool(t, run, "delegate_plan_steps").Execute(ctx, []byte(`{"planRevision":"`+revision+`","steps":["step-1","step-2"]}`)); err != nil {
		t.Fatal(err)
	}
	for _, task := range tasks {
		childSession, _ := trackedSession(t)
		childSession.Context = &Input{Objective: task.Objective, CurrentRequest: &CurrentRequest{Message: "Inspect", Input: "Inspect"}}
		ChildPersistence(session.Context, childSession.Context, task.ID)
		child := openRun(t, childSession)
		calls := 0
		if err = task183Model(child, &calls); err != nil {
			t.Fatal(err)
		}
		checkpoint, err := child.Context.Snapshot("complete")
		if err != nil {
			t.Fatal(err)
		}
		if err = childSession.Context.Checkpoint(ctx, checkpoint); err != nil {
			t.Fatal(err)
		}
		child.Context.Close()
		task.State = "complete"
		result := "Inspected."
		task.Result = &result
		if err = json.Unmarshal([]byte(`{"summary":"Inspected.","findings":[],"citations":[],"openQuestions":[],"confidence":"high","notDone":[]}`), &task.Report); err != nil {
			t.Fatal(err)
		}
		if _, err = binding.Writer.CommitTaskResult(ctx, *binding, contract.RunEvent{DelegatedTask: &contract.RunEventDelegatedTask{Type: "delegated_task", Task: *task}}); err != nil {
			t.Fatal(err)
		}
	}
	proof := binding.Writer.TaskCompletionProof(*binding)
	if len(proof) != 2 {
		t.Fatal("children not proven", len(proof))
	}
	// The objective binds both steps before they close.
	n := 0
	command := func(kind string, revision int64, edit func(*GoalCommand)) GoalCommand {
		n++
		c := GoalCommand{Version: 1, Resource: "task-steps-183", Command: "m16-" + string(rune('a'+n)), Actor: "person-1", ExpectedRevision: goalKitRevision(revision), Kind: kind}
		if edit != nil {
			edit(&c)
		}
		return c
	}
	headOf := func() GoalHead {
		head, _ := store.Load(ctx, "task-steps-183")
		return GoalHead{Revision: head.Revision, Digest: CheckpointBytesDigest(head.Checkpoint)}
	}
	created, err := RunGoalCommand(ctx, store, command("create", 0, func(c *GoalCommand) { c.Objective = goalKitString("objective-m16") }))
	if err != nil {
		t.Fatal(err)
	}
	head := headOf()
	bound, err := RunGoalCommand(ctx, store, command("bind", created.Goal.Revision, func(c *GoalCommand) { c.Steps = []string{"step-1", "step-2"}; c.Head = &head }))
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range proof {
		args := ObjectOf("id", String(row.Step), "expectedRevision", String(planText(task183Saved(t, store).Get("planState").Get("revision"))), "outcome", String("completed"), "evidence", ObjectOf("operations", Array(), "artifacts", Array(), "child", ObjectOf("task", String(row.Task), "checkpointDigest", String(row.CheckpointDigest), "resultDigest", String(row.ResultDigest), "eventDigest", String(row.EventDigest), "commitId", String(row.CommitID))))
		if _, err = hostTool(t, run, "complete_plan_step").Execute(ctx, args.JSON()); err != nil {
			t.Fatal(err)
		}
	}
	head = headOf()
	last := func(store RunStore) GoalEntry {
		held, _ := ReadGoal(ctx, store, "task-steps-183")
		return held.Journal[len(held.Journal)-1]
	}
	// A store that hides its history: the steps cannot be re-verified, and it says so.
	if _, err = RunGoalCommand(ctx, goalNoHistory{store}, command("achieve", bound.Goal.Revision, func(c *GoalCommand) { c.Head = &head })); !goalRefusedWith(err, "history-unsupported") {
		t.Fatal(err)
	}
	if entry := last(store); entry.Step == nil || *entry.Step != "step-1" {
		t.Fatal(entry)
	}
	// A history that no longer holds what recorded the children: not proven.
	if _, err = RunGoalCommand(ctx, goalAlteredHistory{store}, command("achieve", bound.Goal.Revision, func(c *GoalCommand) { c.Head = &head })); !goalRefusedWith(err, "evidence-refused") {
		t.Fatal(err)
	}
	if entry := last(store); entry.Outcome != "evidence-refused" || entry.Step == nil || *entry.Step != "step-1" {
		t.Fatal(entry)
	}
	// The honest store: both steps are proven by the transactions that recorded their children.
	done, err := RunGoalCommand(ctx, store, command("achieve", bound.Goal.Revision, func(c *GoalCommand) { c.Head = &head }))
	if err != nil || done.Goal.Objectives[0].State != "achieved" {
		t.Fatal(err)
	}
}
