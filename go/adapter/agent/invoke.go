package agent

import (
	"context"
	"encoding/json"
	"errors"
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

// invocation is what one invocation's events leave: the text of each item still streaming, the
// messages finished, and the final answer.
type invocation struct {
	mu       sync.Mutex
	partial  map[string]string
	messages int
	final    string
}

// MaxAdmissionAttempts is how many times one invocation is sent where its account refused it at
// admission as busy (canRetryAdmission): the first and two more, after 250 ms and 500 ms.
const MaxAdmissionAttempts = 3

// invoke is one invocation: its tools, its instructions and prompt by the one assembly, the
// admission, the coordinator's count and durable checkpoint (or the run's own save) before
// dispatch, then the host's function, sent again only after a busy refusal at admission. It answers
// the final message, or why there is none.
func (r *runState) invoke(ctx context.Context, prompt, instructions, phase string, recorder *turn.Recorder, model, state string) (string, error) {
	r.invocation++
	steers, notices := r.sc.Execution.TakeSteers(r.sc.Marker, r.invocation, *r.sc.Policy)
	for _, notice := range notices {
		r.deliver(notice)
	}
	if steers != "" {
		if phase == "author" {
			if state != "" {
				state += "\n\n"
			}
			state += steers
		} else {
			prompt += "\n\n" + steers
		}
	}
	tools := r.offeredTo(phase)
	offered := map[string]bool{}
	for _, tool := range tools {
		offered[tool.Name] = true
	}
	if phase == "reformat" {
		r.run.Gate.BeginReformat()
		defer r.run.Gate.EndReformat()
		r.sc.Reformatting = true
		defer func() { r.sc.Reformatting = false }()
	}
	// ADR-0008: while an act call waits for the person's decision, no invocation is dispatched.
	// Defence in depth: drive does not call invoke in that state (the pause cancels the run's
	// execution before a reformat or a critic, and a continuation of an asked approval without a
	// decision is refused before the run opens); guard_test.go holds the line directly.
	if r.sc.Execution.ApprovalAsked() {
		return "", runcontext.ErrApprovalPaused
	}
	instructions = r.sc.System(instructions+runcontext.DataRule+r.run.LanguageInstructions, "")
	encoded, _ := json.Marshal(tools)
	shown, err := runcontext.ParseJSON(encoded)
	if err != nil {
		return "", err
	}
	if phase == "author" {
		stated := ""
		if state != "" {
			stated = "\n\n" + runcontext.StateHeader + "\n" + state
		}
		var current *runcontext.CurrentRequest
		if r.run.Context != nil {
			current = r.run.Context.CurrentRequest
		}
		parts, err := r.sc.Opening(prompt, instructions+stated, shown, current)
		if err != nil {
			return "", err
		}
		prompt = strings.Join(parts, "\n\n") + stated
	} else if prompt, err = r.sc.Prompt(prompt, instructions, shown, nil); err != nil {
		return "", err
	}
	if err := r.sc.Check(instructions, prompt, shown); err != nil {
		return "", err
	}
	wirePhase := "author"
	if phase == "critic" {
		wirePhase = "critic"
	}
	agent := r.run.Agent
	effort := agent.Effort
	tierMap := agent.Tiers
	admit := agent.Admit
	if phase == "critic" {
		// ADR-0010: the critic's effort is the decision's tier, the author's unless the host named one.
		effort = r.criticEffort
		named, bindErr := r.run.Models()
		if bindErr != nil {
			return "", bindErr
		}
		binding, bindErr := adapter.PolicyBinding(r.run, r.sc.ResolvedPolicy, named, "critic")
		if bindErr != nil {
			return "", bindErr
		}
		if binding.Agent != nil {
			tierMap = binding.Agent.Tiers
			admit = binding.Agent.Admit
		}
	}
	if setting := tierMap[effort]; setting != "" {
		effort = setting
	}
	request := adapter.AgentRequest{Prompt: prompt, Instructions: instructions, Tools: tools, Effort: effort, Phase: wirePhase, Model: model}
	if agent.Delegated != "" && agent.Delegated != agent.Model {
		request.Model, request.Delegated = agent.Delegated, true
	}
	request, err = admit(ctx, request)
	if err != nil {
		return "", err
	}
	if body, err := json.Marshal(request); err == nil {
		r.sc.Execution.RecordPrompt(body)
	}
	// Durable before dispatch: the coordinator counts the invocation and writes the checkpoint; a run
	// it did not start writes its own. A checkpoint not acknowledged dispatches nothing.
	if agent.Begin != nil {
		err = agent.Begin(ctx, request)
	} else {
		err = r.save(ctx)
	}
	if err != nil {
		return "", err
	}
	invCtx, stop := context.WithCancel(ctx)
	defer stop()
	held := &invocation{partial: map[string]string{}}
	// Every callback of the invocation is held: whether one ran (the invocation was admitted), and
	// that none is still running when invoke returns, so a host tool cannot overlap what follows.
	watched := &callbacksHeld{}
	watched.idle = sync.NewCond(&watched.mu)
	defer watched.closeAndWait()
	callbacks := watched.wrap(adapter.AgentCallbacks{Event: r.event(invCtx, phase, held), Tool: r.tool(invCtx, offered, recorder)})
	var runErr error
	for attempt := 1; ; attempt++ {
		runErr = agent.Run(invCtx, request, callbacks)
		watched.wait()
		// An attempt that reached any callback was admitted: whatever its error says, it is an
		// outcome and is never sent again.
		if runErr == nil || watched.reached() || r.stopping() || invCtx.Err() != nil ||
			!failure.CanRetryAdmission(failure.Normalize(runErr, "agent", time.Now()), attempt, MaxAdmissionAttempts) {
			break
		}
		// Busy, and no callback ran: admission refused the invocation, nothing ran, nothing is replayed.
		select {
		case <-time.After(time.Duration(attempt) * 250 * time.Millisecond):
		case <-invCtx.Done():
		}
		if invCtx.Err() != nil {
			break
		}
	}
	r.held.Lock()
	fatal, handoff, waiting := r.fatal, r.handoff, r.awaitingInput
	r.held.Unlock()
	switch {
	case fatal != nil:
		return "", fatal
	case handoff:
		return "", errHandoff
	case waiting:
		return "", errWaiting
	case ctx.Err() != nil:
		return "", failure.Cancelled
	case runErr != nil:
		if failure.IsCancelled(runErr) && invCtx.Err() != nil {
			return "", failure.Cancelled
		}
		return "", runErr
	}
	held.mu.Lock()
	defer held.mu.Unlock()
	r.held.Lock()
	proposed := r.toolProposal != nil
	r.held.Unlock()
	if (held.messages == 0 || len(held.partial) > 0 || strings.TrimSpace(held.final) == "") && !(phase == "author" && proposed) {
		return "", failure.Err("invalid-response", "agent", "unknown")
	}
	return held.final, nil
}

// callbacksHeld is one invocation's callbacks, held: whether any ran, how many are running, and,
// once the invocation is over, a refusal of any that comes late.
type callbacksHeld struct {
	mu      sync.Mutex
	idle    *sync.Cond
	active  int
	touched bool
	closed  bool
}

// errLate answers a callback the host makes after its invocation was over.
var errLate = errors.New("the invocation is over; this callback was not run")

func (h *callbacksHeld) enter() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return false
	}
	h.active++
	h.touched = true
	return true
}

