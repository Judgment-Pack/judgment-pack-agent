package runcontext

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	capture "github.com/Judgment-Pack/judgment-pack-agent/go/internal/historycapture"
	"os"
	"strings"
	"testing"
)

func publicationFixture(t *testing.T) *MemoryRunStore {
	t.Helper()
	port, objects, _ := captureFixture(t)
	store := NewMemoryRunStore(MemoryRunStoreOptions{})
	batch := []HistoryObject{}
	for digest, raw := range objects {
		batch = append(batch, HistoryObject{HistoryObjectRef: HistoryObjectRef{Digest: digest, Bytes: len(raw)}, Value: raw})
	}
	ctx := context.Background()
	if e := store.StageHistoryObjects(ctx, HistoryResource{Kind: "run", ID: "r"}, batch); e != nil {
		t.Fatal(e)
	}
	for _, c := range port.commits {
		prior, e := store.Load(ctx, "r")
		if e != nil {
			t.Fatal(e)
		}
		expected := ""
		if prior != nil {
			expected = prior.Revision
		}
		events := [][]byte{}
		for _, ref := range c.Events {
			events = append(events, objects[ref.Digest])
		}
		if _, e = store.CommitHistory(ctx, RunHistoryCommit{Run: RunCommit{RunID: "r", ExpectedRevision: expected, CommitID: c.CommitID, Checkpoint: objects[c.Checkpoint.Digest], Events: events}, Observations: c.Observations}); e != nil {
			t.Fatal(e)
		}
	}
	return store
}
func publicationOptions(t *testing.T) CapturePublicationOptions {
	t.Helper()
	port, _, _ := captureFixture(t)
	entry, e := ReadCapturedCorpus(port)
	if e != nil {
		t.Fatal(e)
	}
	return CapturePublicationOptions{CommitID: "capture-publication", Manifest: entry.Manifest}
}
func publicationSeal(t *testing.T, store HistoryRunStore, change func([]capture.Admission, []json.RawMessage), bind bool) *capture.Seal {
	t.Helper()
	_, _, v := captureFixture(t)
	var rows []capture.Admission
	var frames []json.RawMessage
	json.Unmarshal(v.Get("admissions").JSON(), &rows)
	json.Unmarshal(v.Get("frames").JSON(), &frames)
	if change != nil {
		change(rows, frames)
	}
	owner := new(int)
	collector, root, e := capture.New(owner, "r", capture.Scope{Execution: "r", Role: "author"})
	if e != nil {
		t.Fatal(e)
	}
	if bind {
		if e = collector.BindPublisher(owner, store); e != nil {
			t.Fatal(e)
		}
	}
	tokens := []*capture.Token{}
	for _, row := range rows {
		token, e := collector.Admit(owner, row)
		if e != nil {
			t.Fatal(e)
		}
		tokens = append(tokens, token)
	}
	for _, i := range []int{1, 0} {
		if e = collector.Record(owner, tokens[i], frames[i]); e != nil {
			t.Fatal(e)
		}
		if e = collector.Acknowledge(owner, tokens[i]); e != nil {
			t.Fatal(e)
		}
	}
	head, e := store.ReadHistoryHead(context.Background(), "r")
	if e != nil {
		t.Fatal(e)
	}
	s, e := collector.Seal(owner, root, capture.Terminal{RunID: "r", ExecutionRevision: head.ExecutionRevision, Checkpoint: capture.Ref{Digest: head.Checkpoint.Digest, Bytes: head.Checkpoint.Bytes}, Status: "complete"})
	if e != nil {
		t.Fatal(e)
	}
	return s
}

type publicationStore struct {
	HistoryRunStore
	commit func(context.Context, HistoryMetadataCommit) (RunHistoryHead, error)
	page   func(context.Context, string, string, int, int) (HistoryMetadataPage, error)
	object func(context.Context, HistoryResource, string, string, int, int) (HistoryObjectPage, error)
	stage  func(context.Context, HistoryResource, []HistoryObject) error
}

