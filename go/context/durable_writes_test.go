package runcontext

// #275: what a write that reads alike a recorded one is to that record. The published fingerprint does
// not change; the decision is made from the exact text every row records, so a row a published release
// wrote is judged by the same rule. contract/vectors/storage/durable-repeats.json states the cases and
// storage/durableWrites.test.ts holds the TypeScript to the same ones.
// go/adapter/eino/durable_writes_test.go drives the same pairs through both adapters.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
)

type durableRepeatCase struct {
	Label    string `json:"label"`
	Recorded string `json:"recorded"`
	Request  string `json:"request"`
	Decision string `json:"decision"`
}

type durableResumeCall struct {
	Call    string `json:"call"`
	Outcome string `json:"outcome"`
	Answer  string `json:"answer"`
}

type durableRepeatVector struct {
	Cases      []durableRepeatCase `json:"cases"`
	GoCases    []durableRepeatCase `json:"goCases"`
	Recordable []struct {
		Text       string `json:"text"`
		Recordable bool   `json:"recordable"`
	} `json:"recordable"`
	Resume map[string][]durableResumeCall `json:"resume"`
	Items  []DurableDispatch              `json:"items"`
}

func readDurableRepeats(t *testing.T) durableRepeatVector {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repositoryRoot(t), "contract", "vectors", "storage", "durable-repeats.json"))
	if err != nil {
		t.Fatal(err)
	}
	var vector durableRepeatVector
	if err = json.Unmarshal(raw, &vector); err != nil {
		t.Fatal(err)
	}
	if len(vector.Cases) == 0 || len(vector.Recordable) == 0 || len(vector.Items) == 0 {
		t.Fatal("the vector is empty")
	}
	return vector
}

// A store with only the RunStore methods: no history recorder, as v0.10.0 wrote under.
type plainRunStore struct{ RunStore }

var durableScope = DurableScope{Execution: "repeats", Role: "author"}

func durableWriter(t *testing.T, store RunStore, id string, resume bool) *RunBinding {
	t.Helper()
	_, binding := writerFixture(t, store, id, resume)
	binding.Scope = DurableScope{Execution: id, Role: "author"}
	return binding
}

// runs records what a write was run with and answers with it.
func runs(ran *[]string) func([]byte) (ToolResult, error) {
	return func(raw []byte) (ToolResult, error) {
		*ran = append(*ran, string(raw))
		return ResultOf([]Content{TextContent("ran " + string(raw))}, nil), nil
	}
}

// Every case's two texts have one fingerprint, the decision is the vector's, and the writer acts on it.
func TestTheDurableRepeatVector(t *testing.T) {
	ctx := context.Background()
	vector := readDurableRepeats(t)
	if len(vector.GoCases) == 0 {
		t.Fatal("the vector has no Go cases")
	}
	for _, item := range append(append([]durableRepeatCase{}, vector.Cases...), vector.GoCases...) {
		t.Run(item.Label, func(t *testing.T) {
			left, err := durableFingerprint("host", durableScope, "source", "write", []byte(item.Recorded))
			if err != nil {
				t.Fatal(err)
			}
			right, err := durableFingerprint("host", durableScope, "source", "write", []byte(item.Request))
			if err != nil || left != right {
				t.Fatalf("the two texts do not read alike: %s %s %v", left, right, err)
			}
			got := map[durableRepeat]string{durableSame: "same", durableDifferent: "different", durableConflict: "conflict"}[compareDurableRequests([]byte(item.Recorded), []byte(item.Request))]
			if got != item.Decision {
				t.Fatalf("decided %s", got)
			}
			// Through the exported path, on a store that records whatever a release once recorded.
			binding := durableWriter(t, plainRunStore{NewMemoryRunStore(MemoryRunStoreOptions{})}, "repeats", false)
			ran := []string{}
			first := runs(&ran)
			write := func(text string) (ToolResult, error) {
				return InvokeDurable(ctx, binding, "host", "source", "write", []byte(text), false, first, nil, nil)
			}
			recorded := item.Recorded
			// What this release cannot record is put in the book the way a published release left it.
			if _, err := readHistoryRequestJSON([]byte(recorded)); err != nil {
				fingerprint, _ := durableFingerprint("host", binding.Scope, "source", "write", []byte(recorded))
				result, _ := json.Marshal(map[string]any{"content": []any{map[string]any{"type": "text", "text": "ran " + recorded}}})
				binding.Writer.book.Dispatches = append(binding.Writer.book.Dispatches, DurableDispatch{ID: "published-row", Scope: binding.Scope, Kind: "host", Source: "source", Tool: "write", Fingerprint: fingerprint, Request: recorded, RequestDigest: durableDigest([]byte(recorded)), State: "result", Read: false, Result: result})
				ran = append(ran, recorded)
			} else if _, err := write(recorded); err != nil {
				t.Fatal(err)
			}
			before := len(binding.Writer.book.Dispatches)
			result, err := write(item.Request)
			switch item.Decision {
			case "same":
				if err != nil || len(ran) != 1 || result.Content[0].Text() != "ran "+recorded {
					t.Fatalf("ran %q, answered %v, %v", ran, result.Content, err)
				}
			case "conflict":
				var conflict *DurableRequestConflict
				if !errors.As(err, &conflict) || len(ran) != 1 || len(binding.Writer.book.Dispatches) != before {
					t.Fatalf("ran %q, %v", ran, err)
				}
			default:
				var unrecordable *DurableRequestUnrecordable
				if _, bad := readHistoryRequestJSON([]byte(item.Request)); bad != nil {
					if !errors.As(err, &unrecordable) || len(ran) != 1 {
						t.Fatalf("ran %q, %v", ran, err)
					}
					return
				}
				if err != nil || len(ran) != 2 || result.Content[0].Text() != "ran "+item.Request {
					t.Fatalf("ran %q, answered %v, %v", ran, result.Content, err)
				}
			}
		})
	}
}

