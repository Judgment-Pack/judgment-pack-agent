package runcontext

// #352: a read a durable run serves without a dispatch of its own is given the form its writer records a
// call's result in (#343). A Runtime read through CallTool is answered, retained and cached in that form.
// An entry of the chat's read cache, kept by a run that records nothing or brought back by a stored chat
// from before this change, is served to a durable run only where the record guard leaves it as it is:
// one it would change is neither served nor made the run's, and the read runs again; one it refuses is
// the call's refusal. recordedReads.test.ts holds the TypeScript; the run through both Go adapters is
// go/adapter/durableapprovaltests/recorded_reads_test.go.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

// readSession is a run on chat whose Runtime answers get_schema with the pack id given, counted in calls;
// durable under store where store is not nil.
func readSession(chat *ChatContext, store RunStore, calls *int, id string) Session {
	session := modelSession()
	session.Context = &Input{CurrentRequest: &CurrentRequest{Message: "Read it.", Input: "Read it."}, Chat: chat, Runtime: &RuntimeIdentity{Identity: "jpack", Version: "0.39.0"}}
	if store != nil {
		session.Context.RunStore = &DurableRunInput{Store: store, ID: "recorded-read"}
	}
	session.Tools = []RuntimeTool{{Name: "get_schema", Meta: &ToolTaxonomy{Operation: "read", Effect: "read"}}}
	session.CallTool = func(context.Context, string, json.RawMessage) (ToolResult, error) {
		*calls++
		return ReadToolResult(MustParseJSON(recordedPack(id))), nil
	}
	return session
}

// readOnce opens session, reads get_schema with args through the run's CallTool and closes the run.
func readOnce(t *testing.T, session Session, args string) (ToolResult, error) {
	t.Helper()
	got, err, _ := readAndCheckpoint(t, session, args)
	return got, err
}

// readAndCheckpoint is readOnce, and the text of the checkpoint the run would write after the read.
func readAndCheckpoint(t *testing.T, session Session, args string) (ToolResult, error, string) {
	t.Helper()
	run := openRun(t, session)
	defer run.Context.Close()
	got, err := run.Session.CallTool(context.Background(), "get_schema", json.RawMessage(args))
	saved, snapErr := run.Context.Snapshot("")
	if snapErr != nil {
		t.Fatal(snapErr)
	}
	text, _ := json.Marshal(saved)
	return got, err, string(text)
}

func TestADurableRunsRuntimeReadIsAnsweredAndKeptInTheRecordedForm(t *testing.T) {
	chat, calls := NewChatContext("recorded-reads"), 0
	got, err, checkpoint := readAndCheckpoint(t, readSession(chat, NewMemoryRunStore(MemoryRunStoreOptions{}), &calls, recordedRaw), `{"spec_version":"0.3.0"}`)
	if want := recordedPack(recordedRedacted); err != nil || string(got.JSON()) != want {
		t.Fatalf("answered %s, %v; want %s", got.JSON(), err, want)
	}
	if snapshot, _ := chat.Snapshot(); strings.Contains(string(snapshot), recordedRawPath) || !strings.Contains(string(snapshot), "local-temp-path") {
		t.Fatal("the chat does not hold the recorded form alone")
	}
	if strings.Contains(checkpoint, recordedRawPath) || !strings.Contains(checkpoint, "local-temp-path") {
		t.Fatal("the checkpoint does not hold the recorded form alone")
	}
	// A run that records nothing is answered as the Runtime answered, as before.
	plain := 0
	got, err = readOnce(t, readSession(NewChatContext("plain-reads"), nil, &plain, recordedRaw), `{"spec_version":"0.3.0"}`)
	if want := recordedPack(recordedRaw); err != nil || string(got.JSON()) != want || calls != 1 || plain != 1 {
		t.Fatalf("answered %s, %v; ran %d and %d", got.JSON(), err, calls, plain)
	}
}

