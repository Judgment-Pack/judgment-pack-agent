// Package contractgen writes the Go types of the wire contract from the JSON schemas in
// contract/schemas. It is the generator ADR-0004 names: it lives in this module, so the module's
// own commit pins it, and contract/roundtrip_test.go holds the committed output to what it writes.
//
// It writes types, not a validator. The schemas stay the validator (tests/test_contract.py holds
// every vector to its schema); what the types keep is the shape, and they keep it whole:
//
//   - an object with properties is a struct, its members in the schema's order; a required member
//     is a value, an optional one a pointer (or a slice, map, raw value or Nullable, which keep an
//     empty value apart from an absent one through omitzero);
//   - an open object (additionalProperties not false) keeps the members it does not name in
//     AdditionalMembers and writes them back; a closed one refuses them on decode;
//   - oneOf and anyOf over typed branches are a union struct with one pointer per branch, chosen
//     on decode by the member every object branch fixes with a distinct const where there is one,
//     and otherwise by the JSON kind and the first branch that decodes; branches that only add
//     constraints (required, not, if/then) are not types and are left to the schema;
//   - a union whose branches the value cannot tell apart, because a member of another object
//     decides it (keyedUnions: the delegated task's report, decided by its profile's report), also
//     gets a decode keyed by that object, Decode<Union>, which is the one to read it with;
//   - allOf over a $ref and object branches is one struct with the members of all of them;
//   - a type list of one type and "null" is Nullable[T]; a node with no type is json.RawMessage.
//
// Every schema root and every $defs entry is registered by the address a vector names it with
// ("run-event.json", "budget.json#/$defs/hostPart"), so a test can decode any vector.
package contractgen

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// Generate reads every *.json schema in dir and returns the formatted Go source of package pkg.
func Generate(dir, pkg string) ([]byte, error) {
	g, err := load(dir)
	if err != nil {
		return nil, err
	}
	return g.run(pkg)
}

// Addresses returns, for every struct and union type Generate writes, the schema node it is
// written from, as a file and a JSON pointer ("tool-call.json#", "tool-result.json#/$defs/imagePart").
// It is how a test reads a type's members from the schema itself rather than from the type.
func Addresses(dir string) (map[string]string, error) {
	g, err := load(dir)
	if err != nil {
		return nil, err
	}
	if _, err := g.run("contract"); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(g.named))
	for key, name := range g.named {
		out[name] = key
	}
	return out, nil
}

func load(dir string) (*generator, error) {
	g := &generator{schemas: map[string]*schemaFile{}, named: map[string]string{}, emitted: map[string]bool{}}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		var root map[string]any
		if err := json.Unmarshal(raw, &root); err != nil {
			return nil, fmt.Errorf("%s: %w", entry.Name(), err)
		}
		order, err := keyOrder(raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", entry.Name(), err)
		}
		g.schemas[entry.Name()] = &schemaFile{name: entry.Name(), root: root, order: order}
		g.files = append(g.files, entry.Name())
	}
	sort.Strings(g.files)
	return g, nil
}

type schemaFile struct {
	name  string
	root  map[string]any
	order map[string][]string // JSON pointer of an object -> its keys in document order
}

// loc is one schema node: a file and a JSON pointer within it ("" is the root).
type loc struct{ file, ptr string }

func (l loc) key() string { return l.file + "#" + l.ptr }

type generator struct {
	schemas  map[string]*schemaFile
	files    []string
	named    map[string]string // loc key -> Go type name, for structs and unions
	emitted  map[string]bool
	queue    []loc
	decls    map[string]string // Go type name -> declaration source
	registry []registryEntry
	err      error
}

type registryEntry struct{ address, expr string }

func (g *generator) fail(format string, args ...any) {
	if g.err == nil {
		g.err = fmt.Errorf(format, args...)
	}
}

