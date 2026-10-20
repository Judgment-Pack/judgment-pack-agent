package turn

// Delegation's contract: the ported cases of delegation.test.ts's contract half ("validates
// bounded delegation before pausing or creating work", the profiles held to their vector and their
// ceiling) and of delegationResults.test.ts ("reads each profile's report vector in its own shape,
// and refuses one that breaks the shape"; the task record's reader).

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
)

// delegation.test.ts: a feature adapter may add a profile, never a larger allowance or a tool off
// the gate's ceiling.
func TestReadProfilesHoldsTheCeiling(t *testing.T) {
	wide := TaskProfile{ID: "wide", Description: "d", Instructions: "i", Capabilities: []string{},
		Budget: &TaskBudget{ModelRequests: TaskBudgetCeiling.ModelRequests, ToolCalls: TaskBudgetCeiling.ToolCalls + 1, Milliseconds: TaskBudgetCeiling.Milliseconds}}
	if _, err := ReadProfiles(append(append([]TaskProfile{}, TaskProfiles...), wide)); err == nil {
		t.Error("a profile over the ceiling is refused")
	}
	files := TaskProfile{ID: "files", Description: "d", Instructions: "i", Capabilities: []string{}, RuntimeTools: []string{"get_pack"}}
	if _, err := ReadProfiles(append(append([]TaskProfile{}, TaskProfiles...), files)); err == nil {
		t.Error("a Runtime tool off the gate's ceiling is refused")
	}
	mine := TaskProfile{ID: "mine", Description: "d", Instructions: "i", Capabilities: []string{"read"}}
	if _, err := ReadProfiles(append(append([]TaskProfile{}, TaskProfiles...), mine)); err != nil {
		t.Errorf("a profile within the ceiling is admitted: %v", err)
	}
	if _, err := ReadProfiles(append(append([]TaskProfile{}, TaskProfiles...), TaskProfiles[0])); err == nil {
		t.Error("a repeated profile id is refused")
	}
	for _, held := range []TaskProfile{
		{ID: "elsewhere", Description: "d", Instructions: "i", Capabilities: []string{}, Model: ptr("host")},
		{ID: "unshaped", Description: "d", Instructions: "i", Capabilities: []string{}, Report: ptr("summary")},
		{ID: "unbounded", Description: "d", Instructions: "i", Capabilities: []string{}, Budget: &TaskBudget{ModelRequests: 0, ToolCalls: 1, Milliseconds: 1}},
	} {
		if _, err := ReadProfiles([]TaskProfile{held}); err == nil {
			t.Errorf("%s is refused", held.ID)
		}
	}
	// A profile that declares no budget gets the default.
	same(t, "the default budget", ProfileBudget(mine), json.RawMessage(`{"modelRequests":6,"toolCalls":6,"milliseconds":120000}`))
	same(t, "the research profile's own", ProfileBudget(TaskProfiles[2]), json.RawMessage(`{"modelRequests":10,"toolCalls":12,"milliseconds":360000}`))
	// The author-and-test profile is offered the gate's ceiling, and nothing beside it.
	same(t, "the author-test tools", AuthorTestTools, mustArray(t, rawEach(gate.Ceiling)))
}

func rawEach(list []string) []json.RawMessage {
	out := make([]json.RawMessage, 0, len(list))
	for _, name := range list {
		out = append(out, json.RawMessage(quoteJS(name)))
	}
	return out
}

