package runcontext

// Skills (ADR-0009, skills.go) held to the contract's hand-written vectors (contract/vectors/skills),
// as skills.test.ts holds the TypeScript: the declaration, the digest, the catalogue, the load, the
// unload, the budget, the block after the history, the boundary's system text across a load, and the
// restore of a continued run.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
)

// skillOf is a skill as a vector writes it.
func skillOf(v *Value) Skill {
	source := v.Get("source")
	out := Skill{ID: v.Get("id").Str(), Name: v.Get("name").Str(), Description: v.Get("description").Str(), Instructions: v.Get("instructions").Str(),
		Source: SkillSource{Kind: source.Get("kind").Str(), ID: source.Get("id").Str(), Trust: source.Get("trust").Str()}}
	if held := source.Get("version"); held != nil {
		out.Source.Version = held.Str()
	}
	if held := source.Get("toolListDigest"); held != nil {
		out.Source.ToolListDigest = held.Str()
	}
	if held := v.Get("pinnedDigest"); held != nil {
		out.PinnedDigest = held.Str()
	}
	if held := v.Get("tools"); held != nil {
		for _, tool := range held.Items() {
			out.Tools = append(out.Tools, tool.Str())
		}
	}
	if held := v.Get("resources"); held != nil {
		for _, resource := range held.Items() {
			out.Resources = append(out.Resources, SkillResource{Name: resource.Get("name").Str(), Text: resource.Get("text").Str()})
		}
	}
	return out
}

func vectorSkills(t *testing.T) []Skill {
	t.Helper()
	var out []Skill
	for _, item := range vectorItems(t, "skills/declarations.json") {
		out = append(out, skillOf(item))
	}
	return out
}

var skillHostTools = []string{"ask_user", "propose_pack", "rates__lookup_rate", "tracker__create_issue"}

func noticeEvents(notices []SkillNotice) *Value {
	out := Array()
	for _, notice := range notices {
		raw, _ := json.Marshal(notice.Event())
		held, _ := ParseJSON(raw)
		out.Append(held)
	}
	return out
}

// sameSkillJSON reports two values with the same canonical JSON: the generated types write members in
// the schema's order, the vectors in the order they were written.
func sameSkillJSON(a, b *Value) bool { return CanonicalJSON(a) == CanonicalJSON(b) }

func asValue(t *testing.T, v any) *Value {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	held, err := ParseJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	return held
}

func TestEachSkillsDigestIsTheVectorsAndTheThreeAreAdmitted(t *testing.T) {
	skills := vectorSkills(t)
	for _, skill := range skills {
		if got := SkillDigest(skill); got != skill.PinnedDigest {
			t.Fatalf("%s digests to %s, not %s", skill.Name, got, skill.PinnedDigest)
		}
	}
	out, err := DeclareSkills(skills, skillHostTools, Defaults, nil)
	if err != nil || len(out.Withheld) != 0 || len(out.Skills) != 3 {
		t.Fatalf("%v %+v", err, out)
	}
	for at, held := range vectorItems(t, "skills/admitted.json") {
		if out.Skills[at].Digest != held.Get("digest").Str() || out.Skills[at].Bytes != SkillBytes(skills[at]) {
			t.Fatalf("skill %d admitted as %+v", at, out.Skills[at])
		}
	}
	if got := []int{out.Skills[0].Bytes, out.Skills[1].Bytes, out.Skills[2].Bytes}; got[0] != 240 || got[1] != 114 || got[2] != 70 {
		t.Fatalf("bytes %v", got)
	}
}

