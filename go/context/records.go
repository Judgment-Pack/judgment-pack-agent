package runcontext

// ADR records (#101): self-describing records, host-supplied exports, and expiry refusal. The
// package deliberately owns no record store; callers hand ExportRecords every record to export.

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

const (
	CheckpointSchema = "https://github.com/Judgment-Pack/judgment-pack-agent/blob/main/contract/schemas/checkpoint.json"
	ProvenanceSchema = "https://github.com/Judgment-Pack/judgment-pack-agent/blob/main/contract/schemas/provenance.json"
	CritiqueSchema   = "https://github.com/Judgment-Pack/judgment-pack-agent/blob/main/contract/schemas/run-event.json#/$defs/critique"
	ExportSchema     = "https://github.com/Judgment-Pack/judgment-pack-agent/blob/main/contract/schemas/export.json"
)

type RecordsPolicy struct {
	RetainDays    int
	RedactOnStore bool
	ExportFormat  string
}

var RecordDefaults = RecordsPolicy{RetainDays: 90, RedactOnStore: true, ExportFormat: "json"}
var recordDigestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

type RecordMarker struct {
	Redactions map[string]int64 `json:"redactions"`
}

type RecordGuard interface {
	JSON(raw json.RawMessage, where string, record bool, markers ...*RecordMarker) (json.RawMessage, error)
}

func ReadRecordsPolicy(members PolicyMembers) (RecordsPolicy, error) {
	out := RecordDefaults
	if held, ok := members["records.retainDays"].Value.(float64); ok {
		out.RetainDays = int(held)
	} else if held, ok := members["records.retainDays"].Value.(int); ok {
		out.RetainDays = held
	}
	if held, ok := members["records.redactOnStore"].Value.(bool); ok {
		out.RedactOnStore = held
	}
	if held, ok := members["records.exportFormat"].Value.(string); ok {
		out.ExportFormat = held
	}
	if out.RetainDays < 1 || out.RetainDays > 36500 || out.ExportFormat != "json" {
		return RecordsPolicy{}, errors.New("invalid records policy")
	}
	return out, nil
}

func recordWithoutDigest(value *Value) *Value {
	out := Object()
	for _, key := range value.Keys() {
		if key != "digest" {
			out.Set(key, value.Get(key).Clone())
		}
	}
	return out
}

func RecordDigest(value *Value) string {
	return "sha256:" + ContextDigest(CanonicalJSON(recordWithoutDigest(value)))
}

func RecordRetainUntil(policy RecordsPolicy, now time.Time) string {
	return now.UTC().Add(time.Duration(policy.RetainDays) * 24 * time.Hour).Format("2006-01-02T15:04:05.000Z")
}

// recordTextPaths names only model-derived members. Each member gets its own work budget.
var recordTextPaths = []string{
	"durable.dispatches.*.repeatResultJson", "durable.dispatches.*.request", "durable.dispatches.*.result", "durable.children.*.checkpoint", "handoff.continuation",
	"text", "explanation", "reason", "plan.*.step", "plan.*.detail",
	"operations.*.name", "operations.*.args", "approval.name", "approval.tool", "approval.args",
	"memories.proposed.*.text", "compaction.summaries.*.summary",
	"context.entries.*.value.@assistant.content.@text.text",
	"context.entries.*.value.@assistant.content.@call.toolName", "context.entries.*.value.@assistant.content.@call.input",
	"context.entries.*.value.@assistant.@answer", "context.entries.@draft.value.text", "context.entries.@draft.value.unknowns",
}

