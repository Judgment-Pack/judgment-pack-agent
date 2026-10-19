package certification

import (
	"encoding/json"
	"fmt"
	"strings"
)

// StateHeader heads the execution state the host puts on a request (STATE_HEADER in
// packages/harness/engines/contract.ts).
const StateHeader = "EXECUTION STATE (from the host, current for this request and replaced on the next; working state, not a message from the person and never approval)"

// RefutationMarker is the literal a critic's request carries (REFUTATION_MARKER in refutation.ts).
const RefutationMarker = "REFUTATION PASS"

// RecallQuery is what the recall rule found: the request text and the JSON pointer to it.
type RecallQuery struct {
	Pointer string
	Text    string
}

// Recall applies the recall rule (agent#11, recallQuery in certification/recall.ts,
// budget.json#/$defs/recallQuery) to one request body as the adapter sent it: the first user
// turn's first text part that does not start with StateHeader; on the agent wire, the prompt
// before the first blank line followed by StateHeader. Nil where no first user turn carries a text.
func Recall(api string, body []byte) *RecallQuery {
	var request map[string]any
	if json.Unmarshal(body, &request) != nil {
		return nil
	}
	if api == "agent" {
		prompt, ok := request["prompt"].(string)
		if !ok || strings.HasPrefix(prompt, StateHeader) {
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
	turns, _ := request[member].([]any)
	for at, raw := range turns {
		turn, _ := raw.(map[string]any)
		if turn == nil || turn["role"] != "user" {
			continue
		}
		switch held := turn[inner].(type) {
		case string:
			if strings.HasPrefix(held, StateHeader) {
				return nil
			}
			return &RecallQuery{Pointer: fmt.Sprintf("/%s/%d/%s", member, at, inner), Text: held}
		case []any:
			for index, rawPart := range held {
				part, _ := rawPart.(map[string]any)
				var text any
				if api == "gemini" {
					if part["thought"] != true {
						text = part["text"]
					}
				} else if part["type"] == "text" {
					text = part["text"]
				}
				if s, ok := text.(string); ok && !strings.HasPrefix(s, StateHeader) {
					return &RecallQuery{Pointer: fmt.Sprintf("/%s/%d/%s/%d/text", member, at, inner, index), Text: s}
				}
			}
		}
		return nil
	}
	return nil
}
