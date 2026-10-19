package turn

// The contract's vectors are what this package writes, as they are what the TypeScript writes.
// Each test names the TypeScript test that holds the same vector.

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
)

// figuresOf reads the critic figures a vector's item carries.
type figuresOf struct {
	Figures *contract.ProvenanceCriticFigures `json:"figures"`
}

// refused is an HTTP answer the provider refused, as the wire reads it (failure.Refusal): the
// status alone decides the class, and no body or header is read.
type refused struct{ status int }

func (r refused) Error() string               { return "the provider refused the request" }
func (r refused) StatusCode() int             { return r.status }
func (r refused) ResponseBody() string        { return "" }
func (r refused) ResponseHeader() http.Header { return nil }

// failureOf is the failure an error carries.
func failureOf(t *testing.T, err error) contract.Failure {
	t.Helper()
	held, ok := err.(*failure.Error)
	if !ok {
		t.Fatalf("the error carries no failure: %v", err)
	}
	return held.Failure
}

// root walks up from the working directory to the directory holding contract/vectors.
func root(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "contract", "vectors")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no directory above this one holds contract/vectors")
		}
		dir = parent
	}
}

type vectorFile struct {
	Items   []json.RawMessage `json:"items"`
	Answers []string          `json:"answers"`
	// NotCalls are the answers of tool-call/text-call.json that make no call.
	NotCalls []struct {
		Answer  string `json:"answer"`
		Problem string `json:"problem"`
	} `json:"notCalls"`
}

func vector(t *testing.T, name string) vectorFile {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root(t), "contract", "vectors", name))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var file vectorFile
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return file
}

// harnessJSON reads one file of the TypeScript harness, for the fixtures its own tests read.
func harnessJSON(t *testing.T, name string) *jvalue {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root(t), "packages", "harness", name))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	parsed, err := parseJSON(data)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return parsed
}

// same compares a value this package built with a vector's item, by content and not by member
// order.
func same(t *testing.T, label string, got any, want json.RawMessage) {
	t.Helper()
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("%s: %v", label, err)
	}
	if canonical(value(encoded)) != canonical(value(want)) {
		t.Errorf("%s:\n got %s\nwant %s", label, canonical(value(encoded)), canonical(value(want)))
	}
}

func text(t *testing.T, label, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("%s:\n got %q\nwant %q", label, got, want)
	}
}

// critic.test.ts: "tool-offer/critic-tools.json is the allow-list".
func TestCriticToolsVector(t *testing.T) {
	file := vector(t, "tool-offer/critic-tools.json")
	same(t, "the critic's allow-list", append(append([]string{}, CriticTools...), CriticReaders...), mustArray(t, file.Items))
	// The gate's lists are these, which is why this package reuses them rather than writing them
	// a second time.
	if len(CriticTools) != 4 || len(CriticReaders) != 2 {
		t.Fatalf("the allow-list is four runtime reads and two context readers, got %v and %v", CriticTools, CriticReaders)
	}
	for _, name := range CriticReaders {
		if CriticMayCall(name) {
			t.Errorf("%s is a reader, not a runtime tool the critic may call", name)
		}
	}
	for _, name := range []string{"list_examples", "get_example", "write_file", "toString", "__proto__", "propose_anything"} {
		if CriticMayCall(name) || CriticMayRead(name) {
			t.Errorf("%s is not one of the critic's tools", name)
		}
	}
	text(t, "the refusal", CriticRefusalDetail("write_file"),
		"write_file is not one of the tools the critic may call (validate, experimental_evaluate, get_schema, experimental_test_cases, read_window, search); the call did not leave the page and nothing was written")
}

// mustArray is the items of a vector whose items are plain values, as one array.
func mustArray(t *testing.T, items []json.RawMessage) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

// critic.test.ts reads PROPOSE_PACK_SCHEMA for the tools a critic may not reach; the vector is the
// declaration a host makes.
func TestProposePackVector(t *testing.T) {
	file := vector(t, "tool-offer/propose-pack.json")
	declared := value(file.Items[0])
	text(t, "the tool's name", declared.get("name").s, ProposePackTool)
	same(t, "propose_pack's input schema", ProposePackSchema, raw(declared.get("inputSchema")))
}

