package runcontext

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
)

// Dialect authoring is a first-party host capability. Its only effect is retaining a review proposal.
const (
	DialectDocumentBytes    = 1048576
	DialectPackBytes        = 1048576
	DialectSnapshotBytes    = 4194304
	DialectResultBytes      = 2097152
	DialectRuntimeBytes     = 4194304
	DialectCandidateBytes   = 4194304
	DialectDraftBytes       = 8388608
	DialectRuntimeResults   = 2
	DialectColumns          = 1000
	DialectBindings         = 1000
	DialectSamples          = 3
	DialectSampleBytes      = 16384
	DialectShownSampleBytes = 160
	DialectRememberedReads  = 32
	DialectMilliseconds     = 10000
)

// Published Spec RC2 companion at draft maturity; this tuple does not select general Core support.
type DialectConversionSpec struct {
	Version      string `json:"version"`
	Revision     string `json:"revision"`
	SchemaSHA256 string `json:"schemaSha256"`
	CorpusSHA256 string `json:"corpusSha256"`
}
type DialectConversionContract struct {
	Contract string                `json:"contract"`
	Profile  string                `json:"profile"`
	Spec     DialectConversionSpec `json:"spec"`
}

// DialectRetainedColumnDraft retains its compatibility name for the accepted draft-maturity companion.
func DialectRetainedColumnDraft() DialectConversionContract {
	return DialectConversionContract{"retained-column/1", "base", DialectConversionSpec{"0.3.0-rc.2", "bcac15b7537ce21879201b5e5dfe795496225121", "sha256:0cb49f238c78391cb163a65474cc9af962df2bb1024a55c5310c2b784b2f0557", "sha256:17b16ff7a9b74fe4cc81eb6f6129a6aabdd9834621d9e756aa2d4c1a3f245863"}}
}

type DialectRetainedOccurrence struct {
	Kind string  `json:"kind"`
	Name *string `json:"name,omitempty"`
}
type DialectRetainedRead struct {
	DocumentJSON string                    `json:"documentJson"`
	Occurrence   DialectRetainedOccurrence `json:"occurrence"`
	FramePointer string                    `json:"framePointer"`
}
type DialectResearchField struct {
	Name      string `json:"name"`
	FieldJSON string `json:"fieldJson"`
}

// Fields nil explicitly represents an unavailable original declaration.
type DialectResearchRevision struct {
	Revision string                 `json:"revision"`
	Fields   []DialectResearchField `json:"fields"`
}
type DialectReadRequest struct {
	ID      string `json:"id"`
	PackKey string `json:"packKey"`
}
type DialectIdentity struct {
	ID            string  `json:"id"`
	Revision      string  `json:"revision"`
	PackKey       string  `json:"packKey"`
	PackDigest    string  `json:"packDigest"`
	DialectDigest *string `json:"dialectDigest"`
}
type DialectConversion struct {
	Kind       string  `json:"kind"`
	TrueValue  *string `json:"trueValue,omitempty"`
	FalseValue *string `json:"falseValue,omitempty"`
}
type DialectFactBinding struct {
	Target     string            `json:"target"`
	Column     string            `json:"column"`
	Conversion DialectConversion `json:"conversion"`
}
type DialectEvidenceBinding struct {
	Requirement string `json:"requirement"`
	Column      string `json:"column"`
}
type DialectSample struct {
	ValueJSON string `json:"valueJson"`
	Origin    string `json:"origin"`
}
type DialectResearchDeclaration struct {
	Revision  string `json:"revision"`
	FieldJSON string `json:"fieldJson"`
}
type DialectColumn struct {
	Name     string                      `json:"name"`
	Samples  []DialectSample             `json:"samples"`
	Research *DialectResearchDeclaration `json:"research,omitempty"`
}
type DialectSnapshot struct {
	RetainedRead *DialectRetainedRead `json:"retainedRead,omitempty"`
	// Complete original inventory; nil is unavailable, empty is a known empty inventory.
	ResearchDeclarations []DialectResearchRevision    `json:"researchDeclarations"`
	ID                   string                       `json:"id"`
	Revision             string                       `json:"revision"`
	PackKey              string                       `json:"packKey"`
	Name                 string                       `json:"name"`
	RecordCount          int64                        `json:"recordCount"`
	PackJSON             string                       `json:"packJson"`
	DialectJSON          *string                      `json:"dialectJson,omitempty"`
	Columns              []DialectColumn              `json:"columns"`
	FactTargets          []string                     `json:"factTargets"`
	EvidenceRequirements []string                     `json:"evidenceRequirements"`
	Conversions          map[string]DialectConversion `json:"conversions"`
}
type DialectPreviewIssue struct {
	RecordID string   `json:"recordId"`
	Issues   []string `json:"issues"`
}
type DialectPreview struct {
	Fingerprint       string                `json:"fingerprint"`
	RecordCount       int64                 `json:"recordCount"`
	RecordsWithIssues int64                 `json:"recordsWithIssues"`
	Issues            []DialectPreviewIssue `json:"issues"`
}
type DialectCandidate struct {
	RetainedRead         *DialectRetainedRead      `json:"retainedRead"`
	ResearchDeclarations []DialectResearchRevision `json:"researchDeclarations"`
	ConversionContract   DialectConversionContract `json:"conversionContract"`
	DialectIdentity
	DocumentJSON string                       `json:"documentJson"`
	PackJSON     string                       `json:"packJson"`
	Conversions  map[string]DialectConversion `json:"conversions"`
	Facts        []DialectFactBinding         `json:"facts"`
	Evidence     []DialectEvidenceBinding     `json:"evidence"`
	Summary      string                       `json:"summary"`
	Columns      []DialectColumn              `json:"columns"`
}

