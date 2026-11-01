package adapter_test

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
	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
	"github.com/Judgment-Pack/judgment-pack-agent/go/relay"
)

// #226, the security review's fix round: a host act tool (whose request is its arguments, so it keeps its
// identity on every adapter) through the real eino and agent adapters. The TypeScript twin is
// packages/harness/storage/reauth.test.ts.

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
	return runcontext.ResumeEvidence{Version: 1, Resource: "run-r", Wait: wait, Model: contract.RunResumeCurrentModel{Provider: "openai", Model: "fixture", Status: "available"}, Policy: contract.RunResumeCurrentPolicy{Digest: held.Pins.Policy}, Plan: held.Pins.Plan, Sources: held.Pins.Sources, Artifacts: held.Pins.Artifacts}
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

// mintingHost is a host certified for the store: the reference host's admission for the kit's operations
// (source host), the case's body for the run's.
type mintingHost struct {
	reference *runcontext.ReferenceEffectHost
	body      func(context.Context, runcontext.EffectAdmission, func(context.Context) error) error
}

func (h *mintingHost) EffectFencing() string { return runcontext.EffectFencingEnforced }
func (h *mintingHost) Dispatch(ctx context.Context, admission runcontext.EffectAdmission, perform func(context.Context) error) error {
	if admission.Operation.Source == "host" {
		return h.reference.Dispatch(ctx, admission, perform)
	}
	return h.body(ctx, admission, perform)
}
func certifiedAs(t *testing.T, store hostStore, body func(context.Context, runcontext.EffectAdmission, func(context.Context) error) error) *runcontext.CertifiedEffectHost {
	t.Helper()
	certified, err := runcontext.CertifyEffectHost(context.Background(), func(authority runcontext.LeaseAuthority) runcontext.EffectHost {
		return &mintingHost{reference: runcontext.NewReferenceEffectHost(authority), body: body}
	}, store)
	if err != nil {
		t.Fatal(err)
	}
	return certified
}

// H1: performed, then refused: the row and the model hear an unknown outcome, with nothing said about
// admission, and there is no proof.
func TestReauthorizationPerformedThenRefusedIsAnUnknownOutcome(t *testing.T) {
	for _, engine := range hostEngines {
		t.Run(engine, func(t *testing.T) {
			ctx := context.Background()
			store := waitStore{runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})}
			certified := certifiedAs(t, store, func(ctx context.Context, a runcontext.EffectAdmission, perform func(context.Context) error) error {
				_ = perform(ctx)
				return runcontext.NewReauthorizationRequired(ctx, a, *hostRefusal())
			})
			lease, _ := store.AcquireLease(ctx, runcontext.LeaseAcquire{Resource: "run-r", Owner: "worker-a", DurationMs: hostHour})
			view, err := runcontext.UnattendedRunStore(store, runcontext.FenceOf(lease), certified)
			if err != nil {
				t.Fatal(err)
			}
			var n atomic.Int32
			out := runHostTool(t, view, "run-r", engine, false, nil, hostTool(hostExecute(&n, nil), "allow"), []string{hostArgs})
			if n.Load() != 1 || runcontext.ReauthorizationOf(view) != nil {
				t.Fatalf("executed %d proof %v", n.Load(), runcontext.ReauthorizationOf(view))
			}
			rows := hostRows(t, store, "run-r")
			if len(rows) != 1 || !unknownFailure(rows[0]) {
				t.Fatalf("rows %+v", rows)
			}
			if strings.Contains(strings.Join(out.bodies, "\n"), "not admitted") {
				t.Fatal("the model was told the call was not admitted")
			}
		})
	}
}

// H1: the re-send is admitted, then fails with a failure of the typed shape. No later view sends it again,
// whatever the effect host remembers: that is the row the grant reads.
func TestReauthorizationNoLaterViewSendsAnAdmittedCall(t *testing.T) {
	var kept *runcontext.ReauthorizationRequired
	for name, fails := range map[string]func() error{
		"the exported type": func() error { return &runcontext.ReauthorizationRequired{} },
		"a typed failure of the same shape": func() error {
			f := failure.New("authentication", "tool", "not-started")
			detail := "SENTINEL-HOST-DETAIL the operation was not admitted"
			f.Detail = &detail
			return failure.NewError(f)
		},
		"the run's own proof, kept and returned again": func() error { return kept },
	} {
		for _, engine := range hostEngines {
			t.Run(name+"/"+engine, func(t *testing.T) {
				s := hostRefused(t, engine, nil, func(call int32) error {
					if call == 1 {
						return fails()
					}
					return nil
				})
				kept = runcontext.ReauthorizationOf(s.view)
				resumed := s.claimed(t)
				first := runHostTool(t, s.viewOf(t, resumed, s.host), "run-r", engine, true, nil, s.tool, []string{hostArgs})
				rows := hostRows(t, s.store, "run-r")
				if s.executed.Load() != 1 || !unknownFailure(rows[len(rows)-1]) {
					t.Fatalf("executed %d rows %+v", s.executed.Load(), rows)
				}
				if strings.Contains(strings.Join(first.bodies, "\n"), "SENTINEL-HOST-DETAIL") {
					t.Fatal("the failure's detail reached the model")
				}
				// A second view of the claim, over the same host: not sent, so the host is not even asked.
				_ = runHostTool(t, s.viewOf(t, resumed, s.host), "run-r", engine, true, nil, s.tool, []string{hostArgs})
				if s.executed.Load() != 1 || len(s.host.Refusals()) != 0 {
					t.Fatalf("executed %d refusals %+v", s.executed.Load(), s.host.Refusals())
				}
				// Over a new reference host, as after a restart under the held lease: its admissions are gone.
				_ = runHostTool(t, s.viewOf(t, resumed, runcontext.NewReferenceEffectHost(s.store)), "run-r", engine, true, nil, s.tool, []string{hostArgs})
				if s.executed.Load() != 1 {
					t.Fatalf("the admitted call ran %d times", s.executed.Load())
				}
			})
		}
	}
}