// engines/vercel/capabilities.test.ts holds extractToolCall to each answer of this vector.
func TestTextCallVector(t *testing.T) {
	file := vector(t, "tool-call/text-call.json")
	if len(file.Answers) != len(file.Items) {
		t.Fatalf("the vector states %d answers for %d calls", len(file.Answers), len(file.Items))
	}
	for at, answer := range file.Answers {
		call, problem := ExtractToolCall(answer)
		if call == nil {
			t.Fatalf("answer %d made no call: %s", at, problem)
		}
		made := jobj()
		made.set("tool", jstr(call.Tool))
		made.set("arguments", value(call.Arguments))
		same(t, "the call the answer made", raw(made), file.Items[at])
	}
	for _, held := range file.NotCalls {
		call, problem := ExtractToolCall(held.Answer)
		if call != nil {
			t.Errorf("an answer the vector says makes no call made one: %v", call)
		}
		text(t, "the problem the model is told", problem, held.Problem)
	}
}

// critic.test.ts: "critic/declared-cases.json: cases the reader reads, as the matrix the critic is
// shown".
func TestDeclaredCasesVector(t *testing.T) {
	file := vector(t, "critic/declared-cases.json")
	cases, err := ReadCriticCases(mustArray(t, file.Items))
	if err != nil {
		t.Fatalf("the reader refused the vector's cases: %v", err)
	}
	if len(cases) != len(file.Items) {
		t.Fatalf("read %d cases of %d", len(cases), len(file.Items))
	}
	for at, held := range cases {
		same(t, "the case read whole", held.Case, file.Items[at])
	}
	for _, row := range value(CriticMatrix(cases)).get("cases").a {
		if row.has("source") {
			t.Error("a matrix row carries source, which the runtime's matrix does not have")
		}
	}
	// The request's own case states part of a disposition; the saved tests state the whole of one.
	if Testable(cases[0]) {
		t.Error("a case stating kind and outcomeId alone is compared by rehearsal only")
	}
	for _, at := range []int{1, 2, 3} {
		if !Testable(cases[at]) {
			t.Errorf("case %s states a whole disposition or an error class", cases[at].ID)
		}
	}
}

// critic.test.ts: "critic/declared-cases-members.json: a case with every member beside its facts
// and expectation is read whole".
func TestDeclaredCaseMembersVector(t *testing.T) {
	file := vector(t, "critic/declared-cases-members.json")
	cases, err := ReadCriticCases(mustArray(t, file.Items))
	if err != nil {
		t.Fatalf("the reader refused the vector's case: %v", err)
	}
	same(t, "the case read whole", cases[0].Case, file.Items[0])
	for _, member := range []string{"evidenceAvailability", "supportedExtensions", "expectedHandoffTarget", "origin", "specSection", "cites"} {
		if !value(cases[0].Case).has(member) {
			t.Errorf("the case kept %s", member)
		}
	}
}

