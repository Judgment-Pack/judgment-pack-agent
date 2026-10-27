package runcontext

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

func archiveMemory(t *testing.T) *MemoryRunStore {
	t.Helper()
	store := NewMemoryRunStore(MemoryRunStoreOptions{Now: func() time.Time { return time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC) }})
	source, _ := NewStoredChatScope(store, "chat", NewChatContext("shared"), nil)
	session, e := ContextualSession(context.Background(), historyStoredSession(store, source, "r", false))
	if e != nil {
		t.Fatal(e)
	}
	boundary := NewBoundary(session)
	historyAssertNoFailure(t, boundary.Open())
	for _, text := range []string{"Account code SECRET-123", "second"} {
		events, _ := boundary.Event(contract.RunEvent{Message: &contract.RunEventMessage{Type: "message", Text: text}})
		historyAssertNoFailure(t, events)
	}
	events, _ := boundary.Event(contract.RunEvent{End: &contract.RunEventEnd{Type: "end"}})
	historyAssertNoFailure(t, events)
	boundary.Close()
	return store
}
func archiveMembers() PolicyMembers {
	return PolicyMembers{"outputPatterns": {Value: []any{map[string]any{"name": "account-code", "pattern": "SECRET-[0-9]+"}}}}
}
func TestHistoryArchiveActualMemoryAndDisplayQuery(t *testing.T) {
	ctx := context.Background()
	store := archiveMemory(t)
	before, _ := store.Load(ctx, "r")
	result, e := ExportRunArchive(ctx, store, "r", ArchiveExportOptions{Members: archiveMembers()})
	if e != nil {
		t.Fatal("export", e)
	}
	if bytes.Contains(result.Bytes, []byte("SECRET-123")) || result.Manifest.Coverage != "recording-incomplete" || result.Digest != HistoryBytesDigest(result.Bytes) {
		t.Fatal("redaction/coverage/digest")
	}
	view, e := ReadRunArchive(result.Bytes, result.Digest)
	if e != nil {
		t.Fatal("read", e)
	}
	if !bytes.Equal(view.CanonicalBytes(), result.Bytes) {
		t.Fatal("canonical bytes changed")
	}
	page, e := QueryHistoryEvents(view, HistoryEventQuery{Types: []string{"message"}})
	if e != nil || len(page.Items) != 2 || page.Items[0].Event.Get("text").Str() != "Account code [redacted: account-code]" || page.Items[0].Representation == nil || page.Items[0].Representation.Kind != "redacted-view" {
		t.Fatal("redacted query", e)
	}
	relations, e := FindHistoryRelations(view, "job", "fake")
	if e != nil || relations.Kind != "unavailable" {
		t.Fatal("invented relation")
	}
	after, _ := store.Load(ctx, "r")
	if !archiveEqual(before, after) {
		t.Fatal("archive changed execution")
	}
	cp, e := view.ReadDisplay(*view.Head().Checkpoint)
	if e != nil {
		t.Fatal(e)
	}
	cp.Document.Set("id", String("changed"))
	again, _ := view.ReadDisplay(*view.Head().Checkpoint)
	if again.Document.Get("id").Str() != "r" {
		t.Fatal("display alias")
	}
	if _, e = ExportRunArchive(ctx, store, "r", ArchiveExportOptions{Members: archiveMembers(), RequireComplete: true}); e == nil {
		t.Fatal("strict incomplete export admitted")
	}
	if _, e = ReadRunArchive(result.Bytes, "sha256:"+strings.Repeat("0", 64)); e == nil {
		t.Fatal("replacement pin admitted")
	}
}
func archiveResign(t *testing.T, v *Value) []byte {
	t.Helper()
	manifest := v.Get("manifest")
	body := manifest.Clone()
	archiveDelete(body, "digest")
	manifest.Set("digest", String(HistoryBytesDigest([]byte(CanonicalJSON(body)))))
	return []byte(CanonicalJSON(v))
}
func TestHistoryArchiveCorruption(t *testing.T) {
	result, e := ExportRunArchive(context.Background(), archiveMemory(t), "r", ArchiveExportOptions{Members: archiveMembers()})
	if e != nil {
		t.Fatal(e)
	}
	tests := []func(*Value){func(v *Value) { v.Set("version", Number(2)) }, func(v *Value) { v.Set("path", String("../escape")) }, func(v *Value) { v.Get("objects").a[0].Set("bytes", String("AAAA")) }, func(v *Value) {
		a := v.Get("objects").a
		for i, j := 0, len(a)-1; i < j; i, j = i+1, j-1 {
			a[i], a[j] = a[j], a[i]
		}
	}, func(v *Value) { a := v.Get("objects"); a.a = a.a[:len(a.a)-1] }, func(v *Value) { a := v.Get("objects"); a.Append(a.a[0].Clone()) }, func(v *Value) {
		a := v.Get("manifest").Get("commits")
		for i, j := 0, len(a.a)-1; i < j; i, j = i+1, j-1 {
			a.a[i], a.a[j] = a.a[j], a.a[i]
		}
	}, func(v *Value) { a := v.Get("manifest").Get("commits"); a.a = a.a[:len(a.a)-1] }, func(v *Value) { v.Get("manifest").Get("members").a[0].Set("kind", String("future")) }, func(v *Value) {
		a := v.Get("manifest").Get("source")
		a.Set("eventCount", Number(a.Get("eventCount").Num()+1))
	}, func(v *Value) { v.Get("manifest").Set("coverage", String("complete")) }, func(v *Value) {
		for _, row := range v.Get("manifest").Get("members").a {
			if row.Get("representation").Get("kind").Str() == "redacted-view" {
				row.Set("representation", ObjectOf("kind", String("exact")))
				return
			}
		}
	}}
	for i, change := range tests {
		v := MustParseJSON(string(result.Bytes))
		change(v)
		if _, e = ReadRunArchive(archiveResign(t, v), ""); e == nil {
			t.Fatalf("corruption %d admitted", i)
		}
	}
	if _, e = ReadRunArchive(bytes.Replace(result.Bytes, []byte(`"version":1`), []byte(`"version":1,"version":1`), 1), ""); e == nil {
		t.Fatal("duplicate JSON admitted")
	}
	if _, e = ReadRunArchive(append(bytes.Clone(result.Bytes), ' '), ""); e == nil {
		t.Fatal("noncanonical JSON admitted")
	}
	// The caller cannot deserialize authority into an imported view.
	data, _ := json.Marshal(ImportedRunHistory{})
	if string(data) != "{}" {
		t.Fatal("private imported state exported")
	}
}

