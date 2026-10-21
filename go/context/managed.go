package runcontext

// The boundary every adapter's events pass through (the port of packages/harness/context/managed.ts).
//
// All adapters enter here, including direct tests, critics and delegated runs. Derived context
// lives only as long as the execution, or, where the host hands the run a chat-scoped context, as
// long as the chat (agent#8). Originals remain in the owning chat or research journal. Rebuilding
// cannot resurrect deleted snapshots.
//
// The TypeScript is one async generator that wraps the adapter's own: it yields the opening
// checkpoint, then each of the adapter's events, with a checkpoint after each call, result and
// question of a tracked run, the pause the budget earns, the closing checkpoint, and the refusal
// of a proposal in Plan mode or where proposals are off. Go has no generator, so the boundary is
// a type the adapter puts each of its events through, in order:
//
//	boundary := runcontext.NewBoundary(run)
//	for _, event := range boundary.Open() { deliver(event) }
//	for event := range adapter {
//	    out, stop := boundary.Event(event)
//	    for _, held := range out { deliver(held) }
//	    if stop { break }
//	}
//	for _, event := range boundary.Failed(err) { deliver(event) } // where the run went wrong
//	boundary.Close()
//
// Event answers the events to deliver in place of the one given (none where it is refused or
// swallowed, several where a checkpoint joins it) and whether the run is over. Failed answers the
// failure event and the end a run that could not open or could not finish carries; it answers
// nothing once the adapter's own end has been delivered, as the TypeScript rethrows there. Close
// ends the run's turn on its chat, as the TypeScript's finally does.
//
// What the gate holds is not here (go/gate): the guardrail notices a refused call earns
// (gate.Options.Emit writes them between the call and its result) and the critic's own narrowing.

import (
	"errors"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
)

// Boundary is the boundary one run's events pass through.
type Boundary struct {
	run     *Run
	tracked bool
	ended   bool
	paused  bool
	failed  bool
	stopped bool
	// held are the adapter's results the boundary answered itself (ADR-0008: a call refused while
	// another waits for the person), by call id, or by name where the events carry none; requested
	// is the call id of the approval_requested event the run emitted.
	held       map[string]bool
	heldByName map[string]int
	requested  string
}

// NewBoundary is the boundary for one opened run. A run is tracked where the host gave it a
// current request or a saved execution: only a tracked run's events carry checkpoints.
func NewBoundary(run *Run) *Boundary {
	tracked := false
	if run.Session.Context != nil {
		tracked = run.Session.Context.CurrentRequest != nil || run.Session.Context.Execution != nil
	}
	return &Boundary{run: run, tracked: tracked, held: map[string]bool{}, heldByName: map[string]int{}}
}

// Stopped says the run was stopped by its caller, as session.signal.aborted does: the closing
// checkpoint then says stopped rather than complete.
func (b *Boundary) Stopped() { b.stopped = true }

// Open reports policy admission, then the opening checkpoint of a tracked run.
func (b *Boundary) Open() []contract.RunEvent {
	// ADR-0008: the checkpoints the continuation of an approval wrote, acknowledged, before the
	// run's first request.
	continued := b.run.Context.TakeContinued()
	policy := []contract.RunEvent{{PolicyResolved: &contract.RunEventPolicyResolved{Type: "policy_resolved", Members: b.run.Context.ResolvedPolicy.Record()}}}
	if b.run.Context.PaceWarning != "" {
		reason := "pace warning"
		policy = append(policy, contract.RunEvent{Guardrail: &contract.RunEventGuardrail{Type: "guardrail", Tool: "pace", Action: "refused", Reason: &reason, Detail: b.run.Context.PaceWarning}})
	}
	continued = append(policy, continued...)
	if !b.tracked {
		return append(b.notices(), continued...)
	}
	return append(append(continued, b.checkpoint(nil, "")...), b.notices()...)
}

