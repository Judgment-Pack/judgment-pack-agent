package runcontext

import (
	"encoding/json"
	"errors"
	"os"
	"slices"
	"testing"
	"time"
)

// The Go twin of packages/harness/storage/goalRecords.test.ts (#230): the shared goal vectors and the goal table's
// transitions. The kit is run_goal_conformance_test.go, and the flow through a real run is go/adapter/goaltests.

func readGoalVector(t *testing.T, name string) map[string]json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile("../../contract/vectors/goal/" + name)
	if err != nil {
		t.Fatal(err)
	}
	var file map[string]json.RawMessage
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	return file
}
func goalItems(t *testing.T, raw json.RawMessage) []json.RawMessage {
	t.Helper()
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		t.Fatal(err)
	}
	return items
}
func goalRefusedWith(err error, reason string) bool {
	var refused *GoalRefused
	return errors.As(err, &refused) && (reason == "" || refused.Reason == reason)
}

func TestGoalRecordVectors(t *testing.T) {
	file := readGoalVector(t, "goal.json")
	var names []string
	_ = json.Unmarshal(file["names"], &names)
	for i, record := range goalItems(t, file["items"]) {
		held, err := ReadRunGoal(record)
		if err != nil {
			t.Fatalf("%s: %v", names[i], err)
		}
		if jobCanonOf(t, held) != jobCanon(t, record) {
			t.Fatalf("%s: %s", names[i], jobCanonOf(t, held))
		}
	}
	for _, list := range []string{"refused", "readerRefused"} {
		for _, row := range jobVectorRows(t, file[list]) {
			if _, err := ReadRunGoal(row["record"]); !goalRefusedWith(err, "invalid") {
				t.Fatalf("%s: %v", row["problem"], err)
			}
		}
	}
}

func TestGoalRecordsReachNoOtherReader(t *testing.T) {
	file := readGoalVector(t, "goal.json")
	for _, record := range goalItems(t, file["items"]) {
		readers := map[string]func([]byte) error{
			"job":      func(raw []byte) error { _, err := ReadRunJob(raw); return err },
			"wait":     func(raw []byte) error { _, err := ReadRunWait(raw); return err },
			"export":   func(raw []byte) error { _, err := ReadWaitExport(raw); return err },
			"lease":    func(raw []byte) error { _, err := ReadRunLease(raw); return err },
			"fence":    func(raw []byte) error { _, err := ReadLeaseFence(raw); return err },
			"admitted": func(raw []byte) error { _, err := ReadEffectAdmission(raw); return err },
			"checkpoint": func(raw []byte) error {
				value, err := ParseJSON(raw)
				if err != nil {
					return err
				}
				held, err := ReadExecution(value)
				if err == nil && held == nil {
					return errors.New("read nothing")
				}
				return err
			},
		}
		for name, read := range readers {
			if read(record) == nil {
				t.Fatalf("%s took a goal record", name)
			}
		}
	}
	jobs := readJobVector(t, "job.json")
	for _, record := range goalItems(t, jobs["items"]) {
		if _, err := ReadRunGoal(record); !goalRefusedWith(err, "invalid") {
			t.Fatal("the goal reader took a job record", err)
		}
	}
}

func TestGoalCommandVectors(t *testing.T) {
	file := readGoalVector(t, "command.json")
	var digests []string
	_ = json.Unmarshal(file["digests"], &digests)
	for i, raw := range goalItems(t, file["items"]) {
		command, err := ReadGoalCommand(raw)
		if err != nil {
			t.Fatalf("%d: %v", i, err)
		}
		if jobCanonOf(t, command) != jobCanon(t, raw) {
			t.Fatalf("%d: %s", i, jobCanonOf(t, command))
		}
		if digest, err := GoalCommandDigest(command); err != nil || digest != digests[i] {
			t.Fatalf("%d: %s %v", i, digest, err)
		}
	}
	for _, list := range []string{"refused", "readerRefused"} {
		for _, row := range jobVectorRows(t, file[list]) {
			if _, err := ReadGoalCommand(row["record"]); !goalRefusedWith(err, "invalid") {
				t.Fatalf("%s: %v", row["problem"], err)
			}
		}
	}
	for _, raw := range goalItems(t, file["transitions"]) {
		held, err := ReadGoalTransition(raw)
		if err != nil || jobCanonOf(t, held) != jobCanon(t, raw) {
			t.Fatal(string(raw), err)
		}
	}
	for _, list := range []string{"transitionsRefused", "transitionsReaderRefused"} {
		for _, row := range jobVectorRows(t, file[list]) {
			if _, err := ReadGoalTransition(row["record"]); !goalRefusedWith(err, "invalid") {
				t.Fatalf("%s: %v", row["problem"], err)
			}
		}
	}
	for _, raw := range goalItems(t, file["inspects"]) {
		held, err := ReadGoalInspect(raw)
		if err != nil || jobCanonOf(t, held) != jobCanon(t, raw) {
			t.Fatal(string(raw), err)
		}
	}
	var deliveryDigests []string
	_ = json.Unmarshal(file["deliveryDigests"], &deliveryDigests)
	for i, raw := range goalItems(t, file["deliveries"]) {
		held, err := ReadGoalDeliveries(raw)
		if err != nil || jobCanonOf(t, held) != jobCanon(t, raw) {
			t.Fatal(string(raw), err)
		}
		if digest, err := GoalDeliveryDigest(held); err != nil || digest != deliveryDigests[i] {
			t.Fatal(digest, err)
		}
	}
}

