package runcontext

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/wire"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
	"os"
	"reflect"
	"strings"
	"testing"
)

func taxonomyVector(t *testing.T, name string, out any) {
	t.Helper()
	raw, err := os.ReadFile("../../contract/testdata/taxonomy/" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, out); err != nil {
		t.Fatal(err)
	}
}
func TestTaxonomySharedTools(t *testing.T) {
	var v struct {
		Valid []struct {
			Taxonomy     *ToolTaxonomy
			Runtime      bool
			Approval     string
			Review, Work bool
		}
		Limits []struct {
			Taxonomy *ToolTaxonomy
			Limit    *TaxonomyLimit
			Allowed  bool
		}
		Invalid []json.RawMessage
		Hints   []struct {
			Effect      string
			Annotations map[string]bool
		}
	}
	taxonomyVector(t, "tools", &v)
	for _, r := range v.Valid {
		if p := wire.TaxonomyProblem(r.Taxonomy, r.Runtime); p != "" {
			t.Fatal(p)
		}
		if DefaultApproval(r.Taxonomy.Effect) != r.Approval {
			t.Fatal("approval default")
		}
		review, work := PresetLimit("review"), PresetLimit("work")
		if TaxonomyFits(r.Taxonomy, &review) != r.Review || TaxonomyFits(r.Taxonomy, &work) != r.Work {
			t.Fatal("preset family")
		}
	}
	for _, raw := range v.Invalid {
		var taxonomy *ToolTaxonomy
		if err := json.Unmarshal(raw, &taxonomy); err == nil && wire.TaxonomyProblem(taxonomy, false) == "" {
			t.Fatalf("accepted malformed taxonomy %s", raw)
		}
	}
	for _, r := range v.Limits {
		if TaxonomyFits(r.Taxonomy, r.Limit) != r.Allowed {
			t.Fatal("invalid or widened taxonomy limit")
		}
	}
	for _, r := range v.Hints {
		if !reflect.DeepEqual(TaxonomyAnnotations(r.Effect), r.Annotations) {
			t.Fatal("effect annotations")
		}
	}
}
func taxonomyProfileVectors(t *testing.T) (map[string]*ToolTaxonomy, []string, []string) {
	var v struct {
		Tools    map[string]*ToolTaxonomy
		Profiles []struct {
			Tools        []string
			Profile      CapabilityProfile
			Review, Work bool
		}
		Unknown, Kinds, Tags []string
	}
	taxonomyVector(t, "profiles", &v)
	for _, r := range v.Profiles {
		p, err := DeriveCapabilityProfile(r.Tools, v.Tools)
		if err != nil || !reflect.DeepEqual(p, r.Profile) {
			t.Fatalf("profile: %#v %v", p, err)
		}
		if ProfileFits(p, PresetLimit("review")) != r.Review || ProfileFits(p, PresetLimit("work")) != r.Work {
			t.Fatal("profile preset")
		}
	}
	if _, err := DeriveCapabilityProfile(v.Unknown, v.Tools); err == nil {
		t.Fatal("unadmitted dependency accepted")
	}
	return v.Tools, v.Kinds, v.Tags
}
func TestTaxonomySharedProfiles(t *testing.T) {
	taxonomyProfileVectors(t)
	if wire.RuntimeTaxonomyProblem(&ToolTaxonomy{Operation: "act", Effect: "act"}, "get_schema") == "" {
		t.Fatal("runtime action bypass")
	}
	if p := wire.RuntimeTaxonomyProblem(&ToolTaxonomy{Operation: "evaluate", Effect: "act"}, "experimental_evaluate"); p != "" {
		t.Fatal("permitted acting evaluation refused", p)
	}
	if !TaxonomyFits(&ToolTaxonomy{Operation: "ask", Effect: "pure"}, &TaxonomyLimit{Operations: []string{}, Effects: []string{}}) {
		t.Fatal("ask must remain offered")
	}
}
func taxonomySkill(tools []string) Skill {
	return Skill{ID: "notes", Name: "notes", Description: "Review notes", Instructions: "Read carefully.", Source: SkillSource{Kind: "custom", ID: "library", Trust: "person", Version: "1"}, Tools: tools, PinnedDigest: "sha256:" + strings.Repeat("0", 64)}
}
func TestTaxonomySkillPinsAndPresets(t *testing.T) {
	tools, kinds, tags := taxonomyProfileVectors(t)
	for _, kind := range kinds {
		s := taxonomySkill(nil)
		s.Kind = kind
		s.Tags = tags
		s.PinnedDigest = SkillDigest(s, tools)
		out, err := DeclareSkills([]Skill{s}, nil, Defaults, nil, tools)
		if err != nil || len(out.Skills) != 1 || out.Skills[0].Profile.Effect != "pure" {
			t.Fatalf("kind/tags admission %v", err)
		}
	}
	tagged := taxonomySkill(nil)
	tagged.Kind = "method"
	tagged.Tags = []string{"finance"}
	tagged.PinnedDigest = SkillDigest(tagged)
	for _, change := range []Skill{func() Skill { x := tagged; x.Kind = "review"; return x }(), func() Skill { x := tagged; x.Tags = []string{"legal"}; return x }()} {
		out, err := DeclareSkills([]Skill{change}, nil, Defaults, nil)
		if err != nil || len(out.Withheld) != 1 {
			t.Fatal("kind/tag changes must reapprove")
		}
	}
	s := taxonomySkill([]string{"read_notes"})
	s.Kind = "procedure"
	s.PinnedDigest = SkillDigest(s, tools)
	out, err := DeclareSkills([]Skill{s}, []string{"read_notes"}, Defaults, nil, tools)
	if err != nil || len(out.Skills) != 1 {
		t.Fatalf("read skill admission %v", err)
	}
	section, _, _ := CatalogueSkills(out.Skills, Defaults, "marker", "review")
	if !strings.Contains(section, "reads external data") {
		t.Fatal("derived catalogue label")
	}
	changed := map[string]*ToolTaxonomy{}
	for k, v := range tools {
		changed[k] = v
	}
	changed["read_notes"] = &ToolTaxonomy{Operation: "act", Effect: "act"}
	refused, err := DeclareSkills([]Skill{s}, []string{"read_notes"}, Defaults, nil, changed)
	if err != nil || len(refused.Withheld) != 1 {
		t.Fatal("dependency change must reapprove")
	}
	acting := taxonomySkill([]string{"send_notes"})
	acting.Kind = "procedure"
	acting.PinnedDigest = SkillDigest(acting, tools)
	acts, err := DeclareSkills([]Skill{acting}, []string{"send_notes"}, Defaults, nil, tools)
	if err != nil {
		t.Fatal(err)
	}
	_, catalogued, _ := CatalogueSkills(acts.Skills, Defaults, "marker", "review")
	if len(catalogued) != 0 {
		t.Fatal("review admitted acting skill")
	}
	section, _, _ = CatalogueSkills(acts.Skills, Defaults, "marker", "work")
	if !strings.Contains(section, "acts; needs approval") {
		t.Fatal("work profile label")
	}
	for _, bad := range []Skill{{Kind: "model-choice"}, {Tags: make([]string, 9)}, {Tags: []string{strings.Repeat("x", 33)}}} {
		s := taxonomySkill(nil)
		s.Kind = bad.Kind
		s.Tags = bad.Tags
		if _, err := DeclareSkills([]Skill{s}, nil, Defaults, nil); err == nil {
			t.Fatal("invalid skill metadata")
		}
	}
}
func taxonomyLocal() HostTool {
	return HostTool{Name: "read_notes", Taxonomy: &ToolTaxonomy{Operation: "read", Effect: "read"}, Description: "Read notes", InputSchema: json.RawMessage(`{"type":"object"}`), Execute: func(context.Context, json.RawMessage) (ToolResult, error) { return ResultOf([]Content{}, nil), nil }}
}
func TestTaxonomyAdmissionBoundaries(t *testing.T) {
	tool := taxonomyLocal()
	tool.Taxonomy = nil
	if _, err := DeclareSources([]HostTool{tool}, nil); err == nil {
		t.Fatal("missing taxonomy admitted")
	}
	tool = taxonomyLocal()
	tool.EffectClass = "act"
	if _, err := DeclareSources([]HostTool{tool}, nil); err == nil {
		t.Fatal("contradictory projection admitted")
	}
	tool = taxonomyLocal()
	tool.Taxonomy = &ToolTaxonomy{Operation: "evaluate", Effect: "pure"}
	if _, err := DeclareSources([]HostTool{tool}, nil); err == nil {
		t.Fatal("owner spoofing admitted")
	}
	tool = taxonomyLocal()
	tool.Taxonomy = &ToolTaxonomy{Operation: "act", Effect: "act"}
	tool.Approval = "none"
	tool.Source = &ToolSource{Kind: "custom", ID: "notes", Trust: "first-party", Version: "1"}
	tool.Source.ToolListDigest = ToolListDigest([]ListedTool{{Name: tool.Name, Description: tool.Description, InputSchema: tool.InputSchema, Taxonomy: tool.Taxonomy}})
	if _, err := DeclareSources([]HostTool{tool}, nil); err == nil {
		t.Fatal("approval widening admitted")
	}
	tool = taxonomyLocal()
	tool.TaxonomyLimit = &TaxonomyLimit{Operations: []string{"check"}}
	out, err := DeclareSources([]HostTool{tool}, nil)
	if err != nil || len(out.HostTools) != 0 || len(out.Withheld) != 1 {
		t.Fatal("narrowing limit")
	}
}
func TestTaxonomyExternalReceiptBinding(t *testing.T) {
	tool := taxonomyLocal()
	tool.Taxonomy = &ToolTaxonomy{Operation: "read", Effect: "read-external"}
	tool.Source = &ToolSource{Kind: "gateway", ID: "notes", Trust: "connected", Version: "1", VerifyReceipt: func(_ context.Context, e ReceiptEvidence) (bool, error) {
		return e.Receipt == "signed-call-1" && e.Tool == "read_notes" && e.Args == `{"id":"one"}` && e.Result == `{"content":[{"text":"one","type":"text"}]}`, nil
	}}
	tool.Execute = func(_ context.Context, args json.RawMessage) (ToolResult, error) {
		v, _ := ParseJSON(args)
		out := ResultOf([]Content{TextContent(v.Get("id").Str())}, nil)
		out.Receipt = "signed-call-1"
		return out, nil
	}
	tool.Source.ToolListDigest = ToolListDigest([]ListedTool{{Name: tool.Name, Description: tool.Description, InputSchema: tool.InputSchema, Taxonomy: tool.Taxonomy}})
	declared, err := DeclareSources([]HostTool{tool}, nil)
	if err != nil || len(declared.HostTools) != 1 {
		t.Fatalf("external admission %v", err)
	}
	rows := []contract.ProvenanceSourceFigures{}
	policy := Defaults
	gate := NewSourceGate(declared, &policy, "marker", "json-schema", func() *[]contract.ProvenanceSourceFigures { return &rows }, nil)
	offered := declared.HostTools[0]
	if _, err = gate.Call(context.Background(), offered.Name, json.RawMessage(`{"id":"one"}`), offered.Execute); err != nil {
		t.Fatal(err)
	}
	if _, err = gate.Call(context.Background(), offered.Name, json.RawMessage(`{"id":"two"}`), offered.Execute); err == nil || !strings.Contains(err.Error(), "receipt verification failed") {
		t.Fatal("mismatched receipt was not refused", err)
	}
	var rejected *failure.Error
	if !errors.As(err, &rejected) || rejected.Failure.Code != "tool-failed" || rejected.Failure.Outcome != "unknown" {
		t.Fatal("post-call receipt failure must remain unknown", err)
	}
	badResult := func(context.Context, json.RawMessage) (ToolResult, error) {
		out := ResultOf([]Content{TextContent("tampered")}, nil)
		out.Receipt = "signed-call-1"
		return out, nil
	}
	if _, err = gate.Call(context.Background(), offered.Name, json.RawMessage(`{"id":"one"}`), badResult); err == nil {
		t.Fatal("receipt from another result was accepted")
	}
	for _, row := range rows {
		if row.Source == "notes" && (row.Calls != 3 || row.Refused != 2) {
			t.Fatal("executed receipt refusals must consume source-call accounting")
		}
	}
	tool.Source.VerifyReceipt = nil
	if _, err = DeclareSources([]HostTool{tool}, nil); err == nil {
		t.Fatal("missing receipt verifier admitted")
	}
}

