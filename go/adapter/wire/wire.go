// Package wire carries the three model wires the Go adapter speaks over the host's keyless relay
// (ADR-0004, ADR-0003), as the deepagents adapter's engines/deepagents/wire.ts does for that one:
// a request body built for one family out of neutral messages, and the answer read back from the
// relay as a stream or as one whole object, whatever was asked for.
//
// Why the adapter writes its own wires rather than using a framework's provider clients, measured
// against the pinned versions: a provider client reaches for the process's own transport or
// carries a credential header of its own, and the conformance session's seal refuses both; it
// reads reasoning from one member only, never carries reasoning back on the next request, and
// fails on a whole answer to a streamed request. So each wire is written here, once, with the
// shapes the conformance session holds every model adapter to: the members each wire format
// defines, the execution state where the capability record places it, the cache breakpoint on
// Anthropic, the tier's members from the host's table (thinking.go), signed reasoning carried back
// exactly as it arrived, and the answer read off its content type.
//
// Nothing here holds an address or a credential: a request is a path suffix and a body, handed to
// the relay capability the host passes in (ModelCall).
package wire

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
)

// IMAGES_HEADER's text: the head of the message that shows a vision model a result's images. The
// vercel adapter's words (IMAGES_HEADER in engines/deepagents/wire.ts).
const ImagesHeader = "IMAGES from"

// UnparsedArguments is UNPARSED: the member a call's arguments are kept under where they could not
// be read as an object. The TypeScript carries it inside a LangChain message's args; here a call
// keeps the argument text the model wrote and ToolCall.Unparsed answers it, so the name is
// exported for the boundary that reads the TypeScript's shape.
const UnparsedArguments = "__judgmentPackUnparsedArguments"

// cacheBreakpoint is Anthropic's prompt-cache breakpoint, placed after the system head and after
// the tools and, under trailing-user placement only, after the request text (CACHE_BREAKPOINT).
func cacheBreakpoint() *Value { return Obj(F("type", Str("ephemeral"))) }

// ModelRequest is one model request as an adapter hands it to the host: the protocol's headers and
// the body, never an address and never a credential. The same shape as go/certification's.
type ModelRequest struct {
	Method  string // POST unless GET
	Headers map[string]string
	Body    []byte
}

// ModelCall is the host's relay capability (session.model.call): a path suffix after the relay's
// mount, and a request. The host builds the address and writes its own session as the bearer; the
// relay attaches the provider's key on the far side. An adapter's only reach to a model is this.
type ModelCall func(ctx context.Context, suffix string, request ModelRequest) (*http.Response, error)

// Role is a neutral message's role.
type Role string

// The four roles a neutral message takes.
const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Part is one part of a message: a text, or an image. The TypeScript reads these off a LangChain
// message's content blocks.
type Part struct {
	Text string
	// Image is set where this part is an image rather than a text.
	Image *ImagePart
}

// TextPart is one text part.
func TextPart(text string) Part { return Part{Text: text} }

// ImagePartOf is one image part.
func ImagePartOf(image ImagePart) Part { return Part{Image: &image} }

// ToolCall is one tool call, with its arguments as the model wrote them. The TypeScript keeps a
// parsed object and, where the arguments were not one, the text under UNPARSED; this keeps the
// text, and Args and Unparsed read it exactly as argumentsOf does.
type ToolCall struct {
	ID   string
	Name string
	// Arguments is the argument text as the endpoint sent it. "" is no arguments at all.
	Arguments string
}

// Args is the call's arguments as an object, as a wire carries them back; an empty object where
// they could not be read as one.
func (c ToolCall) Args() *Value {
	args, _ := argumentsOf(c.Arguments)
	return args
}

// Unparsed is the argument text where it was not a JSON object, and "" where it was.
func (c ToolCall) Unparsed() string {
	_, unparsed := argumentsOf(c.Arguments)
	return unparsed
}

// Replay is the carried-back form of one answer, per family (Replay in
// engines/deepagents/wire.ts): what goes back on the next request, exactly as it arrived —
// Anthropic's content blocks (signed thinking among them), Gemini's parts (signed thoughts and
// signed calls among them), or the OpenAI-compatible reasoning.
type Replay struct {
	Family EndpointKind
	// Blocks is the anthropic family's content blocks.
	Blocks []*Value
	// Parts is the gemini family's parts.
	Parts []*Value
	// Reasoning is the openai-compatible family's reasoning text.
	Reasoning string
}

