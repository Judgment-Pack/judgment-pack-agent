package gate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

// Options are what a host gives the gate for one session.
type Options struct {
	// Runtime is the runtime's transport, before any gate. Only the gate holds it.
	Runtime Call
	// Granted is what the host's configuration granted; the allow-list is it intersected with the
	// ceiling (Allowed).
	Granted []string
	// Bounds are the policy's bounds on the test tool; DefaultBounds where zero.
	Bounds Bounds
	// TestCalls is the turn's test calls so far: the execution checkpoint's testCalls, carried
	// through a retry, a budget continuation and a delegation's parent continuation.
	TestCalls int
	// Emit receives the guardrail events, written while the call is in flight, so each lands
	// between its call's tool_call and tool_result.
	Emit func(contract.RunEvent)
}

// Gate is the host's boundary for one session: the gated transport, the session's offer, the
// phase the run is in and the turn's test-call count. Its methods are safe for concurrent use.
type Gate struct {
	transport Call
	allowed   []string
	bounds    Bounds
	emit      func(contract.RunEvent)

	mu        sync.Mutex
	offer     contract.ToolOffer
	phase     Phase
	testCalls int
	nextID    int64
}

// New puts a session's runtime transport behind the gate.
func New(options Options) *Gate {
	g := &Gate{allowed: Allowed(options.Granted), bounds: options.Bounds, emit: options.Emit, testCalls: options.TestCalls}
	if g.bounds == (Bounds{}) {
		g.bounds = DefaultBounds
	}
	if g.emit == nil {
		g.emit = func(contract.RunEvent) {}
	}
	g.transport = Transport(options.Runtime, g.allowed, g.bounds, func(notice Notice) { g.emit(notice.Event()) })
	g.phase = LoopPhase(contract.ToolOffer{})
	return g
}

// AllowList is the session's allow-list.
func (g *Gate) AllowList() []string { return append([]string(nil), g.allowed...) }

// Transport is the gated transport: every frame passes Inspect before it reaches the runtime.
func (g *Gate) Transport() Call { return g.transport }

// TestCalls is the turn's test calls so far, for the checkpoint.
func (g *Gate) TestCalls() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.testCalls
}

func (g *Gate) id() int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.nextID++
	return g.nextID
}

func (g *Gate) rpc(ctx context.Context, method string, params any, into any) error {
	frame, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": g.id(), "method": method, "params": params})
	if err != nil {
		return err
	}
	reply, err := g.transport(ctx, frame)
	if err != nil {
		return err
	}
	var answer struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(reply, &answer); err != nil {
		return err
	}
	if answer.Error != nil {
		return &RuntimeError{Code: answer.Error.Code, Message: answer.Error.Message}
	}
	if into != nil {
		return json.Unmarshal(answer.Result, into)
	}
	return nil
}

// RuntimeError is a JSON-RPC error the runtime answered.
type RuntimeError struct {
	Code    int
	Message string
}

func (e *RuntimeError) Error() string { return fmt.Sprintf("runtime error %d: %s", e.Code, e.Message) }

// Open runs the runtime's handshake through the gate and sets the session's offer: of the tools
// tools/list served, those on the allow-list, as served and in its order, beside the host's
// tools. The run starts in the loop's phase, which offers all of them.
func (g *Gate) Open(ctx context.Context, hostTools []contract.ToolOfferHostTool) (contract.ToolOffer, error) {
	if err := g.rpc(ctx, "initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "judgment-pack-agent-go", "version": "0"},
	}, nil); err != nil {
		return contract.ToolOffer{}, err
	}
	if _, err := g.transport(ctx, json.RawMessage(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)); err != nil {
		return contract.ToolOffer{}, err
	}
	var listed struct {
		Tools []contract.ToolOfferRuntimeTool `json:"tools"`
	}
	if err := g.rpc(ctx, "tools/list", map[string]any{}, &listed); err != nil {
		return contract.ToolOffer{}, err
	}
	offer := contract.ToolOffer{Tools: []contract.ToolOfferRuntimeTool{}, HostTools: append([]contract.ToolOfferHostTool{}, hostTools...)}
	for _, tool := range listed.Tools {
		if contains(g.allowed, tool.Name) {
			offer.Tools = append(offer.Tools, tool)
		}
	}
	g.SetOffer(offer)
	return offer, nil
}

