package runcontext

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

func TestCorpusToleranceAndTamper(t *testing.T) {
	expected := map[string]any{"gateDecisions": []any{map[string]any{"action": "refused"}}, "outcomes": []any{}, "refusals": []any{}, "timings": 1.0}
	actual := map[string]any{"gateDecisions": []any{map[string]any{"action": "refused"}}, "outcomes": []any{}, "refusals": []any{}, "timings": 2.0}
	if err := CompareCorpus(expected, actual, []string{"/timings"}); err != nil {
		t.Fatal(err)
	}
	actual["gateDecisions"] = []any{map[string]any{"action": "rewrote"}}
	if err := CompareCorpus(expected, actual, []string{"/timings"}); err == nil || err.Error() != "/gateDecisions/0/action differs" {
		t.Fatalf("tamper: %v", err)
	}
}

func TestCorpusSharedManifestArrayPointer(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "contract", "vectors", "corpus", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var vector struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(raw, &vector); err != nil {
		t.Fatal(err)
	}
	var manifest CorpusManifest
	if err := json.Unmarshal(vector.Items[0], &manifest); err != nil {
		t.Fatal(err)
	}
	expected := map[string]any{"gateDecisions": []any{}, "outcomes": []any{}, "refusals": []any{}, "drawnMarkers": []any{"left", "same"}}
	actual := map[string]any{"gateDecisions": []any{}, "outcomes": []any{}, "refusals": []any{}, "drawnMarkers": []any{"right", "same"}}
	if err := CompareCorpus(expected, actual, manifest.Tolerance); err != nil {
		t.Fatal(err)
	}
}

func TestCorpusProjectionExcludesProse(t *testing.T) {
	left, _ := CorpusExpected([]contract.RunEvent{{Message: &contract.RunEventMessage{Type: "message", Text: "old prose"}}})
	right, _ := CorpusExpected([]contract.RunEvent{{Message: &contract.RunEventMessage{Type: "message", Text: "different prose"}}})
	if err := CompareCorpus(left, right, nil); err != nil {
		t.Fatal(err)
	}
}

type corpusTestGuard struct{ refuse bool }

func (g corpusTestGuard) JSON(raw json.RawMessage, _ string, _ bool) (json.RawMessage, error) {
	if g.refuse {
		return nil, errors.New("refuse")
	}
	return json.RawMessage([]byte(string(raw))), nil
}

type corpusReplacingGuard struct{}

func (corpusReplacingGuard) JSON(raw json.RawMessage, _ string, _ bool) (json.RawMessage, error) {
	return json.RawMessage(strings.ReplaceAll(string(raw), "token", "[REDACTED]")), nil
}

type corpusTestSink struct {
	called bool
	files  map[string][]byte
}

func (s *corpusTestSink) WriteCorpus(_ string, files map[string][]byte) error {
	s.called = true
	s.files = files
	return nil
}
func TestCorpusRedactionRefusalWritesNothing(t *testing.T) {
	sink := &corpusTestSink{}
	event, err := WriteCorpusEntry(CorpusEntry{Manifest: CorpusManifest{ID: "refused"}, Requests: json.RawMessage(`[]`), ToolResults: json.RawMessage(`[]`), Checkpoint: json.RawMessage(`{}`)}, corpusTestGuard{refuse: true}, sink)
	if err != nil {
		t.Fatal(err)
	}
	if sink.called {
		t.Fatal("sink called")
	}
	if event.Guardrail == nil || event.Guardrail.Reason == nil || *event.Guardrail.Reason != "record refused" {
		t.Fatalf("event: %#v", event)
	}
}

func TestCorpusRedactsBeforeTheSink(t *testing.T) {
	manifest := CorpusManifest{ID: "redacted", PackageVersion: "0.8.0", Policy: map[string]any{"taskClass": "boundary"}}
	manifest.Files.Requests, manifest.Files.ToolResults, manifest.Files.Events, manifest.Files.Checkpoint = "requests.json", "tool-results.json", "events.json", "checkpoint.json"
	manifest.Expected.File = "expected.json"
	sink := &corpusTestSink{}
	event, err := WriteCorpusEntry(CorpusEntry{Manifest: manifest, Requests: json.RawMessage(`[]`), ToolResults: json.RawMessage(`[]`), Checkpoint: json.RawMessage(`{"secret":"token"}`)}, corpusReplacingGuard{}, sink)
	if err != nil {
		t.Fatal(err)
	}
	if event.Recorded == nil {
		t.Fatalf("event: %#v", event)
	}
	if strings.Contains(string(sink.files["checkpoint.json"]), "token") || !strings.Contains(string(sink.files["checkpoint.json"]), "[REDACTED]") {
		t.Fatalf("checkpoint: %s", sink.files["checkpoint.json"])
	}
}

func TestCorpusInvalidManifestWritesNothing(t *testing.T) {
	manifest := CorpusManifest{ID: "INVALID ID", PackageVersion: "0.8.0", Policy: map[string]any{"taskClass": "boundary"}}
	manifest.Files.Requests, manifest.Files.ToolResults, manifest.Files.Events, manifest.Files.Checkpoint = "requests.json", "tool-results.json", "events.json", "checkpoint.json"
	manifest.Expected.File = "expected.json"
	sink := &corpusTestSink{}
	event, err := WriteCorpusEntry(CorpusEntry{Manifest: manifest, Requests: json.RawMessage(`[]`), ToolResults: json.RawMessage(`[]`), Checkpoint: json.RawMessage(`{}`)}, corpusTestGuard{}, sink)
	if err != nil {
		t.Fatal(err)
	}
	if sink.called {
		t.Fatal("sink called")
	}
	if event.Guardrail == nil || event.Guardrail.Reason == nil || *event.Guardrail.Reason != "record refused" {
		t.Fatalf("event: %#v", event)
	}
}
