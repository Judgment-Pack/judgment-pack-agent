package certification

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

// The legs beside the engine leg: the text tool path, the strict turn order, and the thinking,
// refutation and wire legs (the describe.each(MODEL_ENGINES) blocks of
// certification/conformance.test.ts after `engine %s`), each check a Go reading of the TypeScript
// one by its own name. parity.json maps each TypeScript check to the Go check that makes it.

// Check is one check of a leg block: its Go name, the TypeScript block it reads, the endpoints it
// runs on, and the run itself, which answers what did not hold.
type Check struct {
	Name  string
	Block string
	Legs  []Endpoint
	Run   func(ctx context.Context, server *Server, endpoint Endpoint, adapter Adapter) ([]Finding, error)
}

// AllLegs are the six engine legs: three wires, each answered as a stream and whole.
var AllLegs = []Endpoint{
	{API: "openai-compatible", AnswerAs: "stream"}, {API: "openai-compatible", AnswerAs: "whole"},
	{API: "anthropic", AnswerAs: "stream"}, {API: "anthropic", AnswerAs: "whole"},
	{API: "gemini", AnswerAs: "stream"}, {API: "gemini", AnswerAs: "whole"},
}

// streamLegs are the strict alternation block's three: each wire, streamed.
var streamLegs = []Endpoint{{API: "openai-compatible", AnswerAs: "stream"}, {API: "anthropic", AnswerAs: "stream"}, {API: "gemini", AnswerAs: "stream"}}

var geminiLegs = []Endpoint{{API: "gemini", AnswerAs: "stream"}, {API: "gemini", AnswerAs: "whole"}}

// findings collects one check's failures.
type findings struct {
	check string
	list  []Finding
}

func (f *findings) fail(format string, args ...any) {
	f.list = append(f.list, Finding{f.check, fmt.Sprintf(format, args...)})
}

func ofType[T any](events []contract.RunEvent, pick func(contract.RunEvent) *T) []*T {
	var out []*T
	for _, event := range events {
		if held := pick(event); held != nil {
			out = append(out, held)
		}
	}
	return out
}

func errorsOf(events []contract.RunEvent) []*contract.RunEventError {
	return ofType(events, func(e contract.RunEvent) *contract.RunEventError { return e.Error })
}
func proposalsOf(events []contract.RunEvent) []*contract.RunEventProposal {
	return ofType(events, func(e contract.RunEvent) *contract.RunEventProposal { return e.Proposal })
}
func critiquesOf(events []contract.RunEvent) []*contract.RunEventCritique {
	return ofType(events, func(e contract.RunEvent) *contract.RunEventCritique { return e.Critique })
}
func reasoningOf(events []contract.RunEvent) []*contract.RunEventReasoning {
	return ofType(events, func(e contract.RunEvent) *contract.RunEventReasoning { return e.Reasoning })
}
func noticesOf(events []contract.RunEvent) []*contract.RunEventThinkingUnavailable {
	return ofType(events, func(e contract.RunEvent) *contract.RunEventThinkingUnavailable { return e.ThinkingUnavailable })
}
func guardrailsOf(events []contract.RunEvent, action string) []*contract.RunEventGuardrail {
	return ofType(events, func(e contract.RunEvent) *contract.RunEventGuardrail {
		if e.Guardrail != nil && (action == "" || e.Guardrail.Action == action) {
			return e.Guardrail
		}
		return nil
	})
}

func callNames(events []contract.RunEvent) []string {
	var out []string
	for _, event := range events {
		if event.ToolCall != nil {
			out = append(out, event.ToolCall.Name)
		}
	}
	return out
}

func seenNames(seen []ServerObservation) []string {
	out := []string{}
	for _, call := range seen {
		out = append(out, call.Name)
	}
	return out
}

func refusals(seen []ServerObservation) []string {
	var out []string
	for _, call := range seen {
		if call.Refusal != "" {
			out = append(out, call.Name+": "+call.Refusal)
		}
	}
	return out
}

func eventTypes(events []contract.RunEvent) []string {
	out := make([]string, len(events))
	for i, event := range events {
		out[i] = EventType(event)
	}
	return out
}

func steps(requests []RecordedRequest) ([]string, []int) {
	var names []string
	var results []int
	for _, request := range requests {
		names = append(names, request.Step)
		results = append(results, request.Results)
	}
	return names, results
}

// recallMisses is the recall rule (agent#11) on every request a leg sent, the critic's second
// conversation aside: what it finds where it does not find the text the run started from.
func recallMisses(api string, bodies [][]byte, policy string) []string {
	var misses []string
	at := 0
	for _, body := range bodies {
		if strings.Contains(string(body), RefutationMarker) {
			continue
		}
		at++
		found := runcontext.Recall(api, body)
		if found == nil || found.Text != policy {
			misses = append(misses, fmt.Sprintf("request %d: %v", at, found))
		}
	}
	return misses
}

