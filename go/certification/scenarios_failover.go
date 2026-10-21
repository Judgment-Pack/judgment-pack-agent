package certification

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
	"github.com/Judgment-Pack/judgment-pack-agent/go/relay"
)

var failoverNames = []string{"20 switch", "20 refusal", "20 cross-family", "20 usage"}
var AgentFailoverNames = []string{"20 agent switch", "20 agent refusal", "20 agent cross-family", "20 agent usage", "20 agent delegation refusal"}

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
