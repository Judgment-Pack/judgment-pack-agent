// Package runcontext is the run's context in Go: the budget table and its bounds, the data
// framing, the context ledger and its reading tools, the chat-scoped context, the session's
// prompt and compaction, the execution controller and its checkpoint, the recall rule and the
// prompt half of Plan mode. It is the port of packages/harness/context/*.ts,
// packages/harness/framing.ts, packages/harness/execution/*.ts,
// packages/harness/certification/recall.ts and packages/harness/planning/policy.ts.
//
// The package name is runcontext because context would shadow the standard library's.
//
// What a model or a person reads is the TypeScript's text byte for byte, and every bound is
// counted on the same bytes: a value's JSON is what JSON.stringify writes (Value below), a
// string's length and offsets are UTF-16 code units, as JavaScript counts them, and a byte count
// is the UTF-8 length (UTF8Bytes).
package runcontext

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// Kind names the kind of a JSON value.
type Kind int

// The kinds a Value may be.
const (
	KindNull Kind = iota
	KindBool
	KindNumber
	KindString
	KindArray
	KindObject
)

// Value is one JSON value as JavaScript holds it, so that what this package counts and writes is
// what JSON.stringify counts and writes: an object keeps its members in the order a JavaScript
// object enumerates them (integer-like names first, ascending, then the order they were written;
// a repeated name keeps its first place and its last value), a number is a float64, and a string
// is UTF-8 (a lone surrogate, which a JavaScript string may hold, cannot occur in one).
//
// It is the Go reading of the value JSON.parse makes and JSON.stringify writes, the same reading
// go/gate holds for a frame (go/gate/json.go), repeated here because that one is the gate's own.
type Value struct {
	kind Kind
	b    bool
	n    float64
	s    string
	a    []*Value
	o    []member
}

type member struct {
	key string
	val *Value
}

// Null, True, False, Number, String are values of each kind.
func Null() *Value            { return &Value{kind: KindNull} }
func Bool(b bool) *Value      { return &Value{kind: KindBool, b: b} }
func Number(n float64) *Value { return &Value{kind: KindNumber, n: n} }
func String(s string) *Value  { return &Value{kind: KindString, s: s} }

// Array is an array value of the items given.
func Array(items ...*Value) *Value {
	if items == nil {
		items = []*Value{}
	}
	return &Value{kind: KindArray, a: items}
}

// Object is an empty object value; Set writes its members in order.
func Object() *Value { return &Value{kind: KindObject, o: []member{}} }

// ObjectOf is an object value with the members given, in the order given.
func ObjectOf(pairs ...any) *Value {
	out := Object()
	for at := 0; at+1 < len(pairs); at += 2 {
		key, _ := pairs[at].(string)
		out.Set(key, pairs[at+1].(*Value))
	}
	return out
}

var errTrailing = errors.New("data after the JSON value")

// ParseJSON reads one JSON text as JSON.parse does.
func ParseJSON(data []byte) (*Value, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	value, err := readValue(decoder)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, errTrailing
	}
	return value, nil
}

// MustParseJSON reads one JSON text and panics where it is not one. For a literal in code.
func MustParseJSON(text string) *Value {
	value, err := ParseJSON([]byte(text))
	if err != nil {
		panic(err)
	}
	return value
}

func readValue(decoder *json.Decoder) (*Value, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	switch t := token.(type) {
	case nil:
		return Null(), nil
	case bool:
		return Bool(t), nil
	case json.Number:
		f, err := strconv.ParseFloat(string(t), 64)
		if err != nil && !errors.Is(err, strconv.ErrRange) {
			return nil, err
		}
		return Number(f), nil
	case string:
		return String(t), nil
	case json.Delim:
		switch t {
		case '[':
			out := Array()
			for decoder.More() {
				item, err := readValue(decoder)
				if err != nil {
					return nil, err
				}
				out.a = append(out.a, item)
			}
			_, err := decoder.Token()
			return out, err
		case '{':
			out := Object()
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return nil, err
				}
				key, _ := keyToken.(string)
				item, err := readValue(decoder)
				if err != nil {
					return nil, err
				}
				out.Set(key, item)
			}
			_, err := decoder.Token()
			return out, err
		}
	}
	return nil, errors.New("not a JSON value")
}

