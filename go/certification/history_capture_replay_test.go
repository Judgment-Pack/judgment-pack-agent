package certification

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/eino"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	capture "github.com/Judgment-Pack/judgment-pack-agent/go/internal/historycapture"
)

// Constructed native journal over handwritten seed frames. This does not claim live capture hooks.
func importedDerivedReplay(t *testing.T) *runcontext.CorpusReplayView {
	t.Helper()
	ctx := context.Background()
	root, e := RepositoryRoot()
	if e != nil {
		t.Fatal(e)
	}
	read := func(path string) *runcontext.Value {
		raw, e := os.ReadFile(filepath.Join(root, path))
		if e != nil {
			t.Fatal(e)
		}
		v, e := runcontext.ParseJSON(raw)
		if e != nil {
			t.Fatal(e)
		}
		return v
	}
	cp := read("contract/fixtures/history/task-identity.json").Get("checkpoint").Clone()
	seed := "corpus/model-recorded-tool-result/"
	frames := read(seed + "requests.json")
	var manifest runcontext.CorpusManifest
	if e = json.Unmarshal(read(seed+"manifest.json").JSON(), &manifest); e != nil {
		t.Fatal(e)
	}
	id := cp.Get("id").Str()
	scope := runcontext.ObjectOf("execution", runcontext.String(id), "role", runcontext.String("author"))
	store := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
	owner := new(int)
	collector, rootToken, e := capture.New(owner, id, capture.Scope{Execution: id, Role: "author"})
	if e != nil {
		t.Fatal(e)
	}
	if e = collector.BindPublisher(owner, store); e != nil {
		t.Fatal(e)
	}
	dispatches := runcontext.Array()
	objects := []runcontext.HistoryObject{}
	observations := []runcontext.HistoryObjectRef{}
	admissions := []capture.Admission{}
	stage := func(v *runcontext.Value) runcontext.HistoryObjectRef {
		object, e := runcontext.NewHistoryObject([]byte(runcontext.CanonicalJSON(v)))
		if e != nil {
			t.Fatal(e)
		}
		objects = append(objects, object)
		return object.HistoryObjectRef
	}
	for i, frame := range frames.Items() {
		requestValue := runcontext.ObjectOf("suffix", frame.Get("suffix"), "method", frame.Get("request").Get("method"), "body", frame.Get("request").Get("body"))
		request := runcontext.CanonicalJSON(requestValue)
		digest := runcontext.HistoryBytesDigest([]byte(request))
		dispatchID := fmt.Sprintf("replay-%d", i)
		fingerprint := runcontext.ObjectOf("kind", runcontext.String("model"), "scope", scope, "source", runcontext.String("provider"), "tool", runcontext.String("fixture"), "request", requestValue)
		dispatches.Append(runcontext.ObjectOf("id", runcontext.String(dispatchID), "scope", scope, "kind", runcontext.String("model"), "source", runcontext.String("provider"), "tool", runcontext.String("fixture"), "request", runcontext.String(request), "requestDigest", runcontext.String(digest), "fingerprint", runcontext.String(runcontext.HistoryBytesDigest([]byte(runcontext.CanonicalJSON(fingerprint)))), "state", runcontext.String("result"), "read", runcontext.Bool(true)))
		m, parts, e := runcontext.ChunkHistoryValue([]byte(request))
		if e != nil {
			t.Fatal(e)
		}
		objects = append(objects, m)
		objects = append(objects, parts...)
		ref := runcontext.ObjectOf("digest", runcontext.String(m.Digest), "bytes", runcontext.Number(float64(m.Bytes)))
		o := stage(runcontext.ObjectOf("version", runcontext.Number(1), "kind", runcontext.String("request"), "scope", scope, "dispatchId", runcontext.String(dispatchID), "source", runcontext.String("provider"), "tool", runcontext.String("fixture"), "request", ref, "originals", runcontext.Array(), "compactions", runcontext.Array()))
		expectation := stage(runcontext.ObjectOf("version", runcontext.Number(1), "kind", runcontext.String("capture-request"), "captureKind", runcontext.String("model"), "scope", scope, "dispatchId", runcontext.String(dispatchID), "source", runcontext.String("provider"), "tool", runcontext.String("fixture"), "requestDigest", runcontext.String(digest), "surface", runcontext.String("session-model")))
		observations = append(observations, o, expectation)
		admissions = append(admissions, capture.Admission{Identity: capture.Identity{Scope: capture.Scope{Execution: id, Role: "author"}, DispatchID: dispatchID, Kind: "model", Source: "provider", Tool: "fixture", RequestDigest: digest, Surface: "session-model"}, Sequence: 1, Ordinal: len(observations) - 1, CommitID: "constructed", Expectation: capture.Ref{Digest: expectation.Digest, Bytes: expectation.Bytes}, Request: request})
	}
	cp.Set("durable", runcontext.ObjectOf("dispatches", dispatches, "children", runcontext.Array(), "observations", runcontext.Array()))
	if e = store.StageHistoryObjects(ctx, runcontext.HistoryResource{Kind: "run", ID: id}, objects); e != nil {
		t.Fatal(e)
	}
	events := [][]byte{}
	for _, event := range read(seed + "events.json").Items() {
		if event.Get("type").Str() != "end" {
			events = append(events, []byte(runcontext.CanonicalJSON(runcontext.ObjectOf("version", runcontext.Number(1), "scope", scope, "event", event))))
		}
	}
	if _, e = store.CommitHistory(ctx, runcontext.RunHistoryCommit{Run: runcontext.RunCommit{RunID: id, CommitID: "constructed", Checkpoint: []byte(runcontext.CanonicalJSON(cp)), Events: events}, Observations: observations}); e != nil {
		t.Fatal(e)
	}
	for i, a := range admissions {
		token, e := collector.Admit(owner, a)
		if e != nil {
			t.Fatal(e)
		}
		if e = collector.Record(owner, token, frames.Items()[i].JSON()); e != nil {
			t.Fatal(e)
		}
		if e = collector.Acknowledge(owner, token); e != nil {
			t.Fatal(e)
		}
	}
	head, e := store.ReadHistoryHead(ctx, id)
	if e != nil {
		t.Fatal(e)
	}
	seal, e := collector.Seal(owner, rootToken, capture.Terminal{RunID: id, ExecutionRevision: head.ExecutionRevision, Checkpoint: capture.Ref{Digest: head.Checkpoint.Digest, Bytes: head.Checkpoint.Bytes}, Status: "complete"})
	if e != nil {
		t.Fatal(e)
	}
	members := runcontext.PolicyMembers{"outputPatterns": {Value: []any{map[string]any{"name": "prose", "pattern": "First secret prose"}}}}
	prepared, e := runcontext.PrepareCapturePublication(ctx, store, seal, runcontext.CapturePublicationOptions{CommitID: "capture", Manifest: manifest, Members: members})
	if e != nil {
		t.Fatal(e)
	}
	if result, e := runcontext.CommitCapturePublication(ctx, store, prepared, nil); e != nil || result.Capture != "committed" {
		t.Fatal("publication", e)
	}
	exported, e := runcontext.ExportRunArchive(ctx, store, id, runcontext.ArchiveExportOptions{Members: members})
	if e != nil {
		t.Fatal(e)
	}
	imported, e := runcontext.ReadRunArchive(exported.Bytes, exported.Digest)
	if e != nil || !bytes.Equal(imported.CanonicalBytes(), exported.Bytes) {
		t.Fatal("archive bytes", e)
	}
	view, e := runcontext.ReadArchiveCapturedCorpusView(imported)
	if e != nil {
		t.Fatal(e)
	}
	return view
}
func TestHistoryCaptureFreshActualAdapterReplay(t *testing.T) {
	view := importedDerivedReplay(t)
	if view.Checkpoint.Representation != "derived" {
		t.Fatal("expected derived display")
	}
	invoked, newOutcomes := 0, 0
	a := AdapterFunc(func(ctx context.Context, run *Run) error {
		invoked++
		emit := run.Emit
		run.Emit = func(event contract.RunEvent) {
			if event.ToolResult != nil {
				newOutcomes++
			}
			emit(event)
		}
		return eino.New().Run(ctx, run)
	})
	if e := runCorpusAdapter(context.Background(), view.Entry, a, false, nil); e != nil {
		t.Fatal(e)
	}
	if invoked != 1 || newOutcomes != 1 {
		t.Fatal("fresh adapter did not produce tool outcome", invoked, newOutcomes)
	}
	wrong := view.Entry
	wrong.Expected = map[string]any{"gateDecisions": []any{}, "outcomes": []any{}, "refusals": []any{}}
	if e := runCorpusAdapter(context.Background(), wrong, a, false, nil); e == nil || !strings.Contains(e.Error(), "/outcomes/length differs") {
		t.Fatal("new outcome comparison safeguard", e)
	}
	extra := view.Entry
	var requests []json.RawMessage
	if e := json.Unmarshal(extra.Requests, &requests); e != nil {
		t.Fatal(e)
	}
	requests = append(requests, requests[0])
	extra.Requests, _ = json.Marshal(requests)
	if e := runCorpusAdapter(context.Background(), extra, a, false, nil); e == nil || !strings.Contains(e.Error(), "corpus requests") {
		t.Fatal("recording exhaustion safeguard", e)
	}
}
