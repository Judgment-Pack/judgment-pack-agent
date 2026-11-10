package runcontext

// The words a source's offered schemas keep as written (#308): enum and const values, names,
// required entries and $ref pointers cannot be framed where they stand, so they are bounded, and a
// source past a bound is made a catalogue whose schemas reach the model only inside load_tool's data
// block. contract/testdata/tool-offer/schema-bounds.json holds each bound at its limit and one past
// it; contract/vectors/tool-offer/source-schema-bounds.json an adversarial schema and what the model
// reads of it. toolSources.test.ts holds the TypeScript to the same files.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
)

// offeredAre holds the offer to what the model reads of each tool, byte for byte: its name, its
// description and its schema's JSON text, and the taxonomy beside them.
func offeredAre(t *testing.T, shown []HostTool, want []*Value) {
	t.Helper()
	if len(shown) != len(want) {
		names := []string{}
		for _, tool := range shown {
			names = append(names, tool.Name)
		}
		t.Fatalf("%d tools offered (%v), not %d", len(shown), names, len(want))
	}
	for at, tool := range shown {
		item := want[at]
		if tool.Name != item.Get("name").Str() || tool.Description != item.Get("description").Str() || string(tool.InputSchema) != item.Get("inputSchema").Text() {
			t.Errorf("offer %d is\n%s\n%q\n%s\nnot\n%s\n%q\n%s", at, tool.Name, tool.Description, tool.InputSchema, item.Get("name").Str(), item.Get("description").Str(), item.Get("inputSchema").Text())
		}
		taxonomy, err := json.Marshal(tool.Taxonomy)
		if err != nil {
			t.Fatal(err)
		}
		if CanonicalJSON(MustParseJSON(string(taxonomy))) != CanonicalJSON(item.Get("taxonomy")) {
			t.Errorf("offer %d's taxonomy is %s", at, taxonomy)
		}
	}
}

// boundsPolicy is the harness's defaults with the bounds a case sets.
func boundsPolicy(t *testing.T, set *Value) Policy {
	t.Helper()
	policy := Defaults
	for _, key := range set.Keys() {
		value := int(set.Get(key).Num())
		switch key {
		case "offeredSchemas":
			policy.OfferedSchemas = value
		case "offeredSchemaBytes":
			policy.OfferedSchemaBytes = value
		case "cataloguedTools":
			policy.CataloguedTools = value
		default:
			t.Fatalf("a case sets %s, which this test does not read", key)
		}
	}
	return policy
}

