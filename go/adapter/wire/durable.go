package wire

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"unicode/utf8"

	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
)

// DurableModelBytes bounds private in-memory response quarantine. Bytes are replayed to
// the engine only after its claim finishes; they are never written into a checkpoint.
const DurableModelBytes = 4 * 1024 * 1024

func incompleteModel() error {
	return failure.Authored("invalid-response", "The durable model response has no verified complete terminal outcome.", "model")
}

// QuarantineDurableResponse reads the actual response to a bounded, cancellable end,
// validates the source family's explicit terminal protocol, and restores exactly those
// bytes. HTTP headers, ordinary EOF, local Close and SDK advancement are not proof.
func QuarantineDurableResponse(ctx context.Context, family EndpointKind, response *http.Response) error {
	if response == nil || response.Body == nil {
		return incompleteModel()
	}
	original := response.Body
	stop := context.AfterFunc(ctx, func() { original.Close() })
	defer stop()
	raw, err := io.ReadAll(io.LimitReader(original, DurableModelBytes+1))
	original.Close()
	if ctx.Err() != nil {
		return failure.Cancelled
	}
	if err != nil {
		return failure.Err("connection-lost", "model", "unknown")
	}
	if len(raw) > DurableModelBytes || !utf8.Valid(raw) {
		return incompleteModel()
	}
	response.Body = io.NopCloser(bytes.NewReader(raw))
	if response.StatusCode < 200 || response.StatusCode > 299 {
		value, parseErr := Parse(raw)
		if !utf8.Valid(raw) || parseErr != nil || !value.IsObject() {
			return incompleteModel()
		}
		return &RelayRefusal{Status: response.StatusCode, Body: string(raw), Headers: response.Header}
	}
	return verifyDurableTerminal(ctx, family, IsEventStream(response), raw)
}

func verifyDurableTerminal(ctx context.Context, family EndpointKind, streamed bool, raw []byte) error {
	terminal, marker, started := false, false, false
	blocks := map[int]bool{}
	take := func(value *Value, event string) error {
		if !value.IsObject() || value.Get("error") != nil && value.Get("error").Kind() != KindNull {
			return incompleteModel()
		}
		switch family {
		case OpenAICompatible:
			choices := value.Get("choices").Items()
			if len(choices) > 1 {
				return incompleteModel()
			}
			if len(choices) == 0 {
				return nil
			} // Usage-only chunks do not create a terminal.
			choice := choices[0]
			if !choice.IsObject() {
				return incompleteModel()
			}
			if index := choice.Get("index"); index != nil {
				if n, ok := index.Number(); !ok || n != 0 {
					return incompleteModel()
				}
			}
			if terminal && choice.Get("delta").IsObject() && len(choice.Get("delta").Keys()) > 0 {
				return incompleteModel()
			}
			reason := choice.Get("finish_reason")
			if reason != nil && reason.Kind() != KindNull {
				if !reason.IsString() || openAIFinish(reason) == FinishOther || terminal {
					return incompleteModel()
				}
				terminal = true
			}
		case Anthropic:
			if !streamed {
				if value.Get("content").Kind() != KindArray || !value.Get("stop_reason").IsString() || anthropicFinish(value.Get("stop_reason")) == FinishOther {
					return incompleteModel()
				}
				terminal = true
				marker = true
				return nil
			}
			kind := value.Get("type").Text()
			if kind == "" {
				kind = event
			}
			if terminal && (kind == "message_start" || kind == "content_block_start" || kind == "content_block_delta" || kind == "content_block_stop") {
				return incompleteModel()
			}
			switch kind {
			case "error":
				return incompleteModel()
			case "message_start":
				if started {
					return incompleteModel()
				}
				started = true
			case "content_block_start":
				index, ok := definedIndex(value.Get("index"))
				if !ok || blocks[index] {
					return incompleteModel()
				}
				blocks[index] = true
			case "content_block_delta":
				index, ok := definedIndex(value.Get("index"))
				if !ok || !blocks[index] {
					return incompleteModel()
				}
			case "content_block_stop":
				index, ok := definedIndex(value.Get("index"))
				if !ok || !blocks[index] {
					return incompleteModel()
				}
				delete(blocks, index)
			case "message_delta":
				reason := value.Get("delta").Get("stop_reason")
				if reason != nil && reason.Kind() != KindNull {
					if !reason.IsString() || anthropicFinish(reason) == FinishOther || terminal {
						return incompleteModel()
					}
					terminal = true
				}
			case "message_stop":
				if !started || !terminal || len(blocks) != 0 {
					return incompleteModel()
				}
				marker = true
			case "ping":
			default:
				return incompleteModel()
			}
		case Gemini:
			if feedback := value.Get("promptFeedback"); feedback.Get("blockReason").IsString() && feedback.Get("blockReason").Text() != "" {
				terminal = true
			}
			candidates := value.Get("candidates").Items()
			if len(candidates) > 1 {
				return incompleteModel()
			}
			if len(candidates) == 0 {
				return nil
			}
			candidate := candidates[0]
			if !candidate.IsObject() {
				return incompleteModel()
			}
			if terminal && len(candidate.Get("content").Get("parts").Items()) > 0 {
				return incompleteModel()
			}
			reason := candidate.Get("finishReason")
			if reason != nil && reason.Kind() != KindNull {
				if !reason.IsString() || geminiFinish(reason, 0, false) == FinishOther || geminiFinish(reason, 0, false) == FinishError || terminal {
					return incompleteModel()
				}
				terminal = true
			}
		default:
			return incompleteModel()
		}
		return nil
	}
	if streamed {
		reader := newSSEReader(ctx, bytes.NewReader(raw))
		for {
			event, more, err := reader.next()
			if err != nil {
				return err
			}
			if !more {
				break
			}
			if marker {
				return incompleteModel()
			}
			if family == OpenAICompatible && event.data == "[DONE]" {
				if !terminal {
					return incompleteModel()
				}
				marker = true
				continue
			}
			value, err := Parse([]byte(event.data))
			if err != nil {
				return incompleteModel()
			}
			if err = take(value, event.name); err != nil {
				return err
			}
		}
	} else {
		value, err := Parse(raw)
		if err != nil {
			return incompleteModel()
		}
		if family == Gemini && value.Kind() == KindArray {
			for _, item := range value.Items() {
				if err = take(item, ""); err != nil {
					return err
				}
			}
		} else if err = take(value, ""); err != nil {
			return err
		}
	}
	if ctx.Err() != nil {
		return failure.Cancelled
	}
	if !terminal || streamed && (family == OpenAICompatible || family == Anthropic) && !marker {
		return incompleteModel()
	}
	return nil
}
