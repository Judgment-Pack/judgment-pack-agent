package runcontext

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

// MaxRunReportDepth bounds parsing before recursive semantic validation. Supported report v1
// reaches depth 13: a pending Plan candidate cannot itself have a pending candidate.
const MaxRunReportDepth = 64

// Each JSON value consumes at least one encoded byte; this does not narrow the byte contract.
const MaxRunReportNodes = MaxRunReportBytes

type RunReportInvalid struct{}

func (*RunReportInvalid) Error() string {
	return "RunReportInvalid: the retained run report is invalid or unsupported."
}

//go:embed run_report_v1.json
var runReportV1JSON []byte
var runReportV1 = MustParseJSON(string(runReportV1JSON))

//go:embed run_report_v2.json
var runReportV2JSON []byte
var runReportV2 = MustParseJSON(string(runReportV2JSON))

// exactReportNumber checks decimal value before float64 can round an invalid spelling.
// Every numeric domain in report v1 is an integer.
func exactReportNumber(token string) (float64, bool) {
	negative := strings.HasPrefix(token, "-")
	token = strings.TrimPrefix(token, "-")
	parts := strings.FieldsFunc(token, func(r rune) bool { return r == 'e' || r == 'E' })
	mantissa := parts[0]
	integer, fraction, _ := strings.Cut(mantissa, ".")
	digits := strings.TrimLeft(integer+fraction, "0")
	if digits == "" {
		return 0, true
	}
	exponent := 0
	if len(parts) > 1 {
		var err error
		exponent, err = strconv.Atoi(parts[1])
		if err != nil {
			return 0, false
		}
	}
	// Keep arithmetic and exponentiation bounded even for an enormous exponent spelling.
	if exponent < -MaxRunReportBytes || exponent > MaxRunReportBytes {
		return 0, false
	}
	scale := exponent - len(fraction)
	for strings.HasSuffix(digits, "0") {
		digits = digits[:len(digits)-1]
		scale++
	}
	if scale < 0 || len(digits)+scale > 16 {
		return 0, false
	}
	n, err := strconv.ParseUint(digits+strings.Repeat("0", scale), 10, 64)
	if err != nil || n > 9007199254740991 {
		return 0, false
	}
	out := float64(n)
	if negative {
		out = -out
	}
	return out, true
}

// Go's generic JSON parser replaces lone surrogate escapes. Check them in source first.
func validReportUnicode(data []byte) bool {
	if !utf8.Valid(data) {
		return false
	}
	inString := false
	hex := func(at int) (uint64, bool) {
		if at+4 > len(data) {
			return 0, false
		}
		n, e := strconv.ParseUint(string(data[at:at+4]), 16, 16)
		return n, e == nil
	}
	for at := 0; at < len(data); at++ {
		c := data[at]
		if !inString {
			if c == '"' {
				inString = true
			}
			continue
		}
		if c == '"' {
			inString = false
			continue
		}
		if c != '\\' {
			continue
		}
		at++
		if at >= len(data) {
			return false
		}
		if data[at] != 'u' {
			continue
		}
		n, ok := hex(at + 1)
		if !ok {
			return false
		}
		at += 4
		if n >= 0xdc00 && n <= 0xdfff {
			return false
		}
		if n >= 0xd800 && n <= 0xdbff {
			if at+6 >= len(data) || data[at+1] != '\\' || data[at+2] != 'u' {
				return false
			}
			low, ok := hex(at + 3)
			if !ok || low < 0xdc00 || low > 0xdfff {
				return false
			}
			at += 6
		}
	}
	return true
}

