// Source analysis only: stdlib parsing; never load or execute a production package.
package main

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Site struct {
	Kind string `json:"kind"`
	File string `json:"file"`
	Line int    `json:"line"`
}
type Result struct {
	Emits []Site `json:"emits"`
	Reads []Site `json:"reads"`
}

func text(n ast.Expr) string {
	switch n := n.(type) {
	case *ast.Ident:
		return n.Name
	case *ast.SelectorExpr:
		return n.Sel.Name
	case *ast.StarExpr:
		return text(n.X)
	}
	return ""
}
func str(n ast.Expr) string {
	if n, ok := n.(*ast.BasicLit); ok && n.Kind == token.STRING {
		s, _ := strconv.Unquote(n.Value)
		return s
	}
	return ""
}
func inspect(n ast.Node, f func(ast.Node)) {
	ast.Inspect(n, func(n ast.Node) bool {
		if n != nil {
			f(n)
		}
		return true
	})
}

// decodedEventSinks follows local values at the point they are copied or
// published. A binding and its member path identify storage; a type/field name
// does not identify an instance. Pointer aliases retain storage identity, while
// value copies take a snapshot at their assignment position.
func decodedEventSinks(fn *ast.FuncDecl, sinks []ast.Expr, inferred func(ast.Expr, map[ast.Node]bool) string) map[*ast.CallExpr]bool {
	type write struct {
		left, value ast.Expr
		at          token.Pos
	}
	type storage struct {
		root   *ast.Object
		fields string
	}
	writes := []write{}
	decoders := []*ast.CallExpr{}
	own := map[ast.Node]bool{}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if n == nil {
			return false
		}
		if _, nested := n.(*ast.FuncLit); nested {
			return false
		}
		own[n] = true
		switch n := n.(type) {
		case *ast.AssignStmt:
			for i, left := range n.Lhs {
				var value ast.Expr
				if i < len(n.Rhs) {
					value = n.Rhs[i]
				}
				writes = append(writes, write{left, value, n.Pos()})
			}
		case *ast.ValueSpec:
			for i, left := range n.Names {
				var value ast.Expr
				if i < len(n.Values) {
					value = n.Values[i]
				}
				writes = append(writes, write{left, value, n.Pos()})
			}
		case *ast.CallExpr:
			if text(n.Fun) == "Unmarshal" && len(n.Args) == 2 && inferred(n.Args[1], map[ast.Node]bool{}) == "RunEvent" {
				decoders = append(decoders, n)
			}
		}
		return true
	})
	binding := func(object *ast.Object, at token.Pos) (ast.Expr, token.Pos) {
		var value ast.Expr
		var pos token.Pos
		for _, w := range writes {
			if id, ok := w.left.(*ast.Ident); ok && id.Obj == object && w.at < at && w.at > pos {
				value, pos = w.value, w.at
			}
		}
		return value, pos
	}
	var location, pointer func(ast.Expr, token.Pos) storage
	pointer = func(n ast.Expr, at token.Pos) storage {
		switch n := n.(type) {
		case *ast.ParenExpr:
			return pointer(n.X, at)
		case *ast.UnaryExpr:
			if n.Op == token.AND {
				return location(n.X, at)
			}
		case *ast.Ident:
			if n.Obj != nil {
				if value, pos := binding(n.Obj, at); pos.IsValid() {
					return pointer(value, pos)
				}
				if field, ok := n.Obj.Decl.(*ast.Field); ok {
					if _, ok := field.Type.(*ast.StarExpr); ok {
						return storage{n.Obj, "*"}
					}
				}
			}
		}
		return storage{}
	}
	location = func(n ast.Expr, at token.Pos) storage {
		switch n := n.(type) {
		case *ast.ParenExpr:
			return location(n.X, at)
		case *ast.Ident:
			return storage{n.Obj, ""}
		case *ast.StarExpr:
			return pointer(n.X, at)
		case *ast.SelectorExpr:
			base := pointer(n.X, at)
			if base.root == nil {
				base = location(n.X, at)
			}
			if base.root != nil {
				base.fields += "." + n.Sel.Name
			}
			return base
		}
		return storage{}
	}
	contains := func(parent, child storage) bool {
		return parent.root != nil && parent.root == child.root && (parent.fields == child.fields || strings.HasPrefix(child.fields, parent.fields+"."))
	}
	targets := map[*ast.CallExpr]storage{}
	for _, decoder := range decoders {
		targets[decoder] = pointer(decoder.Args[1], decoder.Pos())
	}
	accepted := map[*ast.CallExpr]bool{}
	for _, candidate := range decoders {
		type visit struct {
			source ast.Expr
			at     token.Pos
		}
		seen := map[visit]bool{}
		var reaches func(ast.Expr, token.Pos) bool
		reaches = func(n ast.Expr, at token.Pos) bool {
			if n == nil || seen[visit{n, at}] {
				return false
			}
			seen[visit{n, at}] = true
			if call, ok := n.(*ast.CallExpr); ok && (text(call.Fun) == "StampEvent" || text(call.Fun) == "StampFeatureEvent" || text(call.Fun) == "append") {
				for _, arg := range call.Args {
					if reaches(arg, at) {
						return true
					}
				}
				return false
			}
			source := location(n, at)
			if source.root == nil {
				return false
			}
			var latest *write
			var last token.Pos
			for i := range writes {
				w := &writes[i]
				if w.at < at && w.at > last && contains(location(w.left, w.at), source) {
					latest, last = w, w.at
				}
			}
			var decoded *ast.CallExpr
			for _, call := range decoders {
				if call.Pos() < at && call.Pos() > last && contains(targets[call], source) {
					decoded, last = call, call.Pos()
				}
			}
			if decoded != nil {
				return decoded == candidate
			}
			if latest == nil || latest.value == nil {
				return false
			}
			// Copy a selected member from the source aggregate at copy time, never at
			// the later return time. Decoding the original after a copy cannot change it.
			value := latest.value
			suffix := strings.TrimPrefix(source.fields, location(latest.left, latest.at).fields)
			for _, member := range strings.Split(strings.TrimPrefix(suffix, "."), ".") {
				if member != "" {
					value = &ast.SelectorExpr{X: value, Sel: ast.NewIdent(member)}
				}
			}
			return reaches(value, latest.at)
		}
		for _, sink := range sinks {
			if own[sink] && reaches(sink, sink.Pos()) {
				accepted[candidate] = true
				break
			}
		}
	}
	return accepted
}

