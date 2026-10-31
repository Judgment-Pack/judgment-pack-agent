package runcontext

// ADR-0029 at the session boundary, the twins of packages/harness/principalSession.test.ts: a
// delegated run's tool events, refusals and checkpoint carry its principal under event 3 and
// checkpoint 4; a resume continues only under the same principals; a run with no delegation keeps
// its earlier versions and members.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
)

func principalSessionTools(ran *principalRan) []HostTool {
	tools := principalTools(ran, nil, nil)
	out := []HostTool{}
	for _, tool := range tools {
		if tool.Name == "read_note" || tool.Name == "delete_note" {
			out = append(out, tool)
		}
	}
	return pinLocalFixture(out)
}

func principalSession(t *testing.T, ran *principalRan, chain int, execution *contract.Checkpoint, software ...string) Session {
	t.Helper()
	session, _ := trackedSession(t)
	session.HostTools = principalSessionTools(ran)
	input := *session.Context
	input.Execution = execution
	if chain >= 0 {
		v := loadPrincipalChains(t)
		principal := principalInput(v, v.Items[chain], principalAdmitAll, func() (time.Time, error) { return time.Parse(time.RFC3339Nano, "2026-10-30T01:00:00.000Z") })
		if len(software) > 0 {
			principal.Software.Version = software[0]
		}
		input.Principal = principal
	}
	session.Context = &input
	return session
}

// principalRunEvents runs a read and a delete of the notes source through the boundary.
func principalRunEvents(t *testing.T, run *Run) []contract.RunEvent {
	t.Helper()
	boundary := NewBoundary(run)
	defer boundary.Close()
	out := boundary.Open()
	for _, call := range [][2]string{{"c1", "notes__read_note"}, {"c2", "notes__delete_note"}} {
		id, name := call[0], call[1]
		events, _ := boundary.Event(contract.RunEvent{ToolCall: &contract.ToolCall{Type: "tool_call", CallID: &id, Name: name, Args: json.RawMessage(`{}`)}})
		out = append(out, events...)
		text, isError := "done", false
		if _, err := hostTool(t, run, name).Execute(WithCallID(context.Background(), id), json.RawMessage(`{}`)); err != nil {
			text, isError = err.Error(), true
		}
		events, _ = boundary.Event(contract.RunEvent{ToolResult: &contract.ToolResult{Type: "tool_result", CallID: &id, Name: name, IsError: isError, Text: text}})
		out = append(out, events...)
	}
	events, _ := boundary.Event(contract.RunEvent{End: &contract.RunEventEnd{Type: "end"}})
	return append(out, events...)
}

func principalLastCheckpoint(events []contract.RunEvent) *contract.RunEventExecutionCheckpoint {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].ExecutionCheckpoint != nil {
			return events[i].ExecutionCheckpoint
		}
	}
	return nil
}