// SetOffer sets the session's offer, where the host lists the runtime's tools itself, and enters
// the loop's phase.
func (g *Gate) SetOffer(offer contract.ToolOffer) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.offer = offer
	g.phase = LoopPhase(offer)
}

// Offer is the session's offer.
func (g *Gate) Offer() contract.ToolOffer {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.offer
}

// Enter moves the run into a phase; the phase's offer is what a call is held to from here.
func (g *Gate) Enter(phase Phase) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.phase = phase
}

// Phase is the phase the run is in.
func (g *Gate) Phase() Phase {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.phase
}

// Result is a call's answer as the model is shown it and the tool_result event carries it.
type Result struct {
	Text       string
	IsError    bool
	Structured json.RawMessage
}

// Event is the result as a tool_result event.
func (r Result) Event(name string) contract.RunEvent {
	return contract.RunEvent{ToolResult: &contract.ToolResult{Type: "tool_result", Name: name, IsError: r.IsError, Text: r.Text, Structured: r.Structured}}
}

func failed(failure contract.Failure) Result {
	structured, _ := json.Marshal(map[string]any{"failure": failure})
	return Result{Text: *failure.Detail, IsError: true, Structured: structured}
}

// UnofferedRefusal is what the model is told of a call to a tool it was not offered, whoever
// refused it (unofferedRefusal in engines/contract.ts).
func UnofferedRefusal(refusal string) string {
	return "refused: " + refusal + ". This assistant proposes; it never writes a file and never calls a tool it was not offered."
}

// TestCallCapDetail is the failure a test call past the turn's cap earns (testCallCapFailure in
// context/session.ts): tool-failed, source tool, outcome not-started.
func TestCallCapDetail(limit int) string {
	plural := "s"
	if limit == 1 {
		plural = ""
	}
	return fmt.Sprintf("This turn has called %s %d time%s, the most one turn may (testCallsPerTurn); this call was not run. "+
		"Use the reports already obtained: fix what they show and propose, or say what remains. The person can test again in a new turn.",
		TestTool, limit, plural)
}

// ServedNotOffered is the refusal of a name the session serves but this phase was not offered
// (refuseUnoffered in context/session.ts).
func ServedNotOffered(name string) string {
	return name + " is served to this session but was not offered to this run; the call did not leave the page and nothing was written"
}

// The refusals a phase makes of its own, as the TypeScript boundary words them.
const (
	reformatRefusal = " is not offered to the reformat run, which is offered no tool; the call did not leave the page and nothing was written"
	planRefusal     = "Plan mode does not permit this operation. Review the plan and choose Start work."
	childRefusal    = "This task was not offered that Runtime tool."
)

// refuse emits the guardrail and returns the refusal the model is told.
func (g *Gate) refuse(name, detail string) Result {
	g.emit(Notice{Tool: name, Action: "refused", Detail: detail}.Event())
	return Result{Text: UnofferedRefusal(detail), IsError: true}
}

