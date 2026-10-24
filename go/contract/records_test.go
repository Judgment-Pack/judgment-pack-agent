package contract

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestRecordCompatibilityVectors(t *testing.T) {
	for _, pair := range [][2]string{{"checkpoint", "checkpoint"}, {"event", "events"}, {"policy", "policy"}} {
		kind, file := pair[0], pair[1]
		raw, err := os.ReadFile("../../contract/vectors/record-versions/" + file + ".json")
		if err != nil {
			t.Fatal(err)
		}
		var vector struct {
			Items []json.RawMessage
			Cases []struct {
				Name    string
				Raw     string
				Input   json.RawMessage
				Outcome string
			}
		}
		if err = json.Unmarshal(raw, &vector); err != nil {
			t.Fatal(err)
		}
		reader := func(raw []byte) error {
			switch kind {
			case "checkpoint":
				var value Checkpoint
				return json.Unmarshal(raw, &value)
			case "event":
				var value RunEvent
				return json.Unmarshal(raw, &value)
			default:
				var value Policy
				return json.Unmarshal(raw, &value)
			}
		}
		for _, value := range vector.Items {
			if err := reader(value); err != nil {
				t.Fatalf("%s positive vector: %v", kind, err)
			}
		}
		for _, row := range vector.Cases {
			t.Run(kind+"/"+row.Name, func(t *testing.T) {
				input := row.Input
				if row.Raw != "" {
					input = []byte(row.Raw)
				}
				err := reader(input)
				var version *UnsupportedRecordVersion
				var member *UnknownRecordMember
				switch row.Outcome {
				case "accepted":
					if err != nil {
						t.Fatal(err)
					}
				case "UnsupportedRecordVersion":
					if !errors.As(err, &version) {
						t.Fatalf("want named version refusal, got %v", err)
					}
				case "UnknownRecordMember":
					if !errors.As(err, &member) {
						t.Fatalf("want named member refusal, got %v", err)
					}
				default:
					if !errors.Is(err, ErrInvalidRecord) {
						t.Fatalf("want invalid record, got %v", err)
					}
				}
			})
		}
	}
}
func TestHistoricalRecordMembersAreFrozen(t *testing.T) {
	raw, err := os.ReadFile("../../contract/records/historical-shapes.json")
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%x", sha256.Sum256(raw)) != "71e8ab3df9fa5daf051e3913f90ab62b577932d3a0ba5c0fa966b59a54a43925" {
		t.Fatal("published historical member inventory changed")
	}
	current := shapeObject(recordShapes["current"]["checkpoint.json"])
	properties := shapeObject(current["properties"])
	properties["unreleased"] = map[string]any{}
	defer delete(properties, "unreleased")
	for _, version := range []int{1, 2} {
		raw := []byte(fmt.Sprintf(`{"version":%d,"id":"compatibility","requestKey":"%s","phase":"work","status":"running","unit":"model-requests","used":0,"limit":20,"grants":[],"plan":[],"explanation":"","operations":[],"context":{"scope":"compatibility","entries":[]},"unreleased":true}`, version, "0000000000000000000000000000000000000000000000000000000000000000"))
		err := CheckRecord("checkpoint", raw)
		var unknown *UnknownRecordMember
		if version == 1 && !errors.As(err, &unknown) {
			t.Fatalf("historical reader guessed new member: %v", err)
		}
		if version == 2 && err != nil {
			t.Fatal(err)
		}
	}
}
func TestCurrentEventWriterCoversEveryVector(t *testing.T) {
	raw, _ := os.ReadFile("../../contract/vectors/record-versions/events.json")
	var vector struct{ Items []json.RawMessage }
	if err := json.Unmarshal(raw, &vector); err != nil {
		t.Fatal(err)
	}
	for _, raw := range vector.Items {
		var value RunEvent
		if err := json.Unmarshal(raw, &value); err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(StampEvent(value))
		if err != nil {
			t.Fatal(err)
		}
		var object map[string]any
		json.Unmarshal(encoded, &object)
		if object["version"] != float64(1) {
			t.Fatalf("missing writer version: %s", encoded)
		}
		if err := CheckRecord("event", encoded); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRecordSizeLimitIsForCheckpoints(t *testing.T) {
	text := strings.Repeat("x", 4*1024*1024+1)
	raw, _ := json.Marshal(map[string]any{"version": 1, "type": "message", "text": text})
	var event RunEvent
	if err := json.Unmarshal(raw, &event); err != nil {
		t.Fatal(err)
	}
	if event.Message == nil || event.Message.Text != text {
		t.Fatal("large ordinary event changed")
	}
	if CheckRecord("checkpoint", raw) == nil {
		t.Fatal("checkpoint size bound removed")
	}
}

func TestRecordStructuralWorkIsBounded(t *testing.T) {
	values := make([]string, 200001)
	for i := range values {
		values[i] = "kept"
	}
	raw, _ := json.Marshal(map[string]any{"version": 1, "outputAllow": values})
	if CheckRecord("policy", raw) == nil {
		t.Fatal("structural visit bound removed")
	}
	properties := shapeObject(shapeObject(recordShapes["current"]["policy.json"])["properties"])
	properties["next"] = map[string]any{"$ref": "policy.json"}
	defer delete(properties, "next")
	var value any = map[string]any{"version": 1}
	for i := 0; i < 100; i++ {
		value = map[string]any{"version": 1, "next": value}
	}
	raw, _ = json.Marshal(value)
	if CheckRecord("policy", raw) == nil {
		t.Fatal("structural depth bound removed")
	}
}

func TestRecordNumericDecodeKeepsOpaqueTokens(t *testing.T) {
	raw, err := os.ReadFile("../../contract/vectors/record-versions/checkpoint.json")
	if err != nil {
		t.Fatal(err)
	}
	var vector struct{ Cases []struct{ Name, Raw string } }
	if err := json.Unmarshal(raw, &vector); err != nil {
		t.Fatal(err)
	}
	for _, row := range vector.Cases {
		if row.Name != "opaque numeric tokens preserved" {
			continue
		}
		original := []byte(row.Raw)
		before := string(original)
		normalized, err := recordDecodeBytes("checkpoint", original)
		if err != nil {
			t.Fatal(err)
		}
		if string(original) != before {
			t.Fatal("input bytes changed")
		}
		if !strings.Contains(string(normalized), `"n":2e0`) || !strings.Contains(string(normalized), `"near":1.00000000000000001`) {
			t.Fatal("opaque numeric token changed")
		}
		var value Checkpoint
		if err := json.Unmarshal(original, &value); err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(encoded), `"retainUntil":"2099-01-01T00:00:00.000Z"`) || !strings.Contains(string(encoded), `"digest":"sha256:`+strings.Repeat("a", 64)+`"`) {
			t.Fatal("lifecycle metadata changed")
		}
		if !strings.Contains(string(encoded), `"n":2e0`) {
			t.Fatal("typed codec changed opaque numeric token")
		}
		return
	}
	t.Fatal("missing opaque numeric control")
}
