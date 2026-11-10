package runcontext

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
)

// One durable goal (ADR-0025 decision 8, #230; contract/schemas/run-goal.json). A run may have one objective at
// a time, kept by the store beside the run, never in its checkpoint or event log, and changed only by host
// commands: no model call and no run commit. Each command carries its id, its actor and the record's expected
// revision, and the store decides it in one transaction and journals it, accepted or refused. Paused is a
// disposition, never a state. The package checks what only it can read (the head's accepted Plan and its
// evidence) and the store checks that the head it read is still the head. A command sent through a worker's
// fenced view carries that view's fence, and the store decides it only while the fence is current (#315); the
// command's actor is a label the host attests, never checked. The host owns scheduling, clocks, notifications and
// who may issue a command. The TypeScript twin is packages/harness/storage/goal.ts with goalCommands.ts.

const (
	GoalRecordVersion = 1
	// GoalObjectiveLimit bounds a run's objectives, the current one last. History is never overwritten: each
	// earlier one is kept whole.
	GoalObjectiveLimit = 8
	// GoalJournalLimit (M12) is the journal's bound. Past it the oldest refused entry goes first, then the
	// oldest entry, and discarded counts them, so no command is ever refused for size.
	GoalJournalLimit    = 64
	GoalStepLimit       = 20
	GoalWaitLimit       = 16
	GoalLifetimeDurable = "durable"
	GoalLifetimeProcess = "process"
)

type (
	RunGoal         = contract.RunGoal
	GoalObjective   = contract.RunGoalObjective
	GoalStamp       = contract.RunGoalStamp
	GoalStep        = contract.RunGoalStep
	GoalPlanSteps   = contract.RunGoalPlanSteps
	GoalPlan        = contract.RunGoalPlan
	GoalHead        = contract.RunGoalHead
	GoalJob         = contract.RunGoalJob
	GoalLeftWait    = contract.RunGoalLeftWait
	GoalBlock       = contract.RunGoalBlock
	GoalAchievement = contract.RunGoalAchievement
	GoalAbandonment = contract.RunGoalAbandonment
	GoalDelivery    = contract.RunGoalDelivery
	GoalEntry       = contract.RunGoalEntry
	GoalInspections = contract.RunGoalInspections
	GoalCommand     = contract.RunGoalCommand
	GoalFinding     = contract.RunGoalFinding
	GoalTransition  = contract.RunGoalTransition
	GoalInspect     = contract.RunGoalInspect
	GoalDeliveries  = contract.RunGoalDeliveries
	GoalReport      = contract.RunGoalReport
	GoalReportEntry = contract.RunGoalReportEntry
)

// GoalStates, GoalCommandKinds, GoalRefusalReasons and GoalCapabilityReasons are every value, in the vector's
// order. GoalFindings are what only the package can find from the head it read: the store records the finding
// as the command's refusal.
var (
	GoalStates            = []string{"active", "achieved", "abandoned", "blocked"}
	GoalCommandKinds      = []string{"create", "bind", "achieve", "pause", "resume", "block", "unblock", "abandon"}
	GoalRefusalReasons    = []string{"invalid", "conflict", "revision-changed", "no-objective", "objective-exists", "objective-reused", "goals-exhausted", "objective-terminal", "objective-paused", "not-paused", "objective-blocked", "not-blocked", "plan-bound", "plan-unbound", "plan-invalid", "plan-reused", "step-settled", "head-changed", "lineage-changed", "decision-pending", "wait-pending", "step-incomplete", "evidence-refused", "history-unsupported", "jobs-unsettled", "waiver-invalid", "wait-unresolved", "lease-required", "abandon-unfinished"}
	GoalCapabilityReasons = []string{"store-unsupported", "store-lifetime"}
	GoalFindings          = []string{"head-changed", "plan-invalid", "step-settled", "decision-pending", "lineage-changed", "step-incomplete", "evidence-refused", "history-unsupported", "plan-unbound"}
	goalStepFindings      = []string{"step-settled", "step-incomplete", "evidence-refused", "history-unsupported"}
	goalWaitReasons       = []string{"approval", "budget", "question", "reauthorization", "job"}
	// A job a goal lists is one that did not succeed: its outcome is never succeeded.
	goalJobOutcomes = []string{"failed", "cancelled", "unknown"}
	goalJobStates   = []string{"unproven", "started", "not-started", "completed"}
	goalDeliveries  = []string{"sent", "already-admitted", "failed", "undelivered", "no-handle", "settled", "contradicted", "acknowledged", "invalid"}
)

// GoalRefused is a named goal refusal. Where the run has a goal record the store journaled it, and nothing else
// changed.
type GoalRefused struct{ Reason string }

func (e *GoalRefused) Error() string { return "GoalRefused: " + e.Reason + "." }
func (e *GoalRefused) Unwrap() error {
	f := failure.New("stale-state", "storage", "not-started")
	detail := e.Error()
	f.Detail = &detail
	return failure.NewError(f)
}

// Record is the refusal's versioned record.
func (e *GoalRefused) Record() contract.RunGoalRefusal {
	return contract.RunGoalRefusal{Version: GoalRecordVersion, Reason: e.Reason}
}

// GoalCapabilityRefused: a store that cannot give the guarantee refuses by name before any command; it never
// pretends.
type GoalCapabilityRefused struct{ Reason string }

func (e *GoalCapabilityRefused) Error() string { return "GoalCapabilityRefused: " + e.Reason + "." }
func (e *GoalCapabilityRefused) Unwrap() error {
	detail := e.Error()
	f := failure.New("configuration", "storage", "not-started")
	f.Detail = &detail
	return failure.NewError(f)
}

// Record is the refusal's versioned record.
func (e *GoalCapabilityRefused) Record() contract.RunGoalCapabilityRefusal {
	return contract.RunGoalCapabilityRefusal{Version: GoalRecordVersion, Reason: e.Reason}
}

func goalRefused(reason string) error { return &GoalRefused{Reason: reason} }
func goalInvalid() error              { return goalRefused("invalid") }

// goalFields is one object of a goal record, read with exactly the members named; every failure is invalid.
type goalFields struct{ m waitMembers }

func goalShape(raw json.RawMessage, required, optional []string) (goalFields, error) {
	m, err := waitShape(raw, required, optional)
	if err != nil {
		return goalFields{}, goalInvalid()
	}
	return goalFields{m}, nil
}
func (g goalFields) has(name string) bool { return g.m.has(name) }
func (g goalFields) id(name string) (string, error) {
	value, err := g.m.id(name)
	if err != nil {
		return "", goalInvalid()
	}
	return value, nil
}
func (g goalFields) digest(name string) (string, error) {
	value, err := g.m.digest(name)
	if err != nil {
		return "", goalInvalid()
	}
	return value, nil
}
func (g goalFields) time(name string) (string, error) {
	value, err := g.m.time(name)
	if err != nil {
		return "", goalInvalid()
	}
	return value, nil
}
func (g goalFields) enum(name string, values []string) (string, error) {
	value, err := g.m.enum(name, values...)
	if err != nil {
		return "", goalInvalid()
	}
	return value, nil
}
func (g goalFields) count(name string, min int64) (int64, error) {
	value, err := g.m.integer(name, min, 9007199254740991)
	if err != nil {
		return 0, goalInvalid()
	}
	return value, nil
}
func (g goalFields) version() error {
	if g.m.version() != nil {
		return goalInvalid()
	}
	return nil
}
func (g goalFields) list(name string, min, max int) ([]json.RawMessage, error) {
	items, err := g.m.list(name, max)
	if err != nil || len(items) < min {
		return nil, goalInvalid()
	}
	return items, nil
}
func (g goalFields) stepID(name string) (string, error) {
	value, err := g.m.str(name)
	if err != nil || !goalStepID(value) {
		return "", goalInvalid()
	}
	return value, nil
}

