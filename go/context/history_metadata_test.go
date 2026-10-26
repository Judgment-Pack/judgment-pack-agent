package runcontext

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

func historyFeedback() RunFeedback {
	empty := ""
	return RunFeedback{Version: 1, ID: "f", RunID: "r", Sequence: 1, Target: FeedbackTarget{Kind: "run"}, Rating: "bad", Remark: &empty, Actor: "host-user", RecordedAt: "2026-10-25T01:02:03.000Z"}
}
func TestHistoryMetadataAtomicNoExecutionMutation(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryRunStore(MemoryRunStoreOptions{})
	_, err := store.Commit(ctx, RunCommit{RunID: "r", CommitID: "one", Checkpoint: []byte(`{"value":false}`), Events: [][]byte{[]byte(`{"version":1,"scope":{"execution":"r","role":"author"},"event":{"version":1,"type":"message","text":"Done"}}`)}})
	if err != nil {
		t.Fatal(err)
	}
	head, _ := store.ReadHistoryHead(ctx, "r")
	before, _ := store.Load(ctx, "r")
	events, _ := store.ReadEvents(ctx, "r", "", 100)
	feedback := historyFeedback()
	input := HistoryMetadataCommit{RunID: "r", ExpectedHistoryRevision: head.Revision, CommitID: "feedback-one", Record: HistoryMetadata{Version: 1, Kind: "feedback", Feedback: &feedback}}
	written, err := store.CommitHistoryMetadata(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := store.Load(ctx, "r")
	againEvents, _ := store.ReadEvents(ctx, "r", "", 100)
	if written.ExecutionRevision != before.Revision || written.Revision == head.Revision || written.CommitCount != head.CommitCount || written.EventCount != head.EventCount || written.Root != head.Root || after.Revision != before.Revision || after.Cursor != before.Cursor || !bytes.Equal(after.Checkpoint, before.Checkpoint) || historyInputText(events) != historyInputText(againEvents) {
		t.Fatal("metadata mutated execution")
	}
	same, err := store.CommitHistoryMetadata(ctx, input)
	if err != nil || historyInputText(same) != historyInputText(written) {
		t.Fatal("metadata replay", err)
	}
	old, err := store.ReadHistoryMetadataPage(ctx, "r", head.Revision, 0, 100)
	if err != nil || len(old.Items) != 0 || old.Total != 0 {
		t.Fatal("pinned old metadata grew", err)
	}
	rows, err := store.ReadHistoryMetadataPage(ctx, "r", written.Revision, 0, 100)
	if err != nil || rows.Total != 1 || rows.Items[0].Sequence != 1 || rows.Items[0].PreviousDigest != nil || historyInputText(rows.Items[0].Record) != historyInputText(input.Record) {
		t.Fatal("metadata row", err)
	}
}
func TestHistoryMetadataCASAndFeedbackChain(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryRunStore(MemoryRunStoreOptions{})
	store.Commit(ctx, RunCommit{RunID: "r", CommitID: "one", Checkpoint: []byte(`{"test":1}`)})
	head, _ := store.ReadHistoryHead(ctx, "r")
	first := historyFeedback()
	input := HistoryMetadataCommit{RunID: "r", ExpectedHistoryRevision: head.Revision, CommitID: "feedback-one", Record: HistoryMetadata{Version: 1, Kind: "feedback", Feedback: &first}}
	saved, err := store.CommitHistoryMetadata(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	stale := input
	stale.CommitID = "competing"
	var conflict *StorageConflict
	if _, err = store.CommitHistoryMetadata(ctx, stale); !errors.As(err, &conflict) {
		t.Fatal("stale composite CAS accepted", err)
	}
	independent := input
	independent.CommitID = "metadata-only-stale-head"
	independent.ExpectedMetadataRevision = &saved.Metadata.Revision
	other := historyFeedback()
	other.ID = "other"
	independent.Record.Feedback = &other
	if _, err = store.CommitHistoryMetadata(ctx, independent); !errors.As(err, &conflict) {
		t.Fatal("metadata-only stale composite head accepted", err)
	}
	stale.ExpectedHistoryRevision = saved.Revision
	if _, err = store.CommitHistoryMetadata(ctx, stale); !errors.As(err, &conflict) {
		t.Fatal("stale metadata CAS accepted", err)
	}
	second := historyFeedback()
	raw, _ := historyJSONBytes(first)
	previous := HistoryBytesDigest(raw)
	second.Sequence = 2
	second.Previous = &previous
	second.Rating = "good"
	next := HistoryMetadataCommit{RunID: "r", ExpectedHistoryRevision: saved.Revision, ExpectedMetadataRevision: &saved.Metadata.Revision, CommitID: "feedback-two", Record: HistoryMetadata{Version: 1, Kind: "feedback", Feedback: &second}}
	newHead, err := store.CommitHistoryMetadata(ctx, next)
	if err != nil {
		t.Fatal(err)
	}
	rows, _ := store.ReadHistoryMetadataPage(ctx, "r", newHead.Revision, 0, 100)
	if len(rows.Items) != 2 || rows.Items[0].Record.Feedback.Rating != "bad" || rows.Items[1].Record.Feedback.Rating != "good" {
		t.Fatal("feedback history dropped")
	}
	wrong := second
	wrong.Sequence = 3
	sumRaw, _ := historyJSONBytes(second)
	sum := HistoryBytesDigest(sumRaw)
	wrong.Previous = &sum
	wrong.Target = FeedbackTarget{Kind: "assistant-message", EventSequence: 1, EventDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	bad := next
	bad.ExpectedHistoryRevision = newHead.Revision
	bad.ExpectedMetadataRevision = &newHead.Metadata.Revision
	bad.CommitID = "wrong-target"
	bad.Record.Feedback = &wrong
	if _, err = store.CommitHistoryMetadata(ctx, bad); err == nil {
		t.Fatal("feedback target changed")
	}
	current, _ := store.ReadHistoryHead(ctx, "r")
	if current.Revision != newHead.Revision {
		t.Fatal("partial metadata publication")
	}
}
func TestHistoryMetadataExactAssistantEventTarget(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryRunStore(MemoryRunStoreOptions{})
	store.Commit(ctx, RunCommit{RunID: "r", CommitID: "one", Checkpoint: []byte(`{"test":1}`), Events: [][]byte{[]byte(`{"version":1,"scope":{"execution":"r","role":"author"},"event":{"version":1,"type":"message","text":"Done"}}`), []byte(`{"version":1,"scope":{"execution":"r","role":"author"},"event":{"version":1,"type":"end"}}`)}})
	head, _ := store.ReadHistoryHead(ctx, "r")
	page, _ := store.ReadHistoryPage(ctx, "r", head.Revision, 0, 100)
	row := historyFeedback()
	row.Target = FeedbackTarget{Kind: "assistant-message", EventSequence: 1, EventDigest: page.Items[0].Events[0].Digest}
	input := HistoryMetadataCommit{RunID: "r", ExpectedHistoryRevision: head.Revision, CommitID: "message-feedback", Record: HistoryMetadata{Version: 1, Kind: "feedback", Feedback: &row}}
	saved, err := store.CommitHistoryMetadata(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	row.ID = "other"
	row.Target = FeedbackTarget{Kind: "assistant-message", EventSequence: 2, EventDigest: page.Items[0].Events[1].Digest}
	input.Record.Feedback = &row
	input.ExpectedHistoryRevision = saved.Revision
	input.ExpectedMetadataRevision = &saved.Metadata.Revision
	input.CommitID = "wrong-event-kind"
	if _, err = store.CommitHistoryMetadata(ctx, input); err == nil {
		t.Fatal("end event treated as assistant message")
	}
}
func TestHistoryMetadataClosedReaders(t *testing.T) {
	row := historyFeedback()
	raw, _ := historyJSONBytes(row)
	if parsed, err := ReadRunFeedback(raw); err != nil || historyInputText(parsed) != historyInputText(row) {
		t.Fatal("feedback read", err)
	}
	value, _ := ParseJSON(raw)
	for _, bad := range []*Value{value.Clone().Set("unknown", Bool(true)), value.Clone().Set("version", Number(2)), value.Clone().Set("recordedAt", String("2026-02-30T01:02:03.000Z")), value.Clone().Set("sequence", Number(0))} {
		if _, err := ReadRunFeedback([]byte(CanonicalJSON(bad))); err == nil {
			t.Fatal("invalid feedback accepted")
		}
	}
	if _, err := readHistoryJSON([]byte(`{"n":9007199254740993}`), 100, true, true); err == nil {
		t.Fatal("rounded numeric spelling accepted")
	}
	if _, err := readHistoryJSON([]byte(`{"n":0.1}`), 100, true, true); err != nil {
		t.Fatal("finite source fraction rejected", err)
	}
}
