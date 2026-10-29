package runcontext

// RunStore is an injected resource-scoped port, not a backend registry or an authorization
// capability. Reads are observations. A receipt proves a past storage transaction, never current
// authority, external success or permission to dispatch after an uncertain acknowledgement.
import (
	"bytes"
	"context"
	"crypto/rand"
	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const StoragePageLimit = 100
const StorageRecordBytes = 4 * 1024 * 1024
const StorageTransactionBytes = 8 * 1024 * 1024
const StorageEventLimit = 256
const RunStoreHost = "host"
const RunStoreMemory = "memory"
const RunStoreSingleWriterCallback = "single-writer-callback"

type StorageInputError struct{}

func (*StorageInputError) Error() string {
	return "The storage request is invalid or exceeds its bounds."
}

type StorageConflict struct{ ExpectedRevision, ActualRevision string }

func (*StorageConflict) Error() string { return "The run changed; no storage write was applied." }

type StorageCommitMismatch struct{}

func (*StorageCommitMismatch) Error() string {
	return "This commit identity already names different content."
}

type StorageCursorInvalid struct{}

func (*StorageCursorInvalid) Error() string {
	return "The storage cursor is invalid for this resource."
}

func (e *StorageConflict) Unwrap() error {
	f := failure.New("stale-state", "storage", "not-started")
	detail := "StorageConflict: " + e.Error()
	f.Detail = &detail
	return failure.NewError(f)
}
func (e *StorageCommitMismatch) Unwrap() error {
	f := failure.New("storage", "storage", "not-started")
	detail := "StorageCommitMismatch: " + e.Error()
	f.Detail = &detail
	return failure.NewError(f)
}

// Empty ExpectedRevision means create only if absent. Nil Checkpoint leaves it unchanged.
// Record validation, guarding and canonicalization happen in the Agent writer, before the store.
type RunCommit struct {
	RunID, ExpectedRevision, CommitID string
	Checkpoint                        []byte
	Events                            [][]byte
}
type RunReceipt struct{ Revision, Cursor, CommitID string }
type RunHead struct {
	RunReceipt
	RunID      string
	Checkpoint []byte
}
type StoredRunEvent struct {
	Cursor string
	Bytes  []byte
}
type RunEventPage struct {
	Items      []StoredRunEvent
	NextCursor string
	HasMore    bool
}
type RunStore interface {
	Durability() string
	Load(context.Context, string) (*RunHead, error)
	Commit(context.Context, RunCommit) (RunReceipt, error)
	ReadEvents(context.Context, string, string, int) (RunEventPage, error)
}

// Hooks inject faults for conformance tests. They must not call the same store recursively.
type MemoryRunStoreOptions struct {
	BeforeCommit, AfterCommit func() error
	Now                       func() time.Time
}
type heldRunCommit struct {
	input          RunCommit
	receipt        RunReceipt
	historyInput   string
	historyReceipt RunHistoryReceipt
}
type heldRun struct {
	head    RunHead
	events  []StoredRunEvent
	commits map[string]heldRunCommit
}
type MemoryRunStore struct {
	mu          sync.Mutex
	runs        map[string]heldRun
	options     MemoryRunStoreOptions
	acknowledge func(context.Context, []byte) error
	durability  string
	history     *memoryHistoryJournal
	leases      *LeaseTable
}

func NewMemoryRunStore(options MemoryRunStoreOptions) *MemoryRunStore {
	return &MemoryRunStore{runs: map[string]heldRun{}, options: options, durability: RunStoreMemory, history: newMemoryHistoryJournal(options.Now), leases: NewLeaseTable(options.Now)}
}
func (m *MemoryRunStore) Durability() string { return m.durability }
func validStorageID(id string) bool {
	if len(id) == 0 || len(id) > 256 || !utf8.ValidString(id) {
		return false
	}
	for _, r := range id {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func copyStorageBytes(v []byte) ([]byte, error) {
	if len(v) == 0 || len(v) > StorageRecordBytes || !utf8.Valid(v) {
		return nil, &StorageInputError{}
	}
	return bytes.Clone(v), nil
}
func freezeRunCommit(input RunCommit) (RunCommit, error) {
	if !validStorageID(input.RunID) || !validStorageID(input.CommitID) || input.ExpectedRevision != "" && !validStorageID(input.ExpectedRevision) || len(input.Events) > StorageEventLimit || input.Checkpoint == nil && len(input.Events) == 0 {
		return RunCommit{}, &StorageInputError{}
	}
	out := input
	var err error
	total := 0
	if input.Checkpoint != nil {
		out.Checkpoint, err = copyStorageBytes(input.Checkpoint)
		if err != nil {
			return RunCommit{}, err
		}
		total = len(out.Checkpoint)
	}
	out.Events = make([][]byte, len(input.Events))
	for i, event := range input.Events {
		out.Events[i], err = copyStorageBytes(event)
		if err != nil {
			return RunCommit{}, err
		}
		total += len(event)
		if total > StorageTransactionBytes {
			return RunCommit{}, &StorageInputError{}
		}
	}
	return out, nil
}
func identicalRunCommit(a, b RunCommit) bool {
	if a.ExpectedRevision != b.ExpectedRevision || (a.Checkpoint == nil) != (b.Checkpoint == nil) || !bytes.Equal(a.Checkpoint, b.Checkpoint) || len(a.Events) != len(b.Events) {
		return false
	}
	for i := range a.Events {
		if !bytes.Equal(a.Events[i], b.Events[i]) {
			return false
		}
	}
	return true
}
func (m *MemoryRunStore) Load(ctx context.Context, id string) (*RunHead, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !validStorageID(id) {
		return nil, &StorageInputError{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	held, ok := m.runs[id]
	if !ok {
		return nil, nil
	}
	head := held.head
	head.Checkpoint = bytes.Clone(head.Checkpoint)
	return &head, nil
}
func (m *MemoryRunStore) Commit(ctx context.Context, input RunCommit) (RunReceipt, error) {
	receipt, _, err := m.commitTransaction(ctx, input, nil, nil)
	return receipt, err
}
func (m *MemoryRunStore) commitTransaction(ctx context.Context, input RunCommit, extra *RunHistoryCommit, fence *LeaseFence) (RunReceipt, RunHistoryReceipt, error) {
	if err := ctx.Err(); err != nil {
		return RunReceipt{}, RunHistoryReceipt{}, err
	}
	frozen, err := freezeRunCommit(input)
	if err != nil {
		return RunReceipt{}, RunHistoryReceipt{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return RunReceipt{}, RunHistoryReceipt{}, err
	}
	// #223: the fence is checked in the same locked transaction as the revision, and again at publication.
	if err := m.checkCommitFence(frozen.RunID, fence); err != nil {
		return RunReceipt{}, RunHistoryReceipt{}, err
	}
	held := m.runs[frozen.RunID]
	if prior, ok := held.commits[frozen.CommitID]; ok {
		if !identicalRunCommit(prior.input, frozen) || prior.historyInput != historyInputText(extra) {
			return RunReceipt{}, RunHistoryReceipt{}, &StorageCommitMismatch{}
		}
		return prior.receipt, historyClone(prior.historyReceipt), nil
	}
	if held.head.Revision != frozen.ExpectedRevision {
		return RunReceipt{}, RunHistoryReceipt{}, &StorageConflict{ExpectedRevision: frozen.ExpectedRevision, ActualRevision: held.head.Revision}
	}
	receipt := RunReceipt{Revision: rand.Text(), CommitID: frozen.CommitID}
	checkpoint := frozen.Checkpoint
	if checkpoint == nil {
		checkpoint = held.head.Checkpoint
	}
	prepared, err := m.history.prepare(frozen, receipt, checkpoint, extra)
	if err != nil {
		return RunReceipt{}, RunHistoryReceipt{}, err
	}
	if m.options.BeforeCommit != nil {
		if err = m.options.BeforeCommit(); err != nil {
			return RunReceipt{}, RunHistoryReceipt{}, err
		}
	}
	if err = ctx.Err(); err != nil {
		return RunReceipt{}, RunHistoryReceipt{}, err
	}
	if m.acknowledge != nil && frozen.Checkpoint != nil {
		if err = m.acknowledge(ctx, bytes.Clone(frozen.Checkpoint)); err != nil {
			return RunReceipt{}, RunHistoryReceipt{}, err
		}
	}
	if err = ctx.Err(); err != nil {
		return RunReceipt{}, RunHistoryReceipt{}, err
	}
	events := append([]StoredRunEvent{}, held.events...)
	cursor := held.head.Cursor
	for _, raw := range frozen.Events {
		cursor = rand.Text()
		events = append(events, StoredRunEvent{Cursor: cursor, Bytes: raw})
	}
	receipt.Cursor = cursor
	historyReceipt := RunHistoryReceipt{RunReceipt: receipt, History: prepared.view.head}
	commits := make(map[string]heldRunCommit, len(held.commits)+1)
	for id, commit := range held.commits {
		commits[id] = commit
	}
	commits[frozen.CommitID] = heldRunCommit{input: frozen, receipt: receipt, historyInput: historyInputText(extra), historyReceipt: historyReceipt}
	if err := m.checkCommitFence(frozen.RunID, fence); err != nil {
		return RunReceipt{}, RunHistoryReceipt{}, err
	}
	// All validation/acknowledgement precedes the single publication under the store lock.
	m.runs[frozen.RunID] = heldRun{head: RunHead{RunReceipt: receipt, RunID: frozen.RunID, Checkpoint: checkpoint}, events: events, commits: commits}
	m.history.publish(prepared)
	if m.options.AfterCommit != nil {
		if err = m.options.AfterCommit(); err != nil {
			return RunReceipt{}, RunHistoryReceipt{}, err
		}
	}
	return receipt, historyClone(historyReceipt), nil
}
func (m *MemoryRunStore) ReadEvents(ctx context.Context, id, after string, limit int) (RunEventPage, error) {
	if err := ctx.Err(); err != nil {
		return RunEventPage{}, err
	}
	if !validStorageID(id) || limit < 1 || limit > StoragePageLimit {
		return RunEventPage{}, &StorageInputError{}
	}
	if after != "" && !validStorageID(after) {
		return RunEventPage{}, &StorageCursorInvalid{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return RunEventPage{}, err
	}
	events := m.runs[id].events
	start := 0
	if after != "" {
		start = -1
		for i, item := range events {
			if item.Cursor == after {
				start = i + 1
				break
			}
		}
		if start < 0 {
			return RunEventPage{}, &StorageCursorInvalid{}
		}
	}
	page := RunEventPage{Items: []StoredRunEvent{}, NextCursor: after}
	total := 0
	index := start
	for ; index < len(events) && len(page.Items) < limit; index++ {
		item := events[index]
		if total+len(item.Bytes) > StorageTransactionBytes {
			break
		}
		page.Items = append(page.Items, StoredRunEvent{Cursor: item.Cursor, Bytes: bytes.Clone(item.Bytes)})
		total += len(item.Bytes)
		page.NextCursor = item.Cursor
	}
	page.HasMore = index < len(events)
	return page, nil
}

// NewCheckpointCallbackRunStore is explicitly NONconformant durable storage: revisions, cursors
// and event retention are process-local. Only one writer may use the legacy callback. Decode is
// explicit; no codec, version or lossy checkpoint projection is guessed. Import into a fresh
// adapter uses the ordinary absent-run commit, never an overwrite of an existing head.
func NewCheckpointCallbackRunStore[T any](callback func(context.Context, T) error, decode func([]byte) (T, error)) RunStore {
	m := NewMemoryRunStore(MemoryRunStoreOptions{})
	m.durability = RunStoreSingleWriterCallback
	m.acknowledge = func(ctx context.Context, raw []byte) error {
		state, err := decode(raw)
		if err != nil {
			return err
		}
		return callback(ctx, state)
	}
	return &checkpointCallbackStore{RunStore: m}
}

// Deliberately exposes only the legacy port, never the history/context capability.
type checkpointCallbackStore struct{ RunStore }
