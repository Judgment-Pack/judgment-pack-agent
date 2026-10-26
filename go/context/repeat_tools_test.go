package runcontext

import (
	"encoding/json"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
	"os"
	"strconv"
	"testing"
)

func repeat204State(stop int64) contract.CheckpointRepeatExecution {
	return contract.CheckpointRepeatExecution{Execution: "execution", RemindAt: 1, StopAt: stop, Seen: []contract.CheckpointRepeatObservation{}}
}
func TestRepeat204SharedHandwrittenVectors(t *testing.T) {
	raw, err := os.ReadFile("../../contract/testdata/repeat-tools/scenarios.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors struct {
		Items []struct {
			Name   string
			StopAt int64
			Calls  []struct {
				Source, Tool      string
				Args, Result      json.RawMessage
				Count             int64
				Reminder, Stopped bool
			}
		}
	}
	if err = json.Unmarshal(raw, &vectors); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range vectors.Items {
		t.Run(scenario.Name, func(t *testing.T) {
			state := repeat204State(scenario.StopAt)
			for i, input := range scenario.Calls {
				source, tool := input.Source, input.Tool
				if source == "" {
					source = "source"
				}
				if tool == "" {
					tool = "lookup"
				}
				row := DurableDispatch{ID: string(rune('a' + i)), Scope: DurableScope{Execution: "execution", Role: "author"}, Kind: "host", Source: source, Tool: tool, Request: string(input.Args), State: "result", Read: true, Result: input.Result}
				exact := string(input.Result)
				row.RepeatResultJson = &exact
				next, event, e := observeRepeat(state, row)
				if e != nil {
					t.Fatal(e)
				}
				state = next
				if state.Count != input.Count || state.Stopped != input.Stopped || (event != nil && event.Reminder) != input.Reminder {
					t.Fatalf("row%d count/reminder/stop: %+v event%+v", i, state, event)
				}
				duplicate, event, e := observeRepeat(state, row)
				if e != nil || event != nil || duplicate.Count != state.Count || len(duplicate.Seen) != len(state.Seen) {
					t.Fatal("dispatch ID counted twice")
				}
				held, _ := json.Marshal(contract.CheckpointRepeatTools{Executions: []contract.CheckpointRepeatExecution{state}})
				if !validRepeatTools(MustParseJSON(string(held))) {
					t.Fatal("valid shared state refused")
				}
			}
		})
	}
}
func TestRepeat204UnknownAndForeignScope(t *testing.T) {
	row := DurableDispatch{ID: "call", Scope: DurableScope{Execution: "other"}, Kind: "host", Request: "{}", State: "unknown"}
	if _, err := repeatKey(row); err == nil {
		t.Fatal("unknown result minted a digest")
	}
	if _, _, err := observeRepeat(repeat204State(5), row); err == nil {
		t.Fatal("foreign scope accepted")
	}
}
func TestRepeat204RecordSelectorsAndPolicy(t *testing.T) {
	event := contract.StampEvent(contract.RunEvent{RepeatDetected: &contract.RunEventRepeatDetected{Type: "repeat_detected", Execution: "execution", DispatchID: "dispatch", KeyDigest: "sha256:" + ContextDigest("fixture"), Count: 1, Reminder: true}})
	raw, _ := json.Marshal(event)
	if err := contract.CheckRecord("event", raw); err != nil {
		t.Fatal(err)
	}
	if event.RepeatDetected.Version == nil || *event.RepeatDetected.Version != 2 {
		t.Fatal("repeat event downstamped")
	}
	old := contract.StampEvent(contract.RunEvent{Message: &contract.RunEventMessage{Type: "message", Text: "Done."}})
	if old.Message.Version == nil || *old.Message.Version != 1 {
		t.Fatal("legacy event changed")
	}
	state := writerCheckpoint("version")
	state.Version = 3
	raw, _ = json.Marshal(state)
	written, err := WriteRecord(CheckpointSchema, MustParseJSON(string(raw)), RecordDefaults, nil, record204Time(), "")
	if err != nil || written.Get("version").Num() != 3 {
		t.Fatal("explicit current checkpoint downstamped", err)
	}
}
func TestRepeat204SharedRecordVersionsAndRefusals(t *testing.T) {
	raw, err := os.ReadFile("../../contract/testdata/repeat-tools/records.json")
	if err != nil {
		t.Fatal(err)
	}
	records := MustParseJSON(string(raw))
	for _, kind := range []string{"policy", "event", "checkpoint"} {
		items := []*Value{records.Get("checkpoint")}
		old := float64(2)
		if kind != "checkpoint" {
			name := "policies"
			old = 1
			if kind == "event" {
				name = "events"
			}
			items = records.Get(name).Items()
		}
		for _, value := range items {
			if err := contract.CheckRecord(kind, value.JSON()); err != nil {
				t.Fatal(kind, err)
			}
			if err := contract.CheckRecord(kind, value.Clone().Set("version", Number(old)).JSON()); err == nil {
				t.Fatal("historical version accepted repeat fields", kind)
			}
		}
	}
	if _, err := ReadExecution(records.Get("checkpoint")); err != nil {
		t.Fatal(err)
	}
	table := PolicyLayer{"models": map[string]any{"author": map[string]any{"model": "fixture", "reasoning": "off"}}}
	host := PolicyHost{Connections: []string{"fixture"}, Requests: 16}
	on, err := ResolvePolicy(PolicyLayer{"version": 2, "repeatTools": map[string]any{"mode": "on"}}, nil, table, host)
	if err != nil || repeatPolicy(on.Members).RemindAt != 1 || repeatPolicy(on.Members).StopAt != 5 {
		t.Fatal("host defaults", err)
	}
	for _, input := range []any{"", nil, map[string]any{"mode": "invalid"}, map[string]any{"remindAt": 0}, map[string]any{"stopAt": 401}, map[string]any{"remindAt": 3, "stopAt": 2}, map[string]any{"unknown": 1}} {
		_, err := ResolvePolicy(PolicyLayer{"version": 2, "repeatTools": input}, nil, table, host)
		refusal, ok := err.(interface{ Event() contract.RunEvent })
		if !ok {
			t.Fatal("invalid policy accepted", input, err)
		}
		raw, _ := json.Marshal(contract.StampEvent(refusal.Event()))
		if err := contract.CheckRecord("event", raw); err != nil {
			t.Fatal("invalid policy-refusal event", err)
		}
	}
	absent, err := ResolvePolicy(nil, nil, table, host)
	if err != nil {
		t.Fatal(err)
	}
	off, err := ResolvePolicy(PolicyLayer{"version": 2, "repeatTools": map[string]any{"mode": "off"}}, nil, table, host)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(absent.Members)
	b, _ := json.Marshal(off.Members)
	if string(a) != string(b) {
		t.Fatal("off resolved policy changed bytes")
	}
}
func TestRepeat204ExactResultAndLosslessArguments(t *testing.T) {
	raw, err := os.ReadFile("../../contract/testdata/repeat-tools/identities.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors struct {
		Cases []struct {
			Name, LeftArgs, RightArgs, LeftResult, RightResult, LeftStoredResult, RightStoredResult string
			Same                                                                                    bool
		}
	}
	if err = json.Unmarshal(raw, &vectors); err != nil {
		t.Fatal(err)
	}
	for _, item := range vectors.Cases {
		t.Run(item.Name, func(t *testing.T) {
			row := func(args, result, stored string) DurableDispatch {
				if stored == "" {
					stored = result
				}
				return DurableDispatch{ID: "physical", Scope: DurableScope{Execution: "execution", Role: "author"}, Kind: "host", Source: "source", Tool: "lookup", Request: args, State: "result", Read: true, Result: []byte(stored), RepeatResultJson: &result}
			}
			left, err := repeatKey(row(item.LeftArgs, item.LeftResult, item.LeftStoredResult))
			if err != nil {
				t.Fatal(err)
			}
			right, err := repeatKey(row(item.RightArgs, item.RightResult, item.RightStoredResult))
			if err != nil {
				t.Fatal(err)
			}
			if (left == right) != item.Same {
				t.Fatal("exact numeric custody identity changed")
			}
		})
	}
}
func TestRepeat204SharedExactCustodyRecord(t *testing.T) {
	raw, err := os.ReadFile("../../contract/testdata/repeat-tools/records.json")
	if err != nil {
		t.Fatal(err)
	}
	cp := MustParseJSON(string(raw)).Get("custodyCheckpoint")
	if _, err := ReadExecution(cp); err != nil {
		t.Fatal(err)
	}
	for _, change := range []string{"mismatch", "orphan", "unknown", "model"} {
		bad := cp.Clone()
		row := bad.Get("durable").Get("dispatches").Items()[0]
		switch change {
		case "mismatch":
			row.Set("repeatResultJson", String(`{"n":2}`))
		case "orphan":
			bad.Set("repeatTools", Null())
		case "unknown":
			row.Set("state", String("unknown"))
		case "model":
			row.Set("kind", String("model"))
		}
		if _, err := ReadExecution(bad); err == nil {
			t.Fatal("invalid exact custody accepted", change)
		}
	}
	historical := cp.Clone().Set("version", Number(2))
	if err := contract.CheckRecord("checkpoint", historical.JSON()); err == nil {
		t.Fatal("historical version admitted nested custody")
	}
}
func TestRepeat204CapacityAndDeliveredReminder(t *testing.T) {
	state := repeat204State(400)
	exact := "{}"
	for i := 0; i < 400; i++ {
		row := DurableDispatch{ID: strconv.Itoa(i), Scope: DurableScope{Execution: "execution"}, Kind: "host", Source: "source", Tool: "lookup", Request: strconv.Itoa(i), State: "result", Result: []byte(exact), RepeatResultJson: &exact}
		var err error
		state, _, err = observeRepeat(state, row)
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(state.Seen) != 400 || state.Seen[0].DispatchID != "0" {
		t.Fatal("physical observations evicted")
	}
	if _, _, err := observeRepeat(state, DurableDispatch{ID: "overflow", Scope: DurableScope{Execution: "execution"}, Kind: "host", Source: "source", Tool: "lookup", Request: "400", State: "result", Result: []byte(exact), RepeatResultJson: &exact}); err == nil {
		t.Fatal("physical observation capacity bypassed")
	}
	state.Reminded = true
	state.ReminderDelivered = true
	if repeatReminder(&state, "marker") != "" {
		t.Fatal("delivered reminder replayed")
	}
}

func TestRepeat204NamedFailureEnvelope(t *testing.T) {
	cause := &RepeatedToolLimit{}
	got := failure.Normalize(cause, "tool", record204Time())
	if got.Code != "repeated-work" || got.Source != "desk" || got.Outcome != "not-started" || got.Detail == nil || *got.Detail != cause.Error() {
		t.Fatal("named stop failure envelope changed", got)
	}
}
