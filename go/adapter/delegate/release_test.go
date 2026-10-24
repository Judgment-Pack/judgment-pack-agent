package delegate_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/agent"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/delegate"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/turn"
	"github.com/Judgment-Pack/judgment-pack-agent/go/certification"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
)

// unjoined is an adapter on the agent capability that reports its handoff from inside the
// invocation's tool callback and returns at once, while the host's function still holds the account
// (as an engine that does not wait for its function would): it does not join the invocation. Its
// children and its continuation answer at once.
func unjoined(running chan<- struct{}) adapter.Adapter {
	var once sync.Once
	return adapter.Func(func(ctx context.Context, run *adapter.Run) error {
		end := func() { run.Emit(contract.RunEvent{End: &contract.RunEventEnd{Type: "end"}}) }
		request := adapter.AgentRequest{Prompt: run.Prompt, Instructions: "i", Tools: []adapter.AgentTool{}, Phase: "author"}
		if run.Agent.Delegated != "" {
			request.Model, request.Delegated = run.Agent.Delegated, true
		}
		admitted, err := run.Agent.Admit(ctx, request)
		if err == nil && run.Agent.Begin != nil {
			err = run.Agent.Begin(ctx, admitted)
		}
		if err != nil {
			run.Emit(contract.RunEvent{Error: &contract.RunEventError{Type: "error", Message: err.Error()}})
			end()
			return nil
		}
		parent := strings.Contains(run.Prompt, turn.DelegationInstructions) && !strings.Contains(run.Prompt, turn.Continuation)
		handedOff := make(chan struct{})
		callbacks := adapter.AgentCallbacks{
			Event: func(context.Context, adapter.AgentEvent) error { return nil },
			Tool: func(context.Context, adapter.AgentToolCall) (adapter.AgentToolAnswer, error) {
				close(handedOff)
				return adapter.AgentToolAnswer{}, errors.New("handed off")
			},
		}
		if parent {
			go func() {
				_ = run.Agent.Run(context.WithoutCancel(ctx), admitted, callbacks)
				once.Do(func() { close(running) })
			}()
			<-handedOff
			id := "call-1"
			run.Emit(contract.RunEvent{DelegationRequested: &contract.RunEventDelegationRequested{Type: "delegation_requested", CallID: &id,
				Request: contract.DelegatedTaskRequest{Tasks: []contract.DelegatedTaskTaskRequest{{Title: "A", Objective: "Check part A.", Profile: "analyze"}}}}})
			end()
			return nil
		}
		if err := run.Agent.Run(ctx, admitted, callbacks); err == nil {
			run.Emit(contract.RunEvent{Message: &contract.RunEventMessage{Type: "message", Text: "Checked."}})
		}
		end()
		return nil
	})
}

// The coordinator dispatches a child only once the parent's invocation has released the account
// (ADR-0007), even under an adapter that returns before the host's function does: the fake
// exclusive account, which holds the parent's invocation a while after its handoff, records no
// child asked for it while it was held.
func TestAChildWaitsForTheParentsRelease(t *testing.T) {
	account := certification.NewFakeAccount()
	account.ReleaseAfter = 150 * time.Millisecond
	running := make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var events []contract.RunEvent
	run := &adapter.Run{ModelName: "agent-model", Prompt: "Check the pack.", AllowConversation: true, Interactive: true,
		Agent: &adapter.Agent{Model: "agent-model", Run: account.Run(func(request adapter.AgentRequest, _ int) certification.AgentPlay {
			if strings.Contains(request.Prompt, turn.DelegationInstructions) && !strings.Contains(request.Prompt, turn.Continuation) {
				return certification.AgentPlay{Steps: []certification.AgentStep{{Tool: turn.DelegateTool}}}
			}
			return certification.AgentPlay{}
		})},
		Children: func(context.Context, contract.DelegatedTaskProfile, func(contract.RunEvent)) (*gate.Calls, []contract.ToolOfferRuntimeTool, error) {
			return nil, nil, nil
		},
		Context: &runcontext.Input{Chat: runcontext.NewChatContext("chat-1")},
		Emit:    func(event contract.RunEvent) { events = append(events, event) }}
	if err := delegate.With(unjoined(running), delegate.Options{}).Run(ctx, run); err != nil {
		t.Fatal(err)
	}
	<-running
	if violations := account.Violations(); len(violations) > 0 {
		t.Fatalf("the account was asked for while the parent held it: %v", violations)
	}
	children := 0
	for _, event := range events {
		raw, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		var header struct{ Version int }
		if err := json.Unmarshal(raw, &header); err != nil {
			t.Fatal(err)
		}
		if header.Version != 1 {
			t.Fatalf("coordinator event has no current header: %s", raw)
		}
		if event.DelegatedTask != nil && event.DelegatedTask.Task.State == "complete" {
			children++
		}
		if event.Error != nil {
			t.Errorf("an error event: %s", event.Error.Message)
		}
	}
	if children != 1 {
		t.Fatalf("%d children completed", children)
	}
}

