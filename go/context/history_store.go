package runcontext

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"time"
)

type HistoryResource struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}
type ContextSnapshotUpdate struct {
	ChatID           string           `json:"chatId"`
	ExpectedRevision *string          `json:"expectedRevision"`
	Manifest         HistoryObjectRef `json:"manifest"`
	Turn             *int             `json:"turn,omitempty"`
	Closed           bool             `json:"closed,omitempty"`
	PrivateScope     string           `json:"privateScope,omitempty"`
}
type ContextSnapshotHead struct {
	ChatID   string           `json:"chatId"`
	Revision string           `json:"revision"`
	Manifest HistoryObjectRef `json:"manifest"`
}
type HistoryContextRef struct {
	ContextSnapshotHead
	Turn         *int   `json:"turn,omitempty"`
	Closed       bool   `json:"closed,omitempty"`
	PrivateScope string `json:"privateScope,omitempty"`
}
type RunHistoryCommit struct {
	Run          RunCommit
	Observations []HistoryObjectRef
	Genesis      bool
	Context      *ContextSnapshotUpdate
}
type RunHistoryEntry struct {
	Version           int                `json:"version"`
	Sequence          int                `json:"sequence"`
	EventStart        int                `json:"eventStart"`
	EventCount        int                `json:"eventCount"`
	CommitID          string             `json:"commitId"`
	ExecutionRevision string             `json:"executionRevision"`
	RecordedAt        string             `json:"recordedAt"`
	PreviousDigest    *string            `json:"previousDigest"`
	Checkpoint        *HistoryObjectRef  `json:"checkpoint"`
	Events            []HistoryObjectRef `json:"events"`
	Observations      []HistoryObjectRef `json:"observations"`
	Context           *HistoryContextRef `json:"context,omitempty"`
	Digest            string             `json:"digest"`
}
type RunHistoryHead struct {
	Version           int                  `json:"version"`
	RunID             string               `json:"runId"`
	Revision          string               `json:"revision"`
	ExecutionRevision string               `json:"executionRevision"`
	CommitCount       int                  `json:"commitCount"`
	EventCount        int                  `json:"eventCount"`
	Root              string               `json:"root"`
	Coverage          string               `json:"coverage"`
	Checkpoint        *HistoryObjectRef    `json:"checkpoint"`
	Context           *HistoryContextRef   `json:"context,omitempty"`
	Metadata          *HistoryMetadataHead `json:"metadata,omitempty"`
}
type RunHistoryReceipt struct {
	RunReceipt
	History RunHistoryHead
}
type RunHistoryPage struct {
	Revision      string
	AfterSequence int
	Items         []RunHistoryEntry
	NextSequence  int
	HasMore       bool
	Total         int
}
type HistoryObjectPage struct {
	Digest     string
	Total      int
	Offset     int
	Value      []byte
	NextOffset int
	HasMore    bool
}

