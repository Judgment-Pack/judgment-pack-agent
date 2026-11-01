package adapter_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	native "github.com/Judgment-Pack/judgment-pack-agent/go/adapter/agent"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/eino"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
	"github.com/Judgment-Pack/judgment-pack-agent/go/relay"
	"io"
	"net/http"
	"strings"
	"testing"
)

func runtimeActionOwner() gate.RuntimeExecutionOwner {
	pin := "sha256:" + strings.Repeat("a", 64)
	return gate.RuntimeExecutionOwner{ArtifactDigest: pin, ProcessID: "launch-one", ProjectID: "project-one", ProjectLabel: "Example project", SessionID: "server-session-one", PrincipalDigest: pin, PrincipalLabel: "Project editor", ScopeDigest: pin, ScopeLabel: "Evaluate example project"}
}
func runtimeActionTool() contract.ToolOfferRuntimeTool {
	description := "Evaluate one unsaved draft"
	experimental := true
	return contract.ToolOfferRuntimeTool{Name: "experimental_evaluate", Description: &description, InputSchema: json.RawMessage(`{"type":"object","properties":{"value":{"type":"integer"},"rehearsal":{"type":"boolean"}}}`), Meta: contract.ToolTaxonomy{Operation: "evaluate", Effect: "act", Experimental: &experimental}}
}

type runtimeActionFixture struct {
	principal    *runcontext.RunPrincipal
	gate         *gate.Gate
	owner        gate.RuntimeExecutionOwner
	listed       []contract.ToolOfferRuntimeTool
	calls        []json.RawMessage
	events       []contract.RunEvent
	policy       string
	approve      func(gate.RuntimeApprovalRequest) string
	observe      func() gate.RuntimeExecutionOwner
	presentation []contract.ToolOfferRuntimeTool
	link         *runtimeActionLink
}

