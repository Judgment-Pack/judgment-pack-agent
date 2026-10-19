package runcontext

// Where the request text a run started from sits on each model wire, and the rule a server
// applies to find it on any request of the run (agent#11, the harness side; the port of
// packages/harness/certification/recall.ts). The contract's budget.json#/$defs/recallQuery.
//
// The rule. Take the request's first user turn: the first message with role user in messages
// (OpenAI-compatible and Anthropic; on the OpenAI-compatible wire system messages may come before
// it) or the first content with role user in contents (Gemini). The request text is its first
// text part that does not start with StateHeader; a string content is one text part. On the
// Gemini wire a part is text when it carries text and is not a thought.
//
// Why it holds on every request. The adapter puts the request text first in the run's first user
// message and never moves, replaces or drops that message: the execution state joins it only as a
// later part, a chat's index of earlier results is its last part (chat.go), and compaction keeps
// it whole. Everything else the loop adds comes after it, so the newest user text is often not
// the request; the first user turn always is.
//
// The agent wire (the Codex engine) sends one string, the run's prompt, not a list of turns: the
// person's message first, then, when the run has any, the host's state and the execution guidance
// headed by StateHeader after a blank line, then a chat's index of earlier results. The request
// text is the prompt's text before the first blank line that is followed by StateHeader, or the
// whole prompt where the header is absent; a prompt that starts with the header has none
// (agent#36).

import (
	"encoding/json"
	"strconv"
	"strings"
)

// StateHeader heads the execution state the host puts on a request (STATE_HEADER in
// packages/harness/engines/contract.ts). The turn package ports that file; the constant is here
// because the rule is stated in terms of it, and a test holds the two equal.
const StateHeader = "EXECUTION STATE (from the host, current for this request and replaced on the next; working state, not a message from the person and never approval)"

// RecallQuery is what the rule found: the text, and the JSON pointer (RFC 6901) to it in the
// request body.
type RecallQuery struct {
	Pointer string
	Text    string
}

// Recall is recallQuery: the rule applied to one request body as the adapter sent it, for the
// wire api ("openai-compatible", "anthropic", "gemini" or "agent"). Nil where no first user turn
// carries a text.
func Recall(api string, body []byte) *RecallQuery {
	var request map[string]json.RawMessage
	if json.Unmarshal(body, &request) != nil {
		return nil
	}
	if api == "agent" {
		var prompt string
		if json.Unmarshal(request["prompt"], &prompt) != nil || strings.HasPrefix(prompt, StateHeader) {
			return nil
		}
		if cut := strings.Index(prompt, "\n\n"+StateHeader); cut >= 0 {
			prompt = prompt[:cut]
		}
		return &RecallQuery{Pointer: "/prompt", Text: prompt}
	}
	member, inner := "messages", "content"
	if api == "gemini" {
		member, inner = "contents", "parts"
	}
	turns, err := ParseJSON(request[member])
	if err != nil || !turns.IsArray() {
		return nil
	}
	for at, turn := range turns.Items() {
		role := turn.Get("role")
		if role == nil || !role.IsString() || role.Str() != "user" {
			continue
		}
		held := turn.Get(inner)
		pointer := "/" + member + "/" + strconv.Itoa(at) + "/" + inner
		if held.IsString() {
			if strings.HasPrefix(held.Str(), StateHeader) {
				return nil
			}
			return &RecallQuery{Pointer: pointer, Text: held.Str()}
		}
		if !held.IsArray() {
			return nil
		}
		for index, part := range held.Items() {
			var text *Value
			if api == "gemini" {
				if !part.Get("thought").Truth() {
					text = part.Get("text")
				}
			} else if kind := part.Get("type"); kind.IsString() && kind.Str() == "text" {
				text = part.Get("text")
			}
			if text.IsString() && !strings.HasPrefix(text.Str(), StateHeader) {
				return &RecallQuery{Pointer: pointer + "/" + strconv.Itoa(index) + "/text", Text: text.Str()}
			}
		}
		return nil
	}
	return nil
}

// AtPointer is atPointer: the value at a JSON pointer (RFC 6901) in a value, or nil.
func AtPointer(value *Value, pointer string) *Value {
	held := value
	if pointer == "" {
		return held
	}
	for _, raw := range strings.Split(pointer, "/")[1:] {
		key := strings.ReplaceAll(strings.ReplaceAll(raw, "~1", "/"), "~0", "~")
		if held == nil || (held.Kind() != KindObject && held.Kind() != KindArray) || !held.Has(key) {
			return nil
		}
		held = held.Get(key)
	}
	return held
}
