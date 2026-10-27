package runcontext

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/internal/providerrelease"
)

type nativePlanAttemptKey struct{}
type nativePlanAttempt struct {
	mu                              sync.Mutex
	owner                           *SessionContext
	writer                          *RunWriter
	scope                           DurableScope
	open, entered, marked, released bool
	claim                           *DurableDispatch
	paused                          *contract.Checkpoint
	pause                           *contract.CheckpointNativePlanPause
}
type nativePlanDisposition struct {
	old      DurableDispatch
	decision contract.CheckpointPlanDecision
}

// This slot stays below the host callback context. Ordinary native calls acquire
// no release obligation; only an actual drained package Plan pause uses its proof.
func (c *SessionContext) beginNativePlanPause(ctx context.Context, role string) (context.Context, *providerrelease.Capability, func() error, func(), error) {
	noop := func() error { return nil }
	binding := c.Persistence(role)
	if binding == nil || !binding.Root || !c.session.AllowConversation || role != "author" || c.CriticReading || c.Reformatting || c.Execution.State().PlanState.Get("accepted") == nil {
		return ctx, nil, noop, func() {}, nil
	}
	if err := c.Execution.MutateCheckpoint(CheckpointPatch{"Version": 3}); err != nil {
		return ctx, nil, nil, nil, err
	}
	attempt := &nativePlanAttempt{owner: c, writer: binding.Writer, scope: binding.Scope, open: true}
	release, finish := providerrelease.NewReleaseAttempt()
	var once sync.Once
	var finishErr error
	complete := func() error {
		once.Do(func() {
			acknowledged := finish()
			attempt.mu.Lock()
			defer attempt.mu.Unlock()
			attempt.released = acknowledged
			if attempt.marked && (!acknowledged || ctx.Err() != nil) {
				finishErr = &ProviderReleaseUnknown{}
			}
		})
		return finishErr
	}
	close := func() { _ = complete(); attempt.mu.Lock(); attempt.open = false; attempt.mu.Unlock() }
	return context.WithValue(ctx, nativePlanAttemptKey{}, attempt), release, complete, close, nil
}

