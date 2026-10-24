package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/relay"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
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
		"agent-effort": `func probe(session adapter.RequestSession) { _ = session.AgentCapability().Effort }`,
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

func isRawReasoningSelection(selection *types.Selection) bool {
	// A defined type retains its underlying struct's field objects, even when
	// the receiver has a different name or belongs to another package.
	field, ok := selection.Obj().(*types.Var)
	if !ok || !field.IsField() || field.Pkg() == nil || field.Pkg().Path() != "github.com/Judgment-Pack/judgment-pack-agent/go/adapter" {
		return false
	}
	owner := "Run"
	switch field.Name() {
	case "Thinking", "CriticReasoning":
	case "Effort":
		owner = "Agent"
	default:
		return false
	}
	declaration := field.Pkg().Scope().Lookup(owner)
	if declaration == nil {
		return false
	}
	structure, ok := declaration.Type().Underlying().(*types.Struct)
	if !ok {
		return false
	}
	for i := 0; i < structure.NumFields(); i++ {
		if structure.Field(i) == field {
			return true
		}
	}
	return false
}

// Canonical field objects survive aliases, definitions, pointers and promotion.
// Protect every raw member by default: extending Run never requires a new name here.
func isRawRequestField(field *types.Var) bool {
	if field == nil || !field.IsField() || field.Pkg() == nil {
		return false
	}
	operational := map[string]map[string]bool{
		"Run":     {"Prompt": true, "TestPrompt": true, "Purpose": true, "AllowConversation": true, "Interactive": true, "Cases": true, "Gate": true, "Emit": true, "BeforeRuntimeCall": true, "LanguageInstructions": true, "Context": true},
		"Session": {"Prompt": true, "TestPrompt": true, "Purpose": true, "AllowConversation": true, "Interactive": true, "CallTool": true, "Context": true},
		"Input":   {"AcceptedDocuments": true, "CurrentState": true, "ArtifactProposals": true, "Authorize": true, "Checkpoint": true, "Execution": true, "CurrentRequest": true, "Phase": true, "Plan": true, "Documents": true, "Chat": true, "Runtime": true, "Memory": true, "Objective": true, "Link": true, "DelegationReuse": true, "Approval": true, "Steer": true},
	}
	owners := []string{}
	switch field.Pkg().Path() {
	case "github.com/Judgment-Pack/judgment-pack-agent/go/adapter":
		owners = []string{"Run", "Agent", "PolicyConnection"}
	case "github.com/Judgment-Pack/judgment-pack-agent/go/context":
		owners = []string{"Session", "ModelSession", "AgentSession", "Input"}
	}
	for _, owner := range owners {
		declaration := field.Pkg().Scope().Lookup(owner)
		if declaration == nil {
			continue
		}
		structure, ok := declaration.Type().Underlying().(*types.Struct)
		if !ok {
			continue
		}
		for i := 0; i < structure.NumFields(); i++ {
			if structure.Field(i) == field {
				return !operational[owner][field.Name()]
			}
		}
	}
	return false
}
func isRawRequestSelection(selection *types.Selection) bool {
	field, _ := selection.Obj().(*types.Var)
	return isRawRequestField(field)
}

// A method with a conversion's name is not that package's free function.
func requestFunctionOwner(function *ast.FuncDecl) string {
	if function.Recv == nil {
		return function.Name.Name
	}
	receiver := function.Recv.List[0].Type
	prefix := ""
	if pointer, ok := receiver.(*ast.StarExpr); ok {
		prefix = "*"
		receiver = pointer.X
	}
	if name, ok := receiver.(*ast.Ident); ok {
		return prefix + name.Name + "." + function.Name.Name
	}
	return ""
}

