package contractgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRequiredIntegerDiscriminatorGeneration(t *testing.T) {
	for _, row := range []struct {
		name, first, second, required, helper string
	}{
		{"integer constants", `{"const":1}`, `{"const":2}`, `["version","payload"]`, "memberInteger"},
		{"singleton integer enums", `{"enum":[1]}`, `{"enum":[2]}`, `["version","payload"]`, "memberInteger"},
		{"optional version remains ambiguous", `{"const":1}`, `{"const":2}`, `["payload"]`, ""},
		{"fractional constants remain ambiguous", `{"const":1.5}`, `{"const":2.5}`, `["version","payload"]`, ""},
		{"unsafe constants remain ambiguous", `{"const":9007199254740992}`, `{"const":9007199254740994}`, `["version","payload"]`, ""},
		{"string discriminator control", `{"const":"one"}`, `{"const":"two"}`, `["version","payload"]`, "memberString"},
	} {
		t.Run(row.name, func(t *testing.T) {
			dir := t.TempDir()
			schema := `{"$schema":"https://json-schema.org/draft/2020-12/schema","$id":"sample.json","oneOf":[{"$ref":"#/$defs/first"},{"$ref":"#/$defs/second"}],"$defs":{"first":{"type":"object","additionalProperties":false,"required":` + row.required + `,"properties":{"version":` + row.first + `,"payload":{}}},"second":{"type":"object","additionalProperties":false,"required":` + row.required + `,"properties":{"version":` + row.second + `,"payload":{}}}}}`
			if err := os.WriteFile(filepath.Join(dir, "sample.json"), []byte(schema), 0600); err != nil {
				t.Fatal(err)
			}
			g, err := load(dir)
			if err != nil {
				t.Fatal(err)
			}
			g.decls = map[string]string{}
			g.emitUnion(loc{"sample.json", ""}, "Sample")
			if g.err != nil {
				t.Fatal(g.err)
			}
			text := g.decls["Sample"]
			if strings.Contains(text, "memberInteger(data") != (row.helper == "memberInteger") || strings.Contains(text, "memberString(data") != (row.helper == "memberString") {
				t.Fatal("generator selected the wrong discriminator path")
			}
			if row.helper == "memberInteger" && (!strings.Contains(text, "case 1:") || !strings.Contains(text, "case 2:") || !strings.Contains(text, "json.Unmarshal(view,")) {
				t.Fatal("integer tags did not use the detached typed decode view")
			}
		})
	}
}
