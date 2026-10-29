package runcontext

// Receipt quarantine (agent#242; the amendment to ADR-0027): an external read whose receipt does not
// verify is withheld, and nothing of its result is kept where a context reader, a later request or
// a Plan step could reach it. Held to the hand-written
// contract/vectors/tool-result/receipt-quarantine.json, as receiptQuarantine.test.ts holds the
// TypeScript. The runs that drive the API and native adapters with an in-process model peer are
// go/adapter/receipt_quarantine_test.go.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
)

const quarantineVectorFile = "contract/vectors/tool-result/receipt-quarantine.json"

// quarantineCalls counts one source tool's executions and verifier calls; where entered is set, the
// verifier says it was entered, and where release is set, it waits on it.
type quarantineCalls struct {
	executions, verifications atomic.Int32
	entered                   chan struct{}
	release                   chan struct{}
	once                      sync.Once
}

// quarantineTool is a declaration of the vector's as a host tool whose executor answers result with
// receipt, and whose verifier answers as verifier says: "rejects", "answers true after the
// deadline", or "accepts its receipt for its result".
func quarantineTool(t *testing.T, declaration, result *Value, receipt, verifier string, calls *quarantineCalls) HostTool {
	t.Helper()
	tool := HostTool{Name: declaration.Get("name").Str(), Description: declaration.Get("description").Str(), InputSchema: declaration.Get("inputSchema").JSON()}
	if err := json.Unmarshal(declaration.Get("taxonomy").JSON(), &tool.Taxonomy); err != nil {
		t.Fatal(err)
	}
	source := declaration.Get("source")
	tool.Source = &ToolSource{Kind: source.Get("kind").Str(), ID: source.Get("id").Str(), Trust: source.Get("trust").Str(), Version: source.Get("version").Str()}
	tool.Execute = func(ctx context.Context, _ json.RawMessage) (ToolResult, error) {
		calls.executions.Add(1)
		if receiptAdmissionOf(ctx) != nil {
			t.Error("the host's own execute was handed the call's receipt check")
		}
		held := ReadToolResult(result.Clone())
		held.Receipt = receipt
		return held, nil
	}
	tool.Source.VerifyReceipt = func(ctx context.Context, e ReceiptEvidence) (bool, error) {
		calls.verifications.Add(1)
		if calls.entered != nil {
			calls.once.Do(func() { close(calls.entered) })
		}
		if calls.release != nil {
			<-calls.release
		}
		switch verifier {
		case "accepts its receipt for its result":
			return e.Receipt == receipt && e.Result == CanonicalJSON(result), nil
		case "answers true after the deadline":
			<-ctx.Done()
			return true, nil
		}
		return false, nil
	}
	return tool
}

// quarantineSession is a run on chat, recorded in store, with the source tools pinned.
func quarantineSession(chat *ChatContext, store RunStore, tools []HostTool, sourceMilliseconds int) Session {
	session := modelSession()
	session.AllowConversation = true
	session.Context = &Input{CurrentRequest: &CurrentRequest{Message: "Read record e0", Input: "e0"}, Chat: chat}
	if store != nil {
		session.Context.RunStore = &DurableRunInput{Store: store, ID: "receipt-quarantine"}
	}
	session.HostTools = pinLocalFixture(tools)
	if sourceMilliseconds > 0 {
		session.Policy = PolicyLayer{"budget": map[string]any{"sourceCallMilliseconds": sourceMilliseconds}}
	}
	return session
}

// quarantineEvent is the tool_result event a call answered with, as the vector's items state it:
// its callId and source record left out.
func quarantineEvent(name string, result ToolResult, err error) *Value {
	if err != nil {
		held := failure.Normalize(err, "tool", time.Time{})
		raw, _ := json.Marshal(held)
		text := ""
		if held.Detail != nil {
			text = *held.Detail
		}
		return ObjectOf("type", String("tool_result"), "name", String(name), "isError", Bool(true), "text", String(text), "structured", ObjectOf("failure", MustParseJSON(string(raw))))
	}
	texts := []string{}
	for _, part := range result.Content {
		if part.Type() == "text" {
			texts = append(texts, part.Text())
		}
	}
	out := ObjectOf("type", String("tool_result"), "name", String(name), "isError", Bool(result.Err()), "text", String(strings.Join(texts, "\n")))
	if result.StructuredContent != nil {
		out.Set("structured", result.StructuredContent.Clone())
	}
	return out
}

