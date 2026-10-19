package gate

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
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
	c := g.Calls()
	test := json.RawMessage(`{"pack":"{}","matrix":"{}"}`)
	if result, _ := c.CallTool(ctx, TestTool, test); result.IsError || frames != 1 {
		t.Fatalf("the first test call: %+v", result)
	}
	if result, _ := c.CallTool(ctx, TestTool, test); !result.IsError || !strings.Contains(result.Text, "testCallsPerTurn") || frames != 1 {
		t.Fatalf("the second test call: %+v", result)
	}
	c.BeginCritic()
	if result, _ := c.CallTool(ctx, "list_examples", nil); !result.IsError || frames != 1 {
		t.Fatalf("the critic called list_examples: %+v", result)
	}
	if refused := c.AdmitHost("propose_pack"); refused == nil || c.AdmitHost("read_window") != nil {
		t.Fatal("the critic's host tools are not its readers")
	}
	c.EndCritic()
	c.BeginReformat()
	if result, _ := c.CallTool(ctx, "validate", nil); !result.IsError || frames != 1 {
		t.Fatalf("the reformat run called validate: %+v", result)
	}
	c.EndReformat()
	g.Enter(PlanPhase(offer))
	if result, _ := c.CallTool(ctx, RehearsalTool, nil); !result.IsError || frames != 1 {
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

// A session whose runtime serves the scenario's five, counting what reaches it.
func fiveServed(t *testing.T, granted []string, bounds Bounds) (*Gate, *[]string, *[]contract.RunEvent) {
	t.Helper()
	var calls []string
	var events []contract.RunEvent
	runtime := func(_ context.Context, frame json.RawMessage) (json.RawMessage, error) {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Name string `json:"name"`
			} `json:"params"`
		}
		if err := json.Unmarshal(frame, &request); err != nil {
			return nil, err
		}
		switch request.Method {
		case "initialize":
			return json.RawMessage(`{"jsonrpc":"2.0","id":` + string(request.ID) + `,"result":{}}`), nil
		case "notifications/initialized":
			return nil, nil
		case "tools/list":
			return json.RawMessage(`{"jsonrpc":"2.0","id":` + string(request.ID) + `,"result":{"tools":[{"name":"get_schema"},{"name":"list_examples"},{"name":"get_example"},{"name":"validate"},{"name":"experimental_evaluate"}]}}`), nil
		}
		calls = append(calls, request.Params.Name)
		return json.RawMessage(`{"jsonrpc":"2.0","id":` + string(request.ID) + `,"result":{"content":[{"type":"text","text":"ok"}]}}`), nil
	}
	g := New(Options{Runtime: runtime, Granted: granted, Bounds: bounds, Emit: func(event contract.RunEvent) { events = append(events, event) }})
	if _, err := g.Open(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	return g, &calls, &events
}

// The loop refuses a name the host granted and the runtime did not serve before any call: the
// grant is the ceiling, tools/list serves five, and experimental_test_cases is refused with its
// guardrail, the runtime not reached.
func TestAGrantedNameTheLoopWasNotOfferedIsRefusedBeforeAnyCall(t *testing.T) {
	g, calls, events := fiveServed(t, Ceiling, DefaultBounds)
	result, err := g.Calls().CallTool(context.Background(), TestTool, json.RawMessage(`{"pack":"{}","matrix":"{}"}`))
	if err != nil || !result.IsError || result.Text != UnofferedRefusal(ServedNotOffered(TestTool)) {
		t.Fatalf("answered %+v (%v)", result, err)
	}
	if len(*calls) != 0 {
		t.Fatalf("the runtime was reached by %v", *calls)
	}
	if len(*events) != 1 || (*events)[0].Guardrail == nil || (*events)[0].Guardrail.Detail != ServedNotOffered(TestTool) {
		t.Fatalf("events %+v", *events)
	}
	if g.TestCalls() != 0 {
		t.Fatalf("%d test calls counted for a call never made", g.TestCalls())
	}
}

