package contract

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const CheckpointVersion = 2
const EventVersion = 1
const PolicyVersion = 1

//go:embed record_shapes.json
var recordShapesJSON []byte
var recordShapes = func() map[string]map[string]any {
	var out map[string]map[string]any
	if err := json.Unmarshal(recordShapesJSON, &out); err != nil {
		panic(err)
	}
	return out
}()

type UnsupportedRecordVersion struct {
	Kind    string
	Version int64
}

func (e *UnsupportedRecordVersion) Error() string {
	return "Unsupported record version: " + e.Kind + "."
}
func (e *UnsupportedRecordVersion) FailureCode() string {
	if e.Kind == "policy" {
		return "configuration"
	}
	return "stale-state"
}
func (e *UnsupportedRecordVersion) Event() RunEvent {
	return recordRefusal(e.Kind, "record version unsupported", e.Error())
}

type UnknownRecordMember struct {
	Member string
	Kind   string
}

func (e *UnknownRecordMember) Error() string {
	p := e.Member
	if len(p) > 256 {
		p = p[:256]
	}
	return "Unknown record member: " + p + "."
}
func (e *UnknownRecordMember) FailureCode() string {
	if e.Kind == "policy" {
		return "configuration"
	}
	return "stale-state"
}
func (e *UnknownRecordMember) Event() RunEvent {
	return recordRefusal("records", "record member unsupported", "Record contains an unsupported member.")
}
func recordRefusal(tool, reason, detail string) RunEvent {
	return StampEvent(RunEvent{Guardrail: &RunEventGuardrail{Type: "guardrail", Tool: tool, Action: "refused", Reason: &reason, Detail: detail}})
}

var ErrInvalidRecord = errors.New("Record is invalid.")
var errRecord = ErrInvalidRecord

func recordVersion(kind string, value any) (int64, error) {
	obj, ok := value.(map[string]any)
	if !ok {
		return 0, errRecord
	}
	v, present := obj["version"]
	if !present && kind != "checkpoint" {
		return 0, nil
	}
	n, ok := v.(json.Number)
	if !ok {
		return 0, errRecord
	}
	f, err := n.Float64()
	if err != nil || f < 1 || f > 9007199254740991 || math.Trunc(f) != f {
		return 0, errRecord
	}
	current := int64(1)
	if kind == "checkpoint" {
		current = CheckpointVersion
	}
	version := int64(f)
	if version > current {
		return 0, &UnsupportedRecordVersion{kind, version}
	}
	return version, nil
}
func shapeObject(v any) map[string]any { s, _ := v.(map[string]any); return s }
func shapeList(v any) []any            { s, _ := v.([]any); return s }
func resolveShape(graph map[string]any, file string, s map[string]any) (string, map[string]any) {
	ref, _ := s["$ref"].(string)
	if ref == "" {
		return file, s
	}
	f, p, _ := strings.Cut(ref, "#")
	if f != "" {
		file = f
	}
	var node any = graph[file]
	for _, bit := range strings.Split(p, "/")[1:] {
		node = shapeObject(node)[strings.ReplaceAll(strings.ReplaceAll(bit, "~1", "/"), "~0", "~")]
	}
	return resolveShape(graph, file, shapeObject(node))
}
func shapeMatches(graph map[string]any, file string, s map[string]any, v any) bool {
	file, s = resolveShape(graph, file, s)
	equal := func(a, b any) bool {
		if number, ok := a.(float64); ok {
			n, ok := b.(json.Number)
			if !ok {
				return false
			}
			f, err := n.Float64()
			return err == nil && number == f
		}
		aa, _ := json.Marshal(a)
		bb, _ := json.Marshal(b)
		return bytes.Equal(aa, bb)
	}
	if c, ok := s["const"]; ok && !equal(c, v) {
		return false
	}
	if values, ok := s["enum"].([]any); ok {
		found := false
		for _, x := range values {
			found = found || equal(x, v)
		}
		if !found {
			return false
		}
	}
	if typ, present := s["type"]; present {
		types, ok := typ.([]any)
		if !ok {
			types = []any{typ}
		}
		kind := "null"
		switch v.(type) {
		case map[string]any:
			kind = "object"
		case []any:
			kind = "array"
		case string:
			kind = "string"
		case bool:
			kind = "boolean"
		case json.Number:
			kind = "number"
		}
		found := false
		for _, t := range types {
			found = found || t == kind || t == "integer" && safeRecordInteger(v)
		}
		if !found {
			return false
		}
	}
	if obj, ok := v.(map[string]any); ok {
		for _, key := range shapeList(s["required"]) {
			if _, present := obj[key.(string)]; !present {
				return false
			}
		}
		for key, child := range shapeObject(s["properties"]) {
			c := shapeObject(child)
			if held, present := obj[key]; present && (c["const"] != nil || c["enum"] != nil) && !shapeMatches(graph, file, c, held) {
				return false
			}
		}
	}
	return true
}

