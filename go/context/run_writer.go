package runcontext

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"strings"
	"sync"
	"time"

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
	ID            string                `json:"id"`
	Scope         DurableScope          `json:"scope"`
	Kind          string                `json:"kind"`
	Source        string                `json:"source"`
	Tool          string                `json:"tool"`
	Fingerprint   string                `json:"fingerprint"`
	Request       string                `json:"request"`
	RequestDigest string                `json:"requestDigest"`
	State         string                `json:"state"`
	Read          bool                  `json:"read"`
	Result        json.RawMessage       `json:"result,omitempty"`
	Failure       *contract.Failure     `json:"failure,omitempty"`
	Artifacts     []ArtifactObservation `json:"artifacts,omitempty"`
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
	Store  RunStore
	ID     string
	Resume bool
}
type RunBinding struct {
	Writer *RunWriter
	Scope  DurableScope
	Root   bool
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

func (*ContinuationIdentityChanged) Error() string {
	return "ContinuationIdentityChanged: the admitted continuation identity differs."
}
func (e *ContinuationIdentityChanged) Unwrap() error {
	return storageNamedFailure("stale-state", "context", "not-started", e.Error())
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
type RunWriter struct {
	mu        sync.Mutex
	Store     RunStore
	ID        string
	revision  string
	latest    *contract.Checkpoint
	book      DurableBook
	recovered map[string]bool
	models    map[*modelCompletion]bool
	fatal     error
	snapshot  func() (contract.Checkpoint, error)
	policy    RecordsPolicy
	guard     func() RecordGuard
	Decorate  func(contract.Checkpoint) (contract.Checkpoint, error)
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
	w := &RunWriter{Store: store, ID: id, recovered: map[string]bool{}, models: map[*modelCompletion]bool{}, book: DurableBook{Dispatches: []DurableDispatch{}, Observations: []DurableObservation{}, Children: []DurableChild{}}}
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
	}
	for i, row := range w.book.Dispatches {
		w.recovered[row.ID] = true
		if row.State == "claimed" {
			w.book.Dispatches[i].State = "unknown"
		}
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
func (w *RunWriter) Failed() bool { w.mu.Lock(); defer w.mu.Unlock(); return w.fatal != nil }
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
func (w *RunWriter) saveLocked(ctx context.Context, binding RunBinding, state *contract.Checkpoint, event *contract.RunEvent, observed *DurableDispatch, artifacts []ArtifactObservation) (contract.RunEvent, error) {
	if w.fatal != nil {
		return contract.RunEvent{}, w.fatal
	}
	fail := func(err error) (contract.RunEvent, error) { w.fatal = err; return contract.RunEvent{}, err }
	if err := ctx.Err(); err != nil {
		return fail(err)
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
	if w.book.PendingHandoff != nil {
		value.Set("status", String("running"))
	}
	book, err := canonicalRaw(w.book)
	if err != nil {
		return fail(err)
	}
	held, err := ParseJSON(book)
	if err != nil {
		return fail(err)
	}
	value.Set("durable", held)
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
		encoded, e := canonicalRaw(StoredRunEnvelope{Version: 1, Scope: binding.Scope, Event: output})
		if e != nil {
			return fail(e)
		}
		events = append(events, encoded)
	}
	receipt, err := w.Store.Commit(ctx, RunCommit{RunID: w.ID, ExpectedRevision: w.revision, CommitID: commitID, Checkpoint: []byte(CanonicalJSON(written)), Events: events})
	if err != nil {
		return fail(err)
	}
	if receipt.CommitID != commitID || !validStorageID(receipt.Revision) || receipt.Revision == w.revision || receipt.Cursor != "" && !validStorageID(receipt.Cursor) || event != nil && receipt.Cursor == "" {
		return fail(&StorageAcknowledgementInvalid{})
	}
	w.revision = receipt.Revision
	w.latest = &checkpoint
	if err = ctx.Err(); err != nil {
		return fail(err)
	}
	return output, nil
}
func (w *RunWriter) Save(ctx context.Context, binding RunBinding, state contract.Checkpoint) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, err := w.saveLocked(ctx, binding, &state, nil, nil, nil)
	return err
}
func (w *RunWriter) Event(ctx context.Context, binding RunBinding, event contract.RunEvent) (contract.RunEvent, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
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
			replay := true
			if kind == "agent" && w.latest != nil && w.latest.Handoff != nil {
				handoff := w.latest.Handoff
				replay = handoff.State == "continued" && handoff.Continuation != nil && strings.Contains(row.Request, string(mustJSONText(*handoff.Continuation)))
			}
			if replay {
				if kind == "model" && !row.Read {
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
			if kind != "agent" || w.latest == nil || w.latest.Handoff == nil {
				return true
			}
			h := w.latest.Handoff
			return h.State == "continued" && h.Continuation != nil && strings.Contains(row.Request, string(mustJSONText(*h.Continuation)))
		}
	}
	return false
}

// InvokeDurable is used only AFTER existing current offer/source/authority checks. Its execution
// callback rechecks host authority after claim acknowledgement. Generic recovered action results
// are isolated by logical execution/role/task/source/tool/args, never shared by siblings.
func InvokeDurable[T any](ctx context.Context, binding *RunBinding, kind, source, tool string, request []byte, read bool, execute func([]byte) (T, error), successful func(T) bool, artifacts func(T) []ArtifactObservation) (T, error) {
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
	w.mu.Lock()
	if w.fatal != nil {
		err := w.fatal
		w.mu.Unlock()
		return zero, err
	}
	request, prepareErr := w.preparedLocked(binding, kind, source, tool, request, true)
	if prepareErr != nil {
		w.mu.Unlock()
		return zero, prepareErr
	}
	parsed, _ := ParseJSON(request)
	scope, _ := canonicalRaw(binding.Scope)
	fingerprint := durableDigest([]byte(CanonicalJSON(ObjectOf("kind", String(kind), "scope", MustParseJSON(string(scope)), "source", String(source), "tool", String(tool), "request", parsed))))
	if !read {
		for _, row := range w.book.Dispatches {
			if !row.Read && row.Kind == kind && row.Fingerprint == fingerprint {
				w.mu.Unlock()
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
	}
	if len(w.book.Dispatches) >= 400 {
		w.mu.Unlock()
		return zero, &ContinuationNotRecoverable{}
	}
	claim := DurableDispatch{ID: newStorageIdentity(), Scope: binding.Scope, Kind: kind, Source: source, Tool: tool, Fingerprint: fingerprint, Request: string(request), RequestDigest: durableDigest(request), State: "claimed", Read: read}
	w.book.Dispatches = append(w.book.Dispatches, claim)
	_, err := w.saveLocked(ctx, *binding, nil, nil, nil, nil)
	w.mu.Unlock()
	if err != nil {
		return zero, err
	}
	if err = ctx.Err(); err != nil {
		return zero, err
	}
	value, runErr := execute(request)
	w.mu.Lock()
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
				held := failure.Normalize(runErr, "tool", time.Now())
				updated.Failure = &held
			} else {
				updated.State = "result"
				if kind == "host" || kind == "runtime" {
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
	if _, err = w.saveLocked(ctx, *binding, nil, nil, observed, observations); err != nil {
		// The operation already ran. Keep the writer stopped with the same uncertainty
		// returned to the caller, so later event delivery cannot mask it with the raw cause.
		w.fatal = &StorageOutcomeUnknown{cause: err}
		return zero, w.fatal
	}
	if kind == "model" && runErr != nil {
		w.fatal = runErr
	}
	return value, runErr
}

// PrepareDurableInput loads only for an explicit host start/resume. It never resumes on GET/reload.
func PrepareDurableInput(ctx context.Context, input *Input) (*Input, error) {
	if input == nil || input.Persistence != nil {
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
	if copy.RunStore != nil && copy.Checkpoint != nil {
		return nil, &ContinuationIdentityChanged{}
	}
	id := newStorageIdentity()
	var store RunStore
	var head *RunHead
	var err error
	if copy.Execution != nil {
		id = copy.Execution.ID
	}
	if copy.RunStore != nil {
		id = copy.RunStore.ID
		store = copy.RunStore.Store
		if !validStorageID(id) || store == nil {
			return nil, &ContinuationIdentityChanged{}
		}
		head, err = store.Load(ctx, id)
		if err != nil {
			return nil, err
		}
		if (head != nil) != copy.RunStore.Resume {
			return nil, &StorageConflict{}
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
	writer, err := NewRunWriter(store, id, head, copy.Execution)
	if err != nil {
		return nil, err
	}
	binding := &RunBinding{Writer: writer, Scope: DurableScope{Execution: id, Role: "author"}, Root: true}
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
	return InvokeDurable(ctx, c.Persistence(role), "host", source, tool.Name, args, ReadingTool(&tool), func(raw []byte) (ToolResult, error) {
		if c.session.Context != nil && c.session.Context.Authorize != nil {
			if err := c.session.Context.Authorize(ctx); err != nil {
				return ToolResult{}, err
			}
		}
		return execute(ctx, raw)
	}, func(result ToolResult) bool { return !result.Err() }, func(result ToolResult) []ArtifactObservation {
		if tool.Artifacts != nil {
			return tool.Artifacts(result, args)
		}
		return nil
	})
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
		parsed, err := ParseJSON([]byte(row.Request))
		if err != nil {
			return false
		}
		scoped, _ := canonicalRaw(row.Scope)
		expected := durableDigest([]byte(CanonicalJSON(ObjectOf("kind", String(row.Kind), "scope", MustParseJSON(string(scoped)), "source", String(row.Source), "tool", String(row.Tool), "request", parsed))))
		if row.Fingerprint != expected {
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
