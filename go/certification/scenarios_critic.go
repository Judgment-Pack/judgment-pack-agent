package certification

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/turn"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

// Scenario 10 (ADR-0010, certification/scenarios/critic-switch.json): the critic's switch. One
// request and one proposal, and the file's list of runs, played in order, one session each: auto with
// declared cases, a conversation turn, a proposal on an existing pack and one on a new pack, the
// model's ask taken and a second ask refused, an ask under off, on, a reviewer assigned under on, the
// critic at the author's tier and lowered explicitly, a run broken off once the model's ask is
// written and continued from its checkpoint's JSON text, and the measurement: the same proposal at
// two author tiers, with and without the critic. On the agent capability, a delegation batch that
// would take the invocation held back for the critic, a smaller one, and the larger one under off.
// The host's half is this runner's, as it is scenarios.test.ts's.

type switchListed struct {
	Name    string `json:"name"`
	Author  string `json:"author"`
	Session struct {
		Cases            bool   `json:"cases"`
		ExistingPack     bool   `json:"existingPack"`
		Critic           string `json:"critic"`
		ReviewerAssigned bool   `json:"reviewerAssigned"`
		Tier             string `json:"tier"`
		CriticReasoning  string `json:"criticReasoning"`
	} `json:"session"`
	BreakAtAsk bool   `json:"breakAtAsk"`
	ResumeFrom string `json:"resumeFrom"`
	Measure    bool   `json:"measure"`
}

type switchFile struct {
	Request    string                   `json:"request"`
	TestPrompt string                   `json:"testPrompt"`
	Cases      []json.RawMessage        `json:"cases"`
	Author     map[string][]sourcesTurn `json:"author"`
	Critic     []sourcesTurn            `json:"critic"`
	Tiers      struct {
		Model []string `json:"model"`
		Agent []string `json:"agent"`
	} `json:"tiers"`
	Runs       []switchListed `json:"runs"`
	Delegation struct {
		Tasks []json.RawMessage `json:"tasks"`
		Child sourcesTurn       `json:"child"`
	} `json:"delegation"`
}

// switchRun is one run of scenario 10: its events, and the model requests it sent (the model
// capability) or the invocations it made (the agent capability).
type switchRun struct {
	Events      []contract.RunEvent
	Requests    []EndpointRequest
	Invocations []adapter.AgentRequest
}

// MeasuredRow is one row of scenario 10's measurement: the same proposal at an author tier, with or
// without the critic. Recorded, never judged: no verdict is drawn from it.
type MeasuredRow struct {
	Adapter, Leg, Author, Critic string
	Ran                          bool
	Reasoning, Refuted           string
	Checks, CriticRequests       int
}

var measurement struct {
	mu   sync.Mutex
	rows []MeasuredRow
}

// TakeMeasurement are the measurement rows scenario 10 recorded since the last take, for a report to
// print as a table (cmd/harness-check).
func TakeMeasurement() []MeasuredRow {
	measurement.mu.Lock()
	defer measurement.mu.Unlock()
	out := measurement.rows
	measurement.rows = nil
	return out
}

type switched struct {
	runs     map[string]*switchRun
	saved    *contract.Checkpoint
	measured []MeasuredRow
	reserve  map[string]*switchRun
}

// switchPlay plays one run: the author's turns, the run's members, the author's tier, and where
// stop answers true for an event the run is broken off there.
type switchPlay func(ctx context.Context, author []sourcesTurn, run Run, tier string, stop func(contract.RunEvent) bool) (*switchRun, error)