func TestAChatsCachedReadTheGuardWouldChangeIsReadAgainInADurableRun(t *testing.T) {
	want := recordedPack(recordedRedacted)
	for _, from := range []string{"the same chat", "a stored chat"} {
		t.Run(from, func(t *testing.T) {
			chat, calls := NewChatContext("recorded-reads"), 0
			// A run that records nothing keeps the Runtime's answer in the chat's read cache as it was given.
			if got, err := readOnce(t, readSession(chat, nil, &calls, recordedRaw), `{}`); err != nil || string(got.JSON()) != recordedPack(recordedRaw) {
				t.Fatalf("answered %s, %v", got.JSON(), err)
			}
			if from == "a stored chat" {
				saved, err := chat.Snapshot()
				if err != nil || !strings.Contains(string(saved), recordedRawPath) {
					t.Fatalf("the chat did not keep the read: %v", err)
				}
				if chat, err = RestoreChatContext(saved); err != nil {
					t.Fatal(err)
				}
			}
			got, err, checkpoint := readAndCheckpoint(t, readSession(chat, NewMemoryRunStore(MemoryRunStoreOptions{}), &calls, recordedRaw), `{}`)
			if err != nil || string(got.JSON()) != want || calls != 2 {
				t.Fatalf("answered %s, %v; ran %d times", got.JSON(), err, calls)
			}
			// The kept entry was not made the durable run's: its checkpoint holds the recorded form alone.
			if strings.Contains(checkpoint, recordedRawPath) || !strings.Contains(checkpoint, "local-temp-path") {
				t.Fatal("the checkpoint does not hold the recorded form alone")
			}
			// The recorded answer is kept in its place: a later run is served it without a call.
			if got, err := readOnce(t, readSession(chat, nil, &calls, recordedRaw), `{}`); err != nil || string(got.JSON()) != want || calls != 2 {
				t.Fatalf("answered %s, %v; ran %d", got.JSON(), err, calls)
			}
		})
	}
}

// An entry the guard leaves as it is is served from the cache, as before.
func TestAChatsCachedReadTheGuardLeavesIsServedToADurableRun(t *testing.T) {
	kept := "https://packs.example/var/evaluation-pack"
	chat, calls := NewChatContext("recorded-reads"), 0
	if _, err := readOnce(t, readSession(chat, nil, &calls, kept), `{}`); err != nil {
		t.Fatal(err)
	}
	got, err := readOnce(t, readSession(chat, NewMemoryRunStore(MemoryRunStoreOptions{}), &calls, kept), `{}`)
	if err != nil || string(got.JSON()) != recordedPack(kept) || calls != 1 {
		t.Fatalf("answered %s, %v; ran %d", got.JSON(), err, calls)
	}
}

// Under a refusing guard the cached entry is the read's refusal: nothing is served and nothing is run.
func TestAChatsCachedReadARefusingGuardWithholdsIsNotServed(t *testing.T) {
	chat, calls := NewChatContext("recorded-reads"), 0
	if _, err := readOnce(t, readSession(chat, nil, &calls, recordedRaw), `{}`); err != nil {
		t.Fatal(err)
	}
	session := readSession(chat, NewMemoryRunStore(MemoryRunStoreOptions{}), &calls, recordedRaw)
	session.Policy = PolicyLayer{"outputGuard": "refuse"}
	got, err, checkpoint := readAndCheckpoint(t, session, `{}`)
	var refused *OutputRefused
	if !errors.As(err, &refused) || refused.Pattern != "local-temp-path" || got.Content != nil || got.StructuredContent != nil || calls != 1 {
		t.Fatalf("answered %s, %v; ran %d", got.JSON(), err, calls)
	}
	if strings.Contains(checkpoint, recordedRawPath) {
		t.Fatal("the checkpoint holds the refused entry")
	}
	// The entry is left as it was kept.
	if got, err := readOnce(t, readSession(chat, nil, &calls, recordedRaw), `{}`); err != nil || string(got.JSON()) != recordedPack(recordedRaw) || calls != 1 {
		t.Fatalf("the refused entry is %s, %v; ran %d", got.JSON(), err, calls)
	}
}

