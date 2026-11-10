package runcontext

import "testing"

// #344: which acting Runtime calls skip the approval path. Only the acting experimental_evaluate, from
// the critic's reading or a delegated child, outside Plan mode and the reformat run; every other
// acting call still needs its approved attempt (out.CallTool refuses it), and a pure one never did.
func TestOnlyACriticsOrAChildsEvaluationIsARehearsal(t *testing.T) {
	for _, row := range []struct {
		label   string
		tool    string
		effect  string
		shape   func(c *SessionContext)
		approve bool
	}{
		{"the loop's acting evaluation", "experimental_evaluate", "act", func(*SessionContext) {}, true},
		{"the critic's", "experimental_evaluate", "act", func(c *SessionContext) { c.CriticReading = true }, false},
		{"a delegated child's", "experimental_evaluate", "act", func(c *SessionContext) { c.objective = "Run the tests." }, false},
		{"a declared sub-agent's", "experimental_evaluate", "act", func(c *SessionContext) { c.objective = "Run the tests."; c.admittedChild = true }, true},
		{"the critic's in the reformat run", "experimental_evaluate", "act", func(c *SessionContext) { c.CriticReading = true; c.Reformatting = true }, true},
		{"the critic's in Plan mode", "experimental_evaluate", "act", func(c *SessionContext) {
			c.CriticReading = true
			if err := c.Execution.MutateCheckpoint(CheckpointPatch{"Phase": "plan"}); err != nil {
				t.Fatal(err)
			}
		}, true},
		{"another acting tool, the critic's", "other_action", "act", func(c *SessionContext) { c.CriticReading = true }, true},
		{"another acting tool, a child's", "other_action", "act", func(c *SessionContext) { c.objective = "Run the tests." }, true},
		{"a pure evaluation", "experimental_evaluate", "pure", func(*SessionContext) {}, false},
		{"the critic's evaluation served as a read", "experimental_evaluate", "read", func(c *SessionContext) { c.CriticReading = true }, false},
		{"a child's evaluation served as a read", "experimental_evaluate", "read", func(c *SessionContext) { c.objective = "Run the tests." }, false},
	} {
		t.Run(row.label, func(t *testing.T) {
			execution := &ExecutionController{}
			if err := execution.MutateCheckpoint(CheckpointPatch{"Phase": "work"}); err != nil {
				t.Fatal(err)
			}
			c := &SessionContext{Execution: execution, runtimeTools: []RuntimeTool{{Name: row.tool, Meta: &ToolTaxonomy{Operation: "evaluate", Effect: row.effect}}}}
			row.shape(c)
			if got := c.RuntimeNeedsApproval(row.tool); got != row.approve {
				t.Fatalf("RuntimeNeedsApproval = %v", got)
			}
			if got := c.runtimeRehearsal(row.tool); got != (row.effect == "act" && !row.approve) {
				t.Fatalf("runtimeRehearsal = %v", got)
			}
		})
	}
}