// arrayIndex reports a name a JavaScript object orders as an integer index.
func arrayIndex(key string) (uint64, bool) {
	if key == "" || (len(key) > 1 && key[0] == '0') {
		return 0, false
	}
	n, err := strconv.ParseUint(key, 10, 64)
	if err != nil || n >= math.MaxUint32 {
		return 0, false
	}
	return n, true
}

// Kind is the value's kind.
func (v *Value) Kind() Kind { return v.kind }

// IsObject reports an object; IsArray an array; IsString a string.
func (v *Value) IsObject() bool { return v != nil && v.kind == KindObject }
func (v *Value) IsArray() bool  { return v != nil && v.kind == KindArray }
func (v *Value) IsString() bool { return v != nil && v.kind == KindString }

// Str is a string value's text, Num a number's value, Truth a boolean's.
func (v *Value) Str() string  { return v.s }
func (v *Value) Num() float64 { return v.n }
func (v *Value) Truth() bool  { return v != nil && v.kind == KindBool && v.b }

// Items are an array's items.
func (v *Value) Items() []*Value {
	if v == nil || v.kind != KindArray {
		return nil
	}
	return v.a
}

// Len is an array's length or an object's member count.
func (v *Value) Len() int {
	if v == nil {
		return 0
	}
	if v.kind == KindArray {
		return len(v.a)
	}
	return len(v.o)
}

// Get is an own member of an object, or nil. An array answers by its index as a name, as
// JavaScript's property access does.
func (v *Value) Get(key string) *Value {
	if v == nil {
		return nil
	}
	if v.kind == KindArray {
		at, ok := arrayIndex(key)
		if !ok || at >= uint64(len(v.a)) {
			return nil
		}
		return v.a[at]
	}
	if v.kind != KindObject {
		return nil
	}
	for _, m := range v.o {
		if m.key == key {
			return m.val
		}
	}
	return nil
}

// Has reports an own member, as Object.hasOwn does.
func (v *Value) Has(key string) bool { return v.Get(key) != nil }

// Set writes a member where it is, or adds it in the place a JavaScript object gives it.
func (v *Value) Set(key string, val *Value) *Value {
	for i := range v.o {
		if v.o[i].key == key {
			v.o[i].val = val
			return v
		}
	}
	v.o = append(v.o, member{key, val})
	sort.SliceStable(v.o, func(i, j int) bool {
		a, aIndex := arrayIndex(v.o[i].key)
		b, bIndex := arrayIndex(v.o[j].key)
		if aIndex && bIndex {
			return a < b
		}
		return aIndex && !bIndex
	})
	return v
}

// Append adds an item to an array.
func (v *Value) Append(item *Value) *Value { v.a = append(v.a, item); return v }

// Keys are an object's member names in enumeration order, or an array's indices as names, as
// Object.keys gives them.
func (v *Value) Keys() []string {
	if v.IsArray() {
		names := make([]string, 0, len(v.a))
		for at := range v.a {
			names = append(names, strconv.Itoa(at))
		}
		return names
	}
	if v == nil || v.kind != KindObject {
		return nil
	}
	names := make([]string, 0, len(v.o))
	for _, m := range v.o {
		names = append(names, m.key)
	}
	return names
}

// Clone copies a value deeply, as structuredClone does.
func (v *Value) Clone() *Value {
	if v == nil {
		return nil
	}
	out := *v
	if v.a != nil {
		out.a = make([]*Value, len(v.a))
		for i, item := range v.a {
			out.a[i] = item.Clone()
		}
	}
	if v.o != nil {
		out.o = make([]member, len(v.o))
		for i, m := range v.o {
			out.o[i] = member{m.key, m.val.Clone()}
		}
	}
	return &out
}

