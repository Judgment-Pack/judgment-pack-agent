package certification

import (
	"context"
	"encoding/json"
	"errors"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"testing"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

func TestApprovalParallelFixtureBindsEitherOriginalOwner(t *testing.T) {
	var file pauseFile
	if err := ScenarioFile("approval-pause", &file); err != nil {
		t.Fatal(err)
	}
	calls := file.Pausing[1].Calls
	for index, call := range calls {
		var args map[string]json.RawMessage
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			t.Fatal(err)
		}
		requested := &contract.RunEventApprovalRequested{Args: args}
		asked, pending, ok := pauseExpectedArguments(file, requested, true)
		if !ok || !sameJSON(asked, call.Arguments) || !sameJSON(pending, calls[1-index].Arguments) {
			t.Fatalf("parallel original owner %d lost exact argument binding", index)
		}
		_, _, ordered := pauseExpectedArguments(file, requested, false)
		if ordered != (index == 0) {
			t.Fatalf("model order changed for owner %d", index)
		}
	}
	unrelated := &contract.RunEventApprovalRequested{Args: map[string]json.RawMessage{"title": json.RawMessage(`"A call never offered"`)}}
	if _, _, ok := pauseExpectedArguments(file, unrelated, true); ok {
		t.Fatal("an approval for unoffered arguments became an owner")
	}
}

func TestRecordFixtureReadsOnlyTheVersionRegistry(t *testing.T) {
	// Both roots contain unrelated string enums. Version selection must retain the
	// integer registry without interpreting those payload enums as versions.
	for file, want := range map[string]int64{"checkpoint.json": 3, "run-event.json": 2} {
		got, err := recordFixtureMaxVersion(file)
		if err != nil || got != want {
			t.Fatalf("%s version registry: got %d, error %v; want %d", file, got, err, want)
		}
	}
}

func TestParallelApprovalFixtureHonorsCancellationAndLeavesDeclarationsDetached(t *testing.T) {
	for _, cancelObserver := range []bool{false, true} {
		parent, stopParent := context.WithCancel(context.Background())
		defer stopParent()
		observer, stopObserver := context.WithCancel(context.Background())
		defer stopObserver()
		if cancelObserver {
			stopObserver()
		} else {
			stopParent()
		}
		original := []runcontext.HostTool{{Name: "create_issue", Source: &runcontext.ToolSource{CurrentDigest: func(context.Context) (string, error) { return "original", nil }}}}
		tools, _ := parallelApprovalFixture(parent, original, "create_issue", func(contract.RunEvent) {})
		if _, err := tools[0].Source.CurrentDigest(observer); !errors.Is(err, context.Canceled) {
			t.Fatal("cancelled coordination waited for a missing sibling")
		}
		if value, err := original[0].Source.CurrentDigest(context.Background()); err != nil || value != "original" {
			t.Fatal("fixture changed the original declaration")
		}
	}
}
func TestParallelApprovalFixtureWaitsForTheNamedRefusedResult(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{})
	release := make(chan error, 1)
	tools, emit := parallelApprovalFixture(ctx, []runcontext.HostTool{{Name: "create_issue", Source: &runcontext.ToolSource{CurrentDigest: func(context.Context) (string, error) { close(entered); return "original", nil }}}}, "create_issue", func(contract.RunEvent) {})
	go func() { _, err := tools[0].Source.CurrentDigest(ctx); release <- err }()
	<-entered
	select {
	case <-release:
		t.Fatal("approval observer released before the sibling result")
	default:
	}
	emit(contract.RunEvent{ToolResult: &contract.ToolResult{Type: "tool_result", Name: "tracker__create_issue", IsError: true}})
	if err := <-release; err != nil {
		t.Fatal(err)
	}
}
