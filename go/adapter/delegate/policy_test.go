package delegate

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/turn"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
	"github.com/Judgment-Pack/judgment-pack-agent/go/relay"
)

func TestChildrenUseResolvedBindingsTiersAndConcurrency(t *testing.T) {
	for _, agent := range []bool{false, true} {
		t.Run(map[bool]string{false: "model", true: "agent"}[agent], func(t *testing.T) {
			steers := make(chan runcontext.SteerInput, 1)
			steers <- runcontext.SteerInput{Text: "Use the corrected requirement."}
			parentTier, childTier := "on", "off"
			if agent {
				parentTier, childTier = "high", "low"
			}
			layer := runcontext.PolicyLayer{"models": map[string]any{"author": map[string]any{"reasoning": parentTier}, "child.review": map[string]any{"model": "reviewer", "provider": "reviewer", "reasoning": childTier}}, "concurrency": map[string]any{"children": 1, "requests": 1}}
			session := runcontext.Session{Policy: layer, PolicyConnections: []string{"parent", "reviewer"}, Model: &runcontext.ModelSession{Family: "openai-compatible", Model: "parent", Thinking: parentTier}}
			if agent {
				session.Model = nil
				session.Agent = &runcontext.AgentSession{Model: "parent", Effort: parentTier}
			}
			parent, err := runcontext.NewSessionContext(session)
			if err != nil {
				t.Fatal(err)
			}
			defer parent.Close()
			if err := parent.Execution.Initialize(); err != nil {
				t.Fatal(err)
			}
			calls := 0
			selected := adapter.PolicyConnection{ModelName: "review-model", Wire: "openai-compatible", Model: func(context.Context, string, relay.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("{}"))}, nil
			}}
			run := &adapter.Run{Policy: layer, Thinking: parentTier, ModelName: "parent", Wire: "openai-compatible", Model: func(context.Context, string, relay.Request) (*http.Response, error) {
				t.Error("parent transport used by child")
				return nil, nil
			}, Emit: func(contract.RunEvent) {}, Context: &runcontext.Input{Connection: "parent", Steer: steers}}
			if agent {
				run.Model = nil
				run.Agent = &adapter.Agent{Model: "parent", Effort: parentTier, Run: func(context.Context, adapter.AgentRequest, adapter.AgentCallbacks) error {
					t.Error("parent agent used by child")
					return nil
				}}
				selected.Model = nil
				selected.Agent = &adapter.Agent{Model: "review-model", Run: func(_ context.Context, r adapter.AgentRequest, _ adapter.AgentCallbacks) error {
					calls++
					if r.Model != "review-model" || r.Effort != childTier {
						t.Errorf("agent wire: %+v", r)
					}
					return nil
				}}
			}
			run.PolicyConnections = map[string]adapter.PolicyConnection{"reviewer": selected}
			run.Children = func(context.Context, turn.TaskProfile, func(contract.RunEvent)) (*gate.Calls, []contract.ToolOfferRuntimeTool, error) {
				return nil, nil, nil
			}
			c := &coordinator{run: run, chat: runcontext.NewChatContext(""), models: map[string]string{}, shared: allowance{parent: parent}, runs: agentRuns{children: map[string]int{}}, account: newAccount()}
			defer c.chat.Dispose()
			if got := c.concurrency(); got != 1 {
				t.Fatalf("concurrency %d, want 1", got)
			}
			// Both limits independently constrain model scheduling.
			if !agent {
				for _, limits := range [][2]float64{{1, 2}, {2, 1}} {
					parent.ResolvedPolicy["concurrency.children"] = runcontext.PolicyMember{Value: limits[0], Layer: "run"}
					parent.ResolvedPolicy["concurrency.requests"] = runcontext.PolicyMember{Value: limits[1], Layer: "run"}
					if c.concurrency() != 1 {
						t.Fatal("one concurrency limit ignored")
					}
				}
			}
			profile := turn.TaskProfile{ID: "review"}
			if got := c.childModelName(profile); got != "review-model" {
				t.Fatalf("child record model %q", got)
			}
			c.inner = adapter.Func(func(ctx context.Context, child *adapter.Run) error {
				if child.Context.Steer != nil {
					t.Fatal("delegated child inherited the parent's steering queue")
				}
				named, err := child.Models()
				if err != nil {
					return err
				}
				sc := runcontext.Session{Policy: child.Policy, PolicyProject: child.PolicyProject, Context: child.Context, PolicyConnections: adapter.PolicyConnectionIDs(child), PolicyLegacyModels: &named, CallTool: func(context.Context, string, json.RawMessage) (runcontext.ToolResult, error) {
					return runcontext.ToolResult{}, nil
				}}
				if agent {
					sc.Agent = &runcontext.AgentSession{Model: child.Agent.Model, Effort: child.Agent.Effort}
				} else {
					sc.Model = &runcontext.ModelSession{Family: child.Wire, Model: child.ModelName, Thinking: child.Thinking}
				}
				opened, err := runcontext.ContextualSession(ctx, sc)
				if err != nil {
					return err
				}
				defer opened.Context.Close()
				if opened.Context.ResolvedPolicy["models.author.model"].Value != "reviewer" || opened.Context.ResolvedPolicy["models.author.reasoning"].Value != childTier {
					t.Fatalf("child admitted as parent: %+v", opened.Context.ResolvedPolicy)
				}
				if err := adapter.ApplyPolicy(child, opened.Context, named); err != nil {
					return err
				}
				if agent {
					r, err := child.Agent.Admit(ctx, adapter.AgentRequest{Phase: "author", Effort: child.Agent.Effort, Model: child.Agent.Delegated, Delegated: true})
					if err != nil {
						return err
					}
					return child.Agent.Run(ctx, r, adapter.AgentCallbacks{})
				}
				if child.Thinking != childTier {
					t.Fatalf("wire tier %q", child.Thinking)
				}
				response, err := child.Model(ctx, "chat/completions", relay.Request{Method: "POST", Body: []byte("{}")})
				if err == nil {
					response.Body.Close()
				}
				return err
			})
			task := &contract.DelegatedTask{ID: "child", Profile: "review"}
			p := &planned{budget: turn.TaskBudget{ModelRequests: 2, ToolCalls: 2}, visible: map[string]bool{}}
			if err := c.runChild(context.Background(), p, profile, task, "Review.", "Review.", 2, false, &meter{started: time.Now()}, &childRun{}); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("selected calls %d", calls)
			}
			if !agent && task.ModelRequests != 1 {
				t.Fatal("selected binding bypassed shared accounting")
			}
			select {
			case steer := <-steers:
				if steer.Text != "Use the corrected requirement." {
					t.Fatalf("parent steer changed to %q", steer.Text)
				}
			default:
				t.Fatal("delegated child drained the parent's steer")
			}
		})
	}
}

