package durablewritetests

// Moved from go/adapter/eino/durable_writes_test.go unchanged: the durable write tests are two binaries, each
// under its time limit (#254). go/adapter/durablewiretests holds the rest.

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/eino"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
	"github.com/Judgment-Pack/judgment-pack-agent/go/relay"
)

func TestADurableWriteIsNeverAnsweredFromAnotherWritesRecord(t *testing.T) {
	kelvin := string(rune(0x212a))
	ran := func(call string) string { return "ran " + call }
	big, near := `{"id":9007199254740993}`, `{"id":9007199254740992}`
	for _, row := range []struct {
		name         string
		calls        []string
		ran, answers []string
	}{
		// Different numbers: each is its own write, a legitimate integer past 2^53 among them.
		{"an integer past 2^53, its neighbour first", []string{near, big}, []string{near, big}, []string{ran(near), ran(big)}},
		{"zero, then an underflow", []string{`{"q":0}`, `{"q":1e-400}`}, []string{`{"q":0}`, `{"q":1e-400}`}, []string{ran(`{"q":0}`), ran(`{"q":1e-400}`)}},
		{"a consumer's nested limit: zero, then an underflow its decoder must see", []string{`{"limits":{"searches":0}}`, `{"limits":{"searches":1e-400}}`}, []string{`{"limits":{"searches":0}}`, `{"limits":{"searches":1e-400}}`}, []string{ran(`{"limits":{"searches":0}}`), ran(`{"limits":{"searches":1e-400}}`)}},
		// What a text the parsed form does not say reads as: it may be a copy from the model's history, so it is
		// refused, and a write run for it would be a second write on another id.
		{"an integer past 2^53, then its neighbour", []string{big, near}, []string{big}, []string{ran(big), conflictRefusal}},
		{"an underflow, then zero", []string{`{"q":1e-400}`, `{"q":0}`}, []string{`{"q":1e-400}`}, []string{ran(`{"q":1e-400}`), conflictRefusal}},
		{"past a float64's precision, then what it reads as", []string{`{"id":7.0000000000000000001}`, `{"id":7}`}, []string{`{"id":7.0000000000000000001}`}, []string{ran(`{"id":7.0000000000000000001}`), conflictRefusal}},
		// The same value written differently: it may be that write and may not be. Neither run nor answered.
		{"1.0 then 1", []string{`{"q":1.0}`, `{"q":1}`}, []string{`{"q":1.0}`}, []string{ran(`{"q":1.0}`), conflictRefusal}},
		{"1 then 1.0", []string{`{"q":1}`, `{"q":1.0}`}, []string{`{"q":1}`}, []string{ran(`{"q":1}`), conflictRefusal}},
		{"-0 then 0", []string{`{"q":-0}`, `{"q":0}`}, []string{`{"q":-0}`}, []string{ran(`{"q":-0}`), conflictRefusal}},
		{"0 then -0", []string{`{"q":0}`, `{"q":-0}`}, []string{`{"q":0}`}, []string{ran(`{"q":0}`), conflictRefusal}},
		{"names that fold together", []string{`{"ID":1,"id":2}`, `{"id":2,"ID":1}`}, []string{`{"ID":1,"id":2}`}, []string{ran(`{"ID":1,"id":2}`), conflictRefusal}},
		{"names that fold together, the other order first", []string{`{"id":2,"ID":1}`, `{"ID":1,"id":2}`}, []string{`{"id":2,"ID":1}`}, []string{ran(`{"id":2,"ID":1}`), conflictRefusal}},
		{"the Kelvin sign and k", []string{`{"` + kelvin + `":1,"k":2}`, `{"k":2,"` + kelvin + `":1}`}, []string{`{"` + kelvin + `":1,"k":2}`}, []string{ran(`{"` + kelvin + `":1,"k":2}`), conflictRefusal}},
		// Not recordable as written: refused, and the run goes on. The next call is its own write.
		{"a repeated name, then the name it reads as", []string{`{"a":{"x":1},"a":{"y":2}}`, `{"a":{"y":2}}`}, []string{`{"a":{"y":2}}`}, []string{unrecordable, ran(`{"a":{"y":2}}`)}},
		{"a number out of range, twice", []string{`{"q":1e400}`, `{"q":2e400}`}, nil, []string{unrecordable, unrecordable}},
		// A true repeat is answered from its record, and not run.
		{"the same bytes", []string{`{"id":7}`, `{"id":7}`}, []string{`{"id":7}`}, []string{ran(`{"id":7}`), ran(`{"id":7}`)}},
		{"the same bytes, past 2^53", []string{big, big}, []string{big}, []string{ran(big), ran(big)}},
		{"the same bytes, a spelling JSON.stringify does not write", []string{`{"q":1.0}`, `{"q":1.0}`}, []string{`{"q":1.0}`}, []string{ran(`{"q":1.0}`), ran(`{"q":1.0}`)}},
		{"the same bytes, names that fold together", []string{`{"ID":1,"id":2}`, `{"ID":1,"id":2}`}, []string{`{"ID":1,"id":2}`}, []string{ran(`{"ID":1,"id":2}`), ran(`{"ID":1,"id":2}`)}},
		{"another order and whitespace, which the fingerprint leaves out", []string{`{"a":1,"b":2}`, ` { "b" : 2 , "a" : 1 } `}, []string{`{"a":1,"b":2}`}, []string{ran(`{"a":1,"b":2}`), ran(`{"a":1,"b":2}`)}},
		// Each row is looked at: the record that is this write is found past one that only reads alike.
		{"the first of two neighbours again", []string{near, big, near, big}, []string{near, big}, []string{ran(near), ran(big), ran(near), ran(big)}},
	} {
		wires := durableTableWires
		if durableFamilyRows[row.name] {
			wires = append(append([]string{}, durableTableWires...), durableFamilyWires...)
		}
		for _, engine := range wires {
			t.Run(engine+"/"+row.name, func(t *testing.T) {
				executed, answered, failures := durableRun(t, engine, true, row.calls)
				checkDurableRun(t, executed, answered, failures, row.ran, row.answers)
			})
		}
	}
}

