package runcontext

// #352: a durable call's failure is recorded with the form the record guard (ADR-0018) leaves of its
// detail alone, read whole and then cut to the thousand UTF-16 code units a detail may hold, and the call
// fails with that same form, so the record and what the model is given agree. A model's failure is stored in
// that form. A guard refusal is an unknown outcome, and nothing of the failure is stored.
// recordedFailures.test.ts holds the TypeScript.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
)

const (
	recordedFailureRaw      = "Could not read " + recordedRaw + " today"
	recordedFailureRedacted = "Could not read " + recordedRedacted + " today"
)

func failingCall(cause error, calls *int) func([]byte) (ToolResult, error) {
	return func([]byte) (ToolResult, error) {
		*calls++
		return ToolResult{}, cause
	}
}

func TestADurableCallsFailureIsRecordedAndDeliveredInTheRecordedForm(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryRunStore(MemoryRunStoreOptions{})
	binding, guard := recordedWriter(t, store, "failed", false, RecordDefaults, nil)
	cause := failure.Authored("tool-failed", recordedFailureRaw, "tool")
	calls := 0
	_, err := InvokeDurable(ctx, binding, "host", "source", "evaluate", []byte(`{}`), false, failingCall(cause, &calls), nil, nil)
	// The call fails with the recorded form, and the cause is still what it wraps.
	if err == nil || err.Error() != recordedFailureRedacted || !errors.Is(err, cause) || calls != 1 {
		t.Fatalf("failed with %v after %d calls", err, calls)
	}
	// What the model reads of it.
	text, structured := failure.ToolFailure(err)
	want := `{"failure":{"code":"tool-failed","detail":"Could not read https://packs.example[redacted: local-temp-path] today","outcome":"unknown","source":"tool","version":1}}`
	if text != recordedFailureRedacted || CanonicalJSON(MustParseJSON(string(structured))) != want {
		t.Fatalf("the model reads %q, %s", text, structured)
	}
	row, stored := recordedRow(t, store, "failed")
	if row.Get("state").Str() != "unknown" || "{\"failure\":"+CanonicalJSON(row.Get("failure"))+"}" != want || strings.Contains(stored, recordedRawPath) {
		t.Fatalf("recorded %s", row.Text())
	}
	// The storage pass's channel: no delivered-output count or notice (ADR-0017) is made of it.
	if len(guard.Counts()) != 0 || len(guard.TakeNotices()) != 0 {
		t.Fatalf("counted %v", guard.Counts())
	}
}

// A detail the guard leaves as it is: the call fails with its own error, and the row holds the detail.
func TestADurableCallsFailureWithNothingRedactedIsItsOwn(t *testing.T) {
	store := NewMemoryRunStore(MemoryRunStoreOptions{})
	binding, _ := recordedWriter(t, store, "kept", false, RecordDefaults, nil)
	cause := failure.Authored("tool-failed", "Could not read https://packs.example/var/evaluation-pack.", "tool")
	calls := 0
	_, err := InvokeDurable(context.Background(), binding, "host", "source", "evaluate", []byte(`{}`), false, failingCall(cause, &calls), nil, nil)
	if err != error(cause) {
		t.Fatalf("failed with %#v, want the call's own error", err)
	}
	if row, _ := recordedRow(t, store, "kept"); row.Get("failure").Get("detail").Str() != *cause.Failure.Detail {
		t.Fatalf("recorded %s", row.Text())
	}
}

// Redaction can lengthen a detail past the thousand UTF-16 code units it may hold: it is cut there, and a
// pair the cut would split is left out whole.
func TestARecordedFailureDetailIsCutWithoutSplittingAPair(t *testing.T) {
	filler := strings.Repeat("a", 950)
	raw := recordedRaw + " " + filler + "\U0001F600"
	if units := len(utf16.Encode([]rune(raw))); units != 994 {
		t.Fatalf("the fixture is %d units", units)
	}
	// The redacted text is 1001 units, its pair at units 999 and 1000: the cut keeps 999.
	want := recordedRedacted + " " + filler
	store := NewMemoryRunStore(MemoryRunStoreOptions{})
	binding, _ := recordedWriter(t, store, "cut", false, RecordDefaults, nil)
	calls := 0
	_, err := InvokeDurable(context.Background(), binding, "host", "source", "evaluate", []byte(`{}`), false, failingCall(failure.Authored("tool-failed", raw, "tool"), &calls), nil, nil)
	if err == nil || err.Error() != want || len(utf16.Encode([]rune(err.Error()))) != 999 {
		t.Fatalf("failed with %d units: %v", len(utf16.Encode([]rune(err.Error()))), err)
	}
	if row, _ := recordedRow(t, store, "cut"); row.Get("failure").Get("detail").Str() != want {
		t.Fatalf("recorded %s", row.Text())
	}
}

