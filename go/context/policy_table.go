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
	Value  any    `json:"value"`
	Layer  string `json:"layer"`
	Source string `json:"source,omitempty"`
}
type PolicyMembers map[string]PolicyMember
type PolicyHost struct {
	Connections      []string
	Families         map[string]string
	AuthorConnection string
	Agent            bool
	Requests         int
	PaceUsed         map[string]int64
}
type ResolvedPolicy struct {
	Members PolicyMembers
	Warning string
}
type PolicyRefusal struct {
	Member, Layer, Detail string
	Pace                  bool
	Checkpoint            *contract.Checkpoint
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
	if strings.Contains(e.Member, ".fallback") {
		reason = "failover refused"
	}
	return contract.RunEvent{Guardrail: &contract.RunEventGuardrail{Type: "guardrail", Tool: e.Member, Action: "refused", Reason: &reason, Detail: e.Error()}}
}

type policyShape struct {
	Ref         string                 `json:"$ref"`
	Type        json.RawMessage        `json:"type"`
	Properties  map[string]policyShape `json:"properties"`
	Patterns    map[string]policyShape `json:"patternProperties"`
	Required    []string               `json:"required"`
	Items       *policyShape           `json:"items"`
	Defs        map[string]policyShape `json:"$defs"`
	Enum        []any                  `json:"enum"`
	Const       any                    `json:"const"`
	Minimum     *float64               `json:"minimum"`
	Maximum     *float64               `json:"maximum"`
	MinLength   *int                   `json:"minLength"`
	MaxLength   *int                   `json:"maxLength"`
	MinItems    *int                   `json:"minItems"`
	MaxItems    *int                   `json:"maxItems"`
	UniqueItems bool                   `json:"uniqueItems"`
	Default     json.RawMessage        `json:"default"`
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
	if path == "outputPatterns" || path == "outputAllow" {
		var err error
		if path == "outputPatterns" {
			_, err = ReadOutputPatterns(value)
		} else {
			_, err = ReadOutputAllow(value)
		}
		if err != nil {
			return &PolicyRefusal{Member: path, Layer: layer, Detail: "invalid output guard configuration"}
		}
		out[path] = value
		return nil
	}

	if strings.HasPrefix(s.Ref, "#/$defs/") {
		s = policySchema.Defs[strings.TrimPrefix(s.Ref, "#/$defs/")]
	}
	refuse := func() error {
		return &PolicyRefusal{Member: path, Layer: layer, Detail: "invalid or unknown policy member"}
	}
	if string(s.Type) == `"object"` {
		object, ok := value.(map[string]any)
		if !ok {
			return refuse()
		}
		for _, required := range s.Required {
			if _, ok := object[required]; !ok {
				return refuse()
			}
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
	if string(s.Type) == `"array"` {
		items, ok := value.([]any)
		if !ok || s.MinItems != nil && len(items) < *s.MinItems || s.MaxItems != nil && len(items) > *s.MaxItems {
			return refuse()
		}
		seen := map[string]bool{}
		for at, item := range items {
			encoded, _ := json.Marshal(item)
			if s.UniqueItems && seen[string(encoded)] {
				return refuse()
			}
			seen[string(encoded)] = true
			if s.Items != nil {
				if err := policyRead(item, *s.Items, fmt.Sprintf("%s[%d]", path, at), layer, map[string]any{}); err != nil {
					return err
				}
			}
		}
		out[path] = value
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
		out[path] = PolicyMember{Value: v, Layer: "table", Source: "layer"}
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
			members[k] = PolicyMember{Value: v, Layer: layer.name, Source: "layer"}
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
	for _, name := range []string{"models.author.model"} {
		if _, ok := members[name]; !ok {
			return result, refuse(name, "no default or host value", false)
		}
	}
	tiers := ModelTiers
	if host.Agent {
		tiers = AgentTiers
	}
	rank := func(value string) int {
		if host.Agent {
			return slices.Index(tiers, value)
		}
		return map[string]int{"off": 0, "low": 1, "on": 2, "medium": 2, "ultra": 3, "high": 3}[value]
	}
	roles := map[string]bool{"author": true, "critic": true}
	for name := range members {
		if strings.HasPrefix(name, "models.") && !strings.HasPrefix(name, "models.taskClasses.") {
			role := strings.TrimPrefix(name, "models.")
			for _, suffix := range []string{".model", ".reasoning", ".provider", ".fallback"} {
				role = strings.TrimSuffix(role, suffix)
			}
			if at := strings.Index(role, ".discovered."); at >= 0 {
				role = role[:at]
			}
			roles[role] = true
		}
	}
	base := host.AuthorConnection
	if base == "" {
		base, _ = members["models.author.model"].Value.(string)
	}
	family := func(id string) string {
		if f := host.Families[id]; f != "" {
			return f
		}
		if host.Agent {
			return "codex"
		}
		return "openai"
	}
	authorConnection := base
	order := []string{"author"}
	for _, role := range policyKeys(roles) {
		if role != "author" {
			order = append(order, role)
		}
	}
	for _, role := range order {
		name := "models." + role + ".provider"
		member, ok := members[name]
		if !ok {
			member = PolicyMember{Value: "same", Layer: "table", Source: "layer"}
		}
		members[name] = member
		choice, _ := member.Value.(string)
		selected := choice
		if choice == "same" {
			selected = authorConnection
		}
		if choice == "other" {
			selected = ""
			for _, id := range host.Connections {
				if family(id) != family(authorConnection) {
					selected = id
					break
				}
			}
		}
		if selected == "" || !slices.Contains(host.Connections, selected) {
			return result, refuse(name, "no connected provider satisfies the role", false)
		}
		members[name] = PolicyMember{Value: selected, Layer: member.Layer, Source: member.Source}
		fallbackName := "models." + role + ".fallback"
		fallbackMember, ok := members[fallbackName]
		if !ok {
			fallbackMember = PolicyMember{Value: []any{}, Layer: "table", Source: "layer"}
			members[fallbackName] = fallbackMember
		}
		fallbacks, ok := fallbackMember.Value.([]any)
		if !ok {
			return result, refuse(fallbackName, "fallback must be an array", false)
		}
		for _, raw := range fallbacks {
			id, ok := raw.(string)
			if !ok || !slices.Contains(host.Connections, id) {
				return result, refuse(fallbackName, "fallback connection is not available", false)
			}
			if family(id) != family(selected) {
				return result, refuse(fallbackName, "cross-family fallback is refused", false)
			}
			if id == selected {
				return result, refuse(fallbackName, "fallback repeats the selected connection", false)
			}
		}
		if role == "author" {
			authorConnection = selected
		}
	}
	resolveReasoning := func(role string, inherited *PolicyMember) (PolicyMember, error) {
		name := "models." + role + ".reasoning"
		explicit, hasExplicit := members[name]
		defaultMember, hasDefault := members["models."+role+".discovered.reasoning.default"]
		supportedMember, hasSupported := members["models."+role+".discovered.reasoning.supported"]
		supported, _ := supportedMember.Value.([]any)
		contains := func(value any) bool {
			for _, candidate := range supported {
				if candidate == value {
					return true
				}
			}
			return false
		}
		if hasDefault && (!hasSupported || !contains(defaultMember.Value)) {
			return PolicyMember{}, &PolicyRefusal{Member: name, Layer: defaultMember.Layer, Detail: fmt.Sprintf("discovered default %q is not advertised as supported", defaultMember.Value)}
		}
		if hasExplicit {
			if hasSupported && !contains(explicit.Value) {
				model := members["models."+role+".model"].Value
				return PolicyMember{}, &PolicyRefusal{Member: name, Layer: explicit.Layer, Detail: fmt.Sprintf("reasoning value %q is not supported by model %q", explicit.Value, model)}
			}
			return explicit, nil
		}
		if hasDefault {
			return PolicyMember{Value: defaultMember.Value, Layer: defaultMember.Layer, Source: "discovered"}, nil
		}
		if inherited != nil {
			member := *inherited
			member.Layer = "table"
			return member, nil
		}
		return PolicyMember{Layer: "table", Source: "model-default"}, nil
	}
	authorMember, err := resolveReasoning("author", nil)
	if err != nil {
		return result, err
	}
	members["models.author.reasoning"] = authorMember
	author, _ := authorMember.Value.(string)
	for _, role := range policyKeys(roles) {
		for _, field := range []string{"model", "reasoning"} {
			name := "models." + role + "." + field
			if _, ok := members[name]; !ok && field == "model" {
				members[name] = PolicyMember{Value: members["models.author."+field].Value, Layer: "table", Source: "layer"}
			}
			if field == "reasoning" && role != "author" {
				member, reasoningErr := resolveReasoning(role, &authorMember)
				if reasoningErr != nil {
					return result, reasoningErr
				}
				members[name] = member
			}
			value, _ := members[name].Value.(string)
			if field == "model" && !slices.Contains(host.Connections, value) {
				return result, refuse(name, "model connection is not available", false)
			}
			if field == "reasoning" {
				if value != "" && !slices.Contains(tiers, value) {
					return result, refuse(name, "tier is not supported on this capability", false)
				}
				if value != "" && (strings.HasPrefix(role, "child.") || strings.HasPrefix(role, "subagent.")) {
					models, _ := project["models"].(map[string]any)
					assigned, _ := models[role].(map[string]any)
					allowance, _ := assigned["reasoning"].(string)
					unknownRunIncrease := author == "" && members[name].Layer == "run" && allowance == ""
					knownIncrease := author != "" && rank(value) > rank(author) && (allowance == "" || rank(value) > rank(allowance))
					if unknownRunIncrease || knownIncrease {
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
		// A new run needs positive allowance for its first unit.
		if float64(used) >= number(name) {
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
		var data json.RawMessage
		if v.Value != nil || v.Source != "model-default" {
			data, _ = json.Marshal(v.Value)
		}
		var source *string
		if v.Source != "" {
			source = &v.Source
		}
		out[k] = contract.PolicyMember{Value: data, Layer: v.Layer, Source: source}
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
	tier := ""
	if session.Model != nil && session.Model.Thinking != "" {
		tier = session.Model.Thinking
	}
	if session.Agent != nil {
		tier = session.Agent.Effort
	}
	authorRole := map[string]any{"model": current}
	if tier != "" {
		authorRole["reasoning"] = tier
	}
	table["models"] = map[string]any{"author": authorRole, "compactor": map[string]any{"model": current, "reasoning": "low"}}
	for _, s := range session.Subagents {
		role := map[string]any{"model": s.Model}
		if s.Reasoning != "" {
			role["reasoning"] = s.Reasoning
		}
		table["models"].(map[string]any)["subagent."+s.ID] = role
	}
	capacity := 2
	if session.PolicyLegacyModels != nil && session.PolicyLegacyModels.ConcurrentRequests != nil {
		capacity = *session.PolicyLegacyModels.ConcurrentRequests
	}
	children := capacity
	if session.Agent != nil {
		children = 1
	}
	table["concurrency"] = map[string]any{"children": children, "requests": capacity}
	if session.PolicyLegacyModels != nil {
		table["models"].(map[string]any)["critic"] = map[string]any{"model": session.PolicyLegacyModels.Critic}
		if session.PolicyLegacyModels.ConcurrentRequests != nil {
			table["concurrency"].(map[string]any)["requests"] = *session.PolicyLegacyModels.ConcurrentRequests
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
		requests = 2
		if session.PolicyLegacyModels != nil && session.PolicyLegacyModels.ConcurrentRequests != nil {
			requests = *session.PolicyLegacyModels.ConcurrentRequests
		}
	}
	return ResolvePolicy(run, project, table, PolicyHost{Connections: connections, Families: session.PolicyFamilies, AuthorConnection: current, Agent: session.Agent != nil, Requests: requests, PaceUsed: session.PaceUsed})
}

// FailoverRefusal records the boundary; same-family failover is left to issue #104.
func FailoverRefusal(role string, from, to contract.ProvenanceProvider) *contract.RunEvent {
	if from.Family == to.Family {
		return nil
	}
	refusal := (&PolicyRefusal{Member: "models." + role + ".provider", Layer: "run", Detail: "cross-family failover refused: the prefix and cache do not carry"}).Event()
	return &refusal
}