func TestOmittedChildAndSubagentReasoningStayOutOfChildPolicy(t *testing.T) {
	for _, profile := range []string{"review", "audit"} {
		t.Run(profile, func(t *testing.T) {
			role := "child." + profile
			if profile == "audit" {
				role = "subagent.audit"
			}
			layer := runcontext.PolicyLayer{"models": map[string]any{role: map[string]any{"model": "reviewer", "provider": "reviewer"}}}
			parent, err := runcontext.NewSessionContext(runcontext.Session{Policy: layer, PolicyConnections: []string{"parent", "reviewer"}, Model: &runcontext.ModelSession{Family: "openai-compatible", Model: "parent"}})
			if err != nil {
				t.Fatal(err)
			}
			defer parent.Close()
			run := &adapter.Run{Policy: layer, ModelName: "parent", Wire: "openai-compatible", Context: &runcontext.Input{Connection: "parent"}, PolicyConnections: map[string]adapter.PolicyConnection{"reviewer": {ModelName: "reviewer", Wire: "openai-compatible", Model: func(context.Context, string, relay.Request) (*http.Response, error) { return nil, nil }}}}
			if profile == "audit" {
				run.Subagents = []runcontext.Subagent{{ID: "audit"}}
			}
			c := &coordinator{run: run, shared: allowance{parent: parent}}
			_, _, childPolicy, err := c.childBinding(turn.TaskProfile{ID: profile})
			if err != nil {
				t.Fatal(err)
			}
			author := childPolicy["models"].(map[string]any)["author"].(map[string]any)
			if _, present := author["reasoning"]; present {
				t.Fatalf("omitted reasoning entered child policy: %#v", author)
			}
		})
	}
}
