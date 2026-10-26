package wire

import (
	"bytes"
	"encoding/json"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

// ToolTaxonomy is owner-declared; legacy HostTool.Effect remains execution bookkeeping.
type ToolTaxonomy struct {
	Operation    string `json:"operation"`
	Effect       string `json:"effect"`
	CheckKind    string `json:"checkKind,omitempty"`
	Experimental *bool  `json:"experimental,omitempty"`
}

var OperationFamilies = []string{"discover", "read", "answer", "check", "transform", "evaluate", "propose", "act", "run", "own-state", "ask"}
var TaxonomyEffects = []string{"pure", "read", "read-external", "propose", "act"}
var CheckKinds = []string{"validate", "check", "verify", "test", "analyze", "advise"}
var taxonomyCompatibility = map[string][]string{
	"discover": {"pure", "read", "read-external"}, "read": {"read", "read-external"}, "answer": {"pure", "read", "read-external"},
	"check": {"pure", "read", "read-external"}, "transform": {"pure"}, "evaluate": {"pure", "read", "act"}, "propose": {"propose"}, "act": {"act"},
	"run": {"read", "read-external", "propose", "act"}, "own-state": {"pure", "read"}, "ask": {"pure"},
}

func taxonomyContains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// TaxonomyProblem takes runtime evidence only at the actual served-runtime path.
func TaxonomyProblem(t *ToolTaxonomy, runtime bool) string {
	if t == nil {
		return "mandatory tool taxonomy is missing or malformed"
	}
	if !taxonomyContains(OperationFamilies, t.Operation) || !taxonomyContains(TaxonomyEffects, t.Effect) {
		return "tool taxonomy operation or effect is undeclared"
	}
	if !taxonomyContains(taxonomyCompatibility[t.Operation], t.Effect) {
		return "tool taxonomy operation contradicts its effect"
	}
	if (t.Operation == "check" && !taxonomyContains(CheckKinds, t.CheckKind)) || (t.Operation != "check" && t.CheckKind != "") {
		return "tool taxonomy checkKind contradicts its operation"
	}
	if t.Operation == "evaluate" && !runtime {
		return "only the gated runtime may declare evaluate"
	}
	return ""
}
func (t *ToolTaxonomy) UnmarshalJSON(data []byte) error {
	type plain ToolTaxonomy
	var x plain
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&x); err != nil {
		return err
	}
	*t = ToolTaxonomy(x)
	return nil
}

func TaxonomyFromContract(t contract.ToolTaxonomy) *ToolTaxonomy {
	out := &ToolTaxonomy{Operation: t.Operation, Effect: t.Effect, Experimental: t.Experimental}
	if t.CheckKind != nil {
		out.CheckKind = *t.CheckKind
	}
	return out
}

func RuntimeTaxonomyProblem(t *ToolTaxonomy, name string) string {
	if !taxonomyContains([]string{"get_schema", "list_examples", "get_example", "validate", "experimental_evaluate", "experimental_test_cases"}, name) {
		return "runtime tool is outside the gated ceiling"
	}
	if p := TaxonomyProblem(t, true); p != "" {
		return p
	}
	if !taxonomyContains([]string{"discover", "read", "check", "transform", "evaluate"}, t.Operation) || !(taxonomyContains([]string{"pure", "read"}, t.Effect) || name == "experimental_evaluate" && t.Effect == "act") {
		return "runtime taxonomy exceeds its local computation contract"
	}
	if (name == "experimental_evaluate") != (t.Operation == "evaluate") {
		return "runtime evaluation taxonomy contradicts its gated name"
	}
	return ""
}

func CloneTaxonomy(t *ToolTaxonomy) *ToolTaxonomy {
	if t == nil {
		return nil
	}
	out := *t
	if t.Experimental != nil {
		x := *t.Experimental
		out.Experimental = &x
	}
	return &out
}

func TaxonomyAnnotations(effect string) map[string]bool {
	return map[string]bool{"readOnlyHint": effect != "act", "destructiveHint": effect == "act", "idempotentHint": effect != "act", "openWorldHint": effect == "read-external" || effect == "act"}
}
