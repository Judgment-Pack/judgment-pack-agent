package runcontext

// The JSON value model, which every bound in this package is counted on.

import (
	"strings"
	"testing"
)

func TestTheValueWritesWhatJSONStringifyWrites(t *testing.T) {
	for _, held := range []struct{ text, want string }{
		// Member order is the order the members were written in, with integer-like names first.
		{`{"b":1,"a":2}`, `{"b":1,"a":2}`},
		{`{"2":1,"b":2,"1":3}`, `{"1":3,"2":1,"b":2}`},
		// Numbers as JavaScript writes them.
		{`[1,1.5,1e21,1e-7,-0.5,100]`, `[1,1.5,1e+21,1e-7,-0.5,100]`},
		// Only the quote, the backslash and the control characters are escaped: U+2028 and the
		// HTML characters are written as themselves, which Go's own encoder does not do.
		{`["<&> \u0001\t\"\\"]`, "[\"<&> \\u0001\\t\\\"\\\\\"]"},
		{`{"a":[],"b":{}}`, `{"a":[],"b":{}}`},
	} {
		if got := MustParseJSON(held.text).Text(); got != held.want {
			t.Errorf("%s was written as %s", held.text, got)
		}
	}
}

func TestTheIndentedValueIsWhatTheReaderReads(t *testing.T) {
	// JSON.stringify(value, null, 1): the lines an arbitrary retained value is read by.
	held := MustParseJSON(`{"a":[1,{"b":"c"}]}`)
	want := "{\n \"a\": [\n  1,\n  {\n   \"b\": \"c\"\n  }\n ]\n}"
	if got := string(held.JSONIndented()); got != want {
		t.Errorf("the indented value is\n%q", got)
	}
}

func TestCanonicalJSONSortsEveryObjectsMembers(t *testing.T) {
	held := MustParseJSON(`{"b":{"d":1,"c":[{"f":1,"e":2}]},"a":"x"}`)
	if got := CanonicalJSON(held); got != `{"a":"x","b":{"c":[{"e":2,"f":1}],"d":1}}` {
		t.Errorf("the canonical form is %s", got)
	}
}

func TestStringsAreCountedAsJavaScriptCountsThem(t *testing.T) {
	// An astral character is two UTF-16 code units and four UTF-8 bytes.
	if U16Length("😀") != 2 || UTF8Bytes("😀") != 4 {
		t.Errorf("😀 is %d units and %d bytes", U16Length("😀"), UTF8Bytes("😀"))
	}
	if held := U16Slice("a😀b", 1, 3); held != "😀" {
		t.Errorf("the slice is %q", held)
	}
	if held := reverseString("a😀b"); held != "b😀a" {
		t.Errorf("the reversal is %q", held)
	}
	// A slice that would cut a pair in half: the callers check the halves first, and the model
	// never sees one, because a Go string cannot hold half of a pair.
	if held := U16Slice("😀", 0, 1); !strings.Contains(held, "�") {
		t.Errorf("half a pair sliced to %q", held)
	}
	if indexOfU16("a😀b😀", "😀", 1) != 1 || indexOfU16("a😀b😀", "😀", 2) != 4 {
		t.Error("indexOf does not count UTF-16 code units")
	}
	if lastIndexOfU16("a😀b😀", "😀") != 4 {
		t.Error("lastIndexOf does not count UTF-16 code units")
	}
}

func TestARandomUUIDIsVersionFour(t *testing.T) {
	held := randomUUID()
	if len(held) != 36 || held[14] != '4' || !strings.Contains("89ab", held[19:20]) {
		t.Errorf("the uuid is %s", held)
	}
	if randomUUID() == held {
		t.Error("two draws gave one uuid")
	}
}
