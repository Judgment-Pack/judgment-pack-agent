package runcontext

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
)

func TestLifecycleStopFinalizesOriginalWriter(t *testing.T) {
	for _, mode := range []string{"dispatch-canceled", "callback-canceled", "explicit-stop", "verify-canceled"} {
		t.Run(mode, func(t *testing.T) {
			memory := NewMemoryRunStore(MemoryRunStoreOptions{})
			stops := 0
			store := writerReceiptStore{RunStore: memory, write: func(ctx context.Context, in RunCommit) (RunReceipt, error) {
				var cp contract.Checkpoint
				if err := json.Unmarshal(in.Checkpoint, &cp); err != nil {
					return RunReceipt{}, err
				}
				if cp.Status == "stopped" {
					stops++
					if len(in.Events) != 2 {
						t.Fatalf("stop transaction events=%d", len(in.Events))
					}
				}
				return memory.Commit(ctx, in)
			}}
			writer, binding := writerFixture(t, store, "cancel-finalize", false)
			exact := ReadToolResult(MustParseJSON(`{"content":[{"type":"text","text":"earlier exact result"}],"opaque":{"nested":[null,0,false]}}`))
			toolCalls := 0
			_, err := InvokeDurable(context.Background(), binding, "host", "source", "write", []byte(`{"value":1}`), false, func([]byte) (ToolResult, error) { toolCalls++; return exact, nil }, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			before := writer.Checkpoint().Durable.Dispatches[0]
			dispatch, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			_, err = InvokeDurableModel(dispatch, binding, "source", "fixture", []byte(`{"request":"exact"}`), func([]byte) (*http.Response, error) {
				calls++
				if mode == "explicit-stop" {
					if err := writer.CloseDispatch(*binding); err != nil {
						t.Fatal(err)
					}
				}
				if mode == "verify-canceled" {
					return &http.Response{StatusCode: 200}, nil
				}
				if mode != "callback-canceled" {
					cancel()
				}
				return nil, context.Canceled
			}, func(*http.Response) error {
				if mode == "verify-canceled" {
					cancel()
					return nil
				}
				t.Fatal("canceled model verified")
				return nil
			})
			var uncertain *StorageOutcomeUnknown
			if !failure.IsCancelled(err) || errors.As(err, &uncertain) || calls != 1 {
				t.Fatalf("cancellation poisoned persistence: calls=%d err=%v", calls, err)
			}
			// Actual adapters can attempt terminal delivery on their canceled dispatch context.
			if mode != "callback-canceled" {
				_, _ = writer.Event(dispatch, *binding, contract.RunEvent{End: &contract.RunEventEnd{Type: "end"}})
			}
			_, err = InvokeDurableModel(context.Background(), binding, "source", "fixture", []byte(`{}`), func([]byte) (*http.Response, error) { calls++; return nil, nil }, func(*http.Response) error { return nil })
			if err == nil || calls != 1 {
				t.Fatal("cancellation reopened dispatch")
			}
			if _, err = writer.Event(context.Background(), *binding, contract.RunEvent{End: &contract.RunEventEnd{Type: "end"}}); err == nil {
				t.Fatal("closed writer published an ordinary terminal event")
			}
			if err = writer.Save(context.Background(), *binding, writerCheckpoint(writer.ID)); err == nil {
				t.Fatal("closed writer published an ordinary checkpoint")
			}
			events, err := writer.FinalizeStop(context.Background(), *binding)
			if err != nil || len(events) != 2 || events[0].ExecutionCheckpoint == nil || events[1].End == nil {
				t.Fatalf("original writer did not finalize: events=%v err=%v", events, err)
			}
			saved := writer.Checkpoint()
			if stops != 1 || saved.Status != "stopped" || saved.Durable.Dispatches[1].State != "unknown" || !reflect.DeepEqual(saved.Durable.Dispatches[0], before) {
				t.Fatal("stop changed exact completed result or lost unknown claim")
			}
			page, err := memory.ReadEvents(context.Background(), writer.ID, "", 100)
			if err != nil || len(page.Items) != 2 {
				t.Fatalf("terminal log=%v err=%v", page, err)
			}
			for _, item := range page.Items {
				var env StoredRunEnvelope
				if err = json.Unmarshal(item.Bytes, &env); err != nil || env.Scope != binding.Scope {
					t.Fatal("terminal scope changed")
				}
			}
			head, _ := memory.Load(context.Background(), writer.ID)
			restored, err := NewRunWriter(memory, writer.ID, head, nil)
			if err != nil {
				t.Fatal(err)
			}
			if restored.Checkpoint().Status != "stopped" || restored.Checkpoint().Durable.Dispatches[1].State != "unknown" {
				t.Fatal("reload lost stopped unknown outcome")
			}
			next := *binding
			next.Writer = restored
			_, err = InvokeDurable(context.Background(), &next, "host", "source", "write", []byte(`{"value":1}`), false, func([]byte) (ToolResult, error) { toolCalls++; return exact, nil }, nil, nil)
			if err == nil || toolCalls != 1 {
				t.Fatal("terminal reload admitted physical replay")
			}
		})
	}
}

func TestLifecycleStopIdentityCASAndStorageFailure(t *testing.T) {
	for _, mode := range []string{"wrong-writer", "wrong-run", "child", "stale-revision", "lost-ack", "store-failure"} {
		t.Run(mode, func(t *testing.T) {
			memory := NewMemoryRunStore(MemoryRunStoreOptions{})
			armed := false
			writes := 0
			lost := errors.New("fixture storage outcome unavailable")
			store := writerReceiptStore{RunStore: memory, write: func(ctx context.Context, in RunCommit) (RunReceipt, error) {
				writes++
				if armed && mode == "store-failure" {
					return RunReceipt{}, lost
				}
				receipt, err := memory.Commit(ctx, in)
				if armed && mode == "lost-ack" && err == nil {
					return RunReceipt{}, lost
				}
				return receipt, err
			}}
			writer, binding := writerFixture(t, store, "stop-identity", false)
			if err := writer.Save(context.Background(), *binding, writerCheckpoint(writer.ID)); err != nil {
				t.Fatal(err)
			}
			target := *binding
			switch mode {
			case "wrong-writer":
				target.Writer, _ = NewRunWriter(memory, "other", nil, nil)
			case "wrong-run":
				target.Scope.Execution = "other"
			case "child":
				target.Root = false
				target.Scope.Task = "child"
			case "stale-revision":
				head, _ := memory.Load(context.Background(), writer.ID)
				if _, err := memory.Commit(context.Background(), RunCommit{RunID: writer.ID, ExpectedRevision: head.Revision, CommitID: "competing-commit", Checkpoint: head.Checkpoint}); err != nil {
					t.Fatal(err)
				}
			}
			before := writes
			armed = true
			_, err := writer.FinalizeStop(context.Background(), target)
			if err == nil {
				t.Fatal("invalid finalization accepted")
			}
			if mode == "wrong-writer" || mode == "wrong-run" || mode == "child" {
				if writes != before {
					t.Fatal("foreign binding touched store")
				}
				return
			}
			_, again := writer.FinalizeStop(context.Background(), *binding)
			if again != err || writes != before+1 {
				t.Fatalf("uncertain persistence was retried or cleared: first=%v next=%v writes=%d", err, again, writes-before)
			}
			if mode == "stale-revision" {
				var conflict *StorageConflict
				if !errors.As(err, &conflict) {
					t.Fatalf("CAS refusal lost type: %v", err)
				}
			}
		})
	}
}

func TestLifecycleStopDeadlineAndImmediateClosure(t *testing.T) {
	memory := NewMemoryRunStore(MemoryRunStoreOptions{})
	armed := false
	seenBound := false
	store := writerReceiptStore{RunStore: memory, write: func(ctx context.Context, in RunCommit) (RunReceipt, error) {
		if armed {
			deadline, ok := ctx.Deadline()
			seenBound = ok && time.Until(deadline) <= 5*time.Second
			<-ctx.Done()
			return RunReceipt{}, ctx.Err()
		}
		return memory.Commit(ctx, in)
	}}
	writer, binding := writerFixture(t, store, "bounded-stop", false)
	if err := writer.Save(context.Background(), *binding, writerCheckpoint(writer.ID)); err != nil {
		t.Fatal(err)
	}
	if err := writer.CloseDispatch(*binding); err != nil {
		t.Fatal(err)
	}
	calls := 0
	_, err := InvokeDurable(context.Background(), binding, "host", "source", "write", []byte(`{}`), false, func([]byte) (int, error) { calls++; return 1, nil }, nil, nil)
	if err == nil || calls != 0 {
		t.Fatal("CloseDispatch admitted work")
	}
	armed = true
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = writer.FinalizeStop(ctx, *binding)
	var unknown *StorageOutcomeUnknown
	if !errors.As(err, &unknown) || !seenBound || time.Since(start) > time.Second {
		t.Fatalf("finalization exceeded host bound: %v", err)
	}
	if writer.Checkpoint().Status != "running" {
		t.Fatal("failed stop fabricated acknowledged checkpoint")
	}
}

func TestLifecycleChildKeepsLatestAcknowledgedRoot(t *testing.T) {
	store := NewMemoryRunStore(MemoryRunStoreOptions{})
	writer, binding := writerFixture(t, store, "root-owner", false)
	stale := writerCheckpoint(writer.ID)
	stale.Status = "complete"
	writer.Bind(func() (contract.Checkpoint, error) { return stale, nil }, RecordDefaults, nil)
	active := writerCheckpoint(writer.ID)
	if err := writer.Save(context.Background(), *binding, active); err != nil {
		t.Fatal(err)
	}
	child := RunBinding{Writer: writer, Scope: DurableScope{Execution: "child", Role: "author", Task: "child"}}
	_, err := InvokeDurable(context.Background(), &child, "host", "source", "read", []byte(`{}`), true, func([]byte) (int, error) {
		if writer.Checkpoint().Status != "running" {
			t.Fatal("child claim replaced acknowledged root with stale snapshot")
		}
		return 1, nil
	}, nil, nil)
	if err != nil || writer.Checkpoint().Status != "running" {
		t.Fatalf("child result replaced acknowledged root: %v", err)
	}
	// A new root bind still carries legitimate continuation updates.
	fresh := writerCheckpoint(writer.ID)
	fresh.Used = 3
	writer.Bind(func() (contract.Checkpoint, error) { return fresh, nil }, RecordDefaults, nil)
	if _, err = writer.Event(context.Background(), *binding, contract.RunEvent{End: &contract.RunEventEnd{Type: "end"}}); err != nil || writer.Checkpoint().Used != 3 {
		t.Fatal("continuation live root update lost")
	}
}

func TestLifecycleStopHasIndependentFiveSecondCap(t *testing.T) {
	memory := NewMemoryRunStore(MemoryRunStoreOptions{})
	armed := false
	store := writerReceiptStore{RunStore: memory, write: func(ctx context.Context, in RunCommit) (RunReceipt, error) {
		if armed {
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) > 5*time.Second {
				t.Fatal("missing five-second persistence deadline")
			}
			return RunReceipt{}, errors.New("bounded fixture storage failure")
		}
		return memory.Commit(ctx, in)
	}}
	writer, binding := writerFixture(t, store, "stop-cap", false)
	if err := writer.Save(context.Background(), *binding, writerCheckpoint(writer.ID)); err != nil {
		t.Fatal(err)
	}
	armed = true
	_, _ = writer.FinalizeStop(context.Background(), *binding)
}

