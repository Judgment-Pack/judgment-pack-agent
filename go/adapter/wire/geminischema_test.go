package wire

import (
	"slices"
	"sort"
	"testing"
)

// The removal list, keyword by keyword, and the two properties that make it a ruling rather than a
// convenience: it is closed, and it removes nothing else (geminiSchema.test.ts). The schema an
// engine may show the model is engines/contract.test.ts's.

// everyKey is every key in a value, at every depth, property names included.
func everyKey(value *Value, into map[string]bool) map[string]bool {
	if into == nil {
		into = map[string]bool{}
	}
	if value.Kind() == KindArray {
		for _, item := range value.Items() {
			everyKey(item, into)
		}
		return into
	}
	for _, field := range value.Members() {
		into[field.Name] = true
		everyKey(field.Value, into)
	}
	return into
}

// TestRemovesEachKeywordAtEveryDepth is "removes %s wherever it appears, at every depth".
func TestRemovesEachKeywordAtEveryDepth(t *testing.T) {
	for _, keyword := range GeminiSchemaRemovals {
		value := Str("x")
		if keyword == "additionalProperties" {
			value = Bool(false)
		}
		schema := Obj(
			F("type", Str("object")),
			F(keyword, value),
			F("properties", Obj(F("outer", Obj(
				F("type", Str("array")),
				F("items", Obj(F("type", Str("object")), F(keyword, value))),
			)))),
			F("$defs", Obj(F("named", Obj(F("type", Str("string")), F(keyword, Str("x")))))),
		)
		walked := WithoutUnsupportedKeywords(schema)
		if everyKey(walked, nil)[keyword] {
			t.Errorf("%s survived the walk", keyword)
		}
		// …and the schema it came from is untouched: this answers a new value rather than editing
		// the runtime's own.
		if !everyKey(schema, nil)[keyword] {
			t.Errorf("%s was taken out of the schema the runtime served", keyword)
		}
	}
}

// TestRemovesNothingElse is "removes nothing else at all, and keeps every value by identity where
// it changed nothing".
func TestRemovesNothingElse(t *testing.T) {
	untouched := Obj(
		F("type", Str("object")),
		F("description", Str("a description")),
		F("required", Arr(Str("a"))),
		F("properties", Obj(
			F("a", Obj(F("type", Str("string")), F("enum", Arr(Str("x"), Str("y"))), F("description", Str("a")))),
			F("b", Obj(F("type", Str("array")), F("items", Obj(F("type", Str("integer")))), F("minItems", Int(1)))),
		)),
		F("oneOf", Arr(Obj(F("type", Str("string"))), Obj(F("type", Str("number"))))),
		F("anyOf", Arr(Obj(F("type", Str("null"))))),
	)
	// Identity, not equality: a subtree with no removal in it is the value the runtime served.
	if WithoutUnsupportedKeywords(untouched) != untouched {
		t.Error("a schema with nothing to remove was copied")
	}
}

// TestKeepsOneOf is "keeps oneOf, because a union removed reads as 'anything at all'".
func TestKeepsOneOf(t *testing.T) {
	if slices.Contains(GeminiSchemaRemovals, "oneOf") {
		t.Fatal("oneOf is on the removal list")
	}
	schema := Obj(F("oneOf", Arr(Obj(F("type", Str("string"))), Obj(F("type", Str("number"))))))
	equalJSON(t, "a union", WithoutUnsupportedKeywords(schema), schema)
}

// TestLeavesAnAuthoredNameAlone is "leaves an authored name alone under %s, and still walks the
// schema it points at".
func TestLeavesAnAuthoredNameAlone(t *testing.T) {
	for _, nameMap := range []string{"properties", "$defs", "definitions", "dependentSchemas"} {
		named := Obj()
		for _, keyword := range GeminiSchemaRemovals {
			named.Set(keyword, Obj(F("type", Str("string")), F("additionalProperties", Bool(false)), F("const", Str("x"))))
		}
		walked := WithoutUnsupportedKeywords(Obj(F("type", Str("object")), F(nameMap, named)))
		held := walked.Get(nameMap)
		names := append([]string{}, held.Keys()...)
		sort.Strings(names)
		wanted := append([]string{}, GeminiSchemaRemovals...)
		sort.Strings(wanted)
		if !slices.Equal(names, wanted) {
			t.Errorf("%s: the authored names are %v", nameMap, names)
		}
		// …and the removable keywords inside each child schema are still gone.
		for _, name := range GeminiSchemaRemovals {
			child := held.Get(name)
			if child.Get("additionalProperties") != nil || child.Get("const") != nil {
				t.Errorf("%s/%s kept a removable keyword", nameMap, name)
			}
			equalText(t, nameMap+"/"+name+"'s type", child.Get("type").Text(), "string")
		}
	}
}

