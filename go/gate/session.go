package gate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/wire"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/internal/runtimeactions"
)

// Options are what a host gives the gate for one session.
type Options struct {
	// Runtime is the runtime's transport, before any gate. Only the gate holds it.
	Runtime        Call
	RuntimeActions *RuntimeActionPort
	// Granted is what the host's configuration granted; the allow-list is it intersected with the
	// ceiling (Allowed).
	Granted []string
	// Bounds are the policy's bounds on the test tool; DefaultBounds where zero.
	Bounds Bounds
	// TestCalls is the turn's test calls so far: the execution checkpoint's testCalls, carried
	// through a retry, a budget continuation and a delegation's parent continuation.
	TestCalls int
	// Turn is the gate of the turn this gate's session runs within: a delegated child's gate takes
	// its parent's, and its test calls are then charged to the parent's turn, one count for the
	// parent and all its children (TestCalls is not read). Nil for a turn's own gate.
	Turn *Gate
	// Emit receives the guardrail events, written while the call is in flight, so each lands
	// between its call's tool_call and tool_result.
	Emit func(contract.RunEvent)
}

// Gate is the host's boundary for one session: the gated transport, the session's offer, the
// phase the run is in and the turn's test-call count. Its methods are safe for concurrent use.
//
// It is the host's handle and never an adapter's: it sets the offer, enters Plan mode and a
// child's phase, and holds the transport (which inspects frames and counts nothing). An adapter is
// handed Calls, the narrow handle, which can call, admit a host tool, and narrow the run to the
// critic's or the reformat run's phase and back, and nothing else (ADR-0004, decision 3).
type Gate struct {
	transport      Call
	runtimeActions *RuntimeActionPort
	runtimeOwner   *runtimeactions.Handle
	allowed        []string
	bounds         Bounds
	emit           func(contract.RunEvent)
	noticeGuard    func(contract.RunEvent) (contract.RunEvent, bool)

	mu                 sync.Mutex
	offer              contract.ToolOffer
	phase              Phase     // the phase the host entered
	narrowed           PhaseKind // the critic's or the reformat run's narrowing of it, or ""
	turn               *turnCount
	nextID             int64
	calls              *Calls
	contextEnrollments map[string]bool
	contextEnrollment  string
}

// GuardNotices binds only notice delivery to a run's output boundary. The returned function
// releases the binding when that run ends; neither the verdict nor the model's result changes.
func GuardNotices(c *Calls, guard func(contract.RunEvent) (contract.RunEvent, bool)) func() {
	if c == nil {
		return func() {}
	}
	g := c.g
	g.mu.Lock()
	g.noticeGuard = guard
	g.mu.Unlock()
	return func() { g.mu.Lock(); g.noticeGuard = nil; g.mu.Unlock() }
}

func (g *Gate) emitNotice(event contract.RunEvent) {
	g.mu.Lock()
	guard := g.noticeGuard
	g.mu.Unlock()
	if guard != nil {
		var deliver bool
		event, deliver = guard(event)
		if !deliver {
			return
		}
	}
	g.emit(contract.StampEvent(event))
}

// turnCount is a turn's test calls, one count shared by the turn's gate and its children's.
type turnCount struct {
	mu    sync.Mutex
	calls int
}

// Calls is the adapter's handle on the gate: the gated call, a host tool's admission, and the
// critic's and the reformat run's phases, each only a narrowing of the phase the host entered.
// It sets no offer, enters no other phase and holds no transport.
type Calls struct{ g *Gate }

// Calls is the narrow handle the host gives an adapter.
func (g *Gate) Calls() *Calls { return g.calls }

// CallTool is Gate.CallTool: the gated call.
func (c *Calls) CallTool(ctx context.Context, name string, args json.RawMessage) (Result, error) {
	return c.g.CallTool(ctx, name, args)
}

// AdmitHost is Gate.AdmitHost: a host tool held to the phase's offer.
func (c *Calls) AdmitHost(name string) *Result { return c.g.AdmitHost(name) }

