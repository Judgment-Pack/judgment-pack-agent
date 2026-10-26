package runcontext

import (
	"bytes"
	"context"
	"crypto/rand"
	"strings"
	"testing"
)

func TestHistoryRecordingChunkedPublicOriginalAndAtomicVisibility(t *testing.T) {
	ctx := context.Background()
	store := historyCompletedStore(t)
	source := `[{"role":"assistant","content":"` + strings.Repeat("x", HistoryObjectBytes) + `","extra":{"z":0.1,"a":false,"none":null}}]`
	prepared, err := PrepareHistoryOriginal(DurableScope{Execution: "r", Role: "author"}, "boundary", source, "messages", 0)
	if err != nil {
		t.Fatal(err)
	}
	head, _ := store.ReadHistoryHead(ctx, "r")
	ref, err := StageHistoryObservation(ctx, store, "r", prepared)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.ReadHistoryObject(ctx, HistoryResource{Kind: "run", ID: "r"}, head.Revision, ref.Digest, 0, HistoryObjectBytes); err == nil {
		t.Fatal("staging published original")
	}
	value := prepared.Observation.Get("value")
	var manifest HistoryChunkManifest
	for _, object := range prepared.Objects {
		if object.Digest == value.Get("digest").Str() {
			manifest, err = ReadHistoryChunkManifest(object.Value)
		}
	}
	if err != nil || len(manifest.Parts) != 2 {
		t.Fatal("original not chunked", err)
	}
	prepared.Objects[0].Value[0] ^= 1
	actual, _ := store.Load(ctx, "r")
	_, err = store.CommitHistory(ctx, RunHistoryCommit{Run: RunCommit{RunID: "r", ExpectedRevision: actual.Revision, CommitID: "original-evidence", Checkpoint: actual.Checkpoint}, Observations: []HistoryObjectRef{ref}})
	if err != nil {
		t.Fatal(err)
	}
	view, err := ReadRetainedRunHistory(ctx, store, "r")
	if err != nil {
		t.Fatal(err)
	}
	full := []byte{}
	for _, part := range manifest.Parts {
		raw, err := view.ReadObject(part)
		if err != nil {
			t.Fatal(err)
		}
		full = append(full, raw...)
	}
	if !bytes.Equal(full, []byte(source)) {
		t.Fatal("ordered public source changed")
	}
	if _, err = StageHistoryObservation(ctx, store, "r", prepared); err == nil {
		t.Fatal("altered prepared object accepted")
	}
}
func TestHistoryRecordingClosedGenesisAndCaptureSurface(t *testing.T) {
	scope := DurableScope{Execution: "r", Role: "author"}
	genesis, err := PrepareHistoryGenesis(scope)
	if err != nil || genesis.Observation.Get("kind").Str() != "genesis" {
		t.Fatal("genesis", err)
	}
	identity := HistoryCaptureIdentity{Scope: scope, DispatchID: "d", Kind: "model", Source: "provider", Tool: "model", RequestDigest: "sha256:" + strings.Repeat("a", 64), Surface: "session-model"}
	expectation, err := PrepareCaptureExpectation(identity)
	if err != nil || expectation.Observation.Get("captureKind").Str() != "model" {
		t.Fatal("expectation", err)
	}
	identity.Surface = "native-aggregate"
	if _, err = PrepareCaptureExpectation(identity); err == nil {
		t.Fatal("capture surface conflated")
	}
	genesis.Observation.Get("profile").Set("compactions", Bool(false))
	if _, err = ReadHistoryObservation([]byte(CanonicalJSON(genesis.Observation))); err == nil {
		t.Fatal("incomplete profile called full")
	}
}
func TestHistoryRecordingTypedEarlierCompactionClosure(t *testing.T) {
	ctx := context.Background()
	store := historyCompletedStore(t)
	scope := DurableScope{Execution: "r", Role: "author"}
	original, err := PrepareHistoryOriginal(scope, "boundary", `[{"role":"assistant","content":"original"}]`, "messages", 0)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := StageHistoryObservation(ctx, store, "r", original)
	if err != nil {
		t.Fatal(err)
	}
	publish := func(refs []HistoryObjectRef) error {
		held, _ := store.Load(ctx, "r")
		_, err := store.CommitHistory(ctx, RunHistoryCommit{Run: RunCommit{RunID: "r", ExpectedRevision: held.Revision, CommitID: rand.Text(), Checkpoint: held.Checkpoint}, Observations: refs})
		return err
	}
	compact, err := PrepareHistoryCompaction(scope, "boundary", []HistoryObjectRef{ref}, `"replacement"`, "index", nil, "legacy-digest")
	if err != nil {
		t.Fatal(err)
	}
	compacted, err := StageHistoryObservation(ctx, store, "r", compact)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := store.ReadHistoryHead(ctx, "r")
	for _, refs := range [][]HistoryObjectRef{{compacted}, {ref, compacted}} {
		if err = publish(refs); err == nil {
			t.Fatal("unacknowledged original accepted")
		}
	}
	now, _ := store.ReadHistoryHead(ctx, "r")
	if now.Revision != before.Revision {
		t.Fatal("partial closure publication")
	}
	if err = publish([]HistoryObjectRef{ref}); err != nil {
		t.Fatal(err)
	}
	if err = publish([]HistoryObjectRef{compacted}); err != nil {
		t.Fatal(err)
	}
	valid, _ := store.ReadHistoryHead(ctx, "r")
	for index := 0; index < 2; index++ {
		badScope := scope
		prior := []HistoryObjectRef{ref}
		if index == 1 {
			badScope.Role = "critic"
			prior = nil
		}
		bad, err := PrepareHistoryCompaction(badScope, "next", []HistoryObjectRef{ref}, `"replacement"`, "summary", prior, "legacy-digest")
		if err != nil {
			t.Fatal(err)
		}
		invalid, err := StageHistoryObservation(ctx, store, "r", bad)
		if err != nil {
			t.Fatal(err)
		}
		if err = publish([]HistoryObjectRef{invalid}); err == nil {
			t.Fatal("wrong prior kind or scope accepted")
		}
		if _, err = store.ReadHistoryObject(ctx, HistoryResource{Kind: "run", ID: "r"}, valid.Revision, invalid.Digest, 0, HistoryObjectBytes); err == nil {
			t.Fatal("failed closure made object visible")
		}
	}
	view, err := ReadRetainedRunHistory(ctx, store, "r")
	if err != nil || view.Head().Revision != valid.Revision {
		t.Fatal("valid compaction closure", err)
	}
}
