package eino

import (
	"strings"

	"github.com/cloudwego/eino/schema"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/turn"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/wire"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
)

// What one request is made of besides the framework's history: the state text and where it sits,
// the budgeted history, and the completion an answer reports.

// listener is told the answer as the wire reads it, and puts the prose, the reasoning and the end
// of each passage on the run's ordered events before the step's calls are dispatched.
type listener struct {
	r          *runState
	publicText string
}

func (l *listener) Text(delta string) error {
	if !l.r.run.Interactive {
		return nil
	}
	l.publicText += delta
	l.r.deliver(contract.RunEvent{MessageProgress: &contract.RunEventMessageProgress{Type: "message_progress", Text: turn.StreamingProse(l.publicText)}})
	return nil
}

func (l *listener) Reasoning(delta string) error {
	if delta != "" {
		l.r.deliver(contract.RunEvent{Reasoning: &contract.RunEventReasoning{Type: "reasoning", Text: delta, Done: false}})
	}
	return nil
}

func (l *listener) ReasoningEnd(text string) error {
	l.r.deliver(contract.RunEvent{Reasoning: &contract.RunEventReasoning{Type: "reasoning", Text: text, Done: true}})
	l.r.slot.SawReasoning()
	return nil
}

// stateText is the state's text: the header, then whatever is current for this request.
func stateText(parts ...string) string {
	held := []string{turn.StateHeader}
	for _, part := range parts {
		if part != "" {
			held = append(held, part)
		}
	}
	return strings.Join(held, "\n")
}

// placeState is one request's messages and system text with the state placed (placeState in the
// deepagents loop): where the history ends with a user message the state is a second text part of
// it; after a tool result it is one user message after the history, or the end of the system text,
// as the placement says. The history itself is never changed.
func placeState(placement wire.StatePlacement, history []*schema.Message, system, state string) (wire.SystemText, []*schema.Message) {
	if len(history) > 0 && history[len(history)-1].Role == schema.User {
		last := history[len(history)-1]
		carried := userMessage(append(userTexts(last), state)...)
		carried.Extra = last.Extra
		out := append(append([]*schema.Message{}, history[:len(history)-1]...), carried)
		return wire.SystemText{Head: system}, out
	}
	if placement == "trailing-user" {
		return wire.SystemText{Head: system}, append(append([]*schema.Message{}, history...), userMessage(state))
	}
	return wire.SystemText{Head: system, Tail: state, HasTail: true}, history
}

// completionOf is the completion one answer reported, in the contract's shape.
func completionOf(answer wire.Answer) contract.ProvenanceCompletion {
	usage := failure.Usage{InputTokens: int64Of(answer.Usage.InputTokens), OutputTokens: int64Of(answer.Usage.OutputTokens)}
	if details := answer.Usage.OutputTokenDetails; details != nil {
		usage.ReasoningTokens = int64Of(details.ReasoningTokens)
	}
	if details := answer.Usage.InputTokenDetails; details != nil {
		usage.CacheReadTokens = int64Of(details.CacheReadTokens)
		usage.CacheWriteTokens = int64Of(details.CacheWriteTokens)
	}
	return failure.ModelCompletion(string(answer.FinishReason), usage)
}

func int64Of(v *int) *int64 {
	if v == nil {
		return nil
	}
	held := int64(*v)
	return &held
}

// projected is the history in the shape the context's budget and compaction read (the ai
// package's message shape as session.ts counts it), one for one.
func (r *runState) projected(history []*schema.Message) []runcontext.Message {
	out := make([]runcontext.Message, 0, len(history))
	for _, message := range history {
		switch message.Role {
		case schema.Assistant:
			var parts []runcontext.Part
			if message.Content != "" {
				parts = append(parts, runcontext.TextPart(message.Content))
			}
			if row, ok := r.side.find(message); ok && row.replay != nil {
				if reasoning := replayText(*row.replay); reasoning != "" {
					parts = append(parts, runcontext.ReasoningPart(reasoning))
				}
			}
			for _, call := range message.ToolCalls {
				input, _ := runcontext.ParseJSON([]byte(argumentsOrEmpty(call.Function.Arguments)))
				if input == nil {
					input = runcontext.ObjectOf()
				}
				parts = append(parts, runcontext.ToolCallPart(call.ID, call.Function.Name, input))
			}
			if parts == nil {
				parts = []runcontext.Part{}
			}
			out = append(out, runcontext.PartsMessage("assistant", parts...))
		case schema.Tool:
			out = append(out, runcontext.PartsMessage("tool", runcontext.ToolResultPart(message.ToolCallID, message.ToolName,
				runcontext.Output{Type: "text", Value: runcontext.String(message.Content)})))
		default:
			texts := userTexts(message)
			if len(message.MultiContent) == 0 {
				out = append(out, runcontext.TextMessage("user", message.Content))
				continue
			}
			parts := make([]runcontext.Part, 0, len(texts))
			for _, text := range texts {
				parts = append(parts, runcontext.TextPart(text))
			}
			out = append(out, runcontext.PartsMessage("user", parts...))
		}
	}
	return out
}

func argumentsOrEmpty(arguments string) string {
	if strings.TrimSpace(arguments) == "" {
		return "{}"
	}
	return arguments
}

// replayText is the reasoning an answer's replay carries, as the projection counts it: the
// reasoning text on the OpenAI-compatible wire, the signed blocks or parts as JSON elsewhere.
func replayText(replay wire.Replay) string {
	switch replay.Family {
	case wire.OpenAICompatible:
		return replay.Reasoning
	case wire.Anthropic:
		held := wire.Arr()
		for _, block := range replay.Blocks {
			kind := block.Get("type")
			if kind != nil && (kind.Text() == "text" || kind.Text() == "tool_use") {
				continue
			}
			held.Append(block)
		}
		if len(held.Items()) == 0 {
			return ""
		}
		return held.String()
	case wire.Gemini:
		held := wire.Arr()
		for _, part := range replay.Parts {
			thought := part.Get("thought")
			signed := part.Get("thoughtSignature")
			flag := false
			if thought != nil {
				flag, _ = thought.Boolean()
			}
			if flag || signed != nil && signed.IsString() {
				held.Append(part)
			}
		}
		if len(held.Items()) == 0 {
			return ""
		}
		return held.String()
	}
	return ""
}

// budgeted is the history within the budget: the context's compaction over the projection, mapped
// back. The compaction answers the first message, its summary where it made one, and a suffix of
// the rest; the summary is a new user message.
func (r *runState) budgeted(history []*schema.Message, instructions string, schemas *runcontext.Value) ([]*schema.Message, error) {
	view := r.projected(history)
	held, err := r.sc.Messages(view, instructions, schemas)
	if err != nil {
		return nil, err
	}
	if len(held) == len(view) {
		return history, nil
	}
	if len(held) < 2 || len(history) == 0 {
		return history, nil
	}
	kept := len(held) - 2
	out := []*schema.Message{history[0], userMessage(held[1].Text)}
	return append(out, history[len(history)-kept:]...), nil
}
