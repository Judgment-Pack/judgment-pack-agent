package adapter

import (
	"context"
	"encoding/json"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/internal/physicaldispatch"
	"net/http"
	"sync"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/turn"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/wire"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
	"github.com/Judgment-Pack/judgment-pack-agent/go/relay"
)

// RequestAgent is the admitted agent capability visible to request builders.
// It deliberately has no Effort field; Reasoning is the only source of one.
type RequestAgent struct {
	family    string
	tiers     map[string]string
	model     string
	run       AgentRun
	enabled   func(context.Context, string) error
	delegated string
	begin     func(context.Context, AgentRequest) error
}

func requestAgent(agent *Agent) *RequestAgent {
	if agent == nil {
		return nil
	}
	tiers := map[string]string{}
	for key, value := range agent.Tiers {
		tiers[key] = value
	}
	return &RequestAgent{family: agent.Family, tiers: tiers, model: agent.Model, run: agent.Run, enabled: agent.Enabled, delegated: agent.Delegated, begin: agent.Begin}
}
func (a *RequestAgent) Name() string { return a.model }
func (a *RequestAgent) TierMap() map[string]string {
	copy := map[string]string{}
	for key, value := range a.tiers {
		copy[key] = value
	}
	return copy
}
func (a *RequestAgent) DelegatedModel() string { return a.delegated }
func (a *RequestAgent) Run(ctx context.Context, r AgentRequest, c AgentCallbacks) error {
	return a.run(ctx, r, c)
}
func (a *RequestAgent) HasBegin() bool                                  { return a.begin != nil }
func (a *RequestAgent) Begin(ctx context.Context, r AgentRequest) error { return a.begin(ctx, r) }

// AdmissionAgent copies a host capability for the one pre-resolution handoff
// used by the delegation wrapper. Request builders receive RequestAgent instead.
func AdmissionAgent(agent *Agent) *Agent {
	if agent == nil {
		return nil
	}
	held := *agent
	return &held
}

// AdmissionReasoning is the raw host input consumed only by policy resolution.
type AdmissionReasoning struct{ Thinking, Effort, Critic string }

// ReadAdmissionReasoning is the sole raw reasoning read before policy resolution.
func ReadAdmissionReasoning(run *Run) AdmissionReasoning {
	held := AdmissionReasoning{Thinking: run.Thinking, Critic: run.CriticReasoning}
	if run.Agent != nil {
		held.Effort = run.Agent.Effort
	}
	return held
}

// Admit applies the agent model admission without manufacturing an effort.
func (a *RequestAgent) Admit(ctx context.Context, request AgentRequest) (AgentRequest, error) {
	return (&Agent{Family: a.family, Tiers: a.tiers, Model: a.model, Run: a.run, Enabled: a.enabled,
		Delegated: a.delegated, Begin: a.begin}).Admit(ctx, request)
}

// RequestPolicyConnection is a host binding with its raw reasoning preference removed.
type RequestPolicyConnection struct {
	physicalModel   *physicaldispatch.Model
	modelName, wire string
	capabilities    *contract.CapabilityEndpointCapabilities
	model           relay.Call
	agent           *RequestAgent
}

func (b RequestPolicyConnection) ModelID() string                { return b.modelName }
func (b RequestPolicyConnection) Family() string                 { return b.wire }
func (b RequestPolicyConnection) AgentCapability() *RequestAgent { return b.agent }
func (b RequestPolicyConnection) ResolvedCapabilities() (wire.ModelCapabilities, error) {
	var override *wire.Value
	if b.capabilities != nil {
		raw, err := json.Marshal(b.capabilities)
		if err != nil {
			return wire.ModelCapabilities{}, err
		}
		override, _ = wire.Parse(raw)
	}
	return wire.CapabilitiesFor(wire.EndpointKind(b.wire), b.modelName, nil, override)
}

// RequestContext retains only request operations, never host configuration or bindings.
// PreparedRequest contains operations and the admitted tool offer, never a raw Session.
type PreparedRequest struct {
	offered            func(string) bool
	Prompt, TestPrompt string
	AllowConversation  bool
	CallTool           func(context.Context, string, json.RawMessage) (runcontext.ToolResult, error)
	runtime            []runcontext.RuntimeTool
	hosts              []runcontext.HostTool
}