func (s *publicationStore) CommitHistoryMetadata(c context.Context, v HistoryMetadataCommit) (RunHistoryHead, error) {
	if s.commit != nil {
		return s.commit(c, v)
	}
	return s.HistoryRunStore.CommitHistoryMetadata(c, v)
}
func (s *publicationStore) ReadHistoryMetadataPage(c context.Context, id, r string, a, n int) (HistoryMetadataPage, error) {
	if s.page != nil {
		return s.page(c, id, r, a, n)
	}
	return s.HistoryRunStore.ReadHistoryMetadataPage(c, id, r, a, n)
}
func (s *publicationStore) ReadHistoryObject(c context.Context, r HistoryResource, rev, d string, o, n int) (HistoryObjectPage, error) {
	if s.object != nil {
		return s.object(c, r, rev, d, o, n)
	}
	return s.HistoryRunStore.ReadHistoryObject(c, r, rev, d, o, n)
}
func (s *publicationStore) StageHistoryObjects(c context.Context, r HistoryResource, v []HistoryObject) error {
	if s.stage != nil {
		return s.stage(c, r, v)
	}
	return s.HistoryRunStore.StageHistoryObjects(c, r, v)
}

type publicationSink struct {
	calls   int
	failure bool
	files   map[string][]byte
}

