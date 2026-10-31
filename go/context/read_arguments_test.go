package runcontext

// #271: a read's cached answer belongs to the bytes that were run. Both caches key from what ParseJSON
// reads and the tool is run with the raw arguments, so arguments that reading does not say exactly
// get no key: contract/vectors/chat/read-arguments.json states the cases (context/readKeys.test.ts
// holds the TypeScript to the same ones). go/adapter/eino/read_keys_test.go drives the same collisions
// through both adapters.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type readArgumentsOutcome struct {
	Canonical string `json:"canonical"`
	Sha256    string `json:"sha256"`
	Key       string `json:"key"`
}

type readArgumentsVector struct {
	Tool  string `json:"tool"`
	Cases []struct {
		Label string               `json:"label"`
		Text  string               `json:"text"`
		Go    readArgumentsOutcome `json:"go"`
	} `json:"cases"`
}

// Each case keys as the vector states, or gets no key, through the exported ReadCacheKeyOf alone. An
// ordinary object keeps the key it always had (contract/vectors/chat/cache-key.json holds those).
func TestTheReadArgumentsVectorKeys(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repositoryRoot(t), "contract", "vectors", "chat", "read-arguments.json"))
	if err != nil {
		t.Fatal(err)
	}
	var vector readArgumentsVector
	if err = json.Unmarshal(raw, &vector); err != nil {
		t.Fatal(err)
	}
	if len(vector.Cases) == 0 {
		t.Fatal("the vector has no cases")
	}
	for _, item := range vector.Cases {
		t.Run(item.Label, func(t *testing.T) {
			key, ok := ReadCacheKeyOf(vector.Tool, json.RawMessage(item.Text), false, nil)
			if item.Go.Key == "none" {
				if ok {
					t.Fatalf("%s keys as %s", item.Text, key)
				}
				return
			}
			if !ok || key != item.Go.Sha256 {
				t.Fatalf("%s keys as %s %v", item.Text, key, ok)
			}
			if got := ContextDigest(item.Go.Canonical); got != item.Go.Sha256 {
				t.Fatalf("the vector's digest of %s is not %s", item.Go.Canonical, got)
			}
		})
	}
}

// Two calls that are run differently and that ParseJSON reads alike, one row per way of reading them
// alike. The first two rows are the issue's.
var readArgumentCollisions = []struct{ name, first, second string }{
	{"an array reads as {}", `[1]`, `[2]`},
	{"a number out of range reads as null", `{"q":1e400}`, `{"q":2e400}`},
	{"an integer past 2^53 reads as a float64", `{"id":9007199254740993}`, `{"id":9007199254740992}`},
	{"a repeated name keeps the last value", `{"a":{"x":1},"a":{"y":2}}`, `{"a":{"y":2}}`},
	{"a repeated name, escaped, keeps the last value", `{"a":1,"a":2}`, `{"a":2}`},
	{"a repeated name in a nested object", `{"o":{"a":1,"a":2}}`, `{"o":{"a":2}}`},
	{"another spelling of a number", `{"q":1.0}`, `{"q":1}`},
	{"an exponent", `{"q":1e2}`, `{"q":100}`},
	{"negative zero", `{"q":-0}`, `{"q":0}`},
	{"underflow", `{"q":1e-400}`, `{"q":0}`},
	{"a number nested in an array", `{"a":[1e400]}`, `{"a":[2e400]}`},
	{"a number nested in an object", `{"a":{"b":1e400}}`, `{"a":{"b":2e400}}`},
	{"an array reads as an empty object's {}", `[1]`, `{}`},
	{"text that is not JSON reads as {}", `{bad`, `{worse`},
	{"data after the object reads as {}", `{"q":1} {"q":2}`, `{"q":1} {"q":3}`},
}

// readArgumentScopes are the caches a read can be served from: the run's alone, the chat's, and the
// run's on a chat where the read's key needs a Runtime the host has not named.
func readArgumentScopes() []struct {
	name string
	chat bool
	kept *KeptRead
} {
	return []struct {
		name string
		chat bool
		kept *KeptRead
	}{{"run cache", false, nil}, {"chat cache", true, &KeptRead{}}, {"run cache on a chat", true, &KeptRead{Runtime: true}}}
}

func readArgumentsRun(t *testing.T, chat bool) *Run {
	t.Helper()
	session := modelSession()
	session.Context = &Input{CurrentRequest: &CurrentRequest{Message: "Read it.", Input: "Read it."}}
	if chat {
		session.Context.Chat = NewChatContext("read-arguments")
	}
	return openRun(t, session)
}