// dyingStore loses every commit after the one that takes a host dispatch's claim, as a process that died
// there would. armed is set once the re-send's turn starts.
type dyingStore struct {
	waitStore
	armed, dead *atomic.Bool
}

func (d dyingStore) dying(checkpoint []byte) error {
	if d.dead.Load() {
		return errors.New("the process died")
	}
	if !d.armed.Load() || checkpoint == nil {
		return nil
	}
	var head struct {
		Durable struct {
			Dispatches []hostRow `json:"dispatches"`
		} `json:"durable"`
	}
	if json.Unmarshal(checkpoint, &head) == nil {
		for _, row := range head.Durable.Dispatches {
			if row.Kind == "host" && row.State == "claimed" {
				d.dead.Store(true)
			}
		}
	}
	return nil
}
func (d dyingStore) CommitFenced(ctx context.Context, input runcontext.RunCommit, fence runcontext.LeaseFence) (runcontext.RunReceipt, error) {
	if err := d.dying(input.Checkpoint); err != nil {
		return runcontext.RunReceipt{}, err
	}
	return d.waitStore.CommitFenced(ctx, input, fence)
}
func (d dyingStore) CommitHistoryFenced(ctx context.Context, input runcontext.RunHistoryCommit, fence runcontext.LeaseFence) (runcontext.RunHistoryReceipt, error) {
	if err := d.dying(input.Run.Checkpoint); err != nil {
		return runcontext.RunHistoryReceipt{}, err
	}
	return d.waitStore.CommitHistoryFenced(ctx, input, fence)
}

// A crash after the re-send is claimed and performed: the head holds a claim with no failure, and no later
// view runs it, though a new reference host has forgotten the admission.
func TestReauthorizationACrashAfterTheResendNeverRunsItTwice(t *testing.T) {
	for _, engine := range hostEngines {
		t.Run(engine, func(t *testing.T) {
			armed, dead := &atomic.Bool{}, &atomic.Bool{}
			s := hostRefused(t, engine, func(base waitStore) hostStore { return dyingStore{base, armed, dead} }, nil)
			resumed := s.claimed(t)
			armed.Store(true)
			_ = runHostTool(t, s.viewOf(t, resumed, s.host), "run-r", engine, true, nil, s.tool, []string{hostArgs})
			if !dead.Load() || s.executed.Load() != 1 {
				t.Fatalf("dead %v executed %d", dead.Load(), s.executed.Load())
			}
			rows := hostRows(t, s.store, "run-r")
			if last := rows[len(rows)-1]; last.State != "claimed" || last.Failure != nil {
				t.Fatalf("the head after the crash: %+v", last)
			}
			armed.Store(false)
			dead.Store(false)
			_ = runHostTool(t, s.viewOf(t, resumed, runcontext.NewReferenceEffectHost(s.store)), "run-r", engine, true, nil, s.tool, []string{hostArgs})
			if s.executed.Load() != 1 {
				t.Fatalf("the crashed call ran %d times", s.executed.Load())
			}
		})
	}
}