func (p *PreparedRequest) RuntimeTools() []runcontext.RuntimeTool {
	out := []runcontext.RuntimeTool{}
	for _, tool := range p.runtime {
		if p.offered == nil || p.offered(tool.Name) {
			out = append(out, tool)
		}
	}
	for i := range out {
		out[i].Meta = wire.CloneTaxonomy(out[i].Meta)
	}
	return out
}
func (p *PreparedRequest) HostTools() []runcontext.HostTool {
	out := []runcontext.HostTool{}
	for _, tool := range p.hosts {
		if p.offered == nil || p.offered(tool.Name) {
			out = append(out, tool)
		}
	}
	return copyHostTools(out)
}
func copyHostTools(tools []runcontext.HostTool) []runcontext.HostTool {
	out := append([]runcontext.HostTool{}, tools...)
	for i := range out {
		out[i].InputSchema = append(json.RawMessage(nil), out[i].InputSchema...)
		if out[i].Taxonomy != nil {
			data, _ := json.Marshal(out[i].Taxonomy)
			var held runcontext.ToolTaxonomy
			_ = json.Unmarshal(data, &held)
			out[i].Taxonomy = &held
		}
		if out[i].TaxonomyLimit != nil {
			held := *out[i].TaxonomyLimit
			if held.Operations != nil {
				held.Operations = append([]string{}, held.Operations...)
			}
			if held.Effects != nil {
				held.Effects = append([]string{}, held.Effects...)
			}
			out[i].TaxonomyLimit = &held
		}
		if out[i].ContextPolicy != nil {
			copy := *out[i].ContextPolicy
			out[i].ContextPolicy = &copy
		}
		if out[i].Source != nil {
			copy := *out[i].Source
			if copy.Identity != nil {
				data, _ := json.Marshal(copy.Identity)
				var identity contract.ToolOfferIdentity
				_ = json.Unmarshal(data, &identity)
				copy.Identity = &identity
			}
			out[i].Source = &copy
		}
		if out[i].Presentation != nil {
			data, _ := json.Marshal(out[i].Presentation)
			var presentation contract.ToolOfferHostToolPresentation
			_ = json.Unmarshal(data, &presentation)
			out[i].Presentation = &presentation
		}
	}
	return out
}

type RequestContext struct {
	CurrentRequest *runcontext.CurrentRequest
	CurrentState   string
	Phase          string
	Checkpoint     func(context.Context, contract.Checkpoint) error
}

// RequestSession exposes operations explicitly and keeps every resolved input private.
type RequestSession struct {
	offered                        func(string) bool
	physicalModel                  *physicaldispatch.Model
	outputCeiling                  int
	openingProjection              *runcontext.OpeningProjection
	connections                    map[string]RequestPolicyConnection
	failover                       func(role, reason string) (from, to string, ok bool)
	connection                     func(role string) string
	wire, modelName                string
	identityConnection             string
	model                          relay.Call
	agent                          *RequestAgent
	capabilities                   *contract.CapabilityEndpointCapabilities
	tools                          []contract.ToolOfferRuntimeTool
	hostTools                      []runcontext.HostTool
	context                        *RequestContext
	named                          func() (runcontext.RunModels, error)
	child                          bool
	values                         *runcontext.RequestValues
	prepared                       *PreparedRequest
	Gate                           *gate.Calls
	Prompt, TestPrompt             string
	Purpose                        string
	AllowConversation, Interactive bool
	Cases                          []json.RawMessage
	LanguageInstructions           string
	BeforeRuntimeCall              func(context.Context, string) error
	Emit                           func(contract.RunEvent)
}

func (s *RequestSession) OutputCeiling() int { return s.outputCeiling }
func (s *RequestSession) OpeningProjection() *runcontext.OpeningProjection {
	if s.openingProjection == nil {
		return nil
	}
	held := *s.openingProjection
	held.PreviewPaths = append([]string{}, held.PreviewPaths...)
	return &held
}

