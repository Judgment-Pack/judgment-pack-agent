package runcontext

import (
	"context"
	"encoding/json"
	"sort"
	"unicode/utf8"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

// The host's goal commands (ADR-0025 decision 8, #230). None calls a model, commits to the run or writes a
// checkpoint, wait, budget or approval: each is one goal transaction, and an abandon adds the job abandon's own.
// The package reads what only it can (the head's accepted Plan and its evidence) and hands the store the head it
// read; the store decides against the head it holds. The TypeScript twin is
// packages/harness/storage/goalCommands.ts.

func goalPortOf(store RunStore) (GoalRunStore, error) {
	if view := leaseViewOf(store); view != nil {
		store = view.store
	}
	held, ok := store.(GoalRunStore)
	if !ok || held.GoalVersion() != GoalRecordVersion || held.JobVersion() != JobRecordVersion || held.WaitVersion() != WaitRecordVersion || held.LeaseVersion() != LeaseRecordVersion || store.Durability() == RunStoreSingleWriterCallback {
		return nil, &GoalCapabilityRefused{Reason: "store-unsupported"}
	}
	if held.GoalLifetime() != GoalLifetimeDurable || held.JobLifetime() != JobLifetimeDurable || held.WaitLifetime() != WaitLifetimeDurable || held.LeaseLifetime() != LeaseLifetimeDurable {
		return nil, &GoalCapabilityRefused{Reason: "store-lifetime"}
	}
	return held, nil
}

// AdmitGoals: an objective needs a store whose goal record, leases, waits and jobs outlive its process. Anything
// else refuses by name.
func AdmitGoals(store RunStore) error {
	_, err := goalPortOf(store)
	return err
}
func goalReadBack(goal RunGoal, resource string) (RunGoal, error) {
	raw, err := json.Marshal(goal)
	if err != nil {
		return RunGoal{}, goalInvalid()
	}
	held, err := ReadRunGoal(raw)
	if err != nil {
		return RunGoal{}, err
	}
	if held.Resource != resource {
		return RunGoal{}, goalInvalid()
	}
	return held, nil
}

// goalAnswerOf: whatever the store answers is read through the readers, and it must be this command's own
// accepted entry.
func goalAnswerOf(answer GoalAnswer, command GoalCommand) (GoalAnswer, error) {
	goal, err := goalReadBack(answer.Goal, command.Resource)
	if err != nil {
		return GoalAnswer{}, err
	}
	raw, _ := json.Marshal(answer.Entry)
	entry, err := ReadGoalEntry(raw)
	if err != nil {
		return GoalAnswer{}, err
	}
	digest, err := GoalCommandDigest(command)
	if err != nil {
		return GoalAnswer{}, err
	}
	journaled := false
	for _, row := range goal.Journal {
		if string(goalJSON(row)) == string(goalJSON(entry)) {
			journaled = true
		}
	}
	if entry.Command != command.Command || entry.Kind != command.Kind || entry.Digest != digest || entry.Outcome != "accepted" || !journaled {
		return GoalAnswer{}, goalInvalid()
	}
	out := GoalAnswer{Goal: goal, Entry: entry}
	if answer.Lease != nil && (command.Kind == "pause" || command.Kind == "resume") {
		raw, _ := json.Marshal(*answer.Lease)
		lease, err := ReadRunLease(raw)
		if err != nil {
			return GoalAnswer{}, err
		}
		if lease.Resource != command.Resource {
			return GoalAnswer{}, goalInvalid()
		}
		out.Lease = &lease
	}
	return out, nil
}

// goalCheckpoint is the head's checkpoint, read through its reader, as a value and as a record; nil where it does
// not read.
func goalCheckpoint(raw []byte) (*Value, *contract.Checkpoint) {
	if !utf8.Valid(raw) {
		return nil, nil
	}
	value, err := ParseJSON(raw)
	if err != nil {
		return nil, nil
	}
	state, err := ReadExecution(value)
	if err != nil || state == nil {
		return nil, nil
	}
	return value, state
}

// goalHeadOf is the run head the command names, as the store holds it now; nil where it is not that head.
func goalHeadOf(ctx context.Context, port GoalRunStore, command GoalCommand) (*RunHead, error) {
	head, err := port.Load(ctx, command.Resource)
	if err != nil {
		return nil, err
	}
	if head == nil || head.Checkpoint == nil || head.Revision != command.Head.Revision || CheckpointBytesDigest(head.Checkpoint) != command.Head.Digest {
		return nil, nil
	}
	return head, nil
}
func goalFindingOf(reason string, step string) *GoalFinding {
	finding := GoalFinding{Reason: reason}
	if step != "" {
		finding.Step = &step
	}
	return &finding
}
func goalOriginOf(step, accepted *Value) (int, bool) {
	origin := step.Get("origin")
	if origin == nil || planText(origin.Get("plan")) != planText(accepted.Get("id")) || planText(origin.Get("revision")) != planText(accepted.Get("revision")) {
		return 0, false
	}
	return int(origin.Get("index").Num()), true
}

// goalBindFinding (M11): a bind takes the head's accepted Plan, nonempty unique steps with origins, and no step
// already closed.
func goalBindFinding(ctx context.Context, port GoalRunStore, command GoalCommand) (*GoalPlanSteps, *GoalFinding, error) {
	head, err := goalHeadOf(ctx, port, command)
	if err != nil {
		return nil, nil, err
	}
	if head == nil {
		return nil, goalFindingOf("head-changed", ""), nil
	}
	value, _ := goalCheckpoint(head.Checkpoint)
	accepted := value.Get("planState").Get("accepted")
	if value == nil || accepted == nil || !validStorageID(planText(accepted.Get("id"))) {
		return nil, goalFindingOf("plan-invalid", ""), nil
	}
	type held struct {
		step      string
		at, index int
	}
	rows := []held{}
	plan := value.Get("plan").Items()
	for _, step := range command.Steps {
		at := -1
		for i, row := range plan {
			if planText(row.Get("id")) == step {
				at = i
				break
			}
		}
		if at < 0 {
			return nil, goalFindingOf("plan-invalid", ""), nil
		}
		index, ok := goalOriginOf(plan[at], accepted)
		if !ok {
			return nil, goalFindingOf("plan-invalid", ""), nil
		}
		if status := planText(plan[at].Get("status")); status == "completed" || status == "not-needed" {
			return nil, goalFindingOf("step-settled", step), nil
		}
		rows = append(rows, held{step, at, index})
	}
	sort.SliceStable(rows, func(a, b int) bool { return rows[a].at < rows[b].at })
	out := GoalPlanSteps{ID: planText(accepted.Get("id")), Revision: planText(accepted.Get("revision"))}
	for _, row := range rows {
		out.Steps = append(out.Steps, GoalStep{Step: row.step, Index: int64(row.index)})
	}
	return &out, nil, nil
}
func goalRunJobs(ctx context.Context, port GoalRunStore, resource string) (map[string]RunJob, error) {
	jobs := map[string]RunJob{}
	after := ""
	for {
		page, err := port.ListJobs(ctx, resource, after, JobPageLimit)
		if err != nil {
			return nil, err
		}
		for _, job := range page.Jobs {
			jobs[job.Operation] = job
		}
		if page.Next == "" {
			return jobs, nil
		}
		after = page.Next
	}
}

// goalAchieveFinding is an achievement's evidence, re-verified from the head's own book: the accepted Plan as
// bound, nothing pending, no required step changed, removed or moved out of the required steps' order (M17), each
// required step completed with evidence that proves it now, and a delegated one by the writer's history check
// (M16).
func goalAchieveFinding(ctx context.Context, port GoalRunStore, command GoalCommand) (*GoalFinding, error) {
	record, err := port.ReadGoal(ctx, command.Resource)
	if err != nil {
		return nil, err
	}
	// Without a Plan bound the store refuses what its record says.
	if record == nil {
		return nil, nil
	}
	goal, err := goalReadBack(*record, command.Resource)
	if err != nil {
		return nil, err
	}
	bound := goal.Objectives[len(goal.Objectives)-1].Plan
	if bound == nil {
		return nil, nil
	}
	head, err := goalHeadOf(ctx, port, command)
	if err != nil {
		return nil, err
	}
	if head == nil {
		return goalFindingOf("head-changed", ""), nil
	}
	value, state := goalCheckpoint(head.Checkpoint)
	if value == nil {
		return goalFindingOf("lineage-changed", ""), nil
	}
	if state.Status == "paused" || state.Approval != nil && state.Approval.State == "asked" || state.PlanState != nil && state.PlanState.Pending != nil {
		return goalFindingOf("decision-pending", ""), nil
	}
	accepted := value.Get("planState").Get("accepted")
	if accepted == nil || planText(accepted.Get("id")) != bound.ID || planText(accepted.Get("revision")) != bound.Revision {
		return goalFindingOf("lineage-changed", ""), nil
	}
	// M17: steps the model added, and moves among them, are no departure here; a required step changed, removed or
	// reordered against the other required steps is, whoever approved it.
	plan := value.Get("plan").Items()
	texts := accepted.Get("steps").Items()
	at := make([]int, len(bound.Steps))
	for k, required := range bound.Steps {
		at[k] = -1
		for i, row := range plan {
			if index, ok := goalOriginOf(row, accepted); ok && int64(index) == required.Index {
				at[k] = i
				break
			}
		}
		if at[k] < 0 || int(required.Index) >= len(texts) || planText(plan[at[k]].Get("step")) != planText(texts[required.Index]) || k > 0 && at[k] <= at[k-1] {
			return goalFindingOf("lineage-changed", ""), nil
		}
	}
	for k, required := range bound.Steps {
		if row := plan[at[k]]; planText(row.Get("status")) != "completed" || !row.Has("evidence") {
			return goalFindingOf("step-incomplete", required.Step), nil
		}
	}
	jobs, err := goalRunJobs(ctx, port, command.Resource)
	if err != nil {
		return nil, err
	}
	book := DurableBook{Dispatches: []DurableDispatch{}, Observations: []DurableObservation{}, Children: []DurableChild{}}
	if value.Has("durable") && json.Unmarshal(value.Get("durable").JSON(), &book) != nil {
		return goalFindingOf("lineage-changed", ""), nil
	}
	scope := DurableScope{Execution: command.Resource, Role: "author"}
	var schedule TaskSchedule
	if rows := value.Get("planState").Get("taskSchedule"); rows != nil && json.Unmarshal(rows.JSON(), &schedule) != nil {
		return goalFindingOf("lineage-changed", ""), nil
	}
	var delegated map[string]TaskCompletionEvidence
	checked := false
	for k, required := range bound.Steps {
		evidence := plan[at[k]].Get("evidence")
		if !evidence.Has("child") {
			if planEvidenceFailure(evidence, &book, scope, jobs) != "" {
				return goalFindingOf("evidence-refused", required.Step), nil
			}
			continue
		}
		// M16: a delegated step is proven only by the history transaction that recorded its child; a store without
		// history cannot.
		history, ok := port.(HistoryRunStore)
		if !ok || history.HistoryVersion() != 1 {
			return goalFindingOf("history-unsupported", required.Step), nil
		}
		if !checked {
			checked = true
			saved := []TaskStepRow{}
			for _, row := range schedule.Rows {
				if row.Child != nil && row.Child.Result != nil {
					saved = append(saved, row)
				}
			}
			if verified, err := verifyTaskEvidence(ctx, history, command.Resource, head.Revision, head.Checkpoint, saved, scope); err == nil {
				delegated = verified
			}
		}
		verified, found := delegated[required.Step]
		var row *TaskStepRow
		for i := range schedule.Rows {
			if schedule.Rows[i].ID == required.Step {
				row = &schedule.Rows[i]
			}
		}
		if !found || row == nil || row.Child == nil || row.Child.Result == nil || evidence.Get("operations").Len() != 0 || evidence.Get("artifacts").Len() != 0 {
			return goalFindingOf("evidence-refused", required.Step), nil
		}
		expected := ObjectOf("task", String(verified.Task), "checkpointDigest", String(verified.CheckpointDigest), "resultDigest", String(verified.ResultDigest), "eventDigest", String(verified.EventDigest), "commitId", String(verified.CommitID))
		result, _ := json.Marshal(verified.TaskChildResult)
		held, _ := json.Marshal(*row.Child.Result)
		if CanonicalJSON(evidence.Get("child")) != CanonicalJSON(expected) || string(goalCanonical(held)) != string(goalCanonical(result)) {
			return goalFindingOf("evidence-refused", required.Step), nil
		}
	}
	return nil, nil
}
func goalCanonical(raw []byte) []byte {
	value, err := ParseJSON(raw)
	if err != nil {
		return nil
	}
	return []byte(CanonicalJSON(value))
}

// RunGoalCommand is a host's goal command: create, bind, achieve, pause, resume, block or unblock (abandon is
// AbandonGoal). One goal transaction: no model call, no run commit, no checkpoint, wait, budget or approval
// written. Accepted: the record and the command's entry. Refused: GoalRefused, journaled where the run has a goal
// record. A repeat of the command answers as the first did.
func RunGoalCommand(ctx context.Context, store RunStore, input GoalCommand) (GoalAnswer, error) {
	port, err := goalPortOf(store)
	if err != nil {
		return GoalAnswer{}, err
	}
	command, err := ReadGoalCommand(goalJSON(input))
	if err != nil {
		return GoalAnswer{}, err
	}
	if command.Kind == "abandon" {
		return GoalAnswer{}, goalInvalid()
	}
	transition := GoalTransition{Version: GoalRecordVersion, Command: command}
	switch command.Kind {
	case "bind":
		plan, finding, err := goalBindFinding(ctx, port, command)
		if err != nil {
			return GoalAnswer{}, err
		}
		transition.Plan, transition.Refused = plan, finding
	case "achieve":
		finding, err := goalAchieveFinding(ctx, port, command)
		if err != nil {
			return GoalAnswer{}, err
		}
		transition.Refused = finding
	}
	answer, err := port.CommandGoal(ctx, transition)
	if err != nil {
		return GoalAnswer{}, err
	}
	return goalAnswerOf(answer, command)
}

// GoalAbandonAnswer is an objective's abandon: its record, its entry, the job abandon's fence (none where no lease
// could be granted) and what became of each job.
type GoalAbandonAnswer struct {
	GoalAnswer
	Fence *LeaseFence
	Jobs  []JobAbandonDelivery
}

// AbandonGoal is ADR-0025 decision 8: abandon the objective, then ask a cancel of every started job of the run
// (AbandonJobs: the lease under its reserved owner at a new epoch, the command standing for the person's
// approval). The objective's record holds what the abandon left, the run's waits not resumed or ended and its jobs
// not settled, and each attempt's deliveries are journaled: the job abandon closes and journals nothing itself. A
// retry of the command is answered from the record and sends again only what the effect host has not admitted.
// None of it proves anything stopped.
func AbandonGoal(ctx context.Context, store RunStore, host EffectHost, input GoalCommand, durationMs int64, cancel func(context.Context, JobCancelInput) error) (GoalAbandonAnswer, error) {
	port, err := goalPortOf(store)
	if err != nil {
		return GoalAbandonAnswer{}, err
	}
	command, err := ReadGoalCommand(goalJSON(input))
	if err != nil {
		return GoalAbandonAnswer{}, err
	}
	if command.Kind != "abandon" {
		return GoalAbandonAnswer{}, goalInvalid()
	}
	if err := AdmitJobs(port, host); err != nil {
		return GoalAbandonAnswer{}, err
	}
	answer, err := port.CommandGoal(ctx, GoalTransition{Version: GoalRecordVersion, Command: command})
	if err != nil {
		return GoalAbandonAnswer{}, err
	}
	held, err := goalAnswerOf(answer, command)
	if err != nil {
		return GoalAbandonAnswer{}, err
	}
	taken, err := AbandonJobs(ctx, port, host, command.Resource, command.Command, durationMs, cancel)
	if err != nil {
		return GoalAbandonAnswer{}, err
	}
	deliveries := []GoalDelivery{}
	for _, job := range taken.Jobs {
		deliveries = append(deliveries, GoalDelivery{Job: job.Job, Delivery: job.Delivery})
	}
	recorded, err := port.RecordGoalDeliveries(ctx, GoalDeliveries{Version: GoalRecordVersion, Resource: command.Resource, Command: command.Command, Deliveries: deliveries})
	if err != nil {
		return GoalAbandonAnswer{}, err
	}
	goal, err := goalReadBack(recorded, command.Resource)
	if err != nil {
		return GoalAbandonAnswer{}, err
	}
	return GoalAbandonAnswer{GoalAnswer: GoalAnswer{Goal: goal, Entry: held.Entry}, Fence: taken.Fence, Jobs: taken.Jobs}, nil
}

// InspectGoal (M12) is the record, with the inspection counted and no revision moved. Nil where the run has none.
func InspectGoal(ctx context.Context, store RunStore, resource, command, actor string) (*RunGoal, error) {
	port, err := goalPortOf(store)
	if err != nil {
		return nil, err
	}
	input := GoalInspect{Version: GoalRecordVersion, Resource: resource, Command: command, Actor: actor}
	if _, err := ReadGoalInspect(goalJSON(input)); err != nil {
		return nil, err
	}
	held, err := port.InspectGoal(ctx, input)
	if err != nil || held == nil {
		return nil, err
	}
	goal, err := goalReadBack(*held, resource)
	if err != nil {
		return nil, err
	}
	return &goal, nil
}

// ReadGoal is the run's goal record, or nil.
func ReadGoal(ctx context.Context, store RunStore, resource string) (*RunGoal, error) {
	port, err := goalPortOf(store)
	if err != nil {
		return nil, err
	}
	held, err := port.ReadGoal(ctx, resource)
	if err != nil || held == nil {
		return nil, err
	}
	goal, err := goalReadBack(*held, resource)
	if err != nil {
		return nil, err
	}
	return &goal, nil
}

// GoalReportFor is the run's goal report, or nil where it has no record.
func GoalReportFor(ctx context.Context, store RunStore, resource string) (*GoalReport, error) {
	goal, err := ReadGoal(ctx, store, resource)
	if err != nil || goal == nil {
		return nil, err
	}
	report, err := GoalReportOf(*goal)
	if err != nil {
		return nil, err
	}
	return &report, nil
}
