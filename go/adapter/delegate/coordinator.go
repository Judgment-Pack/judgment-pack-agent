package delegate

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/turn"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
)

// Options are the coordinator's: the profiles a batch may name (TASK_PROFILES where nil).
type Options struct {
	Profiles []turn.TaskProfile
}

// With is withDelegation: an adapter whose interactive, conversational runs are offered
// delegate_tasks, and whose children run on the same adapter. A run the host gives no Children
// capability, or that is not interactive and conversational, is the adapter's own run.
//
// On the agent capability (run.Agent, ADR-0007) the parent's delegate_tasks call is a handoff: the
// parent's invocation ends at delegation_requested and releases its account, the batch's children run
// one at a time on that account, and a new invocation of the parent continues the response with the
// call's exact result (agent.go).
func With(inner adapter.Adapter, options Options) adapter.Adapter {
	return adapter.Func(func(ctx context.Context, run *adapter.Run) error {
		prepared, err := runcontext.PrepareDurableInput(ctx, run.Context)
		if err != nil {
			for _, event := range runcontext.FailedToOpen(err) {
				run.Emit(event)
			}
			return nil
		}
		copied := *run
		copied.Context = prepared
		run = &copied
		if !run.AllowConversation || !run.Interactive || run.Children == nil {
			return inner.Run(ctx, run)
		}
		if run.Agent != nil && run.ModelName == "" {
			held := *run
			held.ModelName = run.Agent.Model
			run = &held
		}
		profiles := options.Profiles
		if profiles == nil {
			profiles = turn.TaskProfiles
		}
		c := &coordinator{inner: inner, run: run, profiles: profiles, parentRunID: newID(), storageContext: ctx, models: map[string]string{},
			refusedBy: map[string]string{}, childStarts: map[string]time.Time{}, childStops: map[string]context.CancelFunc{}, handedBack: map[string]bool{},
			runs: agentRuns{children: map[string]int{}}, account: newAccount(), criticReserve: criticReserve(run)}
		if run.Context != nil && run.Context.Persistence != nil {
			run.Context.Persistence.Writer.Decorate = func(state contract.Checkpoint) (contract.Checkpoint, error) { return c.decorate(state), nil }
			if run.Agent != nil && run.Context.TaskSteps {
				if err := run.Context.Persistence.Writer.AttachNativeTaskEnvelope(*run.Context.Persistence, runcontext.NativeTaskEnvelopeSource{Limit: turn.MaxAgentRuns, ChildReserve: 1 + c.criticReserve, Counts: func() map[string]int {
					c.runsMu.Lock()
					parent := c.runs.parent
					c.runsMu.Unlock()
					counts := map[string]int{run.Context.Persistence.Writer.ID: parent}
					for _, batch := range c.batchFigures() {
						for _, child := range batch.Children {
							if child.AgentRuns != nil {
								counts[child.Task] += int(*child.AgentRuns)
							}
						}
					}
					return counts
				}, Reserve: func(scope runcontext.DurableScope) int {
					if scope.Task != "" {
						return 1 + c.criticReserve
					}
					return 0
				}}); err != nil {
					for _, event := range runcontext.FailedToOpen(err) {
						run.Emit(event)
					}
					return nil
				}
			}

		}
		return c.drive(ctx)
	})
}

func newID() string { return uuid() }

// criticReserve is the agent invocation held back for the critic (ADR-0010): 1 on an agent
// capability under auto and on, where nothing forces the critic off, from the first invocation on; 0
// under off and on the model capability, where nothing is held.
func criticReserve(run *adapter.Run) int {
	if run.Agent == nil {
		return 0
	}
	forced := runcontext.CriticForcedFacts{Purpose: run.Purpose, ReviewerAssigned: run.ReviewerAssigned}
	if run.Context != nil {
		forced.Child = run.Context.Objective != ""
		forced.Phase = run.Context.Phase
	}
	table := runcontext.CriticTable{CriticDefault: runcontext.Defaults.CriticDefault, CriticReasoningFloor: runcontext.Defaults.CriticReasoningFloor, AdversarialReview: runcontext.Defaults.AdversarialReview}
	mode, _ := runcontext.CriticMode(runcontext.CriticSettings{Critic: run.Critic, CriticProject: run.CriticProject, AdversarialReview: run.Review}, table)
	if runcontext.CriticForcedOff(forced) != "" || mode == "off" {
		return 0
	}
	return 1
}

// allowance is one response's shared limits (ResponseAllowance): its model requests, owned by the
// parent run's execution once that run is attached; and its tool calls.
type allowance struct {
	mu        sync.Mutex
	parent    *runcontext.SessionContext
	usedHere  int
	limitHere int
	toolCalls int
	granted   map[string]*grant
}

