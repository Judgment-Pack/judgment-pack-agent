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
// waits for the person's decision. drive never reaches invoke in that state (the pause cancels the
// run's execution before a reformat or a critic, and a continuation of an asked approval without a
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
	opened.Context.Execution.State.Approval = &contract.CheckpointApproval{CallID: "call-a", Name: "tracker__create_issue", Tool: "create_issue", State: "asked"}
	dispatched := 0
	r := &runState{sc: opened.Context, session: opened.Session, run: &adapter.Run{Agent: &adapter.Agent{Model: "fixture",
		Run: func(context.Context, adapter.AgentRequest, adapter.AgentCallbacks) error {
			dispatched++
			return nil
		}}}}
	for _, phase := range []string{"author", "critic"} {
		if _, err := r.invoke(context.Background(), "Current request", "", phase, nil, "", ""); !errors.Is(err, runcontext.ErrApprovalPaused) {
			t.Fatalf("%s: the invocation answered %v", phase, err)
		}
	}
	if dispatched != 0 {
		t.Fatalf("%d invocations were dispatched while the approval was asked", dispatched)
	}
}
