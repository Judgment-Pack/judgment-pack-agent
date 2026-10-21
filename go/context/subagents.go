package runcontext

// ADR-0013: admission and catalogue for pinned, reads-only host definitions.
import (
	"encoding/json"
	"fmt"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
	"slices"
	"strings"
)

// Shared by admission and fitting; the 16-byte encoded summary and mandatory fields
// are carried by subagents/result-minimum.json (unstated is the longest confidence).
var SubagentResultMinBytes = UTF8Bytes(ObjectOf("summary", String("No findings..."), "findings", Array(), "citations", Array(), "openQuestions", Array(), "confidence", String("unstated"), "notDone", Array()).Text())

const SubagentChanged = "subagent changed: The declared sub-agent was withdrawn or changed; it was not dispatched again."

type Subagent = contract.Subagent

func SubagentDigest(s Subagent) string {
	raw, _ := json.Marshal(s)
	var bundle map[string]json.RawMessage
	_ = json.Unmarshal(raw, &bundle)
	delete(bundle, "digest")
	delete(bundle, "pinnedDigest")
	raw, _ = json.Marshal(bundle)
	return "sha256:" + ContextDigest(CanonicalJSON(MustParseJSON(string(raw))))
}
func SubagentRecord(s Subagent) *contract.SubagentRecord {
	if s.Digest == nil {
		return nil
	}
	return &contract.SubagentRecord{ID: s.ID, Digest: *s.Digest, Source: contract.SubagentRecordSource(s.Source)}
}
func SubagentProfile(s Subagent) contract.DelegatedTaskProfile {
	model, report := "child", "analyze"
	return contract.DelegatedTaskProfile{ID: s.ID, Description: s.Purpose, Instructions: "Complete the assigned read-only task.", Capabilities: []string{}, Model: &model, Report: &report, Budget: &contract.DelegatedTaskBudget{ModelRequests: s.Bounds.ModelRequests, ToolCalls: s.Bounds.ToolCalls, Milliseconds: s.Bounds.Milliseconds}}
}
func SubagentNotice(id, detail, reason string) contract.RunEvent {
	return contract.RunEvent{Guardrail: &contract.RunEventGuardrail{Type: "guardrail", Tool: id, Action: "refused", Detail: detail, Reason: &reason}}
}
func DeclareSubagents(declared []Subagent, tools []HostTool, skills []AdmittedSkill, policy Policy) ([]Subagent, []contract.RunEvent, error) {
	var admitted []Subagent
	var notices []contract.RunEvent
	ids := map[string]bool{}
	for _, s := range declared {
		src := SkillSource{Kind: s.Source.Kind, ID: s.Source.ID, Trust: s.Source.Trust}
		if s.Source.Version != nil {
			src.Version = *s.Source.Version
		}
		if s.Source.ToolListDigest != nil {
			src.ToolListDigest = *s.Source.ToolListDigest
		}
		bad := !sourceID.MatchString(s.ID) || s.Name == "" || U16Length(s.Name) > 64 || s.Purpose == "" || U16Length(s.Purpose) > 1024 || s.Instructions == "" || U16Length(s.Instructions) > 4000 || s.Model == "" || U16Length(s.Model) > 200 || s.Reasoning == "" || U16Length(s.Reasoning) > 16 || skillSourceProblem(src) != "" || !sourceDigest.MatchString(s.PinnedDigest) || s.Digest != nil || len(s.Tools) > 64 || len(s.Skills) > 32 || ids[s.ID]
		for i, name := range s.Tools {
			if name == "" || U16Length(name) > 128 || slices.Contains(s.Tools[:i], name) {
				bad = true
			}
		}
		for i, name := range s.Skills {
			if !sourceID.MatchString(name) || slices.Contains(s.Skills[:i], name) {
				bad = true
			}
		}
		b := s.Bounds
		if bad || b.ModelRequests < 1 || b.AgentRuns < 1 || b.ToolCalls < 1 || b.Bytes < 1 || b.Milliseconds < 1 || b.ResultBytes < 1 {
			return nil, nil, failure.Authored("configuration", "The sub-agent declaration is not a closed subagent.json definition. Nothing was sent.", "context")
		}
		ids[s.ID] = true
		hold := func(detail string) { notices = append(notices, SubagentNotice(s.ID, detail, "subagent withheld")) }
		if slices.Contains([]string{"analyze", "review", "research", "author-test"}, s.ID) {
			hold("A declaration cannot replace a built-in profile.")
			continue
		}
		unknown, unsafe := false, false
		for _, name := range s.Tools {
			at := slices.IndexFunc(tools, func(t HostTool) bool { return t.Name == name })
			if at < 0 {
				unknown = true
				continue
			}
			t := tools[at]
			if t.Interaction != "" || !(t.Source != nil && t.EffectClass == "read" || t.Source == nil && (t.Effect == "read" || t.Effect == "" && t.Delegation == "read")) {
				unsafe = true
			}
		}
		if unknown {
			hold("An undeclared tool was named; the definition was withheld.")
			continue
		}
		if unsafe {
			hold("A sub-agent admits declared reads only; effects cannot be raised.")
			continue
		}
		for _, id := range s.Skills {
			if !slices.ContainsFunc(skills, func(skill AdmittedSkill) bool { return skill.ID == id }) {
				unknown = true
			}
		}
		if unknown {
			hold("An undeclared skill was named; the definition was withheld.")
			continue
		}
		if b.ModelRequests > 10 || b.AgentRuns > 5 || b.ToolCalls > 12 || b.Bytes > int64(policy.MaxInputBytes) || b.Milliseconds > int64(min(360000, policy.TurnMilliseconds)) || b.ResultBytes > int64(policy.ToolResultBytes) {
			hold("The definition exceeds the turn or child bounds ceiling.")
			continue
		}
		if b.ResultBytes < int64(SubagentResultMinBytes) {
			hold(fmt.Sprintf("resultBytes %d is below the report minimum %d.", b.ResultBytes, SubagentResultMinBytes))
			continue
		}
		if UTF8Bytes(s.Instructions) > policy.ProjectInstructionsBytes {
			hold("The definition instructions exceed projectInstructionsBytes.")
			continue
		}
		digest := SubagentDigest(s)
		if digest != s.PinnedDigest {
			hold("The definition digest changed; re-approval is required.")
			continue
		}
		s.Digest = &digest
		admitted = append(admitted, s)
	}
	return admitted, notices, nil
}
func CatalogueSubagents(definitions []Subagent, policy Policy, marker string) (string, []Subagent, []contract.RunEvent) {
	var kept []Subagent
	var entries []string
	var notices []contract.RunEvent
	bytes := 0
	for _, s := range definitions {
		purpose := Prefix(strings.TrimSpace(strings.Split(s.Purpose, "\n")[0]), 160)
		size := UTF8Bytes(ObjectOf("id", String(s.ID), "purpose", String(purpose)).Text())
		if len(kept) >= policy.CataloguedSubagents || bytes+size > policy.SubagentCatalogueBytes {
			notices = append(notices, SubagentNotice(s.ID, "The sub-agent catalogue count or byte bound was reached.", "subagent withheld"))
			continue
		}
		bytes += size
		kept = append(kept, s)
		name, _ := json.Marshal(s.ID)
		entries = append(entries, s.ID+"\n"+FrameBlock("the purpose of sub-agent "+string(name), purpose, marker))
	}
	if len(entries) == 0 {
		return "", kept, notices
	}
	return "\n\nDECLARED SUB-AGENTS (reads only; select by profile id)\n" + strings.Join(entries, "\n") + "\nEND DECLARED SUB-AGENTS", kept, notices
}