// Test calls made through the adapter's handle count, and past the cap they are not made.
func TestTheAdapterHandleCountsTestCalls(t *testing.T) {
	var reached int
	g := New(Options{
		Runtime: func(_ context.Context, frame json.RawMessage) (json.RawMessage, error) {
			reached++
			return json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"ok"}]}}`), nil
		},
		Granted: Ceiling,
	})
	offer := contract.ToolOffer{}
	for _, name := range Ceiling {
		offer.Tools = append(offer.Tools, contract.ToolOfferRuntimeTool{Name: name})
	}
	g.SetOffer(offer)
	c := g.Calls()
	var refused int
	for range 5 {
		result, err := c.CallTool(context.Background(), TestTool, json.RawMessage(`{"pack":"{}","matrix":"{}"}`))
		if err != nil {
			t.Fatal(err)
		}
		if result.IsError && strings.Contains(result.Text, "testCallsPerTurn") {
			refused++
		}
	}
	if reached != 3 || refused != 2 || g.TestCalls() != 3 {
		t.Fatalf("%d reached the runtime, %d refused, %d counted", reached, refused, g.TestCalls())
	}
}

// Plan mode, entered by the host, cannot be lifted from the adapter's handle: whatever it begins
// and ends, an evaluate is refused and the runtime is not reached.
func TestPlanModeCannotBeLiftedFromTheAdapterHandle(t *testing.T) {
	g, calls, _ := fiveServed(t, Ceiling, DefaultBounds)
	g.Enter(PlanPhase(g.Offer()))
	c := g.Calls()
	ctx := context.Background()
	for _, step := range []func(){c.BeginCritic, c.EndCritic, c.BeginReformat, c.EndReformat, c.EndCritic} {
		step()
		if result, _ := c.CallTool(ctx, RehearsalTool, json.RawMessage(`{"pack":"{}"}`)); !result.IsError {
			t.Fatalf("an evaluate under Plan mode was answered %+v", result)
		}
	}
	c.BeginCritic()
	if result, _ := c.CallTool(ctx, RehearsalTool, json.RawMessage(`{"pack":"{}"}`)); !result.IsError {
		t.Fatalf("the critic widened Plan mode: %+v", result)
	}
	if len(*calls) != 0 {
		t.Fatalf("the runtime was reached by %v", *calls)
	}
	if g.Phase().Kind != PhaseCritic || len(g.Phase().Runtime) != 2 {
		t.Fatalf("the critic under Plan mode holds %+v", g.Phase())
	}
}

// The adapter's handle is the gated call, a host tool's admission and the two narrowings, and
// nothing that sets an offer, enters a phase or reaches the transport.
func TestTheAdapterHandleHasOnlyItsMethods(t *testing.T) {
	var methods []string
	handle := reflect.TypeOf(&Calls{})
	for i := range handle.NumMethod() {
		methods = append(methods, handle.Method(i).Name)
	}
	want := []string{"AdmitHost", "BeginCritic", "BeginReformat", "CallTool", "EndCritic", "EndReformat"}
	if !reflect.DeepEqual(methods, want) {
		t.Fatalf("the adapter's handle has %v", methods)
	}
	if reflect.TypeOf(Calls{}).NumField() != 1 || reflect.TypeOf(Calls{}).Field(0).IsExported() {
		t.Fatal("the adapter's handle exposes a field")
	}
}

// An unoffered test call in Plan mode or a child's phase is counted before the phase refuses it, as
// the TypeScript boundary counts it (agent#56): the phase's refusal while the turn has calls left,
// the cap's failure past it, and the runtime reached by neither.
func TestPlanModeAndAChildCountAnUnofferedTestCall(t *testing.T) {
	for _, child := range []bool{false, true} {
		g, calls, _ := fiveServed(t, Ceiling, Bounds{TestCallsPerTurn: 1, TestPackBytes: 100, TestMatrixBytes: 100})
		if child {
			g.Enter(ChildPhase(contract.DelegatedTaskProfile{ID: "review"}, g.Offer(), LoopPhase(g.Offer())))
		} else {
			g.Enter(PlanPhase(g.Offer()))
		}
		test := json.RawMessage(`{"pack":"{}","matrix":"{}"}`)
		first, _ := g.Calls().CallTool(context.Background(), TestTool, test)
		second, _ := g.Calls().CallTool(context.Background(), TestTool, test)
		refusal := planRefusal
		if child {
			refusal = childRefusal
		}
		if first.Text != UnofferedRefusal(refusal) || !strings.Contains(second.Text, "testCallsPerTurn") || g.TestCalls() != 1 || len(*calls) != 0 {
			t.Fatalf("child %t: %q then %q, %d counted, %v reached", child, first.Text, second.Text, g.TestCalls(), *calls)
		}
	}
}

// A delegated child's test calls are charged to its parent's turn (Options.Turn, the gate the
// host's Children capability opens a child on): a parent that made one call and two author-test
// children that each try two reach the runtime three times in all, testCallsPerTurn, and the
// fourth and fifth are refused with the cap's failure.
func TestAChildsTestCallsAreChargedToItsParentsTurn(t *testing.T) {
	var mu sync.Mutex
	reached := 0
	runtime := func(_ context.Context, frame json.RawMessage) (json.RawMessage, error) {
		mu.Lock()
		reached++
		mu.Unlock()
		return json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"ok"}]}}`), nil
	}
	offer := contract.ToolOffer{}
	for _, name := range Ceiling {
		offer.Tools = append(offer.Tools, contract.ToolOfferRuntimeTool{Name: name})
	}
	parent := New(Options{Runtime: runtime, Granted: Ceiling})
	parent.SetOffer(offer)
	test := json.RawMessage(`{"pack":"{}","matrix":"{}"}`)
	if result, err := parent.Calls().CallTool(context.Background(), TestTool, test); err != nil || result.IsError {
		t.Fatalf("the parent's call: %+v (%v)", result, err)
	}
	profile := contract.DelegatedTaskProfile{ID: "author-test", RuntimeTools: []string{TestTool}}
	var wg sync.WaitGroup
	var refused atomic.Int32
	for range 2 {
		child := New(Options{Runtime: runtime, Granted: Ceiling, Turn: parent})
		child.SetOffer(offer)
		child.Enter(ChildPhase(profile, offer, parent.Phase()))
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 2 {
				result, err := child.Calls().CallTool(context.Background(), TestTool, test)
				if err != nil {
					t.Error(err)
				}
				if result.IsError && strings.Contains(result.Text, "testCallsPerTurn") {
					refused.Add(1)
				}
			}
		}()
	}
	wg.Wait()
	if reached != 3 || refused.Load() != 2 || parent.TestCalls() != 3 {
		t.Fatalf("%d reached the runtime, %d refused, the parent's turn counts %d", reached, refused.Load(), parent.TestCalls())
	}
}