// keptRawEntry is the id and text of the entry a run that records nothing kept on chat for its get_schema read.
func keptRawEntry(t *testing.T, chat *ChatContext) (string, string) {
	t.Helper()
	calls := 0
	if _, err := readOnce(t, readSession(chat, nil, &calls, recordedRaw), `{}`); err != nil || len(chat.results) != 1 {
		t.Fatalf("the chat kept nothing: %v", err)
	}
	id := chat.results[0].entry
	chat.mu.Lock()
	defer chat.mu.Unlock()
	return id, chat.entries.get(id).Value.Text()
}

// recordedEntry is the record guard's form of the entry keptRawEntry keeps, its members in their order.
const recordedEntry = `{"content":[{"type":"text","text":"{\"packId\":\"https://packs.example[redacted: local-temp-path]\",\"status\":\"evaluated\"}"}],"structuredContent":{"packId":"https://packs.example[redacted: local-temp-path]","status":"evaluated"}}`

// carriedEntry is the entry under id a checkpoint carries, or nil.
func carriedEntry(saved contract.Checkpoint, id string) *contract.CheckpointRetainedEntry {
	for i := range saved.Context.Entries {
		if saved.Context.Entries[i].ID == id {
			return &saved.Context.Entries[i]
		}
	}
	return nil
}

// #352: an entry of the chat a durable run reads by id is read, and carried by its checkpoint, in the form
// its writer records; the chat keeps its own. A refusing guard's refusal is the read's, and nothing is
// referenced. The run through both Go adapters is durableapprovaltests/recorded_reads_test.go.
func TestADurableRunsReadByIDOfAChatEntryIsCarriedInTheRecordedForm(t *testing.T) {
	for _, name := range []string{ReadTool, "read_context"} {
		t.Run(name, func(t *testing.T) {
			chat := NewChatContext("recorded-reads")
			id, kept := keptRawEntry(t, chat)
			want := recordedEntry
			calls := 0
			run := openRun(t, readSession(chat, NewMemoryRunStore(MemoryRunStoreOptions{}), &calls, recordedRaw))
			defer run.Context.Close()
			got, err := hostTool(t, run, name).Execute(context.Background(), json.RawMessage(`{"id":"`+id+`"}`))
			if err != nil || strings.Contains(string(got.JSON()), recordedRawPath) || !strings.Contains(string(got.JSON()), "local-temp-path") {
				t.Fatalf("read %s, %v", got.JSON(), err)
			}
			// Read again, it is the same: the run reads the recorded form it holds, not the chat's.
			if again, err := hostTool(t, run, name).Execute(context.Background(), json.RawMessage(`{"id":"`+id+`"}`)); err != nil || string(again.JSON()) != string(got.JSON()) {
				t.Fatalf("read again %s, %v; first %s", again.JSON(), err, got.JSON())
			}
			saved, err := run.Context.Snapshot("")
			if err != nil {
				t.Fatal(err)
			}
			carried := carriedEntry(saved, id)
			if carried == nil || string(carried.Value) != want || carried.Digest != ContextDigest(want) || carried.Bytes != int64(UTF8Bytes(want)) {
				t.Fatalf("the checkpoint carries %s, want %s", carried.Value, want)
			}
			chat.mu.Lock()
			own := chat.entries.get(id).Value.Text()
			chat.mu.Unlock()
			if own != kept {
				t.Fatalf("the chat's own entry is %s", own)
			}
		})
	}
	t.Run("a refusing guard", func(t *testing.T) {
		chat := NewChatContext("recorded-reads")
		id, _ := keptRawEntry(t, chat)
		calls := 0
		session := readSession(chat, NewMemoryRunStore(MemoryRunStoreOptions{}), &calls, recordedRaw)
		session.Policy = PolicyLayer{"outputGuard": "refuse"}
		run := openRun(t, session)
		defer run.Context.Close()
		_, err := hostTool(t, run, ReadTool).Execute(context.Background(), json.RawMessage(`{"id":"`+id+`"}`))
		var refused *OutputRefused
		if !errors.As(err, &refused) || refused.Pattern != "local-temp-path" {
			t.Fatalf("read by id answered %v", err)
		}
		saved, err := run.Context.Snapshot("")
		if err != nil || carriedEntry(saved, id) != nil {
			t.Fatalf("the refused entry was made the run's: %v", err)
		}
	})
}

