package turn

// The refutation pass: a second, adversarial run over the proposed document, and the three rules
// the desk holds it to (ADR-0001): a non-empty list of checks before "not refuted" is rendered;
// the verdict is never taken from the model's prose, only from the runtime's results; and the
// critic runs inside the same tool gate as the main loop. The primitive is each adapter's; what is
// here is everything a person is shown, in one place, because the rule written twice is the rule
// two readers can disagree about — and this one is whether a document was refuted.
//
// Nothing here writes. The pass runs after the main loop produced a proposal and before the
// proposal event is emitted, and every call it makes is a read.

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
)

// CriticTools is CRITIC_TOOLS: the critic's tools, an allow-list of reads (agent#10), and
// CriticReaders is CRITIC_READERS, the harness's own context readers it may also call (review row
// 6). Both are the host gate's lists (gate.CriticTools, gate.CriticReaders), reused rather than
// written twice; a test holds them equal to contract/vectors/tool-offer/critic-tools.json.
var (
	CriticTools   = gate.CriticTools
	CriticReaders = gate.CriticReaders
)

// CriticMayCall is criticMayCall: whether the critic may call a runtime tool of this name.
func CriticMayCall(name string) bool { return contains(CriticTools, name) }

// CriticMayRead is criticMayRead: whether the critic may call a host tool of this name — the
// harness's own readers, and the skill loaders (ADR-0009; a skill grants no tool).
func CriticMayRead(name string) bool {
	return contains(CriticReaders, name) || contains(gate.SkillTools, name)
}

// CriticRefusalDetail is criticRefusalDetail: why the critic's call of name did not leave the
// page. The gate's sentence (gate.CriticRefusalDetail), which is the same sentence.
func CriticRefusalDetail(name string) string { return gate.CriticRefusalDetail(name) }

// CriticRuns is criticRuns: whether the critic runs, by one rule every adapter reads (agent#10) —
// the session's adversarialReview where the host set it (a non-nil session), and otherwise the
// policy table's, which is false. So the critic runs only where the host asks for it, at any
// reasoning tier and on any adapter.
func CriticRuns(session *bool, policy bool) bool {
	if session != nil {
		return *session
	}
	return policy
}

// RefutationMarker is REFUTATION_MARKER: the literal a critic's request carries, so a scripted
// endpoint — and a reader of a transcript — can tell the two conversations apart.
const RefutationMarker = "REFUTATION PASS"

// CriticSentence is CRITIC_SENTENCE: the one sentence the desk adds, and the whole of it. The
// instructions the critic works from are the runtime's own test_pack prompt, handed over verbatim;
// this says the three things that prompt cannot know.
const CriticSentence = RefutationMarker + ". A pack has just been drafted and is about to be shown to the " +
	"author. Try to refute it: run the runtime’s own checks against the document below and " +
	"report exactly what the runtime said. You never state a verdict of your own, you never " +
	"write a file, and every evaluation you run is a rehearsal."

// CriticSystem is CRITIC_SYSTEM: what the critic is told about itself, above the runtime's prompt.
const CriticSystem = RefutationMarker + ". You are the judgment-pack desk’s critic. You check a proposed " +
	"document against the runtime and quote what the runtime returned. You propose nothing, " +
	"decide nothing, and write nothing."

// MaxCriticTurns is MAX_CRITIC_TURNS: how many turns a critic gets. Four — a check, a rehearsal,
// and a sentence about them, with one spare. The pass is a bounded read over a document that
// already exists, not a second authoring session.
const MaxCriticTurns = 4

// SettledStatus is SETTLED_STATUS: the tools whose answers count as a check, and the runtime's own
// word for "this one did not refuse the document".
//
// Two words and not one. The brief's rule reads status !== 'valid', and over validate alone that
// is right; but a rehearsal experimental_evaluate answers "status": "evaluated", so the same rule
// over both would report every session ever run as refuted. A tool that is not here is not a
// check: get_schema, list_examples and get_example are questions about the runtime rather than
// about the document, and a critic that only asked those has run no check at all.
var SettledStatus = map[string]string{
	"validate":              "valid",
	"experimental_evaluate": "evaluated",
}

// IsCheckTool is isCheckTool: the names this table holds, and only the names it holds. A Go map
// has no prototype, so the defect the TypeScript closes with Object.hasOwn — a critic calling a
// tool named toString producing a check from a table that names no such tool — cannot arise here;
// the function stays, because the rule is the table's and not the language's.
func IsCheckTool(tool string) bool {
	_, held := SettledStatus[tool]
	return held
}

// Check is Check: one runtime answer the critic caused, as the verdict reads it.
type Check struct {
	Tool string
	// Status is the runtime's own status, verbatim.
	Status string
	// Diagnostics is how many diagnostics it carried, for the quote.
	Diagnostics int
}

// NoChecks is NO_CHECKS: the sentence a proposal is shown under when the critic reached no check.
// ADR-0001's first rule: a non-empty list of checks before "not refuted" is rendered. (The
// TypeScript passes it through the Desk's sourceMessage, which carries no values here and is the
// identity on it.)
const NoChecks = "the critic ran no runtime check, so nothing here says whether the document holds"

// NoTestPrompt is NO_TEST_PROMPT: the sentence the pass reports where the runtime advertises no
// test_pack prompt. A runtime with no testing prompt gets no critic rather than a critic working
// from the desk's sentence alone, and the proposal is shown without a refutation line, because a
// pass that did not run refuted nothing.
const NoTestPrompt = "the refutation pass did not run: this desk has no test_pack prompt from the runtime to work from, and the critic works from the runtime’s instructions rather than from words of this desk’s own"

