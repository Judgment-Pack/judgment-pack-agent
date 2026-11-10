package jobtests

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
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

// #229: run-owned jobs through the real eino and agent adapters, the package's writer and the reference effect
// host. The TypeScript twin is packages/harness/storage/job.test.ts. These have their own binary: the adapter's
// is near its limit.
const (
	jobRun    = "run-j"
	jobKey    = "job.key.0001.Q7z2Xm9LpV4cR8tNb3Yh6Df1Gj5K"
	jobWake   = "wk.job.0001.Hs8Pq3Lz6Wd1Rm9Tc4Vb7Nx2Ka5U"
	jobSecret = "SENTINEL-EXTERNAL-HANDLE-4f1c9e7a2b"
	jobRcpt   = "SENTINEL-RECEIPT-8d3b6a0e5c"
	jobHour   = int64(3_600_000)
	jobArgs   = `{"name":"nightly"}`
)

var engines = []string{"eino", "native"}

type durableJobStore struct{ *runcontext.MemoryRunStore }

func (durableJobStore) LeaseLifetime() string { return runcontext.LeaseLifetimeDurable }
func (durableJobStore) WaitLifetime() string  { return runcontext.WaitLifetimeDurable }
func (durableJobStore) JobLifetime() string   { return runcontext.JobLifetimeDurable }

type jobClock struct {
	mu sync.Mutex
	at time.Time
}

func (c *jobClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.at }
func (c *jobClock) move(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

type leasedRun struct {
	store durableJobStore
	clock *jobClock
	host  *runcontext.ReferenceEffectHost
	lease runcontext.RunLease
	fence runcontext.LeaseFence
	view  runcontext.RunStore
}

func newLeased(t *testing.T, credentials runcontext.CredentialCheck) *leasedRun {
	t.Helper()
	clock := &jobClock{at: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	store := durableJobStore{runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{Now: clock.now})}
	var host *runcontext.ReferenceEffectHost
	if credentials != nil {
		host = runcontext.NewReferenceEffectHostWith(store, runcontext.ReferenceEffectHostOptions{Credentials: credentials})
	} else {
		host = runcontext.NewReferenceEffectHost(store)
	}
	lease, err := store.AcquireLease(context.Background(), runcontext.LeaseAcquire{Resource: jobRun, Owner: "worker-a", DurationMs: jobHour})
	if err != nil {
		t.Fatal(err)
	}
	fence := runcontext.FenceOf(lease)
	view, err := runcontext.UnattendedRunStore(store, fence, host)
	if err != nil {
		t.Fatal(err)
	}
	return &leasedRun{store: store, clock: clock, host: host, lease: lease, fence: fence, view: view}
}

type startCalls struct {
	mu       sync.Mutex
	requests []string
	jobs     []string
}

func (s *startCalls) count() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.requests) }

func answered(answer runcontext.JobStartAnswer, err error) func(context.Context, runcontext.JobStartInput) (runcontext.JobStartAnswer, error) {
	return func(context.Context, runcontext.JobStartInput) (runcontext.JobStartAnswer, error) { return answer, err }
}

var startedAnswer = runcontext.JobStartAnswer{Started: true, Handle: jobSecret, Receipt: jobRcpt}

func startTool(t *testing.T, calls *startCalls, start func(context.Context, runcontext.JobStartInput) (runcontext.JobStartAnswer, error), approve string, mint func(context.Context, string) (string, error)) runcontext.HostTool {
	t.Helper()
	schema := json.RawMessage(`{"type":"object"}`)
	taxonomy := &runcontext.ToolTaxonomy{Operation: "run", Effect: "act"}
	digest := runcontext.ToolListDigest([]runcontext.ListedTool{{Name: "start_job", Description: "Start one background job.", InputSchema: schema, Taxonomy: taxonomy}})
	if mint == nil {
		mint = func(context.Context, string) (string, error) { return runcontext.JobKeyDigest(jobKey) }
	}
	tool, err := runcontext.JobStartTool(runcontext.JobStartSpec{Name: "start_job", Description: "Start one background job.", InputSchema: schema,
		Source:  &runcontext.ToolSource{Kind: "custom", ID: "runner", Trust: "person", Version: "1", ToolListDigest: digest, CurrentDigest: func(context.Context) (string, error) { return digest, nil }},
		Approve: func(runcontext.ApprovalRequest) string { return approve }, Approval: "required", Mint: mint,
		Start: func(ctx context.Context, input runcontext.JobStartInput) (runcontext.JobStartAnswer, error) {
			calls.mu.Lock()
			calls.requests = append(calls.requests, string(input.Request))
			calls.jobs = append(calls.jobs, input.Job)
			calls.mu.Unlock()
			return start(ctx, input)
		}})
	if err != nil {
		t.Fatal(err)
	}
	return tool
}

// jobCall is one call the model or the agent makes: "start" or a wire name.
type jobCall struct{ tool, args string }

type runResult struct {
	events []contract.RunEvent
	err    error
	bodies []string
	asks   atomic.Int32
}

func modelCall(wire, id, args string) *http.Response {
	raw, _ := json.Marshal(map[string]any{"id": "fixture", "object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": id, "type": "function", "function": map[string]any{"name": wire, "arguments": args}}}}, "finish_reason": "tool_calls"}}})
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: " + string(raw) + "\n\ndata: [DONE]\n\n"))}
}
func modelFinal() *http.Response {
	raw, _ := json.Marshal(map[string]any{"id": "fixture", "object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": "Done."}, "finish_reason": "stop"}}})
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: " + string(raw) + "\n\ndata: [DONE]\n\n"))}
}

// options are what a continuation of a run brings.
type options struct {
	ctx          context.Context
	requestCap   *int
	authorize    func(context.Context) error
	resume       bool
	decision     *contract.CheckpointApprovalDecision
	continuation *runcontext.RunJob
	plan         *runcontext.Value
	// args, where set, is what each call's arguments are at the moment it is made (agent#323 LOW-3: a job the same run
	// started).
	args func(jobCall) string
}

// laterTurn (#330): a finished run is resumed by no plain resume, and a job continuation only where a claimed wait bears
// it out. A later turn here resumes the run as one stopped before its end would be: its head is left running, under the
// run's own fence. A continuation handed explicitly is the test's own case.
func laterTurn(t *testing.T, store runcontext.RunStore, with options) {
	t.Helper()
	if !with.resume || with.continuation != nil {
		return
	}
	interrupted(t, store)
}

// interrupted leaves a finished head running, as a run stopped before its end leaves it.
func interrupted(t *testing.T, store runcontext.RunStore) {
	t.Helper()
	head, err := store.Load(context.Background(), jobRun)
	if err != nil || head == nil || head.Checkpoint == nil {
		return
	}
	var saved map[string]any
	if err := json.Unmarshal(head.Checkpoint, &saved); err != nil {
		t.Fatal(err)
	}
	if saved["status"] != "complete" {
		return
	}
	saved["status"] = "running"
	raw, err := json.Marshal(saved)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Commit(context.Background(), runcontext.RunCommit{RunID: jobRun, ExpectedRevision: head.Revision, CommitID: fmt.Sprintf("interrupted-%d", time.Now().UnixNano()), Checkpoint: raw, Events: [][]byte{}}); err != nil {
		t.Fatal(err)
	}
}

// drive runs the tools: the model or the agent makes each call, then ends.
func drive(t *testing.T, store runcontext.RunStore, engine string, tools []runcontext.HostTool, calls []jobCall, with options) *runResult {
	t.Helper()
	out := &runResult{}
	laterTurn(t, store, with)
	var mu sync.Mutex
	g := gate.New(gate.Options{})
	g.SetOffer(contract.ToolOffer{})
	start := ""
	run := &adapter.Run{Prompt: "Run the job.", Wire: "openai-compatible", ModelName: "fixture", Thinking: "off", Critic: "off", AllowConversation: true, Gate: g.Calls(), HostTools: tools,
		Context: &runcontext.Input{RequestCap: with.requestCap, Authorize: with.authorize, CurrentRequest: &runcontext.CurrentRequest{Message: "Run the job.", Input: "job"}, RunStore: &runcontext.DurableRunInput{Store: store, ID: jobRun, Resume: with.resume}, Approval: with.decision, JobContinuation: with.continuation, Plan: with.plan, Phase: phaseOf(with.plan)},
		Emit: func(event contract.RunEvent) {
			mu.Lock()
			out.events = append(out.events, event)
			mu.Unlock()
		},
		EnrollContext: func(_ context.Context, offer adapter.ContextOffer) error {
			for _, held := range offer.HostTools {
				if held.Taxonomy.Operation == "run" && !strings.HasSuffix(held.Name, "cancel_job") {
					start = held.Name
				}
			}
			return g.EnrollContext(offer.ID, offer.HostTools)
		}}
	wireOf := func(call jobCall) string {
		if call.tool == "start" {
			return start
		}
		return call.tool
	}
	argsOf := func(call jobCall) string {
		if with.args != nil {
			return with.args(call)
		}
		return call.args
	}
	var implementation adapter.Adapter
	if engine == "native" {
		implementation = native.New()
		run.Agent = &adapter.Agent{Model: "fixture", Run: func(ctx context.Context, request adapter.AgentRequest, cb adapter.AgentCallbacks) error {
			if cb.ProviderRelease != nil {
				defer cb.ProviderRelease.Acknowledge(cb.ProviderRelease.Invocation)
			}
			out.asks.Add(1)
			mu.Lock()
			out.bodies = append(out.bodies, request.Prompt)
			mu.Unlock()
			for i, call := range calls {
				name := ""
				for _, held := range request.Tools {
					if strings.HasPrefix(held.Description, wireOf(call)+": ") {
						name = held.Name
					}
				}
				answer, err := cb.Tool(ctx, adapter.AgentToolCall{ID: fmt.Sprintf("native-call-%d", i), Name: name, Arguments: json.RawMessage(argsOf(call))})
				raw, _ := json.Marshal(answer)
				mu.Lock()
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
				return modelCall(wireOf(calls[seen-1]), fmt.Sprintf("model-call-%d-%d", time.Now().UnixNano(), seen), argsOf(calls[seen-1])), nil
			}
			return modelFinal(), nil
		}
	}
	ctx := with.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	out.err = implementation.Run(ctx, run)
	return out
}

type bookRow struct {
	ID            string          `json:"id"`
	Kind          string          `json:"kind"`
	Tool          string          `json:"tool"`
	State         string          `json:"state"`
	Request       string          `json:"request"`
	RequestDigest string          `json:"requestDigest"`
	Result        json.RawMessage `json:"result"`
	Failure       map[string]any  `json:"failure"`
}
type bookObservation struct {
	DispatchID string `json:"dispatchId"`
}
type book struct {
	Dispatches   []bookRow         `json:"dispatches"`
	Observations []bookObservation `json:"observations"`
}

func headBook(t *testing.T, store runcontext.RunStore) (book, string) {
	t.Helper()
	head, err := store.Load(context.Background(), jobRun)
	if err != nil || head == nil {
		t.Fatal("no head", err)
	}
	var checkpoint struct {
		Durable book `json:"durable"`
	}
	if err := json.Unmarshal(head.Checkpoint, &checkpoint); err != nil {
		t.Fatal(err)
	}
	return checkpoint.Durable, string(head.Checkpoint)
}
func (b book) row(id string) bookRow {
	for _, row := range b.Dispatches {
		if row.ID == id {
			return row
		}
	}
	return bookRow{}
}
func (b book) hosts() []bookRow {
	out := []bookRow{}
	for _, row := range b.Dispatches {
		if row.Kind == "host" {
			out = append(out, row)
		}
	}
	return out
}
func onlyJob(t *testing.T, store runcontext.RunStore) runcontext.RunJob {
	t.Helper()
	page, err := runcontext.ListJobs(context.Background(), store, jobRun, "", runcontext.JobPageLimit)
	if err != nil || len(page.Jobs) != 1 {
		t.Fatal(page.Jobs, err)
	}
	return page.Jobs[0]
}
func refusedAs(err error, reason string) bool {
	var refused *runcontext.JobRefused
	return errors.As(err, &refused) && refused.Reason == reason
}

// lastToolResult is the last tool result's text: a request body also holds what earlier turns were told.
func lastToolResult(t *testing.T, result *runResult) string {
	t.Helper()
	for i := len(result.events) - 1; i >= 0; i-- {
		raw, err := json.Marshal(result.events[i])
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), `"type":"tool_result"`) {
			return string(raw)
		}
	}
	return ""
}

func toolAnswer(result *runResult) string {
	raw, _ := json.Marshal(result.events)
	return string(raw) + strings.Join(result.bodies, "\n")
}
func completion(job string, over func(*runcontext.JobCompletionEvidence)) runcontext.JobCompletionEvidence {
	out := runcontext.JobCompletionEvidence{Version: 1, Resource: jobRun, Job: job, Generation: 1, Evidence: "done-1", Outcome: "succeeded", Key: jobKey}
	if over != nil {
		over(&out)
	}
	return out
}

// spyHost records what a job's record says when its effect host is asked.
type spyHost struct {
	inner *runcontext.ReferenceEffectHost
	store runcontext.RunStore
	seen  []string
}

func (s *spyHost) EffectFencing() string { return s.inner.EffectFencing() }
func (s *spyHost) Dispatch(ctx context.Context, admission runcontext.EffectAdmission, perform func(context.Context) error) error {
	held, err := runcontext.ReadJob(ctx, s.store, jobRun, runcontext.JobIdentity(jobRun, admission.Operation.Operation))
	if err != nil || held == nil {
		s.seen = append(s.seen, "absent")
	} else {
		s.seen = append(s.seen, held.State)
	}
	return s.inner.Dispatch(ctx, admission, perform)
}

func TestJobsAStartIsRegisteredWithItsClaimBeforeItIsAdmitted(t *testing.T) {
	for _, engine := range engines {
		t.Run(engine, func(t *testing.T) {
			l := newLeased(t, nil)
			spy := &spyHost{store: l.store}
			certified, err := runcontext.CertifyEffectHost(context.Background(), func(authority runcontext.LeaseAuthority) runcontext.EffectHost {
				spy.inner = runcontext.NewReferenceEffectHost(authority)
				return spy
			}, l.store)
			if err != nil {
				t.Fatal(err)
			}
			spy.seen = nil
			view, err := runcontext.FencedRunStore(l.store, l.fence, certified)
			if err != nil {
				t.Fatal(err)
			}
			calls := &startCalls{}
			result := drive(t, view, engine, []runcontext.HostTool{startTool(t, calls, answered(startedAnswer, nil), "allow", nil)}, []jobCall{{"start", jobArgs}}, options{})
			if result.err != nil {
				t.Fatal(result.err)
			}
			if strings.Join(spy.seen, ",") != "unproven" {
				t.Fatal(spy.seen)
			}
			job := onlyJob(t, l.store)
			if job.State != "started" || job.Handle == nil || *job.Handle != jobSecret || *job.Receipt != jobRcpt || job.Registered.Owner != "worker-a" || job.Job != calls.jobs[0] || job.Job != runcontext.JobIdentity(jobRun, job.Operation) {
				t.Fatalf("%+v", job)
			}
			rows, checkpoint := headBook(t, l.store)
			row := rows.row(job.Operation)
			if row.State != "result" || row.Request != calls.requests[0] || row.RequestDigest != job.RequestDigest {
				t.Fatalf("%+v %v", row, calls.requests)
			}
			// M15: a handle and a receipt go only to the job record.
			stored, _ := l.store.ReadEvents(context.Background(), jobRun, "", 100)
			everything := toolAnswer(result) + checkpoint
			for _, item := range stored.Items {
				everything += string(item.Bytes)
			}
			if strings.Contains(everything, jobSecret) || strings.Contains(everything, jobRcpt) || !strings.Contains(everything, "started, not finished") {
				t.Fatal(everything)
			}
		})
	}
}

func TestJobsARefusedRegistrationLeavesNoRowAndTheRunGoesOn(t *testing.T) {
	for _, engine := range engines {
		t.Run(engine, func(t *testing.T) {
			l := newLeased(t, nil)
			calls := &startCalls{}
			tool := startTool(t, calls, answered(startedAnswer, nil), "allow", nil)
			if result := drive(t, l.view, engine, []runcontext.HostTool{tool}, []jobCall{{"start", jobArgs}}, options{}); result.err != nil {
				t.Fatal(result.err)
			}
			before, _ := headBook(t, l.store)
			again := drive(t, l.view, engine, []runcontext.HostTool{tool}, []jobCall{{"start", `{"name":"other"}`}}, options{resume: true})
			if calls.count() != 1 || !strings.Contains(toolAnswer(again), "JobRefused: key-reused") {
				t.Fatal(calls.count(), toolAnswer(again), again.err)
			}
			after, _ := headBook(t, l.store)
			if len(after.hosts()) != len(before.hosts()) {
				t.Fatal(after.hosts())
			}
			onlyJob(t, l.store)
		})
	}
}

func TestJobsAKeyTheHostCannotMintLeavesNoRowAndRunsNothing(t *testing.T) {
	for _, engine := range engines {
		for name, mint := range map[string]func(context.Context, string) (string, error){
			"it fails":     func(context.Context, string) (string, error) { return "", errors.New("no key") },
			"another form": func(context.Context, string) (string, error) { return "not-a-digest", nil },
		} {
			t.Run(engine+"/"+name, func(t *testing.T) {
				l := newLeased(t, nil)
				calls := &startCalls{}
				// The writer checks it itself: the store is never handed it.
				counting := countingStore{durableJobStore: l.store, calls: &atomic.Int32{}, registered: &atomic.Int32{}}
				view, err := runcontext.FencedRunStore(counting, l.fence, runcontext.NewReferenceEffectHost(counting))
				if err != nil {
					t.Fatal(err)
				}
				result := drive(t, view, engine, []runcontext.HostTool{startTool(t, calls, answered(startedAnswer, nil), "allow", mint)}, []jobCall{{"start", jobArgs}}, options{})
				if calls.count() != 0 || counting.registered.Load() != 0 || !strings.Contains(toolAnswer(result), "JobRefused: invalid") {
					t.Fatal(calls.count(), counting.registered.Load(), toolAnswer(result))
				}
				rows, _ := headBook(t, l.store)
				if len(rows.hosts()) != 0 {
					t.Fatal(rows.hosts())
				}
			})
		}
	}
}

