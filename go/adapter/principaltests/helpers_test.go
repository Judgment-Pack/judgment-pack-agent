package principaltests

// Copied from go/adapter/reauthtests, whose tests use the same ones: a test binary cannot import another's
// test files, and these two binaries exist only to hold each under its time limit (#254).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	native "github.com/Judgment-Pack/judgment-pack-agent/go/adapter/agent"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/eino"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
	"github.com/Judgment-Pack/judgment-pack-agent/go/relay"
)

func enrollmentFinal() *http.Response {
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: {\"id\":\"fixture\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Done.\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"fixture\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))}
}

const hostKey = "wk.reauth.host.Q7z2Xm9LpV4cR8tNb3Yh6Df1G"

const hostHour = int64(3_600_000)

const hostArgs = `{"title":"Mileage"}`

var hostEngines = []string{"eino", "native"}

func hostPrincipal() string { return "sha256:" + strings.Repeat("7", 64) }

func hostRefusal() *runcontext.CredentialRefusal {
	return &runcontext.CredentialRefusal{Cause: "expired", Connection: "tracker-conn", Principal: hostPrincipal(), Permissions: []string{"issues:write"}, Requires: []string{"issues:write"}}
}

func refusalReason(err error) string {
	var a *runcontext.ReauthorizationRefused
	var b *runcontext.WaitRefused
	var c *runcontext.ResumeRefused
	var d *runcontext.LeaseRefused
	switch {
	case errors.As(err, &a):
		return "reauth:" + a.Reason
	case errors.As(err, &b):
		return "wait:" + b.Reason
	case errors.As(err, &c):
		return "resume:" + c.Reason
	case errors.As(err, &d):
		return "lease:" + d.Reason
	}
	return fmt.Sprint(err)
}

func hostTool(exec func(context.Context, json.RawMessage) (runcontext.ToolResult, error), approve string) runcontext.HostTool {
	tool := runcontext.HostTool{Name: "create_issue", Description: "Create one issue.", Taxonomy: &runcontext.ToolTaxonomy{Operation: "act", Effect: "act"}, EffectClass: "act", Approval: "required", InputSchema: json.RawMessage(`{"type":"object"}`), Execute: exec, Approve: func(runcontext.ApprovalRequest) string { return approve }}
	digest := runcontext.ToolListDigest([]runcontext.ListedTool{{Name: tool.Name, Description: tool.Description, InputSchema: tool.InputSchema, Taxonomy: tool.Taxonomy}})
	tool.Source = &runcontext.ToolSource{Kind: "custom", ID: "tracker", Trust: "person", Version: "1", ToolListDigest: digest, CurrentDigest: func(context.Context) (string, error) { return digest, nil }}
	return tool
}

func hostModelCall(wire, id, args string) *http.Response {
	raw, _ := json.Marshal(map[string]any{"id": "fixture", "object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": id, "type": "function", "function": map[string]any{"name": wire, "arguments": args}}}}, "finish_reason": "tool_calls"}}})
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: " + string(raw) + "\n\ndata: [DONE]\n\n"))}
}

type hostRun struct {
	events []contract.RunEvent
	err    error
	bodies []string
	asks   atomic.Int32
}

// hostDelegation is the delegation runHostTool's runs act under, where a test sets one (ADR-0029).
var hostDelegation *runcontext.RunPrincipal

// hostPolicy is the policy layer runHostTool's runs read, where a test sets one.
var hostPolicy runcontext.PolicyLayer