// switchScenario plays scenario 10's runs with play, in the file's order.
func switchScenario(ctx context.Context, file switchFile, kind string, play switchPlay, label [2]string) (*switched, error) {
	tiers := file.Tiers.Model
	if kind == "agent" {
		tiers = file.Tiers.Agent
	}
	tierOf := func(symbol string) string {
		switch symbol {
		case "low":
			return tiers[0]
		case "high":
			return tiers[1]
		}
		return ""
	}
	out := &switched{runs: map[string]*switchRun{}}
	for _, listed := range file.Runs {
		run := Run{Prompt: file.Request, TestPrompt: file.TestPrompt, AllowConversation: true, Interactive: false, Context: requestInput(file.Request),
			Critic: listed.Session.Critic, ExistingPack: listed.Session.ExistingPack, ReviewerAssigned: listed.Session.ReviewerAssigned,
			CriticReasoning: tierOf(listed.Session.CriticReasoning)}
		if listed.Session.Cases {
			run.Cases = file.Cases
		}
		if listed.ResumeFrom != "" {
			execution, err := readBack(out.saved)
			if err != nil {
				return nil, fmt.Errorf("the run %s has no checkpoint to continue: %w", listed.ResumeFrom, err)
			}
			run.Context.Execution = execution
		}
		var stop func(contract.RunEvent) bool
		if listed.BreakAtAsk {
			stop = func(event contract.RunEvent) bool {
				held := event.ExecutionCheckpoint
				return held != nil && held.Checkpoint.Critic != nil && held.Checkpoint.Critic.Trigger != nil && *held.Checkpoint.Critic.Trigger == "model-asked"
			}
		}
		ran, err := play(ctx, file.Author[listed.Author], run, tierOf(listed.Session.Tier), stop)
		if err != nil {
			return nil, err
		}
		out.runs[listed.Name] = ran
		if listed.BreakAtAsk {
			out.saved = lastCheckpointOf(ran.Events)
		}
		if listed.Measure {
			row := MeasuredRow{Adapter: label[0], Leg: label[1], Author: tierOf(listed.Session.Tier), Critic: listed.Session.Critic}
			if decided := decisionsOf(ran.Events); len(decided) > 0 {
				row.Ran = decided[0].Ran
				if decided[0].Reasoning != nil {
					row.Reasoning = *decided[0].Reasoning
				}
			}
			if critiques := critiquesOf(ran.Events); len(critiques) > 0 {
				row.Refuted = fmt.Sprint(critiques[0].Refuted)
				row.Checks = len(critiques[0].Checks)
				if critiques[0].Figures != nil {
					row.CriticRequests = int(critiques[0].Figures.Requests)
				}
			}
			out.measured = append(out.measured, row)
		}
	}
	measurement.mu.Lock()
	measurement.rows = append(measurement.rows, out.measured...)
	measurement.mu.Unlock()
	return out, nil
}

func decisionsOf(events []contract.RunEvent) []*contract.RunEventCriticDecided {
	return ofType(events, func(e contract.RunEvent) *contract.RunEventCriticDecided { return e.CriticDecided })
}

// decisionOf is the one decision a run made, as the checkpoint keeps it, or a finding.
func decisionOf(f *findings, name string, ran *switchRun) *contract.CheckpointCritic {
	if ran == nil {
		f.fail("the run %s did not run", name)
		return nil
	}
	decided := decisionsOf(ran.Events)
	if len(decided) != 1 {
		f.fail("the run %s made %d decisions", name, len(decided))
		return nil
	}
	held := decided[0]
	ranValue, reason := held.Ran, held.Reason
	return &contract.CheckpointCritic{Mode: held.Mode, Ran: &ranValue, Reason: &reason, Reasoning: held.Reasoning, Trigger: held.Trigger}
}

// want is a decision as the checks state it: mode, ran, reason, the trigger and the tier, "" none.
func want(mode string, ran bool, reason, trigger, reasoning string) *contract.CheckpointCritic {
	return runcontext.CriticDecision{Mode: mode, Ran: ran, Reason: reason, Trigger: trigger, Reasoning: reasoning}.Record()
}

func sameDecision(f *findings, name string, got, wanted *contract.CheckpointCritic) {
	if got == nil {
		return
	}
	if comparisonJSON(got) != comparisonJSON(wanted) {
		f.fail("the run %s decided %s, not %s", name, comparisonJSON(got), comparisonJSON(wanted))
	}
}

// criticRan is whether a run sent a critic request or made a critic invocation.
func criticRan(ran *switchRun) bool {
	if ran == nil {
		return false
	}
	for _, request := range ran.Requests {
		if strings.Contains(string(request.Body), turn.RefutationMarker) {
			return true
		}
	}
	for _, request := range ran.Invocations {
		if request.Phase == "critic" {
			return true
		}
	}
	return false
}

