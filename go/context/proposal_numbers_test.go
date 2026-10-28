package runcontext

import (
	"context"
	"encoding/json"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"strings"
	"testing"
)

func TestProposalNumericDraftCustody(t *testing.T) {
	s := modelSession()
	s.Context = &Input{AcceptedDocuments: []AcceptedDocument{{ID: "p", Document: json.RawMessage(`{"id":"p","n":1.20,"zero":-0,"nested":{"n":2.500}}`)}}}
	s.Tools = []RuntimeTool{{Name: "validate", Meta: &ToolTaxonomy{Operation: "check", Effect: "pure", CheckKind: "validate"}}}
	validated := 0
	s.CallTool = func(context.Context, string, json.RawMessage) (ToolResult, error) {
		validated++
		return Answer(ObjectOf("status", String("valid"))), nil
	}
	opened, err := ContextualSession(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Context.Close()
	g := opened.Context.Drafts
	initial := string(g.Proposal().Document)
	prior, _ := json.Marshal(g.Snapshot())
	if _, err = g.Edit(context.Background(), "propose_edit", json.RawMessage(`{"document":"p","old_text":"1.2","new_text":"9007199254740993"}`)); err == nil || !strings.Contains(err.Error(), "numerical value changes") {
		t.Fatalf("lossy edit: %v", err)
	}
	after, _ := json.Marshal(g.Snapshot())
	if string(after) != string(prior) || string(g.Proposal().Document) != initial || validated != 0 {
		t.Fatal("lossy edit changed custody or reached validation")
	}
	if _, err = g.Edit(context.Background(), "propose_edit", json.RawMessage(`{"document":"p","old_text":"1.2","new_text":"1.50"}`)); err != nil {
		t.Fatal(err)
	}
	saved, err := opened.Context.Execution.Snapshot("")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(saved)
	var roundtrip contract.Checkpoint
	if err = json.Unmarshal(raw, &roundtrip); err != nil {
		t.Fatal(err)
	}
	s.Context = &Input{Execution: &roundtrip}
	resumed, err := ContextualSession(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Context.Close()
	g = resumed.Context.Drafts
	if _, err = g.Edit(context.Background(), "undo_edit", json.RawMessage(`{"document":"p"}`)); err != nil {
		t.Fatal(err)
	}
	if string(g.Proposal().Document) != initial || len(g.Proposal().Edits) != 2 {
		t.Fatal("resume/undo changed numeric source custody")
	}
}
func TestProposalNumericStoredTextPredicate(t *testing.T) {
	for _, text := range []string{`{"id":"p","n":9007199254740993}`, `{"id":"p","n":9007199254740992}`} {
		s := modelSession()
		opened, err := ContextualSession(context.Background(), s)
		if err != nil {
			t.Fatal(err)
		}
		digest := "sha256:" + ContextDigest(text)
		row := contract.CheckpointDraftDocument{ID: "draft:p", Digest: digest, Edits: []contract.ProvenanceEdit{}}
		content, _ := json.Marshal(draftContent{Text: text, Unknowns: []string{}, Undo: []draftUndo{}})
		v, _ := ParseJSON(content)
		_, err = opened.Context.Ledger.Retain(Document{ID: "proposal-state:handwritten", Kind: "artifact", Label: draftLabel(row), Value: v}, false)
		if err != nil {
			t.Fatal(err)
		}
		opened.Context.Execution.MutateCheckpoint(CheckpointPatch{"Draft": &contract.CheckpointDraft{Documents: []contract.CheckpointDraftDocument{row}}})
		err = opened.Context.OpenDrafts()
		if strings.Contains(text, "9007199254740993") {
			if err == nil || !strings.Contains(err.Error(), "saved draft text violates") {
				t.Fatalf("stored violating text: %v", err)
			}
		} else if err != nil {
			t.Fatalf("already-rounded historical text refused: %v", err)
		}
		opened.Context.Close()
	}
}

func TestProposalNumericDecodedOutputGuard(t *testing.T) {
	for _, mode := range []string{"redact", "refuse"} {
		for _, valid := range []bool{true, false} {
			s := modelSession()
			s.Policy = PolicyLayer{"outputGuard": mode}
			s.Tools = []RuntimeTool{{Name: "validate", Meta: &ToolTaxonomy{Operation: "check", Effect: "pure", CheckKind: "validate"}}}
			validations := 0
			s.CallTool = func(context.Context, string, json.RawMessage) (ToolResult, error) {
				validations++
				status := "invalid"
				if valid {
					status = "valid"
				}
				return Answer(ObjectOf("status", String(status))), nil
			}
			opened, err := ContextualSession(context.Background(), s)
			if err != nil {
				t.Fatal(err)
			}
			err = opened.Context.Drafts.SeedContext(context.Background(), json.RawMessage(`{"id":"p","n":1.20,"nested":{"\u0041KIAABCDEFGHIJKLMNOP":"\u0041KIAABCDEFGHIJKLMNOP"}}`), nil)
			if mode == "refuse" || !valid {
				if err == nil || opened.Context.Drafts.Proposal() != nil {
					t.Fatalf("guarded proposal admitted mode=%s valid=%v", mode, valid)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				text := string(opened.Context.Drafts.Proposal().Document)
				if !strings.Contains(text, "[redacted: aws-access-key]") || strings.Contains(text, "AKIAABCDEFGHIJKLMNOP") || validations != 1 {
					t.Fatalf("decoded guard/custody: %s", text)
				}
			}
			opened.Context.Close()
		}
	}
}
func TestProposalNumericTypedSeedDepth(t *testing.T) {
	opened, err := ContextualSession(context.Background(), modelSession())
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Context.Close()
	source := `{"n":1}`
	for at := 0; at < 128; at++ {
		source = `{"nested":` + source + `}`
	}
	if err := opened.Context.Drafts.Seed(json.RawMessage(source), nil); err == nil || !strings.Contains(err.Error(), "work bound") {
		t.Fatalf("seed depth: %v", err)
	}
	if opened.Context.Drafts.Proposal() != nil {
		t.Fatal("over-depth seed acquired custody")
	}
}
