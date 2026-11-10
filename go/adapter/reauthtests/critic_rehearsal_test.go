package reauthtests

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	native "github.com/Judgment-Pack/judgment-pack-agent/go/adapter/agent"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/delegate"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/eino"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/turn"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
	"github.com/Judgment-Pack/judgment-pack-agent/go/relay"
)

// #344: a critic, and a delegated author-test child, rehearse a saved case through a Runtime that
// serves experimental_evaluate with effect act: sent as a rehearsal, no approval asked, nothing
// recorded as an effect, and the case compared.

const rehearsalDocument = `{"specVersion":"0.2.0-draft","id":"https://example.com/expense","title":"Expense"}`
const rehearsalFacts = `{"claim":{"amount":"120.00","receiptAttached":true}}`
const rehearsalAnswer = `{"status":"evaluated","rehearsal":true,"disposition":{"handoff":{"state":"none"},"kind":"outcome","outcomeId":"approve","reasons":[]}}`
const rehearsalRewrote = "the call carried rehearsal: false; it was rewritten to an own rehearsal: true before the frame left the page, so the runtime appends no audit record (runtime ADR-0018, ADR-0028)"

var rehearsalCase = json.RawMessage(`{"source":"test","id":"receipted-120","facts":` + rehearsalFacts + `,"expectedDisposition":{"kind":"outcome","outcomeId":"approve"}}`)

func rehearsalProposal() string {
	return "Ready.\n\n```json\n{\"proposal\":{\"document\":" + rehearsalDocument + ",\"unknowns\":[]}}\n```"
}

// The critic's own call, as the model writes it: rehearsal false, which the gate rewrites.
func rehearsalCall() json.RawMessage {
	raw, _ := json.Marshal(map[string]any{"pack": rehearsalDocument, "facts": rehearsalFacts, "rehearsal": false})
	return raw
}

// rehearsalRuntime is a Runtime serving tools behind the real gate, with the approval port: every
// tools/call frame is kept, and an evaluation answers the saved case's outcome.
type rehearsalRuntime struct {
	calls  []json.RawMessage
	events []contract.RunEvent
	tools  []contract.ToolOfferRuntimeTool
}

func (r *rehearsalRuntime) transport(_ context.Context, frame json.RawMessage) (json.RawMessage, error) {
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
		result = map[string]any{"tools": r.tools}
	case "tools/call":
		r.calls = append(r.calls, append(json.RawMessage(nil), request.Params...))
		result = map[string]any{"content": []map[string]any{{"type": "text", "text": rehearsalAnswer}}, "structuredContent": json.RawMessage(rehearsalAnswer)}
	default:
		return nil, errors.New("unexpected method")
	}
	return json.Marshal(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
}

func (r *rehearsalRuntime) gate(t *testing.T, turnOf *gate.Gate, emit func(contract.RunEvent)) *gate.Gate {
	t.Helper()
	owner := runtimeActionOwner()
	port := &gate.RuntimeActionPort{Owner: owner, Approve: func(gate.RuntimeApprovalRequest) string { return "ask" },
		Current: func(context.Context) (gate.RuntimeExecutionOwner, error) { return owner, nil }}
	return gate.New(gate.Options{Granted: gate.Ceiling, RuntimeActions: port, Runtime: r.transport, Emit: emit, Turn: turnOf})
}

