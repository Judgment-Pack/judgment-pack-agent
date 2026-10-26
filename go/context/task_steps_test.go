package runcontext

import (
	"encoding/json"
	"errors"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"os"
	"reflect"
	"strings"
	"testing"
)

func taskSteps183Reason(t *testing.T, err error, want string) {
	t.Helper()
	var refused *TaskStepRefused
	if !errors.As(err, &refused) || refused.Reason != want {
		t.Fatalf("want %s, got %v", want, err)
	}
}
func TestTaskSteps183HandwrittenParity(t *testing.T) {
	raw, e := os.ReadFile("../../contract/testdata/task-steps/scenarios.json")
	if e != nil {
		t.Fatal(e)
	}
	var vector struct {
		Binding TaskScheduleBinding `json:"binding"`
		Cases   []struct {
			ID        string               `json:"id"`
			Steps     []TaskStepDefinition `json:"steps"`
			Statuses  map[string]string    `json:"statuses"`
			Available int                  `json:"available"`
			Ready     []string             `json:"ready"`
			Error     string               `json:"error"`
		} `json:"cases"`
	}
	if e = json.Unmarshal(raw, &vector); e != nil {
		t.Fatal(e)
	}
	for _, row := range vector.Cases {
		t.Run(row.ID, func(t *testing.T) {
			e := ValidateTaskGraph(row.Steps)
			var schedule TaskSchedule
			if e == nil {
				schedule, e = CreateTaskSchedule(vector.Binding, row.Steps, row.Available)
			}
			if row.Error != "" {
				taskSteps183Reason(t, e, row.Error)
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			ready, e := ReadyTaskSteps(row.Steps, row.Statuses)
			if e != nil || !reflect.DeepEqual(ready, row.Ready) {
				t.Fatalf("ready=%v, %v", ready, e)
			}
			restored, e := ReadTaskSchedule(schedule, vector.Binding, row.Steps)
			if e != nil || !reflect.DeepEqual(restored, schedule) {
				t.Fatalf("restore=%+v, %v", restored, e)
			}
		})
	}
}
func TestTaskSteps183UnknownShareAndUnreadyResume(t *testing.T) {
	binding := TaskScheduleBinding{Run: "root", Execution: "execution", PlanRevision: "sha256:" + strings.Repeat("a", 64), ScopeDigest: "sha256:" + strings.Repeat("b", 64), Unit: "model-requests"}
	digest := "sha256:" + strings.Repeat("c", 64)
	steps := []TaskStepDefinition{{ID: "step-1", Budget: &TaskStepShare{1}}, {ID: "step-2", Budget: &TaskStepShare{2}}}
	statuses := map[string]string{"step-1": "in_progress", "step-2": "pending"}
	initial, e := CreateTaskSchedule(binding, steps, 3)
	if e != nil {
		t.Fatal(e)
	}
	next, e := ReserveTaskStep(initial, steps, statuses, "step-1", "exact-dispatch", digest, 3)
	if e != nil {
		t.Fatal(e)
	}
	if len(initial.Rows[0].Claims) != 0 {
		t.Fatal("candidate mutated live input")
	}
	next.Rows[0].Claims[0].State = "unknown"
	saved, e := ReadTaskSchedule(next, binding, steps)
	if e != nil || saved.Rows[0].Claims[0].State != "unknown" {
		t.Fatalf("unknown dropped: %v", e)
	}
	_, e = ReserveTaskStep(saved, steps, statuses, "step-1", "exact-dispatch", digest, 3)
	taskSteps183Reason(t, e, "reservation-already-held")
	_, e = ReserveTaskStep(saved, steps, statuses, "step-1", "new-dispatch", digest, 2)
	taskSteps183Reason(t, e, "step-budget-pause")
	changed := binding
	changed.ScopeDigest = "sha256:" + strings.Repeat("d", 64)
	_, e = ReadTaskSchedule(saved, changed, steps)
	taskSteps183Reason(t, e, "identity-changed")
	steps[0].Budget = &TaskStepShare{2}
	_, e = ReadTaskSchedule(saved, binding, steps)
	taskSteps183Reason(t, e, "identity-changed")
	dependent := []TaskStepDefinition{{ID: "step-1"}, {ID: "step-2", DependsOn: []string{"step-1"}}}
	schedule, _ := CreateTaskSchedule(binding, dependent, 3)
	_, e = ReserveTaskStep(schedule, dependent, map[string]string{"step-1": "pending", "step-2": "in_progress"}, "step-2", "d", digest, 3)
	taskSteps183Reason(t, e, "dependencies-unready")
	unassigned := []TaskStepDefinition{{ID: "step-1"}, {ID: "step-2", Budget: &TaskStepShare{2}}}
	schedule, _ = CreateTaskSchedule(binding, unassigned, 3)
	_, e = ReserveTaskStep(schedule, unassigned, statuses, "step-1", "d", digest, 2)
	taskSteps183Reason(t, e, "step-budget-pause")
}

func TestTaskSteps183ExactProfilePin(t *testing.T) {
	raw := []byte(`{"id":"research","digest":"sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd","source":{"kind":"custom","id":"project","trust":"person","version":"one"}}`)
	var pin contract.SubagentRecord
	if e := json.Unmarshal(raw, &pin); e != nil {
		t.Fatal(e)
	}
	steps := []TaskStepDefinition{{ID: "step-1", Delegation: &pin}}
	if e := RequireTaskProfiles(steps, []*contract.SubagentRecord{&pin}); e != nil {
		t.Fatal(e)
	}
	taskSteps183Reason(t, RequireTaskProfiles(steps, nil), "profile-not-admitted")
	changed := pin
	changed.Digest = "sha256:" + strings.Repeat("e", 64)
	taskSteps183Reason(t, RequireTaskProfiles(steps, []*contract.SubagentRecord{&changed}), "profile-not-admitted")
}

func TestTaskSteps183ReconciliationAndExplicitShareRelease(t *testing.T) {
	binding := TaskScheduleBinding{Run: "run", Execution: "execution", PlanRevision: "sha256:" + strings.Repeat("a", 64), ScopeDigest: "sha256:" + strings.Repeat("b", 64), Unit: "model-requests"}
	digest := "sha256:" + strings.Repeat("c", 64)
	steps := []TaskStepDefinition{{ID: "step-1", Budget: &TaskStepShare{1}}, {ID: "step-2", Budget: &TaskStepShare{2}}}
	before := map[string]string{"step-1": "in_progress", "step-2": "pending"}
	after := map[string]string{"step-1": "completed", "step-2": "pending"}
	initial, e := CreateTaskSchedule(binding, steps, 3)
	if e != nil {
		t.Fatal(e)
	}
	held, e := ReserveTaskStep(initial, steps, before, "step-1", "original-physical-claim", digest, 3)
	if e != nil {
		t.Fatal(e)
	}
	held.Rows[0].Claims[0].State = "unknown"
	revised := binding
	revised.PlanRevision = "sha256:" + strings.Repeat("e", 64)
	next, e := ReconcileTaskSchedule(&held, steps, before, revised, steps, after, 2)
	if e != nil {
		t.Fatal(e)
	}
	if next.Rows[0].Claims[0].ID != "original-physical-claim" || next.Rows[0].Claims[0].State != "unknown" || next.Binding.PlanRevision != revised.PlanRevision || held.Binding.PlanRevision != binding.PlanRevision {
		t.Fatal("revision reset held identity")
	}
	changed := append([]TaskStepDefinition{}, steps...)
	changed[0].Budget = &TaskStepShare{2}
	_, e = ReconcileTaskSchedule(&held, steps, before, revised, changed, map[string]string{"step-1": "pending", "step-2": "pending"}, 3)
	taskSteps183Reason(t, e, "active-definition-changed")
	_, e = ReconcileTaskSchedule(&held, steps, before, revised, steps[1:], map[string]string{"step-2": "pending"}, 3)
	taskSteps183Reason(t, e, "active-definition-changed")
	_, e = ReserveTaskStep(initial, steps, before, "step-1", "new-physical", digest, 1)
	taskSteps183Reason(t, e, "step-budget-pause")
	if _, e = ReserveTaskStep(initial, steps, map[string]string{"step-1": "in_progress", "step-2": "not-needed"}, "step-1", "new-physical", digest, 1); e != nil {
		t.Fatal("explicit terminal closure did not release unused share", e)
	}
}
func TestTaskSteps183AtomicChildAssociationCandidate(t *testing.T) {
	binding := TaskScheduleBinding{Run: "run", Execution: "execution", PlanRevision: "sha256:" + strings.Repeat("a", 64), ScopeDigest: "sha256:" + strings.Repeat("b", 64), Unit: "model-requests"}
	var pin contract.SubagentRecord
	if e := json.Unmarshal([]byte(`{"id":"research","digest":"sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd","source":{"kind":"custom","id":"project","trust":"person","version":"one"}}`), &pin); e != nil {
		t.Fatal(e)
	}
	steps := []TaskStepDefinition{{ID: "step-1", Delegation: &pin}, {ID: "step-2", Delegation: &pin}}
	statuses := map[string]string{"step-1": "pending", "step-2": "pending"}
	schedule, e := CreateTaskSchedule(binding, steps, 4)
	if e != nil {
		t.Fatal(e)
	}
	children := []TaskChildCandidate{{Step: "step-1", Task: "child-one", Profile: pin}, {Step: "step-2", Task: "child-two", Profile: pin}}
	next, e := BindTaskChildren(schedule, steps, statuses, children)
	if e != nil {
		t.Fatal(e)
	}
	if schedule.Rows[0].Child != nil || schedule.Rows[1].Child != nil || next.Rows[0].Child.Task != "child-one" || next.Rows[1].Child.Task != "child-two" || next.Rows[0].Child.PlanRevision != binding.PlanRevision {
		t.Fatal("candidate mutated original or lost exact association")
	}
	changed := children[1]
	changed.Profile.Digest = "sha256:" + strings.Repeat("f", 64)
	duplicateTask := children[1]
	duplicateTask.Task = "child-one"
	for _, bad := range [][]TaskChildCandidate{{children[0], children[0]}, {children[0], duplicateTask}, {children[0], changed}} {
		if _, e = BindTaskChildren(schedule, steps, statuses, bad); e == nil {
			t.Fatal("malformed child batch admitted")
		}
		if schedule.Rows[0].Child != nil || schedule.Rows[1].Child != nil {
			t.Fatal("partial child association leaked")
		}
	}
	duplicate := usageCopy(next)
	duplicate.Rows[1].Child.Task = "child-one"
	duplicate.Rows[1].Child.Execution = "child-one"
	_, e = ReadTaskSchedule(duplicate, binding, steps)
	taskSteps183Reason(t, e, "invalid-child-binding")
	_, e = BindTaskChildren(next, steps, statuses, children[:1])
	taskSteps183Reason(t, e, "child-not-admitted")
	claimed := usageCopy(schedule)
	claimed.Rows[0].Claims = []TaskStepClaim{{ID: "local-claim", RequestDigest: "sha256:" + strings.Repeat("c", 64), State: "unknown"}}
	_, e = BindTaskChildren(claimed, steps, statuses, children[:1])
	taskSteps183Reason(t, e, "child-not-admitted")
	next.Rows[0].Child.Result = &TaskChildResult{CheckpointDigest: "sha256:" + strings.Repeat("a", 64), ResultDigest: "sha256:" + strings.Repeat("b", 64), EventDigest: "sha256:" + strings.Repeat("c", 64), CommitID: "actual-child-transaction"}
	if _, e = ReadTaskSchedule(next, binding, steps); e != nil {
		t.Fatal(e)
	}
	next.Rows[0].Child.Result.CommitID = ""
	_, e = ReadTaskSchedule(next, binding, steps)
	taskSteps183Reason(t, e, "invalid-child-evidence")
}
