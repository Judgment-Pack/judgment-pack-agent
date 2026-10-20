package delegate

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

// The figures of the agent capability carry no modelRequests (agent#71): the provider runs its own
// loop and reports no request count, so nothing is written as 0. A model capability's child writes
// its count, 0 included.
func TestAnAgentChildCarriesNoModelRequestsAndAModelChildCarriesItsCount(t *testing.T) {
	duration := int64(10)
	task := &contract.DelegatedTask{ID: "9a1b2c3d-4e5f-4061-8273-94a5b6c7d8e9", Profile: "analyze", State: "complete", DurationMs: &duration}
	agent := &coordinator{run: &adapter.Run{Agent: &adapter.Agent{Model: "agent-model"}}, runs: agentRuns{children: map[string]int{task.ID: 1}}, account: newAccount()}
	figure := agent.childFigures(task, 1)
	encoded, err := json.Marshal(figure)
	if err != nil {
		t.Fatal(err)
	}
	if figure.ModelRequests != nil || strings.Contains(string(encoded), "modelRequests") || figure.AgentRuns == nil || *figure.AgentRuns != 1 {
		t.Fatalf("an agent child's figures: %s", encoded)
	}
	model := &coordinator{run: &adapter.Run{ModelName: "a-model"}}
	figure = model.childFigures(task, 1)
	encoded, _ = json.Marshal(figure)
	if figure.ModelRequests == nil || *figure.ModelRequests != 0 || !strings.Contains(string(encoded), `"modelRequests":0`) || figure.AgentRuns != nil {
		t.Fatalf("a model child's figures: %s", encoded)
	}
}

// A batch's total counts model requests only: a model child's requests are summed, an agent child
// adds none, and a total no child has a count for carries none.
func TestATotalCountsModelRequestsOnly(t *testing.T) {
	two, one := int64(2), int64(1)
	model := contract.ProvenanceChildFigures{Task: "m", Batch: 1, Profile: "analyze", State: "complete", ModelRequests: &two, ToolCalls: 1}
	agent := contract.ProvenanceChildFigures{Task: "a", Batch: 1, Profile: "analyze", State: "complete", AgentRuns: &one, ToolCalls: 2}
	both := totalOf([]contract.ProvenanceChildFigures{model, agent}, 5)
	if both.ModelRequests == nil || *both.ModelRequests != 2 || both.ToolCalls != 3 || both.AgentRuns == nil || *both.AgentRuns != 1 {
		t.Fatalf("a model child and an agent child: %+v", both)
	}
	alone := totalOf([]contract.ProvenanceChildFigures{agent, agent}, 5)
	encoded, _ := json.Marshal(alone)
	if alone.ModelRequests != nil || strings.Contains(string(encoded), "modelRequests") || *alone.AgentRuns != 2 {
		t.Fatalf("two agent children: %s", encoded)
	}
}

// A checkpoint written by v0.4.0 on an agent capability carries modelRequests: 0 on its children and
// on each batch's total. Restored, those batches drop the member, so the checkpoint the coordinator
// writes next does not mix 0 and absence (agent#71); the checkpoint the host handed back is not
// edited.
func TestARestoredV040BatchDropsItsZeroModelRequests(t *testing.T) {
	zero, one := int64(0), int64(1)
	saved := contract.Checkpoint{Version: 1, ID: "x", RequestKey: "k", Phase: "work", Status: "running", Unit: "provider-managed", Limit: 1,
		Delegation: &contract.ProvenanceDelegationReport{Parent: contract.ProvenanceParentFigures{Model: "agent-model", AgentRuns: &one},
			Batches: []contract.ProvenanceBatchFigures{{Batch: 1,
				Children: []contract.ProvenanceChildFigures{{Task: "a", Batch: 1, Profile: "analyze", State: "complete", ModelRequests: &zero, AgentRuns: &one, ToolCalls: 1}},
				Total:    contract.ProvenanceFiguresTotal{ModelRequests: &zero, AgentRuns: &one, ToolCalls: 1, DurationMs: 5}}}}}
	c := &coordinator{run: &adapter.Run{Agent: &adapter.Agent{Model: "agent-model"}}, runs: agentRuns{children: map[string]int{}}, account: newAccount()}
	c.restore(&saved)
	if len(c.figures) != 1 {
		t.Fatalf("%d batches restored", len(c.figures))
	}
	encoded, _ := json.Marshal(c.figures)
	if c.figures[0].Children[0].ModelRequests != nil || c.figures[0].Total.ModelRequests != nil || strings.Contains(string(encoded), "modelRequests") ||
		*c.figures[0].Children[0].AgentRuns != 1 || c.figures[0].Total.ToolCalls != 1 {
		t.Fatalf("the restored batch: %s", encoded)
	}
	if saved.Delegation.Batches[0].Children[0].ModelRequests == nil || saved.Delegation.Batches[0].Total.ModelRequests == nil {
		t.Fatal("the checkpoint the host handed back was edited")
	}
}