// constructedEventSources follows constructed values separately from decoded JSON.
// Values are immutable snapshots, branches merge possible values, and tuple results
// retain their position. A payload factory is visited only when its result is used;
// returning a payload alone does not establish publication of a RunEvent.
type eventValue struct {
	strings []string
	sites   []Site
	members map[string]eventValue
	event   bool
}

func mergeEventValues(a, b eventValue) eventValue {
	out := eventValue{event: a.event || b.event, members: map[string]eventValue{}}
	stringsSeen, sitesSeen := map[string]bool{}, map[Site]bool{}
	for _, value := range append(append([]string{}, a.strings...), b.strings...) {
		if !stringsSeen[value] {
			stringsSeen[value] = true
			out.strings = append(out.strings, value)
		}
	}
	for _, site := range append(append([]Site{}, a.sites...), b.sites...) {
		if !sitesSeen[site] {
			sitesSeen[site] = true
			out.sites = append(out.sites, site)
		}
	}
	for name, value := range a.members {
		out.members[name] = value
	}
	for name, value := range b.members {
		out.members[name] = mergeEventValues(out.members[name], value)
	}
	return out
}

func constructedEventSources(fn *ast.FuncDecl, functions map[string]*ast.FuncDecl, args []eventValue, fset *token.FileSet, root string) []Site {
	type environment map[*ast.Object]eventValue
	clone := func(env environment) environment {
		out := environment{}
		for object, value := range env {
			out[object] = value
		}
		return out
	}
	resultTypes := func(fn *ast.FuncDecl) []string {
		out := []string{}
		if fn.Type.Results != nil {
			for _, field := range fn.Type.Results.List {
				count := len(field.Names)
				if count == 0 {
					count = 1
				}
				for i := 0; i < count; i++ {
					out = append(out, text(field.Type))
				}
			}
		}
		return out
	}
	active := map[*ast.FuncDecl]bool{}
	out := []Site{}
	publish := func(value eventValue) {
		if value.event {
			out = append(out, value.sites...)
		}
	}
	var run func(*ast.FuncDecl, []eventValue) []eventValue
	run = func(current *ast.FuncDecl, args []eventValue) []eventValue {
		if current == nil || current.Body == nil || active[current] {
			return nil
		}
		active[current] = true
		defer delete(active, current)
		env := environment{}
		i := 0
		if current.Type.Params != nil {
			for _, field := range current.Type.Params.List {
				for _, name := range field.Names {
					if i < len(args) {
						env[name.Obj] = args[i]
					}
					i++
				}
			}
		}
		var eval func(ast.Expr, environment) []eventValue
		one := func(values []eventValue) eventValue {
			if len(values) > 0 {
				return values[0]
			}
			return eventValue{}
		}
		eval = func(expr ast.Expr, state environment) []eventValue {
			value := eventValue{}
			switch n := expr.(type) {
			case *ast.BasicLit:
				if n.Kind == token.STRING {
					value.strings = []string{str(n)}
				}
			case *ast.Ident:
				value = state[n.Obj]
			case *ast.ParenExpr:
				return eval(n.X, state)
			case *ast.UnaryExpr:
				return eval(n.X, state)
			case *ast.StarExpr:
				return eval(n.X, state)
			case *ast.SelectorExpr:
				value = one(eval(n.X, state)).members[n.Sel.Name]
			case *ast.CompositeLit:
				value.members = map[string]eventValue{}
				name := text(n.Type)
				value.event = name == "RunEvent"
				if array, ok := n.Type.(*ast.ArrayType); ok {
					value.event = text(array.Elt) == "RunEvent"
				}
				for _, entry := range n.Elts {
					if kv, ok := entry.(*ast.KeyValueExpr); ok {
						member := one(eval(kv.Value, state))
						value.members[text(kv.Key)] = member
						value.sites = append(value.sites, member.sites...)
						if text(kv.Key) == "Type" && strings.HasPrefix(name, "RunEvent") {
							pos := fset.Position(kv.Pos())
							rel, _ := filepath.Rel(root, pos.Filename)
							for _, kind := range member.strings {
								value.sites = append(value.sites, Site{kind, filepath.ToSlash(rel), pos.Line})
							}
						}
					} else {
						value.sites = append(value.sites, one(eval(entry, state)).sites...)
					}
				}
			case *ast.CallExpr:
				arguments := []eventValue{}
				for _, arg := range n.Args {
					arguments = append(arguments, one(eval(arg, state)))
				}
				name := text(n.Fun)
				if name == "StampEvent" || name == "StampFeatureEvent" {
					if len(arguments) > 0 {
						return arguments[:1]
					}
				}
				if name == "append" {
					for _, arg := range arguments {
						value = mergeEventValues(value, arg)
					}
					return []eventValue{value}
				}
				if target := functions[name]; target != nil {
					for _, typ := range resultTypes(target) {
						if typ != "RunEvent" && strings.HasPrefix(typ, "RunEvent") {
							return run(target, arguments)
						}
					}
				}
			}
			return []eventValue{value}
		}
		var assign func(ast.Expr, eventValue, environment)
		assign = func(left ast.Expr, value eventValue, state environment) {
			switch n := left.(type) {
			case *ast.Ident:
				if n.Obj != nil {
					state[n.Obj] = value
				}
			case *ast.SelectorExpr:
				parent := one(eval(n.X, state))
				members := map[string]eventValue{}
				for name, held := range parent.members {
					members[name] = held
				}
				members[n.Sel.Name] = value
				parent.members, parent.sites = members, nil
				for _, held := range members {
					parent.sites = append(parent.sites, held.sites...)
				}
				// Declared union storage becomes an event when a union member is set.
				if id, ok := n.X.(*ast.Ident); ok && id.Obj != nil {
					if decl, ok := id.Obj.Decl.(*ast.ValueSpec); ok {
						parent.event = text(decl.Type) == "RunEvent"
					}
				}
				assign(n.X, parent, state)
			}
		}
		returns := []eventValue{}
		var block func([]ast.Stmt, environment) bool
		block = func(statements []ast.Stmt, state environment) bool {
			for _, statement := range statements {
				// Read publication arguments before assignment changes their local bindings.
				ast.Inspect(statement, func(node ast.Node) bool {
					if _, nested := node.(*ast.FuncLit); nested {
						return false
					}
					if _, nested := node.(*ast.BlockStmt); nested {
						return false
					}
					if call, ok := node.(*ast.CallExpr); ok {
						if target := functions[text(call.Fun)]; target != nil && target.Type.Params != nil {
							i := 0
							for _, field := range target.Type.Params.List {
								count := len(field.Names)
								if count == 0 {
									count = 1
								}
								for j := 0; j < count; j++ {
									if text(field.Type) == "RunEvent" && i < len(call.Args) {
										publish(one(eval(call.Args[i], state)))
									}
									i++
								}
							}
						}
					}
					return true
				})
				switch n := statement.(type) {
				case *ast.AssignStmt:
					values := []eventValue{}
					for _, rhs := range n.Rhs {
						values = append(values, eval(rhs, state)...)
					}
					for i, lhs := range n.Lhs {
						value := eventValue{}
						if i < len(values) {
							value = values[i]
						}
						assign(lhs, value, state)
					}
				case *ast.DeclStmt:
					if decl, ok := n.Decl.(*ast.GenDecl); ok {
						for _, spec := range decl.Specs {
							if spec, ok := spec.(*ast.ValueSpec); ok {
								for i, name := range spec.Names {
									value := eventValue{event: text(spec.Type) == "RunEvent"}
									if i < len(spec.Values) {
										value = one(eval(spec.Values[i], state))
									}
									assign(name, value, state)
								}
							}
						}
					}
				case *ast.ExprStmt:
					if call, ok := n.X.(*ast.CallExpr); ok {
						switch text(call.Fun) {
						case "Emit", "emit", "deliver", "notify", "Notify", "publishPlan", "PublishPlan":
							for _, arg := range call.Args {
								publish(one(eval(arg, state)))
							}
						}
					}
				case *ast.ReturnStmt:
					values := []eventValue{}
					for _, expr := range n.Results {
						values = append(values, eval(expr, state)...)
					}
					for i, value := range values {
						for len(returns) <= i {
							returns = append(returns, eventValue{})
						}
						returns[i] = mergeEventValues(returns[i], value)
					}
					return false
				case *ast.IfStmt:
					if n.Init != nil {
						block([]ast.Stmt{n.Init}, state)
					}
					yes, no := clone(state), clone(state)
					if condition, ok := n.Cond.(*ast.BinaryExpr); ok && (condition.Op == token.EQL || condition.Op == token.NEQ) {
						if id, ok := condition.X.(*ast.Ident); ok && str(condition.Y) != "" {
							value := state[id.Obj]
							matching, other := value, value
							matching.strings, other.strings = []string{str(condition.Y)}, nil
							for _, held := range value.strings {
								if held != str(condition.Y) {
									other.strings = append(other.strings, held)
								}
							}
							if condition.Op == token.EQL {
								yes[id.Obj], no[id.Obj] = matching, other
							} else {
								yes[id.Obj], no[id.Obj] = other, matching
							}
						}
					}
					yesLive := block(n.Body.List, yes)
					noLive := true
					if n.Else != nil {
						noLive = block([]ast.Stmt{n.Else}, no)
					}
					for key := range state {
						delete(state, key)
					}
					if yesLive {
						for key, value := range yes {
							state[key] = value
						}
					}
					if noLive {
						for key, value := range no {
							state[key] = mergeEventValues(state[key], value)
						}
					}
					if !yesLive && !noLive {
						return false
					}
				case *ast.BlockStmt:
					if !block(n.List, state) {
						return false
					}
				case *ast.RangeStmt:
					loop := clone(state)
					block(n.Body.List, loop)
					for key, value := range loop {
						state[key] = mergeEventValues(state[key], value)
					}
				case *ast.ForStmt:
					loop := clone(state)
					block(n.Body.List, loop)
					for key, value := range loop {
						state[key] = mergeEventValues(state[key], value)
					}
				}

			}
			return true
		}
		block(current.Body.List, env)
		return returns
	}
	values := run(fn, args)
	for i, typ := range resultTypes(fn) {
		if typ == "RunEvent" && i < len(values) {
			publish(values[i])
		}
	}
	return out
}

