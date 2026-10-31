package runcontext

import (
	"context"
	"encoding/json"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/wire"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

// Content is one part of a tool result's content (an item of McpToolResult.content in engine.ts):
// its type, its text, and every other member it carries, in the order it carries them, so the
// part's JSON is the bytes the TypeScript's is. An image part carries kind, mediaType and bytes
// instead of a type and a text.
type Content struct{ value *Value }

// NewContent is one content part from the object it is.
func NewContent(value *Value) Content { return Content{value} }

// TextContent is {type:'text',text}.
func TextContent(text string) Content {
	return Content{ObjectOf("type", String("text"), "text", String(text))}
}

// Type is the part's type member, or "" where it carries none.
func (c Content) Type() string {
	if held := c.value.Get("type"); held.IsString() {
		return held.Str()
	}
	return ""
}

// Text is the part's text member, or "" where it carries none.
func (c Content) Text() string {
	if held := c.value.Get("text"); held.IsString() {
		return held.Str()
	}
	return ""
}

// HasText reports a part that carries a text member of type string (the filter the chat's index
// and the compaction use).
func (c Content) HasText() bool { return c.value.Get("text").IsString() }

// Value is the part as it is.
func (c Content) Value() *Value { return c.value }

// ToolResult is an MCP tool result (McpToolResult in engine.ts): its content parts, its
// structured content and its isError, each kept absent where the result does not carry it,
// because what the budget counts is the bytes of the result's JSON.
type ToolResult struct {
	additional map[string]*Value
	// Content are the result's content parts; nil where it carries no content member.
	Content []Content
	// StructuredContent is the result's structured content; nil where it carries none.
	StructuredContent *Value
	// IsError is the result's isError; nil where it carries none.
	IsError *bool
	// order is the member order the result was written in, so its JSON is the TypeScript's bytes;
	// empty means content, structuredContent, isError, this struct's own order.
	order []string
	// Receipt is a host tool with a source's receipt id for this call (ADR-0006; a gateway
	// connection's), carried on the tool_result's source record and never in the result's JSON.
	Receipt string
}

// ResultOf is a tool result written content first, as a tool's own answer is.
func ResultOf(content []Content, structured *Value) ToolResult {
	return ToolResult{Content: content, StructuredContent: structured}
}

// Answer is answer in context/ledger.ts: one value as a tool result, its structured content and
// the same value as the one text part, in that order.
func Answer(value *Value) ToolResult {
	return ToolResult{
		Content:           []Content{TextContent(value.Text())},
		StructuredContent: value,
		order:             []string{"structuredContent", "content", "isError"},
	}
}

// WithIsError is the result with its isError set, after the members it already carries.
func (r ToolResult) WithIsError(isError bool) ToolResult {
	order := r.order
	if len(order) == 0 {
		order = []string{"content", "structuredContent", "isError"}
	}
	r.order = append(append([]string{}, order...), "isError")
	r.IsError = &isError
	return r
}

// Err reports isError === true.
func (r ToolResult) Err() bool { return r.IsError != nil && *r.IsError }

// Value is the result as one JSON value, its members in the order it was written in.
func (r ToolResult) Value() *Value {
	out := Object()
	written := map[string]bool{}
	order := r.order
	if len(order) == 0 {
		order = []string{"content", "structuredContent", "isError"}
	}
	for _, name := range order {
		if written[name] {
			continue
		}
		written[name] = true
		switch name {
		case "content":
			if r.Content != nil {
				parts := Array()
				for _, part := range r.Content {
					parts.Append(part.Value())
				}
				out.Set("content", parts)
			}
		case "structuredContent":
			if r.StructuredContent != nil {
				out.Set("structuredContent", r.StructuredContent)
			}
		case "isError":
			if r.IsError != nil {
				out.Set("isError", Bool(*r.IsError))
			}
		default:
			if value := r.additional[name]; value != nil {
				out.Set(name, value.Clone())
			}
		}
	}
	return out
}

// JSON is the result as JSON.stringify writes it.
func (r ToolResult) JSON() []byte { return r.Value().JSON() }

// ReadToolResult is one tool result read from the JSON a tool answered with, its member order
// kept.
func ReadToolResult(value *Value) ToolResult {
	out := ToolResult{order: value.Keys(), additional: map[string]*Value{}}
	for _, name := range value.Keys() {
		if name != "content" && name != "structuredContent" && name != "isError" {
			out.additional[name] = value.Get(name).Clone()
		}
	}
	if content := value.Get("content"); content.IsArray() {
		out.Content = []Content{}
		for _, part := range content.Items() {
			out.Content = append(out.Content, NewContent(part))
		}
	}
	if structured := value.Get("structuredContent"); structured != nil {
		out.StructuredContent = structured
	}
	if isError := value.Get("isError"); isError != nil && isError.Kind() == KindBool {
		held := isError.Truth()
		out.IsError = &held
	}
	return out
}

// Clone copies a result deeply, as structuredClone does.
func (r ToolResult) Clone() ToolResult {
	return ReadToolResult(r.Value().Clone())
}

// HostTool is a host tool's declaration and its implementation (HostTool in engine.ts). Effect,
// Interaction and Delegation are "" where the host declares none.
type HostTool struct {
	// Original-writer capability; absent from every public declaration and wire format.
	taskDelegation *taskDelegationRegistration
	Taxonomy       *wire.ToolTaxonomy
	TaxonomyLimit  *TaxonomyLimit
	Annotations    map[string]bool
	// Artifacts authenticates artifact observations from an actual host result, never narration.
	Artifacts func(ToolResult, json.RawMessage) []ArtifactObservation

	Name          string
	Description   string
	InputSchema   json.RawMessage
	Effect        string
	Interaction   string
	Delegation    string
	ContextPolicy *ToolContextPolicy
	Presentation  *contract.ToolOfferHostToolPresentation
	Execute       func(ctx context.Context, args json.RawMessage) (ToolResult, error)
	// Source is where the tool comes from, for a tool a source outside the host serves (ADR-0006,
	// sources.go): the host still executes it; the boundary names it for the wire, frames its
	// description, holds it to its source's bounds and reports it by source.
	Source *ToolSource
	// SourceTool is the tool's own name at its source, set by the boundary on the offered tool,
	// whose Name is then its wire name. A declaration leaves it empty.
	SourceTool string
	// EffectClass is what a call of a tool with a source may do: read, propose or act ("" is act).
	EffectClass string
	// Approval is whether each call needs the person's approval: none or required ("" is the
	// effect class's default, required for act).
	Approval string
	// Approve is the host's approval policy for a tool with a source whose calls need approval
	// (ADR-0006, ADR-0008): asked at each call with the call (ApprovalRequest: its wire name, the
	// tool's own name, its source record and a copy of its arguments) and answering at once:
	// "allow" runs it, "refuse" refuses it, "ask" pauses the run (approval_requested) until the
	// person's decision continues it. Nil, every such call asks. The package holds no grant.
	Approve ApprovalPolicy
}

// Declaration is the tool as the offer declares it to the model (tool-offer.json#/$defs/hostTool).
func (t HostTool) Declaration() contract.ToolOfferHostTool {
	out := contract.ToolOfferHostTool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema}
	if t.Taxonomy != nil {
		raw, _ := json.Marshal(t.Taxonomy)
		_ = json.Unmarshal(raw, &out.Taxonomy)
	}
	if t.Taxonomy != nil {
		raw, _ := json.Marshal(TaxonomyAnnotations(t.Taxonomy.Effect))
		_ = json.Unmarshal(raw, &out.Annotations)
	}
	if t.Effect != "" {
		effect := t.Effect
		out.Effect = &effect
	}
	if t.Interaction != "" {
		interaction := t.Interaction
		out.Interaction = &interaction
	}
	if t.Delegation != "" {
		delegation := t.Delegation
		out.Delegation = &delegation
	}
	if t.ContextPolicy != nil {
		policy := &contract.ToolOfferHostToolContextPolicy{}
		if t.ContextPolicy.Immutable {
			held := true
			policy.Immutable = &held
		}
		if t.ContextPolicy.Reference {
			held := true
			policy.Reference = &held
		}
		if t.ContextPolicy.Runtime {
			held := true
			policy.Runtime = &held
		}
		out.ContextPolicy = policy
	}
	out.Presentation = t.Presentation
	if t.Source != nil {
		record := t.Source.Record(t.SourceTool)
		out.Source = &record
	}
	if t.EffectClass != "" {
		class := t.EffectClass
		out.EffectClass = &class
	}
	if t.Approval != "" {
		approval := t.Approval
		out.Approval = &approval
	}
	return out
}