// M2: a refusal followed by a late perform runs nothing and is no proof. After the park, a late perform still
// runs nothing, and the re-send is the one run.
func TestReauthorizationALatePerformRunsNothing(t *testing.T) {
	for _, engine := range hostEngines {
		t.Run(engine, func(t *testing.T) {
			ctx := context.Background()
			clock := &waitClock{}
			clock.at.Store(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli())
			var refuse atomic.Bool
			refuse.Store(true)
			var late func(context.Context) error
			var lateCtx context.Context
			body := func(ctx context.Context, a runcontext.EffectAdmission, perform func(context.Context) error) error {
				if !refuse.Load() {
					return perform(ctx)
				}
				late, lateCtx = perform, ctx
				return runcontext.NewReauthorizationRequired(ctx, a, *hostRefusal())
			}
			var n atomic.Int32
			tool := hostTool(hostExecute(&n, nil), "allow")
			// Before the park: the refusal is taken back.
			first := waitStore{runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{Now: clock.now})}
			lease, _ := first.AcquireLease(ctx, runcontext.LeaseAcquire{Resource: "run-r", Owner: "worker-a", DurationMs: hostHour})
			view, err := runcontext.UnattendedRunStore(first, runcontext.FenceOf(lease), certifiedAs(t, first, body))
			if err != nil {
				t.Fatal(err)
			}
			_ = runHostTool(t, view, "run-r", engine, false, nil, tool, []string{hostArgs})
			if runcontext.ReauthorizationOf(view) == nil {
				t.Fatal("no proof before the late perform")
			}
			if err := late(lateCtx); refusalReason(err) != "lease:invalid" {
				t.Fatalf("a late perform: %v", err)
			}
			if n.Load() != 0 || runcontext.ReauthorizationOf(view) != nil {
				t.Fatalf("executed %d proof %v", n.Load(), runcontext.ReauthorizationOf(view))
			}
			// After the park: nothing runs, and the re-send is the one run.
			second := waitStore{runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{Now: clock.now})}
			certified := certifiedAs(t, second, body)
			lease, _ = second.AcquireLease(ctx, runcontext.LeaseAcquire{Resource: "run-r", Owner: "worker-a", DurationMs: hostHour})
			view, err = runcontext.UnattendedRunStore(second, runcontext.FenceOf(lease), certified)
			if err != nil {
				t.Fatal(err)
			}
			_ = runHostTool(t, view, "run-r", engine, false, nil, tool, []string{hostArgs})
			s := &hostSetup{ctx: ctx, clock: clock, store: second, view: view, fence: runcontext.FenceOf(lease), refuse: &refuse}
			held, err := s.park(runcontext.ReauthorizationOf(view), "wait-r", hostKey, s.fence)
			if err != nil {
				t.Fatal(refusalReason(err))
			}
			if err := late(lateCtx); refusalReason(err) != "lease:invalid" || n.Load() != 0 {
				t.Fatalf("a late perform after the park: %v, executed %d", err, n.Load())
			}
			resumed, err := runcontext.ResumeParked(ctx, s.wakeInput(t, "wait-r", hostKey, held, second))
			if err != nil {
				t.Fatal(refusalReason(err))
			}
			refuse.Store(false)
			_ = runHostTool(t, s.viewOf(t, resumed, certified), "run-r", engine, true, nil, tool, []string{hostArgs})
			if n.Load() != 1 {
				t.Fatalf("executed %d", n.Load())
			}
		})
	}
}

// M3: a re-send refused again stops the run and cannot park (unbound): the continuation's own model or agent
// row follows the refused row. On the same view the grant is spent; another view of the claim sends it once.
func TestReauthorizationARefusedResendCannotParkAndItsGrantIsSpent(t *testing.T) {
	for _, engine := range hostEngines {
		t.Run(engine, func(t *testing.T) {
			s := hostRefused(t, engine, nil, nil)
			resumed := s.claimed(t)
			s.refuse.Store(true)
			v1 := s.viewOf(t, resumed, s.host)
			_ = runHostTool(t, v1, "run-r", engine, true, nil, s.tool, []string{hostArgs})
			proof := runcontext.ReauthorizationOf(v1)
			if s.executed.Load() != 0 || proof == nil {
				t.Fatalf("executed %d proof %v", s.executed.Load(), proof)
			}
			if _, err := s.park(proof, "wait-r2", "wk.reauth.host.Hs8Pq3Lz6Wd1Rm9Tc4Vb7Nx2A", resumed.Fence); refusalReason(err) != "reauth:unbound" {
				t.Fatalf("a second park: %v", err)
			}
			rows := hostRows(t, s.store, "run-r")
			if last := rows[len(rows)-1]; last.State != "unknown" || last.Failure["outcome"] != "not-started" {
				t.Fatalf("the refused row: %+v", last)
			}
			s.refuse.Store(false)
			_ = runHostTool(t, v1, "run-r", engine, true, nil, s.tool, []string{hostArgs})
			if s.executed.Load() != 0 {
				t.Fatalf("a spent grant sent it: executed %d", s.executed.Load())
			}
			_ = runHostTool(t, s.viewOf(t, resumed, s.host), "run-r", engine, true, nil, s.tool, []string{hostArgs})
			if s.executed.Load() != 1 {
				t.Fatalf("another view of the claim: executed %d", s.executed.Load())
			}
		})
	}
}

