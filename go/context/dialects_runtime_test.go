package runcontext

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Read/preview/retention remain handwritten host seams; all semantic calls use the actual selected Runtime CLI.
func TestDialectActualRuntime(t *testing.T) {
	executable := os.Getenv("JPACK_DIALECT_RUNTIME")
	if executable == "" {
		t.Skip("actual candidate Runtime must be explicitly selected")
	}
	if !filepath.IsAbs(executable) {
		t.Fatal("Runtime source build must be an absolute selection")
	}
	if os.Getenv("TMPDIR") == "" {
		t.Fatal("session scratch/TMPDIR required")
	}
	modes := []string{"text", "decimal", "number", "boolean", "research-value", "omitted", "generated", "planned"}
	if selected := os.Getenv("JPACK_DIALECT_RUNTIME_MODE"); selected != "" {
		if selected != "generated" {
			t.Fatal("unsupported focused Runtime mode")
		}
		modes = []string{"generated"}
	}
	calls := 0
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			w := newDialectWorld(t)
			w.declarations = retainedPolicyFixture(t)
			if mode == "generated" {
				raw, e := os.ReadFile("../../contract/testdata/dialect-authoring/generated-read.json")
				if e != nil {
					t.Fatal(e)
				}
				w.snapshot.RetainedRead = &DialectRetainedRead{}
				if json.Unmarshal(raw, w.snapshot.RetainedRead) != nil {
					t.Fatal("bad source template")
				}
			}
			if mode == "planned" {
				w.snapshot.RecordCount = 0
				w.declarations = nil
				for i := range w.snapshot.Columns {
					w.snapshot.Columns[i].Samples = []DialectSample{}
					w.snapshot.Columns[i].Research = nil
				}
			}
			directory := t.TempDir()
			packFile := filepath.Join(directory, "pack.json")
			w.ports.Runtime = func(c context.Context, name string, args map[string]string) (ToolResult, error) {
				calls++
				if calls > len(modes)*3 {
					t.Fatal("actual Runtime call bound exceeded")
				}
				operation := "contract"
				if name == "dialect_validate" {
					operation = "validate"
				}
				if name == "dialect_check" {
					operation = "check"
				}
				argv := []string{"dialect", operation, "--draft-contract", args["draftContract"], "--draft-profile", args["draftProfile"]}
				if operation != "contract" {
					argv = append(argv, "-")
				}
				if operation == "check" {
					if e := os.WriteFile(packFile, []byte(args["pack"]), 0600); e != nil {
						return ToolResult{}, e
					}
					argv = append(argv, "--pack", packFile)
				}
				argv = append(argv, "--format", "json")
				cmd := exec.CommandContext(c, executable, argv...)
				cmd.Stdin = strings.NewReader(args["document"])
				output, e := cmd.Output()
				if evidence := os.Getenv("JPACK_DIALECT_EVIDENCE"); evidence != "" {
					record, _ := json.MarshalIndent(map[string]any{"mode": mode, "operation": operation, "args": args, "reportJson": string(output)}, "", "  ")
					if err := os.WriteFile(filepath.Join(evidence, fmt.Sprintf("go-runtime-%02d.json", calls)), record, 0600); err != nil {
						return ToolResult{}, err
					}
				}
				if c.Err() != nil {
					return ToolResult{}, c.Err()
				}
				if _, ok := e.(*exec.ExitError); e != nil && !ok {
					return ToolResult{}, e
				}
				result := ResultOf([]Content{TextContent(string(output))}, nil)
				if e != nil {
					yes := true
					result.IsError = &yes
				}
				return result, nil
			}
			w.tools = NewDialectTools(w.ports)
			a := w.args(w.read())
			kind := mode
			if mode == "generated" || mode == "planned" {
				kind = "decimal"
			}
			c := DialectConversion{Kind: kind}
			if mode == "boolean" {
				c.TrueValue = dialectStringPointer("yes")
				c.FalseValue = dialectStringPointer("no")
			}
			column := "typed"
			if mode == "planned" {
				column = "missing"
			}
			a["facts"] = []DialectFactBinding{{Target: "/amount", Column: column, Conversion: c}}
			if mode == "omitted" {
				a["facts"] = []DialectFactBinding{}
				a["evidence"] = []DialectEvidenceBinding{}
			}
			if _, e := w.propose(a); e != nil {
				t.Fatal(e)
			}
			if len(w.retained) != 1 || w.retained[0].Validation["status"] != "document-valid" {
				t.Fatal("actual Runtime entry did not validate declared policy")
			}
			if mode == "generated" {
				if evidence := os.Getenv("JPACK_DIALECT_EVIDENCE"); evidence != "" {
					checkpoint, err := json.MarshalIndent(w.drafts[0], "", "  ")
					if err != nil {
						t.Fatal(err)
					}
					if err = os.WriteFile(filepath.Join(evidence, "go-generated-checkpoint.json"), checkpoint, 0600); err != nil {
						t.Fatal(err)
					}
				}
				raw, err := os.ReadFile("../../contract/testdata/dialect-authoring/generated-read-expected.json")
				if err != nil {
					t.Fatal(err)
				}
				var expected struct {
					Document map[string]any `json:"document"`
					Digest   string         `json:"digest"`
				}
				if err := json.Unmarshal(raw, &expected); err != nil {
					t.Fatal(err)
				}
				document, err := dialectCanonical(expected.Document)
				if err != nil {
					t.Fatal(err)
				}
				proposal := w.retained[0]
				if proposal.DocumentJSON != document {
					t.Fatal("generated complete document differs from shared expectation")
				}
				for _, report := range []map[string]any{proposal.Validation, proposal.Coverage} {
					if report["document"] != document || report["digest"] != expected.Digest {
						t.Fatal("Runtime generated complete document/digest differs from shared expectation")
					}
				}
			}
		})
	}
	if calls != len(modes)*3 {
		t.Fatal("incomplete actual Runtime call count", calls)
	}
}