// CallTool is the gated call: one Runtime tool call the model made, as the model wrote it, held
// to the phase's offer, the turn's test-call cap and the gate on the wire, in that order.
//
//   - A name the phase did not offer is refused before any call. In the critic's phase and the
//     reformat run's, with a guardrail event and their own words (agent#10); in Plan mode and a
//     delegated child's, as the TypeScript boundary does, with the invalid-tool-input failure its
//     own words state and no guardrail. In the loop's phase, a name on the allow-list was served
//     and not offered: refused with a guardrail; any other name is put through the gate on the
//     wire as the model wrote it, which refuses it with its guardrail, and nothing reaches the
//     runtime either way. The model is told UnofferedRefusal.
//   - experimental_test_cases is counted against the turn before the gate sees it; past
//     testCallsPerTurn the call is not made and the model reads the failure that names the limit.
//   - The gate on the wire (Inspect) refuses with a guardrail and the permission failure, or
//     rewrites and sends; the runtime's answer comes back as its text, its structured content and
//     whether it is an error.
//
// An error is returned only where the call could not be completed at all (the context ended, the
// transport failed); every refusal is a Result the model reads.
func (g *Gate) CallTool(ctx context.Context, name string, args json.RawMessage) (Result, error) {
	g.mu.Lock()
	phase := g.phase
	g.mu.Unlock()
	if !contains(phase.Runtime, name) {
		switch phase.Kind {
		case PhaseCritic:
			return g.refuse(name, CriticRefusalDetail(name)), nil
		case PhaseReformat:
			return g.refuse(name, name+reformatRefusal), nil
		case PhasePlan:
			return Result{Text: UnofferedRefusal(planRefusal), IsError: true}, nil
		case PhaseChild:
			return Result{Text: UnofferedRefusal(childRefusal), IsError: true}, nil
		}
		if contains(g.allowed, name) {
			return g.refuse(name, ServedNotOffered(name)), nil
		}
		result, err := g.send(ctx, name, args)
		if err != nil {
			return Result{}, err
		}
		if !result.IsError {
			// Unreachable: a name off the allow-list never leaves. Held here so that it never could.
			return Result{}, errors.New("the gate sent a call the phase did not offer")
		}
		return Result{Text: UnofferedRefusal(result.Text), IsError: true}, nil
	}
	if name == TestTool {
		g.mu.Lock()
		if g.testCalls >= g.bounds.TestCallsPerTurn {
			g.mu.Unlock()
			detail := TestCallCapDetail(g.bounds.TestCallsPerTurn)
			return failed(contract.Failure{Version: 1, Code: "tool-failed", Source: "tool", Outcome: "not-started", Detail: &detail}), nil
		}
		g.testCalls++
		g.mu.Unlock()
	}
	return g.send(ctx, name, args)
}

// send puts one tools/call frame through the gated transport.
func (g *Gate) send(ctx context.Context, name string, args json.RawMessage) (Result, error) {
	params := map[string]any{"name": name}
	if len(strings.TrimSpace(string(args))) > 0 {
		params["arguments"] = args
	}
	frame, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": g.id(), "method": ToolsCall, "params": params})
	if err != nil {
		// Arguments that are not JSON: the frame cannot be read, and is refused as the gate refuses
		// any frame it cannot read.
		verdict := Inspect(nil, g.allowed, g.bounds)
		g.emit(verdict.Notice.Event())
		return failed(verdict.Violation.Failure()), nil
	}
	reply, err := g.transport(ctx, frame)
	var violation *Violation
	if errors.As(err, &violation) {
		result := failed(violation.Failure())
		return result, nil
	}
	if err != nil {
		return Result{}, err
	}
	var answer struct {
		Result *struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			StructuredContent json.RawMessage `json:"structuredContent"`
			IsError           bool            `json:"isError"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(reply, &answer); err != nil {
		return Result{}, fmt.Errorf("the runtime's answer is not JSON-RPC: %w", err)
	}
	if answer.Error != nil {
		return Result{Text: answer.Error.Message, IsError: true}, nil
	}
	if answer.Result == nil {
		return Result{}, errors.New("the runtime answered with neither a result nor an error")
	}
	var texts []string
	for _, part := range answer.Result.Content {
		if part.Type == "text" {
			texts = append(texts, part.Text)
		}
	}
	return Result{Text: strings.Join(texts, "\n"), IsError: answer.Result.IsError, Structured: answer.Result.StructuredContent}, nil
}

// AdmitHost holds a host tool call to the phase's offer before the host runs it: nil where the
// phase offered it; otherwise the refusal the model is told, after the guardrail event (critic,
// reformat and loop) or with the phase's own words (Plan mode, a child).
func (g *Gate) AdmitHost(name string) *Result {
	g.mu.Lock()
	phase := g.phase
	g.mu.Unlock()
	if contains(phase.Host, name) {
		return nil
	}
	var result Result
	switch phase.Kind {
	case PhaseCritic:
		result = g.refuse(name, CriticRefusalDetail(name))
	case PhaseReformat:
		result = g.refuse(name, name+reformatRefusal)
	case PhasePlan:
		result = Result{Text: UnofferedRefusal(planRefusal), IsError: true}
	case PhaseChild:
		result = Result{Text: UnofferedRefusal(childRefusal), IsError: true}
	default:
		result = g.refuse(name, name+" is not one of the tools this run was offered; the call did not leave the page and nothing was written")
	}
	return &result
}
