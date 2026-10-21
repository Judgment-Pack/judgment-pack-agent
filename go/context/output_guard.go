package runcontext

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"unicode/utf16"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

//go:embed output_patterns.json
var outputPatternsJSON []byte

type OutputPattern struct {
	Name    string `json:"name"`
	Pattern string `json:"pattern"`
}

var baseOutputPatterns = func() []OutputPattern {
	var v struct {
		Patterns []OutputPattern `json:"patterns"`
	}
	if err := json.Unmarshal(outputPatternsJSON, &v); err != nil {
		panic(err)
	}
	return v.Patterns
}()
var outputName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)

type OutputRefused struct{ Pattern string }

func (e *OutputRefused) Error() string { return fmt.Sprintf("Output refused: %s.", e.Pattern) }
func ReadOutputPatterns(value any) ([]OutputPattern, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var rows []map[string]any
	if err = json.Unmarshal(raw, &rows); err != nil || rows == nil || len(rows) > 32 {
		return nil, fmt.Errorf("invalid outputPatterns")
	}
	names := map[string]bool{"work-bound": true, "proposal-validation": true, "invalid-output": true}
	for _, p := range baseOutputPatterns {
		names[p.Name] = true
	}
	out := []OutputPattern{}
	for _, row := range rows {
		name, ok := row["name"].(string)
		pattern, good := row["pattern"].(string)
		if len(row) != 2 || !ok || !good || !outputName.MatchString(name) || names[name] {
			return nil, fmt.Errorf("invalid output pattern")
		}
		if _, err = CompilePattern(pattern, false); err != nil {
			return nil, err
		}
		names[name] = true
		out = append(out, OutputPattern{name, pattern})
	}
	return out, nil
}
func ReadOutputAllow(value any) ([]string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var names []string
	if err = json.Unmarshal(raw, &names); err != nil || names == nil || len(names) > 64 {
		return nil, fmt.Errorf("invalid outputAllow")
	}
	seen := map[string]bool{}
	for _, name := range names {
		if !outputName.MatchString(name) || seen[name] {
			return nil, fmt.Errorf("invalid outputAllow")
		}
		seen[name] = true
	}
	return names, nil
}

type outputCompiled struct {
	name    string
	pattern *CompiledPattern
}
type OutputGuard struct {
	mu       sync.Mutex
	Mode     string
	patterns []outputCompiled
	counts   func() map[string]int64
	notices  []contract.RunEvent
}

func NewOutputGuard(members PolicyMembers, counts func() map[string]int64) (*OutputGuard, error) {
	mode, _ := members["outputGuard"].Value.(string)
	if mode == "" {
		mode = "redact"
	}
	extras := members["outputPatterns"].Value
	if extras == nil {
		extras = []any{}
	}
	extra, err := ReadOutputPatterns(extras)
	if err != nil {
		return nil, err
	}
	allowed := members["outputAllow"].Value
	if allowed == nil {
		allowed = []any{}
	}
	allow, err := ReadOutputAllow(allowed)
	if err != nil {
		return nil, err
	}
	g := &OutputGuard{Mode: mode, counts: counts}
	for _, p := range append(append([]OutputPattern{}, baseOutputPatterns...), extra...) {
		skip := false
		for _, a := range allow {
			if a == p.Name {
				skip = true
			}
		}
		if skip {
			continue
		}
		compiled, err := CompilePattern(p.Pattern, false)
		if err != nil {
			return nil, err
		}
		g.patterns = append(g.patterns, outputCompiled{p.Name, compiled})
	}
	return g, nil
}
func (g *OutputGuard) TakeNotices() []contract.RunEvent {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := g.notices
	g.notices = nil
	return out
}
func (g *OutputGuard) record(where, name, action string, record bool) {
	if !record {
		return
	}
	g.counts()[name]++
	g.notices = append(g.notices, contract.RunEvent{OutputGuarded: &contract.RunEventOutputGuarded{Type: "output_guarded", Where: where, Pattern: name, Action: action}})
}
func (g *OutputGuard) refuse(where, name string, _ bool) error {
	record := true
	g.record(where, name, "refuse", record)
	if record {
		reason := "output refused"
		g.notices = append(g.notices, contract.RunEvent{Guardrail: &contract.RunEventGuardrail{Type: "guardrail", Tool: where, Action: "refused", Reason: &reason, Detail: (&OutputRefused{name}).Error()}})
	}
	return &OutputRefused{name}
}
func (g *OutputGuard) Refuse(where, name string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.refuse(where, name, true)
}

