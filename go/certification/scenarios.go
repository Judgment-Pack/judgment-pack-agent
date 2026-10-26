package certification

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/turn"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
)

// The certified scenarios (agent#13, certification/scenarios.test.ts): eleven scripted scenarios run
// on the OpenAI-compatible wire, once with each answer shape, through the shared server's scenario
// sessions. The host's half of each (its host tools, the chat context, Plan mode, the correction
// loop's checks, the delegation coordinator) is this runner's, as it is the TypeScript suite's.

// ScenarioFile reads one scenario's file (certification/scenarios/<name>.json).
func ScenarioFile(name string, into any) error {
	root, err := RepositoryRoot()
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(filepath.Join(root, "packages", "harness", "certification", "scenarios", name+".json"))
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, into)
}

// ScenarioHostTool is a host tool as a scenario lists it (ScenarioHostTool in scenarioDriver.ts).
type ScenarioHostTool struct {
	Taxonomy      *runcontext.ToolTaxonomy      `json:"taxonomy"`
	Name          string                        `json:"name"`
	Effect        string                        `json:"effect"`
	Delegation    string                        `json:"delegation"`
	ContextPolicy *runcontext.ToolContextPolicy `json:"contextPolicy"`
	Description   string                        `json:"description"`
	Result        string                        `json:"result"`
}

// Received is one host tool call as the host received it.
type Received struct {
	Tool      string
	Arguments json.RawMessage
}

// HostLedger counts what the host tools ran (hostToolsOf's executed and received).
type HostLedger struct {
	mu       sync.Mutex
	Executed map[string]int
	Received []Received
}

func (h *HostLedger) executed() map[string]int {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := map[string]int{}
	for name, count := range h.Executed {
		out[name] = count
	}
	return out
}

// HostToolsOf is hostToolsOf: the host tools a scenario lists, each counting its own executions.
func HostToolsOf(listed []ScenarioHostTool, ledger *HostLedger) []runcontext.HostTool {
	var out []runcontext.HostTool
	for _, tool := range listed {
		held := tool
		schema := json.RawMessage(`{"type":"object","properties":{},"additionalProperties":true}`)
		if held.Name == turn.ProposePackTool {
			schema = json.RawMessage(turn.ProposePackSchema)
		}
		out = append(out, runcontext.HostTool{
			Name: held.Name, Taxonomy: held.Taxonomy, Effect: held.Effect, Delegation: held.Delegation, ContextPolicy: held.ContextPolicy,
			Description: held.Description, InputSchema: schema,
			Execute: func(_ context.Context, args json.RawMessage) (runcontext.ToolResult, error) {
				ledger.mu.Lock()
				if ledger.Executed == nil {
					ledger.Executed = map[string]int{}
				}
				ledger.Executed[held.Name]++
				ledger.Received = append(ledger.Received, Received{Tool: held.Name, Arguments: args})
				ledger.mu.Unlock()
				return runcontext.ResultOf([]runcontext.Content{runcontext.TextContent(held.Result)}, nil), nil
			},
		})
	}
	return out
}

// boundaryTools are the declarations of the context tools the boundary adds to a run: the
// ledger's readers, and the execution's three controls where the run permits conversation; and request_review
// (ADR-0010), which the gate admits in the loop's phase and the boundary offers where the critic's
// mode is auto, the table's default (runcontext.OfferedReviewTool). This fixture uses default
// policy; production final-offer enrollment reads the actual contextual Session instead.
func boundaryTools(allowConversation bool) []contract.ToolOfferHostTool {
	ledger := runcontext.NewContextLedger(&runcontext.Defaults, nil)
	out := []contract.ToolOfferHostTool{}
	for _, tool := range ledger.Tools() {
		out = append(out, tool.Declaration())
	}
	if allowConversation {
		controller := &runcontext.ExecutionController{}
		for _, tool := range []runcontext.HostTool{controller.Tool(), controller.CompletionTool(), controller.BudgetTool()} {
			out = append(out, tool.Declaration())
		}
	}
	return append(out, runcontext.OfferedReviewTool(runcontext.Session{}, runcontext.Defaults)...)
}

// ScenarioRun is one run of a scenario: its events, what reached the runtime, the requests the
// endpoint read, and the host's own counts.
type ScenarioRun struct {
	Events   []contract.RunEvent
	Seen     []ServerObservation
	Requests []EndpointRequest
	RunError error
}

// scenarioHost is the host's side of one scenario session: the gate over its runtime, the offer
// opened through it, and the events collected.
type scenarioHost struct {
	session *Session
	gate    *gate.Gate
	offer   contract.ToolOffer
	mu      sync.Mutex
	events  []contract.RunEvent
	// watcher sees each event after it is collected: a host that breaks a run off at an event
	// (scenario 10's resume) cancels it there.
	watcher func(contract.RunEvent)
}

