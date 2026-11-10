package runcontext

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"github.com/Judgment-Pack/judgment-pack-agent/go/internal/usagewitness"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf16"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
)

// DurableScope is assigned by trusted host/coordinator admission, never from provider call ids.
type DurableScope struct {
	Execution string `json:"execution"`
	Role      string `json:"role"`
	Task      string `json:"task,omitempty"`
}
type ArtifactObservation struct {
	Kind   string `json:"kind"`
	ID     string `json:"id"`
	Digest string `json:"digest"`
	Access string `json:"access,omitempty"`
}
type DurableDispatch struct {
	PlanPause        *contract.CheckpointNativePlanPause `json:"planPause,omitempty"`
	ID               string                              `json:"id"`
	Scope            DurableScope                        `json:"scope"`
	Kind             string                              `json:"kind"`
	Source           string                              `json:"source"`
	Tool             string                              `json:"tool"`
	Fingerprint      string                              `json:"fingerprint"`
	Request          string                              `json:"request"`
	RequestDigest    string                              `json:"requestDigest"`
	State            string                              `json:"state"`
	Read             bool                                `json:"read"`
	Result           json.RawMessage                     `json:"result,omitempty"`
	RepeatResultJson *string                             `json:"repeatResultJson,omitempty"`
	Failure          *contract.Failure                   `json:"failure,omitempty"`
	Artifacts        []ArtifactObservation               `json:"artifacts,omitempty"`
}
type DurableObservation struct {
	ID         string               `json:"id"`
	Scope      DurableScope         `json:"scope"`
	Kind       string               `json:"kind"`
	Source     string               `json:"source"`
	DispatchID string               `json:"dispatchId"`
	CommitID   string               `json:"commitId"`
	Artifact   *ArtifactObservation `json:"artifact,omitempty"`
}
type DurableChild struct {
	Scope      DurableScope        `json:"scope"`
	Checkpoint contract.Checkpoint `json:"checkpoint"`
}
type DurablePendingHandoff struct {
	CallID  string                        `json:"callId"`
	Request contract.DelegatedTaskRequest `json:"request"`
}
type DurableBook struct {
	Dispatches     []DurableDispatch      `json:"dispatches"`
	Observations   []DurableObservation   `json:"observations"`
	Children       []DurableChild         `json:"children"`
	PendingHandoff *DurablePendingHandoff `json:"pendingHandoff,omitempty"`
}
type DurableRunInput struct {
	Store   RunStore
	ID      string
	Resume  bool
	Context *StoredChatScope
	// Successor (#340) makes (Resume false) or continues (Resume true) the successor run of a complete run's last
	// question; ID is then SuccessorRunID(Successor.Predecessor, Successor.Question).
	Successor *RunSuccessor
}
type RunBinding struct {
	captureRoot *captureRootAuthority
	Writer      *RunWriter
	Scope       DurableScope
	Root        bool
}
type StoredRunEnvelope struct {
	Version int               `json:"version"`
	Scope   DurableScope      `json:"scope"`
	Event   contract.RunEvent `json:"event"`
}

func storageNamedFailure(code, source, outcome, detail string) error {
	f := failure.New(code, source, outcome)
	f.Detail = &detail
	return failure.NewError(f)
}

type StorageOutcomeUnknown struct {
	// Retain the storage cause for internal diagnosis. Unwrap deliberately exposes only
	// the bounded named failure, never a host's raw detail.
	cause error
}

func (*StorageOutcomeUnknown) Error() string {
	return "StorageOutcomeUnknown: the dispatched operation result was not acknowledged."
}
func (e *StorageOutcomeUnknown) Unwrap() error {
	return storageNamedFailure("storage", "storage", "unknown", e.Error())
}

type durableDispatchScopeKey struct{}

func WithDurableDispatchScope(ctx context.Context, binding *RunBinding) context.Context {
	if binding == nil {
		return ctx
	}
	return context.WithValue(ctx, durableDispatchScopeKey{}, binding.Scope)
}
func DurableDispatchScope(ctx context.Context) (DurableScope, bool) {
	scope, ok := ctx.Value(durableDispatchScopeKey{}).(DurableScope)
	return scope, ok
}

type ProviderReleaseUnknown struct{}

func (*ProviderReleaseUnknown) Error() string {
	return "ProviderReleaseUnknown: provider account release was not acknowledged for this invocation."
}
func (e *ProviderReleaseUnknown) Unwrap() error {
	return storageNamedFailure("storage", "agent", "unknown", e.Error())
}

type StorageAcknowledgementInvalid struct{}

func (*StorageAcknowledgementInvalid) Error() string {
	return "StorageAcknowledgementInvalid: the host did not acknowledge this exact transaction."
}
func (e *StorageAcknowledgementInvalid) Unwrap() error {
	f := failure.New("storage", "storage", "unknown")
	detail := e.Error()
	f.Detail = &detail
	return failure.NewError(f)
}

type ContinuationNotRecoverable struct{}

func (*ContinuationNotRecoverable) Error() string {
	return "ContinuationNotRecoverable: exact safe continuation bytes cannot be retained."
}
func (e *ContinuationNotRecoverable) Unwrap() error {
	return storageNamedFailure("storage", "storage", "not-started", e.Error())
}

type ContinuationIdentityChanged struct{}

// RunCompleteDetail is the detail of a refused resume of a finished run (#328).
const RunCompleteDetail = "RunComplete: the run is complete, and a finished run is not resumed. Only a claimed job wait's or a re-authorization's continuation, or a leg of an open objective, resumes it."

// RunComplete (#328): a resume of a run whose run-store head is complete is refused by name, before anything of the run
// is bound: no model request, no tool call and no record write. A finished run asked again would make new requests and
// could act again. Three resumes of a complete head are kept, each borne out by the store: a job wait's continuation
// (ResumeParked, then Input.JobContinuation) where the store holds that wait claimed at this head, a re-authorization
// claim's view (ReauthorizedRunStore) while its grant is unspent, and a leg of the run's open objective (#330). A question's answer and a chat's next turn are new runs. An execution
// the host holds itself, with no run store, is the host's own record and is not refused.
type RunComplete struct{}

func (*RunComplete) Error() string { return RunCompleteDetail }
func (e *RunComplete) Unwrap() error {
	return storageNamedFailure("stale-state", "context", "not-started", e.Error())
}

// completeHeadBase is the store a view reads through: the store itself for a plain store.
func completeHeadBase(store RunStore) RunStore {
	if view := leaseViewOf(store); view != nil {
		return view.store
	}
	return store
}

// claimedJobWait (#330 HIGH-1): a job continuation resumes a complete head only where the store bears it out: this run's
// wait on that job, claimed, and parked at this head's revision. A continuation delivered again after it ran meets a head
// that has moved.
func claimedJobWait(ctx context.Context, store RunStore, id, job, revision string) (bool, error) {
	waits, ok := completeHeadBase(store).(interface {
		ExportWaits(context.Context, string) (WaitExport, error)
	})
	if !ok {
		return false, nil
	}
	held, err := waits.ExportWaits(ctx, id)
	if err != nil {
		return false, err
	}
	for _, wait := range held.Waits {
		if wait.Resource == id && wait.State == "resumed" && wait.Pending.Job != nil && wait.Pending.Job.Job == job && wait.Checkpoint.Revision == revision {
			return true, nil
		}
	}
	return false, nil
}

// openObjective (#330 MEDIUM-1): a later leg of an objective's run. A complete head resumes while the store's goal record
// for this run has an active objective whose bound Plan still has a required step the head holds unfinished; a paused
// one is still active, and its held lease runs on (M8). Once the objective is blocked or terminal, or its Plan has
// nothing left, the head is refused as any other.
func openObjective(ctx context.Context, store RunStore, id string, saved *contract.Checkpoint) (bool, error) {
	goals, ok := completeHeadBase(store).(interface {
		ReadGoal(context.Context, string) (*RunGoal, error)
	})
	if !ok {
		return false, nil
	}
	goal, err := goals.ReadGoal(ctx, id)
	if err != nil {
		return false, err
	}
	if goal == nil || goal.Resource != id || len(goal.Objectives) == 0 {
		return false, nil
	}
	current := goal.Objectives[len(goal.Objectives)-1]
	if current.State != "active" || current.Plan == nil || saved.PlanState == nil || saved.PlanState.Accepted == nil {
		return false, nil
	}
	accepted := saved.PlanState.Accepted
	if accepted.ID != current.Plan.ID || accepted.Revision != current.Plan.Revision {
		return false, nil
	}
	for _, required := range current.Plan.Steps {
		for _, row := range saved.Plan {
			if row.Origin != nil && row.Origin.Plan == accepted.ID && row.Origin.Revision == accepted.Revision && row.Origin.Index == required.Index && (row.Status == "pending" || row.Status == "in_progress") {
				return true, nil
			}
		}
	}
	return false, nil
}

// reauthorizedContinuation (#328): a resumed re-authorization claim's view, its grant unspent, over a head that holds
// the granted operation as its never-admitted row: the host may open another view of the claim, which sends it once
// more.
func reauthorizedContinuation(store RunStore, checkpoint []byte) bool {
	view := leaseViewOf(store)
	if view == nil {
		return false
	}
	view.mu.Lock()
	grant := view.grant
	var operation EffectOperation
	open := grant != nil && !grant.used
	if open {
		operation = grant.operation
	}
	view.mu.Unlock()
	if !open {
		return false
	}
	var head struct {
		Durable *struct {
			Dispatches []struct {
				ID      string            `json:"id"`
				Kind    string            `json:"kind"`
				State   string            `json:"state"`
				Failure *contract.Failure `json:"failure"`
			} `json:"dispatches"`
		} `json:"durable"`
	}
	if json.Unmarshal(checkpoint, &head) != nil || head.Durable == nil {
		return false
	}
	for _, row := range head.Durable.Dispatches {
		if row.ID == operation.Operation && row.State == "unknown" && (row.Kind == "host" || row.Kind == "runtime") && isNeverAdmittedFailure(row.Failure) {
			return true
		}
	}
	return false
}

func (*ContinuationIdentityChanged) Error() string {
	return "ContinuationIdentityChanged: the admitted continuation identity differs."
}
func (e *ContinuationIdentityChanged) Unwrap() error {
	return storageNamedFailure("stale-state", "context", "not-started", e.Error())
}

// DurableRequestConflict refuses a write that reads the same as one recorded in this run and is written
// differently (#275): the same numbers in another spelling, a repeated name, two names encoding/json
// folds together. It may be that write and it may not, so it is neither run, which could perform a
// write twice, nor answered from that record, which could report a write nothing performed.
type DurableRequestConflict struct{}

func (*DurableRequestConflict) Error() string {
	return "DurableRequestConflict: this call reads the same as a write already recorded in this run but is written differently, so it may be that write. It was not run and was not answered from that record. To get that write's result, repeat its arguments exactly as your earlier call shows them. If this is a different write, it cannot be told apart in this run: tell the person."
}
func (e *DurableRequestConflict) Unwrap() error {
	return storageNamedFailure("invalid-tool-input", "tool", "not-started", e.Error())
}

// DurableRequestUnrecordable refuses a call whose arguments the durable record cannot hold as written
// (#275): a repeated member name, a number out of range, a lone surrogate or bytes that are not UTF-8.
// Refused before anything is claimed, and the run goes on. Retained history refuses the same bytes (readHistoryRequestJSON), which
// reached the writer after the claim and ended the run with a storage failure.
type DurableRequestUnrecordable struct{}

func (*DurableRequestUnrecordable) Error() string {
	return "DurableRequestUnrecordable: this call's arguments cannot be recorded as written: they hold a repeated member name, a number out of range, a lone surrogate or bytes that are not UTF-8. Nothing was run. Write them again without it."
}
func (e *DurableRequestUnrecordable) Unwrap() error {
	return storageNamedFailure("invalid-tool-input", "tool", "not-started", e.Error())
}

type HostOutcomeUnknown struct{}

func (*HostOutcomeUnknown) Error() string {
	return "HostOutcomeUnknown: this operation may have run; it was not run again."
}
func (e *HostOutcomeUnknown) Unwrap() error {
	f := failure.New("unknown", "tool", "unknown")
	detail := e.Error()
	f.Detail = &detail
	return failure.NewError(f)
}

// RunWriter is the sole root transaction/revision owner. Its private book carries child state and
// trusted acknowledged provenance. Storage receipts do not establish current action authority.
// NativeTaskEnvelopeSource is registered by the existing package coordinator only.
type NativeTaskEnvelopeSource struct {
	Limit        int
	ChildReserve int
	Counts       func() map[string]int
	Reserve      func(DurableScope) int
}
type taskDispatchSource struct {
	snapshot func() (contract.Checkpoint, error)
	accept   func(contract.Checkpoint) error
	profiles func() []*contract.SubagentRecord
	charge   func(context.Context, string, string, string) error
}

type RunWriter struct {
	// #229: a job start's registration, set only while the claim that carries it is saved, and this run's jobs by
	// their start's dispatch id, where the host offers job tools.
	jobRegistration        *JobRegistration
	jobRecords             map[string]RunJob
	repeatTools            *contract.CheckpointRepeatTools
	repeatScopes           map[string]repeatDispatchSource
	restoredTaskHead       bool
	configuredTaskEnvelope *TaskEnvelopeBinding
	nativeTaskEnvelope     *NativeTaskEnvelopeSource
	nativeTaskCoordinator  *TaskCoordinatorBinding
	taskSteps              atomic.Bool
	taskRoot               string
	liveTaskPending        string
	taskResultStep         string
	taskResults            map[string]TaskCompletionEvidence
	taskSchedule           *TaskSchedule
	taskScopes             map[string]taskDispatchSource
	budget                 *usageJournalOwner
	mu                     sync.Mutex
	Store                  RunStore
	ID                     string
	contextSource          *StoredChatScope
	repeatRequestCharges   map[string]func(string) error
	capture                *liveCapture
	history                *liveHistoryRecorder
	acknowledgedCheckpoint []byte
	revision               string
	latest                 *contract.Checkpoint
	book                   DurableBook
	recovered              map[string]bool
	models                 map[*modelCompletion]bool
	planDelivery           map[any]planDelivery
	nativeClaims           map[*nativeRouteClaim]bool
	nativeSuperseded       map[string]nativeRouteDisposition
	nativePlanSuperseded   map[string]nativePlanDisposition
	fatal                  error
	dispatchClosed         atomic.Bool
	// closure is the trusted re-authorization refusal that closed dispatch (#226): nothing after it is
	// committed or dispatched.
	closure  error
	stopped  bool
	snapshot func() (contract.Checkpoint, error)
	policy   RecordsPolicy
	guard    func() RecordGuard
	Decorate func(contract.Checkpoint) (contract.Checkpoint, error)
}

// retainedCheckpoint preserves exact lexical evidence before checkpoint canonicalization.
func retainedCheckpoint(value *Value, populate bool, policy RecordsPolicy) (string, error) {
	signatures := Array()
	var visit func(*Value, int) error
	visit = func(state *Value, depth int) error {
		if depth > 64 {
			return &ContinuationNotRecoverable{}
		}
		scope := state.Get("context").Get("scope")
		entries := state.Get("context").Get("entries")
		if !scope.IsString() || !entries.IsArray() {
			return &ContinuationNotRecoverable{}
		}
		for _, entry := range entries.Items() {
			id, digest, size := entry.Get("id"), entry.Get("digest"), entry.Get("bytes")
			if !id.IsString() || !digest.IsString() || size == nil || size.Kind() != KindNumber || !entry.Has("value") || !strings.HasPrefix(id.Str(), scope.Str()+":") {
				return &ContinuationNotRecoverable{}
			}
			var source *string
			if entry.Has("valueJSON") {
				if !entry.Get("valueJSON").IsString() {
					return &ContinuationNotRecoverable{}
				}
				raw := entry.Get("valueJSON").Str()
				source = &raw
			}
			_, text, err := retainedRepresentation(entry.Get("value"), source)
			if err != nil || ContextDigest(text) != digest.Str() || float64(UTF8Bytes(text)) != size.Num() {
				return &ContinuationNotRecoverable{}
			}
			if populate {
				entry.Set("valueJSON", String(text))
			} else if source == nil {
				return &ContinuationNotRecoverable{}
			}
			signatures.Append(Array(id.Clone(), digest.Clone(), size.Clone(), entry.Get("valueJSON").Clone()))
		}
		for _, child := range state.Get("durable").Get("children").Items() {
			checkpoint := child.Get("checkpoint")
			if err := contract.CheckRecord("checkpoint", checkpoint.JSON()); err != nil {
				return &ContinuationNotRecoverable{}
			}
			if !checkpoint.Get("id").IsString() || !child.Get("scope").Get("execution").IsString() || checkpoint.Get("id").Str() != child.Get("scope").Get("execution").Str() || !checkpoint.Get("schema").IsString() || checkpoint.Get("schema").Str() != CheckpointSchema || !checkpoint.Get("digest").IsString() || !checkpoint.Get("retainUntil").IsString() || !ValidRecord(checkpoint) {
				return &ContinuationNotRecoverable{}
			}
			until := checkpoint.Get("retainUntil").Str()
			if err := visit(checkpoint, depth+1); err != nil {
				return err
			}
			if populate {
				written, err := WriteRecord(CheckpointSchema, checkpoint, policy, nil, time.Now(), until)
				if err != nil {
					return err
				}
				child.Set("checkpoint", written)
			}
		}
		return nil
	}
	if err := visit(value, 0); err != nil {
		return "", err
	}
	return CanonicalJSON(signatures), nil
}