func (g *generator) run(pkg string) ([]byte, error) {
	g.decls = map[string]string{}
	for _, file := range g.files {
		root := loc{file, ""}
		g.registry = append(g.registry, registryEntry{file, g.typeExpr(root)})
		defs, _ := g.schemas[file].root["$defs"].(map[string]any)
		for _, name := range g.keys(loc{file, "/$defs"}, defs) {
			def := loc{file, "/$defs/" + escape(name)}
			g.registry = append(g.registry, registryEntry{file + "#/$defs/" + name, g.typeExpr(def)})
		}
	}
	for len(g.queue) > 0 {
		next := g.queue[0]
		g.queue = g.queue[1:]
		g.emit(next)
	}
	if g.err != nil {
		return nil, g.err
	}
	var out bytes.Buffer
	fmt.Fprintf(&out, "// Code generated by contractgen from contract/schemas; DO NOT EDIT.\n\n")
	fmt.Fprintf(&out, "package %s\n\nimport \"encoding/json\"\n\n", pkg)
	out.WriteString("// Registry maps the address a vector names its schema by to a new value of its Go type.\n")
	out.WriteString("var Registry = map[string]func() any{\n")
	for _, entry := range g.registry {
		fmt.Fprintf(&out, "\t%q: func() any { return new(%s) },\n", entry.address, entry.expr)
	}
	out.WriteString("}\n\n")
	names := make([]string, 0, len(g.decls))
	for name := range g.decls {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		out.WriteString(g.decls[name])
		out.WriteString("\n")
	}
	out.WriteString("var _ json.RawMessage\n")
	formatted, err := format.Source(out.Bytes())
	if err != nil {
		return nil, fmt.Errorf("format: %w\n%s", err, out.String())
	}
	return formatted, nil
}

// node returns the schema object at l.
func (g *generator) node(l loc) map[string]any {
	file, ok := g.schemas[l.file]
	if !ok {
		g.fail("no schema %s", l.file)
		return map[string]any{}
	}
	var current any = file.root
	if l.ptr != "" {
		for _, raw := range strings.Split(l.ptr[1:], "/") {
			segment := unescape(raw)
			switch value := current.(type) {
			case map[string]any:
				current = value[segment]
			case []any:
				index, err := strconv.Atoi(segment)
				if err != nil || index >= len(value) {
					g.fail("bad pointer %s", l.key())
					return map[string]any{}
				}
				current = value[index]
			default:
				g.fail("bad pointer %s", l.key())
				return map[string]any{}
			}
		}
	}
	object, ok := current.(map[string]any)
	if !ok {
		g.fail("%s is not a schema object", l.key())
		return map[string]any{}
	}
	return object
}

// keys returns an object's keys in document order.
func (g *generator) keys(l loc, object map[string]any) []string {
	ordered := g.schemas[l.file].order[l.ptr]
	if len(ordered) != len(object) {
		ordered = make([]string, 0, len(object))
		for key := range object {
			ordered = append(ordered, key)
		}
		sort.Strings(ordered)
	}
	return ordered
}

// resolve follows $ref from a node at l, repeatedly.
func (g *generator) resolve(l loc) loc {
	for i := 0; i < 32; i++ {
		ref, ok := g.node(l)["$ref"].(string)
		if !ok {
			return l
		}
		l = g.target(l.file, ref)
	}
	g.fail("$ref chain too long at %s", l.key())
	return l
}

func (g *generator) target(file, ref string) loc {
	path, fragment, _ := strings.Cut(ref, "#")
	if path != "" {
		file = path
	}
	return loc{file, fragment}
}

// isNamed reports whether l is a schema root or a $defs entry.
func isNamed(l loc) bool {
	return l.ptr == "" || (strings.HasPrefix(l.ptr, "/$defs/") && strings.Count(l.ptr, "/") == 2)
}

// nameFor derives a Go type name from a location: the file's stem, then each meaningful segment.
func nameFor(l loc) string {
	name := pascal(strings.TrimSuffix(l.file, ".json"))
	if l.ptr == "" {
		return name
	}
	segments := strings.Split(l.ptr[1:], "/")
	for i := 0; i < len(segments); i++ {
		segment := unescape(segments[i])
		switch segment {
		case "$defs", "properties":
			continue
		case "items":
			name += "Item"
		case "additionalProperties":
			name += "Value"
		case "oneOf", "anyOf", "allOf":
			if i+1 < len(segments) {
				index, _ := strconv.Atoi(segments[i+1])
				name += "Option" + strconv.Itoa(index+1)
				i++
			}
		default:
			name += pascal(segment)
		}
	}
	return name
}

// typedBranches returns the branches of a oneOf/anyOf that are types rather than constraints.
func (g *generator) typedBranches(l loc, keyword string) []loc {
	list, _ := g.node(l)[keyword].([]any)
	var typed []loc
	for i := range list {
		branch := loc{l.file, fmt.Sprintf("%s/%s/%d", l.ptr, keyword, i)}
		n := g.node(branch)
		_, ref := n["$ref"]
		_, kind := n["type"]
		_, constant := n["const"]
		_, enum := n["enum"]
		if !ref && !kind && !constant && !enum {
			return nil
		}
		typed = append(typed, branch)
	}
	return typed
}

func (g *generator) unionBranches(l loc) []loc {
	if typed := g.typedBranches(l, "oneOf"); len(typed) > 0 {
		return typed
	}
	return g.typedBranches(l, "anyOf")
}

