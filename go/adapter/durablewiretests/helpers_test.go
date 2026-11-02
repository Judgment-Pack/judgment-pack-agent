package durablewiretests

// Copied from go/adapter/eino, whose tests use the same ones: a test binary cannot import another's test
// files (#254).

import (
	"encoding/json"
	"go/ast"
	"go/token"
	"go/types"
	"io"
	"net/http"
	"strings"
	"testing"
)

// readKeyCall is one streamed completion whose call carries exactly the argument text given.
func readKeyCall(id, name, arguments string) *http.Response {
	chunk := map[string]any{"id": "fixture", "object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": id, "type": "function", "function": map[string]any{"name": name, "arguments": arguments}}}}, "finish_reason": "tool_calls"}}}
	body, _ := json.Marshal(chunk)
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: " + string(body) + "\n\ndata: [DONE]\n\n"))}
}

// quarantineFinal is one streamed completion that answers "Done." and stops.
func quarantineFinal() *http.Response {
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: {\"id\":\"fixture\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Done.\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"fixture\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))}
}

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
