package runcontext

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"strings"
	"testing"
	"time"
)

func retainedFixture(t *testing.T, original bool) contract.Checkpoint {
	t.Helper()
	v := vectorItems(t, "storage/retained-value.json")[0]
	var cp contract.Checkpoint
	if err := json.Unmarshal(v.JSON(), &cp); err != nil {
		t.Fatal(err)
	}
	if original {
		cp.Context.Entries[0].Value = json.RawMessage(*cp.Context.Entries[0].ValueJSON)
		cp.Context.Entries[0].ValueJSON = nil
	}
	return cp
}
func retainedHeader(t *testing.T, cp contract.Checkpoint, version int64) contract.Checkpoint {
	t.Helper()
	raw, err := json.Marshal(cp)
	if err != nil {
		t.Fatal(err)
	}
	value, err := ParseJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	written, err := WriteRecord(CheckpointSchema, value, RecordDefaults, nil, time.Now(), "2030-01-01T00:00:00.000Z")
	if err != nil {
		t.Fatal(err)
	}
	written.Set("version", Number(float64(version)))
	written.Set("digest", String(RecordDigest(written)))
	if err = json.Unmarshal(written.JSON(), &cp); err != nil {
		t.Fatal(err)
	}
	return cp
}
func retainedLedger() *ContextLedger {
	return NewContextLedger(&Policy{ReadBytes: Defaults.ReadBytes, RetainedBytes: Defaults.RetainedBytes, ToolResultBytes: Defaults.ToolResultBytes}, nil)
}
func TestRetainedRepresentationSharedV2AndFrozenV1(t *testing.T) {
	cp := retainedFixture(t, false)
	l := retainedLedger()
	if err := l.Restore(cp.Context); err != nil {
		t.Fatal(err)
	}
	got := l.Snapshot().Entries[0]
	if string(got.Value) != *cp.Context.Entries[0].ValueJSON || got.Digest != cp.Context.Entries[0].Digest {
		t.Fatal("exact retained source was lost")
	}
	cp.Version = 1
	raw, _ := json.Marshal(cp)
	var unknown *contract.UnknownRecordMember
	if err := contract.CheckRecord("checkpoint", raw); !errors.As(err, &unknown) {
		t.Fatal("v1 accepted new retained member", err)
	}
	legacy := retainedFixture(t, true)
	legacy.Version = 1
	raw, _ = json.Marshal(legacy)
	if err := contract.CheckRecord("checkpoint", raw); err != nil {
		t.Fatal(err)
	}
	if err := retainedLedger().Restore(legacy.Context); err != nil {
		t.Fatal(err)
	}
	legacy.Context.Entries[0].Value = json.RawMessage(`{"changed":true}`)
	if err := retainedLedger().Restore(legacy.Context); err == nil {
		t.Fatal("corrupt legacy digest accepted")
	}
}
func TestRetainedRepresentationRefusesForgedPairs(t *testing.T) {
	for _, mode := range []string{"raw", "semantic", "digest", "bytes", "missing-value", "malformed", "oversized", "alternate-spelling"} {
		t.Run(mode, func(t *testing.T) {
			cp := retainedFixture(t, false)
			e := &cp.Context.Entries[0]
			switch mode {
			case "raw":
				v := `{"z":3,"a":{"last":1,"first":0}}`
				e.ValueJSON = &v
			case "semantic":
				e.Value = json.RawMessage(`{"different":true}`)
			case "digest":
				e.Digest = strings.Repeat("0", 64)
			case "bytes":
				e.Bytes++
			case "missing-value":
				e.Value = nil
			case "malformed":
				v := "{"
				e.ValueJSON = &v
			case "oversized":
				v := strings.Repeat(" ", StorageRecordBytes+1)
				e.ValueJSON = &v
			case "alternate-spelling":
				v := " " + *e.ValueJSON
				e.ValueJSON = &v
			}
			if err := retainedLedger().Restore(cp.Context); err == nil {
				t.Fatal("forged retained representation accepted")
			}
		})
	}
}

type retainedPairGuard struct{ mode string }