// mergeable reports whether allOf at l merges into one struct: every branch a $ref or an object.
func (g *generator) mergeable(l loc) bool {
	list, _ := g.node(l)["allOf"].([]any)
	if len(list) == 0 {
		return false
	}
	for i := range list {
		n := g.node(loc{l.file, fmt.Sprintf("%s/allOf/%d", l.ptr, i)})
		_, ref := n["$ref"]
		_, properties := n["properties"]
		if !ref && !properties {
			return false
		}
	}
	return true
}

func (g *generator) kind(l loc) string {
	n := g.node(l)
	if _, ok := n["$ref"]; ok {
		return "ref"
	}
	if len(g.unionBranches(l)) > 0 {
		return "union"
	}
	if g.mergeable(l) {
		return "struct"
	}
	if _, ok := n["properties"]; ok {
		return "struct"
	}
	return "other"
}

// typeExpr returns the Go type of the node at l, queueing a named type where it needs one.
func (g *generator) typeExpr(l loc) string {
	n := g.node(l)
	if ref, ok := n["$ref"].(string); ok {
		return g.typeExpr(g.target(l.file, ref))
	}
	switch g.kind(l) {
	case "union", "struct":
		return g.nameOf(l)
	}
	switch kind := n["type"].(type) {
	case []any:
		var others []string
		null := false
		for _, item := range kind {
			if item == "null" {
				null = true
			} else if s, ok := item.(string); ok {
				others = append(others, s)
			}
		}
		if null && len(others) == 1 {
			return "Nullable[" + scalar(others[0], n, func() string { return g.collection(l, others[0]) }) + "]"
		}
		return "json.RawMessage"
	case string:
		return scalar(kind, n, func() string { return g.collection(l, kind) })
	}
	if constant, ok := n["const"]; ok {
		return goKindOf(constant)
	}
	if enum, ok := n["enum"].([]any); ok && len(enum) > 0 {
		first := goKindOf(enum[0])
		for _, item := range enum[1:] {
			if goKindOf(item) != first {
				return "json.RawMessage"
			}
		}
		return first
	}
	return "json.RawMessage"
}

func scalar(kind string, _ map[string]any, collection func() string) string {
	switch kind {
	case "string":
		return "string"
	case "integer":
		return "int64"
	case "number":
		return "float64"
	case "boolean":
		return "bool"
	case "object", "array":
		return collection()
	}
	return "json.RawMessage"
}

// collection types an object without properties (a map) or an array.
func (g *generator) collection(l loc, kind string) string {
	n := g.node(l)
	if kind == "array" {
		if _, ok := n["items"].(map[string]any); ok {
			return "[]" + g.typeExpr(loc{l.file, l.ptr + "/items"})
		}
		return "[]json.RawMessage"
	}
	if _, ok := n["additionalProperties"].(map[string]any); ok {
		return "map[string]" + g.typeExpr(loc{l.file, l.ptr + "/additionalProperties"})
	}
	return "map[string]json.RawMessage"
}

func goKindOf(value any) string {
	switch v := value.(type) {
	case string:
		return "string"
	case bool:
		return "bool"
	case float64:
		if v == float64(int64(v)) {
			return "int64"
		}
		return "float64"
	}
	return "json.RawMessage"
}

func (g *generator) nameOf(l loc) string {
	if name, ok := g.named[l.key()]; ok {
		return name
	}
	name := nameFor(l)
	for taken := true; taken; {
		taken = false
		for key, other := range g.named {
			if other == name && key != l.key() {
				name += "_"
				taken = true
			}
		}
	}
	g.named[l.key()] = name
	g.queue = append(g.queue, l)
	return name
}

type field struct {
	member, goName, expr string
	required             bool
}

// structFields collects a struct's members, merging allOf branches, in document order.
func (g *generator) structFields(l loc, seen map[string]bool) ([]field, map[string]bool) {
	n := g.node(l)
	required := map[string]bool{}
	if list, ok := n["required"].([]any); ok {
		for _, item := range list {
			if s, ok := item.(string); ok {
				required[s] = true
			}
		}
	}
	var fields []field
	if all, ok := n["allOf"].([]any); ok && g.mergeable(l) {
		for i := range all {
			branch := g.resolve(loc{l.file, fmt.Sprintf("%s/allOf/%d", l.ptr, i)})
			more, req := g.structFields(branch, seen)
			fields = append(fields, more...)
			for name := range req {
				required[name] = true
			}
		}
	}
	properties, _ := n["properties"].(map[string]any)
	for _, member := range g.keys(loc{l.file, l.ptr + "/properties"}, properties) {
		if seen[member] {
			continue
		}
		seen[member] = true
		fields = append(fields, field{member: member, expr: g.typeExpr(loc{l.file, l.ptr + "/properties/" + escape(member)})})
	}
	for i := range fields {
		fields[i].required = required[fields[i].member]
	}
	return fields, required
}

