package runcontext

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func TestAskingWellAdmission(t *testing.T) {
	raw, err := os.ReadFile("../../contract/testdata/asking-well/scenario.json")
	if err != nil {
		t.Fatal(err)
	}
	var vector struct {
		Skill   Skill             `json:"skill"`
		Pin     string            `json:"pin"`
		Profile CapabilityProfile `json:"profile"`
	}
	if err = json.Unmarshal(raw, &vector); err != nil {
		t.Fatal(err)
	}
	ask := HostTool{Name: "ask_user", Interaction: "question", Taxonomy: &ToolTaxonomy{Operation: "ask", Effect: "pure"}}
	tools := []HostTool{ask}
	taxonomy := map[string]*ToolTaxonomy{"ask_user": ask.Taxonomy}
	skill := AskingWellSkill()
	if !reflect.DeepEqual(skill, vector.Skill) {
		t.Fatal("shared bundle differs from package bytes")
	}
	if skill.Kind != "method" || len(skill.Tools) != 1 || skill.Tools[0] != "ask_user" || SkillDigest(skill, taxonomy) != vector.Pin || AskingWellPin != vector.Pin {
		t.Fatal("actual bundle pin mismatch")
	}
	selected, err := SelectAskingWell(nil, tools)
	if err != nil {
		t.Fatal(err)
	}
	declared, err := DeclareSkills(selected, []string{"ask_user"}, Defaults, nil, taxonomy)
	if err != nil {
		t.Fatal(err)
	}
	if len(declared.Skills) != 1 || declared.Skills[0].Profile.Effect != vector.Profile.Effect || len(declared.Skills[0].Profile.Operations) != 1 || declared.Skills[0].Profile.Operations[0] != "ask" {
		t.Fatal("profile must derive ask/pure")
	}
	if err = RequireAskingWell(declared, tools, Defaults, "test"); err != nil {
		t.Fatal(err)
	}
	changed := skill
	changed.Instructions += " changed"
	if SkillDigest(changed, taxonomy) == vector.Pin {
		t.Fatal("instructions must be pinned")
	}
	if SkillDigest(skill, map[string]*ToolTaxonomy{"ask_user": {Operation: "read", Effect: "read"}}) == vector.Pin {
		t.Fatal("dependency taxonomy must be pinned")
	}
	replacement := skill
	replacement.ID = "project-ask"
	replacement.Source = SkillSource{Kind: "custom", ID: "project", Trust: "person", Version: "2"}
	replacement.Instructions = "Use project context before asking."
	replacement.PinnedDigest = SkillDigest(replacement, taxonomy)
	chosen, err := SelectAskingWell([]Skill{replacement}, tools)
	if err != nil || len(chosen) != 1 || chosen[0].Source.Trust != "person" {
		t.Fatal("replacement provenance changed")
	}
	for _, bad := range [][]Skill{{func() Skill { x := replacement; x.Kind = "knowledge"; return x }()}, {replacement, replacement}, {func() Skill { x := replacement; x.Tools = nil; return x }()}} {
		if _, err = SelectAskingWell(bad, tools); err == nil {
			t.Fatal("invalid replacement accepted")
		}
	}
	absent, err := SelectAskingWell(nil, nil)
	if err != nil || len(absent) != 0 {
		t.Fatal("absent question must not insert")
	}
	stale := skill
	stale.PinnedDigest = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	denied, err := DeclareSkills([]Skill{stale}, []string{"ask_user"}, Defaults, nil, taxonomy)
	if err != nil {
		t.Fatal(err)
	}
	if RequireAskingWell(denied, tools, Defaults, "test") == nil {
		t.Fatal("stale pin must refuse without fallback")
	}
	for _, bounds := range []Policy{func() Policy { p := Defaults; p.CataloguedSkills = 0; return p }(), func() Policy { p := Defaults; p.SkillsCatalogueBytes = 0; return p }()} {
		if RequireAskingWell(declared, tools, bounds, "test") == nil {
			t.Fatal("capacity must refuse")
		}
	}
	for _, mode := range []string{"bytes", "missing"} {
		p := Defaults
		names := []string{"ask_user"}
		if mode == "bytes" {
			p.SkillBytes = 1
		} else {
			names = nil
		}
		d, e := DeclareSkills([]Skill{skill}, names, p, nil, taxonomy)
		if e != nil {
			t.Fatal(e)
		}
		if RequireAskingWell(d, tools, Defaults, "test") == nil {
			t.Fatal("withheld method must refuse")
		}
	}
}