// TestRemovesPatternPropertiesWhole is "removes patternProperties whole, names and all, because it
// is on the list".
func TestRemovesPatternPropertiesWhole(t *testing.T) {
	schema := Obj(F("type", Str("object")), F("patternProperties", Obj(F("^const$", Obj(F("type", Str("string")))))))
	equalJSON(t, "a schema with pattern properties", WithoutUnsupportedKeywords(schema), Obj(F("type", Str("object"))))
	sent := KeywordsSent(Obj(F("patternProperties", Obj(F("const", Obj(F("type", Str("string"))))))))
	if !sent["patternProperties"] || sent["const"] {
		t.Errorf("a regular expression somebody wrote was read as a keyword: %v", sent)
	}
}

// TestKeepsARefResolvable is "keeps a $ref resolvable, which is the point of the rule".
func TestKeepsARefResolvable(t *testing.T) {
	walked := WithoutUnsupportedKeywords(Obj(
		F("type", Str("object")),
		F("definitions", Obj(F("const", Obj(F("type", Str("string")), F("additionalProperties", Bool(false)))))),
		F("properties", Obj(F("x", Obj(F("$ref", Str("#/definitions/const")))))),
	))
	if names := walked.Get("definitions").Keys(); !slices.Equal(names, []string{"const"}) {
		t.Errorf("the definitions are %v", names)
	}
	equalText(t, "the reference", walked.Get("properties").Get("x").Get("$ref").Text(), "#/definitions/const")
}

// TestAKeywordUnderANameMapIsAName is "reads a keyword under a name map as a name and not as a
// keyword sent", and "leaves a property actually named like a keyword alone".
func TestAKeywordUnderANameMapIsAName(t *testing.T) {
	sent := KeywordsSent(Obj(
		F("type", Str("object")),
		F("definitions", Obj(F("pattern", Obj(F("type", Str("string")))))),
		F("dependentRequired", Obj(F("pattern", Arr(Str("x"))))),
	))
	if !sent["definitions"] || !sent["dependentRequired"] || sent["pattern"] {
		t.Errorf("the keywords sent are %v", sent)
	}
	walked := WithoutUnsupportedKeywords(Obj(F("type", Str("object")), F("properties", Obj(
		F("const", Obj(F("type", Str("string")), F("additionalProperties", Bool(false)))),
		F("examples", Obj(F("type", Str("string")))),
	))))
	names := append([]string{}, walked.Get("properties").Keys()...)
	sort.Strings(names)
	if !slices.Equal(names, []string{"const", "examples"}) {
		t.Errorf("the authored properties are %v", names)
	}
	if walked.Get("properties").Get("const").Get("additionalProperties") != nil {
		t.Error("a property called const kept a removable keyword inside its own schema")
	}
	equalText(t, "its type", walked.Get("properties").Get("const").Get("type").Text(), "string")
}

// recordedTools are the runtime's own five, as tools/list served them in the recording.
func recordedTools(t *testing.T) []McpTool {
	t.Helper()
	var tools []McpTool
	for _, item := range vectorItems(t, "contract", "vectors", "tool-offer", "runtime-tools.json") {
		tools = append(tools, McpTool{
			Name:        item.Get("name").Text(),
			Description: item.Get("description").Text(),
			InputSchema: item.Get("inputSchema"),
		})
	}
	if len(tools) == 0 {
		t.Fatal("the recorded offer carries no tools")
	}
	return tools
}

// TestAppliesToTheGeminiFamilyAndNoOther is "applies to the gemini family and to no other, over
// the runtime's own five".
func TestAppliesToTheGeminiFamilyAndNoOther(t *testing.T) {
	for _, tool := range recordedTools(t) {
		if !everyKey(tool.InputSchema, nil)["additionalProperties"] {
			t.Errorf("%s: the runtime served a schema without additionalProperties", tool.Name)
		}
		narrowed, err := ServedSchemaFor(Gemini, tool)
		if err != nil {
			t.Fatalf("%s: %v", tool.Name, err)
		}
		if everyKey(narrowed, nil)["additionalProperties"] {
			t.Errorf("%s: the gemini family was shown additionalProperties", tool.Name)
		}
		for _, family := range []EndpointKind{OpenAICompatible, Anthropic} {
			shown, err := ServedSchemaFor(family, tool)
			if err != nil {
				t.Fatalf("%s on %s: %v", tool.Name, family, err)
			}
			if shown != tool.InputSchema {
				t.Errorf("%s on %s: the served schema was not the one that travelled", tool.Name, family)
			}
		}
		// This adapter shows what the deepagents adapter shows, so nothing is lost beyond the
		// desk's own declared ruling and no narrowing notice is owed.
		if lost := KeywordsLost(EngineID, Gemini, tool.InputSchema); len(lost) != 0 {
			t.Errorf("%s: %v lost beyond the desk's own removals", tool.Name, lost)
		}
	}
	if events := NarrowingEvents(EngineID, Gemini, recordedTools(t)); len(events) != 0 {
		t.Errorf("this engine owed %d narrowing notices", len(events))
	}
	// A tool the runtime served without a schema is not the notice pass's refusal to make.
	if events := NarrowingEvents(EngineID, Gemini, []McpTool{{Name: "a_new_tool"}}); len(events) != 0 {
		t.Error("the notice pass refused a tool with no schema")
	}
}