func newRuntimeActionFixture(t *testing.T) *runtimeActionFixture {
	t.Helper()
	f := &runtimeActionFixture{owner: runtimeActionOwner(), listed: []contract.ToolOfferRuntimeTool{runtimeActionTool()}}
	port := &gate.RuntimeActionPort{Owner: f.owner, Approve: func(request gate.RuntimeApprovalRequest) string {
		if f.approve == nil {
			return "ask"
		}
		return f.approve(request)
	}, Current: func(context.Context) (gate.RuntimeExecutionOwner, error) {
		if f.observe != nil {
			return f.observe(), nil
		}
		return f.owner, nil
	}}
	f.gate = gate.New(gate.Options{Granted: []string{"experimental_evaluate"}, RuntimeActions: port, Emit: func(e contract.RunEvent) { f.events = append(f.events, e) }, Runtime: func(_ context.Context, frame json.RawMessage) (json.RawMessage, error) {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(frame, &request); err != nil {
			return nil, err
		}
		if len(request.ID) == 0 {
			return nil, nil
		}
		var result any
		switch request.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "bounded-runtime", "version": "fixture"}}
		case "tools/list":
			result = map[string]any{"tools": f.listed}
		case "tools/call":
			f.calls = append(f.calls, append(json.RawMessage(nil), request.Params...))
			result = map[string]any{"content": []map[string]any{{"type": "text", "text": "evaluated draft"}}, "structuredContent": map[string]any{"status": "ok", "auditAppended": false}}
		default:
			return nil, errors.New("unexpected method")
		}
		return json.Marshal(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
	}})
	if _, err := f.gate.Open(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	return f
}
func runtimeActionResponse() *http.Response {
	chunk := map[string]any{"id": "fixture", "object": "chat.completion.chunk", "model": "fixture", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "fixture-call", "type": "function", "function": map[string]any{"name": "experimental_evaluate", "arguments": `{"value":7,"rehearsal":false}`}}}}, "finish_reason": "tool_calls"}}}
	raw, _ := json.Marshal(chunk)
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: " + string(raw) + "\n\ndata: [DONE]\n\n"))}
}
func (f *runtimeActionFixture) run(t *testing.T, store runcontext.RunStore, engine string, resume bool, decision *contract.CheckpointApprovalDecision, ack bool) ([]contract.RunEvent, error) {
	t.Helper()
	events := []contract.RunEvent{}
	run := &adapter.Run{Prompt: "Evaluate the unsaved draft.", Wire: "openai-compatible", ModelName: "fixture", Thinking: "off", Critic: "off", Gate: f.gate.Calls(), Tools: f.gate.Offer().Tools, AllowConversation: true, Context: &runcontext.Input{CurrentRequest: &runcontext.CurrentRequest{Message: "Evaluate the unsaved draft.", Input: "draft"}, RunStore: &runcontext.DurableRunInput{Store: store, ID: "runtime-action-run", Resume: resume}, Approval: decision}, Emit: func(e contract.RunEvent) { events = append(events, e) }}
	if f.presentation != nil {
		run.Tools = f.presentation
	}
	if f.link != nil {
		run.Context.Link = f.link
	}
	run.Context.Principal = f.principal
	run.EnrollContext = func(ctx context.Context, offer adapter.ContextOffer) error {
		if !ack {
			return nil
		}
		return f.gate.EnrollContext(offer.ID, offer.HostTools)
	}
	var implementation adapter.Adapter
	if engine == "native" {
		implementation = native.New()
		run.Agent = &adapter.Agent{Model: "fixture", Run: func(ctx context.Context, request adapter.AgentRequest, cb adapter.AgentCallbacks) error {
			if cb.ProviderRelease != nil {
				defer func() {
					if !cb.ProviderRelease.Acknowledge(cb.ProviderRelease.Invocation) {
						t.Error("release acknowledgement refused")
					}
				}()
			}
			if resume {
				return cb.Event(ctx, adapter.AgentEvent{Type: "message", ID: "final", Text: "Evaluation completed.", Phase: "final"})
			}
			for _, tool := range request.Tools {
				if strings.HasPrefix(tool.Description, "experimental_evaluate: ") {
					_, err := cb.Tool(ctx, adapter.AgentToolCall{ID: "native-call", Name: tool.Name, Arguments: json.RawMessage(`{"value":7,"rehearsal":false}`)})
					return err
				}
			}
			return errors.New("Runtime evaluation was not offered")
		}}
	} else {
		implementation = eino.New()
		requests := 0
		run.Model = func(context.Context, string, relay.Request) (*http.Response, error) {
			requests++
			if resume {
				return enrollmentFinal(), nil
			}
			if requests > 1 {
				if f.principal != nil {
					return enrollmentFinal(), nil
				}
				return nil, errors.New("follow-up dispatched while approval held")
			}
			return runtimeActionResponse(), nil
		}
	}
	err := implementation.Run(context.Background(), run)
	return events, err
}
func runtimeActionAsked(t *testing.T, events []contract.RunEvent) *contract.RunEventApprovalRequested {
	t.Helper()
	for _, e := range events {
		if e.ApprovalRequested != nil {
			return e.ApprovalRequested
		}
	}
	raw, _ := json.Marshal(events)
	t.Fatalf("no actual approval request: %s", raw)
	return nil
}
func runtimeActionHead(t *testing.T, store runcontext.RunStore) *runcontext.Value {
	t.Helper()
	head, err := store.Load(context.Background(), "runtime-action-run")
	if err != nil || head == nil {
		t.Fatal("missing original store head", err)
	}
	value, err := runcontext.ParseJSON(head.Checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	return value
}
func TestRuntimeActionActualExportedAdapters(t *testing.T) {
	for _, engine := range []string{"eino", "native"} {
		t.Run(engine, func(t *testing.T) {
			f := newRuntimeActionFixture(t)
			store := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
			events, err := f.run(t, store, engine, false, nil, true)
			if err != nil {
				t.Fatal(err)
			}
			asked := runtimeActionAsked(t, events)
			if len(f.calls) != 0 || asked.Runtime == nil || asked.Runtime.Owner.PrincipalLabel != "Project editor" || !sameJSON(mustRuntimeJSON(asked.Args), json.RawMessage(`{"value":7,"rehearsal":true}`)) {
				t.Fatal("ask did not bind canonical call before dispatch")
			}
			for _, e := range events {
				if e.ToolCall != nil {
					t.Fatal("acting call event preceded approval")
				}
			}
			saved := runtimeActionHead(t, store)
			if saved.Get("version").Num() != 3 || saved.Get("status").Str() != "paused" || saved.Get("approval").Get("state").Str() != "asked" {
				t.Fatalf("pause not acknowledged in original store: version=%v status=%s approval=%s", saved.Get("version").Num(), saved.Get("status").Str(), saved.Get("approval").Text())
			}
			events, err = f.run(t, store, engine, true, &contract.CheckpointApprovalDecision{CallID: asked.CallID, Approved: true}, true)
			if err != nil {
				raw, _ := json.Marshal(events)
				t.Fatalf("resume: %v %s", err, raw)
			}
			if len(f.calls) != 1 || !sameJSON(f.calls[0], json.RawMessage(`{"name":"experimental_evaluate","arguments":{"value":7,"rehearsal":true}}`)) {
				t.Fatal("exact one approved canonical transport call missing", f.calls)
			}
			done := runtimeActionHead(t, store)
			if done.Get("approval").Get("state").Str() != "answered" {
				t.Fatal("result not acknowledged", done.Get("approval").Text())
			}
			count := 0
			for _, d := range done.Get("durable").Get("dispatches").Items() {
				if d.Get("kind").Str() == "runtime" {
					count++
					if d.Get("read").Truth() || d.Get("state").Str() != "result" {
						t.Fatal("Runtime action cached as a read")
					}
				}
			}
			if count != 1 {
				t.Fatal("original writer Runtime claim missing")
			}
			_, _ = f.run(t, store, engine, true, nil, true)
			if len(f.calls) != 1 {
				t.Fatal("recovered approval replayed")
			}
			report, err := runcontext.ProjectRunReport(*mustRuntimeCheckpoint(t, done))
			if err != nil || report.Get("version").Num() != 2 || report.Has("usageTotals") {
				t.Fatal("no-budget Runtime report projection invented usage", err)
			}
		})
	}
}
func mustRuntimeJSON(v any) json.RawMessage { raw, _ := json.Marshal(v); return raw }
func mustRuntimeCheckpoint(t *testing.T, v *runcontext.Value) *contract.Checkpoint {
	t.Helper()
	cp, err := runcontext.ReadExecution(v)
	if err != nil {
		t.Fatal(err)
	}
	return cp
}
func TestRuntimeActionStaleAndEnrollmentRefusals(t *testing.T) {
	for _, engine := range []string{"eino", "native"} {
		for _, change := range []string{"owner", "sibling", "enrollment", "phase"} {
			t.Run(engine+"/"+change, func(t *testing.T) {
				f := newRuntimeActionFixture(t)
				store := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
				first, err := f.run(t, store, engine, false, nil, true)
				if err != nil {
					t.Fatal(err)
				}
				asked := runtimeActionAsked(t, first)
				switch change {
				case "owner":
					f.owner.ScopeDigest = "sha256:" + strings.Repeat("b", 64)
				case "sibling":
					f.listed = append(f.listed, contract.ToolOfferRuntimeTool{Name: "get_schema", Meta: contract.ToolTaxonomy{Operation: "read", Effect: "read"}})
				case "phase":
					f.gate.Enter(gate.PlanPhase(f.gate.Offer()))
				}
				_, _ = f.run(t, store, engine, true, &contract.CheckpointApprovalDecision{CallID: asked.CallID, Approved: true}, change != "enrollment")
				if len(f.calls) != 0 {
					t.Fatal("stale action or missing final-offer acknowledgement dispatched")
				}
			})
		}
	}
}

type runtimeFaultStore struct {
	runcontext.RunStore
	mode  string
	armed bool
}

func (s *runtimeFaultStore) Commit(ctx context.Context, in runcontext.RunCommit) (runcontext.RunReceipt, error) {
	var cp contract.Checkpoint
	_ = json.Unmarshal(in.Checkpoint, &cp)
	if s.armed && s.mode == "result" && cp.Durable != nil {
		for _, d := range cp.Durable.Dispatches {
			if d.Kind == "runtime" && d.State == "result" {
				return runcontext.RunReceipt{}, errors.New("interrupted result commit")
			}
		}
	}
	receipt, err := s.RunStore.Commit(ctx, in)
	if err != nil {
		return receipt, err
	}
	if s.armed && s.mode == "asked" && cp.Approval != nil && cp.Approval.State == "asked" {
		return runcontext.RunReceipt{}, errors.New("lost pause acknowledgement")
	}
	return receipt, nil
}
func TestRuntimeActionUnacknowledgedPauseAndUnknownResult(t *testing.T) {
	for _, engine := range []string{"eino", "native"} {
		for _, mode := range []string{"asked", "result"} {
			t.Run(engine+"/"+mode, func(t *testing.T) {
				f := newRuntimeActionFixture(t)
				f.link = &runtimeActionLink{}
				store := &runtimeFaultStore{RunStore: runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{}), mode: mode, armed: mode == "asked"}
				first, _ := f.run(t, store, engine, false, nil, true)
				if mode == "asked" {
					if f.link.context.Execution.State().Approval != nil {
						t.Fatal("unacknowledged pause retained asked state")
					}
					for _, e := range first {
						if e.ApprovalRequested != nil {
							t.Fatal("unacknowledged pause exposed actionable request")
						}
					}
					if len(f.calls) != 0 {
						t.Fatal("unacknowledged pause dispatched")
					}
					return
				}
				asked := runtimeActionAsked(t, first)
				store.armed = true
				_, _ = f.run(t, store, engine, true, &contract.CheckpointApprovalDecision{CallID: asked.CallID, Approved: true}, true)
				if len(f.calls) != 1 {
					t.Fatal("fixture did not reach uncertain dispatched result")
				}
				store.armed = false
				_, _ = f.run(t, store, engine, true, nil, true)
				if len(f.calls) != 1 {
					t.Fatal("unknown action replayed")
				}
			})
		}
	}
}

