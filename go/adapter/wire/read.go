package wire

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"slices"
	"strings"

	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
)

// The answer read back from the relay (readAnswer in engines/deepagents/wire.ts): a stream where
// the answer is one, a whole object otherwise, read off the answer's content type and never off
// what was asked for.

// sseEvent is one server-sent event: its event name, where it has one, and its data text.
type sseEvent struct {
	name    string
	hasName bool
	data    string
}

// sseReader reads the events of a streamed answer as they arrive, each bounded by the run's own
// context. The TypeScript cancels the body's reader when the run's signal aborts; here the body is
// the context's, so a cancelled context ends the read, and the context is checked before each one.
type sseReader struct {
	ctx      context.Context
	body     io.Reader
	buffered []byte
	chunk    []byte
	name     string
	hasName  bool
	data     []string
	queue    []sseEvent
	done     bool
}

func newSSEReader(ctx context.Context, body io.Reader) *sseReader {
	return &sseReader{ctx: ctx, body: body, chunk: make([]byte, 16<<10)}
}

// next is the reader's next event, or false where the stream has ended.
func (r *sseReader) next() (sseEvent, bool, error) {
	for {
		if len(r.queue) > 0 {
			event := r.queue[0]
			r.queue = r.queue[1:]
			return event, true, nil
		}
		if r.done {
			return sseEvent{}, false, nil
		}
		if err := r.ctx.Err(); err != nil {
			return sseEvent{}, false, failure.Cancelled
		}
		if r.body == nil {
			r.done = true
			continue
		}
		read, err := r.body.Read(r.chunk)
		if read > 0 {
			r.buffered = append(r.buffered, r.chunk[:read]...)
		}
		ended := err != nil
		if ended && !errors.Is(err, io.EOF) {
			if r.ctx.Err() != nil {
				return sseEvent{}, false, failure.Cancelled
			}
			return sseEvent{}, false, failure.Err("connection-lost", "model", "unknown")
		}
		lines := bytes.Split(r.buffered, []byte("\n"))
		if ended {
			r.buffered = nil
		} else {
			r.buffered = lines[len(lines)-1]
			lines = lines[:len(lines)-1]
		}
		for _, raw := range lines {
			line := strings.TrimSuffix(string(raw), "\r")
			switch {
			case line == "":
				if len(r.data) > 0 {
					r.queue = append(r.queue, sseEvent{name: r.name, hasName: r.hasName, data: strings.Join(r.data, "\n")})
				}
				r.name, r.hasName, r.data = "", false, nil
			case strings.HasPrefix(line, "data:"):
				r.data = append(r.data, strings.TrimPrefix(line[5:], " "))
			case strings.HasPrefix(line, "event:"):
				r.name, r.hasName = strings.TrimSpace(line[6:]), true
			}
		}
		if ended {
			if len(r.data) > 0 {
				r.queue = append(r.queue, sseEvent{name: r.name, hasName: r.hasName, data: strings.Join(r.data, "\n")})
				r.data = nil
			}
			r.done = true
		}
	}
}

// parsedJSON is one answer's JSON text, parsed, or the failure an unreadable answer is.
func parsedJSON(text string) (*Value, error) {
	value, err := Parse([]byte(text))
	if err != nil {
		return nil, &RelayUnreadable{Detail: "the endpoint answered with something that is not JSON"}
	}
	return value, nil
}

// wholeBody is a whole answer's body, read under the run's context. A body that breaks while it is
// read is the connection ending before the answer did (connection-lost), as the vercel adapter's
// relay reports it; one that is not JSON is an answer with nothing usable in it.
func wholeBody(ctx context.Context, response *http.Response) (*Value, error) {
	if response.Body == nil {
		return parsedJSON("")
	}
	text, err := io.ReadAll(response.Body)
	if err != nil {
		if ctx.Err() != nil || failure.IsCancelled(err) {
			return nil, failure.Cancelled
		}
		return nil, failure.Err("connection-lost", "model", "unknown")
	}
	return parsedJSON(string(text))
}

