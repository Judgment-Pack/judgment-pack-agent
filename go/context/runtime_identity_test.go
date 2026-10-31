package runcontext

// #246: a Runtime identity or read-cache key with a lone UTF-16 surrogate, the cases
// contract/vectors/chat/runtime-identity.json states (context/chat.test.ts holds the TypeScript to
// the same items).

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
)

// vectorString decodes one raw JSON string literal as JSON.parse does, except that a lone surrogate
// escape becomes the three bytes its code unit would take in UTF-8 (its WTF-8 form): the only way a
// Go string can carry the unit, and never valid UTF-8. encoding/json would read it as U+FFFD.
func vectorString(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	text := string(raw)
	if len(text) < 2 || text[0] != '"' || text[len(text)-1] != '"' {
		t.Fatalf("not a JSON string: %s", raw)
	}
	text = text[1 : len(text)-1]
	unit := func(at int) uint16 {
		if at+4 > len(text) {
			t.Fatalf("a short escape in %s", raw)
		}
		n, err := strconv.ParseUint(text[at:at+4], 16, 16)
		if err != nil {
			t.Fatal(err)
		}
		return uint16(n)
	}
	var out []byte
	for at := 0; at < len(text); at++ {
		if text[at] != '\\' {
			out = append(out, text[at])
			continue
		}
		at++
		switch text[at] {
		case 'u':
			u := unit(at + 1)
			at += 4
			if u >= 0xd800 && u <= 0xdbff && at+6 < len(text) && text[at+1] == '\\' && text[at+2] == 'u' {
				if low := unit(at + 3); low >= 0xdc00 && low <= 0xdfff {
					out = utf8.AppendRune(out, utf16.DecodeRune(rune(u), rune(low)))
					at += 6
					continue
				}
			}
			if u >= 0xd800 && u <= 0xdfff {
				out = append(out, 0xe0|byte(u>>12), 0x80|byte(u>>6&0x3f), 0x80|byte(u&0x3f))
				continue
			}
			out = utf8.AppendRune(out, rune(u))
		case 'b':
			out = append(out, '\b')
		case 'f':
			out = append(out, '\f')
		case 'n':
			out = append(out, '\n')
		case 'r':
			out = append(out, '\r')
		case 't':
			out = append(out, '\t')
		default:
			out = append(out, text[at])
		}
	}
	return string(out)
}

type identityVector struct {
	Refusal string `json:"refusal"`
	Items   []struct {
		Tool      string          `json:"tool"`
		Arguments json.RawMessage `json:"arguments"`
		Runtime   *struct {
			Identity json.RawMessage `json:"identity"`
			Version  json.RawMessage `json:"version"`
		} `json:"runtime"`
	} `json:"items"`
	Outcomes []struct {
		Label     string `json:"label"`
		Admission string `json:"admission"`
		Canonical string `json:"canonical"`
		SHA256    string `json:"sha256"`
		Key       string `json:"key"`
	} `json:"outcomes"`
}

func readIdentityVector(t *testing.T) identityVector {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repositoryRoot(t), "contract", "vectors", "chat", "runtime-identity.json"))
	if err != nil {
		t.Fatal(err)
	}
	var vector identityVector
	if err = json.Unmarshal(raw, &vector); err != nil {
		t.Fatal(err)
	}
	if len(vector.Items) != len(vector.Outcomes) || len(vector.Items) == 0 {
		t.Fatal("the vector's items and outcomes do not pair")
	}
	return vector
}

func TestTheVectorStringsKeepALoneSurrogate(t *testing.T) {
	// The reader itself, by hand: a pair is the scalar, a lone unit its three bytes, U+FFFD itself.
	for raw, want := range map[string]string{`"a😀"`: "a\U0001F600", `"a\ud800"`: "a\xed\xa0\x80", `"\udc00"`: "\xed\xb0\x80", `"�"`: "�", `"\ude00\ud83d"`: "\xed\xb8\x80\xed\xa0\xbd"} {
		if got := vectorString(t, json.RawMessage(raw)); got != want {
			t.Errorf("%s reads as %q", raw, got)
		}
	}
}

