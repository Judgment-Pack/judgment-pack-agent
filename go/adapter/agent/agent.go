// Package agent is the Go adapter on the agent capability (ADR-0007): a run whose model loop is the
// provider's, each invocation one call of the host's provider-run function (adapter.Agent.Run). It
// follows the TypeScript codex engine's lifecycle (packages/harness/engines/codex/index.ts), as the
// behavioural reference and not a copy, and is provider-neutral: no transport, account or
// credential is the package's.
//
// One run is the author's invocation, then, where a proposal could not be read, one tool-free
// reformat invocation, then, where the session asks for it, the critic's invocation and the
// harness's own comparisons. Every invocation is built by the one assembly ADR-0005 made canonical
// (runcontext.SessionContext.System and Opening): its instructions are the package's guidance, the
// host's parts and the data rule; its prompt is the request text, a chat's index of earlier
// results, then the execution state. Every tool the invocation is offered is named by an alias
// (desk_runtime_N, desk_host_N) and answered through the same gate the model adapters use: a Runtime
// call through the narrow handle, a host tool held to the phase by AdmitHost first.
//
// A delegate_tasks call is a handoff, not an awaited call: the engine emits delegation_requested
// with the call's id, stops the invocation and returns once the host's function has returned, so the
// invocation's account is released before anything else runs; the delegation coordinator
// (go/adapter/delegate) runs the batch and starts the response's next run with the call's result. A
// question to the person ends the run the same way, unanswered.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/turn"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/wire"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
)

// ID is the engine id the contract gives a run on an agent capability (the inventory's codex): it
// names the capability's lifecycle, not a provider, and is what the host's models answer is asked
// for.
const ID = "codex"

// CriticPass is how long one critic pass may take (CRITIC_PASS_MILLISECONDS): the critic's
// invocation and the harness's own calls after it, from the invocation's start. A harness call is
// not made once it has passed.
const CriticPass = 10 * time.Minute

// New is the adapter.
func New() adapter.Adapter { return adapter.Func(Run) }

var (
	// errWaiting ends a run that asked the person a question: the run ends, unanswered.
	errWaiting = errors.New("waiting for input")
	// errHandoff ends the parent's invocation at its delegate_tasks call (ADR-0007).
	errHandoff = errors.New("delegation handoff")
)

// route is one alias the invocations are offered: the tool's own name, and the host tool where it is
// one.
type route struct {
	name string
	host *runcontext.HostTool
}

// runState is one run.
type runState struct {
	run      *adapter.Run
	sc       *runcontext.SessionContext
	session  runcontext.Session
	boundary *runcontext.Boundary

	routes map[string]route
	served map[string]bool
	all    []adapter.AgentTool

	// mu guards the delivery to the host; held guards what a tool call sets while an invocation
	// runs.
	mu    sync.Mutex
	ended bool

	// criticEffort is the critic's effort (ADR-0010): the decision's tier, set before the critic runs.
	criticEffort string

	held          sync.Mutex
	awaitingInput bool
	handoff       bool
	toolProposal  *turn.Proposal
	fatal         error
	pause         context.CancelFunc
}