// ReadAnswer is readAnswer: one answer read as a stream where the answer is one, and as a whole
// object otherwise — off the answer's content type, never off what was asked for, because an
// endpoint may ignore the stream member. ids answers an id for a call an endpoint sent without
// one (crypto.randomUUID in the TypeScript, RandomUUID here).
func ReadAnswer(ctx context.Context, family EndpointKind, response *http.Response, listener Listener, reasoningField ReasoningField, ids func() string) (Answer, error) {
	var events *sseReader
	var whole *Value
	if IsEventStream(response) {
		events = newSSEReader(ctx, response.Body)
	} else {
		read, err := wholeBody(ctx, response)
		if err != nil {
			return Answer{}, err
		}
		whole = read
	}
	switch family {
	case Anthropic:
		return anthropicAnswer(events, whole, listener)
	case Gemini:
		return geminiAnswer(events, whole, listener, ids)
	}
	return openAIAnswer(events, whole, listener, reasoningField)
}

// objectOr is the TypeScript `object(value) ? value : {}`.
func objectOr(value *Value) *Value {
	if value.IsObject() {
		return value
	}
	return Obj()
}

// firstChoice is `Array.isArray(x.choices) && object(x.choices[0]) ? x.choices[0] : …`.
func firstChoice(answer *Value) *Value {
	items := answer.Get("choices").Items()
	if len(items) > 0 && items[0].IsObject() {
		return items[0]
	}
	return nil
}

// definedIndex is Number(data.index) where it is an index an array holds. The TypeScript writes a
// non-index to a property of the array, where nothing afterwards finds it among the blocks; here
// such an event is skipped.
func definedIndex(value *Value) (int, bool) {
	number, ok := value.Number()
	if !ok || number < 0 || number != math.Trunc(number) || number > 1<<20 {
		return 0, false
	}
	return int(number), true
}

// growTo grows a slice to hold index, as a JavaScript array grows with holes.
func growTo[T any](items []T, index int) []T {
	var zero T
	for len(items) <= index {
		items = append(items, zero)
	}
	return items
}

func openAIFinish(reason *Value) FinishReason {
	switch reason.Text() {
	case "stop":
		return FinishStop
	case "length":
		return FinishLength
	case "tool_calls", "function_call":
		return FinishToolCalls
	case "content_filter":
		return FinishContentFilter
	}
	return FinishOther
}

func openAIUsage(usage *Value) Usage {
	if !usage.IsObject() {
		return Usage{}
	}
	prompt := objectOr(usage.Get("prompt_tokens_details"))
	completion := objectOr(usage.Get("completion_tokens_details"))
	read := Usage{}
	if held, ok := safeCount(usage.Get("prompt_tokens")); ok {
		read.InputTokens = &held
	}
	if held, ok := safeCount(usage.Get("completion_tokens")); ok {
		read.OutputTokens = &held
	}
	if held, ok := safeCount(prompt.Get("cached_tokens")); ok {
		read.InputTokenDetails = &InputTokenDetails{CacheReadTokens: &held}
	}
	if held, ok := safeCount(completion.Get("reasoning_tokens")); ok {
		read.OutputTokenDetails = &OutputTokenDetails{ReasoningTokens: &held}
	}
	return read
}

// openAIReasoning is the reasoning one OpenAI-compatible delta or message carries: the pinned
// member, or either.
func openAIReasoning(holder *Value, field ReasoningField) string {
	if field != "" {
		return holder.Get(string(field)).Text()
	}
	if content := holder.Get("reasoning_content"); content.IsString() && content.Text() != "" {
		return content.Text()
	}
	return holder.Get("reasoning").Text()
}

type openAICall struct {
	id        string
	name      string
	arguments string
}