// BeginCritic narrows the run to the critic's phase (agent#10): of the entered phase's offer, the
// critic's tools and readers.
func (c *Calls) BeginCritic() { c.g.narrow(PhaseCritic) }

// EndCritic ends the critic's phase, where the run is in it.
func (c *Calls) EndCritic() { c.g.widen(PhaseCritic) }

// BeginReformat narrows the run to the tool-free reformat run, which is offered nothing.
func (c *Calls) BeginReformat() { c.g.narrow(PhaseReformat) }

// EndReformat ends the reformat run's phase, where the run is in it.
func (c *Calls) EndReformat() { c.g.widen(PhaseReformat) }

func (g *Gate) narrow(kind PhaseKind) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.narrowed = kind
}

func (g *Gate) widen(kind PhaseKind) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.narrowed == kind {
		g.narrowed = ""
	}
}

// current is the phase a call is held to: the entered phase, narrowed where the adapter narrowed
// it. A narrowing only takes names away.
func (g *Gate) current() Phase {
	g.mu.Lock()
	defer g.mu.Unlock()
	switch g.narrowed {
	case PhaseCritic:
		return Phase{Kind: PhaseCritic, Runtime: intersect(g.phase.Runtime, CriticTools), Host: intersect(g.phase.Host, append(append([]string{}, CriticReaders...), SkillTools...))}
	case PhaseReformat:
		return ReformatPhase()
	}
	return g.phase
}

func intersect(list, within []string) []string {
	out := []string{}
	for _, name := range list {
		if contains(within, name) {
			out = append(out, name)
		}
	}
	return out
}

// New puts a session's runtime transport behind the gate.
func New(options Options) *Gate {
	g := &Gate{allowed: Allowed(options.Granted), bounds: options.Bounds, emit: options.Emit, turn: &turnCount{calls: options.TestCalls}}
	if options.RuntimeActions != nil {
		port := *options.RuntimeActions
		g.runtimeActions = &port
	}
	if options.Turn != nil {
		g.turn = options.Turn.turn
	}
	if g.bounds == (Bounds{}) {
		g.bounds = DefaultBounds
	}
	if g.emit == nil {
		g.emit = func(contract.RunEvent) {}
	}
	g.transport = Transport(options.Runtime, g.allowed, g.bounds, func(notice Notice) { g.emitNotice(notice.Event()) })
	g.phase = LoopPhase(contract.ToolOffer{})
	g.calls = &Calls{g: g}
	return g
}

// AllowList is the session's allow-list.
func (g *Gate) AllowList() []string { return append([]string(nil), g.allowed...) }

// Transport is the gated transport: every frame passes Inspect before it reaches the runtime.
func (g *Gate) Transport() Call { return g.transport }

// TestCalls is the turn's test calls so far, for the checkpoint: the parent's and its children's.
func (g *Gate) TestCalls() int {
	g.turn.mu.Lock()
	defer g.turn.mu.Unlock()
	return g.turn.calls
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
	var listErr error
	listed.Tools, listErr = g.listRuntime(ctx)
	if listErr != nil {
		return contract.ToolOffer{}, listErr
	}
	offer := contract.ToolOffer{Tools: []contract.ToolOfferRuntimeTool{}, HostTools: append([]contract.ToolOfferHostTool{}, hostTools...)}
	for _, tool := range listed.Tools {
		if contains(g.allowed, tool.Name) {
			offer.Tools = append(offer.Tools, tool)
		}
	}

	for _, tool := range offer.Tools {
		if p := wire.RuntimeTaxonomyProblem(wire.TaxonomyFromContract(tool.Meta), tool.Name); p != "" {
			g.emitNotice(Notice{Tool: "runtime", Action: "refused", Detail: "Runtime tool taxonomy refused."}.Event())
			return contract.ToolOffer{}, fmt.Errorf("runtime tool taxonomy refused")
		}
	}
	for _, tool := range offer.HostTools {
		if p := wire.TaxonomyProblem(wire.TaxonomyFromContract(tool.Taxonomy), false); p != "" {
			g.emitNotice(Notice{Tool: "host", Action: "refused", Detail: "Host tool taxonomy refused."}.Event())
			return contract.ToolOffer{}, fmt.Errorf("host tool taxonomy refused")
		}
	}
	g.SetOffer(offer)
	if err := g.bindRuntimeActions(listed.Tools); err != nil {
		return contract.ToolOffer{}, err
	}
	return g.Offer(), nil
}

