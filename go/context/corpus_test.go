package runcontext

import (
	"encoding/json"
	"errors"
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
