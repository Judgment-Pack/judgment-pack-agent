package runcontext

// The shared scoped context (the ledger cases of packages/harness/context/context.test.ts).

import (
	"strings"
	"testing"
)

func TestTheLedgerRetainsTypedDataAndReadsExactPointers(t *testing.T) {
	ledger := NewContextLedger(&Policy{ToolResultBytes: Defaults.ToolResultBytes, ReadBytes: Defaults.ReadBytes,
		RetainedBytes: Defaults.RetainedBytes}, nil)
	// A member whose value is undefined is not in the JSON a host hands the ledger, so the Go
	// original is the JSON the TypeScript's snapshot makes of its own.
	original := MustParseJSON(`{"nothing":null,"zero":0,"flag":false,"decimal":"50","a/b":{"~key":"exact"}}`)
	e, err := ledger.Retain(Document{ID: "facts", Kind: "artifact", Revision: "r4", Label: "Current facts", Value: original}, false)
	if err != nil {
		t.Fatal(err)
	}
	// The entry is a copy: a later change to the host's own value does not reach it.
	original.Set("decimal", String("999"))
	read := func(args string) *Value {
		t.Helper()
		held, err := ledger.Read(MustParseJSON(args))
		if err != nil {
			t.Fatal(err)
		}
		return held
	}
	id := QuoteJSON(e.ID)
	if held := read(`{"id":` + id + `,"path":"/decimal"}`); held.Get("text").Str() != "50" || held.Get("revision").Str() != "r4" {
		t.Errorf("the decimal read back as %s", held.Text())
	}
	for _, held := range []struct{ path, want string }{{"/flag", "false"}, {"/nothing", "null"}, {"/zero", "0"}} {
		if value := read(`{"id":` + id + `,"path":"` + held.path + `"}`); value.Get("value").Text() != held.want {
			t.Errorf("%s read back as %s", held.path, value.Text())
		}
	}
	if held := read(`{"id":` + id + `,"path":"/a~1b/~0key"}`); held.Get("text").Str() != "exact" {
		t.Errorf("the escaped pointer read back as %s", held.Text())
	}
	for _, held := range []struct{ args, want string }{
		{`{"id":` + id + `,"path":"/missing"}`, "does not exist"},
		{`{"id":` + id + `,"path":"/__proto__"}`, "does not exist"},
		{`{"id":` + id + `,"sha256":"stale"}`, "revision changed"},
	} {
		_, err := ledger.Read(MustParseJSON(held.args))
		refused(t, err, held.want)
	}
	// Another execution's ledger holds nothing of this one.
	elsewhere := NewContextLedger(&Policy{ReadBytes: Defaults.ReadBytes, RetainedBytes: Defaults.RetainedBytes}, nil)
	_, err = elsewhere.Read(MustParseJSON(`{"id":` + id + `}`))
	refused(t, err, "unavailable")
	ledger.Clear()
	_, err = ledger.Read(MustParseJSON(`{"id":` + id + `}`))
	refused(t, err, "unavailable")
}

func TestTheLedgerPagesUnicodeAndEscapedTextWithoutLosingBytes(t *testing.T) {
	c := openContext(t, smallWindowSession())
	text := strings.Repeat("😀é\u0001\"\\\n", 5000)
	e := retain(t, c, "source", "tool", "Page", String(text))
	read, offset := "", 0
	for {
		answered, err := call(t, tool(t, c, "read_context"), `{"id":`+QuoteJSON(e.ID)+`,"offset":`+itoa(offset)+`}`)
		if err != nil {
			t.Fatal(err)
		}
		projected, err := c.Result("read_context", answered)
		if err != nil {
			t.Fatal(err)
		}
		if UTF8Bytes(string(projected.JSON())) > Defaults.ToolResultBytes {
			t.Fatalf("a page is %d bytes", UTF8Bytes(string(projected.JSON())))
		}
		page := projected.StructuredContent
		read += page.Get("text").Str()
		next := page.Get("nextOffset")
		if next.Kind() == KindNull {
			break
		}
		offset = int(next.Num())
	}
	if read != text {
		t.Error("the text did not come back whole")
	}
	if held := c.Ledger.List(0).Get("items").Len(); held != 1 {
		t.Errorf("%d items are retained", held)
	}
}

