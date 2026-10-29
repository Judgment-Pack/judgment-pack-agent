package agent_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/agent"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
)

// emptyRuntime is a runtime that serves no tool.
func emptyRuntime(_ context.Context, frame json.RawMessage) (json.RawMessage, error) {
	var request struct {
		ID json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(frame, &request); err != nil || request.ID == nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"tools": []any{}}})
}

// oneTool is a run on the agent capability with one host tool, the host's function given.
func oneTool(t *testing.T, tool runcontext.HostTool, run adapter.AgentRun) (*adapter.Run, *[]contract.RunEvent) {
	t.Helper()
	var mu sync.Mutex
	events := &[]contract.RunEvent{}
	emit := func(event contract.RunEvent) {
		mu.Lock()
		defer mu.Unlock()
		*events = append(*events, event)
	}
	// The actual contextual session supplies the final declarations, including its
	// source framing marker and package tools, through EnrollContext below.
	g := gate.New(gate.Options{Runtime: emptyRuntime, Granted: gate.Ceiling, Emit: emit})
	offer, err := g.Open(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}

	return &adapter.Run{ModelName: "agent-model", Agent: &adapter.Agent{Model: "agent-model", Run: run}, Gate: g.Calls(), Tools: offer.Tools,
		HostTools: []runcontext.HostTool{tool}, Prompt: "Write the note.", AllowConversation: true, Critic: "off",
		Context: &runcontext.Input{CurrentRequest: &runcontext.CurrentRequest{Message: "Write the note.", Input: "Write the note."}}, Emit: emit,
		EnrollContext: func(_ context.Context, offer adapter.ContextOffer) error {
			return g.EnrollContext(offer.ID, offer.HostTools)
		}}, events
}

func aliasOf(request adapter.AgentRequest, name string) string {
	for _, tool := range request.Tools {
		if strings.HasPrefix(tool.Description, name+":") {
			return tool.Name
		}
	}
	return name
}

// writeNoteSource pins the handwritten action declaration, independently of the production digest.
func writeNoteSource() *runcontext.ToolSource {
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(`[{"description":"Writes a note.","inputSchema":{"type":"object"},"name":"write_note","taxonomy":{"effect":"act","operation":"act"}}]`)))
	return &runcontext.ToolSource{Kind: "custom", ID: "notes", Trust: "person", Version: "1", ToolListDigest: digest,
		CurrentDigest: func(context.Context) (string, error) { return digest, nil }}
}

