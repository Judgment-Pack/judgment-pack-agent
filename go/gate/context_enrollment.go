package gate

import (
	"bytes"
	"encoding/json"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/wire"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
)

func enrollmentRefused() error {
	return failure.Authored("configuration", "ContextEnrollmentRefused: acknowledge the exact final contextual offer once before dispatch.", "context")
}
func detachedDeclarations(hosts []contract.ToolOfferHostTool) ([]contract.ToolOfferHostTool, error) {
	raw, err := json.Marshal(hosts)
	if err != nil || len(raw) > 4*1024*1024 {
		return nil, enrollmentRefused()
	}
	var copied []contract.ToolOfferHostTool
	if err = json.Unmarshal(raw, &copied); err != nil {
		return nil, enrollmentRefused()
	}
	return copied, nil
}

// EnrollContext is a trusted-host operation. It replaces the gate's host metadata with
// the complete offer from the actual contextual session; no executable registry changes.
// Current phase policy is derived anew from that exact offer, never from reserved-name bypasses.
func (g *Gate) EnrollContext(id string, hosts []contract.ToolOfferHostTool) error {
	if id == "" || len(id) > 256 || len(hosts) > 400 {
		return enrollmentRefused()
	}
	copied, err := detachedDeclarations(hosts)
	if err != nil {
		return err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.contextEnrollments[id] || len(g.contextEnrollments) >= 400 {
		return enrollmentRefused()
	}
	existing := map[string][]byte{}
	for _, tool := range g.offer.HostTools {
		if _, duplicate := existing[tool.Name]; duplicate {
			return enrollmentRefused()
		}
		existing[tool.Name], err = json.Marshal(tool)
		if err != nil {
			return enrollmentRefused()
		}
	}
	seen := map[string]bool{}
	for _, tool := range g.offer.Tools {
		seen[tool.Name] = true
	}
	for _, tool := range copied {
		var schema map[string]json.RawMessage
		if tool.Name == "" || len(tool.Name) > 256 || seen[tool.Name] || tool.Description == "" || json.Unmarshal(tool.InputSchema, &schema) != nil || schema == nil || wire.TaxonomyProblem(wire.TaxonomyFromContract(tool.Taxonomy), false) != "" {
			return enrollmentRefused()
		}
		if prior, ok := existing[tool.Name]; ok {
			declared, marshalErr := json.Marshal(tool)
			if marshalErr != nil || !bytes.Equal(prior, declared) {
				return enrollmentRefused()
			}
		}
		seen[tool.Name] = true
	}
	for name := range existing {
		if !seen[name] {
			return enrollmentRefused()
		}
	}
	offer := g.offer
	offer.HostTools = copied
	var phase Phase
	switch g.phase.Kind {
	case PhaseLoop:
		phase = LoopPhase(offer)
	case PhasePlan:
		phase = PlanPhase(offer)
	case PhaseChild:
		if g.phase.child == nil || g.phase.parent == nil {
			return enrollmentRefused()
		}
		phase = ChildPhase(*g.phase.child, offer, *g.phase.parent)
	case PhaseCritic:
		phase = CriticPhase(offer)
	case PhaseReformat:
		phase = ReformatPhase()
	default:
		return enrollmentRefused()
	}
	if g.contextEnrollments == nil {
		g.contextEnrollments = map[string]bool{}
	}
	g.contextEnrollments[id] = true
	g.contextEnrollment = id
	g.offer = offer
	g.phase = phase
	return nil
}

// VerifyContextEnrollment observes an acknowledgement through the narrow adapter handle.
// It cannot set an offer or enter a phase. Detached bytes must still match after the callback.
func VerifyContextEnrollment(calls *Calls, id string, hosts []contract.ToolOfferHostTool, mode PhaseKind) error {
	if calls == nil {
		return enrollmentRefused()
	}
	g := calls.g
	expected, err := json.Marshal(hosts)
	if err != nil {
		return enrollmentRefused()
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	actual, err := json.Marshal(g.offer.HostTools)
	if err != nil || g.contextEnrollment != id || g.phase.Kind != mode || !bytes.Equal(actual, expected) {
		return enrollmentRefused()
	}
	return nil
}
