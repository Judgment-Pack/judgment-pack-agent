package turn

// The contract's own vocabulary, held where every adapter reads it: the ported cases of
// engines/contract.test.ts. Not ported from that file are its cases for the run gate, the one
// await on the world outside an engine and the iterator every engine is wrapped in: they are
// JavaScript's async plumbing, which the Go adapter does with a context.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
)

// engines/contract.test.ts: "does not misclassify an ordinary code example as a pack proposal".
func TestOrdinaryExampleIsNotAProposal(t *testing.T) {
	if HasProposalFence("```json\n{\"x\":1}\n```") {
		t.Error("an ordinary JSON example is not a proposal")
	}
	if !HasProposalFence("```json\n{\"proposal\":{\"document\":{}}}\n```") {
		t.Error("an explicit envelope is a proposal")
	}
	text(t, "a partial fence marker", StreamingProse("Answer.\n\n``"), "Answer.")
	text(t, "a proposal that has begun", StreamingProse("Answer.\n\n```json\n{\"proposal\":"), "Answer.")
}

// engines/contract.test.ts: "finds one explicit proposal after indented examples without
// consuming its fence".
func TestOneExplicitProposalAfterExamples(t *testing.T) {
	examples := "Runtime report:\n```text\nstatus: valid\n```\n1. Probe\n     ```json\n     {\"status\":\"evaluated\"}\n     ```\n"
	proposal := "```json\n{\"proposal\":{\"kind\":\"create\",\"document\":{\"id\":\"p\"},\"unknowns\":[]}}\n```"
	if !HasProposalFence(examples + proposal) {
		t.Error("the proposal is found after the examples")
	}
	read, err := ExtractProposal(examples + proposal)
	if err != nil {
		t.Fatalf("the proposal reads: %v", err)
	}
	same(t, "the document", read.Document, json.RawMessage(`{"id":"p"}`))
	same(t, "the unknowns", read.Unknowns, json.RawMessage(`[]`))
	text(t, "the prose", ProseOf(examples+proposal), strings.Trim(examples, jsSpace))
	if _, err := ExtractProposal(examples + proposal + "\n" + proposal); err == nil || !strings.Contains(err.Error(), "carried 2") {
		t.Errorf("two envelopes are refused: %v", err)
	}
	if HasProposalFence("```json\n{\"example\":{\"proposal\":{}}}\n```") {
		t.Error("a nested example is not a proposal")
	}
	if HasProposalFence("```text\n{\"proposal\":{\"document\":{}}}\n```") {
		t.Error("a fence that is not marked json is not a proposal")
	}
	if HasProposalFence("```json\n{\"proposal\":{\"document\":{}}}") {
		t.Error("an unclosed fence is not a proposal")
	}
	if _, err := ExtractProposal("```json\n{\"proposal\":{\"document\":null}}\n```"); err == nil || !strings.Contains(err.Error(), "document object") {
		t.Errorf("a proposal with no document object is refused: %v", err)
	}
}

// engines/contract.test.ts: the reasons the model is given where no closed json proposal was
// found, and the kind it may not propose.
func TestWhyNoProposalWasFound(t *testing.T) {
	for _, held := range []struct{ answer, why string }{
		{"```json\n{\"proposal\":{\"document\":{}}}", "its proposal block is not closed"},
		{"```text\n{\"proposal\": {\"document\":{}}}\n```", "its proposal is in a fence that is not marked json"},
		{"Nothing fenced at all.", "exactly one fenced JSON block holding the proposal; this one carried 0"},
		{"```json\n{\"proposal\":{\"kind\":\"create\",\"document\":{\"id\":\"p\",},\"unknowns\":[]}}\n```", "the fenced proposal block is not valid JSON"},
		{"```json\n{\"proposal\":{\"kind\":\"replace\",\"document\":{}}}\n```", "the proposal kind is not supported"},
	} {
		_, err := ExtractProposal(held.answer)
		if err == nil || !strings.Contains(err.Error(), held.why) {
			t.Errorf("%q:\n got %v\nwant it to name %q", held.answer, err, held.why)
		}
		if failureOf(t, err).Code != "invalid-response" {
			t.Errorf("%q: the class is invalid-response", held.answer)
		}
	}
	// The unknowns are every item as a string, and nothing at all where they are not a list.
	read, err := ExtractProposal("```json\n{\"proposal\":{\"document\":{\"id\":\"p\"},\"unknowns\":[\"who signs\",2,null]}}\n```")
	if err != nil {
		t.Fatal(err)
	}
	same(t, "the unknowns", read.Unknowns, json.RawMessage(`["who signs","2","null"]`))
	read, err = ExtractProposal("```json\n{\"proposal\":{\"document\":{\"id\":\"p\"},\"unknowns\":\"one\"}}\n```")
	if err != nil {
		t.Fatal(err)
	}
	same(t, "unknowns that are not a list", read.Unknowns, json.RawMessage(`[]`))
}

