package eino

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/turn"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/wire"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
)

// The boundary's dispatch: every call the model makes, on either of Eino's two entry points (the
// tools node's middleware for a declared name, its unknown-tools handler for any other) and on the
// text path, reaches dispatch, and a Runtime call leaves only through the gate's narrow handle.

// gatedCall is the Runtime call the context wraps (contextualSession's callTool): the gate's
// CallTool, its answer as an MCP result.
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

// resultOf is a gate result as an MCP result: its text, its structured content, whether it failed.
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

// ran is what one call gives the model: its bounded result, that result as a data block where a
// reading tool answered, and the images a vision model is shown.
type ran struct {
	result runcontext.ToolResult
	images []wire.ImagePart
	framed string
}

func bare(result runcontext.ToolResult) ran { return ran{result: result} }

// content is what the model is shown of a call: the data block, or the bounded result as JSON.
func (c ran) content() string {
	if c.framed != "" {
		return c.framed
	}
	return string(c.result.JSON())
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

// stopped is what the model reads of a call the run stopped before.
const stopped = "the session was stopped before this call was made; nothing was written"

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

func (t *turnState) callEvent(callID, name string, args json.RawMessage, origin string) contract.RunEvent {
	event := &contract.ToolCall{Type: "tool_call", Name: name, Args: args}
	if callID != "" {
		event.CallID = &callID
	}
	if origin != "" {
		event.Origin = &origin
	}
	return contract.RunEvent{ToolCall: event}
}

func (t *turnState) resultEvent(callID, name string, result runcontext.ToolResult, origin string) contract.RunEvent {
	event := &contract.ToolResult{Type: "tool_result", Name: name, IsError: result.Err(), Text: textOfResult(result), Structured: structuredOf(result)}
	if callID != "" {
		event.CallID = &callID
	}
	if origin != "" {
		event.Origin = &origin
	}
	return contract.RunEvent{ToolResult: event}
}

// rejectToolStep refuses a call on a request reserved for a final answer, or one that repeats
// completed work: journaled as not started, with the arguments as the model wrote them, and never
// run (rejectToolStep in the deepagents loop).
func (t *turnState) rejectToolStep(name string, args json.RawMessage) *runcontext.ToolResult {
	parsed, _ := runcontext.ParseJSON(nonEmpty(args))
	if !t.toolsClosed && !t.r.sc.Execution.Progress.Rejects(name, parsed) {
		return nil
	}
	code, detail := "invalid-response", "This call repeats completed work with the same arguments and was not executed. Use its retained result, choose a different concrete action, or explain the remaining blocker."
	if t.r.sc.Execution.Progress.Stalled() {
		code = "repeated-work"
	}
	if t.toolsClosed {
		detail = "This request is reserved for a final answer. This tool call was not executed. Use completed results or explain what remains."
	}
	f := failure.New(code, "desk", "not-started")
	f.Detail = &detail
	result := failed(failure.NewError(f))
	callID := wire.RandomUUID()
	t.r.deliver(t.callEvent(callID, name, nonEmpty(args), ""))
	t.r.deliver(t.resultEvent(callID, name, result, ""))
	return &result
}

func nonEmpty(args json.RawMessage) json.RawMessage {
	if len(strings.TrimSpace(string(args))) == 0 {
		return json.RawMessage(`{}`)
	}
	return args
}

// correlation is the call id an interactive run's events carry, or none.
func (t *turnState) correlation() string {
	if t.r.run.Interactive {
		return wire.RandomUUID()
	}
	return ""
}

// asData is a reading tool's bounded answer as one data block, closed by the run's marker
// (agent#12).
func (t *turnState) asData(name string, result runcontext.ToolResult, images []wire.ImagePart) ran {
	return ran{result: result, images: images, framed: t.r.sc.Frame(name, string(result.JSON()))}
}

// runRuntime is one Runtime call, wherever the model made it: through the context's read cache to
// the gate's handle, with the call, the result and the gate's guardrail between them.
func (t *turnState) runRuntime(ctx context.Context, name string, args json.RawMessage, origin string) (ran, error) {
	args = nonEmpty(args)
	if t.r.awaitingInput {
		return ran{}, errWaiting
	}
	if err := ctx.Err(); err != nil {
		return ran{}, failure.Cancelled
	}
	if refusal := t.rejectToolStep(name, args); refusal != nil {
		return bare(*refusal), nil
	}
	callID := t.correlation()
	t.r.deliver(t.callEvent(callID, name, args, origin))
	limit := time.Duration(t.r.sc.Policy.ToolCallMilliseconds) * time.Millisecond
	answer, err := turn.WithDeadline(ctx, limit, func() error { return turn.ToolTimeout(limit) }, func(ctx context.Context) (runcontext.ToolResult, error) {
		return t.r.session.CallTool(ctx, name, args)
	})
	if err != nil {
		if failure.IsCancelled(err) && ctx.Err() != nil {
			return bare(textResult(stopped, true)), nil
		}
		result := failed(err)
		t.r.deliver(t.resultEvent(callID, name, result, origin))
		return bare(result), nil
	}
	t.r.deliver(t.resultEvent(callID, name, answer, origin))
	if t.recording != nil {
		t.recording.Saw(name, textOfResult(answer), args, structuredOf(answer))
	}
	separated, images, err := t.r.separate(ctx, name, answer)
	if err != nil {
		return ran{}, err
	}
	bounded, err := t.r.sc.Result(name, separated)
	if err != nil {
		return ran{}, err
	}
	return t.asData(name, bounded, images), nil
}

// runHost is one host tool call, wherever the model made it: held to the phase by the gate's
// handle first, then a question, a delegation, propose_pack, or the host's own function under the
// tool-call deadline.
func (t *turnState) runHost(ctx context.Context, tool runcontext.HostTool, args json.RawMessage) (ran, error) {
	args = nonEmpty(args)
	if t.r.awaitingInput {
		return ran{}, errWaiting
	}
	if err := ctx.Err(); err != nil {
		return ran{}, failure.Cancelled
	}
	if refusal := t.rejectToolStep(tool.Name, args); refusal != nil {
		return bare(*refusal), nil
	}
	callID := t.correlation()
	// ADR-0008: an act call of a tool with a source always carries an id, whether or not the run's
	// events carry one, so the decision taken before its call is its own, never another call's.
	if callID == "" && t.r.sc.Sources != nil && t.r.sc.Sources.NeedsApproval(tool.Name) {
		callID = wire.RandomUUID()
	}
	// ADR-0008: an act call the host's policy asks about is held for the person, never run: its
	// approval_requested in place of the call, and the run's next request is refused.
	if held := t.r.sc.Approval(ctx, tool.Name, args, callID); held != nil {
		t.r.deliver(*held)
		return bare(textResult(runcontext.ApprovalHeld, true)), nil
	}
	if tool.Interaction != "question" {
		t.r.deliver(t.callEvent(callID, tool.Name, args, ""))
	}
	// The call's id goes to the host tool in its context, as the TypeScript engines hand it to execute.
	answer, err := t.hostAnswer(runcontext.WithCallID(ctx, callID), tool, args)
	if err != nil {
		if err == errWaiting {
			return ran{}, err
		}
		if failure.IsCancelled(err) && ctx.Err() != nil {
			return bare(textResult(stopped, true)), nil
		}
		result := failed(err)
		if tool.Interaction == "question" {
			result = resultOf(turn.QuestionFailureResult(err))
		}
		t.r.deliver(t.resultEvent(callID, tool.Name, result, ""))
		return bare(result), nil
	}
	t.r.deliver(t.resultEvent(callID, tool.Name, answer, ""))
	separated, images, err := t.r.separate(ctx, tool.Name, answer)
	if err != nil {
		return ran{}, err
	}
	bounded, err := t.r.sc.Result(tool.Name, separated)
	if err != nil {
		return ran{}, err
	}
	if runcontext.ReadingTool(&tool) && tool.Name != turn.ProposePackTool {
		return t.asData(tool.Name, bounded, images), nil
	}
	return ran{result: bounded, images: images}, nil
}

// hostAnswer is what one host tool answers.
func (t *turnState) hostAnswer(ctx context.Context, tool runcontext.HostTool, args json.RawMessage) (runcontext.ToolResult, error) {
	if refused := t.r.run.Gate.AdmitHost(tool.Name); refused != nil {
		return resultOf(*refused), nil
	}
	switch {
	case tool.Interaction == "delegation":
		// An awaited call inside this step (agent#14): the coordinator runs the batch while the call
		// is open, bounded by its children's limits and by the run, not by the tool-call deadline.
		if _, err := turn.ReadDelegationRequest(args); err != nil {
			return runcontext.ToolResult{}, err
		}
		return tool.Execute(ctx, args)
	case tool.Interaction == "question":
		// The question pause: a malformed question is a failed result the model corrects in this
		// turn; a well-formed one ends the turn, unanswered.
		question, err := turn.ReadModelQuestion(args)
		if err != nil {
			return runcontext.ToolResult{}, err
		}
		if t.r.awaitingInput {
			return runcontext.ToolResult{}, errWaiting
		}
		t.r.awaitingInput = true
		t.r.deliver(contract.RunEvent{UserQuestion: &contract.RunEventUserQuestion{Type: "user_question", ID: wire.RandomUUID(), Question: question}})
		if t.r.pause != nil {
			t.r.pause()
		}
		return runcontext.ToolResult{}, errWaiting
	case tool.Name == turn.ProposePackTool:
		// The proposal, read by the contract's reader; the host's own function is never reached.
		proposal, err := turn.ReadProposedPack(args)
		if err != nil {
			return runcontext.ToolResult{}, err
		}
		if err := t.r.sc.Drafts.Seed(proposal.Document, proposal.Unknowns); err != nil {
			return runcontext.ToolResult{}, err
		}
		t.toolProposal = &proposal
		return textResult(turn.ProposalReceived, false), nil
	}
	limit := time.Duration(t.r.sc.Policy.ToolCallMilliseconds) * time.Millisecond
	return turn.WithDeadline(ctx, limit, func() error { return turn.ToolTimeout(limit) }, func(ctx context.Context) (runcontext.ToolResult, error) {
		return tool.Execute(ctx, args)
	})
}

// refuseUnoffered is a call to a name this phase did not offer: put through the gate as the model
// wrote it, so the layer that holds is the one that reports, and answered refused whatever the
// gate said (refusedUnoffered). The native path, Eino's unknown-tools handler and the text path
// alike.
func (t *turnState) refuseUnoffered(ctx context.Context, name string, args json.RawMessage) (runcontext.ToolResult, error) {
	t.r.deliver(t.callEvent("", name, nonEmpty(args), ""))
	if name == "delegate_tasks" && t.r.run.ChildDefinition != nil {
		t.r.deliver(runcontext.SubagentNotice(name, "A child cannot delegate.", "subagent refused"))
	}
	refusal := "the call did not leave the page and nothing was written"
	// ADR-0006: the Runtime's row of the run report counts the refusal, as the TypeScript boundary's
	// wrapped callTool does.
	if t.r.sc.Sources != nil {
		t.r.sc.Sources.RuntimeRefused()
	}
	answered, err := t.r.run.Gate.CallTool(ctx, name, nonEmpty(args))
	if err != nil {
		if failure.IsCancelled(err) || ctx.Err() != nil {
			return runcontext.ToolResult{}, failure.Cancelled
		}
		refusal = err.Error()
	}
	text := gate.UnofferedRefusal(refusal)
	if answered.IsError && strings.HasPrefix(answered.Text, "refused: ") {
		text = answered.Text
	}
	result := textResult(text, true)
	t.r.deliver(t.resultEvent("", name, result, ""))
	return result, nil
}

// refuseReview is a request_review call the run does not admit (ADR-0010): its call, the switch's
// guardrail (put on the stream by the boundary before the result) and the refused result, as an
// unoffered call's reads.
func (t *turnState) refuseReview(name string, args json.RawMessage) runcontext.ToolResult {
	t.r.deliver(t.callEvent("", name, nonEmpty(args), ""))
	result := textResult(gate.UnofferedRefusal(t.r.sc.RefuseReview()), true)
	t.r.deliver(t.resultEvent("", name, result, ""))
	return result
}

// dispatch is one call, wherever it was made, through the same dispatch and so the same gate: only
// a name this phase offered is looked up.
func (t *turnState) dispatch(ctx context.Context, name string, args json.RawMessage, offered map[string]bool) (ran, error) {
	if unparsed := unparsedArguments(args); unparsed != "" {
		// Arguments that were not a JSON object: nothing is run, and the model is told why.
		result := failed(failure.ToolInput("The call’s arguments are not a JSON object; nothing was run. Call the tool again with an object of its arguments."))
		callID := wire.RandomUUID()
		held, _ := json.Marshal(map[string]string{wire.UnparsedArguments: unparsed})
		t.r.deliver(t.callEvent(callID, name, held, ""))
		t.r.deliver(t.resultEvent(callID, name, result, ""))
		return bare(result), nil
	}
	// ADR-0010: request_review reaches the switch's own tool only in the author's phase of a run it is
	// offered to; anywhere else it is refused here, with the guardrail review refused, before anything
	// runs and before the gate, and nothing is recorded.
	if runcontext.IsEdit(name) && (!offered[name] || !t.r.sc.MemoryAdmitted()) {
		t.r.deliver(t.callEvent("", name, nonEmpty(args), ""))
		var input struct {
			Document string `json:"document"`
		}
		_ = json.Unmarshal(nonEmpty(args), &input)
		err := t.r.sc.Drafts.Refuse(name, input.Document)
		result := textResult(err.Error(), true)
		t.r.deliver(t.resultEvent("", name, result, ""))
		return bare(result), nil
	}
	if name == runcontext.Remember && (!offered[name] || !t.r.sc.MemoryAdmitted()) {
		t.r.deliver(t.callEvent("", name, nonEmpty(args), ""))
		result := textResult(gate.UnofferedRefusal(t.r.sc.RefuseMemory()), true)
		t.r.deliver(t.resultEvent("", name, result, ""))
		return bare(result), nil
	}
	if name == runcontext.RequestReview && (!offered[name] || !t.r.sc.ReviewAdmitted()) {
		return bare(t.refuseReview(name, args)), nil
	}
	if !offered[name] {
		if rejected := t.rejectToolStep(name, args); rejected != nil {
			return bare(*rejected), nil
		}
		result, err := t.refuseUnoffered(ctx, name, args)
		return bare(result), err
	}
	for _, tool := range t.r.session.HostTools {
		if tool.Name == name {
			return t.runHost(ctx, tool, args)
		}
	}
	return t.runRuntime(ctx, name, args, "")
}

// unparsedArguments is the argument text where it is not a JSON object, and "" where it is one or
// is empty (wire.ToolCall.Unparsed's rule).
func unparsedArguments(args json.RawMessage) string {
	return wire.ToolCall{Arguments: string(args)}.Unparsed()
}

// separate takes a result's images out (agent#5): a vision model is shown them after the results
// they belong to; any other is shown the host's rendering inside the result.
func (r *runState) separate(ctx context.Context, name string, result runcontext.ToolResult) (runcontext.ToolResult, []wire.ImagePart, error) {
	value, err := wire.Parse(result.JSON())
	if err != nil {
		return result, nil, nil
	}
	held := wire.ToolResult{Name: name, StructuredContent: value.Get("structuredContent")}
	for _, item := range value.Get("content").Items() {
		held.Content = append(held.Content, item)
	}
	if isError := value.Get("isError"); isError != nil {
		flag, _ := isError.Boolean()
		held.IsError = &flag
	}
	hasImage := false
	for _, item := range held.Content {
		if kind := item.Get("type"); kind != nil && kind.Text() == "image" {
			hasImage = true
		}
	}
	if !hasImage {
		return result, nil, nil
	}
	routing := wire.ImageRouting{
		Vision:  r.vision(),
		Resolve: func(reference string) *wire.ImagePart { return r.ledgerImage(reference) },
	}
	separated, images, err := wire.SeparateImages(ctx, held, routing)
	if err != nil {
		return runcontext.ToolResult{}, nil, err
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
		return result, nil, nil
	}
	return runcontext.ReadToolResult(parsed), images, nil
}

// vision is whether this run's model is shown images (agent#5): a vision record, on a wire that can
// carry an image beside a result in the turn order it keeps.
func (r *runState) vision() bool {
	strict := r.capabilities.StrictAlternation != nil && *r.capabilities.StrictAlternation
	return r.capabilities.Vision && (!r.nativeTools || !strict || r.family == wire.Anthropic)
}

// ledgerImage is a retained image by its reference, as the wire's image part.
func (r *runState) ledgerImage(reference string) *wire.ImagePart {
	image := r.sc.Ledger.Image(reference)
	if image == nil {
		return nil
	}
	encoded, err := json.Marshal(image)
	if err != nil {
		return nil
	}
	var out wire.ImagePart
	if json.Unmarshal(encoded, &out) != nil {
		return nil
	}
	return &out
}
