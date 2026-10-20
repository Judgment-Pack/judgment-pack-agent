package gate

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

const vectorsDir = "../../contract/vectors"

// The vector families the gate is held to (agent#53): every file in them is run through the gate
// and compared with what it states, or is named in notTheGates with the reason it is not a gate
// decision, so a vector added to a family fails here until it is one or the other.
var families = []string{"tool-offer", "tool-call", "tool-result", "approval", "bounds"}

// The vectors outside those families the gate is held to as well.
var beyond = []string{"scenario/expense-reimbursement.json", "framing/injection-refused.json", "budget/harness-defaults.json"}

var notTheGates = map[string]string{
	"tool-offer/agent-wire-offer.json":  "the codex engine's aliases for the offer on the agent wire: the adapter's encoding of an offer the gate decided (scenario-offer.json)",
	"tool-offer/host-declarations.json": "host tool declarations: the session of phase-offers.json, held there",
	"tool-offer/propose-pack.json":      "propose_pack as a host declares it: the session of phase-offers.json, held there",
	"tool-call/host-presentation.json":  "a host-owned presentation the host consumer adds to a tool_call event; not a call the gate decides",
	"tool-call/text-call.json":          "a call made in text, which the adapter reads (extractToolCall) and then dispatches as a native call",
	"tool-result/attachments.json":      "documents a host hands a run to retain: the context ledger's",
	"tool-result/image-read.json":       "read_window's answer for an image: the context reader's",
	"tool-result/image-result.json":     "tool results that carry images: what a tool answered, after the gate",
	"tool-result/search-arguments.json": "arguments to search, a host context tool the boundary runs, not a Runtime call",
	"tool-result/search.json":           "search's answer: the context ledger's",
	"tool-result/text-result.json":      "the messages that carry a text call's result back: the adapter's encoding",
	"bounds/rate-limited-retried.json":  "the model request's retry: the adapter's, on the relay",
	"bounds/tool-timeout.json":          "the tool-call deadline: the adapter's dispatch",
	"bounds/turn-clock.json":            "the turn's wall clock: the adapter's loop",
	// ADR-0006: tools with a source are host tools, held by the boundary's source gate
	// (go/context, sources_test.go), not by the gate on the Runtime's wire.
	"tool-offer/source-declarations.json": "host tool declarations with a source: the boundary's (go/context, DeclareSources)",
	"tool-offer/source-offer.json":        "the offer of tools with a source: the boundary's (go/context, OfferSources)",
	"tool-offer/catalogue.json":           "catalogue entries: the boundary's schema budget (go/context, OfferSources)",
	"tool-offer/source-schema.json":       "the text inside a source's input schema, framed and bounded: the boundary's (go/context, OfferSources, DescribedSchema)",
	"tool-offer/load-tool.json":           "load_tool, a host tool the boundary adds (go/context, SourceGate.LoadToolHost)",
	"tool-offer/source-names.json":        "wire names of tools with a source: the boundary's (go/context, WireName)",
	"tool-offer/tool-list-digest.json":    "a source's pinned tool list: the boundary's (go/context, ToolListDigest)",
	"tool-call/source-calls.json":         "the source record on a tool_call event: the boundary's (go/context, SourceGate.Decorate)",
	"tool-result/loaded-schema.json":      "load_tool's answer: the boundary's (go/context, SourceGate.LoadToolHost)",
	"tool-result/source-results.json":     "the source record on a tool_result event: the boundary's (go/context, SourceGate.Decorate)",
	// ADR-0008: the approval pause is the boundary's and the source gate's (go/context,
	// approval_test.go; certified scenario 8), not a decision of the gate on the Runtime's wire.
	"approval/pause.json":                    "an act call paused for the person: the boundary's (go/context, SourceGate.Decide, Boundary)",
	"approval/second-act-while-pending.json": "an act call refused while another waits: the boundary's (go/context, SourceGate.Decide, Boundary)",
	"approval/continue-refused.json":         "the continuation of a refused act call: the boundary's (go/context, ContextualSession)",
	"approval/continue-approved.json":        "the continuation of an approved act call: the boundary's (go/context, ContextualSession, SourceGate.Approved)",
	"approval/wrong-call-id.json":            "a decision for another call, refused before the run: the execution's (go/context, ExecutionController.Initialize)",
}

