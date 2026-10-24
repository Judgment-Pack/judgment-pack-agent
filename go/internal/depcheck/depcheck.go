// Package depcheck holds the module's dependency graph to an allow-list (ADR-0004, as amended,
// agent#56): every package of the module and its tests (go list -deps -test ./...) is in the
// standard library, in the module itself, or in a module on Allowed at the version Allowed names.
// A package of a module not on the list fails, whatever it is called, so a provider's model client
// (which could hold a key of its own or dial a provider by its own transport) cannot come in under
// a name nobody listed; a new module is a change to this list, reviewed. The relay capability is an
// adapter's only route to a model, and K1a in Go seals only http.DefaultTransport.
//
// Denied stays as a second check by import path: the provider clients by name, so a module the
// list admits cannot carry one of them in a package of its own.
package depcheck

// Module is the module this package checks.
const Module = "github.com/Judgment-Pack/judgment-pack-agent/go"

// Allowed are the modules the module's packages and their tests may depend on, each at its exact
// version: Eino (ADR-0004, decision 1) and the modules its packages bring.
var Allowed = map[string]string{
	"github.com/cloudwego/eino":             "v0.9.21",
	"github.com/bahlo/generic-list-go":      "v0.2.0",
	"github.com/buger/jsonparser":           "v1.1.2",
	"github.com/bytedance/gopkg":            "v0.1.3",
	"github.com/bytedance/sonic":            "v1.15.0",
	"github.com/bytedance/sonic/loader":     "v0.5.0",
	"github.com/cloudwego/base64x":          "v0.1.6",
	"github.com/dustin/go-humanize":         "v1.0.1",
	"github.com/eino-contrib/jsonschema":    "v1.0.3",
	"github.com/google/uuid":                "v1.6.0",
	"github.com/goph/emperror":              "v0.17.2",
	"github.com/json-iterator/go":           "v1.1.12",
	"github.com/klauspost/cpuid/v2":         "v2.2.9",
	"github.com/mailru/easyjson":            "v0.7.7",
	"github.com/modern-go/concurrent":       "v0.0.0-20180306012644-bacd9c7ef1dd",
	"github.com/modern-go/reflect2":         "v1.0.2",
	"github.com/nikolalohinski/gonja":       "v1.5.3",
	"github.com/pelletier/go-toml/v2":       "v2.0.9",
	"github.com/pkg/errors":                 "v0.9.1",
	"github.com/sirupsen/logrus":            "v1.9.3",
	"github.com/slongfield/pyfmt":           "v0.0.0-20220222012616-ea85ff4c361f",
	"github.com/twitchyliquid64/golang-asm": "v0.15.1",
	"github.com/wk8/go-ordered-map/v2":      "v2.1.8",
	"github.com/yargevad/filepathx":         "v1.0.0",
	"golang.org/x/arch":                     "v0.11.0",
	"golang.org/x/exp":                      "v0.0.0-20230713183714-613f0c0eb8a1",
	"golang.org/x/sys":                      "v0.48.0",
	"gopkg.in/yaml.v3":                      "v3.0.1",
}

// Admitted says why a package of a module at a version is not admitted, or "" where it is: the
// standard library (module "") and the module itself are; any other module must be on Allowed at
// its version.
func Admitted(module, version string) string {
	if module == "" || module == Module {
		return ""
	}
	want, ok := Allowed[module]
	if !ok {
		return "the module " + module + " is not on the allow-list"
	}
	if version != want {
		return "the module " + module + " is at " + version + ", not the allowed " + want
	}
	return ""
}

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
