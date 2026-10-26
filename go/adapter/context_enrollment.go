package adapter

import (
	"context"
	"crypto/rand"
	"encoding/json"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
)

// ContextOffer is detached metadata from the actual resolved contextual session.
// ID identifies this one acknowledgement; execution/task identity grants no authority.
// Execute callbacks and source/approval capabilities are never exposed here.
type ContextOffer struct {
	ID        string
	Execution string
	Task      string
	Mode      gate.PhaseKind
	HostTools []contract.ToolOfferHostTool
}

// EnrollContext observes final real context declarations before approval continuation or adapter dispatch.
func EnrollContext(ctx context.Context, run *Run, execution string, hosts []contract.ToolOfferHostTool) error {
	if run.EnrollContext == nil {
		return nil
	}
	raw, err := json.Marshal(hosts)
	if err != nil {
		return err
	}
	var detached []contract.ToolOfferHostTool
	if err = json.Unmarshal(raw, &detached); err != nil {
		return err
	}
	offer := ContextOffer{ID: rand.Text(), Execution: execution, Mode: gate.PhaseLoop, HostTools: detached}
	if run.Context != nil {
		if run.Context.Phase == "plan" {
			offer.Mode = gate.PhasePlan
		}
		if run.Context.Objective != "" {
			offer.Mode = gate.PhaseChild
			offer.Task = offer.Execution
		}
	}
	id, mode := offer.ID, offer.Mode
	if err = run.EnrollContext(ctx, offer); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	return gate.VerifyContextEnrollment(run.Gate, id, hosts, mode)
}
