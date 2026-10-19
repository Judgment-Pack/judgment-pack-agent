package wire

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

// A wire body is the value JSON.parse makes of what arrived and the text JSON.stringify writes of
// what is sent, so this file is that value in Go: an object's members in the order a JavaScript
// object enumerates them (integer-like names first, ascending, then the order they were written; a
// repeated name keeps its first place and its last value), numbers as float64, and the text
// JSON.stringify writes (no HTML escaping, the short escapes, nothing else). It is the value model
// of go/gate's json.go, which this package cannot import, written here over the same rules.

// Kind is the JSON kind one Value has.
type Kind int

// The six kinds a JSON value has.
const (
	KindNull Kind = iota
	KindBool
	KindNumber
	KindString
	KindArray
	KindObject
)

// Value is one JSON value whose object members keep their order, as a JavaScript object's do.
type Value struct {
	kind    Kind
	boolean bool
	number  float64
	text    string
	items   []*Value
	members []member
}

type member struct {
	name  string
	value *Value
}

// Field is one object member as Obj takes it.
type Field struct {
	Name  string
	Value *Value
}

// F is one object member, for Obj.
func F(name string, value *Value) Field { return Field{Name: name, Value: value} }

// Null is the JSON null.
func Null() *Value { return &Value{kind: KindNull} }

// Bool is a JSON true or false.
func Bool(b bool) *Value { return &Value{kind: KindBool, boolean: b} }

// Num is a JSON number.
func Num(f float64) *Value { return &Value{kind: KindNumber, number: f} }

// Int is a JSON number written from an integer.
func Int(i int) *Value { return Num(float64(i)) }

// Str is a JSON string.
func Str(s string) *Value { return &Value{kind: KindString, text: s} }

// Arr is a JSON array of the values given, in their order.
func Arr(items ...*Value) *Value {
	held := make([]*Value, 0, len(items))
	held = append(held, items...)
	return &Value{kind: KindArray, items: held}
}

// Obj is a JSON object of the members given, in their order; a member whose value is nil is left
// out, as JSON.stringify leaves out an undefined one.
func Obj(fields ...Field) *Value {
	value := &Value{kind: KindObject}
	for _, field := range fields {
		if field.Value == nil {
			continue
		}
		value.Set(field.Name, field.Value)
	}
	return value
}

// Parse reads one JSON text, as JSON.parse does.
func Parse(data []byte) (*Value, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	value, err := readJSONValue(decoder)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, errTrailingJSON
	}
	return value, nil
}

var errTrailingJSON = errors.New("data after the JSON value")

func readJSONValue(decoder *json.Decoder) (*Value, error) {
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
		return Num(f), nil
	case string:
		return Str(t), nil
	case json.Delim:
		switch t {
		case '[':
			value := &Value{kind: KindArray}
			for decoder.More() {
				item, err := readJSONValue(decoder)
				if err != nil {
					return nil, err
				}
				value.items = append(value.items, item)
			}
			if _, err := decoder.Token(); err != nil {
				return nil, err
			}
			return value, nil
		case '{':
			value := &Value{kind: KindObject}
			for decoder.More() {
				name, err := decoder.Token()
				if err != nil {
					return nil, err
				}
				key, ok := name.(string)
				if !ok {
					return nil, errTrailingJSON
				}
				item, err := readJSONValue(decoder)
				if err != nil {
					return nil, err
				}
				value.Set(key, item)
			}
			if _, err := decoder.Token(); err != nil {
				return nil, err
			}
			return value, nil
		}
	}
	return nil, errTrailingJSON
}

// Kind is this value's JSON kind; a nil value is null's kind, as an absent member reads.
func (v *Value) Kind() Kind {
	if v == nil {
		return KindNull
	}
	return v.kind
}

// IsObject reports the `object(value)` test the TypeScript makes: an object and not an array.
func (v *Value) IsObject() bool { return v.Kind() == KindObject }

// IsString reports a JSON string, the `typeof value === 'string'` test.
func (v *Value) IsString() bool { return v.Kind() == KindString }

// Text is a string value's text, and "" for anything else.
func (v *Value) Text() string {
	if v == nil || v.kind != KindString {
		return ""
	}
	return v.text
}

// Number is a number value's number, and false for anything else.
func (v *Value) Number() (float64, bool) {
	if v == nil || v.kind != KindNumber {
		return 0, false
	}
	return v.number, true
}

// Boolean is a boolean value's own, and false for anything else.
func (v *Value) Boolean() (bool, bool) {
	if v == nil || v.kind != KindBool {
		return false, false
	}
	return v.boolean, true
}

// Items are an array's values, in order; none for anything else.
func (v *Value) Items() []*Value {
	if v == nil || v.kind != KindArray {
		return nil
	}
	return v.items
}

// Get is an own member of an object, or nil where there is none (an undefined member).
func (v *Value) Get(name string) *Value {
	if v == nil || v.kind != KindObject {
		return nil
	}
	for _, held := range v.members {
		if held.name == name {
			return held.value
		}
	}
	return nil
}

