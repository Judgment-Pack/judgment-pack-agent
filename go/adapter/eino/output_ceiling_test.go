package eino

import (
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/wire"
	"strings"
	"testing"
)

func TestCanonicalFallbackOutputCeilingIsAppliedAfterRewrite(t *testing.T) {
	for _, family := range []string{"openai-compatible", "anthropic", "gemini"} {
		t.Run(family, func(t *testing.T) {
			request := wire.ModelRequest{Body: []byte(`{"model":"before","max_tokens":1,"generationConfig":{"maxOutputTokens":1},"messages":[]}`)}
			_, rewritten, err := fallbackRequest(fallbackBinding(family, "after"), "on", "v1/messages", request)
			if err != nil {
				t.Fatal(err)
			}
			if family == "anthropic" && !strings.Contains(string(rewritten.Body), `"max_tokens":4096`) {
				t.Fatalf("fixture did not exercise fallback maximum rewrite: %s", rewritten.Body)
			}
			bounded, err := wire.EnforceOutputCeiling(wire.EndpointKind(family), rewritten.Body, 1)
			if err != nil {
				t.Fatal(err)
			}
			key := `"max_tokens":1`
			if family == "gemini" {
				key = `"maxOutputTokens":1`
			}
			if !strings.Contains(string(bounded), key) {
				t.Fatalf("canonical fallback raised final outbound ceiling: %s", bounded)
			}
		})
	}
}
