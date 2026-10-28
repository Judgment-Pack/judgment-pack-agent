package runcontext

import (
	"context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"strings"
	"testing"
)

type relationLegacyPort struct{ RunStore }

func relationActual(t *testing.T, legacy ...bool) *MemoryRunStore {
	t.Helper()
	ctx := context.Background()
	store := NewMemoryRunStore(MemoryRunStoreOptions{})
	var port RunStore = store
	if len(legacy) > 0 && legacy[0] {
		port = &relationLegacyPort{RunStore: store}
	}
	writer, e := NewRunWriter(port, "r", nil, nil)
	if e != nil {
		t.Fatal(e)
	}
	state := writerCheckpoint("r")
	writer.Bind(func() (contract.Checkpoint, error) { return state, nil }, RecordDefaults, nil)
	binding := RunBinding{Writer: writer, Scope: DurableScope{Execution: "r", Role: "author"}, Root: true}
	calls := 0
	if _, e = InvokeDurable(ctx, &binding, "host", "host-source", "save_object", []byte(`{"value":1}`), false, func([]byte) (ToolResult, error) {
		calls++
		return ReadToolResult(MustParseJSON(`{"content":[{"type":"text","text":"SECRET-123"}]}`)), nil
	}, nil, nil); e != nil {
		t.Fatal(e)
	}
	state.Status = "complete"
	if e = writer.Save(ctx, binding, state); e != nil {
		t.Fatal(e)
	}
	if calls != 1 {
		t.Fatal("physical host count")
	}
	return store
}
func relationAppend(t *testing.T, store *MemoryRunStore, change func(*Value)) {
	t.Helper()
	ctx := context.Background()
	view, e := ReadRetainedRunHistory(ctx, store, "r")
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := view.ReadObject(*view.Head().Checkpoint)
	cp := MustParseJSON(string(raw))
	claim := cp.Get("durable").Get("dispatches").Items()[0]
	var observed *Value
	for _, row := range cp.Get("durable").Get("observations").Items() {
		if row.Get("dispatchId").Str() == claim.Get("id").Str() && row.Get("kind").Str() == "operation" {
			observed = row
			break
		}
	}
	target := ObjectOf("kind", String("job"), "id", String("job-observed-by-host"))
	evidence := ObjectOf("version", Number(1), "kind", String("observed-relation"), "scope", claim.Get("scope").Clone(), "dispatchId", claim.Get("id").Clone(), "checkpoint", historyRefJSON(*view.Head().Checkpoint), "observationId", observed.Get("id").Clone(), "requestDigest", claim.Get("requestDigest").Clone(), "resultDigest", String(HistoryBytesDigest([]byte(CanonicalJSON(claim.Get("result"))))), "target", target.Clone())
	if change != nil {
		change(evidence)
	}
	object, e := NewHistoryObject([]byte(CanonicalJSON(evidence)))
	if e != nil {
		t.Fatal(e)
	}
	observation := ObjectOf("version", Number(1), "kind", String("relation"), "scope", claim.Get("scope").Clone(), "dispatchId", evidence.Get("dispatchId").Clone(), "target", target, "evidence", historyRefJSON(object.HistoryObjectRef))
	prepared, e := prepareHistoryObservation(observation, []HistoryObject{object})
	if e != nil {
		t.Fatal(e)
	}
	ref, e := StageHistoryObservation(ctx, store, "r", prepared)
	if e != nil {
		t.Fatal(e)
	}
	head, _ := store.Load(ctx, "r")
	if _, e = store.CommitHistory(ctx, RunHistoryCommit{Run: RunCommit{RunID: "r", ExpectedRevision: head.Revision, CommitID: "host-relation", Checkpoint: head.Checkpoint, Events: [][]byte{}}, Observations: []HistoryObjectRef{ref}}); e != nil {
		t.Fatal(e)
	}
}
func TestHistoryRelationsActualScopedEvidenceAndRedactedArchive(t *testing.T) {
	ctx := context.Background()
	store := relationActual(t)
	relationAppend(t, store, nil)
	view, e := ReadRetainedRunHistory(ctx, store, "r")
	if e != nil {
		t.Fatal(e)
	}
	found, e := FindHistoryRelations(view, "job", "job-observed-by-host")
	if e != nil || found.Kind != "observed" || len(found.Rows) != 1 {
		t.Fatal("actual relation", e)
	}
	missing, e := FindHistoryRelations(view, "job", "SECRET-123")
	if e != nil || missing.Kind != "unavailable" {
		t.Fatal("prose/result value inferred")
	}
	exported, e := ExportRunArchive(ctx, store, "r", ArchiveExportOptions{Members: archiveMembers()})
	if e != nil || strings.Contains(string(exported.Bytes), "SECRET-123") {
		t.Fatal("redacted export", e)
	}
	imported, e := ReadRunArchive(exported.Bytes, "")
	if e != nil {
		t.Fatal(e)
	}
	found, e = FindHistoryRelations(imported, "job", "job-observed-by-host")
	if e != nil || found.Kind != "observed" {
		t.Fatal("source label relation lost", e)
	}
}
func TestHistoryRelationsMismatchRefusedBeforeExposure(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*Value)
	}{{"absent-dispatch", func(e *Value) { e.Set("dispatchId", String("missing")) }}, {"request", func(e *Value) { e.Set("requestDigest", String("sha256:"+strings.Repeat("0", 64))) }}, {"result", func(e *Value) { e.Set("resultDigest", String("sha256:"+strings.Repeat("0", 64))) }}, {"observation", func(e *Value) { e.Set("observationId", String("unobserved")) }}, {"scope", func(e *Value) { e.Set("scope", ObjectOf("execution", String("foreign"), "role", String("author"))) }}, {"target", func(e *Value) { e.Set("target", ObjectOf("kind", String("job"), "id", String("foreign"))) }}, {"checkpoint", func(e *Value) {
		e.Set("checkpoint", historyRefJSON(HistoryObjectRef{Digest: "sha256:" + strings.Repeat("0", 64), Bytes: 1}))
	}}} {
		t.Run(test.name, func(t *testing.T) {
			store := relationActual(t)
			relationAppend(t, store, test.change)
			if _, e := ReadRetainedRunHistory(context.Background(), store, "r"); e == nil {
				t.Fatal("unmatched relation exposed")
			}
			if _, e := ExportRunArchive(context.Background(), store, "r", ArchiveExportOptions{}); e == nil {
				t.Fatal("unmatched relation exported")
			}
		})
	}
}

func TestHistoryRelationsMissingRetainedRequestRefused(t *testing.T) {
	store := relationActual(t, true)
	relationAppend(t, store, nil)
	if _, e := ReadRetainedRunHistory(context.Background(), store, "r"); e == nil {
		t.Fatal("legacy relation without request evidence exposed")
	}
}