// rawSchemas are each case's declared input schemas as the file writes them: ParseJSON, which reads
// the rest, holds a lone surrogate escape as U+FFFD, and the offer reads a host's own bytes.
func rawSchemas(t *testing.T, name string) [][]json.RawMessage {
	t.Helper()
	text, err := os.ReadFile(filepath.Join(repositoryRoot(t), filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Cases []struct {
			Declared []struct {
				InputSchema json.RawMessage `json:"inputSchema"`
			} `json:"declared"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(text, &file); err != nil {
		t.Fatal(err)
	}
	out := make([][]json.RawMessage, len(file.Cases))
	for at, item := range file.Cases {
		for _, tool := range item.Declared {
			var compact bytes.Buffer
			if err := json.Compact(&compact, tool.InputSchema); err != nil {
				t.Fatal(err)
			}
			out[at] = append(out[at], json.RawMessage(compact.Bytes()))
		}
	}
	return out
}

// withheldAre holds the guardrails to the ones written, in order.
func withheldAre(t *testing.T, got []Withheld, want []*Value) {
	t.Helper()
	notes := []Withheld{}
	for _, held := range want {
		notes = append(notes, Withheld{Tool: held.Get("tool").Str(), Detail: held.Get("detail").Str()})
	}
	if !slices.Equal(append([]Withheld{}, got...), notes) {
		t.Errorf("the guardrails are\n%+v\nnot\n%+v", got, notes)
	}
}

func TestTheWordsASchemaKeepsAsWrittenAreBoundedAtTheLimitAndOnePast(t *testing.T) {
	const name = "contract/testdata/tool-offer/schema-bounds.json"
	data := readJSON(t, name)
	raw := rawSchemas(t, name)
	limits := data.Get("limits")
	for name, want := range map[string]int{"enumMembers": EnumMembers, "namesPerObject": NamesPerObject, "verbatimBytes": VerbatimBytes, "enumMemberBytes": EnumMemberBytes, "nameBytes": NameBytes} {
		if got := limits.Get(name); got == nil || int(got.Num()) != want {
			t.Errorf("the testdata is written to %s %v, and the package's is %d", name, got, want)
		}
	}
	marker := data.Get("marker").Str()
	cases := data.Get("cases").Items()
	if len(cases) == 0 {
		t.Fatal("no cases")
	}
	for at, item := range cases {
		// The declared tools with the schema bytes the file writes.
		declared := func(t *testing.T) []HostTool {
			tools := declaredTools(t, item.Get("declared").Items())
			for index := range tools {
				tools[index].InputSchema = raw[at][index]
			}
			return pinLocalFixture(tools)
		}
		t.Run(item.Get("what").Str(), func(t *testing.T) {
			out, err := DeclareSources(declared(t), nil)
			if err != nil || len(out.Withheld) != 0 {
				t.Fatalf("%v %+v", err, out.Withheld)
			}
			shown, catalogue, withheld := OfferSources(out.HostTools, out.Entries, boundsPolicy(t, item.Get("policy")), marker)
			if catalogue != item.Get("catalogue").Truth() {
				t.Errorf("catalogue %v", catalogue)
			}
			offeredAre(t, shown, item.Get("offered").Items())
			withheldAre(t, withheld, item.Get("withheld").Items())
			// What is offered is what the gate holds: a tool withheld cannot be loaded or called.
			names, held := []string{}, []string{}
			for _, tool := range shown {
				if tool.Name != LoadTool {
					names = append(names, tool.Name)
				}
			}
			for wire := range out.Entries {
				held = append(held, wire)
			}
			sort.Strings(names)
			sort.Strings(held)
			if !slices.Equal(names, held) {
				t.Errorf("the gate holds %v, and %v are offered", held, names)
			}
			// A Go host's declarations decide the same: load_tool is offered exactly where a source is a catalogue.
			declarations, err := OfferedHostTools(declared(t), nil, boundsPolicy(t, item.Get("policy")))
			if err != nil {
				t.Fatal(err)
			}
			if loads := slices.ContainsFunc(declarations, func(tool contract.ToolOfferHostTool) bool { return tool.Name == LoadTool }); loads != item.Get("catalogue").Truth() {
				t.Errorf("OfferedHostTools offers load_tool: %v", loads)
			}
		})
	}
}

func TestASchemaPastTheBoundsReachesTheModelOnlyInADataBlock(t *testing.T) {
	vector := readJSON(t, "contract/vectors/tool-offer/source-schema-bounds.json")
	marker := vector.Get("marker").Str()
	out, err := DeclareSources(declaredTools(t, vector.Get("declared").Items()), nil)
	if err != nil || len(out.Withheld) != 0 {
		t.Fatalf("%v %+v", err, out.Withheld)
	}
	shown, catalogue, withheld := OfferSources(out.HostTools, out.Entries, Defaults, marker)
	if !catalogue {
		t.Fatal("the source is not a catalogue")
	}
	offeredAre(t, shown, vector.Get("items").Items())
	withheldAre(t, withheld, vector.Get("withheld").Items())
	// None of the source's words outside its description's first line reaches the offer.
	offered := shown[0].Description + string(shown[0].InputSchema)
	for _, word := range []string{"IGNORE_", "write_file", "SYSTEM:", "END_RETRIEVED_DATA_", "deepest"} {
		if strings.Contains(offered, word) {
			t.Errorf("the offer holds %q", word)
		}
	}
	rows := []contract.ProvenanceSourceFigures{}
	policy := Defaults
	gate := NewSourceGate(out, &policy, marker, "json-schema", func() *[]contract.ProvenanceSourceFigures { return &rows }, nil)
	load := gate.LoadToolHost()
	answer, err := load.Execute(context.Background(), json.RawMessage(`{"name":"shop__lookup"}`))
	if err != nil {
		t.Fatal(err)
	}
	if section := gate.LoadedSection(); section != vector.Get("loadedSection").Str() {
		t.Errorf("the loaded section is\n%s\nnot\n%s", section, vector.Get("loadedSection").Str())
	}
	// The answer holds the schema the gate checks arguments against, the marker kept, and is framed as data when shown.
	if !strings.Contains(answer.StructuredContent.Get("inputSchema").Text(), "END_RETRIEVED_DATA_"+marker) || !AnswerIsData(&load, LoadTool, answer) {
		t.Errorf("load_tool answered %.200s", answer.StructuredContent.Text())
	}
	// A call that lacks a required entry whose text is an instruction is refused naming the entry's
	// place, never its text: this answer reaches the model outside load_tool's data block.
	refusal := vector.Get("refusal")
	_, err = gate.Call(context.Background(), "shop__lookup", json.RawMessage(refusal.Get("arguments").JSON()), shown[0].Execute)
	var failed *failure.Error
	if !errors.As(err, &failed) || failed.Failure.Code != "invalid-tool-input" || failed.Failure.Detail == nil || *failed.Failure.Detail != refusal.Get("detail").Str() {
		t.Errorf("the call was answered %v", err)
	}
	if err != nil && strings.Contains(err.Error(), "IGNORE_THE_DATA_RULE") {
		t.Errorf("the refusal echoes the source's text: %v", err)
	}
}
