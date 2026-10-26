package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
)

// AgentRecordingStep preserves callback order, including calls between streamed events.
type AgentRecordingStep struct {
	Event *AgentEvent    `json:"event,omitempty"`
	Tool  *AgentToolCall `json:"tool,omitempty"`
}

// RecordAgentRun captures the capability's callbacks for a corpus request response body.
// The capture is in memory; the caller passes it through WriteCorpusEntry before storage.
func RecordAgentRun(run AgentRun, capture func(AgentRequest, json.RawMessage)) AgentRun {
	return func(ctx context.Context, request AgentRequest, callbacks AgentCallbacks) error {
		capturedRequest := request
		if request.Tools != nil {
			capturedRequest.Tools = append([]AgentTool{}, request.Tools...)
			for i := range capturedRequest.Tools {
				capturedRequest.Tools[i].InputSchema = append(json.RawMessage(nil), request.Tools[i].InputSchema...)
			}
		}
		steps := []AgentRecordingStep{}
		var mu sync.Mutex
		watched := callbacks
		watched.Event = func(ctx context.Context, event AgentEvent) error {
			held := event
			mu.Lock()
			steps = append(steps, AgentRecordingStep{Event: &held})
			mu.Unlock()
			return callbacks.Event(ctx, event)
		}
		watched.Tool = func(ctx context.Context, call AgentToolCall) (AgentToolAnswer, error) {
			held := call
			held.Arguments = append(json.RawMessage(nil), call.Arguments...)
			mu.Lock()
			steps = append(steps, AgentRecordingStep{Tool: &held})
			mu.Unlock()
			return callbacks.Tool(ctx, call)
		}
		err := run(ctx, request, watched)
		mu.Lock()
		body, _ := json.Marshal(struct {
			Steps []AgentRecordingStep `json:"steps"`
		}{steps})
		mu.Unlock()
		capture(capturedRequest, body)
		return err
	}
}

// ReplayAgentBody delivers recorded callbacks through the real adapter and its tool gate.
// Events-only seed entries remain readable.
func ReplayAgentBody(ctx context.Context, body json.RawMessage, callbacks AgentCallbacks) error {
	var held struct {
		Steps  []AgentRecordingStep `json:"steps"`
		Events []AgentEvent         `json:"events"`
	}
	if err := json.Unmarshal(body, &held); err != nil {
		return err
	}
	if held.Steps == nil {
		for i := range held.Events {
			held.Steps = append(held.Steps, AgentRecordingStep{Event: &held.Events[i]})
		}
	}
	for _, step := range held.Steps {
		if err := ctx.Err(); err != nil {
			return err
		}
		if (step.Event == nil) == (step.Tool == nil) {
			return fmt.Errorf("invalid recorded agent callback")
		}
		if step.Event != nil {
			if err := callbacks.Event(ctx, *step.Event); err != nil {
				return err
			}
		} else {
			if _, err := callbacks.Tool(ctx, *step.Tool); err != nil {
				return err
			}
		}
	}
	return nil
}