// Message is one neutral message of the conversation, without its system text. The TypeScript
// takes LangChain messages; the Eino adapter converts to this.
type Message struct {
	Role Role
	// Parts are the message's text and image parts, in order.
	Parts []Part
	// ToolCalls are an assistant message's calls.
	ToolCalls []ToolCall
	// Replay is the replay of the answer this assistant message carried, where it had one.
	Replay *Replay
	// CallID is a tool message's call, the id of the call it answers.
	CallID string
	// Name is a tool message's tool.
	Name string
	// IsError marks a tool message that failed (a LangChain ToolMessage's error status).
	IsError bool
	// Images are the images a tool result carries for a vision model (the TypeScript keeps them on
	// the message's artifact).
	Images []ImagePart
}

// Text is messageText: the text of a message's content, its text parts joined.
func (m Message) Text() string {
	text := ""
	for _, part := range m.Parts {
		if part.Image == nil {
			text += part.Text
		}
	}
	return text
}

// UserText is humanText: a user message of text parts, in the shape the encoders read.
func UserText(parts ...string) Message {
	message := Message{Role: RoleUser}
	for _, text := range parts {
		message.Parts = append(message.Parts, TextPart(text))
	}
	return message
}

// UserWithImages is humanWithImages: a user message with images after its text, for a vision model
// on the text path.
func UserWithImages(text string, images []ImagePart) Message {
	message := Message{Role: RoleUser, Parts: []Part{TextPart(text)}}
	for _, image := range images {
		message.Parts = append(message.Parts, ImagePartOf(image))
	}
	return message
}

// AnswerMessage is answerMessage: one answer as the assistant message the agent carries — its
// text, its calls and its replay. The TypeScript also puts the finish reason and the usage on the
// message's response metadata; here they stay on the Answer, which the caller holds.
func AnswerMessage(answer Answer) Message {
	return Message{
		Role:      RoleAssistant,
		Parts:     []Part{TextPart(answer.Text)},
		ToolCalls: answer.ToolCalls,
		Replay:    &answer.Replay,
	}
}

// WireTool is one tool as the model is shown it: its name, description and the schema the record
// says it takes (WireTool).
type WireTool struct {
	Name        string
	Description string
	Schema      *Value
}

// SystemText is the stable system text, and the execution state where the placement puts it at the
// system's end (WireRequest's `system`). Tail is "" where the state rides elsewhere.
type SystemText struct {
	Head string
	Tail string
	// HasTail tells an empty tail from none, as the TypeScript tells "" from undefined.
	HasTail bool
}

// ImageBounds is the images one request carries, at most, newest first.
type ImageBounds struct {
	Count int
	Bytes int
}

// Request is what one request asks, besides its messages (WireRequest).
type Request struct {
	Family EndpointKind
	Model  string
	System SystemText
	// Messages is the conversation, without its system message.
	Messages []Message
	// Tools are the tools offered on the wire; none sends no tool member at all.
	Tools []WireTool
	// OneCallPerTurn is where the record says the endpoint takes no parallel calls.
	OneCallPerTurn bool
	// Tier is the tier's members, from the host's table (thinking.go), or nil.
	Tier *Value
	// MaxOutputTokens is the output reserve the budget table states.
	MaxOutputTokens int
	// ReasoningField is the OpenAI-compatible member reasoning streams in and is carried back in,
	// where the record pins one; "" is not pinned.
	ReasoningField ReasoningField
	// WholeAnswer: the endpoint answers whole whatever it is asked, so it is asked for one.
	WholeAnswer bool
	// Images is the bound on the images one request carries.
	Images ImageBounds
	// Placement is where the run's state sits (decided once per run): the Anthropic request's third
	// cache breakpoint, on the request text, is placed under trailing-user only.
	Placement StatePlacement
}

// Encoded is the suffix, body and protocol headers one request is sent with (Encoded).
type Encoded struct {
	Suffix  string
	Body    []byte
	Headers map[string]string
}

// ModelRequest is the encoded request as the relay capability takes it.
func (e Encoded) ModelRequest() ModelRequest {
	return ModelRequest{Method: http.MethodPost, Headers: e.Headers, Body: e.Body}
}

// FinishReason is the provider-neutral completion reason (ModelCompletion's finishReason, whose
// values are completion.ts's COMPLETION_REASONS).
type FinishReason string