func TestTheReadWrapperServesNoCallAnotherCallsAnswer(t *testing.T) {
	for _, scope := range readArgumentScopes() {
		for _, pair := range readArgumentCollisions {
			t.Run(scope.name+"/"+pair.name, func(t *testing.T) {
				run := readArgumentsRun(t, scope.chat)
				defer run.Context.Close()
				ran := []string{}
				read := run.Context.Execution.WrapRead("lookup", func(_ context.Context, raw json.RawMessage) (ToolResult, error) {
					ran = append(ran, string(raw))
					return ResultOf([]Content{TextContent("answer to " + string(raw))}, nil), nil
				}, &ToolContextPolicy{Immutable: true}, true, scope.kept)
				for at, call := range []string{pair.first, pair.second, pair.first} {
					result, err := read(context.Background(), json.RawMessage(call))
					if err != nil {
						t.Fatal(err)
					}
					if got, want := result.Content[0].Text(), "answer to "+call; got != want {
						t.Fatalf("call %d, %s, was answered %q", at+1, call, got)
					}
				}
				// The first arguments are not exact, so they are not kept even for themselves. The
				// second of a pair may be: {"q":1} is an ordinary read, and it runs once either way.
				if len(ran) != 3 {
					t.Fatalf("ran %q", ran)
				}
			})
		}
	}
}

// The check turns nothing else off: arguments the parsed form says exactly are served from the cache,
// and a chat's key still leaves out what the published form leaves out.
func TestTheReadWrapperStillServesOrdinaryArguments(t *testing.T) {
	for _, scope := range readArgumentScopes() {
		t.Run(scope.name, func(t *testing.T) {
			run := readArgumentsRun(t, scope.chat)
			defer run.Context.Close()
			ran := 0
			read := run.Context.Execution.WrapRead("lookup", func(_ context.Context, raw json.RawMessage) (ToolResult, error) {
				ran++
				return ResultOf([]Content{TextContent("answer to " + string(raw))}, nil), nil
			}, &ToolContextPolicy{Immutable: true}, true, scope.kept)
			for _, call := range []string{`{"id":7,"name":"x","ratio":1.5,"big":1e+21,"at":[0,-1,null,true],"nested":{"a":1}}`, ` { "id" : 7 , "name" : "x" , "ratio" : 1.5 , "big" : 1e+21 , "at" : [ 0 , -1 , null , true ] , "nested" : { "a" : 1 } } `} {
				if _, err := read(context.Background(), json.RawMessage(call)); err != nil {
					t.Fatal(err)
				}
			}
			if ran != 1 {
				t.Fatalf("ran %d times", ran)
			}
		})
	}
}

// No arguments at all are {}: the tool is run with what the caches key, and the two share an answer.
func TestTheReadWrapperRunsWhatItKeysWhereThereAreNoArguments(t *testing.T) {
	for _, scope := range readArgumentScopes() {
		t.Run(scope.name, func(t *testing.T) {
			run := readArgumentsRun(t, scope.chat)
			defer run.Context.Close()
			ran := []string{}
			read := run.Context.Execution.WrapRead("lookup", func(_ context.Context, raw json.RawMessage) (ToolResult, error) {
				ran = append(ran, string(raw))
				return ResultOf([]Content{TextContent("answer")}, nil), nil
			}, &ToolContextPolicy{Immutable: true}, true, scope.kept)
			for _, call := range []json.RawMessage{nil, json.RawMessage(`{}`)} {
				if _, err := read(context.Background(), call); err != nil {
					t.Fatal(err)
				}
			}
			if len(ran) != 1 || ran[0] != `{}` {
				t.Fatalf("ran %q", ran)
			}
		})
	}
}

// A Runtime tool reaches the same wrapper through the session's CallTool, under its own key.
func TestARuntimeReadIsServedNoOtherCallsAnswer(t *testing.T) {
	session := modelSession()
	session.Context = &Input{CurrentRequest: &CurrentRequest{Message: "Read it.", Input: "Read it."}, Chat: NewChatContext("read-arguments"), Runtime: &RuntimeIdentity{Identity: "jpack", Version: "0.39.0"}}
	ran := []string{}
	session.Tools = []RuntimeTool{{Name: "get_schema", Meta: &ToolTaxonomy{Operation: "read", Effect: "read"}}}
	session.CallTool = func(_ context.Context, name string, raw json.RawMessage) (ToolResult, error) {
		ran = append(ran, name+" "+string(raw))
		return ResultOf([]Content{TextContent("answer to " + string(raw))}, nil), nil
	}
	run := openRun(t, session)
	defer run.Context.Close()
	for _, call := range []string{`[1]`, `[2]`, `{"spec_version":1e400}`, `{"spec_version":2e400}`, `{"spec_version":"0.3.0"}`, `{"spec_version":"0.3.0"}`} {
		result, err := run.Session.CallTool(context.Background(), "get_schema", json.RawMessage(call))
		if err != nil {
			t.Fatal(err)
		}
		if got, want := result.Content[0].Text(), "answer to "+call; got != want {
			t.Fatalf("%s was answered %q", call, got)
		}
	}
	if len(ran) != 5 {
		t.Fatalf("ran %q", ran)
	}
}