// ParseRunReport reads bounded UTF-8 JSON directly, rejecting duplicate names and lossy numbers.
// It never constructs an execution checkpoint or grants authority to resume.
func ParseRunReport(data []byte) (*Value, error) {
	if len(data) > MaxRunReportBytes {
		return nil, &RunReportOverflow{}
	}
	if !validReportUnicode(data) {
		return nil, &RunReportInvalid{}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	nodes := 0
	var read func(int) (*Value, error)
	read = func(depth int) (*Value, error) {
		nodes++
		if nodes > MaxRunReportNodes || depth > MaxRunReportDepth {
			return nil, &RunReportOverflow{}
		}
		token, err := decoder.Token()
		if err != nil {
			return nil, &RunReportInvalid{}
		}
		switch t := token.(type) {
		case nil:
			return Null(), nil
		case bool:
			return Bool(t), nil
		case string:
			return String(t), nil
		case json.Number:
			n, ok := exactReportNumber(string(t))
			if !ok {
				return nil, &RunReportInvalid{}
			}
			return Number(n), nil
		case json.Delim:
			if t != '{' && t != '[' {
				return nil, &RunReportInvalid{}
			}
			out := Object()
			end := json.Delim('}')
			if t == '[' {
				out = Array()
				end = ']'
			}
			for decoder.More() {
				key := ""
				if t == '{' {
					token, err := decoder.Token()
					if err != nil {
						return nil, &RunReportInvalid{}
					}
					var ok bool
					key, ok = token.(string)
					if !ok || out.Has(key) {
						return nil, &RunReportInvalid{}
					}
				}
				held, err := read(depth + 1)
				if err != nil {
					return nil, err
				}
				if t == '[' {
					out.Append(held)
				} else {
					out.Set(key, held)
				}
			}
			token, err = decoder.Token()
			if err != nil || token != end {
				return nil, &RunReportInvalid{}
			}
			return out, nil
		}
		return nil, &RunReportInvalid{}
	}
	held, err := read(0)
	if err != nil {
		return nil, err
	}
	if _, err = decoder.Token(); err != io.EOF {
		return nil, &RunReportInvalid{}
	}
	return validateRunReport(held)
}

// ReadRunReport accepts a Value and returns a detached report. Precision lost before constructing
// that Value cannot be detected; use ParseRunReport at an untrusted byte boundary.
func ReadRunReport(value *Value) (*Value, error) {
	nodes, total := 0, 0
	seen := map[*Value]bool{}
	var copyValue func(*Value, int) (*Value, error)
	copyValue = func(v *Value, depth int) (*Value, error) {
		nodes++
		if nodes > MaxRunReportNodes || depth > MaxRunReportDepth {
			return nil, &RunReportOverflow{}
		}
		if v == nil || seen[v] {
			return nil, &RunReportInvalid{}
		}
		seen[v] = true
		defer delete(seen, v)
		var out *Value
		switch v.kind {
		case KindNull:
			out = Null()
			total += 4
		case KindBool:
			out = Bool(v.b)
			total += 5
			if v.b {
				total--
			}
		case KindNumber:
			if math.IsNaN(v.n) || math.IsInf(v.n, 0) || math.Trunc(v.n) != v.n || math.Abs(v.n) > 9007199254740991 {
				return nil, &RunReportInvalid{}
			}
			out = Number(v.n)
			total += len(jsNumber(v.n))
		case KindString:
			if !utf8.ValidString(v.s) {
				return nil, &RunReportInvalid{}
			}
			out = String(v.s)
			total += len(QuoteJSON(v.s))
		case KindArray:
			out = Array()
			total += 2
			for i, item := range v.a {
				if i > 0 {
					total++
				}
				held, err := copyValue(item, depth+1)
				if err != nil {
					return nil, err
				}
				out.Append(held)
			}
		case KindObject:
			out = Object()
			total += 2
			for i, m := range v.o {
				if !utf8.ValidString(m.key) || out.Has(m.key) {
					return nil, &RunReportInvalid{}
				}
				if i > 0 {
					total++
				}
				total += len(QuoteJSON(m.key)) + 1
				held, err := copyValue(m.val, depth+1)
				if err != nil {
					return nil, err
				}
				out.Set(m.key, held)
			}
		default:
			return nil, &RunReportInvalid{}
		}
		if total > MaxRunReportBytes {
			return nil, &RunReportOverflow{}
		}
		return out, nil
	}
	held, err := copyValue(value, 0)
	if err != nil {
		return nil, err
	}
	return validateRunReport(held)
}

var reportPatterns = map[string]*regexp.Regexp{}

func reportPattern(pattern, text string) bool {
	// Patterns are trusted frozen schema constants; compile once at initialization below.
	return reportPatterns[pattern].MatchString(text)
}
func init() {
	var visit func(*Value)
	visit = func(v *Value) {
		if p := v.Get("pattern"); p != nil {
			pattern := p.Str()
			// JavaScript's explicit absolute-end lookahead has the exact RE2 equivalent \z.
			// Translate only this trusted suffix; no arbitrary lookahead or pattern is dropped.
			if strings.HasSuffix(pattern, `$(?![\s\S])`) {
				pattern = strings.TrimSuffix(pattern, `$(?![\s\S])`) + `\z`
			}
			// Exact trusted ECMA control ranges have identical RE2 byte escapes.
			switch pattern {
			case `^[^\u0000-\u001f]+$`:
				pattern = `^[^\x00-\x1f]+$`
			case `^[^\u0000-\u001f\u007f]+$`:
				pattern = `^[^\x00-\x1f\x7f]+$`
			}
			reportPatterns[p.Str()] = regexp.MustCompile(pattern)
		}
		if v.IsObject() {
			for _, key := range v.Keys() {
				visit(v.Get(key))
			}
		} else if v.IsArray() {
			for _, item := range v.Items() {
				visit(item)
			}
		}
	}
	visit(runReportV1)
	visit(runReportV2)
}
func reportSchemaMatches(s, v *Value, graphs ...*Value) bool {
	graph := runReportV1
	if len(graphs) > 0 {
		graph = graphs[0]
	}
	if neg := s.Get("not"); neg != nil && reportSchemaMatches(neg, v, graph) {
		return false
	}
	if ref := s.Get("$ref"); ref != nil {
		return reportSchemaMatches(graph.Get("definitions").Get(ref.Str()), v, graph)
	}
	if constant := s.Get("const"); constant != nil && CanonicalJSON(constant) != CanonicalJSON(v) {
		return false
	}
	if enum := s.Get("enum"); enum != nil {
		found := false
		for _, item := range enum.Items() {
			if CanonicalJSON(item) == CanonicalJSON(v) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if typ := s.Get("type"); typ != nil {
		kind := map[string]Kind{"null": KindNull, "boolean": KindBool, "number": KindNumber, "integer": KindNumber, "string": KindString, "array": KindArray, "object": KindObject}[typ.Str()]
		if v == nil || v.kind != kind || typ.Str() == "integer" && !isInteger(v) {
			return false
		}
	}
	if v.kind == KindNumber {
		if bound := s.Get("minimum"); bound != nil && v.n < bound.n {
			return false
		}
		if bound := s.Get("maximum"); bound != nil && v.n > bound.n {
			return false
		}
	}
	if v.IsString() {
		length := float64(U16Length(v.s))
		if n := s.Get("minLength"); n != nil && length < n.n {
			return false
		}
		if n := s.Get("maxLength"); n != nil && length > n.n {
			return false
		}
		if p := s.Get("pattern"); p != nil && !reportPattern(p.Str(), v.s) {
			return false
		}
	}
	if v.IsArray() {
		if n := s.Get("minItems"); n != nil && float64(v.Len()) < n.n {
			return false
		}
		if n := s.Get("maxItems"); n != nil && float64(v.Len()) > n.n {
			return false
		}
		seen := map[string]bool{}
		for _, item := range v.a {
			if child := s.Get("items"); child != nil && !reportSchemaMatches(child, item, graph) {
				return false
			}
			if s.Get("uniqueItems").Truth() {
				key := CanonicalJSON(item)
				if seen[key] {
					return false
				}
				seen[key] = true
			}
		}
	}
	if v.IsObject() {
		for _, key := range s.Get("required").Items() {
			if !v.Has(key.Str()) {
				return false
			}
		}
		if n := s.Get("minProperties"); n != nil && float64(v.Len()) < n.n {
			return false
		}
		for _, m := range v.o {
			if names := s.Get("propertyNames"); names != nil && !reportSchemaMatches(names, String(m.key), graph) {
				return false
			}
			if child := s.Get("properties").Get(m.key); child != nil {
				if !reportSchemaMatches(child, m.val, graph) {
					return false
				}
			} else if additional := s.Get("additionalProperties"); additional != nil {
				if additional.kind == KindBool && !additional.b {
					return false
				}
				if additional.IsObject() && !reportSchemaMatches(additional, m.val, graph) {
					return false
				}
			}
		}
		if deps := s.Get("dependentRequired"); deps != nil {
			for _, key := range deps.Keys() {
				if v.Has(key) {
					for _, required := range deps.Get(key).Items() {
						if !v.Has(required.Str()) {
							return false
						}
					}
				}
			}
		}
	}
	if branches := s.Get("anyOf"); branches != nil {
		ok := false
		for _, branch := range branches.Items() {
			if reportSchemaMatches(branch, v, graph) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	if branches := s.Get("oneOf"); branches != nil {
		count := 0
		for _, branch := range branches.Items() {
			if reportSchemaMatches(branch, v, graph) {
				count++
			}
		}
		if count != 1 {
			return false
		}
	}
	if branches := s.Get("allOf"); branches != nil {
		for _, branch := range branches.Items() {
			if !reportSchemaMatches(branch, v, graph) {
				return false
			}
		}
	}
	if condition := s.Get("if"); condition != nil && reportSchemaMatches(condition, v, graph) {
		if then := s.Get("then"); then != nil && !reportSchemaMatches(then, v, graph) {
			return false
		}
	}
	return true
}

// oldSourceTaskFacts follows only typed Plan paths; legacy Plan drift remains permitted.
func oldSourceTaskFacts(v *Value) bool {
	if v.Get("version").Num() != 2 || v.Get("checkpointVersion").Num() == 3 {
		return true
	}
	step := func(s *Value) bool {
		return s == nil || (!s.Has("dependsOn") &&
			!s.Has("budget") &&
			!s.Has("delegation") &&
			!s.Get("evidence").Has("child"))
	}
	steps := func(rows *Value) bool {
		for _, s := range rows.Items() {
			if !step(s) {
				return false
			}
		}
		return true
	}
	var facts func(*Value) bool
	facts = func(v *Value) bool {
		if !steps(v.Get("plan")) {
			return false
		}
		state := v.Get("planState")
		if state.Has("taskSchedule") {
			return false
		}
		if state.Has("taskEnvelope") {
			return false
		}
		for _, row := range state.Get("history").Items() {
			for _, change := range row.Get("diff").Items() {
				if !step(change.Get("before")) || !step(change.Get("after")) {
					return false
				}
			}
		}
		candidate := state.Get("pending").Get("candidate")
		return candidate == nil || facts(candidate)
	}
	return facts(v)
}

func validRepeatReport(v *Value) bool {
	r := v.Get("repeatTools")
	if r == nil {
		return true
	}
	if v.Get("version").Num() != 2 || v.Get("checkpointVersion").Num() != 3 {
		return false
	}
	p := r.Get("policy")
	remind := p.Get("repeatTools.remindAt").Get("value")
	stop := p.Get("repeatTools.stopAt").Get("value")
	if p.Get("repeatTools.mode").Get("value").Str() != "on" || !snapshotCount(remind, 1) || !snapshotCount(stop, 1) || stop.Num() < remind.Num() {
		return false
	}
	seen := map[string]bool{}
	for _, row := range r.Get("executions").Items() {
		id := row.Get("execution").Str()
		count := row.Get("count")
		if !historyID(row.Get("execution")) || seen[id] || row.Get("remindAt").Num() != remind.Num() || row.Get("stopAt").Num() != stop.Num() || !snapshotCount(count, 0) || row.Get("reminded").Truth() != (count.Num() >= remind.Num()) || row.Get("stopped").Truth() != (count.Num() >= stop.Num()) || row.Get("reminderDelivered").Truth() && !row.Get("reminded").Truth() {
			return false
		}
		seen[id] = true
	}
	return r.Get("executions").Len() > 0
}

func validReportSemantics(v *Value) bool {
	if !oldSourceTaskFacts(v) || !validRepeatReport(v) {
		return false
	}
	if v.Get("version").Num() == 2 {
		if v.Get("checkpointVersion").Num() != 3 && (v.Has("usageTotals") || v.Has("route") || !v.Get("feedback").IsArray() || v.Get("feedback").Len() == 0) {
			return false
		}
		if rows := v.Get("feedback"); rows != nil {
			if !rows.IsArray() || rows.Len() < 1 || rows.Len() > HistoryMaxRows {
				return false
			}
			prior := map[string]*Value{}
			for _, row := range rows.Items() {
				if !snapshotObject(row, []string{"sourceDigest", "record"}) || !historySHA256.MatchString(row.Get("sourceDigest").Str()) || !validFeedbackValue(row.Get("record")) || row.Get("record").Get("runId").Str() != v.Get("id").Str() {
					return false
				}
				record := row.Get("record")
				old := prior[record.Get("id").Str()]
				sequence := 1
				previous := Null()
				if old != nil {
					sequence = int(old.Get("record").Get("sequence").Num()) + 1
					previous = old.Get("sourceDigest")
					if CanonicalJSON(old.Get("record").Get("target")) != CanonicalJSON(record.Get("target")) {
						return false
					}
				}
				if record.Get("sequence").Num() != float64(sequence) || CanonicalJSON(record.Get("previous")) != CanonicalJSON(previous) {
					return false
				}
				prior[record.Get("id").Str()] = row
			}
		}
	}

	if route := v.Get("route"); route != nil {
		var held TurnRoute
		if json.Unmarshal(route.JSON(), &held) != nil || !ValidTurnRoute(held) {
			return false
		}
	}
	used, limit := v.Get("used").n, v.Get("limit").n
	if used > limit {
		return false
	}
	capped := v.Has("requestCap")
	if capped && (v.Get("unit").s != "model-requests" || limit != v.Get("requestCap").n || v.Get("grants").Len() != 0) {
		return false
	}
	chained := float64(MaxTurns)
	if v.Get("unit").s == "provider-managed" {
		chained = 1
	}
	if capped {
		chained = v.Get("requestCap").n
	}
	for _, grant := range v.Get("grants").Items() {
		if grant.Get("from").n != chained || grant.Get("to").n <= chained || !parsableDate(grant.Get("at").s) {
			return false
		}
		chained = grant.Get("to").n
	}
	plan := v.Get("plan")
	if limit != chained || plan.Len() > 0 && !ValidTaskSteps(plan) || !validPlanState(plan, v.Get("planState")) {
		return false
	}
	if x := v.Get("delegation"); x != nil && !ValidDelegationFigures(x, v.Get("unit").s) {
		return false
	}
	if x := v.Get("sources"); x != nil && !ValidSourceFigures(x) {
		return false
	}
	if x := v.Get("sourceIdentities"); x != nil && !ValidSourceIdentities(x) {
		return false
	}
	if x := v.Get("skills"); x != nil && !ValidSkillLoads(x) {
		return false
	}
	if x := v.Get("usage"); x != nil && !validUsage(x) {
		return false
	}
	if x := v.Get("critic"); x != nil {
		var held contract.CheckpointCritic
		if json.Unmarshal(x.JSON(), &held) != nil || !ValidCriticRecord(&held) {
			return false
		}
	}
	if rows := v.Get("childUsage"); rows != nil {
		if !uniqueIDs(rows, "id") {
			return false
		}
		for _, row := range rows.Items() {
			if !validUsage(row.Get("usage")) {
				return false
			}
		}
	}
	if rows := v.Get("failover"); rows != nil {
		if !uniqueIDs(rows, "role") {
			return false
		}
		for _, row := range rows.Items() {
			if row.Get("consumed").n != float64(row.Get("history").Len()) {
				return false
			}
		}
	}
	last := 0.0
	for _, row := range v.Get("prompts").Items() {
		if row.Get("request").n <= last {
			return false
		}
		last = row.Get("request").n
	}
	for _, row := range v.Get("completions").Items() {
		if row.Get("request").n > used {
			return false
		}
		if held := row.Get("failure"); held != nil {
			if _, ok := readFailureValue(held); !ok {
				return false
			}
		}
		if (row.Has("schema") || row.Has("digest") || row.Has("retainUntil")) && !ValidRecordHeader(row, ProvenanceSchema) {
			return false
		}
	}
	return true
}
func validateRunReport(v *Value) (*Value, error) {
	graph := runReportV1
	if v.Get("version").Num() == 2 {
		graph = runReportV2
	}
	if !reportSchemaMatches(graph.Get("root"), v, graph) || !validReportSemantics(v) {
		return nil, &RunReportInvalid{}
	}
	if UTF8Bytes(CanonicalJSON(v)) > MaxRunReportBytes {
		return nil, &RunReportOverflow{}
	}
	return v, nil
}

// readRunReportDisplay validates the closed presentation graph for an explicitly derived view.
// It supplies neither native record integrity nor resume authority.
func readRunReportDisplay(value *Value) (*Value, error) {
	if value == nil || value.Get("version") == nil || len(CanonicalJSON(value)) > MaxRunReportBytes {
		return nil, &RunReportInvalid{}
	}
	graph := runReportV1
	if value.Get("version").Num() == 2 {
		graph = runReportV2
	}
	if !reportSchemaMatches(graph.Get("root"), value, graph) || !oldSourceTaskFacts(value) || !validRepeatReport(value) {
		return nil, &RunReportInvalid{}
	}
	return value.Clone(), nil
}
