package runcontext

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestPolicyTable(t *testing.T) {
	table := PolicyLayer{"models": map[string]any{"author": map[string]any{"model": "connected", "reasoning": "off"}}}
	host := PolicyHost{Connections: []string{"connected"}, Requests: 16}
	t.Run("precedence and layers", func(t *testing.T) {
		raw, err := os.ReadFile("../../contract/vectors/policy/precedence.json")
		if err != nil {
			t.Fatal(err)
		}
		var vector struct {
			Cases []struct {
				Name                string
				Run, Project, Table PolicyLayer
			}
			Items []PolicyMembers
		}
		if err = json.Unmarshal(raw, &vector); err != nil {
			t.Fatal(err)
		}
		for i, c := range vector.Cases {
			got, err := ResolvePolicy(c.Run, c.Project, c.Table, host)
			if err != nil {
				t.Fatal(err)
			}
			for name, want := range vector.Items[i] {
				if !reflect.DeepEqual(got.Members[name], want) {
					t.Errorf("%s %s: got %+v want %+v", c.Name, name, got.Members[name], want)
				}
			}
		}
	})
	t.Run("alias", func(t *testing.T) {
		old := PolicyLayer{"readBytes": 512, "models": table["models"]}
		got, err := ResolvePolicy(nil, nil, old, host)
		if err != nil || got.Members["budget.readBytes"].Value != float64(512) {
			t.Fatalf("alias: %+v %v", got, err)
		}
	})
	t.Run("pace", func(t *testing.T) {
		project := PolicyLayer{"pace": map[string]any{"requestsPerDay": 10, "warnAt": 0.8}}
		for _, used := range []int64{7, 8, 11} {
			h := host
			h.PaceUsed = map[string]int64{"requestsPerDay": used}
			got, err := ResolvePolicy(nil, project, table, h)
			if used == 11 {
				if err == nil || !strings.Contains(err.Error(), "pace.requestsPerDay (project)") {
					t.Fatalf("pace refusal: %v", err)
				}
			} else if err != nil || (got.Warning != "") != (used == 8) {
				t.Fatalf("pace %d: %+v %v", used, got, err)
			}
		}
	})
	t.Run("tier ceiling", func(t *testing.T) {
		for _, role := range []string{"child.review", "subagent.audit"} {
			raised := PolicyLayer{"models": map[string]any{role: map[string]any{"reasoning": "ultra"}}}
			if _, err := ResolvePolicy(raised, nil, table, host); err == nil || !strings.Contains(err.Error(), "tier exceeds") {
				t.Fatalf("run raised tier: %v", err)
			}
			got, err := ResolvePolicy(nil, raised, table, host)
			if err != nil || got.Members["models."+role+".reasoning"].Layer != "project" {
				t.Fatalf("project tier: %+v %v", got, err)
			}
		}
	})
	t.Run("missing and unknown", func(t *testing.T) {
		if _, err := ResolvePolicy(nil, nil, nil, host); err == nil {
			t.Fatal("missing author accepted")
		}
		if _, err := ResolvePolicy(PolicyLayer{"budget": map[string]any{"unlimited": 1}}, nil, table, host); err == nil {
			t.Fatal("unknown budget member accepted")
		}
		if _, err := ResolvePolicy(PolicyLayer{"models": map[string]any{"critic": map[string]any{"model": "missing"}}}, nil, table, host); err == nil {
			t.Fatal("missing connection accepted")
		}
	})
	t.Run("schema copy", func(t *testing.T) {
		raw, err := os.ReadFile("../../contract/schemas/policy.json")
		if err != nil {
			t.Fatal(err)
		}
		if string(raw) != string(policySchemaJSON) {
			t.Fatal("embedded schema drift")
		}
	})
}
