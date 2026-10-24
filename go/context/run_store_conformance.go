package runcontext

import (
	"bytes"
	"context"
	"errors"
)

// StorageConformanceFailure names the violated port property without exposing record content.
type StorageConformanceFailure struct{ Property string }

func (e *StorageConformanceFailure) Error() string {
	return "Storage conformance failed: " + e.Property
}

// CheckRunStoreConformance is reusable by hosts. Factory creates a fresh, authorized, isolated
// store every time. It certifies this port, not host lease/attachment/backup properties. Fixtures
// are hand authored. Physical fault injection is tested separately at the host's transaction seam.
func CheckRunStoreConformance(ctx context.Context, create func() RunStore) error {
	fail := func(property string) error { return &StorageConformanceFailure{Property: property} }
	store := create()
	if store.Durability() == RunStoreSingleWriterCallback {
		return fail("run.durability.declared")
	}
	raw := []byte(`{"id":"run-alpha","payload":{"nested":[null,0,"kept"]}}`)
	input := RunCommit{RunID: "run-alpha", CommitID: "open-alpha", Checkpoint: raw, Events: [][]byte{[]byte(`{"type":"opened"}`)}}
	committed, err := store.Commit(ctx, input)
	if err != nil {
		return fail("run.head.atomic")
	}
	if committed.CommitID != "open-alpha" || committed.Revision == "" || committed.Cursor == "" {
		return fail("run.receipt.scoped")
	}
	raw[0] = ' '
	input.Events[0][0] = ' '
	head, err := store.Load(ctx, "run-alpha")
	if err != nil || head == nil || !bytes.Equal(head.Checkpoint, []byte(`{"id":"run-alpha","payload":{"nested":[null,0,"kept"]}}`)) {
		return fail("run.bytes.preserved")
	}
	if head.Revision != committed.Revision || head.Cursor != committed.Cursor {
		return fail("run.head.atomic")
	}
	head.Checkpoint[0] = ' '
	head, err = store.Load(ctx, "run-alpha")
	if err != nil || head == nil || head.Checkpoint[0] != '{' {
		return fail("run.bytes.detached")
	}
	input.Checkpoint = []byte(`{"id":"run-alpha","payload":{"nested":[null,0,"kept"]}}`)
	input.Events = [][]byte{[]byte(`{"type":"opened"}`)}
	again, err := store.Commit(ctx, input)
	if err != nil || again != committed {
		return fail("run.commit.idempotent")
	}
	changed := input
	changed.Checkpoint = []byte(`{}`)
	_, err = store.Commit(ctx, changed)
	var mismatch *StorageCommitMismatch
	if !errors.As(err, &mismatch) {
		return fail("run.commit.content-mismatch")
	}
	changed = input
	changed.CommitID = "different-open"
	_, err = store.Commit(ctx, changed)
	var conflict *StorageConflict
	if !errors.As(err, &conflict) {
		return fail("run.create.no-overwrite")
	}
	type outcome struct {
		name    string
		receipt RunReceipt
		err     error
	}
	results := make(chan outcome, 2)
	for _, name := range []string{"left", "right"} {
		go func(name string) {
			r, e := store.Commit(ctx, RunCommit{RunID: "run-alpha", ExpectedRevision: committed.Revision, CommitID: name, Checkpoint: []byte(`{"winner":"` + name + `"}`), Events: [][]byte{[]byte(`{"winner":"` + name + `"}`)}})
			results <- outcome{name: name, receipt: r, err: e}
		}(name)
	}
	a, b := <-results, <-results
	if a.err != nil {
		a, b = b, a
	}
	if a.err != nil || !errors.As(b.err, &conflict) {
		return fail("run.cas.atomic")
	}
	head, err = store.Load(ctx, "run-alpha")
	all, readErr := store.ReadEvents(ctx, "run-alpha", "", 100)
	if err != nil || readErr != nil || head == nil || string(head.Checkpoint) != `{"winner":"`+a.name+`"}` || len(all.Items) != 2 || string(all.Items[1].Bytes) != string(head.Checkpoint) {
		return fail("run.cas.checkpoint-and-events")
	}
	appended, err := store.Commit(ctx, RunCommit{RunID: "run-alpha", ExpectedRevision: a.receipt.Revision, CommitID: "event-only", Events: [][]byte{[]byte(`{"type":"third"}`)}})
	after, loadErr := store.Load(ctx, "run-alpha")
	if err != nil || loadErr != nil || after == nil || appended.Revision == a.receipt.Revision || !bytes.Equal(after.Checkpoint, head.Checkpoint) {
		return fail("run.event-only.revision")
	}
	first, e1 := store.ReadEvents(ctx, "run-alpha", "", 1)
	second, e2 := store.ReadEvents(ctx, "run-alpha", first.NextCursor, 1)
	third, e3 := store.ReadEvents(ctx, "run-alpha", second.NextCursor, 1)
	end, e4 := store.ReadEvents(ctx, "run-alpha", third.NextCursor, 1)
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil || len(first.Items) != 1 || len(second.Items) != 1 || len(third.Items) != 1 || len(end.Items) != 0 || first.Items[0].Cursor != committed.Cursor || second.Items[0].Cursor != a.receipt.Cursor || third.Items[0].Cursor != appended.Cursor || !first.HasMore || !second.HasMore || third.HasMore || end.NextCursor != third.NextCursor {
		return fail("run.events.order")
	}
	first.Items[0].Bytes[0] = ' '
	first, err = store.ReadEvents(ctx, "run-alpha", "", 1)
	if err != nil || len(first.Items) != 1 || string(first.Items[0].Bytes) != `{"type":"opened"}` {
		return fail("run.events.detached")
	}
	_, err = store.ReadEvents(ctx, "run-beta", committed.Cursor, 1)
	var cursor *StorageCursorInvalid
	if !errors.As(err, &cursor) {
		return fail("run.cursor.resource-scope")
	}
	_, err = create().ReadEvents(ctx, "run-alpha", committed.Cursor, 1)
	if !errors.As(err, &cursor) {
		return fail("run.cursor.store-scope")
	}
	_, err = store.ReadEvents(ctx, "run-alpha", "unknown-cursor", 1)
	if !errors.As(err, &cursor) {
		return fail("run.cursor.invalid")
	}
	absent, err := store.Load(ctx, "run-beta")
	empty, readErr := store.ReadEvents(ctx, "run-beta", "", 1)
	if err != nil || readErr != nil || absent != nil || len(empty.Items) != 0 {
		return fail("run.read.missing-observational")
	}
	return nil
}

