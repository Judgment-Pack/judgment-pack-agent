package wire

import (
	"slices"
	"sort"
	"testing"
)

// The capability record (capabilities.test.ts), held to the contract: the three families'
// defaults are the contract's vectors, the reader is closed as the schema is, and a run's record
// is the family's, then the host's, then the session's.

var capabilityVectors = map[EndpointKind]string{
	OpenAICompatible: "openai-compatible.json",
	Anthropic:        "anthropic.json",
	Gemini:           "gemini.json",
}

// TestFamilyRecordsAreTheVectors is "the three families' default records are the contract's
// vectors, member for member".
func TestFamilyRecordsAreTheVectors(t *testing.T) {
	for family, file := range capabilityVectors {
		items := vectorItems(t, "contract", "vectors", "capability", file)
		if len(items) != 1 {
			t.Fatalf("%s: the vector carries %d items", family, len(items))
		}
		equalJSON(t, string(family)+"'s default record", FamilyCapabilities(family), items[0])
	}
}

// TestFamilyRecordsSpeakTheirOwnWire is "each speak their own wire's reasoning".
func TestFamilyRecordsSpeakTheirOwnWire(t *testing.T) {
	for _, family := range EndpointKinds {
		record, err := ReadCapabilities(FamilyCapabilities(family))
		if err != nil {
			t.Fatalf("%s: %v", family, err)
		}
		if !slices.Contains(WireReasoning(family), record.Reasoning.Kind) {
			t.Errorf("%s does not speak %s", family, record.Reasoning.Kind)
		}
	}
}

// TestRecordNamesExactlyTheContractsMembers is "names exactly the members the contract states".
func TestRecordNamesExactlyTheContractsMembers(t *testing.T) {
	schema := vectorFile(t, "contract", "schemas", "capability.json")
	held := append([]string{}, CapabilityMembers...)
	sort.Strings(held)
	for _, names := range [][]string{
		schema.Get("properties").Keys(),
		schema.Get("$defs").Get("endpointCapabilities").Get("properties").Keys(),
	} {
		sorted := append([]string{}, names...)
		sort.Strings(sorted)
		if !slices.Equal(sorted, held) {
			t.Errorf("the record's members are %v; the contract's are %v", held, sorted)
		}
	}
}

// TestReadsEveryRecordTheContractCarries is "takes every record the contract carries".
func TestReadsEveryRecordTheContractCarries(t *testing.T) {
	for _, file := range []string{"openai-compatible.json", "anthropic.json", "gemini.json", "open-weight-local.json"} {
		item := vectorItems(t, "contract", "vectors", "capability", file)[0]
		record, err := ReadCapabilities(item.Clone())
		if err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		equalJSON(t, file+" read and written back", record.Value(), item)
	}
}

// TestTheRecordIsClosed is "is closed, and holds the window to the budget table's bounds".
func TestTheRecordIsClosed(t *testing.T) {
	refused := func(fields ...Field) string {
		base := FamilyCapabilities(OpenAICompatible)
		for _, field := range fields {
			base.Set(field.Name, field.Value)
		}
		_, err := ReadCapabilities(base)
		if err == nil {
			t.Fatalf("a record carrying %v was not refused", fields)
		}
		return err.Error()
	}
	contains(t, "an unknown member", refused(F("supportsTools", Bool(true))), `names "supportsTools"`)
	contains(t, "a window with no reserve", refused(F("contextWindowTokens", Int(8192))), "come together")
	contains(t, "a reserve over half the window", refused(F("contextWindowTokens", Int(8192)), F("outputTokens", Int(4097))), "half the window")
	contains(t, "a window under the bound", refused(F("contextWindowTokens", Int(2048)), F("outputTokens", Int(1024))), "4096 to 2000000")
	contains(t, "an unknown reasoning kind", refused(F("reasoning", Obj(F("kind", Str("chat-template"))))), "reasoning.kind must be one of")
	contains(t, "an unknown reasoning member", refused(F("reasoning", Obj(F("kind", Str("none")), F("budget", Int(1))))), `reasoning names "budget"`)
	contains(t, "an unknown reasoning field", refused(F("streamingQuirks", Obj(F("reasoningField", Str("thoughts")), F("wholeJsonAnswer", Bool(false))))), "reasoningField")
	contains(t, "an unknown schema shape", refused(F("toolSchemas", Str("openapi"))), "toolSchemas")
	contains(t, "an unknown placement", refused(F("statePlacement", Str("leading-user"))), "statePlacement")
	contains(t, "vision as a word", refused(F("vision", Str("yes"))), "vision must be true or false")
	// agent#17: the connection's capacity is not the record's; it is the connection's answer.
	contains(t, "the connection's capacity", refused(F("concurrentRequests", Int(3))), "concurrentRequests")
	_, err := ReadCapabilities(Null())
	if err == nil {
		t.Fatal("null was read as a record")
	}
	contains(t, "a record that is not an object", err.Error(), "not an object")
}

// TestTheRecordARunReads is "the record a run reads": the family's, then the host's, then the
// session's, member by member.
func TestTheRecordARunReads(t *testing.T) {
	family, model := OpenAICompatible, "a-model"
	none, err := CapabilitiesFor(family, model, nil, nil)
	if err != nil {
		t.Fatalf("with no host answer: %v", err)
	}
	equalJSON(t, "the family's default where the host knows nothing", none.Value(), FamilyCapabilities(family))

	var asked [][2]string
	host := func(askedFamily EndpointKind, askedModel string) *Value {
		asked = append(asked, [2]string{string(askedFamily), askedModel})
		return Obj(F("vision", Bool(true)), F("strictAlternation", Bool(true)))
	}
	layered, err := CapabilitiesFor(family, model, host, nil)
	if err != nil {
		t.Fatalf("with the host's answer: %v", err)
	}
	want := FamilyCapabilities(family)
	want.Set("vision", Bool(true))
	want.Set("strictAlternation", Bool(true))
	equalJSON(t, "the host's answer member by member over the family's", layered.Value(), want)
	if len(asked) != 1 || asked[0] != [2]string{string(family), model} {
		t.Errorf("the host was asked %v", asked)
	}

	session, err := CapabilitiesFor(family, model, host, Obj(F("vision", Bool(false)), F("nativeToolCalls", Bool(false))))
	if err != nil {
		t.Fatalf("with the session's own: %v", err)
	}
	want.Set("vision", Bool(false))
	want.Set("nativeToolCalls", Bool(false))
	equalJSON(t, "the session's own over the host's", session.Value(), want)
}

// TestTheRecordARunReadsIsRefused is "is refused where its reasoning is not spoken on the
// session's wire, or the host's answer is not an object".
func TestTheRecordARunReadsIsRefused(t *testing.T) {
	_, err := CapabilitiesFor(OpenAICompatible, "a-model", func(EndpointKind, string) *Value {
		return Obj(F("reasoning", Obj(F("kind", Str("gemini-thinking")))))
	}, nil)
	if err == nil {
		t.Fatal("a reasoning kind the wire does not speak was taken")
	}
	contains(t, "a kind the wire does not speak", err.Error(), "not spoken on the openai-compatible wire")

	_, err = CapabilitiesFor(OpenAICompatible, "a-model", func(EndpointKind, string) *Value { return nil }, nil)
	if err == nil {
		t.Fatal("an answer that is not an object was taken")
	}
	contains(t, "an answer that is not an object", err.Error(), "other than an object")
}
