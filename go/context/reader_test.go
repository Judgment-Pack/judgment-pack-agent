package runcontext

// One read, one result (the port of packages/harness/context/reader.test.ts).
//
// A reading tool that holds its window to readWindowBytes is answered whole: its result never
// becomes a ledger reference and a preview the model has to page back. A result that still
// exceeds the limit is stubbed as before, its reference naming the reader to page it back with.
// And the ledger's own reader, read_window, pages a retained item by line or page with a cursor,
// each window one result.

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// page is a page of text that costs more to encode than it is long: prose, escapes, emoji, a
// control character (PAGE in reader.test.ts).
func page() string {
	lines := []string{}
	for i := 0; i < 900; i++ {
		control := ""
		if i%7 == 0 {
			control = "\u0001"
		}
		lines = append(lines, "Line "+strconv.Itoa(i)+`: a claim of "50 USD" or less is approved \ unless 😀 é`+"\t"+control)
	}
	return strings.Join(lines, "\n")
}

// pageReader is a page reader shaped like the Desk's link reader: a short header, then one window
// of the page's text, in a text result beside a small structured member.
func pageReader(text string, window func(string) string) func(offset int) ToolResult {
	return func(offset int) ToolResult {
		held := window(U16Slice(text, offset, U16Length(text)))
		next := -1
		if offset+U16Length(held) < U16Length(text) {
			next = offset + U16Length(held)
		}
		header := "retrieved: https://example.org/policy\ntitle: Expense policy\n" +
			strconv.Itoa(U16Length(text)) + " characters; showing " + strconv.Itoa(offset) + "–" + strconv.Itoa(offset+U16Length(held))
		if next >= 0 {
			header += " (more: call read_page with offset " + strconv.Itoa(next) + ")"
		}
		structured := ObjectOf("pages", Array(Number(1)))
		if next >= 0 {
			structured.Set("nextOffset", Number(float64(next)))
		} else {
			structured.Set("nextOffset", Null())
		}
		return ResultOf([]Content{TextContent(header + "\n\n" + held)}, structured)
	}
}

func TestAReadingToolThatHoldsItsWindowIsAnsweredWhole(t *testing.T) {
	c := openContext(t, modelSession())
	text := page()
	reader := pageReader(text, func(held string) string { return EncodedPrefix(held, ReadWindowBytes(c.Policy.ToolResultBytes)) })
	offset, read, reads := 0, "", 0
	for {
		answered := reader(offset)
		shown, err := c.Result("read_page", answered)
		if err != nil {
			t.Fatal(err)
		}
		reads++
		// The model is shown what the tool answered, not a reference to it.
		if shown.Value().Text() != answered.Value().Text() {
			t.Fatalf("read %d was projected", reads)
		}
		if strings.Contains(shown.Value().Text(), "contextId") {
			t.Fatal("the model was shown a reference")
		}
		if UTF8Bytes(string(shown.JSON())) > c.Policy.ToolResultBytes {
			t.Fatalf("read %d is %d bytes", reads, UTF8Bytes(string(shown.JSON())))
		}
		body := shown.Content[0].Text()
		read += body[strings.Index(body, "\n\n")+2:]
		next := shown.StructuredContent.Get("nextOffset")
		if next.Kind() == KindNull {
			break
		}
		offset = int(next.Num())
	}
	if read != text {
		t.Error("the page did not come back whole")
	}
	// Every read was a page of the text, not a preview: nothing was retained to page back.
	if held := c.Ledger.List(0).Get("items").Len(); held != 0 {
		t.Errorf("%d items were retained", held)
	}
	if most := UTF8Bytes(QuoteJSON(text))/(ReadWindowBytes(c.Policy.ToolResultBytes)-2) + 2; reads > most {
		t.Errorf("%d reads, more than the %d the window allows", reads, most)
	}
}

func TestAResultOverTheLimitIsStillStubbed(t *testing.T) {
	c := openContext(t, modelSession())
	text := page()
	// The Desk's link reader before its swap: 20,000 characters a window.
	reader := pageReader(text, func(held string) string { return U16Slice(held, 0, 20000) })
	shown, err := c.Result("read_page", reader(0))
	if err != nil {
		t.Fatal(err)
	}
	reference := shown.StructuredContent
	if !reference.Get("operationReturned").Truth() || reference.Get("readWith").Str() != ReadTool {
		t.Errorf("the reference is %s", reference.Text())
	}
	if ReadTool != "read_window" {
		t.Error("the reader's name changed")
	}
	if U16Length(reference.Get("preview").Str()) > 1000 {
		t.Error("the preview is longer than a thousand characters")
	}
	if UTF8Bytes(string(shown.JSON())) > c.Policy.ToolResultBytes {
		t.Errorf("the stub is %d bytes", UTF8Bytes(string(shown.JSON())))
	}
	held, err := c.Ledger.Read(MustParseJSON(`{"id":` + QuoteJSON(reference.Get("contextId").Str()) + `,"path":"/content/0/text","limit":128}`))
	if err != nil {
		t.Fatal(err)
	}
	if held.Get("type").Str() != "text" {
		t.Errorf("the retained original read back as %s", held.Text())
	}
}