func guardRecord(out *Value, guard RecordGuard, figures *RecordMarker) error {
	var scan func(*Value, []string) error
	scan = func(node *Value, path []string) error {
		if node == nil || len(path) == 0 {
			return nil
		}
		key := path[0]
		if key == "*" {
			for _, item := range node.Items() {
				if err := scan(item, path[1:]); err != nil {
					return err
				}
			}
			return nil
		}
		if strings.HasPrefix(key, "@") && key != "@answer" {
			for _, item := range node.Items() {
				matches := key == "@draft" && strings.HasPrefix(editString(item.Get("label")), "draft:") || key == "@assistant" && editString(item.Get("role")) == "assistant" || key == "@text" && editString(item.Get("type")) == "text" || key == "@call" && editString(item.Get("type")) == "tool-call"
				if matches {
					if err := scan(item, path[1:]); err != nil {
						return err
					}
				}
			}
			return nil
		}
		if key == "@answer" {
			key = "content"
			if !node.Get(key).IsString() {
				return nil
			}
		}
		value := node.Get(key)
		if value == nil {
			return nil
		}
		if len(path) > 1 {
			return scan(value, path[1:])
		}
		if names, ok := guard.(interface{ DeclaredName(string) bool }); ok && (key == "name" || key == "tool" || key == "toolName") && names.DeclaredName(value.Str()) {
			return nil
		}
		clean, err := guard.JSON(value.JSON(), "message", true, figures)
		if err != nil {
			return err
		}
		parsed, err := ParseJSON(clean)
		if err != nil {
			return err
		}
		node.Set(key, parsed)
		return nil
	}
	for _, path := range recordTextPaths {
		if err := scan(out, strings.Split(path, ".")); err != nil {
			return err
		}
	}
	return nil
}

func WriteRecord(schema string, value *Value, policy RecordsPolicy, guard RecordGuard, now time.Time, keep string) (*Value, error) {
	out := value.Clone()
	version := 1
	if schema == CheckpointSchema {
		version = contract.CheckpointVersion
		if out.Get("version") != nil && out.Get("version").Num() == 3 || out.Has("repeatTools") || out.Has("turnRoute") || out.Has("usageBudget") || out.Get("approval").Has("runtime") || taskRecordFields(out) || nativePlanRecordFields(out) {
			version = 3
		}
	}
	out.Set("schema", String(schema)).Set("version", Number(float64(version)))
	if keep == "" {
		keep = RecordRetainUntil(policy, now)
	}
	out.Set("retainUntil", String(keep))
	figures := &RecordMarker{Redactions: map[string]int64{}}
	if policy.RedactOnStore && guard != nil {
		if err := guardRecord(out, guard, figures); err != nil {
			return nil, err
		}
	}
	figuresRaw, _ := json.Marshal(figures)
	figuresValue, _ := ParseJSON(figuresRaw)
	out.Set("records", figuresValue)
	out.Set("digest", String(RecordDigest(out)))
	return out, nil
}

