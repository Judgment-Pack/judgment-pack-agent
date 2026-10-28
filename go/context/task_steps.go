package runcontext

import (
	"bytes"
	"encoding/json"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"slices"
	"unicode/utf8"
)

// Detached candidates only. These functions issue no physical dispatch authority.
type TaskStepShare struct {
	Requests int `json:"requests"`
}
type TaskStepDefinition struct {
	ID         string                   `json:"id"`
	DependsOn  []string                 `json:"dependsOn,omitempty"`
	Budget     *TaskStepShare           `json:"budget,omitempty"`
	Delegation *contract.SubagentRecord `json:"delegation,omitempty"`
}
type TaskScheduleBinding struct {
	Run          string `json:"run"`
	Execution    string `json:"execution"`
	PlanRevision string `json:"planRevision"`
	ScopeDigest  string `json:"scopeDigest"`
	Unit         string `json:"unit"`
}
type TaskStepClaim struct {
	ID            string `json:"id"`
	RequestDigest string `json:"requestDigest"`
	State         string `json:"state"`
}
type TaskChildResult struct {
	CheckpointDigest string `json:"checkpointDigest"`
	ResultDigest     string `json:"resultDigest"`
	EventDigest      string `json:"eventDigest"`
	CommitID         string `json:"commitId"`
}
type TaskCompletionEvidence struct {
	Step         string `json:"step"`
	Task         string `json:"task"`
	PlanRevision string `json:"planRevision"`
	TaskChildResult
}
type TaskChildBinding struct {
	Task         string                  `json:"task"`
	Execution    string                  `json:"execution"`
	PlanRevision string                  `json:"planRevision"`
	Profile      contract.SubagentRecord `json:"profile"`
	Result       *TaskChildResult        `json:"result,omitempty"`
}
type TaskStepRow struct {
	ID     string            `json:"id"`
	Limit  *int              `json:"limit,omitempty"`
	Claims []TaskStepClaim   `json:"claims"`
	Child  *TaskChildBinding `json:"child,omitempty"`
}
type TaskPendingDelegation struct {
	PlanRevision  string   `json:"planRevision"`
	RequestDigest string   `json:"requestDigest"`
	Steps         []string `json:"steps"`
}
type TaskSchedule struct {
	Pending          *TaskPendingDelegation `json:"pending,omitempty"`
	Version          int                    `json:"version"`
	Binding          TaskScheduleBinding    `json:"binding"`
	DefinitionDigest string                 `json:"definitionDigest"`
	Rows             []TaskStepRow          `json:"rows"`
}
type TaskStepRefused struct{ Reason string }