func TestJobsAThrowLeavesTheJobUnprovenAndARepeatIsAnsweredFromTheJob(t *testing.T) {
	for _, engine := range engines {
		t.Run(engine, func(t *testing.T) {
			l := newLeased(t, nil)
			calls := &startCalls{}
			tool := startTool(t, calls, answered(runcontext.JobStartAnswer{}, errors.New("lost")), "allow", nil)
			drive(t, l.view, engine, []runcontext.HostTool{tool}, []jobCall{{"start", jobArgs}}, options{})
			job := onlyJob(t, l.store)
			if job.State != "unproven" || job.Handle != nil {
				t.Fatalf("%+v", job)
			}
			rows, _ := headBook(t, l.store)
			if rows.row(job.Operation).State != "unknown" {
				t.Fatalf("%+v", rows.row(job.Operation))
			}
			repeat := drive(t, l.view, engine, []runcontext.HostTool{tool}, []jobCall{{"start", jobArgs}}, options{resume: true})
			if calls.count() != 1 || !strings.Contains(toolAnswer(repeat), "not proven to have started") {
				t.Fatal(calls.count(), toolAnswer(repeat))
			}
			// With no grant the repeat asks the store for no re-claim.
			if held := onlyJob(t, l.store); held.Reclaimed != nil {
				t.Fatalf("%+v", held)
			}
		})
	}
}

func TestJobsAnAnswerThatIsNotTypedLeavesTheJobUnproven(t *testing.T) {
	for _, engine := range engines {
		t.Run(engine, func(t *testing.T) {
			for _, answer := range []runcontext.JobStartAnswer{{Started: true}, {Handle: "a handle on a refusal"}} {
				l := newLeased(t, nil)
				tool := startTool(t, &startCalls{}, answered(answer, nil), "allow", nil)
				drive(t, l.view, engine, []runcontext.HostTool{tool}, []jobCall{{"start", jobArgs}}, options{})
				if job := onlyJob(t, l.store); job.State != "unproven" {
					t.Fatalf("%+v", job)
				}
			}
		})
	}
}

func TestJobsATypedNotStartedClosesTheJob(t *testing.T) {
	for _, engine := range engines {
		t.Run(engine, func(t *testing.T) {
			l := newLeased(t, nil)
			tool := startTool(t, &startCalls{}, answered(runcontext.JobStartAnswer{}, nil), "allow", nil)
			result := drive(t, l.view, engine, []runcontext.HostTool{tool}, []jobCall{{"start", jobArgs}}, options{})
			job := onlyJob(t, l.store)
			if job.State != "not-started" || job.Closed == nil || job.Closed.By != "answer" || !strings.Contains(toolAnswer(result), "it did not start") {
				t.Fatalf("%+v %s", job, toolAnswer(result))
			}
		})
	}
}

func TestJobsALateAnswerIsKept(t *testing.T) {
	for _, engine := range engines {
		t.Run(engine, func(t *testing.T) {
			l := newLeased(t, nil)
			tool := startTool(t, &startCalls{}, func(context.Context, runcontext.JobStartInput) (runcontext.JobStartAnswer, error) {
				l.clock.move(2 * time.Hour)
				return startedAnswer, nil
			}, "allow", nil)
			drive(t, l.view, engine, []runcontext.HostTool{tool}, []jobCall{{"start", jobArgs}}, options{})
			job := onlyJob(t, l.store)
			if job.State != "started" || job.Handle == nil || *job.Handle != jobSecret {
				t.Fatalf("%+v", job)
			}
			// The row could not be committed under that fence, and it is never re-sent.
			rows, _ := headBook(t, l.store)
			if rows.row(job.Operation).State == "result" {
				t.Fatalf("%+v", rows.row(job.Operation))
			}
		})
	}
}

// reauthRun parks a start a trusted credential check refused, wakes it and claims it.
func reauthRun(t *testing.T, engine string, closeFirst bool) (*startCalls, durableJobStore, runcontext.RunJob, *runResult) {
	t.Helper()
	return reauthRunWith(t, engine, func(l *leasedRun, job string) {
		if !closeFirst {
			return
		}
		if _, err := runcontext.ReconcileJob(context.Background(), l.store, runcontext.JobReconciliation{Version: 1, Resource: jobRun, Job: job, Generation: 1, Evidence: "find-1", Finding: "not-started", Key: jobKey}); err != nil {
			t.Fatal(err)
		}
	})
}

func reauthRunWith(t *testing.T, engine string, before func(*leasedRun, string)) (*startCalls, durableJobStore, runcontext.RunJob, *runResult) {
	t.Helper()
	principal := "sha256:" + strings.Repeat("7", 64)
	var refused atomic.Bool
	refused.Store(true)
	l := newLeased(t, func(context.Context, runcontext.EffectOperation) (*runcontext.CredentialRefusal, error) {
		if refused.Load() {
			return &runcontext.CredentialRefusal{Cause: "expired", Connection: "runner-conn", Principal: principal, Permissions: []string{"jobs:write"}, Requires: []string{"jobs:write"}}, nil
		}
		return nil, nil
	})
	calls := &startCalls{}
	tool := startTool(t, calls, answered(startedAnswer, nil), "allow", nil)
	drive(t, l.view, engine, []runcontext.HostTool{tool}, []jobCall{{"start", jobArgs}}, options{})
	if calls.count() != 0 {
		t.Fatal(calls.count())
	}
	job := onlyJob(t, l.store)
	ctx := context.Background()
	digest, _ := runcontext.WakeKeyDigest(jobWake)
	model := runcontext.PinnedModel{Provider: "openai", Model: "fixture"}
	if _, _, err := runcontext.ParkRun(ctx, runcontext.ParkInput{Store: l.store, Fence: l.fence, Wait: "wait-r", Continuation: "k", Condition: runcontext.WaitCondition{Event: &contract.RunWaitEventCondition{Kind: "reauthorized", Subject: "runner-conn"}}, Model: model, WakeKeyDigest: digest, Release: []runcontext.WaitRelease{}, Reauthorization: runcontext.ReauthorizationOf(l.view)}); err != nil {
		t.Fatal(err)
	}
	l.clock.move(time.Minute)
	if _, err := runcontext.WakeRun(ctx, l.store, runcontext.WakeEvidence{Version: 1, Resource: jobRun, Wait: "wait-r", Evidence: "refreshed", Kind: "event", Event: &contract.RunWaitEventCondition{Kind: "reauthorized", Subject: "runner-conn"}, Key: jobWake}); err != nil {
		t.Fatal(err)
	}
	at := l.clock.now().UTC().Format("2006-01-02T15:04:05.000Z")
	l.clock.move(time.Minute)
	resumed, err := runcontext.ResumeParked(ctx, runcontext.ResumeInput{Store: l.store, Resource: jobRun, Wait: "wait-r", Evidence: "refreshed", Owner: "worker-b", DurationMs: jobHour, Current: currentEvidence(t, l.store, "wait-r"), Reauthorization: &runcontext.ReauthAction{Version: 1, Resource: jobRun, Wait: "wait-r", Connection: "runner-conn", Action: "refresh", Principal: principal, Permissions: []string{"jobs:write"}, Actor: "person-owner", At: at}})
	if err != nil {
		t.Fatal(err)
	}
	refused.Store(false)
	before(l, job.Job)
	view, err := runcontext.ReauthorizedRunStore(ctx, l.store, l.host, resumed)
	if err != nil {
		t.Fatal(err)
	}
	return calls, l.store, job, drive(t, view, engine, []runcontext.HostTool{tool}, []jobCall{{"start", jobArgs}}, options{resume: true})
}
func currentEvidence(t *testing.T, store runcontext.RunStore, wait string) *runcontext.ResumeEvidence {
	t.Helper()
	held, err := store.(durableJobStore).ReadWait(context.Background(), jobRun, wait)
	if err != nil || held == nil {
		t.Fatal(held, err)
	}
	pins := held.Pins
	raw, _ := json.Marshal(map[string]any{"version": 1, "resource": jobRun, "wait": wait, "model": map[string]any{"provider": pins.Model.Provider, "model": pins.Model.Model, "status": "available"}, "policy": map[string]any{"digest": pins.Policy}, "sources": pins.Sources, "artifacts": pins.Artifacts, "plan": pins.Plan})
	var fields map[string]any
	_ = json.Unmarshal(raw, &fields)
	if fields["plan"] == nil {
		delete(fields, "plan")
	}
	raw, _ = json.Marshal(fields)
	evidence, err := runcontext.ReadResumeEvidence(raw)
	if err != nil {
		t.Fatal(err)
	}
	return &evidence
}

func TestJobsAGrantResendsAStartOnlyWhileItsJobIsUnproven(t *testing.T) {
	for _, engine := range engines {
		t.Run(engine+"/unproven", func(t *testing.T) {
			calls, store, job, result := reauthRun(t, engine, false)
			if calls.count() != 1 || result.err != nil {
				t.Fatal(calls.count(), result.err)
			}
			held, _ := runcontext.ReadJob(context.Background(), store, jobRun, job.Job)
			if held.State != "started" || held.Reclaimed == nil || held.Reclaimed.Owner != "worker-b" || *held.Handle != jobSecret {
				t.Fatalf("%+v", held)
			}
			rows, _ := headBook(t, store)
			if rows.row(job.Operation).State != "result" {
				t.Fatalf("%+v", rows.row(job.Operation))
			}
		})
		t.Run(engine+"/closed", func(t *testing.T) {
			calls, store, job, result := reauthRun(t, engine, true)
			if calls.count() != 0 || strings.Contains(toolAnswer(result), "JobRefused") || !strings.Contains(toolAnswer(result), "it did not start") {
				t.Fatal(calls.count(), toolAnswer(result))
			}
			held, _ := runcontext.ReadJob(context.Background(), store, jobRun, job.Job)
			if held.State != "not-started" || held.Reclaimed != nil {
				t.Fatalf("%+v", held)
			}
			rows, _ := headBook(t, store)
			row := rows.row(job.Operation)
			if row.State != "unknown" || row.Failure["code"] != "authentication" || row.Failure["outcome"] != "not-started" {
				t.Fatalf("%+v", row)
			}
		})
	}
}

func parkJob(t *testing.T, l *leasedRun, job string, over func(*runcontext.ParkInput)) error {
	t.Helper()
	digest, _ := runcontext.WakeKeyDigest(jobWake)
	input := runcontext.ParkInput{Store: l.store, Fence: l.fence, Wait: "wait-j", Continuation: "k", Job: job, Condition: runcontext.WaitCondition{Event: &contract.RunWaitEventCondition{Kind: runcontext.JobWaitEvent, Subject: job}}, Model: runcontext.PinnedModel{Provider: "openai", Model: "fixture"}, WakeKeyDigest: digest, Release: []runcontext.WaitRelease{}}
	if over != nil {
		over(&input)
	}
	_, _, err := runcontext.ParkRun(context.Background(), input)
	return err
}

func TestJobsAWaitIsWokenByTheCompletionAndTheResumeSaysWhy(t *testing.T) {
	for _, engine := range engines {
		t.Run(engine, func(t *testing.T) {
			ctx := context.Background()
			l := newLeased(t, nil)
			tool := startTool(t, &startCalls{}, answered(startedAnswer, nil), "allow", nil)
			read, err := runcontext.JobReadTool(l.view)
			if err != nil {
				t.Fatal(err)
			}
			if result := drive(t, l.view, engine, []runcontext.HostTool{tool, read}, []jobCall{{"start", jobArgs}}, options{}); result.err != nil {
				t.Fatal(result.err)
			}
			job := onlyJob(t, l.store)
			if err := parkJob(t, l, job.Job, nil); err != nil {
				t.Fatal(err)
			}
			if _, err := l.store.AcquireLease(ctx, runcontext.LeaseAcquire{Resource: jobRun, Owner: "worker-x", DurationMs: jobHour}); err == nil {
				t.Fatal("a parked run was leased")
			}
			l.clock.move(time.Minute)
			done, err := runcontext.CompleteJob(ctx, l.store, completion(job.Job, nil))
			if err != nil {
				t.Fatal(err)
			}
			waited, _ := l.store.ReadWait(ctx, jobRun, "wait-j")
			if waited.State != "woken" || waited.Disposition.Evidence != "done-1" {
				t.Fatalf("%+v", waited)
			}
			// A decision is not a job's to take.
			yes := &contract.CheckpointApprovalDecision{CallID: "x", Approved: true}
			var wait *runcontext.WaitRefused
			if _, err := runcontext.ResumeParked(ctx, runcontext.ResumeInput{Store: l.store, Resource: jobRun, Wait: "wait-j", Evidence: "done-1", Owner: "worker-b", DurationMs: jobHour, Current: currentEvidence(t, l.store, "wait-j"), Approval: yes}); !errors.As(err, &wait) || wait.Reason != "decision-required" {
				t.Fatal(err)
			}
			resumed, err := runcontext.ResumeParked(ctx, runcontext.ResumeInput{Store: l.store, Resource: jobRun, Wait: "wait-j", Evidence: "done-1", Owner: "worker-b", DurationMs: jobHour, Current: currentEvidence(t, l.store, "wait-j")})
			if err != nil {
				t.Fatal(err)
			}
			if resumed.Job == nil || resumed.Job.State != done.State {
				t.Fatalf("%+v", resumed.Job)
			}
			view, err := runcontext.UnattendedRunStore(l.store, resumed.Fence, l.host)
			if err != nil {
				t.Fatal(err)
			}
			// #330 HIGH-1: the claimed wait is on this job: a continuation of another job of the run is not borne out.
			other := *resumed.Job
			other.Job = runcontext.JobIdentity(jobRun, "op-other")
			if mismatched := drive(t, view, engine, []runcontext.HostTool{tool}, []jobCall{{"start", jobArgs}}, options{resume: true, continuation: &other}); mismatched.err != nil || mismatched.asks.Load() != 0 || !refusedComplete(mismatched) {
				t.Fatal(mismatched.err, mismatched.asks.Load(), mismatched.events)
			}
			read, err = runcontext.JobReadTool(view)
			if err != nil {
				t.Fatal(err)
			}
			args := `{"job":"` + job.Job + `"}`
			next := drive(t, view, engine, []runcontext.HostTool{tool, read}, []jobCall{{"job_read", args}, {"job_read", args}}, options{resume: true, continuation: resumed.Job})
			if next.err != nil {
				t.Fatal(next.err)
			}
			if len(next.bodies) == 0 || !strings.Contains(next.bodies[0], "JOB RESULT:") {
				t.Fatal(next.bodies)
			}
			all := toolAnswer(next)
			if strings.Contains(all, jobSecret) || !strings.Contains(all, `\"changed\":true`) || !strings.Contains(all, `\"changed\":false`) || !strings.Contains(all, "unchanged: do not poll") {
				t.Fatal(all)
			}
			// #330 HIGH-1: the same continuation delivered again, after it ran, meets a head that has moved: refused
			// RunComplete, and the model is not asked.
			again := drive(t, view, engine, []runcontext.HostTool{tool, read}, []jobCall{{"start", jobArgs}}, options{resume: true, continuation: resumed.Job})
			if again.err != nil || again.asks.Load() != 0 || !refusedComplete(again) {
				t.Fatal(again.err, again.asks.Load(), again.events)
			}
		})
	}
}

// refusedComplete: the run was refused RunComplete (#328).
func refusedComplete(result *runResult) bool {
	for _, event := range result.events {
		if event.Error != nil && strings.HasPrefix(event.Error.Message, "RunComplete:") {
			return true
		}
	}
	return false
}

// #330 HIGH-1: a resume of a complete head that hands this run's own job as the continuation, with no wait parked and none
// claimed, is refused RunComplete before the model is asked: nothing starts again.
func TestJobsAContinuationNoClaimedWaitBearsOutIsRefusedAtAFinishedRun(t *testing.T) {
	for _, engine := range engines {
		t.Run(engine, func(t *testing.T) {
			l := newLeased(t, nil)
			calls := &startCalls{}
			tool := startTool(t, calls, answered(startedAnswer, nil), "allow", nil)
			if result := drive(t, l.view, engine, []runcontext.HostTool{tool}, []jobCall{{"start", jobArgs}}, options{}); result.err != nil {
				t.Fatal(result.err)
			}
			job := onlyJob(t, l.store)
			second := drive(t, l.view, engine, []runcontext.HostTool{tool}, []jobCall{{"start", `{"name":"again"}`}}, options{resume: true, continuation: &job})
			page, _ := runcontext.ListJobs(context.Background(), l.store, jobRun, "", runcontext.JobPageLimit)
			if second.err != nil || second.asks.Load() != 0 || !refusedComplete(second) || calls.count() != 1 || len(page.Jobs) != 1 {
				t.Fatal(second.err, second.asks.Load(), calls.count(), len(page.Jobs), second.events)
			}
			// A wait parked on that job and not claimed bears nothing out either.
			if err := parkJob(t, l, job.Job, nil); err != nil {
				t.Fatal(err)
			}
			third := drive(t, l.view, engine, []runcontext.HostTool{tool}, []jobCall{{"start", `{"name":"again"}`}}, options{resume: true, continuation: &job})
			if third.err != nil || third.asks.Load() != 0 || !refusedComplete(third) || calls.count() != 1 {
				t.Fatal(third.err, third.asks.Load(), calls.count(), third.events)
			}
		})
	}
}

