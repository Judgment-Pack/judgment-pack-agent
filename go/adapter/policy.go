package adapter

import (
	"context"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/relay"
	"net/http"
	"sort"
	"strings"
	"sync"
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
	sc.Execution.UsageModels = map[string]string{}
	for id, binding := range run.PolicyConnections {
		name := binding.ModelName
		if binding.Agent != nil {
			name = binding.Agent.Model
		}
		sc.Execution.UsageModels[id] = name
	}
	own := run.ModelName
	if run.Agent != nil {
		own = run.Agent.Model
	}
	id := own
	if run.Context != nil && run.Context.Connection != "" {
		id = run.Context.Connection
	}
	sc.Execution.UsageModels[id] = own
	restored := map[string]string{}
	for _, row := range sc.Execution.State().Failover {
		if _, seen := restored[row.Role]; seen {
			return &runcontext.PolicyRefusal{Member: "models." + row.Role + ".fallback", Layer: "run", Detail: "saved failover roles repeat"}
		}
		primary, _ := sc.ResolvedPolicy["models."+row.Role+".provider"].Value.(string)
		chain := []string{primary}
		fallbacks, _ := sc.ResolvedPolicy["models."+row.Role+".fallback"].Value.([]any)
		for _, raw := range fallbacks {
			chain = append(chain, raw.(string))
		}
		valid := len(row.History) < len(chain) && row.Consumed == int64(len(row.History))
		for i, h := range row.History {
			if i+1 >= len(chain) || h.From != chain[i] || h.To != chain[i+1] {
				valid = false
				break
			}
		}
		if !valid || row.Connection != chain[row.Consumed] {
			return &runcontext.PolicyRefusal{Member: "models." + row.Role + ".fallback", Layer: "run", Detail: "saved failover chain differs from admitted policy"}
		}
		restored[row.Role] = row.Connection
	}
	remember := func(role, from, to, reason string) {
		copiedRows := sc.Execution.State().Failover
		rows := &copiedRows
		defer func() { sc.Execution.MutateCheckpoint(runcontext.CheckpointPatch{"Failover": copiedRows}) }()
		for i := range *rows {
			if (*rows)[i].Role == role {
				(*rows)[i].Connection = to
				(*rows)[i].Consumed++
				(*rows)[i].History = append((*rows)[i].History, contract.CheckpointFailoverItemHistoryItem{From: from, To: to, Reason: reason})
				return
			}
		}
		*rows = append(*rows, contract.CheckpointFailoverItem{Role: role, Connection: to, Consumed: 1, History: []contract.CheckpointFailoverItemHistoryItem{{From: from, To: to, Reason: reason}}})
	}
	refuse := func(role string) {
		reason := "failover refused"
		run.Emit(contract.RunEvent{Guardrail: &contract.RunEventGuardrail{Type: "guardrail", Tool: "models." + role + ".fallback", Action: "refused", Detail: "The admitted fallback chain is exhausted.", Reason: &reason}})
	}
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
	for role, binding := range map[string]PolicyConnection{"author": author, "critic": critic} {
		id, _ := sc.ResolvedPolicy["models."+role+".provider"].Value.(string)
		model := binding.ModelName
		if binding.Agent != nil {
			model = binding.Agent.Model
		}
		sc.Execution.UsageModels[role+"@"+id] = model
	}
	named.Critic = critic.ModelName
	if critic.Agent != nil {
		named.Critic = critic.Agent.Model
	}
	capacity := int(sc.ResolvedPolicy["concurrency.requests"].Value.(float64))
	named.ConcurrentRequests = &capacity
	run.Models = func() (runcontext.RunModels, error) { return named, nil }
	if run.Agent != nil {
		activeIDs := map[string]string{}
		activeAgents := map[string]Agent{}
		for role, binding := range map[string]PolicyConnection{"author": author, "critic": critic} {
			activeIDs[role], _ = sc.ResolvedPolicy["models."+role+".provider"].Value.(string)
			if id := restored[role]; id != "" {
				activeIDs[role] = id
				binding = run.PolicyConnections[id]
			}
			selected := *binding.Agent
			activeAgents[role] = selected
		}
		var failoverMu sync.Mutex
		bound := activeAgents["author"]
		bound.Run = func(ctx context.Context, request AgentRequest, callbacks AgentCallbacks) error {
			role := "author"
			if request.Phase == "critic" {
				role = "critic"
			}
			failoverMu.Lock()
			selected := activeAgents[role]
			failoverMu.Unlock()
			return selected.Run(ctx, request, callbacks)
		}
		run.Agent = &bound
		run.Connection = func(role string) string { failoverMu.Lock(); defer failoverMu.Unlock(); return activeIDs[role] }
		run.Failover = func(role, reason string) (string, string, bool) {
			failoverMu.Lock()
			defer failoverMu.Unlock()
			classes, _ := sc.ResolvedPolicy["failover.classes"].Value.([]any)
			allowed := false
			for _, held := range classes {
				if held == reason {
					allowed = true
				}
			}
			if !allowed {
				return "", "", false
			}
			fallbacks, _ := sc.ResolvedPolicy["models."+role+".fallback"].Value.([]any)
			primary, _ := sc.ResolvedPolicy["models."+role+".provider"].Value.(string)
			chain := []string{primary}
			for _, raw := range fallbacks {
				if id, ok := raw.(string); ok {
					chain = append(chain, id)
				}
			}
			if len(chain) == 1 {
				return "", "", false
			}
			at := -1
			for i, id := range chain {
				if id == activeIDs[role] {
					at = i
				}
			}
			if at < 0 || at+1 >= len(chain) {
				refuse(role)
				return "", "", false
			}
			if run.Context != nil && run.Context.Objective != "" {
				detail := "A provider switch during agent delegation is refused because the account is exclusive."
				reasonName := "failover refused"
				run.Emit(contract.RunEvent{Guardrail: &contract.RunEventGuardrail{Type: "guardrail", Tool: "models." + role + ".fallback", Action: "refused", Detail: detail, Reason: &reasonName}})
				return "", "", false
			}
			to := chain[at+1]
			binding, ok := run.PolicyConnections[to]
			if !ok || binding.Agent == nil {
				return "", "", false
			}
			from := activeIDs[role]
			activeIDs[role] = to
			remember(role, from, to, reason)
			switched := *binding.Agent
			activeAgents[role] = switched
			sc.Execution.UseConnection(to, role)
			return from, to, true
		}
	} else {
		activeIDs := map[string]string{}
		active := map[string]PolicyConnection{"author": author, "critic": critic}
		for role := range active {
			activeIDs[role], _ = sc.ResolvedPolicy["models."+role+".provider"].Value.(string)
			if id := restored[role]; id != "" {
				activeIDs[role] = id
				active[role] = run.PolicyConnections[id]
			}
		}
		var failoverMu sync.Mutex
		run.Connection = func(role string) string { failoverMu.Lock(); defer failoverMu.Unlock(); return activeIDs[role] }
		run.Failover = func(role, reason string) (string, string, bool) {
			failoverMu.Lock()
			defer failoverMu.Unlock()
			classes, _ := sc.ResolvedPolicy["failover.classes"].Value.([]any)
			allowed := false
			for _, held := range classes {
				if held == reason {
					allowed = true
				}
			}
			if !allowed {
				return "", "", false
			}
			member := sc.ResolvedPolicy["models."+role+".fallback"]
			fallbacks, _ := member.Value.([]any)
			primary, _ := sc.ResolvedPolicy["models."+role+".provider"].Value.(string)
			chain := []string{primary}
			for _, raw := range fallbacks {
				if id, ok := raw.(string); ok {
					chain = append(chain, id)
				}
			}
			if len(chain) == 1 {
				return "", "", false
			}
			at := -1
			for i, id := range chain {
				if id == activeIDs[role] {
					at = i
				}
			}
			if at < 0 || at+1 >= len(chain) {
				refuse(role)
				return "", "", false
			}
			to := chain[at+1]
			binding, ok := run.PolicyConnections[to]
			if !ok || binding.Model == nil {
				return "", "", false
			}
			from := activeIDs[role]
			activeIDs[role] = to
			remember(role, from, to, reason)
			active[role] = binding
			sc.Execution.UseConnection(to, role)
			return from, to, true
		}
		run.ModelName = author.ModelName
		run.Wire = author.Wire
		run.Capabilities = author.Capabilities
		run.Model = func(ctx context.Context, suffix string, request relay.Request) (*http.Response, error) {
			role := "author"
			if sc.CriticReading {
				role = "critic"
			}
			failoverMu.Lock()
			selected := active[role]
			failoverMu.Unlock()
			return selected.Model(ctx, suffix, request)
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
