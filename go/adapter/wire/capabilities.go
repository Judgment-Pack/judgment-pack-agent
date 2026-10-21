package wire

import (
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
)

// The capability record (capabilities.ts): what one endpoint and model can do, and the whole of
// what a loop reads where it decides — the thinking dialect, whether a schema is narrowed, whether
// tools are offered at all and one call per turn, how reasoning is read, images, the budget, and
// where the execution state goes. The family stays the wire; the record says what the endpoint
// behind that wire does.

// ReasoningKind is how the endpoint is asked to reason, if at all (ReasoningKind in
// capabilities.ts).
type ReasoningKind string

// The four reasoning kinds a record may name.
const (
	AnthropicThinking ReasoningKind = "anthropic-thinking"
	GeminiThinking    ReasoningKind = "gemini-thinking"
	OpenAIEffort      ReasoningKind = "openai-effort"
	NoReasoning       ReasoningKind = "none"
)

// ReasoningKinds is REASONING_KINDS: the kinds in the order capabilities.ts states them.
var ReasoningKinds = []ReasoningKind{AnthropicThinking, GeminiThinking, OpenAIEffort, NoReasoning}

// ReasoningField is the stream member an OpenAI-compatible endpoint puts its reasoning in
// (ReasoningField in capabilities.ts). "" is the TypeScript null: not pinned.
type ReasoningField string

// The two members reasoning may stream in.
const (
	FieldReasoning        ReasoningField = "reasoning"
	FieldReasoningContent ReasoningField = "reasoning_content"
)

// ReasoningFields is REASONING_FIELDS.
var ReasoningFields = []ReasoningField{FieldReasoning, FieldReasoningContent}

// StatePlacement is where a loop puts the execution state after the first request (StatePlacement
// in capabilities.ts). "" is absent: by family and tier.
type StatePlacement string

// The two placements a record may name.
const (
	TrailingUser StatePlacement = "trailing-user"
	SystemTail   StatePlacement = "system-tail"
)

// StatePlacements is STATE_PLACEMENTS.
var StatePlacements = []StatePlacement{TrailingUser, SystemTail}

// ToolSchemas is what the endpoint takes for a tool's parameters (ToolSchemas in capabilities.ts).
type ToolSchemas string

// The two schema shapes an endpoint takes.
const (
	JSONSchema   ToolSchemas = "json-schema"
	GeminiSubset ToolSchemas = "gemini-subset"
)

// ToolSchemasValues is TOOL_SCHEMAS.
var ToolSchemasValues = []ToolSchemas{JSONSchema, GeminiSubset}

// ReasoningCapability is the record's `reasoning` (ReasoningCapability in capabilities.ts).
type ReasoningCapability struct {
	Kind ReasoningKind
	// Ignored: the endpoint accepts the parameter and does nothing with it, so nothing is sent.
	// Absent where the record does not name it.
	Ignored *bool
}

// StreamingQuirks is the record's `streamingQuirks` (StreamingQuirks in capabilities.ts).
type StreamingQuirks struct {
	// ReasoningField pins the member reasoning streams in and is carried back in; "" is not pinned.
	ReasoningField ReasoningField
	// WholeJSONAnswer: the endpoint answers whole whatever it is asked, so it is asked for a whole
	// answer.
	WholeJSONAnswer bool
}

// ModelCapabilities is one endpoint and model as a loop reads it (ModelCapabilities in
// capabilities.ts, the contract's capability.json).
type ModelCapabilities struct {
	Family           string
	Tiers            map[string]string
	CacheBreakpoints bool
	// ContextWindowTokens and OutputTokens come together, and replace the budget table's.
	ContextWindowTokens *int
	OutputTokens        *int
	NativeToolCalls     bool
	ParallelToolCalls   bool
	Vision              bool
	Reasoning           ReasoningCapability
	StreamingQuirks     StreamingQuirks
	ToolSchemas         ToolSchemas
	// StatePlacement is absent as "": by family and tier.
	StatePlacement StatePlacement
	// StrictAlternation: the endpoint refuses two user turns in a row and a user text after a tool
	// result. Absent where the record does not name it.
	StrictAlternation *bool
}

