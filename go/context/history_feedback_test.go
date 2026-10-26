package runcontext

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"sort"
	"strings"
	"testing"
)

func feedbackInput(t *testing.T, store *MemoryRunStore) RunFeedbackInput {
	t.Helper()
	head, e := store.ReadHistoryHead(context.Background(), "r")
	if e != nil {
		t.Fatal(e)
	}
	return RunFeedbackInput{RunID: "r", ID: "rating", CommitID: "feedback-1", ExpectedHistoryRevision: head.Revision, ExpectedMetadataRevision: head.MetadataRevision(), Target: FeedbackTarget{Kind: "run"}, Rating: "good", Actor: "user", RecordedAt: "2026-10-25T01:02:03.000Z"}
}
func TestRunFeedbackEditsAndExecutionIsolation(t *testing.T) {
	ctx := context.Background()
	store := archiveMemory(t)
	before, _ := store.Load(ctx, "r")
	events, _ := store.ReadEvents(ctx, "r", "", 100)
	view, e := ReadRetainedRunHistory(ctx, store, "r")
	if e != nil {
		t.Fatal(e)
	}
	original, e := ProjectHistoryRunReport(view)
	if e != nil || original.Get("version").Num() != 1 {
		t.Fatal("feature-free report", e)
	}
	first, e := WriteRunFeedback(ctx, store, feedbackInput(t, store), PrepareRunFeedbackOptions{}, nil)
	if e != nil || first.Delivery != "not-requested" || first.Candidate != nil {
		t.Fatal("first feedback", e)
	}
	view, e = ReadRetainedRunHistory(ctx, store, "r")
	if e != nil {
		t.Fatal(e)
	}
	record := *view.Metadata()[0].Record.Feedback
	if record.Remark != nil {
		t.Fatal("absent remark populated")
	}
	previous := HistoryBytesDigest([]byte(historyInputText(record)))
	edit := feedbackInput(t, store)
	edit.CommitID = "feedback-2"
	edit.Previous = &previous
	edit.Rating = "bad"
	empty := ""
	edit.Remark = &empty
	edit.Actor = "second-user"
	edit.RecordedAt = "2026-10-25T02:02:03.000Z"
	prepared, e := PrepareRunFeedback(ctx, store, edit, PrepareRunFeedbackOptions{Members: archiveMembers()})
	if e != nil {
		t.Fatal("prepare edit", e)
	}
	result, e := CommitPreparedRunFeedback(ctx, store, prepared)
	if e != nil {
		t.Fatal("commit edit", e)
	}
	replay, e := CommitPreparedRunFeedback(ctx, store, prepared)
	if e != nil || !archiveEqual(result.Head, replay.Head) {
		t.Fatal("exact receipt replay", e)
	}
	if result.Candidate == nil || result.Candidate.Status != "pending-review" || result.Candidate.Replay.Kind != "unavailable" {
		t.Fatal("pending candidate")
	}
	view, e = ReadRetainedRunHistory(ctx, store, "r")
	if e != nil {
		t.Fatal(e)
	}
	rows := view.Metadata()
	if len(rows) != 2 || !archiveEqual(*rows[0].Record.Feedback, record) || rows[1].Record.Feedback.Remark == nil || *rows[1].Record.Feedback.Remark != "" || rows[1].Record.Feedback.Sequence != 2 {
		t.Fatal("edit history or empty remark lost")
	}
	report, e := ProjectHistoryRunReport(view)
	if e != nil || report.Get("version").Num() != 2 || report.Get("checkpointVersion").Num() != 2 || report.Has("usageTotals") || report.Has("route") || report.Get("feedback").Len() != 2 {
		t.Fatal("feedback report version", e)
	}
	exported, e := ExportRunArchive(ctx, store, "r", ArchiveExportOptions{Members: archiveMembers()})
	if e != nil {
		t.Fatal("export", e)
	}
	imported, e := ReadRunArchive(exported.Bytes, "")
	if e != nil {
		t.Fatal(e)
	}
	held, e := imported.ReadDisplay(imported.Manifest().Report)
	if e != nil || CanonicalJSON(held.Document) != CanonicalJSON(report) {
		t.Fatal("report archive projection", e)
	}
	after, _ := store.Load(ctx, "r")
	afterEvents, _ := store.ReadEvents(ctx, "r", "", 100)
	if !archiveEqual(before, after) || !archiveEqual(events, afterEvents) {
		t.Fatal("feedback changed execution")
	}
}

