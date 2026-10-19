// Package gate is the host's tool gate in Go: the port of packages/harness/toolGate.ts and of the
// boundary rules around it (ADR-0001, ADR-0004), held to the contract's vectors as the TypeScript
// gate is.
//
// It is the host's, never an adapter's. A Go host puts the runtime's transport behind it
// (Transport), and an adapter reaches the runtime only through the gated call it is handed
// (Gate.CallTool), so an adapter that is wrong about a name, a phase or a member is stopped at
// the same place:
//
//   - the ceiling: the six Runtime tool names any session may be allowed (Ceiling); a session's
//     allow-list is what the host granted, intersected with it (Allowed);
//   - every outbound frame is read as JSON once, checked against the JSON-RPC message shapes the
//     MCP SDK accepts, and what leaves is the frame the gate wrote from what it read (Inspect);
//   - a tools/call whose name is not on the allow-list is refused before it leaves, with a
//     guardrail "refused" event; experimental_test_cases leaves only with pack and matrix, each a
//     string within its byte bound; experimental_evaluate always leaves with an own
//     rehearsal: true, with a guardrail "rewrote" event where the call did not carry it;
//   - each phase offers its own tools (the loop's, the critic's allow-list, Plan mode's reads, a
//     delegation profile's), and a name the phase did not offer is refused before any call
//     (Phase, Gate.CallTool, Gate.AdmitHost);
//   - one turn calls experimental_test_cases at most testCallsPerTurn times (Gate.CallTool);
//   - approvals are the host's state, read as approval.json states it: no call mints a grant
//     (approval.go).
package gate

