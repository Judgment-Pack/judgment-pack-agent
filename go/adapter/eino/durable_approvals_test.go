package eino_test

// #275: what a person approves is what runs, byte for byte, and an approved call is answered by the same
// decision over the record as any other. The approval record's args are the parsed form, so a call held for
// approval used to run another number; a call whose bytes args cannot say now keeps them in approval.request.
// A checkpoint a published release wrote holds only the parsed form, so what that form may have changed is
// refused rather than run: contract/testdata/durable-writes/published-approvals.json is that release's own
// output. Both Go adapters.

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	native "github.com/Judgment-Pack/judgment-pack-agent/go/adapter/agent"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/eino"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
	"github.com/Judgment-Pack/judgment-pack-agent/go/relay"
)

func approvalRuntime(_ context.Context, frame json.RawMessage) (json.RawMessage, error) {
	var request struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	if err := json.Unmarshal(frame, &request); err != nil {
		return nil, err
	}
	if request.Method == "notifications/initialized" {
		return nil, nil
	}
	if request.Method == "tools/list" {
		return json.RawMessage(`{"jsonrpc":"2.0","id":` + string(request.ID) + `,"result":{"tools":[]}}`), nil
	}
	return json.RawMessage(`{"jsonrpc":"2.0","id":` + string(request.ID) + `,"result":{}}`), nil
}

// approvalScene is one run's act tool, what it ran with, and what the policy answers next.
type approvalScene struct {
	store   runcontext.RunStore
	engine  string
	answers []string
	ran     []string
	events  []contract.RunEvent
}

func (s *approvalScene) run(t *testing.T, resume bool, decision *contract.CheckpointApprovalDecision, calls []string) {
	t.Helper()
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(`[{"description":"Update a work item.","inputSchema":{"type":"object"},"name":"update_work_item","taxonomy":{"effect":"act","operation":"act"}}]`)))
	tool := runcontext.HostTool{Name: "update_work_item", Taxonomy: &runcontext.ToolTaxonomy{Operation: "act", Effect: "act"}, Description: "Update a work item.",
		InputSchema: json.RawMessage(`{"type":"object"}`), EffectClass: "act", Approval: "required",
		Source: &runcontext.ToolSource{Kind: "gateway", ID: "ado", Trust: "connected", Version: "2026-10", ToolListDigest: digest, CurrentDigest: func(context.Context) (string, error) { return digest, nil }},
		Approve: func(runcontext.ApprovalRequest) string {
			if len(s.answers) == 0 {
				return "allow"
			}
			answer := s.answers[0]
			s.answers = s.answers[1:]
			return answer
		},
		Execute: func(_ context.Context, raw json.RawMessage) (runcontext.ToolResult, error) {
			s.ran = append(s.ran, string(raw))
			return runcontext.ResultOf([]runcontext.Content{runcontext.TextContent("ran " + string(raw))}, nil), nil
		}}
	declared, err := runcontext.OfferedHostTools([]runcontext.HostTool{tool}, gate.Ceiling, runcontext.Defaults)
	if err != nil {
		t.Fatal(err)
	}
	g := gate.New(gate.Options{Runtime: approvalRuntime, Granted: gate.Ceiling, Emit: func(contract.RunEvent) {}})
	offer, err := g.Open(context.Background(), declared)
	if err != nil {
		t.Fatal(err)
	}
	requests := 0
	run := &adapter.Run{
		Wire: "openai-compatible", ModelName: "fixture", Gate: g.Calls(), Tools: offer.Tools, Thinking: "off", Policy: runcontext.PolicyLayer{"records": map[string]any{"retainDays": 36500}},
		Prompt: "Update the item.", AllowConversation: true, Interactive: true, Critic: "off", HostTools: []runcontext.HostTool{tool},
		Context: &runcontext.Input{
			CurrentRequest: &runcontext.CurrentRequest{Message: "Update the item.", Input: "Update the item."},
			Connection:     "provider", ConnectionRevisions: map[string]string{"provider": "one"},
			RunStore: &runcontext.DurableRunInput{Store: s.store, ID: "run-approval", Resume: resume},
			Approval: decision,
		},
		Emit: func(event contract.RunEvent) { s.events = append(s.events, event) },
	}
	var implementation adapter.Adapter
	if s.engine == "native" {
		implementation = native.New()
		run.Agent = &adapter.Agent{Model: "fixture", Run: func(ctx context.Context, request adapter.AgentRequest, cb adapter.AgentCallbacks) error {
			alias := ""
			for _, offered := range request.Tools {
				if strings.Contains(offered.Description, "Update a work item.") {
					alias = offered.Name
				}
			}
			for n, arguments := range calls {
				if _, err := cb.Tool(ctx, adapter.AgentToolCall{ID: fmt.Sprintf("call-%d-%v", n, resume), Name: alias, Arguments: json.RawMessage(arguments)}); err != nil {
					return err
				}
			}
			return cb.Event(ctx, adapter.AgentEvent{Type: "message", ID: "final", Text: "Done.", Phase: "final"})
		}}
	} else {
		implementation = eino.New()
		run.Model = func(_ context.Context, _ string, _ relay.Request) (*http.Response, error) {
			requests++
			if requests <= len(calls) {
				return readKeyCall(fmt.Sprintf("call_%d_%v", requests, resume), "ado__update_work_item", calls[requests-1]), nil
			}
			return quarantineFinal(), nil
		}
	}
	if err := implementation.Run(legContext(t), run); err != nil {
		t.Fatal(err)
	}
}