// JSON is the value as JSON.stringify writes it: no whitespace, members in enumeration order,
// only the quote, the backslash and the control characters escaped (U+2028 and U+2029 are written
// as themselves, as JSON.stringify writes them and Go's own encoder does not).
func (v *Value) JSON() []byte {
	var b bytes.Buffer
	v.write(&b, "", "")
	return b.Bytes()
}

// Text is the value as JSON.stringify writes it, as a string.
func (v *Value) Text() string { return string(v.JSON()) }

// JSONIndented is JSON.stringify(value, null, 1): one space of indent a level, as the reader
// reads an arbitrary retained value by its lines.
func (v *Value) JSONIndented() []byte {
	var b bytes.Buffer
	v.write(&b, " ", "")
	return b.Bytes()
}

func (v *Value) write(b *bytes.Buffer, indent, at string) {
	if v == nil {
		b.WriteString("null")
		return
	}
	inner := at + indent
	gap, colon := "", ":"
	if indent != "" {
		gap = "\n" + inner
		colon = ": "
	}
	switch v.kind {
	case KindNull:
		b.WriteString("null")
	case KindBool:
		b.WriteString(strconv.FormatBool(v.b))
	case KindNumber:
		if math.IsInf(v.n, 0) || math.IsNaN(v.n) {
			b.WriteString("null")
		} else {
			b.WriteString(jsNumber(v.n))
		}
	case KindString:
		b.WriteString(quoteJS(v.s))
	case KindArray:
		if len(v.a) == 0 {
			b.WriteString("[]")
			return
		}
		b.WriteByte('[')
		for i, item := range v.a {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(gap)
			item.write(b, indent, inner)
		}
		if indent != "" {
			b.WriteString("\n" + at)
		}
		b.WriteByte(']')
	case KindObject:
		if len(v.o) == 0 {
			b.WriteString("{}")
			return
		}
		b.WriteByte('{')
		for i, m := range v.o {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(gap)
			b.WriteString(quoteJS(m.key))
			b.WriteString(colon)
			m.val.write(b, indent, inner)
		}
		if indent != "" {
			b.WriteString("\n" + at)
		}
		b.WriteByte('}')
	}
}

