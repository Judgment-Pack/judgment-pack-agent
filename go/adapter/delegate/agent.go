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

// The agent capability's half of delegation (ADR-0007; delegation.ts's drive on an agent
// capability, as the behavioural reference). The parent's delegate_tasks call ends its invocation:
// the agent engine reports delegation_requested with the call's id and returns once the host's
// function has returned, its account released. The coordinator then writes the handoff into the
// parent's checkpoint, acknowledged by the host's save before anything is dispatched; runs the
// batch's children one at a time, each only after every earlier invocation has returned, each
// counted and its record written before it is dispatched and after it finishes; writes the call's
// result; and starts a new invocation of the parent with that result as one data block (the
// continuation). A restored checkpoint carrying a handoff continues from where it stood: a child it
// shows dispatched is never dispatched again, and a restored continuation starts again, as any
// restored parent run does.

// agentRuns is one response's admitted invocations (MAX_AGENT_RUNS): the parent's (its critic's
// among them), each child's by task, and those of the batches a restored checkpoint carried. Guarded
// by runsMu, a leaf lock.
type agentRuns struct {
	parent   int
	children map[string]int
	earlier  int
}

func (r agentRuns) total() int {
	total := r.parent + r.earlier
	for _, n := range r.children {
		total += n
	}
	return total
}

func (c *coordinator) childRuns(id string) int {
	c.runsMu.Lock()
	defer c.runsMu.Unlock()
	return c.runs.children[id]
}

// mayDelegate is whether a parent invocation starting now may be offered delegate_tasks: it, one
// child and the next parent invocation must all remain.
func (c *coordinator) mayDelegate() bool {
	c.runsMu.Lock()
	defer c.runsMu.Unlock()
	return c.runs.total()+3+c.criticReserve <= turn.MaxAgentRuns
}

// mayAct is whether a parent invocation starting now may be offered an act call (ADR-0008): the
// invocation that continues after its pause must remain beside it.
func (c *coordinator) mayAct() bool {
	c.runsMu.Lock()
	defer c.runsMu.Unlock()
	return c.runs.total()+2 <= turn.MaxAgentRuns
}

// reserveContinuation holds a batch to the room it needs before it is admitted: one child and the
// invocation that continues the response with its result; under auto and on (ADR-0010) one invocation
// for each of its tasks, the continuation and the one held back for the critic.
func (c *coordinator) reserveContinuation(tasks int) error {
	c.runsMu.Lock()
	defer c.runsMu.Unlock()
	if c.criticReserve > 0 {
		if c.runs.total()+tasks+1+c.criticReserve > turn.MaxAgentRuns {
			return failure.ToolInput("This response has started " + itoa(c.runs.total()) + " of its " + itoa(turn.MaxAgentRuns) + " agent runs, and a batch of " + itoa(tasks) +
				" needs one for each child, one for the run that continues with its result and one kept for the critic; no task was started. Delegate fewer tasks, or answer from the findings you have.")
		}
		return nil
	}
	if c.runs.total()+2 > turn.MaxAgentRuns {
		return failure.ToolInput("This response has started " + itoa(c.runs.total()) + " of its " + itoa(turn.MaxAgentRuns) +
			" agent runs, and a batch needs one for a child and one for the run that continues with its result; no task was started. Answer from the findings you have.")
	}
	return nil
}

// admitRun counts one invocation where the response has room for it. A child never takes the
// last: the response continues after its batch. Nothing given back, through a child or a
// continuation.
func (c *coordinator) admitRun(task *contract.DelegatedTask) error {
	c.runsMu.Lock()
	defer c.runsMu.Unlock()
	limit := turn.MaxAgentRuns
	if task != nil {
		// A child never takes the continuation's invocation, nor under auto and on (ADR-0010) the
		// critic's.
		limit -= 1 + c.criticReserve
	}
	if c.runs.total() >= limit {
		return failure.ToolInput("The shared agent-invocation limit was reached. No additional run was sent.")
	}
	if task != nil {
		c.runs.children[task.ID]++
	} else {
		c.runs.parent++
	}
	return nil
}

