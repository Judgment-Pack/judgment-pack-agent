package runcontext

// ADR-0022: portable, redacted evaluation-corpus entries and their prose-free comparison.

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

var CorpusFiles = map[string]string{"manifest": "manifest.json", "requests": "requests.json", "toolResults": "tool-results.json", "events": "events.json", "checkpoint": "checkpoint.json", "expected": "expected.json"}

type CorpusManifest struct {
	ID             string         `json:"id"`
	PackageVersion string         `json:"packageVersion"`
	Policy         map[string]any `json:"policy"`
	Files          struct {
		Requests, ToolResults, Events, Checkpoint string
	} `json:"files"`
	Expected struct {
		File string `json:"file"`
	} `json:"expected"`
	Tolerance []string `json:"tolerance"`
}

type CorpusEntry struct {
	Manifest    CorpusManifest
	Requests    json.RawMessage
	ToolResults json.RawMessage
	Events      []contract.RunEvent
	Checkpoint  json.RawMessage
	Expected    map[string]any
}

type CorpusGuard interface {
	JSON(raw json.RawMessage, where string, record bool) (json.RawMessage, error)
}

type CorpusSink interface {
	WriteCorpus(id string, files map[string][]byte) error
}

func corpusProjection(events []contract.RunEvent) (map[string]any, error) {
	gate, outcomes, refusals := []any{}, []any{}, []any{}
	var proposalDigest any
	var critic any
	for _, event := range events {
		raw, err := json.Marshal(event)
		if err != nil {
			return nil, err
		}
		var row map[string]any
		if err := json.Unmarshal(raw, &row); err != nil {
			return nil, err
		}
		switch row["type"] {
		case "guardrail":
			decision := map[string]any{"tool": row["tool"], "action": row["action"]}
			if reason, ok := row["reason"]; ok {
				decision["reason"] = reason
			}
			gate = append(gate, decision)
			if row["action"] == "refused" {
				refusals = append(refusals, decision)
			}
		case "tool_result":
			outcomes = append(outcomes, map[string]any{"type": "tool_result", "name": row["name"], "isError": row["isError"]})
		case "error":
			failure, _ := row["failure"].(map[string]any)
			outcomes = append(outcomes, map[string]any{"type": "error", "code": failure["code"], "outcome": failure["outcome"]})
		case "proposal":
			outcomes = append(outcomes, map[string]any{"type": "proposal"})
			proposalDigest = row["digest"]
			if held, ok := row["critique"].(map[string]any); ok {
				critic = map[string]any{"verdict": held["refuted"], "reasons": held["checks"]}
			}
		}
	}
	out := map[string]any{"gateDecisions": gate, "outcomes": outcomes, "refusals": refusals}
	if proposalDigest != nil {
		out["proposalDigest"] = proposalDigest
	}
	if critic != nil {
		out["critic"] = critic
	}
	return out, nil
}

func CorpusExpected(events []contract.RunEvent) (map[string]any, error) {
	return corpusProjection(events)
}

func canonicalBytes(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	held, err := ParseJSON(raw)
	if err != nil {
		return nil, err
	}
	return []byte(CanonicalJSON(held)), nil
}

func WriteCorpusEntry(entry CorpusEntry, guard CorpusGuard, sink CorpusSink) (contract.RunEvent, error) {
	expected, err := corpusProjection(entry.Events)
	if err != nil {
		return corpusRefused(), nil
	}
	entry.Expected = expected
	values := map[string]any{"manifest.json": entry.Manifest, "requests.json": json.RawMessage(entry.Requests), "tool-results.json": json.RawMessage(entry.ToolResults), "events.json": entry.Events, "checkpoint.json": json.RawMessage(entry.Checkpoint), "expected.json": entry.Expected}
	files := map[string][]byte{}
	total := 0
	for name, value := range values {
		raw, problem := canonicalBytes(value)
		if problem == nil {
			raw, problem = guard.JSON(raw, "message", true)
		}
		if problem != nil {
			return corpusRefused(), nil
		}
		files[name] = raw
		total += len(raw)
	}
	if err := sink.WriteCorpus(entry.Manifest.ID, files); err != nil {
		return contract.RunEvent{}, err
	}
	return recordedEvent(entry.Manifest.ID, total), nil
}

func recordedEvent(id string, bytes int) contract.RunEvent {
	// The generated Recorded member is supplied by go generate after corpus-entry and recorded land.
	raw := fmt.Sprintf(`{"type":"recorded","id":%q,"bytes":%d}`, id, bytes)
	var event contract.RunEvent
	_ = json.Unmarshal([]byte(raw), &event)
	return event
}
func corpusRefused() contract.RunEvent {
	reason := "record refused"
	return contract.RunEvent{Guardrail: &contract.RunEventGuardrail{Type: "guardrail", Tool: "record", Action: "refused", Reason: &reason, Detail: "The output guard could not redact the corpus entry; no recording was written."}}
}

func deletePointer(value any, pointer string) {
	if !strings.HasPrefix(pointer, "/") {
		return
	}
	parts := strings.Split(strings.TrimPrefix(pointer, "/"), "/")
	var at any = value
	for _, part := range parts[:len(parts)-1] {
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		object, ok := at.(map[string]any)
		if !ok {
			return
		}
		at = object[part]
	}
	if object, ok := at.(map[string]any); ok {
		delete(object, parts[len(parts)-1])
	}
}
func firstCorpusDifference(left, right any, path string) string {
	a, ok := left.(map[string]any)
	b, bok := right.(map[string]any)
	if ok && bok {
		keys := map[string]bool{}
		for key := range a {
			keys[key] = true
		}
		for key := range b {
			keys[key] = true
		}
		names := make([]string, 0, len(keys))
		for key := range keys {
			names = append(names, key)
		}
		sort.Strings(names)
		for _, key := range names {
			if found := firstCorpusDifference(a[key], b[key], path+"/"+key); found != "" {
				return found
			}
		}
		return ""
	}
	aa, ok := left.([]any)
	bb, bok := right.([]any)
	if ok && bok {
		if len(aa) != len(bb) {
			return path + "/length"
		}
		for index := range aa {
			if found := firstCorpusDifference(aa[index], bb[index], fmt.Sprintf("%s/%d", path, index)); found != "" {
				return found
			}
		}
		return ""
	}
	x, _ := json.Marshal(left)
	y, _ := json.Marshal(right)
	if string(x) != string(y) {
		if path == "" {
			return "/"
		}
		return path
	}
	return ""
}
func CompareCorpus(expected, actual map[string]any, tolerance []string) error {
	x, _ := json.Marshal(expected)
	y, _ := json.Marshal(actual)
	var left, right map[string]any
	_ = json.Unmarshal(x, &left)
	_ = json.Unmarshal(y, &right)
	for _, pointer := range tolerance {
		deletePointer(left, pointer)
		deletePointer(right, pointer)
	}
	if member := firstCorpusDifference(left, right, ""); member != "" {
		return fmt.Errorf("%s differs", member)
	}
	return nil
}
func ReplayCorpus(entry CorpusEntry) error {
	if len(entry.Requests) == 0 || len(entry.ToolResults) == 0 {
		return errors.New("recorded request or tool-result file is missing")
	}
	actual, err := corpusProjection(entry.Events)
	if err != nil {
		return err
	}
	if err := CompareCorpus(entry.Expected, actual, entry.Manifest.Tolerance); err != nil {
		return fmt.Errorf("corpus %s: %w", entry.Manifest.ID, err)
	}
	return nil
}
