package runcontext

import (
	"bytes"
	"context"
	"crypto/rand"
)

type historyMetadataCommit struct {
	input string
	head  RunHistoryHead
}
type preparedHistoryMetadata struct {
	replay  bool
	publish func() RunHistoryHead
}

func (j *memoryHistoryJournal) prepareMetadata(input HistoryMetadataCommit) (preparedHistoryMetadata, error) {
	invalid := func() (preparedHistoryMetadata, error) { return preparedHistoryMetadata{}, &HistoryInputInvalid{} }
	if !validStorageID(input.RunID) || !validStorageID(input.ExpectedHistoryRevision) || !validStorageID(input.CommitID) || input.ExpectedMetadataRevision != nil && !validStorageID(*input.ExpectedMetadataRevision) {
		return invalid()
	}
	raw, err := historyJSONBytes(input.Record)
	if err != nil {
		return invalid()
	}
	record, err := ReadHistoryMetadata(raw)
	if err != nil {
		return invalid()
	}
	input.Record = *record
	text := historyInputText(input)
	key := historyInputText([]string{input.RunID, input.CommitID})
	if prior, ok := j.metadataCommits[key]; ok {
		if prior.input != text {
			return invalid()
		}
		return preparedHistoryMetadata{replay: true, publish: func() RunHistoryHead { return historyClone(prior.head) }}, nil
	}
	held := j.runs[input.RunID]
	if held == nil {
		return invalid()
	}
	head := held.head.head
	meta := head.Metadata
	if head.Revision != input.ExpectedHistoryRevision {
		return preparedHistoryMetadata{}, &StorageConflict{ExpectedRevision: input.ExpectedHistoryRevision, ActualRevision: head.Revision}
	}
	expected, actual := "", ""
	if input.ExpectedMetadataRevision != nil {
		expected = *input.ExpectedMetadataRevision
	}
	if meta != nil {
		actual = meta.Revision
	}
	if expected != actual {
		return preparedHistoryMetadata{}, &StorageConflict{ExpectedRevision: expected, ActualRevision: actual}
	}
	if len(held.entries)+len(held.metadata) >= HistoryMaxRows {
		return invalid()
	}
	resource := HistoryResource{Kind: "run", ID: input.RunID}
	refs := []HistoryObjectRef{}
	if record.Kind == "feedback" {
		feedback := record.Feedback
		if feedback.RunID != input.RunID {
			return invalid()
		}
		var previous *RunFeedback
		for _, entry := range held.metadata {
			if entry.Record.Kind == "feedback" && entry.Record.Feedback.ID == feedback.ID {
				previous = entry.Record.Feedback
			}
		}
		sequence := 1
		var digest *string
		if previous != nil {
			sequence = previous.Sequence + 1
			raw, _ := historyJSONBytes(previous)
			sum := HistoryBytesDigest(raw)
			digest = &sum
		}
		if feedback.Sequence != sequence || historyInputText(feedback.Previous) != historyInputText(digest) || previous != nil && historyInputText(previous.Target) != historyInputText(feedback.Target) {
			return invalid()
		}
		if record.Candidate != nil {
			object, e := j.resolve(resource, *record.Candidate)
			if e != nil {
				return invalid()
			}
			candidate, e := ReadCorpusCandidate(object.Value)
			if e != nil || validateCandidateFeedback(candidate, *feedback) != nil || candidate.SourceView != head.Revision {
				return invalid()
			}
			if head.Checkpoint == nil {
				return invalid()
			}
			cpObject, e := j.resolve(resource, *head.Checkpoint)
			if e != nil {
				return invalid()
			}
			cpValue, e := ParseJSON(cpObject.Value)
			if e != nil {
				return invalid()
			}
			checkpoint, e := ReadExecution(cpValue)
			report, err := ParseJSON(candidate.Review.Report)
			if e != nil || checkpoint == nil || err != nil || checkpoint.Status != "complete" && checkpoint.Status != "stopped" || report.Get("checkpointVersion").Num() != float64(checkpoint.Version) || report.Get("status").Str() != checkpoint.Status {
				return invalid()
			}
			refs = append(refs, *record.Candidate)
		}
		if feedback.Target.Kind == "assistant-message" {
			var ref *HistoryObjectRef
			for _, entry := range held.entries {
				if feedback.Target.EventSequence >= entry.EventStart && feedback.Target.EventSequence < entry.EventStart+entry.EventCount {
					copy := entry.Events[feedback.Target.EventSequence-entry.EventStart]
					ref = &copy
					break
				}
			}
			if ref == nil || ref.Digest != feedback.Target.EventDigest {
				return invalid()
			}
			object, err := j.resolve(resource, *ref)
			if err != nil {
				return invalid()
			}
			event, err := ReadStoredRunEvent(object.Value)
			if err != nil || event.Event.Message == nil {
				return invalid()
			}
		}
	} else {
		object, err := j.resolve(resource, *record.Capture)
		if err != nil {
			return invalid()
		}
		refs = append(refs, *record.Capture)
		capture, err := ReadRunCapture(object.Value)
		if err != nil || capture.RunID != input.RunID || capture.ExecutionRevision != head.ExecutionRevision || head.Checkpoint == nil || capture.Checkpoint != *head.Checkpoint {
			return invalid()
		}
		checkpointObject, err := j.resolve(resource, *head.Checkpoint)
		if err != nil {
			return invalid()
		}
		value, err := ParseJSON(checkpointObject.Value)
		if err != nil {
			return invalid()
		}
		checkpoint, err := ReadExecution(value)
		if err != nil || checkpoint == nil || checkpoint.Status != "complete" && checkpoint.Status != "stopped" {
			return invalid()
		}
		requests := map[string]*Value{}
		for _, entry := range held.entries {
			if entry.Checkpoint == nil {
				continue
			}
			object, err := j.resolve(resource, *entry.Checkpoint)
			if err != nil {
				return invalid()
			}
			v, err := ParseJSON(object.Value)
			if err != nil {
				return invalid()
			}
			if _, err = ReadExecution(v); err != nil {
				return invalid()
			}
			if durable := v.Get("durable"); durable != nil {
				for _, claim := range durable.Get("dispatches").Items() {
					if oneOf(claim.Get("kind"), "model", "agent") {
						requests[claim.Get("id").Str()] = claim
					}
				}
			}
		}
		if len(requests) != len(capture.Requests) {
			return invalid()
		}
		for _, row := range capture.Requests {
			claim := requests[row.DispatchID]
			if claim == nil || claim.Get("kind").Str() != row.Kind || claim.Get("source").Str() != row.Source || claim.Get("tool").Str() != row.Tool || claim.Get("requestDigest").Str() != row.RequestDigest || CanonicalJSON(claim.Get("scope")) != historyInputText(row.Scope) || claim.Get("state").Str() != "result" {
				return invalid()
			}
		}
		total := 0
		for _, file := range capture.Files {
			refs = append(refs, file.Manifest)
			object, err := j.resolve(resource, file.Manifest)
			if err != nil {
				return invalid()
			}
			manifest, err := ReadHistoryChunkManifest(object.Value)
			if err != nil {
				return invalid()
			}
			total += manifest.Bytes
			if total > HistoryTotalBytes {
				return invalid()
			}
			raw := make([]byte, 0, manifest.Bytes)
			for _, part := range manifest.Parts {
				refs = append(refs, part)
				object, err := j.resolve(resource, part)
				if err != nil {
					return invalid()
				}
				raw = append(raw, object.Value...)
			}
			if HistoryBytesDigest(raw) != manifest.Digest {
				return invalid()
			}
			if _, err = readHistoryJSON(raw, HistoryTotalBytes, true, true); err != nil {
				return invalid()
			}
		}
	}
	sequence := len(held.metadata) + 1
	recordedAt := j.now().UTC().Format("2006-01-02T15:04:05.000Z")
	var previous *string
	if meta != nil {
		previous = meta.Root
	}
	entry := HistoryMetadataEntry{Version: 1, ExecutionRevision: head.ExecutionRevision, CommitSequence: head.CommitCount, Sequence: sequence, RecordedAt: recordedAt, PreviousDigest: previous, Record: *record}
	raw, _ = historyJSONBytes(entry)
	value, _ := ParseJSON(raw)
	entry.Digest = HistoryBytesDigest([]byte(CanonicalJSON(recordWithoutDigest(value))))
	root := entry.Digest
	metadata := &HistoryMetadataHead{Version: 1, RunID: input.RunID, Revision: rand.Text(), ExecutionRevision: head.ExecutionRevision, Count: sequence, Root: &root}
	next := historyClone(head)
	next.Revision = rand.Text()
	next.Metadata = metadata
	view := historyView{head: next, length: len(held.entries), publication: held.head.publication + 1, metadataLength: sequence}
	return preparedHistoryMetadata{publish: func() RunHistoryHead {
		for _, ref := range refs {
			object := j.objects[resource][ref.Digest]
			if object.published == 0 {
				object.published = view.publication
			}
		}
		held.metadata = append(held.metadata, entry)
		held.views[next.Revision] = view
		held.head = view
		j.metadataCommits[key] = historyMetadataCommit{input: text, head: next}
		return historyClone(next)
	}}, nil
}
func (m *MemoryRunStore) CommitHistoryMetadata(ctx context.Context, input HistoryMetadataCommit) (RunHistoryHead, error) {
	// Detach and enforce object bounds before waiting for the containing original owner mutex.
	raw, err := historyJSONBytes(input.Record)
	if err != nil {
		return RunHistoryHead{}, &HistoryInputInvalid{}
	}
	record, err := ReadHistoryMetadata(bytes.Clone(raw))
	if err != nil {
		return RunHistoryHead{}, err
	}
	input.Record = *record
	if input.ExpectedMetadataRevision != nil {
		copy := *input.ExpectedMetadataRevision
		input.ExpectedMetadataRevision = &copy
	}
	if err = ctx.Err(); err != nil {
		return RunHistoryHead{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err = ctx.Err(); err != nil {
		return RunHistoryHead{}, err
	}
	prepared, err := m.history.prepareMetadata(input)
	if err != nil {
		return RunHistoryHead{}, err
	}
	if prepared.replay {
		return prepared.publish(), nil
	}
	if m.options.BeforeCommit != nil {
		if err = m.options.BeforeCommit(); err != nil {
			return RunHistoryHead{}, err
		}
	}
	if err = ctx.Err(); err != nil {
		return RunHistoryHead{}, err
	}
	head := prepared.publish()
	if m.options.AfterCommit != nil {
		if err = m.options.AfterCommit(); err != nil {
			return RunHistoryHead{}, err
		}
	}
	return head, nil
}
func (m *MemoryRunStore) ReadHistoryMetadataPage(ctx context.Context, id, revision string, after, limit int) (HistoryMetadataPage, error) {
	if err := ctx.Err(); err != nil {
		return HistoryMetadataPage{}, err
	}
	if !validStorageID(id) || !validStorageID(revision) || limit < 1 || limit > StoragePageLimit {
		return HistoryMetadataPage{}, &HistoryInputInvalid{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	held := m.history.runs[id]
	if held == nil {
		return HistoryMetadataPage{}, &HistoryChanged{}
	}
	view, ok := held.views[revision]
	if !ok {
		return HistoryMetadataPage{}, &HistoryChanged{}
	}
	if after < 0 || after > view.metadataLength {
		return HistoryMetadataPage{}, &HistoryInputInvalid{}
	}
	items := []HistoryMetadataEntry{}
	size := 0
	for _, row := range held.metadata[after:min(view.metadataLength, after+limit)] {
		raw, _ := historyJSONBytes(row)
		if size+len(raw) > StorageTransactionBytes {
			break
		}
		size += len(raw)
		items = append(items, historyClone(row))
	}
	next := after + len(items)
	return HistoryMetadataPage{Revision: revision, AfterSequence: after, Items: items, NextSequence: next, HasMore: next < view.metadataLength, Total: view.metadataLength}, nil
}