func authoringBodies(bodies [][]byte) int {
	n := 0
	for _, body := range bodies {
		if !strings.Contains(string(body), RefutationMarker) {
			n++
		}
	}
	return n
}

// thinkText and thinkSignature are the scripted endpoint's reasoning and signature for a step,
// recomputed here rather than trusted from the endpoint (scriptedModel.ts).
func thinkText(id, tool string) string {
	if tool == "" {
		tool = "no tool"
	}
	return "[scripted reasoning " + id + "] The policy has three branches. Next I will call " + tool +
		" to make progress; I must not state a verdict of my own."
}

func thinkSignature(id string) string {
	return base64.StdEncoding.EncodeToString([]byte("scripted-thinking-signature:" + id))
}

func isDraftV2(r *LegResult, proposal *contract.RunEventProposal) bool {
	document, _ := json.Marshal(proposal.Document)
	return sameJSON(document, r.scenario.Documents["DRAFT_V2"])
}

func boolPtr(b bool) *bool { return &b }

// run is one leg with its options, its checks collected under the check's name.
func run(ctx context.Context, server *Server, endpoint Endpoint, options LegOptions, adapter Adapter) (*LegResult, error) {
	return scenarioLeg(ctx, server, endpoint, adapter, "", options)
}

var textCapabilities = &contract.CapabilityEndpointCapabilities{NativeToolCalls: boolPtr(false)}

// TextChecks are the text tool calls block (agent#4): an endpoint whose capability record says it
// takes no native tool calls.
var TextChecks = []Check{
	{Name: "text", Block: "engine %s · text tool calls", Legs: AllLegs, Run: func(ctx context.Context, server *Server, endpoint Endpoint, adapter Adapter) ([]Finding, error) {
		endpoint.ToolCalls = "text"
		r, err := run(ctx, server, endpoint, LegOptions{Capabilities: textCapabilities}, adapter)
		if err != nil {
			return nil, err
		}
		f := &findings{check: "text"}
		if len(r.reached) > 0 {
			f.fail("reached for the default transport: %v", r.reached)
		}
		if errs := errorsOf(r.Events); len(errs) > 0 {
			f.fail("error events: %v", errs[0].Message)
		}
		names, results := steps(r.Observations.Requests)
		if !slices.Equal(names, scenarioSteps) || !slices.Equal(results, []int{0, 1, 2, 3, 4, 5, 6, 7}) {
			f.fail("the steps were %v with %v results", names, results)
		}
		for _, request := range r.Observations.Requests {
			if len(request.ToolNames) > 0 || slices.Contains(request.BodyMembers, "tools") {
				f.fail("request %s carried tools", request.Step)
			}
		}
		if !slices.Equal(callNames(r.Events), scenarioCalls) {
			f.fail("the calls were %v", callNames(r.Events))
		}
		if !slices.Equal(seenNames(r.Observations.Seen), scenarioReached) || len(refusals(r.Observations.Seen)) > 0 {
			f.fail("the runtime saw %v, refusals %v", seenNames(r.Observations.Seen), refusals(r.Observations.Seen))
		}
		for _, call := range r.Observations.Seen {
			if call.Name == "experimental_evaluate" && call.Args["rehearsal"] != true {
				f.fail("the evaluate reached the runtime without rehearsal")
			}
		}
		var rewrote, refused []string
		for _, g := range guardrailsOf(r.Events, "rewrote") {
			rewrote = append(rewrote, g.Tool)
		}
		for _, g := range guardrailsOf(r.Events, "refused") {
			refused = append(refused, g.Tool)
		}
		if !slices.Equal(rewrote, []string{"experimental_evaluate"}) || !slices.Equal(refused, []string{"write_file"}) {
			f.fail("guardrails rewrote %v refused %v", rewrote, refused)
		}
		told := false
		for _, event := range r.Events {
			if event.ToolResult != nil && event.ToolResult.Name == "write_file" {
				told = event.ToolResult.IsError && strings.Contains(event.ToolResult.Text, "never calls a tool it was not offered")
			}
		}
		if !told {
			f.fail("the refused write_file was not told as a failed result")
		}
		proposals := proposalsOf(r.Events)
		if len(proposals) != 1 || !isDraftV2(r, proposals[0]) || !slices.Equal(proposals[0].Unknowns, r.scenario.Unknowns) {
			f.fail("%d proposals, not DRAFT_V2 with the unknowns", len(proposals))
		}
		if !slices.Equal(eventTypes(r.Events), r.sequence) {
			f.fail("the events were %v", eventTypes(r.Events))
		}
		return f.list, nil
	}},
	{Name: "text agent#11", Block: "engine %s · text tool calls", Legs: AllLegs, Run: func(ctx context.Context, server *Server, endpoint Endpoint, adapter Adapter) ([]Finding, error) {
		endpoint.ToolCalls = "text"
		r, err := run(ctx, server, endpoint, LegOptions{Capabilities: textCapabilities}, adapter)
		if err != nil {
			return nil, err
		}
		f := &findings{check: "text agent#11"}
		if len(r.bodies) <= 7 {
			f.fail("%d bodies", len(r.bodies))
		}
		for _, miss := range recallMisses(endpoint.API, r.bodies, r.scenario.Policy) {
			f.fail("%s", miss)
		}
		return f.list, nil
	}},
	{Name: "text strict", Block: "engine %s · text tool calls", Legs: AllLegs, Run: func(ctx context.Context, server *Server, endpoint Endpoint, adapter Adapter) ([]Finding, error) {
		endpoint.ToolCalls, endpoint.Strict = "text", true
		r, err := run(ctx, server, endpoint, LegOptions{Capabilities: &contract.CapabilityEndpointCapabilities{NativeToolCalls: boolPtr(false), StrictAlternation: boolPtr(true)}}, adapter)
		if err != nil {
			return nil, err
		}
		f := &findings{check: "text strict"}
		if errs := errorsOf(r.Events); len(errs) > 0 {
			f.fail("error events: %v", errs[0].Message)
		}
		names, _ := steps(r.Observations.Requests)
		if !slices.Equal(names, scenarioSteps) {
			f.fail("the steps were %v", names)
		}
		if proposals := proposalsOf(r.Events); len(proposals) == 0 || !isDraftV2(r, proposals[0]) {
			f.fail("no DRAFT_V2 proposal")
		}
		return f.list, nil
	}},
}