type grant struct{ given, spent int }

// used and remaining take a.mu; the Locked forms assume it is held.
func (a *allowance) used() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.usedLocked()
}

func (a *allowance) usedLocked() int {
	if a.parent != nil {
		return a.parent.Execution.State().Used
	}
	return a.usedHere
}

func (a *allowance) limitLocked() int {
	if a.parent != nil {
		return a.parent.Execution.State().Limit
	}
	return a.limitHere
}

func (a *allowance) remaining() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.remainingLocked()
}

func (a *allowance) remainingLocked() int { return a.limitLocked() - a.usedLocked() }

// grantFor is the requests a child about to run may make: what the response can still give
// children, split among the children that run at once, never past its own budget.
func (a *allowance) grantFor(task *contract.DelegatedTask, spent int64, budget turn.TaskBudget, slots int) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	held, others := 0, 0
	for id, given := range a.granted {
		if id != task.ID {
			if left := given.given - given.spent; left > 0 {
				held += left
			}
			others++
		}
	}
	free := a.remainingLocked() - synthesisReserve - held
	share := slots - others
	if share < 1 {
		share = 1
	}
	given := int(budget.ModelRequests) - int(spent)
	if part := floorDiv(free, share); part < given {
		given = part
	}
	if given < 0 {
		given = 0
	}
	if a.granted == nil {
		a.granted = map[string]*grant{}
	}
	a.granted[task.ID] = &grant{given: given}
	return given
}

func floorDiv(a, b int) int {
	if a < 0 && a%b != 0 {
		return a/b - 1
	}
	return a / b
}

func (a *allowance) release(task *contract.DelegatedTask) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.granted, task.ID)
}

// childRequest charges a child's request to the response and its grant; refused where it would take
// the request kept for the parent.
func (a *allowance) childRequest(task *contract.DelegatedTask) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.remainingLocked() <= synthesisReserve {
		return runcontext.ErrExecutionPaused
	}
	if a.parent != nil {
		a.parent.Execution.MutateCheckpoint(runcontext.CheckpointPatch{"Used": runcontext.CheckpointIncrement(1)})
	} else {
		a.usedHere++
	}
	if given := a.granted[task.ID]; given != nil {
		given.spent++
	}
	return nil
}

// parentLink attaches the parent run's context to the response's allowance.
type parentLink struct{ c *coordinator }

func (l parentLink) Attach(sc *runcontext.SessionContext) {
	l.c.shared.mu.Lock()
	l.c.shared.parent = sc
	l.c.shared.mu.Unlock()
	sc.TaskDispatchCharge = func(ctx context.Context, kind, role string) error {
		if kind == "model" {
			l.c.batchMu.Lock()
			l.c.parentCount++
			l.c.batchMu.Unlock()
			return nil
		}
		if err := l.c.released(ctx); err != nil {
			return err
		}
		if err := l.c.admitRun(nil); err != nil {
			return err
		}
		l.c.emitMu.Lock()
		if l.c.handoff != nil && l.c.handoff.state == "answered" {
			l.c.handoff.state = "continued"
		}
		l.c.emitMu.Unlock()
		return nil
	}
	sc.Execution.ToolAllowance = func() *runcontext.ToolAllowance {
		l.c.shared.mu.Lock()
		defer l.c.shared.mu.Unlock()
		return &runcontext.ToolAllowance{Used: l.c.shared.toolCalls, Limit: turn.MaxTools, Scope: "response"}
	}
}

// parentSources is the source gate of the parent run's context, once it is attached; nil before, or
// where the parent declares no tool with a source.
func (c *coordinator) parentSources() *runcontext.SourceGate {
	c.shared.mu.Lock()
	defer c.shared.mu.Unlock()
	if c.shared.parent == nil {
		return nil
	}
	return c.shared.parent.Sources
}

