package runcontext

// The shared execution continuity (the port of packages/harness/execution/execution.test.ts). The
// Plan-mode refusal of a call is the gate's (gate.PlanPhase), so what is held here is the state,
// the plan tool, the budget tool, the read cache and the grants.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

// trackedSession is execution.test.ts's base session: a model capability, a current request, a
// conversation allowed.
func trackedSession(t *testing.T) (Session, *int) {
	t.Helper()
	calls := 0
	held := Session{Prompt: "Fix the missing-size exception and verify unchanged expectations",
		TestPrompt: "", Tools: []RuntimeTool{}, HostTools: []HostTool{}, AllowConversation: true, Interactive: true,
		Model:   &ModelSession{Family: "gemini", Model: "fixture"},
		Context: &Input{CurrentRequest: &CurrentRequest{Message: "Fix it", Input: "current pack and test facts"}}}
	held.CallTool = func(ctx context.Context, name string, args json.RawMessage) (ToolResult, error) {
		calls++
		return ResultOf([]Content{TextContent(name)}, nil), nil
	}
	return held, &calls
}

func openRun(t *testing.T, session Session) *Run {
	t.Helper()
	run, err := ContextualSession(context.Background(), session)
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func hostTool(t *testing.T, run *Run, name string) HostTool {
	t.Helper()
	for _, held := range run.Session.HostTools {
		if held.Name == name {
			return held
		}
	}
	t.Fatalf("no host tool named %s", name)
	return HostTool{}
}

func TestTheExecutionRefusesAChangedRequestOrPhase(t *testing.T) {
	session, _ := trackedSession(t)
	run := openRun(t, session)
	saved, err := run.Context.Execution.Snapshot("paused")
	if err != nil {
		t.Fatal(err)
	}
	// A paused execution is paused until the person grants more.
	paused := session
	held := *session.Context
	held.Execution = &saved
	paused.Context = &held
	if _, err := ContextualSession(context.Background(), paused); err != ErrExecutionPaused {
		t.Errorf("a paused execution answered %v", err)
	}
	extended, err := ExtendExecution(saved, false, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// The phase changed.
	changed := session
	phase := *session.Context
	phase.Phase = "plan"
	phase.Execution = extended
	changed.Context = &phase
	_, err = ContextualSession(context.Background(), changed)
	refused(t, err, "phase changed")
	// The request changed.
	other := session
	other.Context = &Input{CurrentRequest: &CurrentRequest{Message: "Different", Input: "different"}, Execution: extended}
	_, err = ContextualSession(context.Background(), other)
	refused(t, err, "request or execution phase changed")
}

func TestTheExecutionDetectsModifiedRetainedValuesAndMalformedGrants(t *testing.T) {
	session, _ := trackedSession(t)
	run := openRun(t, session)
	if _, err := run.Context.Ledger.Retain(Document{ID: "source", Kind: "tool", Label: "Source",
		Value: ObjectOf("flag", Bool(false))}, false); err != nil {
		t.Fatal(err)
	}
	paused, err := run.Context.Execution.Snapshot("paused")
	if err != nil {
		t.Fatal(err)
	}
	saved, err := ExtendExecution(paused, false, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	saved.Context.Entries[0].Value = json.RawMessage(`{"flag":true}`)
	changed := session
	changed.Context = &Input{CurrentRequest: session.Context.CurrentRequest, Execution: saved}
	_, err = ContextualSession(context.Background(), changed)
	refused(t, err, "digest")
	// A limit that does not chain from its grants is refused.
	malformed := *saved
	malformed.Limit = 100
	raw, err := json.Marshal(malformed)
	if err != nil {
		t.Fatal(err)
	}
	_, err = ReadExecution(MustParseJSON(string(raw)))
	refused(t, err, "invalid")
}

func TestTheReadCacheServesUnchangedContractsAndNeverWrites(t *testing.T) {
	session, calls := trackedSession(t)
	session.Tools = []RuntimeTool{{Name: "get_schema"}}
	run := openRun(t, session)
	for at := 0; at < 2; at++ {
		if _, err := run.Session.CallTool(context.Background(), "get_schema", json.RawMessage(`{}`)); err != nil {
			t.Fatal(err)
		}
	}
	for at := 0; at < 2; at++ {
		if _, err := run.Session.CallTool(context.Background(), "save_pack", json.RawMessage(`{"id":"one"}`)); err != nil {
			t.Fatal(err)
		}
	}
	if *calls != 3 {
		t.Errorf("the runtime was called %d times", *calls)
	}
}

func TestTheReadCacheFollowsTheHostsDeclaration(t *testing.T) {
	session, _ := trackedSession(t)
	immutable, mutable, write := 0, 0, 0
	count := func(held *int, text string) func(context.Context, json.RawMessage) (ToolResult, error) {
		return func(ctx context.Context, args json.RawMessage) (ToolResult, error) {
			*held++
			return ResultOf([]Content{TextContent(text)}, nil), nil
		}
	}
	session.HostTools = []HostTool{
		{Name: "future_contract", Effect: "read", ContextPolicy: &ToolContextPolicy{Immutable: true, Reference: true},
			Description: "Contract", InputSchema: json.RawMessage(`{}`), Execute: count(&immutable, "stable")},
		{Name: "future_current_reference", Effect: "read", ContextPolicy: &ToolContextPolicy{Reference: true},
			Description: "Current reference", InputSchema: json.RawMessage(`{}`), Execute: count(&mutable, "current")},
		{Name: "future_write", Effect: "workflow", Delegation: "read", ContextPolicy: &ToolContextPolicy{Immutable: true},
			Description: "Write", InputSchema: json.RawMessage(`{}`), Execute: count(&write, "")},
	}
	run := openRun(t, session)
	for _, name := range []string{"future_contract", "future_current_reference", "future_write"} {
		held := hostTool(t, run, name)
		for at := 0; at < 2; at++ {
			if _, err := held.Execute(context.Background(), json.RawMessage(`{}`)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if immutable != 1 || mutable != 2 || write != 2 {
		t.Errorf("the tools were called %d, %d and %d times", immutable, mutable, write)
	}
	// Changing arguments still performs a distinct read; failures never poison the cache.
	if _, err := hostTool(t, run, "future_contract").Execute(context.Background(), json.RawMessage(`{"revision":"next"}`)); err != nil {
		t.Fatal(err)
	}
	if immutable != 2 {
		t.Errorf("a changed argument was served from the cache (%d calls)", immutable)
	}
}

func TestUpdatePlanGrantsNoWorkOrRequests(t *testing.T) {
	session, _ := trackedSession(t)
	held := *session.Context
	held.Phase = "plan"
	session.Context = &held
	run := openRun(t, session)
	plan := hostTool(t, run, "update_plan")
	// A limit the model writes into its arguments grants nothing: the tool records progress only.
	if _, err := plan.Execute(context.Background(),
		json.RawMessage(`{"explanation":"Prepare review","plan":[{"step":"Inspect","status":"completed"}],"limit":999}`)); err != nil {
		t.Fatal(err)
	}
	answered, err := plan.Execute(context.Background(),
		json.RawMessage(`{"explanation":"Prepare review","plan":[{"step":"Inspect","status":"completed"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if answered.StructuredContent.Get("status").Str() != "progress-recorded" ||
		!answered.StructuredContent.Get("permissionsUnchanged").Truth() {
		t.Errorf("update_plan answered %s", answered.StructuredContent.Text())
	}
	if run.Context.Execution.State.Limit != MaxTurns {
		t.Errorf("the limit is %d", run.Context.Execution.State.Limit)
	}
	_, err = plan.Execute(context.Background(),
		json.RawMessage(`{"explanation":"","plan":[{"step":"a","status":"in_progress"},{"step":"b","status":"in_progress"}]}`))
	refused(t, err, "at most one")
}

func TestTheBudgetToolAsksOnlyNearTheCeiling(t *testing.T) {
	session, _ := trackedSession(t)
	run := openRun(t, session)
	request := hostTool(t, run, "request_budget_increase")
	args := json.RawMessage(`{"reason":"The proposed guard needs two boundary checks.","nextStep":"Evaluate both boundaries with unchanged expectations."}`)
	answered, err := request.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if answered.StructuredContent.Get("status").Str() != "within-allowance" {
		t.Errorf("the tool answered %s", answered.StructuredContent.Text())
	}
	if run.Context.Execution.PauseRequested {
		t.Error("a pause was requested within the allowance")
	}
	run.Context.Execution.State.Used = 18
	answered, err = request.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if answered.StructuredContent.Get("status").Str() != "approval-required" {
		t.Errorf("the tool answered %s", answered.StructuredContent.Text())
	}
	state := run.Context.Execution.State
	if state.Used != 18 || state.Limit != 20 || len(state.Grants) != 0 {
		t.Errorf("the state is %+v", state)
	}
	if !run.Context.Execution.PauseRequested {
		t.Error("no pause was requested at the ceiling")
	}
	if state.Reason == nil || !strings.Contains(*state.Reason, "two boundary checks") {
		t.Errorf("the reason is %v", state.Reason)
	}
}

func TestTheRequestIsClaimedAndCheckpointedBeforeItIsSent(t *testing.T) {
	session, _ := trackedSession(t)
	order := []string{}
	held := *session.Context
	var saved []contract.ProvenancePromptDigest
	held.Checkpoint = func(ctx context.Context, state contract.Checkpoint) error {
		order = append(order, "saved-"+itoa(int(state.Used)))
		saved = state.Prompts
		return nil
	}
	session.Context = &held
	run := openRun(t, session)
	body := []byte(`{"model":"scripted-model","messages":[{"role":"user","content":"Check the pack."}]}`)
	if err := run.Context.ClaimRequest(context.Background(), body); err != nil {
		t.Fatal(err)
	}
	order = append(order, "sent")
	if len(order) != 3 || order[0] != "saved-0" || order[1] != "saved-1" || order[2] != "sent" {
		t.Errorf("the order is %v", order)
	}
	// ADR-0005: the checkpoint saved before the request carries the digest of its body.
	if len(saved) != 1 || saved[0].Request != 1 || saved[0].Sha256 != ContextDigest(string(body)) {
		t.Errorf("the saved prompts are %+v", saved)
	}
	// A checkpoint that cannot be saved stops the request, as a storage failure.
	unavailable, _ := trackedSession(t)
	other := *unavailable.Context
	other.Checkpoint = func(ctx context.Context, state contract.Checkpoint) error {
		return &authoredError{"Storage unavailable"}
	}
	unavailable.Context = &other
	_, err := ContextualSession(context.Background(), unavailable)
	if err == nil {
		t.Fatal("the request was sent")
	}
	if !strings.Contains(err.Error(), "Policy checkpoint could not be saved") {
		t.Errorf("the failure is %q", err.Error())
	}
}

func TestAnExhaustedFailedRequestNeedsAnExplicitGrant(t *testing.T) {
	session, _ := trackedSession(t)
	run := openRun(t, session)
	task := run.Context.Execution
	task.State.Used = 20
	callID := "call-1"
	task.ObserveToolCall(contract.ToolCall{Type: "tool_call", Name: "read_contract",
		Args: json.RawMessage(`{"revision":"retained"}`), CallID: &callID})
	task.ObserveToolResult(contract.ToolResult{Type: "tool_result", Name: "read_contract",
		IsError: false, Text: "Already read", CallID: &callID})
	stopped, err := task.Snapshot("stopped")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ExtendExecution(stopped, false, time.Now()); err == nil {
		t.Error("a stopped execution was extended without the interrupted grant")
	} else {
		refused(t, err, "cannot receive")
	}
	extended, err := ExtendExecution(stopped, true, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if extended.Used != 20 || extended.Limit != 30 || extended.Status != "running" ||
		len(extended.Grants) != 1 || extended.Grants[0].From != 20 || extended.Grants[0].To != 30 {
		t.Errorf("the extension is %+v", extended)
	}
	if len(extended.Operations) != len(stopped.Operations) {
		t.Error("the extension lost the completed operations")
	}
	if stopped.Used != 20 || stopped.Limit != 20 || stopped.Status != "stopped" || len(stopped.Grants) != 0 {
		t.Errorf("the stopped checkpoint was changed: %+v", stopped)
	}
	complete := stopped
	complete.Status = "complete"
	if _, err := ExtendExecution(complete, true, time.Now()); err == nil {
		t.Error("a complete execution was extended")
	}
	below := stopped
	below.Used = 19
	if _, err := ExtendExecution(below, true, time.Now()); err == nil {
		t.Error("an execution below its ceiling was extended")
	}
}

func TestTheSnapshotValidatesAgainstTheCheckpointSchema(t *testing.T) {
	session, _ := trackedSession(t)
	run := openRun(t, session)
	callID := "call-1"
	run.Context.Execution.ObserveToolCall(contract.ToolCall{Type: "tool_call", Name: "get_schema",
		Args: json.RawMessage(`{"spec_version":"0.2.0-draft"}`), CallID: &callID})
	run.Context.Execution.ObserveToolResult(contract.ToolResult{Type: "tool_result", Name: "get_schema",
		IsError: false, Text: "{}", CallID: &callID, Structured: json.RawMessage(`{"status":"ok"}`)})
	if _, err := run.Context.Ledger.Retain(Document{ID: "page", Kind: "tool", Label: "A page", Value: String("read")}, false); err != nil {
		t.Fatal(err)
	}
	held, err := run.Context.Snapshot("")
	if err != nil {
		t.Fatal(err)
	}
	// The snapshot reads back as the checkpoint the contract states, and the reader admits it.
	raw, err := json.Marshal(held)
	if err != nil {
		t.Fatal(err)
	}
	read, err := ReadExecution(MustParseJSON(string(raw)))
	if err != nil {
		t.Fatalf("the snapshot was refused: %v", err)
	}
	if read.Version != 1 || read.Unit != "model-requests" || read.Limit != MaxTurns ||
		len(read.Operations) != 1 || read.Operations[0].Status != "complete" ||
		len(read.Context.Entries) != 1 {
		t.Errorf("the checkpoint is %+v", read)
	}
	// And the delegation checkpoint vector is one this reader admits too.
	for at, item := range vectorItems(t, "delegation/checkpoint.json") {
		if _, err := ReadExecution(item); err != nil {
			t.Errorf("the delegation checkpoint item %d was refused: %v", at, err)
		}
	}
}

func TestTheRequestStateStatesTheToolCallCapAsTheContractDoes(t *testing.T) {
	session, _ := trackedSession(t)
	run := openRun(t, session)
	for at, item := range vectorItems(t, "budget/tool-call-cap.json") {
		run.Context.Execution.ToolAllowance = func() *ToolAllowance {
			return &ToolAllowance{Used: int(item.Get("used").Num()), Limit: int(item.Get("limit").Num()),
				Scope: item.Get("scope").Str()}
		}
		state := MustParseJSON(strings.SplitN(run.Context.Execution.RequestState(), "\n", 2)[1])
		if !equalJSON(state.Get("toolCalls"), item) {
			t.Errorf("item %d: the state says\n%s\nand the vector says\n%s", at, state.Get("toolCalls").Text(), item.Text())
		}
	}
}

func TestTheRequestStateStatesTheRequestAllowance(t *testing.T) {
	session, _ := trackedSession(t)
	run := openRun(t, session)
	state := MustParseJSON(strings.SplitN(run.Context.Execution.RequestState(), "\n", 2)[1])
	if !equalJSON(state.Get("requests"), MustParseJSON(`{"unit":"model-requests","used":0,"limit":20,"remaining":20}`)) {
		t.Errorf("the state says %s", state.Get("requests").Text())
	}
	// On an agent capability the provider runs its own loop.
	agent, _ := trackedSession(t)
	agent.Model = nil
	agent.Agent = &AgentSession{Model: "scripted-agent"}
	run = openRun(t, agent)
	state = MustParseJSON(strings.SplitN(run.Context.Execution.RequestState(), "\n", 2)[1])
	if state.Get("requests").Get("unit").Str() != "provider-managed" ||
		!strings.Contains(state.Get("requests").Get("note").Str(), "subscription provider") {
		t.Errorf("the state says %s", state.Get("requests").Text())
	}
	if !strings.HasPrefix(run.Context.Execution.Guidance(), ExecutionInstructions) {
		t.Error("the guidance does not open with the instructions")
	}
}

func TestLimitToHoldsAChildToItsBudget(t *testing.T) {
	session, _ := trackedSession(t)
	run := openRun(t, session)
	run.Context.Execution.LimitTo(4)
	if run.Context.Execution.State.Limit != 4 {
		t.Errorf("the limit is %d", run.Context.Execution.State.Limit)
	}
	run.Context.Execution.LimitTo(0)
	if run.Context.Execution.State.Limit != 1 {
		t.Errorf("the floor is %d", run.Context.Execution.State.Limit)
	}
	// Claim takes one request, and the ceiling pauses the run.
	run.Context.Execution.State.Limit = 1
	if err := run.Context.Execution.Claim(); err != nil {
		t.Fatal(err)
	}
	if err := run.Context.Execution.Claim(); err != ErrExecutionPaused {
		t.Errorf("the ceiling answered %v", err)
	}
}

// agent#71: a provider-managed execution's delegation figures may omit modelRequests, as the agent
// capability's do; a model-requests execution's carry it on the parent, each child and each total.
func TestDelegationFiguresReadModelRequestsByUnit(t *testing.T) {
	items := vectorItems(t, "delegation/figures-agent-runs.json")
	agent, model := items[0], items[1]
	if !ValidDelegationFigures(agent, "provider-managed") || ValidDelegationFigures(agent, "model-requests") {
		t.Fatalf("the agent capability's figures, without modelRequests: %s", agent.Text())
	}
	if !ValidDelegationFigures(model, "model-requests") || !ValidDelegationFigures(model, "provider-managed") {
		t.Fatalf("a model capability's figures: %s", model.Text())
	}
	// Each of the parent, a child and a total is held to the member where the unit counts requests.
	for _, cut := range []func(map[string]any) map[string]any{
		func(v map[string]any) map[string]any { return v["parent"].(map[string]any) },
		func(v map[string]any) map[string]any {
			return v["batches"].([]any)[0].(map[string]any)["children"].([]any)[0].(map[string]any)
		},
		func(v map[string]any) map[string]any {
			return v["batches"].([]any)[0].(map[string]any)["total"].(map[string]any)
		},
	} {
		var whole map[string]any
		if err := json.Unmarshal([]byte(model.Text()), &whole); err != nil {
			t.Fatal(err)
		}
		delete(cut(whole), "modelRequests")
		encoded, _ := json.Marshal(whole)
		held, err := ParseJSON(encoded)
		if err != nil {
			t.Fatal(err)
		}
		if ValidDelegationFigures(held, "model-requests") {
			t.Fatalf("a model-requests execution's figures without modelRequests were read: %s", encoded)
		}
	}
}