// What the record can hold is the same on a store with a history recorder and on one without: a call it
// cannot hold is refused before anything is claimed, the writer stays well, and the run goes on.
func TestTheDurableRecordableVector(t *testing.T) {
	ctx := context.Background()
	vector := readDurableRepeats(t)
	for _, store := range []struct {
		name string
		make func() RunStore
	}{{"history store", func() RunStore { return NewMemoryRunStore(MemoryRunStoreOptions{}) }}, {"plain store", func() RunStore { return plainRunStore{NewMemoryRunStore(MemoryRunStoreOptions{})} }}} {
		for _, read := range []bool{false, true} {
			for _, item := range vector.Recordable {
				for _, kind := range []string{"host", "runtime"} {
					t.Run(fmt.Sprintf("%s/%s/read=%v/%s", store.name, kind, read, item.Text), func(t *testing.T) {
						binding := durableWriter(t, store.make(), "recordable", false)
						ran := []string{}
						_, err := InvokeDurable(ctx, binding, kind, "source", "write", []byte(item.Text), read, runs(&ran), nil, nil)
						if item.Recordable {
							if err != nil || len(ran) != 1 {
								t.Fatalf("ran %q, %v", ran, err)
							}
							return
						}
						var refused *DurableRequestUnrecordable
						if !errors.As(err, &refused) || len(ran) != 0 || len(binding.Writer.book.Dispatches) != 0 {
							t.Fatalf("ran %q, %v", ran, err)
						}
						// The writer goes on: the next call runs and is recorded.
						if _, err := InvokeDurable(ctx, binding, kind, "source", "write", []byte(`{"next":1}`), read, runs(&ran), nil, nil); err != nil || len(ran) != 1 {
							t.Fatalf("ran %q, %v", ran, err)
						}
					})
				}
			}
		}
	}
}

// Both refusals read as the model reads a mistake in its input, and say nothing ran.
func TestTheDurableRefusalsAreNamed(t *testing.T) {
	for name, err := range map[string]error{"DurableRequestConflict": &DurableRequestConflict{}, "DurableRequestUnrecordable": &DurableRequestUnrecordable{}} {
		held := failure.Normalize(err, "tool", time.Now())
		if held.Code != "invalid-tool-input" || held.Source != "tool" || held.Outcome != "not-started" || held.Detail == nil || !strings.HasPrefix(*held.Detail, name+": ") {
			t.Fatalf("%s normalizes to %+v", name, held)
		}
	}
}

// The fingerprint keeps its published form and its parse error: the digests the vector's rows carry, and
// a request that does not read has no fingerprint at all.
func TestTheDurableFingerprintKeepsItsFormAndItsParseError(t *testing.T) {
	for _, row := range readDurableRepeats(t).Items {
		got, err := durableFingerprint(row.Kind, row.Scope, row.Source, row.Tool, []byte(row.Request))
		if err != nil || got != row.Fingerprint {
			t.Fatalf("%s fingerprints as %s, not %s: %v", row.Request, got, row.Fingerprint, err)
		}
	}
	for _, text := range []string{``, `{bad`, `{"q":1} {"q":2}`, `{"q":1} ]`} {
		var refused *ContinuationNotRecoverable
		if got, err := durableFingerprint("host", durableScope, "source", "write", []byte(text)); !errors.As(err, &refused) || got != "" {
			t.Fatalf("%q fingerprints as %q, %v", text, got, err)
		}
	}
}