// account is the provider account the invocations share, as the coordinator sees it: how many of
// its invocations have been dispatched and not yet returned. A child or a continuation is dispatched
// only once every earlier invocation has returned, its release confirmed.
type account struct {
	mu     sync.Mutex
	active int
	idle   chan struct{}
}

func newAccount() *account {
	idle := make(chan struct{})
	close(idle)
	return &account{idle: idle}
}

func (a *account) enter() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.active == 0 {
		a.idle = make(chan struct{})
	}
	a.active++
}

func (a *account) leave() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.active--
	if a.active == 0 {
		close(a.idle)
	}
}

// released waits until no invocation of this response holds the account.
func (a *account) released(ctx context.Context) error {
	a.mu.Lock()
	idle := a.idle
	a.mu.Unlock()
	select {
	case <-idle:
		return nil
	case <-ctx.Done():
		return failure.Cancelled
	}
}

// tracked is the host's function with each call held on the account until it returns, and a
// child's first event or tool call noted as its first byte.
func (c *coordinator) tracked(run adapter.AgentRun, m *meter) adapter.AgentRun {
	return func(ctx context.Context, request adapter.AgentRequest, callbacks adapter.AgentCallbacks) error {
		c.account.enter()
		defer c.account.leave()
		if m != nil {
			event, tool := callbacks.Event, callbacks.Tool
			callbacks.Event = func(ctx context.Context, held adapter.AgentEvent) error {
				m.mark()
				return event(ctx, held)
			}
			callbacks.Tool = func(ctx context.Context, call adapter.AgentToolCall) (adapter.AgentToolAnswer, error) {
				m.mark()
				return tool(ctx, call)
			}
		}
		return run(ctx, request, callbacks)
	}
}

// parentAgent is the parent's capability: the host's function tracked on the account, and each
// invocation counted and made durable before it is dispatched.
func (c *coordinator) parentAgent() *adapter.Agent {
	held := adapter.AdmissionAgent(c.run.Agent)
	held.Run = c.tracked(held.Run, nil)
	held.Begin = func(ctx context.Context, _ adapter.AgentRequest) error {
		if err := c.account.released(ctx); err != nil {
			return err
		}
		if err := c.admitRun(nil); err != nil {
			return err
		}
		c.emitMu.Lock()
		if c.handoff != nil && c.handoff.state == "answered" {
			c.handoff.state = "continued"
		}
		c.emitMu.Unlock()
		return c.durable(ctx)
	}
	return held
}

// childAgent is a child's capability: the model the host named for children, the host's function
// tracked on the account, and its invocation dispatched only after every earlier one has returned,
// counted under its task and made durable first.
func (c *coordinator) childAgent(task *contract.DelegatedTask, model string, m *meter, base *adapter.Agent, definition *runcontext.Subagent) *adapter.Agent {
	return &adapter.Agent{Family: base.Family, Tiers: base.Tiers, Model: base.Model, Enabled: base.Enabled, Delegated: model, Run: c.tracked(base.Run, m),
		Begin: func(ctx context.Context, _ adapter.AgentRequest) error {
			if err := c.account.released(ctx); err != nil {
				return err
			}
			if definition != nil && c.childRuns(task.ID) >= int(definition.Bounds.AgentRuns) {
				return failure.ToolInput("The declared sub-agent invocation bound was reached.")
			}
			if err := c.admitRun(task); err != nil {
				return err
			}
			c.edit(func() { task.Attempts++ })
			return c.accountChild(ctx, task, m)
		}}
}

// durable writes the parent's checkpoint and, where the host does not acknowledge it, makes the
// failure the response's: nothing more is dispatched.
func (c *coordinator) durable(ctx context.Context) error {
	if err := c.save(ctx); err != nil {
		if !failure.IsCancelled(err) {
			c.emitMu.Lock()
			if c.fatal == nil {
				c.fatal = err
			}
			c.emitMu.Unlock()
		}
		return err
	}
	return nil
}