// runHostTool runs the tool: the model or the agent makes each of calls, then ends.
func runHostTool(t *testing.T, store runcontext.RunStore, id, engine string, resume bool, decision *contract.CheckpointApprovalDecision, tool runcontext.HostTool, calls []string) *hostRun {
	t.Helper()
	out := &hostRun{}
	var mu sync.Mutex
	g := gate.New(gate.Options{})
	g.SetOffer(contract.ToolOffer{})
	wire := ""
	run := &adapter.Run{Prompt: "File the issue.", Wire: "openai-compatible", ModelName: "fixture", Thinking: "off", Critic: "off", AllowConversation: true, Gate: g.Calls(), HostTools: []runcontext.HostTool{tool}, Context: &runcontext.Input{CurrentRequest: &runcontext.CurrentRequest{Message: "File the issue.", Input: "issue"}, RunStore: &runcontext.DurableRunInput{Store: store, ID: id, Resume: resume}, Approval: decision}, Emit: func(e contract.RunEvent) { mu.Lock(); out.events = append(out.events, e); mu.Unlock() }, EnrollContext: func(_ context.Context, offer adapter.ContextOffer) error {
		for _, held := range offer.HostTools {
			if held.Taxonomy.Operation == "act" {
				wire = held.Name
			}
		}
		return g.EnrollContext(offer.ID, offer.HostTools)
	}}
	run.Context.Principal = hostDelegation
	run.Policy = hostPolicy
	var implementation adapter.Adapter
	if engine == "native" {
		implementation = native.New()
		run.Agent = &adapter.Agent{Model: "fixture", Run: func(ctx context.Context, request adapter.AgentRequest, cb adapter.AgentCallbacks) error {
			if cb.ProviderRelease != nil {
				defer cb.ProviderRelease.Acknowledge(cb.ProviderRelease.Invocation)
			}
			out.asks.Add(1)
			name := ""
			for _, held := range request.Tools {
				if strings.HasPrefix(held.Description, wire+": ") {
					name = held.Name
				}
			}
			for i, args := range calls {
				answer, err := cb.Tool(ctx, adapter.AgentToolCall{ID: fmt.Sprintf("native-call-%d", i), Name: name, Arguments: json.RawMessage(args)})
				mu.Lock()
				raw, _ := json.Marshal(answer)
				out.bodies = append(out.bodies, string(raw))
				mu.Unlock()
				if err != nil {
					return err
				}
			}
			return cb.Event(ctx, adapter.AgentEvent{Type: "message", ID: "final", Text: "Done.", Phase: "final"})
		}}
	} else {
		implementation = eino.New()
		seen := 0
		run.Model = func(_ context.Context, _ string, request relay.Request) (*http.Response, error) {
			mu.Lock()
			out.bodies = append(out.bodies, string(request.Body))
			mu.Unlock()
			out.asks.Add(1)
			seen++
			if seen <= len(calls) {
				return hostModelCall(wire, fmt.Sprintf("model-call-%d-%d", time.Now().UnixNano(), seen), calls[seen-1]), nil
			}
			return enrollmentFinal(), nil
		}
	}
	out.err = implementation.Run(context.Background(), run)
	return out
}

type hostRow struct {
	ID      string         `json:"id"`
	Kind    string         `json:"kind"`
	State   string         `json:"state"`
	Request string         `json:"request"`
	Failure map[string]any `json:"failure"`
}

func hostRows(t *testing.T, store runcontext.RunStore, id string) []hostRow {
	t.Helper()
	head, err := store.Load(context.Background(), id)
	if err != nil || head == nil {
		t.Fatal("no head", err)
	}
	var checkpoint struct {
		Durable struct {
			Dispatches []hostRow `json:"dispatches"`
		} `json:"durable"`
	}
	if err := json.Unmarshal(head.Checkpoint, &checkpoint); err != nil {
		t.Fatal(err)
	}
	rows := []hostRow{}
	for _, row := range checkpoint.Durable.Dispatches {
		if row.Kind == "host" {
			rows = append(rows, row)
		}
	}
	return rows
}

func unknownFailure(row hostRow) bool {
	_, detail := row.Failure["detail"]
	return row.State == "unknown" && row.Failure["code"] == "authentication" && row.Failure["source"] == "tool" && row.Failure["outcome"] == "unknown" && !detail
}

// hostStore is the store a setup runs over; the crash test wraps the durable memory kind.
type hostStore interface {
	runcontext.WaitRunStore
}

type hostSetup struct {
	ctx      context.Context
	clock    *waitClock
	store    hostStore
	refuse   *atomic.Bool
	host     *runcontext.ReferenceEffectHost
	executed *atomic.Int32
	tool     runcontext.HostTool
	fence    runcontext.LeaseFence
	view     runcontext.RunStore
	first    *hostRun
}

func hostExecute(n *atomic.Int32, fails func(call int32) error) func(context.Context, json.RawMessage) (runcontext.ToolResult, error) {
	return func(context.Context, json.RawMessage) (runcontext.ToolResult, error) {
		call := n.Add(1)
		if fails != nil {
			if err := fails(call); err != nil {
				return runcontext.ToolResult{}, err
			}
		}
		return runcontext.ResultOf([]runcontext.Content{runcontext.TextContent("created")}, nil), nil
	}
}