func main() {
	root := os.Args[1]
	fset := token.NewFileSet()
	out := Result{Emits: []Site{}, Reads: []Site{}}
	// Index declared function results and struct fields so consumers reached through
	// returned storage records and field chains are recognized as RunEvent reads.
	functions := map[string]*ast.FuncDecl{}
	members := map[string]map[string]ast.Expr{}
	fieldWrites := map[string][]ast.Expr{}
	_ = filepath.WalkDir(filepath.Join(root, "go"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "certification" || d.Name() == "examples" || d.Name() == "spike" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, e := parser.ParseFile(fset, path, nil, 0)
		if e != nil {
			return e
		}
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				functions[d.Name.Name] = d
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					if typ, ok := spec.(*ast.TypeSpec); ok {
						if st, ok := typ.Type.(*ast.StructType); ok {
							row := map[string]ast.Expr{}
							for _, field := range st.Fields.List {
								for _, name := range field.Names {
									row[name.Name] = field.Type
								}
							}
							members[typ.Name.Name] = row
						}
					}
				}
			}
		}
		return nil
	})
	var inferred func(ast.Expr, map[ast.Node]bool) string
	inferred = func(expr ast.Expr, seen map[ast.Node]bool) string {
		if expr == nil || seen[expr] {
			return ""
		}
		seen[expr] = true
		switch n := expr.(type) {
		case *ast.UnaryExpr:
			return inferred(n.X, seen)
		case *ast.StarExpr:
			return inferred(n.X, seen)
		case *ast.CompositeLit:
			return text(n.Type)
		case *ast.CallExpr:
			if fn := functions[text(n.Fun)]; fn != nil && fn.Type.Results != nil && len(fn.Type.Results.List) > 0 {
				return text(fn.Type.Results.List[0].Type)
			}
		case *ast.SelectorExpr:
			return text(members[inferred(n.X, seen)][n.Sel.Name])
		case *ast.Ident:
			if n.Obj != nil {
				switch d := n.Obj.Decl.(type) {
				case *ast.Field:
					return text(d.Type)
				case *ast.ValueSpec:
					if d.Type != nil {
						return text(d.Type)
					}
					if len(d.Values) > 0 {
						return inferred(d.Values[0], seen)
					}
				case *ast.AssignStmt:
					for i, l := range d.Lhs {
						if text(l) == n.Name && i < len(d.Rhs) {
							return inferred(d.Rhs[i], seen)
						}
					}
				case *ast.RangeStmt:
					if a, ok := d.X.(*ast.Ident); ok && a.Obj != nil {
						if f, ok := a.Obj.Decl.(*ast.Field); ok {
							if arr, ok := f.Type.(*ast.ArrayType); ok {
								return text(arr.Elt)
							}
						}
					}
				}
			}
		}
		return ""
	}
	callArguments := map[string][]eventValue{}
	for _, fn := range functions {
		if fn.Body == nil {
			continue
		}
		inspect(fn.Body, func(n ast.Node) {
			// Index possible discriminants once. Literal arguments establish
			// values for conditional constructors, never publication by themselves.
			if call, ok := n.(*ast.CallExpr); ok {
				name := text(call.Fun)
				args := callArguments[name]
				for i, arg := range call.Args {
					for len(args) <= i {
						args = append(args, eventValue{})
					}
					if kind := str(arg); kind != "" {
						args[i] = mergeEventValues(args[i], eventValue{strings: []string{kind}})
					}
				}
				callArguments[name] = args
			}
			if a, ok := n.(*ast.AssignStmt); ok {
				for i, left := range a.Lhs {
					if sel, ok := left.(*ast.SelectorExpr); ok && i < len(a.Rhs) {
						key := inferred(sel.X, map[ast.Node]bool{}) + "." + sel.Sel.Name
						fieldWrites[key] = append(fieldWrites[key], a.Rhs[i])
					}
				}
			}
		})
	}

	factories := map[string]int{}
	for name, fn := range functions {
		if fn.Body == nil {
			continue
		}
		eventResult, decodesEvent := false, false
		if fn.Type.Results != nil {
			for _, r := range fn.Type.Results.List {
				if text(r.Type) == "RunEvent" {
					eventResult = true
				}
			}
		}
		inspect(fn.Body, func(n ast.Node) {
			if call, ok := n.(*ast.CallExpr); ok && text(call.Fun) == "Unmarshal" && len(call.Args) == 2 && inferred(call.Args[1], map[ast.Node]bool{}) == "RunEvent" {
				decodesEvent = true
			}
		})
		if !eventResult || !decodesEvent {
			continue
		}
		params := []string{}
		if fn.Type.Params != nil {
			for _, p := range fn.Type.Params.List {
				for _, n := range p.Names {
					params = append(params, n.Name)
				}
			}
		}
		inspect(fn.Body, func(n ast.Node) {
			if call, ok := n.(*ast.CallExpr); ok && text(call.Fun) == "Set" && len(call.Args) == 2 && str(call.Args[0]) == "type" {
				if wrap, ok := call.Args[1].(*ast.CallExpr); ok && text(wrap.Fun) == "String" && len(wrap.Args) == 1 {
					for i, p := range params {
						if text(wrap.Args[0]) == p {
							factories[name] = i
						}
					}
				}
			}
		})
	}
	fields := map[string]string{}
	schemaBytes, err := os.ReadFile(filepath.Join(root, "contract/schemas/run-event.json"))
	if err != nil {
		panic(err)
	}
	var schema struct {
		Properties struct{ Type struct{ Enum []string } }
	}
	if err = json.Unmarshal(schemaBytes, &schema); err != nil {
		panic(err)
	}
	for _, kind := range schema.Properties.Type.Enum {
		parts := strings.Split(kind, "_")
		for i := range parts {
			parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
		}
		fields[strings.Join(parts, "")] = kind
	}
	add := func(list *[]Site, kind string, n ast.Node) {
		p := fset.Position(n.Pos())
		rel, _ := filepath.Rel(root, p.Filename)
		*list = append(*list, Site{kind, filepath.ToSlash(rel), p.Line})
	}
	err = filepath.WalkDir(filepath.Join(root, "go"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "certification", "examples", "spike":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || strings.HasSuffix(path, "_gen.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			returnsEvent := false
			if fn.Type.Results != nil {
				for _, r := range fn.Type.Results.List {
					if text(r.Type) == "RunEvent" || func() bool { a, ok := r.Type.(*ast.ArrayType); return ok && text(a.Elt) == "RunEvent" }() {
						returnsEvent = true
					}
				}
			}
			// Bind local event variables and parameters by their declared/constructed RunEvent type.
			variables := map[string]bool{}
			if fn.Type.Params != nil {
				for _, p := range fn.Type.Params.List {
					if text(p.Type) == "RunEvent" {
						for _, n := range p.Names {
							variables[n.Name] = true
						}
					}
				}
			}
			inspect(fn.Body, func(n ast.Node) {
				switch n := n.(type) {
				case *ast.ValueSpec:
					if text(n.Type) == "RunEvent" {
						for _, name := range n.Names {
							variables[name.Name] = true
						}
					}
				case *ast.AssignStmt:
					for i, r := range n.Rhs {
						if c, ok := r.(*ast.CompositeLit); ok && text(c.Type) == "RunEvent" && i < len(n.Lhs) {
							variables[text(n.Lhs[i])] = true
						}
					}
				}
			})
			// Event constructors count only on an event-return path or in a publication call.
			var trace func(ast.Node)
			seen := map[ast.Node]bool{}
			sinks := []ast.Expr{}
			trace = func(n ast.Node) {
				if n == nil || seen[n] {
					return
				}
				seen[n] = true
				switch n := n.(type) {
				case *ast.CompositeLit:
					name := text(n.Type)
					if a, ok := n.Type.(*ast.ArrayType); ok && text(a.Elt) == "RunEvent" {
						for _, e := range n.Elts {
							if c, ok := e.(*ast.CompositeLit); ok {
								for _, entry := range c.Elts {
									if kv, ok := entry.(*ast.KeyValueExpr); ok {
										trace(kv.Value)
									}
								}
							} else {
								trace(e)
							}
						}
					}
					if name == "RunEvent" || strings.HasPrefix(name, "RunEvent") || name == "ToolCall" || name == "ToolResult" {
						for _, e := range n.Elts {
							if kv, ok := e.(*ast.KeyValueExpr); ok {
								if text(kv.Key) == "Type" {
									add(&out.Emits, str(kv.Value), kv)
								} else {
									trace(kv.Value)
								}
							}
						}
					}
				case *ast.UnaryExpr:
					trace(n.X)
				case *ast.StarExpr:
					trace(n.X)
				case *ast.SelectorExpr:
					for _, value := range fieldWrites[inferred(n.X, map[ast.Node]bool{})+"."+n.Sel.Name] {
						trace(value)
					}
				case *ast.Ident:
					if n.Obj != nil {
						switch d := n.Obj.Decl.(type) {
						case *ast.AssignStmt:
							for _, r := range d.Rhs {
								trace(r)
							}
						case *ast.ValueSpec:
							for _, r := range d.Values {
								trace(r)
							}
						}
					}
				case *ast.CallExpr:
					// The indexed factory writes this argument into its JSON type key.
					if i, ok := factories[text(n.Fun)]; ok && len(n.Args) > i {
						add(&out.Emits, str(n.Args[i]), n)
					}
					if text(n.Fun) == "StampEvent" || text(n.Fun) == "StampFeatureEvent" || text(n.Fun) == "append" {
						for _, a := range n.Args {
							trace(a)
						}
					}
				}
			}
			publish := func(n ast.Expr) {
				if n != nil {
					sinks = append(sinks, n)
				}
				trace(n)
			}
			inspect(fn.Body, func(n ast.Node) {
				switch n := n.(type) {
				case *ast.ReturnStmt:
					if returnsEvent {
						for _, r := range n.Results {
							publish(r)
						}
					}
				case *ast.CallExpr:
					if decl := functions[text(n.Fun)]; decl != nil && decl.Type.Params != nil {
						i := 0
						for _, p := range decl.Type.Params.List {
							count := len(p.Names)
							if count == 0 {
								count = 1
							}
							for j := 0; j < count; j++ {
								if text(p.Type) == "RunEvent" && i < len(n.Args) {
									publish(n.Args[i])
								}
								i++
							}
						}
					}
					switch text(n.Fun) {
					case "Emit", "emit", "deliver", "notify", "Notify", "publishPlan", "PublishPlan", "append":
						for _, a := range n.Args {
							publish(a)
						}
					}
				case *ast.SelectorExpr:
					if variables[text(n.X)] || inferred(n.X, map[ast.Node]bool{}) == "RunEvent" {
						if kind, ok := fields[n.Sel.Name]; ok {
							add(&out.Reads, kind, n)
						}
					}
				}
			})
			out.Emits = append(out.Emits, constructedEventSources(fn, functions, callArguments[fn.Name.Name], fset, root)...)
			decoded := decodedEventSinks(fn, sinks, inferred)
			{
				rawSeen := map[ast.Node]bool{}
				var raw func(ast.Expr)
				raw = func(n ast.Expr) {
					if n == nil || rawSeen[n] {
						return
					}
					rawSeen[n] = true
					switch n := n.(type) {
					case *ast.Ident:
						if n.Obj != nil {
							switch d := n.Obj.Decl.(type) {
							case *ast.AssignStmt:
								for _, v := range d.Rhs {
									raw(v)
								}
							case *ast.ValueSpec:
								for _, v := range d.Values {
									raw(v)
								}
							}
						}
					case *ast.CallExpr:
						for _, v := range n.Args {
							raw(v)
						}
					case *ast.BasicLit:
						dec := json.NewDecoder(strings.NewReader(str(n)))
						first, e := dec.Token()
						if e != nil || first != json.Delim('{') {
							return
						}
						for dec.More() {
							key, e := dec.Token()
							if e != nil {
								return
							}
							var value any
							if dec.Decode(&value) != nil {
								return
							}
							if key == "type" {
								if kind, ok := value.(string); ok {
									add(&out.Emits, kind, n)
								}
								return
							}
						}
					}
				}
				inspect(fn.Body, func(n ast.Node) {
					if call, ok := n.(*ast.CallExpr); ok && text(call.Fun) == "Unmarshal" && len(call.Args) == 2 && inferred(call.Args[1], map[ast.Node]bool{}) == "RunEvent" && decoded[call] {
						raw(call.Args[0])
					}
				})
			}
		}
		return nil
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	unique := map[Site]bool{}
	emits := []Site{}
	for _, site := range out.Emits {
		if !unique[site] {
			unique[site] = true
			emits = append(emits, site)
		}
	}
	out.Emits = emits
	if err = json.NewEncoder(os.Stdout).Encode(out); err != nil {
		panic(err)
	}
}
