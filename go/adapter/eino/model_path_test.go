package eino

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/wire"
)

func TestGeminiFallbackModelPathVectors(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "contract", "vectors", "failover", "gemini-model-path.json"))
	if err != nil {
		t.Fatal(err)
	}
	var vector struct {
		Items []struct {
			Model   string `json:"model"`
			Segment string `json:"segment"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &vector); err != nil {
		t.Fatal(err)
	}
	for _, item := range vector.Items {
		t.Run(item.Model, func(t *testing.T) {
			if got := encodePathSegment(item.Model); got != item.Segment {
				t.Fatalf("encoded segment %q, want %q", got, item.Segment)
			}
			suffix, _, err := fallbackRequest(
				fallbackBinding("gemini", item.Model),
				"off",
				"v1beta/models/primary:streamGenerateContent?alt=sse",
				wire.ModelRequest{Body: []byte(`{}`)},
			)
			if err != nil {
				t.Fatal(err)
			}
			want := "v1beta/models/" + item.Segment + ":streamGenerateContent?alt=sse"
			if suffix != want {
				t.Fatalf("fallback suffix %q, want %q", suffix, want)
			}
		})
	}
}

func TestFallbackReasoningOmissionVectors(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "contract", "vectors", "providers", "reasoning-omission.json"))
	if err != nil {
		t.Fatal(err)
	}
	var vector struct {
		Items []struct{ Family string }
		Cases []struct {
			Name   string
			Record int
			Phase  string
			Absent string
		}
	}
	if err := json.Unmarshal(raw, &vector); err != nil {
		t.Fatal(err)
	}
	for _, item := range vector.Cases {
		if item.Phase != "fallback" {
			continue
		}
		t.Run(item.Name, func(t *testing.T) {
			family := vector.Items[item.Record].Family
			if family == "openai" {
				family = "openai-compatible"
			}
			body := []byte(`{"model":"primary","reasoning_effort":"high","messages":[]}`)
			if family == "gemini" {
				body = []byte(`{"generationConfig":{"maxOutputTokens":4096,"thinkingConfig":{"thinkingBudget":0}},"contents":[]}`)
			}
			_, rebuilt, rebuildErr := fallbackRequest(fallbackBinding(family, "backup"), "", "v1beta/models/primary:generateContent", wire.ModelRequest{Body: body})
			if rebuildErr != nil {
				t.Fatal(rebuildErr)
			}
			var decoded map[string]any
			if err := json.Unmarshal(rebuilt.Body, &decoded); err != nil {
				t.Fatal(err)
			}
			container := decoded
			if family == "gemini" {
				container = decoded["generationConfig"].(map[string]any)
			}
			if _, present := container[item.Absent]; present {
				t.Fatalf("%s remained in %s", item.Absent, rebuilt.Body)
			}
		})
	}
}

func TestCriticReasoningOmissionVectors(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "contract", "vectors", "providers", "reasoning-omission.json"))
	if err != nil {
		t.Fatal(err)
	}
	var vector struct {
		Items []json.RawMessage
		Cases []struct {
			Name   string
			Record int
			Phase  string
		}
	}
	if err := json.Unmarshal(raw, &vector); err != nil {
		t.Fatal(err)
	}
	for _, item := range vector.Cases {
		if item.Phase != "critic" {
			continue
		}
		t.Run(item.Name, func(t *testing.T) {
			override, parseErr := wire.Parse(vector.Items[item.Record])
			if parseErr != nil {
				t.Fatal(parseErr)
			}
			family := wire.EndpointKind(override.Get("family").Text())
			if family == "openai" {
				family = wire.OpenAICompatible
			}
			capabilities, capabilityErr := wire.CapabilitiesFor(family, "critic", nil, override)
			if capabilityErr != nil {
				t.Fatal(capabilityErr)
			}
			if members := criticThinking("", family, capabilities).Members(); members != nil {
				t.Fatalf("omitted critic reasoning produced %s", members.JSON())
			}
		})
	}
}

func fallbackBinding(family, model string) adapter.RequestPolicyConnection {
	run := &adapter.Run{PolicyConnections: map[string]adapter.PolicyConnection{"fixture": {Wire: family, ModelName: model}}}
	return adapter.NewRequestSession(run, nil).Binding("fixture")
}
