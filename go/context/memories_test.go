package runcontext

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
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
	if _, err = RecallMemory("legacy", 4000, "marker"); err == nil {
		t.Fatal("retired text alias admitted")
	}
}

func TestMemoryAliasEquivalence(t *testing.T) {
	vector := readJSON(t, "contract/vectors/memory/alias-equivalence.json")
	expected := vector.Get("expected")
	bound := int(expected.Get("bytes").Num())
	var outputs []string
	for _, item := range vector.Get("items").Items() {
		var value any
		var records []contract.Memory
		if item.IsString() {
			value = item.Str()
		} else {
			if err := json.Unmarshal(item.JSON(), &records); err != nil {
				t.Fatal(err)
			}
			value = records
		}
		recalled, err := RecallMemory(value, bound, "marker")
		if err != nil {
			t.Fatal(err)
		}
		if recalled.Bytes != bound || recalled.Text != FrameBlock("memory", expected.Get("frameBody").Str(), "marker") {
			t.Fatalf("alias accounting/framing: %+v", recalled)
		}
		if _, err := RecallMemory(value, bound-1, "marker"); err == nil {
			t.Fatal("alias budget not enforced")
		}
		host := HostFuncs{}
		if item.IsString() {
			host.MemoryRecordsForFunc = func(context.Context, string) ([]contract.Memory, error) { return typedMemory(item.Str()), nil }
		} else {
			host.MemoryRecordsForFunc = func(context.Context, string) ([]contract.Memory, error) { return records, nil }
		}
		parts, err := readHostParts(context.Background(), host, "q", Defaults, true, "marker", nil)
		if err != nil {
			t.Fatal(err)
		}
		parts.Brief, parts.ProjectInstructions = "brief", "project"
		outputs = append(outputs, SystemText("guidance", parts, "\ntools"))
	}
	if outputs[0] != outputs[1] {
		t.Fatal("alias placement differs")
	}
	if strings.Index(outputs[0], "END PROJECT INSTRUCTIONS") >= strings.Index(outputs[0], "BEGIN RETRIEVED DATA") || strings.Index(outputs[0], "END PROJECT MEMORY") >= strings.Index(outputs[0], "\ntools") {
		t.Fatal("memory moved from layer 5")
	}
	for _, part := range HostPartsTable {
		if part.Name == "memory" && part.Layer != int(expected.Get("layer").Num()) {
			t.Fatal("memory layer differs")
		}
	}
}
func TestMemoryValidationBoundaries(t *testing.T) {
	vector := readJSON(t, "contract/vectors/memory/validation.json")
	check := func(v *Value, accepted bool) {
		t.Helper()
		var m contract.Memory
		if err := json.Unmarshal(v.JSON(), &m); err != nil {
			t.Fatal(err)
		}
		if validMemory(m) != accepted {
			t.Errorf("validation %s: want %v", v.JSON(), accepted)
		}
		_, err := RecallMemory([]contract.Memory{m}, 4000, "marker")
		if (err == nil) != accepted {
			t.Errorf("recall validation %s: %v", v.JSON(), err)
		}
		m.State = "proposed"
		raw, _ := json.Marshal(contract.ProvenanceMemories{Proposed: []contract.Memory{m}, Recalled: []contract.ProvenanceMemoryRecall{}})
		book, _ := ParseJSON(raw)
		if ValidMemories(book) != accepted {
			t.Errorf("checkpoint validation %s: want %v", raw, accepted)
		}
	}
	for _, v := range vector.Get("items").Items() {
		check(v, true)
	}
	for _, v := range vector.Get("refused").Items() {
		check(v.Get("record"), false)
	}
	for _, id := range []string{strings.Repeat("é", 100), strings.Repeat("😀", 100), strings.Repeat("é", 101), strings.Repeat("😀", 101), ""} {
		raw, _ := json.Marshal(contract.ProvenanceMemories{Proposed: []contract.Memory{}, Recalled: []contract.ProvenanceMemoryRecall{{ID: id, Digest: vector.Get("items").Items()[0].Get("digest").Str(), Kind: "fact", State: "accepted"}}})
		book, _ := ParseJSON(raw)
		if ValidMemories(book) != (len([]rune(id)) > 0 && len([]rune(id)) <= 100) {
			t.Errorf("recalled identifier validation %q", id)
		}
	}
}

func TestMemoryEnumVectors(t *testing.T) {
	for _, book := range vectorItems(t, "memory/report.json") {
		if !ValidMemories(book) {
			t.Fatal("report enum vector refused")
		}
	}
	if vectorItems(t, "memory/reserved-name.json")[0].Str() != Remember {
		t.Fatal("reserved name differs")
	}
	run, err := ContextualSession(context.Background(), modelSession())
	if err != nil {
		t.Fatal(err)
	}
	defer run.Context.Close()
	for _, args := range vectorItems(t, "memory/remember.json") {
		if _, err := run.Context.Memories.Tool().Execute(context.Background(), args.JSON()); err != nil {
			t.Fatal(err)
		}
	}
	if len(run.Context.Memories.Snapshot().Proposed) != 4 {
		t.Fatal("remember kinds not all recorded")
	}
}