type feedbackFailSink struct {
	calls     int
	candidate CorpusCandidate
}

func (s *feedbackFailSink) WriteCandidate(_ string, candidate CorpusCandidate) error {
	s.calls++
	s.candidate = candidate
	return errors.New("offline")
}
func TestRunFeedbackRedactedCandidateAndSourceDigestEditChain(t *testing.T) {
	ctx := context.Background()
	store := archiveMemory(t)
	input := feedbackInput(t, store)
	input.Rating = "bad"
	remark := "SECRET-123"
	input.Remark = &remark
	sink := &feedbackFailSink{}
	first, e := WriteRunFeedback(ctx, store, input, PrepareRunFeedbackOptions{Members: archiveMembers()}, sink)
	if e != nil || first.Delivery != "unknown" || sink.calls != 1 || strings.Contains(historyInputText(first.Candidate), "SECRET-123") || first.Candidate == nil || *first.Candidate.Feedback.Remark != "[redacted: account-code]" {
		t.Fatal("redacted delivery", e)
	}
	view, e := ReadRetainedRunHistory(ctx, store, "r")
	if e != nil {
		t.Fatal(e)
	}
	old := *view.Metadata()[0].Record.Feedback
	original := HistoryBytesDigest([]byte(historyInputText(old)))
	edit := feedbackInput(t, store)
	edit.CommitID = "feedback-2"
	edit.Previous = &original
	edit.Rating = "good"
	updated := "updated"
	edit.Remark = &updated
	if _, e = WriteRunFeedback(ctx, store, edit, PrepareRunFeedbackOptions{Members: archiveMembers()}, nil); e != nil {
		t.Fatal(e)
	}
	exported, e := ExportRunArchive(ctx, store, "r", ArchiveExportOptions{Members: archiveMembers()})
	if e != nil || strings.Contains(string(exported.Bytes), "SECRET-123") {
		t.Fatal("redacted archive", e)
	}
	imported, e := ReadRunArchive(exported.Bytes, "")
	if e != nil {
		t.Fatal(e)
	}
	held, _ := imported.ReadDisplay(imported.Manifest().Report)
	feedback := held.Document.Get("feedback").Items()
	if feedback[0].Get("sourceDigest").Str() != original || feedback[1].Get("record").Get("previous").Str() != original || HistoryBytesDigest([]byte(CanonicalJSON(feedback[0].Get("record")))) == original || feedback[0].Get("record").Get("remark").Str() != "[redacted: account-code]" {
		t.Fatal("source digest identity lost")
	}
}
func TestRunFeedbackInvalidTargetAndStaleHead(t *testing.T) {
	ctx := context.Background()
	store := archiveMemory(t)
	start := feedbackInput(t, store)
	if _, e := WriteRunFeedback(ctx, store, start, PrepareRunFeedbackOptions{}, nil); e != nil {
		t.Fatal(e)
	}
	if _, e := PrepareRunFeedback(ctx, store, start, PrepareRunFeedbackOptions{}); e == nil {
		t.Fatal("stale view admitted")
	}
	view, _ := ReadRetainedRunHistory(ctx, store, "r")
	old := *view.Metadata()[0].Record.Feedback
	previous := HistoryBytesDigest([]byte(historyInputText(old)))
	page, _ := QueryHistoryEvents(view, HistoryEventQuery{Types: []string{"message"}})
	message := page.Items[0]
	for _, test := range []struct {
		id       string
		previous *string
		target   FeedbackTarget
	}{{"rating", nil, FeedbackTarget{Kind: "run"}}, {"rating", &previous, FeedbackTarget{Kind: "assistant-message", EventSequence: message.Sequence, EventDigest: message.Source.Digest}}, {"message", nil, FeedbackTarget{Kind: "assistant-message", EventSequence: message.Sequence, EventDigest: "sha256:" + strings.Repeat("0", 64)}}} {
		input := feedbackInput(t, store)
		input.ID = test.id
		input.CommitID = "bad"
		input.Previous = test.previous
		input.Target = test.target
		if _, e := PrepareRunFeedback(ctx, store, input, PrepareRunFeedbackOptions{}); e == nil {
			t.Fatal("invalid target/previous admitted")
		}
	}
	head, _ := store.ReadHistoryHead(ctx, "r")
	if head.Metadata.Count != 1 {
		t.Fatal("invalid feedback published")
	}
}

