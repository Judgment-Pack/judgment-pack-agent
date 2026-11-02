package capturetests

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/eino"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/relay"
	"net/http"
	"strings"
	"testing"
)

func TestOriginalLiveCaptureAcceptedPlanPrivacy(t *testing.T) {
	ctx := context.Background()
	store := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
	sink := &originalCaptureSink{}
	run := originalCaptureRun(store, sink)
	run.Context.Objective = ""
	run.Context.Phase = "work"
	run.Context.Plan = runcontext.MustParseJSON(`{"id":"live-private-plan","revision":1,"objective":"Read source.","steps":["Read SECRET-PLAN prose"],"questions":[],"references":[],"status":"accepted","createdAt":"2026-10-26T00:00:00.000Z","acceptedAt":"2026-10-26T00:00:00.000Z"}`)
	run.Policy = runcontext.PolicyLayer{"version": 2, "records": map[string]any{"redactOnStore": false}, "outputPatterns": []any{map[string]any{"name": "plan-prose", "pattern": "SECRET-PLAN"}}}
	run.Model = func(context.Context, string, relay.Request) (*http.Response, error) { return usageReply(), nil }
	if e := eino.New().Run(ctx, run); e != nil {
		t.Fatal(e)
	}
	if sink.calls != 1 {
		t.Fatal("original private Plan capture absent", sink.calls)
	}
	head, e := store.Load(ctx, "original-live")
	if e != nil {
		t.Fatal(e)
	}
	native, e := runcontext.ReadExecution(runcontext.MustParseJSON(string(head.Checkpoint)))
	if e != nil {
		t.Fatal(e)
	}
	if native == nil || !strings.Contains(string(head.Checkpoint), "SECRET-PLAN") {
		t.Fatal("original execution Plan changed")
	}
	retained, e := runcontext.ReadRetainedRunHistory(ctx, store, "original-live")
	if e != nil {
		t.Fatal(e)
	}
	view, e := runcontext.ReadCapturedCorpusView(retained)
	if e != nil {
		t.Fatal(e)
	}
	if view.Checkpoint.Representation != "derived" {
		t.Fatal("derived Plan not marked")
	}
	for _, raw := range sink.files {
		if strings.Contains(string(raw), "SECRET-PLAN") {
			t.Fatal("sink leaked private Plan")
		}
	}
	if _, e := runcontext.ReadExecution(view.Checkpoint.Document); e == nil {
		t.Fatal("derived Plan granted native execution authority")
	}
	members := runcontext.PolicyMembers{"outputPatterns": {Value: []any{map[string]any{"name": "plan-prose", "pattern": "SECRET-PLAN"}}}}
	archive, e := runcontext.ExportRunArchive(ctx, store, "original-live", runcontext.ArchiveExportOptions{Members: members})
	if e != nil {
		t.Fatal(e)
	}
	imported, e := runcontext.ReadRunArchive(archive.Bytes, archive.Digest)
	if e != nil {
		t.Fatal(e)
	}
	captured, e := runcontext.ReadArchiveCapturedCorpusView(imported)
	if e != nil {
		t.Fatal(e)
	}
	var doc struct {
		Objects []struct {
			Bytes string `json:"bytes"`
		} `json:"objects"`
	}
	_ = json.Unmarshal(archive.Bytes, &doc)
	for _, blob := range doc.Objects {
		raw, _ := base64.StdEncoding.DecodeString(blob.Bytes)
		if strings.Contains(string(raw), "SECRET-PLAN") {
			t.Fatal("archive leaked private Plan")
		}
	}
	replayOriginalCapture(t, captured.Entry, false, false)
}
