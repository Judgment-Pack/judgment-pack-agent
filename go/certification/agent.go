package certification

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/turn"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
)

// The agent capability's certification (ADR-0007, agent#67). Each check runs the adapter and the
// delegation coordinator the caller binds against a provider-run function for a script: the
// package's stand-in, a fake exclusive account (StandIn), or a host's own bridge, account and tool
// handler bound to the same script (AgentHost.Bind). Whatever the binding, the check holds its
// function inside a monitor that records every invocation, its request, the answers its calls got,
// and any invocation started while another was still running on the account: one active invocation
// per account is the rule a child started before its parent's release breaks.

// AgentStep is one thing a scripted provider does in an invocation, in order: a call of the tool
// offered under the name Tool (an alias's description starts with the tool's own name and a colon),
// with its arguments; a commentary message; or the final message.
type AgentStep struct {
	Tool       string
	Arguments  json.RawMessage
	Commentary string
	Say        string
}

// AgentPlay is what one invocation does: its steps; Busy, how many of its attempts the account
// refuses at admission as busy before it admits it (nothing having run); Fail, the error an
// admitted invocation ends with after its steps (an outcome, never sent again); Hold, the
// invocation waits, its steps not taken, until it is stopped.
type AgentPlay struct {
	Steps []AgentStep
	Busy  int
	Fail  error
	Hold  bool
}

// AgentScript is a check's provider: what the invocation with this request does, number being its
// place among the check's admitted invocations, the first 1.
type AgentScript func(request adapter.AgentRequest, number int) AgentPlay

// AgentHost is what the agent checks run against. The package's stand-in is StandIn; a host passes
// its own bridge, account and tool handler bound to each check's script, so its own binding is
// what is certified (cmd/harness-check -agent says which ran).
type AgentHost struct {
	// Name is what the checks ran against, as a report names it.
	Name string
	// StandIn is true for the package's own fake account, false for a host's binding.
	StandIn bool
	// Bind is the provider-run function for one check's script: it plays each invocation's steps
	// through the binding's own account and handler, answers the account's busy refusal as an error
	// wrapping adapter.ErrBusy, and returns only once the invocation has released the account.
	Bind func(script AgentScript) adapter.AgentRun
	// NewGate is the host's gate over a check's runtime; nil is gate.New over the ceiling.
	NewGate func(runtime gate.Call, emit func(contract.RunEvent)) *gate.Gate
}

// FakeAccount is the stand-in's provider account: one active invocation at a time. An invocation
// admitted while another holds it is a violation, recorded, and refused as busy. The account is
// released ReleaseAfter its invocation's run ends (the process reaped), and the invocation's
// function returns only then.
type FakeAccount struct {
	ReleaseAfter time.Duration

	mu         sync.Mutex
	held       bool
	admitted   int
	refused    map[int]int
	violations []string
}

// NewFakeAccount is an account with a short release, as a bridge's reap takes.
func NewFakeAccount() *FakeAccount {
	return &FakeAccount{ReleaseAfter: 20 * time.Millisecond, refused: map[int]int{}}
}

// Violations are the invocations the account was asked to admit while another held it.
func (a *FakeAccount) Violations() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.violations...)
}

// Run is the account's provider-run function for a script.
func (a *FakeAccount) Run(script AgentScript) adapter.AgentRun {
	return func(ctx context.Context, request adapter.AgentRequest, callbacks adapter.AgentCallbacks) error {
		a.mu.Lock()
		if a.held {
			a.violations = append(a.violations, "an invocation ("+describe(request)+") asked for the account while another held it")
			a.mu.Unlock()
			return fmt.Errorf("the account is running another invocation: %w", adapter.ErrBusy)
		}
		number := a.admitted + 1
		play := script(request, number)
		if a.refused[number] < play.Busy {
			a.refused[number]++
			a.mu.Unlock()
			return fmt.Errorf("the account refused the invocation at admission: %w", adapter.ErrBusy)
		}
		a.held = true
		a.admitted = number
		a.mu.Unlock()
		defer func() {
			if a.ReleaseAfter > 0 {
				time.Sleep(a.ReleaseAfter)
			}
			a.mu.Lock()
			a.held = false
			a.mu.Unlock()
		}()
		return Play(ctx, request, callbacks, play)
	}
}

