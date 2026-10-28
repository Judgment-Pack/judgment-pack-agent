package runcontext

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

const compactionExact = `{ "z":false, "zero":0, "escaped":"\u0061" }`

func compactionFixture(id string) ([]Message, []Message) {
	call := ToolCallPart(id, "check", ObjectOf("flag", Bool(false), "zero", Number(0), "exact", String(compactionExact)))
	result := ToolResultPart(id, "check", Output{Type: "json", Value: ObjectOf("ok", Bool(false), "count", Number(0))})
	public := []Message{PartsMessage("assistant", call), PartsMessage("tool", result)}
	call.ProviderOptions = ObjectOf("google", ObjectOf("thoughtSignature", String(strings.Repeat("TRANSPORT", 9000))))
	result.ProviderOptions = ObjectOf("google", ObjectOf("thoughtSignature", String("OTHER-SIGNATURE")))
	return []Message{PartsMessage("assistant", ReasoningPart("PRIVATE-REASONING"), call), PartsMessage("tool", result)}, public
}
func compactionSession(store RunStore, mode string, resume bool, compactor func(context.Context, CompactorRequest) (string, error)) Session {
	session := smallWindowSession()
	session.Policy = PolicyLayer{"compaction": mode}
	session.Context.RunStore = &DurableRunInput{Store: store, ID: "compaction", Resume: resume}
	session.Context.Configuration.Models["small"] = ModelBudget{ContextWindowTokens: 8192, OutputTokens: 1024}
	session.Context.Compactor = compactor
	return session
}
func compactionHistory() []Message {
	block, _ := compactionFixture("one")
	messages := append([]Message{TextMessage("user", "Task")}, block...)
	for i := 0; i < 16; i++ {
		messages = append(messages, TextMessage("assistant", strings.Repeat("earlier public discussion ", 35)), TextMessage("user", "continue"))
	}
	return messages
}
func compactionRows(t *testing.T, store HistoryRunStore) (*RetainedRunHistory, []*Value, []*Value) {
	t.Helper()
	view, rows := historyWriterRows(t, store, "compaction")
	originals, compactions := []*Value{}, []*Value{}
	for _, row := range rows {
		if row.Get("kind").Str() == "original" && row.Get("format").Str() == "messages" {
			originals = append(originals, row)
		}
		if row.Get("kind").Str() == "compaction" {
			compactions = append(compactions, row)
		}
	}
	return view, originals, compactions
}
func compactionSource(t *testing.T, view *RetainedRunHistory, value *Value) string {
	t.Helper()
	raw, err := view.ReadObject(liveHistoryRef(value))
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := ReadHistoryChunkManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	full := []byte{}
	for _, part := range manifest.Parts {
		raw, err = view.ReadObject(part)
		if err != nil {
			t.Fatal(err)
		}
		full = append(full, raw...)
	}
	return string(full)
}
func TestHistoryCompactionActualPublicSpanAndReplacement(t *testing.T) {
	for _, mode := range []string{"index", "summary"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			store := NewMemoryRunStore(MemoryRunStoreOptions{})
			calls := 0
			run, err := ContextualSession(ctx, compactionSession(store, mode, false, func(context.Context, CompactorRequest) (string, error) { calls++; return "Public summary", nil }))
			if err != nil {
				t.Fatal(err)
			}
			history := compactionHistory()
			projected, err := run.Context.MessagesContext(ctx, history, "host", noTools())
			if err != nil {
				t.Fatal(err)
			}
			view, originals, compactions := compactionRows(t, store)
			if len(originals) != 1 || len(compactions) != 1 {
				t.Fatalf("missing actual source/replacement observations: %d/%d", len(originals), len(compactions))
			}
			source := compactionSource(t, view, originals[0].Get("value"))
			consumed, err := ParseJSON([]byte(source))
			if err != nil {
				t.Fatal(err)
			}
			for _, message := range projected[2:] {
				consumed.Append(message.Value())
			}
			_, public := compactionFixture("one")
			want := append(public, history[3:]...)
			if consumed.Text() != MessagesValue(want).Text() {
				t.Fatal("consumed public span was lost or altered")
			}
			if strings.Contains(source, "PRIVATE-REASONING") || strings.Contains(source, "thoughtSignature") || strings.Contains(source, "TRANSPORT") || strings.Contains(source, "OTHER-SIGNATURE") {
				t.Fatal("private reasoning or transport metadata retained")
			}
			row := compactions[0]
			if compactionSource(t, view, row.Get("replacement")) != projected[1].Value().Text() || row.Get("mode").Str() != mode || row.Get("prior").Len() != 0 || row.Get("boundary").Str() != run.Context.Ledger.Scope() || liveHistoryScope(row.Get("scope")) != run.Context.Persistence("author").Scope {
				t.Fatal("replacement identity, mode or provenance changed")
			}
			wantCalls := 0
			if mode == "summary" {
				wantCalls = 1
			}
			if calls != wantCalls || view.Head().Coverage != "recording-incomplete" {
				t.Fatal("compactor call count or coverage changed", calls)
			}
		})
	}
}
func TestHistoryCompactionRepeatedResumeRetainsAllSourcesWithoutInventedEdges(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryRunStore(MemoryRunStoreOptions{})
	run, err := ContextualSession(ctx, compactionSession(store, "index", false, nil))
	if err != nil {
		t.Fatal(err)
	}
	history := []Message{TextMessage("user", "Task")}
	replacements := []Message{}
	for i := 0; i < 3; i++ {
		var previous *Value
		if len(history) > 1 {
			previous = history[1].Value()
		}
		block, _ := compactionFixture("call-" + itoa(i))
		history = append(history, block...)
		history, err = run.Context.MessagesContext(ctx, history, "host", noTools())
		if err != nil {
			t.Fatal(err)
		}
		replacements = append(replacements, history[1])
		state, err := run.Context.Snapshot("")
		if err != nil {
			t.Fatal(err)
		}
		binding := run.Context.Persistence("author")
		if err = binding.Writer.Save(ctx, *binding, state); err != nil {
			t.Fatal(err)
		}
		view, originals, compactions := compactionRows(t, store)
		if len(originals) != i+1 || len(compactions) != i+1 {
			t.Fatal("repeated compaction lost recorded members")
		}
		for j, row := range compactions {
			if row.Get("prior").Len() != 0 {
				t.Fatal("text equality invented an occurrence edge")
			}
			if compactionSource(t, view, row.Get("replacement")) != replacements[j].Value().Text() {
				t.Fatal("older replacement was lost")
			}
		}
		if previous != nil {
			consumed, err := ParseJSON([]byte(compactionSource(t, view, originals[len(originals)-1].Get("value"))))
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, message := range consumed.Items() {
				found = found || message.Text() == previous.Text()
			}
			if !found {
				t.Fatal("summary-looking input was dropped")
			}
		}
		if i == 0 {
			run.Context.Close()
			run, err = ContextualSession(ctx, compactionSession(store, "index", true, nil))
			if err != nil {
				t.Fatal(err)
			}
		}
	}
}

