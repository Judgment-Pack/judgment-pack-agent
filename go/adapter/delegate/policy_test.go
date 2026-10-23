package delegate

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
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
				request := adapter.NewRequestSession(child, opened.Context.ResolvedPolicy)
				if agent {
					r, err := request.AgentCapability().Admit(ctx, adapter.AgentRequest{Phase: "author", Effort: request.Reasoning("author"), Model: request.AgentCapability().DelegatedModel(), Delegated: true})
					if err != nil {
						return err
					}
					return request.AgentCapability().Run(ctx, r, adapter.AgentCallbacks{})
				}
				if request.Reasoning("author") != childTier {
					t.Fatalf("wire tier %q", request.Reasoning("author"))
				}
				response, err := request.CallModel(ctx, "chat/completions", relay.Request{Method: "POST", Body: []byte("{}")})
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

// The request boundary, rather than childPolicy alone, must retain role admission.
func TestDelegatedRequestReasoning(t *testing.T) {
	discovered := func(tier string) map[string]any {
		reasoning := map[string]any{"supported": []any{"high"}}
		if tier != "" {
			reasoning["default"] = tier
		}
		return map[string]any{"reasoning": reasoning}
	}
	for _, agent := range []bool{false, true} {
		for _, profile := range []string{"review", "audit"} {
			for _, setting := range []string{"omitted", "discovered", "explicit", "table", "absent"} {
				t.Run(map[bool]string{false: "model", true: "agent"}[agent]+"/"+profile+"/"+setting, func(t *testing.T) {
					role := "child." + profile
					if profile == "audit" {
						role = "subagent.audit"
					}
					author, selected := map[string]any{}, map[string]any{"model": "fixture"}
					rawTier := "low"
					want := runcontext.PolicyMember{Value: "low", Layer: "table", Source: "layer"}
					switch setting {
					case "absent":
						rawTier = ""
						want = runcontext.PolicyMember{Layer: "table", Source: "model-default"}
						if profile == "audit" {
							want = runcontext.PolicyMember{Value: "low", Layer: "table", Source: "layer"}
						}
					case "omitted":
						author["discovered"], selected["discovered"] = discovered(""), discovered("")
						want = runcontext.PolicyMember{Layer: "table", Source: "model-default"}
					case "discovered":
						author["discovered"], selected["discovered"] = discovered("high"), discovered("high")
						want = runcontext.PolicyMember{Value: "high", Layer: "run", Source: "discovered"}
						if profile == "review" {
							want.Layer = "table"
						}
					case "explicit":
						author["reasoning"], selected["reasoning"] = "high", "high"
						author["discovered"], selected["discovered"] = discovered(""), discovered("")
						want = runcontext.PolicyMember{Value: "high", Layer: "run", Source: "layer"}
					}
					layer := runcontext.PolicyLayer{"models": map[string]any{"author": author, role: selected}}
					parentInput := runcontext.Session{Policy: layer, Model: &runcontext.ModelSession{Family: "openai-compatible", Model: "fixture", Thinking: rawTier}}
					if profile == "audit" {
						parentInput.Subagents = []runcontext.Subagent{{ID: "audit", Model: "fixture", Reasoning: "low"}}
					}
					if agent {
						parentInput.Model = nil
						parentInput.Agent = &runcontext.AgentSession{Model: "fixture", Effort: rawTier}
					}
					parent, err := runcontext.NewSessionContext(parentInput)
					if err != nil {
						t.Fatal(err)
					}
					defer parent.Close()
					if err = parent.Execution.Initialize(); err != nil {
						t.Fatal(err)
					}
					before := parent.ResolvedPolicy.Record()
					if !reflect.DeepEqual(parent.ResolvedPolicy["models."+role+".reasoning"], want) {
						t.Fatalf("role before: %+v", parent.ResolvedPolicy["models."+role+".reasoning"])
					}
					run := &adapter.Run{ModelName: "fixture", Wire: "openai-compatible", Thinking: rawTier, Policy: layer, Context: &runcontext.Input{Connection: "fixture"}, Emit: func(contract.RunEvent) {}, Model: func(context.Context, string, relay.Request) (*http.Response, error) { return nil, nil }}
					if profile == "audit" {
						run.Subagents = parentInput.Subagents
					}
					if agent {
						run.Model = nil
						run.Agent = &adapter.Agent{Model: "fixture", Effort: rawTier, Run: func(context.Context, adapter.AgentRequest, adapter.AgentCallbacks) error { return nil }}
					}
					run.Children = func(context.Context, turn.TaskProfile, func(contract.RunEvent)) (*gate.Calls, []contract.ToolOfferRuntimeTool, error) {
						return nil, nil, nil
					}
					c := &coordinator{run: run, chat: runcontext.NewChatContext(""), models: map[string]string{}, shared: allowance{parent: parent}, runs: agentRuns{children: map[string]int{}}, account: newAccount()}
					defer c.chat.Dispose()
					observed := 0
					c.inner = adapter.Func(func(ctx context.Context, child *adapter.Run) error {
						named, err := child.Models()
						if err != nil {
							return err
						}
						input := runcontext.Session{Policy: child.Policy, PolicyProject: child.PolicyProject, Context: child.Context, PolicyConnections: adapter.PolicyConnectionIDs(child), PolicyLegacyModels: &named}
						raw := adapter.ReadAdmissionReasoning(child)
						if agent {
							input.Agent = &runcontext.AgentSession{Model: child.Agent.Model, Effort: raw.Effort}
						} else {
							input.Model = &runcontext.ModelSession{Family: child.Wire, Model: child.ModelName, Thinking: raw.Thinking}
						}
						opened, err := runcontext.ContextualSession(ctx, input)
						if err != nil {
							return err
						}
						defer opened.Context.Close()
						request := adapter.NewRequestSession(child, opened.Context.ResolvedPolicy)
						observed++
						tier, _ := want.Value.(string)
						if got := request.Reasoning("author"); got != tier {
							t.Errorf("request reasoning %q, want %q", got, tier)
						}
						if got := opened.Context.ResolvedPolicy["models.author.reasoning"]; !reflect.DeepEqual(got, want) {
							t.Errorf("child provenance %+v, want %+v", got, want)
						}
						for key, value := range parent.ResolvedPolicy {
							if strings.HasPrefix(key, "models."+role+".discovered.") {
								mapped := "models.author." + strings.TrimPrefix(key, "models."+role+".")
								if !reflect.DeepEqual(opened.Context.ResolvedPolicy[mapped], value) {
									t.Errorf("discovery %s: %+v, want %+v", mapped, opened.Context.ResolvedPolicy[mapped], value)
								}
							}
						}
						return nil
					})
					planned := &planned{budget: turn.TaskBudget{ModelRequests: 2, ToolCalls: 2}, visible: map[string]bool{}}
					if profile == "audit" {
						planned.subagent = &runcontext.Subagent{ID: "audit", Model: "fixture", Bounds: contract.SubagentBounds{Bytes: 12000, ResultBytes: 3000, Milliseconds: 30000}}
					}
					if err := c.runChild(context.Background(), planned, turn.TaskProfile{ID: profile}, &contract.DelegatedTask{ID: "child", Profile: profile}, "Check evidence.", "Check evidence.", 2, false, &meter{started: time.Now()}, &childRun{}); err != nil {
						t.Fatal(err)
					}
					saved, err := parent.Snapshot("stopped")
					if err != nil {
						t.Fatal(err)
					}
					saved.Policy = parent.ResolvedPolicy.Record()
					pending := contract.DelegatedTaskTaskRequest{Title: "Check evidence", Objective: "Check supplied evidence.", Profile: profile}
					if profile == "audit" {
						pending.Subagent = &contract.SubagentRecord{ID: "audit", Digest: "sha256:" + strings.Repeat("0", 64), Source: contract.SubagentRecordSource{Kind: "custom", ID: "project", Version: strPtr("1"), Trust: "person"}}
					}
					saved.Handoff = &contract.CheckpointHandoff{CallID: "call-resume", Batch: 1, State: "released", Tasks: []contract.DelegatedTask{}, Request: contract.DelegatedTaskRequest{Tasks: []contract.DelegatedTaskTaskRequest{pending}}}
					// A saved handoff constructs children before the parent's next
					// invocation, without a live parent context. This also covers
					// built-in profiles whose binding selection does not use saved roles.
					run.Context.Execution = &saved
					c.shared.parent = nil
					if err := c.runChild(context.Background(), planned, turn.TaskProfile{ID: profile}, &contract.DelegatedTask{ID: "resumed-child", Profile: profile}, "Continue checking.", "Continue checking.", 2, false, &meter{started: time.Now()}, &childRun{}); err != nil {
						t.Fatal(err)
					}
					if _, err := runcontext.RestoredDelegatedPolicy(&saved, role, "changed", "fixture"); err == nil || !strings.Contains(err.Error(), "delegated role differs from child admission") {
						t.Fatalf("saved binding mismatch: %v", err)
					}
					broken := saved
					broken.ID = ""
					if _, err := runcontext.RestoredDelegatedPolicy(&broken, role, "fixture", "fixture"); err == nil {
						t.Fatal("invalid saved checkpoint admitted")
					}
					if observed != 2 {
						t.Fatalf("children %d, want 2", observed)
					}
					if !reflect.DeepEqual(parent.ResolvedPolicy.Record(), before) {
						t.Fatal("child changed parent policy")
					}
				})
			}
		}
	}
}