// The grant is for the request that was refused, byte for byte: the effect host admits that request's digest,
// and the tool runs the bytes the call wrote. So other bytes never run under the refused identity. A re-send that
// is not those bytes gets what any call gets from the row (#275): an exact variant is this write, and is answered
// HostOutcomeUnknown; the same values written differently are refused with DurableRequestConflict; different
// numbers are another write, which runs under an identity of its own. Each way the refused row keeps its typed
// failure and the grant stays, and the request itself then runs once, on its own row.
func TestReauthorizationTheGrantIsForTheBytesThatWereRefused(t *testing.T) {
	const unknownOutcome = "The tool could not complete this operation." // HostOutcomeUnknown, as the model reads it
	cases := map[string]struct {
		refused, sent string
		answer        string
	}{
		"past what a float64 holds": {`{"id":9007199254740992}`, `{"id":9007199254740993}`, ""},
		"another spelling":          {`{"id":1}`, `{"id":1.0}`, "DurableRequestConflict"},
		"a repeated name":           {`{"id":2}`, `{"id":1,"id":2}`, "DurableRequestConflict"},
		"whitespace":                {`{"id":1}`, `{ "id": 1 }`, unknownOutcome},
		"member order":              {`{"a":1,"b":2}`, `{"b":2,"a":1}`, unknownOutcome},
	}
	for _, engine := range hostEngines {
		for name, held := range cases {
			t.Run(engine+"/"+name, func(t *testing.T) {
				s := hostRefusedWith(t, engine, held.refused, nil, nil)
				ran := []string{}
				inner := s.tool.Execute
				s.tool.Execute = func(ctx context.Context, raw json.RawMessage) (runcontext.ToolResult, error) {
					ran = append(ran, string(raw))
					return inner(ctx, raw)
				}
				resumed := s.claimed(t)
				view := s.viewOf(t, resumed, s.host)
				out := runHostTool(t, view, "run-r", engine, true, nil, s.tool, []string{held.sent})
				rows := hostRows(t, s.store, "run-r")
				if held.answer != "" {
					if len(ran) != 0 {
						t.Fatalf("other bytes ran: %q", ran)
					}
					if len(rows) != 1 {
						t.Fatalf("the rows: %+v", rows)
					}
					answered := false
					for _, event := range out.events {
						if event.ToolResult != nil && strings.Contains(event.ToolResult.Text, held.answer) {
							answered = true
						}
					}
					if !answered {
						texts := []string{}
						for _, event := range out.events {
							if event.ToolResult != nil {
								texts = append(texts, event.ToolResult.Text)
							}
						}
						t.Fatalf("not answered %s: %q, %v", held.answer, texts, out.err)
					}
				} else if len(ran) != 1 || ran[0] != held.sent || len(rows) != 2 || rows[1].Request != held.sent || rows[1].State != "result" {
					t.Fatalf("another write runs on its own row: ran %q, rows %+v", ran, rows)
				}
				if rows[0].Request != held.refused || rows[0].State != "unknown" || rows[0].Failure["outcome"] != "not-started" {
					t.Fatalf("the refused row: %+v", rows)
				}
				_ = runHostTool(t, view, "run-r", engine, true, nil, s.tool, []string{held.refused})
				if len(ran) == 0 || ran[len(ran)-1] != held.refused {
					t.Fatalf("the refused request itself: %q", ran)
				}
				if rows := hostRows(t, s.store, "run-r"); rows[0].State != "result" {
					t.Fatalf("the refused row after its grant: %+v", rows)
				}
			})
		}
	}
}

// #271: no arguments at all run as {}, so they are the same request as {} on the credential path, either way
// round, and it runs once with {}. A write's immutable hint gives it no read cache to be answered from.
func TestReauthorizationNoArgumentsAreTheRequestForAnEmptyObject(t *testing.T) {
	for _, engine := range hostEngines {
		for _, held := range [][2]string{{`{}`, ``}, {``, `{}`}} {
			t.Run(fmt.Sprintf("%s/%q then %q", engine, held[0], held[1]), func(t *testing.T) {
				s := hostRefusedWith(t, engine, held[0], nil, nil)
				if s.executed.Load() != 0 || runcontext.ReauthorizationOf(s.view) == nil {
					t.Fatalf("the refusal: executed %d", s.executed.Load())
				}
				ran := []string{}
				inner := s.tool.Execute
				s.tool.Execute = func(ctx context.Context, raw json.RawMessage) (runcontext.ToolResult, error) {
					ran = append(ran, string(raw))
					return inner(ctx, raw)
				}
				s.tool.ContextPolicy = &runcontext.ToolContextPolicy{Immutable: true}
				resumed := s.claimed(t)
				view := s.viewOf(t, resumed, s.host)
				_ = runHostTool(t, view, "run-r", engine, true, nil, s.tool, []string{held[1], held[1]})
				if len(ran) != 1 || ran[0] != `{}` {
					t.Fatalf("the re-send ran %q", ran)
				}
				rows := hostRows(t, s.store, "run-r")
				if len(rows) != 1 || rows[0].State != "result" {
					t.Fatalf("the row: %+v", rows)
				}
			})
		}
	}
}

