package runcontext

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/internal/historycapture"
)

// PreparedCapturePublication is opaque and bound to the original store capability.
// Copying it or constructing its zero value never supplies a publisher authority.
type PreparedCapturePublication struct {
	identity  *PreparedCapturePublication
	store     HistoryRunStore
	source    RunHistoryHead
	commit    HistoryMetadataCommit
	capture   HistoryObjectRef
	objects   map[string][]byte
	files     map[string][]byte
	id        string
	mu        sync.Mutex
	attempted bool
	delivery  string
}
type CapturePublicationOptions struct {
	CommitID string
	Manifest CorpusManifest
	Members  PolicyMembers
}
type CapturePublicationResult struct {
	Capture    string
	Delivery   string
	Head       *RunHistoryHead
	CaptureRef *HistoryObjectRef
}

func captureCheckpointMatches(a, b *Value) bool {
	return captureDisplayMatches(a, b) && archiveEqual(archiveTaskIdentity("checkpoint", a), archiveTaskIdentity("checkpoint", b))
}
func capturePublicationEvents(view *RetainedRunHistory) ([]captureEvent, error) {
	out := []captureEvent{}
	for _, c := range view.Commits() {
		for _, ref := range c.Events {
			raw, e := view.ReadObject(ref)
			if e != nil {
				return nil, e
			}
			envelope, e := ReadStoredRunEvent(raw)
			if e != nil {
				return nil, e
			}
			v, e := ParseJSON(raw)
			if e != nil {
				return nil, e
			}
			out = append(out, captureEvent{scope: envelope.Scope, event: envelope.Event, value: v.Get("event"), sequence: c.Sequence})
		}
	}
	return out, nil
}
func capturePublicationTools(events []captureEvent) (*Value, error) {
	calls := map[string]*Value{}
	out := Array()
	for _, r := range events {
		e := r.value
		if !oneOf(e.Get("type"), "tool_call", "tool_result") {
			continue
		}
		if !historyID(e.Get("callId")) {
			return nil, &ReplayUnavailable{}
		}
		key := historyInputText([]any{r.scope, e.Get("callId").Str()})
		if e.Get("type").Str() == "tool_call" {
			if !historyID(e.Get("callId")) || !e.Get("args").IsObject() || calls[key] != nil {
				return nil, &ReplayUnavailable{}
			}
			calls[key] = e
		} else if e.Get("type").Str() == "tool_result" {
			call := calls[key]
			if !historyID(e.Get("callId")) || call == nil || call.Get("name").Str() != e.Get("name").Str() {
				return nil, &ReplayUnavailable{}
			}
			delete(calls, key)
			result := ObjectOf("content", Array(ObjectOf("type", String("text"), "text", e.Get("text"))), "isError", e.Get("isError"))
			if e.Has("structured") {
				result.Set("structuredContent", e.Get("structured"))
			}
			out.Append(ObjectOf("name", e.Get("name"), "args", call.Get("args"), "result", result))
		}
	}
	if len(calls) > 0 {
		return nil, &ReplayUnavailable{}
	}
	return out, nil
}

type capturePublicationMaterial struct {
	objects map[string][]byte
	files   map[string][]byte
	ref     HistoryObjectRef
	record  HistoryMetadata
	checked *CorpusEntry
}