// save is the parent's checkpoint as it stands, with the figures and the handoff, given to the
// host's durable callback (its acknowledgement awaited) and delivered as a durable
// execution_checkpoint. A run the host does not track has nothing to save.
func (c *coordinator) save(ctx context.Context) error {
	c.usageMu.Lock()
	defer c.usageMu.Unlock()
	var state contract.Checkpoint
	c.shared.mu.Lock()
	live := c.shared.parent
	c.shared.mu.Unlock()
	c.emitMu.Lock()
	open := c.parentOpen
	last := c.parentCheckpoint
	c.emitMu.Unlock()
	switch {
	case open && live != nil && c.isTracked():
		c.shared.mu.Lock()
		held, err := live.Snapshot("")
		c.shared.mu.Unlock()
		if err != nil {
			return err
		}
		state = held
	case last != nil:
		state = *last
		// The parent's invocation has returned and its children run: their source calls are in the
		// parent's rows (ADR-0006), which the last checkpoint, a copy, may not hold yet.
		if live != nil {
			c.shared.mu.Lock()
			state, _, _ = runcontext.MutateCheckpoint(state, runcontext.CheckpointPatch{"Sources": live.Sources.Rows(state.Sources)})
			usage := live.Execution.Usage()
			state, _, _ = runcontext.MutateCheckpoint(state, runcontext.CheckpointPatch{"Usage": &usage})
			state, _, _ = runcontext.MutateCheckpoint(state, runcontext.CheckpointPatch{"ChildUsage": live.Execution.State().ChildUsage})
			if live.Output != nil {
				state, _, _ = runcontext.MutateCheckpoint(state, runcontext.CheckpointPatch{"OutputGuard": live.Output.Counts()})
			}
			c.shared.mu.Unlock()
		}
	default:
		return nil
	}
	state, _, _ = runcontext.MutateCheckpoint(state, runcontext.CheckpointPatch{"Status": "running"})
	state = c.decorate(state)
	if c.run.Context != nil && c.run.Context.Checkpoint != nil {
		if err := c.run.Context.Checkpoint(ctx, state); err != nil {
			if failure.IsCancelled(err) || ctx.Err() != nil {
				return failure.Cancelled
			}
			return failure.Err("storage", "storage", "not-started")
		}
	}
	c.emitMu.Lock()
	c.parentCheckpoint = &state
	c.emitMu.Unlock()
	durable := true
	c.deliver(contract.RunEvent{ExecutionCheckpoint: &contract.RunEventExecutionCheckpoint{Type: "execution_checkpoint", Durable: &durable, Checkpoint: state}})
	return nil
}

// isTracked is whether the host tracks this response: it gave a current request or a saved execution.
func (c *coordinator) isTracked() bool {
	return c.run.Context != nil && (c.run.Context.CurrentRequest != nil || c.run.Context.Execution != nil)
}

// handoff is the delegation handoff in progress (checkpoint.json#/$defs/handoff), its tasks the live
// records. Guarded by emitMu; the records by taskMu.
type handoff struct {
	callID  string
	batch   int
	state   string
	request contract.DelegatedTaskRequest
	tasks   []*contract.DelegatedTask
	answer  *contract.DelegatedTaskAnswer
}

// handoffRecord is the handoff as the checkpoint carries it, or nil.
func (c *coordinator) handoffRecord() *contract.CheckpointHandoff {
	c.emitMu.Lock()
	held := c.handoff
	var out *contract.CheckpointHandoff
	if held != nil {
		out = &contract.CheckpointHandoff{CallID: held.callID, Batch: int64(held.batch), State: held.state, Request: held.request,
			Tasks: []contract.DelegatedTask{}, Answer: held.answer}
	}
	tasks := []*contract.DelegatedTask(nil)
	if held != nil {
		tasks = held.tasks
	}
	c.emitMu.Unlock()
	if out == nil {
		return nil
	}
	c.taskMu.Lock()
	for _, task := range tasks {
		snapshot := *task
		snapshot.Steps = append([]contract.DelegatedTaskStepsItem{}, task.Steps...)
		out.Tasks = append(out.Tasks, snapshot)
	}
	c.taskMu.Unlock()
	return out
}

func (c *coordinator) setHandoff(held *handoff) {
	c.emitMu.Lock()
	c.handoff = held
	c.emitMu.Unlock()
}

