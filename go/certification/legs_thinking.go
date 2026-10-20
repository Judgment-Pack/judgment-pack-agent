package certification

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/wire"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

// The thinking block (engine %s · thinking): the tier on every request, reasoning streamed and kept
// from the runtime, signatures carried back, the refutation pass where the session asks for it,
// the degrade, and the Gemini and Anthropic wires' own legs.

const thinkingBlock = "engine %s · thinking"

// expectation is the tier parameter the desk's table puts on a family's first request.
func expectation(tier, api string) *wire.Expectation {
	normalized := wire.Normalize(wire.ThinkingTier(tier), wire.EndpointKind(api))
	if normalized.Wire == nil {
		return nil
	}
	return &normalized.Wire.Expect
}

func paramIs(param *ThinkingParam, value *wire.Value) bool {
	return param != nil && value != nil && sameJSON(param.Value, value.JSON())
}

// thinkingLeg runs one thinking leg.
func thinkingLeg(ctx context.Context, server *Server, endpoint Endpoint, mode, tier string, review *bool, adapter Adapter, edit func(*Endpoint, *LegOptions)) (*LegResult, error) {
	endpoint.Thinking = mode
	options := LegOptions{Tier: tier, Review: review}
	if edit != nil {
		edit(&endpoint, &options)
	}
	return run(ctx, server, endpoint, options, adapter)
}

// signaturesHeld is the T-b rows: every request after a result carries back every signature the
// endpoint emitted, whole, on a part that may carry one.
func signaturesHeld(f *findings, r *LegResult) {
	var after []RecordedRequest
	for _, request := range r.Observations.Requests {
		if request.Results >= 1 && !request.Refutation {
			after = append(after, request)
		}
	}
	if len(after) == 0 {
		f.fail("no request after a result")
		return
	}
	for _, request := range after {
		if len(request.SignaturesMissing) > 0 || len(request.SignaturesTruncated) > 0 || len(request.SignaturesMalformed) > 0 {
			f.fail("%s: missing %v, truncated %v, malformed %v", request.Step, request.SignaturesMissing, request.SignaturesTruncated, request.SignaturesMalformed)
		}
	}
	if !slices.Contains(after[0].SignaturesCarried, thinkSignature("T1")) {
		f.fail("the first step's signature was not carried back")
	}
}

func noErrors(f *findings, r *LegResult) {
	if errs := errorsOf(r.Events); len(errs) > 0 {
		f.fail("error events: %s", errs[0].Message)
	}
}

func oneDraft(f *findings, r *LegResult) {
	if proposals := proposalsOf(r.Events); len(proposals) != 1 || !isDraftV2(r, proposals[0]) {
		f.fail("not one DRAFT_V2 proposal")
	}
}

// criticChecks are what a passed critic shows (runs the refutation pass, takes its verdict from the
// runtime, and shows it).
func criticChecks(f *findings, r *LegResult) {
	var marked []RecordedRequest
	for _, request := range r.Observations.Requests {
		if request.Refutation {
			marked = append(marked, request)
		}
	}
	if len(marked) == 0 {
		f.fail("no request of the critic")
	}
	_, offers, _ := ReadVector("tool-offer/critic-offer.json")
	var offer contract.ToolOffer
	if len(offers) > 0 {
		_ = json.Unmarshal(offers[0], &offer)
	}
	var want []string
	for _, tool := range offer.HostTools {
		want = append(want, tool.Name)
	}
	for _, tool := range offer.Tools {
		want = append(want, tool.Name)
	}
	for _, request := range marked {
		if !slices.Equal(request.ToolNames, want) {
			f.fail("the critic was offered %v, not %v", request.ToolNames, want)
		}
	}
	critiques := critiquesOf(r.Events)
	if len(critiques) != 1 {
		f.fail("%d critiques", len(critiques))
		return
	}
	critique := critiques[0]
	checks := make([]string, 0, len(critique.Checks))
	for _, check := range critique.Checks {
		checks = append(checks, check.Tool+" "+check.Status)
	}
	if critique.Refuted || !slices.Equal(checks, []string{"validate valid", "experimental_evaluate evaluated"}) || !strings.Contains(critique.Text, "quoted from the runtime") {
		f.fail("the critique is refuted %t, checks %v, text %q", critique.Refuted, checks, critique.Text)
	}
	kinds := eventTypes(r.Events)
	if slices.Index(kinds, "critique") > slices.Index(kinds, "proposal") {
		f.fail("the critique came after the proposal")
	}
	if proposals := proposalsOf(r.Events); len(proposals) == 0 || proposals[0].Critique == nil || proposals[0].Critique.Refuted {
		f.fail("the proposal does not carry refuted: false")
	}
	evaluates := 0
	for _, call := range r.Observations.Seen {
		if call.Name == "experimental_evaluate" {
			evaluates++
			if call.Args["rehearsal"] != true {
				f.fail("an evaluate without rehearsal reached the runtime")
			}
		}
	}
	if evaluates != 2 {
		f.fail("%d evaluates, not two", evaluates)
	}
	if refused := refusals(r.Observations.Seen); len(refused) > 0 {
		f.fail("the runtime refused %v", refused)
	}
}

