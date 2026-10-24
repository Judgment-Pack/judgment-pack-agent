package runcontext

import (
	"encoding/json"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"testing"
	"time"
)

func TestRecordVersionsPreserveHistoricalBytesAndProvenance(t *testing.T) {
	value := recordVector(t, "checkpoint").Clone()
	value.Set("used", Number(1)).Set("completions", Array(ObjectOf("request", Number(1), "phase", String("work"), "result", ObjectOf("finishReason", String("stop")), "connection", String("primary"), "role", String("author"))))
	value.Set("policy", ObjectOf("pace", ObjectOf("value", String("steady"), "layer", String("run"))))
	value.Set("operations", Array(ObjectOf("id", String("call"), "name", String("read"), "args", ObjectOf("arbitrary", Array(Null(), Bool(false), Number(0))), "status", String("complete"), "result", ObjectOf("content", Array(), "providerExtension", ObjectOf("kept", Bool(true))))))
	value.Set("digest", String(RecordDigest(value)))
	saved, err := ReadExecution(value)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(saved)
	if CanonicalJSON(MustParseJSON(string(raw))) != CanonicalJSON(value) {
		t.Fatal("historical read changed the record")
	}
	state, err := executionOf(saved)
	if err != nil {
		t.Fatal(err)
	}
	next, err := WriteRecord(CheckpointSchema, state.Value(), RecordDefaults, nil, time.Now(), state.RetainUntil)
	if err != nil {
		t.Fatal(err)
	}
	if next.Get("version").Num() != 2 || next.Get("retainUntil").Str() != value.Get("retainUntil").Str() || !ValidRecord(next) {
		t.Fatalf("current snapshot metadata: %s", next.Text())
	}
	if next.Get("completions").Items()[0].Get("connection").Str() != "primary" || next.Get("completions").Items()[0].Get("role").Str() != "author" {
		t.Fatal("provenance connection/role lost")
	}
	if !next.Get("operations").Items()[0].Get("result").Get("providerExtension").Get("kept").Truth() {
		t.Fatal("declared arbitrary provider payload lost")
	}
	for _, record := range []*Value{value, next} {
		exported, _, _, err := ExportRecords([]json.RawMessage{record.JSON()}, RecordDefaults, "test", time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if exported.Manifest.Records[0].Version != int64(record.Get("version").Num()) {
			t.Fatal("export guessed version")
		}
		if CanonicalJSON(MustParseJSON(string(exported.Records[0]))) != CanonicalJSON(record) {
			t.Fatal("export rewrote supplied record")
		}
	}
}
func TestRecordVersionFailedOpenNotices(t *testing.T) {
	for _, cause := range []error{&contract.UnsupportedRecordVersion{Kind: "checkpoint", Version: 3}, &contract.UnknownRecordMember{Member: "/unexpected"}} {
		events := FailedToOpen(cause)
		if len(events) < 2 || events[0].Guardrail == nil {
			t.Fatal("named notice missing")
		}
		for _, event := range events {
			raw, err := json.Marshal(event)
			if err != nil {
				t.Fatal(err)
			}
			var obj map[string]any
			json.Unmarshal(raw, &obj)
			if obj["version"] != float64(1) {
				t.Fatalf("unversioned failed-open event: %s", raw)
			}
			if err := contract.CheckRecord("event", raw); err != nil {
				t.Fatal(err)
			}
		}
	}
}