// coordinator is one response's delegation.
type coordinator struct {
	inner       adapter.Adapter
	run         *adapter.Run
	profiles    []turn.TaskProfile
	parentRunID string
	shared      allowance
	policy      runcontext.Policy

	chat     *runcontext.ChatContext
	hostChat bool
	ledger   *runcontext.ContextLedger
	chatTurn *runcontext.ChatTurn

	deliveryMu       sync.Mutex
	storageContext   context.Context
	usageMu          sync.Mutex
	emitMu           sync.Mutex
	ended            bool
	parentCheckpoint *contract.Checkpoint
	fatal            error
	releaseFailure   error

	// The locks, taken in this order where more than one is held: shared.mu, batchMu, taskMu,
	// childMu. taskMu guards every task record's members once runBatch has published it (children
	// run at once and the parent's checkpoints read their figures); childMu guards the per-child maps
	// (models, refusedBy, childStarts, childStops, handedBack).
	taskActionMu sync.Mutex
	taskMu       sync.Mutex
	childMu      sync.Mutex
	batchMu      sync.Mutex
	batches      int
	extraBatch   bool
	figures      []contract.ProvenanceBatchFigures
	running      *runningBatch
	parentCount  int
	pending      []pendingReuse
	models       map[string]string
	refusedBy    map[string]string
	childStarts  map[string]time.Time
	childStops   map[string]context.CancelFunc
	handedBack   map[string]bool
	shownReports sync.Map

	// The agent capability's half (agent.go): the response's invocations, the account the
	// invocations run on one at a time, the handoff in progress, whether the parent's context is
	// open, and whether the parent's run reported its own failure.
	runsMu sync.Mutex
	runs   agentRuns
	// criticReserve is the invocation held back for the critic (ADR-0010; criticReserve), 0 or 1.
	criticReserve int
	account       *account
	handoff       *handoff
	parentOpen    bool
	parentFailed  bool
	requested     *contract.RunEventDelegationRequested
}

type runningBatch struct {
	tasks   []*contract.DelegatedTask
	number  int
	started time.Time
}

type pendingReuse struct {
	key  string
	kept runcontext.ReusableResult
}

// deliver hands one event to the host, in order; nothing after the end.
func (c *coordinator) deliver(event contract.RunEvent, committed ...bool) {
	c.deliveryMu.Lock()
	defer c.deliveryMu.Unlock()
	c.emitMu.Lock()
	ended := c.ended
	c.emitMu.Unlock()
	if ended {
		return
	}
	if c.run.Context != nil && c.run.Context.Persistence != nil && len(committed) == 0 {
		binding := c.run.Context.Persistence
		if !binding.Writer.Failed() {
			saved, err := binding.Writer.Event(c.storageContext, *binding, event)
			if err != nil {
				c.emitMu.Lock()
				if c.fatal == nil {
					c.fatal = err
				}
				c.emitMu.Unlock()
				event = failure.Event(err, "storage")
			} else {
				event = saved
			}
		}
	}
	c.run.Emit(contract.StampEvent(event))
}

// publish is one task's record as its delegated_task event.
func (c *coordinator) publish(task *contract.DelegatedTask) {
	c.taskMu.Lock()
	snapshot := *task
	snapshot.Steps = append([]contract.DelegatedTaskStepsItem{}, task.Steps...)
	c.taskMu.Unlock()
	event := contract.RunEvent{DelegatedTask: &contract.RunEventDelegatedTask{Type: "delegated_task", Task: snapshot, Subagent: task.Subagent, Provider: c.taskProvider(task.Profile)}}
	if c.run.Context != nil && c.run.Context.Persistence != nil && c.run.Context.Persistence.Writer.TaskStepsEnabled() {
		binding := c.run.Context.Persistence
		output, err := binding.Writer.CommitTaskResult(c.storageContext, *binding, event)
		if err != nil {
			c.emitMu.Lock()
			if c.fatal == nil {
				c.fatal = err
			}
			c.emitMu.Unlock()
			return
		}
		c.deliver(output, true)
	} else {
		c.deliver(event)
	}
}

// claimTool counts one tool call against the response and, for a child, its task.
func (c *coordinator) claimTool(task *contract.DelegatedTask, budget *turn.TaskBudget) error {
	c.shared.mu.Lock()
	defer c.shared.mu.Unlock()
	if c.shared.toolCalls >= turn.MaxTools {
		if task != nil {
			return failure.ToolInput("This response has made its " + itoa(turn.MaxTools) + " tool calls, the parent’s and its children’s together; this call was not made. Return the findings you have to the parent.")
		}
		return failure.ToolInput("This response has made its " + itoa(turn.MaxTools) + " tool calls, yours and your delegated children’s together (toolCalls in the execution state); this call was not made. Answer from the results you have.")
	}
	c.taskMu.Lock()
	defer c.taskMu.Unlock()
	if task != nil && budget != nil && task.ToolCalls >= budget.ToolCalls {
		return failure.ToolInput("This task has made its " + itoa(int(budget.ToolCalls)) + " tool calls, its profile’s budget; this call was not made. Return the findings you have to the parent.")
	}
	c.shared.toolCalls++
	if task != nil {
		task.ToolCalls++
	}
	return nil
}