func (g *generator) emit(l loc) {
	name := g.named[l.key()]
	if g.emitted[name] {
		return
	}
	g.emitted[name] = true
	if g.kind(l) == "union" {
		g.emitUnion(l, name)
		return
	}
	g.emitStruct(l, name)
}

func (g *generator) describe(l loc) string {
	description, _ := g.node(l)["description"].(string)
	address := l.file
	if l.ptr != "" {
		address += "#" + l.ptr
	}
	text := "From " + address + "."
	if description != "" {
		text += " " + description
	}
	var b strings.Builder
	for _, line := range wrap(text, 96) {
		b.WriteString("// " + line + "\n")
	}
	return b.String()
}

func (g *generator) emitStruct(l loc, name string) {
	fields, _ := g.structFields(l, map[string]bool{})
	patterns, _ := g.node(l)["patternProperties"].(map[string]any)
	closed := g.node(l)["additionalProperties"] == false && len(patterns) == 0
	var allowed []string
	for pattern := range patterns {
		allowed = append(allowed, strconv.Quote(pattern))
	}
	sort.Strings(allowed)
	var b strings.Builder
	b.WriteString(g.describe(l))
	fmt.Fprintf(&b, "type %s struct {\n", name)
	used := map[string]bool{"AdditionalMembers": !closed}
	var members []string
	for _, f := range fields {
		goName := pascal(f.member)
		for used[goName] {
			goName += "_"
		}
		used[goName] = true
		expr := f.expr
		tag := f.member
		if !f.required {
			if !(strings.HasPrefix(expr, "[]") || strings.HasPrefix(expr, "map[") || expr == "json.RawMessage" || strings.HasPrefix(expr, "Nullable[")) {
				expr = "*" + expr
			}
			tag += ",omitzero"
		}
		fmt.Fprintf(&b, "\t%s %s `json:%q`\n", goName, expr, tag)
		members = append(members, strconv.Quote(f.member))
	}
	if !closed {
		b.WriteString("\t// AdditionalMembers holds the members the schema does not name; the object is open.\n")
		b.WriteString("\tAdditionalMembers map[string]json.RawMessage `json:\"-\"`\n")
	}
	b.WriteString("}\n\n")
	known := strings.ToLower(name[:1]) + name[1:] + "Members"
	fmt.Fprintf(&b, "var %s = []string{%s}\n\n", known, strings.Join(members, ", "))
	fmt.Fprintf(&b, "// UnmarshalJSON decodes a %s object", name)
	if closed {
		b.WriteString(" and refuses a member the closed schema does not name.\n")
	} else {
		b.WriteString(" and keeps the members the schema does not name.\n")
	}
	fmt.Fprintf(&b, "func (v *%s) UnmarshalJSON(data []byte) error {\n", name)
	if kind := recordKind(l); kind != "" {
		fmt.Fprintf(&b, "\tvar recordErr error; data, recordErr = recordDecodeBytes(%q, data); if recordErr != nil { return recordErr }\n", kind)
	}
	fmt.Fprintf(&b, "\ttype plain %s\n\tvar p plain\n", name)
	fmt.Fprintf(&b, "\textra, err := decodeObject(data, &p, %s, %t)\n", known, closed)
	fmt.Fprintf(&b, "\tif err != nil {\n\t\treturn fmt.Errorf(\"%s: %%w\", err)\n\t}\n\t*v = %s(p)\n", name, name)
	if len(allowed) > 0 {
		fmt.Fprintf(&b, "\tif err := checkPatternMembers(extra, []string{%s}); err != nil { return err }\n", strings.Join(allowed, ", "))
	}
	if !closed {
		b.WriteString("\tv.AdditionalMembers = extra\n")
	} else {
		b.WriteString("\t_ = extra\n")
	}
	b.WriteString("\treturn nil\n}\n\n")
	fmt.Fprintf(&b, "// MarshalJSON encodes a %s object", name)
	if !closed {
		b.WriteString(", with the members the schema does not name after the ones it does.\n")
	} else {
		b.WriteString(".\n")
	}
	fmt.Fprintf(&b, "func (v %s) MarshalJSON() ([]byte, error) {\n\ttype plain %s\n", name, name)
	if closed {
		b.WriteString("\treturn encodeObject(plain(v), nil)\n}\n")
	} else {
		b.WriteString("\treturn encodeObject(plain(v), v.AdditionalMembers)\n}\n")
	}
	for _, f := range fields {
		for _, keyed := range keyedUnions {
			file, ptr, _ := strings.Cut(keyed.union, "#")
			union := g.typeExpr(loc{file, ptr})
			if f.expr != union {
				continue
			}
			file, ptr, _ = strings.Cut(keyed.key, "#")
			key := g.typeExpr(loc{file, ptr})
			goName := pascal(f.member)
			fmt.Fprintf(&b, "\n// %sFor decodes %s in the shape key's %s member names (Decode%s): the value alone\n// cannot say which (%s).\n", goName, goName, keyed.member, union, keyed.union)
			fmt.Fprintf(&b, "func (v %s) %sFor(key %s) (*%s, error) {\n", name, goName, key, union)
			if !f.required {
				fmt.Fprintf(&b, "\tif v.%s == nil {\n\t\treturn nil, nil\n\t}\n", goName)
			}
			fmt.Fprintf(&b, "\tdata, err := json.Marshal(v.%s)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tdecoded, err := Decode%s(key, data)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn &decoded, nil\n}\n", goName, union)
		}
	}
	g.decls[name] = strings.Replace(b.String(), "fmt.Errorf", "errorf", -1)
}

