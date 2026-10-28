package runcontext

// search (the port of packages/harness/context/search.test.ts): a regular expression over what
// the execution retains, each match located and shown with its numbered lines, held to one
// result, with a cursor.

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

const policyText = "Expense policy\nA claim of 50 USD or less is approved.\nA claim over 50 USD needs a receipt.\nTravel is reimbursed at cost.\nMeals are capped at 30 USD a day."
const claimsText = "Claims this month\nclaim 1: 42 USD, lunch\nclaim 2: 180 USD, hotel\nclaim 3: 12 EUR, taxi"

// searchShown is one search, as the model is shown it: one result, within the limit, never a
// reference.
func searchShown(t *testing.T, c *SessionContext, args string) *Value {
	t.Helper()
	answered, err := call(t, tool(t, c, SearchTool), args)
	if err != nil {
		t.Fatal(err)
	}
	shown, err := c.Result(SearchTool, answered)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(shown.Value().Text(), "contextId") {
		t.Fatal("a search was projected into a reference")
	}
	if UTF8Bytes(string(shown.JSON())) > c.Policy.ToolResultBytes {
		t.Fatalf("the search is %d bytes", UTF8Bytes(string(shown.JSON())))
	}
	return shown.StructuredContent
}

func searchRefused(t *testing.T, c *SessionContext, args string) error {
	t.Helper()
	_, err := call(t, tool(t, c, SearchTool), args)
	return err
}

// at is one member of a match, by name.
func lines(found *Value, member string) []int {
	out := []int{}
	for _, match := range found.Get("matches").Items() {
		out = append(out, int(match.Get(member).Num()))
	}
	return out
}

func TestSearchFindsAPatternAcrossTwoItems(t *testing.T) {
	c := openContext(t, modelSession())
	policy := retain(t, c, "policy", "artifact", "The policy", String(policyText))
	claims := retain(t, c, "claims", "artifact", "The claims", String(claimsText))
	found := searchShown(t, c, `{"pattern":"\\b\\d+ USD\\b"}`)
	want := []struct {
		id                   string
		line, offset, column int
	}{
		{policy.ID, 2, 1, 11}, {policy.ID, 3, 2, 13}, {policy.ID, 5, 4, 20},
		{claims.ID, 2, 1, 9}, {claims.ID, 3, 2, 9},
	}
	matches := found.Get("matches").Items()
	if len(matches) != len(want) {
		t.Fatalf("the search found %s", found.Text())
	}
	for at, held := range want {
		match := matches[at]
		if match.Get("id").Str() != held.id || int(match.Get("line").Num()) != held.line ||
			int(match.Get("offset").Num()) != held.offset || int(match.Get("column").Num()) != held.column {
			t.Errorf("match %d is %s", at, match.Text())
		}
	}
	// Two lines either side, numbered as read numbers them, and the item's label.
	if matches[0].Get("label").Str() != "The policy" ||
		matches[0].Get("text").Str() != "1\tExpense policy\n2\tA claim of 50 USD or less is approved.\n3\tA claim over 50 USD needs a receipt.\n4\tTravel is reimbursed at cost." {
		t.Errorf("the first match shows %s", matches[0].Text())
	}
	if matches[4].Get("text").Str() != "1\tClaims this month\n2\tclaim 1: 42 USD, lunch\n3\tclaim 2: 180 USD, hotel\n4\tclaim 3: 12 EUR, taxi" {
		t.Errorf("the last match shows %q", matches[4].Get("text").Str())
	}
	if found.Get("more").Kind() != KindNull {
		t.Error("the search left a cursor")
	}
	// The line a match names is the line read shows under that number.
	read := readWindow(t, c, `{"id":`+QuoteJSON(claims.ID)+`,"offset":2,"limit":1}`)
	if read.Text != "3\tclaim 2: 180 USD, hotel" {
		t.Errorf("read shows %q", read.Text)
	}
}