// Critique is Critique: the verdict, and the words on either side of it.
type Critique struct {
	// Refuted is computed from the runtime's own statuses and outcomes. Never from prose.
	Refuted bool
	Checks  []Check
	// Text is the runtime's words, quoted.
	Text string
	// ModelText is the critic's own words, kept beside the verdict and never deciding it.
	ModelText string
	// Cases is each declared case a rehearsal or a test run compared, where the host declared any
	// (agent#10). Nil is the TypeScript's absent member — the host declared no cases; a non-nil
	// empty slice is a host that declared cases none of which was compared.
	Cases []contract.RunEventCaseComparison
	// Incomplete are the declared cases nothing compared, by id (review row 1). Each counts as
	// unsettled, so a critique with any is never "not refuted".
	Incomplete []string
	// Figures is what the critic cost (agent#10).
	Figures *contract.ProvenanceCriticFigures
}

// CriticCase is CriticCase: one case whose outcome the host declares (agent#10) — one of the
// pack's saved tests, or a case the request declares. The members are a row of the runtime's
// matrix (matrixVersion 3), so the critic can run them all with experimental_test_cases as they
// are, plus source, which the matrix does not carry.
//
// Case is the case whole, as the host wrote it, in its own member order: it is what the matrix the
// critic is shown carries, so the text a model reads is the host's bytes. Where it is absent, the
// matrix row is written from the members below, in the schema's order.
type CriticCase struct {
	Source string
	ID     string
	Facts  json.RawMessage
	// EvidenceAvailability, SupportedExtensions, ExpectedDisposition, ExpectedErrorClass and
	// ExpectedErrorPhase are absent where they are nil.
	EvidenceAvailability json.RawMessage
	SupportedExtensions  []string
	ExpectedDisposition  json.RawMessage
	ExpectedErrorClass   *string
	ExpectedErrorPhase   *string
	Case                 json.RawMessage
}

// caseMembers are the members a declared case may carry (CASE_MEMBERS), in the schema's order.
var caseMembers = []string{
	"source", "id", "facts", "evidenceAvailability", "supportedExtensions", "expectedDisposition",
	"expectedErrorClass", "expectedErrorPhase", "expectedHandoffTarget", "origin", "focus", "specSection", "cites",
}

// whole is the case as a JSON value: the bytes the host wrote, or the members above in order.
func (c CriticCase) whole() *jvalue {
	if len(c.Case) > 0 {
		if parsed := value(c.Case); parsed != nil {
			return parsed
		}
	}
	out := jobj()
	out.set("source", jstr(c.Source))
	out.set("id", jstr(c.ID))
	if c.Facts != nil {
		out.set("facts", value(c.Facts))
	}
	if c.EvidenceAvailability != nil {
		out.set("evidenceAvailability", value(c.EvidenceAvailability))
	}
	if c.SupportedExtensions != nil {
		items := &jvalue{kind: jarray, a: []*jvalue{}}
		for _, name := range c.SupportedExtensions {
			items.a = append(items.a, jstr(name))
		}
		out.set("supportedExtensions", items)
	}
	if c.ExpectedDisposition != nil {
		out.set("expectedDisposition", value(c.ExpectedDisposition))
	}
	if c.ExpectedErrorClass != nil {
		out.set("expectedErrorClass", jstr(*c.ExpectedErrorClass))
	}
	if c.ExpectedErrorPhase != nil {
		out.set("expectedErrorPhase", jstr(*c.ExpectedErrorPhase))
	}
	return out
}

// matrixRow is the case as the runtime's matrix carries it: the case without source.
func (c CriticCase) matrixRow() *jvalue {
	row := c.whole().clone()
	kept := jobj()
	for _, member := range row.o {
		if member.key != "source" {
			kept.set(member.key, member.val)
		}
	}
	return kept
}

