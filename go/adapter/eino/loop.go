package eino

import (
	"context"
	"encoding/json"

	"github.com/cloudwego/eino/schema"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/turn"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/wire"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
)

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

// toolMessage is what the model is shown of one native call, as the tool message the agent carries.
func (t *turnState) toolMessage(callID, name string, result ran) *schema.Message {
	return t.r.toolMessage(callID, name, result.content(), result.result.Err(), result.images)
}

// textTurn is the messages that carry a text call's answer back: the model's own answer, then the
// result as the next user message.
func (t *turnState) textTurn(answer *schema.Message, name string, result ran) []*schema.Message {
	text := turn.TextToolResultHeader(name, result.result.Err()) + "\n" + result.content()
	next := userMessage(text)
	if len(result.images) > 0 {
		next.Extra = map[string]any{extraKey: t.r.side.keep(held{images: result.images})}
	}
	return []*schema.Message{answer, next}
}

// stepSettings is what one request asks besides its messages.
func (t *turnState) settings(tools []wire.WireTool, closed bool) *stepRequest {
	request := &stepRequest{tier: t.r.tier, oneCall: !t.r.capabilities.ParallelToolCalls && t.r.nativeTools && !closed}
	if !(closed && t.r.nativeTools) && t.r.nativeTools {
		request.tools = tools
	}
	return request
}

// sendStep sends one prepared request and reads the answer; a failure is kept as it was met.
func (t *turnState) sendStep(ctx context.Context, send sender, system wire.SystemText, messages []*schema.Message, tools []wire.WireTool, closed bool) (*schema.Message, wire.Answer, error) {
	request := t.settings(tools, closed)
	request.system = system
	var read wire.Answer
	request.answered = func(answer wire.Answer) { read = answer }
	answer, err := send(ctx, request, messages)
	if err != nil {
		if t.streamed == nil {
			t.streamed = err
		}
		return nil, read, err
	}
	return answer, read, nil
}

// closeCalls refuses the calls an answer made on a request that closed the tools, as the boundary
// refuses them at dispatch (rejectToolStep), and hands the framework the answer without them, so
// the stretch ends there: Eino's loop runs while the model calls tools.
func (t *turnState) closeCalls(answer *schema.Message) *schema.Message {
	if len(answer.ToolCalls) == 0 {
		return answer
	}
	for _, call := range answer.ToolCalls {
		t.rejectToolStep(call.Function.Name, json.RawMessage(call.Function.Arguments))
	}
	held := *answer
	held.ToolCalls = nil
	return &held
}