type feedbackFalseReceipt struct {
	HistoryRunStore
	calls int
}

func (s *feedbackFalseReceipt) CommitHistoryMetadata(ctx context.Context, input HistoryMetadataCommit) (RunHistoryHead, error) {
	s.calls++
	head, e := s.HistoryRunStore.CommitHistoryMetadata(ctx, input)
	head.ExecutionRevision = "forged"
	return head, e
}
func TestRunFeedbackFalseReceiptNeverExposesCandidate(t *testing.T) {
	ctx := context.Background()
	store := archiveMemory(t)
	before, _ := store.Load(ctx, "r")
	input := feedbackInput(t, store)
	input.Rating = "bad"
	prepared, e := PrepareRunFeedback(ctx, store, input, PrepareRunFeedbackOptions{Members: archiveMembers()})
	if e != nil {
		t.Fatal(e)
	}
	wrapped := &feedbackFalseReceipt{HistoryRunStore: store}
	_, e = CommitPreparedRunFeedback(ctx, wrapped, prepared)
	var unknown *FeedbackOutcomeUnknown
	if !errors.As(e, &unknown) || wrapped.calls != 1 {
		t.Fatal("false receipt accepted or execution retried", e)
	}
	after, _ := store.Load(ctx, "r")
	head, _ := store.ReadHistoryHead(ctx, "r")
	if !archiveEqual(before, after) || head.Metadata.Count != 1 {
		t.Fatal("metadata changed execution")
	}
}