// Run is the adapter: one run on the agent capability.
func Run(ctx context.Context, run *adapter.Run) error {
	r := &runState{run: run}
	if run.Agent == nil || run.Agent.Run == nil || run.Model != nil {
		r.emitAll(runcontext.FailedToOpen(failure.Authored("configuration", "The run is not on an agent capability alone; nothing was sent.", "desk")))
		return nil
	}
	named := runcontext.RunModels{Critic: run.ModelName, Child: run.ModelName}
	if run.Agent != nil {
		named.Critic = run.Agent.Model
		named.Child = run.Agent.Model
	}
	if run.Models != nil {
		var err error
		named, err = run.Models()
		if err != nil {
			r.emitAll(runcontext.FailedToOpen(err))
			return nil
		}
	}
	session := runcontext.Session{
		Prompt:            run.Prompt,
		TestPrompt:        run.TestPrompt,
		HostTools:         append([]runcontext.HostTool{}, run.HostTools...),
		Context:           run.Context,
		Purpose:           run.Purpose,
		AllowConversation: run.AllowConversation,
		Interactive:       run.Interactive,
		CallTool:          r.gatedCall,
		Agent:             &runcontext.AgentSession{Model: run.Agent.Model, Effort: run.Agent.Effort},
		Host:              run.Host,
		Skills:            run.Skills,
		// ADR-0010: the critic's switch, as the host passes it.
		Critic: run.Critic,
		Policy: run.Policy, PolicyProject: run.PolicyProject, TaskClass: run.TaskClass, PaceUsed: run.PaceUsed, PolicyLegacyModels: &named, PolicyConnections: append(adapter.PolicyConnectionIDs(run), named.Critic, named.Child), PolicyRequests: run.PolicyRequests,
		CriticProject:     run.CriticProject,
		CriticReasoning:   run.CriticReasoning,
		ExistingPack:      run.ExistingPack,
		ReviewerAssigned:  run.ReviewerAssigned,
		AdversarialReview: run.Review,
	}
	for _, tool := range run.Tools {
		session.Tools = append(session.Tools, runcontext.RuntimeTool{Name: tool.Name})
	}
	opened, err := runcontext.ContextualSession(ctx, session)
	if err != nil {
		r.emitAll(runcontext.FailedToOpen(err))
		return nil
	}
	r.sc = opened.Context
	local := *run
	if err := adapter.ApplyPolicy(&local, r.sc, named); err != nil {
		r.sc.Close()
		r.emitAll(runcontext.FailedToOpen(err))
		return nil
	}
	r.run = &local
	// ADR-0008: an act call's approval is written asked only once this run has claimed its pause.
	r.sc.PauseClaim = func() bool { return r.claimPause(false) }
	r.session = opened.Session
	r.boundary = runcontext.NewBoundary(opened)
	defer r.boundary.Close()
	r.emitAll(r.boundary.Open())

	err = r.drive(ctx)
	switch {
	case err == nil || errors.Is(err, errWaiting) || errors.Is(err, errHandoff):
	case ctx.Err() != nil || failure.IsCancelled(err):
		r.boundary.Stopped()
	case errors.Is(err, runcontext.ErrExecutionPaused):
		if state, snapErr := r.sc.Snapshot("paused"); snapErr == nil {
			r.deliver(contract.RunEvent{BudgetPaused: &contract.RunEventBudgetPaused{Type: "budget_paused", Checkpoint: state}})
		}
	default:
		r.deliver(failure.Event(err, "agent"))
	}
	r.deliver(contract.RunEvent{End: &contract.RunEventEnd{Type: "end"}})
	return nil
}

// emitAll hands events to the host, in order.
func (r *runState) emitAll(events []contract.RunEvent) {
	for _, event := range events {
		r.run.Emit(event)
	}
}

// deliver puts one event through the boundary and hands what it answers to the host, in order;
// nothing after the end.
func (r *runState) deliver(event contract.RunEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ended {
		return
	}
	out, stop := r.boundary.Event(event)
	for _, held := range out {
		r.run.Emit(held)
	}
	if stop {
		r.ended = true
	}
}

// systemFor is the run's stable instructions for its purpose.
func (r *runState) systemFor() string {
	switch {
	case r.run.Purpose == "graph":
		return turn.GraphSystem
	case r.run.Purpose == "brief":
		return turn.BriefSystem
	case r.run.Purpose == "test-design":
		return turn.TestDesignSystem
	case r.session.AllowConversation:
		return turn.ConversationSystem
	}
	return turn.System
}

// gatedCall is the Runtime call the context wraps: the delegation coordinator's count first, where it
// holds one, then the gate's narrow handle.
func (r *runState) gatedCall(ctx context.Context, name string, args json.RawMessage) (runcontext.ToolResult, error) {
	if r.run.BeforeRuntimeCall != nil {
		if err := r.run.BeforeRuntimeCall(ctx, name); err != nil {
			return runcontext.ToolResult{}, err
		}
	}
	result, err := r.run.Gate.CallTool(ctx, name, args)
	if err != nil {
		return runcontext.ToolResult{}, err
	}
	return resultOf(result), nil
}

