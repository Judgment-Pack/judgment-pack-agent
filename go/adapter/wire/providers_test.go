package wire

import (
	"encoding/json"
	"fmt"
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
		slot := OpenThinking("high", family, &caps.Reasoning, caps.Tiers, caps.FallbackTiers)
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

func TestFallbackTierMaps(t *testing.T) {
	raw, err := os.ReadFile("../../../contract/vectors/providers/fallback-tiers.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Items   []json.RawMessage
		Refused json.RawMessage
		Error   string
		Cases   []struct {
			Record  int
			Tier    ThinkingTier
			Members json.RawMessage
		}
	}
	if err = json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	for _, c := range v.Cases {
		t.Run(fmt.Sprintf("%d/%s", c.Record, c.Tier), func(t *testing.T) {
			override, err := Parse(v.Items[c.Record])
			if err != nil {
				t.Fatal(err)
			}
			family := EndpointKind(override.Get("family").Text())
			if family == "openai" {
				family = OpenAICompatible
			}
			caps, err := CapabilitiesFor(family, "test", nil, override)
			if err != nil {
				t.Fatal(err)
			}
			slot := OpenThinking(c.Tier, family, &caps.Reasoning, caps.Tiers, caps.FallbackTiers)
			member := "thinking"
			if family == Gemini {
				member = "thinkingBudget"
			}
			if slot.Refused(400, member).Kind != RefusalRetry {
				t.Fatal("no fallback")
			}
			expected, err := Parse(c.Members)
			if err != nil {
				t.Fatal(err)
			}
			equalJSON(t, "fallback tier setting", slot.Members(), expected)
		})
	}
	refused, err := Parse(v.Refused)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = CapabilitiesFor(Anthropic, "test", nil, refused); err == nil || err.Error() != v.Error {
		t.Fatalf("non-integer fallback tier: %v, want %q", err, v.Error)
	}
}

func TestReasoningOmissionRequestVectors(t *testing.T) {
	raw, err := os.ReadFile("../../../contract/vectors/providers/reasoning-omission.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Items []json.RawMessage
		Cases []struct {
			Name   string
			Record int
			Phase  string
			Absent string
		}
	}
	if err = json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	for _, c := range v.Cases {
		if c.Phase == "fallback" {
			continue
		}
		t.Run(c.Name, func(t *testing.T) {
			override, parseErr := Parse(v.Items[c.Record])
			if parseErr != nil {
				t.Fatal(parseErr)
			}
			family := EndpointKind(override.Get("family").Text())
			caps, capabilityErr := CapabilitiesFor(family, "test", nil, override)
			if capabilityErr != nil {
				t.Fatal(capabilityErr)
			}
			slot := OpenThinking("", family, &caps.Reasoning, caps.Tiers, caps.FallbackTiers)
			if slot.Members() != nil || slot.Opening() != nil || slot.TurnEnded(true) != nil {
				t.Fatal("an omitted preference produced thinking state")
			}
			body, parseErr := Parse(EncodeRequest(oneRequest(family, slot.Members())).Body)
			if parseErr != nil {
				t.Fatal(parseErr)
			}
			container := body
			if family == Gemini {
				container = body.Get("generationConfig")
			}
			if container.Get(c.Absent) != nil {
				t.Fatalf("%s was present in %s", c.Absent, body.JSON())
			}
		})
	}
}
