package runcontext

// Gateway snapshot canonicalization is separate from Agent record canonicalization.
import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const DescriptorSnapshotBytes = 320 << 10

// DescriptorTool keeps the original schema text opaque at the snapshot boundary.
type DescriptorTool struct {
	Description     *string `json:"description,omitempty"`
	InputSchemaText *string `json:"inputSchemaText,omitempty"`
}
type DescriptorServer struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}
type DescriptorSnapshot struct {
	Binding    string                    `json:"binding"`
	CapturedAt string                    `json:"capturedAt"`
	Endpoint   *string                   `json:"endpoint,omitempty"`
	Platform   string                    `json:"platform"`
	Policy     int                       `json:"policy"`
	Server     *DescriptorServer         `json:"server,omitempty"`
	Tools      map[string]DescriptorTool `json:"tools"`
}

func descriptorError() error {
	return errors.New("descriptor snapshot: malformed or noncanonical snapshot")
}

// descriptorJSON refuses invalid UTF-8 and lone surrogate escapes before encoding/json
// can replace them. Its token reader preserves duplicate names and raw number spellings.
func descriptorJSON(data []byte, integers bool) (any, error) {
	if !utf8.Valid(data) {
		return nil, descriptorError()
	}
	for at := 0; at < len(data); at++ {
		if data[at] != '"' {
			continue
		}
		at++
		for at < len(data) && data[at] != '"' {
			if data[at] == '\\' {
				at++
				if at >= len(data) {
					return nil, descriptorError()
				}
				if data[at] == 'u' {
					code, err := descriptorEscape(data, at)
					if err != nil {
						return nil, err
					}
					at += 4
					if code >= 0xd800 && code <= 0xdbff {
						if at+2 >= len(data) || data[at+1] != '\\' || data[at+2] != 'u' {
							return nil, descriptorError()
						}
						low, err := descriptorEscape(data, at+2)
						if err != nil || low < 0xdc00 || low > 0xdfff {
							return nil, descriptorError()
						}
						at += 6
					} else if code >= 0xdc00 && code <= 0xdfff {
						return nil, descriptorError()
					}
				}
			}
			at++
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var read func(int) (any, error)
	read = func(depth int) (any, error) {
		if depth > 128 {
			return nil, descriptorError()
		}
		token, err := decoder.Token()
		if err != nil {
			return nil, descriptorError()
		}
		switch value := token.(type) {
		case json.Delim:
			switch value {
			case '{':
				object := map[string]any{}
				for decoder.More() {
					token, err := decoder.Token()
					key, ok := token.(string)
					if err != nil || !ok {
						return nil, descriptorError()
					}
					if _, duplicate := object[key]; duplicate {
						return nil, descriptorError()
					}
					member, err := read(depth + 1)
					if err != nil {
						return nil, err
					}
					object[key] = member
				}
				end, err := decoder.Token()
				if err != nil || end != json.Delim('}') {
					return nil, descriptorError()
				}
				return object, nil
			case '[':
				list := []any{}
				for decoder.More() {
					member, err := read(depth + 1)
					if err != nil {
						return nil, err
					}
					list = append(list, member)
				}
				end, err := decoder.Token()
				if err != nil || end != json.Delim(']') {
					return nil, descriptorError()
				}
				return list, nil
			}
		case json.Number:
			if integers {
				integer, err := strconv.ParseInt(string(value), 10, 64)
				if err != nil || integer < -9007199254740991 || integer > 9007199254740991 {
					return nil, descriptorError()
				}
				return integer, nil
			}
			if _, err := strconv.ParseFloat(string(value), 64); err != nil {
				return nil, descriptorError()
			}
			return value, nil
		case nil, bool, string:
			return value, nil
		}
		return nil, descriptorError()
	}
	value, err := read(0)
	if err != nil {
		return nil, err
	}
	if _, err = decoder.Token(); err != io.EOF {
		return nil, descriptorError()
	}
	return value, nil
}
func descriptorEscape(data []byte, at int) (uint64, error) {
	if at+5 > len(data) {
		return 0, descriptorError()
	}
	v, err := strconv.ParseUint(string(data[at+1:at+5]), 16, 16)
	if err != nil {
		return 0, descriptorError()
	}
	return v, nil
}
func descriptorCanonical(v any) string {
	switch value := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, key := range keys {
			parts = append(parts, QuoteJSON(key)+":"+descriptorCanonical(value[key]))
		}
		return "{" + strings.Join(parts, ",") + "}"
	case []any:
		parts := make([]string, 0, len(value))
		for _, item := range value {
			parts = append(parts, descriptorCanonical(item))
		}
		return "[" + strings.Join(parts, ",") + "]"
	case string:
		return QuoteJSON(value)
	case int64:
		return strconv.FormatInt(value, 10)
	case bool:
		if value {
			return "true"
		}
		return "false"
	default:
		return "null"
	}
}
func descriptorMembers(v map[string]any, required, optional []string) bool {
	allowed := map[string]bool{}
	for _, name := range required {
		allowed[name] = true
		if _, present := v[name]; !present {
			return false
		}
	}
	for _, name := range optional {
		allowed[name] = true
	}
	for name := range v {
		if !allowed[name] {
			return false
		}
	}
	return true
}
func descriptorPlatform(s string) bool {
	if s == "" || strings.TrimSpace(s) != s || strings.ContainsAny(s, "/=\x00") {
		return false
	}
	for _, r := range s {
		if !descriptorGraphic(r) {
			return false
		}
	}
	return true
}

func descriptorGraphic(r rune) bool {
	index := sort.Search(len(descriptorGraphicRanges), func(i int) bool { return descriptorGraphicRanges[i][1] >= r })
	return index < len(descriptorGraphicRanges) && descriptorGraphicRanges[index][0] <= r
}

var descriptorBinding = regexp.MustCompile(`^[^/\\@]+@sha256:[0-9a-f]{64}$`)
var descriptorCapturedAt = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$`)
var descriptorHost = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
var descriptorPort = regexp.MustCompile(`^[1-9][0-9]*$`)

func descriptorEndpoint(s string) bool {
	if len(s) > 2048 || strings.ContainsAny(s, "?#") {
		return false
	}
	for _, c := range []byte(s) {
		if c < 0x21 || c > 0x7e {
			return false
		}
	}
	rest, secure := strings.CutPrefix(s, "https://")
	if !secure {
		var plain bool
		rest, plain = strings.CutPrefix(s, "http://")
		if !plain {
			return false
		}
	}
	authority, path, _ := strings.Cut(rest, "/")
	host, port, hasPort := strings.Cut(authority, ":")
	if strings.HasPrefix(authority, "[") {
		close := strings.IndexByte(authority, ']')
		if close < 0 {
			return false
		}
		host = authority[:close+1]
		tail := authority[close+1:]
		hasPort = tail != ""
		if hasPort {
			var ok bool
			port, ok = strings.CutPrefix(tail, ":")
			if !ok {
				return false
			}
		}
		address, err := netip.ParseAddr(host[1 : len(host)-1])
		if err != nil || !address.Is6() || address.Zone() != "" {
			return false
		}
	} else if !descriptorHost.MatchString(host) {
		return false
	}
	if hasPort {
		n, err := strconv.ParseUint(port, 10, 16)
		if !descriptorPort.MatchString(port) || err != nil || n == 0 {
			return false
		}
	}
	for i := 0; i < len(path); i++ {
		if path[i] == '%' {
			if i+2 >= len(path) {
				return false
			}
			if _, err := strconv.ParseUint(path[i+1:i+3], 16, 8); err != nil {
				return false
			}
		}
	}
	if secure {
		return true
	}
	if host == "[::1]" {
		return true
	}
	address, err := netip.ParseAddr(host)
	return err == nil && address.Is4() && address.As4()[0] == 127
}

// ReadDescriptorSnapshot requires the exact complete canonical UTF-8 bytes used by Gateway.
// inputSchemaText is deliberately not parsed here: its original text is part of the pin.
func ReadDescriptorSnapshot(data []byte) (DescriptorSnapshot, error) {
	if len(data) > DescriptorSnapshotBytes {
		return DescriptorSnapshot{}, descriptorError()
	}
	v, err := descriptorJSON(data, true)
	if err != nil || descriptorCanonical(v) != string(data) {
		return DescriptorSnapshot{}, descriptorError()
	}
	object, ok := v.(map[string]any)
	if !ok || !descriptorMembers(object, []string{"binding", "capturedAt", "platform", "policy", "tools"}, []string{"endpoint", "server"}) {
		return DescriptorSnapshot{}, descriptorError()
	}
	platform, pok := object["platform"].(string)
	binding, bok := object["binding"].(string)
	captured, cok := object["capturedAt"].(string)
	if !pok || !descriptorPlatform(platform) || !bok || !descriptorBinding.MatchString(binding) || strings.HasPrefix(binding, ".@") || strings.HasPrefix(binding, "..@") || !cok || !descriptorCapturedAt.MatchString(captured) || object["policy"] != int64(1) {
		return DescriptorSnapshot{}, descriptorError()
	}
	if _, err := time.Parse(time.RFC3339, captured); err != nil {
		return DescriptorSnapshot{}, descriptorError()
	}
	if endpoint, present := object["endpoint"]; present {
		value, ok := endpoint.(string)
		if !ok || !descriptorEndpoint(value) {
			return DescriptorSnapshot{}, descriptorError()
		}
	}
	if server, present := object["server"]; present {
		value, ok := server.(map[string]any)
		if !ok || !descriptorMembers(value, []string{"name", "version"}, nil) {
			return DescriptorSnapshot{}, descriptorError()
		}
		if _, ok := value["name"].(string); !ok {
			return DescriptorSnapshot{}, descriptorError()
		}
		if _, ok := value["version"].(string); !ok {
			return DescriptorSnapshot{}, descriptorError()
		}
	}
	tools, ok := object["tools"].(map[string]any)
	if !ok {
		return DescriptorSnapshot{}, descriptorError()
	}
	for _, raw := range tools {
		entry, ok := raw.(map[string]any)
		if !ok || len(entry) == 0 || !descriptorMembers(entry, nil, []string{"description", "inputSchemaText"}) {
			return DescriptorSnapshot{}, descriptorError()
		}
		for _, field := range entry {
			if _, ok := field.(string); !ok {
				return DescriptorSnapshot{}, descriptorError()
			}
		}
	}
	var snapshot DescriptorSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return DescriptorSnapshot{}, descriptorError()
	}
	return snapshot, nil
}

// DescriptorSnapshotDigest hashes the full validated original bytes, not a serving projection.
func DescriptorSnapshotDigest(data []byte) (string, error) {
	if _, err := ReadDescriptorSnapshot(data); err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

// descriptorSchema reads captured schema text without a last-wins duplicate-key interpretation.
func descriptorSchema(text string) (json.RawMessage, error) {
	if _, err := descriptorJSON([]byte(text), false); err != nil {
		return nil, fmt.Errorf("descriptor schema: invalid JSON")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(text), &object); err != nil || object == nil {
		return nil, fmt.Errorf("descriptor schema: not an object")
	}
	return json.RawMessage(text), nil
}