// SubagentAdmissionBudget resolves host bounds before a definition contributes any model role.
func SubagentAdmissionBudget(session Session) (Policy, error) {
	session.Subagents = nil
	var configuration *Configuration
	var budget *ModelBudget
	if session.Context != nil {
		configuration = session.Context.Configuration
	}
	if session.Model != nil {
		budget = session.Model.Budget
	}
	policy, err := ContextPolicy(configuration, session.ModelName(), budget)
	if err != nil {
		return policy, err
	}
	resolved, err := resolveSessionPolicy(session, policy)
	if err != nil {
		return policy, err
	}
	raw := map[string]any{}
	for name, member := range resolved.Members {
		if strings.HasPrefix(name, "budget.") {
			raw[strings.TrimPrefix(name, "budget.")] = member.Value
		}
	}
	encoded, _ := json.Marshal(raw)
	err = json.Unmarshal(encoded, &policy)
	return policy, err
}
func RecordSubagents(request *contract.DelegatedTaskRequest, definitions []Subagent) {
	for i := range request.Tasks {
		for _, s := range definitions {
			if s.ID == request.Tasks[i].Profile {
				request.Tasks[i].Subagent = SubagentRecord(s)
			}
		}
	}
}

func ValidSubagentRecord(v *Value) bool {
	var record contract.SubagentRecord
	if !isObject(v) || json.Unmarshal(v.JSON(), &record) != nil || !sourceID.MatchString(record.ID) || !sourceDigest.MatchString(record.Digest) {
		return false
	}
	s := SkillSource{Kind: record.Source.Kind, ID: record.Source.ID, Trust: record.Source.Trust}
	if record.Source.Version != nil {
		s.Version = *record.Source.Version
	}
	if record.Source.ToolListDigest != nil {
		s.ToolListDigest = *record.Source.ToolListDigest
	}
	return skillSourceProblem(s) == ""
}
