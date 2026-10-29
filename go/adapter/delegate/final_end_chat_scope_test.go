package delegate_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/agent"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/delegate"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/eino"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
	"github.com/Judgment-Pack/judgment-pack-agent/go/relay"
)

// agent#248: these fixtures are handwritten. The Runtime peer, the model replies and the host
// store are offline doubles reached through the actual gate transport, eino adapter and
// delegation coordinator.

// chatScopeStore is the host's durable store: it refuses any root checkpoint whose context scope
// is not the retained chat's, the invariant a later turn on that chat reopens against.
type chatScopeStore struct {
	runcontext.RunStore
	scope   string
	mu      sync.Mutex
	refused []string
}

func (s *chatScopeStore) Commit(ctx context.Context, in runcontext.RunCommit) (runcontext.RunReceipt, error) {
	if len(in.Checkpoint) > 0 {
		var cp struct {
			Status  string `json:"status"`
			Context struct {
				Scope string `json:"scope"`
			} `json:"context"`
		}
		if err := json.Unmarshal(in.Checkpoint, &cp); err != nil {
			return runcontext.RunReceipt{}, err
		}
		if cp.Context.Scope != s.scope {
			s.mu.Lock()
			s.refused = append(s.refused, cp.Status+":"+cp.Context.Scope)
			s.mu.Unlock()
			return runcontext.RunReceipt{}, errors.New("fixture store: checkpoint left the retained chat scope")
		}
	}
	return s.RunStore.Commit(ctx, in)
}

func chatScopeReply(name, args string) *http.Response {
	delta := map[string]any{"content": "Proposal ready."}
	finish := "stop"
	if name != "" {
		delta = map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "call-" + name, "type": "function", "function": map[string]any{"name": name, "arguments": args}}}}
		finish = "tool_calls"
	}
	chunk, _ := json.Marshal(map[string]any{"id": "fixture", "object": "chat.completion.chunk", "model": "fixture", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}, "usage": map[string]int{"prompt_tokens": 2, "completion_tokens": 1}})
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: " + string(chunk) + "\n\ndata: [DONE]\n\n"))}
}

func TestDelegateFinalEndKeepsRetainedChatScope248(t *testing.T) {
	for _, engine := range []string{"api", "native"} {
		t.Run(engine, func(t *testing.T) { chatScopeTwoTurns(t, engine) })
	}
}

