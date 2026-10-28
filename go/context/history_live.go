package runcontext

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
)

type liveHistoryItem struct {
	ref                       HistoryObjectRef
	row                       *Value
	sourceDigest, replacement string
}
type liveHistoryRecorder struct {
	store                         HistoryRunStore
	runID, executionRevision      string
	checkpoint                    []byte
	initialized, enabled, genesis bool
	head                          *RunHistoryHead
	contexts                      map[string]HistoryObjectRef
	active                        map[string][]HistoryObjectRef
	requests                      map[string]bool
	replacements                  map[string]HistoryObjectRef
	pending                       []liveHistoryItem
}
type preparedLiveHistory struct {
	observations []HistoryObjectRef
	genesis      bool
	acknowledge  func(context.Context, RunHistoryHead, RunCommit, string) error
}

func newLiveHistoryRecorder(store HistoryRunStore, id, revision string, legacy bool, checkpoint []byte) *liveHistoryRecorder {
	return &liveHistoryRecorder{store: store, runID: id, executionRevision: revision, checkpoint: bytes.Clone(checkpoint), enabled: true, genesis: revision == "" && !legacy, contexts: map[string]HistoryObjectRef{}, active: map[string][]HistoryObjectRef{}, requests: map[string]bool{}, replacements: map[string]HistoryObjectRef{}}
}
func liveHistoryRef(v *Value) HistoryObjectRef {
	var ref HistoryObjectRef
	_ = json.Unmarshal(v.JSON(), &ref)
	return ref
}
func liveHistoryRefs(v *Value) []HistoryObjectRef {
	refs := []HistoryObjectRef{}
	for _, row := range v.Items() {
		refs = append(refs, liveHistoryRef(row))
	}
	return refs
}
func liveHistoryScope(v *Value) DurableScope {
	var scope DurableScope
	_ = json.Unmarshal(v.JSON(), &scope)
	return scope
}
func liveHistoryChunk(ref HistoryObjectRef, resolve func(HistoryObjectRef) ([]byte, error)) ([]byte, string, error) {
	raw, err := resolve(ref)
	if err != nil {
		return nil, "", err
	}
	manifest, err := ReadHistoryChunkManifest(raw)
	if err != nil {
		return nil, "", err
	}
	full := []byte{}
	for _, part := range manifest.Parts {
		raw, err = resolve(part)
		if err != nil {
			return nil, "", err
		}
		full = append(full, raw...)
	}
	if len(full) != manifest.Bytes || HistoryBytesDigest(full) != manifest.Digest {
		return nil, "", &HistoryInputInvalid{}
	}
	return full, manifest.Digest, nil
}
func (h *liveHistoryRecorder) accept(item liveHistoryItem) {
	row := item.row
	scope := liveHistoryScope(row.Get("scope"))
	key := historyInputText(scope)
	switch row.Get("kind").Str() {
	case "original":
		if row.Get("format").Str() == "context" {
			h.contexts[historyInputText([]any{scope, row.Get("boundary").Str(), item.sourceDigest})] = item.ref
		}
	case "request":
		h.requests[row.Get("dispatchId").Str()] = true
		h.active[key] = liveHistoryRefs(row.Get("compactions"))
	case "compaction":
		h.replacements[historyInputText([]any{scope, item.replacement})] = item.ref
		active := []HistoryObjectRef{}
		prior := liveHistoryRefs(row.Get("prior"))
		for _, ref := range h.active[key] {
			keep := true
			for _, p := range prior {
				if p.Digest == ref.Digest {
					keep = false
				}
			}
			if keep {
				active = append(active, ref)
			}
		}
		h.active[key] = append(active, item.ref)
	}
}
func (h *liveHistoryRecorder) initialize(ctx context.Context) error {
	if h.initialized {
		return nil
	}
	head, err := h.store.ReadHistoryHead(ctx, h.runID)
	if err != nil {
		return err
	}
	if head != nil {
		raw, e := historyJSONBytes(head)
		if e != nil {
			return e
		}
		strict, e := ReadRunHistoryHead(raw)
		if e != nil {
			return e
		}
		head = &strict
		if head.RunID != h.runID || head.ExecutionRevision != h.executionRevision {
			return &HistoryInputInvalid{}
		}
		if h.checkpoint != nil && (head.Checkpoint == nil || *head.Checkpoint != (HistoryObjectRef{Digest: HistoryBytesDigest(h.checkpoint), Bytes: len(h.checkpoint)})) {
			return &HistoryInputInvalid{}
		}
	}
	if h.genesis {
		if head != nil {
			return &HistoryChanged{}
		}
	} else if head != nil && head.Coverage == "recording-incomplete" {
		view, e := ReadRetainedRunHistory(ctx, h.store, h.runID)
		if e != nil {
			return e
		}
		if historyInputText(view.Head()) != historyInputText(head) {
			return &HistoryChanged{}
		}
		genesis := 0
		for _, commit := range view.Commits() {
			for _, ref := range commit.Observations {
				raw, e := view.ReadObject(ref)
				if e != nil {
					return e
				}
				row, e := ReadHistoryObservation(raw)
				if e != nil {
					return e
				}
				item := liveHistoryItem{ref: ref, row: row}
				if row.Get("kind").Str() == "genesis" {
					scope := liveHistoryScope(row.Get("scope"))
					if commit.Sequence != 1 || scope != (DurableScope{Execution: h.runID, Role: "author"}) {
						return &HistoryInputInvalid{}
					}
					genesis++
				}
				if row.Get("kind").Str() == "original" && row.Get("format").Str() == "context" {
					_, item.sourceDigest, e = liveHistoryChunk(liveHistoryRef(row.Get("value")), view.ReadObject)
					if e != nil {
						return e
					}
				}
				if row.Get("kind").Str() == "compaction" {
					full, _, e := liveHistoryChunk(liveHistoryRef(row.Get("replacement")), view.ReadObject)
					if e != nil {
						return e
					}
					item.replacement = string(full)
				}
				h.accept(item)
			}
		}
		if genesis != 1 {
			return &HistoryInputInvalid{}
		}
	} else {
		h.enabled = false
	}
	h.head = head
	h.initialized = true
	return nil
}
func (h *liveHistoryRecorder) add(ctx context.Context, prepared PreparedHistoryObservation) (HistoryObjectRef, error) {
	if err := h.initialize(ctx); err != nil {
		return HistoryObjectRef{}, err
	}
	if !h.enabled || len(h.pending) >= 256 {
		return HistoryObjectRef{}, &HistoryInputInvalid{}
	}
	ref, err := StageHistoryObservation(ctx, h.store, h.runID, prepared)
	if err != nil {
		return ref, err
	}
	resolve := func(ref HistoryObjectRef) ([]byte, error) {
		for _, object := range prepared.Objects {
			if object.HistoryObjectRef == ref {
				return bytes.Clone(object.Value), nil
			}
		}
		return nil, &HistoryInputInvalid{}
	}
	raw, err := resolve(ref)
	if err != nil {
		return ref, err
	}
	row, err := ReadHistoryObservation(raw)
	if err != nil {
		return ref, err
	}
	item := liveHistoryItem{ref: ref, row: row}
	if row.Get("kind").Str() == "original" && row.Get("format").Str() == "context" {
		_, item.sourceDigest, err = liveHistoryChunk(liveHistoryRef(row.Get("value")), resolve)
	}
	if row.Get("kind").Str() == "compaction" {
		var full []byte
		full, _, err = liveHistoryChunk(liveHistoryRef(row.Get("replacement")), resolve)
		item.replacement = string(full)
	}
	if err != nil {
		return ref, err
	}
	h.pending = append(h.pending, item)
	return ref, nil
}
func liveHistoryContextSource(state contract.Checkpoint) (string, error) {
	rows := Array()
	for _, entry := range state.Context.Entries {
		if entry.ValueJSON == nil {
			return "", &HistoryInputInvalid{}
		}
		row := ObjectOf("id", String(entry.ID), "kind", String(entry.Kind), "label", String(entry.Label), "valueJSON", String(*entry.ValueJSON), "bytes", Number(float64(entry.Bytes)), "digest", String(entry.Digest))
		if entry.Revision != nil {
			row.Set("revision", String(*entry.Revision))
		}
		rows.Append(row)
	}
	return rows.Text(), nil
}
func (h *liveHistoryRecorder) prepare(ctx context.Context, state contract.Checkpoint, scope DurableScope, contextUpdate bool) (*preparedLiveHistory, error) {
	if err := h.initialize(ctx); err != nil {
		return nil, err
	}
	if !h.enabled && !contextUpdate {
		return nil, nil
	}
	if h.enabled {
		if h.genesis {
			found := false
			for _, item := range h.pending {
				found = found || item.row.Get("kind").Str() == "genesis"
			}
			if !found {
				prepared, err := PrepareHistoryGenesis(DurableScope{Execution: h.runID, Role: "author"})
				if err != nil {
					return nil, err
				}
				if _, err = h.add(ctx, prepared); err != nil {
					return nil, err
				}
			}
		}
		recordContext := func(state contract.Checkpoint, scope DurableScope) (HistoryObjectRef, error) {
			source, err := liveHistoryContextSource(state)
			if err != nil {
				return HistoryObjectRef{}, err
			}
			sum := HistoryBytesDigest([]byte(source))
			key := historyInputText([]any{scope, state.Context.Scope, sum})
			if ref, ok := h.contexts[key]; ok {
				return ref, nil
			}
			for _, item := range h.pending {
				row := item.row
				if row.Get("kind").Str() == "original" && row.Get("format").Str() == "context" && historyInputText([]any{liveHistoryScope(row.Get("scope")), row.Get("boundary").Str(), item.sourceDigest}) == key {
					return item.ref, nil
				}
			}
			prepared, err := PrepareHistoryOriginal(scope, state.Context.Scope, source, "context", 0)
			if err != nil {
				return HistoryObjectRef{}, err
			}
			return h.add(ctx, prepared)
		}
		rootScope := DurableScope{Execution: h.runID, Role: "author"}
		if scope.Execution == h.runID {
			rootScope.Role = scope.Role
		}
		if _, err := recordContext(state, rootScope); err != nil {
			return nil, err
		}
		if state.Durable != nil {
			var book DurableBook
			raw, err := json.Marshal(state.Durable)
			if err != nil {
				return nil, err
			}
			if err = json.Unmarshal(raw, &book); err != nil {
				return nil, err
			}
			for _, child := range book.Children {
				if _, err := recordContext(child.Checkpoint, child.Scope); err != nil {
					return nil, err
				}
			}
			for _, claim := range book.Dispatches {
				if h.requests[claim.ID] {
					continue
				}
				pending := false
				for _, item := range h.pending {
					if item.row.Get("kind").Str() == "request" && item.row.Get("dispatchId").Str() == claim.ID {
						pending = true
					}
				}
				if pending {
					continue
				}
				var used *contract.Checkpoint
				if claim.Scope.Execution == state.ID {
					used = &state
				} else {
					for i := range book.Children {
						if book.Children[i].Scope == claim.Scope {
							used = &book.Children[i].Checkpoint
							break
						}
					}
				}
				originals := []HistoryObjectRef{}
				if used != nil {
					ref, err := recordContext(*used, claim.Scope)
					if err != nil {
						return nil, err
					}
					originals = append(originals, ref)
				}
				active := h.active[historyInputText(claim.Scope)]
				if active == nil {
					active = []HistoryObjectRef{}
				}
				prepared, err := PrepareHistoryRequest(claim.Scope, claim.ID, claim.Source, claim.Tool, claim.Request, active, originals)
				if err != nil {
					return nil, err
				}
				if _, err = h.add(ctx, prepared); err != nil {
					return nil, err
				}
			}
		}
	}
	rows := []liveHistoryItem{}
	for _, item := range h.pending {
		if item.row.Get("kind").Str() == "genesis" {
			rows = append(rows, item)
		}
	}
	for _, item := range h.pending {
		if item.row.Get("kind").Str() != "genesis" {
			rows = append(rows, item)
		}
	}
	refs := []HistoryObjectRef{}
	for _, row := range rows {
		refs = append(refs, row.ref)
	}
	previous := h.head
	genesis := h.genesis
	return &preparedLiveHistory{observations: refs, genesis: genesis, acknowledge: func(ctx context.Context, inputHead RunHistoryHead, input RunCommit, executionRevision string) error {
		invalid := func() error { return &StorageAcknowledgementInvalid{} }
		raw, err := historyJSONBytes(inputHead)
		if err != nil {
			return invalid()
		}
		head, err := ReadRunHistoryHead(raw)
		if err != nil {
			return invalid()
		}
		count, events := 0, 0
		coverage := "legacy-incomplete"
		var previousRoot *string
		var checkpoint *HistoryObjectRef
		if genesis {
			coverage = "recording-incomplete"
		}
		if previous != nil {
			count = previous.CommitCount
			events = previous.EventCount
			coverage = previous.Coverage
			previousRoot = &previous.Root
			checkpoint = previous.Checkpoint
		}
		if input.Checkpoint != nil {
			checkpoint = &HistoryObjectRef{Digest: HistoryBytesDigest(input.Checkpoint), Bytes: len(input.Checkpoint)}
		}
		if head.RunID != h.runID || head.ExecutionRevision != executionRevision || previous != nil && head.Revision == previous.Revision || head.Coverage != coverage || head.CommitCount != count+1 || head.EventCount != events+len(input.Events) || historyInputText(head.Checkpoint) != historyInputText(checkpoint) {
			return invalid()
		}
		page, err := h.store.ReadHistoryPage(ctx, h.runID, head.Revision, count, 1)
		if err != nil {
			return err
		}
		if page.Revision != head.Revision || page.AfterSequence != count || page.NextSequence != count+1 || page.HasMore || page.Total != count+1 || len(page.Items) != 1 {
			return invalid()
		}
		raw, err = historyJSONBytes(page.Items[0])
		if err != nil {
			return invalid()
		}
		row, err := ReadRunHistoryEntry(raw)
		if err != nil {
			return invalid()
		}
		eventRefs := []HistoryObjectRef{}
		for _, event := range input.Events {
			eventRefs = append(eventRefs, HistoryObjectRef{Digest: HistoryBytesDigest(event), Bytes: len(event)})
		}
		if row.Sequence != count+1 || historyInputText(row.PreviousDigest) != historyInputText(previousRoot) || row.EventStart != events+1 || row.Digest != head.Root || row.CommitID != input.CommitID || row.ExecutionRevision != executionRevision || historyInputText(row.Checkpoint) != historyInputText(checkpoint) || historyInputText(row.Context) != historyInputText(head.Context) || historyInputText(row.Observations) != historyInputText(refs) || historyInputText(row.Events) != historyInputText(eventRefs) {
			return invalid()
		}
		for _, item := range rows {
			h.accept(item)
		}
		h.pending = nil
		h.genesis = false
		h.head = &head
		return nil
	}}, nil
}

