package runcontext

// #343: a durable host or runtime call whose result the record guard redacts (ADR-0018) is recorded in
// that redacted form and answered with the same form, so the claim settles, the record and what the
// model is given agree, and the effect is never run again. The fixtures name a temp-folder segment
// inside a URI's path, as the issue's witness does: the pattern keeps matching there by the
// maintainer's decision. recordedResults.test.ts holds the TypeScript; the runs through both Go
// adapters are go/adapter/durableapprovaltests/recorded_results_test.go.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

const (
	recordedRaw      = "https://packs.example/tmp/evaluation-pack"
	recordedRedacted = "https://packs.example[redacted: local-temp-path]"
	recordedRawPath  = "packs.example/tmp/"
)

// recordedPack is the result a call answered with, naming the pack id twice as the witness does: in
// its text and in its structured content.
func recordedPack(id string) string {
	text, _ := json.Marshal(`{"packId":"` + id + `","status":"evaluated"}`)
	return `{"content":[{"type":"text","text":` + string(text) + `}],"structuredContent":{"packId":"` + id + `","status":"evaluated"}}`
}

// recordedWriter is a writer bound to a real output guard built from members, under policy.
func recordedWriter(t *testing.T, store RunStore, id string, resume bool, policy RecordsPolicy, members PolicyMembers) (*RunBinding, *OutputGuard) {
	t.Helper()
	writer, binding := writerFixture(t, store, id, resume)
	counts := map[string]int64{}
	guard, err := NewOutputGuard(members, func() map[string]int64 { return counts })
	if err != nil {
		t.Fatal(err)
	}
	state := writerCheckpoint(id)
	if saved := writer.Checkpoint(); saved != nil {
		state = *saved
	}
	writer.Bind(func() (contract.Checkpoint, error) { return state, nil }, policy, func() RecordGuard { return guard })
	return binding, guard
}

// recordedRow is the stored run's one host row, and the stored checkpoint's text.
func recordedRow(t *testing.T, store RunStore, id string) (*Value, string) {
	t.Helper()
	head, err := store.Load(context.Background(), id)
	if err != nil || head == nil {
		t.Fatalf("no stored run: %v", err)
	}
	rows := MustParseJSON(string(head.Checkpoint)).Get("durable").Get("dispatches").Items()
	if len(rows) != 1 {
		t.Fatalf("%d rows", len(rows))
	}
	return rows[0], string(head.Checkpoint)
}

func TestARedactedResultIsRecordedAndAnsweredInTheSameForm(t *testing.T) {
	ctx := context.Background()
	want := recordedPack(recordedRedacted)
	for _, read := range []bool{false, true} {
		name := map[bool]string{false: "an act", true: "a read"}[read]
		t.Run(name, func(t *testing.T) {
			store := NewMemoryRunStore(MemoryRunStoreOptions{})
			binding, guard := recordedWriter(t, store, "recorded", false, RecordDefaults, nil)
			calls := 0
			execute := func([]byte) (ToolResult, error) {
				calls++
				return ReadToolResult(MustParseJSON(recordedPack(recordedRaw))), nil
			}
			got, err := InvokeDurable(ctx, binding, "host", "source", "evaluate", []byte(`{"pack":"evaluation-pack"}`), read, execute, nil, nil)
			if err != nil || string(got.JSON()) != want {
				t.Fatalf("answered %s, %v; want %s", got.JSON(), err, want)
			}
			row, stored := recordedRow(t, store, "recorded")
			if row.Get("state").Str() != "result" || CanonicalJSON(row.Get("result")) != CanonicalJSON(MustParseJSON(want)) {
				t.Fatalf("recorded %s", row.Text())
			}
			if strings.Contains(stored, recordedRawPath) {
				t.Fatal("the stored checkpoint holds the redacted text")
			}
			// The storage pass's channel: no delivered-output count or notice (ADR-0017) is made of it.
			if len(guard.Counts()) != 0 || len(guard.TakeNotices()) != 0 {
				t.Fatalf("counted %v", guard.Counts())
			}
			resumed, _ := recordedWriter(t, store, "recorded", true, RecordDefaults, nil)
			again, err := InvokeDurable(ctx, resumed, "host", "source", "evaluate", []byte(`{"pack":"evaluation-pack"}`), read, execute, nil, nil)
			if err != nil || CanonicalJSON(MustParseJSON(string(again.JSON()))) != CanonicalJSON(MustParseJSON(want)) {
				t.Fatalf("resumed answer %s, %v", again.JSON(), err)
			}
			// An act is answered from its record and never runs again; a read runs again, answered the same way.
			if ran := map[bool]int{false: 1, true: 2}[read]; calls != ran {
				t.Fatalf("ran %d times, want %d", calls, ran)
			}
		})
	}
}

