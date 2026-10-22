package runcontext

import (
	"context"
	"encoding/json"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"os"
	"reflect"
	"strings"
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
			if got := result.Members["models."+v.Role+".provider"]; !reflect.DeepEqual(got, PolicyMember{Value: v.Expected, Layer: "run", Source: "layer"}) {
				t.Fatalf("provider: %+v", got)
			}
		})
	}
	t.Run("default despite model", func(t *testing.T) {
		result, err := ResolvePolicy(PolicyLayer{"models": map[string]any{"critic": map[string]any{"model": "reviewer"}}}, nil, table, host)
		if err != nil {
			t.Fatal(err)
		}
		if got := result.Members["models.critic.provider"]; !reflect.DeepEqual(got, PolicyMember{Value: "author", Layer: "table", Source: "layer"}) {
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

func TestResumeRefusesReboundFamily(t *testing.T) {
	for _, role := range []string{"author", "critic", "child.review"} {
		t.Run(role, func(t *testing.T) {
			session := modelSession()
			session.Policy = PolicyLayer{"models": map[string]any{role: map[string]any{"provider": "saved"}}}
			session.PolicyConnections = []string{"small", "saved"}
			session.PolicyFamilies = map[string]string{"small": "openai", "saved": "openai"}
			session.Context = &Input{CurrentRequest: &CurrentRequest{Message: "Continue.", Input: "Continue."}}
			run, err := ContextualSession(context.Background(), session)
			if err != nil {
				t.Fatal(err)
			}
			run.Context.RecordProvider("author")
			run.Context.RecordProvider(role)
			saved, err := run.Context.Snapshot("stopped")
			run.Context.Close()
			if err != nil {
				t.Fatal(err)
			}
			session.Context.Execution = &saved
			session.PolicyFamilies = map[string]string{"small": "openai", "saved": "anthropic"}
			reopened, err := ContextualSession(context.Background(), session)
			if err == nil {
				reopened.Context.Close()
				t.Fatal("cross-family continuation admitted")
			}
			events := FailedToOpen(err)
			if events[0].Guardrail == nil || *events[0].Guardrail.Reason != "policy refused" || events[0].Guardrail.Tool != "models."+role+".provider" || !strings.Contains(events[0].Guardrail.Detail, "cross-family continuation") {
				t.Fatalf("refusal: %+v", events[0])
			}
			if len(events) < 2 || events[1].ExecutionCheckpoint == nil {
				t.Fatal("saved report missing")
			}
			stopped := events[1].ExecutionCheckpoint.Checkpoint
			if stopped.Status != "stopped" {
				t.Fatal(stopped.Status)
			}
			for i, row := range stopped.CrossProvider {
				if row.Continued == nil || *row.Continued || row.Provider != saved.CrossProvider[i].Provider || row.Author != saved.CrossProvider[i].Author {
					t.Fatalf("report relabelled: %+v", row)
				}
			}
			session.PolicyFamilies = map[string]string{"small": "openai", "saved": "openai"}
			same, err := ContextualSession(context.Background(), session)
			if err != nil {
				t.Fatal(err)
			}
			same.Context.Close()
		})
	}
}