// restore reads a checkpoint the host handed back: the counts and figures it carries, and the
// handoff it stood at, where it stood at one.
func (c *coordinator) restore(saved *contract.Checkpoint) *handoff {
	held := *saved
	c.emitMu.Lock()
	c.parentCheckpoint = &held
	c.emitMu.Unlock()
	var pending *handoff
	if record := saved.Handoff; record != nil {
		pending = &handoff{callID: record.CallID, batch: int(record.Batch), state: record.State, request: record.Request, answer: record.Answer}
		for at := range record.Tasks {
			task := record.Tasks[at]
			pending.tasks = append(pending.tasks, &task)
		}
	}
	report := saved.Delegation
	if report == nil {
		return pending
	}
	c.runsMu.Lock()
	if report.Parent.AgentRuns != nil {
		c.runs.parent = int(*report.Parent.AgentRuns)
	}
	c.runsMu.Unlock()
	open := pending != nil && (pending.state == "released" || pending.state == "children")
	c.batchMu.Lock()
	for _, batch := range report.Batches {
		runs := 0
		for _, child := range batch.Children {
			if child.AgentRuns != nil {
				runs += int(*child.AgentRuns)
			}
		}
		if open && int(batch.Batch) == pending.batch {
			c.runsMu.Lock()
			for _, child := range batch.Children {
				if child.AgentRuns != nil {
					c.runs.children[child.Task] += int(*child.AgentRuns)
				}
			}
			c.runsMu.Unlock()
			continue
		}
		c.figures = append(c.figures, withoutZeroRequests(batch))
		c.runsMu.Lock()
		c.runs.earlier += runs
		c.runsMu.Unlock()
	}
	c.batches = len(c.figures)
	c.batchMu.Unlock()
	return pending
}

// withoutZeroRequests is a restored batch's figures without a modelRequests of 0. A v0.4.0
// checkpoint on an agent capability wrote 0 on its children and its total; nothing writes it there
// now (agent#71), so the restored batch does not mix 0 and absence. The batch is copied: the
// checkpoint the host handed back is left as it was.
func withoutZeroRequests(batch contract.ProvenanceBatchFigures) contract.ProvenanceBatchFigures {
	zero := func(held *int64) bool { return held != nil && *held == 0 }
	children := make([]contract.ProvenanceChildFigures, len(batch.Children))
	copy(children, batch.Children)
	for at := range children {
		if zero(children[at].ModelRequests) {
			children[at].ModelRequests = nil
		}
	}
	batch.Children = children
	if zero(batch.Total.ModelRequests) {
		batch.Total.ModelRequests = nil
	}
	return batch
}

// driveAgent is the response on an agent capability: parent invocations, each ending at its answer,
// a question or a handoff, and between them the batch each handoff asked for.
func (c *coordinator) driveAgent(ctx context.Context) {
	run := c.run
	var execution *contract.Checkpoint
	if run.Context != nil {
		execution = run.Context.Execution
	}
	var pending *handoff
	if execution != nil {
		pending = c.restore(execution)
		c.setHandoff(pending)
	}
	continued := ""
	defer func() {
		c.deliver(contract.RunEvent{End: &contract.RunEventEnd{Type: "end"}})
		c.emitMu.Lock()
		c.ended = true
		c.emitMu.Unlock()
	}()
	fail := func(err error) {
		c.emitMu.Lock()
		reported := c.parentFailed
		c.emitMu.Unlock()
		if !reported && !failure.IsCancelled(err) && ctx.Err() == nil {
			c.deliver(failure.Event(err, "desk"))
		}
	}
	for {
		if pending != nil && (pending.state == "released" || pending.state == "children") {
			answer, err := c.agentBatch(ctx, pending)
			if ctx.Err() != nil {
				return
			}
			if fatal := c.fatalError(); fatal != nil {
				fail(fatal)
				return
			}
			if err != nil {
				// Refused before any child was dispatched: the refusal is the call's result, and the
				// handoff is over.
				c.setHandoff(nil)
				pending = nil
				text, _ := failure.ToolFailure(err)
				continued = "\n\n" + turn.Continuation + "\n" + runcontext.FrameData(turn.DelegateTool, string(runcontext.String(text).JSON()), runcontext.DataMarker())
			} else {
				c.emitMu.Lock()
				pending.answer = &answer
				pending.state = "answered"
				c.emitMu.Unlock()
				if err := c.durable(ctx); err != nil {
					fail(err)
					return
				}
			}
			c.emitMu.Lock()
			last := c.parentCheckpoint
			c.emitMu.Unlock()
			execution = last
		}
		if pending != nil && pending.answer != nil {
			encoded, _ := json.Marshal(pending.answer)
			continued = "\n\n" + turn.Continuation + "\n" + runcontext.FrameData(turn.DelegateTool, string(encoded), runcontext.DataMarker())
		}
		request, callID, ended := c.parentInvocation(ctx, execution, continued)
		if ended {
			return
		}
		pending = &handoff{callID: callID, batch: c.nextBatch(), state: "released", request: request, tasks: []*contract.DelegatedTask{}}
		c.setHandoff(pending)
		if err := c.durable(ctx); err != nil {
			fail(err)
			return
		}
	}
}