func (s *publicationSink) WriteCorpus(_ string, files map[string][]byte) error {
	s.calls++
	s.files = files
	if s.failure {
		return errors.New("offline")
	}
	return nil
}
func TestCapturePublicationActualStoreAndCanonicalArchive(t *testing.T) {
	ctx := context.Background()
	store := publicationFixture(t)
	before, _ := store.Load(ctx, "r")
	prepared, e := PrepareCapturePublication(ctx, store, publicationSeal(t, store, nil, true), publicationOptions(t))
	if e != nil {
		t.Fatal(e)
	}
	head, _ := store.ReadHistoryHead(ctx, "r")
	if head.Metadata != nil {
		t.Fatal("private staging exposed capture")
	}
	result, e := CommitCapturePublication(ctx, store, prepared, nil)
	if e != nil || result.Capture != "committed" || result.Delivery != "not-requested" {
		t.Fatal("publication", result, e)
	}
	after, _ := store.Load(ctx, "r")
	if !archiveEqual(before, after) {
		t.Fatal("capture changed execution")
	}
	again, e := CommitCapturePublication(ctx, store, prepared, nil)
	if e != nil || !archiveEqual(result, again) {
		t.Fatal("explicit idempotent publication replay", e)
	}
	view, e := ReadRetainedRunHistory(ctx, store, "r")
	if e != nil {
		t.Fatal(e)
	}
	entry, e := ReadCapturedCorpus(view)
	if e != nil || len(entry.Requests) == 0 {
		t.Fatal("actual store corpus", e)
	}
	exported, e := ExportRunArchive(ctx, store, "r", ArchiveExportOptions{})
	if e != nil {
		t.Fatal(e)
	}
	imported, e := ReadRunArchive(exported.Bytes, exported.Digest)
	if e != nil || !bytes.Equal(imported.CanonicalBytes(), exported.Bytes) {
		t.Fatal("exact archive bytes", e)
	}
	if _, e = ReadArchiveCapturedCorpus(imported); e != nil {
		t.Fatal(e)
	}
	if _, e = CommitCapturePublication(ctx, store, &PreparedCapturePublication{}, nil); e == nil {
		t.Fatal("caller-authored preparation admitted")
	}
}
func TestCapturePublicationStoreIdentityAndOneUse(t *testing.T) {
	ctx := context.Background()
	store := publicationFixture(t)
	seal := publicationSeal(t, store, nil, true)
	other := &publicationStore{HistoryRunStore: store}
	if _, e := PrepareCapturePublication(ctx, other, seal, publicationOptions(t)); e == nil {
		t.Fatal("wrong original store admitted")
	}
	if _, e := PrepareCapturePublication(ctx, store, seal, publicationOptions(t)); e != nil {
		t.Fatal(e)
	}
	if _, e := PrepareCapturePublication(ctx, store, seal, publicationOptions(t)); e == nil {
		t.Fatal("consumed publication seal reused")
	}
	if _, e := PrepareCapturePublication(ctx, store, publicationSeal(t, store, nil, false), publicationOptions(t)); e == nil {
		t.Fatal("unbound seal admitted")
	}
}
func TestCapturePublicationSourceAssociationRefusals(t *testing.T) {
	for _, fault := range []string{"ordinal", "commit-id", "expectation", "frame-hidden-by-redaction"} {
		t.Run(fault, func(t *testing.T) {
			ctx := context.Background()
			base := publicationFixture(t)
			store := &publicationStore{HistoryRunStore: base}
			staged := 0
			store.stage = func(c context.Context, r HistoryResource, v []HistoryObject) error {
				staged++
				return base.StageHistoryObjects(c, r, v)
			}
			seal := publicationSeal(t, store, func(rows []capture.Admission, frames []json.RawMessage) {
				switch fault {
				case "ordinal":
					rows[0].Ordinal = 0
				case "commit-id":
					rows[0].CommitID = "different"
				case "expectation":
					rows[0].Expectation = rows[1].Expectation
				default:
					v, _ := ParseJSON(frames[0])
					v.Get("request").Set("body", String("WRONG-PRIVATE"))
					frames[0] = v.JSON()
				}
			}, true)
			options := publicationOptions(t)
			options.Members = PolicyMembers{"outputPatterns": {Value: []any{map[string]any{"name": "mismatch", "pattern": "WRONG-PRIVATE"}}}}
			if _, e := PrepareCapturePublication(ctx, store, seal, options); e == nil {
				t.Fatal("source association mismatch admitted")
			}
			if staged != 0 {
				t.Fatal("invalid material staged")
			}
		})
	}
}
func TestCapturePublicationOverbudgetLogicalMaterialRefuses(t *testing.T) {
	ctx := context.Background()
	base := publicationFixture(t)
	store := &publicationStore{HistoryRunStore: base}
	staged, metadata := 0, 0
	before, _ := base.ReadHistoryHead(ctx, "r")
	store.stage = func(c context.Context, r HistoryResource, objects []HistoryObject) error {
		staged++
		return base.StageHistoryObjects(c, r, objects)
	}
	store.commit = func(c context.Context, row HistoryMetadataCommit) (RunHistoryHead, error) {
		metadata++
		return base.CommitHistoryMetadata(c, row)
	}
	seal := publicationSeal(t, store, func(_ []capture.Admission, frames []json.RawMessage) {
		v, _ := ParseJSON(frames[0])
		v.Get("response").Set("body", String("BODY-MARKER"))
		prefix := strings.Index("["+CanonicalJSON(v), "BODY-MARKER")
		length := HistoryObjectBytes - 5 - prefix
		text := strings.Repeat("Public transcript. ", length/19+1)[:length] + "SECRET-123"
		v.Get("response").Set("body", String(text))
		if strings.Index("["+CanonicalJSON(v), "SECRET-123") != HistoryObjectBytes-5 {
			t.Fatal("secret did not cross future chunk boundary")
		}
		frames[0] = v.JSON()
	}, true)
	options := publicationOptions(t)
	options.Members = PolicyMembers{"outputPatterns": {Value: []any{map[string]any{"name": "private", "pattern": "SECRET-123"}}}}
	sink := &publicationSink{}
	prepared, e := PrepareCapturePublication(ctx, store, seal, options)
	if e == nil {
		_, e = CommitCapturePublication(ctx, store, prepared, sink)
	}
	var unavailable *ReplayUnavailable
	if !errors.As(e, &unavailable) {
		t.Fatal("overbudget logical material did not refuse", e)
	}
	after, _ := base.ReadHistoryHead(ctx, "r")
	if staged != 0 || metadata != 0 || sink.calls != 0 || !archiveEqual(before, after) {
		t.Fatal("overbudget material reached publication", staged, metadata, sink.calls)
	}
}
func TestCapturePublicationWholeLogicalPrivacy(t *testing.T) {
	ctx := context.Background()
	base := publicationFixture(t)
	staged := 0
	store := &publicationStore{HistoryRunStore: base}
	store.stage = func(c context.Context, r HistoryResource, objects []HistoryObject) error {
		staged++
		for _, o := range objects {
			if bytes.Contains(o.Value, []byte("SECRET-123")) {
				t.Fatal("raw capture material staged")
			}
		}
		return base.StageHistoryObjects(c, r, objects)
	}
	seal := publicationSeal(t, store, func(_ []capture.Admission, frames []json.RawMessage) {
		v, _ := ParseJSON(frames[0])
		v.Get("response").Set("body", String(strings.Repeat("Public transcript. ", 128)+"SECRET-123 public ending."))
		frames[0] = v.JSON()
	}, true)
	options := publicationOptions(t)
	options.Members = PolicyMembers{"outputPatterns": {Value: []any{map[string]any{"name": "private", "pattern": "SECRET-123"}}}}
	prepared, e := PrepareCapturePublication(ctx, store, seal, options)
	if e != nil {
		t.Fatal(e)
	}
	sink := &publicationSink{}
	result, e := CommitCapturePublication(ctx, store, prepared, sink)
	if e != nil || result.Capture != "committed" || result.Delivery != "delivered" || sink.calls != 1 {
		t.Fatal(result, e)
	}
	for _, raw := range sink.files {
		if strings.Contains(string(raw), "SECRET-123") {
			t.Fatal("raw capture delivered")
		}
	}
	if staged == 0 || !bytes.Contains(sink.files["requests.json"], []byte("[redacted: private]")) {
		t.Fatal("clean request was not staged and delivered")
	}
	retained, e := ReadRetainedRunHistory(ctx, store, "r")
	if e != nil {
		t.Fatal(e)
	}
	view, e := ReadCapturedCorpusView(retained)
	if e != nil {
		t.Fatal(e)
	}
	requests, e := ParseJSON(view.Entry.Requests)
	if e != nil {
		t.Fatal(e)
	}
	request := requests.Items()[0]
	if request.Get("response").Get("body").Str() != strings.Repeat("Public transcript. ", 128)+"[redacted: private] public ending." {
		t.Fatal("complete logical request not preserved")
	}

}
func TestCapturePublicationDerivedPlanRetainsSourceAuthority(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryRunStore(MemoryRunStoreOptions{})
	cp := taskIdentityFixture(t).Get("checkpoint")
	id := cp.Get("id").Str()
	if _, e := store.Commit(ctx, RunCommit{RunID: id, CommitID: "terminal", Checkpoint: []byte(CanonicalJSON(cp)), Events: [][]byte{}}); e != nil {
		t.Fatal(e)
	}
	before, _ := store.Load(ctx, id)
	head, _ := store.ReadHistoryHead(ctx, id)
	owner := new(int)
	collector, root, e := capture.New(owner, id, capture.Scope{Execution: id, Role: "author"})
	if e != nil {
		t.Fatal(e)
	}
	if e = collector.BindPublisher(owner, store); e != nil {
		t.Fatal(e)
	}
	seal, e := collector.Seal(owner, root, capture.Terminal{RunID: id, ExecutionRevision: head.ExecutionRevision, Checkpoint: capture.Ref{Digest: head.Checkpoint.Digest, Bytes: head.Checkpoint.Bytes}, Status: "complete"})
	if e != nil {
		t.Fatal(e)
	}
	options := publicationOptions(t)
	options.Members = PolicyMembers{"outputPatterns": {Value: []any{map[string]any{"name": "prose", "pattern": "First secret prose"}}}}
	prepared, e := PrepareCapturePublication(ctx, store, seal, options)
	if e != nil {
		t.Fatal(e)
	}
	result, e := CommitCapturePublication(ctx, store, prepared, nil)
	if e != nil || result.Capture != "committed" {
		t.Fatal("derived publication", e)
	}
	retained, e := ReadRetainedRunHistory(ctx, store, id)
	if e != nil {
		t.Fatal(e)
	}
	view, e := ReadCapturedCorpusView(retained)
	if e != nil {
		t.Fatal(e)
	}
	d := view.Checkpoint.Document
	if view.Checkpoint.Representation != "derived" || view.Checkpoint.Source != *head.Checkpoint || d.Get("planState").Get("revision").Str() != cp.Get("planState").Get("revision").Str() || d.Get("plan").Items()[0].Get("step").Str() != "[redacted: prose]" {
		t.Fatal("derived source labels changed")
	}
	raw := []byte(CanonicalJSON(d))
	if view.Checkpoint.Derived != (HistoryObjectRef{HistoryBytesDigest(raw), len(raw)}) {
		t.Fatal("actual derived bytes not hashed")
	}
	if _, e = ReadCapturedCorpus(retained); e == nil {
		t.Fatal("derived bare checkpoint accepted natively")
	}
	if _, e = ReadExecution(d); e == nil {
		t.Fatal("sanitized Plan native-valid")
	}
	wrapped, _ := ParseJSON(view.Entry.Checkpoint)
	if _, e = ReadExecution(wrapped); e == nil {
		t.Fatal("wrapper accepted natively")
	}
	if _, e = ReadCorpusCheckpointView(view.Entry.Checkpoint); e != nil {
		t.Fatal(e)
	}
	after, _ := store.Load(ctx, id)
	if !archiveEqual(before, after) {
		t.Fatal("source execution changed")
	}
	exported, e := ExportRunArchive(ctx, store, id, ArchiveExportOptions{Members: options.Members})
	if e != nil {
		t.Fatal(e)
	}
	imported, e := ReadRunArchive(exported.Bytes, exported.Digest)
	if e != nil || !bytes.Equal(imported.CanonicalBytes(), exported.Bytes) {
		t.Fatal("canonical archive", e)
	}
	replay, e := ReadArchiveCapturedCorpusView(imported)
	if e != nil || !archiveEqual(replay.Checkpoint, view.Checkpoint) {
		t.Fatal("imported derived view", e)
	}
	if _, e = ReadArchiveCapturedCorpus(imported); e == nil {
		t.Fatal("derived archive accepted natively")
	}
}
func TestCapturePublicationStaleMetadataCAS(t *testing.T) {
	ctx := context.Background()
	store := publicationFixture(t)
	prepared, e := PrepareCapturePublication(ctx, store, publicationSeal(t, store, nil, true), publicationOptions(t))
	if e != nil {
		t.Fatal(e)
	}
	head, _ := store.ReadHistoryHead(ctx, "r")
	_, e = store.CommitHistoryMetadata(ctx, HistoryMetadataCommit{RunID: "r", ExpectedHistoryRevision: head.Revision, CommitID: "rating-race", Record: HistoryMetadata{Version: 1, Kind: "feedback", Feedback: &RunFeedback{Version: 1, ID: "rating", RunID: "r", Sequence: 1, Target: FeedbackTarget{Kind: "run"}, Rating: "good", Actor: "reviewer", RecordedAt: "2026-10-26T00:00:00.000Z"}}})
	if e != nil {
		t.Fatal(e)
	}
	sink := &publicationSink{}
	if _, e = CommitCapturePublication(ctx, store, prepared, sink); e == nil {
		t.Fatal("stale metadata CAS accepted")
	}
	if sink.calls != 0 {
		t.Fatal("stale capture delivered")
	}
}
func TestCapturePublicationUnknownReceiptsNeverDeliver(t *testing.T) {
	for _, fault := range []string{"receipt", "metadata-page", "object", "lost-ack"} {
		t.Run(fault, func(t *testing.T) {
			ctx := context.Background()
			base := publicationFixture(t)
			store := &publicationStore{HistoryRunStore: base}
			prepared, e := PrepareCapturePublication(ctx, store, publicationSeal(t, store, nil, true), publicationOptions(t))
			if e != nil {
				t.Fatal(e)
			}
			calls := 0
			store.commit = func(c context.Context, v HistoryMetadataCommit) (RunHistoryHead, error) {
				calls++
				head, e := base.CommitHistoryMetadata(c, v)
				if fault == "receipt" {
					head.CommitCount++
				}
				if fault == "lost-ack" && calls == 1 {
					return RunHistoryHead{}, errors.New("lost acknowledgement")
				}
				return head, e
			}
			if fault == "metadata-page" {
				store.page = func(c context.Context, id, r string, a, n int) (HistoryMetadataPage, error) {
					p, e := base.ReadHistoryMetadataPage(c, id, r, a, n)
					p.HasMore = true
					return p, e
				}
			}
			if fault == "object" {
				store.object = func(c context.Context, r HistoryResource, rev, d string, o, n int) (HistoryObjectPage, error) {
					p, e := base.ReadHistoryObject(c, r, rev, d, o, n)
					if len(p.Value) > 0 {
						p.Value[0] = 0
					}
					return p, e
				}
			}
			sink := &publicationSink{}
			result, e := CommitCapturePublication(ctx, store, prepared, sink)
			if e != nil || result.Capture != "unknown" || result.Delivery != "not-attempted" || sink.calls != 0 || calls != 1 {
				t.Fatal("unknown receipt exposed/delivered capture", result, e, calls, sink.calls)
			}
			if fault == "lost-ack" {
				result, e = CommitCapturePublication(ctx, store, prepared, nil)
				if e != nil || result.Capture != "committed" || calls != 2 {
					t.Fatal("explicit reconciliation", result, e)
				}
			}
		})
	}
}
func TestCapturePublicationSinkErrorNoAutomaticRetry(t *testing.T) {
	ctx := context.Background()
	store := publicationFixture(t)
	prepared, e := PrepareCapturePublication(ctx, store, publicationSeal(t, store, nil, true), publicationOptions(t))
	if e != nil {
		t.Fatal(e)
	}
	sink := &publicationSink{failure: true}
	for i := 0; i < 2; i++ {
		result, e := CommitCapturePublication(ctx, store, prepared, sink)
		if e != nil || result.Capture != "committed" || result.Delivery != "unknown" {
			t.Fatal(result, e)
		}
	}
	if sink.calls != 1 {
		t.Fatal("external delivery retried", sink.calls)
	}
}