func TestRunFeedbackHandwrittenSharedRecordsAndReportVersions(t *testing.T) {
	raw, e := os.ReadFile("../../contract/fixtures/feedback/records.json")
	if e != nil {
		t.Fatal(e)
	}
	vectors, _ := ParseJSON(raw)
	for _, row := range vectors.Get("records").Items() {
		if _, e = ReadRunFeedback([]byte(CanonicalJSON(row))); e != nil {
			t.Fatal("shared feedback", e)
		}
	}
	for _, row := range vectors.Get("reports").Items() {
		t.Run(row.Get("name").Str(), func(t *testing.T) {
			if _, e := ReadRunReport(row.Get("report")); e != nil {
				t.Fatal("shared report", e)
			}
			ctx := context.Background()
			store := NewMemoryRunStore(MemoryRunStoreOptions{})
			checkpoint := row.Get("checkpoint")
			if _, e := store.Commit(ctx, RunCommit{RunID: "r", CommitID: "source", Checkpoint: []byte(CanonicalJSON(checkpoint)), Events: [][]byte{}}); e != nil {
				t.Fatal(e)
			}
			before, e := ReadRetainedRunHistory(ctx, store, "r")
			if e != nil {
				t.Fatal(e)
			}
			base, e := ProjectHistoryRunReport(before)
			if e != nil {
				t.Fatal(e)
			}
			cp, e := ReadExecution(checkpoint)
			if e != nil {
				t.Fatal(e)
			}
			native, e := ProjectRunReport(*cp)
			if e != nil || CanonicalJSON(base) != CanonicalJSON(native) {
				t.Fatal("feature-free projection changed", e)
			}
			input := feedbackInput(t, store)
			input.Actor = row.Get("feedback").Get("actor").Str()
			input.RecordedAt = row.Get("feedback").Get("recordedAt").Str()
			if _, e = WriteRunFeedback(ctx, store, input, PrepareRunFeedbackOptions{}, nil); e != nil {
				t.Fatal(e)
			}
			view, e := ReadRetainedRunHistory(ctx, store, "r")
			if e != nil {
				t.Fatal(e)
			}
			report, e := ProjectHistoryRunReport(view)
			if e != nil || CanonicalJSON(report) != CanonicalJSON(row.Get("report")) {
				t.Fatal("truthful report source version", e)
			}
			exported, e := ExportRunArchive(ctx, store, "r", ArchiveExportOptions{})
			if e != nil {
				t.Fatal(e)
			}
			imported, e := ReadRunArchive(exported.Bytes, "")
			if e != nil {
				t.Fatal(e)
			}
			display, e := imported.ReadDisplay(imported.Manifest().Report)
			if e != nil || CanonicalJSON(display.Document) != CanonicalJSON(row.Get("report")) {
				t.Fatal("shared archive projection", e)
			}
		})
	}
	for _, row := range vectors.Get("invalidReports").Items() {
		if _, e = ReadRunReport(row.Get("report")); e == nil {
			t.Fatal("invalid report admitted", row.Get("name").Str())
		}
	}
}
func feedbackRewriteReport(t *testing.T, raw []byte, change func(*Value)) []byte {
	t.Helper()
	a := MustParseJSON(string(raw))
	manifest := a.Get("manifest")
	var mapping *Value
	for _, row := range manifest.Get("mappings").Items() {
		if row.Get("kind").Str() == "report" {
			mapping = row
			break
		}
	}
	old := mapping.Get("exported").Get("digest").Str()
	var blob *Value
	for _, row := range a.Get("objects").Items() {
		if row.Get("digest").Str() == old {
			blob = row
			break
		}
	}
	body, e := base64.StdEncoding.DecodeString(blob.Get("bytes").Str())
	if e != nil {
		t.Fatal(e)
	}
	report := MustParseJSON(string(body))
	change(report)
	body = []byte(CanonicalJSON(report))
	ref := historyRefJSON(HistoryObjectRef{Digest: HistoryBytesDigest(body), Bytes: len(body)})
	mapping.Set("source", ref.Clone())
	mapping.Set("exported", ref.Clone())
	manifest.Set("report", ref.Clone())
	for _, row := range manifest.Get("members").Items() {
		if row.Get("digest").Str() == old {
			row.Set("digest", ref.Get("digest"))
			row.Set("bytes", ref.Get("bytes"))
			row.Set("source", ref.Clone())
		}
	}
	blob.Set("digest", ref.Get("digest"))
	blob.Set("bytes", String(base64.StdEncoding.EncodeToString(body)))
	sort.Slice(manifest.Get("mappings").a, func(i, j int) bool {
		return manifest.Get("mappings").a[i].Get("source").Get("digest").Str() < manifest.Get("mappings").a[j].Get("source").Get("digest").Str()
	})
	for _, array := range []*Value{manifest.Get("members"), a.Get("objects")} {
		sort.Slice(array.a, func(i, j int) bool { return archiveTestKey(array.a[i]) < archiveTestKey(array.a[j]) })
	}
	return archiveResign(t, a)
}
func TestRunFeedbackArchiveReportSourceFacts(t *testing.T) {
	raw, e := os.ReadFile("../../contract/fixtures/feedback/records.json")
	if e != nil {
		t.Fatal(e)
	}
	vectors, _ := ParseJSON(raw)
	checkpoint := vectors.Get("reports").Items()[3].Get("checkpoint")
	ctx := context.Background()
	store := NewMemoryRunStore(MemoryRunStoreOptions{})
	if _, e = store.Commit(ctx, RunCommit{RunID: "r", CommitID: "source", Checkpoint: []byte(CanonicalJSON(checkpoint)), Events: [][]byte{}}); e != nil {
		t.Fatal(e)
	}
	if _, e = WriteRunFeedback(ctx, store, feedbackInput(t, store), PrepareRunFeedbackOptions{}, nil); e != nil {
		t.Fatal(e)
	}
	exported, e := ExportRunArchive(ctx, store, "r", ArchiveExportOptions{})
	if e != nil {
		t.Fatal(e)
	}
	for _, change := range []func(*Value){func(r *Value) {
		r.Set("checkpointVersion", Number(2))
		archiveDelete(r, "usageTotals")
		archiveDelete(r, "route")
	}, func(r *Value) { r.Get("usageTotals").Get("inputTokens").Set("known", Number(7)) }, func(r *Value) { r.Get("route").Set("reason", String("different")) }} {
		if _, e = ReadRunArchive(feedbackRewriteReport(t, exported.Bytes, change), ""); e == nil {
			t.Fatal("self-consistent source report mismatch admitted")
		}
	}
}

