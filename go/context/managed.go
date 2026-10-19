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
}

// NewBoundary is the boundary for one opened run. A run is tracked where the host gave it a
// current request or a saved execution: only a tracked run's events carry checkpoints.
func NewBoundary(run *Run) *Boundary {
	tracked := false
	if run.Session.Context != nil {
		tracked = run.Session.Context.CurrentRequest != nil || run.Session.Context.Execution != nil
	}
	return &Boundary{run: run, tracked: tracked}
}

// Stopped says the run was stopped by its caller, as session.signal.aborted does: the closing
// checkpoint then says stopped rather than complete.
func (b *Boundary) Stopped() { b.stopped = true }

// Open is the opening checkpoint of a tracked run, and nothing for a run that is not tracked.
func (b *Boundary) Open() []contract.RunEvent {
	if !b.tracked {
		return nil
	}
	return b.checkpoint(nil, "")
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
	out := []contract.RunEvent{}
	context := b.run.Context
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
