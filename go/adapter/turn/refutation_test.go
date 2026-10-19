package turn

// The three rules ADR-0001 holds the refutation pass to, each measured here: the ported cases of
// refutation.test.ts, of critic.test.ts's own units and of criticDefault.test.ts.

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

const (
	validAnswer     = `{"status":"valid","diagnostics":[]}`
	invalidAnswer   = `{"status":"invalid","diagnostics":[{"code":"JPS-SEMANTIC-UNRESOLVED-OUTCOME"}]}`
	evaluatedAnswer = `{"status":"evaluated","rehearsal":true}`
)

// refutation.test.ts, "what counts as a check".
func TestWhatCountsAsACheck(t *testing.T) {
	// A question about the runtime — the schema, the examples — is not evidence about a document.
	names := make([]string, 0, len(SettledStatus))
	for name := range SettledStatus {
		names = append(names, name)
	}
	sort.Strings(names)
	same(t, "the tools that say something about the document", names, json.RawMessage(`["experimental_evaluate","validate"]`))
	// The reason this is a table and not the constant 'valid'.
	text(t, "validate's settled word", SettledStatus["validate"], "valid")
	text(t, "experimental_evaluate's settled word", SettledStatus["experimental_evaluate"], "evaluated")

	status, diagnostics, held := StatusOf(invalidAnswer)
	if !held || status != "invalid" || diagnostics != 1 {
		t.Errorf("the runtime's status and diagnostic count: %q %d %v", status, diagnostics, held)
	}
	if status, diagnostics, held = StatusOf(validAnswer); !held || status != "valid" || diagnostics != 0 {
		t.Errorf("the runtime's status and diagnostic count: %q %d %v", status, diagnostics, held)
	}
	if _, _, held = StatusOf("not json"); held {
		t.Error("an answer that is not JSON carries no status")
	}
	if _, _, held = StatusOf(`{"diagnostics":[]}`); held {
		t.Error("an answer with no status carries none")
	}

	recorder := OpenCritique(CritiqueOptions{})
	recorder.Saw("get_schema", `{"status":"ok"}`, nil, nil)
	recorder.Saw("list_examples", `{"status":"ok"}`, nil, nil)
	recorder.Saw("validate", validAnswer, nil, nil)
	same(t, "only the answers about the document", recorder.Critique("").Checks,
		json.RawMessage(`[{"Tool":"validate","Status":"valid","Diagnostics":0}]`))

	// The verdict is a thing the runtime said. This used to record a `refused` status of the
	// desk's own invention, so a validate the gate never let out of the page rendered as the
	// runtime refuting the proposal. An answer with no status is not a check, whatever it says.
	silent := OpenCritique(CritiqueOptions{})
	silent.Saw("validate", "refused: write_file is not one of the tools this assistant may call", nil, nil)
	silent.Saw("validate", "the runtime is unreachable", nil, nil)
	silent.Saw("experimental_evaluate", "", nil, nil)
	critique := silent.Critique("")
	if len(critique.Checks) != 0 || critique.Refuted || critique.Text != NoChecks {
		t.Errorf("an answer with no runtime status is not a check: %+v", critique)
	}

	// The table is read, and nothing behind it.
	for _, name := range []string{"toString", "constructor", "valueOf", "hasOwnProperty", "__proto__"} {
		if IsCheckTool(name) {
			t.Errorf("%s is not a check tool", name)
		}
		held := OpenCritique(CritiqueOptions{})
		held.Saw(name, `{"status":"invalid","diagnostics":[{"code":"X"}]}`, nil, nil)
		if len(held.Critique("").Checks) != 0 {
			t.Errorf("%s produced a check from a table that does not name it", name)
		}
	}
	for _, name := range []string{"validate", "experimental_evaluate"} {
		if !IsCheckTool(name) {
			t.Errorf("%s is a check tool", name)
		}
	}
	if IsCheckTool("get_schema") {
		t.Error("get_schema is a question about the runtime, not a check")
	}
}

