package runcontext

// The chat-scoped context (the port of packages/harness/context/chat.test.ts, the cases
// ChatContext, its ledger and the session's opening decide; the ones that drive an engine against
// a scripted endpoint are the adapter's).

import (
	"fmt"
	"math"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

const testMarker = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// chatRun is one run's turn on a chat, with a ledger over it (fixtureRun in chat.test.ts).
func chatRun(t *testing.T, chat *ChatContext, retainedBytes int) (*ChatTurn, *ContextLedger) {
	t.Helper()
	turn, err := chat.OpenTurn()
	if err != nil {
		t.Fatal(err)
	}
	policy := Defaults
	policy.RetainedBytes = retainedBytes
	return turn, NewContextLedger(&policy, turn)
}

// runtimeAnswer is the shape a runtime answer takes: one text part of the value's JSON, and the
// value as structured content (runtimeAnswer in testing/scripted.ts).
func runtimeAnswer(value *Value) ToolResult {
	return ResultOf([]Content{TextContent(value.Text())}, value)
}

// indexData is the JSON inside an index's data block.
func indexData(t *testing.T, text string) *Value {
	t.Helper()
	if !strings.HasPrefix(text, IndexNotice) {
		t.Fatalf("the index does not open with its notice: %q", text[:min(80, len(text))])
	}
	found := regexp.MustCompile(`(?m)^` + Opening + ` ([0-9a-f]{32}) from ` + IndexSource + `;`).FindStringSubmatch(text)
	if found == nil {
		t.Fatal("the index carries no data block")
	}
	data, ok := UnframeData(text[len(IndexNotice):], found[1])
	if !ok {
		t.Fatal("the index's block is not one block")
	}
	held, err := ParseJSON([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	return held
}

// keepResult keeps one of the index vector's fixture results.
func keepResult(t *testing.T, turn *ChatTurn, ledger *ContextLedger, result *Value) {
	t.Helper()
	if err := turn.Keep(ledger, result.Get("tool").Str(), result.Get("arguments"),
		runtimeAnswer(result.Get("value")), "", false); err != nil {
		t.Fatal(err)
	}
}

func TestTheCacheKeyIsTheContractVector(t *testing.T) {
	vector := readJSON(t, "contract/vectors/chat/cache-key.json")
	keys := vector.Get("keys").Items()
	for at, item := range vector.Get("items").Items() {
		if held := CanonicalJSON(item); held != keys[at].Get("canonical").Str() {
			t.Errorf("item %d canonicalizes to %s", at, held)
		}
		var runtime *RuntimeIdentity
		if held := item.Get("runtime"); held != nil {
			runtime = &RuntimeIdentity{Identity: held.Get("identity").Str(), Version: held.Get("version").Str()}
		}
		key, ok := ReadCacheKeyOf(item.Get("tool").Str(), item.Get("arguments"), runtime != nil, runtime)
		if !ok || key != keys[at].Get("sha256").Str() {
			t.Errorf("item %d keys to %s", at, key)
		}
	}
}

func TestTheCacheKeyCarriesTheArguments(t *testing.T) {
	runtime := &RuntimeIdentity{Identity: "jpack", Version: "0.19.0"}
	one, _ := ReadCacheKeyOf("get_schema", MustParseJSON(`{"spec_version":"0.2.0-draft"}`), true, runtime)
	two, _ := ReadCacheKeyOf("get_schema", MustParseJSON(`{"spec_version":"0.1.0-draft"}`), true, runtime)
	if one == two {
		t.Error("two calls that differ only in their arguments have one key")
	}
	// A Runtime-served read the host named no Runtime for is not kept across runs.
	if _, ok := ReadCacheKeyOf("get_schema", Object(), true, nil); ok {
		t.Error("a read with no Runtime was given a key")
	}
}

func TestTheIndexIsTheContractVector(t *testing.T) {
	vector := readJSON(t, "contract/vectors/chat/turn-index.json")
	fixture := vector.Get("fixture")
	chat := NewChatContext(fixture.Get("scope").Str())
	turn, ledger := chatRun(t, chat, Defaults.RetainedBytes)
	for _, result := range fixture.Get("results").Items()[:3] {
		keepResult(t, turn, ledger, result)
	}
	// A run sees nothing of its own turn in the index.
	if held := turn.Index(Defaults.TurnIndexEntries, Defaults.TurnIndexBytes, testMarker, IndexNoRoomBound); held != "" {
		t.Errorf("the first run was shown an index: %q", held)
	}
	ledger.Release()
	second, _ := chatRun(t, chat, Defaults.RetainedBytes)
	held := indexData(t, second.Index(Defaults.TurnIndexEntries, Defaults.TurnIndexBytes, testMarker, IndexNoRoomBound))
	if !equalJSON(held, vector.Get("items").Items()[0]) {
		t.Errorf("the index is\n%s\nand the vector is\n%s", held.Text(), vector.Get("items").Items()[0].Text())
	}
}

func TestTheIndexSaysWhatWasEvicted(t *testing.T) {
	vector := readJSON(t, "contract/vectors/chat/turn-index.json")
	fixture := vector.Get("fixture")
	bound := int(fixture.Get("retainedBytes").Num())
	chat := NewChatContext(fixture.Get("scope").Str())
	first, ledger := chatRun(t, chat, bound+10_000)
	for _, result := range fixture.Get("results").Items()[:3] {
		keepResult(t, first, ledger, result)
	}
	ledger.Release()
	ids := chat.EntryIDs()
	instructions, schema, tests := ids[0], ids[1], ids[2]
	second, secondLedger := chatRun(t, chat, bound)
	second.Index(Defaults.TurnIndexEntries, Defaults.TurnIndexBytes, testMarker, IndexNoRoomBound)
	keepResult(t, second, secondLedger, fixture.Get("results").Items()[3])
	// The two oldest went, in the order they were retained; the third was enough room.
	held := chat.EntryIDs()
	if len(held) != 2 || held[0] != tests || !strings.Contains(held[1], ":result-get_example-") {
		t.Errorf("the chat holds %v", held)
	}
	if chat.Size() > bound {
		t.Errorf("the chat holds %d bytes", chat.Size())
	}
	// A read of an evicted entry says why it is gone.
	_, err := secondLedger.Read(MustParseJSON(`{"id":` + QuoteJSON(schema) + `}`))
	refused(t, err, Evicted)
	_, err = secondLedger.ReadUnits(MustParseJSON(`{"id":` + QuoteJSON(instructions) + `}`))
	refused(t, err, Evicted)
	secondLedger.Release()
	third, thirdLedger := chatRun(t, chat, bound)
	data := indexData(t, third.Index(Defaults.TurnIndexEntries, Defaults.TurnIndexBytes, testMarker, IndexNoRoomBound))
	if !equalJSON(data, vector.Get("items").Items()[1]) {
		t.Errorf("the index is\n%s\nand the vector is\n%s", data.Text(), vector.Get("items").Items()[1].Text())
	}
	// Each eviction is listed once: once an index that lists it is sent, the next lists it no more.
	third.Sent()
	thirdLedger.Release()
	fourth, _ := chatRun(t, chat, bound)
	if held := indexData(t, fourth.Index(Defaults.TurnIndexEntries, Defaults.TurnIndexBytes, testMarker, IndexNoRoomBound)); held.Get("evicted").Len() != 0 {
		t.Errorf("the evictions were listed again: %s", held.Get("evicted").Text())
	}
}

func TestTheChatEvictsByAge(t *testing.T) {
	chat := NewChatContext("")
	document := func(id string) Document {
		return Document{ID: id, Kind: "tool", Label: id, Value: String(strings.Repeat("x", 200))}
	}
	// Each entry is 202 bytes: three fit under 700, four do not.
	first, firstLedger := chatRun(t, chat, 700)
	a, err := firstLedger.Retain(document("a"), false)
	if err != nil {
		t.Fatal(err)
	}
	b, err := firstLedger.Retain(document("b"), false)
	if err != nil {
		t.Fatal(err)
	}
	_ = first
	firstLedger.Release()
	_, secondLedger := chatRun(t, chat, 700)
	// Read in turn two: a is now younger than b.
	if _, err := secondLedger.Read(MustParseJSON(`{"id":` + QuoteJSON(a.ID) + `}`)); err != nil {
		t.Fatal(err)
	}
	c, err := secondLedger.Retain(document("c"), false)
	if err != nil {
		t.Fatal(err)
	}
	d, err := secondLedger.Retain(document("d"), false)
	if err != nil {
		t.Fatal(err)
	}
	if held := chat.EntryIDs(); len(held) != 3 || held[0] != a.ID || held[1] != c.ID || held[2] != d.ID {
		t.Errorf("the chat holds %v", held)
	}
	if chat.HasEntry(b.ID) {
		t.Error("b was not evicted")
	}
	// Everything left is the open run's: nothing more can be evicted, so the bound refuses.
	if _, err := secondLedger.Retain(document("e"), false); err != ErrContextBudget {
		t.Errorf("the bound answered %v", err)
	}
	secondLedger.Release()
	_, thirdLedger := chatRun(t, chat, 700)
	e, err := thirdLedger.Retain(document("e"), false)
	if err != nil {
		t.Fatal(err)
	}
	// Of a, c and d (all turn two's), the first retained goes first.
	if held := chat.EntryIDs(); len(held) != 3 || held[0] != c.ID || held[1] != d.ID || held[2] != e.ID {
		t.Errorf("the chat holds %v", held)
	}
}

func TestTheIndexIsBoundedByThePolicy(t *testing.T) {
	chat := NewChatContext("")
	first, ledger := chatRun(t, chat, Defaults.RetainedBytes)
	for at := 0; at < 30; at++ {
		if err := first.Keep(ledger, "get_example", ObjectOf("name", String("example-"+itoa(at))),
			runtimeAnswer(ObjectOf("example", Number(float64(at)))), "", false); err != nil {
			t.Fatal(err)
		}
	}
	ledger.Release()
	second, secondLedger := chatRun(t, chat, Defaults.RetainedBytes)
	listed := indexData(t, second.Index(20, 1_000_000, testMarker, IndexNoRoomBound))
	if listed.Get("steps").Len() != 20 || int(listed.Get("omitted").Num()) != 10 {
		t.Errorf("the index lists %d rows and omits %s", listed.Get("steps").Len(), listed.Get("omitted").Text())
	}
	if held := listed.Get("steps").Items()[0].Get("firstLine").Str(); held != `{"example":10}` {
		t.Errorf("the first row shows %q", held)
	}
	none := indexData(t, second.Index(0, 1_000_000, testMarker, IndexNoRoomBound))
	if none.Get("steps").Len() != 0 || int(none.Get("omitted").Num()) != 30 {
		t.Errorf("an index of no rows is %s", none.Text())
	}
	secondLedger.Release()
	third, _ := chatRun(t, chat, Defaults.RetainedBytes)
	text := third.Index(20, 3000, testMarker, IndexNoRoomBound)
	if UTF8Bytes(text) > 3000 {
		t.Errorf("the index is %d bytes", UTF8Bytes(text))
	}
	bounded := indexData(t, text)
	if bounded.Get("steps").Len() >= 20 {
		t.Errorf("the bounded index lists %d rows", bounded.Get("steps").Len())
	}
	if bounded.Get("steps").Len()+int(bounded.Get("omitted").Num()) != 30 {
		t.Errorf("the bounded index accounts for %d of 30", bounded.Get("steps").Len()+int(bounded.Get("omitted").Num()))
	}
	last := bounded.Get("steps").Items()[bounded.Get("steps").Len()-1]
	if last.Get("firstLine").Str() != `{"example":29}` {
		t.Errorf("the newest row is %s", last.Text())
	}
}

// chatWith is a chat whose first run kept count results, each a few hundred bytes.
func chatWith(t *testing.T, count int) *ChatContext {
	t.Helper()
	chat := NewChatContext("")
	turn, ledger := chatRun(t, chat, Defaults.RetainedBytes)
	for at := 0; at < count; at++ {
		if err := turn.Keep(ledger, "get_example", ObjectOf("name", String("example-"+itoa(at))),
			runtimeAnswer(ObjectOf("example", Number(float64(at)), "text", String(strings.Repeat("An example the Runtime serves. ", 6)))), "", false); err != nil {
			t.Fatal(err)
		}
	}
	ledger.Release()
	return chat
}

// longestWhole is the longest prompt a context sends whole with the instructions and no tools.
func longestWhole(t *testing.T, c *SessionContext, instructions string) string {
	t.Helper()
	fits := ""
	for step := 1 << 17; step > 0; step >>= 1 {
		candidate := fits + strings.Repeat("p", step)
		held, err := c.Prompt(candidate, instructions, noTools(), nil)
		if err == nil && held == candidate {
			fits = candidate
		}
	}
	return fits
}

func TestTheIndexNeverPreviewsAPromptSentWhole(t *testing.T) {
	instructions := strings.Repeat("i", 4000)
	indexed := []int{}
	for _, window := range []int{8192, 9216, 10240, 11264, 12288} {
		plain := openContext(t, windowed(window, nil))
		fits := longestWhole(t, plain, instructions)
		if len(fits) <= 1000 {
			t.Fatalf("at %d tokens only %d bytes fit whole", window, len(fits))
		}
		parts, err := plain.Opening(fits, instructions, noTools(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(parts) != 1 || parts[0] != fits {
			t.Fatalf("at %d tokens the whole prompt was projected", window)
		}
		plain.Close()
		chat := chatWith(t, 30)
		// Untrimmed, the index would be most of the 8,000 bytes turnIndexBytes allows.
		turn, err := chat.OpenTurn()
		if err != nil {
			t.Fatal(err)
		}
		if whole := turn.Index(Defaults.TurnIndexEntries, Defaults.TurnIndexBytes, testMarker, IndexNoRoomBound); UTF8Bytes(whole) <= 7000 {
			t.Fatalf("the whole index is %d bytes", UTF8Bytes(whole))
		}
		kept := openContext(t, windowed(window, chat))
		parts, err = kept.Opening(fits, instructions, noTools(), nil)
		if err != nil {
			t.Fatal(err)
		}
		// At the longest whole prompt nothing is left for an index: none is sent.
		if len(parts) != 1 || parts[0] != fits {
			t.Errorf("at %d tokens the opening is %d parts", window, len(parts))
		}
		kept.Close()
		// With room, an index is sent within a quarter of the allowance.
		policy, err := ContextPolicy(windowed(window, nil).Context.Configuration, "scripted-model", nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		allowance := int(math.Floor(float64(InputBytes(policy)-UTF8Bytes(instructions)-2-policy.BriefBytes-policy.MemoryBytes-1536) * 0.6))
		room := openContext(t, windowed(window, chat))
		short, err := room.Opening("Change the pack.", instructions, noTools(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if short[0] != "Change the pack." || len(short) > 2 {
			t.Fatalf("at %d tokens the opening is %v", window, short)
		}
		if len(short) == 2 {
			indexed = append(indexed, window)
			if PartBytes(short[1]) > int(math.Floor(float64(allowance)*IndexShare)) {
				t.Errorf("at %d tokens the index costs %d of %d", window, PartBytes(short[1]), int(math.Floor(float64(allowance)*IndexShare)))
			}
			if int(indexData(t, short[1]).Get("omitted").Num()) <= 0 {
				t.Errorf("at %d tokens the index omitted nothing", window)
			}
		}
		room.Close()
	}
	// A quarter of the allowance holds an index from 9,216 tokens up; at 8,192, none is sent.
	if len(indexed) != 4 || indexed[0] != 9216 {
		t.Errorf("an index was sent at %v", indexed)
	}
}

func TestTheEvictionsAreTakenOnlyOnceAnIndexThatListsThemIsSent(t *testing.T) {
	vector := readJSON(t, "contract/vectors/chat/turn-index.json")
	fixture := vector.Get("fixture")
	bound := int(fixture.Get("retainedBytes").Num())
	chat := NewChatContext(fixture.Get("scope").Str())
	first, firstLedger := chatRun(t, chat, bound+10_000)
	for _, result := range fixture.Get("results").Items()[:3] {
		keepResult(t, first, firstLedger, result)
	}
	firstLedger.Release()
	second, secondLedger := chatRun(t, chat, bound)
	keepResult(t, second, secondLedger, fixture.Get("results").Items()[3])
	secondLedger.Release()
	// Too small for even an index with no rows: nothing is sent, nothing is taken.
	third, thirdLedger := chatRun(t, chat, bound)
	if held := third.Index(20, 500, testMarker, IndexNoRoomBound); held != "" {
		t.Errorf("a 500-byte bound sent %q", held)
	}
	third.Sent()
	thirdLedger.Release()
	fourth, fourthLedger := chatRun(t, chat, bound)
	if held := indexData(t, fourth.Index(Defaults.TurnIndexEntries, Defaults.TurnIndexBytes, testMarker, IndexNoRoomBound)); !equalJSON(held, vector.Get("items").Items()[1]) {
		t.Errorf("the index is %s", held.Text())
	}
	fourthLedger.Release()
	// And through a run's first message: with no room the index is left out, and the evictions wait.
	tight := openContext(t, windowed(8192, chat))
	fits := longestWhole(t, openContext(t, windowed(8192, nil)), strings.Repeat("i", 4000))
	parts, err := tight.Opening(fits, strings.Repeat("i", 4000), noTools(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 1 {
		t.Errorf("the tight opening is %d parts", len(parts))
	}
	tight.Close()
	roomy := openContext(t, windowed(65536, chat))
	parts, err = roomy.Opening("Change the pack.", "instructions", noTools(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 2 {
		t.Fatalf("the roomy opening is %d parts", len(parts))
	}
	evicted := indexData(t, parts[1]).Get("evicted")
	if evicted.Len() != 2 || evicted.Items()[0].Get("tool").Str() != "get_authoring_instructions" ||
		evicted.Items()[1].Get("tool").Str() != "get_schema" {
		t.Errorf("the index lists %s", evicted.Text())
	}
	roomy.Close()
	// Sent once: the next run's index lists them no more.
	after := openContext(t, windowed(65536, chat))
	parts, err = after.Opening("Again.", "instructions", noTools(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if held := indexData(t, parts[1]).Get("evicted"); held.Len() != 0 {
		t.Errorf("the evictions were listed again: %s", held.Text())
	}
	after.Close()
}

func TestTheIndexIsNotSentToARunResumedFromAnotherChat(t *testing.T) {
	source := NewChatContext("")
	resumed := chatWith(t, 3)
	session := func(chat *ChatContext, execution *contract.Checkpoint) Session {
		held := windowed(65536, chat)
		held.Prompt = "Change the pack."
		held.Context.CurrentRequest = &CurrentRequest{Message: "Change the pack.", Input: "Change the pack."}
		held.Context.Execution = execution
		return held
	}
	writer := openContext(t, session(source, nil))
	if err := writer.Execution.Initialize(); err != nil {
		t.Fatal(err)
	}
	retain(t, writer, "page", "tool", "page", String("A page the first chat read."))
	saved, err := writer.Execution.Snapshot("")
	if err != nil {
		t.Fatal(err)
	}
	writer.Close()
	if saved.Context.Scope != source.Scope() {
		t.Errorf("the checkpoint's scope is %s", saved.Context.Scope)
	}
	reader := openContext(t, session(resumed, &saved))
	if err := reader.Execution.Initialize(); err != nil {
		t.Fatal(err)
	}
	if reader.Ledger.Chat() != nil {
		t.Error("the resumed run works in the chat's ledger")
	}
	parts, err := reader.Opening("Change the pack.", "instructions", noTools(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 1 || parts[0] != "Change the pack." {
		t.Errorf("the resumed opening is %v", parts)
	}
	reader.Close()
	// The chat it was handed still has its index for the next run.
	next := openContext(t, session(resumed, nil))
	parts, err = next.Opening("Change the pack.", "instructions", noTools(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 2 || indexData(t, parts[1]).Get("steps").Len() != 3 {
		t.Errorf("the next run's opening is %v", parts)
	}
	next.Close()
}

func TestTheChatIsHeldToRetainedBytesWhenACheckpointIsRestored(t *testing.T) {
	chat := NewChatContext("")
	document := func(id string) Document {
		return Document{ID: id, Kind: "tool", Label: id, Value: String(strings.Repeat("x", 200))}
	}
	_, firstLedger := chatRun(t, chat, 700)
	if _, err := firstLedger.Retain(document("a"), false); err != nil {
		t.Fatal(err)
	}
	saved := firstLedger.Snapshot()
	firstLedger.Release()
	// The chat lets a go, then fills up with others.
	_, secondLedger := chatRun(t, chat, 202)
	if _, err := secondLedger.Retain(document("b"), false); err != nil {
		t.Fatal(err)
	}
	secondLedger.Release()
	_, thirdLedger := chatRun(t, chat, 404)
	if _, err := thirdLedger.Retain(document("c"), false); err != nil {
		t.Fatal(err)
	}
	if chat.Size() != 404 {
		t.Errorf("the chat holds %d bytes", chat.Size())
	}
	thirdLedger.Release()
	// Restored under a 404-byte bound: a comes back, and an older entry goes to make room.
	_, resumed := chatRun(t, chat, 404)
	if err := resumed.Restore(saved); err != nil {
		t.Fatal(err)
	}
	if !chat.HasEntry(saved.Entries[0].ID) {
		t.Error("the restored entry is not in the chat")
	}
	if chat.Size() > 404 {
		t.Errorf("the chat holds %d bytes", chat.Size())
	}
}

func TestARunsCheckpointCarriesWhatItRetainedOrRead(t *testing.T) {
	vector := readJSON(t, "contract/vectors/chat/turn-index.json")
	fixture := vector.Get("fixture")
	chat := NewChatContext(fixture.Get("scope").Str())
	first, firstLedger := chatRun(t, chat, Defaults.RetainedBytes)
	for _, result := range fixture.Get("results").Items()[:3] {
		keepResult(t, first, firstLedger, result)
	}
	firstLedger.Release()
	ids := chat.EntryIDs()
	instructions, schema := ids[0], ids[1]
	_, secondLedger := chatRun(t, chat, Defaults.RetainedBytes)
	if _, err := secondLedger.Read(MustParseJSON(`{"id":` + QuoteJSON(schema) + `}`)); err != nil {
		t.Fatal(err)
	}
	saved := secondLedger.Snapshot()
	if saved.Scope != chat.Scope() {
		t.Errorf("the checkpoint's scope is %s", saved.Scope)
	}
	if len(saved.Entries) != 1 || saved.Entries[0].ID != schema {
		t.Errorf("the checkpoint carries %d entries", len(saved.Entries))
	}
	secondLedger.Release()
	// Resumed in the same chat after the entry went: it is retained again; nothing else changes.
	chat.DeleteEntry(schema)
	_, resumed := chatRun(t, chat, Defaults.RetainedBytes)
	if err := resumed.Restore(saved); err != nil {
		t.Fatal(err)
	}
	if !chat.HasEntry(schema) || !chat.HasEntry(instructions) {
		t.Error("the chat did not take the entry back")
	}
	resumed.Release()
	// Resumed under another chat context: a ledger of the run's own, the other chat untouched.
	other := NewChatContext("")
	_, elsewhere := chatRun(t, other, Defaults.RetainedBytes)
	if err := elsewhere.Restore(saved); err != nil {
		t.Fatal(err)
	}
	if elsewhere.Chat() != nil {
		t.Error("the resumed ledger is the other chat's")
	}
	held, err := elsewhere.Read(MustParseJSON(`{"id":` + QuoteJSON(schema) + `}`))
	if err != nil {
		t.Fatal(err)
	}
	if held.Get("contextId").Str() != schema {
		t.Errorf("the read answered %s", held.Text())
	}
	if other.Size() != 0 || len(other.EntryIDs()) != 0 {
		t.Error("the other chat took the entries")
	}
}

func TestTheChatKeepsItsLedgerUntilItIsDisposed(t *testing.T) {
	chat := NewChatContext("")
	turn, ledger := chatRun(t, chat, Defaults.RetainedBytes)
	keepResult(t, turn, ledger, MustParseJSON(`{"tool":"get_schema","arguments":{},"value":{"type":"object"}}`))
	ledger.Release()
	if chat.TurnCount() != 1 || len(chat.EntryIDs()) != 1 {
		t.Errorf("the chat holds %d entries after %d turns", len(chat.EntryIDs()), chat.TurnCount())
	}
	// A second run's turn, and then no open turn: a bound of three bytes evicts everything.
	_, second := chatRun(t, chat, Defaults.RetainedBytes)
	second.Release()
	if chat.TurnCount() != 2 {
		t.Errorf("%d turns were opened", chat.TurnCount())
	}
	_, third := chatRun(t, chat, 3)
	if _, err := third.Retain(Document{ID: "new", Kind: "tool", Label: "new", Value: String("n")}, false); err != nil {
		t.Fatal(err)
	}
	if held := chat.Entries(); len(held) != 1 || held[0].Label != "new" {
		t.Errorf("the chat holds %d entries", len(held))
	}
	third.Release()
	chat.Dispose()
	if len(chat.EntryIDs()) != 0 || !chat.IsDisposed() {
		t.Error("dispose left the chat holding entries")
	}
	// Every later run is refused.
	if _, err := chat.OpenTurn(); err != ErrDisposed {
		t.Errorf("a later turn answered %v", err)
	}
	session := windowed(65536, chat)
	if _, err := NewSessionContext(session); err == nil {
		t.Error("a run on a disposed chat opened")
	}
}

func TestTheIndexIsPresentOnTurnsTwoAndThree(t *testing.T) {
	// The multi-turn checks of certification/scenarios.test.ts (scenario 6) that ChatContext alone
	// decides: turn one's three reads are listed on turns two and three, within the bounds.
	chat := NewChatContext("")
	reads := []string{"get_schema", "get_authoring_instructions", "read_pack_tests"}
	first, ledger := chatRun(t, chat, Defaults.RetainedBytes)
	for _, name := range reads {
		if err := first.Keep(ledger, name, Object(), runtimeAnswer(ObjectOf("tool", String(name))), "", false); err != nil {
			t.Fatal(err)
		}
	}
	ledger.Release()
	held := len(chat.EntryIDs())
	for turn := 2; turn <= 3; turn++ {
		later, laterLedger := chatRun(t, chat, Defaults.RetainedBytes)
		text := later.Index(Defaults.TurnIndexEntries, Defaults.TurnIndexBytes, testMarker, IndexNoRoomBound)
		if text == "" {
			t.Fatalf("turn %d was shown no index", turn)
		}
		data := indexData(t, text)
		if data.Get("steps").Len() != len(reads) || int(data.Get("omitted").Num()) != 0 {
			t.Errorf("turn %d is shown %s", turn, data.Text())
		}
		for at, step := range data.Get("steps").Items() {
			if step.Get("tool").Str() != reads[at] {
				t.Errorf("turn %d lists %s at %d", turn, step.Get("tool").Str(), at)
			}
		}
		if UTF8Bytes(text) > Defaults.TurnIndexBytes || data.Get("steps").Len() > Defaults.TurnIndexEntries {
			t.Errorf("turn %d's index is %d bytes", turn, UTF8Bytes(text))
		}
		later.Sent()
		laterLedger.Release()
	}
	// The ledger survives each run.
	if len(chat.EntryIDs()) != held || held == 0 {
		t.Errorf("the chat holds %d entries", len(chat.EntryIDs()))
	}
	if chat.TurnCount() != 3 {
		t.Errorf("%d turns were opened", chat.TurnCount())
	}
}

func TestAChildsTurnSeesOnlyItsReferences(t *testing.T) {
	// agent#15: a delegated child's view of the chat.
	chat := NewChatContext("")
	first, ledger := chatRun(t, chat, Defaults.RetainedBytes)
	parent, err := ledger.Retain(Document{ID: "page", Kind: "tool", Label: "A page", Value: String("the parent's page")}, false)
	if err != nil {
		t.Fatal(err)
	}
	other, err := ledger.Retain(Document{ID: "other", Kind: "tool", Label: "Another page", Value: String("not given")}, false)
	if err != nil {
		t.Fatal(err)
	}
	ledger.Release()
	_ = first
	visible := map[string]bool{parent.ID: true}
	child, err := chat.ChildScope(visible).OpenTurn()
	if err != nil {
		t.Fatal(err)
	}
	policy := Defaults
	childLedger := NewContextLedger(&policy, child)
	if _, err := childLedger.Read(MustParseJSON(`{"id":` + QuoteJSON(parent.ID) + `}`)); err != nil {
		t.Fatal(err)
	}
	_, err = childLedger.Read(MustParseJSON(`{"id":` + QuoteJSON(other.ID) + `}`))
	refused(t, err, NotReferenced)
	// Its first message carries no index: its brief names its references.
	if held := child.Index(Defaults.TurnIndexEntries, Defaults.TurnIndexBytes, testMarker, IndexNoRoomBound); held != "" {
		t.Errorf("the child was shown an index: %q", held)
	}
	// What the child retains itself joins what it may read.
	own, err := childLedger.Retain(Document{ID: "own", Kind: "tool", Label: "Its own", Value: String("read by the child")}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !visible[own.ID] {
		t.Error("what the child retained is not in its references")
	}
}

// Children run at once on one chat (agent#15): each opens its turn, retains, reads, keeps its task
// and a reusable result while its siblings do, and the chat holds every entry. Run under -race this
// is the lock discipline's check; without the chat's lock it ends in concurrent map writes.
func TestChildrenUseOneChatAtOnce(t *testing.T) {
	chat := NewChatContext("")
	parent, _ := chatRun(t, chat, Defaults.RetainedBytes)
	shared := map[string]bool{}
	var wg sync.WaitGroup
	errs := make(chan error, 8*20)
	for child := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			visible := map[string]bool{}
			for at := range 20 {
				turn, err := chat.ChildScope(visible).OpenTurn()
				if err != nil {
					errs <- err
					return
				}
				ledger := NewContextLedger(&Defaults, turn)
				id := fmt.Sprintf("c%d-%d", child, at)
				entry, err := ledger.Retain(Document{ID: id, Kind: "tool", Label: id, Value: String(id)}, false)
				if err != nil {
					errs <- err
					return
				}
				if _, err := ledger.Read(MustParseJSON(`{"id":` + QuoteJSON(entry.ID) + `}`)); err != nil {
					errs <- err
					return
				}
				chat.Reveal(shared, entry.ID)
				_ = chat.KeepTask([]string{id}, &TaskThread{Profile: "analyze", Title: id, Visible: visible})
				_ = chat.KeepResult(id, ReusableResult{Task: id, Text: id})
				_ = chat.Reusable(id)
				ledger.Release()
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	parent.Release()
	if len(shared) != 8*20 || chat.Size() == 0 {
		t.Fatalf("%d entries revealed, the chat holds %d bytes", len(shared), chat.Size())
	}
}