func TestSearchScopesOneItemOrTheAttachments(t *testing.T) {
	c := openContext(t, modelSession())
	// The host's documents for the run: what the boundary retains as attachments.
	if err := c.Initialize([]Document{{ID: "claims", Kind: "artifact", Label: "The claims", Value: String(claimsText)}}, true); err != nil {
		t.Fatal(err)
	}
	policy := retain(t, c, "policy", "artifact", "The policy", String(policyText))
	attached := searchShown(t, c, `{"pattern":"claim","scope":"attachments"}`)
	for _, match := range attached.Get("matches").Items() {
		if match.Get("label").Str() != "The claims" {
			t.Errorf("the attachments' search found %s", match.Text())
		}
	}
	if attached.Get("scope").Str() != "attachments" {
		t.Errorf("the scope is %s", attached.Get("scope").Text())
	}
	one := searchShown(t, c, `{"pattern":"claim","id":`+QuoteJSON(policy.ID)+`}`)
	if held := lines(one, "line"); len(held) != 2 || held[0] != 2 || held[1] != 3 {
		t.Errorf("one item's search found %v", held)
	}
	if !equalJSON(one.Get("scope"), ObjectOf("id", String(policy.ID))) {
		t.Errorf("the scope is %s", one.Get("scope").Text())
	}
	caseless := searchShown(t, c, `{"pattern":"CLAIM \\d","ignoreCase":true}`)
	if held := lines(caseless, "line"); len(held) != 3 || held[0] != 2 || held[2] != 4 {
		t.Errorf("the case-insensitive search found %v", held)
	}
}

func TestSearchNamesThePageAMatchIsOn(t *testing.T) {
	c := openContext(t, modelSession())
	retain(t, c, "document", "artifact", "An attachment",
		MustParseJSON(`{"pages":["Page one.\nNo match.","Page two: the receipt.","Page three.\nAnother receipt."]}`))
	found := searchShown(t, c, `{"pattern":"receipt"}`)
	if held, pages := lines(found, "line"), lines(found, "page"); len(held) != 2 ||
		held[0] != 3 || pages[0] != 2 || held[1] != 5 || pages[1] != 3 {
		t.Errorf("the search found %s", found.Text())
	}
}

func TestSearchStopsAtItsLimitAndGoesOnFromItsCursor(t *testing.T) {
	c := openContext(t, modelSession())
	rows := []string{}
	for i := 0; i < 300; i++ {
		held := "none"
		if i%3 == 0 {
			held = "MATCH"
		}
		rows = append(rows, "row "+strconv.Itoa(i)+": "+held+" "+strings.Repeat("pad ", 30))
	}
	retain(t, c, "rows", "tool", "Rows", String(strings.Join(rows, "\n")))
	first := searchShown(t, c, `{"pattern":"MATCH","limit":2}`)
	if held := lines(first, "line"); len(held) != 2 || held[0] != 1 || held[1] != 4 {
		t.Errorf("the first search found %v", held)
	}
	cursor := first.Get("more").Str()
	if !strings.HasPrefix(cursor, "0:6:") {
		t.Errorf("the cursor is %q", cursor)
	}
	second := searchShown(t, c, `{"pattern":"MATCH","limit":2,"cursor":`+QuoteJSON(cursor)+`}`)
	if held := lines(second, "line"); len(held) != 2 || held[0] != 7 || held[1] != 10 {
		t.Errorf("the second search found %v", held)
	}
	// Without a limit it stops where one more match would not fit in one result.
	all, searches, next := []int{}, 0, ""
	for {
		args := `{"pattern":"MATCH","limit":100}`
		if next != "" {
			args = `{"pattern":"MATCH","limit":100,"cursor":` + QuoteJSON(next) + `}`
		}
		found := searchShown(t, c, args)
		all = append(all, lines(found, "line")...)
		searches++
		if found.Get("more").Kind() == KindNull {
			break
		}
		next = found.Get("more").Str()
	}
	want := []int{}
	for i := range rows {
		if i%3 == 0 {
			want = append(want, i+1)
		}
	}
	if len(all) != len(want) {
		t.Fatalf("%d matches over %d searches", len(all), searches)
	}
	for at := range want {
		if all[at] != want[at] {
			t.Fatalf("match %d is line %d", at, all[at])
		}
	}
	if searches <= 1 {
		t.Error("one result held every match")
	}
	// A cursor belongs to its own search.
	refused(t, searchRefused(t, c, `{"pattern":"none","cursor":`+QuoteJSON(cursor)+`}`), "belongs to another search")
}