func TestTheLedgerExternalizesLargeResults(t *testing.T) {
	c := openContext(t, smallWindowSession())
	records := Array()
	for i := 0; i < 800; i++ {
		records.Append(ObjectOf("id", Number(float64(i)), "flag", Bool(false), "text", String(strings.Repeat("record ", 40))))
	}
	original := ToolResult{StructuredContent: ObjectOf("records", records), order: []string{"isError", "structuredContent"}}
	held := true
	original.IsError = &held
	projected, err := c.Result("save_records", original)
	if err != nil {
		t.Fatal(err)
	}
	if !projected.Err() {
		t.Error("the projected result lost its isError")
	}
	reference := projected.StructuredContent
	if !reference.Get("operationReturned").Truth() || reference.Get("readWith").Str() != ReadTool {
		t.Errorf("the reference is %s", reference.Text())
	}
	read, err := c.Ledger.Read(MustParseJSON(`{"id":` + QuoteJSON(reference.Get("contextId").Str()) + `,"path":"/structuredContent/records/799"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !equalJSON(read.Get("value"), records.Items()[799]) {
		t.Errorf("the last record read back as %s", read.Get("value").Text())
	}
	if records.Len() != 800 {
		t.Error("the original was changed")
	}
}

func TestACompletedOperationIsNotATryableFailureWhenRetentionIsFull(t *testing.T) {
	c := openContext(t, smallWindowSession())
	c.Policy.RetainedBytes = 100
	result, err := c.Result("propose_change", ResultOf([]Content{TextContent(strings.Repeat("x", 13000))}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if result.Err() {
		t.Error("a completed operation was shown as an error")
	}
	held := result.StructuredContent
	if !held.Get("operationReturned").Truth() || !held.Get("contextUnavailable").Truth() || held.Get("isError").Truth() {
		t.Errorf("the result is %s", held.Text())
	}
}

func TestTheLedgerSearchesRetainedStringsLiterally(t *testing.T) {
	c := openContext(t, smallWindowSession())
	text := strings.Repeat("😀", 1000) + "a.*b " + strings.Repeat("😀", 1000) + "a.*b"
	e := retain(t, c, "search", "artifact", "Original", ObjectOf("text", String(text)))
	first := indexOfU16(text, "a.*b", 0)
	last := lastIndexOfU16(text, "a.*b")
	offset := 0
	for _, want := range []int{first, last} {
		answered, err := call(t, tool(t, c, "read_context"),
			`{"id":`+QuoteJSON(e.ID)+`,"path":"/text","query":"a.*b","offset":`+itoa(offset)+`}`)
		if err != nil {
			t.Fatal(err)
		}
		projected, err := c.Result("read_context", answered)
		if err != nil {
			t.Fatal(err)
		}
		if UTF8Bytes(string(projected.JSON())) > c.Policy.ToolResultBytes {
			t.Fatalf("the search is %d bytes", UTF8Bytes(string(projected.JSON())))
		}
		found := projected.StructuredContent
		if int(found.Get("matchOffset").Num()) != want {
			t.Errorf("the match is at %s, not %d", found.Get("matchOffset").Text(), want)
		}
		if !strings.Contains(found.Get("text").Str(), "a.*b") || strings.Contains(found.Get("text").Str(), "�") {
			t.Errorf("the window is %q", found.Get("text").Str())
		}
		if next := found.Get("nextOffset"); next.Kind() == KindNull {
			offset = U16Length(text)
		} else {
			offset = int(next.Num())
		}
	}
	held, err := c.Ledger.Read(MustParseJSON(`{"id":` + QuoteJSON(e.ID) + `,"path":"/text","query":"a.*b","offset":` + itoa(offset) + `}`))
	if err != nil {
		t.Fatal(err)
	}
	if held.Get("found").Truth() || held.Get("nextOffset").Kind() != KindNull {
		t.Errorf("the search past the last match answered %s", held.Text())
	}
	held, err = c.Ledger.Read(MustParseJSON(`{"id":` + QuoteJSON(e.ID) + `,"query":"a.*b"}`))
	if err != nil {
		t.Fatal(err)
	}
	match := held.Get("matches").Items()[0]
	if !held.Get("found").Truth() || match.Get("path").Str() != "/text" || int(match.Get("matchOffset").Num()) != first {
		t.Errorf("the object search answered %s", held.Text())
	}
	_, err = c.Ledger.Read(MustParseJSON(`{"id":` + QuoteJSON(e.ID) + `,"path":"/text","query":""}`))
	refused(t, err, "non-empty")
	if c.Ledger.List(0).Get("items").Len() != 1 {
		t.Error("a read retained something of its own")
	}
}

func TestTheLedgerExposesSchemaTextPathsAndSearchesAToolEnvelope(t *testing.T) {
	c := openContext(t, smallWindowSession())
	schema := ObjectOf("$defs", ObjectOf("condition", ObjectOf("operator", String("less-than-or-equal"),
		"description", String(strings.Repeat("x", 15000))))).Text()
	result, err := c.Result("get_schema", ResultOf([]Content{TextContent(schema)},
		ObjectOf("status", String("ok"), "schemaId", String("fixture"))))
	if err != nil {
		t.Fatal(err)
	}
	reference := result.StructuredContent
	found := false
	for _, section := range reference.Get("sections").Items() {
		if section.Get("path").Str() == "/content/0/text" && section.Get("type").Str() == "string" {
			found = true
		}
	}
	if !found {
		t.Errorf("the sections are %s", reference.Get("sections").Text())
	}
	id := QuoteJSON(reference.Get("contextId").Str())
	held, err := c.Ledger.Read(MustParseJSON(`{"id":` + id + `,"query":"operator"}`))
	if err != nil {
		t.Fatal(err)
	}
	match := held.Get("matches").Items()[0]
	if !held.Get("found").Truth() || match.Get("path").Str() != "/content/0/text" || !strings.Contains(match.Get("text").Str(), "operator") {
		t.Errorf("the envelope search answered %s", held.Text())
	}
	_, err = c.Ledger.Read(MustParseJSON(`{"id":` + id + `,"path":"/$defs"}`))
	refused(t, err, "/content/0/text")
	held, err = c.Ledger.Read(MustParseJSON(`{"id":` + id + `,"path":"/content/0/text","query":"operator"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !held.Get("found").Truth() {
		t.Errorf("the text search answered %s", held.Text())
	}
}

func TestListContextPagesTheEntriesAndRefusesABadOffset(t *testing.T) {
	c := openContext(t, modelSession())
	for at := 0; at < 25; at++ {
		retain(t, c, "item-"+itoa(at), "tool", "Item "+itoa(at), String("x"))
	}
	answered, err := call(t, tool(t, c, "list_context"), `{}`)
	if err != nil {
		t.Fatal(err)
	}
	held := answered.StructuredContent
	if held.Get("items").Len() != 20 || int(held.Get("nextOffset").Num()) != 20 {
		t.Errorf("the first page is %s", held.Get("nextOffset").Text())
	}
	answered, err = call(t, tool(t, c, "list_context"), `{"offset":20}`)
	if err != nil {
		t.Fatal(err)
	}
	if answered.StructuredContent.Get("items").Len() != 5 || answered.StructuredContent.Get("nextOffset").Kind() != KindNull {
		t.Errorf("the second page is %s", answered.StructuredContent.Text())
	}
	_, err = call(t, tool(t, c, "list_context"), `{"offset":-1}`)
	refused(t, err, "Invalid offset")
}

func TestTheDigestIsTheSameOneTheLedgerComputes(t *testing.T) {
	// chat.test.ts: the ledger's digest of the empty text.
	if held := ContextDigest(""); held != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Errorf("the digest is %s", held)
	}
}
