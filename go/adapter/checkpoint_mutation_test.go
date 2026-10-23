package adapter

import (
	"bytes"
	"encoding/json"
	"errors"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func checkpointField(selection *types.Selection) bool {
	field, ok := selection.Obj().(*types.Var)
	if !ok || !field.IsField() || field.Pkg() == nil {
		return false
	}
	for _, owner := range []string{"Execution", "Checkpoint", "ExecutionController"} {
		declared := field.Pkg().Scope().Lookup(owner)
		if declared == nil {
			continue
		}
		structure, ok := declared.Type().Underlying().(*types.Struct)
		if !ok {
			continue
		}
		for i := 0; i < structure.NumFields(); i++ {
			if structure.Field(i) == field {
				return owner != "ExecutionController" || field.Name() == "checkpoint"
			}
		}
	}
	return false
}

func checkpointWriteSites(file *ast.File, info *types.Info, initializationOwner ...bool) []token.Pos {
	found := []token.Pos{}
	type origin struct {
		member    string
		book      types.Object
		aggregate types.Object
		owner     string
	}
	aliases := map[types.Object]origin{}

	receiverAliases := map[types.Object]string{}
	aggregateOwners := map[types.Object]string{}
	objectIDs := map[types.Object]int{}
	objectKey := func(object types.Object) string {
		if object == nil {
			return ""
		}
		id := objectIDs[object]
		if id == 0 {
			id = len(objectIDs) + 1
			objectIDs[object] = id
		}
		return strconv.Itoa(id)
	}
	var identity func(ast.Expr) string
	identity = func(expression ast.Expr) string {
		switch held := expression.(type) {
		case *ast.Ident:
			object := info.ObjectOf(held)
			if alias := receiverAliases[object]; alias != "" {
				return alias
			}
			return objectKey(object)
		case *ast.SelectorExpr:
			if root := identity(held.X); root != "" {
				return root + "." + held.Sel.Name
			}
		case *ast.ParenExpr:
			return identity(held.X)
		case *ast.StarExpr:
			return identity(held.X)
		case *ast.UnaryExpr:
			return identity(held.X)
		}
		return ""
	}
	functionOf := func(expression ast.Expr) types.Object {
		switch held := expression.(type) {
		case *ast.Ident:
			return info.ObjectOf(held)
		case *ast.SelectorExpr:
			return info.ObjectOf(held.Sel)
		}
		return nil
	}
	aggregateOwner := func(expression ast.Expr) string {
		if id, ok := expression.(*ast.Ident); ok {
			if owner := aggregateOwners[info.ObjectOf(id)]; owner != "" {
				return owner
			}
		}
		if call, ok := expression.(*ast.CallExpr); ok {
			function := functionOf(call.Fun)
			if selected, ok := call.Fun.(*ast.SelectorExpr); ok && function != nil && function.Name() == "State" && function.Pkg() != nil && function.Pkg().Path() == "github.com/Judgment-Pack/judgment-pack-agent/go/context" {
				return identity(selected.X)
			}
		}
		return identity(expression)
	}
	var source func(ast.Expr) (origin, bool)
	source = func(expression ast.Expr) (origin, bool) {
		switch held := expression.(type) {
		case *ast.Ident:
			value, ok := aliases[info.ObjectOf(held)]
			return value, ok
		case *ast.SelectorExpr:
			if selection := info.Selections[held]; selection != nil && checkpointField(selection) {
				var aggregate types.Object
				if id, ok := held.X.(*ast.Ident); ok {
					aggregate = info.ObjectOf(id)
				}
				return origin{member: held.Sel.Name, aggregate: aggregate, owner: aggregateOwner(held.X)}, true
			}
			return source(held.X)
		case *ast.IndexExpr:
			return source(held.X)
		case *ast.StarExpr:
			return source(held.X)
		case *ast.UnaryExpr:
			return source(held.X)
		case *ast.ParenExpr:
			return source(held.X)
		case *ast.CallExpr:
			if name, ok := held.Fun.(*ast.Ident); ok && name.Name == "append" && len(held.Args) > 0 {
				return source(held.Args[0])
			}
		}
		return origin{}, false
	}
	// Only explicit publication of this local book under its owning member permits
	// a detached edit. Publication of a fresh read or another member is insufficient.
	scopes := map[ast.Node]ast.Node{}
	deferred := map[ast.Node]ast.Node{}
	initializers := map[ast.Node]string{}
	var collect func(ast.Node, ast.Node)
	collect = func(node ast.Node, scope ast.Node) {
		if node == nil {
			return
		}
		switch node.(type) {
		case *ast.FuncDecl, *ast.FuncLit:
			if enclosing := deferred[node]; enclosing != nil {
				scope = enclosing
			} else {
				scope = node
			}
		}

		if fn, ok := node.(*ast.FuncDecl); ok && len(initializationOwner) > 0 && initializationOwner[0] && fn.Name.Name == "Initialize" && fn.Recv != nil && len(fn.Recv.List) == 1 && len(fn.Recv.List[0].Names) == 1 {
			function := info.Defs[fn.Name]
			if function != nil && function.Pkg() != nil && function.Pkg().Path() == "github.com/Judgment-Pack/judgment-pack-agent/go/context" {
				signature, _ := function.Type().(*types.Signature)
				if signature != nil && signature.Recv() != nil {
					receiver := signature.Recv().Type()
					if pointer, ok := receiver.(*types.Pointer); ok {
						receiver = pointer.Elem()
					}
					if named, ok := receiver.(*types.Named); ok && named.Obj().Name() == "ExecutionController" {
						initializers[scope] = identity(fn.Recv.List[0].Names[0])
					}
				}
			}
		}
		scopes[node] = scope
		if statement, ok := node.(*ast.DeferStmt); ok {
			if fn, ok := statement.Call.Fun.(*ast.FuncLit); ok {
				deferred[fn] = scope
			}
		}

		bind := func(left ast.Expr, right ast.Expr) {
			id, ok := left.(*ast.Ident)
			if !ok {
				return
			}
			symbol := info.ObjectOf(id)
			// Copying a pointed-to struct creates a separate working book.
			copied := false
			if _, dereference := right.(*ast.StarExpr); dereference {
				if valueType := info.TypeOf(right); valueType != nil {
					switch valueType.Underlying().(type) {
					case *types.Struct, *types.Array:
						copied = true
					}
				}
			}
			if receiver := identity(right); receiver != "" && !copied {
				receiverAliases[symbol] = receiver
			} else {
				delete(receiverAliases, symbol)
			}
			if owner := aggregateOwner(right); owner != "" {
				aggregateOwners[symbol] = owner
			} else {
				delete(aggregateOwners, symbol)
			}
			// Only the owning Initialize method may attach a validated materialization
			// to its receiver. A later publication never supplies an unknown origin owner.
			if receiver := initializers[scope]; receiver != "" {
				if call, ok := right.(*ast.CallExpr); ok {
					if factory := functionOf(call.Fun); factory != nil && factory.Name() == "executionOf" && factory.Pkg() != nil && factory.Pkg().Path() == "github.com/Judgment-Pack/judgment-pack-agent/go/context" {
						aggregateOwners[symbol] = receiver
					}
				}
			}
			if held, ok := source(right); ok {
				if held.book == nil || copied {
					held.book = symbol
				}
				aliases[symbol] = held
			}
		}

		switch held := node.(type) {
		case *ast.AssignStmt:
			for i, left := range held.Lhs {
				if i < len(held.Rhs) {
					bind(left, held.Rhs[i])
				}
			}
		case *ast.ValueSpec:
			for i, left := range held.Names {
				if i < len(held.Values) {
					bind(left, held.Values[i])
				}
			}
		case *ast.RangeStmt:
			if held.Value != nil {
				bind(held.Value, held.X)
			}
		}
		ast.Inspect(node, func(child ast.Node) bool {
			if child == nil || child == node {
				return true
			}
			collect(child, scope)
			return false
		})
	}
	collect(file, file)
	type publication struct {
		scope    ast.Node
		origin   origin
		position token.Pos
		owner    string
	}
	published := []publication{}
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}

		function := functionOf(call.Fun)
		if function == nil || function.Name() != "MutateCheckpoint" || function.Pkg() == nil || function.Pkg().Path() != "github.com/Judgment-Pack/judgment-pack-agent/go/context" {
			return true
		}
		publishingOwner := ""
		arguments := call.Args
		signature, _ := function.Type().(*types.Signature)
		if signature != nil && signature.Recv() != nil {
			if selected, ok := call.Fun.(*ast.SelectorExpr); ok {
				if selection := info.Selections[selected]; selection != nil && selection.Kind() == types.MethodExpr {
					if len(arguments) > 0 {
						publishingOwner = identity(arguments[0])
						arguments = arguments[1:]
					}
				} else {
					publishingOwner = identity(selected.X)
				}
			}
		} else if len(arguments) > 0 {
			publishingOwner = identity(arguments[0])
			arguments = arguments[1:]
		}

		for _, arg := range arguments {
			if projection, ok := arg.(*ast.CallExpr); ok {
				if fn, ok := projection.Fun.(*ast.Ident); ok && fn.Name == "checkpointMembers" && len(projection.Args) == 1 {
					owner := info.ObjectOf(fn)
					if owner == nil || owner.Pkg() == nil || owner.Pkg().Path() != "github.com/Judgment-Pack/judgment-pack-agent/go/context" {
						continue
					}
					if id, ok := projection.Args[0].(*ast.Ident); ok {
						for _, held := range aliases {
							if held.aggregate != nil && held.aggregate == info.ObjectOf(id) {
								published = append(published, publication{scopes[call], held, call.Pos(), publishingOwner})
							}
						}
					}
				}
			}
		}

		for _, arg := range arguments {
			if patch, ok := arg.(*ast.CompositeLit); ok {
				for _, entry := range patch.Elts {
					pair, ok := entry.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					key, ok := pair.Key.(*ast.BasicLit)
					if !ok {
						continue
					}
					member, err := strconv.Unquote(key.Value)
					if err != nil {
						continue
					}
					held, ok := source(pair.Value)
					if ok && held.book != nil && held.member == member {
						position := call.Pos()
						for fn, scope := range deferred {
							if call.Pos() >= fn.Pos() && call.End() <= fn.End() {
								position = scope.End()
							}
						}
						published = append(published, publication{scopes[call], held, position, publishingOwner})
					}
				}
			}
		}
		return true
	})

	record := func(expression ast.Expr) {
		caught := false
		if held, ok := source(expression); ok && held.book != nil {
			committed := false
			for _, publication := range published {
				if publication.scope == scopes[expression] && publication.origin == held && held.owner != "" && publication.owner == held.owner && publication.position > expression.Pos() {
					committed = true
				}
			}
			caught = !committed
		}
		ast.Inspect(expression, func(node ast.Node) bool {
			if selected, ok := node.(*ast.SelectorExpr); ok {
				if selection := info.Selections[selected]; selection != nil && checkpointField(selection) {
					caught = true
				}
			}
			return true
		})
		if caught {
			found = append(found, expression.Pos())
		}
	}
	ast.Inspect(file, func(node ast.Node) bool {
		switch held := node.(type) {
		case *ast.SelectorExpr:
			if selection := info.Selections[held]; selection != nil && selection.Obj().Name() == "checkpoint" && checkpointField(selection) {
				found = append(found, held.Pos())
			}
		case *ast.AssignStmt:
			for _, left := range held.Lhs {
				// Binding or replacing the local book is not an edit to one of its members.
				if _, local := left.(*ast.Ident); !local {
					record(left)
				} else if held.Tok == token.ASSIGN {
					for _, right := range held.Rhs {
						if call, ok := right.(*ast.CallExpr); ok {
							if fn, ok := call.Fun.(*ast.Ident); ok && fn.Name == "append" {
								record(left)
							}
						}
					}
				}
			}
		case *ast.IncDecStmt:
			record(held.X)
		case *ast.UnaryExpr:
			if held.Op == token.AND {
				if _, allocation := held.X.(*ast.CompositeLit); allocation {
					return true
				}
				// Taking a detached book's address is a read; subsequent pointer writes
				// are tracked through aliases. Direct aggregate addresses remain forbidden.
				if origin, ok := source(held.X); !ok || origin.book == nil {
					record(held.X)
				}
			}
		case *ast.CallExpr:
			if name, ok := held.Fun.(*ast.Ident); ok && (name.Name == "copy" || name.Name == "delete" || name.Name == "clear") && len(held.Args) > 0 {
				record(held.Args[0])
			}
		}
		return true
	})
	return found
}

