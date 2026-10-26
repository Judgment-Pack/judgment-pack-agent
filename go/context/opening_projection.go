package runcontext

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync/atomic"
	"unicode/utf8"

	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
)

// OpeningProjection permits only prefixes of explicitly named source-preview strings.
// All other JSON members remain package-owned copies of the original complete envelope.
type OpeningProjection struct {
	PreviewPaths []string
	Project      func(context.Context, OpeningProjectionInput) (map[string]string, error)
}
type OpeningProjectionInput struct {
	Input json.RawMessage
	Fit   func(map[string]string) (OpeningFit, error)
}
type OpeningFit struct {
	Bytes, Allowance int
	Fits             bool
}

// strictProjectionDocument refuses ambiguous duplicate members and bounds structural work.
func strictProjectionDocument(raw []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	nodes := 0
	var visit func(int) bool
	visit = func(depth int) bool {
		nodes++
		if depth > 64 || nodes > 65536 {
			return false
		}
		token, err := decoder.Token()
		if err != nil {
			return false
		}
		if delim, ok := token.(json.Delim); ok {
			switch delim {
			case '{':
				seen := map[string]bool{}
				for decoder.More() {
					key, err := decoder.Token()
					if err != nil {
						return false
					}
					name, ok := key.(string)
					if !ok || seen[name] {
						return false
					}
					seen[name] = true
					if !visit(depth + 1) {
						return false
					}
				}
				end, err := decoder.Token()
				return err == nil && end == json.Delim('}')
			case '[':
				for decoder.More() {
					if !visit(depth + 1) {
						return false
					}
				}
				end, err := decoder.Token()
				return err == nil && end == json.Delim(']')
			default:
				return false
			}
		}
		return true
	}
	return visit(0)
}

func projectionPath(path string) ([]string, error) {
	if path == "" || len(path) > 512 || !strings.HasPrefix(path, "/") {
		return nil, ErrContextBudget
	}
	parts := strings.Split(path[1:], "/")
	for i, part := range parts {
		var out strings.Builder
		for j := 0; j < len(part); j++ {
			if part[j] != '~' {
				out.WriteByte(part[j])
				continue
			}
			j++
			if j >= len(part) {
				return nil, ErrContextBudget
			}
			switch part[j] {
			case '0':
				out.WriteByte('~')
			case '1':
				out.WriteByte('/')
			default:
				return nil, ErrContextBudget
			}
		}
		parts[i] = out.String()
	}
	return parts, nil
}
func replacePreview(raw json.RawMessage, path []string, replacement *string) (json.RawMessage, error) {
	if len(path) == 0 {
		var original string
		if json.Unmarshal(raw, &original) != nil {
			return nil, ErrContextBudget
		}
		if replacement == nil {
			return raw, nil
		}
		if !utf8.ValidString(*replacement) || !strings.HasPrefix(original, *replacement) {
			return nil, ErrContextBudget
		}
		return json.Marshal(*replacement)
	}
	if len(raw) == 0 {
		return nil, ErrContextBudget
	}
	switch bytes.TrimSpace(raw)[0] {
	case '{':
		var value map[string]json.RawMessage
		if json.Unmarshal(raw, &value) != nil {
			return nil, ErrContextBudget
		}
		child, ok := value[path[0]]
		if !ok {
			return nil, ErrContextBudget
		}
		updated, err := replacePreview(child, path[1:], replacement)
		if err != nil {
			return nil, err
		}
		if replacement == nil {
			return raw, nil
		}
		value[path[0]] = updated
		return json.Marshal(value)
	case '[':
		var value []json.RawMessage
		if json.Unmarshal(raw, &value) != nil {
			return nil, ErrContextBudget
		}
		index, err := strconv.Atoi(path[0])
		if err != nil || index < 0 || index >= len(value) || strconv.Itoa(index) != path[0] {
			return nil, ErrContextBudget
		}
		updated, err := replacePreview(value[index], path[1:], replacement)
		if err != nil {
			return nil, err
		}
		if replacement == nil {
			return raw, nil
		}
		value[index] = updated
		return json.Marshal(value)
	}
	return nil, ErrContextBudget
}

