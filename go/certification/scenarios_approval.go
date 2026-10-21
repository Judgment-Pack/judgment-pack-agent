package certification

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
)

// Scenario 8 (ADR-0008, certification/scenarios/approval-pause.json): the approval pause on the
// scripted MCP server of scenario 7. The host's policy answers ask for create_issue and allow for
// every other act. The pausing run loads create_issue's schema and makes two create_issue calls in
// one answer: the first pauses the run, the second, made while the first waits, is refused with
// the guardrail. The paused checkpoint is then continued in fresh runs read back from its JSON
// text: with a decision for another call, refused before anything runs; refused, with the person's
// note; approved, broken off once the call's result was written and acknowledged; and once more from
// that checkpoint, which runs the continuation and not the call. The host's half (the server, its
// policy, the durable callback) is this runner's, as it is scenarios.test.ts's.

type pauseFile struct {
	Request string `json:"request"`
	Asked   struct {
		Tool      string          `json:"tool"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"asked"`
	Pausing     []sourcesTurn `json:"pausing"`
	Continued   []sourcesTurn `json:"continued"`
	Note        string        `json:"note"`
	WrongCallID string        `json:"wrongCallId"`
}

// pauseServer is scenario 7's scripted MCP server as one external system across every run of the
// scenario: the calls it ran, what the policy was asked, and the order the host's acknowledged
// writes and the server's runs happened in. choreograph holds the agent capability's two calls made
// at once to the order the model engines make them in: the first call's served-list check (by its
// call id, the step's first) waits until the second has reached its own, and the second's waits
// until the policy has answered the first, each for at most two seconds.
type pauseServer struct {
	mu       sync.Mutex
	executed int
	asked    []runcontext.ApprovalRequest
	log      []string

	choreograph bool
	digests     atomic.Int32
	second      chan struct{}
	first       chan struct{}
	once        sync.Once
	secondOnce  sync.Once
}

func newPauseServer(choreograph bool) *pauseServer {
	return &pauseServer{choreograph: choreograph, second: make(chan struct{}), first: make(chan struct{})}
}

func (s *pauseServer) note(line string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.log = append(s.log, line)
}

func (s *pauseServer) ran() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.executed
}

// pauseTools are the host's declarations for one run, fresh each time: scenario 7's server, pinned
// to its digest, its policy asking about create_issue and allowing every other act.
func pauseTools(file sourcesFile, asked string, server *pauseServer) []runcontext.HostTool {
	var out []runcontext.HostTool
	for _, listed := range file.Server {
		held := listed
		out = append(out, runcontext.HostTool{
			Name: held.Name, Description: held.Description, InputSchema: held.InputSchema,
			Source: &runcontext.ToolSource{Kind: "mcp", ID: "tracker", Trust: "public", ToolListDigest: file.Tracker.Pinned,
				CurrentDigest: func(ctx context.Context) (string, error) {
					if server.choreograph && held.Name == asked && server.digests.Add(1) <= 2 {
						if strings.HasSuffix(runcontext.CallIDOf(ctx), "-1") {
							select {
							case <-server.second:
							case <-time.After(2 * time.Second):
							}
						} else {
							server.secondOnce.Do(func() { close(server.second) })
							select {
							case <-server.first:
							case <-time.After(2 * time.Second):
							}
						}
					}
					return file.Tracker.Pinned, nil
				}},
			Approve: func(call runcontext.ApprovalRequest) string {
				server.mu.Lock()
				server.asked = append(server.asked, call)
				server.mu.Unlock()
				if held.Name != asked {
					return "allow"
				}
				server.once.Do(func() { close(server.first) })
				return "ask"
			},
			Execute: func(context.Context, json.RawMessage) (runcontext.ToolResult, error) {
				server.mu.Lock()
				server.executed++
				server.log = append(server.log, "execute")
				server.mu.Unlock()
				return runcontext.ResultOf([]runcontext.Content{runcontext.TextContent(file.Issue)}, nil), nil
			},
		})
	}
	return out
}

// pauseRun is one run of scenario 8: its events, and what the model or the agent was sent: each
// request's first user message on a model capability, each invocation's prompt on an agent's.
type pauseRun struct {
	Events []contract.RunEvent
	Sent   []string
	// Bodies are the model requests' whole bodies (a model capability), what each carried.
	Bodies []string
}

// pausePlay is one run of the scenario on one capability: the turns, fresh host tools, the input,
// and whether to break the run off once the call's result was written and acknowledged.
type pausePlay func(ctx context.Context, turns []sourcesTurn, tools []runcontext.HostTool, input *runcontext.Input) (*pauseRun, error)

