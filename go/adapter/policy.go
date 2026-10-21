package adapter

import (
	"context"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/relay"
	"net/http"
	"sort"
	"strings"
)

// PolicyConnection is a host-bound connection. Policy names IDs, never URLs or credentials.
type PolicyConnection struct {
	ModelName, Wire string
	Capabilities    *contract.CapabilityEndpointCapabilities
	Model           relay.Call
	Agent           *Agent
}

func PolicyConnectionIDs(run *Run) []string {
	ids := []string{run.ModelName}
	if run.Agent != nil {
		ids = append(ids, run.Agent.Model)
	}
	if run.Context != nil && run.Context.Connection != "" {
		ids = append(ids, run.Context.Connection)
	}
	extra := []string{}
	for id := range run.PolicyConnections {
		extra = append(extra, id)
	}
	sort.Strings(extra)
	ids = append(ids, extra...)
	return ids
}

// PolicyBinding selects a resolved role using the host's actual capability bindings.
func PolicyBinding(run *Run, members runcontext.PolicyMembers, named runcontext.RunModels, role string) (PolicyConnection, error) {
	name := "models." + role + ".provider"
	member := members[name]
	id, _ := member.Value.(string)
	if id == "" {
		id, _ = members["models."+role+".model"].Value.(string)
	}
	current := run.ModelName
	if run.Agent != nil {
		current = run.Agent.Model
	}
	if run.Context != nil && run.Context.Connection != "" {
		current = run.Context.Connection
	}
	own := PolicyConnection{ModelName: run.ModelName, Wire: run.Wire, Model: run.Model, Agent: run.Agent, Capabilities: run.Capabilities}
	if id == current {
		if model, ok := members["models."+role+".model"].Value.(string); ok && (model == named.Critic || model == named.Child) {
			// A subordinate model stays on the selected connection even if its
			// name collides with another host connection ID.
			own.ModelName = model
			if own.Agent != nil {
				bound := *own.Agent
				bound.Model = model
				own.Agent = &bound
			}
		}
		return own, nil
	}
	if id == run.ModelName || run.Agent != nil && id == run.Agent.Model || run.Context != nil && id == run.Context.Connection {
		return own, nil
	}
	binding, ok := run.PolicyConnections[id]
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
		return PolicyConnection{}, &runcontext.PolicyRefusal{Member: name, Layer: member.Layer, Detail: "model connection is not available"}
	}
	if (binding.Agent != nil) != (run.Agent != nil) || run.Agent == nil && binding.Model == nil {
		return PolicyConnection{}, &runcontext.PolicyRefusal{Member: name, Layer: member.Layer, Detail: "connection requires a different capability or wire family"}
	}
	return binding, nil
}

// ApplyPolicy selects the admitted author's binding and routes the critic through its binding.
// A wire-family or capability change this adapter cannot serve is refused before any request.
func ApplyPolicy(run *Run, sc *runcontext.SessionContext, named runcontext.RunModels) error {
	for name := range sc.ResolvedPolicy {
		if strings.HasPrefix(name, "models.") && strings.HasSuffix(name, ".provider") {
			role := strings.TrimSuffix(strings.TrimPrefix(name, "models."), ".provider")
			if _, err := PolicyBinding(run, sc.ResolvedPolicy, named, role); err != nil {
				return err
			}
		}
	}
	author, err := PolicyBinding(run, sc.ResolvedPolicy, named, "author")
	if err != nil {
		return err
	}
	critic, err := PolicyBinding(run, sc.ResolvedPolicy, named, "critic")
	if err != nil {
		return err
	}
	named.Critic = critic.ModelName
	if critic.Agent != nil {
		named.Critic = critic.Agent.Model
	}
	capacity := int(sc.ResolvedPolicy["concurrency.requests"].Value.(float64))
	named.ConcurrentRequests = &capacity
	run.Models = func() (runcontext.RunModels, error) { return named, nil }
	tier, _ := sc.ResolvedPolicy["models.author.reasoning"].Value.(string)
	if run.Agent != nil {
		bound := *author.Agent
		bound.Effort = tier
		send := bound.Run
		bound.Run = func(ctx context.Context, request AgentRequest, callbacks AgentCallbacks) error {
			if request.Phase == "critic" {
				return critic.Agent.Run(ctx, request, callbacks)
			}
			return send(ctx, request, callbacks)
		}
		run.Agent = &bound
	} else {
		run.ModelName = author.ModelName
		run.Wire = author.Wire
		run.Capabilities = author.Capabilities
		run.Thinking = tier
		run.Model = func(ctx context.Context, suffix string, request relay.Request) (*http.Response, error) {
			if sc.CriticReading {
				return critic.Model(ctx, suffix, request)
			}
			return author.Model(ctx, suffix, request)
		}
	}
	return nil
}

// PolicyFamilies describes only bindings supplied by the host, never model output.
func PolicyFamilies(run *Run, named runcontext.RunModels) map[string]string {
	family := func(w string, a *Agent) string {
		if a != nil {
			if a.Family != "" {
				return a.Family
			}
			return "codex"
		}
		if w == "openai-compatible" || w == "" {
			return "openai"
		}
		return w
	}
	own := family(run.Wire, run.Agent)
	if run.Capabilities != nil && run.Capabilities.Family != nil {
		own = *run.Capabilities.Family
	}
	result := map[string]string{run.ModelName: own, named.Critic: own, named.Child: own}
	if run.Agent != nil {
		result[run.Agent.Model] = own
	}
	if run.Context != nil {
		result[run.Context.Connection] = own
	}
	for id, b := range run.PolicyConnections {
		result[id] = family(b.Wire, b.Agent)
		if b.Capabilities != nil && b.Capabilities.Family != nil {
			result[id] = *b.Capabilities.Family
		}
	}
	return result
}
