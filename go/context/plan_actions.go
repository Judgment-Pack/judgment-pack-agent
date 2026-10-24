package runcontext

import (
	"context"
	"encoding/json"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
	"strings"
)

func validStepEvidence(v *Value) bool {
	if !v.IsObject() || !hasOnly(v, "operations", "artifacts") || !v.Get("operations").IsArray() || !v.Get("artifacts").IsArray() || v.Get("operations").Len()+v.Get("artifacts").Len() > 20 {
		return false
	}
	seen := map[string]bool{}
	for _, id := range v.Get("operations").Items() {
		if !isText(id, 256) || planText(id) == "" || seen[planText(id)] {
			return false
		}
		seen[planText(id)] = true
	}
	seen = map[string]bool{}
	for _, a := range v.Get("artifacts").Items() {
		key := CanonicalJSON(a)
		if !a.IsObject() || !hasOnly(a, "kind", "id", "digest") || !oneOf(a.Get("kind"), "artifact", "dialect") || !isText(a.Get("id"), 256) || planText(a.Get("id")) == "" || !planRevisionDigest(a.Get("digest")) || seen[key] {
			return false
		}
		seen[key] = true
	}
	return true
}
func validRecordedStep(s *Value) bool {
	if o := s.Get("origin"); o != nil {
		if !o.IsObject() || !hasOnly(o, "plan", "revision", "index") || !isText(o.Get("plan"), 256) || planText(o.Get("plan")) == "" || !planRevisionDigest(o.Get("revision")) || !isCount(o.Get("index"), 19) {
			return false
		}
	}
	if e := s.Get("evidence"); e != nil {
		if planText(s.Get("status")) != "completed" || !validStepEvidence(e) || e.Get("operations").Len()+e.Get("artifacts").Len() == 0 {
			return false
		}
	}
	if planText(s.Get("status")) == "not-needed" {
		return isText(s.Get("notNeededReason"), 1500) && strings.TrimSpace(planText(s.Get("notNeededReason"))) != "" && !s.Has("evidence")
	}
	return !s.Has("notNeededReason")
}
func planEvidenceFailure(e *Value, book *DurableBook, scope DurableScope) string {
	if book == nil {
		return "evidence-unavailable"
	}
	if e.Get("operations").Len()+e.Get("artifacts").Len() == 0 {
		return "missing-evidence"
	}
	find := func(id string) *DurableDispatch {
		for i := range book.Dispatches {
			if book.Dispatches[i].ID == id {
				return &book.Dispatches[i]
			}
		}
		return nil
	}
	success := func(d *DurableDispatch) bool {
		if d == nil || (d.Kind != "host" && d.Kind != "runtime") || d.Scope != scope || d.State != "result" || d.Failure != nil || len(d.Result) == 0 {
			return false
		}
		v, err := ParseJSON(d.Result)
		return err == nil && !v.Get("isError").Truth()
	}
	for _, id := range e.Get("operations").Items() {
		d := find(planText(id))
		if d == nil {
			return "unknown-operation"
		}
		if d.Scope != scope {
			return "wrong-scope"
		}
		observed := false
		for _, o := range book.Observations {
			if o.Kind == "operation" && o.DispatchID == planText(id) && o.Scope == scope {
				observed = true
			}
		}
		if !success(d) || (d.Kind != "host" && d.Kind != "runtime") || !observed {
			return "unsuccessful-operation"
		}
	}
	for _, a := range e.Get("artifacts").Items() {
		found := false
		for _, o := range book.Observations {
			if (o.Kind == "artifact-read" || o.Kind == "artifact-produced") && o.Scope == scope && o.Artifact != nil && o.Artifact.Kind == planText(a.Get("kind")) && o.Artifact.ID == planText(a.Get("id")) && o.Artifact.Digest == planText(a.Get("digest")) && success(find(o.DispatchID)) {
				found = true
			}
		}
		if !found {
			return "unobserved-revision"
		}
	}
	return ""
}
func closePlan(plan, state, args *Value, book *DurableBook, scope DurableScope) (*Value, *Value, string) {
	if !args.IsObject() || !hasOnly(args, "id", "expectedRevision", "outcome", "evidence", "reason") || !isText(args.Get("id"), 256) || planText(args.Get("id")) == "" || !args.Get("expectedRevision").IsString() || !oneOf(args.Get("outcome"), "completed", "not-needed") {
		return nil, nil, "invalid-input"
	}
	if planText(args.Get("expectedRevision")) != planText(state.Get("revision")) {
		return nil, nil, "revision-changed"
	}
	var active *Value
	for _, s := range plan.Items() {
		if planText(s.Get("status")) == "in_progress" {
			active = s
		}
	}
	if active == nil || planText(active.Get("id")) != planText(args.Get("id")) {
		return nil, nil, "active-step-required"
	}
	step := active.Clone()
	explanation := "Active step completed with recorded evidence."
	if planText(args.Get("outcome")) == "not-needed" {
		if args.Has("evidence") || !isText(args.Get("reason"), 1500) || strings.TrimSpace(planText(args.Get("reason"))) == "" {
			return nil, nil, "invalid-input"
		}
		step.Set("notNeededReason", args.Get("reason").Clone())
		explanation = "Active step closed as not-needed."
	} else {
		if args.Has("reason") || !validStepEvidence(args.Get("evidence")) {
			return nil, nil, "invalid-input"
		}
		if reason := planEvidenceFailure(args.Get("evidence"), book, scope); reason != "" {
			return nil, nil, reason
		}
		step.Set("evidence", args.Get("evidence").Clone())
	}
	step.Set("status", args.Get("outcome").Clone())
	incoming := plan.Clone()
	for i, s := range incoming.Items() {
		if planText(s.Get("id")) == planText(active.Get("id")) {
			incoming.a[i] = step
		}
	}
	next, saved, err := revisePlan(plan, state, incoming, explanation, true)
	if err != nil {
		return nil, nil, "invalid-input"
	}
	return next, saved, ""
}
func planDepartures(accepted, plan *Value) *Value {
	out := Array()
	for index, text := range accepted.Get("steps").Items() {
		at := -1
		var found *Value
		for i, s := range plan.Items() {
			o := s.Get("origin")
			if planText(o.Get("plan")) == planText(accepted.Get("id")) && planText(o.Get("revision")) == planText(accepted.Get("revision")) && planNumber(o.Get("index")) == float64(index) {
				at = i
				found = s
				break
			}
		}
		row := func(kind, id string) *Value {
			return ObjectOf("kind", String(kind), "step", String(id), "index", Number(float64(index)))
		}
		if at < 0 {
			out.Append(row("removed", "accepted-"+Number(float64(index)).Text()))
			continue
		}
		if planText(found.Get("step")) != planText(text) {
			out.Append(row("changed", planText(found.Get("id"))))
		}
		if at != index {
			out.Append(row("reordered", planText(found.Get("id"))))
		}
	}
	for _, s := range plan.Items() {
		if !s.Has("origin") {
			out.Append(ObjectOf("kind", String("added"), "step", s.Get("id").Clone()))
		}
	}
	return out
}
func changedPlanDepartures(plan, state, next *Value) *Value {
	accepted := state.Get("accepted")
	if accepted == nil {
		return Array()
	}
	changes := Array()
	for index := range accepted.Get("steps").Items() {
		find := func(rows *Value) (int, *Value) {
			for i, s := range rows.Items() {
				o := s.Get("origin")
				if planText(o.Get("plan")) == planText(accepted.Get("id")) && planText(o.Get("revision")) == planText(accepted.Get("revision")) && planNumber(o.Get("index")) == float64(index) {
					return i, s
				}
			}
			return -1, nil
		}
		from, prior := find(plan)
		to, after := find(next)
		if from < 0 {
			continue
		}
		row := func(kind string, s *Value) *Value {
			return ObjectOf("kind", String(kind), "step", s.Get("id").Clone(), "index", Number(float64(index)))
		}
		if to < 0 {
			changes.Append(row("removed", prior))
			continue
		}
		if planText(prior.Get("step")) != planText(after.Get("step")) || prior.Has("detail") != after.Has("detail") || planText(prior.Get("detail")) != planText(after.Get("detail")) {
			changes.Append(row("changed", after))
		}
		if from != to {
			changes.Append(row("reordered", after))
		}
	}
	for _, s := range next.Items() {
		found := false
		for _, prior := range plan.Items() {
			if planText(prior.Get("id")) == planText(s.Get("id")) {
				found = true
			}
		}
		if !s.Has("origin") && !found {
			changes.Append(ObjectOf("kind", String("added"), "step", s.Get("id").Clone()))
		}
	}
	return changes
}

