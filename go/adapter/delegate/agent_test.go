package delegate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

func handoffVector(t *testing.T) []contract.CheckpointHandoff {
	t.Helper()
	dir, _ := os.Getwd()
	for {
		raw, err := os.ReadFile(filepath.Join(dir, "contract", "vectors", "delegation", "handoff.json"))
		if err == nil {
			var file struct {
				Items []contract.CheckpointHandoff `json:"items"`
			}
			if err := json.Unmarshal(raw, &file); err != nil {
				t.Fatal(err)
			}
			return file.Items
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no contract/vectors above the working directory")
		}
		dir = parent
	}
}

// A checkpoint the host hands back after a restart is read where it stood (ADR-0007): each item of
// the handoff vector restores its state, its records and its result, and the coordinator writes the
// same record back; the response's runs so far are the figures the checkpoint carries, never fewer.
func TestARestoredHandoffIsReadWhereItStood(t *testing.T) {
	for at, item := range handoffVector(t) {
		runs := int64(2)
		one := int64(1)
		saved := contract.Checkpoint{Version: 1, ID: "x", RequestKey: "k", Phase: "work", Status: "running", Unit: "provider-managed", Limit: 1,
			Handoff: &item, Delegation: &contract.ProvenanceDelegationReport{Parent: contract.ProvenanceParentFigures{Model: "agent-model", AgentRuns: &runs},
				Batches: []contract.ProvenanceBatchFigures{{Batch: 1, Children: []contract.ProvenanceChildFigures{
					{Task: item.Request.Tasks[0].Title, Batch: 1, Profile: "analyze", State: "running", AgentRuns: &one}}}}}}
		c := &coordinator{run: &adapter.Run{Agent: &adapter.Agent{Model: "agent-model"}}, runs: agentRuns{children: map[string]int{}}, account: newAccount()}
		pending := c.restore(&saved)
		if pending == nil || pending.state != item.State || pending.callID != item.CallID || len(pending.tasks) != len(item.Tasks) {
			t.Fatalf("item %d restored as %+v", at, pending)
		}
		c.setHandoff(pending)
		again, _ := json.Marshal(c.handoffRecord())
		want, _ := json.Marshal(item)
		if string(again) != string(want) {
			t.Errorf("item %d is written back as\n%s\nnot\n%s", at, again, want)
		}
		if item.Answer != nil && item.Answer.Note != answerNote {
			t.Errorf("item %d: the answer's note is not the coordinator's", at)
		}
		open := item.State == "released" || item.State == "children"
		total := c.runs.total()
		if total != 3 {
			t.Errorf("item %d: %d runs restored, where the checkpoint carries three", at, total)
		}
		if open != (len(c.figures) == 0) {
			t.Errorf("item %d: the batch in progress was taken as finished, or a finished one as in progress", at)
		}
	}
}
