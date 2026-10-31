package runcontext

// #271: a read's cached answer belongs to the bytes that were run. Both caches key from what ParseJSON
// reads and the tool is run with the raw arguments, so arguments that reading does not say exactly
// get no key: contract/vectors/chat/read-arguments.json states the cases (context/readKeys.test.ts
// holds the TypeScript to the same ones). go/adapter/eino/read_keys_test.go drives the same collisions
// through both adapters.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"
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

// The bytes these name are built, not written, so that an escape in them stays one: the row that holds
// these escapes checks it holds a backslash (TestTheReadArgumentRowsHoldTheirEscapes).
var (
	escapedA          = string(rune(0x5c)) + "u0061"
	escapedKelvinSign = string(rune(0x5c)) + "u212a"
	kelvinSign        = string(rune(0x212a))
	longS             = string(rune(0x017f))
)

// Two calls that are run differently and that ParseJSON reads alike, or that a struct reader tells
// apart by order, one row per way of reading them alike. The first two rows are the issue's.
var readArgumentCollisions = []struct{ name, first, second string }{
	{"an array reads as {}", `[1]`, `[2]`},
	{"a number out of range reads as null", `{"q":1e400}`, `{"q":2e400}`},
	{"an integer past 2^53 reads as a float64", `{"id":9007199254740993}`, `{"id":9007199254740992}`},
	{"a repeated name keeps the last value", `{"a":{"x":1},"a":{"y":2}}`, `{"a":{"y":2}}`},
	{"a repeated name, escaped, keeps the last value", `{"a":1,"` + escapedA + `":2}`, `{"a":2}`},
	{"a repeated name in a nested object", `{"o":{"a":1,"a":2}}`, `{"o":{"a":2}}`},
	{"another spelling of a number", `{"q":1.0}`, `{"q":1}`},
	{"an exponent", `{"q":1e2}`, `{"q":100}`},
	{"an upper-case exponent", `{"q":1E+21}`, `{"q":1e+21}`},
	{"negative zero", `{"q":-0}`, `{"q":0}`},
	{"underflow", `{"q":1e-400}`, `{"q":0}`},
	{"a number nested in an array", `{"a":[1e400]}`, `{"a":[2e400]}`},
	{"a number nested in an object", `{"a":{"b":1e400}}`, `{"a":{"b":2e400}}`},
	{"an array reads as an empty object's {}", `[1]`, `{}`},
	{"text that is not JSON reads as {}", `{bad`, `{worse`},
	{"a second value after the object reads as {}", `{"q":1} {"q":2}`, `{"q":1} {"q":3}`},
	{"garbage after the object reads as {}", `{"q":1} ]`, `{"q":2} ]`},
	{"two names that fold together, in both orders", `{"ID":1,"id":2}`, `{"id":2,"ID":1}`},
	{"two names that fold together, nested", `{"o":{"ID":1,"id":2}}`, `{"o":{"id":2,"ID":1}}`},
	{"a name and its Kelvin sign", `{"` + kelvinSign + `":1,"k":2}`, `{"k":2,"` + kelvinSign + `":1}`},
	{"a name and its Kelvin sign, escaped", `{"` + escapedKelvinSign + `":1,"k":2}`, `{"k":2,"` + escapedKelvinSign + `":1}`},
	{"a name and its long s", `{"` + longS + `":1,"s":2}`, `{"s":2,"` + longS + `":1}`},
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
			ordinary := `{"id":7,"name":"x","ratio":1.5,"big":1e+21,"at":[0,-1,null,true],"nested":{"a":1}}`
			spaced := ` { "id" : 7 , "name" : "` + string(rune(0x5c)) + `u0078" , "ratio" : 1.5 , "big" : 1e+21 , "at" : [ 0 , -1 , null , true ] , "nested" : { "a" : 1 } } `
			if !strings.Contains(spaced, string(rune(0x5c))) {
				t.Fatal("the spaced form holds no escape")
			}
			for _, call := range []string{ordinary, spaced, "\n" + ordinary, "\t" + ordinary, "\r\n " + ordinary} {
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

func TestTheReadArgumentRowsHoldTheirEscapes(t *testing.T) {
	for _, row := range []struct{ text, reads string }{{escapedA, "a"}, {escapedKelvinSign, kelvinSign}} {
		var read string
		if err := json.Unmarshal([]byte(`"`+row.text+`"`), &read); err != nil || read != row.reads || !strings.HasPrefix(row.text, string(rune(0x5c))) {
			t.Fatalf("%q reads as %q, %v", row.text, read, err)
		}
	}
	if len(kelvinSign) != 3 || len(longS) != 2 {
		t.Fatal("the rows' non-ASCII names are not the code points they name")
	}
}

// Folding is encoding/json's: a struct field matches a name exactly where foldName says the two are
// equal, read through the real decoder. Then every rune with a fold set, and a sample with none.
func TestFoldNameIsWhatEncodingJSONMatches(t *testing.T) {
	type field struct {
		Value int `json:"KS"`
	}
	dotless := string(rune(0x131))
	for name, match := range map[string]bool{"KS": true, "ks": true, "kS": true, kelvinSign + "S": true, "K" + longS: true, kelvinSign + longS: true, "K": false, "KSS": false, "K" + dotless: false} {
		var got field
		if err := json.Unmarshal([]byte(`{"`+name+`":1}`), &got); err != nil {
			t.Fatal(err)
		}
		if (got.Value == 1) != match || (foldName(name) == foldName("KS")) != match {
			t.Fatalf("%q: encoding/json matched %v, foldName %v, want %v", name, got.Value == 1, foldName(name) == foldName("KS"), match)
		}
	}
	for r := rune(0); r <= 0x10ffff; r++ {
		if r >= 0xd800 && r <= 0xdfff {
			continue
		}
		for other := unicode.SimpleFold(r); other != r; other = unicode.SimpleFold(other) {
			if foldName(string(r)) != foldName(string(other)) || !strings.EqualFold(string(r), string(other)) {
				t.Fatalf("%U and %U are one fold set", r, other)
			}
		}
	}
	for _, pair := range [][2]rune{{0x131, 'i'}, {0x130, 'i'}, {0x131, 0x130}, {'a', 'b'}} {
		if foldName(string(pair[0])) == foldName(string(pair[1])) {
			t.Fatalf("%U and %U are not one fold set", pair[0], pair[1])
		}
	}
}

// A snapshot a published release wrote (#271): contract/vectors/chat/context-snapshot-v1-cache.json is
// byte for byte what the v0.12.0 tag's code writes for its reads. It still loads, and a read returns
// it as written. Restored, it keeps its entries and index and none of its cache rows, and the next
// snapshot is the current version.
func TestASnapshotFromBeforeTheFixRestoresWithoutItsCacheRows(t *testing.T) {
	vector := readJSON(t, "contract/vectors/chat/context-snapshot-v1-cache.json")
	items := vector.Get("items").Items()
	written, want := []byte(CanonicalJSON(items[0])), []byte(CanonicalJSON(items[1]))
	read, err := ReadChatContextSnapshot(written)
	if err != nil || read.Version != 1 || len(read.Cache) != len(vector.Get("reads").Items()) {
		t.Fatalf("the published snapshot does not read as written: %v %+v", err, read)
	}
	chat, err := RestoreChatContext(written)
	if err != nil {
		t.Fatal(err)
	}
	if len(chat.cache) != 0 {
		t.Fatalf("%d cache rows restored", len(chat.cache))
	}
	if len(chat.EntryIDs()) != len(read.Entries) || len(chat.results) != len(read.Results) {
		t.Fatalf("%d entries and %d index rows restored", len(chat.EntryIDs()), len(chat.results))
	}
	again, err := chat.Snapshot()
	if err != nil || string(again) != string(want) {
		t.Fatalf("the next snapshot is\n%s\nnot\n%s\n%v", again, want, err)
	}
	// The version written now round-trips, its cache rows with it.
	current := items[0].Clone().Set("version", Number(chatSnapshotVersion))
	restored, err := RestoreChatContext([]byte(CanonicalJSON(current)))
	if err != nil || len(restored.cache) != len(read.Cache) {
		t.Fatalf("a current snapshot lost its cache rows: %v", err)
	}
	if again, err = restored.Snapshot(); err != nil || string(again) != CanonicalJSON(current) {
		t.Fatalf("a current snapshot does not round-trip: %v", err)
	}
	// Only the versions written are read: a later one is refused by its version, before the body.
	var unsupported *UnsupportedContextSnapshotVersion
	if _, err = RestoreChatContext([]byte(CanonicalJSON(items[0].Clone().Set("version", Number(chatSnapshotVersion+1))))); !errors.As(err, &unsupported) {
		t.Fatalf("a later version: %v", err)
	}
	var invalid *ContextSnapshotInvalid
	for _, version := range []float64{0, -1, 1.5} {
		if _, err = RestoreChatContext([]byte(CanonicalJSON(items[0].Clone().Set("version", Number(version))))); !errors.As(err, &invalid) {
			t.Fatalf("version %v: %v", version, err)
		}
	}
}

// The same through the store, the path a host's stored chat takes: stored by a published release, read
// back and resumed on its open turn, it answers none of the calls its rows would have answered.
func TestAStoredChatFromBeforeTheFixServesNoCachedAnswer(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryRunStore(MemoryRunStoreOptions{})
	vector := readJSON(t, "contract/vectors/chat/context-snapshot-v1-cache.json")
	turn := 1
	item := vector.Get("items").Items()[0].Clone().Set("open", Array(ObjectOf("number", Number(float64(turn)), "referenced", Array(), "offered", Array())))
	manifest, objects, err := ChunkHistoryValue([]byte(CanonicalJSON(item)))
	if err != nil {
		t.Fatal(err)
	}
	if err = store.StageHistoryObjects(ctx, HistoryResource{Kind: "chat", ID: "stored"}, append(objects, manifest)); err != nil {
		t.Fatal(err)
	}
	head, err := store.CommitContextSnapshot(ctx, ContextSnapshotUpdate{ChatID: "stored", Manifest: manifest.HistoryObjectRef, Turn: &turn}, "commit-v1")
	if err != nil {
		t.Fatal(err)
	}
	scope, err := RestoreStoredChatScope(ctx, store, HistoryContextRef{ContextSnapshotHead: head, Turn: &turn})
	if err != nil {
		t.Fatal(err)
	}
	opened, err := scope.OpenTurn()
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range vector.Get("reads").Items() {
		key, ok := ReadCacheKeyOf("lookup", json.RawMessage(row.Get("call").Str()), false, nil)
		if !ok {
			t.Fatalf("%s has no key", row.Get("call").Str())
		}
		if held, ok := opened.Cached(key); ok {
			t.Fatalf("%s was served %s", row.Get("call").Str(), held.JSON())
		}
	}
	if got := len(scope.Context().EntryIDs()); got != len(vector.Get("reads").Items()) {
		t.Fatalf("%d entries restored", got)
	}
}

// No arguments at all are {} on every wrapped tool, a write's as much as a read's: the tool is run with
// what the caches key, and an immutable read's two forms share an answer.
func TestEmptyArgumentsRunAsAnEmptyObjectOnEveryWrappedTool(t *testing.T) {
	for _, tool := range []struct {
		name     string
		policy   *ToolContextPolicy
		readOnly bool
	}{{"a mutable read", nil, true}, {"a write", nil, false}, {"a write that says it is immutable", &ToolContextPolicy{Immutable: true}, false}} {
		t.Run(tool.name, func(t *testing.T) {
			run := readArgumentsRun(t, false)
			defer run.Context.Close()
			ran := []string{}
			call := run.Context.Execution.WrapRead("lookup", func(_ context.Context, raw json.RawMessage) (ToolResult, error) {
				ran = append(ran, string(raw))
				return ResultOf([]Content{TextContent("answer")}, nil), nil
			}, tool.policy, tool.readOnly, nil)
			for _, given := range []json.RawMessage{nil, json.RawMessage(`{}`)} {
				if _, err := call(context.Background(), given); err != nil {
					t.Fatal(err)
				}
			}
			if len(ran) != 2 || ran[0] != `{}` || ran[1] != `{}` {
				t.Fatalf("ran %q", ran)
			}
		})
	}
}

// A direct call of a wrapped tool with no arguments, the output guard off, under a run store: the
// durable writer used to refuse the empty bytes and now runs {}. The upgrade guide says so.
func TestAnEmptyCallUnderARunStoreRunsAsAnEmptyObject(t *testing.T) {
	session := modelSession()
	ran := []string{}
	session.Policy = PolicyLayer{"outputGuard": "off"}
	session.HostTools = []HostTool{{Name: "write_it", Description: "Write it.", Effect: "workflow", InputSchema: json.RawMessage(`{"type":"object"}`), Taxonomy: &ToolTaxonomy{Operation: "own-state", Effect: "pure"},
		Execute: func(_ context.Context, raw json.RawMessage) (ToolResult, error) {
			ran = append(ran, string(raw))
			return ResultOf([]Content{TextContent("wrote")}, nil), nil
		}}}
	session.Context = &Input{CurrentRequest: &CurrentRequest{Message: "Work", Input: "Work"}, RunStore: &DurableRunInput{Store: NewMemoryRunStore(MemoryRunStoreOptions{}), ID: "empty"}}
	run := openRun(t, session)
	defer run.Context.Close()
	if _, err := hostTool(t, run, "write_it").Execute(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if len(ran) != 1 || ran[0] != `{}` {
		t.Fatalf("ran %q", ran)
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