// engines/contract.test.ts, "streamed Markdown projection": "streams unfinished ordinary code and
// subsequent formatted sections before the answer ends".
func TestStreamsUnfinishedCodeAndSections(t *testing.T) {
	code := "Example:\n\n```typescript\nconst count = 1;"
	text(t, "unfinished ordinary code", StreamingProse(code), code)
	text(t, "a partial closing fence", StreamingProse(code+"\n``"), code)
	body := code + "\n```\n\n## Results\n\n- First\n- Second\n\n> A quote\n\n| Item | Value |\n| --- | --- |\n| Count | 1 |"
	text(t, "the formatted sections after it", StreamingProse(body), body)
	text(t, "a proposal that has begun", StreamingProse(body+"\n\n```json\n{\"proposal\":{\"document\":"), body)
}

// engines/contract.test.ts: "streams explicitly marked JSON examples token by token, including
// nested proposal examples".
func TestStreamsMarkedJSONExamples(t *testing.T) {
	prefix := "```json example\n{\"example\":{\"proposal\":{\"document\":"
	text(t, "a marked example", StreamingProse(prefix), prefix)
	if HasProposalFence(prefix + "{}}}}\n```") {
		t.Error("a `json example` fence is explanatory, never a proposal")
	}
}

// engines/contract.test.ts: "releases legacy JSON examples once their value is complete, without
// waiting for the closing fence or final reply".
func TestReleasesLegacyJSONExamples(t *testing.T) {
	prefix := "Example:\n\n```json\n"
	text(t, "an incomplete value", StreamingProse(prefix+"{\"status\":"), "Example:")
	text(t, "a complete value", StreamingProse(prefix+"{\"status\":\"valid\"}"), prefix+"{\"status\":\"valid\"}")
	nested := prefix + "{\"example\":{\"proposal\":{}}}"
	text(t, "a nested proposal example", StreamingProse(nested+"\n```"), nested+"\n```")
}

// engines/contract.test.ts: "never flashes an incomplete legacy proposal".
func TestNeverFlashesAnIncompleteProposal(t *testing.T) {
	for _, body := range []string{
		`{"proposal":{"document":{"id":"p"}}}`,
		`{"metadata":1,"proposal":{"document":{"id":"p"}}}`,
		`{"propo\u0073al":{"document":{"id":"p"}}}`,
	} {
		answer := "Before.\n\n```json\n" + body + "\n```\n\nAfter."
		begin := strings.Index(answer, "```")
		for n := begin; n <= strings.Index(answer, "After."); n++ {
			if shown := StreamingProse(answer[:n]); shown != "Before." {
				t.Fatalf("%s at %d: got %q", body, n, shown)
			}
		}
		text(t, body, StreamingProse(answer), "Before.\n\n\nAfter.")
	}
}

// engines/contract.test.ts: "handles tildes, longer fences, CRLF, indented examples, and inline
// backticks without freezing prose".
func TestFencesOfEveryShape(t *testing.T) {
	for _, code := range []string{"~~~sql\r\nselect 1;\r\n~~~", "````text\n```\n````", "    ```python\n    print(1)\n    ```", "Inline ``a ` b`` continues."} {
		body := code + "\n\nNext paragraph."
		text(t, code, StreamingProse(body), body)
		whole := body + "\n```json\n{\"proposal\":{\"document\":{}}}\n```"
		text(t, code+" with a proposal", StreamingProse(whole), ProseOf(whole))
	}
}