// CapabilityMembers is CAPABILITY_MEMBERS: every member a record may carry, and the whole of them,
// in the order the contract's capability.json states them.
var CapabilityMembers = []string{
	"contextWindowTokens", "outputTokens", "nativeToolCalls", "parallelToolCalls", "vision",
	"reasoning", "streamingQuirks", "toolSchemas", "statePlacement", "strictAlternation", "family", "tiers", "cacheBreakpoints",
}

// WireReasoning is WIRE_REASONING: the reasoning kinds each wire speaks. A record that names
// another is refused for that wire.
func WireReasoning(family EndpointKind) []ReasoningKind {
	switch family {
	case Anthropic:
		return []ReasoningKind{AnthropicThinking, NoReasoning}
	case Gemini:
		return []ReasoningKind{GeminiThinking, NoReasoning}
	default:
		return []ReasoningKind{OpenAIEffort, NoReasoning}
	}
}

// FamilyCapabilities is FAMILY_CAPABILITIES: the three families the Desk knows today as records —
// what the loop did for each before it read a record, so a host that supplies these changes
// nothing. The answer is a fresh value each time, as the TypeScript's is frozen.
func FamilyCapabilities(family EndpointKind) *Value {
	record := func(vision bool, kind ReasoningKind, schemas ToolSchemas) *Value {
		f := string(family)
		if family == OpenAICompatible {
			f = "openai"
		}
		low, medium, high := "low", "high", "xhigh"
		if family == Gemini {
			low, medium, high = "4096", "8192", "24576"
		}
		return Obj(
			F("nativeToolCalls", Bool(true)),
			F("parallelToolCalls", Bool(true)),
			F("vision", Bool(vision)),
			F("reasoning", Obj(F("kind", Str(string(kind))))),
			F("streamingQuirks", Obj(F("reasoningField", Null()), F("wholeJsonAnswer", Bool(false)))),
			F("toolSchemas", Str(string(schemas))),
			F("family", Str(f)), F("tiers", Obj(F("low", Str(low)), F("medium", Str(medium)), F("high", Str(high)))), F("cacheBreakpoints", Bool(family == Anthropic)),
		)
	}
	switch family {
	case Anthropic:
		return record(true, AnthropicThinking, JSONSchema)
	case Gemini:
		return record(true, GeminiThinking, GeminiSubset)
	default:
		return record(false, OpenAIEffort, JSONSchema)
	}
}

// refuseCapabilities is the TypeScript `refuse`: the authored configuration failure a record that
// is not one earns.
func refuseCapabilities(detail string) error {
	return failure.Authored("configuration", fmt.Sprintf("the model's capability record is invalid: %s; nothing was run", detail), "desk")
}

// closedMembers is the TypeScript `closed`: a member the record does not have is refused, as
// contextConfiguration refuses one.
func closedMembers(value *Value, members []string, where string) error {
	for _, name := range value.Keys() {
		if !slices.Contains(members, name) {
			return refuseCapabilities(fmt.Sprintf("%s names %s, which the record does not have", where, jsQuote(name)))
		}
	}
	return nil
}