func TestJobsAParkRefusesASettledOrUnprovenJobOrAPendingDecision(t *testing.T) {
	engine := "eino"
	settled := newLeased(t, nil)
	drive(t, settled.view, engine, []runcontext.HostTool{startTool(t, &startCalls{}, answered(startedAnswer, nil), "allow", nil)}, []jobCall{{"start", jobArgs}}, options{})
	job := onlyJob(t, settled.store)
	if _, err := runcontext.CompleteJob(context.Background(), settled.store, completion(job.Job, nil)); err != nil {
		t.Fatal(err)
	}
	if err := parkJob(t, settled, job.Job, nil); !refusedAs(err, "job-settled") {
		t.Fatal(err)
	}
	if err := parkJob(t, settled, runcontext.JobIdentity(jobRun, "no-such"), nil); !refusedAs(err, "no-job") {
		t.Fatal(err)
	}
	unproven := newLeased(t, nil)
	drive(t, unproven.view, engine, []runcontext.HostTool{startTool(t, &startCalls{}, answered(runcontext.JobStartAnswer{}, errors.New("lost")), "allow", nil)}, []jobCall{{"start", jobArgs}}, options{})
	if err := parkJob(t, unproven, onlyJob(t, unproven.store).Job, nil); !refusedAs(err, "job-unproven") {
		t.Fatal(err)
	}
	asked := newLeased(t, nil)
	drive(t, asked.view, engine, []runcontext.HostTool{startTool(t, &startCalls{}, answered(startedAnswer, nil), "ask", nil)}, []jobCall{{"start", jobArgs}}, options{})
	var wait *runcontext.WaitRefused
	if err := parkJob(t, asked, runcontext.JobIdentity(jobRun, "unused"), nil); !errors.As(err, &wait) || wait.Reason != "decision-required" {
		t.Fatal(err)
	}
}

// parkSpy counts what reaches the store's own park; lying answers a read with another run's job.
type parkSpy struct {
	durableJobStore
	parks *atomic.Int32
}

func (s parkSpy) ParkWait(ctx context.Context, park runcontext.RunWait, fence runcontext.LeaseFence) (runcontext.RunWait, runcontext.AttentionEvent, error) {
	s.parks.Add(1)
	return s.durableJobStore.ParkWait(ctx, park, fence)
}

type lying struct {
	durableJobStore
	job *runcontext.RunJob
}

func (s lying) ReadJob(context.Context, string, string) (*runcontext.RunJob, error) {
	return s.job, nil
}

func TestJobsAParkReadsTheJobItself(t *testing.T) {
	l := newLeased(t, nil)
	drive(t, l.view, "eino", []runcontext.HostTool{startTool(t, &startCalls{}, answered(startedAnswer, nil), "allow", nil)}, []jobCall{{"start", jobArgs}}, options{})
	job := onlyJob(t, l.store)
	if _, err := runcontext.CompleteJob(context.Background(), l.store, completion(job.Job, nil)); err != nil {
		t.Fatal(err)
	}
	spy := parkSpy{durableJobStore: l.store, parks: &atomic.Int32{}}
	if err := parkJob(t, l, job.Job, func(input *runcontext.ParkInput) { input.Store = spy }); !refusedAs(err, "job-settled") || spy.parks.Load() != 0 {
		t.Fatal(err, spy.parks.Load())
	}
}