// goalStepID is a plan step id: step-N with N a safe integer from 1.
func goalStepID(text string) bool {
	digits, ok := strings.CutPrefix(text, "step-")
	if !ok || len(digits) == 0 || len(digits) > 16 || digits[0] == '0' {
		return false
	}
	for _, c := range digits {
		if c < '0' || c > '9' {
			return false
		}
	}
	n, err := strconv.ParseInt(digits, 10, 64)
	return err == nil && n >= 1 && n <= 9007199254740991
}
func goalUnique[T any](items []T, key func(T) string) bool {
	seen := map[string]bool{}
	for _, item := range items {
		if seen[key(item)] {
			return false
		}
		seen[key(item)] = true
	}
	return true
}
func goalStampOf(raw json.RawMessage, extra, optional []string) (GoalStamp, goalFields, error) {
	g, err := goalShape(raw, append([]string{"command", "actor", "at"}, extra...), optional)
	if err != nil {
		return GoalStamp{}, g, err
	}
	var stamp GoalStamp
	if stamp.Command, err = g.id("command"); err != nil {
		return GoalStamp{}, g, err
	}
	if stamp.Actor, err = g.id("actor"); err != nil {
		return GoalStamp{}, g, err
	}
	if stamp.At, err = g.time("at"); err != nil {
		return GoalStamp{}, g, err
	}
	return stamp, g, nil
}
func readGoalStamp(raw json.RawMessage) (GoalStamp, error) {
	stamp, _, err := goalStampOf(raw, nil, nil)
	return stamp, err
}

// ReadGoalHead reads a run head as a goal names it.
func ReadGoalHead(raw json.RawMessage) (GoalHead, error) {
	g, err := goalShape(raw, []string{"revision", "digest"}, nil)
	if err != nil {
		return GoalHead{}, err
	}
	var head GoalHead
	if head.Revision, err = g.id("revision"); err != nil {
		return GoalHead{}, err
	}
	if head.Digest, err = g.digest("digest"); err != nil {
		return GoalHead{}, err
	}
	return head, nil
}
func readGoalSteps(items []json.RawMessage) ([]GoalStep, error) {
	steps := make([]GoalStep, 0, len(items))
	for _, item := range items {
		g, err := goalShape(item, []string{"step", "index"}, nil)
		if err != nil {
			return nil, err
		}
		var step GoalStep
		if step.Step, err = g.stepID("step"); err != nil {
			return nil, err
		}
		index, err := g.m.integer("index", 0, GoalStepLimit-1)
		if err != nil {
			return nil, goalInvalid()
		}
		step.Index = index
		steps = append(steps, step)
	}
	if !goalUnique(steps, func(s GoalStep) string { return s.Step }) || !goalUnique(steps, func(s GoalStep) string { return strconv.FormatInt(s.Index, 10) }) {
		return nil, goalInvalid()
	}
	return steps, nil
}
func readGoalPlanStepsOf(g goalFields) (GoalPlanSteps, error) {
	var plan GoalPlanSteps
	var err error
	if plan.ID, err = g.id("id"); err != nil {
		return GoalPlanSteps{}, err
	}
	if plan.Revision, err = g.digest("revision"); err != nil {
		return GoalPlanSteps{}, err
	}
	items, err := g.list("steps", 1, GoalStepLimit)
	if err != nil {
		return GoalPlanSteps{}, err
	}
	if plan.Steps, err = readGoalSteps(items); err != nil {
		return GoalPlanSteps{}, err
	}
	return plan, nil
}

