package adapter

import (
	"context"
	"encoding/json"

	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
	"github.com/Judgment-Pack/judgment-pack-agent/go/relay"
)

// RequestAgent is the admitted agent capability visible to request builders.
// It deliberately has no Effort field; Reasoning is the only source of one.
type RequestAgent struct {
	Family    string
	Tiers     map[string]string
	Model     string
	Run       AgentRun
	Enabled   func(context.Context, string) error
	Delegated string
	Begin     func(context.Context, AgentRequest) error
}

func requestAgent(agent *Agent) *RequestAgent {
	if agent == nil {
		return nil
	}
	return &RequestAgent{Family: agent.Family, Tiers: agent.Tiers, Model: agent.Model, Run: agent.Run,
		Enabled: agent.Enabled, Delegated: agent.Delegated, Begin: agent.Begin}
}

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
	return (&Agent{Family: a.Family, Tiers: a.Tiers, Model: a.Model, Run: a.Run, Enabled: a.Enabled,
		Delegated: a.Delegated, Begin: a.Begin}).Admit(ctx, request)
}

// RequestPolicyConnection is a host binding with its raw reasoning preference removed.
type RequestPolicyConnection struct {
	ModelName, Wire string
	Capabilities    *contract.CapabilityEndpointCapabilities
	Model           relay.Call
	Agent           *RequestAgent
}

// RequestSession is the internal request boundary. Host policy preferences,
// Thinking, CriticReasoning and Agent.Effort are absent; role reasoning is held
// privately and read only through Reasoning.
type RequestSession struct {
	PolicyConnections    map[string]RequestPolicyConnection
	Failover             func(role, reason string) (from, to string, ok bool)
	Connection           func(role string) string
	Wire, ModelName      string
	Model                relay.Call
	Agent                *RequestAgent
	Capabilities         *contract.CapabilityEndpointCapabilities
	Gate                 *gate.Calls
	Tools                []contract.ToolOfferRuntimeTool
	HostTools            []runcontext.HostTool
	Subagents            []runcontext.Subagent
	ChildDefinition      *runcontext.Subagent
	Skills               []runcontext.Skill
	Prompt, TestPrompt   string
	Purpose              string
	AllowConversation    bool
	Interactive          bool
	Cases                []json.RawMessage
	Context              *runcontext.Input
	LanguageInstructions string
	Models               func() (runcontext.RunModels, error)
	Children             Children
	BeforeRuntimeCall    func(context.Context, string) error
	Emit                 func(contract.RunEvent)
	Host                 runcontext.Host
	reasoning            map[string]string
}

// NewRequestSession is the only conversion from a host Run to request state.
func NewRequestSession(run *Run, members runcontext.PolicyMembers) *RequestSession {
	reasoning := map[string]string{}
	for name, member := range members {
		const prefix, suffix = "models.", ".reasoning"
		if len(name) > len(prefix)+len(suffix) && name[:len(prefix)] == prefix && name[len(name)-len(suffix):] == suffix {
			if value, ok := member.Value.(string); ok {
				reasoning[name[len(prefix):len(name)-len(suffix)]] = value
			}
		}
	}
	connections := make(map[string]RequestPolicyConnection, len(run.PolicyConnections))
	for id, binding := range run.PolicyConnections {
		connections[id] = RequestPolicyConnection{ModelName: binding.ModelName, Wire: binding.Wire,
			Capabilities: binding.Capabilities, Model: binding.Model, Agent: requestAgent(binding.Agent)}
	}
	return &RequestSession{PolicyConnections: connections, Failover: run.Failover, Connection: run.Connection,
		Wire: run.Wire, ModelName: run.ModelName, Model: run.Model, Agent: requestAgent(run.Agent), Capabilities: run.Capabilities,
		Gate: run.Gate, Tools: run.Tools, HostTools: run.HostTools, Subagents: run.Subagents, ChildDefinition: run.ChildDefinition,
		Skills: run.Skills, Prompt: run.Prompt, TestPrompt: run.TestPrompt, Purpose: run.Purpose,
		AllowConversation: run.AllowConversation, Interactive: run.Interactive, Cases: run.Cases, Context: run.Context,
		LanguageInstructions: run.LanguageInstructions, Models: run.Models, Children: run.Children,
		BeforeRuntimeCall: run.BeforeRuntimeCall, Emit: run.Emit, Host: run.Host, reasoning: reasoning}
}

// Reasoning is an admitted role's reasoning value; empty preserves omission.
func (s *RequestSession) Reasoning(role string) string { return s.reasoning[role] }

// PolicyBinding selects an already admitted binding without exposing its raw effort.
func (s *RequestSession) PolicyBinding(members runcontext.PolicyMembers, named runcontext.RunModels, role string) (RequestPolicyConnection, error) {
	name := "models." + role + ".provider"
	member := members[name]
	id, _ := member.Value.(string)
	if id == "" {
		id, _ = members["models."+role+".model"].Value.(string)
	}
	current := s.ModelName
	if s.Agent != nil {
		current = s.Agent.Model
	}
	if s.Context != nil && s.Context.Connection != "" {
		current = s.Context.Connection
	}
	own := RequestPolicyConnection{ModelName: s.ModelName, Wire: s.Wire, Model: s.Model, Agent: s.Agent, Capabilities: s.Capabilities}
	if id == current {
		if model, ok := members["models."+role+".model"].Value.(string); ok && (model == named.Critic || model == named.Child) {
			own.ModelName = model
			if own.Agent != nil {
				bound := *own.Agent
				bound.Model = model
				own.Agent = &bound
			}
		}
		return own, nil
	}
	if id == s.ModelName || s.Agent != nil && id == s.Agent.Model || s.Context != nil && id == s.Context.Connection {
		return own, nil
	}
	binding, ok := s.PolicyConnections[id]
	if !ok && (id == named.Critic || id == named.Child) {
		own.ModelName = id
		if own.Agent != nil {
			agent := *own.Agent
			agent.Model = id
			own.Agent = &agent
		}
		return own, nil
	}
	if !ok {
		return RequestPolicyConnection{}, &runcontext.PolicyRefusal{Member: name, Layer: member.Layer, Detail: "model connection is not available"}
	}
	if (binding.Agent != nil) != (s.Agent != nil) || s.Agent == nil && binding.Model == nil {
		return RequestPolicyConnection{}, &runcontext.PolicyRefusal{Member: name, Layer: member.Layer, Detail: "connection requires a different capability or wire family"}
	}
	return binding, nil
}