type vectorFile struct {
	Schema   string            `json:"schema"`
	Items    []json.RawMessage `json:"items"`
	Allowed  []string          `json:"allowed"`
	Bounds   *Bounds           `json:"bounds"`
	Outcomes []outcome         `json:"outcomes"`
	Frames   []frameCase       `json:"frames"`
	Phases   []struct {
		Phase   PhaseKind `json:"phase"`
		Profile string    `json:"profile"`
		Within  PhaseKind `json:"within"`
	} `json:"phases"`
	Checks []approvalCheck `json:"checks"`
}

type outcome struct {
	What    string          `json:"what"`
	Allowed *[]string       `json:"allowed"`
	Bounds  *Bounds         `json:"bounds"`
	Verdict string          `json:"verdict"`
	Notice  *Notice         `json:"notice"`
	Sent    json.RawMessage `json:"sent"`
}

type frameCase struct {
	outcome
	Frame json.RawMessage `json:"frame"`
}

type approvalCheck struct {
	What     string                   `json:"what"`
	State    int                      `json:"state"`
	Now      string                   `json:"now"`
	Request  contract.ApprovalRequest `json:"request"`
	Resource Resource                 `json:"resource"`
	Mode     string                   `json:"mode"`
	Admitted bool                     `json:"admitted"`
	After    string                   `json:"after"`
	Refusal  string                   `json:"refusal"`
}

