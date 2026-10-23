package runcontext

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
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
				member := got.Members[name]
				if member.Value != want.Value || member.Layer != want.Layer || member.Source != "layer" {
					t.Errorf("%s %s: got %+v want %+v", c.Name, name, got.Members[name], want)
				}
			}
		}
	})
	t.Run("reasoning defaults and discovery", func(t *testing.T) {
		raw, err := os.ReadFile("../../contract/vectors/policy/reasoning-default.json")
		if err != nil {
			t.Fatal(err)
		}
		var vector struct {
			Cases   []struct{ Run, Project, Table PolicyLayer }
			Items   []PolicyMembers
			Refusal struct {
				Run, Project, Table PolicyLayer
				Detail              string
			}
		}
		if err = json.Unmarshal(raw, &vector); err != nil {
			t.Fatal(err)
		}
		h := PolicyHost{Connections: []string{"model-a"}, Agent: true, Requests: 16}
		for i, input := range vector.Cases {
			got, resolveErr := ResolvePolicy(input.Run, input.Project, input.Table, h)
			want := vector.Items[i]["models.author.reasoning"]
			if resolveErr != nil || !reflect.DeepEqual(got.Members["models.author.reasoning"], want) {
				t.Fatalf("case %d: got %+v want %+v: %v", i, got.Members["models.author.reasoning"], want, resolveErr)
			}
		}
		if _, err = ResolvePolicy(vector.Refusal.Run, vector.Refusal.Project, vector.Refusal.Table, h); err == nil || err.Error() != vector.Refusal.Detail {
			t.Fatalf("unsupported effort: %v", err)
		}
	})
	t.Run("table reasoning defaults and explicit refusals", func(t *testing.T) {
		raw, err := os.ReadFile("../../contract/vectors/policy/reasoning-table-defaults.json")
		if err != nil {
			t.Fatal(err)
		}
		var vector struct {
			Cases []struct {
				Run, Project, Table PolicyLayer
				Member              string
			}
			Items    []PolicyMembers
			Refusals []struct {
				Name, Layer, Role, Value string
				Agent                    bool
			}
		}
		if err = json.Unmarshal(raw, &vector); err != nil {
			t.Fatal(err)
		}
		for i, input := range vector.Cases {
			got, resolveErr := ResolvePolicy(input.Run, input.Project, input.Table, PolicyHost{Connections: []string{"model-a"}, Requests: 16})
			want := vector.Items[i][input.Member]
			if resolveErr != nil || !reflect.DeepEqual(got.Members[input.Member], want) {
				t.Fatalf("case %d: got %+v want %+v: %v", i, got.Members[input.Member], want, resolveErr)
			}
		}
		for _, row := range vector.Refusals {
			role := map[string]any{"model": "model-a", "reasoning": row.Value, "discovered": map[string]any{"reasoning": map[string]any{"default": "high", "supported": []any{"high"}}}}
			layer := PolicyLayer{"models": map[string]any{row.Role: role}}
			author := map[string]any{"model": "model-a"}
			if !row.Agent {
				author["reasoning"] = "off"
			}
			tableLayer := PolicyLayer{"models": map[string]any{"author": author, row.Role: map[string]any{"model": "model-a"}}}
			var run, project PolicyLayer
			if row.Layer == "run" {
				run = layer
			} else {
				project = layer
			}
			_, resolveErr := ResolvePolicy(run, project, tableLayer, PolicyHost{Connections: []string{"model-a"}, Agent: row.Agent, Requests: 16})
			if resolveErr == nil || !strings.Contains(resolveErr.Error(), fmt.Sprintf("models.%s.reasoning (%s): reasoning value %q", row.Role, row.Layer, row.Value)) {
				t.Fatalf("%s: %v", row.Name, resolveErr)
			}
		}
	})
	t.Run("dropped table defaults stay absent", func(t *testing.T) {
		raw, err := os.ReadFile("../../contract/vectors/providers/reasoning-omission.json")
		if err != nil {
			t.Fatal(err)
		}
		var vector struct {
			Dropped []struct {
				Name, Role, Capability, Raw string
				Supported                   []any
				Resolved                    PolicyMember
			}
		}
		if err = json.Unmarshal(raw, &vector); err != nil {
			t.Fatal(err)
		}
		for _, row := range vector.Dropped {
			t.Run(row.Name, func(t *testing.T) {
				tableModels := map[string]any{"author": map[string]any{"model": "model-a", "reasoning": row.Raw}}
				if row.Role == "critic" {
					tableModels["critic"] = map[string]any{"model": "model-a"}
				}
				run := PolicyLayer{"models": map[string]any{row.Role: map[string]any{"discovered": map[string]any{"reasoning": map[string]any{"supported": row.Supported}}}}}
				got, resolveErr := ResolvePolicy(run, nil, PolicyLayer{"models": tableModels}, PolicyHost{Connections: []string{"model-a"}, Agent: row.Capability == "agent", Requests: 16})
				member := got.Members["models."+row.Role+".reasoning"]
				if resolveErr != nil || !reflect.DeepEqual(member, row.Resolved) {
					t.Fatalf("got %+v, want %+v: %v", member, row.Resolved, resolveErr)
				}
			})
		}
	})
	t.Run("discovery vector", func(t *testing.T) {
		raw, err := os.ReadFile("../../contract/vectors/policy/discovery.json")
		if err != nil {
			t.Fatal(err)
		}
		var vector struct{ Items []PolicyLayer }
		if err = json.Unmarshal(raw, &vector); err != nil {
			t.Fatal(err)
		}
		got, err := ResolvePolicy(vector.Items[0], nil, PolicyLayer{"models": map[string]any{"author": map[string]any{"model": "connected"}}}, PolicyHost{Connections: []string{"connected"}, Agent: true, Requests: 16})
		if err != nil {
			t.Fatal(err)
		}
		if member := got.Members["models.author.reasoning"]; member.Value != "high" || member.Source != "discovered" {
			t.Fatalf("discovery: %+v", member)
		}
		oversized := make([]any, 11)
		for i := range oversized {
			oversized[i] = "low"
		}
		for _, discovered := range []any{
			map[string]any{},
			map[string]any{"reasoning": map[string]any{}},
			map[string]any{"reasoning": map[string]any{"default": "low", "supported": oversized}},
		} {
			_, resolveErr := ResolvePolicy(PolicyLayer{"models": map[string]any{"author": map[string]any{"discovered": discovered}}}, nil, PolicyLayer{"models": map[string]any{"author": map[string]any{"model": "connected"}}}, PolicyHost{Connections: []string{"connected"}, Agent: true, Requests: 16})
			var refusal *PolicyRefusal
			if !errors.As(resolveErr, &refusal) {
				t.Fatalf("malformed discovery admitted: %#v", discovered)
			}
			eventJSON, marshalErr := json.Marshal(refusal.Event())
			var event contract.RunEvent
			if marshalErr != nil || json.Unmarshal(eventJSON, &event) != nil || event.Guardrail == nil {
				t.Fatalf("invalid refusal event for %#v: %s (%v)", discovered, eventJSON, marshalErr)
			}
		}
	})
	t.Run("reasoning resume vector", func(t *testing.T) {
		raw, err := os.ReadFile("../../contract/vectors/policy/reasoning-resume.json")
		if err != nil {
			t.Fatal(err)
		}
		var vector struct{ Items []json.RawMessage }
		if err = json.Unmarshal(raw, &vector); err != nil {
			t.Fatal(err)
		}
		value, err := ParseJSON(vector.Items[0])
		if err != nil {
			t.Fatal(err)
		}
		saved, err := ReadExecution(value)
		if err != nil {
			t.Fatal(err)
		}
		member := saved.Policy["models.author.reasoning"]
		if len(member.Value) != 0 || member.Source == nil || *member.Source != "model-default" || len(saved.Prompts) != 1 {
			t.Fatalf("resume vector: %+v", saved)
		}
	})
	t.Run("table reasoning resume vector", func(t *testing.T) {
		raw, err := os.ReadFile("../../contract/vectors/policy/reasoning-table-resume.json")
		if err != nil {
			t.Fatal(err)
		}
		var vector struct {
			Items   []json.RawMessage
			Members []PolicyMember
		}
		if err = json.Unmarshal(raw, &vector); err != nil {
			t.Fatal(err)
		}
		for i, item := range vector.Items {
			value, parseErr := ParseJSON(item)
			if parseErr != nil {
				t.Fatal(parseErr)
			}
			saved, readErr := ReadExecution(value)
			want := PolicyMembers{"member": vector.Members[i]}.Record()["member"]
			if readErr != nil || !reflect.DeepEqual(saved.Policy["models.compactor.reasoning"], want) || len(saved.Prompts) != 1 {
				t.Fatalf("case %d: %+v (%v)", i, saved, readErr)
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
	t.Run("exhausted allowance", func(t *testing.T) {
		for _, key := range []string{"requestsPerDay", "invocationsPerDay", "bytesPerDay"} {
			for _, limit := range []int64{0, 10} {
				h := host
				h.PaceUsed = map[string]int64{key: limit}
				_, err := ResolvePolicy(nil, PolicyLayer{"pace": map[string]any{key: limit}}, table, h)
				var refused *PolicyRefusal
				if !errors.As(err, &refused) || refused.FailureCode() != "pace exceeded" || refused.Member != "pace."+key {
					t.Fatalf("exhausted %s %d: %v", key, limit, err)
				}
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
	t.Run("omitted author has no invented child ceiling", func(t *testing.T) {
		for _, row := range []struct {
			agent bool
			value string
		}{{false, "off"}, {true, "none"}} {
			for _, role := range []string{"child.review", "subagent.audit"} {
				assigned := PolicyLayer{"models": map[string]any{role: map[string]any{"reasoning": row.value}}}
				omitted := PolicyLayer{"models": map[string]any{"author": map[string]any{"model": "connected"}, role: map[string]any{"model": "connected"}}}
				for _, project := range []PolicyLayer{nil, assigned} {
					got, err := ResolvePolicy(assigned, project, omitted, PolicyHost{Connections: []string{"connected"}, Agent: row.agent, Requests: 16})
					if err != nil || got.Members["models."+role+".reasoning"].Value != row.value {
						t.Fatalf("agent=%t role=%s project=%t: %+v %v", row.agent, role, project != nil, got, err)
					}
				}
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
	t.Run("fallback refusals are schema-valid", func(t *testing.T) {
		h := PolicyHost{Connections: []string{"connected", "other"}, Families: map[string]string{"connected": "openai", "other": "anthropic"}, Requests: 16}
		schema := readJSON(t, "contract/schemas/run-event.json").Get("$defs").Get("guardrail")
		for _, fallback := range []string{"", "missing", strings.Repeat("x", 129), "other"} {
			_, err := ResolvePolicy(PolicyLayer{"models": map[string]any{"author": map[string]any{"fallback": []any{fallback}}}}, nil, table, h)
			var refused *PolicyRefusal
			if !errors.As(err, &refused) {
				t.Fatalf("%q admitted: %v", fallback, err)
			}
			event := refused.Event()
			raw, marshalErr := json.Marshal(event)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			actual, parseErr := ParseJSON(raw)
			if parseErr != nil {
				t.Fatal(parseErr)
			}
			if got := ArgumentProblem(schema, actual); got != "" {
				t.Fatalf("%q emitted an invalid event: %s", fallback, got)
			}
			if event.Guardrail == nil || event.Guardrail.Reason == nil || *event.Guardrail.Reason != "failover refused" {
				t.Fatalf("%q emitted %+v", fallback, event)
			}
		}
	})
}