// HistoryRunStore is one host-bound resource owner. CommitHistory atomically compares and publishes
// both run and optional chat heads. Reads and staged objects grant no execution authority.
type HistoryRunStore interface {
	RunStore
	HistoryVersion() int
	StageHistoryObjects(context.Context, HistoryResource, []HistoryObject) error
	CommitHistory(context.Context, RunHistoryCommit) (RunHistoryReceipt, error)
	ReadHistoryHead(context.Context, string) (*RunHistoryHead, error)
	ReadHistoryPage(context.Context, string, string, int, int) (RunHistoryPage, error)
	ReadHistoryObject(context.Context, HistoryResource, string, string, int, int) (HistoryObjectPage, error)
	LoadContextSnapshot(context.Context, string) (*ContextSnapshotHead, error)
	CommitContextSnapshot(context.Context, ContextSnapshotUpdate, string) (ContextSnapshotHead, error)
	CommitHistoryMetadata(context.Context, HistoryMetadataCommit) (RunHistoryHead, error)
	ReadHistoryMetadataPage(context.Context, string, string, int, int) (HistoryMetadataPage, error)
}
type historyStoredObject struct {
	object    HistoryObject
	published int
}
type historyView struct {
	head           RunHistoryHead
	length         int
	publication    int
	metadataLength int
}
type historyRun struct {
	entries  []RunHistoryEntry
	metadata []HistoryMetadataEntry
	views    map[string]historyView
	head     historyView
}
type historyContextState struct {
	scope    string
	turns    int
	disposed bool
	open     map[int]bool
}
type historyContextView struct {
	state    historyContextState
	head     ContextSnapshotHead
	sequence int
}
type historyContext struct {
	head  historyContextView
	views map[string]historyContextView
}
type historyContextCommit struct {
	input string
	head  ContextSnapshotHead
}
type memoryHistoryJournal struct {
	objects         map[HistoryResource]map[string]*historyStoredObject
	runs            map[string]*historyRun
	contexts        map[string]*historyContext
	contextCommits  map[string]historyContextCommit
	turnOwners      map[string]string
	metadataCommits map[string]historyMetadataCommit
	now             func() time.Time
}
type preparedContext struct {
	state    historyContextState
	head     ContextSnapshotHead
	sequence int
	refs     []HistoryObjectRef
}
type preparedHistory struct {
	resource HistoryResource
	objects  []HistoryObject
	entry    RunHistoryEntry
	view     historyView
	context  *preparedContext
}