// authorOffered are the names the run's first request or invocation offered the author.
func authorOffered(ran *switchRun) []string {
	if ran == nil {
		return nil
	}
	if len(ran.Requests) > 0 {
		return offeredNames(ran.Requests[0])
	}
	if len(ran.Invocations) > 0 {
		var names []string
		for _, tool := range agentOffered(ran.Invocations[0]) {
			names = append(names, tool.Name)
		}
		return names
	}
	return nil
}

// criticOffered are the names each critic request or invocation offered, together.
func criticOffered(ran *switchRun) []string {
	var names []string
	for _, request := range ran.Requests {
		if strings.Contains(string(request.Body), turn.RefutationMarker) {
			names = append(names, offeredNames(request)...)
		}
	}
	for _, request := range ran.Invocations {
		if request.Phase == "critic" {
			for _, tool := range agentOffered(request) {
				names = append(names, tool.Name)
			}
		}
	}
	return names
}

// reviewGuardrails are a run's guardrails on request_review: each its reason and detail.
func reviewGuardrails(ran *switchRun) [][2]string {
	var out [][2]string
	for _, held := range ofType(ran.Events, func(e contract.RunEvent) *contract.RunEventGuardrail { return e.Guardrail }) {
		if held.Tool == runcontext.RequestReview {
			reason := ""
			if held.Reason != nil {
				reason = *held.Reason
			}
			out = append(out, [2]string{reason, held.Detail})
		}
	}
	return out
}

// reviewResults are a run's results of request_review, in order.
func reviewResults(ran *switchRun) []*contract.ToolResult {
	var out []*contract.ToolResult
	for _, held := range ofType(ran.Events, func(e contract.RunEvent) *contract.ToolResult { return e.ToolResult }) {
		if held.Name == runcontext.RequestReview {
			out = append(out, held)
		}
	}
	return out
}

// inOrder holds the decision to the durable checkpoint that follows it, carrying it, and before the
// critique and the proposal.
func inOrder(f *findings, name string, ran *switchRun) {
	at := -1
	for i, event := range ran.Events {
		if event.CriticDecided != nil {
			at = i
		}
	}
	if at < 0 || at+1 >= len(ran.Events) {
		f.fail("the run %s carries no decision followed by its checkpoint", name)
		return
	}
	next := ran.Events[at+1].ExecutionCheckpoint
	held := ran.Events[at].CriticDecided
	ranValue, reason := held.Ran, held.Reason
	if next == nil || next.Durable == nil || !*next.Durable || comparisonJSON(next.Checkpoint.Critic) != comparisonJSON(&contract.CheckpointCritic{Mode: held.Mode, Ran: &ranValue, Reason: &reason, Reasoning: held.Reasoning, Trigger: held.Trigger}) {
		f.fail("the run %s's decision is not followed by a durable checkpoint carrying it: %s", name, comparisonJSON(next))
	}
	for i, event := range ran.Events {
		if (event.Critique != nil || event.Proposal != nil) && i < at {
			f.fail("the run %s's decision comes after its critique or proposal", name)
		}
	}
}

func typesOf(events []contract.RunEvent) []string {
	var out []string
	for _, event := range withoutCheckpoints(events) {
		raw, _ := json.Marshal(event)
		var typed struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(raw, &typed)
		out = append(out, typed.Type)
	}
	return out
}

