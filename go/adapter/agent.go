package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
)

// The agent capability (ADR-0007; agent.ts in the TypeScript package, closed): a host-owned
// function that runs one invocation of a provider-managed agent. The provider runs its own model
// loop; the adapter hands it a prompt, instructions and the tools it may call by name, and answers
// each tool call through its callbacks. No endpoint address, key, credential or network access
// crosses: the host's function holds its own account and transport, and the adapter holds none.

// AgentTool is one tool an invocation is offered (AgentTool): its name on the agent wire, its
// description and its input schema.
type AgentTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// AgentRequest is one invocation as the host's function receives it (AgentRequest): the prompt, the
// instructions (the system text, ADR-0005's one assembly), the tools offered by name, the effort the
// host's configuration asks for, the phase (author or critic) and the model it runs on. Model is
// always the admitted model (Agent.Admit); Delegated marks a delegated child's invocation and is
// never sent to a provider. Its JSON is what the run's checkpoint digests (ADR-0005).
type AgentRequest struct {
	Prompt       string      `json:"prompt"`
	Instructions string      `json:"instructions"`
	Tools        []AgentTool `json:"tools"`
	Effort       string      `json:"effort,omitempty"`
	Phase        string      `json:"phase"`
	Model        string      `json:"model,omitempty"`
	Delegated    bool        `json:"-"`
}

// AgentEvent is what an invocation reports while it runs (AgentEvent): text so far of one item
// (type text, its id and the text added), a finished message (type message, its id, its text and
// its phase, commentary or final), or the provider's own recovery notice (type retry, its attempt).
type AgentEvent struct {
	Type    string
	ID      string
	Text    string
	Phase   string
	Attempt int
}

// AgentToolCall is one tool call an invocation makes (AgentToolCall): the provider's call id, the
// name it was offered and the arguments as the model wrote them.
type AgentToolCall struct {
	ID        string
	Name      string
	Arguments json.RawMessage
}

// AgentToolAnswer is the answer to one call (AgentToolAnswer): text only, and whether it failed.
type AgentToolAnswer struct {
	Text    string
	IsError bool
}

// AgentCallbacks are the adapter's half of one invocation: each event and each tool call, in the
// order the provider makes them. An error from Tool ends the invocation: the host's function stops
// the provider's run and returns.
type AgentCallbacks struct {
	Event func(ctx context.Context, event AgentEvent) error
	Tool  func(ctx context.Context, call AgentToolCall) (AgentToolAnswer, error)
}

// AgentRun is the host's provider-run function: one invocation, from admission on its account to
// the provider's end. It returns only once the invocation has released the account it ran on (the
// release the delegation coordinator waits for before a child starts), and once ctx is done it
// stops the provider's run and returns. It answers an error wrapping ErrBusy where the account
// refused the invocation at admission because another was active, nothing having run; any other
// error is an admitted invocation's outcome, which is never sent again.
type AgentRun func(ctx context.Context, request AgentRequest, callbacks AgentCallbacks) error

// ErrBusy is the documented not-admitted refusal (failure class busy, outcome not-started): the
// provider's account was running another invocation and admitted none. An invocation refused so
// is sent again at most twice, after 250 ms and 500 ms (canRetryAdmission); nothing else is.
var ErrBusy error = failure.Err("busy", "agent", "not-started")

// IsBusy reports an error that is, or wraps, a busy refusal at admission.
func IsBusy(err error) bool {
	if errors.Is(err, ErrBusy) {
		return true
	}
	held := failure.Normalize(err, "agent", time.Time{})
	return held.Code == "busy" && held.Outcome == "not-started"
}

// Agent is the agent capability a host binds a run to: the model it is bound to, the effort, the
// host's provider-run function and its enabled-model admission. The delegation coordinator hands a
// delegated child's run a copy with Delegated and Begin set.
type Agent struct {
	// Model is the model the host bound the capability to: the run's own.
	Model string
	// Effort is the reasoning effort the host's configuration asks for, or empty.
	Effort string
	// Run is the host's provider-run function.
	Run AgentRun
	// Enabled is the host's enabled-model admission (the connection's enabled models), asked before
	// every invocation, the parent's, a critic's and a child's, with the model it would run on; an
	// error refuses the invocation before it is dispatched. Nil admits every model Admit passes.
	Enabled func(ctx context.Context, model string) error
	// Delegated is the delegation coordinator's, on a delegated child's run: the model the host
	// named for children. Empty on every other run.
	Delegated string
	// Begin is the delegation coordinator's: asked once per invocation, after admission and before
	// dispatch, to count it against the response's invocations and write the durable checkpoint;
	// an error refuses the invocation. A host leaves it nil.
	Begin func(ctx context.Context, request AgentRequest) error
}

var modelName = regexp.MustCompile("^[^\r\n\x00]{1,128}$")

// Admit is the admission every invocation passes (bindAgentRun's rule in agentTransport.ts, and the
// host's enabled models): the model an invocation runs on is the bound model, or, on a critic's
// invocation or a delegated child's only, the model the host named for them; then the host's
// Enabled. It answers the request with Model set to the model admitted, or a configuration failure
// (source agent, outcome not-started) with nothing dispatched.
func (a *Agent) Admit(ctx context.Context, request AgentRequest) (AgentRequest, error) {
	chosen := a.Model
	if request.Model != "" && request.Model != a.Model {
		if request.Phase != "critic" && !request.Delegated {
			return request, failure.Err("configuration", "agent", "not-started")
		}
		chosen = request.Model
	}
	if !modelName.MatchString(chosen) {
		return request, failure.Err("configuration", "agent", "not-started")
	}
	if a.Enabled != nil {
		if err := a.Enabled(ctx, chosen); err != nil {
			if failure.IsCancelled(err) {
				return request, err
			}
			return request, failure.Err("configuration", "agent", "not-started")
		}
	}
	request.Model = chosen
	return request, nil
}
