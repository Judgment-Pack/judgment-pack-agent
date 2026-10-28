package runcontext

// ADR-0022: portable, redacted evaluation-corpus entries and their prose-free comparison.

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

var CorpusFiles = map[string]string{"manifest": "manifest.json", "requests": "requests.json", "toolResults": "tool-results.json", "events": "events.json", "checkpoint": "checkpoint.json", "expected": "expected.json"}

type CorpusManifest struct {
	ID             string         `json:"id"`
	PackageVersion string         `json:"packageVersion"`
	Policy         map[string]any `json:"policy"`
	Files          struct {
		Requests    string `json:"requests"`
		ToolResults string `json:"toolResults"`
		Events      string `json:"events"`
		Checkpoint  string `json:"checkpoint"`
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

type recordedCorpusToolResult struct {
	Name   string          `json:"name"`
	Args   json.RawMessage `json:"args"`
	Result json.RawMessage `json:"result"`
}

// ScriptedCorpusTools serves only the recording's ordered tool results. A missing or mismatched
// call is remembered as structured corpus drift while the adapter receives an error result and can
// finish its loop; no fallback transport exists.
type ScriptedCorpusTools struct {
	id         string
	rows       []recordedCorpusToolResult
	at         int
	difference *CorpusDifference
}

func NewScriptedCorpusTools(entry CorpusEntry) (*ScriptedCorpusTools, error) {
	var rows []recordedCorpusToolResult
	if err := json.Unmarshal(entry.ToolResults, &rows); err != nil {
		return nil, err
	}
	return &ScriptedCorpusTools{id: entry.Manifest.ID, rows: rows}, nil
}

func corpusCall(name string, args json.RawMessage) map[string]any {
	var value any
	if err := json.Unmarshal(args, &value); err != nil {
		value = nil
	}
	return map[string]any{"name": name, "args": value}
}

func sameCorpusCall(row recordedCorpusToolResult, name string, args json.RawMessage) bool {
	want, err := canonicalBytes(corpusCall(row.Name, row.Args))
	if err != nil {
		return false
	}
	got, err := canonicalBytes(corpusCall(name, args))
	return err == nil && string(want) == string(got)
}

// Call returns the recorded MCP result for this call and records drift rather than failing the
// adapter when the recording has no matching result.
func (s *ScriptedCorpusTools) Call(name string, args json.RawMessage) json.RawMessage {
	at := s.at
	s.at++
	actual := corpusCall(name, args)
	if at >= len(s.rows) || !sameCorpusCall(s.rows[at], name, args) {
		var expected any
		if at < len(s.rows) {
			expected = corpusCall(s.rows[at].Name, s.rows[at].Args)
		}
		if s.difference == nil {
			s.difference = &CorpusDifference{Member: fmt.Sprintf("/toolResults/%d", at), Expected: expected, Actual: actual}
		}
		return json.RawMessage(`{"content":[{"type":"text","text":"The recording has no result for this tool call."}],"isError":true}`)
	}
	return append(json.RawMessage(nil), s.rows[at].Result...)
}

func (s *ScriptedCorpusTools) Complete() error {
	if s.difference != nil {
		return &CorpusDrift{ID: s.id, Difference: *s.difference}
	}
	if s.at != len(s.rows) {
		row := s.rows[s.at]
		return &CorpusDrift{ID: s.id, Difference: CorpusDifference{Member: fmt.Sprintf("/toolResults/%d", s.at), Expected: corpusCall(row.Name, row.Args), Actual: nil}}
	}
	return nil
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
	var precedingCritique map[string]any
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
		case "critique":
			precedingCritique = row
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
			if precedingCritique != nil {
				critic = map[string]any{"verdict": precedingCritique["refuted"], "reasons": precedingCritique["checks"]}
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
	if err := validateCorpusManifest(entry.Manifest); err != nil {
		return corpusRefused(), nil
	}
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

var corpusID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

func validateCorpusManifest(manifest CorpusManifest) error {
	if !corpusID.MatchString(manifest.ID) || len(manifest.PackageVersion) < 1 || len(manifest.PackageVersion) > 64 || manifest.Policy == nil ||
		manifest.Files.Requests != "requests.json" || manifest.Files.ToolResults != "tool-results.json" || manifest.Files.Events != "events.json" || manifest.Files.Checkpoint != "checkpoint.json" || manifest.Expected.File != "expected.json" || len(manifest.Tolerance) > 64 {
		return errors.New("invalid corpus manifest")
	}
	seen := map[string]bool{}
	for _, pointer := range manifest.Tolerance {
		if seen[pointer] || len(pointer) > 256 || !(pointer == "/timings" || strings.HasPrefix(pointer, "/timings/") || pointer == "/drawnMarkers" || strings.HasPrefix(pointer, "/drawnMarkers/")) {
			return errors.New("invalid corpus tolerance")
		}
		seen[pointer] = true
	}
	return nil
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
	unescape := func(part string) string { return strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~") }
	for _, encoded := range parts[:len(parts)-1] {
		part := unescape(encoded)
		switch held := at.(type) {
		case map[string]any:
			at = held[part]
		case []any:
			index, err := strconv.Atoi(part)
			if err != nil || index < 0 || index >= len(held) {
				return
			}
			at = held[index]
		default:
			return
		}
	}
	last := unescape(parts[len(parts)-1])
	if object, ok := at.(map[string]any); ok {
		delete(object, last)
	} else if list, ok := at.([]any); ok {
		index, err := strconv.Atoi(last)
		if err == nil && index >= 0 && index < len(list) {
			list[index] = nil
		}
	}
}

type CorpusDifference struct {
	Member           string
	Expected, Actual any
}

func firstCorpusDifference(left, right any, path string) *CorpusDifference {
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
			if found := firstCorpusDifference(a[key], b[key], path+"/"+key); found != nil {
				return found
			}
		}
		return nil
	}
	aa, ok := left.([]any)
	bb, bok := right.([]any)
	if ok && bok {
		if len(aa) != len(bb) {
			return &CorpusDifference{Member: path + "/length", Expected: len(aa), Actual: len(bb)}
		}
		for index := range aa {
			if found := firstCorpusDifference(aa[index], bb[index], fmt.Sprintf("%s/%d", path, index)); found != nil {
				return found
			}
		}
		return nil
	}
	x, _ := json.Marshal(left)
	y, _ := json.Marshal(right)
	if string(x) != string(y) {
		if path == "" {
			path = "/"
		}
		return &CorpusDifference{Member: path, Expected: left, Actual: right}
	}
	return nil
}

type CorpusDrift struct {
	ID         string
	Difference CorpusDifference
}

func (e *CorpusDrift) Error() string {
	prefix := ""
	if e.ID != "" {
		prefix = "corpus " + e.ID + ": "
	}
	return prefix + e.Difference.Member + " differs"
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
	if difference := firstCorpusDifference(left, right, ""); difference != nil {
		return &CorpusDrift{Difference: *difference}
	}
	return nil
}
func ReplayCorpus(entry CorpusEntry) error {
	if err := validateCorpusManifest(entry.Manifest); err != nil {
		return err
	}
	if len(entry.Requests) == 0 || len(entry.ToolResults) == 0 {
		return errors.New("recorded request or tool-result file is missing")
	}
	actual, err := corpusProjection(entry.Events)
	if err != nil {
		return err
	}
	if err := CompareCorpus(entry.Expected, actual, entry.Manifest.Tolerance); err != nil {
		if drift, ok := err.(*CorpusDrift); ok {
			drift.ID = entry.Manifest.ID
			return drift
		}
		return err
	}
	return nil
}