// buildRoutes names every tool of the session by its alias, Runtime tools first, then host tools
// (the boundary's context tools among them), in the session's order.
func (r *runState) buildRoutes() error {
	r.routes, r.served = map[string]route{}, map[string]bool{}
	add := func(kind, name, description string, schema json.RawMessage, host *runcontext.HostTool) error {
		if r.served[name] {
			return failure.Authored("configuration", "Assistant tool names must be unique; "+name+" is named twice. Nothing was run.", "desk")
		}
		r.served[name] = true
		alias := "desk_" + kind + "_" + strconv.Itoa(len(r.all))
		r.routes[alias] = route{name: name, host: host}
		if len(strings.TrimSpace(string(schema))) == 0 {
			schema = json.RawMessage(`{"type":"object"}`)
		}
		r.all = append(r.all, adapter.AgentTool{Name: alias, Description: name + ": " + description, InputSchema: schema})
		return nil
	}
	for _, tool := range r.run.Tools {
		description := ""
		if tool.Description != nil {
			description = *tool.Description
		}
		if err := add("runtime", tool.Name, description, tool.InputSchema, nil); err != nil {
			return err
		}
	}
	for at := range r.session.HostTools {
		tool := &r.session.HostTools[at]
		if err := add("host", tool.Name, tool.Description, tool.InputSchema, tool); err != nil {
			return err
		}
	}
	return nil
}

// offeredTo is the tools one invocation is offered: the author every tool of its phase (Plan mode's
// reads and planning tools in Plan mode); the critic its allow-list of the Runtime's reads and the
// harness's two readers; the reformat invocation none.
func (r *runState) offeredTo(phase string) []adapter.AgentTool {
	out := []adapter.AgentTool{}
	if phase == "reformat" {
		return out
	}
	plan := r.run.Context != nil && r.run.Context.Phase == "plan"
	for _, tool := range r.all {
		held := r.routes[tool.Name]
		keep := true
		switch {
		case phase == "critic" && held.host == nil:
			keep = turn.CriticMayCall(held.name)
		case phase == "critic":
			keep = turn.CriticMayRead(held.name)
		case plan && held.host == nil:
			keep = contains(gate.PlanRuntimeReads, held.name)
		case plan:
			keep = contains(gate.ContextTools, held.name) || gate.PlanningTool(held.host.Declaration())
		}
		if keep {
			out = append(out, tool)
		}
	}
	return out
}

func contains(list []string, name string) bool {
	for _, held := range list {
		if held == name {
			return true
		}
	}
	return false
}

