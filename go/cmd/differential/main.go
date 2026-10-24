// Command differential is a test-only NDJSON bridge to the production prompt and wire entrypoints.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	w "github.com/Judgment-Pack/judgment-pack-agent/go/adapter/wire"
	c "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

type listener struct{ events [][]string }

func (l *listener) Text(s string) error { l.events = append(l.events, []string{"text", s}); return nil }
func (l *listener) Reasoning(s string) error {
	l.events = append(l.events, []string{"reasoning", s})
	return nil
}
func (l *listener) ReasoningEnd(s string) error {
	l.events = append(l.events, []string{"reasoningEnd", s})
	return nil
}
func number(v *w.Value) int   { n, _ := v.Number(); return int(n) }
func boolean(v *w.Value) bool { b, _ := v.Boolean(); return b }
func image(v *w.Value) w.ImagePart {
	return w.ImagePart{MediaType: v.Get("mediaType").Text(), Bytes: v.Get("bytes").Text(), Reference: v.Get("reference").Text()}
}
func images(v *w.Value) []w.ImagePart {
	out := []w.ImagePart{}
	for _, v := range v.Items() {
		out = append(out, image(v))
	}
	return out
}
func encoded(e w.Encoded) *w.Value {
	headers := w.Obj()
	// Header maps have no order contract; emit the known names in protocol order.
	for _, name := range []string{"content-type", "anthropic-version"} {
		if value, ok := e.Headers[name]; ok {
			headers.Set(name, w.Str(value))
		}
	}
	if len(headers.Keys()) != len(e.Headers) {
		panic("unhandled protocol header")
	}
	return w.Obj(w.F("suffix", w.Str(e.Suffix)), w.F("body", w.Str(string(e.Body))), w.F("headers", headers))
}
func usage(u w.Usage) *w.Value {
	out := w.Obj()
	if u.InputTokens != nil {
		out.Set("inputTokens", w.Num(float64(*u.InputTokens)))
	}
	if u.OutputTokens != nil {
		out.Set("outputTokens", w.Num(float64(*u.OutputTokens)))
	}
	if u.InputTokenDetails != nil {
		v := w.Obj()
		if u.InputTokenDetails.CacheReadTokens != nil {
			v.Set("cacheReadTokens", w.Num(float64(*u.InputTokenDetails.CacheReadTokens)))
		}
		if u.InputTokenDetails.CacheWriteTokens != nil {
			v.Set("cacheWriteTokens", w.Num(float64(*u.InputTokenDetails.CacheWriteTokens)))
		}
		out.Set("inputTokenDetails", v)
	}
	if u.OutputTokenDetails != nil {
		v := w.Obj()
		if u.OutputTokenDetails.ReasoningTokens != nil {
			v.Set("reasoningTokens", w.Num(float64(*u.OutputTokenDetails.ReasoningTokens)))
		}
		out.Set("outputTokenDetails", v)
	}
	return out
}
func run(v *w.Value) (*w.Value, error) {
	ctx := context.Background()
	var memory []contract.Memory
	if v.Get("memory").Kind() != w.KindNull {
		if err := json.Unmarshal(v.Get("memory").JSON(), &memory); err != nil {
			return nil, err
		}
	}
	host := c.HostFuncs{
		BriefFunc:               func(context.Context) (string, error) { return v.Get("brief").Text(), nil },
		ProjectInstructionsFunc: func(context.Context) (string, error) { return v.Get("projectInstructions").Text(), nil },
		MemoryRecordsForFunc:    func(context.Context, string) ([]contract.Memory, error) { return memory, nil },
	}
	parts, err := c.ReadHostParts(ctx, host, "query", c.Defaults, true)
	if err != nil {
		return nil, err
	}
	// The public host reader uses the empty marker; the TS bridge does too.
	prompt := c.SystemText(v.Get("guidance").Text(), parts, v.Get("toolsText").Text())
	r := v.Get("request")
	request := w.Request{
		Placement:       w.StatePlacement(r.Get("placement").Text()),
		Family:          w.EndpointKind(r.Get("family").Text()),
		Model:           r.Get("model").Text(),
		System:          w.SystemText{Head: prompt, Tail: r.Get("system").Get("tail").Text(), HasTail: r.Get("system").Has("tail")},
		OneCallPerTurn:  boolean(r.Get("oneCallPerTurn")),
		Tier:            r.Get("tier"),
		MaxOutputTokens: number(r.Get("maxOutputTokens")),
		ReasoningField:  w.ReasoningField(r.Get("reasoningField").Text()),
		WholeAnswer:     boolean(r.Get("wholeAnswer")),
		Images:          w.ImageBounds{Count: number(r.Get("images").Get("count")), Bytes: number(r.Get("images").Get("bytes"))},
	}
	if request.Tier.Kind() == w.KindNull {
		request.Tier = nil
	}
	for _, t := range r.Get("tools").Items() {
		request.Tools = append(request.Tools, w.WireTool{Name: t.Get("name").Text(), Description: t.Get("description").Text(), Schema: t.Get("schema")})
	}
	for _, m := range r.Get("messages").Items() {
		message := w.Message{Role: w.Role(m.Get("role").Text()), CallID: m.Get("callID").Text(), Name: m.Get("name").Text(), IsError: boolean(m.Get("error")), Images: images(m.Get("images"))}
		if message.Role == w.RoleUser {
			for _, p := range m.Get("parts").Items() {
				if p.Has("image") {
					message.Parts = append(message.Parts, w.ImagePartOf(image(p.Get("image"))))
				} else {
					message.Parts = append(message.Parts, w.TextPart(p.Get("text").Text()))
				}
			}
		} else {
			message.Parts = []w.Part{w.TextPart(m.Get("text").Text())}
		}
		for _, call := range m.Get("calls").Items() {
			message.ToolCalls = append(message.ToolCalls, w.ToolCall{ID: call.Get("id").Text(), Name: call.Get("name").Text(), Arguments: string(call.Get("args").JSON())})
		}
		if replay := m.Get("replay"); replay.IsObject() {
			message.Replay = &w.Replay{Family: w.EndpointKind(replay.Get("family").Text()), Blocks: replay.Get("blocks").Items(), Parts: replay.Get("parts").Items(), Reasoning: replay.Get("reasoning").Text()}
		}
		request.Messages = append(request.Messages, message)
	}
	first := w.EncodeRequest(request)
	response := v.Get("response")
	l := &listener{events: [][]string{}}
	answer, err := w.ReadAnswer(ctx, request.Family, &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{response.Get("contentType").Text()}}, Body: io.NopCloser(strings.NewReader(response.Get("body").Text()))}, l, request.ReasoningField, func() string { return "generated-id" })
	if err != nil {
		return nil, err
	}
	request.Messages = append(request.Messages, w.AnswerMessage(answer))
	events := w.Arr()
	for _, event := range l.events {
		events.Append(w.Arr(w.Str(event[0]), w.Str(event[1])))
	}
	return w.Obj(w.F("prompt", w.Str(prompt)), w.F("encoded", encoded(first)), w.F("text", w.Str(answer.Text)), w.F("finishReason", w.Str(string(answer.FinishReason))), w.F("usage", usage(answer.Usage)), w.F("events", events), w.F("replay", encoded(w.EncodeRequest(request)))), nil
}
func main() {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		v, err := w.Parse(scanner.Bytes())
		if err != nil {
			panic(err)
		}
		out, err := run(v)
		if err != nil {
			panic(fmt.Errorf("%s: %w", v.Get("caseID").Text(), err))
		}
		fmt.Println(out.String())
	}
	if err := scanner.Err(); err != nil {
		panic(err)
	}
}