// Definitions and conversions retain the canonical struct's field objects.
// Same-shaped independent structs and structs embedding an admitted view do not.
func protectedRequestRepresentation(value types.Type) string {
	structure, ok := value.Underlying().(*types.Struct)
	if !ok {
		return ""
	}
	for i := 0; i < structure.NumFields(); i++ {
		field := structure.Field(i)
		if field.Pkg() == nil {
			continue
		}
		owners := []string{}
		switch field.Pkg().Path() {
		case "github.com/Judgment-Pack/judgment-pack-agent/go/adapter":
			owners = []string{"RequestSession", "RequestPolicyConnection", "RequestAgent", "PreparedRequest"}
		case "github.com/Judgment-Pack/judgment-pack-agent/go/context":
			owners = []string{"RequestValues"}
		}
		for _, owner := range owners {
			declaration := field.Pkg().Scope().Lookup(owner)
			if declaration == nil {
				continue
			}
			canonical, ok := declaration.Type().Underlying().(*types.Struct)
			if !ok {
				continue
			}
			for j := 0; j < canonical.NumFields(); j++ {
				if canonical.Field(j) == field {
					return owner
				}
			}
		}
	}
	return ""
}

func requestSourceViolations(root string, files *token.FileSet, trees []*ast.File, info *types.Info) []string {
	unexpected := []string{}
	allowed := map[string][][2]token.Pos{}
	for _, tree := range trees {
		path := files.Position(tree.Pos()).Filename
		relative, _ := filepath.Rel(root, path)
		admission := map[string][]string{
			"adapter/request_session.go":      {"requestAgent", "ReadAdmissionReasoning", "AdmissionAgent", "*RequestAgent.Admit", "NewRequestSession", "NewRequestOperations"},
			"adapter/policy.go":               {"PolicyConnectionIDs", "PolicyBinding", "ApplyPolicy", "PolicyFamilies"},
			"adapter/agent.go":                {"*Agent.Admit"},
			"adapter/agent/agent.go":          {"Run"},
			"adapter/eino/run.go":             {"Run"},
			"adapter/delegate/coordinator.go": {"With", "criticReserve", "*coordinator.drive", "*coordinator.taskProvider"},
			"adapter/delegate/policy.go":      {"*coordinator.parentPolicy", "*coordinator.childBinding", "*coordinator.hasRestoredDefinitions"},
			"adapter/delegate/batch.go":       {"*coordinator.plan", "*coordinator.childModelName", "*coordinator.runChild", "*coordinator.childHostTools"},
			"adapter/delegate/agent.go":       {"*coordinator.parentAgent", "*coordinator.childAgent", "*coordinator.driveAgent", "*coordinator.restoreAgent", "*coordinator.continueAgent", "*coordinator.parentInvocation"},
			"adapter/delegate/run.go":         {"*coordinator.drive", "*coordinator.runBatch", "*coordinator.parent", "*coordinator.child", "*coordinator.concurrency"},
			"adapter/delegate/figures.go":     {"*coordinator.childFigures", "*coordinator.batchFigures", "*coordinator.withFigures"},
			"context/session.go":              {"NewSessionContext", "ContextualSession", "*SessionContext.ModelName", "*SessionContext.Models", "*SessionContext.AskHost", "*SessionContext.MessagesContext"},
			"context/policy_table.go":         {"resolveSessionPolicy"},
			"context/delegation_policy.go":    {"delegatedSessionPolicy", "*DelegatedPolicy.restore"},
			"context/planning.go":             {"PlanningSession"},
			"context/types.go":                {"Session.ModelName"},
			"context/critic.go":               {"OfferedReviewTool"},
			"context/memories.go":             {"*SessionContext.MemoryAdmitted"},
			"context/subagents.go":            {"SubagentAdmissionBudget"},
			"context/managed.go":              {"*Boundary.Event"},
			"context/output_boundary.go":      {"*Boundary.guardName", "*SessionContext.ValidateOutputProposal"},
			"context/prompt.go":               {"*SessionContext.AskHost"},
			"context/controller.go":           {"*ExecutionController.Initialize"},
			"context/edits.go":                {"OfferedEditTools", "*SessionContext.OpenDrafts", "*Drafts.Edit"},
		}
		for _, declaration := range tree.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok {
				continue
			}
			for _, name := range admission[filepath.ToSlash(relative)] {
				if requestFunctionOwner(function) == name {
					allowed[path] = append(allowed[path], [2]token.Pos{function.Pos(), function.End()})
				}
			}
		}
	}
	for _, tree := range trees {
		ast.Inspect(tree, func(node ast.Node) bool {
			entry, ok := node.(*ast.KeyValueExpr)
			if !ok {
				return true
			}
			key, ok := entry.Key.(*ast.Ident)
			if !ok {
				return true
			}
			field, _ := info.Uses[key].(*types.Var)
			if !isRawRequestField(field) {
				return true
			}
			position := files.Position(key.Pos())
			permitted := false
			for _, bounds := range allowed[position.Filename] {
				if key.Pos() >= bounds[0] && key.End() <= bounds[1] {
					permitted = true
				}
			}
			if !permitted {
				relative, _ := filepath.Rel(root, position.Filename)
				unexpected = append(unexpected, filepath.ToSlash(relative)+":"+key.Name+":raw-construction")
			}
			return true
		})
	}
	for expression, typed := range info.Types {
		literal, ok := expression.(*ast.CompositeLit)
		if !ok {
			continue
		}
		name := protectedRequestRepresentation(typed.Type)
		if name == "" {
			continue
		}
		position := files.Position(literal.Pos())
		relative, _ := filepath.Rel(root, position.Filename)
		permitted := false
		constructors := map[string][]string{
			"adapter/request_session.go": {"requestAgent", "*RequestSession.ResolvedCapabilities", "NewRequestOperations", "NewRequestSession", "*RequestSession.PolicyBinding"},
			"context/request_values.go":  {"NewRequestValues"},
		}
		for _, tree := range trees {
			for _, declaration := range tree.Decls {
				function, ok := declaration.(*ast.FuncDecl)
				if !ok || literal.Pos() < function.Pos() || literal.End() > function.End() {
					continue
				}
				for _, allowed := range constructors[filepath.ToSlash(relative)] {
					if requestFunctionOwner(function) == allowed {
						permitted = true
					}
				}
			}
		}
		if permitted {
			continue
		}
		unexpected = append(unexpected, filepath.ToSlash(relative)+":"+name+":request-construction")
	}
	for selector, selection := range info.Selections {
		raw := isRawRequestSelection(selection)
		if !raw {
			continue
		}
		position := files.Position(selector.Pos())
		permitted := false
		for _, bounds := range allowed[position.Filename] {
			if selector.Pos() >= bounds[0] && selector.End() <= bounds[1] {
				permitted = true
			}
		}
		writeOnly := false
		for _, tree := range trees {
			ast.Inspect(tree, func(node ast.Node) bool {
				assignment, ok := node.(*ast.AssignStmt)
				if ok && assignment.Tok == token.ASSIGN {
					for _, left := range assignment.Lhs {
						if left == selector {
							writeOnly = true
						}
					}
				}
				return true
			})
		}
		if isRawReasoningSelection(selection) && !writeOnly {
			relative, _ := filepath.Rel(root, position.Filename)
			permitted = filepath.ToSlash(relative) == "adapter/request_session.go"
			if permitted {
				permitted = false
				for _, tree := range trees {
					for _, d := range tree.Decls {
						if fn, ok := d.(*ast.FuncDecl); ok && requestFunctionOwner(fn) == "ReadAdmissionReasoning" && selector.Pos() >= fn.Pos() && selector.End() <= fn.End() {
							permitted = true
						}
					}
				}
			}
		}
		if !permitted {
			relative, _ := filepath.Rel(root, position.Filename)
			unexpected = append(unexpected, filepath.ToSlash(relative)+":"+selector.Sel.Name)
		}
	}
	return unexpected
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
	command := exec.Command("go", "list", "-deps", "-export", "-json", "./adapter/...", "./context")
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
		if strings.HasPrefix(item.ImportPath, "github.com/Judgment-Pack/judgment-pack-agent/go/adapter") || item.ImportPath == "github.com/Judgment-Pack/judgment-pack-agent/go/context" {
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
	t.Run("typed-probes", func(t *testing.T) {
		// These hand-written fixtures must type-check before the source guard runs.
		const source = `package guardprobe
import "github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
type restored adapter.Run
type twice restored
type restoredAgent adapter.Agent
type twiceAgent restoredAgent
type runAlias = adapter.Run
type agentAlias = adapter.Agent
type resolved struct { Thinking, CriticReasoning, Effort string }
func probe(run *adapter.Run, agent *adapter.Agent, admitted adapter.AdmissionReasoning, request adapter.AgentRequest) {
 _ = restored(*run).Thinking
 _ = (*restored)(run).CriticReasoning
 _ = twice(*run).Thinking
 _ = runAlias(*run).Thinking
 _ = restoredAgent(*agent).Effort
 _ = (*twiceAgent)(agent).Effort
 _ = agentAlias(*agent).Effort
 _ = admitted.Thinking
 _ = admitted.Effort
 _ = request.Effort
 _ = resolved{}.Thinking
 _ = resolved{}.CriticReasoning
 _ = resolved{}.Effort
}`
		files := token.NewFileSet()
		file, err := parser.ParseFile(files, "probe.go", source, 0)
		if err != nil {
			t.Fatal(err)
		}
		info := types.Info{Selections: map[*ast.SelectorExpr]*types.Selection{}, Uses: map[*ast.Ident]types.Object{}, Types: map[ast.Expr]types.TypeAndValue{}}
		configuration := types.Config{Importer: importer.ForCompiler(files, "gc", lookup)}
		if _, err := configuration.Check("guardprobe", files, []*ast.File{file}, &info); err != nil {
			t.Fatalf("typed probes do not compile: %v", err)
		}
		caught := map[int]string{}
		for selector, selection := range info.Selections {
			if isRawReasoningSelection(selection) {
				caught[files.Position(selector.Pos()).Line] = selector.Sel.Name
			}
		}
		expected := map[int]string{11: "Thinking", 12: "CriticReasoning", 13: "Thinking", 14: "Thinking", 15: "Effort", 16: "Effort", 17: "Effort"}
		if !reflect.DeepEqual(caught, expected) {
			t.Fatalf("raw field provenance: got %v, want %v", caught, expected)
		}
	})
	unexpected := []string{}
	for _, item := range packages {
		files := token.NewFileSet()
		trees := make([]*ast.File, 0, len(item.GoFiles))
		for _, name := range item.GoFiles {
			path := filepath.Join(item.Dir, name)
			file, parseErr := parser.ParseFile(files, path, nil, 0)
			if parseErr != nil {
				t.Fatal(parseErr)
			}
			trees = append(trees, file)
		}

		info := types.Info{Selections: map[*ast.SelectorExpr]*types.Selection{}, Uses: map[*ast.Ident]types.Object{}, Types: map[ast.Expr]types.TypeAndValue{}}
		configuration := types.Config{Importer: importer.ForCompiler(files, "gc", lookup)}
		if _, err := configuration.Check(item.ImportPath, files, trees, &info); err != nil {
			t.Fatal(err)
		}
		unexpected = append(unexpected, requestSourceViolations(root, files, trees, &info)...)
	}
	if len(unexpected) != 0 {
		t.Fatalf("raw request values outside admission/conversion: %v", unexpected)
	}
}

