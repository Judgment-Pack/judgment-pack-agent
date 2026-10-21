package runcontext

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestOutputSharedVectors(t *testing.T) {
	files, err := filepath.Glob("../../contract/vectors/output/*.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var vector struct {
			Guard *struct {
				Input, Output, Negative, Where, Mode, Refused string
				Allow                                         []string
				Patterns                                      []OutputPattern
				Counts                                        map[string]int64
			}
		}
		if err = json.Unmarshal(raw, &vector); err != nil {
			t.Fatal(err)
		}
		if vector.Guard == nil {
			continue
		}
		t.Run(filepath.Base(file), func(t *testing.T) {
			v := vector.Guard
			counts := map[string]int64{}
			allow := v.Allow
			if allow == nil {
				allow = []string{}
			}
			patterns := v.Patterns
			if patterns == nil {
				patterns = []OutputPattern{}
			}
			g, err := NewOutputGuard(PolicyMembers{"outputGuard": {Value: v.Mode}, "outputPatterns": {Value: patterns}, "outputAllow": {Value: allow}}, func() map[string]int64 { return counts })
			if err != nil {
				t.Fatal(err)
			}
			got, err := g.Text(v.Input, v.Where, true)
			if v.Refused != "" {
				if err == nil || err.Error() != (&OutputRefused{v.Refused}).Error() {
					t.Fatalf("refusal: %v", err)
				}
			} else if err != nil || got != v.Output {
				t.Fatalf("got %q, %v; want %q", got, err, v.Output)
			}
			if !reflect.DeepEqual(counts, v.Counts) {
				t.Fatalf("counts %v want %v", counts, v.Counts)
			}
			if v.Negative != "" {
				got, err = g.Text(v.Negative, v.Where, true)
				if err != nil || got != v.Negative || !reflect.DeepEqual(counts, v.Counts) {
					t.Fatalf("negative %q %v %v", got, err, counts)
				}
			}
		})
	}
}

func TestOutputValidationRefusals(t *testing.T) {
	schemaRaw, err := os.ReadFile("../../contract/schemas/run-event.json")
	if err != nil {
		t.Fatal(err)
	}
	schema, _ := ParseJSON(schemaRaw)
	for _, response := range []*Value{nil, Object(), ObjectOf("status", String("")), ObjectOf("status", String(strings.Repeat("x", 10000)))} {
		s := modelSession()
		s.Context = &Input{AcceptedDocuments: []AcceptedDocument{{ID: "pack", Document: json.RawMessage(`{"title":"Before"}`)}}}
		s.Tools = []RuntimeTool{{Name: "validate"}}
		calls := 0
		s.CallTool = func(_ context.Context, _ string, args json.RawMessage) (ToolResult, error) {
			calls++
			if strings.Contains(string(args), "AKIAABCDEFGHIJKLMNOP") || !strings.Contains(string(args), "[redacted: aws-access-key]") {
				t.Fatalf("validation did not see hole: %s", args)
			}
			return Answer(response), nil
		}
		opened, err := ContextualSession(context.Background(), s)
		if err != nil {
			t.Fatal(err)
		}
		g := opened.Context.Drafts
		if err = g.SeedContext(context.Background(), json.RawMessage(`{"title":"AKIAABCDEFGHIJKLMNOP"}`), nil); err == nil {
			t.Fatal("invalid proposal delivered")
		}
		if _, err = g.Edit(context.Background(), "propose_edit", json.RawMessage(`{"document":"pack","old_text":"Before","new_text":"AKIAABCDEFGHIJKLMNOP"}`)); err == nil {
			t.Fatal("invalid draft delivered")
		}
		if calls != 2 || !strings.Contains(string(g.Proposal().Document), "Before") {
			t.Fatal("validation or atomicity lost")
		}
		for _, event := range opened.Context.Output.TakeNotices() {
			raw, _ := json.Marshal(event)
			value, _ := ParseJSON(raw)
			if problem := ArgumentProblem(schema.Get("$defs").Get(value.Get("type").Str()), value); problem != "" {
				t.Fatal(problem)
			}
		}
		opened.Context.Close()
	}
}

func TestOutputMalformedRefusals(t *testing.T) {
	schemaRaw, err := os.ReadFile("../../contract/schemas/run-event.json")
	if err != nil {
		t.Fatal(err)
	}
	schema, _ := ParseJSON(schemaRaw)
	for _, input := range []json.RawMessage{nil, json.RawMessage(``), json.RawMessage(`{"secret":"AKIAABCDEFGHIJKLMNOP"}`), json.RawMessage(`{"oversized":"` + strings.Repeat("x", 200000) + `"}`)} {
		counts := map[string]int64{}
		g, err := NewOutputGuard(PolicyMembers{"outputGuard": {Value: "refuse"}}, func() map[string]int64 { return counts })
		if err != nil {
			t.Fatal(err)
		}
		if _, err = g.JSON(input, "tool argument", false); err == nil {
			t.Fatal("malformed or sensitive output was released")
		}
		notices := g.TakeNotices()
		if len(notices) != 2 {
			t.Fatalf("refusal notices: %d", len(notices))
		}
		for _, event := range notices {
			raw, _ := json.Marshal(event)
			value, _ := ParseJSON(raw)
			if problem := ArgumentProblem(schema.Get("$defs").Get(value.Get("type").Str()), value); problem != "" {
				t.Fatal(problem)
			}
		}
	}
}
