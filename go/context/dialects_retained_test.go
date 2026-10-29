package runcontext

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

func retainedPolicyFixture(t *testing.T) []DialectResearchRevision {
	t.Helper()
	raw, e := os.ReadFile("../../contract/testdata/dialect-authoring/retained-policy.json")
	if e != nil {
		t.Fatal(e)
	}
	var f struct {
		ResearchDeclarations []DialectResearchRevision `json:"researchDeclarations"`
	}
	if json.Unmarshal(raw, &f) != nil {
		t.Fatal("bad handwritten inventory")
	}
	return f.ResearchDeclarations
}
func dialectTestIndex(t *testing.T, inventory []DialectResearchRevision) *DialectResearchIndex {
	t.Helper()
	index, err := DialectResearchIndexOf(inventory)
	if err != nil {
		t.Fatal(err)
	}
	return index
}
func TestDialectRetainedPolicy(t *testing.T) {
	for _, kind := range []string{"text", "decimal", "number", "boolean", "research-value"} {
		t.Run(kind, func(t *testing.T) {
			w := newDialectWorld(t)
			w.declarations = retainedPolicyFixture(t)
			a := w.args(w.read())
			c := DialectConversion{Kind: kind}
			if kind == "boolean" {
				c.TrueValue = dialectStringPointer("yes")
				c.FalseValue = dialectStringPointer("no")
			}
			a["facts"] = []DialectFactBinding{{Target: "/amount", Column: "typed", Conversion: c}}
			if _, e := w.propose(a); e != nil {
				t.Fatal(e)
			}
			candidate := w.retained[0]
			var m map[string]any
			_ = json.Unmarshal([]byte(candidate.DocumentJSON), &m)
			conversion := map[string]any{"contractVersion": "1", "column": "typed", "kind": kind}
			if kind == "boolean" {
				conversion["trueValue"] = "yes"
				conversion["falseValue"] = "no"
			}
			expected := map[string]any{"facts": []any{map[string]any{"target": "/amount", "source": "/retained", "conversion": conversion}}, "evidence": []any{map[string]any{"requirement": "receipt", "source": "/retained", "availability": map[string]any{"contractVersion": "1", "column": "availability"}}}}
			if !reflect.DeepEqual(m["case"], expected) {
				t.Fatal("declared conversion leaves differ", m)
			}
			index := dialectTestIndex(t, w.declarations)
			if !reflect.DeepEqual(candidate.ResearchIndex, index) || candidate.ResearchDeclarations != nil || candidate.Version != 2 {
				t.Fatal("lost complete original declaration inventory")
			}
			for _, d := range w.drafts {
				if !reflect.DeepEqual(d.Candidate.ResearchIndex, index) || d.Version != 2 {
					t.Fatal("checkpoint lost original declarations")
				}
			}
			read, err := ReadDialectDeclarations(context.Background(), w.ports.ReadDeclarations, candidate.ID, candidate.PackKey, candidate.Revision, candidate.ResearchIndex)
			if err != nil || !reflect.DeepEqual(read, w.declarations) {
				t.Fatal("the index does not recover the exact original declarations", err)
			}
		})
	}
}
func TestDialectRetainedInventoryUnavailableAndPlanned(t *testing.T) {
	for _, inventory := range [][]DialectResearchRevision{nil, {}, {{Revision: "original:missing", Fields: nil}}} {
		w := newDialectWorld(t)
		w.declarations = inventory
		w.snapshot.RecordCount = 0
		for i := range w.snapshot.Columns {
			w.snapshot.Columns[i].Samples = []DialectSample{}
			w.snapshot.Columns[i].Research = nil
		}
		a := w.args(w.read())
		a["facts"] = []DialectFactBinding{{Target: "/amount", Column: "missing", Conversion: DialectConversion{Kind: "research-value"}}}
		if _, e := w.propose(a); e != nil {
			t.Fatal(e)
		}
		if !reflect.DeepEqual(w.retained[0].ResearchIndex, dialectTestIndex(t, inventory)) || (inventory == nil) != (w.retained[0].ResearchIndex == nil) || w.retained[0].Preview.RecordCount != 0 {
			t.Fatal("invented original declaration or readiness")
		}
	}
	for _, row := range []struct{ column, issue string }{{"amount/raw~value", "research-context"}, {"typed", "research-declaration"}, {"missing", "research-field"}, {"name", "research-metadata"}} {
		w := newDialectWorld(t)
		w.snapshot.Columns = append(w.snapshot.Columns, DialectColumn{Name: "name", Samples: []DialectSample{}})
		w.declarations = nil
		// Handwritten host preview seam; this is not actual Runner conversion evidence.
		w.ports.Preview = func(_ context.Context, _ DialectCandidate, f string) (DialectPreview, error) {
			return DialectPreview{Fingerprint: f, RecordCount: 1, RecordsWithIssues: 1, Issues: []DialectPreviewIssue{{RecordID: "actual-row:one", Issues: []string{row.issue}}}}, nil
		}
		w.tools = NewDialectTools(w.ports)
		a := w.args(w.read())
		a["facts"] = []DialectFactBinding{{Target: "/amount", Column: row.column, Conversion: DialectConversion{Kind: "research-value"}}}
		if _, e := w.propose(a); e != nil {
			t.Fatal(e)
		}
		if w.retained[0].Preview.Issues[0].Issues[0] != row.issue || w.retained[0].ResearchIndex != nil {
			t.Fatal("proposal/value issue distinction lost")
		}
	}
}
func TestDialectRetainedCapabilityIdentity(t *testing.T) {
	for _, drift := range []string{"missing-opt-in", "foreign-opt-in", "old-alpha", "discovery-spec", "checked-spec", "checked-role", "checked-profile", "checked-limits", "missing-operation", "malformed-limit"} {
		t.Run(drift, func(t *testing.T) {
			w := newDialectWorld(t)
			original := w.ports.Runtime
			if drift == "missing-opt-in" {
				w.ports.ConversionContract = nil
			}
			if drift == "foreign-opt-in" {
				c := DialectRetainedColumnDraft()
				c.Spec.Revision = strings.Repeat("a", 40)
				w.ports.ConversionContract = &c
			}
			w.ports.Runtime = func(c context.Context, name string, args map[string]string) (ToolResult, error) {
				result, e := original(c, name, args)
				if e != nil {
					return result, e
				}
				var r map[string]any
				_ = json.Unmarshal([]byte(result.Content[0].Value().Get("text").Str()), &r)
				if name == "get_dialect_contract" {
					switch drift {
					case "old-alpha":
						delete(r, "contract")
						delete(r, "spec")
						r["capabilities"] = map[string]any{"validationVersions": []int{2}, "packCheckVersions": []int{2}}
					case "discovery-spec":
						r["spec"].(map[string]any)["schemaSha256"] = "sha256:" + strings.Repeat("0", 64)
					case "missing-operation":
						r["capabilities"].(map[string]any)["operations"] = []string{"check"}
					case "malformed-limit":
						r["limits"].(map[string]any)["documentBytes"] = "1048576"
						r["capabilities"].(map[string]any)["conversionContracts"].([]any)[0].(map[string]any)["limits"].(map[string]any)["documentBytes"] = "1048576"
					}
				}
				if name == "dialect_validate" {
					switch drift {
					case "checked-spec":
						r["spec"].(map[string]any)["corpusSha256"] = "sha256:" + strings.Repeat("0", 64)
					case "checked-role":
						r["role"] = "preparation"
					case "checked-profile":
						r["profile"] = "extended"
					case "checked-limits":
						r["limits"].(map[string]any)["frameBytes"] = 2097153
					}
				}
				raw, _ := json.Marshal(r)
				return ResultOf([]Content{TextContent(string(raw))}, nil), nil
			}
			w.tools = NewDialectTools(w.ports)
			if _, e := w.propose(w.args(w.read())); e == nil {
				t.Fatal("exact conversion capability guard missing")
			}
			if len(w.drafts) == 0 || len(w.retained) != 0 || strings.Contains(strings.Join(w.calls, ","), "preview") {
				t.Fatal("capability drift crossed review boundary")
			}
		})
	}
}
func TestDialectRetainedMalformedDeclarations(t *testing.T) {
	for _, raw := range []string{`{"name":"typed","type":"text","name":"typed"}`, `{"name":"wrong","type":"text"}`, `{"name":"typed","type":null}`, `{"name":"typed","type":"text","data":"\ud800"}`} {
		w := newDialectWorld(t)
		w.declarations = []DialectResearchRevision{{Revision: "original", Fields: []DialectResearchField{{Name: "typed", FieldJSON: raw}}}}
		args, _ := json.Marshal(w.fixture.Request)
		if _, e := w.tools[0].Execute(context.Background(), args); e == nil {
			t.Fatal("malformed original declaration accepted", raw)
		}
	}
}
func TestDialectRetainedSelectedOccurrence(t *testing.T) {
	w := newDialectWorld(t)
	raw, e := os.ReadFile("../../contract/testdata/dialect-authoring/generated-read.json")
	if e != nil {
		t.Fatal(e)
	}
	var selected DialectRetainedRead
	if json.Unmarshal(raw, &selected) != nil {
		t.Fatal("bad template")
	}
	w.snapshot.RetainedRead = &selected
	if _, e = w.propose(w.args(w.read())); e != nil {
		t.Fatal(e)
	}
	candidate := w.retained[0]
	var mapping, original, plain map[string]any
	_ = json.Unmarshal([]byte(candidate.DocumentJSON), &mapping)
	_ = json.Unmarshal([]byte(selected.DocumentJSON), &original)
	_ = json.Unmarshal([]byte(w.fixture.ExpectedDocument), &plain)
	sources := mapping["sources"].([]any)
	want := original["sources"].([]any)
	if !reflect.DeepEqual(mapping["case"], map[string]any{"facts": []any{}, "evidence": []any{}}) || !reflect.DeepEqual(sources[1].(map[string]any)["read"].(map[string]any)["copy"], plain["case"]) {
		t.Fatal("generated read became asserted case")
	}
	want[1].(map[string]any)["read"].(map[string]any)["copy"] = plain["case"]
	if !reflect.DeepEqual(sources, want) || !reflect.DeepEqual(candidate.RetainedRead, &selected) {
		t.Fatal("selected occurrence changed original source order/profile/arguments/unwrap")
	}
}
func TestDialectRetainedCheckpointResume(t *testing.T) {
	w := newDialectWorld(t)
	w.declarations = retainedPolicyFixture(t)
	if _, e := w.propose(w.args(w.read())); e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(w.drafts[0])
	var checkpoint DialectDraftCheckpoint
	if json.Unmarshal(raw, &checkpoint) != nil {
		t.Fatal("checkpoint restore")
	}
	fresh := newDialectWorld(t)
	fresh.declarations = w.declarations
	if _, e := fresh.propose(fresh.args(checkpoint.Candidate.DialectIdentity)); e == nil {
		t.Fatal("restored factory omitted required fresh read")
	}
	if _, e := fresh.propose(fresh.args(fresh.read())); e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(fresh.retained[0].ResearchIndex, checkpoint.Candidate.ResearchIndex) {
		t.Fatal("resume lost inventory")
	}
}
func TestDialectRetainedDiscoveredCeiling(t *testing.T) {
	w := newDialectWorld(t)
	w.mode = "small-runtime-bound"
	if _, e := w.propose(w.args(w.read())); e == nil || !strings.Contains(e.Error(), "byte ceiling") {
		t.Fatal("missing byte ceiling refusal", e)
	}
	if strings.Contains(strings.Join(w.calls, ","), "dialect_validate") {
		t.Fatal("document sent above discovered byte ceiling")
	}
}
func TestDialectRetainedAmbiguousOccurrence(t *testing.T) {
	w := newDialectWorld(t)
	raw, e := os.ReadFile("../../contract/testdata/dialect-authoring/generated-read.json")
	if e != nil {
		t.Fatal(e)
	}
	var selected DialectRetainedRead
	_ = json.Unmarshal(raw, &selected)
	var m map[string]any
	_ = json.Unmarshal([]byte(selected.DocumentJSON), &m)
	sources := m["sources"].([]any)
	m["sources"] = append(sources, sources[1])
	raw, _ = json.Marshal(m)
	selected.DocumentJSON = string(raw)
	w.snapshot.RetainedRead = &selected
	if _, e = w.propose(w.args(w.read())); e == nil || !strings.Contains(e.Error(), "one existing copy read") {
		t.Fatal("ambiguous source occurrence accepted", e)
	}
}