func canonicalRaw(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	v, err := ParseJSON(raw)
	if err != nil {
		return nil, err
	}
	return []byte(CanonicalJSON(v)), nil
}
func durableDigest(raw []byte) string { return "sha256:" + ContextDigest(string(raw)) }
func NewRunWriter(store RunStore, id string, head *RunHead, legacy *contract.Checkpoint) (*RunWriter, error) {
	if store != nil && store.Durability() == RunStoreSingleWriterCallback {
		return nil, &ContinuationNotRecoverable{}
	}
	w := &RunWriter{Store: store, ID: id, restoredTaskHead: head != nil || legacy != nil, recovered: map[string]bool{}, models: map[*modelCompletion]bool{}, book: DurableBook{Dispatches: []DurableDispatch{}, Observations: []DurableObservation{}, Children: []DurableChild{}}}
	saved := legacy
	if head != nil {
		if head.RunID != id || !validStorageID(head.Revision) || !validStorageID(head.CommitID) || head.Cursor != "" && !validStorageID(head.Cursor) {
			return nil, &StorageAcknowledgementInvalid{}
		}
		w.revision = head.Revision
		if head.Checkpoint != nil {
			value, err := ParseJSON(head.Checkpoint)
			if err != nil {
				return nil, err
			}
			saved, err = ReadExecution(value)
			if err != nil {
				return nil, err
			}
		}
	}
	if saved != nil {
		if saved.ID != id {
			return nil, &ContinuationIdentityChanged{}
		}
		raw, err := json.Marshal(saved)
		if err != nil {
			return nil, err
		}
		value, err := ParseJSON(raw)
		if err != nil {
			return nil, err
		}
		if held := value.Get("durable"); held != nil {
			if err = json.Unmarshal(held.JSON(), &w.book); err != nil {
				return nil, err
			}
		}
		copy := *saved
		w.latest = &copy
		if copy.Status == "stopped" {
			w.dispatchClosed.Store(true)
			w.stopped = true
		}
	}
	if saved != nil && saved.RepeatTools != nil {
		raw, _ := json.Marshal(saved.RepeatTools)
		if !validRepeatTools(MustParseJSON(string(raw))) {
			return nil, &ContinuationIdentityChanged{}
		}
		if err := json.Unmarshal(raw, &w.repeatTools); err != nil {
			return nil, err
		}
		if !w.repeatEvidenceValid() {
			return nil, &ContinuationIdentityChanged{}
		}
	}
	for i, row := range w.book.Dispatches {
		w.recovered[row.ID] = true
		if row.State == "claimed" {
			w.book.Dispatches[i].State = "unknown"
		}
	}
	if history, ok := store.(HistoryRunStore); ok && history.HistoryVersion() == 1 {
		var raw []byte
		if head != nil {
			raw = head.Checkpoint
			w.acknowledgedCheckpoint = bytes.Clone(raw)
		}
		w.history = newLiveHistoryRecorder(history, id, w.revision, legacy != nil && head == nil, raw)
	}
	return w, nil
}
func (w *RunWriter) Bind(snapshot func() (contract.Checkpoint, error), policy RecordsPolicy, guard func() RecordGuard) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.snapshot = snapshot
	w.policy = policy
	w.guard = guard
}
func (w *RunWriter) Failed() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.fatal != nil || w.dispatchClosed.Load()
}

func (w *RunWriter) rootBinding(binding RunBinding) bool {
	return binding.Writer == w && binding.Root && binding.Scope.Execution == w.ID && binding.Scope.Task == "" && binding.Scope.Role == "author"
}

// UseAcknowledgedRoot transfers snapshot ownership from a returned parent invocation to
// its coordinator. The next admitted root Bind installs the continuation's live snapshot.
func (w *RunWriter) UseAcknowledgedRoot(binding RunBinding) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.rootBinding(binding) || w.latest == nil {
		return &ContinuationIdentityChanged{}
	}
	w.snapshot = nil
	return nil
}

// CloseDispatch closes admission immediately, before the host cancels provider work.
// It grants no persistence or execution authority to a replacement writer.
func (w *RunWriter) CloseDispatch(binding RunBinding) error {
	if !w.rootBinding(binding) {
		return &ContinuationIdentityChanged{}
	}
	w.dispatchClosed.Store(true)
	return nil
}

// FinalizeStop uses the original writer and CAS to atomically retain stopped state and
// terminal events. The host supplies an uncanceled persistence context; work is capped
// at five seconds, including a shorter host deadline. No physical work is retried.
func (w *RunWriter) FinalizeStop(ctx context.Context, binding RunBinding) ([]contract.RunEvent, error) {
	if err := w.CloseDispatch(binding); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	// An admitted commit may still hold the writer. Its latency cannot extend Stop's bound.
	wait := time.NewTicker(time.Millisecond)
	defer wait.Stop()
	for !w.mu.TryLock() {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-wait.C:
		}
	}
	defer w.mu.Unlock()
	if w.latest == nil {
		return nil, &ContinuationIdentityChanged{}
	}
	if w.fatal != nil {
		return nil, w.fatal
	}
	if w.stopped {
		return nil, &ContinuationIdentityChanged{}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if w.latest.Status != "running" && w.latest.Status != "paused" {
		return nil, &ContinuationIdentityChanged{}
	}
	state, _, err := MutateCheckpoint(*w.latest, CheckpointPatch{"Status": "stopped"})
	if err != nil {
		return nil, err
	}
	for i := range w.book.Dispatches {
		if w.book.Dispatches[i].State == "claimed" {
			w.book.Dispatches[i].State = "unknown"
		}
	}
	checkpoint := contract.RunEvent{ExecutionCheckpoint: &contract.RunEventExecutionCheckpoint{Type: "execution_checkpoint", Checkpoint: state}}
	end := contract.RunEvent{End: &contract.RunEventEnd{Type: "end"}}
	saved, err := w.saveLocked(ctx, binding, &state, &checkpoint, nil, nil, end)
	if err != nil {
		var conflict *StorageConflict
		if w.fatal != nil && !errors.As(err, &conflict) {
			w.fatal = &StorageOutcomeUnknown{cause: err}
			return nil, w.fatal
		}
		return nil, err
	}
	w.stopped = true
	w.snapshot = nil
	return []contract.RunEvent{saved, contract.StampEvent(end)}, nil
}
func (w *RunWriter) Checkpoint() *contract.Checkpoint {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.latest == nil {
		return nil
	}
	raw, _ := json.Marshal(w.latest)
	var out contract.Checkpoint
	if json.Unmarshal(raw, &out) != nil {
		return nil
	}
	return &out
}
func (w *RunWriter) Child(task string) *contract.Checkpoint {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, child := range w.book.Children {
		if child.Scope.Task == task {
			raw, _ := json.Marshal(child.Checkpoint)
			var saved contract.Checkpoint
			if json.Unmarshal(raw, &saved) == nil {
				return &saved
			}
		}
	}
	return nil
}
func (w *RunWriter) saveLocked(ctx context.Context, binding RunBinding, state *contract.Checkpoint, event *contract.RunEvent, observed *DurableDispatch, artifacts []ArtifactObservation, extra ...contract.RunEvent) (contract.RunEvent, error) {
	if w.fatal != nil {
		return contract.RunEvent{}, w.fatal
	}
	fail := func(err error) (contract.RunEvent, error) { w.fatal = err; return contract.RunEvent{}, err }
	if w.stopped {
		return contract.RunEvent{}, failure.Cancelled
	}
	if err := ctx.Err(); err != nil {
		w.dispatchClosed.Store(true)
		return contract.RunEvent{}, err
	}
	commitID := newStorageIdentity()
	if state != nil && !binding.Root {
		found := false
		for i, child := range w.book.Children {
			if child.Scope == binding.Scope {
				w.book.Children[i].Checkpoint = *state
				found = true
				break
			}
		}
		if !found {
			w.book.Children = append(w.book.Children, DurableChild{Scope: binding.Scope, Checkpoint: *state})
		}
	}
	count := 0
	if observed != nil {
		count = 1 + len(artifacts)
	}
	if len(w.book.Children) > 32 || len(w.book.Observations)+count > 400 {
		return fail(&ContinuationNotRecoverable{})
	}
	if observed != nil {
		w.book.Observations = append(w.book.Observations, DurableObservation{ID: newStorageIdentity(), Scope: observed.Scope, Kind: "operation", Source: observed.Source, DispatchID: observed.ID, CommitID: commitID})
		for _, artifact := range artifacts {
			kind := "artifact-produced"
			if artifact.Access == "read" || artifact.Access == "" && observed.Read {
				kind = "artifact-read"
			}
			copy := artifact
			w.book.Observations = append(w.book.Observations, DurableObservation{ID: newStorageIdentity(), Scope: observed.Scope, Kind: kind, Source: observed.Source, DispatchID: observed.ID, CommitID: commitID, Artifact: &copy})
		}
	}
	var root contract.Checkpoint
	var err error
	if binding.Root && state != nil {
		root = *state
	} else if w.taskSteps.Load() {
		root, err = w.taskStateLocked()
	} else if !binding.Root && w.latest != nil {
		root = *w.latest
	} else if w.snapshot != nil {
		root, err = w.snapshot()
	} else if w.latest != nil {
		root = *w.latest
	} else {
		return fail(&ContinuationNotRecoverable{})
	}
	if err != nil {
		return fail(err)
	}
	if w.Decorate != nil {
		root, err = w.Decorate(root)
		if err != nil {
			return fail(err)
		}
	}
	patch := CheckpointPatch{}
	if w.repeatTools != nil {
		patch["Version"] = int64(3)
		raw, _ := json.Marshal(w.repeatTools)
		var repeated contract.CheckpointRepeatTools
		if err := json.Unmarshal(raw, &repeated); err != nil {
			return fail(err)
		}
		patch["RepeatTools"] = &repeated
		if current := w.repeatStateLocked(root.ID); current != nil && current.Stopped {
			reason := "RepeatedToolLimit"
			patch["Status"], patch["Reason"] = "stopped", &reason
		}
		for i := range w.book.Children {
			child := &w.book.Children[i]
			if current := w.repeatStateLocked(child.Scope.Execution); current != nil && current.Stopped {
				reason := "RepeatedToolLimit"
				child.Checkpoint, _, err = MutateCheckpoint(child.Checkpoint, CheckpointPatch{"Status": "stopped", "Reason": &reason})
				if err != nil {
					return fail(err)
				}
			}
		}
	}
	if w.budget != nil {
		patch["Version"] = int64(3)
		raw, err := json.Marshal(w.budget.journal)
		if err != nil {
			return fail(err)
		}
		var held contract.UsageBudget
		if err = json.Unmarshal(raw, &held); err != nil {
			return fail(err)
		}
		patch["UsageBudget"] = &held
	}
	for _, row := range w.book.Dispatches {
		if row.PlanPause != nil {
			patch["Version"] = int64(3)
			break
		}
	}
	root, _, err = MutateCheckpoint(root, patch)
	if err != nil {
		return fail(err)
	}
	if root.Handoff != nil {
		w.book.PendingHandoff = nil
	}
	raw, err := json.Marshal(root)
	if err != nil {
		return fail(err)
	}
	value, err := ParseJSON(raw)
	if err != nil {
		return fail(err)
	}
	if w.configuredTaskEnvelope != nil {
		if !value.Get("planState").IsObject() {
			return fail(&ContinuationIdentityChanged{})
		}
		raw, _ := json.Marshal(w.configuredTaskEnvelope)
		value.Get("planState").Set("taskEnvelope", MustParseJSON(string(raw)))
		value.Set("version", Number(3))
	}
	if w.nativeTaskCoordinator != nil {
		if !value.Get("planState").IsObject() {
			return fail(&ContinuationIdentityChanged{})
		}
		raw, _ := json.Marshal(w.nativeTaskCoordinator)
		value.Get("planState").Set("taskCoordinator", MustParseJSON(string(raw)))
		value.Set("version", Number(3))
	}
	if w.taskSchedule != nil {
		if planText(value.Get("planState").Get("revision")) != w.taskSchedule.Binding.PlanRevision {
			return fail(&ContinuationIdentityChanged{})
		}
		schedule, _ := json.Marshal(w.taskSchedule)
		value.Get("planState").Set("taskSchedule", MustParseJSON(string(schedule)))
		value.Set("version", Number(3))
	}
	if w.book.PendingHandoff != nil && !w.dispatchClosed.Load() && (w.repeatStateLocked(root.ID) == nil || !w.repeatStateLocked(root.ID).Stopped) {
		value.Set("status", String("running"))
	}
	book, err := json.Marshal(w.book)
	if err != nil {
		return fail(err)
	}
	held, err := ParseJSON(book)
	if err != nil {
		return fail(err)
	}
	value.Set("durable", held)
	retained, err := retainedCheckpoint(value, true, w.policy)
	if err != nil {
		return fail(err)
	}
	book = []byte(CanonicalJSON(value.Get("durable")))
	keep := ""
	if root.RetainUntil != nil {
		keep = *root.RetainUntil
	}
	var guard RecordGuard
	if w.guard != nil {
		guard = w.guard()
	}
	written, err := WriteRecord(CheckpointSchema, value, w.policy, guard, time.Now(), keep)
	if err != nil {
		return fail(err)
	}
	if after, e := retainedCheckpoint(written, false, w.policy); e != nil || after != retained {
		return fail(&ContinuationNotRecoverable{})
	}
	if w.repeatTools != nil && CanonicalJSON(written.Get("repeatTools")) != CanonicalJSON(value.Get("repeatTools")) {
		return fail(&ContinuationNotRecoverable{})
	}
	if w.budget != nil && CanonicalJSON(written.Get("usageBudget")) != CanonicalJSON(value.Get("usageBudget")) {
		return fail(&ContinuationNotRecoverable{})
	}
	if root.PlanState != nil && (CanonicalJSON(written.Get("plan")) != CanonicalJSON(value.Get("plan")) || CanonicalJSON(written.Get("planState")) != CanonicalJSON(value.Get("planState"))) {
		return fail(&ContinuationNotRecoverable{})
	}
	if !bytes.Equal([]byte(CanonicalJSON(written.Get("durable"))), book) || len(written.JSON()) > StorageRecordBytes || root.ID != w.ID {
		return fail(&ContinuationNotRecoverable{})
	}
	var checkpoint contract.Checkpoint
	if err = json.Unmarshal(written.JSON(), &checkpoint); err != nil {
		return fail(err)
	}
	if _, err = ReadExecution(written); err != nil {
		return fail(err)
	}
	events := [][]byte{}
	var verified *TaskCompletionEvidence
	var output contract.RunEvent
	if event != nil {
		output = contract.StampEvent(*event)
		if output.ExecutionCheckpoint != nil {
			copy := *output.ExecutionCheckpoint
			durable := true
			copy.Durable = &durable
			if binding.Root {
				copy.Checkpoint = checkpoint
			}
			output = contract.RunEvent{ExecutionCheckpoint: &copy}
		}
		if output.BudgetPaused != nil && binding.Root {
			copy := *output.BudgetPaused
			copy.Checkpoint = checkpoint
			output = contract.RunEvent{BudgetPaused: &copy}
		}
		output = contract.StampEvent(output)
		if w.taskSchedule != nil && planEventIdentity(output) != nil || w.budget != nil && (output.ExecutionCheckpoint != nil || output.BudgetPaused != nil) {
			output = contract.StampFeatureEvent(output)
		}
		if w.taskResultStep != "" {
			if output.DelegatedTask == nil || !w.rootBinding(binding) {
				return fail(&ContinuationIdentityChanged{})
			}
			task := output.DelegatedTask.Task
			var row *Value
			for _, candidate := range written.Get("planState").Get("taskSchedule").Get("rows").Items() {
				if planText(candidate.Get("id")) == w.taskResultStep {
					row = candidate
					break
				}
			}
			if row == nil {
				return fail(&ContinuationIdentityChanged{})
			}
			var child TaskChildBinding
			if json.Unmarshal(row.Get("child").JSON(), &child) != nil {
				return fail(&ContinuationIdentityChanged{})
			}
			checkpointDigest, ok := successfulTaskChild(checkpoint, child, task)
			if !ok {
				return fail(&ContinuationIdentityChanged{})
			}
			taskRaw, _ := canonicalRaw(task)
			eventRaw, _ := canonicalRaw(StoredRunEnvelope{Version: 1, Scope: binding.Scope, Event: output})
			result := TaskChildResult{CheckpointDigest: checkpointDigest, ResultDigest: durableDigest(taskRaw), EventDigest: durableDigest(eventRaw), CommitID: commitID}
			raw, _ := json.Marshal(result)
			row.Get("child").Set("result", MustParseJSON(string(raw)))
			written.Set("digest", String(RecordDigest(written)))
			if err = json.Unmarshal(written.JSON(), &checkpoint); err != nil {
				return fail(err)
			}
			if _, err = ReadExecution(written); err != nil {
				return fail(err)
			}
			verified = &TaskCompletionEvidence{Step: w.taskResultStep, Task: task.ID, PlanRevision: child.PlanRevision, TaskChildResult: result}
		}
		encoded, e := canonicalRaw(StoredRunEnvelope{Version: 1, Scope: binding.Scope, Event: output})
		if e != nil {
			return fail(e)
		}
		events = append(events, encoded)
	}
	for _, additional := range extra {
		encoded, e := canonicalRaw(StoredRunEnvelope{Version: 1, Scope: binding.Scope, Event: contract.StampEvent(additional)})
		if e != nil {
			return fail(e)
		}
		events = append(events, encoded)
	}
	if err := w.commitLocked(ctx, commitID, checkpoint, []byte(CanonicalJSON(written)), events, binding.Scope); err != nil {
		return contract.RunEvent{}, err
	}
	if verified != nil {
		var schedule TaskSchedule
		if json.Unmarshal(written.Get("planState").Get("taskSchedule").JSON(), &schedule) != nil {
			return fail(&ContinuationIdentityChanged{})
		}
		w.taskSchedule = &schedule
		if w.taskResults == nil {
			w.taskResults = map[string]TaskCompletionEvidence{}
		}
		w.taskResults[verified.Step] = *verified
		if err = w.taskScopes[w.taskRoot].accept(checkpoint); err != nil {
			return fail(err)
		}
	}
	return output, nil
}

