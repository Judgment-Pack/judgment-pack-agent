package turn

// The turn reads and writes JSON as the TypeScript does: on the value JSON.parse makes of a text,
// written back as JSON.stringify writes it. This file is that value in Go: an object's members in
// the order a JavaScript object enumerates them (integer-like names first, ascending, then the
// rest in the order they were written; a repeated name keeps its first place and its last value),
// numbers as float64, and the texts JSON.stringify and String write.
//
// It is a second copy of go/gate/json.go, whose own is unexported; the two are held to the same
// rules and neither package may import the other's.

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
)

type jkind int

const (
	jnull jkind = iota
	jbool
	jnumber
	jstring
	jarray
	jobject
)

type jvalue struct {
	kind jkind
	b    bool
	n    float64
	s    string
	a    []*jvalue
	o    []jmember
}

type jmember struct {
	key string
	val *jvalue
}

// jsSpace are the code points JavaScript's \s matches and trimStart and trimEnd remove.
var jsSpace = string([]rune{
	0x0009, 0x000a, 0x000b, 0x000c, 0x000d, 0x0020, 0x00a0, 0x1680,
	0x2000, 0x2001, 0x2002, 0x2003, 0x2004, 0x2005, 0x2006, 0x2007,
	0x2008, 0x2009, 0x200a, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff,
})

func trimStartJS(s string) string { return strings.TrimLeft(s, jsSpace) }
func trimEndJS(s string) string   { return strings.TrimRight(s, jsSpace) }

// parseJSON reads one JSON text, as JSON.parse does; nil and an error where it would throw.
func parseJSON(data []byte) (*jvalue, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	value, err := readJSON(decoder)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, errData
	}
	return value, nil
}

var errData = errorString("data after the JSON value")

type errorString string

func (e errorString) Error() string { return string(e) }

func readJSON(decoder *json.Decoder) (*jvalue, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	switch t := token.(type) {
	case nil:
		return &jvalue{kind: jnull}, nil
	case bool:
		return &jvalue{kind: jbool, b: t}, nil
	case json.Number:
		f, err := strconv.ParseFloat(string(t), 64)
		if err != nil && !errors.Is(err, strconv.ErrRange) {
			return nil, err
		}
		return &jvalue{kind: jnumber, n: f}, nil
	case string:
		return &jvalue{kind: jstring, s: t}, nil
	case json.Delim:
		switch t {
		case '[':
			out := &jvalue{kind: jarray, a: []*jvalue{}}
			for decoder.More() {
				item, err := readJSON(decoder)
				if err != nil {
					return nil, err
				}
				out.a = append(out.a, item)
			}
			_, err := decoder.Token()
			return out, err
		case '{':
			out := &jvalue{kind: jobject, o: []jmember{}}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return nil, err
				}
				key, _ := keyToken.(string)
				item, err := readJSON(decoder)
				if err != nil {
					return nil, err
				}
				out.set(key, item)
			}
			_, err := decoder.Token()
			return out, err
		}
	}
	return nil, errorString("not a JSON value")
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

func (v *jvalue) isObject() bool { return v != nil && v.kind == jobject }
func (v *jvalue) isArray() bool  { return v != nil && v.kind == jarray }

// get returns an own member, or nil (undefined).
func (v *jvalue) get(key string) *jvalue {
	if !v.isObject() {
		return nil
	}
	for _, member := range v.o {
		if member.key == key {
			return member.val
		}
	}
	return nil
}

// has reports an own member, as Object.hasOwn does.
func (v *jvalue) has(key string) bool {
	if !v.isObject() {
		return false
	}
	for _, member := range v.o {
		if member.key == key {
			return true
		}
	}
	return false
}

// set writes a member where it is, or adds it in the place a JavaScript object gives it.
func (v *jvalue) set(key string, val *jvalue) {
	for i := range v.o {
		if v.o[i].key == key {
			v.o[i].val = val
			return
		}
	}
	v.o = append(v.o, jmember{key, val})
	sort.SliceStable(v.o, func(i, j int) bool {
		a, aIndex := arrayIndex(v.o[i].key)
		b, bIndex := arrayIndex(v.o[j].key)
		if aIndex && bIndex {
			return a < b
		}
		return aIndex && !bIndex
	})
}

// keys are an object's member names in enumeration order; none, where the value is not an object.
func (v *jvalue) keys() []string {
	if !v.isObject() {
		return nil
	}
	names := make([]string, 0, len(v.o))
	for _, member := range v.o {
		names = append(names, member.key)
	}
	return names
}

func (v *jvalue) clone() *jvalue {
	if v == nil {
		return nil
	}
	out := *v
	if v.a != nil {
		out.a = make([]*jvalue, len(v.a))
		for i, item := range v.a {
			out.a[i] = item.clone()
		}
	}
	if v.o != nil {
		out.o = make([]jmember, len(v.o))
		for i, member := range v.o {
			out.o[i] = jmember{member.key, member.val.clone()}
		}
	}
	return &out
}

func jobj(members ...jmember) *jvalue { return &jvalue{kind: jobject, o: members} }
func jstr(s string) *jvalue           { return &jvalue{kind: jstring, s: s} }
func jbln(b bool) *jvalue             { return &jvalue{kind: jbool, b: b} }

// stringify writes a value as JSON.stringify does. A nil value is an absent one, which
// JSON.stringify writes as undefined; this writes null, so that a host never faults on one. No
// call site of this package reaches it: every value it writes was read from a JSON text first.
func (v *jvalue) stringify() string {
	if v == nil {
		return "null"
	}
	var b strings.Builder
	v.write(&b, "", "")
	return b.String()
}