func TestAResultWithNothingRedactedIsKeptByteForByte(t *testing.T) {
	ctx := context.Background()
	// Not sorted, with a member the result type does not name, and a path the patterns do not match.
	given := `{"structuredContent":{"status":"evaluated","packId":"https://packs.example/var/evaluation-pack"},"content":[{"type":"text","text":"evaluated"}],"opaque":{"z":[null,0],"a":"kept"},"isError":false}`
	binding, _ := recordedWriter(t, NewMemoryRunStore(MemoryRunStoreOptions{}), "unchanged", false, RecordDefaults, nil)
	got, err := InvokeDurable(ctx, binding, "host", "source", "evaluate", []byte(`{}`), false, func([]byte) (ToolResult, error) { return ReadToolResult(MustParseJSON(given)), nil }, nil, nil)
	if err != nil || string(got.JSON()) != given {
		t.Fatalf("answered %s, %v", got.JSON(), err)
	}
}

func TestARedactedResultKeepsItsMemberOrderAndReceipt(t *testing.T) {
	ctx := context.Background()
	order := func(id string) string {
		text, _ := json.Marshal(id)
		return `{"structuredContent":{"status":"evaluated","packId":` + string(text) + `},"content":[{"type":"text","text":` + string(text) + `}]}`
	}
	binding, _ := recordedWriter(t, NewMemoryRunStore(MemoryRunStoreOptions{}), "ordered", false, RecordDefaults, nil)
	got, err := InvokeDurable(ctx, binding, "host", "source", "evaluate", []byte(`{}`), true, func([]byte) (ToolResult, error) {
		held := ReadToolResult(MustParseJSON(order(recordedRaw)))
		held.Receipt = "receipt-7"
		return held, nil
	}, nil, nil)
	if err != nil || string(got.JSON()) != order(recordedRedacted) || got.Receipt != "receipt-7" {
		t.Fatalf("answered %s (receipt %q), %v", got.JSON(), got.Receipt, err)
	}
}

// The source gate answers an external read with its receipt check's own copy of the result. That copy
// is given the recorded form as well, so the raw text cannot reach the model that way.
func TestAnExternalReadsCheckedResultIsGivenTheRecordedForm(t *testing.T) {
	binding, _ := recordedWriter(t, NewMemoryRunStore(MemoryRunStoreOptions{}), "checked", false, RecordDefaults, nil)
	admission := &receiptAdmission{check: func(result ToolResult) (ToolResult, bool) {
		receipt := result.Receipt
		result = result.Clone()
		result.Receipt = receipt
		return result, result.Receipt == "receipt-7"
	}}
	ctx := context.WithValue(context.Background(), receiptAdmissionKey{}, admission)
	got, err := invokeDurable(ctx, binding, "host", "source", "read_record", []byte(`{}`), true, func([]byte) (ToolResult, error) {
		held := ReadToolResult(MustParseJSON(recordedPack(recordedRaw)))
		held.Receipt = "receipt-7"
		return held, nil
	}, admission.admit, nil, nil)
	checked, verified, ran := admission.outcome()
	want := recordedPack(recordedRedacted)
	if err != nil || !ran || !verified || string(got.JSON()) != want || string(checked.JSON()) != want || checked.Receipt != "receipt-7" {
		t.Fatalf("answered %s, checked %s (receipt %q, verified %v), %v", got.JSON(), checked.JSON(), checked.Receipt, verified, err)
	}
}