func (e *TaskStepRefused) Error() string { return "TaskStepRefused: " + e.Reason }
func taskStepFail(reason string) error   { return &TaskStepRefused{reason} }
func taskSafeID(s string) bool {
	if s == "" || U16Length(s) > 100 || !utf8.ValidString(s) {
		return false
	}
	for _, c := range s {
		if c < 32 {
			return false
		}
	}
	return true
}
func taskBounded(v any) bool                                 { b, e := json.Marshal(v); return e == nil && len(b) <= 65536 }
func taskDefinitionDigest(steps []TaskStepDefinition) string { return RouteDigest(steps) }
func ValidateTaskGraph(steps []TaskStepDefinition) error {
	if len(steps) < 1 || len(steps) > 20 {
		return taskStepFail("invalid-steps")
	}
	ids := map[string]bool{}
	for _, s := range steps {
		if !planStepID(String(s.ID)) || ids[s.ID] {
			return taskStepFail("invalid-step-id")
		}
		ids[s.ID] = true
		if len(s.DependsOn) > 19 {
			return taskStepFail("invalid-dependencies")
		}
		seen := map[string]bool{}
		for _, d := range s.DependsOn {
			if !planStepID(String(d)) || seen[d] {
				return taskStepFail("invalid-dependencies")
			}
			seen[d] = true
		}
		if s.Budget != nil && (s.Budget.Requests < 1 || s.Budget.Requests > 100) {
			return taskStepFail("invalid-share")
		}
		if s.Delegation != nil {
			b, _ := json.Marshal(s.Delegation)
			v, e := ParseJSON(b)
			if e != nil || !ValidSubagentRecord(v) {
				return taskStepFail("invalid-profile-pin")
			}
		}
	}
	graph := map[string][]string{}
	for _, s := range steps {
		graph[s.ID] = s.DependsOn
	}
	active, done := map[string]bool{}, map[string]bool{}
	var visit func(string) error
	visit = func(name string) error {
		if active[name] {
			return taskStepFail("dependency-cycle")
		}
		if done[name] {
			return nil
		}
		active[name] = true
		for _, d := range graph[name] {
			if d == name || !ids[d] {
				return taskStepFail("unknown-dependency")
			}
			if e := visit(d); e != nil {
				return e
			}
		}
		delete(active, name)
		done[name] = true
		return nil
	}
	for _, s := range steps {
		if e := visit(s.ID); e != nil {
			return e
		}
	}
	if !taskBounded(steps) {
		return taskStepFail("capacity")
	}
	return nil
}
func ReadyTaskSteps(steps []TaskStepDefinition, statuses map[string]string) ([]string, error) {
	if e := ValidateTaskGraph(steps); e != nil {
		return nil, e
	}
	if len(statuses) != len(steps) {
		return nil, taskStepFail("invalid-statuses")
	}
	out := []string{}
	for _, s := range steps {
		if !slices.Contains([]string{"pending", "in_progress", "completed", "not-needed"}, statuses[s.ID]) {
			return nil, taskStepFail("invalid-statuses")
		}
		ready := statuses[s.ID] == "pending"
		for _, d := range s.DependsOn {
			ready = ready && (statuses[d] == "completed" || statuses[d] == "not-needed")
		}
		if ready {
			out = append(out, s.ID)
		}
	}
	return out, nil
}
func validTaskBinding(b TaskScheduleBinding) bool {
	return taskSafeID(b.Run) && taskSafeID(b.Execution) && recordDigestPattern.MatchString(b.PlanRevision) && recordDigestPattern.MatchString(b.ScopeDigest) && (b.Unit == "model-requests" || b.Unit == "provider-managed")
}

