package certification

import (
	"encoding/json"
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