// ReadCriticCases is readCriticCases: the host's declared cases, read closed, or an error naming
// what is wrong. A row of the runtime's matrix and source, nothing else: a unique id, a facts
// object, and exactly one expectation, a disposition object with a kind or an error class. At most
// 200. The error is a plain one, as the TypeScript's is: the adapter that reads a session's cases
// reports it as a configuration failure and ends the run before any request.
func ReadCriticCases(cases json.RawMessage) ([]CriticCase, error) {
	parsed := value(cases)
	if !parsed.isArray() || len(parsed.a) > 200 {
		return nil, errors.New("The declared cases must be an array of at most 200 cases.")
	}
	seen := map[string]bool{}
	out := make([]CriticCase, 0, len(parsed.a))
	for at, item := range parsed.a {
		name := "case " + jsNumber(float64(at+1))
		if id := item.get("id"); item.isObject() && id != nil && id.kind == jstring {
			name = "case " + quoteJS(cutUTF16(id.s, 80))
		}
		fail := func(why string) error { return errors.New("The declared " + name + " " + why + ".") }
		if !item.isObject() {
			return nil, fail("is not an object")
		}
		for _, member := range item.keys() {
			if !contains(caseMembers, member) {
				return nil, fail("carries " + quoteJS(cutUTF16(member, 64)) + ", which a declared case does not have")
			}
		}
		source := item.get("source")
		if source == nil || source.kind != jstring || (source.s != "declared" && source.s != "test") {
			return nil, fail("has no source: declared or test")
		}
		id := item.get("id")
		if id == nil || id.kind != jstring || id.s == "" || utf16Length(id.s) > 200 {
			return nil, fail("has no id of 1 to 200 characters")
		}
		if seen[id.s] {
			return nil, fail("repeats an id")
		}
		seen[id.s] = true
		facts := item.get("facts")
		if !facts.isObject() {
			return nil, fail("has no facts object")
		}
		evidence := item.get("evidenceAvailability")
		if evidence != nil && !evidence.isObject() {
			return nil, fail("has an evidenceAvailability that is not an object")
		}
		extensions := item.get("supportedExtensions")
		if extensions != nil {
			if !extensions.isArray() {
				return nil, fail("has supportedExtensions that are not strings")
			}
			for _, entry := range extensions.a {
				if entry.kind != jstring {
					return nil, fail("has supportedExtensions that are not strings")
				}
			}
		}
		disposition, errorClass := item.get("expectedDisposition"), item.get("expectedErrorClass")
		if (disposition != nil) == (errorClass != nil) {
			return nil, fail("must expect exactly one of a disposition and an error class")
		}
		if disposition != nil {
			kind := disposition.get("kind")
			if !disposition.isObject() || kind == nil || kind.kind != jstring {
				return nil, fail("expects a disposition that is not an object with a kind")
			}
		}
		if errorClass != nil && (errorClass.kind != jstring || errorClass.s == "") {
			return nil, fail("expects an error class that is not a name")
		}
		phase := item.get("expectedErrorPhase")
		if phase != nil && (errorClass == nil || phase.kind != jstring) {
			return nil, fail("states an error phase without an error class")
		}
		held := CriticCase{Source: source.s, ID: id.s, Facts: raw(facts), Case: raw(item)}
		if evidence != nil {
			held.EvidenceAvailability = raw(evidence)
		}
		if extensions != nil {
			held.SupportedExtensions = []string{}
			for _, entry := range extensions.a {
				held.SupportedExtensions = append(held.SupportedExtensions, entry.s)
			}
		}
		if disposition != nil {
			held.ExpectedDisposition = raw(disposition)
		}
		if errorClass != nil {
			class := errorClass.s
			held.ExpectedErrorClass = &class
		}
		if phase != nil {
			at := phase.s
			held.ExpectedErrorPhase = &at
		}
		out = append(out, held)
	}
	return out, nil
}

// CriticRehearsals is CRITIC_REHEARSALS: how many of the declared cases the critic's own calls
// left uncompared the harness rehearses itself, one call each, after the critic's turns (agent#10).
// The rest stay incomplete. Bounded so that a large saved matrix cannot spend the turn's shared
// tool calls on one pass.
const CriticRehearsals = 8

// RehearsalOf is rehearsalOf: the rehearsal that compares one declared case (agent#10) — the
// proposed document over the case's own facts, evidence and extensions, declared a rehearsal, as
// experimental_evaluate takes them.
func RehearsalOf(item CriticCase, document json.RawMessage) json.RawMessage {
	args := jobj()
	args.set("pack", jstr(value(document).stringify()))
	args.set("facts", jstr(value(item.Facts).stringify()))
	if item.EvidenceAvailability != nil {
		args.set("evidence", jstr(value(item.EvidenceAvailability).stringify()))
	}
	if item.SupportedExtensions != nil {
		items := &jvalue{kind: jarray, a: []*jvalue{}}
		for _, name := range item.SupportedExtensions {
			items.a = append(items.a, jstr(name))
		}
		args.set("supported_extensions", items)
	}
	args.set("rehearsal", jbln(true))
	return raw(args)
}

// CompareTheRest is compareTheRest: the comparison the critic's own calls left open, made by the
// harness (agent#10). After the critic's turns, the declared cases nothing compared are run once
// through call, the same gated dispatch the critic's calls take: one experimental_test_cases call
// over those stating a whole disposition or an error class, where the critic is offered it, then
// one rehearsal each of what is left, at most CriticRehearsals, where it is offered
// experimental_evaluate. A case the runtime gives no outcome for stays incomplete. No model
// request is made.
//
// call makes the call and hands its answer to the recorder, as the adapter's own dispatch does.
func CompareTheRest(ctx context.Context, recorder *Recorder, document json.RawMessage, offered []string,
	call func(context.Context, string, json.RawMessage) error) error {
	// Only the cases the test tool can compare (review row 2); the rest by rehearsal.
	var whole []CriticCase
	for _, item := range recorder.Uncompared() {
		if Testable(item) {
			whole = append(whole, item)
		}
	}
	if len(whole) > 0 && contains(offered, "experimental_test_cases") {
		args := jobj()
		args.set("pack", jstr(value(document).stringify()))
		args.set("matrix", jstr(string(CriticMatrix(whole))))
		if err := call(ctx, "experimental_test_cases", raw(args)); err != nil {
			return err
		}
	}
	if !contains(offered, "experimental_evaluate") {
		return nil
	}
	rest := recorder.Uncompared()
	if len(rest) > CriticRehearsals {
		rest = rest[:CriticRehearsals]
	}
	for _, item := range rest {
		if err := call(ctx, "experimental_evaluate", RehearsalOf(item, document)); err != nil {
			return err
		}
	}
	return nil
}

// Testable is testable: whether a case can go to experimental_test_cases (review row 2) — it
// expects an error class, or states the whole §8.3 disposition (kind, reasons, handoff), which the
// runtime compares byte for byte. A case that states part of a disposition (a request's kind and
// outcomeId) comes back a mismatch there, so it is compared by rehearsal only.
func Testable(item CriticCase) bool {
	if item.ExpectedErrorClass != nil {
		return true
	}
	disposition := value(item.ExpectedDisposition)
	for _, member := range []string{"kind", "reasons", "handoff"} {
		if !disposition.has(member) {
			return false
		}
	}
	return true
}