// ReadTaskSchedule retains held/unknown claims; it never recreates an admission grant.
func ReadTaskSchedule(value any, binding TaskScheduleBinding, steps []TaskStepDefinition) (TaskSchedule, error) {
	var out TaskSchedule
	if e := ValidateTaskGraph(steps); e != nil {
		return out, e
	}
	raw, e := json.Marshal(value)
	if e != nil || len(raw) > 65536 {
		return out, taskStepFail("identity-changed")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&out) != nil || !validTaskBinding(binding) || out.Version != 1 || out.Binding != binding || out.DefinitionDigest != taskDefinitionDigest(steps) || out.Rows == nil || len(out.Rows) != len(steps) {
		return out, taskStepFail("identity-changed")
	}
	seen := map[string]bool{}
	children := map[string]bool{}
	size := 0
	for i, r := range out.Rows {
		s := steps[i]
		if r.ID != s.ID || r.Claims == nil || len(r.Claims) > 100 || (r.Limit == nil) != (s.Budget == nil) || r.Limit != nil && *r.Limit != s.Budget.Requests {
			return out, taskStepFail("invalid-reservation")
		}
		for _, c := range r.Claims {
			if !taskSafeID(c.ID) || seen[c.ID] || !recordDigestPattern.MatchString(c.RequestDigest) || !slices.Contains([]string{"held", "admitted", "unknown"}, c.State) {
				return out, taskStepFail("invalid-reservation")
			}
			seen[c.ID] = true
			size++
		}
		if r.Limit != nil && len(r.Claims) > *r.Limit {
			return out, taskStepFail("share-exceeded")
		}
		if child := r.Child; child != nil {
			raw, _ := json.Marshal(child.Profile)
			pin, e := ParseJSON(raw)
			if !taskSafeID(child.Task) || child.Execution != child.Task || children[child.Task] || !recordDigestPattern.MatchString(child.PlanRevision) || e != nil || !ValidSubagentRecord(pin) || s.Delegation == nil || usageCanonical(child.Profile) != usageCanonical(s.Delegation) {
				return out, taskStepFail("invalid-child-binding")
			}
			children[child.Task] = true
			if result := child.Result; result != nil && (!recordDigestPattern.MatchString(result.CheckpointDigest) || !recordDigestPattern.MatchString(result.ResultDigest) || !recordDigestPattern.MatchString(result.EventDigest) || !taskSafeID(result.CommitID)) {
				return out, taskStepFail("invalid-child-evidence")
			}
		}
	}
	if pending := out.Pending; pending != nil {
		if pending.PlanRevision != binding.PlanRevision || !recordDigestPattern.MatchString(pending.RequestDigest) || len(pending.Steps) < 1 || len(pending.Steps) > 3 {
			return out, taskStepFail("invalid-pending-delegation")
		}
		seen := map[string]bool{}
		for _, name := range pending.Steps {
			at := slices.IndexFunc(steps, func(step TaskStepDefinition) bool { return step.ID == name })
			if seen[name] || at < 0 || steps[at].Delegation == nil || out.Rows[at].Child != nil {
				return out, taskStepFail("invalid-pending-delegation")
			}
			seen[name] = true
		}
	}
	if size > 400 {
		return out, taskStepFail("capacity")
	}
	return out, nil
}
func CreateTaskSchedule(binding TaskScheduleBinding, steps []TaskStepDefinition, available int) (TaskSchedule, error) {
	out := TaskSchedule{}
	if e := ValidateTaskGraph(steps); e != nil {
		return out, e
	}
	if !validTaskBinding(binding) || available < 0 || available > 100 {
		return out, taskStepFail("invalid-allowance")
	}
	shares := 0
	for _, s := range steps {
		if s.Budget != nil {
			shares += s.Budget.Requests
		}
	}
	if shares > available {
		return out, taskStepFail("shares-exceed-allowance")
	}
	out = TaskSchedule{Version: 1, Binding: binding, DefinitionDigest: taskDefinitionDigest(steps), Rows: []TaskStepRow{}}
	for _, s := range steps {
		r := TaskStepRow{ID: s.ID, Claims: []TaskStepClaim{}}
		if s.Budget != nil {
			n := s.Budget.Requests
			r.Limit = &n
		}
		out.Rows = append(out.Rows, r)
	}
	return out, nil
}

// ReserveTaskStep must run in the original owner transaction before root/170 admission.
func ReserveTaskStep(current TaskSchedule, steps []TaskStepDefinition, statuses map[string]string, step, id, requestDigest string, available int) (TaskSchedule, error) {
	return reserveTaskStepCandidate(current, steps, statuses, step, id, requestDigest, &available)
}
func reserveTaskStepCandidate(current TaskSchedule, steps []TaskStepDefinition, statuses map[string]string, step, id, requestDigest string, available *int) (TaskSchedule, error) {
	next, e := ReadTaskSchedule(current, current.Binding, steps)
	if e != nil {
		return next, e
	}
	if !taskSafeID(id) || !recordDigestPattern.MatchString(requestDigest) || available != nil && (*available < 0 || *available > 100) {
		return next, taskStepFail("invalid-reservation")
	}
	if available == nil {
		if current.Binding.Unit != "provider-managed" {
			return next, taskStepFail("invalid-reservation")
		}
		for _, definition := range steps {
			if definition.Budget != nil {
				return next, taskStepFail("native-envelope-required")
			}
		}
	}
	at := -1
	others, size := 0, 0
	for i, r := range next.Rows {
		if r.ID == step {
			at = i
		} else if r.Limit != nil && statuses[r.ID] != "completed" && statuses[r.ID] != "not-needed" {
			others += max(0, *r.Limit-len(r.Claims))
		}
		for _, c := range r.Claims {
			if c.ID == id {
				return next, taskStepFail("reservation-already-held")
			}
			size++
		}
	}
	if at < 0 {
		return next, taskStepFail("unknown-step")
	}
	if _, e := ReadyTaskSteps(steps, statuses); e != nil {
		return next, e
	}
	if statuses[step] != "pending" && statuses[step] != "in_progress" {
		return next, taskStepFail("dependencies-unready")
	}
	for _, d := range steps[at].DependsOn {
		if statuses[d] != "completed" && statuses[d] != "not-needed" {
			return next, taskStepFail("dependencies-unready")
		}
	}
	row := &next.Rows[at]
	if available != nil && *available <= others || row.Limit != nil && len(row.Claims) >= *row.Limit {
		return next, taskStepFail("step-budget-pause")
	}
	row.Claims = append(row.Claims, TaskStepClaim{id, requestDigest, "held"})
	if size >= 400 || !taskBounded(next) {
		return next, taskStepFail("capacity")
	}
	return next, nil
}

