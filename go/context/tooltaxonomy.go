package runcontext

import (
	"context"
	"fmt"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/wire"
	"time"
)

type ToolTaxonomy = wire.ToolTaxonomy
type CapabilityProfile struct {
	Effect     string   `json:"effect"`
	Operations []string `json:"operations"`
}
type TaxonomyLimit struct {
	Operations []string
	Effects    []string
}

func taxonomyContains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
func DefaultApproval(effect string) string {
	if effect == "act" {
		return "required"
	}
	return "none"
}
func TaxonomyAnnotations(effect string) map[string]bool { return wire.TaxonomyAnnotations(effect) }
func TaxonomyLimitProblem(limit *TaxonomyLimit) string {
	if limit == nil {
		return ""
	}
	for _, operation := range limit.Operations {
		if !taxonomyContains(wire.OperationFamilies, operation) {
			return "host taxonomy limit is malformed"
		}
	}
	for _, effect := range limit.Effects {
		if !taxonomyContains(wire.TaxonomyEffects, effect) {
			return "host taxonomy limit is malformed"
		}
	}
	return ""
}
func TaxonomyFits(t *ToolTaxonomy, limit *TaxonomyLimit) bool {
	if wire.TaxonomyProblem(t, true) != "" || TaxonomyLimitProblem(limit) != "" {
		return false
	}
	if t.Operation == "ask" || limit == nil {
		return true
	}
	return (limit.Operations == nil || taxonomyContains(limit.Operations, t.Operation)) && (limit.Effects == nil || taxonomyContains(limit.Effects, t.Effect))
}
func PresetLimit(preset string) TaxonomyLimit {
	if preset != "review" && preset != "work" {
		return TaxonomyLimit{Operations: []string{}, Effects: []string{}}
	}
	if preset == "review" {
		return TaxonomyLimit{Operations: []string{"discover", "read", "check", "ask"}, Effects: []string{"pure", "read", "read-external"}}
	}
	return TaxonomyLimit{Operations: []string{"discover", "read", "check", "propose", "act", "ask"}, Effects: append([]string{}, wire.TaxonomyEffects...)}
}
func ProfileFits(p CapabilityProfile, limit TaxonomyLimit) bool {
	if limit.Effects != nil && !taxonomyContains(limit.Effects, p.Effect) {
		return false
	}
	for _, op := range p.Operations {
		if op != "ask" && limit.Operations != nil && !taxonomyContains(limit.Operations, op) {
			return false
		}
	}
	return true
}
func DeriveCapabilityProfile(names []string, tools map[string]*ToolTaxonomy) (CapabilityProfile, error) {
	p := CapabilityProfile{Effect: "pure", Operations: []string{}}
	ops := map[string]bool{}
	strength := 0
	for _, name := range names {
		t := tools[name]
		if t == nil || wire.TaxonomyProblem(t, true) != "" {
			return p, fmt.Errorf("skill references an unadmitted tool")
		}
		for rank, effect := range wire.TaxonomyEffects {
			if effect == t.Effect && rank > strength {
				strength = rank
				p.Effect = effect
			}
		}
		ops[t.Operation] = true
	}
	for _, op := range wire.OperationFamilies {
		if ops[op] {
			p.Operations = append(p.Operations, op)
		}
	}
	return p, nil
}
func ProfileLabel(p CapabilityProfile) string {
	return map[string]string{"pure": "pure", "read": "reads local data", "read-external": "reads external data", "propose": "proposes for review", "act": "acts; needs approval"}[p.Effect]
}