// drive is the run: the author's invocation, the reformat and the critic where they apply, and the
// proposal.
func (r *runState) drive(ctx context.Context) error {
	execution, pause := context.WithCancel(ctx)
	defer pause()
	r.pause = pause
	if _, err := turn.ReadCriticCases(casesOf(r.run.Cases)); err != nil {
		return failure.Authored("configuration", err.Error(), "context")
	}
	for _, host := range r.run.HostTools {
		for _, served := range r.run.Tools {
			if host.Name == served.Name {
				return failure.Authored("configuration", "host tool "+host.Name+" is named like a tool the runtime serves; nothing was run", "desk")
			}
		}
	}
	if err := r.buildRoutes(); err != nil {
		return err
	}
	var declared []contract.ToolOfferHostTool
	for _, tool := range r.session.HostTools {
		declared = append(declared, tool.Declaration())
	}
	instructions := r.systemFor()
	if turn.OffersProposeTool(declared) {
		instructions += turn.ProposePackInstructions
	}
	var parts []string
	if r.run.Context != nil && strings.TrimSpace(r.run.Context.CurrentState) != "" {
		parts = append(parts, r.run.Context.CurrentState)
	}
	if r.session.AllowConversation {
		parts = append(parts, r.sc.Execution.Guidance())
	}
	final, err := r.invoke(execution, r.session.Prompt, instructions, "author", nil, "", strings.Join(parts, "\n\n"))
	if err != nil {
		// A proposal made through propose_pack is finished work: delivered, with a note naming the
		// failure where the run failed after it. A person's stop still stops.
		made := r.takeProposal()
		if made == nil || ctx.Err() != nil {
			return err
		}
		if errors.Is(err, errWaiting) || errors.Is(err, errHandoff) {
			r.deliver(proposalEvent(*made))
			return err
		}
		if failure.IsCancelled(err) {
			return err
		}
		event := proposalEvent(*made)
		note := turn.ProposalNote("turn-unfinished", err, "agent")
		event.Proposal.Note = &note
		r.deliver(event)
		return nil
	}
	made := r.takeProposal()
	if r.session.AllowConversation && made == nil && !turn.HasProposalFence(final) {
		// ADR-0010: nothing to refute; the decision says so.
		r.deliver(runcontext.CriticDecidedEvent(r.sc.DecideCritic(false, len(r.run.Cases) > 0)))
		r.deliver(messageEvent(turn.ProseOf(final)))
		return nil
	}
	var proposal turn.Proposal
	prose := ""
	var unread error
	switch {
	case made != nil && turn.HasProposalFence(final):
		unread = turn.TwoProposals()
	case made != nil:
		proposal, prose = *made, turn.ProseOf(final)
	default:
		proposal, unread = turn.ExtractProposal(final)
		prose = turn.ProseOf(final)
	}
	if unread != nil {
		// One reformat invocation, tool-free, shown its own answer and asked for the same proposal in
		// the shape the contract reads. The first answer is delivered whole whatever it returns.
		keep := func() {
			if kept := turn.WholeAnswer(final); kept != "" && ctx.Err() == nil {
				r.deliver(messageEvent(kept))
			}
		}
		r.deliver(contract.RunEvent{ResponseRecovery: &contract.RunEventResponseRecovery{Type: "response_recovery"}})
		reformatted, err := r.invoke(execution, ReformatPrompt(final, unread), instructions, "reformat", nil, "", "")
		if err != nil {
			if failure.IsCancelled(err) || ctx.Err() != nil {
				return err
			}
			keep()
			return err
		}
		if r.session.AllowConversation && !turn.HasProposalFence(reformatted) {
			keep()
			if said := turn.ProseOf(reformatted); said != "" {
				r.deliver(messageEvent(said))
			}
			return unread
		}
		recovered, err := turn.ExtractProposal(reformatted)
		if err != nil {
			keep()
			return unread
		}
		proposal = recovered
		prose = turn.ProseOf(final)
		if prose == "" {
			prose = turn.ProseOf(reformatted)
		}
	}
	if prose != "" {
		r.deliver(messageEvent(prose))
	}
	var critique *turn.Critique
	var note *contract.RunEventProposalNote
	// ADR-0010: one decision on every adapter, emitted before anything the critic does; on this
	// capability the critic has room where the response's invocations leave it one.
	decided := r.sc.DecideCritic(true, len(r.run.Cases) > 0)
	r.deliver(runcontext.CriticDecidedEvent(decided))
	r.criticEffort = decided.Reasoning
	if decided.Ran {
		if cannot := turn.CriticCannotRun(r.session.TestPrompt); cannot != nil {
			critique = cannot
			r.deliver(turn.CritiqueEvent(*cannot))
		} else {
			held, heldNote, err := r.critic(execution, proposal)
			if err != nil {
				return err
			}
			critique, note = held, heldNote
		}
	}
	event := proposalEvent(proposal)
	event.Proposal.Critique = turn.CritiqueOnProposal(critique)
	event.Proposal.Note = note
	r.deliver(event)
	return nil
}

// ReformatPrompt is reformatPrompt: the one reformat invocation's prompt, the model shown its own
// answer, since every invocation starts on a new thread.
func ReformatPrompt(answer string, cause error) string {
	return turn.ReformatRequest(cause) + "\n\nYour previous answer is between the two lines below.\nBEGIN PREVIOUS ANSWER\n" + answer + "\nEND PREVIOUS ANSWER"
}

func (r *runState) takeProposal() *turn.Proposal {
	r.held.Lock()
	defer r.held.Unlock()
	made := r.toolProposal
	r.toolProposal = nil
	return made
}