// sameEvent fails where got is not the vector's item.
func sameEvent(t *testing.T, what string, got, want *Value) {
	t.Helper()
	if CanonicalJSON(got) != CanonicalJSON(want) {
		t.Fatalf("%s is\n%s\nnot\n%s", what, CanonicalJSON(got), CanonicalJSON(want))
	}
}

// retainedAs is the id the run's chat would have retained result under (ChatTurn.Keep).
func retainedAs(scope, wire string, result *Value) string {
	text := string(ReadToolResult(result.Clone()).JSON())
	digest := ContextDigest(text)
	return scope + ":result-" + wire + "-" + digest + ":" + digest[:16]
}

// readerArguments are a reader's arguments with $retained replaced by id.
func readerArguments(reader *Value, id string) json.RawMessage {
	args := reader.Get("arguments").Clone()
	for _, key := range args.Keys() {
		if args.Get(key).IsString() && args.Get(key).Str() == "$retained" {
			args.Set(key, String(id))
		}
	}
	return args.JSON()
}

// storedBytes is every checkpoint and event the store holds for the run, as text.
func storedBytes(t *testing.T, store *MemoryRunStore) string {
	t.Helper()
	ctx := context.Background()
	head, err := store.Load(ctx, "receipt-quarantine")
	if err != nil || head == nil {
		t.Fatalf("no stored run: %v", err)
	}
	out := string(head.Checkpoint)
	after := ""
	for {
		page, err := store.ReadEvents(ctx, "receipt-quarantine", after, 100)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range page.Items {
			out += "\n" + string(item.Bytes)
			after = item.Cursor
		}
		if !page.HasMore {
			return out
		}
	}
}

// dispatchesOf are the durable rows of one tool.
func dispatchesOf(book *DurableBook, tool string) []DurableDispatch {
	out := []DurableDispatch{}
	if book == nil {
		return out
	}
	for _, row := range book.Dispatches {
		if row.Tool == tool {
			out = append(out, row)
		}
	}
	return out
}

// withheldRow fails where the rejected call is not recorded once, as an uncertain outcome whose
// failure is the gate's refusal, with no result; or where it is offered or accepted as Plan evidence.
func withheldRow(t *testing.T, run *Run, wire string, refusal *Value) {
	t.Helper()
	book, scope := run.Context.Execution.PlanProof()
	rows := dispatchesOf(book, wire)
	if len(rows) != 1 {
		t.Fatalf("the call is recorded %d times", len(rows))
	}
	row := rows[0]
	ctx := context.Background()
	if _, err := hostTool(t, run, "update_plan").Execute(ctx, json.RawMessage(`{"explanation":"Read the record","plan":[{"step":"Read the record","status":"in_progress"}]}`)); err != nil {
		t.Fatal(err)
	}
	controller := run.Context.Execution
	args := ObjectOf("id", controller.State().Plan.Items()[0].Get("id"), "expectedRevision", controller.State().PlanState.Get("revision"), "outcome", String("completed"), "evidence", ObjectOf("operations", Array(String(row.ID)), "artifacts", Array()))
	if _, err := hostTool(t, run, "complete_plan_step").Execute(ctx, args.JSON()); err == nil || !strings.Contains(err.Error(), "unsuccessful-operation") {
		t.Fatalf("a Plan step accepted the rejected call: %v", err)
	}
	if reason := planEvidenceFailure(ObjectOf("operations", Array(String(row.ID)), "artifacts", Array()), book, scope); reason != "unsuccessful-operation" {
		t.Fatalf("the rejected call is Plan evidence: %q", reason)
	}
	if strings.Contains(controller.RequestState(), row.ID) {
		t.Fatal("the rejected call is offered as Plan evidence")
	}
	if row.State != "unknown" || row.Result != nil || row.RepeatResultJson != nil || row.Failure == nil || row.Failure.Code != "tool-failed" || row.Failure.Outcome != "unknown" || row.Failure.Detail == nil || *row.Failure.Detail != refusal.Get("text").Str() {
		raw, _ := json.Marshal(row)
		t.Fatalf("the rejected call is recorded as %s", raw)
	}
	for _, observed := range book.Observations {
		if observed.DispatchID == row.ID {
			t.Fatal("the rejected call is observed as an operation")
		}
	}
}