// delegation.test.ts: "validates bounded delegation before pausing or creating work".
func TestReadDelegationRequestBounds(t *testing.T) {
	task := func(title string) string {
		return `{"title":"` + title + `","profile":"review","objective":"Check the supplied claims (` + title + `)."}`
	}
	many := []string{}
	for at := 0; at < 4; at++ {
		many = append(many, task("Task "+jsNumber(float64(at))))
	}
	references := []string{}
	for at := 0; at < 21; at++ {
		references = append(references, quoteJS("doc-"+jsNumber(float64(at))))
	}
	for _, request := range []string{
		`{"tasks":[` + strings.Join(many, ",") + `]}`,
		`{"tasks":[{"title":"t","profile":"review","objective":"o","context":"Provided company description."}]}`,
		`{"tasks":[{"title":"t","profile":"review","objective":"o","references":[` + strings.Join(references, ",") + `]}]}`,
		`{"tasks":[{"title":"t","profile":"review","objective":"o","resume":"not-a-task-id"}]}`,
		`{"tasks":[{"title":"t","profile":"review","objective":"o","autoApprove":true}]}`,
		`{"tasks":[]}`,
		`{"tasks":[{"title":"","profile":"review","objective":"o"}]}`,
		`{"tasks":[{"title":"t","profile":"review","objective":"o","summarize":"yes"}]}`,
		`{"tasks":{"title":"t"}}`,
		`{"batch":[{"title":"t","profile":"review","objective":"o"}]}`,
	} {
		if _, err := ReadDelegationRequest(json.RawMessage(request)); err == nil {
			t.Errorf("the reader refuses %s", request)
		} else if failureOf(t, err).Code != "invalid-tool-input" {
			t.Errorf("%s: the class the model reads and corrects", request)
		}
	}
	read, err := ReadDelegationRequest(json.RawMessage(`{"tasks":[{"title":"t","profile":"review","objective":"o","references":["policy"],"summarize":true}]}`))
	if err != nil {
		t.Fatalf("a bounded request reads: %v", err)
	}
	same(t, "the references it carries", read.Tasks[0].References, json.RawMessage(`["policy"]`))
	if read.Tasks[0].Summarize == nil || !*read.Tasks[0].Summarize {
		t.Error("the summarize member is kept")
	}
	text(t, "the tool's interaction", *DelegationTool(TaskProfiles).Interaction, "delegation")
}

// The delegate_tasks declaration a parent run is offered: the profiles it may name and their
// limits, in the schema the host writes.
func TestDelegationToolDeclaration(t *testing.T) {
	declared := DelegationTool(TaskProfiles)
	text(t, "the tool's name", declared.Name, DelegateTool)
	schema := value(declared.InputSchema)
	items := schema.get("properties").get("tasks").get("items")
	same(t, "the task's required members", raw(items.get("required")), json.RawMessage(`["title","objective","profile"]`))
	if items.get("additionalProperties").b {
		t.Error("the task's shape is closed")
	}
	profile := items.get("properties").get("profile")
	var ids []string
	for _, held := range profile.get("enum").a {
		ids = append(ids, held.s)
	}
	same(t, "the profiles the model may name", ids, json.RawMessage(`["analyze","review","research","author-test"]`))
	for _, want := range []string{
		"analyze: Analyze supplied context without calling tools. Up to 6 model requests, 6 tool calls, 120 seconds.",
		"research: Research one focused question using the parent’s permitted source tools. Up to 10 model requests, 12 tool calls, 360 seconds.",
		"author-test: Validate a draft pack or run its tests with the Runtime’s read-only tools. Up to 8 model requests, 8 tool calls, 240 seconds; Runtime tools get_schema, list_examples, get_example, validate, experimental_evaluate, experimental_test_cases.",
	} {
		if !strings.Contains(profile.get("description").s, want) {
			t.Errorf("the profile's description says %q:\n%s", want, profile.get("description").s)
		}
	}
	if max := items.get("properties").get("references").get("maxItems").n; max != float64(MaxReferences) {
		t.Errorf("the references are bounded at %v", max)
	}
}

