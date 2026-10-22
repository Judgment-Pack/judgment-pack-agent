package eino

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/turn"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/wire"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
	"github.com/Judgment-Pack/judgment-pack-agent/go/relay"
)

// ID is the adapter's engine id, as a host's configuration names it and as the schema rules name
// what it shows the model (wire.EngineID).
const ID = wire.EngineID

// New is the adapter.
func New() adapter.Adapter { return adapter.Func(Run) }

// errWaiting ends a turn that asked the person a question (the question pause, ADR-0003 decision
// 2, which ADR-0004 decision 4 keeps): the turn ends, unanswered, and no framework interrupt is used.
var errWaiting = errors.New("waiting for input")

// runState is one run: what the host handed it, the context the boundary opened, and everything
// the turn holds between requests.
type runState struct {
	run          *adapter.Run
	family       wire.EndpointKind
	capabilities wire.ModelCapabilities
	opened       *runcontext.Run
	sc           *runcontext.SessionContext
	session      runcontext.Session
	boundary     *runcontext.Boundary
	side         sideTable
	slot         *wire.ThinkingSlot
	tier         *wire.Value
	placement    wire.StatePlacement
	call         wire.ModelCall
	listener     *listener
	nativeTools  bool

	// currentPhase is the phase a request's record names: work, synthesis or review.
	currentPhase string

	mu       sync.Mutex
	produced int
	ended    bool

	// The run's own clock and its pause for input, beneath the caller's context.
	overTime      bool
	awaitingInput bool
	pause         context.CancelFunc
}