func TestLifecycleStopRejectsLateModelCompletion(t *testing.T) {
	store := NewMemoryRunStore(MemoryRunStoreOptions{})
	writer, binding := writerFixture(t, store, "late-completion", false)
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		_, err := InvokeDurableModel(context.Background(), binding, "source", "fixture", []byte(`{}`), func([]byte) (*http.Response, error) {
			close(entered)
			<-release
			return &http.Response{StatusCode: 200}, nil
		}, func(*http.Response) error { return nil })
		done <- err
	}()
	<-entered
	if _, err := writer.FinalizeStop(context.Background(), *binding); err != nil {
		t.Fatal(err)
	}
	before := writer.Checkpoint()
	close(release)
	if err := <-done; err == nil {
		t.Fatal("late model completion escaped stopped writer")
	}
	if !reflect.DeepEqual(before, writer.Checkpoint()) {
		t.Fatal("late model completion changed stopped checkpoint")
	}
	_, err := writer.Event(context.Background(), *binding, contract.RunEvent{End: &contract.RunEventEnd{Type: "end"}})
	var changed *ContinuationIdentityChanged
	if errors.As(err, &changed) {
		t.Fatal("late model completion poisoned original stopped writer")
	}
}

func TestLifecycleStopDeadlineIncludesBusyWriter(t *testing.T) {
	memory := NewMemoryRunStore(MemoryRunStoreOptions{})
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	store := writerReceiptStore{RunStore: memory, write: func(ctx context.Context, in RunCommit) (RunReceipt, error) {
		close(entered)
		<-release
		return memory.Commit(ctx, in)
	}}
	writer, binding := writerFixture(t, store, "busy-stop", false)
	go func() { done <- writer.Save(context.Background(), *binding, writerCheckpoint(writer.ID)) }()
	<-entered
	closed := make(chan error, 1)
	go func() { closed <- writer.CloseDispatch(*binding) }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		close(release)
		<-done
		t.Fatal("CloseDispatch waited behind a store commit")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	finished := make(chan error, 1)
	go func() { _, err := writer.FinalizeStop(ctx, *binding); finished <- err }()
	select {
	case err := <-finished:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("busy finalization: %v", err)
		}
	case <-time.After(time.Second):
		close(release)
		<-done
		t.Fatal("Stop deadline did not bound writer acquisition")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestLifecycleStopDoesNotRevivePendingHandoff(t *testing.T) {
	store := NewMemoryRunStore(MemoryRunStoreOptions{})
	writer, binding := writerFixture(t, store, "pending-stop", false)
	call := "delegate-wire"
	event := contract.RunEvent{DelegationRequested: &contract.RunEventDelegationRequested{Type: "delegation_requested", CallID: &call, Request: contract.DelegatedTaskRequest{Tasks: []contract.DelegatedTaskTaskRequest{{Title: "Review", Profile: "review", Objective: "Check claims."}}}}}
	if _, err := writer.Event(context.Background(), *binding, event); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.FinalizeStop(context.Background(), *binding); err != nil {
		t.Fatal(err)
	}
	if writer.Checkpoint().Status != "stopped" || writer.Checkpoint().Durable.PendingHandoff == nil {
		t.Fatal("pending handoff revived stopped root or erased release uncertainty")
	}
}