type branch struct {
	l        loc
	goName   string
	expr     string
	jsonKind string // object, string, number, boolean, array
	tag      string // discriminator value, for object branches when there is one
}

func (g *generator) jsonKind(l loc) string {
	target := g.resolve(l)
	switch g.kind(target) {
	case "union":
		return "any"
	case "struct":
		return "object"
	}
	n := g.node(target)
	if kind, ok := n["type"].(string); ok {
		switch kind {
		case "integer", "number":
			return "number"
		}
		return kind
	}
	switch goKindOf(n["const"]) {
	case "string":
		return "string"
	}
	if enum, ok := n["enum"].([]any); ok && len(enum) > 0 {
		switch goKindOf(enum[0]) {
		case "string":
			return "string"
		case "bool":
			return "boolean"
		case "int64", "float64":
			return "number"
		}
	}
	return "any"
}

// constOf returns the const string a struct node fixes member to, or "".
func (g *generator) constOf(l loc, member string) string {
	target := g.resolve(l)
	fields, _ := g.structFields(target, map[string]bool{})
	for _, f := range fields {
		if f.member != member {
			continue
		}
		var property loc
		found := false
		g.walkProperties(target, func(owner loc, name string) {
			if name == member && !found {
				property = loc{owner.file, owner.ptr + "/properties/" + escape(name)}
				found = true
			}
		})
		if !found {
			return ""
		}
		n := g.node(g.resolve(property))
		if s, ok := n["const"].(string); ok {
			return s
		}
		if enum, ok := n["enum"].([]any); ok && len(enum) == 1 {
			if s, ok := enum[0].(string); ok {
				return s
			}
		}
	}
	return ""
}

func (g *generator) walkProperties(l loc, visit func(owner loc, name string)) {
	n := g.node(l)
	if all, ok := n["allOf"].([]any); ok && g.mergeable(l) {
		for i := range all {
			g.walkProperties(g.resolve(loc{l.file, fmt.Sprintf("%s/allOf/%d", l.ptr, i)}), visit)
		}
	}
	properties, _ := n["properties"].(map[string]any)
	for _, name := range g.keys(loc{l.file, l.ptr + "/properties"}, properties) {
		visit(l, name)
	}
}