// refutation.test.ts, "the verdict, and where it does not come from".
func TestTheVerdict(t *testing.T) {
	settled := []Check{{Tool: "validate", Status: "valid"}, {Tool: "experimental_evaluate", Status: "evaluated"}}
	if VerdictOf(settled, nil, nil) {
		t.Error("every check came back as the runtime's own settled word")
	}
	broken := []Check{{Tool: "validate", Status: "invalid", Diagnostics: 1}, {Tool: "experimental_evaluate", Status: "evaluated"}}
	if !VerdictOf(broken, nil, nil) {
		t.Error("a check that did not refutes")
	}
	// ADR-0001's first rule, at the verdict: with nothing checked there is nothing refuted and
	// nothing to say "not refuted" about.
	if VerdictOf(nil, nil, nil) {
		t.Error("no check at all is not a refutation")
	}

	// The prose is the model's and the verdict is the runtime's, and neither is allowed to be the
	// other.
	said := OpenCritique(CritiqueOptions{})
	said.Saw("validate", validAnswer, nil, nil)
	if said.Critique("REFUTATION: this pack is broken and must not be used.").Refuted {
		t.Error("the critic's prose does not refute a valid document")
	}
	other := OpenCritique(CritiqueOptions{})
	other.Saw("validate", invalidAnswer, nil, nil)
	if !other.Critique("REFUTATION: none found. Everything checks out.").Refuted {
		t.Error("the critic's prose does not rescue an invalid document")
	}

	beside := OpenCritique(CritiqueOptions{})
	beside.Saw("validate", validAnswer, nil, nil)
	critique := beside.Critique("I looked hard and found nothing.")
	text(t, "the critic's words", critique.ModelText, "I looked hard and found nothing.")
	if strings.Contains(critique.Text, "I looked hard") {
		t.Error("the critic's words are beside the verdict, never in it")
	}
	if !strings.Contains(critique.Text, `"status": "valid"`) {
		t.Error("the verdict quotes the runtime")
	}
}

// refutation.test.ts, "what a person is shown".
func TestWhatAPersonIsShown(t *testing.T) {
	text(t, "the runtime, quoted tool by tool",
		QuoteOf([]Check{{Tool: "validate", Status: "invalid", Diagnostics: 2}, {Tool: "experimental_evaluate", Status: "evaluated"}}),
		`quoted from the runtime: validate → "status": "invalid", 2 diagnostic(s); experimental_evaluate → "status": "evaluated", 0 diagnostic(s)`)
	text(t, "no check at all", QuoteOf(nil), NoChecks)
	if !strings.Contains(NoChecks, "no runtime check") || strings.Contains(NoChecks, "not refuted") {
		t.Errorf("the sentence says the critic ran no check rather than %q: %s", "not refuted", NoChecks)
	}

	// ADR-0001's first rule, at the other end: the proposal is still shown, and without a claim
	// nobody measured.
	nothing := OpenCritique(CritiqueOptions{}).Critique("I had a look.")
	if CritiqueOnProposal(&nothing) != nil {
		t.Error("a proposal the critic did not check carries no refutation line")
	}
	if CritiqueOnProposal(nil) != nil {
		t.Error("a run with no critic carries no refutation line")
	}
	checked := OpenCritique(CritiqueOptions{})
	checked.Saw("validate", invalidAnswer, nil, nil)
	critique := checked.Critique("")
	same(t, "the proposal's critique", CritiqueOnProposal(&critique), json.RawMessage(`{"refuted":true}`))

	event := OpenCritique(CritiqueOptions{})
	event.Saw("validate", invalidAnswer, nil, nil)
	event.Saw("experimental_evaluate", evaluatedAnswer, nil, nil)
	built := CritiqueEvent(event.Critique("my prose"))
	same(t, "the event's verdict and checks", built.Critique.Checks,
		json.RawMessage(`[{"tool":"validate","status":"invalid"},{"tool":"experimental_evaluate","status":"evaluated"}]`))
	if !built.Critique.Refuted || !strings.Contains(built.Critique.Text, "quoted from the runtime") {
		t.Errorf("the event carries the verdict and the runtime's words: %+v", built.Critique)
	}
	if built.Critique.Cases != nil || built.Critique.Incomplete != nil {
		t.Error("with no declared cases the event carries no cases and no incomplete")
	}
}