// head is the stored run's approval, version and host rows.
func (s *approvalScene) head(t *testing.T) (approval *runcontext.Value, version float64, rows []string) {
	t.Helper()
	head, err := s.store.Load(context.Background(), "run-approval")
	if err != nil || head == nil {
		t.Fatal("no stored run", err)
	}
	saved := runcontext.MustParseJSON(string(head.Checkpoint))
	for _, row := range saved.Get("durable").Get("dispatches").Items() {
		if row.Get("kind").Str() == "host" {
			rows = append(rows, row.Get("request").Str())
		}
	}
	return saved.Get("approval"), saved.Get("version").Num(), rows
}

// approve decides the asked call, approved, and has the model make the calls given afterwards.
func (s *approvalScene) approve(t *testing.T, calls ...string) {
	t.Helper()
	approval, _, _ := s.head(t)
	if approval == nil || approval.Get("state").Str() != "asked" {
		t.Fatalf("no asked approval: %v", approval)
	}
	s.events = nil
	head, err := s.store.Load(context.Background(), "run-approval")
	if err != nil {
		t.Fatal(err)
	}
	var saved contract.Checkpoint
	if err = json.Unmarshal(head.Checkpoint, &saved); err != nil {
		t.Fatal(err)
	}
	// A host that has moved to the readers says what the person was shown.
	digest := runcontext.ApprovalShownDigest(*saved.Approval)
	s.run(t, true, &contract.CheckpointApprovalDecision{CallID: approval.Get("callId").Str(), Approved: true, ShownDigest: &digest}, calls)
}

func (s *approvalScene) answered(t *testing.T) string {
	t.Helper()
	approval, _, _ := s.head(t)
	if approval == nil {
		t.Fatal("no approval")
	}
	return approval.Get("result").Get("content").Items()[0].Get("text").Str()
}

func approvalScenes(t *testing.T) map[string]func() *approvalScene {
	t.Helper()
	out := map[string]func() *approvalScene{}
	for _, engine := range []string{"api", "native"} {
		out[engine] = func() *approvalScene {
			return &approvalScene{store: runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{}), engine: engine}
		}
	}
	return out
}

