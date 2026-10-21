package adapter

import (
	"context"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/relay"
	"net/http"
)

// PolicyConnection is a host-bound connection. Policy names IDs, never URLs or credentials.
type PolicyConnection struct {
	ModelName, Wire string
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
	for id := range run.PolicyConnections {
		ids = append(ids, id)
	}
	return ids
}

// PolicyBinding selects a resolved role using the host's actual capability bindings.
func PolicyBinding(run *Run, members runcontext.PolicyMembers, named runcontext.RunModels, role string) (PolicyConnection, error) {
	name := "models." + role + ".model"
	member := members[name]
	id, _ := member.Value.(string)
	own := PolicyConnection{ModelName: run.ModelName, Wire: run.Wire, Model: run.Model, Agent: run.Agent}
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
	if (binding.Agent != nil) != (run.Agent != nil) || run.Agent == nil && (binding.Model == nil || binding.Wire != run.Wire) {
		return PolicyConnection{}, &runcontext.PolicyRefusal{Member: name, Layer: member.Layer, Detail: "connection requires a different capability or wire family"}
	}
	return binding, nil
}

// ApplyPolicy selects the admitted author's binding and routes the critic through its binding.
// A wire-family or capability change this adapter cannot serve is refused before any request.
func ApplyPolicy(run *Run, sc *runcontext.SessionContext, named runcontext.RunModels) error {
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
