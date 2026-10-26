package runcontext

import (
	"encoding/json"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"os"
	"testing"
)

func TestRuntimeActionCurrentRecords(t *testing.T) {
	for _, name := range []string{"runtime-pause", "runtime-request"} {
		raw, err := os.ReadFile("../../contract/vectors/approval/" + name + ".json")
		if err != nil {
			t.Fatal(err)
		}
		var vector struct{ Items []json.RawMessage }
		if json.Unmarshal(raw, &vector) != nil {
			t.Fatal("bad vector")
		}
		for _, item := range vector.Items {
			v, err := ParseJSON(item)
			if err != nil {
				t.Fatal(err)
			}
			if name == "runtime-pause" {
				if _, err = ReadExecution(v); err != nil {
					t.Fatal(err)
				}
				v.Set("version", Number(2))
				if _, err = ReadExecution(v); err == nil {
					t.Fatal("Runtime binding accepted under frozen checkpoint2")
				}
			} else {
				if err = contract.CheckRecord("event", item); err != nil {
					t.Fatal(err)
				}
				v.Set("version", Number(1))
				if err = contract.CheckRecord("event", v.JSON()); err == nil {
					t.Fatal("Runtime binding accepted under frozen event1")
				}
			}
		}
	}
}
