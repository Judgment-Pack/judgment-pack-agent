package adapter_test

import (
	"context"
	"encoding/json"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	"testing"
)

func TestCaptureRecorderPreservesNativeCallbackCapabilities(t *testing.T) {
	ctx := context.WithValue(context.Background(), struct{}{}, "same")
	release := new(adapter.ProviderRelease)
	terminal := new(adapter.NativeTermination)
	needed := 0
	captured := 0
	callbacks := adapter.AgentCallbacks{ProviderRelease: release, NativeTermination: terminal, CapabilityNeeded: func(given context.Context, need adapter.RouteCapabilityNeed) error {
		if given != ctx || need.Name != "lookup" {
			t.Fatal("capability callback arguments replaced")
		}
		needed++
		return nil
	}, Event: func(given context.Context, event adapter.AgentEvent) error {
		if given != ctx || event.Text != "Answer" {
			t.Fatal("event callback arguments replaced")
		}
		return nil
	}, Tool: func(given context.Context, call adapter.AgentToolCall) (adapter.AgentToolAnswer, error) {
		if given != ctx || call.ID != "one" {
			t.Fatal("tool callback arguments replaced")
		}
		call.Arguments[5] = '9'
		return adapter.AgentToolAnswer{}, nil
	}}
	request := adapter.AgentRequest{Prompt: "Original", Instructions: "Instructions", Tools: []adapter.AgentTool{{Name: "lookup", Description: "Read", InputSchema: json.RawMessage(`{}`)}}, Phase: "author", Delegated: true}
	recorded := adapter.RecordAgentRun(func(given context.Context, r adapter.AgentRequest, cb adapter.AgentCallbacks) error {
		if given != ctx || cb.ProviderRelease != release || cb.NativeTermination != terminal {
			t.Fatal("native callback capability identity lost")
		}
		if e := cb.CapabilityNeeded(given, adapter.RouteCapabilityNeed{Kind: "tool", Name: "lookup"}); e != nil {
			return e
		}
		r.Tools[0].Name = "changed"
		if e := cb.Event(given, adapter.AgentEvent{Type: "message", ID: "answer", Text: "Answer"}); e != nil {
			return e
		}
		_, e := cb.Tool(given, adapter.AgentToolCall{ID: "one", Name: "lookup", Arguments: json.RawMessage(`{"x":1}`)})
		return e
	}, func(r adapter.AgentRequest, raw json.RawMessage) {
		captured++
		if r.Tools[0].Name != "lookup" || !r.Delegated {
			t.Fatal("recorded request aliased provider mutation")
		}
		var body struct {
			Steps []adapter.AgentRecordingStep `json:"steps"`
		}
		if json.Unmarshal(raw, &body) != nil || len(body.Steps) != 2 || string(body.Steps[1].Tool.Arguments) != `{"x":1}` {
			t.Fatal("recorder material changed", string(raw))
		}
	})
	if e := recorded(ctx, request, callbacks); e != nil || captured != 1 || needed != 1 {
		t.Fatal("capture call counts", e, captured, needed)
	}
}

func TestCaptureMaterialNeverSuppressesOriginalInvocation(t *testing.T) {
	called, delivered := false, false
	request := adapter.AgentRequest{Prompt: "Original", Tools: []adapter.AgentTool{{InputSchema: json.RawMessage(`{`)}}}
	recorded := adapter.RecordAgentRun(func(_ context.Context, r adapter.AgentRequest, _ adapter.AgentCallbacks) error {
		called = true
		if string(r.Tools[0].InputSchema) != "{" {
			t.Fatal("original malformed input replaced")
		}
		return nil
	}, func(r adapter.AgentRequest, _ json.RawMessage) {
		delivered = true
		if string(r.Tools[0].InputSchema) != "{" {
			t.Fatal("recorded input fabricated")
		}
	})
	if e := recorded(context.Background(), request, adapter.AgentCallbacks{}); e != nil || !called || !delivered {
		t.Fatal("material snapshot suppressed original invocation", e, called, delivered)
	}
}