// hostRefused is a run whose act call the reference effect host refused for its credentials.
func hostRefused(t *testing.T, engine string, wrap func(waitStore) hostStore, fails func(call int32) error) *hostSetup {
	t.Helper()
	return hostRefusedWith(t, engine, hostArgs, wrap, fails)
}

// hostRefusedWith is hostRefused for a call with these arguments, as written.
func hostRefusedWith(t *testing.T, engine, args string, wrap func(waitStore) hostStore, fails func(call int32) error) *hostSetup {
	t.Helper()
	s := &hostSetup{ctx: context.Background(), clock: &waitClock{}, refuse: &atomic.Bool{}, executed: &atomic.Int32{}}
	s.refuse.Store(true)
	s.clock.at.Store(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli())
	base := waitStore{runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{Now: s.clock.now})}
	s.store = base
	if wrap != nil {
		s.store = wrap(base)
	}
	s.host = runcontext.NewReferenceEffectHostWith(s.store, runcontext.ReferenceEffectHostOptions{Credentials: func(context.Context, runcontext.EffectOperation) (*runcontext.CredentialRefusal, error) {
		if s.refuse.Load() {
			return hostRefusal(), nil
		}
		return nil, nil
	}})
	a, err := s.store.AcquireLease(s.ctx, runcontext.LeaseAcquire{Resource: "run-r", Owner: "worker-a", DurationMs: hostHour})
	if err != nil {
		t.Fatal(err)
	}
	s.fence = runcontext.FenceOf(a)
	if s.view, err = runcontext.UnattendedRunStore(s.store, s.fence, s.host); err != nil {
		t.Fatal(err)
	}
	s.tool = hostTool(hostExecute(s.executed, fails), "allow")
	s.first = runHostTool(t, s.view, "run-r", engine, false, nil, s.tool, []string{args})
	return s
}

func (s *hostSetup) park(proof *runcontext.ReauthorizationRequired, wait, key string, fence runcontext.LeaseFence) (runcontext.RunWait, error) {
	digest, _ := runcontext.WakeKeyDigest(key)
	held, _, err := runcontext.ParkRun(s.ctx, runcontext.ParkInput{Store: s.store, Fence: fence, Wait: wait, Continuation: "continue-r", Condition: runcontext.WaitCondition{Event: &contract.RunWaitEventCondition{Kind: "reauthorized", Subject: "tracker-conn"}}, Model: runcontext.PinnedModel{Provider: "openai", Model: "fixture"}, WakeKeyDigest: digest, Release: []runcontext.WaitRelease{}, Reauthorization: proof})
	return held, err
}

func hostEvidence(wait string, held runcontext.RunWait) runcontext.ResumeEvidence {
	return runcontext.ResumeEvidence{Version: 1, Resource: "run-r", Wait: wait, Model: contract.RunResumeCurrentModel{Provider: "openai", Model: "fixture", Status: "available"}, Policy: contract.RunResumeCurrentPolicy{Digest: held.Pins.Policy}, Plan: held.Pins.Plan, Sources: held.Pins.Sources, Artifacts: held.Pins.Artifacts, Principal: held.Pins.Principal}
}

func (s *hostSetup) wakeInput(t *testing.T, wait, key string, held runcontext.RunWait, store runcontext.RunStore) runcontext.ResumeInput {
	t.Helper()
	s.clock.at.Add(60_000)
	if _, err := runcontext.WakeRun(s.ctx, s.store, runcontext.WakeEvidence{Version: 1, Resource: "run-r", Wait: wait, Evidence: "refreshed-1", Kind: "event", Event: &contract.RunWaitEventCondition{Kind: "reauthorized", Subject: "tracker-conn"}, Key: key}); err != nil {
		t.Fatal(err)
	}
	evidence := hostEvidence(wait, held)
	at := time.UnixMilli(s.clock.at.Load()).UTC().Format("2006-01-02T15:04:05.000Z")
	action := runcontext.ReauthAction{Version: 1, Resource: "run-r", Wait: wait, Connection: "tracker-conn", Action: "refresh", Principal: hostPrincipal(), Permissions: []string{"issues:write"}, Actor: "person-owner", At: at}
	return runcontext.ResumeInput{Store: store, Resource: "run-r", Wait: wait, Evidence: "refreshed-1", Owner: "worker-b", DurationMs: 24 * hostHour, Current: &evidence, Reauthorization: &action}
}