// An invocation that reached a callback was admitted: its error is its outcome, whatever class it
// carries. One that ran a write and then answered an error wrapping ErrBusy is not sent again, and
// the write ran once (review of #70, row 4).
func TestAnAdmittedInvocationIsNotRetriedOnABusyError(t *testing.T) {
	var writes, calls atomic.Int32
	note := runcontext.HostTool{Name: "write_note", Taxonomy: &runcontext.ToolTaxonomy{Operation: "act", Effect: "act"}, EffectClass: "act", Approval: "required", Source: writeNoteSource(),
		Approve: func(runcontext.ApprovalRequest) string { return "allow" }, Description: "Writes a note.", InputSchema: json.RawMessage(`{"type":"object"}`),
		Execute: func(context.Context, json.RawMessage) (runcontext.ToolResult, error) {
			writes.Add(1)
			return runcontext.ResultOf([]runcontext.Content{runcontext.TextContent("written")}, nil), nil
		}}
	run, events := oneTool(t, note, func(ctx context.Context, request adapter.AgentRequest, callbacks adapter.AgentCallbacks) error {
		calls.Add(1)
		if _, err := callbacks.Tool(ctx, adapter.AgentToolCall{ID: "c1", Name: aliasOf(request, "notes__write_note"), Arguments: json.RawMessage(`{}`)}); err != nil {
			return err
		}
		return fmt.Errorf("the account said busy after the run: %w", adapter.ErrBusy)
	})
	if err := agent.New().Run(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || writes.Load() != 1 {
		t.Fatalf("the host's function ran %d times and the write %d times", calls.Load(), writes.Load())
	}
	failed := false
	for _, event := range *events {
		failed = failed || event.Error != nil
	}
	if !failed {
		t.Fatal("the run ended without its failure")
	}
}

// A busy refusal before any callback is still sent again: nothing ran.
func TestABusyRefusalBeforeAnyCallbackIsSentAgain(t *testing.T) {
	var calls atomic.Int32
	note := runcontext.HostTool{Name: "read_note", Taxonomy: &runcontext.ToolTaxonomy{Operation: "read", Effect: "read"}, Effect: "read", Description: "Reads a note.", InputSchema: json.RawMessage(`{"type":"object"}`),
		Execute: func(context.Context, json.RawMessage) (runcontext.ToolResult, error) {
			return runcontext.ResultOf([]runcontext.Content{runcontext.TextContent("read")}, nil), nil
		}}
	run, _ := oneTool(t, note, func(ctx context.Context, request adapter.AgentRequest, callbacks adapter.AgentCallbacks) error {
		if calls.Add(1) == 1 {
			return fmt.Errorf("another invocation holds the account: %w", adapter.ErrBusy)
		}
		if err := callbacks.Event(ctx, adapter.AgentEvent{Type: "message", ID: "m", Text: "Done.", Phase: "final"}); err != nil {
			return err
		}
		return nil
	})
	if err := agent.New().Run(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("the host's function ran %d times", calls.Load())
	}
}

// A host's function that returns while a tool call it made is still running: the adapter returns
// only once that call's host tool has finished, so it cannot overlap what runs next (review of
// #70, row 5).
func TestTheAdapterWaitsForACallStillRunningWhenTheHostReturns(t *testing.T) {
	var finished atomic.Bool
	started := make(chan struct{})
	slow := runcontext.HostTool{Name: "read_note", Taxonomy: &runcontext.ToolTaxonomy{Operation: "read", Effect: "read"}, Effect: "read", Description: "Reads a note slowly.", InputSchema: json.RawMessage(`{"type":"object"}`),
		Execute: func(context.Context, json.RawMessage) (runcontext.ToolResult, error) {
			close(started)
			time.Sleep(200 * time.Millisecond)
			finished.Store(true)
			return runcontext.ResultOf([]runcontext.Content{runcontext.TextContent("read")}, nil), nil
		}}
	run, _ := oneTool(t, slow, func(ctx context.Context, request adapter.AgentRequest, callbacks adapter.AgentCallbacks) error {
		go func() {
			_, _ = callbacks.Tool(ctx, adapter.AgentToolCall{ID: "c1", Name: aliasOf(request, "read_note"), Arguments: json.RawMessage(`{}`)})
		}()
		<-started
		return nil
	})
	if err := agent.New().Run(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if !finished.Load() {
		t.Fatal("the adapter returned while the host tool was still running")
	}
}

func TestRetiredReviewRefusesPresence(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		calls := 0
		note := runcontext.HostTool{Name: "read_note", Taxonomy: &runcontext.ToolTaxonomy{Operation: "read", Effect: "read"}, Effect: "read", Description: "Reads a note.", InputSchema: json.RawMessage(`{"type":"object"}`)}
		run, events := oneTool(t, note, func(context.Context, adapter.AgentRequest, adapter.AgentCallbacks) error { calls++; return nil })
		run.Review = &legacy
		run.Critic = "on"
		if err := agent.New().Run(context.Background(), run); err != nil {
			t.Fatal(err)
		}
		refused := 0
		guided := false
		for _, event := range *events {
			if event.Guardrail != nil && strings.Contains(event.Guardrail.Detail, "use critic") {
				guided = true
			}
			if event.Error != nil {
				refused++
				if event.Error.Failure == nil || event.Error.Failure.Code != "configuration" {
					t.Fatal("wrong alias refusal")
				}
			}
		}
		if calls != 0 || refused != 1 || !guided {
			t.Fatal("retired Review invoked")
		}
	}
}

func TestPlanDriftStopsProviderInvocation(t *testing.T) {
	var effects atomic.Int32
	note := runcontext.HostTool{Name: "write_note", Taxonomy: &runcontext.ToolTaxonomy{Operation: "act", Effect: "act"}, EffectClass: "act", Approval: "required", Source: writeNoteSource(),
		Approve: func(runcontext.ApprovalRequest) string { return "allow" }, Description: "Writes a note.", InputSchema: json.RawMessage(`{"type":"object"}`), Execute: func(context.Context, json.RawMessage) (runcontext.ToolResult, error) {
			effects.Add(1)
			return runcontext.ResultOf(nil, nil), nil
		}}
	afterPause := false
	run, events := oneTool(t, note, func(ctx context.Context, request adapter.AgentRequest, callbacks adapter.AgentCallbacks) error {
		_, err := callbacks.Tool(ctx, adapter.AgentToolCall{ID: "departure", Name: aliasOf(request, "update_plan"), Arguments: json.RawMessage(`{"explanation":"Narrow accepted work","plan":[{"id":"step-2","step":"Report","status":"pending"}]}`)})
		if err == nil || ctx.Err() == nil {
			t.Error("provider invocation continued after plan pause")
		}
		afterPause = true
		_, _ = callbacks.Tool(ctx, adapter.AgentToolCall{ID: "later-call", Name: aliasOf(request, "notes__write_note"), Arguments: json.RawMessage(`{}`)})
		return err
	})
	run.Context.Plan = runcontext.MustParseJSON(`{"id":"accepted-plan","revision":1,"objective":"Inspect and report","steps":["Inspect","Report"],"questions":[],"references":[],"status":"accepted","createdAt":"2026-10-01T00:00:00.000Z","acceptedAt":"2026-10-01T00:00:00.000Z"}`)
	if err := agent.New().Run(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	approvals := 0
	paused := false
	for _, event := range *events {
		if event.ApprovalRequested != nil {
			approvals++
			if event.ApprovalRequested.Purpose == nil || *event.ApprovalRequested.Purpose != "plan-drift" {
				t.Fatal("plan departure used tool authority")
			}
		}
		if event.ExecutionCheckpoint != nil && event.ExecutionCheckpoint.Checkpoint.Status == "paused" {
			paused = true
		}
		if event.Error != nil {
			t.Fatal(event.Error.Message)
		}
	}
	if approvals != 1 || !paused || !afterPause || effects.Load() != 0 {
		t.Fatalf("approvals %d paused %v callback %v effects %d", approvals, paused, afterPause, effects.Load())
	}
}

// #253 (N2): a source act is a side effect by its effect class, whatever the framing of its answer: the
// run's checkpoint is acknowledged before the act runs, as for any other effect.
func TestASourceActIsSavedBeforeItRuns(t *testing.T) {
	var saves, before, atExecute atomic.Int32
	note := runcontext.HostTool{Name: "write_note", Taxonomy: &runcontext.ToolTaxonomy{Operation: "act", Effect: "act"}, EffectClass: "act", Approval: "required", Source: writeNoteSource(),
		Approve: func(runcontext.ApprovalRequest) string { return "allow" }, Description: "Writes a note.", InputSchema: json.RawMessage(`{"type":"object"}`),
		Execute: func(context.Context, json.RawMessage) (runcontext.ToolResult, error) {
			atExecute.Store(saves.Load())
			return runcontext.ResultOf([]runcontext.Content{runcontext.TextContent("written")}, nil), nil
		}}
	run, _ := oneTool(t, note, func(ctx context.Context, request adapter.AgentRequest, callbacks adapter.AgentCallbacks) error {
		before.Store(saves.Load())
		if _, err := callbacks.Tool(ctx, adapter.AgentToolCall{ID: "c1", Name: aliasOf(request, "notes__write_note"), Arguments: json.RawMessage(`{}`)}); err != nil {
			return err
		}
		return callbacks.Event(ctx, adapter.AgentEvent{Type: "message", ID: "m", Text: "Done.", Phase: "final"})
	})
	run.Context.Checkpoint = func(context.Context, contract.Checkpoint) error { saves.Add(1); return nil }
	if err := agent.New().Run(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if atExecute.Load() <= before.Load() {
		t.Fatalf("the act ran before its checkpoint was acknowledged: %d saves before the call, %d when it ran", before.Load(), atExecute.Load())
	}
}
