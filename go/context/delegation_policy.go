package runcontext

import (
	"encoding/json"
	"reflect"
	"strings"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

// DelegatedPolicy is an opaque projection of one admitted role. Only the context
// can create it, from a live admission or a validated saved checkpoint.
type DelegatedPolicy struct {
	connection, model string
	members           PolicyMembers
}

type delegatedPolicyLink interface{ DelegatedPolicy() *DelegatedPolicy }

func projectDelegatedPolicy(members PolicyMembers, role, connection, model string) (*DelegatedPolicy, error) {
	prefix := "models." + role + "."
	if provider, ok := members[prefix+"provider"].Value.(string); ok && provider != connection {
		return nil, &PolicyRefusal{Member: "models.author.provider", Layer: "table", Detail: "delegated role differs from child admission"}
	}
	if _, ok := members[prefix+"reasoning"]; !ok {
		prefix = "models.author."
	}
	if _, ok := members[prefix+"reasoning"]; !ok {
		return nil, nil
	}
	selected := PolicyMembers{}
	for key, member := range members {
		if key == prefix+"reasoning" || strings.HasPrefix(key, prefix+"discovered.reasoning.") {
			// Values are detached, including the supported array.
			data, err := json.Marshal(member.Value)
			if err != nil {
				return nil, err
			}
			var value any
			if err = json.Unmarshal(data, &value); err != nil {
				return nil, err
			}
			member.Value = value
			selected["models.author."+strings.TrimPrefix(key, prefix)] = member
		}
	}
	return &DelegatedPolicy{connection: connection, model: model, members: selected}, nil
}

// DelegatedPolicy projects a live parent's admitted role for a child binding.
func (c *SessionContext) DelegatedPolicy(role, connection, model string) (*DelegatedPolicy, error) {
	return projectDelegatedPolicy(c.ResolvedPolicy, role, connection, model)
}

// RestoredDelegatedPolicy uses the same closed checkpoint reader as execution
// restore. Saved policy supplies provenance, never permission to skip admission.
func RestoredDelegatedPolicy(saved *contract.Checkpoint, role, connection, model string) (*DelegatedPolicy, error) {
	data, err := json.Marshal(saved)
	if err != nil {
		return nil, err
	}
	value, err := ParseJSON(data)
	if err != nil {
		return nil, err
	}
	checked, err := ReadExecution(value)
	if err != nil {
		return nil, err
	}
	if checked == nil {
		return nil, errSavedExecution
	}
	members := PolicyMembers{}
	for key, member := range checked.Policy {
		var value any
		if len(member.Value) > 0 {
			if err = json.Unmarshal(member.Value, &value); err != nil {
				return nil, err
			}
		}
		source := ""
		if member.Source != nil {
			source = *member.Source
		}
		members[key] = PolicyMember{Value: value, Layer: member.Layer, Source: source}
	}
	return projectDelegatedPolicy(members, role, connection, model)
}

func delegatedSessionPolicy(session Session) *DelegatedPolicy {
	if session.Context != nil {
		if link, ok := session.Context.Link.(delegatedPolicyLink); ok {
			return link.DelegatedPolicy()
		}
	}
	return nil
}

func (p *DelegatedPolicy) author() map[string]any {
	author := map[string]any{}
	if value := p.members["models.author.reasoning"].Value; value != nil {
		author["reasoning"] = value
	}
	if supported, ok := p.members["models.author.discovered.reasoning.supported"]; ok {
		reasoning := map[string]any{"supported": supported.Value}
		if value, ok := p.members["models.author.discovered.reasoning.default"]; ok {
			reasoning["default"] = value.Value
		}
		author["discovered"] = map[string]any{"reasoning": reasoning}
	}
	return author
}

func (p *DelegatedPolicy) restore(session Session, current string, resolved *ResolvedPolicy) error {
	if current != p.connection || resolved.Members["models.author.provider"].Value != p.connection || session.ModelName() != p.model || !reflect.DeepEqual(resolved.Members["models.author.reasoning"].Value, p.members["models.author.reasoning"].Value) {
		return &PolicyRefusal{Member: "models.author.reasoning", Layer: "table", Detail: "delegated role differs from child admission"}
	}
	for key, member := range resolved.Members {
		if strings.HasPrefix(key, "models.author.discovered.reasoning.") {
			if _, ok := p.members[key]; !ok {
				return &PolicyRefusal{Member: key, Layer: member.Layer, Detail: "delegated discovery differs from child admission"}
			}
		}
	}
	for key, member := range p.members {
		if key != "models.author.reasoning" && !reflect.DeepEqual(resolved.Members[key].Value, member.Value) {
			return &PolicyRefusal{Member: key, Layer: member.Layer, Detail: "delegated discovery differs from child admission"}
		}
		resolved.Members[key] = member
	}
	return nil
}
