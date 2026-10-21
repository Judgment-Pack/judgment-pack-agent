package wire

import (
	"encoding/json"
	"os"
	"testing"
)

func TestProviderTierOverride(t *testing.T) {
	raw, err := os.ReadFile("../../../contract/vectors/providers/tier-override.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct{ Items []json.RawMessage }
	if err = json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	for i, family := range []EndpointKind{Anthropic, Gemini} {
		override, err := Parse(v.Items[i])
		if err != nil {
			t.Fatal(err)
		}
		caps, err := CapabilitiesFor(family, "review", nil, override)
		if err != nil {
			t.Fatal(err)
		}
		slot := OpenThinking("high", family, &caps.Reasoning, caps.Tiers)
		members := slot.Members()
		if family == Anthropic {
			if members.Get("output_config").Get("effort").Text() != "high" {
				t.Fatal("tier map ignored")
			}
		} else {
			n, _ := members.Get("thinkingConfig").Get("thinkingBudget").Number()
			if n != 4096 {
				t.Fatal("tier map ignored")
			}
		}
	}
}