// refutation.test.ts, "what the critic is told".
func TestWhatTheCriticIsTold(t *testing.T) {
	if !strings.Contains(CriticSystem, RefutationMarker) || !strings.Contains(CriticSentence, RefutationMarker) {
		t.Error("the marker is in both the system text and the first message")
	}
	message := CriticMessage("THE RUNTIME’S GUIDANCE", json.RawMessage(`{"id":"a-pack"}`), nil)
	if strings.Index(message, "THE RUNTIME’S GUIDANCE") != 0 {
		t.Error("the runtime's guidance comes first")
	}
	if !strings.Contains(message, CriticSentence) || !strings.Contains(message, "```json") || !strings.Contains(message, `"id": "a-pack"`) {
		t.Errorf("the desk's one sentence and the document, fenced: %q", message)
	}
	// The document comes after the instructions, fenced, which is the shape the runtime's own
	// prompts use for caller-supplied material.
	if strings.Index(message, "```json") <= strings.Index(message, CriticSentence) {
		t.Error("the document is fenced after the instructions")
	}

	// Never a critic on this desk's sentence alone.
	cannot := CriticCannotRun("")
	if cannot == nil {
		t.Fatal("a pass with no runtime testing prompt does not run")
	}
	if len(cannot.Checks) != 0 || cannot.Refuted || cannot.Text != NoTestPrompt {
		t.Errorf("the pass reports that it did not run: %+v", cannot)
	}
	if !strings.Contains(NoTestPrompt, "no test_pack prompt from the runtime") {
		t.Error("the sentence says which prompt is missing")
	}
	if CritiqueOnProposal(cannot) != nil {
		t.Error("zero checks, so the proposal is shown without a refutation line")
	}
	if CriticCannotRun("   \n ") == nil {
		t.Error("whitespace is not a prompt either")
	}
	if CriticCannotRun("THE RUNTIME’S GUIDANCE") != nil {
		t.Error("a runtime prompt runs the pass")
	}
	// A pass is a bounded read over a document, not a second session.
	if MaxCriticTurns != 4 {
		t.Errorf("MaxCriticTurns is 4, got %d", MaxCriticTurns)
	}
}

// The documents and outcomes of critic.test.ts.
const (
	testDocument = `{"specVersion":"0.2.0-draft","id":"https://example.com/expense","title":"Expense"}`
	testFacts    = `{"claim":{"amount":"120.00","receiptAttached":true}}`
	otherFacts   = `{"claim":{"amount":"120.00","receiptAttached":false}}`
	approve      = `{"handoff":{"state":"none"},"kind":"outcome","outcomeId":"approve","reasons":[]}`
	decline      = `{"handoff":{"state":"none"},"kind":"outcome","outcomeId":"decline","reasons":[]}`
)

func testCases() []CriticCase {
	return []CriticCase{
		{Source: "declared", ID: "receipted-120", Facts: json.RawMessage(testFacts), ExpectedDisposition: json.RawMessage(`{"kind":"outcome","outcomeId":"approve"}`)},
		{Source: "test", ID: "unreceipted-120", Facts: json.RawMessage(otherFacts), ExpectedDisposition: json.RawMessage(`{"kind":"outcome","outcomeId":"decline"}`)},
	}
}

// wholeCases are the same two as saved tests stating the whole §8.3 disposition, which the test
// tool compares (review row 2).
func wholeCases() []CriticCase {
	return []CriticCase{
		{Source: "test", ID: "receipted-120", Facts: json.RawMessage(testFacts), ExpectedDisposition: json.RawMessage(approve)},
		{Source: "test", ID: "unreceipted-120", Facts: json.RawMessage(otherFacts), ExpectedDisposition: json.RawMessage(decline)},
	}
}

// rehearseArgs is rehearse(facts, extra) in critic.test.ts.
func rehearseArgs(facts string, extra ...string) json.RawMessage {
	args := `{"pack":` + quoteJS(testDocument) + `,"facts":` + quoteJS(facts)
	for _, member := range extra {
		args += "," + member
	}
	return json.RawMessage(args + "}")
}

// sawRehearsal is saw(cases, args, disposition) in critic.test.ts: one rehearsal the runtime
// answered evaluated with this disposition.
func sawRehearsal(cases []CriticCase, args json.RawMessage, disposition string) Critique {
	recorder := OpenCritique(CritiqueOptions{Cases: cases, Document: json.RawMessage(testDocument)})
	answer := `{"status":"evaluated","rehearsal":true,"disposition":` + disposition + `}`
	recorder.Saw("experimental_evaluate", answer, args, json.RawMessage(answer))
	return recorder.Critique("")
}

