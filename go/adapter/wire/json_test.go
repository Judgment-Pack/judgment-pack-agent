package wire

import (
	"slices"
	"strings"
	"testing"
)

// The value model this package sends and reads: the bytes JSON.stringify writes, since the wire's
// bodies are compared byte for byte against what the TypeScript sends.

// TestStringifyWritesWhatJSONStringifyWrites holds the writer to JSON.stringify's own output.
func TestStringifyWritesWhatJSONStringifyWrites(t *testing.T) {
	for _, held := range []struct {
		what  string
		value *Value
		want  string
	}{
		{"an object's members in the order they were written", Obj(F("b", Int(1)), F("a", Int(2))), `{"b":1,"a":2}`},
		{"no HTML escaping", Str(`<a href="x">&</a>`), `"<a href=\"x\">&</a>"`},
		{"the line separators JSON.stringify leaves as themselves", Str("a b c"), "\"a b c\""},
		{"the short escapes", Str("a\nb\tc\u0001"), `"a\nb\tc\u0001"`},
		{"an integer", Int(4096), "4096"},
		{"a whole number that arrived with a point", mustParseValue(t, "1.0"), "1"},
		{"a fraction", Num(2.25), "2.25"},
		{"a large number", Num(1e21), "1e+21"},
		{"a small number", Num(1e-7), "1e-7"},
		{"a number at the edge of the plain form", Num(1e-6), "0.000001"},
		{"nothing at all", Null(), "null"},
	} {
		equalText(t, held.what, held.value.String(), held.want)
	}
}

// TestAnObjectEnumeratesAsJavaScriptDoes holds Set to a JavaScript object's own order: an
// integer-like name goes first, ascending, and a repeated name keeps its first place and its last
// value.
func TestAnObjectEnumeratesAsJavaScriptDoes(t *testing.T) {
	held := Obj(F("b", Int(1)), F("2", Int(2)), F("a", Int(3)), F("1", Int(4)))
	if names := held.Keys(); !slices.Equal(names, []string{"1", "2", "b", "a"}) {
		t.Errorf("the members enumerate as %v", names)
	}
	held.Set("b", Int(5))
	equalText(t, "a repeated name", held.String(), `{"1":4,"2":2,"b":5,"a":3}`)
}

// TestParseKeepsTheOrderItRead is what a replay rests on: a value read off the wire and written
// back carries its members in the order they arrived.
func TestParseKeepsTheOrderItRead(t *testing.T) {
	text := `{"type":"thinking","thinking":"a","signature":"s"}`
	equalText(t, "a block read and written back", mustParseValue(t, text).String(), text)
	if _, err := Parse([]byte(`{"a":1} and more`)); err == nil {
		t.Error("data after the value was read as the value")
	}
}

func mustParseValue(t *testing.T, text string) *Value {
	t.Helper()
	value, err := Parse([]byte(text))
	if err != nil {
		t.Fatalf("%s: %v", text, err)
	}
	return value
}

// What is read keeps its own text, byte for byte, at any depth, and loses it once written to (#275).
func TestArgumentTextIsTheTextThatWasRead(t *testing.T) {
	for _, text := range []string{
		`{"id":9007199254740993}`, `{"q":1e400}`, `{"q":1e-400}`, `{"q":1.0}`, `{"q":-0}`, `{"a":1,"a":2}`, `{"ID":1,"id":2}`, `{"b":2,"a":1}`,
		`{ "c" : 3 }`, " {\n\t\"c\": 3\r\n} ", `{"q":"` + string(rune(0x5c)) + `u0041é"}`, `{"o":{"p":[{"id":9007199254740993}]}}`, `{}`,
	} {
		whole, err := Parse([]byte(text))
		if err != nil {
			t.Fatal(err)
		}
		if got := whole.ArgumentText(); got != text {
			t.Errorf("as a whole text: %q, not %q", got, text)
		}
		body, err := Parse([]byte(`{ "before" : [1, "}"] , "input" :  ` + text + `  , "after": {"n":1.50} }`))
		if err != nil {
			t.Fatal(err)
		}
		want := strings.TrimSpace(text)
		if got := body.Get("input").ArgumentText(); got != want {
			t.Errorf("as a member: %q, not %q", got, want)
		}
		if got := body.Clone().Get("input").ArgumentText(); got != want {
			t.Errorf("a clone: %q, not %q", got, want)
		}
		if got := body.Get("after").ArgumentText(); got != `{"n":1.50}` {
			t.Errorf("the next member: %q", got)
		}
	}
	held, err := Parse([]byte(`{"id":9007199254740993}`))
	if err != nil {
		t.Fatal(err)
	}
	held.Set("id", Num(7))
	if got := held.ArgumentText(); got != `{"id":7}` {
		t.Errorf("written to: %q", got)
	}
	if got := Obj().ArgumentText(); got != `{}` {
		t.Errorf("never read: %q", got)
	}
}

