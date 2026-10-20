package eino

import (
	"context"
	"errors"
	"strconv"
	"sync"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/wire"
)

// The adapter's chat model: an Eino model.ToolCallingChatModel whose only reach to a model is the
// host's relay capability (ADR-0004, decision 1). It holds no key, no address and no client of its
// own. What one request carries besides its messages (the system text and where the execution state
// sits, the tools the request shows, the tier, one call per turn) is the boundary's decision, made
// in its WrapModel hook and handed to the model with the request's context (stepKey); nothing about
// a request is decided here.

// stepKey carries one request's settings from the boundary to the model.
type stepKey struct{}

// stepRequest is what the boundary decided for one request.
type stepRequest struct {
	system  wire.SystemText
	tools   []wire.WireTool
	oneCall bool
	tier    *wire.Value
	// answered receives the answer as the wire read it, for the boundary's accounting.
	answered func(wire.Answer)
}

// relayModel is the chat model.
type relayModel struct {
	run   *runState
	model string
}

var _ model.ToolCallingChatModel = (*relayModel)(nil)

// WithTools returns the model: the tools a request shows are the boundary's (stepRequest), never
// what the framework binds.
func (m *relayModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) { return m, nil }

// Generate sends one request through the relay and reads its answer.
func (m *relayModel) Generate(ctx context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	step, ok := ctx.Value(stepKey{}).(*stepRequest)
	if !ok || step == nil {
		return nil, errors.New("a model request reached the relay without the boundary's settings; nothing was sent")
	}
	r := m.run
	messages := make([]wire.Message, 0, len(input))
	for _, message := range input {
		messages = append(messages, r.toWire(message))
	}
	encoded := wire.EncodeRequest(wire.Request{
		Family:          r.family,
		Model:           m.model,
		System:          step.system,
		Messages:        messages,
		Tools:           step.tools,
		OneCallPerTurn:  step.oneCall && len(step.tools) > 0,
		Tier:            step.tier,
		MaxOutputTokens: r.outputTokens(),
		ReasoningField:  r.reasoningField(),
		WholeAnswer:     r.capabilities.StreamingQuirks.WholeJSONAnswer,
		Images:          r.imageBounds(),
		Placement:       r.placement,
	})
	response, err := r.call(ctx, encoded.Suffix, encoded.ModelRequest())
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		body, _ := readLimited(response)
		return nil, &wire.RelayRefusal{Status: response.StatusCode, Body: body, Headers: response.Header}
	}
	answer, err := wire.ReadAnswer(ctx, r.family, response, r.listener, r.reasoningField(), func() string { return "call-" + wire.RandomUUID() })
	if err != nil {
		return nil, err
	}
	if step.answered != nil {
		step.answered(answer)
	}
	return r.fromAnswer(answer), nil
}

// Stream is Generate's answer as a stream of one: the answer's events are said by the wire's
// listener as it reads (ADR-0004: the events come from the model and the boundary, not from Eino's
// stream), and the runner is not asked to stream.
func (m *relayModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	message, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

// The side table: what an Eino message cannot hold as the wire needs it (an answer's replay, a
// tool message's failure and images), kept by the run and found again by an id in the message's
// Extra. A value in Extra stays a string, which Eino's state can carry.
const extraKey = "judgmentPack"

type held struct {
	replay  *wire.Replay
	isError bool
	images  []wire.ImagePart
}

type sideTable struct {
	mu    sync.Mutex
	next  int
	rows  map[string]held
	calls map[string]held
}

// byCall keeps a tool message's failure and images by the call it answers, for the tool message
// Eino's tools node writes from the text the boundary returned.
func (t *sideTable) byCall(callID string, row held) {
	if callID == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.calls == nil {
		t.calls = map[string]held{}
	}
	t.calls[callID] = row
}

func (t *sideTable) keep(row held) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.rows == nil {
		t.rows = map[string]held{}
	}
	t.next++
	id := "jp-" + strconv.Itoa(t.next)
	t.rows[id] = row
	return id
}

func (t *sideTable) find(message *schema.Message) (held, bool) {
	id, _ := message.Extra[extraKey].(string)
	t.mu.Lock()
	defer t.mu.Unlock()
	if id == "" {
		if message.Role == schema.Tool && message.ToolCallID != "" {
			row, ok := t.calls[message.ToolCallID]
			return row, ok
		}
		return held{}, false
	}
	row, ok := t.rows[id]
	return row, ok
}

// fromAnswer is one answer as the assistant message the agent carries: its text, its calls with
// their arguments as the model wrote them, and its replay in the side table.
func (r *runState) fromAnswer(answer wire.Answer) *schema.Message {
	out := &schema.Message{Role: schema.Assistant, Content: answer.Text}
	for _, call := range answer.ToolCalls {
		out.ToolCalls = append(out.ToolCalls, schema.ToolCall{ID: call.ID, Type: "function", Function: schema.FunctionCall{Name: call.Name, Arguments: call.Arguments}})
	}
	replay := answer.Replay
	out.Extra = map[string]any{extraKey: r.side.keep(held{replay: &replay})}
	return out
}

// userMessage is a user message of text parts.
func userMessage(parts ...string) *schema.Message {
	if len(parts) == 1 {
		return &schema.Message{Role: schema.User, Content: parts[0]}
	}
	message := &schema.Message{Role: schema.User}
	for _, text := range parts {
		message.MultiContent = append(message.MultiContent, schema.ChatMessagePart{Type: schema.ChatMessagePartTypeText, Text: text})
	}
	return message
}

// userTexts are a user message's text parts.
func userTexts(message *schema.Message) []string {
	if len(message.MultiContent) == 0 {
		return []string{message.Content}
	}
	var texts []string
	for _, part := range message.MultiContent {
		if part.Type == schema.ChatMessagePartTypeText {
			texts = append(texts, part.Text)
		}
	}
	return texts
}

// toolMessage is one call's answer as the tool message the agent carries.
func (r *runState) toolMessage(callID, name, text string, isError bool, images []wire.ImagePart) *schema.Message {
	return &schema.Message{Role: schema.Tool, ToolCallID: callID, ToolName: name, Content: text,
		Extra: map[string]any{extraKey: r.side.keep(held{isError: isError, images: images})}}
}

// toWire is one Eino message as the wire's neutral message.
func (r *runState) toWire(message *schema.Message) wire.Message {
	row, _ := r.side.find(message)
	switch message.Role {
	case schema.Assistant:
		out := wire.Message{Role: wire.RoleAssistant, Parts: []wire.Part{wire.TextPart(message.Content)}, Replay: row.replay}
		for _, call := range message.ToolCalls {
			out.ToolCalls = append(out.ToolCalls, wire.ToolCall{ID: call.ID, Name: call.Function.Name, Arguments: call.Function.Arguments})
		}
		return out
	case schema.Tool:
		return wire.Message{Role: wire.RoleTool, CallID: message.ToolCallID, Name: message.ToolName, Parts: []wire.Part{wire.TextPart(message.Content)}, IsError: row.isError, Images: row.images}
	case schema.System:
		return wire.Message{Role: wire.RoleSystem, Parts: []wire.Part{wire.TextPart(message.Content)}}
	}
	out := wire.Message{Role: wire.RoleUser}
	for _, text := range userTexts(message) {
		out.Parts = append(out.Parts, wire.TextPart(text))
	}
	for _, image := range row.images {
		out.Parts = append(out.Parts, wire.ImagePartOf(image))
	}
	return out
}