func TestRuntimeActionFinalOwnerCheck(t *testing.T) {
	for _, engine := range []string{"eino", "native"} {
		t.Run(engine, func(t *testing.T) {
			f := newRuntimeActionFixture(t)
			observations := 0
			f.approve = func(gate.RuntimeApprovalRequest) string { return "allow" }
			f.observe = func() gate.RuntimeExecutionOwner {
				observations++
				current := f.owner
				if observations > 1 {
					current.ScopeDigest = "sha256:" + strings.Repeat("b", 64)
				}
				return current
			}
			_, _ = f.run(t, runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{}), engine, false, nil, true)
			if observations < 2 {
				t.Fatal("fixture did not reach final currentness check")
			}
			if len(f.calls) != 0 {
				t.Fatal("changed owner dispatched after policy preparation")
			}
		})
	}
}
func TestRuntimeActionHostPolicyIsolationAndRefusals(t *testing.T) {
	for _, engine := range []string{"eino", "native"} {
		for _, mode := range []string{"refuse", "malformed", "panic", "mutation"} {
			t.Run(engine+"/"+mode, func(t *testing.T) {
				f := newRuntimeActionFixture(t)
				f.approve = func(request gate.RuntimeApprovalRequest) string {
					switch mode {
					case "refuse":
						return "refuse"
					case "malformed":
						return ""
					case "panic":
						panic("host refusal")
					default:
						request.Args = json.RawMessage(`{"value":99,"rehearsal":true}`)
						request.Runtime.Owner.ScopeLabel = "Other scope"
						return "allow"
					}
				}
				events, _ := f.run(t, runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{}), engine, false, nil, true)
				for _, event := range events {
					if event.ApprovalRequested != nil {
						t.Fatal("configured policy refusal became an ask")
					}
				}
				if mode == "mutation" {
					if len(f.calls) != 1 || !sameJSON(f.calls[0], json.RawMessage(`{"name":"experimental_evaluate","arguments":{"value":7,"rehearsal":true}}`)) {
						t.Fatal("policy mutation retargeted exact call")
					}
				} else if len(f.calls) != 0 {
					t.Fatal("invalid/refused policy dispatched")
				}
			})
		}
	}
}