func openAIAnswer(events *sseReader, whole *Value, listener Listener, field ReasoningField) (Answer, error) {
	text, reasoning := "", ""
	open := false
	var calls []*openAICall
	var finish, usage *Value
	endReasoning := func() error {
		if !open {
			return nil
		}
		open = false
		return listener.ReasoningEnd(reasoning)
	}
	take := func(holder *Value) error {
		if said := openAIReasoning(holder, field); said != "" {
			reasoning += said
			open = true
			if err := listener.Reasoning(said); err != nil {
				return err
			}
		}
		content := holder.Get("content")
		toolCalls := holder.Get("tool_calls").Items()
		said := content.IsString() && content.Text() != ""
		if said || len(toolCalls) > 0 {
			if err := endReasoning(); err != nil {
				return err
			}
		}
		if said {
			text += content.Text()
			if err := listener.Text(content.Text()); err != nil {
				return err
			}
		}
		for position, call := range toolCalls {
			index := position
			if held, ok := call.Get("index").Number(); ok {
				if read, fits := definedIndex(Num(held)); fits {
					index = read
				}
			}
			function := objectOr(call.Get("function"))
			calls = growTo(calls, index)
			if calls[index] == nil {
				calls[index] = &openAICall{}
			}
			held := calls[index]
			if id := call.Get("id"); id.IsString() && id.Text() != "" {
				held.id = id.Text()
			}
			if name := function.Get("name"); name.IsString() && name.Text() != "" {
				held.name = name.Text()
			}
			if arguments := function.Get("arguments"); arguments.IsString() {
				held.arguments += arguments.Text()
			}
		}
		return nil
	}
	if events == nil {
		answer := objectOr(whole)
		if answer.Get("error").IsObject() {
			return Answer{}, &RelayStreamError{Envelope: answer}
		}
		choice := firstChoice(answer)
		if choice == nil {
			choice = Obj()
		}
		if message := choice.Get("message"); message.IsObject() {
			if err := take(message); err != nil {
				return Answer{}, err
			}
		}
		finish = choice.Get("finish_reason")
		usage = answer.Get("usage")
	} else {
		for {
			event, more, err := events.next()
			if err != nil {
				return Answer{}, err
			}
			if !more || event.data == "[DONE]" {
				break
			}
			chunk, err := parsedJSON(event.data)
			if err != nil {
				return Answer{}, err
			}
			if !chunk.IsObject() {
				continue
			}
			if chunk.Get("error").IsObject() {
				return Answer{}, &RelayStreamError{Envelope: chunk}
			}
			if held := chunk.Get("usage"); held != nil && held.Kind() != KindNull {
				usage = held
			}
			choice := firstChoice(chunk)
			if choice == nil {
				continue
			}
			if delta := choice.Get("delta"); delta.IsObject() {
				if err := take(delta); err != nil {
					return Answer{}, err
				}
			}
			if reason := choice.Get("finish_reason"); reason != nil && reason.Kind() != KindNull {
				finish = reason
			}
		}
	}
	if err := endReasoning(); err != nil {
		return Answer{}, err
	}
	var toolCalls []ToolCall
	for _, call := range calls {
		if call == nil {
			continue
		}
		toolCalls = append(toolCalls, ToolCall{ID: call.id, Name: call.name, Arguments: call.arguments})
	}
	return Answer{
		Text:         text,
		ToolCalls:    toolCalls,
		FinishReason: openAIFinish(finish),
		Usage:        openAIUsage(usage),
		Replay:       Replay{Family: OpenAICompatible, Reasoning: reasoning},
	}, nil
}

func anthropicFinish(reason *Value) FinishReason {
	switch reason.Text() {
	case "end_turn", "stop_sequence", "pause_turn":
		return FinishStop
	case "max_tokens", "model_context_window_exceeded":
		return FinishLength
	case "tool_use":
		return FinishToolCalls
	case "refusal":
		return FinishContentFilter
	}
	return FinishOther
}

func anthropicUsage(input *Value, output *int) Usage {
	fresh, hasFresh := safeCount(input.Get("input_tokens"))
	read, hasRead := safeCount(input.Get("cache_read_input_tokens"))
	written, hasWritten := safeCount(input.Get("cache_creation_input_tokens"))
	usage := Usage{}
	if hasFresh {
		total := fresh
		if hasRead {
			total += read
		}
		if hasWritten {
			total += written
		}
		usage.InputTokens = &total
	}
	if output != nil {
		held := *output
		usage.OutputTokens = &held
	}
	if hasRead || hasWritten {
		details := &InputTokenDetails{}
		if hasRead {
			held := read
			details.CacheReadTokens = &held
		}
		if hasWritten {
			held := written
			details.CacheWriteTokens = &held
		}
		usage.InputTokenDetails = details
	}
	return usage
}

