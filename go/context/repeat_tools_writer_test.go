package runcontext

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"
)

func record204Time() time.Time { return time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC) }
func attach204(t *testing.T, binding *RunBinding, stop int64) {
	t.Helper()
	if err := binding.Writer.attachRepeatScope(*binding, &RepeatPolicy{1, stop}, repeatDispatchSource{}); err != nil {
		t.Fatal(err)
	}
}
func invoke204(binding *RunBinding, args string, value int) (int, error) {
	return InvokeDurable(context.Background(), binding, "host", "source", "lookup", []byte(args), true, func([]byte) (int, error) { return value, nil }, nil, nil)
}
func TestRepeat204WriterInterleavingRolesChildrenAndStop(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryRunStore(MemoryRunStoreOptions{})
	writer, binding := writerFixture(t, store, "repeat", false)
	attach204(t, binding, 2)
	for _, input := range []struct {
		args  string
		value int
	}{{`{"n":1}`, 1}, {`{"n":1}`, 2}, {`{"n":2}`, 1}} {
		if _, err := invoke204(binding, input.args, input.value); err != nil {
			t.Fatal(err)
		}
	}
	critic := binding.WithRole("critic")
	if _, err := invoke204(critic, `{"n":1}`, 1); err != nil {
		t.Fatal(err)
	}
	if writer.RepeatState(*binding).Count != 1 {
		t.Fatal("role reset cumulative repeat count")
	}
	child := &RunBinding{Writer: writer, Scope: DurableScope{Execution: "child", Task: "child", Role: "author"}}
	attach204(t, child, 2)
	if _, err := invoke204(child, `{"n":1}`, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := invoke204(child, `{"n":1}`, 1); err != nil {
		t.Fatal(err)
	}
	if writer.RepeatState(*child).Count != 1 || writer.RepeatState(*binding).Count != 1 {
		t.Fatal("child scope leaked")
	}
	if _, err := invoke204(binding, `{"n":2}`, 1); err != nil {
		t.Fatal(err)
	}
	calls := 0
	for _, kind := range []string{"host", "runtime", "model", "agent"} {
		_, err := InvokeDurable(ctx, critic, kind, "source", "lookup", []byte(`{}`), true, func([]byte) (int, error) { calls++; return 1, nil }, nil, nil)
		var stopped *RepeatedToolLimit
		if !errors.As(err, &stopped) {
			t.Fatalf("%s bypassed named stop: %v", kind, err)
		}
	}
	if calls != 0 {
		t.Fatal("stopped physical dispatch ran")
	}
	head, _ := store.Load(ctx, "repeat")
	cp, err := ReadExecution(MustParseJSON(string(head.Checkpoint)))
	if err != nil || cp.Status != "stopped" || cp.Reason == nil || *cp.Reason != "RepeatedToolLimit" {
		t.Fatal("named stop not retained", err)
	}
	resumed, held := writerFixture(t, store, "repeat", true)
	attach204(t, held, 2)
	if resumed.RepeatState(*held).Count != 2 {
		t.Fatal("resume reset count")
	}
	page, err := store.ReadEvents(ctx, "repeat", "", 100)
	if err != nil {
		t.Fatal(err)
	}
	repeats := 0
	for _, item := range page.Items {
		event, e := ReadStoredRunEvent(item.Bytes)
		if e != nil {
			t.Fatal(e)
		}
		if event.Event.RepeatDetected != nil {
			repeats++
		}
	}
	if repeats != 3 {
		t.Fatalf("atomic repeat events=%d want3", repeats)
	}
}

type repeat204FaultStore struct {
	*MemoryRunStore
	armed, after bool
}

func (s *repeat204FaultStore) Commit(ctx context.Context, input RunCommit) (RunReceipt, error) {
	cp := MustParseJSON(string(input.Checkpoint))
	hit := s.armed && cp.Get("repeatTools").Get("executions").Len() > 0 && cp.Get("repeatTools").Get("executions").Items()[0].Get("count").Num() == 1
	if hit {
		s.armed = false
		if !s.after {
			return RunReceipt{}, errors.New("fixture precommit result loss")
		}
	}
	receipt, err := s.MemoryRunStore.Commit(ctx, input)
	if hit && err == nil {
		return receipt, errors.New("fixture lost result ACK")
	}
	return receipt, err
}

// Intentionally expose only RunStore: the fault is the actual original result CAS.
type repeat204PlainStore struct{ target *repeat204FaultStore }

func (s repeat204PlainStore) Durability() string { return s.target.Durability() }
func (s repeat204PlainStore) Load(ctx context.Context, id string) (*RunHead, error) {
	return s.target.Load(ctx, id)
}
func (s repeat204PlainStore) ReadEvents(ctx context.Context, id, cursor string, limit int) (RunEventPage, error) {
	return s.target.ReadEvents(ctx, id, cursor, limit)
}
func (s repeat204PlainStore) Commit(ctx context.Context, input RunCommit) (RunReceipt, error) {
	return s.target.Commit(ctx, input)
}
func TestRepeat204WriterAcknowledgementLoss(t *testing.T) {
	for _, after := range []bool{false, true} {
		t.Run(map[bool]string{false: "before", true: "after"}[after], func(t *testing.T) {
			fault := &repeat204FaultStore{MemoryRunStore: NewMemoryRunStore(MemoryRunStoreOptions{}), after: after}
			store := repeat204PlainStore{fault}
			_, binding := writerFixture(t, store, "ack", false)
			attach204(t, binding, 5)
			if _, err := invoke204(binding, `{}`, 1); err != nil {
				t.Fatal(err)
			}
			fault.armed = true
			if _, err := InvokeDurable(context.Background(), binding, "host", "source", "lookup", []byte(`{}`), false, func([]byte) (int, error) { return 1, nil }, nil, nil); err == nil {
				t.Fatal("lost result ACK reported success")
			}
			writer, resumed := writerFixture(t, store, "ack", true)
			attach204(t, resumed, 5)
			want := int64(0)
			if after {
				want = 1
			}
			if writer.RepeatState(*resumed).Count != want {
				t.Fatal("ACK-loss count duplicated or fabricated")
			}
			_, err := InvokeDurable(context.Background(), resumed, "host", "source", "lookup", []byte(`{}`), false, func([]byte) (int, error) { t.Fatal("uncertain mutation physically replayed"); return 0, nil }, nil, nil)
			if after && err != nil || !after && err == nil {
				t.Fatal("acknowledged/unknown recovery differs", err)
			}
		})
	}
}
func TestRepeat204WriterConcurrentAlreadyAdmitted(t *testing.T) {
	store := NewMemoryRunStore(MemoryRunStoreOptions{})
	writer, binding := writerFixture(t, store, "concurrent", false)
	attach204(t, binding, 1)
	if _, err := invoke204(binding, `{}`, 1); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	var group sync.WaitGroup
	failures := make(chan error, 2)
	for i := 0; i < 2; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			_, err := InvokeDurable(context.Background(), binding, "host", "source", "lookup", []byte(`{}`), true, func([]byte) (int, error) { entered <- struct{}{}; <-release; return 1, nil }, nil, nil)
			failures <- err
		}()
	}
	<-entered
	<-entered
	close(release)
	group.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal("already admitted claim lost result", err)
		}
	}
	if writer.RepeatState(*binding).Count != 2 || len(writer.RepeatState(*binding).Seen) != 3 {
		t.Fatal("concurrent result transaction lost count")
	}
}
func TestRepeat204WriterRejectsMissingRetainedEvidence(t *testing.T) {
	store := NewMemoryRunStore(MemoryRunStoreOptions{})
	_, binding := writerFixture(t, store, "evidence", false)
	attach204(t, binding, 5)
	invoke204(binding, `{}`, 1)
	head, _ := store.Load(context.Background(), "evidence")
	cp := MustParseJSON(string(head.Checkpoint))
	cp.Get("repeatTools").Get("executions").Items()[0].Set("seen", Array())
	cp.Set("digest", String(RecordDigest(cp)))
	head.Checkpoint = cp.JSON()
	if _, err := NewRunWriter(store, "evidence", head, nil); err == nil {
		t.Fatal("missing acknowledged observation accepted")
	}
}
func TestRepeat204WriterExactPrecisionCustodyAndResume(t *testing.T) {
	store := NewMemoryRunStore(MemoryRunStoreOptions{})
	writer, binding := writerFixture(t, store, "precision", false)
	attach204(t, binding, 5)
	for _, raw := range []string{`{"n":9007199254740992}`, `{"n":9007199254740993}`} {
		_, err := InvokeDurable(context.Background(), binding, "host", "source", "lookup", []byte(`{}`), true, func([]byte) (json.RawMessage, error) { return json.RawMessage(raw), nil }, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
	}
	if writer.RepeatState(*binding).Count != 0 {
		t.Fatal("precision-changing raw result counted as repeat")
	}
	head, _ := store.Load(context.Background(), "precision")
	cp := MustParseJSON(string(head.Checkpoint))
	rows := cp.Get("durable").Get("dispatches").Items()
	if rows[1].Get("repeatResultJson").Str() != `{"n":9007199254740993}` {
		t.Fatal("original result precision lost before custody")
	}
	resumed, held := writerFixture(t, store, "precision", true)
	attach204(t, held, 5)
	_, err := InvokeDurable(context.Background(), held, "host", "source", "lookup", []byte(`{}`), true, func([]byte) (json.RawMessage, error) { return json.RawMessage(`{"n":9007199254740993}`), nil }, nil, nil)
	if err != nil || resumed.RepeatState(*held).Count != 1 {
		t.Fatal("exact result custody failed after resume", err)
	}
	for _, args := range []string{`{"n":9007199254740992}`, `{"n":9007199254740993}`} {
		if _, err := invoke204(held, args, 1); err != nil {
			t.Fatal(err)
		}
	}
	if resumed.RepeatState(*held).Count != 1 {
		t.Fatal("precision-changing arguments counted as repeat")
	}
}
func TestRepeat204StartupCheckpointBeforeDispatch(t *testing.T) {
	store := NewMemoryRunStore(MemoryRunStoreOptions{})
	session, _ := trackedSession(t)
	session.Policy = PolicyLayer{"version": 2, "repeatTools": map[string]any{"mode": "on"}}
	session.Context.RunStore = &DurableRunInput{Store: store, ID: "startup"}
	first := openRun(t, session)
	head, err := store.Load(context.Background(), "startup")
	if err != nil || head == nil {
		t.Fatal("startup checkpoint missing", err)
	}
	cp, err := ReadExecution(MustParseJSON(string(head.Checkpoint)))
	if err != nil || cp.Version != 3 || cp.RepeatTools == nil || len(cp.RepeatTools.Executions) != 1 || cp.RepeatTools.Executions[0].Count != 0 {
		t.Fatal("empty repeat state not acknowledged before dispatch", err)
	}
	first.Context.Close()
	session.Context.RunStore.Resume = true
	resumed := openRun(t, session)
	defer resumed.Context.Close()
	binding := resumed.Context.Persistence("author")
	if _, err := invoke204(binding, `{}`, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := invoke204(binding, `{}`, 1); err != nil {
		t.Fatal(err)
	}
	if binding.Writer.RepeatState(*binding).Count != 1 {
		t.Fatal("startup resume lost admitted repeat scope")
	}
}