func TestTaxonomyRuntimeSkillReferences(t *testing.T) {
	runtime := []RuntimeTool{{Name: "validate", Meta: &ToolTaxonomy{Operation: "check", Effect: "pure", CheckKind: "validate"}}, {Name: "get_pack", Meta: &ToolTaxonomy{Operation: "read", Effect: "read"}}}
	tools := ToolTaxonomies(nil, runtime)
	if tools["validate"] == nil || tools["get_pack"] != nil {
		t.Fatal("runtime references must be validated and offered")
	}
	s := taxonomySkill([]string{"validate"})
	s.Kind = "method"
	s.PinnedDigest = SkillDigest(s, tools)
	admitted, err := DeclareSkills([]Skill{s}, []string{"validate"}, Defaults, nil, tools)
	if err != nil || len(admitted.Skills) != 1 || admitted.Skills[0].Profile.Effect != "pure" {
		t.Fatal("runtime skill profile", err)
	}
	withheld, err := DeclareSkills([]Skill{s}, nil, Defaults, nil, tools)
	if err != nil || len(withheld.Withheld) != 1 {
		t.Fatal("withdrawn runtime tool did not withhold skill")
	}
}

// The actual shared host vector is held at the exported declaration boundary.
// Removing either permitted dimension withholds the tool without granting dispatch.
func TestTaxonomyLimitSharedHostAdmission(t *testing.T) {
	raw, err := os.ReadFile("../../contract/vectors/tool-offer/taxonomy-limits.json")
	if err != nil {
		t.Fatal(err)
	}
	var file struct{ Items []HostTool }
	if err := json.Unmarshal(raw, &file); err != nil || len(file.Items) != 1 {
		t.Fatal("invalid shared taxonomy ceiling vector", err)
	}
	tool := file.Items[0]
	if tool.Taxonomy == nil || tool.TaxonomyLimit == nil || tool.Taxonomy.Operation != "read" || tool.Taxonomy.Effect != "read" {
		t.Fatal("shared host vector lost explicit read taxonomy or ceilings")
	}
	admitted, err := DeclareSources([]HostTool{tool}, nil)
	if err != nil || len(admitted.HostTools) != 1 || len(admitted.Withheld) != 0 || admitted.HostTools[0].Name != tool.Name || !reflect.DeepEqual(admitted.HostTools[0].TaxonomyLimit, tool.TaxonomyLimit) {
		t.Fatal("shared host ceiling refused or lost a permitted read", err)
	}
	withoutRead := func(values []string) []string {
		out := []string{}
		for _, value := range values {
			if value != "read" {
				out = append(out, value)
			}
		}
		if len(out) != len(values)-1 {
			t.Fatal("shared ceiling lacks exactly one read member")
		}
		return out
	}
	for _, dimension := range []string{"operation", "effect"} {
		t.Run(dimension, func(t *testing.T) {
			limited := tool
			limit := *tool.TaxonomyLimit
			limited.TaxonomyLimit = &limit
			if dimension == "operation" {
				limit.Operations = withoutRead(limit.Operations)
			} else {
				limit.Effects = withoutRead(limit.Effects)
			}
			declared, err := DeclareSources([]HostTool{limited}, nil)
			if err != nil {
				t.Fatal("valid narrower host ceiling failed before withholding", err)
			}
			if len(declared.HostTools) != 0 || len(declared.Withheld) != 1 || declared.Withheld[0].Tool != "taxonomy" || declared.Withheld[0].Detail != "The host taxonomy limit withholds this tool." {
				t.Fatalf("%s ceiling failed to withhold the shared host tool: %+v", dimension, declared)
			}
		})
	}
	admitted, err = DeclareSources([]HostTool{tool}, nil)
	if err != nil || len(admitted.HostTools) != 1 || len(admitted.Withheld) != 0 {
		t.Fatal("narrowed copy changed original host ceiling", err)
	}
}