func (g *generator) emitUnion(l loc, name string) {
	locs := g.unionBranches(l)
	branches := make([]branch, 0, len(locs))
	used := map[string]bool{}
	for i, at := range locs {
		b := branch{l: at, expr: g.typeExpr(at), jsonKind: g.jsonKind(at)}
		goName := strings.TrimPrefix(b.expr, name)
		if goName == "" || strings.ContainsAny(goName, "[]*.") || !unicode.IsUpper(rune(goName[0])) {
			goName = "Option" + strconv.Itoa(i+1)
		}
		if b.expr == "string" || b.expr == "bool" || b.expr == "int64" || b.expr == "float64" {
			goName = pascal(b.expr)
		}
		for used[goName] {
			goName += "_"
		}
		used[goName] = true
		b.goName = goName
		branches = append(branches, b)
	}
	// The discriminator: a member every object branch fixes to a distinct const string.
	discriminator := ""
	var objects []int
	for i, b := range branches {
		if b.jsonKind == "object" {
			objects = append(objects, i)
		}
	}
	if len(objects) > 1 {
		first, _ := g.structFields(g.resolve(branches[objects[0]].l), map[string]bool{})
		for _, candidate := range first {
			values := map[string]bool{}
			ok := true
			for _, i := range objects {
				value := g.constOf(branches[i].l, candidate.member)
				if value == "" || values[value] {
					ok = false
					break
				}
				values[value] = true
			}
			if ok {
				discriminator = candidate.member
				for _, i := range objects {
					branches[i].tag = g.constOf(branches[i].l, candidate.member)
				}
				break
			}
		}
	}

	keyed, isKeyed := keyedUnionAt(l)
	untagged := 0
	for _, br := range branches {
		if br.tag == "" {
			untagged++
		}
	}
	// Where two members could both decode one value, the decode tries every one. One that decodes
	// it is set; of several, the one closed branch among them (it describes the value exactly, an
	// open one only admits it); otherwise none is set and Raw keeps the value whole.
	ambiguous := untagged > 1
	var b strings.Builder
	b.WriteString(g.describe(l))
	fmt.Fprintf(&b, "//\n// A union: exactly one member is set.")
	if discriminator != "" {
		fmt.Fprintf(&b, " An object is decoded by its %q member.", discriminator)
	}
	if ambiguous {
		b.WriteString(" Where more than one member decodes the value, the one closed member among them is set; where there is not exactly one, none is set and Raw keeps the value whole.")
	}
	if isKeyed {
		fmt.Fprintf(&b, " Its branches cannot be told apart by the value: Decode%s decodes it by the %s that decides it, and a type that holds it reads it with its <member>For method.", name, keyed.member)
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "type %s struct {\n", name)
	for _, br := range branches {
		if br.tag != "" {
			fmt.Fprintf(&b, "\t// %s is set where %s is %q.\n", br.goName, discriminator, br.tag)
		}
		fmt.Fprintf(&b, "\t%s *%s\n", br.goName, br.expr)
	}
	if ambiguous {
		b.WriteString("\t// Raw is the value where more than one member decodes it; no member is set then.\n\tRaw json.RawMessage\n")
	}
	b.WriteString("}\n\n")
	fmt.Fprintf(&b, "// UnmarshalJSON sets the one member the value decodes as.\nfunc (v *%s) UnmarshalJSON(data []byte) error {\n", name)
	if kind := recordKind(l); kind != "" {
		fmt.Fprintf(&b, "\tvar recordErr error; data, recordErr = recordDecodeBytes(%q, data); if recordErr != nil { return recordErr }\n", kind)
	}
	fmt.Fprintf(&b, "\t*v = %s{}\n\tkind := jsonKind(data)\n", name)
	if discriminator != "" {
		fmt.Fprintf(&b, "\tif kind == \"object\" {\n\t\ttag, err := memberString(data, %q)\n\t\tif err != nil {\n\t\t\treturn errorf(\"%s: %%w\", err)\n\t\t}\n\t\tswitch tag {\n", discriminator, name)
		for _, br := range branches {
			if br.tag == "" {
				continue
			}
			fmt.Fprintf(&b, "\t\tcase %q:\n\t\t\tv.%s = new(%s)\n\t\t\treturn json.Unmarshal(data, v.%s)\n", br.tag, br.goName, br.expr, br.goName)
		}
		fmt.Fprintf(&b, "\t\t}\n\t\treturn errorf(\"%s: no member has %s %%q\", tag)\n\t}\n", name, discriminator)
	}
	if ambiguous {
		b.WriteString("\tdecoded, closed := 0, 0\n\tvar first, firstClosed func()\n")
	}
	for _, br := range branches {
		if br.tag != "" {
			continue
		}
		cond := fmt.Sprintf("kind == %q", br.jsonKind)
		if br.jsonKind == "any" {
			cond = "true"
		}
		if br.jsonKind == "object" {
			// An object branch is a candidate only where every member it requires is present, so an
			// open branch does not take an object that another branch describes.
			_, required := g.structFields(g.resolve(br.l), map[string]bool{})
			var names []string
			for member := range required {
				names = append(names, strconv.Quote(member))
			}
			sort.Strings(names)
			if len(names) > 0 {
				cond += " && hasMembers(data, " + strings.Join(names, ", ") + ")"
			}
		}
		if ambiguous {
			isClosed := ""
			if g.node(g.resolve(br.l))["additionalProperties"] == false && br.jsonKind == "object" {
				isClosed = "\t\t\tclosed++\n\t\t\tif firstClosed == nil {\n\t\t\t\tfirstClosed = set\n\t\t\t}\n"
			}
			fmt.Fprintf(&b, "\tif %s {\n\t\tvar candidate %s\n\t\tif err := json.Unmarshal(data, &candidate); err == nil {\n\t\t\tset := func() { v.%s = &candidate }\n\t\t\tdecoded++\n\t\t\tif first == nil {\n\t\t\t\tfirst = set\n\t\t\t}\n%s\t\t}\n\t}\n", cond, br.expr, br.goName, isClosed)
		} else {
			fmt.Fprintf(&b, "\tif %s {\n\t\tvar candidate %s\n\t\tif err := json.Unmarshal(data, &candidate); err == nil {\n\t\t\tv.%s = &candidate\n\t\t\treturn nil\n\t\t}\n\t}\n", cond, br.expr, br.goName)
		}
	}
	if ambiguous {
		fmt.Fprintf(&b, "\tswitch {\n\tcase decoded == 0:\n\t\treturn errorf(\"%s: the value is none of the union's members\")\n\tcase decoded == 1:\n\t\tfirst()\n\t\treturn nil\n\tcase closed == 1:\n\t\tfirstClosed()\n\t\treturn nil\n\t}\n\t*v = %s{Raw: append(json.RawMessage(nil), data...)}\n\treturn nil\n}\n\n", name, name)
	} else {
		fmt.Fprintf(&b, "\treturn errorf(\"%s: the value is none of the union's members\")\n}\n\n", name)
	}
	fmt.Fprintf(&b, "// MarshalJSON encodes the one member that is set.\nfunc (v %s) MarshalJSON() ([]byte, error) {\n\tvar set []any\n", name)
	for _, br := range branches {
		fmt.Fprintf(&b, "\tif v.%s != nil {\n\t\tset = append(set, v.%s)\n\t}\n", br.goName, br.goName)
	}
	if ambiguous {
		b.WriteString("\tif len(set) == 0 && v.Raw != nil {\n\t\treturn v.Raw, nil\n\t}\n")
	}
	fmt.Fprintf(&b, "\tif len(set) != 1 {\n\t\treturn nil, errorf(\"%s: %%d members are set; a union holds exactly one\", len(set))\n\t}\n\treturn json.Marshal(set[0])\n}\n", name)
	if isKeyed {
		g.emitKeyedDecode(&b, l, name, keyed, branches)
	}
	g.decls[name] = b.String()
}