// safeRecordInteger follows the finite IEEE-754 parsed-value model shared with TypeScript.
func safeRecordInteger(v any) bool {
	n, ok := v.(json.Number)
	if !ok {
		return false
	}
	f, err := n.Float64()
	return err == nil && !math.IsInf(f, 0) && !math.IsNaN(f) && math.Trunc(f) == f && math.Abs(f) <= 9007199254740991
}
func integerShape(s map[string]any) bool {
	if s["type"] == "integer" {
		return true
	}
	for _, t := range shapeList(s["type"]) {
		if t == "integer" {
			return true
		}
	}
	if c, ok := s["const"].(float64); ok {
		return math.Trunc(c) == c
	}
	values := shapeList(s["enum"])
	if len(values) == 0 {
		return false
	}
	for _, v := range values {
		f, ok := v.(float64)
		if !ok || math.Trunc(f) != f {
			return false
		}
	}
	return true
}

// CheckRecord checks version and structural members without changing historical values.
func CheckRecord(kind string, data []byte) error { return checkRecord(kind, data, nil) }

// recordDecodeBytes normalizes only declared integer leaves in a detached decode tree.
// Arbitrary payload bytes (including member order) and the supplied bytes remain untouched.
func recordDecodeBytes(kind string, data []byte) ([]byte, error) {
	var normalized []byte
	if err := checkRecord(kind, data, &normalized); err != nil {
		return nil, err
	}
	return normalized, nil
}
func checkRecord(kind string, data []byte, normalized *[]byte) error {
	if kind == "checkpoint" && len(data) > 4*1024*1024 {
		return errRecord
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return errRecord
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return errRecord
	}
	version, err := recordVersion(kind, value)
	if err != nil {
		return err
	}
	generation := "current"
	if version == 0 || kind == "checkpoint" && version == 1 {
		generation = "historical"
	}
	graph := recordShapes[generation]
	file := map[string]string{"checkpoint": "checkpoint.json", "event": "run-event.json", "policy": "policy.json"}[kind]
	if file == "" {
		return errRecord
	}
	work := 0
	replacements := map[string]string{}
	var walk func(map[string]any, string, map[string]any, any, string, int) error
	pointer := func(path, key string) string {
		return path + "/" + strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")
	}
	walk = func(graph map[string]any, file string, s map[string]any, v any, path string, depth int) error {
		work++
		if work > 200000 || depth > 64 {
			return errRecord
		}
		if s["$ref"] == "checkpoint.json" && path != "" {
			raw, _ := json.Marshal(v)
			if len(raw) > 4*1024*1024 {
				return errRecord
			}
			version, err := recordVersion("checkpoint", v)
			if err != nil {
				return err
			}
			generation := "current"
			if version == 1 {
				generation = "historical"
			}
			nested := recordShapes[generation]
			// A nested record keeps its own traversal budget, as the TypeScript root reader does.
			outerWork := work
			work = 0
			err = walk(nested, "checkpoint.json", shapeObject(nested["checkpoint.json"]), v, path, 0)
			work = outerWork
			return err
		}
		if s["$ref"] == "tool-result.json#/$defs/mcpToolResult" {
			return nil
		}
		file, s = resolveShape(graph, file, s)
		if !shapeMatches(graph, file, s, v) {
			return errRecord
		}
		if _, numeric := v.(json.Number); numeric && integerShape(s) {
			if !safeRecordInteger(v) {
				return errRecord
			}
			if normalized != nil {
				f, _ := v.(json.Number).Float64()
				replacement := strconv.FormatInt(int64(f), 10)
				if v.(json.Number).String() != replacement {
					replacements[path] = replacement
				}
			}
		}
		branches := shapeList(s["oneOf"])
		if branches == nil && s["properties"] == nil {
			branches = shapeList(s["anyOf"])
		}
		if s["properties"] != nil && branches != nil {
			typed := false
			for _, b := range branches {
				_, branch := resolveShape(graph, file, shapeObject(b))
				typed = typed || branch["type"] != nil
			}
			if !typed {
				branches = nil
			}
		}
		if branches != nil {
			var first error
			for _, b := range branches {
				if !shapeMatches(graph, file, shapeObject(b), v) {
					continue
				}
				e := walk(graph, file, shapeObject(b), v, path, depth+1)
				if e == nil {
					return nil
				}
				var unknown *UnknownRecordMember
				if !errors.As(e, &unknown) {
					return e
				}
				if first == nil {
					first = e
				}
			}
			if first != nil {
				return first
			}
			return errRecord
		}
		if list, ok := v.([]any); ok {
			if child := shapeObject(s["items"]); child != nil {
				for i, x := range list {
					if e := walk(graph, file, child, x, pointer(path, fmt.Sprint(i)), depth+1); e != nil {
						return e
					}
				}
			}
			return nil
		}
		obj, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		type named struct {
			file  string
			shape map[string]any
		}
		props := map[string]named{}
		patterns := map[string]named{}
		add := func(f string, p map[string]any) {
			for k, x := range shapeObject(p["properties"]) {
				props[k] = named{f, shapeObject(x)}
			}
			for k, x := range shapeObject(p["patternProperties"]) {
				patterns[k] = named{f, shapeObject(x)}
			}
		}
		add(file, s)
		for _, b := range shapeList(s["allOf"]) {
			f, p := resolveShape(graph, file, shapeObject(b))
			add(f, p)
		}
		mapped := shapeObject(s["additionalProperties"])
		if len(props) == 0 && len(patterns) == 0 && mapped == nil && s["additionalProperties"] != false {
			return nil
		}
		keys := make([]string, 0, len(obj))
		for k := range obj {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		patternKeys := make([]string, 0, len(patterns))
		for k := range patterns {
			patternKeys = append(patternKeys, k)
		}
		sort.Strings(patternKeys)
		for _, key := range keys {
			child, found := props[key]
			if !found {
				for _, p := range patternKeys {
					if regexp.MustCompile(p).MatchString(key) {
						child, found = patterns[p], true
						break
					}
				}
			}
			if found {
				if e := walk(graph, child.file, child.shape, obj[key], pointer(path, key), depth+1); e != nil {
					return e
				}
				continue
			}
			if mapped != nil {
				if e := walk(graph, file, mapped, obj[key], pointer(path, key), depth+1); e != nil {
					return e
				}
				continue
			}
			return &UnknownRecordMember{Member: pointer(path, key), Kind: kind}
		}
		return nil
	}
	if err := walk(graph, file, shapeObject(graph[file]), value, "", 0); err != nil {
		return err
	}
	if normalized != nil {
		out, err := patchRecordIntegers(data, replacements)
		if err != nil {
			return errRecord
		}
		*normalized = out
	}
	return nil
}

// patchRecordIntegers changes only numeric token spans selected by the structural walk.
// In particular it never reserializes arbitrary values: retained-context digests bind
// their original member order, as well as their numeric/string representation.
func patchRecordIntegers(data []byte, replacements map[string]string) ([]byte, error) {
	if len(replacements) == 0 {
		return data, nil
	}
	type patch struct {
		start, end int
		text       string
	}
	patches := map[string]patch{}
	prefixes := map[string]bool{"": true}
	for path := range replacements {
		for at := strings.LastIndex(path, "/"); at > 0; at = strings.LastIndex(path[:at], "/") {
			prefixes[path[:at]] = true
		}
	}
	var scan func([]byte, string, int) error
	scan = func(raw []byte, path string, base int) error {
		if replacement, found := replacements[path]; found {
			patches[path] = patch{base, base + len(raw), replacement}
			return nil
		}
		if !prefixes[path] {
			return nil
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return errRecord
		}
		index := 0
		for decoder.More() {
			key := fmt.Sprint(index)
			if delimiter == '{' {
				token, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok = token.(string)
				if !ok {
					return errRecord
				}
			}
			var child json.RawMessage
			if err := decoder.Decode(&child); err != nil {
				return err
			}
			childPath := path + "/" + strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")
			if err := scan(child, childPath, base+int(decoder.InputOffset())-len(child)); err != nil {
				return err
			}
			index++
		}
		return nil
	}
	if err := scan(data, "", 0); err != nil {
		return nil, err
	}
	if len(patches) != len(replacements) {
		return nil, errRecord
	}
	ordered := make([]patch, 0, len(patches))
	for _, p := range patches {
		ordered = append(ordered, p)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].start < ordered[j].start })
	var out bytes.Buffer
	cursor := 0
	for _, p := range ordered {
		out.Write(data[cursor:p.start])
		out.WriteString(p.text)
		cursor = p.end
	}
	out.Write(data[cursor:])
	return out.Bytes(), nil
}

// StampEvent is the writer boundary; codecs keep historical records unchanged.
func StampEvent(event RunEvent) RunEvent {
	out := reflect.ValueOf(&event).Elem()
	for i := 0; i < out.NumField(); i++ {
		field := out.Field(i)
		if field.Kind() != reflect.Pointer || field.IsNil() {
			continue
		}
		copy := reflect.New(field.Elem().Type())
		copy.Elem().Set(field.Elem())
		version := copy.Elem().FieldByName("Version")
		if version.IsValid() {
			v := int64(EventVersion)
			version.Set(reflect.ValueOf(&v))
		}
		field.Set(copy)
	}
	return event
}
