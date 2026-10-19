package runcontext

// The recall rule, held to the contract's five recall vectors, as go/certification's
// TestTheRecallRuleIsTheVectors holds its own reading (the port of certification/recall.ts).

import (
	"strings"
	"testing"
)

func TestTheRecallRuleIsTheVectors(t *testing.T) {
	for _, name := range []string{"openai-compatible", "anthropic", "gemini", "agent", "delegated-child"} {
		for at, item := range vectorItems(t, "recall/"+name+".json") {
			found := Recall(item.Get("family").Str(), item.Get("body").JSON())
			if found == nil {
				t.Errorf("%s item %d: the rule found nothing", name, at)
				continue
			}
			if found.Pointer != item.Get("pointer").Str() || found.Text != item.Get("text").Str() {
				t.Errorf("%s item %d: the rule found %q at %s", name, at, found.Text, found.Pointer)
			}
			// And the pointer names that text in the body. On the agent wire the pointer names
			// the whole prompt, of which the request text is the part before the state's header.
			held := AtPointer(item.Get("body"), found.Pointer)
			if item.Get("family").Str() == "agent" {
				if !held.IsString() || !strings.HasPrefix(held.Str(), found.Text) {
					t.Errorf("%s item %d: the prompt does not open with the text", name, at)
				}
				continue
			}
			if !held.IsString() || held.Str() != found.Text {
				t.Errorf("%s item %d: the pointer %s does not name the text", name, at, found.Pointer)
			}
		}
	}
}

func TestTheRecallRuleFindsNoRequestTextWhereThereIsNone(t *testing.T) {
	// A prompt that starts with the header has no request text; so has a request with no user turn.
	if held := Recall("agent", []byte(`{"prompt":`+QuoteJSON(StateHeader+"\nthe state")+`}`)); held != nil {
		t.Errorf("the rule found %+v", held)
	}
	if held := Recall("openai-compatible", []byte(`{"messages":[{"role":"system","content":"s"}]}`)); held != nil {
		t.Errorf("the rule found %+v", held)
	}
	// The first user turn's first text part, even where a later turn carries newer text.
	held := Recall("anthropic", []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"the request"}]},{"role":"user","content":[{"type":"text","text":"TOOL RESULT"}]}]}`))
	if held == nil || held.Text != "the request" || held.Pointer != "/messages/0/content/0/text" {
		t.Errorf("the rule found %+v", held)
	}
	// On the Gemini wire a thought is not the request text.
	held = Recall("gemini", []byte(`{"contents":[{"role":"user","parts":[{"text":"a thought","thought":true},{"text":"the request"}]}]}`))
	if held == nil || held.Text != "the request" || held.Pointer != "/contents/0/parts/1/text" {
		t.Errorf("the rule found %+v", held)
	}
	// On the agent wire the request text is the prompt before the blank line the header follows.
	held = Recall("agent", []byte(`{"prompt":`+QuoteJSON("Write it.\n\n"+StateHeader+"\nthe state")+`}`))
	if held == nil || held.Text != "Write it." {
		t.Errorf("the rule found %+v", held)
	}
}

func TestTheStateHeaderIsTheOneTheWireUses(t *testing.T) {
	// The turn package ports engines/contract.ts; this holds the header the rule reads to the one
	// the contract's own vectors carry.
	for _, item := range vectorItems(t, "recall/agent.json") {
		prompt := item.Get("body").Get("prompt")
		if !prompt.IsString() || !strings.Contains(prompt.Str(), StateHeader) {
			t.Fatalf("the vector's prompt does not carry the header this package holds")
		}
	}
}