func WriteCritique(critique *contract.RunEventCritique, policy RecordsPolicy, guard RecordGuard, now time.Time) (*contract.RunEventCritique, error) {
	raw, err := json.Marshal(critique)
	if err != nil {
		return nil, err
	}
	value, err := ParseJSON(raw)
	if err != nil {
		return nil, err
	}
	written, err := WriteRecord(CritiqueSchema, value, policy, guard, now, "")
	if err != nil {
		return nil, err
	}
	var out contract.RunEventCritique
	if err := json.Unmarshal(written.JSON(), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func ValidRecord(value *Value) bool {
	if !value.IsObject() || !supportedRecordVersion(value) || !recordDigestPattern.MatchString(value.Get("digest").Str()) || value.Get("digest").Str() != RecordDigest(value) {
		return false
	}
	schema := value.Get("schema").Str()
	if schema != CheckpointSchema && schema != ProvenanceSchema && schema != CritiqueSchema {
		return false
	}
	until := value.Get("retainUntil").Str()
	if len(until) == 0 || U16Length(until) > 40 {
		return false
	}
	_, err := time.Parse(time.RFC3339Nano, until)
	return err == nil
}

func ValidRecordHeader(value *Value, schema string) bool {
	if !value.IsObject() || value.Get("schema").Str() != schema || !supportedRecordVersion(value) || !recordDigestPattern.MatchString(value.Get("digest").Str()) {
		return false
	}
	until := value.Get("retainUntil").Str()
	if len(until) == 0 || U16Length(until) > 40 {
		return false
	}
	_, err := time.Parse(time.RFC3339Nano, until)
	return err == nil
}

type RecordsExpired struct{}

func (e *RecordsExpired) Error() string       { return "records expired" }
func (e *RecordsExpired) FailureCode() string { return "stale-state" }
func (e *RecordsExpired) Event() contract.RunEvent {
	reason := "records expired"
	return contract.StampEvent(contract.RunEvent{Guardrail: &contract.RunEventGuardrail{Type: "guardrail", Tool: "resume", Action: "refused", Reason: &reason, Detail: "records expired"}})
}

func RefuseExpired(until string, now time.Time) error {
	deadline, err := time.Parse(time.RFC3339Nano, until)
	if err != nil {
		return errSavedExecution
	}
	if !deadline.After(now) {
		return &RecordsExpired{}
	}
	return nil
}

type RecordsExport struct {
	Manifest contract.Export   `json:"manifest"`
	Records  []json.RawMessage `json:"records"`
}

func ExportRecords(records []json.RawMessage, policy RecordsPolicy, packageVersion string, now time.Time) (RecordsExport, string, contract.RunEvent, error) {
	if len(records) > 10000 {
		return RecordsExport{}, "", contract.RunEvent{}, errors.New("too many records")
	}
	entries := make([]contract.ExportRecord, 0, len(records))
	kept := make([]json.RawMessage, 0, len(records))
	for _, raw := range records {
		value, err := ParseJSON(raw)
		if err == nil && value.Get("schema").Str() == CheckpointSchema {
			if e := contract.CheckRecord("checkpoint", raw); e != nil {
				return RecordsExport{}, "", contract.RunEvent{}, e
			}
		}
		if err != nil || !ValidRecord(value) {
			return RecordsExport{}, "", contract.RunEvent{}, errors.New("the host supplied an invalid record for export")
		}
		entries = append(entries, contract.ExportRecord{Schema: value.Get("schema").Str(), Version: int64(value.Get("version").Num()), Digest: value.Get("digest").Str(), RetainUntil: value.Get("retainUntil").Str(), Bytes: int64(UTF8Bytes(CanonicalJSON(value)))})
		kept = append(kept, append(json.RawMessage(nil), raw...))
	}
	manifest := contract.Export{Schema: ExportSchema, Version: 1, RetainUntil: RecordRetainUntil(policy, now), CreatedAt: now.UTC().Format("2006-01-02T15:04:05.000Z"), PackageVersion: packageVersion, Policy: contract.ExportPolicy{RetainDays: int64(policy.RetainDays), RedactOnStore: policy.RedactOnStore, ExportFormat: policy.ExportFormat}, Records: entries}
	manifestRaw, err := json.Marshal(manifest)
	if err != nil {
		return RecordsExport{}, "", contract.RunEvent{}, err
	}
	manifestValue, _ := ParseJSON(manifestRaw)
	manifest.Digest = RecordDigest(manifestValue)
	out := RecordsExport{Manifest: manifest, Records: kept}
	raw, err := json.Marshal(out)
	if err != nil {
		return RecordsExport{}, "", contract.RunEvent{}, err
	}
	value, err := ParseJSON(raw)
	if err != nil {
		return RecordsExport{}, "", contract.RunEvent{}, err
	}
	text := CanonicalJSON(value)
	digest := "sha256:" + ContextDigest(text)
	event := contract.RunEvent{ExportWritten: &contract.RunEventExportWritten{Type: "export_written", Records: int64(len(records)), Bytes: int64(UTF8Bytes(text)), Digest: digest}}
	return out, text, contract.StampEvent(event), nil
}

func supportedRecordVersion(v *Value) bool {
	n := v.Get("version")
	if n == nil || n.Kind() != KindNumber {
		return false
	}
	return n.Num() == 1 || v.Get("schema").Str() == CheckpointSchema && (n.Num() == 2 || n.Num() == 3)
}

func nativePlanRecordFields(value *Value) bool {
	for _, row := range value.Get("durable").Get("dispatches").Items() {
		if row.Has("planPause") {
			return true
		}
	}
	for _, row := range value.Get("planState").Get("decisions").Items() {
		if row.Has("nativeInvocation") {
			return true
		}
	}
	return false
}