// A takeover gets no grant: its fence is not the claim's, and the old fence no longer holds the lease.
func TestReauthorizationATakeoverGetsNoGrant(t *testing.T) {
	s := hostRefused(t, "native", nil, nil)
	resumed := s.claimed(t)
	s.clock.at.Add(25 * hostHour)
	head, _ := s.store.Load(s.ctx, "run-r")
	x, err := s.store.AcquireLease(s.ctx, runcontext.LeaseAcquire{Resource: "run-r", Owner: "worker-x", ExpectedRevision: head.Revision, DurationMs: hostHour})
	if err != nil {
		t.Fatal(refusalReason(err))
	}
	taken := resumed
	taken.Fence = runcontext.FenceOf(x)
	if _, err := runcontext.ReauthorizedRunStore(s.ctx, s.store, s.host, taken); refusalReason(err) != "wait:invalid" {
		t.Fatalf("the takeover's fence: %v", err)
	}
	if _, err := runcontext.ReauthorizedRunStore(s.ctx, s.store, s.host, resumed); refusalReason(err) != "lease:stale-epoch" {
		t.Fatalf("the old fence: %v", err)
	}
	plain, _ := runcontext.UnattendedRunStore(s.store, runcontext.FenceOf(x), s.host)
	_ = runHostTool(t, plain, "run-r", "native", true, nil, s.tool, []string{hostArgs})
	if s.executed.Load() != 0 {
		t.Fatalf("a plain view sent it: executed %d", s.executed.Load())
	}
}

// The view needs the claim's lease held: released, it is refused.
func TestReauthorizationTheViewNeedsTheClaimsLeaseHeld(t *testing.T) {
	s := hostRefused(t, "native", nil, nil)
	resumed := s.claimed(t)
	if _, err := s.store.ReleaseLease(s.ctx, resumed.Fence); err != nil {
		t.Fatal(err)
	}
	if _, err := runcontext.ReauthorizedRunStore(s.ctx, s.store, s.host, resumed); refusalReason(err) != "lease:stale-epoch" {
		t.Fatalf("a released lease: %v", err)
	}
}

// forgingStore answers a reauthorization claim with what forge makes of it.
type forgingStore struct {
	waitStore
	forge func(runcontext.RunWait, runcontext.RunLease) (runcontext.RunWait, runcontext.RunLease)
}

func (f forgingStore) ResumeWait(ctx context.Context, claim runcontext.WaitClaim) (runcontext.RunWait, runcontext.RunLease, error) {
	wait, lease, err := f.waitStore.ResumeWait(ctx, claim)
	if err != nil || claim.Reauthorized == nil {
		return wait, lease, err
	}
	wait, lease = f.forge(wait, lease)
	return wait, lease, nil
}

// A claim that did not record the refresh it was given is refused invalid.
func TestReauthorizationAClaimMustRecordTheRefresh(t *testing.T) {
	s := hostRefused(t, "native", nil, nil)
	held, err := s.park(runcontext.ReauthorizationOf(s.view), "wait-r", hostKey, s.fence)
	if err != nil {
		t.Fatal(refusalReason(err))
	}
	forged := forgingStore{s.store.(waitStore), func(w runcontext.RunWait, l runcontext.RunLease) (runcontext.RunWait, runcontext.RunLease) {
		refresh := *w.Reauthorized
		refresh.Permissions = []string{"issues:read", "issues:write"}
		w.Reauthorized = &refresh
		return w, l
	}}
	if _, err := runcontext.ResumeParked(s.ctx, s.wakeInput(t, "wait-r", hostKey, held, forged)); refusalReason(err) != "wait:invalid" {
		t.Fatalf("a forged refresh: %v", err)
	}
}

