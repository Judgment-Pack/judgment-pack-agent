package wire

import (
	"slices"
	"strconv"
	"strings"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

// The thinking tier, normalized in the desk (thinking.ts): one table, one closed list of refusals,
// one lifecycle and one sentence per state. An adapter reads members off the slot and puts them on
// the wire; it never chooses a parameter, never decides what a 400 meant and never writes a line
// about the tier.
//
// Off is expressed by omission on two families and by a member on the third: Anthropic rejects a
// disabled thinking member and several OpenAI-compatible endpoints answer 400 to an effort of
// none, while on the native Gemini wire omission means thinking, so off there is a zero thinking
// budget.

// ThinkingState is the five states, three of which a person may select (ThinkingState in
// thinking.ts). `always` and `unavailable` are reports and not settings.
type ThinkingState string

// The five states.
const (
	StateOff         ThinkingState = "off"
	StateOn          ThinkingState = "on"
	StateUltra       ThinkingState = "ultra"
	StateAlways      ThinkingState = "always"
	StateUnavailable ThinkingState = "unavailable"
)

// ThinkingDialect is the endpoint's vocabulary: one slot tier, five spellings (ThinkingDialect in
// thinking.ts).
type ThinkingDialect string

// The five dialects the three families are tried at.
const (
	DialectOpenAI            ThinkingDialect = "openai"
	DialectAnthropicAdaptive ThinkingDialect = "anthropic-adaptive"
	DialectAnthropicEnabled  ThinkingDialect = "anthropic-enabled"
	DialectGeminiBudget      ThinkingDialect = "gemini-budget"
	DialectGeminiLevel       ThinkingDialect = "gemini-level"
)

// Expectation is the member a checker reads, so a leg can assert the parameter and not a shape
// (WireThinking's `expect` in thinking.ts).
type Expectation struct {
	Path  string
	Value *Value
}

// WireThinking is what one (family, tier) pair puts on the wire, and what that is called
// (WireThinking in thinking.ts).
type WireThinking struct {
	// Members are top-level body members, merged into the request by whichever adapter runs.
	Members *Value
	Expect  Expectation
}

// ResponseTokens is RESPONSE_TOKENS: the tokens the desk leaves for the model's answer, beyond any
// thinking budget.
const ResponseTokens = 4096

// GeminiBudget is GEMINI_BUDGET: the two thinking budgets the Gemini dialect asks for, in tokens —
// the desk's choice inside a documented field, an ordinary working depth and a deep one.
func GeminiBudget(tier ThinkingTier) int {
	if tier == "low" {
		return 4096
	}
	if tier == TierUltra || tier == "high" {
		return 24576
	}
	return 8192
}

// GeminiLevel is GEMINI_LEVEL: the levels the other Gemini dialect asks for. `minimal` is what off
// sends on this dialect, because the enumeration has no "none" in it.
func GeminiLevel(tier ThinkingTier) string {
	switch tier {
	case TierOff:
		return "minimal"
	case "low":
		return "low"
	case "high":
		return "high"
	case TierUltra:
		return "high"
	default:
		return "medium"
	}
}

// WireFor is wireFor: the table, one place, every family. nil for off on the two families where
// off is omission; a real member on gemini, where omission means thinking and the only way to ask
// for none is to ask.
func WireFor(tier ThinkingTier, dialect ThinkingDialect, maps ...map[string]string) *WireThinking {
	const geminiPath = "generationConfig.thinkingConfig"
	if tier == TierOff {
		switch dialect {
		case DialectGeminiBudget:
			config := Obj(F("thinkingBudget", Int(0)))
			return &WireThinking{Members: Obj(F("thinkingConfig", config)), Expect: Expectation{geminiPath, config}}
		case DialectGeminiLevel:
			config := Obj(F("thinkingLevel", Str(GeminiLevel(TierOff))))
			return &WireThinking{Members: Obj(F("thinkingConfig", config)), Expect: Expectation{geminiPath, config}}
		}
		return nil
	}
	effort := "high"
	if tier == "low" {
		effort = "low"
	}
	if tier == TierUltra || tier == "high" {
		effort = "xhigh"
	}
	setting := ""
	if len(maps) > 0 {
		key := "medium"
		if tier == "low" {
			key = "low"
		}
		if tier == TierUltra || tier == "high" {
			key = "high"
		}
		setting = maps[0][key]
		if setting != "" {
			effort = setting
		}
	}
	switch dialect {
	case DialectGeminiBudget:
		// includeThoughts is what makes the thought summaries arrive at all; the budget is how deep
		// they go. Both, or the tier asks for thinking nobody can read.
		config := Obj(F("includeThoughts", Bool(true)), F("thinkingBudget", Int(func() int {
			if setting != "" {
				v, _ := strconv.Atoi(setting)
				return v
			}
			return GeminiBudget(tier)
		}())))
		return &WireThinking{Members: Obj(F("thinkingConfig", config)), Expect: Expectation{geminiPath, config}}
	case DialectGeminiLevel:
		config := Obj(F("includeThoughts", Bool(true)), F("thinkingLevel", Str(GeminiLevel(tier))))
		return &WireThinking{Members: Obj(F("thinkingConfig", config)), Expect: Expectation{geminiPath, config}}
	case DialectOpenAI:
		return &WireThinking{
			Members: Obj(F("reasoning_effort", Str(effort))),
			Expect:  Expectation{"reasoning_effort", Str(effort)},
		}
	case DialectAnthropicAdaptive:
		return &WireThinking{
			Members: Obj(F("thinking", Obj(F("type", Str("adaptive")))), F("output_config", Obj(F("effort", Str(effort))))),
			Expect:  Expectation{"thinking", Obj(F("type", Str("adaptive")))},
		}
	case DialectAnthropicEnabled:
		budget := 8000
		if tier == TierUltra || tier == "high" {
			budget = 16000
		}
		return &WireThinking{
			Members: Obj(
				F("thinking", Obj(F("type", Str("enabled")), F("budget_tokens", Int(budget)))),
				// Strictly above the budget, or the endpoint refuses the request.
				F("max_tokens", Int(budget+ResponseTokens)),
			),
			Expect: Expectation{"thinking", Obj(F("type", Str("enabled")), F("budget_tokens", Int(budget)))},
		}
	}
	return nil
}

// TierMembers is TIER_MEMBERS: every member the table can add to a request in order to ask for
// thinking, and so what a request is stripped of when the slot degrades. max_tokens is
// deliberately not here.
var TierMembers = []string{"reasoning_effort", "thinking", "output_config", "thinkingConfig"}

// FirstDialect is firstDialect: the dialect a family is tried at first.
func FirstDialect(family EndpointKind) ThinkingDialect {
	switch family {
	case Anthropic:
		return DialectAnthropicAdaptive
	case Gemini:
		return DialectGeminiBudget
	default:
		return DialectOpenAI
	}
}

// DialectFor is dialectFor: the dialect a capability record's reasoning kind is tried at first —
// the same first spelling FirstDialect gives the family that speaks it. A kind of none asks for
// nothing and has no dialect, and answers "".
func DialectFor(kind ReasoningKind) ThinkingDialect {
	switch kind {
	case AnthropicThinking:
		return DialectAnthropicAdaptive
	case GeminiThinking:
		return DialectGeminiBudget
	case OpenAIEffort:
		return DialectOpenAI
	}
	return ""
}

// NextDialect is nextDialect: the one fallback there is, or "" — adaptive then the token budget on
// Anthropic, the token budget then the level on Gemini, and after that the endpoint has no
// thinking this desk knows how to ask for.
func NextDialect(dialect ThinkingDialect) ThinkingDialect {
	switch dialect {
	case DialectAnthropicAdaptive:
		return DialectAnthropicEnabled
	case DialectGeminiBudget:
		return DialectGeminiLevel
	}
	return ""
}

// thinkingMembers is THINKING_MEMBERS: the member names a thinking refusal may be about, and the
// whole of them. max_tokens is not on it: a refusal about it alone is about the size of an answer.
var thinkingMembers = []string{
	"thinking", "reasoning_effort", "output_config", "budget_tokens",
	"thinkingConfig", "thinkingBudget", "thinkingLevel", "includeThoughts",
}

// namesSent is every name this desk actually sent, one level down as well as at the top.
func namesSent(members *Value) []string {
	var names []string
	add := func(name string) {
		if !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	for _, field := range members.Members() {
		add(field.Name)
		if field.Value.IsObject() {
			for _, inner := range field.Value.Keys() {
				add(inner)
			}
		}
	}
	kept := names[:0]
	for _, name := range names {
		if slices.Contains(thinkingMembers, name) {
			kept = append(kept, name)
		}
	}
	return kept
}

// UnsupportedThinking is unsupportedThinking: a 400 — and only a 400 — whose message names a
// member this desk actually added, out of the closed list of the members that ask for thinking at
// all. The prose alone is not enough: "Unsupported parameter" is a sentence an endpoint writes
// about any member, and a rule that read it alone hid real failures behind a thinking story.
func UnsupportedThinking(status int, message string, members *Value) bool {
	if status != 400 {
		return false
	}
	for _, name := range namesSent(members) {
		if strings.Contains(message, name) {
			return true
		}
	}
	return false
}

// Permanence is PERMANENCE: how many consecutive turns an inference from absence takes. A refusal
// is believed at once; one observation promoted to a session-wide capability is not.
const Permanence = 2

// IsTruncatedSignature is isTruncatedSignature: whether this block's signature came back shorter
// than it went out, against the one signature it was given and not against every signature ever
// seen. A strict prefix or suffix; one carried back whole is equal to what was sent.
func IsTruncatedSignature(sent, carried string) bool {
	if carried == "" || sent == "" || sent == carried {
		return false
	}
	return utf16Length(sent) > utf16Length(carried) && (strings.HasPrefix(sent, carried) || strings.HasSuffix(sent, carried))
}

// ThinkingAlways is THINKING_ALWAYS.
const ThinkingAlways = "this model always thinks"

// AlwaysFromAbsence is ALWAYS_FROM_ABSENCE: the evidence for the state the desk reaches without a
// refusal.
var AlwaysFromAbsence = SourceMessage("the tier asked for no thinking and the endpoint returned reasoning anyway", nil)

// AlwaysFromRefusal is alwaysFromRefusal: the other evidence, the endpoint's own refusal.
func AlwaysFromRefusal(status int) string {
	return SourceMessage("the endpoint answered {{value0}} to every spelling of the parameter that turns thinking off", map[string]any{"value0": status})
}

// ThinkingUnavailable is THINKING_UNAVAILABLE.
const ThinkingUnavailable = "thinking is unavailable for this endpoint"

// ThinkingNotice is thinkingNotice: the desk's own notice, with the reason after it.
func ThinkingNotice(state ThinkingState, reason string) string {
	if state == StateAlways {
		if reason == "" {
			return SourceMessage("this model always thinks.", nil)
		}
		return SourceMessage("this model always thinks: {{message0}}", map[string]any{"message0": reason})
	}
	if reason == "" {
		return SourceMessage("thinking is unavailable for this endpoint.", nil)
	}
	return SourceMessage("thinking is unavailable for this endpoint: {{message0}}", map[string]any{"message0": reason})
}

// ThinkingLine is thinkingLine: one line for the tab, naming the tier the file asked for and the
// real state. The TypeScript's translator parameter is not ported; the desk's own text is used.
func ThinkingLine(tier ThinkingTier, state ThinkingState) string {
	label := func(value ThinkingTier) string { return SourceMessage(string(value), nil) }
	switch state {
	case StateAlways:
		return SourceMessage("thinking {{tier}} · this model always thinks", map[string]any{"tier": label(tier)})
	case StateUnavailable:
		return SourceMessage("thinking {{tier}} · unavailable for this endpoint", map[string]any{"tier": label(tier)})
	}
	return SourceMessage("thinking {{tier}}", map[string]any{"tier": label(ThinkingTier(state))})
}

// StateFromEvents is stateFromEvents: the state a finished run reached, read off its own events.
func StateFromEvents(tier ThinkingTier, events []contract.RunEvent) ThinkingState {
	notices := 0
	for _, event := range events {
		if event.ThinkingUnavailable == nil {
			continue
		}
		notices++
		if strings.HasPrefix(event.ThinkingUnavailable.Detail, ThinkingAlways) {
			return StateAlways
		}
	}
	if notices > 0 {
		return StateUnavailable
	}
	return ThinkingState(tier)
}

// NormalizedThinking is the normalized result the session carries: the tier, its wire, its state
// (NormalizedThinking in thinking.ts).
type NormalizedThinking struct {
	Tier ThinkingTier
	// Wire is what the first request carries, or nil at off.
	Wire *WireThinking
	// State is where the slot starts; always and unavailable are only ever reached later.
	State ThinkingState
}

// Normalize is normalize: the tier, normalized once, where the session is bound.
func Normalize(tier ThinkingTier, family EndpointKind) NormalizedThinking {
	return NormalizedThinking{Tier: tier, Wire: WireFor(tier, FirstDialect(family)), State: ThinkingState(tier)}
}

// RefusalKind is what a refused request meant, decided in the slot and never in an adapter
// (ThinkingRefusal in thinking.ts).
type RefusalKind string

// The three things a refusal can mean.
const (
	// RefusalOther is not about the tier: the adapter reports the failure as it always would.
	RefusalOther RefusalKind = "other"
	// RefusalRetry is the same request again at the next dialect. Nothing is said.
	RefusalRetry RefusalKind = "retry"
	// RefusalDegrade is the endpoint having no thinking: the request is retried plain.
	RefusalDegrade RefusalKind = "degrade"
)

// ThinkingRefusal is what the endpoint's refusal meant, and the notice it owes. Event is nil where
// the desk has already said it, so an adapter cannot announce a degrade twice by being asked
// twice.
type ThinkingRefusal struct {
	Kind  RefusalKind
	Event *contract.RunEvent
}

// Why a record leaves the tier nothing to ask with, in the desk's own words.
var (
	noParameter      = SourceMessage("its capability record names no reasoning parameter", nil)
	ignoredParameter = SourceMessage("its capability record says the endpoint ignores the reasoning parameter", nil)
)

// ThinkingSlot is one session's thinking slot: the table, the fallback and the degrade, together,
// and each of them once (ThinkingSlot in thinking.ts). Held by whichever adapter runs; every
// transition answers the event to emit, or nil where the desk has already said it.
type ThinkingSlot struct {
	tiers   map[string]string
	tier    ThinkingTier
	silent  bool
	dialect ThinkingDialect
	// reasoningCapability is the record's reasoning, where the session was opened with one.
	reasoningCapability *ReasoningCapability

	unavailable bool
	always      bool
	// offRefused: the endpoint refused the member that asks it not to think, at every spelling this
	// desk knows, so there is nothing left to send.
	offRefused       bool
	announced        bool
	reasonedThisTurn bool
	reasoningRun     int
	quietRun         int
}

// OpenThinking is openThinking: the slot one session runs with. `reasoning` is the capability
// record's — the dialect the tier is spelled in is the kind's, and a kind of none, or a parameter
// the endpoint ignores, sends no member at any tier; nil is the family's first dialect, as before
// there was a record. The TypeScript takes the session and reads its tier and family off it; this
// takes the two.
func OpenThinking(tier ThinkingTier, family EndpointKind, reasoning *ReasoningCapability, maps ...map[string]string) *ThinkingSlot {
	silent := reasoning != nil && (reasoning.Kind == NoReasoning || (reasoning.Ignored != nil && *reasoning.Ignored))
	dialect := FirstDialect(family)
	if reasoning != nil && reasoning.Kind != NoReasoning {
		dialect = DialectFor(reasoning.Kind)
	}
	var tiers map[string]string
	if len(maps) > 0 {
		tiers = maps[0]
	}
	return &ThinkingSlot{
		tiers:               tiers,
		tier:                tier,
		silent:              silent,
		dialect:             dialect,
		unavailable:         silent && tier != TierOff,
		reasoningCapability: reasoning,
	}
}

// Tier is the tier the file asked for.
func (s *ThinkingSlot) Tier() ThinkingTier { return s.tier }

// State is the current state, which only ever moves away from the tier.
func (s *ThinkingSlot) State() ThinkingState {
	if s.always {
		return StateAlways
	}
	if s.unavailable {
		return StateUnavailable
	}
	return ThinkingState(s.tier)
}

// wire is what this request carries: WireFor already answers nil at off on the two families where
// off is omission, so what this adds is the two states in which the desk has stopped asking.
func (s *ThinkingSlot) wire() *WireThinking {
	if s.silent || s.unavailable || s.offRefused {
		return nil
	}
	return WireFor(s.tier, s.dialect, s.tiers)
}

// Members are the members this request carries, or nil.
func (s *ThinkingSlot) Members() *Value {
	held := s.wire()
	if held == nil {
		return nil
	}
	return held.Members
}

func (s *ThinkingSlot) notice(state ThinkingState, reason string) *contract.RunEvent {
	if s.announced {
		return nil
	}
	s.announced = true
	return &contract.RunEvent{ThinkingUnavailable: &contract.RunEventThinkingUnavailable{
		Type:   "thinking_unavailable",
		Detail: ThinkingNotice(state, reason),
	}}
}

// Refused is what the endpoint's refusal meant.
func (s *ThinkingSlot) Refused(status int, message string) ThinkingRefusal {
	current := s.wire()
	if current == nil {
		return ThinkingRefusal{Kind: RefusalOther}
	}
	if !UnsupportedThinking(status, message, current.Members) {
		return ThinkingRefusal{Kind: RefusalOther}
	}
	if next := NextDialect(s.dialect); next != "" {
		// The fallback is silent, because it is not a degrade: the desk asked in one spelling and
		// asks again in the other. It runs at off too, before anything is concluded — a 400 naming
		// thinkingBudget may be "this model cannot be turned off" or "this model spells it
		// thinkingLevel", and trying the other spelling is what tells them apart.
		s.dialect = next
		return ThinkingRefusal{Kind: RefusalRetry}
	}
	if s.tier == TierOff {
		// The endpoint will not be turned off, and said so: `always` reached by a refusal rather
		// than by absence, and immediate for the reason every refusal is. The member comes off with
		// it — there is nothing left to ask.
		s.always = true
		s.offRefused = true
		return ThinkingRefusal{Kind: RefusalDegrade, Event: s.notice(StateAlways, AlwaysFromRefusal(status))}
	}
	s.unavailable = true
	// The status, and the desk's own sentence. The endpoint's body is matched against a closed list
	// and never quoted past it.
	return ThinkingRefusal{
		Kind:  RefusalDegrade,
		Event: s.notice(StateUnavailable, SourceMessage("the endpoint answered {{value0}} to the tier parameter", map[string]any{"value0": status})),
	}
}

// SawReasoning marks reasoning in the turn now in progress. It says nothing on its own: the turn
// is what carries evidence about an endpoint, not a passage.
func (s *ThinkingSlot) SawReasoning() { s.reasonedThisTurn = true }

// TurnEnded is one model turn finished, and whether it carried an answer of its own. This is where
// permanence is decided, and it takes more than one turn. A turn that said nothing but called a
// tool is no evidence either way, so it neither counts nor resets.
func (s *ThinkingSlot) TurnEnded(hadText bool) *contract.RunEvent {
	reasoned := s.reasonedThisTurn
	s.reasonedThisTurn = false
	if !hadText {
		return nil
	}
	if s.tier == TierOff {
		// An answer that returned no reasoning is counter-evidence, so the run starts again:
		// "always" means every answer, not one of them.
		if reasoned {
			s.reasoningRun++
		} else {
			s.reasoningRun = 0
		}
		if s.always || s.reasoningRun < Permanence {
			return nil
		}
		s.always = true
		return s.notice(StateAlways, AlwaysFromAbsence)
	}
	if s.unavailable {
		return nil
	}
	if reasoned {
		s.quietRun = 0
		return nil
	}
	s.quietRun++
	if s.quietRun < Permanence {
		return nil
	}
	s.unavailable = true
	return s.notice(StateUnavailable, SourceMessage("{{value0}} consecutive turns carried an answer and no reasoning block", map[string]any{"value0": Permanence}))
}

// Truncated is a carried signature that came back short. It degrades, once, with the reason.
func (s *ThinkingSlot) Truncated(reason string) *contract.RunEvent {
	if s.unavailable {
		return nil
	}
	s.unavailable = true
	return s.notice(StateUnavailable, reason)
}

// Opening is the notice owed before the first request, once, or nil: where the capability record
// says the endpoint has no reasoning parameter, or ignores the one it has, a tier other than off is
// unavailable from the start.
func (s *ThinkingSlot) Opening() *contract.RunEvent {
	if !s.silent || s.tier == TierOff || s.announced {
		return nil
	}
	if s.reasoningCapability != nil && s.reasoningCapability.Kind == NoReasoning {
		return s.notice(StateUnavailable, noParameter)
	}
	return s.notice(StateUnavailable, ignoredParameter)
}