// ThinkingChecks are the thinking block's checks on every leg.
var ThinkingChecks = []Check{
	{Name: "T-a", Block: thinkingBlock, Legs: AllLegs, Run: func(ctx context.Context, server *Server, endpoint Endpoint, adapter Adapter) ([]Finding, error) {
		r, err := thinkingLeg(ctx, server, endpoint, "on", "on", boolPtr(true), adapter, nil)
		if err != nil {
			return nil, err
		}
		f := &findings{check: "T-a"}
		if len(r.Observations.Requests) <= 7 {
			f.fail("%d requests", len(r.Observations.Requests))
		}
		expect := expectation("on", endpoint.API)
		for at, request := range r.Observations.Requests {
			if !request.ThinkingRequested {
				f.fail("request %d (%s) carried no tier parameter", at, request.Step)
				continue
			}
			if expect == nil || request.ThinkingParam == nil || request.ThinkingParam.Path != expect.Path || !paramIs(request.ThinkingParam, expect.Value) {
				f.fail("request %d (%s) carried %v", at, request.Step, request.ThinkingParam)
			}
		}
		return f.list, nil
	}},
	{Name: "thinking parts", Block: thinkingBlock, Legs: AllLegs, Run: func(ctx context.Context, server *Server, endpoint Endpoint, adapter Adapter) ([]Finding, error) {
		r, err := thinkingLeg(ctx, server, endpoint, "on", "on", boolPtr(true), adapter, func(_ *Endpoint, options *LegOptions) { options.Host = &Supplied })
		if err != nil {
			return nil, err
		}
		f := &findings{check: "thinking parts"}
		if len(r.bodies) == authoringBodies(r.bodies) {
			f.fail("no request of the critic among the bodies")
		}
		if authoringBodies(r.bodies) <= 7 {
			f.fail("%d authoring bodies", authoringBodies(r.bodies))
		}
		// A thinking tier on the Anthropic wire puts the state at the system's tail, where the request
		// text is no breakpoint; and the critic's requests carry the same parts after the critic's
		// guidance, the state in the same place.
		placement := wire.TrailingUser
		if endpoint.API == "anthropic" {
			placement = wire.SystemTail
		}
		for _, miss := range placementMisses(endpoint.API, r, Supplied, false, placementHow{placement: placement}) {
			f.fail("%s", miss)
		}
		for _, miss := range placementMisses(endpoint.API, r, Supplied, false, placementHow{critic: true, placement: placement}) {
			f.fail("%s", miss)
		}
		return f.list, nil
	}},
	{Name: "ultra", Block: thinkingBlock, Legs: AllLegs, Run: func(ctx context.Context, server *Server, endpoint Endpoint, adapter Adapter) ([]Finding, error) {
		r, err := thinkingLeg(ctx, server, endpoint, "on", "ultra", boolPtr(true), adapter, nil)
		if err != nil {
			return nil, err
		}
		f := &findings{check: "ultra"}
		expect := expectation("ultra", endpoint.API)
		for at, request := range r.Observations.Requests {
			if expect == nil || !paramIs(request.ThinkingParam, expect.Value) {
				f.fail("request %d carried %v", at, request.ThinkingParam)
			}
		}
		return f.list, nil
	}},
	{Name: "reasoning", Block: thinkingBlock, Legs: AllLegs, Run: func(ctx context.Context, server *Server, endpoint Endpoint, adapter Adapter) ([]Finding, error) {
		r, err := thinkingLeg(ctx, server, endpoint, "on", "on", boolPtr(true), adapter, nil)
		if err != nil {
			return nil, err
		}
		f := &findings{check: "reasoning"}
		var passages []*contract.RunEventReasoning
		for _, event := range reasoningOf(r.Events) {
			if event.Done {
				passages = append(passages, event)
			}
		}
		if len(passages) == 0 || !strings.Contains(passages[0].Text, thinkText("T1", "get_schema")) {
			f.fail("the first passage is not the endpoint's T1 reasoning")
		}
		sent, _ := json.Marshal(r.Observations.Seen)
		if strings.Contains(string(sent), "[scripted reasoning") || strings.Contains(string(sent), "must not state a verdict") {
			f.fail("reasoning reached the runtime")
		}
		return f.list, nil
	}},
	{Name: "T-b", Block: thinkingBlock, Legs: AllLegs, Run: func(ctx context.Context, server *Server, endpoint Endpoint, adapter Adapter) ([]Finding, error) {
		if endpoint.API == "openai-compatible" {
			// The OpenAI-compatible wire signs no reasoning: there is nothing to hold it to, as the
			// TypeScript leg returns there.
			return nil, nil
		}
		r, err := thinkingLeg(ctx, server, endpoint, "on", "on", nil, adapter, nil)
		if err != nil {
			return nil, err
		}
		f := &findings{check: "T-b"}
		signaturesHeld(f, r)
		return f.list, nil
	}},
	{Name: "critic", Block: thinkingBlock, Legs: AllLegs, Run: func(ctx context.Context, server *Server, endpoint Endpoint, adapter Adapter) ([]Finding, error) {
		r, err := thinkingLeg(ctx, server, endpoint, "on", "on", boolPtr(true), adapter, nil)
		if err != nil {
			return nil, err
		}
		f := &findings{check: "critic"}
		criticChecks(f, r)
		return f.list, nil
	}},
	{Name: "critic refuted", Block: thinkingBlock, Legs: AllLegs, Run: func(ctx context.Context, server *Server, endpoint Endpoint, adapter Adapter) ([]Finding, error) {
		r, err := thinkingLeg(ctx, server, endpoint, "on", "on", boolPtr(true), adapter, func(e *Endpoint, _ *LegOptions) { e.Refuted = true })
		if err != nil {
			return nil, err
		}
		f := &findings{check: "critic refuted"}
		critiques := critiquesOf(r.Events)
		if len(critiques) != 1 || !critiques[0].Refuted || len(critiques[0].Checks) == 0 ||
			critiques[0].Checks[0].Tool != "validate" || critiques[0].Checks[0].Status != "invalid" || !strings.Contains(critiques[0].Text, "1 diagnostic(s)") {
			f.fail("the critique is not refuted by validate's one diagnostic: %+v", critiques)
		}
		proposals := proposalsOf(r.Events)
		if len(proposals) != 1 || proposals[0].Critique == nil || !proposals[0].Critique.Refuted || !isDraftV2(r, proposals[0]) {
			f.fail("the proposal does not carry refuted: true")
		}
		noErrors(f, r)
		return f.list, nil
	}},
	{Name: "no critic", Block: thinkingBlock, Legs: AllLegs, Run: func(ctx context.Context, server *Server, endpoint Endpoint, adapter Adapter) ([]Finding, error) {
		f := &findings{check: "no critic"}
		for _, tier := range []string{"off", "on"} {
			r, err := thinkingLeg(ctx, server, endpoint, tier, tier, nil, adapter, nil)
			if err != nil {
				return nil, err
			}
			for _, request := range r.Observations.Requests {
				if request.Refutation {
					f.fail("%s: a request of the critic", tier)
				}
			}
			if len(critiquesOf(r.Events)) > 0 {
				f.fail("%s: a critique", tier)
			}
			if proposals := proposalsOf(r.Events); len(proposals) == 0 || proposals[0].Critique != nil {
				f.fail("%s: the proposal carries a critique or there is none", tier)
			}
		}
		return f.list, nil
	}},
}