// M9: a credential check's error, a typed failure with a detail included, stays with the host.
func TestReauthorizationACheckErrorStaysWithTheHost(t *testing.T) {
	const secret = "SENTINEL-CHECK-DETAIL-token-4f1c"
	for name, check := range map[string]error{
		"a plain error":   errors.New(secret),
		"a wrapped error": fmt.Errorf("refresh: %w", errors.New(secret)),
		"an authored":     failure.Authored("authentication", secret, "tool"),
		"a typed not-started": func() error {
			f := failure.New("authentication", "tool", "not-started")
			detail := secret
			f.Detail = &detail
			return failure.NewError(f)
		}(),
	} {
		for _, engine := range hostEngines {
			t.Run(name+"/"+engine, func(t *testing.T) {
				ctx := context.Background()
				store := waitStore{runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})}
				host := runcontext.NewReferenceEffectHostWith(store, runcontext.ReferenceEffectHostOptions{Credentials: func(context.Context, runcontext.EffectOperation) (*runcontext.CredentialRefusal, error) {
					return nil, check
				}})
				lease, _ := store.AcquireLease(ctx, runcontext.LeaseAcquire{Resource: "run-r", Owner: "worker-a", DurationMs: hostHour})
				view, err := runcontext.UnattendedRunStore(store, runcontext.FenceOf(lease), host)
				if err != nil {
					t.Fatal(err)
				}
				var n atomic.Int32
				out := runHostTool(t, view, "run-r", engine, false, nil, hostTool(hostExecute(&n, nil), "allow"), []string{hostArgs})
				if n.Load() != 0 || runcontext.ReauthorizationOf(view) != nil {
					t.Fatalf("executed %d proof %v", n.Load(), runcontext.ReauthorizationOf(view))
				}
				rows := hostRows(t, store, "run-r")
				if len(rows) != 1 || rows[0].State != "unknown" || rows[0].Failure["code"] != "unknown" || rows[0].Failure["outcome"] != "unknown" {
					t.Fatalf("rows %+v", rows)
				}
				head, _ := store.Load(ctx, "run-r")
				raw, _ := json.Marshal(out.events)
				page, _ := store.ReadEvents(ctx, "run-r", "", 500)
				published := []string{string(head.Checkpoint), string(raw), strings.Join(out.bodies, "\n")}
				for _, item := range page.Items {
					published = append(published, string(item.Bytes))
				}
				if strings.Contains(strings.Join(published, "\n"), secret) {
					t.Fatal("the check's error was published")
				}
			})
		}
	}
}

// M9: an actor that is free text, an address or a bearer string is refused: nothing is claimed or exported.
func TestReauthorizationAnActorIsABoundedReference(t *testing.T) {
	s := hostRefused(t, "native", nil, nil)
	held, err := s.park(runcontext.ReauthorizationOf(s.view), "wait-r", hostKey, s.fence)
	if err != nil {
		t.Fatal(refusalReason(err))
	}
	input := s.wakeInput(t, "wait-r", hostKey, held, s.store)
	for _, actor := range []string{"Bearer SENTINEL-ACCESS-TOKEN person@example.invalid", "person@example.invalid", strings.Repeat("a", 65)} {
		action := *input.Reauthorization
		action.Actor = actor
		attempt := input
		attempt.Reauthorization = &action
		if _, err := runcontext.ResumeParked(s.ctx, attempt); refusalReason(err) != "reauth:invalid" {
			t.Fatalf("%s: %v", actor, err)
		}
	}
	exported, _ := runcontext.ExportWaits(s.ctx, s.store, "run-r")
	raw, _ := json.Marshal(exported)
	if strings.Contains(string(raw), "SENTINEL") || exported.Waits[0].State != "woken" {
		t.Fatalf("export %s", raw)
	}
}

// approvedThenRefused is an act call asked, parked with a one-hour approval, approved and claimed, whose
// dispatch the reference effect host then refuses for its credentials, parked as a reauthorization wait.
type approvedRefused struct {
	*hostSetup
	tool     runcontext.HostTool
	executed *atomic.Int32
	w1, w2   runcontext.RunWait
	decision *contract.CheckpointApprovalDecision
	input    runcontext.ResumeInput
	base     waitStore
}

