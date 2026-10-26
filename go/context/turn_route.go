package runcontext

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"unicode/utf8"
)

// RouteSelection is presentation over an admitted catalogue, never execution authority.
type RouteSelection struct {
	Route      string   `json:"route"`
	Tier       *string  `json:"tier,omitempty"`
	Skills     []string `json:"skills"`
	Tools      any      `json:"tools"`
	Operations any      `json:"operations"`
	Planning   string   `json:"planning"`
}
type RouteFacts struct {
	Mode        string `json:"mode"`
	Attachments bool   `json:"attachments"`
	Artifact    bool   `json:"artifact"`
	TurnBytes   int    `json:"turnBytes"`
	Slash       string `json:"slash,omitempty"`
}
type RouteRule struct {
	ID    string         `json:"id"`
	When  map[string]any `json:"when"`
	Route RouteSelection `json:"route"`
}
type RouteClassifier struct {
	Labels              map[string]RouteSelection `json:"labels"`
	MaxOutputTokens     int                       `json:"maxOutputTokens"`
	TimeoutMilliseconds int                       `json:"timeoutMilliseconds"`
}
type RoutingPolicy struct {
	Mode       string           `json:"mode"`
	Tiers      []string         `json:"tiers"`
	Rules      []RouteRule      `json:"rules,omitempty"`
	Classifier *RouteClassifier `json:"classifier,omitempty"`
}
type RouteTool struct {
	Name        string `json:"name"`
	Operation   string `json:"operation"`
	Effect      string `json:"effect,omitempty"`
	Interaction string `json:"interaction,omitempty"`
}
type RouteCatalogue struct {
	Tools    []RouteTool `json:"tools"`
	Skills   []string    `json:"skills"`
	Tiers    []string    `json:"tiers"`
	Tier     *string     `json:"tier,omitempty"`
	Planning bool        `json:"planning"`
	Native   bool        `json:"native"`
}
type RouteClassifierRecord struct {
	Binding      string           `json:"binding"`
	PromptDigest string           `json:"promptDigest"`
	Status       string           `json:"status"`
	Label        string           `json:"label,omitempty"`
	Usage        map[string]int64 `json:"usage,omitempty"`
}
type RouteRefusal struct {
	Stage  string `json:"stage"`
	Reason string `json:"reason"`
}
type RouteWidenedInvocation struct {
	DispatchID    string `json:"dispatchId"`
	RequestDigest string `json:"requestDigest"`
}
type TurnRoute struct {
	RouteSelection
	Version           int                     `json:"version"`
	DecidedBy         string                  `json:"decidedBy"`
	Reason            string                  `json:"reason"`
	BindingDigest     string                  `json:"bindingDigest"`
	CatalogueDigest   string                  `json:"catalogueDigest"`
	Widened           bool                    `json:"widened"`
	WidenedFor        string                  `json:"widenedFor,omitempty"`
	WidenedInvocation *RouteWidenedInvocation `json:"widenedInvocation,omitempty"`
	Refusals          []RouteRefusal          `json:"refusals"`
	Classifier        *RouteClassifierRecord  `json:"classifier,omitempty"`
}

var ErrTurnRouteInvalid = errors.New("TurnRouteInvalid")
var ErrTurnRouteIdentityChanged = errors.New("TurnRouteIdentityChanged")
var ErrTurnRouteWidenRefused = errors.New("TurnRouteWidenRefused")