// The instructions a parent and a child are told, and the limits they state.
func TestDelegationInstructions(t *testing.T) {
	if !strings.Contains(DelegationInstructions, "at most 20 tool calls in all, yours and your children’s together") {
		t.Errorf("the shared tool-call limit is stated: %s", DelegationInstructions)
	}
	if !strings.Contains(DelegationInstructions, "up to 4 batches") {
		t.Error("the batch limit is stated")
	}
	if !strings.Contains(AgentDelegationInstructions, "at most 5 agent runs") {
		t.Error("an agent capability's parent is told its run limit")
	}
	// delegationResults.test.ts: the shape the child is told is the one its reader holds.
	if !strings.Contains(ReportInstructions("review"), `"severity": "gap" | "inconsistency" | "unsupported"`) {
		t.Error("a review child is told its severity")
	}
	if !strings.Contains(ReportInstructions("author-test"), `"check": the Runtime tool you ran`) {
		t.Error("an author-test child is told to name its check")
	}
	if !strings.Contains(ReportInstructions("analyze"), "openQuestions: at most 4") {
		t.Error("the child's own open questions are bounded at four")
	}
	// Every profile's child is told its own shape, and the brief's line ends the prompt.
	for _, profile := range TaskProfiles {
		prompt := ChildPrompt(profile, Brief{Objective: "o", TimeLimitSeconds: 120})
		if !strings.HasPrefix(prompt, profile.Instructions+"\n\n"+ChildRules) {
			t.Errorf("%s: the profile's instructions and the rules every child is held to", profile.ID)
		}
		if !strings.Contains(prompt, ReportInstructions(*profile.Report)) {
			t.Errorf("%s: its report shape", profile.ID)
		}
		if strings.Contains(prompt, ReferenceRule) || strings.Contains(prompt, ResumeRule) {
			t.Errorf("%s: a task with no references and no earlier work is told neither rule", profile.ID)
		}
	}
	with := ChildPrompt(TaskProfiles[2], Brief{Objective: "o", TimeLimitSeconds: 360,
		References: []contract.DelegatedTaskBriefReference{{ID: "chat-1:policy:0000000000000000", Label: "Policy", Kind: "artifact", Bytes: 10}}})
	if !strings.Contains(with, ReferenceRule) {
		t.Error("a task given material by reference is told the reference rule")
	}
}

// delegationResults.test.ts: "refuses one that breaks the shape".
func TestReadReportRefusals(t *testing.T) {
	review := vector(t, "delegation/report-review.json").Items[0]
	analyze := vector(t, "delegation/report-analyze.json").Items[0]
	research := vector(t, "delegation/report-research.json").Items[0]
	authorTest := vector(t, "delegation/report-author-test.json").Items[0]
	for _, held := range []struct{ kind, report, why string }{
		// A review finding without its severity, a research finding citing nothing, an
		// author-test finding naming no check.
		{"review", replaceMember(review, "findings", `[{"text":"A gap."}]`), "severity"},
		{"research", replaceMember(research, "findings", `[{"text":"The rate is 0.58."}]`), "at least one source"},
		{"author-test", replaceMember(authorTest, "findings", `[{"text":"Valid.","status":"passed"}]`), "Runtime check"},
		// A finding citing an id the report does not list, and a member no shape names.
		{"analyze", replaceMember(analyze, "findings", `[{"text":"x","citations":["elsewhere"]}]`), "citations"},
		{"analyze", replaceMember(analyze, "approved", `true`), "approved"},
		{"analyze", replaceMember(analyze, "confidence", `"certain"`), "confidence must be low, medium or high"},
		{"analyze", replaceMember(analyze, "summary", `""`), "summary must be text"},
		{"analyze", replaceMember(analyze, "notDone", `[1]`), "notDone must be at most"},
		{"analyze", replaceMember(analyze, "openQuestions", `[{"kind":"other","text":"x"}]`), "openQuestions must be at most"},
		{"analyze", `[]`, "report is not an object"},
	} {
		_, problem := ReadReport(held.kind, json.RawMessage(held.report))
		if problem == "" {
			t.Errorf("%s is refused", held.report)
			continue
		}
		if !strings.Contains(problem, held.why) {
			t.Errorf("%s:\n got %q\nwant it to name %q", held.report, problem, held.why)
		}
	}
	// The analyze shape does not name a severity, and the record's reader admits every shape's.
	if _, problem := ReadReport("analyze", review); problem == "" {
		t.Error("a review report is not an analyze report")
	}
	if _, problem := ReadReport("any", review); problem != "" {
		t.Errorf("the record's reader admits every profile's shape: %s", problem)
	}
}