// switchFindings are scenario 10's checks over what a capability's runs left, names prefixed.
func switchFindings(state *switched, prefix string, kind string, tiers []string) []Finding {
	var out []Finding
	low, high := tiers[0], tiers[1]
	unset := "none"
	if kind == "model" {
		unset = "off"
	}
	runs := state.runs

	f := &findings{check: prefix + "cases"}
	sameDecision(f, "cases", decisionOf(f, "cases", runs["cases"]), want("auto", true, "cases", "cases", unset))
	if !criticRan(runs["cases"]) {
		f.fail("auto with declared cases ran no critic")
	} else {
		inOrder(f, "cases", runs["cases"])
		if types := typesOf(runs["cases"].Events); len(types) < 3 || !slices.Equal(types[len(types)-3:], []string{"critique", "proposal", "end"}) {
			f.fail("the run with cases ended %v", types)
		}
	}
	out = append(out, f.list...)

	f = &findings{check: prefix + "converse"}
	sameDecision(f, "converse", decisionOf(f, "converse", runs["converse"]), want("auto", false, "no-proposal", "", unset))
	if criticRan(runs["converse"]) || len(critiquesOf(runs["converse"].Events)) > 0 {
		f.fail("a conversation turn ran a critic")
	}
	out = append(out, f.list...)

	f = &findings{check: prefix + "existing"}
	sameDecision(f, "existing", decisionOf(f, "existing", runs["existing"]), want("auto", true, "changes-existing", "changes-existing", unset))
	sameDecision(f, "fresh", decisionOf(f, "fresh", runs["fresh"]), want("auto", false, "table", "", unset))
	if !criticRan(runs["existing"]) || criticRan(runs["fresh"]) {
		f.fail("the critic ran %v on the existing pack and %v on the new one", criticRan(runs["existing"]), criticRan(runs["fresh"]))
	}
	out = append(out, f.list...)

	f = &findings{check: prefix + "asked"}
	asked := runs["asked"]
	sameDecision(f, "asked", decisionOf(f, "asked", asked), want("auto", true, "model-asked", "model-asked", unset))
	if !criticRan(asked) {
		f.fail("the model's ask under auto ran no critic")
	}
	results := reviewResults(asked)
	if len(results) != 2 || results[0].IsError || !results[1].IsError {
		f.fail("the two asks were answered %s", comparisonJSON(results))
	} else if note := string(results[0].Structured); note != `{"asked":true,"note":`+runcontext.QuoteJSON(runcontext.ReviewTaken)+`}` {
		f.fail("the first ask was answered %s", note)
	}
	if got := reviewGuardrails(asked); len(got) != 1 || got[0] != [2]string{"review refused", runcontext.ReviewRefusal("asked")} {
		f.fail("the second ask's guardrails were %v", got)
	}
	if !slices.Contains(authorOffered(asked), runcontext.RequestReview) || slices.Contains(criticOffered(asked), runcontext.RequestReview) {
		f.fail("the author was offered %v and the critic %v", authorOffered(asked), criticOffered(asked))
	}
	out = append(out, f.list...)

	f = &findings{check: prefix + "off"}
	off := runs["off"]
	sameDecision(f, "off", decisionOf(f, "off", off), want("off", false, "off", "", unset))
	if criticRan(off) || slices.Contains(authorOffered(off), runcontext.RequestReview) {
		f.fail("under off the critic ran (%v) or request_review was offered", criticRan(off))
	}
	if got := reviewGuardrails(off); len(got) != 1 || got[0] != [2]string{"review refused", runcontext.ReviewRefusal("off")} {
		f.fail("the ask under off earned the guardrails %v", got)
	}
	if results := reviewResults(off); len(results) != 1 || !results[0].IsError {
		f.fail("the ask under off was answered %s", comparisonJSON(results))
	}
	if last := lastCheckpointOf(off.Events); last == nil || comparisonJSON(last.Critic) != comparisonJSON(want("off", false, "off", "", unset)) {
		f.fail("the checkpoint under off kept %s", comparisonJSON(last))
	}
	out = append(out, f.list...)

	f = &findings{check: prefix + "on"}
	sameDecision(f, "on", decisionOf(f, "on", runs["on"]), want("on", true, "person", "", unset))
	if !criticRan(runs["on"]) || slices.Contains(authorOffered(runs["on"]), runcontext.RequestReview) {
		f.fail("on ran no critic, or offered request_review")
	} else {
		inOrder(f, "on", runs["on"])
	}
	out = append(out, f.list...)

	f = &findings{check: prefix + "reviewer"}
	sameDecision(f, "reviewer", decisionOf(f, "reviewer", runs["reviewer"]), want("on", false, "reviewer-assigned", "", unset))
	if criticRan(runs["reviewer"]) || len(critiquesOf(runs["reviewer"].Events)) > 0 {
		f.fail("a reviewer assigned under on ran a critic")
	}
	out = append(out, f.list...)

	f = &findings{check: prefix + "tier"}
	sameDecision(f, "tier", decisionOf(f, "tier", runs["tier"]), want("on", true, "person", "", high))
	sameDecision(f, "lowered", decisionOf(f, "lowered", runs["lowered"]), want("on", true, "person", "", low))
	if kind == "model" {
		// On the OpenAI-compatible wire on is reasoning_effort high and off is no member at all.
		sent := func(ran *switchRun, critic bool) []string {
			var out []string
			for _, request := range ran.Requests {
				if strings.Contains(string(request.Body), turn.RefutationMarker) != critic {
					continue
				}
				var body struct {
					Effort *string `json:"reasoning_effort"`
				}
				_ = json.Unmarshal(request.Body, &body)
				effort := "none"
				if body.Effort != nil {
					effort = *body.Effort
				}
				out = append(out, effort)
			}
			return out
		}
		if author, critic := sent(runs["tier"], false), sent(runs["tier"], true); len(critic) == 0 || slices.ContainsFunc(append(author, critic...), func(e string) bool { return e != "high" }) {
			f.fail("at the author's on the author sent %v and the critic %v", author, critic)
		}
		if critic := sent(runs["lowered"], true); len(critic) == 0 || slices.ContainsFunc(critic, func(e string) bool { return e != "none" }) {
			f.fail("lowered to off the critic sent %v", critic)
		}
	} else {
		efforts := func(ran *switchRun) [][2]string {
			var out [][2]string
			for _, request := range ran.Invocations {
				out = append(out, [2]string{request.Phase, request.Effort})
			}
			return out
		}
		if got := efforts(runs["tier"]); !slices.Equal(got, [][2]string{{"author", high}, {"critic", high}}) {
			f.fail("at the author's %s the invocations ran at %v", high, got)
		}
		if got := efforts(runs["lowered"]); !slices.Equal(got, [][2]string{{"author", high}, {"critic", low}}) {
			f.fail("lowered to %s the invocations ran at %v", low, got)
		}
	}
	out = append(out, f.list...)

	f = &findings{check: prefix + "resume"}
	if state.saved == nil || comparisonJSON(state.saved.Critic) != comparisonJSON(&contract.CheckpointCritic{Mode: "auto", Trigger: optionalString("model-asked")}) {
		f.fail("the broken run's checkpoint kept %s", comparisonJSON(state.saved))
	}
	if broken := runs["broken"]; broken == nil || len(decisionsOf(broken.Events)) > 0 {
		f.fail("the broken run decided before the break")
	}
	resumed := runs["resumed"]
	sameDecision(f, "resumed", decisionOf(f, "resumed", resumed), want("auto", true, "model-asked", "model-asked", unset))
	if !criticRan(resumed) {
		f.fail("the resumed run ran no critic")
	}
	if got := reviewGuardrails(resumed); len(got) != 1 || got[0][1] != runcontext.ReviewRefusal("asked") {
		f.fail("the resumed run's own ask earned %v", got)
	}
	if last := lastCheckpointOf(resumed.Events); last == nil || comparisonJSON(last.Critic) != comparisonJSON(want("auto", true, "model-asked", "model-asked", unset)) {
		f.fail("the resumed run's checkpoint kept %s", comparisonJSON(last))
	}
	out = append(out, f.list...)

	f = &findings{check: prefix + "measurement"}
	var rows [][4]string
	for _, row := range state.measured {
		rows = append(rows, [4]string{row.Author, row.Critic, fmt.Sprint(row.Ran), row.Reasoning})
	}
	if wanted := [][4]string{{low, "off", "false", low}, {low, "on", "true", low}, {high, "off", "false", high}, {high, "on", "true", high}}; !slices.Equal(rows, wanted) {
		f.fail("the measurement's rows were %v, not %v", rows, wanted)
	}
	for _, row := range state.measured {
		if row.Checks == 0 && row.Refuted != "" {
			f.fail("a critique with no check: %+v", row)
		}
	}
	out = append(out, f.list...)
	return out
}