func (s *RequestSession) ModelID() string                { return s.modelName }
func (s *RequestSession) Family() string                 { return s.wire }
func (s *RequestSession) AgentCapability() *RequestAgent { return s.agent }
func (s *RequestSession) HasConnection() bool            { return s.connection != nil }
func (s *RequestSession) Connection(role string) string  { return s.connection(role) }
func (s *RequestSession) HasFailover() bool              { return s.failover != nil }
func (s *RequestSession) Failover(role, reason string) (string, string, bool) {
	return s.failover(role, reason)
}
func (s *RequestSession) HasModels() bool { return s.named != nil }
func (s *RequestSession) Models() (runcontext.RunModels, error) {
	if s.named != nil {
		return s.named()
	}
	return runcontext.RunModels{Critic: s.modelName, Child: s.modelName}, nil
}
func (s *RequestSession) CallModel(ctx context.Context, suffix string, r relay.Request) (*http.Response, error) {
	return s.model(ctx, suffix, r)
}
func (s *RequestSession) RequestContext() *RequestContext            { return s.context }
func (s *RequestSession) IsChild() bool                              { return s.child }
func (s *RequestSession) Binding(id string) RequestPolicyConnection  { return s.connections[id] }
func (s *RequestSession) Member(name string) runcontext.PolicyMember { return s.values.Member(name) }
func (s *RequestSession) RuntimeTools() []contract.ToolOfferRuntimeTool {
	var tools []contract.ToolOfferRuntimeTool
	if s.tools != nil {
		tools = []contract.ToolOfferRuntimeTool{}
	}
	for _, tool := range s.tools {
		if s.offered == nil || s.offered(tool.Name) {
			tools = append(tools, tool)
		}
	}
	raw, _ := json.Marshal(tools)
	var copy []contract.ToolOfferRuntimeTool
	_ = json.Unmarshal(raw, &copy)
	return copy
}
func (s *RequestSession) OfferedHostTools() []runcontext.HostTool {
	out := []runcontext.HostTool{}
	for _, tool := range s.hostTools {
		if s.offered == nil || s.offered(tool.Name) {
			out = append(out, tool)
		}
	}
	return copyHostTools(out)
}
func (s *RequestSession) ResolvedCapabilities() (wire.ModelCapabilities, error) {
	return (RequestPolicyConnection{modelName: s.modelName, wire: s.wire, capabilities: s.capabilities}).ResolvedCapabilities()
}

func (s *RequestSession) Prepared() *PreparedRequest { return s.prepared }

// NewRequestOperations carries only operational callbacks before policy admission.
func NewRequestOperations(run *Run) *RequestSession {
	return &RequestSession{Gate: run.Gate, Emit: run.Emit, BeforeRuntimeCall: run.BeforeRuntimeCall}
}

// NewRequestSession is the sole raw-Run conversion for request builders.
// The shared RequestValues projection is also used by standalone compaction.
func NewRequestSession(run *Run, members runcontext.PolicyMembers, opened ...*runcontext.Run) *RequestSession {
	values := (*runcontext.RequestValues)(nil)
	if len(opened) > 0 {
		values = opened[0].Context.RequestValues()
	}
	if values == nil {
		values = runcontext.NewRequestValues(members)
	}
	connections := map[string]RequestPolicyConnection{}
	copyCapabilities := func(value *contract.CapabilityEndpointCapabilities) *contract.CapabilityEndpointCapabilities {
		if value == nil {
			return nil
		}
		raw, _ := json.Marshal(value)
		var copy contract.CapabilityEndpointCapabilities
		_ = json.Unmarshal(raw, &copy)
		return &copy
	}
	for id, binding := range run.PolicyConnections {
		connections[id] = RequestPolicyConnection{physicalModel: binding.physicalModel, modelName: binding.ModelName, wire: binding.Wire, capabilities: copyCapabilities(binding.Capabilities), model: binding.Model, agent: requestAgent(binding.Agent)}
	}
	prepared := &PreparedRequest{Prompt: run.Prompt, TestPrompt: run.TestPrompt, AllowConversation: run.AllowConversation, hosts: copyHostTools(run.HostTools)}
	if len(opened) > 0 {
		session := opened[0].Session
		prepared = &PreparedRequest{Prompt: session.Prompt, TestPrompt: session.TestPrompt, AllowConversation: session.AllowConversation, CallTool: session.CallTool, runtime: append([]runcontext.RuntimeTool{}, session.Tools...), hosts: copyHostTools(session.HostTools)}
	}
	identityConnection := ""
	var input *RequestContext
	if run.Context != nil {
		identityConnection = run.Context.Connection
		input = &RequestContext{CurrentRequest: run.Context.CurrentRequest, CurrentState: run.Context.CurrentState, Phase: run.Context.Phase, Checkpoint: run.Context.Checkpoint}
	}
	raw, _ := json.Marshal(run.Tools)
	var tools []contract.ToolOfferRuntimeTool
	_ = json.Unmarshal(raw, &tools)
	projection := run.OpeningProjection
	if projection != nil {
		copy := *projection
		copy.PreviewPaths = append([]string{}, projection.PreviewPaths...)
		// A managed retry rechecks the same detached projection against its actual allowance;
		// it does not ask the trusted host to read or project the opening a second time.
		if project := copy.Project; project != nil {
			var once sync.Once
			var held map[string]string
			var problem error
			copy.Project = func(ctx context.Context, input runcontext.OpeningProjectionInput) (map[string]string, error) {
				once.Do(func() {
					result, err := project(ctx, input)
					problem = err
					held = map[string]string{}
					for key, value := range result {
						held[key] = value
					}
				})
				result := map[string]string{}
				for key, value := range held {
					result[key] = value
				}
				return result, problem
			}
		}
		projection = &copy
	}
	var offered func(string) bool
	if len(opened) > 0 {
		offered = opened[0].Context.RouteOffers
	}
	prepared.offered = offered
	return &RequestSession{offered: offered, physicalModel: run.physicalModel, outputCeiling: run.RequestOutputCeiling, openingProjection: projection, identityConnection: identityConnection, connections: connections, failover: run.Failover, connection: run.Connection, wire: run.Wire, modelName: run.ModelName, model: run.Model, agent: requestAgent(run.Agent), capabilities: copyCapabilities(run.Capabilities), tools: tools, hostTools: copyHostTools(run.HostTools), context: input, named: run.Models, child: run.ChildDefinition != nil, values: values, prepared: prepared, Gate: run.Gate, Prompt: run.Prompt, TestPrompt: run.TestPrompt, Purpose: run.Purpose, AllowConversation: run.AllowConversation, Interactive: run.Interactive, Cases: run.Cases, LanguageInstructions: run.LanguageInstructions, BeforeRuntimeCall: run.BeforeRuntimeCall, Emit: run.Emit}
}

