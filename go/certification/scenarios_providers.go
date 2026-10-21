package certification

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/delegate"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/turn"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
	"github.com/Judgment-Pack/judgment-pack-agent/go/relay"
)

var providerNames = []string{"12 provider", "12 refused", "12 child"}
var AgentProviderNames = []string{"12 agent provider", "12 agent refused", "12 agent child"}

type ProviderMeasurement struct {
	Adapter, Author, Critic, Policy string
	Requests, Checks                int
}

var providerMeasurements struct {
	sync.Mutex
	rows []ProviderMeasurement
}

func TakeProviderMeasurement() []ProviderMeasurement {
	providerMeasurements.Lock()
	defer providerMeasurements.Unlock()
	rows := providerMeasurements.rows
	providerMeasurements.rows = nil
	return rows
}

// Hand-authored wire answers. No fixtures are captured from a run of the implementation.
func providerAnswer(family, text string, child bool) *http.Response {
	var body any
	if family == "anthropic" {
		body = map[string]any{"id": "message", "type": "message", "role": "assistant", "model": "review-model", "content": []any{map[string]any{"type": "text", "text": text}}, "stop_reason": "end_turn", "usage": map[string]any{"input_tokens": 10, "output_tokens": 5}}
	} else if family == "gemini" {
		body = map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"role": "model", "parts": []any{map[string]any{"text": text}}}, "finishReason": "STOP"}}, "usageMetadata": map[string]any{"promptTokenCount": 10, "candidatesTokenCount": 5, "totalTokenCount": 15}}
	} else {
		message := map[string]any{"role": "assistant", "content": text}
		reason := "stop"
		if child {
			message["content"] = nil
			message["tool_calls"] = []any{map[string]any{"id": "task-call", "type": "function", "function": map[string]any{"name": "delegate_tasks", "arguments": `{"tasks":[{"title":"Review","objective":"Check the supplied claims.","profile":"review"}]}`}}}
			reason = "tool_calls"
		}
		body = map[string]any{"id": "response", "object": "chat.completion", "model": "author-model", "choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": reason}}, "usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}}
	}
	raw, _ := json.Marshal(body)
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(raw)))}
}
func providerFindings(ctx context.Context, server *Server, inner Adapter, agent bool) ([]Finding, error) {
	var file struct{ Request, Proposal, Findings string }
	if err := ScenarioFile("cross-provider", &file); err != nil {
		return nil, err
	}
	prefix := "12 "
	engine := "eino"
	authorFamily := "openai"
	if agent {
		prefix = "12 agent "
		engine = "agent"
		authorFamily = "codex"
	}
	var out []Finding
	families := []string{"anthropic", "gemini", "openai"}
	if agent {
		families = []string{"anthropic"}
	}
	for _, family := range families {
		for _, mode := range []string{"other", "reviewer", "refused", "child", "subagent"} {
			if family == "openai" && mode != "reviewer" {
				continue
			}
			child := mode == "child" || mode == "subagent"
			name := "provider"
			if mode == "refused" || child {
				name = mode
				if child {
					name = "child"
				}
			}
			f := findings{check: prefix + name}
			declaration := turn.DelegationTool(turn.TaskProfiles)
			host, err := openScenario(ctx, server, ScenarioEndpoint{Scenario: "policy-table", AnswerAs: "whole"}, []runcontext.HostTool{{Name: declaration.Name, Description: declaration.Description, InputSchema: declaration.InputSchema}}, true)
			if err != nil {
				return nil, err
			}
			var mu sync.Mutex
			var events []contract.RunEvent
			var sent []string
			var bodies []map[string]any
			record := func(id string, raw []byte) {
				mu.Lock()
				defer mu.Unlock()
				sent = append(sent, id)
				var body map[string]any
				_ = json.Unmarshal(raw, &body)
				bodies = append(bodies, body)
			}
			role := "critic"
			choice := mode
			if mode == "refused" || child {
				choice = "other"
			}
			if child {
				role = "child.review"
				if mode == "subagent" {
					role = "subagent.review"
				}
			}
			run := Run{ModelName: "author-model", Wire: "openai-compatible", Thinking: "ultra", Prompt: file.Request, TestPrompt: "Review the pack.", AllowConversation: true, Critic: "on", Gate: host.gate.Calls(), Tools: host.offer.Tools, Context: &runcontext.Input{Connection: "author", CurrentRequest: &runcontext.CurrentRequest{Message: file.Request, Input: file.Request}}, Policy: runcontext.PolicyLayer{"models": map[string]any{role: map[string]any{"provider": choice}}}, Emit: func(e contract.RunEvent) { mu.Lock(); defer mu.Unlock(); events = append(events, e) }}
			authorCalls := 0
			run.Model = func(_ context.Context, _ string, r relay.Request) (*http.Response, error) {
				record("author", []byte(r.Body))
				authorCalls++
				return providerAnswer("openai", file.Proposal, child && authorCalls == 1), nil
			}
			tiers := &contract.CapabilityEndpointCapabilitiesTiers{Low: "low", Medium: "medium", High: "high"}
			if family == "gemini" {
				tiers = &contract.CapabilityEndpointCapabilitiesTiers{Low: "1024", Medium: "2048", High: "4096"}
			}
			wireFamily := family
			if family == "openai" {
				wireFamily = "openai-compatible"
			}
			binding := adapter.PolicyConnection{ModelName: "review-model", Wire: wireFamily, Capabilities: &contract.CapabilityEndpointCapabilities{Tiers: tiers}, Model: func(_ context.Context, _ string, r relay.Request) (*http.Response, error) {
				record("reviewer", []byte(r.Body))
				return providerAnswer(family, file.Findings, false), nil
			}}
			if agent {
				makeAgent := func(id, family, model string) *adapter.Agent {
					return &adapter.Agent{Family: family, Model: model, Effort: "high", Run: func(_ context.Context, r adapter.AgentRequest, c adapter.AgentCallbacks) error {
						raw, _ := json.Marshal(r)
						record(id, raw)
						if id == "author" && child && authorCalls == 0 {
							authorCalls++
							for _, tool := range r.Tools {
								if strings.HasPrefix(tool.Description, "delegate_tasks:") || tool.Name == "delegate_tasks" {
									_, err := c.Tool(ctx, adapter.AgentToolCall{ID: "task-call", Name: tool.Name, Arguments: json.RawMessage(`{"tasks":[{"title":"Review","objective":"Check the supplied claims.","profile":"review"}]}`)})
									return err
								}
							}
						}
						text := file.Proposal
						if id == "reviewer" {
							text = file.Findings
						}
						return c.Event(ctx, adapter.AgentEvent{Type: "message", ID: "reply", Text: text, Phase: "final"})
					}}
				}
				run.Agent = makeAgent("author", "codex", "author-model")
				run.Model = nil
				binding.Agent = makeAgent("reviewer", family, "review-model")
				binding.Agent.Tiers = map[string]string{"low": "low", "medium": "medium", "high": "medium"}
				binding.Model = nil
			}
			if mode != "refused" {
				run.PolicyConnections = map[string]adapter.PolicyConnection{"reviewer": binding}
			}
			target := inner
			if child {
				run.Critic = "off"
				run.Interactive = true
				run.Children = func(_ context.Context, _ contract.DelegatedTaskProfile, _ func(contract.RunEvent)) (*gate.Calls, []contract.ToolOfferRuntimeTool, error) {
					return host.gate.Calls(), nil, nil
				}
				target = delegate.With(inner, delegate.Options{})
			}
			if err = target.Run(ctx, &run); err != nil {
				return nil, err
			}
			if mode == "refused" {
				if len(sent) != 0 {
					f.fail("unmet policy sent %d requests", len(sent))
				}
				found := false
				for _, e := range events {
					if e.Guardrail != nil && e.Guardrail.Reason != nil && *e.Guardrail.Reason == "policy refused" && e.Guardrail.Tool == "models.critic.provider" {
						found = true
					}
				}
				if !found {
					f.fail("missing named provider refusal")
				}
			} else if child {
				found := false
				for _, e := range events {
					if e.DelegatedTask != nil {
						if e.DelegatedTask.Provider == nil || e.DelegatedTask.Provider.Family != family {
							f.fail("child family unrecorded")
						}
						if e.DelegatedTask.Task.State == "complete" {
							found = true
						}
					}
				}
				if !found {
					raw, _ := json.Marshal(events)
					f.fail("child did not return data: %s", raw)
				}
			} else {
				if len(sent) != 2 || sent[0] != "author" || sent[1] != "reviewer" {
					f.fail("wrong dispatch %v", sent)
				}
				checks := 0
				found := false
				for _, e := range events {
					if e.Critique != nil {
						found = true
						checks = len(e.Critique.Checks)
						if e.Critique.Provider == nil || e.Critique.Provider.Connection != "reviewer" || e.Critique.Provider.Family != family {
							f.fail("critic family unrecorded")
						}
					}
				}
				if !found {
					f.fail("critic did not answer")
				}
				checkpoint := lastCheckpointOf(events)
				recorded := false
				if checkpoint != nil {
					for _, row := range checkpoint.CrossProvider {
						if row.Role == "critic" && row.Relation == map[bool]string{true: "same", false: "other"}[family == authorFamily] && row.Provider.Family == family {
							recorded = true
						}
					}
				}
				if !recorded {
					f.fail("crossProvider report missing")
				}
				if len(bodies) > 1 {
					if agent {
						if bodies[1]["effort"] != "medium" {
							f.fail("agent effort map ignored")
						}
					} else if family == "openai" {
						if bodies[0]["reasoning_effort"] != "xhigh" || bodies[1]["reasoning_effort"] != "high" {
							f.fail("same-family critic ignored its connection tier override")
						}
					} else if family == "gemini" {
						config, _ := bodies[1]["generationConfig"].(map[string]any)
						thinking, _ := config["thinkingConfig"].(map[string]any)
						if thinking["thinkingBudget"] != float64(4096) {
							f.fail("encoder ignored tier override")
						}
					} else {
						config, _ := bodies[1]["output_config"].(map[string]any)
						if config["effort"] != "high" {
							f.fail("encoder ignored tier override")
						}
					}
				}
				providerMeasurements.Lock()
				providerMeasurements.rows = append(providerMeasurements.rows, ProviderMeasurement{engine, authorFamily, family, mode, len(sent), checks})
				providerMeasurements.Unlock()
			}
			out = append(out, f.list...)
		}
	}
	return out, nil
}

var Providers = ScenarioCheck{Scenario: "cross-provider", Names: providerNames, Run: func(ctx context.Context, server *Server, _ string, inner Adapter) ([]Finding, error) {
	return providerFindings(ctx, server, inner, false)
}}

func agentProviderChecks(ctx context.Context, server *Server, _ AgentHost, _ func(Adapter) Adapter, inner Adapter) ([]Finding, error) {
	return providerFindings(ctx, server, inner, true)
}
