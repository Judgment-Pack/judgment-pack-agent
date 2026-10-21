package runcontext

// The budget table and its bounds, held to the contract's vectors (the Go side of
// context/chat.test.ts's "the harness defaults" and of budget/*.json).

import (
	"encoding/json"
	"strings"
	"testing"
)

// policyValue is the policy as the vector's items write it.
func policyValue(t *testing.T, policy Policy) *Value {
	t.Helper()
	raw, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	value, err := ParseJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestTheDeskTableIsTheFileItWasWrittenFrom(t *testing.T) {
	// Go cannot embed a file outside its module, so the table is written in policy.go; this holds
	// it equal to contract/desk/policy.json, member for member.
	nested := readJSON(t, "contract/desk/policy.json")
	file := nested.Get("budget")
	file.Set("criticDefault", nested.Get("critic").Get("default"))
	file.Set("criticReasoningFloor", nested.Get("critic").Get("reasoningFloor"))
	table := policyValue(t, DeskPolicy)
	for _, name := range file.Keys() {
		if !equalJSON(file.Get(name), table.Get(name)) {
			t.Errorf("%s: the file says %s, the table %s", name, file.Get(name).Text(), table.Get(name).Text())
		}
	}
	for _, name := range table.Keys() {
		if file.Has(name) {
			continue
		}
		// The harness's own bounds are not in the Desk's table; at the zero value they are the
		// members the table does not carry.
		if !equalJSON(table.Get(name), Number(0)) && !equalJSON(table.Get(name), Bool(false)) {
			t.Errorf("%s is in the table but not in the file", name)
		}
	}
	// And the Desk's defaults are the first item of the Desk vector.
	if deskDefaults := vectorItems(t, "budget/desk-defaults.json")[0]; !equalJSON(deskDefaults, file) {
		t.Errorf("the desk-defaults vector is not the file")
	}
}

func TestTheHarnessDefaultsAreTheContractVector(t *testing.T) {
	item := vectorItems(t, "budget/harness-defaults.json")[0]
	held := policyValue(t, Defaults)
	if !equalJSON(item, held) {
		t.Errorf("the defaults are\n%s\nand the vector is\n%s", held.Text(), item.Text())
	}
}

func TestTheDeskLimitsAreTheContractVector(t *testing.T) {
	item := vectorItems(t, "budget/desk-limits.json")[0]
	for name, held := range map[string]int{
		"MAX_TURNS": MaxTurns, "REQUEST_INCREMENT": RequestIncrement,
		"MAX_REQUEST_ALLOWANCE": MaxRequestAllowance, "MAX_CHECKPOINT_BYTES": MaxCheckpointBytes,
	} {
		if int(item.Get(name).Num()) != held {
			t.Errorf("%s: the vector says %s, this package %d", name, item.Get(name).Text(), held)
		}
	}
}

func TestTheBriefAndMemoryAreBoundsNotReserves(t *testing.T) {
	// BriefBytes and MemoryBytes are the parts' bounds (budget/harness-defaults.json); the policy
	// takes no reserve for either, so ContextPolicy answers the table whatever the host says of
	// memory (the host's word is Input.Memory, which only decides whether MemoryFor is asked).
	policy, err := ContextPolicy(nil, "scripted-model", nil)
	if err != nil {
		t.Fatal(err)
	}
	if policy != Defaults || policy.BriefBytes != 8000 || policy.MemoryBytes != 4000 {
		t.Errorf("the policy is not the table with the parts' bounds: %+v", policy)
	}
}

func TestThePolicyLayersTheRecordAndTheProfile(t *testing.T) {
	// The table, then the capability record, then the profile's override for the model.
	policy, err := ContextPolicy(nil, "small", &ModelBudget{ContextWindowTokens: 16384, OutputTokens: 4096})
	if err != nil {
		t.Fatal(err)
	}
	if policy.ContextWindowTokens != 16384 || policy.OutputTokens != 4096 {
		t.Errorf("the record's window was not taken: %+v", policy)
	}
	configuration := &Configuration{Version: 1, Models: map[string]ModelBudget{"small": {ContextWindowTokens: 32768, OutputTokens: 8192}}}
	policy, err = ContextPolicy(configuration, "small", &ModelBudget{ContextWindowTokens: 16384, OutputTokens: 4096})
	if err != nil {
		t.Fatal(err)
	}
	if policy.ContextWindowTokens != 32768 || policy.OutputTokens != 8192 {
		t.Errorf("the profile's override was not the last word: %+v", policy)
	}
	// A model the profile does not name keeps the defaults (context.test.ts).
	policy, err = ContextPolicy(configuration, "different", nil)
	if err != nil {
		t.Fatal(err)
	}
	if policy.ContextWindowTokens != Defaults.ContextWindowTokens {
		t.Errorf("a model the profile does not name took %d", policy.ContextWindowTokens)
	}
}

func TestTheConfigurationIsReadClosed(t *testing.T) {
	for _, text := range []string{
		`{"version":2}`,
		`{"version":1,"unknown":1}`,
		`[]`,
		`{"version":1,"projectBrief":""}`,
		`{"version":1,"projectBrief":"../elsewhere"}`,
		`{"version":1,"projectBrief":"a\\b"}`,
		`{"version":1,"models":[]}`,
		`{"version":1,"models":{"small":{"contextWindowTokens":1024,"outputTokens":1024}}}`,
		`{"version":1,"models":{"small":{"contextWindowTokens":8192,"outputTokens":8192}}}`,
		`{"version":1,"models":{"small":{"contextWindowTokens":8192,"outputTokens":1024,"extra":1}}}`,
		`{"version":1,"models":{" small ":{"contextWindowTokens":8192,"outputTokens":1024}}}`,
	} {
		if _, err := ReadConfiguration(MustParseJSON(text)); err == nil {
			t.Errorf("%s was admitted", text)
		}
	}
	held, err := ReadConfiguration(MustParseJSON(`{"version":1,"projectBrief":"docs/brief.md","models":{"small":{"contextWindowTokens":8192,"outputTokens":1024}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if held.ProjectBrief != "docs/brief.md" || held.Models["small"].OutputTokens != 1024 {
		t.Errorf("the configuration read back as %+v", held)
	}
}

func TestInputBytesAndTheReadWindow(t *testing.T) {
	if InputBytes(Defaults) != (Defaults.ContextWindowTokens-Defaults.OutputTokens)*3 {
		t.Errorf("the input bytes are %d", InputBytes(Defaults))
	}
	wide := Defaults
	wide.ContextWindowTokens = 1000000
	if InputBytes(wide) != wide.MaxInputBytes {
		t.Errorf("a wide window is not held to maxInputBytes: %d", InputBytes(wide))
	}
	small := Defaults
	small.ContextWindowTokens, small.OutputTokens = 4096, 2048
	if InputBytes(small) != (4096-2048)*3 {
		t.Errorf("a small window gives %d", InputBytes(small))
	}
	tiny := Defaults
	tiny.ContextWindowTokens, tiny.OutputTokens = 4096, 4000
	if InputBytes(tiny) != 1024 {
		t.Errorf("the floor is %d", InputBytes(tiny))
	}
	// reader.test.ts: the read window is the result limit less the envelope, from the policy.
	if ReadWindowBytes(Defaults.ToolResultBytes) != Defaults.ToolResultBytes-ReadEnvelopeBytes {
		t.Error("the read window is not the result limit less the envelope")
	}
	if ReadWindowBytes(30000) != 30000-ReadEnvelopeBytes {
		t.Error("the read window does not follow the policy it was given")
	}
	if ReadWindowBytes(600) != 256 {
		t.Errorf("the read window's floor is %d", ReadWindowBytes(600))
	}
}

func TestPrefixAndEncodedPrefix(t *testing.T) {
	// prefix never cuts a character in half.
	if held := Prefix("é😀", 3); held != "é" {
		t.Errorf("prefix cut to %q", held)
	}
	if held := Prefix("short", 100); held != "short" {
		t.Errorf("prefix changed a text that fits: %q", held)
	}
	// reader.test.ts: encodedPrefix cuts text to the longest prefix whose JSON encoding fits,
	// escapes and pairs included. A lone surrogate cannot occur in a Go string, so the cases are
	// an astral character, a control character and the short escapes.
	text := strings.Repeat("😀é\u0001\"\\\n\t x", 400)
	for _, bytes := range []int{2, 3, 7, 64, 1000, 4097} {
		cut := EncodedPrefix(text, bytes)
		if UTF8Bytes(QuoteJSON(cut)) > bytes {
			t.Errorf("at %d the cut encodes to %d bytes", bytes, UTF8Bytes(QuoteJSON(cut)))
		}
		// The longest: one more code point would not fit.
		units := u16(text)
		at := U16Length(cut)
		if at < len(units) {
			_, width := codePointAt(units, at)
			next := U16Slice(text, 0, at+width)
			if UTF8Bytes(QuoteJSON(next)) <= bytes {
				t.Errorf("at %d one more code point would have fitted", bytes)
			}
		}
		// A surrogate pair is never split.
		if highSurrogateAt(cut, U16Length(cut)-1) {
			t.Errorf("at %d the cut ends in half a pair", bytes)
		}
	}
	if held := EncodedPrefix("short", 100); held != "short" {
		t.Errorf("encodedPrefix changed a text that fits: %q", held)
	}
}

func TestTheBudgetErrorIsTheCatalogueSentence(t *testing.T) {
	if ErrContextBudget.FailureCode() != "context-limit" {
		t.Error("the budget error does not name its class")
	}
	if !strings.Contains(ErrContextBudget.Error(), "exceeds its configured budget") {
		t.Errorf("the budget error says %q", ErrContextBudget.Error())
	}
}