func approvedThenRefused(t *testing.T, engine string) *approvedRefused {
	t.Helper()
	const approvalKey = "wk.reauth.host.Ka5Ue2Jy8Gt3Wo6Bf9Mn1Sv4B"
	ctx := context.Background()
	clock := &waitClock{}
	clock.at.Store(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli())
	base := waitStore{runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{Now: clock.now})}
	refuse := &atomic.Bool{}
	host := runcontext.NewReferenceEffectHostWith(base, runcontext.ReferenceEffectHostOptions{Credentials: func(context.Context, runcontext.EffectOperation) (*runcontext.CredentialRefusal, error) {
		if refuse.Load() {
			return hostRefusal(), nil
		}
		return nil, nil
	}})
	n := &atomic.Int32{}
	tool := hostTool(hostExecute(n, nil), "ask")
	a, _ := base.AcquireLease(ctx, runcontext.LeaseAcquire{Resource: "run-r", Owner: "worker-a", DurationMs: hostHour})
	view, _ := runcontext.UnattendedRunStore(base, runcontext.FenceOf(a), host)
	first := runHostTool(t, view, "run-r", engine, false, nil, tool, []string{hostArgs})
	var asked *contract.RunEventApprovalRequested
	for _, e := range first.events {
		if e.ApprovalRequested != nil {
			asked = e.ApprovalRequested
		}
	}
	if asked == nil {
		t.Fatal("no approval request")
	}
	approvalDigest, _ := runcontext.WakeKeyDigest(approvalKey)
	ttl := hostHour
	w1, _, err := runcontext.ParkRun(ctx, runcontext.ParkInput{Store: base, Fence: runcontext.FenceOf(a), Wait: "wait-a", Continuation: "continue-a", Limits: &runcontext.WaitLimits{ApprovalTtlMs: &ttl}, Condition: runcontext.WaitCondition{Event: &contract.RunWaitEventCondition{Kind: "approval-decision", Subject: asked.CallID}}, Model: runcontext.PinnedModel{Provider: "openai", Model: "fixture"}, WakeKeyDigest: approvalDigest, Release: []runcontext.WaitRelease{}})
	if err != nil {
		t.Fatal(refusalReason(err))
	}
	clock.at.Add(60_000)
	if _, err := runcontext.WakeRun(ctx, base, runcontext.WakeEvidence{Version: 1, Resource: "run-r", Wait: "wait-a", Evidence: "decision-1", Kind: "event", Event: &contract.RunWaitEventCondition{Kind: "approval-decision", Subject: asked.CallID}, Key: approvalKey}); err != nil {
		t.Fatal(err)
	}
	current := hostEvidence("wait-a", w1)
	decision := &contract.CheckpointApprovalDecision{CallID: asked.CallID, Approved: true}
	approved, err := runcontext.ResumeParked(ctx, runcontext.ResumeInput{Store: base, Resource: "run-r", Wait: "wait-a", Evidence: "decision-1", Owner: "worker-b", DurationMs: hostHour, Approval: decision, Current: &current})
	if err != nil {
		t.Fatal(refusalReason(err))
	}
	// The credentials expired meanwhile: the approved call is refused at the admission boundary.
	refuse.Store(true)
	refused, _ := runcontext.UnattendedRunStore(base, approved.Fence, host)
	_ = runHostTool(t, refused, "run-r", engine, true, decision, tool, nil)
	proof := runcontext.ReauthorizationOf(refused)
	if n.Load() != 0 || proof == nil {
		t.Fatalf("executed %d proof %v", n.Load(), proof)
	}
	s := &hostSetup{ctx: ctx, clock: clock, store: base, refuse: refuse, host: host}
	w2, err := s.park(proof, "wait-r", hostKey, approved.Fence)
	if err != nil {
		t.Fatal(refusalReason(err))
	}
	return &approvedRefused{hostSetup: s, tool: tool, executed: n, w1: w1, w2: w2, decision: decision, input: s.wakeInput(t, "wait-r", hostKey, w2, base), base: base}
}

