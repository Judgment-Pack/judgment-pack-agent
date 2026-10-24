package turn

// The proposal as a host tool call (propose_pack), the state header every request's working text
// begins with, and the text half of a tool answer.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

// critic.test.ts offers propose_pack among the host tools a critic may not reach; the arguments
// the adapter reads are the fence's rules, held on the tool path too.
func TestReadProposedPack(t *testing.T) {
	read, err := ReadProposedPack(json.RawMessage(`{"documentJson":"{\"id\":\"p\"}","unknowns":["who signs"]}`))
	if err != nil {
		t.Fatalf("the call's arguments are a proposal: %v", err)
	}
	same(t, "the document", read.Document, json.RawMessage(`{"id":"p"}`))
	same(t, "the unknowns", read.Unknowns, json.RawMessage(`["who signs"]`))
	read, err = ReadProposedPack(json.RawMessage(`{"documentJson":"{\"id\":\"p\"}","kind":"create"}`))
	if err != nil {
		t.Fatalf("a call that names the kind create is a proposal: %v", err)
	}
	same(t, "the unknowns a call left out", read.Unknowns, json.RawMessage(`[]`))
	for _, held := range []struct{ args, why string }{
		{`[]`, "Call propose_pack with an object holding documentJson and unknowns."},
		{`{"unknowns":[]}`, "propose_pack now requires documentJson containing the complete document as JSON text; omit the legacy document member."},
		{`{"document":"{\"id\":\"p\"}"}`, "propose_pack now requires documentJson containing the complete document as JSON text; omit the legacy document member."},
		{`{"document":null}`, "propose_pack now requires documentJson containing the complete document as JSON text; omit the legacy document member."},
		{`{"documentJson":"{}","kind":"replace"}`, `Only a new pack can be proposed: leave kind out, or pass "create".`},
		{`{"documentJson":"{}","unknowns":"one"}`, "Pass unknowns as an array of strings."},
		{`{"documentJson":"{}","unknowns":[1]}`, "Pass unknowns as an array of strings."},
	} {
		_, err := ReadProposedPack(json.RawMessage(held.args))
		if err == nil {
			t.Errorf("%s is refused", held.args)
			continue
		}
		text(t, held.args, err.Error(), held.why)
		if failureOf(t, err).Code != "invalid-tool-input" {
			t.Errorf("%s: the class the model reads and corrects", held.args)
		}
	}
	// A proposal made through the tool beside another in a fenced block is ambiguous.
	if err := TwoProposals(); failureOf(t, err).Code != "invalid-response" ||
		!strings.Contains(err.Error(), "it must carry exactly one") {
		t.Errorf("two proposals in one answer: %v", err)
	}
	// Whether the host offered the tool at all.
	if !OffersProposeTool([]contract.ToolOfferHostTool{{Name: "read_page"}, {Name: ProposePackTool}}) {
		t.Error("the host declared propose_pack")
	}
	if OffersProposeTool([]contract.ToolOfferHostTool{{Name: "read_page"}}) {
		t.Error("a host that declared no propose_pack is not offering it")
	}
	if !strings.HasPrefix(ProposePackInstructions, "\nDeliver the pack proposal by calling propose_pack") {
		t.Errorf("what the model is told about the tool: %q", ProposePackInstructions)
	}
}

// A recovery action for old replies, never an automatic import of chat prose
// (recoverableProposal).
func TestRecoverableProposal(t *testing.T) {
	whole := "```json\n{\"proposal\":{\"document\":{\"specVersion\":\"0.2.0-draft\",\"id\":\"p\",\"title\":\"A pack\"}}}\n```"
	read := RecoverableProposal(whole)
	if read == nil {
		t.Fatal("a reply carrying a whole pack is recoverable")
	}
	same(t, "the document", read.Document, json.RawMessage(`{"specVersion":"0.2.0-draft","id":"p","title":"A pack"}`))
	for _, answer := range []string{
		"Just prose about a pack.",
		"```json\n{\"proposal\":{\"document\":{\"id\":\"p\",\"title\":\"A pack\"}}}\n```",
		"```json\n{\"proposal\":{\"document\":{\"specVersion\":\"0.2.0-draft\",\"id\":\"p\"}}}\n```",
		"```json\n{\"proposal\":{\"document\":{\"specVersion\":1,\"id\":\"p\",\"title\":\"A pack\"}}}\n```",
	} {
		if RecoverableProposal(answer) != nil {
			t.Errorf("%q is not a recoverable pack proposal", answer)
		}
	}
}

// budget/state-placement.json: the first line of every text that carries the execution state, and
// the turns one session may take.
func TestStateHeaderVector(t *testing.T) {
	file := vector(t, "budget/state-placement.json")
	for at, item := range file.Items {
		if held := value(item).get("header"); held != nil {
			text(t, "the state header of item "+jsNumber(float64(at)), held.s, StateHeader)
		}
	}
	// Eight is the whole scripted scenario; twenty bounds a loop whose stopping condition is a
	// model's own decision to stop calling tools.
	if MaxTurns != 20 {
		t.Errorf("MaxTurns is 20, got %d", MaxTurns)
	}
}

// The text half of one tool answer, joined in the runtime's own order (textOf).
func TestTextOf(t *testing.T) {
	var result contract.ToolResultMCPToolResult
	if err := json.Unmarshal([]byte(`{"content":[{"type":"text","text":"first"},`+
		`{"kind":"image","mediaType":"image/png","data":"AA=="},{"type":"text","text":"second"},{"type":"text"}]}`), &result); err != nil {
		t.Fatal(err)
	}
	text(t, "the text half, in order", TextOf(result), "first\nsecond\n")
	if TextOf(contract.ToolResultMCPToolResult{}) != "" {
		t.Error("an answer with no content has no text")
	}
}