func (h *callbacksHeld) leave() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.active--
	if h.active == 0 {
		h.idle.Broadcast()
	}
}

// wait returns once no callback is running.
func (h *callbacksHeld) wait() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for h.active > 0 {
		h.idle.Wait()
	}
}

func (h *callbacksHeld) reached() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.touched
}

// closeAndWait refuses every later callback and waits for the running ones.
func (h *callbacksHeld) closeAndWait() {
	h.mu.Lock()
	h.closed = true
	h.mu.Unlock()
	h.wait()
}

func (h *callbacksHeld) wrap(callbacks adapter.AgentCallbacks) adapter.AgentCallbacks {
	return adapter.AgentCallbacks{
		Event: func(ctx context.Context, event adapter.AgentEvent) error {
			if !h.enter() {
				return errLate
			}
			defer h.leave()
			return callbacks.Event(ctx, event)
		},
		Tool: func(ctx context.Context, call adapter.AgentToolCall) (adapter.AgentToolAnswer, error) {
			if !h.enter() {
				return adapter.AgentToolAnswer{}, errLate
			}
			defer h.leave()
			return callbacks.Tool(ctx, call)
		},
	}
}

// stopping is whether a tool call ended the invocation: a handoff, a question or a failed save.
func (r *runState) stopping() bool {
	r.held.Lock()
	defer r.held.Unlock()
	return r.handoff || r.awaitingInput || r.fatal != nil
}

