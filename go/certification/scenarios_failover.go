package certification

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/delegate"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/turn"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
	"github.com/Judgment-Pack/judgment-pack-agent/go/relay"
)

var failoverNames = []string{"20 switch", "20 refusal", "20 cross-family", "20 usage", "20 child fallback", "20 partial usage"}
var AgentFailoverNames = []string{"20 agent switch", "20 agent refusal", "20 agent cross-family", "20 agent usage", "20 agent delegation refusal", "20 agent child refusal"}

func failoverFindings(ctx context.Context, server *Server, inner Adapter, agent bool) ([]Finding, error) {
	var file struct{ Request, Proposal string }
	if err := ScenarioFile("provider-failover", &file); err != nil {
		return nil, err
	}
	prefix := "20 "
	if agent {
		prefix = "20 agent "
	}
	var out []Finding
	// Qualifying failure: the identical complete request is sent once to the same-family fallback.
	f := findings{check: prefix + "switch"}
	host, err := openScenario(ctx, server, ScenarioEndpoint{Scenario: "policy-table", AnswerAs: "whole"}, nil, true)
	if err != nil {
		return nil, err
	}
	var events []contract.RunEvent
	var bodies []string
	calls := []string{}
	run := Run{ModelName: "primary-model", Wire: "openai-compatible", Thinking: "ultra", Prompt: file.Request, AllowConversation: true, Critic: "off", Gate: host.gate.Calls(), Tools: host.offer.Tools, Context: &runcontext.Input{Connection: "primary", CurrentRequest: &runcontext.CurrentRequest{Message: file.Request, Input: file.Request}}, Policy: runcontext.PolicyLayer{"models": map[string]any{"author": map[string]any{"fallback": []any{"backup"}}}}, Emit: func(e contract.RunEvent) { events = append(events, e) }}
	backup := adapter.PolicyConnection{ModelName: "backup-model", Wire: "openai-compatible", Model: func(_ context.Context, _ string, r relay.Request) (*http.Response, error) {
		calls = append(calls, "backup")
		bodies = append(bodies, string(r.Body))
		return providerAnswer("openai", file.Proposal, false), nil
	}}
	run.Model = func(_ context.Context, _ string, r relay.Request) (*http.Response, error) {
		calls = append(calls, "primary")
		bodies = append(bodies, string(r.Body))
		return &http.Response{StatusCode: 503, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	}
	if agent {
		makeAgent := func(id string, fail bool) *adapter.Agent {
			return &adapter.Agent{Family: "codex", Model: id + "-model", Effort: "high", Run: func(_ context.Context, r adapter.AgentRequest, c adapter.AgentCallbacks) error {
				raw, _ := json.Marshal(r)
				calls = append(calls, id)
				bodies = append(bodies, string(raw))
				if fail {
					return failure.Err("unavailable", "agent", "unknown")
				}
				return c.Event(ctx, adapter.AgentEvent{Type: "message", ID: "answer", Text: file.Proposal, Phase: "final"})
			}}
		}
		run.Agent = makeAgent("primary", true)
		run.Model = nil
		run.Wire = ""
		backup = adapter.PolicyConnection{Agent: makeAgent("backup", false)}
	}
	run.PolicyConnections = map[string]adapter.PolicyConnection{"backup": backup}
	if err = inner.Run(ctx, &run); err != nil {
		return nil, err
	}
	if len(calls) != 2 || calls[0] != "primary" || calls[1] != "backup" {
		f.fail("dispatch is %v", calls)
	}
	if len(bodies) == 2 && bodies[0] != bodies[1] {
		f.fail("fallback request was not rebuilt identically")
	}
	switches := ofType(events, func(e contract.RunEvent) *contract.RunEventConnectionSwitched { return e.ConnectionSwitched })
	if len(switches) != 1 || switches[0].From != "primary" || switches[0].To != "backup" || switches[0].Reason != "unavailable" {
		f.fail("switch event is %+v", switches)
	}
	usage := ofType(events, func(e contract.RunEvent) *contract.RunEventUsage { return e.Usage })
	if len(usage) != 1 {
		f.fail("%d usage events", len(usage))
	} else {
		accounted := map[string]bool{}
		for _, row := range usage[0].Usage.Rows {
			if (row.Connection == "primary" || row.Connection == "backup") && row.Bytes > 0 && (agent && row.Invocations == 1 || !agent && row.Requests == 1) {
				accounted[row.Connection] = true
			}
			if agent && row.Tokens != nil {
				f.fail("agent tokens were invented")
			}
		}
		if !accounted["primary"] || !accounted["backup"] {
			f.fail("failover cache cost was not split across both connections: %+v", usage[0].Usage.Rows)
		}
	}
	// Resume on the active connection with the consumed chain and reason intact.
	checkpoints := ofType(events, func(e contract.RunEvent) *contract.RunEventExecutionCheckpoint { return e.ExecutionCheckpoint })
	if len(checkpoints) == 0 {
		f.fail("no checkpoint for resume")
	} else {
		saved := checkpoints[len(checkpoints)-1].Checkpoint
		if len(saved.Failover) != 1 || saved.Failover[0].Connection != "backup" || saved.Failover[0].Consumed != 1 || len(saved.Failover[0].History) != 1 || saved.Failover[0].History[0].Reason != "unavailable" {
			f.fail("switch state missing: %+v", saved.Failover)
		}
		run.Context.Execution = &saved
		if err := inner.Run(ctx, &run); err != nil {
			return nil, err
		}
		if len(calls) != 3 || calls[2] != "backup" {
			f.fail("resume dispatch: %v", calls)
		}
		run.Context.Execution = nil
	}
	// A resumed exhausted chain cannot regain its first switch.
	if len(checkpoints) > 0 {
		saved := checkpoints[len(checkpoints)-1].Checkpoint
		if len(saved.Failover) == 1 {
			state := saved.Failover[0]
			if len(state.History) != 1 || state.Connection != "backup" {
				f.fail("consumed chain was not carried")
			}
		}
	}
	// Two unavailable connections exhaust the request's one-switch bound.
	events = nil
	calls = nil
	heldBackup := run.PolicyConnections["backup"]
	failedBackup := heldBackup
	if agent {
		copy := *heldBackup.Agent
		copy.Run = func(context.Context, adapter.AgentRequest, adapter.AgentCallbacks) error {
			calls = append(calls, "backup")
			return failure.Err("unavailable", "agent", "unknown")
		}
		failedBackup.Agent = &copy
	} else {
		failedBackup.Model = func(context.Context, string, relay.Request) (*http.Response, error) {
			calls = append(calls, "backup")
			return &http.Response{StatusCode: 503, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
		}
	}
	run.PolicyConnections["backup"] = failedBackup
	run.Prompt = ""
	run.Context.CurrentRequest = &runcontext.CurrentRequest{}
	if err := inner.Run(ctx, &run); err != nil {
		return nil, err
	}
	refusals := 0
	for _, e := range events {
		if e.Guardrail != nil && e.Guardrail.Reason != nil && *e.Guardrail.Reason == "failover refused" {
			refusals++
			_, items, err := ReadVector("failover/refused-mid-delegation.json")
			if err != nil {
				return nil, err
			}
			var expected contract.RunEvent
			if err := json.Unmarshal(items[2], &expected); err != nil {
				return nil, err
			}
			if !reflect.DeepEqual(e, expected) {
				f.fail("exhaustion differs from schema vector")
			}
			raw, _ := json.Marshal(e)
			var decoded contract.RunEvent
			if err := json.Unmarshal(raw, &decoded); err != nil {
				f.fail("invalid exhaustion event: %v", err)
			}
		}
	}
	if len(calls) != 2 || refusals != 1 {
		f.fail("exhaustion: calls %v, refusals %d", calls, refusals)
	}
	run.PolicyConnections["backup"] = heldBackup
	run.Prompt = file.Request
	run.Context.CurrentRequest = &runcontext.CurrentRequest{Message: file.Request, Input: file.Request}
	out = append(out, f.list...)

	// A refusal is final and never reaches the fallback.
	f = findings{check: prefix + "refusal"}
	events = nil
	calls = nil
	run.Emit = func(e contract.RunEvent) { events = append(events, e) }
	if agent {
		run.Agent = &adapter.Agent{Family: "codex", Model: "primary-model", Run: func(context.Context, adapter.AgentRequest, adapter.AgentCallbacks) error {
			calls = append(calls, "primary")
			return failure.Err("permission", "agent", "refused")
		}}
		run.Model = nil
	} else {
		run.Agent = nil
		run.Wire = "openai-compatible"
		run.Model = func(context.Context, string, relay.Request) (*http.Response, error) {
			calls = append(calls, "primary")
			return &http.Response{StatusCode: 403, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
		}
	}
	if err = inner.Run(ctx, &run); err != nil {
		return nil, err
	}
	if len(calls) != 1 || len(ofType(events, func(e contract.RunEvent) *contract.RunEventConnectionSwitched { return e.ConnectionSwitched })) != 0 {
		f.fail("refusal dispatch %v", calls)
	}
	out = append(out, f.list...)

	// Admission rejects a fallback in another family before dispatch.
	f = findings{check: prefix + "cross-family"}
	events = nil
	calls = nil
	run.Emit = func(e contract.RunEvent) { events = append(events, e) }
	if agent {
		run.Agent = &adapter.Agent{Family: "codex", Model: "primary-model", Run: func(context.Context, adapter.AgentRequest, adapter.AgentCallbacks) error {
			calls = append(calls, "primary")
			return nil
		}}
		run.PolicyConnections["backup"] = adapter.PolicyConnection{Agent: &adapter.Agent{Family: "anthropic", Model: "backup-model", Run: func(context.Context, adapter.AgentRequest, adapter.AgentCallbacks) error { return nil }}}
	} else {
		run.PolicyConnections["backup"] = adapter.PolicyConnection{Wire: "anthropic", ModelName: "backup-model", Model: backup.Model}
	}
	if err = inner.Run(ctx, &run); err != nil {
		return nil, err
	}
	found := false
	for _, e := range events {
		if e.Guardrail != nil && e.Guardrail.Reason != nil && *e.Guardrail.Reason == "failover refused" {
			found = true
		}
	}
	if len(calls) != 0 || !found {
		f.fail("cross-family admission calls %v, guardrail %t", calls, found)
	}
	out = append(out, f.list...)

	if agent {
		f = findings{check: "20 agent delegation refusal"}
		events = nil
		calls = nil
		run.Emit = func(e contract.RunEvent) { events = append(events, e) }
		run.Context.Objective = "Delegated review"
		run.PolicyConnections["backup"] = backup
		run.Agent = &adapter.Agent{Family: "codex", Model: "primary-model", Run: func(context.Context, adapter.AgentRequest, adapter.AgentCallbacks) error {
			calls = append(calls, "primary")
			return failure.Err("unavailable", "agent", "unknown")
		}}
		if err = inner.Run(ctx, &run); err != nil {
			return nil, err
		}
		found = false
		for _, e := range events {
			if e.Guardrail != nil && e.Guardrail.Reason != nil && *e.Guardrail.Reason == "failover refused" {
				found = true
			}
		}
		if !found || len(calls) != 1 {
			f.fail("delegation refusal calls %v, guardrail %t", calls, found)
		}
		out = append(out, f.list...)
	}
	if !agent {
		f = findings{check: "20 partial usage"}
		_, items, err := ReadVector("failover/usage-partial.json")
		if err != nil {
			return nil, err
		}
		var expected contract.Usage
		if err := json.Unmarshal(items[0], &expected); err != nil {
			return nil, err
		}
		for _, family := range []string{"openai", "anthropic", "gemini"} {
			events = nil
			wire := family
			if family == "openai" {
				wire = "openai-compatible"
			}
			partial := Run{ModelName: "author-model", Wire: wire, Prompt: file.Request, AllowConversation: true, Interactive: true, Critic: "off", Gate: host.gate.Calls(), Tools: host.offer.Tools, Context: &runcontext.Input{Connection: "primary"}, Model: func(context.Context, string, relay.Request) (*http.Response, error) {
				return providerAnswer(family, file.Proposal, false), nil
			}, Emit: func(e contract.RunEvent) { events = append(events, e) }}
			if err := inner.Run(ctx, &partial); err != nil {
				return nil, err
			}
			usages := ofType(events, func(e contract.RunEvent) *contract.RunEventUsage { return e.Usage })
			if len(usages) != 1 {
				f.fail("%s usage events %d", family, len(usages))
				continue
			}
			for _, row := range usages[0].Usage.Rows {
				if row.Source == "model" && !reflect.DeepEqual(row.Tokens, expected.Rows[0].Tokens) {
					f.fail("%s partial tokens %+v", family, row.Tokens)
				}
			}
		}
		out = append(out, f.list...)
	}
	f = findings{check: map[bool]string{false: "20 child fallback", true: "20 agent child refusal"}[agent]}
	events = nil
	calls = nil
	declaration := turn.DelegationTool(turn.TaskProfiles)
	childHost, err := openScenario(ctx, server, ScenarioEndpoint{Scenario: "policy-table", AnswerAs: "whole"}, []runcontext.HostTool{{Name: declaration.Name, Description: declaration.Description, InputSchema: declaration.InputSchema}}, true)
	if err != nil {
		return nil, err
	}
	parentCalls := 0
	taskArgs := `{"tasks":[{"title":"Analyze","objective":"Check the supplied claims.","profile":"analyze"}]}`
	childRun := Run{ModelName: "parent", Wire: "openai-compatible", Prompt: file.Request, AllowConversation: true, Interactive: true, Critic: "off", Gate: childHost.gate.Calls(), Tools: childHost.offer.Tools, Context: &runcontext.Input{Connection: "parent"}, Policy: runcontext.PolicyLayer{"models": map[string]any{"child.analyze": map[string]any{"provider": "child-primary", "fallback": []any{"child-backup"}}}}, Emit: func(e contract.RunEvent) { events = append(events, e) }}
	childRun.Children = func(context.Context, contract.DelegatedTaskProfile, func(contract.RunEvent)) (*gate.Calls, []contract.ToolOfferRuntimeTool, error) {
		return childHost.gate.Calls(), nil, nil
	}
	binding := func(id string) adapter.PolicyConnection {
		b := adapter.PolicyConnection{ModelName: id, Wire: "openai-compatible"}
		if agent {
			b.Agent = &adapter.Agent{Family: "codex", Model: id, Effort: "high", Run: func(ctx context.Context, r adapter.AgentRequest, c adapter.AgentCallbacks) error {
				calls = append(calls, id)
				if id == "child-primary" {
					return failure.Err("timeout", "agent", "unknown")
				}
				if id == "parent" && parentCalls == 0 {
					parentCalls++
					for _, tool := range r.Tools {
						if tool.Name == "delegate_tasks" || strings.HasPrefix(tool.Description, "delegate_tasks:") {
							_, err := c.Tool(ctx, adapter.AgentToolCall{ID: "task-call", Name: tool.Name, Arguments: json.RawMessage(taskArgs)})
							return err
						}
					}
				}
				return c.Event(ctx, adapter.AgentEvent{Type: "message", ID: "answer", Text: file.Proposal, Phase: "final"})
			}}
		} else {
			b.Model = func(context.Context, string, relay.Request) (*http.Response, error) {
				calls = append(calls, id)
				if id == "child-primary" {
					return nil, failure.Err("timeout", "model", "unknown")
				}
				if id == "parent" && parentCalls == 0 {
					parentCalls++
					body := map[string]any{"id": "response", "object": "chat.completion", "model": id, "choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{"id": "task-call", "type": "function", "function": map[string]any{"name": "delegate_tasks", "arguments": taskArgs}}}}, "finish_reason": "tool_calls"}}, "usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 5}}
					data, _ := json.Marshal(body)
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(data)))}, nil
				}
				return providerAnswer("openai", file.Proposal, false), nil
			}
		}
		return b
	}
	parent := binding("parent")
	childRun.Model = parent.Model
	childRun.Agent = parent.Agent
	childRun.PolicyConnections = map[string]adapter.PolicyConnection{"child-primary": binding("child-primary"), "child-backup": binding("child-backup")}
	if err := delegate.With(inner, delegate.Options{}).Run(ctx, &childRun); err != nil {
		return nil, err
	}
	backupCalled := false
	refused := false
	for _, id := range calls {
		backupCalled = backupCalled || id == "child-backup"
	}
	for _, e := range events {
		if e.Guardrail != nil && e.Guardrail.Reason != nil && *e.Guardrail.Reason == "failover refused" {
			refused = true
		}
	}
	if agent {
		if backupCalled || !refused {
			f.fail("agent child fallback: calls %v refused %v", calls, refused)
		}
	} else {
		if !backupCalled {
			f.fail("child fallback never called: %v", calls)
		}
		usages := ofType(events, func(e contract.RunEvent) *contract.RunEventUsage { return e.Usage })
		if len(usages) == 0 {
			f.fail("no parent usage")
		} else {
			total := int64(0)
			found := false
			for _, row := range usages[len(usages)-1].Usage.Rows {
				total += row.Requests
				if row.Connection == "child-backup" && row.Role == "child.analyze" && row.Model != nil && *row.Model == "child-backup" && row.Tokens != nil {
					found = true
				}
			}
			if total != int64(len(calls)) || !found {
				f.fail("parent ledger requests %d, calls %v, child present %v", total, calls, found)
			}
		}
	}
	out = append(out, f.list...)
	return out, nil
}

var Failover = ScenarioCheck{Scenario: "provider-failover", Names: failoverNames, Run: func(ctx context.Context, server *Server, _ string, inner Adapter) ([]Finding, error) {
	return failoverFindings(ctx, server, inner, false)
}}

func agentFailoverChecks(ctx context.Context, server *Server, _ AgentHost, _ func(Adapter) Adapter, inner Adapter) ([]Finding, error) {
	if server == nil {
		return nil, nil
	}
	return failoverFindings(ctx, server, inner, true)
}