// StandIn is the package's own binding: a fake exclusive account per check.
func StandIn() AgentHost {
	return AgentHost{Name: "the package's stand-in (a fake exclusive account)", StandIn: true,
		Bind: func(script AgentScript) adapter.AgentRun { return NewFakeAccount().Run(script) }}
}

// Play takes one invocation's steps through its callbacks, as a provider would: each call by the
// alias its tool was offered under, each message as its text and then the finished message. A
// callback's error ends the invocation, as a bridge stops its provider's run.
func Play(ctx context.Context, request adapter.AgentRequest, callbacks adapter.AgentCallbacks, play AgentPlay) error {
	if play.Hold {
		<-ctx.Done()
		return ctx.Err()
	}
	for at, step := range play.Steps {
		if err := ctx.Err(); err != nil {
			return err
		}
		id := fmt.Sprintf("item-%d", at+1)
		switch {
		case step.Tool != "":
			args := step.Arguments
			if len(args) == 0 {
				args = json.RawMessage(`{}`)
			}
			if _, err := callbacks.Tool(ctx, adapter.AgentToolCall{ID: "call-" + id, Name: OfferedName(request, step.Tool), Arguments: args}); err != nil {
				return err
			}
		case step.Commentary != "":
			if err := callbacks.Event(ctx, adapter.AgentEvent{Type: "message", ID: id, Text: step.Commentary, Phase: "commentary"}); err != nil {
				return err
			}
		case step.Say != "":
			if err := callbacks.Event(ctx, adapter.AgentEvent{Type: "text", ID: id, Text: step.Say}); err != nil {
				return err
			}
			if err := callbacks.Event(ctx, adapter.AgentEvent{Type: "message", ID: id, Text: step.Say, Phase: "final"}); err != nil {
				return err
			}
		}
	}
	return play.Fail
}

// OfferedName is the name a tool was offered to an invocation under, or the tool's own name where
// it was not offered (as a model that ignored its offer would call it).
func OfferedName(request adapter.AgentRequest, tool string) string {
	for _, offered := range request.Tools {
		if strings.HasPrefix(offered.Description, tool+":") {
			return offered.Name
		}
	}
	return tool
}

// OfferedTools are the tools' own names an invocation was offered.
func OfferedTools(request adapter.AgentRequest) []string {
	out := []string{}
	for _, offered := range request.Tools {
		name, _, _ := strings.Cut(offered.Description, ":")
		out = append(out, name)
	}
	return out
}

func describe(request adapter.AgentRequest) string {
	kind := "parent"
	switch {
	case request.Phase == "critic":
		kind = "critic"
	case isChild(request):
		kind = "child"
	}
	return kind
}

// isChild is whether a request is a delegated child's: its prompt carries the child's rules.
func isChild(request adapter.AgentRequest) bool {
	return strings.Contains(request.Prompt, turn.ChildRules) || strings.Contains(request.Instructions, turn.ChildRules)
}

// Observed is one call of the bound function as the monitor saw it.
type Observed struct {
	Request  adapter.AgentRequest
	Answers  []ObservedAnswer
	Err      error
	Busy     bool
	Overlap  bool
	Sequence int
}

// ObservedAnswer is the answer one tool call got: the tool's own name, its text, whether it failed.
type ObservedAnswer struct {
	Tool    string
	Text    string
	IsError bool
}

// monitor holds a bound function: every call recorded, any call started while another still runs
// on the account recorded as a violation, and a log of what happened in order.
type monitor struct {
	mu         sync.Mutex
	active     int
	calls      []*Observed
	violations []string
	log        []string
	onStart    func(*Observed)
}

func (m *monitor) note(line string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.log = append(m.log, line)
}

