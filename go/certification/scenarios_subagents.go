package certification

import (
	"context"
	"encoding/json"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/delegate"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/turn"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
	"github.com/Judgment-Pack/judgment-pack-agent/go/relay"
	"io"
	"net/http"
	"strings"
	"sync"
)

var subagentNames = []string{"13 declared sub-agents"}
var AgentSubagentNames = []string{"13 agent declared sub-agents"}

func subagentFindings(ctx context.Context, server *Server, inner Adapter, agent bool) ([]Finding, error) {
	name := "13 declared sub-agents"
	if agent {
		name = "13 agent declared sub-agents"
	}
	f := findings{check: name}
	version := "1"
	definition := runcontext.Subagent{ID: "auditor", Name: "Auditor", Purpose: "Check supplied evidence.", Instructions: "Use only the supplied evidence and cite it.", Source: contract.SubagentSource{Kind: "custom", ID: "project", Version: &version, Trust: "person"}, Tools: []string{"lookup"}, Skills: []string{"evidence"}, Model: "fixture", Reasoning: "low", Bounds: contract.SubagentBounds{ModelRequests: 5, AgentRuns: 1, ToolCalls: 2, Bytes: 24000, Milliseconds: 30000, ResultBytes: int64(runcontext.SubagentResultMinBytes)}}
	skill := runcontext.Skill{ID: "evidence", Name: "evidence", Description: "Check evidence.", Instructions: "SKILL EVIDENCE RULE: cite supplied sources.", Source: runcontext.SkillSource{Kind: "custom", ID: "project", Version: "1", Trust: "person"}, Tools: []string{"lookup"}}
	skill.PinnedDigest = runcontext.SkillDigest(skill)
	definition.PinnedDigest = runcontext.SubagentDigest(definition)
	declaration := turn.DelegationTool(turn.TaskProfiles)
	host, err := openScenario(ctx, server, ScenarioEndpoint{Scenario: "policy-table", AnswerAs: "whole"}, []runcontext.HostTool{{Name: declaration.Name, Description: declaration.Description, InputSchema: declaration.InputSchema}, {Name: "lookup", Effect: "read"}, {Name: "load_skill", Effect: "plan"}, {Name: "unload_skill", Effect: "plan"}}, true)
	if err != nil {
		return nil, err
	}
	var mu sync.Mutex
	var events []contract.RunEvent
	var requests []string
	var answers []string
	reads, parent, child := 0, 0, 0
	emit := func(e contract.RunEvent) { mu.Lock(); defer mu.Unlock(); events = append(events, e) }
	task := json.RawMessage(`{"tasks":[{"title":"Evidence","objective":"Check the evidence.","profile":"auditor"}]}`)
	steps := []struct {
		name string
		args json.RawMessage
	}{{"load_skill", json.RawMessage(`{"name":"evidence"}`)}, {"lookup", json.RawMessage(`{}`)}, {"delegate_tasks", task}}
	run := Run{ModelName: "fixture", Wire: "openai-compatible", Thinking: "low", Prompt: "Delegate the evidence check.", AllowConversation: true, Interactive: true, Critic: "off", Gate: host.gate.Calls(), Tools: nil, Subagents: []runcontext.Subagent{definition}, Skills: []runcontext.Skill{skill}, Context: &runcontext.Input{CurrentRequest: &runcontext.CurrentRequest{Message: "Delegate the evidence check.", Input: "Delegate the evidence check."}}, Emit: emit}
	run.HostTools = []runcontext.HostTool{{Name: "lookup", Effect: "read", Description: "Read evidence.", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`), Execute: func(context.Context, json.RawMessage) (runcontext.ToolResult, error) {
		reads++
		return runcontext.ToolResult{Content: []runcontext.Content{runcontext.TextContent("EVIDENCE: supplied facts.")}}, nil
	}}}
	tier := "off"
	if agent {
		tier = "none"
	}
	run.Policy = runcontext.PolicyLayer{"models": map[string]any{"child.auditor": map[string]any{"reasoning": tier}}}
	run.Children = func(context.Context, contract.DelegatedTaskProfile, func(contract.RunEvent)) (*gate.Calls, []contract.ToolOfferRuntimeTool, error) {
		return host.gate.Calls(), nil, nil
	}
	run.Model = func(_ context.Context, _ string, r relay.Request) (*http.Response, error) {
		requests = append(requests, string(r.Body))
		var request struct {
			Messages []struct {
				Role    string
				Content json.RawMessage
			}
		}
		_ = json.Unmarshal([]byte(r.Body), &request)
		isChild := false
		for _, m := range request.Messages {
			if m.Role == "system" && strings.Contains(string(m.Content), definition.Instructions) {
				isChild = true
			}
		}
		if isChild {
			var value map[string]any
			_ = json.Unmarshal(r.Body, &value)
			if value["reasoning_effort"] != "low" {
				f.fail("declared tier was shadowed by child-profile policy")
			}
		}
		tool := ""
		var args json.RawMessage
		if isChild {
			if child < len(steps) {
				tool, args = steps[child].name, steps[child].args
			}
			child++
		} else {
			if parent == 0 {
				tool, args = "delegate_tasks", task
			}
			parent++
		}
		message := map[string]any{"role": "assistant", "content": "Child evidence."}
		reason := "stop"
		if tool != "" {
			message["content"] = nil
			message["tool_calls"] = []any{map[string]any{"id": "call", "type": "function", "function": map[string]any{"name": tool, "arguments": string(args)}}}
			reason = "tool_calls"
		}
		raw, _ := json.Marshal(map[string]any{"id": "answer", "object": "chat.completion", "choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": reason}}})
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
	}
	if agent {
		run.Model = nil
		run.Agent = &adapter.Agent{Model: "fixture", Effort: "low", Run: func(ctx context.Context, r adapter.AgentRequest, c adapter.AgentCallbacks) error {
			raw, _ := json.Marshal(r)
			requests = append(requests, string(raw))
			isChild := strings.Contains(r.Instructions, definition.Instructions)
			if isChild && r.Effort != "low" {
				f.fail("declared tier was shadowed by child-profile policy")
			}
			call := func(name string, args json.RawMessage) error {
				for _, t := range r.Tools {
					if strings.HasPrefix(t.Description, name+":") {
						name = t.Name
						break
					}
				}
				result, err := c.Tool(ctx, adapter.AgentToolCall{ID: "call", Name: name, Arguments: args})
				raw, _ := json.Marshal(result)
				answers = append(answers, string(raw))
				return err
			}
			if !isChild && parent == 0 {
				parent++
				return call("delegate_tasks", task)
			}
			if isChild {
				for _, s := range steps {
					if err := call(s.name, s.args); err != nil {
						return err
					}
				}
			}
			return c.Event(ctx, adapter.AgentEvent{Type: "message", ID: "answer", Text: "Child evidence.", Phase: "final"})
		}}
	}
	if err := delegate.With(inner, delegate.Options{}).Run(ctx, &run); err != nil {
		return nil, err
	}
	if reads != 1 {
		f.fail("declared read called %d times", reads)
	}
	if len(requests) == 0 || !strings.Contains(requests[0], "DECLARED SUB-AGENTS") {
		f.fail("catalogue missing")
	}
	complete, refused, recorded := false, false, false
	for _, e := range events {
		if e.Error != nil {
			f.fail("run error: %s", e.Error.Message)
		}
		if e.Guardrail != nil && e.Guardrail.Reason != nil && *e.Guardrail.Reason == "subagent refused" {
			refused = true
		}
		if e.DelegatedTask != nil && e.DelegatedTask.Task.State == "complete" {
			complete = true
			s := e.DelegatedTask.Task.Subagent
			if s == nil || s.Digest != definition.PinnedDigest || s.Source.ID != "project" || e.DelegatedTask.Subagent == nil {
				f.fail("approved record missing")
			}
		}
		if e.ExecutionCheckpoint != nil && e.ExecutionCheckpoint.Checkpoint.Delegation != nil {
			for _, b := range e.ExecutionCheckpoint.Checkpoint.Delegation.Batches {
				for _, c := range b.Children {
					if c.Subagent != nil && c.Subagent.Digest == definition.PinnedDigest {
						recorded = true
					}
				}
			}
		}
	}
	if !complete || !refused || !recorded {
		f.fail("completion %v, child refusal %v, checkpoint record %v", complete, refused, recorded)
	}
	// The parent's actual answer, including any prose, holds the admitted result bound.
	reports := 0
	var visit func(any)
	visit = func(value any) {
		switch v := value.(type) {
		case string:
			var parsed any
			if json.Unmarshal([]byte(v), &parsed) == nil {
				visit(parsed)
			} else if strings.Contains(v, "\n") {
				for _, line := range strings.Split(v, "\n") {
					visit(line)
				}
			}
		case []any:
			for _, item := range v {
				visit(item)
			}
		case map[string]any:
			if tasks, ok := v["tasks"].([]any); ok {
				for _, item := range tasks {
					if task, ok := item.(map[string]any); ok && task["report"] != nil {
						reports++
						raw, _ := json.Marshal(task["report"])
						size := len(raw)
						if text, ok := task["result"].(string); ok {
							encoded, _ := json.Marshal(text)
							size += len(encoded)
						}
						if int64(size) > definition.Bounds.ResultBytes {
							f.fail("child report exceeds admitted resultBytes: %d > %d", size, definition.Bounds.ResultBytes)
						}
					}
				}
			}
			for _, item := range v {
				visit(item)
			}
		}
	}
	for _, raw := range append(append([]string{}, requests...), answers...) {
		visit(raw)
	}
	if reports == 0 {
		f.fail("no parent report checked against resultBytes")
	}
	// A queued or running record is restored as data, never as permission to re-run it.
	var saved *contract.Checkpoint
	for _, e := range events {
		if e.ExecutionCheckpoint != nil {
			cp := e.ExecutionCheckpoint.Checkpoint
			candidate := agent && cp.Handoff != nil && cp.Handoff.State == "children" && len(cp.Handoff.Tasks) > 0 && cp.Handoff.Tasks[0].State == "queued"
			if !agent && cp.Delegation != nil {
				for _, b := range cp.Delegation.Batches {
					for _, c := range b.Children {
						if c.Subagent != nil && (c.State == "queued" || c.State == "running") {
							candidate = true
						}
					}
				}
			}
			if candidate {
				raw, _ := json.Marshal(cp)
				var copy contract.Checkpoint
				_ = json.Unmarshal(raw, &copy)
				saved = &copy
				break
			}
		}
	}
	if saved == nil {
		f.fail("no restorable declared task checkpoint")
	} else {
		for _, mode := range []string{"withdrawn", "changed", "lower-tier", "lower-bound"} {
			reads, parent, child = 0, 1, 0
			events = nil
			requests = nil
			run.Subagents = nil
			if mode != "withdrawn" {
				held := definition
				held.Instructions += " Changed."
				if mode == "lower-tier" {
					held.Reasoning = tier
				}
				if mode == "lower-bound" {
					held.Bounds.ToolCalls = 1
				}
				if strings.HasPrefix(mode, "lower-") {
					held.PinnedDigest = runcontext.SubagentDigest(held)
				}
				run.Subagents = []runcontext.Subagent{held}
			}
			raw, _ := json.Marshal(saved)
			var copy contract.Checkpoint
			_ = json.Unmarshal(raw, &copy)
			run.Context.Execution = &copy
			if err := delegate.With(inner, delegate.Options{}).Run(ctx, &run); err != nil {
				return nil, err
			}
			if reads != 0 {
				f.fail("restored withdrawn definition dispatched")
			}
			said := false
			for _, e := range events {
				if e.Guardrail != nil && strings.Contains(e.Guardrail.Detail, "not dispatched again") {
					said = true
				}
				if e.Error != nil {
					f.fail("restore error: %s", e.Error.Message)
				}
			}
			if strings.HasPrefix(mode, "lower-") {
				record, report := false, false
				for _, e := range events {
					if e.ExecutionCheckpoint != nil && e.ExecutionCheckpoint.Checkpoint.Delegation != nil {
						for _, b := range e.ExecutionCheckpoint.Checkpoint.Delegation.Batches {
							for _, c := range b.Children {
								if c.State == "interrupted" && c.Problem != nil && strings.Contains(*c.Problem, "subagent changed") {
									record = true
								}
							}
						}
					}
					if e.ExecutionCheckpoint != nil {
						raw, _ := json.Marshal(e.ExecutionCheckpoint.Checkpoint.Delegation)
						report = report || strings.Contains(string(raw), "subagent changed")
					}
				}
				if !record || !report {
					f.fail("%s: interrupted record %v, report %v", mode, record, report)
				}
			}
			if !said {
				f.fail("withdrawn definition not reported")
			}
			if strings.HasPrefix(mode, "lower-") {
				copy.Policy["budget.toolCalls"] = contract.PolicyMember{Value: json.RawMessage(`999`), Layer: "run"}
				run.Context.Execution = &copy
				events = nil
				if err := delegate.With(inner, delegate.Options{}).Run(ctx, &run); err != nil {
					return nil, err
				}
				refused := false
				for _, e := range events {
					if e.Error != nil && strings.Contains(e.Error.Message, "policy differs") {
						refused = true
					}
				}
				if !refused {
					f.fail("unrelated saved policy change accepted with %s", mode)
				}
			}

		}
	}
	// Admission failures stay off the offer on this same engine and scripted source.
	run.Context.Execution = nil
	for _, mode := range []string{"tool", "skill", "digest", "bounds"} {
		held := definition
		switch mode {
		case "tool":
			held.Tools = []string{"missing"}
		case "skill":
			held.Skills = []string{"missing"}
		case "digest":
			held.Instructions += " Changed."
		case "bounds":
			held.Bounds.Bytes = 180001
		}
		if mode != "digest" {
			held.PinnedDigest = runcontext.SubagentDigest(held)
		}
		run.Subagents = []runcontext.Subagent{held}
		reads, parent, child = 0, 1, 0
		events = nil
		requests = nil
		if err := delegate.With(inner, delegate.Options{}).Run(ctx, &run); err != nil {
			return nil, err
		}
		withheld := false
		for _, e := range events {
			if e.Guardrail != nil && e.Guardrail.Reason != nil && *e.Guardrail.Reason == "subagent withheld" {
				withheld = true
			}
		}
		if !withheld || reads != 0 || len(requests) != 1 || strings.Contains(requests[0], "DECLARED SUB-AGENTS") {
			f.fail("%s admission was not withheld", mode)
		}
	}
	// Re-approval admits the changed bundle on this adapter, with its new record.
	definition.Instructions += " Changed."
	definition.PinnedDigest = runcontext.SubagentDigest(definition)
	run.Subagents = []runcontext.Subagent{definition}
	reads, parent, child = 0, 0, 0
	events, requests = nil, nil
	if err := delegate.With(inner, delegate.Options{}).Run(ctx, &run); err != nil {
		return nil, err
	}
	reapproved := false
	for _, e := range events {
		if e.DelegatedTask != nil && e.DelegatedTask.Task.State == "complete" && e.DelegatedTask.Subagent != nil && e.DelegatedTask.Subagent.Digest == definition.PinnedDigest {
			reapproved = true
		}
	}
	if reads != 1 || !reapproved {
		f.fail("changed definition did not run after re-approval")
	}
	// A model cannot add a larger bound to the task, even for an admitted definition.
	task = json.RawMessage(`{"tasks":[{"title":"Evidence","objective":"Check the evidence.","profile":"auditor","bounds":{"bytes":999999}}]}`)
	reads, parent, child = 0, 0, 0
	events, requests = nil, nil
	runErr := delegate.With(inner, delegate.Options{}).Run(ctx, &run)
	feedback, _ := json.Marshal(events)
	if reads != 0 || (runErr == nil && !strings.Contains(string(feedback)+strings.Join(requests, ""), "Delegate one to three focused tasks")) {
		f.fail("task-supplied bound was not refused")
	}
	for _, e := range events {
		if e.DelegatedTask != nil {
			f.fail("task-supplied bound reached child dispatch")
		}
	}
	return f.list, nil
}

var Subagents = ScenarioCheck{Scenario: "subagents", Names: subagentNames, Run: func(ctx context.Context, s *Server, _ string, a Adapter) ([]Finding, error) {
	return subagentFindings(ctx, s, a, false)
}}

func agentSubagentChecks(ctx context.Context, s *Server, _ AgentHost, _ func(Adapter) Adapter, a Adapter) ([]Finding, error) {
	return subagentFindings(ctx, s, a, true)
}