func newMemoryHistoryJournal(now func() time.Time) *memoryHistoryJournal {
	if now == nil {
		now = time.Now
	}
	return &memoryHistoryJournal{objects: map[HistoryResource]map[string]*historyStoredObject{}, runs: map[string]*historyRun{}, contexts: map[string]*historyContext{}, contextCommits: map[string]historyContextCommit{}, turnOwners: map[string]string{}, metadataCommits: map[string]historyMetadataCommit{}, now: now}
}
func historyClone[T any](v T) T {
	raw, _ := json.Marshal(v)
	var out T
	_ = json.Unmarshal(raw, &out)
	return out
}
func historyInputText(v any) string { raw, _ := historyJSONBytes(v); return string(raw) }
func validHistoryResource(v HistoryResource) bool {
	return (v.Kind == "run" || v.Kind == "chat") && validStorageID(v.ID)
}
func freezeHistoryExtra(input RunHistoryCommit) (RunHistoryCommit, error) {
	out := input
	out.Run = RunCommit{}
	if len(input.Observations) > 256 {
		return out, &HistoryInputInvalid{}
	}
	out.Observations = append([]HistoryObjectRef{}, input.Observations...)
	for _, ref := range out.Observations {
		if !validHistoryRef(ref) {
			return out, &HistoryInputInvalid{}
		}
	}
	if input.Context != nil {
		c := historyClone(*input.Context)
		if !validStorageID(c.ChatID) || c.ExpectedRevision != nil && !validStorageID(*c.ExpectedRevision) || !validHistoryRef(c.Manifest) || c.Turn != nil && *c.Turn < 1 || c.Closed && c.Turn == nil || c.PrivateScope != "" && (!c.Closed || !validStorageID(c.PrivateScope)) {
			return out, &HistoryInputInvalid{}
		}
		out.Context = &c
	}
	return out, nil
}
func (j *memoryHistoryJournal) stage(resource HistoryResource, objects []HistoryObject) error {
	if !validHistoryResource(resource) || len(objects) < 1 || len(objects) > 256 {
		return &HistoryInputInvalid{}
	}
	total := 0
	frozen := make([]HistoryObject, len(objects))
	for i, v := range objects {
		if !validHistoryRef(v.HistoryObjectRef) || len(v.Value) != v.Bytes || HistoryBytesDigest(v.Value) != v.Digest {
			return &HistoryInputInvalid{}
		}
		total += len(v.Value)
		if total > StorageTransactionBytes {
			return &HistoryInputInvalid{}
		}
		frozen[i] = HistoryObject{HistoryObjectRef: v.HistoryObjectRef, Value: bytes.Clone(v.Value)}
	}
	held := j.objects[resource]
	if held == nil {
		held = map[string]*historyStoredObject{}
	}
	for _, v := range frozen {
		if old := held[v.Digest]; old != nil && (!bytes.Equal(old.object.Value, v.Value) || old.object.Bytes != v.Bytes) {
			return &HistoryInputInvalid{}
		}
	}
	for _, v := range frozen {
		if held[v.Digest] == nil {
			held[v.Digest] = &historyStoredObject{object: v}
		}
	}
	j.objects[resource] = held
	return nil
}
func (j *memoryHistoryJournal) resolve(resource HistoryResource, ref HistoryObjectRef) (HistoryObject, error) {
	v := j.objects[resource][ref.Digest]
	if v == nil || v.object.Bytes != ref.Bytes {
		return HistoryObject{}, &HistoryInputInvalid{}
	}
	return v.object, nil
}
func (j *memoryHistoryJournal) prepareContext(input ContextSnapshotUpdate) (*preparedContext, error) {
	held := j.contexts[input.ChatID]
	actual := ""
	sequence := 1
	if held != nil {
		actual = held.head.head.Revision
		sequence = held.head.sequence + 1
	}
	expected := ""
	if input.ExpectedRevision != nil {
		expected = *input.ExpectedRevision
	}
	if actual != expected {
		return nil, &StorageConflict{ExpectedRevision: expected, ActualRevision: actual}
	}
	resource := HistoryResource{Kind: "chat", ID: input.ChatID}
	object, err := j.resolve(resource, input.Manifest)
	if err != nil {
		return nil, err
	}
	manifest, err := readHistoryChunkManifest(object.Value)
	if err != nil {
		return nil, err
	}
	full := make([]byte, 0, manifest.Bytes)
	refs := []HistoryObjectRef{input.Manifest}
	for _, ref := range manifest.Parts {
		part, err := j.resolve(resource, ref)
		if err != nil {
			return nil, err
		}
		full = append(full, part.Value...)
		refs = append(refs, ref)
	}
	if HistoryBytesDigest(full) != manifest.Digest {
		return nil, &HistoryInputInvalid{}
	}
	snapshot, err := ReadChatContextSnapshot(full)
	if err != nil {
		return nil, err
	}
	if input.Turn != nil {
		found := false
		for _, turn := range snapshot.Open {
			found = found || turn.Number == *input.Turn
		}
		if *input.Turn > snapshot.Turns || found == input.Closed {
			return nil, &HistoryInputInvalid{}
		}
	}
	if input.PrivateScope != "" && (!input.Closed || input.PrivateScope == snapshot.Scope) {
		return nil, &HistoryInputInvalid{}
	}
	state := historyContextState{scope: snapshot.Scope, turns: snapshot.Turns, disposed: snapshot.Disposed, open: map[int]bool{}}
	for _, turn := range snapshot.Open {
		state.open[turn.Number] = true
	}
	if held != nil {
		old := held.head.state
		if state.scope != old.scope || state.turns < old.turns || old.disposed && !state.disposed {
			return nil, &HistoryInputInvalid{}
		}
		for n := range state.open {
			if n <= old.turns && !old.open[n] {
				return nil, &HistoryInputInvalid{}
			}
		}
	}
	return &preparedContext{state: state, head: ContextSnapshotHead{ChatID: input.ChatID, Revision: rand.Text(), Manifest: input.Manifest}, sequence: sequence, refs: refs}, nil
}
func (j *memoryHistoryJournal) prepare(run RunCommit, receipt RunReceipt, checkpoint []byte, extra *RunHistoryCommit) (*preparedHistory, error) {
	held := j.runs[run.RunID]
	sequence, eventCount := 1, len(run.Events)
	var previous *string
	coverage := "legacy-incomplete"
	var contextRef *HistoryContextRef
	if held != nil {
		sequence = held.head.length + 1
		eventCount += held.head.head.EventCount
		root := held.head.head.Root
		previous = &root
		coverage = held.head.head.Coverage
		contextRef = held.head.head.Context
	} else if extra != nil && extra.Genesis {
		coverage = "recording-incomplete"
	}
	if extra != nil && (sequence > HistoryMaxRows || eventCount > HistoryMaxEvents || extra.Genesis && held != nil) {
		return nil, &HistoryInputInvalid{}
	}
	prepared := &preparedHistory{resource: HistoryResource{Kind: "run", ID: run.RunID}}
	ref := func(raw []byte) *HistoryObjectRef {
		if raw == nil {
			return nil
		}
		object := HistoryObject{HistoryObjectRef: HistoryObjectRef{Digest: HistoryBytesDigest(raw), Bytes: len(raw)}, Value: bytes.Clone(raw)}
		prepared.objects = append(prepared.objects, object)
		out := object.HistoryObjectRef
		return &out
	}
	cp := ref(checkpoint)
	events := []HistoryObjectRef{}
	for _, raw := range run.Events {
		events = append(events, *ref(raw))
	}
	observations := []HistoryObjectRef{}
	if extra != nil {
		observations = append(observations, extra.Observations...)
		recorded := map[string]int{}
		if held != nil {
			for _, row := range held.entries {
				for _, ref := range row.Observations {
					if recorded[ref.Digest] == 0 {
						recorded[ref.Digest] = row.Sequence
					}
				}
			}
		}
		for _, ref := range observations {
			if recorded[ref.Digest] == 0 {
				recorded[ref.Digest] = sequence
			}
		}
		closure, err := historyObservationClosure(observations, func(ref HistoryObjectRef) (HistoryObject, error) { return j.resolve(prepared.resource, ref) }, recorded)
		if err != nil {
			return nil, err
		}
		prepared.objects = append(prepared.objects, closure...)
		if c := extra.Context; c != nil {
			if c.Turn == nil {
				return nil, &HistoryInputInvalid{}
			}
			owner := j.turnOwners[historyInputText([]any{c.ChatID, *c.Turn})]
			if owner != "" && owner != run.RunID || contextRef != nil && (contextRef.ChatID != c.ChatID || contextRef.Turn == nil || *contextRef.Turn != *c.Turn || contextRef.Closed && !c.Closed || contextRef.PrivateScope != "" && c.PrivateScope == "") {
				return nil, &HistoryInputInvalid{}
			}
			context, err := j.prepareContext(*c)
			if err != nil {
				return nil, err
			}
			prepared.context = context
			turn := *c.Turn
			contextRef = &HistoryContextRef{ContextSnapshotHead: context.head, Turn: &turn, Closed: c.Closed, PrivateScope: c.PrivateScope}
		}
	}
	entry := RunHistoryEntry{Version: 1, Sequence: sequence, EventStart: eventCount - len(events) + 1, EventCount: len(events), CommitID: run.CommitID, ExecutionRevision: receipt.Revision, RecordedAt: j.now().UTC().Format("2006-01-02T15:04:05.000Z"), PreviousDigest: previous, Checkpoint: cp, Events: events, Observations: observations, Context: contextRef}
	body, _ := historyJSONBytes(entry)
	value, _ := ParseJSON(body)
	value = recordWithoutDigest(value)
	entry.Digest = HistoryBytesDigest([]byte(CanonicalJSON(value)))
	prepared.entry = entry
	publication, metadataLength := 1, 0
	var metadata *HistoryMetadataHead
	if held != nil {
		publication = held.head.publication + 1
		metadataLength = len(held.metadata)
		if held.head.head.Metadata != nil {
			copy := historyClone(*held.head.head.Metadata)
			copy.ExecutionRevision = receipt.Revision
			metadata = &copy
		}
	}
	prepared.view = historyView{head: RunHistoryHead{Version: 1, RunID: run.RunID, Revision: rand.Text(), ExecutionRevision: receipt.Revision, CommitCount: sequence, EventCount: eventCount, Root: entry.Digest, Coverage: coverage, Checkpoint: cp, Context: contextRef, Metadata: metadata}, length: sequence, publication: publication, metadataLength: metadataLength}
	return prepared, nil
}
func (j *memoryHistoryJournal) publishContext(p *preparedContext) {
	resource := HistoryResource{Kind: "chat", ID: p.head.ChatID}
	for _, ref := range p.refs {
		v := j.objects[resource][ref.Digest]
		if v.published == 0 {
			v.published = p.sequence
		}
	}
	held := j.contexts[p.head.ChatID]
	if held == nil {
		held = &historyContext{views: map[string]historyContextView{}}
		j.contexts[p.head.ChatID] = held
	}
	view := historyContextView{head: p.head, sequence: p.sequence, state: p.state}
	held.views[p.head.Revision] = view
	held.head = view
}
func (j *memoryHistoryJournal) publish(p *preparedHistory) {
	objects := j.objects[p.resource]
	if objects == nil {
		objects = map[string]*historyStoredObject{}
		j.objects[p.resource] = objects
	}
	for _, object := range p.objects {
		if objects[object.Digest] == nil {
			objects[object.Digest] = &historyStoredObject{object: object}
		}
		if objects[object.Digest].published == 0 {
			objects[object.Digest].published = p.view.publication
		}
	}
	for _, ref := range p.entry.Observations {
		if objects[ref.Digest].published == 0 {
			objects[ref.Digest].published = p.view.publication
		}
	}
	held := j.runs[p.view.head.RunID]
	if held == nil {
		held = &historyRun{views: map[string]historyView{}}
		j.runs[p.view.head.RunID] = held
	}
	held.entries = append(held.entries, p.entry)
	held.views[p.view.head.Revision] = p.view
	held.head = p.view
	if p.context != nil {
		j.publishContext(p.context)
		j.turnOwners[historyInputText([]any{p.context.head.ChatID, *p.entry.Context.Turn})] = p.view.head.RunID
	}
}
func (m *MemoryRunStore) HistoryVersion() int { return 1 }
func (m *MemoryRunStore) StageHistoryObjects(ctx context.Context, r HistoryResource, objects []HistoryObject) error {
	if !validHistoryResource(r) || len(objects) < 1 || len(objects) > 256 {
		return &HistoryInputInvalid{}
	}
	total := 0
	for _, object := range objects {
		total += len(object.Value)
		if !validHistoryRef(object.HistoryObjectRef) || len(object.Value) != object.Bytes || total > StorageTransactionBytes {
			return &HistoryInputInvalid{}
		}
	}
	frozen := make([]HistoryObject, len(objects))
	for i, object := range objects {
		frozen[i] = HistoryObject{HistoryObjectRef: object.HistoryObjectRef, Value: bytes.Clone(object.Value)}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	return m.history.stage(r, frozen)
}
func (m *MemoryRunStore) CommitHistory(ctx context.Context, input RunHistoryCommit) (RunHistoryReceipt, error) {
	extra, err := freezeHistoryExtra(input)
	if err != nil {
		return RunHistoryReceipt{}, err
	}
	_, receipt, err := m.commitTransaction(ctx, input.Run, &extra, nil)
	return receipt, err
}
func (m *MemoryRunStore) ReadHistoryHead(ctx context.Context, id string) (*RunHistoryHead, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !validStorageID(id) {
		return nil, &HistoryInputInvalid{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if held := m.history.runs[id]; held != nil {
		out := historyClone(held.head.head)
		return &out, nil
	}
	return nil, nil
}
func (m *MemoryRunStore) ReadHistoryPage(ctx context.Context, id, revision string, after, limit int) (RunHistoryPage, error) {
	if err := ctx.Err(); err != nil {
		return RunHistoryPage{}, err
	}
	if !validStorageID(id) || !validStorageID(revision) || limit < 1 || limit > StoragePageLimit {
		return RunHistoryPage{}, &HistoryInputInvalid{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	held := m.history.runs[id]
	if held == nil {
		return RunHistoryPage{}, &HistoryChanged{}
	}
	view, ok := held.views[revision]
	if !ok {
		return RunHistoryPage{}, &HistoryChanged{}
	}
	if after < 0 || after > view.length {
		return RunHistoryPage{}, &HistoryInputInvalid{}
	}
	out := RunHistoryPage{Revision: revision, AfterSequence: after, Items: []RunHistoryEntry{}, NextSequence: after, Total: view.length}
	total := 0
	for i := after; i < view.length && len(out.Items) < limit; i++ {
		raw, _ := historyJSONBytes(held.entries[i])
		if total+len(raw) > StorageTransactionBytes {
			break
		}
		total += len(raw)
		out.Items = append(out.Items, historyClone(held.entries[i]))
		out.NextSequence = i + 1
	}
	out.HasMore = out.NextSequence < view.length
	return out, nil
}
func (m *MemoryRunStore) ReadHistoryObject(ctx context.Context, r HistoryResource, revision, digest string, offset, limit int) (HistoryObjectPage, error) {
	if err := ctx.Err(); err != nil {
		return HistoryObjectPage{}, err
	}
	if !validHistoryResource(r) || !validStorageID(revision) || offset < 0 || limit < 1 || limit > HistoryObjectBytes {
		return HistoryObjectPage{}, &HistoryInputInvalid{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	bound := 0
	if r.Kind == "run" {
		if held := m.history.runs[r.ID]; held != nil {
			bound = held.views[revision].publication
		}
	} else if held := m.history.contexts[r.ID]; held != nil {
		bound = held.views[revision].sequence
	}
	if bound == 0 {
		return HistoryObjectPage{}, &HistoryChanged{}
	}
	v := m.history.objects[r][digest]
	if v == nil || v.published == 0 || v.published > bound || offset > v.object.Bytes {
		return HistoryObjectPage{}, &HistoryInputInvalid{}
	}
	end := min(v.object.Bytes, offset+limit)
	return HistoryObjectPage{Digest: digest, Total: v.object.Bytes, Offset: offset, Value: bytes.Clone(v.object.Value[offset:end]), NextOffset: end, HasMore: end < v.object.Bytes}, nil
}
func (m *MemoryRunStore) LoadContextSnapshot(ctx context.Context, id string) (*ContextSnapshotHead, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !validStorageID(id) {
		return nil, &HistoryInputInvalid{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if held := m.history.contexts[id]; held != nil {
		out := historyClone(held.head.head)
		return &out, nil
	}
	return nil, nil
}
func (m *MemoryRunStore) CommitContextSnapshot(ctx context.Context, input ContextSnapshotUpdate, commitID string) (ContextSnapshotHead, error) {
	return m.commitContextSnapshot(ctx, input, commitID, nil)
}

// CommitContextSnapshotFenced (#223): a leased run's chat snapshot commits only under its current fence,
// checked in the same locked transaction. The fence names the run; the snapshot names its chat.
func (m *MemoryRunStore) CommitContextSnapshotFenced(ctx context.Context, input ContextSnapshotUpdate, commitID string, fence LeaseFence) (ContextSnapshotHead, error) {
	return m.commitContextSnapshot(ctx, input, commitID, &fence)
}
func (m *MemoryRunStore) commitContextSnapshot(ctx context.Context, input ContextSnapshotUpdate, commitID string, fence *LeaseFence) (ContextSnapshotHead, error) {
	extra, err := freezeHistoryExtra(RunHistoryCommit{Context: &input})
	if err != nil {
		return ContextSnapshotHead{}, err
	}
	if !validStorageID(commitID) {
		return ContextSnapshotHead{}, &HistoryInputInvalid{}
	}
	if err = ctx.Err(); err != nil {
		return ContextSnapshotHead{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err = ctx.Err(); err != nil {
		return ContextSnapshotHead{}, err
	}
	if fence != nil {
		if _, err = m.leases.Check(*fence); err != nil {
			return ContextSnapshotHead{}, err
		}
	}
	key := historyInputText([]string{input.ChatID, commitID})
	text := historyInputText(extra.Context)
	if prior, ok := m.history.contextCommits[key]; ok {
		if prior.input != text {
			return ContextSnapshotHead{}, &HistoryInputInvalid{}
		}
		return historyClone(prior.head), nil
	}
	p, err := m.history.prepareContext(*extra.Context)
	if err != nil {
		return ContextSnapshotHead{}, err
	}
	m.history.publishContext(p)
	m.history.contextCommits[key] = historyContextCommit{input: text, head: p.head}
	return historyClone(p.head), nil
}
