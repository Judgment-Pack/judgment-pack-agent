package runcontext

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
	"github.com/Judgment-Pack/judgment-pack-agent/go/internal/runtimeactions"
)

// RuntimeCall is an opaque exact call prepared by this context. A copied handle still shares
// its single-use private attempt; an empty or caller-created handle has no authority.
type RuntimeCall struct {
	context   *SessionContext
	attempt   *runtimeactions.Attempt
	callID    string
	Requested *contract.RunEvent
	Args      json.RawMessage
}

func (c *SessionContext) RuntimeNeedsApproval(name string) bool {
	for _, t := range c.runtimeTools {
		if t.Name == name {
			return t.Meta != nil && t.Meta.Effect == "act"
		}
	}
	return false
}
func (c *SessionContext) runtimeAdmission() json.RawMessage {
	state := c.Execution.State()
	turn := 0
	if c.turn != nil {
		turn = c.turn.Number()
	}
	role := "author"
	if c.CriticReading {
		role = "critic"
	}
	if c.Reformatting {
		role = "reformat"
	}
	var task any
	if b := c.Persistence(role); b != nil && b.Scope.Task != "" {
		task = b.Scope.Task
	}
	offer := []any{}
	for _, tool := range c.runtimeTools {
		row := map[string]any{"name": tool.Name, "taxonomy": tool.Meta}
		if tool.Offer != nil {
			if tool.Offer.Description != nil {
				row["description"] = *tool.Offer.Description
			}
			if len(tool.Offer.InputSchema) > 0 {
				row["inputSchema"] = tool.Offer.InputSchema
			}
		}
		offer = append(offer, row)
	}
	raw, _ := json.Marshal(map[string]any{"execution": state.ID, "turn": turn, "role": role, "task": task, "phase": state.Phase, "effectivePermissions": map[string]any{"policy": c.ResolvedPolicy, "child": c.objective != "" || c.admittedChild}, "finalRuntimeOffer": offer})
	return raw
}
func (c *SessionContext) runtimeCheck(name string) error {
	if !c.RuntimeNeedsApproval(name) || c.CriticReading || c.Reformatting || c.objective != "" || c.admittedChild || c.Execution.State().Phase == "plan" {
		return c.runtimeRefuse(name, "This phase admits no Runtime action. Nothing was sent.")
	}
	if c.Execution.State().PlanState.Has("pending") {
		return c.runtimeRefuse(name, "PlanApprovalRequired: this run waits for its plan decision.")
	}
	if c.Persistence("author") == nil {
		return c.runtimeRefuse(name, "Runtime actions require the original durable run store. Nothing was sent.")
	}
	return c.RequireRouteCapability(name, nil)
}
func (c *SessionContext) runtimeRefuse(name, detail string) error {
	reason := "approval stale"
	c.notices = append(c.notices, contract.RunEvent{Guardrail: &contract.RunEventGuardrail{Type: "guardrail", Tool: name, Action: "refused", Reason: &reason, Detail: detail}})
	return failure.Authored("permission", detail, "tool")
}

// delegationRefused is a call the run's delegation does not admit (ADR-0029): its guardrail names the
// refusal and the run's actor and subject, and the model is told nothing was run.
func (c *SessionContext) delegationRefused(name string, refusal PrincipalRefusal) error {
	detail := PrincipalRefusalDetail(name, refusal)
	reason := "delegation refused"
	c.notices = append(c.notices, contract.RunEvent{Guardrail: &contract.RunEventGuardrail{Type: "guardrail", Tool: name, Action: "refused", Detail: detail, Reason: &reason, Refusal: refusal.Contract(), Principal: c.Principal.Record.Contract()}})
	return failure.NewError(contract.Failure{Version: 1, Code: "permission", Source: "tool", Outcome: "not-started", Detail: &detail})
}

// refuseUnlessStanding holds a host tool with no source to a delegation that still stands (ADR-0029).
func (c *SessionContext) refuseUnlessStanding(ctx context.Context, tool HostTool) error {
	effect := ""
	if tool.Taxonomy != nil {
		effect = tool.Taxonomy.Effect
	}
	refusal, err := c.Principal.AdmitPrincipalStanding(ctx, tool.Name, effect)
	if err != nil {
		return err
	}
	if refusal != nil {
		return c.delegationRefused(tool.Name, *refusal)
	}
	return nil
}

