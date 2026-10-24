package runcontext

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

func writerCheckpoint(id string) contract.Checkpoint {
	return contract.Checkpoint{Version: 2, ID: id, RequestKey: strings.Repeat("a", 64), Phase: "work", Status: "running", Unit: "model-requests", Used: 0, Limit: 20, Grants: []contract.CheckpointGrant{}, Plan: []contract.CheckpointTaskStep{}, Explanation: "", Operations: []contract.CheckpointOperation{}, Context: contract.CheckpointContext{Scope: "fixture", Entries: []contract.CheckpointRetainedEntry{}}}
}
func writerFixture(t *testing.T, store RunStore, id string, resume bool) (*RunWriter, *RunBinding) {
	t.Helper()
	ctx := context.Background()
	var head *RunHead
	var err error
	if resume {
		head, err = store.Load(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
	}
	writer, err := NewRunWriter(store, id, head, nil)
	if err != nil {
		t.Fatal(err)
	}
	state := writerCheckpoint(id)
	if saved := writer.Checkpoint(); saved != nil {
		state = *saved
	}
	writer.Bind(func() (contract.Checkpoint, error) { return state, nil }, RecordDefaults, nil)
	return writer, &RunBinding{Writer: writer, Scope: DurableScope{Execution: id, Role: "author"}, Root: true}
}
func TestRunWriterAcknowledgedScopeAndNoRecoveredHostReplay(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryRunStore(MemoryRunStoreOptions{})
	writer, binding := writerFixture(t, store, "logical-execution", false)
	calls := 0
	execute := func([]byte) (ToolResult, error) {
		calls++
		return ReadToolResult(MustParseJSON(`{"content":[{"type":"text","text":"saved"}],"opaque":{"nested":[null,0]}}`)), nil
	}
	artifacts := func(ToolResult) []ArtifactObservation {
		return []ArtifactObservation{{Kind: "artifact", ID: "opaque-artifact", Digest: "sha256:" + strings.Repeat("b", 64)}}
	}
	if _, err := InvokeDurable(ctx, binding, "host", "admitted-source", "write", []byte(`{"value":1}`), false, execute, func(r ToolResult) bool { return !r.Err() }, artifacts); err != nil {
		t.Fatal(err)
	}
	head, _ := store.Load(ctx, writer.ID)
	value, _ := ParseJSON(head.Checkpoint)
	if len(value.Get("durable").Get("observations").Items()) != 2 {
		t.Fatal("trusted operation/artifact observations were not committed")
	}
	_, resumed := writerFixture(t, store, writer.ID, true)
	result, err := InvokeDurable(ctx, resumed, "host", "admitted-source", "write", []byte(`{"value":1}`), false, execute, nil, nil)
	if err != nil || calls != 1 || result.Value().Get("opaque") == nil {
		t.Fatalf("recovered replay/result preservation: calls=%d err=%v", calls, err)
	}
	// Same bytes in another trusted role/task are a distinct operation and receive their own result.
	child := *resumed
	child.Scope = DurableScope{Execution: "child-execution", Role: "author", Task: "child-task"}
	child.Root = false
	if _, err = InvokeDurable(ctx, &child, "host", "admitted-source", "write", []byte(`{"value":1}`), false, execute, nil, nil); err != nil || calls != 2 {
		t.Fatalf("cross-child result reuse: %d %v", calls, err)
	}
	fresh, err := NewRunWriter(store, "new-explicit-run", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	fresh.Bind(func() (contract.Checkpoint, error) { return writerCheckpoint("new-explicit-run"), nil }, RecordDefaults, nil)
	newBinding := &RunBinding{Writer: fresh, Scope: DurableScope{Execution: "new-explicit-run", Role: "author"}, Root: true}
	if _, err = InvokeDurable(ctx, newBinding, "host", "admitted-source", "write", []byte(`{"value":1}`), false, execute, nil, nil); err != nil || calls != 3 {
		t.Fatalf("explicit new run authority was deduplicated: %d %v", calls, err)
	}
}
func TestRunWriterUnknownHostOutcomeNeverReplays(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryRunStore(MemoryRunStoreOptions{})
	_, binding := writerFixture(t, store, "unknown-run", false)
	calls := 0
	execute := func([]byte) (ToolResult, error) {
		calls++
		return ToolResult{}, errors.New("uncertain external outcome")
	}
	if _, err := InvokeDurable(ctx, binding, "host", "source", "write", []byte(`{}`), false, execute, nil, nil); err == nil {
		t.Fatal("missing original failure")
	}
	_, resumed := writerFixture(t, store, "unknown-run", true)
	_, err := InvokeDurable(ctx, resumed, "host", "source", "write", []byte(`{}`), false, execute, nil, nil)
	var unknown *HostOutcomeUnknown
	if !errors.As(err, &unknown) || calls != 1 {
		t.Fatalf("unknown call repeated: %d %v", calls, err)
	}
}
func TestRunWriterEventsHaveTrustedScopeAndBoundedEnvelope(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryRunStore(MemoryRunStoreOptions{})
	writer, binding := writerFixture(t, store, "event-run", false)
	if _, err := writer.Event(ctx, *binding, contract.RunEvent{Message: &contract.RunEventMessage{Type: "message", Text: "done"}}); err != nil {
		t.Fatal(err)
	}
	page, err := store.ReadEvents(ctx, "event-run", "", 1)
	if err != nil || len(page.Items) != 1 {
		t.Fatal(err)
	}
	envelope, err := ReadStoredRunEvent(page.Items[0].Bytes)
	if err != nil || envelope.Scope != binding.Scope || envelope.Event.Message == nil {
		t.Fatalf("envelope: %+v %v", envelope, err)
	}
	var raw map[string]json.RawMessage
	if err = json.Unmarshal(page.Items[0].Bytes, &raw); err != nil {
		t.Fatal(err)
	}
	raw["invented"] = json.RawMessage(`true`)
	broken, _ := json.Marshal(raw)
	if _, err = ReadStoredRunEvent(broken); err == nil {
		t.Fatal("unknown envelope field accepted")
	}
}

func TestRunWriterSharedCurrentVectorsAndRecursiveBound(t *testing.T) {
	raw, err := os.ReadFile("../../contract/vectors/storage/checkpoints.json")
	if err != nil {
		t.Fatal(err)
	}
	var vector struct{ Items []json.RawMessage }
	if err = json.Unmarshal(raw, &vector); err != nil {
		t.Fatal(err)
	}
	for _, item := range vector.Items {
		v, e := ParseJSON(item)
		if e != nil {
			t.Fatal(e)
		}
		if saved, e := ReadExecution(v); e != nil || saved == nil {
			t.Fatalf("shared vector: %v", e)
		}
	}
	nested := MustParseJSON(string(vector.Items[0]))
	for i := 0; i < 6; i++ {
		nested = ObjectOf("version", Number(2), "id", String("storage-fixture"), "requestKey", String(strings.Repeat("0", 64)), "phase", String("work"), "status", String("running"), "unit", String("model-requests"), "used", Number(0), "limit", Number(20), "grants", Array(), "plan", Array(), "explanation", String(""), "operations", Array(), "context", ObjectOf("scope", String("fixture"), "entries", Array()), "durable", ObjectOf("dispatches", Array(), "observations", Array(), "children", Array(ObjectOf("scope", ObjectOf("execution", String("storage-fixture"), "role", String("author")), "checkpoint", nested))))
	}
	if contract.CheckRecord("checkpoint", nested.JSON()) == nil {
		t.Fatal("unbounded structural record recursion")
	}
}
func TestRunWriterExactPreparedBytesAndUnknownClaim(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryRunStore(MemoryRunStoreOptions{})
	_, binding := writerFixture(t, store, "prepared", false)
	exact := []byte(`{ "suffix": "/call", "body":"{ \"value\": 1 }" }`)
	_, err := InvokeDurable(ctx, binding, "model", "source", "fixture", exact, true, func([]byte) (struct{}, error) { return struct{}{}, errors.New("interrupted") }, nil, nil)
	if err == nil {
		t.Fatal("missing interruption")
	}
	_, resumed := writerFixture(t, store, "prepared", true)
	held, err := PrepareDurableRequest(resumed, "model", "source", "fixture", []byte(`{}`))
	if err != nil || string(held) != string(exact) {
		t.Fatalf("exact bytes lost: %q %v", held, err)
	}
	if _, err = PrepareDurableRequest(resumed, "model", "changed-source", "fixture", []byte(`{}`)); err == nil {
		t.Fatal("changed source continued")
	}
	calls := 0
	if _, err = InvokeDurable(ctx, resumed.WithRole("critic"), "model", "source", "fixture", []byte(`{"body":"{\"type\":\"reasoning\",\"text\":\"private\"}"}`), true, func([]byte) (struct{}, error) { calls++; return struct{}{}, nil }, nil, nil); err == nil || calls != 0 {
		t.Fatal("private reasoning retained/dispatched")
	}
}
func TestRunWriterEveryCommitInterruptionNeverRepeatsHost(t *testing.T) {
	for _, after := range []bool{false, true} {
		for at := 1; at <= 4; at++ {
			ordinal, armed := 0, true
			fault := func() error {
				ordinal++
				if armed && ordinal == at {
					armed = false
					return errors.New("injected acknowledgement failure")
				}
				return nil
			}
			options := MemoryRunStoreOptions{}
			if after {
				options.AfterCommit = fault
			} else {
				options.BeforeCommit = fault
			}
			store := NewMemoryRunStore(options)
			_, binding := writerFixture(t, store, "faulted", false)
			calls := 0
			call := func([]byte) (ToolResult, error) { calls++; return ReadToolResult(MustParseJSON(`{"content":[]}`)), nil }
			_ = binding.Writer.Save(context.Background(), *binding, writerCheckpoint("faulted"))
			_, _ = InvokeDurable(context.Background(), binding, "host", "source", "write", []byte(`{}`), false, call, nil, nil)
			_, _ = binding.Writer.Event(context.Background(), *binding, contract.RunEvent{End: &contract.RunEventEnd{Type: "end"}})
			armed = false
			head, _ := store.Load(context.Background(), "faulted")
			_, resumed := writerFixture(t, store, "faulted", head != nil)
			_, _ = InvokeDurable(context.Background(), resumed, "host", "source", "write", []byte(`{}`), false, call, nil, nil)
			if calls > 1 {
				t.Fatalf("host repeated: after=%v at=%d", after, at)
			}
		}
	}
}

type writerReceiptStore struct {
	RunStore
	write func(context.Context, RunCommit) (RunReceipt, error)
}

func (s writerReceiptStore) Commit(ctx context.Context, in RunCommit) (RunReceipt, error) {
	return s.write(ctx, in)
}
func TestRunWriterRejectsMalformedReceiptAndCurrentUnknownRepeat(t *testing.T) {
	ctx := context.Background()
	memory := NewMemoryRunStore(MemoryRunStoreOptions{})
	store := writerReceiptStore{RunStore: memory, write: func(ctx context.Context, in RunCommit) (RunReceipt, error) {
		receipt, err := memory.Commit(ctx, in)
		receipt.CommitID = "wrong-transaction"
		return receipt, err
	}}
	_, binding := writerFixture(t, store, "bad-receipt", false)
	calls := 0
	_, err := InvokeDurable(ctx, binding, "host", "source", "write", []byte(`{}`), false, func([]byte) (ToolResult, error) { calls++; return ToolResult{}, nil }, nil, nil)
	var invalid *StorageAcknowledgementInvalid
	if !errors.As(err, &invalid) || calls != 0 {
		t.Fatalf("malformed receipt dispatched: %d %v", calls, err)
	}
	_, resumed := writerFixture(t, memory, "bad-receipt", true)
	_, err = InvokeDurable(ctx, resumed, "host", "source", "write", []byte(`{}`), false, func([]byte) (ToolResult, error) { calls++; return ToolResult{}, nil }, nil, nil)
	var unknown *HostOutcomeUnknown
	if !errors.As(err, &unknown) || calls != 0 {
		t.Fatalf("unknown claim repeated: %d %v", calls, err)
	}
	named := failure.Normalize(err, "tool", time.Now())
	if named.Detail == nil || !strings.Contains(*named.Detail, "HostOutcomeUnknown") || named.Outcome != "unknown" {
		t.Fatalf("named unknown disposition lost: %+v", named)
	}
}
func TestRunWriterCancellationKeepsCommittedClaimWithoutDispatch(t *testing.T) {
	memory := NewMemoryRunStore(MemoryRunStoreOptions{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := writerReceiptStore{RunStore: memory, write: func(ctx context.Context, in RunCommit) (RunReceipt, error) {
		receipt, err := memory.Commit(ctx, in)
		cancel()
		return receipt, err
	}}
	_, binding := writerFixture(t, store, "cancelled-claim", false)
	calls := 0
	_, err := InvokeDurable(ctx, binding, "host", "source", "write", []byte(`{}`), false, func([]byte) (ToolResult, error) { calls++; return ToolResult{}, nil }, nil, nil)
	if err == nil || calls != 0 {
		t.Fatalf("cancelled acknowledgement dispatched: %d %v", calls, err)
	}
	head, _ := memory.Load(context.Background(), "cancelled-claim")
	if head == nil {
		t.Fatal("lost uncertain committed claim")
	}
}

// A failed outcome save cannot downgrade into a raw error during later event publication.
func TestRunWriterOutcomeAcknowledgementFailureRemainsNamedAndStopped(t *testing.T) {
	ctx := context.Background()
	injected := errors.New("private host storage detail")
	commits := 0
	store := NewMemoryRunStore(MemoryRunStoreOptions{AfterCommit: func() error {
		commits++
		if commits == 2 {
			return injected
		}
		return nil
	}})
	writer, binding := writerFixture(t, store, "sticky-uncertainty", false)
	calls := 0
	_, err := InvokeDurable(ctx, binding, "host", "source", "write", []byte(`{}`), false, func([]byte) (ToolResult, error) { calls++; return ResultOf([]Content{TextContent("saved")}, nil), nil }, nil, nil)
	var unknown *StorageOutcomeUnknown
	if !errors.As(err, &unknown) || unknown.cause != injected || !writer.Failed() {
		t.Fatalf("outcome did not retain named fatal and private cause: %T", err)
	}
	_, later := writer.Event(ctx, *binding, contract.RunEvent{Message: &contract.RunEventMessage{Type: "message", Text: "not published"}})
	if later != err || commits != 2 || calls != 1 {
		t.Fatal("failed writer retried or replaced its sticky uncertainty")
	}
	classified := failure.Normalize(later, "storage", time.Now())
	if classified.Code != "storage" || classified.Outcome != "unknown" || classified.Detail == nil || *classified.Detail != unknown.Error() || strings.Contains(*classified.Detail, injected.Error()) {
		t.Fatal("later event leaked raw detail or lost named uncertainty")
	}
	_, resumed := writerFixture(t, store, "sticky-uncertainty", true)
	_, err = InvokeDurable(ctx, resumed, "host", "source", "write", []byte(`{}`), false, func([]byte) (ToolResult, error) { calls++; return ToolResult{}, nil }, nil, nil)
	if err != nil || calls != 1 {
		t.Fatalf("acknowledgement loss replayed a committed host result: calls=%d err=%v", calls, err)
	}
}