// A call is written back as the model wrote it only where its parsed form cannot say it (#275).
func TestACallIsWrittenBackAsWrittenOnlyWhereItsParsedFormCannotSayIt(t *testing.T) {
	for _, text := range []string{`{"id":9007199254740993}`, `{"amount":10.50}`, `{"q":1e-400}`, `{"q":-0}`, `{"o":{"p":[{"id":9007199254740993}]}}`, `{"a":[1.0]}`, `{ "id" : 9007199254740993 }`} {
		if got := (ToolCall{Arguments: text}).Args().String(); got != text {
			t.Errorf("%s written back as %s", text, got)
		}
	}
	for text, want := range map[string]string{`{"id":7}`: `{"id":7}`, `{ "b" : 2 , "a" : 1 }`: `{"b":2,"a":1}`, `{"q":"x"}`: `{"q":"x"}`, `{"q":0.5}`: `{"q":0.5}`, `{}`: `{}`} {
		if got := (ToolCall{Arguments: text}).Args().String(); got != want {
			t.Errorf("%s written back as %s", text, got)
		}
	}
	// What the record cannot hold goes back in its parsed form, as published: nothing can copy it to a row.
	for text, want := range map[string]string{`{"q":1e400}`: `{"q":null}`, `{"a":1,"a":2}`: `{"a":2}`, `{"o":{"q":-1e400}}`: `{"o":{"q":null}}`, `{"n":1.0,"q":"` + string(rune(0x5c)) + `ud800"}`: `{"n":1,"q":"` + string(rune(0xfffd)) + `"}`} {
		if got := (ToolCall{Arguments: text}).Args().String(); got != want {
			t.Errorf("%s written back as %s", text, got)
		}
	}
	// Written to, it says what it now holds.
	held := (ToolCall{Arguments: `{"id":9007199254740993}`}).Args()
	held.Set("more", Num(1))
	if got := held.String(); strings.Contains(got, "9007199254740993") == false && !strings.Contains(got, "more") {
		t.Errorf("written to: %s", got)
	}
	if got := held.String(); !strings.Contains(got, "more") {
		t.Errorf("written to, it lost the member: %s", got)
	}
}

// A write under a marked object is not lost, at any depth, in an object or an array (#275).
func TestAWriteUnderAMarkedObjectIsKept(t *testing.T) {
	body, err := Parse([]byte(`{"contents":[{"args":{"id":9007199254740993}}],"generationConfig":{"maxOutputTokens":4096},"list":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	body.Get("generationConfig").Set("maxOutputTokens", Int(8))
	body.Get("list").Append(Int(1))
	want := `{"contents":[{"args":{"id":9007199254740993}}],"generationConfig":{"maxOutputTokens":8},"list":[1]}`
	if got := body.String(); got != want {
		t.Fatalf("%s", got)
	}
	clone := body.Clone()
	if got := clone.String(); got != want {
		t.Fatalf("a clone: %s", got)
	}
}

// The ceiling holds on a body something in which its parsed form cannot say.
func TestTheOutputCeilingHoldsOnAMarkedBody(t *testing.T) {
	for family, raw := range map[EndpointKind]string{
		Gemini:           `{"contents":[{"role":"model","parts":[{"functionCall":{"name":"x","args":{"id":9007199254740993}}}]}],"generationConfig":{"maxOutputTokens":4096}}`,
		Anthropic:        `{"messages":[{"role":"assistant","content":[{"type":"tool_use","input":{"id":9007199254740993}}]}],"max_tokens":4096}`,
		OpenAICompatible: `{"tools":[{"parameters":{"minimum":1.0}}],"max_tokens":4096}`,
	} {
		out, err := EnforceOutputCeiling(family, []byte(raw), 8)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(out), "4096") {
			t.Errorf("%v: %s", family, out)
		}
	}
}

// A call with no replay, from another family, is written back in each family as the model wrote it (LOW-10).
func TestACallWithNoReplayIsWrittenBackAsWritten(t *testing.T) {
	call := ToolCall{ID: "c", Name: "write", Arguments: `{"id":9007199254740993}`}
	for _, family := range []EndpointKind{OpenAICompatible, Anthropic, Gemini} {
		encoded := EncodeRequest(Request{Family: family, MaxOutputTokens: 8, Messages: []Message{{Role: RoleUser, Parts: []Part{TextPart("Do it.")}}, {Role: RoleAssistant, ToolCalls: []ToolCall{call}}}})
		if !strings.Contains(string(encoded.Body), "9007199254740993") {
			t.Errorf("%v: %s", family, encoded.Body)
		}
	}
}

// What the record can hold, by its surrogate escapes: a pair, and no other.
func TestHeldText(t *testing.T) {
	escape := string(rune(0x5c)) + "u"
	for text, want := range map[string]bool{
		`{"q":"x"}`: true,
		`{"q":"` + escape + `d83d` + escape + `de00"}`:                 true,
		`{"q":"` + escape + `0041"}`:                                   true,
		`{"q":"` + string(rune(0x5c)) + string(rune(0x5c)) + `ud800"}`: true,
		`{"q":"` + escape + `d800"}`:                                   false,
		`{"q":"` + escape + `de00"}`:                                   false,
		`{"q":"` + escape + `d83d` + escape + `0041"}`:                 false,
		"{\"q\":\"\xff\"}":                                             false,
	} {
		if heldText(text) != want {
			t.Errorf("%q: not %v", text, want)
		}
	}
}