func TestRunFeedbackActualPromptCompactionAndResumeIsolation(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryRunStore(MemoryRunStoreOptions{})
	session := compactionSession(store, "index", false, nil)
	session.Context.RunStore.ID = "r"
	run, e := ContextualSession(ctx, session)
	if e != nil {
		t.Fatal(e)
	}
	before, _ := store.Load(ctx, "r")
	input := feedbackInput(t, store)
	input.Rating = "bad"
	remark := "PRIVATE-FEEDBACK-ONLY"
	input.Remark = &remark
	first, e := WriteRunFeedback(ctx, store, input, PrepareRunFeedbackOptions{}, nil)
	if e != nil || first.Delivery != "deferred" || first.Candidate != nil {
		t.Fatal("active feedback not deferred", e)
	}
	after, _ := store.Load(ctx, "r")
	if !archiveEqual(before, after) {
		t.Fatal("active feedback changed execution")
	}
	request, e := run.Context.MessagesContext(ctx, []Message{TextMessage("user", "Task")}, "host", noTools())
	if e != nil || strings.Contains(MessagesValue(request).Text(), remark) {
		t.Fatal("feedback entered actual prompt", e)
	}
	projected, e := run.Context.MessagesContext(ctx, compactionHistory(), "host", noTools())
	if e != nil || !strings.Contains(MessagesValue(projected).Text(), "Completed tool calls must not be repeated") || strings.Contains(MessagesValue(projected).Text(), remark) {
		t.Fatal("feedback entered actual compaction", e)
	}
	state, e := run.Context.Snapshot("")
	if e != nil {
		t.Fatal(e)
	}
	binding := run.Context.Persistence("author")
	if e = binding.Writer.Save(ctx, *binding, state); e != nil {
		t.Fatal(e)
	}
	run.Context.Close()
	session = compactionSession(store, "index", true, nil)
	session.Context.RunStore.ID = "r"
	run, e = ContextualSession(ctx, session)
	if e != nil {
		t.Fatal(e)
	}
	resumed, e := run.Context.MessagesContext(ctx, projected, "host", noTools())
	if e != nil || strings.Contains(MessagesValue(resumed).Text(), remark) {
		t.Fatal("feedback entered resumed prompt", e)
	}
	view, e := ReadRetainedRunHistory(ctx, store, "r")
	if e != nil || len(view.Metadata()) != 1 || !view.Metadata()[0].Record.CandidateRequested {
		t.Fatal("feedback intent lost through resume", e)
	}
	run.Context.Close()
}
func TestRunFeedbackActiveIntentCancellationAndExplicitTerminalRevision(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryRunStore(MemoryRunStoreOptions{})
	raw, e := os.ReadFile("../../contract/fixtures/feedback/records.json")
	if e != nil {
		t.Fatal(e)
	}
	v, _ := ParseJSON(raw)
	checkpoint := v.Get("reports").Items()[1].Get("checkpoint").Clone()
	checkpoint.Set("status", String("running"))
	archiveDelete(checkpoint, "reason")
	if _, e = store.Commit(ctx, RunCommit{RunID: "r", CommitID: "active", Checkpoint: []byte(CanonicalJSON(checkpoint)), Events: [][]byte{}}); e != nil {
		t.Fatal(e)
	}
	input := feedbackInput(t, store)
	input.Rating = "bad"
	prepared, e := PrepareRunFeedback(ctx, store, input, PrepareRunFeedbackOptions{})
	if e != nil {
		t.Fatal(e)
	}
	first, e := CommitPreparedRunFeedback(ctx, store, prepared)
	if e != nil || !first.CandidateDeferred || first.Candidate != nil {
		t.Fatal("active candidate not deferred", e)
	}
	view, _ := ReadRetainedRunHistory(ctx, store, "r")
	old := view.Metadata()[0].Record.Feedback
	previous := HistoryBytesDigest([]byte(historyInputText(*old)))
	edit := feedbackInput(t, store)
	edit.CommitID = "cancel"
	edit.Previous = &previous
	if _, e = WriteRunFeedback(ctx, store, edit, PrepareRunFeedbackOptions{}, nil); e != nil {
		t.Fatal(e)
	}
	view, _ = ReadRetainedRunHistory(ctx, store, "r")
	current := view.Metadata()[1].Record
	if current.CandidateRequested {
		t.Fatal("good edit retained stale candidate intent")
	}
	replay, e := CommitPreparedRunFeedback(ctx, store, prepared)
	if e != nil || !replay.Superseded || replay.Candidate != nil {
		t.Fatal("superseded intent exposed", e)
	}
	checkpoint.Set("status", String("complete"))
	checkpoint.Set("reason", String("Finished"))
	native, _ := store.Load(ctx, "r")
	if _, e = store.Commit(ctx, RunCommit{RunID: "r", ExpectedRevision: native.Revision, CommitID: "terminal", Checkpoint: []byte(CanonicalJSON(checkpoint)), Events: [][]byte{}}); e != nil {
		t.Fatal(e)
	}
	previous = HistoryBytesDigest([]byte(historyInputText(*current.Feedback)))
	edit = feedbackInput(t, store)
	edit.CommitID = "explicit-terminal"
	edit.Previous = &previous
	terminal, e := WriteRunFeedback(ctx, store, edit, PrepareRunFeedbackOptions{ProposeGood: true}, nil)
	if e != nil || terminal.Candidate == nil || terminal.Delivery != "pending" {
		t.Fatal("explicit terminal candidate", e)
	}
	exported, e := ExportRunArchive(ctx, store, "r", ArchiveExportOptions{})
	if e != nil {
		t.Fatal(e)
	}
	imported, e := ReadRunArchive(exported.Bytes, "")
	if e != nil {
		t.Fatal(e)
	}
	display, e := imported.ReadDisplay(imported.Manifest().Metadata[0])
	if e != nil || !display.Document.Get("record").Get("candidateRequested").Truth() {
		t.Fatal("intent missing from archive", e)
	}
}