// CheckRunStoreFaultConformance requires a fixture that injects exactly one failure at its
// physical commit seam ("before" or "after" publication), then permits ordinary calls.
// A postcommit receipt proves persistence only, not authority or any external operation's result.
func CheckRunStoreFaultConformance(ctx context.Context, create func(phase string) RunStore) error {
	fail := func(property string) error { return &StorageConformanceFailure{Property: property} }
	for _, phase := range []string{"before", "after"} {
		store := create(phase)
		input := RunCommit{RunID: "fault-run", CommitID: "fault-commit", Checkpoint: []byte(`{"payload":"kept"}`), Events: [][]byte{[]byte(`{"type":"kept"}`)}}
		if _, err := store.Commit(ctx, input); err == nil {
			return fail("run.fault." + phase + ".injected")
		}
		head, err := store.Load(ctx, "fault-run")
		page, readErr := store.ReadEvents(ctx, "fault-run", "", 100)
		if err != nil || readErr != nil {
			return fail("run.fault." + phase + ".readable")
		}
		if phase == "before" && (head != nil || len(page.Items) != 0) {
			return fail("run.fault.before.atomic")
		}
		if phase == "after" && (head == nil || string(head.Checkpoint) != `{"payload":"kept"}` || len(page.Items) != 1 || head.Cursor != page.Items[0].Cursor) {
			return fail("run.fault.after.complete")
		}
		receipt, err := store.Commit(ctx, input)
		after, loadErr := store.Load(ctx, "fault-run")
		events, eventErr := store.ReadEvents(ctx, "fault-run", "", 100)
		if err != nil || loadErr != nil || eventErr != nil || after == nil || after.Revision != receipt.Revision || string(after.Checkpoint) != `{"payload":"kept"}` || len(events.Items) != 1 {
			return fail("run.fault." + phase + ".recovered")
		}
		if phase == "after" && receipt.Revision != head.Revision {
			return fail("run.fault.after.idempotent")
		}
	}
	return nil
}