// readWindow is one read_window call, as the model is shown it.
func readWindow(t *testing.T, c *SessionContext, args string) ReadResult {
	t.Helper()
	answered, err := call(t, tool(t, c, ReadTool), args)
	if err != nil {
		t.Fatal(err)
	}
	shown, err := c.Result(ReadTool, answered)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(shown.Value().Text(), "contextId") {
		t.Fatal("a read was projected into a reference")
	}
	if UTF8Bytes(string(shown.JSON())) > c.Policy.ToolResultBytes {
		t.Fatalf("the read is %d bytes", UTF8Bytes(string(shown.JSON())))
	}
	held := shown.StructuredContent
	out := ReadResult{ID: held.Get("id").Str(), Unit: held.Get("unit").Str(),
		Offset: int(held.Get("offset").Num()), Count: int(held.Get("count").Num()),
		Total: int(held.Get("total").Num()), Text: held.Get("text").Str()}
	if column := held.Get("column"); column != nil {
		out.Column = int(column.Num())
	}
	if more := held.Get("more"); more.IsString() {
		out.More = more.Str()
	}
	return out
}

func TestReadWindowPagesAnItemByLineWithACursor(t *testing.T) {
	c := openContext(t, modelSession())
	// A line far longer than the window sits in the middle: it is read in part, across reads.
	long := strings.Repeat(`x"`, 9000)
	text := page() + "\n" + long + "\n" + page()
	entry := retain(t, c, "page", "tool", "A long page", String(text))
	lines := strings.Split(text, "\n")
	numbered := regexp.MustCompile(`^\d+\t`)
	rebuilt, windows, args := "", 0, `{"id":`+QuoteJSON(entry.ID)+`}`
	for {
		window := readWindow(t, c, args)
		windows++
		if window.Unit != "line" || window.ID != entry.ID || window.Total != len(lines) {
			t.Fatalf("window %d is %+v", windows, window)
		}
		first := strings.SplitN(window.Text, "\n", 2)[0]
		if !strings.HasPrefix(first, strconv.Itoa(window.Offset+1)+"\t") {
			t.Fatalf("window %d opens with %q", windows, first)
		}
		// A window that continues a line part way through carries no separator before it. Each
		// line shown starts with its number and a tab, which the rebuild takes off.
		if windows > 1 && window.Column == 0 {
			rebuilt += "\n"
		}
		shown := []string{}
		for _, line := range strings.Split(window.Text, "\n") {
			shown = append(shown, numbered.ReplaceAllString(line, ""))
		}
		rebuilt += strings.Join(shown, "\n")
		if window.More == "" {
			break
		}
		if !regexp.MustCompile(`^line:\d+(\+\d+)?$`).MatchString(window.More) {
			t.Fatalf("the cursor is %q", window.More)
		}
		args = `{"id":` + QuoteJSON(entry.ID) + `,"cursor":` + QuoteJSON(window.More) + `}`
	}
	if rebuilt != text {
		t.Error("the item did not come back exactly")
	}
	if windows <= 3 {
		t.Errorf("%d windows", windows)
	}
	// The read results were answered whole: nothing new was retained to page back through.
	if held := c.Ledger.List(0).Get("items").Len(); held != 1 {
		t.Errorf("%d items are retained", held)
	}
}

func TestReadWindowReadsALineRangeAndSaysWhereToReadOn(t *testing.T) {
	c := openContext(t, modelSession())
	entry := retain(t, c, "lines", "request", "Lines", String("zero\none\ntwo\nthree\nfour\nfive"))
	held := readWindow(t, c, `{"id":`+QuoteJSON(entry.ID)+`,"offset":2,"limit":2}`)
	want := ReadResult{ID: entry.ID, Unit: "line", Offset: 2, Count: 2, Total: 6, Text: "3\ttwo\n4\tthree", More: "line:4"}
	if held != want {
		t.Errorf("the read is %+v", held)
	}
	held = readWindow(t, c, `{"id":`+QuoteJSON(entry.ID)+`,"cursor":"line:4"}`)
	if held.Offset != 4 || held.Count != 2 || held.Text != "5\tfour\n6\tfive" || held.More != "" {
		t.Errorf("the next read is %+v", held)
	}
}