// MarkNativePlanPause is used only by the package adapter after its exact pending
// Plan pause and callback drain. The provider never receives this private context.
func (c *SessionContext) MarkNativePlanPause(ctx context.Context) error {
	attempt, _ := ctx.Value(nativePlanAttemptKey{}).(*nativePlanAttempt)
	binding := c.Persistence("author")
	if attempt == nil || binding == nil || attempt.owner != c || attempt.writer != binding.Writer || attempt.scope != binding.Scope || ctx.Err() != nil {
		return &ContinuationIdentityChanged{}
	}
	state, err := c.Snapshot("paused")
	if err != nil {
		return err
	}
	pause := nativePlanPending(&state)
	if pause == nil || state.Version != 3 {
		return &ContinuationIdentityChanged{}
	}
	w := binding.Writer
	w.mu.Lock()
	defer w.mu.Unlock()
	attempt.mu.Lock()
	defer attempt.mu.Unlock()
	if !attempt.open || !attempt.entered || attempt.marked || attempt.claim == nil || w.fatal != nil || w.dispatchClosed.Load() {
		return &ContinuationIdentityChanged{}
	}
	found := false
	for _, row := range w.book.Dispatches {
		if row.ID == attempt.claim.ID {
			found = row.State == "claimed" && sameNativePlanClaim(row, *attempt.claim)
			break
		}
	}
	if !found {
		return &ContinuationIdentityChanged{}
	}
	attempt.marked = true
	attempt.paused = &state
	attempt.pause = pause
	return nil
}
func nativePlanPending(state *contract.Checkpoint) *contract.CheckpointNativePlanPause {
	if state == nil || state.Status != "paused" || state.PlanState == nil {
		return nil
	}
	value := taskCheckpointValue(*state).Get("planState")
	pending := value.Get("pending")
	if planText(pending.Get("purpose")) != "plan-drift" || planText(pending.Get("revision")) != planText(value.Get("revision")) || !validStorageID(planText(pending.Get("callId"))) || !recordDigestPattern.MatchString(planText(pending.Get("revision"))) || !recordDigestPattern.MatchString(planText(pending.Get("candidate").Get("planState").Get("revision"))) {
		return nil
	}
	return &contract.CheckpointNativePlanPause{CallID: planText(pending.Get("callId")), Revision: planText(pending.Get("revision")), CandidateRevision: planText(pending.Get("candidate").Get("planState").Get("revision"))}
}
func sameNativePlanClaim(a, b DurableDispatch) bool {
	return a.ID == b.ID && a.Scope == b.Scope && a.Kind == "agent" && b.Kind == "agent" && a.Read && b.Read && a.Source == b.Source && a.Tool == b.Tool && a.Fingerprint == b.Fingerprint && a.Request == b.Request && a.RequestDigest == b.RequestDigest && durableDigest([]byte(a.Request)) == a.RequestDigest
}
func nativePlanClaimEventLocked(ctx context.Context, binding *RunBinding, claim DurableDispatch) (*contract.RunEvent, error) {
	attempt, _ := ctx.Value(nativePlanAttemptKey{}).(*nativePlanAttempt)
	if attempt == nil || claim.Kind != "agent" {
		return nil, nil
	}
	attempt.mu.Lock()
	defer attempt.mu.Unlock()
	if !attempt.open || attempt.entered || attempt.writer != binding.Writer || attempt.scope != binding.Scope || !binding.Root || claim.State != "claimed" || !claim.Read {
		return nil, &ContinuationIdentityChanged{}
	}
	attempt.entered = true
	copy := claim
	attempt.claim = &copy
	event := contract.StampFeatureEvent(contract.RunEvent{ExecutionCheckpoint: &contract.RunEventExecutionCheckpoint{Type: "execution_checkpoint"}})
	return &event, nil
}
func nativePlanOutcomeLocked(ctx context.Context, binding *RunBinding, claim DurableDispatch, runErr error) (*contract.Checkpoint, *contract.RunEvent, error) {
	attempt, _ := ctx.Value(nativePlanAttemptKey{}).(*nativePlanAttempt)
	if attempt == nil || claim.Kind != "agent" {
		return nil, nil, nil
	}
	attempt.mu.Lock()
	defer attempt.mu.Unlock()
	if !attempt.marked || !attempt.released {
		return nil, nil, nil
	}
	if !attempt.open || attempt.writer != binding.Writer || attempt.scope != binding.Scope || attempt.claim == nil || !sameNativePlanClaim(claim, *attempt.claim) || runErr == nil || ctx.Err() != nil || attempt.paused == nil || nativePlanPending(attempt.paused) == nil {
		return nil, nil, &ContinuationIdentityChanged{}
	}
	w := binding.Writer
	found := false
	for i, row := range w.book.Dispatches {
		if row.ID == claim.ID {
			if row.State != "unknown" || !sameNativePlanClaim(row, claim) || row.PlanPause != nil {
				return nil, nil, &ContinuationIdentityChanged{}
			}
			w.book.Dispatches[i].PlanPause = usageCopy(attempt.pause)
			found = true
		}
	}
	if !found {
		return nil, nil, &ContinuationIdentityChanged{}
	}
	state := usageCopy(*attempt.paused)
	event := contract.StampFeatureEvent(contract.RunEvent{ExecutionCheckpoint: &contract.RunEventExecutionCheckpoint{Type: "execution_checkpoint", Checkpoint: state}})
	return &state, &event, nil
}