// paused is what scenario 8 left on one capability.
type paused struct {
	pausing, wrong, refused, approved, restarted *pauseRun
	saved                                        *contract.Checkpoint
	approvedLog                                  []string
	counts                                       []int
	server                                       *pauseServer
	file                                         pauseFile
	sources                                      sourcesFile
}

// readBack is a checkpoint as a fresh process reads it back from its JSON text.
func readBack(checkpoint *contract.Checkpoint) (*contract.Checkpoint, error) {
	if checkpoint == nil {
		return nil, errors.New("no checkpoint was written")
	}
	raw, err := json.Marshal(checkpoint)
	if err != nil {
		return nil, err
	}
	var out contract.Checkpoint
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// writtenAt is the last durable checkpoint whose approval is in state, or nil.
func writtenAt(events []contract.RunEvent, state string) *contract.Checkpoint {
	var out *contract.Checkpoint
	for _, event := range events {
		if held := event.ExecutionCheckpoint; held != nil && held.Durable != nil && *held.Durable && held.Checkpoint.Approval != nil && held.Checkpoint.Approval.State == state {
			checkpoint := held.Checkpoint
			out = &checkpoint
		}
	}
	return out
}

// lastCheckpointOf is the last checkpoint a run carried, or nil.
func lastCheckpointOf(events []contract.RunEvent) *contract.Checkpoint {
	var out *contract.Checkpoint
	for _, event := range events {
		if event.ExecutionCheckpoint != nil {
			held := event.ExecutionCheckpoint.Checkpoint
			out = &held
		}
	}
	return out
}

// approvalScenario plays scenario 8 with play: the pausing run, then each continued run from the
// paused checkpoint's JSON text, the server's runs counted after each.
func approvalScenario(ctx context.Context, play pausePlay, choreograph bool) (*paused, error) {
	out := &paused{server: newPauseServer(choreograph)}
	if err := ScenarioFile("approval-pause", &out.file); err != nil {
		return nil, err
	}
	if err := ScenarioFile("tool-sources", &out.sources); err != nil {
		return nil, err
	}
	request := func() *runcontext.Input { return requestInput(out.file.Request) }
	tools := func() []runcontext.HostTool { return pauseTools(out.sources, out.file.Asked.Tool, out.server) }
	var err error
	if out.pausing, err = play(ctx, out.file.Pausing, tools(), request()); err != nil {
		return nil, err
	}
	out.server.choreograph = false
	out.counts = append(out.counts, out.server.ran())
	var last *contract.Checkpoint
	for _, event := range out.pausing.Events {
		if event.ExecutionCheckpoint != nil && event.ExecutionCheckpoint.Checkpoint.Status == "paused" {
			held := event.ExecutionCheckpoint.Checkpoint
			last = &held
		}
	}
	if out.saved, err = readBack(last); err != nil {
		return nil, errors.New("the pausing run wrote no paused checkpoint")
	}
	callID := "none"
	if out.saved.Approval != nil {
		callID = out.saved.Approval.CallID
	}
	continued := func(decision *contract.CheckpointApprovalDecision, from *contract.Checkpoint, save func(context.Context, contract.Checkpoint) error) (*pauseRun, error) {
		execution, err := readBack(from)
		if err != nil {
			return nil, err
		}
		input := request()
		input.Execution = execution
		input.Approval = decision
		input.Checkpoint = save
		return play(ctx, out.file.Continued, tools(), input)
	}
	if out.wrong, err = continued(&contract.CheckpointApprovalDecision{CallID: out.file.WrongCallID, Approved: true}, out.saved, nil); err != nil {
		return nil, err
	}
	out.counts = append(out.counts, out.server.ran())
	note := out.file.Note
	if out.refused, err = continued(&contract.CheckpointApprovalDecision{CallID: callID, Approved: false, Note: &note}, out.saved, nil); err != nil {
		return nil, err
	}
	out.counts = append(out.counts, out.server.ran())
	// The approved run, broken off once the call's result was written: the host acknowledges the
	// writes up to it, and nothing after.
	out.server.mu.Lock()
	out.server.log = nil
	out.server.mu.Unlock()
	var stopped atomic.Bool
	var written *contract.Checkpoint
	breakOff, cancel := context.WithCancel(ctx)
	defer cancel()
	save := func(_ context.Context, state contract.Checkpoint) error {
		if stopped.Load() {
			return context.Canceled
		}
		approval := "none"
		if state.Approval != nil {
			approval = state.Approval.State
		}
		out.server.note("acknowledged " + approval)
		if approval == "executed" {
			held := state
			written = &held
			stopped.Store(true)
			cancel()
		}
		return nil
	}
	execution, err := readBack(out.saved)
	if err != nil {
		return nil, err
	}
	input := request()
	input.Execution = execution
	input.Approval = &contract.CheckpointApprovalDecision{CallID: callID, Approved: true}
	input.Checkpoint = save
	if out.approved, err = play(breakOff, out.file.Continued, tools(), input); err != nil {
		return nil, err
	}
	out.server.mu.Lock()
	out.approvedLog = append([]string(nil), out.server.log...)
	out.server.mu.Unlock()
	out.counts = append(out.counts, out.server.ran())
	if written == nil {
		return out, nil
	}
	// The restart continues the checkpoint as it stood; the decision was already taken, so none is handed again.
	if out.restarted, err = continued(nil, written, nil); err != nil {
		return nil, err
	}
	out.counts = append(out.counts, out.server.ran())
	return out, nil
}

var continuationClose = regexp.MustCompile(`\nEND RETRIEVED DATA [0-9a-f]{32}`)
var continuationOpen = regexp.MustCompile(`^BEGIN RETRIEVED DATA ([0-9a-f]{32}) from tool "tracker__create_issue"; `)

// continuationIn is the text a continued run's first request or invocation carried from the
// continuation's line through its block, or "".
func continuationIn(run *pauseRun) string {
	if run == nil || len(run.Sent) == 0 {
		return ""
	}
	whole := run.Sent[0]
	at := strings.Index(whole, runcontext.ApprovalContinuation)
	if at < 0 {
		return ""
	}
	rest := whole[at:]
	if loc := continuationClose.FindStringIndex(rest); loc != nil {
		return rest[:loc[1]]
	}
	return rest
}

// approvalData is the data of a continuation's one block, and its marker, or false.
func approvalData(text string) (map[string]any, string, bool) {
	line, rest, ok := strings.Cut(text, "\n")
	if !ok || line != runcontext.ApprovalContinuation {
		return nil, "", false
	}
	opening, body, ok := strings.Cut(rest, "\n")
	m := continuationOpen.FindStringSubmatch(opening)
	if !ok || m == nil || !strings.HasSuffix(body, "\nEND RETRIEVED DATA "+m[1]) {
		return nil, "", false
	}
	var data map[string]any
	if json.Unmarshal([]byte(strings.TrimSuffix(body, "\nEND RETRIEVED DATA "+m[1])), &data) != nil {
		return nil, "", false
	}
	return data, m[1], true
}

// resultText is the text of a continuation's result, as its data carries it.
func resultText(data map[string]any) string {
	result, _ := data["result"].(map[string]any)
	content, _ := result["content"].([]any)
	if len(content) == 0 {
		return ""
	}
	part, _ := content[0].(map[string]any)
	text, _ := part["text"].(string)
	return text
}

// approvalVector is one vector file under contract/vectors/approval, by member.
func approvalVector(name string) (map[string]json.RawMessage, error) {
	root, err := RepositoryRoot()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(filepath.Join(root, "contract", "vectors", "approval", name))
	if err != nil {
		return nil, err
	}
	var vector map[string]json.RawMessage
	if err := json.Unmarshal(raw, &vector); err != nil {
		return nil, err
	}
	return vector, nil
}

// withCallID is a vector item with its callId the run's own (each run draws its own).
func withCallID(item json.RawMessage, callID string) []byte {
	var value map[string]any
	if json.Unmarshal(item, &value) != nil {
		return nil
	}
	value["callId"] = callID
	out, _ := json.Marshal(value)
	return out
}

// drawnMarker is the shape of the framing marker a run draws: 128 random bits, as hex.
var drawnMarker = regexp.MustCompile(`^[0-9a-f]{32}$`)

// withMarker is a vector's approval with its marker the run's own (each run draws its own); the
// run's must be a marker, or the item is left as the vector has it and the comparison fails.
func withMarker(item []byte, marker *string) []byte {
	var value map[string]any
	if marker == nil || !drawnMarker.MatchString(*marker) || json.Unmarshal(item, &value) != nil {
		return item
	}
	value["marker"] = *marker
	out, _ := json.Marshal(value)
	return out
}

// markerOf is the framing marker an approval carries, or nil.
func markerOf(approval *contract.CheckpointApproval) *string {
	if approval == nil {
		return nil
	}
	return approval.Marker
}

// legacyIdentityJSON projects the identity members out when a current event is compared with an
// older shared vector. The vector remains a valid minimum contract example; scenario 19 carries
// and tests the identity members themselves.
func legacyIdentityJSON(value any) []byte {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var projected any
	if err := json.Unmarshal(raw, &projected); err != nil {
		return nil
	}
	var strip func(any)
	strip = func(value any) {
		switch held := value.(type) {
		case map[string]any:
			delete(held, "identity")
			delete(held, "identityDigest")
			for _, child := range held {
				strip(child)
			}
		case []any:
			for _, child := range held {
				strip(child)
			}
		}
	}
	strip(projected)
	out, _ := json.Marshal(projected)
	return out
}

// pauseAgainstVector compares the pausing run's last three events with approval/pause.json's items:
// the same approval_requested, the checkpoint durable and paused with the same approval, then end,
// the call id and the framing marker being the run's own; the checkpoint's other members (its id, operations, figures) are
// each run's. It answers what differs, or "".
func pauseAgainstVector(requested *contract.RunEventApprovalRequested, emitted *contract.RunEventExecutionCheckpoint, end contract.RunEvent) string {
	vector, err := approvalVector("pause.json")
	if err != nil {
		return err.Error()
	}
	var items []json.RawMessage
	if err := json.Unmarshal(vector["items"], &items); err != nil || len(items) != 3 {
		return "its items are not the three events"
	}
	var checkpoint struct {
		Durable    bool `json:"durable"`
		Checkpoint struct {
			Status   string          `json:"status"`
			Approval json.RawMessage `json:"approval"`
		} `json:"checkpoint"`
	}
	if err := json.Unmarshal(items[1], &checkpoint); err != nil {
		return err.Error()
	}
	switch {
	case !sameJSON(legacyIdentityJSON(requested), withCallID(items[0], requested.CallID)):
		return "approval_requested was " + comparisonJSON(requested)
	case emitted.Durable == nil || *emitted.Durable != checkpoint.Durable || emitted.Checkpoint.Status != checkpoint.Checkpoint.Status:
		return "the checkpoint was " + comparisonJSON(emitted)
	case !sameJSON(legacyIdentityJSON(emitted.Checkpoint.Approval), withMarker(withCallID(checkpoint.Checkpoint.Approval, requested.CallID), markerOf(emitted.Checkpoint.Approval))):
		return "the checkpoint's approval was " + comparisonJSON(emitted.Checkpoint.Approval)
	case !sameJSON([]byte(comparisonJSON(end)), items[2]):
		return "the last event was " + comparisonJSON(end)
	}
	return ""
}

// approvalPauseFindings are scenario 8's checks over what one capability left, by names prefixed
// with prefix ("8 " for a model capability, "8 agent " for an agent capability).
func approvalPauseFindings(state *paused, prefix string, agent bool) []Finding {
	var out []Finding
	record := `{"id":"tracker","identity":{"kind":"connection","label":"tracker"},"identityDigest":"sha256:626ce84087e5243c1da2172ee15a40305960e3d46e4555b0a3e00e144ec341e2","kind":"mcp","tool":"create_issue","toolListDigest":"` + state.sources.Tracker.Pinned + `","trust":"public"}`
	asked := state.file.Asked.Arguments
	count := func(at int) int {
		if at < len(state.counts) {
			return state.counts[at]
		}
		return -1
	}

	f := &findings{check: prefix + "pause"}
	var requested []*contract.RunEventApprovalRequested
	for _, event := range state.pausing.Events {
		if event.ApprovalRequested != nil {
			requested = append(requested, event.ApprovalRequested)
		}
	}
	if len(requested) != 1 || requested[0].Name != "tracker__create_issue" || requested[0].Tool != "create_issue" ||
		!sameJSON([]byte(comparisonJSON(requested[0].Source)), []byte(record)) || !sameJSON([]byte(comparisonJSON(requested[0].Args)), asked) {
		f.fail("approval_requested was %s", comparisonJSON(requested))
	} else {
		for _, event := range state.pausing.Events {
			if event.ToolCall != nil && event.ToolCall.CallID != nil && *event.ToolCall.CallID == requested[0].CallID ||
				event.ToolResult != nil && event.ToolResult.CallID != nil && *event.ToolResult.CallID == requested[0].CallID {
				f.fail("the asked call is on the stream as a call or a result")
			}
		}
		events := state.pausing.Events
		var saved *contract.RunEventExecutionCheckpoint
		for _, event := range events {
			if event.ExecutionCheckpoint != nil {
				saved = event.ExecutionCheckpoint
			}
		}
		if n := len(events); n < 3 || events[n-1].End == nil || events[n-2].Usage == nil || saved == nil ||
			saved.Durable == nil || !*saved.Durable || saved.Checkpoint.Status != "paused" || saved.Checkpoint.Approval == nil ||
			saved.Checkpoint.Approval.State != "asked" || saved.Checkpoint.Approval.CallID != requested[0].CallID {
			f.fail("the run did not end with the paused checkpoint, durable, and end")
		} else if detail := pauseAgainstVector(requested[0], saved, events[n-1]); detail != "" {
			f.fail("against approval/pause.json: %s", detail)
		}
	}
	if len(errorsOf(state.pausing.Events)) > 0 || endsOf(state.pausing.Events) != 1 || count(0) != 0 {
		f.fail("%d errors, %d ends, %d calls run", len(errorsOf(state.pausing.Events)), endsOf(state.pausing.Events), count(0))
	}
	if want := 2; !agent && len(state.pausing.Sent) != want || agent && len(state.pausing.Sent) != 1 {
		f.fail("the pausing run sent %d requests or invocations", len(state.pausing.Sent))
	}
	out = append(out, f.list...)

	f = &findings{check: prefix + "pending"}
	flow := withoutCheckpoints(state.pausing.Events)
	calls := 0
	for at, event := range flow {
		if event.ToolCall == nil || event.ToolCall.Name != "tracker__create_issue" {
			continue
		}
		calls++
		if !strings.Contains(string(event.ToolCall.Args), "Mileage rate for the UK") || event.ToolCall.Source == nil ||
			at+2 >= len(flow) || flow[at+1].Guardrail == nil || flow[at+1].Guardrail.Reason == nil || *flow[at+1].Guardrail.Reason != "approval pending" ||
			flow[at+1].Guardrail.Action != "refused" || flow[at+2].ToolResult == nil || !flow[at+2].ToolResult.IsError ||
			comparisonJSON(flow[at+2].ToolResult.CallID) != comparisonJSON(event.ToolCall.CallID) {
			f.fail("the second call is not its call, its approval pending guardrail and its refused result: %s", comparisonJSON(flow[at:]))
		}
	}
	state.server.mu.Lock()
	policy := append([]runcontext.ApprovalRequest(nil), state.server.asked...)
	state.server.mu.Unlock()
	firstAsked := 0
	for _, call := range policy {
		if call.Name == "tracker__create_issue" {
			firstAsked++
			if !sameJSON(call.Args, asked) {
				f.fail("the policy was asked about %s", call.Args)
			}
		}
	}
	if calls != 1 || firstAsked < 1 || count(0) != 0 {
		f.fail("%d create_issue calls on the stream, the policy asked %d times, %d calls run", calls, firstAsked, count(0))
	}
	out = append(out, f.list...)

	f = &findings{check: prefix + "wrong"}
	errs := errorsOf(state.wrong.Events)
	if len(errs) != 1 || errs[0].Failure == nil || errs[0].Failure.Code != "stale-state" || endsOf(state.wrong.Events) != 1 ||
		state.wrong.Events[len(state.wrong.Events)-1].End == nil || len(state.wrong.Sent) != 0 || count(1) != 0 {
		f.fail("the decision for another call: errors %s, %d sent, %d calls run", comparisonJSON(errs), len(state.wrong.Sent), count(1))
	}
	// The contract's vector (approval/wrong-call-id.json): the run's events are its events, word for word.
	if vector, err := approvalVector("wrong-call-id.json"); err != nil {
		f.fail("approval/wrong-call-id.json: %v", err)
	} else if got := comparisonJSON(withoutCheckpoints(state.wrong.Events)); !sameJSON([]byte(got), vector["events"]) {
		f.fail("the events were %s, not approval/wrong-call-id.json's", got)
	}
	out = append(out, f.list...)

	callID := ""
	if state.saved.Approval != nil {
		callID = state.saved.Approval.CallID
	}
	f = &findings{check: prefix + "refused"}
	refused := writtenAt(state.refused.Events, "refused")
	if refused == nil || refused.Approval.CallID != callID || refused.Approval.DecidedAt == nil || refused.Approval.Note == nil ||
		*refused.Approval.Note != state.file.Note || refused.Approval.Result == nil || refused.Approval.Result.IsError == nil || !*refused.Approval.Result.IsError ||
		!strings.Contains(string(refused.Approval.Result.StructuredContent), `"permission"`) {
		f.fail("the refused approval written was %s", comparisonJSON(refused))
	}
	text := continuationIn(state.refused)
	data, _, ok := approvalData(text)
	if !ok || data["approved"] != false || data["callId"] != callID || data["note"] != state.file.Note || data["tool"] != "create_issue" {
		f.fail("the continuation carried %q", text)
	}
	if len(state.refused.Sent) > 0 {
		whole := state.refused.Sent[0]
		if len(state.refused.Bodies) > 0 {
			whole = state.refused.Bodies[0]
		}
		if strings.Count(whole, state.file.Note) != 1 || !onlyFramed(text, state.file.Note) {
			f.fail("the note is not once, inside the block, in what the model was sent")
		}
	}
	if last := lastCheckpointOf(state.refused.Events); last == nil || last.Approval == nil || last.Approval.State != "answered" ||
		len(errorsOf(state.refused.Events)) > 0 || count(2) != 0 {
		f.fail("the refused run ended on %s with %d errors, %d calls run", comparisonJSON(last), len(errorsOf(state.refused.Events)), count(2))
	}
	// The person's refusal is said once, after the written refusal: a guardrail with the reason
	// approval refused, its sentence the refused result's and not the person's note.
	notices := guardrailsOf(state.refused.Events, "")
	if len(notices) != 1 || notices[0].Tool != "tracker__create_issue" || notices[0].Action != "refused" || notices[0].Reason == nil || *notices[0].Reason != "approval refused" ||
		strings.Contains(notices[0].Detail, state.file.Note) || !strings.Contains(notices[0].Detail, "The person refused this call of tracker__create_issue") {
		f.fail("the refused run's guardrails were %s", comparisonJSON(notices))
	} else {
		at := -1
		for i, event := range state.refused.Events {
			if held := event.ExecutionCheckpoint; held != nil && held.Durable != nil && *held.Durable && held.Checkpoint.Approval != nil && held.Checkpoint.Approval.State == "refused" {
				at = i
			}
		}
		if at < 0 || at+1 >= len(state.refused.Events) || state.refused.Events[at+1].Guardrail == nil {
			f.fail("the guardrail does not follow the written refusal")
		}
	}
	for name, run := range map[string]*pauseRun{"approved": state.approved, "restarted": state.restarted} {
		for _, notice := range guardrailsOf(runEvents(run), "") {
			if notice.Reason != nil && *notice.Reason == "approval refused" {
				f.fail("the %s run said the person refused the call", name)
			}
		}
	}
	out = append(out, f.list...)

	f = &findings{check: prefix + "approved"}
	executed := writtenAt(state.approved.Events, "executed")
	if strings.Join(state.approvedLog, ", ") != "acknowledged approved, execute, acknowledged executed" || writtenAt(state.approved.Events, "approved") == nil ||
		executed == nil || executed.Approval.Result == nil || !strings.Contains(comparisonJSON(executed.Approval.Result), state.sources.Issue) ||
		len(state.approved.Sent) != 0 || count(3) != 1 {
		f.fail("the approved run: %v, %d sent, %d calls run", state.approvedLog, len(state.approved.Sent), count(3))
	}
	out = append(out, f.list...)

	f = &findings{check: prefix + "restart"}
	if state.restarted == nil {
		f.fail("the approved run wrote no executed checkpoint to restart from")
	} else {
		data, _, ok := approvalData(continuationIn(state.restarted))
		if count(4) != 1 || writtenAt(state.restarted.Events, "approved") != nil || !ok || data["approved"] != true || resultText(data) != state.sources.Issue {
			f.fail("the restart: %d calls run, continuation %q", count(4), continuationIn(state.restarted))
		}
		if last := lastCheckpointOf(state.restarted.Events); last == nil || last.Approval == nil || last.Approval.State != "answered" || len(errorsOf(state.restarted.Events)) > 0 {
			f.fail("the restart ended on %s", comparisonJSON(last))
		}
	}
	out = append(out, f.list...)

	f = &findings{check: prefix + "continuation"}
	var markers []string
	for _, run := range []*pauseRun{state.refused, state.restarted} {
		if run == nil || len(run.Sent) == 0 {
			f.fail("a continued run sent nothing")
			continue
		}
		whole := run.Sent[0]
		at := strings.Index(whole, runcontext.ApprovalContinuation)
		if strings.Index(whole, state.file.Request) < 0 || at < strings.Index(whole, state.file.Request) || strings.Count(whole, runcontext.ApprovalContinuation) != 1 {
			f.fail("the continuation does not follow the request text once")
		}
		if _, marker, ok := approvalData(continuationIn(run)); ok {
			markers = append(markers, marker)
		}
	}
	if len(markers) != 2 || markers[0] == markers[1] {
		f.fail("the continuations' markers were %v", markers)
	}
	// On a model capability the stable prefix, the tool list and the system text, is the pausing
	// run's bytes in each continued run, the refused one and the approved one, with nothing
	// normalised: the continued runs reuse the marker the paused checkpoint carries. A model
	// capability's requests are bodies; a pausing run with none is a failure, not a skip.
	if !agent {
		held, found := "", false
		if len(state.pausing.Bodies) > 0 {
			held, found = stablePrefix(state.pausing.Bodies[0])
		}
		marker := ""
		if state.saved != nil && state.saved.Approval != nil && state.saved.Approval.Marker != nil {
			marker = *state.saved.Approval.Marker
		}
		switch {
		case !found:
			f.fail("the pausing run's first request carries no tool list or system text")
		case marker == "" || !strings.Contains(held, "BEGIN RETRIEVED DATA "+marker+" from the description of tool"):
			f.fail("the pausing run's framed descriptions do not carry the marker the paused checkpoint holds (%q)", marker)
		}
		for _, one := range []struct {
			name string
			run  *pauseRun
		}{{"refused", state.refused}, {"approved", state.restarted}} {
			if one.run == nil || len(one.run.Bodies) == 0 {
				f.fail("the %s run sent no request body", one.name)
				continue
			}
			if again, _ := stablePrefix(one.run.Bodies[0]); !found || again != held {
				f.fail("the %s run's tool list or system text is not the pausing run's", one.name)
			}
		}
	}
	out = append(out, f.list...)

	f = &findings{check: prefix + "figures"}
	if state.restarted != nil {
		after := lastCheckpointOf(state.restarted.Events)
		switch {
		case after == nil:
			f.fail("the restart wrote no checkpoint")
		case !agent && (after.Unit != "model-requests" || after.Used != state.saved.Used+1):
			f.fail("the continuation counted %d requests after %d", after.Used, state.saved.Used)
		case agent && (after.Unit != "provider-managed" || after.Used != 0 || len(after.Completions) > 0 || after.Delegation == nil ||
			after.Delegation.Parent.AgentRuns == nil || state.saved.Delegation == nil || state.saved.Delegation.Parent.AgentRuns == nil ||
			*after.Delegation.Parent.AgentRuns != *state.saved.Delegation.Parent.AgentRuns+1 || after.Delegation.Parent.Tokens != nil):
			f.fail("the continuation's figures were %s after %s", comparisonJSON(after.Delegation), comparisonJSON(state.saved.Delegation))
		}
	}
	return append(out, f.list...)
}

// ApprovalPause is scenario 8 on a model capability, the adapter run as it is given.
var ApprovalPause = ApprovalPauseWith(nil)

// ApprovalPauseWith is scenario 8 on a model capability through the delegation coordinator with
// wraps the adapter in (go/adapter/delegate, the path a continued run takes in a host that
// delegates), or as given where with is nil.
func ApprovalPauseWith(with func(Adapter) Adapter) ScenarioCheck {
	return ScenarioCheck{
		Scenario: "approval-pause",
		Names:    []string{"8 pause", "8 pending", "8 wrong", "8 refused", "8 approved", "8 restart", "8 continuation", "8 figures"},
		Run: func(ctx context.Context, server *Server, answerAs string, adapter Adapter) ([]Finding, error) {
			if with != nil {
				adapter = with(adapter)
			}
			var sources sourcesFile
			if err := ScenarioFile("tool-sources", &sources); err != nil {
				return nil, err
			}
			var file pauseFile
			if err := ScenarioFile("approval-pause", &file); err != nil {
				return nil, err
			}
			declared := pauseTools(sources, file.Asked.Tool, newPauseServer(false))
			scenario, err := openScenario(ctx, server, ScenarioEndpoint{Scenario: "approval-pause", AnswerAs: answerAs}, declared, true)
			if err != nil {
				return nil, err
			}
			sent := 0
			first := true
			play := func(ctx context.Context, _ []sourcesTurn, tools []runcontext.HostTool, input *runcontext.Input) (*pauseRun, error) {
				// Each continued run is answered with the continuation's turns.
				if !first {
					if err := scenario.session.Advance(context.WithoutCancel(ctx)); err != nil {
						return nil, err
					}
				}
				first = false
				ran, err := scenario.runOn(ctx, adapter, &Run{Prompt: file.Request, AllowConversation: true, Interactive: true, HostTools: tools, Context: input,
					Children: scenarioChildren(scenario)})
				if err != nil {
					return nil, err
				}
				requests := ran.Requests[sent:]
				sent = len(ran.Requests)
				out := &pauseRun{Events: ran.Events}
				for _, request := range requests {
					out.Sent = append(out.Sent, strings.Join(openingOf(request), "\n"))
					out.Bodies = append(out.Bodies, string(request.Body))
				}
				return out, nil
			}
			state, err := approvalScenario(ctx, play, false)
			if err != nil {
				return nil, err
			}
			return approvalPauseFindings(state, "8 ", false), nil
		},
	}
}

// scenarioChildren is the host's capability to start a child's gated session on a scenario's
// runtime, so a delegation coordinator drives the response; scenario 8 delegates nothing.
func scenarioChildren(scenario *scenarioHost) adapter.Children {
	return func(_ context.Context, profile contract.DelegatedTaskProfile, emit func(contract.RunEvent)) (*gate.Calls, []contract.ToolOfferRuntimeTool, error) {
		child := gate.New(gate.Options{Runtime: gate.Call(scenario.session.RuntimeCall()), Granted: gate.Ceiling, Emit: emit, Turn: scenario.gate})
		child.SetOffer(scenario.offer)
		phase := gate.ChildPhase(profile, scenario.offer, scenario.gate.Phase())
		child.Enter(phase)
		return child.Calls(), phase.Offer(scenario.offer).Tools, nil
	}
}

// openingOf is the text parts of a request's first user message.
func openingOf(request EndpointRequest) []string {
	var body struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	_ = json.Unmarshal(request.Body, &body)
	for _, message := range body.Messages {
		if message.Role != "user" {
			continue
		}
		var text string
		if json.Unmarshal(message.Content, &text) == nil {
			return []string{text}
		}
		var parts []struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(message.Content, &parts)
		var out []string
		for _, part := range parts {
			out = append(out, part.Text)
		}
		return out
	}
	return nil
}

// AgentApprovalNames are scenario 8's checks on the agent capability: the model legs' checks of the
// same names, made at the provider-run function's seam with the delegation coordinator counting the
// invocations (the TypeScript suite's agent engine makes the same ones).
var AgentApprovalNames = []string{"8 agent pause", "8 agent pending", "8 agent wrong", "8 agent refused", "8 agent approved", "8 agent restart", "8 agent continuation", "8 agent figures"}

// agentApprovalChecks is scenario 8 on the agent capability, through the coordinator the caller
// binds (with), each run's invocations played by the binding's provider-run function. The pausing
// invocation makes its two calls at once.
func agentApprovalChecks(ctx context.Context, server *Server, host AgentHost, with func(Adapter) Adapter, inner Adapter) ([]Finding, error) {
	var sources sourcesFile
	if err := ScenarioFile("tool-sources", &sources); err != nil {
		return nil, err
	}
	var file pauseFile
	if err := ScenarioFile("approval-pause", &file); err != nil {
		return nil, err
	}
	scenario, err := openScenario(ctx, server, ScenarioEndpoint{Scenario: "approval-pause", AnswerAs: "stream"}, pauseTools(sources, file.Asked.Tool, newPauseServer(false)), true)
	if err != nil {
		return nil, err
	}
	runner := inner
	if with != nil {
		runner = with(inner)
	}
	// The host's capability to start a child, so the coordinator drives the response and counts its
	// invocations; the scenario delegates nothing.
	children := scenarioChildren(scenario)
	play := func(ctx context.Context, turns []sourcesTurn, tools []runcontext.HostTool, input *runcontext.Input) (*pauseRun, error) {
		steps := sourcesSteps(turns)
		watch := &monitor{}
		run := &Run{ModelName: parentModel, Gate: scenario.gate.Calls(), Tools: scenario.offer.Tools, Prompt: file.Request, AllowConversation: true, Interactive: true,
			HostTools: tools, Context: input, Emit: scenario.emit, Children: children,
			Agent: &adapter.Agent{Model: parentModel, Run: watch.wrap(host.Bind(func(adapter.AgentRequest, int) AgentPlay { return AgentPlay{Steps: steps} }))}}
		seal := sealNetwork()
		_ = runner.Run(ctx, run)
		reached := seal.lift()
		events := scenario.take()
		if len(reached) > 0 {
			events = append(events, contract.RunEvent{Error: &contract.RunEventError{Type: "error", Message: "the adapter reached for http.DefaultTransport: " + strings.Join(reached, ", ")}})
		}
		out := &pauseRun{Events: events}
		for _, call := range watch.admitted() {
			out.Sent = append(out.Sent, call.Request.Prompt)
		}
		return out, nil
	}
	state, err := approvalScenario(ctx, play, true)
	if err != nil {
		return nil, err
	}
	findings := approvalPauseFindings(state, "8 agent ", true)
	if n := len(state.counts); n != 5 {
		findings = append(findings, Finding{Check: "8 agent restart", Detail: "the scenario stopped after " + strconv.Itoa(n) + " runs"})
	}
	return findings, nil
}

// runEvents are a run's events, none where the run did not happen.
func runEvents(run *pauseRun) []contract.RunEvent {
	if run == nil {
		return nil
	}
	return run.Events
}

// stablePrefix is a model request's tool list and system text, as the wire spells them, byte for
// byte; false where the body carries neither.
func stablePrefix(body string) (string, bool) {
	var whole map[string]json.RawMessage
	if json.Unmarshal([]byte(body), &whole) != nil {
		return "", false
	}
	prefix := map[string]json.RawMessage{}
	for _, key := range []string{"tools", "system", "systemInstruction"} {
		if held, ok := whole[key]; ok {
			prefix[key] = held
		}
	}
	var messages []json.RawMessage
	if json.Unmarshal(whole["messages"], &messages) == nil {
		var system []json.RawMessage
		for _, message := range messages {
			var role struct {
				Role string `json:"role"`
			}
			if json.Unmarshal(message, &role) == nil && role.Role == "system" {
				system = append(system, message)
			}
		}
		if len(system) > 0 {
			encoded, _ := json.Marshal(system)
			prefix["messages"] = encoded
		}
	}
	if len(prefix) == 0 {
		return "", false
	}
	encoded, _ := json.Marshal(prefix)
	return string(encoded), true
}