// critic is the refutation pass (agent#10) on this capability: one invocation on the host's critic
// model, offered the critic's allow-list, then the harness's own comparisons within the pass's
// clock. A critic that does not finish is a note on the proposal, never an error ahead of it.
func (r *runState) critic(ctx context.Context, proposal turn.Proposal) (*turn.Critique, *contract.RunEventProposalNote, error) {
	cases, err := turn.ReadCriticCases(casesOf(r.run.Cases))
	if err != nil {
		return nil, nil, failure.Authored("configuration", err.Error(), "context")
	}
	recorder := turn.OpenCritique(turn.CritiqueOptions{Cases: cases, Document: proposal.Document})
	figures := contract.ProvenanceCriticFigures{Model: r.run.Agent.Model, Unit: "agent-runs"}
	harnessCalls := 0
	counted := func() *contract.ProvenanceCriticFigures {
		held := figures
		if harnessCalls > 0 {
			calls := int64(harnessCalls)
			held.HarnessCalls = &calls
		}
		return &held
	}
	r.sc.BeginIsolatedPhase()
	r.sc.CriticReading = true
	r.run.Gate.BeginCritic()
	defer func() {
		r.sc.CriticReading = false
		r.run.Gate.EndCritic()
	}()
	started := time.Now()
	text := ""
	err = func() error {
		model := r.run.Agent.Model
		if r.run.Models != nil {
			named, err := r.run.Models()
			if err != nil {
				return err
			}
			if named.Critic != "" {
				model = named.Critic
			}
		}
		figures = contract.ProvenanceCriticFigures{Model: model, Unit: "agent-runs", Requests: 1}
		asked := ""
		if model != r.run.Agent.Model {
			asked = model
		}
		said, err := r.invoke(ctx, turn.CriticMessage(r.session.TestPrompt, proposal.Document, cases), turn.CriticSystem, "critic", recorder, asked, "")
		if err != nil {
			return err
		}
		text = said
		var offered []string
		for _, tool := range r.offeredTo("critic") {
			offered = append(offered, r.routes[tool.Name].name)
		}
		return turn.CompareTheRest(ctx, recorder, proposal.Document, offered, func(ctx context.Context, name string, args json.RawMessage) error {
			made, err := r.harnessCall(ctx, name, args, recorder, CriticPass-time.Since(started))
			if made {
				harnessCalls++
			}
			return err
		})
	}()
	if err != nil {
		if failure.IsCancelled(err) || ctx.Err() != nil || errors.Is(err, errWaiting) {
			return nil, nil, err
		}
		if r.fatalError() != nil {
			return nil, nil, r.fatalError()
		}
		note := turn.CriticNote(err, "agent", counted())
		return nil, &note, nil
	}
	critique := recorder.Critique(text)
	critique.Figures = counted()
	r.deliver(turn.CritiqueEvent(critique))
	return &critique, nil, nil
}

// harnessCall is one runtime call the harness makes itself in the critic's pass: through the same
// gated call and deadline as the agent's calls, on the stream as a call and its result, and shown to
// the recorder. Past the pass's clock it is not made.
func (r *runState) harnessCall(ctx context.Context, name string, args json.RawMessage, recorder *turn.Recorder, left time.Duration) (bool, error) {
	if left <= 0 {
		return false, nil
	}
	callID := wire.RandomUUID()
	origin := "harness"
	r.deliver(callEvent(callID, name, args, origin))
	limit := time.Duration(r.sc.Policy.ToolCallMilliseconds) * time.Millisecond
	if left < limit {
		limit = left
	}
	answered := true
	result, err := turn.WithDeadline(ctx, limit, func() error { return turn.ToolTimeout(limit) }, func(ctx context.Context) (runcontext.ToolResult, error) {
		return r.session.CallTool(ctx, name, args)
	})
	if err != nil {
		if failure.IsCancelled(err) || ctx.Err() != nil {
			return true, failure.Cancelled
		}
		result, answered = failed(err), false
	}
	r.deliver(resultEvent(callID, name, result, origin))
	if answered {
		recorder.Saw(name, textOfResult(result), args, structuredOf(result))
	}
	return true, nil
}

func (r *runState) fatalError() error {
	r.held.Lock()
	defer r.held.Unlock()
	return r.fatal
}

// casesOf is the host's declared cases as one JSON array.
func casesOf(cases []json.RawMessage) json.RawMessage {
	if len(cases) == 0 {
		return json.RawMessage(`[]`)
	}
	encoded, _ := json.Marshal(cases)
	return encoded
}

func proposalEvent(proposal turn.Proposal) contract.RunEvent {
	var document map[string]json.RawMessage
	_ = json.Unmarshal(proposal.Document, &document)
	unknowns := proposal.Unknowns
	if unknowns == nil {
		unknowns = []string{}
	}
	return contract.RunEvent{Proposal: &contract.RunEventProposal{Type: "proposal", Document: document, Unknowns: unknowns}}
}

func messageEvent(text string) contract.RunEvent {
	return contract.RunEvent{Message: &contract.RunEventMessage{Type: "message", Text: text}}
}
