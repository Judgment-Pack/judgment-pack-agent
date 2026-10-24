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
	for _, fn := range functions {
		if fn.Body == nil {
			continue
		}
		inspect(fn.Body, func(n ast.Node) {
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
					if text(n.Fun) == "StampEvent" || text(n.Fun) == "append" {
						for _, a := range n.Args {
							trace(a)
						}
					}
				}
			}
			inspect(fn.Body, func(n ast.Node) {
				switch n := n.(type) {
				case *ast.ReturnStmt:
					if returnsEvent {
						for _, r := range n.Results {
							trace(r)
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
									trace(n.Args[i])
								}
								i++
							}
						}
					}
					switch text(n.Fun) {
					case "Emit", "emit", "deliver", "notify", "Notify", "publishPlan", "PublishPlan", "append":
						for _, a := range n.Args {
							trace(a)
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
			// Trace only bytes actually decoded into a returned event. An unrelated
			// JSON literal in a function that also calls Unmarshal is not an emitter.
			if returnsEvent {
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
					if call, ok := n.(*ast.CallExpr); ok && text(call.Fun) == "Unmarshal" && len(call.Args) == 2 && inferred(call.Args[1], map[ast.Node]bool{}) == "RunEvent" {
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
	if err = json.NewEncoder(os.Stdout).Encode(out); err != nil {
		panic(err)
	}
}
