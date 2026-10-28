package gate_test

import (
	"encoding/json"
	"testing"

	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
)

func finalDeclarations() []contract.ToolOfferHostTool {
	ledger := runcontext.NewContextLedger(&runcontext.Defaults, nil)
	out := []contract.ToolOfferHostTool{}
	for _, tool := range ledger.Tools() {
		out = append(out, tool.Declaration())
	}
	c := &runcontext.ExecutionController{}
	for _, tool := range []runcontext.HostTool{c.Tool(), c.CompletionTool(), c.BudgetTool()} {
		out = append(out, tool.Declaration())
	}
	for _, d := range []struct{ name, effect, delegation string }{{"inspect", "read", "read"}, {"act", "act", "act"}} {
		out = append(out, (runcontext.HostTool{Name: d.name, Description: d.name, InputSchema: json.RawMessage(`{"type":"object"}`), Taxonomy: &runcontext.ToolTaxonomy{Operation: d.effect, Effect: d.effect}, Effect: d.effect, Delegation: d.delegation}).Declaration())
	}
	return out
}
func TestContextEnrollmentPreservesPhaseRestrictions(t *testing.T) {
	hosts := finalDeclarations()
	offer := contract.ToolOffer{HostTools: hosts}
	for _, mode := range []gate.PhaseKind{gate.PhaseLoop, gate.PhasePlan, gate.PhaseChild, gate.PhaseCritic, gate.PhaseReformat} {
		t.Run(string(mode), func(t *testing.T) {
			g := gate.New(gate.Options{})
			g.SetOffer(offer)
			var phase gate.Phase
			switch mode {
			case gate.PhaseLoop:
				phase = gate.LoopPhase(offer)
			case gate.PhasePlan:
				phase = gate.PlanPhase(offer)
			case gate.PhaseChild:
				phase = gate.ChildPhase(contract.DelegatedTaskProfile{ID: "reader", Capabilities: []string{"read"}}, offer, gate.PlanPhase(offer))
			case gate.PhaseCritic:
				phase = gate.CriticPhase(offer)
			case gate.PhaseReformat:
				phase = gate.ReformatPhase()
			}
			g.Enter(phase)
			if err := g.EnrollContext("final", hosts); err != nil {
				t.Fatal(err)
			}
			if err := gate.VerifyContextEnrollment(g.Calls(), "final", hosts, mode); err != nil {
				t.Fatal(err)
			}
			for _, tool := range hosts {
				allowed := false
				for _, name := range phase.Host {
					if name == tool.Name {
						allowed = true
					}
				}
				if (g.Calls().AdmitHost(tool.Name) == nil) != allowed {
					t.Fatalf("phase authority changed for %s", tool.Name)
				}
			}
			g.Calls().BeginReformat()
			if g.Calls().AdmitHost("read_window") == nil {
				t.Fatal("enrollment reopened reformat")
			}
			g.Calls().EndReformat()
			for _, name := range []string{"unknown", "READ_WINDOW", "read_window_extra"} {
				if g.Calls().AdmitHost(name) == nil {
					t.Fatal("unknown name admitted")
				}
			}
		})
	}
}
func TestContextEnrollmentRejectsCollisionReplacementAndDuplicate(t *testing.T) {
	for _, mode := range []string{"identical", "replacement", "missing", "duplicate-name", "invalid-schema", "runtime-collision"} {
		t.Run(mode, func(t *testing.T) {
			hosts := finalDeclarations()
			g := gate.New(gate.Options{})
			base := contract.ToolOffer{HostTools: hosts[:1]}
			g.SetOffer(base)
			switch mode {
			case "replacement":
				hosts[0].Description = "changed"
			case "missing":
				hosts = hosts[1:]
			case "duplicate-name":
				hosts = append(hosts, hosts[0])
			case "invalid-schema":
				hosts[0].InputSchema = json.RawMessage(`null`)
			case "runtime-collision":
				hosts[0].Name = "get_schema"
				base.HostTools = nil
				base.Tools = []contract.ToolOfferRuntimeTool{{Name: hosts[0].Name, Meta: contract.ToolTaxonomy{Operation: "read", Effect: "read"}}}
				g.SetOffer(base)
			}
			err := g.EnrollContext("final", hosts)
			if mode == "identical" {
				if err != nil {
					t.Fatal(err)
				}
				if err = g.EnrollContext("final", hosts); err == nil {
					t.Fatal("duplicate acknowledgement accepted")
				}
				hosts[0].Description = "later mutation"
				if err = gate.VerifyContextEnrollment(g.Calls(), "final", hosts, gate.PhaseLoop); err == nil {
					t.Fatal("acknowledgement metadata alias")
				}
			} else if err == nil {
				t.Fatal("inconsistent offer accepted")
			}
		})
	}
}