func TestRequestSourceGuardDeclarationOwnership(t *testing.T) {
	for _, fixture := range []struct {
		name, pkg, path, source string
		want                    []string
	}{
		{"same-name-methods", "adapter", "adapter/request_session.go", `package guardprobe
 type Run struct { ModelName, Thinking string }
 type RequestSession struct { modelName string }
 type RequestAgent struct{}
 type unrelated struct{}
 func NewRequestSession(run *Run) *RequestSession { return &RequestSession{modelName:run.ModelName} }
 func ReadAdmissionReasoning(run *Run) string { return run.Thinking }
 func (unrelated) NewRequestSession(run *Run) *RequestSession { _ = run.ModelName; return &RequestSession{} }
 func (unrelated) ReadAdmissionReasoning(run *Run) string { return run.Thinking }
 func (s *RequestSession) PolicyBinding() RequestSession { return RequestSession{} }
 func (unrelated) PolicyBinding() RequestSession { return RequestSession{} }
 func unrelatedValue(run struct{ModelName string}) string { return run.ModelName }
 func projection(session *RequestSession) *RequestSession { return session }
`, []string{"adapter/request_session.go:ModelName", "adapter/request_session.go:Thinking", "adapter/request_session.go:RequestSession:request-construction", "adapter/request_session.go:RequestSession:request-construction"}},
		{"defined-request-types", "adapter", "adapter/new_writer.go", `package guardprobe
 type RequestSession struct { modelName string }
 type RequestAgent struct { model string }
 type RequestPolicyConnection struct { wire string }
 type PreparedRequest struct { tools []string }
 type reviewConstructed RequestSession
 type twice reviewConstructed
 type requestAlias = RequestSession
 type agentDefinition RequestAgent
 type bindingDefinition RequestPolicyConnection
 type preparedDefinition PreparedRequest
 type unrelated struct { modelName string }
 type admittedProjection struct { *RequestSession }
 func namedReviewConstruction() *RequestSession { return (*RequestSession)(&reviewConstructed{modelName:"unadmitted"}) }
 func definedTwice() RequestSession { return RequestSession(twice{modelName:"unadmitted"}) }
 func alias() RequestSession { return requestAlias{} }
 func agent() RequestAgent { return RequestAgent(agentDefinition{}) }
 func binding() RequestPolicyConnection { return RequestPolicyConnection(bindingDefinition{}) }
 func prepared() PreparedRequest { return PreparedRequest(preparedDefinition{}) }
 func direct() *RequestSession { return &RequestSession{modelName:"unadmitted"} }
 func unrelatedValue() unrelated { return unrelated{modelName:"ordinary"} }
 func projection(session *RequestSession) admittedProjection { return admittedProjection{session} }
`, []string{"adapter/new_writer.go:RequestSession:request-construction", "adapter/new_writer.go:RequestSession:request-construction", "adapter/new_writer.go:RequestSession:request-construction", "adapter/new_writer.go:RequestAgent:request-construction", "adapter/new_writer.go:RequestPolicyConnection:request-construction", "adapter/new_writer.go:PreparedRequest:request-construction", "adapter/new_writer.go:RequestSession:request-construction"}},
		{"defined-request-values", "context", "context/request_values.go", `package guardprobe
 type RequestValues struct { members map[string]string }
 type defined RequestValues
 type twice defined
 type unrelated struct { members map[string]string }
 func NewRequestValues() *RequestValues { return &RequestValues{} }
 func (unrelated) NewRequestValues() *RequestValues { return &RequestValues{} }
 func construction() *RequestValues { return (*RequestValues)(&twice{}) }
 func projection(values *RequestValues) *defined { return (*defined)(values) }
 func unrelatedValue() unrelated { return unrelated{} }
`, []string{"context/request_values.go:RequestValues:request-construction", "context/request_values.go:RequestValues:request-construction"}},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			root := t.TempDir()
			files := token.NewFileSet()
			tree, err := parser.ParseFile(files, filepath.Join(root, filepath.FromSlash(fixture.path)), fixture.source, 0)
			if err != nil {
				t.Fatal(err)
			}
			info := types.Info{Selections: map[*ast.SelectorExpr]*types.Selection{}, Uses: map[*ast.Ident]types.Object{}, Types: map[ast.Expr]types.TypeAndValue{}}
			if _, err := (&types.Config{}).Check("github.com/Judgment-Pack/judgment-pack-agent/go/"+fixture.pkg, files, []*ast.File{tree}, &info); err != nil {
				t.Fatalf("typed ownership fixture does not compile: %v", err)
			}
			got := requestSourceViolations(root, files, []*ast.File{tree}, &info)
			sort.Strings(got)
			sort.Strings(fixture.want)
			if !reflect.DeepEqual(got, fixture.want) {
				t.Fatalf("declaration ownership: got %v, want %v", got, fixture.want)
			}
		})
	}
}