// claimed parks the proof, wakes the wait and claims it: the credentials current again.
func (s *hostSetup) claimed(t *testing.T) runcontext.ResumedWait {
	t.Helper()
	held, err := s.park(runcontext.ReauthorizationOf(s.view), "wait-r", hostKey, s.fence)
	if err != nil {
		t.Fatal(refusalReason(err))
	}
	resumed, err := runcontext.ResumeParked(s.ctx, s.wakeInput(t, "wait-r", hostKey, held, s.store))
	if err != nil {
		t.Fatal(refusalReason(err))
	}
	s.refuse.Store(false)
	return resumed
}

func (s *hostSetup) viewOf(t *testing.T, resumed runcontext.ResumedWait, host runcontext.EffectHost) runcontext.RunStore {
	t.Helper()
	view, err := runcontext.ReauthorizedRunStore(s.ctx, s.store, host, resumed)
	if err != nil {
		t.Fatal(refusalReason(err))
	}
	return view
}

// reauthRun runs the Runtime action fixture with an allowed call: the model (or the native agent) asks for
// the Runtime evaluation, then ends with a final message.
func reauthRun(t *testing.T, f *runtimeActionFixture, store runcontext.RunStore, engine string, resume bool, requests *atomic.Int32) ([]contract.RunEvent, error) {
	t.Helper()
	events := []contract.RunEvent{}
	run := &adapter.Run{Prompt: "Evaluate the unsaved draft.", Wire: "openai-compatible", ModelName: "fixture", Thinking: "off", Critic: "off", Gate: f.gate.Calls(), Tools: f.gate.Offer().Tools, AllowConversation: true, Context: &runcontext.Input{CurrentRequest: &runcontext.CurrentRequest{Message: "Evaluate the unsaved draft.", Input: "draft"}, RunStore: &runcontext.DurableRunInput{Store: store, ID: "runtime-action-run", Resume: resume}}, Emit: func(e contract.RunEvent) { events = append(events, e) }}
	run.Context.Principal = f.principal
	run.EnrollContext = func(ctx context.Context, offer adapter.ContextOffer) error {
		return f.gate.EnrollContext(offer.ID, offer.HostTools)
	}
	var implementation adapter.Adapter
	if engine == "native" {
		implementation = native.New()
		run.Agent = &adapter.Agent{Model: "fixture", Run: func(ctx context.Context, request adapter.AgentRequest, cb adapter.AgentCallbacks) error {
			if cb.ProviderRelease != nil {
				defer cb.ProviderRelease.Acknowledge(cb.ProviderRelease.Invocation)
			}
			requests.Add(1)
			for _, tool := range request.Tools {
				if strings.HasPrefix(tool.Description, "experimental_evaluate: ") {
					if _, err := cb.Tool(ctx, adapter.AgentToolCall{ID: "native-call", Name: tool.Name, Arguments: json.RawMessage(`{"value":7,"rehearsal":false}`)}); err != nil {
						return err
					}
					return cb.Event(ctx, adapter.AgentEvent{Type: "message", ID: "final", Text: "Evaluation completed.", Phase: "final"})
				}
			}
			return errors.New("Runtime evaluation was not offered")
		}}
	} else {
		implementation = eino.New()
		seen := 0
		run.Model = func(context.Context, string, relay.Request) (*http.Response, error) {
			requests.Add(1)
			seen++
			if seen == 1 {
				return runtimeActionResponse(), nil
			}
			return enrollmentFinal(), nil
		}
	}
	err := implementation.Run(context.Background(), run)
	return events, err
}

// waitStore stands for a host store kind whose leases and waits outlive its process (#224).
type waitStore struct{ *runcontext.MemoryRunStore }

func (waitStore) LeaseLifetime() string { return runcontext.LeaseLifetimeDurable }

func (waitStore) WaitLifetime() string { return runcontext.WaitLifetimeDurable }

type waitClock struct{ at atomic.Int64 }

func (c *waitClock) now() time.Time { return time.UnixMilli(c.at.Load()).UTC() }

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

type runtimeActionLink struct{ context *runcontext.SessionContext }

func (l *runtimeActionLink) Attach(c *runcontext.SessionContext) { l.context = c }
