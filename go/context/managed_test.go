package runcontext

// The boundary every adapter's events pass through (the port of packages/harness/context/managed.ts,
// whose own tests drive the engines; these hold the events the boundary adds and refuses).

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
)

func eventTypes(events []contract.RunEvent) []string {
	out := []string{}
	for _, event := range events {
		raw, err := json.Marshal(event)
		if err != nil {
			out = append(out, "?")
			continue
		}
		var held struct {
			Type    string `json:"type"`
			Durable *bool  `json:"durable"`
		}
		_ = json.Unmarshal(raw, &held)
		name := held.Type
		if held.Durable != nil && *held.Durable {
			name += "+durable"
		}
		out = append(out, name)
	}
	return out
}

func TestTheBoundaryCheckpointsTheOpeningAndEachCall(t *testing.T) {
	session, _ := trackedSession(t)
	run := openRun(t, session)
	boundary := NewBoundary(run)
	if held := eventTypes(boundary.Open()); len(held) != 2 || held[0] != "policy_resolved" || held[1] != "execution_checkpoint" {
		t.Errorf("the opening is %v", held)
	}
	callID := "call-1"
	// A read's checkpoint is not durable; another tool's is.
	held, stop := boundary.Event(contract.RunEvent{ToolCall: &contract.ToolCall{Type: "tool_call",
		Name: "get_schema", Args: json.RawMessage(`{}`), CallID: &callID}})
	if stop || len(held) != 2 || eventTypes(held)[0] != "execution_checkpoint" || eventTypes(held)[1] != "tool_call" {
		t.Errorf("the call's events are %v", eventTypes(held))
	}
	held, _ = boundary.Event(contract.RunEvent{ToolCall: &contract.ToolCall{Type: "tool_call",
		Name: "save_pack", Args: json.RawMessage(`{}`)}})
	if eventTypes(held)[0] != "execution_checkpoint+durable" {
		t.Errorf("a write's checkpoint is %v", eventTypes(held))
	}
	// The closing checkpoint says complete, and the end is delivered after it.
	held, stop = boundary.Event(contract.RunEvent{End: &contract.RunEventEnd{Type: "end"}})
	if !stop || eventTypes(held)[0] != "execution_checkpoint+durable" || eventTypes(held)[1] != "usage" || eventTypes(held)[2] != "end" {
		t.Errorf("the closing events are %v", eventTypes(held))
	}
	if run.Context.Execution.State().Status != "complete" {
		t.Errorf("the closing status is %s", run.Context.Execution.State().Status)
	}
	// Nothing more is delivered for a run whose end was delivered.
	if held := boundary.Failed(&authoredError{"after the end"}); held != nil {
		t.Errorf("the boundary added %v after the end", eventTypes(held))
	}
	boundary.Close()
}

func TestTheBoundaryPausesTheRunAtItsRequestLimit(t *testing.T) {
	session, _ := trackedSession(t)
	run := openRun(t, session)
	boundary := NewBoundary(run)
	boundary.Open()
	held, stop := boundary.Event(contract.RunEvent{Error: &contract.RunEventError{Type: "error",
		Message: ErrExecutionPaused.Error(), Failure: &contract.Failure{Version: 1, Code: "request-limit", Source: "desk", Outcome: "unknown"}}})
	if stop || len(held) != 1 || eventTypes(held)[0] != "budget_paused" {
		t.Errorf("the pause is %v", eventTypes(held))
	}
	if run.Context.Execution.State().Status != "paused" {
		t.Errorf("the status is %s", run.Context.Execution.State().Status)
	}
	// A pause the budget tool asked for ends the run after the call's result.
	run.Context.Execution.PauseRequested = true
	held, stop = boundary.Event(contract.RunEvent{ToolResult: &contract.ToolResult{Type: "tool_result",
		Name: "request_budget_increase", Text: "{}"}})
	if !stop {
		t.Error("the run went on after the pause")
	}
	if got := eventTypes(held); len(got) != 4 || got[0] != "tool_result" || got[1] != "budget_paused" || got[2] != "usage" || got[3] != "end" {
		t.Errorf("the pause's events are %v", got)
	}
}