func TestRequestSessionRejectsRawResolvedValues(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	fields := []string{"ModelName", "Wire", "Model", "Agent", "Capabilities", "Tools", "HostTools", "PolicyConnections", "Policy", "PolicyProject", "Context", "Skills", "Subagents", "ChildDefinition"}
	for _, shape := range []string{"direct", "alias", "copy"} {
		t.Run(shape, func(t *testing.T) {
			directory := t.TempDir()
			module := "module requestvalues\n\ngo 1.23\n\nrequire github.com/Judgment-Pack/judgment-pack-agent/go v0.0.0\nreplace github.com/Judgment-Pack/judgment-pack-agent/go => " + filepath.ToSlash(root) + "\n"
			if err := os.WriteFile(filepath.Join(directory, "go.mod"), []byte(module), 0600); err != nil {
				t.Fatal(err)
			}
			body := ""
			receiver := "session"
			if shape == "alias" {
				body = "held:=&session;"
				receiver = "held"
			}
			if shape == "copy" {
				body = "held:=session;"
				receiver = "held"
			}
			for _, field := range fields {
				body += "_ = " + receiver + "." + field + ";"
			}
			source := `package main
import "github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
func probe(session adapter.RequestSession){` + body + `}
func main(){}`
			if err := os.WriteFile(filepath.Join(directory, "main.go"), []byte(source), 0600); err != nil {
				t.Fatal(err)
			}
			command := exec.Command("go", "build", "-gcflags=-e", ".")
			command.Dir = directory
			command.Env = append(os.Environ(), "GOFLAGS=-mod=mod -buildvcs=false")
			output, err := command.CombinedOutput()
			if err == nil {
				t.Fatal("raw resolved fields compiled")
			}
			for _, field := range fields {
				if !strings.Contains(string(output), "."+field+" undefined") {
					t.Fatalf("%s did not fail for its absent raw field: %s", field, output)
				}
			}
		})
	}
}

