package runcontext

// The progress tracker (the port of packages/harness/execution/progress.test.ts).

import "testing"

// operation is one completed operation, as the tracker reads it.
func operation(name string, args *Value, result *Value) *Operation {
	held := ToolResult{StructuredContent: result, order: []string{"structuredContent"}}
	return &Operation{ID: randomUUID(), Name: name, Args: args, Status: "complete", Result: &held}
}

func defaultOperation() *Operation {
	return operation("read", MustParseJSON(`{"id":"one"}`), MustParseJSON(`{"value":false}`))
}

func TestTheTrackerDetectsUnchangedAlternatingTools(t *testing.T) {
	progress := NewProgressTracker()
	read := operation("read", MustParseJSON(`{"id":"one","type":"pack"}`), MustParseJSON(`{"value":false}`))
	validate := operation("validate", MustParseJSON(`{"id":"one"}`), MustParseJSON(`{"value":false}`))
	progress.Observe(read)
	progress.Observe(validate)
	// The same arguments written in another order are the same work.
	progress.Observe(operation("read", MustParseJSON(`{"type":"pack","id":"one"}`), MustParseJSON(`{"value":false}`)))
	progress.Observe(validate)
	if progress.Stalled() {
		t.Error("two repetitions stalled the response")
	}
	if progress.Guidance() == "" {
		t.Error("the state carries no note about the repetition")
	}
	progress.Observe(read)
	if !progress.Stalled() {
		t.Error("three repetitions did not stall the response")
	}
	// A changed result is new work.
	progress.Observe(operation("read", MustParseJSON(`{"id":"one","type":"pack"}`), MustParseJSON(`{"value":true}`)))
	if progress.Stalled() {
		t.Error("a changed result left the response stalled")
	}
}

func TestTheTrackerKeepsTypedValuesDistinct(t *testing.T) {
	progress := NewProgressTracker()
	for _, value := range []string{`{}`, `{"value":null}`, `{"value":false}`, `{"value":0}`, `{"value":"0"}`} {
		progress.Observe(operation("read", Object(), MustParseJSON(value)))
		if progress.Stalled() {
			t.Errorf("%s was read as a repetition", value)
		}
	}
	for i := 0; i < 4; i++ {
		progress.Observe(operation("evaluate", ObjectOf("id", Number(float64(i))), MustParseJSON(`{"value":false}`)))
		if progress.Stalled() {
			t.Errorf("changed arguments at %d were read as a repetition", i)
		}
	}
}

func TestTheTrackerNeedsNewRepetitionAfterAContinuation(t *testing.T) {
	progress := NewProgressTracker()
	read := defaultOperation()
	progress.Seed([]*Operation{read, read, read, read})
	if progress.Stalled() {
		t.Error("a restored checkpoint started stalled")
	}
	progress.Observe(read)
	progress.Observe(read)
	if progress.Stalled() {
		t.Error("two repetitions after a continuation stalled the response")
	}
	interrupted := *read
	interrupted.Status = "interrupted"
	progress.Observe(&interrupted)
	progress.Observe(read)
	if progress.Stalled() {
		t.Error("an interrupted call was counted")
	}
}

func TestTheTrackerPermitsOneCorrectiveStep(t *testing.T) {
	progress := NewProgressTracker()
	read := operation("read", MustParseJSON(`{"a":0,"b":false}`), MustParseJSON(`{"value":false}`))
	for i := 0; i < 4; i++ {
		progress.Observe(read)
	}
	if held := progress.NextStep(5); held != "recover" {
		t.Errorf("the next step is %s", held)
	}
	if !progress.Rejects("read", MustParseJSON(`{"b":false,"a":0}`)) {
		t.Error("the recovery request admitted the same work again")
	}
	if progress.Rejects("read", MustParseJSON(`{"a":"0","b":false}`)) {
		t.Error("the recovery request refused different arguments")
	}
	if progress.Rejects("evaluate", MustParseJSON(`{"a":0,"b":false}`)) {
		t.Error("the recovery request refused another tool")
	}
	// A host refusal is not new evidence or completed work.
	failed := *read
	failed.Status = "failed"
	held := ToolResult{StructuredContent: MustParseJSON(`{"failure":{"version":1,"code":"repeated-work","source":"desk","outcome":"not-started"}}`),
		order: []string{"structuredContent"}}
	failed.Result = &held
	progress.Observe(&failed)
	if !progress.Stalled() {
		t.Error("a refused recovery call opened another loop")
	}
	if step := progress.NextStep(4); step != "finish" {
		t.Errorf("the next step is %s", step)
	}
}

func TestTheTrackerResumesUsefulWorkAfterACorrection(t *testing.T) {
	progress := NewProgressTracker()
	read := defaultOperation()
	for i := 0; i < 4; i++ {
		progress.Observe(read)
	}
	if held := progress.NextStep(10); held != "recover" {
		t.Errorf("the next step is %s", held)
	}
	progress.Observe(operation("evaluate", MustParseJSON(`{"id":"one"}`), MustParseJSON(`{"value":false}`)))
	if held := progress.NextStep(9); held != "work" {
		t.Errorf("the next step is %s", held)
	}
	for i := 0; i < 3; i++ {
		progress.Observe(read)
	}
	if held := progress.NextStep(8); held != "finish" {
		t.Errorf("the next step is %s", held)
	}
}

func TestTheTrackerKeepsTheLastRequestForAFinalAnswer(t *testing.T) {
	progress := NewProgressTracker()
	read := defaultOperation()
	for i := 0; i < 4; i++ {
		progress.Observe(read)
	}
	if held := progress.NextStep(1); held != "finish" {
		t.Errorf("the next step is %s", held)
	}
	progress.BeginPhase()
	if held := progress.NextStep(5); held != "work" {
		t.Errorf("the next step is %s", held)
	}
	progress.Observe(read)
	if progress.Guidance() != "" {
		t.Errorf("the isolated phase carries %q", progress.Guidance())
	}
}
