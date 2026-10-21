package delegate

import (
	"encoding/json"
	"strings"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/turn"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
)

func (c *coordinator) parentPolicy() runcontext.PolicyMembers {
	c.shared.mu.Lock()
	defer c.shared.mu.Unlock()
	if c.shared.parent == nil {
		if c.hasRestoredDefinitions() {
			members := runcontext.PolicyMembers{}
			for k, v := range c.run.Context.Execution.Policy {
				var value any
				_ = json.Unmarshal(v.Value, &value)
				members[k] = runcontext.PolicyMember{Value: value, Layer: v.Layer}
			}
			return members
		}
		return nil
	}
	return c.shared.parent.ResolvedPolicy
}

// childBinding reads the admitted parent's role before constructing the child's run.
func (c *coordinator) childBinding(profile turn.TaskProfile) (adapter.PolicyConnection, string, runcontext.PolicyLayer, error) {
	named := runcontext.RunModels{Critic: c.run.ModelName, Child: c.run.ModelName}
	if c.run.Models != nil {
		var err error
		named, err = c.run.Models()
		if err != nil {
			return adapter.PolicyConnection{}, "", nil, err
		}
	}
	members := c.parentPolicy()
	definitions := c.run.Subagents
	c.shared.mu.Lock()
	if c.shared.parent != nil {
		definitions = c.shared.parent.Subagents
	}
	c.shared.mu.Unlock()
	role := delegatedRole(members, profile.ID, definitions)
	if _, ok := members["models."+role+".model"]; !ok {
		// Profiles retain their legacy default unless the policy names this role.
		id := named.Child
		if id == "" || profile.Model != nil && *profile.Model == "parent" {
			id = c.run.ModelName
		}
		tier := c.run.Thinking
		if c.run.Agent != nil {
			tier = c.run.Agent.Effort
		}
		if tier == "" {
			tier = "off"
			if c.run.Agent != nil {
				tier = "none"
			}
		}
		if author, ok := members["models.author.reasoning"]; ok {
			tier, _ = author.Value.(string)
		}
		selected := runcontext.PolicyMembers{"models." + role + ".model": {Value: id, Layer: "table"}, "models." + role + ".reasoning": {Value: tier, Layer: "table"}}
		for k, v := range members {
			selected[k] = v
		}
		members = selected
	}
	binding, err := adapter.PolicyBinding(c.run, members, named, role)
	if err != nil {
		return binding, "", nil, err
	}
	id, _ := members["models."+role+".model"].Value.(string)
	if provider, ok := members["models."+role+".provider"].Value.(string); ok {
		id = provider
	}
	tier, _ := members["models."+role+".reasoning"].Value.(string)
	if binding.Agent != nil {
		held := *binding.Agent
		held.Effort = tier
		binding.Agent = &held
	}
	policy := runcontext.PolicyLayer{}
	// Inherit effective limits, without reinterpreting the parent's other roles
	// against this child's (possibly lower) author tier.
	for name, member := range members {
		if strings.HasPrefix(name, "models.") {
			continue
		}
		section, leaf, nested := strings.Cut(name, ".")
		if !nested {
			policy[name] = member.Value
			continue
		}
		object, _ := policy[section].(map[string]any)
		if object == nil {
			object = map[string]any{}
			policy[section] = object
		}
		object[leaf] = member.Value
	}
	policy["models"] = map[string]any{"author": map[string]any{"model": id, "reasoning": tier}}
	policy["critic"] = map[string]any{"default": "off"}
	return binding, id, policy, nil
}

func delegatedRole(members runcontext.PolicyMembers, id string, definitions ...[]runcontext.Subagent) string {
	for _, list := range definitions {
		for _, s := range list {
			if s.ID == id {
				return "subagent." + id
			}
		}
	}
	if _, child := members["models.child."+id+".provider"]; !child {
		if _, declared := members["models.subagent."+id+".provider"]; declared {
			return "subagent." + id
		}
	}
	return "child." + id
}

func (c *coordinator) hasRestoredDefinitions() bool {
	if c.run.Context == nil || c.run.Context.Execution == nil {
		return false
	}
	saved := c.run.Context.Execution
	if saved.Handoff != nil {
		for _, task := range saved.Handoff.Request.Tasks {
			if task.Subagent != nil {
				return true
			}
		}
	}
	if saved.Delegation != nil {
		for _, batch := range saved.Delegation.Batches {
			for _, child := range batch.Children {
				if child.Subagent != nil {
					return true
				}
			}
		}
	}
	return false
}