func nativePlanDecisionRows(state *contract.Checkpoint) ([]contract.CheckpointPlanDecision, error) {
	if state == nil || state.PlanState == nil {
		return nil, nil
	}
	raw := taskCheckpointValue(*state).Get("planState").Get("decisions")
	if raw == nil {
		return nil, nil
	}
	var rows []contract.CheckpointPlanDecision
	err := json.Unmarshal(raw.JSON(), &rows)
	return rows, err
}
func nativePlanBook(state *contract.Checkpoint) (DurableBook, error) {
	var book DurableBook
	if state == nil || state.Durable == nil {
		return book, nil
	}
	raw, err := json.Marshal(state.Durable)
	if err != nil {
		return book, err
	}
	err = json.Unmarshal(raw, &book)
	return book, err
}
func (w *RunWriter) nativePlanDecisionLocked(ctx context.Context, binding RunBinding, state *contract.Checkpoint, event *contract.RunEvent) (*nativePlanDisposition, error) {
	if event == nil || event.PlanDriftDecided == nil {
		return nil, nil
	}
	pending := nativePlanPending(w.latest)
	if pending == nil {
		return nil, nil
	}
	var old *DurableDispatch
	for _, row := range w.book.Dispatches {
		if row.Kind != "agent" || row.Scope != binding.Scope || (row.State != "unknown" && row.State != "claimed") {
			continue
		}
		if _, verified := w.nativePlanSuperseded[row.ID]; verified {
			if skip, err := w.supersededNativePlanLocked(&binding, row); err != nil || !skip {
				return nil, &ContinuationIdentityChanged{}
			}
			continue
		}
		if row.PlanPause == nil || row.State != "unknown" || usageCanonical(row.PlanPause) != usageCanonical(pending) || old != nil {
			return nil, &HostOutcomeUnknown{}
		}
		copy := row
		old = &copy
	}
	if old == nil {
		return nil, nil
	}
	if !w.rootBinding(binding) || state.Version != 3 {
		return nil, &ContinuationIdentityChanged{}
	}
	if err := w.verifyNativePlanJournalLocked(ctx, binding, *old, nil); err != nil {
		return nil, err
	}
	marker := &contract.CheckpointNativePlanInvocation{DispatchID: old.ID, RequestDigest: old.RequestDigest}
	rows, err := nativePlanDecisionRows(state)
	if err != nil || len(rows) == 0 {
		return nil, &ContinuationIdentityChanged{}
	}
	last := &rows[len(rows)-1]
	if last.CallID != pending.CallID || last.Revision != pending.Revision || last.CandidateRevision != pending.CandidateRevision || last.NativeInvocation != nil {
		return nil, &ContinuationIdentityChanged{}
	}
	last.NativeInvocation = marker
	value := taskCheckpointValue(*state)
	raw, _ := json.Marshal(rows)
	value.Get("planState").Set("decisions", MustParseJSON(string(raw)))
	if err = json.Unmarshal(value.JSON(), state); err != nil {
		return nil, err
	}
	event.PlanDriftDecided.NativeInvocation = usageCopy(marker)
	*event = contract.StampFeatureEvent(*event)
	return &nativePlanDisposition{old: *old, decision: *last}, nil
}
func (w *RunWriter) ackNativePlanDecisionLocked(binding RunBinding, proof *nativePlanDisposition) error {
	if proof == nil {
		return nil
	}
	if !w.rootBinding(binding) || w.latest == nil {
		return &ContinuationIdentityChanged{}
	}
	rows, err := nativePlanDecisionRows(w.latest)
	if err != nil {
		return err
	}
	found := 0
	for _, row := range rows {
		if usageCanonical(row) == usageCanonical(proof.decision) {
			found++
		}
	}
	if found != 1 {
		return &ContinuationIdentityChanged{}
	}
	if w.nativePlanSuperseded == nil {
		w.nativePlanSuperseded = map[string]nativePlanDisposition{}
	}
	w.nativePlanSuperseded[proof.old.ID] = *proof
	return nil
}
func (w *RunWriter) supersededNativePlanLocked(binding *RunBinding, row DurableDispatch) (bool, error) {
	if row.Kind != "agent" || row.Scope != binding.Scope {
		return false, nil
	}
	proof, ok := w.nativePlanSuperseded[row.ID]
	if !ok {
		if row.PlanPause != nil || len(w.nativePlanSuperseded) > 0 || nativePlanPending(w.latest) != nil {
			return false, &HostOutcomeUnknown{}
		}
		return false, nil
	}
	if row.State != "unknown" || usageCanonical(row) != usageCanonical(proof.old) || proof.decision.NativeInvocation == nil || proof.decision.NativeInvocation.DispatchID != row.ID || proof.decision.NativeInvocation.RequestDigest != row.RequestDigest {
		return false, &ContinuationIdentityChanged{}
	}
	rows, err := nativePlanDecisionRows(w.latest)
	if err != nil {
		return false, err
	}
	matches := 0
	for _, decision := range rows {
		if usageCanonical(decision) == usageCanonical(proof.decision) {
			matches++
		}
	}
	if matches != 1 {
		return false, &ContinuationIdentityChanged{}
	}
	return true, nil
}
func (w *RunWriter) restoreNativePlanDecisions(ctx context.Context, binding RunBinding) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	rows, err := nativePlanDecisionRows(w.latest)
	if err != nil {
		return err
	}
	for _, decision := range rows {
		if decision.NativeInvocation == nil {
			continue
		}
		var old *DurableDispatch
		for _, row := range w.book.Dispatches {
			if row.ID == decision.NativeInvocation.DispatchID {
				copy := row
				old = &copy
			}
		}
		if old == nil || old.State != "unknown" || old.PlanPause == nil || old.Scope != binding.Scope || old.RequestDigest != decision.NativeInvocation.RequestDigest || old.PlanPause.CallID != decision.CallID || old.PlanPause.Revision != decision.Revision || old.PlanPause.CandidateRevision != decision.CandidateRevision {
			return &ContinuationIdentityChanged{}
		}
		if err = w.verifyNativePlanJournalLocked(ctx, binding, *old, &decision); err != nil {
			return err
		}
		if _, exists := w.nativePlanSuperseded[old.ID]; exists {
			return &ContinuationIdentityChanged{}
		}
		if err = w.ackNativePlanDecisionLocked(binding, &nativePlanDisposition{old: *old, decision: decision}); err != nil {
			return err
		}
	}
	return nil
}