func TestSearchRefusesWhatRE2Refuses(t *testing.T) {
	c := openContext(t, modelSession())
	retain(t, c, "policy", "artifact", "The policy", String(policyText))
	for _, held := range []struct{ args, want string }{
		{`{"pattern":"(USD) \\1"}`, "backreferences are not supported"},
		{`{"pattern":"(?<cur>USD) \\k<cur>"}`, "backreferences are not supported"},
		{`{"pattern":"claim(?= of)"}`, "lookahead and lookbehind are not supported"},
		{`{"pattern":"(?<!over )50"}`, "lookahead and lookbehind are not supported"},
		{`{"pattern":"\\p{L}+"}`, "Unicode property classes"},
		{`{"pattern":"(?i)claim"}`, "inline flags"},
		{`{"pattern":` + QuoteJSON(strings.Repeat("a", PatternLength+1)) + `}`, "at most 256 characters"},
		{`{"pattern":"a{1001}"}`, "at most 1000"},
		{`{"pattern":"(abc){999}"}`, "steps to compile"},
		{`{"pattern":"[x]a{0,998}b{0,10}"}`, "instructions"},
		{`{"pattern":"claim","id":"x","scope":"all"}`, "not both"},
		{`{"pattern":"claim","regex":true}`, `does not take "regex"`},
		{`{"pattern":"claim","limit":101}`, "limit from 1 to 100"},
	} {
		refused(t, searchRefused(t, c, held.args), held.want)
	}
	// At the bound itself, a pattern is taken.
	if err := searchRefused(t, c, `{"pattern":`+QuoteJSON(strings.Repeat("a", PatternLength))+`}`); err != nil {
		t.Errorf("a pattern at the bound was refused: %v", err)
	}
}