func TestReadWindowReadsAPagedDocumentByPage(t *testing.T) {
	c := openContext(t, modelSession())
	entry := retain(t, c, "document", "artifact", "An attachment",
		MustParseJSON(`{"pages":["Page one.","Page two.\nSecond line.","Page three."]}`))
	held := readWindow(t, c, `{"id":`+QuoteJSON(entry.ID)+`,"unit":"page","limit":1}`)
	if held.Offset != 0 || held.Count != 1 || held.Total != 3 || held.Text != "Page one." || held.More != "page:1" {
		t.Errorf("the page read is %+v", held)
	}
	held = readWindow(t, c, `{"id":`+QuoteJSON(entry.ID)+`,"cursor":"page:1"}`)
	if held.Count != 2 || held.Text != "Page two.\nSecond line.\fPage three." || held.More != "" {
		t.Errorf("the next pages are %+v", held)
	}
	held = readWindow(t, c, `{"id":`+QuoteJSON(entry.ID)+`,"offset":1,"limit":2}`)
	if held.Unit != "line" || held.Text != "2\tPage two.\n3\tSecond line." {
		t.Errorf("the same document by line is %+v", held)
	}
}

func TestReadWindowRefusesWhatItCannotRead(t *testing.T) {
	c := openContext(t, modelSession())
	entry := retain(t, c, "text", "request", "Text", String("one\ntwo"))
	id := QuoteJSON(entry.ID)
	for _, held := range []struct{ args, want string }{
		{`{"id":` + id + `,"unit":"page"}`, "no pages; read it by line"},
		{`{"id":` + id + `,"cursor":"line:1","offset":1}`, "without unit or offset"},
		{`{"id":` + id + `,"cursor":"line:one"}`, "cursor a previous read returned"},
		{`{"id":` + id + `,"offset":9}`, "holds 2 lines"},
		{`{"id":` + id + `,"limit":0}`, "limit from 1"},
		{`{"id":"elsewhere"}`, "unavailable"},
	} {
		_, err := call(t, tool(t, c, ReadTool), held.args)
		refused(t, err, held.want)
	}
}

func TestReadWindowIsDeclaredAsTheContractStates(t *testing.T) {
	c := openContext(t, modelSession())
	held := tool(t, c, ReadTool)
	var item *Value
	for _, one := range vectorItems(t, "tool-offer/host-tools.json") {
		if one.Get("name").Str() == ReadTool {
			item = one
		}
	}
	if item == nil {
		t.Fatal("the vector does not declare read_window")
	}
	declared := ObjectOf("name", String(held.Name), "effect", String(held.Effect),
		"contextPolicy", ObjectOf("immutable", Bool(true)), "description", String(held.Description),
		"inputSchema", MustParseJSON(string(held.InputSchema)))
	if CanonicalJSON(declared) != CanonicalJSON(item) {
		t.Errorf("the declaration is\n%s\nand the vector is\n%s", CanonicalJSON(declared), CanonicalJSON(item))
	}
}

func TestEveryReaderTakesTheArgumentsTheContractStates(t *testing.T) {
	schema := readJSON(t, "contract/schemas/tool-result.json").Get("$defs").Get("readArguments")
	if !strings.Contains(schema.Get("description").Str(), "exactly the input schema") {
		t.Error("the schema's description does not say it is the reader's own")
	}
	held := without(schema, "description")
	if CanonicalJSON(held) != CanonicalJSON(MustParseJSON(string(ReadInputSchema()))) {
		t.Errorf("the schema is\n%s\nand the reader takes\n%s", CanonicalJSON(held), CanonicalJSON(MustParseJSON(string(ReadInputSchema()))))
	}
}

func TestReadWindowHoldsItsWindowToThePolicyItWasGiven(t *testing.T) {
	c := openContext(t, modelSession())
	c.Policy.ToolResultBytes = 3000
	entry := retain(t, c, "page", "tool", "Page", String(page()))
	window := readWindow(t, c, `{"id":`+QuoteJSON(entry.ID)+`}`)
	if UTF8Bytes(QuoteJSON(window.Text)) > ReadWindowBytes(3000) {
		t.Errorf("the window is %d bytes", UTF8Bytes(QuoteJSON(window.Text)))
	}
	if window.More == "" {
		t.Error("the window read the whole item")
	}
}

