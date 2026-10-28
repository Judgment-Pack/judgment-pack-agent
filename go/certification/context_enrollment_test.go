package certification

import (
	"encoding/json"
	"testing"
)

func TestBoundaryToolsUseCompletePackageDeclarations(t *testing.T) {
	tools := boundaryTools(true)
	seen := map[string]bool{}
	for _, tool := range tools {
		seen[tool.Name] = true
		var schema map[string]json.RawMessage
		if tool.Description == "" || json.Unmarshal(tool.InputSchema, &schema) != nil || schema == nil || tool.Taxonomy.Operation == "" {
			t.Fatalf("incomplete boundary declaration %s", tool.Name)
		}
	}
	for _, name := range []string{"list_context", "read_context", "read_window", "search", "update_plan", "complete_plan_step", "request_budget_increase"} {
		if !seen[name] {
			t.Fatalf("missing package declaration %s", name)
		}
	}
	for _, tool := range boundaryTools(false) {
		if tool.Name == "update_plan" || tool.Name == "complete_plan_step" || tool.Name == "request_budget_increase" {
			t.Fatal("conversation control enabled by fixture")
		}
	}
}