// RuntimeTool is one tool of the Runtime this session serves, by name (the member of
// session.tools this package reads).
type RuntimeTool struct {
	Offer *contract.ToolOfferRuntimeTool
	Name  string
	Meta  *ToolTaxonomy
}

// CurrentRequest is the person's latest message and the workspace context supplied with it
// (CurrentRequest in session.ts).
type CurrentRequest struct {
	Message string `json:"message"`
	Input   string `json:"input"`
}

// Value is the request as the preview encodes it: message, then input.
func (r CurrentRequest) Value() *Value {
	return ObjectOf("message", String(r.Message), "input", String(r.Input))
}

// Document is one immutable source copy a host hands the run to retain (ContextDocument in
// context/ledger.ts). Kind is history, artifact, request or tool.
type Document struct {
	ID       string
	Kind     string
	Label    string
	Value    *Value
	Revision string
}

// RuntimeIdentity is what the host knows the Runtime by: an identity (its pinned digest, or the
// name its initialize answer gave) and its version (RuntimeIdentity in context/chat.ts).
//
// Both are valid UTF-8 (#246). A run whose Input.Runtime is not is refused with
// RuntimeIdentityRefused before anything is sent. A host that reads the Runtime's answer with
// encoding/json gets U+FFFD for a lone surrogate escape without being told; read it with a reader
// that refuses lone surrogate escapes instead, so the version the host names is the one the
// Runtime gave.
type RuntimeIdentity struct {
	Identity string `json:"identity"`
	Version  string `json:"version"`
}