func (m *monitor) wrap(run adapter.AgentRun) adapter.AgentRun {
	return func(ctx context.Context, request adapter.AgentRequest, callbacks adapter.AgentCallbacks) error {
		m.mu.Lock()
		held := &Observed{Request: request, Sequence: len(m.calls) + 1}
		if m.active > 0 {
			held.Overlap = true
			m.violations = append(m.violations, "a "+describe(request)+" invocation started while another was still running on the account")
		}
		m.active++
		m.calls = append(m.calls, held)
		m.log = append(m.log, "start "+describe(request))
		start := m.onStart
		m.mu.Unlock()
		if start != nil {
			start(held)
		}
		tool := callbacks.Tool
		callbacks.Tool = func(ctx context.Context, call adapter.AgentToolCall) (adapter.AgentToolAnswer, error) {
			answer, err := tool(ctx, call)
			name := call.Name
			for _, offered := range request.Tools {
				if offered.Name == call.Name {
					name, _, _ = strings.Cut(offered.Description, ":")
				}
			}
			m.mu.Lock()
			held.Answers = append(held.Answers, ObservedAnswer{Tool: name, Text: answer.Text, IsError: answer.IsError || err != nil})
			m.mu.Unlock()
			return answer, err
		}
		err := run(ctx, request, callbacks)
		m.mu.Lock()
		m.active--
		held.Err = err
		held.Busy = err != nil && adapter.IsBusy(err)
		m.log = append(m.log, "end "+describe(request))
		m.mu.Unlock()
		return err
	}
}

// admitted are the calls the account admitted, in order: every call but a busy refusal.
func (m *monitor) admitted() []*Observed {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*Observed
	for _, held := range m.calls {
		if !held.Busy {
			out = append(out, held)
		}
	}
	return out
}

func (m *monitor) logLines() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.log...)
}

func (m *monitor) violationLines() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.violations...)
}

func (m *monitor) all() []*Observed {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]*Observed(nil), m.calls...)
}

func (m *monitor) running() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.active
}

// agentRuntime is the agent checks' runtime: the gate's ceiling, each tool answering a fixed text,
// and the calls it received.
type agentRuntime struct {
	mu   sync.Mutex
	seen []string
}

func (rt *agentRuntime) call(_ context.Context, frame json.RawMessage) (json.RawMessage, error) {
	var request struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params struct {
			Name string `json:"name"`
		} `json:"params"`
	}
	if err := json.Unmarshal(frame, &request); err != nil {
		return nil, err
	}
	answer := func(result any) (json.RawMessage, error) {
		encoded, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
		return encoded, err
	}
	switch request.Method {
	case "notifications/initialized":
		return nil, nil
	case "initialize":
		return answer(map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "serverInfo": map[string]any{"name": "agent-checks", "version": "0"}})
	case "tools/list":
		var tools []map[string]any
		for _, name := range gate.Ceiling {
			tools = append(tools, map[string]any{"name": name, "description": name + " of the checks' runtime", "inputSchema": map[string]any{"type": "object"}})
		}
		return answer(map[string]any{"tools": tools})
	case "tools/call":
		rt.mu.Lock()
		rt.seen = append(rt.seen, request.Params.Name)
		rt.mu.Unlock()
		return answer(map[string]any{"content": []any{map[string]any{"type": "text", "text": request.Params.Name + " answered."}}})
	}
	return answer(map[string]any{})
}

func (rt *agentRuntime) calls() []string {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return append([]string(nil), rt.seen...)
}

// agentWorld is one agent check's host: the runtime and its gate, the host tools and what they ran,
// the chat, the durable callback and what it acknowledged, the events, and the monitored binding.
type agentWorld struct {
	runtime *agentRuntime
	gate    *gate.Gate
	offer   contract.ToolOffer
	host    AgentHost
	watch   *monitor

	mu        sync.Mutex
	events    []contract.RunEvent
	ran       map[string]int
	saves     []contract.Checkpoint
	failSave  func(state contract.Checkpoint) bool
	afterSave func(state contract.Checkpoint)
	enabled   []string
	asked     []string
}

// The host tools of the agent checks: a read (offered to a review child), a research source (a
// capability no review child holds), a write (offered to no child), and delegate_tasks, which the
// coordinator adds.
const (
	readTool     = "read_note"
	researchTool = "search_web"
	writeTool    = "write_note"
)