func optionalString(value string) *string { return &value }

// CriticSwitch is scenario 10 on a model capability.
var CriticSwitch = ScenarioCheck{
	Scenario: "critic-switch",
	Names:    []string{"10 cases", "10 converse", "10 existing", "10 asked", "10 off", "10 on", "10 reviewer", "10 tier", "10 resume", "10 measurement"},
	Run: func(ctx context.Context, server *Server, answerAs string, inner Adapter) ([]Finding, error) {
		var file switchFile
		if err := ScenarioFile("critic-switch", &file); err != nil {
			return nil, err
		}
		scenario, err := openScenario(ctx, server, ScenarioEndpoint{Scenario: "critic-switch", AnswerAs: answerAs}, nil, true)
		if err != nil {
			return nil, err
		}
		sent, first := 0, true
		play := func(ctx context.Context, _ []sourcesTurn, run Run, tier string, stop func(contract.RunEvent) bool) (*switchRun, error) {
			if !first {
				if err := scenario.session.Advance(ctx); err != nil {
					return nil, err
				}
			}
			first = false
			run.Thinking = tier
			broken, cancel := context.WithCancel(ctx)
			defer cancel()
			scenario.watch(func(event contract.RunEvent) {
				if stop != nil && stop(event) {
					cancel()
				}
			})
			defer scenario.watch(nil)
			ran, err := scenario.runOn(broken, inner, &run)
			if err != nil {
				return nil, err
			}
			out := &switchRun{Events: ran.Events, Requests: ran.Requests[sent:]}
			sent = len(ran.Requests)
			return out, nil
		}
		state, err := switchScenario(ctx, file, "model", play, [2]string{"eino", answerAs})
		if err != nil {
			return nil, err
		}
		return switchFindings(state, "10 ", "model", file.Tiers.Model), nil
	},
}