// replaceMember writes one member of a JSON object, as the TypeScript's spread does.
func replaceMember(report json.RawMessage, member, held string) string {
	out := value(report).clone()
	out.set(member, value(json.RawMessage(held)))
	return out.stringify()
}

// delegationResults.test.ts: every task record the coordinator emits is one the contract names.
func TestValidDelegatedTask(t *testing.T) {
	record := `{"id":"5d8e2f40-1a6b-4c3d-8e9f-0a1b2c3d4e5f","parentRunId":"6d8e2f40-1a6b-4c3d-8e9f-0a1b2c3d4e5f",` +
		`"title":"Check evidence","objective":"Check the supplied claims.","profile":"review","state":"complete",` +
		`"createdAt":"2026-10-19T10:00:00.000Z","finishedAt":"2026-10-19T10:01:00.000Z","modelRequests":1,"toolCalls":0,` +
		`"attempts":1,"steps":[{"name":"run","state":"complete"}],"result":"One unsupported claim.",` +
		`"budget":{"modelRequests":6,"toolCalls":6,"milliseconds":120000},"model":"fixture"}`
	if !ValidDelegatedTask(json.RawMessage(record)) {
		t.Fatal("the record the coordinator writes is one the contract names")
	}
	for _, held := range []struct{ why, change string }{
		{"an id that is not a uuid", `"id":"task-1"`},
		{"a state the contract does not name", `"state":"paused"`},
		{"a created time that is not a date", `"createdAt":"the other day"`},
		{"more attempts than the record carries", `"attempts":9`},
		{"a budget member the contract does not name", `"budget":{"modelRequests":6,"toolCalls":6,"milliseconds":120000,"pages":2}`},
		{"a step with no name", `"steps":[{"state":"complete"}]`},
		{"a failure the catalogue does not admit", `"model":"fixture","failure":{"version":1,"code":"made-up","source":"model","outcome":"unknown"}`},
		{"a retried member with no failure", `"model":"fixture","retried":{"waitMs":1000}`},
		{"token counts the contract does not name", `"model":"fixture","tokens":{"thinkingTokens":1}`},
		{"a partial that is not true", `"model":"fixture","partial":false`},
	} {
		broken := strings.Replace(record, `"model":"fixture"`, held.change, 1)
		if ValidDelegatedTask(json.RawMessage(broken)) {
			t.Errorf("%s is refused", held.why)
		}
	}
	// A member the contract does not name is kept: the record is open.
	open := strings.Replace(record, `"model":"fixture"`, `"model":"fixture","hostNote":"kept"`, 1)
	if !ValidDelegatedTask(json.RawMessage(open)) {
		t.Error("a member the contract does not name is kept")
	}
	// The report a complete task carries is read in no profile's shape in particular.
	report := strings.Replace(record, `"model":"fixture"`, `"model":"fixture","report":`+
		string(vector(t, "delegation/report-review.json").Items[0]), 1)
	if !ValidDelegatedTask(json.RawMessage(report)) {
		t.Error("the record's reader admits a report of any profile's shape")
	}
	// Token counts, each a safe integer where present.
	if !ValidTokens(json.RawMessage(`{"inputTokens":10,"cacheReadTokens":0}`)) {
		t.Error("the token counts a provider reported")
	}
	if ValidTokens(json.RawMessage(`{"inputTokens":-1}`)) {
		t.Error("a negative count is not one")
	}
}

// The line the response's next run on an agent capability starts its delegation part with
// (ADR-0007) is the contract's (delegation/continuation.json), the call's result the data block
// after it.
func TestTheContinuationLineIsTheVectors(t *testing.T) {
	var line string
	if err := json.Unmarshal(vector(t, "delegation/continuation.json").Items[0], &line); err != nil {
		t.Fatal(err)
	}
	first, _, _ := strings.Cut(line, "\n")
	if first != Continuation {
		t.Fatalf("the continuation line is\n%s\nand the vector's\n%s", Continuation, first)
	}
}
