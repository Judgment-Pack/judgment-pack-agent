package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

// ADR-0008 (the v0.5.0 release review, #83 row 5): no invocation is dispatched while an act call
// waits for the person's decision. drive never reaches invoke in that state (the pause ends the
// invocation before a reformat or a critic, and a continuation of an asked approval without a
// decision is refused before the run opens), so the guard is defence in depth; this test holds it
// directly, on a run whose approval is asked.
func TestNoInvocationIsDispatchedWhileAnApprovalIsAsked(t *testing.T) {
	opened, err := runcontext.ContextualSession(context.Background(), runcontext.Session{Prompt: "Current request",
		Tools: []runcontext.RuntimeTool{}, HostTools: []runcontext.HostTool{},
		CallTool: func(context.Context, string, json.RawMessage) (runcontext.ToolResult, error) {
			return runcontext.ResultOf(nil, nil), nil
		},
		Agent: &runcontext.AgentSession{Model: "fixture"}})
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Context.Close()
	opened.Context.Execution.MutateCheckpoint(runcontext.CheckpointPatch{"Approval": &contract.CheckpointApproval{CallID: "call-a", Name: "tracker__create_issue", Tool: "create_issue", State: "asked"}})
	dispatched := 0
	raw := &adapter.Run{Agent: &adapter.Agent{Model: "fixture",
		Run: func(context.Context, adapter.AgentRequest, adapter.AgentCallbacks) error {
			dispatched++
			return nil
		}}}
	r := &runState{sc: opened.Context, session: adapter.NewRequestSession(raw, opened.Context.ResolvedPolicy, opened).Prepared(), run: adapter.NewRequestSession(raw, opened.Context.ResolvedPolicy, opened)}
	for _, phase := range []string{"author", "critic"} {
		if _, err := r.invoke(context.Background(), "Current request", "", phase, nil, "", ""); !errors.Is(err, runcontext.ErrApprovalPaused) {
			t.Fatalf("%s: the invocation answered %v", phase, err)
		}
	}
	if dispatched != 0 {
		t.Fatalf("%d invocations were dispatched while the approval was asked", dispatched)
	}
}

// This isolates invoke's return: terminal event persistence must not be the only place
// a failed outcome acknowledgement takes precedence over an intentional handoff.
type outcomeFailureStore struct {
	runcontext.RunStore
	fail bool
}

func (s *outcomeFailureStore) Commit(ctx context.Context, in runcontext.RunCommit) (runcontext.RunReceipt, error) {
	receipt, err := s.RunStore.Commit(ctx, in)
	if err == nil && s.fail {
		return runcontext.RunReceipt{}, errors.New("private fixture storage cause")
	}
	return receipt, err
}
func TestInvocationDoesNotSuppressOutcomeUncertaintyDuringHandoff(t *testing.T) {
	ctx := context.Background()
	store := &outcomeFailureStore{RunStore: runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})}
	opened, err := runcontext.ContextualSession(ctx, runcontext.Session{Prompt: "Current request", Tools: []runcontext.RuntimeTool{}, HostTools: []runcontext.HostTool{},
		Context: &runcontext.Input{RunStore: &runcontext.DurableRunInput{Store: store, ID: "handoff-uncertainty"}},
		CallTool: func(context.Context, string, json.RawMessage) (runcontext.ToolResult, error) {
			return runcontext.ResultOf(nil, nil), nil
		}, Agent: &runcontext.AgentSession{Model: "fixture"}})
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Context.Close()
	var r *runState
	raw := &adapter.Run{Context: opened.Session.Context, Emit: func(contract.RunEvent) {}, Agent: &adapter.Agent{Model: "fixture", Run: func(context.Context, adapter.AgentRequest, adapter.AgentCallbacks) error {
		r.claimPause(true)
		store.fail = true
		return nil
	}}}
	prepared := adapter.NewRequestSession(raw, opened.Context.ResolvedPolicy, opened)
	r = &runState{sc: opened.Context, session: prepared.Prepared(), run: prepared}
	_, err = r.invoke(ctx, "Current request", "", "author", nil, "", "")
	var unknown *runcontext.StorageOutcomeUnknown
	if !errors.As(err, &unknown) {
		t.Fatalf("handoff suppressed outcome uncertainty: %v", err)
	}
	if !opened.Session.Context.Persistence.Writer.Failed() {
		t.Fatal("uncertain writer was reopened")
	}
}