// A Runtime result's image part is named, not shown, and none is dropped in silence: the call's
// text keeps each text part and a line per other part, in the result's order.
func TestARuntimeResultsImageIsNamedNotDropped(t *testing.T) {
	g := New(Options{
		Runtime: func(_ context.Context, frame json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"before"},` +
				`{"type":"image","data":"iVBORw0KGgo=","mimeType":"image/png"},{"type":"text","text":"after"},` +
				`{"type":"image","data":"/9j/","mimeType":"image/jpeg"},{"type":"audio","data":"AAAA","mimeType":"audio/wav"}]}}`), nil
		},
		Granted: Ceiling,
	})
	offer := contract.ToolOffer{}
	for _, name := range Ceiling {
		offer.Tools = append(offer.Tools, contract.ToolOfferRuntimeTool{Name: name})
	}
	g.SetOffer(offer)
	result, err := g.Calls().CallTool(context.Background(), "get_schema", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"before",
		"[image 1 (image/png): not shown, because " + RuntimePartNotShown + "]",
		"after",
		"[image 2 (image/jpeg): not shown, because " + RuntimePartNotShown + "]",
		"[audio 1 (audio/wav): not shown, because " + RuntimePartNotShown + "]",
	}, "\n")
	if result.Text != want {
		t.Fatalf("answered %q", result.Text)
	}
}
