package wire

import (
	"encoding/json"
	"testing"
)

func TestPhysicalOutputCeilingCannotBeRaisedByTier(t *testing.T) {
	for _, family := range []EndpointKind{OpenAICompatible, Anthropic, Gemini} {
		t.Run(string(family), func(t *testing.T) {
			request := oneRequest(family, Obj(F("max_tokens", Int(12096))))
			request.OutputTokenCeiling = 1
			body := EncodeRequest(request).Body
			var value map[string]json.RawMessage
			if err := json.Unmarshal(body, &value); err != nil {
				t.Fatal(err)
			}
			key := "max_tokens"
			if family == Gemini {
				_ = json.Unmarshal(value["generationConfig"], &value)
				key = "maxOutputTokens"
			}
			if string(value[key]) != "1" {
				t.Fatalf("physical output ceiling raised: %s", body)
			}
		})
	}
}

func TestPhysicalOutputCeilingPinsConflictsAndRefusesUnknownProtocol(t *testing.T) {
	raw := []byte(`{"model":"fixed","max_tokens":12000,"max_completion_tokens":8000,"max_output_tokens":"unbounded"}`)
	bounded, err := EnforceOutputCeiling(OpenAICompatible, raw, 1)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]json.RawMessage
	_ = json.Unmarshal(bounded, &body)
	for _, key := range []string{"max_tokens", "max_completion_tokens", "max_output_tokens"} {
		if string(body[key]) != "1" {
			t.Fatalf("conflicting physical output field survived: %s", bounded)
		}
	}
	same, err := EnforceOutputCeiling(OpenAICompatible, raw, 0)
	if err != nil || string(same) != string(raw) {
		t.Fatal("ordinary no-ceiling bytes changed")
	}
	if _, err = EnforceOutputCeiling(EndpointKind("opaque"), raw, 1); err == nil {
		t.Fatal("unknown protocol claimed an enforceable ceiling")
	}
}