// CompactorRequest is ADR-0016's bounded, tool-free request to the connection selected by
// models.compactor. Tool results stay in the deterministic retained-context index.
type CompactorRequest struct {
	Messages   []Message
	Digests    []string
	MaxBytes   int
	Connection string
	Family     string
	Model      string
	Reasoning  string
}

// SteerInput is one ADR-0021 instruction the person sends while a run is in flight.
type SteerInput struct {
	Text string `json:"text"`
}

// Input is the context a host gives one run (ContextInput in session.ts).
//
// CurrentState is host-authored text that changes between requests or responses (the chat's draft
// snapshot, the latest check). An adapter sends it after the history, at the end of every
// request, with the execution state, so a change to it invalidates no cached prefix.
//
// Chat (agent#8) is the chat-scoped context the host made for this run's chat: the ledger and the
// read cache kept across the chat's runs; absent, the run's are its own and end with it. Runtime
// is what the host knows the Runtime this run's tools reach by, which keys a Runtime-served read
// in the chat's read cache. Memory (agent#11) is whether the project's memory is on: false and the
// host's MemoryFor is not asked (ADR-0005); the input allowance takes no reserve either way. Objective (agent#11) is
// a delegated child's objective, its request text, sent as the first text part of its first
// message, and the query its MemoryFor is asked with. Link (agent#14) is set by the delegation coordinator on a run it
// starts. Connection is the AI connection the run's model or agent capability is bound to, as the
// host names it (host.ts's AITarget, a string here). DelegationReuse (agent#17) is false where
// this response's delegated tasks reuse no earlier result of the chat.
type Input struct {
	Capture *LiveCaptureConfig
	// TaskSteps explicitly enables package-owned task scheduling; it grants no authority.
	TaskSteps         bool
	TaskStepEnvelope  *TaskStepEnvelope
	UsageBudget       *UsageBudgetInput
	checkpointProfile bool
	checkpointRunID   string
	RunStore          *DurableRunInput
	// Persistence is inherited only by the trusted coordinator, never decoded from a checkpoint.
	Persistence *RunBinding

	// RequestCap is a fixed API request reservation, 1..20; nil preserves ordinary grants.
	RequestCap        *int
	AcceptedDocuments []AcceptedDocument
	CurrentState      string
	ArtifactProposals *bool
	Authorize         func(ctx context.Context) error
	Checkpoint        func(ctx context.Context, state contract.Checkpoint) error
	Execution         *contract.Checkpoint
	CurrentRequest    *CurrentRequest
	Phase             string
	Plan              *Value
	PlanResolver      *PlanResolver
	PlanDrift         []string
	Configuration     *Configuration
	Documents         []Document
	Chat              ChatScope
	Runtime           *RuntimeIdentity
	Memory            *bool
	Objective         string
	Link              ExecutionLink
	Connection        string
	// ConnectionRevisions binds durable provider continuation to current host connection metadata.
	ConnectionRevisions map[string]string
	DelegationReuse     *bool
	Compactor           func(ctx context.Context, request CompactorRequest) (string, error)
	// Approval is the person's decision on the act call a paused Execution waits for (ADR-0008;
	// checkpoint.json#/$defs/approvalDecision), the continuation input beside a request grant. A
	// decision for any other call is refused before anything runs (stale-state).
	Approval *contract.CheckpointApprovalDecision
	// Steer is the host-owned in-flight input queue. Adapters drain it only at request boundaries.
	Steer <-chan SteerInput
}

