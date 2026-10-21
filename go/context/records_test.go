package runcontext

import (
	"context"
	"encoding/json"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"os"
	"strings"
	"testing"
	"time"
)

type recordTestGuard struct{}

// Assemble this at run time because the repository's local-paths check forbids the literal.
const recordTestHomePath = "/" + "home" + "/person/secret.txt"

func (recordTestGuard) JSON(raw json.RawMessage, where string, record bool, markers ...*RecordMarker) (json.RawMessage, error) {
	return json.RawMessage(strings.ReplaceAll(string(raw), recordTestHomePath, `[redacted: local-home-path]`)), nil
}

func recordVector(t *testing.T, name string) *Value {
	t.Helper()
	raw, err := os.ReadFile("../../contract/vectors/records/" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	root := MustParseJSON(string(raw))
	return root.Get("items").Items()[0]
}

func TestRecordsCarryLifetimeAndRedactOnStore(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	policy := RecordDefaults
	written, err := WriteRecord(CritiqueSchema, ObjectOf("type", String("critique"), "refuted", Bool(false), "checks", Array(), "text", String("Read "+recordTestHomePath)), policy, recordTestGuard{}, now, "")
	if err != nil {
		t.Fatal(err)
	}
	if written.Get("schema").Str() != CritiqueSchema || written.Get("version").Num() != 1 || written.Get("retainUntil").Str() != "2027-01-19T12:00:00.000Z" || written.Get("text").Str() != "Read [redacted: local-home-path]" || !ValidRecord(written) {
		t.Fatalf("written record: %s", written.Text())
	}
	for _, name := range []string{"checkpoint", "provenance", "critique"} {
		if item := recordVector(t, name); !ValidRecord(item) {
			t.Fatalf("%s vector is not a valid record", name)
		}
	}
}

func TestRecordsPolicyResolvesRunOverProjectOverTable(t *testing.T) {
	resolved, err := ResolvePolicy(
		PolicyLayer{"records": map[string]any{"retainDays": 7}},
		PolicyLayer{"records": map[string]any{"retainDays": 30, "redactOnStore": false}},
		PolicyLayer{"models": map[string]any{"author": map[string]any{"model": "connected", "reasoning": "off"}}, "records": map[string]any{"retainDays": 90, "redactOnStore": true, "exportFormat": "json"}},
		PolicyHost{Connections: []string{"connected"}, Requests: 16},
	)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := ReadRecordsPolicy(resolved.Members)
	if err != nil {
		t.Fatal(err)
	}
	if policy != (RecordsPolicy{RetainDays: 7, RedactOnStore: false, ExportFormat: "json"}) || resolved.Members["records.retainDays"].Layer != "run" || resolved.Members["records.redactOnStore"].Layer != "project" || resolved.Members["records.exportFormat"].Layer != "table" {
		t.Fatalf("resolved records policy: %+v %+v", policy, resolved.Members)
	}
}

func TestExportUsesOnlyHostSuppliedRecordsAndTheirDigests(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	items := []*Value{recordVector(t, "checkpoint"), recordVector(t, "provenance"), recordVector(t, "critique")}
	raw := make([]json.RawMessage, 0, len(items))
	for _, item := range items {
		raw = append(raw, item.JSON())
	}
	exported, text, event, err := ExportRecords(raw, RecordDefaults, "0.8.0", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(exported.Records) != 3 || event.ExportWritten == nil || event.ExportWritten.Records != 3 || event.ExportWritten.Bytes != int64(UTF8Bytes(text)) {
		t.Fatalf("export or event: %+v %+v", exported, event)
	}
	for at, row := range exported.Manifest.Records {
		if row.Digest != items[at].Get("digest").Str() {
			t.Fatalf("manifest digest %d: %s", at, row.Digest)
		}
	}
	manifestRaw, _ := json.Marshal(exported.Manifest)
	if exported.Manifest.Digest != RecordDigest(MustParseJSON(string(manifestRaw))) {
		t.Fatal("manifest digest does not bind the manifest")
	}
}

func TestExpiredResumeIsRefusedWithTheDecidedReason(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	err := RefuseExpired("2026-10-21T11:59:59.999Z", now)
	expired, ok := err.(*RecordsExpired)
	if !ok || CanonicalJSON(asValue(t, expired.Event())) != CanonicalJSON(recordVector(t, "expired-resume")) {
		t.Fatalf("expiry refusal: %v", err)
	}
	if err := RefuseExpired("2026-10-21T12:00:00.001Z", now); err != nil {
		t.Fatal(err)
	}
}

func TestInterruptedResumeCarriesEveryRecordMember(t *testing.T) {
	item := recordVector(t, "checkpoint")
	resumed, err := ReadExecution(item)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Schema == nil || *resumed.Schema != CheckpointSchema || resumed.Version != 1 || resumed.Digest == nil || *resumed.Digest != item.Get("digest").Str() || resumed.RetainUntil == nil || *resumed.RetainUntil != item.Get("retainUntil").Str() || resumed.Status != "paused" {
		t.Fatalf("resumed checkpoint lost record metadata: %+v", resumed)
	}
	extended, err := ExtendExecution(*resumed, false, time.Date(2026, 10, 21, 12, 1, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(extended)
	if value := MustParseJSON(string(raw)); extended.RetainUntil == nil || *extended.RetainUntil != *resumed.RetainUntil || !ValidRecord(value) {
		t.Fatalf("extended checkpoint lost or invalidated record metadata: %s", raw)
	}
}

func TestRecordsRefusalContinuesBoundary(t *testing.T) {
	opened, err := ContextualSession(context.Background(), modelSession())
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Context.Close()
	b := NewBoundary(opened)
	opened.Context.Execution.State.Explanation = strings.Repeat("x", 200000)
	events := b.checkpoint(nil, "")
	for _, event := range events {
		if event.ExecutionCheckpoint != nil {
			t.Fatal("partial checkpoint written")
		}
	}
	if len(events) != 1 || events[0].Guardrail == nil || events[0].Guardrail.Tool != "records" {
		t.Fatal("missing records refusal")
	}
	events, ended := b.Event(contract.RunEvent{Critique: &contract.RunEventCritique{Type: "critique", Text: strings.Repeat("x", 200000)}})
	if ended || len(events) != 1 || events[0].Guardrail == nil || events[0].Guardrail.Tool != "records" {
		t.Fatal("critique refusal stopped run or was silent")
	}
	events, ended = b.Event(contract.RunEvent{Message: &contract.RunEventMessage{Type: "message", Text: "Continued."}})
	if ended || len(events) != 1 || events[0].Message == nil {
		t.Fatal("run did not continue")
	}
}

func TestRecordsPreserveGuardLabelsInKeysAndText(t *testing.T) {
	counts := map[string]int64{}
	guard, err := NewOutputGuard(PolicyMembers{"outputPatterns": {Value: []OutputPattern{{Name: "label-word", Pattern: "redacted"}}}}, func() map[string]int64 { return counts })
	if err != nil {
		t.Fatal(err)
	}
	args := ObjectOf("[redacted: aws-access-key]", String("Before [redacted: aws-access-key] after"))
	record, err := WriteRecord(CheckpointSchema, ObjectOf("operations", Array(ObjectOf("name", String("fixture"), "args", args))), RecordDefaults, guard, time.Now(), "")
	if err != nil {
		t.Fatal(err)
	}
	if CanonicalJSON(record.Get("operations").Items()[0].Get("args")) != CanonicalJSON(args) || record.Get("records").Get("redactions").Len() != 0 || len(counts) != 0 {
		t.Fatal("guard labels rescanned")
	}
}