// RequireTaskProfiles compares the actual admitted pin, with no name-only fallback.
func RequireTaskProfiles(steps []TaskStepDefinition, admitted []*contract.SubagentRecord) error {
	if e := ValidateTaskGraph(steps); e != nil {
		return e
	}
	for _, s := range steps {
		if s.Delegation == nil {
			continue
		}
		want, _ := json.Marshal(s.Delegation)
		matched := false
		for _, pin := range admitted {
			if pin == nil {
				continue
			}
			raw, _ := json.Marshal(pin)
			a, _ := ParseJSON(want)
			b, e := ParseJSON(raw)
			if e == nil && ValidSubagentRecord(b) && CanonicalJSON(a) == CanonicalJSON(b) {
				matched = true
				break
			}
		}
		if !matched {
			return taskStepFail("profile-not-admitted")
		}
	}
	return nil
}

// ReconcileTaskSchedule preserves held facts across an owner-accepted revision, never grants work.
func ReconcileTaskSchedule(current *TaskSchedule, oldSteps []TaskStepDefinition, oldStatuses map[string]string, binding TaskScheduleBinding, steps []TaskStepDefinition, statuses map[string]string, available int) (TaskSchedule, error) {
	var out TaskSchedule
	if e := ValidateTaskGraph(steps); e != nil {
		return out, e
	}
	if _, e := ReadyTaskSteps(steps, statuses); e != nil {
		return out, e
	}
	if current == nil {
		return CreateTaskSchedule(binding, steps, available)
	}
	saved, e := ReadTaskSchedule(*current, current.Binding, oldSteps)
	if e != nil {
		return out, e
	}
	if _, e = ReadyTaskSteps(oldSteps, oldStatuses); e != nil {
		return out, e
	}
	if !validTaskBinding(binding) || binding.Run != saved.Binding.Run || binding.Execution != saved.Binding.Execution || binding.ScopeDigest != saved.Binding.ScopeDigest || binding.Unit != saved.Binding.Unit || available < 0 || available > 100 {
		return out, taskStepFail("identity-changed")
	}
	for _, row := range saved.Rows {
		before := oldSteps[slices.IndexFunc(oldSteps, func(s TaskStepDefinition) bool { return s.ID == row.ID })]
		at := slices.IndexFunc(steps, func(s TaskStepDefinition) bool { return s.ID == row.ID })
		if (len(row.Claims) > 0 || row.Child != nil || oldStatuses[row.ID] == "in_progress") && (at < 0 || usageCanonical(before) != usageCanonical(steps[at])) {
			return out, taskStepFail("active-definition-changed")
		}
	}
	out = TaskSchedule{Version: 1, Binding: binding, DefinitionDigest: taskDefinitionDigest(steps), Rows: []TaskStepRow{}}
	held := 0
	for _, step := range steps {
		row := TaskStepRow{ID: step.ID, Claims: []TaskStepClaim{}}
		if step.Budget != nil {
			limit := step.Budget.Requests
			row.Limit = &limit
		}
		for _, prior := range saved.Rows {
			if prior.ID == step.ID {
				row.Claims = prior.Claims
				row.Child = prior.Child
				break
			}
		}
		out.Rows = append(out.Rows, row)
		if statuses[row.ID] != "completed" && statuses[row.ID] != "not-needed" && row.Limit != nil {
			held += max(0, *row.Limit-len(row.Claims))
		}
	}
	if saved.Pending != nil {
		if binding.PlanRevision != saved.Binding.PlanRevision {
			return out, taskStepFail("delegation-pending")
		}
		out.Pending = saved.Pending
	}
	if held > available {
		return out, taskStepFail("shares-exceed-allowance")
	}
	return ReadTaskSchedule(out, binding, steps)
}