// save writes the run's checkpoint through the host's durable callback, where it gave one, and
// answers storage (not started) where the host did not acknowledge it.
func (r *runState) save(ctx context.Context) error {
	if r.run.Context == nil || r.run.Context.Checkpoint == nil {
		return nil
	}
	// Match deliver's lock order: r.mu, then the execution snapshot's criticMu.
	r.mu.Lock()
	state, err := r.sc.Snapshot("")
	r.mu.Unlock()
	if err != nil {
		return err
	}
	if err := r.run.Context.Checkpoint(ctx, state); err != nil {
		if failure.IsCancelled(err) || ctx.Err() != nil {
			return failure.Cancelled
		}
		return failure.Err("storage", "storage", "not-started")
	}
	return nil
}

// event is one invocation's events: the provider's recovery notice, the author's prose so far, its
// commentary as messages, and the final message kept.
func (r *runState) event(invCtx context.Context, phase string, held *invocation) func(context.Context, adapter.AgentEvent) error {
	return func(_ context.Context, event adapter.AgentEvent) error {
		if invCtx.Err() != nil {
			return nil
		}
		switch event.Type {
		case "retry":
			r.deliver(contract.RunEvent{Retry: &contract.RunEventRetry{Type: "retry", Attempt: int64(event.Attempt)}})
		case "text":
			held.mu.Lock()
			text := held.partial[event.ID] + event.Text
			held.partial[event.ID] = text
			held.mu.Unlock()
			if phase == "author" && r.run.Interactive {
				r.deliver(contract.RunEvent{MessageProgress: &contract.RunEventMessageProgress{Type: "message_progress", Text: turn.StreamingProse(text)}})
			}
		case "message":
			held.mu.Lock()
			delete(held.partial, event.ID)
			held.messages++
			if event.Phase != "commentary" {
				held.final = event.Text
			}
			held.mu.Unlock()
			if event.Phase == "commentary" && phase == "author" {
				if prose := turn.ProseOf(event.Text); prose != "" {
					r.deliver(messageEvent(prose))
				}
			}
		}
		return nil
	}
}

