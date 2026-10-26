package wire

import "github.com/Judgment-Pack/judgment-pack-agent/go/failure"

// EnforceOutputCeiling pins the actual encoded request after all tier/model rewrites.
// Unknown protocols refuse: a nominal configuration is not proof of an encoded ceiling.
func EnforceOutputCeiling(family EndpointKind, raw []byte, ceiling int) ([]byte, error) {
	if ceiling == 0 {
		return raw, nil
	}
	refused := func() ([]byte, error) {
		return nil, failure.Authored("configuration", "This model route cannot prove its physical output ceiling.", "context")
	}
	if ceiling < 0 {
		return refused()
	}
	body, err := Parse(raw)
	if err != nil || !body.IsObject() {
		return refused()
	}
	clamp := func(value *Value, key string) {
		number, ok := value.Get(key).Number()
		if !ok || number <= 0 || number > float64(ceiling) {
			value.Set(key, Int(ceiling))
		}
	}
	switch family {
	case OpenAICompatible:
		clamp(body, "max_tokens")
		for _, key := range []string{"max_completion_tokens", "max_output_tokens"} {
			if body.Has(key) {
				clamp(body, key)
			}
		}
	case Anthropic:
		clamp(body, "max_tokens")
	case Gemini:
		config := body.Get("generationConfig")
		if !config.IsObject() {
			return refused()
		}
		clamp(config, "maxOutputTokens")
	default:
		return refused()
	}
	return body.JSON(), nil
}
