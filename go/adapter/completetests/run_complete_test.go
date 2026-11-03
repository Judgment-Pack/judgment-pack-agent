package completetests

// #328: a resume of a run whose run-store head is complete is refused by name before anything of the run is bound, on
// eino and on the agent adapter, each plain and through the delegation coordinator. The TypeScript twin is
// packages/harness/storage/startRefusal.test.ts. The one resume of a complete head, a job wait's continuation, is pinned
// where the job tools are (go/adapter/jobtests, through ResumeParked).

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	native "github.com/Judgment-Pack/judgment-pack-agent/go/adapter/agent"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/delegate"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/eino"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
	"github.com/Judgment-Pack/judgment-pack-agent/go/relay"
)

const completeRun = "run-complete"

// scene is one durable run with a host write the model asks for on every request it makes.
type scene struct {
	mu     sync.Mutex
	store  *runcontext.MemoryRunStore
	asks   int
	writes int
	events []contract.RunEvent
}

func (s *scene) tool() runcontext.HostTool {
	return runcontext.HostTool{Name: "write_note", Description: "write_note", InputSchema: json.RawMessage(`{"type":"object"}`), Taxonomy: &runcontext.ToolTaxonomy{Operation: "own-state", Effect: "pure"},
		Execute: func(context.Context, json.RawMessage) (runcontext.ToolResult, error) {
			s.mu.Lock()
			s.writes++
			s.mu.Unlock()
			return runcontext.ResultOf([]runcontext.Content{runcontext.TextContent("written")}, nil), nil
		}}
}

func sse(chunk map[string]any) *http.Response {
	raw, _ := json.Marshal(chunk)
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: " + string(raw) + "\n\ndata: [DONE]\n\n"))}
}

// run drives the engine once: each request or invocation asks for one write, then the next answers.
func (s *scene) run(t *testing.T, engine string, delegating, resume bool) error {
	t.Helper()
	g := gate.New(gate.Options{})
	g.SetOffer(contract.ToolOffer{})
	run := &adapter.Run{Wire: "openai-compatible", ModelName: "fixture", Gate: g.Calls(), Thinking: "off", Prompt: "Write the note.", AllowConversation: true, Critic: "off", HostTools: []runcontext.HostTool{s.tool()},
		EnrollContext: func(_ context.Context, offer adapter.ContextOffer) error {
			return g.EnrollContext(offer.ID, offer.HostTools)
		},
		Context: &runcontext.Input{CurrentRequest: &runcontext.CurrentRequest{Message: "Write the note.", Input: "note"}, RunStore: &runcontext.DurableRunInput{Store: s.store, ID: completeRun, Resume: resume}},
		Emit: func(event contract.RunEvent) {
			s.mu.Lock()
			s.events = append(s.events, event)
			s.mu.Unlock()
		}}
	var implementation adapter.Adapter
	if engine == "agent" {
		implementation = native.New()
		run.Agent = &adapter.Agent{Model: "fixture", Run: func(ctx context.Context, request adapter.AgentRequest, cb adapter.AgentCallbacks) error {
			if cb.ProviderRelease != nil {
				defer cb.ProviderRelease.Acknowledge(cb.ProviderRelease.Invocation)
			}
			s.mu.Lock()
			s.asks++
			s.mu.Unlock()
			for _, tool := range request.Tools {
				if strings.HasPrefix(tool.Description, "write_note:") {
					if _, err := cb.Tool(ctx, adapter.AgentToolCall{ID: "wire-write", Name: tool.Name, Arguments: json.RawMessage(`{"n":1}`)}); err != nil {
						return err
					}
				}
			}
			return cb.Event(ctx, adapter.AgentEvent{Type: "message", ID: "final", Text: "Done.", Phase: "final"})
		}}
	} else {
		implementation = eino.New()
		run.Model = func(_ context.Context, _ string, request relay.Request) (*http.Response, error) {
			s.mu.Lock()
			s.asks++
			at := s.asks
			s.mu.Unlock()
			if at%2 == 1 {
				return sse(map[string]any{"id": "fixture", "object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "call-write", "type": "function", "function": map[string]any{"name": "write_note", "arguments": `{"n":1}`}}}}, "finish_reason": "tool_calls"}}}), nil
			}
			return sse(map[string]any{"id": "fixture", "object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": "Done."}, "finish_reason": "stop"}}}), nil
		}
	}
	if delegating {
		implementation = delegate.With(implementation, delegate.Options{})
	}
	return implementation.Run(context.Background(), run)
}

func (s *scene) status(t *testing.T) (string, string) {
	t.Helper()
	head, err := s.store.Load(context.Background(), completeRun)
	if err != nil || head == nil || head.Checkpoint == nil {
		t.Fatal(head, err)
	}
	var saved contract.Checkpoint
	if err := json.Unmarshal(head.Checkpoint, &saved); err != nil {
		t.Fatal(err)
	}
	return saved.Status, head.Revision
}

func vectorEvents(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile("../../../contract/vectors/checkpoint/run-complete.json")
	if err != nil {
		t.Fatal(err)
	}
	var vector struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(raw, &vector); err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, item := range vector.Items {
		value, err := runcontext.ParseJSON(item)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, runcontext.CanonicalJSON(value))
	}
	return out
}

func (s *scene) emitted(t *testing.T) string {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []string{}
	for _, event := range s.events {
		raw, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		value, err := runcontext.ParseJSON(raw)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, runcontext.CanonicalJSON(value))
	}
	return strings.Join(out, "\n")
}

func TestAResumeOfACompleteHeadIsRefusedBeforeAnything(t *testing.T) {
	want := vectorEvents(t)
	for _, engine := range []string{"eino", "agent"} {
		for _, delegating := range []bool{false, true} {
			name := engine + "/plain"
			if delegating {
				name = engine + "/through the coordinator"
			}
			t.Run(name, func(t *testing.T) {
				s := &scene{store: runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})}
				if err := s.run(t, engine, delegating, false); err != nil {
					t.Fatal(err)
				}
				if head, _ := s.store.Load(context.Background(), completeRun); head == nil {
					t.Fatalf("no head after the first run: %s", s.emitted(t))
				}
				status, revision := s.status(t)
				if status != "complete" || s.writes != 1 {
					t.Fatalf("the first run: %s, %d writes, events %s", status, s.writes, s.emitted(t))
				}
				asks, log := s.asks, s.logged(t)
				s.events = nil
				if err := s.run(t, engine, delegating, true); err != nil {
					t.Fatal(err)
				}
				status, after := s.status(t)
				// The refusal is the vector's event, once, and the run ends: an empty usage report is all else.
				if got := s.emitted(t); got != want[0]+"\n"+`{"type":"usage","usage":{"rows":[]},"version":1}`+"\n"+want[1] {
					t.Fatalf("the resume emitted\n%s\nnot\n%s", got, want)
				}
				if s.asks != asks || s.writes != 1 || status != "complete" || after != revision || s.logged(t) != log {
					t.Fatalf("the resume asked %d more, wrote %d, moved the head %v, logged %d more", s.asks-asks, s.writes-1, after != revision, s.logged(t)-log)
				}
			})
		}
	}
}

func (s *scene) logged(t *testing.T) int {
	t.Helper()
	count := 0
	for after := ""; ; {
		page, err := s.store.ReadEvents(context.Background(), completeRun, after, 100)
		if err != nil {
			t.Fatal(err)
		}
		count += len(page.Items)
		if !page.HasMore {
			return count
		}
		after = page.NextCursor
	}
}