// ExecutionLink is how the delegation coordinator reaches a run it starts (agent#14): told the
// run's context once its execution is initialized, before its first request.
type ExecutionLink interface{ Attach(c *SessionContext) }

// SourceLinked is the link of a run the delegation coordinator starts as a child (ADR-0006): it
// hands the child's boundary the source gate of the run that delegated it, whose report rows and
// changed sources the child's gate shares, so a child's source calls count against the turn and
// reach the parent's report. Nil where the parent holds none.
type SourceLinked interface{ ParentSources() *SourceGate }

// ModelSession is a run on a model capability (the model member of EngineSession): the family and
// the model the host named, and the capability record's two budget members where the host
// resolved them (capabilitiesFor belongs to the wire package).
type ModelSession struct {
	Family string
	Model  string
	Budget *ModelBudget
	// ToolSchemas is what the model's endpoint takes for a tool's parameters, as its capability
	// record says ("json-schema" or "gemini-subset"): how a schema load_tool loads is shown. Empty,
	// it is read from Family, as the family's default record reads it.
	ToolSchemas string
	// Thinking is the tier the run asks at (off, on or ultra; "" stays omitted): the author's tier, which
	// the critic's floor reads (ADR-0010).
	Thinking string
}

// AgentSession is a run on an agent capability (the agent member of EngineSession): the provider
// runs its own model loop.
type AgentSession struct {
	Model string
	// Effort is the reasoning effort the host's configuration asks for, or "": the author's tier,
	// which the critic's floor reads (ADR-0010).
	Effort string
}

// Session is one run as the harness is handed it (EngineSession in engine.ts), in the members
// this package reads. Model and Agent are exclusive: a run is on one capability or the other.
type Session struct {
	Route              *RouteSelection
	RouteFacts         *RouteFacts
	Policy             PolicyLayer
	PolicyProject      PolicyLayer
	TaskClass          string
	PaceUsed           map[string]int64
	PolicyConnections  []string
	PolicyFamilies     map[string]string
	PolicyLegacyModels *RunModels
	PolicyRequests     int

	Prompt            string
	TestPrompt        string
	Tools             []RuntimeTool
	HostTools         []HostTool
	RuntimeCalls      *gate.Calls
	CallTool          func(ctx context.Context, name string, args json.RawMessage) (ToolResult, error)
	Model             *ModelSession
	Agent             *AgentSession
	Context           *Input
	Purpose           string
	AllowConversation bool
	Interactive       bool
	// Critic is the run's word on the critic (ADR-0010): off, auto or on, the person's per-run
	// control; it wins over CriticProject and the table's CriticDefault. "" states none.
	Critic string
	// CriticProject is the project's word on the critic, read where the run states none.
	CriticProject string
	// CriticReasoning is the critic's tier in the enum of the run's capability; "" is the author's.
	CriticReasoning string
	// ExistingPack is the host's fact that the run works on a pack that already exists.
	ExistingPack bool
	// ReviewerAssigned is the host's fact that a human reviewer is assigned: no critic in any mode.
	ReviewerAssigned bool
	// AdversarialReview is retired in v0.9.1 and refused when nonnil; use Critic
	// Nil states none; even false alongside Critic is refused.
	AdversarialReview *bool
	// ModelsFor is the host's injection point for the models a connection names for its critic
	// and its delegated children (modelsFor in host.ts), asked once per run through
	// SessionContext.Models. Absent, the run's own model is both.
	ModelsFor func(RunIdentity) (NamedModels, error)
	// Host is the host's side of the prompt (ADR-0005): the brief, the project's own instructions
	// and recalled memory, asked once per run through SessionContext.AskHost. Nil supplies none.
	Host Host
	// Skills are the skills the host declares for the run (ADR-0009, skills.go), beside its host
	// tools, each with the digest the person approved. A delegated child is handed none.
	Subagents       []Subagent
	ChildDefinition *Subagent
	Skills          []Skill
}

