package runcontext

// A catalogued tool's arguments held to the schema load_tool loaded (ADR-0006; the port of
// packages/harness/sourceArguments.ts).
//
// A catalogued tool is offered with an open schema, so nothing on the wire checks what the model
// sends; the boundary does, before the host's Execute. This is the subset of JSON Schema a tool's
// arguments are held to, and it only ever refuses what it understands: type, enum, const, required,
// properties, additionalProperties, items, minItems, maxItems, minimum, maximum, minLength,
// maxLength, allOf, anyOf and oneOf (read as anyOf: a value that matches two is not refused here).
// Any other keyword, $ref and pattern among them, is left to the host, which validates what it
// executes. The words are the TypeScript's, held by the same cases in arguments_test.go and
// sourceArguments.test.ts.

import (
	"math"
	"strings"
	"unicode/utf8"
)

// argumentSchemaDepth is how deep a schema is read; past it the schema asks nothing.
const argumentSchemaDepth = 16

func jsonKind(value *Value) string {
	if value == nil {
		return "null"
	}
	switch value.Kind() {
	case KindNull:
		return "null"
	case KindBool:
		return "boolean"
	case KindNumber:
		if n := value.Num(); n == math.Trunc(n) && !math.IsInf(n, 0) {
			return "integer"
		}
		return "number"
	case KindString:
		return "string"
	case KindArray:
		return "array"
	}
	return "object"
}

// isJSONType reports whether value is of the JSON Schema type (an integer is also a number).
func isJSONType(value *Value, kind string) bool {
	actual := jsonKind(value)
	return actual == kind || (kind == "number" && actual == "integer")
}

func pointerAt(path, name string) string {
	return path + "/" + strings.ReplaceAll(strings.ReplaceAll(name, "~", "~0"), "/", "~1")
}

func where(path string) string {
	if path == "" {
		return "the arguments"
	}
	return path
}

func numberText(value *Value) string { return jsNumber(value.Num()) }

// ArgumentProblem is the first way value does not match schema, in words, or "" where it does.
func ArgumentProblem(schema, value *Value) string { return argumentProblem(schema, value, "", 0) }

func argumentProblem(schema, value *Value, path string, depth int) string {
	if !schema.IsObject() || depth >= argumentSchemaDepth {
		return ""
	}
	if declared := schema.Get("type"); declared != nil {
		var types []string
		if declared.IsArray() {
			for _, item := range declared.Items() {
				types = append(types, item.Str())
			}
		} else {
			types = []string{declared.Str()}
		}
		matched := false
		for _, kind := range types {
			if isJSONType(value, kind) {
				matched = true
			}
		}
		if !matched {
			return where(path) + " must be " + strings.Join(types, " or ") + ", not " + jsonKind(value)
		}
	}
	if listed := schema.Get("enum"); listed.IsArray() {
		found := false
		for _, member := range listed.Items() {
			if CanonicalJSON(member) == CanonicalJSON(value) {
				found = true
			}
		}
		if !found {
			return where(path) + " is not one of the values the schema lists"
		}
	}
	if schema.Has("const") && CanonicalJSON(schema.Get("const")) != CanonicalJSON(value) {
		return where(path) + " is not the value the schema requires"
	}
	if parts := schema.Get("allOf"); parts.IsArray() {
		for _, part := range parts.Items() {
			if problem := argumentProblem(part, value, path, depth+1); problem != "" {
				return problem
			}
		}
	}
	for _, name := range []string{"anyOf", "oneOf"} {
		choice := schema.Get(name)
		if !choice.IsArray() || choice.Len() == 0 {
			continue
		}
		first, none := "", true
		for at, part := range choice.Items() {
			problem := argumentProblem(part, value, path, depth+1)
			if at == 0 {
				first = problem
			}
			if problem == "" {
				none = false
			}
		}
		if none {
			return where(path) + " matches none of the schema's alternatives (" + first + ")"
		}
	}
	if value != nil && value.Kind() == KindNumber {
		if bound := schema.Get("minimum"); bound != nil && bound.Kind() == KindNumber && value.Num() < bound.Num() {
			return where(path) + " is below the schema's minimum of " + numberText(bound)
		}
		if bound := schema.Get("maximum"); bound != nil && bound.Kind() == KindNumber && value.Num() > bound.Num() {
			return where(path) + " is above the schema's maximum of " + numberText(bound)
		}
	}
	if value != nil && value.Kind() == KindString {
		length := utf8.RuneCountInString(value.Str())
		if bound := schema.Get("minLength"); bound != nil && bound.Kind() == KindNumber && float64(length) < bound.Num() {
			return where(path) + " is shorter than the schema's " + numberText(bound) + " characters"
		}
		if bound := schema.Get("maxLength"); bound != nil && bound.Kind() == KindNumber && float64(length) > bound.Num() {
			return where(path) + " is longer than the schema's " + numberText(bound) + " characters"
		}
	}
	if value.IsArray() {
		if bound := schema.Get("minItems"); bound != nil && bound.Kind() == KindNumber && float64(value.Len()) < bound.Num() {
			return where(path) + " has fewer than the schema's " + numberText(bound) + " items"
		}
		if bound := schema.Get("maxItems"); bound != nil && bound.Kind() == KindNumber && float64(value.Len()) > bound.Num() {
			return where(path) + " has more than the schema's " + numberText(bound) + " items"
		}
		if items := schema.Get("items"); items.IsObject() {
			for at, item := range value.Items() {
				if problem := argumentProblem(items, item, pointerAt(path, itoa(at)), depth+1); problem != "" {
					return problem
				}
			}
		}
	}
	if value.IsObject() {
		if required := schema.Get("required"); required.IsArray() {
			for _, name := range required.Items() {
				if name.IsString() && !value.Has(name.Str()) {
					return pointerAt(path, name.Str()) + " is required and missing"
				}
			}
		}
		properties := schema.Get("properties")
		for _, name := range value.Keys() {
			member := value.Get(name)
			switch {
			case properties.IsObject() && properties.Has(name):
				if problem := argumentProblem(properties.Get(name), member, pointerAt(path, name), depth+1); problem != "" {
					return problem
				}
			case schema.Get("additionalProperties") != nil && schema.Get("additionalProperties").Kind() == KindBool && !schema.Get("additionalProperties").Truth() && !schema.Get("patternProperties").IsObject():
				return pointerAt(path, name) + " is not a member the schema names"
			case schema.Get("additionalProperties").IsObject():
				if problem := argumentProblem(schema.Get("additionalProperties"), member, pointerAt(path, name), depth+1); problem != "" {
					return problem
				}
			}
		}
	}
	return ""
}
