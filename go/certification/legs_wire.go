package certification

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/wire"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

// The Gemini wire's and the Anthropic dialects' legs of the thinking block.

// EngineID is the id the schema rules name the Go adapter by.
const EngineID = wire.EngineID

// contextToolCount is how many context tools a request shows before the runtime's (the offer
// vector's host tools).
func contextToolCount() int {
	tools, _ := BoundaryHostTools()
	return len(tools)
}

// vocabulary is one tool whose schema carries exactly the recorded runtime's own schema vocabulary
// (VOCABULARY in conformance.test.ts), under the first recorded tool's name.
func vocabulary() contract.ToolOfferRuntimeTool {
	description := "the recorded runtime’s own schema vocabulary"
	return contract.ToolOfferRuntimeTool{Name: "get_schema", Description: &description, InputSchema: json.RawMessage(`{"$schema":"https://json-schema.org/draft/2020-12/schema","$id":"urn:judgment-pack-desk:conformance:vocabulary","$comment":"every keyword the recorded runtime 0.19.0 emits","title":"Vocabulary","description":"the recorded runtime’s own schema vocabulary","type":"object","additionalProperties":false,"required":["a"],"$defs":{"named":{"type":"string","minLength":1}},"properties":{"a":{"type":"string","pattern":"^[a-z]+$","minLength":1,"format":"uri"},"b":{"$ref":"#/$defs/named"},"c":{"type":"array","items":{"type":"string"},"minItems":1,"uniqueItems":true},"d":{"type":"object","propertyNames":{"type":"string"}},"e":{"type":"string","enum":["x","y"]},"f":{"anyOf":[{"type":"string"},{"type":"number"}]},"g":{"allOf":[{"type":"object"}]},"h":{"oneOf":[{"type":"string"},{"type":"number"}]},"i":{"not":{"type":"string"}},"j":{"type":"string","const":"x"},"k":{"if":{"type":"string"},"then":{"minLength":1}}}}`)}
}

func mcp(tool contract.ToolOfferRuntimeTool) wire.McpTool {
	description := ""
	if tool.Description != nil {
		description = *tool.Description
	}
	schema, _ := wire.Parse(tool.InputSchema)
	return wire.McpTool{Name: tool.Name, Description: description, InputSchema: schema}
}

func keywordsOf(raw json.RawMessage) map[string]bool {
	value, err := wire.Parse(raw)
	if err != nil {
		return map[string]bool{}
	}
	return wire.KeywordsSent(value)
}

func sameValue(raw json.RawMessage, value *wire.Value) bool {
	return value != nil && sameJSON(raw, value.JSON())
}

// modelTurns are the role: model turns a Gemini request carried back, each its parts.
func modelTurns(request RecordedRequest) [][]map[string]any {
	var out [][]map[string]any
	for _, raw := range request.ModelParts {
		var parts []map[string]any
		_ = json.Unmarshal(raw, &parts)
		out = append(out, parts)
	}
	return out
}

func alwaysChecks(f *findings, r *LegResult, evidence string) {
	notices := noticesOf(r.Events)
	if len(notices) != 1 || !strings.Contains(notices[0].Detail, wire.ThinkingAlways) || !strings.Contains(notices[0].Detail, evidence) {
		f.fail("the notices were %s", comparisonJSON(notices))
	}
	if state := wire.StateFromEvents(wire.TierOff, r.Events); state != "always" {
		f.fail("the state read from the events is %s", state)
	}
}

func level(param *ThinkingParam) bool {
	if param == nil {
		return false
	}
	var value map[string]any
	_ = json.Unmarshal(param.Value, &value)
	_, ok := value["thinkingLevel"]
	return ok
}

func paramType(param *ThinkingParam) string {
	if param == nil {
		return ""
	}
	var value map[string]any
	_ = json.Unmarshal(param.Value, &value)
	kind, _ := value["type"].(string)
	return kind
}