// runOnce is one attempt at the turn.
func (r *runState) runOnce(ctx context.Context) error {
	t := &turnState{r: r, turnLimit: r.sc.Execution.Remaining()}
	runtimeTools, hostTools := r.offeredTools()
	t.offered = map[string]bool{}
	for _, tool := range runtimeTools {
		t.offered[tool.Name] = true
	}
	for _, tool := range hostTools {
		t.offered[tool.Name] = true
	}
	tools, err := r.toolList(nil)
	if err != nil {
		return err
	}
	shown, err := r.wireTools(tools)
	if err != nil {
		return err
	}
	declarationsOf := declarations(tools)
	if !r.nativeTools {
		declarationsOf = nil
	}
	instructions := r.systemFor()
	declaredHosts := make([]contract.ToolOfferHostTool, 0, len(r.session.HostTools))
	for _, tool := range r.session.HostTools {
		declaredHosts = append(declaredHosts, tool.Declaration())
	}
	if turn.OffersProposeTool(declaredHosts) {
		instructions += turn.ProposePackInstructions
	}
	instructions += runcontext.DataRule + r.run.LanguageInstructions
	t.schemas = runcontext.Array()
	if r.nativeTools {
		t.schemas = schemasValue(tools)
	}
	toolSection := ""
	if !r.nativeTools {
		described, err := r.described(t.offered)
		if err != nil {
			return err
		}
		toolSection = "\n\n" + described
	}
	r.tier = r.slot.Members()
	if r.tier != nil {
		if maximum := r.tier.Get("max_tokens"); maximum != nil {
			tokens, _ := maximum.Number()
			if err := r.sc.ReserveOutput(int(tokens)); err != nil {
				return err
			}
		}
	}
	t.instructions = instructions
	// The stable prefix (ADR-0005): the package's guidance with the execution instructions, then
	// the host's parts, then the tools described in text where the endpoint takes no native calls.
	t.system = r.sc.System(instructions+"\n"+runcontext.ExecutionInstructions, toolSection)
	var currentRequest *runcontext.CurrentRequest
	if r.run.Context != nil {
		currentRequest = r.run.Context.CurrentRequest
	}
	opening, err := r.sc.Opening(r.session.Prompt, r.sc.System(instructions, toolSection)+"\n"+r.sc.Execution.Guidance(), t.schemas, currentRequest)
	if err != nil {
		return err
	}
	t.messages = []*schema.Message{userMessage(opening...)}
	t.lastMessages = t.messages

	model := &relayModel{run: r, model: r.run.ModelName}
	var current *phase
	loop := t.loopPhase(shown)
	current = loop
	get := func() *phase { return current }

	var unread *unreadAnswer
	keepUnread := func() {
		if unread == nil {
			return
		}
		text := turn.WholeAnswer(unread.text)
		unread = nil
		if text != "" {
			r.deliver(contract.RunEvent{Message: &contract.RunEventMessage{Type: "message", Text: text}})
		}
	}
	if d := r.sc.Drafts.Proposal(); d != nil {
		t.toolProposal = &turn.Proposal{Document: d.Document, Unknowns: d.Unknowns, Edits: d.Edits}
	}
	var proposal *turn.Proposal
	turnErr := func() error {
		for {
			t.completion = nil
			t.lastCalls = 0
			t.lastAnswer = nil
			t.stepInRun = 0
			if err := r.stretch(ctx, declarationsOf, model, get, t.messages); err != nil {
				if r.awaitingInput {
					return errWaiting
				}
				if t.streamed != nil {
					return t.streamed
				}
				return err
			}
			if t.streamed != nil {
				return t.streamed
			}
			if noticed := r.slot.TurnEnded(t.final != ""); noticed != nil {
				r.deliver(*noticed)
			}
			t.finalText = t.final
			answered := t.lastAnswer
			if !r.nativeTools && answered != nil {
				// The text path (agent#4): an answer that makes one call in a fenced JSON block is a
				// tool turn, through the same dispatch and so the same gate.
				call, problem := turn.ExtractToolCall(t.finalText)
				if call != nil || problem != "" {
					t.lastCalls = 1
					if problem != "" {
						if !t.toolsClosed && !t.synthesis {
							t.messages = append(append([]*schema.Message{}, t.lastMessages...), answered,
								userMessage(turn.TextToolResultHeader("the tool call", true)+"\nThe call was not made: "+problem+"."))
							continue
						}
					} else if t.toolsClosed || t.synthesis {
						t.rejectToolStep(call.Tool, call.Arguments)
					} else {
						result, err := t.dispatch(ctx, call.Tool, call.Arguments, t.offered)
						if err != nil {
							return err
						}
						if r.awaitingInput {
							return errWaiting
						}
						t.messages = append(append([]*schema.Message{}, t.lastMessages...), t.textTurn(answered, call.Tool, result)...)
						continue
					}
				}
			}
			attempted := t.synthesis || (t.toolsClosed && r.sc.Execution.Progress.Stalled())
			if t.toolProposal == nil && failure.CanFinishResponse(t.completion, t.finalText, t.lastCalls, r.sc.Execution.Remaining(), attempted) {
				t.synthesis = true
				if r.placement == "system-tail" {
					t.messages = t.lastMessages
				} else {
					t.messages = append(append([]*schema.Message{}, t.lastMessages...), userMessage(failure.FinishResponseInstructions))
				}
				continue
			}
			reason := ""
			if t.completion != nil {
				reason = t.completion.FinishReason
			}
			cut := reason == "length" || reason == "content-filter" || reason == "error"
			if t.toolProposal == nil && !r.sc.Execution.Progress.Stalled() && !trimmed(t.finalText) && t.steps >= t.turnLimit && !cut {
				return runcontext.ErrExecutionPaused
			}
			if t.toolProposal == nil && ((t.toolsClosed && t.lastCalls > 0) || !trimmed(t.finalText) || cut) {
				return t.unfinished()
			}
			if t.toolProposal != nil || !r.session.AllowConversation || turn.HasProposalFence(t.finalText) {
				var readErr error
				if t.toolProposal != nil && turn.HasProposalFence(t.finalText) {
					t.toolProposal = nil
					readErr = turn.TwoProposals()
				} else if t.toolProposal != nil {
					d := r.sc.Drafts.Proposal()
					proposal = &turn.Proposal{Document: d.Document, Unknowns: d.Unknowns, Edits: d.Edits}
				} else {
					held, err := turn.ExtractProposal(t.finalText)
					if err != nil {
						readErr = err
					} else {
						proposal = &held
					}
				}
				if readErr != nil {
					// One reformat request, within the allowance: tool-free, shown its own answer.
					if unread == nil && r.sc.Execution.Remaining() > 0 && answered != nil {
						unread = &unreadAnswer{text: t.finalText, cause: readErr}
						t.reformatting, t.synthesis = true, true
						r.run.Gate.BeginReformat()
						t.messages = append(append([]*schema.Message{}, t.lastMessages...), answered, userMessage(turn.ReformatRequest(readErr)))
						continue
					}
					if unread == nil {
						unread = &unreadAnswer{text: t.finalText, cause: readErr}
					}
					return unread.cause
				}
			}
			return nil
		}
	}()
	if t.reformatting {
		r.run.Gate.EndReformat()
	}
	if turnErr != nil {
		keepUnread()
		// A proposal made through propose_pack is finished work: a later request of the turn that
		// fails does not take it with it.
		if made := t.toolProposal; made != nil && ctx.Err() == nil {
			if d := r.sc.Drafts.Proposal(); d != nil {
				made = &turn.Proposal{Document: d.Document, Unknowns: d.Unknowns, Edits: d.Edits}
			}
			t.toolProposal = nil
			event := proposalEvent(*made)
			if r.awaitingInput {
				r.deliver(event)
				return turnErr
			}
			cause := turnErr
			if r.overTime {
				cause = r.turnTimeFailure()
			}
			note := turn.ProposalNote("turn-unfinished", cause, "model")
			event.Proposal.Note = &note
			r.deliver(event)
			return nil
		}
		return turnErr
	}
	if proposal == nil && unread != nil {
		first := unread.cause
		keepUnread()
		if prose := turn.ProseOf(t.finalText); prose != "" {
			r.deliver(messageEvent(prose))
		}
		return first
	}
	return t.finish(ctx, proposal, unread)
}

// unreadAnswer is an answer whose proposal could not be read: delivered whole before the run
// reports why.
type unreadAnswer struct {
	text  string
	cause error
}

// proposalEvent is a proposal as its event.
func proposalEvent(proposal turn.Proposal) contract.RunEvent {
	var document map[string]json.RawMessage
	_ = json.Unmarshal(proposal.Document, &document)
	unknowns := proposal.Unknowns
	if unknowns == nil {
		unknowns = []string{}
	}
	return contract.RunEvent{Proposal: &contract.RunEventProposal{Type: "proposal", Document: document, Unknowns: unknowns, Edits: proposal.Edits}}
}

// messageEvent is the model's own prose as a message event.
func messageEvent(text string) contract.RunEvent {
	return contract.RunEvent{Message: &contract.RunEventMessage{Type: "message", Text: text}}
}
