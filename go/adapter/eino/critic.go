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

// finish delivers the turn's prose, runs the critic where the session asks for it, and delivers
// the proposal: the critic is a second agent on the same relay, the same boundary and the same
// dispatch, offered its allow-list, after the proposal exists and before it is delivered.
func (t *turnState) finish(ctx context.Context, proposal *turn.Proposal, unread *unreadAnswer) error {
	r := t.r
	if proposal == nil {
		r.deliver(messageEvent(turn.ProseOf(t.finalText)))
		if r.sc.Execution.Remaining() <= 0 && planUnfinished(r.sc.Execution.State.Plan) {
			return runcontext.ErrExecutionPaused
		}
		return nil
	}
	prose := ""
	if unread != nil {
		prose = turn.ProseOf(unread.text)
	}
	if prose == "" {
		prose = turn.ProseOf(t.finalText)
	}
	if prose != "" {
		r.deliver(messageEvent(prose))
	}
	var critique *turn.Critique
	var note *contract.RunEventProposalNote
	// Plan mode runs no critic (planningSession sets adversarialReview false).
	asked := r.run.Review
	if r.run.Context != nil && r.run.Context.Phase == "plan" {
		no := false
		asked = &no
	}
	review := turn.CriticRuns(asked, r.sc.Policy.AdversarialReview)
	if review {
		if cannot := turn.CriticCannotRun(r.session.TestPrompt); cannot != nil {
			critique = cannot
			r.deliver(turn.CritiqueEvent(*cannot))
		} else {
			held, heldNote, err := t.critic(ctx, *proposal)
			if err != nil {
				return err
			}
			critique, note = held, heldNote
		}
	}
	event := proposalEvent(*proposal)
	event.Proposal.Critique = turn.CritiqueOnProposal(critique)
	event.Proposal.Note = note
	r.deliver(event)
	return nil
}

// planUnfinished is whether the execution's plan holds a step not completed.
func planUnfinished(plan *runcontext.Value) bool {
	if plan == nil {
		return false
	}
	for _, step := range plan.Items() {
		if status := step.Get("status"); status == nil || status.Str() != "completed" {
			return true
		}
	}
	return false
}

