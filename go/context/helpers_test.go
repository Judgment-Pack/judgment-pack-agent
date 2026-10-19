package runcontext

// The sessions and the little calls the tests here share.

import (
	"context"
	"encoding/json"
	"testing"
)

// modelSession is the session the unit tests run against: a model capability, no tools, no model
// call (a context unit test makes none).
func modelSession() Session {
	return Session{Prompt: "Current request", Tools: []RuntimeTool{}, HostTools: []HostTool{},
		CallTool: func(ctx context.Context, name string, args json.RawMessage) (ToolResult, error) {
			return ResultOf([]Content{}, nil), nil
		},
		Model: &ModelSession{Family: "openai-compatible", Model: "small"}}
}

// smallWindowSession is context.test.ts's session: the profile gives the model a 16,384-token
// window with a 4,096-token output reserve.
func smallWindowSession() Session {
	held := modelSession()
	held.Model = &ModelSession{Family: "gemini", Model: "small"}
	held.Context = &Input{Configuration: &Configuration{Version: 1,
		Models: map[string]ModelBudget{"small": {ContextWindowTokens: 16384, OutputTokens: 4096}}}}
	return held
}

// windowed is a session at a window of the tokens given with a 1,024-token output reserve, and
// the chat where one is given (windowed in context/chat.test.ts).
func windowed(window int, chat ChatScope) Session {
	held := modelSession()
	held.Prompt = ""
	held.Model = &ModelSession{Family: "openai-compatible", Model: "scripted-model"}
	held.Context = &Input{Configuration: &Configuration{Version: 1,
		Models: map[string]ModelBudget{"scripted-model": {ContextWindowTokens: window, OutputTokens: 1024}}}}
	if chat != nil {
		held.Context.Chat = chat
	}
	return held
}

func openContext(t *testing.T, session Session) *SessionContext {
	t.Helper()
	held, err := NewSessionContext(session)
	if err != nil {
		t.Fatal(err)
	}
	return held
}

// tool is one of the ledger's tools by name.
func tool(t *testing.T, c *SessionContext, name string) HostTool {
	t.Helper()
	for _, held := range c.Ledger.Tools() {
		if held.Name == name {
			return held
		}
	}
	t.Fatalf("no tool named %s", name)
	return HostTool{}
}

// call runs one tool with the arguments written as JSON.
func call(t *testing.T, held HostTool, args string) (ToolResult, error) {
	t.Helper()
	return held.Execute(context.Background(), json.RawMessage(args))
}

// retain retains one document, or fails the test.
func retain(t *testing.T, c *SessionContext, id, kind, label string, value *Value) *Entry {
	t.Helper()
	entry, err := c.Ledger.Retain(Document{ID: id, Kind: kind, Label: label, Value: value}, false)
	if err != nil {
		t.Fatal(err)
	}
	return entry
}

// noTools is the tool list an adapter sends when it offers none.
func noTools() *Value { return Array() }
