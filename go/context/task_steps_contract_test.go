package runcontext

import (
	"encoding/json"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"os"
	"testing"
)

func TestTaskSteps183HandwrittenCurrentRecords(t *testing.T) {
	raw, err := os.ReadFile("../../contract/testdata/task-steps/records.json")
	if err != nil {
		t.Fatal(err)
	}
	v := MustParseJSON(string(raw))
	for _, row := range v.Get("items").Items() {
		t.Run(row.Get("name").Str(), func(t *testing.T) {
			cp := row.Get("checkpoint")
			if _, err := ReadExecution(cp); err != nil {
				t.Fatal(err)
			}
			if err := contract.CheckRecord("checkpoint", cp.JSON()); err != nil {
				t.Fatal(err)
			}
			old := cp.Clone()
			old.Set("version", Number(2))
			if err := contract.CheckRecord("checkpoint", old.JSON()); err == nil {
				t.Fatal("published cp2 accepted current task fields")
			}
		})
	}
	for _, row := range v.Get("events").Items() {
		t.Run(row.Get("name").Str(), func(t *testing.T) {
			if err := contract.CheckRecord("event", row.Get("event").JSON()); err != nil {
				t.Fatal(err)
			}
			if row.Get("event").Has("nativeInvocation") {
				old := row.Get("event").Clone()
				old.Set("version", Number(1))
				if err := contract.CheckRecord("event", old.JSON()); err == nil {
					t.Fatal("published event1 admitted native invocation")
				}
			}
		})
	}
	for _, row := range v.Get("invalid").Items() {
		t.Run(row.Get("name").Str(), func(t *testing.T) {
			var state any
			if json.Unmarshal(v.Get("items").Items()[int(row.Get("base").Num())].Get("checkpoint").JSON(), &state) != nil {
				t.Fatal("fixture decode")
			}
			at := state
			path := row.Get("path").Items()
			for _, key := range path[:len(path)-1] {
				if key.Kind() == KindNumber {
					at = at.([]any)[int(key.Num())]
				} else {
					at = at.(map[string]any)[key.Str()]
				}
			}
			last := path[len(path)-1].Str()
			m := at.(map[string]any)
			if row.Has("remove") {
				delete(m, last)
			} else {
				var value any
				_ = json.Unmarshal(row.Get("value").JSON(), &value)
				m[last] = value
			}
			raw, _ := json.Marshal(state)
			if _, err := ReadExecution(MustParseJSON(string(raw))); err == nil {
				t.Fatal("malformed current task record admitted")
			}
		})
	}
}