// parentInvocation is one run of the parent: offered delegate_tasks while a batch and the
// invocations it needs remain. It answers the handoff's request and call id, or ended where the
// response is over: answered, waiting for the person, stopped or failed.
func (c *coordinator) parentInvocation(ctx context.Context, execution *contract.Checkpoint, continued string) (contract.DelegatedTaskRequest, string, bool) {
	run := c.run
	offered := c.batchesLeft() && c.mayDelegate()
	parent := *run
	// ADR-0008: an act call pauses the response and the next counted invocation continues it, so an
	// act is offered only while that invocation remains, as delegate_tasks is.
	hostTools := run.HostTools
	if !c.mayAct() {
		hostTools = nil
		for _, tool := range run.HostTools {
			if !runcontext.AsksApproval(tool) {
				hostTools = append(hostTools, tool)
			}
		}
	}
	if offered {
		parent.Prompt = run.Prompt + "\n\n" + turn.DelegationInstructions + " " + turn.AgentDelegationInstructions + continued
		parent.HostTools = append(append([]runcontext.HostTool{}, c.wrapHostTools(hostTools, nil, nil)...), c.handoffTool())
	} else {
		parent.Prompt = run.Prompt + "\n\n" + turn.DelegationSpent + continued
		parent.HostTools = c.wrapHostTools(hostTools, nil, nil)
	}
	input := runcontext.Input{}
	if run.Context != nil {
		input = *run.Context
	}
	input.Chat = c.chat
	input.Link = agentLink{c: c}
	input.Execution = execution
	if run.Context != nil && run.Context.Checkpoint != nil {
		save := run.Context.Checkpoint
		input.Checkpoint = func(ctx context.Context, state contract.Checkpoint) error {
			state = c.decorate(state)
			if err := save(ctx, state); err != nil {
				return err
			}
			c.emitMu.Lock()
			c.parentCheckpoint = &state
			c.emitMu.Unlock()
			return nil
		}
	}
	parent.Context = &input
	parent.Model = nil
	parent.Agent = c.parentAgent()
	parent.BeforeRuntimeCall = func(context.Context, string) error { return c.claimTool(nil, nil) }
	parent.Emit = c.parentEmit
	c.emitMu.Lock()
	c.requested, c.parentFailed = nil, false
	c.emitMu.Unlock()
	_ = c.inner.Run(ctx, &parent)
	c.emitMu.Lock()
	c.parentOpen = false
	requested, failed := c.requested, c.parentFailed
	c.emitMu.Unlock()
	if ctx.Err() != nil || failed {
		return contract.DelegatedTaskRequest{}, "", true
	}
	if fatal := c.fatalError(); fatal != nil {
		c.deliver(failure.Event(fatal, "desk"))
		return contract.DelegatedTaskRequest{}, "", true
	}
	if requested == nil {
		return contract.DelegatedTaskRequest{}, "", true
	}
	callID := newID()
	if requested.CallID != nil && *requested.CallID != "" {
		callID = *requested.CallID
	}
	return requested.Request, callID, false
}

// agentLink attaches the parent's context to the response's allowance and marks it open, so a
// checkpoint written while its invocation runs is its live state.
type agentLink struct{ c *coordinator }