// critic.test.ts: "critic/outcome-refuted.json: the critique of a seeded wrong outcome". The
// checks and the disposition are the runtime's answers in the certification fixture (T5 and T6).
func TestOutcomeRefutedVector(t *testing.T) {
	file := vector(t, "critic/outcome-refuted.json")
	scenario := harnessJSON(t, "certification/scenario.json")
	draft := raw(scenario.get("documents").get("DRAFT_V2"))
	facts := raw(scenario.get("documents").get("FACTS"))
	small := json.RawMessage(`{"claim":{"type":"expense-reimbursement","amount":"40.00","receiptAttached":false}}`)
	recorder := OpenCritique(CritiqueOptions{Document: draft, Cases: []CriticCase{
		{Source: "declared", ID: "receipted-120", Facts: facts, ExpectedDisposition: json.RawMessage(`{"kind":"outcome","outcomeId":"decline"}`)},
		{Source: "test", ID: "small-40", Facts: small, ExpectedDisposition: json.RawMessage(`{"kind":"outcome","outcomeId":"approve"}`)},
	}})
	recording := harnessJSON(t, "certification/runtime.json")
	for _, step := range []string{"T5", "T6"} {
		call := recordedCall(t, recording, step)
		tool := "experimental_evaluate"
		if step == "T5" {
			tool = "validate"
		}
		recorder.Saw(tool, answerText(call), raw(call.get("arguments")), raw(call.get("result").get("structuredContent")))
	}
	critique := recorder.Critique("I could not refute it.")
	var figures figuresOf
	if err := json.Unmarshal(file.Items[0], &figures); err != nil {
		t.Fatal(err)
	}
	critique.Figures = figures.Figures
	same(t, "the critique event", CritiqueEvent(critique), file.Items[0])
	// The proposal carries the verdict and the case nothing compared.
	same(t, "the proposal's critique", CritiqueOnProposal(&critique), raw(value(file.Items[1]).get("critique")))
}

// recordedCall is one call of the certification recording, by its step.
func recordedCall(t *testing.T, recording *jvalue, step string) *jvalue {
	t.Helper()
	for _, call := range recording.get("calls").a {
		if held := call.get("step"); held != nil && held.kind == jstring && held.s == step {
			return call
		}
	}
	t.Fatalf("the recording has no step %s", step)
	return nil
}

// answerText is the text half of a recorded MCP result (TextOf, read off the fixture's value).
func answerText(call *jvalue) string {
	content := call.get("result").get("content")
	if !content.isArray() || len(content.a) == 0 {
		return ""
	}
	held := content.a[0].get("text")
	if held == nil || held.kind != jstring {
		return ""
	}
	return held.s
}

// critic.test.ts: "critic/test-run-compared.json: a test run's rows of the saved tests stating a
// whole disposition".
func TestTestRunComparedVector(t *testing.T) {
	file := vector(t, "critic/test-run-compared.json")
	declared := vector(t, "critic/declared-cases.json")
	scenario := harnessJSON(t, "certification/scenario.json")
	draft := raw(scenario.get("documents").get("DRAFT_V2"))
	cases, err := ReadCriticCases(mustArray(t, declared.Items[1:3]))
	if err != nil {
		t.Fatal(err)
	}
	approve := `{"handoff":{"state":"none"},"kind":"outcome","outcomeId":"approve","reasons":[]}`
	decline := `{"handoff":{"state":"none"},"kind":"outcome","outcomeId":"decline","reasons":[]}`
	report := `{"status":"failed","packs":[{"rows":[` +
		`{"id":"small-40","status":"passed","expected":` + quoteJS(approve) + `,"actual":` + quoteJS(approve) + `},` +
		`{"id":"unreceipted-120","status":"failed","expected":` + quoteJS(decline) + `,"actual":` + quoteJS(approve) + `}]}]}`
	args := jobj()
	args.set("pack", jstr(value(draft).stringify()))
	args.set("matrix", jstr(string(CriticMatrix(cases))))
	recorder := OpenCritique(CritiqueOptions{Document: draft, Cases: cases})
	recorder.Saw("experimental_test_cases", report, raw(args), json.RawMessage(report))
	critique := recorder.Critique("")
	var figures figuresOf
	if err := json.Unmarshal(file.Items[0], &figures); err != nil {
		t.Fatal(err)
	}
	critique.Figures = figures.Figures
	same(t, "the critique event", CritiqueEvent(critique), file.Items[0])
	// A test run is not a status check: only its rows of declared cases count.
	if len(critique.Checks) != 0 {
		t.Errorf("a test run produced %d checks", len(critique.Checks))
	}
}