// ModelName is the model the run is on, whichever capability it is.
func (s Session) ModelName() string {
	if s.Model != nil {
		return s.Model.Model
	}
	if s.Agent != nil {
		return s.Agent.Model
	}
	return ""
}

// Output is a tool-result part's output (the output member of a tool-result part): its type and
// its value.
type Output struct {
	Type  string
	Value *Value
}

// JSONValue is the output as JSON.stringify writes it.
func (o Output) JSONValue() *Value {
	out := ObjectOf("type", String(o.Type))
	if o.Value != nil {
		out.Set("value", o.Value)
	}
	return out
}

// Part is one part of a message's content, in the projection the compaction reads (the parts of
// ModelMessage session.ts uses: text, reasoning, tool-call and tool-result).
type Part struct {
	Type       string
	Text       string
	ToolCallID string
	ToolName   string
	Input      *Value
	Output     *Output
	// ProviderOptions is the transport state a provider carries on a part (a signed reasoning
	// block among it); the compaction keeps it out of what it retains.
	ProviderOptions *Value
	// Other is a part this projection does not name, kept whole (an approval request among them).
	Other *Value
}

// TextPart, ReasoningPart, ToolCallPart and ToolResultPart are the four parts by name.
func TextPart(text string) Part { return Part{Type: "text", Text: text} }
func ReasoningPart(text string) Part {
	return Part{Type: "reasoning", Text: text}
}
func ToolCallPart(id, name string, input *Value) Part {
	return Part{Type: "tool-call", ToolCallID: id, ToolName: name, Input: input}
}
func ToolResultPart(id, name string, output Output) Part {
	return Part{Type: "tool-result", ToolCallID: id, ToolName: name, Output: &output}
}

// Value is the part as JSON.stringify writes it, its members in the order the TypeScript's
// literal writes them.
func (p Part) Value() *Value {
	if p.Other != nil {
		return p.Other
	}
	out := ObjectOf("type", String(p.Type))
	switch p.Type {
	case "text", "reasoning":
		out.Set("text", String(p.Text))
	case "tool-call":
		out.Set("toolCallId", String(p.ToolCallID))
		out.Set("toolName", String(p.ToolName))
		out.Set("input", orNull(p.Input))
	case "tool-result":
		out.Set("toolCallId", String(p.ToolCallID))
		out.Set("toolName", String(p.ToolName))
		out.Set("output", p.Output.JSONValue())
	}
	if p.ProviderOptions != nil {
		out.Set("providerOptions", p.ProviderOptions)
	}
	return out
}

func orNull(v *Value) *Value {
	if v == nil {
		return Null()
	}
	return v
}

// Message is one message of the model's history as the compaction reads it (ModelMessage of the
// ai package as session.ts uses it): a role (user, assistant or tool) and a content that is one
// text or a list of parts.
type Message struct {
	Role string
	// Text is the content where the content is one string (Parts nil).
	Text string
	// Parts is the content where the content is a list of parts.
	Parts []Part
}

// TextMessage is a message whose content is one string.
func TextMessage(role, text string) Message { return Message{Role: role, Text: text} }

// PartsMessage is a message whose content is a list of parts.
func PartsMessage(role string, parts ...Part) Message { return Message{Role: role, Parts: parts} }

// IsText reports a message whose content is one string.
func (m Message) IsText() bool { return m.Parts == nil }

// Value is the message as JSON.stringify writes it: role, then content.
func (m Message) Value() *Value {
	out := ObjectOf("role", String(m.Role))
	if m.Parts == nil {
		out.Set("content", String(m.Text))
		return out
	}
	parts := Array()
	for _, part := range m.Parts {
		parts.Append(part.Value())
	}
	out.Set("content", parts)
	return out
}

// MessagesValue is a list of messages as one JSON value.
func MessagesValue(messages []Message) *Value {
	out := Array()
	for _, message := range messages {
		out.Append(message.Value())
	}
	return out
}
