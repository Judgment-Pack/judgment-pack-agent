package runcontext

import (
	"context"
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
)

// #269: the selector is the final Spec 0.3.0 tuple Runner v0.27.0 compiles, byte for byte. The
// vector is a handwritten copy of Runner's declaration with its source cited.
func TestDialectSelectorIsRunnerInstalledConversionSpec(t *testing.T) {
	raw, err := os.ReadFile("../../contract/testdata/dialect-authoring/runner-v0.27.0-installed-conversion-spec.json")
	if err != nil {
		t.Fatal(err)
	}
	var vector struct {
		Source struct {
			Repository, Tag, Commit, Path, Symbol string
		}
		SourceLine string `json:"sourceLine"`
		JSON       string `json:"json"`
	}
	if err = json.Unmarshal(raw, &vector); err != nil {
		t.Fatal(err)
	}
	if vector.Source.Repository != "Judgment-Pack/judgment-pack-runner" || vector.Source.Tag != "v0.27.0" || vector.Source.Commit != "1164d78cb401f169e63623700853db5c6e635848" || vector.Source.Path != "internal/runner/column_role_v1.go" || vector.Source.Symbol != "installedConversionSpec" {
		t.Fatal("the vector does not cite Runner v0.27.0's installedConversionSpec")
	}
	encoded, err := json.Marshal(DialectRetainedColumnDraft().Spec)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != vector.JSON {
		t.Fatalf("selector spec %s is not Runner v0.27.0's installed tuple %s", encoded, vector.JSON)
	}
	// The vector's two forms agree: the values written in Runner's declaration, in its member order.
	held := regexp.MustCompile(`^var installedConversionSpec = ConversionSpec\{Version: "([^"]*)", Revision: "([^"]*)", SchemaSHA256: "([^"]*)", CorpusSHA256: "([^"]*)"\}$`).FindStringSubmatch(vector.SourceLine)
	if held == nil {
		t.Fatal("the source line is not Runner's declaration")
	}
	fromSource, _ := json.Marshal(struct {
		Version      string `json:"version"`
		Revision     string `json:"revision"`
		SchemaSHA256 string `json:"schemaSha256"`
		CorpusSHA256 string `json:"corpusSha256"`
	}{held[1], held[2], held[3], held[4]})
	if string(fromSource) != vector.JSON {
		t.Fatalf("the vector's source line and json differ: %s", fromSource)
	}
	if c := DialectRetainedColumnDraft(); c.Contract != "retained-column/1" || c.Profile != "base" {
		t.Fatal("selector contract or profile changed")
	}
}

func TestDialectPublishedFinalIdentity(t *testing.T) {
	raw, err := os.ReadFile("../../contract/testdata/dialect-authoring/authoring.json")
	if err != nil {
		t.Fatal(err)
	}
	var current struct {
		ConversionContract DialectConversionContract `json:"conversionContract"`
	}
	if err = json.Unmarshal(raw, &current); err != nil {
		t.Fatal(err)
	}
	if DialectRetainedColumnDraft() != current.ConversionContract {
		t.Fatal("published final tuple differs from shared current fixture")
	}
	raw, err = os.ReadFile("../../contract/testdata/dialect-authoring/superseded-prototype-identity.json")
	if err != nil {
		t.Fatal(err)
	}
	var superseded DialectConversionContract
	if err = json.Unmarshal(raw, &superseded); err != nil {
		t.Fatal(err)
	}
	w := newDialectWorld(t)
	w.ports.ConversionContract = &superseded
	w.tools = NewDialectTools(w.ports)
	if _, err = w.propose(w.args(w.read())); err == nil || !strings.Contains(err.Error(), "exact package-supported retained-column opt-in") {
		t.Fatal("superseded prototype identity reached Runtime", err)
	}
	if strings.Contains(strings.Join(w.calls, ","), "get_dialect_contract") || strings.Contains(strings.Join(w.calls, ","), "preview") || len(w.drafts) == 0 || len(w.retained) != 0 {
		t.Fatal("superseded identity crossed the consumer boundary")
	}
}

// #269: a host that still opts into the Spec 0.3.0-rc.2 tuple v0.12.0 selected is refused before
// the Runtime, as any other tuple that is not the package-supported one.
func TestDialectRC2IdentityIsNoLongerTheOptIn(t *testing.T) {
	raw, err := os.ReadFile("../../contract/testdata/dialect-authoring/superseded-rc2-identity.json")
	if err != nil {
		t.Fatal(err)
	}
	var rc2 DialectConversionContract
	if err = json.Unmarshal(raw, &rc2); err != nil {
		t.Fatal(err)
	}
	current := DialectRetainedColumnDraft()
	if rc2 == current || rc2.Spec.SchemaSHA256 != current.Spec.SchemaSHA256 || rc2.Spec.CorpusSHA256 != current.Spec.CorpusSHA256 {
		t.Fatal("the RC2 fixture is not the same leaf under its candidate identity")
	}
	w := newDialectWorld(t)
	w.ports.ConversionContract = &rc2
	w.tools = NewDialectTools(w.ports)
	if _, err = w.propose(w.args(w.read())); err == nil || !strings.Contains(err.Error(), "exact package-supported retained-column opt-in") {
		t.Fatal("RC2 identity reached Runtime", err)
	}
	if strings.Contains(strings.Join(w.calls, ","), "get_dialect_contract") || strings.Contains(strings.Join(w.calls, ","), "preview") || len(w.retained) != 0 {
		t.Fatal("RC2 identity crossed the consumer boundary")
	}
}