// drive runs the parent with delegate_tasks offered while batches remain.
func (c *coordinator) drive(ctx context.Context) error {
	run := c.run
	if _, err := turn.ReadProfiles(c.profiles); err != nil {
		c.failAndEnd(err)
		return nil
	}
	for _, tool := range run.HostTools {
		if tool.Name == turn.DelegateTool {
			c.failAndEnd(failure.ToolInput("Delegation profiles and tool names must be unique."))
			return nil
		}
	}
	var configuration *runcontext.Configuration
	if run.Context != nil {
		configuration = run.Context.Configuration
		if chat, ok := run.Context.Chat.(*runcontext.ChatContext); ok && chat != nil {
			c.chat, c.hostChat = chat, true
		}
		if run.Context.Execution != nil {
			c.shared.usedHere = int(run.Context.Execution.Used)
			c.shared.limitHere = int(run.Context.Execution.Limit)
		}
	}
	if c.shared.limitHere == 0 {
		c.shared.limitHere = runcontext.MaxTurns
	}
	policy, err := runcontext.ContextPolicy(configuration, run.ModelName, nil)
	if err != nil {
		c.failAndEnd(err)
		return nil
	}
	c.policy = policy
	if c.chat == nil {
		c.chat = runcontext.NewChatContext("")
	}
	chatTurn, err := c.chat.OpenTurn()
	if err != nil {
		c.failAndEnd(err)
		return nil
	}
	c.chatTurn = chatTurn
	c.ledger = runcontext.NewContextLedger(&c.policy, chatTurn)
	defer func() {
		c.chatTurn.Release()
		if !c.hostChat {
			c.chat.Dispose()
		}
	}()
	if run.Agent != nil {
		c.driveAgent(ctx)
		return nil
	}

	if c.hasRestoredDefinitions() && run.Context.Execution.Delegation != nil {
		c.figures = append(c.figures, run.Context.Execution.Delegation.Batches...)
		for i := range c.figures {
			for j := range c.figures[i].Children {
				child := &c.figures[i].Children[j]
				if child.Subagent != nil && (child.State == "queued" || child.State == "running") {
					child.State = "interrupted"
					found := false
					for _, s := range run.Subagents {
						if s.ID == child.Subagent.ID && s.PinnedDigest == child.Subagent.Digest && runcontext.SubagentDigest(s) == s.PinnedDigest {
							found = true
						}
					}
					if !found {
						child.Problem = strPtr(runcontext.SubagentChanged)
						c.deliver(runcontext.SubagentNotice(child.Subagent.ID, *child.Problem, "subagent withheld"))
					}
				}
			}
		}
	}
	parent := *run
	offered := c.batchesLeft()
	if offered {
		parent.Prompt = run.Prompt + "\n\n" + turn.DelegationInstructions
		parent.HostTools = append(append([]runcontext.HostTool{}, c.wrapHostTools(run.HostTools, nil, nil)...), c.delegateTool())
		if run.Context != nil && run.Context.TaskSteps && run.Context.Persistence != nil {
			tool, err := c.taskDelegationTool(false)
			if err != nil {
				c.failAndEnd(err)
				return nil
			}
			parent.HostTools = append(parent.HostTools, tool)
		}
	} else {
		parent.Prompt = run.Prompt + "\n\n" + turn.DelegationSpent
		parent.HostTools = c.wrapHostTools(run.HostTools, nil, nil)
	}
	input := runcontext.Input{}
	if run.Context != nil {
		input = *run.Context
	}
	input.Chat = c.chat
	input.Link = parentLink{c: c}
	parent.Context = &input
	parent.Model = c.parentModel(run.Model)
	parent.BeforeRuntimeCall = func(context.Context, string) error { return c.claimTool(nil, nil) }
	parent.Emit = c.parentEmit
	runErr := c.inner.Run(ctx, &parent)
	if run.Context != nil && run.Context.Persistence != nil && run.Context.Persistence.Writer.Checkpoint() != nil {
		// agent#248: the invocation has closed its session and released its ledger; the
		// coordinator's events after it belong to the root the original writer acknowledged.
		if err := run.Context.Persistence.Writer.UseAcknowledgedRoot(*run.Context.Persistence); err != nil {
			c.emitMu.Lock()
			if c.fatal == nil {
				c.fatal = err
			}
			c.emitMu.Unlock()
		}
	}
	if c.fatal != nil {
		c.deliver(failure.Event(c.fatal, "desk"))
	} else if runErr != nil && !failure.IsCancelled(runErr) && ctx.Err() == nil {
		c.deliver(failure.Event(runErr, "desk"))
	}
	c.deliver(contract.RunEvent{End: &contract.RunEventEnd{Type: "end"}})
	c.emitMu.Lock()
	c.ended = true
	c.emitMu.Unlock()
	return nil
}

