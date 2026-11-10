package durableapprovaltests

// #343: a durable call whose result the record guard redacts (ADR-0018) is recorded in that form, and the
// run's events and the model are given the same form, so an approved act that ran settles rather than
// ending unknown, and nothing runs it again. Both Go adapters; recordedResults.test.ts holds the
// TypeScript.

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
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

const (
	recordedRaw      = "https://packs.example/tmp/evaluation-pack"
	recordedRedacted = "https://packs.example[redacted: local-temp-path]"
	recordedRawPath  = "packs.example/tmp/"
)

// recordedScene is one run of one evaluate tool, and everything the run gave out: its events, the model's
// requests (api) and the tool answers the agent was given (native).
type recordedScene struct {
	store    runcontext.RunStore
	engine   string
	read     bool
	id       string
	answers  []string
	ran      int
	events   []contract.RunEvent
	requests []string
	given    []string
}

func (s *recordedScene) run(t *testing.T, resume bool, decision *contract.CheckpointApprovalDecision) {
	t.Helper()
	effect := map[bool]string{false: "act", true: "read"}[s.read]
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(`[{"description":"Evaluate a pack.","inputSchema":{"type":"object"},"name":"evaluate","taxonomy":{"effect":"`+effect+`","operation":"`+effect+`"}}]`)))
	tool := runcontext.HostTool{Name: "evaluate", Taxonomy: &runcontext.ToolTaxonomy{Operation: effect, Effect: effect}, Description: "Evaluate a pack.",
		InputSchema: json.RawMessage(`{"type":"object"}`), EffectClass: effect,
		Source: &runcontext.ToolSource{Kind: s.source(), ID: s.sourceID(), Trust: "connected", Version: "2026-10", ToolListDigest: digest, CurrentDigest: func(context.Context) (string, error) { return digest, nil }},
		Execute: func(_ context.Context, _ json.RawMessage) (runcontext.ToolResult, error) {
			s.ran++
			text := `{"packId":"` + s.id + `","status":"evaluated"}`
			return runcontext.ResultOf([]runcontext.Content{runcontext.TextContent(text)}, runcontext.MustParseJSON(text)), nil
		}}
	if !s.read {
		tool.Approval = "required"
		tool.Approve = func(runcontext.ApprovalRequest) string {
			if len(s.answers) == 0 {
				return "allow"
			}
			answer := s.answers[0]
			s.answers = s.answers[1:]
			return answer
		}
	}
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
		Prompt: "Evaluate the pack.", AllowConversation: true, Interactive: true, Critic: "off", HostTools: []runcontext.HostTool{tool},
		Context: &runcontext.Input{
			CurrentRequest: &runcontext.CurrentRequest{Message: "Evaluate the pack.", Input: "Evaluate the pack."},
			Connection:     "provider", ConnectionRevisions: map[string]string{"provider": "one"},
			RunStore: &runcontext.DurableRunInput{Store: s.store, ID: "run-recorded", Resume: resume},
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
				if strings.Contains(offered.Description, "Evaluate a pack.") {
					alias = offered.Name
				}
			}
			answer, err := cb.Tool(ctx, adapter.AgentToolCall{ID: fmt.Sprintf("call-%v", resume), Name: alias, Arguments: json.RawMessage(`{"pack":"evaluation-pack"}`)})
			if err != nil {
				return err
			}
			s.given = append(s.given, answer.Text)
			return cb.Event(ctx, adapter.AgentEvent{Type: "message", ID: "final", Text: "Done.", Phase: "final"})
		}}
	} else {
		implementation = eino.New()
		run.Model = func(_ context.Context, _ string, request relay.Request) (*http.Response, error) {
			requests++
			s.requests = append(s.requests, string(request.Body))
			if requests == 1 {
				return readKeyCall(fmt.Sprintf("call_%v", resume), s.sourceID()+"__evaluate", `{"pack":"evaluation-pack"}`), nil
			}
			return quarantineFinal(), nil
		}
	}
	if err := implementation.Run(legContext(t), run); err != nil {
		t.Fatal(err)
	}
}

// source is the tool's source: an act is a gateway's, and a read a local one's, as the taxonomy requires.
func (s *recordedScene) source() string {
	return map[bool]string{false: "gateway", true: "custom"}[s.read]
}

func (s *recordedScene) sourceID() string {
	return map[bool]string{false: "ado", true: "records"}[s.read]
}

// stored is the stored run's checkpoint text, its approval and its one host row.
func (s *recordedScene) stored(t *testing.T) (text string, approval, row *runcontext.Value) {
	t.Helper()
	head, err := s.store.Load(context.Background(), "run-recorded")
	if err != nil || head == nil {
		t.Fatal("no stored run", err)
	}
	saved := runcontext.MustParseJSON(string(head.Checkpoint))
	for _, held := range saved.Get("durable").Get("dispatches").Items() {
		if held.Get("kind").Str() == "host" {
			if row != nil {
				t.Fatal("two host rows")
			}
			row = held
		}
	}
	return string(head.Checkpoint), saved.Get("approval"), row
}