// Reuse the bounded strict decoder: duplicate members and trailing JSON cannot
// be normalized into journal authority, even if the typed event would decode.
func nativePlanJSONDepth(raw []byte) error {
	if !utf8.Valid(raw) || !dialectStrictJSONLimit(raw, 64) {
		return &ContinuationIdentityChanged{}
	}
	return nil
}

// The basic RunStore journal is authoritative; optional history objects are not
// required. Cursors order observations but never grant execution authority.
func (w *RunWriter) verifyNativePlanJournalLocked(ctx context.Context, binding RunBinding, old DurableDispatch, decision *contract.CheckpointPlanDecision) error {
	invalid := func() error { return &ContinuationIdentityChanged{} }
	if !w.rootBinding(binding) || old.Kind != "agent" || !old.Read || old.State != "unknown" || old.PlanPause == nil || old.Scope != binding.Scope || durableDigest([]byte(old.Request)) != old.RequestDigest {
		return invalid()
	}
	head, err := w.Store.Load(ctx, w.ID)
	if err != nil {
		return err
	}
	if head == nil || head.RunID != w.ID || head.Revision != w.revision || !validStorageID(head.CommitID) || !validStorageID(head.Cursor) || len(head.Checkpoint) > StorageRecordBytes {
		return invalid()
	}
	value, err := ParseJSON(head.Checkpoint)
	if err != nil {
		return err
	}
	current, err := ReadExecution(value)
	if err != nil {
		return err
	}
	if usageCanonical(current) != usageCanonical(w.latest) || current.Version != 3 || current.RetainUntil == nil || RefuseExpired(*current.RetainUntil, time.Now()) != nil {
		return invalid()
	}
	matches := func(state *contract.Checkpoint, status string, paused bool) bool {
		if state == nil || state.ID != w.ID || state.Version != 3 || state.RequestKey != current.RequestKey || state.Context.Scope != current.Context.Scope || state.RetainUntil == nil || RefuseExpired(*state.RetainUntil, time.Now()) != nil {
			return false
		}
		book, e := nativePlanBook(state)
		if e != nil {
			return false
		}
		count := 0
		for _, row := range book.Dispatches {
			if row.ID == old.ID {
				if row.State != status || !sameNativePlanClaim(row, old) {
					return false
				}
				if paused && usageCanonical(row) != usageCanonical(old) {
					return false
				}
				if !paused && row.PlanPause != nil {
					return false
				}
				count++
			}
		}
		if count != 1 {
			return false
		}
		if paused {
			return usageCanonical(nativePlanPending(state)) == usageCanonical(old.PlanPause)
		}
		return state.Status == "running"
	}
	claimed, paused, decided := false, false, false
	var pauseState *contract.Checkpoint
	after := ""
	seen := map[string]bool{}
	count, total := 0, len(head.Checkpoint)
	reached := false
	for !reached {
		page, e := w.Store.ReadEvents(ctx, w.ID, after, StoragePageLimit)
		if e != nil {
			return e
		}
		if len(page.Items) == 0 || len(page.Items) > StoragePageLimit {
			return invalid()
		}
		for _, item := range page.Items {
			count++
			total += len(item.Bytes)
			if count > 4000 || total > 32*1024*1024 || len(item.Bytes) > StorageRecordBytes {
				return &ContinuationNotRecoverable{}
			}
			if !validStorageID(item.Cursor) || seen[item.Cursor] || reached {
				return invalid()
			}
			seen[item.Cursor] = true
			if err = nativePlanJSONDepth(item.Bytes); err != nil {
				return err
			}
			envelope, e := ReadStoredRunEvent(item.Bytes)
			if e != nil {
				return e
			}
			eventRaw, e := json.Marshal(envelope.Event)
			if e != nil {
				return e
			}
			if contract.CheckRecord("event", eventRaw) != nil {
				return invalid()
			}
			if envelope.Scope == binding.Scope {
				if checkpointEvent := envelope.Event.ExecutionCheckpoint; checkpointEvent != nil {
					stateRaw, e := json.Marshal(checkpointEvent.Checkpoint)
					if e != nil {
						return e
					}
					stateValue, e := ParseJSON(stateRaw)
					if e != nil {
						return e
					}
					state, e := ReadExecution(stateValue)
					if e != nil {
						return e
					}
					if matches(state, "claimed", false) {
						if paused {
							return invalid()
						}
						claimed = true
					}
					if matches(state, "unknown", true) {
						if !claimed || decided {
							return invalid()
						}
						if pauseState != nil && usageCanonical(nativePlanPending(pauseState)) != usageCanonical(nativePlanPending(state)) {
							return invalid()
						}
						paused = true
						pauseState = state
					}
				}
				if event := envelope.Event.PlanDriftDecided; event != nil && event.NativeInvocation != nil && event.NativeInvocation.DispatchID == old.ID {
					if decision == nil || !claimed || !paused || decided || event.NativeInvocation.RequestDigest != old.RequestDigest || pauseState == nil {
						return invalid()
					}
					expected := usageCopy(*pauseState)
					reconstructed, handled, e := decidePlan(&expected, &contract.CheckpointApprovalDecision{CallID: decision.CallID, Approved: decision.Approved, Note: decision.Note})
					if e != nil || !handled || reconstructed == nil {
						return invalid()
					}
					reconstructed.PlanDriftDecided.NativeInvocation = usageCopy(decision.NativeInvocation)
					exact := contract.StampFeatureEvent(*reconstructed)
					if usageCanonical(exact) != usageCanonical(envelope.Event) {
						return invalid()
					}
					rows, e := nativePlanDecisionRows(&expected)
					if e != nil || len(rows) == 0 {
						return invalid()
					}
					last := rows[len(rows)-1]
					last.NativeInvocation = usageCopy(decision.NativeInvocation)
					if usageCanonical(last) != usageCanonical(*decision) {
						return invalid()
					}
					decided = true
				}
			}
			after = item.Cursor
			reached = after == head.Cursor
		}
		if page.NextCursor != after || page.HasMore == reached {
			return invalid()
		}
	}
	again, err := w.Store.Load(ctx, w.ID)
	if err != nil {
		return err
	}
	if again == nil || again.RunID != head.RunID || again.Revision != head.Revision || again.CommitID != head.CommitID || again.Cursor != head.Cursor || !bytes.Equal(again.Checkpoint, head.Checkpoint) || !claimed || !paused || (decision != nil) != decided {
		return invalid()
	}
	return nil
}

