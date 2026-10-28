package runcontext

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/wire"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"io"
	"net/http"
	"strings"
	"testing"
)

func task183Input(t *testing.T, store RunStore, native bool, envelope *TaskStepEnvelope, resume bool) Session {
	t.Helper()
	session, _ := trackedSession(t)
	session.Context.TaskSteps = true
	session.Context.TaskStepEnvelope = envelope
	session.Context.RunStore = &DurableRunInput{Store: store, ID: "task-steps-183", Resume: resume}
	if native {
		session.Model = nil
		session.Agent = &AgentSession{Model: "native"}
	} else {
		limit := 4
		session.Context.RequestCap = &limit
	}
	return session
}
func task183Update(t *testing.T, run *Run, plan string) error {
	t.Helper()
	_, err := hostTool(t, run, "update_plan").Execute(context.Background(), []byte(`{"explanation":"Work in order.","plan":`+plan+`}`))
	return err
}
func task183Saved(t *testing.T, store RunStore) *Value {
	t.Helper()
	head, err := store.Load(context.Background(), "task-steps-183")
	if err != nil || head == nil {
		t.Fatal(err)
	}
	return MustParseJSON(string(head.Checkpoint))
}
func task183Model(run *Run, calls *int) error {
	ctx := context.Background()
	body := []byte(`{"model":"fixture","messages":[{"role":"user","content":"Work"}]}`)
	if err := run.Context.ClaimRequest(ctx, body); err != nil {
		return err
	}
	request := []byte(`{"method":"POST","body":` + string(mustMarshal183(string(body))) + `}`)
	_, err := InvokeDurableModel(ctx, run.Context.Persistence("author"), "source", "fixture", request, func([]byte) (*http.Response, error) {
		*calls++
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"id":"answer","object":"chat.completion","model":"fixture","choices":[{"index":0,"message":{"role":"assistant","content":"Done."},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1}}`))}, nil
	}, func(response *http.Response) error {
		return wire.QuarantineDurableResponse(ctx, wire.OpenAICompatible, response)
	})
	return err
}
func mustMarshal183(value any) []byte { raw, _ := json.Marshal(value); return raw }
func task183Native(run *Run, calls *int) error {
	_, err := InvokeDurable(context.Background(), run.Context.Persistence("author"), "agent", "source", "native", []byte(`{"prompt":"Work"}`), true, func([]byte) (int, error) { *calls++; return 1, nil }, nil, nil)
	return err
}
func TestTaskSteps183ActualStoreAtomicModelAndResume(t *testing.T) {
	store := NewMemoryRunStore(MemoryRunStoreOptions{})
	run := openRun(t, task183Input(t, store, false, nil, false))
	calls := 0
	if err := task183Update(t, run, `[{"step":"First","status":"in_progress","budget":{"requests":1}},{"step":"Next","status":"pending","budget":{"requests":2}}]`); err != nil {
		t.Fatal(err)
	}
	if err := task183Model(run, &calls); err != nil {
		t.Fatal(err)
	}
	saved := task183Saved(t, store)
	claims := saved.Get("planState").Get("taskSchedule").Get("rows").Items()[0].Get("claims")
	if saved.Get("used").Num() != 1 || claims.Len() != 1 {
		t.Fatal("root and step were not acknowledged together")
	}
	id := planText(claims.Items()[0].Get("id"))
	matched := false
	for _, dispatch := range saved.Get("durable").Get("dispatches").Items() {
		if planText(dispatch.Get("id")) == id && planText(dispatch.Get("kind")) == "model" && planText(dispatch.Get("state")) == "result" && planText(dispatch.Get("requestDigest")) == planText(claims.Items()[0].Get("requestDigest")) {
			matched = true
		}
	}
	if !matched {
		t.Fatal("step claim lacks exact original physical identity")
	}
	if err := task183Model(run, &calls); !errors.Is(err, ErrExecutionPaused) || calls != 1 {
		t.Fatalf("share pause err=%v calls=%d", err, calls)
	}
	run.Context.Close()
	resumed := openRun(t, task183Input(t, store, false, nil, true))
	defer resumed.Context.Close()
	if err := task183Model(resumed, &calls); !errors.Is(err, ErrExecutionPaused) || calls != 1 {
		t.Fatalf("resume reset share err=%v calls=%d", err, calls)
	}
}
func TestTaskSteps183ActualStoreCycleAndUnready(t *testing.T) {
	store := NewMemoryRunStore(MemoryRunStoreOptions{})
	run := openRun(t, task183Input(t, store, false, nil, false))
	defer run.Context.Close()
	if err := task183Update(t, run, `[{"step":"First","status":"pending"},{"step":"Next","status":"pending"}]`); err != nil {
		t.Fatal(err)
	}
	before := CanonicalJSON(task183Saved(t, store).Get("plan"))
	for _, plan := range []string{`[{"id":"step-1","step":"First","status":"pending","dependsOn":["step-2"]},{"id":"step-2","step":"Next","status":"pending","dependsOn":["step-1"]}]`, `[{"id":"step-1","step":"First","status":"pending"},{"id":"step-2","step":"Next","status":"in_progress","dependsOn":["step-1"]}]`} {
		if err := task183Update(t, run, plan); err == nil {
			t.Fatal("invalid graph admitted")
		}
		if CanonicalJSON(task183Saved(t, store).Get("plan")) != before {
			t.Fatal("invalid graph changed acknowledged plan")
		}
	}
}
func TestTaskSteps183ActualStoreStandaloneHostEnvelope(t *testing.T) {
	store := NewMemoryRunStore(MemoryRunStoreOptions{})
	envelope := &TaskStepEnvelope{ID: "host-envelope", Revision: "sha256:" + strings.Repeat("a", 64), Unit: "provider-invocations", Limit: 2}
	calls := 0
	run := openRun(t, task183Input(t, store, true, envelope, false))
	if err := task183Native(run, &calls); err != nil {
		t.Fatal(err)
	}
	if err := task183Update(t, run, `[{"step":"Finish","status":"in_progress","budget":{"requests":1}}]`); err != nil {
		t.Fatal(err)
	}
	if err := task183Native(run, &calls); err != nil {
		t.Fatal(err)
	}
	if err := task183Native(run, &calls); !errors.Is(err, ErrExecutionPaused) || calls != 2 {
		t.Fatalf("envelope pause %v calls=%d", err, calls)
	}
	state := task183Saved(t, store)
	if state.Get("limit").Num() != 1 || state.Get("used").Num() != 0 || state.Get("planState").Get("taskSchedule").Get("rows").Items()[0].Get("claims").Len() != 1 {
		t.Fatal("native checkpoint semantics or pre-plan count changed")
	}
	run.Context.Close()
	for _, configuration := range []*TaskStepEnvelope{nil, {ID: envelope.ID, Revision: envelope.Revision, Unit: envelope.Unit, Limit: 3}, {ID: "foreign", Revision: envelope.Revision, Unit: envelope.Unit, Limit: 2}} {
		if ready, err := ContextualSession(context.Background(), task183Input(t, store, true, configuration, true)); err == nil {
			ready.Context.Close()
			t.Fatal("changed or missing configured envelope admitted")
		}
	}
	resumed := openRun(t, task183Input(t, store, true, envelope, true))
	defer resumed.Context.Close()
	if err := task183Native(resumed, &calls); !errors.Is(err, ErrExecutionPaused) || calls != 2 {
		t.Fatal("resume reset native envelope")
	}
}
func TestTaskSteps183ActualStoreNativeWithoutEnvelopeAddsNoCap(t *testing.T) {
	store := NewMemoryRunStore(MemoryRunStoreOptions{})
	run := openRun(t, task183Input(t, store, true, nil, false))
	defer run.Context.Close()
	calls := 0
	if err := task183Update(t, run, `[{"step":"Budget","status":"in_progress","budget":{"requests":1}}]`); err == nil {
		t.Fatal("budget without native envelope admitted")
	}
	if err := task183Update(t, run, `[{"step":"Dependency work","status":"in_progress"}]`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		if err := task183Native(run, &calls); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 6 {
		t.Fatal("standalone acquired an invented coordinator cap")
	}
}

func task183PackageSession(t *testing.T, store RunStore) Session {
	session := task183Input(t, store, false, nil, false)
	session.Subagents = []Subagent{subagentFixture(t)}
	session.HostTools = []HostTool{{Name: "lookup", Effect: "read", Taxonomy: &ToolTaxonomy{Operation: "read", Effect: "read"}, Description: "Read supplied evidence.", InputSchema: json.RawMessage(`{"type":"object"}`), Execute: func(context.Context, json.RawMessage) (ToolResult, error) { return ToolResult{}, nil }}}
	return session
}
func TestTaskSteps183ActualStoreRegisteredDelegation(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryRunStore(MemoryRunStoreOptions{})
	session := task183PackageSession(t, store)
	input, err := PrepareDurableInput(ctx, session.Context)
	if err != nil {
		t.Fatal(err)
	}
	session.Context = input
	binding := input.Persistence
	started := []*contract.DelegatedTask{}
	action, err := binding.Writer.TaskDelegationTool(*binding, func(ctx context.Context, request contract.DelegatedTaskRequest) (ToolResult, error) {
		tasks := []*contract.DelegatedTask{}
		for _, item := range request.Tasks {
			tasks = append(tasks, &contract.DelegatedTask{ID: newStorageIdentity(), ParentRunID: newStorageIdentity(), Title: item.Title, Objective: item.Objective, Profile: item.Profile, Subagent: item.Subagent, State: "queued", CreatedAt: "2026-10-26T00:00:00Z", Steps: []contract.DelegatedTaskStepsItem{}})
		}
		if err := binding.Writer.BindTaskDelegation(ctx, *binding, request, tasks); err != nil {
			return ToolResult{}, err
		}
		started = append(started, tasks...)
		return ToolResult{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	session.HostTools = append(session.HostTools, action)
	run := openRun(t, session)
	defer run.Context.Close()
	pin, _ := json.Marshal(SubagentRecord(run.Context.Subagents[0]))
	if err = task183Update(t, run, `[{"step":"Inspect first","status":"pending","delegation":`+string(pin)+`,"budget":{"requests":1}},{"step":"Inspect next","status":"pending","delegation":`+string(pin)+`,"budget":{"requests":1},"dependsOn":["step-1"]}]`); err != nil {
		t.Fatal(err)
	}
	revision := planText(task183Saved(t, store).Get("planState").Get("revision"))
	offered := hostTool(t, run, "delegate_plan_steps")
	for _, raw := range []string{`{"planRevision":"` + revision + `","steps":[]}`, `{"planRevision":"` + revision + `","steps":["step-1","step-1"]}`, `{"planRevision":"sha256:` + strings.Repeat("f", 64) + `","steps":["step-1"]}`, `{"planRevision":"` + revision + `","steps":["step-1","step-2"]}`} {
		if _, err = offered.Execute(ctx, []byte(raw)); err == nil {
			t.Fatal("invalid whole selection admitted")
		}
	}
	if len(started) != 0 || task183Saved(t, store).Get("planState").Get("taskSchedule").Has("pending") {
		t.Fatal("invalid batch created work or association")
	}
	if _, err = offered.Execute(ctx, []byte(`{"planRevision":"`+revision+`","steps":["step-1"]}`)); err != nil {
		t.Fatal(err)
	}
	schedule := task183Saved(t, store).Get("planState").Get("taskSchedule")
	child := schedule.Get("rows").Items()[0].Get("child")
	if len(started) != 1 || planText(child.Get("task")) != started[0].ID || planText(child.Get("execution")) != started[0].ID || planText(child.Get("planRevision")) != revision || CanonicalJSON(child.Get("profile")) != CanonicalJSON(MustParseJSON(string(pin))) || schedule.Has("pending") {
		t.Fatal("actual original-CAS child association missing")
	}
	copied := HostTool{Name: offered.Name, InputSchema: offered.InputSchema, Interaction: "delegation"}
	if _, registered, err := NormalizeTaskDelegation(ctx, copied, binding, []byte(`{}`)); registered || err != nil {
		t.Fatal("public name/schema granted source registration")
	}
}
func TestTaskSteps183ActualStoreSiblingShare(t *testing.T) {
	store := NewMemoryRunStore(MemoryRunStoreOptions{})
	run := openRun(t, task183Input(t, store, false, nil, false))
	defer run.Context.Close()
	calls := 0
	if err := task183Update(t, run, `[{"step":"Unbudgeted active","status":"in_progress"},{"step":"Protected sibling","status":"pending","budget":{"requests":3}}]`); err != nil {
		t.Fatal(err)
	}
	if err := task183Model(run, &calls); err != nil {
		t.Fatal(err)
	}
	if err := task183Model(run, &calls); !errors.Is(err, ErrExecutionPaused) || calls != 1 {
		t.Fatal("active step stole the sibling's held share", err, calls)
	}
}
func TestTaskSteps183ActualStoreLostClaimACK(t *testing.T) {
	memory := NewMemoryRunStore(MemoryRunStoreOptions{})
	lose := true
	store := writerReceiptStore{RunStore: memory, write: func(ctx context.Context, in RunCommit) (RunReceipt, error) {
		receipt, err := memory.Commit(ctx, in)
		if err == nil && lose && len(in.Checkpoint) > 0 {
			state := MustParseJSON(string(in.Checkpoint))
			for _, row := range state.Get("durable").Get("dispatches").Items() {
				if planText(row.Get("kind")) == "model" && planText(row.Get("state")) == "claimed" {
					lose = false
					return receipt, errors.New("fixture lost claim acknowledgement")
				}
			}
		}
		return receipt, err
	}}
	run := openRun(t, task183Input(t, store, false, nil, false))
	calls := 0
	if err := task183Update(t, run, `[{"step":"First","status":"in_progress","budget":{"requests":1}}]`); err != nil {
		t.Fatal(err)
	}
	if err := task183Model(run, &calls); err == nil || calls != 0 {
		t.Fatal("unacknowledged claim reached provider")
	}
	run.Context.Close()
	state := task183Saved(t, memory)
	if state.Get("used").Num() != 1 || state.Get("planState").Get("taskSchedule").Get("rows").Items()[0].Get("claims").Len() != 1 {
		t.Fatal("lost ACK discarded original held share")
	}
	resumed := openRun(t, task183Input(t, store, false, nil, true))
	defer resumed.Context.Close()
	if err := task183Model(resumed, &calls); err == nil || !strings.Contains(err.Error(), "HostOutcomeUnknown") || calls != 0 {
		t.Fatal("uncertain original claim redispatched", err, calls)
	}
}

func TestTaskSteps183CompleteChildCheckpointAloneRefused(t *testing.T) {
	for _, mode := range []string{"none", "all-refused", "unknown-plus-success"} {
		t.Run(mode, func(t *testing.T) { task183ChildProofRefused(t, mode) })
	}
}

func task183ChildProofRefused(t *testing.T, mode string) {
	ctx := context.Background()
	store := NewMemoryRunStore(MemoryRunStoreOptions{})
	session := task183PackageSession(t, store)
	input, err := PrepareDurableInput(ctx, session.Context)
	if err != nil {
		t.Fatal(err)
	}
	session.Context = input
	binding := input.Persistence
	var task *contract.DelegatedTask
	action, err := binding.Writer.TaskDelegationTool(*binding, func(ctx context.Context, request contract.DelegatedTaskRequest) (ToolResult, error) {
		item := request.Tasks[0]
		task = &contract.DelegatedTask{ID: newStorageIdentity(), ParentRunID: newStorageIdentity(), Title: item.Title, Objective: item.Objective, Profile: item.Profile, Subagent: item.Subagent, State: "queued", CreatedAt: "2026-10-26T00:00:00Z", Steps: []contract.DelegatedTaskStepsItem{}}
		err := binding.Writer.BindTaskDelegation(ctx, *binding, request, []*contract.DelegatedTask{task})
		return ToolResult{}, err
	})
	if err != nil {
		t.Fatal(err)
	}
	session.HostTools = append(session.HostTools, action)
	run := openRun(t, session)
	defer run.Context.Close()
	pin, _ := json.Marshal(SubagentRecord(run.Context.Subagents[0]))
	budget := "1"
	if mode == "unknown-plus-success" {
		budget = "2"
	}
	if err = task183Update(t, run, `[{"step":"Inspect","status":"pending","budget":{"requests":`+budget+`},"delegation":`+string(pin)+`}]`); err != nil {
		t.Fatal(err)
	}
	revision := planText(task183Saved(t, store).Get("planState").Get("revision"))
	if _, err = hostTool(t, run, "delegate_plan_steps").Execute(ctx, []byte(`{"planRevision":"`+revision+`","steps":["step-1"]}`)); err != nil {
		t.Fatal(err)
	}
	childSession, _ := trackedSession(t)
	childSession.Context = &Input{Objective: "Inspect", CurrentRequest: &CurrentRequest{Message: "Inspect", Input: "Inspect"}}
	ChildPersistence(session.Context, childSession.Context, task.ID)
	child := openRun(t, childSession)
	if mode == "all-refused" {
		request := []byte(`{"method":"POST","body":"{\"model\":\"fixture\",\"messages\":[{\"role\":\"user\",\"content\":\"Work\"}]}"}`)
		if _, err = InvokeDurableModel(ctx, child.Context.Persistence("author"), "source", "fixture", request, func([]byte) (*http.Response, error) {
			return &http.Response{StatusCode: 429, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"type":"rate_limit_error"}}`))}, nil
		}, func(response *http.Response) error {
			return wire.QuarantineDurableResponse(ctx, wire.OpenAICompatible, response)
		}); err != nil {
			t.Fatal(err)
		}
	} else if mode == "unknown-plus-success" {
		calls := 0
		if err = task183Model(child, &calls); err != nil {
			t.Fatal(err)
		}
	}
	checkpoint, err := child.Context.Snapshot("complete")
	if err != nil {
		t.Fatal(err)
	}
	if err = childSession.Context.Checkpoint(ctx, checkpoint); err != nil {
		t.Fatal(err)
	}
	if mode == "unknown-plus-success" {
		if _, err = InvokeDurableModel(ctx, child.Context.Persistence("author"), "source", "fixture", []byte(`{"method":"POST","body":"{}"}`), func([]byte) (*http.Response, error) { return nil, errors.New("fixture physical outcome unknown") }, func(*http.Response) error { return nil }); err == nil {
			t.Fatal("unknown physical accepted")
		}
	}
	child.Context.Close()
	task.State = "complete"
	result := "Claimed completion."
	task.Result = &result
	if err = json.Unmarshal([]byte(`{"summary":"Claimed completion.","findings":[],"citations":[],"openQuestions":[],"confidence":"high","notDone":[]}`), &task.Report); err != nil {
		t.Fatal(err)
	}
	if _, err = binding.Writer.CommitTaskResult(ctx, *binding, contract.RunEvent{DelegatedTask: &contract.RunEventDelegatedTask{Type: "delegated_task", Task: *task}}); err != nil {
		t.Fatal(err)
	}
	if task183Saved(t, store).Get("planState").Get("taskSchedule").Get("rows").Items()[0].Get("child").Has("result") || len(binding.Writer.TaskCompletionProof(*binding)) != 0 {
		t.Fatal("complete checkpoint without successful physical work established child proof")
	}
	args := ObjectOf("id", String("step-1"), "expectedRevision", String(revision), "outcome", String("completed"), "evidence", ObjectOf("operations", Array(), "artifacts", Array(), "child", ObjectOf("task", String(task.ID), "checkpointDigest", String("sha256:"+strings.Repeat("c", 64)), "resultDigest", String("sha256:"+strings.Repeat("d", 64)), "eventDigest", String("sha256:"+strings.Repeat("e", 64)), "commitId", String("caller-text"))))
	if _, err = hostTool(t, run, "complete_plan_step").Execute(ctx, args.JSON()); err == nil || mode != "unknown-plus-success" && !strings.Contains(err.Error(), "evidence-unavailable") {
		t.Fatal("caller text established completion", err)
	}
}