// AgentSwitchNames are scenario 10's checks on the agent capability: the model legs' checks of the
// same names, made at the provider-run function's seam, and the invocation held back for the critic.
var AgentSwitchNames = []string{"10 agent cases", "10 agent converse", "10 agent existing", "10 agent asked", "10 agent off", "10 agent on", "10 agent reviewer", "10 agent tier", "10 agent resume", "10 agent measurement", "10 agent reserve"}

// agentSwitchChecks is scenario 10 on the agent capability, each run played as the scenario's turns by
// the binding's provider-run function; then the reserve, three responses of the delegation
// coordinator.
func agentSwitchChecks(ctx context.Context, server *Server, host AgentHost, with func(Adapter) Adapter, inner Adapter) ([]Finding, error) {
	var file switchFile
	if err := ScenarioFile("critic-switch", &file); err != nil {
		return nil, err
	}
	scenario, err := openScenario(ctx, server, ScenarioEndpoint{Scenario: "critic-switch", AnswerAs: "stream"}, nil, true)
	if err != nil {
		return nil, err
	}
	critic := sourcesSteps(file.Critic)
	play := func(ctx context.Context, author []sourcesTurn, run Run, tier string, stop func(contract.RunEvent) bool) (*switchRun, error) {
		steps := sourcesSteps(author)
		watch := &monitor{}
		broken, cancel := context.WithCancel(ctx)
		defer cancel()
		run.ModelName, run.Gate, run.Tools = parentModel, scenario.gate.Calls(), scenario.offer.Tools
		run.Emit = func(event contract.RunEvent) {
			scenario.emit(event)
			if stop != nil && stop(event) {
				cancel()
			}
		}
		run.Agent = &adapter.Agent{Model: parentModel, Effort: tier, Run: watch.wrap(host.Bind(func(request adapter.AgentRequest, _ int) AgentPlay {
			if request.Phase == "critic" {
				return AgentPlay{Steps: critic}
			}
			return AgentPlay{Steps: steps}
		}))}
		seal := sealNetwork()
		_ = inner.Run(broken, &run)
		reached := seal.lift()
		events := scenario.take()
		if len(reached) > 0 {
			events = append(events, contract.RunEvent{Error: &contract.RunEventError{Type: "error", Message: "the adapter reached for http.DefaultTransport: " + strings.Join(reached, ", ")}})
		}
		out := &switchRun{Events: events}
		for _, call := range watch.admitted() {
			out.Invocations = append(out.Invocations, call.Request)
		}
		return out, nil
	}
	state, err := switchScenario(ctx, file, "agent", play, [2]string{"agent", "agent"})
	if err != nil {
		return nil, err
	}
	findings := switchFindings(state, "10 agent ", "agent", file.Tiers.Agent)
	reserve, err := switchReserve(ctx, host, with, inner, file)
	if err != nil {
		return nil, err
	}
	return append(findings, reserve...), nil
}