// Run is the adapter: one run of the contract's turn, its loop on Eino's ChatModelAgent.
func Run(ctx context.Context, run *adapter.Run) error {
	r := &runState{run: run, family: wire.EndpointKind(run.Wire)}
	host := func(wire.EndpointKind, string) *wire.Value {
		if run.Capabilities == nil {
			return wire.Obj()
		}
		encoded, err := json.Marshal(run.Capabilities)
		if err != nil {
			return wire.Obj()
		}
		value, err := wire.Parse(encoded)
		if err != nil {
			return wire.Obj()
		}
		return value
	}
	capabilities, err := wire.CapabilitiesFor(r.family, run.ModelName, host, nil)
	if err != nil {
		r.emitAll(runcontext.FailedToOpen(err))
		return nil
	}
	r.capabilities = capabilities
	r.nativeTools = capabilities.NativeToolCalls

	named := runcontext.RunModels{Critic: run.ModelName, Child: run.ModelName}
	if run.Agent != nil {
		named.Critic = run.Agent.Model
		named.Child = run.Agent.Model
	}
	if run.Models != nil {
		var err error
		named, err = run.Models()
		if err != nil {
			r.emitAll(runcontext.FailedToOpen(err))
			return nil
		}
	}
	if named.Critic == "" {
		named.Critic = run.ModelName
	}
	if named.Child == "" {
		named.Child = run.ModelName
	}
	session := runcontext.Session{
		Prompt:            run.Prompt,
		TestPrompt:        run.TestPrompt,
		HostTools:         append([]runcontext.HostTool{}, run.HostTools...),
		Context:           run.Context,
		Purpose:           run.Purpose,
		AllowConversation: run.AllowConversation,
		Interactive:       run.Interactive,
		CallTool:          r.gatedCall,
		Model:             &runcontext.ModelSession{Family: run.Wire, Model: run.ModelName, Budget: budgetOf(capabilities), ToolSchemas: string(capabilities.ToolSchemas), Thinking: run.Thinking},
		Host:              run.Host,
		Skills:            run.Skills,
		Subagents:         run.Subagents, ChildDefinition: run.ChildDefinition,
		// ADR-0010: the critic's switch, as the host passes it.
		Critic: run.Critic,
		Policy: run.Policy, PolicyProject: run.PolicyProject, TaskClass: run.TaskClass, PaceUsed: run.PaceUsed, PolicyLegacyModels: &named, PolicyFamilies: adapter.PolicyFamilies(run, named), PolicyConnections: append(adapter.PolicyConnectionIDs(run), named.Critic, named.Child), PolicyRequests: run.PolicyRequests,
		CriticProject:     run.CriticProject,
		CriticReasoning:   run.CriticReasoning,
		ExistingPack:      run.ExistingPack,
		ReviewerAssigned:  run.ReviewerAssigned,
		AdversarialReview: run.Review,
	}
	for _, tool := range run.Tools {
		session.Tools = append(session.Tools, runcontext.RuntimeTool{Name: tool.Name})
	}
	opened, err := runcontext.ContextualSession(ctx, session)
	if err != nil {
		r.emitAll(runcontext.FailedToOpen(err))
		return nil
	}
	r.opened = opened
	r.sc = opened.Context
	local := *run
	r.sc.ProviderFamilies = adapter.PolicyFamilies(run, named)
	if err := adapter.ApplyPolicy(&local, r.sc, named); err != nil {
		r.sc.Close()
		r.emitAll(runcontext.FailedToOpen(err))
		return nil
	}
	r.run = &local
	r.family = wire.EndpointKind(local.Wire)
	var override *wire.Value
	if local.Capabilities != nil {
		raw, _ := json.Marshal(local.Capabilities)
		override, _ = wire.Parse(raw)
	}
	capabilities, err = wire.CapabilitiesFor(r.family, local.ModelName, nil, override)
	if err != nil {
		r.sc.Close()
		r.emitAll(runcontext.FailedToOpen(err))
		return nil
	}
	r.capabilities = capabilities
	r.nativeTools = capabilities.NativeToolCalls
	r.session = opened.Session
	r.boundary = runcontext.NewBoundary(opened)
	defer r.boundary.Close()
	defer r.boundary.GuardGateNotices(run.Gate)()
	r.sc.RecordProvider("author")
	r.sc.Execution.UseConnection(r.run.Connection("author"), "author")
	r.emitAll(r.boundary.Open())

	r.slot = wire.OpenThinking(wire.ThinkingTier(local.Thinking), r.family, &capabilities.Reasoning, capabilities.Tiers, capabilities.FallbackTiers)
	r.placement = statePlacement(r.family, wire.ThinkingTier(local.Thinking), capabilities)
	r.listener = &listener{r: r}

	err = r.drive(ctx)
	if err != nil {
		cause := err
		switch {
		case ctx.Err() != nil || failure.IsCancelled(cause) && !r.overTime:
			r.boundary.Stopped()
		case errors.Is(cause, runcontext.ErrExecutionPaused):
			if state, snapErr := r.sc.Snapshot("paused"); snapErr == nil {
				r.deliver(contract.RunEvent{BudgetPaused: &contract.RunEventBudgetPaused{Type: "budget_paused", Checkpoint: state}})
			}
		default:
			r.deliver(failure.Event(cause, "model"))
		}
	}
	r.deliver(contract.RunEvent{End: &contract.RunEventEnd{Type: "end"}})
	return nil
}

// budgetOf is the capability record's window and output cap, where it carries both.
func budgetOf(capabilities wire.ModelCapabilities) *runcontext.ModelBudget {
	if capabilities.ContextWindowTokens == nil || capabilities.OutputTokens == nil {
		return nil
	}
	return &runcontext.ModelBudget{ContextWindowTokens: *capabilities.ContextWindowTokens, OutputTokens: *capabilities.OutputTokens}
}

// statePlacement is where the execution state sits after the first request (statePlacement in
// engines/vercel/loop.ts, budget.json#/$defs/stateLocation): the system tail for an endpoint that
// keeps a strict turn order, the record's placement where it names one, and otherwise the system
// tail on Anthropic with a tier on and a trailing user message everywhere else.
func statePlacement(family wire.EndpointKind, tier wire.ThinkingTier, capabilities wire.ModelCapabilities) wire.StatePlacement {
	if capabilities.StrictAlternation != nil && *capabilities.StrictAlternation {
		return "system-tail"
	}
	if capabilities.StatePlacement != "" {
		return capabilities.StatePlacement
	}
	if family == wire.Anthropic && tier != "" && tier != wire.TierOff {
		return "system-tail"
	}
	return "trailing-user"
}

// emitAll hands events to the host, in order.
func (r *runState) emitAll(events []contract.RunEvent) {
	for _, event := range events {
		r.run.Emit(event)
	}
}