func TestAnUnverifiedReceiptLeavesNothingAReaderCanReach(t *testing.T) {
	vector := readJSON(t, quarantineVectorFile)
	fixture, items := vector.Get("fixture"), vector.Get("items").Items()
	sentinel, wire := fixture.Get("sentinel").Str(), fixture.Get("wireName").Str()
	for _, held := range fixture.Get("cases").Items() {
		t.Run(held.Get("name").Str(), func(t *testing.T) {
			ctx := context.Background()
			chat := NewChatContext(fixture.Get("scope").Str())
			store := NewMemoryRunStore(MemoryRunStoreOptions{})
			calls := &quarantineCalls{}
			receipt := ""
			if given := held.Get("receipt"); given != nil {
				receipt = given.Str()
			}
			tool := quarantineTool(t, fixture.Get("declaration"), held.Get("result"), receipt, held.Get("verifier").Str(), calls)
			milliseconds := 0
			if held.Get("name").Str() == "expired-receipt" {
				milliseconds = int(fixture.Get("sourceCallMilliseconds").Num())
			}
			run := openRun(t, quarantineSession(chat, store, []HostTool{tool}, milliseconds))
			defer run.Context.Close()
			result, err := hostTool(t, run, wire).Execute(WithCallID(ctx, "call-e0"), fixture.Get("arguments").JSON())
			sameEvent(t, "the source's answer", quarantineEvent(wire, result, err), items[0])
			retained := retainedAs(chat.Scope(), wire, held.Get("result"))
			for at, reader := range fixture.Get("readers").Items() {
				name := reader.Get("tool").Str()
				result, err := hostTool(t, run, name).Execute(ctx, readerArguments(reader, retained))
				sameEvent(t, name+"'s answer", quarantineEvent(name, result, err), items[at+1])
			}
			if calls.executions.Load() != 1 || calls.verifications.Load() != int32(held.Get("verifierCalls").Num()) {
				t.Fatalf("executions %d, verifier calls %d", calls.executions.Load(), calls.verifications.Load())
			}
			if len(chat.EntryIDs()) != 0 {
				t.Fatalf("the chat retained %v", chat.EntryIDs())
			}
			if strings.Contains(run.Context.Execution.RequestState(), sentinel) {
				t.Fatal("the next request's state carries the result")
			}
			withheldRow(t, run, wire, items[0])
			if strings.Contains(storedBytes(t, store), sentinel) {
				t.Fatal("a stored checkpoint or event carries the result")
			}
			// The chat's next run is shown no index of it.
			next, err := chat.OpenTurn()
			if err != nil {
				t.Fatal(err)
			}
			defer next.Release()
			if index := next.Index(Defaults.TurnIndexEntries, Defaults.TurnIndexBytes, testMarker, IndexNoRoomBound); strings.Contains(index, sentinel) || strings.Contains(index, retained) {
				t.Fatalf("the next run's index carries it: %q", index)
			}
		})
	}
}