// keyedUnion is a union decided by a member of another object, not by its value.
type keyedUnion struct {
	union   string // the union's address
	key     string // the address of the object whose member decides it
	member  string // that member: a string enum, one value per branch
	absent  string // the value the member stands for where it is absent
	pattern string // the $defs name of a value's branch, from the value in lower camel case
}

// keyedUnions are the contract's unions whose branches only another object tells apart: the
// delegated task's report (agent#16), whose four closed branches carry the same member names, and
// whose shape is the one its profile's report member names, analyze where absent.
var keyedUnions = []keyedUnion{{
	union:   "delegated-task.json#/$defs/report",
	key:     "delegated-task.json#/$defs/profile",
	member:  "report",
	absent:  "analyze",
	pattern: "%sReport",
}}

func keyedUnionAt(l loc) (keyedUnion, bool) {
	for _, keyed := range keyedUnions {
		if keyed.union == l.key() {
			return keyed, true
		}
	}
	return keyedUnion{}, false
}

// emitKeyedDecode writes Decode<Union>(key, data): the branch the key object's member names.
func (g *generator) emitKeyedDecode(b *strings.Builder, l loc, name string, keyed keyedUnion, branches []branch) {
	file, ptr, _ := strings.Cut(keyed.key, "#")
	keyLoc := loc{file, ptr}
	keyType := g.typeExpr(keyLoc)
	member := g.node(g.resolve(loc{file, ptr + "/properties/" + escape(keyed.member)}))
	values, _ := member["enum"].([]any)
	required := false
	if list, ok := g.node(keyLoc)["required"].([]any); ok {
		for _, item := range list {
			required = required || item == keyed.member
		}
	}
	field := pascal(keyed.member)
	fmt.Fprintf(b, "\n// Decode%s decodes a %s in the shape a %s's %s member names (%q where it is absent): the\n", name, name, keyType, keyed.member, keyed.absent)
	fmt.Fprintf(b, "// branches carry the same member names, so the value cannot say which it is (%s).\n", keyed.union)
	fmt.Fprintf(b, "func Decode%s(key %s, data []byte) (%s, error) {\n", name, keyType, name)
	if required {
		fmt.Fprintf(b, "\tkind := key.%s\n", field)
	} else {
		fmt.Fprintf(b, "\tkind := %q\n\tif key.%s != nil {\n\t\tkind = *key.%s\n\t}\n", keyed.absent, field, field)
	}
	fmt.Fprintf(b, "\tvar v %s\n\tswitch kind {\n", name)
	matched := map[int]bool{}
	absentNamed := false
	for _, raw := range values {
		value, _ := raw.(string)
		absentNamed = absentNamed || value == keyed.absent
		lower := pascal(value)
		lower = strings.ToLower(lower[:1]) + lower[1:]
		want := "/$defs/" + fmt.Sprintf(keyed.pattern, lower)
		found := -1
		for i, br := range branches {
			if target := g.resolve(br.l); target.file == l.file && target.ptr == want {
				found = i
			}
		}
		if found < 0 || matched[found] {
			g.fail("%s: %s %q names no branch of its own (%s)", keyed.union, keyed.member, value, want)
			continue
		}
		matched[found] = true
		br := branches[found]
		fmt.Fprintf(b, "\tcase %q:\n\t\tv.%s = new(%s)\n\t\tif err := json.Unmarshal(data, v.%s); err != nil {\n\t\t\treturn %s{}, errorf(\"%s as %%s: %%w\", kind, err)\n\t\t}\n\t\treturn v, nil\n",
			value, br.goName, br.expr, br.goName, name, name)
	}
	if len(matched) != len(branches) || !absentNamed {
		g.fail("%s: the %s values and the branches do not match one to one", keyed.union, keyed.member)
	}
	fmt.Fprintf(b, "\t}\n\treturn %s{}, errorf(\"%s: %s %%q names none of its shapes\", kind)\n}\n", name, name, keyed.member)
}

