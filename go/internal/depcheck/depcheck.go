// Package depcheck holds the module's dependency graph to a deny list (ADR-0004): no package in
// it may be a provider's model client, which could hold a key of its own or dial a provider by its
// own transport. The relay capability is an adapter's only route to a model, and K1a in Go seals
// only http.DefaultTransport, so what the graph may not contain is checked here, by import path,
// for every package of the module and its tests (go list -deps -test ./...).
package depcheck

// Denied are the import path prefixes no package of the module may depend on: Eino's model
// components (eino-ext), the Gemini and Vertex AI clients (genai), ADK for Go's model package, and
// the provider SDKs a Go dependency could bring.
var Denied = []string{
	"github.com/cloudwego/eino-ext/components/model/",
	"github.com/cloudwego/eino-ext/libs/acl/openai",
	"google.golang.org/genai",
	"google.golang.org/adk/model",
	"github.com/google/generative-ai-go/",
	"cloud.google.com/go/vertexai/",
	"cloud.google.com/go/aiplatform/",
	"github.com/openai/openai-go",
	"github.com/sashabaranov/go-openai",
	"github.com/anthropics/anthropic-sdk-go",
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime",
	"github.com/ollama/ollama/api",
}

// Matches names the denied prefixes a package path falls under: the path itself, or a package
// below it.
func Matches(path string) []string {
	var hit []string
	for _, prefix := range Denied {
		trimmed := prefix
		if trimmed[len(trimmed)-1] == '/' {
			trimmed = trimmed[:len(trimmed)-1]
		}
		if path == trimmed || len(path) > len(trimmed) && path[:len(trimmed)] == trimmed && path[len(trimmed)] == '/' {
			hit = append(hit, prefix)
		}
	}
	return hit
}