func TestTheBoundaryRefusesAProposalWhereProposalsAreOff(t *testing.T) {
	proposal := contract.RunEvent{Proposal: &contract.RunEventProposal{Type: "proposal",
		Document: map[string]json.RawMessage{"id": json.RawMessage(`"expense"`)}, Unknowns: []string{}}}
	for _, held := range []struct {
		name   string
		input  *Input
		detail string
	}{
		{"proposals off", &Input{ArtifactProposals: new(bool)}, "unavailable in this conversation"},
		{"plan mode", &Input{Phase: "plan"}, "unavailable in Plan mode"},
	} {
		session, _ := trackedSession(t)
		session.Context = held.input
		run := openRun(t, session)
		boundary := NewBoundary(run)
		out, stop := boundary.Event(proposal)
		if stop || len(out) != 1 || out[0].Guardrail == nil {
			t.Fatalf("%s: the events are %v", held.name, eventTypes(out))
		}
		if out[0].Guardrail.Tool != "proposal" || out[0].Guardrail.Action != "refused" ||
			!strings.Contains(out[0].Guardrail.Detail, held.detail) {
			t.Errorf("%s: the guardrail is %+v", held.name, out[0].Guardrail)
		}
	}
}

func TestTheBoundaryDigestsCanonicalProposalJSON(t *testing.T) {
	session, _ := trackedSession(t)
	run := openRun(t, session)
	boundary := NewBoundary(run)
	out, stop := boundary.Event(contract.RunEvent{Proposal: &contract.RunEventProposal{Type: "proposal", Document: map[string]json.RawMessage{"id": json.RawMessage(`"p"`)}, Unknowns: []string{"who signs"}}})
	if stop || len(out) != 1 || out[0].Proposal == nil {
		t.Fatalf("events: %v", eventTypes(out))
	}
	if got := out[0].Proposal.Digest; got != "sha256:681f94562c65706b6b99916c65ad1a90bf2f8a90cd3067f3326ca514502042fa" {
		t.Fatalf("digest: %s", got)
	}
}

func TestTheBoundaryCarriesTheFailureOfAContextThatCannotOpen(t *testing.T) {
	// A host tool that takes a name the boundary reserves: the run cannot open.
	session, _ := trackedSession(t)
	session.HostTools = []HostTool{{Name: SearchTool, Description: "A reserved name.",
		InputSchema: json.RawMessage(`{"type":"object"}`),
		Execute:     func(ctx context.Context, args json.RawMessage) (ToolResult, error) { return ResultOf(nil, nil), nil }}}
	_, err := ContextualSession(context.Background(), session)
	refused(t, err, "reserved")
	held := FailedToOpen(err)
	if got := eventTypes(held); len(got) != 3 || got[0] != "error" || got[1] != "usage" || got[2] != "end" {
		t.Errorf("the events are %v", got)
	}
	if held[0].Error.Failure == nil || held[0].Error.Failure.Source != "context" {
		t.Errorf("the failure is %+v", held[0].Error)
	}
	// A run its caller stopped is no error event.
	if got := eventTypes(FailedToOpen(failure.Cancelled)); len(got) != 2 || got[0] != "usage" || got[1] != "end" {
		t.Errorf("a cancelled run gave %v", got)
	}
}

func TestTheBoundaryDoesNotCheckpointAnUntrackedRun(t *testing.T) {
	session := modelSession()
	run := openRun(t, session)
	boundary := NewBoundary(run)
	if held := boundary.Open(); len(held) != 1 || held[0].PolicyResolved == nil {
		t.Errorf("an untracked run opened with %v", eventTypes(held))
	}
	held, _ := boundary.Event(contract.RunEvent{ToolCall: &contract.ToolCall{Type: "tool_call",
		Name: "get_schema", Args: json.RawMessage(`{}`)}})
	if got := eventTypes(held); len(got) != 1 || got[0] != "tool_call" {
		t.Errorf("an untracked call is %v", got)
	}
	// Its operations are still journaled, so a later checkpoint carries them.
	if len(run.Context.Execution.State().Operations) != 1 {
		t.Errorf("%d operations were journaled", len(run.Context.Execution.State().Operations))
	}
}

func TestOutputRefusalClosesUsageBeforeEnd(t *testing.T) {
	for _, input := range []string{"AKIAABCDEFGHIJKLMNOP", strings.Repeat("x", 200000)} {
		session, _ := trackedSession(t)
		session.Policy = PolicyLayer{"outputGuard": "refuse"}
		opened := openRun(t, session)
		boundary := NewBoundary(opened)
		events, ended := boundary.Event(contract.RunEvent{Message: &contract.RunEventMessage{Type: "message", Text: input}})
		if !ended || len(events) < 2 || events[len(events)-2].Usage == nil || events[len(events)-1].End == nil {
			t.Fatalf("terminal events: %v", eventTypes(events))
		}
		count := 0
		for _, event := range events {
			if event.Usage != nil {
				count++
			}
		}
		if count != 1 || opened.Context.Execution.State().Usage == nil {
			t.Fatalf("usage count: %d", count)
		}
		raw, _ := json.Marshal(events[len(events)-2])
		var decoded contract.RunEvent
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatal(err)
		}
	}
}