func TestRunFeedbackSharedCandidatesMetadataAndRelationEvidence(t *testing.T) {
	raw, e := os.ReadFile("../../contract/fixtures/feedback/candidates.json")
	if e != nil {
		t.Fatal(e)
	}
	v, _ := ParseJSON(raw)
	for _, c := range v.Get("candidates").Items() {
		got, e := ReadCorpusCandidate([]byte(CanonicalJSON(c)))
		if e != nil || historyInputText(got) != CanonicalJSON(c) {
			t.Fatal("shared candidate", e)
		}
	}
	for _, m := range v.Get("metadata").Items() {
		got, e := ReadHistoryMetadata([]byte(CanonicalJSON(m)))
		if e != nil || historyInputText(got) != CanonicalJSON(m) {
			t.Fatal("shared metadata", e)
		}
	}
	for _, r := range v.Get("relations").Items() {
		got, e := ReadHistoryRelationEvidence([]byte(CanonicalJSON(r)))
		if e != nil || CanonicalJSON(got) != CanonicalJSON(r) {
			t.Fatal("shared evidence", e)
		}
	}
	for _, r := range v.Get("invalidCandidates").Items() {
		if _, e := ReadCorpusCandidate([]byte(CanonicalJSON(r.Get("candidate")))); e == nil {
			t.Fatal("invalid candidate", r.Get("name").Str())
		}
	}
	for _, raw := range []string{"{}", "null"} {
		if _, e := ReadHistoryRelationEvidence([]byte(raw)); e == nil {
			t.Fatal("malformed evidence accepted")
		}
	}
}
func TestRunFeedbackPrivateCandidateCannotAttachAfterEdit(t *testing.T) {
	ctx := context.Background()
	store := archiveMemory(t)
	input := feedbackInput(t, store)
	input.Rating = "bad"
	prepared, e := PrepareRunFeedback(ctx, store, input, PrepareRunFeedbackOptions{})
	if e != nil {
		t.Fatal(e)
	}
	ref := prepared.Commit.Record.Candidate
	if ref == nil {
		t.Fatal("candidate missing")
	}
	if _, e = store.ReadHistoryObject(ctx, HistoryResource{Kind: "run", ID: "r"}, prepared.Source.Revision, ref.Digest, 0, ref.Bytes); e == nil {
		t.Fatal("privately staged candidate exposed")
	}
	input = feedbackInput(t, store)
	input.CommitID = "newer"
	if _, e = WriteRunFeedback(ctx, store, input, PrepareRunFeedbackOptions{}, nil); e != nil {
		t.Fatal(e)
	}
	if _, e = CommitPreparedRunFeedback(ctx, store, prepared); e == nil {
		t.Fatal("stale candidate attached")
	}
	view, e := ReadRetainedRunHistory(ctx, store, "r")
	if e != nil || len(view.Metadata()) != 1 || view.Metadata()[0].Record.Candidate != nil {
		t.Fatal("candidate leaked into edited rating", e)
	}
}