// A read is never answered from a record: it runs each time it is asked. What cannot be recorded used
// to end the run here too; now the model reads the refusal and the run goes on.
func TestADurableReadIsRefusedOnlyWhereItCannotBeRecorded(t *testing.T) {
	ran := func(call string) string { return "ran " + call }
	for _, engine := range durableTableWires {
		t.Run(engine, func(t *testing.T) {
			calls := []string{`{"q":1.0}`, `{"q":1}`, `{"id":9007199254740993}`, `{"id":9007199254740992}`, `{"q":1e400}`, `{"a":1,"a":2}`, `{"id":7}`}
			executed, answered, failures := durableRun(t, engine, false, calls)
			checkDurableRun(t, executed, answered, failures, []string{calls[0], calls[1], calls[2], calls[3], calls[6]},
				[]string{ran(calls[0]), ran(calls[1]), ran(calls[2]), ran(calls[3]), unrecordable, unrecordable, ran(calls[6])})
		})
	}
}

// Under a token budget the next request is read too, so what the record cannot hold must not go back as written: the
// call is refused and the run goes on, on the family whose input carries it literally.
func TestARefusedCallDoesNotStopTheNextRequestUnderABudget(t *testing.T) {
	for _, call := range []string{`{"q":1e400}`, `{"a":1,"a":2}`, `{"n":1.0,"q":"` + string(rune(0x5c)) + `ud800"}`} {
		t.Run(call, func(t *testing.T) {
			store := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
			budget, err := runcontext.NewMemoryUsageBudgetStore("usage-project", runcontext.MemoryUsageBudgetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			g := gate.New(gate.Options{})
			g.SetOffer(contract.ToolOffer{})
			ran := 0
			tool := runcontext.HostTool{Name: "change_record", Description: "change_record: change one record", Effect: "workflow", InputSchema: json.RawMessage(`{"type":"object"}`), Taxonomy: &runcontext.ToolTaxonomy{Operation: "own-state", Effect: "pure"},
				Execute: func(context.Context, json.RawMessage) (runcontext.ToolResult, error) {
					ran++
					return runcontext.ResultOf([]runcontext.Content{runcontext.TextContent("ran")}, nil), nil
				}}
			errors := []string{}
			requests := 0
			r := &adapter.Run{Prompt: "Change it.", Wire: "anthropic", ModelName: "fixture", Thinking: "off", Critic: "off", AllowConversation: true, Interactive: true, Gate: g.Calls(), HostTools: []runcontext.HostTool{tool},
				Context: &runcontext.Input{CurrentRequest: &runcontext.CurrentRequest{Message: "Change it.", Input: "Change it."}, Connection: "provider", ConnectionRevisions: map[string]string{"provider": "one"}, RunStore: &runcontext.DurableRunInput{Store: store, ID: "budget"},
					UsageBudget: &runcontext.UsageBudgetInput{Store: budget, Scope: runcontext.UsageBudgetScope{Resource: "usage-project", ID: "shared-scope", Revision: "policy-r1", Limits: runcontext.UsageAmounts{"modelRequests": 8, "inputTokens": 1000, "outputTokens": 1000000}},
						Bounder: func(_ context.Context, request runcontext.UsageBoundRequest) (runcontext.UsageAmounts, error) {
							out := runcontext.UsageAmounts{"inputTokens": 10}
							if request.EncodedOutputMaximum != nil {
								out["outputTokens"] = *request.EncodedOutputMaximum
							}
							return out, nil
						}}},
				Emit: func(e contract.RunEvent) {
					if e.Error != nil {
						errors = append(errors, e.Error.Message)
					}
				},
				EnrollContext: func(_ context.Context, offer adapter.ContextOffer) error {
					return g.EnrollContext(offer.ID, offer.HostTools)
				},
				Model: func(context.Context, string, relay.Request) (*http.Response, error) {
					requests++
					if requests == 1 {
						return wireReply("api/anthropic/sse", "call-1", tool.Name, call), nil
					}
					return wireReply("api/anthropic/sse", "", "", ""), nil
				}}
			if err := eino.New().Run(context.Background(), r); err != nil {
				t.Fatal(err)
			}
			if len(errors) != 0 || ran != 0 || requests != 2 {
				t.Fatalf("errors %q, ran %d, requests %d", errors, ran, requests)
			}
		})
	}
}