// tool is one invocation's tool calls: a name it was not offered refused, a host tool held to the
// phase by the gate first, a delegation a handoff, a question a pause, propose_pack read by the
// contract's reader, and every other call under the policy's deadline, its answer bounded and, from
// a reading tool, framed.
func (r *runState) tool(invCtx context.Context, offered map[string]bool, recorder *turn.Recorder) func(context.Context, adapter.AgentToolCall) (adapter.AgentToolAnswer, error) {
	return func(ctx context.Context, call adapter.AgentToolCall) (adapter.AgentToolAnswer, error) {
		if r.stopping() {
			return adapter.AgentToolAnswer{}, errWaiting
		}
		ctx, stop := joined(invCtx, ctx)
		defer stop()
		if ctx.Err() != nil {
			return adapter.AgentToolAnswer{}, failure.Cancelled
		}
		args := nonEmpty(call.Arguments)
		held, known := r.routes[call.Name]
		// ADR-0010: request_review reaches the switch's own tool only in the author's invocation of a
		// run it is offered to; anywhere else it is refused here, with the guardrail review refused,
		// before anything runs and before the gate, and nothing is recorded.
		editName := call.Name
		if known {
			editName = held.name
		}
		if runcontext.IsEdit(editName) && (!known || !offered[call.Name] || !r.sc.MemoryAdmitted()) {
			r.deliver(callEvent(call.ID, editName, args, ""))
			var input struct {
				Document string `json:"document"`
			}
			_ = json.Unmarshal(args, &input)
			err := r.sc.Drafts.Refuse(editName, input.Document)
			text := err.Error()
			r.deliver(resultEvent(call.ID, editName, textResult(text, true), ""))
			return adapter.AgentToolAnswer{Text: text, IsError: true}, nil
		}
		if (known && held.name == runcontext.Remember || !known && call.Name == runcontext.Remember) && (!known || !offered[call.Name] || !r.sc.MemoryAdmitted()) {
			r.deliver(callEvent(call.ID, runcontext.Remember, args, ""))
			text := gate.UnofferedRefusal(r.sc.RefuseMemory())
			r.deliver(resultEvent(call.ID, runcontext.Remember, textResult(text, true), ""))
			return adapter.AgentToolAnswer{Text: text, IsError: true}, nil
		}
		if (known && held.name == runcontext.RequestReview || !known && call.Name == runcontext.RequestReview) && (!known || !offered[call.Name] || !r.sc.ReviewAdmitted()) {
			return r.refuseReview(call, args)
		}
		if !known || !offered[call.Name] {
			return r.refuseUnoffered(ctx, call, held, known, args)
		}
		// ADR-0008: an act call the host's policy asks about ends the invocation as a delegation
		// handoff does: its approval_requested in place of the call, the call never run.
		if held.host != nil {
			// The pause is claimed before the approval is written asked (SessionContext.PauseClaim).
			if asked := r.sc.Approval(ctx, held.name, args, call.ID); asked != nil {
				r.deliver(*asked)
				r.pause()
				return adapter.AgentToolAnswer{}, errWaiting
			}
		}
		if held.host == nil || held.host.Interaction == "" {
			r.deliver(callEvent(call.ID, held.name, args, ""))
		}
		result, answered, err := r.answer(ctx, call, held, args)
		if err != nil {
			if errors.Is(err, errWaiting) || errors.Is(err, errHandoff) || r.fatalError() != nil {
				return adapter.AgentToolAnswer{}, err
			}
			if failure.IsCancelled(err) || ctx.Err() != nil {
				return adapter.AgentToolAnswer{}, failure.Cancelled
			}
			var violation *gate.Violation
			if held.host != nil && held.host.Interaction == "question" && !errors.As(err, &violation) {
				result = resultOf(turn.QuestionFailureResult(err))
			} else {
				result = failed(err)
			}
			answered = false
		}
		if r.stopping() {
			return adapter.AgentToolAnswer{}, errWaiting
		}
		if ctx.Err() != nil {
			return adapter.AgentToolAnswer{}, failure.Cancelled
		}
		r.deliver(resultEvent(call.ID, held.name, result, ""))
		if answered && held.host == nil && recorder != nil {
			recorder.Saw(held.name, textOfResult(result), args, structuredOf(result))
		}
		// The agent wire carries text only: an image is shown as a line saying it is not shown.
		separated, err := separate(ctx, held.name, result)
		if err != nil {
			return adapter.AgentToolAnswer{}, err
		}
		projected, err := r.sc.Result(held.name, separated)
		if err != nil {
			return adapter.AgentToolAnswer{}, err
		}
		text := textOfResult(projected)
		if projected.StructuredContent != nil {
			text = string(projected.JSON())
		}
		if answered && runcontext.ReadingTool(held.host) {
			text = r.sc.Frame(held.name, text)
		}
		return adapter.AgentToolAnswer{Text: text, IsError: result.Err()}, nil
	}
}

// refuseReview answers a request_review call the run does not admit (ADR-0010): its call, the
// switch's guardrail (put on the stream by the boundary before the result) and the refused result.
func (r *runState) refuseReview(call adapter.AgentToolCall, args json.RawMessage) (adapter.AgentToolAnswer, error) {
	r.deliver(callEvent(call.ID, runcontext.RequestReview, args, ""))
	text := gate.UnofferedRefusal(r.sc.RefuseReview())
	r.deliver(resultEvent(call.ID, runcontext.RequestReview, textResult(text, true), ""))
	return adapter.AgentToolAnswer{Text: text, IsError: true}, nil
}

// refuseUnoffered answers a call to a name this invocation was not offered, refused, and the
// invocation goes on. A name the session serves is refused here before any call; any other is put
// through the gate as the agent asked for it, so the layer that holds is the one that reports.
func (r *runState) refuseUnoffered(ctx context.Context, call adapter.AgentToolCall, held route, known bool, args json.RawMessage) (adapter.AgentToolAnswer, error) {
	name := call.Name
	if known {
		name = held.name
	}
	r.deliver(callEvent(call.ID, name, args, ""))
	if name == "delegate_tasks" && r.run.ChildDefinition != nil {
		r.deliver(runcontext.SubagentNotice(name, "A child cannot delegate.", "subagent refused"))
	}
	// ADR-0006: the Runtime's row of the run report counts the refusal, as the TypeScript boundary's
	// wrapped callTool and the eino adapter's own refusal do.
	if r.sc.Sources != nil {
		r.sc.Sources.RuntimeRefused()
	}
	var text string
	if known || r.served[name] {
		text = gate.UnofferedRefusal(gate.ServedNotOffered(name))
	} else {
		text = gate.UnofferedRefusal("the call did not leave the page and nothing was written")
		answered, err := r.run.Gate.CallTool(ctx, name, args)
		switch {
		case err != nil && (failure.IsCancelled(err) || ctx.Err() != nil):
			return adapter.AgentToolAnswer{}, failure.Cancelled
		case err != nil:
			text = gate.UnofferedRefusal(err.Error())
		case answered.IsError && strings.HasPrefix(answered.Text, "refused: "):
			text = answered.Text
		}
	}
	r.deliver(resultEvent(call.ID, name, textResult(text, true), ""))
	return adapter.AgentToolAnswer{Text: text, IsError: true}, nil
}