// Has reports an own member, including one whose value is null.
func (v *Value) Has(name string) bool { return v.Get(name) != nil }

// Keys are an object's member names in enumeration order.
func (v *Value) Keys() []string {
	if v == nil || v.kind != KindObject {
		return nil
	}
	names := make([]string, 0, len(v.members))
	for _, held := range v.members {
		names = append(names, held.name)
	}
	return names
}

// Members are an object's names and values in enumeration order, as Object.entries reads them.
func (v *Value) Members() []Field {
	if v == nil || v.kind != KindObject {
		return nil
	}
	fields := make([]Field, 0, len(v.members))
	for _, held := range v.members {
		fields = append(fields, Field{Name: held.name, Value: held.value})
	}
	return fields
}

// Set writes a member where it already is, or adds it in the place a JavaScript object gives it.
func (v *Value) Set(name string, value *Value) {
	if v.kind != KindObject {
		return
	}
	for i := range v.members {
		if v.members[i].name == name {
			v.members[i].value = value
			return
		}
	}
	v.members = append(v.members, member{name, value})
	sort.SliceStable(v.members, func(i, j int) bool {
		left, leftIndex := arrayIndexName(v.members[i].name)
		right, rightIndex := arrayIndexName(v.members[j].name)
		if leftIndex && rightIndex {
			return left < right
		}
		return leftIndex && !rightIndex
	})
}

// Append adds one value to an array.
func (v *Value) Append(items ...*Value) {
	if v.kind != KindArray {
		return
	}
	v.items = append(v.items, items...)
}

// Last is an array's final value, or nil where it has none (`at(-1)`).
func (v *Value) Last() *Value {
	if v == nil || v.kind != KindArray || len(v.items) == 0 {
		return nil
	}
	return v.items[len(v.items)-1]
}

// Clone copies a value deeply, as structuredClone does.
func (v *Value) Clone() *Value {
	if v == nil {
		return nil
	}
	out := *v
	if v.items != nil {
		out.items = make([]*Value, len(v.items))
		for i, item := range v.items {
			out.items[i] = item.Clone()
		}
	}
	if v.members != nil {
		out.members = make([]member, len(v.members))
		for i, held := range v.members {
			out.members[i] = member{held.name, held.value.Clone()}
		}
	}
	return &out
}

// arrayIndexName reports a name JavaScript enumerates as an array index.
func arrayIndexName(name string) (uint64, bool) {
	if name == "" || (len(name) > 1 && name[0] == '0') {
		return 0, false
	}
	n, err := strconv.ParseUint(name, 10, 64)
	if err != nil || n >= math.MaxUint32 {
		return 0, false
	}
	return n, true
}

// JSON is the value as JSON.stringify writes it.
func (v *Value) JSON() []byte {
	var b bytes.Buffer
	v.write(&b)
	return b.Bytes()
}

// String is the value as JSON.stringify writes it, as text.
func (v *Value) String() string { return string(v.JSON()) }

// MarshalJSON writes the value as JSON.stringify does rather than as Go's own encoder would.
func (v *Value) MarshalJSON() ([]byte, error) { return v.JSON(), nil }

// UnmarshalJSON reads one JSON text, keeping its members' order.
func (v *Value) UnmarshalJSON(data []byte) error {
	read, err := Parse(data)
	if err != nil {
		return err
	}
	*v = *read
	return nil
}

func (v *Value) write(b *bytes.Buffer) {
	if v == nil {
		b.WriteString("null")
		return
	}
	switch v.kind {
	case KindNull:
		b.WriteString("null")
	case KindBool:
		b.WriteString(strconv.FormatBool(v.boolean))
	case KindNumber:
		if math.IsInf(v.number, 0) || math.IsNaN(v.number) {
			b.WriteString("null")
		} else {
			b.WriteString(jsNumber(v.number))
		}
	case KindString:
		b.WriteString(jsQuote(v.text))
	case KindArray:
		b.WriteByte('[')
		for i, item := range v.items {
			if i > 0 {
				b.WriteByte(',')
			}
			item.write(b)
		}
		b.WriteByte(']')
	case KindObject:
		b.WriteByte('{')
		for i, held := range v.members {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(jsQuote(held.name))
			b.WriteByte(':')
			held.value.write(b)
		}
		b.WriteByte('}')
	}
}

// jsQuote writes a string as JSON.stringify does: the quote, the backslash and the control
// characters escaped, with the short escapes where JSON has them, and nothing else — U+2028 and
// U+2029 travel as themselves, as JSON.stringify sends them and Go's own encoder does not.
func jsQuote(s string) string {
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

// utf16Length is String.length: a string's length in UTF-16 code units.
func utf16Length(s string) int {
	return len(utf16.Encode([]rune(s)))
}

// safeCount is the TypeScript `count`: a safe non-negative integer, or false.
func safeCount(value *Value) (int, bool) {
	number, ok := value.Number()
	if !ok || number < 0 || number != math.Trunc(number) || number > 9007199254740991 {
		return 0, false
	}
	return int(number), true
}