func sseOf(delta map[string]any, finish string) *http.Response {
	chunk := func(delta map[string]any, finish any) string {
		raw, _ := json.Marshal(map[string]any{"id": "fixture", "object": "chat.completion.chunk", "model": "fixture", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}})
		return "data: " + string(raw) + "\n\n"
	}
	body := chunk(delta, nil) + chunk(map[string]any{}, finish) + "data: [DONE]\n\n"
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}
}
func sseText(text string) *http.Response {
	return sseOf(map[string]any{"role": "assistant", "content": text}, "stop")
}
func sseTool(name string, args json.RawMessage) *http.Response {
	return sseOf(map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": 0, "id": "call-" + name, "type": "function", "function": map[string]any{"name": name, "arguments": string(args)}}}}, "tool_calls")
}

// rehearsalChecks are what every rehearsal here must show: one frame on the wire, a rehearsal; no
// approval asked or held; no Runtime effect in the original writer; no refusal.
func rehearsalChecks(t *testing.T, r *rehearsalRuntime, events []contract.RunEvent, store runcontext.RunStore, id string, wire json.RawMessage) {
	t.Helper()
	if len(r.calls) != 1 || !sameJSON(r.calls[0], wire) {
		t.Fatalf("the Runtime received %s, not the one rehearsal %s", r.calls, wire)
	}
	var sent struct {
		Arguments map[string]json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(r.calls[0], &sent); err != nil || string(sent.Arguments["rehearsal"]) != "true" {
		t.Fatalf("the frame on the wire carries no rehearsal: true: %s", r.calls[0])
	}
	for _, e := range append(append([]contract.RunEvent{}, events...), r.events...) {
		if e.ApprovalRequested != nil {
			t.Fatal("a rehearsal asked for approval")
		}
		if e.Guardrail != nil && e.Guardrail.Action == "refused" {
			t.Fatalf("a rehearsal was refused: %s", e.Guardrail.Detail)
		}
		if e.Error != nil {
			t.Fatalf("the run failed: %s", e.Error.Message)
		}
	}
	head, err := store.Load(context.Background(), id)
	if err != nil || head == nil {
		t.Fatal("missing store head", err)
	}
	saved, err := runcontext.ParseJSON(head.Checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Has("approval") && saved.Get("approval").Text() != "null" {
		t.Fatalf("a rehearsal left an approval: %s", saved.Get("approval").Text())
	}
	for _, d := range saved.Get("durable").Get("dispatches").Items() {
		if d.Get("kind").Str() == "runtime" {
			t.Fatalf("a rehearsal was recorded as a Runtime effect: %s", d.Text())
		}
	}
}

func rewroteOnce(t *testing.T, events []contract.RunEvent) {
	t.Helper()
	rewrote := 0
	for _, e := range events {
		if e.Guardrail != nil && e.Guardrail.Action == "rewrote" {
			rewrote++
			if e.Guardrail.Detail != rehearsalRewrote {
				t.Fatalf("rewrite notice %q", e.Guardrail.Detail)
			}
		}
	}
	if rewrote != 1 {
		t.Fatalf("%d rewrite notices", rewrote)
	}
}

func comparedCase(t *testing.T, events []contract.RunEvent) {
	t.Helper()
	for _, e := range events {
		if e.Critique == nil {
			continue
		}
		raw, _ := json.Marshal(e.Critique)
		var critique struct {
			Cases []struct {
				Case   string `json:"case"`
				Agrees bool   `json:"agrees"`
			} `json:"cases"`
			Incomplete []string `json:"incomplete"`
			Text       string   `json:"text"`
		}
		if err := json.Unmarshal(raw, &critique); err != nil {
			t.Fatal(err)
		}
		if len(critique.Cases) != 1 || critique.Cases[0].Case != "receipted-120" || !critique.Cases[0].Agrees || len(critique.Incomplete) != 0 || strings.Contains(critique.Text, "not compared") {
			t.Fatalf("the saved case was not compared: %s", raw)
		}
		return
	}
	t.Fatal("no critique")
}

// criticRun is one author turn that proposes at once, then the critic: rehearsing the saved case
// itself (own) or leaving it to the harness's comparison.
func criticRun(t *testing.T, engine string, own bool, principal ...*runcontext.RunPrincipal) (*rehearsalRuntime, []contract.RunEvent, runcontext.RunStore, string) {
	t.Helper()
	r := &rehearsalRuntime{tools: []contract.ToolOfferRuntimeTool{runtimeActionTool()}}
	g := r.gate(t, nil, func(e contract.RunEvent) { r.events = append(r.events, e) })
	offer, err := g.Open(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	store := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
	events := []contract.RunEvent{}
	received := ""
	run := &adapter.Run{Prompt: "Check the expense pack.", TestPrompt: "Test the pack with the runtime.", Wire: "openai-compatible", ModelName: "fixture", Thinking: "off", Critic: "on",
		Cases: []json.RawMessage{rehearsalCase}, Gate: g.Calls(), Tools: offer.Tools, AllowConversation: true,
		Context: &runcontext.Input{CurrentRequest: &runcontext.CurrentRequest{Message: "Check the expense pack.", Input: "The expense pack."}, RunStore: &runcontext.DurableRunInput{Store: store, ID: "critic-rehearsal"}},
		Emit:    func(e contract.RunEvent) { events = append(events, e) }}
	run.EnrollContext = func(_ context.Context, offer adapter.ContextOffer) error {
		return g.EnrollContext(offer.ID, offer.HostTools)
	}
	refused := len(principal) > 0 && principal[0] != nil
	if refused {
		run.Context.Principal = principal[0]
	}
	var implementation adapter.Adapter
	if engine == "native" {
		implementation = native.New()
		run.Agent = &adapter.Agent{Model: "fixture", Run: func(ctx context.Context, request adapter.AgentRequest, cb adapter.AgentCallbacks) error {
			if cb.ProviderRelease != nil {
				defer cb.ProviderRelease.Acknowledge(cb.ProviderRelease.Invocation)
			}
			if request.Phase != "critic" {
				return cb.Event(ctx, adapter.AgentEvent{Type: "message", ID: "author", Text: rehearsalProposal(), Phase: "final"})
			}
			if own {
				for _, tool := range request.Tools {
					if strings.HasPrefix(tool.Description, "experimental_evaluate: ") {
						answer, err := cb.Tool(ctx, adapter.AgentToolCall{ID: "critic-call", Name: tool.Name, Arguments: rehearsalCall()})
						if err != nil {
							return err
						}
						received = answer.Text
					}
				}
			}
			return cb.Event(ctx, adapter.AgentEvent{Type: "message", ID: "critic", Text: "Done.", Phase: "final"})
		}}
	} else {
		implementation = eino.New()
		run.Model = func(_ context.Context, _ string, request relay.Request) (*http.Response, error) {
			body := string(request.Body)
			if !strings.Contains(body, turn.RefutationMarker) {
				return sseText(rehearsalProposal()), nil
			}
			if own && !strings.Contains(body, `"role":"tool"`) {
				return sseTool("experimental_evaluate", rehearsalCall()), nil
			}
			if own {
				received = body
			}
			return sseText("Done."), nil
		}
	}
	if err := implementation.Run(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if own && !refused && !strings.Contains(received, `\"outcomeId\":\"approve\"`) && !strings.Contains(received, `"outcomeId":"approve"`) {
		t.Fatalf("the critic's model did not receive the Runtime's answer: %q", received)
	}
	return r, events, store, "critic-rehearsal"
}

func TestACriticRehearsesASavedCaseOnAnActingEvaluate(t *testing.T) {
	for _, engine := range []string{"eino", "native"} {
		t.Run(engine+"/critic's own call", func(t *testing.T) {
			r, events, store, id := criticRun(t, engine, true)
			wire, _ := json.Marshal(map[string]any{"name": "experimental_evaluate", "arguments": map[string]any{"pack": rehearsalDocument, "facts": rehearsalFacts, "rehearsal": true}})
			rehearsalChecks(t, r, events, store, id, wire)
			rewroteOnce(t, r.events)
			comparedCase(t, events)
		})
		t.Run(engine+"/the harness's comparison", func(t *testing.T) {
			r, events, store, id := criticRun(t, engine, false)
			cases, err := turn.ReadCriticCases(json.RawMessage(`[` + string(rehearsalCase) + `]`))
			if err != nil {
				t.Fatal(err)
			}
			wire, _ := json.Marshal(map[string]any{"name": "experimental_evaluate", "arguments": turn.RehearsalOf(cases[0], json.RawMessage(rehearsalDocument))})
			rehearsalChecks(t, r, events, store, id, wire)
			for _, e := range r.events {
				if e.Guardrail != nil {
					t.Fatalf("the harness's own rehearsal earned a guardrail: %s", e.Guardrail.Detail)
				}
			}
			comparedCase(t, events)
		})
	}
}

// childRun is a parent that delegates one task to profile, whose child calls experimental_evaluate
// (and, where act is set, an acting tool other than it) and reports.
func childRun(t *testing.T, engine, profile string, extra []contract.ToolOfferRuntimeTool, also string) (*rehearsalRuntime, []contract.RunEvent, runcontext.RunStore, []string) {
	t.Helper()
	r := &rehearsalRuntime{tools: append([]contract.ToolOfferRuntimeTool{runtimeActionTool()}, extra...)}
	g := r.gate(t, nil, func(e contract.RunEvent) { r.events = append(r.events, e) })
	offer, err := g.Open(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	store := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
	events := []contract.RunEvent{}
	answers := []string{}
	var child *gate.Gate
	children := func(_ context.Context, p contract.DelegatedTaskProfile, emit func(contract.RunEvent)) (*gate.Calls, []contract.ToolOfferRuntimeTool, error) {
		child = r.gate(t, g, func(e contract.RunEvent) { r.events = append(r.events, e); emit(e) })
		child.SetOffer(offer)
		phase := gate.ChildPhase(p, offer, g.Phase())
		child.Enter(phase)
		return child.Calls(), phase.Offer(offer).Tools, nil
	}
	delegation, _ := json.Marshal(map[string]any{"tasks": []any{map[string]any{"title": "Run the tests", "objective": "Rehearse the saved case and say whether it holds.", "profile": profile}}})
	run := &adapter.Run{Prompt: "Check the expense pack.", Wire: "openai-compatible", ModelName: "fixture", Thinking: "off", Critic: "off", Gate: g.Calls(), Tools: offer.Tools, AllowConversation: true, Interactive: true,
		HostTools: nil, Children: children,
		EnrollContext: func(_ context.Context, o adapter.ContextOffer) error {
			if o.Mode == gate.PhaseChild {
				return child.EnrollContext(o.ID, o.HostTools)
			}
			return g.EnrollContext(o.ID, o.HostTools)
		},
		Context: &runcontext.Input{CurrentRequest: &runcontext.CurrentRequest{Message: "Check the expense pack.", Input: "The expense pack."}, Chat: runcontext.NewChatContext("chat-1"), RunStore: &runcontext.DurableRunInput{Store: store, ID: "child-rehearsal"}},
		Emit:    func(e contract.RunEvent) { events = append(events, e) }}
	isChild := func(text string) bool {
		return strings.Contains(text, "Rehearse the saved case and say whether it holds.") && !strings.Contains(text, turn.Continuation)
	}
	var implementation adapter.Adapter
	if engine == "native" {
		implementation = native.New()
		run.Agent = &adapter.Agent{Model: "fixture", Run: func(ctx context.Context, request adapter.AgentRequest, cb adapter.AgentCallbacks) error {
			if cb.ProviderRelease != nil {
				defer cb.ProviderRelease.Acknowledge(cb.ProviderRelease.Invocation)
			}
			if request.Delegated || isChild(request.Prompt) && !strings.Contains(request.Prompt, turn.DelegationInstructions) {
				for _, name := range []string{"experimental_evaluate", also} {
					for _, tool := range request.Tools {
						if name != "" && strings.HasPrefix(tool.Description, name+": ") {
							answer, err := cb.Tool(ctx, adapter.AgentToolCall{ID: "child-" + name, Name: tool.Name, Arguments: rehearsalCall()})
							if err != nil {
								return err
							}
							answers = append(answers, answer.Text)
						}
					}
				}
				return cb.Event(ctx, adapter.AgentEvent{Type: "message", ID: "child", Text: "The case holds.", Phase: "final"})
			}
			if strings.Contains(request.Prompt, turn.Continuation) {
				return cb.Event(ctx, adapter.AgentEvent{Type: "message", ID: "parent", Text: "Done.", Phase: "final"})
			}
			for _, tool := range request.Tools {
				if strings.HasPrefix(tool.Description, "delegate_tasks:") {
					_, err := cb.Tool(ctx, adapter.AgentToolCall{ID: "delegate", Name: tool.Name, Arguments: delegation})
					return err
				}
			}
			return errors.New("no delegation tool")
		}}
	} else {
		implementation = eino.New()
		run.Model = func(_ context.Context, _ string, request relay.Request) (*http.Response, error) {
			body := string(request.Body)
			if strings.Contains(body, "Rehearse the saved case and say whether it holds.") && !strings.Contains(body, turn.DelegationInstructions) {
				done := strings.Count(body, `"role":"tool"`)
				calls := []string{"experimental_evaluate"}
				if also != "" {
					calls = append(calls, also)
				}
				if done > 0 {
					answers = append(answers, body)
				}
				if done < len(calls) {
					return sseTool(calls[done], rehearsalCall()), nil
				}
				return sseText("The case holds."), nil
			}
			if strings.Contains(body, `"role":"tool"`) {
				return sseText("Done."), nil
			}
			return sseTool(turn.DelegateTool, delegation), nil
		}
	}
	if err := delegate.With(implementation, delegate.Options{}).Run(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	return r, events, store, answers
}

func TestAnAuthorTestChildRehearsesOnAnActingEvaluate(t *testing.T) {
	for _, engine := range []string{"eino", "native"} {
		t.Run(engine, func(t *testing.T) {
			r, events, store, answers := childRun(t, engine, "author-test", nil, "")
			wire, _ := json.Marshal(map[string]any{"name": "experimental_evaluate", "arguments": map[string]any{"pack": rehearsalDocument, "facts": rehearsalFacts, "rehearsal": true}})
			rehearsalChecks(t, r, events, store, "child-rehearsal", wire)
			rewroteOnce(t, r.events)
			if len(answers) != 1 || !strings.Contains(answers[0], "outcomeId") || !strings.Contains(answers[0], "approve") {
				t.Fatalf("the child's model did not receive the Runtime's answer: %q", answers)
			}
			complete := false
			for _, e := range events {
				if e.DelegatedTask != nil && e.DelegatedTask.Task.State == "complete" && e.DelegatedTask.Task.Profile == "author-test" {
					complete = true
				}
			}
			if !complete {
				t.Fatal("the author-test child did not complete")
			}
		})
	}
}

type rehearsalPrincipalPort struct {
	mu    sync.Mutex
	reads []runcontext.PrincipalAdmissionRead
}

// rehearsalPrincipal is chains.json's root that names source runtime, at a time it is valid, answered
// by answer (principaltests' runtimePrincipal).
func rehearsalPrincipal(t *testing.T, port *rehearsalPrincipalPort, answer string) *runcontext.RunPrincipal {
	t.Helper()
	raw, err := os.ReadFile("../../../contract/vectors/principal/chains.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Keys     map[string][]string `json:"keys"`
		Software struct {
			Package string `json:"package"`
			Version string `json:"version"`
		} `json:"software"`
		Audience string            `json:"audience"`
		Items    []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return &runcontext.RunPrincipal{Chain: v.Items[5], Keys: v.Keys, Software: runcontext.PrincipalSoftware{Package: v.Software.Package, Version: v.Software.Version}, Audience: v.Audience,
		Admission: func(_ context.Context, read runcontext.PrincipalAdmissionRead) ([]byte, error) {
			port.mu.Lock()
			defer port.mu.Unlock()
			port.reads = append(port.reads, read)
			return []byte(answer), nil
		},
		Clock: func() (time.Time, error) { return time.Parse(time.RFC3339Nano, "2026-10-30T01:00:00.000Z") }}
}

// ADR-0029: a rehearsal of an acting evaluation is admitted as the act it is served as, counted,
// immediately before the call; a delegation that no longer stands refuses it and nothing is sent.
func TestACriticsRehearsalIsHeldToTheRunsDelegation(t *testing.T) {
	for _, engine := range []string{"eino", "native"} {
		t.Run(engine+"/admitted", func(t *testing.T) {
			port := &rehearsalPrincipalPort{}
			r, events, store, id := criticRun(t, engine, true, rehearsalPrincipal(t, port, `{"admission":"admit"}`))
			wire, _ := json.Marshal(map[string]any{"name": "experimental_evaluate", "arguments": map[string]any{"pack": rehearsalDocument, "facts": rehearsalFacts, "rehearsal": true}})
			rehearsalChecks(t, r, events, store, id, wire)
			counted := []bool{}
			for _, read := range port.reads {
				if read.Call.Tool == "experimental_evaluate" {
					counted = append(counted, read.Call.Count)
					if read.Call.Source != "runtime" || read.Call.Effect != "act" {
						t.Fatalf("the rehearsal was read as %+v", read.Call)
					}
				}
			}
			if len(counted) != 1 || !counted[0] {
				t.Fatalf("the rehearsal's admissions %v, not one counted read", counted)
			}
		})
		t.Run(engine+"/revoked", func(t *testing.T) {
			port := &rehearsalPrincipalPort{}
			r, events, _, _ := criticRun(t, engine, true, rehearsalPrincipal(t, port, `{"admission":"refuse","refusal":"revoked"}`))
			if len(r.calls) != 0 {
				t.Fatalf("%d frames reached the Runtime", len(r.calls))
			}
			refusals := 0
			for _, e := range events {
				if g := e.Guardrail; g != nil && g.Tool == "experimental_evaluate" && g.Reason != nil && *g.Reason == "delegation refused" && g.Refusal != nil && g.Refusal.Name == "revoked" {
					refusals++
					if g.Detail != "experimental_evaluate was not run: the delegation this run acts under does not admit it (revoked). Nothing was changed. Do not retry it; say what could not be done." {
						t.Fatalf("refusal detail %q", g.Detail)
					}
				}
				if e.ApprovalRequested != nil {
					t.Fatal("a refused rehearsal asked for approval")
				}
			}
			// The critic's own call, then the harness's comparison of the case it left: both refused.
			if refusals != 2 {
				t.Fatalf("%d delegation refusals", refusals)
			}
		})
	}
}