// A delegated child's report blocks (reportBlocks), read by the proposal fence's own reader
// (delegationResults.test.ts reads them out of a child's answer).
func TestReportBlocks(t *testing.T) {
	report := `{"report":{"summary":"Done.","findings":[],"citations":[],"openQuestions":[],"confidence":"high","notDone":[]}}`
	answer := "My findings.\n\n```json\n" + report + "\n```\n"
	bodies, prose := ReportBlocks(answer)
	// The body is the fence's contents, the newline before its closing line included, as the
	// TypeScript's slice keeps it.
	if len(bodies) != 1 || bodies[0] != report+"\n" {
		t.Fatalf("the report block, as the child wrote it: %q", bodies)
	}
	text(t, "the prose without it", prose, "My findings.")
	if bodies, _ := ReportBlocks("No report here."); len(bodies) != 0 {
		t.Errorf("an answer with no report block has none: %v", bodies)
	}
}

// The messages that carry a text call's result back (tool-result/text-result.json;
// engines/vercel/capabilities.test.ts holds a run's requests to each).
func TestTextToolResultMessages(t *testing.T) {
	file := vector(t, "tool-result/text-result.json")
	heads := []struct{ name string }{{"validate"}, {"the tool call"}, {"write_file"}}
	for at, held := range heads {
		content := value(file.Items[at]).get("content").s
		line := strings.SplitN(content, "\n", 2)
		text(t, "the head of message "+jsNumber(float64(at)), line[0], TextToolResultHeader(held.name, at > 0))
	}
	// The refusal a call of a tool the run was not offered earns, with the default refusal where
	// the dispatch said nothing.
	refusal, err := RefusedUnoffered(context.Background(),
		func(context.Context, string, json.RawMessage) error { return nil }, "write_file", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(value(file.Items[2]).get("content").s, refusal) {
		t.Errorf("the refusal the vector carries:\n got %q", refusal)
	}
	// The dispatch's own refusal, and a cancelled run that stops rather than reporting a refusal
	// that never happened.
	refusal, err = RefusedUnoffered(context.Background(),
		func(context.Context, string, json.RawMessage) error { return errors.New("the gate refused it") }, "write_file", nil)
	if err != nil {
		t.Fatal(err)
	}
	text(t, "the dispatch's refusal", refusal, UnofferedRefusal("the gate refused it"))
	if _, err := RefusedUnoffered(context.Background(),
		func(context.Context, string, json.RawMessage) error { return failure.Cancelled }, "write_file", nil); !failure.IsCancelled(err) {
		t.Errorf("a cancelled run stops here: %v", err)
	}
}

// The tools an endpoint with no native tool calls is told about (textToolInstructions), and the
// one reformat request a parse failure earns (reformatRequest).
func TestTextToolInstructionsAndReformat(t *testing.T) {
	instructions := TextToolInstructions([]DescribedTool{
		{Name: "validate", Description: "Validate a pack.", Schema: json.RawMessage(`{"type":"object"}`)},
		{Name: "get_schema", Schema: json.RawMessage(`{"type": "object", "properties": {}}`)},
	})
	if !strings.HasPrefix(instructions, "TOOLS\nThis endpoint takes no native tool calls") {
		t.Errorf("the instructions begin with the heading: %q", instructions)
	}
	if !strings.HasSuffix(instructions, "- validate: Validate a pack.\n  input schema: {\"type\":\"object\"}\n"+
		"- get_schema: \n  input schema: {\"type\":\"object\",\"properties\":{}}") {
		t.Errorf("each tool's name, description and schema:\n%q", instructions)
	}
	_, err := ExtractProposal("```json\n{\"proposal\":{\"document\":null}}\n```")
	if err == nil {
		t.Fatal("the proposal does not read")
	}
	text(t, "the reformat request", ReformatRequest(err),
		ReformatInstructions+" The problem: the proposal must contain a document object.")
	text(t, "a failure with no detail", ReformatRequest(errors.New("something else")), ReformatInstructions)
}

// A time limit as a person reads it (limitText), and the failure a tool call past its deadline
// earns (toolTimeout).
func TestLimitTextAndToolTimeout(t *testing.T) {
	for _, held := range []struct {
		limit time.Duration
		want  string
	}{
		{60 * time.Second, "1 minute"},
		{120 * time.Second, "2 minutes"},
		{90 * time.Second, "90 seconds"},
		{time.Second, "1 second"},
		{1500 * time.Millisecond, "1500 milliseconds"},
		{time.Millisecond, "1 millisecond"},
	} {
		text(t, held.limit.String(), LimitText(held.limit), held.want)
	}
	err := ToolTimeout(30 * time.Second)
	held := failureOf(t, err)
	if held.Code != "timeout" || held.Source != "tool" || held.Outcome != "unknown" {
		t.Errorf("the class of a tool past its deadline: %+v", held)
	}
	text(t, "what the model reads", err.Error(),
		"The tool did not answer within 30 seconds and is no longer waited for; it may still complete. Its outcome is unknown; check it before repeating the call.")
}

// One call bounded by a deadline as well as by the run (withDeadline): the deadline passing is the
// call's own failure; the run ending is the run unwinding.
func TestWithDeadline(t *testing.T) {
	late := func() error { return ToolTimeout(10 * time.Millisecond) }
	answered, err := WithDeadline(context.Background(), time.Minute, late, func(context.Context) (string, error) {
		return "answered", nil
	})
	if err != nil || answered != "answered" {
		t.Errorf("the work's own answer: %q %v", answered, err)
	}
	if _, err := WithDeadline(context.Background(), 10*time.Millisecond, late, func(ctx context.Context) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}); failureOf(t, err).Code != "timeout" {
		t.Errorf("the deadline passing is the call's own failure: %v", err)
	}
	closed, stop := context.WithCancel(context.Background())
	stop()
	// A thunk, so the work is not even started on a closed run.
	started := 0
	if _, err := WithDeadline(closed, time.Minute, late, func(context.Context) (string, error) {
		started++
		return "never asked", nil
	}); !failure.IsCancelled(err) {
		t.Errorf("a closed run is cancelled, not timed out: %v", err)
	}
	if started != 0 {
		t.Error("no work is started on a closed run")
	}
	// A run that closed while the work ran is owed nothing, whatever the work then answered.
	closing, end := context.WithCancel(context.Background())
	if _, err := WithDeadline(closing, time.Minute, late, func(context.Context) (string, error) {
		end()
		return "an answer for a run that is over", nil
	}); !failure.IsCancelled(err) {
		t.Errorf("the run closed while the work ran: %v", err)
	}
	// The work's own failure is its own.
	own := errors.New("the transport failed")
	if _, err := WithDeadline(context.Background(), time.Minute, late, func(context.Context) (string, error) {
		return "", own
	}); !errors.Is(err, own) {
		t.Errorf("the work's own failure: %v", err)
	}
}

// A call on the text path keeps the model's arguments, byte for byte (#275). Elsewhere a parsed value still
// writes as JSON.stringify writes it.
func TestAFencedCallKeepsTheModelsText(t *testing.T) {
	for _, text := range []string{`{"id":9007199254740993}`, `{"q":1e400}`, `{"q":1e-400}`, `{"q":1.0}`, `{"a":1,"a":2}`, `{"b":2,"a":1}`, `{ "c" : 3 }`, `{"o":{"p":[1.50]}}`} {
		call, problem := ExtractToolCall("```json\n{ \"tool\" : \"lookup\" , \"arguments\" :  " + text + "  }\n```")
		if problem != "" || call == nil {
			t.Fatalf("%s: %s", text, problem)
		}
		if string(call.Arguments) != text {
			t.Errorf("%q, not %q", call.Arguments, text)
		}
	}
	parsed, err := parseJSON([]byte(`{"q":1.0}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw(parsed)) != `{"q":1}` {
		t.Errorf("raw wrote %s", raw(parsed))
	}
	parsed.set("q", &jvalue{kind: jnumber, n: 2})
	if string(written(parsed)) != `{"q":2}` {
		t.Errorf("written to, it wrote %s", written(parsed))
	}
}