func TestAnApprovedCallRunsWhatThePersonApproved(t *testing.T) {
	big, near := `{"id":9007199254740993}`, `{"id":9007199254740992}`
	for engine, scene := range approvalScenes(t) {
		t.Run(engine, func(t *testing.T) {
			check := func(t *testing.T, s *approvalScene, ran []string, answer string, rows []string) {
				t.Helper()
				if fmt.Sprint(s.ran) != fmt.Sprint(ran) {
					t.Fatalf("the tool ran %q, want %q", s.ran, ran)
				}
				if got := s.answered(t); !strings.HasPrefix(got, answer) {
					t.Fatalf("the approved call was answered %q, want %q", got, answer)
				}
				if _, _, got := s.head(t); fmt.Sprint(got) != fmt.Sprint(rows) {
					t.Fatalf("the rows are %q, want %q", got, rows)
				}
			}
			t.Run("an integer past 2^53, shown and run as written", func(t *testing.T) {
				s := scene()
				s.answers = []string{"ask"}
				s.run(t, false, nil, []string{big})
				shown := ""
				for _, event := range s.events {
					if event.ApprovalRequested != nil {
						shown = string(event.ApprovalRequested.Args["id"])
					}
				}
				if shown != "9007199254740993" {
					t.Fatalf("the person was shown %q", shown)
				}
				s.approve(t)
				check(t, s, []string{big}, "ran "+big, []string{big})
			})
			t.Run("a repeat of a recorded write is answered from its record", func(t *testing.T) {
				s := scene()
				s.answers = []string{"allow", "ask"}
				s.run(t, false, nil, []string{big, big})
				s.approve(t)
				check(t, s, []string{big}, "ran "+big, []string{big})
			})
			t.Run("its neighbour recorded, the integer is its own write", func(t *testing.T) {
				s := scene()
				s.answers = []string{"allow", "ask"}
				s.run(t, false, nil, []string{near, big})
				s.approve(t)
				check(t, s, []string{near, big}, "ran "+big, []string{near, big})
			})
			t.Run("the model repeats what was approved, and the record answers it", func(t *testing.T) {
				s := scene()
				s.answers = []string{"ask"}
				s.run(t, false, nil, []string{big})
				s.approve(t, big)
				if fmt.Sprint(s.ran) != fmt.Sprint([]string{big}) {
					t.Fatalf("the tool ran %q", s.ran)
				}
			})
			t.Run("a spelling a row recorded, the same bytes", func(t *testing.T) {
				s := scene()
				s.answers = []string{"allow", "ask"}
				s.run(t, false, nil, []string{`{"q":1.0}`, `{"q":1.0}`})
				s.approve(t)
				check(t, s, []string{`{"q":1.0}`}, `ran {"q":1.0}`, []string{`{"q":1.0}`})
			})
			t.Run("a spelling approved first, and repeated", func(t *testing.T) {
				s := scene()
				s.answers = []string{"ask"}
				s.run(t, false, nil, []string{`{"q":1.0}`})
				s.approve(t, `{"q":1.0}`)
				if fmt.Sprint(s.ran) != fmt.Sprint([]string{`{"q":1.0}`}) {
					t.Fatalf("the tool ran %q", s.ran)
				}
			})
			t.Run("another spelling of a recorded write is refused after the approval too", func(t *testing.T) {
				s := scene()
				s.answers = []string{"allow", "ask"}
				s.run(t, false, nil, []string{`{"q":1}`, `{"q":1.0}`})
				s.approve(t)
				check(t, s, []string{`{"q":1}`}, "DurableRequestConflict", []string{`{"q":1}`})
			})
			kept := []string{`{"q":0}`, `{"q":null}`, `{"r":0.1000000000000000055}`}
			if engine == "native" { // These run in the race job, and the record is the same on both adapters.
				kept = kept[:1]
			}
			for _, call := range kept {
				t.Run("byte for byte: "+call, func(t *testing.T) {
					s := scene()
					s.answers = []string{"ask"}
					s.run(t, false, nil, []string{call})
					approval, version, _ := s.head(t)
					if approval.Get("request") == nil || approval.Get("request").Str() != call || version != 5 {
						t.Fatalf("the record does not hold the text: version %v, %s", version, approval.Text())
					}
					s.approve(t)
					check(t, s, []string{call}, "ran "+call, []string{call})
				})
			}
			t.Run("what args say is recorded as a published release recorded it", func(t *testing.T) {
				s := scene()
				s.answers = []string{"ask"}
				s.run(t, false, nil, []string{`{"id":7}`})
				approval, version, _ := s.head(t)
				if approval.Has("request") || version != 2 {
					t.Fatalf("version %v, %s", version, approval.Text())
				}
				s.approve(t)
				check(t, s, []string{`{"id":7}`}, `ran {"id":7}`, []string{`{"id":7}`})
			})
			t.Run("what is shown at every depth is what runs", func(t *testing.T) {
				s := scene()
				s.answers = []string{"ask"}
				s.run(t, false, nil, []string{`{"sort":{"name":1,"created":-1},"list":[{"b":2,"a":1}]}`})
				want := `{"list":[{"a":1,"b":2}],"sort":{"created":-1,"name":1}}`
				shown := ""
				for _, event := range s.events {
					if event.ApprovalRequested != nil {
						raw, _ := json.Marshal(event.ApprovalRequested.Args)
						shown = string(raw)
						if got := runcontext.ApprovalEventArguments(*event.ApprovalRequested); got != want {
							t.Fatalf("a host renders %s", got)
						}
					}
				}
				if shown != want {
					t.Fatalf("the person was shown %s", shown)
				}
				s.approve(t)
				check(t, s, []string{want}, "ran "+want, []string{want})
			})
			t.Run("a host renders the text that runs, from a stored event or a stored record", func(t *testing.T) {
				s := scene()
				s.answers = []string{"ask"}
				s.run(t, false, nil, []string{big})
				page, err := s.store.ReadEvents(context.Background(), "run-approval", "", 100)
				if err != nil {
					t.Fatal(err)
				}
				stored := false
				for _, item := range page.Items {
					var envelope struct {
						Event json.RawMessage `json:"event"`
					}
					var event contract.RunEvent
					if json.Unmarshal(item.Bytes, &envelope) == nil && json.Unmarshal(envelope.Event, &event) == nil && event.ApprovalRequested != nil {
						stored = true
						if got := runcontext.ApprovalEventArguments(*event.ApprovalRequested); got != big {
							t.Fatalf("a host renders %s from a stored event", got)
						}
						if err := contract.CheckRecord("event", envelope.Event); err != nil {
							t.Fatal(err)
						}
					}
				}
				if !stored {
					t.Fatal("no stored event")
				}
				head, err := s.store.Load(context.Background(), "run-approval")
				if err != nil {
					t.Fatal(err)
				}
				var saved contract.Checkpoint
				if err = json.Unmarshal(head.Checkpoint, &saved); err != nil {
					t.Fatal(err)
				}
				if got := runcontext.ApprovalShownArguments(*saved.Approval); got != big {
					t.Fatalf("a host renders %s from the stored record", got)
				}
			})
			// A host that builds its card and its approval reference from the checkpoint it stored.
			for _, row := range []struct{ name, call, shown string }{
				{"an integer past 2^53", big, big},
				{"an amount", `{"amount":10.50}`, `{"amount":10.50}`},
				{"a nested object", `{"sort":{"name":1,"created":-1}}`, `{"sort":{"created":-1,"name":1}}`},
			} {
				if engine == "native" && row.call != big { // The record is the same on both adapters.
					continue
				}
				t.Run("a host's card from its checkpoint: "+row.name, func(t *testing.T) {
					s := scene()
					s.answers = []string{"ask"}
					s.run(t, false, nil, []string{row.call})
					head, err := s.store.Load(context.Background(), "run-approval")
					if err != nil {
						t.Fatal(err)
					}
					var saved contract.Checkpoint
					if err = json.Unmarshal(head.Checkpoint, &saved); err != nil {
						t.Fatal(err)
					}
					card, reference := runcontext.ApprovalShownArguments(*saved.Approval), runcontext.ApprovalShownDigest(*saved.Approval)
					if card != row.shown {
						t.Fatalf("the card shows %s", card)
					}
					s.approve(t)
					if len(s.ran) != 1 || s.ran[0] != card {
						t.Fatalf("the card shows %s, the tool received %q", card, s.ran)
					}
					if got := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(s.ran[0]))); got != reference {
						t.Fatalf("the reference is %s, what ran digests as %s", reference, got)
					}
				})
			}
			// A host that has not moved: its card and its reference are over args, and its decision proves nothing.
			for _, call := range []string{big, `{"amount":10.50}`} {
				if engine == "native" && call != big {
					continue
				}
				t.Run("a host that has not moved runs nothing: "+call, func(t *testing.T) {
					s := scene()
					s.answers = []string{"ask"}
					s.run(t, false, nil, []string{call})
					before, _ := s.store.Load(context.Background(), "run-approval")
					approval, _, _ := s.head(t)
					card := approval.Get("args").Text()
					if card == call {
						t.Fatalf("the unmoved card already shows %s", card)
					}
					wrong := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(card)))
					for _, shown := range []*string{nil, &wrong} {
						s.events = nil
						s.run(t, true, &contract.CheckpointApprovalDecision{CallID: approval.Get("callId").Str(), Approved: true, ShownDigest: shown}, nil)
						if len(s.ran) != 0 {
							t.Fatalf("the tool ran %q", s.ran)
						}
						named := false
						for _, event := range s.events {
							if event.Error != nil && strings.Contains(event.Error.Message, "ApprovalNotShown") && strings.Contains(event.Error.Message, "approvalShownDigest") {
								named = true
							}
						}
						if !named {
							t.Fatalf("no named refusal: %+v", s.events)
						}
						after, _ := s.store.Load(context.Background(), "run-approval")
						if after.Revision != before.Revision {
							t.Fatal("the pause moved")
						}
					}
					// The same pause, once the host has moved.
					s.approve(t)
					if len(s.ran) != 1 || s.ran[0] != call {
						t.Fatalf("the tool ran %q", s.ran)
					}
				})
			}
			t.Run("a person's refusal needs no digest", func(t *testing.T) {
				s := scene()
				s.answers = []string{"ask"}
				s.run(t, false, nil, []string{big})
				approval, _, _ := s.head(t)
				s.run(t, true, &contract.CheckpointApprovalDecision{CallID: approval.Get("callId").Str(), Approved: false}, nil)
				if got, _, _ := s.head(t); got.Get("state").Str() == "asked" || len(s.ran) != 0 {
					t.Fatalf("%s, ran %q", got.Text(), s.ran)
				}
			})
			t.Run("no text kept: a host that has not moved goes on as it was", func(t *testing.T) {
				s := scene()
				s.answers = []string{"ask"}
				s.run(t, false, nil, []string{`{"id":7}`})
				approval, version, _ := s.head(t)
				if approval.Has("request") || version != 2 {
					t.Fatalf("version %v, %s", version, approval.Text())
				}
				s.run(t, true, &contract.CheckpointApprovalDecision{CallID: approval.Get("callId").Str(), Approved: true}, nil)
				if len(s.ran) != 1 || s.ran[0] != `{"id":7}` {
					t.Fatalf("the tool ran %q", s.ran)
				}
			})
			// MEDIUM-11: one form shown, run and digested, for what encoding/json would have written otherwise.
			for _, call := range []string{`{"a":"x&y"}`, `{"html":"<b>&</b>"}`, `{"ls":"` + string(rune(0x2028)) + `"}`, `{"` + string(rune(0x1F600)) + `":1,"` + string(rune(0xFFEE)) + `":2}`} {
				if engine == "native" && !strings.Contains(call, "&") {
					continue
				}
				t.Run("one form shown, run and digested: "+call, func(t *testing.T) {
					s := scene()
					s.answers = []string{"ask"}
					s.run(t, false, nil, []string{call})
					head, err := s.store.Load(context.Background(), "run-approval")
					if err != nil {
						t.Fatal(err)
					}
					var saved contract.Checkpoint
					if err = json.Unmarshal(head.Checkpoint, &saved); err != nil {
						t.Fatal(err)
					}
					card, reference := runcontext.ApprovalShownArguments(*saved.Approval), runcontext.ApprovalShownDigest(*saved.Approval)
					s.approve(t)
					if len(s.ran) != 1 || s.ran[0] != card {
						t.Fatalf("the card shows %s, the tool received %q", card, s.ran)
					}
					_, _, rows := s.head(t)
					if len(rows) != 1 || fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(rows[0]))) != reference {
						t.Fatalf("the reference is %s, the row is %q", reference, rows)
					}
				})
			}
			t.Run("whitespace and member order run as the person was shown them", func(t *testing.T) {
				s := scene()
				s.answers = []string{"ask"}
				s.run(t, false, nil, []string{` { "b" : 2 , "a" : 1 } `})
				approval, version, _ := s.head(t)
				if approval.Has("request") || version != 2 {
					t.Fatalf("version %v, %s", version, approval.Text())
				}
				s.approve(t)
				check(t, s, []string{`{"a":1,"b":2}`}, `ran {"a":1,"b":2}`, []string{`{"a":1,"b":2}`})
			})
			refused := []string{`{"q":1e400}`, `{"a":1,"a":2}`, `{"q":"` + string(rune(0x5c)) + `ud800"}`, `{"ID":1,"id":2}`, `{"o":[{"id":2,"ID":1}]}`}
			if engine == "native" {
				refused = refused[:1]
			}
			for _, call := range refused {
				t.Run("what the record cannot hold is refused before anyone is asked: "+call, func(t *testing.T) {
					s := scene()
					s.answers = []string{"ask"}
					s.run(t, false, nil, []string{call})
					approval, _, rows := s.head(t)
					if approval != nil || len(s.ran) != 0 || len(rows) != 0 {
						t.Fatalf("approval %v, ran %q, rows %q", approval, s.ran, rows)
					}
					refused := false
					for _, event := range s.events {
						if event.ToolResult != nil && event.ToolResult.IsError && strings.Contains(event.ToolResult.Text, "DurableRequestUnrecordable") {
							refused = true
						}
						if event.ApprovalRequested != nil {
							t.Fatal("the person was asked")
						}
					}
					if !refused {
						t.Fatal("no refusal")
					}
				})
			}
		})
	}
}