// deliver puts one event through the boundary (runcontext.Boundary) and hands what it answers to
// the host, in order. Nothing is delivered after the end.
func (r *runState) deliver(event contract.RunEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ended {
		return
	}
	if event.MessageProgress == nil || event.MessageProgress.Text != "" {
		r.produced++
	}
	out, stop := r.boundary.Event(event)
	for _, held := range out {
		r.run.Emit(held)
	}
	if stop {
		r.ended = true
	}
}

// turnTimeFailure is the turn's wall clock passing, as a failure the catalogue can say.
func (r *runState) turnTimeFailure() error {
	limit := time.Duration(r.sc.Policy.TurnMilliseconds) * time.Millisecond
	return failure.Authored("timeout", "The turn reached its wall-clock limit of "+turn.LimitText(limit)+".", "model")
}

// modelCall is the relay capability as the wire takes it, each request claimed against the
// response's allowance first (contextualSession's model wrapper) and sent again once where a
// refusal earns it (retryingCall).
func (r *runState) modelCall(phase func() string) wire.ModelCall {
	claimed := func(ctx context.Context, suffix string, request wire.ModelRequest) (*http.Response, error) {
		role := "author"
		if phase() == "review" {
			role = "critic"
		}
		if r.run.Connection != nil {
			r.sc.Execution.UseConnection(r.run.Connection(role), role)
		}
		if request.Method != http.MethodGet {
			if r.run.Connection != nil && r.run.Connection(role) != r.sc.ResolvedPolicy["models."+role+".provider"].Value {
				binding := r.run.PolicyConnections[r.run.Connection(role)]
				var err error
				tier, _ := r.sc.ResolvedPolicy["models."+role+".reasoning"].Value.(string)
				suffix, request, err = fallbackRequest(binding, tier, suffix, request)
				if err != nil {
					return nil, err
				}
			}
			if err := r.sc.ClaimRequest(ctx, request.Body); err != nil {
				return nil, err
			}
		}
		return r.run.Model(ctx, suffix, relay.Request{Method: request.Method, Headers: request.Headers, Body: request.Body})
	}
	return wire.RetryingCall(claimed, wire.RetryOptions{
		OnRefused: func(refused wire.RefusedAttempt) error {
			var waitMs *int
			if refused.Wait > 0 {
				held := int(refused.Wait / time.Millisecond)
				waitMs = &held
			}
			r.sc.Execution.RecordRefusal(phase(), refused.Attempt, refused.Failure, waitMs)
			if waitMs != nil {
				r.deliver(contract.RunEvent{Retry: &contract.RunEventRetry{Type: "retry", Attempt: int64(refused.Attempt)}})
			}
			return nil
		},
		OnAttempt: func(attempt int) { r.sc.Execution.BeginAttempt(attempt) },
		// The last request of the allowance is the turn's tool-free final answer: a retry never takes it.
		MayRetry: func() bool { return r.sc.Execution.Remaining() > 1 },
		OnExhausted: func(code string) {
			allowed := false
			classes, _ := r.sc.ResolvedPolicy["failover.classes"].Value.([]any)
			for _, held := range classes {
				if held == code {
					allowed = true
				}
			}
			if !allowed {
				return
			}
			role := "author"
			if phase() == "review" {
				role = "critic"
			}
			reason := "failover refused"
			r.deliver(contract.RunEvent{Guardrail: &contract.RunEventGuardrail{Type: "guardrail", Tool: "models." + role + ".fallback", Action: "refused", Detail: "The one-switch limit for this request is exhausted.", Reason: &reason}})
		},
		Fallback: func(reason string) (string, string, bool) {
			if r.run.Failover == nil {
				return "", "", false
			}
			role := "author"
			if phase() == "review" {
				role = "critic"
			}
			return r.run.Failover(role, reason)
		},
		OnSwitch: func(from, to, reason string) error {
			role := "author"
			if phase() == "review" {
				role = "critic"
			}
			r.deliver(contract.RunEvent{ConnectionSwitched: &contract.RunEventConnectionSwitched{Type: "connection_switched", Role: role, From: from, To: to, Reason: reason}})
			return nil
		},
	})
}