// Through the real gate and read wrapper: what the call answers, what the chat retains and what the run
// stores all hold the redacted form, and the receipt was verified against the result the source gave.
func TestAnExternalReadThroughTheGateAnswersTheRecordedForm(t *testing.T) {
	vector := readJSON(t, quarantineVectorFile)
	fixture := vector.Get("fixture")
	wire := fixture.Get("wireName").Str()
	chat := NewChatContext(fixture.Get("scope").Str())
	store := NewMemoryRunStore(MemoryRunStoreOptions{})
	calls := &quarantineCalls{}
	result := MustParseJSON(recordedPack(recordedRaw))
	tool := quarantineTool(t, fixture.Get("declaration"), result, "rcpt-e0", "accepts its receipt for its result", calls)
	run := openRun(t, quarantineSession(chat, store, []HostTool{tool}, 0))
	defer run.Context.Close()
	got, err := hostTool(t, run, wire).Execute(WithCallID(context.Background(), "call-e0"), fixture.Get("arguments").JSON())
	if err != nil || CanonicalJSON(MustParseJSON(string(got.JSON()))) != CanonicalJSON(MustParseJSON(recordedPack(recordedRedacted))) {
		t.Fatalf("answered %s, %v", got.JSON(), err)
	}
	if calls.executions.Load() != 1 || calls.verifications.Load() != 1 {
		t.Fatalf("executions %d, verifications %d", calls.executions.Load(), calls.verifications.Load())
	}
	if snapshot, _ := chat.Snapshot(); strings.Contains(string(snapshot), recordedRawPath) {
		t.Fatal("the chat retained the redacted text")
	}
	if text := storedBytes(t, store); strings.Contains(text, recordedRawPath) || !strings.Contains(text, recordedRedacted) {
		t.Fatal("the store does not hold the redacted form alone")
	}
}

