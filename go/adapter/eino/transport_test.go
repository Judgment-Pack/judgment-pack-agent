package eino_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// K1a beyond the process's default transport: no package an adapter run reaches builds a transport
// or a client of its own, dials, or reaches for the default one. The relay capability (go/relay,
// whose handler the host binds) is the only way out; the certification seals http.DefaultTransport
// while a leg runs, and the dependency allow-list (go/internal/depcheck) holds the modules beneath.
func TestNoAdapterPackageOpensATransportOfItsOwn(t *testing.T) {
	forbidden := map[string][]string{
		"http": {"Client", "Transport", "DefaultClient", "DefaultTransport", "Get", "Post", "PostForm", "Head", "NewRequest", "NewRequestWithContext"},
		"net":  {"Dial", "DialTimeout", "Dialer", "DialTCP", "DialUDP", "DialUnix", "DialIP", "Listen"},
		"tls":  {"Dial", "DialWithDialer", "Dialer", "Client"},
	}
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
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			checked++
			ast.Inspect(file, func(node ast.Node) bool {
				selector, ok := node.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				pkg, ok := selector.X.(*ast.Ident)
				if !ok {
					return true
				}
				for _, banned := range forbidden[pkg.Name] {
					if selector.Sel.Name == banned {
						t.Errorf("%s uses %s.%s", path, pkg.Name, banned)
					}
				}
				return true
			})
		}
	}
	if checked < 20 {
		t.Fatalf("only %d files checked", checked)
	}
}