func TestClosedAgentParentKeepsChildOutputCounts(t *testing.T) {
	ctx := context.Background()
	calls := 0
	g := gate.New(gate.Options{})
	g.SetOffer(contract.ToolOffer{Tools: []contract.ToolOfferRuntimeTool{}, HostTools: []contract.ToolOfferHostTool{turn.DelegationTool(turn.TaskProfiles)}})
	var saved []contract.Checkpoint
	var events []contract.RunEvent
	run := &adapter.Run{Gate: g.Calls(), ModelName: "fixture", Prompt: "Review", Critic: "off", AllowConversation: true, Interactive: true,
		Context: &runcontext.Input{CurrentRequest: &runcontext.CurrentRequest{Message: "Review", Input: "Review"}, Checkpoint: func(_ context.Context, state contract.Checkpoint) error { saved = append(saved, state); return nil }},
		Emit:    func(event contract.RunEvent) { events = append(events, event) },
		Children: func(context.Context, contract.DelegatedTaskProfile, func(contract.RunEvent)) (*gate.Calls, []contract.ToolOfferRuntimeTool, error) {
			return nil, nil, nil
		},
		Agent: &adapter.Agent{Model: "fixture", Run: func(ctx context.Context, request adapter.AgentRequest, cb adapter.AgentCallbacks) error {
			calls++
			if calls == 1 {
				for _, tool := range request.Tools {
					if strings.HasPrefix(tool.Description, "delegate_tasks:") {
						_, err := cb.Tool(ctx, adapter.AgentToolCall{ID: "call", Name: tool.Name, Arguments: json.RawMessage(`{"tasks":[{"title":"Review","profile":"review","objective":"Check the supplied claims."}]}`)})
						return err
					}
				}
				t.Fatal("no delegation tool")
			}
			text := "AKIAABCDEFGHIJKLMNOP"
			if calls == 3 {
				text = "Parent finished."
			}
			return cb.Event(ctx, adapter.AgentEvent{Type: "message", ID: "reply", Text: text, Phase: "final"})
		}},
	}
	if err := delegate.With(agent.New(), delegate.Options{}).Run(ctx, run); err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatalf("invocations=%d", calls)
	}
	var last *contract.Checkpoint
	child := false
	for _, e := range events {
		if e.Error != nil {
			t.Fatalf("error: %s", e.Error.Message)
		}
		if e.ExecutionCheckpoint != nil {
			last = &e.ExecutionCheckpoint.Checkpoint
		}
		if e.DelegatedTask != nil && e.DelegatedTask.Task.State == "complete" {
			child = true
		}
	}
	if !child || last == nil || last.OutputGuard["aws-access-key"] != 1 {
		t.Fatalf("child=%v final checkpoint=%+v", child, last)
	}
	if len(saved) == 0 || saved[len(saved)-1].OutputGuard["aws-access-key"] != 1 {
		t.Fatal("saved checkpoint lost the child count")
	}
}
