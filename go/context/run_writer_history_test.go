package runcontext

import (
	"bytes"
	"context"
	"errors"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"strings"
	"testing"
)

type historyWriterFaultStore struct {
	*MemoryRunStore
	armed        bool
	writes       int
	mutate       func(context.Context, RunHistoryCommit, *RunHistoryReceipt) error
	page         *RunHistoryPage
	badHead      bool
	stages       int
	headMutation func(*RunHistoryHead)
}

func (s *historyWriterFaultStore) CommitHistory(ctx context.Context, input RunHistoryCommit) (RunHistoryReceipt, error) {
	receipt, err := s.MemoryRunStore.CommitHistory(ctx, input)
	if err == nil && s.armed {
		s.writes++
		if s.mutate != nil {
			err = s.mutate(ctx, input, &receipt)
		}
	}
	return receipt, err
}
func (s *historyWriterFaultStore) ReadHistoryPage(ctx context.Context, id, revision string, after, limit int) (RunHistoryPage, error) {
	if s.page != nil {
		return historyClone(*s.page), nil
	}
	return s.MemoryRunStore.ReadHistoryPage(ctx, id, revision, after, limit)
}
func (s *historyWriterFaultStore) ReadHistoryHead(ctx context.Context, id string) (*RunHistoryHead, error) {
	head, err := s.MemoryRunStore.ReadHistoryHead(ctx, id)
	if head != nil && s.headMutation != nil {
		s.headMutation(head)
	}
	if head != nil && s.badHead {
		head.ExecutionRevision = "different"
	}
	return head, err
}
func (s *historyWriterFaultStore) StageHistoryObjects(ctx context.Context, resource HistoryResource, objects []HistoryObject) error {
	s.stages++
	return s.MemoryRunStore.StageHistoryObjects(ctx, resource, objects)
}
func historyWriterRows(t *testing.T, store HistoryRunStore, id string) (*RetainedRunHistory, []*Value) {
	t.Helper()
	view, err := ReadRetainedRunHistory(context.Background(), store, id)
	if err != nil {
		t.Fatal(err)
	}
	rows := []*Value{}
	for _, commit := range view.Commits() {
		for _, ref := range commit.Observations {
			raw, err := view.ReadObject(ref)
			if err != nil {
				t.Fatal(err)
			}
			row, err := ReadHistoryObservation(raw)
			if err != nil {
				t.Fatal(err)
			}
			rows = append(rows, row)
		}
	}
	return view, rows
}
func historyWriterCount(rows []*Value, kind string) int {
	n := 0
	for _, row := range rows {
		if row.Get("kind").Str() == kind {
			n++
		}
	}
	return n
}
func TestRunWriterHistoryFreshGenesisResumeAndExactRequest(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryRunStore(MemoryRunStoreOptions{})
	writer, binding := writerFixture(t, store, "history-writer", false)
	calls := 0
	exact := ` { "n":9007199254740993, "negative":-0, "exponent":1e+3, "escaped":"\u0061\/", "fraction":0.10000000000000001 } `
	send := func(raw []byte) (int, error) {
		calls++
		if string(raw) != exact {
			t.Fatal("original request bytes changed")
		}
		return 1, nil
	}
	if _, err := InvokeDurable(ctx, binding, "host", "source", "write", []byte(exact), false, send, nil, nil); err != nil {
		t.Fatal(err)
	}
	view, rows := historyWriterRows(t, store, writer.ID)
	if view.Head().Coverage != "recording-incomplete" || historyWriterCount(rows, "genesis") != 1 || historyWriterCount(rows, "original") != 1 || historyWriterCount(rows, "request") != 1 || len(view.Commits()[0].Observations) != 3 {
		t.Fatal("fresh genesis/original/request publication missing")
	}
	for _, row := range rows {
		if row.Get("kind").Str() == "genesis" && CanonicalJSON(row) != `{"kind":"genesis","profile":{"compactions":true,"genesis":true,"originals":true,"profile":"full-history","requests":true,"version":1},"scope":{"execution":"history-writer","role":"author"},"version":1}` {
			t.Fatal("genesis differs from handwritten record")
		}
		if row.Get("kind").Str() == "request" {
			full, _, err := liveHistoryChunk(liveHistoryRef(row.Get("request")), view.ReadObject)
			if err != nil || string(full) != exact || row.Get("originals").Len() != 1 {
				t.Fatal("exact request payload/original missing", err)
			}
		}
	}
	resumed, next := writerFixture(t, store, writer.ID, true)
	if _, err := resumed.Event(ctx, *next, contract.RunEvent{Message: &contract.RunEventMessage{Type: "message", Text: "after resume"}}); err != nil {
		t.Fatal(err)
	}
	_, rows = historyWriterRows(t, store, writer.ID)
	if historyWriterCount(rows, "genesis") != 1 || historyWriterCount(rows, "original") != 1 || historyWriterCount(rows, "request") != 1 {
		t.Fatal("resumed recorder duplicated known roots")
	}
	if _, err := InvokeDurable(ctx, next, "host", "source", "write", []byte(exact), false, send, nil, nil); err != nil || calls != 1 {
		t.Fatal("original send repeated", calls, err)
	}
}
func TestRunWriterHistoryLegacyRemainsDisabled(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryRunStore(MemoryRunStoreOptions{})
	raw, _ := canonicalRaw(writerCheckpoint("legacy"))
	if _, err := store.Commit(ctx, RunCommit{RunID: "legacy", CommitID: "before-recording", Checkpoint: raw}); err != nil {
		t.Fatal(err)
	}
	writer, binding := writerFixture(t, store, "legacy", true)
	if err := writer.Save(ctx, *binding, *writer.Checkpoint()); err != nil {
		t.Fatal(err)
	}
	view, rows := historyWriterRows(t, store, "legacy")
	if view.Head().Coverage != "legacy-incomplete" || len(rows) != 0 {
		t.Fatal("legacy history was fabricated")
	}
	if _, err := writer.HistoryOriginal(ctx, *binding, "messages", "[]", "messages", 0); err == nil {
		t.Fatal("legacy helper fabricated original")
	}
}
func TestRunWriterHistoryAtomicChatEventCheckpoint(t *testing.T) {
	ctx := context.Background()
	memory := NewMemoryRunStore(MemoryRunStoreOptions{})
	store := &historyWriterFaultStore{MemoryRunStore: memory}
	writer, binding := writerFixture(t, store, "history-chat", false)
	source, err := NewStoredChatScope(store, "chat", NewChatContext("fixture"), nil)
	if err != nil {
		t.Fatal(err)
	}
	source.OpenTurn()
	writer.contextSource = source
	if _, err = writer.Event(ctx, *binding, contract.RunEvent{Message: &contract.RunEventMessage{Type: "message", Text: "exact event"}}); err != nil {
		t.Fatal(err)
	}
	head, _ := memory.Load(ctx, writer.ID)
	history, _ := memory.ReadHistoryHead(ctx, writer.ID)
	chat, _ := memory.LoadContextSnapshot(ctx, "chat")
	view, rows := historyWriterRows(t, store, writer.ID)
	if head.Revision != history.ExecutionRevision || history.Context == nil || chat.Revision != history.Context.Revision || len(view.Commits()[0].Events) != 1 || historyWriterCount(rows, "genesis") != 1 {
		t.Fatal("checkpoint/events/chat/history not atomically visible")
	}
	before := historyInputText(source.expected)
	store.armed = true
	store.mutate = func(ctx context.Context, input RunHistoryCommit, receipt *RunHistoryReceipt) error {
		page, _ := memory.ReadHistoryPage(ctx, writer.ID, receipt.History.Revision, receipt.History.CommitCount-1, 1)
		page.Items = []RunHistoryEntry{}
		store.page = &page
		return nil
	}
	if _, err = InvokeDurable(ctx, binding, "host", "source", "write", []byte(`{}`), false, func([]byte) (int, error) { t.Fatal("dispatch escaped invalid history ACK"); return 0, nil }, nil, nil); err == nil {
		t.Fatal("missing history row accepted")
	}
	if historyInputText(source.expected) != before {
		t.Fatal("chat acknowledgement advanced before history validation")
	}
}
func TestRunWriterHistoryHelperExactCheckpointAndOrphanIsolation(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryRunStore(MemoryRunStoreOptions{})
	writer, binding := writerFixture(t, store, "history-helper", false)
	if err := writer.Save(ctx, *binding, writerCheckpoint(writer.ID)); err != nil {
		t.Fatal(err)
	}
	before, _ := store.ReadHistoryHead(ctx, writer.ID)
	actual, _ := store.Load(ctx, writer.ID)
	exact := bytes.Clone(actual.Checkpoint)
	writer, binding = writerFixture(t, store, writer.ID, true)
	before, _ = store.ReadHistoryHead(ctx, writer.ID)
	changed := writerCheckpoint(writer.ID)
	changed.Used = 7
	writer.Bind(func() (contract.Checkpoint, error) { return changed, nil }, RecordDefaults, nil)
	state := historyInputText(writer.Checkpoint())
	prepared, _ := PrepareHistoryOriginal(binding.Scope, "orphan", "[]", "messages", 0)
	orphan, err := StageHistoryObservation(ctx, store, writer.ID, prepared)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.ReadHistoryObject(ctx, HistoryResource{Kind: "run", ID: writer.ID}, before.Revision, orphan.Digest, 0, HistoryObjectBytes); err == nil {
		t.Fatal("orphan staging published")
	}
	ref, err := writer.HistoryOriginal(ctx, *binding, "messages", `[{"role":"user","content":"exact"}]`, "messages", 0)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := store.ReadHistoryHead(ctx, writer.ID)
	head, _ := store.Load(ctx, writer.ID)
	if !bytes.Equal(head.Checkpoint, exact) {
		t.Fatal("helper changed exact acknowledged checkpoint/event transition")
	}
	view, _ := historyWriterRows(t, store, writer.ID)
	if !bytes.Equal(head.Checkpoint, exact) || historyInputText(after.Checkpoint) != historyInputText(before.Checkpoint) || historyInputText(writer.Checkpoint()) != state || after.ExecutionRevision == before.ExecutionRevision || after.Root == before.Root || after.CommitCount != before.CommitCount+1 || after.EventCount != before.EventCount || view.Commits()[len(view.Commits())-1].EventStart != before.EventCount+1 {
		t.Fatal("helper changed exact acknowledged checkpoint/event transition")
	}
	if _, err = view.ReadObject(ref); err != nil {
		t.Fatal(err)
	}
	if _, err = view.ReadObject(orphan); err == nil {
		t.Fatal("helper published an unrelated orphan")
	}
	compact, err := writer.HistoryCompaction(ctx, *binding, "compact", []HistoryObjectRef{ref}, `"replacement"`, "summary", nil, "legacy")
	if err != nil {
		t.Fatal(err)
	}
	view, _ = historyWriterRows(t, store, writer.ID)
	if _, err = view.ReadObject(compact); err != nil {
		t.Fatal(err)
	}
}
func TestRunWriterHistoryChildScopeIsolation(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryRunStore(MemoryRunStoreOptions{})
	writer, binding := writerFixture(t, store, "history-parent", false)
	if err := writer.Save(ctx, *binding, writerCheckpoint(writer.ID)); err != nil {
		t.Fatal(err)
	}
	childWriter, childBinding := writerFixture(t, store, "child", false)
	if err := childWriter.Save(ctx, *childBinding, writerCheckpoint("child")); err != nil {
		t.Fatal(err)
	}
	child := RunBinding{Writer: writer, Scope: DurableScope{Execution: "child", Role: "author", Task: "child"}}
	if err := writer.Save(ctx, child, *childWriter.Checkpoint()); err != nil {
		t.Fatal(err)
	}
	ref, err := writer.HistoryOriginal(ctx, child, "child-messages", "[]", "messages", 0)
	if err != nil {
		t.Fatal(err)
	}
	unknown := child
	unknown.Scope.Task = "unknown"
	if _, err = writer.HistoryOriginal(ctx, unknown, "unknown", "[]", "messages", 0); err == nil {
		t.Fatal("unknown helper scope accepted")
	}
	if _, err = writer.HistoryCompaction(ctx, *binding, "cross-scope", []HistoryObjectRef{ref}, `"replacement"`, "summary", nil, "legacy"); err == nil {
		t.Fatal("cross-scope original accepted")
	}
	resumed, next := writerFixture(t, store, writer.ID, true)
	next.Writer = writer
	if _, err = resumed.HistoryOriginal(ctx, *next, "wrong", "[]", "messages", 0); err == nil {
		t.Fatal("foreign writer helper accepted")
	}
}
func TestRunWriterHistoryInvalidAcknowledgementNoDispatchOrRetry(t *testing.T) {
	for _, mode := range []string{"missing-head", "head-checkpoint", "row-commit", "row-previous", "row-sequence", "row-revision", "row-checkpoint", "row-events", "row-observations", "row-event-start", "lost-ack"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			memory := NewMemoryRunStore(MemoryRunStoreOptions{})
			store := &historyWriterFaultStore{MemoryRunStore: memory}
			writer, binding := writerFixture(t, store, "history-fault", false)
			if mode != "missing-head" {
				if err := writer.Save(ctx, *binding, writerCheckpoint(writer.ID)); err != nil {
					t.Fatal(err)
				}
			}
			before := historyInputText(writer.Checkpoint())
			store.armed = true
			store.mutate = func(ctx context.Context, input RunHistoryCommit, receipt *RunHistoryReceipt) error {
				if mode == "lost-ack" {
					return errors.New("lost acknowledgement")
				}
				if mode == "missing-head" {
					receipt.History = RunHistoryHead{}
					return nil
				}
				if mode == "head-checkpoint" {
					receipt.History.Checkpoint.Bytes++
					return nil
				}
				page, _ := memory.ReadHistoryPage(ctx, writer.ID, receipt.History.Revision, receipt.History.CommitCount-1, 1)
				row := &page.Items[0]
				switch mode {
				case "row-commit":
					row.CommitID = "different"
				case "row-previous":
					sum := "sha256:" + strings.Repeat("a", 64)
					row.PreviousDigest = &sum
				case "row-sequence":
					row.Sequence++
				case "row-revision":
					row.ExecutionRevision = "different"
				case "row-checkpoint":
					row.Checkpoint.Bytes++
				case "row-events":
					row.Events = []HistoryObjectRef{{Digest: "sha256:" + strings.Repeat("b", 64), Bytes: 1}}
				case "row-observations":
					row.Observations = []HistoryObjectRef{}
				case "row-event-start":
					row.EventStart++
				}
				row.EventCount = len(row.Events)
				raw, _ := historyJSONBytes(row)
				value, _ := ParseJSON(raw)
				row.Digest = HistoryBytesDigest([]byte(CanonicalJSON(recordWithoutDigest(value))))
				receipt.History.Root = row.Digest
				store.page = &page
				return nil
			}
			calls := 0
			send := func([]byte) (int, error) { calls++; return 1, nil }
			if _, err := InvokeDurable(ctx, binding, "host", "source", "write", []byte(`{}`), false, send, nil, nil); err == nil {
				t.Fatal("invalid history acknowledgement accepted")
			}
			if calls != 0 {
				t.Fatal("dispatch escaped a refused history acknowledgement")
			}
			if historyInputText(writer.Checkpoint()) != before {
				t.Fatal("local checkpoint advanced on invalid acknowledgement")
			}
			if _, err := InvokeDurable(ctx, binding, "host", "source", "write", []byte(`{}`), false, send, nil, nil); err == nil || store.writes != 1 || calls != 0 {
				t.Fatal("uncertain original publication repeated")
			}
		})
	}
}
func TestRunWriterHistoryLostHelperAcknowledgement(t *testing.T) {
	ctx := context.Background()
	store := &historyWriterFaultStore{MemoryRunStore: NewMemoryRunStore(MemoryRunStoreOptions{})}
	writer, binding := writerFixture(t, store, "history-lost-helper", false)
	if err := writer.Save(ctx, *binding, writerCheckpoint(writer.ID)); err != nil {
		t.Fatal(err)
	}
	before := historyInputText(writer.Checkpoint())
	store.armed = true
	store.mutate = func(context.Context, RunHistoryCommit, *RunHistoryReceipt) error {
		return errors.New("lost helper acknowledgement")
	}
	if _, err := writer.HistoryOriginal(ctx, *binding, "messages", "[]", "messages", 0); err == nil {
		t.Fatal("helper returned ref without acknowledgement")
	}
	if historyInputText(writer.Checkpoint()) != before {
		t.Fatal("helper advanced local state without acknowledgement")
	}
	if _, err := writer.HistoryOriginal(ctx, *binding, "messages", "[]", "messages", 0); err == nil || store.writes != 1 {
		t.Fatal("helper retried uncertain publication")
	}
	store.armed = false
	resumed, next := writerFixture(t, store, writer.ID, true)
	if _, err := resumed.HistoryOriginal(ctx, *next, "after-resume", "[]", "messages", 0); err != nil {
		t.Fatal(err)
	}
}
func TestRunWriterHistoryMalformedHeadAndRequestControls(t *testing.T) {
	ctx := context.Background()
	store := &historyWriterFaultStore{MemoryRunStore: NewMemoryRunStore(MemoryRunStoreOptions{})}
	writer, binding := writerFixture(t, store, "history-bad-head", false)
	if err := writer.Save(ctx, *binding, writerCheckpoint(writer.ID)); err != nil {
		t.Fatal(err)
	}
	resumed, next := writerFixture(t, store, writer.ID, true)
	store.badHead = true
	calls := 0
	if _, err := InvokeDurable(ctx, next, "host", "source", "write", []byte(`{}`), false, func([]byte) (int, error) { calls++; return 1, nil }, nil, nil); err == nil || calls != 0 {
		t.Fatal("malformed retained head allowed dispatch")
	}
	_ = resumed
	for _, bad := range []string{`{"x":1,"x":2}`, `{"x":1,"\u0078":2}`, `{"x":01}`, `{"x":1e9999}`, `{"x":"\ud800"}`, `{"x":1} trailing`, `[1,]`} {
		if _, err := readHistoryRequestJSON([]byte(bad)); err == nil {
			t.Fatal("malformed lexical request accepted", bad)
		}
	}
	if _, err := readHistoryJSON([]byte(`{"n":9007199254740993}`), 100, true, true); err == nil {
		t.Fatal("outer envelope large-number contract weakened")
	}
}
func TestRunWriterHistoryHelperWaitsForAcknowledgement(t *testing.T) {
	ctx := context.Background()
	store := &historyWriterFaultStore{MemoryRunStore: NewMemoryRunStore(MemoryRunStoreOptions{})}
	writer, binding := writerFixture(t, store, "history-helper-wait", false)
	if err := writer.Save(ctx, *binding, writerCheckpoint(writer.ID)); err != nil {
		t.Fatal(err)
	}
	before := historyInputText(writer.Checkpoint())
	entered := make(chan struct{})
	release := make(chan struct{})
	store.armed = true
	store.mutate = func(_ context.Context, input RunHistoryCommit, _ *RunHistoryReceipt) error {
		if len(input.Run.Events) != 0 || !bytes.Equal(input.Run.Checkpoint, writer.acknowledgedCheckpoint) {
			t.Fatal("helper fabricated checkpoint/event")
		}
		close(entered)
		<-release
		return nil
	}
	done := make(chan error, 1)
	go func() { _, err := writer.HistoryOriginal(ctx, *binding, "messages", "[]", "messages", 0); done <- err }()
	<-entered
	select {
	case <-done:
		close(release)
		t.Fatal("helper returned before acknowledgement")
	default:
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if historyInputText(writer.Checkpoint()) != before {
		t.Fatal("helper changed latest checkpoint")
	}
}
func TestRunWriterHistoryLostResultAcknowledgementDoesNotRepeatSend(t *testing.T) {
	ctx := context.Background()
	store := &historyWriterFaultStore{MemoryRunStore: NewMemoryRunStore(MemoryRunStoreOptions{})}
	writer, binding := writerFixture(t, store, "history-lost-result", false)
	store.armed = true
	store.mutate = func(_ context.Context, input RunHistoryCommit, _ *RunHistoryReceipt) error {
		v, err := ParseJSON(input.Run.Checkpoint)
		if err != nil {
			return err
		}
		for _, row := range v.Get("durable").Get("dispatches").Items() {
			if row.Get("state").Str() == "result" {
				return errors.New("lost result acknowledgement")
			}
		}
		return nil
	}
	calls := 0
	send := func([]byte) (int, error) { calls++; return 1, nil }
	if _, err := InvokeDurable(ctx, binding, "host", "source", "write", []byte(`{}`), false, send, nil, nil); err == nil || calls != 1 {
		t.Fatal("missing original outcome uncertainty", calls, err)
	}
	store.armed = false
	_, next := writerFixture(t, store, writer.ID, true)
	if _, err := InvokeDurable(ctx, next, "host", "source", "write", []byte(`{}`), false, send, nil, nil); err != nil || calls != 1 {
		t.Fatal("original send repeated", calls, err)
	}
}

func TestRunWriterHistoryInitializationBeforeStaging(t *testing.T) {
	for _, kind := range []string{"legacy-malformed", "stale-resume"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			store := &historyWriterFaultStore{MemoryRunStore: NewMemoryRunStore(MemoryRunStoreOptions{})}
			id := "history-init"
			if kind == "legacy-malformed" {
				raw, err := canonicalRaw(writerCheckpoint(id))
				if err != nil {
					t.Fatal(err)
				}
				if _, err = store.Commit(ctx, RunCommit{RunID: id, CommitID: "before-recording", Checkpoint: raw}); err != nil {
					t.Fatal(err)
				}
			} else {
				first, binding := writerFixture(t, store, id, false)
				if err := first.Save(ctx, *binding, writerCheckpoint(id)); err != nil {
					t.Fatal(err)
				}
			}
			writer, binding := writerFixture(t, store, id, true)
			if kind == "legacy-malformed" {
				store.headMutation = func(head *RunHistoryHead) { head.Version = 0 }
			} else {
				competing, other := writerFixture(t, store, id, true)
				changed := *competing.Checkpoint()
				changed.Used = 1
				if err := competing.Save(ctx, *other, changed); err != nil {
					t.Fatal(err)
				}
			}
			store.stages = 0
			calls := 0
			if _, err := InvokeDurable(ctx, binding, "host", "source", "write", []byte(`{}`), false, func([]byte) (int, error) { calls++; return 1, nil }, nil, nil); err == nil {
				t.Fatal("invalid initialization allowed dispatch")
			}
			if calls != 0 || store.stages != 0 {
				t.Fatalf("initialization allowed staging or dispatch: stages=%d calls=%d", store.stages, calls)
			}
			_ = writer
		})
	}
}
