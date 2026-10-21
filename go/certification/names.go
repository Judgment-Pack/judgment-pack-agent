package certification

// The checks the Go runner makes, by name: the engine leg's (CheckNames), each leg block's, and
// each scenario's. parity.json maps every TypeScript check to these.

// LegChecks are the leg blocks after the engine leg, in the TypeScript suite's order.
func LegChecks() []Check {
	var out []Check
	for _, block := range [][]Check{TextChecks, StrictChecks, ThinkingChecks, MoreThinkingChecks, WireChecks} {
		out = append(out, block...)
	}
	return out
}

// DelegationNames are the delegation scenario's checks.
var DelegationNames = []string{"2 runs", "2 findings", "2 offers", "2 references", "2 reports", "2 figures", "2 resume"}

// AllScenarios are the ten scenarios' checks (the seventh ADR-0006's, the eighth ADR-0008's, the ninth ADR-0009's, the tenth ADR-0010's) and the delegation capacity
// check, the delegation scenario's and the capacity check's made with the coordinator the caller
// wraps the adapter in.
func AllScenarios(with func(Adapter) Adapter) []ScenarioCheck {
	out := []ScenarioCheck{}
	for _, scenario := range Scenarios {
		// Scenario 8 runs through the coordinator, the path a continued run takes (ADR-0008).
		if scenario.Scenario == "approval-pause" {
			scenario = ApprovalPauseWith(with)
		}
		out = append(out, scenario)
	}
	return append(out, Delegation(with), Capacity(with))
}

// AllCheckNames are every check the Go runner makes, by name.
func AllCheckNames() []string {
	out := append([]string{}, CheckNames...)
	for _, check := range LegChecks() {
		out = append(out, check.Name)
	}
	for _, scenario := range Scenarios {
		out = append(out, scenario.Names...)
	}
	out = append(out, DelegationNames...)
	out = append(out, CapacityNames...)
	// The agent capability's checks that have a TypeScript counterpart (ADR-0007); the rest are the
	// package's own (AgentOwnNames, parity.json's agentCapability).
	out = append(out, AgentScenarioNames...)
	out = append(out, AgentSourcesNames...)
	out = append(out, AgentApprovalNames...)
	out = append(out, AgentSkillsNames...)
	out = append(out, AgentSwitchNames...)
	out = append(out, AgentPolicyNames...)
	out = append(out, AgentProviderNames...)
	out = append(out, AgentSubagentNames...)
	out = append(out, AgentMemoryWriteNames...)
	out = append(out, AgentProposalEditNames...)
	out = append(out, AgentIdentityNames...)
	return append(out, AgentDelegationNames...)
}
