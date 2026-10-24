package runcontext

import "encoding/json"
import "github.com/Judgment-Pack/judgment-pack-agent/go/contract"

// RequestValues is the closed, complete admitted member book shared by adapters
// and standalone compaction. A new policy member cannot inherit a raw default.
type RequestValues struct {
	members  PolicyMembers
	families map[string]string
}

// NewRequestValues is the sole conversion of resolved policy into request values.
func NewRequestValues(members PolicyMembers, providerFamilies ...map[string]string) *RequestValues {
	copy := PolicyMembers{}
	for key, member := range members {
		copy[key] = copyRequestMember(member)
	}
	families := map[string]string{}
	if len(providerFamilies) > 0 {
		for key, value := range providerFamilies[0] {
			families[key] = value
		}
	}
	return &RequestValues{members: copy, families: families}
}
func copyRequestMember(member PolicyMember) PolicyMember {
	if member.Value != nil {
		data, err := json.Marshal(member.Value)
		if err != nil {
			panic(err)
		}
		if err = json.Unmarshal(data, &member.Value); err != nil {
			panic(err)
		}
	}
	return member
}

// Member returns a detached admitted member, including members added later.
func (v *RequestValues) Member(name string) PolicyMember { return copyRequestMember(v.members[name]) }
func (v *RequestValues) Reasoning(role string) string {
	value, _ := v.Member("models." + role + ".reasoning").Value.(string)
	return value
}
func (v *RequestValues) Model(role string) string {
	value, _ := v.Member("models." + role + ".model").Value.(string)
	return value
}
func (v *RequestValues) Provider(role string) *contract.ProvenanceProvider {
	id, _ := v.Member("models." + role + ".provider").Value.(string)
	if id == "" {
		id, _ = v.Member("models.author.provider").Value.(string)
	}
	family := v.families[id]
	if family == "" {
		family = "openai"
	}
	return &contract.ProvenanceProvider{Connection: id, Family: family}
}
func (c *SessionContext) RequestValues() *RequestValues {
	c.requestValuesOnce.Do(func() { c.requestValues = NewRequestValues(c.ResolvedPolicy, c.ProviderFamilies) })
	return c.requestValues
}