// SetOffer sets the session's offer, where the host lists the runtime's tools itself, and enters
// the loop's phase.
func (g *Gate) SetOffer(offer contract.ToolOffer) {

	valid := offer
	valid.Tools = nil
	valid.HostTools = nil
	for _, tool := range offer.Tools {
		if wire.RuntimeTaxonomyProblem(wire.TaxonomyFromContract(tool.Meta), tool.Name) == "" {
			raw, _ := json.Marshal(wire.TaxonomyAnnotations(tool.Meta.Effect))
			members := map[string]json.RawMessage{}
			for k, v := range tool.AdditionalMembers {
				members[k] = append(json.RawMessage{}, v...)
			}
			members["annotations"] = raw
			tool.AdditionalMembers = members
			valid.Tools = append(valid.Tools, tool)
		} else {
			g.emitNotice(Notice{Tool: "runtime", Action: "refused", Detail: "Runtime tool taxonomy refused."}.Event())
		}
	}
	for _, tool := range offer.HostTools {
		if wire.TaxonomyProblem(wire.TaxonomyFromContract(tool.Taxonomy), false) == "" {
			valid.HostTools = append(valid.HostTools, tool)
		} else {
			g.emitNotice(Notice{Tool: "host", Action: "refused", Detail: "Host tool taxonomy refused."}.Event())
		}
	}
	offer = valid
	g.mu.Lock()
	defer g.mu.Unlock()
	g.offer = offer
	g.phase = LoopPhase(offer)
	g.narrowed = ""
}

// Offer is the session's offer.
func (g *Gate) Offer() contract.ToolOffer {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.offer
}

// Enter moves the run into a phase (Plan mode, a child's, the loop's): the host's, never the
// adapter's. The phase's offer is what a call is held to from here; a narrowing is ended.
func (g *Gate) Enter(phase Phase) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.phase = phase
	g.narrowed = ""
}

// Phase is the phase a call is held to now.
func (g *Gate) Phase() Phase { return g.current() }

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
	if !strings.HasSuffix(refusal, ".") {
		refusal += "."
	}
	return "refused: " + refusal + " This assistant proposes; it never writes a file and never calls a tool it was not offered."
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
	g.emitNotice(Notice{Tool: name, Action: "refused", Detail: detail}.Event())
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
	phase := g.current()
	if g.acting(name) {
		return g.refuse(name, "Runtime action requires an exact approved attempt. Nothing was sent."), nil
	}
	if g.runtimeActions != nil && name == RehearsalTool {
		listed, err := g.listRuntime(ctx)
		if err != nil {
			return Result{}, err
		}
		for _, tool := range listed {
			if tool.Name == name && tool.Meta.Effect == "act" {
				return g.refuse(name, "Runtime action requires an exact approved attempt. Nothing was sent."), nil
			}
		}
	}

	if !contains(phase.Runtime, name) {
		// Plan mode's and a child's refusal come after the turn's test-call count, as the TypeScript
		// boundary orders them (contextualSession's claimTestCall wraps the phase's own callTool,
		// agent#56): an unoffered test call there is counted, and past the cap the model reads the
		// cap's failure instead of the phase's refusal.
		if (phase.Kind == PhasePlan || phase.Kind == PhaseChild) && name == TestTool {
			if capped, ok := g.claimTestCall(); !ok {
				return capped, nil
			}
		}
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
		// Off the allow-list: decided by the gate on the wire, as the model wrote it, and never
		// sent; Inspect is the decision without the transport.
		verdict := Inspect(g.frame(name, args), g.allowed, g.bounds)
		if verdict.Send {
			return Result{}, errors.New("the gate would send a call its phase did not offer; it was not sent")
		}
		g.emitNotice(verdict.Notice.Event())
		return Result{Text: UnofferedRefusal(verdict.Violation.Error()), IsError: true}, nil
	}
	if name == TestTool {
		if capped, ok := g.claimTestCall(); !ok {
			return capped, nil
		}
	}
	return g.send(ctx, name, args)
}