func TestOverlappingCallsAndARestoredCheckpointKeepOnlyTheVerifiedResult(t *testing.T) {
	vector := readJSON(t, quarantineVectorFile)
	fixture, items := vector.Get("fixture"), vector.Get("items").Items()
	overlap := fixture.Get("overlap")
	sentinel, wire, other := fixture.Get("sentinel").Str(), fixture.Get("wireName").Str(), overlap.Get("wireName").Str()
	rejected := fixture.Get("cases").Items()[0]
	ctx := context.Background()
	chat := NewChatContext(fixture.Get("scope").Str())
	store := NewMemoryRunStore(MemoryRunStoreOptions{})
	held := &quarantineCalls{entered: make(chan struct{}), release: make(chan struct{})}
	verified := &quarantineCalls{}
	tools := []HostTool{
		quarantineTool(t, fixture.Get("declaration"), rejected.Get("result"), rejected.Get("receipt").Str(), rejected.Get("verifier").Str(), held),
		quarantineTool(t, overlap.Get("declaration"), overlap.Get("result"), overlap.Get("receipt").Str(), overlap.Get("verifier").Str(), verified),
	}
	run := openRun(t, quarantineSession(chat, store, tools, 0))
	defer run.Context.Close()
	type answer struct {
		result ToolResult
		err    error
	}
	done := make(chan answer, 1)
	go func() {
		result, err := hostTool(t, run, wire).Execute(WithCallID(ctx, "call-e0"), fixture.Get("arguments").JSON())
		done <- answer{result, err}
	}()
	select {
	case <-held.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the rejected call's receipt check was never entered")
	}
	// While that check runs, the other source's calls run and are recorded whole.
	for at, args := range overlap.Get("calls").Items() {
		result, err := hostTool(t, run, other).Execute(WithCallID(ctx, "call-b"+itoa(at+1)), args.JSON())
		sameEvent(t, "the verified source's answer", quarantineEvent(other, result, err), overlap.Get("event"))
	}
	head, err := store.Load(ctx, "receipt-quarantine")
	if err != nil || head == nil {
		t.Fatal("no checkpoint was written while the receipt check ran", err)
	}
	written := append([]byte(nil), head.Checkpoint...)
	close(held.release)
	var got answer
	select {
	case got = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the rejected call never answered")
	}
	sameEvent(t, "the rejected call's answer", quarantineEvent(wire, got.result, got.err), items[0])
	if strings.Contains(string(written), sentinel) {
		t.Fatal("the checkpoint written while the receipt check ran carries the result")
	}
	var saved contract.Checkpoint
	if err := json.Unmarshal(written, &saved); err != nil {
		t.Fatal(err)
	}
	retained := retainedAs(chat.Scope(), wire, rejected.Get("result"))
	kept := retainedAs(chat.Scope(), other, overlap.Get("result"))
	if len(saved.Context.Entries) != 1 || saved.Context.Entries[0].ID != kept {
		raw, _ := json.Marshal(saved.Context.Entries)
		t.Fatalf("the checkpoint written while the receipt check ran holds %s", raw)
	}
	// Restored, as a resumed run's ledger is: the verified result is read whole, the rejected one is
	// answered as the items say.
	policy := Defaults
	restored := NewContextLedger(&policy, NewEntryStore(saved.Context.Scope))
	if err := restored.Restore(saved.Context); err != nil {
		t.Fatal(err)
	}
	readers := map[string]HostTool{}
	for _, tool := range restored.Tools() {
		readers[tool.Name] = tool
	}
	for _, ledger := range []map[string]HostTool{readers, nil} {
		reader := func(name string) HostTool {
			if ledger != nil {
				return ledger[name]
			}
			return hostTool(t, run, name)
		}
		read, err := reader("read_context").Execute(ctx, json.RawMessage(`{"id":`+QuoteJSON(kept)+`}`))
		if err != nil || !strings.Contains(string(read.JSON()), "VERIFIED_B1_PAYLOAD") {
			t.Fatalf("the verified result is not read whole: %v %s", err, read.JSON())
		}
		listed, err := reader("list_context").Execute(ctx, json.RawMessage(`{}`))
		if err != nil || listed.StructuredContent.Get("items").Len() != 1 || listed.StructuredContent.Get("items").Items()[0].Get("contextId").Str() != kept {
			t.Fatalf("the listing is %v %s", err, listed.JSON())
		}
		for at, reader := range fixture.Get("readers").Items()[1:] {
			name := reader.Get("tool").Str()
			var result ToolResult
			var err error
			if ledger != nil {
				result, err = ledger[name].Execute(ctx, readerArguments(reader, retained))
			} else {
				result, err = hostTool(t, run, name).Execute(ctx, readerArguments(reader, retained))
			}
			sameEvent(t, name+"'s answer", quarantineEvent(name, result, err), items[at+2])
		}
	}
	if held.executions.Load() != 1 || held.verifications.Load() != 1 || verified.executions.Load() != 2 || verified.verifications.Load() != 2 {
		t.Fatalf("executions %d and %d, verifier calls %d and %d", held.executions.Load(), verified.executions.Load(), held.verifications.Load(), verified.verifications.Load())
	}
	withheldRow(t, run, wire, items[0])
	book, _ := run.Context.Execution.PlanProof()
	for _, row := range dispatchesOf(book, other) {
		if row.State != "result" || row.Failure != nil {
			t.Fatalf("a verified call is recorded as %s", row.State)
		}
	}
	if strings.Contains(storedBytes(t, store), sentinel) {
		t.Fatal("a stored checkpoint or event carries the result")
	}
}