func (l agentLink) Attach(context *runcontext.SessionContext) {
	parentLink(l).Attach(context)
	// ADR-0010: the critic has room where the response has an invocation left for it.
	context.CriticRoom = func() bool {
		l.c.runsMu.Lock()
		defer l.c.runsMu.Unlock()
		return l.c.runs.total() < turn.MaxAgentRuns
	}
	l.c.emitMu.Lock()
	l.c.parentOpen = true
	l.c.emitMu.Unlock()
}

// handoffTool is delegate_tasks on an agent capability: the engine answers its call with a handoff
// and never runs it. A call that reached it would run the batch inside the parent's invocation, on
// the account the parent holds, so it is refused.
func (c *coordinator) handoffTool() runcontext.HostTool {
	declared := turn.DelegationTool(c.profiles)
	tool := runcontext.HostTool{Name: declared.Name, Description: declared.Description, InputSchema: declared.InputSchema, Interaction: "delegation"}
	tool.Execute = func(context.Context, json.RawMessage) (runcontext.ToolResult, error) {
		return runcontext.ToolResult{}, failure.Authored("configuration", "delegate_tasks ends the parent's invocation on an agent capability; it is never awaited inside it. No task was started.", "desk")
	}
	return tool
}

func (c *coordinator) fatalError() error {
	c.emitMu.Lock()
	defer c.emitMu.Unlock()
	return c.fatal
}

// nextBatch is the number the next batch of this response takes.
func (c *coordinator) nextBatch() int {
	c.batchMu.Lock()
	defer c.batchMu.Unlock()
	if len(c.figures) > 0 {
		return int(c.figures[len(c.figures)-1].Batch) + 1
	}
	return 1
}

// uncertainProblem is the record of a child a restored checkpoint shows dispatched and not finished.
const uncertainProblem = "This task's run was dispatched before the response stopped, and its outcome is unknown; it was not run again."

