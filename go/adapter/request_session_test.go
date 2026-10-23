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

func TestRequestSessionRejectsRawReasoningShapes(t *testing.T) {
	cases := map[string]string{
		"direct":       `func probe(session adapter.RequestSession) { _ = session.Thinking }`,
		"alias":        `func probe(session adapter.RequestSession) { held := session; _ = held.Thinking }`,
		"destructure":  `func probe(session adapter.RequestSession) { held := session; _ = held.Thinking }`,
		"computed":     `func probe(session adapter.RequestSession) { _ = session["Thinking"] }`,
		"spread":       `func probe(session adapter.RequestSession) { copied := session; _ = copied.Thinking }`,
		"helper":       `func raw(run *adapter.Run) string { return run.Agent.Effort }; func probe(session adapter.RequestSession) { _ = raw(&session) }`,
		"agent-effort": `func probe(session adapter.RequestSession) { _ = session.Agent.Effort }`,
	}
	for _, shape := range []string{"direct", "alias", "destructure", "computed", "spread", "helper", "agent-effort"} {
		t.Run(shape, func(t *testing.T) {
			root, err := filepath.Abs("..")
			if err != nil {
				t.Fatal(err)
			}
			directory := t.TempDir()
			module := "module requestboundary\n\ngo 1.23\n\nrequire github.com/Judgment-Pack/judgment-pack-agent/go v0.0.0\nreplace github.com/Judgment-Pack/judgment-pack-agent/go => " + filepath.ToSlash(root) + "\n"
			if err := os.WriteFile(filepath.Join(directory, "go.mod"), []byte(module), 0o600); err != nil {
				t.Fatal(err)
			}
			imports := `import "github.com/Judgment-Pack/judgment-pack-agent/go/adapter"`
			source := "package main\n\n" + imports + "\n\n" + cases[shape] + "\nfunc main() {}\n"
			if err := os.WriteFile(filepath.Join(directory, "main.go"), []byte(source), 0o600); err != nil {
				t.Fatal(err)
			}
			command := exec.Command("go", "build", ".")
			command.Dir = directory
			command.Env = append(os.Environ(), "GOFLAGS=-mod=mod -buildvcs=false")
			output, err := command.CombinedOutput()
			if err == nil {
				t.Fatal("raw request boundary fixture compiled")
			}
			message := string(output)
			if shape == "helper" {
				if !strings.Contains(message, "cannot use &session") || !strings.Contains(message, "*adapter.Run") {
					t.Fatalf("helper failed for the wrong reason: %s", message)
				}
			} else if shape == "computed" {
				if !strings.Contains(message, "cannot index session") {
					t.Fatalf("computed failed for the wrong reason: %s", message)
				}
			} else if shape == "agent-effort" {
				if !strings.Contains(message, "Effort undefined") {
					t.Fatalf("agent effort failed for the wrong reason: %s", message)
				}
			} else if !strings.Contains(message, "Thinking undefined") {
				t.Fatalf("%s failed for the wrong reason: %s", shape, message)
			}
		})
	}
}

func TestRawReasoningReadsStayInTheAdmissionConversion(t *testing.T) {
	root := os.Getenv("JP_REASONING_GUARD_ROOT")
	if root == "" {
		root = ".."
	} else {
		root = filepath.Join(root, "go")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("go", "list", "-deps", "-export", "-json", "./adapter/...")
	command.Dir = root
	command.Env = append(os.Environ(), "GOFLAGS=-buildvcs=false")
	listed, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	type listedPackage struct {
		Dir, ImportPath, Export string
		GoFiles                 []string
	}
	packages := []listedPackage{}
	exports := map[string]string{}
	decoder := json.NewDecoder(bytes.NewReader(listed))
	for {
		var item listedPackage
		if err := decoder.Decode(&item); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if item.Export != "" {
			exports[item.ImportPath] = item.Export
		}
		if strings.HasPrefix(item.ImportPath, "github.com/Judgment-Pack/judgment-pack-agent/go/adapter") {
			packages = append(packages, item)
		}
	}
	lookup := func(path string) (io.ReadCloser, error) {
		file := exports[path]
		if file == "" {
			return nil, errors.New("no export data for " + path)
		}
		return os.Open(file)
	}
	unexpected := []string{}
	for _, item := range packages {
		files := token.NewFileSet()
		trees := make([]*ast.File, 0, len(item.GoFiles))
		allowed := map[string][2]token.Pos{}
		written := map[*ast.SelectorExpr]bool{}
		for _, name := range item.GoFiles {
			path := filepath.Join(item.Dir, name)
			file, parseErr := parser.ParseFile(files, path, nil, 0)
			if parseErr != nil {
				t.Fatal(parseErr)
			}
			trees = append(trees, file)
			if name == "request_session.go" {
				for _, declaration := range file.Decls {
					function, ok := declaration.(*ast.FuncDecl)
					if ok && function.Name.Name == "ReadAdmissionReasoning" {
						allowed[path] = [2]token.Pos{function.Pos(), function.End()}
					}
				}
			}
		}
		for _, file := range trees {
			ast.Inspect(file, func(node ast.Node) bool {
				assignment, ok := node.(*ast.AssignStmt)
				if !ok {
					return true
				}
				for _, left := range assignment.Lhs {
					ast.Inspect(left, func(part ast.Node) bool {
						if selector, ok := part.(*ast.SelectorExpr); ok {
							written[selector] = true
						}
						return true
					})
				}
				return true
			})
		}
		info := types.Info{Selections: map[*ast.SelectorExpr]*types.Selection{}}
		configuration := types.Config{Importer: importer.ForCompiler(files, "gc", lookup)}
		if _, err := configuration.Check(item.ImportPath, files, trees, &info); err != nil {
			t.Fatal(err)
		}
		for selector, selection := range info.Selections {
			if written[selector] {
				continue
			}
			receiver := selection.Recv()
			if pointer, ok := receiver.(*types.Pointer); ok {
				receiver = pointer.Elem()
			}
			named, ok := receiver.(*types.Named)
			if !ok || named.Obj().Pkg() == nil || named.Obj().Pkg().Path() != "github.com/Judgment-Pack/judgment-pack-agent/go/adapter" {
				continue
			}
			raw := named.Obj().Name() == "Run" && (selector.Sel.Name == "Thinking" || selector.Sel.Name == "CriticReasoning") || named.Obj().Name() == "Agent" && selector.Sel.Name == "Effort"
			if !raw {
				continue
			}
			position := files.Position(selector.Pos())
			range_, permitted := allowed[position.Filename]
			if !permitted || selector.Pos() < range_[0] || selector.End() > range_[1] {
				relative, _ := filepath.Rel(root, position.Filename)
				unexpected = append(unexpected, filepath.ToSlash(relative)+":"+selector.Sel.Name)
			}
		}
	}
	if len(unexpected) != 0 {
		t.Fatalf("raw reasoning reads outside ReadAdmissionReasoning: %v", unexpected)
	}
}