type TaskChildCandidate struct {
	Step, Task string
	Profile    contract.SubagentRecord
}

// BindTaskChildren validates every association before deriving a detached candidate.
func BindTaskChildren(current TaskSchedule, steps []TaskStepDefinition, statuses map[string]string, children []TaskChildCandidate) (TaskSchedule, error) {
	next, e := ReadTaskSchedule(current, current.Binding, steps)
	if e != nil {
		return next, e
	}
	ready, e := ReadyTaskSteps(steps, statuses)
	if e != nil {
		return next, e
	}
	if len(children) < 1 || len(children) > 20 {
		return next, taskStepFail("invalid-child-set")
	}
	seenSteps, seenTasks := map[string]bool{}, map[string]bool{}
	for _, child := range children {
		if seenSteps[child.Step] || seenTasks[child.Task] {
			return next, taskStepFail("invalid-child-set")
		}
		seenSteps[child.Step] = true
		seenTasks[child.Task] = true
		at := slices.IndexFunc(steps, func(s TaskStepDefinition) bool { return s.ID == child.Step })
		raw, _ := json.Marshal(child.Profile)
		pin, err := ParseJSON(raw)
		if at < 0 || !slices.Contains(ready, child.Step) || next.Rows[at].Child != nil || len(next.Rows[at].Claims) > 0 || !taskSafeID(child.Task) || err != nil || !ValidSubagentRecord(pin) || steps[at].Delegation == nil || usageCanonical(steps[at].Delegation) != usageCanonical(child.Profile) || slices.ContainsFunc(next.Rows, func(r TaskStepRow) bool { return r.Child != nil && r.Child.Task == child.Task }) {
			return next, taskStepFail("child-not-admitted")
		}
	}
	for _, child := range children {
		at := slices.IndexFunc(next.Rows, func(r TaskStepRow) bool { return r.ID == child.Step })
		next.Rows[at].Child = &TaskChildBinding{Task: child.Task, Execution: child.Task, PlanRevision: current.Binding.PlanRevision, Profile: child.Profile}
	}
	return ReadTaskSchedule(next, current.Binding, steps)
}

// TaskDefinitions projects only configured fields, never text or completion evidence.
func TaskDefinitions(plan *Value) ([]TaskStepDefinition, map[string]string, error) {
	definitions := []TaskStepDefinition{}
	statuses := map[string]string{}
	for _, step := range plan.Items() {
		projected := ObjectOf("id", step.Get("id"))
		for _, key := range []string{"dependsOn", "budget", "delegation"} {
			if step.Has(key) {
				projected.Set(key, step.Get(key))
			}
		}
		var definition TaskStepDefinition
		decoder := json.NewDecoder(bytes.NewReader(projected.JSON()))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&definition); err != nil {
			return nil, nil, taskStepFail("invalid-steps")
		}
		definitions = append(definitions, definition)
		statuses[definition.ID] = planText(step.Get("status"))
	}
	return definitions, statuses, nil
}
func taskConfigured(step *Value) bool {
	return step.Has("dependsOn") || step.Has("budget") || step.Has("delegation")
}
func validateTaskPlan(plan *Value, schedule *Value) error {
	definitions, statuses, err := TaskDefinitions(plan)
	if err != nil {
		return err
	}
	if _, err = ReadyTaskSteps(definitions, statuses); err != nil {
		return err
	}
	for _, step := range definitions {
		if statuses[step.ID] == "in_progress" {
			for _, dependency := range step.DependsOn {
				if statuses[dependency] != "completed" && statuses[dependency] != "not-needed" {
					return taskStepFail("dependencies-unready")
				}
			}
		}
	}
	if schedule != nil {
		var saved TaskSchedule
		if json.Unmarshal(schedule.JSON(), &saved) != nil {
			return taskStepFail("invalid-reservation")
		}
		if _, err = ReadTaskSchedule(saved, saved.Binding, definitions); err != nil {
			return err
		}
	}
	return nil
}