// The check runs once: a result the durable writer refused is not checked again by the read
// wrapper or the gate, and a refused one is never kept by any of them.
func TestTheReceiptCheckRunsOnceAndRefusesEverywhere(t *testing.T) {
	checks := 0
	admission := &receiptAdmission{check: func(result ToolResult) (ToolResult, bool) {
		checks++
		return result, false
	}}
	result := ResultOf([]Content{TextContent("UNVERIFIED")}, nil)
	for range 3 {
		err := admission.admit(result)
		var held *failure.Error
		if !errors.As(err, &held) || held.Failure.Code != "tool-failed" || held.Failure.Outcome != "unknown" || held.Failure.Detail == nil || *held.Failure.Detail != receiptWithheldDetail {
			t.Fatalf("the refusal is %v", err)
		}
	}
	if _, verified, ran := admission.outcome(); checks != 1 || verified || !ran {
		t.Fatalf("checked %d times", checks)
	}
	ctx := context.WithValue(context.Background(), receiptAdmissionKey{}, admission)
	if receiptAdmissionOf(withoutReceiptAdmission(ctx)) != nil || receiptAdmissionOf(ctx) != admission {
		t.Fatal("the host's own execute is handed the call's check")
	}
}

// The read wrapper keeps nothing its call's check refuses, whatever ran below it: not in the chat,
// not in the run's read cache; and it keeps what the check verifies.
func TestTheReadWrapperRetainsNothingItsCheckRefuses(t *testing.T) {
	sentinel := readJSON(t, quarantineVectorFile).Get("fixture").Get("sentinel").Str()
	chat := NewChatContext("receipt-quarantine")
	run := openRun(t, quarantineSession(chat, nil, nil, 0))
	defer run.Context.Close()
	ran := 0
	read := run.Context.Execution.WrapRead("records__read_record", func(context.Context, json.RawMessage) (ToolResult, error) {
		ran++
		return ResultOf([]Content{TextContent(sentinel)}, nil), nil
	}, &ToolContextPolicy{Immutable: true}, true, &KeptRead{})
	for _, verified := range []bool{false, false, true} {
		admission := &receiptAdmission{check: func(result ToolResult) (ToolResult, bool) { return result, verified }}
		_, err := read(context.WithValue(context.Background(), receiptAdmissionKey{}, admission), json.RawMessage(`{"id":"e0"}`))
		if (err == nil) != verified {
			t.Fatalf("verified %v answered %v", verified, err)
		}
		if held := len(chat.EntryIDs()); (held == 1) != verified {
			t.Fatalf("verified %v left %d entries", verified, held)
		}
	}
	if ran != 3 {
		t.Fatalf("a refused result was served again: %d runs", ran)
	}
}