// CriticMatrix is criticMatrix: the declared cases as the runtime's matrix, which
// experimental_test_cases takes as it is. Each row is the case the host declared, without source.
func CriticMatrix(cases []CriticCase) json.RawMessage {
	rows := &jvalue{kind: jarray, a: []*jvalue{}}
	for _, item := range cases {
		rows.a = append(rows.a, item.matrixRow())
	}
	matrix := jobj()
	matrix.set("matrixVersion", jstr("3"))
	matrix.set("cases", rows)
	return raw(matrix)
}

// criticMatrixRows is criticMatrix(cases).cases: the rows alone, for the rehearse-only block.
func criticMatrixRows(cases []CriticCase) *jvalue {
	rows := &jvalue{kind: jarray, a: []*jvalue{}}
	for _, item := range cases {
		rows.a = append(rows.a, item.matrixRow())
	}
	return rows
}

// payloadOf is payloadOf: the runtime's answer as its JSON payload — the structured content, or
// the text read as JSON.
func payloadOf(text string, structured json.RawMessage) *jvalue {
	if held := value(structured); held.isObject() {
		return held
	}
	if held := value(json.RawMessage(text)); held.isObject() {
		return held
	}
	return nil
}

// expectationOf is expectationOf: the expectation a declared case states, as a comparison reports
// it.
func expectationOf(item CriticCase) contract.RunEventCaseOutcome {
	if item.ExpectedErrorClass != nil {
		option := &contract.RunEventCaseOutcomeOption2{ErrorClass: *item.ExpectedErrorClass}
		if item.ExpectedErrorPhase != nil {
			phase := *item.ExpectedErrorPhase
			option.Phase = &phase
		}
		return contract.RunEventCaseOutcome{Option2: option}
	}
	return contract.RunEventCaseOutcome{Option1: &contract.RunEventCaseOutcomeOption1{Disposition: toMap(value(item.ExpectedDisposition))}}
}

// toMap is an object's members as the contract's open map carries them.
func toMap(v *jvalue) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	if !v.isObject() {
		return out
	}
	for _, member := range v.o {
		out[member.key] = raw(member.val)
	}
	return out
}

// mapValue is a contract map as a JSON value. Its member order is the map's, so only canonical
// comparisons and member sets are read from it, never its bytes.
func mapValue(m map[string]json.RawMessage) *jvalue {
	out := jobj()
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.SliceStable(names, func(i, j int) bool { return lessJS(names[i], names[j]) })
	for _, name := range names {
		out.set(name, value(m[name]))
	}
	return out
}

// OutcomeAgrees is outcomeAgrees: whether an outcome holds what a case expects — every member the
// expected disposition states equal to the outcome's, or the expected error class (and phase,
// where stated). A disposition where an error was expected, or the reverse, never agrees.
func OutcomeAgrees(expected, actual contract.RunEventCaseOutcome) bool {
	if expected.Option1 != nil {
		if actual.Option1 == nil {
			return false
		}
		wanted := asSets(mapValue(expected.Option1.Disposition))
		got := asSets(mapValue(actual.Option1.Disposition))
		for _, member := range wanted.o {
			if canonical(member.val) != canonical(got.get(member.key)) {
				return false
			}
		}
		return true
	}
	if expected.Option2 == nil {
		return false
	}
	if actual.Option2 == nil || actual.Option2.ErrorClass != expected.Option2.ErrorClass {
		return false
	}
	if expected.Option2.Phase == nil {
		return true
	}
	return actual.Option2.Phase != nil && *actual.Option2.Phase == *expected.Option2.Phase
}

// asSets is asSets: a disposition with its two §8.3 sets in one order (review row 3). reasons and
// handoff.triggeredBy are sets, which the runtime serializes sorted and compares as sets, so a
// case may list them in any order.
func asSets(disposition *jvalue) *jvalue {
	out := disposition.clone()
	if out.has("reasons") {
		out.set("reasons", sortedValue(out.get("reasons")))
	}
	if handoff := out.get("handoff"); handoff.isObject() && handoff.has("triggeredBy") {
		held := handoff.clone()
		held.set("triggeredBy", sortedValue(held.get("triggeredBy")))
		out.set("handoff", held)
	}
	return out
}

// sortedValue sorts an array by its items' canonical text, and leaves anything else alone.
func sortedValue(v *jvalue) *jvalue {
	if !v.isArray() {
		return v
	}
	items := append([]*jvalue(nil), v.a...)
	sort.SliceStable(items, func(i, j int) bool { return lessJS(canonical(items[i]), canonical(items[j])) })
	return &jvalue{kind: jarray, a: items}
}

// sameInputs is sameInputs: the evidence and extensions a rehearsal ran under, normalized as the
// runtime reads them.
func sameInputs(item CriticCase, args *jvalue) bool {
	facts := textValue(args.get("facts"))
	if !facts.isObject() || canonical(facts) != canonical(value(item.Facts)) {
		return false
	}
	// An omitted evidence document and an empty one both leave every requirement unknown.
	evidence := jobj()
	if held := args.get("evidence"); held != nil {
		evidence = textValue(held)
	}
	declared := jobj()
	if item.EvidenceAvailability != nil {
		declared = value(item.EvidenceAvailability)
	}
	if !evidence.isObject() || canonical(evidence) != canonical(declared) {
		return false
	}
	return extensionsText(args.get("supported_extensions")) == extensionsText(stringsValue(item.SupportedExtensions))
}

// extensionsText is `canonical(Array.isArray(value) ? [...value].map(String).sort() : [])`.
func extensionsText(v *jvalue) string {
	items := &jvalue{kind: jarray, a: []*jvalue{}}
	if v.isArray() {
		for _, entry := range v.a {
			items.a = append(items.a, jstr(jsString(entry)))
		}
		sort.SliceStable(items.a, func(i, j int) bool { return lessJS(items.a[i].s, items.a[j].s) })
	}
	return canonical(items)
}

