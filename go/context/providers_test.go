package runcontext

import (
	"encoding/json"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"os"
	"reflect"
	"testing"
)

func TestProviders(t *testing.T) {
	table := PolicyLayer{"models": map[string]any{"author": map[string]any{"model": "author", "reasoning": "ultra"}}}
	host := PolicyHost{Connections: []string{"author", "reviewer"}, Families: map[string]string{"author": "openai", "reviewer": "anthropic"}, Requests: 2}
	for _, name := range []string{"same", "other", "explicit", "child", "subagent"} {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile("../../contract/vectors/providers/" + name + ".json")
			if err != nil {
				t.Fatal(err)
			}
			var v struct {
				Items          []PolicyLayer
				Role, Expected string
			}
			if err = json.Unmarshal(raw, &v); err != nil {
				t.Fatal(err)
			}
			result, err := ResolvePolicy(v.Items[0], nil, table, host)
			if err != nil {
				t.Fatal(err)
			}
			if got := result.Members["models."+v.Role+".provider"]; !reflect.DeepEqual(got, PolicyMember{v.Expected, "run"}) {
				t.Fatalf("provider: %+v", got)
			}
		})
	}
	t.Run("default despite model", func(t *testing.T) {
		result, err := ResolvePolicy(PolicyLayer{"models": map[string]any{"critic": map[string]any{"model": "reviewer"}}}, nil, table, host)
		if err != nil {
			t.Fatal(err)
		}
		if got := result.Members["models.critic.provider"]; !reflect.DeepEqual(got, PolicyMember{"author", "table"}) {
			t.Fatalf("provider: %+v", got)
		}
	})
	t.Run("unavailable", func(t *testing.T) {
		host.Connections = []string{"author"}
		_, err := ResolvePolicy(PolicyLayer{"models": map[string]any{"critic": map[string]any{"provider": "other"}}}, nil, table, host)
		refusal, ok := err.(*PolicyRefusal)
		if !ok {
			t.Fatalf("other fell back: %v", err)
		}
		var v struct{ Items []contract.RunEvent }
		raw, _ := os.ReadFile("../../contract/vectors/providers/unavailable.json")
		if err = json.Unmarshal(raw, &v); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(refusal.Event(), v.Items[0]) {
			t.Fatalf("refusal: %+v", refusal.Event())
		}
	})
	t.Run("failover", func(t *testing.T) {
		event := FailoverRefusal("critic", contract.ProvenanceProvider{Connection: "author", Family: "openai"}, contract.ProvenanceProvider{Connection: "reviewer", Family: "anthropic"})
		var v struct{ Items []contract.RunEvent }
		raw, _ := os.ReadFile("../../contract/vectors/providers/failover.json")
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Fatal(err)
		}
		if event == nil || !reflect.DeepEqual(*event, v.Items[0]) {
			t.Fatalf("failover: %+v", event)
		}
	})
}