// WireChecks are the Gemini wire's legs and the Anthropic dialects'.
var WireChecks = []Check{
	{Name: "gemini shown", Block: thinkingBlock, Legs: geminiLegs, Run: func(ctx context.Context, server *Server, endpoint Endpoint, adapter Adapter) ([]Finding, error) {
		r, err := run(ctx, server, endpoint, LegOptions{}, adapter)
		if err != nil {
			return nil, err
		}
		f := &findings{check: "gemini shown"}
		if len(r.Observations.Requests) == 0 {
			f.fail("no request")
			return f.list, nil
		}
		var shown []*wire.Value
		for _, tool := range r.offer.Tools {
			shown = append(shown, wire.SchemaShown(EngineID, wire.Gemini, mcp(tool).InputSchema))
		}
		skip := contextToolCount()
		for _, request := range r.Observations.Requests {
			if len(request.Schemas) != skip+len(shown) {
				f.fail("%s sent %d schemas", request.Step, len(request.Schemas))
				continue
			}
			for at, schema := range request.Schemas[skip:] {
				if !sameValue(schema, shown[at]) {
					f.fail("%s sent a different schema for %s", request.Step, r.offer.Tools[at].Name)
				}
			}
		}
		for _, keyword := range wire.GeminiSchemaRemovals {
			if slices.Contains(r.Observations.Requests[0].SchemaKeywords, keyword) {
				f.fail("showed the model %s", keyword)
			}
		}
		if len(proposalsOf(r.Events)) != 1 || len(refusals(r.Observations.Seen)) > 0 {
			f.fail("not one proposal with nothing refused")
		}
		noErrors(f, r)
		return f.list, nil
	}},
	{Name: "gemini removals", Block: thinkingBlock, Legs: geminiLegs, Run: func(ctx context.Context, server *Server, endpoint Endpoint, adapter Adapter) ([]Finding, error) {
		probe := vocabulary()
		r, err := run(ctx, server, endpoint, LegOptions{Tools: []contract.ToolOfferRuntimeTool{probe}}, adapter)
		if err != nil {
			return nil, err
		}
		f := &findings{check: "gemini removals"}
		desk, err := wire.ServedSchemaFor(wire.Gemini, mcp(probe))
		if err != nil {
			return nil, err
		}
		skip := contextToolCount()
		if len(r.Observations.Requests) == 0 || len(r.Observations.Requests[0].Schemas) <= skip {
			f.fail("no request carried the probe")
			return f.list, nil
		}
		carried := keywordsOf(r.Observations.Requests[0].Schemas[skip])
		for keyword := range wire.KeywordsSent(desk) {
			if !carried[keyword] {
				f.fail("the adapter removed %s, which this desk does not declare", keyword)
			}
		}
		for _, request := range r.Observations.Requests {
			if len(request.Schemas) != skip+1 || !sameValue(request.Schemas[skip], desk) {
				f.fail("%s did not show the desk's schema", request.Step)
			}
		}
		return f.list, nil
	}},
	{Name: "gemini notices", Block: thinkingBlock, Legs: geminiLegs, Run: func(ctx context.Context, server *Server, endpoint Endpoint, adapter Adapter) ([]Finding, error) {
		r, err := run(ctx, server, endpoint, LegOptions{}, adapter)
		if err != nil {
			return nil, err
		}
		f := &findings{check: "gemini notices"}
		if narrowed := guardrailsOf(r.Events, "narrowed"); len(narrowed) > 0 {
			f.fail("narrowing notices: %s", comparisonJSON(narrowed))
		}
		for _, tool := range r.offer.Tools {
			if lost := wire.KeywordsLost(EngineID, wire.Gemini, mcp(tool).InputSchema); len(lost) > 0 {
				f.fail("%s loses %v", tool.Name, lost)
			}
		}
		return f.list, nil
	}},
	{Name: "gemini vocabulary", Block: thinkingBlock, Legs: geminiLegs, Run: func(ctx context.Context, server *Server, endpoint Endpoint, adapter Adapter) ([]Finding, error) {
		probe := vocabulary()
		r, err := run(ctx, server, endpoint, LegOptions{Tools: []contract.ToolOfferRuntimeTool{probe}}, adapter)
		if err != nil {
			return nil, err
		}
		f := &findings{check: "gemini vocabulary"}
		desk, err := wire.ServedSchemaFor(wire.Gemini, mcp(probe))
		if err != nil {
			return nil, err
		}
		skip := contextToolCount()
		if len(r.Observations.Requests) == 0 {
			f.fail("no request")
		}
		for _, request := range r.Observations.Requests {
			if len(request.Schemas) != skip+1 || !sameValue(request.Schemas[skip], desk) {
				f.fail("%s did not show the served schema", request.Step)
			}
		}
		if narrowed := guardrailsOf(r.Events, "narrowed"); len(narrowed) > 0 {
			f.fail("narrowing notices")
		}
		noErrors(f, r)
		return f.list, nil
	}},
	{Name: "gemini keyword", Block: thinkingBlock, Legs: geminiLegs, Run: func(ctx context.Context, server *Server, endpoint Endpoint, adapter Adapter) ([]Finding, error) {
		r, err := thinkingLeg(ctx, server, endpoint, "refuses-a-keyword", "off", nil, adapter, nil)
		if err != nil {
			return nil, err
		}
		f := &findings{check: "gemini keyword"}
		errs := errorsOf(r.Events)
		if len(errs) != 1 || !strings.Contains(errs[0].Message, `"properties"`) || !strings.Contains(errs[0].Message, "closed") {
			f.fail("the errors were %s", comparisonJSON(errs))
		} else {
			for _, keyword := range wire.GeminiSchemaRemovals {
				if !strings.Contains(errs[0].Message, keyword) {
					f.fail("the error does not name %s", keyword)
				}
			}
		}
		if len(proposalsOf(r.Events)) > 0 || len(r.Observations.Seen) > 0 {
			f.fail("a proposal, or a call that reached the runtime")
		}
		return f.list, nil
	}},
	{Name: "gemini level", Block: thinkingBlock, Legs: geminiLegs, Run: func(ctx context.Context, server *Server, endpoint Endpoint, adapter Adapter) ([]Finding, error) {
		r, err := thinkingLeg(ctx, server, endpoint, "level-only", "on", nil, adapter, nil)
		if err != nil {
			return nil, err
		}
		f := &findings{check: "gemini level"}
		requests := r.Observations.Requests
		expect := expectation("on", "gemini")
		if len(requests) == 0 || expect == nil || !paramIs(requests[0].ThinkingParam, expect.Value) {
			f.fail("the first request did not ask with a budget")
		}
		var accepted []RecordedRequest
		for _, request := range requests {
			if level(request.ThinkingParam) {
				accepted = append(accepted, request)
			}
		}
		if len(accepted) <= 5 || !sameJSON(accepted[0].ThinkingParam.Value, []byte(`{"includeThoughts":true,"thinkingLevel":"medium"}`)) {
			f.fail("%d requests asked with a level", len(accepted))
		}
		if len(noticesOf(r.Events)) > 0 || len(reasoningOf(r.Events)) == 0 {
			f.fail("a notice, or no reasoning")
		}
		if proposals := proposalsOf(r.Events); len(proposals) == 0 || !isDraftV2(r, proposals[0]) {
			f.fail("no DRAFT_V2")
		}
		return f.list, nil
	}},
	{Name: "gemini signatures", Block: thinkingBlock, Legs: geminiLegs, Run: func(ctx context.Context, server *Server, endpoint Endpoint, adapter Adapter) ([]Finding, error) {
		r, err := thinkingLeg(ctx, server, endpoint, "on", "on", nil, adapter, nil)
		if err != nil {
			return nil, err
		}
		f := &findings{check: "gemini signatures"}
		signaturesHeld(f, r)
		var passages []*contract.RunEventReasoning
		for _, event := range reasoningOf(r.Events) {
			if event.Done {
				passages = append(passages, event)
			}
		}
		if len(passages) == 0 || passages[0].Text != thinkText("T1", "get_schema") {
			f.fail("the first passage did not arrive whole")
		}
		noErrors(f, r)
		return f.list, nil
	}},
	{Name: "gemini placements", Block: thinkingBlock, Legs: geminiLegs, Run: func(ctx context.Context, server *Server, endpoint Endpoint, adapter Adapter) ([]Finding, error) {
		f := &findings{check: "gemini placements"}
		for _, signatures := range []string{"thought", "call", "parallel"} {
			r, err := thinkingLeg(ctx, server, endpoint, "on", "on", nil, adapter, func(e *Endpoint, _ *LegOptions) { e.Signatures = signatures })
			if err != nil {
				return nil, err
			}
			held := &findings{check: "gemini placements"}
			signaturesHeld(held, r)
			for _, finding := range held.list {
				f.fail("%s: %s", signatures, finding.Detail)
			}
			if len(proposalsOf(r.Events)) != 1 {
				f.fail("%s: not one proposal", signatures)
			}
			if problem := callsInOrder(r.Events); problem != "" {
				f.fail("%s: %s", signatures, problem)
			}
			if errs := errorsOf(r.Events); len(errs) > 0 {
				f.fail("%s: %s", signatures, errs[0].Message)
			}
		}
		return f.list, nil
	}},
	{Name: "gemini empty", Block: thinkingBlock, Legs: geminiLegs, Run: func(ctx context.Context, server *Server, endpoint Endpoint, adapter Adapter) ([]Finding, error) {
		r, err := thinkingLeg(ctx, server, endpoint, "on", "on", nil, adapter, func(e *Endpoint, _ *LegOptions) { e.Signatures = "empty" })
		if err != nil {
			return nil, err
		}
		f := &findings{check: "gemini empty"}
		var replayed []RecordedRequest
		for _, request := range r.Observations.Requests {
			if len(request.ModelParts) > 0 {
				replayed = append(replayed, request)
			}
		}
		if len(replayed) == 0 {
			f.fail("no request replayed a model turn")
			return f.list, nil
		}
		first := modelTurns(replayed[0])[0]
		signature := thinkSignature("T1")
		want := []map[string]any{{"text": "", "thought": true, "thoughtSignature": signature}, {"text": "", "thought": true, "thoughtSignature": signature + ".2"}}
		if len(first) < 2 || comparisonJSON(first[:2]) != comparisonJSON(want) {
			f.fail("the first turn came back as %s", comparisonJSON(first))
		}
		for _, request := range replayed {
			if len(request.SignaturesMissing)+len(request.SignaturesTruncated)+len(request.SignaturesMalformed) > 0 {
				f.fail("%s: missing %v truncated %v malformed %v", request.Step, request.SignaturesMissing, request.SignaturesTruncated, request.SignaturesMalformed)
			}
		}
		if len(proposalsOf(r.Events)) != 1 {
			f.fail("not one proposal")
		}
		noErrors(f, r)
		for _, event := range reasoningOf(r.Events) {
			if event.Text != "" {
				f.fail("a passage was shown: %q", event.Text)
			}
		}
		return f.list, nil
	}},
	{Name: "always empty", Block: thinkingBlock, Legs: geminiLegs, Run: func(ctx context.Context, server *Server, endpoint Endpoint, adapter Adapter) ([]Finding, error) {
		r, err := thinkingLeg(ctx, server, endpoint, "always", "off", nil, adapter, func(e *Endpoint, _ *LegOptions) { e.Signatures = "empty" })
		if err != nil {
			return nil, err
		}
		f := &findings{check: "always empty"}
		alwaysChecks(f, r, wire.AlwaysFromAbsence)
		if len(proposalsOf(r.Events)) != 1 {
			f.fail("not one proposal")
		}
		noErrors(f, r)
		return f.list, nil
	}},
	{Name: "always", Block: thinkingBlock, Legs: geminiLegs, Run: func(ctx context.Context, server *Server, endpoint Endpoint, adapter Adapter) ([]Finding, error) {
		r, err := thinkingLeg(ctx, server, endpoint, "always", "off", nil, adapter, nil)
		if err != nil {
			return nil, err
		}
		f := &findings{check: "always"}
		alwaysChecks(f, r, wire.AlwaysFromAbsence)
		for _, request := range r.Observations.Requests {
			if request.ThinkingRequested || request.ThinkingParam != nil {
				f.fail("%s asked for thinking", request.Step)
			}
		}
		if len(reasoningOf(r.Events)) == 0 {
			f.fail("no reasoning reached the tab")
		}
		sent, _ := json.Marshal(r.Observations.Seen)
		if strings.Contains(string(sent), "[scripted reasoning") {
			f.fail("reasoning reached the runtime")
		}
		oneDraft(f, r)
		noErrors(f, r)
		return f.list, nil
	}},
	{Name: "always refused", Block: thinkingBlock, Legs: geminiLegs, Run: func(ctx context.Context, server *Server, endpoint Endpoint, adapter Adapter) ([]Finding, error) {
		r, err := thinkingLeg(ctx, server, endpoint, "no-off", "off", nil, adapter, nil)
		if err != nil {
			return nil, err
		}
		f := &findings{check: "always refused"}
		alwaysChecks(f, r, "400")
		withMember := 0
		for _, request := range r.Observations.Requests {
			if request.ThinkingParam != nil {
				f.fail("%s asked with %s", request.Step, request.ThinkingParam.Value)
			}
			if slices.Contains(request.BodyMembers, "generationConfig") {
				withMember++
			}
		}
		if withMember == 0 {
			f.fail("no request carried generationConfig")
		}
		oneDraft(f, r)
		noErrors(f, r)
		return f.list, nil
	}},
	{Name: "anthropic enabled", Block: thinkingBlock, Legs: []Endpoint{{API: "anthropic", AnswerAs: "stream"}}, Run: func(ctx context.Context, server *Server, endpoint Endpoint, adapter Adapter) ([]Finding, error) {
		r, err := thinkingLeg(ctx, server, endpoint, "enabled-only", "on", nil, adapter, nil)
		if err != nil {
			return nil, err
		}
		f := &findings{check: "anthropic enabled"}
		requests := r.Observations.Requests
		if len(requests) == 0 || requests[0].ThinkingParam == nil || !sameJSON(requests[0].ThinkingParam.Value, []byte(`{"type":"adaptive"}`)) {
			f.fail("the first request did not ask in the adaptive spelling")
		}
		var accepted []RecordedRequest
		for _, request := range requests {
			if paramType(request.ThinkingParam) == "enabled" {
				accepted = append(accepted, request)
			}
		}
		if len(accepted) <= 5 || !sameJSON(accepted[0].ThinkingParam.Value, []byte(`{"type":"enabled","budget_tokens":8000}`)) {
			f.fail("%d requests asked in the enabled spelling", len(accepted))
		}
		for tier, budget := range map[wire.ThinkingTier]int{wire.TierOn: 8000, wire.TierUltra: 16000} {
			held := wire.WireFor(tier, "anthropic-enabled")
			maximum := held.Members.Get("max_tokens")
			value, _ := maximum.Number()
			if int(value) != budget+wire.ResponseTokens {
				f.fail("%s: max_tokens %v", tier, value)
			}
		}
		if len(noticesOf(r.Events)) > 0 || len(reasoningOf(r.Events)) == 0 {
			f.fail("a notice, or no reasoning")
		}
		if proposals := proposalsOf(r.Events); len(proposals) == 0 || !isDraftV2(r, proposals[0]) {
			f.fail("no DRAFT_V2")
		}
		return f.list, nil
	}},
	{Name: "anthropic split", Block: thinkingBlock, Legs: []Endpoint{{API: "anthropic", AnswerAs: "stream"}}, Run: func(ctx context.Context, server *Server, endpoint Endpoint, adapter Adapter) ([]Finding, error) {
		r, err := thinkingLeg(ctx, server, endpoint, "split", "on", nil, adapter, nil)
		if err != nil {
			return nil, err
		}
		f := &findings{check: "anthropic split"}
		for _, request := range r.Observations.Requests {
			if len(request.SignaturesTruncated) > 0 || len(request.SignaturesMalformed) > 0 {
				f.fail("%s sent a fragment or an empty block", request.Step)
			}
			if request.ThinkingRequested && request.Results >= 1 && len(request.SignaturesMissing) > 0 {
				f.fail("%s asked for thinking without carrying back what it was signed", request.Step)
			}
		}
		if proposals := proposalsOf(r.Events); len(proposals) == 0 || !isDraftV2(r, proposals[0]) {
			f.fail("no DRAFT_V2")
		}
		ends := 0
		for _, kind := range eventTypes(r.Events) {
			if kind == "end" {
				ends++
			}
		}
		if ends != 1 {
			f.fail("%d ends", ends)
		}
		return f.list, nil
	}},
}