// critic.test.ts, "the critic compares outcomes with the declared ones (agent#10)".
func TestCriticComparesOutcomes(t *testing.T) {
	t.Run("refutes a rehearsal whose outcome the declared case does not have", func(t *testing.T) {
		critique := sawRehearsal(testCases(), rehearseArgs(testFacts), decline)
		same(t, "the checks", critique.Checks, json.RawMessage(`[{"Tool":"experimental_evaluate","Status":"evaluated","Diagnostics":0}]`))
		if !critique.Refuted {
			t.Error("an outcome the declared case does not have refutes")
		}
		same(t, "the comparison", critique.Cases, json.RawMessage(`[{"case":"receipted-120","source":"declared","tool":"experimental_evaluate","agrees":false,`+
			`"expected":{"disposition":{"kind":"outcome","outcomeId":"approve"}},"actual":{"disposition":`+decline+`}}]`))
		same(t, "the cases nothing compared", critique.Incomplete, json.RawMessage(`["unreceipted-120"]`))
		for _, want := range []string{
			`case receipted-120 (declared) disagrees: declared {"kind":"outcome","outcomeId":"approve"}, experimental_evaluate gave {"kind":"outcome","outcomeId":"decline"}`,
			"not compared, so nothing here says the proposal holds: unreceipted-120",
		} {
			if !strings.Contains(critique.Text, want) {
				t.Errorf("the text says %q:\n%s", want, critique.Text)
			}
		}
		// The status rule alone, which is what refuted meant before, says nothing here.
		if VerdictOf(critique.Checks, nil, nil) {
			t.Error("the status rule alone does not refute")
		}
	})

	t.Run("agrees where every member the case states is the outcome's, keys in any order", func(t *testing.T) {
		critique := sawRehearsal(testCases()[:1], rehearseArgs(`{"claim":{"receiptAttached":true,"amount":"120.00"}}`), approve)
		if critique.Refuted {
			t.Error("an outcome the case states agrees")
		}
		if len(critique.Cases) != 1 || !critique.Cases[0].Agrees {
			t.Errorf("one comparison, which agrees: %+v", critique.Cases)
		}
		if !strings.Contains(critique.Text, "1 declared case(s) agree") {
			t.Errorf("the text counts the cases that agree: %s", critique.Text)
		}
	})

	t.Run("compares nothing a rehearsal did not run over the document with the case's inputs", func(t *testing.T) {
		// Another pack, a pack_id, other evidence, other extensions, other facts.
		for _, args := range []json.RawMessage{
			json.RawMessage(`{"pack":` + quoteJS(`{"specVersion":"0.2.0-draft","id":"https://example.com/expense","title":"Another"}`) + `,"facts":` + quoteJS(testFacts) + `}`),
			json.RawMessage(`{"pack_id":"expense","facts":` + quoteJS(testFacts) + `}`),
			rehearseArgs(testFacts, `"evidence":`+quoteJS(`{"receipt":"present"}`)),
			rehearseArgs(testFacts, `"supported_extensions":["x-extension"]`),
			rehearseArgs(`{"claim":{"amount":"12.00"}}`),
		} {
			critique := sawRehearsal(testCases(), args, decline)
			if len(critique.Cases) != 0 {
				t.Errorf("%s compared %d cases", args, len(critique.Cases))
			}
			// Nothing compared: both cases incomplete, so the verdict is not "not refuted".
			same(t, "the cases nothing compared", critique.Incomplete, json.RawMessage(`["receipted-120","unreceipted-120"]`))
			if !critique.Refuted {
				t.Errorf("%s: a declared case nothing compared is unsettled", args)
			}
		}
		// An evidence document that leaves every requirement unknown is the case's none.
		if !sawRehearsal(testCases(), rehearseArgs(testFacts, `"evidence":"{}"`), decline).Refuted {
			t.Error("an empty evidence document is the case's none, so the rehearsal compares")
		}
	})

	t.Run("compares an expected evaluation error by its class and phase", func(t *testing.T) {
		cases := []CriticCase{{Source: "test", ID: "bad-version", Facts: json.RawMessage(testFacts),
			ExpectedErrorClass: ptr("pack-not-conformant"), ExpectedErrorPhase: ptr("preflight")}}
		recorder := OpenCritique(CritiqueOptions{Cases: cases, Document: json.RawMessage(testDocument)})
		recorder.Saw("experimental_evaluate", "the pack is not conformant", rehearseArgs(testFacts),
			json.RawMessage(`{"status":"error","evaluationError":{"class":"pack-not-conformant","phase":"preflight"}}`))
		critique := recorder.Critique("")
		if len(critique.Cases) != 1 || !critique.Cases[0].Agrees {
			t.Fatalf("the expected error class agrees: %+v", critique.Cases)
		}
		same(t, "the outcome", critique.Cases[0].Actual, json.RawMessage(`{"errorClass":"pack-not-conformant","phase":"preflight"}`))
		for _, held := range []struct {
			expected, actual string
			agrees           bool
		}{
			{`{"errorClass":"malformed-input"}`, `{"errorClass":"pack-not-conformant"}`, false},
			{`{"errorClass":"malformed-input"}`, `{"disposition":` + approve + `}`, false},
			{`{"disposition":{"kind":"outcome"}}`, `{"errorClass":"malformed-input"}`, false},
			{`{"errorClass":"malformed-input"}`, `{"errorClass":"malformed-input","phase":"preflight"}`, true},
		} {
			if outcomesAgree(t, held.expected, held.actual) != held.agrees {
				t.Errorf("%s against %s: expected %v", held.expected, held.actual, held.agrees)
			}
		}
	})

	t.Run("reads a test run's rows of the cases sent as declared, and nothing the critic changed", func(t *testing.T) {
		cases := wholeCases()
		matrix := CriticMatrix(cases)
		failed := sawTestRun(cases, matrix, "failed", testDocument)
		if !failed.Refuted {
			t.Error("a row the runtime failed refutes")
		}
		if len(failed.Cases) != 2 || !failed.Cases[0].Agrees || failed.Cases[1].Agrees {
			t.Fatalf("one row agrees and one does not: %+v", failed.Cases)
		}
		same(t, "the failed row", failed.Cases[1], json.RawMessage(`{"case":"unreceipted-120","source":"test","tool":"experimental_test_cases",`+
			`"agrees":false,"expected":{"disposition":`+decline+`},"actual":{"disposition":`+approve+`}}`))
		// A test run is not a status check: only its rows of declared cases count.
		if len(failed.Checks) != 0 {
			t.Errorf("a test run produced %d checks", len(failed.Checks))
		}
		if sawTestRun(cases, matrix, "passed", testDocument).Refuted {
			t.Error("rows the runtime passed do not refute")
		}
		// The critic rewrote the second case's expectation: that row compares nothing.
		rows := value(matrix).get("cases")
		changed := value(matrix).clone()
		second := rows.a[1].clone()
		second.set("expectedDisposition", value(json.RawMessage(approve)))
		changed.set("cases", &jvalue{kind: jarray, a: []*jvalue{rows.a[0], second}})
		critique := sawTestRun(cases, raw(changed), "passed", testDocument)
		if len(critique.Cases) != 1 || critique.Cases[0].Case != "receipted-120" {
			t.Errorf("only the case sent as declared is compared: %+v", critique.Cases)
		}
		same(t, "the case the critic changed", critique.Incomplete, json.RawMessage(`["unreceipted-120"]`))
	})

	t.Run("never compares a case stating part of a disposition by a test run", func(t *testing.T) {
		cases := testCases()
		partial := sawTestRun(cases, CriticMatrix(cases), "passed", testDocument)
		if len(partial.Cases) != 0 {
			t.Errorf("a case stating part of a disposition compares nothing by a test run: %+v", partial.Cases)
		}
		same(t, "both stay incomplete", partial.Incomplete, json.RawMessage(`["receipted-120","unreceipted-120"]`))
		if Testable(cases[0]) {
			t.Error("a case stating kind and outcomeId is not testable")
		}
		if !Testable(wholeCases()[0]) {
			t.Error("a case stating the whole disposition is testable")
		}
		if !Testable(CriticCase{Source: "test", ID: "e", Facts: json.RawMessage(`{}`), ExpectedErrorClass: ptr("malformed-input")}) {
			t.Error("a case expecting an error class is testable")
		}
	})

	t.Run("compares nothing a test run over another pack ran", func(t *testing.T) {
		cases := wholeCases()
		elsewhere := sawTestRun(cases, CriticMatrix(cases), "passed",
			`{"specVersion":"0.2.0-draft","id":"https://example.com/expense","title":"Another"}`)
		if len(elsewhere.Cases) != 0 {
			t.Errorf("another pack's rows compare nothing: %+v", elsewhere.Cases)
		}
		same(t, "both stay incomplete", elsewhere.Incomplete, json.RawMessage(`["receipted-120","unreceipted-120"]`))
		if !elsewhere.Refuted {
			t.Error("a declared case nothing compared is unsettled")
		}
	})

	t.Run("compares the §8.3 sets in any order: reasons and handoff.triggeredBy", func(t *testing.T) {
		saved := `{"kind":"escalation","reasons":[{"code":"b"},{"code":"a"}],"handoff":{"state":"required","triggeredBy":["unknown","conflict"]}}`
		given := `{"kind":"escalation","reasons":[{"code":"a"},{"code":"b"}],"handoff":{"state":"required","triggeredBy":["conflict","unknown"]}}`
		if !outcomesAgree(t, `{"disposition":`+saved+`}`, `{"disposition":`+given+`}`) {
			t.Error("the two sets compare as sets")
		}
		fewer := `{"kind":"escalation","reasons":[{"code":"a"}],"handoff":{"state":"required","triggeredBy":["conflict","unknown"]}}`
		if outcomesAgree(t, `{"disposition":`+saved+`}`, `{"disposition":`+fewer+`}`) {
			t.Error("a set with a member missing does not agree")
		}
		critique := sawRehearsal([]CriticCase{{Source: "test", ID: "reordered", Facts: json.RawMessage(testFacts),
			ExpectedDisposition: json.RawMessage(saved)}}, rehearseArgs(testFacts), given)
		if critique.Refuted || len(critique.Cases) != 1 || !critique.Cases[0].Agrees || len(critique.Incomplete) != 0 {
			t.Errorf("the reordered sets agree: %+v", critique)
		}
	})

	t.Run("hands the critic the whole cases as the matrix and the partial ones apart", func(t *testing.T) {
		cases := append(testCases(), CriticCase{Source: "test", ID: "whole-unreceipted",
			Facts: json.RawMessage(otherFacts), ExpectedDisposition: json.RawMessage(decline)})
		message := CriticMessage("GUIDANCE", json.RawMessage(testDocument), cases)
		if !strings.Contains(message, CriticCasesSentence) || !strings.Contains(message, CriticRehearseSentence) {
			t.Error("both sentences are in the message")
		}
		blocks := fencedJSON(t, message)
		if len(blocks) != 3 {
			t.Fatalf("the document, the matrix and the rehearse-only cases: %d blocks", len(blocks))
		}
		if blocks[1].get("matrixVersion").s != "3" || len(blocks[1].get("cases").a) != 1 ||
			blocks[1].get("cases").a[0].get("id").s != "whole-unreceipted" {
			t.Errorf("the matrix carries the whole cases: %s", blocks[1].stringify())
		}
		var ids []string
		for _, row := range blocks[2].a {
			ids = append(ids, row.get("id").s)
		}
		same(t, "the rehearse-only cases", ids, json.RawMessage(`["receipted-120","unreceipted-120"]`))
		if strings.Contains(message, `"source"`) {
			t.Error("the matrix the critic is shown carries no source")
		}
		if strings.Index(message, `"matrixVersion"`) <= strings.Index(message, `"title": "Expense"`) {
			t.Error("the matrix comes after the document")
		}
		if strings.Contains(CriticMessage("GUIDANCE", json.RawMessage(testDocument), nil), "matrixVersion") {
			t.Error("no declared cases, no matrix")
		}
		if strings.Contains(CriticMessage("GUIDANCE", json.RawMessage(testDocument), testCases()), "matrixVersion") {
			t.Error("cases stating part of a disposition get no matrix")
		}
	})

	t.Run("rehearses a case's evidence and extensions as it declares them", func(t *testing.T) {
		item := CriticCase{Source: "test", ID: "a", Facts: json.RawMessage(testFacts),
			EvidenceAvailability: json.RawMessage(`{"receipt":"present"}`), SupportedExtensions: []string{"x"},
			ExpectedErrorClass: ptr("malformed-input")}
		same(t, "the rehearsal's arguments", RehearsalOf(item, json.RawMessage(testDocument)),
			json.RawMessage(`{"pack":`+quoteJS(testDocument)+`,"facts":`+quoteJS(testFacts)+
				`,"evidence":`+quoteJS(`{"receipt":"present"}`)+`,"supported_extensions":["x"],"rehearsal":true}`))
	})
}

