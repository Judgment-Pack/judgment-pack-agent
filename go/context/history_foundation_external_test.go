package runcontext_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	rc "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"strings"
	"testing"
)

func TestHistoryFoundationExportedScopeAtomicResume(t *testing.T) {
	ctx := context.Background()
	store := rc.NewMemoryRunStore(rc.MemoryRunStoreOptions{})
	chat := rc.NewChatContext("shared")
	if _, err := chat.OpenTurn(); err != nil {
		t.Fatal(err)
	}
	raw, err := chat.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	manifest, objects, err := rc.ChunkHistoryValue(raw)
	if err != nil {
		t.Fatal(err)
	}
	objects = append(objects, manifest)
	if err = store.StageHistoryObjects(ctx, rc.HistoryResource{Kind: "chat", ID: "c"}, objects); err != nil {
		t.Fatal(err)
	}
	checkpoint := contract.Checkpoint{Version: 2, ID: "r", RequestKey: strings.Repeat("a", 64), Phase: "work", Status: "running", Unit: "model-requests", Limit: 20, Grants: []contract.CheckpointGrant{}, Plan: []contract.CheckpointTaskStep{}, Operations: []contract.CheckpointOperation{}, Context: contract.CheckpointContext{Scope: "shared", Entries: []contract.CheckpointRetainedEntry{}}}
	cp, _ := json.Marshal(checkpoint)
	turn := 1
	first, err := store.CommitHistory(ctx, rc.RunHistoryCommit{Run: rc.RunCommit{RunID: "r", CommitID: "seed", Checkpoint: cp}, Context: &rc.ContextSnapshotUpdate{ChatID: "c", Manifest: manifest.HistoryObjectRef, Turn: &turn}})
	if err != nil {
		t.Fatal(err)
	}
	a, err := rc.RestoreStoredChatScope(ctx, store, *first.History.Context)
	if err != nil {
		t.Fatal(err)
	}
	b, err := rc.RestoreStoredChatScope(ctx, store, *first.History.Context)
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := rc.PrepareDurableInput(ctx, &rc.Input{Chat: a, RunStore: &rc.DurableRunInput{Store: store, ID: "r", Resume: true, Context: a}})
	if err != nil {
		t.Fatal(err)
	}
	head, _ := store.Load(ctx, "r")
	if !bytes.Equal(head.Checkpoint, cp) || head.Revision == first.Revision {
		t.Fatal("claim rewrote original bytes or missed CAS")
	}
	if _, err = a.OpenTurn(); err != nil {
		t.Fatal(err)
	}
	if _, err = a.OpenTurn(); err == nil {
		t.Fatal("duplicate open")
	}
	if a.Context().TurnCount() != 1 {
		t.Fatal("turn incremented")
	}
	if _, err = rc.PrepareDurableInput(ctx, &rc.Input{Chat: b, RunStore: &rc.DurableRunInput{Store: store, ID: "r", Resume: true, Context: b}}); err == nil {
		t.Fatal("competing attachment accepted")
	}
	calls := 0
	if _, err = rc.InvokeDurable(ctx, resumed.Persistence, "host", "fixture", "write", []byte(`{}`), false, func([]byte) (rc.ToolResult, error) { calls++; return rc.ResultOf([]rc.Content{}, nil), nil }, nil, nil); err != nil || calls != 1 {
		t.Fatal("actual original writer boundary", calls, err)
	}
}
func TestHistoryFoundationExportedSnapshotFixedRefusals(t *testing.T) {
	all := retainedFixture(t, "chat/context-snapshot.json")
	for _, item := range all.Get("items").Items() {
		raw := []byte(rc.CanonicalJSON(item))
		c, err := rc.RestoreChatContext(raw)
		if err != nil {
			t.Fatal(err)
		}
		again, _ := c.Snapshot()
		if !bytes.Equal(raw, again) {
			t.Fatal("exported shared roundtrip")
		}
		bad := item.Clone().Set("unrecognized", rc.Bool(false))
		var invalid *rc.ContextSnapshotInvalid
		if _, err = rc.RestoreChatContext([]byte(rc.CanonicalJSON(bad))); !errors.As(err, &invalid) {
			t.Fatal("unknown accepted", err)
		}
	}
}
