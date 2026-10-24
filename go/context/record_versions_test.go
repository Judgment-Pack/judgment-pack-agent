package runcontext

import (
	"encoding/json"
	"errors"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"strings"
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

func TestRecordVersionsRetainedDigestSurvivesNumericDecode(t *testing.T) {
	payload := MustParseJSON(`{"z":2,"a":{"last":1,"first":0}}`)
	entry := (&Entry{ID: "compatibility:kept", Kind: "tool", Label: "Ordered fixture", Value: payload, Bytes: UTF8Bytes(payload.Text()), Digest: ContextDigest(payload.Text())}).Retained()
	for _, version := range []string{"1", "2", "2e0"} {
		saved := recordVector(t, "checkpoint").Clone()
		rawEntry, err := json.Marshal(entry)
		if err != nil {
			t.Fatal(err)
		}
		saved.Set("version", Number(2)).Set("context", ObjectOf("scope", String("compatibility"), "entries", Array(MustParseJSON(string(rawEntry)))))
		raw := strings.Replace(string(saved.JSON()), `"version":2`, `"version":`+version, 1)
		var checkpoint contract.Checkpoint
		if err := json.Unmarshal([]byte(raw), &checkpoint); err != nil {
			t.Fatal(err)
		}
		policy := Defaults
		ledger := NewContextLedger(&policy, nil)
		if err := ledger.Restore(checkpoint.Context); err != nil {
			t.Fatalf("%s: retained digest changed on decode: %v", version, err)
		}
		if string(checkpoint.Context.Entries[0].Value) != payload.Text() {
			t.Fatal("retained member order changed")
		}
	}
}

func TestRecordPolicyComparisonIsDetached(t *testing.T) {
	original := map[string]contract.PolicyMember{"pace": {Value: json.RawMessage(`"steady"`), Layer: "run"}}
	compared := policyComparisonRecord(original)
	if original["pace"].Source != nil || compared["pace"].Source == nil || *compared["pace"].Source != "layer" {
		t.Fatal("comparison rewrote historical source or lost its runtime default")
	}
	delete(compared, "pace")
	if len(original) != 1 {
		t.Fatal("comparison map aliases the checkpoint")
	}
	if policyComparisonRecord(nil) != nil || policyComparisonRecord(map[string]contract.PolicyMember{}) == nil {
		t.Fatal("comparison merged absent and empty policy")
	}
}

func TestRecordPolicyVersionPrecedesValueFallback(t *testing.T) {
	table := PolicyLayer{"models": map[string]any{"author": map[string]any{"model": "connected", "reasoning": "off"}}}
	host := PolicyHost{Connections: []string{"connected"}, Requests: 16}
	for layer := 0; layer < 3; layer++ {
		for _, version := range []any{0, -1, 1.5, nil, "1", float64(9007199254740992)} {
			inputs := []PolicyLayer{{}, {}, table}
			changed := PolicyLayer{}
			for key, value := range inputs[layer] {
				changed[key] = value
			}
			changed["version"] = version
			inputs[layer] = changed
			if _, err := ResolvePolicy(inputs[0], inputs[1], inputs[2], host); !errors.Is(err, contract.ErrInvalidRecord) {
				t.Fatalf("layer %d version %v bypassed admission: %v", layer, version, err)
			}
		}
		inputs := []PolicyLayer{{}, {}, table}
		inputs[layer] = PolicyLayer{"version": 3, "readBytes": 1}
		var future *contract.UnsupportedRecordVersion
		if _, err := ResolvePolicy(inputs[0], inputs[1], inputs[2], host); !errors.As(err, &future) {
			t.Fatalf("layer %d did not refuse future metadata before retired body: %v", layer, err)
		}
	}
}