// stringsValue is a list of strings as a JSON value; nil stays absent.
func stringsValue(list []string) *jvalue {
	if list == nil {
		return nil
	}
	items := &jvalue{kind: jarray, a: []*jvalue{}}
	for _, name := range list {
		items.a = append(items.a, jstr(name))
	}
	return items
}

// isTheDocument is isTheDocument: the pack a call evaluated is the proposed document, read as
// JSON. Never a pack_id.
func isTheDocument(pack *jvalue, document json.RawMessage) bool {
	held := textValue(pack)
	return held != nil && canonical(held) == canonical(value(document))
}

// compareRehearsal is compareRehearsal: the comparisons one rehearsal makes — each declared case
// whose facts, evidence and extensions it ran, over the proposed document, against the outcome the
// runtime gave.
func compareRehearsal(cases []CriticCase, document, args json.RawMessage, payload *jvalue) []contract.RunEventCaseComparison {
	parsed := value(args)
	if !isTheDocument(parsed.get("pack"), document) {
		return nil
	}
	var actual *contract.RunEventCaseOutcome
	status, disposition := payload.get("status"), payload.get("disposition")
	evaluationError := payload.get("evaluationError")
	switch {
	case status != nil && status.kind == jstring && status.s == "evaluated" && disposition.isObject():
		actual = &contract.RunEventCaseOutcome{Option1: &contract.RunEventCaseOutcomeOption1{Disposition: toMap(disposition)}}
	case evaluationError.isObject() && stringMember(evaluationError, "class") != nil:
		option := &contract.RunEventCaseOutcomeOption2{ErrorClass: *stringMember(evaluationError, "class")}
		option.Phase = stringMember(evaluationError, "phase")
		actual = &contract.RunEventCaseOutcome{Option2: option}
	default:
		return nil
	}
	var found []contract.RunEventCaseComparison
	for _, item := range cases {
		if !sameInputs(item, parsed) {
			continue
		}
		expected := expectationOf(item)
		found = append(found, contract.RunEventCaseComparison{Case: item.ID, Source: item.Source,
			Tool: "experimental_evaluate", Agrees: OutcomeAgrees(expected, *actual), Expected: expected, Actual: actual})
	}
	return found
}

// stringMember is a string member of an object, or nil where it is absent or not a string.
func stringMember(v *jvalue, key string) *string {
	held := v.get(key)
	if held == nil || held.kind != jstring {
		return nil
	}
	text := held.s
	return &text
}

// compareTestRun is compareTestRun: the comparisons one test run makes — each row of a declared
// case, where the matrix the critic sent carried that case exactly as the host declared it and the
// pack was the proposed document. The runtime compared the row; its status is the verdict and its
// actual outcome is reported. A row of a case the critic changed or wrote itself compares nothing.
func compareTestRun(cases []CriticCase, document, args json.RawMessage, payload *jvalue) []contract.RunEventCaseComparison {
	parsed := value(args)
	if !isTheDocument(parsed.get("pack"), document) {
		return nil
	}
	matrix := textValue(parsed.get("matrix"))
	var sent []*jvalue
	if matrix.isObject() && matrix.get("cases").isArray() {
		for _, row := range matrix.get("cases").a {
			if row.isObject() {
				sent = append(sent, row)
			}
		}
	}
	var rows []*jvalue
	if payload.get("packs").isArray() {
		for _, pack := range payload.get("packs").a {
			if !pack.isObject() || !pack.get("rows").isArray() {
				continue
			}
			for _, row := range pack.get("rows").a {
				if row.isObject() {
					rows = append(rows, row)
				}
			}
		}
	}
	var found []contract.RunEventCaseComparison
	for _, row := range rows {
		// A case stating part of a disposition is never compared by a test run (review row 2).
		var item *CriticCase
		for at := range cases {
			if id := row.get("id"); id != nil && id.kind == jstring && id.s == cases[at].ID && Testable(cases[at]) {
				item = &cases[at]
				break
			}
		}
		if item == nil {
			continue
		}
		declared := canonical(item.matrixRow())
		asDeclared := false
		for _, candidate := range sent {
			if id := candidate.get("id"); id != nil && id.kind == jstring && id.s == item.ID && canonical(candidate) == declared {
				asDeclared = true
				break
			}
		}
		if !asDeclared {
			continue
		}
		var actual *contract.RunEventCaseOutcome
		if class := stringMember(row, "actualErrorClass"); class != nil && *class != "" {
			option := &contract.RunEventCaseOutcomeOption2{ErrorClass: *class}
			if phase := stringMember(row, "actualErrorPhase"); phase != nil && *phase != "" {
				option.Phase = phase
			}
			actual = &contract.RunEventCaseOutcome{Option2: option}
		} else if disposition := textValue(row.get("actual")); disposition.isObject() {
			actual = &contract.RunEventCaseOutcome{Option1: &contract.RunEventCaseOutcomeOption1{Disposition: toMap(disposition)}}
		}
		status := row.get("status")
		found = append(found, contract.RunEventCaseComparison{Case: item.ID, Source: item.Source, Tool: "experimental_test_cases",
			Agrees: status != nil && status.kind == jstring && status.s == "passed", Expected: expectationOf(*item), Actual: actual})
	}
	return found
}

