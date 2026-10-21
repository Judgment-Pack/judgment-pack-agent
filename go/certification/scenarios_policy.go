package certification

import (
	"context"
	"strings"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

var policyNames = []string{"11 resolved", "11 pace", "11 ceiling", "11 resume", "11 alias"}
var AgentPolicyNames = []string{"11 agent resolved", "11 agent pace", "11 agent ceiling", "11 agent resume", "11 agent alias"}

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
	project := runcontext.PolicyLayer{"budget": map[string]any{"readBytes": 768}, "critic": map[string]any{"default": "off"}, "models": map[string]any{"author": map[string]any{"reasoning": high}}, "concurrency": map[string]any{"requests": 2}, "pace": map[string]any{"requestsPerDay": 20}, "taskClass": "material"}
	runLayer := runcontext.PolicyLayer{"budget": map[string]any{"readBytes": 512}, "critic": map[string]any{"default": "auto"}, "models": map[string]any{"author": map[string]any{"reasoning": low}}, "concurrency": map[string]any{"requests": 3}, "pace": map[string]any{"requestsPerDay": 10}, "taskClass": "boundary"}
	var ran *switchRun
	var err error
	var run Run
	f := findings{check: prefix + "resolved"}
	for _, layer := range []string{"table", "project", "run"} {
		run = base()
		run.Critic = ""
		if layer != "table" {
			run.PolicyProject = project
		}
		if layer == "run" {
			run.Policy = runLayer
		}
		ran, err = play(ctx, turns, run, low, nil)
		if err != nil {
			return nil, err
		}
		resolved := ofType(ran.Events, func(e contract.RunEvent) *contract.RunEventPolicyResolved { return e.PolicyResolved })
		if len(resolved) != 1 {
			f.fail("%s resolved count %d", layer, len(resolved))
			continue
		}
		if len(ran.Events) == 0 || ran.Events[0].PolicyResolved == nil {
			f.fail("policy was not the first event")
		}
		if len(ran.Requests)+len(ran.Invocations) != 1 {
			f.fail("%s did not make one request", layer)
		}
		// Hand-authored table values, not captured from the resolver.
		budget := map[string]any{
			"memoryProposalBytes": 1000, "memoryProposalsPerRun": 4, "editsPerTurn": 24, "editBytes": 8000, "proposalBytes": 65536,
			"version": 1, "maxInputBytes": 180000, "contextWindowTokens": 65536, "outputTokens": 8192,
			"toolResultBytes": 12000, "readBytes": 6000, "retainedBytes": 8388608, "recentMessages": 8, "summaryBytes": 4000,
			"historyBytes": 32000, "briefBytes": 8000, "memoryBytes": 4000, "turnMilliseconds": 900000,
			"toolCallMilliseconds": 120000, "imagesPerRequest": 8, "imageBytesPerRequest": 16777216,
			"testCallsPerTurn": 3, "testPackBytes": 65536, "testMatrixBytes": 65536, "turnIndexEntries": 20,
			"turnIndexBytes": 8000, "projectInstructionsBytes": 4000, "toolDescriptionBytes": 1024,
			"offeredSchemas": 16, "offeredSchemaBytes": 24000, "cataloguedTools": 64, "catalogueBytes": 24000,
			"sourceResultBytes": 8000, "sourceCallsPerTurn": 8, "sourceCallMilliseconds": 30000,
			"skillBytes": 8000, "skillsPerRun": 4, "skillsCatalogueBytes": 8000, "cataloguedSkills": 32, "cataloguedSubagents": 16, "subagentCatalogueBytes": 4000,
		}
		model := "scripted-model"
		if agent {
			model = parentModel
		}
		values := map[string]any{"compaction": "index", "critic.default": "auto", "critic.reasoningFloor": "author",
			"failover.classes":       []any{"unavailable", "rate-limited", "timeout"},
			"models.author.provider": model, "models.critic.provider": model,
			"models.compactor.provider": model,
			"models.author.model":       model, "models.author.reasoning": low,
			"models.critic.model": model, "models.critic.reasoning": low,
			"models.compactor.model": model, "models.compactor.reasoning": "low",
			"models.author.fallback": []any{}, "models.critic.fallback": []any{}, "models.compactor.fallback": []any{},
			"concurrency.children": 2, "concurrency.requests": 2,
			"pace.requestsPerDay": nil, "pace.invocationsPerDay": nil, "pace.bytesPerDay": nil, "pace.warnAt": 0.8, "taskClass": "routine"}
		if agent {
			values["concurrency.children"] = 1
		}
		for name, value := range budget {
			values["budget."+name] = value
		}
		want := runcontext.PolicyMembers{}
		for name, value := range values {
			want[name] = runcontext.PolicyMember{Value: value, Layer: "table"}
		}
		if layer != "table" {
			selected := []string{"budget.readBytes", "critic.default", "models.author.reasoning", "concurrency.requests", "pace.requestsPerDay", "taskClass"}
			chosen := []any{768, "off", high, 2, 20, "material"}
			if layer == "run" {
				chosen = []any{512, "auto", low, 3, 10, "boundary"}
			}
			for i, name := range selected {
				want[name] = runcontext.PolicyMember{Value: chosen[i], Layer: layer}
			}
			want["models.critic.reasoning"] = runcontext.PolicyMember{Value: chosen[2], Layer: "table"}
		}
		if comparisonJSON(resolved[0].Members) != comparisonJSON(want.Record()) {
			f.fail("%s members: got %s want %s", layer, comparisonJSON(resolved[0].Members), comparisonJSON(want.Record()))
		}
		saved := lastCheckpointOf(ran.Events)
		if saved == nil || comparisonJSON(saved.Policy) != comparisonJSON(want.Record()) {
			f.fail("model text changed %s policy", layer)
		}
		messages := ofType(ran.Events, func(e contract.RunEvent) *contract.RunEventMessage { return e.Message })
		if len(messages) != 1 || messages[0].Text != turns[0].Say {
			f.fail("model-visible policy instruction was not played")
		}
	}
	out = append(out, f.list...)
	f = findings{check: prefix + "resume"}
	run = base()
	run.Policy = runLayer
	run.PolicyProject = project
	ran, err = play(ctx, turns, run, low, func(e contract.RunEvent) bool {
		return e.ExecutionCheckpoint != nil && e.ExecutionCheckpoint.Checkpoint.Policy != nil
	})
	if err != nil {
		return nil, err
	}
	saved, err := readBack(lastCheckpointOf(ran.Events))
	if err != nil {
		return nil, err
	}
	run.Context.Execution = saved
	ran, err = play(ctx, turns, run, low, nil)
	if err != nil {
		return nil, err
	}
	resumed := ofType(ran.Events, func(e contract.RunEvent) *contract.RunEventPolicyResolved { return e.PolicyResolved })
	if len(resumed) != 1 || comparisonJSON(resumed[0].Members) != comparisonJSON(saved.Policy) {
		f.fail("resume lost policy values or layers")
	}
	if last := lastCheckpointOf(ran.Events); last == nil || comparisonJSON(last.Policy) != comparisonJSON(saved.Policy) {
		f.fail("resumed checkpoint lost policy")
	}
	if len(ran.Requests)+len(ran.Invocations) != 1 {
		f.fail("resume did not dispatch")
	}
	out = append(out, f.list...)
	f = findings{check: prefix + "alias"}
	var aliasMembers map[string]contract.PolicyMember
	for _, nested := range []bool{false, true} {
		run = base()
		if nested {
			run.Policy = runcontext.PolicyLayer{"budget": map[string]any{"readBytes": 512}}
		} else {
			run.Policy = runcontext.PolicyAlias(runcontext.PolicyLayer{"readBytes": 512})
		}
		ran, err = play(ctx, turns, run, low, nil)
		if err != nil {
			return nil, err
		}
		resolved := ofType(ran.Events, func(e contract.RunEvent) *contract.RunEventPolicyResolved { return e.PolicyResolved })
		if len(resolved) != 1 {
			f.fail("alias resolved count %d", len(resolved))
			continue
		}
		member := resolved[0].Members["budget.readBytes"]
		if string(member.Value) != "512" || member.Layer != "run" {
			f.fail("alias not read as policy.budget")
		}
		if nested && comparisonJSON(aliasMembers) != comparisonJSON(resolved[0].Members) {
			f.fail("alias and nested policy differ")
		}
		aliasMembers = resolved[0].Members
	}
	run = base()
	run.Policy = runcontext.PolicyAlias(runcontext.PolicyLayer{"readBytes": 512, "budget": map[string]any{"readBytes": 768}})
	ran, err = play(ctx, turns, run, low, nil)
	if err != nil {
		return nil, err
	}
	both := ofType(ran.Events, func(e contract.RunEvent) *contract.RunEventPolicyResolved { return e.PolicyResolved })
	if len(both) != 1 || string(both[0].Members["budget.readBytes"].Value) != "768" {
		f.fail("nested spelling did not win over alias")
	}
	out = append(out, f.list...)
	f = findings{check: prefix + "pace"}
	for _, used := range []int64{8, 10, 11} {
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
		failures := ofType(ran.Events, func(e contract.RunEvent) *contract.RunEventError { return e.Error })
		if used >= 10 {
			if len(failures) != 1 || failures[0].Failure == nil || failures[0].Failure.Code != "pace exceeded" {
				f.fail("pace refusal has no pace exceeded class")
			}
		} else if len(failures) != 0 || len(ran.Requests)+len(ran.Invocations) != 1 {
			f.fail("pace warning did not admit one request")
		}
		if used >= 10 && (len(ran.Requests) > 0 || len(ran.Invocations) > 0) {
			f.fail("pace exceeded sent a request")
		}
	}
	for _, keys := range [][]string{{"requestsPerDay", "invocationsPerDay", "bytesPerDay"}, {"requestsPerDay"}, {"invocationsPerDay"}, {"bytesPerDay"}} {
		run = base()
		pace := map[string]any{}
		for _, key := range keys {
			pace[key] = 0
		}
		run.PolicyProject = runcontext.PolicyLayer{"pace": pace}
		run.PaceUsed = map[string]int64{"requestsPerDay": 0, "invocationsPerDay": 0, "bytesPerDay": 0}
		ran, err = play(ctx, turns, run, low, nil)
		if err != nil {
			return nil, err
		}
		failures := ofType(ran.Events, func(e contract.RunEvent) *contract.RunEventError { return e.Error })
		if len(failures) != 1 || failures[0].Failure == nil || failures[0].Failure.Code != "pace exceeded" || len(ran.Requests)+len(ran.Invocations) != 0 {
			f.fail("zero pace %v did not refuse before dispatch", keys)
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
		if project {
			resolved := ofType(ran.Events, func(e contract.RunEvent) *contract.RunEventPolicyResolved { return e.PolicyResolved })
			if len(resolved) != 1 {
				f.fail("project tier has no policy record")
			} else {
				member := resolved[0].Members["models.child.review.reasoning"]
				if string(member.Value) != comparisonJSON(high) || member.Layer != "project" {
					f.fail("project tier not recorded")
				}
			}
			if len(ran.Requests)+len(ran.Invocations) != 1 {
				f.fail("project tier did not admit one request")
			}
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
	play := func(ctx context.Context, _ []sourcesTurn, run Run, tier string, stop func(contract.RunEvent) bool) (*switchRun, error) {
		if !first {
			if err := scenario.session.Advance(ctx); err != nil {
				return nil, err
			}
		}
		first = false
		run.Thinking = tier
		broken, cancel := context.WithCancel(ctx)
		defer cancel()
		scenario.watch(func(event contract.RunEvent) {
			if stop != nil && stop(event) {
				cancel()
			}
		})
		defer scenario.watch(nil)
		ran, err := scenario.runOn(broken, inner, &run)
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
	play := func(ctx context.Context, turns []sourcesTurn, run Run, tier string, stop func(contract.RunEvent) bool) (*switchRun, error) {
		watch := &monitor{}
		run.ModelName, run.Gate, run.Tools = parentModel, scenario.gate.Calls(), scenario.offer.Tools
		broken, cancel := context.WithCancel(ctx)
		defer cancel()
		run.Emit = func(event contract.RunEvent) {
			scenario.emit(event)
			if stop != nil && stop(event) {
				cancel()
			}
		}
		run.Agent = &adapter.Agent{Model: parentModel, Effort: tier, Run: watch.wrap(host.Bind(func(adapter.AgentRequest, int) AgentPlay { return AgentPlay{Steps: sourcesSteps(turns)} }))}
		if err := inner.Run(broken, &run); err != nil {
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