func appendPlanRecord(state *Value, key string, row *Value) {
	rows := state.Get(key)
	if rows == nil {
		rows = Array()
		state.Set(key, rows)
		state.Set(key+"Discarded", Number(0))
	}
	rows.Append(row)
	if rows.Len() > 32 {
		state.Set(key, Array(rows.Items()[1:]...))
		state.Set(key+"Discarded", Number(state.Get(key+"Discarded").Num()+1))
	}
}
func planEvent(kind, execution string, row *Value) (contract.RunEvent, error) {
	v := row.Clone()
	v.Set("type", String(kind))
	if kind != "approval_requested" {
		v.Set("execution", String(execution))
	}
	v.Set("version", Number(1))
	var event contract.RunEvent
	err := json.Unmarshal(v.JSON(), &event)
	return event, err
}
func (c *ExecutionController) publishPlan(ctx context.Context, event contract.RunEvent) error {
	if c.PublishPlan == nil {
		return nil
	}
	cp, err := c.Snapshot("")
	if err != nil {
		return err
	}
	_, err = c.PublishPlan(ctx, cp, event)
	return err
}
func (c *ExecutionController) applyPlan(ctx context.Context, plan, state *Value, explanation string, event contract.RunEvent) error {
	changes := changedPlanDepartures(c.State().Plan, c.State().PlanState, plan)
	if changes.Len() > 0 {
		if strings.TrimSpace(explanation) == "" {
			return failure.ToolInput("PlanDepartureExplanationRequired: explain departures from the accepted plan.")
		}
		appendPlanRecord(state, "departures", ObjectOf("revision", state.Get("revision").Clone(), "explanation", String(explanation), "changes", changes.Clone()))
		policy := state.Get("driftPolicy")
		if policy == nil {
			policy = Array(String("removed"))
		}
		pause := false
		for _, d := range changes.Items() {
			for _, k := range policy.Items() {
				if planText(k) == planText(d.Get("kind")) {
					pause = true
				}
			}
		}
		if pause {
			pending := ObjectOf("purpose", String("plan-drift"), "callId", String(randomUUID()), "revision", c.State().PlanState.Get("revision").Clone(), "candidate", ObjectOf("plan", plan, "planState", state), "explanation", String(explanation), "departures", changes)
			current := c.State().PlanState.Clone()
			current.Set("pending", pending)
			if err := c.MutateCheckpoint(CheckpointPatch{"PlanState": current, "Status": "paused"}); err != nil {
				return err
			}
			notice, err := c.ApprovalNotice()
			if err != nil {
				return err
			}
			return c.publishPlan(ctx, notice)
		}
	}
	if err := c.MutateCheckpoint(CheckpointPatch{"Plan": plan, "PlanState": state, "Explanation": explanation}); err != nil {
		return err
	}
	if changes.Len() > 0 {
		notice, err := planEvent("plan_departed", c.State().ID, ObjectOf("revision", state.Get("revision"), "acceptedRevision", state.Get("accepted").Get("revision"), "explanation", String(explanation), "departures", changes))
		if err != nil {
			return err
		}
		if err = c.publishPlan(ctx, notice); err != nil {
			return err
		}
	}
	return c.publishPlan(ctx, event)
}
func (c *ExecutionController) ApprovalNotice() (contract.RunEvent, error) {
	if p := c.State().PlanState.Get("pending"); p != nil {
		return planEvent("approval_requested", c.State().ID, ObjectOf("purpose", String("plan-drift"), "callId", p.Get("callId"), "plan", ObjectOf("execution", String(c.State().ID), "revision", p.Get("revision"), "candidateRevision", p.Get("candidate").Get("planState").Get("revision"), "explanation", p.Get("explanation"), "departures", p.Get("departures"))))
	}
	if c.State().Approval != nil {
		return *ApprovalRequested(*c.State().Approval), nil
	}
	return contract.RunEvent{}, nil
}
func (c *ExecutionController) CompletionTool() HostTool {
	return HostTool{Name: "complete_plan_step", Effect: "plan", Description: "Close only the current active step with acknowledged execution evidence, or as not-needed with a reason. Pass its exact id and current planRevision.", InputSchema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"string","maxLength":100},"expectedRevision":{"type":"string","pattern":"^sha256:[a-f0-9]{64}$"},"outcome":{"enum":["completed","not-needed"]},"reason":{"type":"string","minLength":1,"maxLength":1500},"evidence":{"type":"object","properties":{"operations":{"type":"array","maxItems":20,"items":{"type":"string","maxLength":256}},"artifacts":{"type":"array","maxItems":20,"items":{"type":"object","properties":{"kind":{"enum":["artifact","dialect"]},"id":{"type":"string","maxLength":256},"digest":{"type":"string","pattern":"^sha256:[a-f0-9]{64}$"}},"required":["kind","id","digest"],"additionalProperties":false}}},"required":["operations","artifacts"],"additionalProperties":false}},"required":["id","expectedRevision","outcome"],"additionalProperties":false,"oneOf":[{"properties":{"outcome":{"const":"completed"}},"required":["evidence"],"not":{"required":["reason"]}},{"properties":{"outcome":{"const":"not-needed"}},"required":["reason"],"not":{"required":["evidence"]}}]}`), Execute: func(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
		if err := ctx.Err(); err != nil {
			return ToolResult{}, err
		}
		c.planMu.Lock()
		defer c.planMu.Unlock()
		if c.State().PlanState.Has("pending") {
			return ToolResult{}, failure.ToolInput("PlanApprovalRequired: this run waits for its plan decision.")
		}
		args, err := ParseJSON(raw)
		if err != nil {
			args = Object()
		}
		plan, state := identifyPlan(c.State().Plan, c.State().PlanState)
		var book *DurableBook
		var scope DurableScope
		if c.PlanProof != nil {
			book, scope = c.PlanProof()
		}
		next, saved, reason := closePlan(plan, state, args, book, scope)
		if reason != "" {
			row := ObjectOf("revision", state.Get("revision"), "reason", String(reason))
			if id := args.Get("id"); isText(id, 100) && planText(id) != "" {
				row.Set("step", id.Clone())
			}
			appendPlanRecord(state, "refusals", row)
			if err = c.MutateCheckpoint(CheckpointPatch{"Plan": plan, "PlanState": state}); err != nil {
				return ToolResult{}, err
			}
			event, e := planEvent("plan_completion_refused", c.State().ID, row)
			if e != nil {
				return ToolResult{}, e
			}
			if e = c.publishPlan(ctx, event); e != nil {
				return ToolResult{}, e
			}
			name := "PlanEvidenceInvalid"
			if reason == "evidence-unavailable" {
				name = "PlanEvidenceUnavailable"
			}
			return ToolResult{}, failure.ToolInput(name + ": completion refused (" + reason + "); the active step remains in progress.")
		}
		row := saved.Get("history").Items()[saved.Get("history").Len()-1]
		event, err := planEvent("plan_revised", c.State().ID, row)
		if err != nil {
			return ToolResult{}, err
		}
		if err = c.applyPlan(ctx, next, saved, planText(row.Get("explanation")), event); err != nil {
			return ToolResult{}, err
		}
		return Answer(ObjectOf("status", args.Get("outcome"), "plan", c.State().Plan, "planRevision", c.State().PlanState.Get("revision"))), nil
	}}
}

func validPlanInput(value *Value) bool {
	if !value.IsArray() || value.Len() < 1 || value.Len() > 20 {
		return false
	}
	active := 0
	for _, s := range value.Items() {
		if !s.IsObject() || !hasOnly(s, "id", "step", "status", "detail") || s.Has("id") && !planStepID(s.Get("id")) || !isText(s.Get("step"), 1000) || strings.TrimSpace(planText(s.Get("step"))) == "" || !oneOf(s.Get("status"), "pending", "in_progress", "completed", "not-needed") || s.Has("detail") && !isText(s.Get("detail"), 1500) {
			return false
		}
		if planText(s.Get("status")) == "in_progress" {
			active++
		}
	}
	return active <= 1
}

func (c *ExecutionController) availablePlanEvidence() *Value {
	var book *DurableBook
	var scope DurableScope
	if c.PlanProof != nil {
		book, scope = c.PlanProof()
	}
	operations, artifacts := Array(), Array()
	if book == nil {
		return ObjectOf("available", Bool(false), "operations", operations, "artifacts", artifacts)
	}
	for _, d := range book.Dispatches {
		if planEvidenceFailure(ObjectOf("operations", Array(String(d.ID)), "artifacts", Array()), book, scope) == "" {
			operations.Append(ObjectOf("id", String(d.ID), "tool", String(d.Tool)))
		}
	}
	seen := map[string]bool{}
	for _, o := range book.Observations {
		if o.Artifact != nil {
			a := ObjectOf("kind", String(o.Artifact.Kind), "id", String(o.Artifact.ID), "digest", String(o.Artifact.Digest))
			key := CanonicalJSON(a)
			if !seen[key] && planEvidenceFailure(ObjectOf("operations", Array(), "artifacts", Array(a)), book, scope) == "" {
				artifacts.Append(a)
				seen[key] = true
			}
		}
	}
	return ObjectOf("available", Bool(true), "operations", operations, "artifacts", artifacts)
}

func (c *SessionContext) PlanApprovalRefusal(name string) error {
	if !c.Execution.State().PlanState.Has("pending") {
		return nil
	}
	reason := "approval pending"
	detail := "PlanApprovalRequired: this run waits for its plan decision."
	c.notices = append(c.notices, contract.RunEvent{Guardrail: &contract.RunEventGuardrail{Type: "guardrail", Tool: name, Action: "refused", Reason: &reason, Detail: detail}})
	return failure.ToolInput(detail)
}