// What cannot be recorded still ends unknown, and nothing runs again: a refusing guard, or a store that
// does not take the result.
func TestARedactedResultThatCannotBeRecordedIsStillUnknown(t *testing.T) {
	ctx := context.Background()
	execute := func(calls *int) func([]byte) (ToolResult, error) {
		return func([]byte) (ToolResult, error) {
			*calls++
			return ReadToolResult(MustParseJSON(recordedPack(recordedRaw))), nil
		}
	}
	refuse := PolicyMembers{"outputGuard": {Value: "refuse"}}
	t.Run("a refusing guard", func(t *testing.T) {
		store := NewMemoryRunStore(MemoryRunStoreOptions{})
		binding, guard := recordedWriter(t, store, "refused", false, RecordDefaults, refuse)
		calls := 0
		got, err := InvokeDurable(ctx, binding, "host", "source", "evaluate", []byte(`{}`), false, execute(&calls), nil, nil)
		var unknown *StorageOutcomeUnknown
		if !errors.As(err, &unknown) || calls != 1 || got.Content != nil {
			t.Fatalf("answered %s, %v", got.JSON(), err)
		}
		notices := guard.TakeNotices()
		if len(notices) != 1 || notices[0].Guardrail == nil || notices[0].Guardrail.Tool != "records" || notices[0].Guardrail.Detail != "Output refused: local-temp-path." {
			t.Fatalf("notices %+v", notices)
		}
		row, stored := recordedRow(t, store, "refused")
		if row.Get("state").Str() != "claimed" || row.Has("result") || strings.Contains(stored, recordedRawPath) {
			t.Fatalf("recorded %s", row.Text())
		}
		resumed, _ := recordedWriter(t, store, "refused", true, RecordDefaults, refuse)
		_, err = InvokeDurable(ctx, resumed, "host", "source", "evaluate", []byte(`{}`), false, execute(&calls), nil, nil)
		var host *HostOutcomeUnknown
		if !errors.As(err, &host) || calls != 1 {
			t.Fatalf("resumed: ran %d, %v", calls, err)
		}
	})
	// The checked result is not given the form when the result itself could not be: one refusal, one notice.
	t.Run("a refusing guard, an external read", func(t *testing.T) {
		binding, guard := recordedWriter(t, NewMemoryRunStore(MemoryRunStoreOptions{}), "refused-read", false, RecordDefaults, refuse)
		admission := &receiptAdmission{check: func(result ToolResult) (ToolResult, bool) { return result.Clone(), true }}
		held := context.WithValue(ctx, receiptAdmissionKey{}, admission)
		calls := 0
		_, err := invokeDurable(held, binding, "host", "source", "read_record", []byte(`{}`), true, execute(&calls), admission.admit, nil, nil)
		var unknown *StorageOutcomeUnknown
		if !errors.As(err, &unknown) || calls != 1 {
			t.Fatalf("ran %d, %v", calls, err)
		}
		if notices := guard.TakeNotices(); len(notices) != 1 || notices[0].Guardrail == nil || notices[0].Guardrail.Tool != "records" {
			t.Fatalf("notices %+v", notices)
		}
	})
	t.Run("a store that does not take the result", func(t *testing.T) {
		memory := NewMemoryRunStore(MemoryRunStoreOptions{})
		commits := 0
		store := writerReceiptStore{RunStore: memory, write: func(ctx context.Context, in RunCommit) (RunReceipt, error) {
			if commits++; commits == 2 {
				return RunReceipt{}, errors.New("private host storage detail")
			}
			return memory.Commit(ctx, in)
		}}
		binding, _ := recordedWriter(t, store, "lost", false, RecordDefaults, nil)
		calls := 0
		_, err := InvokeDurable(ctx, binding, "host", "source", "evaluate", []byte(`{}`), false, execute(&calls), nil, nil)
		var unknown *StorageOutcomeUnknown
		if !errors.As(err, &unknown) || calls != 1 {
			t.Fatalf("ran %d, %v", calls, err)
		}
		resumed, _ := recordedWriter(t, memory, "lost", true, RecordDefaults, nil)
		_, err = InvokeDurable(ctx, resumed, "host", "source", "evaluate", []byte(`{}`), false, execute(&calls), nil, nil)
		var host *HostOutcomeUnknown
		if !errors.As(err, &host) || calls != 1 {
			t.Fatalf("resumed: ran %d, %v", calls, err)
		}
	})
}

// With redaction on store off, the host keeps results as they are: recorded and answered unredacted.
func TestRedactionOnStoreOffKeepsTheResult(t *testing.T) {
	policy := RecordDefaults
	policy.RedactOnStore = false
	binding, _ := recordedWriter(t, NewMemoryRunStore(MemoryRunStoreOptions{}), "kept", false, policy, nil)
	got, err := InvokeDurable(context.Background(), binding, "host", "source", "evaluate", []byte(`{}`), false, func([]byte) (ToolResult, error) {
		return ReadToolResult(MustParseJSON(recordedPack(recordedRaw))), nil
	}, nil, nil)
	if err != nil || string(got.JSON()) != recordedPack(recordedRaw) {
		t.Fatalf("answered %s, %v", got.JSON(), err)
	}
}

// Under repeat tools the row's exact result text is the recorded form's, so the repeat key holds and a
// resumed run admits the row.
func TestARedactedResultsRepeatTextIsTheRecordedForm(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryRunStore(MemoryRunStoreOptions{})
	binding, _ := recordedWriter(t, store, "repeated", false, RecordDefaults, nil)
	attach204(t, binding, 5)
	execute := func([]byte) (ToolResult, error) { return ReadToolResult(MustParseJSON(recordedPack(recordedRaw))), nil }
	if _, err := InvokeDurable(ctx, binding, "host", "source", "lookup", []byte(`{"n":1}`), true, execute, nil, nil); err != nil {
		t.Fatal(err)
	}
	row, _ := recordedRow(t, store, "repeated")
	if row.Get("repeatResultJson").Str() != recordedPack(recordedRedacted) {
		t.Fatalf("repeat text %s", row.Get("repeatResultJson").Str())
	}
	resumed, _ := recordedWriter(t, store, "repeated", true, RecordDefaults, nil)
	attach204(t, resumed, 5)
	if _, err := InvokeDurable(ctx, resumed, "host", "source", "lookup", []byte(`{"n":1}`), true, execute, nil, nil); err != nil {
		t.Fatal(err)
	}
	if count := resumed.Writer.RepeatState(*resumed).Count; count != 1 {
		t.Fatalf("repeat count %d", count)
	}
}

