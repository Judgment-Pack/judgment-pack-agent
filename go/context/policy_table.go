package runcontext

// Host-only policy admission (ADR-0011), shared by both adapters.
import (
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

//go:embed policy_schema.json
var policySchemaJSON []byte

type PolicyLayer map[string]any
type PolicyMember struct {
	Value any    `json:"value"`
	Layer string `json:"layer"`
}
type PolicyMembers map[string]PolicyMember
type PolicyHost struct {
	Connections []string
	Agent       bool
	Requests    int
	PaceUsed    map[string]int64
}
type ResolvedPolicy struct {
	Members PolicyMembers
	Warning string
}
type PolicyRefusal struct {
	Member, Layer, Detail string
	Pace                  bool
}

func (e *PolicyRefusal) Error() string {
	return fmt.Sprintf("%s (%s): %s", e.Member, e.Layer, e.Detail)
}
func (e *PolicyRefusal) FailureCode() string {
	if e.Pace {
		return "pace exceeded"
	}
	return "configuration"
}
func (e *PolicyRefusal) Event() contract.RunEvent {
	reason := "policy refused"
	return contract.RunEvent{Guardrail: &contract.RunEventGuardrail{Type: "guardrail", Tool: e.Member, Action: "refused", Reason: &reason, Detail: e.Error()}}
}

type policyShape struct {
	Ref        string                 `json:"$ref"`
	Type       json.RawMessage        `json:"type"`
	Properties map[string]policyShape `json:"properties"`
	Patterns   map[string]policyShape `json:"patternProperties"`
	Defs       map[string]policyShape `json:"$defs"`
	Enum       []any                  `json:"enum"`
	Const      any                    `json:"const"`
	Minimum    *float64               `json:"minimum"`
	Maximum    *float64               `json:"maximum"`
	MinLength  *int                   `json:"minLength"`
	MaxLength  *int                   `json:"maxLength"`
	Default    json.RawMessage        `json:"default"`
}

var policySchema = func() policyShape {
	var s policyShape
	if err := json.Unmarshal(policySchemaJSON, &s); err != nil {
		panic(err)
	}
	return s
}()

func policyKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
func policyRead(value any, s policyShape, path, layer string, out map[string]any) error {
	if s.Ref == "#/$defs/role" {
		s = policySchema.Defs["role"]
	}
	refuse := func() error {
		return &PolicyRefusal{Member: path, Layer: layer, Detail: "invalid or unknown policy member"}
	}
	if string(s.Type) == `"object"` {
		object, ok := value.(map[string]any)
		if !ok {
			return refuse()
		}
		for _, key := range policyKeys(object) {
			child, ok := s.Properties[key]
			if !ok {
				for pattern, v := range s.Patterns {
					if regexp.MustCompile(pattern).MatchString(key) {
						child = v
						ok = true
						break
					}
				}
			}
			name := key
			if path != "" {
				name = path + "." + key
			}
			if !ok {
				return &PolicyRefusal{Member: name, Layer: layer, Detail: "unknown policy member"}
			}
			if err := policyRead(object[key], child, name, layer, out); err != nil {
				return err
			}
		}
		return nil
	}
	if s.Enum != nil && !slices.Contains(s.Enum, value) {
		return refuse()
	}
	if s.Const != nil && s.Const != value {
		return refuse()
	}
	kind := string(s.Type)
	if value == nil && strings.Contains(kind, `"null"`) {
		out[path] = nil
		return nil
	}
	if strings.Contains(kind, `"integer"`) || strings.Contains(kind, `"number"`) {
		n, ok := value.(float64)
		if !ok || math.IsNaN(n) || math.IsInf(n, 0) || math.Abs(n) > 9007199254740991 || strings.Contains(kind, `"integer"`) && math.Trunc(n) != n {
			return refuse()
		}
		if s.Minimum != nil && n < *s.Minimum || s.Maximum != nil && n > *s.Maximum {
			return refuse()
		}
	} else if kind == `"string"` {
		v, ok := value.(string)
		if !ok {
			return refuse()
		}
		if s.MinLength != nil && U16Length(v) < *s.MinLength || s.MaxLength != nil && U16Length(v) > *s.MaxLength {
			return refuse()
		}
	}
	out[path] = value
	return nil
}
func policyDefaults(s policyShape, path string, out PolicyMembers) {
	if s.Default != nil {
		var v any
		_ = json.Unmarshal(s.Default, &v)
		out[path] = PolicyMember{v, "table"}
	}
	for k, v := range s.Properties {
		name := k
		if path != "" {
			name = path + "." + k
		}
		policyDefaults(v, name, out)
	}
}

// PolicyAlias reads budget.json as policy.budget for one release; new members win.
func PolicyAlias(value PolicyLayer) PolicyLayer {
	out := PolicyLayer{}
	budget := map[string]any{}
	critic := map[string]any{}
	for k, v := range value {
		if _, ok := policySchema.Properties["budget"].Properties[k]; ok {
			budget[k] = v
		} else {
			out[k] = v
		}
	}
	if v, ok := value["criticDefault"]; ok {
		critic["default"] = v
	} else if v, ok := value["adversarialReview"].(bool); ok {
		critic["default"] = "off"
		if v {
			critic["default"] = "on"
		}
	}
	if v, ok := value["criticReasoningFloor"]; ok {
		critic["reasoningFloor"] = v
	}
	for _, k := range []string{"criticDefault", "criticReasoningFloor", "adversarialReview"} {
		delete(out, k)
	}
	if v, ok := value["budget"].(map[string]any); ok {
		for k, x := range v {
			budget[k] = x
		}
	}
	if v, ok := value["critic"].(map[string]any); ok {
		for k, x := range v {
			critic[k] = x
		}
	}
	if len(budget) > 0 {
		out["budget"] = budget
	}
	if len(critic) > 0 {
		out["critic"] = critic
	}
	return out
}

// ResolvePolicy is the one run > project > table resolver. It reads only typed host inputs.
func ResolvePolicy(run, project, table PolicyLayer, host PolicyHost) (ResolvedPolicy, error) {
	result := ResolvedPolicy{Members: PolicyMembers{}}
	members := result.Members
	policyDefaults(policySchema, "", members)
	for _, layer := range []struct {
		name  string
		input PolicyLayer
	}{{"table", PolicyAlias(table)}, {"project", project}, {"run", run}} {
		data, err := json.Marshal(layer.input)
		if err != nil {
			return result, err
		}
		input := map[string]any{}
		if layer.input != nil {
			if err = json.Unmarshal(data, &input); err != nil {
				return result, err
			}
		}
		flat := map[string]any{}
		if err = policyRead(input, policySchema, "", layer.name, flat); err != nil {
			return result, err
		}
		for k, v := range flat {
			members[k] = PolicyMember{v, layer.name}
		}
	}
	refuse := func(name, detail string, pace bool) error {
		layer := members[name].Layer
		if layer == "" {
			layer = "table"
		}
		return &PolicyRefusal{Member: name, Layer: layer, Detail: detail, Pace: pace}
	}
	task, _ := members["taskClass"].Value.(string)
	if mapped, ok := members["models.taskClasses."+task]; ok {
		if author, ok := members["models.author.reasoning"]; !ok || author.Layer == "table" {
			members["models.author.reasoning"] = mapped
		}
	}
	for _, name := range []string{"models.author.model", "models.author.reasoning"} {
		if _, ok := members[name]; !ok {
			return result, refuse(name, "no default or host value", false)
		}
	}
	tiers := ModelTiers
	if host.Agent {
		tiers = AgentTiers
	}
	author, _ := members["models.author.reasoning"].Value.(string)
	roles := map[string]bool{"author": true, "critic": true}
	for name := range members {
		if strings.HasPrefix(name, "models.") && !strings.HasPrefix(name, "models.taskClasses.") {
			roles[name[7:strings.LastIndex(name, ".")]] = true
		}
	}
	for _, role := range policyKeys(roles) {
		for _, field := range []string{"model", "reasoning"} {
			name := "models." + role + "." + field
			if _, ok := members[name]; !ok {
				members[name] = PolicyMember{members["models.author."+field].Value, "table"}
			}
			value, _ := members[name].Value.(string)
			if field == "model" && !slices.Contains(host.Connections, value) {
				return result, refuse(name, "model connection is not available", false)
			}
			if field == "reasoning" {
				if !slices.Contains(tiers, value) {
					return result, refuse(name, "tier is not supported on this capability", false)
				}
				if (strings.HasPrefix(role, "child.") || strings.HasPrefix(role, "subagent.")) && slices.Index(tiers, value) > slices.Index(tiers, author) {
					models, _ := project["models"].(map[string]any)
					assigned, _ := models[role].(map[string]any)
					allowance, _ := assigned["reasoning"].(string)
					if allowance == "" || slices.Index(tiers, value) > slices.Index(tiers, allowance) {
						return result, refuse(name, "tier exceeds the author's without project authorization", false)
					}
				}
			}
		}
	}
	number := func(name string) float64 { n, _ := members[name].Value.(float64); return n }
	if host.Agent && number("concurrency.children") != 1 {
		return result, refuse("concurrency.children", "agent children are sequential", false)
	}
	if number("concurrency.requests") > float64(host.Requests) {
		return result, refuse("concurrency.requests", "exceeds host capacity", false)
	}
	for _, key := range []string{"requestsPerDay", "invocationsPerDay", "bytesPerDay"} {
		name := "pace." + key
		used := host.PaceUsed[key]
		if used < 0 {
			return result, refuse(name, "invalid host period total", false)
		}
		if members[name].Value == nil {
			continue
		}
		if float64(used) > number(name) {
			return result, refuse(name, "project period budget exceeded", true)
		}
		if float64(used) >= number(name)*number("pace.warnAt") && result.Warning == "" {
			result.Warning = fmt.Sprintf("%s (%s): project period warning threshold reached", name, members[name].Layer)
		}
	}
	return result, nil
}
func (p PolicyMembers) Record() map[string]contract.PolicyMember {
	out := map[string]contract.PolicyMember{}
	for k, v := range p {
		data, _ := json.Marshal(v.Value)
		out[k] = contract.PolicyMember{Value: data, Layer: v.Layer}
	}
	return out
}

func resolveSessionPolicy(session Session, budget Policy) (ResolvedPolicy, error) {
	data, _ := json.Marshal(budget)
	table := PolicyLayer{}
	_ = json.Unmarshal(data, &table)
	current := session.ModelName()
	if session.Context != nil && session.Context.Connection != "" {
		current = session.Context.Connection
	}
	tier := "off"
	if session.Model != nil && session.Model.Thinking != "" {
		tier = session.Model.Thinking
	}
	if session.Agent != nil {
		tier = session.Agent.Effort
		if tier == "" {
			tier = "none"
		}
	}
	table["models"] = map[string]any{"author": map[string]any{"model": current, "reasoning": tier}}
	if session.PolicyLegacyModels != nil {
		table["models"].(map[string]any)["critic"] = map[string]any{"model": session.PolicyLegacyModels.Critic}
		if session.PolicyLegacyModels.ConcurrentRequests != nil {
			table["concurrency"] = map[string]any{"requests": *session.PolicyLegacyModels.ConcurrentRequests}
		}
	}
	clone := func(layer PolicyLayer) PolicyLayer {
		out := PolicyLayer{}
		if layer != nil {
			raw, _ := json.Marshal(layer)
			_ = json.Unmarshal(raw, &out)
		}
		return out
	}
	run, project := clone(session.Policy), clone(session.PolicyProject)
	critic := func(layer PolicyLayer, mode string) {
		v, _ := layer["critic"].(map[string]any)
		if v == nil {
			v = map[string]any{}
		}
		if _, ok := v["default"]; !ok && mode != "" {
			v["default"] = mode
		}
		layer["critic"] = v
	}
	mode := session.Critic
	if mode == "" {
		mode = criticAlias(session.AdversarialReview)
	}
	critic(run, mode)
	critic(project, session.CriticProject)
	if session.TaskClass != "" {
		run["taskClass"] = session.TaskClass
	}
	if session.CriticReasoning != "" {
		models, _ := run["models"].(map[string]any)
		if models == nil {
			models = map[string]any{}
		}
		role, _ := models["critic"].(map[string]any)
		if role == nil {
			role = map[string]any{}
		}
		if _, ok := role["reasoning"]; !ok {
			role["reasoning"] = session.CriticReasoning
		}
		models["critic"] = role
		run["models"] = models
	}
	connections := session.PolicyConnections
	if connections == nil {
		connections = []string{current}
	}
	requests := session.PolicyRequests
	if requests == 0 {
		requests = 1
		if session.PolicyLegacyModels != nil && session.PolicyLegacyModels.ConcurrentRequests != nil {
			requests = *session.PolicyLegacyModels.ConcurrentRequests
		}
	}
	return ResolvePolicy(run, project, table, PolicyHost{Connections: connections, Agent: session.Agent != nil, Requests: requests, PaceUsed: session.PaceUsed})
}