func anthropicAnswer(events *sseReader, whole *Value, listener Listener) (Answer, error) {
	var blocks []*Value
	partial := map[int]string{}
	var partialOrder []int
	text := ""
	var finish *Value
	input := Obj()
	var output *int
	setPartial := func(index int, held string) {
		if _, ok := partial[index]; !ok {
			partialOrder = append(partialOrder, index)
		}
		partial[index] = held
	}
	closeBlock := func(block *Value, index int) error {
		if block == nil {
			return nil
		}
		if block.Get("type").Text() == "tool_use" {
			if held, ok := partial[index]; ok {
				args, unparsed := argumentsOf(held)
				if unparsed == "" {
					block.Set("input", args)
				} else {
					block.Set("input", Str(unparsed))
				}
				delete(partial, index)
				partialOrder = slices.DeleteFunc(partialOrder, func(at int) bool { return at == index })
			}
		}
		if block.Get("type").Text() == "thinking" {
			return listener.ReasoningEnd(block.Get("thinking").Text())
		}
		return nil
	}
	if events == nil {
		answer := objectOr(whole)
		if answer.Get("type").Text() == "error" || answer.Get("error").IsObject() {
			return Answer{}, &RelayStreamError{Envelope: answer}
		}
		for _, item := range answer.Get("content").Items() {
			if !item.IsObject() {
				continue
			}
			block := item.Clone()
			blocks = append(blocks, block)
			switch block.Get("type").Text() {
			case "thinking":
				said := block.Get("thinking").Text()
				if said != "" {
					if err := listener.Reasoning(said); err != nil {
						return Answer{}, err
					}
				}
				if err := listener.ReasoningEnd(said); err != nil {
					return Answer{}, err
				}
			case "text":
				if held := block.Get("text"); held.IsString() {
					text += held.Text()
					if held.Text() != "" {
						if err := listener.Text(held.Text()); err != nil {
							return Answer{}, err
						}
					}
				}
			}
		}
		finish = answer.Get("stop_reason")
		input = objectOr(answer.Get("usage"))
		if held, ok := safeCount(answer.Get("usage").Get("output_tokens")); ok {
			output = &held
		}
	} else {
	stream:
		for {
			event, more, err := events.next()
			if err != nil {
				return Answer{}, err
			}
			if !more {
				break
			}
			data, err := parsedJSON(event.data)
			if err != nil {
				return Answer{}, err
			}
			if !data.IsObject() {
				continue
			}
			// data.type ?? event.event: the event's own name only where the frame named none.
			kind := ""
			if held := data.Get("type"); held != nil && held.Kind() != KindNull {
				kind = jsStringOf(held)
			} else if event.hasName {
				kind = event.name
			}
			switch kind {
			case "error":
				return Answer{}, &RelayStreamError{Envelope: data}
			case "message_start":
				message := objectOr(data.Get("message"))
				input = objectOr(message.Get("usage"))
				output = nil
				if held, ok := safeCount(message.Get("usage").Get("output_tokens")); ok {
					output = &held
				}
			case "content_block_start":
				index, ok := definedIndex(data.Get("index"))
				if !ok {
					continue
				}
				start := Obj()
				if held := data.Get("content_block"); held.IsObject() {
					start = held.Clone()
				}
				switch start.Get("type").Text() {
				case "tool_use":
					start.Set("input", Obj())
				case "thinking":
					start.Set("thinking", Str(start.Get("thinking").Text()))
					start.Set("signature", Str(start.Get("signature").Text()))
				case "text":
					start.Set("text", Str(start.Get("text").Text()))
				}
				blocks = growTo(blocks, index)
				blocks[index] = start
			case "content_block_delta":
				index, ok := definedIndex(data.Get("index"))
				if !ok {
					continue
				}
				if index >= len(blocks) || blocks[index] == nil {
					continue
				}
				block := blocks[index]
				delta := objectOr(data.Get("delta"))
				switch delta.Get("type").Text() {
				case "thinking_delta":
					if held := delta.Get("thinking"); held.IsString() {
						block.Set("thinking", Str(block.Get("thinking").Text()+held.Text()))
						if held.Text() != "" {
							if err := listener.Reasoning(held.Text()); err != nil {
								return Answer{}, err
							}
						}
					}
				case "signature_delta":
					// A signature split across events is one signature: the fragments are joined,
					// in order.
					if held := delta.Get("signature"); held.IsString() {
						block.Set("signature", Str(block.Get("signature").Text()+held.Text()))
					}
				case "text_delta":
					if held := delta.Get("text"); held.IsString() {
						block.Set("text", Str(block.Get("text").Text()+held.Text()))
						text += held.Text()
						if held.Text() != "" {
							if err := listener.Text(held.Text()); err != nil {
								return Answer{}, err
							}
						}
					}
				case "input_json_delta":
					if held := delta.Get("partial_json"); held.IsString() {
						setPartial(index, partial[index]+held.Text())
					}
				}
			case "content_block_stop":
				index, ok := definedIndex(data.Get("index"))
				if !ok {
					continue
				}
				if index < len(blocks) {
					if err := closeBlock(blocks[index], index); err != nil {
						return Answer{}, err
					}
				}
			case "message_delta":
				delta := objectOr(data.Get("delta"))
				if reason := delta.Get("stop_reason"); reason != nil && reason.Kind() != KindNull {
					finish = reason
				}
				usage := objectOr(data.Get("usage"))
				if held, ok := safeCount(usage.Get("output_tokens")); ok {
					output = &held
				}
				for _, member := range []string{"input_tokens", "cache_read_input_tokens", "cache_creation_input_tokens"} {
					if _, ok := safeCount(usage.Get(member)); ok {
						input = input.Clone()
						input.Set(member, usage.Get(member))
					}
				}
			case "message_stop":
				break stream
			}
		}
		// A block the stream never closed is closed here, so its call and its passage are not lost.
		for _, index := range append([]int{}, partialOrder...) {
			var block *Value
			if index < len(blocks) {
				block = blocks[index]
			}
			if err := closeBlock(block, index); err != nil {
				return Answer{}, err
			}
		}
	}
	var kept []*Value
	for _, block := range blocks {
		if block.IsObject() {
			kept = append(kept, block)
		}
	}
	var toolCalls []ToolCall
	var replay []*Value
	for _, block := range kept {
		carried := block
		if block.Get("type").Text() == "tool_use" {
			held := block.Get("input")
			call := ToolCall{ID: jsStringOf(block.Get("id")), Name: jsStringOf(block.Get("name"))}
			switch {
			case held.IsString():
				// A call whose input could not be read goes back with the text the endpoint sent as
				// its input, and the replay carries an empty object in its place.
				call.Arguments = held.Text()
				carried = block.Clone()
				carried.Set("input", Obj())
			case held.IsObject():
				call.Arguments = held.String()
			}
			toolCalls = append(toolCalls, call)
		}
		replay = append(replay, carried)
	}
	return Answer{
		Text:         text,
		ToolCalls:    toolCalls,
		FinishReason: anthropicFinish(finish),
		Usage:        anthropicUsage(input, output),
		Replay:       Replay{Family: Anthropic, Blocks: replay},
	}, nil
}

