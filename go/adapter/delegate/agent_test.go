package delegate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
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

// ADR-0010: under auto and on one invocation is held back for the critic from the first one on, so a
// batch is admitted only while the critic's invocation and the continuation both remain, and a child
// never takes it; under off nothing is held back.
func TestTheCriticsInvocationIsHeldBackBeforeABatch(t *testing.T) {
	held := func(critic string, purpose string) *coordinator {
		run := &adapter.Run{Agent: &adapter.Agent{Model: "agent-model"}, Critic: critic, Purpose: purpose}
		return &coordinator{run: run, runs: agentRuns{parent: 1, children: map[string]int{}}, account: newAccount(), criticReserve: criticReserve(run)}
	}
	if c := held("", ""); c.criticReserve != 1 || c.reserveContinuation(3) == nil || c.reserveContinuation(2) != nil {
		t.Errorf("under auto the reserve is %d, a batch of three admitted: %v, a batch of two refused: %v", c.criticReserve, c.reserveContinuation(3) == nil, c.reserveContinuation(2) != nil)
	}
	if c := held("on", ""); c.criticReserve != 1 || !c.mayDelegate() {
		t.Error("under on the first invocation may not delegate, or nothing is held back")
	}
	if c := held("off", ""); c.criticReserve != 0 || c.reserveContinuation(3) != nil {
		t.Error("under off an invocation is held back")
	}
	if c := held("on", "graph"); c.criticReserve != 0 {
		t.Error("a graph-purpose run, which runs no critic, holds an invocation back")
	}
	if c := (&coordinator{run: &adapter.Run{}, runs: agentRuns{children: map[string]int{}}}); criticReserve(c.run) != 0 {
		t.Error("the model capability holds an invocation back")
	}
	// A child never takes the critic's invocation: with the parent, two children and the
	// continuation counted, a third child is refused under auto and admitted under off.
	auto, off := held("", ""), held("off", "")
	for _, c := range []*coordinator{auto, off} {
		c.runs.children["a"], c.runs.children["b"] = 1, 1
	}
	task := &contract.DelegatedTask{ID: "c"}
	if err := auto.admitRun(task); err == nil {
		t.Error("under auto a child took the critic's invocation")
	}
	if err := off.admitRun(task); err != nil {
		t.Errorf("under off a child was refused: %v", err)
	}
	// The critic's room is what the agent link answers the parent's context: an invocation left.
	sc, err := runcontext.NewSessionContext(runcontext.Session{Agent: &runcontext.AgentSession{Model: "agent-model"}})
	if err != nil {
		t.Fatal(err)
	}
	agentLink{auto}.Attach(sc)
	if !sc.CriticRoom() || auto.admitRun(nil) != nil || !sc.CriticRoom() {
		t.Error("under auto the continuation took the critic's room")
	}
	if auto.admitRun(nil) != nil || sc.CriticRoom() {
		t.Error("the critic has room once the response's five invocations are taken")
	}
}

// The schema inventory routes handoff to the coordinator that actually restores and projects it.
func TestCheckpointMembersHandoffInterruptResume(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	raw, err := os.ReadFile(filepath.Join(root, "contract", "vectors", "checkpoint", "members.json"))
	if err != nil {
		t.Fatal(err)
	}
	var vector struct {
		ProbeOwners map[string]string     `json:"probeOwners"`
		Items       []contract.Checkpoint `json:"items"`
	}
	if err := json.Unmarshal(raw, &vector); err != nil {
		t.Fatal(err)
	}
	if vector.ProbeOwners["handoff"] != "delegation" || len(vector.Items) != 1 {
		t.Fatal("handoff coverage inventory missing")
	}
	saved := vector.Items[0]
	// A stopped parent is handed back by the host, then restored before any child dispatch.
	saved.Status = "stopped"
	c := &coordinator{run: &adapter.Run{Agent: &adapter.Agent{Model: "fixture"}}, runs: agentRuns{children: map[string]int{}}, account: newAccount()}
	pending := c.restore(&saved)
	if pending == nil {
		t.Fatal("interrupted handoff not restored")
	}
	c.setHandoff(pending)
	continued := c.decorate(saved)
	want, _ := json.Marshal(saved.Handoff)
	got, _ := json.Marshal(continued.Handoff)
	if string(got) != string(want) {
		t.Fatalf("handoff changed across owning projection: %s versus %s", got, want)
	}
}
