package runcontext

import (
	"encoding/json"
	"os"
	"testing"
)

func TestTurnRouteHandwrittenParity(t *testing.T) {
	raw, e := os.ReadFile("../../contract/fixtures/turn-route/decision.json")
	if e != nil {
		t.Fatal(e)
	}
	var v struct {
		Cases []struct {
			ID    string `json:"id"`
			Input struct {
				Policy     RoutingPolicy          `json:"policy"`
				Catalogue  RouteCatalogue         `json:"catalogue"`
				Facts      RouteFacts             `json:"facts"`
				Binding    any                    `json:"binding"`
				Host       *RouteSelection        `json:"host"`
				Classifier *RouteClassifierRecord `json:"classifier"`
			} `json:"input"`
			Expected struct {
				DecidedBy string `json:"decidedBy"`
				Reason    string `json:"reason"`
				Route     string `json:"route"`
				Widen     string `json:"widen"`
			} `json:"expected"`
		} `json:"cases"`
	}
	if e = json.Unmarshal(raw, &v); e != nil {
		t.Fatal(e)
	}
	for _, row := range v.Cases {
		t.Run(row.ID, func(t *testing.T) {
			i := row.Input
			r, e := DecideTurnRoute(i.Policy, i.Catalogue, i.Facts, i.Binding, i.Host, i.Classifier)
			if e != nil {
				t.Fatal(e)
			}
			if r.DecidedBy != row.Expected.DecidedBy || r.Reason != row.Expected.Reason || r.Route != row.Expected.Route {
				t.Fatalf("precedence changed: %+v", r)
			}
			if _, e = RestoreTurnRoute(r, i.Catalogue, i.Binding); e != nil {
				t.Fatal(e)
			}
			if _, e = RestoreTurnRoute(r, i.Catalogue, map[string]string{"other": "run"}); e == nil {
				t.Fatal("stale route restored")
			}
			if _, e = WidenedTurnRoute(r, i.Catalogue, "undeclared"); e == nil {
				t.Fatal("undeclared capability widened")
			}
			if name := row.Expected.Widen; name != "" {
				if RouteOffers(r, i.Catalogue, name) {
					t.Fatal("withheld capability offered")
				}
				wide, e := WidenedTurnRoute(r, i.Catalogue, name)
				if e != nil || wide.Route != "agent" || !wide.Widened || wide.Tools != "all" {
					t.Fatalf("widen failed: %+v %v", wide, e)
				}
				if _, e = WidenedTurnRoute(wide, i.Catalogue, "mutate"); e == nil {
					t.Fatal("route widened twice")
				}
				if _, e = WidenedTurnRoute(wide, i.Catalogue, "declared"); e == nil {
					t.Fatal("route widened twice for a declared skill")
				}
			}
		})
	}
}

func TestTurnRouteAskPureAndWidenedDisposition(t *testing.T) {
	c := RouteCatalogue{Tools: []RouteTool{{Name: "ask_user", Operation: "ask", Effect: "pure", Interaction: "question"}, {Name: "unsafe_ask", Operation: "ask", Effect: "write"}}, Skills: []string{}, Tiers: []string{"off"}}
	r, e := DecideTurnRoute(RoutingPolicy{Mode: "rules", Tiers: []string{"off"}}, c, RouteFacts{}, "binding", &RouteSelection{Route: "direct", Skills: []string{}, Tools: []string{}, Operations: []string{}, Planning: "withheld"}, nil)
	if e != nil || !RouteOffers(r, c, "ask_user") || RouteOffers(r, c, "unsafe_ask") || RouteOffers(r, c, "undeclared") {
		t.Fatalf("ask/pure admitted invariant: %v", e)
	}
	i := &RouteWidenedInvocation{DispatchID: "dispatch-original", RequestDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	r.WidenedInvocation = i
	if ValidTurnRoute(r) {
		t.Fatal("marker on narrow route accepted")
	}
	r.Widened = true
	r.WidenedFor = "ask_user"
	if !ValidTurnRoute(r) {
		t.Fatal("valid exact marker refused")
	}
	for _, raw := range []map[string]any{{"dispatchId": "", "requestDigest": i.RequestDigest}, {"dispatchId": i.DispatchID, "requestDigest": "bad"}, {"dispatchId": i.DispatchID, "requestDigest": i.RequestDigest, "extra": true}} {
		b, _ := json.Marshal(r)
		var v map[string]any
		json.Unmarshal(b, &v)
		v["widenedInvocation"] = raw
		if _, e := RestoreTurnRoute(v, c, "binding"); e == nil {
			t.Fatal("malformed marker restored")
		}
	}
	if _, e := RestoreTurnRoute(r, c, "other-execution"); e == nil {
		t.Fatal("marker widened stale identity")
	}
}