const (
	parentModel = "agent-model"
	childModel  = "agent-small"
	criticModel = "agent-critic"
)

func newAgentWorld(ctx context.Context, host AgentHost, plan bool) (*agentWorld, error) {
	w := &agentWorld{runtime: &agentRuntime{}, host: host, watch: &monitor{}, ran: map[string]int{}, enabled: []string{parentModel, childModel, criticModel}}
	if host.NewGate != nil {
		w.gate = host.NewGate(w.runtime.call, w.emit)
	} else {
		w.gate = gate.New(gate.Options{Runtime: w.runtime.call, Granted: gate.Ceiling, Emit: w.emit})
	}
	delegate := turn.DelegationTool(turn.TaskProfiles)
	declared := []contract.ToolOfferHostTool{delegate}
	for _, tool := range w.hostTools() {
		declared = append(declared, tool.Declaration())
	}
	declared = append(declared, boundaryTools(true)...)
	offer, err := w.gate.Open(ctx, declared)
	if err != nil {
		return nil, err
	}
	w.offer = offer
	if plan {
		w.gate.Enter(gate.PlanPhase(offer))
	}
	w.events = nil
	return w, nil
}

func (w *agentWorld) emit(event contract.RunEvent) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.events = append(w.events, event)
}

func (w *agentWorld) take() []contract.RunEvent {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := w.events
	w.events = nil
	return out
}

