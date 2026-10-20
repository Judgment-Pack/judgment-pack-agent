package runcontext

// The approval pause (ADR-0008): the decision on an act call, the call held while it waits, a second
// one refused, no act call where none is admitted, the readers, and a restored approved call never
// run again. Certified scenario 8 runs the same on both Go adapters (go/certification).

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

// book is an approval book the tests hold: the approval, and why no act call is admitted.
type book struct {
	approval *contract.CheckpointApproval
	closed   string
}

func (b *book) Current() *contract.CheckpointApproval       { return b.approval }
func (b *book) Ask(record contract.CheckpointApproval) bool { b.approval = &record; return true }
func (b *book) Closed() string                              { return b.closed }

// asking are the vector's declarations, each act's policy answering answer and noting what it was asked.
func asking(t *testing.T, answer string, asked *[]string) []HostTool {
	tools := declaredTools(t, vectorItems(t, "tool-offer/source-declarations.json"))
	for at := range tools {
		tools[at].Approve = func(call ApprovalRequest) string {
			*asked = append(*asked, call.Name)
			return answer
		}
	}
	return tools
}

func TestAnActCallIsAnsweredOnceAndASecondWhileItWaitsIsPending(t *testing.T) {
	var asked []string
	ran := 0
	tools := asking(t, "ask", &asked)
	for at := range tools {
		tools[at].Execute = func(context.Context, json.RawMessage) (ToolResult, error) { ran++; return ResultOf(nil, nil), nil }
	}
	gate, byName, rows := sourceGate(t, tools, Defaults, false)
	held := &book{}
	gate.SetApprovals(held)
	if !gate.NeedsApproval("tracker__create_issue") || gate.NeedsApproval("databricks__query_table") {
		t.Fatal("NeedsApproval")
	}
	answer, _, callID := gate.Decide(context.Background(), "tracker__create_issue", json.RawMessage(`{"repository":"expense","title":"a"}`), "call-a", true)
	if answer != "ask" || callID != "call-a" || held.approval == nil || held.approval.State != "asked" || held.approval.CallID != "call-a" ||
		held.approval.Name != "tracker__create_issue" || held.approval.Tool != "create_issue" || string(held.approval.Args["title"]) != `"a"` {
		t.Fatalf("the first decision: %s, %+v", answer, held.approval)
	}
	if _, err := gate.Call(WithCallID(context.Background(), "call-a"), "tracker__create_issue", json.RawMessage(`{}`), byName["tracker__create_issue"].Execute); err != ErrApprovalHeld {
		t.Fatalf("the asked call reached the gate and answered %v", err)
	}
	answer, detail, _ := gate.Decide(context.Background(), "ado__update_work_item", json.RawMessage(`{"id":1,"state":"Done"}`), "call-b", true)
	if answer != "pending" || !strings.Contains(detail, "waits for the person") || gate.Pending("call-b", "ado__update_work_item") != detail {
		t.Fatalf("the second decision: %s, %q", answer, detail)
	}
	if _, err := gate.Call(WithCallID(context.Background(), "call-b"), "ado__update_work_item", json.RawMessage(`{"id":1,"state":"Done"}`), byName["ado__update_work_item"].Execute); err == nil {
		t.Fatal("the pending call ran")
	}
	if len(asked) != 1 || ran != 0 || held.approval.CallID != "call-a" || (*rows)[4].Refused != 1 {
		t.Fatalf("asked %v, ran %d, approval %+v, ado %+v", asked, ran, held.approval, (*rows)[4])
	}
	// With no call id on the events, the decision draws one and keeps the next decision of the tool in order.
	other := &book{}
	queued, queuedTools, _ := sourceGate(t, asking(t, "ask", &asked), Defaults, false)
	queued.SetApprovals(other)
	if _, _, drawn := queued.Decide(context.Background(), "tracker__create_issue", json.RawMessage(`{}`), "", true); len(drawn) != 36 || other.approval.CallID != drawn {
		t.Fatalf("the drawn call id %q, approval %+v", drawn, other.approval)
	}
	if _, err := callThrough(queued, queuedTools, "tracker__create_issue", `{}`); err == nil {
		t.Fatal("a call with no call id ran while another waits")
	}
}

