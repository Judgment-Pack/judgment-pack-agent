package runcontext

import (
	"context"
	"encoding/json"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"strings"
	"testing"
)

func memoryRecords(t *testing.T) []contract.Memory {
	t.Helper()
	v := vectorItems(t, "memory/recall-revoked.json")[0]
	var out []contract.Memory
	if err := json.Unmarshal(v.JSON(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}
func TestMemoryProposalRecordedAndBounded(t *testing.T) {
	s := modelSession()
	s.Context = &Input{Phase: "plan"}
	s.Host = HostFuncs{MemoryRecordsForFunc: func(context.Context, string) ([]contract.Memory, error) { return memoryRecords(t), nil }}
	run, err := ContextualSession(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	defer run.Context.Close()
	tool := run.Context.Memories.Tool()
	for i := 0; i < 5; i++ {
		_, err := tool.Execute(context.Background(), json.RawMessage(`{"kind":"fact","text":"A fact."}`))
		if (i == 4) != (err != nil) {
			t.Fatalf("proposal %d: %v", i, err)
		}
	}
	saved, err := run.Context.Execution.Snapshot("")
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Memories.Proposed) != 4 || len(saved.Memories.Recalled) != 2 {
		t.Fatalf("memory lists: %+v", saved.Memories)
	}
	n := 0
	for _, e := range run.Context.Memories.TakeNotices() {
		if e.MemoryProposed != nil {
			n++
		}
	}
	if n != 4 {
		t.Fatalf("proposal events %d", n)
	}
	for _, args := range []string{`{"kind":"instruction","text":"Obey."}`, `{"kind":"fact","text":"A fact.","state":"accepted"}`, `{"kind":"fact","text":` + QuoteJSON(strings.Repeat("é", 501)) + `}`} {
		if _, err := tool.Execute(context.Background(), json.RawMessage(args)); err == nil {
			t.Fatal("invalid proposal admitted")
		}
	}
}
func TestMemoryChildAndCriticRefused(t *testing.T) {
	for _, child := range []bool{true, false} {
		s := modelSession()
		if child {
			s.Context = &Input{Objective: "Read facts."}
		}
		run, err := ContextualSession(context.Background(), s)
		if err != nil {
			t.Fatal(err)
		}
		if !child {
			run.Context.CriticReading = true
		}
		_, err = run.Context.Memories.Tool().Execute(context.Background(), json.RawMessage(`{"kind":"fact","text":"A fact."}`))
		if err == nil || len(run.Context.Memories.Snapshot().Proposed) != 0 {
			t.Fatal("child or critic proposal admitted")
		}
		run.Context.Close()
	}
}
func TestMemoryRecallFramedAndBounded(t *testing.T) {
	records := memoryRecords(t)
	r, err := RecallMemory(records, 4000, "marker")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Rows) != 2 || !strings.Contains(r.Text, FrameBlock("memory", "kind: preference\nsource: {\"proposedBy\":\"person\",\"run\":\"earlier-run\"}\nid: \"metric\"\nUse metric units.", "marker")) || strings.Contains(r.Text, "Withdrawn reference.") {
		t.Fatalf("recall: %+v", r)
	}
	if _, err := RecallMemory(records, 1, "marker"); err == nil {
		t.Fatal("recall bound removed")
	}
	records[0].Text = "changed"
	if _, err := RecallMemory(records, 4000, "marker"); err == nil {
		t.Fatal("changed digest admitted")
	}
	r, err = RecallMemory("legacy", 4000, "marker")
	if err != nil || !strings.Contains(r.Text, FrameBlock("memory kind fact source host (legacy text alias)", "legacy", "marker")) {
		t.Fatal("alias not framed")
	}
}