func TestASkillDeclarationThePackageCannotReadIsRefusedBeforeAnyRequest(t *testing.T) {
	// The Go type carries no member it does not name and no digest of its own: the refusals the type
	// itself cannot hold are the vector's.
	style := vectorSkills(t)[1]
	runtime := style
	runtime.Source.Kind = "runtime"
	_, err := DeclareSkills([]Skill{runtime}, nil, Defaults, nil)
	refused(t, err, "its source is the Runtime, which declares no skill")
	named := style
	named.Name = "issue style"
	_, err = DeclareSkills([]Skill{named}, nil, Defaults, nil)
	refused(t, err, "its name is not 1 to 64 letters, digits, underscores and hyphens")
	many := style
	for n := 0; n < 17; n++ {
		many.Tools = append(many.Tools, "tool_"+itoa(n))
	}
	_, err = DeclareSkills([]Skill{many}, nil, Defaults, nil)
	refused(t, err, "its tools are not a list of at most 16 names")
	unpinned := style
	unpinned.PinnedDigest = ""
	_, err = DeclareSkills([]Skill{unpinned}, nil, Defaults, nil)
	refused(t, err, "its pinned digest is not sha256: and 64 lower-case hex digits")
	_, err = DeclareSkills([]Skill{style, style}, nil, Defaults, nil)
	refused(t, err, "Two skills are declared")
	review := vectorSkills(t)[2]
	_, err = DeclareSkills([]Skill{review}, nil, Defaults, []ToolSource{{Kind: "mcp", ID: "tracker", Trust: "public", ToolListDigest: "sha256:" + strings.Repeat("1", 64)}})
	refused(t, err, "declared twice")
}

func TestASkillWithAChangedBundleOrAnUndeclaredToolIsWithheldWithTheVectorsGuardrail(t *testing.T) {
	for _, name := range []string{"skills/withheld-digest.json", "skills/withheld-tool.json"} {
		vector := readJSON(t, "contract/vectors/"+name)
		tools := skillHostTools
		if held := vector.Get("hostTools"); held != nil {
			tools = nil
			for _, tool := range held.Items() {
				tools = append(tools, tool.Str())
			}
		}
		out, err := DeclareSkills([]Skill{skillOf(vector.Get("declaration"))}, tools, Defaults, nil)
		if err != nil || len(out.Skills) != 0 {
			t.Fatalf("%s: %v %+v", name, err, out)
		}
		if got := noticeEvents(out.Withheld); !equalJSON(got, vector.Get("items")) {
			t.Fatalf("%s: %s", name, got.Text())
		}
	}
	tight := Defaults
	tight.SkillBytes = 100
	out, err := DeclareSkills(vectorSkills(t), skillHostTools, tight, nil)
	if err != nil || len(out.Skills) != 1 || out.Withheld[0].Detail != "the skill expense_policy is 240 bytes, over skillBytes (100); it is not offered" {
		t.Fatalf("%v %+v", err, out)
	}
}

func TestTheCatalogueIsTheVectorsSectionAndIsBoundedByCountAndBytes(t *testing.T) {
	vector := readJSON(t, "contract/vectors/skills/catalogue.json")
	out, err := DeclareSkills(vectorSkills(t), skillHostTools, Defaults, nil)
	if err != nil {
		t.Fatal(err)
	}
	section, catalogued, withheld := CatalogueSkills(out.Skills, Defaults, vector.Get("marker").Str())
	if section != vector.Get("section").Str() || len(catalogued) != 3 || len(withheld) != 0 {
		t.Fatalf("the section was %q", section)
	}
	var forty []Skill
	for n := 0; n < 40; n++ {
		kind, id := "custom", "policies"
		if n >= 30 {
			kind, id = "exchange", "jx-more"
		}
		skill := Skill{ID: "s-" + itoa(n), Name: "skill_" + itoa(n), Description: "Skill " + itoa(n) + ".", Instructions: "Do step " + itoa(n) + ".",
			Source: SkillSource{Kind: kind, ID: id, Trust: "person", Version: "1"}}
		skill.PinnedDigest = SkillDigest(skill)
		forty = append(forty, skill)
	}
	admitted, err := DeclareSkills(forty, nil, Defaults, nil)
	if err != nil || len(admitted.Skills) != 40 {
		t.Fatal(err)
	}
	_, catalogued, withheld = CatalogueSkills(admitted.Skills, Defaults, vector.Get("marker").Str())
	want := "the skills catalogue holds at most 32 skills and 8000 bytes (cataloguedSkills, skillsCatalogueBytes), so 8 of jx-more's skills are not offered; 32 are offered across the catalogue"
	if len(catalogued) != 32 || len(withheld) != 1 || withheld[0].Tool != "jx-more" || withheld[0].Detail != want {
		t.Fatalf("%d catalogued, %+v", len(catalogued), withheld)
	}
	tight := Defaults
	tight.SkillsCatalogueBytes = 1000
	_, catalogued, withheld = CatalogueSkills(admitted.Skills, tight, vector.Get("marker").Str())
	if len(catalogued) >= 32 || len(withheld) != 2 || withheld[0].Tool != "policies" || withheld[1].Tool != "jx-more" {
		t.Fatalf("%d catalogued, %+v", len(catalogued), withheld)
	}
}