// outcomeText is outcomeText: an outcome as a person reads it — the members the case states, or
// the error class.
func outcomeText(outcome *contract.RunEventCaseOutcome, shape *contract.RunEventCaseOutcome) string {
	if outcome == nil {
		return "no outcome"
	}
	if outcome.Option2 != nil {
		text := "error class " + outcome.Option2.ErrorClass
		if outcome.Option2.Phase != nil {
			text += " (" + *outcome.Option2.Phase + ")"
		}
		return text
	}
	if outcome.Option1 == nil {
		return "no outcome"
	}
	members := outcome.Option1.Disposition
	if shape != nil && shape.Option1 != nil {
		members = shape.Option1.Disposition
	}
	shown := jobj()
	for name := range members {
		shown.set(name, value(outcome.Option1.Disposition[name]))
	}
	return canonical(shown)
}

// ComparisonText is comparisonText: the comparisons, as a person is shown them.
func ComparisonText(cases []contract.RunEventCaseComparison, incomplete []string) string {
	var parts []string
	agreed := map[string]bool{}
	for _, item := range cases {
		if item.Agrees {
			agreed[item.Case] = true
		}
	}
	for _, item := range cases {
		if item.Agrees {
			continue
		}
		expected := item.Expected
		parts = append(parts, "case "+item.Case+" ("+item.Source+") disagrees: declared "+outcomeText(&expected, nil)+
			", "+item.Tool+" gave "+outcomeText(item.Actual, &expected))
	}
	if len(agreed) > 0 {
		parts = append(parts, jsNumber(float64(len(agreed)))+" declared case(s) agree")
	}
	if len(incomplete) > 0 {
		parts = append(parts, "incomplete, "+jsNumber(float64(len(incomplete)))+
			" declared case(s) not compared, so nothing here says the proposal holds: "+strings.Join(incomplete, ", "))
	}
	return "compared with the declared outcomes: " + strings.Join(parts, "; ")
}

// StatusOf is statusOf: the runtime's status and diagnostic count out of one tool answer. Held is
// false where the answer carries no status at all.
func StatusOf(text string) (status string, diagnostics int, held bool) {
	payload := value(json.RawMessage(text))
	if payload == nil {
		return "", 0, false
	}
	said := payload.get("status")
	if said == nil || said.kind != jstring || said.s == "" {
		return "", 0, false
	}
	count := 0
	if list := payload.get("diagnostics"); list.isArray() {
		count = len(list.a)
	}
	return said.s, count, true
}

// VerdictOf is verdictOf: the verdict, from the runtime's results and from nothing else.
//
// ADR-0001's second rule. Refuted is true exactly where the critic caused at least one check and
// at least one of them came back as something other than the runtime's own word for "this went
// through"; since agent#10, a rehearsal that says "evaluated" with an outcome other than the one a
// declared case states refutes too, so a semantic error is visible, and a declared case nothing
// compared is unsettled (review row 1), so a critique that did not reach every declared case is
// never "not refuted".
func VerdictOf(checks []Check, cases []contract.RunEventCaseComparison, incomplete []string) bool {
	if len(checks) == 0 && len(cases) == 0 && len(incomplete) == 0 {
		return false
	}
	for _, check := range checks {
		if check.Status != SettledStatus[check.Tool] {
			return true
		}
	}
	for _, item := range cases {
		if !item.Agrees {
			return true
		}
	}
	return len(incomplete) > 0
}

// QuoteOf is quoteOf: the runtime's answers, quoted — the text a person is shown as the verdict.
func QuoteOf(checks []Check) string {
	if len(checks) == 0 {
		return NoChecks
	}
	parts := make([]string, 0, len(checks))
	for _, check := range checks {
		parts = append(parts, check.Tool+" → \"status\": \""+check.Status+"\", "+jsNumber(float64(check.Diagnostics))+" diagnostic(s)")
	}
	return "quoted from the runtime: " + strings.Join(parts, "; ")
}

// CritiqueOptions is CritiqueOptions: what the comparison needs (agent#10) — the cases the host
// declares and the document the critic is checking. With no cases there is nothing to compare, and
// the critique is the checks alone.
type CritiqueOptions struct {
	Cases    []CriticCase
	Document json.RawMessage
}

// Recorder is CritiqueRecorder: what the desk saw the critic cause, collected as it happens. The
// adapter hands it every tool answer that came back through the gate; this decides which of them
// is a check. An adapter cannot add a check by saying so, and cannot suppress one by not
// mentioning it — it reports every answer it received, and the filtering is here.
type Recorder struct {
	checks   []Check
	declared []CriticCase
	document json.RawMessage
	compared []contract.RunEventCaseComparison
}

// OpenCritique is openCritique.
func OpenCritique(options CritiqueOptions) *Recorder {
	return &Recorder{checks: []Check{}, declared: options.Cases, document: options.Document,
		compared: []contract.RunEventCaseComparison{}}
}

// Saw is saw: one answer the runtime gave the critic.
//
// The contract is in the name: an adapter calls this only where its own gated call returned —
// where the frame passed the tool gate, reached the runtime, and came back. A call the gate
// refused never gets here, and neither does one a cancelled run abandoned. args are the arguments
// the call left with and structured the answer's structured content, which the outcome comparison
// reads (agent#10); nil args is the TypeScript's absent argument, which compares nothing.
//
// A check is a thing the runtime said, and nothing else: only names SettledStatus holds are read,
// and a status comes only out of the runtime's own JSON. An answer with no status is not a check —
// including an in-band isError from the runtime, which is the runtime declining to answer rather
// than declining the document.
func (r *Recorder) Saw(tool, text string, args, structured json.RawMessage) {
	// agent#10: the outcomes, compared with the declared cases, from the runtime's own answer and
	// the arguments the call left with.
	if len(r.declared) > 0 && args != nil {
		if payload := payloadOf(text, structured); payload != nil {
			if tool == "experimental_evaluate" {
				r.compared = append(r.compared, compareRehearsal(r.declared, r.document, args, payload)...)
			}
			if tool == "experimental_test_cases" {
				r.compared = append(r.compared, compareTestRun(r.declared, r.document, args, payload)...)
			}
		}
	}
	if !IsCheckTool(tool) {
		return
	}
	status, diagnostics, held := StatusOf(text)
	if !held {
		return
	}
	r.checks = append(r.checks, Check{Tool: tool, Status: status, Diagnostics: diagnostics})
}

