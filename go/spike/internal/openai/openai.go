// Package openai is the spikes' OpenAI-compatible wire: the request body, and the answer read as
// it arrived (one JSON object or an event stream), through the host's relay capability. Each
// spike maps its framework's messages onto these and back; the mapping is what a spike measures.
package openai

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/Judgment-Pack/judgment-pack-agent/go/certification"
)

// Message is one chat message on the wire.
type Message struct {
	Role       string     `json:"role"`
	Content    *string    `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// ToolCall is one call an assistant message carries.
type ToolCall struct {
	ID       string   `json:"id"`
	Type     string   `json:"type"`
	Function Function `json:"function"`
}

// Function is a call's name and its arguments as a JSON text.
type Function struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Tool is one tool declaration.
type Tool struct {
	Type     string      `json:"type"`
	Function Declaration `json:"function"`
}

// Declaration is a tool's name, description and parameter schema.
type Declaration struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

// Text makes a content pointer.
func Text(s string) *string { return &s }

// Complete sends one request through the relay and reads the answer as it came: a JSON object, or
// an event stream of chunks, whatever was asked for.
func Complete(ctx context.Context, call certification.ModelCall, messages []Message, tools []Tool, stream bool) (Message, error) {
	body, err := json.Marshal(map[string]any{"model": "scripted-model", "messages": messages, "tools": tools, "stream": stream})
	if err != nil {
		return Message{}, err
	}
	response, err := call(ctx, "chat/completions", certification.ModelRequest{
		Headers: map[string]string{"content-type": "application/json", "accept": "application/json, text/event-stream"},
		Body:    body,
	})
	if err != nil {
		return Message{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		raw, _ := io.ReadAll(response.Body)
		return Message{}, fmt.Errorf("the relay answered %d: %s", response.StatusCode, raw)
	}
	if strings.HasPrefix(response.Header.Get("content-type"), "text/event-stream") {
		return readStream(response.Body)
	}
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		return Message{}, err
	}
	var answer struct {
		Choices []struct {
			Message Message `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &answer); err != nil || len(answer.Choices) == 0 {
		return Message{}, fmt.Errorf("an answer that is not a completion: %.200s", raw)
	}
	return answer.Choices[0].Message, nil
}

func readStream(body io.Reader) (Message, error) {
	message := Message{Role: "assistant"}
	var text strings.Builder
	sawText := false
	calls := map[int]*ToolCall{}
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 1<<20), 16<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content   *string `json:"content"`
					ToolCalls []struct {
						Index    int      `json:"index"`
						ID       string   `json:"id"`
						Function Function `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return Message{}, fmt.Errorf("a chunk that is not JSON: %.200s", data)
		}
		for _, choice := range chunk.Choices {
			if choice.Delta.Content != nil {
				sawText = true
				text.WriteString(*choice.Delta.Content)
			}
			for _, delta := range choice.Delta.ToolCalls {
				call, ok := calls[delta.Index]
				if !ok {
					call = &ToolCall{Type: "function"}
					calls[delta.Index] = call
				}
				if delta.ID != "" {
					call.ID = delta.ID
				}
				call.Function.Name += delta.Function.Name
				call.Function.Arguments += delta.Function.Arguments
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return Message{}, err
	}
	if sawText {
		message.Content = Text(text.String())
	}
	indexes := make([]int, 0, len(calls))
	for index := range calls {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	for _, index := range indexes {
		message.ToolCalls = append(message.ToolCalls, *calls[index])
	}
	return message, nil
}