// quoteJS writes a string as JSON.stringify does.
func quoteJS(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				b.WriteString(`\u00`)
				b.WriteString(strconv.FormatInt(int64(r)>>4, 16))
				b.WriteString(strconv.FormatInt(int64(r)&0xf, 16))
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// QuoteJSON is JSON.stringify of one string.
func QuoteJSON(s string) string { return quoteJS(s) }

// jsNumber writes a finite number as JavaScript's Number::toString does.
func jsNumber(f float64) string {
	if f == 0 {
		return "0"
	}
	sign := ""
	if f < 0 {
		sign = "-"
		f = -f
	}
	formatted := strconv.FormatFloat(f, 'e', -1, 64)
	mantissa, exponent, _ := strings.Cut(formatted, "e")
	digits := strings.Replace(mantissa, ".", "", 1)
	e, _ := strconv.Atoi(exponent)
	k, n := len(digits), e+1
	switch {
	case k <= n && n <= 21:
		return sign + digits + strings.Repeat("0", n-k)
	case 0 < n && n <= 21:
		return sign + digits[:n] + "." + digits[n:]
	case -6 < n && n <= 0:
		return sign + "0." + strings.Repeat("0", -n) + digits
	}
	exp := n - 1
	expSign := "+"
	if exp < 0 {
		expSign = "-"
		exp = -exp
	}
	if k == 1 {
		return sign + digits + "e" + expSign + strconv.Itoa(exp)
	}
	return sign + digits[:1] + "." + digits[1:] + "e" + expSign + strconv.Itoa(exp)
}

// UTF8Bytes is the length of a string in UTF-8, as TextEncoder counts it (utf8Bytes in
// context/policy.ts).
func UTF8Bytes(s string) int {
	if utf8.ValidString(s) {
		return len(s)
	}
	return len([]byte(strings.ToValidUTF8(s, "�")))
}

// CanonicalJSON is JSON with every object's members sorted by name (in UTF-16 code units, as
// Array.prototype.sort orders strings), no whitespace, and each string and number as
// JSON.stringify writes it (canonicalJSON in context/chat.ts).
func CanonicalJSON(v *Value) string {
	var b strings.Builder
	canonical(&b, v)
	return b.String()
}

func canonical(b *strings.Builder, v *Value) {
	if v == nil {
		b.WriteString("null")
		return
	}
	switch v.kind {
	case KindArray:
		b.WriteByte('[')
		for i, item := range v.a {
			if i > 0 {
				b.WriteByte(',')
			}
			canonical(b, item)
		}
		b.WriteByte(']')
	case KindObject:
		names := v.Keys()
		sort.SliceStable(names, func(i, j int) bool { return lessUTF16(names[i], names[j]) })
		b.WriteByte('{')
		for i, name := range names {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(quoteJS(name))
			b.WriteByte(':')
			canonical(b, v.Get(name))
		}
		b.WriteByte('}')
	default:
		b.Write(v.JSON())
	}
}

// lessUTF16 orders two strings by their UTF-16 code units, as JavaScript compares strings.
func lessUTF16(a, b string) bool {
	left, right := utf16.Encode([]rune(a)), utf16.Encode([]rune(b))
	for i := 0; i < len(left) && i < len(right); i++ {
		if left[i] != right[i] {
			return left[i] < right[i]
		}
	}
	return len(left) < len(right)
}

// --- strings as JavaScript counts them ---------------------------------------------------------

// u16 is a string's UTF-16 code units, as String.prototype.length counts them.
func u16(s string) []uint16 { return utf16.Encode([]rune(s)) }

// U16Length is String.prototype.length: the count of UTF-16 code units.
func U16Length(s string) int { return len(u16(s)) }

// U16Slice is String.prototype.slice(from, to) over UTF-16 code units, with the bounds clamped.
// A surrogate pair cut in half leaves U+FFFD, because a Go string cannot hold half of one; the
// callers that must not cut one (the reader's cursor, the search's line window, encodedPrefix)
// each check the halves first, as the TypeScript does.
func U16Slice(s string, from, to int) string {
	units := u16(s)
	if from < 0 {
		from = 0
	}
	if to > len(units) {
		to = len(units)
	}
	if from >= to {
		return ""
	}
	return string(utf16.Decode(units[from:to]))
}

// U16At is the UTF-16 code unit at an index, and false past the end.
func U16At(s string, at int) (uint16, bool) {
	units := u16(s)
	if at < 0 || at >= len(units) {
		return 0, false
	}
	return units[at], true
}

func isHighSurrogate(c uint16) bool { return c >= 0xd800 && c <= 0xdbff }
func isLowSurrogate(c uint16) bool  { return c >= 0xdc00 && c <= 0xdfff }

// lowSurrogateAt reports a code unit at an index that is a low surrogate (the TypeScript's
// /[\uDC00-\uDFFF]/ test of one character).
func lowSurrogateAt(s string, at int) bool {
	c, ok := U16At(s, at)
	return ok && isLowSurrogate(c)
}

// highSurrogateAt reports a code unit at an index that is a high surrogate.
func highSurrogateAt(s string, at int) bool {
	c, ok := U16At(s, at)
	return ok && isHighSurrogate(c)
}

// codePointAt is String.prototype.codePointAt over UTF-16 code units: the code point at an index
// and its width in code units.
func codePointAt(units []uint16, at int) (rune, int) {
	c := units[at]
	if isHighSurrogate(c) && at+1 < len(units) && isLowSurrogate(units[at+1]) {
		return utf16.DecodeRune(rune(c), rune(units[at+1])), 2
	}
	return rune(c), 1
}

// itoa is strconv.Itoa, for the texts that name a number.
func itoa(n int) string { return strconv.Itoa(n) }

// reverseString is [...value].reverse().join(”): the string's code points, reversed.
func reverseString(s string) string {
	runes := []rune(s)
	for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
		runes[i], runes[j] = runes[j], runes[i]
	}
	return string(runes)
}