func (c *coordinator) failAndEnd(err error) {
	c.deliver(failure.Event(err, "desk"))
	c.deliver(contract.RunEvent{End: &contract.RunEventEnd{Type: "end"}})
}

// parentEmit is the parent run's events: its checkpoints carry its children's figures and its own
// (agent#18), its end waits for the coordinator's.
func (c *coordinator) parentEmit(event contract.RunEvent) {
	switch {
	case event.End != nil:
		return
	case event.DelegationRequested != nil:
		// The handoff (ADR-0007): the coordinator's, never the host's to see.
		held := *event.DelegationRequested
		c.emitMu.Lock()
		c.requested = &held
		c.emitMu.Unlock()
		return
	case event.Error != nil:
		c.emitMu.Lock()
		c.parentFailed = true
		c.emitMu.Unlock()
	case event.ExecutionCheckpoint != nil:
		held := *event.ExecutionCheckpoint
		if c.run.Context == nil || c.run.Context.Persistence == nil {
			held.Checkpoint = c.decorate(held.Checkpoint)
		}
		c.emitMu.Lock()
		handedOff := c.requested != nil
		c.emitMu.Unlock()
		if handedOff && (c.run.Context == nil || c.run.Context.Persistence == nil) {
			// The parent's invocation ended at its handoff, and the response goes on.
			held.Checkpoint, _, _ = runcontext.MutateCheckpoint(held.Checkpoint, runcontext.CheckpointPatch{"Status": "running"})
		}
		c.emitMu.Lock()
		state := held.Checkpoint
		c.parentCheckpoint = &state
		c.emitMu.Unlock()
		event = contract.RunEvent{ExecutionCheckpoint: &held}
	case event.BudgetPaused != nil:
		held := *event.BudgetPaused
		if c.run.Context == nil || c.run.Context.Persistence == nil {
			held.Checkpoint = c.decorate(held.Checkpoint)
		}
		event = contract.RunEvent{BudgetPaused: &held}
	}
	c.deliver(event, true)
}

func (c *coordinator) batchesLeft() bool {
	c.batchMu.Lock()
	defer c.batchMu.Unlock()
	limit := turn.MaxBatches
	if c.extraBatch {
		limit++
	}
	return c.batches < limit
}

// claimBatch is one more batch, or the call's failure once the response has run its last.
func (c *coordinator) claimBatch() error {
	c.batchMu.Lock()
	defer c.batchMu.Unlock()
	limit := turn.MaxBatches
	if c.extraBatch {
		limit++
	}
	if c.batches >= limit {
		return failure.ToolInput("This response has run its " + itoa(limit) + " delegation batches; no task was started. Answer from the findings you have.")
	}
	c.batches++
	return nil
}

// handedBackText is what a research child's source call that needs the parent's research choice
// returns as (HANDED_BACK, review row 7).
const handedBackText = "This source request needs the parent’s research choice. No source request was made."

func mustJSON(value any) json.RawMessage {
	encoded, _ := json.Marshal(value)
	return encoded
}

// edit changes a task record's members under taskMu.
func (c *coordinator) edit(change func()) {
	c.taskMu.Lock()
	defer c.taskMu.Unlock()
	change()
}

// spent is the model requests a task has made, read under taskMu.
func (c *coordinator) spent(task *contract.DelegatedTask) int64 {
	c.taskMu.Lock()
	defer c.taskMu.Unlock()
	return task.ModelRequests
}

// toolCalls is the counted tool calls a task has made, read under taskMu.
func (c *coordinator) toolCalls(task *contract.DelegatedTask) int64 {
	c.taskMu.Lock()
	defer c.taskMu.Unlock()
	return task.ToolCalls
}

func (c *coordinator) taskProvider(profile string) *contract.ProvenanceProvider {
	c.shared.mu.Lock()
	defer c.shared.mu.Unlock()
	if c.shared.parent != nil {
		return c.shared.parent.RecordProvider(delegatedRole(c.shared.parent.ResolvedPolicy, profile, c.shared.parent.Subagents))
	}
	id := c.run.ModelName
	if c.run.Agent != nil {
		id = c.run.Agent.Model
	}
	families := adapter.PolicyFamilies(c.run, runcontext.RunModels{})
	return &contract.ProvenanceProvider{Connection: id, Family: families[id]}
}