func readPublishedCheckpoints(t *testing.T) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repositoryRoot(t), "contract", "testdata", "durable-writes", "published-checkpoints.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Checkpoints map[string]string `json:"checkpoints"`
	}
	if err = json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Checkpoints
}

// A resume over a checkpoint a published release wrote: it still reads, its rows keep their fingerprints,
// a row is judged by the bytes it recorded, and no write runs a second time. A row under the old
// fingerprint answers only its own write. Then a second resume, over what this release wrote on top.
func TestAResumeOverAPublishedCheckpoint(t *testing.T) {
	ctx := context.Background()
	vector := readDurableRepeats(t)
	checkpoints := readPublishedCheckpoints(t)
	for _, tag := range []string{"v0.12.0", "v0.10.0"} {
		for _, store := range []struct {
			name string
			make func() RunStore
		}{{"history store", func() RunStore { return NewMemoryRunStore(MemoryRunStoreOptions{}) }}, {"plain store", func() RunStore { return plainRunStore{NewMemoryRunStore(MemoryRunStoreOptions{})} }}} {
			t.Run(tag+"/"+store.name, func(t *testing.T) {
				held := store.make()
				raw := []byte(checkpoints[tag])
				value := MustParseJSON(string(raw))
				if !ValidDurable(value.Get("durable")) {
					t.Fatal("the published book does not read")
				}
				if _, err := held.Commit(ctx, RunCommit{RunID: "published-writes", CommitID: "published", Checkpoint: raw, Events: [][]byte{}}); err != nil {
					t.Fatal(err)
				}
				binding := durableWriter(t, held, "published-writes", true)
				for _, row := range binding.Writer.book.Dispatches {
					got, err := durableFingerprint(row.Kind, row.Scope, row.Source, row.Tool, []byte(row.Request))
					if err != nil || got != row.Fingerprint {
						t.Fatalf("%s fingerprints as %s: %v", row.Request, got, err)
					}
				}
				ran := []string{}
				want := []string{}
				for at, call := range vector.Resume[tag] {
					result, err := InvokeDurable(ctx, binding, "host", "source", "write", []byte(call.Call), false, runs(&ran), nil, nil)
					label := fmt.Sprintf("call %d, %s", at+1, call.Call)
					switch call.Outcome {
					case "run":
						want = append(want, call.Call)
						if err != nil || result.Content[0].Text() != call.Answer {
							t.Fatalf("%s: answered %v, %v", label, result.Content, err)
						}
					case "record":
						if err != nil || result.Content[0].Text() != call.Answer {
							t.Fatalf("%s: answered %v, %v", label, result.Content, err)
						}
					case "conflict":
						var refused *DurableRequestConflict
						if !errors.As(err, &refused) {
							t.Fatalf("%s: %v", label, err)
						}
					case "unrecordable":
						var refused *DurableRequestUnrecordable
						if !errors.As(err, &refused) {
							t.Fatalf("%s: %v", label, err)
						}
					case "unknown":
						var refused *HostOutcomeUnknown
						if !errors.As(err, &refused) {
							t.Fatalf("%s: %v", label, err)
						}
					default:
						t.Fatalf("%s: no outcome %q", label, call.Outcome)
					}
					if fmt.Sprint(ran) != fmt.Sprint(want) {
						t.Fatalf("%s: ran %q, want %q", label, ran, want)
					}
				}
				// Resumed again: everything that ran has its record, and nothing runs a second time.
				if len(want) == 0 {
					t.Fatal("nothing ran")
				}
				again := durableWriter(t, held, "published-writes", true)
				if !ValidDurable(MustParseJSON(string(durableBookJSON(t, again.Writer.book)))) {
					t.Fatal("the book this release wrote does not read")
				}
				for _, call := range vector.Resume[tag] {
					if _, err := InvokeDurable(ctx, again, "host", "source", "write", []byte(call.Call), false, runs(&ran), nil, nil); err != nil && (call.Outcome == "record" || call.Outcome == "run") {
						t.Fatalf("%s: %v", call.Call, err)
					}
				}
				if fmt.Sprint(ran) != fmt.Sprint(want) {
					t.Fatalf("a resume ran %q, want %q", ran, want)
				}
			})
		}
	}
}

func durableBookJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// The checkpoint a write that shares a fingerprint leaves: two rows with one fingerprint, each with its own
// request and digest, and the book still reads.
func TestTwoWritesThatReadAlikeKeepTheirOwnRows(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryRunStore(MemoryRunStoreOptions{})
	binding := durableWriter(t, store, "neighbours", false)
	ran := []string{}
	calls := []string{`{"id":9007199254740993}`, `{"id":9007199254740992}`}
	for _, call := range calls {
		if _, err := InvokeDurable(ctx, binding, "host", "source", "write", []byte(call), false, runs(&ran), nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	head, err := store.Load(ctx, "neighbours")
	if err != nil || head == nil {
		t.Fatal(err)
	}
	var saved contract.Checkpoint
	if err = json.Unmarshal(head.Checkpoint, &saved); err != nil {
		t.Fatal(err)
	}
	book := MustParseJSON(string(head.Checkpoint)).Get("durable")
	if !ValidDurable(book) {
		t.Fatal("the book does not read")
	}
	rows := book.Get("dispatches").Items()
	if len(rows) != 2 || rows[0].Get("fingerprint").Str() != rows[1].Get("fingerprint").Str() || rows[0].Get("request").Str() != calls[0] || rows[1].Get("request").Str() != calls[1] || rows[0].Get("requestDigest").Str() == rows[1].Get("requestDigest").Str() {
		t.Fatalf("the rows are %s", book.Get("dispatches").Text())
	}
}

// Bytes that are not UTF-8 decode as a replacement character, so two of them read alike, and neither says
// what the other was.
func TestRawBytesThatAreNotUTF8AreExactOnNeitherSide(t *testing.T) {
	left, right := []byte("{\"q\":\"\xff\"}"), []byte("{\"q\":\"\xfe\"}")
	if got := compareDurableRequests(left, right); got != durableConflict {
		t.Fatalf("decided %v", got)
	}
	if got := compareDurableRequests(left, left); got != durableSame {
		t.Fatalf("the same bytes decided %v", got)
	}
}

// A book is read closed: a row's fingerprint is what its request says, whichever bytes the row holds, so
// a row cannot be made to answer another write by writing another fingerprint on it.
func TestABookWithAForgedFingerprintDoesNotRead(t *testing.T) {
	var checkpoint struct {
		Durable map[string]any `json:"durable"`
	}
	if err := json.Unmarshal([]byte(readPublishedCheckpoints(t)["v0.12.0"]), &checkpoint); err != nil {
		t.Fatal(err)
	}
	read := func() bool {
		raw, err := json.Marshal(checkpoint.Durable)
		if err != nil {
			t.Fatal(err)
		}
		return ValidDurable(MustParseJSON(string(raw)))
	}
	if !read() {
		t.Fatal("the published book does not read")
	}
	rows := checkpoint.Durable["dispatches"].([]any)
	var row map[string]any
	for _, held := range rows {
		if held.(map[string]any)["kind"] == "host" {
			row = held.(map[string]any)
		}
	}
	honest := row["fingerprint"]
	row["fingerprint"] = "sha256:" + strings.Repeat("a", 64)
	if read() {
		t.Fatal("a row with another fingerprint reads")
	}
	row["fingerprint"] = honest
	if !read() {
		t.Fatal("the honest row does not read")
	}
}

// The binding of a call held for approval covers the text the person was shown, in both languages
// (storage/resume.test.ts). A call that keeps no text binds as it always has.
func TestAnApprovalBindingCoversTheKeptText(t *testing.T) {
	text := `{"id":9007199254740993}`
	approval := contract.CheckpointApproval{CallID: "held", Name: "tracker__create_issue", Tool: "create_issue", Source: contract.ToolOfferSourceRecord{Kind: "custom", ID: "tracker", Trust: "person"}, Args: map[string]json.RawMessage{"id": json.RawMessage(`9007199254740992`)}, State: "asked", Request: &text}
	const want = "sha256:54a95b12cb74b5e1d6634512ec56ca79018b17db2ea2c6551ed165f440d19ce2"
	binding, err := ApprovalBindingOf(approval)
	if err != nil || binding.CallDigest != want {
		t.Fatalf("%+v, %v", binding, err)
	}
	approval.Request = nil
	if binding, err = ApprovalBindingOf(approval); err != nil || binding.CallDigest == want {
		t.Fatalf("%+v, %v", binding, err)
	}
}