// notices are the guardrail events the source gate's refusals earned (ADR-0006), put on the stream
// before the next event: a withheld declaration's at the start, a refused call's before its result.
func (b *Boundary) notices() []contract.RunEvent {
	out := b.run.Context.TakeNotices()
	if b.run.Context.Output != nil {
		out = append(out, b.run.Context.Output.TakeNotices()...)
	}
	if b.run.Context.Sources != nil {
		out = append(out, b.run.Context.Sources.TakeNotices()...)
	}
	// ADR-0010: a refused request_review's guardrail, before its result.
	if b.run.Context.Review != nil {
		out = append(out, b.run.Context.Review.TakeNotices()...)
	}
	if b.run.Context.Drafts != nil {
		out = append(out, b.run.Context.Drafts.TakeNotices()...)
	}
	if b.run.Context.Memories != nil {
		out = append(out, b.run.Context.Memories.TakeNotices()...)
	}
	if g := b.run.Context.Output; g != nil {
		clean := make([]contract.RunEvent, 0, len(out))
		for _, notice := range out {
			guarded, err := b.guardNotice(notice)
			if err == nil {
				clean = append(clean, guarded)
			}
		}
		// Only guard-generated labels remain; do not scan replacement holes again.
		out = append(clean, g.TakeNotices()...)
	}
	return out
}

// checkpoint is one execution_checkpoint event, or nothing where the checkpoint is too large to
// write (the failure the snapshot answers reaches the adapter through Failed).
func (b *Boundary) checkpoint(durable *bool, status string) []contract.RunEvent {
	state, err := b.run.Context.Execution.Snapshot(status)
	if err != nil {
		return nil
	}
	return []contract.RunEvent{{ExecutionCheckpoint: &contract.RunEventExecutionCheckpoint{
		Type: "execution_checkpoint", Durable: durable, Checkpoint: state}}}
}

// readNames are the tool names a checkpoint after them need not be durable: a read changes
// nothing a resume would have to replay.
var readNames = []string{"get_schema", "get_authoring_instructions", "list_context", "read_context", "list_examples", "get_example"}