// claimTestCall counts one test call against the turn (claimTestCall in context/session.ts): false,
// with the failure the model reads, past testCallsPerTurn.
func (g *Gate) claimTestCall() (Result, bool) {
	g.turn.mu.Lock()
	defer g.turn.mu.Unlock()
	if g.turn.calls >= g.bounds.TestCallsPerTurn {
		detail := TestCallCapDetail(g.bounds.TestCallsPerTurn)
		return failed(contract.Failure{Version: 1, Code: "tool-failed", Source: "tool", Outcome: "not-started", Detail: &detail}), false
	}
	g.turn.calls++
	return Result{}, true
}

// frame is one tools/call frame for a call as the model wrote it; arguments that are not JSON
// make a frame the gate cannot read, which it refuses as it refuses any such frame.
func (g *Gate) frame(name string, args json.RawMessage) json.RawMessage {
	params := map[string]any{"name": name}
	if len(strings.TrimSpace(string(args))) > 0 {
		params["arguments"] = args
	}
	frame, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": g.id(), "method": ToolsCall, "params": params})
	if err != nil {
		return json.RawMessage("unreadable")
	}
	return frame
}

// send puts one tools/call frame through the gated transport.
func (g *Gate) send(ctx context.Context, name string, args json.RawMessage) (Result, error) {
	reply, err := g.transport(ctx, g.frame(name, args))
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
				Type     string `json:"type"`
				Text     string `json:"text"`
				MimeType string `json:"mimeType"`
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
	counted := map[string]int{}
	for _, part := range answer.Result.Content {
		if part.Type == "text" {
			texts = append(texts, part.Text)
			continue
		}
		counted[part.Type]++
		texts = append(texts, notShownPart(part.Type, counted[part.Type], part.MimeType))
	}
	return Result{Text: strings.Join(texts, "\n"), IsError: answer.Result.IsError, Structured: answer.Result.StructuredContent}, nil
}

// RuntimePartNotShown is why a Runtime result's part other than text is not shown: the gate's call
// answers the adapter with the result's text and structured content only.
const RuntimePartNotShown = "a Runtime tool's result reaches this engine's model as text, and this part is not text"

// notShownPart is the line a Runtime result's part other than text leaves in its place, so none is
// dropped in silence: an image's is the contract's notShownLine (`[image n (type): not shown,
// because …]`), numbered among the result's images; another kind's names its kind the same way.
func notShownPart(kind string, number int, mediaType string) string {
	media := ""
	if mediaType != "" {
		media = " (" + mediaType + ")"
	}
	if kind == "" {
		kind = "part"
	}
	return fmt.Sprintf("[%s %d%s: not shown, because %s]", kind, number, media, RuntimePartNotShown)
}

// AdmitHost holds a host tool call to the phase's offer before the host runs it: nil where the
// phase offered it; otherwise the refusal the model is told, after the guardrail event (critic,
// reformat and loop) or with the phase's own words (Plan mode, a child).
func (g *Gate) AdmitHost(name string) *Result {
	phase := g.current()
	// ADR-0014: the reserved package proposal tool is always available to the author, including Plan mode.
	// The session boundary refuses host declarations using this name; children and the critic cannot enter it.
	if name == "remember" && (phase.Kind == PhaseLoop || phase.Kind == PhasePlan) {
		return nil
	}
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