func TestRuntimeActionExactAttemptSingleUse(t *testing.T) {
	f := newRuntimeActionFixture(t)
	f.approve = func(gate.RuntimeApprovalRequest) string { return "allow" }
	store := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
	tool := runtimeActionTool()
	experimental := true
	session := runcontext.Session{Prompt: "Evaluate the unsaved draft.", Tools: []runcontext.RuntimeTool{{Name: tool.Name, Meta: &runcontext.ToolTaxonomy{Operation: "evaluate", Effect: "act", Experimental: &experimental}, Offer: &tool}}, RuntimeCalls: f.gate.Calls(), HostTools: []runcontext.HostTool{}, Model: &runcontext.ModelSession{Family: "openai-compatible", Model: "fixture", Thinking: "off"}, Critic: "off", AllowConversation: true, Context: &runcontext.Input{CurrentRequest: &runcontext.CurrentRequest{Message: "Evaluate the unsaved draft.", Input: "draft"}, RunStore: &runcontext.DurableRunInput{Store: store, ID: "runtime-action-run"}}, CallTool: func(context.Context, string, json.RawMessage) (runcontext.ToolResult, error) {
		t.Fatal("arbitrary acting callback reached")
		return runcontext.ToolResult{}, nil
	}}
	opened, err := runcontext.ContextualSession(context.Background(), session)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Context.Close()
	call, err := opened.Context.PrepareRuntimeCall(context.Background(), tool.Name, json.RawMessage(`{"value":7,"rehearsal":false}`), "one-use-call")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = call.Execute(context.Background()); err != nil {
		t.Fatal(err)
	}
	copied := *call
	if _, err = copied.Execute(context.Background()); err == nil {
		t.Fatal("completed Runtime attempt reused retained dispatch result")
	}
	if len(f.calls) != 1 {
		t.Fatal("same exact attempt dispatched more than once")
	}
	var forged runcontext.RuntimeCall
	if _, err = forged.Execute(context.Background()); err == nil {
		t.Fatal("forged empty attempt dispatched")
	}
}

