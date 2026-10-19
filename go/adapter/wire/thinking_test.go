package wire

import (
	"slices"
	"testing"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

// The desk-owned tier table, its one fallback and the five states (thinking.test.ts). Everything
// here is the desk's rather than an adapter's: the table, the closed list of refusals, the dialect
// fallback, and the two states a tier cannot express.

func slot(tier ThinkingTier, family EndpointKind) *ThinkingSlot {
	return OpenThinking(tier, family, nil)
}

func members(t *testing.T, held *WireThinking) *Value {
	t.Helper()
	if held == nil {
		t.Fatal("the table answered nothing where it was to answer members")
	}
	return held.Members
}

// TestOffIsOmissionWhereOmissionIsTaken is "expresses off by omission on the two dialects that
// accept omission".
func TestOffIsOmissionWhereOmissionIsTaken(t *testing.T) {
	for _, dialect := range []ThinkingDialect{DialectOpenAI, DialectAnthropicAdaptive, DialectAnthropicEnabled} {
		if WireFor(TierOff, dialect) != nil {
			t.Errorf("%s sends a member at off", dialect)
		}
	}
}

// TestOffIsAMemberOnGemini is "expresses off by a member on the Gemini wire, where omission means
// thinking", and "asks for thought summaries and a depth on the Gemini wire, in both spellings".
func TestOffIsAMemberOnGemini(t *testing.T) {
	equalJSON(t, "off on the budget spelling", members(t, WireFor(TierOff, DialectGeminiBudget)),
		Obj(F("thinkingConfig", Obj(F("thinkingBudget", Int(0))))))
	equalJSON(t, "off on the level spelling", members(t, WireFor(TierOff, DialectGeminiLevel)),
		Obj(F("thinkingConfig", Obj(F("thinkingLevel", Str("minimal"))))))
	equalJSON(t, "on at a budget", members(t, WireFor(TierOn, DialectGeminiBudget)),
		Obj(F("thinkingConfig", Obj(F("includeThoughts", Bool(true)), F("thinkingBudget", Int(GeminiBudget(TierOn)))))))
	equalJSON(t, "ultra at a budget", members(t, WireFor(TierUltra, DialectGeminiBudget)),
		Obj(F("thinkingConfig", Obj(F("includeThoughts", Bool(true)), F("thinkingBudget", Int(GeminiBudget(TierUltra)))))))
	equalJSON(t, "on at a level", members(t, WireFor(TierOn, DialectGeminiLevel)),
		Obj(F("thinkingConfig", Obj(F("includeThoughts", Bool(true)), F("thinkingLevel", Str("medium"))))))
	equalJSON(t, "ultra at a level", members(t, WireFor(TierUltra, DialectGeminiLevel)),
		Obj(F("thinkingConfig", Obj(F("includeThoughts", Bool(true)), F("thinkingLevel", Str("high"))))))
	if GeminiBudget(TierUltra) <= GeminiBudget(TierOn) {
		t.Error("ultra is not deeper than on")
	}
	// The member is named and not placed: the path a checker reads is the one each engine writes
	// it at, and the members themselves carry no wrapper.
	equalText(t, "the path a checker reads", WireFor(TierOn, DialectGeminiBudget).Expect.Path, "generationConfig.thinkingConfig")
}

// TestTheDialectsAndTheirOneFallback is "tries the budget spelling first and the level second, and
// then stops" with "starts each family at its own dialect and offers exactly one fallback".
func TestTheDialectsAndTheirOneFallback(t *testing.T) {
	equalText(t, "gemini's first dialect", string(FirstDialect(Gemini)), "gemini-budget")
	equalText(t, "gemini's fallback", string(NextDialect(DialectGeminiBudget)), "gemini-level")
	equalText(t, "after the level spelling", string(NextDialect(DialectGeminiLevel)), "")
	equalText(t, "anthropic's first dialect", string(FirstDialect(Anthropic)), "anthropic-adaptive")
	equalText(t, "the OpenAI-compatible first dialect", string(FirstDialect(OpenAICompatible)), "openai")
	equalText(t, "anthropic's fallback", string(NextDialect(DialectAnthropicAdaptive)), "anthropic-enabled")
	equalText(t, "after the enabled spelling", string(NextDialect(DialectAnthropicEnabled)), "")
	equalText(t, "after the openai spelling", string(NextDialect(DialectOpenAI)), "")
	// agent#4: a record's reasoning kind is tried at the same first spelling.
	equalText(t, "the anthropic kind's dialect", string(DialectFor(AnthropicThinking)), "anthropic-adaptive")
	equalText(t, "the gemini kind's dialect", string(DialectFor(GeminiThinking)), "gemini-budget")
	equalText(t, "the openai kind's dialect", string(DialectFor(OpenAIEffort)), "openai")
}

// TestTheOtherTwoWires is "sends reasoning_effort on the OpenAI-compatible wire", "sends the
// adaptive spelling first, with the depth in its sibling", "falls back to a token budget, with the
// maximum that budget requires" and "leaves the maximum alone on the dialect that has no budget".
func TestTheOtherTwoWires(t *testing.T) {
	equalJSON(t, "on", members(t, WireFor(TierOn, DialectOpenAI)), Obj(F("reasoning_effort", Str("high"))))
	equalJSON(t, "ultra", members(t, WireFor(TierUltra, DialectOpenAI)), Obj(F("reasoning_effort", Str("xhigh"))))
	equalText(t, "the path a checker reads", WireFor(TierOn, DialectOpenAI).Expect.Path, "reasoning_effort")
	equalJSON(t, "the member a checker reads", WireFor(TierOn, DialectOpenAI).Expect.Value, Str("high"))

	equalJSON(t, "adaptive on", members(t, WireFor(TierOn, DialectAnthropicAdaptive)),
		Obj(F("thinking", Obj(F("type", Str("adaptive")))), F("output_config", Obj(F("effort", Str("high"))))))
	equalJSON(t, "adaptive ultra", members(t, WireFor(TierUltra, DialectAnthropicAdaptive)),
		Obj(F("thinking", Obj(F("type", Str("adaptive")))), F("output_config", Obj(F("effort", Str("xhigh"))))))

	// Two numbers, one decision: the budget is spent out of the maximum, so the table sets both.
	equalJSON(t, "enabled on", members(t, WireFor(TierOn, DialectAnthropicEnabled)),
		Obj(F("thinking", Obj(F("type", Str("enabled")), F("budget_tokens", Int(8000)))), F("max_tokens", Int(8000+ResponseTokens))))
	equalJSON(t, "enabled ultra", members(t, WireFor(TierUltra, DialectAnthropicEnabled)),
		Obj(F("thinking", Obj(F("type", Str("enabled")), F("budget_tokens", Int(16000)))), F("max_tokens", Int(16000+ResponseTokens))))
	for _, tier := range []ThinkingTier{TierOn, TierUltra} {
		held := members(t, WireFor(tier, DialectAnthropicEnabled))
		budget, _ := held.Get("thinking").Get("budget_tokens").Number()
		maximum, _ := held.Get("max_tokens").Number()
		if maximum <= budget {
			t.Errorf("%s: the maximum %v is not above the budget %v", tier, maximum, budget)
		}
	}
	if names := members(t, WireFor(TierOn, DialectAnthropicAdaptive)).Keys(); !slices.Equal(names, []string{"thinking", "output_config"}) {
		t.Errorf("the adaptive dialect sends %v", names)
	}
	if names := members(t, WireFor(TierOn, DialectOpenAI)).Keys(); !slices.Equal(names, []string{"reasoning_effort"}) {
		t.Errorf("the openai dialect sends %v", names)
	}
}

// TestNormalize is "normalizes the tier into the wire the family takes".
func TestNormalize(t *testing.T) {
	held := Normalize(TierOn, Anthropic)
	if held.Tier != TierOn || held.State != StateOn {
		t.Errorf("the normalized tier is %v at %v", held.Tier, held.State)
	}
	equalJSON(t, "the wire anthropic takes", held.Wire.Members, members(t, WireFor(TierOn, DialectAnthropicAdaptive)))
	off := Normalize(TierOff, OpenAICompatible)
	if off.Wire != nil || off.State != StateOff {
		t.Errorf("off on the OpenAI-compatible wire carries %v at %v", off.Wire, off.State)
	}
}

// TestWhatCountsAsARefusalOfTheTier is "what counts as a refusal of the tier": only a 400, and
// only one naming a member this desk actually sent.
func TestWhatCountsAsARefusalOfTheTier(t *testing.T) {
	effort := Obj(F("reasoning_effort", Str("high")))
	adaptive := Obj(F("thinking", Obj(F("type", Str("adaptive")))), F("output_config", Obj(F("effort", Str("high")))))
	budget := Obj(F("thinking", Obj(F("type", Str("enabled")), F("budget_tokens", Int(8000)))), F("max_tokens", Int(12096)))

	// The status every provider documents for a member it will not take; a 422 is something else.
	for _, held := range []struct {
		status  int
		message string
		members *Value
		want    bool
	}{
		{400, "Unsupported parameter: reasoning_effort", effort, true},
		{422, "Unsupported parameter: reasoning_effort", effort, false},
		{422, "thinking is not supported", adaptive, false},
		{500, "Unsupported parameter: reasoning_effort", effort, false},
		// The conjunction: the prose alone is what an endpoint writes about any member.
		{400, "Unsupported parameter: temperature", effort, false},
		{400, "Extra inputs are not permitted: tools", adaptive, false},
		{400, "messages: at least one message is required", effort, false},
		{400, "the pack is invalid: rule 3 names no outcome", effort, false},
		// Each documented refusal, because each names the member.
		{400, "This model does not support the reasoning_effort parameter", effort, true},
		{400, "Unknown parameter: thinking", adaptive, true},
		{400, "Unrecognized request argument supplied: thinking", adaptive, true},
		{400, "thinking: Extra inputs are not permitted", adaptive, true},
		{400, "thinking.type.enabled: Extended thinking is not supported for this model", budget, true},
		{400, "Adaptive thinking is not supported by this model", adaptive, true},
		{400, "output_config.effort: not supported on this model", adaptive, true},
		{400, "thinking.budget_tokens: must be less than max_tokens", budget, true},
		// A member one level down, and one the desk did not send.
		{400, "budget_tokens is out of range", budget, true},
		{400, "budget_tokens is out of range", effort, false},
		{400, "thinking is not supported here", effort, false},
		// A refusal about the size of an answer is not one about thinking.
		{400, "max_tokens exceeds the model maximum", budget, false},
	} {
		if got := UnsupportedThinking(held.status, held.message, held.members); got != held.want {
			t.Errorf("%d %q: %v, wanted %v", held.status, held.message, got, held.want)
		}
	}
}

// turn is one turn of a slot: reasoning or not, and whether it produced an answer.
func turnOf(held *ThinkingSlot, reasoned, hadText bool) *contract.RunEvent {
	if reasoned {
		held.SawReasoning()
	}
	return held.TurnEnded(hadText)
}

// TestTheFiveStates is "the five states".
func TestTheFiveStates(t *testing.T) {
	for _, tier := range ThinkingTiers {
		if state := slot(tier, OpenAICompatible).State(); state != ThinkingState(tier) {
			t.Errorf("%s started at %s", tier, state)
		}
	}

	// One turn is a turn, not a capability, and the notice is said once.
	always := slot(TierOff, OpenAICompatible)
	if always.Members() != nil {
		t.Error("off sent a member on the OpenAI-compatible wire")
	}
	if turnOf(always, true, true) != nil {
		t.Error("one reasoning turn at off said something")
	}
	if always.State() != StateOff {
		t.Error("one reasoning turn at off changed the state")
	}
	second := turnOf(always, true, true)
	if second == nil || second.ThinkingUnavailable == nil {
		t.Fatal("two reasoning turns at off said nothing")
	}
	contains(t, "the notice", second.ThinkingUnavailable.Detail, ThinkingAlways)
	if always.State() != StateAlways {
		t.Error("two reasoning turns did not reach always")
	}
	if turnOf(always, true, true) != nil {
		t.Error("a third reasoning turn was a second line")
	}

	// Not for one reasoning turn among quiet ones, and a quiet answer starts the count again.
	scattered := slot(TierOff, OpenAICompatible)
	for _, reasoned := range []bool{false, true, false, true} {
		if turnOf(scattered, reasoned, true) != nil {
			t.Error("a scattered reasoning turn reached always")
		}
	}
	if scattered.State() != StateOff {
		t.Error("scattered reasoning turns reached always")
	}

	// A tool-only turn neither counts nor resets, in either tier.
	toolOnly := slot(TierOff, OpenAICompatible)
	turnOf(toolOnly, true, true)
	turnOf(toolOnly, false, false)
	turnOf(toolOnly, false, false)
	if toolOnly.State() != StateOff {
		t.Error("a tool-only turn at off changed the state")
	}
	if turnOf(toolOnly, true, true) == nil {
		t.Error("the tool call in the middle wiped the count")
	}

	// One fallback on Anthropic, then a degrade.
	anthropic := slot(TierOn, Anthropic)
	equalJSON(t, "the adaptive members", anthropic.Members(),
		Obj(F("thinking", Obj(F("type", Str("adaptive")))), F("output_config", Obj(F("effort", Str("high"))))))
	if held := anthropic.Refused(400, "Adaptive thinking is not supported by this model"); held.Kind != RefusalRetry {
		t.Errorf("the first refusal was %s", held.Kind)
	}
	if anthropic.State() != StateOn {
		t.Error("the silent fallback changed the state")
	}
	equalJSON(t, "the enabled members", anthropic.Members(),
		Obj(F("thinking", Obj(F("type", Str("enabled")), F("budget_tokens", Int(8000)))), F("max_tokens", Int(8000+ResponseTokens))))
	degraded := anthropic.Refused(400, "thinking: Extra inputs are not permitted")
	if degraded.Kind != RefusalDegrade || degraded.Event == nil || degraded.Event.ThinkingUnavailable == nil {
		t.Fatalf("the second refusal was %s with %v", degraded.Kind, degraded.Event)
	}
	if anthropic.State() != StateUnavailable || anthropic.Members() != nil {
		t.Error("the degrade left the tier asking")
	}

	// The first refusal degrades where the family has no second dialect, and says so once.
	once := slot(TierOn, OpenAICompatible)
	if held := once.Refused(400, "Unsupported parameter: reasoning_effort"); held.Kind != RefusalDegrade || held.Event == nil {
		t.Errorf("the only refusal was %s", held.Kind)
	}
	if once.State() != StateUnavailable {
		t.Error("the degrade did not reach unavailable")
	}
	if held := once.Refused(400, "Unsupported parameter: reasoning_effort"); held.Kind != RefusalOther {
		t.Errorf("a refusal after the degrade was %s", held.Kind)
	}

	// A refusal that is not about the tier is the adapter's.
	if slot(TierOn, OpenAICompatible).Refused(400, "messages: at least one message is required").Kind != RefusalOther {
		t.Error("a refusal about the request at large was read as one about the tier")
	}
	if slot(TierOn, OpenAICompatible).Refused(429, "rate limited").Kind != RefusalOther {
		t.Error("a 429 was read as a refusal of the tier")
	}

	// Unavailable only after two consecutive answers carry no reasoning.
	quiet := slot(TierOn, OpenAICompatible)
	if turnOf(quiet, false, true) != nil || quiet.State() != StateOn {
		t.Error("one quiet answer reached unavailable")
	}
	said := turnOf(quiet, false, true)
	if said == nil || said.ThinkingUnavailable == nil {
		t.Fatal("two quiet answers said nothing")
	}
	contains(t, "the notice", said.ThinkingUnavailable.Detail, ThinkingUnavailable)
	if quiet.State() != StateUnavailable {
		t.Error("two quiet answers did not reach unavailable")
	}

	// A turn that only called a tool is not counted at on either.
	tools := slot(TierOn, OpenAICompatible)
	for range 3 {
		if turnOf(tools, false, false) != nil {
			t.Error("a tool-only turn said something")
		}
	}
	if tools.State() != StateOn || turnOf(tools, true, true) != nil || tools.State() != StateOn {
		t.Error("tool-only turns stripped the tier")
	}

	// A turn that does reason starts the count again.
	restarted := slot(TierOn, OpenAICompatible)
	turnOf(restarted, false, true)
	turnOf(restarted, true, true)
	turnOf(restarted, false, true)
	if restarted.State() != StateOn {
		t.Error("the count did not start again")
	}
	if turnOf(restarted, false, true) == nil || restarted.State() != StateUnavailable {
		t.Error("two quiet answers after the restart said nothing")
	}

	// Nothing is said about a quiet turn when the tier is off.
	quietOff := slot(TierOff, OpenAICompatible)
	if turnOf(quietOff, false, true) != nil || turnOf(quietOff, false, true) != nil || quietOff.State() != StateOff {
		t.Error("a quiet turn at off said something")
	}

	// A truncated signature degrades once.
	truncated := slot(TierOn, Anthropic)
	notice := truncated.Truncated("the signature came back as a prefix of what arrived")
	if notice == nil || notice.ThinkingUnavailable == nil {
		t.Fatal("a truncated signature said nothing")
	}
	contains(t, "the reason", notice.ThinkingUnavailable.Detail, "prefix")
	if truncated.State() != StateUnavailable || truncated.Truncated("again") != nil {
		t.Error("a truncated signature degraded twice")
	}
}

// TestTheOpeningNotice is openThinking's `opening` (agent#4): a record that leaves the tier
// nothing to ask with is unavailable from the start, and says so once.
func TestTheOpeningNotice(t *testing.T) {
	ignored := true
	for _, held := range []struct {
		reasoning *ReasoningCapability
		tier      ThinkingTier
		want      string
	}{
		{&ReasoningCapability{Kind: NoReasoning}, TierOn, "its capability record names no reasoning parameter"},
		{&ReasoningCapability{Kind: OpenAIEffort, Ignored: &ignored}, TierOn, "its capability record says the endpoint ignores the reasoning parameter"},
	} {
		opened := OpenThinking(held.tier, OpenAICompatible, held.reasoning)
		notice := opened.Opening()
		if notice == nil || notice.ThinkingUnavailable == nil {
			t.Fatalf("%v said nothing", held.reasoning)
		}
		contains(t, "the opening notice", notice.ThinkingUnavailable.Detail, held.want)
		if opened.State() != StateUnavailable || opened.Members() != nil {
			t.Error("a silent record still asked")
		}
		if opened.Opening() != nil {
			t.Error("the opening notice was said twice")
		}
	}
	// Nothing at off, and nothing where the record leaves something to ask with.
	if OpenThinking(TierOff, OpenAICompatible, &ReasoningCapability{Kind: NoReasoning}).Opening() != nil {
		t.Error("off owed an opening notice")
	}
	if OpenThinking(TierOn, OpenAICompatible, &ReasoningCapability{Kind: OpenAIEffort}).Opening() != nil {
		t.Error("a record with a parameter owed an opening notice")
	}
}

// TestATruncatedSignature is "a signature that came back short".
func TestATruncatedSignature(t *testing.T) {
	for _, held := range []struct {
		sent, carried string
		want          bool
	}{
		{"abcdef", "abc", true},
		{"abcdef", "def", true},
		{"abcdef", "abcdef", false},
		{"abc", "abc", false},
		{"xyz", "abc", false},
		{"", "abc", false},
		{"abcdef", "", false},
	} {
		if got := IsTruncatedSignature(held.sent, held.carried); got != held.want {
			t.Errorf("%q against %q: %v", held.sent, held.carried, got)
		}
	}
}

// TestTheLineTheTabShows is "the line the tab shows" and "is read off the run's own events".
func TestTheLineTheTabShows(t *testing.T) {
	equalText(t, "off", ThinkingLine(TierOff, StateOff), "thinking off")
	equalText(t, "ultra", ThinkingLine(TierUltra, StateUltra), "thinking ultra")
	contains(t, "always", ThinkingLine(TierOff, StateAlways), "always thinks")
	contains(t, "unavailable", ThinkingLine(TierOn, StateUnavailable), "unavailable for this endpoint")
	contains(t, "the tier beside it", ThinkingLine(TierOn, StateUnavailable), "thinking on")

	notice := func(detail string) contract.RunEvent {
		return contract.RunEvent{ThinkingUnavailable: &contract.RunEventThinkingUnavailable{Type: "thinking_unavailable", Detail: detail}}
	}
	if StateFromEvents(TierOn, nil) != StateOn {
		t.Error("a run that said nothing is not at its tier")
	}
	if StateFromEvents(TierOn, []contract.RunEvent{notice(ThinkingUnavailable + ": the endpoint answered 400")}) != StateUnavailable {
		t.Error("the degrade was not read off the events")
	}
	if StateFromEvents(TierOff, []contract.RunEvent{notice(ThinkingAlways + ".")}) != StateAlways {
		t.Error("always was not read off the events")
	}
}

// TestTheGeminiWireWhereOffIsAMember is "the Gemini wire, where off is a member the endpoint may
// refuse".
func TestTheGeminiWireWhereOffIsAMember(t *testing.T) {
	// The off member is sent, so a refusal of it is a thing the endpoint said.
	sent := slot(TierOff, Gemini)
	equalJSON(t, "off's member", sent.Members(), Obj(F("thinkingConfig", Obj(F("thinkingBudget", Int(0))))))
	if sent.State() != StateOff {
		t.Error("off on gemini did not start at off")
	}

	// The other spelling is tried before anything is concluded from a refusal at off.
	tried := slot(TierOff, Gemini)
	if held := tried.Refused(400, `Unknown name "thinkingBudget" in generationConfig.thinkingConfig`); held.Kind != RefusalRetry {
		t.Errorf("the first refusal at off was %s", held.Kind)
	}
	if tried.State() != StateOff {
		t.Error("the silent fallback at off changed the state")
	}
	equalJSON(t, "the level spelling", tried.Members(), Obj(F("thinkingConfig", Obj(F("thinkingLevel", Str("minimal"))))))

	// Always when every spelling of off is refused, at once and once.
	refusedOff := slot(TierOff, Gemini)
	refusedOff.Refused(400, `Unknown name "thinkingBudget"`)
	said := refusedOff.Refused(400, "thinkingLevel: minimal is not supported by this model")
	if said.Kind != RefusalDegrade || said.Event == nil || said.Event.ThinkingUnavailable == nil {
		t.Fatalf("every spelling refused answered %s", said.Kind)
	}
	contains(t, "the notice", said.Event.ThinkingUnavailable.Detail, ThinkingAlways)
	contains(t, "the status in it", said.Event.ThinkingUnavailable.Detail, "400")
	if refusedOff.State() != StateAlways || refusedOff.Members() != nil {
		t.Error("the member did not come off with the state")
	}
	if refusedOff.Refused(400, "thinkingBudget").Kind != RefusalOther {
		t.Error("a refusal after always was still about the tier")
	}

	// Always from absence too, and the desk keeps asking for what the file asked.
	absent := slot(TierOff, Gemini)
	absent.SawReasoning()
	if absent.TurnEnded(true) != nil {
		t.Error("one reasoning turn at off on gemini said something")
	}
	absent.SawReasoning()
	second := absent.TurnEnded(true)
	if second == nil || second.ThinkingUnavailable == nil {
		t.Fatal("two reasoning turns at off on gemini said nothing")
	}
	contains(t, "the state", second.ThinkingUnavailable.Detail, ThinkingAlways)
	contains(t, "the evidence", second.ThinkingUnavailable.Detail, AlwaysFromAbsence)
	if absent.State() != StateAlways {
		t.Error("absence did not reach always")
	}
	equalJSON(t, "what the desk goes on asking for", absent.Members(), Obj(F("thinkingConfig", Obj(F("thinkingBudget", Int(0))))))

	// A degrade rather than always where the tier asked to think.
	asking := slot(TierOn, Gemini)
	asking.Refused(400, `Unknown name "thinkingBudget"`)
	if held := asking.Refused(400, "thinkingLevel is not supported"); held.Kind != RefusalDegrade {
		t.Errorf("the second refusal at on was %s", held.Kind)
	}
	if asking.State() != StateUnavailable || asking.Members() != nil {
		t.Error("the degrade at on left the tier asking")
	}

	// A refusal that names none of its members is an ordinary failure.
	ordinary := slot(TierOn, Gemini)
	if ordinary.Refused(400, "The document was too large").Kind != RefusalOther {
		t.Error("a refusal naming no member was read as one about the tier")
	}
	if ordinary.Refused(429, "thinkingBudget").Kind != RefusalOther {
		t.Error("a 429 was read as a refusal of the tier")
	}
	if ordinary.State() != StateOn {
		t.Error("an ordinary failure changed the state")
	}
}
