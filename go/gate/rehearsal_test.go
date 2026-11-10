package gate

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

// #344: the gate admits experimental_evaluate, served with effect act, only as a rehearsal: in the
// critic's phase and a delegated child's that offers it, and only once its rewrite to an own
// rehearsal: true is confirmed on the frame. Every other acting call is refused with the same bytes.

const actingRefusal = "refused: Runtime action requires an exact approved attempt. Nothing was sent. This assistant proposes; it never writes a file and never calls a tool it was not offered."

type rehearsalWire struct {
	sent   []json.RawMessage
	events []contract.RunEvent
}

func rehearsalGate(t *testing.T) (*Gate, *rehearsalWire) {
	t.Helper()
	w := &rehearsalWire{}
	g := New(Options{Granted: Ceiling, Emit: func(e contract.RunEvent) { w.events = append(w.events, e) }, Runtime: func(_ context.Context, frame json.RawMessage) (json.RawMessage, error) {
		w.sent = append(w.sent, append(json.RawMessage(nil), frame...))
		var request struct {
			ID json.RawMessage `json:"id"`
		}
		if err := json.Unmarshal(frame, &request); err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"content": []any{map[string]any{"type": "text", "text": "evaluated"}}}})
	}})
	experimental := true
	g.SetOffer(contract.ToolOffer{Tools: []contract.ToolOfferRuntimeTool{{Name: RehearsalTool, InputSchema: json.RawMessage(`{"type":"object"}`), Meta: contract.ToolTaxonomy{Operation: "evaluate", Effect: "act", Experimental: &experimental}}}})
	if len(g.Offer().Tools) != 1 {
		t.Fatal("the acting evaluation was not offered")
	}
	return g, w
}

func (w *rehearsalWire) guardrails() []string {
	out := []string{}
	for _, e := range w.events {
		if e.Guardrail != nil {
			out = append(out, e.Guardrail.Action+": "+e.Guardrail.Detail)
		}
	}
	return out
}