// HistoryOriginal retains public source through the original writer lock and CAS, returning only acknowledged refs.
func (w *RunWriter) HistoryOriginal(ctx context.Context, binding RunBinding, boundary, sourceJSON, format string, ordinal int) (HistoryObjectRef, error) {
	return w.publishHistory(ctx, binding, func() (PreparedHistoryObservation, error) {
		return PrepareHistoryOriginal(binding.Scope, boundary, sourceJSON, format, ordinal)
	})
}
func (w *RunWriter) HistoryCompaction(ctx context.Context, binding RunBinding, boundary string, originals []HistoryObjectRef, replacementJSON, mode string, prior []HistoryObjectRef, legacyDigest string) (HistoryObjectRef, error) {
	originals = append([]HistoryObjectRef{}, originals...)
	prior = append([]HistoryObjectRef{}, prior...)
	return w.publishHistory(ctx, binding, func() (PreparedHistoryObservation, error) {
		return PrepareHistoryCompaction(binding.Scope, boundary, originals, replacementJSON, mode, prior, legacyDigest)
	})
}
func (w *RunWriter) publishHistory(ctx context.Context, binding RunBinding, prepare func() (PreparedHistoryObservation, error)) (HistoryObjectRef, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.fatal != nil {
		return HistoryObjectRef{}, w.fatal
	}
	if binding.Writer != w {
		return HistoryObjectRef{}, &ContinuationIdentityChanged{}
	}
	known := binding.Scope.Execution == w.ID && binding.Scope.Task == ""
	for _, child := range w.book.Children {
		known = known || child.Scope == binding.Scope
	}
	if !known {
		return HistoryObjectRef{}, &ContinuationIdentityChanged{}
	}
	if w.dispatchClosed.Load() || w.stopped || ctx.Err() != nil {
		if ctx.Err() != nil {
			return HistoryObjectRef{}, ctx.Err()
		}
		return HistoryObjectRef{}, failure.Cancelled
	}
	fail := func(err error) (HistoryObjectRef, error) { w.fatal = err; return HistoryObjectRef{}, err }
	if w.latest == nil || w.acknowledgedCheckpoint == nil || w.history == nil {
		return fail(&ContinuationNotRecoverable{})
	}
	if err := w.history.initialize(ctx); err != nil {
		return fail(err)
	}
	if !w.history.enabled {
		return fail(&ContinuationNotRecoverable{})
	}
	prepared, err := prepare()
	if err != nil {
		return fail(err)
	}
	if liveHistoryScope(prepared.Observation.Get("scope")) != binding.Scope {
		return fail(&ContinuationIdentityChanged{})
	}
	ref, err := w.history.add(ctx, prepared)
	if err != nil {
		return fail(err)
	}
	if err = w.commitLocked(ctx, newStorageIdentity(), *w.latest, bytes.Clone(w.acknowledgedCheckpoint), [][]byte{}, binding.Scope); err != nil {
		return fail(err)
	}
	return ref, nil
}

// historyRecording inspects the optional recorder under the original writer lock.
func (w *RunWriter) historyRecording(ctx context.Context) (bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.fatal != nil {
		return false, w.fatal
	}
	if w.history == nil {
		return false, nil
	}
	if err := w.history.initialize(ctx); err != nil {
		w.fatal = err
		return false, err
	}
	return w.history.enabled, nil
}