func capturePublicationFiles(view *RetainedRunHistory, value *Value, identities []HistoryCaptureIdentity, original bool) (*capturePublicationMaterial, error) {
	unavailable := func() (*capturePublicationMaterial, error) { return nil, &ReplayUnavailable{} }
	source, commits := view.Head(), view.Commits()
	files := map[string][]byte{}
	for name, key := range map[string]string{"manifest.json": "manifest", "requests.json": "requests", "tool-results.json": "toolResults", "events.json": "events", "checkpoint.json": "checkpoint", "expected.json": "expected"} {
		files[name] = []byte(CanonicalJSON(value.Get(key)))
	}
	objects := map[string][]byte{}
	names := []string{}
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	fileRefs := []HistoryCaptureFile{}
	for _, name := range names {
		manifest, parts, e := ChunkHistoryValue(files[name])
		if e != nil {
			return unavailable()
		}
		for _, o := range append(parts, manifest) {
			objects[o.Digest] = append([]byte(nil), o.Value...)
		}
		fileRefs = append(fileRefs, HistoryCaptureFile{Name: name, Manifest: manifest.HistoryObjectRef})
	}
	capture := RunCapture{EventProjection: "transcript-v1", Version: 1, RunID: source.RunID, ExecutionRevision: source.ExecutionRevision, Checkpoint: *source.Checkpoint, Requests: identities, Files: fileRefs, Redacted: true}
	captureRaw, e := canonicalBytes(capture)
	if e != nil {
		return unavailable()
	}
	if _, e = ReadRunCapture(captureRaw); e != nil {
		return unavailable()
	}
	object, e := NewHistoryObject(captureRaw)
	if e != nil {
		return unavailable()
	}
	objects[object.Digest] = object.Value
	ref := object.HistoryObjectRef
	count := 0
	var previous *string
	if source.Metadata != nil {
		count = source.Metadata.Count
		previous = source.Metadata.Root
	}
	record := HistoryMetadata{Version: 1, Kind: "capture", Capture: &ref}
	entry := HistoryMetadataEntry{Version: 1, ExecutionRevision: source.ExecutionRevision, CommitSequence: source.CommitCount, Sequence: count + 1, RecordedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z"), PreviousDigest: previous, Record: record}
	entryValue, _ := ParseJSON([]byte(historyInputText(entry)))
	entry.Digest = HistoryBytesDigest([]byte(CanonicalJSON(recordWithoutDigest(entryValue))))
	root := entry.Digest
	head := historyClone(source)
	head.Revision = "capture-preflight"
	head.Metadata = &HistoryMetadataHead{Version: 1, RunID: source.RunID, Revision: "capture-preflight", ExecutionRevision: source.ExecutionRevision, Count: count + 1, Root: &root}
	checked, e := readCapturedCorpus(capturePort{head: head, commits: commits, metadata: append(view.Metadata(), entry), read: func(ref HistoryObjectRef, _ ...string) ([]byte, error) {
		if b, ok := objects[ref.Digest]; ok {
			return append([]byte(nil), b...), nil
		}
		return view.ReadObject(ref)
	}}, original, !original, nil)
	if e != nil {
		return unavailable()
	}
	return &capturePublicationMaterial{objects: objects, files: files, ref: ref, record: record, checked: checked}, nil
}

// PrepareCapturePublication consumes only an enrolled opaque seal. It never mints enrollment
// from an input checkpoint, archive, caller marker or reconstructed physical frame.
func PrepareCapturePublication(ctx context.Context, store HistoryRunStore, seal *historycapture.Seal, options CapturePublicationOptions) (*PreparedCapturePublication, error) {
	unavailable := func() (*PreparedCapturePublication, error) { return nil, &ReplayUnavailable{} }
	material, e := seal.Consume(store)
	if e != nil || !validStorageID(options.CommitID) {
		return unavailable()
	}
	view, e := ReadRetainedRunHistory(ctx, store, material.Terminal.RunID)
	if e != nil {
		return unavailable()
	}
	source := view.Head()
	if source.Checkpoint == nil || source.ExecutionRevision != material.Terminal.ExecutionRevision || source.Checkpoint.Digest != material.Terminal.Checkpoint.Digest || source.Checkpoint.Bytes != material.Terminal.Checkpoint.Bytes {
		return unavailable()
	}
	for _, row := range view.Metadata() {
		if row.Record.Kind == "capture" && row.ExecutionRevision == source.ExecutionRevision {
			return unavailable()
		}
	}
	cpRaw, e := view.ReadObject(*source.Checkpoint)
	if e != nil {
		return unavailable()
	}
	cpValue, e := ParseJSON(cpRaw)
	if e != nil {
		return unavailable()
	}
	cp, e := ReadExecution(cpValue)
	if e != nil || cp == nil || cp.Status != material.Terminal.Status || (cp.Status != "complete" && cp.Status != "stopped") {
		return unavailable()
	}
	commits := view.Commits()
	for _, row := range material.Rows {
		a := row.Admission
		if a.Sequence < 1 || a.Sequence > len(commits) {
			return unavailable()
		}
		c := commits[a.Sequence-1]
		if c.CommitID != a.CommitID || a.Ordinal < 0 || a.Ordinal >= len(c.Observations) || c.Observations[a.Ordinal].Digest != a.Expectation.Digest || c.Observations[a.Ordinal].Bytes != a.Expectation.Bytes {
			return unavailable()
		}
		raw, e := view.ReadObject(c.Observations[a.Ordinal])
		if e != nil {
			return unavailable()
		}
		o, e := ReadHistoryObservation(raw)
		if e != nil || o.Get("kind").Str() != "capture-request" {
			return unavailable()
		}
		var identity HistoryCaptureIdentity
		json.Unmarshal(o.JSON(), &identity)
		identity.Kind = o.Get("captureKind").Str()
		nativeIdentity := HistoryCaptureIdentity{Scope: DurableScope{Execution: a.Identity.Scope.Execution, Role: a.Identity.Scope.Role, Task: a.Identity.Scope.Task}, DispatchID: a.Identity.DispatchID, Kind: a.Identity.Kind, Source: a.Identity.Source, Tool: a.Identity.Tool, RequestDigest: a.Identity.RequestDigest, Surface: a.Identity.Surface}
		if !archiveEqual(identity, nativeIdentity) || c.Checkpoint == nil {
			return unavailable()
		}
		raw, e = view.ReadObject(*c.Checkpoint)
		if e != nil {
			return unavailable()
		}
		v, e := ParseJSON(raw)
		if e != nil {
			return unavailable()
		}
		found := false
		for _, claim := range v.Get("durable").Get("dispatches").Items() {
			if claim.Get("id").Str() == a.Identity.DispatchID {
				found = claim.Get("request").Str() == a.Request
			}
		}
		if !found {
			return unavailable()
		}
	}
	events, e := capturePublicationEvents(view)
	if e != nil {
		return unavailable()
	}
	visible := Array()
	visibleEvents := []captureEvent{}
	for _, row := range events {
		if row.scope.Execution == source.RunID && row.scope.Task == "" && row.scope.Role == "author" && !oneOf(row.value.Get("type"), "end", "recorded", "execution_checkpoint") {
			visible.Append(row.value)
			visibleEvents = append(visibleEvents, row)
		}
	}
	tools, e := capturePublicationTools(events)
	if e != nil {
		return unavailable()
	}
	requests := Array()
	for _, row := range material.Rows {
		v, e := ParseJSON(row.Frame)
		if e != nil {
			return unavailable()
		}
		requests.Append(v)
	}
	rawManifest, e := canonicalBytes(options.Manifest)
	if e != nil {
		return unavailable()
	}
	manifest, e := ParseJSON(rawManifest)
	if e != nil {
		return unavailable()
	}
	// The existing projection accepts the validated event union.
	eventContracts := make([]contract.RunEvent, len(visibleEvents))
	for i, row := range visibleEvents {
		eventContracts[i] = row.event
	}
	expected, e := CorpusExpected(eventContracts)
	if e != nil {
		return unavailable()
	}
	expectedRaw, _ := canonicalBytes(expected)
	expectedValue, e := ParseJSON(expectedRaw)
	if e != nil {
		return unavailable()
	}
	raw := ObjectOf("manifest", manifest, "requests", requests, "toolResults", tools, "events", visible, "checkpoint", cpValue, "expected", expectedValue)
	identities := []HistoryCaptureIdentity{}
	for i, row := range material.Rows {
		a := row.Admission.Identity
		identities = append(identities, HistoryCaptureIdentity{Scope: DurableScope{Execution: a.Scope.Execution, Role: a.Scope.Role, Task: a.Scope.Task}, DispatchID: a.DispatchID, Kind: a.Kind, Source: a.Source, Tool: a.Tool, RequestDigest: a.RequestDigest, Surface: a.Surface, CaptureIndex: i})
	}
	if _, e = capturePublicationFiles(view, raw, identities, true); e != nil {
		return unavailable()
	}
	members := PolicyMembers{}
	for k, v := range options.Members {
		members[k] = v
	}
	members["outputGuard"] = PolicyMember{Value: "redact"}
	guard, e := NewOutputGuard(members, func() map[string]int64 { return map[string]int64{} })
	if e != nil {
		return unavailable()
	}
	cleanRaw, e := guard.JSON(json.RawMessage(CanonicalJSON(raw)), "message", false)
	if e != nil {
		return unavailable()
	}
	clean, e := ParseJSON(cleanRaw)
	if e != nil || !captureDisplayMatches(raw, clean) || !captureCheckpointMatches(cpValue, clean.Get("checkpoint")) {
		return unavailable()
	}
	held, e := capturePublicationFiles(view, clean, identities, false)
	if e != nil {
		return unavailable()
	}
	objects, files, ref, record, checked := held.objects, held.files, held.ref, held.record, held.checked
	latest, e := store.ReadHistoryHead(ctx, source.RunID)
	if e != nil {
		return unavailable()
	}
	if latest == nil || !archiveEqual(*latest, source) {
		return nil, &HistoryChanged{}
	}
	batch := []HistoryObject{}
	batchBytes, total := 0, 0
	for digest, value := range objects {
		total += len(value)
		if total > HistoryTotalBytes {
			return unavailable()
		}
		if len(batch) >= 256 || batchBytes+len(value) > 8*1024*1024 {
			if e = store.StageHistoryObjects(ctx, HistoryResource{Kind: "run", ID: source.RunID}, batch); e != nil {
				return unavailable()
			}
			batch = []HistoryObject{}
			batchBytes = 0
		}
		batch = append(batch, HistoryObject{HistoryObjectRef: HistoryObjectRef{Digest: digest, Bytes: len(value)}, Value: append([]byte(nil), value...)})
		batchBytes += len(value)
	}
	if len(batch) > 0 {
		if e = store.StageHistoryObjects(ctx, HistoryResource{Kind: "run", ID: source.RunID}, batch); e != nil {
			return unavailable()
		}
	}
	prepared := &PreparedCapturePublication{store: store, source: source, commit: HistoryMetadataCommit{RunID: source.RunID, ExpectedHistoryRevision: source.Revision, ExpectedMetadataRevision: source.MetadataRevision(), CommitID: options.CommitID, Record: record}, capture: ref, objects: objects, files: files, id: checked.Manifest.ID}
	prepared.identity = prepared
	return prepared, nil
}

// CommitCapturePublication supports explicit same-prepared reconciliation only. Unknown storage
// acknowledgement never invokes a sink; an external sink is attempted at most once per preparation.
func CommitCapturePublication(ctx context.Context, store HistoryRunStore, prepared *PreparedCapturePublication, sink CorpusSink) (CapturePublicationResult, error) {
	unknown := func() (CapturePublicationResult, error) {
		return CapturePublicationResult{Capture: "unknown", Delivery: "not-attempted"}, nil
	}
	if prepared == nil || prepared.identity != prepared || prepared.store != store {
		return CapturePublicationResult{}, &ReplayUnavailable{}
	}
	source, commit := historyClone(prepared.source), historyClone(prepared.commit)
	head, e := store.CommitHistoryMetadata(ctx, commit)
	if e != nil {
		var conflict *StorageConflict
		var invalid *HistoryInputInvalid
		if errors.As(e, &conflict) || errors.As(e, &invalid) {
			return CapturePublicationResult{}, e
		}
		return unknown()
	}
	checked, e := ReadRunHistoryHead([]byte(historyInputText(head)))
	if e != nil || !archiveEqual(checked, head) {
		return unknown()
	}
	head = checked
	before, after := source, head
	before.Revision = ""
	before.Metadata = nil
	after.Revision = ""
	after.Metadata = nil
	count := 0
	var previous *string
	if source.Metadata != nil {
		count = source.Metadata.Count
		previous = source.Metadata.Root
	}
	if !archiveEqual(before, after) || head.Revision == source.Revision || head.Metadata == nil || head.Metadata.Count != count+1 || source.Metadata != nil && head.Metadata.Revision == source.Metadata.Revision {
		return unknown()
	}
	page, e := store.ReadHistoryMetadataPage(ctx, source.RunID, head.Revision, count, 1)
	if e != nil || page.Revision != head.Revision || page.AfterSequence != count || len(page.Items) != 1 || page.NextSequence != count+1 || page.HasMore || page.Total != head.Metadata.Count {
		return unknown()
	}
	row, e := ReadHistoryMetadataEntry([]byte(historyInputText(page.Items[0])))
	if e != nil || row.Sequence != count+1 || !archiveEqual(row.PreviousDigest, previous) || head.Metadata.Root == nil || row.Digest != *head.Metadata.Root || row.ExecutionRevision != source.ExecutionRevision || row.CommitSequence != source.CommitCount || !archiveEqual(row.Record, commit.Record) {
		return unknown()
	}
	for digest, value := range prepared.objects {
		object, e := store.ReadHistoryObject(ctx, HistoryResource{Kind: "run", ID: source.RunID}, head.Revision, digest, 0, HistoryObjectBytes)
		if e != nil || object.Digest != digest || object.Total != len(value) || object.Offset != 0 || object.NextOffset != len(value) || object.HasMore || !bytes.Equal(object.Value, value) || HistoryBytesDigest(object.Value) != digest {
			return unknown()
		}
	}
	delivery := "not-requested"
	if sink != nil {
		prepared.mu.Lock()
		attempt := !prepared.attempted
		if attempt {
			prepared.attempted = true
			prepared.delivery = "unknown"
		}
		delivery = prepared.delivery
		prepared.mu.Unlock()
		if attempt {
			files := map[string][]byte{}
			for k, v := range prepared.files {
				files[k] = append([]byte(nil), v...)
			}
			if sink.WriteCorpus(prepared.id, files) == nil {
				delivery = "delivered"
			} else {
				delivery = "unknown"
			}
			prepared.mu.Lock()
			prepared.delivery = delivery
			prepared.mu.Unlock()
		}
	}
	ref := prepared.capture
	return CapturePublicationResult{Capture: "committed", Delivery: delivery, Head: &head, CaptureRef: &ref}, nil
}