func TestJobsAResumeReadsTheJobThroughTheReader(t *testing.T) {
	ctx := context.Background()
	l := newLeased(t, nil)
	drive(t, l.view, "eino", []runcontext.HostTool{startTool(t, &startCalls{}, answered(startedAnswer, nil), "allow", nil)}, []jobCall{{"start", jobArgs}}, options{})
	job := onlyJob(t, l.store)
	if err := parkJob(t, l, job.Job, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := runcontext.CompleteJob(ctx, l.store, completion(job.Job, nil)); err != nil {
		t.Fatal(err)
	}
	foreign := job
	foreign.Resource, foreign.Job = "run-other", runcontext.JobIdentity("run-other", job.Operation)
	_, err := runcontext.ResumeParked(ctx, runcontext.ResumeInput{Store: lying{l.store, &foreign}, Resource: jobRun, Wait: "wait-j", Evidence: "done-1", Owner: "worker-b", DurationMs: jobHour, Current: currentEvidence(t, l.store, "wait-j")})
	if !refusedAs(err, "invalid") {
		t.Fatal(err)
	}
}

// stoppedHead answers its head's checkpoint as stopped.
type stoppedHead struct{ durableJobStore }

func (s stoppedHead) Load(ctx context.Context, id string) (*runcontext.RunHead, error) {
	head, err := s.durableJobStore.Load(ctx, id)
	if err != nil || head == nil {
		return head, err
	}
	var checkpoint map[string]json.RawMessage
	if err := json.Unmarshal(head.Checkpoint, &checkpoint); err != nil {
		return nil, err
	}
	checkpoint["status"] = json.RawMessage(`"stopped"`)
	head.Checkpoint, err = json.Marshal(checkpoint)
	return head, err
}

func TestJobsAStoppedCheckpointHasNothingToContinue(t *testing.T) {
	l := newLeased(t, nil)
	drive(t, l.view, "eino", []runcontext.HostTool{startTool(t, &startCalls{}, answered(startedAnswer, nil), "allow", nil)}, []jobCall{{"start", jobArgs}}, options{})
	job := onlyJob(t, l.store)
	var wait *runcontext.WaitRefused
	if err := parkJob(t, l, job.Job, func(input *runcontext.ParkInput) { input.Store = stoppedHead{l.store} }); !errors.As(err, &wait) || wait.Reason != "nothing-pending" {
		t.Fatal(err)
	}
}

func TestJobsAContinuationMustBeThisRunsJob(t *testing.T) {
	l := newLeased(t, nil)
	tool := startTool(t, &startCalls{}, answered(startedAnswer, nil), "allow", nil)
	drive(t, l.view, "eino", []runcontext.HostTool{tool}, []jobCall{{"start", jobArgs}}, options{})
	foreign := onlyJob(t, l.store)
	foreign.Resource, foreign.Job = "run-other", runcontext.JobIdentity("run-other", foreign.Operation)
	// #330: at a finished head, a continuation no claimed wait bears out is RunComplete first; this reads the session's own
	// check at a head left running.
	interrupted(t, l.view)
	result := drive(t, l.view, "eino", []runcontext.HostTool{tool}, nil, options{resume: true, continuation: &foreign})
	if result.asks.Load() != 0 || !strings.Contains(fmt.Sprint(result.err)+toolAnswer(result), "JobRefused: invalid") {
		t.Fatal(result.asks.Load(), result.err, toolAnswer(result))
	}
}

func TestJobsATypedAnswerIsKeptWhenTheCallsContextEnded(t *testing.T) {
	for _, engine := range engines {
		t.Run(engine, func(t *testing.T) {
			l := newLeased(t, nil)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			tool := startTool(t, &startCalls{}, func(context.Context, runcontext.JobStartInput) (runcontext.JobStartAnswer, error) {
				cancel()
				return startedAnswer, nil
			}, "allow", nil)
			drive(t, l.view, engine, []runcontext.HostTool{tool}, []jobCall{{"start", jobArgs}}, options{ctx: ctx})
			job := onlyJob(t, l.store)
			if job.State != "started" || job.Handle == nil || *job.Handle != jobSecret {
				t.Fatalf("%+v", job)
			}
		})
	}
}

func TestJobsACloseReadsTheJobAfresh(t *testing.T) {
	for name, c := range map[string]struct {
		outcome, want, not string
	}{
		"a job that failed since the run opened closes nothing":     {"failed", "unsuccessful-operation", "recorded evidence"},
		"a job that succeeded since the run opened closes its step": {"succeeded", `\"status\":\"completed\"`, "unsuccessful-operation"},
	} {
		t.Run(name, func(t *testing.T) { closesAfresh(t, c.outcome, c.want, c.not) })
	}
}

func closesAfresh(t *testing.T, outcome, want, not string) {
	t.Helper()
	ctx := context.Background()
	l := newLeased(t, nil)
	tool := startTool(t, &startCalls{}, answered(startedAnswer, nil), "allow", nil)
	drive(t, l.view, "eino", []runcontext.HostTool{tool}, []jobCall{{"start", jobArgs}}, options{})
	job := onlyJob(t, l.store)
	revision := "sha256:" + runcontext.ContextDigest(`[{"id":"step-1","status":"in_progress","step":"Run it"}]`)
	calls := []jobCall{{"update_plan", `{"plan":[{"step":"Run it","status":"in_progress"}],"explanation":"start"}`}, {"complete_plan_step", `{"id":"step-1","expectedRevision":"` + revision + `","outcome":"completed","evidence":{"operations":["` + job.Operation + `"],"artifacts":[]}}`}}
	// The job settles while the run is open: the close must read that, not what the run opened with.
	counting := &failsOnFirstList{durableJobStore: l.store, fail: func() {
		if _, err := runcontext.CompleteJob(ctx, l.store, completion(job.Job, func(e *runcontext.JobCompletionEvidence) { e.Outcome = outcome })); err != nil {
			t.Fatal(err)
		}
	}, seen: &atomic.Int32{}}
	view, err := runcontext.FencedRunStore(counting, l.fence, runcontext.NewReferenceEffectHost(counting))
	if err != nil {
		t.Fatal(err)
	}
	result := drive(t, view, "eino", []runcontext.HostTool{tool}, calls, options{resume: true})
	if !strings.Contains(toolAnswer(result), want) || strings.Contains(toolAnswer(result), not) {
		t.Fatal(toolAnswer(result), result.err)
	}
}

// failsOnFirstList lets the job fail right after the run's first read of its jobs.
type failsOnFirstList struct {
	durableJobStore
	fail func()
	seen *atomic.Int32
}

func (s failsOnFirstList) ListJobs(ctx context.Context, resource, after string, limit int) (runcontext.JobPage, error) {
	page, err := s.durableJobStore.ListJobs(ctx, resource, after, limit)
	if s.seen.Add(1) == 1 {
		s.fail()
	}
	return page, err
}

func TestJobsAHeldStartGoesThroughTheOrdinaryApproval(t *testing.T) {
	for _, engine := range engines {
		t.Run(engine, func(t *testing.T) {
			l := newLeased(t, nil)
			calls := &startCalls{}
			tool := startTool(t, calls, answered(startedAnswer, nil), "ask", nil)
			first := drive(t, l.view, engine, []runcontext.HostTool{tool}, []jobCall{{"start", jobArgs}}, options{})
			callID := ""
			for _, event := range first.events {
				if event.ApprovalRequested != nil {
					callID = event.ApprovalRequested.CallID
				}
			}
			if callID == "" || calls.count() != 0 {
				t.Fatal(callID, calls.count(), first.err)
			}
			if page, _ := runcontext.ListJobs(context.Background(), l.store, jobRun, "", 10); len(page.Jobs) != 0 {
				t.Fatal(page.Jobs)
			}
			// ApprovalNotShown: no digest, or another one, is refused before the claim; the pause stands and nothing
			// has run.
			wrong := "sha256:" + strings.Repeat("0", 64)
			for _, decision := range []*contract.CheckpointApprovalDecision{{CallID: callID, Approved: true}, {CallID: callID, Approved: true, ShownDigest: &wrong}} {
				refused := drive(t, l.view, engine, []runcontext.HostTool{tool}, nil, options{resume: true, decision: decision})
				if calls.count() != 0 || !strings.Contains(fmt.Sprint(refused.err)+toolAnswer(refused), "ApprovalNotShown") {
					t.Fatal(calls.count(), refused.err, toolAnswer(refused))
				}
			}
			proof := heldProof(t, l.store, callID)
			next := drive(t, l.view, engine, []runcontext.HostTool{tool}, nil, options{resume: true, decision: proof})
			if next.err != nil || calls.count() != 1 {
				t.Fatal(next.err, calls.count())
			}
			job := onlyJob(t, l.store)
			rows, _ := headBook(t, l.store)
			if job.State != "started" || rows.row(job.Operation).Request != calls.requests[0] {
				t.Fatalf("%+v %+v", job, rows.row(job.Operation))
			}
			// What was shown is what the row holds: the digest the person's decision proved is the job's.
			if job.RequestDigest != *proof.ShownDigest {
				t.Fatal(job.RequestDigest, *proof.ShownDigest)
			}
		})
	}
}

// heldProof is a decision on a held start: the digest of what the person was shown, read from the stored pause.
func heldProof(t *testing.T, store runcontext.RunStore, callID string) *contract.CheckpointApprovalDecision {
	t.Helper()
	head, err := store.Load(context.Background(), jobRun)
	if err != nil || head == nil {
		t.Fatal(head, err)
	}
	var saved struct {
		Approval contract.CheckpointApproval `json:"approval"`
	}
	if err := json.Unmarshal(head.Checkpoint, &saved); err != nil {
		t.Fatal(err)
	}
	digest := runcontext.ApprovalShownDigest(saved.Approval)
	return &contract.CheckpointApprovalDecision{CallID: callID, Approved: true, ShownDigest: &digest}
}

func TestJobsTheRowsOfJobToolsAreNeverPlanEvidence(t *testing.T) {
	for _, engine := range engines {
		t.Run(engine, func(t *testing.T) {
			l := newLeased(t, nil)
			tool := startTool(t, &startCalls{}, answered(startedAnswer, nil), "allow", nil)
			drive(t, l.view, engine, []runcontext.HostTool{tool}, []jobCall{{"start", jobArgs}}, options{})
			job := onlyJob(t, l.store)
			read, err := runcontext.JobReadTool(l.view)
			if err != nil {
				t.Fatal(err)
			}
			result := drive(t, l.view, engine, []runcontext.HostTool{tool, read}, []jobCall{{"job_read", `{"job":"` + job.Job + `"}`}}, options{resume: true})
			if result.err != nil {
				t.Fatal(result.err)
			}
			rows, _ := headBook(t, l.store)
			reads := 0
			for _, row := range rows.Dispatches {
				if strings.HasSuffix(row.Tool, "job_read") {
					reads++
					if row.State != "result" {
						t.Fatalf("%+v", row)
					}
					for _, observed := range rows.Observations {
						if observed.DispatchID == row.ID {
							t.Fatalf("a read is evidence: %+v", observed)
						}
					}
				}
			}
			if reads != 1 {
				t.Fatal(reads)
			}
			for _, observed := range rows.Observations {
				if observed.DispatchID == job.Operation {
					t.Fatalf("a start is evidence: %+v", observed)
				}
			}
		})
	}
}

func TestJobsAJobOfAnotherRunAnswersNoJob(t *testing.T) {
	l := newLeased(t, nil)
	read, err := runcontext.JobReadTool(l.view)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, args := range []string{`{"job":"` + runcontext.JobIdentity("run-other", "op-1") + `"}`, `{"job":"job-1"}`, `{"JOB":"` + runcontext.JobIdentity(jobRun, "op-1") + `"}`, `{"job":"a","job":"b"}`} {
		if _, err := read.Execute(ctx, json.RawMessage(args)); !refusedAs(err, "no-job") {
			t.Fatal(args, err)
		}
	}
}

// startFailure is the failure a start's row holds once it ran and its answer could not be recorded.
func startFailure(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile("../../../contract/vectors/job/model.json")
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		StartFailure map[string]any `json:"startFailure"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	return file.StartFailure
}

func reconcileStarted(t *testing.T, store runcontext.RunStore, job, handle string) {
	t.Helper()
	if _, err := runcontext.ReconcileJob(context.Background(), store, runcontext.JobReconciliation{Version: 1, Resource: jobRun, Job: job, Generation: 1, Evidence: "find-1", Finding: "started", Handle: &handle, Key: jobKey}); err != nil {
		t.Fatal(err)
	}
}

func TestJobsAStartThatRanIsToldWhatItsJobSays(t *testing.T) {
	const hostText = "SENTINEL-HOST-TEXT: created job H1 then failed"
	for name, c := range map[string]struct {
		start     func(*testing.T, *leasedRun, runcontext.JobStartInput) (runcontext.JobStartAnswer, error)
		state, in string
	}{
		"an answer that is not typed": {func(*testing.T, *leasedRun, runcontext.JobStartInput) (runcontext.JobStartAnswer, error) {
			return runcontext.JobStartAnswer{Started: true}, nil
		}, "unproven", "not proven to have started"},
		"a start that fails with the host's words": {func(*testing.T, *leasedRun, runcontext.JobStartInput) (runcontext.JobStartAnswer, error) {
			return runcontext.JobStartAnswer{}, failure.Authored("tool-failed", hostText, "tool")
		}, "unproven", "not proven to have started"},
		"a handle that conflicts with a reconciliation": {func(t *testing.T, l *leasedRun, input runcontext.JobStartInput) (runcontext.JobStartAnswer, error) {
			reconcileStarted(t, l.store, input.Job, "H1")
			return runcontext.JobStartAnswer{Started: true, Handle: "H2"}, nil
		}, "started", "started, not finished"},
		"a late not-started on a completed job": {func(t *testing.T, l *leasedRun, input runcontext.JobStartInput) (runcontext.JobStartAnswer, error) {
			if _, err := runcontext.CompleteJob(context.Background(), l.store, completion(input.Job, nil)); err != nil {
				t.Fatal(err)
			}
			return runcontext.JobStartAnswer{}, nil
		}, "completed", "completed"},
	} {
		for _, engine := range engines {
			t.Run(name+"/"+engine, func(t *testing.T) {
				l := newLeased(t, nil)
				calls := &startCalls{}
				tool := startTool(t, calls, func(_ context.Context, input runcontext.JobStartInput) (runcontext.JobStartAnswer, error) {
					return c.start(t, l, input)
				}, "allow", nil)
				result := drive(t, l.view, engine, []runcontext.HostTool{tool}, []jobCall{{"start", jobArgs}}, options{})
				job := onlyJob(t, l.store)
				rows, _ := headBook(t, l.store)
				row := rows.row(job.Operation)
				answer := toolAnswer(result)
				if calls.count() != 1 || job.State != c.state || row.State != "unknown" || !reflect.DeepEqual(row.Failure, startFailure(t)) {
					t.Fatalf("%d %+v %+v", calls.count(), job, row)
				}
				if !strings.Contains(answer, c.in) || strings.Contains(answer, "JobRefused") || strings.Contains(answer, "invalid-tool-input") || strings.Contains(answer, "SENTINEL-HOST-TEXT") {
					t.Fatal(answer)
				}
			})
		}
	}
}

// R-1: a repeat answers from the job whatever the row holds.
func TestJobsARepeatAnswersFromTheJobWhateverItsRowHolds(t *testing.T) {
	for name, c := range map[string]struct {
		answer runcontext.JobStartAnswer
		state  string
	}{"a completed job": {startedAnswer, "completed"}, "evidence after a closure": {runcontext.JobStartAnswer{}, "not-started"}} {
		for _, engine := range engines {
			t.Run(name+"/"+engine, func(t *testing.T) {
				l := newLeased(t, nil)
				calls := &startCalls{}
				tool := startTool(t, calls, answered(c.answer, nil), "allow", nil)
				drive(t, l.view, engine, []runcontext.HostTool{tool}, []jobCall{{"start", jobArgs}}, options{})
				job := onlyJob(t, l.store)
				if _, err := runcontext.CompleteJob(context.Background(), l.store, completion(job.Job, nil)); err != nil {
					t.Fatal(err)
				}
				repeat := drive(t, l.view, engine, []runcontext.HostTool{tool}, []jobCall{{"start", jobArgs}}, options{resume: true})
				job = onlyJob(t, l.store)
				answer := lastToolResult(t, repeat)
				if job.State != c.state || calls.count() != 1 || strings.Contains(answer, "nothing is running") || !strings.Contains(answer, c.state) {
					t.Fatal(job.State, calls.count(), answer)
				}
			})
		}
	}
}

func TestJobsAHandleTheRecordCannotHoldIsKeptAsItsDigest(t *testing.T) {
	for _, engine := range engines {
		t.Run(engine, func(t *testing.T) {
			l := newLeased(t, nil)
			long, receipt := strings.Repeat("h", 300), strings.Repeat("r", 300)
			tool := startTool(t, &startCalls{}, answered(runcontext.JobStartAnswer{Started: true, Handle: long, Receipt: receipt}, nil), "allow", nil)
			result := drive(t, l.view, engine, []runcontext.HostTool{tool}, []jobCall{{"start", jobArgs}}, options{})
			job := onlyJob(t, l.store)
			sum := func(text string) string { return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(text))) }
			if job.State != "started" || job.Handle == nil || *job.Handle != sum(long) || job.HandleDigested == nil || !*job.HandleDigested || job.Receipt == nil || *job.Receipt != sum(receipt) {
				t.Fatalf("%+v", job)
			}
			rows, _ := headBook(t, l.store)
			if rows.row(job.Operation).State != "result" || strings.Contains(toolAnswer(result), "hhhh") || !strings.Contains(toolAnswer(result), "started, not finished") {
				t.Fatal(rows.row(job.Operation), toolAnswer(result))
			}
		})
	}
}

func TestJobsAClosedJobTheAnswerContradictsSaysSo(t *testing.T) {
	for _, engine := range engines {
		t.Run(engine, func(t *testing.T) {
			l := newLeased(t, nil)
			tool := startTool(t, &startCalls{}, func(_ context.Context, input runcontext.JobStartInput) (runcontext.JobStartAnswer, error) {
				if _, err := runcontext.ReconcileJob(context.Background(), l.store, runcontext.JobReconciliation{Version: 1, Resource: jobRun, Job: input.Job, Generation: 1, Evidence: "find-1", Finding: "not-started", Key: jobKey}); err != nil {
					t.Fatal(err)
				}
				return startedAnswer, nil
			}, "allow", nil)
			result := drive(t, l.view, engine, []runcontext.HostTool{tool}, []jobCall{{"start", jobArgs}}, options{})
			job := onlyJob(t, l.store)
			if job.State != "not-started" || len(job.Contradictions) != 1 || !strings.Contains(toolAnswer(result), "evidence since says it ran") || strings.Contains(toolAnswer(result), "nothing is running") {
				t.Fatalf("%+v %s", job, toolAnswer(result))
			}
		})
	}
}

// A refused re-claim on a job a reconciliation proved started is answered from that job.
func TestJobsARefusedReclaimOnAStartedJobIsAnsweredFromTheJob(t *testing.T) {
	for _, engine := range engines {
		t.Run(engine, func(t *testing.T) {
			calls, store, job, result := reauthRunWith(t, engine, func(l *leasedRun, job string) { reconcileStarted(t, l.store, job, "H1") })
			held, _ := runcontext.ReadJob(context.Background(), store, jobRun, job.Job)
			if calls.count() != 0 || held.State != "started" || strings.Contains(toolAnswer(result), "JobRefused") || !strings.Contains(toolAnswer(result), "started, not finished") {
				t.Fatal(calls.count(), held.State, toolAnswer(result))
			}
		})
	}
}

// keyHost notes the idempotency key each admission carries.
type keyHost struct {
	inner *runcontext.ReferenceEffectHost
	keys  *[]string
}

func (h keyHost) EffectFencing() string { return h.inner.EffectFencing() }
func (h keyHost) Dispatch(ctx context.Context, admission runcontext.EffectAdmission, perform func(context.Context) error) error {
	*h.keys = append(*h.keys, admission.IdempotencyKey)
	return h.inner.Dispatch(ctx, admission, perform)
}

// M-9: a start reaches the gateway's members through its job: the key its effect host admitted.
func TestJobsAStartReachesTheGatewayMembersThroughItsJob(t *testing.T) {
	ctx := context.Background()
	l := newLeased(t, nil)
	keys := []string{}
	certified, err := runcontext.CertifyEffectHost(ctx, func(authority runcontext.LeaseAuthority) runcontext.EffectHost {
		return keyHost{inner: runcontext.NewReferenceEffectHost(authority), keys: &keys}
	}, l.store)
	if err != nil {
		t.Fatal(err)
	}
	keys = keys[:0]
	view, err := runcontext.FencedRunStore(l.store, l.fence, certified)
	if err != nil {
		t.Fatal(err)
	}
	var members runcontext.JobGatewayMembers
	tool := startTool(t, &startCalls{}, func(ctx context.Context, input runcontext.JobStartInput) (runcontext.JobStartAnswer, error) {
		job, err := runcontext.ReadJob(ctx, l.store, jobRun, input.Job)
		if err != nil || job == nil {
			t.Fatal(job, err)
		}
		if members, err = runcontext.GatewayActionMembers(*job, false); err != nil {
			t.Fatal(err)
		}
		return startedAnswer, nil
	}, "allow", nil)
	drive(t, view, "eino", []runcontext.HostTool{tool}, []jobCall{{"start", jobArgs}}, options{})
	if len(keys) != 1 || "sha256:"+members.ActionKey != keys[0] || members.KeyScope == nil || *members.KeyScope != "authority" {
		t.Fatalf("%+v %v", members, keys)
	}
}

// M-7: a nested run on the context a start was handed is no start itself.
func TestJobsANestedRunInAStartRegistersNothing(t *testing.T) {
	for _, engine := range engines {
		t.Run(engine, func(t *testing.T) {
			l, nested := newLeased(t, nil), newLeased(t, nil)
			var mints atomic.Int32
			mint := func(context.Context, string) (string, error) { mints.Add(1); return runcontext.JobKeyDigest(jobKey) }
			tool := startTool(t, &startCalls{}, func(ctx context.Context, _ runcontext.JobStartInput) (runcontext.JobStartAnswer, error) {
				drive(t, nested.view, engine, nil, nil, options{ctx: ctx})
				return startedAnswer, nil
			}, "allow", mint)
			drive(t, l.view, engine, []runcontext.HostTool{tool}, []jobCall{{"start", jobArgs}}, options{})
			if job := onlyJob(t, l.store); job.State != "started" || mints.Load() != 1 {
				t.Fatalf("%+v %d", job, mints.Load())
			}
			page, err := runcontext.ListJobs(context.Background(), nested.store, jobRun, "", 10)
			if err != nil || len(page.Jobs) != 0 {
				t.Fatalf("%+v %v", page, err)
			}
		})
	}
}

// declaredOnly says the fence is enforced and checks nothing.
type declaredOnly struct{}

func (declaredOnly) EffectFencing() string { return runcontext.EffectFencingEnforced }
func (declaredOnly) Dispatch(ctx context.Context, _ runcontext.EffectAdmission, perform func(context.Context) error) error {
	return perform(ctx)
}

func TestJobsAViewNotHeldToItsStoreReachesNoOfferAndNoStart(t *testing.T) {
	for name, host := range map[string]runcontext.EffectHost{"no host": nil, "a host that only declares": declaredOnly{}} {
		for _, engine := range engines {
			t.Run(name+"/"+engine, func(t *testing.T) {
				l := newLeased(t, nil)
				view, err := runcontext.FencedRunStore(l.store, l.fence, host)
				if err != nil {
					t.Fatal(err)
				}
				calls := &startCalls{}
				result := drive(t, view, engine, []runcontext.HostTool{startTool(t, calls, answered(startedAnswer, nil), "allow", nil)}, []jobCall{{"start", jobArgs}}, options{})
				page, _ := runcontext.ListJobs(context.Background(), l.store, jobRun, "", 10)
				text := toolAnswer(result)
				if result.err != nil {
					text += result.err.Error()
				}
				if calls.count() != 0 || len(page.Jobs) != 0 || result.asks.Load() != 0 || !strings.Contains(text, "effect-host-unfenced") {
					t.Fatal(calls.count(), len(page.Jobs), result.asks.Load(), text)
				}
			})
		}
	}
}

func TestJobsARunAtItsRequestLimitDoesNotPark(t *testing.T) {
	l := newLeased(t, nil)
	cap := 2
	drive(t, l.view, "eino", []runcontext.HostTool{startTool(t, &startCalls{}, answered(startedAnswer, nil), "allow", nil)}, []jobCall{{"start", jobArgs}}, options{requestCap: &cap})
	job := onlyJob(t, l.store)
	var refused *runcontext.WaitRefused
	if err := parkJob(t, l, job.Job, nil); !errors.As(err, &refused) || refused.Reason != "not-resumable" {
		t.Fatal(err)
	}
	waits, err := l.store.ExportWaits(context.Background(), jobRun)
	if err != nil || len(waits.Waits) != 0 {
		t.Fatalf("%+v %v", waits, err)
	}
}

func TestJobsAJobWithNoRowInTheHeadIsRefusedBeforeAnOffer(t *testing.T) {
	ctx := context.Background()
	for _, engine := range engines {
		t.Run(engine, func(t *testing.T) {
			l := newLeased(t, nil)
			calls := &startCalls{}
			tool := startTool(t, calls, answered(startedAnswer, nil), "allow", nil)
			drive(t, l.view, engine, []runcontext.HostTool{tool}, nil, options{})
			head, err := l.store.Load(ctx, jobRun)
			if err != nil || head == nil {
				t.Fatal(head, err)
			}
			key, _ := runcontext.JobKeyDigest(jobKey)
			if _, err := l.store.CommitFenced(ctx, runcontext.RunCommit{RunID: jobRun, ExpectedRevision: head.Revision, CommitID: "restored", Checkpoint: head.Checkpoint, Events: [][]byte{}, Job: &runcontext.JobRegistration{Job: runcontext.JobIdentity(jobRun, "op-restored-away"), Operation: "op-restored-away", Source: "runner", Tool: "start_job", RequestDigest: "sha256:" + strings.Repeat("a", 64), KeyDigest: key}}, l.fence); err != nil {
				t.Fatal(err)
			}
			result := drive(t, l.view, engine, []runcontext.HostTool{tool}, []jobCall{{"start", jobArgs}}, options{resume: true})
			text := toolAnswer(result)
			if result.err != nil {
				text += result.err.Error()
			}
			if calls.count() != 0 || result.asks.Load() != 0 || !strings.Contains(text, "JobCapabilityRefused: store-inconsistent") {
				t.Fatal(calls.count(), result.asks.Load(), text)
			}
		})
	}
}

func TestJobsAToolOfferedWithNoDurableStoreRefusesByName(t *testing.T) {
	calls := &startCalls{}
	tool := startTool(t, calls, answered(startedAnswer, nil), "allow", nil)
	var asked atomic.Int32
	events := []contract.RunEvent{}
	g := gate.New(gate.Options{})
	g.SetOffer(contract.ToolOffer{})
	run := &adapter.Run{Prompt: "Run the job.", Wire: "openai-compatible", ModelName: "fixture", Thinking: "off", Critic: "off", AllowConversation: true, Gate: g.Calls(), HostTools: []runcontext.HostTool{tool},
		Context: &runcontext.Input{CurrentRequest: &runcontext.CurrentRequest{Message: "Run the job.", Input: "job"}},
		Emit:    func(event contract.RunEvent) { events = append(events, event) },
		EnrollContext: func(_ context.Context, offer adapter.ContextOffer) error {
			return g.EnrollContext(offer.ID, offer.HostTools)
		},
		Model: func(context.Context, string, relay.Request) (*http.Response, error) {
			asked.Add(1)
			return modelFinal(), nil
		}}
	err := eino.New().Run(context.Background(), run)
	raw, _ := json.Marshal(events)
	if asked.Load() != 0 || err == nil && !strings.Contains(string(raw), "JobCapabilityRefused: store-unsupported") || err != nil && !strings.Contains(err.Error(), "JobCapabilityRefused: store-unsupported") {
		t.Fatal(err, asked.Load(), string(raw))
	}
}

// countingStore counts every job port call and every commit carrying a registration.
type countingStore struct {
	durableJobStore
	calls, registered *atomic.Int32
}

func (s countingStore) ListJobs(ctx context.Context, resource, after string, limit int) (runcontext.JobPage, error) {
	s.calls.Add(1)
	return s.durableJobStore.ListJobs(ctx, resource, after, limit)
}
func (s countingStore) ReadJob(ctx context.Context, resource, job string) (*runcontext.RunJob, error) {
	s.calls.Add(1)
	return s.durableJobStore.ReadJob(ctx, resource, job)
}
func (s countingStore) AnswerJob(ctx context.Context, answer runcontext.JobAnswer, fence runcontext.LeaseFence) (runcontext.RunJob, error) {
	s.calls.Add(1)
	return s.durableJobStore.AnswerJob(ctx, answer, fence)
}
func (s countingStore) CommitFenced(ctx context.Context, input runcontext.RunCommit, fence runcontext.LeaseFence) (runcontext.RunReceipt, error) {
	if input.Job != nil {
		s.registered.Add(1)
	}
	return s.durableJobStore.CommitFenced(ctx, input, fence)
}

func (s countingStore) CommitHistoryFenced(ctx context.Context, input runcontext.RunHistoryCommit, fence runcontext.LeaseFence) (runcontext.RunHistoryReceipt, error) {
	if input.Run.Job != nil {
		s.registered.Add(1)
	}
	return s.durableJobStore.CommitHistoryFenced(ctx, input, fence)
}

func TestJobsWithNoJobToolOfferedTheStoreIsNeverAskedAboutJobs(t *testing.T) {
	for _, engine := range engines {
		t.Run(engine, func(t *testing.T) {
			l := newLeased(t, nil)
			counting := countingStore{durableJobStore: l.store, calls: &atomic.Int32{}, registered: &atomic.Int32{}}
			host := runcontext.NewReferenceEffectHost(counting)
			view, err := runcontext.FencedRunStore(counting, l.fence, host)
			if err != nil {
				t.Fatal(err)
			}
			schema := json.RawMessage(`{"type":"object"}`)
			taxonomy := &runcontext.ToolTaxonomy{Operation: "run", Effect: "act"}
			digest := runcontext.ToolListDigest([]runcontext.ListedTool{{Name: "plain", Description: "Plain.", InputSchema: schema, Taxonomy: taxonomy}})
			plain := runcontext.HostTool{Name: "plain", Description: "Plain.", InputSchema: schema, Taxonomy: taxonomy, EffectClass: "act", Approval: "required",
				Source:  &runcontext.ToolSource{Kind: "custom", ID: "runner", Trust: "person", Version: "1", ToolListDigest: digest, CurrentDigest: func(context.Context) (string, error) { return digest, nil }},
				Approve: func(runcontext.ApprovalRequest) string { return "allow" },
				Execute: func(context.Context, json.RawMessage) (runcontext.ToolResult, error) {
					return runcontext.ToolResult{Content: []runcontext.Content{runcontext.TextContent("ok")}}, nil
				}}
			if result := drive(t, view, engine, []runcontext.HostTool{plain}, []jobCall{{"start", jobArgs}}, options{}); result.err != nil {
				t.Fatal(result.err)
			}
			if counting.calls.Load() != 0 || counting.registered.Load() != 0 {
				t.Fatal(counting.calls.Load(), counting.registered.Load())
			}
		})
	}
}

// jobPair is a start and its cancel, declared as a pair on one source.
type jobPair struct {
	start, cancel runcontext.HostTool
	calls         *startCalls
	asked, sent   *atomic.Int32
	inputs        *[]runcontext.JobCancelInput
	id            string
}

func pairOf(t *testing.T, id string, answer func(context.Context, runcontext.JobStartInput) (runcontext.JobStartAnswer, error), cancel func(context.Context, runcontext.JobCancelInput) error, approve, key string) *jobPair {
	t.Helper()
	schema := json.RawMessage(`{"type":"object"}`)
	taxonomy := &runcontext.ToolTaxonomy{Operation: "run", Effect: "act"}
	digest := runcontext.ToolListDigest([]runcontext.ListedTool{{Name: "start_job", Description: "Start one background job.", InputSchema: schema, Taxonomy: taxonomy}, {Name: "cancel_job", Description: "Cancel one background job.", InputSchema: runcontext.JobCancelSchema, Taxonomy: taxonomy}})
	source := func() *runcontext.ToolSource {
		return &runcontext.ToolSource{Kind: "custom", ID: id, Trust: "person", Version: "1", ToolListDigest: digest, CurrentDigest: func(context.Context) (string, error) { return digest, nil }}
	}
	if answer == nil {
		answer = answered(startedAnswer, nil)
	}
	out := &jobPair{calls: &startCalls{}, asked: &atomic.Int32{}, sent: &atomic.Int32{}, inputs: &[]runcontext.JobCancelInput{}, id: id}
	var err error
	out.start, err = runcontext.JobStartTool(runcontext.JobStartSpec{Name: "start_job", Description: "Start one background job.", InputSchema: schema, Source: source(),
		Approve: func(runcontext.ApprovalRequest) string { return "allow" }, Approval: "required",
		Mint: func(context.Context, string) (string, error) { return runcontext.JobKeyDigest(key) },
		Start: func(ctx context.Context, input runcontext.JobStartInput) (runcontext.JobStartAnswer, error) {
			out.calls.mu.Lock()
			out.calls.requests = append(out.calls.requests, string(input.Request))
			out.calls.mu.Unlock()
			return answer(ctx, input)
		}})
	if err != nil {
		t.Fatal(err)
	}
	out.cancel, err = runcontext.JobCancelTool(runcontext.JobCancelSpec{Name: "cancel_job", Description: "Cancel one background job.", Source: source(), Start: out.start, Approval: "required",
		Approve: func(runcontext.ApprovalRequest) string { out.asked.Add(1); return approve },
		Cancel: func(ctx context.Context, input runcontext.JobCancelInput) error {
			out.sent.Add(1)
			*out.inputs = append(*out.inputs, input)
			if cancel != nil {
				return cancel(ctx, input)
			}
			return nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func (p *jobPair) tools() []runcontext.HostTool { return []runcontext.HostTool{p.start, p.cancel} }
func cancelArgs(job string) string              { return `{"job":"` + job + `","generation":1}` }

// startedPair is a run whose model started one job through the pair and ended its turn.
func startedPair(t *testing.T, engine string, p *jobPair) (*leasedRun, runcontext.RunJob) {
	t.Helper()
	l := newLeased(t, nil)
	drive(t, l.view, engine, p.tools(), []jobCall{{p.id + "__start_job", jobArgs}}, options{})
	return l, onlyJob(t, l.store)
}

func cancelRows(t *testing.T, store runcontext.RunStore) []bookRow {
	t.Helper()
	rows, _ := headBook(t, store)
	out := []bookRow{}
	for _, row := range rows.hosts() {
		if strings.HasSuffix(row.Tool, "cancel_job") {
			out = append(out, row)
		}
	}
	return out
}

func TestJobsACancelIsRequestedBeforeAdmissionAndSendsTheRecord(t *testing.T) {
	for _, engine := range engines {
		t.Run(engine, func(t *testing.T) {
			var l *leasedRun
			by := ""
			p := pairOf(t, "runner", nil, func(ctx context.Context, input runcontext.JobCancelInput) error {
				held, err := runcontext.ReadJob(ctx, l.store, jobRun, input.Job)
				if err == nil && held != nil && held.Cancel != nil {
					by = held.Cancel.Requested.By
				}
				return nil
			}, "allow", jobKey)
			var job runcontext.RunJob
			l, job = startedPair(t, engine, p)
			result := drive(t, l.view, engine, p.tools(), []jobCall{{"runner__cancel_job", cancelArgs(job.Job)}}, options{resume: true})
			held := onlyJob(t, l.store)
			if p.sent.Load() != 1 || by != "tool" || held.State != "started" || held.Cancel == nil || held.Cancel.Requested.Owner == nil || *held.Cancel.Requested.Owner != "worker-a" {
				t.Fatalf("%d %q %+v", p.sent.Load(), by, held)
			}
			sent := (*p.inputs)[0]
			if sent.Job != job.Job || sent.Generation != 1 || sent.Source != job.Source || sent.Tool != job.Tool || sent.Handle == nil || *sent.Handle != jobSecret || sent.Operation == "" {
				t.Fatalf("%+v", sent)
			}
			rows := cancelRows(t, l.store)
			if len(rows) != 1 || rows[0].State != "result" || rows[0].ID != held.Cancel.Requested.Operation {
				t.Fatalf("%+v", rows)
			}
			answer := lastToolResult(t, result)
			if !strings.Contains(answer, "cancel requested") || strings.Contains(answer, jobSecret) {
				t.Fatal(answer)
			}
		})
	}
}

func TestJobsAnotherSourcesCancelIsNoJob(t *testing.T) {
	for _, engine := range engines {
		t.Run(engine, func(t *testing.T) {
			prod, other := pairOf(t, "runner", nil, nil, "allow", jobKey), pairOf(t, "other", nil, nil, "allow", "job.key.0002.Hs8Pq3Lz6Wd1Rm9Tc4Vb7Nx2Ka5U")
			l, job := startedPair(t, engine, prod)
			result := drive(t, l.view, engine, append(prod.tools(), other.tools()...), []jobCall{{"other__cancel_job", cancelArgs(job.Job)}}, options{resume: true})
			if other.asked.Load() != 0 || other.sent.Load() != 0 || prod.sent.Load() != 0 || !strings.Contains(lastToolResult(t, result), "JobRefused: no-job") || len(cancelRows(t, l.store)) != 0 {
				t.Fatal(other.asked.Load(), other.sent.Load(), prod.sent.Load(), lastToolResult(t, result), cancelRows(t, l.store))
			}
			if held := onlyJob(t, l.store); held.Cancel != nil {
				t.Fatalf("%+v", held)
			}
		})
	}
}

func TestJobsACancelIsDeclaredWithAStartOfItsSource(t *testing.T) {
	a, b := pairOf(t, "runner", nil, nil, "allow", jobKey), pairOf(t, "other", nil, nil, "allow", jobKey)
	spec := runcontext.JobCancelSpec{Name: "cancel_job", Description: "Cancel.", Source: b.start.Source, Start: a.start, Cancel: func(context.Context, runcontext.JobCancelInput) error { return nil }}
	var refused *runcontext.JobCapabilityRefused
	if _, err := runcontext.JobCancelTool(spec); !errors.As(err, &refused) || refused.Reason != "job-host-unsupported" {
		t.Fatal(err)
	}
	spec.Source, spec.Start = a.start.Source, runcontext.HostTool{Name: "x", Source: a.start.Source}
	if _, err := runcontext.JobCancelTool(spec); !errors.As(err, &refused) || refused.Reason != "job-host-unsupported" {
		t.Fatal(err)
	}
	// Its source's list holds the cancel alone, so the source is offered, and the pair is not.
	alone := runcontext.ToolListDigest([]runcontext.ListedTool{{Name: "cancel_job", Description: "Cancel one background job.", InputSchema: runcontext.JobCancelSchema, Taxonomy: &runcontext.ToolTaxonomy{Operation: "run", Effect: "act"}}})
	spec.Description, spec.Start = "Cancel one background job.", a.start
	spec.Source = &runcontext.ToolSource{Kind: "custom", ID: "runner", Trust: "person", Version: "1", ToolListDigest: alone, CurrentDigest: func(context.Context) (string, error) { return alone, nil }}
	spec.Approve, spec.Approval = func(runcontext.ApprovalRequest) string { return "allow" }, "required"
	tool, err := runcontext.JobCancelTool(spec)
	if err != nil {
		t.Fatal(err)
	}
	l := newLeased(t, nil)
	result := drive(t, l.view, "eino", []runcontext.HostTool{tool}, nil, options{})
	text := toolAnswer(result)
	if result.err != nil {
		text += result.err.Error()
	}
	if !strings.Contains(text, "JobCapabilityRefused: job-host-unsupported") || result.asks.Load() != 0 {
		t.Fatal(result.asks.Load(), text)
	}
}

func TestJobsWhatARecordRefusesIsRefusedBeforeTheCardAndTheClaim(t *testing.T) {
	for _, engine := range engines {
		t.Run(engine, func(t *testing.T) {
			p := pairOf(t, "runner", nil, nil, "ask", jobKey)
			l, job := startedPair(t, engine, p)
			absent := drive(t, l.view, engine, p.tools(), []jobCall{{"runner__cancel_job", cancelArgs(runcontext.JobIdentity(jobRun, "no-such"))}}, options{resume: true})
			if !strings.Contains(lastToolResult(t, absent), "JobRefused: no-job") {
				t.Fatal(lastToolResult(t, absent))
			}
			generation := drive(t, l.view, engine, p.tools(), []jobCall{{"runner__cancel_job", `{"job":"` + job.Job + `","generation":2}`}}, options{resume: true})
			if strings.Contains(lastToolResult(t, generation), "requested") || p.sent.Load() != 0 {
				t.Fatal(lastToolResult(t, generation))
			}
			if _, err := runcontext.CompleteJob(context.Background(), l.store, completion(job.Job, nil)); err != nil {
				t.Fatal(err)
			}
			settled := drive(t, l.view, engine, p.tools(), []jobCall{{"runner__cancel_job", cancelArgs(job.Job)}}, options{resume: true})
			if !strings.Contains(lastToolResult(t, settled), "JobRefused: job-settled") || p.asked.Load() != 0 || p.sent.Load() != 0 || len(cancelRows(t, l.store)) != 0 {
				t.Fatal(lastToolResult(t, settled), p.asked.Load(), p.sent.Load())
			}
			none := pairOf(t, "runner", answered(runcontext.JobStartAnswer{Started: true}, nil), nil, "ask", jobKey)
			lost, unproven := startedPair(t, engine, none)
			result := drive(t, lost.view, engine, none.tools(), []jobCall{{"runner__cancel_job", cancelArgs(unproven.Job)}}, options{resume: true})
			if unproven.State != "unproven" || !strings.Contains(lastToolResult(t, result), "JobRefused: job-unproven") || none.asked.Load() != 0 || none.sent.Load() != 0 {
				t.Fatal(unproven.State, lastToolResult(t, result), none.asked.Load())
			}
		})
	}
}

func TestJobsAHeldCancelProvesWhatWasShown(t *testing.T) {
	for _, engine := range engines {
		t.Run(engine, func(t *testing.T) {
			p := pairOf(t, "runner", nil, nil, "ask", jobKey)
			l, job := startedPair(t, engine, p)
			first := drive(t, l.view, engine, p.tools(), []jobCall{{"runner__cancel_job", cancelArgs(job.Job)}}, options{resume: true})
			callID := ""
			for _, event := range first.events {
				if event.ApprovalRequested != nil {
					callID = event.ApprovalRequested.CallID
				}
			}
			if callID == "" || p.sent.Load() != 0 || onlyJob(t, l.store).Cancel != nil {
				t.Fatal(callID, p.sent.Load())
			}
			card := runcontext.JobCancelCardOf(onlyJob(t, l.store))
			if card.Source != job.Source || card.Tool != job.Tool || card.State != "started" || card.Generation != 1 || card.Job != job.Job {
				t.Fatalf("%+v", card)
			}
			refused := drive(t, l.view, engine, p.tools(), nil, options{resume: true, decision: &contract.CheckpointApprovalDecision{CallID: callID, Approved: true}})
			if !strings.Contains(fmt.Sprint(refused.err)+toolAnswer(refused), "ApprovalNotShown") || p.sent.Load() != 0 {
				t.Fatal(refused.err, p.sent.Load())
			}
			drive(t, l.view, engine, p.tools(), nil, options{resume: true, decision: heldProof(t, l.store, callID)})
			if p.sent.Load() != 1 {
				t.Fatal(p.sent.Load())
			}
		})
	}
}

func TestJobsADigestedHandleIsNeverSentAsTheReference(t *testing.T) {
	long := strings.Repeat("h", 300)
	p := pairOf(t, "runner", answered(runcontext.JobStartAnswer{Started: true, Handle: long}, nil), nil, "allow", jobKey)
	l, job := startedPair(t, "eino", p)
	drive(t, l.view, "eino", p.tools(), []jobCall{{"runner__cancel_job", cancelArgs(job.Job)}}, options{resume: true})
	if p.sent.Load() != 1 || (*p.inputs)[0].Handle != nil || (*p.inputs)[0].HandleDigest == nil || *(*p.inputs)[0].HandleDigest != fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(long))) {
		t.Fatalf("%+v", *p.inputs)
	}
}

func TestJobsARepeatedCancelIsAnsweredFromTheJob(t *testing.T) {
	for _, engine := range engines {
		t.Run(engine, func(t *testing.T) {
			p := pairOf(t, "runner", nil, nil, "allow", jobKey)
			l, job := startedPair(t, engine, p)
			call := []jobCall{{"runner__cancel_job", cancelArgs(job.Job)}}
			drive(t, l.view, engine, p.tools(), call, options{resume: true})
			if _, err := runcontext.AcknowledgeJobCancel(context.Background(), l.store, runcontext.JobCancelAcknowledgement{Version: 1, Resource: jobRun, Job: job.Job, Generation: 1, Evidence: "word-1", Outcome: "accepted", Key: jobKey}); err != nil {
				t.Fatal(err)
			}
			repeat := drive(t, l.view, engine, p.tools(), call, options{resume: true})
			if p.sent.Load() != 1 || !strings.Contains(lastToolResult(t, repeat), "cancel accepted") {
				t.Fatal(p.sent.Load(), lastToolResult(t, repeat))
			}
		})
	}
}

func TestJobsACancelThatFailsHoldsOneFixedFailureAndIsNotSentAgain(t *testing.T) {
	for _, engine := range engines {
		t.Run(engine, func(t *testing.T) {
			p := pairOf(t, "runner", nil, func(context.Context, runcontext.JobCancelInput) error {
				return failure.Authored("tool-failed", "SENTINEL-HOST-TEXT: half sent", "tool")
			}, "allow", jobKey)
			l, job := startedPair(t, engine, p)
			call := []jobCall{{"runner__cancel_job", cancelArgs(job.Job)}}
			result := drive(t, l.view, engine, p.tools(), call, options{resume: true})
			rows := cancelRows(t, l.store)
			if len(rows) != 1 || rows[0].State != "unknown" || rows[0].Failure["outcome"] != "unknown" || !strings.Contains(fmt.Sprint(rows[0].Failure["detail"]), "JobCancelUnrecorded") {
				t.Fatalf("%+v", rows)
			}
			answer := toolAnswer(result)
			if strings.Contains(answer, "SENTINEL-HOST-TEXT") || strings.Contains(answer, "invalid-tool-input") || !strings.Contains(lastToolResult(t, result), "cancel requested") {
				t.Fatal(answer)
			}
			drive(t, l.view, engine, p.tools(), call, options{resume: true})
			if p.sent.Load() != 1 {
				t.Fatal(p.sent.Load())
			}
		})
	}
}

// settlesFirst completes the job just before the store is asked to record the cancel.
type settlesFirst struct {
	durableJobStore
	settle func()
}

func (s *settlesFirst) RequestJobCancel(ctx context.Context, ask runcontext.JobCancelAsk, fence runcontext.LeaseFence) (runcontext.RunJob, error) {
	s.settle()
	return s.durableJobStore.RequestJobCancel(ctx, ask, fence)
}

func TestJobsAJobThatSettlesAfterItWasAskedAboutIsNotSent(t *testing.T) {
	for _, engine := range engines {
		t.Run(engine, func(t *testing.T) {
			p := pairOf(t, "runner", nil, nil, "allow", jobKey)
			l, job := startedPair(t, engine, p)
			store := &settlesFirst{durableJobStore: l.store, settle: func() {
				if _, err := runcontext.CompleteJob(context.Background(), l.store, completion(job.Job, nil)); err != nil {
					t.Fatal(err)
				}
			}}
			view, err := runcontext.UnattendedRunStore(store, l.fence, runcontext.NewReferenceEffectHost(store))
			if err != nil {
				t.Fatal(err)
			}
			result := drive(t, view, engine, p.tools(), []jobCall{{"runner__cancel_job", cancelArgs(job.Job)}}, options{resume: true})
			held := onlyJob(t, l.store)
			rows := cancelRows(t, l.store)
			if p.sent.Load() != 0 || held.State != "completed" || held.Cancel != nil || len(rows) != 1 || rows[0].State != "unknown" || rows[0].Failure["outcome"] != "not-started" || !strings.Contains(fmt.Sprint(rows[0].Failure["detail"]), "JobCancelNotSent") {
				t.Fatalf("%d %+v %+v", p.sent.Load(), held, rows)
			}
			if !strings.Contains(lastToolResult(t, result), "completed") {
				t.Fatal(lastToolResult(t, result))
			}
		})
	}
}

func TestJobsTheHostsWordMovesNoStateAndOnlyACompletionSaysItStopped(t *testing.T) {
	ctx := context.Background()
	p := pairOf(t, "runner", nil, nil, "allow", jobKey)
	l, job := startedPair(t, "eino", p)
	drive(t, l.view, "eino", p.tools(), []jobCall{{"runner__cancel_job", cancelArgs(job.Job)}}, options{resume: true})
	if _, err := l.store.ReleaseLease(ctx, l.fence); err != nil {
		t.Fatal(err)
	}
	worded, err := runcontext.AcknowledgeJobCancel(ctx, l.store, runcontext.JobCancelAcknowledgement{Version: 1, Resource: jobRun, Job: job.Job, Generation: 1, Evidence: "word-1", Outcome: "accepted", Key: jobKey})
	if err != nil || worded.State != "started" || runcontext.JobProjectionOf(worded).Cancel == nil || *runcontext.JobProjectionOf(worded).Cancel != "accepted" {
		t.Fatalf("%+v %v", worded, err)
	}
	done, err := runcontext.CompleteJob(ctx, l.store, completion(job.Job, func(e *runcontext.JobCompletionEvidence) { e.Outcome = "cancelled" }))
	if err != nil || done.State != "completed" || done.Completion.Outcome != "cancelled" || done.Cancel == nil || done.Cancel.Acknowledged == nil {
		t.Fatalf("%+v %v", done, err)
	}
	report, err := runcontext.JobReportOf(ctx, l.store, jobRun)
	if err != nil {
		t.Fatal(err)
	}
	decisions := []string{}
	for _, entry := range report.Entries {
		decisions = append(decisions, entry.Decision)
	}
	if strings.Join(decisions, ",") != "register,start,cancel-requested,cancel-acknowledged,complete" {
		t.Fatal(decisions)
	}
}

func TestJobsASubAgentIsOfferedNoCancelToolAtDeclaration(t *testing.T) {
	// The seam is the whole of it: the declaration's refusal reads it, in TestJobToolsAreOfferedToNoSubAgent.
	p := pairOf(t, "runner", nil, nil, "allow", jobKey)
	policy := runcontext.Policy{CataloguedSubagents: 16, SubagentCatalogueBytes: 4000, MaxInputBytes: 1 << 20, TurnMilliseconds: 1 << 20, ToolResultBytes: 1 << 20}
	raw, err := os.ReadFile("../../../contract/vectors/subagents/declaration.json")
	if err != nil {
		t.Fatal(err)
	}
	var file struct{ Items []runcontext.Subagent }
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	s := file.Items[0]
	s.Tools = []string{p.cancel.Name}
	admitted, _, err := runcontext.DeclareSubagents([]runcontext.Subagent{s}, []runcontext.HostTool{p.cancel}, nil, policy)
	if err != nil || len(admitted) != 0 {
		t.Fatal(len(admitted), err)
	}
}

func TestJobsAnAbandonTakesAFreshFenceAndSendsThroughTheEffectHost(t *testing.T) {
	ctx := context.Background()
	p := pairOf(t, "runner", nil, nil, "refuse", jobKey)
	l, job := startedPair(t, "eino", p)
	before, err := l.store.Load(ctx, jobRun)
	if err != nil || before == nil {
		t.Fatal(before, err)
	}
	inputs := []runcontext.JobCancelInput{}
	send := func(_ context.Context, input runcontext.JobCancelInput) error {
		inputs = append(inputs, input)
		return nil
	}
	answer, err := runcontext.AbandonJobs(ctx, l.store, l.host, jobRun, "cmd-1", jobHour, send)
	if err != nil {
		t.Fatal(err)
	}
	if answer.Fence == nil || answer.Fence.Owner != runcontext.JobAbandonOwner || answer.Fence.Epoch == l.fence.Epoch || len(answer.Jobs) != 1 || answer.Jobs[0].Delivery != "sent" {
		t.Fatalf("%+v", answer)
	}
	if len(inputs) != 1 || inputs[0].Handle == nil || *inputs[0].Handle != jobSecret || p.asked.Load() != 0 {
		t.Fatalf("%+v %d", inputs, p.asked.Load())
	}
	after, _ := l.store.Load(ctx, jobRun)
	if after.Revision != before.Revision {
		t.Fatal("an abandon committed to the run")
	}
	var refused *runcontext.LeaseRefused
	if _, err := l.store.CommitFenced(ctx, runcontext.RunCommit{RunID: jobRun, ExpectedRevision: before.Revision, CommitID: "late", Checkpoint: before.Checkpoint, Events: [][]byte{}}, l.fence); !errors.As(err, &refused) || refused.Reason != "stale-epoch" {
		t.Fatal(err)
	}
	held := onlyJob(t, l.store)
	if held.State != "started" || held.Cancel == nil || held.Cancel.Requested.By != "command" || held.Cancel.Requested.Operation != runcontext.JobCancelOperation("cmd-1", job.Job) {
		t.Fatalf("%+v", held)
	}
	// A retry of the command sends nothing twice: the effect host admits its stable operation once.
	again, err := runcontext.AbandonJobs(ctx, l.store, l.host, jobRun, "cmd-1", jobHour, send)
	if err != nil || len(inputs) != 1 || again.Jobs[0].Delivery != "already-admitted" {
		t.Fatalf("%+v %v %d", again, err, len(inputs))
	}
}

func TestJobsAnAbandonSaysWhatItCouldNotSend(t *testing.T) {
	ctx := context.Background()
	send := func(context.Context, runcontext.JobCancelInput) error { return errors.New("down") }
	abandon := func(t *testing.T, l *leasedRun) runcontext.JobAbandonAnswer {
		t.Helper()
		answer, err := runcontext.AbandonJobs(ctx, l.store, l.host, jobRun, "cmd-1", jobHour, send)
		if err != nil {
			t.Fatal(err)
		}
		return answer
	}
	t.Run("a failed cancel", func(t *testing.T) {
		l, _ := startedPair(t, "eino", pairOf(t, "runner", nil, nil, "allow", jobKey))
		if got := abandon(t, l); got.Jobs[0].Delivery != "failed" {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("no handle", func(t *testing.T) {
		l, _ := startedPair(t, "eino", pairOf(t, "runner", answered(runcontext.JobStartAnswer{Started: true}, nil), nil, "allow", jobKey))
		if got := abandon(t, l); got.Jobs[0].Delivery != "no-handle" {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("settled", func(t *testing.T) {
		l, job := startedPair(t, "eino", pairOf(t, "runner", nil, nil, "allow", jobKey))
		if _, err := runcontext.CompleteJob(ctx, l.store, completion(job.Job, nil)); err != nil {
			t.Fatal(err)
		}
		if got := abandon(t, l); got.Jobs[0].Delivery != "settled" {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("evidence on a closed job", func(t *testing.T) {
		l, job := startedPair(t, "eino", pairOf(t, "runner", answered(runcontext.JobStartAnswer{}, nil), nil, "allow", jobKey))
		if _, err := runcontext.CompleteJob(ctx, l.store, completion(job.Job, nil)); err != nil {
			t.Fatal(err)
		}
		if got := abandon(t, l); got.Jobs[0].Delivery != "contradicted" {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("the host accepted", func(t *testing.T) {
		p := pairOf(t, "runner", nil, nil, "allow", jobKey)
		l, job := startedPair(t, "eino", p)
		drive(t, l.view, "eino", p.tools(), []jobCall{{"runner__cancel_job", cancelArgs(job.Job)}}, options{resume: true})
		if _, err := runcontext.AcknowledgeJobCancel(ctx, l.store, runcontext.JobCancelAcknowledgement{Version: 1, Resource: jobRun, Job: job.Job, Generation: 1, Evidence: "word-1", Outcome: "accepted", Key: jobKey}); err != nil {
			t.Fatal(err)
		}
		if got := abandon(t, l); got.Jobs[0].Delivery != "acknowledged" {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("past its deadline", func(t *testing.T) {
		l, _ := startedPair(t, "eino", pairOf(t, "runner", nil, nil, "allow", jobKey))
		limit := l.clock.now().Add(time.Minute).UTC().Format("2006-01-02T15:04:05.000Z")
		if _, err := l.store.LimitRun(ctx, runcontext.RunLimit{Version: 1, Resource: jobRun, Run: limit}); err != nil {
			t.Fatal(err)
		}
		l.clock.move(time.Minute)
		got := abandon(t, l)
		held := onlyJob(t, l.store)
		if got.Fence != nil || got.Jobs[0].Delivery != "undelivered" || held.Cancel == nil || held.Cancel.Requested.Undelivered == nil {
			t.Fatalf("%+v %+v", got, held)
		}
	})
	t.Run("a digest is never the reference", func(t *testing.T) {
		l, _ := startedPair(t, "eino", pairOf(t, "runner", answered(runcontext.JobStartAnswer{Started: true, Handle: strings.Repeat("h", 300)}, nil), nil, "allow", jobKey))
		var sent runcontext.JobCancelInput
		if _, err := runcontext.AbandonJobs(ctx, l.store, l.host, jobRun, "cmd-1", jobHour, func(_ context.Context, input runcontext.JobCancelInput) error { sent = input; return nil }); err != nil {
			t.Fatal(err)
		}
		if sent.Handle != nil || sent.HandleDigest == nil {
			t.Fatalf("%+v", sent)
		}
	})
	t.Run("a host that only declares", func(t *testing.T) {
		l, _ := startedPair(t, "eino", pairOf(t, "runner", nil, nil, "allow", jobKey))
		var refused *runcontext.LeaseCapabilityRefused
		if _, err := runcontext.AbandonJobs(ctx, l.store, declaredOnly{}, jobRun, "cmd-1", jobHour, send); !errors.As(err, &refused) {
			t.Fatal(err)
		}
		if lease, _ := l.store.ReadLease(ctx, jobRun); lease == nil || lease.Epoch != l.fence.Epoch {
			t.Fatalf("%+v", lease)
		}
	})
}

func TestJobsAHeldCancelWhoseJobSettledBeforeTheDecisionIsNotSent(t *testing.T) {
	for _, engine := range engines {
		t.Run(engine, func(t *testing.T) {
			p := pairOf(t, "runner", nil, nil, "ask", jobKey)
			l, job := startedPair(t, engine, p)
			first := drive(t, l.view, engine, p.tools(), []jobCall{{"runner__cancel_job", cancelArgs(job.Job)}}, options{resume: true})
			callID := ""
			for _, event := range first.events {
				if event.ApprovalRequested != nil {
					callID = event.ApprovalRequested.CallID
				}
			}
			decision := heldProof(t, l.store, callID)
			if _, err := runcontext.CompleteJob(context.Background(), l.store, completion(job.Job, nil)); err != nil {
				t.Fatal(err)
			}
			result := drive(t, l.view, engine, p.tools(), nil, options{resume: true, decision: decision})
			if p.sent.Load() != 0 || !strings.Contains(toolAnswer(result), "JobRefused: job-settled") || len(cancelRows(t, l.store)) != 0 {
				t.Fatal(p.sent.Load(), toolAnswer(result), cancelRows(t, l.store))
			}
		})
	}
}

// otherOwnerAbandon answers an abandon's lease under another owner.
type otherOwnerAbandon struct{ durableJobStore }

func (s otherOwnerAbandon) AbandonJobs(ctx context.Context, input runcontext.JobAbandon) (runcontext.JobAbandonTaken, error) {
	taken, err := s.durableJobStore.AbandonJobs(ctx, input)
	if taken.Lease != nil {
		taken.Lease.Owner = "worker-z"
	}
	return taken, err
}

func TestJobsAnAbandonTrustsNoLeaseOfAnotherOwner(t *testing.T) {
	l, _ := startedPair(t, "eino", pairOf(t, "runner", nil, nil, "allow", jobKey))
	store := otherOwnerAbandon{l.store}
	sent := 0
	_, err := runcontext.AbandonJobs(context.Background(), store, runcontext.NewReferenceEffectHost(store), jobRun, "cmd-1", jobHour, func(context.Context, runcontext.JobCancelInput) error { sent++; return nil })
	var refused *runcontext.LeaseRefused
	if !errors.As(err, &refused) || refused.Reason != "invalid" || sent != 0 {
		t.Fatal(err, sent)
	}
}

// A nested run on the context a cancel was handed is no cancel itself.
func TestJobsANestedRunInACancelIsNoCancel(t *testing.T) {
	for _, engine := range engines {
		t.Run(engine, func(t *testing.T) {
			nested := newLeased(t, nil)
			nestedAnswer := ""
			p := pairOf(t, "runner", nil, func(ctx context.Context, _ runcontext.JobCancelInput) error {
				result := drive(t, nested.view, engine, nil, nil, options{ctx: ctx})
				nestedAnswer = fmt.Sprint(result.err) + toolAnswer(result)
				return result.err
			}, "allow", jobKey)
			l, job := startedPair(t, engine, p)
			result := drive(t, l.view, engine, p.tools(), []jobCall{{"runner__cancel_job", cancelArgs(job.Job)}}, options{resume: true})
			rows := cancelRows(t, l.store)
			if p.sent.Load() != 1 || len(rows) != 1 || rows[0].State != "result" || !strings.Contains(lastToolResult(t, result), "cancel requested") {
				t.Fatal(p.sent.Load(), rows, lastToolResult(t, result))
			}
			// The nested run's own requests went through: none was taken for a cancel.
			if strings.Contains(nestedAnswer, `"type":"error"`) {
				t.Fatal(nestedAnswer)
			}
		})
	}
}

// lostJobs answers as a store whose job rows were lost: a repeat's read finds none.
type lostJobs struct{ durableJobStore }

func (lostJobs) ReadJob(context.Context, string, string) (*runcontext.RunJob, error) { return nil, nil }

func TestJobsWithItsRecordLostARepeatOfAStartAnswersItsRow(t *testing.T) {
	for _, engine := range engines {
		t.Run(engine, func(t *testing.T) {
			l := newLeased(t, nil)
			calls := &startCalls{}
			tool := startTool(t, calls, answered(startedAnswer, nil), "allow", nil)
			drive(t, l.view, engine, []runcontext.HostTool{tool}, []jobCall{{"start", jobArgs}}, options{})
			store := lostJobs{l.store}
			view, err := runcontext.UnattendedRunStore(store, l.fence, runcontext.NewReferenceEffectHost(store))
			if err != nil {
				t.Fatal(err)
			}
			repeat := drive(t, view, engine, []runcontext.HostTool{tool}, []jobCall{{"start", jobArgs}}, options{resume: true})
			if calls.count() != 1 || !strings.Contains(lastToolResult(t, repeat), "started, not finished") {
				t.Fatal(calls.count(), lastToolResult(t, repeat))
			}
		})
	}
}

// askLost records the cancel and loses the acknowledgement, as a crash there leaves it.
type askLost struct {
	durableJobStore
	lost *atomic.Bool
}

func (s askLost) RequestJobCancel(ctx context.Context, ask runcontext.JobCancelAsk, fence runcontext.LeaseFence) (runcontext.RunJob, error) {
	job, err := s.durableJobStore.RequestJobCancel(ctx, ask, fence)
	if err == nil && s.lost.Swap(false) {
		return runcontext.RunJob{}, errors.New("acknowledgement lost")
	}
	return job, err
}

// #303, #310: the batch. A host that refuses after perform, the cancel's answer, and the abandon's.

// refusingAfter performs, then refuses, while on: a host that does something after perform.
type refusingAfter struct {
	inner   *runcontext.ReferenceEffectHost
	on      *atomic.Bool
	refusal func() error
}

func (h refusingAfter) EffectFencing() string { return h.inner.EffectFencing() }
func (h refusingAfter) Dispatch(ctx context.Context, admission runcontext.EffectAdmission, perform func(context.Context) error) error {
	if err := h.inner.Dispatch(ctx, admission, perform); err != nil || !h.on.Load() {
		return err
	}
	return h.refusal()
}

// certifiedView is the run's view through a certified effect host that create builds over the store.
func certifiedView(t *testing.T, l *leasedRun, create func(*runcontext.ReferenceEffectHost) runcontext.EffectHost) runcontext.RunStore {
	t.Helper()
	certified, err := runcontext.CertifyEffectHost(context.Background(), func(authority runcontext.LeaseAuthority) runcontext.EffectHost {
		return create(runcontext.NewReferenceEffectHost(authority))
	}, l.store)
	if err != nil {
		t.Fatal(err)
	}
	view, err := runcontext.FencedRunStore(l.store, l.fence, certified)
	if err != nil {
		t.Fatal(err)
	}
	return view
}

func failureDetail(row bookRow) string {
	detail, _ := row.Failure["detail"].(string)
	return detail
}

// R-4: after perform returned, nothing reads as refused input: the start's job keeps its answer.
func TestJobsAHostThatRefusesAfterPerformTakesTheThrownStartPath(t *testing.T) {
	refusals := []struct {
		name    string
		refusal func() error
	}{
		{"LeaseRefused", func() error { return &runcontext.LeaseRefused{Reason: "stale-epoch"} }},
		{"JobRefused", func() error { return &runcontext.JobRefused{Reason: "invalid"} }},
		{"an authored input refusal", func() error { return failure.Authored("invalid-tool-input", "SENTINEL-AUTHORED", "tool") }},
	}
	for _, engine := range engines {
		for _, c := range refusals {
			t.Run(engine+"/"+c.name, func(t *testing.T) {
				l, on := newLeased(t, nil), &atomic.Bool{}
				view := certifiedView(t, l, func(inner *runcontext.ReferenceEffectHost) runcontext.EffectHost {
					return refusingAfter{inner: inner, on: on, refusal: c.refusal}
				})
				calls := &startCalls{}
				tool := startTool(t, calls, answered(startedAnswer, nil), "allow", nil)
				on.Store(true)
				result := drive(t, view, engine, []runcontext.HostTool{tool}, []jobCall{{"start", jobArgs}}, options{})
				job := onlyJob(t, l.store)
				rows, _ := headBook(t, l.store)
				row := rows.row(job.Operation)
				if calls.count() != 1 || job.State != "started" || job.Handle == nil || *job.Handle != jobSecret || row.State != "unknown" || !strings.HasPrefix(failureDetail(row), "JobStartUnrecorded:") {
					t.Fatalf("%d %+v %+v", calls.count(), job, row)
				}
				if !strings.Contains(lastToolResult(t, result), "started, not finished") || strings.Contains(toolAnswer(result), "SENTINEL-AUTHORED") {
					t.Fatal(lastToolResult(t, result))
				}
			})
		}
	}
}

// F-17: a cancel whose effect host refuses after it was handed over reads as maybe sent, never as not started.
func TestJobsACancelWhoseHostRefusesAfterPerformMayHaveBeenSent(t *testing.T) {
	for _, engine := range engines {
		t.Run(engine, func(t *testing.T) {
			p := pairOf(t, "runner", nil, nil, "allow", jobKey)
			l, on := newLeased(t, nil), &atomic.Bool{}
			view := certifiedView(t, l, func(inner *runcontext.ReferenceEffectHost) runcontext.EffectHost {
				return refusingAfter{inner: inner, on: on, refusal: func() error { return &runcontext.LeaseRefused{Reason: "stale-epoch"} }}
			})
			drive(t, view, engine, p.tools(), []jobCall{{"runner__start_job", jobArgs}}, options{})
			job := onlyJob(t, l.store)
			on.Store(true)
			result := drive(t, view, engine, p.tools(), []jobCall{{"runner__cancel_job", cancelArgs(job.Job)}}, options{resume: true})
			rows := cancelRows(t, l.store)
			if p.sent.Load() != 1 || len(rows) != 1 || rows[0].State != "unknown" || !strings.HasPrefix(failureDetail(rows[0]), "JobCancelUnrecorded:") {
				t.Fatal(p.sent.Load(), rows)
			}
			// The model is told what the job says: a cancel requested, which may not have been delivered.
			if answer := lastToolResult(t, result); strings.Contains(answer, "this cancel was not sent") || !strings.Contains(answer, "cancel requested") {
				t.Fatal(answer)
			}
		})
	}
}

// refusingOnError is an effect host that answers any failure of its dispatch with its own LeaseRefused, once on.
type refusingOnError struct {
	inner *runcontext.ReferenceEffectHost
	on    *atomic.Bool
}

func (h refusingOnError) EffectFencing() string { return h.inner.EffectFencing() }
func (h refusingOnError) Dispatch(ctx context.Context, admission runcontext.EffectAdmission, perform func(context.Context) error) error {
	err := h.inner.Dispatch(ctx, admission, perform)
	if err != nil && h.on.Load() {
		return &runcontext.LeaseRefused{Reason: "stale-epoch"}
	}
	return err
}

// agent#323 LOW-1: the host's cancel fails after its request left, so the execute never returns, and the effect host
// answers with its own LeaseRefused. The cancel was handed over: its row is keyed on that, not on the execute returning.
func TestJobsACancelWhoseHostThrowsAfterItWasHandedOverMayHaveBeenSent(t *testing.T) {
	for _, engine := range engines {
		t.Run(engine, func(t *testing.T) {
			p := pairOf(t, "runner", nil, func(context.Context, runcontext.JobCancelInput) error {
				return errors.New("the request left; its answer did not come back")
			}, "allow", jobKey)
			l, on := newLeased(t, nil), &atomic.Bool{}
			view := certifiedView(t, l, func(inner *runcontext.ReferenceEffectHost) runcontext.EffectHost {
				return refusingOnError{inner: inner, on: on}
			})
			drive(t, view, engine, p.tools(), []jobCall{{"runner__start_job", jobArgs}}, options{})
			job := onlyJob(t, l.store)
			on.Store(true)
			result := drive(t, view, engine, p.tools(), []jobCall{{"runner__cancel_job", cancelArgs(job.Job)}}, options{resume: true})
			rows := cancelRows(t, l.store)
			if p.sent.Load() != 1 || len(rows) != 1 || rows[0].State != "unknown" || !strings.HasPrefix(failureDetail(rows[0]), "JobCancelUnrecorded:") {
				t.Fatal(p.sent.Load(), rows)
			}
			if answer := lastToolResult(t, result); strings.Contains(answer, "this cancel was not sent") || !strings.Contains(answer, "cancel requested") {
				t.Fatal(answer)
			}
		})
	}
}

// F-20: an effect host that refuses with LeaseRefused before anything was handed over: the cancel was not sent.
func TestJobsACancelWhoseHostRefusesBeforePerformWasNotSent(t *testing.T) {
	for _, engine := range engines {
		t.Run(engine, func(t *testing.T) {
			p := pairOf(t, "runner", nil, nil, "allow", jobKey)
			l, on := newLeased(t, nil), &atomic.Bool{}
			view := certifiedView(t, l, func(inner *runcontext.ReferenceEffectHost) runcontext.EffectHost {
				return failingHost{inner: inner, on: on, err: &runcontext.LeaseRefused{Reason: "other-owner"}}
			})
			drive(t, view, engine, p.tools(), []jobCall{{"runner__start_job", jobArgs}}, options{})
			job := onlyJob(t, l.store)
			on.Store(true)
			result := drive(t, view, engine, p.tools(), []jobCall{{"runner__cancel_job", cancelArgs(job.Job)}}, options{resume: true})
			if p.sent.Load() != 0 || !strings.Contains(lastToolResult(t, result), "this cancel was not sent") {
				t.Fatal(p.sent.Load(), lastToolResult(t, result))
			}
		})
	}
}

// answersAltered records the cancel and answers the job asked for, altered. A pointer keeps it comparable, as the
// effect host's authority must be.
type answersAltered struct {
	durableJobStore
	alter *func(*runcontext.RunJob)
}

func (s answersAltered) RequestJobCancel(ctx context.Context, ask runcontext.JobCancelAsk, fence runcontext.LeaseFence) (runcontext.RunJob, error) {
	held, err := s.durableJobStore.RequestJobCancel(ctx, ask, fence)
	if err == nil {
		(*s.alter)(&held)
	}
	return held, err
}

// readsAltered (agent#323 LOW-3) also alters its read of one job (of every job where job is empty), as answersAltered
// alters the ask; its list stays honest.
type readsAltered struct {
	answersAltered
	job string
}

func (s readsAltered) ReadJob(ctx context.Context, resource, job string) (*runcontext.RunJob, error) {
	held, err := s.answersAltered.ReadJob(ctx, resource, job)
	if err == nil && held != nil && (s.job == "" || held.Job == s.job) {
		(*s.alter)(held)
	}
	return held, err
}

// F-2b: a store that answers the job asked for with another job's handle, or another source, sends nothing. agent#323
// LOW-3: a store that alters its read of the job as it alters its ask is held to what the writer saw of the job at attach.
func TestJobsACancelSendsNothingTheRecordReadBeforeDoesNotSay(t *testing.T) {
	for _, engine := range engines {
		for _, both := range []bool{false, true} {
			// read is what a store that also alters its read is answered with: a job read under another source or tool is
			// not this pair's, and is refused before anything is asked (jobCancelRefusal); the others reach the ask.
			for _, c := range []struct {
				name, first, read string
				alter             func(*runcontext.RunJob)
			}{
				{"another job's handle", "H-ONE", "this cancel was not sent", func(job *runcontext.RunJob) { other := "H-TWO"; job.Handle = &other }},
				{"another source", "H-ONE", "JobRefused: no-job.", func(job *runcontext.RunJob) { job.Source = "other-source" }},
				// A handle kept as its digest, answered without its marker, would go out as the reference.
				{"its digest marker dropped", strings.Repeat("L", 300), "this cancel was not sent", func(job *runcontext.RunJob) { job.HandleDigested = nil }},
				{"another tool", "H-ONE", "JobRefused: no-job.", func(job *runcontext.RunJob) { job.Tool = "other-tool" }},
			} {
				t.Run(fmt.Sprintf("%s/%s/read-altered=%t", engine, c.name, both), func(t *testing.T) {
					p := pairOf(t, "runner", answered(runcontext.JobStartAnswer{Started: true, Handle: c.first}, nil), nil, "allow", jobKey)
					l := newLeased(t, nil)
					drive(t, l.view, engine, p.tools(), []jobCall{{"runner__start_job", `{"name":"one"}`}}, options{})
					second := pairOf(t, "runner", answered(runcontext.JobStartAnswer{Started: true, Handle: "H-TWO"}, nil), nil, "allow", "job.key.0002.Hs8Pq3Lz6Wd1Rm9Tc4Vb7Nx2Ka5U")
					drive(t, l.view, engine, second.tools(), []jobCall{{"runner__start_job", `{"name":"two"}`}}, options{resume: true})
					page, _ := runcontext.ListJobs(context.Background(), l.store, jobRun, "", 10)
					if len(page.Jobs) != 2 {
						t.Fatalf("%+v", page.Jobs)
					}
					first := page.Jobs[0].Job
					if page.Jobs[0].Handle != nil && *page.Jobs[0].Handle == "H-TWO" {
						first = page.Jobs[1].Job
					}
					store := answersAltered{l.store, &c.alter}
					view, err := runcontext.UnattendedRunStore(store, l.fence, runcontext.NewReferenceEffectHost(store))
					if both {
						store := readsAltered{store, first}
						view, err = runcontext.UnattendedRunStore(store, l.fence, runcontext.NewReferenceEffectHost(store))
					}
					if err != nil {
						t.Fatal(err)
					}
					result := drive(t, view, engine, p.tools(), []jobCall{{"runner__cancel_job", cancelArgs(first)}}, options{resume: true})
					want := "this cancel was not sent"
					if both {
						want = c.read
					}
					if p.sent.Load() != 0 || second.sent.Load() != 0 || !strings.Contains(lastToolResult(t, result), want) {
						t.Fatal(p.sent.Load(), second.sent.Load(), lastToolResult(t, result))
					}
				})
			}
		}
	}
}

// agent#323 LOW-3: the writer that started a job holds what its start's acknowledged answer said, with no attach after it:
// a store that then answers its read and its ask of the job alike with another record sends nothing.
func TestJobsAWriterThatStartedAJobHoldsItsStartsAnswer(t *testing.T) {
	for _, engine := range engines {
		for _, c := range []struct {
			name, first string
			alter       func(*runcontext.RunJob)
		}{
			{"another job's handle", "H-ONE", func(job *runcontext.RunJob) { other := "H-TWO"; job.Handle = &other }},
			{"its digest marker dropped", strings.Repeat("L", 300), func(job *runcontext.RunJob) { job.HandleDigested = nil }},
		} {
			t.Run(engine+"/"+c.name, func(t *testing.T) {
				p := pairOf(t, "runner", answered(runcontext.JobStartAnswer{Started: true, Handle: c.first}, nil), nil, "allow", jobKey)
				l, armed := newLeased(t, nil), &atomic.Bool{}
				alter := func(job *runcontext.RunJob) {
					if armed.Load() {
						c.alter(job)
					}
				}
				store := readsAltered{answersAltered{l.store, &alter}, ""}
				view, err := runcontext.UnattendedRunStore(store, l.fence, runcontext.NewReferenceEffectHost(store))
				if err != nil {
					t.Fatal(err)
				}
				// The cancel's arguments are the job the start made, read from the honest store; from then on the store lies.
				args := func(call jobCall) string {
					if call.args != "the job started" {
						return call.args
					}
					page, err := runcontext.ListJobs(context.Background(), l.store, jobRun, "", 10)
					if err != nil || len(page.Jobs) != 1 {
						return "{}"
					}
					armed.Store(true)
					return cancelArgs(page.Jobs[0].Job)
				}
				result := drive(t, view, engine, p.tools(), []jobCall{{"runner__start_job", jobArgs}, {"runner__cancel_job", "the job started"}}, options{args: args})
				if !armed.Load() || p.sent.Load() != 0 || !strings.Contains(lastToolResult(t, result), "this cancel was not sent") {
					t.Fatal(armed.Load(), p.sent.Load(), lastToolResult(t, result))
				}
			})
		}
	}
}

// F-3: cancel is handed the identity it is admitted under: its row's, or the abandon's operation.
func TestJobsACancelIsHandedItsOperation(t *testing.T) {
	ctx := context.Background()
	for _, engine := range engines {
		t.Run(engine, func(t *testing.T) {
			p := pairOf(t, "runner", nil, nil, "allow", jobKey)
			l, job := startedPair(t, engine, p)
			drive(t, l.view, engine, p.tools(), []jobCall{{"runner__cancel_job", cancelArgs(job.Job)}}, options{resume: true})
			held := onlyJob(t, l.store)
			if len(*p.inputs) != 1 || held.Cancel == nil || (*p.inputs)[0].Operation != held.Cancel.Requested.Operation || (*p.inputs)[0].Operation == "" {
				t.Fatalf("%+v %+v", *p.inputs, held.Cancel)
			}
		})
	}
	p := pairOf(t, "runner", nil, nil, "allow", jobKey)
	l, job := startedPair(t, "eino", p)
	inputs := []runcontext.JobCancelInput{}
	if _, err := runcontext.AbandonJobs(ctx, l.store, l.host, jobRun, "cmd-op", jobHour, func(_ context.Context, input runcontext.JobCancelInput) error {
		inputs = append(inputs, input)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(inputs) != 1 || inputs[0].Operation != runcontext.JobCancelOperation("cmd-op", job.Job) {
		t.Fatalf("%+v", inputs)
	}
}

// admissionSpy records each admission's operation.
type admissionSpy struct {
	inner *runcontext.ReferenceEffectHost
	seen  *[]runcontext.EffectOperation
}

func (h admissionSpy) EffectFencing() string { return h.inner.EffectFencing() }
func (h admissionSpy) Dispatch(ctx context.Context, admission runcontext.EffectAdmission, perform func(context.Context) error) error {
	*h.seen = append(*h.seen, admission.Operation)
	return h.inner.Dispatch(ctx, admission, perform)
}

// F-14: an abandon's cancel is admitted as the abandon's, not as its start.
func TestJobsAnAbandonsCancelIsAdmittedAsTheAbandons(t *testing.T) {
	ctx := context.Background()
	p := pairOf(t, "runner", nil, nil, "allow", jobKey)
	l, job := startedPair(t, "eino", p)
	seen := []runcontext.EffectOperation{}
	certified, err := runcontext.CertifyEffectHost(ctx, func(authority runcontext.LeaseAuthority) runcontext.EffectHost {
		return admissionSpy{inner: runcontext.NewReferenceEffectHost(authority), seen: &seen}
	}, l.store)
	if err != nil {
		t.Fatal(err)
	}
	seen = seen[:0]
	if _, err := runcontext.AbandonJobs(ctx, l.store, certified, jobRun, "cmd-name", jobHour, func(context.Context, runcontext.JobCancelInput) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || seen[0].Tool != runcontext.JobAbandonOwner || seen[0].Source != job.Source || seen[0].Operation != runcontext.JobCancelOperation("cmd-name", job.Job) {
		t.Fatalf("%+v", seen)
	}
}

// F-13: a contradicted job is called contradicted by the cancel tool, as the abandon calls it.
func TestJobsAContradictedJobIsCalledContradictedByBoth(t *testing.T) {
	ctx := context.Background()
	for _, engine := range engines {
		t.Run(engine, func(t *testing.T) {
			p := pairOf(t, "runner", answered(runcontext.JobStartAnswer{Started: false}, nil), nil, "allow", jobKey)
			l, job := startedPair(t, engine, p)
			if _, err := runcontext.CompleteJob(ctx, l.store, completion(job.Job, nil)); err != nil {
				t.Fatal(err)
			}
			result := drive(t, l.view, engine, p.tools(), []jobCall{{"runner__cancel_job", cancelArgs(job.Job)}}, options{resume: true})
			if !strings.Contains(toolAnswer(result), "JobRefused: job-contradicted") || p.sent.Load() != 0 {
				t.Fatal(toolAnswer(result))
			}
			answer, err := runcontext.AbandonJobs(ctx, l.store, l.host, jobRun, "cmd-c", jobHour, func(context.Context, runcontext.JobCancelInput) error { return nil })
			if err != nil || len(answer.Jobs) != 1 || answer.Jobs[0].Delivery != "contradicted" {
				t.Fatalf("%+v %v", answer, err)
			}
		})
	}
}

// F-5: a found start's long handle is kept as its digest and marked, never sent as the reference, and its late
// answer agrees.
func TestJobsAFoundStartsLongHandleIsKeptAsItsMarkedDigest(t *testing.T) {
	ctx := context.Background()
	long := strings.Repeat("L", 300)
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(long)))
	p := pairOf(t, "runner", answered(runcontext.JobStartAnswer{}, errors.New("lost")), nil, "allow", jobKey)
	l, job := startedPair(t, "eino", p)
	if job.State != "unproven" {
		t.Fatalf("%+v", job)
	}
	found, err := runcontext.ReconcileJob(ctx, l.store, runcontext.JobReconciliation{Version: 1, Resource: jobRun, Job: job.Job, Generation: 1, Evidence: "find-1", Finding: "started", Handle: &long, Key: jobKey})
	if err != nil || found.State != "started" || found.Handle == nil || *found.Handle != digest || found.HandleDigested == nil || !*found.HandleDigested {
		t.Fatalf("%+v %v", found, err)
	}
	inputs := []runcontext.JobCancelInput{}
	if _, err := runcontext.AbandonJobs(ctx, l.store, l.host, jobRun, "cmd-f5", jobHour, func(_ context.Context, input runcontext.JobCancelInput) error {
		inputs = append(inputs, input)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(inputs) != 1 || inputs[0].Handle != nil || inputs[0].HandleDigest == nil || *inputs[0].HandleDigest != digest {
		t.Fatalf("%+v", inputs)
	}
	// The start's own late answer of the same bytes reads the same: no conflict.
	marked := true
	late, err := l.store.AnswerJob(ctx, runcontext.JobAnswer{Version: 1, Resource: jobRun, Job: job.Job, Generation: 1, Answer: "started", Handle: &digest, HandleDigested: &marked}, l.fence)
	if err != nil || late.HandleDigested == nil || !*late.HandleDigested {
		t.Fatalf("%+v %v", late, err)
	}
}

// F-9, F-10: an abandon the store refuses moves no lease, and one on a run it has never seen takes none.
func TestJobsAnAbandonRefusedOrOfNoRunMovesNoLease(t *testing.T) {
	ctx := context.Background()
	l := newLeased(t, nil)
	before, _ := l.store.ReadLease(ctx, jobRun)
	if _, err := l.store.AbandonJobs(ctx, runcontext.JobAbandon{Resource: jobRun, Command: "", DurationMs: jobHour}); !refusedAs(err, "invalid") {
		t.Fatal(err)
	}
	if after, _ := l.store.ReadLease(ctx, jobRun); !reflect.DeepEqual(after, before) {
		t.Fatalf("%+v %+v", after, before)
	}
	unseen, err := l.store.AbandonJobs(ctx, runcontext.JobAbandon{Resource: "run-never", Command: "cmd-1", DurationMs: jobHour})
	if err != nil || unseen.Lease != nil || len(unseen.Jobs) != 0 {
		t.Fatalf("%+v %v", unseen, err)
	}
	if held, _ := l.store.ReadLease(ctx, "run-never"); held != nil {
		t.Fatalf("%+v", held)
	}
	if _, err := l.store.AcquireLease(ctx, runcontext.LeaseAcquire{Resource: "run-never", Owner: "worker-a", DurationMs: jobHour}); err != nil {
		t.Fatal(err)
	}
}

// F-11: a head restored from before a cancel's row is refused before anything is offered: the cancel would be
// asked, and sent, again.
func TestJobsAHeadRestoredFromBeforeACancelsRowIsRefused(t *testing.T) {
	ctx := context.Background()
	for _, engine := range engines {
		t.Run(engine, func(t *testing.T) {
			p := pairOf(t, "runner", nil, nil, "allow", jobKey)
			l, job := startedPair(t, engine, p)
			before, _ := l.store.Load(ctx, jobRun)
			drive(t, l.view, engine, p.tools(), []jobCall{{"runner__cancel_job", cancelArgs(job.Job)}}, options{resume: true})
			if p.sent.Load() != 1 {
				t.Fatal(p.sent.Load())
			}
			now, _ := l.store.Load(ctx, jobRun)
			if _, err := l.store.CommitFenced(ctx, runcontext.RunCommit{RunID: jobRun, ExpectedRevision: now.Revision, CommitID: "restored", Checkpoint: before.Checkpoint, Events: [][]byte{}}, l.fence); err != nil {
				t.Fatal(err)
			}
			repeat := drive(t, l.view, engine, p.tools(), []jobCall{{"runner__cancel_job", cancelArgs(job.Job)}}, options{resume: true})
			if !strings.Contains(fmt.Sprint(repeat.err)+toolAnswer(repeat), "store-inconsistent") || p.sent.Load() != 1 || repeat.asks.Load() != 0 {
				t.Fatal(repeat.err, toolAnswer(repeat), p.sent.Load(), repeat.asks.Load())
			}
		})
	}
}

// R-7: a malformed job gives no gateway key, as TypeScript's reader refuses it.
func TestJobsAMalformedJobGivesNoGatewayKey(t *testing.T) {
	p := pairOf(t, "runner", nil, nil, "allow", jobKey)
	_, job := startedPair(t, "eino", p)
	if _, err := runcontext.GatewayActionMembers(job, false); err != nil {
		t.Fatal(err)
	}
	job.KeyDigest = "not-a-digest"
	if members, err := runcontext.GatewayActionMembers(job, false); err == nil {
		t.Fatalf("%+v", members)
	}
}

// plainHostTool is an ordinary host tool of no job.
func plainHostTool() runcontext.HostTool {
	schema := json.RawMessage(`{"type":"object"}`)
	taxonomy := &runcontext.ToolTaxonomy{Operation: "run", Effect: "act"}
	digest := runcontext.ToolListDigest([]runcontext.ListedTool{{Name: "plain", Description: "Plain.", InputSchema: schema, Taxonomy: taxonomy}})
	return runcontext.HostTool{Name: "plain", Description: "Plain.", InputSchema: schema, Taxonomy: taxonomy, EffectClass: "act", Approval: "required",
		Source:  &runcontext.ToolSource{Kind: "custom", ID: "runner", Trust: "person", Version: "1", ToolListDigest: digest, CurrentDigest: func(context.Context) (string, error) { return digest, nil }},
		Approve: func(runcontext.ApprovalRequest) string { return "allow" },
		Execute: func(context.Context, json.RawMessage) (runcontext.ToolResult, error) {
			return runcontext.ToolResult{Content: []runcontext.Content{runcontext.TextContent("ok")}}, nil
		}}
}

// R-5: a nested run on the context a mint, an authorize or an effect host's dispatch was handed is no start
// itself, and on native nothing panics. The nested run calls an ordinary host tool: its row is no start either.
func TestJobsANestedRunInAMintAnAuthorizeOrADispatchRegistersNothing(t *testing.T) {
	for _, engine := range engines {
		for _, where := range []string{"mint", "authorize", "dispatch"} {
			t.Run(engine+"/"+where, func(t *testing.T) {
				l, nested := newLeased(t, nil), newLeased(t, nil)
				var once atomic.Bool
				var nestedErr error
				nest := func(ctx context.Context) {
					if once.CompareAndSwap(false, true) {
						nestedErr = drive(t, nested.view, engine, []runcontext.HostTool{plainHostTool()}, []jobCall{{"start", jobArgs}}, options{ctx: ctx}).err
					}
				}
				mint := func(ctx context.Context, _ string) (string, error) {
					if where == "mint" {
						nest(ctx)
					}
					return runcontext.JobKeyDigest(jobKey)
				}
				tool := startTool(t, &startCalls{}, answered(startedAnswer, nil), "allow", mint)
				view := l.view
				if where == "dispatch" {
					view = certifiedView(t, l, func(inner *runcontext.ReferenceEffectHost) runcontext.EffectHost {
						return nestingHost{inner: inner, nest: nest, store: l.store}
					})
				}
				with := options{}
				if where == "authorize" {
					with.authorize = func(ctx context.Context) error {
						// The start's own admission: its job is registered and not yet answered.
						if page, err := runcontext.ListJobs(ctx, l.store, jobRun, "", 10); err == nil && len(page.Jobs) == 1 && page.Jobs[0].State == "unproven" {
							nest(ctx)
						}
						return nil
					}
				}
				drive(t, view, engine, []runcontext.HostTool{tool}, []jobCall{{"start", jobArgs}}, with)
				if job := onlyJob(t, l.store); job.State != "started" || !once.Load() || nestedErr != nil {
					t.Fatalf("%+v %v %v", job, once.Load(), nestedErr)
				}
				page, err := runcontext.ListJobs(context.Background(), nested.store, jobRun, "", 10)
				if err != nil || len(page.Jobs) != 0 {
					t.Fatalf("%+v %v", page, err)
				}
			})
		}
	}
}

// nestingHost runs a nested run on the context its dispatch is handed, once a job is registered.
type nestingHost struct {
	inner *runcontext.ReferenceEffectHost
	nest  func(context.Context)
	store runcontext.RunStore
}

func (h nestingHost) EffectFencing() string { return h.inner.EffectFencing() }
func (h nestingHost) Dispatch(ctx context.Context, admission runcontext.EffectAdmission, perform func(context.Context) error) error {
	if held, err := runcontext.ReadJob(ctx, h.store, jobRun, runcontext.JobIdentity(jobRun, admission.Operation.Operation)); err == nil && held != nil {
		h.nest(ctx)
	}
	return h.inner.Dispatch(ctx, admission, perform)
}

// failingHost fails a dispatch before anything is performed, while on: with err, or a plain error.
type failingHost struct {
	inner *runcontext.ReferenceEffectHost
	on    *atomic.Bool
	err   error
}

func (h failingHost) EffectFencing() string { return h.inner.EffectFencing() }
func (h failingHost) Dispatch(ctx context.Context, admission runcontext.EffectAdmission, perform func(context.Context) error) error {
	if h.on.Load() {
		if h.err != nil {
			return h.err
		}
		return errors.New("the authority is down")
	}
	return h.inner.Dispatch(ctx, admission, perform)
}

func TestJobsACancelThatNeverLeftSaysItWasNotSent(t *testing.T) {
	ctx := context.Background()
	for _, engine := range engines {
		t.Run(engine+"/an acknowledgement lost after requested", func(t *testing.T) {
			p := pairOf(t, "runner", nil, nil, "allow", jobKey)
			l, job := startedPair(t, engine, p)
			lost := &atomic.Bool{}
			lost.Store(true)
			store := askLost{l.store, lost}
			view, err := runcontext.UnattendedRunStore(store, l.fence, runcontext.NewReferenceEffectHost(store))
			if err != nil {
				t.Fatal(err)
			}
			first := drive(t, view, engine, p.tools(), []jobCall{{"runner__cancel_job", cancelArgs(job.Job)}}, options{resume: true})
			held := onlyJob(t, l.store)
			if p.sent.Load() != 0 || held.Cancel == nil || !strings.Contains(lastToolResult(t, first), "this cancel was not sent") {
				t.Fatal(p.sent.Load(), held.Cancel, lastToolResult(t, first))
			}
			repeat := drive(t, view, engine, p.tools(), []jobCall{{"runner__cancel_job", cancelArgs(job.Job)}}, options{resume: true})
			if p.sent.Load() != 0 || !strings.Contains(lastToolResult(t, repeat), "may not have been delivered") {
				t.Fatal(p.sent.Load(), lastToolResult(t, repeat))
			}
		})
		t.Run(engine+"/an effect host that fails", func(t *testing.T) {
			p := pairOf(t, "runner", nil, nil, "allow", jobKey)
			l := newLeased(t, nil)
			on := &atomic.Bool{}
			certified, err := runcontext.CertifyEffectHost(ctx, func(authority runcontext.LeaseAuthority) runcontext.EffectHost {
				return failingHost{inner: runcontext.NewReferenceEffectHost(authority), on: on}
			}, l.store)
			if err != nil {
				t.Fatal(err)
			}
			view, err := runcontext.FencedRunStore(l.store, l.fence, certified)
			if err != nil {
				t.Fatal(err)
			}
			drive(t, view, engine, p.tools(), []jobCall{{"runner__start_job", jobArgs}}, options{})
			job := onlyJob(t, l.store)
			on.Store(true)
			result := drive(t, view, engine, p.tools(), []jobCall{{"runner__cancel_job", cancelArgs(job.Job)}}, options{resume: true})
			held := onlyJob(t, l.store)
			if p.sent.Load() != 0 || held.Cancel == nil || !strings.Contains(lastToolResult(t, result), "this cancel was not sent") {
				t.Fatal(p.sent.Load(), held.Cancel, lastToolResult(t, result))
			}
		})
		// The run's own reads inside the dispatch, before the cancel is handed over: a delegation's counted read
		// is one, and the host's Authorize is another at the same point. Go's job tests have no delegated driver,
		// so Authorize stands in for it here; TypeScript drives the delegation itself.
		t.Run(engine+"/a refusal inside the dispatch", func(t *testing.T) {
			p := pairOf(t, "runner", nil, nil, "allow", jobKey)
			l, job := startedPair(t, engine, p)
			// It admits the run's opening and refuses once the cancel is asked for: the point a counted read is at.
			refuse := func(ctx context.Context) error {
				if held, err := runcontext.ReadJob(ctx, l.store, jobRun, job.Job); err == nil && held != nil && held.Cancel != nil {
					return failure.Authored("permission", "revoked", "context")
				}
				return nil
			}
			result := drive(t, l.view, engine, p.tools(), []jobCall{{"runner__cancel_job", cancelArgs(job.Job)}}, options{resume: true, authorize: refuse})
			held := onlyJob(t, l.store)
			if p.sent.Load() != 0 || held.Cancel == nil || !strings.Contains(lastToolResult(t, result), "this cancel was not sent") {
				t.Fatal(p.sent.Load(), held.Cancel, lastToolResult(t, result))
			}
		})
	}
}

// otherJobAnswer records the cancel and answers another job's record.
type otherJobAnswer struct {
	durableJobStore
	other *string
}

func (s otherJobAnswer) RequestJobCancel(ctx context.Context, ask runcontext.JobCancelAsk, fence runcontext.LeaseFence) (runcontext.RunJob, error) {
	if _, err := s.durableJobStore.RequestJobCancel(ctx, ask, fence); err != nil {
		return runcontext.RunJob{}, err
	}
	held, err := s.durableJobStore.ReadJob(ctx, ask.Resource, *s.other)
	if err != nil || held == nil {
		return runcontext.RunJob{}, err
	}
	return *held, nil
}

func TestJobsWhatIsSentIsTheRecordOfTheJobAskedFor(t *testing.T) {
	for _, engine := range engines {
		t.Run(engine, func(t *testing.T) {
			p := pairOf(t, "runner", nil, nil, "allow", jobKey)
			l := newLeased(t, nil)
			drive(t, l.view, engine, p.tools(), []jobCall{{"runner__start_job", `{"name":"one"}`}}, options{})
			page, _ := runcontext.ListJobs(context.Background(), l.store, jobRun, "", 10)
			first := page.Jobs[0].Job
			second := pairOf(t, "runner", nil, nil, "allow", "job.key.0002.Hs8Pq3Lz6Wd1Rm9Tc4Vb7Nx2Ka5U")
			drive(t, l.view, engine, second.tools(), []jobCall{{"runner__start_job", `{"name":"two"}`}}, options{resume: true})
			page, _ = runcontext.ListJobs(context.Background(), l.store, jobRun, "", 10)
			if len(page.Jobs) != 2 {
				t.Fatalf("%+v", page.Jobs)
			}
			other := page.Jobs[1].Job
			store := otherJobAnswer{l.store, &other}
			view, err := runcontext.UnattendedRunStore(store, l.fence, runcontext.NewReferenceEffectHost(store))
			if err != nil {
				t.Fatal(err)
			}
			result := drive(t, view, engine, p.tools(), []jobCall{{"runner__cancel_job", cancelArgs(first)}}, options{resume: true})
			if p.sent.Load() != 0 || !strings.Contains(lastToolResult(t, result), "this cancel was not sent") {
				t.Fatal(p.sent.Load(), lastToolResult(t, result))
			}
		})
	}
}

// phaseOf is the run's phase: work under an accepted Plan, none without one.
func phaseOf(plan *runcontext.Value) string {
	if plan == nil {
		return ""
	}
	return "work"
}

// durableGoalJobStore is the memory kind held as a durable one, with the goal port's lifetime declared too.
type durableGoalJobStore struct{ durableJobStore }

func (durableGoalJobStore) GoalLifetime() string { return runcontext.GoalLifetimeDurable }

// #313 (C10's twin): a start cited as a required step's evidence proves it only as its job reads now. Through the
// real eino adapter: the model starts a job and closes its step with the start's row; the objective binds that
// step; the achievement is accepted once the job succeeded and refused once it failed.
func TestJobsAStartCitedAsEvidenceProvesItsStepOnlyAsItsJobReadsNow(t *testing.T) {
	plan := runcontext.ObjectOf("id", runcontext.String("accepted-plan"), "revision", runcontext.Number(1), "objective", runcontext.String("Run and report"), "steps", runcontext.Array(runcontext.String("Run")), "questions", runcontext.Array(), "references", runcontext.Array(), "status", runcontext.String("accepted"), "createdAt", runcontext.String("2026-01-01T00:00:00.000Z"), "acceptedAt", runcontext.String("2026-01-01T00:00:00.000Z"))
	for _, outcome := range []string{"succeeded", "failed"} {
		t.Run(outcome, func(t *testing.T) {
			ctx := context.Background()
			clock := &jobClock{at: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
			store := durableGoalJobStore{durableJobStore{runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{Now: clock.now})}}
			lease, err := store.AcquireLease(ctx, runcontext.LeaseAcquire{Resource: jobRun, Owner: "worker-a", DurationMs: jobHour})
			if err != nil {
				t.Fatal(err)
			}
			view, err := runcontext.UnattendedRunStore(store, runcontext.FenceOf(lease), runcontext.NewReferenceEffectHost(store))
			if err != nil {
				t.Fatal(err)
			}
			tool := startTool(t, &startCalls{}, answered(startedAnswer, nil), "allow", nil)
			drive(t, view, "eino", []runcontext.HostTool{tool}, []jobCall{{"update_plan", `{"explanation":"work","plan":[{"id":"step-1","step":"Run","status":"in_progress"}]}`}, {"start", jobArgs}}, options{plan: plan})
			job := onlyJob(t, store)
			var saved struct {
				PlanState struct {
					Revision string `json:"revision"`
				} `json:"planState"`
			}
			head, _ := store.Load(ctx, jobRun)
			if err := json.Unmarshal(head.Checkpoint, &saved); err != nil || saved.PlanState.Revision == "" {
				t.Fatal(err, string(head.Checkpoint))
			}
			goal := func(kind, id string, revision int64, edit func(*runcontext.GoalCommand)) runcontext.GoalCommand {
				c := runcontext.GoalCommand{Version: 1, Resource: jobRun, Command: id, Actor: "person-1", Kind: kind}
				if revision == 0 {
					c.ExpectedRevision.Null = true
				} else {
					c.ExpectedRevision.Value = revision
				}
				if edit != nil {
					edit(&c)
				}
				return c
			}
			created, err := runcontext.RunGoalCommand(ctx, store, goal("create", "cmd-1", 0, func(c *runcontext.GoalCommand) { objective := "objective-1"; c.Objective = &objective }))
			if err != nil {
				t.Fatal(err)
			}
			// The objective binds the open step; the step is closed after.
			at := runcontext.GoalHead{Revision: head.Revision, Digest: runcontext.CheckpointBytesDigest(head.Checkpoint)}
			bound, err := runcontext.RunGoalCommand(ctx, store, goal("bind", "cmd-2", created.Goal.Revision, func(c *runcontext.GoalCommand) { c.Steps = []string{"step-1"}; c.Head = &at }))
			if err != nil {
				t.Fatal(err)
			}
			// The step is closed by the start's row once its job has succeeded, as complete_plan_step requires.
			if _, err := runcontext.CompleteJob(ctx, store, completion(job.Job, nil)); err != nil {
				t.Fatal(err)
			}
			closed := drive(t, view, "eino", []runcontext.HostTool{tool}, []jobCall{{"complete_plan_step", `{"id":"step-1","expectedRevision":"` + saved.PlanState.Revision + `","outcome":"completed","evidence":{"operations":["` + job.Operation + `"],"artifacts":[]}}`}}, options{plan: plan, resume: true})
			if strings.Contains(lastToolResult(t, closed), `"isError":true`) {
				t.Fatal(lastToolResult(t, closed))
			}
			head, _ = store.Load(ctx, jobRun)
			at = runcontext.GoalHead{Revision: head.Revision, Digest: runcontext.CheckpointBytesDigest(head.Checkpoint)}
			// The job as it reads now: a store whose record says, since, that it failed.
			var reads runcontext.RunStore = store
			if outcome == "failed" {
				reads = jobsSayFailed{store, job.Job}
			}
			done, err := runcontext.RunGoalCommand(ctx, reads, goal("achieve", "cmd-3", bound.Goal.Revision, func(c *runcontext.GoalCommand) { c.Head = &at }))
			var refused *runcontext.GoalRefused
			if outcome == "succeeded" && (err != nil || done.Goal.Objectives[0].State != "achieved") {
				t.Fatal(err, done.Goal.Objectives)
			}
			if outcome == "failed" && (!errors.As(err, &refused) || refused.Reason != "evidence-refused") {
				t.Fatal(err)
			}
		})
	}
}

// jobsSayFailed lists the job as completed with failed.
type jobsSayFailed struct {
	durableGoalJobStore
	job string
}

func (s jobsSayFailed) ListJobs(ctx context.Context, resource, after string, limit int) (runcontext.JobPage, error) {
	page, err := s.durableGoalJobStore.ListJobs(ctx, resource, after, limit)
	for i := range page.Jobs {
		if page.Jobs[i].Job == s.job && page.Jobs[i].Completion != nil {
			completion := *page.Jobs[i].Completion
			completion.Outcome = "failed"
			page.Jobs[i].Completion = &completion
		}
	}
	return page, err
}