// ReadCapabilities is readCapabilities: a capability record read closed — every member it names is
// checked, and a member it does not have is refused. The window bounds are the budget table's.
func ReadCapabilities(value *Value) (ModelCapabilities, error) {
	var record ModelCapabilities
	if !value.IsObject() {
		return record, refuseCapabilities("it is not an object")
	}
	if err := closedMembers(value, CapabilityMembers, "the record"); err != nil {
		return record, err
	}
	for _, flag := range []string{"nativeToolCalls", "parallelToolCalls", "vision"} {
		held, ok := value.Get(flag).Boolean()
		if !ok {
			return record, refuseCapabilities(fmt.Sprintf("%s must be true or false", flag))
		}
		switch flag {
		case "nativeToolCalls":
			record.NativeToolCalls = held
		case "parallelToolCalls":
			record.ParallelToolCalls = held
		case "vision":
			record.Vision = held
		}
	}
	if f := value.Get("family"); f != nil {
		record.Family = f.Text()
		if !f.IsString() || !slices.Contains([]string{"openai", "anthropic", "gemini", "codex"}, record.Family) {
			return record, refuseCapabilities("unknown family")
		}
	}
	if tiers := value.Get("tiers"); tiers != nil {
		if !tiers.IsObject() {
			return record, refuseCapabilities("tiers must be an object")
		}
		if err := closedMembers(tiers, []string{"low", "medium", "high"}, "tiers"); err != nil {
			return record, err
		}
		record.Tiers = map[string]string{}
		for _, tier := range []string{"low", "medium", "high"} {
			v := tiers.Get(tier)
			if !v.IsString() || v.Text() == "" {
				return record, refuseCapabilities("tiers must name every setting")
			}
			record.Tiers[tier] = v.Text()
		}
	}
	if v := value.Get("cacheBreakpoints"); v != nil {
		b, ok := v.Boolean()
		if !ok {
			return record, refuseCapabilities("cacheBreakpoints must be boolean")
		}
		record.CacheBreakpoints = b
	}
	window, output := value.Get("contextWindowTokens"), value.Get("outputTokens")
	if (window == nil) != (output == nil) {
		return record, refuseCapabilities("contextWindowTokens and outputTokens come together")
	}
	if window != nil {
		held, ok := integerOf(window)
		if !ok || held < 4096 || held > 2000000 {
			return record, refuseCapabilities("contextWindowTokens must be an integer from 4096 to 2000000")
		}
		reserve, okOutput := integerOf(output)
		if !okOutput || reserve < 1024 || float64(reserve) > float64(held)/2 {
			return record, refuseCapabilities("outputTokens must be an integer from 1024 to half the window")
		}
		record.ContextWindowTokens, record.OutputTokens = &held, &reserve
	}
	reasoning := value.Get("reasoning")
	if !reasoning.IsObject() {
		return record, refuseCapabilities("reasoning must be an object")
	}
	if err := closedMembers(reasoning, []string{"kind", "ignored"}, "reasoning"); err != nil {
		return record, err
	}
	kind := ReasoningKind(reasoning.Get("kind").Text())
	if !reasoning.Get("kind").IsString() || !slices.Contains(ReasoningKinds, kind) {
		return record, refuseCapabilities(fmt.Sprintf("reasoning.kind must be one of %s", joinKinds(ReasoningKinds)))
	}
	record.Reasoning.Kind = kind
	if ignored := reasoning.Get("ignored"); ignored != nil {
		held, ok := ignored.Boolean()
		if !ok {
			return record, refuseCapabilities("reasoning.ignored must be true or false")
		}
		record.Reasoning.Ignored = &held
	}
	quirks := value.Get("streamingQuirks")
	if !quirks.IsObject() {
		return record, refuseCapabilities("streamingQuirks must be an object")
	}
	if err := closedMembers(quirks, []string{"reasoningField", "wholeJsonAnswer"}, "streamingQuirks"); err != nil {
		return record, err
	}
	field := quirks.Get("reasoningField")
	if field.Kind() != KindNull {
		named := ReasoningField(field.Text())
		if !field.IsString() || !slices.Contains(ReasoningFields, named) {
			return record, refuseCapabilities("streamingQuirks.reasoningField must be reasoning, reasoning_content or null")
		}
		record.StreamingQuirks.ReasoningField = named
	}
	whole, ok := quirks.Get("wholeJsonAnswer").Boolean()
	if !ok {
		return record, refuseCapabilities("streamingQuirks.wholeJsonAnswer must be true or false")
	}
	record.StreamingQuirks.WholeJSONAnswer = whole
	schemas := ToolSchemas(value.Get("toolSchemas").Text())
	if !value.Get("toolSchemas").IsString() || !slices.Contains(ToolSchemasValues, schemas) {
		names := make([]string, 0, len(ToolSchemasValues))
		for _, held := range ToolSchemasValues {
			names = append(names, string(held))
		}
		return record, refuseCapabilities(fmt.Sprintf("toolSchemas must be one of %s", strings.Join(names, ", ")))
	}
	record.ToolSchemas = schemas
	if placement := value.Get("statePlacement"); placement != nil {
		named := StatePlacement(placement.Text())
		if !placement.IsString() || !slices.Contains(StatePlacements, named) {
			names := make([]string, 0, len(StatePlacements))
			for _, held := range StatePlacements {
				names = append(names, string(held))
			}
			return record, refuseCapabilities(fmt.Sprintf("statePlacement must be one of %s", strings.Join(names, ", ")))
		}
		record.StatePlacement = named
	}
	if strict := value.Get("strictAlternation"); strict != nil {
		held, ok := strict.Boolean()
		if !ok {
			return record, refuseCapabilities("strictAlternation must be true or false")
		}
		record.StrictAlternation = &held
	}
	return record, nil
}

