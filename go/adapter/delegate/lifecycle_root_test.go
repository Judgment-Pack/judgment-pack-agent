package delegate_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

func TestLifecycleCoordinatorKeepsRootRunningAtEveryDispatch(t *testing.T) {
	ctx := context.Background()
	memory := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
	store := lifecycleStore{RunStore: memory}
	claims := 0
	store.commit = func(ctx context.Context, in runcontext.RunCommit) (runcontext.RunReceipt, error) {
		var cp contract.Checkpoint
		if err := json.Unmarshal(in.Checkpoint, &cp); err != nil {
			return runcontext.RunReceipt{}, err
		}
		if cp.Handoff != nil && cp.Handoff.State != "continued" && cp.Status != "running" {
			t.Fatalf("coordinator event published terminal root during handoff: status=%s state=%s", cp.Status, cp.Handoff.State)
		}
		if cp.Durable != nil {
			for _, row := range cp.Durable.Dispatches {
				if row.State == "claimed" {
					claims++
					if cp.Status != "running" {
						t.Fatalf("claimed dispatch published terminal root: status=%s handoff=%v scope=%+v", cp.Status, cp.Handoff, row.Scope)
					}
				}
			}
		}
		return memory.Commit(ctx, in)
	}
	calls := 0
	releases := 0
	provider := func(ctx context.Context, request adapter.AgentRequest, cb adapter.AgentCallbacks) error {
		calls++
		head, err := memory.Load(context.Background(), "coordinator-root")
		if err != nil || head == nil {
			t.Fatal("dispatch has no acknowledged root")
		}
		var cp contract.Checkpoint
		if err = json.Unmarshal(head.Checkpoint, &cp); err != nil || cp.Status != "running" {
			t.Fatalf("physical dispatch %d root=%s err=%v", calls, cp.Status, err)
		}
		defer func() {
			if cb.ProviderRelease != nil && cb.ProviderRelease.Acknowledge(cb.ProviderRelease.Invocation) {
				releases++
			}
		}()
		if calls == 1 {
			return lifecycleDelegate(ctx, request, cb)
		}
		return cb.Event(ctx, adapter.AgentEvent{Type: "message", ID: "answer", Text: "Exact child findings.", Phase: "final"})
	}
	lifecycleExecute(t, ctx, lifecycleRun(store, "coordinator-root", false, provider, func(e contract.RunEvent) {
		if e.Error != nil {
			t.Fatalf("handoff error: %s", e.Error.Message)
		}
	}))
	if calls != 3 || releases != 3 || claims < 3 {
		t.Fatalf("parent/child/parent calls=%d releases=%d claims=%d", calls, releases, claims)
	}
	head, _ := memory.Load(ctx, "coordinator-root")
	var cp contract.Checkpoint
	_ = json.Unmarshal(head.Checkpoint, &cp)
	if cp.Status != "complete" || cp.Handoff == nil || cp.Handoff.State != "continued" || cp.Handoff.Continuation == nil || len(cp.Durable.Children) != 1 {
		t.Fatal("handoff/child continuation lost")
	}
	child := cp.Durable.Children[0]
	if child.Scope.Execution != child.Checkpoint.ID || (child.Scope.Task == nil || *child.Scope.Task != child.Checkpoint.ID) || child.Checkpoint.Status != "complete" {
		t.Fatal("exact child checkpoint scope lost")
	}
}