// #352: a checkpoint restored into a chat that still holds an entry as it was kept: the durable run reads
// it, and carries it, in the recorded form, as RecordWith gives the entries a restore referenced.
func TestARestoredChatEntryIsCarriedInTheRecordedForm(t *testing.T) {
	chat := NewChatContext("recorded-reads")
	id, _ := keptRawEntry(t, chat)
	want := recordedEntry
	turn, err := chat.OpenTurn()
	if err != nil {
		t.Fatal(err)
	}
	defer turn.Release()
	ledger := NewContextLedger(&Defaults, turn)
	saved := contract.CheckpointContext{Scope: turn.Scope(), Entries: []contract.CheckpointRetainedEntry{{ID: id, Kind: "tool", Label: "get_schema", Value: json.RawMessage(want), Bytes: int64(UTF8Bytes(want)), Digest: ContextDigest(want)}}}
	if err := ledger.Restore(saved); err != nil {
		t.Fatal(err)
	}
	guard, err := NewOutputGuard(PolicyMembers{}, func() map[string]int64 { return map[string]int64{} })
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.RecordWith(recordedValueForm(RecordDefaults, func() RecordGuard { return guard })); err != nil {
		t.Fatal(err)
	}
	carried := ledger.Snapshot().Entries
	if len(carried) != 1 || carried[0].ID != id || string(carried[0].Value) != want || carried[0].Digest != ContextDigest(want) {
		t.Fatalf("the restored run carries %d entries: %s, want %s", len(carried), carried[0].Value, want)
	}
	// And the run reads that form, as its checkpoint carries it, not the chat's.
	read, err := ledger.ReadItem(ObjectOf("id", String(id)))
	if err != nil || strings.Contains(string(read.JSON()), recordedRawPath) || !strings.Contains(string(read.JSON()), "[redacted: local-temp-path]") {
		t.Fatalf("the restored run read %s, %v", read.JSON(), err)
	}
}

// #352: a restored checkpoint references a chat entry the record guard now refuses: binding the run fails
// with that refusal, and the entry is no longer the run's, so nothing of it is carried or read.
func TestARestoredChatEntryTheRecordGuardRefusesIsNotCarried(t *testing.T) {
	chat := NewChatContext("recorded-reads")
	id, _ := keptRawEntry(t, chat)
	turn, err := chat.OpenTurn()
	if err != nil {
		t.Fatal(err)
	}
	defer turn.Release()
	ledger := NewContextLedger(&Defaults, turn)
	saved := contract.CheckpointContext{Scope: turn.Scope(), Entries: []contract.CheckpointRetainedEntry{{ID: id, Kind: "tool", Label: "get_schema", Value: json.RawMessage(recordedEntry), Bytes: int64(UTF8Bytes(recordedEntry)), Digest: ContextDigest(recordedEntry)}}}
	if err := ledger.Restore(saved); err != nil {
		t.Fatal(err)
	}
	guard, err := NewOutputGuard(PolicyMembers{"outputGuard": {Value: "refuse", Layer: "host"}}, func() map[string]int64 { return map[string]int64{} })
	if err != nil {
		t.Fatal(err)
	}
	err = ledger.RecordWith(recordedValueForm(RecordDefaults, func() RecordGuard { return guard }))
	var refused *OutputRefused
	if !errors.As(err, &refused) || refused.Pattern != "local-temp-path" {
		t.Fatalf("binding the run answered %v, want the guard's refusal", err)
	}
	if carried := ledger.Snapshot().Entries; len(carried) != 0 {
		t.Fatalf("the refused run carries %d entries: %s", len(carried), carried[0].Value)
	}
	if read, err := ledger.ReadItem(ObjectOf("id", String(id))); !errors.As(err, &refused) {
		t.Fatalf("the refused run read %s, %v", read.JSON(), err)
	}
}

