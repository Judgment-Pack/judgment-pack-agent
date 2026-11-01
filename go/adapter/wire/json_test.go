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
