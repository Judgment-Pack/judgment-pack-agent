package runcontext

import (
	"bytes"
	"context"
	"errors"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/wire"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestHistoryFoundationSnapshotAliasesAndBinding(t *testing.T) {
	c := NewChatContext("chat")
	visible := map[string]bool{"chat:reference": true}
	thread := &TaskThread{Profile: "research", Title: "title", Exchanges: []*Value{Object().Set("objective", String("ask")).Set("result", String("answer"))}, Visible: visible}
	if err := c.KeepTask([]string{"one", "two"}, thread); err != nil {
		t.Fatal(err)
	}
	turn, err := c.ChildScope(visible).OpenTurn()
	if err != nil {
		t.Fatal(err)
	}
	row := &evictedResult{tool: "lookup", arguments: "digest", bytes: 10}
	c.evicted = []*evictedResult{row}
	turn.offered = []*evictedResult{row}
	c.reported([]*evictedResult{row}) // already reported, still offered
	raw, err := c.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := RestoreChatContext(raw)
	if err != nil {
		t.Fatal(err)
	}
	again, err := restored.Snapshot()
	if err != nil || !bytes.Equal(raw, again) {
		t.Fatalf("roundtrip %v\n%s\n%s", err, raw, again)
	}
	if restored.Task("one") != restored.Task("two") {
		t.Fatal("task alias lost")
	}
	restored.Task("one").Visible["chat:later"] = true
	if !restored.Task("two").Visible["chat:later"] {
		t.Fatal("visibility alias lost")
	}
	scope, err := restored.ResumeTurn(turn.number)
	if err != nil {
		t.Fatal(err)
	}
	attached, err := scope.OpenTurn()
	if err != nil || !attached.visible["chat:later"] {
		t.Fatal("turn alias lost", err)
	}
	if len(attached.offered) != 1 {
		t.Fatal("already reported offer lost")
	}
	if _, err = scope.OpenTurn(); err == nil {
		t.Fatal("duplicate attachment accepted")
	}
	if restored.TurnCount() != c.TurnCount() {
		t.Fatal("turn incremented")
	}
	bad := bytes.Replace(raw, []byte(`"version":2`), []byte(`"version":3`), 1)
	var unsupported *UnsupportedContextSnapshotVersion
	if _, err = RestoreChatContext(bad); !errors.As(err, &unsupported) {
		t.Fatal("future accepted", err)
	}
}
func TestHistoryFoundationAtomicPortAndPinnedRows(t *testing.T) {
	ctx := context.Background()
	clockCalls := 0
	m := NewMemoryRunStore(MemoryRunStoreOptions{Now: func() time.Time { clockCalls++; return time.Date(2026, 10, 25, 1, 2, 3, 0, time.UTC) }})
	c := NewChatContext("chat")
	turn, _ := c.OpenTurn()
	raw, _ := c.Snapshot()
	manifest, objects, err := ChunkHistoryValue(raw)
	if err != nil {
		t.Fatal(err)
	}
	objects = append(objects, manifest)
	if err = m.StageHistoryObjects(ctx, HistoryResource{Kind: "chat", ID: "c"}, objects); err != nil {
		t.Fatal(err)
	}
	n := turn.number
	input := RunHistoryCommit{Run: RunCommit{RunID: "r", CommitID: "a", Checkpoint: []byte(`{"test":1}`), Events: [][]byte{[]byte(`{"event":1}`)}}, Observations: []HistoryObjectRef{}, Genesis: true, Context: &ContextSnapshotUpdate{ChatID: "c", Manifest: manifest.HistoryObjectRef, Turn: &n}}
	first, err := m.CommitHistory(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	same, err := m.CommitHistory(ctx, input)
	if err != nil || same.Revision != first.Revision || clockCalls != 1 {
		t.Fatal("idempotence", err, clockCalls)
	}
	next, err := m.Commit(ctx, RunCommit{RunID: "r", ExpectedRevision: first.Revision, CommitID: "b", Checkpoint: []byte(`{"test":2}`)})
	if err != nil {
		t.Fatal(err)
	}
	page, err := m.ReadHistoryPage(ctx, "r", first.History.Revision, 0, 100)
	if err != nil || len(page.Items) != 1 || page.Total != 1 || page.Items[0].EventStart != 1 || page.Items[0].RecordedAt != "2026-10-25T01:02:03.000Z" {
		t.Fatal("pinned page", page, err)
	}
	head, _ := m.ReadHistoryHead(ctx, "r")
	page, err = m.ReadHistoryPage(ctx, "r", head.Revision, 0, 100)
	if err != nil || len(page.Items) != 2 || page.Items[1].EventCount != 0 || page.Items[1].EventStart != 2 || page.Items[1].PreviousDigest == nil || *page.Items[1].PreviousDigest != page.Items[0].Digest {
		t.Fatal("checkpoint only journal", page, err)
	}
	// A stale chat comparison refuses the entire run update, including checkpoint/history.
	input.Run.ExpectedRevision = next.Revision
	input.Run.CommitID = "conflict"
	input.Genesis = false
	if _, err = m.CommitHistory(ctx, input); err == nil {
		t.Fatal("stale chat accepted")
	}
	loaded, _ := m.Load(ctx, "r")
	after, _ := m.ReadHistoryHead(ctx, "r")
	if loaded.Revision != next.Revision || after.Revision != head.Revision {
		t.Fatal("partial publication")
	}
	object, err := m.ReadHistoryObject(ctx, HistoryResource{Kind: "chat", ID: "c"}, first.History.Context.Revision, manifest.Digest, 0, HistoryObjectBytes)
	if err != nil || !bytes.Equal(object.Value, manifest.Value) {
		t.Fatal("pinned object", err)
	}
}

func TestHistoryFoundationSharedSnapshots(t *testing.T) {
	vector := readJSON(t, "contract/vectors/chat/context-snapshot.json")
	for _, item := range vector.Get("items").Items() {
		// The items are version 1; the same state at the version written now is what round-trips (#271).
		raw := []byte(CanonicalJSON(item.Clone().Set("version", Number(chatSnapshotVersion))))
		chat, err := RestoreChatContext(raw)
		if err != nil {
			t.Fatal(err)
		}
		again, err := chat.Snapshot()
		if err != nil || !bytes.Equal(raw, again) {
			t.Fatalf("shared byte rule %v\n%s\n%s", err, raw, again)
		}
		entry := chat.entries.get("shared:entry")
		if entry == nil || entry.Value.Text() != item.Get("entries").Items()[0].Get("valueJSON").Str() || entry.Retained().Revision == nil || *entry.Retained().Revision != "" {
			t.Fatal("opaque order/empty revision lost")
		}
		retained := entry.Retained()
		normal, err := EntryOf(retained)
		if err != nil || normal.Retained().Revision == nil {
			t.Fatal("normal reader presence", err)
		}
		if chat.Task("one") != chat.Task("two") {
			t.Fatal("shared thread alias lost")
		}
		chat.Task("one").Visible["shared:later"] = true
		scope, _ := chat.ResumeTurn(2)
		turn, err := scope.OpenTurn()
		if err != nil || !turn.visible["shared:later"] {
			t.Fatal("shared visibility alias lost", err)
		}
		if len(turn.offered) != 1 {
			t.Fatal("offered eviction lost")
		}
		if len(chat.evicted) > 0 {
			chat.evicted[0].bytes = 99
			if turn.offered[0].bytes != 99 {
				t.Fatal("pending/offered eviction alias lost")
			}
		} else {
			other, _ := chat.ResumeTurn(1)
			a, _ := other.OpenTurn()
			if a.offered[0] != turn.offered[0] {
				t.Fatal("already reported alias lost")
			}
		}
	}
}
func TestHistoryFoundationGoNilThreadDomain(t *testing.T) {
	vector := readJSON(t, "contract/vectors/chat/context-snapshot.json")
	for _, item := range vector.Get("goOnly").Items() {
		raw := []byte(CanonicalJSON(item.Clone().Set("version", Number(chatSnapshotVersion))))
		chat, err := RestoreChatContext(raw)
		if err != nil {
			t.Fatal(err)
		}
		if (chat.Task("one").Visible == nil) != !item.Get("threads").Items()[0].Has("visible") || chat.Task("one") != chat.Task("two") {
			t.Fatal("Go domain/alias changed")
		}
		again, err := chat.Snapshot()
		if err != nil || !bytes.Equal(raw, again) {
			t.Fatal("nil thread roundtrip", err)
		}
	}
}
func TestHistoryFoundationStoredScopeLatestAndExactTurn(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryRunStore(MemoryRunStoreOptions{})
	chat := NewChatContext("shared")
	a, _ := NewStoredChatScope(store, "c", chat, nil)
	a.OpenTurn()
	update, _ := a.prepare(ctx)
	first, err := store.CommitHistory(ctx, RunHistoryCommit{Run: RunCommit{RunID: "a", CommitID: "a", Checkpoint: []byte(`{"test":1}`)}, Context: &update})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewStoredChatScope(store, "c", chat, &first.History.Context.ContextSnapshotHead)
	b.OpenTurn()
	update, _ = b.prepare(ctx)
	second, err := store.CommitHistory(ctx, RunHistoryCommit{Run: RunCommit{RunID: "b", CommitID: "b", Checkpoint: []byte(`{"test":2}`)}, Context: &update})
	if err != nil {
		t.Fatal(err)
	}
	restored, err := RestoreStoredChatScope(ctx, store, *first.History.Context)
	if err != nil || !restored.matches(first.History.Context) || restored.Context().TurnCount() != 2 {
		t.Fatal("latest/original binding", err)
	}
	turn, err := restored.OpenTurn()
	if err != nil || turn.number != 1 {
		t.Fatal("exact turn", err)
	}
	update, err = restored.prepare(ctx)
	if err != nil || update.ExpectedRevision == nil || *update.ExpectedRevision != second.History.Context.Revision {
		t.Fatal("latest CAS", err)
	}
	if _, err = store.CommitHistory(ctx, RunHistoryCommit{Run: RunCommit{RunID: "a", ExpectedRevision: first.Revision, CommitID: "resume-a", Checkpoint: []byte(`{"test":1}`)}, Context: &update}); err != nil {
		t.Fatal(err)
	}
}

func historyStoredSession(store HistoryRunStore, source *StoredChatScope, id string, resume bool) Session {
	session := modelSession()
	session.Prompt = "Work"
	session.Context = &Input{Chat: source, CurrentRequest: &CurrentRequest{Message: "Work", Input: "Work"}, RunStore: &DurableRunInput{Store: store, ID: id, Resume: resume, Context: source}}
	return session
}
func historyAssertNoFailure(t *testing.T, events []contract.RunEvent) {
	t.Helper()
	for _, event := range events {
		if event.Error != nil {
			t.Fatalf("unexpected failure %s", event.Error.Message)
		}
	}
}
func TestHistoryFoundationActualPauseResumeTerminalBoundary(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryRunStore(MemoryRunStoreOptions{})
	source, _ := NewStoredChatScope(store, "c", NewChatContext("shared"), nil)
	run, err := ContextualSession(ctx, historyStoredSession(store, source, "paused", false))
	if err != nil {
		t.Fatal(err)
	}
	boundary := NewBoundary(run)
	historyAssertNoFailure(t, boundary.Open())
	snapshot, err := run.Context.Execution.Snapshot("paused")
	if err != nil {
		t.Fatal(err)
	}
	events, _ := boundary.Event(contract.RunEvent{BudgetPaused: &contract.RunEventBudgetPaused{Type: "budget_paused", Checkpoint: snapshot}})
	historyAssertNoFailure(t, events)
	events, _ = boundary.Event(contract.RunEvent{End: &contract.RunEventEnd{Type: "end"}})
	historyAssertNoFailure(t, events)
	boundary.Close()
	actualRaw, _ := source.Context().Snapshot()
	actualState, _ := ReadChatContextSnapshot(actualRaw)
	if len(actualState.Open) != 1 {
		t.Fatal("actual paused logical turn dropped")
	}
	paused, _ := store.ReadHistoryHead(ctx, "paused")
	raw, err := ReadStoredChatSnapshot(ctx, store, paused.Context.ContextSnapshotHead)
	if err != nil {
		t.Fatal(err)
	}
	saved, _ := ReadChatContextSnapshot(raw)
	if paused.Context.Closed || len(saved.Open) != 1 || saved.Open[0].Number != 1 {
		t.Fatal("paused logical turn dropped")
	}
	// Real explicit host allowance transition, not an inferred automatic grant.
	head, _ := store.Load(ctx, "paused")
	value, _ := ParseJSON(head.Checkpoint)
	checkpoint, err := ReadExecution(value)
	if err != nil {
		t.Fatal(err)
	}
	granted, err := ExtendExecution(*checkpoint, false, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	grantRaw, _ := canonicalRaw(granted)
	if _, err = store.Commit(ctx, RunCommit{RunID: "paused", ExpectedRevision: head.Revision, CommitID: "explicit-host-allowance", Checkpoint: grantRaw}); err != nil {
		t.Fatal(err)
	}
	restored, err := RestoreStoredChatScope(ctx, store, *paused.Context)
	if err != nil {
		t.Fatal(err)
	}
	next, err := ContextualSession(ctx, historyStoredSession(store, restored, "paused", true))
	if err != nil {
		t.Fatal(err)
	}
	closing := NewBoundary(next)
	historyAssertNoFailure(t, closing.Open())
	events, stop := closing.Event(contract.RunEvent{End: &contract.RunEventEnd{Type: "end"}})
	historyAssertNoFailure(t, events)
	if !stop {
		t.Fatal("did not end")
	}
	terminal, _ := store.ReadHistoryHead(ctx, "paused")
	raw, err = ReadStoredChatSnapshot(ctx, store, terminal.Context.ContextSnapshotHead)
	if err != nil {
		t.Fatal(err)
	}
	saved, _ = ReadChatContextSnapshot(raw)
	if !terminal.Context.Closed || len(saved.Open) != 0 || saved.Turns != 1 {
		t.Fatal("phantom/duplicate terminal turn")
	}
	closing.Close()
}

type historyCloseFaultStore struct {
	HistoryRunStore
	lost bool
}

func (s *historyCloseFaultStore) CommitHistory(ctx context.Context, input RunHistoryCommit) (RunHistoryReceipt, error) {
	if input.Context != nil && input.Context.Closed {
		if s.lost {
			if _, err := s.HistoryRunStore.CommitHistory(ctx, input); err != nil {
				return RunHistoryReceipt{}, err
			}
		}
		return RunHistoryReceipt{}, errors.New("injected closed CAS failure")
	}
	return s.HistoryRunStore.CommitHistory(ctx, input)
}
func TestHistoryFoundationClosedCASNeverDeliversSuccess(t *testing.T) {
	for _, lost := range []bool{false, true} {
		ctx := context.Background()
		memory := NewMemoryRunStore(MemoryRunStoreOptions{})
		store := &historyCloseFaultStore{HistoryRunStore: memory, lost: lost}
		source, _ := NewStoredChatScope(store, "c", NewChatContext("shared"), nil)
		run, err := ContextualSession(ctx, historyStoredSession(store, source, "failed", false))
		if err != nil {
			t.Fatal(err)
		}
		boundary := NewBoundary(run)
		historyAssertNoFailure(t, boundary.Open())
		events, _ := boundary.Event(contract.RunEvent{End: &contract.RunEventEnd{Type: "end"}})
		failed := false
		for _, event := range events {
			failed = failed || event.Error != nil
			if event.ExecutionCheckpoint != nil && (event.ExecutionCheckpoint.Checkpoint.Status == "complete" || event.ExecutionCheckpoint.Checkpoint.Status == "stopped") {
				t.Fatal("successful terminal checkpoint after failed/unknown CAS")
			}
		}
		if !failed {
			t.Fatal("missing storage failure")
		}
		raw, _ := source.Context().Snapshot()
		saved, _ := ReadChatContextSnapshot(raw)
		if len(saved.Open) != 0 {
			t.Fatal("serialized close without actual close")
		}
		boundary.Close()
	}
}
func TestHistoryFoundationInterruptedExactRequestAndHostClose(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryRunStore(MemoryRunStoreOptions{})
	source, _ := NewStoredChatScope(store, "c", NewChatContext("shared"), nil)
	run, err := ContextualSession(ctx, historyStoredSession(store, source, "interrupted", false))
	if err != nil {
		t.Fatal(err)
	}
	binding := run.Session.Context.Persistence
	exact := []byte(`{ "body":"{\"z\":false,\"a\":null}", "method":"POST", "suffix":"/call" }`)
	if _, err = beginDurableModel(ctx, binding, "source", "fixture", exact); err != nil {
		t.Fatal(err)
	}
	run.Context.Close()
	head, _ := store.ReadHistoryHead(ctx, "interrupted")
	restored, err := RestoreStoredChatScope(ctx, store, *head.Context)
	if err != nil {
		t.Fatal(err)
	}
	next, err := ContextualSession(ctx, historyStoredSession(store, restored, "interrupted", true))
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := PrepareDurableRequest(next.Session.Context.Persistence, "model", "source", "fixture", []byte(`{"changed":true}`))
	if err != nil || !bytes.Equal(prepared, exact) {
		t.Fatal("prepared request bytes changed", err)
	}
	next.Context.Close()
	closed, err := restored.Close(ctx)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := ReadStoredChatSnapshot(ctx, store, closed)
	if err != nil {
		t.Fatal(err)
	}
	saved, _ := ReadChatContextSnapshot(raw)
	if len(saved.Open) != 0 || saved.Turns != 1 {
		t.Fatal("host close not acknowledged")
	}
}
func TestHistoryFoundationHandwrittenChunkAndRowDigests(t *testing.T) {
	chunks := readJSON(t, "contract/vectors/storage/history-chunks.json")
	for _, item := range chunks.Get("items").Items() {
		manifest, err := ReadHistoryChunkManifest([]byte(CanonicalJSON(item)))
		if err != nil || manifest.Digest != HistoryBytesDigest([]byte(chunks.Get("sourceText").Str())) {
			t.Fatal("handwritten chunk", err)
		}
		if _, err = ReadHistoryChunkManifest([]byte(CanonicalJSON(item.Clone().Set("unknown", Bool(true))))); err == nil {
			t.Fatal("unknown chunk member accepted")
		}
	}
	rows := readJSON(t, "contract/vectors/storage/history-entries.json")
	for _, item := range rows.Get("items").Items() {
		if HistoryBytesDigest([]byte(CanonicalJSON(recordWithoutDigest(item)))) != item.Get("digest").Str() {
			t.Fatal("independent row digest differs")
		}
	}
}

func TestHistoryFoundationPrivateCriticRestartExactScopeAndRequest(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryRunStore(MemoryRunStoreOptions{})
	chat := NewChatContext("shared")
	source, _ := NewStoredChatScope(store, "c", chat, nil)
	run, err := ContextualSession(ctx, historyStoredSession(store, source, "critic", false))
	if err != nil {
		t.Fatal(err)
	}
	author, err := run.Context.Ledger.Retain(Document{ID: "author-only", Kind: "artifact", Label: "Author work", Value: String("SECRET-AUTHOR")}, false)
	if err != nil {
		t.Fatal(err)
	}
	other, _ := chat.OpenTurn()
	run.Context.BeginIsolatedPhase()
	run.Context.CriticReading = true
	privateScope := run.Context.Ledger.Scope()
	if privateScope == chat.Scope() || len(run.Context.Ledger.Snapshot().Entries) != 0 {
		t.Fatal("author visible in critic")
	}
	if err = chat.room(0, 0); err != nil || chat.entries.get(author.ID) != nil || !chat.hasActiveTurn(other.number) {
		t.Fatal("author release/other turn changed", err)
	}
	binding := run.Context.Persistence("critic")
	exact := []byte(`{ "body":"{\"z\":false,\"a\":null}", "method":"POST", "suffix":"/chat/completions" }`)
	var physical [][]byte
	verify := func(response *http.Response) error {
		return wire.QuarantineDurableResponse(ctx, wire.OpenAICompatible, response)
	}
	_, err = InvokeDurableModel(ctx, binding, "source", "fixture", exact, func(raw []byte) (*http.Response, error) {
		physical = append(physical, bytes.Clone(raw))
		return nil, errors.New("interrupted transport")
	}, verify)
	if err == nil {
		t.Fatal("transport fixture succeeded")
	}
	run.Context.Close()
	held, _ := store.ReadHistoryHead(ctx, "critic")
	if held.Context == nil || !held.Context.Closed || held.Context.PrivateScope != privateScope {
		t.Fatal("private binding missing")
	}
	restored, err := RestoreStoredChatScope(ctx, store, *held.Context)
	if err != nil {
		t.Fatal(err)
	}
	next, err := ContextualSession(ctx, historyStoredSession(store, restored, "critic", true))
	if err != nil {
		t.Fatal(err)
	}
	next.Context.BeginIsolatedPhase()
	next.Context.CriticReading = true
	if next.Context.Ledger.Scope() != privateScope || restored.Context().TurnCount() != 2 {
		t.Fatal("critic resume re-isolated/opened turn")
	}
	nextBinding := next.Context.Persistence("critic")
	prepared, err := PrepareDurableRequest(nextBinding, "model", "source", "fixture", []byte(`{"changed":true}`))
	if err != nil || !bytes.Equal(prepared, exact) {
		t.Fatal("prepared scope/bytes changed", err)
	}
	response, err := InvokeDurableModel(ctx, nextBinding, "source", "fixture", prepared, func(raw []byte) (*http.Response, error) {
		physical = append(physical, bytes.Clone(raw))
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"id":"critic","object":"chat.completion","created":0,"model":"fixture","choices":[{"index":0,"message":{"role":"assistant","content":"Reviewed."},"finish_reason":"stop"}]}`))}, nil
	}, verify)
	if err != nil || response == nil || len(physical) != 2 || !bytes.Equal(physical[0], physical[1]) {
		t.Fatal("physical resumed call changed", err)
	}
	response.Body.Close()
	if len(next.Context.Ledger.Snapshot().Entries) != 0 || !restored.Context().hasActiveTurn(other.number) {
		t.Fatal("author visible/other turn lost")
	}
	next.Context.BeginIsolatedPhase()
	if next.Context.Ledger.Scope() == privateScope || restored.Context().TurnCount() != 2 {
		t.Fatal("later critic reused restored private phase")
	}
	next.Context.Close()
}
func TestHistoryFoundationPrivateCriticCASLossNoDispatch(t *testing.T) {
	for _, lost := range []bool{false, true} {
		ctx := context.Background()
		memory := NewMemoryRunStore(MemoryRunStoreOptions{})
		store := &historyCloseFaultStore{HistoryRunStore: memory, lost: lost}
		source, _ := NewStoredChatScope(store, "c", NewChatContext("shared"), nil)
		run, err := ContextualSession(ctx, historyStoredSession(store, source, "private-loss", false))
		if err != nil {
			t.Fatal(err)
		}
		run.Context.BeginIsolatedPhase()
		run.Context.CriticReading = true
		calls := 0
		_, err = InvokeDurableModel(ctx, run.Context.Persistence("critic"), "source", "fixture", []byte(`{}`), func([]byte) (*http.Response, error) { calls++; return nil, nil }, func(*http.Response) error { return nil })
		if err == nil || calls != 0 || source.Context().hasActiveTurn(1) {
			t.Fatal("private CAS loss dispatched or retained author", err, calls)
		}
		run.Context.Close()
	}
}
func TestHistoryFoundationPrivateBindingRefusals(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryRunStore(MemoryRunStoreOptions{})
	source, _ := NewStoredChatScope(store, "c", NewChatContext("shared"), nil)
	run, err := ContextualSession(ctx, historyStoredSession(store, source, "bad-private", false))
	if err != nil {
		t.Fatal(err)
	}
	head, _ := store.ReadHistoryHead(ctx, "bad-private")
	bad := *head.Context
	bad.Closed = true
	bad.PrivateScope = "invented"
	if _, err = RestoreStoredChatScope(ctx, store, bad); err == nil {
		t.Fatal("active author accepted as private")
	}
	run.Context.BeginIsolatedPhase()
	if _, err = beginDurableModel(ctx, run.Context.Persistence("author"), "source", "fixture", []byte(`{"wrongRole":true}`)); err != nil {
		t.Fatal(err)
	}
	run.Context.Close()
	held, _ := store.ReadHistoryHead(ctx, "bad-private")
	restored, err := RestoreStoredChatScope(ctx, store, *held.Context)
	if err != nil {
		t.Fatal(err)
	}
	next, err := ContextualSession(ctx, historyStoredSession(store, restored, "bad-private", true))
	if err != nil {
		t.Fatal(err)
	}
	next.Context.BeginIsolatedPhase()
	calls := 0
	if _, err = InvokeDurableModel(ctx, next.Context.Persistence("critic"), "source", "fixture", []byte(`{}`), func([]byte) (*http.Response, error) { calls++; return nil, nil }, func(*http.Response) error { return nil }); err == nil || calls != 0 {
		t.Fatal("wrong pending role admitted", err, calls)
	}
	next.Context.Close()
}

func TestHistoryFoundationPausedPrivateCriticScopeAndRequest(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryRunStore(MemoryRunStoreOptions{})
	source, _ := NewStoredChatScope(store, "c", NewChatContext("shared"), nil)
	run, err := ContextualSession(ctx, historyStoredSession(store, source, "paused-critic", false))
	if err != nil {
		t.Fatal(err)
	}
	boundary := NewBoundary(run)
	historyAssertNoFailure(t, boundary.Open())
	run.Context.BeginIsolatedPhase()
	run.Context.CriticReading = true
	privateScope := run.Context.Ledger.Scope()
	exact := []byte(`{ "body":"critic request", "method":"POST", "suffix":"/call" }`)
	if _, err = beginDurableModel(ctx, run.Context.Persistence("critic"), "source", "fixture", exact); err != nil {
		t.Fatal(err)
	}
	paused, err := run.Context.Snapshot("paused")
	if err != nil {
		t.Fatal(err)
	}
	events, _ := boundary.Event(contract.RunEvent{BudgetPaused: &contract.RunEventBudgetPaused{Type: "budget_paused", Checkpoint: paused}})
	historyAssertNoFailure(t, events)
	events, _ = boundary.Event(contract.RunEvent{End: &contract.RunEventEnd{Type: "end"}})
	historyAssertNoFailure(t, events)
	boundary.Close()
	held, _ := store.ReadHistoryHead(ctx, "paused-critic")
	head, _ := store.Load(ctx, "paused-critic")
	saved, err := ReadExecution(MustParseJSON(string(head.Checkpoint)))
	if err != nil {
		t.Fatal(err)
	}
	if saved.Status != "paused" || saved.Context.Scope != privateScope || held.Context.PrivateScope != privateScope || source.Context().hasActiveTurn(1) {
		t.Fatal("paused private binding changed")
	}
	grant, err := ExtendExecution(*saved, false, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := canonicalRaw(grant)
	if _, err = store.Commit(ctx, RunCommit{RunID: "paused-critic", ExpectedRevision: head.Revision, CommitID: "explicit-host-critic-allowance", Checkpoint: raw}); err != nil {
		t.Fatal(err)
	}
	restored, err := RestoreStoredChatScope(ctx, store, *held.Context)
	if err != nil {
		t.Fatal(err)
	}
	next, err := ContextualSession(ctx, historyStoredSession(store, restored, "paused-critic", true))
	if err != nil {
		t.Fatal(err)
	}
	next.Context.BeginIsolatedPhase()
	next.Context.CriticReading = true
	if next.Context.Ledger.Scope() != privateScope || restored.Context().TurnCount() != 1 {
		t.Fatal("paused critic re-isolated/opened turn")
	}
	prepared, err := PrepareDurableRequest(next.Context.Persistence("critic"), "model", "source", "fixture", []byte(`{"changed":true}`))
	if err != nil || !bytes.Equal(prepared, exact) {
		t.Fatal("paused private request changed", err)
	}
	next.Context.Close()
}
