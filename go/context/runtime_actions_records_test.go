package runcontext

import (
	"encoding/json"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"os"
	"testing"
)

func TestRuntimeActionCurrentRecords(t *testing.T) {
	for _, name := range []string{"runtime-pause", "runtime-request"} {
		raw, err := os.ReadFile("../../contract/vectors/approval/" + name + ".json")
		if err != nil {
			t.Fatal(err)
		}
		var vector struct{ Items []json.RawMessage }
		if json.Unmarshal(raw, &vector) != nil {
			t.Fatal("bad vector")
		}
		for _, item := range vector.Items {
			v, err := ParseJSON(item)
			if err != nil {
				t.Fatal(err)
			}
			if name == "runtime-pause" {
				if _, err = ReadExecution(v); err != nil {
					t.Fatal(err)
				}
				v.Set("version", Number(2))
				if _, err = ReadExecution(v); err == nil {
					t.Fatal("Runtime binding accepted under frozen checkpoint2")
				}
			} else {
				if err = contract.CheckRecord("event", item); err != nil {
					t.Fatal(err)
				}
				v.Set("version", Number(1))
				if err = contract.CheckRecord("event", v.JSON()); err == nil {
					t.Fatal("Runtime binding accepted under frozen event1")
				}
			}
		}
	}
}

// The standalone binding vector is reporting, and must be the exact binding embedded
// in both supported record forms. It does not construct a connector or a call grant.
func TestRuntimeActionCurrentBinding(t *testing.T) {
	read := func(name string) []json.RawMessage {
		t.Helper()
		raw, err := os.ReadFile("../../contract/vectors/approval/" + name + ".json")
		if err != nil {
			t.Fatal(err)
		}
		var file struct{ Items []json.RawMessage }
		if err = json.Unmarshal(raw, &file); err != nil || len(file.Items) != 1 {
			t.Fatal("invalid binding fixture", err)
		}
		return file.Items
	}
	binding := read("runtime-binding")[0]
	var declared contract.CheckpointRuntimeApprovalBinding
	if err := json.Unmarshal(binding, &declared); err != nil {
		t.Fatal(err)
	}
	var pause contract.Checkpoint
	if err := json.Unmarshal(read("runtime-pause")[0], &pause); err != nil {
		t.Fatal(err)
	}
	var request contract.RunEvent
	if err := json.Unmarshal(read("runtime-request")[0], &request); err != nil {
		t.Fatal(err)
	}
	if pause.Approval == nil || pause.Approval.Runtime == nil || request.ApprovalRequested == nil || request.ApprovalRequested.Runtime == nil {
		t.Fatal("current records lost Runtime binding")
	}
	for _, held := range []contract.CheckpointRuntimeApprovalBinding{*pause.Approval.Runtime, *request.ApprovalRequested.Runtime} {
		raw, err := json.Marshal(held)
		if err != nil {
			t.Fatal(err)
		}
		if CanonicalJSON(MustParseJSON(string(raw))) != CanonicalJSON(MustParseJSON(string(binding))) {
			t.Fatal("standalone binding differs from current record binding")
		}
	}
	bad := MustParseJSON(string(binding)).Set("grant", Bool(true))
	if json.Unmarshal(bad.JSON(), &declared) == nil {
		t.Fatal("binding record accepted a dispatch grant member")
	}
}