func geminiFinish(reason *Value, calls int, blocked bool) FinishReason {
	if blocked {
		return FinishContentFilter
	}
	named := reason.Text()
	if named == "STOP" || reason == nil {
		if calls > 0 {
			return FinishToolCalls
		}
		return FinishStop
	}
	if named == "MAX_TOKENS" {
		return FinishLength
	}
	if slices.Contains([]string{"SAFETY", "RECITATION", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII", "IMAGE_SAFETY"}, jsStringOf(reason)) {
		return FinishContentFilter
	}
	if named == "MALFORMED_FUNCTION_CALL" {
		return FinishError
	}
	return FinishOther
}

func geminiUsage(usage *Value) Usage {
	if !usage.IsObject() {
		return Usage{}
	}
	read := Usage{}
	if held, ok := safeCount(usage.Get("promptTokenCount")); ok {
		read.InputTokens = &held
	}
	candidates, hasCandidates := safeCount(usage.Get("candidatesTokenCount"))
	thoughts, hasThoughts := safeCount(usage.Get("thoughtsTokenCount"))
	if hasCandidates || hasThoughts {
		total := 0
		if hasCandidates {
			total += candidates
		}
		if hasThoughts {
			total += thoughts
		}
		read.OutputTokens = &total
	}
	if held, ok := safeCount(usage.Get("cachedContentTokenCount")); ok {
		read.InputTokenDetails = &InputTokenDetails{CacheReadTokens: &held}
	}
	if hasThoughts {
		held := thoughts
		read.OutputTokenDetails = &OutputTokenDetails{ReasoningTokens: &held}
	}
	return read
}

// geminiAnswer joins Gemini's streamed pieces into the parts that come back.
//
// A text piece continues the part before it where both are text of the same kind (a thought, or
// not) and the part before it carries no signature yet: a signature belongs to the bytes it was
// computed over and closes its part. So a summary streamed in pieces with its signature on the
// last comes back as one signed part, and two empty signed thoughts come back as two.
func geminiAnswer(events *sseReader, whole *Value, listener Listener, ids func() string) (Answer, error) {
	var parts []*Value
	text := ""
	var finish, usage *Value
	blocked := false
	// thinking is the thought part whose passage is still open.
	var thinking *Value
	endThought := func() error {
		if thinking == nil {
			return nil
		}
		passage := thinking.Get("text").Text()
		thinking = nil
		return listener.ReasoningEnd(passage)
	}
	take := func(part *Value) error {
		var last *Value
		if len(parts) > 0 {
			last = parts[len(parts)-1]
		}
		isText := part.Get("text").IsString() && part.Get("functionCall") == nil
		isThought := isTrue(part.Get("thought"))
		continues := isText && last != nil && last.Get("text").IsString() &&
			last.Get("functionCall") == nil && isTrue(last.Get("thought")) == isThought &&
			last.Get("thoughtSignature") == nil
		if continues {
			last.Set("text", Str(last.Get("text").Text()+part.Get("text").Text()))
			for _, member := range part.Members() {
				if member.Name != "text" {
					last.Set(member.Name, member.Value)
				}
			}
		} else {
			// A new part ends the passage before it, whatever kind it is.
			if err := endThought(); err != nil {
				return err
			}
			parts = append(parts, part.Clone())
		}
		held := parts[len(parts)-1]
		switch {
		case isThought && isText:
			thinking = held
			if part.Get("text").Text() != "" {
				if err := listener.Reasoning(part.Get("text").Text()); err != nil {
					return err
				}
			}
			if held.Get("thoughtSignature") != nil {
				if err := endThought(); err != nil {
					return err
				}
			}
		case isText && part.Get("text").Text() != "":
			text += part.Get("text").Text()
			if err := listener.Text(part.Get("text").Text()); err != nil {
				return err
			}
		}
		return nil
	}
	read := func(answer *Value) error {
		if answer.Get("error").IsObject() {
			return &RelayStreamError{Envelope: answer}
		}
		if feedback := answer.Get("promptFeedback"); feedback.IsObject() && feedback.Get("blockReason") != nil {
			blocked = true
		}
		candidate := Obj()
		if items := answer.Get("candidates").Items(); len(items) > 0 && items[0].IsObject() {
			candidate = items[0]
		}
		content := objectOr(candidate.Get("content"))
		for _, part := range content.Get("parts").Items() {
			if !part.IsObject() {
				continue
			}
			if err := take(part); err != nil {
				return err
			}
		}
		if reason := candidate.Get("finishReason"); reason != nil && reason.Kind() != KindNull {
			finish = reason
		}
		if held := answer.Get("usageMetadata"); held != nil {
			usage = held
		}
		return nil
	}
	if events == nil {
		answers := []*Value{whole}
		if whole.Kind() == KindArray {
			answers = whole.Items()
		}
		for _, answer := range answers {
			if !answer.IsObject() {
				continue
			}
			if err := read(answer); err != nil {
				return Answer{}, err
			}
		}
	} else {
		for {
			event, more, err := events.next()
			if err != nil {
				return Answer{}, err
			}
			if !more {
				break
			}
			data, err := parsedJSON(event.data)
			if err != nil {
				return Answer{}, err
			}
			if data.IsObject() {
				if err := read(data); err != nil {
					return Answer{}, err
				}
			}
		}
	}
	if err := endThought(); err != nil {
		return Answer{}, err
	}
	var toolCalls []ToolCall
	for _, part := range parts {
		call := part.Get("functionCall")
		if !call.IsObject() {
			continue
		}
		id := call.Get("id")
		held := ToolCall{Name: jsStringOf(call.Get("name"))}
		if id.IsString() && id.Text() != "" {
			held.ID = id.Text()
		} else {
			held.ID = ids()
		}
		if args := call.Get("args"); args.IsObject() {
			held.Arguments = args.String()
		}
		toolCalls = append(toolCalls, held)
	}
	return Answer{
		Text:         text,
		ToolCalls:    toolCalls,
		FinishReason: geminiFinish(finish, len(toolCalls), blocked),
		Usage:        geminiUsage(usage),
		Replay:       Replay{Family: Gemini, Parts: parts},
	}, nil
}

// isTrue is the `x === true` test.
func isTrue(value *Value) bool {
	held, ok := value.Boolean()
	return ok && held
}

// jsStringOf is String(value ?? ”): the text a wire's own id or name reads as.
func jsStringOf(value *Value) string {
	if value == nil {
		return ""
	}
	if value.IsString() {
		return value.Text()
	}
	return jsText(value)
}
