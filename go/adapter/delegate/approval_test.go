package delegate_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/delegate"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
)

// On the agent capability an act call pauses the response and the next counted invocation continues
// it (ADR-0008), so the coordinator offers a parent invocation an act only while that invocation
// remains beside it, as delegate_tasks is offered only while its batch's invocations remain: after
// two of the response's five invocations the act is offered, after four it is not.
func TestAnActIsOfferedOnlyWhileTheContinuationsInvocationRemains(t *testing.T) {
	act := runcontext.HostTool{Name: "create_issue", Description: "Open an issue.", InputSchema: json.RawMessage(`{"type":"object"}`),
		Source:  &runcontext.ToolSource{Kind: "custom", ID: "tracker", Trust: "person", Version: "1"},
		Approve: func(runcontext.ApprovalRequest) string { return "allow" },
		Execute: func(context.Context, json.RawMessage) (runcontext.ToolResult, error) {
			return runcontext.ToolResult{}, errors.New("not run here")
		}}
	read := runcontext.HostTool{Name: "lookup", Effect: "read", Description: "Look up.", InputSchema: json.RawMessage(`{"type":"object"}`),
		Execute: func(context.Context, json.RawMessage) (runcontext.ToolResult, error) {
			return runcontext.ToolResult{}, nil
		}}
	for _, held := range []struct {
		runs  int64
		offer bool
	}{{0, true}, {2, true}, {4, false}} {
		var offered []string
		inner := adapter.Func(func(_ context.Context, run *adapter.Run) error {
			for _, tool := range run.HostTools {
				offered = append(offered, tool.Name)
			}
			run.Emit(contract.RunEvent{End: &contract.RunEventEnd{Type: "end"}})
			return nil
		})
		runs := held.runs
		saved := &contract.Checkpoint{Version: 1, ID: "x", RequestKey: "k", Phase: "work", Status: "running", Unit: "provider-managed", Limit: 1,
			Delegation: &contract.ProvenanceDelegationReport{Batches: []contract.ProvenanceBatchFigures{}, Parent: contract.ProvenanceParentFigures{Model: "agent-model", AgentRuns: &runs}}}
		run := &adapter.Run{AllowConversation: true, Interactive: true, HostTools: []runcontext.HostTool{act, read},
			Context: &runcontext.Input{Execution: saved}, Emit: func(contract.RunEvent) {},
			Children: func(context.Context, contract.DelegatedTaskProfile, func(contract.RunEvent)) (*gate.Calls, []contract.ToolOfferRuntimeTool, error) {
				return nil, nil, errors.New("no child here")
			},
			Agent: &adapter.Agent{Model: "agent-model", Run: func(context.Context, adapter.AgentRequest, adapter.AgentCallbacks) error { return nil }}}
		if err := delegate.With(inner, delegate.Options{}).Run(context.Background(), run); err != nil {
			t.Fatal(err)
		}
		if slices.Contains(offered, "create_issue") != held.offer || !slices.Contains(offered, "lookup") {
			t.Errorf("after %d invocations the parent was offered %v", held.runs, offered)
		}
	}
}