// #352: the read cache serves a durable run the entry it peeked and found unchanged, and no other: where
// the key has since been given another entry, CachedAs serves nothing, and the read runs as on a miss.
func TestTheReadCacheServesOnlyTheEntryThatWasPeeked(t *testing.T) {
	chat := NewChatContext("recorded-reads")
	turn, err := chat.OpenTurn()
	if err != nil {
		t.Fatal(err)
	}
	defer turn.Release()
	ledger := NewContextLedger(&Defaults, turn)
	text := func(s string) ToolResult {
		return ReadToolResult(ObjectOf("content", Array(ObjectOf("type", String("text"), "text", String(s)))))
	}
	if err := turn.Keep(ledger, "get_schema", ObjectOf("v", String("1")), text("peeked"), "get_schema:key", true); err != nil {
		t.Fatal(err)
	}
	_, peeked, ok := turn.Peek("get_schema:key")
	if !ok {
		t.Fatal("nothing to peek")
	}
	if err := turn.Keep(ledger, "get_schema", ObjectOf("v", String("2")), text("replaced"), "get_schema:key", true); err != nil {
		t.Fatal(err)
	}
	if held, ok := turn.CachedAs("get_schema:key", peeked); ok {
		t.Fatalf("the read cache served %s for the entry it no longer holds under the key", held.JSON())
	}
	_, now, _ := turn.Peek("get_schema:key")
	if held, ok := turn.CachedAs("get_schema:key", now); !ok || !strings.Contains(string(held.JSON()), "replaced") {
		t.Fatalf("the read cache served %s, %v for the entry it holds", held.JSON(), ok)
	}
}

// #352: a durable run's search over every item matches each chat entry in the form it reads that entry by
// id, so what it finds, and where, agrees with a search of that one item and with read_window, and the
// search makes no entry the run's. A run that records nothing searches the chat's own entries. A guard
// refusal of an entry is the search's error.
func TestADurableRunsSearchOfEveryItemMatchesTheRecordedForm(t *testing.T) {
	ctx := context.Background()
	chat := NewChatContext("recorded-search")
	id, _ := keptRawEntry(t, chat)
	matches := func(t *testing.T, run *Run, args string) string {
		t.Helper()
		found, err := hostTool(t, run, SearchTool).Execute(ctx, json.RawMessage(args))
		if err != nil {
			t.Fatalf("search %s: %v", args, err)
		}
		return CanonicalJSON(found.StructuredContent.Get("matches"))
	}
	t.Run("a durable run", func(t *testing.T) {
		calls := 0
		run := openRun(t, readSession(chat, NewMemoryRunStore(MemoryRunStoreOptions{}), &calls, recordedRaw))
		defer run.Context.Close()
		// The raw text's path is not there to find.
		if got := matches(t, run, `{"pattern":"tmp/evaluation"}`); got != "[]" {
			t.Fatalf("found the raw path: %s", got)
		}
		every := matches(t, run, `{"pattern":"local-temp-path"}`)
		saved, err := run.Context.Snapshot("")
		if err != nil {
			t.Fatal(err)
		}
		if carried := carriedEntry(saved, id); carried != nil {
			t.Fatalf("the search made the entry the run's: %s", carried.Value)
		}
		// What is there is found where a search of the one item finds it.
		if one := matches(t, run, `{"pattern":"local-temp-path","id":"`+id+`"}`); every == "[]" || every != one || strings.Contains(every, recordedRawPath) {
			t.Fatalf("every item: %s; the one item: %s", every, one)
		}
	})
	t.Run("a run that records nothing", func(t *testing.T) {
		calls := 0
		run := openRun(t, readSession(chat, nil, &calls, recordedRaw))
		defer run.Context.Close()
		if got := matches(t, run, `{"pattern":"tmp/evaluation"}`); got == "[]" {
			t.Fatal("the chat's own entry was not searched")
		}
	})
	t.Run("a refusing guard", func(t *testing.T) {
		calls := 0
		session := readSession(chat, NewMemoryRunStore(MemoryRunStoreOptions{}), &calls, recordedRaw)
		session.Policy = PolicyLayer{"outputGuard": "refuse"}
		run := openRun(t, session)
		defer run.Context.Close()
		found, err := hostTool(t, run, SearchTool).Execute(ctx, json.RawMessage(`{"pattern":"evaluated"}`))
		var refused *OutputRefused
		if !errors.As(err, &refused) || refused.Pattern != "local-temp-path" || found.StructuredContent != nil {
			t.Fatalf("searched %s, %v", found.JSON(), err)
		}
	})
}
