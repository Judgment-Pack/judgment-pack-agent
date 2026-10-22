package eino

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
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
		})
	}
}