func (h *scenarioHost) emit(event contract.RunEvent) {
	h.mu.Lock()
	h.events = append(h.events, event)
	watcher := h.watcher
	h.mu.Unlock()
	if watcher != nil {
		watcher(event)
	}
}

// watch sets the watcher, or clears it with nil.
func (h *scenarioHost) watch(watcher func(contract.RunEvent)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.watcher = watcher
}

func (h *scenarioHost) take() []contract.RunEvent {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := h.events
	h.events = nil
	return out
}

func openScenario(ctx context.Context, server *Server, endpoint ScenarioEndpoint, hostTools []runcontext.HostTool, allowConversation bool) (*scenarioHost, error) {
	session, err := server.NewScenarioSession(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	h := &scenarioHost{session: session}
	h.gate = gate.New(gate.Options{Runtime: gate.Call(session.RuntimeCall()), Granted: gate.Ceiling, Emit: h.emit})
	// The host's tools as the boundary will offer them (ADR-0006): a tool with a source by its wire
	// name, and load_tool where the schema budget makes a source a catalogue.
	declared, err := runcontext.OfferedHostTools(hostTools, gate.Ceiling, runcontext.Defaults)
	if err != nil {
		return nil, err
	}
	declared = append(declared, boundaryTools(allowConversation)...)
	h.offer, err = h.gate.Open(ctx, declared)
	if err != nil {
		return nil, fmt.Errorf("the handshake through the gate: %w", err)
	}
	h.take()
	return h, nil
}

// runOn runs the adapter once on the scenario session, the run's events collected.
func (h *scenarioHost) runOn(ctx context.Context, adapter Adapter, run *Run) (*ScenarioRun, error) {
	return h.runOnWithDefault(ctx, adapter, run, true)
}

// runOnOmittedThinking is the one certification path that distinguishes omission from explicit
// off; legacy scenario fixtures that do not state a tier retain their historical off default.
func (h *scenarioHost) runOnOmittedThinking(ctx context.Context, adapter Adapter, run *Run) (*ScenarioRun, error) {
	return h.runOnWithDefault(ctx, adapter, run, false)
}

func (h *scenarioHost) runOnWithDefault(ctx context.Context, adapter Adapter, run *Run, defaultOff bool) (*ScenarioRun, error) {
	run.Wire = "openai-compatible"
	if run.ModelName == "" {
		run.ModelName = "scripted-model"
	}
	run.Model = h.session.ModelCall()
	run.Gate = h.gate.Calls()
	run.Tools = h.offer.Tools
	if defaultOff && run.Thinking == "" {
		run.Thinking = "off"
	}
	run.Emit = h.emit
	seal := sealNetwork()
	err := adapter.Run(ctx, run)
	reached := seal.lift()
	if err == nil && len(reached) > 0 {
		err = fmt.Errorf("the adapter reached for http.DefaultTransport: %v", reached)
	}
	// What the server observed is read whatever ended the run, a host that broke it off included.
	observed, obsErr := h.session.Observations(context.WithoutCancel(ctx))
	if obsErr != nil {
		return nil, obsErr
	}
	return &ScenarioRun{Events: h.take(), Seen: observed.Seen, Requests: observed.Endpoint, RunError: err}, nil
}

// request is the context a scenario's run is given for the person's message.
func requestInput(message string) *runcontext.Input {
	return &runcontext.Input{CurrentRequest: &runcontext.CurrentRequest{Message: message, Input: message}}
}

// offeredNames are the tool names a request showed the model, in its order.
func offeredNames(request EndpointRequest) []string {
	var body struct {
		Tools []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	_ = json.Unmarshal(request.Body, &body)
	var out []string
	for _, tool := range body.Tools {
		out = append(out, tool.Function.Name)
	}
	return out
}

func sortedCopy(list []string) []string {
	out := append([]string(nil), list...)
	sort.Strings(out)
	return out
}

// withoutCheckpoints is a run's behavioural events less its execution checkpoints and the
// terminal accounting record. Scenario 20 certifies that record itself; older scenario vectors
// remain comparisons of the behaviour they name.
func withoutCheckpoints(events []contract.RunEvent) []contract.RunEvent {
	var out []contract.RunEvent
	for _, event := range events {
		if event.ExecutionCheckpoint == nil && event.Usage == nil {
			out = append(out, event)
		}
	}
	return out
}

// ScenarioCheck is one scenario's checks on one answer shape: the names it reports by, and the run.
type ScenarioCheck struct {
	Scenario string
	Names    []string
	Run      func(ctx context.Context, server *Server, answerAs string, adapter Adapter) ([]Finding, error)
}