func (g retainedPairGuard) JSON(raw json.RawMessage, _ string, _ bool, _ ...*RecordMarker) (json.RawMessage, error) {
	value, err := ParseJSON(raw)
	if err != nil {
		return nil, err
	}
	if g.mode == "redact-root" && value.IsString() && value.Str() == "before guard" {
		return String("after guard").JSON(), nil
	}
	if entries := value.Get("context").Get("entries"); entries.Len() > 0 {
		e := entries.Items()[0]
		switch g.mode {
		case "value":
			e.Set("value", ObjectOf("redacted", Bool(true)))
		case "remove-value":
			out := Object()
			for _, key := range e.Keys() {
				if key != "value" {
					out.Set(key, e.Get(key))
				}
			}
			entries.a[0] = out
		case "raw":
			e.Set("valueJSON", String(`{"private":"forged"}`))
		}
	}
	return value.JSON(), nil
}
func TestRetainedRepresentationNestedGuardAndCanonicalResume(t *testing.T) {
	for _, mode := range []string{"unchanged", "value", "remove-value", "raw"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			store := NewMemoryRunStore(MemoryRunStoreOptions{})
			root := writerCheckpoint("root-nested")
			child := retainedFixture(t, true)
			child.ID = "child-one"
			child = retainedHeader(t, child, 2)
			writer, err := NewRunWriter(store, root.ID, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			writer.Bind(func() (contract.Checkpoint, error) { return root, nil }, RecordDefaults, func() RecordGuard { return retainedPairGuard{mode} })
			binding := RunBinding{Writer: writer, Scope: DurableScope{Execution: child.ID, Role: "author", Task: "task-one"}}
			err = writer.Save(ctx, binding, child)
			head, _ := store.Load(ctx, root.ID)
			if mode != "unchanged" {
				var refused *ContinuationNotRecoverable
				if !errors.As(err, &refused) || head != nil {
					t.Fatal("guard mismatch published or restored unsafe data", err)
				}
				return
			}
			if err != nil || head == nil {
				t.Fatal("valid pair refused", err)
			}
			value, err := ParseJSON(head.Checkpoint)
			if err != nil || CanonicalJSON(value) != string(head.Checkpoint) {
				t.Fatal("checkpoint bytes stopped being canonical")
			}
			var cp contract.Checkpoint
			if err = json.Unmarshal(head.Checkpoint, &cp); err != nil {
				t.Fatal(err)
			}
			saved := cp.Durable.Children[0].Checkpoint
			rawSaved, _ := json.Marshal(saved)
			if !ValidRecord(MustParseJSON(string(rawSaved))) || *saved.RetainUntil != *child.RetainUntil {
				t.Fatal("converted child header or retention invalid")
			}
			l := retainedLedger()
			if err = l.Restore(saved.Context); err != nil {
				t.Fatal(err)
			}
			if string(l.Snapshot().Entries[0].Value) != *retainedFixture(t, false).Context.Entries[0].ValueJSON {
				t.Fatal("nested source order lost after canonical resume")
			}
		})
	}
}
func TestRetainedRepresentationDuplicationKeepsCheckpointLimit(t *testing.T) {
	ctx := context.Background()
	root := writerCheckpoint("retained-limit")
	text := strings.Repeat("x", 2200000)
	encoded := String(text).Text()
	root.Context.Entries = []contract.CheckpointRetainedEntry{{ID: root.Context.Scope + ":large", Kind: "artifact", Label: "Large", Value: json.RawMessage(encoded), Bytes: int64(UTF8Bytes(encoded)), Digest: ContextDigest(encoded)}}
	store := NewMemoryRunStore(MemoryRunStoreOptions{})
	writer, binding := writerFixture(t, store, root.ID, false)
	err := writer.Save(ctx, *binding, root)
	var refused *ContinuationNotRecoverable
	head, _ := store.Load(ctx, root.ID)
	if !errors.As(err, &refused) || head != nil {
		t.Fatal("companion duplication escaped 4MiB bound", err)
	}
}

