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
	"strings"
	"sync"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

// Run is what the runner hands an adapter for one leg, the Go form of the TypeScript session:
// capabilities, never an address or a credential.
//
// Runtime is the runtime's transport before any gate. The gate is the host's (ADR-0001); until
// the Go gate exists (ADR-0004), an adapter under test puts this transport behind the gate it is
// given by its own test, and the leg measures at the runtime what got through.
type Run struct {
	Wire      string
	Model     ModelCall
	Runtime   RuntimeCall
	HostTools []contract.ToolOfferHostTool
	Prompt    string
	Emit      func(contract.RunEvent)
}

// Adapter is one Go adapter, as the runner drives it.
type Adapter interface {
	Run(ctx context.Context, run *Run) error
}

// AdapterFunc lets a function be an Adapter.
type AdapterFunc func(ctx context.Context, run *Run) error

// Run calls f.
func (f AdapterFunc) Run(ctx context.Context, run *Run) error { return f(ctx, run) }

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
	scenario, err := server.Scenario(ctx)
	if err != nil {
		return nil, err
	}
	_, offers, err := ReadVector("tool-offer/scenario-offer.json")
	if err != nil {
		return nil, err
	}
	result := &LegResult{Endpoint: endpoint, scenario: scenario}
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
	run := &Run{
		Wire:      endpoint.API,
		Model:     session.ModelCall(),
		Runtime:   session.RuntimeCall(),
		HostTools: result.offer.HostTools,
		Prompt:    scenario.Policy,
		Emit: func(event contract.RunEvent) {
			mu.Lock()
			defer mu.Unlock()
			result.Events = append(result.Events, event)
		},
	}
	seal := sealNetwork()
	result.RunError = adapter.Run(ctx, run)
	result.reached = seal.lift()
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

// Checks holds a leg to the checks of the TypeScript `engine <id>` leg that read the events and
// the server's records: K1a (as http.DefaultTransport), K1, K2, K3a, K3b, K3c, and the event
// sequence the contract's scenario vector states. It returns the checks that did not hold, each by its name.
func (r *LegResult) Checks() []Finding {
	var findings []Finding
	fail := func(check, format string, args ...any) {
		findings = append(findings, Finding{check, fmt.Sprintf(format, args...)})
	}
	if r.RunError != nil {
		fail("run", "the adapter's run failed: %v", r.RunError)
	}
	for _, reach := range r.reached {
		fail("K1a", "the adapter reached for http.DefaultTransport: %s", reach)
	}
	requests := r.Observations.Requests
	seen := r.Observations.Seen

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
	evaluated := false
	for _, call := range seen {
		if call.Name == "experimental_evaluate" {
			evaluated = true
			if call.Args["rehearsal"] != true {
				fail("K3a", "experimental_evaluate reached the runtime without rehearsal: true")
			}
		}
	}
	if !evaluated {
		fail("K3a", "experimental_evaluate never reached the runtime")
	}
	types := make([]string, len(r.Events))
	for i, event := range r.Events {
		types[i] = EventType(event)
	}
	guard := func(check, tool, action string) {
		matched := 0
		for i, event := range r.Events {
			if event.Guardrail == nil || event.Guardrail.Tool != tool || event.Guardrail.Action != action {
				continue
			}
			matched++
			if i == 0 || i+1 >= len(types) || types[i-1] != "tool_call" || types[i+1] != "tool_result" {
				fail(check, "the %s guardrail for %s is not between its call and its result", action, tool)
			}
		}
		if matched != 1 {
			fail(check, "%d %s guardrails for %s, not one", matched, action, tool)
		}
	}
	guard("K3a", "experimental_evaluate", "rewrote")

	// K3b: write_file never reaches the runtime, and the model is told.
	for _, call := range seen {
		if call.Name == "write_file" {
			fail("K3b", "write_file reached the runtime")
		}
		if call.Refusal != "" && call.Name != "write_file" {
			fail("K3b", "the runtime refused %s: %s", call.Name, call.Refusal)
		}
	}
	guard("K3b", "write_file", "refused")
	told := false
	for _, event := range r.Events {
		if event.ToolResult != nil && event.ToolResult.Name == "write_file" && event.ToolResult.IsError {
			told = true
		}
	}
	if !told {
		fail("K3b", "no failed tool_result for write_file")
	}

	// K3c: one proposal, DRAFT_V2 member for member, and the unknowns whole.
	var proposals []*contract.RunEventProposal
	for _, event := range r.Events {
		if event.Proposal != nil {
			proposals = append(proposals, event.Proposal)
		}
	}
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

	// The sequence the scenario vector states, one end, no error.
	if !reflect.DeepEqual(types, r.sequence) {
		fail("sequence", "the events were %v, not %v", types, r.sequence)
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