func TestGoalRefusalVectors(t *testing.T) {
	file := readGoalVector(t, "refusal.json")
	reasons := []string{}
	for _, raw := range goalItems(t, file["items"]) {
		held, err := ReadGoalRefusal(raw)
		if err != nil || jobCanonOf(t, (&GoalRefused{Reason: held.Reason}).Record()) != jobCanon(t, raw) {
			t.Fatal(string(raw), err)
		}
		reasons = append(reasons, held.Reason)
	}
	if !slices.Equal(reasons, GoalRefusalReasons) {
		t.Fatal(reasons)
	}
	capability := []string{}
	for _, raw := range goalItems(t, file["capability"]) {
		held, err := ReadGoalCapabilityRefusal(raw)
		if err != nil || jobCanonOf(t, (&GoalCapabilityRefused{Reason: held.Reason}).Record()) != jobCanon(t, raw) {
			t.Fatal(string(raw), err)
		}
		capability = append(capability, held.Reason)
	}
	if !slices.Equal(capability, GoalCapabilityReasons) {
		t.Fatal(capability)
	}
	for _, row := range jobVectorRows(t, file["refused"]) {
		if _, err := ReadGoalRefusal(row["record"]); !goalRefusedWith(err, "invalid") {
			t.Fatal(err)
		}
	}
}

func TestGoalReportVectors(t *testing.T) {
	records := goalItems(t, readGoalVector(t, "goal.json")["items"])
	reports := goalItems(t, readGoalVector(t, "report.json")["items"])
	for i, raw := range records {
		goal, err := ReadRunGoal(raw)
		if err != nil {
			t.Fatal(err)
		}
		report, err := GoalReportOf(goal)
		if err != nil || jobCanonOf(t, report) != jobCanon(t, reports[i]) {
			t.Fatalf("%d: %s %v", i, jobCanonOf(t, report), err)
		}
		read, err := ReadGoalReport(reports[i])
		if err != nil || jobCanonOf(t, read) != jobCanon(t, reports[i]) {
			t.Fatalf("%d: %v", i, err)
		}
	}
}

func TestGoalTransitionVectors(t *testing.T) {
	var file struct {
		Scenarios []struct {
			Name     string          `json:"name"`
			Resource string          `json:"resource"`
			Final    json.RawMessage `json:"final"`
			Steps    []struct {
				At      string          `json:"at"`
				Op      string          `json:"op"`
				Input   json.RawMessage `json:"input"`
				Refused string          `json:"refused"`
				Context *struct {
					Head *struct {
						Revision   string `json:"revision"`
						Checkpoint string `json:"checkpoint"`
					} `json:"head"`
					Waits []RunWait `json:"waits"`
					Jobs  []RunJob  `json:"jobs"`
					Lease *RunLease `json:"lease"`
				} `json:"context"`
			} `json:"steps"`
		} `json:"scenarios"`
	}
	raw, err := os.ReadFile("../../contract/vectors/goal/transitions.json")
	if err != nil || json.Unmarshal(raw, &file) != nil {
		t.Fatal(err)
	}
	for _, scenario := range file.Scenarios {
		var at time.Time
		table := NewGoalTable(func() time.Time { return at })
		for i, step := range scenario.Steps {
			at, _ = time.Parse("2006-01-02T15:04:05.000Z", step.At)
			run := GoalContext{Waits: []RunWait{}, Jobs: []RunJob{}}
			if step.Context != nil {
				if step.Context.Head != nil {
					run.Head = &RunHead{RunReceipt: RunReceipt{Revision: step.Context.Head.Revision}, Checkpoint: []byte(step.Context.Head.Checkpoint)}
				}
				if step.Context.Waits != nil {
					run.Waits = step.Context.Waits
				}
				if step.Context.Jobs != nil {
					run.Jobs = step.Context.Jobs
				}
				run.Lease = step.Context.Lease
			}
			var err error
			var answer GoalAnswer
			switch step.Op {
			case "inspect":
				var input GoalInspect
				_ = json.Unmarshal(step.Input, &input)
				_, err = table.Inspect(input)
			case "deliver":
				var input GoalDeliveries
				_ = json.Unmarshal(step.Input, &input)
				_, err = table.Deliver(input)
			default:
				var input GoalTransition
				if json.Unmarshal(step.Input, &input) != nil {
					t.Fatalf("%s, step %d: unread", scenario.Name, i)
				}
				answer, err = table.Command(input, func(string) GoalContext { return run })
			}
			var refused *GoalRefused
			got := ""
			if errors.As(err, &refused) {
				got = refused.Reason
			} else if err != nil {
				t.Fatalf("%s, step %d: %v", scenario.Name, i, err)
			}
			if got != step.Refused {
				t.Fatalf("%s, step %d: %q, want %q", scenario.Name, i, got, step.Refused)
			}
			if step.Refused == "" && step.Op == "command" && run.Lease != nil && (answer.Lease == nil || jobCanonOf(t, *answer.Lease) != jobCanonOf(t, *run.Lease)) {
				t.Fatalf("%s, step %d: lease %v", scenario.Name, i, answer.Lease)
			}
		}
		record, err := table.Read(scenario.Resource)
		if err != nil || record == nil || jobCanonOf(t, *record) != jobCanon(t, scenario.Final) {
			t.Fatalf("%s: %s %v", scenario.Name, jobCanonOf(t, record), err)
		}
		if _, err := ReadRunGoal([]byte(jobCanonOf(t, *record))); err != nil {
			t.Fatal(err)
		}
	}
}