func TestRetainedRepresentationRootGuardCannotPreserveUnredactedText(t *testing.T) {
	ctx := context.Background()
	root := writerCheckpoint("retained-guard")
	raw := `[{"role":"assistant","content":"before guard"}]`
	root.Context.Entries = []contract.CheckpointRetainedEntry{{ID: root.Context.Scope + ":guarded", Kind: "history", Label: "Guarded", Value: json.RawMessage(raw), Bytes: int64(UTF8Bytes(raw)), Digest: ContextDigest(raw)}}
	store := NewMemoryRunStore(MemoryRunStoreOptions{})
	writer, err := NewRunWriter(store, root.ID, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	writer.Bind(func() (contract.Checkpoint, error) { return root, nil }, RecordDefaults, func() RecordGuard { return retainedPairGuard{"redact-root"} })
	err = writer.Save(ctx, RunBinding{Writer: writer, Scope: DurableScope{Execution: root.ID, Role: "author"}, Root: true}, root)
	var refused *ContinuationNotRecoverable
	head, _ := store.Load(ctx, root.ID)
	if !errors.As(err, &refused) || head != nil {
		t.Fatal("pre-guard companion text survived redaction", err)
	}
}
func TestRetainedRepresentationCorruptedLegacyCannotBecomeCompanion(t *testing.T) {
	ctx := context.Background()
	root := retainedFixture(t, true)
	root.Context.Entries[0].Value = json.RawMessage(`{"changed":true}`)
	store := NewMemoryRunStore(MemoryRunStoreOptions{})
	writer, err := NewRunWriter(store, root.ID, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	writer.Bind(func() (contract.Checkpoint, error) { return root, nil }, RecordDefaults, nil)
	err = writer.Save(ctx, RunBinding{Writer: writer, Scope: DurableScope{Execution: root.ID, Role: "author"}, Root: true}, root)
	var refused *ContinuationNotRecoverable
	head, _ := store.Load(ctx, root.ID)
	if !errors.As(err, &refused) || head != nil {
		t.Fatal("corrupt legacy evidence acquired a strict companion", err)
	}
}

func TestRetainedRepresentationChildHeaderConversion(t *testing.T) {
	for _, version := range []int64{1, 2} {
		t.Run(string(rune('0'+version)), func(t *testing.T) {
			ctx := context.Background()
			root := writerCheckpoint("root-conversion")
			child := retainedFixture(t, true)
			child.ID = "child-one"
			child = retainedHeader(t, child, version)
			before, _ := json.Marshal(child)
			store := NewMemoryRunStore(MemoryRunStoreOptions{})
			writer, _ := NewRunWriter(store, root.ID, nil, nil)
			writer.Bind(func() (contract.Checkpoint, error) { return root, nil }, RecordDefaults, nil)
			if err := writer.Save(ctx, RunBinding{Writer: writer, Scope: DurableScope{Execution: child.ID, Role: "author", Task: "task-one"}}, child); err != nil {
				t.Fatal(err)
			}
			head, _ := store.Load(ctx, root.ID)
			var saved contract.Checkpoint
			if err := json.Unmarshal(head.Checkpoint, &saved); err != nil {
				t.Fatal(err)
			}
			converted := saved.Durable.Children[0].Checkpoint
			raw, _ := json.Marshal(converted)
			if converted.Version != 2 || !ValidRecord(MustParseJSON(string(raw))) || *converted.RetainUntil != *child.RetainUntil || converted.Context.Entries[0].Digest != child.Context.Entries[0].Digest || converted.Context.Entries[0].Bytes != child.Context.Entries[0].Bytes {
				t.Fatal("child conversion changed evidence or retention")
			}
			after, _ := json.Marshal(child)
			if string(before) != string(after) {
				t.Fatal("original child record mutated")
			}
			if err := retainedLedger().Restore(converted.Context); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestRetainedRepresentationInvalidChildCannotBeResigned(t *testing.T) {
	for _, mode := range []string{"digest", "unsupported-version", "missing-header", "scope"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			root := writerCheckpoint("root-invalid")
			child := retainedFixture(t, true)
			child.ID = "child-one"
			child = retainedHeader(t, child, 2)
			switch mode {
			case "digest":
				bad := "sha256:" + strings.Repeat("0", 64)
				child.Digest = &bad
			case "unsupported-version":
				child.Version = 4
				raw, err := json.Marshal(child)
				if err != nil {
					t.Fatal(err)
				}
				digest := RecordDigest(MustParseJSON(string(raw)))
				child.Digest = &digest
			case "missing-header":
				child.Schema = nil
				child.Digest = nil
				child.RetainUntil = nil
			}
			store := NewMemoryRunStore(MemoryRunStoreOptions{})
			writer, _ := NewRunWriter(store, root.ID, nil, nil)
			writer.Bind(func() (contract.Checkpoint, error) { return root, nil }, RecordDefaults, nil)
			id := child.ID
			if mode == "scope" {
				id = "different-child"
			}
			err := writer.Save(ctx, RunBinding{Writer: writer, Scope: DurableScope{Execution: id, Role: "author", Task: "task-one"}}, child)
			var refused *ContinuationNotRecoverable
			head, _ := store.Load(ctx, root.ID)
			if !errors.As(err, &refused) || head != nil {
				t.Fatal("invalid child record was re-signed into authority", err)
			}
		})
	}
}