// skillRun is a run's state with the skill sources' rows, and its source gate and skill gate.
func skillRun(t *testing.T, agentWire bool, limit int) (*SkillGate, *SourceGate, *Execution) {
	t.Helper()
	out, err := DeclareSkills(vectorSkills(t), skillHostTools, Defaults, nil)
	if err != nil {
		t.Fatal(err)
	}
	state := &Execution{}
	var sources []ToolSource
	for _, source := range out.Sources {
		sources = append(sources, ToolSource{Kind: source.Kind, ID: source.ID, Trust: source.Trust, Version: source.Version, ToolListDigest: source.ToolListDigest})
	}
	policy := Defaults
	policy.SkillsPerRun = limit
	gate := NewSourceGate(&Declared{Entries: map[string]*SourceEntry{}, Sources: sources}, &policy, "", "", func() *[]contract.ProvenanceSourceFigures { return &state.Sources }, nil)
	marker := readJSON(t, "contract/vectors/skills/load.json").Get("marker").Str()
	return NewSkillGate(out.Skills, policy, gate, state, marker, agentWire, out.SourceIDs), gate, state
}

func loadOrUnload(t *testing.T, tool HostTool, name string) ToolResult {
	t.Helper()
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"name":`+QuoteJSON(name)+`}`))
	if err != nil {
		text, raw := failure.ToolFailure(err)
		structured, _ := ParseJSON(raw)
		return ResultOf([]Content{TextContent(text)}, structured).WithIsError(true)
	}
	return result
}

func TestLoadSkillAndUnloadSkillAreTheVectors(t *testing.T) {
	skills, sources, state := skillRun(t, false, 4)
	for name, held := range map[string]HostTool{"skills/load-skill.json": skills.LoadSkillHost(), "skills/unload-skill.json": skills.UnloadSkillHost()} {
		if got := asValue(t, held.Declaration()); CanonicalJSON(got) != CanonicalJSON(vectorItems(t, name)[0]) {
			t.Fatalf("%s: %s", name, got.Text())
		}
	}
	load := readJSON(t, "contract/vectors/skills/load.json")
	answer := loadOrUnload(t, skills.LoadSkillHost(), "expense_policy")
	items := load.Get("items").Items()
	if answer.Err() || !equalJSON(answer.StructuredContent, items[2].Get("structured")) || answer.Content[0].Text() != items[2].Get("text").Str() {
		t.Fatalf("load_skill answered %s", answer.Value().Text())
	}
	notices := sources.TakeNotices()
	if len(notices) != 1 || !sameSkillJSON(asValue(t, notices[0]), items[1]) {
		t.Fatalf("the load's events were %s", asValue(t, notices).Text())
	}
	if skills.Section() != load.Get("block").Str() || !strings.HasPrefix(skills.Section(), SkillInstructionsHeader+"\n") {
		t.Fatalf("the block was %q", skills.Section())
	}
	if len(state.Skills) != 1 || state.Skills[0].ID != "expense-policy" || state.Skills[0].Bytes != 240 || !parsableDate(state.Skills[0].LoadedAt) {
		t.Fatalf("the loaded set was %+v", state.Skills)
	}
	unload := vectorItems(t, "skills/unload.json")
	answer = loadOrUnload(t, skills.UnloadSkillHost(), "expense_policy")
	if answer.Err() || !equalJSON(answer.StructuredContent, unload[2].Get("structured")) || answer.Content[0].Text() != unload[2].Get("text").Str() {
		t.Fatalf("unload_skill answered %s", answer.Value().Text())
	}
	if notices := sources.TakeNotices(); len(notices) != 1 || !sameSkillJSON(asValue(t, notices[0]), unload[1]) {
		t.Fatalf("the unload's events were %s", asValue(t, notices).Text())
	}
	if skills.Section() != "" || len(state.Skills) != 0 {
		t.Fatal("the block or the loaded set outlived the unload")
	}
	if answer := loadOrUnload(t, skills.UnloadSkillHost(), "expense_policy"); !answer.Err() {
		t.Fatal("a skill not loaded was unloaded")
	}
	if answer := loadOrUnload(t, skills.LoadSkillHost(), "file_helper"); !answer.Err() || !strings.Contains(answer.Content[0].Text(), "is not one; nothing was loaded") {
		t.Fatalf("a name not catalogued answered %s", answer.Value().Text())
	}
}

func TestEachLoadIsCountedAgainstSkillsPerRunAndTheLoadPastItRefused(t *testing.T) {
	skills, sources, state := skillRun(t, false, 4)
	load, unload := skills.LoadSkillHost(), skills.UnloadSkillHost()
	loadOrUnload(t, load, "expense_policy")
	loadOrUnload(t, load, "expense_policy")
	if sources.SkillLoads() != 1 {
		t.Fatalf("an already loaded skill was counted: %d", sources.SkillLoads())
	}
	loadOrUnload(t, unload, "expense_policy")
	loadOrUnload(t, load, "expense_policy")
	loadOrUnload(t, load, "review_checklist")
	loadOrUnload(t, unload, "review_checklist")
	loadOrUnload(t, load, "review_checklist")
	sources.TakeNotices()
	if sources.SkillLoads() != 4 {
		t.Fatalf("%d loads counted", sources.SkillLoads())
	}
	budget := vectorItems(t, "skills/budget.json")
	answer := loadOrUnload(t, load, "issue_style")
	if !answer.Err() || answer.Content[0].Text() != budget[2].Get("text").Str() || !equalJSON(answer.StructuredContent, budget[2].Get("structured")) {
		t.Fatalf("the load past the budget answered %s", answer.Value().Text())
	}
	if notices := sources.TakeNotices(); len(notices) != 1 || !sameSkillJSON(asValue(t, notices[0]), budget[1]) {
		t.Fatalf("the refusal's events were %s", asValue(t, notices).Text())
	}
	if sources.SkillLoads() != 4 || len(state.Skills) != 2 {
		t.Fatalf("the refused load counted or loaded: %d, %+v", sources.SkillLoads(), state.Skills)
	}
}

func TestTheAgentWireCarriesTheBlockInLoadSkillsAnswer(t *testing.T) {
	skills, _, _ := skillRun(t, true, 4)
	answer := loadOrUnload(t, skills.LoadSkillHost(), "expense_policy")
	block := readJSON(t, "contract/vectors/skills/load.json").Get("block").Str()
	if text := answer.Content[0].Text(); !strings.HasSuffix(text, "\n"+block) || !strings.Contains(text, LoadedAgentNote) {
		t.Fatalf("the agent wire's answer was %q", text)
	}
}

// skillsSession is a session with the vectors' skills and two of the host tools they name.
func skillsSession(skills []Skill, tools ...string) Session {
	held := modelSession()
	if len(tools) == 0 {
		tools = []string{"ask_user", "rates__lookup_rate"}
	}
	for _, name := range tools {
		held.HostTools = append(held.HostTools, HostTool{Name: name, Description: name, Effect: "read", InputSchema: json.RawMessage(`{"type":"object"}`),
			Execute: func(context.Context, json.RawMessage) (ToolResult, error) {
				return ResultOf([]Content{TextContent("ok")}, nil), nil
			}})
	}
	held.Skills = skills
	held.Context = &Input{CurrentRequest: &CurrentRequest{Message: "Current request", Input: "Current request"}}
	return held
}

func hostToolNamed(run *Run, name string) *HostTool {
	for at := range run.Session.HostTools {
		if run.Session.HostTools[at].Name == name {
			return &run.Session.HostTools[at]
		}
	}
	return nil
}

func TestTheBoundaryKeepsTheSystemTextAcrossALoadWhileTheStateCarriesTheBlock(t *testing.T) {
	run, err := ContextualSession(context.Background(), skillsSession(vectorSkills(t)))
	if err != nil {
		t.Fatal(err)
	}
	defer run.Context.Close()
	before := run.Context.System("GUIDANCE", "\n\nTOOLS")
	if !strings.HasPrefix(before, "GUIDANCE\n\nTOOLS\n\nSKILLS (") || !strings.HasSuffix(before, "\nEND SKILLS") {
		t.Fatalf("the system text was %q", before)
	}
	load, unload := hostToolNamed(run, LoadSkill), hostToolNamed(run, UnloadSkill)
	if load == nil || unload == nil {
		t.Fatal("the loaders were not offered")
	}
	if strings.Contains(run.Context.Execution.RequestState(), SkillInstructionsHeader) {
		t.Fatal("the state carried a block before any load")
	}
	if _, err := load.Execute(context.Background(), json.RawMessage(`{"name":"issue_style"}`)); err != nil {
		t.Fatal(err)
	}
	if run.Context.System("GUIDANCE", "\n\nTOOLS") != before {
		t.Fatal("the system text changed with the load")
	}
	state := run.Context.Execution.RequestState()
	if !strings.HasSuffix(state, run.Context.Skills.Section()) || !strings.Contains(state, SkillInstructionsHeader+"\n"+SkillOpening+" "+run.Context.Marker+` "issue_style"`) {
		t.Fatalf("the state was %q", state)
	}
	if _, err := unload.Execute(context.Background(), json.RawMessage(`{"name":"issue_style"}`)); err != nil {
		t.Fatal(err)
	}
	if run.Context.System("GUIDANCE", "\n\nTOOLS") != before || strings.Contains(run.Context.Execution.RequestState(), SkillInstructionsHeader) {
		t.Fatal("the unload left the block or moved the system text")
	}
	snapshot, err := run.Context.Execution.Snapshot("")
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range snapshot.Sources {
		if row.Source == "jx-issue-style" && (row.Kind != "exchange" || row.Skills == nil || *row.Skills != 1) {
			t.Fatalf("the row was %+v", row)
		}
	}
	for _, name := range []string{LoadSkill, UnloadSkill} {
		reserved := skillsSession(nil, name)
		if _, err := ContextualSession(context.Background(), reserved); err == nil {
			t.Fatalf("a host tool named %s was offered", name)
		}
	}
}

func TestAContinuedRunRestoresTheLoadedSetAndSaysWhatItDidNotRestore(t *testing.T) {
	saved := readJSON(t, "contract/vectors/skills/checkpoint.json").Get("items").Items()[0]
	saved.Set("requestKey", String(ContextDigest(ObjectOf("request", ObjectOf("message", String("Current request"), "input", String("Current request")), "phase", String("work"), "purpose", Null()).Text())))
	var checkpoint contract.Checkpoint
	if err := json.Unmarshal(saved.JSON(), &checkpoint); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadExecution(saved); err != nil {
		t.Fatal(err)
	}
	skills := vectorSkills(t)
	session := skillsSession([]Skill{skills[0], skills[2]}, "rates__lookup_rate", "tracker__create_issue")
	session.Context.Execution = &checkpoint
	run, err := ContextualSession(context.Background(), session)
	if err != nil {
		t.Fatal(err)
	}
	defer run.Context.Close()
	notices := run.Context.Sources.TakeNotices()
	want := "the skill issue-style loaded earlier in this response is no longer declared as it was loaded (" + skills[1].PinnedDigest + "); it was not restored, and its instructions are not in this run"
	if len(notices) != 1 || notices[0].Guardrail == nil || notices[0].Guardrail.Tool != "issue-style" || notices[0].Guardrail.Detail != want || *notices[0].Guardrail.Reason != "skill withheld" {
		t.Fatalf("the notices were %s", asValue(t, notices).Text())
	}
	if current := run.Context.Skills.Current(); len(current) != 1 || current[0].ID != "expense-policy" {
		t.Fatalf("restored %+v", current)
	}
	if loads := run.Context.Skills.Loads(); len(loads) != 1 || loads[0].ID != "expense-policy" {
		t.Fatalf("the loaded set is %+v", loads)
	}
	if !strings.Contains(run.Context.Execution.RequestState(), `"expense_policy" from source "policies"`) {
		t.Fatal("the restored skill's block is not in the state")
	}
	if run.Context.Sources.SkillLoads() != 3 {
		t.Fatalf("the loads before the resume are %d", run.Context.Sources.SkillLoads())
	}
}

func TestTheCheckpointsLoadedSetIsReadClosed(t *testing.T) {
	saved := readJSON(t, "contract/vectors/skills/checkpoint.json").Get("items").Items()[0]
	if !ValidSkillLoads(saved.Get("skills")) {
		t.Fatal("the vector's loaded set was refused")
	}
	first := saved.Get("skills").Items()[0]
	extra, _ := ParseJSON(first.JSON())
	extra.Set("instructions", String("x"))
	if ValidSkillLoads(Array(extra)) || ValidSkillLoads(Array(first, first)) {
		t.Fatal("a loaded set with another member or a skill twice was read")
	}
}