var strictCapabilities = &contract.CapabilityEndpointCapabilities{StrictAlternation: boolPtr(true)}

// StrictChecks are the strict alternation block (agent#9): the first request carries the state in
// the person's own message and every later one in the system tail.
var StrictChecks = []Check{
	{Name: "strict", Block: "engine %s · strict alternation", Legs: streamLegs, Run: func(ctx context.Context, server *Server, endpoint Endpoint, adapter Adapter) ([]Finding, error) {
		endpoint.Strict = true
		r, err := run(ctx, server, endpoint, LegOptions{Capabilities: strictCapabilities}, adapter)
		if err != nil {
			return nil, err
		}
		f := &findings{check: "strict"}
		if errs := errorsOf(r.Events); len(errs) > 0 {
			f.fail("error events: %v", errs[0].Message)
		}
		if !slices.Contains(seenNames(r.Observations.Seen), "experimental_evaluate") {
			f.fail("the evaluate never reached the runtime")
		}
		if proposals := proposalsOf(r.Events); len(proposals) != 1 || !isDraftV2(r, proposals[0]) {
			f.fail("not one DRAFT_V2 proposal")
		}
		if len(r.Observations.Requests) <= 7 {
			f.fail("%d requests", len(r.Observations.Requests))
		}
		return f.list, nil
	}},
	{Name: "strict agent#11", Block: "engine %s · strict alternation", Legs: streamLegs, Run: func(ctx context.Context, server *Server, endpoint Endpoint, adapter Adapter) ([]Finding, error) {
		endpoint.Strict = true
		r, err := run(ctx, server, endpoint, LegOptions{Capabilities: strictCapabilities}, adapter)
		if err != nil {
			return nil, err
		}
		f := &findings{check: "strict agent#11"}
		if len(r.bodies) <= 7 {
			f.fail("%d bodies", len(r.bodies))
		}
		for _, miss := range recallMisses(endpoint.API, r.bodies, r.scenario.Policy) {
			f.fail("%s", miss)
		}
		return f.list, nil
	}},
	{Name: "strict refused", Block: "engine %s · strict alternation", Legs: streamLegs, Run: func(ctx context.Context, server *Server, endpoint Endpoint, adapter Adapter) ([]Finding, error) {
		endpoint.Strict = true
		trailing := "trailing-user"
		r, err := run(ctx, server, endpoint, LegOptions{Capabilities: &contract.CapabilityEndpointCapabilities{StatePlacement: &trailing}}, adapter)
		if err != nil {
			return nil, err
		}
		f := &findings{check: "strict refused"}
		errs := errorsOf(r.Events)
		if len(errs) != 1 || errs[0].Failure == nil || errs[0].Failure.Status == nil || *errs[0].Failure.Status != 400 {
			f.fail("the failures were %v, not one 400", errs)
		}
		if len(proposalsOf(r.Events)) != 0 {
			f.fail("a proposal")
		}
		return f.list, nil
	}},
}
