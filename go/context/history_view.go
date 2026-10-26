package runcontext

import (
	"bytes"
	"context"
	"encoding/json"
)

// ReadHistoryObservation validates portable sidecar data; it confers no execution authority.
func ReadHistoryObservation(raw []byte) (*Value, error) {
	v, err := readCanonicalHistoryJSON(raw, HistoryObjectBytes)
	if err != nil || v.Get("version").Num() != 1 || !historyScopeValue(v.Get("scope")) {
		return nil, &HistoryInputInvalid{}
	}
	refs := func(v *Value) bool {
		if !v.IsArray() || v.Len() > 10000 {
			return false
		}
		for _, r := range v.Items() {
			if !historyRefValue(r) {
				return false
			}
		}
		return true
	}
	valid := false
	switch v.Get("kind").Str() {
	case "genesis":
		profile := v.Get("profile")
		valid = snapshotObject(v, []string{"version", "kind", "scope", "profile"}) && snapshotObject(profile, []string{"version", "profile", "genesis", "originals", "compactions", "requests"}) && profile.Get("version").Num() == 1 && profile.Get("profile").Str() == "full-history"
		for _, key := range []string{"genesis", "originals", "compactions", "requests"} {
			valid = valid && profile.Get(key).Kind() == KindBool && profile.Get(key).Truth()
		}
	case "capture-request":
		valid = snapshotObject(v, []string{"version", "kind", "scope", "dispatchId", "captureKind", "source", "tool", "requestDigest", "surface"}) && historyID(v.Get("dispatchId")) && oneOf(v.Get("captureKind"), "model", "agent") && historyID(v.Get("source")) && historyID(v.Get("tool")) && historySHA256.MatchString(v.Get("requestDigest").Str()) && (v.Get("captureKind").Str() == "model" && v.Get("surface").Str() == "session-model" || v.Get("captureKind").Str() == "agent" && v.Get("surface").Str() == "native-aggregate")
	case "original":
		valid = snapshotObject(v, []string{"version", "kind", "scope", "boundary", "value", "format", "ordinal"}) && historyID(v.Get("boundary")) && historyRefValue(v.Get("value")) && oneOf(v.Get("format"), "messages", "context") && snapshotCount(v.Get("ordinal"), 0)
	case "compaction":
		valid = snapshotObject(v, []string{"version", "kind", "scope", "boundary", "mode", "originals", "replacement", "prior", "legacyDigest"}) && historyID(v.Get("boundary")) && oneOf(v.Get("mode"), "index", "summary") && refs(v.Get("originals")) && historyRefValue(v.Get("replacement")) && refs(v.Get("prior")) && historyID(v.Get("legacyDigest"))
	case "request":
		valid = snapshotObject(v, []string{"version", "kind", "scope", "dispatchId", "source", "tool", "request", "compactions", "originals"}) && historyID(v.Get("dispatchId")) && historyID(v.Get("source")) && historyID(v.Get("tool")) && historyRefValue(v.Get("request")) && refs(v.Get("compactions")) && refs(v.Get("originals"))
	case "relation":
		target := v.Get("target")
		valid = snapshotObject(v, []string{"version", "kind", "scope", "dispatchId", "target", "evidence"}) && historyID(v.Get("dispatchId")) && snapshotObject(target, []string{"kind", "id"}) && oneOf(target.Get("kind"), "job", "artifact", "dialect") && historyID(target.Get("id")) && historyRefValue(v.Get("evidence"))
	}
	if !valid {
		return nil, &HistoryInputInvalid{}
	}
	return v, nil
}
func historyContextValue(v *Value) bool {
	return snapshotObject(v, []string{"chatId", "revision", "manifest", "turn"}, "closed", "privateScope") && historyID(v.Get("chatId")) && historyID(v.Get("revision")) && historyRefValue(v.Get("manifest")) && snapshotCount(v.Get("turn"), 1) && (!v.Has("closed") || v.Get("closed").Kind() == KindBool && v.Get("closed").Truth()) && (!v.Has("privateScope") || v.Get("closed").Truth() && historyID(v.Get("privateScope")))
}
func historyNullableRef(v *Value) bool { return v.Kind() == KindNull || historyRefValue(v) }
func historyNullableDigest(v *Value) bool {
	return v.Kind() == KindNull || v.IsString() && historySHA256.MatchString(v.Str())
}

