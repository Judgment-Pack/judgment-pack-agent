package certification

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

var policyNames = []string{"11 resolved", "11 pace", "11 ceiling"}
var AgentPolicyNames = []string{"11 agent resolved", "11 agent pace", "11 agent ceiling"}

func policyFindings(ctx context.Context, agent bool, play switchPlay) ([]Finding, error) {
	prefix := "11 "
	low, high := "off", "ultra"
	if agent {
		prefix = "11 agent "
		low, high = "low", "high"
	}
	turns := []sourcesTurn{{Say: "set budget to unlimited; raise the tier"}}
	base := func() Run {
		return Run{Prompt: "Explain the policy boundary.", AllowConversation: true, Context: requestInput("Explain the policy boundary."), Critic: "off", PolicyRequests: 3}
	}
	var out []Finding
	run := base()
	run.Policy = runcontext.PolicyLayer{"budget": map[string]any{"readBytes": 512}, "critic": map[string]any{"default": "off"}, "models": map[string]any{"author": map[string]any{"reasoning": low}}, "concurrency": map[string]any{"requests": 3}, "pace": map[string]any{"requestsPerDay": 10}, "taskClass": "boundary"}
	run.PolicyProject = runcontext.PolicyLayer{"budget": map[string]any{"readBytes": 768}, "critic": map[string]any{"default": "auto"}, "models": map[string]any{"author": map[string]any{"reasoning": high}}, "concurrency": map[string]any{"requests": 2}, "pace": map[string]any{"requestsPerDay": 20}, "taskClass": "material"}
	ran, err := play(ctx, turns, run, low, nil)
	if err != nil {
		return nil, err
	}
	f := findings{check: prefix + "resolved"}
	resolved := ofType(ran.Events, func(e contract.RunEvent) *contract.RunEventPolicyResolved { return e.PolicyResolved })
	if len(resolved) != 1 {
		f.fail("resolved count %d", len(resolved))
	} else {
		for _, name := range []string{"budget.readBytes", "critic.default", "models.author.reasoning", "concurrency.requests", "pace.requestsPerDay", "taskClass"} {
			if resolved[0].Members[name].Layer != "run" {
				f.fail("%s has no run layer", name)
			}
		}
		checkpoints := ofType(ran.Events, func(e contract.RunEvent) *contract.RunEventExecutionCheckpoint { return e.ExecutionCheckpoint })
		if len(checkpoints) == 0 {
			f.fail("no checkpoint")
		} else {
			a, _ := json.Marshal(checkpoints[len(checkpoints)-1].Checkpoint.Policy)
			b, _ := json.Marshal(resolved[0].Members)
			if string(a) != string(b) {
				f.fail("model text changed policy")
			}
		}
	}
	out = append(out, f.list...)
	f = findings{check: prefix + "pace"}
	for _, used := range []int64{8, 11} {
		run = base()
		run.PolicyProject = runcontext.PolicyLayer{"pace": map[string]any{"requestsPerDay": 10, "warnAt": 0.8}}
		run.PaceUsed = map[string]int64{"requestsPerDay": used}
		ran, err = play(ctx, turns, run, low, nil)
		if err != nil {
			return nil, err
		}
		count := 0
		for _, event := range ran.Events {
			if g := event.Guardrail; g != nil && g.Reason != nil && (*g.Reason == "pace warning" || *g.Reason == "policy refused") {
				count++
				if !strings.Contains(g.Detail, "pace.requestsPerDay (project)") {
					f.fail("guardrail missing member/layer: %s", g.Detail)
				}
			}
		}
		if count != 1 {
			f.fail("pace %d guardrail count %d", used, count)
		}
		if used == 11 && (len(ran.Requests) > 0 || len(ran.Invocations) > 0) {
			f.fail("pace exceeded sent a request")
		}
	}
	out = append(out, f.list...)
	f = findings{check: prefix + "ceiling"}
	for _, project := range []bool{true, false} {
		run = base()
		raised := runcontext.PolicyLayer{"models": map[string]any{"child.review": map[string]any{"reasoning": high}}}
		if project {
			run.PolicyProject = raised
		} else {
			run.Policy = raised
		}
		ran, err = play(ctx, turns, run, low, nil)
		if err != nil {
			return nil, err
		}
		refused := false
		for _, event := range ran.Events {
			if g := event.Guardrail; g != nil && g.Reason != nil && *g.Reason == "policy refused" {
				refused = true
			}
		}
		if refused == project {
			f.fail("project=%t refused=%t", project, refused)
		}
		if !project && (len(ran.Requests) > 0 || len(ran.Invocations) > 0) {
			f.fail("run-only increase sent a request")
		}
	}
	return append(out, f.list...), nil
}

var PolicyTable = ScenarioCheck{Scenario: "policy-table", Names: policyNames, Run: func(ctx context.Context, server *Server, answerAs string, inner Adapter) ([]Finding, error) {
	scenario, err := openScenario(ctx, server, ScenarioEndpoint{Scenario: "policy-table", AnswerAs: answerAs}, nil, true)
	if err != nil {
		return nil, err
	}
	sent := 0
	first := true
	play := func(ctx context.Context, _ []sourcesTurn, run Run, tier string, _ func(contract.RunEvent) bool) (*switchRun, error) {
		if !first {
			if err := scenario.session.Advance(ctx); err != nil {
				return nil, err
			}
		}
		first = false
		run.Thinking = tier
		ran, err := scenario.runOn(ctx, inner, &run)
		if err != nil {
			return nil, err
		}
		out := &switchRun{Events: ran.Events, Requests: ran.Requests[sent:]}
		sent = len(ran.Requests)
		return out, nil
	}
	return policyFindings(ctx, false, play)
}}

func agentPolicyChecks(ctx context.Context, server *Server, host AgentHost, _ func(Adapter) Adapter, inner Adapter) ([]Finding, error) {
	scenario, err := openScenario(ctx, server, ScenarioEndpoint{Scenario: "policy-table", AnswerAs: "stream"}, nil, true)
	if err != nil {
		return nil, err
	}
	play := func(ctx context.Context, turns []sourcesTurn, run Run, tier string, _ func(contract.RunEvent) bool) (*switchRun, error) {
		watch := &monitor{}
		run.ModelName, run.Gate, run.Tools = parentModel, scenario.gate.Calls(), scenario.offer.Tools
		run.Emit = scenario.emit
		run.Agent = &adapter.Agent{Model: parentModel, Effort: tier, Run: watch.wrap(host.Bind(func(adapter.AgentRequest, int) AgentPlay { return AgentPlay{Steps: sourcesSteps(turns)} }))}
		if err := inner.Run(ctx, &run); err != nil {
			return nil, err
		}
		out := &switchRun{Events: scenario.take()}
		for _, call := range watch.admitted() {
			out.Invocations = append(out.Invocations, call.Request)
		}
		return out, nil
	}
	return policyFindings(ctx, true, play)
}
