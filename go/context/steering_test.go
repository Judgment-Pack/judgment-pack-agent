package runcontext

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

func validSteerRefusal(t *testing.T, event contract.RunEvent) {
	t.Helper()
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := ParseJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	schema := readJSON(t, "contract/schemas/run-event.json").Get("$defs").Get("guardrail")
	if got := ArgumentProblem(schema, actual); got != "" {
		t.Fatal(got)
	}
}

func steerController(input <-chan SteerInput) *ExecutionController {
	return &ExecutionController{session: Session{Context: &Input{Steer: input}}, State: &Execution{Steers: &contract.ProvenanceSteerReport{Queued: []contract.ProvenanceQueuedSteer{}, Delivered: []contract.ProvenanceDeliveredSteer{}}}}
}

func steerInput(values ...string) <-chan SteerInput {
	q := make(chan SteerInput, len(values))
	for _, value := range values {
		q <- SteerInput{Text: value}
	}
	close(q)
	return q
}

func TestSteersAreFramedOrderedBoundedAndRecorded(t *testing.T) {
	policy := Defaults
	c := steerController(steerInput("first", "second"))
	text, events := c.TakeSteers(strings.Repeat("a", 32), 3, policy)
	if len(events) != 2 || events[0].Steered == nil || events[0].Steered.Bytes != 5 || events[1].Steered == nil || events[1].Steered.Bytes != 6 || strings.Index(text, "\nfirst\n") >= strings.Index(text, "\nsecond\n") || strings.Count(text, Opening) != 2 {
		t.Fatalf("ordered framed delivery: %q %#v", text, events)
	}
	if len(c.State.Steers.Queued) != 0 || len(c.State.Steers.Delivered) != 2 || c.State.Steers.Delivered[0].AtRequest != 3 {
		t.Fatalf("report: %#v", c.State.Steers)
	}

	c = steerController(steerInput(strings.Repeat("x", 2001)))
	text, events = c.TakeSteers(strings.Repeat("b", 32), 1, policy)
	if text != "" || len(events) != 1 || events[0].Guardrail == nil || events[0].Guardrail.Reason == nil || *events[0].Guardrail.Reason != "steer refused" {
		t.Fatalf("oversized refusal: %q %#v", text, events)
	}
	validSteerRefusal(t, events[0])

	c = steerController(steerInput(""))
	_, events = c.TakeSteers(strings.Repeat("b", 32), 1, policy)
	validSteerRefusal(t, events[0])

	c = steerController(steerInput("fifth"))
	c.State.Steers.Delivered = make([]contract.ProvenanceDeliveredSteer, policy.SteersPerRun)
	_, events = c.TakeSteers(strings.Repeat("b", 32), 2, policy)
	validSteerRefusal(t, events[0])
}

func TestSteerIsRefusedDuringApprovalPauseAndQueuedStateResumes(t *testing.T) {
	policy := Defaults
	c := steerController(steerInput("not the decision"))
	c.State.Approval = &contract.CheckpointApproval{State: "asked"}
	c.State.Steers.Queued = []contract.ProvenanceQueuedSteer{{Text: "accepted before pause", Bytes: 21}}
	events := c.QueueSteers(policy)
	if len(events) != 1 || events[0].Guardrail == nil || len(c.State.Steers.Queued) != 1 || c.State.Steers.Queued[0].Text != "accepted before pause" {
		t.Fatalf("pause refusal: %#v, queue %#v", events, c.State.Steers.Queued)
	}
	validSteerRefusal(t, events[0])

	c = steerController(nil)
	c.State.Steers.Queued = []contract.ProvenanceQueuedSteer{{Text: "resume this", Bytes: 11}}
	text, events := c.TakeSteers(strings.Repeat("d", 32), 2, policy)
	if !strings.Contains(text, "\nresume this\n") || len(events) != 1 || len(c.State.Steers.Queued) != 0 || len(c.State.Steers.Delivered) != 1 {
		t.Fatalf("resumed queue: %q %#v", text, c.State.Steers)
	}
}
