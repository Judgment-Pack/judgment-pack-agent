package runcontext

import (
	"bytes"
	"context"
	"os"
	"testing"
)

func taskIdentityFixture(t *testing.T) *Value {
	t.Helper()
	raw, e := os.ReadFile("../../contract/fixtures/history/task-identity.json")
	if e != nil {
		t.Fatal(e)
	}
	v, e := ParseJSON(raw)
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func TestHistoryArchiveCurrentTaskIdentity(t *testing.T) {
	v := taskIdentityFixture(t)
	if cp, e := ReadExecution(v.Get("checkpoint")); e != nil || cp == nil {
		t.Fatal("otherwise-valid actual current checkpoint3", e)
	}
	for _, row := range v.Get("cases").Items() {
		t.Run(row.Get("name").Str(), func(t *testing.T) {
			raw := []byte(CanonicalJSON(row.Get("archive")))
			view, e := ReadRunArchive(raw, row.Get("expectedDigest").Str())
			if row.Get("valid").Truth() {
				if e != nil || !bytes.Equal(view.CanonicalBytes(), raw) {
					t.Fatal("valid task checkpoint archive refused", e)
				}
			} else if e == nil {
				t.Fatal("protected current task identity edit admitted")
			}
		})
	}
}
func TestHistoryArchiveTaskProseAndUnrelatedUserObject(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryRunStore(MemoryRunStoreOptions{})
	v := taskIdentityFixture(t)
	cp := v.Get("checkpoint")
	id := cp.Get("id").Str()
	_, e := store.Commit(ctx, RunCommit{RunID: id, CommitID: "task-source", Checkpoint: []byte(CanonicalJSON(cp)), Events: [][]byte{}})
	if e != nil {
		t.Fatal(e)
	}
	prepared, e := PrepareHistoryOriginal(DurableScope{Execution: id, Role: "author"}, "user-context", `{"dependsOn":["PRIVATE"],"planState":{"taskSchedule":{"run":"PRIVATE"},"taskEnvelope":{"run":"PRIVATE"}}}`, "context", 0)
	if e != nil {
		t.Fatal(e)
	}
	ref, e := StageHistoryObservation(ctx, store, id, prepared)
	if e != nil {
		t.Fatal(e)
	}
	head, _ := store.Load(ctx, id)
	_, e = store.CommitHistory(ctx, RunHistoryCommit{Run: RunCommit{RunID: id, ExpectedRevision: head.Revision, CommitID: "user-original", Checkpoint: head.Checkpoint, Events: [][]byte{}}, Observations: []HistoryObjectRef{ref}})
	if e != nil {
		t.Fatal(e)
	}
	result, e := ExportRunArchive(ctx, store, id, ArchiveExportOptions{Members: PolicyMembers{"outputPatterns": {Value: []any{map[string]any{"name": "prose", "pattern": "First secret prose"}, map[string]any{"name": "user", "pattern": "PRIVATE"}}}}})
	if e != nil {
		t.Fatal(e)
	}
	view, e := ReadRunArchive(result.Bytes, "")
	if e != nil {
		t.Fatal(e)
	}
	display, e := view.ReadDisplay(*view.Head().Checkpoint, "checkpoint")
	if e != nil {
		t.Fatal(e)
	}
	if display.Document.Get("plan").Items()[0].Get("step").Str() != "[redacted: prose]" || !archiveEqual(display.Document.Get("plan").Items()[1].Get("dependsOn"), cp.Get("plan").Items()[1].Get("dependsOn")) || !archiveEqual(display.Document.Get("planState").Get("taskSchedule"), cp.Get("planState").Get("taskSchedule")) || !archiveEqual(display.Document.Get("planState").Get("taskEnvelope"), cp.Get("planState").Get("taskEnvelope")) {
		t.Fatal("task identity/prose mismatch")
	}
	seen := false
	for _, m := range view.Manifest().Mappings {
		if m.Kind == "source-chunk" {
			d, e := view.ReadDisplay(m.Source, m.Kind)
			if e != nil || !bytes.Contains([]byte(d.Document.Str()), []byte("[redacted: user]")) {
				t.Fatal("unrelated user object was overblocked", e)
			}
			seen = true
		}
	}
	if !seen {
		t.Fatal("source fixture absent")
	}
}
