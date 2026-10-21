package runcontext

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestProposalEditsAtomicResumeAndUndo(t *testing.T) {
	s := modelSession()
	s.Context = &Input{AcceptedDocuments: []AcceptedDocument{{ID: "pack", Document: json.RawMessage(`{"specVersion":"1","id":"pack","title":"Before","notes":"same same"}`)}}}
	s.Tools = []RuntimeTool{{Name: "validate"}}
	s.CallTool = func(_ context.Context, name string, args json.RawMessage) (ToolResult, error) {
		v, _ := ParseJSON(args)
		doc, _ := ParseJSON([]byte(editString(v.Get("content"))))
		status := "invalid"
		if editString(doc.Get("specVersion")) == "1" {
			status = "valid"
		}
		return Answer(ObjectOf("status", String(status))), nil
	}
	opened, err := ContextualSession(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Context.Close()
	g := opened.Context.Drafts
	run := func(name, args string) {
		t.Helper()
		if _, err := g.Edit(context.Background(), name, json.RawMessage(args)); err != nil {
			t.Fatal(err)
		}
	}
	search, err := opened.Context.Ledger.Search(MustParseJSON(`{"id":"draft:pack","pattern":"Before"}`))
	if err != nil || len(search.Matches) != 1 || search.Matches[0].Line != 4 {
		t.Fatalf("search: %+v %v", search, err)
	}
	run("propose_edit", `{"document":"pack","old_text":"Before","new_text":"After"}`)
	if !strings.Contains(string(g.Proposal().Document), `"After"`) || len(g.Proposal().Edits) != 1 {
		t.Fatal("whole draft or provenance lost")
	}
	for _, bad := range []struct{ name, args, reason string }{{"propose_edit", `{"document":"pack","old_text":"same","new_text":"x"}`, "2 times"}, {"propose_delete", `{"document":"pack","old_text":"  \"specVersion\": \"1\",\n"}`, "Validation failed"}} {
		before := string(g.Proposal().Document)
		if _, err := g.Edit(context.Background(), bad.name, json.RawMessage(bad.args)); err == nil || !strings.Contains(err.Error(), bad.reason) {
			t.Fatalf("refusal: %v", err)
		}
		if string(g.Proposal().Document) != before {
			t.Fatal("refused edit changed draft")
		}
	}
	saved, err := opened.Context.Execution.Snapshot("")
	if err != nil {
		t.Fatal(err)
	}
	s.Context = &Input{Execution: &saved}
	resumed, err := ContextualSession(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Context.Close()
	g = resumed.Context.Drafts
	if len(g.Proposal().Edits) != 1 {
		t.Fatal("resume lost provenance")
	}
	run("propose_insert", `{"document":"pack","after_anchor":"After","text":" again"}`)
	run("undo_edit", `{"document":"pack"}`)
	if !strings.Contains(string(g.Proposal().Document), `"After"`) {
		t.Fatal("undo did not restore preceding draft")
	}
	run("undo_edit", `{"document":"pack"}`)
	if !strings.Contains(string(g.Proposal().Document), `"Before"`) {
		t.Fatal("undo did not restore origin")
	}
	if _, err := g.Edit(context.Background(), "undo_edit", json.RawMessage(`{"document":"pack"}`)); err == nil {
		t.Fatal("undo applied twice")
	}
}
