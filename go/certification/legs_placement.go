package certification

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

// atPointer is the value at a JSON pointer in a body, or nil.
func atPointer(body []byte, pointer string) any {
	var value any
	if json.Unmarshal(body, &value) != nil {
		return nil
	}
	for _, token := range strings.Split(strings.TrimPrefix(pointer, "/"), "/") {
		token = strings.ReplaceAll(strings.ReplaceAll(token, "~1", "/"), "~0", "~")
		switch held := value.(type) {
		case map[string]any:
			value = held[token]
		case []any:
			at, err := strconv.Atoi(token)
			if err != nil || at < 0 || at >= len(held) {
				return nil
			}
			value = held[at]
		default:
			return nil
		}
	}
	return value
}

// placementCheck holds where the execution state sits, headed STATE_HEADER, in the first and second
// requests of the scenario on each wire and placement to budget/state-placement.json (agent#9): the
// header the recall rule cuts at (agent#11) is where the vector says, on every item.
var placementCheck = Check{Name: "placement", Block: "engine %s", Legs: []Endpoint{{API: "openai-compatible", AnswerAs: "whole"}}, Run: func(ctx context.Context, server *Server, _ Endpoint, adapter Adapter) ([]Finding, error) {
	_, items, err := ReadVector("budget/state-placement.json")
	if err != nil {
		return nil, err
	}
	f := &findings{check: "placement"}
	runs := map[string]*LegResult{}
	for _, raw := range items {
		var item struct {
			Family       string                                   `json:"family"`
			Thinking     string                                   `json:"thinking"`
			Capabilities *contract.CapabilityEndpointCapabilities `json:"capabilities"`
			Placement    string                                   `json:"placement"`
			Request      int                                      `json:"request"`
			Pointer      string                                   `json:"pointer"`
			Header       string                                   `json:"header"`
		}
		if err := json.Unmarshal(raw, &item); err != nil {
			return nil, err
		}
		key := item.Family + "|" + item.Thinking + "|" + string(mustMarshal(item.Capabilities))
		r := runs[key]
		if r == nil {
			mode := "off"
			if item.Thinking != "off" {
				mode = "on"
			}
			r, err = run(ctx, server, Endpoint{API: item.Family, AnswerAs: "whole", Thinking: mode}, LegOptions{Tier: item.Thinking, Capabilities: item.Capabilities}, adapter)
			if err != nil {
				return nil, err
			}
			runs[key] = r
		}
		if item.Request > len(r.bodies) {
			f.fail("%s %s: no request %d", item.Family, item.Placement, item.Request)
			continue
		}
		// The state is the text at the pointer from its header, which starts a line, to the end; and
		// the header is nowhere else in the request (prefix.test.ts).
		body := r.bodies[item.Request-1]
		text, _ := atPointer(body, item.Pointer).(string)
		at := strings.Index(text, item.Header)
		if item.Header != StateHeader || at < 0 || at > 0 && text[at-1] != '\n' || strings.Count(string(body), item.Header) != 1 {
			f.fail("%s %s request %d: the header is not where %s says, once", item.Family, item.Placement, item.Request, item.Pointer)
		}
	}
	return f.list, nil
}}

func mustMarshal(value any) []byte {
	encoded, _ := json.Marshal(value)
	return encoded
}

func init() { WireChecks = append(WireChecks, placementCheck) }