// outcomesAgree reads two outcomes from their JSON and compares them (outcomeAgrees).
func outcomesAgree(t *testing.T, expected, actual string) bool {
	t.Helper()
	var first, second contract.RunEventCaseOutcome
	if err := json.Unmarshal([]byte(expected), &first); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(actual), &second); err != nil {
		t.Fatal(err)
	}
	return OutcomeAgrees(first, second)
}

// sawTestRun is testRun(cases, sent, status, pack) in critic.test.ts: one experimental_test_cases
// answer whose first row passed and whose second carries status.
func sawTestRun(cases []CriticCase, sent json.RawMessage, status, pack string) Critique {
	second := approve
	if status == "passed" {
		second = decline
	}
	report := `{"status":"` + failedUnless(status) + `","packs":[{"packId":"https://example.com/expense","rows":[` +
		`{"id":"receipted-120","status":"passed","expected":` + quoteJS(approve) + `,"actual":` + quoteJS(approve) + `},` +
		`{"id":"unreceipted-120","status":"` + status + `","expected":` + quoteJS(decline) + `,"actual":` + quoteJS(second) + `}]}]}`
	args := `{"pack":` + quoteJS(pack) + `,"matrix":` + quoteJS(string(sent)) + `}`
	recorder := OpenCritique(CritiqueOptions{Cases: cases, Document: json.RawMessage(testDocument)})
	recorder.Saw("experimental_test_cases", report, json.RawMessage(args), json.RawMessage(report))
	return recorder.Critique("")
}

