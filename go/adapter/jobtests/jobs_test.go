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
	resume       bool
	decision     *contract.CheckpointApprovalDecision
	continuation *runcontext.RunJob
}

// drive runs the tools: the model or the agent makes each call, then ends.
func drive(t *testing.T, store runcontext.RunStore, engine string, tools []runcontext.HostTool, calls []jobCall, with options) *runResult {
	t.Helper()
	out := &runResult{}
	var mu sync.Mutex
	g := gate.New(gate.Options{})
	g.SetOffer(contract.ToolOffer{})
	start := ""
	run := &adapter.Run{Prompt: "Run the job.", Wire: "openai-compatible", ModelName: "fixture", Thinking: "off", Critic: "off", AllowConversation: true, Gate: g.Calls(), HostTools: tools,
		Context: &runcontext.Input{RequestCap: with.requestCap, CurrentRequest: &runcontext.CurrentRequest{Message: "Run the job.", Input: "job"}, RunStore: &runcontext.DurableRunInput{Store: store, ID: jobRun, Resume: with.resume}, Approval: with.decision, JobContinuation: with.continuation},
		Emit: func(event contract.RunEvent) {
			mu.Lock()
			out.events = append(out.events, event)
			mu.Unlock()
		},
		EnrollContext: func(_ context.Context, offer adapter.ContextOffer) error {
			for _, held := range offer.HostTools {
				if held.Taxonomy.Operation == "run" {
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
				answer, err := cb.Tool(ctx, adapter.AgentToolCall{ID: fmt.Sprintf("native-call-%d", i), Name: name, Arguments: json.RawMessage(call.args)})
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
				return modelCall(wireOf(calls[seen-1]), fmt.Sprintf("model-call-%d-%d", time.Now().UnixNano(), seen), calls[seen-1].args), nil
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
	counting := failsOnFirstList{durableJobStore: l.store, fail: func() {
		if _, err := runcontext.CompleteJob(ctx, l.store, completion(job.Job, func(e *runcontext.JobCompletionEvidence) { e.Outcome = outcome })); err != nil {
			t.Fatal(err)
		}
	}, seen: &atomic.Int32{}}
	view, err := runcontext.FencedRunStore(counting, l.fence, l.host)
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

func TestJobsAHandleTheRecordCannotHoldIsKeptAsItsDigest(t *testing.T) {
	for _, engine := range engines {
		t.Run(engine, func(t *testing.T) {
			l := newLeased(t, nil)
			long, receipt := strings.Repeat("h", 300), strings.Repeat("r", 300)
			tool := startTool(t, &startCalls{}, answered(runcontext.JobStartAnswer{Started: true, Handle: long, Receipt: receipt}, nil), "allow", nil)
			result := drive(t, l.view, engine, []runcontext.HostTool{tool}, []jobCall{{"start", jobArgs}}, options{})
			job := onlyJob(t, l.store)
			sum := func(text string) string { return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(text))) }
			if job.State != "started" || job.Handle == nil || *job.Handle != sum(long) || job.Receipt == nil || *job.Receipt != sum(receipt) {
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
	l := newLeased(t, nil)
	calls := &startCalls{}
	tool := startTool(t, calls, answered(startedAnswer, nil), "allow", nil)
	drive(t, l.view, "eino", []runcontext.HostTool{tool}, nil, options{})
	head, err := l.store.Load(ctx, jobRun)
	if err != nil || head == nil {
		t.Fatal(head, err)
	}
	key, _ := runcontext.JobKeyDigest(jobKey)
	if _, err := l.store.CommitFenced(ctx, runcontext.RunCommit{RunID: jobRun, ExpectedRevision: head.Revision, CommitID: "restored", Checkpoint: head.Checkpoint, Events: [][]byte{}, Job: &runcontext.JobRegistration{Job: runcontext.JobIdentity(jobRun, "op-restored-away"), Operation: "op-restored-away", Source: "runner", Tool: "start_job", RequestDigest: "sha256:" + strings.Repeat("a", 64), KeyDigest: key}}, l.fence); err != nil {
		t.Fatal(err)
	}
	for _, engine := range engines {
		t.Run(engine, func(t *testing.T) {
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
