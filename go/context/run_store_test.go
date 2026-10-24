package runcontext

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

func storageOpening() RunCommit {
	return RunCommit{RunID: "opaque-run", CommitID: "opening", Checkpoint: []byte(`{"id":"opaque-run"}`), Events: [][]byte{[]byte(`{"type":"opened"}`)}}
}
func TestMemoryRunStoreConformance(t *testing.T) {
	if err := CheckRunStoreConformance(context.Background(), func() RunStore { return NewMemoryRunStore(MemoryRunStoreOptions{}) }); err != nil {
		t.Fatal(err)
	}
}
func TestMemoryRunStoreFaultAtomicityAndLostAcknowledgement(t *testing.T) {
	ctx := context.Background()
	injected := errors.New("injected storage fault")
	before := true
	store := NewMemoryRunStore(MemoryRunStoreOptions{BeforeCommit: func() error {
		if before {
			return injected
		}
		return nil
	}})
	if _, err := store.Commit(ctx, storageOpening()); !errors.Is(err, injected) {
		t.Fatalf("precommit fault: %v", err)
	}
	head, _ := store.Load(ctx, "opaque-run")
	page, _ := store.ReadEvents(ctx, "opaque-run", "", 1)
	if head != nil || len(page.Items) != 0 {
		t.Fatal("precommit fault published partial state")
	}
	before = false
	if _, err := store.Commit(ctx, storageOpening()); err != nil {
		t.Fatal(err)
	}
	lost := NewMemoryRunStore(MemoryRunStoreOptions{AfterCommit: func() error { return injected }})
	if _, err := lost.Commit(ctx, storageOpening()); !errors.Is(err, injected) {
		t.Fatalf("lost acknowledgement: %v", err)
	}
	head, _ = lost.Load(ctx, "opaque-run")
	if head == nil {
		t.Fatal("lost acknowledgement forgot applied transaction")
	}
	retry, err := lost.Commit(ctx, storageOpening())
	if err != nil || retry != head.RunReceipt {
		t.Fatalf("receipt recovery: %v %v", retry, err)
	}
	page, _ = lost.ReadEvents(ctx, "opaque-run", "", 100)
	if len(page.Items) != 1 {
		t.Fatal("receipt recovery duplicated event")
	}
}
func TestMemoryRunStoreBoundsAndRefusals(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryRunStore(MemoryRunStoreOptions{})
	receipt, err := store.Commit(ctx, storageOpening())
	if err != nil {
		t.Fatal(err)
	}
	rows := []RunCommit{storageOpening(), storageOpening(), storageOpening(), storageOpening(), storageOpening(), storageOpening()}
	rows[0].RunID = ""
	rows[1].CommitID = strings.Repeat("x", 257)
	rows[2].Checkpoint = []byte{255}
	rows[3].Checkpoint = bytes.Repeat([]byte{'x'}, StorageRecordBytes+1)
	rows[4].Checkpoint = nil
	rows[4].Events = nil
	rows[5].Checkpoint = bytes.Repeat([]byte{'x'}, StorageRecordBytes)
	rows[5].Events = [][]byte{bytes.Repeat([]byte{'x'}, StorageRecordBytes), []byte("x")}
	for i, row := range rows {
		_, err := store.Commit(ctx, row)
		var invalid *StorageInputError
		if !errors.As(err, &invalid) {
			t.Errorf("invalid row %d: %v", i, err)
		}
	}
	for _, limit := range []int{0, 101, -1} {
		_, err := store.ReadEvents(ctx, "opaque-run", "", limit)
		var invalid *StorageInputError
		if !errors.As(err, &invalid) {
			t.Errorf("invalid limit %d: %v", limit, err)
		}
	}
	for _, cursor := range []string{"missing", "\n"} {
		_, err := store.ReadEvents(ctx, "opaque-run", cursor, 1)
		var invalid *StorageCursorInvalid
		if !errors.As(err, &invalid) {
			t.Errorf("invalid cursor: %v", err)
		}
	}
	conflict := storageOpening()
	conflict.CommitID = "conflict"
	conflict.Checkpoint = []byte("secret payload")
	_, err = store.Commit(ctx, conflict)
	var refused *StorageConflict
	if !errors.As(err, &refused) || refused.ActualRevision != receipt.Revision || refused.Error() != "The run changed; no storage write was applied." {
		t.Fatalf("bounded conflict: %v", err)
	}
	head, _ := store.Load(ctx, "opaque-run")
	if head.Revision != receipt.Revision {
		t.Fatal("refusal changed state")
	}
}
func TestMemoryRunStorePageByteLimitAndContextCancellation(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryRunStore(MemoryRunStoreOptions{})
	input := storageOpening()
	input.Checkpoint = nil
	big := bytes.Repeat([]byte{'x'}, StorageRecordBytes)
	input.Events = [][]byte{big, big}
	first, err := store.Commit(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	input.ExpectedRevision = first.Revision
	input.CommitID = "extra"
	input.Events = [][]byte{[]byte("tail")}
	if _, err = store.Commit(ctx, input); err != nil {
		t.Fatal(err)
	}
	page, err := store.ReadEvents(ctx, "opaque-run", "", 100)
	if err != nil || len(page.Items) != 2 || len(page.Items[0].Bytes)+len(page.Items[1].Bytes) != StorageTransactionBytes || !page.HasMore {
		t.Fatalf("page bound: %v %v", page, err)
	}
	tail, err := store.ReadEvents(ctx, "opaque-run", page.NextCursor, 100)
	if err != nil || len(tail.Items) != 1 || string(tail.Items[0].Bytes) != "tail" {
		t.Fatal("cursor lost next event")
	}
	cancelled, stop := context.WithCancel(ctx)
	stop()
	if _, err = store.Commit(cancelled, storageOpening()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled commit: %v", err)
	}
	if _, err = store.Load(cancelled, "opaque-run"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled read: %v", err)
	}
}
func TestCheckpointCallbackRunStoreExplicitLimitsAndAcknowledgement(t *testing.T) {
	ctx := context.Background()
	entered, release := make(chan struct{}), make(chan struct{})
	var seen []byte
	store := NewCheckpointCallbackRunStore(func(_ context.Context, value []byte) error {
		seen = bytes.Clone(value)
		close(entered)
		<-release
		return nil
	}, func(raw []byte) ([]byte, error) { return raw, nil })
	if store.Durability() != RunStoreSingleWriterCallback {
		t.Fatal("callback durability was overstated")
	}
	input := storageOpening()
	var receipt RunReceipt
	var commitErr error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); receipt, commitErr = store.Commit(ctx, input) }()
	<-entered
	// The callback receives a detached copy. Publication is after this acknowledgement.
	seen[0] = ' '
	close(release)
	wg.Wait()
	if commitErr != nil || receipt.Revision == "" {
		t.Fatalf("callback commit: %v", commitErr)
	}
	head, _ := store.Load(ctx, "opaque-run")
	if string(head.Checkpoint) != `{"id":"opaque-run"}` {
		t.Fatal("callback mutated stored input")
	}
	err := CheckRunStoreConformance(ctx, func() RunStore { return store })
	var failed *StorageConformanceFailure
	if !errors.As(err, &failed) || failed.Property != "run.durability.declared" {
		t.Fatalf("callback certified as durable: %v", err)
	}
}
func TestCheckpointCallbackFailurePublishesNothing(t *testing.T) {
	ctx := context.Background()
	injected := errors.New("save failed")
	store := NewCheckpointCallbackRunStore(func(context.Context, []byte) error { return injected }, func(raw []byte) ([]byte, error) { return raw, nil })
	if _, err := store.Commit(ctx, storageOpening()); !errors.Is(err, injected) {
		t.Fatal(err)
	}
	head, _ := store.Load(ctx, "opaque-run")
	if head != nil {
		t.Fatal("callback failure published local state")
	}
}
func TestMemoryRunStoreDifferentScopesDoNotShareCursorOrRevision(t *testing.T) {
	ctx := context.Background()
	a, b := NewMemoryRunStore(MemoryRunStoreOptions{}), NewMemoryRunStore(MemoryRunStoreOptions{})
	left, err := a.Commit(ctx, storageOpening())
	if err != nil {
		t.Fatal(err)
	}
	right, err := b.Commit(ctx, storageOpening())
	if err != nil {
		t.Fatal(err)
	}
	if left.Revision == right.Revision {
		t.Fatal("distinct scope reused revision")
	}
	_, err = b.ReadEvents(ctx, "opaque-run", left.Cursor, 1)
	var invalid *StorageCursorInvalid
	if !errors.As(err, &invalid) {
		t.Fatalf("cross-scope cursor: %v", err)
	}
}

func TestMemoryRunStorePhysicalFaultConformance(t *testing.T) {
	err := CheckRunStoreFaultConformance(context.Background(), func(phase string) RunStore {
		armed := true
		fault := func() error {
			if armed {
				armed = false
				return errors.New("injected physical fault")
			}
			return nil
		}
		options := MemoryRunStoreOptions{}
		if phase == "before" {
			options.BeforeCommit = fault
		} else {
			options.AfterCommit = fault
		}
		return NewMemoryRunStore(options)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRunStoreRechecksCancellationBeforeAcknowledgement(t *testing.T) {
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	saved := false
	store := NewMemoryRunStore(MemoryRunStoreOptions{BeforeCommit: func() error { stop(); return nil }})
	store.acknowledge = func(context.Context, []byte) error { saved = true; return nil }
	if _, err := store.Commit(ctx, storageOpening()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled acknowledgement: %v", err)
	}
	head, _ := store.Load(context.Background(), "opaque-run")
	if saved || head != nil {
		t.Fatal("cancelled precommit reached save or published state")
	}
}