// indent writes a value as JSON.stringify(value, null, n) does; null for a nil value, as
// stringify writes one.
func (v *jvalue) indent(n int) string {
	if v == nil {
		return "null"
	}
	var b strings.Builder
	v.write(&b, strings.Repeat(" ", n), "")
	return b.String()
}

func (v *jvalue) write(b *strings.Builder, step, prefix string) {
	switch v.kind {
	case jnull:
		b.WriteString("null")
	case jbool:
		b.WriteString(strconv.FormatBool(v.b))
	case jnumber:
		if math.IsInf(v.n, 0) || math.IsNaN(v.n) {
			b.WriteString("null")
		} else {
			b.WriteString(jsNumber(v.n))
		}
	case jstring:
		b.WriteString(quoteJS(v.s))
	case jarray:
		if len(v.a) == 0 {
			b.WriteString("[]")
			return
		}
		inner := prefix + step
		b.WriteByte('[')
		for i, item := range v.a {
			if i > 0 {
				b.WriteByte(',')
			}
			if step != "" {
				b.WriteString("\n" + inner)
			}
			item.write(b, step, inner)
		}
		if step != "" {
			b.WriteString("\n" + prefix)
		}
		b.WriteByte(']')
	case jobject:
		if len(v.o) == 0 {
			b.WriteString("{}")
			return
		}
		inner := prefix + step
		b.WriteByte('{')
		for i, member := range v.o {
			if i > 0 {
				b.WriteByte(',')
			}
			if step != "" {
				b.WriteString("\n" + inner)
			}
			b.WriteString(quoteJS(member.key))
			b.WriteByte(':')
			if step != "" {
				b.WriteByte(' ')
			}
			member.val.write(b, step, inner)
		}
		if step != "" {
			b.WriteString("\n" + prefix)
		}
		b.WriteByte('}')
	}
}

// quoteJS writes a string as JSON.stringify does: only the quote, the backslash and the control
// characters are escaped, with the short escapes where JSON has them, and nothing is escaped for
// HTML. Go differs from JavaScript on two counts, neither reachable from a parsed JSON text: a
// lone surrogate, which JSON.stringify writes as \udXXX and Go cannot hold; and U+2028 and
// U+2029, which neither escapes.
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

// jsString writes a value as JavaScript's String does; a nil value is undefined.
func jsString(v *jvalue) string {
	if v == nil {
		return "undefined"
	}
	switch v.kind {
	case jnull:
		return "null"
	case jbool:
		return strconv.FormatBool(v.b)
	case jnumber:
		if math.IsInf(v.n, 1) {
			return "Infinity"
		}
		if math.IsInf(v.n, -1) {
			return "-Infinity"
		}
		if math.IsNaN(v.n) {
			return "NaN"
		}
		return jsNumber(v.n)
	case jstring:
		return v.s
	case jarray:
		parts := make([]string, len(v.a))
		for i, item := range v.a {
			if item.kind != jnull {
				parts[i] = jsString(item)
			}
		}
		return strings.Join(parts, ",")
	}
	return "[object Object]"
}

// lessJS compares two strings as JavaScript's < does: by UTF-16 code unit.
func lessJS(a, b string) bool {
	left, right := utf16.Encode([]rune(a)), utf16.Encode([]rune(b))
	for i := 0; i < len(left) && i < len(right); i++ {
		if left[i] != right[i] {
			return left[i] < right[i]
		}
	}
	return len(left) < len(right)
}

// canonical is stable-key JSON (canonical in refutation.ts), so two values compare by content and
// not by member order. A nil value is undefined, which JSON.stringify leaves out and the
// TypeScript reads as the text "null".
func canonical(v *jvalue) string {
	if v == nil {
		return "null"
	}
	switch v.kind {
	case jarray:
		parts := make([]string, len(v.a))
		for i, item := range v.a {
			parts[i] = canonical(item)
		}
		return "[" + strings.Join(parts, ",") + "]"
	case jobject:
		members := append([]jmember(nil), v.o...)
		sort.SliceStable(members, func(i, j int) bool { return lessJS(members[i].key, members[j].key) })
		parts := make([]string, len(members))
		for i, member := range members {
			parts[i] = quoteJS(member.key) + ":" + canonical(member.val)
		}
		return "{" + strings.Join(parts, ",") + "}"
	}
	return v.stringify()
}

// boundedText is text(v, max, empty), which questions.ts and delegationContract.ts each define
// the same way: a string within max UTF-16 code units, non-blank unless empty is allowed.
func boundedText(v *jvalue, max int, empty bool) bool {
	return v != nil && v.kind == jstring && utf16Length(v.s) <= max && (empty || strings.Trim(v.s, jsSpace) != "")
}

// utf16Length counts a string in UTF-16 code units, as String.length does.
func utf16Length(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// cutUTF16 is String.prototype.slice(0, n): at most n UTF-16 code units, never half a pair.
func cutUTF16(s string, n int) string {
	units := 0
	for at, r := range s {
		size := 1
		if r >= 0x10000 {
			size = 2
		}
		if units+size > n {
			return s[:at]
		}
		units += size
	}
	return s
}

// raw is a value as the bytes of its JSON text.
func raw(v *jvalue) json.RawMessage { return json.RawMessage(v.stringify()) }

// value is the parsed form of a JSON text, or nil where it is not JSON (parsed in refutation.ts).
func value(data json.RawMessage) *jvalue {
	if len(data) == 0 {
		return nil
	}
	parsed, err := parseJSON(data)
	if err != nil {
		return nil
	}
	return parsed
}

// textValue is parsed(text) in refutation.ts: JSON text read back to its value, or nil where the
// member is not a string or its text is not JSON.
func textValue(v *jvalue) *jvalue {
	if v == nil || v.kind != jstring {
		return nil
	}
	return value(json.RawMessage(v.s))
}
