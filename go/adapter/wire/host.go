package wire

import (
	"regexp"
	"strconv"
	"strings"
)

// EndpointKind is the endpoint families a model connection speaks (EndpointKind in host.ts).
type EndpointKind string

// The three families the Desk's connections speak.
const (
	OpenAICompatible EndpointKind = "openai-compatible"
	Anthropic        EndpointKind = "anthropic"
	Gemini           EndpointKind = "gemini"
)

// EndpointKinds are the three families, in the order host.ts states them.
var EndpointKinds = []EndpointKind{OpenAICompatible, Anthropic, Gemini}

// ThinkingTier is the depths a run may ask a model to reason at (ThinkingTier in host.ts).
type ThinkingTier string

// The three tiers a person may select.
const (
	TierOff   ThinkingTier = "off"
	TierOn    ThinkingTier = "on"
	TierUltra ThinkingTier = "ultra"
)

// ThinkingTiers are the three tiers, in the order host.ts states them.
var ThinkingTiers = []ThinkingTier{TierOff, TierOn, TierUltra}

var sourceToken = regexp.MustCompile(`\{\{(\w+)\}\}`)

// SourceMessage is desk-authored text filled from its named values: {{name}} becomes values[name]
// as JavaScript's String writes it (sourceMessage in host.ts). A token the values do not name is
// left as it stands.
func SourceMessage(source string, values map[string]any) string {
	return sourceToken.ReplaceAllStringFunc(source, func(token string) string {
		name := strings.TrimSuffix(strings.TrimPrefix(token, "{{"), "}}")
		held, ok := values[name]
		if !ok {
			return token
		}
		return jsText(held)
	})
}

// jsText writes a value as JavaScript's String does, for the values the harness's own messages
// carry: a string and a number.
func jsText(value any) string {
	switch held := value.(type) {
	case string:
		return held
	case int:
		return strconv.Itoa(held)
	case float64:
		return jsNumber(held)
	case bool:
		return strconv.FormatBool(held)
	case *Value:
		return held.String()
	}
	return ""
}