// critic.test.ts: "critic/error-outcome-compared.json: an expected error class and phase agree
// with the runtime's". The vector's text is the harness's own call's, which the TypeScript test
// does not compare either; what is held here is the comparison.
func TestErrorOutcomeComparedVector(t *testing.T) {
	file := vector(t, "critic/error-outcome-compared.json")
	critique := value(file.Items[2])
	if critique.get("refuted").b {
		t.Error("an expected error class the runtime gave does not refute")
	}
	comparison := critique.get("cases").a[0]
	var expected, actual contract.RunEventCaseOutcome
	if err := json.Unmarshal(raw(comparison.get("expected")), &expected); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw(comparison.get("actual")), &actual); err != nil {
		t.Fatal(err)
	}
	if !OutcomeAgrees(expected, actual) {
		t.Error("the declared error class and phase agree with the runtime's")
	}
	if !comparison.get("agrees").b {
		t.Error("the vector states that they agree")
	}
	// The harness's own calls are marked (review row 8).
	for _, at := range []int{0, 1} {
		if origin := file.Items[at]; !value(origin).has("origin") {
			t.Errorf("item %d is a call the harness made and carries origin", at)
		}
	}
}

// critic.test.ts: "critic/figures.json: what each engine's critique carries". Items 1 and 2 are an
// agent adapter's own count of its runs; what this package computes is the model adapter's.
func TestFiguresVectors(t *testing.T) {
	for _, held := range []struct {
		file                                  string
		input, output, reasoning, read, write int64
	}{
		{"critic/figures.json", 1205, 48, 0, 900, -1},
		{"critic/figures-cache-write.json", 1500, 40, 16, 1000, 500},
	} {
		file := vector(t, held.file)
		row := func() contract.Provenance {
			completion := contract.ProvenanceCompletion{FinishReason: "stop", InputTokens: &held.input, OutputTokens: &held.output,
				ReasoningTokens: &held.reasoning, CacheReadTokens: &held.read}
			if held.write >= 0 {
				write := held.write
				completion.CacheWriteTokens = &write
			}
			return contract.Provenance{Request: 1, Phase: "review", Result: completion}
		}
		model := value(file.Items[0]).get("model").s
		figures := ModelCriticFigures(model, CriticStart{Used: 1, Rows: 1},
			CriticEnd{Used: 3, Completions: []contract.Provenance{{Request: 1, Phase: "author"}, row(), row()}})
		same(t, held.file, figures, file.Items[0])
	}
}

// critic.test.ts: "proposal/unreadable.json" — the answer whose marked fence is not valid JSON is
// delivered whole, and the failure is the host-authored invalid-response.
func TestUnreadableProposalVector(t *testing.T) {
	file := vector(t, "proposal/unreadable.json")
	answer := value(file.Items[1]).get("text").s
	if _, err := ExtractProposal(answer); err == nil {
		t.Fatal("the fence is not valid JSON and the proposal cannot be read")
	} else {
		same(t, "the failure", failureOf(t, err), raw(value(file.Items[2]).get("failure")))
		text(t, "the error's message", err.Error(), value(file.Items[2]).get("message").s)
	}
	text(t, "the whole answer", WholeAnswer(answer), answer)
}

// critic.test.ts: "proposal/critic-unfinished.json" and
// delegation/propose-pack's turn note: the note a proposal carries where what ran after it did not
// finish.
func TestProposalNoteVectors(t *testing.T) {
	for _, held := range []struct{ file, kind string }{
		{"proposal/critic-unfinished.json", "critic-unfinished"},
		{"proposal/turn-unfinished.json", "turn-unfinished"},
	} {
		file := vector(t, held.file)
		var proposal *jvalue
		for _, item := range file.Items {
			if value(item).get("type").s == "proposal" {
				proposal = value(item)
			}
		}
		if proposal == nil {
			t.Fatalf("%s: no proposal event", held.file)
		}
		note := proposal.get("note")
		var figures figuresOf
		if err := json.Unmarshal(raw(note), &figures); err != nil {
			t.Fatal(err)
		}
		built := ProposalNote(held.kind, refused{status: 503}, "model")
		built.Figures = figures.Figures
		same(t, held.file, built, raw(note))
	}
}

