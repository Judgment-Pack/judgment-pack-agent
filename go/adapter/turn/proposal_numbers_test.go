package turn

import (
	"encoding/json"
	"github.com/Judgment-Pack/judgment-pack-agent/go/proposal"
	"os"
	"strings"
	"testing"
)

func TestProposalNumericSources(t *testing.T) {
	data, err := os.ReadFile("../../../contract/vectors/proposal/numeric-admission.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors struct {
		Cases []struct{ Name, Source, Canonical, Refusal string }
	}
	if err = json.Unmarshal(data, &vectors); err != nil {
		t.Fatal(err)
	}
	for _, row := range vectors.Cases {
		t.Run(row.Name, func(t *testing.T) {
			got, err := ReadProposalDocument(json.RawMessage(row.Source))
			if row.Refusal != "" {
				if problem, ok := err.(*proposal.Error); !ok || problem.Kind != row.Refusal {
					t.Fatalf("expected %s, got %s %v", row.Refusal, got, err)
				}
			} else if err != nil || string(got) != row.Canonical {
				t.Fatalf("canonical source: %s %v, want %s", got, err, row.Canonical)
			}
			args, _ := json.Marshal(map[string]string{"documentJson": row.Source})
			native, err := ReadProposedPack(args)
			if row.Refusal != "" {
				if err == nil || failureOf(t, err).Code != "invalid-tool-input" {
					t.Fatalf("native refusal: %v", err)
				}
			} else if err != nil || string(native.Document) != row.Canonical {
				t.Fatalf("native source: %s %v", native.Document, err)
			}
		})
	}
}
func TestProposalNumericNativeSourceRequired(t *testing.T) {
	for _, document := range []string{`{"id":"p"}`, `null`, `{"n":1}`, `{"n":null}`, `{"n":9007199254740991.1}`} {
		for _, args := range []string{`{"document":` + document + `}`, `{"document":` + document + `,"documentJson":"{}"}`} {
			if _, err := ReadProposedPack(json.RawMessage(args)); err == nil || !strings.Contains(err.Error(), "documentJson") {
				t.Fatalf("legacy source admitted: %s %v", args, err)
			}
		}
	}
	for _, args := range []string{`{}`, `{"documentJson":null}`, `{"documentJson":""}`, `{"documentJson":"[]"}`} {
		if _, err := ReadProposedPack(json.RawMessage(args)); err == nil {
			t.Fatalf("malformed source admitted: %s", args)
		}
	}
}
func TestProposalNumericFenceSelectionAndBounds(t *testing.T) {
	for _, body := range []string{`{"propo\u0073al":{"docu\u006dent":{"n":9007199254740993}}}`, `{"proposal":{"document":{"n":9007199254740993}},"proposal":{"document":{"n":1}}}`} {
		if _, err := ExtractProposal("```json\n" + body + "\n```"); err == nil || !strings.Contains(err.Error(), "numerical value changes") {
			t.Fatalf("source loss admitted: %s %v", body, err)
		}
	}
	if got, err := ExtractProposal("```json\n" + `{"metadata":9007199254740993,"proposal":{"document":{"n":1.20}}}` + "\n```"); err != nil || string(got.Document) != `{"n":1.2}` {
		t.Fatalf("unrelated metadata: %s %v", got.Document, err)
	}
	for _, source := range []string{`{"text":"` + strings.Repeat("x", proposal.SourceBytes) + `"}`, `{"text":"` + strings.Repeat("é", proposal.SourceBytes/2) + `"}`, `{"n":` + strings.Repeat("[", proposal.SourceDepth) + `0` + strings.Repeat("]", proposal.SourceDepth) + `}`} {
		if _, err := ReadProposalDocument(json.RawMessage(source)); err == nil || !strings.Contains(err.Error(), "work bound") {
			t.Fatalf("source work bound: %v", err)
		}
	}
	if _, err := ReadProposalDocument(json.RawMessage(`{"text":"` + strings.Repeat("x", 70000) + `"}`)); err != nil {
		t.Fatalf("existing larger-than-default support lost: %v", err)
	}
	if _, err := ReadProposalDocument(json.RawMessage(`{"n":0e9999999}`)); err != nil {
		t.Fatal(err)
	}
}

func TestProposalNumericDepthBeforeSyntaxLimit(t *testing.T) {
	source := `{"id":"p","n":` + strings.Repeat("[", 10001) + "0" + strings.Repeat("]", 10001) + "}"
	if _, err := ReadProposalDocument(json.RawMessage(source)); err == nil {
		t.Fatal("deep source admitted")
	} else if issue, ok := err.(*proposal.Error); !ok || issue.Kind != "work-bound" {
		t.Fatalf("deep source classification: got %v; want work-bound", err)
	}
	if _, err := ExtractProposal("```json\n" + `{"proposal":{"document":` + source + "}}\n```"); !IsTerminalProposalSourceRefusal(err) {
		t.Fatalf("deep source lost terminal classification: %v", err)
	}
	// Delimiters and escaped quotes inside strings must not consume the nesting allowance.
	quoted := `{"text":"` + strings.Repeat("[{]}", 10001) + `","escaped":"\\\"[{}]","n":1}`
	if _, err := ReadProposalDocument(json.RawMessage(quoted)); err != nil {
		t.Fatalf("quoted containers: %v", err)
	}
	for _, malformed := range []string{"", `{`, `{"n":oops}`, `{"text":"unterminated\`} {
		if _, err := ReadProposalDocument(json.RawMessage(malformed)); err == nil {
			t.Fatal("malformed source admitted")
		} else if issue, ok := err.(*proposal.Error); !ok || issue.Kind != "invalid-json" {
			t.Fatalf("ordinary malformed classification: %v", err)
		}
	}
}