// keyOrder records, for every object in a JSON document, its keys in document order.
func keyOrder(raw []byte) (map[string][]string, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	order := map[string][]string{}
	var walk func(ptr string) error
	walk = func(ptr string) error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			keys := []string{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key := keyToken.(string)
				keys = append(keys, key)
				if err := walk(ptr + "/" + escape(key)); err != nil {
					return err
				}
			}
			order[ptr] = keys
		case '[':
			for i := 0; decoder.More(); i++ {
				if err := walk(ptr + "/" + strconv.Itoa(i)); err != nil {
					return err
				}
			}
		}
		_, err = decoder.Token()
		return err
	}
	return order, walk("")
}

func escape(segment string) string {
	return strings.ReplaceAll(strings.ReplaceAll(segment, "~", "~0"), "/", "~1")
}

func unescape(segment string) string {
	return strings.ReplaceAll(strings.ReplaceAll(segment, "~1", "/"), "~0", "~")
}

var initialisms = map[string]string{"id": "ID", "url": "URL", "json": "JSON", "mcp": "MCP", "api": "API", "http": "HTTP", "sse": "SSE", "ui": "UI"}

// pascal turns a member, file or definition name into an exported Go identifier.
func pascal(s string) string {
	var words []string
	var current []rune
	runes := []rune(s)
	flush := func() {
		if len(current) > 0 {
			words = append(words, string(current))
			current = nil
		}
	}
	for i, r := range runes {
		switch {
		case !unicode.IsLetter(r) && !unicode.IsDigit(r):
			flush()
		case unicode.IsUpper(r) && i > 0 && (unicode.IsLower(runes[i-1]) || (i+1 < len(runes) && unicode.IsLower(runes[i+1]) && unicode.IsUpper(runes[i-1]))):
			flush()
			current = append(current, r)
		default:
			current = append(current, r)
		}
	}
	flush()
	var b strings.Builder
	for _, word := range words {
		lower := strings.ToLower(word)
		if upper, ok := initialisms[lower]; ok {
			b.WriteString(upper)
			continue
		}
		b.WriteString(strings.ToUpper(lower[:1]) + lower[1:])
	}
	out := b.String()
	if out == "" || unicode.IsDigit(rune(out[0])) {
		out = "M" + out
	}
	return out
}

func wrap(text string, width int) []string {
	var lines []string
	line := ""
	for _, word := range strings.Fields(text) {
		if line != "" && len(line)+1+len(word) > width {
			lines = append(lines, line)
			line = word
			continue
		}
		if line != "" {
			line += " "
		}
		line += word
	}
	if line != "" {
		lines = append(lines, line)
	}
	return lines
}

// Only public record roots enforce record-version and structural contracts.
func recordKind(l loc) string {
	if l.ptr != "" {
		return ""
	}
	switch l.file {
	case "checkpoint.json":
		return "checkpoint"
	case "run-event.json":
		return "event"
	case "policy.json":
		return "policy"
	}
	return ""
}