type compactionFaultStore struct {
	*MemoryRunStore
	phase, fault string
	faults       int
}

func (s *compactionFaultStore) CommitHistory(ctx context.Context, input RunHistoryCommit) (RunHistoryReceipt, error) {
	receipt, err := s.MemoryRunStore.CommitHistory(ctx, input)
	if err != nil {
		return receipt, err
	}
	for _, ref := range input.Observations {
		page, err := s.MemoryRunStore.ReadHistoryObject(ctx, HistoryResource{Kind: "run", ID: input.Run.RunID}, receipt.History.Revision, ref.Digest, 0, HistoryObjectBytes)
		if err != nil {
			return receipt, err
		}
		row, err := ReadHistoryObservation(page.Value)
		if err != nil {
			return receipt, err
		}
		if s.phase == "original" && row.Get("kind").Str() == "original" && row.Get("format").Str() == "messages" || s.phase == "replacement" && row.Get("kind").Str() == "compaction" {
			s.faults++
			if s.fault == "lost" {
				return receipt, errors.New("lost publication acknowledgement")
			}
			receipt.History = RunHistoryHead{}
			break
		}
	}
	return receipt, nil
}
func TestHistoryCompactionAcknowledgementBeforeReplacementAndDispatch(t *testing.T) {
	for _, phase := range []string{"original", "replacement"} {
		for _, fault := range []string{"missing", "lost"} {
			t.Run(phase+"/"+fault, func(t *testing.T) {
				ctx := context.Background()
				store := &compactionFaultStore{MemoryRunStore: NewMemoryRunStore(MemoryRunStoreOptions{}), phase: phase, fault: fault}
				compactorCalls, physical, used := 0, 0, false
				run, err := ContextualSession(ctx, compactionSession(store, "summary", false, func(context.Context, CompactorRequest) (string, error) {
					compactorCalls++
					return "Public summary", nil
				}))
				if err != nil {
					t.Fatal(err)
				}
				attempt := func() error {
					projected, err := run.Context.MessagesContext(ctx, compactionHistory(), "host", noTools())
					if err != nil {
						return err
					}
					used = true
					_, err = InvokeDurableModel(ctx, run.Context.Persistence("author"), "provider", "author", []byte(MessagesValue(projected).Text()), func([]byte) (*http.Response, error) { physical++; return nil, errors.New("physical author transport") }, nil)
					return err
				}
				if err = attempt(); err == nil || used || physical != 0 {
					t.Fatal("unacknowledged replacement was exposed or dispatched", err, used, physical)
				}
				wantCalls := 0
				if phase == "replacement" {
					wantCalls = 1
				}
				if compactorCalls != wantCalls {
					t.Fatal("compactor crossed original acknowledgement", compactorCalls)
				}
				if err = attempt(); err == nil || store.faults != 1 || compactorCalls != wantCalls {
					t.Fatal("uncertain publication or physical work repeated", err, store.faults, compactorCalls)
				}
			})
		}
	}
}

type compactionPlainStore struct {
	RunStore
	writes int
}

func (s *compactionPlainStore) Commit(ctx context.Context, input RunCommit) (RunReceipt, error) {
	s.writes++
	return s.RunStore.Commit(ctx, input)
}
func TestHistoryCompactionAbsentCapabilityPreservesOrdinaryFallback(t *testing.T) {
	store := &compactionPlainStore{RunStore: NewMemoryRunStore(MemoryRunStoreOptions{})}
	ctx := context.Background()
	run, err := ContextualSession(ctx, compactionSession(store, "summary", false, func(context.Context, CompactorRequest) (string, error) {
		return "", errors.New("ordinary compactor failure")
	}))
	if err != nil {
		t.Fatal(err)
	}
	store.writes = 0 // Admission already acknowledged its ordinary startup checkpoint.
	projected, err := run.Context.MessagesContext(ctx, compactionHistory(), "host", noTools())
	if err != nil || !strings.Contains(MessagesValue(projected).Text(), "Completed tool calls must not be repeated") || store.writes != 0 {
		t.Fatal("optional history absence changed compaction/fallback", err, store.writes)
	}
}