// commitLocked is the one exact CAS/receipt boundary, also used by metadata-only Plan preparation.
func (w *RunWriter) commitLocked(ctx context.Context, commitID string, checkpoint contract.Checkpoint, raw []byte, events [][]byte, scopes ...DurableScope) error {
	input, err := freezeRunCommit(RunCommit{RunID: w.ID, ExpectedRevision: w.revision, CommitID: commitID, Checkpoint: raw, Events: events, Job: w.jobRegistration})
	if err != nil {
		w.fatal = err
		return err
	}
	detached, err := freezeRunCommit(input)
	if err != nil {
		w.fatal = err
		return err
	}
	var receipt RunReceipt
	var update *ContextSnapshotUpdate
	var history RunHistoryReceipt
	var prepared *preparedLiveHistory
	scope := DurableScope{Execution: w.ID, Role: "author"}
	if len(scopes) > 0 {
		scope = scopes[0]
	}
	if w.contextSource != nil {
		var value ContextSnapshotUpdate
		scope := checkpoint.Context.Scope
		value, err = w.contextSource.prepareForCheckpoint(ctx, checkpoint.Status == "complete" || checkpoint.Status == "stopped", &scope)
		update = &value
	}
	if err == nil && w.history != nil {
		w.prepareCaptureLocked(ctx, checkpoint)
		prepared, err = w.history.prepare(ctx, checkpoint, scope, update != nil)
	}
	if err == nil {
		if update != nil || prepared != nil {
			observations := []HistoryObjectRef{}
			genesis := false
			if prepared != nil {
				observations = append(observations, prepared.observations...)
				genesis = prepared.genesis
			}
			store, ok := w.Store.(HistoryRunStore)
			if !ok {
				err = &ContinuationIdentityChanged{}
			} else {
				history, err = store.CommitHistory(ctx, RunHistoryCommit{Run: detached, Observations: observations, Genesis: genesis, Context: historyClone(update)})
				receipt = history.RunReceipt
			}
		} else {
			receipt, err = w.Store.Commit(ctx, detached)
		}
	}
	if err == nil && (receipt.CommitID != commitID || !validStorageID(receipt.Revision) || receipt.Revision == w.revision || receipt.Cursor != "" && !validStorageID(receipt.Cursor) || len(events) > 0 && receipt.Cursor == "") {
		err = &StorageAcknowledgementInvalid{}
	}
	if err == nil && update != nil {
		c := history.History.Context
		expected := ""
		if update.ExpectedRevision != nil {
			expected = *update.ExpectedRevision
		}
		if c == nil || history.History.RunID != w.ID || history.History.ExecutionRevision != receipt.Revision || c.ChatID != update.ChatID || c.Turn == nil || update.Turn == nil || *c.Turn != *update.Turn || c.Closed != update.Closed || c.PrivateScope != update.PrivateScope || c.Manifest != update.Manifest || !validStorageID(c.Revision) || c.Revision == expected {
			err = &StorageAcknowledgementInvalid{}
		}
	}
	if err == nil && prepared != nil {
		err = prepared.acknowledge(ctx, history.History, input, receipt.Revision)
	}
	if err != nil {
		w.fatal = err
		return err
	}
	if prepared != nil {
		w.admitCaptureLocked(history, commitID, prepared)
	}
	if update != nil {
		w.contextSource.acknowledge(history.History.Context)
	}
	w.acknowledgedCheckpoint = bytes.Clone(input.Checkpoint)
	w.revision = receipt.Revision
	w.latest = &checkpoint
	if ctx.Err() != nil {
		w.dispatchClosed.Store(true)
	}
	return nil
}

type planDelivery struct {
	scope DurableScope
	bytes string
}

// CommitRoute publishes the exact current route and its event in the original writer CAS.
func (w *RunWriter) CommitRoute(ctx context.Context, binding RunBinding, state contract.Checkpoint, event contract.RunEvent, native ...*nativeRouteClaim) (contract.RunEvent, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.fatal != nil {
		return contract.RunEvent{}, w.fatal
	}
	if w.dispatchClosed.Load() {
		return contract.RunEvent{}, failure.Cancelled
	}
	state = usageCopy(state)
	rawEvent, cloneErr := json.Marshal(contract.StampEvent(event))
	if cloneErr != nil {
		return contract.RunEvent{}, cloneErr
	}
	var copiedEvent contract.RunEvent
	if cloneErr = json.Unmarshal(rawEvent, &copiedEvent); cloneErr != nil {
		return contract.RunEvent{}, cloneErr
	}
	event = copiedEvent
	var proof *nativeRouteClaim
	if event.RouteWidened != nil {
		if event.RouteWidened.Route.WidenedInvocation != nil || state.TurnRoute != nil && state.TurnRoute.WidenedInvocation != nil {
			return contract.RunEvent{}, &ContinuationIdentityChanged{}
		}
		if len(native) > 0 && native[0] != nil {
			proof = native[0]
			if !w.nativeClaims[proof] || proof.live || !proof.released || proof.consumed || proof.scope != binding.Scope || w.latest == nil || w.latest.TurnRoute == nil || w.latest.TurnRoute.Widened || w.latest.TurnRoute.BindingDigest != proof.route || event.RouteWidened.Route.BindingDigest != proof.route || (event.RouteWidened.Route.WidenedFor == nil || *event.RouteWidened.Route.WidenedFor != proof.needed) {
				return contract.RunEvent{}, &ContinuationIdentityChanged{}
			}
			var row *DurableDispatch
			for i := range w.book.Dispatches {
				if w.book.Dispatches[i].ID == proof.id {
					row = &w.book.Dispatches[i]
				}
			}
			if row == nil || row.Kind != "agent" || !row.Read || row.State != "unknown" || row.Scope != binding.Scope || row.RequestDigest != proof.requestDigest || durableDigest([]byte(row.Request)) != proof.requestDigest {
				return contract.RunEvent{}, &ContinuationIdentityChanged{}
			}
			marker := &contract.TurnRouteWidenedInvocation{DispatchID: proof.id, RequestDigest: proof.requestDigest}
			event.RouteWidened.Route.WidenedInvocation = marker
			if state.TurnRoute == nil {
				return contract.RunEvent{}, &ContinuationIdentityChanged{}
			}
			route := usageCopy(state.TurnRoute)
			route.WidenedInvocation = usageCopy(marker)
			var err error
			state, _, err = MutateCheckpoint(state, CheckpointPatch{"TurnRoute": route})
			if err != nil {
				return contract.RunEvent{}, err
			}
		}
	}
	var route *contract.TurnRoute
	if event.RouteDecided != nil {
		route = &event.RouteDecided.Route
	}
	if event.RouteWidened != nil {
		route = &event.RouteWidened.Route
	}
	if binding.Writer != w || state.ID != binding.Scope.Execution || state.Version < 3 || state.TurnRoute == nil || route == nil || usageCanonical(route) != usageCanonical(state.TurnRoute) {
		return contract.RunEvent{}, &ContinuationIdentityChanged{}
	}
	if len(w.planDelivery) >= 400 {
		return contract.RunEvent{}, &ContinuationNotRecoverable{}
	}
	output, err := w.saveLocked(ctx, binding, &state, &event, nil, nil)
	if err != nil {
		return contract.RunEvent{}, err
	}
	if w.planDelivery == nil {
		w.planDelivery = map[any]planDelivery{}
	}
	raw, err := canonicalRaw(output)
	if err != nil {
		return contract.RunEvent{}, err
	}
	if proof != nil {
		proof.consumed = true
		if w.nativeSuperseded == nil {
			w.nativeSuperseded = map[string]nativeRouteDisposition{}
		}
		w.nativeSuperseded[usageCanonical(binding.Scope)] = nativeRouteDisposition{id: proof.id, requestDigest: proof.requestDigest, route: usageCanonical(state.TurnRoute)}
	}
	w.planDelivery[planEventIdentity(output)] = planDelivery{scope: binding.Scope, bytes: string(raw)}
	return output, nil
}

// CommitPlan returns a private pointer identity for this exact acknowledged event delivery.
func (w *RunWriter) CommitPlan(ctx context.Context, binding RunBinding, state contract.Checkpoint, event contract.RunEvent) (contract.RunEvent, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.fatal != nil {
		return contract.RunEvent{}, w.fatal
	}
	if w.dispatchClosed.Load() {
		return contract.RunEvent{}, failure.Cancelled
	}
	if planEventExecution(event) != binding.Scope.Execution || planEventIdentity(event) == nil {
		return contract.RunEvent{}, &ContinuationIdentityChanged{}
	}
	if len(w.planDelivery) >= 400 {
		return contract.RunEvent{}, &ContinuationNotRecoverable{}
	}
	prepared, schedule, err := w.prepareTaskPlanLocked(binding, state)
	if err != nil {
		return contract.RunEvent{}, err
	}
	proof, err := w.nativePlanDecisionLocked(ctx, binding, &prepared, &event)
	if err != nil {
		return contract.RunEvent{}, err
	}
	if proof != nil && w.guard != nil && w.guard() != nil {
		raw, e := canonicalRaw(event)
		if e != nil {
			return contract.RunEvent{}, e
		}
		guarded, e := w.guard().JSON(raw, "message", false)
		if e != nil {
			return contract.RunEvent{}, e
		}
		if !bytes.Equal(raw, guarded) {
			return contract.RunEvent{}, &ContinuationNotRecoverable{}
		}
	}
	previous := w.taskSchedule
	if schedule != nil {
		w.taskSchedule = schedule
	}
	output, err := w.saveLocked(ctx, binding, &prepared, &event, nil, nil)
	if err != nil {
		w.taskSchedule = previous
	}
	if err != nil {
		return contract.RunEvent{}, err
	}
	if err = w.ackNativePlanDecisionLocked(binding, proof); err != nil {
		return contract.RunEvent{}, err
	}
	if w.planDelivery == nil {
		w.planDelivery = map[any]planDelivery{}
	}
	raw, err := canonicalRaw(output)
	if err != nil {
		return contract.RunEvent{}, err
	}
	w.planDelivery[planEventIdentity(output)] = planDelivery{scope: binding.Scope, bytes: string(raw)}
	return output, nil
}
func (w *RunWriter) Save(ctx context.Context, binding RunBinding, state contract.Checkpoint) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.fatal != nil {
		return w.fatal
	}
	if w.dispatchClosed.Load() {
		return w.closed()
	}
	_, err := w.saveLocked(ctx, binding, &state, nil, nil, nil)
	return err
}
func (w *RunWriter) Event(ctx context.Context, binding RunBinding, event contract.RunEvent) (contract.RunEvent, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.fatal != nil {
		return contract.RunEvent{}, w.fatal
	}
	if w.dispatchClosed.Load() {
		return contract.RunEvent{}, w.closed()
	}
	if delivery, ok := w.planDelivery[planEventIdentity(event)]; ok {
		delete(w.planDelivery, planEventIdentity(event))
		raw, err := canonicalRaw(event)
		if err != nil || delivery.scope != binding.Scope || delivery.bytes != string(raw) {
			return contract.RunEvent{}, &ContinuationIdentityChanged{}
		}
		if w.fatal != nil {
			return contract.RunEvent{}, w.fatal
		}
		return event, nil
	}
	if binding.Root && event.DelegationRequested != nil {
		held := event.DelegationRequested
		if held.CallID == nil {
			return contract.RunEvent{}, &ContinuationNotRecoverable{}
		}
		w.book.PendingHandoff = &DurablePendingHandoff{CallID: *held.CallID, Request: held.Request}
	}
	var state *contract.Checkpoint
	if event.ExecutionCheckpoint != nil {
		state = &event.ExecutionCheckpoint.Checkpoint
	}
	if event.BudgetPaused != nil {
		state = &event.BudgetPaused.Checkpoint
	}
	return w.saveLocked(ctx, binding, state, &event, nil, nil)
}
func newStorageIdentity() string { return rand.Text() }
func (b *RunBinding) WithRole(role string) *RunBinding {
	if b == nil {
		return nil
	}
	copy := *b
	copy.Scope.Role = role
	return &copy
}

// durableFingerprint is a dispatch's published fingerprint: the digest of its kind, scope, source, tool
// and request as ParseJSON reads the request. It is persisted, and a stored book is re-derived against
// it, so its form does not change. It says two requests read alike, not that they are one write: a
// float64 holds 9007199254740993 and 9007199254740992 as one number. compareDurableRequests decides
// that from the bytes the row records.
func durableFingerprint(kind string, scope DurableScope, source, tool string, request []byte) (string, error) {
	parsed, err := ParseJSON(request)
	if err != nil {
		return "", &ContinuationNotRecoverable{}
	}
	scoped, err := canonicalRaw(scope)
	if err != nil {
		return "", &ContinuationNotRecoverable{}
	}
	return durableDigest([]byte(CanonicalJSON(ObjectOf("kind", String(kind), "scope", MustParseJSON(string(scoped)), "source", String(source), "tool", String(tool), "request", parsed)))), nil
}

// durableRepeat is what a request that reads alike a recorded one is to that record.
type durableRepeat int

const (
	// durableDifferent: another write. It has its own record, or runs and makes one.
	durableDifferent durableRepeat = iota
	// durableSame: this write. The record answers it, and it does not run.
	durableSame
	// durableConflict: it cannot be told. Refused (DurableRequestConflict): it neither runs nor is answered.
	durableConflict
)

// compareDurableRequests decides, from the exact bytes, what a request with a recorded row's fingerprint
// is to that row (#275). The same bytes are the same write. Two requests their parsed form says exactly
// (exactArguments, with no lone surrogate and no bytes that are not UTF-8, which ParseJSON reads as a
// replacement character) are too: they differ only in whitespace, a string's escapes or the order of names
// that do not fold together, which the fingerprint has always left out, so a resumed run's repeat of a write
// is never performed twice. A request that is exact, beside a recorded text that is not, is refused: it is what
// that text reads as, whatever the numbers, and a neighbour written after it pays for that with a named refusal.
// Otherwise, exact numbers that differ make a different write: this is what lets a legitimate integer past 2^53
// run beside its neighbour, written first. What remains is the same values written
// differently: another spelling of a number, a repeated name, two names that fold together. A reader may
// tell those apart or may not, so nothing here says which they are.
//
// It reads only the row's request, which every published release has recorded byte for byte, so a row
// from before this rule is judged by it and needs no migration.
func compareDurableRequests(recorded, request []byte) durableRepeat {
	said := validReportUnicode(request) && exactArguments(request)
	if bytes.Equal(recorded, request) || said && validReportUnicode(recorded) && exactArguments(recorded) {
		return durableSame
	}
	// Exact, beside a recorded text the parsed form does not say: with one fingerprint, this is what that text
	// reads as, so it may be a copy of it from the model's history. A write run for it would be a second write.
	if said {
		return durableConflict
	}
	left, leftErr := canonicalRepeatArgs(recorded)
	right, rightErr := canonicalRepeatArgs(request)
	if leftErr == nil && rightErr == nil && left != right {
		return durableDifferent
	}
	return durableConflict
}

