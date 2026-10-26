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

// loopPhase is the loop's phase: the step the deepagents loop's wrapModelCall hook takes, and its
// dispatch.
func (t *turnState) loopPhase(shown []wire.WireTool) *phase {
	r := t.r
	return &phase{
		offered: t.offered,
		step: func(ctx context.Context, input []*schema.Message, send sender) (*schema.Message, error) {
			// A step boundary is the end of the turn before it (the thinking slot's evidence).
			if t.steps > 0 {
				if noticed := r.slot.TurnEnded(t.final != ""); noticed != nil {
					r.deliver(*noticed)
				}
			}
			index := t.steps
			if r.nativeTools {
				index = t.stepInRun
			}
			t.stepInRun++
			t.steps++
			t.lastCalls = 0
			t.completion = nil
			t.final = ""
			r.listener.publicText = ""
			if r.run.Interactive {
				r.deliver(contract.RunEvent{MessageProgress: &contract.RunEventMessageProgress{Type: "message_progress", Text: ""}})
			}
			if t.synthesis {
				r.deliver(contract.RunEvent{ResponseRecovery: &contract.RunEventResponseRecovery{Type: "response_recovery"}})
			}
			if err := r.sc.Execution.RetainProgress(false); err != nil {
				return nil, err
			}
			progress := r.sc.Execution.Progress
			if t.synthesis {
				t.progressStep = "finish"
			} else {
				t.progressStep = progress.NextStep(r.sc.Execution.Remaining())
			}
			if t.progressStep == "finish" && progress.Stalled() && !t.synthesis {
				r.deliver(contract.RunEvent{ResponseRecovery: &contract.RunEventResponseRecovery{Type: "response_recovery"}})
			}
			t.toolsClosed = t.synthesis || t.progressStep == "finish" || index >= t.turnLimit-1 || r.sc.Execution.Remaining() <= 1
			instruction := ""
			switch {
			case t.reformatting:
				instruction = turnReformatInstructions
			case t.synthesis:
				instruction = failure.FinishResponseInstructions
			case t.progressStep == "recover":
				instruction = runcontext.RecoverRepeatedWorkInstructions
			case t.progressStep == "finish":
				instruction = runcontext.RepeatedWorkInstructions
			case t.toolsClosed:
				instruction = finalInstructions
			}
			currentState := ""
			if r.run.RequestContext() != nil {
				currentState = r.run.RequestContext().CurrentState
			}
			r.currentPhase = "work"
			if t.synthesis {
				r.currentPhase = "synthesis"
			}
			steers, notices := r.sc.Execution.TakeSteers(r.sc.Marker, r.sc.Execution.State().Used+1, *r.sc.Policy)
			r.emitAll(notices)
			state := stateText(r.sc.Execution.RequestState(), currentState, instruction)
			// Budgeted as one request: the system text and the state together.
			var held []*schema.Message
			var err error
			if t.steps == 1 && r.run.OpeningProjection() != nil {
				actualSchemas := t.schemas
				if t.toolsClosed && r.nativeTools {
					actualSchemas = runcontext.Array()
				}
				var current *runcontext.CurrentRequest
				if r.run.RequestContext() != nil {
					current = r.run.RequestContext().CurrentRequest
				}
				parts, projectErr := r.sc.OpeningProjected(ctx, r.session.Prompt, t.system+"\n"+state+"\n"+steers, actualSchemas, current, r.run.OpeningProjection())
				if projectErr != nil {
					return nil, projectErr
				}
				held = []*schema.Message{userMessage(parts...)}
			} else {
				// Eino retains its original input history. Keep the admitted protected opening
				// while retaining every subsequent actual message, tool call and result.
				if r.run.OpeningProjection() != nil && len(input) > 0 && len(t.lastMessages) > 0 {
					input = append([]*schema.Message{}, input...)
					input[0] = t.lastMessages[0]
				}
				held, err = r.budgeted(input, t.system+"\n"+state+"\n"+steers, t.schemas)
			}
			if err != nil {
				return nil, err
			}
			t.lastMessages = held
			system, placed := placeState(r.placement, held, t.system, state)
			placed = placeSteer(placed, steers)
			answer, read, err := t.sendStep(ctx, send, system, placed, shown, t.toolsClosed)
			if err != nil {
				return nil, err
			}
			completion := completionOf(read)
			t.completion = &completion
			phaseName := "work"
			if t.synthesis {
				phaseName = "synthesis"
			}
			r.currentPhase = phaseName
			r.sc.Execution.RecordCompletion(completion, phaseName)
			t.final = read.Text
			t.lastCalls = len(answer.ToolCalls)
			t.lastAnswer = answer
			limit := t.turnLimit
			if t.synthesis {
				limit = 1
			}
			if t.toolsClosed || t.stepInRun >= limit {
				closed := t.closeCalls(answer)
				t.lastAnswer = answer
				return closed, nil
			}
			return answer, nil
		},
		call: func(ctx context.Context, name, callID, arguments string) (string, error) {
			result, err := t.dispatch(ctx, name, json.RawMessage(arguments), t.offered)
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
}

// turnReformatInstructions is the state's instruction on the one reformat request.
var turnReformatInstructions = turn.ReformatInstructions