func TestRunFeedbackRawCandidateTerminalAndReportSourceGuards(t *testing.T) {
	ctx := context.Background()
	raw, e := os.ReadFile("../../contract/fixtures/feedback/candidates.json")
	if e != nil {
		t.Fatal(e)
	}
	v, _ := ParseJSON(raw)
	source, e := os.ReadFile("../../contract/fixtures/feedback/records.json")
	if e != nil {
		t.Fatal(e)
	}
	versions, _ := ParseJSON(source)
	for _, status := range []string{"running", "complete"} {
		store := NewMemoryRunStore(MemoryRunStoreOptions{})
		cp := versions.Get("reports").Items()[1].Get("checkpoint").Clone()
		cp.Set("status", String(status))
		if _, e = store.Commit(ctx, RunCommit{RunID: "r", CommitID: "source", Checkpoint: []byte(CanonicalJSON(cp)), Events: [][]byte{}}); e != nil {
			t.Fatal(e)
		}
		head, _ := store.ReadHistoryHead(ctx, "r")
		candidate := v.Get("candidates").Items()[0].Clone()
		candidate.Set("sourceView", String(head.Revision))
		if status == "running" {
			candidate.Get("review").Get("report").Set("checkpointVersion", Number(2))
		}
		object, e := NewHistoryObject([]byte(CanonicalJSON(candidate)))
		if e != nil {
			t.Fatal(e)
		}
		if e = store.StageHistoryObjects(ctx, HistoryResource{Kind: "run", ID: "r"}, []HistoryObject{object}); e != nil {
			t.Fatal(e)
		}
		record := v.Get("metadata").Items()[0].Clone()
		record.Set("candidate", historyRefJSON(object.HistoryObjectRef))
		decoded, e := ReadHistoryMetadata([]byte(CanonicalJSON(record)))
		if e != nil {
			t.Fatal(e)
		}
		if _, e = store.CommitHistoryMetadata(ctx, HistoryMetadataCommit{RunID: "r", ExpectedHistoryRevision: head.Revision, CommitID: "raw-candidate", Record: *decoded}); e == nil {
			t.Fatal("nonterminal/false-source candidate accepted", status)
		}
		after, _ := store.ReadHistoryHead(ctx, "r")
		if after.Metadata != nil {
			t.Fatal("invalid candidate published metadata")
		}
	}
	record := v.Get("metadata").Items()[2].Clone()
	archiveDelete(record, "candidateRequested")
	if _, e = ReadHistoryMetadata([]byte(CanonicalJSON(record))); e == nil {
		t.Fatal("candidate without current intent accepted")
	}
}