// runtimeDelegated admits an acting Runtime call as any act is (ADR-0029): refused outside the scope,
// past expiry and after revocation, before it is asked and again before it runs, and counted only
// immediately before the physical call.
func (c *SessionContext) runtimeDelegated(ctx context.Context, name string, count bool) error {
	if c.Principal == nil {
		return nil
	}
	refusal, err := c.Principal.AdmitPrincipalRuntimeCall(ctx, name, count)
	if err != nil {
		return err
	}
	if refusal != nil {
		return &runtimeDelegationRefused{c.delegationRefused(name, *refusal)}
	}
	return nil
}

// runtimeDelegationRefused marks a refusal an approved call answers with, as a call of a source does.
type runtimeDelegationRefused struct{ error }

func (e *runtimeDelegationRefused) Unwrap() error { return e.error }

func (c *SessionContext) PrepareRuntimeCall(ctx context.Context, name string, args json.RawMessage, callID string) (*RuntimeCall, error) {
	if !c.RuntimeNeedsApproval(name) {
		return nil, nil
	}
	c.runtimeApprovalMu.Lock()
	if c.runtimePreparing || c.Execution.State().Approval != nil && c.Execution.State().Approval.State == "asked" {
		c.runtimeApprovalMu.Unlock()
		return nil, c.runtimeRefuse(name, "One act call already waits for approval. Nothing was sent.")
	}
	c.runtimePreparing = true
	c.runtimeApprovalMu.Unlock()
	defer func() { c.runtimeApprovalMu.Lock(); c.runtimePreparing = false; c.runtimeApprovalMu.Unlock() }()
	if err := c.runtimeCheck(name); err != nil {
		return nil, err
	}
	if callID == "" {
		callID = DataMarker()
	}
	if err := c.runtimeDelegated(ctx, name, false); err != nil {
		return nil, err
	}
	clean, err := c.Output.JSON(args, "tool argument", true)
	if err != nil {
		return nil, err
	}
	if err = c.Authorize(ctx); err != nil {
		return nil, err
	}
	attempt, err := runtimeactions.Prepare(ctx, c.runtimeOwner, name, clean, c.runtimeAdmission(), nil)
	if err != nil {
		return nil, c.runtimeRefuse(name, err.Error())
	}
	answer := runtimeactions.Policy(attempt)
	if answer == "refuse" {
		return nil, c.runtimeRefuse(name, "The host refused this Runtime action. Nothing was sent.")
	}
	request := attempt.Request()
	call := &RuntimeCall{context: c, attempt: attempt, callID: callID, Args: append(json.RawMessage(nil), request.Args...)}
	if answer == "ask" {
		if c.PauseClaim != nil && !c.PauseClaim() {
			return nil, c.runtimeRefuse(name, "The invocation did not claim this approval pause. Nothing was sent.")
		}
		var args map[string]json.RawMessage
		if json.Unmarshal(request.Args, &args) != nil {
			return nil, runtimeactions.ErrStale
		}
		binding := request.Runtime
		marker := c.Marker
		record := contract.CheckpointApproval{CallID: callID, Name: name, Tool: name, Source: request.Source, Args: args, State: "asked", Runtime: &binding, Marker: &marker}
		if err := c.Execution.MutateCheckpoint(CheckpointPatch{"Approval": &record, "Version": 3, "Status": "paused"}); err != nil {
			return nil, err
		}
		state, err := c.Execution.Snapshot("paused")
		if err != nil {
			return nil, err
		}
		persistence := c.Persistence("author")
		if err = persistence.Writer.Save(ctx, *persistence, state); err != nil {
			c.Execution.MutateCheckpoint(CheckpointPatch{"Approval": (*contract.CheckpointApproval)(nil), "Status": "stopped"})
			return nil, err
		}
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		call.Requested = ApprovalRequested(record)
	}
	return call, nil
}
func (r *RuntimeCall) Execute(ctx context.Context) (ToolResult, error) {
	if r == nil || r.context == nil || r.attempt == nil || r.Requested != nil {
		return ToolResult{}, runtimeactions.ErrStale
	}
	if err := runtimeactions.Claim(r.attempt); err != nil {
		return ToolResult{}, err
	}
	c := r.context
	request := r.attempt.Request()
	if err := c.runtimeCheck(request.Name); err != nil {
		return ToolResult{}, err
	}
	descriptor, err := r.attempt.Descriptor(r.callID)
	if err != nil {
		return ToolResult{}, err
	}
	sourceRaw, _ := json.Marshal(map[string]any{"source": request.Source, "runtime": request.Runtime})
	source, _ := runtimeactions.Canonical(sourceRaw)
	inner := func(context.Context, json.RawMessage) (ToolResult, error) {
		return InvokeDurable(ctx, c.Persistence("author"), "runtime", "sha256:"+ContextDigest(source), request.Name, []byte(descriptor), false, func(raw []byte) (ToolResult, error) {
			if string(raw) != descriptor {
				return ToolResult{}, runtimeactions.ErrStale
			}
			if err := c.runtimeCheck(request.Name); err != nil {
				return ToolResult{}, err
			}
			result, err := runtimeactions.Execute(ctx, r.attempt, c.runtimeAdmission, func() error {
				if err := c.runtimeCheck(request.Name); err != nil {
					return err
				}
				return c.Authorize(ctx)
			}, func() error { return c.runtimeDelegated(ctx, request.Name, true) })
			if err != nil {
				return ToolResult{}, err
			}
			value, err := ParseJSON(result)
			if err != nil {
				return ToolResult{}, err
			}
			return ReadToolResult(value), nil
		}, func(result ToolResult) bool { return !result.Err() }, func(ToolResult) []ArtifactObservation { return nil })
	}
	offered := true
	call := func() (ToolResult, error) {
		return c.Execution.WrapRead(request.Name, inner, nil, false, &KeptRead{Runtime: true, Offered: &offered})(ctx, request.Args)
	}
	if c.Sources != nil {
		return c.Sources.Runtime(call)
	}
	return call()
}
func (c *SessionContext) resumeRuntimeCall(ctx context.Context, record contract.CheckpointApproval) (ToolResult, error) {
	if record.Runtime == nil {
		return ToolResult{}, c.runtimeRefuse(record.Name, "The saved Runtime action has no current execution binding. Nothing was sent.")
	}
	if err := c.runtimeCheck(record.Name); err != nil {
		return ToolResult{}, err
	}
	// As an approved call of a source: a refusal is the call's result, and the run continues with it.
	result, err := c.resumeAdmittedRuntimeCall(ctx, record)
	var refused *runtimeDelegationRefused
	if errors.As(err, &refused) {
		text, structured := failure.ToolFailure(refused.error)
		value, _ := ParseJSON(structured)
		return ResultOf([]Content{TextContent(text)}, value).WithIsError(true), nil
	}
	return result, err
}
func (c *SessionContext) resumeAdmittedRuntimeCall(ctx context.Context, record contract.CheckpointApproval) (ToolResult, error) {
	if err := c.runtimeDelegated(ctx, record.Name, false); err != nil {
		return ToolResult{}, err
	}
	args, _ := json.Marshal(record.Args)
	attempt, err := runtimeactions.Prepare(ctx, c.runtimeOwner, record.Name, args, c.runtimeAdmission(), record.Runtime)
	if err != nil {
		return ToolResult{}, c.runtimeRefuse(record.Name, err.Error())
	}
	requested := attempt.Request()
	left, _ := canonicalRaw(record.Source)
	right, _ := canonicalRaw(requested.Source)
	argLeft, _ := runtimeactions.Canonical(args)
	argRight, _ := runtimeactions.Canonical(requested.Args)
	if string(left) != string(right) || argLeft != argRight {
		return ToolResult{}, runtimeactions.ErrStale
	}
	return (&RuntimeCall{context: c, attempt: attempt, callID: record.CallID, Args: requested.Args}).Execute(ctx)
}