// fixtureCheck holds the vocabulary probe to the recorded runtime's own schema keywords, both ways
// (carries exactly the recorded runtime's own schema vocabulary): the five tools' schemas and the
// pack schema its get_schema answered, with that answer's byte count, digest, spec version and
// provenance beside it.
var fixtureCheck = Check{Name: "gemini fixture", Block: thinkingBlock, Legs: []Endpoint{{API: "gemini", AnswerAs: "stream"}}, Run: func(context.Context, *Server, Endpoint, Adapter) ([]Finding, error) {
	root, err := RepositoryRoot()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(filepath.Join(root, "packages", "harness", "certification", "runtime.json"))
	if err != nil {
		return nil, err
	}
	var recorded struct {
		Tools []struct {
			InputSchema json.RawMessage `json:"inputSchema"`
		} `json:"tools"`
		Calls []struct {
			Tool   string `json:"tool"`
			Result struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
				StructuredContent struct {
					SHA256      string `json:"sha256"`
					Bytes       int    `json:"bytes"`
					SpecVersion string `json:"specVersion"`
					Provenance  string `json:"provenance"`
				} `json:"structuredContent"`
			} `json:"result"`
		} `json:"calls"`
	}
	if err := json.Unmarshal(raw, &recorded); err != nil {
		return nil, err
	}
	f := &findings{check: "gemini fixture"}
	found := map[string]bool{}
	for _, tool := range recorded.Tools {
		for keyword := range keywordsOf(tool.InputSchema) {
			found[keyword] = true
		}
	}
	for _, call := range recorded.Calls {
		if call.Tool != "get_schema" || len(call.Result.Content) == 0 {
			continue
		}
		for keyword := range keywordsOf(json.RawMessage(call.Result.Content[0].Text)) {
			found[keyword] = true
		}
		held := call.Result.StructuredContent
		if len(held.SHA256) != 64 || held.Bytes != utf16Units(call.Result.Content[0].Text) || held.SpecVersion == "" || held.Provenance == "" {
			f.fail("the recorded pack schema's provenance does not hold")
		}
	}
	probe := keywordsOf(vocabulary().InputSchema)
	for keyword := range found {
		if !probe[keyword] {
			f.fail("the runtime emits %s and the probe does not carry it", keyword)
		}
	}
	for keyword := range probe {
		if !found[keyword] {
			f.fail("the probe carries %s, which the runtime does not emit", keyword)
		}
	}
	return f.list, nil
}}

func utf16Units(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return n
}

func init() { WireChecks = append(WireChecks, fixtureCheck) }

// callsInOrder is where a turn's calls ran at once rather than one at a time in the model's order:
// each call's result follows it before the next call starts, so each guardrail stays between its own
// call and result (ADR-0004: ExecuteSequentially).
func callsInOrder(events []contract.RunEvent) string {
	open := ""
	for _, event := range events {
		switch {
		case event.ToolCall != nil:
			if open != "" {
				return "a call to " + event.ToolCall.Name + " started before " + open + " answered"
			}
			open = event.ToolCall.Name
		case event.ToolResult != nil:
			if open != event.ToolResult.Name {
				return "a result of " + event.ToolResult.Name + " came while " + open + " was the call in flight"
			}
			open = ""
		}
	}
	return ""
}