func TestRunFeedbackOptedInGoodFinalizedAssistantMessage(t *testing.T) {
	ctx := context.Background()
	store := archiveMemory(t)
	view, e := ReadRetainedRunHistory(ctx, store, "r")
	if e != nil {
		t.Fatal(e)
	}
	page, e := QueryHistoryEvents(view, HistoryEventQuery{Types: []string{"message"}})
	if e != nil {
		t.Fatal(e)
	}
	row := page.Items[0]
	input := feedbackInput(t, store)
	input.Target = FeedbackTarget{Kind: "assistant-message", EventSequence: row.Sequence, EventDigest: row.Source.Digest}
	result, e := WriteRunFeedback(ctx, store, input, PrepareRunFeedbackOptions{ProposeGood: true, Members: archiveMembers()}, nil)
	if e != nil || result.Candidate == nil || !archiveEqual(result.Candidate.Feedback.Target, input.Target) || len(result.Candidate.Review.Messages) != 1 || result.Candidate.Review.Messages[0].Sequence != row.Sequence || result.Candidate.Review.Messages[0].Text != "Account code [redacted: account-code]" || result.Delivery != "pending" {
		t.Fatal("opted-in message candidate", e)
	}
}

func TestRunFeedbackEveryNativeReportSourceVersionRefusal(t *testing.T) {
	raw, e := os.ReadFile("../../contract/fixtures/feedback/records.json")
	if e != nil {
		t.Fatal(e)
	}
	v, _ := ParseJSON(raw)
	ctx := context.Background()
	for _, row := range v.Get("reports").Items()[:3] {
		store := NewMemoryRunStore(MemoryRunStoreOptions{})
		if _, e = store.Commit(ctx, RunCommit{RunID: "r", CommitID: "source", Checkpoint: []byte(CanonicalJSON(row.Get("checkpoint"))), Events: [][]byte{}}); e != nil {
			t.Fatal(e)
		}
		if _, e = WriteRunFeedback(ctx, store, feedbackInput(t, store), PrepareRunFeedbackOptions{}, nil); e != nil {
			t.Fatal(e)
		}
		exported, e := ExportRunArchive(ctx, store, "r", ArchiveExportOptions{})
		if e != nil {
			t.Fatal(e)
		}
		changed := feedbackRewriteReport(t, exported.Bytes, func(r *Value) {
			version := 1
			if r.Get("checkpointVersion").Num() == 1 {
				version = 2
			}
			r.Set("checkpointVersion", Number(float64(version)))
		})
		if _, e = ReadRunArchive(changed, ""); e == nil {
			t.Fatal("false native report source version admitted", row.Get("name").Str())
		}
	}
}