type runtimeActionLink struct{ context *runcontext.SessionContext }

func (l *runtimeActionLink) Attach(c *runcontext.SessionContext) { l.context = c }
func TestRuntimeActionAdmittedDescriptor(t *testing.T) {
	for _, engine := range []string{"eino", "native"} {
		t.Run(engine, func(t *testing.T) {
			f := newRuntimeActionFixture(t)
			tool := runtimeActionTool()
			description := "A stale declaration"
			tool.Description = &description
			f.presentation = []contract.ToolOfferRuntimeTool{tool}
			events, _ := f.run(t, runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{}), engine, false, nil, true)
			for _, event := range events {
				if event.ApprovalRequested != nil {
					t.Fatal("stale descriptor became an actionable approval")
				}
			}
			if len(f.calls) != 0 {
				t.Fatal("stale descriptor dispatched")
			}
		})
	}
}
func TestRuntimeActionFinalAdmissionCheck(t *testing.T) {
	for _, engine := range []string{"eino", "native"} {
		t.Run(engine, func(t *testing.T) {
			f := newRuntimeActionFixture(t)
			f.link = &runtimeActionLink{}
			observations := 0
			f.approve = func(gate.RuntimeApprovalRequest) string { return "allow" }
			f.observe = func() gate.RuntimeExecutionOwner {
				observations++
				if observations == 2 {
					row := f.link.context.ResolvedPolicy["budget.sourceCallsPerTurn"]
					row.Value = 1
					f.link.context.ResolvedPolicy["budget.sourceCallsPerTurn"] = row
				}
				return f.owner
			}
			_, _ = f.run(t, runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{}), engine, false, nil, true)
			if observations < 2 {
				t.Fatal("fixture did not reach final admission check")
			}
			if len(f.calls) != 0 {
				t.Fatal("changed permissions dispatched after preparation")
			}
		})
	}
}
