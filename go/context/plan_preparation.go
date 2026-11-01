package runcontext

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
)

// PlanDecisionExpectation binds the host action to the exact pending Plan transition.
// It grants no action, source, request or budget authority.
type PlanDecisionExpectation struct{ Revision, CandidateRevision string }

func validateOriginalPlan(ctx context.Context, saved *contract.Checkpoint, input *Input, required bool) error {
	if saved.PlanState == nil {
		if required {
			return &ContinuationIdentityChanged{}
		}
		return nil
	}
	raw, _ := json.Marshal(saved.PlanState)
	state, err := ParseJSON(raw)
	if err != nil {
		return err
	}
	accepted := state.Get("accepted")
	if required && (input == nil || input.Plan == nil || accepted == nil) {
		return failure.ToolInput("PlanAcceptanceChanged: supply the original accepted plan.")
	}
	for _, ref := range accepted.Get("references").Items() {
		if input == nil || input.PlanResolver == nil {
			return failure.ToolInput("PlanResolverRequired: verify accepted references before work.")
		}
		var pin PlanReference
		if err = json.Unmarshal(ref.JSON(), &pin); err != nil {
			return err
		}
		if _, err = ResolvePlanReference(ctx, pin, *input.PlanResolver); err != nil {
			return err
		}
	}
	if input != nil && input.Plan != nil && accepted != nil && (planText(input.Plan.Get("id")) != planText(accepted.Get("id")) || planNumber(input.Plan.Get("revision")) != planNumber(accepted.Get("sourceRevision")) || planText(input.Plan.Get("acceptedAt")) != planText(accepted.Get("acceptedAt")) || !oneOf(input.Plan.Get("status"), "accepted", "started") || !input.Plan.Get("questions").IsArray() || input.Plan.Get("questions").Len() != 0 || acceptedPlanDigest(input.Plan) != planText(accepted.Get("revision"))) {
		return failure.ToolInput("PlanAcceptanceChanged: resume with the original accepted plan.")
	}
	return nil
}

