package runcontext

import "github.com/Judgment-Pack/judgment-pack-agent/go/failure"

// AskingWellPin pins the immutable package-owned version 1 bundle and ask/pure dependency.
const AskingWellPin = "sha256:265985dc2ea1d8290d5663e7a3ed4d8e25af47d4ba2beb7194d8029791b41692"
const AskingWellInstructions = "Read the available context and use available reading tools before asking the person. Ask only for necessary information that remains unavailable. Do not repeat a question already answered in this run. Ask one decision per question and state what changes depending on the answer. Offer 2 to 4 mutually exclusive options, with the recommended option first and a one-line reason grounded in the available context. Preserve free text and the person’s choice. A recommendation is never an answer: wait for the person’s actual reply. This method grants no tool or permission; existing controls still apply."

// AskingWellSkill returns fresh package-owned bytes; host replacements keep their provenance.
func AskingWellSkill() Skill {
	return Skill{ID: "asking-well", Name: "asking-well", Kind: "method", Description: "Ask only for missing necessary information, with clear choices and a reasoned recommendation.",
		Source:       SkillSource{Kind: "custom", ID: "agent-methods", Trust: "first-party", Version: "1"},
		Instructions: AskingWellInstructions, Tools: []string{"ask_user"}, PinnedDigest: AskingWellPin}
}
func askingWellRefusal() error {
	return failure.Authored("configuration", "The offered ask_user requires a catalogued asking-well method with only ask_user as its dependency and a current approved digest. Nothing was sent.", "context")
}

// OffersAskUser reads the retained phase/source offer, never AllowConversation.
func OffersAskUser(tools []HostTool) bool {
	for _, t := range tools {
		if t.Name == "ask_user" && t.Interaction == "question" {
			return true
		}
	}
	return false
}

// SelectAskingWell reserves asking-well for an explicit host method replacement when offered.
func SelectAskingWell(declared []Skill, tools []HostTool) ([]Skill, error) {
	if !OffersAskUser(tools) {
		return declared, nil
	}
	for _, t := range tools {
		if t.Name == "ask_user" && (t.Taxonomy == nil || t.Taxonomy.Operation != "ask" || t.Taxonomy.Effect != "pure") {
			return nil, askingWellRefusal()
		}
	}
	count := 0
	for _, s := range declared {
		if s.Name == "asking-well" {
			count++
			if s.Kind != "method" || len(s.Tools) != 1 || s.Tools[0] != "ask_user" {
				return nil, askingWellRefusal()
			}
		}
	}
	if count > 1 {
		return nil, askingWellRefusal()
	}
	if count == 1 {
		return declared, nil
	}
	return append(append([]Skill(nil), declared...), AskingWellSkill()), nil
}

// RequireAskingWell uses normal admission and catalogue bounds; it never expands either.
func RequireAskingWell(declared *DeclaredSkills, tools []HostTool, policy Policy, marker string) error {
	if !OffersAskUser(tools) {
		return nil
	}
	_, catalogued, _ := CatalogueSkills(declared.Skills, policy, marker)
	for _, s := range catalogued {
		if s.Name == "asking-well" && s.Kind == "method" && s.Profile.Effect == "pure" && len(s.Profile.Operations) == 1 && s.Profile.Operations[0] == "ask" {
			return nil
		}
	}
	return askingWellRefusal()
}
