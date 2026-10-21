package agent_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/agent"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
)

// Lock-removal evidence: these cases were run with -race -count=3 on scratch
// copies with the snapshot/writer locks removed and with CriticSwitch.mu removed.
// Both mutations reported data races; pr.md quotes the observed stacks and edits.
func TestConcurrentReviewRequestsThroughTheAgent(t *testing.T) {
	for _, tc := range []struct {
		name       string
		calls      int
		already    bool
		wantTaken  int
		wantDenied int
	}{
		{"two first requests", 2, false, 1, 1},
		{"fifteen refused requests", 15, true, 0, 15},
		{"128 concurrent requests", 128, false, 1, 127},
	} {
		t.Run(tc.name, func(t *testing.T) {
			type savedCheckpoint struct {
				state contract.Checkpoint
				body  []byte
			}
			var saves []savedCheckpoint
			var savesMu sync.Mutex
			checkSaved := func(savedCopies []savedCheckpoint) {
				for i, saved := range savedCopies {
					body, err := json.Marshal(saved.state)
					if err != nil || !bytes.Equal(body, saved.body) {
						t.Errorf("checkpoint %d changed after its save returned: %s -> %s (%v)", i, saved.body, body, err)
					}
				}
			}
			checkpoint := func(_ context.Context, state contract.Checkpoint) error {
				savesMu.Lock()
				defer savesMu.Unlock()
				if len(saves) > 0 {
					checkSaved(saves[len(saves)-1:])
				}
				body, err := json.Marshal(state)
				if err != nil {
					return err
				}
				value, err := runcontext.ParseJSON(body)
				if err != nil {
					return err
				}
				if _, err := runcontext.ReadExecution(value); err != nil {
					t.Errorf("saved checkpoint %d is malformed: %v", len(saves), err)
					return err
				}
				// Retain the callback's value without cloning its pointers or slices:
				// later callbacks and the final check must see the same bytes.
				saves = append(saves, savedCheckpoint{state, body})
				return nil
			}
			var events []contract.RunEvent
			var mu sync.Mutex
			emit := func(event contract.RunEvent) {
				mu.Lock()
				defer mu.Unlock()
				events = append(events, event)
			}
			g := gate.New(gate.Options{Runtime: emptyRuntime, Granted: gate.Ceiling, Emit: emit})
			offer, err := g.Open(context.Background(), runcontext.OfferedReviewTool(runcontext.Session{}, runcontext.Defaults))
			if err != nil {
				t.Fatal(err)
			}
			taken, denied, invocations := 0, 0, 0
			run := &adapter.Run{ModelName: "agent-model", Gate: g.Calls(), Tools: offer.Tools,
				Prompt: "Write the note.", AllowConversation: true, Emit: emit,
				Context: &runcontext.Input{CurrentRequest: &runcontext.CurrentRequest{Message: "Write the note.", Input: "Write the note."}, Checkpoint: checkpoint},
				Agent: &adapter.Agent{Model: "agent-model", Run: func(ctx context.Context, request adapter.AgentRequest, callbacks adapter.AgentCallbacks) error {
					invocations++
					name := aliasOf(request, runcontext.RequestReview)
					if name == runcontext.RequestReview {
						return fmt.Errorf("request_review was not offered through the agent route")
					}
					if tc.already {
						answer, err := callbacks.Tool(ctx, adapter.AgentToolCall{ID: "initial", Name: name, Arguments: json.RawMessage(`{}`)})
						if err != nil || answer.IsError {
							return fmt.Errorf("initial request: %+v, %v", answer, err)
						}
					}
					type result struct {
						answer adapter.AgentToolAnswer
						err    error
					}
					results := make(chan result, tc.calls)
					start := make(chan struct{})
					var ready sync.WaitGroup
					ready.Add(tc.calls)
					for i := range tc.calls {
						go func() {
							ready.Done()
							<-start
							answer, err := callbacks.Tool(ctx, adapter.AgentToolCall{ID: fmt.Sprintf("review-%d", i), Name: name, Arguments: json.RawMessage(`{}`)})
							results <- result{answer, err}
						}()
					}
					ready.Wait()
					close(start)
					for range tc.calls {
						got := <-results
						if got.err != nil {
							t.Errorf("callback failed: %v", got.err)
						} else if got.answer.IsError {
							denied++
							if !strings.Contains(got.answer.Text, runcontext.ReviewRefusal("asked")) {
								t.Errorf("unexpected refusal: %s", got.answer.Text)
							}
						} else {
							taken++
							if !strings.Contains(got.answer.Text, `"asked":true`) {
								t.Errorf("unexpected accepted answer: %s", got.answer.Text)
							}
						}
					}
					return callbacks.Event(ctx, adapter.AgentEvent{Type: "message", ID: "final", Text: "Done.", Phase: "final"})
				}}}
			if err := agent.New().Run(context.Background(), run); err != nil {
				t.Fatal(err)
			}
			savesMu.Lock()
			checkSaved(saves)
			wantSaves := 1 + tc.calls // Before dispatch and before each review call.
			if tc.already {
				wantSaves++
			}
			if len(saves) != wantSaves {
				t.Errorf("durable saves=%d; want %d", len(saves), wantSaves)
			}
			savesMu.Unlock()
			guardrails, refusedResults := 0, 0
			checkpointed := false
			for _, event := range events {
				if event.Error != nil {
					t.Errorf("run failed: %+v", event.Error)
				}
				if g := event.Guardrail; g != nil && g.Tool == runcontext.RequestReview {
					guardrails++
					if g.Action != "refused" || g.Reason == nil || *g.Reason != "review refused" || g.Detail != runcontext.ReviewRefusal("asked") {
						t.Errorf("unexpected guardrail: %+v", g)
					}
				}
				if result := event.ToolResult; result != nil && result.Name == runcontext.RequestReview && result.IsError {
					refusedResults++
					if guardrails < refusedResults {
						t.Error("a refused result arrived before its guardrail")
					}
				}
				if cp := event.ExecutionCheckpoint; cp != nil && cp.Checkpoint.Critic != nil {
					critic := cp.Checkpoint.Critic
					checkpointed = checkpointed || (critic.Mode == "auto" && critic.Trigger != nil && *critic.Trigger == "model-asked")
				}
			}
			if invocations != 1 || taken != tc.wantTaken || denied != tc.wantDenied || guardrails != tc.wantDenied || refusedResults != tc.wantDenied || !checkpointed {
				t.Fatalf("invocations=%d taken=%d refused=%d guardrails=%d refused results=%d checkpointed=%v; want 1, %d, %d, %d, %d, true", invocations, taken, denied, guardrails, refusedResults, checkpointed, tc.wantTaken, tc.wantDenied, tc.wantDenied, tc.wantDenied)
			}
		})
	}
}