func TestCheckpointWritersUseMutationBoundary(t *testing.T) {
	root := os.Getenv("JP_CHECKPOINT_GUARD_ROOT")
	if root == "" {
		root = ".."
	} else {
		root = filepath.Join(root, "go")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("go", "list", "-deps", "-export", "-json", "./...")
	command.Dir = root
	command.Env = append(os.Environ(), "GOFLAGS=-buildvcs=false")
	var stderr bytes.Buffer
	command.Stderr = &stderr
	listed, err := command.Output()
	if err != nil {
		t.Fatalf("source inventory: %v: %s", err, stderr.String())
	}
	type item struct {
		Dir, ImportPath, Export string
		GoFiles                 []string
	}
	packages := []item{}
	exports := map[string]string{}
	decoder := json.NewDecoder(bytes.NewReader(listed))
	for {
		var entry item
		if err := decoder.Decode(&entry); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if entry.Export != "" {
			exports[entry.ImportPath] = entry.Export
		}
		if strings.HasPrefix(entry.ImportPath, "github.com/Judgment-Pack/judgment-pack-agent/go/context") || strings.HasPrefix(entry.ImportPath, "github.com/Judgment-Pack/judgment-pack-agent/go/adapter") {
			packages = append(packages, entry)
		}
	}
	lookup := func(path string) (io.ReadCloser, error) { return os.Open(exports[path]) }
	unexpected := []string{}
	for _, entry := range packages {
		positions := token.NewFileSet()
		trees := []*ast.File{}
		for _, name := range entry.GoFiles {
			tree, err := parser.ParseFile(positions, filepath.Join(entry.Dir, name), nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			trees = append(trees, tree)
		}
		info := checkpointTypeInfo()
		config := types.Config{Importer: importer.ForCompiler(positions, "gc", lookup)}
		if _, err := config.Check(entry.ImportPath, positions, trees, info); err != nil {
			t.Fatal(err)
		}
		for _, tree := range trees {
			name := positions.Position(tree.Pos()).Filename
			if checkpointMutationOwner(root, name) {
				continue
			}
			for _, position := range checkpointWriteSites(tree, info, checkpointMaterializationOwner(root, name)) {
				// Materializing a validated read is not a live writer. The factory owns its new object.
				materialization := false
				if checkpointMaterializationOwner(root, name) {
					for _, decl := range tree.Decls {
						if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "executionOf" && position >= fn.Pos() && position < fn.End() {
							materialization = true
						}
					}
				}
				if !materialization {
					relative, _ := filepath.Rel(root, name)
					unexpected = append(unexpected, relative+":"+positions.Position(position).String())
				}
			}
		}
	}
	if len(unexpected) > 0 {
		t.Fatalf("checkpoint writes outside mutation boundary:\n%s", strings.Join(unexpected, "\n"))
	}
	// A new source file and aliases must be detected by the same typed scanner.
	source := `package probe
import runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
func newWriter(c *runcontext.ExecutionController) {
 c.State().Used++
 alias:=c.State();alias.Reason=nil
 c.State().Approval.State="answered"
 approval:=c.State().Approval;approval.State="answered"
 operations:=c.State().Operations;operations[0].Status="complete"
 operations=append(operations,nil)
 _=&c.State().Sources
 unrelated:=struct{Used int}{};unrelated.Used++
}`
	positions := token.NewFileSet()
	tree, err := parser.ParseFile(positions, "new_writer.go", source, 0)
	if err != nil {
		t.Fatal(err)
	}
	info := checkpointTypeInfo()
	config := types.Config{Importer: importer.ForCompiler(positions, "gc", lookup)}
	if _, err := config.Check("probe", positions, []*ast.File{tree}, info); err != nil {
		t.Fatal(err)
	}
	if sites := checkpointWriteSites(tree, info); len(sites) != 7 {
		t.Fatalf("new writer: got %d sites, want 7", len(sites))
	}
	publicationSource := `package probe
import runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
func committed(c *runcontext.ExecutionController){approval:=c.State().Approval;approval.State="answered";c.MutateCheckpoint(runcontext.CheckpointPatch{"Approval":approval})}
func wrongMember(c *runcontext.ExecutionController){approval:=c.State().Approval;approval.State="answered";c.MutateCheckpoint(runcontext.CheckpointPatch{"Critic":approval})}
func otherBook(c *runcontext.ExecutionController){approval:=c.State().Approval;approval.State="answered";other:=c.State().Approval;c.MutateCheckpoint(runcontext.CheckpointPatch{"Approval":other})}
func otherScope(c *runcontext.ExecutionController){approval:=c.State().Approval;approval.State="answered";later:=func(){c.MutateCheckpoint(runcontext.CheckpointPatch{"Approval":approval})};_ = later}
func tooEarly(c *runcontext.ExecutionController){approval:=c.State().Approval;c.MutateCheckpoint(runcontext.CheckpointPatch{"Approval":approval});approval.State="answered"}
func deferred(c *runcontext.ExecutionController){approval:=c.State().Approval;defer func(){c.MutateCheckpoint(runcontext.CheckpointPatch{"Approval":approval})}();approval.State="answered"}
func committedArray(c *runcontext.ExecutionController){operations:=c.State().Operations;operation:=operations[0];operation.Status="complete";c.MutateCheckpoint(runcontext.CheckpointPatch{"Operations":operations})}
func copiedBook(c *runcontext.ExecutionController){approval:=c.State().Approval;other:=*approval;approval.State="answered";c.MutateCheckpoint(runcontext.CheckpointPatch{"Approval":&other})}
func checkpointMembers(*runcontext.Execution)runcontext.CheckpointPatch{return runcontext.CheckpointPatch{"Used":0}}
func wrongProjection(c *runcontext.ExecutionController){state:=c.State();operations:=state.Operations;operations[0].Status="complete";c.MutateCheckpoint(checkpointMembers(state))}
`
	positions = token.NewFileSet()
	tree, err = parser.ParseFile(positions, "publication.go", publicationSource, 0)
	if err != nil {
		t.Fatal(err)
	}
	info = checkpointTypeInfo()
	config = types.Config{Importer: importer.ForCompiler(positions, "gc", lookup)}
	if _, err := config.Check("probe", positions, []*ast.File{tree}, info); err != nil {
		t.Fatal(err)
	}
	sites := checkpointWriteSites(tree, info)
	if len(sites) != 6 {
		t.Fatalf("matching publication: got %d sites, want 6", len(sites))
	}
	for i, site := range sites {
		expected := []int{4, 5, 6, 7, 10, 12}[i]
		if line := positions.Position(site).Line; line != expected {
			t.Fatalf("publication site %d: line %d, want %d", i, line, expected)
		}
	}

	checkSource := func(name, source string, expected []int) {
		t.Helper()
		positions := token.NewFileSet()
		tree, err := parser.ParseFile(positions, name, source, 0)
		if err != nil {
			t.Fatal(err)
		}
		info := checkpointTypeInfo()
		config := types.Config{Importer: importer.ForCompiler(positions, "gc", lookup)}
		if _, err := config.Check("probe", positions, []*ast.File{tree}, info); err != nil {
			t.Fatal(err)
		}
		sites := checkpointWriteSites(tree, info)
		if len(sites) != len(expected) {
			t.Fatalf("%s: got %d writer sites, want %d", name, len(sites), len(expected))
		}
		for index, site := range sites {
			if line := positions.Position(site).Line; line != expected[index] {
				t.Fatalf("%s site %d: line %d, want %d", name, index, line, expected[index])
			}
		}
	}
	checkSource("owner.go", `package probe
import runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
func wrongOwner(c,d *runcontext.ExecutionController){approval:=c.State().Approval;approval.State="answered";d.MutateCheckpoint(runcontext.CheckpointPatch{"Approval":approval})}
func receiverAlias(c *runcontext.ExecutionController){publisher:=c;state:=c.State();view:=state;approval:=view.Approval;approval.State="answered";publisher.MutateCheckpoint(runcontext.CheckpointPatch{"Approval":approval})}
func wrongAlias(c,d *runcontext.ExecutionController){publisher:=d;approval:=c.State().Approval;approval.State="answered";publisher.MutateCheckpoint(runcontext.CheckpointPatch{"Approval":approval})}
func rawOwner(state *runcontext.Execution){approval:=state.Approval;approval.State="answered";runcontext.MutateCheckpoint(state,runcontext.CheckpointPatch{"Approval":approval})}
func wrongRawOwner(state,other *runcontext.Execution){approval:=state.Approval;approval.State="answered";runcontext.MutateCheckpoint(other,runcontext.CheckpointPatch{"Approval":approval})}
func fieldOwner(pair struct{First,Second *runcontext.ExecutionController}){publisher:=pair.First;approval:=publisher.State().Approval;approval.State="answered";pair.First.MutateCheckpoint(runcontext.CheckpointPatch{"Approval":approval})}
func wrongFieldOwner(pair struct{First,Second *runcontext.ExecutionController}){approval:=pair.First.State().Approval;approval.State="answered";pair.Second.MutateCheckpoint(runcontext.CheckpointPatch{"Approval":approval})}
func methodExpression(c *runcontext.ExecutionController){approval:=c.State().Approval;approval.State="answered";(*runcontext.ExecutionController).MutateCheckpoint(c,runcontext.CheckpointPatch{"Approval":approval})}
func wrongMethodExpression(c,d *runcontext.ExecutionController){approval:=c.State().Approval;approval.State="answered";(*runcontext.ExecutionController).MutateCheckpoint(d,runcontext.CheckpointPatch{"Approval":approval})}
func choose(c *runcontext.ExecutionController)*runcontext.ExecutionController{return c}
func unknownReceiver(c,d *runcontext.ExecutionController){publisher:=c;approval:=c.State().Approval;approval.State="answered";publisher=choose(d);publisher.MutateCheckpoint(runcontext.CheckpointPatch{"Approval":approval})}
func chooseState(c *runcontext.ExecutionController)*runcontext.Execution{return c.State()}
func unknownAggregate(c,d *runcontext.ExecutionController){state:=c.State();state=chooseState(d);approval:=state.Approval;approval.State="answered";c.MutateCheckpoint(runcontext.CheckpointPatch{"Approval":approval})}
`, []int{3, 5, 7, 9, 11, 13, 15})
	checkSource("range.go", `package probe
import runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
func unpublished(c *runcontext.ExecutionController){operations:=c.State().Operations;for _,operation:=range operations {operation.Status="complete"}}
func wrongOwner(c,d *runcontext.ExecutionController){operations:=c.State().Operations;for _,operation:=range operations {operation.Status="complete"};d.MutateCheckpoint(runcontext.CheckpointPatch{"Operations":operations})}
func committed(c *runcontext.ExecutionController){publisher:=c;operations:=c.State().Operations;for _,operation:=range operations {held:=operation;held.Status="complete"};publisher.MutateCheckpoint(runcontext.CheckpointPatch{"Operations":operations})}
func rawOwner(state *runcontext.Execution){operations:=state.Operations;for _,operation:=range operations {operation.Status="complete"};runcontext.MutateCheckpoint(state,runcontext.CheckpointPatch{"Operations":operations})}
`, []int{3, 4})

}

func checkpointTypeInfo() *types.Info {
	return &types.Info{Selections: map[*ast.SelectorExpr]*types.Selection{}, Defs: map[*ast.Ident]types.Object{}, Uses: map[*ast.Ident]types.Object{}, Types: map[ast.Expr]types.TypeAndValue{}}
}

func checkpointMutationOwner(root, name string) bool {
	return name == filepath.Join(root, "context", "checkpoint_mutation.go")
}

func TestCheckpointMutationOwnerExemptionIsExact(t *testing.T) {
	root := filepath.Join("repo", "go")
	if !checkpointMutationOwner(root, filepath.Join(root, "context", "checkpoint_mutation.go")) {
		t.Fatal("owning mutation implementation was not exempt")
	}
	if checkpointMutationOwner(root, filepath.Join(root, "adapter", "delegate", "checkpoint_mutation.go")) {
		t.Fatal("non-owning basename was exempt")
	}
}

func TestCheckpointPrivateStoreReadIsDetected(t *testing.T) {
	positions := token.NewFileSet()
	tree, err := parser.ParseFile(positions, "private_store.go", `package probe
 type Execution struct {Used int}
 type store struct {value *Execution}
 type ExecutionController struct {checkpoint store}
 func read(c *ExecutionController){_ = c.checkpoint.value}
 `, 0)
	if err != nil {
		t.Fatal(err)
	}
	info := checkpointTypeInfo()
	if _, err := new(types.Config).Check("probe", positions, []*ast.File{tree}, info); err != nil {
		t.Fatal(err)
	}
	if sites := checkpointWriteSites(tree, info); len(sites) != 1 {
		t.Fatalf("private store read: got %d sites, want 1", len(sites))
	}
}

func checkpointMaterializationOwner(root, name string) bool {
	return name == filepath.Join(root, "context", "controller.go")
}
func TestCheckpointMaterializationOwnerExemptionIsExact(t *testing.T) {
	root := filepath.Join("repo", "go")
	if !checkpointMaterializationOwner(root, filepath.Join(root, "context", "controller.go")) {
		t.Fatal("owning reader materialization was not exempt")
	}
	if checkpointMaterializationOwner(root, filepath.Join(root, "adapter", "delegate", "controller.go")) {
		t.Fatal("non-owning controller basename was exempt")
	}
}