func TestNoActCallIsAdmittedInAChildPlanModeTheCriticOrTheReformatRun(t *testing.T) {
	for _, closed := range []string{"A delegated child admits reads only", "Plan mode admits reads only", "The critic and the reformat run admit no act call"} {
		var asked []string
		gate, byName, _ := sourceGate(t, asking(t, "allow", &asked), Defaults, false)
		held := &book{closed: closed}
		gate.SetApprovals(held)
		if answer, _, _ := gate.Decide(context.Background(), "tracker__create_issue", json.RawMessage(`{}`), "c", true); answer != "closed" {
			t.Fatalf("%s: %s", closed, answer)
		}
		_, err := gate.Call(WithCallID(context.Background(), "c"), "tracker__create_issue", json.RawMessage(`{}`), byName["tracker__create_issue"].Execute)
		refused(t, err, closed)
		if len(asked) != 0 || held.approval != nil {
			t.Fatalf("%s: the policy was asked %v", closed, asked)
		}
	}
	// The boundary's own book: the critic's reading and Plan mode close it, and a child's gate.
	var asked []string
	session := modelSession()
	session.HostTools = asking(t, "allow", &asked)
	run, err := ContextualSession(context.Background(), session)
	if err != nil {
		t.Fatal(err)
	}
	if answer, _, _ := run.Context.Sources.Decide(context.Background(), "tracker__create_issue", json.RawMessage(`{}`), "open", true); answer != "allow" {
		t.Fatalf("an open run answered %s", answer)
	}
	run.Context.CriticReading = true
	if answer, detail, _ := run.Context.Sources.Decide(context.Background(), "tracker__create_issue", json.RawMessage(`{}`), "critic", true); answer != "closed" || !strings.Contains(detail, "critic") {
		t.Fatalf("the critic answered %s", answer)
	}
	run.Context.Close()
	plan := modelSession()
	plan.HostTools = asking(t, "allow", &asked)
	plan.Context = &Input{Phase: "plan"}
	planned, err := ContextualSession(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	// Plan mode's offer is the Go gate's (gate.PlanPhase); the source gate admits no act call in it either.
	if answer, _, _ := planned.Context.Sources.Decide(context.Background(), "tracker__create_issue", json.RawMessage(`{}`), "plan", true); answer != "closed" {
		t.Fatalf("Plan mode answered %s", answer)
	}
	planned.Context.Close()
	child := approvalBook{c: run.Context, child: true}
	if !strings.Contains(child.Closed(), "delegated child") {
		t.Fatalf("a child's book: %q", child.Closed())
	}
}

func TestTheApprovalIsReadClosed(t *testing.T) {
	approval := `{"callId":"call-a","name":"tracker__create_issue","tool":"create_issue","source":{"kind":"mcp","id":"tracker","trust":"public","toolListDigest":"sha256:` + strings.Repeat("a", 64) + `","tool":"create_issue"},"args":{"repository":"expense"},"state":"asked"}`
	if !ValidApproval(MustParseJSON(approval)) {
		t.Fatal("a valid approval refused")
	}
	for _, bad := range []string{
		strings.Replace(approval, `"state":"asked"`, `"state":"pending"`, 1),
		strings.Replace(approval, `"state":"asked"`, `"state":"asked","approved":true`, 1),
		strings.Replace(approval, `"tracker__create_issue"`, `"tracker:create"`, 1),
		strings.Replace(approval, `"args":{"repository":"expense"}`, `"args":[]`, 1),
	} {
		if ValidApproval(MustParseJSON(bad)) {
			t.Errorf("an approval read: %s", bad)
		}
	}
	text := ApprovalContinuationText(contract.CheckpointApproval{CallID: "call-a", Name: "tracker__create_issue", Tool: "create_issue", State: "refused",
		Result: contractResult(RefusedResult("tracker__create_issue")), Note: ptr("A note.")}, sourceMarker)
	if line, _, _ := strings.Cut(text, "\n"); line != ApprovalContinuation || strings.Count(text, "A note.") != 1 ||
		!strings.HasSuffix(text, "\nEND RETRIEVED DATA "+sourceMarker) || !strings.Contains(text, `"approved":false`) {
		t.Fatalf("the continuation: %s", text)
	}
}

func ptr(text string) *string { return &text }

func TestARestoredApprovedCallIsNotRunAgain(t *testing.T) {
	var asked []string
	ran := 0
	tools := asking(t, "ask", &asked)
	for at := range tools {
		tools[at].Execute = func(context.Context, json.RawMessage) (ToolResult, error) { ran++; return ResultOf(nil, nil), nil }
	}
	request := "Open the issue."
	session := modelSession()
	session.Prompt = request
	session.HostTools = tools
	session.Context = &Input{CurrentRequest: &CurrentRequest{Message: request, Input: request}}
	opened, err := ContextualSession(context.Background(), session)
	if err != nil {
		t.Fatal(err)
	}
	if held := opened.Context.Approval(context.Background(), "tracker__create_issue", json.RawMessage(`{"repository":"expense","title":"a"}`), "call-a"); held == nil || held.ApprovalRequested == nil {
		t.Fatalf("the act call was not held: %+v", held)
	}
	saved, err := opened.Context.Execution.Snapshot("paused")
	if err != nil {
		t.Fatal(err)
	}
	opened.Context.Close()
	approved := *saved.Approval
	approved.State = "approved"
	saved.Approval = &approved
	saved.Status = "running"
	session.HostTools = asking(t, "ask", &asked)
	for at := range session.HostTools {
		session.HostTools[at].Execute = func(context.Context, json.RawMessage) (ToolResult, error) { ran++; return ResultOf(nil, nil), nil }
	}
	var written []contract.Checkpoint
	session.Context = &Input{CurrentRequest: &CurrentRequest{Message: request, Input: request}, Execution: &saved,
		Checkpoint: func(_ context.Context, state contract.Checkpoint) error { written = append(written, state); return nil }}
	restored, err := ContextualSession(context.Background(), session)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Context.Close()
	if ran != 0 || len(written) != 1 || written[0].Approval.State != "executed" || written[0].Approval.Result == nil ||
		!strings.Contains(string(written[0].Approval.Result.StructuredContent), `"outcome":"unknown"`) ||
		!strings.Contains(restored.Session.Prompt, ApprovalContinuation) || !strings.Contains(restored.Session.Prompt, "it may have run") {
		t.Fatalf("ran %d, written %s, prompt %q", ran, comparison(written), restored.Session.Prompt)
	}
	// A decision for another call is refused before anything runs.
	saved.Approval.State = "asked"
	saved.Status = "paused"
	session.Context.Approval = &contract.CheckpointApprovalDecision{CallID: "call-b", Approved: true}
	if _, err := ContextualSession(context.Background(), session); err == nil || !strings.Contains(err.Error(), "not waiting on") {
		t.Fatalf("a decision for another call answered %v", err)
	}
	// A decision handed again for a call already decided is refused before anything runs.
	for _, state := range []string{"refused", "executed", "answered"} {
		saved.Approval.State = state
		saved.Status = "running"
		session.Context.Approval = &contract.CheckpointApprovalDecision{CallID: "call-a", Approved: true}
		if _, err := ContextualSession(context.Background(), session); err == nil || !strings.Contains(err.Error(), "already decided") {
			t.Fatalf("a decision on a %s approval answered %v", state, err)
		}
	}
	if ran != 0 {
		t.Fatalf("ran %d", ran)
	}
}

// pausedSession is a session paused at an act call, and its checkpoint, the policy asking.
func pausedSession(t *testing.T, ran *int) (Session, contract.Checkpoint) {
	t.Helper()
	var asked []string
	request := "Open the issue."
	session := modelSession()
	session.Prompt = request
	session.HostTools = asking(t, "ask", &asked)
	for at := range session.HostTools {
		session.HostTools[at].Execute = func(context.Context, json.RawMessage) (ToolResult, error) { *ran++; return ResultOf(nil, nil), nil }
	}
	session.Context = &Input{CurrentRequest: &CurrentRequest{Message: request, Input: request}}
	opened, err := ContextualSession(context.Background(), session)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Context.Close()
	if held := opened.Context.Approval(context.Background(), "tracker__create_issue", json.RawMessage(`{"repository":"expense","title":"a"}`), "call-a"); held == nil {
		t.Fatal("the act call was not held")
	}
	saved, err := opened.Context.Execution.Snapshot("paused")
	if err != nil {
		t.Fatal(err)
	}
	return session, saved
}

// A Go host that saves only from the run's stream holds no approved write before the call would run,
// since the run's events reach it after its context opens: an approved continuation is refused there,
// and so never runs the call twice; with the durable callback the call runs once.
func TestAnApprovedCallRunsOnlyWhereTheHostAcknowledgesItsWrites(t *testing.T) {
	ran := 0
	session, saved := pausedSession(t, &ran)
	for attempt := 0; attempt < 2; attempt++ {
		held := saved
		session.Context = &Input{CurrentRequest: session.Context.CurrentRequest, Execution: &held,
			Approval: &contract.CheckpointApprovalDecision{CallID: "call-a", Approved: true}}
		if _, err := ContextualSession(context.Background(), session); err == nil || !strings.Contains(err.Error(), "durable callback") {
			t.Fatalf("a continuation without the callback answered %v", err)
		}
	}
	if ran != 0 {
		t.Fatalf("the call ran %d times without the callback", ran)
	}
	var states []string
	held := saved
	session.Context = &Input{CurrentRequest: session.Context.CurrentRequest, Execution: &held,
		Approval: &contract.CheckpointApprovalDecision{CallID: "call-a", Approved: true},
		Checkpoint: func(_ context.Context, state contract.Checkpoint) error {
			states = append(states, state.Approval.State)
			return nil
		}}
	run, err := ContextualSession(context.Background(), session)
	if err != nil {
		t.Fatal(err)
	}
	run.Context.Close()
	if ran != 1 || strings.Join(states, ",") != "approved,executed" {
		t.Fatalf("ran %d, acknowledged %v", ran, states)
	}
}

// An agent adapter claims its pause before an approval is written asked: a call that loses the claim
// to a handoff or a question leaves no asked approval, and is refused, never run.
func TestAnActCallThatLosesThePauseLeavesNoAskedApproval(t *testing.T) {
	var asked []string
	session := modelSession()
	session.HostTools = asking(t, "ask", &asked)
	ran := 0
	for at := range session.HostTools {
		session.HostTools[at].Execute = func(context.Context, json.RawMessage) (ToolResult, error) { ran++; return ResultOf(nil, nil), nil }
	}
	run, err := ContextualSession(context.Background(), session)
	if err != nil {
		t.Fatal(err)
	}
	defer run.Context.Close()
	run.Context.PauseClaim = func() bool { return false }
	if held := run.Context.Approval(context.Background(), "tracker__create_issue", json.RawMessage(`{}`), "call-a"); held != nil || run.Context.Execution.State.Approval != nil {
		t.Fatalf("an act call that lost the pause was held: %+v, %+v", held, run.Context.Execution.State.Approval)
	}
	for _, tool := range run.Session.HostTools {
		if tool.Name == "tracker__create_issue" {
			if _, err := tool.Execute(WithCallID(context.Background(), "call-a"), json.RawMessage(`{}`)); err == nil {
				t.Fatal("the call ran")
			}
		}
	}
	if ran != 0 {
		t.Fatalf("ran %d", ran)
	}
}

func comparison(value any) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}