func failedUnless(status string) string {
	if status == "passed" {
		return "passed"
	}
	return "failed"
}

// fencedJSON reads every ```json block of a text as a value, as critic.test.ts reads the critic's
// first message.
func fencedJSON(t *testing.T, text string) []*jvalue {
	t.Helper()
	var blocks []*jvalue
	for _, block := range jsonFences(text) {
		parsed, err := parseJSON([]byte(block.body))
		if err != nil {
			t.Fatalf("a fenced block is not JSON: %v", err)
		}
		blocks = append(blocks, parsed)
	}
	return blocks
}

// critic.test.ts, "refuses a declared case the reader does not read".
func TestReadCriticCasesRefusals(t *testing.T) {
	for _, held := range []struct{ cases, why string }{
		{`[{"source":"declared","id":"a","facts":{}}]`, "exactly one of a disposition and an error class"},
		{`[{"source":"declared","id":"a","facts":{},"expectedDisposition":{"kind":"outcome"},"expectedErrorClass":"x"}]`, "exactly one"},
		{`[{"source":"other","id":"a","facts":{},"expectedErrorClass":"x"}]`, "no source"},
		{`[{"source":"test","id":"a","facts":[],"expectedErrorClass":"x"}]`, "no facts object"},
		{`[{"source":"test","id":"a","facts":{},"expectedErrorClass":"x"},{"source":"test","id":"a","facts":{},"expectedErrorClass":"x"}]`, "repeats an id"},
		{`[{"source":"test","id":"a","facts":{},"expectedErrorClass":"x","path":"/etc"}]`, `carries "path"`},
		{`[{"source":"test","id":"a","facts":{},"expectedDisposition":{"outcomeId":"approve"}}]`, "not an object with a kind"},
		{`[{"source":"test","id":"a","facts":{},"expectedErrorClass":"x","expectedErrorPhase":1}]`, "states an error phase without an error class"},
		{`{"cases":[]}`, "must be an array of at most 200 cases"},
	} {
		_, err := ReadCriticCases(json.RawMessage(held.cases))
		if err == nil {
			t.Errorf("the reader refuses %s", held.cases)
			continue
		}
		if !strings.Contains(err.Error(), held.why) {
			t.Errorf("%s:\n got %q\nwant it to name %q", held.cases, err.Error(), held.why)
		}
	}
	// A case the reader refuses names itself by its id, cut to eighty characters.
	_, err := ReadCriticCases(json.RawMessage(`[{"source":"declared","id":"a","facts":{}}]`))
	if err == nil || !strings.Contains(err.Error(), `The declared case "a"`) {
		t.Errorf("the refusal names the case: %v", err)
	}
	// Two hundred is the most a session carries.
	many := make([]string, 201)
	for at := range many {
		many[at] = `{"source":"test","id":"c` + jsNumber(float64(at)) + `","facts":{},"expectedErrorClass":"x"}`
	}
	if _, err := ReadCriticCases(json.RawMessage("[" + strings.Join(many, ",") + "]")); err == nil {
		t.Error("201 declared cases are refused")
	}
}