// Draft checkpoints preserve failed authoring work for inspection and editing, never Save authority.
type DialectDraftResult struct {
	Operation  string `json:"operation"`
	ResultJSON string `json:"resultJson"`
}
type DialectDraftFailure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
type DialectDraftCheck struct {
	Results []DialectDraftResult `json:"results"`
	Failure *DialectDraftFailure `json:"failure,omitempty"`
}
type DialectDraftCheckpoint struct {
	AttemptID       string             `json:"attemptId"`
	Version         int                `json:"version"`
	Fingerprint     string             `json:"fingerprint"`
	Stage           string             `json:"stage"`
	Digest          string             `json:"digest"`
	PreviousReceipt *string            `json:"previousReceipt"`
	Candidate       DialectCandidate   `json:"candidate"`
	Check           *DialectDraftCheck `json:"check,omitempty"`
}
type DialectDraftRetention struct {
	Fingerprint     string  `json:"fingerprint"`
	Stage           string  `json:"stage"`
	Digest          string  `json:"digest"`
	PreviousReceipt *string `json:"previousReceipt"`
	Receipt         string  `json:"receipt"`
	Status          string  `json:"status"`
}
type DialectDraftLineage struct {
	AttemptID        string `json:"attemptId"`
	Fingerprint      string `json:"fingerprint"`
	CandidateReceipt string `json:"candidateReceipt"`
	CheckedReceipt   string `json:"checkedReceipt"`
	CheckedDigest    string `json:"checkedDigest"`
}
type dialectDraftState struct {
	Busy      bool
	Unknown   bool
	Candidate *DialectDraftRetention
	Checked   *DialectDraftRetention
}
type DialectReviewProposal struct {
	Draft DialectDraftLineage `json:"draft"`
	DialectCandidate
	Version       int            `json:"version"`
	Fingerprint   string         `json:"fingerprint"`
	MappingDigest string         `json:"mappingDigest"`
	Status        string         `json:"status"`
	Validation    map[string]any `json:"validation"`
	Coverage      map[string]any `json:"coverage"`
	Preview       DialectPreview `json:"preview"`
}
type DialectRetention struct {
	Draft       DialectDraftLineage `json:"draft"`
	Fingerprint string              `json:"fingerprint"`
	Receipt     string              `json:"receipt"`
	Status      string              `json:"status"`
}

// DialectAuthoringPorts has no save, publish, acquire, approve or execute capability.
type DialectAuthoringPorts struct {
	// Explicit trusted opt-in to the exact package-supported companion, never a model argument.
	ConversionContract *DialectConversionContract
	Read               func(context.Context, DialectReadRequest) (DialectSnapshot, error)
	// Runtime is the actual installed Runtime transport; only discovery, validate and check are used.
	Runtime     func(context.Context, string, map[string]string) (ToolResult, error)
	Preview     func(context.Context, DialectCandidate, string) (DialectPreview, error)
	RetainDraft func(context.Context, DialectDraftCheckpoint) (DialectDraftRetention, error)
	Retain      func(context.Context, DialectReviewProposal) (DialectRetention, error)
}
type dialectProposalArgs struct {
	DialectIdentity
	Facts    []DialectFactBinding     `json:"facts"`
	Evidence []DialectEvidenceBinding `json:"evidence"`
	Summary  string                   `json:"summary"`
}
type dialectToolsState struct {
	mu            sync.Mutex
	ports         DialectAuthoringPorts
	reads         map[string]DialectIdentity
	order         []string
	attempts      map[string]*DialectRetention
	drafts        map[string]*dialectDraftState
	draftAttempts int
}

func dialectFail(message string) error      { return failure.ToolInput("Dialect authoring: " + message) }
func dialectStringPointer(v string) *string { return &v }
func dialectUnknown() error {
	return &failure.Error{Failure: contract.Failure{Version: 1, Code: "tool-failed", Source: "storage", Outcome: "unknown", Detail: dialectStringPointer("Dialect proposal retention was not acknowledged. Its outcome is unknown; do not retry automatically. Check the host retained result.")}}
}
func dialectHash(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return "sha256:" + hex.EncodeToString(sum[:])
}
func dialectText(v string, max int) bool { return strings.TrimSpace(v) != "" && len(v) <= max }
func dialectDigest(v string) bool {
	if len(v) != 71 || !strings.HasPrefix(v, "sha256:") {
		return false
	}
	_, err := hex.DecodeString(v[7:])
	return err == nil && strings.ToLower(v) == v
}
func dialectPointer(v string) bool {
	if !dialectText(v, 1024) || !strings.HasPrefix(v, "/") {
		return false
	}
	for _, part := range strings.Split(v, "/") {
		for i := 0; i < len(part); i++ {
			if part[i] == '~' {
				if i+1 >= len(part) || (part[i+1] != '0' && part[i+1] != '1') {
					return false
				}
				i++
			}
		}
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		if slices.Contains([]string{"__proto__", "prototype", "constructor"}, part) {
			return false
		}
	}
	return true
}
func dialectColumn(v string) bool {
	return v != "" && len(v) <= 1024 && utf8.ValidString(v) && !slices.Contains([]string{"__proto__", "prototype", "constructor"}, v)
}
func dialectConversion(v DialectConversion) bool {
	if v.Kind == "boolean" {
		return v.TrueValue != nil && v.FalseValue != nil && dialectText(*v.TrueValue, 160) && dialectText(*v.FalseValue, 160) && *v.TrueValue != *v.FalseValue
	}
	return slices.Contains([]string{"text", "decimal", "number", "research-value"}, v.Kind) && v.TrueValue == nil && v.FalseValue == nil
}
func dialectStrictJSON(raw []byte) bool { return dialectStrictJSONLimit(raw, 128) }
func dialectStrictJSONLimit(raw []byte, maxDepth int) bool {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	var walk func(int) bool
	walk = func(depth int) bool {
		if depth > maxDepth {
			return false
		}
		token, err := dec.Token()
		if err != nil {
			return false
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return true
		}
		switch delimiter {
		case '{':
			seen := map[string]bool{}
			for dec.More() {
				key, err := dec.Token()
				name, ok := key.(string)
				if err != nil || !ok || seen[name] {
					return false
				}
				seen[name] = true
				if !walk(depth + 1) {
					return false
				}
			}
			end, err := dec.Token()
			return err == nil && end == json.Delim('}')
		case '[':
			for dec.More() {
				if !walk(depth + 1) {
					return false
				}
			}
			end, err := dec.Token()
			return err == nil && end == json.Delim(']')
		}
		return false
	}
	if !walk(0) {
		return false
	}
	_, err := dec.Token()
	return err == io.EOF
}