// EndpointCapabilities is what the host knows about one endpoint and model (endpointCapabilities
// in host.ts): any members of the capability record, or nil for nothing known at all.
type EndpointCapabilities func(family EndpointKind, model string) *Value

// CapabilitiesFor is capabilitiesFor: the record one model session runs with — the family's
// default, then the host's answer for this endpoint and model, then the session's own, member by
// member. The host's endpointCapabilities is a parameter here rather than a module binding; nil
// for it is the harness's own default answer, nothing known. Read closed, and refused where its
// reasoning kind is not one the session's wire speaks.
func CapabilitiesFor(family EndpointKind, model string, host EndpointCapabilities, session *Value) (ModelCapabilities, error) {
	var record ModelCapabilities
	var answered *Value
	if host != nil {
		answered = host(family, model)
		if !answered.IsObject() {
			return record, refuseCapabilities("the host answered for the endpoint with something other than an object")
		}
	}
	merged := FamilyCapabilities(family)
	for _, layer := range []*Value{answered, session} {
		for _, field := range layer.Members() {
			merged.Set(field.Name, field.Value)
		}
	}
	record, err := ReadCapabilities(merged)
	if err != nil {
		return record, err
	}
	spoken := WireReasoning(family)
	if !slices.Contains(spoken, record.Reasoning.Kind) {
		names := make([]string, 0, len(spoken))
		for _, held := range spoken {
			names = append(names, string(held))
		}
		return record, refuseCapabilities(fmt.Sprintf("reasoning.kind %s is not spoken on the %s wire (%s)",
			record.Reasoning.Kind, family, strings.Join(names, " or ")))
	}
	return record, nil
}

// Value writes the record back as the contract states it, its members in capability.json's order.
func (c ModelCapabilities) Value() *Value {
	reasoning := Obj(F("kind", Str(string(c.Reasoning.Kind))))
	if c.Reasoning.Ignored != nil {
		reasoning.Set("ignored", Bool(*c.Reasoning.Ignored))
	}
	field := Null()
	if c.StreamingQuirks.ReasoningField != "" {
		field = Str(string(c.StreamingQuirks.ReasoningField))
	}
	record := Obj()
	if c.ContextWindowTokens != nil {
		record.Set("contextWindowTokens", Int(*c.ContextWindowTokens))
	}
	if c.OutputTokens != nil {
		record.Set("outputTokens", Int(*c.OutputTokens))
	}
	record.Set("nativeToolCalls", Bool(c.NativeToolCalls))
	record.Set("parallelToolCalls", Bool(c.ParallelToolCalls))
	record.Set("vision", Bool(c.Vision))
	record.Set("reasoning", reasoning)
	record.Set("streamingQuirks", Obj(F("reasoningField", field), F("wholeJsonAnswer", Bool(c.StreamingQuirks.WholeJSONAnswer))))
	record.Set("toolSchemas", Str(string(c.ToolSchemas)))
	if c.Family != "" {
		record.Set("family", Str(c.Family))
	}
	if c.Tiers != nil {
		record.Set("tiers", Obj(F("low", Str(c.Tiers["low"])), F("medium", Str(c.Tiers["medium"])), F("high", Str(c.Tiers["high"]))))
	}
	if c.Family != "" {
		record.Set("cacheBreakpoints", Bool(c.CacheBreakpoints))
	}
	if c.StatePlacement != "" {
		record.Set("statePlacement", Str(string(c.StatePlacement)))
	}
	if c.StrictAlternation != nil {
		record.Set("strictAlternation", Bool(*c.StrictAlternation))
	}
	return record
}

// integerOf is Number.isInteger with the value in hand.
func integerOf(value *Value) (int, bool) {
	number, ok := value.Number()
	if !ok || number != math.Trunc(number) || math.IsInf(number, 0) {
		return 0, false
	}
	return int(number), true
}

func joinKinds(kinds []ReasoningKind) string {
	names := make([]string, 0, len(kinds))
	for _, held := range kinds {
		names = append(names, string(held))
	}
	return strings.Join(names, ", ")
}