import (
	"context"
	"encoding/json"
	"math"
	"strconv"
	"strings"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

// ToolsCall is the one method the gate has an opinion about.
const ToolsCall = "tools/call"

// RehearsalTool is the one tool whose arguments the gate rewrites, and RehearsalMember the member
// it writes.
const (
	RehearsalTool   = "experimental_evaluate"
	RehearsalMember = "rehearsal"
)

// TestTool is the Runtime's test tool the ceiling admits (agent#7), and TestArguments the members
// its arguments may carry: exactly the two its served schema names, each a string within its bound.
const TestTool = "experimental_test_cases"

// TestArguments are the members experimental_test_cases may carry.
var TestArguments = []string{"pack", "matrix"}

// Ceiling is the runtime tool ceiling (ASSISTANT_TOOLS in packages/harness/host.ts): the six tools
// an assistant may ever be allowed, every one a read. contract/vectors/tool-offer/runtime-ceiling.json.
var Ceiling = []string{
	"get_schema",
	"list_examples",
	"get_example",
	"validate",
	"experimental_evaluate",
	"experimental_test_cases",
}

// RefusedRuntimeTool is a Runtime tool kept off the ceiling, with the reason it was refused.
type RefusedRuntimeTool struct {
	Name   string
	Reason string
}

// RefusedRuntimeTools are the Runtime tools that stay off the ceiling (REFUSED_RUNTIME_TOOLS in
// toolGate.ts). Nothing here is a rule: the ceiling is a closed list, so these are refused by name
// as any other name off it is; each was proposed for it, and its reason is what a later proposal
// has to answer.
var RefusedRuntimeTools = []RefusedRuntimeTool{
	{"test_conformance", `its suite argument is "a local suite directory or manifest.json", a path the runtime reads on the machine ` +
		`that serves it: admitting it would give the model a file read, and the gate checks names, not paths`},
	{"experimental_test_packs", "it tests the saved project tree, not the draft the model is proposing, and saved suites are not held by the " +
		"rehearsal rule that holds experimental_evaluate"},
	{"experimental_test_graphs", "it tests the saved graphs, not the draft, under the same rule as experimental_test_packs"},
	{"get_pack", "it reads the saved project tree; the draft travels in the arguments of the tools that test it"},
	{"list_packs", "it lists the saved project tree, for the same reason as get_pack"},
}

// Bounds are the policy's bounds on the test tool (TEST_DEFAULTS in context/policy.ts).
type Bounds struct {
	TestCallsPerTurn int
	TestPackBytes    int
	TestMatrixBytes  int
}

// DefaultBounds are the policy's defaults: three test calls a turn, 65,536 bytes for each member.
var DefaultBounds = Bounds{TestCallsPerTurn: 3, TestPackBytes: 65_536, TestMatrixBytes: 65_536}

// Allowed is a session's allow-list: the names the host granted, intersected with the ceiling, in
// the order granted, each once. A name off the ceiling cannot be reached by writing a longer list.
func Allowed(configured []string) []string {
	out := []string{}
	for _, name := range configured {
		if contains(Ceiling, name) && !contains(out, name) {
			out = append(out, name)
		}
	}
	return out
}

func contains(list []string, name string) bool {
	for _, item := range list {
		if item == name {
			return true
		}
	}
	return false
}

// Notice is one thing the gate did: a guardrail event's tool, action and detail. The detail says
// what was there and never what the pack said.
type Notice struct {
	Tool   string
	Action string // rewrote or refused
	Detail string
}

// Event is the notice as the run event a host emits.
func (n Notice) Event() contract.RunEvent {
	return contract.RunEvent{Guardrail: &contract.RunEventGuardrail{Type: "guardrail", Tool: n.Tool, Action: n.Action, Detail: n.Detail}}
}

// Violation is a call the gate would not let out: the failure the model reads (permission, source
// tool, outcome not-started), carrying the tool by name.
type Violation struct {
	Tool   string
	Detail string
}

// Error is the violation's message, the failure's detail: "refused on the wire: " and the detail.
func (v *Violation) Error() string { return "refused on the wire: " + v.Detail }

// Failure is the violation as failure.json states it.
func (v *Violation) Failure() contract.Failure {
	message := v.Error()
	return contract.Failure{Version: 1, Code: "permission", Source: "tool", Outcome: "not-started", Detail: &message}
}

// Verdict is what Inspect decided about one frame. There is no pass: a frame that is sent is the
// frame the gate wrote from what it read, never the bytes it was handed.
type Verdict struct {
	Send      bool
	Notice    *Notice
	Frame     json.RawMessage
	Violation *Violation
}

const unreadable = "(a frame this gate could not read)"

func refuse(tool, detail string) Verdict {
	return Verdict{Notice: &Notice{Tool: tool, Action: "refused", Detail: detail}, Violation: &Violation{Tool: tool, Detail: detail}}
}

func send(frame *jvalue, notice *Notice) Verdict {
	return Verdict{Send: true, Notice: notice, Frame: frame.stringify()}
}

// Inspect decides what must happen to one outbound frame before it may leave (inspectOutboundFrame
// in toolGate.ts), in its order:
//
//  1. the frame is one JSON object (a batch is refused whole);
//  2. it is a JSON-RPC message the MCP SDK's own schema accepts: a request, a notification, a
//     response or an error, each whole (jsonrpc.go);
//  3. a method that is not tools/call but is a spelling of it to another reader is refused;
//  4. for tools/call: the name is on the allow-list; experimental_test_cases carries exactly pack
//     and matrix within the bounds; experimental_evaluate gets an own rehearsal: true, written last.
func Inspect(message []byte, allowed []string, bounds Bounds) Verdict {
	frame, err := parseJSON(message)
	if err != nil || frame.kind != jobject {
		if err == nil && frame.kind == jarray {
			return refuse(unreadable, "a JSON-RPC batch is not a frame this gate can check one call at a time, so it is "+
				"refused whole; nothing left the page")
		}
		return refuse(unreadable, "an outbound frame must be a single JSON-RPC object that survives serialization; "+
			"this one is not, and nothing left the page")
	}
	if !jsonrpcMessage(frame) {
		return refuse(unreadable, "an outbound frame must be a JSON-RPC message the SDK itself would accept — a request, "+
			"a notification, a response or an error, each whole; this one is not, and nothing left "+
			"the page")
	}
	method := frame.get("method")
	if method == nil {
		return send(frame, nil)
	}
	if method.kind != jstring {
		return refuse(unreadable, "an outbound frame's method must be a string")
	}
	if method.s != ToolsCall {
		if strings.ToLower(jsTrim(method.s)) == ToolsCall {
			return refuse(unreadable, quote(method.s)+" is a spelling of "+ToolsCall+" this desk will not send: a "+
				"frame two readers disagree about is one it cannot check")
		}
		if params := frame.get("params"); params != nil && params.kind != jobject {
			return refuse(unreadable, "a frame's params must be an object where it has any")
		}
		return send(frame, nil)
	}
	params := frame.get("params")
	if params == nil || params.kind != jobject {
		return refuse(unreadable, "a "+ToolsCall+" frame must carry a params object")
	}
	nameValue := params.get("name")
	if nameValue == nil || nameValue.kind != jstring || !contains(allowed, nameValue.s) {
		spelled := jsString(nameValue)
		list := strings.Join(allowed, ", ")
		if list == "" {
			list = "none"
		}
		return refuse(spelled, spelled+" is not one of the tools this assistant may call ("+list+"); the call did not leave "+
			"the page and nothing was written")
	}
	name := nameValue.s
	supplied := params.get("arguments")
	if supplied != nil && supplied.kind != jobject {
		return refuse(name, "a "+ToolsCall+" frame's arguments must be an object where it has any")
	}
	if name == TestTool {
		if problem := testArgumentsProblem(supplied, bounds); problem != "" {
			return refuse(name, problem+"; the call did not leave the page")
		}
	}
	if name != RehearsalTool {
		return send(frame, nil)
	}
	// The rehearsal member is written onto the data the gate read, last.
	args := supplied
	if args == nil {
		args = &jvalue{kind: jobject, o: []jmember{}}
	}
	held := args.get(RehearsalMember)
	already := held != nil && held.kind == jbool && held.b
	had := "the call carried no " + RehearsalMember + " member that survived serialization"
	if held != nil {
		had = "the call carried " + RehearsalMember + ": " + string(held.stringify())
	}
	args.set(RehearsalMember, &jvalue{kind: jbool, b: true})
	params.set("arguments", args)
	if already {
		return send(frame, nil)
	}
	return send(frame, &Notice{
		Tool:   name,
		Action: "rewrote",
		Detail: had + "; it was rewritten to an own " + RehearsalMember + ": true before the frame left " +
			"the page, so the runtime appends no audit record (runtime ADR-0018, ADR-0028)",
	})
}

// jsTrim is String.prototype.trim: it removes JavaScript's white space and line terminators.
func jsTrim(s string) string {
	return strings.TrimFunc(s, func(r rune) bool {
		switch r {
		case '\t', '\n', '\v', '\f', '\r', ' ', 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
			return true
		}
		return r >= 0x2000 && r <= 0x200a
	})
}

// testArgumentsProblem says why the test tool's arguments may not leave, or "" where they may. It
// names members and sizes, never a value; an invented member is named cut to sixty-four
// characters, at most five of them.
func testArgumentsProblem(supplied *jvalue, bounds Bounds) string {
	args := supplied
	if args == nil {
		args = &jvalue{kind: jobject}
	}
	var extra []string
	for _, member := range args.keys() {
		if !contains(TestArguments, member) {
			extra = append(extra, member)
		}
	}
	if len(extra) > 0 {
		named := make([]string, 0, 5)
		for _, member := range extra {
			if len(named) == 5 {
				break
			}
			named = append(named, quotePrefix(member, 64))
		}
		more := ""
		if len(extra) > 5 {
			more = " and more"
		}
		return TestTool + " takes exactly " + strings.Join(TestArguments, " and ") + ", and this call also carried " +
			strings.Join(named, ", ") + more + ": no path or other member reaches the runtime through this tool"
	}
	limits := map[string]struct {
		limit  int
		policy string
	}{"pack": {bounds.TestPackBytes, "testPackBytes"}, "matrix": {bounds.TestMatrixBytes, "testMatrixBytes"}}
	for _, member := range TestArguments {
		value := args.get(member)
		if value == nil || value.kind != jstring {
			state := "not a string"
			if value == nil {
				state = "missing"
			}
			return TestTool + " takes " + member + " as a string, and this call's " + member + " is " + state
		}
		bound := limits[member]
		if size := utf8Bytes(value.s); size > bound.limit {
			return TestTool + "'s " + member + " is " + strconv.Itoa(size) + " bytes, over the " + strconv.Itoa(bound.limit) +
				" the policy allows (" + bound.policy + ")"
		}
	}
	return ""
}

// Call sends one JSON-RPC message to the runtime and returns its reply, or nil for a
// notification: the runtime's transport.
type Call func(ctx context.Context, message json.RawMessage) (json.RawMessage, error)

// Transport puts a runtime transport behind the gate (gateTransport in toolGate.ts): every frame
// is inspected, a refusal is reported and returned as the *Violation without anything sent, and
// what is sent is the frame the gate wrote.
func Transport(inner Call, allowed []string, bounds Bounds, onGuardrail func(Notice)) Call {
	allowed = append([]string(nil), allowed...)
	return func(ctx context.Context, message json.RawMessage) (json.RawMessage, error) {
		decided := Inspect(message, allowed, bounds)
		if !decided.Send {
			if onGuardrail != nil {
				onGuardrail(*decided.Notice)
			}
			return nil, decided.Violation
		}
		if decided.Notice != nil && onGuardrail != nil {
			onGuardrail(*decided.Notice)
		}
		return inner(ctx, decided.Frame)
	}
}

// safeInteger reports a number Zod's int() accepts: integral and within ±(2^53 − 1).
func safeInteger(v *jvalue) bool {
	return v != nil && v.kind == jnumber && v.n == math.Trunc(v.n) && math.Abs(v.n) <= 9007199254740991
}