// Event is one of the adapter's events put through the boundary: the events to deliver in its
// place, and whether the run is over.
func (b *Boundary) Event(event contract.RunEvent) ([]contract.RunEvent, bool) {
	if b.run.Context.Output != nil && b.run.Context.Output.Mode != "off" && (event.MessageProgress != nil || event.Reasoning != nil && !event.Reasoning.Done) {
		return b.notices(), false
	}
	var err error
	event, err = b.guardOutput(event)
	out := append([]contract.RunEvent{}, b.notices()...)
	if err != nil {
		reason := "output refused"
		reported := false
		for _, notice := range out {
			if notice.Guardrail != nil && notice.Guardrail.Reason != nil && *notice.Guardrail.Reason == reason {
				reported = true
			}
		}
		if !reported {
			out = append(out, contract.RunEvent{Guardrail: &contract.RunEventGuardrail{Type: "guardrail", Tool: "output", Action: "refused", Reason: &reason, Detail: err.Error()}})
		}
		out = append(out, b.checkpoint(nil, "stopped")...)
		b.ended = true
		return append(out, contract.RunEvent{End: &contract.RunEventEnd{Type: "end"}}), true
	}
	context := b.run.Context
	// ADR-0008: an adapter asks the host's approval policy where it hands an act call to the host
	// (SessionContext.Approval) and delivers approval_requested in place of the call where it asks.
	// A call made while another waits is refused: said here with its call, its guardrail and its
	// refused result, whatever the adapter does with the call after, and the adapter's own result
	// for it is not delivered again.
	if event.ApprovalRequested != nil {
		b.requested = event.ApprovalRequested.CallID
	}
	if call := event.ToolCall; call != nil && context.Sources != nil {
		callID := ""
		if call.CallID != nil {
			callID = *call.CallID
		}
		if detail := context.Sources.Pending(callID, call.Name); detail != "" {
			decorated := context.Sources.Decorate(event)
			context.Execution.ObserveToolCall(*decorated.ToolCall)
			reason := "approval pending"
			out = append(out, decorated, contract.RunEvent{Guardrail: &contract.RunEventGuardrail{Type: "guardrail", Tool: call.Name, Action: "refused", Detail: detail, Reason: &reason}})
			text, structured := failure.ToolFailure(failure.NewError(contract.Failure{Version: 1, Code: "permission", Source: "tool", Outcome: "not-started", Detail: &detail}))
			result := contract.RunEvent{ToolResult: &contract.ToolResult{Type: "tool_result", CallID: call.CallID, Name: call.Name, IsError: true, Text: text, Structured: structured}}
			result = context.Sources.Decorate(result)
			context.Execution.ObserveToolResult(*result.ToolResult)
			if callID != "" {
				b.held[callID] = true
			} else {
				b.heldByName[call.Name]++
			}
			return append(out, result), false
		}
	}
	if result := event.ToolResult; result != nil {
		if result.CallID != nil && *result.CallID != "" && b.held[*result.CallID] {
			delete(b.held, *result.CallID)
			return out, false
		}
		if (result.CallID == nil || *result.CallID == "") && b.heldByName[result.Name] > 0 {
			b.heldByName[result.Name]--
			return out, false
		}
	}
	// ADR-0008: the run ends once an act call waits for the person: the adapter's own end, the
	// refused request's error, or a pause of its own all end it paused, with the checkpoint durable
	// and no error.
	if context.Execution.ApprovalAsked() && (event.End != nil || event.Error != nil || event.BudgetPaused != nil) {
		asked := *context.Execution.State.Approval
		if b.requested != asked.CallID {
			out = append(out, *ApprovalRequested(asked))
		}
		b.paused = true
		context.Execution.State.Status = "paused"
		durable := true
		if state, err := context.Execution.Snapshot("paused"); err == nil {
			out = append(out, contract.RunEvent{ExecutionCheckpoint: &contract.RunEventExecutionCheckpoint{Type: "execution_checkpoint", Durable: &durable, Checkpoint: state}})
		}
		b.ended = true
		return append(out, contract.RunEvent{End: &contract.RunEventEnd{Type: "end"}}), true
	}
	// ADR-0006: a tool with a source's call and result carry its source record.
	if context.Sources != nil {
		event = context.Sources.Decorate(event)
	}
	if event.Error != nil {
		b.failed = true
	}
	switch {
	case event.ToolCall != nil:
		context.Execution.ObserveToolCall(*event.ToolCall)
	case event.ToolResult != nil:
		context.Execution.ObserveToolResult(*event.ToolResult)
	}
	paused := event.BudgetPaused != nil
	if event.Error != nil {
		if event.Error.Failure != nil && event.Error.Failure.Code == "request-limit" {
			paused = true
		}
		if event.Error.Message == ErrExecutionPaused.Error() {
			paused = true
		}
	}
	if paused {
		b.paused = true
		context.Execution.State.Status = "paused"
		if state, err := context.Execution.Snapshot("paused"); err == nil {
			out = append(out, contract.RunEvent{BudgetPaused: &contract.RunEventBudgetPaused{
				Type: "budget_paused", Checkpoint: state}})
		}
		return out, false
	}
	if event.ToolResult != nil && context.Execution.PauseRequested {
		out = append(out, event)
		context.Execution.State.Status = "paused"
		if state, err := context.Execution.Snapshot("paused"); err == nil {
			out = append(out, contract.RunEvent{BudgetPaused: &contract.RunEventBudgetPaused{
				Type: "budget_paused", Checkpoint: state}})
		}
		out = append(out, contract.RunEvent{End: &contract.RunEventEnd{Type: "end"}})
		b.ended = true
		return out, true
	}
	if b.tracked && (event.ToolCall != nil || event.ToolResult != nil || event.UserQuestion != nil) {
		name := ""
		if event.ToolCall != nil {
			name = event.ToolCall.Name
		}
		if event.ToolResult != nil {
			name = event.ToolResult.Name
		}
		read := event.UserQuestion == nil && contains(readNames, name)
		for _, tool := range b.run.Session.HostTools {
			if tool.Name != name {
				continue
			}
			if tool.Effect == "read" || tool.Delegation == "read" || tool.Delegation == "research" {
				read = true
			}
		}
		durable := !read
		out = append(out, b.checkpoint(&durable, "")...)
	}
	// ADR-0010: the critic's decision is written durably: the checkpoint carrying it follows the event.
	if event.CriticDecided != nil {
		out = append(out, event)
		if b.tracked {
			durable := true
			out = append(out, b.checkpoint(&durable, "")...)
		}
		return out, false
	}
	if event.End != nil && b.tracked && !b.paused {
		status := "complete"
		if b.stopped || b.failed {
			status = "stopped"
		}
		context.Execution.State.Status = status
		durable := true
		out = append(out, b.checkpoint(&durable, "")...)
	}
	if event.Proposal != nil {
		off := b.run.Session.Context != nil && b.run.Session.Context.ArtifactProposals != nil && !*b.run.Session.Context.ArtifactProposals
		if off {
			return append(out, contract.RunEvent{Guardrail: &contract.RunEventGuardrail{Type: "guardrail",
				Tool: "proposal", Action: "refused",
				Detail: "Artifact proposals are unavailable in this conversation. Use its host tools or start a new artifact conversation."}}), false
		}
		if b.run.Session.Context != nil && b.run.Session.Context.Phase == "plan" {
			return append(out, contract.RunEvent{Guardrail: &contract.RunEventGuardrail{Type: "guardrail",
				Tool: "proposal", Action: "refused",
				Detail: "Artifact proposals are unavailable in Plan mode. Use propose_plan and wait for Start work."}}), false
		}
	}
	if event.End != nil {
		b.ended = true
	}
	return append(out, event), event.End != nil
}