// TestTheSchemaAnEngineMayShow is engines/contract.test.ts's "is the served one, and a refusal
// where the runtime served none", on this family too.
func TestTheSchemaAnEngineMayShow(t *testing.T) {
	schema := Obj(F("type", Str("object")))
	served, err := ServedSchema(McpTool{Name: "validate", InputSchema: schema})
	if err != nil {
		t.Fatalf("a served schema: %v", err)
	}
	equalJSON(t, "the schema the runtime served", served, schema)
	_, err = ServedSchema(McpTool{Name: "a_new_tool"})
	if err == nil {
		t.Fatal("a tool with no schema was given one")
	}
	contains(t, "the refusal", err.Error(), "a_new_tool")
	contains(t, "the refusal", err.Error(), "without an input schema")
	if _, err := ServedSchemaFor(Gemini, McpTool{Name: "a_tool"}); err == nil {
		t.Fatal("a tool with no schema was given one on the gemini family")
	} else {
		contains(t, "the refusal on gemini", err.Error(), "will not write")
	}
	// Every schema this session put in front of the model, and only those.
	shown, err := SchemasShown(string(Gemini), []McpTool{{Name: "a", InputSchema: Obj(F("type", Str("object")), F("additionalProperties", Bool(false)))}})
	if err != nil {
		t.Fatalf("the schemas shown: %v", err)
	}
	equalJSON(t, "the schema shown on gemini", shown[0], Obj(F("type", Str("object"))))
}

// TestAKeywordTheListDoesNotName is "a keyword the list does not name, refused by an endpoint".
func TestAKeywordTheListDoesNotName(t *testing.T) {
	schemas := []*Value{Obj(F("type", Str("object")), F("properties", Obj(F("a", Obj(F("type", Str("string")))))), F("oneOf", Arr()))}
	equalText(t, "the keyword named", RefusedSchemaKeyword(400, `Invalid JSON payload: unknown name "oneOf"`, schemas), "oneOf")
	// The lesson the tier's own classifier learned: the prose alone is a sentence an endpoint
	// writes about anything at all.
	equalText(t, "a refusal naming no keyword", RefusedSchemaKeyword(400, "The document was too large", schemas), "")
	equalText(t, "a keyword the desk did not send", RefusedSchemaKeyword(400, `unknown name "patternProperties"`, schemas), "")
	equalText(t, "a status that is not 400", RefusedSchemaKeyword(500, `unknown name "oneOf"`, schemas), "")
	// The longest name wins: `properties` contains `type`.
	nested := []*Value{Obj(F("type", Str("object")), F("properties", Obj(F("a", Obj(F("type", Str("string")))))))}
	equalText(t, "the longest name", RefusedSchemaKeyword(400, `unknown name "properties"`, nested), "properties")
	// And never a property the author wrote.
	authored := []*Value{Obj(F("type", Str("object")), F("properties", Obj(F("rehearsal", Obj(F("type", Str("boolean")))))))}
	if KeywordsSent(authored[0])["rehearsal"] {
		t.Error("an authored property was read as a keyword sent")
	}
	equalText(t, "an authored name", RefusedSchemaKeyword(400, `unknown name "rehearsal"`, authored), "")
	// It says it was an error and not a strip, and names the closed list.
	said := RefusedSchemaSentence("oneOf")
	contains(t, "the keyword", said, `"oneOf"`)
	contains(t, "the list being closed", said, "closed")
	for _, keyword := range GeminiSchemaRemovals {
		contains(t, "the closed list", said, keyword)
	}
	contains(t, "what was written", said, "nothing was written")
}

// TestTheNarrowingNotice is narrowingNotice's own sentence, which an engine that does lose a
// keyword emits.
func TestTheNarrowingNotice(t *testing.T) {
	said := NarrowingNotice([]string{"const", "additionalProperties"})
	contains(t, "the keywords, sorted", said, "shown to the model without: additionalProperties, const.")
	contains(t, "what still holds the contract", said, "the ToolGate and the runtime hold the")
}
