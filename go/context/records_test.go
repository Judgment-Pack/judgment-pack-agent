package runcontext

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

type recordTestGuard struct{}

func (recordTestGuard) JSON(raw json.RawMessage, where string, record bool) (json.RawMessage, error) {
	return json.RawMessage(strings.ReplaceAll(string(raw), `/home/person/secret.txt`, `[redacted: local-home-path]`)), nil
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
	written, err := WriteRecord(CritiqueSchema, ObjectOf("type", String("critique"), "refuted", Bool(false), "checks", Array(), "text", String("Read /home/person/secret.txt")), policy, recordTestGuard{}, now, "")
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