// ReadGoalPlanSteps reads the accepted Plan a bind takes.
func ReadGoalPlanSteps(raw json.RawMessage) (GoalPlanSteps, error) {
	g, err := goalShape(raw, []string{"id", "revision", "steps"}, nil)
	if err != nil {
		return GoalPlanSteps{}, err
	}
	return readGoalPlanStepsOf(g)
}
func readGoalJob(raw json.RawMessage) (GoalJob, error) {
	g, err := goalShape(raw, []string{"job", "state"}, []string{"outcome", "contradicted"})
	if err != nil {
		return GoalJob{}, err
	}
	var job GoalJob
	if job.Job, err = g.digest("job"); err != nil {
		return GoalJob{}, err
	}
	if job.State, err = g.enum("state", goalJobStates); err != nil {
		return GoalJob{}, err
	}
	// An outcome exactly when completed; a contradiction only on a job closed as not started.
	if (job.State == "completed") != g.has("outcome") || g.has("contradicted") && (string(bytes.TrimSpace(g.m["contradicted"])) != "true" || job.State != "not-started") {
		return GoalJob{}, goalInvalid()
	}
	if g.has("outcome") {
		outcome, err := g.enum("outcome", goalJobOutcomes)
		if err != nil {
			return GoalJob{}, err
		}
		job.Outcome = &outcome
	}
	if g.has("contradicted") {
		yes := true
		job.Contradicted = &yes
	}
	return job, nil
}
func readGoalJobs(items []json.RawMessage) ([]GoalJob, error) {
	jobs := make([]GoalJob, 0, len(items))
	for _, item := range items {
		job, err := readGoalJob(item)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	if !goalUnique(jobs, func(j GoalJob) string { return j.Job }) {
		return nil, goalInvalid()
	}
	return jobs, nil
}
func readGoalDigests(g goalFields, name string) ([]string, error) {
	items, err := g.list(name, 1, JobRunLimit)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		var text string
		if json.Unmarshal(item, &text) != nil || !jobDigest.MatchString(text) {
			return nil, goalInvalid()
		}
		out = append(out, text)
	}
	if !goalUnique(out, func(s string) string { return s }) {
		return nil, goalInvalid()
	}
	return out, nil
}
func readGoalObjective(raw json.RawMessage) (GoalObjective, error) {
	g, err := goalShape(raw, []string{"id", "state", "created"}, []string{"plan", "paused", "blocked", "achieved", "abandoned"})
	if err != nil {
		return GoalObjective{}, err
	}
	var o GoalObjective
	if o.ID, err = g.id("id"); err != nil {
		return GoalObjective{}, err
	}
	if o.State, err = g.enum("state", GoalStates); err != nil {
		return GoalObjective{}, err
	}
	if o.Created, err = readGoalStamp(g.m["created"]); err != nil {
		return GoalObjective{}, err
	}
	if g.has("plan") {
		pg, err := goalShape(g.m["plan"], []string{"id", "revision", "steps", "bound"}, nil)
		if err != nil {
			return GoalObjective{}, err
		}
		steps, err := readGoalPlanStepsOf(pg)
		if err != nil {
			return GoalObjective{}, err
		}
		bound, err := readGoalStamp(pg.m["bound"])
		if err != nil {
			return GoalObjective{}, err
		}
		o.Plan = &GoalPlan{ID: steps.ID, Revision: steps.Revision, Steps: steps.Steps, Bound: bound}
	}
	if g.has("paused") {
		paused, err := readGoalStamp(g.m["paused"])
		if err != nil {
			return GoalObjective{}, err
		}
		o.Paused = &paused
	}
	if g.has("blocked") {
		stamp, bg, err := goalStampOf(g.m["blocked"], []string{"condition"}, nil)
		if err != nil {
			return GoalObjective{}, err
		}
		condition, err := bg.id("condition")
		if err != nil {
			return GoalObjective{}, err
		}
		o.Blocked = &GoalBlock{Command: stamp.Command, Actor: stamp.Actor, At: stamp.At, Condition: condition}
	}
	if g.has("achieved") {
		stamp, ag, err := goalStampOf(g.m["achieved"], []string{"head"}, []string{"waived", "unsuccessful"})
		if err != nil {
			return GoalObjective{}, err
		}
		head, err := ReadGoalHead(ag.m["head"])
		if err != nil {
			return GoalObjective{}, err
		}
		done := GoalAchievement{Command: stamp.Command, Actor: stamp.Actor, At: stamp.At, Head: head}
		if ag.has("waived") {
			if done.Waived, err = readGoalDigests(ag, "waived"); err != nil {
				return GoalObjective{}, err
			}
		}
		if ag.has("unsuccessful") {
			items, err := ag.list("unsuccessful", 1, JobRunLimit)
			if err != nil {
				return GoalObjective{}, err
			}
			if done.Unsuccessful, err = readGoalJobs(items); err != nil {
				return GoalObjective{}, err
			}
			for _, job := range done.Unsuccessful {
				if job.State != "not-started" && job.State != "completed" {
					return GoalObjective{}, goalInvalid()
				}
			}
		}
		o.Achieved = &done
	}
	if g.has("abandoned") {
		stamp, lg, err := goalStampOf(g.m["abandoned"], []string{"waits", "jobs"}, nil)
		if err != nil {
			return GoalObjective{}, err
		}
		left := GoalAbandonment{Command: stamp.Command, Actor: stamp.Actor, At: stamp.At, Waits: []GoalLeftWait{}}
		waits, err := lg.list("waits", 0, GoalWaitLimit)
		if err != nil {
			return GoalObjective{}, err
		}
		for _, item := range waits {
			wg, err := goalShape(item, []string{"wait", "reason", "state"}, nil)
			if err != nil {
				return GoalObjective{}, err
			}
			var wait GoalLeftWait
			if wait.Wait, err = wg.id("wait"); err != nil {
				return GoalObjective{}, err
			}
			if wait.Reason, err = wg.enum("reason", goalWaitReasons); err != nil {
				return GoalObjective{}, err
			}
			if wait.State, err = wg.enum("state", []string{"parked", "woken", "timed-out"}); err != nil {
				return GoalObjective{}, err
			}
			left.Waits = append(left.Waits, wait)
		}
		if !goalUnique(left.Waits, func(w GoalLeftWait) string { return w.Wait }) {
			return GoalObjective{}, goalInvalid()
		}
		jobs, err := lg.list("jobs", 0, JobRunLimit)
		if err != nil {
			return GoalObjective{}, err
		}
		if left.Jobs, err = readGoalJobs(jobs); err != nil {
			return GoalObjective{}, err
		}
		for _, job := range left.Jobs {
			if job.State != "unproven" && job.State != "started" {
				return GoalObjective{}, goalInvalid()
			}
		}
		o.Abandoned = &left
	}
	// Each state holds exactly what says it. Paused is a disposition of an open objective; a terminal one has
	// none (L2).
	var proves bool
	switch o.State {
	case "active":
		proves = o.Blocked == nil && o.Achieved == nil && o.Abandoned == nil
	case "blocked":
		proves = o.Blocked != nil && o.Achieved == nil && o.Abandoned == nil
	case "achieved":
		proves = o.Achieved != nil && o.Plan != nil && o.Blocked == nil && o.Abandoned == nil && o.Paused == nil
	default:
		proves = o.Abandoned != nil && o.Blocked == nil && o.Achieved == nil && o.Paused == nil
	}
	if !proves {
		return GoalObjective{}, goalInvalid()
	}
	return o, nil
}
func goalTerminal(state string) bool { return state == "achieved" || state == "abandoned" }
func readGoalObjectives(items []json.RawMessage) ([]GoalObjective, error) {
	objectives := make([]GoalObjective, 0, len(items))
	for _, item := range items {
		objective, err := readGoalObjective(item)
		if err != nil {
			return nil, err
		}
		objectives = append(objectives, objective)
	}
	if !goalUnique(objectives, func(o GoalObjective) string { return o.ID }) {
		return nil, goalInvalid()
	}
	for _, objective := range objectives[:len(objectives)-1] {
		if !goalTerminal(objective.State) {
			return nil, goalInvalid()
		}
	}
	plans := []string{}
	for _, objective := range objectives {
		if objective.Plan != nil {
			plans = append(plans, objective.Plan.ID+"\x00"+objective.Plan.Revision)
		}
	}
	if !goalUnique(plans, func(s string) string { return s }) {
		return nil, goalInvalid()
	}
	return objectives, nil
}
func readGoalDeliveryList(items []json.RawMessage) ([]GoalDelivery, error) {
	out := make([]GoalDelivery, 0, len(items))
	for _, item := range items {
		g, err := goalShape(item, []string{"job", "delivery"}, nil)
		if err != nil {
			return nil, err
		}
		var d GoalDelivery
		if d.Job, err = g.digest("job"); err != nil {
			return nil, err
		}
		if d.Delivery, err = g.enum("delivery", goalDeliveries); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	if !goalUnique(out, func(d GoalDelivery) string { return d.Job }) {
		return nil, goalInvalid()
	}
	return out, nil
}
func goalJournaledOutcomes() []string {
	out := []string{"accepted", "delivered"}
	for _, reason := range GoalRefusalReasons {
		if reason != "invalid" && reason != "conflict" {
			out = append(out, reason)
		}
	}
	return out
}
func readGoalEntry(raw json.RawMessage, report bool) (GoalEntry, error) {
	required := []string{"command", "kind", "actor", "at", "objective", "outcome", "revision"}
	if !report {
		required = append(required, "digest")
	}
	g, err := goalShape(raw, required, []string{"step", "deliveries"})
	if err != nil {
		return GoalEntry{}, err
	}
	var e GoalEntry
	if e.Command, err = g.id("command"); err != nil {
		return GoalEntry{}, err
	}
	if e.Kind, err = g.enum("kind", GoalCommandKinds); err != nil {
		return GoalEntry{}, err
	}
	if e.Actor, err = g.id("actor"); err != nil {
		return GoalEntry{}, err
	}
	if e.At, err = g.time("at"); err != nil {
		return GoalEntry{}, err
	}
	if e.Objective, err = g.id("objective"); err != nil {
		return GoalEntry{}, err
	}
	if e.Outcome, err = g.enum("outcome", goalJournaledOutcomes()); err != nil {
		return GoalEntry{}, err
	}
	if e.Revision, err = g.count("revision", 1); err != nil {
		return GoalEntry{}, err
	}
	if !report {
		if e.Digest, err = g.digest("digest"); err != nil {
			return GoalEntry{}, err
		}
	}
	// Deliveries exactly on an abandon's delivered entry; a step only on a refusal about one.
	if (e.Outcome == "delivered") != g.has("deliveries") || e.Outcome == "delivered" && e.Kind != "abandon" || g.has("step") && !slices.Contains(goalStepFindings, e.Outcome) {
		return GoalEntry{}, goalInvalid()
	}
	if g.has("step") {
		step, err := g.stepID("step")
		if err != nil {
			return GoalEntry{}, err
		}
		e.Step = &step
	}
	if g.has("deliveries") {
		items, err := g.list("deliveries", 0, JobRunLimit)
		if err != nil {
			return GoalEntry{}, err
		}
		if e.Deliveries, err = readGoalDeliveryList(items); err != nil {
			return GoalEntry{}, err
		}
	}
	return e, nil
}

// ReadGoalEntry reads one journaled entry.
func ReadGoalEntry(raw []byte) (GoalEntry, error) { return readGoalEntry(raw, false) }
func readGoalInspections(raw json.RawMessage) (*GoalInspections, error) {
	g, err := goalShape(raw, []string{"count", "last"}, nil)
	if err != nil {
		return nil, err
	}
	count, err := g.count("count", 1)
	if err != nil {
		return nil, err
	}
	last, err := readGoalStamp(g.m["last"])
	if err != nil {
		return nil, err
	}
	return &GoalInspections{Count: count, Last: contract.RunGoalInspectionsLast{Command: last.Command, Actor: last.Actor, At: last.At}}, nil
}

// ReadRunGoal reads a version 1 goal record and holds every rule the schema cannot state: one open objective at
// most, and only the last; identities never reused; no Plan bound twice in a run (M11); each state holding
// exactly what proves it.
func ReadRunGoal(raw []byte) (RunGoal, error) {
	g, err := goalShape(raw, []string{"version", "kind", "resource", "revision", "objectives", "journal"}, []string{"discarded", "inspections"})
	if err != nil {
		return RunGoal{}, err
	}
	if g.version() != nil || string(bytes.TrimSpace(g.m["kind"])) != `"goal"` {
		return RunGoal{}, goalInvalid()
	}
	goal := RunGoal{Version: GoalRecordVersion, Kind: "goal"}
	if goal.Resource, err = g.id("resource"); err != nil {
		return RunGoal{}, err
	}
	if goal.Revision, err = g.count("revision", 1); err != nil {
		return RunGoal{}, err
	}
	objectives, err := g.list("objectives", 1, GoalObjectiveLimit)
	if err != nil {
		return RunGoal{}, err
	}
	if goal.Objectives, err = readGoalObjectives(objectives); err != nil {
		return RunGoal{}, err
	}
	journal, err := g.list("journal", 0, GoalJournalLimit)
	if err != nil {
		return RunGoal{}, err
	}
	goal.Journal = []GoalEntry{}
	for _, item := range journal {
		entry, err := readGoalEntry(item, false)
		if err != nil {
			return RunGoal{}, err
		}
		goal.Journal = append(goal.Journal, entry)
	}
	if g.has("discarded") {
		discarded, err := g.count("discarded", 1)
		if err != nil {
			return RunGoal{}, err
		}
		goal.Discarded = &discarded
	}
	if g.has("inspections") {
		if goal.Inspections, err = readGoalInspections(g.m["inspections"]); err != nil {
			return RunGoal{}, err
		}
	}
	return goal, nil
}

// goalMembers is which members each command carries: required, then optional.
var goalMembers = map[string][2][]string{"create": {{"objective"}, nil}, "bind": {{"steps", "head"}, nil}, "achieve": {{"head"}, {"waive"}}, "pause": {nil, nil}, "resume": {nil, nil}, "block": {{"condition"}, nil}, "unblock": {nil, nil}, "abandon": {nil, nil}}

// ReadGoalCommand reads a host's command and holds which members its kind carries.
func ReadGoalCommand(raw []byte) (GoalCommand, error) {
	var probe map[string]json.RawMessage
	if json.Unmarshal(raw, &probe) != nil || probe == nil {
		return GoalCommand{}, goalInvalid()
	}
	var kind string
	if json.Unmarshal(probe["kind"], &kind) != nil || !slices.Contains(GoalCommandKinds, kind) {
		return GoalCommand{}, goalInvalid()
	}
	members := goalMembers[kind]
	g, err := goalShape(raw, append([]string{"version", "resource", "command", "actor", "expectedRevision", "kind"}, members[0]...), members[1])
	if err != nil {
		return GoalCommand{}, err
	}
	if g.version() != nil {
		return GoalCommand{}, goalInvalid()
	}
	c := GoalCommand{Version: GoalRecordVersion, Kind: kind}
	if c.Resource, err = g.id("resource"); err != nil {
		return GoalCommand{}, err
	}
	if c.Command, err = g.id("command"); err != nil {
		return GoalCommand{}, err
	}
	if c.Actor, err = g.id("actor"); err != nil {
		return GoalCommand{}, err
	}
	if string(bytes.TrimSpace(g.m["expectedRevision"])) == "null" {
		c.ExpectedRevision = contract.Nullable[int64]{Set: true, Null: true}
	} else {
		revision, err := g.count("expectedRevision", 1)
		if err != nil {
			return GoalCommand{}, err
		}
		c.ExpectedRevision = contract.Nullable[int64]{Set: true, Value: revision}
	}
	if g.has("objective") {
		objective, err := g.id("objective")
		if err != nil {
			return GoalCommand{}, err
		}
		c.Objective = &objective
	}
	if g.has("steps") {
		items, err := g.list("steps", 1, GoalStepLimit)
		if err != nil {
			return GoalCommand{}, err
		}
		for _, item := range items {
			var step string
			if json.Unmarshal(item, &step) != nil || !goalStepID(step) {
				return GoalCommand{}, goalInvalid()
			}
			c.Steps = append(c.Steps, step)
		}
		if !goalUnique(c.Steps, func(s string) string { return s }) {
			return GoalCommand{}, goalInvalid()
		}
	}
	if g.has("head") {
		head, err := ReadGoalHead(g.m["head"])
		if err != nil {
			return GoalCommand{}, err
		}
		c.Head = &head
	}
	if g.has("waive") {
		if c.Waive, err = readGoalDigests(g, "waive"); err != nil {
			return GoalCommand{}, err
		}
	}
	if g.has("condition") {
		condition, err := g.id("condition")
		if err != nil {
			return GoalCommand{}, err
		}
		c.Condition = &condition
	}
	return c, nil
}

// ReadGoalTransition reads what the store is handed. A bind carries its steps or what refused them, never both;
// an achievement carries the revision its finding was read from (#313 H-1) and may carry a finding; nothing else
// carries any of them. Any command may carry the fence of the worker's view it came through (#315), read as the
// lease reads a fence: a malformed one is an invalid transition, as any malformed member is.
func ReadGoalTransition(raw []byte) (GoalTransition, error) {
	g, err := goalShape(raw, []string{"version", "command"}, []string{"plan", "refused", "read", "fence"})
	if err != nil {
		return GoalTransition{}, err
	}
	if g.version() != nil {
		return GoalTransition{}, goalInvalid()
	}
	command, err := ReadGoalCommand(g.m["command"])
	if err != nil {
		return GoalTransition{}, err
	}
	t := GoalTransition{Version: GoalRecordVersion, Command: command}
	if g.has("fence") {
		fence, err := ReadLeaseFence(g.m["fence"])
		if err != nil {
			return GoalTransition{}, goalInvalid()
		}
		t.Fence = &fence
	}
	if g.has("read") != (command.Kind == "achieve") {
		return GoalTransition{}, goalInvalid()
	}
	if g.has("read") {
		read, err := g.count("read", 1)
		if err != nil {
			return GoalTransition{}, err
		}
		t.Read = &read
	}
	if g.has("plan") {
		plan, err := ReadGoalPlanSteps(g.m["plan"])
		if err != nil {
			return GoalTransition{}, err
		}
		t.Plan = &plan
	}
	if g.has("refused") {
		fg, err := goalShape(g.m["refused"], []string{"reason"}, []string{"step"})
		if err != nil {
			return GoalTransition{}, err
		}
		reason, err := fg.enum("reason", GoalFindings)
		if err != nil {
			return GoalTransition{}, err
		}
		finding := GoalFinding{Reason: reason}
		if fg.has("step") {
			if !slices.Contains(goalStepFindings, reason) {
				return GoalTransition{}, goalInvalid()
			}
			step, err := fg.stepID("step")
			if err != nil {
				return GoalTransition{}, err
			}
			finding.Step = &step
		}
		t.Refused = &finding
	}
	if command.Kind == "bind" && (t.Plan == nil) == (t.Refused == nil) || command.Kind != "bind" && (t.Plan != nil || t.Refused != nil && command.Kind != "achieve") {
		return GoalTransition{}, goalInvalid()
	}
	return t, nil
}

// ReadGoalInspect reads an inspection.
func ReadGoalInspect(raw []byte) (GoalInspect, error) {
	g, err := goalShape(raw, []string{"version", "resource", "command", "actor"}, nil)
	if err != nil {
		return GoalInspect{}, err
	}
	if g.version() != nil {
		return GoalInspect{}, goalInvalid()
	}
	in := GoalInspect{Version: GoalRecordVersion}
	if in.Resource, err = g.id("resource"); err != nil {
		return GoalInspect{}, err
	}
	if in.Command, err = g.id("command"); err != nil {
		return GoalInspect{}, err
	}
	if in.Actor, err = g.id("actor"); err != nil {
		return GoalInspect{}, err
	}
	return in, nil
}

// ReadGoalDeliveries reads an abandon's deliveries.
func ReadGoalDeliveries(raw []byte) (GoalDeliveries, error) {
	g, err := goalShape(raw, []string{"version", "resource", "command", "deliveries"}, nil)
	if err != nil {
		return GoalDeliveries{}, err
	}
	if g.version() != nil {
		return GoalDeliveries{}, goalInvalid()
	}
	in := GoalDeliveries{Version: GoalRecordVersion}
	if in.Resource, err = g.id("resource"); err != nil {
		return GoalDeliveries{}, err
	}
	if in.Command, err = g.id("command"); err != nil {
		return GoalDeliveries{}, err
	}
	items, err := g.list("deliveries", 0, JobRunLimit)
	if err != nil {
		return GoalDeliveries{}, err
	}
	if in.Deliveries, err = readGoalDeliveryList(items); err != nil {
		return GoalDeliveries{}, err
	}
	return in, nil
}
func readGoalReason(raw []byte, reasons []string) (string, error) {
	g, err := goalShape(raw, []string{"version", "reason"}, nil)
	if err != nil {
		return "", err
	}
	if g.version() != nil {
		return "", goalInvalid()
	}
	return g.enum("reason", reasons)
}

// ReadGoalRefusal reads a goal refusal's record.
func ReadGoalRefusal(raw []byte) (contract.RunGoalRefusal, error) {
	reason, err := readGoalReason(raw, GoalRefusalReasons)
	return contract.RunGoalRefusal{Version: GoalRecordVersion, Reason: reason}, err
}

// ReadGoalCapabilityRefusal reads a goal capability refusal's record.
func ReadGoalCapabilityRefusal(raw []byte) (contract.RunGoalCapabilityRefusal, error) {
	reason, err := readGoalReason(raw, GoalCapabilityReasons)
	return contract.RunGoalCapabilityRefusal{Version: GoalRecordVersion, Reason: reason}, err
}

// goalJSON is a goal value as canonical JSON bytes, read back through its reader.
func goalJSON(value any) []byte {
	raw, err := canonicalRaw(value)
	if err != nil {
		return nil
	}
	return raw
}

// GoalCommandDigest is a command's digest: a repeat of its id must carry exactly this.
func GoalCommandDigest(command GoalCommand) (string, error) {
	held, err := ReadGoalCommand(goalJSON(command))
	if err != nil {
		return "", err
	}
	return waitCanonicalDigest(held), nil
}

// GoalDeliveryDigest is the digest of a delivered entry: {command, deliveries}.
func GoalDeliveryDigest(input GoalDeliveries) (string, error) {
	held, err := ReadGoalDeliveries(goalJSON(input))
	if err != nil {
		return "", err
	}
	return waitCanonicalDigest(struct {
		Command    string         `json:"command"`
		Deliveries []GoalDelivery `json:"deliveries"`
	}{held.Command, held.Deliveries}), nil
}

// GoalJobOf is a job as a goal lists it.
func GoalJobOf(job RunJob) GoalJob {
	out := GoalJob{Job: job.Job, State: job.State}
	if job.Completion != nil {
		outcome := job.Completion.Outcome
		out.Outcome = &outcome
	}
	if len(job.Contradictions) > 0 {
		yes := true
		out.Contradicted = &yes
	}
	return out
}
func goalUnsettledJob(job RunJob) bool { return job.State == "unproven" || job.State == "started" }
func goalUnresolvedWait(wait *RunWait) bool {
	return wait == nil || wait.State != "resumed" && !terminalWait(wait)
}
func goalRefusedEntry(outcome string) bool { return outcome != "accepted" && outcome != "delivered" }

// goalAppend (M12): an entry past the bound pushes out the oldest refused entry, else the oldest, and is counted.
// Nothing is refused.
func goalAppend(goal *RunGoal, entry GoalEntry) {
	goal.Journal = append(goal.Journal, entry)
	if len(goal.Journal) <= GoalJournalLimit {
		return
	}
	at := slices.IndexFunc(goal.Journal, func(e GoalEntry) bool { return goalRefusedEntry(e.Outcome) })
	if at < 0 {
		at = 0
	}
	goal.Journal = slices.Delete(goal.Journal, at, at+1)
	discarded := int64(1)
	if goal.Discarded != nil {
		discarded += *goal.Discarded
	}
	goal.Discarded = &discarded
}

// GoalContext is what a goal transaction reads of the run, in the same transaction.
type GoalContext struct {
	Head  *RunHead
	Waits []RunWait
	Jobs  []RunJob
	Lease *RunLease
}

// GoalAnswer is an accepted command's answer. Lease (a pause or a resume): the run's lease as the store holds it,
// since a held lease runs on to its expiry and paused is not stopped.
type GoalAnswer struct {
	Goal  RunGoal
	Entry GoalEntry
	Lease *RunLease
}

// deepCloneGoal copies a record through its JSON even where its reader would refuse it (a broken store's).
func deepCloneGoal(goal RunGoal) RunGoal {
	raw, _ := json.Marshal(goal)
	var out RunGoal
	_ = json.Unmarshal(raw, &out)
	return out
}

// GoalTable holds the goal transitions in memory. MemoryRunStore calls them inside its own serialized
// transactions, with the run's head, waits, jobs and lease as they are in that transaction; a host store kind
// implements the same transitions.
type GoalTable struct {
	now        func() time.Time
	checkFence func(LeaseFence) error
	rows       map[string]*RunGoal
}

// NewGoalTable is a table over a store clock. It has no lease authority, so it refuses every fenced transition
// store-unsupported (#315), never decides one unchecked.
func NewGoalTable(now func() time.Time) *GoalTable { return NewFencedGoalTable(now, nil) }

// NewFencedGoalTable is a table over a store clock and the store's own lease authority (#315): check is called
// inside the command's transaction and refuses (a *LeaseRefused) unless the fence is current.
func NewFencedGoalTable(now func() time.Time, check func(LeaseFence) error) *GoalTable {
	if now == nil {
		now = time.Now
	}
	return &GoalTable{now: now, checkFence: check, rows: map[string]*RunGoal{}}
}
func (t *GoalTable) stamp() string { return t.now().UTC().Format("2006-01-02T15:04:05.000Z") }

// Read is the run's record, or nil.
func (t *GoalTable) Read(resource string) (*RunGoal, error) {
	if !validStorageID(resource) {
		return nil, goalInvalid()
	}
	row := t.rows[resource]
	if row == nil {
		return nil, nil
	}
	out := deepCloneGoal(*row)
	return &out, nil
}

// AdmitLease (M8): a paused objective's run is leased by no acquisition and no resume. A held lease runs on.
func (t *GoalTable) AdmitLease(resource string) error {
	if row := t.rows[resource]; row != nil && len(row.Objectives) > 0 && row.Objectives[len(row.Objectives)-1].Paused != nil {
		return goalRefused("objective-paused")
	}
	return nil
}

// AdmitUnfenced: an objective requires a leased run, chosen at create; from then on an unfenced commit of the run
// is refused.
func (t *GoalTable) AdmitUnfenced(resource string) error {
	if t.rows[resource] != nil {
		return goalRefused("lease-required")
	}
	return nil
}
func (t *GoalTable) answer(goal *RunGoal, entry GoalEntry, command GoalCommand, run GoalContext) GoalAnswer {
	out := GoalAnswer{Goal: deepCloneGoal(*goal), Entry: entry}
	if (command.Kind == "pause" || command.Kind == "resume") && run.Lease != nil {
		lease := *run.Lease
		out.Lease = &lease
	}
	return out
}

// Command decides one command against the record's revision and the run as context reads it, and journals it.
func (t *GoalTable) Command(input GoalTransition, context func(resource string) GoalContext) (GoalAnswer, error) {
	held, err := ReadGoalTransition(goalJSON(input))
	if err != nil {
		return GoalAnswer{}, err
	}
	c := held.Command
	// #315 L-3: a command a worker's view sent is decided only while that worker's fence is current for this run,
	// before anything is read or written: a stale holder is refused by the lease's own reason, and nothing is
	// journaled, not even a repeat's answer.
	if held.Fence != nil {
		if held.Fence.Resource != c.Resource {
			return GoalAnswer{}, &LeaseRefused{Reason: "resource-mismatch"}
		}
		if t.checkFence == nil {
			return GoalAnswer{}, &GoalCapabilityRefused{Reason: "store-unsupported"}
		}
		if err := t.checkFence(*held.Fence); err != nil {
			return GoalAnswer{}, err
		}
	}
	row := t.rows[c.Resource]
	digest, err := GoalCommandDigest(c)
	if err != nil {
		return GoalAnswer{}, err
	}
	// A repeat of a command id: the same command is answered as it was; another under that id is conflict.
	if row != nil {
		if at := slices.IndexFunc(row.Journal, func(e GoalEntry) bool { return e.Command == c.Command && e.Outcome != "delivered" }); at >= 0 {
			prior := row.Journal[at]
			if prior.Digest != digest {
				return GoalAnswer{}, goalRefused("conflict")
			}
			if prior.Outcome != "accepted" {
				return GoalAnswer{}, goalRefused(prior.Outcome)
			}
			return t.answer(row, prior, c, context(c.Resource)), nil
		}
	}
	at := t.stamp()
	var current *GoalObjective
	if row != nil {
		current = &row.Objectives[len(row.Objectives)-1]
	}
	by := GoalStamp{Command: c.Command, Actor: c.Actor, At: at}
	refuse := func(reason string, step *string) (GoalAnswer, error) {
		if row != nil {
			objective := ""
			if c.Kind == "create" {
				objective = *c.Objective
			} else {
				objective = current.ID
			}
			entry := GoalEntry{Command: c.Command, Kind: c.Kind, Actor: c.Actor, At: at, Objective: objective, Outcome: reason, Revision: row.Revision, Digest: digest}
			if step != nil {
				s := *step
				entry.Step = &s
			}
			goalAppend(row, entry)
		}
		return GoalAnswer{}, goalRefused(reason)
	}
	revision := int64(0)
	if row != nil {
		revision = row.Revision
	}
	if c.ExpectedRevision.Null != (row == nil) || row != nil && c.ExpectedRevision.Value != revision {
		return refuse("revision-changed", nil)
	}
	// H-1: an achievement's finding is the record's at the command's revision, or it is no finding of this record.
	if c.Kind == "achieve" && (held.Read == nil || c.ExpectedRevision.Null || *held.Read != c.ExpectedRevision.Value) {
		return refuse("revision-changed", nil)
	}
	run := context(c.Resource)
	headIs := func(head GoalHead) bool {
		return run.Head != nil && run.Head.Checkpoint != nil && run.Head.Revision == head.Revision && CheckpointBytesDigest(run.Head.Checkpoint) == head.Digest
	}
	waitOf := func(id string) *RunWait {
		for i := range run.Waits {
			if run.Waits[i].Wait == id {
				return &run.Waits[i]
			}
		}
		return nil
	}
	var next GoalObjective
	if c.Kind == "create" {
		if current != nil && !goalTerminal(current.State) {
			return refuse("objective-exists", nil)
		}
		if row != nil && slices.ContainsFunc(row.Objectives, func(o GoalObjective) bool { return o.ID == *c.Objective }) {
			return refuse("objective-reused", nil)
		}
		if row != nil && len(row.Objectives) >= GoalObjectiveLimit {
			return refuse("goals-exhausted", nil)
		}
		// #318 M-4: an abandon with no deliveries journaled is unfinished: its jobs may never have been asked to stop,
		// and once another objective exists its retry cannot finish it. The host retries the abandon first.
		if current != nil && current.State == "abandoned" && current.Abandoned != nil && !slices.ContainsFunc(row.Journal, func(entry GoalEntry) bool {
			return entry.Command == current.Abandoned.Command && entry.Outcome == "delivered"
		}) {
			return refuse("abandon-unfinished", nil)
		}
		// M10: a wait parked before the abandon, or after it (an abandon closes nothing), is resumed or ended before
		// another objective is created over a terminal one.
		if current != nil && current.Abandoned != nil && slices.ContainsFunc(current.Abandoned.Waits, func(left GoalLeftWait) bool { return goalUnresolvedWait(waitOf(left.Wait)) }) {
			return refuse("wait-unresolved", nil)
		}
		if current != nil && slices.ContainsFunc(run.Waits, func(wait RunWait) bool { return goalUnresolvedWait(&wait) }) {
			return refuse("wait-unresolved", nil)
		}
		next = GoalObjective{ID: *c.Objective, State: "active", Created: by}
	} else {
		if current == nil {
			return refuse("no-objective", nil)
		}
		if goalTerminal(current.State) {
			return refuse("objective-terminal", nil)
		}
		next = deepCloneObjective(*current)
		switch c.Kind {
		case "pause":
			if next.Paused != nil {
				return refuse("objective-paused", nil)
			}
			paused := by
			next.Paused = &paused
		case "resume":
			if next.Paused == nil {
				return refuse("not-paused", nil)
			}
			next.Paused = nil
		case "block":
			if next.State == "blocked" {
				return refuse("objective-blocked", nil)
			}
			next.State = "blocked"
			next.Blocked = &GoalBlock{Command: by.Command, Actor: by.Actor, At: by.At, Condition: *c.Condition}
		case "unblock":
			if next.State != "blocked" {
				return refuse("not-blocked", nil)
			}
			next.State = "active"
			next.Blocked = nil
		case "bind":
			if next.Plan != nil {
				return refuse("plan-bound", nil)
			}
			if !headIs(*c.Head) {
				return refuse("head-changed", nil)
			}
			if held.Refused != nil {
				return refuse(held.Refused.Reason, held.Refused.Step)
			}
			// M11: a Plan an earlier objective of this run bound is never bound again.
			if slices.ContainsFunc(row.Objectives, func(o GoalObjective) bool {
				return o.Plan != nil && o.Plan.ID == held.Plan.ID && o.Plan.Revision == held.Plan.Revision
			}) {
				return refuse("plan-reused", nil)
			}
			next.Plan = &GoalPlan{ID: held.Plan.ID, Revision: held.Plan.Revision, Steps: slices.Clone(held.Plan.Steps), Bound: by}
		case "achieve":
			if next.State == "blocked" {
				return refuse("objective-blocked", nil)
			}
			if next.Plan == nil {
				return refuse("plan-unbound", nil)
			}
			if !headIs(*c.Head) {
				return refuse("head-changed", nil)
			}
			if held.Refused != nil {
				return refuse(held.Refused.Reason, held.Refused.Step)
			}
			if slices.ContainsFunc(run.Waits, func(w RunWait) bool { return goalUnresolvedWait(&w) }) {
				return refuse("wait-pending", nil)
			}
			// Unsettled jobs block unless waived by id; settled ones that did not succeed are listed,
			// contradictions included.
			open := []string{}
			for _, job := range run.Jobs {
				if goalUnsettledJob(job) {
					open = append(open, job.Job)
				}
			}
			if slices.ContainsFunc(c.Waive, func(job string) bool { return !slices.Contains(open, job) }) {
				return refuse("waiver-invalid", nil)
			}
			if slices.ContainsFunc(open, func(job string) bool { return !slices.Contains(c.Waive, job) }) {
				return refuse("jobs-unsettled", nil)
			}
			done := GoalAchievement{Command: by.Command, Actor: by.Actor, At: by.At, Head: *c.Head}
			if len(c.Waive) > 0 {
				done.Waived = slices.Clone(c.Waive)
			}
			for _, job := range run.Jobs {
				if job.State == "not-started" || job.State == "completed" && job.Completion != nil && job.Completion.Outcome != "succeeded" {
					done.Unsuccessful = append(done.Unsuccessful, GoalJobOf(job))
				}
			}
			next.State = "achieved"
			next.Paused = nil
			next.Achieved = &done
		default:
			// The abandon closes nothing: what it left is recorded, so that a later create can be held to it (M10).
			left := GoalAbandonment{Command: by.Command, Actor: by.Actor, At: by.At, Waits: []GoalLeftWait{}, Jobs: []GoalJob{}}
			for i := range run.Waits {
				if goalUnresolvedWait(&run.Waits[i]) {
					left.Waits = append(left.Waits, GoalLeftWait{Wait: run.Waits[i].Wait, Reason: run.Waits[i].Reason, State: run.Waits[i].State})
				}
			}
			for _, job := range run.Jobs {
				if goalUnsettledJob(job) {
					left.Jobs = append(left.Jobs, GoalJobOf(job))
				}
			}
			next.State = "abandoned"
			next.Paused = nil
			next.Blocked = nil
			next.Abandoned = &left
		}
	}
	goal := row
	if goal == nil {
		goal = &RunGoal{Version: GoalRecordVersion, Kind: "goal", Resource: c.Resource, Objectives: []GoalObjective{}, Journal: []GoalEntry{}}
	}
	if c.Kind == "create" {
		goal.Objectives = append(goal.Objectives, next)
	} else {
		goal.Objectives[len(goal.Objectives)-1] = next
	}
	goal.Revision++
	entry := GoalEntry{Command: c.Command, Kind: c.Kind, Actor: c.Actor, At: at, Objective: next.ID, Outcome: "accepted", Revision: goal.Revision, Digest: digest}
	goalAppend(goal, entry)
	t.rows[c.Resource] = goal
	return t.answer(goal, entry, c, run), nil
}
func deepCloneObjective(o GoalObjective) GoalObjective {
	raw, _ := json.Marshal(o)
	var out GoalObjective
	_ = json.Unmarshal(raw, &out)
	return out
}

// Inspect (M12) is a read, coalesced into a count and a last row; it moves no revision. A repeat of the last id is
// not counted again.
func (t *GoalTable) Inspect(input GoalInspect) (*RunGoal, error) {
	held, err := ReadGoalInspect(goalJSON(input))
	if err != nil {
		return nil, err
	}
	row := t.rows[held.Resource]
	if row == nil {
		return nil, nil
	}
	if row.Inspections == nil || row.Inspections.Last.Command != held.Command {
		count := int64(1)
		if row.Inspections != nil {
			count += row.Inspections.Count
		}
		row.Inspections = &GoalInspections{Count: count, Last: contract.RunGoalInspectionsLast{Command: held.Command, Actor: held.Actor, At: t.stamp()}}
	}
	out := deepCloneGoal(*row)
	return &out, nil
}

// Deliver journals what became of each job after the abandon that abandoned the current objective: its
// deliveries live only in its answer, so each attempt of that command is journaled.
func (t *GoalTable) Deliver(input GoalDeliveries) (RunGoal, error) {
	held, err := ReadGoalDeliveries(goalJSON(input))
	if err != nil {
		return RunGoal{}, err
	}
	row := t.rows[held.Resource]
	if row == nil {
		return RunGoal{}, goalInvalid()
	}
	current := row.Objectives[len(row.Objectives)-1]
	if current.Abandoned == nil || current.Abandoned.Command != held.Command {
		return RunGoal{}, goalInvalid()
	}
	digest, err := GoalDeliveryDigest(held)
	if err != nil {
		return RunGoal{}, err
	}
	goalAppend(row, GoalEntry{Command: held.Command, Kind: "abandon", Actor: current.Abandoned.Actor, At: t.stamp(), Objective: current.ID, Outcome: "delivered", Revision: row.Revision, Digest: digest, Deliveries: slices.Clone(held.Deliveries)})
	return deepCloneGoal(*row), nil
}

// GoalStore is the goal port a store kind implements beside its lease, wait and job ports. Every transition is
// atomic with the store's commits and checked against its own clock, and the record survives the store's
// restarts. CommandGoal decides one command in one transaction against the record's revision, the run's head,
// waits, jobs and lease as they are in it, and journals it, accepted or refused; a repeat of a command id answers
// as the first did, with the record as it is now, while its entry is in the journal, and another command under it
// is conflict. An achievement's finding is taken only from the record at the command's revision (Read). The store
// trusts the transition it is handed: a direct caller of CommandGoal can claim any Read and any finding, so only the
// package's RunGoalCommand composes them from what it read (#318 L-11). A transition that carries a fence (#315) is
// decided only while that fence is current for the command's run, checked against the store's leases in the same
// transaction and before anything else: one of another run refuses *LeaseRefused resource-mismatch, and one that is
// not current the lease's own reason (no-lease, stale-epoch, other-owner, principal-changed, released, expired),
// with nothing journaled and nothing changed. A transition with no fence is the host's, decided as before. While
// the current objective is paused,
// AcquireLease and ResumeWait refuse objective-paused; once the run has a goal record an unfenced commit refuses
// lease-required. The package checks none of this in a host store; CheckRunStoreGoalConformance
// (run_goal_conformance.go) does.
type GoalStore interface {
	GoalVersion() int
	// GoalLifetime is durable when the record survives the store's process, process when it ends with it.
	GoalLifetime() string
	// CommandGoal answers an accepted command with the record and its entry. Refused: GoalRefused, journaled where
	// the run has a goal record; *LeaseRefused where the transition's fence is not current for its run, journaled
	// nowhere (#315).
	CommandGoal(ctx context.Context, input GoalTransition) (GoalAnswer, error)
	// InspectGoal is the record, counting the inspection; nil, and nothing written, where the run has none.
	InspectGoal(ctx context.Context, input GoalInspect) (*RunGoal, error)
	// RecordGoalDeliveries journals an abandon's deliveries on the objective that command abandoned.
	RecordGoalDeliveries(ctx context.Context, input GoalDeliveries) (RunGoal, error)
	// ReadGoal is a plain read: no count.
	ReadGoal(ctx context.Context, resource string) (*RunGoal, error)
}

// GoalRunStore is a job run store with the goal port.
type GoalRunStore interface {
	JobRunStore
	GoalStore
}

func (m *MemoryRunStore) GoalVersion() int     { return GoalRecordVersion }
func (m *MemoryRunStore) GoalLifetime() string { return GoalLifetimeProcess }

// goalContext is what a goal transaction reads of the run, under the store lock.
func (m *MemoryRunStore) goalContext(resource string) GoalContext {
	run := GoalContext{Waits: []RunWait{}, Jobs: []RunJob{}}
	if held, ok := m.runs[resource]; ok {
		head := held.head
		run.Head = &head
	}
	for _, row := range m.waits.rows[resource] {
		run.Waits = append(run.Waits, *row)
	}
	for _, row := range m.jobs.rows[resource] {
		run.Jobs = append(run.Jobs, *row)
	}
	if lease, err := m.leases.Read(resource); err == nil && lease != nil {
		run.Lease = lease
	}
	return run
}

// CommandGoal (#230): a goal command reads the run's head, waits, jobs and lease in its own transaction.
func (m *MemoryRunStore) CommandGoal(ctx context.Context, input GoalTransition) (answer GoalAnswer, err error) {
	err = m.leaseTransition(ctx, func() error { answer, err = m.goals.Command(input, m.goalContext); return err })
	return answer, err
}
func (m *MemoryRunStore) InspectGoal(ctx context.Context, input GoalInspect) (held *RunGoal, err error) {
	err = m.leaseTransition(ctx, func() error { held, err = m.goals.Inspect(input); return err })
	return held, err
}
func (m *MemoryRunStore) RecordGoalDeliveries(ctx context.Context, input GoalDeliveries) (held RunGoal, err error) {
	err = m.leaseTransition(ctx, func() error { held, err = m.goals.Deliver(input); return err })
	return held, err
}
func (m *MemoryRunStore) ReadGoal(ctx context.Context, resource string) (held *RunGoal, err error) {
	err = m.leaseTransition(ctx, func() error { held, err = m.goals.Read(resource); return err })
	return held, err
}

// GoalReportOf is the goal report of one run, read from its record, never from a prompt.
func GoalReportOf(source RunGoal) (GoalReport, error) {
	held, err := ReadRunGoal(goalJSON(source))
	if err != nil {
		return GoalReport{}, err
	}
	report := GoalReport{Version: GoalRecordVersion, Resource: held.Resource, Revision: held.Revision, Objectives: held.Objectives, Entries: []GoalReportEntry{}, Inspections: held.Inspections}
	if held.Discarded != nil {
		report.Discarded = *held.Discarded
	}
	for _, e := range held.Journal {
		report.Entries = append(report.Entries, GoalReportEntry{Command: e.Command, Kind: e.Kind, Actor: e.Actor, At: e.At, Objective: e.Objective, Outcome: e.Outcome, Revision: e.Revision, Step: e.Step, Deliveries: e.Deliveries})
	}
	return report, nil
}

// ReadGoalReport reads a goal report.
func ReadGoalReport(raw []byte) (GoalReport, error) {
	g, err := goalShape(raw, []string{"version", "resource", "revision", "objectives", "entries", "discarded"}, []string{"inspections"})
	if err != nil {
		return GoalReport{}, err
	}
	if g.version() != nil {
		return GoalReport{}, goalInvalid()
	}
	report := GoalReport{Version: GoalRecordVersion, Entries: []GoalReportEntry{}}
	if report.Resource, err = g.id("resource"); err != nil {
		return GoalReport{}, err
	}
	if report.Revision, err = g.count("revision", 1); err != nil {
		return GoalReport{}, err
	}
	objectives, err := g.list("objectives", 1, GoalObjectiveLimit)
	if err != nil {
		return GoalReport{}, err
	}
	if report.Objectives, err = readGoalObjectives(objectives); err != nil {
		return GoalReport{}, err
	}
	entries, err := g.list("entries", 0, GoalJournalLimit)
	if err != nil {
		return GoalReport{}, err
	}
	for _, item := range entries {
		e, err := readGoalEntry(item, true)
		if err != nil {
			return GoalReport{}, err
		}
		report.Entries = append(report.Entries, GoalReportEntry{Command: e.Command, Kind: e.Kind, Actor: e.Actor, At: e.At, Objective: e.Objective, Outcome: e.Outcome, Revision: e.Revision, Step: e.Step, Deliveries: e.Deliveries})
	}
	if report.Discarded, err = g.count("discarded", 0); err != nil {
		return GoalReport{}, err
	}
	if g.has("inspections") {
		if report.Inspections, err = readGoalInspections(g.m["inspections"]); err != nil {
			return GoalReport{}, err
		}
	}
	return report, nil
}