// This is an exemption from closing dispatch for the package-owned pause only.
// It never clears a closed dispatcher, recognizes error prose or forgives caller
// cancellation. The exact live slot and acknowledged original-writer row agree.
func nativePlanPauseAcknowledgedLocked(ctx context.Context, binding *RunBinding, claim DurableDispatch) bool {
	if ctx.Err() != nil || binding == nil || binding.Writer.fatal != nil || binding.Writer.dispatchClosed.Load() {
		return false
	}
	attempt, _ := ctx.Value(nativePlanAttemptKey{}).(*nativePlanAttempt)
	if attempt == nil {
		return false
	}
	attempt.mu.Lock()
	defer attempt.mu.Unlock()
	if !attempt.open || !attempt.marked || !attempt.released || attempt.writer != binding.Writer || attempt.scope != binding.Scope || attempt.claim == nil || !sameNativePlanClaim(claim, *attempt.claim) {
		return false
	}
	w := binding.Writer
	if usageCanonical(nativePlanPending(w.latest)) != usageCanonical(attempt.pause) {
		return false
	}
	book, err := nativePlanBook(w.latest)
	if err != nil {
		return false
	}
	for _, row := range book.Dispatches {
		if row.ID == claim.ID {
			return row.State == "unknown" && sameNativePlanClaim(row, claim) && row.PlanPause != nil && usageCanonical(row.PlanPause) == usageCanonical(attempt.pause)
		}
	}
	return false
}