// answer is what one offered call answers, and whether the tool itself answered it.
func (r *runState) answer(ctx context.Context, call adapter.AgentToolCall, held route, args json.RawMessage) (runcontext.ToolResult, bool, error) {
	limit := time.Duration(r.sc.Policy.ToolCallMilliseconds) * time.Millisecond
	late := func() error { return turn.ToolTimeout(limit) }
	host := held.host
	if host == nil {
		result, err := turn.WithDeadline(ctx, limit, late, func(ctx context.Context) (runcontext.ToolResult, error) {
			return r.session.CallTool(ctx, held.name, args)
		})
		return result, err == nil, err
	}
	if refused := r.run.Gate.AdmitHost(held.name); refused != nil {
		return resultOf(*refused), false, nil
	}
	switch {
	case host.Interaction == "delegation":
		// The handoff (ADR-0007): the call's request and id go to the coordinator, the invocation is
		// stopped, and the run ends once the host's function has returned.
		request, err := turn.ReadDelegationRequest(args)
		if err != nil {
			return runcontext.ToolResult{}, false, err
		}
		if !r.claimPause(true) {
			return runcontext.ToolResult{}, false, errWaiting
		}
		runcontext.RecordSubagents(&request, r.sc.Subagents)
		var record *contract.SubagentRecord
		if len(request.Tasks) == 1 {
			record = request.Tasks[0].Subagent
		}
		id := call.ID
		r.deliver(contract.RunEvent{DelegationRequested: &contract.RunEventDelegationRequested{Type: "delegation_requested", Request: request, Subagent: record, CallID: &id}})
		r.pause()
		return runcontext.ToolResult{}, false, errHandoff
	case host.Interaction == "question":
		question, err := turn.ReadModelQuestion(args)
		if err != nil {
			return runcontext.ToolResult{}, false, err
		}
		if !r.claimPause(false) {
			return runcontext.ToolResult{}, false, errWaiting
		}
		r.deliver(contract.RunEvent{UserQuestion: &contract.RunEventUserQuestion{Type: "user_question", ID: wire.RandomUUID(), Question: question}})
		r.pause()
		return runcontext.ToolResult{}, false, errWaiting
	case host.Name == turn.ProposePackTool:
		proposal, err := turn.ReadProposedPack(args)
		if err != nil {
			return runcontext.ToolResult{}, false, err
		}
		if err := r.sc.Drafts.SeedContext(ctx, proposal.Document, proposal.Unknowns); err != nil {
			return runcontext.ToolResult{}, false, err
		}
		r.held.Lock()
		r.toolProposal = &proposal
		r.held.Unlock()
		return textResult(turn.ProposalReceived, false), false, nil
	}
	if !runcontext.ReadingTool(host) {
		// A side effect: the checkpoint is acknowledged before it, or it is not made and the run
		// ends with the storage failure.
		if err := r.save(ctx); err != nil {
			if !failure.IsCancelled(err) {
				r.held.Lock()
				r.fatal = err
				r.held.Unlock()
				r.pause()
			}
			return runcontext.ToolResult{}, false, err
		}
	}
	// The call's id goes to the host tool in its context, as the TypeScript engines hand it to execute.
	result, err := turn.WithDeadline(runcontext.WithCallID(ctx, call.ID), limit, late, func(ctx context.Context) (runcontext.ToolResult, error) {
		return host.Execute(ctx, args)
	})
	return result, err == nil, err
}

// claimPause marks the run waiting, a handoff or a question; false where it already waits.
func (r *runState) claimPause(handoff bool) bool {
	r.held.Lock()
	defer r.held.Unlock()
	if r.awaitingInput || r.handoff {
		return false
	}
	if handoff {
		r.handoff = true
	} else {
		r.awaitingInput = true
	}
	return true
}