func taskConfigurationJSON(step *Value) string {
	value := Object()
	for _, key := range []string{"dependsOn", "budget", "delegation"} {
		if step.Has(key) {
			value.Set(key, step.Get(key))
		}
	}
	return CanonicalJSON(value)
}
func validTaskConfiguration(step *Value) bool {
	if !taskConfigured(step) {
		return true
	}
	projected := ObjectOf("id", String("step-1"))
	if step.Has("budget") {
		projected.Set("budget", step.Get("budget"))
	}
	if step.Has("delegation") {
		projected.Set("delegation", step.Get("delegation"))
	}
	definitions, _, err := TaskDefinitions(Array(projected))
	if err != nil || ValidateTaskGraph(definitions) != nil {
		return false
	}
	if dependencies := step.Get("dependsOn"); dependencies != nil {
		if !dependencies.IsArray() || dependencies.Len() > 19 {
			return false
		}
		seen := map[string]bool{}
		for _, dependency := range dependencies.Items() {
			if !planStepID(dependency) || seen[planText(dependency)] {
				return false
			}
			seen[planText(dependency)] = true
		}
	}
	return true
}

// TaskCoordinatorBinding pins the original package coordinator independently of host configuration.
type TaskCoordinatorBinding struct {
	Version      int    `json:"version"`
	Owner        string `json:"owner"`
	Run          string `json:"run"`
	Execution    string `json:"execution"`
	ScopeDigest  string `json:"scopeDigest"`
	Limit        int    `json:"limit"`
	ChildReserve int    `json:"childReserve"`
}

func readTaskCoordinatorBinding(value *Value) (*TaskCoordinatorBinding, error) {
	var out TaskCoordinatorBinding
	decoder := json.NewDecoder(bytes.NewReader(value.JSON()))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&out) != nil || out.Version != 1 || out.Owner != "native-delegation" || !taskSafeID(out.Run) || !taskSafeID(out.Execution) || !recordDigestPattern.MatchString(out.ScopeDigest) || out.Limit < 1 || out.Limit > 100 || out.ChildReserve < 1 || out.ChildReserve > out.Limit {
		return nil, taskStepFail("invalid-coordinator-envelope")
	}
	return &out, nil
}

// TaskStepEnvelope is a new explicit host-configured finite invocation ceiling, not availability.
type TaskStepEnvelope struct {
	ID       string `json:"id"`
	Revision string `json:"revision"`
	Unit     string `json:"unit"`
	Limit    int    `json:"limit"`
}
type TaskEnvelopeBinding struct {
	Run           string           `json:"run"`
	Execution     string           `json:"execution"`
	ScopeDigest   string           `json:"scopeDigest"`
	Configuration TaskStepEnvelope `json:"configuration"`
}

func validTaskStepEnvelope(value TaskStepEnvelope) bool {
	return taskSafeID(value.ID) && recordDigestPattern.MatchString(value.Revision) && value.Unit == "provider-invocations" && value.Limit >= 1 && value.Limit <= 100
}
func readTaskEnvelopeBinding(value *Value) (*TaskEnvelopeBinding, error) {
	var out TaskEnvelopeBinding
	decoder := json.NewDecoder(bytes.NewReader(value.JSON()))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&out) != nil || !taskSafeID(out.Run) || !taskSafeID(out.Execution) || !recordDigestPattern.MatchString(out.ScopeDigest) || !validTaskStepEnvelope(out.Configuration) {
		return nil, taskStepFail("invalid-execution-envelope")
	}
	return &out, nil
}

func taskRecordFields(value *Value) bool {
	state := value.Get("planState")
	for _, step := range state.Get("pending").Get("candidate").Get("plan").Items() {
		if taskConfigured(step) {
			return true
		}
	}
	if state.Has("taskSchedule") || state.Has("taskEnvelope") || state.Has("taskCoordinator") {
		return true
	}
	for _, step := range value.Get("plan").Items() {
		if taskConfigured(step) {
			return true
		}
	}
	for _, row := range state.Get("history").Items() {
		for _, change := range row.Get("diff").Items() {
			if taskConfigured(change.Get("before")) || taskConfigured(change.Get("after")) {
				return true
			}
		}
	}
	return false
}