func TestSearchRefusesNestedRepetitionsOfAnEmptyGroupAtOnce(t *testing.T) {
	c := openContext(t, modelSession())
	retain(t, c, "policy", "artifact", "The policy", String(policyText))
	for _, pattern := range []string{"((((){1000}){1000}){1000}){1000}", "(((){1000}){1000}){1000}", "(?:(?:(?:){1000}){1000}){1000}"} {
		started := time.Now()
		refused(t, searchRefused(t, c, `{"pattern":`+QuoteJSON(pattern)+`}`), "steps to compile")
		if elapsed := time.Since(started); elapsed > 50*time.Millisecond {
			t.Errorf("%s took %s", pattern, elapsed)
		}
	}
	// A repetition of nothing collapses to nothing: it compiles to no instruction.
	empty, err := CompilePattern("(?:){1000}claim", false)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := CompilePattern("claim", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(empty.ops) != len(plain.ops) {
		t.Errorf("the repetition of nothing compiled to %d instructions", len(empty.ops)-len(plain.ops))
	}
	counted, err := CompilePattern("a{1000}", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(counted.ops) != 1001 {
		t.Errorf("a{1000} compiled to %d instructions", len(counted.ops))
	}
}

func TestSearchChargesEachLineItsSetup(t *testing.T) {
	c := openContext(t, modelSession())
	entry := retain(t, c, "empty", "tool", "Three million empty lines", String(strings.Repeat("\n", 2_999_999)))
	// 1,998 instructions, and no literal a line must contain.
	started := time.Now()
	stopped := searchShown(t, c, `{"pattern":"[x]a{0,998}","id":`+QuoteJSON(entry.ID)+`}`)
	elapsed := time.Since(started)
	if !strings.Contains(stopped.Get("note").Str(), "work bound") {
		t.Errorf("the search answered %s", stopped.Text())
	}
	// Charged the program's size per line, a search reads a few thousand of them, not millions.
	at, err := strconv.Atoi(strings.Split(stopped.Get("more").Str(), ":")[1])
	if err != nil || at >= 10_000 {
		t.Errorf("the search read to line %d", at)
	}
	if elapsed > 3*time.Second {
		t.Errorf("the search took %s", elapsed)
	}
}

func TestSearchPassesOverAnImageAndStopsAtItsWorkBound(t *testing.T) {
	c := openContext(t, modelSession())
	retain(t, c, "scan", "artifact", "A scan", MustParseJSON(`{"kind":"image","mediaType":"image/png","bytes":"QUJD"}`))
	long := []string{}
	for i := 0; i < 40; i++ {
		long = append(long, strings.Repeat("a", 20_000))
	}
	retain(t, c, "long", "tool", "Long lines", String(strings.Join(long, "\n")))
	started := time.Now()
	// A class rather than a literal at the end, so no line is passed over unread.
	stopped := searchShown(t, c, `{"pattern":"[a-z]*[a-z]*[a-z]*[q!]"}`)
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Errorf("the search took %s", elapsed)
	}
	if stopped.Get("matches").Len() != 0 || !strings.Contains(stopped.Get("note").Str(), "work bound") {
		t.Errorf("the search answered %s", stopped.Text())
	}
	// The image is not among the items a search reads: the long item is the first.
	if !strings.HasPrefix(stopped.Get("more").Str(), "0:") {
		t.Errorf("the cursor is %q", stopped.Get("more").Str())
	}
	next := searchShown(t, c, `{"pattern":"[a-z]*[a-z]*[a-z]*[q!]","cursor":`+QuoteJSON(stopped.Get("more").Str())+`}`)
	first, _ := strconv.Atoi(strings.Split(stopped.Get("more").Str(), ":")[1])
	second := 40
	if next.Get("more").IsString() {
		second, _ = strconv.Atoi(strings.Split(next.Get("more").Str(), ":")[1])
	}
	if second <= first {
		t.Errorf("the search went on from %d to %d", first, second)
	}
}

func TestSearchRefusesAPatternThatCannotReadOneLine(t *testing.T) {
	c := openContext(t, modelSession())
	entry := retain(t, c, "one", "tool", "One line", String(strings.Repeat("a", 1_000_000)))
	refused(t, searchRefused(t, c, `{"pattern":"[a-z]*[a-z]*[a-z]*[a-z]*[q!]","id":`+QuoteJSON(entry.ID)+`}`),
		"more work than one search does on line 1")
}

func TestSearchTakesTheArgumentsTheContractStates(t *testing.T) {
	schema := readJSON(t, "contract/schemas/tool-result.json").Get("$defs").Get("searchArguments")
	description := schema.Get("description").Str()
	for _, want := range []string{"exactly the input schema", "just before a line’s final carriage return"} {
		if !strings.Contains(description, want) {
			t.Errorf("the schema's description does not carry %q", want)
		}
	}
	if !strings.Contains(SearchDescription, "just before a line’s final carriage return") {
		t.Error("the tool's description does not carry the reading of $")
	}
	held := without(schema, "description")
	if CanonicalJSON(held) != CanonicalJSON(MustParseJSON(string(SearchInputSchema()))) {
		t.Errorf("the schema is\n%s\nand the search takes\n%s", CanonicalJSON(held), CanonicalJSON(MustParseJSON(string(SearchInputSchema()))))
	}
}

func TestSearchAnswersTheContractVectors(t *testing.T) {
	c := openContext(t, modelSession())
	if err := c.Ledger.SetScope("7b3e1c52-0d6f-4a8e-9c21-5f4d2a6b8e90"); err != nil {
		t.Fatal(err)
	}
	attachments := vectorItems(t, "tool-result/attachments.json")
	// The vector's processed receipt, retained as the run's attachment.
	if err := c.Initialize([]Document{{ID: "receipt-scan", Kind: "artifact", Label: "A scanned receipt",
		Value: attachments[1].Get("value")}}, true); err != nil {
		t.Fatal(err)
	}
	retain(t, c, "policy", "artifact", "The policy", String(policyText))
	items := vectorItems(t, "tool-result/search.json")
	answered, err := call(t, tool(t, c, SearchTool), `{"pattern":"\\b\\d+ USD\\b","limit":3}`)
	if err != nil {
		t.Fatal(err)
	}
	if !equalJSON(answered.StructuredContent, items[0]) {
		t.Errorf("the first search is\n%s\nand the vector is\n%s", answered.StructuredContent.Text(), items[0].Text())
	}
	answered, err = call(t, tool(t, c, SearchTool), `{"pattern":"usd","ignoreCase":true,"scope":"attachments"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !equalJSON(answered.StructuredContent, items[1]) {
		t.Errorf("the second search is\n%s\nand the vector is\n%s", answered.StructuredContent.Text(), items[1].Text())
	}
	// The image the vector's third attachment is, retained under the same id, and a GIF beside it.
	retain(t, c, "receipt-scan", "artifact", "A scanned receipt", attachments[2].Get("value"))
	retain(t, c, "receipt-gif", "artifact", "A receipt sent as a GIF",
		MustParseJSON(`{"kind":"image","mediaType":"image/gif","bytes":"R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7"}`))
	answered, err = call(t, tool(t, c, SearchTool), `{"pattern":"Hotel"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !equalJSON(answered.StructuredContent, items[2]) {
		t.Errorf("the third search is\n%s\nand the vector is\n%s", answered.StructuredContent.Text(), items[2].Text())
	}
}

func TestSearchIsDeclaredAsTheContractStates(t *testing.T) {
	c := openContext(t, modelSession())
	held := tool(t, c, SearchTool)
	var item *Value
	for _, one := range vectorItems(t, "tool-offer/host-tools.json") {
		if one.Get("name").Str() == SearchTool {
			item = one
		}
	}
	if item == nil {
		t.Fatal("the vector does not declare search")
	}
	declared := ObjectOf("name", String(held.Name), "effect", String(held.Effect),
		"taxonomy", ObjectOf("operation", String(held.Taxonomy.Operation), "effect", String(held.Taxonomy.Effect)), "description", String(held.Description), "inputSchema", MustParseJSON(string(held.InputSchema)))
	if CanonicalJSON(declared) != CanonicalJSON(item) {
		t.Errorf("the declaration is\n%s\nand the vector is\n%s", CanonicalJSON(declared), CanonicalJSON(item))
	}
}

func TestReadWindowReadsAnImageItemWhole(t *testing.T) {
	c := openContext(t, modelSession())
	entry := retain(t, c, "scan", "artifact", "A scan", MustParseJSON(`{"kind":"image","mediaType":"image/png","bytes":"QUJD"}`))
	answered, err := call(t, tool(t, c, ReadTool), `{"id":`+QuoteJSON(entry.ID)+`}`)
	if err != nil {
		t.Fatal(err)
	}
	want := ObjectOf("type", String("image"), "id", String(entry.ID), "label", String("A scan"),
		"mediaType", String("image/png"), "bytes", Number(3))
	if !equalJSON(answered.StructuredContent, want) {
		t.Errorf("the read answered %s", answered.StructuredContent.Text())
	}
	if !equalJSON(answered.Content[1].Value(), MustParseJSON(`{"kind":"image","mediaType":"image/png","bytes":"QUJD"}`)) {
		t.Errorf("the image part is %s", answered.Content[1].Value().Text())
	}
	// By the id the host gave it, a result may name it.
	if held := c.Ledger.Image("scan"); held == nil || held.Bytes != "QUJD" {
		t.Errorf("the image by its document id is %+v", held)
	}
	if held := c.Ledger.Image(entry.ID); held == nil || held.Bytes != "QUJD" {
		t.Errorf("the image by its ledger id is %+v", held)
	}
	if held := c.Ledger.Image("elsewhere"); held != nil {
		t.Errorf("an id that names nothing gave %+v", held)
	}
}

func TestAnImageThatCannotBeShownIsTheLineThatSaysWhy(t *testing.T) {
	c := openContext(t, modelSession())
	retain(t, c, "scan", "artifact", "A scan", MustParseJSON(`{"kind":"image","mediaType":"image/png","bytes":"QUJD"}`))
	cases := []struct {
		id    string
		value string
		line  string
	}{
		{"gif", `{"kind":"image","mediaType":"image/gif","bytes":"QUJD"}`, "[image 1 (image/gif): not shown, because an image must be image/png, image/jpeg, image/webp]"},
		{"malformed", `{"kind":"image","mediaType":"image/png","bytes":"not base64!"}`, "[image 1 (image/png): not shown, because an image’s bytes are base64]"},
		{"extra", `{"kind":"image","mediaType":"image/png","bytes":"QUJD","caption":"x"}`, `[image 1: not shown, because an image part does not carry "caption"]`},
		{"unresolved", `{"kind":"image","mediaType":"image/png","reference":"gone"}`, `[image 1 (image/png): not shown, because "gone" names no image this execution retains]`},
	}
	ids := []string{}
	for _, held := range cases {
		entry := retain(t, c, held.id, "artifact", "An attachment, "+held.id, MustParseJSON(held.value))
		ids = append(ids, entry.ID)
		answered, err := call(t, tool(t, c, ReadTool), `{"id":`+QuoteJSON(entry.ID)+`}`)
		if err != nil {
			t.Fatal(err)
		}
		if len(answered.Content) != 1 || answered.Content[0].Text() != held.line {
			t.Errorf("%s read back as %s", held.id, answered.Value().Text())
		}
		if answered.StructuredContent.Get("notShown").Str() != held.line ||
			answered.StructuredContent.Get("type").Str() != "image" ||
			answered.StructuredContent.Get("id").Str() != entry.ID {
			t.Errorf("%s answered %s", held.id, answered.StructuredContent.Text())
		}
	}
	// A reference that resolves is the image it names, whole.
	pointing := retain(t, c, "pointing", "artifact", "The scan again", MustParseJSON(`{"kind":"image","mediaType":"image/png","reference":"scan"}`))
	resolved, err := call(t, tool(t, c, ReadTool), `{"id":`+QuoteJSON(pointing.ID)+`}`)
	if err != nil {
		t.Fatal(err)
	}
	want := ObjectOf("type", String("image"), "id", String(pointing.ID), "label", String("The scan again"),
		"mediaType", String("image/png"), "bytes", Number(3))
	if !equalJSON(resolved.StructuredContent, want) {
		t.Errorf("the resolved reference answered %s", resolved.StructuredContent.Text())
	}
	// search names each one it cannot show, with the same line.
	found := searchShown(t, c, `{"pattern":"anything"}`)
	shown := found.Get("notShown").Items()
	if len(shown) != len(ids) {
		t.Fatalf("the search named %d images", len(shown))
	}
	for at, held := range cases {
		if shown[at].Get("id").Str() != ids[at] || shown[at].Get("text").Str() != held.line {
			t.Errorf("%s is named as %s", held.id, shown[at].Text())
		}
	}
}

func TestSearchNamesAtMostTwentyImagesInAQuarterOfTheWindow(t *testing.T) {
	c := openContext(t, modelSession())
	retain(t, c, "policy", "artifact", "The policy", String(policyText))
	for n := 0; n < 40; n++ {
		reference := "r" + strconv.Itoa(n) + "-"
		reference += strings.Repeat("x", 400-len(reference))
		retain(t, c, "gone-"+strconv.Itoa(n), "artifact", "Attachment "+strconv.Itoa(n),
			MustParseJSON(`{"kind":"image","mediaType":"image/png","reference":`+QuoteJSON(reference)+`}`))
	}
	first := searchShown(t, c, `{"pattern":"USD","limit":1}`)
	shown := first.Get("notShown")
	if shown.Len() == 0 || shown.Len() > SearchNotShown {
		t.Errorf("the search named %d images", shown.Len())
	}
	if UTF8Bytes(shown.Text()) > ReadWindowBytes(c.Policy.ToolResultBytes)/4 {
		t.Errorf("the images it cannot show take %d bytes", UTF8Bytes(shown.Text()))
	}
	if first.Get("matches").Len() != 1 {
		t.Errorf("the search found %d matches", first.Get("matches").Len())
	}
	next := searchShown(t, c, `{"pattern":"USD","limit":1,"cursor":`+QuoteJSON(first.Get("more").Str())+`}`)
	if next.Get("notShown") != nil {
		t.Error("the second page named images it cannot show")
	}
	if next.Get("matches").Len() != 1 {
		t.Errorf("the second page found %d matches", next.Get("matches").Len())
	}
}