// Failed is the failure event and the end a run that went wrong carries: the failure of a context
// that cannot open, or of a run that stopped part way. Nothing once the adapter's own end has
// been delivered, where the TypeScript rethrows instead, and nothing for a run its caller
// stopped, which is no error event.
func (b *Boundary) Failed(cause error) []contract.RunEvent {
	if b.ended || cause == nil {
		return nil
	}
	if failure.IsCancelled(cause) || b.stopped {
		return []contract.RunEvent{{End: &contract.RunEventEnd{Type: "end"}}}
	}
	return []contract.RunEvent{failure.Event(cause, "context"), {End: &contract.RunEventEnd{Type: "end"}}}
}

// FailedToOpen is the events a run whose context could not open carries: the failure, then the
// end. ContextualSession's error reaches an adapter before a boundary exists, so this is a
// function of its own.
func FailedToOpen(cause error) []contract.RunEvent {
	var policy *PolicyRefusal
	if errors.As(cause, &policy) {
		out := []contract.RunEvent{policy.Event()}
		if policy.Checkpoint != nil {
			out = append(out, contract.RunEvent{ExecutionCheckpoint: &contract.RunEventExecutionCheckpoint{Type: "execution_checkpoint", Checkpoint: *policy.Checkpoint}})
		}
		return append(out, failure.Event(cause, "context"), contract.RunEvent{End: &contract.RunEventEnd{Type: "end"}})
	}

	if failure.IsCancelled(cause) {
		return []contract.RunEvent{{End: &contract.RunEventEnd{Type: "end"}}}
	}
	return []contract.RunEvent{failure.Event(cause, "context"), {End: &contract.RunEventEnd{Type: "end"}}}
}

// Close ends the run's turn on its chat, whatever happened.
func (b *Boundary) Close() { b.run.Context.Close() }

func contains(list []string, name string) bool {
	for _, held := range list {
		if held == name {
			return true
		}
	}
	return false
}