func TestARehearsalIsAdmittedOnlyInTheCriticsPhaseAndAChildsThatOffersIt(t *testing.T) {
	authorTest := contract.DelegatedTaskProfile{ID: "author-test", RuntimeTools: []string{RehearsalTool}}
	review := contract.DelegatedTaskProfile{ID: "review", Capabilities: []string{"read"}}
	for _, row := range []struct {
		label    string
		enter    func(g *Gate)
		admitted bool
	}{
		{"the loop", func(*Gate) {}, false},
		{"the critic's phase", func(g *Gate) { g.Calls().BeginCritic() }, true},
		{"an author-test child", func(g *Gate) { g.Enter(ChildPhase(authorTest, g.Offer(), LoopPhase(g.Offer()))) }, true},
		{"a child whose profile does not offer it", func(g *Gate) { g.Enter(ChildPhase(review, g.Offer(), LoopPhase(g.Offer()))) }, false},
		{"Plan mode", func(g *Gate) { g.Enter(PlanPhase(g.Offer())) }, false},
		{"the reformat run", func(g *Gate) { g.Calls().BeginReformat() }, false},
	} {
		t.Run(row.label, func(t *testing.T) {
			g, w := rehearsalGate(t)
			row.enter(g)
			result, err := g.CallTool(context.Background(), RehearsalTool, json.RawMessage(`{"value":7,"rehearsal":false}`))
			if err != nil {
				t.Fatal(err)
			}
			if !row.admitted {
				if result.Text != actingRefusal || !result.IsError || len(w.sent) != 0 {
					t.Fatalf("not refused as an act: %q, %d frames sent", result.Text, len(w.sent))
				}
				if got := w.guardrails(); len(got) != 1 || got[0] != "refused: Runtime action requires an exact approved attempt. Nothing was sent." {
					t.Fatalf("guardrails %q", got)
				}
				return
			}
			if result.Text != "evaluated" || result.IsError || len(w.sent) != 1 {
				t.Fatalf("not sent as a rehearsal: %q, %d frames sent", result.Text, len(w.sent))
			}
			var frame struct {
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			if err := json.Unmarshal(w.sent[0], &frame); err != nil || frame.Method != ToolsCall || string(frame.Params) != `{"arguments":{"value":7,"rehearsal":true},"name":"experimental_evaluate"}` {
				t.Fatalf("the frame on the wire: %s", w.sent[0])
			}
			if got := w.guardrails(); len(got) != 1 || got[0] != "rewrote: the call carried rehearsal: false; it was rewritten to an own rehearsal: true before the frame left the page, so the runtime appends no audit record (runtime ADR-0018, ADR-0028)" {
				t.Fatalf("guardrails %q", got)
			}
		})
	}
}

func TestARehearsalWhoseRewriteFailsIsRefused(t *testing.T) {
	g, w := rehearsalGate(t)
	g.Calls().BeginCritic()
	result, err := g.CallTool(context.Background(), RehearsalTool, json.RawMessage(`[7]`))
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "refused on the wire: a tools/call frame's arguments must be an object where it has any" || !result.IsError || len(w.sent) != 0 {
		t.Fatalf("a rewrite the gate could not apply was not refused: %q, %d frames sent", result.Text, len(w.sent))
	}
	if got := w.guardrails(); len(got) != 1 || got[0] != "refused: a tools/call frame's arguments must be an object where it has any" {
		t.Fatalf("guardrails %q", got)
	}
}

func TestARehearsalWhoseRewriteIsNotConfirmedIsRefused(t *testing.T) {
	for label, frame := range map[string]string{
		"no rehearsal member":   `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"experimental_evaluate","arguments":{"value":7}}}`,
		"rehearsal false":       `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"experimental_evaluate","arguments":{"value":7,"rehearsal":false}}}`,
		"another tool's frame":  `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"validate","arguments":{"rehearsal":true}}}`,
		"rehearsal as a string": `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"experimental_evaluate","arguments":{"rehearsal":"true"}}}`,
	} {
		t.Run(label, func(t *testing.T) {
			g, w := rehearsalGate(t)
			g.inspect = func([]byte, []string, Bounds) Verdict { return Verdict{Send: true, Frame: json.RawMessage(frame)} }
			g.Calls().BeginCritic()
			result, err := g.CallTool(context.Background(), RehearsalTool, json.RawMessage(`{"value":7}`))
			if err != nil {
				t.Fatal(err)
			}
			if result.Text != actingRefusal || !result.IsError || len(w.sent) != 0 {
				t.Fatalf("an unconfirmed rewrite was sent: %q, %d frames sent", result.Text, len(w.sent))
			}
		})
	}
}

func TestRehearsedConfirmsTheRewriteOnTheFrame(t *testing.T) {
	inspected := Inspect([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"experimental_evaluate","arguments":{"value":7}}}`), Ceiling, DefaultBounds)
	if !inspected.Send || !Rehearsed(inspected) {
		t.Fatal("the gate's own rewrite is not confirmed")
	}
	for label, verdict := range map[string]Verdict{
		"a refusal":             refuse(RehearsalTool, "refused"),
		"an unreadable frame":   {Send: true, Frame: json.RawMessage(`{"jsonrpc"`)},
		"another method":        {Send: true, Frame: json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"name":"experimental_evaluate","arguments":{"rehearsal":true}}}`)},
		"no params":             {Send: true, Frame: json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/call"}`)},
		"arguments not object":  {Send: true, Frame: json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"experimental_evaluate","arguments":[true]}}`)},
		"rehearsal null":        {Send: true, Frame: json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"experimental_evaluate","arguments":{"rehearsal":null}}}`)},
		"a later false":         {Send: true, Frame: json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"experimental_evaluate","arguments":{"rehearsal":true,"rehearsal":false}}}`)},
		"refused with a frame":  {Send: false, Frame: json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"experimental_evaluate","arguments":{"rehearsal":true}}}`)},
		"a name not evaluation": {Send: true, Frame: json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"experimental_test_cases","arguments":{"rehearsal":true}}}`)},
	} {
		if Rehearsed(verdict) {
			t.Errorf("%s was confirmed as a rehearsal", label)
		}
	}
}
