package contractgen

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Exercise the generated public codec with the original handwritten retention
// rows: absence, explicit null, an empty array, and nonempty values. Expected
// presence comes from those inputs, never from the generated output.
func TestGeneratedOptionalNullableUnionsPreservePresence(t *testing.T) {
	dir := t.TempDir()
	generated, err := Generate("../../../contract/schemas", "contract")
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		"contract_gen.go": generated,
		"go.mod":          []byte("module generated-presence-test\n\ngo 1.26.0\n"),
		"presence_test.go": []byte(`package contract
import("encoding/json";"os";"reflect";"testing")
func TestPresence(t *testing.T) {
 raw,err:=os.ReadFile("fixture.json");if err!=nil{t.Fatal(err)}
 var fixture struct{Items []json.RawMessage};if err=json.Unmarshal(raw,&fixture);err!=nil{t.Fatal(err)}
 for _,row:=range []int{0,6,7,8,12,13}{
  input:=fixture.Items[row]
  var value DialectDraft;if err=json.Unmarshal(input,&value);err!=nil{t.Fatalf("row %d: %v",row,err)}
  encoded,err:=json.Marshal(value);if err!=nil{t.Fatal(err)}
  var want,got any;if err=json.Unmarshal(input,&want);err!=nil{t.Fatal(err)};if err=json.Unmarshal(encoded,&got);err!=nil{t.Fatal(err)}
  if !reflect.DeepEqual(got,want){t.Fatalf("row %d optional nullable presence changed: input=%s output=%s",row,input,encoded)}
 }
}
`),
	}
	for target, source := range map[string]string{
		"support.go":              "../../contract/support.go",
		"records.go":              "../../contract/records.go",
		"record_shapes.json":      "../../contract/record_shapes.json",
		"record_shapes_v012.json": "../../contract/record_shapes_v012.json",
		"fixture.json":            "../../../contract/vectors/dialect-authoring/retention.json",
	} {
		body, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		files[target] = body
	}
	for name, body := range files {
		if err = os.WriteFile(filepath.Join(dir, name), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	command := exec.Command("go", "test", "-count=1", ".")
	command.Dir = dir
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("generated optional-null runtime probe: %v\n%s", err, output)
	}
}
