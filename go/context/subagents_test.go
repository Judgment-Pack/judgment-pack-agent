package runcontext

import (
	"encoding/json"
	"fmt"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"os"
	"strings"
	"testing"
)

func subagentFixture(t *testing.T) Subagent {
	t.Helper()
	raw, err := os.ReadFile("../../contract/vectors/subagents/declaration.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct{ Items []Subagent }
	if err = json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return v.Items[0]
}
func TestSubagentAdmission(t *testing.T) {
	s := subagentFixture(t)
	tool := HostTool{Name: "lookup", Taxonomy: &ToolTaxonomy{Operation: "read", Effect: "read"}, Effect: "read"}
	if SubagentDigest(s) != s.PinnedDigest {
		t.Fatal("canonical digest differs")
	}
	admit := func(s Subagent, tools []HostTool, p Policy) ([]Subagent, []contract.RunEvent) {
		t.Helper()
		a, n, e := DeclareSubagents([]Subagent{s}, tools, nil, p)
		if e != nil {
			t.Fatal(e)
		}
		return a, n
	}
	for _, bound := range []int64{1, 112, 113} {
		held := s
		held.Bounds.ResultBytes = bound
		held.PinnedDigest = SubagentDigest(held)
		a, n := admit(held, []HostTool{tool}, Defaults)
		if bound == 113 {
			if len(a) != 1 {
				t.Fatal("minimum result bound withheld")
			}
		} else if len(a) != 0 || len(n) != 1 || n[0].Guardrail.Reason == nil || *n[0].Guardrail.Reason != "subagent withheld" || !strings.Contains(n[0].Guardrail.Detail, fmt.Sprintf("resultBytes %d", bound)) || !strings.Contains(n[0].Guardrail.Detail, "minimum 113") {
			t.Fatalf("unrepresentable resultBytes %d admitted: %v", bound, n)
		}
	}
	a, n := admit(s, []HostTool{tool}, Defaults)
	if len(a) != 1 || len(n) != 0 {
		t.Fatal("not admitted", n)
	}
	record := SubagentRecord(a[0])
	if record == nil || record.ID != s.ID || record.Digest != s.PinnedDigest || record.Source.ID != s.Source.ID {
		t.Fatal("record not carried")
	}
	if a, notices := admit(s, nil, Defaults); len(a) != 0 || len(notices) != 1 || !strings.Contains(notices[0].Guardrail.Detail, "undeclared tool") {
		t.Fatal("undeclared tool admitted")
	}
	s.Instructions += " Changed."
	if a, _ := admit(s, []HostTool{tool}, Defaults); len(a) != 0 {
		t.Fatal("changed digest admitted")
	}
	s.PinnedDigest = SubagentDigest(s)
	if a, _ := admit(s, []HostTool{tool}, Defaults); len(a) != 1 {
		t.Fatal("reapproval withheld")
	}
	s = subagentFixture(t)
	s.Skills = []string{"missing"}
	s.PinnedDigest = SubagentDigest(s)
	if a, _ := admit(s, []HostTool{tool}, Defaults); len(a) != 0 {
		t.Fatal("undeclared skill admitted")
	}
	for _, change := range []func(*Subagent){func(s *Subagent) { s.Bounds.Bytes = 180001 }, func(s *Subagent) { s.Bounds.ModelRequests = 11 }, func(s *Subagent) { s.Bounds.AgentRuns = 6 }, func(s *Subagent) { s.Bounds.ToolCalls = 13 }, func(s *Subagent) { s.Bounds.Milliseconds = 360001 }, func(s *Subagent) { s.Bounds.ResultBytes = 12001 }} {
		s = subagentFixture(t)
		change(&s)
		s.PinnedDigest = SubagentDigest(s)
		if a, _ := admit(s, []HostTool{tool}, Defaults); len(a) != 0 {
			t.Fatal("bounds ceiling removed")
		}
	}
	for _, id := range []string{"analyze", "review", "research", "author-test"} {
		s = subagentFixture(t)
		s.ID = id
		s.PinnedDigest = SubagentDigest(s)
		if a, _ := admit(s, []HostTool{tool}, Defaults); len(a) != 0 {
			t.Fatal("built-in altered")
		}
	}
	section, kept, cut := CatalogueSubagents(a, Defaults, "marker")
	if len(kept) != 1 || len(cut) != 0 || !strings.Contains(section, "marker") {
		t.Fatal("catalogue", section)
	}
	p := Defaults
	p.SubagentCatalogueBytes = 1
	_, kept, _ = CatalogueSubagents(a, p, "marker")
	if len(kept) != 0 {
		t.Fatal("catalogue byte bound")
	}
}
