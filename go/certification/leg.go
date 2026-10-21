package certification

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
)

// Run is what the runner hands an adapter for one leg (adapter.Run): capabilities, never an
// address or a credential. Gate carries the gated call, never the runtime's transport (ADR-0004, as
// amended).
type Run = adapter.Run

// Adapter is one Go adapter, as the runner drives it.
type Adapter = adapter.Adapter

// AdapterFunc lets a function be an Adapter.
type AdapterFunc = adapter.Func

// LegResult is what one leg produced and what the shared server saw.
type LegResult struct {
	Endpoint     Endpoint
	Events       []contract.RunEvent
	Observations *Observations
	RunError     error
	scenario     *Scenario
	offer        contract.ToolOffer
	sequence     []string
	reached      []string
	bodies       [][]byte
	traced       string
	// asked is each query the host's MemoryFor was asked with (ADR-0005).
	asked []string
}

// sealed is http.DefaultTransport while a leg runs: an adapter's only reach to a model is the
// relay capability and to the runtime the transport it is handed, so a request through the
// process's default transport is recorded and refused (the TypeScript legs' K1a, for Go).
type sealed struct {
	mu      sync.Mutex
	reached []string
}

func (s *sealed) RoundTrip(request *http.Request) (*http.Response, error) {
	s.mu.Lock()
	s.reached = append(s.reached, request.Method+" "+request.URL.Redacted())
	s.mu.Unlock()
	return nil, fmt.Errorf("an adapter reached for http.DefaultTransport (%s); its only reach to a model is the relay capability", request.URL.Redacted())
}

type seal struct {
	before http.RoundTripper
	s      *sealed
}

func sealNetwork() *seal {
	s := &seal{before: http.DefaultTransport, s: &sealed{}}
	http.DefaultTransport = s.s
	return s
}

func (s *seal) lift() []string {
	http.DefaultTransport = s.before
	s.s.mu.Lock()
	defer s.s.mu.Unlock()
	return append([]string(nil), s.s.reached...)
}

// Finding is one check that did not hold.
type Finding struct {
	Check  string
	Detail string
}

func (f Finding) String() string { return f.Check + ": " + f.Detail }

