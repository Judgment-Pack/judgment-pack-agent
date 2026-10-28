package runcontext

import (
	"fmt"
	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
	"strconv"
	"strings"
)

const PlanHistoryLimit = 32
const PlanDiffLimit = 40
const PlanStepLimit = 20

func planStepID(v *Value) bool {
	if !v.IsString() || !strings.HasPrefix(planText(v), "step-") {
		return false
	}
	s := strings.TrimPrefix(planText(v), "step-")
	if len(s) == 0 || len(s) > 16 || s[0] == '0' {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	return err == nil && n >= 1 && n <= 9007199254740991
}
func planDigest(plan *Value) string { return "sha256:" + ContextDigest(CanonicalJSON(plan)) }
func identifyPlan(plan, state *Value) (*Value, *Value) {
	if state != nil {
		return plan.Clone(), state.Clone()
	}
	steps := orArray(plan).Clone()
	for i, step := range steps.Items() {
		step.Set("id", String(fmt.Sprintf("step-%d", i+1)))
	}
	return steps, ObjectOf("revision", String(planDigest(steps)), "sequence", Number(0), "nextId", Number(float64(steps.Len()+1)), "discarded", Number(0), "history", Array())
}
func planIdentityRefused() error {
	return failure.ToolInput("PlanIdentityInvalid: use each current step id once, or omit id for a new step; the plan was left untouched.")
}

// revisePlan constructs a detached candidate. Its caller publishes checkpoint and event together.
func revisePlan(current, state, incoming *Value, explanation string, closure ...bool) (*Value, *Value, error) {
	hadSchedule := state.Has("taskSchedule")
	detached := Object()
	for _, key := range state.Keys() {
		if key != "taskSchedule" {
			detached.Set(key, state.Get(key).Clone())
		}
	}
	state = detached
	next := planNumber(state.Get("nextId"))
	if planNumber(state.Get("sequence")) >= 9007199254740991 || next > 9007199254740991-float64(newPlanIDs(incoming)) {
		return nil, nil, planIdentityRefused()
	}
	old := map[string]*Value{}
	for _, s := range current.Items() {
		old[planText(s.Get("id"))] = s
	}
	seen := map[string]bool{}
	plan := Array()
	for _, input := range incoming.Items() {
		id := planText(input.Get("id"))
		if input.Has("id") && (old[id] == nil || seen[id]) {
			return nil, nil, planIdentityRefused()
		}
		prior := old[id]
		terminal := func(v *Value) bool {
			return v != nil && (planText(v.Get("status")) == "completed" || planText(v.Get("status")) == "not-needed")
		}
		if (len(closure) == 0 || !closure[0]) && (terminal(input) || terminal(prior)) && (prior == nil || planText(input.Get("status")) != planText(prior.Get("status")) || planText(input.Get("step")) != planText(prior.Get("step")) || input.Has("detail") != prior.Has("detail") || planText(input.Get("detail")) != planText(prior.Get("detail"))) {
			return nil, nil, failure.ToolInput("PlanCompletionActionRequired: terminal work is closed with complete_plan_step; the plan was left untouched.")
		}
		if !input.Has("id") {
			id = fmt.Sprintf("step-%.0f", next)
			next++
		}
		seen[id] = true
		step := Object()
		if old[id] != nil {
			for _, key := range old[id].Keys() {
				if key != "detail" {
					step.Set(key, old[id].Get(key).Clone())
				}
			}
		}
		for _, key := range input.Keys() {
			step.Set(key, input.Get(key).Clone())
		}
		step.Set("id", String(id))
		plan.Append(step)
	}
	configured := hadSchedule
	for _, step := range plan.Items() {
		configured = configured || taskConfigured(step)
	}
	if configured {
		if err := validateTaskPlan(plan, nil); err != nil {
			return nil, nil, err
		}
	}
	diff := Array()
	for i, s := range current.Items() {
		id := planText(s.Get("id"))
		to := -1
		var after *Value
		for j, candidate := range plan.Items() {
			if planText(candidate.Get("id")) == id {
				to = j
				after = candidate
				break
			}
		}
		if to < 0 {
			diff.Append(ObjectOf("id", String(id), "kind", String("removed"), "before", s.Clone(), "from", Number(float64(i))))
			continue
		}
		if planText(s.Get("step")) != planText(after.Get("step")) || s.Has("detail") != after.Has("detail") || planText(s.Get("detail")) != planText(after.Get("detail")) || taskConfigurationJSON(s) != taskConfigurationJSON(after) {
			diff.Append(ObjectOf("id", String(id), "kind", String("changed"), "before", s.Clone(), "after", after.Clone()))
		}
		if planText(s.Get("status")) != planText(after.Get("status")) {
			diff.Append(ObjectOf("id", String(id), "kind", String("status"), "before", s.Clone(), "after", after.Clone()))
		}
		if i != to {
			diff.Append(ObjectOf("id", String(id), "kind", String("reordered"), "from", Number(float64(i)), "to", Number(float64(to))))
		}
	}
	for to, s := range plan.Items() {
		id := planText(s.Get("id"))
		if old[id] == nil {
			diff.Append(ObjectOf("id", String(id), "kind", String("added"), "after", s.Clone(), "to", Number(float64(to))))
		}
	}
	if diff.Len() > PlanDiffLimit {
		return nil, nil, failure.ToolInput("PlanRevisionTooLarge: a plan revision may record at most forty changes; the plan was left untouched.")
	}
	sequence := planNumber(state.Get("sequence")) + 1
	revision := planDigest(plan)
	history := state.Get("history").Clone()
	history.Append(ObjectOf("sequence", Number(sequence), "previous", state.Get("revision").Clone(), "revision", String(revision), "explanation", String(explanation), "diff", diff))
	discarded := planNumber(state.Get("discarded"))
	if history.Len() > PlanHistoryLimit {
		rows := history.Items()
		history = Array(rows[1:]...)
		discarded++
	}
	state.Set("revision", String(revision))
	state.Set("sequence", Number(sequence))
	state.Set("nextId", Number(next))
	state.Set("discarded", Number(discarded))
	state.Set("history", history)
	return plan, state, nil
}
func validPlanState(plan, state *Value) bool {
	if state == nil {
		for _, s := range plan.Items() {
			if s.Has("id") || s.Has("origin") || s.Has("evidence") || s.Has("notNeededReason") || planText(s.Get("status")) == "not-needed" {
				return false
			}
		}
		return true
	}
	if !state.IsObject() || !validPlanExtensions(plan, state) || !isCount(state.Get("sequence"), 9007199254740991) || !isCount(state.Get("discarded"), 9007199254740991) || !isCount(state.Get("nextId"), 9007199254740991) || planNumber(state.Get("nextId")) < 1 || planText(state.Get("revision")) != planDigest(plan) {
		return false
	}
	if state.Has("taskCoordinator") {
		if _, err := readTaskCoordinatorBinding(state.Get("taskCoordinator")); err != nil {
			return false
		}
	}
	if state.Has("taskEnvelope") {
		if _, err := readTaskEnvelopeBinding(state.Get("taskEnvelope")); err != nil {
			return false
		}
	}
	configured := state.Has("taskSchedule")
	for _, step := range plan.Items() {
		configured = configured || taskConfigured(step)
	}
	if configured {
		if validateTaskPlan(plan, state.Get("taskSchedule")) != nil {
			return false
		}
		if state.Has("taskSchedule") && planText(state.Get("taskSchedule").Get("binding").Get("planRevision")) != planText(state.Get("revision")) {
			return false
		}
	}
	sequence, discarded := planNumber(state.Get("sequence")), planNumber(state.Get("discarded"))
	history := state.Get("history")
	if !history.IsArray() || history.Len() > PlanHistoryLimit || discarded+float64(history.Len()) != sequence || float64(history.Len()) != min(sequence, PlanHistoryLimit) {
		return false
	}
	seen := map[string]bool{}
	for _, s := range plan.Items() {
		id := s.Get("id")
		if !planStepID(id) || seen[planText(id)] {
			return false
		}
		n, _ := strconv.ParseInt(strings.TrimPrefix(planText(id), "step-"), 10, 64)
		if float64(n) >= planNumber(state.Get("nextId")) {
			return false
		}
		seen[planText(id)] = true
	}
	previous := ""
	for i, row := range history.Items() {
		if !row.IsObject() || planNumber(row.Get("sequence")) != discarded+float64(i)+1 || !planRevisionDigest(row.Get("previous")) || !planRevisionDigest(row.Get("revision")) || previous != "" && planText(row.Get("previous")) != previous || !isText(row.Get("explanation"), 2000) || !row.Get("diff").IsArray() || row.Get("diff").Len() > PlanDiffLimit {
			return false
		}
		previous = planText(row.Get("revision"))
		for _, d := range row.Get("diff").Items() {
			if !validPlanChange(d, planNumber(state.Get("nextId")), state.Get("accepted")) {
				return false
			}
		}

	}
	return history.Len() == 0 || previous == planText(state.Get("revision"))
}

func planRevisionDigest(v *Value) bool {
	return v.IsString() && strings.HasPrefix(planText(v), "sha256:") && digest64.MatchString(strings.TrimPrefix(planText(v), "sha256:"))
}

func validPlanChange(d *Value, nextID float64, accepted *Value) bool {
	if !d.IsObject() || !planStepID(d.Get("id")) {
		return false
	}
	id := planText(d.Get("id"))
	n, _ := strconv.ParseInt(strings.TrimPrefix(id, "step-"), 10, 64)
	if float64(n) >= nextID {
		return false
	}
	for _, key := range []string{"before", "after"} {
		if s := d.Get(key); s != nil {
			if o := s.Get("origin"); o != nil && (accepted == nil || planText(o.Get("plan")) != planText(accepted.Get("id")) || planText(o.Get("revision")) != planText(accepted.Get("revision")) || planNumber(o.Get("index")) >= float64(accepted.Get("steps").Len())) {
				return false
			}
			if !ValidTaskSteps(Array(s)) || planText(s.Get("id")) != id {
				return false
			}
		}
	}
	switch planText(d.Get("kind")) {
	case "added":
		return d.Has("after") && !d.Has("before") && isCount(d.Get("to"), 19) && !d.Has("from")
	case "removed":
		return d.Has("before") && !d.Has("after") && isCount(d.Get("from"), 19) && !d.Has("to")
	case "reordered":
		return isCount(d.Get("from"), 19) && isCount(d.Get("to"), 19) && !d.Has("before") && !d.Has("after") && planNumber(d.Get("from")) != planNumber(d.Get("to"))
	case "changed", "status":
		return d.Has("before") && d.Has("after") && !d.Has("from") && !d.Has("to")
	default:
		return false
	}
}

func planText(v *Value) string {
	if v == nil {
		return ""
	}
	return v.Str()
}
func planNumber(v *Value) float64 {
	if v == nil {
		return 0
	}
	return v.Num()
}

func newPlanIDs(incoming *Value) int {
	count := 0
	for _, s := range incoming.Items() {
		if !s.Has("id") {
			count++
		}
	}
	return count
}
