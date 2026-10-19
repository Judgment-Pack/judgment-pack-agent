package gate

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

// The gate decides about a frame as the TypeScript gate does: on the value JSON.parse makes of
// it, and sends that value as JSON.stringify writes it. This file is that value in Go: an
// object's members in the order a JavaScript object enumerates them (integer-like names first,
// ascending, then the rest in the order they were written; a repeated name keeps its first place
// and its last value), numbers as float64 (a number too large for one is Infinity, which
// JSON.stringify writes as null), and the texts JSON.stringify and String write.

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

var errTrailing = errors.New("data after the JSON value")

// parseJSON reads one JSON text.
func parseJSON(data []byte) (*jvalue, error) {
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

func readValue(decoder *json.Decoder) (*jvalue, error) {
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
				item, err := readValue(decoder)
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
				item, err := readValue(decoder)
				if err != nil {
					return nil, err
				}
				out.set(key, item)
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

// get returns an own member, or nil.
func (v *jvalue) get(key string) *jvalue {
	if v == nil || v.kind != jobject {
		return nil
	}
	for _, member := range v.o {
		if member.key == key {
			return member.val
		}
	}
	return nil
}

// has reports an own member.
func (v *jvalue) has(key string) bool { return v.get(key) != nil }

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

// keys are an object's member names in enumeration order.
func (v *jvalue) keys() []string {
	names := make([]string, 0, len(v.o))
	for _, member := range v.o {
		names = append(names, member.key)
	}
	return names
}

// clone copies a value deeply.
func (v *jvalue) clone() *jvalue {
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

// stringify writes a value as JSON.stringify does.
func (v *jvalue) stringify() []byte {
	var b bytes.Buffer
	v.write(&b)
	return b.Bytes()
}

func (v *jvalue) write(b *bytes.Buffer) {
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
		b.WriteString(quote(v.s))
	case jarray:
		b.WriteByte('[')
		for i, item := range v.a {
			if i > 0 {
				b.WriteByte(',')
			}
			item.write(b)
		}
		b.WriteByte(']')
	case jobject:
		b.WriteByte('{')
		for i, member := range v.o {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(quote(member.key))
			b.WriteByte(':')
			member.val.write(b)
		}
		b.WriteByte('}')
	}
}

// quote writes a string as JSON.stringify does: only the quote, the backslash and the control
// characters are escaped, with the short escapes where JSON has them.
func quote(s string) string {
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

// jsString writes a value as JavaScript's String does; absent is undefined.
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

// quotePrefix is JSON.stringify(s.slice(0, n)): the string cut to its first n UTF-16 code units,
// and a surrogate pair cut in half written as its lone first half, \udXXX, as JSON.stringify
// writes one.
func quotePrefix(s string, n int) string {
	units := utf16.Encode([]rune(s))
	if len(units) <= n {
		return quote(s)
	}
	units = units[:n]
	last := units[n-1]
	if utf16.IsSurrogate(rune(last)) && last < 0xdc00 {
		inner := quote(string(utf16.Decode(units[:n-1])))
		return inner[:len(inner)-1] + `\u` + strconv.FormatInt(int64(last), 16) + `"`
	}
	return quote(string(utf16.Decode(units)))
}

// utf8Bytes is the length of a string in UTF-8, as TextEncoder counts it.
func utf8Bytes(s string) int {
	if utf8.ValidString(s) {
		return len(s)
	}
	return len([]byte(strings.ToValidUTF8(s, "�")))
}
