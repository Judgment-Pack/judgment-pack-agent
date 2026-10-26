package delegate_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/turn"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

func TestStorageNeverAdmittedChildDoesNotClaimAnotherOwnerRelease(t *testing.T) {
	memory := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
	parent, children := 0, 0
	// This fixture represents a separate account owner's resource. A failed admission
	// must not release it; only this test's owner may close it after the assertion.
	otherOwner := make(chan struct{})
	tokens := map[*adapter.ProviderReleaseInvocation]bool{}
	provider := func(ctx context.Context, request adapter.AgentRequest, cb adapter.AgentCallbacks) error {
		if strings.Contains(request.Prompt, turn.ChildRules) {
			children++
			if tokens[cb.ProviderRelease.Invocation] {
				t.Fatal("admission attempt reused identity")
			}
			tokens[cb.ProviderRelease.Invocation] = true
			if !cb.ProviderRelease.NotAdmitted(cb.ProviderRelease.Invocation) {
				t.Fatal("exact pre-admission refusal rejected")
			}
			return adapter.ErrBusy
		}
		parent++
		defer cb.ProviderRelease.Acknowledge(cb.ProviderRelease.Invocation)
		if parent == 1 {
			return lifecycleDelegate(ctx, request, cb)
		}
		return cb.Event(ctx, adapter.AgentEvent{Type: "message", ID: "final", Text: "The child account was busy; no child started.", Phase: "final"})
	}
	var events []contract.RunEvent
	lifecycleExecute(t, context.Background(), lifecycleRun(memory, "never-admitted-child", false, provider, func(e contract.RunEvent) { events = append(events, e) }))
	if parent != 2 || children != 3 {
		t.Fatalf("unexpected actual invocation counts parent=%d child=%d", parent, children)
	}
	select {
	case <-otherOwner:
		t.Fatal("failed admission closed another owner")
	default:
	}
	for _, e := range events {
		if e.Error != nil && strings.Contains(e.Error.Message, "ProviderReleaseUnknown:") {
			t.Fatal("known child admission refusal poisoned account release")
		}
	}
	head, err := memory.Load(context.Background(), "never-admitted-child")
	if err != nil || head == nil {
		t.Fatal("missing durable child disposition")
	}
	var cp contract.Checkpoint
	if err = json.Unmarshal(head.Checkpoint, &cp); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range cp.Durable.Dispatches {
		if row.Scope.Task != nil && *row.Scope.Task != "" && row.Failure != nil && row.Failure.Outcome == "not-started" {
			found = true
		}
	}
	if !found {
		t.Fatalf("typed child not-started outcome not retained: %s", head.Checkpoint)
	}
	before := children
	lifecycleExecute(t, context.Background(), lifecycleRun(memory, "never-admitted-child", true, provider, func(contract.RunEvent) {}))
	if children != before {
		t.Fatal("restore redispatched the never-admitted child")
	}
	close(otherOwner)
}
