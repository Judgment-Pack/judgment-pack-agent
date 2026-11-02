package principaltests

// #275 with ADR-0029: a delegated run's checkpoint keeps its version when a held call keeps its text. Version 4 holds a
// principal, 5 a text, 6 both, so the text's format can neither drop the principal nor be dropped itself.

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

func TestAHeldCallKeepsItsTextWithoutLoweringADelegatedRun(t *testing.T) {
	for _, engine := range hostEngines {
		for _, row := range []struct {
			name, call string
			delegated  bool
			version    int64
			kept       bool
		}{
			{"delegated, a text it must keep", `{"id":9007199254740993}`, true, 6, true},
			{"delegated, no text", `{"id":7}`, true, 4, false},
			{"undelegated, a text it must keep", `{"id":9007199254740993}`, false, 5, true},
			{"undelegated, no text", `{"id":7}`, false, 2, false},
		} {
			t.Run(engine+"/"+row.name, func(t *testing.T) {
				var executed atomic.Int32
				tool := delegatedWriteTool(&executed)
				tool.Approve = func(runcontext.ApprovalRequest) string { return "ask" }
				ran := []string{}
				tool.Execute = func(_ context.Context, raw json.RawMessage) (runcontext.ToolResult, error) {
					ran = append(ran, string(raw))
					return runcontext.ResultOf([]runcontext.Content{runcontext.TextContent("ran " + string(raw))}, nil), nil
				}
				if row.delegated {
					hostDelegation = runtimePrincipal(t, 0, runtimeNow, &runtimePrincipalPort{}, func(runcontext.PrincipalAdmissionRead) string { return runtimeAdmit })
					t.Cleanup(func() { hostDelegation = nil })
				}
				store := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
				first := runHostTool(t, store, "run-held", engine, false, nil, tool, []string{row.call})
				if first.err != nil {
					t.Fatal(first.err)
				}
				head, err := store.Load(context.Background(), "run-held")
				if err != nil || head == nil {
					t.Fatal("no head", err)
				}
				var saved contract.Checkpoint
				if err = json.Unmarshal(head.Checkpoint, &saved); err != nil {
					t.Fatal(err)
				}
				if saved.Version != row.version || (saved.Principal != nil) != row.delegated || saved.Approval == nil || (saved.Approval.Request != nil) != row.kept {
					t.Fatalf("version %d, principal %v, approval %+v", saved.Version, saved.Principal != nil, saved.Approval)
				}
				// Each of the two is refused if it is taken away from the record that holds it.
				raw := runcontext.MustParseJSON(string(head.Checkpoint))
				if _, err = runcontext.ReadExecution(raw); err != nil {
					t.Fatal(err)
				}
				var loose map[string]any
				if err = json.Unmarshal(head.Checkpoint, &loose); err != nil {
					t.Fatal(err)
				}
				if row.delegated {
					delete(loose, "principal")
					dropped, _ := json.Marshal(loose)
					if _, err = runcontext.ReadExecution(runcontext.MustParseJSON(string(dropped))); err == nil {
						t.Fatal("a delegated checkpoint with no principal reads")
					}
				}
				digest := runcontext.ApprovalShownDigest(*saved.Approval)
				second := runHostTool(t, store, "run-held", engine, true, &contract.CheckpointApprovalDecision{CallID: saved.Approval.CallID, Approved: true, ShownDigest: &digest}, tool, nil)
				if second.err != nil {
					t.Fatal(second.err)
				}
				if strings.Join(ran, " ") != row.call {
					t.Fatalf("the tool ran %q", ran)
				}
			})
		}
	}
}