// M7: an approved act call refused for its credentials, on both adapters. The wait carries the approval's
// binding and expiry; the claim is bounded by it; a changed policy or descriptor refuses the reviewed call;
// and ADR-0008 never sends the approved call again, whatever the refresh. The TypeScript twin is the
// describe "an approved call refused for its credentials" in packages/harness/storage/reauth.test.ts.
func TestReauthorizationAnApprovedCallRefusedIsNotSentAgain(t *testing.T) {
	for _, engine := range hostEngines {
		t.Run(engine, func(t *testing.T) {
			a := approvedThenRefused(t, engine)
			ctx, input := a.ctx, a.input
			expiry := *a.w1.Deadline.Approval
			if a.w2.Approval == nil || *a.w2.Approval != *a.w1.Approval || a.w2.Deadline == nil || a.w2.Deadline.Approval == nil || *a.w2.Deadline.Approval != expiry || a.w2.Deadline.At != expiry {
				t.Fatalf("the wait's approval %+v deadline %+v", a.w2.Approval, a.w2.Deadline)
			}
			// The approved call's descriptor is pinned: changed, it needs an admission, and an admission of it
			// cannot carry the reviewed call.
			var pin *runcontext.SourcePin
			for i := range a.w2.Pins.Sources {
				if a.w2.Pins.Sources[i].Source == "tracker" {
					pin = &a.w2.Pins.Sources[i]
				}
			}
			if pin == nil || pin.DescriptorDigest == nil {
				t.Fatalf("the approved call's source is not pinned with its descriptor: %+v", a.w2.Pins.Sources)
			}
			other := "sha256:" + strings.Repeat("d", 64)
			withDescriptor := func(admissions ...runcontext.ResumeAdmission) runcontext.ResumeInput {
				evidence := *input.Current
				evidence.Sources = []runcontext.SourcePin{}
				for _, held := range input.Current.Sources {
					if held.Source == "tracker" {
						held.DescriptorDigest = &other
					}
					evidence.Sources = append(evidence.Sources, held)
				}
				evidence.Admissions = admissions
				out := input
				out.Current = &evidence
				return out
			}
			if _, err := runcontext.ResumeParked(ctx, withDescriptor()); refusalReason(err) != "resume:reference-changed" {
				t.Fatalf("a changed descriptor: %v", err)
			}
			if _, err := runcontext.ResumeParked(ctx, withDescriptor(runcontext.ResumeAdmission{Kind: "reference", Subject: "source:tracker", From: *pin.DescriptorDigest, To: other, Actor: "person-owner", At: input.Reauthorization.At})); refusalReason(err) != "resume:approval-changed" {
				t.Fatalf("an admitted changed descriptor: %v", err)
			}
			// A changed policy under a trusted layer is accepted for a run, but not for the reviewed call.
			layered := *input.Current
			layer := "sha256:" + strings.Repeat("c", 64)
			layered.Policy = contract.RunResumeCurrentPolicy{Digest: "sha256:" + strings.Repeat("b", 64), Layer: &layer}
			changed := input
			changed.Current = &layered
			if _, err := runcontext.ResumeParked(ctx, changed); refusalReason(err) != "resume:approval-changed" {
				t.Fatalf("a changed policy: %v", err)
			}
			// The claim's lease ends at the approval's expiry, whatever it asks for.
			resumed, err := runcontext.ResumeParked(ctx, input)
			if err != nil {
				t.Fatal(refusalReason(err))
			}
			if resumed.Lease.Expiry != expiry {
				t.Fatalf("the claim's lease ends %s, the approval %s", resumed.Lease.Expiry, expiry)
			}
			a.refuse.Store(false)
			view := a.viewOf(t, resumed, a.host)
			// Continued with the earlier decision: refused, and nothing runs.
			withDecision := runHostTool(t, view, "run-r", engine, true, a.decision, a.tool, nil)
			failed := false
			for _, e := range withDecision.events {
				failed = failed || e.Error != nil
			}
			if !failed || a.executed.Load() != 0 {
				t.Fatalf("with a decision: failed %v executed %d", failed, a.executed.Load())
			}
			// Continued without one: the call is restored as executed with an unknown outcome, never sent.
			_ = runHostTool(t, view, "run-r", engine, true, nil, a.tool, nil)
			if a.executed.Load() != 0 {
				t.Fatalf("the approved call ran %d times", a.executed.Load())
			}
			head, _ := a.store.Load(ctx, "run-r")
			var checkpoint struct {
				Approval struct {
					State  string          `json:"state"`
					Result json.RawMessage `json:"result"`
				} `json:"approval"`
			}
			if err := json.Unmarshal(head.Checkpoint, &checkpoint); err != nil {
				t.Fatal(err)
			}
			if checkpoint.Approval.State != "answered" && checkpoint.Approval.State != "executed" || !strings.Contains(string(checkpoint.Approval.Result), "may have run") {
				t.Fatalf("the approval: %s %s", checkpoint.Approval.State, checkpoint.Approval.Result)
			}
		})
	}
}

// A claim whose lease outlives the attached approval is refused invalid, nothing handed to the host.
func TestReauthorizationAClaimMustEndByTheAttachedApproval(t *testing.T) {
	a := approvedThenRefused(t, "native")
	forged := a.input
	forged.Store = forgingStore{a.base, func(w runcontext.RunWait, l runcontext.RunLease) (runcontext.RunWait, runcontext.RunLease) {
		l.Expiry = "2026-01-02T12:00:00.000Z"
		return w, l
	}}
	if _, err := runcontext.ResumeParked(a.ctx, forged); refusalReason(err) != "wait:invalid" {
		t.Fatalf("a forged lease: %v", err)
	}
}

// At the approval's expiry the reauthorization wait is not claimed, and its deadline wake ends it and the run.
func TestReauthorizationTheApprovalsExpiryEndsTheWait(t *testing.T) {
	a := approvedThenRefused(t, "native")
	a.clock.at.Store(time.Date(2026, 1, 1, 1, 0, 0, 0, time.UTC).UnixMilli())
	if _, err := runcontext.ResumeParked(a.ctx, a.input); refusalReason(err) != "wait:deadline-passed" {
		t.Fatalf("a claim at the expiry: %v", err)
	}
	if _, err := runcontext.WakeRun(a.ctx, a.store, runcontext.WakeEvidence{Version: 1, Resource: "run-r", Wait: "wait-r", Evidence: "deadline-1", Kind: "deadline", Key: hostKey}); err != nil {
		t.Fatal(refusalReason(err))
	}
	held, _ := a.store.ReadWait(a.ctx, "run-r", "wait-r")
	if held.State != "expired" {
		t.Fatalf("the wait: %s", held.State)
	}
	head, _ := a.store.Load(a.ctx, "run-r")
	if _, err := a.store.AcquireLease(a.ctx, runcontext.LeaseAcquire{Resource: "run-r", Owner: "worker-x", ExpectedRevision: head.Revision, DurationMs: hostHour}); refusalReason(err) != "wait:closed" {
		t.Fatalf("a lease after: %v", err)
	}
}
