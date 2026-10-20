package runcontext

// The run's prompt, its compaction and its tool wrapping (the port of the session cases of
// packages/harness/context/context.test.ts, and the allowance cases of
// certification/scenarios.test.ts, scenario 3).

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func toolCall(id, name string, input *Value) Message {
	return PartsMessage("assistant", ToolCallPart(id, name, input))
}

func toolResult(id, name string, output Output) Message {
	return PartsMessage("tool", ToolResultPart(id, name, output))
}

func signed(part Part, signature string) Part {
	part.ProviderOptions = ObjectOf("google", ObjectOf("thoughtSignature", String(signature)))
	return part
}

func TestThePromptBudgetsEscapedRequestsAndRetainsTheOmittedMaterial(t *testing.T) {
	c := openContext(t, smallWindowSession())
	request := "RULE: keep every decision\n" + strings.Repeat("😀\u0000\"", 45000) + "\nLATEST USER REQUEST: map the dialect"
	projected, err := c.Prompt(request, "Host instructions", noTools(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"LATEST USER REQUEST: map the dialect", "RULE: keep every decision"} {
		if !strings.Contains(projected, want) {
			t.Errorf("the preview does not carry %q", want)
		}
	}
	messages, err := c.Messages([]Message{TextMessage("user", projected)}, "Host instructions", noTools())
	if err != nil {
		t.Fatal(err)
	}
	if UTF8Bytes(MessagesValue(messages).Text()) >= InputBytes(*c.Policy) {
		t.Error("the projected prompt does not fit the input allowance")
	}
	reference := c.Ledger.List(0).Get("items").Items()[0]
	if int(reference.Get("bytes").Num()) != UTF8Bytes(QuoteJSON(request)) {
		t.Errorf("the retained request is %s bytes", reference.Get("bytes").Text())
	}
	held, err := c.Ledger.Read(MustParseJSON(`{"id":` + QuoteJSON(reference.Get("contextId").Str()) + `,"offset":0,"limit":128}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(held.Get("text").Str(), "RULE:") {
		t.Errorf("the retained request opens with %q", held.Get("text").Str())
	}
}

func TestTheCompactionKeepsTheLatestExchangesAndPendingCallsTogether(t *testing.T) {
	c := openContext(t, smallWindowSession())
	messages := []Message{TextMessage("user", "Current task")}
	for i := 0; i < 6; i++ {
		messages = append(messages, toolCall("c"+itoa(i), "read", ObjectOf("id", Number(float64(i)))),
			toolResult("c"+itoa(i), "read", Output{Type: "text", Value: String(strings.Repeat("x", 9000))}))
	}
	messages = append(messages, toolCall("pending", "read", Object()))
	before := MessagesValue(messages).Text()
	projected, err := c.Messages(messages, "host", noTools())
	if err != nil {
		t.Fatal(err)
	}
	if MessagesValue(messages).Text() != before {
		t.Error("the history was changed")
	}
	if MessagesValue(projected[len(projected)-1:]).Text() != MessagesValue(messages[len(messages)-1:]).Text() {
		t.Error("the pending call was not kept whole")
	}
	if !strings.Contains(MessagesValue(projected).Text(), "Completed tool calls must not be repeated") {
		t.Error("the summary's notice is not in the history")
	}
	if c.Ledger.List(0).Get("items").Len() != 1 {
		t.Errorf("%d items were retained", c.Ledger.List(0).Get("items").Len())
	}
	if UTF8Bytes(MessagesValue(projected).Text()) >= InputBytes(*c.Policy) {
		t.Error("the compacted history does not fit the input allowance")
	}
}

func TestTheCompactionDoesNotCompactAnUnresolvedApproval(t *testing.T) {
	c := openContext(t, smallWindowSession())
	approval := Part{Type: "tool-approval-request",
		Other: ObjectOf("type", String("tool-approval-request"), "approvalId", String("approval"), "toolCallId", String("action"))}
	messages := []Message{TextMessage("user", "task"), PartsMessage("assistant", approval),
		TextMessage("user", strings.Repeat("x", 60000)), TextMessage("assistant", "still pending")}
	if _, err := c.Messages(messages, "host", noTools()); err != ErrContextBudget {
		t.Errorf("the compaction answered %v", err)
	}
	if c.Ledger.List(0).Get("items").Len() != 0 {
		t.Error("a refused compaction retained something")
	}
	// Instructions too large for the window are refused, not silently dropped.
	if _, err := c.Prompt("request", strings.Repeat("x", 100000), noTools(), nil); err != ErrContextBudget {
		t.Errorf("oversized instructions answered %v", err)
	}
}

func TestTheCompactionLeavesRoomForFollowUpCalls(t *testing.T) {
	c := openContext(t, smallWindowSession())
	prompt, err := c.Prompt(strings.Repeat("x", 250000)+"\nCurrent request", "host", noTools(), nil)
	if err != nil {
		t.Fatal(err)
	}
	history := []Message{TextMessage("user", prompt),
		toolCall("read", "read_context", Object()),
		toolResult("read", "read_context", Output{Type: "text", Value: String(strings.Repeat("y", 8000))})}
	held, err := c.Messages(history, "host", noTools())
	if err != nil {
		t.Fatal(err)
	}
	if MessagesValue(held).Text() != MessagesValue(history).Text() {
		t.Error("a history that fits was compacted")
	}
}

func TestThePreviewKeepsTheLatestQuestionAndItsInput(t *testing.T) {
	c := openContext(t, smallWindowSession())
	currentRequest := &CurrentRequest{Message: "Why is no-documented-routing a mismatch?",
		Input: "Current pack: exceptions first.\n" + MustParseJSON(`{"cases":[{"id":"no-routing","facts":{"routing":"not-documented"},"expected":"set-aside","actual":"unresolved"}]}`).Text()}
	old := "Host task constraints\n" + strings.Repeat("Earlier snapshot ", 20000) + "\n" + currentRequest.Input
	projected, err := c.Prompt(old, "host", noTools(), currentRequest)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(projected, "Host task constraints") {
		t.Error("the preview lost the host's constraints")
	}
	if !strings.Contains(projected, currentRequest.Value().Text()) {
		t.Error("the preview does not carry the current request")
	}
	if U16Length(projected) >= U16Length(old) {
		t.Error("the preview is not shorter than the prompt")
	}
	refs := c.Ledger.List(0).Get("items").Items()
	if len(refs) != 2 {
		t.Fatalf("%d items were retained", len(refs))
	}
	id := QuoteJSON(refs[1].Get("contextId").Str())
	for _, held := range []struct{ path, want string }{{"/message", currentRequest.Message}, {"/input", currentRequest.Input}} {
		read, err := c.Ledger.Read(MustParseJSON(`{"id":` + id + `,"path":"` + held.path + `"}`))
		if err != nil {
			t.Fatal(err)
		}
		if read.Get("text").Str() != held.want {
			t.Errorf("%s read back as %q", held.path, read.Get("text").Str())
		}
	}
	if userMessageBytes(projected) >= InputBytes(*c.Policy) {
		t.Error("the preview does not fit the input allowance")
	}
}

func TestThePreviewPinsTheQuestionBeforeAHugeInput(t *testing.T) {
	c := openContext(t, smallWindowSession())
	message := "Why is the omitted-size case unresolved?"
	input := message + "\n" + strings.Repeat("irrelevant ", 20000) + "EXACT CASE: missing vs false\n" + strings.Repeat("more ", 20000)
	projected, err := c.Prompt(strings.Repeat("history ", 30000)+input, "host", noTools(), &CurrentRequest{Message: message, Input: input})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(projected, message) {
		t.Error("the preview lost the question")
	}
	var reference *Value
	for _, held := range c.Ledger.List(0).Get("items").Items() {
		if strings.HasPrefix(held.Get("label").Str(), "Latest") {
			reference = held
		}
	}
	if reference == nil {
		t.Fatal("the latest message was not retained")
	}
	result, err := c.Ledger.Read(MustParseJSON(`{"id":` + QuoteJSON(reference.Get("contextId").Str()) + `,"path":"/input","query":"EXACT CASE","limit":1000}`))
	if err != nil {
		t.Fatal(err)
	}
	if result.Get("type").Str() != "search" || !result.Get("found").Truth() ||
		int(result.Get("matchOffset").Num()) != indexOfU16(input, "EXACT CASE", 0) ||
		!strings.Contains(result.Get("text").Str(), "EXACT CASE: missing vs false") {
		t.Errorf("the exact case read back as %s", result.Text())
	}
	if userMessageBytes(projected) >= InputBytes(*c.Policy) {
		t.Error("the preview does not fit the input allowance")
	}
}

func TestThePreviewKeepsContinuationConstraints(t *testing.T) {
	c := openContext(t, smallWindowSession())
	currentRequest := &CurrentRequest{Message: "Explain the failed case", Input: "Current pack and cases"}
	tailText := "Continue read-only. Completed action: save_dialect. Do not repeat it. Latest choice: provided sources only."
	projected, err := c.Prompt("Host constraints\n"+strings.Repeat("old ", 50000)+currentRequest.Input+"\n"+tailText,
		"host", noTools(), currentRequest)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(projected, currentRequest.Value().Text()) || !strings.Contains(projected, tailText) {
		t.Error("the preview lost the current request or the continuation constraints")
	}
}

func TestTheCompactionKeepsExactTypedDataAndDropsSignatures(t *testing.T) {
	c := openContext(t, smallWindowSession())
	signature := strings.Repeat("s", 50000)
	messages := []Message{TextMessage("user", "Apply option 1."),
		PartsMessage("assistant", signed(ToolCallPart("check", "experimental_evaluate",
			ObjectOf("facts", MustParseJSON(`{"flag":false,"zero":0,"nothing":null,"decimal":"24"}`))), signature)),
		PartsMessage("tool", signed(ToolResultPart("check", "experimental_evaluate",
			Output{Type: "json", Value: ObjectOf("outcome", String("set-aside"))}), signature))}
	before := MessagesValue(messages).Text()
	projected, err := c.Messages(messages, "host", noTools())
	if err != nil {
		t.Fatal(err)
	}
	if MessagesValue(messages).Text() != before {
		t.Error("the history was changed")
	}
	held := MessagesValue(projected).Text()
	if !strings.Contains(held, "set-aside") || strings.Contains(held, signature) {
		t.Error("the compaction kept the signature or lost the outcome")
	}
	// agent#12: the summary's steps are one data block closed by the run's marker.
	summary := projected[1].Text
	data, ok := UnframeData(strings.SplitN(summary, "\n", 2)[1], c.Marker)
	if !ok {
		t.Fatalf("the summary is not one data block: %q", summary)
	}
	steps := MustParseJSON(data).Get("steps")
	read, err := c.Ledger.Read(steps.Items()[0].Get("read"))
	if err != nil {
		t.Fatal(err)
	}
	if !equalJSON(read.Get("value"), MustParseJSON(`{"type":"json","value":{"outcome":"set-aside"}}`)) {
		t.Errorf("the step's read answered %s", read.Get("value").Text())
	}
	reference := c.Ledger.List(0).Get("items").Items()[0]
	read, err = c.Ledger.Read(MustParseJSON(`{"id":` + QuoteJSON(reference.Get("contextId").Str()) + `,"path":"/0/content/0/input/facts"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !equalJSON(read.Get("value"), MustParseJSON(`{"flag":false,"zero":0,"nothing":null,"decimal":"24"}`)) {
		t.Errorf("the retained facts are %s", read.Get("value").Text())
	}
	if UTF8Bytes(held) >= InputBytes(*c.Policy) {
		t.Error("the compacted history does not fit the input allowance")
	}
}

func TestTheCompactionKeepsAFlatIndexAcrossRepeatedCompactions(t *testing.T) {
	session := smallWindowSession()
	session.HostTools = []HostTool{{Name: "future_reference_tool", Effect: "read",
		ContextPolicy: &ToolContextPolicy{Reference: true}, Description: "Reference",
		InputSchema: json.RawMessage(`{}`),
		Execute: func(ctx context.Context, args json.RawMessage) (ToolResult, error) {
			return ResultOf(nil, nil), nil
		}}}
	c := openContext(t, session)
	messages := []Message{TextMessage("user", "Make the requested change.")}
	for i := 0; i < 8; i++ {
		name := "check_" + itoa(i)
		output := ObjectOf("outcome", String("checked_"+itoa(i)))
		if i == 0 {
			name = "future_reference_tool"
			output = MustParseJSON(`{"content":[{"type":"text","text":"Authoritative contract fixture"}]}`)
		}
		messages = append(messages,
			PartsMessage("assistant", signed(ToolCallPart("c"+itoa(i), name, Object()), strings.Repeat("s", 40000))),
			toolResult("c"+itoa(i), name, Output{Type: "json", Value: output}))
		held, err := c.Messages(messages, "host", noTools())
		if err != nil {
			t.Fatal(err)
		}
		messages = held
	}
	summary := messages[1].Text
	data, ok := UnframeData(strings.SplitN(summary, "\n", 2)[1], c.Marker)
	if !ok {
		t.Fatal("the summary is not one data block")
	}
	if !strings.Contains(summary, "Authoritative contract fixture") {
		t.Error("the authoring contract was lost")
	}
	steps := MustParseJSON(data).Get("steps")
	if steps.Len() != 8 {
		t.Errorf("the summary carries %d steps", steps.Len())
	}
	for at, step := range steps.Items() {
		read, err := c.Ledger.Read(step.Get("read"))
		if err != nil {
			t.Fatalf("step %d: %v", at, err)
		}
		if read.Get("value") == nil {
			t.Errorf("step %d reads no value", at)
		}
	}
	for _, entry := range c.Ledger.Snapshot().Entries {
		if strings.Contains(string(entry.Value), "Earlier completed steps were compacted") {
			t.Error("a summary was retained as an entry")
		}
	}
}

func TestTheCompactionDoesNotCompactAnUnresolvedCall(t *testing.T) {
	c := openContext(t, smallWindowSession())
	messages := []Message{TextMessage("user", "task"),
		PartsMessage("assistant", signed(ToolCallPart("pending", "save", Object()), strings.Repeat("s", 50000)))}
	if _, err := c.Messages(messages, "host", noTools()); err != ErrContextBudget {
		t.Errorf("the compaction answered %v", err)
	}
}

func TestAnIsolatedPhaseStartsWithNoAuthorMaterial(t *testing.T) {
	session := smallWindowSession()
	session.HostTools = []HostTool{{Name: "future_reference", Effect: "read",
		ContextPolicy: &ToolContextPolicy{Reference: true}, Description: "Read task reference",
		InputSchema: json.RawMessage(`{}`),
		Execute: func(ctx context.Context, args json.RawMessage) (ToolResult, error) {
			return ResultOf(nil, nil), nil
		}}}
	run, err := ContextualSession(context.Background(), session)
	if err != nil {
		t.Fatal(err)
	}
	c := run.Context
	original, err := c.Ledger.Retain(Document{ID: "author-only", Kind: "artifact", Label: "Author material",
		Value: ObjectOf("private", String("AUTHOR_ONLY_SENTINEL"))}, false)
	if err != nil {
		t.Fatal(err)
	}
	var read HostTool
	for _, held := range run.Session.HostTools {
		if held.Name == "read_context" {
			read = held
		}
	}
	if _, err := read.Execute(context.Background(), json.RawMessage(`{"id":`+QuoteJSON(original.ID)+`}`)); err != nil {
		t.Fatal(err)
	}
	pair := func(label string) []Message {
		return []Message{TextMessage("user", "Current explicit task"),
			PartsMessage("assistant", signed(ToolCallPart(label, "future_reference", Object()), strings.Repeat("s", 50000))),
			toolResult(label, "future_reference", Output{Type: "json", Value: ObjectOf("reference", String(label))})}
	}
	held, err := c.Messages(pair("AUTHOR_ONLY_SENTINEL"), "host", noTools())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(MessagesValue(held).Text(), "AUTHOR_ONLY_SENTINEL") {
		t.Error("the author's own summary did not carry its material")
	}
	scope := c.Ledger.Scope()
	c.BeginIsolatedPhase()
	if c.Ledger.Scope() == scope {
		t.Error("the isolated phase kept the author's retrieval scope")
	}
	if _, err := read.Execute(context.Background(), json.RawMessage(`{"id":`+QuoteJSON(original.ID)+`}`)); err == nil {
		t.Error("the isolated phase read the author's material")
	} else {
		refused(t, err, "unavailable")
	}
	review, err := c.Messages(pair("REVIEW_ONLY_SENTINEL"), "host", noTools())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(MessagesValue(review).Text(), "AUTHOR_ONLY_SENTINEL") {
		t.Error("the review's summary carries the author's material")
	}
	snapshot, err := json.Marshal(c.Ledger.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(snapshot), "AUTHOR_ONLY_SENTINEL") {
		t.Error("the review's ledger carries the author's material")
	}
	if !strings.Contains(MessagesValue(review).Text(), "REVIEW_ONLY_SENTINEL") {
		t.Error("the review's own material is not in its summary")
	}
}

func TestTheContextToolNamesAreReserved(t *testing.T) {
	for _, name := range reservedNames {
		session := modelSession()
		session.HostTools = []HostTool{{Name: name, Description: "bad override", InputSchema: json.RawMessage(`{}`),
			Execute: func(ctx context.Context, args json.RawMessage) (ToolResult, error) { return ResultOf(nil, nil), nil }}}
		_, err := ContextualSession(context.Background(), session)
		refused(t, err, "reserved")
	}
	session := modelSession()
	session.Tools = []RuntimeTool{{Name: ReadTool}}
	_, err := ContextualSession(context.Background(), session)
	refused(t, err, "reserved")
}

func TestNoReserveIsTakenForTheBriefOrTheMemory(t *testing.T) {
	// certification/scenarios.test.ts, scenario 3: the allowance is the same whatever the host says
	// of memory. Both capabilities make the same checks.
	for _, kind := range []string{"model", "agent"} {
		bare := func(memory *bool) Session {
			held := Session{Prompt: "", TestPrompt: "", Tools: []RuntimeTool{}, HostTools: []HostTool{},
				CallTool: func(ctx context.Context, name string, args json.RawMessage) (ToolResult, error) {
					return ResultOf(nil, nil), nil
				}}
			if kind == "model" {
				held.Model = &ModelSession{Family: "openai-compatible", Model: "scripted-model"}
			} else {
				held.Agent = &AgentSession{Model: "scripted-agent"}
			}
			if memory != nil {
				held.Context = &Input{Memory: memory}
			}
			return held
		}
		instructions := "instructions"
		// The allowance a run holds: the input bytes less the instructions, the tools and the 1,536
		// bytes kept for the request's framing. The brief and the memory are in the instructions,
		// where they are counted; nothing is reserved for either.
		full := InputBytes(Defaults) - UTF8Bytes(instructions) - UTF8Bytes("[]") - 1536
		on, off := true, false
		for _, said := range []*bool{&on, nil, &off} {
			c := openContext(t, bare(said))
			if err := c.Check(instructions, strings.Repeat("p", full), noTools()); err != nil {
				t.Errorf("%s: the allowance left no room for %d bytes: %v", kind, full, err)
			}
			if err := c.Check(instructions, strings.Repeat("p", full+1), noTools()); err == nil {
				t.Errorf("%s: one byte past the allowance was admitted", kind)
			}
			c.Close()
		}
	}
}

func TestTheUsageCountsTheInputAndTheReserve(t *testing.T) {
	c := openContext(t, modelSession())
	retain(t, c, "page", "tool", "page", String("x"))
	held := c.UsageOf(MessagesValue([]Message{TextMessage("user", "the request")}))
	if held.InputBytes != UTF8Bytes(MessagesValue([]Message{TextMessage("user", "the request")}).Text()) {
		t.Errorf("the usage counted %d bytes", held.InputBytes)
	}
	if held.EstimatedInputTokens != (held.InputBytes+2)/3 {
		t.Errorf("the usage estimated %d tokens for %d bytes", held.EstimatedInputTokens, held.InputBytes)
	}
	if held.InputBudgetBytes != InputBytes(*c.Policy) || held.OutputReserveTokens != c.Policy.OutputTokens {
		t.Errorf("the usage is %+v", held)
	}
	if held.RetainedItems != 1 || held.Compactions != 0 {
		t.Errorf("the usage is %+v", held)
	}
}

func TestReserveOutputKeepsTheWindowsHalf(t *testing.T) {
	c := openContext(t, modelSession())
	if err := c.ReserveOutput(c.Policy.OutputTokens - 1); err != nil {
		t.Fatal(err)
	}
	if c.Policy.OutputTokens != Defaults.OutputTokens {
		t.Error("a smaller reserve changed the policy")
	}
	if err := c.ReserveOutput(c.Policy.ContextWindowTokens/2 + 1); err != ErrContextBudget {
		t.Errorf("a reserve past half the window answered %v", err)
	}
	if err := c.ReserveOutput(c.Policy.ContextWindowTokens / 2); err != nil {
		t.Fatal(err)
	}
	if c.Policy.OutputTokens != c.Policy.ContextWindowTokens/2 {
		t.Errorf("the reserve is %d", c.Policy.OutputTokens)
	}
}

func TestTheModelsAreAskedOnceAndReadClosed(t *testing.T) {
	// The host's answer for one run: each member a model name, or the run's own model where it
	// names none; asked once, and a refused answer refused at each read (runModels in host.ts).
	asked := 0
	session := modelSession()
	session.ModelsFor = func(identity RunIdentity) (NamedModels, error) {
		asked++
		if identity.Engine != "eino" || identity.Model != "small" || identity.Family != "openai-compatible" {
			t.Errorf("the host was asked with %+v", identity)
		}
		return NamedModels{Critic: "critic-model"}, nil
	}
	c := openContext(t, session)
	for at := 0; at < 2; at++ {
		held, err := c.Models("eino")
		if err != nil {
			t.Fatal(err)
		}
		if held.Critic != "critic-model" || held.Child != "small" {
			t.Errorf("the models are %+v", held)
		}
	}
	if asked != 1 {
		t.Errorf("the host was asked %d times", asked)
	}
	// An answer that is not a model name is refused at each read.
	refusing := modelSession()
	refusing.ModelsFor = func(identity RunIdentity) (NamedModels, error) {
		return NamedModels{Child: "a model\nname"}, nil
	}
	other := openContext(t, refusing)
	for at := 0; at < 2; at++ {
		if _, err := other.Models("eino"); err == nil {
			t.Error("a line break in a model name was admitted")
		}
	}
	// A host that names none gives the run's own model.
	plain := openContext(t, modelSession())
	held, err := plain.Models("eino")
	if err != nil {
		t.Fatal(err)
	}
	if held.Critic != "small" || held.Child != "small" {
		t.Errorf("the models are %+v", held)
	}
	// And concurrentRequests is a whole number from one to sixteen.
	for _, count := range []int{0, 17} {
		held := count
		if _, err := ReadRunModels(RunIdentity{Model: "small"}, NamedModels{ConcurrentRequests: &held}); err == nil {
			t.Errorf("a concurrency of %d was admitted", count)
		}
	}
}