// critic.test.ts and engine.test.ts: "proposal/propose-pack.json" — the proposal the adapter reads
// out of the model's own call, and what the model is told in answer.
func TestProposePackProposalVector(t *testing.T) {
	file := vector(t, "proposal/propose-pack.json")
	call, result, proposal := value(file.Items[0]), value(file.Items[1]), value(file.Items[3])
	read, err := ReadProposedPack(raw(call.get("args")))
	if err != nil {
		t.Fatalf("the call's arguments are a proposal: %v", err)
	}
	same(t, "the document", read.Document, raw(proposal.get("document")))
	same(t, "the unknowns", read.Unknowns, raw(proposal.get("unknowns")))
	text(t, "what the model is told", result.get("text").s, ProposalReceived)
}

// question/interview.json: the Desk's interview question, as ask_user carries it.
func TestInterviewQuestionVector(t *testing.T) {
	file := vector(t, "question/interview.json")
	read, err := ReadModelQuestion(file.Items[0])
	if err != nil {
		t.Fatalf("the reader refused the vector's question: %v", err)
	}
	same(t, "the question read at the model boundary", read, file.Items[0])
	strict, err := ReadUserQuestion(file.Items[0])
	if err != nil {
		t.Fatalf("the saved history's reader refused it: %v", err)
	}
	same(t, "the question read strictly", strict, file.Items[0])
}

// delegation.test.ts: "expect(profilesVector.items).toEqual(TASK_PROFILES)".
func TestProfilesVector(t *testing.T) {
	file := vector(t, "delegation/profiles.json")
	same(t, "the closed profiles", TaskProfiles, mustArray(t, file.Items))
	read, err := ReadProfiles(TaskProfiles)
	if err != nil {
		t.Fatalf("the closed profiles are within the ceiling: %v", err)
	}
	if len(read) != len(TaskProfiles) {
		t.Fatal("ReadProfiles returns the profiles it was handed")
	}
}

// delegation.test.ts: "validates bounded delegation before pausing or creating work", over the
// request the vector states.
func TestRequestByReferenceVector(t *testing.T) {
	file := vector(t, "delegation/request-by-reference.json")
	read, err := ReadDelegationRequest(file.Items[0])
	if err != nil {
		t.Fatalf("the reader refused the vector's request: %v", err)
	}
	same(t, "the request read closed", read, file.Items[0])
}

// delegationResults.test.ts: "reads each profile's report vector in its own shape".
func TestReportVectors(t *testing.T) {
	for _, held := range []struct{ file, kind string }{
		{"delegation/report-analyze.json", "analyze"},
		{"delegation/report-analyze-proposal.json", "analyze"},
		{"delegation/report-review.json", "review"},
		{"delegation/report-research.json", "research"},
		{"delegation/report-author-test.json", "author-test"},
	} {
		file := vector(t, held.file)
		for at, item := range file.Items {
			report, problem := ReadReport(held.kind, item)
			if problem != "" {
				t.Fatalf("%s item %d: %s", held.file, at, problem)
			}
			same(t, held.file, report, item)
			// The task record's reader does not know the profile's shape and admits every one.
			if _, problem := ReadReport("any", item); problem != "" {
				t.Errorf("%s item %d read as any: %s", held.file, at, problem)
			}
		}
	}
}

// delegation.test.ts: a child's brief carries the members the contract names, and no other.
func TestBriefVectors(t *testing.T) {
	for _, name := range []string{"delegation/brief.json", "delegation/brief-partial.json"} {
		file := vector(t, name)
		for at, item := range file.Items {
			var brief Brief
			if err := json.Unmarshal(item, &brief); err != nil {
				t.Fatalf("%s item %d: %v", name, at, err)
			}
			same(t, name, BriefLine(brief), item)
			prompt := ChildPrompt(TaskProfiles[2], brief)
			if last := prompt[len(prompt)-len(BriefLine(brief)):]; last != string(BriefLine(brief)) {
				t.Errorf("%s: the prompt ends with the brief's JSON line, got %q", name, last)
			}
		}
	}
}