// approve decides the asked call, approved, as a host that has moved to the readers does.
func (s *recordedScene) approve(t *testing.T) {
	t.Helper()
	head, err := s.store.Load(context.Background(), "run-recorded")
	if err != nil || head == nil {
		t.Fatal("no stored run", err)
	}
	var saved contract.Checkpoint
	if err = json.Unmarshal(head.Checkpoint, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Approval == nil || saved.Approval.State != "asked" {
		t.Fatalf("no asked approval: %v", saved.Approval)
	}
	s.events = nil
	digest := runcontext.ApprovalShownDigest(*saved.Approval)
	s.run(t, true, &contract.CheckpointApprovalDecision{CallID: saved.Approval.CallID, Approved: true, ShownDigest: &digest})
}

// toolResults is the text of each tool_result event the run emitted.
func (s *recordedScene) toolResults(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, event := range s.events {
		if event.ToolResult != nil {
			text, err := json.Marshal(event.ToolResult)
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, string(text))
		}
	}
	return out
}

// given is what the model was given: the request bodies after the first (api), or the tool answers (native).
func (s *recordedScene) modelInput() []string {
	if s.engine == "native" {
		return s.given
	}
	if len(s.requests) < 2 {
		return nil
	}
	return s.requests[1:]
}

func TestARedactedResultSettlesAndIsWhatTheModelIsGiven(t *testing.T) {
	for _, engine := range []string{"api", "native"} {
		for _, read := range []bool{false, true} {
			name := engine + map[bool]string{false: ", an approved act", true: ", a read"}[read]
			t.Run(name, func(t *testing.T) {
				s := &recordedScene{store: runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{}), engine: engine, read: read, id: recordedRaw}
				if read {
					s.run(t, false, nil)
				} else {
					s.answers = []string{"ask"}
					s.run(t, false, nil)
					if s.ran != 0 {
						t.Fatal("the act ran before it was approved")
					}
					s.approve(t)
				}
				if s.ran != 1 {
					t.Fatalf("the tool ran %d times", s.ran)
				}
				text, approval, row := s.stored(t)
				if row == nil || row.Get("state").Str() != "result" {
					t.Fatalf("the call did not settle: %v", row)
				}
				want := `{"packId":"` + recordedRedacted + `","status":"evaluated"}`
				if got := row.Get("result").Get("content").Items()[0].Get("text").Str(); got != want {
					t.Fatalf("recorded %q", got)
				}
				if got := row.Get("result").Get("structuredContent").Get("packId").Str(); got != recordedRedacted {
					t.Fatalf("recorded structured %q", got)
				}
				if !read && approval.Get("result").Get("content").Items()[0].Get("text").Str() != want {
					t.Fatalf("the approval was answered %s", approval.Get("result").Text())
				}
				if strings.Contains(text, recordedRawPath) {
					t.Fatal("the stored run holds the redacted text")
				}
				results := s.toolResults(t)
				if len(results) == 0 {
					t.Fatal("no tool_result event")
				}
				for _, result := range results {
					if strings.Contains(result, recordedRawPath) || !strings.Contains(result, "[redacted: local-temp-path]") || strings.Contains(result, "StorageOutcomeUnknown") {
						t.Fatalf("a tool_result event gave %s", result)
					}
				}
				given := s.modelInput()
				if len(given) == 0 {
					t.Fatal("the model was given nothing")
				}
				for _, input := range given {
					if strings.Contains(input, recordedRawPath) || !strings.Contains(input, "[redacted: local-temp-path]") || strings.Contains(input, "StorageOutcomeUnknown") {
						t.Fatalf("the model was given %s", input)
					}
				}
				// A resumed run is answered from the record and the act is not run again.
				if !read {
					s.events = nil
					s.run(t, true, nil)
					if s.ran != 1 {
						t.Fatalf("the act ran %d times after a resume", s.ran)
					}
				}
			})
		}
	}
}

func TestAResultWithNothingRedactedIsGivenAsItWas(t *testing.T) {
	for _, engine := range []string{"api", "native"} {
		t.Run(engine, func(t *testing.T) {
			kept := "https://packs.example/var/evaluation-pack"
			s := &recordedScene{store: runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{}), engine: engine, read: true, id: kept}
			s.run(t, false, nil)
			_, _, row := s.stored(t)
			want := `{"packId":"` + kept + `","status":"evaluated"}`
			if row == nil || row.Get("state").Str() != "result" || row.Get("result").Get("content").Items()[0].Get("text").Str() != want {
				t.Fatalf("recorded %v", row)
			}
			for _, input := range s.modelInput() {
				if !strings.Contains(input, strings.ReplaceAll(want, `"`, `\"`)) && !strings.Contains(input, want) {
					t.Fatalf("the model was given %s", input)
				}
			}
		})
	}
}
