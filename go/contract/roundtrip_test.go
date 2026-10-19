package contract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
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
// for member, an open object's unnamed members included. And no decoded value holds, among the
// members its type keeps as unnamed (AdditionalMembers), a member its schema names: an open type
// that dropped a member would otherwise round-trip it unnoticed as an unnamed one (agent#53).
func TestEveryVectorRoundTrips(t *testing.T) {
	named := schemaMembers(t)
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
			for _, problem := range keptAsUnnamed(reflect.ValueOf(value), named, "") {
				t.Errorf("%s item %d: %s", name, i, problem)
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

// schemaMembers reads, for every generated struct, the members its schema names, from the schema
// itself: its properties and those of its allOf branches, following $ref. It does not use the
// generator's own reading of a node's members, so a generator or a type that drops a member is
// seen here.
func schemaMembers(t *testing.T) map[string]map[string]bool {
	t.Helper()
	addresses, err := contractgen.Addresses(schemasDir)
	if err != nil {
		t.Fatal(err)
	}
	documents := map[string]any{}
	entries, err := os.ReadDir(schemasDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		raw, err := os.ReadFile(filepath.Join(schemasDir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		var document any
		if err := json.Unmarshal(raw, &document); err != nil {
			t.Fatal(err)
		}
		documents[entry.Name()] = document
	}
	at := func(file, pointer string) map[string]any {
		var node any = documents[file]
		if pointer != "" {
			for _, segment := range strings.Split(pointer[1:], "/") {
				segment = strings.ReplaceAll(strings.ReplaceAll(segment, "~1", "/"), "~0", "~")
				switch held := node.(type) {
				case map[string]any:
					node = held[segment]
				case []any:
					var index int
					fmt.Sscanf(segment, "%d", &index)
					node = held[index]
				}
			}
		}
		object, _ := node.(map[string]any)
		return object
	}
	var collect func(file, pointer string, into map[string]bool, depth int)
	collect = func(file, pointer string, into map[string]bool, depth int) {
		node := at(file, pointer)
		if node == nil || depth > 32 {
			return
		}
		if ref, ok := node["$ref"].(string); ok {
			target, fragment, _ := strings.Cut(ref, "#")
			if target == "" {
				target = file
			}
			collect(target, fragment, into, depth+1)
			return
		}
		properties, _ := node["properties"].(map[string]any)
		for member := range properties {
			into[member] = true
		}
		branches, _ := node["allOf"].([]any)
		for i := range branches {
			collect(file, fmt.Sprintf("%s/allOf/%d", pointer, i), into, depth+1)
		}
	}
	out := map[string]map[string]bool{}
	for typeName, address := range addresses {
		file, pointer, _ := strings.Cut(address, "#")
		members := map[string]bool{}
		collect(file, pointer, members, 0)
		out[typeName] = members
	}
	return out
}

// keptAsUnnamed walks a decoded value and names every member a struct keeps in AdditionalMembers
// although its schema names it.
func keptAsUnnamed(value reflect.Value, named map[string]map[string]bool, path string) []string {
	switch value.Kind() {
	case reflect.Pointer, reflect.Interface:
		if value.IsNil() {
			return nil
		}
		return keptAsUnnamed(value.Elem(), named, path)
	case reflect.Slice, reflect.Array:
		if value.Type().Elem() == reflect.TypeOf(byte(0)) {
			return nil
		}
		var out []string
		for i := 0; i < value.Len(); i++ {
			out = append(out, keptAsUnnamed(value.Index(i), named, fmt.Sprintf("%s[%d]", path, i))...)
		}
		return out
	case reflect.Map:
		var out []string
		for _, key := range value.MapKeys() {
			out = append(out, keptAsUnnamed(value.MapIndex(key), named, fmt.Sprintf("%s[%v]", path, key))...)
		}
		return out
	case reflect.Struct:
	default:
		return nil
	}
	var out []string
	typeName := value.Type().Name()
	for i := 0; i < value.NumField(); i++ {
		field := value.Type().Field(i)
		if !field.IsExported() {
			continue
		}
		if field.Name == "AdditionalMembers" {
			var kept []string
			for _, key := range value.Field(i).MapKeys() {
				if named[typeName][key.String()] {
					kept = append(kept, key.String())
				}
			}
			sort.Strings(kept)
			for _, member := range kept {
				out = append(out, fmt.Sprintf("%s%s keeps %q as an unnamed member, and its schema names it", typeName, path, member))
			}
			continue
		}
		out = append(out, keptAsUnnamed(value.Field(i), named, path+"."+field.Name)...)
	}
	return out
}

// TestTheDroppedMemberCheckSeesADroppedMember is the check above on a value built to fail it: a
// tool_call whose name sits among the unnamed members.
func TestTheDroppedMemberCheckSeesADroppedMember(t *testing.T) {
	call := ToolCall{Type: "tool_call", Args: json.RawMessage(`{}`), AdditionalMembers: map[string]json.RawMessage{"name": json.RawMessage(`"validate"`), "later": json.RawMessage(`1`)}}
	problems := keptAsUnnamed(reflect.ValueOf(&call), schemaMembers(t), "")
	if len(problems) != 1 || !strings.Contains(problems[0], `"name"`) {
		t.Fatalf("the check reported %v", problems)
	}
}

// TestEachProfilesReportDecodesByItsProfile decodes each closed profile's report vector by that
// profile (DecodeDelegatedTaskReport): the branch is the one the profile's report names, and the
// value round-trips. The value alone cannot say: a research report decodes as an analyze report
// too, and only the profile tells them apart.
func TestEachProfilesReportDecodesByItsProfile(t *testing.T) {
	var profiles struct {
		Items []DelegatedTaskProfile `json:"items"`
	}
	readJSON(t, filepath.Join(vectorsDir, "delegation", "profiles.json"), &profiles)
	if len(profiles.Items) != 4 {
		t.Fatalf("%d profiles", len(profiles.Items))
	}
	branch := func(report DelegatedTaskReport) string {
		switch {
		case report.DelegatedTaskAnalyzeReport != nil:
			return "analyze"
		case report.DelegatedTaskReviewReport != nil:
			return "review"
		case report.DelegatedTaskResearchReport != nil:
			return "research"
		case report.DelegatedTaskAuthorTestReport != nil:
			return "author-test"
		}
		return ""
	}
	decoded := 0
	for _, profile := range profiles.Items {
		var vector struct {
			Items []json.RawMessage `json:"items"`
		}
		readJSON(t, filepath.Join(vectorsDir, "delegation", "report-"+*profile.Report+".json"), &vector)
		for i, item := range vector.Items {
			report, err := DecodeDelegatedTaskReport(profile, item)
			if err != nil {
				t.Fatalf("%s item %d: %v", profile.ID, i, err)
			}
			if got := branch(report); got != *profile.Report {
				t.Errorf("%s item %d decoded as %s", profile.ID, i, got)
			}
			encoded, err := json.Marshal(report)
			if err != nil {
				t.Fatal(err)
			}
			if !sameJSON(t, item, encoded) {
				t.Errorf("%s item %d does not round-trip: %s", profile.ID, i, clip(encoded))
			}
			decoded++
		}
	}
	research, analyze := readItem(t, "report-research.json"), DelegatedTaskProfile{ID: "analyze"}
	if report, err := DecodeDelegatedTaskReport(analyze, research); err != nil || branch(report) != "analyze" {
		t.Fatalf("a research report read by a profile with no report member: %v, %v", branch(report), err)
	}
	var byValue DelegatedTaskReport
	if err := json.Unmarshal(research, &byValue); err != nil || branch(byValue) != "analyze" {
		t.Fatalf("the value alone decoded a research report as %s (%v): the case the keyed decode is for has changed", branch(byValue), err)
	}
	if _, err := DecodeDelegatedTaskReport(analyze, readItem(t, "report-review.json")); err == nil {
		t.Fatal("a review report, whose findings carry a severity, decoded as an analyze report")
	}
	unknown := "summary"
	if _, err := DecodeDelegatedTaskReport(DelegatedTaskProfile{ID: "x", Report: &unknown}, research); err == nil {
		t.Fatal("a report kind outside the four decoded")
	}
	t.Logf("%d report items decoded by their profiles", decoded)
}

func readJSON(t *testing.T, path string, into any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatal(err)
	}
}

func readItem(t *testing.T, name string) json.RawMessage {
	t.Helper()
	var vector struct {
		Items []json.RawMessage `json:"items"`
	}
	readJSON(t, filepath.Join(vectorsDir, "delegation", name), &vector)
	return vector.Items[0]
}