func safeDurableRequest(raw []byte) error {
	if len(raw) > StorageRecordBytes {
		return &ContinuationNotRecoverable{}
	}
	value, err := ParseJSON(raw)
	if err != nil {
		return &ContinuationNotRecoverable{}
	}
	var walk func(*Value, int) error
	walk = func(v *Value, depth int) error {
		if depth > 64 {
			return &ContinuationNotRecoverable{}
		}
		if v == nil {
			return nil
		}
		if v.IsObject() {
			if oneOf(v.Get("type"), "reasoning", "thinking", "redacted_thinking") {
				return &ContinuationNotRecoverable{}
			}
			for _, key := range v.Keys() {
				child := v.Get(key)
				if key == "body" && child != nil && child.IsString() {
					if body, err := ParseJSON([]byte(child.Str())); err == nil {
						child = body
					}
				}
				if err := walk(child, depth+1); err != nil {
					return err
				}
			}
		}
		if v.IsArray() {
			for _, child := range v.Items() {
				if err := walk(child, depth+1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(value, 0)
}

// PrepareDurableRequest selects the exact recovered body before its prompt digest and admission
// checkpoint are recorded. Admission/authority remains current; the receipt grants no authority.
func PrepareDurableRequest(binding *RunBinding, kind, source, tool string, request []byte) ([]byte, error) {
	if binding == nil {
		return request, nil
	}
	w := binding.Writer
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.preparedLocked(binding, kind, source, tool, request, false)
}
func (w *RunWriter) preparedLocked(binding *RunBinding, kind, source, tool string, request []byte, consume bool) ([]byte, error) {
	if kind == "model" || kind == "agent" {
		for i := len(w.book.Dispatches) - 1; i >= 0; i-- {
			row := w.book.Dispatches[i]
			if !w.recovered[row.ID] || row.Kind != kind || row.Scope != binding.Scope || row.State != "unknown" {
				continue
			}
			superseded, err := w.supersededNativeLocked(binding, row)
			if err != nil {
				return nil, err
			}
			if superseded {
				continue
			}
			replay := true
			if kind == "agent" && w.latest != nil && w.latest.Handoff != nil {
				handoff := w.latest.Handoff
				replay = handoff.State == "continued" && handoff.Continuation != nil && strings.Contains(row.Request, string(mustJSONText(*handoff.Continuation)))
			}
			if replay {
				if w.taskSteps.Load() || w.taskSchedule != nil || kind == "model" && !row.Read {
					return nil, &HostOutcomeUnknown{}
				}
				if row.Source != source || row.Tool != tool {
					return nil, &ContinuationIdentityChanged{}
				}
				request = []byte(row.Request)
				_ = consume
			}
			break
		}
	}
	if err := safeDurableRequest(request); err != nil {
		return nil, err
	}
	return append([]byte(nil), request...), nil
}
func (c *SessionContext) ProviderStorageIdentity(role string) string {
	id := c.Provider(role).Connection
	revision := ""
	if c.session.Context != nil {
		revision = c.session.Context.ConnectionRevisions[id]
	}
	encoded, _ := canonicalRaw(map[string]any{"id": id, "revision": revision})
	return durableDigest(encoded)
}

func RecoveringDurableRequest(binding *RunBinding, kind string) bool {
	if binding == nil {
		return false
	}
	w := binding.Writer
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, row := range w.book.Dispatches {
		if w.recovered[row.ID] && row.Kind == kind && row.Scope == binding.Scope && row.State == "unknown" {
			superseded, err := w.supersededNativeLocked(binding, row)
			if err != nil {
				w.fatal = err
				return true
			}
			if superseded {
				continue
			}
			if kind != "agent" || w.latest == nil || w.latest.Handoff == nil {
				return true
			}
			h := w.latest.Handoff
			return h.State == "continued" && h.Continuation != nil && strings.Contains(row.Request, string(mustJSONText(*h.Continuation)))
		}
	}
	return false
}

// recordedResultLocked is result in the form the record holds it (#343): what the storage pass (ADR-0018)
// leaves of its JSON, each object's members in the order the result wrote them, and its receipt carried.
// With nothing redacted it is result itself. A guard refusal is returned with no result.
func (w *RunWriter) recordedResultLocked(result ToolResult) (ToolResult, error) {
	var guard RecordGuard
	if w.guard != nil {
		guard = w.guard()
	}
	raw := result.JSON()
	clean, changed, err := recordedForm(w.policy, guard, raw)
	if err != nil {
		return ToolResult{}, err
	}
	if !changed {
		return result, nil
	}
	original, err := ParseJSON(raw)
	if err != nil {
		return ToolResult{}, err
	}
	redacted, err := ParseJSON(clean)
	if err != nil {
		return ToolResult{}, err
	}
	// A member's new name is what the same pass leaves of its name.
	rename := func(key string) string {
		raw, _ := json.Marshal(key)
		clean, _, err := recordedForm(w.policy, guard, raw)
		name := key
		if err != nil || json.Unmarshal(clean, &name) != nil {
			return key
		}
		return name
	}
	out := ReadToolResult(inOrderOf(original, redacted, rename))
	out.Receipt = result.Receipt
	return out, nil
}

// inOrderOf is redacted with each object's members in the order original wrote them (#343). The guard
// writes an object it changed with its members sorted, and a result's member order is part of the text a
// model is given, which TypeScript keeps. Every member and value is redacted's. A member whose name the
// guard rewrote (rename gives its new name) follows the others, as TypeScript's guard leaves it: in the
// sorted order of the names it had, compared as UTF-16 code units.
func inOrderOf(original, redacted *Value, rename func(string) string) *Value {
	switch {
	case original.IsObject() && redacted.IsObject():
		out := Object()
		var renamed []string
		for _, key := range original.Keys() {
			if rename(key) != key {
				renamed = append(renamed, key)
			} else if held := redacted.Get(key); held != nil {
				out.Set(key, inOrderOf(original.Get(key), held, rename))
			}
		}
		slices.SortFunc(renamed, func(a, b string) int {
			return slices.Compare(utf16.Encode([]rune(a)), utf16.Encode([]rune(b)))
		})
		for _, key := range renamed {
			if name := rename(key); redacted.Get(name) != nil {
				out.Set(name, inOrderOf(original.Get(key), redacted.Get(name), rename))
			}
		}
		for _, key := range redacted.Keys() {
			if !out.Has(key) {
				out.Set(key, redacted.Get(key))
			}
		}
		return out
	case original.IsArray() && redacted.IsArray() && original.Len() == redacted.Len():
		out := Array()
		for at, item := range redacted.Items() {
			out.Append(inOrderOf(original.Items()[at], item, rename))
		}
		return out
	}
	return redacted
}

// InvokeDurable is used only AFTER existing current offer/source/authority checks. Its execution
// callback rechecks host authority after claim acknowledgement. Generic recovered action results
// are isolated by logical execution/role/task/source/tool/args, never shared by siblings.
func InvokeDurable[T any](ctx context.Context, binding *RunBinding, kind, source, tool string, request []byte, read bool, execute func([]byte) (T, error), successful func(T) bool, artifacts func(T) []ArtifactObservation) (T, error) {
	return invokeDurable(ctx, binding, kind, source, tool, request, read, execute, nil, successful, artifacts)
}

// invokeDurable is InvokeDurable with the source gate's receipt check (#242), where admit is given:
// it runs after the physical execution and before anything of the result is recorded. A result it
// refuses is recorded as the call's uncertain outcome with the refusal as its failure, never as a
// result, so no observation, repeat or Plan step is made of it; the call stays recorded once. With
// no binding nothing is recorded, and the read wrapper holds the result back (WrapRead).
func invokeDurable[T any](ctx context.Context, binding *RunBinding, kind, source, tool string, request []byte, read bool, execute func([]byte) (T, error), admit func(T) error, successful func(T) bool, artifacts func(T) []ArtifactObservation) (T, error) {
	var zero T
	if binding == nil {
		return execute(request)
	}
	if err := safeDurableRequest(request); err != nil {
		return zero, err
	}
	if !validStorageID(source) || !validStorageID(tool) {
		return zero, &ContinuationIdentityChanged{}
	}
	w := binding.Writer
	// R-5: only a host tool's own call is a start or a cancel; a model's or an agent's row on this context is neither.
	var jobStart *jobStartDispatch
	var jobCancel *jobCancelDispatch
	if kind == "host" {
		jobStart, jobCancel = jobStartDispatchOf(ctx), jobCancelDispatchOf(ctx)
	}
	w.mu.Lock()
	if w.fatal != nil {
		err := w.fatal
		w.mu.Unlock()
		return zero, err
	}
	if state := w.repeatStateLocked(binding.Scope.Execution); state != nil && state.Stopped {
		w.mu.Unlock()
		return zero, &RepeatedToolLimit{}
	}
	if binding.Writer != w || w.dispatchClosed.Load() {
		w.mu.Unlock()
		return zero, failure.Cancelled
	}
	if w.repeatStateLocked(binding.Scope.Execution) != nil {
		if _, err := canonicalRepeatArgs(request); err != nil {
			w.mu.Unlock()
			return zero, err
		}
	}
	attempt, _ := ctx.Value(nativeRouteAttemptKey{}).(*nativeRouteAttempt)
	if attempt != nil {
		attempt.mu.Lock()
		valid := attempt.open && !attempt.entered && attempt.writer == w && attempt.scope == binding.Scope && kind == "agent" && read
		if valid {
			attempt.entered = true
		}
		attempt.mu.Unlock()
		if !valid {
			w.mu.Unlock()
			return zero, &ContinuationIdentityChanged{}
		}
	}
	request, prepareErr := w.preparedLocked(binding, kind, source, tool, request, true)
	if prepareErr != nil {
		w.mu.Unlock()
		return zero, prepareErr
	}
	fingerprint, fingerprintErr := durableFingerprint(kind, binding.Scope, source, tool, request)
	if fingerprintErr != nil {
		w.mu.Unlock()
		return zero, fingerprintErr
	}
	reclaimed := -1
	if !read {
		conflict := false
		for i, row := range w.book.Dispatches {
			if !row.Read && row.Kind == kind && row.Fingerprint == fingerprint {
				switch compareDurableRequests([]byte(row.Request), request) {
				case durableConflict:
					conflict = true
					continue
				case durableDifferent:
					continue
				}
				// #226: a dispatch the run holds as unknown is never sent again, except the one operation a
				// resumed re-authorization wait granted on this fenced view, whose recorded failure is the typed
				// never-admitted refusal: it is claimed again under its own identity, once, and its effect host
				// admits it for the first time. The effect host admits the row's digest while the tool runs these
				// bytes, so the grant takes only the very bytes that were refused. Anything else keeps the row's
				// failure and the grant, and gets what any other call gets from this row (#275,
				// compareDurableRequests): an exact variant is this write, and is answered HostOutcomeUnknown below;
				// the same values written differently are refused; different numbers are another write, with a row
				// and an identity of their own. So other bytes never run under the refused identity.
				// #229 (H2, H-1): a grant re-sends a start only while its job is unproven, by a transition under the fence.
				// A job a reconciliation closed or proved started is refused here: the row stays as it is, the grant is not
				// spent, and the call is answered from the job as any repeat is.
				reclaimRefused := false
				if jobStart != nil && row.State == "unknown" && durableDigest(request) == row.RequestDigest && reauthorizedDispatchOpen(w.Store, EffectOperation{Run: w.ID, Operation: row.ID, Source: source, Tool: tool, RequestDigest: row.RequestDigest}, row.Failure) {
					port, err := jobPortOfView(w.Store)
					if err == nil {
						_, err = port.store.ReclaimJob(ctx, w.ID, JobIdentity(w.ID, row.ID), port.fence)
					}
					if err != nil && asJobRefusal(err) == nil {
						w.fatal = err
						w.mu.Unlock()
						return zero, err
					}
					reclaimRefused = err != nil
					jobStart.job = JobIdentity(w.ID, row.ID)
				}
				if !reclaimRefused && row.State == "unknown" && (kind == "host" || kind == "runtime") && durableDigest(request) == row.RequestDigest && takeReauthorizedDispatch(w.Store, EffectOperation{Run: w.ID, Operation: row.ID, Source: source, Tool: tool, RequestDigest: row.RequestDigest}, row.Failure) {
					reclaimed = i
					break
				}
				w.mu.Unlock()
				// #229: a repeated start is answered from its job's record, whatever its row holds, and is never sent
				// again. A row says nothing about its job: result holds words from an earlier moment. Only with the job
				// record lost does a result answer.
				if jobStart != nil {
					port, err := jobPortOfView(w.Store)
					if err != nil {
						return zero, err
					}
					held, err := port.store.ReadJob(ctx, w.ID, JobIdentity(w.ID, row.ID))
					if err != nil {
						return zero, err
					}
					if held != nil {
						job, err := jobReadBack(*held, w.ID, JobIdentity(w.ID, row.ID))
						if err != nil {
							return zero, err
						}
						return any(JobStartResult(job)).(T), nil
					}
				}
				// A repeated cancel likewise: a row says nothing about its job, and nothing is sent again.
				if jobCancel != nil {
					if answer := w.jobCancelAnswer(ctx, jobCancel.job, true); answer != nil {
						return any(*answer).(T), nil
					}
				}
				if row.State != "result" || row.Result == nil {
					return zero, &HostOutcomeUnknown{}
				}
				if _, ok := any(zero).(ToolResult); ok {
					parsed, err := ParseJSON(row.Result)
					if err != nil {
						return zero, err
					}
					return any(ReadToolResult(parsed)).(T), nil
				}
				var result T
				if err := json.Unmarshal(row.Result, &result); err != nil {
					return zero, err
				}
				return result, nil
			}
		}
		if conflict && reclaimed < 0 {
			w.mu.Unlock()
			return zero, &DurableRequestConflict{}
		}
	}
	// A record answers first: a published release may have recorded what this release cannot.
	if (kind == "host" || kind == "runtime") && reclaimed < 0 {
		if _, err := readHistoryRequestJSON(request); err != nil {
			w.mu.Unlock()
			return zero, &DurableRequestUnrecordable{}
		}
	}
	var claim DurableDispatch
	var registration *JobRegistration
	var usageTicketHeld *usageTicket
	if reclaimed >= 0 {
		w.book.Dispatches[reclaimed].State = "claimed"
		w.book.Dispatches[reclaimed].Failure = nil
		claim = w.book.Dispatches[reclaimed]
	} else {
		if len(w.book.Dispatches) >= 400 {
			w.mu.Unlock()
			return zero, &ContinuationNotRecoverable{}
		}
		claimID := newStorageIdentity()
		if w.budget != nil && (kind == "model" || kind == "agent") {
			ticket, _ := ctx.Value(usageDispatchContextKey{}).(*usageTicket)
			usageTicketHeld = ticket
			var err error
			claimID, err = w.budget.consume(ticket, usageDispatchFacts{DurableScope: binding.Scope, Source: source, Tool: tool, Descriptor: string(request), UsageBoundRequest: UsageBoundRequest{Kind: kind}})
			if err != nil {
				w.mu.Unlock()
				return zero, err
			}
		}
		claim = DurableDispatch{ID: claimID, Scope: binding.Scope, Kind: kind, Source: source, Tool: tool, Fingerprint: fingerprint, Request: string(request), RequestDigest: durableDigest(request), State: "claimed", Read: read}
		// #229: a start's job is registered in its claim's own transaction, so there is no claim without a job. Its
		// key digest is the host's and is checked first: a refusal here or by the store leaves no row, and it is
		// the call's answer.
		if jobStart != nil {
			jobStart.job = JobIdentity(w.ID, claim.ID)
			keyDigest, err := jobStart.mint(withoutJobDispatch(ctx), jobStart.job)
			if err != nil || !jobDigest.MatchString(keyDigest) {
				w.mu.Unlock()
				return zero, jobInvalid()
			}
			registration = &JobRegistration{Job: jobStart.job, Operation: claim.ID, Source: source, Tool: tool, RequestDigest: claim.RequestDigest, KeyDigest: keyDigest}
		}
		if !w.taskSteps.Load() && kind == "model" && w.repeatStateLocked(binding.Scope.Execution) != nil && MustParseJSON(string(request)).Get("method").Str() != "GET" {
			charge := w.repeatRequestCharges[binding.Scope.Execution]
			if charge == nil {
				w.mu.Unlock()
				return zero, &ContinuationIdentityChanged{}
			}
			recovering := false
			for _, prior := range w.book.Dispatches {
				if w.recovered[prior.ID] && prior.Kind == "model" && prior.Scope == binding.Scope && prior.State == "unknown" {
					recovering = true
				}
			}
			if !recovering {
				descriptor := MustParseJSON(string(request))
				body := string(request)
				if descriptor.Get("body").IsString() {
					body = descriptor.Get("body").Str()
				}
				if err := charge(body); err != nil {
					w.mu.Unlock()
					return zero, err
				}
			}
		}
		if err := w.claimTaskDispatchLocked(ctx, *binding, claim); err != nil {
			w.mu.Unlock()
			return zero, err
		}
		if state := w.repeatStateLocked(binding.Scope.Execution); state != nil && state.Reminded && !state.ReminderDelivered && (kind == "model" || kind == "agent") && strings.Contains(claim.Request, RepeatReminder) {
			state.ReminderDelivered = true
		}
		if kind == "agent" && w.capture != nil {
			if slot, _ := ctx.Value(nativeCaptureKey{}).(*nativeCaptureSlot); slot != nil && slot.writer == w && slot.scope == binding.Scope && slot.bindings != nil {
				w.capture.bindings[claim.ID] = usageCopy(slot.bindings)
			} else {
				w.capture.unavailable = true
			}
		}
		w.book.Dispatches = append(w.book.Dispatches, claim)
	}
	claimEvent, claimErr := nativePlanClaimEventLocked(ctx, binding, claim)
	if claimErr != nil {
		w.mu.Unlock()
		return zero, claimErr
	}
	w.jobRegistration = registration
	_, err := w.saveLocked(ctx, *binding, nil, claimEvent, nil, nil)
	if registration != nil {
		w.jobRegistration = nil
		if asJobRefusal(err) != nil {
			w.book.Dispatches = w.book.Dispatches[:len(w.book.Dispatches)-1]
			w.fatal = nil
			w.mu.Unlock()
			return zero, err
		}
	}
	if err == nil && kind == "agent" {
		if slot, _ := ctx.Value(nativeCaptureKey{}).(*nativeCaptureSlot); slot != nil && slot.writer == w && slot.scope == binding.Scope && slot.id == "" {
			slot.id = claim.ID
			slot.request = claim.Request
		}
	}
	w.mu.Unlock()
	if err != nil {
		return zero, err
	}
	if err = ctx.Err(); err != nil {
		return zero, err
	}
	if w.dispatchClosed.Load() {
		return zero, failure.Cancelled
	}
	if w.budget != nil && kind == "agent" && usageTicketHeld != nil {
		usageTicketHeld.nativeWitness = usagewitness.New(w, usageTicketHeld)
	}
	var routeClaim *nativeRouteClaim
	if attempt != nil {
		w.mu.Lock()
		attempt.mu.Lock()
		if !attempt.open || attempt.claim != nil || w.latest == nil || w.latest.TurnRoute == nil || w.latest.TurnRoute.Widened || w.latest.TurnRoute.BindingDigest != attempt.route {
			attempt.mu.Unlock()
			w.mu.Unlock()
			return zero, &ContinuationIdentityChanged{}
		}
		routeClaim = &nativeRouteClaim{scope: binding.Scope, id: claim.ID, requestDigest: claim.RequestDigest, request: string(request), source: source, tool: tool, route: attempt.route, live: true}
		if w.nativeClaims == nil {
			w.nativeClaims = map[*nativeRouteClaim]bool{}
		}
		w.nativeClaims[routeClaim] = true
		attempt.claim = routeClaim
		attempt.mu.Unlock()
		w.mu.Unlock()
	}
	// R-4, F-17: set once the host's execute returned with no error inside the effect host's admission.
	performed := false
	if (kind == "host" || kind == "runtime") && !read && leaseViewOf(w.Store) != nil {
		// #223: under a fenced store, a non-read host or runtime effect is admitted by the effect host with
		// the fence; its idempotency identity is the acknowledged claim's, never the epoch.
		inner, operation := execute, EffectOperation{Run: w.ID, Operation: claim.ID, Source: source, Tool: tool, RequestDigest: claim.RequestDigest}
		execute = func(raw []byte) (T, error) {
			var out T
			// R-5, F-15: the effect host gets a context that holds no job dispatch.
			err := fencedEffect(withoutJobDispatch(ctx), w.Store, operation, func(context.Context) error {
				var runErr error
				out, runErr = inner(raw)
				if runErr == nil {
					performed = true
				}
				return runErr
			})
			return out, err
		}
	}
	var value T
	var runErr error
	// #229 (H-1): from here the start has run, so nothing that goes wrong may read as refused input: the row holds the
	// fixed unknown failure, and the model is answered from the job. A cancel likewise, once it has been asked for.
	var answeredFromJob *ToolResult
	cancelRefused := false
	if jobCancel != nil {
		// `requested` is recorded under the current fence before admission, and what is sent is that record's. A job
		// that settled since it was asked about refuses here: nothing is sent.
		if err := w.jobCancelRequest(ctx, jobCancel, claim.ID); err != nil {
			if asJobRefusal(err) != nil {
				runErr, answeredFromJob, cancelRefused = jobCancelNotSent(), w.jobCancelAnswer(ctx, jobCancel.job, false), true
			} else {
				runErr = err
			}
		}
	}
	if runErr == nil {
		value, runErr = execute(request)
	}
	// R-4, F-17: the execute returned and the effect host refused afterwards. The start ran and the cancel left, so nothing
	// reads as refused input: a start's answer goes to its job and the model is answered from it, and either row holds its
	// fixed failure.
	if performed && runErr != nil {
		switch {
		case jobStart != nil:
			_, _ = w.finishJobStart(ctx, jobStart)
			runErr, answeredFromJob = jobStartUnrecorded(), w.jobStartAsRecorded(ctx, jobStart.job)
		case jobCancel != nil:
			runErr = jobCancelUnrecorded()
		}
	}
	if jobCancel != nil && runErr == nil {
		if answer := w.jobCancelAnswer(ctx, jobCancel.job, true); answer != nil {
			value = any(*answer).(T)
		} else {
			runErr = jobCancelUnrecorded()
		}
	}
	if runErr == nil && admit != nil && ctx.Err() == nil {
		// The execution ended within its deadline. The check has the rest of the source's deadline,
		// and its outcome is this dispatch's whenever it ends: the time it takes is not the call's.
		ctx = context.WithoutCancel(ctx)
		runErr = admit(value)
	}
	if jobStart != nil && runErr == nil {
		composed, err := w.finishJobStart(ctx, jobStart)
		if err == nil {
			value = any(composed).(T)
		} else {
			runErr, answeredFromJob = jobStartUnrecorded(), w.jobStartAsRecorded(ctx, jobStart.job)
		}
	}
	var admitted bool
	if usageTicketHeld != nil {
		usagewitness.Close(usageTicketHeld.nativeWitness)
		admitted = usagewitness.Admitted(usageTicketHeld.nativeWitness, w, usageTicketHeld)
	}
	// #226: the typed never-admitted failure means a refusal the package trusted for this very dispatch, and
	// it is what lets a resumed re-authorization wait send the row again. Any other failure of that shape (a
	// tool's, after admission) is recorded and reported as an unknown outcome, with nothing said about
	// admission.
	refused := false
	if kind == "host" || kind == "runtime" {
		if refused = takeFreshReauthorization(runErr); !refused && runErr != nil {
			if held := failure.Normalize(runErr, "tool", time.Now()); isNeverAdmittedFailure(&held) {
				runErr = failure.Err("authentication", "tool", "unknown")
			}
		}
	}
	// F-1: where the cancel was never handed to the host, the answer says it was not sent, whatever the record asked.
	if jobCancel != nil && !refused && !cancelRefused && runErr != nil && answeredFromJob == nil {
		answeredFromJob = w.jobCancelAnswer(ctx, jobCancel.job, jobCancel.sent)
	}
	w.mu.Lock()
	if routeClaim != nil {
		routeClaim.live = false
	}
	if w.dispatchClosed.Load() {
		w.mu.Unlock()
		return zero, failure.Cancelled
	}
	if kind == "model" && runErr == nil {
		if handle, ok := any(value).(*modelCompletion); ok && handle != nil && handle.writer == nil && handle.id == "" {
			handle.writer = w
			handle.binding = *binding
			handle.id = claim.ID
			w.models[handle] = true
			w.mu.Unlock()
			return value, nil
		}
		// A generic model return has no terminal proof. Only the live completion path may finish it.
		runErr = failure.Err("invalid-response", "model", "unknown")
	}
	if kind == "agent" {
		for _, row := range w.book.Dispatches {
			if row.Kind == kind && row.Scope == binding.Scope && row.State == "unknown" {
				delete(w.recovered, row.ID)
			}
		}
	}
	defer w.mu.Unlock()
	for i, row := range w.book.Dispatches {
		if row.ID == claim.ID {
			updated := row
			if runErr != nil {
				updated.State = "unknown"
				if w.budget != nil && (kind == "model" || kind == "agent") && !(kind == "agent" && admitted && ctx.Err() == nil) {
					w.budget.unknown(claim.ID)
				}
				held := failure.Normalize(runErr, "tool", time.Now())
				updated.Failure = &held
			} else {
				updated.State = "result"
				if kind == "host" || kind == "runtime" {
					// #343: the result is recorded in the form the record guard leaves of it, and the call is
					// answered with that same form, so the record and what the model is given agree and the
					// claim settles. An external read's checked result, which the source gate answers with, is
					// given the same form. A job's start or cancel is answered from its job's record (ADR-0025),
					// so its row keeps today's rule: a redacted answer cannot be recorded.
					if result, ok := any(value).(ToolResult); ok && jobStart == nil && jobCancel == nil {
						recorded, err := w.recordedResultLocked(result)
						if held := receiptAdmissionOf(ctx); err == nil && admit != nil && held != nil {
							err = held.deliver(w.recordedResultLocked)
						}
						if err != nil {
							w.fatal = &StorageOutcomeUnknown{cause: err}
							return zero, w.fatal
						}
						value = any(recorded).(T)
					}
					if w.repeatStateLocked(binding.Scope.Execution) != nil {
						var exact []byte
						if result, ok := any(value).(ToolResult); ok {
							exact = result.JSON()
						} else {
							exact, err = json.Marshal(value)
						}
						if err != nil || len(exact) == 0 || len(exact) > StorageRecordBytes {
							w.fatal = &StorageOutcomeUnknown{cause: err}
							return zero, w.fatal
						}
						text := string(exact)
						updated.RepeatResultJson = &text
					}
					if result, ok := any(value).(ToolResult); ok {
						updated.Result = bytes.Clone(result.JSON())
					} else {
						updated.Result, err = canonicalRaw(value)
						if err != nil {
							w.fatal = err
							return zero, err
						}
					}
				}
			}
			w.book.Dispatches[i] = updated
			claim = updated
			break
		}
	}
	var observed *DurableDispatch
	var observations []ArtifactObservation
	if runErr == nil && (kind == "host" || kind == "runtime") && (successful == nil || successful(value)) {
		observed = &claim
		if artifacts != nil {
			observations = artifacts(value)
		}
		for _, artifact := range observations {
			if !validStorageID(artifact.ID) || (artifact.Kind != "artifact" && artifact.Kind != "dialect") || !recordDigestPattern.MatchString(artifact.Digest) {
				w.fatal = &ContinuationNotRecoverable{}
				return zero, w.fatal
			}
		}
	}
	if observed != nil {
		for i, row := range w.book.Dispatches {
			if row.ID == claim.ID {
				w.book.Dispatches[i].Artifacts = append([]ArtifactObservation{}, observations...)
				break
			}
		}
	}
	if ctx.Err() != nil {
		w.dispatchClosed.Store(true)
		for i := range w.book.Dispatches {
			if w.book.Dispatches[i].ID == claim.ID {
				w.book.Dispatches[i].State = "unknown"
				w.book.Dispatches[i].Result = nil
				w.book.Dispatches[i].RepeatResultJson = nil
				w.book.Dispatches[i].Artifacts = nil
			}
		}
		return zero, ctx.Err()
	}

	var repeatEvent *contract.RunEventRepeatDetected
	if current := w.repeatStateLocked(binding.Scope.Execution); current != nil && runErr == nil && (kind == "host" || kind == "runtime") {
		next, event, e := observeRepeat(*current, claim)
		if e != nil {
			w.fatal = &StorageOutcomeUnknown{cause: e}
			return zero, w.fatal
		}
		*current = next
		repeatEvent = event
	}
	extra := []contract.RunEvent{}
	if repeatEvent != nil {
		extra = append(extra, contract.StampEvent(contract.RunEvent{RepeatDetected: repeatEvent}))
	}
	paused, pausedEvent, pauseErr := nativePlanOutcomeLocked(ctx, binding, claim, runErr)
	if pauseErr != nil {
		return zero, pauseErr
	}
	if _, err = w.saveLocked(ctx, *binding, paused, pausedEvent, observed, observations, extra...); err != nil {
		w.fatal = &StorageOutcomeUnknown{cause: err}
		return zero, w.fatal
	}
	if kind == "agent" && runErr == nil {
		w.ackCaptureLocked(claim.ID)
	}
	if current := w.repeatStateLocked(binding.Scope.Execution); current != nil {
		source := w.repeatScopes[binding.Scope.Execution]
		if source.accept != nil {
			source.accept(*current)
		}
		if repeatEvent != nil && source.event != nil {
			event := contract.StampEvent(contract.RunEvent{RepeatDetected: repeatEvent})
			w.rememberRepeatEvent(*binding, event)
			source.event(event)
		}
	}
	if w.budget != nil && kind == "agent" && (runErr == nil || admitted) {
		usageErr := w.budget.observe(ctx, claim.ID, UsageAmounts{"providerInvocations": 1}, durableDigest([]byte(claim.ID+":agent-outcome")))
		if _, err = w.saveLocked(ctx, *binding, nil, nil, nil, nil); err != nil {
			w.fatal = &StorageOutcomeUnknown{cause: err}
			return zero, w.fatal
		}
		if usageErr != nil {
			w.fatal = usageErr
			return zero, usageErr
		}
	}

	if kind == "model" && runErr != nil || failure.IsCancelled(runErr) && !(kind == "agent" && (w.book.PendingHandoff != nil || nativePlanPauseAcknowledgedLocked(ctx, binding, claim))) {
		w.dispatchClosed.Store(true)
	} else if refused {
		// #226: a trusted re-authorization refusal stops the run at the operation it refused: nothing after it
		// is committed and nothing more is dispatched, so the run's head holds it as the never-admitted
		// dispatch a park binds.
		w.closure = runErr
		w.dispatchClosed.Store(true)
	}
	if answeredFromJob != nil {
		return any(*answeredFromJob).(T), nil
	}
	return value, runErr
}

// closed is what a commit after closed dispatch refuses with: the re-authorization refusal that closed it,
// or cancellation.
func (w *RunWriter) closed() error {
	if w.closure != nil {
		return w.closure
	}
	return failure.Cancelled
}

// PrepareDurableInput loads only for an explicit host start/resume. It never resumes on GET/reload. It does not know
// the request, so it makes no successor (#340); PrepareDurableInputFor does.
func PrepareDurableInput(ctx context.Context, input *Input) (*Input, error) {
	return prepareDurableInput(ctx, input, nil)
}

// PrepareDurableInputFor is PrepareDurableInput for an open that names its request, as a session does: a successor's
// first checkpoint is written only for the request and phase its predecessor's holds.
func PrepareDurableInputFor(ctx context.Context, input *Input, request DurableRequest) (*Input, error) {
	return prepareDurableInput(ctx, input, &request)
}

func prepareDurableInput(ctx context.Context, input *Input, request *DurableRequest) (*Input, error) {
	if input == nil || input.Persistence != nil {
		return input, nil
	}
	// #341: a question wait's continuation is a run store's, and only of a head the store holds.
	if input.QuestionContinuation != "" && input.RunStore == nil {
		return nil, waitInvalid()
	}
	// ADR-0029: a durable root's delegation is refused, by name, before its store is touched.
	var record *PrincipalRecord
	if input.RunStore != nil {
		if input.Principal != nil {
			opened, err := OpenRunPrincipal(input.Principal, nil)
			if err != nil {
				return nil, err
			}
			record = &opened.Record
		}
		if err := holdLeasePrincipal(input.RunStore.Store, record); err != nil {
			return nil, err
		}
	}
	if input.Capture != nil {
		if input.RunStore == nil || input.RunStore.Store == nil || input.RunStore.Store.Durability() == RunStoreSingleWriterCallback {
			return nil, &ReplayUnavailable{}
		}
		if _, ok := input.RunStore.Store.(HistoryRunStore); !ok {
			return nil, &ReplayUnavailable{}
		}
	}
	if input.UsageBudget != nil && (input.RunStore == nil || input.RunStore.Store == nil || input.RunStore.Store.Durability() == RunStoreSingleWriterCallback) {
		return nil, &UsageBudgetUnsupported{}
	}
	if input.checkpointProfile {
		if input.Execution != nil && input.Execution.Durable != nil {
			return nil, &ContinuationNotRecoverable{}
		}
		return input, nil
	}
	if input.RunStore == nil && input.Checkpoint == nil {
		if input.Execution != nil && input.Execution.Durable != nil {
			if input.Execution.Version > contract.CheckpointVersion {
				return nil, &contract.UnsupportedRecordVersion{Kind: "checkpoint", Version: input.Execution.Version}
			}
			return nil, &ContinuationNotRecoverable{}
		}
		return input, nil
	}
	copy := *input
	if copy.RequestCap != nil {
		cap := *copy.RequestCap
		copy.RequestCap = &cap
	}
	if copy.RunStore != nil && copy.Checkpoint != nil {
		return nil, &ContinuationIdentityChanged{}
	}
	id := newStorageIdentity()
	var store RunStore
	var head *RunHead
	var err error
	successorFirst := false
	var predecessor *RunHead
	if copy.Execution != nil {
		id = copy.Execution.ID
	}
	if copy.RunStore != nil {
		id = copy.RunStore.ID
		store = copy.RunStore.Store
		if !validStorageID(id) || store == nil {
			return nil, &ContinuationIdentityChanged{}
		}
		if err = readSuccessorInput(&copy); err != nil {
			return nil, err
		}
		if copy.RunStore.Successor != nil && store.Durability() == RunStoreSingleWriterCallback {
			return nil, successorRefused("invalid")
		}
		head, err = store.Load(ctx, id)
		if err != nil {
			return nil, err
		}
		// #340: a question has one successor; making it again meets the one already made.
		if copy.RunStore.Successor != nil && !copy.RunStore.Resume && head != nil {
			return nil, successorRefused("succeeded")
		}
		if (head != nil) != copy.RunStore.Resume {
			return nil, &StorageConflict{}
		}
		if head == nil && copy.QuestionContinuation != "" {
			return nil, waitInvalid()
		}
		if head != nil {
			if head.Checkpoint == nil {
				return nil, &ContinuationNotRecoverable{}
			}
			value, e := ParseJSON(head.Checkpoint)
			if e != nil {
				return nil, e
			}
			saved, e := ReadExecution(value)
			if e != nil {
				return nil, e
			}
			if copy.Execution != nil {
				a, _ := canonicalRaw(copy.Execution)
				b, _ := canonicalRaw(saved)
				if !bytes.Equal(a, b) {
					return nil, &ContinuationIdentityChanged{}
				}
			}
			copy.Execution = saved
			// #340: a successor's head opens only while its predecessor holds it up, every time.
			if copy.RunStore.Successor != nil {
				_, _, answered, err := readPredecessor(ctx, store, *copy.RunStore.Successor)
				if err != nil {
					return nil, err
				}
				copy.questionAnswers = append(copy.questionAnswers, *answered)
			}
			// #341: a claimed question wait's continuation hands on the wait's resolution, and only at the head the
			// wait was parked at.
			var claimed *questionAnswer
			if copy.QuestionContinuation != "" {
				if copy.JobContinuation != nil || !validStorageID(copy.QuestionContinuation) {
					return nil, waitInvalid()
				}
				if claimed, err = claimedQuestionWait(ctx, store, id, copy.QuestionContinuation, head.Revision); err != nil {
					return nil, err
				}
				if claimed == nil && saved.Status != "complete" {
					return nil, waitRefused("mismatch")
				}
			}
			// #328: a complete head resumes only as a claimed job wait's continuation, a claimed question wait's (#341),
			// a re-authorization claim's, or a leg of an open objective (#330).
			if saved.Status == "complete" {
				admitted := reauthorizedContinuation(store, head.Checkpoint) || claimed != nil
				if !admitted && copy.JobContinuation != nil {
					if admitted, err = claimedJobWait(ctx, store, id, copy.JobContinuation.Job, head.Revision); err != nil {
						return nil, err
					}
				}
				if !admitted {
					if admitted, err = openObjective(ctx, store, id, saved); err != nil {
						return nil, err
					}
				}
				if !admitted {
					return nil, &RunComplete{}
				}
				// #340: once the head's last question has its successor, the successor carries the allowance, and the
				// head resumes no other way.
				succeeded, err := succeededHead(ctx, store, id)
				if err != nil {
					return nil, err
				}
				if succeeded {
					return nil, successorRefused("succeeded")
				}
			}
			if claimed != nil {
				copy.questionAnswers = append(copy.questionAnswers, *claimed)
			}
		} else if copy.RunStore.Successor != nil {
			var answered *questionAnswer
			if copy.Execution, answered, predecessor, err = makeSuccessor(ctx, store, &copy, id, *copy.RunStore.Successor, request); err != nil {
				return nil, err
			}
			// ADR-0029: the successor carries the predecessor's principal, and is made only under it.
			if PrincipalsChanged(copy.Execution.Principal, record) {
				return nil, failure.Authored("stale-state", "The run continues under another principal than it began under (principal-changed). Nothing was sent.", "context")
			}
			copy.questionAnswers = []questionAnswer{*answered}
			successorFirst = true
		}
	} else {
		store = NewCheckpointCallbackRunStore(copy.Checkpoint, func(raw []byte) (contract.Checkpoint, error) {
			value, e := ParseJSON(raw)
			if e != nil {
				return contract.Checkpoint{}, e
			}
			saved, e := ReadExecution(value)
			if e != nil {
				return contract.Checkpoint{}, e
			}
			return *saved, nil
		})
	}
	if store.Durability() == RunStoreSingleWriterCallback {
		if head != nil && (head.RunID != id || !validStorageID(head.Revision) || !validStorageID(head.CommitID) || head.Cursor != "" && !validStorageID(head.Cursor)) {
			return nil, &StorageAcknowledgementInvalid{}
		}
		if copy.Execution != nil && copy.Execution.ID != id {
			return nil, &ContinuationIdentityChanged{}
		}
		if copy.Execution != nil && copy.Execution.Durable != nil {
			return nil, &ContinuationNotRecoverable{}
		}
		copy.checkpointProfile = true
		copy.checkpointRunID = id
		copy.RunStore = nil
		copy.Checkpoint = legacyCheckpointSave(store, id, head)
		return &copy, nil
	}
	if copy.Execution != nil && copy.Execution.Durable != nil {
		if copy.Execution.Durable.PendingHandoff != nil && copy.Execution.Handoff == nil {
			return nil, &ProviderReleaseUnknown{}
		}
		for _, row := range copy.Execution.Durable.Dispatches {
			if row.Failure != nil && row.Failure.Detail != nil && strings.HasPrefix(*row.Failure.Detail, "ProviderReleaseUnknown:") {
				return nil, &ProviderReleaseUnknown{}
			}
		}
	}
	if copy.RunStore != nil && copy.RunStore.Context != nil {
		source := copy.RunStore.Context
		if !sameHistoryStore(source.store, store) || copy.Chat != source && !(source.LedgerScope() != "" && copy.Chat == nil) {
			return nil, &ContinuationIdentityChanged{}
		}
		if head != nil {
			history, err := source.store.ReadHistoryHead(ctx, id)
			if err != nil {
				return nil, err
			}
			if history == nil || history.ExecutionRevision != head.Revision || !source.matches(history.Context) {
				return nil, &ContinuationIdentityChanged{}
			}
		}
	}
	if copy.RunStore != nil && copy.RunStore.Context != nil && copy.RunStore.Context.LedgerScope() != "" {
		if copy.Execution == nil || copy.Execution.Context.Scope != copy.RunStore.Context.LedgerScope() || copy.Execution.Status == "complete" || copy.Execution.Status == "stopped" {
			return nil, &ContinuationIdentityChanged{}
		}
		copy.Chat = nil
	}
	writer, err := NewRunWriter(store, id, head, copy.Execution)
	if err != nil {
		return nil, err
	}
	if err = writer.enrollCapture(copy.Capture); err != nil {
		return nil, err
	}
	if err = writer.attachUsage(ctx, copy.UsageBudget); err != nil {
		return nil, err
	}
	if copy.RunStore != nil {
		writer.contextSource = copy.RunStore.Context
		if writer.contextSource != nil && head != nil {
			if err := writer.commitLocked(ctx, newStorageIdentity(), *writer.latest, head.Checkpoint, [][]byte{}); err != nil {
				return nil, err
			}
		}
		// #340: the successor's first checkpoint is created once at its derived id, before anything of the run is sent,
		// so a second successor made at the same time meets this one. It is made under the predecessor's lease, where the
		// store has leases (HIGH-1): an admitted continuation of the predecessor holds that lease, or has lost it to
		// this one's new epoch. The predecessor and its waits are read again after the create: a head that moved, or a
		// question wait parked meanwhile, refuses the successor, and the wait's continuation is refused as well because
		// the successor exists. Neither sends anything.
		if successorFirst {
			asked := copy.RunStore.Successor
			release, err := holdPredecessor(ctx, store, id, *asked, predecessor, copy.Execution.Principal)
			if err != nil {
				return nil, err
			}
			defer release()
			raw, err := canonicalRaw(writer.latest)
			if err == nil {
				err = writer.commitLocked(ctx, newStorageIdentity(), *writer.latest, raw, [][]byte{})
			}
			if errors.As(err, new(*StorageConflict)) {
				return nil, successorRefused("succeeded")
			}
			if err != nil {
				return nil, err
			}
			if err := predecessorUnmoved(ctx, store, *asked, predecessor); err != nil {
				return nil, err
			}
			parked, err := questionWaitHeld(ctx, store, asked.Predecessor, asked.Question)
			if err != nil {
				return nil, err
			}
			if parked {
				return nil, successorRefused("parked")
			}
		}
	}
	binding := &RunBinding{Writer: writer, Scope: DurableScope{Execution: id, Role: "author"}, Root: true}
	if writer.capture != nil {
		binding.captureRoot = &captureRootAuthority{writer: writer}
		writer.capture.rootAuthority = binding.captureRoot
	}
	if err = writer.restoreNativePlanDecisions(ctx, *binding); err != nil {
		return nil, err
	}
	copy.Persistence = binding
	copy.Checkpoint = func(ctx context.Context, state contract.Checkpoint) error { return writer.Save(ctx, *binding, state) }
	return &copy, nil
}
func ChildPersistence(parent *Input, child *Input, task string) {
	if parent == nil || parent.Persistence == nil {
		return
	}
	binding := &RunBinding{Writer: parent.Persistence.Writer, Scope: DurableScope{Execution: task, Role: "author", Task: task}}
	child.Persistence = binding
	child.UsageBudget = parent.UsageBudget
	child.Execution = binding.Writer.Child(task)
	child.Checkpoint = func(ctx context.Context, state contract.Checkpoint) error {
		return binding.Writer.Save(ctx, *binding, state)
	}
}
func (c *SessionContext) Persistence(role string) *RunBinding {
	if c.session.Context == nil {
		return nil
	}
	if c.Reformatting {
		role = "reformat"
	}
	return c.session.Context.Persistence.WithRole(role)
}
func (c *SessionContext) PersistEvents(events []contract.RunEvent) ([]contract.RunEvent, error) {
	binding := c.Persistence("author")
	if binding == nil {
		return currentEvents(events), nil
	}
	out := []contract.RunEvent{}
	for _, event := range events {
		saved, err := binding.Writer.Event(c.outputContext, *binding, event)
		if err != nil {
			return out, err
		}
		out = append(out, saved)
	}
	return out, nil
}
func (c *SessionContext) ExecuteDurableHost(ctx context.Context, tool HostTool, args json.RawMessage, execute func(context.Context, json.RawMessage) (ToolResult, error)) (ToolResult, error) {
	role := "author"
	if c.CriticReading {
		role = "critic"
	}
	source := "host"
	if tool.Source != nil {
		raw, _ := canonicalRaw(tool.Source.Record(tool.SourceTool))
		source = durableDigest(raw)
	}
	// #229: a job tool's rows are never plan evidence; a start runs the row's own bytes under its job's identity.
	binding := c.Persistence(role)
	var jobStart *jobStartDispatch
	if tool.job != nil && tool.job.start != nil {
		jobStart = &jobStartDispatch{mint: tool.job.mint}
		ctx = context.WithValue(ctx, jobStartKey{}, jobStart)
	}
	var jobCancel *jobCancelDispatch
	if tool.job != nil && tool.job.cancel != nil {
		// H5, M4: what the record refuses is refused here as well, before any claim, whatever was asked and when.
		if refused := c.jobCancelRefusal(ctx, tool.job, args); refused != "" {
			return ToolResult{}, jobRefused(refused)
		}
		job, err := readJobCancelArgument(args)
		if err != nil {
			return ToolResult{}, err
		}
		jobCancel = &jobCancelDispatch{job: job}
		ctx = context.WithValue(ctx, jobCancelKey{}, jobCancel)
	}
	// #242: an external read's result is recorded only once its receipt verifies.
	var admit func(ToolResult) error
	if held := receiptAdmissionOf(ctx); held != nil {
		admit = held.admit
	}
	return invokeDurable(ctx, binding, "host", source, tool.Name, args, durableReadingTool(&tool), func(raw []byte) (ToolResult, error) {
		// R-5, F-15: the host's own checks run on a context that holds no job dispatch.
		if c.session.Context != nil && c.session.Context.Authorize != nil {
			if err := c.session.Context.Authorize(withoutJobDispatch(ctx)); err != nil {
				return ToolResult{}, err
			}
		}
		if admit := dispatchAdmissionOf(ctx); admit != nil {
			if err := admit(ctx); err != nil {
				return ToolResult{}, err
			}
		}
		if jobCancel != nil && jobCancel.record != nil {
			// What a cancel returns as an error is no answer: its words reach neither the row nor the model.
			jobCancel.sent = true
			if err := tool.job.cancel(withoutReceiptAdmission(ctx), JobCancelInputOf(*jobCancel.record, jobCancel.operation)); err != nil {
				return ToolResult{}, jobCancelUnrecorded()
			}
			return ToolResult{}, nil
		}
		if jobStart != nil && jobStart.job != "" {
			answer, err := tool.job.start(withoutReceiptAdmission(ctx), JobStartInput{Job: jobStart.job, Request: bytes.Clone(raw)})
			if err != nil {
				// What a start returns as an error is no answer: its words reach neither the row nor the model. A refusal
				// the package trusts is minted before anything is performed, so it never comes from here.
				return ToolResult{}, nil
			}
			jobStart.answer = &answer
			return ToolResult{}, nil
		}
		return execute(withoutReceiptAdmission(ctx), raw)
	}, admit, func(result ToolResult) bool { return tool.job == nil && !result.Err() }, func(result ToolResult) []ArtifactObservation {
		if tool.Artifacts != nil {
			return tool.Artifacts(result, args)
		}
		return nil
	})
}

// durableReadingTool (#223) is whether a host tool's call is a read for the writer, and so never reaches
// the effect host. It is the call's effect, not the framing of its answer (ReadingTool): a tool with a
// source is a read only when its effect class is read, and an owner-declared act or propose effect is
// never a read. Every other call is an effect: its outcome is recorded before dependent work, an unknown
// outcome is never repeated, and under a fenced store the effect host admits it.
func durableReadingTool(tool *HostTool) bool { return DurableReadingTool(tool) }

// DurableReadingTool is durableReadingTool for the adapters: whether a host tool's call is a read for
// the durable record (#253), as distinct from ReadingTool, which frames its answer.
func DurableReadingTool(tool *HostTool) bool {
	if tool.Taxonomy != nil && (tool.Taxonomy.Effect == "act" || tool.Taxonomy.Effect == "propose") {
		return false
	}
	if tool.Source != nil {
		return tool.EffectClass == "read"
	}
	return ReadingTool(tool)
}

// Authorize rechecks current host authority immediately before an acknowledged provider dispatch.
func (c *SessionContext) Authorize(ctx context.Context) error {
	if c.session.Context != nil && c.session.Context.Authorize != nil {
		return c.session.Context.Authorize(ctx)
	}
	return ctx.Err()
}

func mustJSONText(text string) []byte {
	raw, _ := json.Marshal(text)
	if len(raw) >= 2 {
		return raw[1 : len(raw)-1]
	}
	return raw
}
func ValidDurable(value *Value) bool {
	if value == nil {
		return false
	}
	var book DurableBook
	if json.Unmarshal(value.JSON(), &book) != nil || len(book.Dispatches) > 400 || len(book.Observations) > 400 || len(book.Children) > 32 || book.Dispatches == nil || book.Observations == nil || book.Children == nil {
		return false
	}
	scope := func(s DurableScope) bool {
		return validStorageID(s.Execution) && validStorageID(s.Role) && (s.Task == "" || validStorageID(s.Task))
	}
	seen := map[string]DurableDispatch{}
	for _, row := range book.Dispatches {
		if !validStorageID(row.ID) || !scope(row.Scope) || !validStorageID(row.Source) || !validStorageID(row.Tool) || !recordDigestPattern.MatchString(row.Fingerprint) || len(row.Request) > StorageRecordBytes || row.RequestDigest != durableDigest([]byte(row.Request)) || (row.State != "claimed" && row.State != "result" && row.State != "unknown") || (row.Kind != "model" && row.Kind != "agent" && row.Kind != "host" && row.Kind != "runtime") {
			return false
		}
		if _, duplicate := seen[row.ID]; duplicate {
			return false
		}
		expected, err := durableFingerprint(row.Kind, row.Scope, row.Source, row.Tool, []byte(row.Request))
		if err != nil || row.Fingerprint != expected {
			return false
		}
		seen[row.ID] = row
	}
	for _, row := range book.Observations {
		dispatch, ok := seen[row.DispatchID]
		if !ok || dispatch.State != "result" || dispatch.Scope != row.Scope || dispatch.Source != row.Source || !validStorageID(row.ID) || !validStorageID(row.CommitID) || (row.Kind != "operation" && row.Kind != "artifact-read" && row.Kind != "artifact-produced") {
			return false
		}
		if row.Artifact != nil && (!validStorageID(row.Artifact.ID) || !recordDigestPattern.MatchString(row.Artifact.Digest)) {
			return false
		}
	}
	for _, child := range book.Children {
		if !scope(child.Scope) || child.Checkpoint.ID != child.Scope.Execution {
			return false
		}
	}
	return true
}
func ReadStoredRunEvent(raw []byte) (StoredRunEnvelope, error) {
	if len(raw) > StorageRecordBytes {
		return StoredRunEnvelope{}, &StorageInputError{}
	}
	value, err := ParseJSON(raw)
	if err != nil || value.Get("version") == nil || value.Get("version").Num() != 1 || !hasOnly(value, "version", "scope", "event") || !isObject(value.Get("scope")) || !hasOnly(value.Get("scope"), "execution", "role", "task") {
		return StoredRunEnvelope{}, &StorageInputError{}
	}
	var out StoredRunEnvelope
	if err = json.Unmarshal(raw, &out); err != nil {
		return out, err
	}
	if out.Version != 1 || !validStorageID(out.Scope.Execution) || !validStorageID(out.Scope.Role) || out.Scope.Task != "" && !validStorageID(out.Scope.Task) {
		return StoredRunEnvelope{}, &StorageInputError{}
	}
	return out, nil
}

// legacyCheckpointSave retains acknowledged legacy checkpoint delivery without journal authority.
func legacyCheckpointSave(store RunStore, id string, head *RunHead) func(context.Context, contract.Checkpoint) error {
	var mu sync.Mutex
	revision := ""
	if head != nil {
		revision = head.Revision
	}
	var fatal error
	return func(ctx context.Context, state contract.Checkpoint) error {
		if state.Durable != nil {
			return &ContinuationNotRecoverable{}
		}
		raw, err := json.Marshal(state)
		if err != nil {
			return err
		}
		mu.Lock()
		defer mu.Unlock()
		if fatal != nil {
			return fatal
		}
		commitID := newStorageIdentity()
		receipt, err := store.Commit(ctx, RunCommit{RunID: id, ExpectedRevision: revision, CommitID: commitID, Checkpoint: raw, Events: [][]byte{}})
		if err == nil && (receipt.CommitID != commitID || !validStorageID(receipt.Revision) || receipt.Revision == revision || receipt.Cursor != "" && !validStorageID(receipt.Cursor)) {
			err = &StorageAcknowledgementInvalid{}
		}
		if err != nil {
			fatal = err
			return err
		}
		revision = receipt.Revision
		if err = ctx.Err(); err != nil {
			fatal = err
		}
		return err
	}
}

func planEventIdentity(event contract.RunEvent) any {
	if event.RepeatDetected != nil {
		return event.RepeatDetected
	}
	if event.RouteDecided != nil {
		return event.RouteDecided
	}
	if event.RouteWidened != nil {
		return event.RouteWidened
	}
	if event.PlanRevised != nil {
		return event.PlanRevised
	}
	if event.PlanDeparted != nil {
		return event.PlanDeparted
	}
	if event.PlanCompletionRefused != nil {
		return event.PlanCompletionRefused
	}
	if event.PlanDriftDecided != nil {
		return event.PlanDriftDecided
	}
	if event.ApprovalRequested != nil && event.ApprovalRequested.Purpose != nil && *event.ApprovalRequested.Purpose == "plan-drift" {
		return event.ApprovalRequested
	}
	return nil
}
func planEventExecution(event contract.RunEvent) string {
	if event.PlanRevised != nil {
		return event.PlanRevised.Execution
	}
	if event.PlanDeparted != nil {
		return event.PlanDeparted.Execution
	}
	if event.PlanCompletionRefused != nil {
		return event.PlanCompletionRefused.Execution
	}
	if event.PlanDriftDecided != nil {
		return event.PlanDriftDecided.Execution
	}
	if event.ApprovalRequested != nil && event.ApprovalRequested.Plan != nil {
		return event.ApprovalRequested.Plan.Execution
	}
	return ""
}

func (w *RunWriter) TaskStepsEnabled() bool { return w.taskSteps.Load() }
func taskCheckpointValue(state contract.Checkpoint) *Value {
	raw, _ := json.Marshal(state)
	return MustParseJSON(string(raw))
}
func (w *RunWriter) attachTaskScope(binding RunBinding, source taskDispatchSource, configuration *TaskStepEnvelope) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if binding.Writer != w || binding.Root && !w.rootBinding(binding) || !binding.Root && !w.taskSteps.Load() {
		return &ContinuationIdentityChanged{}
	}
	if binding.Root {
		var expected *TaskEnvelopeBinding
		if configuration != nil {
			if !validTaskStepEnvelope(*configuration) {
				return taskStepFail("invalid-execution-envelope")
			}
			state, err := source.snapshot()
			if err != nil {
				return err
			}
			if state.Unit != "provider-managed" {
				return taskStepFail("invalid-execution-envelope")
			}
			scope, _ := canonicalRaw(binding.Scope)
			expected = &TaskEnvelopeBinding{Run: w.ID, Execution: binding.Scope.Execution, ScopeDigest: durableDigest(scope), Configuration: *configuration}
		}
		var saved *TaskEnvelopeBinding
		if w.latest != nil {
			if value := taskCheckpointValue(*w.latest).Get("planState").Get("taskEnvelope"); value != nil {
				var err error
				saved, err = readTaskEnvelopeBinding(value)
				if err != nil {
					return err
				}
			}
		}
		retained := saved
		if retained == nil {
			retained = w.configuredTaskEnvelope
		}
		if (retained != nil || w.restoredTaskHead) && usageCanonical(retained) != usageCanonical(expected) {
			return &ContinuationIdentityChanged{}
		}
		w.configuredTaskEnvelope = expected
		// Seed only the original writer-owned pins before the first Plan candidate.
		// A pending candidate must inherit these facts at creation, not be rewritten.
		if expected != nil || w.nativeTaskCoordinator != nil {
			state, err := source.snapshot()
			if err != nil {
				return err
			}
			value := taskCheckpointValue(state)
			if !value.Get("planState").IsObject() {
				return &ContinuationIdentityChanged{}
			}
			if expected != nil {
				raw, _ := json.Marshal(expected)
				value.Get("planState").Set("taskEnvelope", MustParseJSON(string(raw)))
			}
			if w.nativeTaskCoordinator != nil {
				raw, _ := json.Marshal(w.nativeTaskCoordinator)
				value.Get("planState").Set("taskCoordinator", MustParseJSON(string(raw)))
			}
			var pinned contract.Checkpoint
			if err = json.Unmarshal(value.JSON(), &pinned); err != nil {
				return err
			}
			if err = source.accept(pinned); err != nil {
				return err
			}
		}
		w.taskRoot = binding.Scope.Execution
		w.taskSteps.Store(true)
		if w.latest != nil {
			if value := taskCheckpointValue(*w.latest).Get("planState").Get("taskSchedule"); value != nil {
				var saved TaskSchedule
				if json.Unmarshal(value.JSON(), &saved) != nil {
					return &ContinuationIdentityChanged{}
				}
				w.taskSchedule = &saved
				scope, _ := canonicalRaw(binding.Scope)
				if saved.Binding.Run != w.ID || saved.Binding.Execution != w.ID || saved.Binding.ScopeDigest != durableDigest(scope) {
					return &ContinuationIdentityChanged{}
				}
				for _, row := range saved.Rows {
					for _, claim := range row.Claims {
						found := false
						execution, task := w.ID, ""
						if row.Child != nil {
							execution, task = row.Child.Execution, row.Child.Task
						}
						for _, physical := range w.book.Dispatches {
							if physical.ID == claim.ID && physical.RequestDigest == claim.RequestDigest && physical.Scope.Execution == execution && physical.Scope.Task == task && (physical.Kind == "model" || physical.Kind == "agent") {
								found = true
								break
							}
						}
						if !found {
							return &ContinuationIdentityChanged{}
						}
					}
				}

			}
		}
	}
	if w.taskScopes == nil {
		w.taskScopes = map[string]taskDispatchSource{}
	}
	if len(w.taskScopes) >= 33 {
		if _, ok := w.taskScopes[binding.Scope.Execution]; !ok {
			return &ContinuationNotRecoverable{}
		}
	}
	w.taskScopes[binding.Scope.Execution] = source
	return nil
}
func (w *RunWriter) taskStateLocked() (contract.Checkpoint, error) {
	source, ok := w.taskScopes[w.taskRoot]
	if !ok {
		return contract.Checkpoint{}, &ContinuationIdentityChanged{}
	}
	var state contract.Checkpoint
	var err error
	if w.snapshot == nil && w.latest != nil {
		// The parent has returned and released its session. Coordinator events must
		// retain its acknowledged root until the next admitted invocation binds.
		state = *w.latest
	} else {
		state, err = source.snapshot()
	}
	if err != nil {
		return state, err
	}
	if w.Decorate != nil {
		return w.Decorate(state)
	}
	return state, nil
}
func (w *RunWriter) AttachNativeTaskEnvelope(binding RunBinding, source NativeTaskEnvelopeSource) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.rootBinding(binding) || source.Limit < 1 || source.Limit > 100 || source.ChildReserve < 1 || source.ChildReserve > source.Limit || source.Counts == nil || source.Reserve == nil {
		return &ContinuationIdentityChanged{}
	}
	scope, _ := canonicalRaw(binding.Scope)
	expected := &TaskCoordinatorBinding{Version: 1, Owner: "native-delegation", Run: w.ID, Execution: binding.Scope.Execution, ScopeDigest: durableDigest(scope), Limit: source.Limit, ChildReserve: source.ChildReserve}
	var retained *Value
	if w.latest != nil {
		retained = taskCheckpointValue(*w.latest).Get("planState").Get("taskCoordinator")
	}
	if retained != nil || w.restoredTaskHead {
		if retained == nil || CanonicalJSON(retained) != usageCanonical(expected) {
			return &ContinuationIdentityChanged{}
		}
	}
	if w.nativeTaskCoordinator != nil && usageCanonical(w.nativeTaskCoordinator) != usageCanonical(expected) {
		return &ContinuationIdentityChanged{}
	}
	w.nativeTaskCoordinator = expected
	w.nativeTaskEnvelope = &source
	return nil
}
func (w *RunWriter) taskRemainingLocked(root contract.Checkpoint, scope DurableScope) (int, bool, error) {
	if root.Unit == "model-requests" {
		reserve := 0
		if scope.Task != "" {
			reserve = 1
		}
		return int(root.Limit-root.Used) - reserve, true, nil
	}
	physical := map[string]int{}
	total := 0
	for _, row := range w.book.Dispatches {
		if row.Kind == "agent" {
			physical[row.Scope.Execution]++
			total++
		}
	}
	remaining, known := 0, false
	if source := w.nativeTaskEnvelope; source != nil {
		counts := source.Counts()
		counted := 0
		for execution, count := range counts {
			if count < 0 || count != physical[execution] {
				return 0, false, &ContinuationIdentityChanged{}
			}
			counted += count
		}
		for execution, count := range physical {
			if counts[execution] != count {
				return 0, false, &ContinuationIdentityChanged{}
			}
		}
		reserve := source.Reserve(scope)
		if reserve < 0 || reserve > source.Limit || w.nativeTaskCoordinator == nil || scope.Task == "" && reserve != 0 || scope.Task != "" && reserve != w.nativeTaskCoordinator.ChildReserve {
			return 0, false, &ContinuationIdentityChanged{}
		}
		remaining, known = source.Limit-counted-reserve, true
	}
	if configured := w.configuredTaskEnvelope; configured != nil {
		available := configured.Configuration.Limit - total
		if !known || available < remaining {
			remaining = available
		}
		known = true
	}
	return max(0, remaining), known, nil
}
func (w *RunWriter) prepareTaskPlanLocked(binding RunBinding, state contract.Checkpoint) (contract.Checkpoint, *TaskSchedule, error) {
	if !w.taskSteps.Load() {
		return state, nil, nil
	}
	if !binding.Root {
		return state, nil, nil
	}
	if binding.Scope.Execution != w.taskRoot {
		return state, nil, &ContinuationIdentityChanged{}
	}
	next := taskCheckpointValue(state)
	plan := next.Get("plan")
	if plan.Len() == 0 {
		return state, nil, nil
	}
	if next.Get("planState") == nil {
		return state, nil, &ContinuationIdentityChanged{}
	}
	if err := validateTaskPlan(plan, nil); err != nil {
		return state, nil, err
	}
	definitions, statuses, err := TaskDefinitions(plan)
	if err != nil {
		return state, nil, err
	}
	if err = RequireTaskProfiles(definitions, w.taskScopes[w.taskRoot].profiles()); err != nil {
		return state, nil, err
	}
	oldPlan := Array()
	if w.latest != nil {
		oldPlan = taskCheckpointValue(*w.latest).Get("plan")
	}
	oldDefinitions, oldStatuses, err := TaskDefinitions(oldPlan)
	if err != nil {
		return state, nil, err
	}
	root, err := w.taskStateLocked()
	if err != nil {
		return state, nil, err
	}
	scope, _ := canonicalRaw(binding.Scope)
	available, known, err := w.taskRemainingLocked(root, DurableScope{})
	if err != nil {
		return state, nil, err
	}
	if !known {
		for _, definition := range definitions {
			if definition.Budget != nil {
				return state, nil, taskStepFail("native-envelope-required")
			}
		}
	}
	if w.configuredTaskEnvelope != nil {
		raw, _ := json.Marshal(w.configuredTaskEnvelope)
		next.Get("planState").Set("taskEnvelope", MustParseJSON(string(raw)))
	}
	facts := TaskScheduleBinding{Run: w.ID, Execution: state.ID, PlanRevision: planText(next.Get("planState").Get("revision")), ScopeDigest: durableDigest(scope), Unit: state.Unit}
	schedule, err := ReconcileTaskSchedule(w.taskSchedule, oldDefinitions, oldStatuses, facts, definitions, statuses, available)
	if err != nil {
		return state, nil, err
	}
	raw, _ := json.Marshal(schedule)
	next.Get("planState").Set("taskSchedule", MustParseJSON(string(raw)))
	next.Set("version", Number(3))
	var prepared contract.Checkpoint
	if err = json.Unmarshal(next.JSON(), &prepared); err != nil {
		return state, nil, err
	}
	return prepared, &schedule, nil
}
func (w *RunWriter) claimTaskDispatchLocked(ctx context.Context, binding RunBinding, claim DurableDispatch) error {
	if !w.taskSteps.Load() || claim.Kind != "model" && claim.Kind != "agent" || claim.Kind == "model" && planText(MustParseJSON(claim.Request).Get("method")) == "GET" {
		return nil
	}
	root, err := w.taskStateLocked()
	if err != nil {
		return err
	}
	source, ok := w.taskScopes[binding.Scope.Execution]
	if !ok {
		return &ContinuationIdentityChanged{}
	}
	available, known, err := w.taskRemainingLocked(root, binding.Scope)
	if err != nil {
		return err
	}
	if known && available <= 0 {
		return ErrExecutionPaused
	}
	next := w.taskSchedule
	if next != nil {
		definitions, statuses, err := TaskDefinitions(taskCheckpointValue(root).Get("plan"))
		if err != nil {
			return err
		}
		selected := ""
		for _, row := range next.Rows {
			if binding.Scope.Task != "" && row.Child != nil && row.Child.Task == binding.Scope.Task || binding.Scope.Task == "" && statuses[row.ID] == "in_progress" {
				selected = row.ID
			}
		}
		if selected != "" {
			var allowance *int
			if known {
				allowance = &available
			}
			reserved, e := reserveTaskStepCandidate(*next, definitions, statuses, selected, claim.ID, claim.RequestDigest, allowance)
			if e != nil {
				var refusal *TaskStepRefused
				if errors.As(e, &refusal) && refusal.Reason == "step-budget-pause" {
					return ErrExecutionPaused
				}
				return e
			}
			next = &reserved
		} else {
			held := 0
			for _, row := range next.Rows {
				if statuses[row.ID] != "completed" && statuses[row.ID] != "not-needed" && row.Limit != nil {
					held += max(0, *row.Limit-len(row.Claims))
				}
			}
			if known && available <= held {
				return ErrExecutionPaused
			}
		}
	}
	if err = source.charge(ctx, claim.Kind, binding.Scope.Role, claim.Request); err != nil {
		return err
	}
	if !binding.Root {
		child, err := source.snapshot()
		if err != nil {
			return err
		}
		found := false
		for i, row := range w.book.Children {
			if row.Scope == binding.Scope {
				w.book.Children[i].Checkpoint = child
				found = true
				break
			}
		}
		if !found {
			w.book.Children = append(w.book.Children, DurableChild{Scope: binding.Scope, Checkpoint: child})
		}
	}
	w.taskSchedule = next
	return nil
}

// taskDelegationRegistration is source-created and bound to one exact writer/scope/schema.
// Neither a tool name nor a serialized caller marker can recreate it.
type taskDelegationRegistration struct {
	binding RunBinding
	schema  string
}

// RegisteredTaskDelegationTool is a package capability predicate, never a public marker grant.
func RegisteredTaskDelegationTool(tool HostTool) bool {
	return tool.taskDelegation != nil && tool.Name == "delegate_plan_steps" && tool.taskDelegation.schema == string(tool.InputSchema)
}
func NormalizeTaskDelegation(ctx context.Context, tool HostTool, binding *RunBinding, args json.RawMessage) (contract.DelegatedTaskRequest, bool, error) {
	held := tool.taskDelegation
	if held == nil {
		return contract.DelegatedTaskRequest{}, false, nil
	}
	if binding == nil || held.binding.Writer != binding.Writer || held.binding.Root != binding.Root || held.binding.Scope != binding.Scope || tool.Name != "delegate_plan_steps" || held.schema != string(tool.InputSchema) {
		return contract.DelegatedTaskRequest{}, true, &ContinuationIdentityChanged{}
	}
	request, err := binding.Writer.prepareTaskDelegation(ctx, *binding, args)
	return request, true, err
}

// TaskDelegationTool creates the package's registered action, with unchanged legacy handoff bytes.
func (w *RunWriter) TaskDelegationTool(binding RunBinding, execute func(context.Context, contract.DelegatedTaskRequest) (ToolResult, error)) (HostTool, error) {
	if !w.rootBinding(binding) {
		return HostTool{}, &ContinuationIdentityChanged{}
	}
	schema := json.RawMessage(`{"type":"object","properties":{"planRevision":{"type":"string","pattern":"^sha256:[a-f0-9]{64}$"},"steps":{"type":"array","minItems":1,"maxItems":3,"uniqueItems":true,"items":{"type":"string","pattern":"^step-[1-9][0-9]{0,15}$"}}},"required":["planRevision","steps"],"additionalProperties":false}`)
	tool := HostTool{Name: "delegate_plan_steps", Taxonomy: &ToolTaxonomy{Operation: "run", Effect: "read"}, Description: "Delegate one to three distinct ready package steps from the exact current plan revision. Existing child and response limits remain in force. Completion requires a separate complete_plan_step call with acknowledged child evidence.", Interaction: "delegation", InputSchema: schema, taskDelegation: &taskDelegationRegistration{binding: binding, schema: string(schema)}}
	tool.Execute = func(ctx context.Context, args json.RawMessage) (ToolResult, error) {
		request, _, err := NormalizeTaskDelegation(ctx, tool, &binding, args)
		if err != nil {
			return ToolResult{}, err
		}
		return execute(ctx, request)
	}
	return tool, nil
}
func (w *RunWriter) taskDelegationRequestLocked(root contract.Checkpoint, selected []string) (contract.DelegatedTaskRequest, error) {
	plan := taskCheckpointValue(root).Get("plan")
	definitions, statuses, err := TaskDefinitions(plan)
	if err != nil {
		return contract.DelegatedTaskRequest{}, err
	}
	ready, err := ReadyTaskSteps(definitions, statuses)
	if err != nil {
		return contract.DelegatedTaskRequest{}, err
	}
	if err = RequireTaskProfiles(definitions, w.taskScopes[w.taskRoot].profiles()); err != nil {
		return contract.DelegatedTaskRequest{}, err
	}
	request := contract.DelegatedTaskRequest{Tasks: []contract.DelegatedTaskTaskRequest{}}
	for _, name := range selected {
		var step *Value
		var row *TaskStepRow
		var definition *TaskStepDefinition
		for at := range definitions {
			if definitions[at].ID == name {
				definition = &definitions[at]
				step = plan.Items()[at]
				row = &w.taskSchedule.Rows[at]
				break
			}
		}
		if definition == nil || definition.Delegation == nil || row.Child != nil || !slices.Contains(ready, name) {
			return request, taskStepFail("child-not-admitted")
		}
		objective := planText(step.Get("step"))
		if detail := planText(step.Get("detail")); detail != "" {
			objective += "\n\n" + detail
		}
		if U16Length(objective) > 2000 {
			return request, taskStepFail("delegation-objective-too-large")
		}
		pin := *definition.Delegation
		request.Tasks = append(request.Tasks, contract.DelegatedTaskTaskRequest{Title: "Plan " + name, Objective: objective, Profile: pin.ID, Subagent: &pin})
	}
	return request, nil
}
func (w *RunWriter) prepareTaskDelegation(ctx context.Context, binding RunBinding, args json.RawMessage) (contract.DelegatedTaskRequest, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	empty := contract.DelegatedTaskRequest{}
	if w.fatal != nil {
		return empty, w.fatal
	}
	if w.dispatchClosed.Load() {
		return empty, failure.Cancelled
	}
	if !w.rootBinding(binding) || !w.taskSteps.Load() || w.taskSchedule == nil {
		return empty, taskStepFail("task-plan-required")
	}
	input, err := ParseJSON(args)
	if err != nil || !input.IsObject() || !hasOnly(input, "planRevision", "steps") || planText(input.Get("planRevision")) != w.taskSchedule.Binding.PlanRevision || !input.Get("steps").IsArray() || input.Get("steps").Len() < 1 || input.Get("steps").Len() > 3 {
		return empty, taskStepFail("invalid-delegation-selection")
	}
	if w.taskSchedule.Pending != nil {
		return empty, taskStepFail("delegation-outcome-unknown")
	}
	selected := []string{}
	seen := map[string]bool{}
	for _, step := range input.Get("steps").Items() {
		name := planText(step)
		if !step.IsString() || seen[name] {
			return empty, taskStepFail("invalid-delegation-selection")
		}
		seen[name] = true
		selected = append(selected, name)
	}
	root, err := w.taskStateLocked()
	if err != nil {
		return empty, err
	}
	request, err := w.taskDelegationRequestLocked(root, selected)
	if err != nil {
		return empty, err
	}
	raw, _ := canonicalRaw(request)
	next := *w.taskSchedule
	next.Pending = &TaskPendingDelegation{PlanRevision: next.Binding.PlanRevision, RequestDigest: durableDigest(raw), Steps: selected}
	w.taskSchedule = &next
	event := contract.RunEvent{ExecutionCheckpoint: &contract.RunEventExecutionCheckpoint{Type: "execution_checkpoint", Checkpoint: root}}
	if _, err = w.saveLocked(ctx, binding, &root, &event, nil, nil); err != nil {
		return empty, err
	}
	w.liveTaskPending = next.Pending.RequestDigest
	if err = w.taskScopes[w.taskRoot].accept(*w.latest); err != nil {
		w.fatal = err
		return empty, err
	}
	return request, nil
}

// BindTaskDelegation acknowledges real allocated child IDs before publication or provider work.
func (w *RunWriter) BindTaskDelegation(ctx context.Context, binding RunBinding, request contract.DelegatedTaskRequest, tasks []*contract.DelegatedTask) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.fatal != nil {
		return w.fatal
	}
	if !w.taskSteps.Load() {
		return nil
	}
	if w.dispatchClosed.Load() {
		return failure.Cancelled
	}
	if w.taskSchedule == nil || w.taskSchedule.Pending == nil {
		if w.latest != nil && w.latest.Handoff != nil && w.latest.Handoff.State == "children" && usageCanonical(w.latest.Handoff.Request) == usageCanonical(request) {
			ids := []string{}
			for _, task := range w.latest.Handoff.Tasks {
				ids = append(ids, task.ID)
			}
			actual := []string{}
			for _, task := range tasks {
				actual = append(actual, task.ID)
			}
			if usageCanonical(ids) != usageCanonical(actual) {
				return &ContinuationIdentityChanged{}
			}
		}
		bound := false
		if w.taskSchedule != nil {
			for _, task := range tasks {
				for _, row := range w.taskSchedule.Rows {
					bound = bound || row.Child != nil && row.Child.Task == task.ID
				}
			}
		}
		if bound {
			for at, task := range tasks {
				var child *TaskChildBinding
				for _, row := range w.taskSchedule.Rows {
					if row.Child != nil && row.Child.Task == task.ID {
						child = row.Child
						break
					}
				}
				if child == nil || at >= len(request.Tasks) || usageCanonical(child.Profile) != usageCanonical(task.Subagent) || task.Profile != request.Tasks[at].Profile || task.Objective != request.Tasks[at].Objective || task.Title != request.Tasks[at].Title {
					return &ContinuationIdentityChanged{}
				}
				if task.State == "queued" {
					for _, claim := range w.book.Dispatches {
						if claim.Scope.Execution == task.ID && claim.Scope.Task == task.ID && (claim.Kind == "model" || claim.Kind == "agent") {
							return &HostOutcomeUnknown{}
						}
					}
				}
			}
		}
		return nil
	}
	selected := w.taskSchedule.Pending
	raw, _ := canonicalRaw(request)
	requestDigest := durableDigest(raw)
	handoff := ""
	if w.latest != nil && w.latest.Handoff != nil {
		handoff = usageCanonical(w.latest.Handoff.Request)
	} else if w.book.PendingHandoff != nil {
		handoff = usageCanonical(w.book.PendingHandoff.Request)
	}
	if !w.rootBinding(binding) || selected.PlanRevision != w.taskSchedule.Binding.PlanRevision || selected.RequestDigest != requestDigest || w.liveTaskPending != requestDigest && handoff != string(raw) || len(tasks) != len(selected.Steps) {
		return taskStepFail("delegation-identity-changed")
	}
	root, err := w.taskStateLocked()
	if err != nil {
		return err
	}
	derived, err := w.taskDelegationRequestLocked(root, selected.Steps)
	if err != nil {
		return err
	}
	if usageCanonical(derived) != string(raw) {
		return taskStepFail("delegation-identity-changed")
	}
	children := []TaskChildCandidate{}
	for at, task := range tasks {
		expected := request.Tasks[at]
		if task.State != "queued" || task.Title != expected.Title || task.Objective != expected.Objective || task.Profile != expected.Profile || usageCanonical(task.Subagent) != usageCanonical(expected.Subagent) || task.Resumes != nil || task.Reused != nil {
			return taskStepFail("delegation-identity-changed")
		}
		children = append(children, TaskChildCandidate{Step: selected.Steps[at], Task: task.ID, Profile: *task.Subagent})
	}
	definitions, statuses, err := TaskDefinitions(taskCheckpointValue(root).Get("plan"))
	if err != nil {
		return err
	}
	current := *w.taskSchedule
	current.Pending = nil
	next, err := BindTaskChildren(current, definitions, statuses, children)
	if err != nil {
		return err
	}
	w.taskSchedule = &next
	event := contract.RunEvent{ExecutionCheckpoint: &contract.RunEventExecutionCheckpoint{Type: "execution_checkpoint", Checkpoint: root}}
	if _, err = w.saveLocked(ctx, binding, &root, &event, nil, nil); err != nil {
		return err
	}
	w.liveTaskPending = ""
	if err = w.taskScopes[w.taskRoot].accept(*w.latest); err != nil {
		w.fatal = err
	}
	return err
}

func successfulTaskChild(state contract.Checkpoint, child TaskChildBinding, task contract.DelegatedTask) (string, bool) {
	if task.ID != child.Task || task.State != "complete" || task.Failure != nil || task.Problem != nil || task.Partial != nil && *task.Partial || task.Reused != nil || task.Report == nil || usageCanonical(task.Subagent) != usageCanonical(child.Profile) {
		return "", false
	}
	var book DurableBook
	raw, _ := json.Marshal(state.Durable)
	if json.Unmarshal(raw, &book) != nil {
		return "", false
	}
	found := 0
	digest := ""
	for _, row := range book.Children {
		if row.Scope.Execution == child.Execution && row.Scope.Task == child.Task {
			if row.Checkpoint.Status != "complete" {
				return "", false
			}
			found++
			if row.Checkpoint.Digest == nil {
				return "", false
			}
			digest = *row.Checkpoint.Digest
		}
	}
	physical, successful := 0, 0
	for _, row := range book.Dispatches {
		if row.Scope.Execution == child.Execution && row.Scope.Task == child.Task && (row.Kind == "model" || row.Kind == "agent") {
			physical++
			if row.Failure == nil {
				successful++
			}
			if row.State != "result" || row.Failure != nil && row.Failure.Outcome != "refused" {
				return "", false
			}
		}
	}
	return digest, found == 1 && physical > 0 && successful > 0
}

// CommitTaskResult is called only by the coordinator's actual terminal task publisher.
func (w *RunWriter) CommitTaskResult(ctx context.Context, binding RunBinding, event contract.RunEvent) (contract.RunEvent, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.fatal != nil {
		return contract.RunEvent{}, w.fatal
	}
	if event.DelegatedTask == nil {
		return contract.RunEvent{}, &ContinuationIdentityChanged{}
	}
	if w.taskSchedule != nil {
		for _, row := range w.taskSchedule.Rows {
			if row.Child != nil && row.Child.Task == event.DelegatedTask.Task.ID {
				root, err := w.taskStateLocked()
				if err != nil {
					return contract.RunEvent{}, err
				}
				value := taskCheckpointValue(root)
				raw, _ := json.Marshal(w.book)
				value.Set("durable", MustParseJSON(string(raw)))
				if err = json.Unmarshal(value.JSON(), &root); err != nil {
					return contract.RunEvent{}, err
				}
				if _, ok := successfulTaskChild(root, *row.Child, event.DelegatedTask.Task); ok {
					w.taskResultStep = row.ID
					defer func() { w.taskResultStep = "" }()
				}
				break
			}
		}
	}
	return w.saveLocked(ctx, binding, nil, &event, nil, nil)
}
func (w *RunWriter) TaskCompletionProof(binding RunBinding) []TaskCompletionEvidence {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := []TaskCompletionEvidence{}
	if !w.rootBinding(binding) || w.taskSchedule == nil {
		return out
	}
	for _, row := range w.taskSchedule.Rows {
		if proof, ok := w.taskResults[row.ID]; ok {
			out = append(out, proof)
		}
	}
	return out
}
func (w *RunWriter) restoreTaskEvidence(ctx context.Context, binding RunBinding) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	invalid := func() error { return &ContinuationIdentityChanged{} }
	if !w.rootBinding(binding) || !w.taskSteps.Load() {
		return invalid()
	}
	saved := []TaskStepRow{}
	if w.taskSchedule != nil {
		for _, row := range w.taskSchedule.Rows {
			if row.Child != nil && row.Child.Result != nil {
				saved = append(saved, row)
			}
		}
	}
	if len(saved) == 0 {
		return nil
	}
	store, ok := w.Store.(HistoryRunStore)
	if !ok || store.HistoryVersion() != 1 {
		return nil
	}
	verified, err := verifyTaskEvidence(ctx, store, w.ID, w.revision, w.acknowledgedCheckpoint, saved, binding.Scope)
	if err != nil {
		return err
	}
	if w.taskResults == nil {
		w.taskResults = map[string]TaskCompletionEvidence{}
	}
	for step, result := range verified {
		w.taskResults[step] = result
	}
	return nil
}

