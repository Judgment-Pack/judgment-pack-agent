// Command agent-host is a small consumer of the agent capability (ADR-0007): a host binds its
// provider bridge, its durable run store and its gate, and runs a response through the agent
// adapter and the delegation coordinator. The bridge here is a stand-in that answers at once; a
// host passes its own, which owns the provider account, its credentials and its process.
//
//	go run ./examples/agent-host
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/agent"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/delegate"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
)

// bridge stands for the host's provider bridge: one invocation at a time on its account. Run admits
// the invocation (an error wrapping adapter.ErrBusy where another holds the account and nothing
// ran), plays the provider's run through the callbacks, and returns only once the account is
// released. Once ctx is done it stops the provider's run and returns.
type bridge struct {
	mu     sync.Mutex
	active bool
}

func (b *bridge) Run(ctx context.Context, request adapter.AgentRequest, callbacks adapter.AgentCallbacks) error {
	b.mu.Lock()
	if b.active {
		b.mu.Unlock()
		return fmt.Errorf("the account is running another invocation: %w", adapter.ErrBusy)
	}
	b.active = true
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		b.active = false
		b.mu.Unlock()
	}()
	answer := "Nothing here needs a delegated task: the request is answered."
	if err := callbacks.Event(ctx, adapter.AgentEvent{Type: "text", ID: "m1", Text: answer}); err != nil {
		return err
	}
	return callbacks.Event(ctx, adapter.AgentEvent{Type: "message", ID: "m1", Text: answer, Phase: "final"})
}

// store stands for the host's durable run store: a checkpoint is acknowledged only once it is
// written, and the last one written is what a restart hands back as Input.Execution.
type store struct {
	mu   sync.Mutex
	last []byte
}

func (s *store) Save(_ context.Context, state contract.Checkpoint) error {
	encoded, err := json.Marshal(state)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.last = encoded
	return nil
}

func (s *store) Load() (*contract.Checkpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.last == nil {
		return nil, nil
	}
	var state contract.Checkpoint
	return &state, json.Unmarshal(s.last, &state)
}

// runtime stands for the host's Runtime transport, which only the gate holds.
func runtime(_ context.Context, frame json.RawMessage) (json.RawMessage, error) {
	var request struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	if err := json.Unmarshal(frame, &request); err != nil {
		return nil, err
	}
	if request.ID == nil {
		return nil, nil
	}
	result := map[string]any{}
	if request.Method == "tools/list" {
		result["tools"] = []any{map[string]any{"name": "get_schema", "inputSchema": map[string]any{"type": "object"}}}
	}
	return json.Marshal(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	events := func(event contract.RunEvent) {
		encoded, _ := json.Marshal(event)
		fmt.Println(string(encoded))
	}
	// The host keeps the gate; the adapter and each child get its narrow handle.
	g := gate.New(gate.Options{Runtime: runtime, Granted: gate.Ceiling, Emit: events})
	offer, err := g.Open(ctx, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	children := func(_ context.Context, profile contract.DelegatedTaskProfile, emit func(contract.RunEvent)) (*gate.Calls, []contract.ToolOfferRuntimeTool, error) {
		child := gate.New(gate.Options{Runtime: runtime, Granted: gate.Ceiling, Emit: emit, Turn: g})
		child.SetOffer(offer)
		phase := gate.ChildPhase(profile, offer, g.Phase())
		child.Enter(phase)
		return child.Calls(), phase.Offer(offer).Tools, nil
	}
	saved := &store{}
	restored, err := saved.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	request := "Is the expense pack's schema current?"
	run := &adapter.Run{
		ModelName: "subscription-model",
		Agent: &adapter.Agent{Model: "subscription-model", Run: (&bridge{}).Run,
			// The connection's enabled models, asked for the parent's, a critic's and each child's.
			Enabled: func(_ context.Context, model string) error {
				if model != "subscription-model" {
					return errors.New("not enabled on this connection")
				}
				return nil
			}},
		Gate: g.Calls(), Tools: offer.Tools, Prompt: request, AllowConversation: true, Interactive: true,
		Context: &runcontext.Input{CurrentRequest: &runcontext.CurrentRequest{Message: request, Input: request},
			Chat: runcontext.NewChatContext("chat-1"), Checkpoint: saved.Save, Execution: restored},
		Children: children, Emit: events,
	}
	if err := delegate.With(agent.New(), delegate.Options{}).Run(ctx, run); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