// Members whose names the guard rewrote follow the others in the sorted order of the names they had, as
// TypeScript's guard leaves them (recordedResults.test.ts holds the same object): "a/tmp/x" sorts before
// "aZ/tmp/x", though "aZ[redacted: …]" sorts before "a[redacted: …]".
func TestARedactedResultsRenamedMembersFollowAsTypeScriptLeavesThem(t *testing.T) {
	given := `{"content":[{"type":"text","text":"ok"}],"structuredContent":{"z":1,"aZ/tmp/x":2,"m":3,"a/tmp/x":4}}`
	want := `{"content":[{"type":"text","text":"ok"}],"structuredContent":{"z":1,"m":3,"a[redacted: local-temp-path]":4,"aZ[redacted: local-temp-path]":2}}`
	binding, _ := recordedWriter(t, NewMemoryRunStore(MemoryRunStoreOptions{}), "renamed", false, RecordDefaults, nil)
	got, err := InvokeDurable(context.Background(), binding, "host", "source", "evaluate", []byte(`{}`), true, func([]byte) (ToolResult, error) {
		return ReadToolResult(MustParseJSON(given)), nil
	}, nil, nil)
	if err != nil || string(got.JSON()) != want {
		t.Fatalf("answered %s, %v", got.JSON(), err)
	}
}

// A job's start is answered from its job's record (ADR-0025), not from what its tool said: its answer is the
// record's projection, which holds none of the tool's text, so it settles whatever the tool answered, and the
// writer leaves it as the record gave it.
func TestAJobStartIsAnsweredFromItsRecordWhateverItsToolSaid(t *testing.T) {
	ctx := context.Background()
	store := durableJobStore{NewMemoryRunStore(MemoryRunStoreOptions{})}
	lease, err := store.AcquireLease(ctx, LeaseAcquire{Resource: "run-start", Owner: "worker-a", DurationMs: 3600000})
	if err != nil {
		t.Fatal(err)
	}
	view, err := UnattendedRunStore(store, FenceOf(lease), NewReferenceEffectHost(store))
	if err != nil {
		t.Fatal(err)
	}
	binding, _ := recordedWriter(t, view, "run-start", false, RecordDefaults, nil)
	binding.Scope = DurableScope{Execution: "run-start", Role: "author"}
	dispatch := &jobStartDispatch{mint: func(context.Context, string) (string, error) {
		return JobKeyDigest("kit.job-start.Q7z2Xm9LpV4cR8tNb3Yh6Df1")
	}}
	calls := 0
	got, err := InvokeDurable(context.WithValue(ctx, jobStartKey{}, dispatch), binding, "host", "jobs", "start", []byte(`{"q":1}`), false, func([]byte) (ToolResult, error) {
		calls++
		dispatch.answer = &JobStartAnswer{Started: true, Handle: recordedRaw}
		return ResultOf([]Content{TextContent("started at " + recordedRaw)}, nil), nil
	}, nil, nil)
	if err != nil || calls != 1 || strings.Contains(string(got.JSON()), recordedRawPath) || !strings.Contains(string(got.JSON()), `\"state\":\"started\"`) {
		t.Fatalf("answered %s, ran %d, %v", got.JSON(), calls, err)
	}
	if head, err := store.Load(ctx, "run-start"); err != nil || head == nil || strings.Contains(string(head.Checkpoint), recordedRawPath) {
		t.Fatalf("the stored run: %v", err)
	}
}
