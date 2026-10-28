package runcontext

import (
	"bytes"
	"context"
	"errors"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"testing"
)

func historyCompletedStore(t *testing.T) *MemoryRunStore {
	t.Helper()
	ctx := context.Background()
	store := NewMemoryRunStore(MemoryRunStoreOptions{})
	source, err := NewStoredChatScope(store, "chat", NewChatContext("shared"), nil)
	if err != nil {
		t.Fatal(err)
	}
	session, err := ContextualSession(ctx, historyStoredSession(store, source, "r", false))
	if err != nil {
		t.Fatal(err)
	}
	boundary := NewBoundary(session)
	historyAssertNoFailure(t, boundary.Open())
	events, _ := boundary.Event(contract.RunEvent{Message: &contract.RunEventMessage{Type: "message", Text: "Done"}})
	historyAssertNoFailure(t, events)
	events, _ = boundary.Event(contract.RunEvent{End: &contract.RunEventEnd{Type: "end"}})
	historyAssertNoFailure(t, events)
	boundary.Close()
	return store
}
func TestHistoryViewActualWriterDetachedEvidence(t *testing.T) {
	ctx := context.Background()
	store := historyCompletedStore(t)
	head, _ := store.ReadHistoryHead(ctx, "r")
	view, err := ReadRetainedRunHistory(ctx, store, "r")
	if err != nil {
		t.Fatal(err)
	}
	if historyInputText(view.Head()) != historyInputText(head) || len(view.Commits()) != head.CommitCount {
		t.Fatal("incomplete detached history")
	}
	empty := false
	events := 0
	for _, row := range view.Commits() {
		empty = empty || row.EventCount == 0
		events += row.EventCount
	}
	if !empty || events != head.EventCount {
		t.Fatal("checkpoint-only commit lost")
	}
	raw, err := view.ReadObject(*head.Checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := store.Load(ctx, "r")
	if !bytes.Equal(raw, stored.Checkpoint) {
		t.Fatal("checkpoint bytes changed")
	}
	raw[0] = 0
	again, _ := view.ReadObject(*head.Checkpoint)
	if again[0] == 0 {
		t.Fatal("object alias exposed")
	}
	rows := view.Commits()
	rows[0].CommitID = "changed"
	if view.Commits()[0].CommitID == "changed" {
		t.Fatal("row alias exposed")
	}
}

type historyViewProbe struct {
	HistoryRunStore
	page   func(RunHistoryPage) RunHistoryPage
	object func(HistoryObjectPage) HistoryObjectPage
	after  func()
}

func (p *historyViewProbe) ReadHistoryPage(ctx context.Context, id, rev string, after, limit int) (RunHistoryPage, error) {
	page, err := p.HistoryRunStore.ReadHistoryPage(ctx, id, rev, after, limit)
	if err == nil && p.after != nil {
		f := p.after
		p.after = nil
		f()
	}
	if err == nil && p.page != nil {
		page = p.page(page)
	}
	return page, err
}
func (p *historyViewProbe) ReadHistoryObject(ctx context.Context, r HistoryResource, rev, digest string, offset, limit int) (HistoryObjectPage, error) {
	page, err := p.HistoryRunStore.ReadHistoryObject(ctx, r, rev, digest, offset, limit)
	if err == nil && p.object != nil {
		page = p.object(page)
	}
	return page, err
}
func TestHistoryViewMetadataOnlyHeadRace(t *testing.T) {
	ctx := context.Background()
	store := historyCompletedStore(t)
	execution, _ := store.Load(ctx, "r")
	probe := &historyViewProbe{HistoryRunStore: store}
	probe.after = func() {
		head, _ := store.ReadHistoryHead(ctx, "r")
		feedback := historyFeedback()
		_, err := store.CommitHistoryMetadata(ctx, HistoryMetadataCommit{RunID: "r", ExpectedHistoryRevision: head.Revision, CommitID: "feedback-race", Record: HistoryMetadata{Version: 1, Kind: "feedback", Feedback: &feedback}})
		if err != nil {
			t.Fatal(err)
		}
	}
	var changed *HistoryChanged
	if _, err := ReadRetainedRunHistory(ctx, probe, "r"); !errors.As(err, &changed) {
		t.Fatal("metadata-only head race accepted", err)
	}
	now, _ := store.Load(ctx, "r")
	if now.Revision != execution.Revision {
		t.Fatal("metadata changed execution revision")
	}
	view, err := ReadRetainedRunHistory(ctx, store, "r")
	if err != nil {
		t.Fatal(err)
	}
	row := view.Metadata()[0]
	if row.ExecutionRevision != execution.Revision || row.CommitSequence != view.Head().CommitCount {
		t.Fatal("metadata prefix not store-bound")
	}
}
func TestHistoryViewInvalidPortEvidence(t *testing.T) {
	mutations := []func(RunHistoryPage) RunHistoryPage{func(p RunHistoryPage) RunHistoryPage { p.Items = nil; return p }, func(p RunHistoryPage) RunHistoryPage { p.NextSequence++; return p }, func(p RunHistoryPage) RunHistoryPage { p.Total++; return p }, func(p RunHistoryPage) RunHistoryPage {
		for a, b := 0, len(p.Items)-1; a < b; a, b = a+1, b-1 {
			p.Items[a], p.Items[b] = p.Items[b], p.Items[a]
		}
		return p
	}}
	for _, mutate := range mutations {
		store := historyCompletedStore(t)
		var invalid *HistoryInputInvalid
		if _, err := ReadRetainedRunHistory(context.Background(), &historyViewProbe{HistoryRunStore: store, page: mutate}, "r"); !errors.As(err, &invalid) {
			t.Fatal("corrupt/truncated page accepted", err)
		}
	}
	store := historyCompletedStore(t)
	probe := &historyViewProbe{HistoryRunStore: store, object: func(p HistoryObjectPage) HistoryObjectPage { p.Value[0] ^= 1; return p }}
	var invalid *HistoryInputInvalid
	if _, err := ReadRetainedRunHistory(context.Background(), probe, "r"); !errors.As(err, &invalid) {
		t.Fatal("altered source bytes accepted", err)
	}
}