// A paused approval a published release wrote: what the parsed form may have changed is refused, not run.
func TestAnApprovalAPublishedReleaseWroteRefusesRatherThanRunAnotherNumber(t *testing.T) {
	raw, err := os.ReadFile("../../../contract/testdata/durable-writes/published-approvals.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Checkpoints map[string]string `json:"checkpoints"`
	}
	if err = json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	big := `{"id":9007199254740993}`
	const stale = "holds only"
	for _, row := range []struct {
		name, engine string
		ran          []string
		answer       string
		rows         []string
	}{
		{"repeat past 2^53", "api", nil, stale, []string{big}},
		{"repeat past 2^53, native", "native", nil, stale, []string{big}},
		{"first past 2^53", "api", nil, stale, nil},
		{"out of range", "api", nil, stale, nil},
		{"zero", "api", nil, stale, nil},
		{"a spelling a row recorded", "api", nil, "DurableRequestConflict", []string{`{"q":1.0}`}},
		{"a repeat past a float64's precision", "api", nil, "DurableRequestConflict", []string{`{"id":7.0000000000000000001}`}},
		{"a repeat past a float64's precision, with an exponent", "api", nil, "DurableRequestConflict", []string{`{"n":1.00000000000000000001e2}`}},
		{"ordinary", "api", []string{`{"id":7}`}, `ran {"id":7}`, []string{`{"id":7}`}},
		{"ordinary, native", "native", []string{`{"id":7}`}, `ran {"id":7}`, []string{`{"id":7}`}},
	} {
		t.Run(row.name, func(t *testing.T) {
			store := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
			checkpoint := doc.Checkpoints[row.name]
			if checkpoint == "" {
				t.Fatal("no checkpoint")
			}
			if _, err := store.Commit(context.Background(), runcontext.RunCommit{RunID: "run-approval", CommitID: "published", Checkpoint: []byte(checkpoint), Events: [][]byte{}}); err != nil {
				t.Fatal(err)
			}
			s := &approvalScene{store: store, engine: row.engine}
			s.approve(t)
			if fmt.Sprint(s.ran) != fmt.Sprint(row.ran) {
				t.Fatalf("the tool ran %q, want %q", s.ran, row.ran)
			}
			if got := s.answered(t); !strings.Contains(got, row.answer) {
				t.Fatalf("answered %q, want %q", got, row.answer)
			}
			if _, _, rows := s.head(t); fmt.Sprint(rows) != fmt.Sprint(row.rows) {
				t.Fatalf("rows %q, want %q", rows, row.rows)
			}
		})
	}
}

// A finished run that kept a text exports and reads as any other: its report is version 3 of its own checkpoint.
func TestAKeptTextsRunExportsAnArchive(t *testing.T) {
	s := &approvalScene{store: runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{}), engine: "api", answers: []string{"ask"}}
	s.run(t, false, nil, []string{`{"id":9007199254740993}`})
	s.approve(t)
	result, err := runcontext.ExportRunArchive(context.Background(), s.store.(*runcontext.MemoryRunStore), "run-approval", runcontext.ArchiveExportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runcontext.ReadRunArchive(result.Bytes, result.Digest); err != nil {
		t.Fatal(err)
	}
}