// criticDefault.test.ts: the critic's default is the policy table's, and every adapter reads it
// from there — the session's adversarialReview where the host set it, the table's otherwise.
func TestCriticRuns(t *testing.T) {
	for _, policy := range []bool{true, false} {
		if CriticRuns(nil, policy) != policy {
			t.Errorf("a session that leaves it unset runs the critic as the table says (%v)", policy)
		}
		if CriticRuns(ptr(false), policy) {
			t.Error("a session that says false runs no critic, whatever the table says")
		}
		if !CriticRuns(ptr(true), policy) {
			t.Error("a session that asks for the critic gets one, whatever the table says")
		}
	}
}

// CompareTheRest: the harness's own calls for the declared cases the critic left uncompared
// (critic.test.ts, "each declared case the critic's calls left uncompared is rehearsed once" and
// "at most CRITIC_REHEARSALS of them").
func TestCompareTheRest(t *testing.T) {
	cases := testCases()
	recorder := OpenCritique(CritiqueOptions{Cases: cases, Document: json.RawMessage(testDocument)})
	recorder.Saw("validate", validAnswer, nil, nil)
	var made []string
	call := func(_ context.Context, name string, args json.RawMessage) error {
		made = append(made, name+" "+string(args))
		answer := `{"status":"evaluated","rehearsal":true,"disposition":` + decline + `}`
		recorder.Saw(name, answer, args, json.RawMessage(answer))
		return nil
	}
	if err := CompareTheRest(context.Background(), recorder, json.RawMessage(testDocument), CriticTools, call); err != nil {
		t.Fatal(err)
	}
	// Both cases state part of a disposition, so the test tool compares neither: one rehearsal each.
	if len(made) != 2 || !strings.HasPrefix(made[0], "experimental_evaluate ") {
		t.Fatalf("one rehearsal per uncompared case: %v", made)
	}
	critique := recorder.Critique("")
	if len(critique.Incomplete) != 0 {
		t.Errorf("every case was compared: %v", critique.Incomplete)
	}
	if !critique.Refuted {
		t.Error("the runtime gave decline where the first case says approve")
	}

	// At most CriticRehearsals of them, the rest named as not compared.
	many := make([]CriticCase, 0, CriticRehearsals+2)
	for at := 0; at < CriticRehearsals+2; at++ {
		id := "case-" + jsNumber(float64(at))
		many = append(many, CriticCase{Source: "test", ID: id, Facts: json.RawMessage(`{"claim":{"amount":"` + jsNumber(float64(at)) + `"}}`),
			ExpectedDisposition: json.RawMessage(`{"kind":"outcome","outcomeId":"approve"}`)})
	}
	bounded := OpenCritique(CritiqueOptions{Cases: many, Document: json.RawMessage(testDocument)})
	rehearsals := 0
	count := func(_ context.Context, name string, args json.RawMessage) error {
		rehearsals++
		answer := `{"status":"evaluated","rehearsal":true,"disposition":` + decline + `}`
		bounded.Saw(name, answer, args, json.RawMessage(answer))
		return nil
	}
	if err := CompareTheRest(context.Background(), bounded, json.RawMessage(testDocument), CriticTools, count); err != nil {
		t.Fatal(err)
	}
	if rehearsals != CriticRehearsals {
		t.Errorf("at most %d rehearsals, made %d", CriticRehearsals, rehearsals)
	}
	same(t, "the rest named as not compared", bounded.Critique("").Incomplete, json.RawMessage(`["case-8","case-9"]`))
}
