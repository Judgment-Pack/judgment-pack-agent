package framingtests

// #302's framing drives are a test binary of their own (docs/ci-evidence.md), so that durableapprovaltests keeps its room
// under race. A test binary cannot import another's test files, so these two are copied from durableapprovaltests, which
// copied them from go/adapter/eino.

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// readKeyCall is one streamed completion whose call carries exactly the argument text given.
func readKeyCall(id, name, arguments string) *http.Response {
	chunk := map[string]any{"id": "fixture", "object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": id, "type": "function", "function": map[string]any{"name": name, "arguments": arguments}}}}, "finish_reason": "tool_calls"}}}
	body, _ := json.Marshal(chunk)
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: " + string(body) + "\n\ndata: [DONE]\n\n"))}
}

// quarantineFinal is one streamed completion that answers "Done." and stops.
func quarantineFinal() *http.Response {
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: {\"id\":\"fixture\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Done.\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"fixture\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))}
}
