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

func checkpointWriteSites(file *ast.File, info *types.Info) []token.Pos {
	found := []token.Pos{}
	record := func(expression ast.Expr) {
		caught := false
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
				record(left)
			}
		case *ast.IncDecStmt:
			record(held.X)
		case *ast.UnaryExpr:
			if held.Op == token.AND {
				if _, allocation := held.X.(*ast.CompositeLit); allocation {
					return true
				}
				record(held.X)
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
		info := &types.Info{Selections: map[*ast.SelectorExpr]*types.Selection{}}
		config := types.Config{Importer: importer.ForCompiler(positions, "gc", lookup)}
		if _, err := config.Check(entry.ImportPath, positions, trees, info); err != nil {
			t.Fatal(err)
		}
		for _, tree := range trees {
			name := positions.Position(tree.Pos()).Filename
			if filepath.Base(name) == "checkpoint_mutation.go" {
				continue
			}
			for _, position := range checkpointWriteSites(tree, info) {
				// Materializing a validated read is not a live writer. The factory owns its new object.
				materialization := false
				if filepath.Base(name) == "controller.go" {
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
 _=&c.State().Sources
 unrelated:=struct{Used int}{};unrelated.Used++
}`
	positions := token.NewFileSet()
	tree, err := parser.ParseFile(positions, "new_writer.go", source, 0)
	if err != nil {
		t.Fatal(err)
	}
	info := &types.Info{Selections: map[*ast.SelectorExpr]*types.Selection{}}
	config := types.Config{Importer: importer.ForCompiler(positions, "gc", lookup)}
	if _, err := config.Check("probe", positions, []*ast.File{tree}, info); err != nil {
		t.Fatal(err)
	}
	if sites := checkpointWriteSites(tree, info); len(sites) != 4 {
		t.Fatalf("new writer: got %d sites, want 4", len(sites))
	}
}