func TestPrincipalSessionRecordsAndResume(t *testing.T) {
	v := loadPrincipalChains(t)
	t.Run("a delegated run's tool events, refusal and checkpoint 4 carry its principal", func(t *testing.T) {
		ran := &principalRan{}
		run := openRun(t, principalSession(t, ran, 0, nil))
		events := principalRunEvents(t, run)
		if !sameStrings(ran.get(), []string{"read_note"}) {
			t.Fatalf("ran %v", ran.get())
		}
		tooled, refusals := 0, 0
		for _, event := range events {
			raw, err := json.Marshal(event)
			if err != nil {
				t.Fatal(err)
			}
			if err := contract.CheckRecord("event", raw); err != nil {
				t.Fatalf("event %s: %v", raw, err)
			}
			switch {
			case event.ToolCall != nil:
				tooled++
				if event.ToolCall.Principal == nil || event.ToolCall.Version == nil || *event.ToolCall.Version != 3 || event.ToolCall.Principal.Delegation.Digest != v.Digests.Root {
					t.Fatalf("tool call %s", raw)
				}
			case event.ToolResult != nil:
				tooled++
				if event.ToolResult.Principal == nil || event.ToolResult.Version == nil || *event.ToolResult.Version != 3 {
					t.Fatalf("tool result %s", raw)
				}
			case event.Guardrail != nil && event.Guardrail.Reason != nil && *event.Guardrail.Reason == "delegation refused":
				refusals++
				if event.Guardrail.Principal == nil || event.Guardrail.Refusal == nil || event.Guardrail.Refusal.Name != "out-of-scope" || event.Guardrail.Refusal.Reason == nil || *event.Guardrail.Refusal.Reason != "tool" || *event.Guardrail.Version != 3 {
					t.Fatalf("refusal %s", raw)
				}
			}
		}
		if tooled != 4 || refusals != 1 {
			t.Fatalf("tool events %d refusals %d", tooled, refusals)
		}
		last := principalLastCheckpoint(events)
		if last == nil || last.Checkpoint.Version != 4 || last.Checkpoint.Principal == nil || last.Checkpoint.Principal.Actor.Agent != "b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1" || *last.Version != 3 {
			t.Fatalf("checkpoint %+v", last)
		}
		raw, _ := json.Marshal(last.Checkpoint)
		value, _ := ParseJSON(raw)
		if _, err := ReadExecution(value); err != nil {
			t.Fatal(err)
		}
		// Version 4 is exactly a delegated run's: without its principal it is refused, and version 3
		// cannot carry one.
		bare := Object()
		for _, key := range value.Keys() {
			if key != "principal" {
				bare.Set(key, value.Get(key).Clone())
			}
		}
		if _, err := ReadExecution(bare); err == nil {
			t.Fatal("checkpoint 4 without its principal")
		}
		three, _ := ParseJSON(raw)
		three.Set("version", Number(3))
		if _, err := ReadExecution(three); err == nil {
			t.Fatal("checkpoint 3 with a principal")
		}
		terminal := last.Checkpoint
		terminal.Status = "complete"
		report, err := ProjectRunReport(terminal)
		if err != nil || report.Get("version").Num() != 3 || report.Get("checkpointVersion").Num() != 4 || CanonicalJSON(report.Get("principal")) != CanonicalJSON(value.Get("principal")) {
			t.Fatalf("report %v %v", err, report)
		}
		if _, err := validateRunReport(report); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("a run with no delegation writes as before", func(t *testing.T) {
		ran := &principalRan{}
		run := openRun(t, principalSession(t, ran, -1, nil))
		events := principalRunEvents(t, run)
		if !sameStrings(ran.get(), []string{"read_note", "delete_note"}) {
			t.Fatalf("ran %v", ran.get())
		}
		raw, _ := json.Marshal(events)
		if strings.Contains(string(raw), "principal") || principalLastCheckpoint(events).Checkpoint.Version >= 4 || strings.Contains(string(raw), `"version":3`) {
			t.Fatalf("undelegated events %s", raw)
		}
	})
	t.Run("a leaf whose software is not the run's refuses to start", func(t *testing.T) {
		_, err := ContextualSession(context.Background(), principalSession(t, &principalRan{}, 0, nil, "0.12.0"))
		var held *failure.Error
		if !errors.As(err, &held) || held.Failure.Code != "configuration" || !strings.Contains(err.Error(), "software-mismatch") {
			t.Fatalf("start: %v", err)
		}
	})
	t.Run("a resume continues only under the principals it began under", func(t *testing.T) {
		delegated := principalLastCheckpoint(principalRunEvents(t, openRun(t, principalSession(t, &principalRan{}, 0, nil)))).Checkpoint
		undelegated := principalLastCheckpoint(principalRunEvents(t, openRun(t, principalSession(t, &principalRan{}, -1, nil)))).Checkpoint
		// A resume under the same subject and root actor from another issuing host is another principal.
		otherHost := delegated
		foreign := *delegated.Principal
		foreign.Actor.Host = "other-host"
		otherHost.Principal = &foreign
		// The vector's chains: 0 root, 1 child, 2 re-issued, 3 another actor, 4 another subject.
		for _, row := range []struct {
			saved contract.Checkpoint
			chain int
		}{{delegated, 3}, {delegated, 4}, {delegated, -1}, {undelegated, 0}, {otherHost, 0}} {
			saved := row.saved
			saved.Status = "running"
			_, err := ContextualSession(context.Background(), principalSession(t, &principalRan{}, row.chain, &saved))
			var held *failure.Error
			if !errors.As(err, &held) || held.Failure.Code != "stale-state" || !strings.Contains(err.Error(), "principal-changed") {
				t.Fatalf("resume under chain %d: %v", row.chain, err)
			}
		}
		// A chain re-issued for the same principals replaces the record; so does one narrowed from
		// the same root.
		for chain, id := range map[int]string{0: "33333333333333333333333333333333", 2: "3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a3a", 1: "44444444444444444444444444444444"} {
			saved := delegated
			saved.Status = "running"
			ran := &principalRan{}
			events := principalRunEvents(t, openRun(t, principalSession(t, ran, chain, &saved)))
			if !sameStrings(ran.get(), []string{"read_note"}) || principalLastCheckpoint(events).Checkpoint.Principal.Delegation.ID != id {
				t.Fatalf("chain %d: ran %v", chain, ran.get())
			}
		}
	})
}