func TestHistoryArchiveRepeatResultCustodyPrivacy(t *testing.T) {
	ctx := context.Background()
	raw, e := os.ReadFile("../../contract/testdata/repeat-tools/records.json")
	if e != nil {
		t.Fatal(e)
	}
	vectors, e := ParseJSON(raw)
	if e != nil {
		t.Fatal(e)
	}
	cp := vectors.Get("custodyCheckpoint")
	if _, e = ReadExecution(cp); e != nil {
		t.Fatal(e)
	}
	store := NewMemoryRunStore(MemoryRunStoreOptions{})
	id := cp.Get("id").Str()
	if _, e = store.Commit(ctx, RunCommit{RunID: id, CommitID: "custody-source", Checkpoint: []byte(CanonicalJSON(cp)), Events: [][]byte{}}); e != nil {
		t.Fatal(e)
	}
	before, _ := store.Load(ctx, id)
	head, _ := store.ReadHistoryHead(ctx, id)
	exported, e := ExportRunArchive(ctx, store, id, ArchiveExportOptions{Members: PolicyMembers{"outputPatterns": {Value: []any{map[string]any{"name": "precise", "pattern": "9007199254740993"}}}}})
	if e != nil {
		t.Fatal(e)
	}
	view, e := ReadRunArchive(exported.Bytes, exported.Digest)
	if e != nil {
		t.Fatal(e)
	}
	display, e := view.ReadDisplay(*head.Checkpoint, "checkpoint")
	if e != nil {
		t.Fatal(e)
	}
	if display.Document.Get("durable").Get("dispatches").Items()[0].Get("repeatResultJson").Str() != `{"n":[redacted: precise]}` {
		t.Fatal("raw result custody was not redacted")
	}
	if _, e = ReadExecution(display.Document); e == nil {
		t.Fatal("redacted custody admitted natively")
	}
	after, _ := store.Load(ctx, id)
	if !archiveEqual(before, after) {
		t.Fatal("source custody changed")
	}
	var archive runArchive
	if json.Unmarshal(exported.Bytes, &archive) != nil {
		t.Fatal("archive decode")
	}
	for _, object := range archive.Objects {
		body, e := base64.StdEncoding.DecodeString(object.Bytes)
		if e != nil || bytes.Contains(body, []byte("9007199254740993")) {
			t.Fatal("raw custody exported", e)
		}
	}
	report, e := view.ReadDisplay(view.Manifest().Report, "report")
	if e != nil {
		t.Fatal(e)
	}
	if bytes.Contains(report.Document.JSON(), []byte("repeatResultJson")) || bytes.Contains(report.Document.JSON(), []byte("9007199254740993")) {
		t.Fatal("raw custody reached summary")
	}
}