// critic runs the refutation pass (agent#10) and answers its critique, or the note a critic that
// did not finish leaves on the proposal.
func (t *turnState) critic(ctx context.Context, proposal turn.Proposal) (*turn.Critique, *contract.RunEventProposalNote, error) {
	r := t.r
	cases, err := turn.ReadCriticCases(casesOf(r.run.Cases))
	if err != nil {
		return nil, nil, failure.Authored("configuration", err.Error(), "context")
	}
	recorder := turn.OpenCritique(turn.CritiqueOptions{Cases: cases, Document: proposal.Document})
	t.recording = recorder
	t.toolsClosed = false
	r.sc.BeginIsolatedPhase()
	r.sc.CriticReading = true
	r.run.Gate.BeginCritic()
	defer func() {
		t.recording = nil
		r.sc.CriticReading = false
		r.run.Gate.EndCritic()
	}()
	before := turn.CriticStart{Used: r.sc.Execution.State.Used, Rows: len(r.sc.Execution.State.Completions)}
	criticModel := r.run.ModelName
	harnessCalls := 0
	figures := func() *contract.ProvenanceCriticFigures {
		held := turn.ModelCriticFigures(criticModel, before, turn.CriticEnd{Used: r.sc.Execution.State.Used, Completions: r.sc.Execution.State.Completions})
		if harnessCalls > 0 {
			calls := int64(harnessCalls)
			held.HarnessCalls = &calls
		}
		return &held
	}
	offered := map[string]bool{}
	runtimeTools, hostTools := r.offeredTools()
	for _, tool := range hostTools {
		if turn.CriticMayRead(tool.Name) {
			offered[tool.Name] = true
		}
	}
	for _, tool := range runtimeTools {
		if turn.CriticMayCall(tool.Name) {
			offered[tool.Name] = true
		}
	}
	system := turn.CriticSystem + runcontext.DataRule + r.run.LanguageInstructions
	if !r.nativeTools {
		described, err := r.described(offered)
		if err != nil {
			return nil, nil, err
		}
		system += "\n\n" + described
	}
	criticText := ""
	var unfinished error
	t.streamed = nil
	func() {
		message, err := r.sc.Prompt(turn.CriticMessage(r.session.TestPrompt, proposal.Document, cases), system, t.schemas, nil)
		if err != nil {
			unfinished = err
			return
		}
		messages := []*schema.Message{userMessage(message)}
		held := messages
		if r.run.Models != nil {
			named, _, err := r.run.Models()
			if err != nil {
				unfinished = err
				return
			}
			if named != "" {
				criticModel = named
			}
		}
		tools, err := r.toolList(offered)
		if err != nil {
			unfinished = err
			return
		}
		shown, err := r.wireTools(tools)
		if err != nil {
			unfinished = err
			return
		}
		names := declarations(tools)
		if !r.nativeTools {
			names = nil
		}
		steps := 0
		var answer *schema.Message
		critic := &phase{
			offered: offered,
			step: func(ctx context.Context, input []*schema.Message, send sender) (*schema.Message, error) {
				// The critic's turns are this session's turns.
				if noticed := r.slot.TurnEnded(criticText != ""); noticed != nil {
					r.deliver(*noticed)
				}
				criticText = ""
				r.currentPhase = "review"
				at := steps
				if r.nativeTools {
					at = t.stepInRun
				}
				t.stepInRun++
				remaining := r.sc.Execution.Remaining()
				if left := turn.MaxCriticTurns - at; left < remaining {
					remaining = left
				}
				t.progressStep = r.sc.Execution.Progress.NextStep(remaining)
				t.toolsClosed = t.progressStep == "finish" || at >= turn.MaxCriticTurns-1
				instruction := ""
				switch {
				case t.progressStep == "recover":
					instruction = runcontext.RecoverRepeatedWorkInstructions
				case t.progressStep == "finish":
					instruction = runcontext.RepeatedWorkInstructions
				case t.toolsClosed:
					instruction = finalInstructions
				}
				note := ""
				if instruction != "" {
					note = stateText(instruction)
				}
				budgetText := system
				if note != "" {
					budgetText += "\n" + note
				}
				kept, err := r.budgeted(input, budgetText, t.schemas)
				if err != nil {
					return nil, err
				}
				held = kept
				systemText, placed := wire.SystemText{Head: system}, kept
				if note != "" {
					systemText, placed = placeState(r.placement, kept, system, note)
				}
				answered, read, err := t.sendStep(ctx, send, systemText, placed, shown, t.toolsClosed)
				if err != nil {
					return nil, err
				}
				completion := completionOf(read)
				r.sc.Execution.RecordCompletion(completion, "review")
				if completion.FinishReason == "length" || completion.FinishReason == "content-filter" || completion.FinishReason == "error" {
					f := failure.New(failure.CompletionFailureCode(&completion), "model", "unknown")
					f.Completion = &completion
					if t.streamed == nil {
						t.streamed = failure.NewError(f)
					}
				}
				criticText = read.Text
				answer = answered
				if t.toolsClosed || t.stepInRun >= turn.MaxCriticTurns {
					return t.closeCalls(answered), nil
				}
				return answered, nil
			},
			call: func(ctx context.Context, name, callID, arguments string) (string, error) {
				result, err := t.dispatch(ctx, name, json.RawMessage(arguments), offered)
				if err != nil {
					return "", err
				}
				if r.awaitingInput {
					return "", errWaiting
				}
				r.side.byCall(callID, heldRow(result))
				return result.content(), nil
			},
		}
		model := &relayModel{run: r, model: criticModel}
		get := func() *phase { return critic }
		for {
			answer = nil
			t.stepInRun = 0
			if err := r.stretch(ctx, names, model, get, messages); err != nil {
				if t.streamed == nil {
					unfinished = err
				}
				return
			}
			if r.nativeTools || t.streamed != nil {
				break
			}
			// The text path: a call in the critic's answer runs through the same dispatch, within the
			// tools the critic was offered, and it asks again.
			call, problem := turn.ExtractToolCall(criticText)
			if (call == nil && problem == "") || answer == nil {
				break
			}
			if problem != "" {
				if t.toolsClosed {
					break
				}
				messages = append(append([]*schema.Message{}, held...), answer, userMessage(turn.TextToolResultHeader("the tool call", true)+"\nThe call was not made: "+problem+"."))
			} else if t.toolsClosed {
				t.rejectToolStep(call.Tool, call.Arguments)
				break
			} else {
				result, err := t.dispatch(ctx, call.Tool, call.Arguments, offered)
				if err != nil {
					unfinished = err
					return
				}
				messages = append(append([]*schema.Message{}, held...), t.textTurn(answer, call.Tool, result)...)
			}
			steps++
		}
		if t.streamed == nil {
			// agent#10: the declared cases the critic's own calls left uncompared, run once each by
			// the harness through the same dispatch, gate and recorder; no model request.
			t.toolsClosed = false
			var names []string
			for name := range offered {
				names = append(names, name)
			}
			if err := turn.CompareTheRest(ctx, recorder, proposal.Document, names, func(ctx context.Context, name string, args json.RawMessage) error {
				harnessCalls++
				_, err := t.runRuntime(ctx, name, args, "harness")
				return err
			}); err != nil {
				unfinished = err
				return
			}
		}
		if ended := r.slot.TurnEnded(criticText != ""); ended != nil {
			r.deliver(*ended)
		}
	}()
	if unfinished != nil && (ctx.Err() != nil && !r.overTime) {
		return nil, nil, unfinished
	}
	if unfinished == nil {
		unfinished = t.streamed
	}
	if unfinished == nil {
		critique := recorder.Critique(criticText)
		critique.Figures = figures()
		r.deliver(turn.CritiqueEvent(critique))
		return &critique, nil, nil
	}
	cause := unfinished
	if r.overTime {
		cause = r.turnTimeFailure()
	}
	note := turn.CriticNote(cause, "model", figures())
	return nil, &note, nil
}

// casesOf is the host's declared cases as one JSON array.
func casesOf(cases []json.RawMessage) json.RawMessage {
	if len(cases) == 0 {
		return json.RawMessage(`[]`)
	}
	encoded, _ := json.Marshal(cases)
	return encoded
}