func TestRequestValuesAddedFieldAndNewWriter(t *testing.T) {
	// New Run and nested Input fields are protected without adding their names.
	for _, fixture := range []struct {
		pkg, owner, field string
		want              int
	}{
		{"adapter", "Run", "AddedResolvedRequest", 2}, {"context", "Input", "AddedResolvedRequest", 2},
		{"adapter", "Agent", "Prompt", 2}, {"context", "ModelSession", "Prompt", 2}, {"adapter", "Run", "CurrentRequest", 2}, {"context", "Input", "Prompt", 2},
		{"adapter", "Run", "Prompt", 0}, {"context", "Input", "CurrentRequest", 0}, {"context", "Session", "CallTool", 0},
	} {
		t.Run(fixture.owner+"/"+fixture.field, func(t *testing.T) {
			source := "package guardprobe\ntype " + fixture.owner + " struct { " + fixture.field + " string }\nfunc newWriter(run *" + fixture.owner + "){ alias:=run;_ = alias." + fixture.field + ";alias." + fixture.field + "=\"write\" }\n"
			files := token.NewFileSet()
			tree, err := parser.ParseFile(files, "new_writer.go", source, 0)
			if err != nil {
				t.Fatal(err)
			}
			info := types.Info{Selections: map[*ast.SelectorExpr]*types.Selection{}}
			if _, err = (&types.Config{}).Check("github.com/Judgment-Pack/judgment-pack-agent/go/"+fixture.pkg, files, []*ast.File{tree}, &info); err != nil {
				t.Fatal(err)
			}
			caught := 0
			for _, selection := range info.Selections {
				if isRawRequestSelection(selection) {
					caught++
				}
			}
			if caught != fixture.want {
				t.Fatalf("added field/new writer caught %d of %d reads/writes", caught, fixture.want)
			}
		})
	}
	if _, present := reflect.TypeOf(RequestSession{}).FieldByName("AddedResolvedRequest"); present {
		t.Fatal("future field leaked through the request surface")
	}
}

