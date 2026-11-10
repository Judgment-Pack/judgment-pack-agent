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