// The six reasons an answer ends for.
const (
	FinishStop          FinishReason = "stop"
	FinishLength        FinishReason = "length"
	FinishContentFilter FinishReason = "content-filter"
	FinishToolCalls     FinishReason = "tool-calls"
	FinishError         FinishReason = "error"
	FinishOther         FinishReason = "other"
)

// InputTokenDetails are the input figures a provider reported beyond the total.
type InputTokenDetails struct {
	CacheReadTokens  *int
	CacheWriteTokens *int
}

// OutputTokenDetails are the output figures a provider reported beyond the total.
type OutputTokenDetails struct {
	ReasoningTokens *int
}

// Usage is the usage figures a provider reported, in the vocabulary completion.ts reads
// (WireUsage).
type Usage struct {
	InputTokens        *int
	OutputTokens       *int
	InputTokenDetails  *InputTokenDetails
	OutputTokenDetails *OutputTokenDetails
}

// Answer is one answer, read whole (WireAnswer).
type Answer struct {
	Text         string
	ToolCalls    []ToolCall
	FinishReason FinishReason
	Usage        Usage
	// Replay is what goes back on the next request, exactly as it arrived.
	Replay Replay
}

// Listener is what the reader says as it reads, in order: the prose, the reasoning, and where a
// passage ends (WireListener). A callback that answers an error ends the read with it.
type Listener interface {
	Text(delta string) error
	Reasoning(delta string) error
	// ReasoningEnd is one passage of reasoning ended; text is the whole of it (empty for a signed
	// thought with none).
	ReasoningEnd(text string) error
}

// NoListener is told nothing and says nothing, for a read whose prose nobody is watching.
type NoListener struct{}

// Text does nothing.
func (NoListener) Text(string) error { return nil }

// Reasoning does nothing.
func (NoListener) Reasoning(string) error { return nil }

// ReasoningEnd does nothing.
func (NoListener) ReasoningEnd(string) error { return nil }

// RelayRefusal is a refusal the relay answered, for the failure catalogue to classify
// (RelayRefusal; go/failure's Refusal in Go).
type RelayRefusal struct {
	Status  int
	Body    string
	Headers http.Header
}

// Error is the sentence the TypeScript's own carries, with the status in it and nothing of the
// endpoint's prose.
func (e *RelayRefusal) Error() string { return fmt.Sprintf("the model endpoint answered %d", e.Status) }

// StatusCode is the refusal's status.
func (e *RelayRefusal) StatusCode() int { return e.Status }

// ResponseBody is the refusal's body, read only for the catalogue's codes.
func (e *RelayRefusal) ResponseBody() string { return e.Body }

// ResponseHeader is the refusal's headers, read only for the request id and the retry time.
func (e *RelayRefusal) ResponseHeader() http.Header { return e.Headers }

// RelayStreamError is an error the endpoint sent inside an answer that had begun: its envelope
// (type, code) for the catalogue's aliases, and no status, so it is classified unknown as a broken
// stream is (RelayStreamError).
type RelayStreamError struct {
	Envelope *Value
}

// Error is the sentence the TypeScript's own carries.
func (e *RelayStreamError) Error() string {
	return "the model endpoint reported an error inside its answer"
}

// StatusCode is none: this is not a refusal of a request, and nothing is read from a status.
func (e *RelayStreamError) StatusCode() int { return 0 }

// ResponseBody is the envelope the endpoint sent, as JSON.stringify writes it.
func (e *RelayStreamError) ResponseBody() string {
	if e.Envelope == nil {
		return "{}"
	}
	return e.Envelope.String()
}

// ResponseHeader is none.
func (e *RelayStreamError) ResponseHeader() http.Header { return nil }

// RelayUnreadable is an answer that could not be read: not JSON where JSON was the only thing it
// could be (RelayUnreadable).
type RelayUnreadable struct {
	Detail string
}

// Error is the detail the TypeScript's own carries as its message.
func (e *RelayUnreadable) Error() string { return e.Detail }

// FailureCode is the class this error names: invalid-response.
func (e *RelayUnreadable) FailureCode() string { return "invalid-response" }

// RandomUUID is crypto.randomUUID(): a random version-4 UUID, for a tool call an endpoint sent
// without an id of its own.
func RandomUUID() string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		panic(err)
	}
	bytes[6] = bytes[6]&0x0f | 0x40
	bytes[8] = bytes[8]&0x3f | 0x80
	text := hex.EncodeToString(bytes[:])
	return text[0:8] + "-" + text[8:12] + "-" + text[12:16] + "-" + text[16:20] + "-" + text[20:32]
}