// endpointSentence is the endpoint's own sentence out of a refusal's body, read only for the
// bounded capability-negotiation rules; never a user-facing failure.
func endpointSentence(body string) string {
	if len(body) > 16384 {
		return ""
	}
	var value map[string]any
	if json.Unmarshal([]byte(body), &value) != nil {
		return ""
	}
	switch held := value["error"].(type) {
	case string:
		return held
	case map[string]any:
		if message, ok := held["message"].(string); ok {
			return message
		}
	}
	if message, ok := value["message"].(string); ok {
		return message
	}
	return ""
}

// refusalOf is the status and the endpoint's sentence out of a refusal, or false.
func refusalOf(cause error) (int, string, bool) {
	var refusal *wire.RelayRefusal
	if !errors.As(cause, &refusal) {
		return 0, "", false
	}
	return refusal.Status, endpointSentence(refusal.Body), true
}

// schemaRefusal is the refusal a served schema earned, where the endpoint takes a schema subset and
// names a keyword this run sent.
func (r *runState) schemaRefusal(cause error) error {
	if r.capabilities.ToolSchemas != "gemini-subset" {
		return nil
	}
	status, message, ok := refusalOf(cause)
	if !ok {
		return nil
	}
	var tools []wire.McpTool
	for _, tool := range r.run.Tools {
		description := ""
		if tool.Description != nil {
			description = *tool.Description
		}
		tools = append(tools, mcpTool(tool.Name, description, tool.InputSchema))
	}
	shown, err := wire.SchemasShown(string(r.capabilities.ToolSchemas), tools)
	if err != nil {
		return nil
	}
	keyword := wire.RefusedSchemaKeyword(status, message, shown)
	if keyword == "" {
		return nil
	}
	return failure.Authored("invalid-request", wire.RefusedSchemaSentence(keyword), "model")
}

// drive is the session, with the one retry a tier refusal earns, only before anything was
// delivered, under the turn's wall clock.
func (r *runState) drive(ctx context.Context) error {
	turnCtx, stop := context.WithTimeout(ctx, time.Duration(r.sc.Policy.TurnMilliseconds)*time.Millisecond)
	defer stop()
	execution, pause := context.WithCancel(turnCtx)
	defer pause()
	r.pause = pause
	if _, err := turn.ReadCriticCases(casesOf(r.run.Cases)); err != nil {
		return failure.Authored("configuration", err.Error(), "context")
	}
	for _, host := range r.run.HostTools {
		for _, served := range r.run.Tools {
			if host.Name == served.Name {
				return failure.Authored("configuration", "host tool "+host.Name+" is named like a tool the runtime serves; nothing was run", "desk")
			}
		}
	}
	var tools []wire.McpTool
	for _, tool := range r.run.Tools {
		description := ""
		if tool.Description != nil {
			description = *tool.Description
		}
		tools = append(tools, mcpTool(tool.Name, description, tool.InputSchema))
	}
	for _, host := range r.run.HostTools {
		tools = append(tools, mcpTool(host.Name, host.Description, host.InputSchema))
	}
	for _, notice := range wire.NarrowingEvents(ID, r.family, tools) {
		r.deliver(notice)
	}
	if opened := r.slot.Opening(); opened != nil {
		r.deliver(*opened)
	}
	r.currentPhase = "work"
	r.call = r.modelCall(func() string { return r.currentPhase })
	for attempt := 1; ; attempt++ {
		r.mu.Lock()
		r.produced = 0
		r.mu.Unlock()
		err := r.runOnce(execution, ctx)
		if err == nil {
			return nil
		}
		if r.awaitingInput || errors.Is(err, errWaiting) {
			return nil
		}
		if turnCtx.Err() != nil && ctx.Err() == nil {
			r.overTime = true
			reason := r.turnTimeFailure().Error() + " Completed work is kept. Continuing takes a grant of more requests, so a run already at the " +
				strconv.Itoa(runcontext.MaxRequestAllowance) + "-request limit cannot be continued; start a new response instead."
			r.sc.Execution.State.Reason = &reason
			return runcontext.ErrExecutionPaused
		}
		status, message, refused := refusalOf(err)
		r.mu.Lock()
		produced := r.produced
		r.mu.Unlock()
		if !refused || produced > 0 || attempt >= 3 {
			if held := r.schemaRefusal(err); held != nil {
				return held
			}
			return err
		}
		verdict := r.slot.Refused(status, message)
		if verdict.Kind == "other" {
			if held := r.schemaRefusal(err); held != nil {
				return held
			}
			return err
		}
		if verdict.Kind == "degrade" && verdict.Event != nil {
			r.deliver(*verdict.Event)
		}
	}
}