func TestRequestValuesDetachBindingsCapabilitiesAndTools(t *testing.T) {
	native := false
	raw := &Run{Wire: "openai-compatible", ModelName: "admitted", Agent: nil, Capabilities: &contract.CapabilityEndpointCapabilities{NativeToolCalls: &native}, HostTools: []runcontext.HostTool{{Name: "evidence", InputSchema: json.RawMessage(`{"type":"object"}`), Execute: func(context.Context, json.RawMessage) (runcontext.ToolResult, error) {
		return runcontext.ToolResult{}, nil
	}}}}
	request := NewRequestSession(raw, runcontext.PolicyMembers{"models.author.reasoning": {Layer: "table", Source: "model-default"}})
	*raw.Capabilities.NativeToolCalls = true
	raw.HostTools[0].InputSchema[0] = 'x'
	if request.AllowsTool("evidence", &raw.HostTools[0], "critic") || request.AllowsTool("evidence", &raw.HostTools[0], "reformat") {
		t.Fatal("phase tool admission widened")
	}
	capabilities, err := request.ResolvedCapabilities()
	if err != nil || capabilities.NativeToolCalls {
		t.Fatalf("raw capability alias reached request: %+v %v", capabilities, err)
	}
	if got := string(request.OfferedHostTools()[0].InputSchema); got != `{"type":"object"}` {
		t.Fatalf("raw tool alias reached request: %s", got)
	}
	held := request.OfferedHostTools()
	held[0].InputSchema[0] = 'x'
	if string(request.OfferedHostTools()[0].InputSchema) != `{"type":"object"}` {
		t.Fatal("returned tool alias changed admitted data")
	}
}

