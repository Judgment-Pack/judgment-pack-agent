package runcontext

import (
	"bytes"
	"context"
	"errors"
	"time"
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

// CheckRunStoreLeaseConformance (#223) is for a host store kind's lease port (desk #609). The factory
// builds a fresh store whose authoritative lease clock is the now it is given, so the probe can move
// time; every other property is the store's own. The TypeScript twin is runStoreLeaseConformance.
func CheckRunStoreLeaseConformance(ctx context.Context, create func(now func() time.Time) LeaseRunStore) error {
	fail := func(property string) error { return &StorageConformanceFailure{Property: property} }
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store := create(func() time.Time { return at })
	const run = "lease-run"
	if store.LeaseVersion() != LeaseRecordVersion || (store.LeaseLifetime() != LeaseLifetimeDurable && store.LeaseLifetime() != LeaseLifetimeProcess) || store.Durability() == RunStoreSingleWriterCallback {
		return fail("lease.declared")
	}
	refuses := func(err error, reason string) bool {
		var refused *LeaseRefused
		return errors.As(err, &refused) && refused.Reason == reason
	}
	commit := func(commitID, expected string, fence LeaseFence) (RunReceipt, error) {
		return store.CommitFenced(ctx, RunCommit{RunID: run, ExpectedRevision: expected, CommitID: commitID, Checkpoint: []byte(`{"commit":"` + commitID + `"}`)}, fence)
	}
	opened, err := store.Commit(ctx, RunCommit{RunID: run, CommitID: "lease-open", Checkpoint: []byte(`{"commit":"lease-open"}`)})
	if err != nil {
		return fail("lease.open")
	}
	if _, err = store.AcquireLease(ctx, LeaseAcquire{Resource: run, Owner: "worker-a", DurationMs: 1000}); !refuses(err, "revision-changed") {
		return fail("lease.acquire.revision-checked")
	}
	first, err := store.AcquireLease(ctx, LeaseAcquire{Resource: run, Owner: "worker-a", ExpectedRevision: opened.Revision, DurationMs: 1000})
	if err != nil || first.Version != 1 || first.Resource != run || first.Owner != "worker-a" || first.Epoch != "1" || first.State != "held" || first.Expiry != "2026-01-01T00:00:01.000Z" {
		return fail("lease.acquire.first")
	}
	if _, err = store.AcquireLease(ctx, LeaseAcquire{Resource: run, Owner: "worker-b", ExpectedRevision: opened.Revision, DurationMs: 1000}); !refuses(err, "held") {
		return fail("lease.acquire.held")
	}
	a := FenceOf(first)
	current, err := commit("lease-a-1", opened.Revision, a)
	if head, _ := store.Load(ctx, run); err != nil || current.CommitID != "lease-a-1" || head == nil || head.Revision != current.Revision {
		return fail("lease.commit.current")
	}
	forged := a
	forged.Owner = "worker-b"
	if _, err = commit("lease-forged", current.Revision, forged); !refuses(err, "other-owner") {
		return fail("lease.commit.other-owner")
	}
	forged = a
	forged.Epoch = "2"
	if _, err = commit("lease-forged", current.Revision, forged); !refuses(err, "stale-epoch") {
		return fail("lease.commit.future-epoch")
	}
	if _, err = store.CommitFenced(ctx, RunCommit{RunID: "lease-other", CommitID: "lease-other", Checkpoint: []byte(`{}`)}, a); !refuses(err, "resource-mismatch") {
		return fail("lease.commit.resource")
	}
	forged = a
	forged.Epoch = "01"
	if err = store.CheckFence(ctx, forged); !refuses(err, "invalid") {
		return fail("lease.fence.exact-epoch")
	}
	at = at.Add(500 * time.Millisecond)
	renewed, err := store.RenewLease(ctx, a, 1000)
	if err != nil || renewed.Epoch != "1" || renewed.Owner != "worker-a" || renewed.Expiry != "2026-01-01T00:00:01.500Z" {
		return fail("lease.renew.store-clock")
	}
	forged = a
	forged.Owner = "worker-b"
	if _, err = store.RenewLease(ctx, forged, 1000); !refuses(err, "other-owner") {
		return fail("lease.renew.other-owner")
	}
	at = time.Date(2026, 1, 1, 0, 0, 1, 500_000_000, time.UTC)
	if err = store.CheckFence(ctx, a); !refuses(err, "expired") {
		return fail("lease.expiry.equal-time")
	}
	if _, err = store.RenewLease(ctx, a, 1000); !refuses(err, "expired") {
		return fail("lease.renew.no-revival")
	}
	if _, err = commit("lease-a-late", current.Revision, a); !refuses(err, "expired") {
		return fail("lease.commit.expired")
	}
	if head, _ := store.Load(ctx, run); head == nil || head.Revision != current.Revision {
		return fail("lease.commit.expired-unapplied")
	}
	second, err := store.AcquireLease(ctx, LeaseAcquire{Resource: run, Owner: "worker-b", ExpectedRevision: current.Revision, DurationMs: 1000})
	if err != nil || second.Epoch != "2" || second.Owner != "worker-b" {
		return fail("lease.takeover.greater-epoch")
	}
	if _, err = commit("lease-a-stale", current.Revision, a); !refuses(err, "stale-epoch") {
		return fail("lease.commit.stale-epoch")
	}
	b := FenceOf(second)
	taken, err := commit("lease-b-1", current.Revision, b)
	if err != nil {
		return fail("lease.commit.current")
	}
	released, err := store.ReleaseLease(ctx, b)
	if err != nil || released.State != "released" || released.Epoch != "2" {
		return fail("lease.release.retains-epoch")
	}
	if _, err = commit("lease-b-2", taken.Revision, b); !refuses(err, "released") {
		return fail("lease.release.fence-ends")
	}
	third, err := store.AcquireLease(ctx, LeaseAcquire{Resource: run, Owner: "worker-a", ExpectedRevision: taken.Revision, DurationMs: 1000})
	read, readErr := store.ReadLease(ctx, run)
	if err != nil || readErr != nil || third.Epoch != "3" || read == nil || read.Epoch != "3" {
		return fail("lease.release.next-greater")
	}
	if missing, err := store.ReadLease(ctx, "never-leased"); err != nil || missing != nil {
		return fail("lease.read.missing")
	}
	results := make(chan error, 2)
	for _, owner := range []string{"worker-x", "worker-y"} {
		go func() {
			_, err := store.AcquireLease(ctx, LeaseAcquire{Resource: "lease-race", Owner: owner, DurationMs: 1000})
			results <- err
		}()
	}
	won, held := 0, 0
	for range 2 {
		if err := <-results; err == nil {
			won++
		} else if refuses(err, "held") {
			held++
		}
	}
	if won != 1 || held != 1 {
		return fail("lease.acquire.atomic")
	}
	return nil
}