// TestTaxonomyRehearse holds the ADR-0027 amendment (#241): rehearse is a check of pure or read
// effect only, refused under any other operation or a stronger effect, and a host declaring it
// opens its description with the non-binding words.
func TestTaxonomyRehearse(t *testing.T) {
	raw, err := os.ReadFile("../../contract/vectors/taxonomy/declarations.json")
	if err != nil {
		t.Fatal(err)
	}
	var declarations struct {
		Items   []*ToolTaxonomy
		Refused []struct {
			Taxonomy *ToolTaxonomy
			Problem  string
		}
	}
	if err = json.Unmarshal(raw, &declarations); err != nil {
		t.Fatal(err)
	}
	refusal := func(tool HostTool, problem string) {
		t.Helper()
		_, err := DeclareSources([]HostTool{tool}, nil)
		var refused *failure.Error
		if !errors.As(err, &refused) || refused.Failure.Code != "configuration" || err.Error() != "Tool taxonomy refused: "+problem+". Nothing was sent." {
			t.Fatalf("want the configuration refusal %q, got %v", problem, err)
		}
		event, err := json.Marshal(failure.Event(err, "context"))
		var back contract.RunEvent
		if err != nil || json.Unmarshal(event, &back) != nil || back.Error == nil || back.Error.Failure == nil || back.Error.Failure.Code != "configuration" {
			t.Fatalf("refusal event does not read back: %s", event)
		}
	}
	rehearse := 0
	for _, item := range declarations.Items {
		if p := wire.TaxonomyProblem(item, true); p != "" {
			t.Fatalf("record %+v refused: %s", item, p)
		}
		if item.CheckKind == "rehearse" {
			rehearse++
		}
	}
	if rehearse != 2 || len(declarations.Refused) != 4 {
		t.Fatalf("rehearse records %d, refused %d", rehearse, len(declarations.Refused))
	}
	for _, row := range declarations.Refused {
		if p := wire.TaxonomyProblem(row.Taxonomy, false); p != row.Problem {
			t.Fatalf("%+v: want %q, got %q", row.Taxonomy, row.Problem, p)
		}
		tool := taxonomyLocal()
		tool.Description = "Rehearsal, non-binding: read notes"
		tool.Taxonomy = row.Taxonomy
		refusal(tool, row.Problem)
	}
	var v struct {
		Rehearsal struct {
			Admitted []struct {
				Declaration  HostTool
				Approval     string
				Annotations  map[string]bool
				Review, Work bool
			}
			Refused []struct {
				Declaration HostTool
				Problem     string
			}
		}
	}
	taxonomyVector(t, "tools", &v)
	execute := func(context.Context, json.RawMessage) (ToolResult, error) { return ResultOf([]Content{}, nil), nil }
	for _, row := range v.Rehearsal.Admitted {
		tool := row.Declaration
		tool.Execute = execute
		out, err := DeclareSources([]HostTool{tool}, nil)
		if err != nil || len(out.HostTools) != 1 {
			t.Fatalf("rehearse declaration refused: %v", err)
		}
		admitted := out.HostTools[0]
		if !reflect.DeepEqual(admitted.Annotations, row.Annotations) || DefaultApproval(admitted.Taxonomy.Effect) != row.Approval {
			t.Fatalf("rehearse hints %v, approval %s", admitted.Annotations, DefaultApproval(admitted.Taxonomy.Effect))
		}
		review, work := PresetLimit("review"), PresetLimit("work")
		if TaxonomyFits(admitted.Taxonomy, &review) != row.Review || TaxonomyFits(admitted.Taxonomy, &work) != row.Work {
			t.Fatal("rehearse preset family")
		}
	}
	if len(v.Rehearsal.Refused) != 4 {
		t.Fatalf("rehearsal refusals %d", len(v.Rehearsal.Refused))
	}
	for _, row := range v.Rehearsal.Refused {
		tool := row.Declaration
		tool.Execute = execute
		refusal(tool, row.Problem)
	}
}
