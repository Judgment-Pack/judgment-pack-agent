package contract

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"
)

func dialectShapeFixtures(t *testing.T) ([]json.RawMessage, map[string]any, map[string]any) {
	t.Helper()
	load := func(path string, value any) {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(raw, value); err != nil {
			t.Fatal(err)
		}
	}
	var rows struct{ Items []json.RawMessage }
	load("../../contract/vectors/dialect-authoring/retention.json", &rows)
	var current, historical map[string]any
	load("../../contract/testdata/dialect-authoring/authoring.json", &current)
	load("../../contract/testdata/dialect-authoring/superseded-private-draft-identity.json", &historical)
	return rows.Items, current["conversionContract"].(map[string]any)["spec"].(map[string]any), historical["spec"].(map[string]any)
}

func TestDialectGeneratedInventoryNativeTuples(t *testing.T) {
	rows, current, historical := dialectShapeFixtures(t)
	// No production record references dialect-draft.json; this test-only graph edge
	// exercises the installed generated inventory with the actual native walker.
	properties := shapeObject(shapeObject(recordShapes["current"]["checkpoint.json"])["properties"])
	properties["dialectDraftProbe"] = map[string]any{"$ref": "dialect-draft.json"}
	defer delete(properties, "dialectDraftProbe")
	native := func(draft any) error {
		checkpoint := map[string]any{"version": 3, "id": "compatibility", "requestKey": "0000000000000000000000000000000000000000000000000000000000000000", "phase": "work", "status": "running", "unit": "model-requests", "used": 0, "limit": 20, "grants": []any{}, "plan": []any{}, "explanation": "", "operations": []any{}, "context": map[string]any{"scope": "compatibility", "entries": []any{}}, "dialectDraftProbe": draft}
		raw, err := json.Marshal(checkpoint)
		if err != nil {
			t.Fatal(err)
		}
		return CheckRecord("checkpoint", raw)
	}
	witness := func() (map[string]any, map[string]any) {
		var value map[string]any
		if err := json.Unmarshal(rows[len(rows)-1], &value); err != nil {
			t.Fatal(err)
		}
		return value, value["candidate"].(map[string]any)["conversionContract"].(map[string]any)["spec"].(map[string]any)
	}
	keys := []string{"version", "revision", "schemaSha256", "corpusSha256"}
	// #269: the RC2 tuple shares the final's digests, so only its version and revision can mix.
	var rc2 struct{ Spec map[string]any }
	if raw, err := os.ReadFile("../../contract/testdata/dialect-authoring/superseded-rc2-identity.json"); err != nil || json.Unmarshal(raw, &rc2) != nil {
		t.Fatal("RC2 fixture", err)
	}
	for _, other := range []map[string]any{historical, rc2.Spec} {
		for mask := 0; mask < 16; mask++ {
			value, spec := witness()
			for i, key := range keys {
				spec[key] = current[key]
				if mask&(1<<i) != 0 {
					spec[key] = other[key]
				}
			}
			err := native(value)
			sameDigests := other["schemaSha256"] == current["schemaSha256"] && other["corpusSha256"] == current["corpusSha256"]
			if mask == 0 || mask == 15 || sameDigests && (mask&3 == 0 || mask&3 == 3) {
				if err != nil {
					t.Fatalf("native acceptance of exact tuple %v %d: %v", other["version"], mask, err)
				}
			} else if !errors.Is(err, ErrInvalidRecord) {
				t.Fatalf("native refusal of hybrid tuple %v %d: %v", other["version"], mask, err)
			}
		}
	}
	for _, key := range keys {
		value, spec := witness()
		delete(spec, key)
		if err := native(value); !errors.Is(err, ErrInvalidRecord) {
			t.Fatalf("native missing-field refusal %s: %v", key, err)
		}
	}
	value, spec := witness()
	spec["extra"] = "unsupported"
	var unknown *UnknownRecordMember
	if err := native(value); !errors.As(err, &unknown) {
		t.Fatalf("native unknown-field refusal: %v", err)
	}
}

func TestDialectGeneratedCodecInspection(t *testing.T) {
	rows, _, _ := dialectShapeFixtures(t)
	// The generated public codec preserves structural shape; exact tuple semantics
	// are enforced above by the native walker, not inferred from codec decoding.
	accepted := 0
	for _, raw := range rows {
		var tag map[string]json.RawMessage
		if err := json.Unmarshal(raw, &tag); err != nil {
			t.Fatal(err)
		}
		if tag["candidate"] == nil {
			continue
		}
		var value DialectDraft
		if err := json.Unmarshal(raw, &value); err != nil {
			t.Fatal("generated codec acceptance", err)
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var want, got any
		if err = json.Unmarshal(raw, &want); err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(encoded, &got); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(want, got) {
			t.Fatal("generated codec changed historical/current checkpoint")
		}
		accepted++
	}
	if accepted != 14 {
		t.Fatalf("inspection checkpoint count %d", accepted)
	}
	var value map[string]any
	if err := json.Unmarshal(rows[len(rows)-1], &value); err != nil {
		t.Fatal(err)
	}
	value["candidate"].(map[string]any)["conversionContract"].(map[string]any)["spec"].(map[string]any)["extra"] = "unsupported"
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var decoded DialectDraft
	if err = json.Unmarshal(raw, &decoded); err == nil {
		t.Fatal("generated codec accepted unknown tuple field")
	}
}
