// Package contract holds the wire contract's types in Go, generated from contract/schemas by
// contractgen (go/internal/contractgen), and the helpers the generated code calls.
//
// The types keep a value's shape whole, so a value decoded and encoded again is the value it was
// (roundtrip_test.go holds every vector to that). They do not validate: the schemas do.
package contract

//go:generate go run ../internal/cmd/contractgen -schemas ../../contract/schemas -package contract -out contract_gen.go

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var errorf = fmt.Errorf

// Nullable is a member whose schema admits a value type (including a union) and null. Set is false where the member is
// absent, and Null is true where it is present as null.
type Nullable[T any] struct {
	Set   bool
	Null  bool
	Value T
}

// IsZero reports an absent member, so omitzero leaves it out.
func (n Nullable[T]) IsZero() bool { return !n.Set }

// UnmarshalJSON reads null or a value.
func (n *Nullable[T]) UnmarshalJSON(data []byte) error {
	n.Set = true
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		n.Null = true
		return nil
	}
	return json.Unmarshal(data, &n.Value)
}

// MarshalJSON writes null or the value.
func (n Nullable[T]) MarshalJSON() ([]byte, error) {
	if n.Null {
		return []byte("null"), nil
	}
	return json.Marshal(n.Value)
}

// decodeObject decodes an object into a struct's plain form and returns the members the struct
// does not name. Where closed is true, such a member is an error.
func decodeObject(data []byte, plain any, known []string, closed bool) (map[string]json.RawMessage, error) {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(data, &members); err != nil {
		return nil, err
	}
	if members == nil {
		return nil, errorf("null is not an object")
	}
	if err := json.Unmarshal(data, plain); err != nil {
		return nil, err
	}
	named := make(map[string]bool, len(known))
	for _, name := range known {
		named[name] = true
	}
	var extra map[string]json.RawMessage
	for name, value := range members {
		if named[name] {
			continue
		}
		if closed {
			return nil, errorf("member %q is not named by the closed schema", name)
		}
		if extra == nil {
			extra = map[string]json.RawMessage{}
		}
		extra[name] = value
	}
	return extra, nil
}

// encodeObject encodes a struct's plain form, then the members it does not name, by name.
func encodeObject(plain any, extra map[string]json.RawMessage) ([]byte, error) {
	encoded, err := json.Marshal(plain)
	if err != nil || len(extra) == 0 {
		return encoded, err
	}
	names := make([]string, 0, len(extra))
	for name := range extra {
		names = append(names, name)
	}
	sort.Strings(names)
	var b bytes.Buffer
	b.Write(encoded[:len(encoded)-1])
	first := len(encoded) == 2
	for _, name := range names {
		if !first {
			b.WriteByte(',')
		}
		first = false
		key, _ := json.Marshal(name)
		b.Write(key)
		b.WriteByte(':')
		b.Write(extra[name])
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// jsonKind names the kind of a JSON value by its first byte.
func jsonKind(data []byte) string {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return ""
	}
	switch trimmed[0] {
	case '{':
		return "object"
	case '[':
		return "array"
	case '"':
		return "string"
	case 't', 'f':
		return "boolean"
	case 'n':
		return "null"
	}
	return "number"
}

// memberString reads one string member of an object.
func memberString(data []byte, name string) (string, error) {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(data, &members); err != nil {
		return "", err
	}
	raw, ok := members[name]
	if !ok {
		return "", errorf("no %q member", name)
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", errorf("member %q is not a string", name)
	}
	return value, nil
}

// memberInteger selects an exact safe-integer tag and patches only that leaf for
// typed decoding. It follows the retained report byte reader's exact decimal model,
// rather than rounding a fractional token through float64. Caller and payload bytes stay whole.
func memberInteger(data []byte, name string) (int64, []byte, error) {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(data, &members); err != nil {
		return 0, nil, err
	}
	raw, found := members[name]
	if !found {
		return 0, nil, errorf("no %q member", name)
	}
	raw = bytes.TrimSpace(raw)
	if jsonKind(raw) != "number" {
		return 0, nil, errorf("member %q is not an exact safe integer", name)
	}
	token := string(raw)
	negative := strings.HasPrefix(token, "-")
	unsigned := strings.TrimPrefix(token, "-")
	mantissa, exponentText, exponential := strings.Cut(strings.ToLower(unsigned), "e")
	integer, fraction, _ := strings.Cut(mantissa, ".")
	digits := strings.TrimLeft(integer+fraction, "0")
	var value int64
	if digits != "" {
		exponent := 0
		if exponential {
			var err error
			exponent, err = strconv.Atoi(exponentText)
			// A nonzero safe integer cannot need an exponent larger than its token
			// plus the 16-digit safe domain. Check before any scale arithmetic.
			if err != nil || exponent > len(token)+16 || exponent < -len(token)-16 {
				return 0, nil, errorf("member %q is not an exact safe integer", name)
			}
		}
		trimmed := strings.TrimRight(digits, "0")
		scale := exponent - len(fraction) + len(digits) - len(trimmed)
		if scale < 0 || len(trimmed)+scale > 16 {
			return 0, nil, errorf("member %q is not an exact safe integer", name)
		}
		n, err := strconv.ParseInt(trimmed+strings.Repeat("0", scale), 10, 64)
		if err != nil || n > 9007199254740991 {
			return 0, nil, errorf("member %q is not an exact safe integer", name)
		}
		value = n
		if negative {
			value = -value
		}
	}
	path := "/" + strings.ReplaceAll(strings.ReplaceAll(name, "~", "~0"), "/", "~1")
	view, err := patchRecordIntegers(data, map[string]string{path: strconv.FormatInt(value, 10)})
	return value, view, err
}

// hasMembers reports whether an object has every one of the named members.
func hasMembers(data []byte, names ...string) bool {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(data, &members); err != nil {
		return false
	}
	for _, name := range names {
		if _, ok := members[name]; !ok {
			return false
		}
	}
	return true
}

// checkPatternMembers keeps a patterned object closed to names outside its declared patterns.
func checkPatternMembers(extra map[string]json.RawMessage, patterns []string) error {
	for name := range extra {
		allowed := false
		for _, pattern := range patterns {
			if regexp.MustCompile(pattern).MatchString(name) {
				allowed = true
				break
			}
		}
		if !allowed {
			return errorf("member %q is not named by the closed schema", name)
		}
	}
	return nil
}