// Reasoning is an admitted role's reasoning value; empty preserves omission.
func (s *RequestSession) Reasoning(role string) string { return s.values.Reasoning(role) }

// PolicyBinding selects an already admitted binding without exposing its raw effort.
func (s *RequestSession) PolicyBinding(role string) (RequestPolicyConnection, error) {
	named, err := s.Models()
	if err != nil {
		return RequestPolicyConnection{}, err
	}
	name := "models." + role + ".provider"
	member := s.Member(name)
	id, _ := member.Value.(string)
	if id == "" {
		id, _ = s.Member("models." + role + ".model").Value.(string)
	}
	current := s.modelName
	if s.agent != nil {
		current = s.agent.model
	}
	if s.identityConnection != "" {
		current = s.identityConnection
	}
	own := RequestPolicyConnection{physicalModel: s.physicalModel, modelName: s.modelName, wire: s.wire, model: s.model, agent: s.agent, capabilities: s.capabilities}
	if id == current {
		if model, ok := s.Member("models." + role + ".model").Value.(string); ok && (model == named.Critic || model == named.Child) {
			own.modelName = model
			if own.agent != nil {
				bound := *own.agent
				bound.model = model
				own.agent = &bound
			}
		}
		return own, nil
	}
	if id == s.modelName || s.agent != nil && id == s.agent.model || id == s.identityConnection {
		return own, nil
	}
	binding, ok := s.connections[id]
	if !ok && (id == named.Critic || id == named.Child) {
		own.modelName = id
		if own.agent != nil {
			agent := *own.agent
			agent.model = id
			own.agent = &agent
		}
		return own, nil
	}
	if !ok {
		return RequestPolicyConnection{}, &runcontext.PolicyRefusal{Member: name, Layer: member.Layer, Detail: "model connection is not available"}
	}
	if (binding.agent != nil) != (s.agent != nil) || s.agent == nil && binding.model == nil {
		return RequestPolicyConnection{}, &runcontext.PolicyRefusal{Member: name, Layer: member.Layer, Detail: "connection requires a different capability or wire family"}
	}
	return binding, nil
}

// AllowsTool projects the existing phase rules; the gate retains execution authority.
func (s *RequestSession) AllowsTool(name string, host *runcontext.HostTool, phase string) bool {
	if phase == "reformat" {
		return false
	}
	if phase == "critic" {
		if host == nil {
			return turn.CriticMayCall(name)
		}
		return turn.CriticMayRead(name)
	}
	if s.context != nil && s.context.Phase == "plan" {
		contains := func(names []string) bool {
			for _, held := range names {
				if held == name {
					return true
				}
			}
			return false
		}
		if host == nil {
			return contains(gate.PlanRuntimeReads)
		}
		return contains(gate.ContextTools) || gate.PlanningTool(host.Declaration())
	}
	return true
}