func LegacyClass(t *ToolTaxonomy) string {
	if t.Effect == "act" {
		return "act"
	}
	if t.Effect == "propose" {
		return "propose"
	}
	return "read"
}
func HostTaxonomyProblem(t HostTool) string {
	if p := TaxonomyLimitProblem(t.TaxonomyLimit); p != "" {
		return p
	}
	if p := wire.TaxonomyProblem(t.Taxonomy, false); p != "" {
		return p
	}
	if t.EffectClass != "" && t.EffectClass != LegacyClass(t.Taxonomy) {
		return "legacy effectClass contradicts owner taxonomy"
	}
	if t.Taxonomy.Effect == "act" && t.Approval == "none" {
		return "host approval cannot widen the declared act effect"
	}
	if t.Source == nil && (t.Taxonomy.Effect == "act" || t.Taxonomy.Effect == "read-external") {
		return "external effects require source provenance"
	}
	if t.Source != nil {
		if t.Taxonomy.Operation == "ask" || t.Taxonomy.Operation == "own-state" {
			return "source provenance contradicts an internal operation"
		}
		if t.Source.Kind != "custom" && (t.Taxonomy.Effect == "pure" || t.Taxonomy.Effect == "read") {
			return "external source contradicts local effect"
		}
		if t.Source.GatewayReceipt != nil && t.Taxonomy.Effect != "read-external" {
			return "Gateway acquisition verification requires read-external taxonomy"
		}
		if t.Source.GatewayReceipt != nil && (GatewayTrustProblem(t.Source.GatewayReceipt) != "" || t.Source.DescriptorSnapshot == nil || t.Source.DescriptorApproval == nil) {
			return "Gateway receipt trust requires valid pins and independently approved V2 descriptors"
		}
		if t.Taxonomy.Effect == "read-external" && t.Source.VerifyReceipt == nil && t.Source.GatewayReceipt == nil {
			return "external read requires a host receipt verifier"
		}
	}
	if t.Interaction == "question" && t.Taxonomy.Operation != "ask" {
		return "question interaction contradicts owner taxonomy"
	}
	return ""
}

// ReceiptEvidence contains canonical immutable call evidence; host verifies signatures and binding.
type ReceiptEvidence struct{ Source, Tool, Args, Result, Receipt string }

func verifyReceipt(ctx context.Context, limit time.Duration, check func(context.Context, ReceiptEvidence) (bool, error), e ReceiptEvidence) (ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	if check == nil {
		return false
	}
	bounded, stop := context.WithTimeout(ctx, limit)
	defer stop()
	verified, err := check(bounded, e)
	return verified && err == nil && bounded.Err() == nil
}

func ToolTaxonomies(tools []HostTool, runtime ...[]RuntimeTool) map[string]*ToolTaxonomy {
	out := map[string]*ToolTaxonomy{}
	for _, t := range tools {
		out[t.Name] = t.Taxonomy
	}
	if len(runtime) > 0 {
		for _, t := range runtime[0] {
			if wire.RuntimeTaxonomyProblem(t.Meta, t.Name) == "" {
				out[t.Name] = t.Meta
			}
		}
	}
	return out
}

// SummarizeTaxonomies preserves complete constituent families and strongest effect.
func SummarizeTaxonomies(tools map[string]*ToolTaxonomy) (*ToolTaxonomy, CapabilityProfile, error) {
	names := make([]string, 0, len(tools))
	for name := range tools {
		names = append(names, name)
	}
	if len(names) == 0 {
		return nil, CapabilityProfile{}, fmt.Errorf("empty declared group")
	}
	profile, err := DeriveCapabilityProfile(names, tools)
	if err != nil {
		return nil, profile, err
	}
	var first *ToolTaxonomy
	same, experimental := true, false
	for _, t := range tools {
		if first == nil {
			first = t
		} else if t.Operation != first.Operation || t.CheckKind != first.CheckKind {
			same = false
		}
		experimental = experimental || (t.Experimental != nil && *t.Experimental)
	}
	t := &ToolTaxonomy{Operation: "run", Effect: profile.Effect}
	if same {
		t.Operation, t.CheckKind = first.Operation, first.CheckKind
	}
	if experimental {
		yes := true
		t.Experimental = &yes
	}
	if wire.TaxonomyProblem(t, true) != "" {
		return nil, profile, fmt.Errorf("invalid derived group")
	}
	return t, profile, nil
}