type delegatedPolicyTestLink struct{ policy *runcontext.DelegatedPolicy }

func (l delegatedPolicyTestLink) Attach(*runcontext.SessionContext)            {}
func (l delegatedPolicyTestLink) DelegatedPolicy() *runcontext.DelegatedPolicy { return l.policy }

func TestDelegatedPolicyRequiresConsistentFreshAdmission(t *testing.T) {
	parent, err := runcontext.NewSessionContext(runcontext.Session{Model: &runcontext.ModelSession{Family: "openai-compatible", Model: "fixture", Thinking: "low"}, Policy: runcontext.PolicyLayer{"models": map[string]any{"child.review": map[string]any{"model": "fixture"}}}})
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	projection, err := parent.DelegatedPolicy("child.review", "fixture", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	for _, changed := range []string{"value", "binding", "discovery"} {
		t.Run(changed, func(t *testing.T) {
			author := map[string]any{"model": "fixture"}
			model := "fixture"
			if changed == "value" {
				author["reasoning"] = "high"
			} else if changed == "discovery" {
				author["discovered"] = map[string]any{"reasoning": map[string]any{"supported": []any{"low", "high"}}}
			} else {
				model = "changed"
				author["model"] = "changed"
			}
			_, err := runcontext.NewSessionContext(runcontext.Session{Model: &runcontext.ModelSession{Family: "openai-compatible", Model: model}, Policy: runcontext.PolicyLayer{"models": map[string]any{"author": author}}, Context: &runcontext.Input{Link: delegatedPolicyTestLink{projection}}})
			message := "delegated role differs from child admission"
			if changed == "discovery" {
				message = "delegated discovery differs from child admission"
			}
			if err == nil || !strings.Contains(err.Error(), message) {
				t.Fatalf("mismatch admission: %v", err)
			}
		})
	}
}