func (b *Bounds) UnmarshalJSON(data []byte) error {
	var raw struct {
		TestCallsPerTurn *int `json:"testCallsPerTurn"`
		TestPackBytes    int  `json:"testPackBytes"`
		TestMatrixBytes  int  `json:"testMatrixBytes"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*b = Bounds{TestCallsPerTurn: DefaultBounds.TestCallsPerTurn, TestPackBytes: raw.TestPackBytes, TestMatrixBytes: raw.TestMatrixBytes}
	if raw.TestCallsPerTurn != nil {
		b.TestCallsPerTurn = *raw.TestCallsPerTurn
	}
	return nil
}

func readVector(t *testing.T, name string) vectorFile {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(vectorsDir, name))
	if err != nil {
		t.Fatal(err)
	}
	var vector vectorFile
	if err := json.Unmarshal(raw, &vector); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return vector
}

func sameJSON(t *testing.T, a, b []byte) bool {
	t.Helper()
	var left, right any
	if err := json.Unmarshal(a, &left); err != nil {
		t.Fatalf("%v: %s", err, a)
	}
	if err := json.Unmarshal(b, &right); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	return reflect.DeepEqual(left, right)
}

func encode(t *testing.T, v any) []byte {
	t.Helper()
	out, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func strs(t *testing.T, items []json.RawMessage) []string {
	t.Helper()
	out := make([]string, len(items))
	for i, item := range items {
		if err := json.Unmarshal(item, &out[i]); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func offerOf(t *testing.T, raw json.RawMessage) contract.ToolOffer {
	t.Helper()
	var offer contract.ToolOffer
	if err := json.Unmarshal(raw, &offer); err != nil {
		t.Fatal(err)
	}
	return offer
}

// TestTheGateIsHeldToTheVectors runs every vector item of the gate's families through the gate and
// compares the outcome with what the vector states.
func TestTheGateIsHeldToTheVectors(t *testing.T) {
	counts := map[string]int{}
	held := map[string]func(t *testing.T, name string, vector vectorFile) int{
		"tool-offer/runtime-ceiling.json":     ceilingVector,
		"tool-offer/critic-tools.json":        criticToolsVector,
		"tool-offer/critic-offer.json":        criticOfferVector,
		"tool-offer/scenario-offer.json":      scenarioOfferVector,
		"tool-offer/runtime-tools.json":       runtimeToolsVector,
		"tool-offer/host-tools.json":          hostToolsVector,
		"tool-offer/phase-offers.json":        phaseOffersVector,
		"tool-call/gate-frames.json":          gateFramesVector,
		"tool-call/test-cases-arguments.json": testArgumentsVector,
		"approval/grant-states.json":          approvalVector,
		"bounds/test-call-cap.json":           replayVector(replay{granted: Ceiling, runtime: Ceiling}),
		"scenario/expense-reimbursement.json": scenarioReplay,
		"framing/injection-refused.json":      replayVector(replay{granted: Ceiling, runtime: Ceiling, host: []string{"read_page"}}),
		"budget/harness-defaults.json":        boundsVector,
	}
	var names []string
	for _, family := range families {
		entries, err := os.ReadDir(filepath.Join(vectorsDir, family))
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), ".json") {
				names = append(names, family+"/"+entry.Name())
			}
		}
	}
	names = append(names, beyond...)
	for _, name := range names {
		check, ok := held[name]
		if !ok {
			if _, stated := notTheGates[name]; !stated {
				t.Errorf("%s is in a family the gate is held to, and neither runs through the gate nor says why it is not a gate decision", name)
			}
			continue
		}
		t.Run(name, func(t *testing.T) {
			counts[strings.Split(name, "/")[0]] += check(t, name, readVector(t, name))
		})
	}
	var lines []string
	for family, count := range counts {
		lines = append(lines, fmt.Sprintf("%s %d", family, count))
	}
	sort.Strings(lines)
	t.Logf("items held per family: %s", strings.Join(lines, ", "))
}

func ceilingVector(t *testing.T, _ string, vector vectorFile) int {
	if got := strs(t, vector.Items); !reflect.DeepEqual(got, Ceiling) {
		t.Errorf("the ceiling is %v, the vector %v", Ceiling, got)
	}
	return len(vector.Items)
}

func criticToolsVector(t *testing.T, _ string, vector vectorFile) int {
	want := append(append([]string{}, CriticTools...), CriticReaders...)
	if got := strs(t, vector.Items); !reflect.DeepEqual(got, want) {
		t.Errorf("the critic's allow-list is %v, the vector %v", want, got)
	}
	return len(vector.Items)
}

func scenarioOffer(t *testing.T) contract.ToolOffer {
	return offerOf(t, readVector(t, "tool-offer/scenario-offer.json").Items[0])
}

func criticOfferVector(t *testing.T, _ string, vector vectorFile) int {
	session := scenarioOffer(t)
	got := CriticPhase(session).Offer(session)
	if !sameJSON(t, encode(t, got), vector.Items[0]) {
		t.Errorf("the critic is offered %s", encode(t, got))
	}
	return 1
}

func scenarioOfferVector(t *testing.T, _ string, vector vectorFile) int {
	session := offerOf(t, vector.Items[0])
	var served []string
	for _, tool := range session.Tools {
		served = append(served, tool.Name)
	}
	if got := Allowed(served); !reflect.DeepEqual(got, served) {
		t.Errorf("the scenario's runtime tools %v are not all within the ceiling: %v", served, got)
	}
	if got := LoopPhase(session).Offer(session); !sameJSON(t, encode(t, got), vector.Items[0]) {
		t.Errorf("the loop is offered %s", encode(t, got))
	}
	return 1
}

func runtimeToolsVector(t *testing.T, _ string, vector vectorFile) int {
	session := scenarioOffer(t)
	for i, item := range vector.Items {
		var tool contract.ToolOfferRuntimeTool
		if err := json.Unmarshal(item, &tool); err != nil {
			t.Fatal(err)
		}
		if !contains(Ceiling, tool.Name) {
			t.Errorf("item %d: %s is off the ceiling", i, tool.Name)
		}
		if !sameJSON(t, encode(t, session.Tools[i]), item) {
			t.Errorf("item %d is not the session's tool %d", i, i)
		}
	}
	return len(vector.Items)
}

func hostToolsVector(t *testing.T, _ string, vector vectorFile) int {
	session := scenarioOffer(t)
	critic := CriticPhase(session)
	for i, item := range vector.Items {
		var tool contract.ToolOfferHostTool
		if err := json.Unmarshal(item, &tool); err != nil {
			t.Fatal(err)
		}
		if !contains(ContextTools, tool.Name) {
			t.Errorf("item %d: %s is not one of the host's context tools", i, tool.Name)
		}
		if contains(critic.Host, tool.Name) != contains(CriticReaders, tool.Name) {
			t.Errorf("item %d: the critic's offer of %s is not its readers'", i, tool.Name)
		}
	}
	return len(vector.Items)
}

func phaseOffersVector(t *testing.T, _ string, vector vectorFile) int {
	session := offerOf(t, vector.Items[0])
	profiles := map[string]contract.DelegatedTaskProfile{}
	for _, raw := range readVector(t, "delegation/profiles.json").Items {
		var profile contract.DelegatedTaskProfile
		if err := json.Unmarshal(raw, &profile); err != nil {
			t.Fatal(err)
		}
		profiles[profile.ID] = profile
	}
	if len(vector.Phases) != len(vector.Items) {
		t.Fatalf("%d phases for %d items", len(vector.Phases), len(vector.Items))
	}
	for i, named := range vector.Phases {
		var phase Phase
		switch named.Phase {
		case PhaseLoop:
			phase = LoopPhase(session)
		case PhaseCritic:
			phase = CriticPhase(session)
		case PhasePlan:
			phase = PlanPhase(session)
		case PhaseReformat:
			phase = ReformatPhase()
		case PhaseChild:
			profile, ok := profiles[named.Profile]
			if !ok {
				t.Fatalf("item %d: no profile %q", i, named.Profile)
			}
			parent := LoopPhase(session)
			switch named.Within {
			case PhasePlan:
				parent = PlanPhase(session)
			case "", PhaseLoop:
			default:
				t.Fatalf("item %d: a child within %q", i, named.Within)
			}
			phase = ChildPhase(profile, session, parent)
		default:
			t.Fatalf("item %d: no phase %q", i, named.Phase)
		}
		if got := phase.Offer(session); !sameJSON(t, encode(t, got), vector.Items[i]) {
			t.Errorf("item %d (%s %s within %s): offered %v and %v", i, named.Phase, named.Profile, named.Within, phase.Runtime, phase.Host)
		}
	}
	return len(vector.Items)
}

// decided compares a verdict with an outcome.
func decided(t *testing.T, what string, verdict Verdict, want outcome, sentOf func(json.RawMessage) json.RawMessage) {
	t.Helper()
	switch want.Verdict {
	case "send":
		if !verdict.Send {
			t.Errorf("%s: refused (%s)", what, verdict.Violation.Detail)
			return
		}
		if got := sentOf(verdict.Frame); !sameJSON(t, got, want.Sent) {
			t.Errorf("%s: sent %s, not %s", what, got, want.Sent)
		}
	case "refused":
		if verdict.Send {
			t.Errorf("%s: sent %s", what, verdict.Frame)
			return
		}
		if verdict.Violation.Error() != "refused on the wire: "+want.Notice.Detail || verdict.Violation.Tool != want.Notice.Tool {
			t.Errorf("%s: the violation is %q for %q", what, verdict.Violation.Error(), verdict.Violation.Tool)
		}
	default:
		t.Fatalf("%s: verdict %q", what, want.Verdict)
	}
	if !reflect.DeepEqual(verdict.Notice, want.Notice) {
		t.Errorf("%s: the notice is %+v, not %+v", what, verdict.Notice, want.Notice)
	}
}

func gateFramesVector(t *testing.T, _ string, vector vectorFile) int {
	if len(vector.Outcomes) != len(vector.Items) {
		t.Fatalf("%d outcomes for %d items", len(vector.Outcomes), len(vector.Items))
	}
	params := func(frame json.RawMessage) json.RawMessage {
		var whole struct {
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(frame, &whole); err != nil {
			t.Fatal(err)
		}
		return whole.Params
	}
	for i, item := range vector.Items {
		want := vector.Outcomes[i]
		allowed, bounds := vector.Allowed, *vector.Bounds
		if want.Allowed != nil {
			allowed = *want.Allowed
		}
		if want.Bounds != nil {
			bounds = *want.Bounds
		}
		frame := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":` + string(item) + `}`)
		decided(t, fmt.Sprintf("item %d (%s)", i, want.What), Inspect(frame, allowed, bounds), want, params)
	}
	for i, frame := range vector.Frames {
		decided(t, fmt.Sprintf("frame %d (%s)", i, frame.What), Inspect(frame.Frame, vector.Allowed, *vector.Bounds), frame.outcome,
			func(sent json.RawMessage) json.RawMessage { return sent })
	}
	return len(vector.Items) + len(vector.Frames)
}