// Distinct raw bindings make restoring the author for a critic observable.
func TestRequestValuesSelectAdmittedRoleBindings(t *testing.T) {
	call := func(context.Context, string, relay.Request) (*http.Response, error) { return nil, nil }
	native := false
	raw := &Run{Wire: "openai-compatible", ModelName: "author-model", Model: call,
		Context: &runcontext.Input{Connection: "primary"},
		Models: func() (runcontext.RunModels, error) {
			return runcontext.RunModels{Critic: "review-model", Child: "child-model"}, nil
		},
		PolicyConnections: map[string]PolicyConnection{"reviewer": {Wire: "openai-compatible", ModelName: "review-model", Model: call, Capabilities: &contract.CapabilityEndpointCapabilities{NativeToolCalls: &native}}},
	}
	members := runcontext.PolicyMembers{
		"models.author.provider": {Value: "primary", Layer: "run"}, "models.author.model": {Value: "author-model", Layer: "run"},
		"models.critic.provider": {Value: "reviewer", Layer: "run"}, "models.critic.model": {Value: "review-model", Layer: "run"}, "models.critic.reasoning": {Value: "high", Layer: "run"},
	}
	request := NewRequestSession(raw, members)
	raw.PolicyConnections["reviewer"] = PolicyConnection{ModelName: "changed-raw"}
	binding, err := request.PolicyBinding("critic")
	if err != nil {
		t.Fatal(err)
	}
	capabilities, err := binding.ResolvedCapabilities()
	if err != nil || binding.ModelID() != "review-model" || binding.Family() != "openai-compatible" || capabilities.NativeToolCalls || request.Reasoning("critic") != "high" {
		t.Fatalf("critic lost admitted binding: %s %s %+v %v", binding.ModelID(), binding.Family(), capabilities, err)
	}
	own, err := request.PolicyBinding("author")
	if err != nil || own.ModelID() != "author-model" {
		t.Fatalf("author binding changed: %s %v", own.ModelID(), err)
	}
	members["models.critic.provider"] = runcontext.PolicyMember{Value: "primary", Layer: "run"}
	same := NewRequestSession(raw, members)
	binding, err = same.PolicyBinding("critic")
	if err != nil || binding.ModelID() != "review-model" {
		t.Fatalf("same-connection critic model changed: %s %v", binding.ModelID(), err)
	}
}
