package eino_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// forbidden is, by import path, what an adapter package may not name: a client, a transport, a
// dial or the process's default transport. The check reads import paths, not the names a file
// gives them, so an alias (nh "net/http") or a dot import is read as the package it is.
var forbidden = map[string][]string{
	"net/http":   {"Client", "Transport", "DefaultClient", "DefaultTransport", "Get", "Post", "PostForm", "Head", "NewRequest", "NewRequestWithContext"},
	"net":        {"Dial", "DialTimeout", "Dialer", "DialTCP", "DialUDP", "DialUnix", "DialIP", "Listen"},
	"crypto/tls": {"Dial", "DialWithDialer", "Dialer", "Client"},
}

// stubImporter hands go/types an empty package for every import path, so the file's identifiers
// are resolved to the import they name without compiling (or loading) any dependency. The errors
// this leaves, a member the empty package lacks, are not the check's concern and are ignored.
type stubImporter struct{}

func (stubImporter) Import(path string) (*types.Package, error) {
	name := path[strings.LastIndex(path, "/")+1:]
	pkg := types.NewPackage(path, name)
	pkg.MarkComplete()
	return pkg, nil
}

// reaches lists what a file names from a forbidden package: each `pkg.Member` through the import
// the identifier resolves to (the package's path, whatever the file calls it), and each dot import
// of a forbidden package, whose members need no qualifier.
func reaches(t *testing.T, fset *token.FileSet, file *ast.File) []string {
	t.Helper()
	info := &types.Info{Uses: map[*ast.Ident]types.Object{}}
	config := types.Config{Importer: stubImporter{}, Error: func(error) {}}
	_, _ = config.Check(file.Name.Name, fset, []*ast.File{file}, info)
	var found []string
	for _, spec := range file.Imports {
		path := strings.Trim(spec.Path.Value, `"`)
		if _, banned := forbidden[path]; banned && spec.Name != nil && spec.Name.Name == "." {
			found = append(found, "a dot import of "+path)
		}
	}
	ast.Inspect(file, func(node ast.Node) bool {
		selector, ok := node.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		qualifier, ok := selector.X.(*ast.Ident)
		if !ok {
			return true
		}
		imported, ok := info.Uses[qualifier].(*types.PkgName)
		if !ok {
			return true
		}
		for _, member := range forbidden[imported.Imported().Path()] {
			if selector.Sel.Name == member {
				found = append(found, imported.Imported().Path()+"."+member)
			}
		}
		return true
	})
	return found
}

// The check itself is held: it names a forbidden member through an alias, through the plain name,
// and a dot import, and passes a file that only names what a client may.
func TestTheTransportCheckReadsImportPaths(t *testing.T) {
	cases := map[string]struct {
		source string
		want   []string
	}{
		"an alias":                            {"package p\nimport nh \"net/http\"\nvar _ = nh.DefaultTransport\n", []string{"net/http.DefaultTransport"}},
		"plain":                               {"package p\nimport \"net/http\"\nvar _ = http.Client{}\n", []string{"net/http.Client"}},
		"a dot":                               {"package p\nimport . \"net/http\"\nvar _ = Client{}\n", []string{"a dot import of net/http"}},
		"tls, aliased":                        {"package p\nimport t \"crypto/tls\"\nvar _ = t.Dial\n", []string{"crypto/tls.Dial"}},
		"allowed":                             {"package p\nimport \"net/http\"\nvar _ = http.StatusOK\nvar _ http.Header\n", nil},
		"a local name that is not the import": {"package p\nimport \"strings\"\nfunc f(http struct{ Client int }) int { return http.Client }\nvar _ = strings.ToLower\n", nil},
	}
	for name, c := range cases {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, name+".go", c.source, 0)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got := reaches(t, fset, file)
		if strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("%s: found %v, want %v", name, got, c.want)
		}
	}
}

// K1a beyond the process's default transport: no package an adapter run reaches builds a transport
// or a client of its own, dials, or reaches for the default one. The relay capability (go/relay,
// whose handler the host binds) is the only way out; the certification seals http.DefaultTransport
// while a leg runs, and the dependency allow-list (go/internal/depcheck) holds the modules beneath.
func TestNoAdapterPackageOpensATransportOfItsOwn(t *testing.T) {
	checked := 0
	for _, dir := range []string{".", "../turn", "../wire", "../delegate", "..", "../../context", "../../failure"} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			path := filepath.Join(dir, name)
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			checked++
			for _, found := range reaches(t, fset, file) {
				t.Errorf("%s uses %s", path, found)
			}
		}
	}
	if checked < 20 {
		t.Fatalf("only %d files checked", checked)
	}
}