func TestReadByLineShowsTwoHundredNumberedLines(t *testing.T) {
	// The default window of about two hundred numbered lines (search.test.ts, "read, by line").
	c := openContext(t, modelSession())
	lines := []string{}
	for i := 0; i < 500; i++ {
		lines = append(lines, "entry "+strconv.Itoa(i))
	}
	entry := retain(t, c, "log", "tool", "A log", String(strings.Join(lines, "\n")))
	if ReadWindowLines != 200 {
		t.Error("the default window is not two hundred lines")
	}
	first := readWindow(t, c, `{"id":`+QuoteJSON(entry.ID)+`}`)
	if first.Offset != 0 || first.Count != 200 || first.Total != 500 || first.More != "line:200" {
		t.Errorf("the first window is %+v", first)
	}
	shown := strings.Split(first.Text, "\n")
	for at, line := range shown {
		if line != strconv.Itoa(at+1)+"\t"+lines[at] {
			t.Fatalf("line %d is %q", at, line)
		}
	}
	second := readWindow(t, c, `{"id":`+QuoteJSON(entry.ID)+`,"cursor":`+QuoteJSON(first.More)+`}`)
	if second.Offset != 200 || second.Count != 200 || second.More != "line:400" {
		t.Errorf("the second window is %+v", second)
	}
	if strings.SplitN(second.Text, "\n", 2)[0] != "201\tentry 200" {
		t.Errorf("the second window opens with %q", strings.SplitN(second.Text, "\n", 2)[0])
	}
	third := readWindow(t, c, `{"id":`+QuoteJSON(entry.ID)+`,"cursor":`+QuoteJSON(second.More)+`}`)
	if third.Offset != 400 || third.Count != 100 || third.More != "" {
		t.Errorf("the third window is %+v", third)
	}
	// A limit the model names is the window.
	held := readWindow(t, c, `{"id":`+QuoteJSON(entry.ID)+`,"limit":3}`)
	if held.Count != 3 || held.Text != "1\tentry 0\n2\tentry 1\n3\tentry 2" || held.More != "line:3" {
		t.Errorf("a limited read is %+v", held)
	}
}

func TestTheAttachmentVectorIsReadAsTheReaderServesIt(t *testing.T) {
	// tool-result/attachments.json: the documents a host hands a run to retain — a text, read by
	// line; a processed document, by page or line; and an image, read whole.
	c := openContext(t, modelSession())
	items := vectorItems(t, "tool-result/attachments.json")
	documents := []Document{}
	for _, item := range items {
		documents = append(documents, Document{ID: item.Get("id").Str(), Kind: item.Get("kind").Str(),
			Label: item.Get("label").Str(), Value: item.Get("value")})
	}
	if err := c.Initialize(documents, true); err != nil {
		t.Fatal(err)
	}
	entries := c.Ledger.List(0).Get("items").Items()
	if len(entries) != 3 {
		t.Fatalf("%d attachments were retained", len(entries))
	}
	// The text: read by line, numbered from 1.
	text := readWindow(t, c, `{"id":`+QuoteJSON(entries[0].Get("contextId").Str())+`}`)
	if text.Unit != "line" || text.Total != 3 || !strings.HasPrefix(text.Text, "1\tExpense policy\n") {
		t.Errorf("the policy read back as %+v", text)
	}
	// The processed document: by page, and by line.
	paged := readWindow(t, c, `{"id":`+QuoteJSON(entries[1].Get("contextId").Str())+`,"unit":"page"}`)
	if paged.Total != 3 || paged.Text != "Receipt\fHotel: 180 USD\fPaid by card" {
		t.Errorf("the receipt read back as %+v", paged)
	}
	byLine := readWindow(t, c, `{"id":`+QuoteJSON(entries[1].Get("contextId").Str())+`}`)
	if byLine.Unit != "line" || byLine.Text != "1\tReceipt\n2\tHotel: 180 USD\n3\tPaid by card" {
		t.Errorf("the receipt by line is %+v", byLine)
	}
	// The image: read whole, as the image.
	answered, err := call(t, tool(t, c, ReadTool), `{"id":`+QuoteJSON(entries[2].Get("contextId").Str())+`}`)
	if err != nil {
		t.Fatal(err)
	}
	if answered.StructuredContent.Get("type").Str() != "image" ||
		answered.StructuredContent.Get("mediaType").Str() != "image/png" ||
		len(answered.Content) != 2 {
		t.Errorf("the image read back as %s", answered.Value().Text())
	}
	// And a search over the attachments passes over the image and finds the text.
	found := searchShown(t, c, `{"pattern":"USD","scope":"attachments"}`)
	if found.Get("matches").Len() == 0 {
		t.Errorf("the attachments' search found %s", found.Text())
	}
	if found.Get("notShown") != nil {
		t.Errorf("an image it can show was named: %s", found.Get("notShown").Text())
	}
}