// Uncompared is uncompared: the declared cases no answer has compared yet (agent#10).
func (r *Recorder) Uncompared() []CriticCase {
	reached := map[string]bool{}
	for _, item := range r.compared {
		reached[item.Case] = true
	}
	var out []CriticCase
	for _, item := range r.declared {
		if !reached[item.ID] {
			out = append(out, item)
		}
	}
	return out
}

// Critique is critique: the critique, once the critic has stopped calling tools.
func (r *Recorder) Critique(modelText string) Critique {
	checks := append([]Check{}, r.checks...)
	if len(r.declared) == 0 {
		return Critique{Refuted: VerdictOf(checks, nil, nil), Checks: checks, Text: QuoteOf(checks), ModelText: modelText}
	}
	reached := map[string]bool{}
	for _, item := range r.compared {
		reached[item.Case] = true
	}
	incomplete := []string{}
	for _, item := range r.declared {
		if !reached[item.ID] {
			incomplete = append(incomplete, item.ID)
		}
	}
	compared := append([]contract.RunEventCaseComparison{}, r.compared...)
	var quoted string
	if len(checks) > 0 || len(compared) > 0 {
		var parts []string
		if len(checks) > 0 {
			parts = append(parts, QuoteOf(checks))
		}
		parts = append(parts, ComparisonText(compared, incomplete))
		quoted = strings.Join(parts, "; ")
	} else {
		quoted = NoChecks + "; " + ComparisonText(compared, incomplete)
	}
	return Critique{Refuted: VerdictOf(checks, compared, incomplete), Checks: checks, Text: quoted,
		ModelText: modelText, Cases: compared, Incomplete: incomplete}
}

// CriticCannotRun is criticCannotRun: the critique a pass that could not run produces, or nil
// where it can. A run that reached the pass without the runtime's testing prompt reports that,
// rather than putting a critic in front of a document with only the desk's sentence to go on. Zero
// checks, so the proposal is shown without a refutation line.
func CriticCannotRun(testPrompt string) *Critique {
	if strings.Trim(testPrompt, jsSpace) != "" {
		return nil
	}
	return &Critique{Refuted: false, Checks: []Check{}, Text: NoTestPrompt, ModelText: ""}
}

// CriticCasesSentence is CRITIC_CASES_SENTENCE: the second sentence the desk adds, where the host
// declares cases (agent#10) — what they are, how to run them, and that the comparison is the
// host's. It names the two tools and their arguments and no verdict.
const CriticCasesSentence = "The matrix below declares the outcome each case must have, its whole disposition or its error class: the pack’s tests and the request’s own cases. Run it against the document above with experimental_test_cases (pack: the document as JSON text; matrix: this matrix as it is), or rehearse each case with experimental_evaluate (pack: the document as JSON text; facts and evidence: the case’s facts and evidenceAvailability as JSON text). The host compares each outcome with the declared one."

// CriticRehearseSentence is CRITIC_REHEARSE_SENTENCE: the third, for the cases that state part of
// a disposition (review row 2) — rehearsal only, since the test tool compares a whole disposition.
const CriticRehearseSentence = "The cases below state part of the outcome they must have. Rehearse each one against the document above with experimental_evaluate (pack: the document as JSON text; facts and evidence: the case’s facts and evidenceAvailability as JSON text); experimental_test_cases does not take them. The host compares each outcome with the declared one."

// CriticMessage is criticMessage: the critic's first message — the runtime's testing prompt, the
// desk's one sentence, and the document.
//
// The document is fenced, which is the shape the runtime's own prompts use for caller-supplied
// material, so the critic can tell what it was handed from the instructions around it. testPrompt
// is the runtime's test_pack prompt as prompts/get served it, and it is never empty here: a pass
// with no runtime prompt does not run at all (CriticCannotRun). It is read without its pack
// argument: the document does not exist when a session's prompts are read, and a prompt fetched
// mid-run for each proposal would be a second prompts/get inside a pass whose whole point is that
// it costs one call. The document is handed over below instead, verbatim.
func CriticMessage(testPrompt string, document json.RawMessage, cases []CriticCase) string {
	block := func(v *jvalue) string { return "```json\n" + v.indent(2) + "\n```" }
	fenced := block(value(document))
	guidance := ""
	if strings.Trim(testPrompt, jsSpace) != "" {
		guidance = testPrompt + "\n\n"
	}
	// agent#10: the cases whose outcomes the host declares. Those the test tool can compare as the
	// runtime's matrix, to run as it is; the cases stating part of a disposition apart, for
	// rehearsal only (review row 2).
	var whole, partial []CriticCase
	for _, item := range cases {
		if Testable(item) {
			whole = append(whole, item)
		} else {
			partial = append(partial, item)
		}
	}
	matrix := ""
	if len(whole) > 0 {
		matrix = "\n\n" + CriticCasesSentence + "\n\n" + block(value(CriticMatrix(whole)))
	}
	rehearse := ""
	if len(partial) > 0 {
		rehearse = "\n\n" + CriticRehearseSentence + "\n\n" + block(criticMatrixRows(partial))
	}
	return guidance + CriticSentence + "\n\n" + fenced + matrix + rehearse
}

