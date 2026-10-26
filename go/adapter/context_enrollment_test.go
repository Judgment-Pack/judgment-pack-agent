package adapter_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	native "github.com/Judgment-Pack/judgment-pack-agent/go/adapter/agent"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/eino"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
	"github.com/Judgment-Pack/judgment-pack-agent/go/relay"
)

func sameJSON(a, b json.RawMessage) bool {
	var x, y any
	return json.Unmarshal(a, &x) == nil && json.Unmarshal(b, &y) == nil && reflect.DeepEqual(x, y)
}
func enrollmentFinal() *http.Response {
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: {\"id\":\"fixture\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Done.\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"fixture\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))}
}
func TestActualAdaptersRequireExactFinalContextEnrollment(t *testing.T) {
	for _, engine := range []string{"eino", "native"} {
		for _, mode := range []string{"ack", "missing", "error", "mutated", "duplicate", "conditional", "plan"} {
			t.Run(engine+"/"+mode, func(t *testing.T) {
				g := gate.New(gate.Options{})
				g.SetOffer(contract.ToolOffer{})
				calls, enrolled, executed := 0, 0, 0
				var expected []contract.ToolOfferHostTool
				var failures []contract.RunEvent
				host := runcontext.HostTool{Name: "inspect", Description: "Exact host inspect", Taxonomy: &runcontext.ToolTaxonomy{Operation: "read", Effect: "read"}, Effect: "read", InputSchema: json.RawMessage(`{"type":"object","properties":{"p":{"type":"string"}}}`), Execute: func(context.Context, json.RawMessage) (runcontext.ToolResult, error) {
					executed++
					return runcontext.ResultOf(nil, nil), nil
				}}
				run := &adapter.Run{Prompt: "Read current work.", Wire: "openai-compatible", ModelName: "fixture", Thinking: "off", Critic: "off", AllowConversation: true, Interactive: true, Gate: g.Calls(), HostTools: []runcontext.HostTool{host}, Policy: runcontext.PolicyLayer{"budget": map[string]any{"readBytes": 1024}}, Context: &runcontext.Input{CurrentRequest: &runcontext.CurrentRequest{Message: "Read current work.", Input: "facts"}}, Emit: func(e contract.RunEvent) { failures = append(failures, e) }}
				if mode == "plan" {
					run.Context.Phase = "plan"
					g.Enter(gate.PlanPhase(contract.ToolOffer{}))
				}
				if mode == "conditional" {
					run.Critic = "auto"
					run.Policy["budget"] = map[string]any{"readBytes": 1024, "offeredSchemas": 0}
					skill := runcontext.Skill{ID: "notes", Name: "notes", Description: "Read notes", Instructions: "Read carefully.", Source: runcontext.SkillSource{Kind: "custom", ID: "library", Trust: "person", Version: "1"}}
					skill.PinnedDigest = runcontext.SkillDigest(skill)
					run.Skills = []runcontext.Skill{skill}
					digest := runcontext.ToolListDigest([]runcontext.ListedTool{{Name: host.Name, Description: host.Description, InputSchema: host.InputSchema, Taxonomy: host.Taxonomy}})
					host.Effect = ""
					host.EffectClass = "read"
					host.Source = &runcontext.ToolSource{Kind: "custom", ID: "library-tools", Trust: "person", Version: "1", ToolListDigest: digest, CurrentDigest: func(context.Context) (string, error) { return digest, nil }}
					run.HostTools = []runcontext.HostTool{host}
				}
				run.EnrollContext = func(ctx context.Context, offer adapter.ContextOffer) error {
					enrolled++
					expected = offer.HostTools
					names := map[string]bool{}
					for _, tool := range offer.HostTools {
						names[tool.Name] = true
						if tool.Description == "" || len(tool.InputSchema) == 0 || tool.Taxonomy.Operation == "" {
							t.Fatal("incomplete declaration")
						}
						if tool.Name == "read_context" && !strings.Contains(string(tool.InputSchema), `"maximum":1024`) {
							t.Fatal("resolved read bound absent")
						}
					}
					for _, name := range []string{"list_context", "read_context", "read_window", "search", "update_plan", "complete_plan_step", "request_budget_increase", "remember"} {
						if !names[name] {
							t.Fatalf("actual final offer missing %s", name)
						}
					}
					for _, name := range []string{"propose_edit", "propose_delete", "propose_insert", "undo_edit"} {
						if names[name] != (mode != "plan") {
							t.Fatalf("draft family phase mismatch %s", name)
						}
					}
					if mode == "conditional" {
						for _, name := range []string{"load_tool", "load_skill", "unload_skill", "request_review"} {
							if !names[name] {
								t.Fatalf("conditional family missing %s", name)
							}
						}
					} else if names["load_tool"] || names["load_skill"] || names["request_review"] {
						t.Fatal("unconfigured conditional tool enrolled")
					}

					switch mode {
					case "missing":
						return nil
					case "error":
						return errors.New("fixture refusal")
					case "mutated":
						offer.HostTools[0].Description = "changed"
					}
					if err := g.EnrollContext(offer.ID, offer.HostTools); err != nil {
						return err
					}
					if mode == "duplicate" {
						return g.EnrollContext(offer.ID, offer.HostTools)
					}
					return nil
				}
				inspect := func(tools []adapter.AgentTool) {
					calls++
					if enrolled != 1 || len(expected) == 0 {
						t.Fatal("provider ran before final acknowledgement")
					}
					actual := g.Offer().HostTools
					if len(actual) != len(expected) {
						t.Fatal("gate lost final offer")
					}
					for _, decl := range expected {
						found := false
						for _, tool := range tools {
							if (tool.Description == decl.Description || tool.Description == decl.Name+": "+decl.Description) && sameJSON(tool.InputSchema, decl.InputSchema) {
								found = true
							}
						}
						if !found {
							t.Fatalf("provider/gate declaration differs for %s", decl.Name)
						}
						if g.Calls().AdmitHost(decl.Name) != nil {
							t.Fatalf("acknowledged tool refused %s", decl.Name)
						}
					}
					for _, name := range []string{"READ_WINDOW", "read_window_extra", "missing"} {
						if g.Calls().AdmitHost(name) == nil {
							t.Fatal("unenrolled spelling admitted")
						}
					}
				}
				var implementation adapter.Adapter
				if engine == "native" {
					implementation = native.New()
					run.Agent = &adapter.Agent{Model: "fixture", Run: func(ctx context.Context, request adapter.AgentRequest, cb adapter.AgentCallbacks) error {
						inspect(request.Tools)
						for _, tool := range request.Tools {
							if tool.Description == host.Name+": "+host.Description && mode == "ack" {
								answer, err := cb.Tool(ctx, adapter.AgentToolCall{ID: "inspect", Name: tool.Name, Arguments: json.RawMessage(`{}`)})
								if err != nil || answer.IsError {
									t.Fatalf("retained host closure failed: %v", err)
								}
							}
						}
						return cb.Event(ctx, adapter.AgentEvent{Type: "message", Text: "Done.", Phase: "final"})
					}}
				} else {
					implementation = eino.New()
					run.Model = func(_ context.Context, _ string, request relay.Request) (*http.Response, error) {
						var body struct {
							Tools []struct {
								Function struct {
									Name, Description string
									Parameters        json.RawMessage
								} `json:"function"`
							} `json:"tools"`
						}
						if err := json.Unmarshal(request.Body, &body); err != nil {
							t.Fatal(err)
						}
						tools := []adapter.AgentTool{}
						for _, row := range body.Tools {
							tools = append(tools, adapter.AgentTool{Name: row.Function.Name, Description: row.Function.Description, InputSchema: row.Function.Parameters})
						}
						inspect(tools)
						if mode == "ack" && calls == 1 {
							chunk := map[string]any{"id": "fixture", "object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "inspect", "type": "function", "function": map[string]any{"name": "inspect", "arguments": "{}"}}}}, "finish_reason": "tool_calls"}}}
							raw, _ := json.Marshal(chunk)
							return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: " + string(raw) + "\n\ndata: [DONE]\n\n"))}, nil
						}
						return enrollmentFinal(), nil
					}
				}
				if err := implementation.Run(context.Background(), run); err != nil {
					t.Fatal(err)
				}
				if mode == "ack" || mode == "conditional" || mode == "plan" {
					wantCalls := 1
					if engine == "eino" && mode == "ack" {
						wantCalls = 2
					}
					if calls != wantCalls {
						raw, _ := json.Marshal(failures)
						t.Fatalf("provider calls %d; events %s", calls, raw)
					}
					if mode == "ack" && executed != 1 {
						t.Fatal("execution closure replaced")
					}
				} else if calls != 0 || executed != 0 {
					t.Fatal("refused enrollment dispatched")
				}
			})
		}
	}
}

type preparationAdapterChat struct {
	chat  *runcontext.ChatContext
	turns int
}

func (c *preparationAdapterChat) OpenTurn() (*runcontext.ChatTurn, error) {
	c.turns++
	return c.chat.OpenTurn()
}

type preparationAdapterLink struct{ context *runcontext.SessionContext }

func (l *preparationAdapterLink) Attach(c *runcontext.SessionContext) { l.context = c }
func TestActualAdaptersConsumePreparedPlanBeforePhysicalBootstrap(t *testing.T) {
	for _, engine := range []string{"eino", "native"} {
		t.Run(engine, func(t *testing.T) {
			ctx := context.Background()
			store := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
			chat := &preparationAdapterChat{chat: runcontext.NewChatContext("adapter-plan")}
			link := &preparationAdapterLink{}
			input := &runcontext.Input{RunStore: &runcontext.DurableRunInput{Store: store, ID: "adapter-plan"}, Chat: chat, Link: link, CurrentRequest: &runcontext.CurrentRequest{Message: "Work", Input: "facts"}, Plan: runcontext.MustParseJSON(`{"id":"original","revision":1,"objective":"Work","steps":["Inspect","Report"],"questions":[],"references":[],"status":"accepted","createdAt":"2026-01-01T00:00:00.000Z","acceptedAt":"2026-01-01T00:00:00.000Z"}`)}
			g := gate.New(gate.Options{})
			g.SetOffer(contract.ToolOffer{})
			run := &adapter.Run{Prompt: "Work", Wire: "openai-compatible", ModelName: "fixture", Thinking: "off", Critic: "off", AllowConversation: true, Interactive: true, Gate: g.Calls(), Context: input, EnrollContext: func(_ context.Context, offer adapter.ContextOffer) error {
				return g.EnrollContext(offer.ID, offer.HostTools)
			}, Emit: func(contract.RunEvent) {}}
			args := json.RawMessage(`{"explanation":"Remove accepted work","plan":[{"id":"step-2","step":"Report","status":"pending"}]}`)
			var implementation adapter.Adapter
			if engine == "native" {
				implementation = native.New()
				run.Agent = &adapter.Agent{Model: "fixture", Run: func(ctx context.Context, request adapter.AgentRequest, cb adapter.AgentCallbacks) error {
					for _, tool := range request.Tools {
						if strings.HasPrefix(tool.Description, "update_plan: ") {
							_, err := cb.Tool(ctx, adapter.AgentToolCall{ID: "revise", Name: tool.Name, Arguments: args})
							return err
						}
					}
					t.Fatal("no Plan control")
					return nil
				}}
			} else {
				implementation = eino.New()
				run.Model = func(context.Context, string, relay.Request) (*http.Response, error) {
					chunk := map[string]any{"id": "fixture", "object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "revise", "type": "function", "function": map[string]any{"name": "update_plan", "arguments": string(args)}}}}, "finish_reason": "tool_calls"}}}
					raw, _ := json.Marshal(chunk)
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: " + string(raw) + "\n\ndata: [DONE]\n\n"))}, nil
				}
			}
			if err := implementation.Run(ctx, run); err != nil {
				t.Fatal(err)
			}
			if link.context == nil {
				t.Fatal("actual first adapter did not initialize")
			}
			held := *input
			held.Persistence = link.context.Persistence("author")
			held.Execution = held.Persistence.Writer.Checkpoint()
			raw, _ := json.Marshal(held.Execution)
			v := runcontext.MustParseJSON(string(raw))
			p := v.Get("planState").Get("pending")
			if p == nil {
				t.Fatal("actual first adapter did not pause for Plan drift")
			}
			held.Approval = &contract.CheckpointApprovalDecision{CallID: p.Get("callId").Str(), Approved: true}
			physical := 0
			bootstrap := func() error {
				head, _ := store.Load(ctx, "adapter-plan")
				var saved contract.Checkpoint
				_ = json.Unmarshal(head.Checkpoint, &saved)
				if saved.Status == "paused" {
					return errors.New("physical guard closed")
				}
				physical++
				return nil
			}
			if err := bootstrap(); err == nil || physical != 0 {
				t.Fatal("paused state opened physical Runtime bootstrap")
			}
			turns := chat.turns
			prepared, events, err := runcontext.PreparePlanDecision(ctx, &held, runcontext.PlanDecisionExpectation{Revision: p.Get("revision").Str(), CandidateRevision: p.Get("candidate").Get("planState").Get("revision").Str()})
			if err != nil {
				t.Fatal(err)
			}
			if len(events) != 1 || chat.turns != turns || prepared.Approval != nil {
				t.Fatal("preparation opened a second context or left decision unconsumed")
			}
			if err = bootstrap(); err != nil || physical != 1 {
				t.Fatal("acknowledged Plan did not open physical bootstrap")
			}
			g = gate.New(gate.Options{})
			g.SetOffer(contract.ToolOffer{})
			calls, enrolls, decisions := 0, 0, 0
			var observed []contract.RunEvent
			run.Context = prepared
			run.Gate = g.Calls()
			run.EnrollContext = func(_ context.Context, offer adapter.ContextOffer) error {
				enrolls++
				return g.EnrollContext(offer.ID, offer.HostTools)
			}
			run.Emit = func(e contract.RunEvent) {
				observed = append(observed, e)
				if e.PlanDriftDecided != nil {
					decisions++
				}
			}
			if engine == "native" {
				run.Agent.Run = func(ctx context.Context, _ adapter.AgentRequest, cb adapter.AgentCallbacks) error {
					calls++
					return cb.Event(ctx, adapter.AgentEvent{Type: "message", Text: "Done.", Phase: "final"})
				}
			} else {
				run.Model = func(context.Context, string, relay.Request) (*http.Response, error) {
					calls++
					return enrollmentFinal(), nil
				}
			}
			if err = implementation.Run(ctx, run); err != nil {
				t.Fatal(err)
			}
			if calls != 1 || enrolls != 1 || decisions != 0 || chat.turns != turns+1 {
				raw, _ := json.Marshal(observed)
				t.Fatalf("preparation repeated: providers=%d enrollments=%d decisions=%d turns=%d events=%s", calls, enrolls, decisions, chat.turns-turns, raw)
			}
			page, pageErr := store.ReadEvents(ctx, "adapter-plan", "", runcontext.StoragePageLimit)
			if pageErr != nil {
				t.Fatal(pageErr)
			}
			count := 0
			for _, record := range page.Items {
				var envelope runcontext.StoredRunEnvelope
				_ = json.Unmarshal(record.Bytes, &envelope)
				if envelope.Event.PlanDriftDecided != nil {
					count++
				}
			}
			if count != 1 {
				t.Fatalf("durable Plan decision count=%d", count)
			}
		})
	}
}

func TestActualAdaptersEnrollBeforeApprovedActionContinuation(t *testing.T) {
	for _, engine := range []string{"eino", "native"} {
		for _, mode := range []string{"ack", "missing", "error"} {
			t.Run(engine+"/"+mode, func(t *testing.T) {
				ctx := context.Background()
				store := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
				link := &preparationAdapterLink{}
				executed, provider, enrolled := 0, 0, 0
				action := runcontext.HostTool{Name: "change", Description: "Apply fixture action", Taxonomy: &runcontext.ToolTaxonomy{Operation: "act", Effect: "act"}, EffectClass: "act", Approval: "required", InputSchema: json.RawMessage(`{"type":"object"}`), Execute: func(context.Context, json.RawMessage) (runcontext.ToolResult, error) {
					if enrolled != 1 {
						t.Fatal("approved action executed before exact enrollment")
					}
					executed++
					return runcontext.ResultOf([]runcontext.Content{runcontext.TextContent("changed")}, nil), nil
				}}
				digest := runcontext.ToolListDigest([]runcontext.ListedTool{{Name: action.Name, Description: action.Description, InputSchema: action.InputSchema, Taxonomy: action.Taxonomy}})
				action.Source = &runcontext.ToolSource{Kind: "custom", ID: "actions", Trust: "person", Version: "1", ToolListDigest: digest, CurrentDigest: func(context.Context) (string, error) { return digest, nil }}
				g := gate.New(gate.Options{})
				g.SetOffer(contract.ToolOffer{})
				var wireAction string
				input := &runcontext.Input{RunStore: &runcontext.DurableRunInput{Store: store, ID: "approved-action"}, Link: link, CurrentRequest: &runcontext.CurrentRequest{Message: "Apply the action", Input: "facts"}}
				run := &adapter.Run{Prompt: "Apply the action", Wire: "openai-compatible", ModelName: "fixture", Thinking: "off", Critic: "off", AllowConversation: true, Interactive: true, Gate: g.Calls(), Context: input, HostTools: []runcontext.HostTool{action}, Emit: func(contract.RunEvent) {}, EnrollContext: func(_ context.Context, offer adapter.ContextOffer) error {
					for _, tool := range offer.HostTools {
						if tool.Taxonomy.Operation == "act" {
							wireAction = tool.Name
						}
					}
					return g.EnrollContext(offer.ID, offer.HostTools)
				}}
				var implementation adapter.Adapter
				if engine == "native" {
					implementation = native.New()
					run.Agent = &adapter.Agent{Model: "fixture", Run: func(ctx context.Context, request adapter.AgentRequest, cb adapter.AgentCallbacks) error {
						for _, tool := range request.Tools {
							if strings.HasPrefix(tool.Description, wireAction+": ") {
								_, err := cb.Tool(ctx, adapter.AgentToolCall{ID: "apply", Name: tool.Name, Arguments: json.RawMessage(`{}`)})
								return err
							}
						}
						t.Fatal("action absent from invocation")
						return nil
					}}
				} else {
					implementation = eino.New()
					run.Model = func(context.Context, string, relay.Request) (*http.Response, error) {
						chunk := map[string]any{"id": "fixture", "object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "apply", "type": "function", "function": map[string]any{"name": wireAction, "arguments": "{}"}}}}, "finish_reason": "tool_calls"}}}
						raw, _ := json.Marshal(chunk)
						return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: " + string(raw) + "\n\ndata: [DONE]\n\n"))}, nil
					}
				}
				if err := implementation.Run(ctx, run); err != nil {
					t.Fatal(err)
				}
				if link.context == nil {
					t.Fatal("initial adapter failed")
				}
				held := *input
				held.Persistence = link.context.Persistence("author")
				held.Checkpoint = func(ctx context.Context, state contract.Checkpoint) error {
					return held.Persistence.Writer.Save(ctx, *held.Persistence, state)
				}
				held.Execution = held.Persistence.Writer.Checkpoint()
				if executed != 0 || held.Execution.Approval == nil || held.Execution.Approval.State != "asked" {
					t.Fatal("actual adapter did not persist pending tool approval")
				}
				held.Approval = &contract.CheckpointApprovalDecision{CallID: held.Execution.Approval.CallID, Approved: true}
				run.Context = &held
				g = gate.New(gate.Options{})
				g.SetOffer(contract.ToolOffer{})
				run.Gate = g.Calls()
				run.EnrollContext = func(_ context.Context, offer adapter.ContextOffer) error {
					enrolled++
					if mode == "missing" {
						return nil
					}
					if mode == "error" {
						return errors.New("fixture enrollment refused")
					}
					return g.EnrollContext(offer.ID, offer.HostTools)
				}
				if engine == "native" {
					run.Agent.Run = func(ctx context.Context, _ adapter.AgentRequest, cb adapter.AgentCallbacks) error {
						provider++
						if executed != 1 {
							t.Fatal("provider preceded approved action continuation")
						}
						return cb.Event(ctx, adapter.AgentEvent{Type: "message", Text: "Done.", Phase: "final"})
					}
				} else {
					run.Model = func(context.Context, string, relay.Request) (*http.Response, error) {
						provider++
						if executed != 1 {
							t.Fatal("provider preceded approved action continuation")
						}
						return enrollmentFinal(), nil
					}
				}
				if err := implementation.Run(ctx, run); err != nil {
					t.Fatal(err)
				}
				if mode == "ack" {
					if executed != 1 || provider != 1 || enrolled != 1 {
						t.Fatalf("approved continuation counts action=%d provider=%d enrollment=%d", executed, provider, enrolled)
					}
				} else if executed != 0 || provider != 0 || enrolled != 1 {
					t.Fatal("failed final enrollment dispatched approved action or provider")
				}
			})
		}
	}
}