func TestADurableCallsFailureARefusingGuardWithholdsIsUnknown(t *testing.T) {
	store := NewMemoryRunStore(MemoryRunStoreOptions{})
	binding, _ := recordedWriter(t, store, "refused", false, RecordDefaults, PolicyMembers{"outputGuard": {Value: "refuse"}})
	calls := 0
	_, err := InvokeDurable(context.Background(), binding, "host", "source", "evaluate", []byte(`{}`), false, failingCall(failure.Authored("tool-failed", recordedFailureRaw, "tool"), &calls), nil, nil)
	var unknown *StorageOutcomeUnknown
	if !errors.As(err, &unknown) || calls != 1 || strings.Contains(err.Error(), recordedRawPath) {
		t.Fatalf("failed with %v after %d calls", err, calls)
	}
	row, stored := recordedRow(t, store, "refused")
	if row.Get("state").Str() != "claimed" || row.Has("failure") || strings.Contains(stored, recordedRawPath) {
		t.Fatalf("recorded %s", row.Text())
	}
}

func TestADurableModelsFailureIsStoredInTheRecordedForm(t *testing.T) {
	ctx := context.Background()
	t.Run("redacted", func(t *testing.T) {
		store := NewMemoryRunStore(MemoryRunStoreOptions{})
		binding, _ := recordedWriter(t, store, "model-failed", false, RecordDefaults, nil)
		handle, err := beginDurableModel(ctx, binding, "source", "fixture", []byte(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		if err := finishDurableModel(ctx, binding, handle, failure.Authored("unavailable", recordedFailureRaw, "model"), false); err != nil {
			t.Fatal(err)
		}
		row, stored := recordedRow(t, store, "model-failed")
		want := `{"code":"unavailable","detail":"Could not read https://packs.example[redacted: local-temp-path] today","outcome":"unknown","source":"model","version":1}`
		if row.Get("state").Str() != "unknown" || CanonicalJSON(row.Get("failure")) != want || strings.Contains(stored, recordedRawPath) {
			t.Fatalf("recorded %s", row.Text())
		}
	})
	t.Run("a refusing guard", func(t *testing.T) {
		store := NewMemoryRunStore(MemoryRunStoreOptions{})
		binding, _ := recordedWriter(t, store, "model-refused", false, RecordDefaults, PolicyMembers{"outputGuard": {Value: "refuse"}})
		handle, err := beginDurableModel(ctx, binding, "source", "fixture", []byte(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		err = finishDurableModel(ctx, binding, handle, failure.Authored("unavailable", recordedFailureRaw, "model"), false)
		var unknown *StorageOutcomeUnknown
		if !errors.As(err, &unknown) {
			t.Fatalf("finished with %v", err)
		}
		row, stored := recordedRow(t, store, "model-refused")
		if row.Get("state").Str() != "claimed" || row.Has("failure") || strings.Contains(stored, recordedRawPath) {
			t.Fatalf("recorded %s", row.Text())
		}
	})
}

// A delegation refusal an approved Runtime call answers with, where its durable dispatch recorded it: the
// answer is the recorded form, not the refusal it wraps.
func TestARecordedDelegationRefusalIsAnsweredInTheRecordedForm(t *testing.T) {
	store := NewMemoryRunStore(MemoryRunStoreOptions{})
	binding, _ := recordedWriter(t, store, "delegation", false, RecordDefaults, nil)
	calls := 0
	refused := &runtimeDelegationRefused{failure.Authored("permission", recordedFailureRaw, "tool")}
	_, err := InvokeDurable(context.Background(), binding, "runtime", "source", "evaluate", []byte(`{}`), false, failingCall(refused, &calls), nil, nil)
	answer, ok := delegationRefusalAnswer(err)
	if !ok || !answer.Err() || strings.Contains(string(answer.JSON()), recordedRawPath) || !strings.Contains(string(answer.JSON()), recordedRedacted) {
		t.Fatalf("answered %s, %v", answer.JSON(), ok)
	}
}

// A detail the guard reads whole: Authored cuts a detail to the thousand units it may hold, and a match the
// cut would split (a private key whose END marker lies past unit 1000) is found only in the whole detail.
// The fixtures' key is synthetic.
const (
	wholeKeyBody   = "TUlJRWtleUJvZHlGb3JUZXN0aW5nT25seQ" // 34 units
	wholeRSAKey    = "-----BEGIN RSA PRIVATE KEY-----\n" + wholeKeyBody + wholeKeyBody + wholeKeyBody + wholeKeyBody + wholeKeyBody + wholeKeyBody + wholeKeyBody + wholeKeyBody + "\n-----END RSA PRIVATE KEY-----"
	wholeRedaction = "[redacted: private-key]"
)

// wholeCases are details whose key the cut at unit 1000 would leave unmatched, with the recorded form of each.
func wholeCases() []struct{ name, raw, want string } {
	ecBody := strings.Repeat("Q", 227)
	ecBegin := "-----BEGIN EC PRIVATE KEY-----\n"
	ecFiller := strings.Repeat("x", 1000-len(ecBegin)-len(ecBody)-1-10)
	return []struct{ name, raw, want string }{
		// The key's body crosses unit 1000, its END marker wholly past it.
		{"a body across the cut", "Rejected key after " + strings.Repeat("x", 800) + ": " + wholeRSAKey + " (bad padding)",
			"Rejected key after " + strings.Repeat("x", 800) + ": " + wholeRedaction + " (bad padding)"},
		// Its END marker crosses unit 1000.
		{"an END marker across the cut", "Rejected key after " + strings.Repeat("x", 660) + ": " + wholeRSAKey + " (bad padding)",
			"Rejected key after " + strings.Repeat("x", 660) + ": " + wholeRedaction + " (bad padding)"},
		// An EC-sized key whose body lies within the cut and whose END marker alone crosses it.
		{"an EC key with only its END marker cut", ecFiller + ecBegin + ecBody + "\n-----END EC PRIVATE KEY----- (bad curve)",
			ecFiller + wholeRedaction + " (bad curve)"},
	}
}

func TestARecordedFailureDetailIsGuardedWholeBeforeItIsCut(t *testing.T) {
	for _, c := range wholeCases() {
		t.Run(c.name, func(t *testing.T) {
			cut := c.raw[:1000]
			if !strings.Contains(c.raw[:1000], "-----BEGIN") || strings.Contains(cut, "PRIVATE KEY----- (") || len(c.raw) <= 1000 {
				t.Fatalf("the fixture does not cross the cut: %d", len(c.raw))
			}
			cause := failure.Authored("tool-failed", c.raw, "tool")
			// Where no durable writer reads it, the failure is as it was: cut to a thousand units.
			if cause.Error() != cut || *failure.Normalize(cause, "tool", time.Now()).Detail != cut {
				t.Fatalf("authored %q", cause.Error())
			}
			// The whole detail is no part of the error's encoding or printing.
			encoded, _ := json.Marshal(cause)
			printed := fmt.Sprintf("%v|%+v|%#v|%s", cause, cause, cause, cause)
			if strings.Contains(string(encoded)+printed, "PRIVATE KEY----- (") {
				t.Fatalf("the error shows its whole detail: %s %s", encoded, printed)
			}
			store := NewMemoryRunStore(MemoryRunStoreOptions{})
			binding, _ := recordedWriter(t, store, "whole", false, RecordDefaults, nil)
			calls := 0
			_, err := InvokeDurable(context.Background(), binding, "host", "source", "evaluate", []byte(`{}`), false, failingCall(fmt.Errorf("evaluate: %w", cause), &calls), nil, nil)
			if err == nil || err.Error() != c.want || !errors.Is(err, cause) {
				t.Fatalf("failed with %v", err)
			}
			// Exactly what the model is given, and exactly what the row holds.
			text, structured := failure.ToolFailure(err)
			detail, _ := json.Marshal(c.want)
			want := `{"failure":{"code":"tool-failed","detail":` + string(detail) + `,"outcome":"unknown","source":"tool","version":1}}`
			if text != c.want || CanonicalJSON(MustParseJSON(string(structured))) != want {
				t.Fatalf("the model reads %q, %s", text, structured)
			}
			row, stored := recordedRow(t, store, "whole")
			if "{\"failure\":"+CanonicalJSON(row.Get("failure"))+"}" != want || strings.Contains(stored, "PRIVATE KEY-----\\n") || strings.Contains(stored, "QQQQQQQQ") || strings.Contains(stored, wholeKeyBody) {
				t.Fatalf("recorded %s", row.Text())
			}
			// The error keeps its whole detail once the recorded form is made, so a later use is guarded alike
			// (#367).
			if _, held := failure.WholeDetail(cause, "tool", time.Now()); *held != c.raw {
				t.Fatalf("the error holds %d bytes of %d", len(*held), len(c.raw))
			}
		})
	}
}

func TestADurableModelsFailureDetailIsGuardedWholeBeforeItIsCut(t *testing.T) {
	ctx := context.Background()
	c := wholeCases()[1]
	store := NewMemoryRunStore(MemoryRunStoreOptions{})
	binding, _ := recordedWriter(t, store, "model-whole", false, RecordDefaults, nil)
	handle, err := beginDurableModel(ctx, binding, "source", "fixture", []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := finishDurableModel(ctx, binding, handle, failure.Authored("unavailable", c.raw, "model"), false); err != nil {
		t.Fatal(err)
	}
	row, stored := recordedRow(t, store, "model-whole")
	if row.Get("failure").Get("detail").Str() != c.want || strings.Contains(stored, wholeKeyBody) {
		t.Fatalf("recorded %s", row.Text())
	}
}

// The whole detail is read only for the detail it was cut to: an error whose failure was given another
// detail since is recorded with that one.
func TestAWholeDetailIsReadOnlyForTheDetailItWasCutTo(t *testing.T) {
	cause := failure.Authored("tool-failed", wholeCases()[0].raw, "tool")
	other := "Rejected a key."
	cause.Failure.Detail = &other
	store := NewMemoryRunStore(MemoryRunStoreOptions{})
	binding, _ := recordedWriter(t, store, "restated", false, RecordDefaults, nil)
	calls := 0
	_, err := InvokeDurable(context.Background(), binding, "host", "source", "evaluate", []byte(`{}`), false, failingCall(cause, &calls), nil, nil)
	if err == nil || err.Error() != other {
		t.Fatalf("failed with %v", err)
	}
	if row, _ := recordedRow(t, store, "restated"); row.Get("failure").Get("detail").Str() != other {
		t.Fatalf("recorded %s", row.Text())
	}
}

// Authored cuts as authoredFailure does: a pair the cut would split is left out whole (999 units here).
func TestAnAuthoredDetailIsCutWithoutSplittingAPair(t *testing.T) {
	got := failure.Authored("tool-failed", strings.Repeat("a", 999)+"\U0001F600tail", "tool").Error()
	if got != strings.Repeat("a", 999) {
		t.Fatalf("cut to %d units", len(utf16.Encode([]rune(got))))
	}
}

// recordedRows is the stored run's rows, and the stored checkpoint's text.
func recordedRows(t *testing.T, store RunStore, id string) ([]*Value, string) {
	t.Helper()
	head, err := store.Load(context.Background(), id)
	if err != nil || head == nil {
		t.Fatalf("no stored run: %v", err)
	}
	return MustParseJSON(string(head.Checkpoint)).Get("durable").Get("dispatches").Items(), string(head.Checkpoint)
}

// #367: the same error used twice is guarded whole both times: the error keeps its whole detail once it is
// read, so the second use is not guarded as the cut. recordedFailures.test.ts holds the TypeScript.
func TestTheSameFailureUsedTwiceIsGuardedWholeBothTimes(t *testing.T) {
	ctx := context.Background()
	for _, c := range wholeCases() {
		t.Run(c.name, func(t *testing.T) {
			t.Run("a tool's, twice on one writer", func(t *testing.T) {
				cause := failure.Authored("tool-failed", c.raw, "tool")
				store := NewMemoryRunStore(MemoryRunStoreOptions{})
				binding, _ := recordedWriter(t, store, "twice", false, RecordDefaults, nil)
				for n := 1; n <= 2; n++ {
					calls := 0
					_, err := InvokeDurable(ctx, binding, "host", "source", "evaluate", []byte(fmt.Sprintf(`{"n":%d}`, n)), false, failingCall(cause, &calls), nil, nil)
					if err == nil || err.Error() != c.want || !errors.Is(err, cause) {
						t.Fatalf("use %d failed with %v", n, err)
					}
				}
				rows, stored := recordedRows(t, store, "twice")
				if len(rows) != 2 || strings.Contains(stored, wholeKeyBody) || strings.Contains(stored, "QQQQQQQQ") {
					t.Fatalf("recorded %d rows: %s", len(rows), stored)
				}
				for _, row := range rows {
					if row.Get("failure").Get("detail").Str() != c.want {
						t.Fatalf("recorded %s", row.Text())
					}
				}
			})
			t.Run("a tool's, on a second writer", func(t *testing.T) {
				cause := failure.Authored("tool-failed", c.raw, "tool")
				for _, id := range []string{"first", "second"} {
					store := NewMemoryRunStore(MemoryRunStoreOptions{})
					binding, _ := recordedWriter(t, store, id, false, RecordDefaults, nil)
					calls := 0
					_, err := InvokeDurable(ctx, binding, "host", "source", "evaluate", []byte(`{}`), false, failingCall(cause, &calls), nil, nil)
					if err == nil || err.Error() != c.want {
						t.Fatalf("the %s writer failed with %v", id, err)
					}
					if row, stored := recordedRow(t, store, id); row.Get("failure").Get("detail").Str() != c.want || strings.Contains(stored, wholeKeyBody) {
						t.Fatalf("the %s writer recorded %s", id, row.Text())
					}
				}
			})
			// A model's failure ends its run's dispatch, so its second use is on a second run.
			t.Run("a model's, twice", func(t *testing.T) {
				cause := failure.Authored("unavailable", c.raw, "model")
				for _, id := range []string{"model-first", "model-second"} {
					store := NewMemoryRunStore(MemoryRunStoreOptions{})
					binding, _ := recordedWriter(t, store, id, false, RecordDefaults, nil)
					handle, err := beginDurableModel(ctx, binding, "source", "fixture", []byte(`{}`))
					if err != nil {
						t.Fatal(err)
					}
					if err := finishDurableModel(ctx, binding, handle, cause, false); err != nil {
						t.Fatal(err)
					}
					if row, stored := recordedRow(t, store, id); row.Get("failure").Get("detail").Str() != c.want || strings.Contains(stored, wholeKeyBody) || strings.Contains(stored, "QQQQQQQQ") {
						t.Fatalf("the %s run recorded %s", id, row.Text())
					}
				}
			})
		})
	}
}

// #367: a second writer whose guard holds a pattern the first's does not finds a match the cut would split:
// it reads the same whole detail the first did.
func TestASecondWritersGuardReadsTheWholeDetail(t *testing.T) {
	ticket := "TICKET-" + strings.Repeat("7", 40)
	filler := strings.Repeat("f", 975-len("Rejected ")-len(wholeRedaction)-len(" then "))
	raw := "Rejected " + wholeRSAKey + " then " + filler + ticket + " (end)"
	first := "Rejected " + wholeRedaction + " then " + filler + ticket[:25]
	second := "Rejected " + wholeRedaction + " then " + filler + "[redacted: ticket] (end)"
	if len(first) != 1000 || len(second) != 999 {
		t.Fatalf("the fixture is %d and %d units", len(first), len(second))
	}
	cause := failure.Authored("tool-failed", raw, "tool")
	for _, w := range []struct {
		id      string
		members PolicyMembers
		want    string
	}{
		{"first", nil, first},
		{"second", PolicyMembers{"outputPatterns": {Value: []any{map[string]any{"name": "ticket", "pattern": "TICKET-[0-9]{40}"}}}}, second},
	} {
		store := NewMemoryRunStore(MemoryRunStoreOptions{})
		binding, _ := recordedWriter(t, store, w.id, false, RecordDefaults, w.members)
		calls := 0
		_, err := InvokeDurable(context.Background(), binding, "host", "source", "evaluate", []byte(`{}`), false, failingCall(cause, &calls), nil, nil)
		if err == nil || err.Error() != w.want {
			t.Fatalf("the %s writer failed with %v", w.id, err)
		}
		if row, _ := recordedRow(t, store, w.id); row.Get("failure").Get("detail").Str() != w.want {
			t.Fatalf("the %s writer recorded %s", w.id, row.Text())
		}
	}
}

// #367: the guard reads the cut and WholeDetailMargin past it, the same 16384 UTF-16 code units as
// authoredFailure's WHOLE_DETAIL_MARGIN, never half a pair.
func TestAWholeDetailIsKeptAsFarAsTheMargin(t *testing.T) {
	if failure.WholeDetailMargin != 16384 {
		t.Fatalf("the margin is %d", failure.WholeDetailMargin)
	}
	bound := 1000 + failure.WholeDetailMargin
	units := func(s string) int { return len(utf16.Encode([]rune(s))) }
	for _, c := range []struct{ name, raw, want string }{
		{"a long detail", strings.Repeat("y", 3*bound), strings.Repeat("y", bound)},
		// A pair the bound would split is left out whole.
		{"a pair across the bound", strings.Repeat("y", bound-1) + "\U0001F600tail", strings.Repeat("y", bound-1)},
		{"a pair within the bound", strings.Repeat("y", bound-2) + "\U0001F600tail", strings.Repeat("y", bound-2) + "\U0001F600"},
	} {
		_, whole := failure.WholeDetail(failure.Authored("tool-failed", c.raw, "tool"), "tool", time.Now())
		if *whole != c.want {
			t.Fatalf("%s: kept %d units", c.name, units(*whole))
		}
	}
}

// #367: a very long detail is guarded as far as the margin, within the guard's work bound: the call ends as
// an ordinary stored failure, with exactly the cut's bytes, not an unknown storage outcome.
func TestAVeryLongFailureDetailIsAnOrdinaryStoredFailure(t *testing.T) {
	for _, shape := range []string{"x", "a b/"} {
		raw := strings.Repeat(shape, 300_000/len(shape))
		cut := raw[:1000]
		store := NewMemoryRunStore(MemoryRunStoreOptions{})
		binding, _ := recordedWriter(t, store, "long", false, RecordDefaults, nil)
		cause := failure.Authored("tool-failed", raw, "tool")
		calls := 0
		_, err := InvokeDurable(context.Background(), binding, "host", "source", "evaluate", []byte(`{}`), false, failingCall(cause, &calls), nil, nil)
		if err != error(cause) || binding.Writer.Failed() {
			t.Fatalf("%q: failed with %v (%v)", shape, err, binding.Writer.Failed())
		}
		row, _ := recordedRow(t, store, "long")
		want := `{"code":"tool-failed","detail":"` + cut + `","outcome":"unknown","source":"tool","version":1}`
		if row.Get("state").Str() != "unknown" || CanonicalJSON(row.Get("failure")) != want {
			t.Fatalf("%q: recorded %.200s", shape, row.Text())
		}
	}
}

// #367: a private key the cut would split is found where its END marker ends within the margin; where it
// ends past it, the pattern, which needs both markers, does not match, and the detail is stored as cut
// (ADR-0018 says so).
func TestAKeyWhoseEndLiesPastTheMarginIsStoredAsCut(t *testing.T) {
	head := "Rejected key after " + strings.Repeat("x", 800) + ": "
	begin, end := "-----BEGIN RSA PRIVATE KEY-----\n", "\n-----END RSA PRIVATE KEY-----"
	within := 1000 + failure.WholeDetailMargin - len(head) - len(begin) - len(end)
	for _, c := range []struct {
		name string
		body int
		want func(raw string) string
	}{
		{"its END marker ends at the margin", within, func(string) string { return head + wholeRedaction }},
		{"its END marker ends one unit past it", within + 1, func(raw string) string { return raw[:1000] }},
	} {
		raw := head + begin + strings.Repeat("Q", c.body) + end + " (bad padding)"
		store := NewMemoryRunStore(MemoryRunStoreOptions{})
		binding, _ := recordedWriter(t, store, "margin", false, RecordDefaults, nil)
		calls := 0
		_, err := InvokeDurable(context.Background(), binding, "host", "source", "evaluate", []byte(`{}`), false, failingCall(failure.Authored("tool-failed", raw, "tool"), &calls), nil, nil)
		if err == nil || err.Error() != c.want(raw) {
			t.Fatalf("%s: failed with %.120v", c.name, err)
		}
		if row, _ := recordedRow(t, store, "margin"); row.Get("failure").Get("detail").Str() != c.want(raw) {
			t.Fatalf("%s: recorded %.200s", c.name, row.Text())
		}
	}
}
