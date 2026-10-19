package contract

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Judgment-Pack/judgment-pack-agent/go/internal/contractgen"
)

const (
	schemasDir = "../../contract/schemas"
	vectorsDir = "../../contract/vectors"
)

type vectorFile struct {
	Schema string            `json:"schema"`
	Items  []json.RawMessage `json:"items"`
}

// TestEveryVectorRoundTrips decodes every item of every vector into the Go type its schema
// address names and encodes it again: the value that comes back is the value that went in, member
// for member, an open object's unnamed members included.
func TestEveryVectorRoundTrips(t *testing.T) {
	files := 0
	items := 0
	err := filepath.WalkDir(vectorsDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".json") {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var vector vectorFile
		if err := json.Unmarshal(raw, &vector); err != nil {
			t.Errorf("%s: %v", path, err)
			return nil
		}
		files++
		name, _ := filepath.Rel(vectorsDir, path)
		newValue, ok := Registry[vector.Schema]
		if !ok {
			t.Errorf("%s: no Go type for the schema address %q", name, vector.Schema)
			return nil
		}
		for i, item := range vector.Items {
			items++
			value := newValue()
			if err := json.Unmarshal(item, value); err != nil {
				t.Errorf("%s item %d: decode into %T: %v", name, i, value, err)
				continue
			}
			encoded, err := json.Marshal(value)
			if err != nil {
				t.Errorf("%s item %d: encode %T: %v", name, i, value, err)
				continue
			}
			if !sameJSON(t, item, encoded) {
				t.Errorf("%s item %d: %T does not round-trip\n in:  %s\n out: %s", name, i, value, clip(item), clip(encoded))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if files == 0 || items == 0 {
		t.Fatalf("no vectors read from %s", vectorsDir)
	}
	t.Logf("%d items in %d vector files read", items, files)
}

// TestTheTypesAreTheGeneratorsOutput holds contract_gen.go to what contractgen writes from the
// schemas now, so a schema change without `go generate ./contract` fails here.
func TestTheTypesAreTheGeneratorsOutput(t *testing.T) {
	want, err := contractgen.Generate(schemasDir, "contract")
	if err != nil {
		t.Fatal(err)
	}
	have, err := os.ReadFile("contract_gen.go")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(want, have) {
		t.Fatal("contract_gen.go is not contractgen's output for contract/schemas: run `go generate ./contract` in go/")
	}
}

// TestAClosedObjectRefusesAnUnnamedMember checks the one way a type is stricter than a map: a
// closed schema's object with a member it does not name is refused on decode.
func TestAClosedObjectRefusesAnUnnamedMember(t *testing.T) {
	var question Question
	err := json.Unmarshal([]byte(`{"title":"T","question":"Which?","selection":"single","options":[],"recommendedOptionId":null,"recommendation":null,"unnamed":true}`), &question)
	if err == nil || !strings.Contains(err.Error(), `"unnamed"`) {
		t.Fatalf("a closed object took a member its schema does not name: %v", err)
	}
}

// TestAnOpenObjectKeepsAnUnnamedMember is the other side: an open object writes it back.
func TestAnOpenObjectKeepsAnUnnamedMember(t *testing.T) {
	in := []byte(`{"type":"end","later":{"kept":[1,2]}}`)
	var event RunEvent
	if err := json.Unmarshal(in, &event); err != nil {
		t.Fatal(err)
	}
	if event.End == nil {
		t.Fatalf("an end event decoded as %+v", event)
	}
	out, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if !sameJSON(t, in, out) {
		t.Fatalf("an open object dropped a member: %s", out)
	}
}

func sameJSON(t *testing.T, a, b []byte) bool {
	t.Helper()
	var left, right any
	if err := json.Unmarshal(a, &left); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &right); err != nil {
		t.Fatal(err)
	}
	return reflect.DeepEqual(left, right)
}

func clip(b []byte) string {
	if len(b) > 400 {
		return string(b[:400]) + "…"
	}
	return string(b)
}