// heldRow is a call's failure and images, kept by the call it answers.
func heldRow(result ran) held { return held{isError: result.result.Err(), images: result.images} }

// outputTokens is the output reserve the budget table states, read on each request.
func (r *runState) outputTokens() int { return r.sc.Policy.OutputTokens }

// reasoningField is the member reasoning streams in on the OpenAI-compatible wire, where the record
// pins one.
func (r *runState) reasoningField() wire.ReasoningField {
	if r.family != wire.OpenAICompatible {
		return ""
	}
	return r.capabilities.StreamingQuirks.ReasoningField
}

// encodePathSegment encodes one UTF-8 value as an RFC 3986 path segment, leaving only
// ASCII letters, digits, hyphen, dot, underscore and tilde bare.
func encodePathSegment(value string) string {
	const hex = "0123456789ABCDEF"
	var encoded strings.Builder
	encoded.Grow(len(value))
	for _, b := range []byte(value) {
		if b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '-' || b == '.' || b == '_' || b == '~' {
			encoded.WriteByte(b)
		} else {
			encoded.WriteByte('%')
			encoded.WriteByte(hex[b>>4])
			encoded.WriteByte(hex[b&0x0f])
		}
	}
	return encoded.String()
}

// imageBounds are the images one request carries, at most.
func (r *runState) imageBounds() wire.ImageBounds {
	return wire.ImageBounds{Count: r.sc.Policy.ImagesPerRequest, Bytes: r.sc.Policy.ImageBytesPerRequest}
}

// fallbackRequest rebuilds model-dependent fields before prompt accounting and dispatch.
func fallbackRequest(binding adapter.PolicyConnection, tier, suffix string, request wire.ModelRequest) (string, wire.ModelRequest, error) {
	body, err := wire.Parse(request.Body)
	if err != nil {
		return suffix, request, err
	}
	var overrides *wire.Value
	if binding.Capabilities != nil {
		raw, _ := json.Marshal(binding.Capabilities)
		overrides, err = wire.Parse(raw)
		if err != nil {
			return suffix, request, err
		}
	}
	caps, err := wire.CapabilitiesFor(wire.EndpointKind(binding.Wire), binding.ModelName, nil, overrides)
	if err != nil {
		return suffix, request, err
	}
	strip := func(value *wire.Value, keys []string) *wire.Value {
		out := wire.Obj()
		for _, field := range value.Members() {
			omit := false
			for _, key := range keys {
				if key == field.Name {
					omit = true
				}
			}
			if !omit {
				out.Set(field.Name, field.Value)
			}
		}
		return out
	}
	body = strip(body, wire.TierMembers)
	config := body.Get("generationConfig")
	if config.IsObject() {
		config = strip(config, []string{"thinkingConfig"})
		body.Set("generationConfig", config)
	}
	var settings *wire.WireThinking
	if tier != "" && caps.Reasoning.Kind != "none" {
		settings = wire.WireFor(wire.ThinkingTier(tier), wire.DialectFor(caps.Reasoning.Kind), caps.Tiers, caps.FallbackTiers)
	}
	if binding.Wire == "gemini" {
		if at := strings.Index(suffix, "models/"); at >= 0 {
			if end := strings.Index(suffix[at:], ":"); end >= 0 {
				suffix = suffix[:at] + "models/" + encodePathSegment(binding.ModelName) + suffix[at+end:]
			}
		}
		if !config.IsObject() {
			config = wire.Obj()
			body.Set("generationConfig", config)
		}
		if settings != nil {
			for _, field := range settings.Members.Members() {
				config.Set(field.Name, field.Value)
			}
		}
	} else {
		body.Set("model", wire.Str(binding.ModelName))
		if binding.Wire == "anthropic" {
			max := wire.ResponseTokens
			if caps.OutputTokens != nil {
				max = *caps.OutputTokens
			}
			body.Set("max_tokens", wire.Int(max))
		}
		if settings != nil {
			for _, field := range settings.Members.Members() {
				body.Set(field.Name, field.Value)
			}
		}
	}
	request.Body = body.JSON()
	return suffix, request, nil
}
