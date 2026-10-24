package delegate_test

import (
	"context"
	"encoding/json"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/agent"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/delegate"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/turn"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
	"strings"
	"testing"
)

type observedStorage struct {
	runcontext.RunStore
	states map[string]bool
}

func (s observedStorage) Commit(ctx context.Context, in runcontext.RunCommit) (runcontext.RunReceipt, error) {
	var cp contract.Checkpoint
	if err := json.Unmarshal(in.Checkpoint, &cp); err != nil {
		return runcontext.RunReceipt{}, err
	}
	if cp.Handoff != nil {
		s.states[cp.Handoff.State] = true
	}
	return s.RunStore.Commit(ctx, in)
}
func TestStorageActualAgentHandoffUsesRootWriter(t *testing.T) {
	ctx := context.Background()
	root := ctx
	memory := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
	store := observedStorage{memory, map[string]bool{}}
	g := gate.New(gate.Options{})
	g.SetOffer(contract.ToolOffer{Tools: []contract.ToolOfferRuntimeTool{}, HostTools: []contract.ToolOfferHostTool{turn.DelegationTool(turn.TaskProfiles)}})
	calls := 0
	active := false
	var events []contract.RunEvent
	run := &adapter.Run{Gate: g.Calls(), ModelName: "fixture", Prompt: "Review", Critic: "off", AllowConversation: true, Interactive: true,
		Context: &runcontext.Input{CurrentRequest: &runcontext.CurrentRequest{Message: "Review", Input: "Review"}, RunStore: &runcontext.DurableRunInput{Store: store, ID: "actual-handoff"}},
		Emit:    func(e contract.RunEvent) { events = append(events, e) },
		Children: func(context.Context, contract.DelegatedTaskProfile, func(contract.RunEvent)) (*gate.Calls, []contract.ToolOfferRuntimeTool, error) {
			return nil, nil, nil
		},
		Agent: &adapter.Agent{Model: "fixture", Run: func(ctx context.Context, request adapter.AgentRequest, cb adapter.AgentCallbacks) error {
			if active {
				t.Fatal("provider overlapped")
			}
			active = true
			defer func() {
				active = false
				if cb.ProviderRelease != nil {
					cb.ProviderRelease.Acknowledge(cb.ProviderRelease.Invocation)
				}
			}()
			calls++
			if calls == 1 {
				for _, tool := range request.Tools {
					if strings.HasPrefix(tool.Description, "delegate_tasks:") {
						_, err := cb.Tool(ctx, adapter.AgentToolCall{ID: "delegate-wire", Name: tool.Name, Arguments: json.RawMessage(`{"tasks":[{"title":"Review","profile":"review","objective":"Check the supplied claims."}]}`)})
						if err == nil || ctx.Err() != context.Canceled || root.Err() != nil {
							t.Fatalf("handoff did not stop only its provider attempt: tool=%v provider=%v root=%v", err, ctx.Err(), root.Err())
						}
						// Even a host swallowing the pause error cannot turn it into a result.
						return nil
					}
				}
				t.Fatal("no delegate tool")
			}
			return cb.Event(ctx, adapter.AgentEvent{Type: "message", ID: "reply", Text: "Claims agree.", Phase: "final"})
		}}}
	if err := delegate.With(agent.New(), delegate.Options{}).Run(ctx, run); err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Error != nil {
			t.Fatalf("error: %s", event.Error.Message)
		}
	}
	if calls != 3 {
		t.Fatalf("invocations=%d", calls)
	}
	for _, state := range []string{"released", "children", "answered", "continued"} {
		if !store.states[state] {
			t.Fatalf("missing state %s", state)
		}
	}
	head, err := store.Load(ctx, "actual-handoff")
	if err != nil {
		t.Fatal(err)
	}
	var saved contract.Checkpoint
	if err = json.Unmarshal(head.Checkpoint, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Durable == nil || len(saved.Durable.Dispatches) != 3 || saved.Durable.Dispatches[0].State != "unknown" {
		t.Fatal("paused provider dispatch was promoted to a successful result")
	}
	if saved.Status != "complete" || saved.Handoff == nil || saved.Handoff.Continuation == nil {
		t.Fatalf("final checkpoint lost completion/handoff: %s", head.Checkpoint)
	}
}

func TestStorageProviderReleaseAcknowledgementIsInvocationScoped(t *testing.T) {
	first, finishFirst := adapter.ProviderReleaseAttempt()
	second, finishSecond := adapter.ProviderReleaseAttempt()
	if second.Acknowledge(first.Invocation) || finishSecond() {
		t.Fatal("wrong invocation release acknowledged")
	}
	if !first.Acknowledge(first.Invocation) || !finishFirst() {
		t.Fatal("valid release refused")
	}
	if first.Acknowledge(first.Invocation) {
		t.Fatal("late release acknowledged")
	}
}
func TestStorageMissingProviderReleaseRefusesChildAndResume(t *testing.T) {
	ctx := context.Background()
	store := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
	calls := 0
	execute := func(resume bool) []contract.RunEvent {
		g := gate.New(gate.Options{})
		g.SetOffer(contract.ToolOffer{Tools: []contract.ToolOfferRuntimeTool{}, HostTools: []contract.ToolOfferHostTool{turn.DelegationTool(turn.TaskProfiles)}})
		events := []contract.RunEvent{}
		run := &adapter.Run{Gate: g.Calls(), ModelName: "fixture", Prompt: "Review", Critic: "off", AllowConversation: true, Interactive: true, Context: &runcontext.Input{CurrentRequest: &runcontext.CurrentRequest{Message: "Review", Input: "Review"}, RunStore: &runcontext.DurableRunInput{Store: store, ID: "unknown-release", Resume: resume}}, Emit: func(e contract.RunEvent) { events = append(events, e) }, Children: func(context.Context, contract.DelegatedTaskProfile, func(contract.RunEvent)) (*gate.Calls, []contract.ToolOfferRuntimeTool, error) {
			return nil, nil, nil
		}, Agent: &adapter.Agent{Model: "fixture", Run: func(ctx context.Context, request adapter.AgentRequest, cb adapter.AgentCallbacks) error {
			calls++
			for _, tool := range request.Tools {
				if strings.HasPrefix(tool.Description, "delegate_tasks:") {
					_, err := cb.Tool(ctx, adapter.AgentToolCall{ID: "delegate-wire", Name: tool.Name, Arguments: json.RawMessage(`{"tasks":[{"title":"Review","profile":"review","objective":"Check claims."}]}`)})
					return err
				}
			}
			return nil
		}}}
		if err := delegate.With(agent.New(), delegate.Options{}).Run(ctx, run); err != nil {
			t.Fatal(err)
		}
		return events
	}
	events := execute(false)
	if calls != 1 {
		t.Fatalf("unknown release dispatched %d invocations", calls)
	}
	found := false
	for _, e := range events {
		if e.Error != nil && strings.Contains(e.Error.Message, "ProviderReleaseUnknown:") {
			found = true
		}
	}
	if !found {
		t.Fatal("missing named release refusal")
	}
	head, _ := store.Load(ctx, "unknown-release")
	if head == nil || !strings.Contains(string(head.Checkpoint), "ProviderReleaseUnknown:") {
		t.Fatal("unknown release not durable")
	}
	_ = execute(true)
	if calls != 1 {
		t.Fatal("resume minted provider release authority")
	}
}
