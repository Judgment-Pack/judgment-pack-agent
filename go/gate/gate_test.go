package gate

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

// The gate's lists are the schemas' enums, so a schema change that widens one fails here.
func TestTheListsAreTheSchemas(t *testing.T) {
	read := func(file string) map[string]any {
		raw, err := os.ReadFile("../../contract/schemas/" + file)
		if err != nil {
			t.Fatal(err)
		}
		var schema map[string]any
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatal(err)
		}
		return schema["$defs"].(map[string]any)
	}
	enum := func(defs map[string]any, name string) []string {
		var out []string
		for _, value := range defs[name].(map[string]any)["enum"].([]any) {
			out = append(out, value.(string))
		}
		return out
	}
	offer := read("tool-offer.json")
	for _, held := range []struct {
		name string
		have []string
		want []string
	}{
		{"runtimeCeiling", Ceiling, enum(offer, "runtimeCeiling")},
		{"criticTools", append(append([]string{}, CriticTools...), CriticReaders...), enum(offer, "criticTools")},
		{"reservedName", ContextTools, enum(offer, "reservedName")},
		{"action", ApprovalActions, enum(read("approval.json"), "action")},
	} {
		if !reflect.DeepEqual(held.have, held.want) {
			t.Errorf("%s: the gate holds %v, the schema %v", held.name, held.have, held.want)
		}
	}
	for _, refused := range RefusedRuntimeTools {
		if contains(Ceiling, refused.Name) {
			t.Errorf("%s is refused and on the ceiling", refused.Name)
		}
	}
}

func TestTheAllowListIsTheGrantWithinTheCeiling(t *testing.T) {
	if got := Allowed([]string{"validate", "write_file", "get_schema", "validate", "bash"}); !reflect.DeepEqual(got, []string{"validate", "get_schema"}) {
		t.Fatalf("allowed %v", got)
	}
	if got := Allowed(nil); len(got) != 0 {
		t.Fatalf("allowed %v from nothing", got)
	}
}

// What reaches the transport is the frame the gate wrote, and a refused frame never reaches it.
func TestTheTransportSendsOnlyWhatTheGateWrote(t *testing.T) {
	var wire []string
	var notices []Notice
	inner := func(_ context.Context, frame json.RawMessage) (json.RawMessage, error) {
		wire = append(wire, string(frame))
		return json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{}}`), nil
	}
	call := Transport(inner, Ceiling, DefaultBounds, func(n Notice) { notices = append(notices, n) })
	_, err := call(context.Background(), json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"write_file","arguments":{}}}`))
	var violation *Violation
	if !errors.As(err, &violation) || violation.Tool != "write_file" || len(wire) != 0 {
		t.Fatalf("write_file: %v, on the wire %v", err, wire)
	}
	if _, err := call(context.Background(), json.RawMessage(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"experimental_evaluate","arguments":{"pack":"{}" , "rehearsal":0}}}`)); err != nil {
		t.Fatal(err)
	}
	if len(wire) != 1 || wire[0] != `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"experimental_evaluate","arguments":{"pack":"{}","rehearsal":true}}}` {
		t.Fatalf("on the wire %v", wire)
	}
	if len(notices) != 2 || notices[0].Action != "refused" || notices[1].Action != "rewrote" || !strings.HasPrefix(notices[1].Detail, "the call carried rehearsal: 0;") {
		t.Fatalf("notices %+v", notices)
	}
	if failure := violation.Failure(); failure.Code != "permission" || failure.Outcome != "not-started" || *failure.Detail != violation.Error() {
		t.Fatalf("failure %+v", failure)
	}
}

// The texts the gate writes are JSON.stringify's and String's.
func TestTheTextsAreJavaScripts(t *testing.T) {
	for in, want := range map[float64]string{1: "1", 1.5: "1.5", -0.000001: "-0.000001", 1e-7: "1e-7", 1e21: "1e+21", 123456789012345680000: "123456789012345680000", 0.1: "0.1"} {
		if got := jsNumber(in); got != want {
			t.Errorf("jsNumber(%v) = %s, not %s", in, got, want)
		}
	}
	if got := quotePrefix(strings.Repeat("a", 63)+"\U0001F600", 64); got != `"`+strings.Repeat("a", 63)+`\ud83d"` {
		t.Errorf("a pair cut in half is %s", got)
	}
	if got := quote("a \x01\"<"); got != "\"a \\u0001\\\"<\"" {
		t.Errorf("quote: %s", got)
	}
	value, err := parseJSON([]byte(`{"b":1,"10":2,"a":3,"2":4,"b":5}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(value.stringify()); got != `{"2":4,"10":2,"b":5,"a":3}` {
		t.Errorf("an object enumerates as %s", got)
	}
	if v, _ := parseJSON([]byte(`1e400`)); string(v.stringify()) != "null" || jsString(v) != "Infinity" {
		t.Errorf("1e400 is %s", v.stringify())
	}
}

// The gated call refuses a name its phase did not offer before any frame leaves, and counts the
// test tool against the turn.
func TestTheGatedCallHoldsThePhase(t *testing.T) {
	var frames int
	var events []contract.RunEvent
	g := New(Options{
		Runtime: func(_ context.Context, frame json.RawMessage) (json.RawMessage, error) {
			frames++
			return json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"ok"}]}}`), nil
		},
		Granted: Ceiling,
		Bounds:  Bounds{TestCallsPerTurn: 1, TestPackBytes: 100, TestMatrixBytes: 100},
		Emit:    func(event contract.RunEvent) { events = append(events, event) },
	})
	offer := contract.ToolOffer{}
	for _, name := range Ceiling {
		offer.Tools = append(offer.Tools, contract.ToolOfferRuntimeTool{Name: name})
	}
	offer.HostTools = []contract.ToolOfferHostTool{{Name: "read_window"}, {Name: "propose_pack"}}
	g.SetOffer(offer)
	ctx := context.Background()
	test := json.RawMessage(`{"pack":"{}","matrix":"{}"}`)
	if result, _ := g.CallTool(ctx, TestTool, test); result.IsError || frames != 1 {
		t.Fatalf("the first test call: %+v", result)
	}
	if result, _ := g.CallTool(ctx, TestTool, test); !result.IsError || !strings.Contains(result.Text, "testCallsPerTurn") || frames != 1 {
		t.Fatalf("the second test call: %+v", result)
	}
	g.Enter(CriticPhase(offer))
	if result, _ := g.CallTool(ctx, "list_examples", nil); !result.IsError || frames != 1 {
		t.Fatalf("the critic called list_examples: %+v", result)
	}
	if refused := g.AdmitHost("propose_pack"); refused == nil || g.AdmitHost("read_window") != nil {
		t.Fatal("the critic's host tools are not its readers")
	}
	g.Enter(ReformatPhase())
	if result, _ := g.CallTool(ctx, "validate", nil); !result.IsError || frames != 1 {
		t.Fatalf("the reformat run called validate: %+v", result)
	}
	g.Enter(PlanPhase(offer))
	if result, _ := g.CallTool(ctx, RehearsalTool, nil); !result.IsError || frames != 1 {
		t.Fatalf("Plan mode evaluated: %+v", result)
	}
	var refusals []string
	for _, event := range events {
		if event.Guardrail != nil {
			refusals = append(refusals, event.Guardrail.Tool)
		}
	}
	if !reflect.DeepEqual(refusals, []string{"list_examples", "propose_pack", "validate"}) {
		t.Fatalf("guardrails for %v", refusals)
	}
	if g.TestCalls() != 1 {
		t.Fatalf("%d test calls counted", g.TestCalls())
	}
}