// agentBatch is the batch a handoff asked for: planned and its records written (acknowledged)
// before any child is dispatched, then its children one at a time, each record written after it
// finishes. A restored handoff takes its records as they stood: a queued child runs, a running one
// is interrupted with its outcome unknown and never dispatched again, a finished one is kept.
func (c *coordinator) agentBatch(ctx context.Context, pending *handoff) (contract.DelegatedTaskAnswer, error) {
	if err := ctx.Err(); err != nil {
		return contract.DelegatedTaskAnswer{}, failure.Cancelled
	}
	var plans []*planned
	tasks := pending.tasks
	if pending.state == "released" {
		for _, item := range pending.request.Tasks {
			p, err := c.plan(item)
			if err != nil {
				return contract.DelegatedTaskAnswer{}, err
			}
			plans = append(plans, p)
		}
		if err := c.reserveContinuation(len(plans)); err != nil {
			return contract.DelegatedTaskAnswer{}, err
		}
		if err := c.claimBatch(); err != nil {
			return contract.DelegatedTaskAnswer{}, err
		}
		tasks = c.recordTasks(plans)
		c.emitMu.Lock()
		pending.tasks, pending.state = tasks, "children"
		c.emitMu.Unlock()
		if err := c.durable(ctx); err != nil {
			return contract.DelegatedTaskAnswer{}, err
		}
	} else {
		c.batchMu.Lock()
		c.batches++
		c.batchMu.Unlock()
		for at, task := range tasks {
			var p *planned
			if at < len(pending.request.Tasks) && task.State == "queued" {
				held, err := c.plan(pending.request.Tasks[at])
				if err != nil {
					message := failure.Message(failure.Normalize(err, "desk", time.Now()))
					c.edit(func() {
						task.State = "failed"
						task.Problem = strPtr(runcontext.U16Slice(message, 0, 1000))
						task.FinishedAt = strPtr(now())
					})
				}
				p = held
				if task.Subagent != nil && (p == nil || p.subagent == nil || p.subagent.Digest == nil || *p.subagent.Digest != task.Subagent.Digest || task.Profile != p.subagent.ID || string(mustJSON(task.Subagent)) != string(mustJSON(runcontext.SubagentRecord(*p.subagent)))) {
					c.edit(func() {
						task.State = "interrupted"
						task.Problem = strPtr(runcontext.SubagentChanged)
						task.FinishedAt = strPtr(now())
					})
					c.deliver(runcontext.SubagentNotice(task.Subagent.ID, *task.Problem, "subagent withheld"))
				}
			}
			if task.State == "running" {
				c.edit(func() {
					task.State = "interrupted"
					task.Problem = strPtr(uncertainProblem)
					task.Failure = failureOf("unknown", "agent", "unknown")
					task.FinishedAt = strPtr(now())
				})
			}
			if task.Report != nil {
				c.shownReports.Store(task.ID, shownReport{shown: reportValue(task.Report)})
			}
			c.publish(task)
			plans = append(plans, p)
		}
		if err := c.durable(ctx); err != nil {
			return contract.DelegatedTaskAnswer{}, err
		}
	}
	number := pending.batch
	started := time.Now()
	c.batchMu.Lock()
	c.pending = nil
	c.running = &runningBatch{tasks: tasks, number: number, started: started}
	c.batchMu.Unlock()
	beside := 0
	for _, task := range tasks {
		beside += taskBytes + runcontext.UTF8Bytes(string(runcontext.String(task.Objective).JSON()))
		references := runcontext.Array()
		for _, id := range task.References {
			references.Append(runcontext.String(id))
		}
		beside += runcontext.UTF8Bytes(string(references.JSON()))
	}
	share := max((c.policy.ToolResultBytes-answerBytes-beside)/max(len(tasks), 1), 1000)
	var total time.Duration
	for _, p := range plans {
		if p != nil {
			total += time.Duration(p.budget.Milliseconds) * time.Millisecond
		}
	}
	batchCtx, stop := context.WithTimeout(ctx, max(total, time.Millisecond))
	defer stop()
	batch := &batchState{ctx: batchCtx, parent: ctx, kept: map[string]runcontext.ReusableResult{}, slots: func() int { return 1 }}
	for at, task := range tasks {
		if batchCtx.Err() != nil || c.fatalError() != nil {
			break
		}
		c.taskMu.Lock()
		queued := task.State == "queued"
		c.taskMu.Unlock()
		if !queued || plans[at] == nil {
			continue
		}
		key := ""
		if plans[at].request.Resume == nil && plans[at].profile != nil && task.Model != nil {
			key = reuseKey(plans[at], *task.Model, c.taskProvider(plans[at].request.Profile))
		}
		c.child(plans[at], task, batch, share, key)
		if c.fatalError() != nil || ctx.Err() != nil {
			break
		}
		if err := c.durable(ctx); err != nil {
			break
		}
	}
	var children []contract.ProvenanceChildFigures
	c.taskMu.Lock()
	for _, task := range tasks {
		children = append(children, c.childFigures(task, number))
	}
	c.taskMu.Unlock()
	c.batchMu.Lock()
	c.figures = append(c.figures, contract.ProvenanceBatchFigures{Batch: int64(number), Children: children, Total: totalOf(children, ms(time.Since(started)))})
	c.running = nil
	for _, task := range tasks {
		if report, ok := c.shownReports.Load(task.ID); ok && len(report.(shownReport).shown.Get("openQuestions").Items()) > 0 {
			c.extraBatch = true
		}
	}
	reuse := c.pending
	c.pending = nil
	c.batchMu.Unlock()
	if ctx.Err() != nil {
		return contract.DelegatedTaskAnswer{}, failure.Cancelled
	}
	if c.fatalError() != nil {
		return contract.DelegatedTaskAnswer{}, c.fatalError()
	}
	for _, held := range reuse {
		_ = c.chat.KeepResult(held.key, held.kept)
	}
	answered := c.batchAnswer(tasks)
	var answer contract.DelegatedTaskAnswer
	if err := json.Unmarshal(answered.StructuredContent.JSON(), &answer); err != nil {
		return contract.DelegatedTaskAnswer{}, err
	}
	return answer, nil
}

// reportValue is a task's kept report as the value its answer shows.
func reportValue(report *contract.DelegatedTaskReport) *runcontext.Value {
	encoded, err := json.Marshal(report)
	if err != nil {
		return runcontext.Object()
	}
	value, err := runcontext.ParseJSON(encoded)
	if err != nil {
		return runcontext.Object()
	}
	return value
}