// PreparePlanDecision applies one pending Plan decision before any Runtime bootstrap.
// It opens no Session or Chat turn and resolves no replacement policy. The returned Input
// retains the original admitted writer and consumes Approval; adapters then initialize once.
// Unknown acknowledgement must be reconciled by reading the retained result, never replaying.
func PreparePlanDecision(ctx context.Context, input *Input, expected PlanDecisionExpectation) (*Input, []contract.RunEvent, error) {
	if input == nil || input.Execution == nil || input.Approval == nil || input.RunStore == nil && input.Persistence == nil {
		return nil, nil, &ContinuationIdentityChanged{}
	}
	if input.Authorize != nil {
		if err := input.Authorize(ctx); err != nil {
			return nil, nil, err
		}
	}
	admitted, err := PrepareDurableInput(ctx, input)
	if err != nil {
		return nil, nil, err
	}
	binding := admitted.Persistence
	if binding == nil || !binding.Writer.rootBinding(*binding) {
		return nil, nil, &ContinuationNotRecoverable{}
	}
	w := binding.Writer
	w.mu.Lock()
	originalRevision := w.revision
	if w.fatal != nil {
		err := w.fatal
		w.mu.Unlock()
		return nil, nil, err
	}
	if w.dispatchClosed.Load() || w.latest == nil {
		w.mu.Unlock()
		return nil, nil, &ContinuationIdentityChanged{}
	}
	current, currentErr := canonicalRaw(w.latest)
	w.mu.Unlock()
	if currentErr != nil {
		return nil, nil, currentErr
	}
	identity, identityErr := canonicalRaw(admitted.Execution)
	if identityErr != nil {
		return nil, nil, identityErr
	}
	if !bytes.Equal(identity, current) {
		return nil, nil, &ContinuationIdentityChanged{}
	}
	head, err := w.Store.Load(ctx, w.ID)
	if err != nil {
		return nil, nil, err
	}
	if head == nil || head.Revision != originalRevision {
		return nil, nil, &StorageConflict{ExpectedRevision: originalRevision}
	}
	if head.RunID != w.ID || !validStorageID(head.CommitID) || head.Cursor != "" && !validStorageID(head.Cursor) {
		return nil, nil, &StorageAcknowledgementInvalid{}
	}
	var retained contract.Checkpoint
	if err = json.Unmarshal(head.Checkpoint, &retained); err != nil {
		return nil, nil, err
	}
	retainedIdentity, err := canonicalRaw(retained)
	if err != nil {
		return nil, nil, err
	}
	if !bytes.Equal(retainedIdentity, current) {
		return nil, nil, &ContinuationIdentityChanged{}
	}
	raw := head.Checkpoint
	value, err := ParseJSON(raw)
	if err != nil {
		return nil, nil, err
	}
	saved, err := ReadExecution(value)
	if err != nil {
		return nil, nil, err
	}
	if saved.RetainUntil == nil || saved.Digest == nil || (saved.Version != 2 && saved.Version != 3 && saved.Version != 4) {
		return nil, nil, &ContinuationNotRecoverable{}
	}
	if err = RefuseExpired(*saved.RetainUntil, time.Now()); err != nil {
		return nil, nil, err
	}
	pending := value.Get("planState").Get("pending")
	if expected.Revision == "" || expected.CandidateRevision == "" || planText(pending.Get("revision")) != expected.Revision || planText(pending.Get("candidate").Get("planState").Get("revision")) != expected.CandidateRevision {
		return nil, nil, &ContinuationIdentityChanged{}
	}
	decision := admitted.Approval
	if decision.CallID == "" || U16Length(decision.CallID) > 256 || decision.Note != nil && U16Length(*decision.Note) > 2000 {
		return nil, nil, failure.ToolInput("PlanApprovalRequired: supply a bounded exact Plan decision.")
	}
	if err = validateOriginalPlan(ctx, saved, admitted, true); err != nil {
		return nil, nil, err
	}
	// Retained resolved policy is required. No default policy/session is synthesized here.
	for _, name := range []string{"records.retainDays", "records.redactOnStore", "records.exportFormat", "outputGuard", "outputPatterns", "outputAllow"} {
		if _, ok := saved.Policy[name]; !ok {
			return nil, nil, &ContinuationNotRecoverable{}
		}
	}
	members := PolicyMembers{}
	policyRaw, _ := json.Marshal(saved.Policy)
	if err = json.Unmarshal(policyRaw, &members); err != nil {
		return nil, nil, err
	}
	retain, retainOK := members["records.retainDays"].Value.(float64)
	_, redactOK := members["records.redactOnStore"].Value.(bool)
	_, formatOK := members["records.exportFormat"].Value.(string)
	mode, modeOK := members["outputGuard"].Value.(string)
	if !retainOK || retain != float64(int(retain)) || !redactOK || !formatOK || !modeOK || (mode != "off" && mode != "redact" && mode != "refuse") || members["outputPatterns"].Value == nil || members["outputAllow"].Value == nil {
		return nil, nil, &ContinuationNotRecoverable{}
	}
	if _, err = ReadRecordsPolicy(members); err != nil {
		return nil, nil, err
	}
	guard, err := NewOutputGuard(members, func() map[string]int64 { return saved.OutputGuard })
	if err != nil {
		return nil, nil, err
	}
	event, handled, err := decidePlan(saved, decision)
	if err != nil {
		return nil, nil, err
	}
	if !handled || event == nil {
		return nil, nil, &ContinuationIdentityChanged{}
	}
	schedule, err := preparePlanTaskDecision(value, saved, *binding, decision.Approved)
	if err != nil {
		return nil, nil, err
	}
	w.mu.Lock()
	proof, err := w.nativePlanDecisionLocked(ctx, *binding, saved, event)
	w.mu.Unlock()
	if err != nil {
		return nil, nil, err
	}
	stamped := contract.StampEvent(*event)
	if taskRecordFields(value) || proof != nil {
		stamped = contract.StampFeatureEvent(stamped)
	}
	event = &stamped
	eventRaw, err := canonicalRaw(*event)
	if err != nil {
		return nil, nil, err
	}
	guarded, err := guard.JSON(eventRaw, "message", false)
	if err != nil {
		return nil, nil, err
	}
	if !bytes.Equal(guarded, eventRaw) {
		return nil, nil, &ContinuationNotRecoverable{}
	}
	// Replace only the transition's four members. json.RawMessage preserves prior
	// durable result bytes (including number lexemes) instead of normalizing the book.
	var prior, decided map[string]json.RawMessage
	if err = json.Unmarshal(raw, &prior); err != nil {
		return nil, nil, err
	}
	decidedRaw, err := json.Marshal(saved)
	if err != nil {
		return nil, nil, err
	}
	if err = json.Unmarshal(decidedRaw, &decided); err != nil {
		return nil, nil, err
	}
	for _, name := range []string{"plan", "planState", "explanation", "status"} {
		prior[name] = decided[name]
	}
	updatedRaw, err := json.Marshal(prior)
	if err != nil {
		return nil, nil, err
	}
	updated, err := ParseJSON(updatedRaw)
	if err != nil {
		return nil, nil, err
	}
	prior["digest"], err = json.Marshal(RecordDigest(updated))
	if err != nil {
		return nil, nil, err
	}
	updatedRaw, err = json.Marshal(prior)
	if err != nil {
		return nil, nil, err
	}
	updated, err = ParseJSON(updatedRaw)
	if err != nil {
		return nil, nil, err
	}
	if _, err = ReadExecution(updated); err != nil {
		return nil, nil, err
	}
	if err = json.Unmarshal(updatedRaw, saved); err != nil {
		return nil, nil, err
	}
	if admitted.Authorize != nil {
		if err = admitted.Authorize(ctx); err != nil {
			return nil, nil, err
		}
	}
	if err = ctx.Err(); err != nil {
		return nil, nil, err
	}
	envelope, err := canonicalRaw(StoredRunEnvelope{Version: 1, Scope: binding.Scope, Event: contract.StampEvent(*event)})
	if err != nil {
		return nil, nil, err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.fatal != nil {
		return nil, nil, w.fatal
	}
	latest, latestErr := canonicalRaw(w.latest)
	if latestErr != nil {
		return nil, nil, latestErr
	}
	if w.revision != originalRevision || !bytes.Equal(latest, current) || w.dispatchClosed.Load() {
		return nil, nil, &StorageConflict{ExpectedRevision: originalRevision, ActualRevision: w.revision}
	}
	if err = w.commitLocked(ctx, newStorageIdentity(), *saved, updatedRaw, [][]byte{envelope}); err != nil {
		var conflict *StorageConflict
		if !errors.As(err, &conflict) {
			w.fatal = &StorageOutcomeUnknown{cause: err}
			err = w.fatal
		}
		return nil, nil, err
	}
	if err = w.ackNativePlanDecisionLocked(*binding, proof); err != nil {
		return nil, nil, err
	}
	w.snapshot = nil
	if w.taskSteps.Load() {
		w.taskSchedule = schedule
	}
	result := *admitted
	var detached contract.Checkpoint
	if err = json.Unmarshal(updatedRaw, &detached); err != nil {
		return nil, nil, err
	}
	result.Execution = &detached
	result.Approval = nil
	return &result, []contract.RunEvent{contract.StampEvent(*event)}, nil
}

// Reconcile only acknowledged task facts. No session, policy, live balance or replacement
// host configuration is constructed; standalone and coordinator ceilings stay distinct.
func preparePlanTaskDecision(prior *Value, saved *contract.Checkpoint, binding RunBinding, approved bool) (*TaskSchedule, error) {
	if !taskRecordFields(prior) {
		return nil, nil
	}
	next := taskCheckpointValue(*saved)
	scope, err := canonicalRaw(binding.Scope)
	if err != nil {
		return nil, err
	}
	scopeDigest := durableDigest(scope)
	var current *TaskSchedule
	if held := prior.Get("planState").Get("taskSchedule"); held != nil {
		current = &TaskSchedule{}
		if err = json.Unmarshal(held.JSON(), current); err != nil {
			return nil, err
		}
		if current.Binding.Run != binding.Writer.ID || current.Binding.Execution != saved.ID || current.Binding.ScopeDigest != scopeDigest {
			return nil, &ContinuationIdentityChanged{}
		}
	}
	for _, name := range []string{"taskEnvelope", "taskCoordinator"} {
		if CanonicalJSON(prior.Get("planState").Get(name)) != CanonicalJSON(next.Get("planState").Get(name)) {
			return nil, &ContinuationIdentityChanged{}
		}
	}
	if !approved {
		return current, nil
	}
	oldDefinitions, oldStatuses, err := TaskDefinitions(prior.Get("plan"))
	if err != nil {
		return nil, err
	}
	definitions, statuses, err := TaskDefinitions(next.Get("plan"))
	if err != nil {
		return nil, err
	}
	available, known := int(saved.Limit-saved.Used), saved.Unit == "model-requests"
	if saved.Unit == "provider-managed" {
		physical := 0
		for _, dispatch := range saved.Durable.Dispatches {
			if dispatch.Kind == "agent" {
				physical++
			}
		}
		if held := prior.Get("planState").Get("taskCoordinator"); held != nil {
			pin, err := readTaskCoordinatorBinding(held)
			if err != nil {
				return nil, err
			}
			if pin.Run != binding.Writer.ID || pin.Execution != saved.ID || pin.ScopeDigest != scopeDigest {
				return nil, &ContinuationIdentityChanged{}
			}
			available, known = max(0, pin.Limit-physical), true
		}
		if held := prior.Get("planState").Get("taskEnvelope"); held != nil {
			pin, err := readTaskEnvelopeBinding(held)
			if err != nil {
				return nil, err
			}
			if pin.Run != binding.Writer.ID || pin.Execution != saved.ID || pin.ScopeDigest != scopeDigest {
				return nil, &ContinuationIdentityChanged{}
			}
			remaining := max(0, pin.Configuration.Limit-physical)
			if !known || remaining < available {
				available = remaining
			}
			known = true
		}
		if !known {
			for _, definition := range definitions {
				if definition.Budget != nil {
					return nil, taskStepFail("native-envelope-required")
				}
			}
			available = 0
		}
	}
	facts := TaskScheduleBinding{Run: binding.Writer.ID, Execution: saved.ID, PlanRevision: planText(next.Get("planState").Get("revision")), ScopeDigest: scopeDigest, Unit: saved.Unit}
	schedule, err := ReconcileTaskSchedule(current, oldDefinitions, oldStatuses, facts, definitions, statuses, available)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(schedule)
	if err != nil {
		return nil, err
	}
	next.Get("planState").Set("taskSchedule", MustParseJSON(string(raw)))
	if err = json.Unmarshal(next.JSON(), saved); err != nil {
		return nil, err
	}
	return &schedule, nil
}
