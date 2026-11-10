package completetests

// #340 on a real eino run: a run asks the person a question and completes; its successor run continues the same response
// with the person's answer under the allowance the person approved for the predecessor, and is made once. The unit
// tests and the question wait's continuation (#341) are go/context/run_successor_test.go; the TypeScript twin is
// packages/harness/storage/successor.test.ts.

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/eino"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
	"github.com/Judgment-Pack/judgment-pack-agent/go/relay"
)

const askingRun = "run-asks"
const successorQuestion = `{"title":"Note","question":"Which note should be written?","selection":"single","options":[{"id":"a","label":"The first","description":"One."},{"id":"b","label":"The second","description":"Two."}]}`

type successorScene struct {
	mu      sync.Mutex
	store   *runcontext.MemoryRunStore
	prompts []string
	events  []contract.RunEvent
}

// texts are every string a request body holds: the messages the model is sent.
func texts(value any, out *[]string) {
	switch held := value.(type) {
	case string:
		*out = append(*out, held)
	case []any:
		for _, item := range held {
			texts(item, out)
		}
	case map[string]any:
		for _, item := range held {
			texts(item, out)
		}
	}
}

// run is one run on eino, whose allowance is counted in model requests: it asks the person the question, or writes
// its answer.
func (s *successorScene) run(t *testing.T, input *runcontext.Input, ask bool) error {
	t.Helper()
	s.events, s.prompts = nil, nil
	g := gate.New(gate.Options{})
	g.SetOffer(contract.ToolOffer{})
	asker := runcontext.HostTool{Name: "ask_person", Description: "ask_person", Interaction: "question", InputSchema: json.RawMessage(`{"type":"object"}`), Taxonomy: &runcontext.ToolTaxonomy{Operation: "ask", Effect: "pure"}}
	input.CurrentRequest = &runcontext.CurrentRequest{Message: "Write the note.", Input: "note"}
	run := &adapter.Run{Wire: "openai-compatible", ModelName: "fixture", Gate: g.Calls(), Thinking: "off", Prompt: "Write the note.", AllowConversation: true, Interactive: true, Critic: "off", HostTools: []runcontext.HostTool{asker},
		EnrollContext: func(_ context.Context, offer adapter.ContextOffer) error {
			return g.EnrollContext(offer.ID, offer.HostTools)
		},
		Context: input,
		Emit: func(event contract.RunEvent) {
			s.mu.Lock()
			s.events = append(s.events, event)
			s.mu.Unlock()
		}}
	run.Model = func(_ context.Context, _ string, request relay.Request) (*http.Response, error) {
		var body any
		_ = json.Unmarshal(request.Body, &body)
		sent := []string{}
		texts(body, &sent)
		s.mu.Lock()
		s.prompts = append(s.prompts, strings.Join(sent, "\n"))
		s.mu.Unlock()
		if ask {
			return sse(map[string]any{"id": "fixture", "object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "call-ask", "type": "function", "function": map[string]any{"name": "ask_person", "arguments": successorQuestion}}}}, "finish_reason": "tool_calls"}}}), nil
		}
		return sse(map[string]any{"id": "fixture", "object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": "The second note."}, "finish_reason": "stop"}}}), nil
	}
	return eino.New().Run(context.Background(), run)
}

func (s *successorScene) head(t *testing.T, run string) (*runcontext.RunHead, contract.Checkpoint) {
	t.Helper()
	head, err := s.store.Load(context.Background(), run)
	if err != nil || head == nil || head.Checkpoint == nil {
		t.Fatal(head, err)
	}
	var saved contract.Checkpoint
	if err := json.Unmarshal(head.Checkpoint, &saved); err != nil {
		t.Fatal(err)
	}
	return head, saved
}

func (s *successorScene) emitted(t *testing.T) string {
	t.Helper()
	out := []string{}
	for _, event := range s.events {
		raw, _ := json.Marshal(event)
		value, err := runcontext.ParseJSON(raw)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, runcontext.CanonicalJSON(value))
	}
	return strings.Join(out, "\n")
}

// successorRefusal is the vector's refusal for a reason, as Go emits it: the error event, an empty usage, the end.
func successorRefusal(t *testing.T, reason string) string {
	t.Helper()
	raw, err := os.ReadFile("../../../contract/vectors/checkpoint/successor.json")
	if err != nil {
		t.Fatal(err)
	}
	var vector struct {
		Reasons []string
		Items   []json.RawMessage
	}
	if err := json.Unmarshal(raw, &vector); err != nil {
		t.Fatal(err)
	}
	for i, held := range vector.Reasons {
		if held == reason {
			event, _ := runcontext.ParseJSON(vector.Items[i])
			end, _ := runcontext.ParseJSON(vector.Items[len(vector.Items)-1])
			return runcontext.CanonicalJSON(event) + "\n" + `{"type":"usage","usage":{"rows":[]},"version":1}` + "\n" + runcontext.CanonicalJSON(end)
		}
	}
	t.Fatal("no such reason", reason)
	return ""
}

