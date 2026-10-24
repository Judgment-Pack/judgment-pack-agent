package runcontext

import (
	"context"
	"encoding/json"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
	"strings"
	"time"
)

func validPlanExtensions(plan, state *Value) bool {
	for _, s := range plan.Items() {
		if !validRecordedStep(s) {
			return false
		}
	}
	for _, key := range []string{"departures", "refusals", "decisions"} {
		rows := state.Get(key)
		if rows == nil {
			if state.Has(key + "Discarded") {
				return false
			}
		} else if !rows.IsArray() || rows.Len() > 32 || !isCount(state.Get(key+"Discarded"), 9007199254740991) {
			return false
		}
	}
	accepted := state.Get("accepted")
	if accepted != nil {
		if !accepted.IsObject() || !isText(accepted.Get("id"), 256) || planText(accepted.Get("id")) == "" || !planRevisionDigest(accepted.Get("revision")) || !isCount(accepted.Get("sourceRevision"), 9007199254740991) || planNumber(accepted.Get("sourceRevision")) < 1 || !isText(accepted.Get("objective"), 2000) || strings.TrimSpace(planText(accepted.Get("objective"))) == "" || !accepted.Get("steps").IsArray() || accepted.Get("steps").Len() < 1 || accepted.Get("steps").Len() > 20 || !accepted.Get("references").IsArray() || accepted.Get("references").Len() > 8 || !dateValid(accepted.Get("acceptedAt")) {
			return false
		}
		for _, s := range accepted.Get("steps").Items() {
			if !isText(s, 1000) || strings.TrimSpace(planText(s)) == "" {
				return false
			}
		}
		verified := accepted.Get("validatedReferences")
		refs := accepted.Get("references")
		if !verified.IsArray() || verified.Len() != refs.Len() {
			return false
		}
		for index, ref := range verified.Items() {
			if !ref.IsObject() || !hasOnly(ref, "kind", "id", "digest") || !oneOf(ref.Get("kind"), "artifact", "dialect") || !isText(ref.Get("id"), 256) || planText(ref.Get("id")) == "" || !digest64.MatchString(planText(ref.Get("digest"))) {
				return false
			}
			if refs.Items()[index].Has("digest") && CanonicalJSON(ref) != CanonicalJSON(refs.Items()[index]) {
				return false
			}
		}
		if planText(accepted.Get("revision")) != acceptedPlanDigest(accepted) {
			return false
		}
	}
	origins := map[float64]bool{}
	for _, s := range plan.Items() {
		if o := s.Get("origin"); o != nil {
			index := planNumber(o.Get("index"))
			if accepted == nil || planText(o.Get("plan")) != planText(accepted.Get("id")) || planText(o.Get("revision")) != planText(accepted.Get("revision")) || index >= float64(accepted.Get("steps").Len()) || origins[index] {
				return false
			}
			origins[index] = true
		}
	}
	if p := state.Get("driftPolicy"); p != nil {
		if accepted == nil || !p.IsArray() || p.Len() > 4 {
			return false
		}
		seen := map[string]bool{}
		for _, k := range p.Items() {
			if !oneOf(k, "added", "removed", "changed", "reordered") || seen[planText(k)] {
				return false
			}
			seen[planText(k)] = true
		}
	}
	for _, row := range state.Get("departures").Items() {
		if accepted == nil || !planRevisionDigest(row.Get("revision")) || !isText(row.Get("explanation"), 2000) || strings.TrimSpace(planText(row.Get("explanation"))) == "" || !validDepartureRows(row.Get("changes")) {
			return false
		}
	}
	for _, row := range state.Get("refusals").Items() {
		if !planRevisionDigest(row.Get("revision")) || !oneOf(row.Get("reason"), "invalid-input", "active-step-required", "revision-changed", "evidence-unavailable", "missing-evidence", "unknown-operation", "unsuccessful-operation", "unobserved-revision", "wrong-scope") || row.Has("step") && (!isText(row.Get("step"), 100) || planText(row.Get("step")) == "") {
			return false
		}
	}
	for _, row := range state.Get("decisions").Items() {
		if !isText(row.Get("callId"), 256) || planText(row.Get("callId")) == "" || !planRevisionDigest(row.Get("revision")) || !planRevisionDigest(row.Get("candidateRevision")) || (row.Get("approved") == nil || row.Get("approved").Kind() != KindBool) || row.Has("note") && !isText(row.Get("note"), 2000) {
			return false
		}
	}
	if p := state.Get("pending"); p != nil {
		candidate := p.Get("candidate")
		cs := candidate.Get("planState")
		if accepted == nil || planText(p.Get("purpose")) != "plan-drift" || !isText(p.Get("callId"), 256) || planText(p.Get("callId")) == "" || planText(p.Get("revision")) != planText(state.Get("revision")) || !candidate.IsObject() || !ValidTaskSteps(candidate.Get("plan")) || !cs.IsObject() || cs.Has("pending") || planNumber(cs.Get("sequence")) != planNumber(state.Get("sequence"))+1 || CanonicalJSON(cs.Get("accepted")) != CanonicalJSON(accepted) || CanonicalJSON(cs.Get("driftPolicy")) != CanonicalJSON(state.Get("driftPolicy")) || !isText(p.Get("explanation"), 2000) || strings.TrimSpace(planText(p.Get("explanation"))) == "" || !validDepartureRows(p.Get("departures")) {
			return false
		}
		if !validPlanState(candidate.Get("plan"), cs) || cs.Get("history").Len() == 0 {
			return false
		}
		last := cs.Get("history").Items()[cs.Get("history").Len()-1]
		if planText(last.Get("previous")) != planText(state.Get("revision")) || CanonicalJSON(changedPlanDepartures(plan, state, candidate.Get("plan"))) != CanonicalJSON(p.Get("departures")) {
			return false
		}
		policy := state.Get("driftPolicy")
		if policy == nil {
			policy = Array(String("removed"))
		}
		pause := false
		for _, row := range p.Get("departures").Items() {
			for _, k := range policy.Items() {
				if planText(row.Get("kind")) == planText(k) {
					pause = true
				}
			}
		}
		if !pause {
			return false
		}

		current := Object()
		for _, key := range state.Keys() {
			if key != "pending" {
				current.Set(key, state.Get(key).Clone())
			}
		}
		ids := map[string]bool{}
		for _, step := range plan.Items() {
			ids[planText(step.Get("id"))] = true
		}
		incoming := Array()
		for _, step := range candidate.Get("plan").Items() {
			input := ObjectOf("step", step.Get("step").Clone(), "status", step.Get("status").Clone())
			if ids[planText(step.Get("id"))] {
				input.Set("id", step.Get("id").Clone())
			}
			if step.Has("detail") {
				input.Set("detail", step.Get("detail").Clone())
			}
			incoming.Append(input)
		}
		expected, saved, err := revisePlan(plan, current, incoming, planText(p.Get("explanation")))
		if err != nil {
			return false
		}
		appendPlanRecord(saved, "departures", ObjectOf("revision", saved.Get("revision").Clone(), "explanation", p.Get("explanation").Clone(), "changes", p.Get("departures").Clone()))
		if CanonicalJSON(expected) != CanonicalJSON(candidate.Get("plan")) || CanonicalJSON(saved) != CanonicalJSON(cs) {
			return false
		}
	}
	return true
}
func dateValid(v *Value) bool {
	if !v.IsString() {
		return false
	}
	_, err := time.Parse(time.RFC3339Nano, planText(v))
	return err == nil
}
func validDepartureRows(v *Value) bool {
	if !v.IsArray() || v.Len() < 1 || v.Len() > 40 {
		return false
	}
	for _, r := range v.Items() {
		if !r.IsObject() || !oneOf(r.Get("kind"), "added", "removed", "changed", "reordered") || !isText(r.Get("step"), 100) || planText(r.Get("step")) == "" || r.Has("index") && !isCount(r.Get("index"), 19) {
			return false
		}
	}
	return true
}
func acceptedPlanDigest(plan *Value) string {
	return "sha256:" + ContextDigest(CanonicalJSON(ObjectOf("objective", plan.Get("objective"), "steps", plan.Get("steps"), "questions", Array(), "references", plan.Get("references"))))
}
func (c *ExecutionController) seedAcceptedPlan(ctx context.Context) error {
	if c.session.Context == nil {
		return nil
	}
	input := c.session.Context
	accepted := input.Plan
	if accepted == nil || !oneOf(accepted.Get("status"), "accepted", "started") {
		return nil
	}
	if !dateValid(accepted.Get("acceptedAt")) || !accepted.Get("questions").IsArray() || accepted.Get("questions").Len() > 0 || !accepted.Get("steps").IsArray() || accepted.Get("steps").Len() < 1 || accepted.Get("steps").Len() > 20 {
		return failure.ToolInput("PlanAcceptanceInvalid: use the host-selected accepted plan.")
	}
	for _, s := range accepted.Get("steps").Items() {
		if !isText(s, 1000) || strings.TrimSpace(planText(s)) == "" {
			return failure.ToolInput("PlanAcceptanceInvalid: use the host-selected accepted plan.")
		}
	}
	policy := Array()
	seen := map[string]bool{}
	if input.PlanDrift == nil {
		policy.Append(String("removed"))
	} else {
		if len(input.PlanDrift) > 4 {
			return failure.ToolInput("PlanDriftPolicyInvalid: use a bounded departure-kind set.")
		}
		for _, k := range input.PlanDrift {
			if !oneOf(String(k), "added", "removed", "changed", "reordered") || seen[k] {
				return failure.ToolInput("PlanDriftPolicyInvalid: use a bounded departure-kind set.")
			}
			policy.Append(String(k))
			seen[k] = true
		}
	}
	verified := Array()
	for _, ref := range accepted.Get("references").Items() {
		if input.PlanResolver == nil {
			return failure.ToolInput("PlanResolverRequired: verify accepted references before work.")
		}
		var pin PlanReference
		if err := json.Unmarshal(ref.JSON(), &pin); err != nil {
			return err
		}
		resolved, err := ResolvePlanReference(ctx, pin, *input.PlanResolver)
		if err != nil {
			return err
		}
		raw, _ := json.Marshal(resolved)
		value, _ := ParseJSON(raw)
		verified.Append(value)
	}
	revision := acceptedPlanDigest(accepted)
	plan := c.State().Plan.Clone()
	for index, s := range plan.Items() {
		s.Set("origin", ObjectOf("plan", accepted.Get("id"), "revision", String(revision), "index", Number(float64(index))))
	}
	plan, state := identifyPlan(plan, nil)
	state.Set("accepted", ObjectOf("id", accepted.Get("id").Clone(), "revision", String(revision), "sourceRevision", accepted.Get("revision").Clone(), "objective", accepted.Get("objective").Clone(), "steps", accepted.Get("steps").Clone(), "references", accepted.Get("references").Clone(), "acceptedAt", accepted.Get("acceptedAt").Clone(), "validatedReferences", verified))
	state.Set("driftPolicy", policy)
	return c.MutateCheckpoint(CheckpointPatch{"Plan": plan, "PlanState": state})
}
func (c *ExecutionController) restorePlanDecision(saved *contract.Checkpoint, decision *contract.CheckpointApprovalDecision) (bool, error) {
	raw, _ := json.Marshal(saved)
	v, err := ParseJSON(raw)
	if err != nil {
		return false, err
	}
	state := v.Get("planState")
	p := state.Get("pending")
	if p == nil {
		return false, nil
	}
	if saved.Approval != nil && saved.Approval.State == "asked" || decision == nil || decision.CallID != planText(p.Get("callId")) || planText(p.Get("revision")) != planText(state.Get("revision")) {
		return false, failure.ToolInput("PlanApprovalRequired: continue with the exact pending plan decision.")
	}
	row := ObjectOf("callId", p.Get("callId").Clone(), "revision", p.Get("revision").Clone(), "candidateRevision", p.Get("candidate").Get("planState").Get("revision").Clone(), "approved", Bool(decision.Approved))
	if decision.Note != nil {
		row.Set("note", String(*decision.Note))
	}
	if decision.Approved {
		v.Set("plan", p.Get("candidate").Get("plan"))
		state = p.Get("candidate").Get("planState")
		v.Set("planState", state)
		v.Set("explanation", p.Get("explanation"))
	} else {
		clean := Object()
		for _, k := range state.Keys() {
			if k != "pending" {
				clean.Set(k, state.Get(k))
			}
		}
		state = clean
		v.Set("planState", state)
	}
	appendPlanRecord(state, "decisions", row)
	v.Set("status", String("running"))
	if err = json.Unmarshal(v.JSON(), saved); err != nil {
		return false, err
	}
	event, err := planEvent("plan_drift_decided", saved.ID, row)
	if err != nil {
		return false, err
	}
	c.planDecisionEvent = &event
	return true, nil
}
func (c *ExecutionController) PublishPlanDecision(ctx context.Context) error {
	if c.planDecisionEvent == nil {
		return nil
	}
	if err := c.publishPlan(ctx, *c.planDecisionEvent); err != nil {
		return err
	}
	c.planDecisionEvent = nil
	return nil
}
