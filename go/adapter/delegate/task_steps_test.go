package delegate_test

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/agent"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/delegate"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/eino"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
	"github.com/Judgment-Pack/judgment-pack-agent/go/relay"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func task183Definition(t *testing.T) runcontext.Subagent {
	t.Helper()
	raw, err := os.ReadFile("../../../contract/vectors/subagents/declaration.json")
	if err != nil {
		t.Fatal(err)
	}
	var file struct{ Items []runcontext.Subagent }
	if err = json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	return file.Items[0]
}
func task183Root(t *testing.T, store runcontext.RunStore) *adapter.Run {
	t.Helper()
	g := gate.New(gate.Options{})
	hosts := []contract.ToolOfferHostTool{}
	for _, name := range []string{"lookup", "delegate_tasks", "delegate_plan_steps", "update_plan", "complete_plan_step"} {
		hosts = append(hosts, contract.ToolOfferHostTool{Name: name, Description: "Host fixture admission", InputSchema: json.RawMessage(`{"type":"object"}`), Taxonomy: contract.ToolTaxonomy{Operation: "run", Effect: "read"}})
	}
	g.SetOffer(contract.ToolOffer{Tools: []contract.ToolOfferRuntimeTool{}, HostTools: hosts})
	cap := 8
	concurrency := 2
	return &adapter.Run{Gate: g.Calls(), Wire: "openai-compatible", ModelName: "fixture", Prompt: "Inspect independent evidence.", Critic: "off", AllowConversation: true, Interactive: true, Subagents: []runcontext.Subagent{task183Definition(t)}, HostTools: []runcontext.HostTool{{Name: "lookup", Description: "Read supplied evidence.", Taxonomy: &runcontext.ToolTaxonomy{Operation: "read", Effect: "read"}, Effect: "read", InputSchema: json.RawMessage(`{"type":"object"}`), Execute: func(context.Context, json.RawMessage) (runcontext.ToolResult, error) {
		return runcontext.ResultOf(nil, nil), nil
	}}}, Policy: runcontext.PolicyLayer{"concurrency": map[string]any{"children": 2, "requests": 2}}, Models: func() (runcontext.RunModels, error) {
		return runcontext.RunModels{ConcurrentRequests: &concurrency}, nil
	}, Context: &runcontext.Input{TaskSteps: true, RequestCap: &cap, CurrentRequest: &runcontext.CurrentRequest{Message: "Inspect independent evidence.", Input: "Work"}, RunStore: &runcontext.DurableRunInput{Store: store, ID: "task183-coordinator"}}, Emit: func(contract.RunEvent) {}, Children: func(context.Context, contract.DelegatedTaskProfile, func(contract.RunEvent)) (*gate.Calls, []contract.ToolOfferRuntimeTool, error) {
		child := gate.New(gate.Options{})
		child.SetOffer(contract.ToolOffer{})
		return child.Calls(), nil, nil
	}}
}
func task183Head(t *testing.T, store runcontext.RunStore) *runcontext.Value {
	t.Helper()
	head, err := store.Load(context.Background(), "task183-coordinator")
	if err != nil || head == nil {
		t.Fatal("missing actual original store head", err)
	}
	return runcontext.MustParseJSON(string(head.Checkpoint))
}
func task183Plan(pin *contract.SubagentRecord) any {
	return []any{map[string]any{"step": "Inspect first source", "status": "pending", "budget": map[string]int{"requests": 1}, "delegation": pin}, map[string]any{"step": "Inspect second source", "status": "pending", "budget": map[string]int{"requests": 1}, "delegation": pin}}
}
func task183JSON(value any) json.RawMessage { raw, _ := json.Marshal(value); return raw }
func task183ModelReply(name string, args any) *http.Response {
	delta := map[string]any{"content": "Evidence checked."}
	finish := "stop"
	if name != "" {
		delta = map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "call-" + name, "type": "function", "function": map[string]any{"name": name, "arguments": string(task183JSON(args))}}}}
		finish = "tool_calls"
	}
	chunk := map[string]any{"id": "fixture", "object": "chat.completion.chunk", "model": "fixture", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}, "usage": map[string]int{"prompt_tokens": 2, "completion_tokens": 1}}
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: " + string(task183JSON(chunk)) + "\n\ndata: [DONE]\n\n"))}
}
func task183Completion(state *runcontext.Value, index int) any {
	row := state.Get("planState").Get("taskSchedule").Get("rows").Items()[index]
	child := row.Get("child")
	result := child.Get("result").Clone()
	result.Set("task", child.Get("task").Clone())
	return map[string]any{"id": fmt.Sprintf("step-%d", index+1), "expectedRevision": state.Get("planState").Get("revision").Str(), "outcome": "completed", "evidence": map[string]any{"operations": []string{}, "artifacts": []any{}, "child": json.RawMessage(result.JSON())}}
}
func TestTaskSteps183ActualModelCoordinatorConcurrencyAndCompletion(t *testing.T) {
	store := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
	run := task183Root(t, store)
	raw, err := os.ReadFile("../../../contract/testdata/task-steps/native-subagent.json")
	if err != nil {
		t.Fatal(err)
	}
	var definition runcontext.Subagent
	if err = json.Unmarshal(raw, &definition); err != nil {
		t.Fatal(err)
	}
	// This tests overlap and acknowledged completion, not the shared declaration's
	// 30-second latency bound. The child clock also covers serialized durable
	// admission and result commits, which race instrumentation makes expensive.
	definition.Bounds.Milliseconds = 120000
	definition.PinnedDigest = runcontext.SubagentDigest(definition)
	run.Subagents = []runcontext.Subagent{definition}
	pin := &contract.SubagentRecord{ID: run.Subagents[0].ID, Digest: run.Subagents[0].PinnedDigest, Source: contract.SubagentRecordSource(run.Subagents[0].Source)}
	var mu sync.Mutex
	parentCalls, childCalls, active, peak := 0, 0, 0, 0
	together := make(chan struct{})
	var releaseChildren sync.Once
	rendezvousExpired := false
	started := time.Now()
	run.Emit = func(event contract.RunEvent) {
		if event.ToolResult != nil && event.ToolResult.IsError {
			t.Logf("tool refusal %s", task183JSON(event))
		}
		if event.Error != nil {
			t.Errorf("actual engine error: %s", task183JSON(event))
		}
	}
	run.Model = func(ctx context.Context, _ string, request relay.Request) (*http.Response, error) {
		parent := strings.Contains(string(request.Body), `"name":"delegate_plan_steps"`)
		mu.Lock()
		active++
		if active > peak {
			peak = active
		}
		stage := 0
		if parent {
			parentCalls++
			stage = parentCalls
		} else {
			childCalls++
			if childCalls == 2 {
				releaseChildren.Do(func() { close(together) })
			}
		}
		mu.Unlock()
		defer func() { mu.Lock(); active--; mu.Unlock() }()
		deadline, _ := ctx.Deadline()
		t.Logf("fixture provider parent=%t stage=%d elapsed=%s deadlineRemaining=%s", parent, stage, time.Since(started), time.Until(deadline))
		if !parent {
			timer := time.NewTimer(lifecycleBarrierTimeout)
			defer timer.Stop()
			select {
			case <-together:
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-timer.C:
				mu.Lock()
				select {
				case <-together:
				default:
					rendezvousExpired = true
					releaseChildren.Do(func() { close(together) })
				}
				mu.Unlock()
			}
			return task183ModelReply("", nil), nil
		}
		switch stage {
		case 1:
			return task183ModelReply("update_plan", map[string]any{"explanation": "Inspect independent sources.", "plan": task183Plan(pin)}), nil
		case 2:
			state := task183Head(t, store)
			return task183ModelReply("delegate_plan_steps", map[string]any{"planRevision": state.Get("planState").Get("revision").Str(), "steps": []string{"step-1", "step-2"}}), nil
		case 3, 4:
			state := task183Head(t, store)
			if !state.Get("planState").Get("taskSchedule").Get("rows").Items()[stage-3].Get("child").Has("result") {
				return nil, fmt.Errorf("successful physical child result lacks acknowledged evidence")
			}
			return task183ModelReply("complete_plan_step", task183Completion(state, stage-3)), nil
		default:
			return task183ModelReply("", nil), nil
		}
	}
	if err := delegate.With(eino.New(), delegate.Options{}).Run(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if rendezvousExpired {
		t.Fatalf("independent child provider calls did not overlap: parent=%d child=%d peak=%d", parentCalls, childCalls, peak)
	}
	if parentCalls != 5 || childCalls != 2 || peak != 2 {
		t.Fatalf("parent=%d child=%d peak=%d", parentCalls, childCalls, peak)
	}
	state := task183Head(t, store)
	if state.Get("used").Num() != 7 {
		t.Fatal("root/step double charge or lost charge")
	}
	for _, step := range state.Get("plan").Items() {
		if step.Get("status").Str() != "completed" || !step.Get("evidence").Has("child") {
			t.Fatal("separate child evidence completion missing")
		}
	}
	for _, row := range state.Get("planState").Get("taskSchedule").Get("rows").Items() {
		if row.Get("claims").Len() != 1 || !row.Get("child").Has("result") {
			t.Fatal("actual child identity/evidence missing")
		}
	}
}
func TestTaskSteps183ActualNativeCoordinatorIntersectionAndCompletion(t *testing.T) {
	store := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
	run := task183Root(t, store)
	run.Context.RequestCap = nil
	raw, loadErr := os.ReadFile("../../../contract/testdata/task-steps/native-subagent.json")
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	var native runcontext.Subagent
	if err := json.Unmarshal(raw, &native); err != nil {
		t.Fatal(err)
	}
	run.Subagents = []runcontext.Subagent{native}
	run.Policy = runcontext.PolicyLayer{"concurrency": map[string]any{"children": 1, "requests": 1}}
	run.Context.TaskStepEnvelope = &runcontext.TaskStepEnvelope{ID: "native-host-envelope", Revision: "sha256:" + strings.Repeat("a", 64), Unit: "provider-invocations", Limit: 4}
	pin := &contract.SubagentRecord{ID: run.Subagents[0].ID, Digest: run.Subagents[0].PinnedDigest, Source: contract.SubagentRecordSource(run.Subagents[0].Source)}
	calls, active, peak := 0, 0, 0
	run.Emit = func(event contract.RunEvent) {
		if event.Error != nil {
			t.Errorf("actual native error: %s", task183JSON(event))
		}
	}
	run.Agent = &adapter.Agent{Model: "fixture", Run: func(ctx context.Context, request adapter.AgentRequest, cb adapter.AgentCallbacks) error {
		calls++
		active++
		if active > peak {
			peak = active
		}
		defer func() {
			active--
			if cb.ProviderRelease != nil {
				cb.ProviderRelease.Acknowledge(cb.ProviderRelease.Invocation)
			}
		}()
		invoke := func(name string, args any) (adapter.AgentToolAnswer, error) {
			for _, tool := range request.Tools {
				if strings.HasPrefix(tool.Description, name+":") {
					return cb.Tool(ctx, adapter.AgentToolCall{ID: "task183-" + name, Name: tool.Name, Arguments: task183JSON(args)})
				}
			}
			return adapter.AgentToolAnswer{}, fmt.Errorf("no offered %s", name)
		}
		if calls == 1 {
			if answer, err := invoke("update_plan", map[string]any{"explanation": "Inspect independent sources.", "plan": task183Plan(pin)}); err != nil || answer.IsError {
				return fmt.Errorf("update: %s %v", answer.Text, err)
			}
			revision := task183Head(t, store).Get("planState").Get("revision").Str()
			if answer, err := invoke("delegate_plan_steps", map[string]any{"planRevision": revision, "steps": []string{"step-1", "step-1"}}); err != nil || !answer.IsError {
				return fmt.Errorf("duplicate native selection not refused whole: %v %+v", err, answer)
			}
			_, err := invoke("delegate_plan_steps", map[string]any{"planRevision": revision, "steps": []string{"step-1", "step-2"}})
			return err
		}
		if calls == 4 {
			for i := 0; i < 2; i++ {
				answer, err := invoke("complete_plan_step", task183Completion(task183Head(t, store), i))
				if err != nil || answer.IsError {
					return fmt.Errorf("complete: %s %v", answer.Text, err)
				}
			}
		}
		return cb.Event(ctx, adapter.AgentEvent{Type: "message", ID: "answer", Text: "Evidence checked.", Phase: "final"})
	}}

	if err := delegate.With(agent.New(), delegate.Options{}).Run(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if calls != 4 || peak != 1 {
		state := task183Head(t, store)
		t.Fatalf("native calls=%d peak=%d delegation=%s rows=%s", calls, peak, state.Get("delegation").JSON(), state.Get("planState").Get("taskSchedule").Get("rows").JSON())
	}
	state := task183Head(t, store)
	if state.Get("limit").Num() != 1 || state.Get("used").Num() != 0 || state.Get("planState").Get("taskEnvelope").Get("configuration").Get("limit").Num() != 4 {
		t.Fatal("native balance semantics changed")
	}
	for _, step := range state.Get("plan").Items() {
		if step.Get("status").Str() != "completed" {
			t.Fatal("native child completion missing")
		}
	}
}

func TestTaskSteps183ActualEinoForgedMarkerRefused(t *testing.T) {
	store := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
	run := task183Root(t, store)
	calls, executions, refusals := 0, 0, 0
	run.HostTools = append(run.HostTools, runcontext.HostTool{Name: "forged_task_action", Description: "Fixture forged marker.", Interaction: "delegation", Taxonomy: &runcontext.ToolTaxonomy{Operation: "run", Effect: "read"}, InputSchema: json.RawMessage(`{"type":"object","taskStepRegistration":true,"properties":{"planRevision":{"type":"string"},"steps":{"type":"array","items":{"type":"string"}}},"required":["planRevision","steps"],"additionalProperties":false}`), Execute: func(context.Context, json.RawMessage) (runcontext.ToolResult, error) {
		executions++
		return runcontext.ToolResult{}, nil
	}})
	run.Model = func(context.Context, string, relay.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return task183ModelReply("forged_task_action", map[string]any{"planRevision": "sha256:" + strings.Repeat("a", 64), "steps": []string{"step-1"}}), nil
		}
		return task183ModelReply("", nil), nil
	}
	run.Emit = func(event contract.RunEvent) {
		if event.ToolResult != nil && event.ToolResult.Name == "forged_task_action" && event.ToolResult.IsError {
			refusals++
		}
	}
	if err := eino.New().Run(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || executions != 0 || refusals != 1 {
		t.Fatalf("calls=%d executions=%d refusals=%d", calls, executions, refusals)
	}
}

type task183FaultStore struct {
	runcontext.RunStore
	fault string
	lose  bool
}

func (s *task183FaultStore) Commit(ctx context.Context, input runcontext.RunCommit) (runcontext.RunReceipt, error) {
	receipt, err := s.RunStore.Commit(ctx, input)
	if err != nil {
		return receipt, err
	}
	state := runcontext.MustParseJSON(string(input.Checkpoint))
	bound, physical := false, false
	for _, row := range state.Get("planState").Get("taskSchedule").Get("rows").Items() {
		bound = bound || row.Has("child")
	}
	for _, row := range state.Get("durable").Get("dispatches").Items() {
		physical = physical || row.Get("kind").Str() == "agent" && row.Get("state").Str() == "claimed" && row.Get("scope").Has("task")
	}
	if s.lose && (s.fault == "binding" && bound || s.fault == "physical" && physical) {
		s.lose = false
		return runcontext.RunReceipt{}, fmt.Errorf("fixture %s acknowledgement lost", s.fault)
	}
	return receipt, nil
}
func TestTaskSteps183ActualNativeQueuedAndPhysicalACKResume(t *testing.T) {
	for _, fault := range []string{"binding", "physical"} {
		t.Run(fault, func(t *testing.T) {
			memory := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
			store := &task183FaultStore{RunStore: memory, fault: fault, lose: true}
			run := task183Root(t, store)
			run.Context.RequestCap = nil
			raw, err := os.ReadFile("../../../contract/testdata/task-steps/native-subagent.json")
			if err != nil {
				t.Fatal(err)
			}
			var definition runcontext.Subagent
			if err = json.Unmarshal(raw, &definition); err != nil {
				t.Fatal(err)
			}
			run.Subagents = []runcontext.Subagent{definition}
			run.Policy = runcontext.PolicyLayer{"concurrency": map[string]any{"children": 1, "requests": 1}}
			run.Context.TaskStepEnvelope = &runcontext.TaskStepEnvelope{ID: "native-host-envelope", Revision: "sha256:" + strings.Repeat("a", 64), Unit: "provider-invocations", Limit: 4}
			pin := &contract.SubagentRecord{ID: definition.ID, Digest: definition.PinnedDigest, Source: contract.SubagentRecordSource(definition.Source)}
			calls, children := 0, 0
			run.Agent = &adapter.Agent{Model: "fixture", Run: func(ctx context.Context, request adapter.AgentRequest, cb adapter.AgentCallbacks) error {
				calls++
				defer func() {
					if cb.ProviderRelease != nil {
						cb.ProviderRelease.Acknowledge(cb.ProviderRelease.Invocation)
					}
				}()
				invoke := func(name string, args any) (adapter.AgentToolAnswer, error) {
					for _, tool := range request.Tools {
						if strings.HasPrefix(tool.Description, name+":") {
							return cb.Tool(ctx, adapter.AgentToolCall{ID: fmt.Sprintf("task183-%d-%s", calls, name), Name: tool.Name, Arguments: task183JSON(args)})
						}
					}
					return adapter.AgentToolAnswer{}, fmt.Errorf("missing %s", name)
				}
				if calls == 1 {
					if answer, err := invoke("update_plan", map[string]any{"explanation": "Inspect one source.", "plan": []any{map[string]any{"step": "Inspect source", "status": "pending", "budget": map[string]int{"requests": 1}, "delegation": pin}}}); err != nil || answer.IsError {
						return fmt.Errorf("update %s: %v", answer.Text, err)
					}
					state := task183Head(t, memory)
					_, err := invoke("delegate_plan_steps", map[string]any{"planRevision": state.Get("planState").Get("revision").Str(), "steps": []string{"step-1"}})
					return err
				}
				parent := false
				for _, tool := range request.Tools {
					parent = parent || strings.HasPrefix(tool.Description, "delegate_plan_steps:")
				}
				if !parent {
					children++
				}
				return cb.Event(ctx, adapter.AgentEvent{Type: "message", ID: "answer", Text: "Evidence checked.", Phase: "final"})
			}}
			_ = delegate.With(agent.New(), delegate.Options{}).Run(context.Background(), run)
			if store.lose || calls != 1 || children != 0 {
				t.Fatalf("original invocation retried after uncertain %s ACK: lose=%v calls=%d children=%d", fault, store.lose, calls, children)
			}
			before := task183Head(t, memory)
			id := before.Get("planState").Get("taskSchedule").Get("rows").Items()[0].Get("child").Get("task").Str()
			if before.Get("handoff").Get("tasks").Items()[0].Get("id").Str() != id {
				t.Fatal("original queued identity absent")
			}
			resumed := task183Root(t, store)
			resumed.Context.RequestCap = nil
			resumed.Context.TaskStepEnvelope = run.Context.TaskStepEnvelope
			resumed.Context.RunStore.Resume = true
			resumed.Subagents = run.Subagents
			resumed.Policy = run.Policy
			resumed.Agent = run.Agent
			_ = delegate.With(agent.New(), delegate.Options{}).Run(context.Background(), resumed)
			after := task183Head(t, memory)
			row := after.Get("planState").Get("taskSchedule").Get("rows").Items()[0]
			expected := 0
			if fault == "binding" {
				expected = 1
			}
			if children != expected || row.Get("child").Get("task").Str() != id {
				t.Fatalf("resume changed queued identity or redispatched uncertain claim: children=%d expected=%d row=%s", children, expected, row.JSON())
			}
			claims := 0
			for _, claim := range after.Get("durable").Get("dispatches").Items() {
				if claim.Get("kind").Str() == "agent" && claim.Get("scope").Has("task") && claim.Get("scope").Get("task").Str() == id {
					claims++
				}
			}
			if claims != 1 || row.Get("claims").Len() != 1 {
				t.Fatalf("original physical identity not consumed once: claims=%d row=%s", claims, row.JSON())
			}
			if fault == "physical" && row.Get("child").Has("result") {
				t.Fatal("unknown physical claim established successful result")
			}
		})
	}
}

func TestTaskSteps183ActualModelRetryCharges(t *testing.T) {
	for _, status := range []int{429, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) { task183ActualModelResponse(t, status) })
	}
}
func task183ActualModelResponse(t *testing.T, status int) {
	store := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
	run := task183Root(t, store)
	raw, err := os.ReadFile("../../../contract/testdata/task-steps/native-subagent.json")
	if err != nil {
		t.Fatal(err)
	}
	var definition runcontext.Subagent
	if err = json.Unmarshal(raw, &definition); err != nil {
		t.Fatal(err)
	}
	run.Subagents = []runcontext.Subagent{definition}
	pin := &contract.SubagentRecord{ID: definition.ID, Digest: definition.PinnedDigest, Source: contract.SubagentRecordSource(definition.Source)}
	parent, child := 0, 0
	run.Model = func(ctx context.Context, _ string, request relay.Request) (*http.Response, error) {
		if !strings.Contains(string(request.Body), `"name":"delegate_plan_steps"`) {
			child++
			if child == 1 {
				return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(task183JSON(map[string]any{"error": map[string]any{"type": map[int]string{429: "rate_limit_error", 503: "overloaded_error"}[status]}}))))}, nil
			}
			return task183ModelReply("", nil), nil
		}
		parent++
		if parent == 1 {
			return task183ModelReply("update_plan", map[string]any{"explanation": "Inspect source.", "plan": []any{map[string]any{"step": "Inspect source", "status": "pending", "budget": map[string]int{"requests": 2}, "delegation": pin}}}), nil
		}
		if parent == 2 {
			return task183ModelReply("delegate_plan_steps", map[string]any{"planRevision": task183Head(t, store).Get("planState").Get("revision").Str(), "steps": []string{"step-1"}}), nil
		}
		return task183ModelReply("", nil), nil
	}
	if err = delegate.With(eino.New(), delegate.Options{}).Run(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	state := task183Head(t, store)
	claims := state.Get("planState").Get("taskSchedule").Get("rows").Items()[0].Get("claims")
	if status == 429 {
		if child != 2 || parent != 3 || state.Get("used").Num() != 5 || claims.Len() != 2 || claims.Items()[0].Get("id").Str() == claims.Items()[1].Get("id").Str() || !state.Get("planState").Get("taskSchedule").Get("rows").Items()[0].Get("child").Has("result") {
			t.Fatalf("retry charges: parent=%d child=%d state=%s", parent, child, state.JSON())
		}
	} else if child != 1 || parent != 2 || state.Get("used").Num() != 3 || claims.Len() != 1 || state.Get("planState").Get("taskSchedule").Get("rows").Items()[0].Get("child").Has("result") {
		t.Fatalf("unknown503 retried or established proof: parent=%d child=%d state=%s", parent, child, state.JSON())
	}
}