// verifyTaskEvidence is the writer's history check of delegated evidence (#183), for a reader that holds no
// writer: #230 (M16) re-verifies an objective's delegated step by the same reading. Every saved child result must
// be proven by the exact original history transaction that recorded it, and the history must be the run's whole,
// unchanged, at the head given. Anything else is ContinuationIdentityChanged.
func verifyTaskEvidence(ctx context.Context, store HistoryRunStore, run, revision string, acknowledged []byte, saved []TaskStepRow, scope DurableScope) (map[string]TaskCompletionEvidence, error) {
	invalid := func() error { return &ContinuationIdentityChanged{} }
	readHead := func() (*RunHistoryHead, error) {
		head, err := store.ReadHistoryHead(ctx, run)
		if err != nil {
			return nil, err
		}
		if head == nil {
			return nil, invalid()
		}
		raw, _ := historyJSONBytes(head)
		checked, err := ReadRunHistoryHead(raw)
		return &checked, err
	}
	head, err := readHead()
	if err != nil {
		return nil, err
	}
	expected := HistoryObjectRef{Digest: HistoryBytesDigest(acknowledged), Bytes: len(acknowledged)}
	if head.RunID != run || head.ExecutionRevision != revision || head.Checkpoint == nil || *head.Checkpoint != expected {
		return nil, invalid()
	}
	total, sequence, eventCount := 0, 0, 0
	previous := ""
	verified := map[string]TaskCompletionEvidence{}
	object := func(ref HistoryObjectRef) ([]byte, error) {
		total += ref.Bytes
		if !validHistoryRef(ref) || total > HistoryTotalBytes {
			return nil, invalid()
		}
		page, err := store.ReadHistoryObject(ctx, HistoryResource{Kind: "run", ID: run}, head.Revision, ref.Digest, 0, ref.Bytes)
		if err != nil {
			return nil, err
		}
		if page.Digest != ref.Digest || page.Offset != 0 || page.Total != ref.Bytes || len(page.Value) != ref.Bytes || page.NextOffset != ref.Bytes || page.HasMore || HistoryBytesDigest(page.Value) != ref.Digest {
			return nil, invalid()
		}
		return page.Value, nil
	}
	for sequence < head.CommitCount {
		if sequence >= HistoryMaxRows {
			return nil, invalid()
		}
		page, err := store.ReadHistoryPage(ctx, run, head.Revision, sequence, 100)
		if err != nil {
			return nil, err
		}
		if page.Revision != head.Revision || page.AfterSequence != sequence || len(page.Items) == 0 || len(page.Items) > 100 || page.Total != head.CommitCount || page.NextSequence != sequence+len(page.Items) || page.HasMore != (page.NextSequence < head.CommitCount) {
			return nil, invalid()
		}
		for _, input := range page.Items {
			raw, _ := historyJSONBytes(input)
			transaction, err := ReadRunHistoryEntry(raw)
			if err != nil {
				return nil, err
			}
			sequence++
			prior := ""
			if transaction.PreviousDigest != nil {
				prior = *transaction.PreviousDigest
			}
			if transaction.Sequence != sequence || prior != previous || transaction.EventStart != eventCount+1 {
				return nil, invalid()
			}
			eventCount += transaction.EventCount
			if eventCount > HistoryMaxEvents {
				return nil, invalid()
			}
			previous = transaction.Digest
			for _, row := range saved {
				child, result := row.Child, row.Child.Result
				if transaction.CommitID != result.CommitID {
					continue
				}
				if _, found := verified[row.ID]; found || transaction.Checkpoint == nil {
					return nil, invalid()
				}
				raw, err := object(*transaction.Checkpoint)
				if err != nil {
					return nil, err
				}
				value, err := ParseJSON(raw)
				if err != nil {
					return nil, err
				}
				state, err := ReadExecution(value)
				if err != nil {
					return nil, err
				}
				if state == nil || state.ID != run {
					return nil, invalid()
				}
				var historical *Value
				for _, candidate := range value.Get("planState").Get("taskSchedule").Get("rows").Items() {
					if planText(candidate.Get("id")) == row.ID {
						historical = candidate.Get("child")
						break
					}
				}
				if CanonicalJSON(historical) != usageCanonical(child) {
					return nil, invalid()
				}
				matched := false
				for _, ref := range transaction.Events {
					raw, err := object(ref)
					if err != nil {
						return nil, err
					}
					if HistoryBytesDigest(raw) != result.EventDigest {
						continue
					}
					envelope, err := ReadStoredRunEvent(raw)
					if err != nil {
						return nil, err
					}
					event := envelope.Event
					if matched || envelope.Scope != scope || event.DelegatedTask == nil {
						return nil, invalid()
					}
					taskRaw, _ := canonicalRaw(event.DelegatedTask.Task)
					digest, ok := successfulTaskChild(*state, *child, event.DelegatedTask.Task)
					if durableDigest(taskRaw) != result.ResultDigest || !ok || digest != result.CheckpointDigest {
						return nil, invalid()
					}
					matched = true
				}
				if !matched {
					return nil, invalid()
				}
				verified[row.ID] = TaskCompletionEvidence{Step: row.ID, Task: child.Task, PlanRevision: child.PlanRevision, TaskChildResult: *result}
			}
		}
	}
	again, err := readHead()
	if err != nil {
		return nil, err
	}
	identity := func(head *RunHistoryHead) string {
		return usageCanonical([]any{head.RunID, head.ExecutionRevision, head.CommitCount, head.EventCount, head.Root, head.Checkpoint})
	}
	if len(verified) != len(saved) || previous != head.Root || eventCount != head.EventCount || identity(again) != identity(head) {
		return nil, invalid()
	}
	return verified, nil
}
