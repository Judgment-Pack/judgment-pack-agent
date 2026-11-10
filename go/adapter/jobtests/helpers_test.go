package jobtests

import (
	"context"
	"encoding/json"
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

// #229: run-owned jobs through the real eino and agent adapters, the package's writer and the reference effect
// host. The TypeScript twin is packages/harness/storage/job.test.ts. These have their own binaries: the adapter's
// is near its limit, and one binary of them outgrew its race leg (#364), so they are two packages, jobtests and
// jobsendtests (what a cancel or an abandon sends through the effect host). A test binary cannot import another's
// test files, so this file is copied: helpers_test.go is the same in both packages but for the package clause.
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

// declaredOnly says the fence is enforced and checks nothing.
type declaredOnly struct{}

func (declaredOnly) EffectFencing() string { return runcontext.EffectFencingEnforced }
func (declaredOnly) Dispatch(ctx context.Context, _ runcontext.EffectAdmission, perform func(context.Context) error) error {
	return perform(ctx)
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

// phaseOf is the run's phase: work under an accepted Plan, none without one.
func phaseOf(plan *runcontext.Value) string {
	if plan == nil {
		return ""
	}
	return "work"
}