func testArgumentsVector(t *testing.T, _ string, vector vectorFile) int {
	for i, item := range vector.Items {
		frame := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"experimental_test_cases","arguments":` + string(item) + `}}`)
		verdict := Inspect(frame, Ceiling, DefaultBounds)
		if !verdict.Send || verdict.Notice != nil {
			t.Errorf("item %d did not leave as it was: %+v", i, verdict.Violation)
			continue
		}
		var sent struct {
			Params struct {
				Arguments json.RawMessage `json:"arguments"`
			} `json:"params"`
		}
		if err := json.Unmarshal(verdict.Frame, &sent); err != nil {
			t.Fatal(err)
		}
		if !sameJSON(t, sent.Params.Arguments, item) {
			t.Errorf("item %d left as %s", i, sent.Params.Arguments)
		}
	}
	return len(vector.Items)
}

func approvalVector(t *testing.T, _ string, vector vectorFile) int {
	states := make([]contract.Approval, len(vector.Items))
	for i, item := range vector.Items {
		if err := json.Unmarshal(item, &states[i]); err != nil {
			t.Fatal(err)
		}
	}
	for i, check := range vector.Checks {
		now, err := time.Parse(time.RFC3339, check.Now)
		if err != nil {
			t.Fatal(err)
		}
		state := states[check.State]
		if mode := ApprovalMode(state, now); mode != check.Mode {
			t.Errorf("check %d (%s): the mode is %s, not %s", i, check.What, mode, check.Mode)
		}
		after, err := Commit(state, check.Request, check.Resource, now)
		if (err == nil) != check.Admitted {
			t.Errorf("check %d (%s): admitted is %t (%v)", i, check.What, err == nil, err)
		}
		if err != nil && err.Error() != check.Refusal {
			t.Errorf("check %d (%s): refused with %q, not %q", i, check.What, err, check.Refusal)
		}
		if after != check.After {
			t.Errorf("check %d (%s): the grant is %q after, not %q", i, check.What, after, check.After)
		}
	}
	return len(vector.Items) + len(vector.Checks)
}

func boundsVector(t *testing.T, _ string, vector vectorFile) int {
	var policy struct {
		TestCallsPerTurn int `json:"testCallsPerTurn"`
		TestPackBytes    int `json:"testPackBytes"`
		TestMatrixBytes  int `json:"testMatrixBytes"`
	}
	if err := json.Unmarshal(vector.Items[0], &policy); err != nil {
		t.Fatal(err)
	}
	if got := (Bounds{policy.TestCallsPerTurn, policy.TestPackBytes, policy.TestMatrixBytes}); got != DefaultBounds {
		t.Errorf("the policy's test bounds are %+v, the gate's %+v", got, DefaultBounds)
	}
	return 1
}

// replay is a session a run-event vector is replayed in: what the host granted, the runtime tools
// the session offers, its host tools.
type replay struct {
	granted []string
	runtime []string
	host    []string
}

// replayVector runs a vector's calls through the gate in order: each tool_call is the model's,
// each tool_result after it the answer the runtime (or the host, for a host tool) gives where the
// call reaches it. The gate's events (guardrail) and the results it makes (refusals, the cap)
// must be the vector's, event for event, and nothing the gate refuses may reach the runtime.
func replayVector(session replay) func(t *testing.T, name string, vector vectorFile) int {
	return func(t *testing.T, name string, vector vectorFile) int {
		var items []map[string]json.RawMessage
		for _, raw := range vector.Items {
			var item map[string]json.RawMessage
			if err := json.Unmarshal(raw, &item); err != nil {
				t.Fatal(err)
			}
			items = append(items, item)
		}
		kind := func(item map[string]json.RawMessage) string {
			var k string
			_ = json.Unmarshal(item["type"], &k)
			return k
		}
		var produced []contract.RunEvent
		var reached []string
		var answer map[string]json.RawMessage
		runtime := func(_ context.Context, frame json.RawMessage) (json.RawMessage, error) {
			var call struct {
				ID     json.RawMessage `json:"id"`
				Params struct {
					Name      string                     `json:"name"`
					Arguments map[string]json.RawMessage `json:"arguments"`
				} `json:"params"`
			}
			if err := json.Unmarshal(frame, &call); err != nil {
				return nil, err
			}
			reached = append(reached, call.Params.Name)
			if call.Params.Name == RehearsalTool && string(call.Params.Arguments[RehearsalMember]) != "true" {
				t.Errorf("experimental_evaluate reached the runtime without rehearsal: true")
			}
			if answer == nil {
				t.Errorf("%s reached the runtime where the vector says nothing did", call.Params.Name)
				return json.RawMessage(`{"jsonrpc":"2.0","id":` + string(call.ID) + `,"error":{"code":-32601,"message":"not answered"}}`), nil
			}
			result := map[string]json.RawMessage{"content": encode(t, []map[string]json.RawMessage{{"type": json.RawMessage(`"text"`), "text": answer["text"]}})}
			if structured, ok := answer["structured"]; ok {
				result["structuredContent"] = structured
			}
			if answer["isError"] != nil {
				result["isError"] = answer["isError"]
			}
			return encode(t, map[string]any{"jsonrpc": "2.0", "id": call.ID, "result": result}), nil
		}
		g := New(Options{Runtime: runtime, Granted: session.granted, Emit: func(event contract.RunEvent) { produced = append(produced, event) }})
		var offer contract.ToolOffer
		for _, tool := range session.runtime {
			offer.Tools = append(offer.Tools, contract.ToolOfferRuntimeTool{Name: tool})
		}
		for _, tool := range session.host {
			offer.HostTools = append(offer.HostTools, contract.ToolOfferHostTool{Name: tool})
		}
		g.SetOffer(offer)
		want := []json.RawMessage{}
		calls := 0
		for i, item := range items {
			switch kind(item) {
			case "guardrail":
				want = append(want, vector.Items[i])
				continue
			case "tool_result":
				want = append(want, vector.Items[i])
				continue
			case "tool_call":
			default:
				continue
			}
			calls++
			want = append(want, vector.Items[i])
			var call contract.ToolCall
			if err := json.Unmarshal(vector.Items[i], &call); err != nil {
				t.Fatal(err)
			}
			produced = append(produced, contract.RunEvent{ToolCall: &call})
			// The answer the runtime gives where the call reaches it: the next tool_result. A result
			// the vector says was never run (its failure's outcome is not-started, or the model is told
			// the call was refused) is the gate's own, and then the runtime may not be reached at all.
			answer = nil
			for _, later := range items[i+1:] {
				if kind(later) == "tool_result" {
					answer = later
					break
				}
			}
			if answer != nil && notRun(answer) && !contains(session.host, call.Name) {
				answer = nil
			}
			var result Result
			if contains(session.host, call.Name) {
				if refused := g.AdmitHost(call.Name); refused != nil {
					result = *refused
				} else {
					var text string
					_ = json.Unmarshal(answer["text"], &text)
					result = Result{Text: text}
				}
			} else {
				reachedBefore := len(reached)
				var err error
				result, err = g.CallTool(context.Background(), call.Name, call.Args)
				if err != nil {
					t.Fatalf("%s: %v", call.Name, err)
				}
				if len(reached) == reachedBefore {
					answer = nil
				}
			}
			produced = append(produced, result.Event(call.Name))
		}
		got := []json.RawMessage{}
		for _, event := range produced {
			got = append(got, encode(t, event))
		}
		if !sameJSON(t, encode(t, got), encode(t, want)) {
			for i := range max(len(got), len(want)) {
				var g, w []byte
				if i < len(got) {
					g = got[i]
				}
				if i < len(want) {
					w = want[i]
				}
				if g == nil || w == nil || !sameJSON(t, g, w) {
					t.Errorf("%s event %d:\n gate:   %s\n vector: %s", name, i, clip(g), clip(w))
				}
			}
		}
		t.Logf("%s: %d calls, the runtime reached by %v", name, calls, reached)
		return calls
	}
}

// notRun reports a tool_result for a call that never reached the runtime: a failure whose outcome
// is not-started, or the refusal of a tool the model was not offered.
func notRun(result map[string]json.RawMessage) bool {
	var text string
	_ = json.Unmarshal(result["text"], &text)
	var structured struct {
		Failure *struct {
			Outcome string `json:"outcome"`
		} `json:"failure"`
	}
	_ = json.Unmarshal(result["structured"], &structured)
	return strings.HasPrefix(text, "refused: ") || (structured.Failure != nil && structured.Failure.Outcome == "not-started")
}

// scenarioReplay replays the recorded scenario in its own session: the five granted and served,
// the four context tools offered beside them.
func scenarioReplay(t *testing.T, name string, vector vectorFile) int {
	session := scenarioOffer(t)
	var runtime, host []string
	for _, tool := range session.Tools {
		runtime = append(runtime, tool.Name)
	}
	for _, tool := range session.HostTools {
		host = append(host, tool.Name)
	}
	return replayVector(replay{granted: runtime, runtime: runtime, host: host})(t, name, vector)
}

func clip(b []byte) string {
	if len(b) > 300 {
		return string(b[:300]) + "…"
	}
	return string(b)
}
