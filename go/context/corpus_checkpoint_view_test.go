package runcontext

import (
	"os"
	"testing"
)

func checkpointViewVectors(t *testing.T) *Value {
	t.Helper()
	raw, e := os.ReadFile("../../contract/fixtures/history/corpus-checkpoint-view.json")
	if e != nil {
		t.Fatal(e)
	}
	v, e := ParseJSON(raw)
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func TestCorpusCheckpointViewHandwrittenPairedVectors(t *testing.T) {
	v := checkpointViewVectors(t)
	for _, name := range []string{"exact", "derived", "legacy1", "legacy2"} {
		row := v.Get(name)
		view, e := ReadCorpusCheckpointView([]byte(CanonicalJSON(row)))
		if e != nil || !archiveEqual(view, row) {
			t.Fatal(name, e)
		}
		raw := []byte(CanonicalJSON(view.Document))
		if view.Derived != (HistoryObjectRef{HistoryBytesDigest(raw), len(raw)}) {
			t.Fatal("derived bytes")
		}
		if _, e = ReadExecution(row); e == nil {
			t.Fatal("wrapper became native")
		}
	}
	if _, e := ReadExecution(v.Get("derived").Get("document")); e == nil {
		t.Fatal("sanitized Plan became native")
	}
	if v.Get("derived").Get("source").Get("digest").Str() != "sha256:b9098ae2eb22759ae29710cbeb4bc45744d3586aa171c6530c02f6afd611efc1" || v.Get("derived").Get("derived").Get("digest").Str() != "sha256:59e3cb4e2ad3cb55626448c50910e09f2ff119ae0171580e35e87c2682fc0b40" {
		t.Fatal("independent digest fixture")
	}
}
func TestCorpusCheckpointViewRefusals(t *testing.T) {
	v := checkpointViewVectors(t)
	for _, f := range v.Get("refusals").Items() {
		t.Run(f.Str(), func(t *testing.T) {
			row := v.Get("derived").Clone()
			switch f.Str() {
			case "derived-digest":
				row.Get("derived").Set("digest", String("sha256:0000000000000000000000000000000000000000000000000000000000000000"))
			case "derived-bytes":
				row.Get("derived").Set("bytes", Number(1))
			case "source-version":
				row.Set("sourceVersion", Number(2))
			case "representation":
				row.Set("representation", String("exact"))
			case "sanitized-exact":
				row.Set("source", row.Get("derived").Clone())
				row.Set("representation", String("exact"))
			case "wrapper-member":
				row.Set("id", String("native-looking"))
			case "document-member":
				row.Get("document").Set("unknown", Bool(true))
			case "legacy-current-member":
				row.Get("document").Set("version", Number(2))
				row.Set("sourceVersion", Number(2))
			}
			if f.Str() == "document-member" || f.Str() == "legacy-current-member" {
				raw := []byte(CanonicalJSON(row.Get("document")))
				row.Set("derived", ObjectOf("digest", String(HistoryBytesDigest(raw)), "bytes", Number(float64(len(raw)))))
			}
			if _, e := ReadCorpusCheckpointView([]byte(CanonicalJSON(row))); e == nil {
				t.Fatal("malformed view accepted")
			}
		})
	}
}