func TestTaskSteps183DisabledModePreservesLegacyBytes(t *testing.T) {
	store := NewMemoryRunStore(MemoryRunStoreOptions{})
	session := task183Input(t, store, false, nil, false)
	session.Context.TaskSteps = false
	run := openRun(t, session)
	defer run.Context.Close()
	plan := hostTool(t, run, "update_plan")
	schema := MustParseJSON(string(plan.InputSchema))
	properties := schema.Get("properties").Get("plan").Get("items").Get("properties")
	if properties.Has("dependsOn") || properties.Has("budget") || properties.Has("delegation") || strings.Contains(string(hostTool(t, run, "complete_plan_step").InputSchema), "checkpointDigest") {
		t.Fatal("task-enabled declaration leaked into disabled mode")
	}
	if err := task183Update(t, run, `[{"step":"First","status":"pending"}]`); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"model":"fixture","messages":[{"role":"user","content":"Work"}]}`)
	if err := run.Context.ClaimRequest(context.Background(), body); err != nil {
		t.Fatal(err)
	}
	request := []byte(`{"method":"POST","body":` + string(mustMarshal183(string(body))) + `}`)
	calls := 0
	_, err := InvokeDurableModel(context.Background(), run.Context.Persistence("author"), "source", "fixture", request, func(raw []byte) (*http.Response, error) {
		calls++
		if string(raw) != string(request) {
			t.Fatal("legacy provider request bytes changed")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"id":"answer","object":"chat.completion","model":"fixture","choices":[{"index":0,"message":{"role":"assistant","content":"Done."},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1}}`))}, nil
	}, func(response *http.Response) error {
		return wire.QuarantineDurableResponse(context.Background(), wire.OpenAICompatible, response)
	})
	if err != nil || calls != 1 {
		t.Fatal(err, calls)
	}
	state := task183Saved(t, store)
	if state.Get("version").Num() != 2 || state.Get("planState").Has("taskSchedule") || state.Get("planState").Has("taskEnvelope") {
		t.Fatal("disabled checkpoint changed")
	}
	page, err := store.ReadEvents(context.Background(), "task-steps-183", "", 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range page.Items {
		event := MustParseJSON(string(row.Bytes)).Get("event")
		if event.Get("type").Str() == "plan_revised" {
			found = true
			if event.Get("version").Num() != 1 {
				t.Fatal("disabled plan event changed")
			}
		}
	}
	if !found {
		t.Fatal("legacy revision event missing")
	}
}