// switchReserve is the invocation held back for the critic (ADR-0010) on the agent capability: under
// auto a batch of three, which would take it, is refused and no child runs; a batch of two proceeds
// and the critic has its invocation, five in all; under off a batch of three proceeds, five in all
// and no critic.
func switchReserve(ctx context.Context, host AgentHost, with func(Adapter) Adapter, inner Adapter, file switchFile) ([]Finding, error) {
	respond := func(tasks int, critic string) ([]contract.RunEvent, []*Observed, error) {
		w, err := newAgentWorld(ctx, host, false)
		if err != nil {
			return nil, nil, err
		}
		batch := map[string]any{"tasks": file.Delegation.Tasks[:tasks]}
		proposal := sourcesSteps(file.Author["propose"])
		w.run(ctx, with, inner, func(request adapter.AgentRequest, _ int) AgentPlay {
			switch {
			case request.Phase == "critic":
				return AgentPlay{Steps: sourcesSteps(file.Critic)}
			case isChild(request):
				return AgentPlay{Steps: sourcesSteps([]sourcesTurn{file.Delegation.Child})}
			case isContinuation(request):
				return AgentPlay{Steps: proposal}
			}
			return AgentPlay{Steps: []AgentStep{call(turn.DelegateTool, batch)}}
		}, nil, nil, func(run *Run) {
			run.Review = nil
			run.Critic = critic
			run.Cases = file.Cases
			run.TestPrompt = file.TestPrompt
		})
		return w.take(), w.watch.admitted(), nil
	}
	f := &findings{check: "10 agent reserve"}
	count := func(calls []*Observed, kind string) int {
		n := 0
		for _, held := range calls {
			if describe(held.Request) == kind {
				n++
			}
		}
		return n
	}
	decided := func(events []contract.RunEvent) string {
		held := decisionsOf(events)
		if len(held) != 1 {
			return fmt.Sprintf("%d decisions", len(held))
		}
		return comparisonJSON(held[0])
	}
	events, calls, err := respond(3, "")
	if err != nil {
		return nil, err
	}
	if count(calls, "child") != 0 || count(calls, "critic") != 1 || len(calls) != 3 {
		f.fail("a batch of three under auto ran %v", kinds(calls))
	}
	if parents := slices.DeleteFunc(slices.Clone(calls), func(held *Observed) bool { return describe(held.Request) != "parent" }); len(parents) != 2 ||
		!strings.Contains(parents[1].Request.Prompt, "one kept for the critic; no task was started") {
		f.fail("the refused batch's continuation did not carry the refusal")
	}
	if got := decided(events); got != comparisonJSON(&contract.RunEventCriticDecided{Type: "critic_decided", Mode: "auto", Ran: true, Reason: "cases", Trigger: optionalString("cases"), Reasoning: optionalString("none")}) {
		f.fail("the refused batch's response decided %s", got)
	}
	events, calls, err = respond(2, "")
	if err != nil {
		return nil, err
	}
	if count(calls, "child") != 2 || count(calls, "critic") != 1 || len(calls) != 5 {
		f.fail("a batch of two under auto ran %v", kinds(calls))
	}
	if got := decided(events); got != comparisonJSON(&contract.RunEventCriticDecided{Type: "critic_decided", Mode: "auto", Ran: true, Reason: "cases", Trigger: optionalString("cases"), Reasoning: optionalString("none")}) {
		f.fail("the batch of two's response decided %s", got)
	}
	events, calls, err = respond(3, "off")
	if err != nil {
		return nil, err
	}
	if count(calls, "child") != 3 || count(calls, "critic") != 0 || len(calls) != 5 {
		f.fail("a batch of three under off ran %v", kinds(calls))
	}
	if got := decided(events); got != comparisonJSON(&contract.RunEventCriticDecided{Type: "critic_decided", Mode: "off", Ran: false, Reason: "off", Reasoning: optionalString("none")}) {
		f.fail("the batch of three under off's response decided %s", got)
	}
	return f.list, nil
}