// Each item keys as its outcome states, or gets no key; Go's path is the read wrapper's: the raw
// arguments, then ReadCacheKeyOf.
func TestTheRuntimeIdentityVectorKeys(t *testing.T) {
	vector := readIdentityVector(t)
	if vector.Refusal != RuntimeIdentityRefused {
		t.Fatalf("the refusal is %q", vector.Refusal)
	}
	for at, item := range vector.Items {
		outcome := vector.Outcomes[at]
		args, err := ParseJSON(item.Arguments)
		if err != nil {
			t.Fatal(err)
		}
		var runtime *RuntimeIdentity
		if item.Runtime != nil {
			runtime = &RuntimeIdentity{Identity: vectorString(t, item.Runtime.Identity), Version: vectorString(t, item.Runtime.Version)}
		}
		key, ok := "", false
		if cacheableArguments(item.Arguments) {
			key, ok = ReadCacheKeyOf(item.Tool, args, runtime != nil, runtime)
		}
		if outcome.Canonical == "" {
			if outcome.Key != "none" || ok {
				t.Errorf("%s: keyed %s", outcome.Label, key)
			}
			continue
		}
		if held := CanonicalJSON(ReadCacheKey{Tool: item.Tool, Arguments: args, Runtime: runtime}.Value()); held != outcome.Canonical {
			t.Errorf("%s: canonicalizes to %s", outcome.Label, held)
		}
		if !ok || key != outcome.SHA256 {
			t.Errorf("%s: keys to %s", outcome.Label, key)
		}
	}
}

// A Runtime whose identity or version holds a lone surrogate is refused at the session's opening,
// by name, before anything is sent; every other vector Runtime is admitted, and admitted as a copy.
func TestTheRuntimeIdentityVectorAdmission(t *testing.T) {
	vector := readIdentityVector(t)
	for at, item := range vector.Items {
		outcome := vector.Outcomes[at]
		if item.Runtime == nil {
			continue
		}
		session := modelSession()
		calls := 0
		session.CallTool = func(context.Context, string, json.RawMessage) (ToolResult, error) {
			calls++
			return ResultOf(nil, nil), nil
		}
		runtime := &RuntimeIdentity{Identity: vectorString(t, item.Runtime.Identity), Version: vectorString(t, item.Runtime.Version)}
		session.Context = &Input{CurrentRequest: &CurrentRequest{Message: "Write it.", Input: "Write it."}, Chat: NewChatContext("identity"), Runtime: runtime}
		run, err := ContextualSession(context.Background(), session)
		if outcome.Admission == "refused" {
			var held *failure.Error
			if !errors.As(err, &held) || held.Failure.Code != "configuration" || held.Failure.Detail == nil || *held.Failure.Detail != RuntimeIdentityRefused || run != nil || calls != 0 {
				t.Errorf("%s: answered %v", outcome.Label, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", outcome.Label, err)
		}
		admitted := run.Session.Context.Runtime
		runtime.Version = "changed after admission"
		if admitted == runtime || admitted.Version == runtime.Version {
			t.Errorf("%s: the admitted Runtime is the host's own object", outcome.Label)
		}
		run.Context.Close()
	}
}

// The read wrapper keeps no read whose raw arguments hold a lone surrogate escape, in the chat's
// cache or the run's: ParseJSON reads \ud800 and \ud801 both as U+FFFD, so one would answer for the
// other. Scalar arguments, U+FFFD written out among them, are served from the cache as before.
func TestTheReadWrapperKeepsNoArgumentsWithALoneSurrogate(t *testing.T) {
	for _, held := range []struct {
		name string
		kept *KeptRead
	}{{"chat cache", &KeptRead{}}, {"run cache (a Runtime-bound read with no Runtime named)", &KeptRead{Runtime: true}}} {
		t.Run(held.name, func(t *testing.T) {
			session := modelSession()
			session.Context = &Input{CurrentRequest: &CurrentRequest{Message: "Read it.", Input: "Read it."}, Chat: NewChatContext("lone-surrogate")}
			run := openRun(t, session)
			defer run.Context.Close()
			seen := map[string]int{}
			read := run.Context.Execution.WrapRead("get_schema", func(_ context.Context, raw json.RawMessage) (ToolResult, error) {
				seen[string(raw)]++
				return ResultOf([]Content{TextContent("schema for " + string(raw))}, nil), nil
			}, &ToolContextPolicy{Immutable: true}, true, held.kept)
			calls := []string{`{"spec_version":"0.3.0-\ud800"}`, `{"spec_version":"0.3.0-\ud801"}`, `{"spec_version":"0.3.0-\ud800"}`, `{"spec_version":"0.3.0-�"}`, `{"spec_version":"0.3.0-�"}`}
			for _, call := range calls {
				result, err := read(context.Background(), json.RawMessage(call))
				if err != nil || result.Content[0].Text() != "schema for "+call && !(call == calls[4] && result.Content[0].Text() == "schema for "+calls[3]) {
					t.Fatalf("%s answered %v %v", call, result.Content, err)
				}
			}
			if seen[calls[0]] != 2 || seen[calls[1]] != 1 || seen[calls[3]] != 1 {
				t.Fatalf("executions %v", seen)
			}
		})
	}
}
