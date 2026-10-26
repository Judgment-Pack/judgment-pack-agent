package runcontext

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestDialectReviewedDraftIdentity(t *testing.T) {
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
		t.Fatal("reviewed Draft tuple differs from shared current fixture")
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
	if _, err = w.propose(w.args(w.read())); err == nil || !strings.Contains(err.Error(), "exact package-supported experimental Draft opt-in") {
		t.Fatal("superseded prototype identity reached Runtime", err)
	}
	if strings.Contains(strings.Join(w.calls, ","), "get_dialect_contract") || strings.Contains(strings.Join(w.calls, ","), "preview") || len(w.drafts) == 0 || len(w.retained) != 0 {
		t.Fatal("superseded identity crossed the consumer boundary")
	}
}