func routeName(s string) bool {
	if s == "" || U16Length(s) > 100 || !utf8.ValidString(s) {
		return false
	}
	for _, c := range s {
		if c < 32 {
			return false
		}
	}
	return true
}
func routeNames(v []string) bool {
	if len(v) > 128 || v == nil {
		return false
	}
	seen := map[string]bool{}
	for _, s := range v {
		if !routeName(s) || seen[s] {
			return false
		}
		seen[s] = true
	}
	return true
}
func routeList(v any) ([]string, bool) {
	if s, ok := v.(string); ok {
		return nil, s == "all"
	}
	switch xs := v.(type) {
	case []string:
		return xs, routeNames(xs)
	case []any:
		r := []string{}
		for _, x := range xs {
			s, ok := x.(string)
			if !ok {
				return nil, false
			}
			r = append(r, s)
		}
		return r, routeNames(r)
	}
	return nil, false
}
func routeContains(v any, name string) bool {
	if s, ok := v.(string); ok {
		return s == "all"
	}
	xs, ok := routeList(v)
	return ok && slices.Contains(xs, name)
}
func ValidRouteSelection(r RouteSelection) bool {
	if r.Route != "direct" && r.Route != "agent" || r.Tier != nil && !routeName(*r.Tier) || !routeNames(r.Skills) || r.Planning != "offered" && r.Planning != "withheld" {
		return false
	}
	_, ok := routeList(r.Tools)
	if !ok {
		return false
	}
	ops, ok := routeList(r.Operations)
	if !ok {
		return false
	}
	for _, op := range ops {
		if !slices.Contains([]string{"discover", "read", "answer", "check", "transform", "evaluate", "propose", "act", "run", "own-state", "ask"}, op) {
			return false
		}
	}
	return true
}
func routeBounded(v any) bool  { b, e := json.Marshal(v); return e == nil && len(b) <= 65536 }
func RouteDigest(v any) string { return durableDigest([]byte(usageCanonical(v))) }
func ValidRoutingPolicy(p RoutingPolicy) bool {
	if !slices.Contains([]string{"off", "rules", "auto"}, p.Mode) || !routeNames(p.Tiers) || len(p.Tiers) == 0 || len(p.Rules) > 64 || !routeBounded(p) {
		return false
	}
	seen := map[string]bool{}
	for _, r := range p.Rules {
		if !routeName(r.ID) || seen[r.ID] || r.When == nil || !ValidRouteSelection(r.Route) {
			return false
		}
		seen[r.ID] = true
		for k, v := range r.When {
			switch k {
			case "mode", "slash":
				s, ok := v.(string)
				if !ok || !routeName(s) {
					return false
				}
			case "artifact", "attachments":
				if _, ok := v.(bool); !ok {
					return false
				}
			case "turnBytes", "maxTurnBytes":
				f, ok := v.(float64)
				if !ok {
					if n, yes := v.(int); yes {
						f = float64(n)
						ok = true
					}
				}
				if !ok || f < 0 || f > 1048576 || float64(int(f)) != f {
					return false
				}
			default:
				return false
			}
		}
	}
	if c := p.Classifier; c != nil {
		if len(c.Labels) < 1 || len(c.Labels) > 32 || c.MaxOutputTokens < 1 || c.MaxOutputTokens > 64 || c.TimeoutMilliseconds < 1 || c.TimeoutMilliseconds > 5000 {
			return false
		}
		for label, r := range c.Labels {
			if len(label) > 32 || len(label) == 0 || label[0] < 'a' || label[0] > 'z' || !ValidRouteSelection(r) {
				return false
			}
			for _, c := range label {
				if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
					return false
				}
			}
		}
	}
	return true
}
func routeJSON[T any](value any) (T, error) {
	var out T
	b, e := json.Marshal(value)
	if e != nil || len(b) > 65536 {
		return out, ErrTurnRouteInvalid
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if e = d.Decode(&out); e != nil {
		return out, ErrTurnRouteInvalid
	}
	return out, nil
}
func ReadRoutingPolicy(value any) (RoutingPolicy, error) {
	p, e := routeJSON[RoutingPolicy](value)
	if e != nil || !ValidRoutingPolicy(p) {
		return p, ErrTurnRouteInvalid
	}
	return p, nil
}
func ValidTurnRoute(r TurnRoute) bool {
	if !ValidRouteSelection(r.RouteSelection) || r.Version != 1 || !slices.Contains([]string{"host", "rule", "classifier", "default"}, r.DecidedBy) || !routeName(r.Reason) || !recordDigestPattern.MatchString(r.BindingDigest) || !recordDigestPattern.MatchString(r.CatalogueDigest) || r.WidenedFor != "" && (!r.Widened || !routeName(r.WidenedFor)) || r.Refusals == nil || len(r.Refusals) > 66 || !routeBounded(r) {
		return false
	}
	if i := r.WidenedInvocation; i != nil {
		if !r.Widened || !routeName(r.WidenedFor) || i.DispatchID == "" || U16Length(i.DispatchID) > 256 || !utf8.ValidString(i.DispatchID) || !recordDigestPattern.MatchString(i.RequestDigest) {
			return false
		}
		for _, c := range i.DispatchID {
			if c < 32 {
				return false
			}
		}
	}
	for _, f := range r.Refusals {
		if !slices.Contains([]string{"host", "rule", "classifier"}, f.Stage) || !routeName(f.Reason) {
			return false
		}
	}
	if c := r.Classifier; c != nil {
		if !routeName(c.Binding) || !recordDigestPattern.MatchString(c.PromptDigest) || !slices.Contains([]string{"label", "refused", "timeout", "unknown-label", "unsupported", "pending"}, c.Status) || c.Label != "" && !routeName(c.Label) {
			return false
		}
		for k, v := range c.Usage {
			if k != "inputTokens" && k != "outputTokens" || v < 0 || v > 9007199254740991 {
				return false
			}
		}
	}
	return true
}
func FullRoute(c RouteCatalogue) RouteSelection {
	planning := "withheld"
	if c.Planning {
		planning = "offered"
	}
	return RouteSelection{Route: "agent", Tier: c.Tier, Skills: []string{}, Tools: "all", Operations: "all", Planning: planning}
}
func RouteSelectionProblem(r RouteSelection, c RouteCatalogue) string {
	if !ValidRouteSelection(r) {
		return "invalid-selection"
	}
	if r.Tier != nil && !slices.Contains(c.Tiers, *r.Tier) {
		return "tier-not-admitted"
	}
	for _, s := range r.Skills {
		if !slices.Contains(c.Skills, s) {
			return "skill-not-admitted"
		}
	}
	xs, _ := routeList(r.Tools)
	for _, name := range xs {
		if !slices.ContainsFunc(c.Tools, func(t RouteTool) bool { return t.Name == name }) {
			return "tool-not-admitted"
		}
	}
	ops, _ := routeList(r.Operations)
	for _, op := range ops {
		if !slices.ContainsFunc(c.Tools, func(t RouteTool) bool { return t.Operation == op }) {
			return "operation-not-admitted"
		}
	}
	if r.Planning == "offered" && !c.Planning {
		return "planner-not-admitted"
	}
	if r.Route == "direct" && c.Native {
		return "native-direct-unsupported"
	}
	return ""
}
func MatchesRouteRule(r RouteRule, f RouteFacts) bool {
	b, _ := json.Marshal(f)
	held := map[string]any{}
	_ = json.Unmarshal(b, &held)
	for k, v := range r.When {
		if k == "maxTurnBytes" {
			n, ok := v.(float64)
			if !ok {
				if i, yes := v.(int); yes {
					n = float64(i)
				}
			}
			if float64(f.TurnBytes) > n {
				return false
			}
		} else if fmt.Sprint(held[k]) != fmt.Sprint(v) {
			return false
		}
	}
	return true
}
func DecideTurnRoute(p RoutingPolicy, c RouteCatalogue, f RouteFacts, binding any, host *RouteSelection, classifier *RouteClassifierRecord) (TurnRoute, error) {
	base := TurnRoute{Version: 1, BindingDigest: RouteDigest(binding), CatalogueDigest: RouteDigest(c), Refusals: []RouteRefusal{}, Classifier: classifier}
	if !ValidRoutingPolicy(p) {
		return base, ErrTurnRouteInvalid
	}
	finish := func(r RouteSelection, source, reason string) TurnRoute {
		base.RouteSelection = r
		base.DecidedBy = source
		base.Reason = reason
		copy, _ := routeJSON[TurnRoute](base)
		return copy
	}
	if p.Mode == "off" {
		return finish(FullRoute(c), "default", "off"), nil
	}
	selected := host
	source, reason := "host", "host"
	if selected == nil {
		for _, r := range p.Rules {
			if MatchesRouteRule(r, f) {
				copy := r.Route
				selected = &copy
				source = "rule"
				reason = r.ID
				break
			}
		}
		if selected == nil && p.Mode == "auto" && classifier != nil {
			source = "classifier"
			reason = classifier.Label
			if reason == "" {
				reason = classifier.Status
			}
			if classifier.Status == "label" && p.Classifier != nil {
				if r, ok := p.Classifier.Labels[classifier.Label]; ok {
					selected = &r
				}
			}
			if selected == nil {
				r := classifier.Status
				if r == "label" {
					r = "unknown-label"
				}
				base.Refusals = append(base.Refusals, RouteRefusal{Stage: "classifier", Reason: r})
			}
		}
	}
	if selected != nil {
		if problem := RouteSelectionProblem(*selected, c); problem != "" {
			base.Refusals = append(base.Refusals, RouteRefusal{Stage: source, Reason: problem})
		} else {
			return finish(*selected, source, reason), nil
		}
	}
	reason = "full"
	if len(base.Refusals) > 0 {
		reason = "selection-refused"
	}
	return finish(FullRoute(c), "default", reason), nil
}
func RouteOffers(r TurnRoute, c RouteCatalogue, name string) bool {
	for _, t := range c.Tools {
		if t.Name == name {
			return t.Operation == "ask" && t.Effect == "pure" && t.Interaction == "question" || r.Route != "direct" && routeContains(r.Tools, name) && routeContains(r.Operations, t.Operation) && (name != "update_plan" || r.Planning == "offered")
		}
	}
	return false
}
func RestoreTurnRoute(value any, c RouteCatalogue, binding any) (TurnRoute, error) {
	r, e := routeJSON[TurnRoute](value)
	if e != nil || !ValidTurnRoute(r) || r.CatalogueDigest != RouteDigest(c) || r.BindingDigest != RouteDigest(binding) || RouteSelectionProblem(r.RouteSelection, c) != "" {
		return r, ErrTurnRouteIdentityChanged
	}
	return r, nil
}
func WidenedTurnRoute(r TurnRoute, c RouteCatalogue, name string) (TurnRoute, error) {
	if r.Widened || RouteOffers(r, c, name) || !slices.ContainsFunc(c.Tools, func(t RouteTool) bool { return t.Name == name }) && !slices.Contains(c.Skills, name) {
		return r, ErrTurnRouteWidenRefused
	}
	r.RouteSelection = FullRoute(c)
	r.Widened = true
	r.WidenedFor = name
	return r, nil
}

// RoutingPolicyFromMembers applies the same member precedence as all other policy sections.
func RoutingPolicyFromMembers(m PolicyMembers) (*RoutingPolicy, error) {
	mode, _ := m["routing.mode"].Value.(string)
	if mode == "" || mode == "off" {
		return nil, nil
	}
	v := map[string]any{"mode": mode}
	for _, name := range []string{"tiers", "rules", "classifier"} {
		if x, ok := m["routing."+name]; ok {
			v[name] = x.Value
		}
	}
	p, e := ReadRoutingPolicy(v)
	return &p, e
}