// JSON's legacy decoder replaces lone surrogates; original declarations must reject them.
func dialectScalarJSON(raw string) bool {
	if !utf8.ValidString(raw) {
		return false
	}
	inString := false
	for i := 0; i < len(raw); i++ {
		c := raw[i]
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
		i++
		if i >= len(raw) {
			return false
		}
		if raw[i] != 'u' {
			continue
		}
		if i+4 >= len(raw) {
			return false
		}
		code, err := strconv.ParseUint(raw[i+1:i+5], 16, 16)
		if err != nil {
			return false
		}
		i += 4
		if code >= 0xdc00 && code <= 0xdfff {
			return false
		}
		if code >= 0xd800 && code <= 0xdbff {
			if i+6 >= len(raw) || raw[i+1:i+3] != "\\u" {
				return false
			}
			low, e := strconv.ParseUint(raw[i+3:i+7], 16, 16)
			if e != nil || low < 0xdc00 || low > 0xdfff {
				return false
			}
			i += 6
		}
	}
	return true
}
func dialectDeclaration(name, raw string) bool {
	if len(raw) > DialectSampleBytes || !dialectStrictJSONLimit([]byte(raw), 32) || !dialectScalarJSON(raw) {
		return false
	}
	var field map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &field) != nil {
		return false
	}
	var actual, kind string
	return json.Unmarshal(field["name"], &actual) == nil && actual == name && json.Unmarshal(field["type"], &kind) == nil && string(field["type"]) != "null"
}
func dialectResearchInventory(revisions []DialectResearchRevision) bool {
	if len(revisions) > DialectColumns {
		return false
	}
	seen := map[string]bool{}
	for _, r := range revisions {
		if !dialectText(r.Revision, 160) || !utf8.ValidString(r.Revision) || seen[r.Revision] || len(r.Fields) > DialectColumns {
			return false
		}
		seen[r.Revision] = true
		names := map[string]bool{}
		for _, f := range r.Fields {
			if !dialectColumn(f.Name) || len(f.Name) > 160 || names[f.Name] || !dialectDeclaration(f.Name, f.FieldJSON) {
				return false
			}
			names[f.Name] = true
		}
	}
	return true
}
func dialectSafeTemplateJSON(raw string) bool {
	if !dialectStrictJSON([]byte(raw)) || !dialectScalarJSON(raw) {
		return false
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	for {
		token, e := dec.Token()
		if e == io.EOF {
			return true
		}
		if e != nil {
			return false
		}
		if number, ok := token.(json.Number); ok {
			n, e := number.Int64()
			if e != nil || n < -9007199254740991 || n > 9007199254740991 {
				return false
			}
		}
	}
}
func dialectRetainedRead(value *DialectRetainedRead) bool {
	if value == nil {
		return true
	}
	o := value.Occurrence
	return dialectRaw(value.DocumentJSON, DialectDocumentBytes) && (value.FramePointer == "" || dialectPointer(value.FramePointer)) && ((o.Kind == "case" && o.Name == nil) || (o.Kind == "source" && o.Name != nil && dialectText(*o.Name, 1024)))
}
func dialectJSONEqual(a, b any) bool {
	left, e := dialectCanonical(a)
	if e != nil {
		return false
	}
	right, e := dialectCanonical(b)
	return e == nil && left == right
}
func dialectContractIdentity(r map[string]any, selection DialectConversionContract) bool {
	return r["contract"] == selection.Contract && r["profile"] == selection.Profile && r["role"] == "document-validation" && r["maturity"] == "draft" && r["specRevision"] == selection.Spec.Revision && r["specification"] == "https://github.com/Judgment-Pack/judgment-pack-spec/blob/"+selection.Spec.Revision+"/spec/retained-column.md" && dialectJSONEqual(r["spec"], selection.Spec)
}
func dialectDecode(raw []byte, out any) error {
	if !dialectStrictJSON(raw) {
		return dialectFail("malformed or duplicate JSON members.")
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return dialectFail("unexpected or malformed input members.")
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return dialectFail("trailing input.")
	}
	return nil
}
func dialectRaw(raw string, max int) bool { return len(raw) <= max && dialectStrictJSON([]byte(raw)) }
func dialectCanonical(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	v, err := ParseJSON(raw)
	if err != nil {
		return "", err
	}
	return CanonicalJSON(v), nil
}
func dialectClone[T any](value T) (T, error) {
	var out T
	raw, err := json.Marshal(value)
	if err == nil {
		err = json.Unmarshal(raw, &out)
	}
	return out, err
}
func dialectBounded(ctx context.Context, run func(context.Context) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	bounded, cancel := context.WithTimeout(ctx, time.Duration(DialectMilliseconds)*time.Millisecond)
	defer cancel()
	if err := run(bounded); err != nil {
		return err
	}
	return bounded.Err()
}
func dialectIdentity(s DialectSnapshot) DialectIdentity {
	var current *string
	if s.DialectJSON != nil {
		x := dialectHash(*s.DialectJSON)
		current = &x
	}
	return DialectIdentity{ID: s.ID, Revision: s.Revision, PackKey: s.PackKey, PackDigest: dialectHash(s.PackJSON), DialectDigest: current}
}
func dialectSame(a, b DialectIdentity) bool {
	return a.ID == b.ID && a.Revision == b.Revision && a.PackKey == b.PackKey && a.PackDigest == b.PackDigest && ((a.DialectDigest == nil && b.DialectDigest == nil) || (a.DialectDigest != nil && b.DialectDigest != nil && *a.DialectDigest == *b.DialectDigest))
}
func dialectKey(r DialectReadRequest) string {
	raw, _ := json.Marshal([]string{r.ID, r.PackKey})
	return string(raw)
}
func dialectResult(value any) (ToolResult, error) {
	raw, err := json.Marshal(value)
	if err != nil || len(raw) > DialectResultBytes {
		return ToolResult{}, dialectFail("the result exceeds its byte limit.")
	}
	return ResultOf([]Content{TextContent(string(raw))}, nil), nil
}
func (g *dialectToolsState) read(ctx context.Context, r DialectReadRequest) (DialectSnapshot, error) {
	var s DialectSnapshot
	err := dialectBounded(ctx, func(b context.Context) error { var e error; s, e = g.ports.Read(b, r); return e })
	if err != nil {
		return s, err
	}
	raw, err := json.Marshal(s)
	if err != nil || len(raw) > DialectSnapshotBytes {
		return s, dialectFail("the read snapshot exceeds its byte limit.")
	}
	if s.ID != r.ID || s.PackKey != r.PackKey || !dialectText(s.Revision, 128) || !dialectText(s.Name, 1024) || s.RecordCount < 0 || s.RecordCount > 9007199254740991 || !dialectRaw(s.PackJSON, DialectPackBytes) || (s.DialectJSON != nil && !dialectRaw(*s.DialectJSON, DialectDocumentBytes)) || !dialectResearchInventory(s.ResearchDeclarations) || !dialectRetainedRead(s.RetainedRead) || s.Columns == nil || s.FactTargets == nil || s.EvidenceRequirements == nil || s.Conversions == nil || len(s.Columns) > DialectColumns || len(s.FactTargets) > DialectBindings || len(s.EvidenceRequirements) > DialectBindings || len(s.Conversions) > DialectBindings {
		return s, dialectFail("the read snapshot is malformed or has a different identity.")
	}
	targets := map[string]bool{}
	for _, v := range s.FactTargets {
		if !dialectPointer(v) || targets[v] {
			return s, dialectFail("the fact inventory is malformed.")
		}
		targets[v] = true
	}
	requirements := map[string]bool{}
	for _, v := range s.EvidenceRequirements {
		if !dialectText(v, 1024) || requirements[v] {
			return s, dialectFail("the evidence inventory is malformed.")
		}
		requirements[v] = true
	}
	for target, c := range s.Conversions {
		if !dialectPointer(target) || !dialectConversion(c) {
			return s, dialectFail("current conversions are malformed.")
		}
	}
	columns := map[string]bool{}
	for _, c := range s.Columns {
		if !dialectColumn(c.Name) || columns[c.Name] || c.Samples == nil || len(c.Samples) > DialectSamples {
			return s, dialectFail("the columns are malformed or duplicated.")
		}
		columns[c.Name] = true
		for _, v := range c.Samples {
			if !dialectRaw(v.ValueJSON, DialectSampleBytes) || !dialectText(v.Origin, 128) {
				return s, dialectFail("the samples are malformed or exceed their bound.")
			}
		}
		if c.Research != nil && (!dialectText(c.Research.Revision, 128) || !dialectRaw(c.Research.FieldJSON, DialectSampleBytes)) {
			return s, dialectFail("research-value requires the original declaration and revision.")
		}
		if c.Research != nil && !dialectDeclaration(c.Name, c.Research.FieldJSON) {
			return s, dialectFail("research-value requires the actual original field declaration.")
		}
	}
	return dialectClone(s)
}
func dialectReport(result ToolResult) (map[string]any, error) {
	if len(result.JSON()) > DialectRuntimeBytes {
		return nil, dialectFail("Runtime report exceeds its bound.")
	}
	if result.IsError != nil && *result.IsError {
		return nil, dialectFail("Runtime refused the check. Diagnostics: " + Prefix(string(result.JSON()), 700))
	}
	if len(result.Content) != 1 || dialectValueString(result.Content[0].Value().Get("type")) != "text" {
		return nil, dialectFail("Runtime returned an unsupported report transport.")
	}
	raw := dialectValueString(result.Content[0].Value().Get("text"))
	if len(raw) > DialectRuntimeBytes {
		return nil, dialectFail("Runtime report exceeds its bound.")
	}
	if !dialectStrictJSON([]byte(raw)) {
		return nil, dialectFail("Runtime returned malformed report JSON.")
	}
	var r map[string]any
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	if dec.Decode(&r) != nil || r == nil {
		return nil, dialectFail("Runtime returned malformed report JSON.")
	}
	if r["contractVersion"] != "1" || !dialectText(dialectString(r["specification"]), 1024) || !dialectText(dialectString(r["specRevision"]), 128) {
		return nil, dialectFail("Runtime returned an unsupported report contract.")
	}
	if _, ok := r["diagnostics"].([]any); !ok {
		return nil, dialectFail("Runtime returned malformed diagnostics.")
	}
	return r, nil
}
func (g *dialectToolsState) checkpoint(ctx context.Context, state *dialectDraftState, attemptID, fingerprint, stage string, candidate DialectCandidate, check *DialectDraftCheck) (DialectDraftRetention, error) {
	if err := ctx.Err(); err != nil {
		return DialectDraftRetention{}, err
	}
	var previous *string
	if stage == "checked" {
		previous = dialectStringPointer(state.Candidate.Receipt)
	}
	content := map[string]any{"version": 1, "attemptId": attemptID, "fingerprint": fingerprint, "stage": stage, "previousReceipt": previous, "candidate": candidate}
	if check != nil {
		content["check"] = check
	}
	raw, err := json.Marshal(content)
	if err != nil {
		return DialectDraftRetention{}, err
	}
	if len(raw)+100 > DialectDraftBytes {
		*check = DialectDraftCheck{Results: []DialectDraftResult{}, Failure: &DialectDraftFailure{Code: "report-unavailable", Message: "Full Runtime evidence exceeded the draft byte bound; no complete report was retained."}}
	}
	canonical, err := dialectCanonical(content)
	if err != nil {
		return DialectDraftRetention{}, err
	}
	digest := dialectHash(canonical)
	value := DialectDraftCheckpoint{Version: 1, AttemptID: attemptID, Fingerprint: fingerprint, Stage: stage, Digest: digest, PreviousReceipt: previous, Candidate: candidate, Check: check}
	raw, err = json.Marshal(value)
	if err != nil || len(raw) > DialectDraftBytes {
		return DialectDraftRetention{}, dialectFail("draft checkpoint exceeds its byte limit.")
	}
	g.mu.Lock()
	prior := state.Candidate
	if stage == "checked" {
		prior = state.Checked
	}
	if prior != nil {
		g.mu.Unlock()
		samePrevious := (prior.PreviousReceipt == nil && previous == nil) || (prior.PreviousReceipt != nil && previous != nil && *prior.PreviousReceipt == *previous)
		if prior.Digest != digest || !samePrevious {
			return DialectDraftRetention{}, dialectFail("the retained draft stage changed; its receipt cannot be rebound.")
		}
		return *prior, nil
	}
	state.Unknown = true
	g.mu.Unlock()
	var ack DialectDraftRetention
	err = dialectBounded(ctx, func(b context.Context) error {
		detached, e := dialectClone(value)
		if e != nil {
			return e
		}
		if g.ports.RetainDraft == nil {
			return fmt.Errorf("draft retention unavailable")
		}
		ack, e = g.ports.RetainDraft(b, detached)
		return e
	})
	samePrevious := (ack.PreviousReceipt == nil && previous == nil) || (ack.PreviousReceipt != nil && previous != nil && *ack.PreviousReceipt == *previous)
	if err != nil || ack.Fingerprint != fingerprint || ack.Stage != stage || ack.Digest != digest || !samePrevious || ack.Status != "draft" || !dialectText(ack.Receipt, 128) {
		return DialectDraftRetention{}, dialectUnknown()
	}
	g.mu.Lock()
	if stage == "candidate" {
		state.Candidate = &ack
	} else {
		state.Checked = &ack
	}
	state.Unknown = false
	g.mu.Unlock()
	return ack, nil
}
func dialectValueString(v *Value) string {
	if !v.IsString() {
		return ""
	}
	return v.Str()
}
func dialectString(v any) string      { s, _ := v.(string); return s }
func dialectMap(v any) map[string]any { m, _ := v.(map[string]any); return m }
func dialectIncludes(v any, wanted string) bool {
	a, _ := v.([]any)
	for _, row := range a {
		if fmt.Sprint(row) == wanted {
			return true
		}
	}
	return false
}
func dialectMapping(a dialectProposalArgs, s DialectSnapshot) (string, error) {
	facts := []map[string]any{}
	evidence := []map[string]any{}
	framePointer := "/retained"
	if s.RetainedRead != nil {
		framePointer = s.RetainedRead.FramePointer
	}
	unmapped := []string{}
	unmappedEvidence := []string{}
	targets := map[string]bool{}
	requirements := map[string]bool{}

	for _, f := range a.Facts {
		targets[f.Target] = true
		conversion := map[string]any{"contractVersion": "1", "column": f.Column, "kind": f.Conversion.Kind}
		if f.Conversion.Kind == "boolean" {
			conversion["trueValue"] = *f.Conversion.TrueValue
			conversion["falseValue"] = *f.Conversion.FalseValue
		}
		facts = append(facts, map[string]any{"target": f.Target, "source": framePointer, "conversion": conversion})
	}
	for _, e := range a.Evidence {
		requirements[e.Requirement] = true
		evidence = append(evidence, map[string]any{"requirement": e.Requirement, "source": framePointer, "availability": map[string]any{"contractVersion": "1", "column": e.Column}})
	}
	for _, t := range s.FactTargets {
		if !targets[t] {
			unmapped = append(unmapped, t)
		}
	}
	for _, r := range s.EvidenceRequirements {
		if !requirements[r] {
			unmappedEvidence = append(unmappedEvidence, r)
		}
	}
	bound := map[string]any{"facts": facts, "evidence": evidence}
	mapping := map[string]any{"version": 2, "provider": "", "facts": nil, "evidence": nil, "case": bound}
	if s.RetainedRead != nil {
		parsed, err := ParseJSON([]byte(s.RetainedRead.DocumentJSON))
		if err != nil {
			return "", dialectFail("the retained source template is outside the canonical document domain.")
		}
		canonical := CanonicalJSON(parsed)
		dec := json.NewDecoder(strings.NewReader(canonical))
		dec.UseNumber()
		mapping = nil
		if dec.Decode(&mapping) != nil || fmt.Sprint(mapping["version"]) != "2" || dialectMap(mapping["case"]) == nil {
			return "", dialectFail("the retained source template must declare a version-2 case.")
		}
		// Canonical parsing must not normalize unsupported numbers before Runtime checks.
		if !dialectSafeTemplateJSON(s.RetainedRead.DocumentJSON) {
			return "", dialectFail("the retained source template is outside the canonical document domain.")
		}
		occurrence := s.RetainedRead.Occurrence
		if occurrence.Kind == "source" {
			sources, ok := mapping["sources"].([]any)
			if !ok {
				return "", dialectFail("the selected retained source occurrence is missing.")
			}
			var selected map[string]any
			matches := 0
			for _, raw := range sources {
				row := dialectMap(raw)
				if row["name"] == *occurrence.Name {
					matches++
					selected = row
				}
			}
			read := dialectMap(selected["read"])
			if matches != 1 || dialectMap(read["copy"]) == nil || read["rule"] != nil {
				return "", dialectFail("the selected retained source must have one existing copy read.")
			}
			read["copy"] = bound
		} else {
			selected := dialectMap(mapping["case"])
			selected["facts"] = facts
			selected["evidence"] = evidence
		}
	}
	mapping["unmapped"], mapping["unmappedEvidence"] = unmapped, unmappedEvidence
	return dialectCanonical(mapping)
}
func dialectBindings(a dialectProposalArgs, s DialectSnapshot) error {
	if a.Facts == nil || a.Evidence == nil || len(a.Facts) > DialectBindings || len(a.Evidence) > DialectBindings || !dialectText(a.Summary, 4000) {
		return dialectFail("provide bounded complete bindings and a summary.")
	}
	columns := map[string]DialectColumn{}
	for _, c := range s.Columns {
		columns[c.Name] = c
	}
	seen := map[string]bool{}
	requirements := map[string]bool{}
	for _, f := range a.Facts {
		_, exists := columns[f.Column]
		if !dialectPointer(f.Target) || !dialectColumn(f.Column) || !dialectConversion(f.Conversion) || !slices.Contains(s.FactTargets, f.Target) || !exists || seen[f.Target] {
			return dialectFail("facts must bind unique current targets to existing columns with explicit conversions.")
		}
		for previous := range seen {
			if strings.HasPrefix(previous, f.Target+"/") || strings.HasPrefix(f.Target, previous+"/") {
				return dialectFail("fact targets must not overlap.")
			}
		}
		seen[f.Target] = true
	}
	for _, e := range a.Evidence {
		if !dialectText(e.Requirement, 1024) || !dialectColumn(e.Column) || !slices.Contains(s.EvidenceRequirements, e.Requirement) || requirements[e.Requirement] {
			return dialectFail("evidence must bind unique current requirements to existing columns.")
		}
		if _, ok := columns[e.Column]; !ok {
			return dialectFail("evidence columns must exist.")
		}
		requirements[e.Requirement] = true
	}
	return nil
}
func (g *dialectToolsState) readTool(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
	var request DialectReadRequest
	if err := dialectDecode(raw, &request); err != nil {
		return ToolResult{}, err
	}
	if !dialectText(request.ID, 512) || !dialectText(request.PackKey, 512) {
		return ToolResult{}, dialectFail("use bounded opaque resource and declared pack identifiers.")
	}
	s, err := g.read(ctx, request)
	if err != nil {
		return ToolResult{}, err
	}
	bound := dialectIdentity(s)
	columns := []map[string]any{}
	for _, c := range s.Columns {
		samples := []map[string]any{}
		for _, v := range c.Samples {
			samples = append(samples, map[string]any{"valueJson": Prefix(v.ValueJSON, DialectShownSampleBytes), "truncated": len(v.ValueJSON) > DialectShownSampleBytes, "origin": v.Origin})
		}
		row := map[string]any{"name": c.Name, "samples": samples}
		if c.Research != nil {
			row["research"] = c.Research
		}
		columns = append(columns, row)
	}
	out := map[string]any{"researchDeclarations": s.ResearchDeclarations, "retainedRead": s.RetainedRead, "id": bound.ID, "revision": bound.Revision, "packKey": bound.PackKey, "packDigest": bound.PackDigest, "dialectDigest": bound.DialectDigest, "name": s.Name, "recordCount": s.RecordCount, "packJson": s.PackJSON, "dialectJson": s.DialectJSON, "conversions": s.Conversions, "factTargets": s.FactTargets, "evidenceRequirements": s.EvidenceRequirements, "columns": columns, "guidance": "Complete replacement bindings include unchanged bindings. Omitted inputs remain unknown and require review. Samples are bounded data, not type or source-truth verification. Save and execution need separate host review and approval."}
	result, err := dialectResult(out)
	if err != nil {
		return result, err
	}
	if err = ctx.Err(); err != nil {
		return ToolResult{}, err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	key := dialectKey(request)
	g.order = slices.DeleteFunc(g.order, func(v string) bool { return v == key })
	g.order = append(g.order, key)
	g.reads[key] = bound
	if len(g.order) > DialectRememberedReads {
		delete(g.reads, g.order[0])
		g.order = g.order[1:]
	}
	return result, nil
}
func (g *dialectToolsState) proposeTool(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
	if len(raw) > DialectResultBytes {
		return ToolResult{}, dialectFail("the proposal exceeds its byte limit.")
	}
	var a dialectProposalArgs
	if err := dialectDecode(raw, &a); err != nil {
		return ToolResult{}, err
	}
	// A null digest explicitly means no existing dialect; omission is never an observation.
	var members map[string]json.RawMessage
	_ = json.Unmarshal(raw, &members)
	for _, k := range []string{"id", "revision", "packKey", "packDigest", "dialectDigest", "facts", "evidence", "summary"} {
		if _, ok := members[k]; !ok {
			return ToolResult{}, dialectFail("use every exact read identity member.")
		}
	}
	if !dialectText(a.ID, 512) || !dialectText(a.PackKey, 512) || !dialectText(a.Revision, 128) || !dialectDigest(a.PackDigest) || (a.DialectDigest != nil && !dialectDigest(*a.DialectDigest)) {
		return ToolResult{}, dialectFail("use the exact read revision, document digest and pack digest.")
	}
	request := DialectReadRequest{ID: a.ID, PackKey: a.PackKey}
	key := dialectKey(request)
	g.mu.Lock()
	seen, ok := g.reads[key]
	g.mu.Unlock()
	if !ok || !dialectSame(seen, a.DialectIdentity) {
		return ToolResult{}, dialectFail("read the exact current resource and pack before proposing.")
	}
	s, err := g.read(ctx, request)
	if err != nil {
		return ToolResult{}, err
	}
	if !dialectSame(dialectIdentity(s), a.DialectIdentity) {
		g.mu.Lock()
		delete(g.reads, key)
		g.mu.Unlock()
		return ToolResult{}, dialectFail("the dialect or pack changed; read it again before proposing.")
	}
	var rawFacts []struct {
		Conversion map[string]json.RawMessage `json:"conversion"`
	}
	_ = json.Unmarshal(members["facts"], &rawFacts)
	for i, f := range a.Facts {
		if f.Conversion.Kind != "boolean" {
			for _, member := range []string{"trueValue", "falseValue"} {
				if _, present := rawFacts[i].Conversion[member]; present {
					return ToolResult{}, dialectFail("only boolean conversions accept exact true/false spellings.")
				}
			}
		}
	}
	if err = dialectBindings(a, s); err != nil {
		return ToolResult{}, err
	}
	selection := DialectRetainedColumnDraft()
	runtimeArgs := func(args map[string]string) map[string]string {
		args["draftContract"] = selection.Contract + "@" + selection.Spec.Revision
		args["draftProfile"] = selection.Profile
		return args
	}
	input, err := dialectMapping(a, s)
	if err != nil {
		return ToolResult{}, err
	}
	if len(input) > DialectDocumentBytes {
		return ToolResult{}, dialectFail("the replacement document exceeds its byte limit.")
	}
	conversions := map[string]DialectConversion{}
	for _, f := range a.Facts {
		conversions[f.Target] = f.Conversion
	}
	initial := DialectCandidate{DialectIdentity: a.DialectIdentity, DocumentJSON: input, PackJSON: s.PackJSON, Conversions: conversions, Facts: a.Facts, Evidence: a.Evidence, Summary: a.Summary, Columns: s.Columns, ResearchDeclarations: s.ResearchDeclarations, RetainedRead: s.RetainedRead, ConversionContract: selection}
	encodedInitial, err := json.Marshal(initial)
	if err != nil || len(encodedInitial) > DialectCandidateBytes {
		return ToolResult{}, dialectFail("draft candidate exceeds its byte limit.")
	}
	initialCanonical, err := dialectCanonical(initial)
	if err != nil {
		return ToolResult{}, err
	}
	candidateFingerprint := dialectHash(initialCanonical)
	g.mu.Lock()
	state := g.drafts[candidateFingerprint]
	if state != nil && (state.Unknown || state.Busy) {
		g.mu.Unlock()
		return ToolResult{}, dialectUnknown()
	}
	if g.draftAttempts >= DialectRememberedReads {
		g.mu.Unlock()
		return ToolResult{}, dialectFail("the retained draft attempt bound was reached.")
	}
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		g.mu.Unlock()
		return ToolResult{}, err
	}
	attemptID := hex.EncodeToString(nonce[:])
	attemptCanonical, err := dialectCanonical(map[string]any{"attemptId": attemptID, "candidate": initial})
	if err != nil {
		g.mu.Unlock()
		return ToolResult{}, err
	}
	draftFingerprint := dialectHash(attemptCanonical)
	g.draftAttempts++
	state = &dialectDraftState{}
	g.drafts[candidateFingerprint] = state
	state.Busy = true
	g.mu.Unlock()
	defer func() { g.mu.Lock(); state.Busy = false; g.mu.Unlock() }()
	if _, err = g.checkpoint(ctx, state, attemptID, draftFingerprint, "candidate", initial, nil); err != nil {
		return ToolResult{}, err
	}
	observations := DialectDraftCheck{Results: []DialectDraftResult{}}
	returned, unavailable := false, false
	observedRuntime := func(name string, args map[string]string) (map[string]any, error) {
		returned = false
		var result ToolResult
		e := dialectBounded(ctx, func(b context.Context) error { var e error; result, e = g.ports.Runtime(b, name, args); return e })
		if e != nil {
			return nil, e
		}
		returned = true
		encoded := result.JSON()
		if len(encoded) > DialectRuntimeBytes {
			unavailable = true
			observations.Results = []DialectDraftResult{}
			return nil, dialectFail("Runtime returned oversized check evidence.")
		}
		observations.Results = append(observations.Results, DialectDraftResult{Operation: name, ResultJSON: string(encoded)})
		r, e := dialectReport(result)
		if e != nil && (result.IsError == nil || !*result.IsError) {
			unavailable = true
			observations.Results = []DialectDraftResult{}
		}
		return r, e
	}
	validation, coverage, err := func() (map[string]any, map[string]any, error) {
		if g.ports.ConversionContract == nil || *g.ports.ConversionContract != selection {
			return nil, nil, dialectFail("the exact package-supported retained-column opt-in is required.")
		}
		var discovery ToolResult
		err := dialectBounded(ctx, func(b context.Context) error {
			var e error
			discovery, e = g.ports.Runtime(b, "get_dialect_contract", runtimeArgs(map[string]string{}))
			return e
		})
		if err != nil {
			return nil, nil, err
		}
		contractReport, err := dialectReport(discovery)
		if err != nil {
			returned = true
			unavailable = true
			return nil, nil, err
		}
		returned = true
		capabilities := dialectMap(contractReport["capabilities"])
		limits := dialectMap(contractReport["limits"])
		contracts, contractsOK := capabilities["conversionContracts"].([]any)
		expectedCapability := map[string]any{"contract": selection.Contract, "spec": selection.Spec, "profile": selection.Profile, "role": "document-validation", "limits": limits}
		if contractReport["status"] != "available" || !dialectContractIdentity(contractReport, selection) || !contractsOK || len(contracts) != 1 || !dialectJSONEqual(contracts[0], expectedCapability) || capabilities["packCheckScope"] != "declared-inventory-coverage" {
			return nil, nil, dialectFail("Runtime does not support the exact selected conversion document contract.")
		}
		documentLimit, documentOK := limits["documentBytes"].(json.Number)
		frameLimit, frameOK := limits["frameBytes"].(json.Number)
		packLimit, packOK := capabilities["maxPackBytes"].(json.Number)
		documentMax, documentErr := documentLimit.Int64()
		frameMax, frameErr := frameLimit.Int64()
		packMax, packErr := packLimit.Int64()
		if len(limits) != 2 || !documentOK || !frameOK || !packOK || documentErr != nil || frameErr != nil || packErr != nil || documentMax < 1 || frameMax < 1 || packMax < 1 || documentMax > 9007199254740991 || frameMax > 9007199254740991 || packMax > 9007199254740991 || !dialectIncludes(capabilities["operations"], "validate") || !dialectIncludes(capabilities["operations"], "check") {
			return nil, nil, dialectFail("Runtime omitted its supported operations or byte ceilings.")
		}
		if int64(len(input)) > min(int64(DialectDocumentBytes), documentMax) || int64(len(s.PackJSON)) > min(int64(DialectPackBytes), packMax) {
			return nil, nil, dialectFail("the supplied document or pack exceeds the discovered Runtime byte ceiling.")
		}
		checked := func(name string, args map[string]string) (map[string]any, error) {
			r, e := observedRuntime(name, runtimeArgs(args))
			if e != nil {
				return nil, e
			}
			document := dialectString(r["document"])
			if !dialectContractIdentity(r, selection) || !dialectJSONEqual(r["limits"], limits) || fmt.Sprint(r["dialectVersion"]) != "2" || !dialectDigest(dialectString(r["digest"])) || dialectHash(document) != r["digest"] {
				return nil, dialectFail("Runtime changed its contract or returned a mismatched document identity.")
			}
			left, e := ParseJSON([]byte(input))
			if e != nil {
				return nil, e
			}
			right, e := ParseJSON([]byte(document))
			if e != nil || CanonicalJSON(left) != CanonicalJSON(right) {
				return nil, dialectFail("Runtime checked a different replacement mapping.")
			}
			return r, nil
		}
		validation, err := checked("dialect_validate", map[string]string{"document": input})
		if err != nil {
			return nil, nil, err
		}
		if validation["status"] != "document-valid" || validation["validationScope"] != "experimental-retained-column-"+selection.Profile+"-document" {
			return nil, nil, dialectFail("Runtime did not validate the replacement document.")
		}
		coverage, err := checked("dialect_check", map[string]string{"document": input, "pack": s.PackJSON})
		if err != nil {
			return nil, nil, err
		}
		compat := dialectMap(coverage["compatibility"])
		pack := dialectMap(compat["pack"])
		if compat == nil || !slices.Contains([]string{"complete", "review-required"}, dialectString(coverage["status"])) || compat["status"] != coverage["status"] || compat["scope"] != "declared-inventory-coverage" || pack["sha256"] != a.PackDigest[7:] || compat["valueTypesChecked"] != false || compat["executionChecked"] != false || coverage["validationScope"] != "experimental-retained-column-"+selection.Profile+"-document-and-declared-pack-inventory" || coverage["document"] != validation["document"] || coverage["digest"] != validation["digest"] {
			return nil, nil, dialectFail("Runtime did not establish accepted coverage of this exact document and pack.")
		}
		for _, inventory := range []struct {
			name    string
			targets []string
		}{{"facts", s.FactTargets}, {"evidence", s.EvidenceRequirements}} {
			entries, ok := compat[inventory.name].([]any)
			unique := map[string]bool{}
			if !ok || len(entries) != len(inventory.targets) {
				return nil, nil, dialectFail("host inventory metadata differs from Runtime coverage.")
			}
			for _, e := range entries {
				target := dialectString(dialectMap(e)["target"])
				if !slices.Contains(inventory.targets, target) || unique[target] {
					return nil, nil, dialectFail("host inventory metadata differs from Runtime coverage.")
				}
				unique[target] = true
			}
		}
		return validation, coverage, nil
	}()
	if err != nil {
		if returned && ctx.Err() == nil {
			code, message := "check-refused", "Returned Runtime evidence did not establish accepted validation and coverage."
			if unavailable {
				code, message = "report-unavailable", "Full Runtime evidence was unavailable; no complete report was retained."
			}
			observations.Failure = &DialectDraftFailure{Code: code, Message: message}
			if _, e := g.checkpoint(ctx, state, attemptID, draftFingerprint, "checked", initial, &observations); e != nil {
				return ToolResult{}, e
			}
		}
		return ToolResult{}, err
	}
	if _, err = g.checkpoint(ctx, state, attemptID, draftFingerprint, "checked", initial, &observations); err != nil {
		return ToolResult{}, err
	}
	if observations.Failure != nil {
		return ToolResult{}, dialectFail("full Runtime evidence could not be retained within the draft byte bound.")
	}
	draft := DialectDraftLineage{AttemptID: attemptID, Fingerprint: draftFingerprint, CandidateReceipt: state.Candidate.Receipt, CheckedReceipt: state.Checked.Receipt, CheckedDigest: state.Checked.Digest}
	candidate := initial
	candidate.DocumentJSON = dialectString(validation["document"])
	canonical, err := dialectCanonical(candidate)
	if err != nil {
		return ToolResult{}, err
	}
	fingerprint := dialectHash(canonical)
	var preview DialectPreview
	err = dialectBounded(ctx, func(b context.Context) error {
		detached, e := dialectClone(candidate)
		if e != nil {
			return e
		}
		preview, e = g.ports.Preview(b, detached, fingerprint)
		return e
	})
	if err != nil {
		return ToolResult{}, err
	}
	if preview.Fingerprint != fingerprint || preview.RecordCount != s.RecordCount || preview.RecordsWithIssues < 0 || preview.RecordsWithIssues > preview.RecordCount || preview.Issues == nil || len(preview.Issues) > 5 {
		return ToolResult{}, dialectFail("conversion preview is malformed, oversized or bound to a different candidate.")
	}
	for _, row := range preview.Issues {
		if !dialectText(row.RecordID, 512) || row.Issues == nil || len(row.Issues) > 10 {
			return ToolResult{}, dialectFail("conversion preview issues exceed their bound.")
		}
		for _, v := range row.Issues {
			if !dialectText(v, 1024) {
				return ToolResult{}, dialectFail("conversion preview issues exceed their bound.")
			}
		}
	}
	current, err := g.read(ctx, request)
	if err != nil {
		return ToolResult{}, err
	}
	if !dialectSame(dialectIdentity(current), a.DialectIdentity) {
		g.mu.Lock()
		delete(g.reads, key)
		g.mu.Unlock()
		return ToolResult{}, dialectFail("the dialect or pack changed during preview; read it again before proposing.")
	}
	proposal := DialectReviewProposal{DialectCandidate: candidate, Draft: draft, Version: 1, Fingerprint: fingerprint, MappingDigest: dialectString(validation["digest"]), Status: "review-required", Validation: validation, Coverage: coverage, Preview: preview}
	encoded, _ := json.Marshal(proposal)
	if len(encoded) > DialectRuntimeBytes {
		return ToolResult{}, dialectFail("the review proposal exceeds its byte limit.")
	}
	if err = ctx.Err(); err != nil {
		return ToolResult{}, err
	}
	g.mu.Lock()
	previous, tried := g.attempts[draftFingerprint]
	if !tried {
		if len(g.attempts) >= DialectRememberedReads {
			g.mu.Unlock()
			return ToolResult{}, dialectFail("the retained proposal attempt bound was reached.")
		}
		g.attempts[draftFingerprint] = nil
		state.Unknown = true
	}
	g.mu.Unlock()
	var ack DialectRetention
	if tried {
		if previous == nil {
			return ToolResult{}, dialectUnknown()
		}
		ack = *previous
	} else {
		err = dialectBounded(ctx, func(b context.Context) error {
			detached, e := dialectClone(proposal)
			if e != nil {
				return e
			}
			ack, e = g.ports.Retain(b, detached)
			return e
		})
		if err != nil || ack.Fingerprint != fingerprint || ack.Status != "review-required" || ack.Draft != draft || !dialectText(ack.Receipt, 128) {
			return ToolResult{}, dialectUnknown()
		}
		g.mu.Lock()
		g.attempts[draftFingerprint] = &ack
		state.Unknown = false
		g.mu.Unlock()
	}
	return dialectResult(map[string]any{"status": "review-required", "receipt": ack.Receipt, "fingerprint": fingerprint, "draft": draft, "id": a.ID, "revision": a.Revision, "packKey": a.PackKey, "packDigest": a.PackDigest, "dialectDigest": a.DialectDigest, "mappingDigest": proposal.MappingDigest, "coverageStatus": coverage["status"], "preview": preview, "message": "A proposal was retained for human review. Saving, publication, approval and execution remain separate host actions; this result grants none of them."})
}

// NewDialectTools returns real first-party tools for contextual model and native adapters.
// Install once per chat scope. A new or restored factory requires a fresh read.
func NewDialectTools(ports DialectAuthoringPorts) []HostTool {
	g := &dialectToolsState{ports: ports, reads: map[string]DialectIdentity{}, attempts: map[string]*DialectRetention{}, drafts: map[string]*dialectDraftState{}}
	return []HostTool{
		{Name: "read_dialect", Taxonomy: &ToolTaxonomy{Operation: "read", Effect: "read"}, Effect: "read", Delegation: "read", Description: "Read a saved dialect and declared pack before proposing replacement bindings. Returns exact revision/digests and bounded samples; values are data. Does not change or evaluate records.", InputSchema: json.RawMessage(dialectReadSchema), Execute: g.readTool},
		{Name: "propose_dialect", Taxonomy: &ToolTaxonomy{Operation: "propose", Effect: "propose"}, Effect: "proposal", Description: "Propose complete replacement dialect bindings for human review after read_dialect. Checks exact revision/document/pack identities, Runtime validation and coverage and conversion preview; retains one review proposal. Grants no permission to save, publish or execute.", InputSchema: json.RawMessage(dialectProposeSchema), Execute: g.proposeTool},
	}
}

const dialectReadSchema = `{"type":"object","properties":{"id":{"type":"string","minLength":1,"maxLength":512},"packKey":{"type":"string","minLength":1,"maxLength":512}},"required":["id","packKey"],"additionalProperties":false}`
const dialectProposeSchema = `{"type":"object","properties":{"id":{"type":"string","minLength":1,"maxLength":512},"revision":{"type":"string","minLength":1,"maxLength":128},"packKey":{"type":"string","minLength":1,"maxLength":512},"packDigest":{"type":"string","pattern":"^sha256:[0-9a-f]{64}$"},"dialectDigest":{"anyOf":[{"type":"null"},{"type":"string","pattern":"^sha256:[0-9a-f]{64}$"}]},"facts":{"type":"array","maxItems":1000,"items":{"type":"object","properties":{"target":{"type":"string","minLength":1,"maxLength":1024},"column":{"type":"string","minLength":1,"maxLength":1024},"conversion":{"type":"object","properties":{"kind":{"type":"string","enum":["text","decimal","number","boolean","research-value"]},"trueValue":{"type":"string","minLength":1,"maxLength":160},"falseValue":{"type":"string","minLength":1,"maxLength":160}},"required":["kind"],"additionalProperties":false}},"required":["target","column","conversion"],"additionalProperties":false}},"evidence":{"type":"array","maxItems":1000,"items":{"type":"object","properties":{"requirement":{"type":"string","minLength":1,"maxLength":1024},"column":{"type":"string","minLength":1,"maxLength":1024}},"required":["requirement","column"],"additionalProperties":false}},"summary":{"type":"string","minLength":1,"maxLength":4000}},"required":["id","revision","packKey","packDigest","dialectDigest","facts","evidence","summary"],"additionalProperties":false}`