// The package's words are the vectors' (contract/vectors/approval): the continuation's line and
// text, the refused result and the pending refusal.
func TestTheApprovalPausesWordsAreTheVectors(t *testing.T) {
	refused := readJSON(t, "contract/vectors/approval/continue-refused.json")
	approved := readJSON(t, "contract/vectors/approval/continue-approved.json")
	read := func(item *Value) contract.CheckpointApproval {
		var out contract.CheckpointApproval
		if err := json.Unmarshal(item.Get("checkpoint").Get("approval").JSON(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	marker := continuationMarker.FindStringSubmatch(refused.Get("continuation").Str())[1]
	for _, held := range []struct {
		vector *Value
		at     int
	}{{refused, 0}, {approved, 1}} {
		// The same line and block, and the same data: Go writes a result's members in its own order.
		want := strings.Split(held.vector.Get("continuation").Str(), "\n")
		got := strings.Split(ApprovalContinuationText(read(held.vector.Get("items").Items()[held.at]), marker), "\n")
		if len(got) != 4 || len(want) != 4 || got[0] != want[0] || got[1] != want[1] || got[3] != want[3] ||
			CanonicalJSON(MustParseJSON(got[2])) != CanonicalJSON(MustParseJSON(want[2])) {
			t.Errorf("the continuation\n%s\nnot\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
	}
	record := read(refused.Get("items").Items()[0])
	if got := comparison(contractResult(RefusedResult(record.Name))); !equalJSON(MustParseJSON(got), MustParseJSON(comparison(record.Result))) {
		t.Errorf("the refused result %s, not %s", got, comparison(record.Result))
	}
	pending := readJSON(t, "contract/vectors/approval/second-act-while-pending.json").Get("items").Items()[2].Get("detail").Str()
	var asked []string
	var sources sourcesFile
	if err := json.Unmarshal(readJSON(t, "packages/harness/certification/scenarios/tool-sources.json").JSON(), &sources); err != nil {
		t.Fatal(err)
	}
	var tools []HostTool
	for _, listed := range sources.Server {
		tools = append(tools, HostTool{Name: listed.Name, Description: listed.Description, InputSchema: listed.InputSchema,
			Source:  &ToolSource{Kind: "mcp", ID: "tracker", Trust: "public", ToolListDigest: sources.Tracker.Pinned},
			Approve: func(call ApprovalRequest) string { asked = append(asked, call.Name); return "ask" }})
	}
	wide := Defaults
	wide.OfferedSchemas, wide.OfferedSchemaBytes = 100, 1_000_000
	gate, _, _ := sourceGate(t, tools, wide, false)
	gate.SetApprovals(&book{})
	if answer, _, _ := gate.Decide(context.Background(), "tracker__create_issue", json.RawMessage(`{}`), "a", true); answer != "ask" {
		t.Fatalf("the first call answered %s", answer)
	}
	if _, detail, _ := gate.Decide(context.Background(), "tracker__create_issue", json.RawMessage(`{}`), "b", true); detail != pending {
		t.Errorf("the pending refusal\n%s\nnot\n%s", detail, pending)
	}
}

var continuationMarker = regexp.MustCompile(`BEGIN RETRIEVED DATA ([0-9a-f]{32})`)

// sourcesFile is the part of the tool-sources scenario these tests read.
type sourcesFile struct {
	Tracker struct {
		Pinned string `json:"pinned"`
	} `json:"tracker"`
	Server []struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		InputSchema json.RawMessage `json:"inputSchema"`
	} `json:"server"`
}
