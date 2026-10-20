package certification_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/agent"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/delegate"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/turn"
	"github.com/Judgment-Pack/judgment-pack-agent/go/certification"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

func withDelegation(inner adapter.Adapter) adapter.Adapter {
	return delegate.With(inner, delegate.Options{})
}

// Scenarios 3, 7, 8, 9 and 10 on the agent capability, against the shared server's recorded
// runtime, on the stand-in.
func TestScenariosThreeSevenEightNineAndTenHoldOnTheAgentCapability(t *testing.T) {
	for _, check := range certification.AgentChecks {
		if !slices.Contains([]string{"scenario 3", "scenario 7", "scenario 8", "scenario 9", "scenario 10"}, check.Group) {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		findings, err := check.Run(ctx, server, certification.StandIn(), withDelegation, agent.New())
		if err != nil {
			t.Fatal(err)
		}
		for _, finding := range findings {
			t.Error(finding)
		}
	}
}

// naive is the wrapper agent#67 warns of: delegate_tasks offered as an awaited host tool whose call
// runs each child inside the parent's tool callback, while the parent's invocation still holds the
// account.
func naive(inner adapter.Adapter) adapter.Adapter {
	return adapter.Func(func(ctx context.Context, run *adapter.Run) error {
		declared := turn.DelegationTool(turn.TaskProfiles)
		tool := runcontext.HostTool{Name: declared.Name, Description: declared.Description, InputSchema: declared.InputSchema,
			Execute: func(ctx context.Context, args json.RawMessage) (runcontext.ToolResult, error) {
				request, err := turn.ReadDelegationRequest(args)
				if err != nil {
					return runcontext.ToolResult{}, err
				}
				for _, item := range request.Tasks {
					child := *run
					child.Prompt = turn.ChildRules + " " + item.Objective
					child.HostTools, child.Children, child.Context = nil, nil, &runcontext.Input{Objective: item.Objective}
					child.Emit = func(contract.RunEvent) {}
					_ = inner.Run(ctx, &child)
				}
				return runcontext.ResultOf([]runcontext.Content{runcontext.TextContent("The children ran.")}, nil), nil
			}}
		parent := *run
		parent.HostTools = append(append([]runcontext.HostTool{}, run.HostTools...), tool)
		return inner.Run(ctx, &parent)
	})
}

// The fake exclusive account catches the naive synchronous wrapper: a child started inside the
// parent's callback asks for the account while the parent holds it, and the exclusive and serial
// checks fail.
func TestTheFakeAccountCatchesANaiveSynchronousWrapper(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	findings, err := certification.AgentChecks[0].Run(ctx, server, certification.StandIn(), naive, agent.New())
	if err != nil {
		t.Fatal(err)
	}
	var failed []string
	for _, finding := range findings {
		failed = append(failed, finding.Check)
	}
	for _, check := range []string{"agent exclusive", "delegation serial", "agent handoff"} {
		if !slices.Contains(failed, check) {
			t.Errorf("the naive wrapper passed %q; the findings were %v", check, findings)
		}
	}
	exclusive := ""
	for _, finding := range findings {
		if finding.Check == "agent exclusive" {
			exclusive = finding.Detail
		}
	}
	if !strings.Contains(exclusive, "child invocation started while another was still running") {
		t.Errorf("the exclusive check said %q", exclusive)
	}
}