func chatScopeTwoTurns(t *testing.T, engine string) {
	chat := runcontext.NewChatContext("")
	defer chat.Dispose()
	memory := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
	store := &chatScopeStore{RunStore: memory, scope: chat.Scope()}
	var mu sync.Mutex
	physical := map[string]int{}
	runtime := func(_ context.Context, frame json.RawMessage) (json.RawMessage, error) {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Name string `json:"name"`
			} `json:"params"`
		}
		if err := json.Unmarshal(frame, &request); err != nil {
			return nil, err
		}
		if len(request.ID) == 0 {
			return nil, nil
		}
		var result any
		switch request.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "fixture-runtime", "version": "1.0.0"}}
		case "tools/list":
			result = map[string]any{"tools": []any{
				map[string]any{"name": "get_schema", "description": "Read the pinned schema.", "inputSchema": map[string]any{"type": "object"}, "_meta": map[string]any{"operation": "read", "effect": "read"}},
				map[string]any{"name": "validate", "description": "Validate the workspace document.", "inputSchema": map[string]any{"type": "object"}, "_meta": map[string]any{"operation": "check", "effect": "pure", "checkKind": "validate"}},
			}}
		case "tools/call":
			mu.Lock()
			physical[request.Params.Name]++
			count := physical[request.Params.Name]
			mu.Unlock()
			text := "schema 0.2.0-draft"
			if request.Params.Name == "validate" {
				text = "workspace revision " + strconv.Itoa(count)
			}
			result = map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}}
		default:
			return nil, errors.New("unexpected method " + request.Method)
		}
		return json.Marshal(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
	}
	turn := func(id string) []contract.RunEvent {
		t.Helper()
		g := gate.New(gate.Options{Runtime: runtime, Granted: gate.Ceiling})
		offer, err := g.Open(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		requests := 0
		events := []contract.RunEvent{}
		implementation := eino.New()
		run := &adapter.Run{Wire: "openai-compatible", ModelName: "fixture", Thinking: "off", Critic: "off", Prompt: "Author the pack.", AllowConversation: true, Interactive: true,
			Gate: g.Calls(), Tools: offer.Tools, Emit: func(e contract.RunEvent) { events = append(events, e) },
			Context: &runcontext.Input{Chat: chat, Runtime: &runcontext.RuntimeIdentity{Identity: "fixture-runtime", Version: "1.0.0"}, CurrentRequest: &runcontext.CurrentRequest{Message: "Author the pack.", Input: "Author the pack."}, RunStore: &runcontext.DurableRunInput{Store: store, ID: id}},
			Children: func(context.Context, contract.DelegatedTaskProfile, func(contract.RunEvent)) (*gate.Calls, []contract.ToolOfferRuntimeTool, error) {
				return nil, nil, errors.New("fixture delegates no task")
			},
			Model: func(context.Context, string, relay.Request) (*http.Response, error) {
				requests++
				switch requests {
				case 1:
					return chatScopeReply("get_schema", `{"spec_version":"0.2.0-draft"}`), nil
				case 2:
					return chatScopeReply("validate", `{"document":"workspace"}`), nil
				}
				return chatScopeReply("", ""), nil
			}}
		if engine == "native" {
			implementation = agent.New()
			run.Model = nil
			run.Agent = &adapter.Agent{Model: "fixture", Run: func(ctx context.Context, request adapter.AgentRequest, cb adapter.AgentCallbacks) error {
				if cb.ProviderRelease != nil {
					defer cb.ProviderRelease.Acknowledge(cb.ProviderRelease.Invocation)
				}
				for _, call := range []struct{ name, args string }{{"get_schema", `{"spec_version":"0.2.0-draft"}`}, {"validate", `{"document":"workspace"}`}} {
					requests++
					found := false
					for _, tool := range request.Tools {
						if strings.HasPrefix(tool.Description, call.name+": ") {
							found = true
							if _, err := cb.Tool(ctx, adapter.AgentToolCall{ID: "native-" + call.name, Name: tool.Name, Arguments: json.RawMessage(call.args)}); err != nil {
								return err
							}
						}
					}
					if !found {
						return errors.New("fixture: " + call.name + " was not offered")
					}
				}
				requests++
				return cb.Event(ctx, adapter.AgentEvent{Type: "message", ID: "final", Text: "Proposal ready.", Phase: "final"})
			}}
		}
		if err := delegate.With(implementation, delegate.Options{}).Run(context.Background(), run); err != nil {
			t.Fatal(err)
		}
		for _, e := range events {
			if e.Error != nil {
				raw, _ := json.Marshal(e)
				t.Errorf("%s: %s", id, raw)
			}
		}
		if requests != 3 {
			t.Fatalf("%s: %d model requests, want 3", id, requests)
		}
		head, err := memory.Load(context.Background(), id)
		if err != nil || head == nil {
			t.Fatalf("%s: no durable head: %v", id, err)
		}
		var saved contract.Checkpoint
		if err := json.Unmarshal(head.Checkpoint, &saved); err != nil {
			t.Fatal(err)
		}
		store.mu.Lock()
		refused := append([]string{}, store.refused...)
		store.mu.Unlock()
		if saved.Context.Scope != chat.Scope() || saved.Status != "complete" || len(refused) > 0 {
			t.Fatalf("%s: final checkpoint scope=%s status=%s, want the retained chat scope %s complete; refused commits %v", id, saved.Context.Scope, saved.Status, chat.Scope(), refused)
		}
		return events
	}
	validated := func(events []contract.RunEvent) string {
		for _, e := range events {
			if e.ToolResult != nil && e.ToolResult.Name == "validate" {
				raw, _ := json.Marshal(e.ToolResult)
				return string(raw)
			}
		}
		return ""
	}
	first := turn(engine + "-chat-turn-1")
	second := turn(engine + "-chat-turn-2")
	mu.Lock()
	defer mu.Unlock()
	if physical["get_schema"] != 1 || physical["validate"] != 2 {
		t.Fatalf("physical Runtime reads %v, want one immutable get_schema across both turns and a fresh validate per turn", physical)
	}
	if !strings.Contains(validated(first), "workspace revision 1") || !strings.Contains(validated(second), "workspace revision 2") {
		t.Fatalf("mutable reads were not fresh: turn 1 %s; turn 2 %s", validated(first), validated(second))
	}
}