// declaredCase is one case the host declares for the critic, over the scenario's facts.
func declaredCase(id, source string, facts json.RawMessage, outcome string) json.RawMessage {
	encoded, _ := json.Marshal(map[string]any{"source": source, "id": id, "facts": facts,
		"expectedDisposition": map[string]any{"kind": "outcome", "outcomeId": outcome}})
	return encoded
}

func comparisonJSON(value any) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

// MoreThinkingChecks are the rest of the thinking block on every leg.
var MoreThinkingChecks = []Check{
	{Name: "critic cases", Block: thinkingBlock, Legs: AllLegs, Run: func(ctx context.Context, server *Server, endpoint Endpoint, adapter Adapter) ([]Finding, error) {
		f := &findings{check: "critic cases"}
		scenario, err := server.Scenario(ctx)
		if err != nil {
			return nil, err
		}
		facts := scenario.Documents["FACTS"]
		wrong := declaredCase("receipted-120", "declared", facts, "decline")
		r, err := thinkingLeg(ctx, server, endpoint, "on", "on", boolPtr(true), adapter, func(_ *Endpoint, o *LegOptions) { o.Cases = []json.RawMessage{wrong} })
		if err != nil {
			return nil, err
		}
		critiques := critiquesOf(r.Events)
		if len(critiques) != 1 {
			return append(f.list, Finding{"critic cases", "not one critique"}), nil
		}
		critique := critiques[0]
		want := `[{"case":"receipted-120","source":"declared","tool":"experimental_evaluate","agrees":false,"expected":{"disposition":{"kind":"outcome","outcomeId":"decline"}},"actual":{"disposition":{"handoff":{"state":"none"},"kind":"outcome","outcomeId":"approve","reasons":[]}}}]`
		if !critique.Refuted || len(critique.Checks) != 2 || !sameJSON([]byte(comparisonJSON(critique.Cases)), []byte(want)) || len(critique.Incomplete) != 0 ||
			!strings.Contains(critique.Text, "case receipted-120 (declared) disagrees") {
			f.fail("the critique was %s", comparisonJSON(critique))
		}
		if proposals := proposalsOf(r.Events); len(proposals) == 0 || proposals[0].Critique == nil || !proposals[0].Critique.Refuted {
			f.fail("the proposal does not carry refuted: true")
		}
		agree := declaredCase("receipted-120", "declared", facts, "approve")
		small := declaredCase("small-40", "test", json.RawMessage(`{"claim":{"type":"expense-reimbursement","amount":"40.00","receiptAttached":false}}`), "approve")
		agreed, err := thinkingLeg(ctx, server, endpoint, "on", "on", boolPtr(true), adapter, func(_ *Endpoint, o *LegOptions) { o.Cases = []json.RawMessage{agree, small} })
		if err != nil {
			return nil, err
		}
		held := critiquesOf(agreed.Events)
		if len(held) != 1 || !held[0].Refuted || !slices.Equal(held[0].Incomplete, []string{"small-40"}) || len(held[0].Cases) != 1 || !held[0].Cases[0].Agrees {
			f.fail("the second critique was %s", comparisonJSON(held))
		}
		if proposals := proposalsOf(agreed.Events); len(proposals) == 0 || proposals[0].Critique == nil || !proposals[0].Critique.Refuted ||
			!slices.Equal(proposals[0].Critique.Incomplete, []string{"small-40"}) {
			f.fail("the second proposal's critique is not refuted with small-40 incomplete")
		}
		return f.list, nil
	}},
	{Name: "nothink", Block: thinkingBlock, Legs: AllLegs, Run: func(ctx context.Context, server *Server, endpoint Endpoint, adapter Adapter) ([]Finding, error) {
		r, err := thinkingLeg(ctx, server, endpoint, "nothink", "on", nil, adapter, nil)
		if err != nil {
			return nil, err
		}
		f := &findings{check: "nothink"}
		notices := noticesOf(r.Events)
		if len(notices) != 1 || !strings.Contains(notices[0].Detail, "unavailable for this endpoint") {
			f.fail("the notices were %v", notices)
		}
		if !slices.Contains(seenNames(r.Observations.Seen), "experimental_evaluate") {
			f.fail("the scenario did not run")
		}
		oneDraft(f, r)
		ends := 0
		for _, kind := range eventTypes(r.Events) {
			if kind == "end" {
				ends++
			}
		}
		if ends != 1 {
			f.fail("%d ends", ends)
		}
		noErrors(f, r)
		spellings := map[string]int{"openai-compatible": 1, "anthropic": 2, "gemini": 2}[endpoint.API]
		prefix := len(r.Observations.Requests)
		for at, request := range r.Observations.Requests {
			if !request.ThinkingRequested {
				prefix = at
				break
			}
		}
		if prefix > spellings {
			f.fail("the tier was asked for %d times after the endpoint refused it", prefix)
		}
		for _, request := range r.Observations.Requests[prefix:] {
			if request.ThinkingRequested {
				f.fail("%s asked for the tier again", request.Step)
			}
		}
		return f.list, nil
	}},
	{Name: "critic degraded", Block: thinkingBlock, Legs: AllLegs, Run: func(ctx context.Context, server *Server, endpoint Endpoint, adapter Adapter) ([]Finding, error) {
		r, err := thinkingLeg(ctx, server, endpoint, "nothink", "on", boolPtr(true), adapter, nil)
		if err != nil {
			return nil, err
		}
		f := &findings{check: "critic degraded"}
		marked := false
		for _, request := range r.Observations.Requests {
			marked = marked || request.Refutation
		}
		critiques := critiquesOf(r.Events)
		if !marked || len(critiques) != 1 || len(critiques[0].Checks) != 2 {
			f.fail("the critic did not run with its two checks on a degraded endpoint")
		}
		return f.list, nil
	}},
	{Name: "critic ungranted", Block: thinkingBlock, Legs: []Endpoint{{API: "openai-compatible", AnswerAs: "stream"}}, Run: func(ctx context.Context, server *Server, endpoint Endpoint, adapter Adapter) ([]Finding, error) {
		r, err := thinkingLeg(ctx, server, endpoint, "on", "on", boolPtr(true), adapter, func(_ *Endpoint, o *LegOptions) {
			o.Allowed = []string{"get_schema", "list_examples", "get_example"}
		})
		if err != nil {
			return nil, err
		}
		f := &findings{check: "critic ungranted"}
		names := seenNames(r.Observations.Seen)
		if slices.Contains(names, "validate") || slices.Contains(names, "experimental_evaluate") || len(refusals(r.Observations.Seen)) > 0 {
			f.fail("the runtime saw %v", names)
		}
		critiques := critiquesOf(r.Events)
		if len(critiques) != 1 || len(critiques[0].Checks) != 0 || critiques[0].Refuted || !strings.Contains(critiques[0].Text, "no runtime check") {
			f.fail("the critique was %s", comparisonJSON(critiques))
		}
		proposals := proposalsOf(r.Events)
		if len(proposals) != 1 || proposals[0].Critique != nil {
			f.fail("the proposal carries a refutation line")
		}
		var refused []string
		for _, g := range guardrailsOf(r.Events, "refused") {
			refused = append(refused, g.Tool)
		}
		if !slices.Contains(refused, "validate") {
			f.fail("the refusals were %v", refused)
		}
		return f.list, nil
	}},
}