// ReadVector reads one vector file under contract/vectors.
func ReadVector(name string) (schema string, items []json.RawMessage, err error) {
	root, err := RepositoryRoot()
	if err != nil {
		return "", nil, err
	}
	raw, err := os.ReadFile(filepath.Join(root, "contract", "vectors", name))
	if err != nil {
		return "", nil, err
	}
	var vector struct {
		Schema string            `json:"schema"`
		Items  []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(raw, &vector); err != nil {
		return "", nil, err
	}
	return vector.Schema, vector.Items, nil
}

// ScenarioLeg runs the recorded scenario against an adapter on one endpoint: the leg the
// TypeScript certification runs as `engine <id>` (certification/conformance.test.ts).
func ScenarioLeg(ctx context.Context, server *Server, endpoint Endpoint, adapter Adapter) (*LegResult, error) {
	return scenarioLeg(ctx, server, endpoint, adapter, "", LegOptions{})
}

// TestPrompt is the stand-in for the runtime's test_pack prompt the TypeScript legs hand over
// (TEST_PROMPT in conformance.test.ts).
const TestPrompt = "THE RUNTIME’S TEST_PACK GUIDANCE, AS PROMPTS/GET SERVED IT"

// LegOptions are what a leg asks of the host beside the endpoint (the TypeScript runLeg's `how`):
// the tier the desk's file asked for, the endpoint's capability record as the host knows it,
// whether the session asks for the critic and the cases it declares, the tools the desk's file
// granted, one tool set in place of the runtime's five (a schema probe), and the host's parts of
// the prompt (ADR-0005), the memory answered with the number of the call that asked for it, so
// memory asked more than once is not the same text twice.
type LegOptions struct {
	Tier         string
	Capabilities *contract.CapabilityEndpointCapabilities
	Review       *bool
	Cases        []json.RawMessage
	Allowed      []string
	Tools        []contract.ToolOfferRuntimeTool
	Host         *runcontext.HostParts
}

// Leg runs the recorded scenario against an adapter on one endpoint with the host's options.
func Leg(ctx context.Context, server *Server, endpoint Endpoint, options LegOptions, adapter Adapter) (*LegResult, error) {
	return scenarioLeg(ctx, server, endpoint, adapter, "", options)
}

// TracingKey is the key the traced leg puts in the environment's tracing variables.
const TracingKey = "lsv2-a-key-this-leg-was-given"

// TracingEnvironment is what the traced leg sets (K1b): LangSmith's and LangChain's tracing
// variables, a key and an endpoint, as a host's environment may carry them.
var TracingEnvironment = map[string]string{
	"LANGSMITH_TRACING":    "true",
	"LANGSMITH_TRACING_V2": "true",
	"LANGCHAIN_TRACING_V2": "true",
	"LANGCHAIN_TRACING":    "true",
	"LANGSMITH_API_KEY":    TracingKey,
	"LANGCHAIN_API_KEY":    TracingKey,
	"LANGSMITH_ENDPOINT":   "http://127.0.0.1:9",
	"LANGCHAIN_ENDPOINT":   "http://127.0.0.1:9",
}

// ScenarioLegTraced runs the same leg with TracingEnvironment set in the process's environment
// for its length (the TypeScript leg's K1b): the adapter must reach nothing but the relay and
// send the key nowhere. It changes the process's environment, so it is not run beside another leg.
func ScenarioLegTraced(ctx context.Context, server *Server, endpoint Endpoint, adapter Adapter) (*LegResult, error) {
	type held struct {
		value string
		set   bool
	}
	before := map[string]held{}
	for name, value := range TracingEnvironment {
		previous, set := os.LookupEnv(name)
		before[name] = held{previous, set}
		if err := os.Setenv(name, value); err != nil {
			return nil, err
		}
	}
	defer func() {
		for name, was := range before {
			if was.set {
				_ = os.Setenv(name, was.value)
			} else {
				_ = os.Unsetenv(name)
			}
		}
	}()
	return scenarioLeg(ctx, server, endpoint, adapter, TracingKey, LegOptions{})
}

func scenarioLeg(ctx context.Context, server *Server, endpoint Endpoint, adapter Adapter, traced string, options LegOptions) (*LegResult, error) {
	scenario, err := server.Scenario(ctx)
	if err != nil {
		return nil, err
	}
	_, offers, err := ReadVector("tool-offer/scenario-offer.json")
	if err != nil {
		return nil, err
	}
	result := &LegResult{Endpoint: endpoint, scenario: scenario, traced: traced, sequence: []string{"policy_resolved"}}
	if err := json.Unmarshal(offers[0], &result.offer); err != nil {
		return nil, fmt.Errorf("the scenario's offer vector: %w", err)
	}
	_, items, err := ReadVector("scenario/expense-reimbursement.json")
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		var event struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(item, &event); err != nil {
			return nil, err
		}
		result.sequence = append(result.sequence, event.Type)
	}
	session, err := server.NewSession(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	var mu sync.Mutex
	emit := func(event contract.RunEvent) {
		mu.Lock()
		defer mu.Unlock()
		result.Events = append(result.Events, event)
	}
	// The host's side: the scenario's session grants the five, and the runtime is reached only
	// through the gate, the handshake included.
	granted := scenario.ScenarioTools
	if options.Allowed != nil {
		granted = options.Allowed
	}
	gated := gate.New(gate.Options{Runtime: gate.Call(session.RuntimeCall()), Granted: granted, Emit: emit})
	offer, err := gated.Open(ctx, result.offer.HostTools)
	if err != nil {
		return nil, fmt.Errorf("the handshake through the gate: %w", err)
	}
	// The boundary adds its own context tools to the run (the offer vector's host tools); the
	// session hands it none of its own.
	run := &Run{
		Policy:     runcontext.PolicyLayer{"outputGuard": "off"}, // Legacy wire streaming; scenario 17 covers guarded delivery.
		Wire:       endpoint.API,
		ModelName:  "scripted-model",
		Model:      session.ModelCall(),
		Gate:       gated.Calls(),
		Tools:      offer.Tools,
		Prompt:     scenario.Policy,
		TestPrompt: TestPrompt,
		Thinking:   "off",
		Emit:       emit,
	}
	if options.Tier != "" {
		run.Thinking = options.Tier
	}
	run.Capabilities = options.Capabilities
	run.Review = options.Review
	run.Cases = options.Cases
	if options.Tools != nil {
		run.Tools = options.Tools
	}
	if parts := options.Host; parts != nil {
		var askedMu sync.Mutex
		run.Host = runcontext.HostFuncs{
			BriefFunc:               func(context.Context) (string, error) { return parts.Brief, nil },
			ProjectInstructionsFunc: func(context.Context) (string, error) { return parts.ProjectInstructions, nil },
			MemoryForFunc: func(_ context.Context, query string) (string, error) {
				askedMu.Lock()
				defer askedMu.Unlock()
				result.asked = append(result.asked, query)
				if parts.Memory == "" {
					return "", nil
				}
				return parts.Memory + " (recall " + strconv.Itoa(len(result.asked)) + ")", nil
			},
		}
	}
	seal := sealNetwork()
	result.RunError = adapter.Run(ctx, run)
	result.reached = seal.lift()
	result.bodies = session.Bodies()
	result.Observations, err = session.Observations(ctx)
	if err != nil {
		return nil, err
	}
	return result, nil
}

// EventType names a run event's kind.
func EventType(event contract.RunEvent) string {
	encoded, err := json.Marshal(event)
	if err != nil {
		return "unencodable"
	}
	var head struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(encoded, &head)
	return head.Type
}

// firstSuffix is the path each wire's first request lands on after the relay's mount.
var firstSuffix = map[string]string{
	"openai-compatible": "/chat/completions",
	"anthropic":         "/v1/messages",
	"gemini":            "/v1beta/models/scripted-model:streamGenerateContent",
}

// WireMembers are the members each wire format defines, which every adapter must send
// (WIRE_MEMBERS in certification/conformance.test.ts): what the format itself defines, not the
// union of what the adapters send.
var WireMembers = map[string][]string{
	"openai-compatible": {"messages", "model", "stream", "tools"},
	"anthropic":         {"max_tokens", "messages", "model", "stream", "system", "tools"},
	"gemini":            {"contents", "generationConfig", "systemInstruction", "tools"},
}

// The names the scenario's calls are made by and the names the runtime is reached by, in order.
var (
	scenarioCalls   = []string{"get_schema", "list_examples", "get_example", "validate", "validate", "experimental_evaluate", "write_file"}
	scenarioReached = []string{"get_schema", "list_examples", "get_example", "validate", "validate", "experimental_evaluate"}
	scenarioSteps   = []string{"T1", "T2", "T3", "T4", "T5", "T6", "T7", "T8"}
)

// CheckNames are the checks a leg reports by name, in the order Checks makes them; parity.json
// maps each TypeScript check of the `engine <id>` leg to these.
var CheckNames = []string{"run", "scenario", "K1a", "K1", "K1b", "K2", "K3a", "K3b", "K3c", "sequence", "wire", "steps"}

// Checks holds a leg to the checks of the TypeScript `engine <id>` leg that read the events, the
// request bodies and the server's records (certification/parity.json says which is which): the
// scenario's calls, reaches and answers; K1a (as
// http.DefaultTransport), K1, K1b (on the traced leg), K2, K3a, K3b, K3c; the event sequence the
// contract's scenario vector states, with no error event and one end; the wire's body members;
// and the eight steps with no repeat. It returns the checks that did not hold, each by its name.
func (r *LegResult) Checks() []Finding {
	var findings []Finding
	fail := func(check, format string, args ...any) {
		findings = append(findings, Finding{check, fmt.Sprintf(format, args...)})
	}
	if r.RunError != nil {
		fail("run", "the adapter's run failed: %v", r.RunError)
	}
	requests := r.Observations.Requests
	seen := r.Observations.Seen
	types := make([]string, len(r.Events))
	for i, event := range r.Events {
		types[i] = EventType(event)
	}
	var proposals []*contract.RunEventProposal
	var errorEvents []string
	var results []*contract.ToolResult
	var calls []string
	for _, event := range r.Events {
		switch {
		case event.Proposal != nil:
			proposals = append(proposals, event.Proposal)
		case event.Error != nil:
			errorEvents = append(errorEvents, event.Error.Message)
		case event.ToolResult != nil:
			results = append(results, event.ToolResult)
		case event.ToolCall != nil:
			calls = append(calls, event.ToolCall.Name)
		}
	}

	// The whole scenario: every step's call in order, the two it provokes kept from the runtime,
	// and the runtime's own answers back in the loop (T4 invalid with its diagnostic, T5 valid,
	// T6 a rehearsal with its disposition).
	if !slices.Equal(calls, scenarioCalls) {
		fail("scenario", "the calls were %v, not %v", calls, scenarioCalls)
	}
	var reachedNames []string
	for _, call := range seen {
		reachedNames = append(reachedNames, call.Name)
		if call.Refusal != "" {
			fail("scenario", "the runtime refused %s: %s", call.Name, call.Refusal)
		}
	}
	if !slices.Equal(reachedNames, scenarioReached) {
		fail("scenario", "the runtime was reached by %v, not %v", reachedNames, scenarioReached)
	}
	expect := r.scenario.Expectations
	var validates []map[string]any
	var evaluated map[string]any
	for _, result := range results {
		var answer map[string]any
		if json.Unmarshal([]byte(result.Text), &answer) != nil {
			continue
		}
		switch result.Name {
		case "validate":
			validates = append(validates, answer)
		case "experimental_evaluate":
			evaluated = answer
		}
	}
	diagnostic := func(answer map[string]any) any {
		list, _ := answer["diagnostics"].([]any)
		if len(list) == 0 {
			return nil
		}
		first, _ := list[0].(map[string]any)
		return first["code"]
	}
	if len(validates) != 2 || validates[0]["status"] != expect.T4.Status || diagnostic(validates[0]) != expect.T4.DiagnosticCode || validates[1]["status"] != expect.T5.Status {
		fail("scenario", "the two validate results are not T4 %s (%s) then T5 %s", expect.T4.Status, expect.T4.DiagnosticCode, expect.T5.Status)
	}
	disposition, _ := evaluated["disposition"].(map[string]any)
	if evaluated == nil || evaluated["rehearsal"] != expect.T6.Rehearsal || disposition["kind"] != expect.T6.Disposition.Kind || disposition["outcomeId"] != expect.T6.Disposition.OutcomeID {
		fail("scenario", "the evaluate result is not T6's rehearsal with %s %s", expect.T6.Disposition.Kind, expect.T6.Disposition.OutcomeID)
	}

	// K1a: nothing reached for the process's default transport; no error event, and a proposal.
	for _, reach := range r.reached {
		fail("K1a", "the adapter reached for http.DefaultTransport: %s", reach)
	}
	if len(errorEvents) > 0 {
		fail("K1a", "error events: %v", errorEvents)
	}
	if len(proposals) == 0 {
		fail("K1a", "no proposal")
	}

	// K1: no credential, and nothing called but the relay.
	if len(requests) == 0 {
		fail("K1", "no model request reached the relay")
	}
	for i, request := range requests {
		for _, forbidden := range []string{"x-api-key", "api-key", "cookie", "proxy-authorization", "x-goog-api-key"} {
			if slices.Contains(request.HeaderNames, forbidden) {
				fail("K1", "request %d carried %s", i, forbidden)
			}
		}
		count := 0
		for _, name := range request.HeaderNames {
			if name == "authorization" {
				count++
			}
		}
		if count != 1 {
			fail("K1", "request %d carried the session %d times, not once", i, count)
		}
		parsed, err := url.Parse(request.URL)
		if err != nil || !strings.HasPrefix(request.URL, RelayPrefix+"/") {
			fail("K1", "request %d went to %q, not the relay", i, request.URL)
			continue
		}
		var keys []string
		for key := range parsed.Query() {
			keys = append(keys, key)
		}
		admitted := []string{}
		if r.Endpoint.API == "gemini" && request.StreamRequested {
			admitted = []string{"alt"}
			if parsed.Query().Get("alt") != "sse" {
				fail("K1", "request %d asked for its stream as %q", i, parsed.Query().Get("alt"))
			}
		}
		if !reflect.DeepEqual(append([]string{}, keys...), admitted) {
			fail("K1", "request %d carried the query %v", i, keys)
		}
	}
	if len(requests) > 0 {
		if parsed, err := url.Parse(requests[0].URL); err != nil || parsed.Path != RelayPrefix+firstSuffix[r.Endpoint.API] {
			fail("K1", "the first request went to %q", requests[0].URL)
		}
	}

	// K1b, on the traced leg: the environment's tracing variables reach nothing. Nothing but the
	// relay, the key in no body, no error event, one proposal, and the run ends.
	if r.traced != "" {
		for i, request := range requests {
			if !strings.HasPrefix(request.URL, RelayPrefix+"/") {
				fail("K1b", "request %d went to %q", i, request.URL)
			}
		}
		for i, body := range r.bodies {
			if strings.Contains(string(body), r.traced) {
				fail("K1b", "request %d carried the tracing key", i+1)
			}
		}
		if len(r.reached) > 0 || len(errorEvents) > 0 || len(proposals) != 1 || len(types) == 0 || types[len(types)-1] != "end" {
			fail("K1b", "under the tracing variables: %d reaches, errors %v, %d proposals, last event %v", len(r.reached), errorEvents, len(proposals), types)
		}
	}

	// K2: the offer is the host tools then the five, taken from tools/list.
	var offered []string
	for _, tool := range r.offer.HostTools {
		offered = append(offered, tool.Name)
	}
	for _, tool := range r.offer.Tools {
		offered = append(offered, tool.Name)
	}
	for i, request := range requests {
		if !reflect.DeepEqual(request.ToolNames, offered) {
			fail("K2", "request %d offered %v, not %v", i, request.ToolNames, offered)
		}
	}

	// K3a: the evaluate reaches the runtime with rehearsal: true, reported between call and result.
	evaluatedAtRuntime := false
	for _, call := range seen {
		if call.Name == "experimental_evaluate" {
			evaluatedAtRuntime = true
			if call.Args["rehearsal"] != true {
				fail("K3a", "experimental_evaluate reached the runtime without rehearsal: true")
			}
		}
	}
	if !evaluatedAtRuntime {
		fail("K3a", "experimental_evaluate never reached the runtime")
	}
	guard := func(check, tool, action, detail string) {
		matched := 0
		for i, event := range r.Events {
			if event.Guardrail == nil || event.Guardrail.Tool != tool || event.Guardrail.Action != action {
				continue
			}
			matched++
			if !strings.Contains(event.Guardrail.Detail, detail) {
				fail(check, "the %s guardrail for %s says %q", action, tool, event.Guardrail.Detail)
			}
			if i == 0 || i+1 >= len(types) || types[i-1] != "tool_call" || types[i+1] != "tool_result" {
				fail(check, "the %s guardrail for %s is not between its call and its result", action, tool)
			}
		}
		if matched != 1 {
			fail(check, "%d %s guardrails for %s, not one", matched, action, tool)
		}
	}
	guard("K3a", "experimental_evaluate", "rewrote", "no rehearsal member")

	// K3b: write_file never reaches the runtime, and the model is told.
	for _, call := range seen {
		if call.Name == "write_file" {
			fail("K3b", "write_file reached the runtime")
		}
	}
	guard("K3b", "write_file", "refused", "write_file")
	told := false
	for _, result := range results {
		if result.Name == "write_file" && result.IsError {
			told = true
		}
	}
	if !told {
		fail("K3b", "no failed tool_result for write_file")
	}

	// K3c: one proposal, DRAFT_V2 member for member, and the unknowns whole.
	if len(proposals) != 1 {
		fail("K3c", "%d proposals, not one", len(proposals))
	} else {
		document, _ := json.Marshal(proposals[0].Document)
		if !sameJSON(document, r.scenario.Documents["DRAFT_V2"]) {
			fail("K3c", "the proposal's document is not DRAFT_V2")
		}
		if !reflect.DeepEqual(proposals[0].Unknowns, r.scenario.Unknowns) {
			fail("K3c", "the proposal's unknowns are not the scenario's")
		}
	}

	// The sequence the scenario vector states, ending once, with no error event, nothing
	// narrowed and nothing degraded (the tier is off).
	// ADR-0020's scenario checks the terminal usage event directly on both production adapters.
	// This older scenario vector and its reference adapter predate that event, so compare their
	// original stream after removing only the independently certified ledger event.
	legacyTypes := slices.DeleteFunc(slices.Clone(types), func(kind string) bool { return kind == "usage" })
	if !reflect.DeepEqual(legacyTypes, r.sequence) {
		fail("sequence", "the events were %v, not %v", legacyTypes, r.sequence)
	}
	ends := 0
	for i, kind := range types {
		switch kind {
		case "end":
			ends++
		case "error", "thinking_unavailable":
			fail("sequence", "a %s event", kind)
		case "guardrail":
			if r.Events[i].Guardrail.Action == "narrowed" {
				fail("sequence", "a narrowed guardrail for %s", r.Events[i].Guardrail.Tool)
			}
		}
	}
	if ends != 1 {
		fail("sequence", "%d end events, not one", ends)
	}

	// The wire's own members, on every request.
	for i, request := range requests {
		for _, member := range WireMembers[r.Endpoint.API] {
			if !slices.Contains(request.BodyMembers, member) {
				fail("wire", "request %d (%s) sent {%s}, without %s", i, request.Step, strings.Join(request.BodyMembers, ", "), member)
			}
		}
	}

	// Every result carried back, so the script never repeats a step: T1 to T8, results 0 to 7.
	var steps []string
	var carried []int
	for _, request := range requests {
		steps = append(steps, request.Step)
		carried = append(carried, request.Results)
	}
	if !slices.Equal(steps, scenarioSteps) || !slices.Equal(carried, []int{0, 1, 2, 3, 4, 5, 6, 7}) {
		fail("steps", "the steps were %v with %v results carried back", steps, carried)
	}
	return findings
}

func sameJSON(a, b []byte) bool {
	var left, right any
	if json.Unmarshal(a, &left) != nil || json.Unmarshal(b, &right) != nil {
		return false
	}
	return reflect.DeepEqual(left, right)
}

// BoundaryHostTools are the host tools the boundary adds to a run of the scenario's session (the
// offer vector's hostTools: the context tools), as the gate's offer holds them. A plain loop with no
// boundary declares these itself.
func BoundaryHostTools() ([]contract.ToolOfferHostTool, error) {
	_, offers, err := ReadVector("tool-offer/scenario-offer.json")
	if err != nil {
		return nil, err
	}
	var offer contract.ToolOffer
	if err := json.Unmarshal(offers[0], &offer); err != nil {
		return nil, err
	}
	return offer.HostTools, nil
}