// quarantineGateway is the shared Gateway v3 vector's source (contract/testdata/taxonomy/
// gateway-receipt-v3.json) as a host tool whose executor answers a synthetic envelope signed for the
// call's own receipt session, carrying extra as an unsigned salts member where it is given.
func quarantineGateway(t *testing.T, extra string, executions *atomic.Int32, last *ToolResult) HostTool {
	t.Helper()
	v := receiptVectors(t)
	endpoint := "https://example.invalid/mcp"
	snapshot := receiptSnapshot(&endpoint)
	approval := receiptApproval(&endpoint)
	_, trust, _ := syntheticReceipt(t, v, &endpoint)
	digest, err := DescriptorSnapshotDigest(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	tool := HostTool{Name: v.Tool, Taxonomy: approval.ToolTaxonomies[v.Tool], Description: "approved", InputSchema: json.RawMessage(`{"type":"object"}`),
		Source: &ToolSource{Kind: "mcp", ID: "tickets", Trust: "connected", ToolListDigest: digest, DescriptorSnapshot: snapshot, DescriptorApproval: approval, GatewayReceipt: trust,
			CurrentDescriptorSnapshot: func(context.Context) ([]byte, error) { return snapshot, nil }}}
	var mu sync.Mutex
	tool.Execute = func(ctx context.Context, _ json.RawMessage) (ToolResult, error) {
		executions.Add(1)
		invocation, ok := GatewayInvocation(ctx)
		if !ok {
			return ToolResult{}, errors.New("no receipt session")
		}
		held := v
		held.Session = invocation.ReceiptSession
		envelope, _, _ := syntheticReceipt(t, held, &endpoint)
		if extra != "" {
			envelope["salts"].(map[string]string)["note"] = extra
		}
		mu.Lock()
		defer mu.Unlock()
		*last = receiptOuter(t, envelope)
		return *last, nil
	}
	return tool
}

// A Gateway envelope that verifies but carries a salts member its commitments do not open is
// withheld like any unverified result (the #247 review's F1): the envelope is what the wrappers
// keep, so its unsigned bytes would otherwise be ordinary retained context.
func TestAGatewayEnvelopeWithAnUnsignedSaltsMemberIsWithheld(t *testing.T) {
	vector := readJSON(t, quarantineVectorFile)
	fixture, items := vector.Get("fixture"), vector.Get("items").Items()
	sentinel := fixture.Get("sentinel").Str()
	ctx := context.Background()
	chat := NewChatContext(fixture.Get("scope").Str())
	store := NewMemoryRunStore(MemoryRunStoreOptions{})
	var executions atomic.Int32
	var last ToolResult
	session := quarantineSession(chat, store, nil, 0)
	session.HostTools = []HostTool{quarantineGateway(t, sentinel, &executions, &last)}
	run := openRun(t, session)
	defer run.Context.Close()
	wire := WireName("tickets", "query")
	result, err := hostTool(t, run, wire).Execute(WithCallID(ctx, "call-gateway"), json.RawMessage(`{"id":7}`))
	refusal := items[0].Clone()
	refusal.Set("name", String(wire))
	sameEvent(t, "the source's answer", quarantineEvent(wire, result, err), refusal)
	if !strings.Contains(string(last.JSON()), sentinel) {
		t.Fatal("the envelope did not carry the unsigned member")
	}
	retained := retainedAs(chat.Scope(), wire, last.Value())
	for at, reader := range fixture.Get("readers").Items() {
		name := reader.Get("tool").Str()
		result, err := hostTool(t, run, name).Execute(ctx, readerArguments(reader, retained))
		sameEvent(t, name+"'s answer", quarantineEvent(name, result, err), items[at+1])
	}
	if executions.Load() != 1 || len(chat.EntryIDs()) != 0 || strings.Contains(run.Context.Execution.RequestState(), sentinel) {
		t.Fatalf("executions %d, entries %v", executions.Load(), chat.EntryIDs())
	}
	withheldRow(t, run, wire, items[0])
	if strings.Contains(storedBytes(t, store), sentinel) {
		t.Fatal("a stored checkpoint or event carries the unsigned member")
	}
}

// The gate answers the checked result, for a Gateway source the signed artifact, never the
// envelope the run returned (the #247 review's F2): at the gate alone, where the gate checks it
// itself, and through the session's wrappers, where the durable writer has checked it.
func TestTheGateAnswersTheSignedArtifactNotTheEnvelope(t *testing.T) {
	v := receiptVectors(t)
	raw, _ := json.Marshal(v.Artifact)
	artifact := CanonicalJSON(MustParseJSON(string(raw)))
	var executions atomic.Int32
	var last ToolResult
	tool := quarantineGateway(t, "", &executions, &last)
	declared, err := DeclareSources([]HostTool{tool}, nil)
	if err != nil || len(declared.HostTools) != 1 {
		t.Fatalf("Gateway source refused: %v", err)
	}
	rows := []contract.ProvenanceSourceFigures{}
	policy := Defaults
	gate := NewSourceGate(declared, &policy, "marker", "json-schema", func() *[]contract.ProvenanceSourceFigures { return &rows }, nil)
	offered := declared.HostTools[0]
	answered, err := gate.Call(context.Background(), offered.Name, json.RawMessage(`{"id":7}`), offered.Execute)
	if err != nil || CanonicalJSON(answered.Value()) != artifact || CanonicalJSON(last.Value()) == artifact {
		t.Fatalf("the gate answered %v %s", err, answered.JSON())
	}
	session := quarantineSession(NewChatContext("receipt-quarantine"), NewMemoryRunStore(MemoryRunStoreOptions{}), nil, 0)
	session.HostTools = []HostTool{tool}
	run := openRun(t, session)
	defer run.Context.Close()
	answered, err = hostTool(t, run, WireName("tickets", "query")).Execute(WithCallID(context.Background(), "call-gateway"), json.RawMessage(`{"id":7}`))
	if err != nil || CanonicalJSON(answered.Value()) != artifact || strings.Contains(string(answered.JSON()), "salts") {
		t.Fatalf("the session answered %v %s", err, answered.JSON())
	}
	if executions.Load() != 2 {
		t.Fatalf("executions %d", executions.Load())
	}
}