func (w *agentWorld) hostTools() []runcontext.HostTool {
	tool := func(name, effect, delegation string) runcontext.HostTool {
		return runcontext.HostTool{Name: name, Effect: effect, Delegation: delegation, Description: name + " of the checks' host.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":true}`),
			Execute: func(context.Context, json.RawMessage) (runcontext.ToolResult, error) {
				w.mu.Lock()
				w.ran[name]++
				w.mu.Unlock()
				return runcontext.ResultOf([]runcontext.Content{runcontext.TextContent(name + " answered.")}, nil), nil
			}}
	}
	return []runcontext.HostTool{tool(readTool, "read", "read"), tool(researchTool, "", "research"), tool(writeTool, "write", "")}
}

func (w *agentWorld) hostRan(name string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.ran[name]
}

// children is the host's capability to start a child's gated session, in its profile's phase within
// the parent's, its test calls charged to the parent's turn.
func (w *agentWorld) children() adapter.Children {
	return func(_ context.Context, profile contract.DelegatedTaskProfile, emit func(contract.RunEvent)) (*gate.Calls, []contract.ToolOfferRuntimeTool, error) {
		child := gate.New(gate.Options{Runtime: w.runtime.call, Granted: gate.Ceiling, Emit: emit, Turn: w.gate})
		child.SetOffer(w.offer)
		phase := gate.ChildPhase(profile, w.offer, w.gate.Phase())
		child.Enter(phase)
		return child.Calls(), phase.Offer(w.offer).Tools, nil
	}
}

// checkpoint is the host's durable callback: each save acknowledged, recorded and logged, unless the
// check's failSave refuses it.
func (w *agentWorld) checkpoint(_ context.Context, state contract.Checkpoint) error {
	w.mu.Lock()
	fail, after := w.failSave, w.afterSave
	w.mu.Unlock()
	line := "save"
	if state.Handoff != nil {
		line += " " + state.Handoff.State
	}
	if fail != nil && fail(state) {
		w.watch.note(line + " refused")
		return errors.New("the host's store did not acknowledge the checkpoint")
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return err
	}
	var copied contract.Checkpoint
	if err := json.Unmarshal(encoded, &copied); err != nil {
		return err
	}
	w.mu.Lock()
	w.saves = append(w.saves, copied)
	w.mu.Unlock()
	w.watch.note(line)
	if after != nil {
		after(copied)
	}
	return nil
}

func (w *agentWorld) acknowledged() []contract.Checkpoint {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]contract.Checkpoint(nil), w.saves...)
}

// admit is the host's enabled-model admission: the models the connection enables.
func (w *agentWorld) admit(_ context.Context, model string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.asked = append(w.asked, model)
	for _, enabled := range w.enabled {
		if enabled == model {
			return nil
		}
	}
	return errors.New("the connection does not enable " + model)
}

// agentRequest is the person's message every agent check's response answers.
const agentRequest = "Check the expense pack's receipts and totals, and tell me what is missing."

// run is one response on the adapter the caller binds: an interactive conversation on the agent
// capability, its children on the same binding.
func (w *agentWorld) run(ctx context.Context, with func(Adapter) Adapter, inner Adapter, script AgentScript, chat *runcontext.ChatContext, execution *contract.Checkpoint, configure func(*Run)) {
	if chat == nil {
		chat = runcontext.NewChatContext("chat-agent")
	}
	input := &runcontext.Input{CurrentRequest: &runcontext.CurrentRequest{Message: agentRequest, Input: agentRequest}, Chat: chat,
		Checkpoint: w.checkpoint, Execution: execution,
		Documents: []runcontext.Document{{ID: "receipts", Kind: "artifact", Label: "The trip's receipts", Value: runcontext.String("Taxi 18 USD; hotel 240 USD; dinner 52 USD.")}}}
	if w.gate.Phase().Kind == gate.PhasePlan {
		input.Phase = "plan"
	}
	off := false
	run := &Run{ModelName: parentModel, Gate: w.gate.Calls(), Tools: w.gate.Phase().Offer(w.offer).Tools, Prompt: agentRequest,
		AllowConversation: true, Interactive: true, Review: &off, Context: input, HostTools: w.hostTools(), Children: w.children(), Emit: w.emit,
		Agent: &adapter.Agent{Model: parentModel, Run: w.watch.wrap(w.host.Bind(script)), Enabled: w.admit},
		Models: func() (runcontext.RunModels, error) {
			three := 3
			return runcontext.RunModels{Critic: criticModel, Child: childModel, ConcurrentRequests: &three}, nil
		}}
	if configure != nil {
		configure(run)
	}
	seal := sealNetwork()
	_ = with(inner).Run(ctx, run)
	if reached := seal.lift(); len(reached) > 0 {
		w.emit(contract.RunEvent{Error: &contract.RunEventError{Type: "error", Message: "the adapter reached for http.DefaultTransport: " + strings.Join(reached, ", ")}})
	}
}

// say is a step that answers.
func say(text string) AgentStep { return AgentStep{Say: text} }

// call is a step that calls a tool with arguments.
func call(tool string, args any) AgentStep {
	encoded, _ := json.Marshal(args)
	return AgentStep{Tool: tool, Arguments: encoded}
}

// delegation is a delegate_tasks call's arguments.
func delegation(tasks ...map[string]any) map[string]any { return map[string]any{"tasks": tasks} }

func task(title, objective, profile string, references ...string) map[string]any {
	out := map[string]any{"title": title, "objective": objective, "profile": profile}
	if len(references) > 0 {
		out["references"] = references
	}
	return out
}

// isContinuation is whether a parent's request continues the response after a batch.
func isContinuation(request adapter.AgentRequest) bool {
	return strings.Contains(request.Prompt, turn.Continuation)
}

// childTitle is the title a child's brief names, read from its objective.
func objectiveOf(request adapter.AgentRequest) string {
	var held struct {
		Objective string `json:"objective"`
	}
	for _, line := range strings.Split(request.Prompt, "\n") {
		if strings.HasPrefix(line, "{") && json.Unmarshal([]byte(line), &held) == nil && held.Objective != "" {
			return held.Objective
		}
	}
	return ""
}

// reviewReport is a child's answer in the review profile's shape.
const reviewReport = "The receipts match the claimed amounts.\n\n```json\n{\"report\":{\"summary\":\"The receipts match the claimed amounts.\",\"findings\":[{\"text\":\"Each receipt matches its claim.\",\"severity\":\"gap\"}],\"citations\":[],\"openQuestions\":[],\"confidence\":\"high\",\"notDone\":[]}}\n```"
