package certification

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/turn"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/relay"
)

// CapacityNames are the capacity check's names: the model-engine check of
// packages/harness/certification/delegation.test.ts.
var CapacityNames = []string{"delegation capacity"}

// Capacity is the model-engine check of certification/delegation.test.ts (agent#17): a delegation
// batch of three runs as many children at once as the host's answer for the run's connection says,
// three here (concurrentRequests).
func Capacity(with func(adapter.Adapter) adapter.Adapter) ScenarioCheck {
	return CapacityAt(with, 3)
}

// CapacityAt is the capacity check with the connection's answer naming capacity requests at once.
// The model is the check's own, as the TypeScript check's is: a parent that delegates three tasks
// and then answers, and children that each hold their request a while and answer; the Runtime and
// the gate are a scenario session's.
func CapacityAt(with func(adapter.Adapter) adapter.Adapter, capacity int) ScenarioCheck {
	return ScenarioCheck{
		Scenario: "capacity " + strconv.Itoa(capacity),
		Names:    CapacityNames,
		Run: func(ctx context.Context, server *Server, answerAs string, inner Adapter) ([]Finding, error) {
			delegate := turn.DelegationTool(turn.TaskProfiles)
			declared := []runcontext.HostTool{{Name: delegate.Name, Description: delegate.Description, InputSchema: delegate.InputSchema, Interaction: "delegation"}}
			host, err := openScenario(ctx, server, ScenarioEndpoint{Scenario: "delegation", AnswerAs: answerAs}, declared, true)
			if err != nil {
				return nil, err
			}
			var mu sync.Mutex
			active, peak, children := 0, 0, 0
			firstWave := make(chan struct{})
			waveTimedOut := false
			model := func(ctx context.Context, _ string, request relay.Request) (*http.Response, error) {
				body := string(request.Body)
				if strings.Contains(body, childBrief) {
					mu.Lock()
					active++
					children++
					peak = max(peak, active)
					if children == capacity {
						close(firstWave)
					}
					mu.Unlock()
					// Wait for the configured first wave rather than assuming every child
					// reaches its model within 50ms of the first. Insufficient concurrency
					// must still finish with a finding, even if the caller has no deadline.
					timer := time.NewTimer(5 * time.Second)
					select {
					case <-firstWave:
					case <-timer.C:
						mu.Lock()
						waveTimedOut = true
						mu.Unlock()
					case <-ctx.Done():
					}
					timer.Stop()
					// Retain the original request hold to observe excess concurrency too.
					select {
					case <-time.After(50 * time.Millisecond):
					case <-ctx.Done():
					}
					mu.Lock()
					active--
					mu.Unlock()
					return capacityAnswer(answerAs, "Checked.", nil), nil
				}
				if strings.Contains(body, `"role":"tool"`) {
					return capacityAnswer(answerAs, "All three check.", nil), nil
				}
				tasks := []map[string]string{}
				for _, title := range []string{"A", "B", "C"} {
					tasks = append(tasks, map[string]string{"title": title, "objective": "Check part " + title + ".", "profile": "analyze"})
				}
				return capacityAnswer(answerAs, "", map[string]any{"tasks": tasks}), nil
			}
			off := false
			input := requestInput("Check the expense pack.")
			input.Chat = runcontext.NewChatContext("")
			run := &Run{
				Wire: "openai-compatible", ModelName: "fixture", Model: model, Gate: host.gate.Calls(), Tools: host.offer.Tools, Thinking: "off",
				Prompt: "Check the expense pack.", AllowConversation: true, Interactive: true, Review: &off, Context: input,
				Children: host.children(), Emit: host.emit,
				Models: func() (runcontext.RunModels, error) {
					return runcontext.RunModels{ConcurrentRequests: &capacity}, nil
				},
			}
			seal := sealNetwork()
			runErr := with(inner).Run(ctx, run)
			reached := seal.lift()
			events := host.take()
			f := &findings{check: CapacityNames[0]}
			if runErr != nil {
				f.fail("the run failed: %v", runErr)
			}
			if len(reached) > 0 {
				f.fail("the adapter reached for http.DefaultTransport: %v", reached)
			}
			for _, failed := range errorsOf(events) {
				f.fail("an error event: %s", failed.Message)
			}
			if waveTimedOut {
				f.fail("the configured first wave of %d child requests did not arrive before the bounded wait", capacity)
			}
			if children != 3 || peak != capacity {
				f.fail("%d child requests, at most %d at once, where the connection's answer says %d", children, peak, capacity)
			}
			return f.list, nil
		},
	}
}

// capacityAnswer is one answer on the OpenAI-compatible wire, streamed or whole: the text, or one
// delegate_tasks call with these arguments.
func capacityAnswer(answerAs, text string, args map[string]any) *http.Response {
	message := map[string]any{"role": "assistant", "content": text}
	finish := "stop"
	if args != nil {
		encoded, _ := json.Marshal(args)
		message = map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{
			"index": 0, "id": "call_batch", "type": "function", "function": map[string]any{"name": turn.DelegateTool, "arguments": string(encoded)}}}}
		finish = "tool_calls"
	}
	if answerAs == "whole" {
		body, _ := json.Marshal(map[string]any{"id": "c", "object": "chat.completion", "model": "m",
			"choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": finish}}})
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(string(body)))}
	}
	frame := func(delta map[string]any, finish any) string {
		encoded, _ := json.Marshal(map[string]any{"id": "c", "object": "chat.completion.chunk", "model": "m",
			"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}})
		return "data: " + string(encoded) + "\n\n"
	}
	delta := map[string]any{"content": text}
	if args != nil {
		delta = map[string]any{"tool_calls": message["tool_calls"]}
	}
	body := frame(map[string]any{"role": "assistant", "content": nil}, nil) + frame(delta, nil) + frame(map[string]any{}, finish) + "data: [DONE]\n\n"
	return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}
}
