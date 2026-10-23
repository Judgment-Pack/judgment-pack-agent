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

func TestRequestValuesSharedCompleteBook(t *testing.T) {
	data, err := os.ReadFile("../../contract/vectors/policy/request-session.json")
	if err != nil {
		t.Fatal(err)
	}
	var vector struct{ Items []PolicyMembers }
	if err = json.Unmarshal(data, &vector); err != nil {
		t.Fatal(err)
	}
	for _, members := range vector.Items {
		values := NewRequestValues(members, map[string]string{"compact": "anthropic"})
		for name, want := range members {
			if got := values.Member(name); !reflect.DeepEqual(got, want) {
				t.Fatalf("%s: got %#v want %#v", name, got, want)
			}
		}
		if values.Reasoning("author") != "" || values.Reasoning("compactor") != "" {
			t.Fatal("omitted reasoning was synthesized")
		}
		if provider := values.Provider("compactor"); provider.Connection != "compact" || provider.Family != "anthropic" {
			t.Fatalf("compactor projection: %+v", provider)
		}
		future := values.Member("future.request.option")
		future.Value.(map[string]any)["enabled"] = false
		if values.Member("future.request.option").Value.(map[string]any)["enabled"] != true {
			t.Fatal("member alias changed admitted value")
		}
	}
}

func TestCompactorUsesAdmittedRequestValues(t *testing.T) {
	for _, omitted := range []bool{false, true} {
		session := smallWindowSession()
		session.Policy = PolicyLayer{"compaction": "summary"}
		if omitted {
			session.Policy["models"] = map[string]any{"compactor": map[string]any{"discovered": map[string]any{"reasoning": map[string]any{"supported": []any{"high"}}}}}
		}
		session.Context.Configuration.Models["small"] = ModelBudget{ContextWindowTokens: 8192, OutputTokens: 1024}
		asked := false
		session.Context.Compactor = func(_ context.Context, request CompactorRequest) (string, error) {
			asked = true
			want := "low"
			if omitted {
				want = ""
			}
			if request.Reasoning != want || request.Model != "small" || request.Connection != "small" || request.Family != "openai" || request.MaxBytes != 4000 {
				t.Errorf("compactor request changed: %+v", request)
			}
			return "A bounded summary.", nil
		}
		opened, err := ContextualSession(context.Background(), session)
		if err != nil {
			t.Fatal(err)
		}
		values := opened.Context.RequestValues()
		if values != opened.Context.RequestValues() {
			t.Fatal("adapter and compactor projections differ")
		}
		history := []Message{TextMessage("user", "Keep this stable prefix.")}
		for i := 0; i < 25; i++ {
			history = append(history, TextMessage("assistant", strings.Repeat("earlier discussion ", 50)), TextMessage("user", "continue"))
		}
		if _, err = opened.Context.Messages(history, "host", noTools()); err != nil {
			t.Fatal(err)
		}
		opened.Context.Close()
		if !asked {
			t.Fatal("compactor was not called")
		}
	}
}

func TestRequestValuesKeepHostRecallOnceAcrossRequestSystems(t *testing.T) {
	session := modelSession()
	calls := 0
	session.Context = &Input{CurrentRequest: &CurrentRequest{Message: "Stable question.", Input: "Stable question."}}
	session.Host = HostFuncs{MemoryRecordsForFunc: func(context.Context, string) ([]contract.Memory, error) {
		calls++
		return typedMemory("Stable recalled fact."), nil
	}}
	opened, err := ContextualSession(context.Background(), session)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Context.Close()
	first, second := opened.Context.System("GUIDANCE", ""), opened.Context.System("GUIDANCE", "")
	if calls != 1 || first != second {
		t.Fatalf("request systems recalled host memory %d times or changed prefix", calls)
	}
}