func TestASuccessorRunContinuesUnderTheApprovedAllowance(t *testing.T) {
	ctx := context.Background()
	s := &successorScene{store: runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})}
	if err := s.run(t, &runcontext.Input{RunStore: &runcontext.DurableRunInput{Store: s.store, ID: askingRun}}, true); err != nil {
		t.Fatal(err)
	}
	asked := ""
	for _, event := range s.events {
		if event.UserQuestion != nil {
			asked = event.UserQuestion.ID
		}
	}
	head, saved := s.head(t, askingRun)
	if asked == "" || saved.Status != "complete" {
		t.Fatalf("the predecessor: %s, asked %q: %s", saved.Status, asked, s.emitted(t))
	}
	// The predecessor stands for one the person granted more requests: at the default allowance's end, raised by one
	// grant, its 20th request the one that asked.
	var raw map[string]any
	_ = json.Unmarshal(head.Checkpoint, &raw)
	raw["used"], raw["limit"], raw["grants"] = 20, 30, []any{map[string]any{"from": 20, "to": 30, "at": "2026-01-01T00:00:00.000Z"}}
	patched, _ := json.Marshal(raw)
	value, _ := runcontext.ParseJSON(patched)
	if _, signed := raw["digest"]; signed {
		raw["digest"] = runcontext.RecordDigest(value)
		patched, _ = json.Marshal(raw)
		value, _ = runcontext.ParseJSON(patched)
	}
	if _, err := runcontext.ReadExecution(value); err != nil {
		t.Fatal(err, string(head.Checkpoint))
	}
	receipt, err := s.store.Commit(ctx, runcontext.RunCommit{RunID: askingRun, ExpectedRevision: head.Revision, CommitID: "granted", Checkpoint: []byte(runcontext.CanonicalJSON(value))})
	if err != nil {
		t.Fatal(err)
	}
	successor := runcontext.SuccessorRunID(askingRun, asked)
	open := func(answer contract.QuestionDraft) *runcontext.Input {
		return &runcontext.Input{RunStore: &runcontext.DurableRunInput{Store: s.store, ID: successor, Successor: &runcontext.RunSuccessor{Predecessor: askingRun, Question: asked, Answer: answer}}}
	}
	// An option the question did not offer is no answer: refused by name, nothing asked, nothing written.
	if err := s.run(t, open(contract.QuestionDraft{Selected: []string{"c"}, Text: ""}), false); err != nil {
		t.Fatal(err)
	}
	if got := s.emitted(t); got != successorRefusal(t, "unbound") || len(s.prompts) != 0 {
		t.Fatalf("an unoffered option: %s", got)
	}
	if made, _ := s.store.Load(ctx, successor); made != nil {
		t.Fatal("a refused successor was written")
	}
	if err := s.run(t, open(contract.QuestionDraft{Selected: []string{"b"}, Text: ""}), false); err != nil {
		t.Fatal(err)
	}
	// The one request holds the answer once, and is not paused: the carried grant still holds.
	if len(s.prompts) != 1 || strings.Count(s.prompts[0], runcontext.QuestionAnswered) != 1 || !strings.Contains(s.prompts[0], `{"answer":{"selected":["b"],"text":""},"question":{`) {
		t.Fatalf("the successor's requests: %q", s.prompts)
	}
	for _, event := range s.events {
		if event.BudgetPaused != nil || event.Error != nil {
			t.Fatalf("the successor: %s", s.emitted(t))
		}
	}
	_, carried := s.head(t, successor)
	if carried.Status != "complete" || carried.Used != 21 || carried.Limit != 30 || len(carried.Grants) != 1 || carried.Grants[0] != (contract.CheckpointGrant{From: 20, To: 30, At: "2026-01-01T00:00:00.000Z"}) || carried.ID != successor {
		t.Fatalf("the successor's head: %+v", carried)
	}
	// Made once: a second is refused by name, and the predecessor never moved.
	if err := s.run(t, open(contract.QuestionDraft{Selected: []string{"a"}, Text: ""}), false); err != nil {
		t.Fatal(err)
	}
	if got := s.emitted(t); got != successorRefusal(t, "succeeded") || len(s.prompts) != 0 {
		t.Fatalf("a second successor: %s", got)
	}
	if after, _ := s.head(t, askingRun); after.Revision != receipt.Revision {
		t.Fatal("the predecessor moved")
	}
}