// ReadRunHistoryHead reads a detached, closed history head, separately from mutable execution schemas.
func ReadRunHistoryHead(raw []byte) (RunHistoryHead, error) {
	var out RunHistoryHead
	v, err := readCanonicalHistoryJSON(raw, HistoryObjectBytes)
	if err != nil || !snapshotObject(v, []string{"version", "runId", "revision", "executionRevision", "commitCount", "eventCount", "root", "coverage", "checkpoint"}, "context", "metadata") || v.Get("version").Num() != 1 || !historyID(v.Get("runId")) || !historyID(v.Get("revision")) || !historyID(v.Get("executionRevision")) || !snapshotCount(v.Get("commitCount"), 1) || v.Get("commitCount").Num() > HistoryMaxRows || !snapshotCount(v.Get("eventCount"), 0) || v.Get("eventCount").Num() > HistoryMaxEvents || !historySHA256.MatchString(v.Get("root").Str()) || !oneOf(v.Get("coverage"), "legacy-incomplete", "recording-incomplete", "complete") || !historyNullableRef(v.Get("checkpoint")) || v.Has("context") && !historyContextValue(v.Get("context")) {
		return out, &HistoryInputInvalid{}
	}
	if v.Has("metadata") {
		m := v.Get("metadata")
		if !snapshotObject(m, []string{"version", "runId", "revision", "executionRevision", "count", "root"}) || m.Get("version").Num() != 1 || m.Get("runId").Str() != v.Get("runId").Str() || m.Get("executionRevision").Str() != v.Get("executionRevision").Str() || !historyID(m.Get("revision")) || !snapshotCount(m.Get("count"), 1) || m.Get("count").Num()+v.Get("commitCount").Num() > HistoryMaxRows || !historySHA256.MatchString(m.Get("root").Str()) {
			return out, &HistoryInputInvalid{}
		}
	}
	if json.Unmarshal(raw, &out) != nil {
		return out, &HistoryInputInvalid{}
	}
	return out, nil
}
func ReadRunHistoryEntry(raw []byte) (RunHistoryEntry, error) {
	var out RunHistoryEntry
	v, err := readCanonicalHistoryJSON(raw, HistoryObjectBytes)
	refs := func(v *Value) bool {
		if !v.IsArray() || v.Len() > 256 {
			return false
		}
		for _, r := range v.Items() {
			if !historyRefValue(r) {
				return false
			}
		}
		return true
	}
	if err != nil || !snapshotObject(v, []string{"version", "sequence", "eventStart", "eventCount", "commitId", "executionRevision", "recordedAt", "previousDigest", "checkpoint", "events", "observations", "digest"}, "context") || v.Get("version").Num() != 1 || !snapshotCount(v.Get("sequence"), 1) || !snapshotCount(v.Get("eventStart"), 1) || !snapshotCount(v.Get("eventCount"), 0) || !historyID(v.Get("commitId")) || !historyID(v.Get("executionRevision")) || !historyTimestamp(v.Get("recordedAt")) || !historyNullableDigest(v.Get("previousDigest")) || !historyNullableRef(v.Get("checkpoint")) || !refs(v.Get("events")) || v.Get("eventCount").Num() != float64(v.Get("events").Len()) || !refs(v.Get("observations")) || v.Has("context") && !historyContextValue(v.Get("context")) || HistoryBytesDigest([]byte(CanonicalJSON(recordWithoutDigest(v)))) != v.Get("digest").Str() {
		return out, &HistoryInputInvalid{}
	}
	if json.Unmarshal(raw, &out) != nil {
		return out, &HistoryInputInvalid{}
	}
	return out, nil
}
func ReadHistoryMetadataEntry(raw []byte) (HistoryMetadataEntry, error) {
	var out HistoryMetadataEntry
	v, err := readCanonicalHistoryJSON(raw, StorageTransactionBytes)
	if err != nil || !snapshotObject(v, []string{"version", "executionRevision", "commitSequence", "sequence", "recordedAt", "previousDigest", "record", "digest"}) || v.Get("version").Num() != 1 || !historyID(v.Get("executionRevision")) || !snapshotCount(v.Get("commitSequence"), 1) || !snapshotCount(v.Get("sequence"), 1) || !historyTimestamp(v.Get("recordedAt")) || !historyNullableDigest(v.Get("previousDigest")) || HistoryBytesDigest([]byte(CanonicalJSON(recordWithoutDigest(v)))) != v.Get("digest").Str() {
		return out, &HistoryInputInvalid{}
	}
	if _, err = ReadHistoryMetadata([]byte(CanonicalJSON(v.Get("record")))); err != nil {
		return out, err
	}
	if json.Unmarshal(raw, &out) != nil {
		return out, &HistoryInputInvalid{}
	}
	return out, nil
}