func TestTaskSteps183ConfiguredDriftCandidate(t *testing.T) {
	store := NewMemoryRunStore(MemoryRunStoreOptions{})
	session := task183Input(t, store, false, nil, false)
	session.Context.Plan = MustParseJSON(`{"id":"12345678-1234-4234-8234-123456789abc","revision":1,"objective":"Inspect evidence.","steps":["Inspect source","Report evidence"],"questions":[],"references":[],"status":"accepted","createdAt":"2026-10-26T00:00:00Z","acceptedAt":"2026-10-26T00:00:00Z"}`)
	session.Context.Phase = "work"
	session.Context.PlanDrift = []string{"changed"}
	run := openRun(t, session)
	defer run.Context.Close()
	id := run.Context.Execution.State().Plan.Items()[0].Get("id").Str()
	if err := task183Update(t, run, `[{"id":"`+id+`","step":"Inspect revised evidence","status":"pending","budget":{"requests":1},"dependsOn":[]}]`); err != nil {
		t.Fatal(err)
	}
	state := task183Saved(t, store)
	if state.Get("status").Str() != "paused" || state.Get("plan").Items()[0].Get("step").Str() != "Inspect source" || state.Get("planState").Get("pending").Get("candidate").Get("plan").Items()[0].Get("budget").Get("requests").Num() != 1 {
		t.Fatal("configured candidate or accepted plan lost")
	}
	if _, err := ReadExecution(state); err != nil {
		t.Fatal(err)
	}
}