// joined is a context done when either is.
func joined(a, b context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(b)
	stop := context.AfterFunc(a, cancel)
	return ctx, func() { stop(); cancel() }
}

func nonEmpty(args json.RawMessage) json.RawMessage {
	if len(strings.TrimSpace(string(args))) == 0 {
		return json.RawMessage(`{}`)
	}
	return args
}

// resultOf is a gate result as an MCP result.
func resultOf(result gate.Result) runcontext.ToolResult {
	var structured *runcontext.Value
	if len(result.Structured) > 0 {
		structured, _ = runcontext.ParseJSON(result.Structured)
	}
	out := runcontext.ResultOf([]runcontext.Content{runcontext.TextContent(result.Text)}, structured)
	if result.IsError {
		out = out.WithIsError(true)
	}
	return out
}

// failed is a failure as the result the model reads (toolFailureResult).
func failed(cause error) runcontext.ToolResult {
	text, structured := failure.ToolFailure(cause)
	value, _ := runcontext.ParseJSON(structured)
	return runcontext.ResultOf([]runcontext.Content{runcontext.TextContent(text)}, value).WithIsError(true)
}

func textResult(text string, isError bool) runcontext.ToolResult {
	out := runcontext.ResultOf([]runcontext.Content{runcontext.TextContent(text)}, nil)
	if isError {
		out = out.WithIsError(true)
	}
	return out
}

func structuredOf(result runcontext.ToolResult) json.RawMessage {
	if result.StructuredContent == nil {
		return nil
	}
	return json.RawMessage(result.StructuredContent.JSON())
}

func textOfResult(result runcontext.ToolResult) string {
	var parts []string
	for _, item := range result.Content {
		if item.Type() == "text" {
			parts = append(parts, item.Text())
		}
	}
	return strings.Join(parts, "\n")
}

func callEvent(callID, name string, args json.RawMessage, origin string) contract.RunEvent {
	event := &contract.ToolCall{Type: "tool_call", Name: name, Args: args}
	if callID != "" {
		event.CallID = &callID
	}
	if origin != "" {
		event.Origin = &origin
	}
	return contract.RunEvent{ToolCall: event}
}

func resultEvent(callID, name string, result runcontext.ToolResult, origin string) contract.RunEvent {
	event := &contract.ToolResult{Type: "tool_result", Name: name, IsError: result.Err(), Text: textOfResult(result), Structured: structuredOf(result)}
	if callID != "" {
		event.CallID = &callID
	}
	if origin != "" {
		event.Origin = &origin
	}
	return contract.RunEvent{ToolResult: event}
}

// errNoRender is why an image is not shown on the agent wire: the package holds no render adapter.
var errNoRender = errors.New("no render adapter is bound to this run")

// separate takes a result's images out (agent#5): the agent wire carries text only, so each image
// leaves a line saying it is not shown.
func separate(ctx context.Context, name string, result runcontext.ToolResult) (runcontext.ToolResult, error) {
	value, err := wire.Parse(result.JSON())
	if err != nil {
		return result, nil
	}
	held := wire.ToolResult{Name: name, StructuredContent: value.Get("structuredContent")}
	image := false
	for _, item := range value.Get("content").Items() {
		held.Content = append(held.Content, item)
		if kind := item.Get("type"); kind != nil && kind.Text() == "image" {
			image = true
		}
	}
	if !image {
		return result, nil
	}
	if isError := value.Get("isError"); isError != nil {
		flag, _ := isError.Boolean()
		held.IsError = &flag
	}
	separated, _, err := wire.SeparateImages(ctx, held, wire.ImageRouting{
		Resolve: func(string) *wire.ImagePart { return nil },
		Render:  func(context.Context, wire.ImagePart) (string, error) { return "", errNoRender },
	})
	if err != nil {
		return runcontext.ToolResult{}, err
	}
	out := wire.Obj()
	content := wire.Arr()
	for _, item := range separated.Content {
		content.Append(item)
	}
	out.Set("content", content)
	if separated.StructuredContent != nil {
		out.Set("structuredContent", separated.StructuredContent)
	}
	if separated.IsError != nil {
		out.Set("isError", wire.Bool(*separated.IsError))
	}
	parsed, err := runcontext.ParseJSON(out.JSON())
	if err != nil {
		return result, nil
	}
	return runcontext.ReadToolResult(parsed), nil
}