// CritiqueEvent is critiqueEvent: the contract's own event, built in one place from the critique.
func CritiqueEvent(critique Critique) contract.RunEvent {
	checks := make([]contract.RunEventCritiqueChecksItem, 0, len(critique.Checks))
	for _, check := range critique.Checks {
		checks = append(checks, contract.RunEventCritiqueChecksItem{Tool: check.Tool, Status: check.Status})
	}
	event := &contract.RunEventCritique{Type: "critique", Refuted: critique.Refuted, Checks: checks, Text: critique.Text}
	if critique.Cases != nil {
		event.Cases = critique.Cases
		event.Incomplete = critique.Incomplete
		if event.Incomplete == nil {
			event.Incomplete = []string{}
		}
	}
	event.Figures = critique.Figures
	return contract.RunEvent{Critique: event}
}

// ProposalNote is proposalNote: what the proposal carries where something after it did not finish
// — the critic (its provider error, length cutoff, the allowance or the turn's wall clock), or,
// for a proposal made through propose_pack, a later request of the turn. The proposal is the
// finished work and is delivered; the failure is a note on it, never an error raised ahead of it.
func ProposalNote(kind string, cause error, source string) contract.RunEventProposalNote {
	held := failure.Normalize(cause, source, time.Now())
	// Nothing pauses here: the run ends with the proposal, so the allowance's own sentence
	// ("Work paused …") would say what did not happen.
	why := failure.Message(held)
	if held.Code == "request-limit" {
		why = "The request allowance was used up before it finished."
	}
	what := "The turn stopped after this proposal was made, so nothing after it ran: no closing summary and no critic check."
	if kind == "critic-unfinished" {
		what = "The critic did not finish, so it checked nothing on this proposal."
	}
	return contract.RunEventProposalNote{Kind: kind, Message: what + " " + why, Failure: held}
}

// CriticNote is criticNote: the critic's note, as ProposalNote writes it, carrying what the critic
// cost before it stopped (agent#10).
func CriticNote(cause error, source string, figures *contract.ProvenanceCriticFigures) contract.RunEventProposalNote {
	note := ProposalNote("critic-unfinished", cause, source)
	note.Figures = figures
	return note
}

// CriticStart is the execution's state when the critic started (before in modelCriticFigures): the
// model requests used, and how many completion rows the execution had.
type CriticStart struct {
	Used int
	Rows int
}

// CriticEnd is the execution's state when the critic stopped (after in modelCriticFigures).
type CriticEnd struct {
	Used        int
	Completions []contract.Provenance
}

// ModelCriticFigures is modelCriticFigures: what the critic cost on a model adapter (agent#10) —
// the requests it claimed against the allowance, and the token counts its completion rows
// reported, summed.
func ModelCriticFigures(model string, before CriticStart, after CriticEnd) contract.ProvenanceCriticFigures {
	var rows []contract.ProvenanceCompletion
	completions := after.Completions
	if before.Rows < len(completions) {
		completions = completions[before.Rows:]
	} else {
		completions = nil
	}
	for _, row := range completions {
		if row.Phase == "review" {
			rows = append(rows, row.Result)
		}
	}
	requests := after.Used - before.Used
	if requests < 0 {
		requests = 0
	}
	figures := contract.ProvenanceCriticFigures{Model: model, Unit: "model-requests", Requests: int64(requests)}
	for _, member := range []struct {
		read  func(contract.ProvenanceCompletion) *int64
		write func(*contract.ProvenanceCriticFigures, *int64)
	}{
		{func(c contract.ProvenanceCompletion) *int64 { return c.InputTokens }, func(f *contract.ProvenanceCriticFigures, v *int64) { f.InputTokens = v }},
		{func(c contract.ProvenanceCompletion) *int64 { return c.OutputTokens }, func(f *contract.ProvenanceCriticFigures, v *int64) { f.OutputTokens = v }},
		{func(c contract.ProvenanceCompletion) *int64 { return c.ReasoningTokens }, func(f *contract.ProvenanceCriticFigures, v *int64) { f.ReasoningTokens = v }},
		{func(c contract.ProvenanceCompletion) *int64 { return c.CacheReadTokens }, func(f *contract.ProvenanceCriticFigures, v *int64) { f.CacheReadTokens = v }},
		{func(c contract.ProvenanceCompletion) *int64 { return c.CacheWriteTokens }, func(f *contract.ProvenanceCriticFigures, v *int64) { f.CacheWriteTokens = v }},
	} {
		counted, sum := 0, int64(0)
		for _, row := range rows {
			if held := member.read(row); held != nil {
				counted++
				sum += *held
			}
		}
		if counted > 0 {
			total := sum
			member.write(&figures, &total)
		}
	}
	return figures
}

// CritiqueOnProposal is critiqueOnProposal: what the proposal event carries about the pass, or
// nothing. ADR-0001's first rule again, at the other end: with no checks there is no refutation
// line on the proposal at all. The proposal is still shown — the critic reaching nothing is not a
// reason to withhold a document — and the stream says what happened. A compared case is evidence
// as a check is (agent#10), and a declared case nothing compared is named here, never left to a
// plain not-refuted (review row 1).
func CritiqueOnProposal(critique *Critique) *contract.RunEventProposalCritique {
	if critique == nil {
		return nil
	}
	if len(critique.Checks) == 0 && len(critique.Cases) == 0 && len(critique.Incomplete) == 0 {
		return nil
	}
	out := &contract.RunEventProposalCritique{Refuted: critique.Refuted}
	if len(critique.Incomplete) > 0 {
		out.Incomplete = critique.Incomplete
	}
	return out
}

func contains(list []string, name string) bool {
	for _, held := range list {
		if held == name {
			return true
		}
	}
	return false
}