func TestDialectHistoricalIdentityDoesNotAuthorizeProposal(t *testing.T) {
	raw, err := os.ReadFile("../../contract/testdata/dialect-authoring/superseded-private-draft-identity.json")
	if err != nil {
		t.Fatal(err)
	}
	var historical DialectConversionContract
	if err = json.Unmarshal(raw, &historical); err != nil {
		t.Fatal(err)
	}
	w := newDialectWorld(t)
	w.ports.ConversionContract = &historical
	bound := w.read()
	w.tools = NewDialectTools(w.ports)
	if _, err = w.propose(w.args(bound)); err == nil || !strings.Contains(err.Error(), "read the exact") {
		t.Fatal("historical intent bypassed fresh read")
	}
	if _, err = w.propose(w.args(w.read())); err == nil || !strings.Contains(err.Error(), "exact package-supported retained-column opt-in") {
		t.Fatal("historical identity authorized new proposal", err)
	}
	if strings.Contains(strings.Join(w.calls, ","), "get_dialect_contract") || strings.Contains(strings.Join(w.calls, ","), "preview") || len(w.retained) != 0 {
		t.Fatal("historical intent crossed proposal boundary")
	}
	current := DialectRetainedColumnDraft()
	w.ports.ConversionContract = &current
	w.tools = NewDialectTools(w.ports)
	if _, err = w.propose(w.args(w.read())); err != nil {
		t.Fatal(err)
	}
	if w.retained[0].ConversionContract != current || historical == current {
		t.Fatal("historical identity was rebound")
	}
}

func TestDialectPublishedIdentityEveryReport(t *testing.T) {
	for _, operation := range []string{"get_dialect_contract", "dialect_validate", "dialect_check"} {
		for _, member := range []string{"version", "revision", "schemaSha256", "corpusSha256", "specRevision", "specification", "maturity", "role", "profile"} {
			t.Run(operation+"/"+member, func(t *testing.T) {
				w := newDialectWorld(t)
				original := w.ports.Runtime
				w.ports.Runtime = func(c context.Context, name string, args map[string]string) (ToolResult, error) {
					if args["draftContract"] != "retained-column/1@27cb29bf13e4a2d58d69fe19c7ebfeb762b014fc" || args["draftProfile"] != "base" {
						t.Fatal("wrong selected Runtime tuple")
					}
					result, err := original(c, name, args)
					if err != nil || name != operation {
						return result, err
					}
					var report map[string]any
					if err = json.Unmarshal([]byte(result.Content[0].Value().Get("text").Str()), &report); err != nil {
						t.Fatal(err)
					}
					switch member {
					case "version", "revision", "schemaSha256", "corpusSha256":
						report["spec"].(map[string]any)[member] = "private-draft"
					case "specRevision":
						report[member] = "ade3914b0b81ef299e5b9a8d6147f37ff3f0976c"
					case "specification":
						report[member] = "https://github.com/Judgment-Pack/judgment-pack-spec/blob/" + DialectRetainedColumnDraft().Spec.Revision + "/rfcs/0029-retained-column-conversions.md"
					case "maturity":
						report[member] = "experimental-draft"
					case "role":
						report[member] = "preparation"
					case "profile":
						report[member] = "extended"
					}
					raw, _ := json.Marshal(report)
					return ResultOf([]Content{TextContent(string(raw))}, nil), nil
				}
				w.tools = NewDialectTools(w.ports)
				if _, err := w.propose(w.args(w.read())); err == nil {
					t.Fatal("published identity drift authorized proposal")
				}
				if len(w.retained) != 0 || strings.Contains(strings.Join(w.calls, ","), "preview") {
					t.Fatal("identity drift crossed preview boundary")
				}
				if w.drafts[0].Candidate.ConversionContract != DialectRetainedColumnDraft() {
					t.Fatal("draft lost exact supported tuple")
				}
				if operation != "get_dialect_contract" {
					check := w.drafts[len(w.drafts)-1].Check
					count := 1
					if operation == "dialect_check" {
						count = 2
					}
					if check == nil || check.Failure == nil || check.Failure.Code != "check-refused" || len(check.Results) != count {
						t.Fatal("actual refused evidence not retained")
					}
				}
			})
		}
	}
}
