package historycapture

import (
	"encoding/json"
)

// NativeBindings is issued by the original native adapter from its private route allocation.
// The type is internal; caller data and retained objects cannot construct its private payload.
type NativeBindings struct {
	request string
	aliases map[string]string
}

func NewNativeBindings(request []byte, aliases map[string]string) (*NativeBindings, error) {
	var value struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if json.Unmarshal(request, &value) != nil || value.Tools == nil || len(value.Tools) != len(aliases) {
		return nil, ErrUnavailable
	}
	held := map[string]string{}
	names := map[string]bool{}
	for _, tool := range value.Tools {
		name, ok := aliases[tool.Name]
		if !ok || tool.Name == "" || name == "" || held[tool.Name] != "" || names[name] {
			return nil, ErrUnavailable
		}
		held[tool.Name] = name
		names[name] = true
	}
	return &NativeBindings{request: string(request), aliases: held}, nil
}
func (b *NativeBindings) ForRequest(request []byte) (map[string]string, error) {
	if b == nil || b.request != string(request) {
		return nil, ErrUnavailable
	}
	out := map[string]string{}
	for alias, name := range b.aliases {
		out[alias] = name
	}
	return out, nil
}