// OpeningProjected runs before generic previews. A configured structured opening is either
// complete JSON within canonical framing/allowance or a context refusal, never an excerpt.
func (c *SessionContext) OpeningProjected(ctx context.Context, text, instructions string, tools *Value, current *CurrentRequest, projection *OpeningProjection) ([]string, error) {
	if projection == nil {
		return c.Opening(text, instructions, tools, current)
	}
	if projection.Project == nil || len(projection.PreviewPaths) > 64 || len(text) > StorageRecordBytes || !utf8.ValidString(text) || !json.Valid([]byte(text)) || !strictProjectionDocument([]byte(text)) || !strings.HasPrefix(strings.TrimSpace(text), "{") {
		return nil, ErrContextBudget
	}
	allowance, err := c.allowance(instructions, tools)
	if err != nil {
		return nil, err
	}
	original := json.RawMessage(text)
	paths := map[string][]string{}
	for _, name := range projection.PreviewPaths {
		if _, duplicate := paths[name]; duplicate {
			return nil, ErrContextBudget
		}
		path, err := projectionPath(name)
		if err != nil {
			return nil, err
		}
		if _, err = replacePreview(original, path, nil); err != nil {
			return nil, err
		}
		for _, prior := range paths {
			limit := len(prior)
			if len(path) < limit {
				limit = len(path)
			}
			prefix := true
			for i := 0; i < limit; i++ {
				if prior[i] != path[i] {
					prefix = false
					break
				}
			}
			if prefix {
				return nil, ErrContextBudget
			}
		}
		paths[name] = path
	}
	build := func(replacements map[string]string) ([]string, error) {
		if len(replacements) > len(paths) {
			return nil, ErrContextBudget
		}
		candidate := bytes.Clone(original)
		// Stable configured order keeps repeated fit calls identical.
		for _, name := range projection.PreviewPaths {
			if replacement, ok := replacements[name]; ok {
				var err error
				candidate, err = replacePreview(candidate, paths[name], &replacement)
				if err != nil {
					return nil, err
				}
			}
		}
		for name := range replacements {
			if _, ok := paths[name]; !ok {
				return nil, ErrContextBudget
			}
		}
		parts := []string{string(candidate)}
		if c.objective != "" {
			parts = append([]string{c.objective}, parts...)
		}
		return parts, nil
	}
	measure := func(parts []string) int {
		if len(parts) == 1 {
			return userMessageBytes(parts[0])
		}
		held := make([]Part, 0, len(parts))
		for _, part := range parts {
			held = append(held, TextPart(part))
		}
		return UTF8Bytes(MessagesValue([]Message{PartsMessage("user", held...)}).Text())
	}
	var probes atomic.Int32
	var live atomic.Bool
	live.Store(true)
	fit := func(replacements map[string]string) (OpeningFit, error) {
		if !live.Load() || ctx.Err() != nil {
			return OpeningFit{}, ErrContextBudget
		}
		if probes.Add(1) > 32 {
			return OpeningFit{}, ErrContextBudget
		}
		parts, err := build(replacements)
		if err != nil {
			return OpeningFit{}, err
		}
		size := measure(parts)
		return OpeningFit{Bytes: size, Allowance: allowance, Fits: size <= allowance}, nil
	}
	if err = ctx.Err(); err != nil {
		return nil, failure.Cancelled
	}
	replacements, err := projection.Project(ctx, OpeningProjectionInput{Input: bytes.Clone(original), Fit: fit})
	live.Store(false)
	if probes.Load() > 32 {
		return nil, ErrContextBudget
	}
	if err != nil {
		return nil, ErrContextBudget
	}
	if ctx.Err() != nil {
		return nil, failure.Cancelled
	}
	parts, err := build(replacements)
	if err != nil || measure(parts) > allowance {
		return nil, ErrContextBudget
	}
	index := c.indexWithin(allowance, allowance-measure(parts))
	if index != "" {
		parts = append(parts, index)
	}
	return parts, nil
}

// CheckPhysicalRequest applies the canonical input budget to the complete encoded envelope.
// Output policy and its reserve stay unchanged when a smaller physical output ceiling is used.
func (c *SessionContext) CheckPhysicalRequest(body []byte) error {
	if c.Execution != nil {
		if err := c.Execution.requestCapFailure(); err != nil {
			return err
		}
	}
	if len(body) > InputBytes(*c.Policy) {
		return ErrContextBudget
	}
	return nil
}