// RetainedRunHistory exposes copied evidence, without any store, resume or write capability.
type RetainedRunHistory struct {
	head     RunHistoryHead
	commits  []RunHistoryEntry
	metadata []HistoryMetadataEntry
	objects  map[string][]byte
}

func (h *RetainedRunHistory) Head() RunHistoryHead             { return historyClone(h.head) }
func (h *RetainedRunHistory) Commits() []RunHistoryEntry       { return historyClone(h.commits) }
func (h *RetainedRunHistory) Metadata() []HistoryMetadataEntry { return historyClone(h.metadata) }
func (h *RetainedRunHistory) ReadObject(ref HistoryObjectRef) ([]byte, error) {
	value := h.objects[ref.Digest]
	if !validHistoryRef(ref) || len(value) != ref.Bytes {
		return nil, &HistoryInputInvalid{}
	}
	return bytes.Clone(value), nil
}

// ReadRetainedRunHistory validates a pinned composite view and rereads its head before returning.
func ReadRetainedRunHistory(ctx context.Context, store HistoryRunStore, runID string) (*RetainedRunHistory, error) {
	invalid := func() (*RetainedRunHistory, error) { return nil, &HistoryInputInvalid{} }
	if !validStorageID(runID) || store == nil || store.HistoryVersion() != 1 {
		return invalid()
	}
	initial, err := store.ReadHistoryHead(ctx, runID)
	if err != nil {
		return nil, err
	}
	if initial == nil {
		return invalid()
	}
	raw, err := historyJSONBytes(initial)
	if err != nil {
		return invalid()
	}
	head, err := ReadRunHistoryHead(raw)
	if err != nil || head.RunID != runID {
		return invalid()
	}
	out := &RetainedRunHistory{head: head, commits: []RunHistoryEntry{}, metadata: []HistoryMetadataEntry{}, objects: map[string][]byte{}}
	total := 0
	visible := map[string]bool{}
	load := func(ref HistoryObjectRef, resource HistoryResource, revision string) ([]byte, error) {
		if !validHistoryRef(ref) {
			return nil, &HistoryInputInvalid{}
		}
		key := historyInputText([]string{resource.Kind, resource.ID, revision, ref.Digest})
		prior, exists := out.objects[ref.Digest]
		if exists {
			if len(prior) != ref.Bytes {
				return nil, &HistoryInputInvalid{}
			}
			if visible[key] {
				return prior, nil
			}
		}
		if !exists && (len(out.objects) >= 50000 || total+ref.Bytes > HistoryTotalBytes) {
			return nil, &HistoryInputInvalid{}
		}
		page, err := store.ReadHistoryObject(ctx, resource, revision, ref.Digest, 0, HistoryObjectBytes)
		if err != nil {
			return nil, err
		}
		if page.Digest != ref.Digest || page.Total != ref.Bytes || page.Offset != 0 || page.NextOffset != ref.Bytes || page.HasMore || len(page.Value) != ref.Bytes || HistoryBytesDigest(page.Value) != ref.Digest {
			return nil, &HistoryInputInvalid{}
		}
		value := bytes.Clone(page.Value)
		visible[key] = true
		if !exists {
			out.objects[ref.Digest] = value
			total += len(value)
		}
		return value, nil
	}
	loadRun := func(ref HistoryObjectRef) ([]byte, error) {
		return load(ref, HistoryResource{Kind: "run", ID: runID}, head.Revision)
	}
	loadChunk := func(ref HistoryObjectRef) ([]byte, error) {
		raw, err := loadRun(ref)
		if err != nil {
			return nil, err
		}
		manifest, err := readHistoryChunkManifest(raw)
		if err != nil {
			return nil, err
		}
		full := []byte{}
		for _, part := range manifest.Parts {
			raw, err = loadRun(part)
			if err != nil {
				return nil, err
			}
			full = append(full, raw...)
		}
		if HistoryBytesDigest(full) != manifest.Digest {
			return nil, &HistoryInputInvalid{}
		}
		return full, nil
	}
	refValue := func(v *Value) HistoryObjectRef { var ref HistoryObjectRef; json.Unmarshal(v.JSON(), &ref); return ref }
	previous := ""
	events := 0
	for len(out.commits) < head.CommitCount {
		after := len(out.commits)
		page, err := store.ReadHistoryPage(ctx, runID, head.Revision, after, 100)
		if err != nil {
			return nil, err
		}
		if page.Revision != head.Revision || page.AfterSequence != after || page.Total != head.CommitCount || len(page.Items) < 1 || len(page.Items) > 100 || page.NextSequence != after+len(page.Items) || page.NextSequence > head.CommitCount || page.HasMore != (page.NextSequence < head.CommitCount) {
			return invalid()
		}
		for _, input := range page.Items {
			if len(input.Events) > 256 || len(input.Observations) > 256 {
				return invalid()
			}
			raw, err := historyJSONBytes(input)
			if err != nil {
				return invalid()
			}
			row, err := ReadRunHistoryEntry(raw)
			if err != nil {
				return nil, err
			}
			prior := ""
			if row.PreviousDigest != nil {
				prior = *row.PreviousDigest
			}
			if row.Sequence != len(out.commits)+1 || prior != previous || row.EventStart != events+1 {
				return invalid()
			}
			previous = row.Digest
			events += row.EventCount
			out.commits = append(out.commits, row)
			if row.Checkpoint != nil {
				raw, err := loadRun(*row.Checkpoint)
				if err != nil {
					return nil, err
				}
				v, e := readHistoryJSON(raw, HistoryObjectBytes, true, true)
				if e != nil {
					return invalid()
				}
				cp, e := ReadExecution(v)
				if e != nil || cp == nil || cp.ID != runID {
					return invalid()
				}
			}
			if source := row.Context; source != nil {
				resource := HistoryResource{Kind: "chat", ID: source.ChatID}
				raw, err := load(source.Manifest, resource, source.Revision)
				if err != nil {
					return nil, err
				}
				manifest, err := readHistoryChunkManifest(raw)
				if err != nil {
					return nil, err
				}
				full := []byte{}
				for _, ref := range manifest.Parts {
					part, err := load(ref, resource, source.Revision)
					if err != nil {
						return nil, err
					}
					full = append(full, part...)
				}
				if HistoryBytesDigest(full) != manifest.Digest {
					return invalid()
				}
				saved, err := ReadChatContextSnapshot(full)
				if err != nil {
					return nil, err
				}
				open := false
				if source.Turn == nil || *source.Turn > saved.Turns {
					return invalid()
				}
				for _, turn := range saved.Open {
					open = open || turn.Number == *source.Turn
				}
				if open == source.Closed || source.PrivateScope != "" && (!source.Closed || source.PrivateScope == saved.Scope) {
					return invalid()
				}
			}
			for _, ref := range row.Events {
				raw, err := loadRun(ref)
				if err != nil {
					return nil, err
				}
				if _, err = readHistoryJSON(raw, HistoryObjectBytes, true, true); err != nil {
					return invalid()
				}
				envelope, e := ReadStoredRunEvent(raw)
				if e != nil {
					return invalid()
				}
				if envelope.Scope.Task == "" && envelope.Scope.Execution != runID || envelope.Scope.Task != "" && envelope.Scope.Execution != envelope.Scope.Task {
					return invalid()
				}
				if envelope.Event.ExecutionCheckpoint != nil && envelope.Event.ExecutionCheckpoint.Checkpoint.ID != envelope.Scope.Execution || envelope.Event.BudgetPaused != nil && envelope.Event.BudgetPaused.Checkpoint.ID != envelope.Scope.Execution {
					return invalid()
				}
			}
			for _, ref := range row.Observations {
				raw, err := loadRun(ref)
				if err != nil {
					return nil, err
				}
				v, err := ReadHistoryObservation(raw)
				if err != nil {
					return nil, err
				}
				keys := []string{}
				switch v.Get("kind").Str() {
				case "original":
					keys = []string{"value"}
				case "compaction":
					keys = []string{"originals", "replacement", "prior"}
				case "request":
					keys = []string{"request", "compactions", "originals"}
				case "relation":
					keys = []string{"evidence"}
				}
				for _, key := range keys {
					if key == "value" || key == "replacement" || key == "request" {
						raw, err := loadChunk(refValue(v.Get(key)))
						if err != nil {
							return nil, err
						}
						if key == "request" {
							_, err = readHistoryRequestJSON(raw)
						} else {
							_, err = readHistorySourceJSON(raw)
						}
						if err != nil {
							return nil, err
						}
						continue
					}

					value := v.Get(key)
					if value.IsArray() {
						for _, ref := range value.Items() {
							if _, err = loadRun(refValue(ref)); err != nil {
								return nil, err
							}
						}
					} else {
						if _, err = loadRun(refValue(value)); err != nil {
							return nil, err
						}
					}
				}
			}
		}
	}
	last := out.commits[len(out.commits)-1]
	if events != head.EventCount || previous != head.Root || historyInputText(last.Checkpoint) != historyInputText(head.Checkpoint) || last.ExecutionRevision != head.ExecutionRevision || historyInputText(last.Context) != historyInputText(head.Context) {
		return invalid()
	}
	recorded := map[string]int{}
	roots := []HistoryObjectRef{}
	for _, row := range out.commits {
		for _, ref := range row.Observations {
			if recorded[ref.Digest] == 0 {
				recorded[ref.Digest] = row.Sequence
			}
			roots = append(roots, ref)
		}
	}
	if _, err = historyObservationClosure(roots, func(ref HistoryObjectRef) (HistoryObject, error) {
		raw, err := out.ReadObject(ref)
		return HistoryObject{HistoryObjectRef: ref, Value: raw}, err
	}, recorded); err != nil {
		return nil, err
	}
	count := 0
	if head.Metadata != nil {
		count = head.Metadata.Count
	}
	previous = ""
	for len(out.metadata) < count {
		after := len(out.metadata)
		page, err := store.ReadHistoryMetadataPage(ctx, runID, head.Revision, after, 100)
		if err != nil {
			return nil, err
		}
		if page.Revision != head.Revision || page.AfterSequence != after || page.Total != count || len(page.Items) < 1 || len(page.Items) > 100 || page.NextSequence != after+len(page.Items) || page.NextSequence > count || page.HasMore != (page.NextSequence < count) {
			return invalid()
		}
		for _, input := range page.Items {
			raw, err := historyJSONBytes(input)
			if err != nil {
				return invalid()
			}
			row, err := ReadHistoryMetadataEntry(raw)
			if err != nil {
				return nil, err
			}
			prior := ""
			if row.PreviousDigest != nil {
				prior = *row.PreviousDigest
			}
			if row.Sequence != len(out.metadata)+1 || prior != previous || row.CommitSequence > len(out.commits) || out.commits[row.CommitSequence-1].ExecutionRevision != row.ExecutionRevision {
				return invalid()
			}
			previous = row.Digest
			out.metadata = append(out.metadata, row)
			if row.Record.Kind == "capture" {
				raw, e := loadRun(*row.Record.Capture)
				if e != nil {
					return nil, e
				}
				capture, e := ReadRunCapture(raw)
				if e != nil {
					return nil, e
				}
				for _, file := range capture.Files {
					raw, e = loadRun(file.Manifest)
					if e != nil {
						return nil, e
					}
					manifest, e := ReadHistoryChunkManifest(raw)
					if e != nil {
						return nil, e
					}
					for _, part := range manifest.Parts {
						if _, e = loadRun(part); e != nil {
							return nil, e
						}
					}
				}
			} else if row.Record.Candidate != nil {
				raw, e := loadRun(*row.Record.Candidate)
				if e != nil {
					return invalid()
				}
				candidate, e := ReadCorpusCandidate(raw)
				if e != nil || validateCandidateFeedback(candidate, *row.Record.Feedback) != nil {
					return invalid()
				}
			}
		}
	}
	root := ""
	if head.Metadata != nil && head.Metadata.Root != nil {
		root = *head.Metadata.Root
	}
	if previous != root {
		return invalid()
	}
	if err := validateHistoryRelations(out.commits, func(ref HistoryObjectRef, _ string) (*Value, error) {
		raw, e := out.ReadObject(ref)
		if e != nil {
			return nil, e
		}
		return readHistoryJSON(raw, HistoryObjectBytes, true, true)
	}, nil); err != nil {
		return nil, err
	}
	final, err := store.ReadHistoryHead(ctx, runID)
	if err != nil {
		return nil, err
	}
	if final == nil {
		return nil, &HistoryChanged{}
	}
	raw, err = historyJSONBytes(final)
	if err != nil {
		return invalid()
	}
	checked, err := ReadRunHistoryHead(raw)
	if err != nil {
		return nil, err
	}
	if historyInputText(checked) != historyInputText(head) {
		return nil, &HistoryChanged{}
	}
	return out, nil
}
