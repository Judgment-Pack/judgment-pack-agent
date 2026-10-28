package runcontext

import (
	"encoding/json"
	"testing"
)

func TestCapturePortableManifestUsesContractKeys(t *testing.T) {
	var m CorpusManifest
	m.ID = "capture"
	m.PackageVersion = "0.11.0"
	m.Policy = map[string]any{}
	m.Files.Requests = "requests.json"
	m.Files.ToolResults = "tool-results.json"
	m.Files.Events = "events.json"
	m.Files.Checkpoint = "checkpoint.json"
	m.Expected.File = "expected.json"
	m.Tolerance = []string{}
	raw, e := json.Marshal(m)
	if e != nil {
		t.Fatal(e)
	}
	v, e := ParseJSON(raw)
	if e != nil || !snapshotObject(v.Get("files"), []string{"requests", "toolResults", "events", "checkpoint"}) {
		t.Fatal("portable manifest keys violate contract", string(raw))
	}
}