type outputMatch struct {
	start, end, order int
	name              string
}

func (g *OutputGuard) text(s, where string, record bool, budget *PatternBudget) (string, error) {
	buckets := [][]outputMatch{}
	for order, p := range g.patterns {
		ranges, ok := MatchRanges(p.pattern, s, budget)
		if !ok {
			return "", g.refuse(where, "work-bound", record)
		}
		bucket := []outputMatch{}
		for _, r := range ranges {
			bucket = append(bucket, outputMatch{r[0], r[1], order, p.name})
		}
		buckets = append(buckets, bucket)
	}
	matches := []outputMatch{}
	heads := make([]int, len(buckets))
	for {
		selected := -1
		for i, bucket := range buckets {
			if heads[i] >= len(bucket) {
				continue
			}
			candidate := bucket[heads[i]]
			if selected < 0 || candidate.start < buckets[selected][heads[selected]].start || candidate.start == buckets[selected][heads[selected]].start && candidate.order < buckets[selected][heads[selected]].order {
				selected = i
			}
		}
		if selected < 0 {
			break
		}
		matches = append(matches, buckets[selected][heads[selected]])
		heads[selected]++
	}
	units := u16(s)
	var out strings.Builder
	at := 0
	for i := 0; i < len(matches); i++ {
		m := matches[i]
		if m.start < at {
			continue
		}
		end := m.end
		for i+1 < len(matches) && matches[i+1].start < end {
			i++
			if matches[i].end > end {
				end = matches[i].end
			}
		}
		if g.Mode == "refuse" {
			return "", g.refuse(where, m.name, record)
		}
		g.record(where, m.name, "redact", record)
		out.WriteString(string(utf16.Decode(units[at:m.start])))
		out.WriteString("[redacted: " + m.name + "]")
		at = end
	}
	out.WriteString(string(utf16.Decode(units[at:])))
	return out.String(), nil
}
func (g *OutputGuard) Text(s, where string, record bool) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.Mode == "off" {
		return s, nil
	}
	return g.text(s, where, record, &PatternBudget{Steps: SearchSteps})
}
func (g *OutputGuard) JSON(raw json.RawMessage, where string, record bool) (json.RawMessage, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.Mode == "off" {
		return raw, nil
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil || !json.Valid(raw) {
		return nil, g.refuse(where, "invalid-output", record)
	}
	budget := &PatternBudget{Steps: SearchSteps}
	var walk func(any) (any, error)
	walk = func(v any) (any, error) {
		switch x := v.(type) {
		case string:
			return g.text(x, where, record, budget)
		case []any:
			for i, v := range x {
				clean, err := walk(v)
				if err != nil {
					return nil, err
				}
				x[i] = clean
			}
			return x, nil
		case map[string]any:
			out := map[string]any{}
			for _, k := range policyKeys(x) {
				key, err := g.text(k, where, record, budget)
				if err != nil {
					return nil, err
				}
				clean, err := walk(x[k])
				if err != nil {
					return nil, err
				}
				out[key] = clean
			}
			return out, nil
		default:
			return v, nil
		}
	}
	before, _ := json.Marshal(value)
	clean, err := walk(value)
	if err != nil {
		return nil, err
	}
	after, err := json.Marshal(clean)
	if bytes.Equal(before, after) {
		return raw, err
	}
	return after, err
}
func OutputWhere(name string) string {
	switch name {
	case "propose_pack":
		return "proposal"
	case "remember":
		return "memory proposal"
	case "propose_edit", "propose_insert", "propose_delete", "undo_edit":
		return "draft edit"
	}
	return "tool argument"
}

// Counts returns a detached snapshot; concurrent dispatch cannot race checkpoint encoding.
func (g *OutputGuard) Counts() map[string]int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := map[string]int64{}
	for name, n := range g.counts() {
		out[name] = n
	}
	return out
}

// Adopt records one already-guarded child event in the parent's report.
func (g *OutputGuard) Adopt(pattern string) { g.mu.Lock(); defer g.mu.Unlock(); g.counts()[pattern]++ }