func TestHistoryArchiveHandwrittenSharedVectors(t *testing.T) {
	raw, e := os.ReadFile("../../contract/fixtures/history/archive.json")
	if e != nil {
		t.Fatal(e)
	}
	vectors, e := ParseJSON(raw)
	if e != nil {
		t.Fatal(e)
	}
	for _, row := range vectors.Get("cases").Items() {
		t.Run(row.Get("name").Str(), func(t *testing.T) {
			canonical := []byte(CanonicalJSON(row.Get("archive")))
			view, e := ReadRunArchive(canonical, row.Get("expectedDigest").Str())
			if e != nil {
				t.Fatal(e)
			}
			if !bytes.Equal(view.CanonicalBytes(), canonical) {
				t.Fatal("literal canonical bytes changed")
			}
			page, e := QueryHistoryEvents(view, HistoryEventQuery{Types: []string{"message"}})
			if e != nil || len(page.Items) != 1 || page.Items[0].Event.Get("text").Str() != row.Get("expectedMessage").Str() || page.Items[0].Representation.Kind != row.Get("expectedRepresentation").Str() {
				t.Fatal("shared query", e)
			}
		})
	}
}

func TestHistoryArchiveProtectedScopeRole(t *testing.T) {
	raw, e := os.ReadFile("../../contract/fixtures/history/archive.json")
	if e != nil {
		t.Fatal(e)
	}
	vectors, _ := ParseJSON(raw)
	a := vectors.Get("cases").Items()[1].Get("archive").Clone()
	manifest := a.Get("manifest")
	var mapping *Value
	for _, row := range manifest.Get("mappings").Items() {
		if row.Get("kind").Str() == "event" {
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
	valueRaw, e := base64.StdEncoding.DecodeString(blob.Get("bytes").Str())
	if e != nil {
		t.Fatal(e)
	}
	view, _ := ParseJSON(valueRaw)
	view.Get("document").Get("scope").Set("role", String("[redacted: forged-role]"))
	view.Set("redactions", Array(String("/event/text"), String("/scope/role")))
	valueRaw = []byte(CanonicalJSON(view))
	ref := ObjectOf("digest", String(HistoryBytesDigest(valueRaw)), "bytes", Number(float64(len(valueRaw))))
	mapping.Set("exported", ref.Clone())
	mapping.Get("representation").Set("redactions", view.Get("redactions").Clone())
	for _, row := range manifest.Get("members").Items() {
		if row.Get("digest").Str() == old {
			row.Set("digest", ref.Get("digest"))
			row.Set("bytes", ref.Get("bytes"))
			row.Set("representation", mapping.Get("representation").Clone())
		}
	}
	blob.Set("digest", ref.Get("digest"))
	blob.Set("bytes", String(base64.StdEncoding.EncodeToString(valueRaw)))
	for _, array := range []*Value{manifest.Get("members"), a.Get("objects")} {
		sort.Slice(array.a, func(i, j int) bool { return archiveTestKey(array.a[i]) < archiveTestKey(array.a[j]) })
	}
	if _, e = ReadRunArchive(archiveResign(t, a), ""); e == nil {
		t.Fatal("protected scope role admitted")
	}
}

func TestHistoryArchiveRedactedPayloadDAG(t *testing.T) {
	ctx := context.Background()
	store := archiveMemory(t)
	scope := DurableScope{Execution: "r", Role: "author"}
	original, e := PrepareHistoryOriginal(scope, "boundary", `[{"text":"SECRET-123"}]`, "messages", 0)
	if e != nil {
		t.Fatal(e)
	}
	commit := func(prepared PreparedHistoryObservation, id string) HistoryObjectRef {
		t.Helper()
		ref, e := StageHistoryObservation(ctx, store, "r", prepared)
		if e != nil {
			t.Fatal(e)
		}
		head, _ := store.Load(ctx, "r")
		_, e = store.CommitHistory(ctx, RunHistoryCommit{Run: RunCommit{RunID: "r", ExpectedRevision: head.Revision, CommitID: id, Checkpoint: head.Checkpoint, Events: [][]byte{}}, Observations: []HistoryObjectRef{ref}})
		if e != nil {
			t.Fatal(e)
		}
		return ref
	}
	originalRef := commit(original, "original")
	compaction, e := PrepareHistoryCompaction(scope, "boundary", []HistoryObjectRef{originalRef}, `{"content":"Summary SECRET-123"}`, "summary", []HistoryObjectRef{}, "legacy-digest")
	if e != nil {
		t.Fatal(e)
	}
	compactionRef := commit(compaction, "compaction")
	request, e := PrepareHistoryRequest(scope, "dispatch", "model", "model", `{"messages":[{"role":"user","content":"SECRET-123"}]}`, []HistoryObjectRef{compactionRef}, []HistoryObjectRef{originalRef})
	if e != nil {
		t.Fatal(e)
	}
	commit(request, "request")
	result, e := ExportRunArchive(ctx, store, "r", ArchiveExportOptions{Members: archiveMembers()})
	if e != nil {
		t.Fatal(e)
	}
	view, e := ReadRunArchive(result.Bytes, "")
	if e != nil {
		t.Fatal(e)
	}
	derived := 0
	for _, mapping := range view.Manifest().Mappings {
		if mapping.Representation.Kind == "redacted-view" && strings.HasSuffix(mapping.Kind, "-chunk") {
			derived++
			display, e := view.ReadDisplay(mapping.Source)
			if e != nil || strings.Contains(display.Document.Str(), "SECRET-123") || mapping.Source.Digest == mapping.Exported.Digest {
				t.Fatal("redacted source mapping", e)
			}
		}
	}
	if derived != 3 {
		t.Fatalf("expected three derived payload chunks, got %d", derived)
	}
	display, e := view.ReadDisplay(compactionRef)
	if e != nil || !archiveEqual(display.Document.Get("originals"), []HistoryObjectRef{originalRef}) || !archiveEqual(display.Document.Get("replacement"), compaction.Observation.Get("replacement")) {
		t.Fatal("source DAG changed", e)
	}
	if !bytes.Equal(view.CanonicalBytes(), result.Bytes) {
		t.Fatal("canonical bytes changed")
	}
}

type archiveChangingHead struct {
	HistoryRunStore
	reads int
}

func (s *archiveChangingHead) ReadHistoryHead(ctx context.Context, id string) (*RunHistoryHead, error) {
	head, e := s.HistoryRunStore.ReadHistoryHead(ctx, id)
	s.reads++
	if s.reads == 3 && head != nil {
		head.Revision = "changed"
	}
	return head, e
}
func TestHistoryArchiveFinalHeadChange(t *testing.T) {
	store := archiveMemory(t)
	wrapped := &archiveChangingHead{HistoryRunStore: store}
	_, e := ExportRunArchive(context.Background(), wrapped, "r", ArchiveExportOptions{Members: archiveMembers()})
	var changed *HistoryChanged
	if !errors.As(e, &changed) {
		t.Fatal("archive final head change was not refused", e)
	}
}

func TestHistoryArchiveConflictingSourceMapping(t *testing.T) {
	raw, e := os.ReadFile("../../contract/fixtures/history/archive.json")
	if e != nil {
		t.Fatal(e)
	}
	v, _ := ParseJSON(raw)
	a := v.Get("cases").Items()[1].Get("archive").Clone()
	m := a.Get("manifest")
	rows := m.Get("mappings")
	var original *Value
	for _, row := range rows.Items() {
		if row.Get("kind").Str() == "event" {
			original = row
			break
		}
	}
	var blob *Value
	for _, row := range a.Get("objects").Items() {
		if row.Get("digest").Str() == original.Get("exported").Get("digest").Str() {
			blob = row
			break
		}
	}
	bytes, e := base64.StdEncoding.DecodeString(blob.Get("bytes").Str())
	if e != nil {
		t.Fatal(e)
	}
	wrapper, e := ParseJSON(bytes)
	if e != nil {
		t.Fatal(e)
	}
	wrapper.Get("document").Get("event").Set("text", String("Alternative [redacted: fixture]"))
	bytes = []byte(CanonicalJSON(wrapper))
	ref := historyRefJSON(HistoryObjectRef{Digest: HistoryBytesDigest(bytes), Bytes: len(bytes)})
	conflict := original.Clone()
	conflict.Set("exported", ref.Clone())
	rows.Append(conflict)
	m.Get("members").Append(ObjectOf("digest", ref.Get("digest"), "bytes", ref.Get("bytes"), "source", original.Get("source").Clone(), "kind", original.Get("kind"), "representation", original.Get("representation").Clone()))
	a.Get("objects").Append(ObjectOf("digest", ref.Get("digest"), "bytes", String(base64.StdEncoding.EncodeToString(bytes))))
	sort.Slice(rows.a, func(i, j int) bool {
		return archiveTestKey(rows.a[i]) < archiveTestKey(rows.a[j])
	})
	for _, array := range []*Value{m.Get("members"), a.Get("objects")} {
		sort.Slice(array.a, func(i, j int) bool { return archiveTestKey(array.a[i]) < archiveTestKey(array.a[j]) })
	}
	if _, e = ReadRunArchive(archiveResign(t, a), ""); e == nil {
		t.Fatal("conflicting source-to-derived mappings accepted")
	}
}

func archiveTestKey(v *Value) string {
	if v.Has("source") {
		return archiveRoleKey(v.Get("source").Get("digest").Str(), v.Get("kind").Str())
	}
	return v.Get("digest").Str()
}
func TestHistoryArchiveHandwrittenTypedRoles(t *testing.T) {
	raw, e := os.ReadFile("../../contract/fixtures/history/typed-role.json")
	if e != nil {
		t.Fatal(e)
	}
	v, e := ParseJSON(raw)
	if e != nil {
		t.Fatal(e)
	}
	for _, row := range v.Get("cases").Items() {
		t.Run(row.Get("name").Str(), func(t *testing.T) {
			raw := []byte(CanonicalJSON(row.Get("archive")))
			view, e := ReadRunArchive(raw, row.Get("expectedDigest").Str())
			if !row.Get("valid").Truth() {
				if e == nil {
					t.Fatal("invalid typed role admitted")
				}
				return
			}
			if e != nil {
				t.Fatal("valid typed role refused", e)
			}
			if !bytes.Equal(view.CanonicalBytes(), raw) || len(view.Manifest().Members) <= len(view.objects) {
				t.Fatal("canonical bytes/blob aliases lost")
			}
			if row.Has("ambiguous") {
				ref := archiveRef(row.Get("ambiguous"))
				if _, e = view.ReadDisplay(ref); e == nil {
					t.Fatal("ambiguous role lookup admitted")
				}
				count := 0
				for _, m := range view.Manifest().Mappings {
					if m.Source.Digest == ref.Digest {
						count++
						if _, e = view.ReadDisplay(ref, m.Kind); e != nil {
							t.Fatal("explicit role refused", e)
						}
					}
				}
				if count < 2 {
					t.Fatal("fixture has no alias")
				}
			}
			corpus, e := ReadArchiveCapturedCorpus(view)
			if row.Get("captureReady").Truth() {
				if e != nil || corpus == nil {
					t.Fatal("exact native corpus unavailable", e)
				}
			} else if e == nil {
				t.Fatal("derived or incomplete corpus made native")
			}
		})
	}
}

func TestHistoryArchiveActualTypedRolePayloadRedaction(t *testing.T) {
	ctx := context.Background()
	store := archiveMemory(t)
	scope := DurableScope{Execution: "r", Role: "author"}
	text := `{"text":"SECRET-123"}`
	original, e := PrepareHistoryOriginal(scope, "boundary", text, "context", 0)
	if e != nil {
		t.Fatal(e)
	}
	commit := func(p PreparedHistoryObservation, id string) {
		t.Helper()
		ref, e := StageHistoryObservation(ctx, store, "r", p)
		if e != nil {
			t.Fatal(e)
		}
		head, _ := store.Load(ctx, "r")
		_, e = store.CommitHistory(ctx, RunHistoryCommit{Run: RunCommit{RunID: "r", ExpectedRevision: head.Revision, CommitID: id, Checkpoint: head.Checkpoint, Events: [][]byte{}}, Observations: []HistoryObjectRef{ref}})
		if e != nil {
			t.Fatal(e)
		}
	}
	commit(original, "role-original")
	request, e := PrepareHistoryRequest(scope, "dispatch", "model", "model", text, []HistoryObjectRef{}, []HistoryObjectRef{original.Ref})
	if e != nil {
		t.Fatal(e)
	}
	commit(request, "role-request")
	result, e := ExportRunArchive(ctx, store, "r", ArchiveExportOptions{Members: archiveMembers()})
	if e != nil {
		t.Fatal("typed role export", e)
	}
	view, e := ReadRunArchive(result.Bytes, "")
	if e != nil {
		t.Fatal(e)
	}
	if bytes.Contains(result.Bytes, []byte("SECRET-123")) || !bytes.Equal(result.Bytes, view.CanonicalBytes()) {
		t.Fatal("role redaction/bytes")
	}
	var archive runArchive
	if json.Unmarshal(result.Bytes, &archive) != nil {
		t.Fatal("archive fixture decode")
	}
	for _, blob := range archive.Objects {
		raw, e := base64.StdEncoding.DecodeString(blob.Bytes)
		if e != nil || bytes.Contains(raw, []byte("SECRET-123")) {
			t.Fatal("raw sensitive bytes retained through alias", e)
		}
	}

	aliases := []ArchiveMapping{}
	for _, m := range view.Manifest().Mappings {
		if m.Source.Digest == HistoryBytesDigest([]byte(text)) && (m.Kind == "source-chunk" || m.Kind == "request-chunk") {
			aliases = append(aliases, m)
		}
	}
	if len(aliases) != 2 || aliases[0].Source != aliases[1].Source {
		t.Fatal("role alias missing")
	}
	if _, e = view.ReadDisplay(aliases[0].Source); e == nil {
		t.Fatal("ambiguous role lookup admitted")
	}
	for _, m := range aliases {
		d, e := view.ReadDisplay(m.Source, m.Kind)
		if e != nil || !strings.Contains(d.Document.Str(), "[redacted: account-code]") {
			t.Fatal("whole role redaction", e)
		}
	}
}

func TestHistoryArchiveSharedExactNativeBindings(t *testing.T) {
	port, objects, _ := publicationCaptureFixture(t)
	corpus, e := ReadCapturedCorpus(port)
	if e != nil {
		t.Fatal("exact native-binding corpus unavailable", e)
	}
	requests, e := ParseJSON(corpus.Requests)
	if e != nil || len(requests.Items()) != 2 {
		t.Fatal("missing exact requests", e)
	}
	native := requests.Items()[1]
	if native.Get("response").Get("headers").Get("x-jp-native-tool-bindings").Str() != "{}" || !native.Get("request").Get("tools").IsArray() || len(native.Get("request").Get("tools").Items()) != 0 {
		t.Fatal("native empty tool allocation changed")
	}
	found := false
	for _, commit := range port.commits {
		for _, ref := range commit.Observations {
			observation, e := ParseJSON(objects[ref.Digest])
			if e != nil {
				t.Fatal(e)
			}
			if observation.Get("kind").Str() == "capture-request" && observation.Get("captureKind").Str() == "agent" {
				bindings := archiveRef(observation.Get("nativeBindings"))
				if bindings.Bytes != 2 || string(objects[bindings.Digest]) != "{}" {
					t.Fatal("original binding evidence absent")
				}
				found = true
			}
		}
	}
	if !found {
		t.Fatal("native observation absent")
	}
}
