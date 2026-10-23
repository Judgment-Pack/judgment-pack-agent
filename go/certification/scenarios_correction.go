package certification

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strings"

	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

// testGuidance is the stand-in for the runtime's test_pack prompt the scenario hands the critic.
const testGuidance = "THE RUNTIME’S TEST_PACK GUIDANCE, AS PROMPTS/GET SERVED IT"

type correctionFile struct {
	Case struct {
		ID              string `json:"id"`
		ExpectedOutcome string `json:"expectedOutcome"`
	} `json:"case"`
}

type validation struct {
	Status      string `json:"status"`
	Diagnostics []struct {
		Code         string `json:"code"`
		InstancePath string `json:"instancePath"`
	} `json:"diagnostics"`
}

// corrected is what the correction loop left (Corrected in scenarios.test.ts).
type corrected struct {
	authors  [][]contract.RunEvent
	checks   []validation
	state    runcontext.CorrectionState
	count    int
	requests []EndpointRequest
	seen     []ServerObservation
}

// correctionLoop is the host's correction loop over the adapter (correctionLoop in scenarios.test.ts):
// one authoring run, the host's validate through the gate, one correction request with the critic
// and the declared case, and its validate.
func correctionLoop(ctx context.Context, server *Server, answerAs string, adapter Adapter, beforeCritic string) (*corrected, error) {
	var file correctionFile
	if err := ScenarioFile("correction-loop", &file); err != nil {
		return nil, err
	}
	scenario, err := server.Scenario(ctx)
	if err != nil {
		return nil, err
	}
	host, err := openScenario(ctx, server, ScenarioEndpoint{Scenario: "correction-loop", AnswerAs: answerAs, BeforeCritic: beforeCritic}, nil, false)
	if err != nil {
		return nil, err
	}
	declared := declaredCase(file.Case.ID, "declared", scenario.Documents["FACTS"], file.Case.ExpectedOutcome)
	out := &corrected{}
	author := func(ctx context.Context, prompt string, review bool) (json.RawMessage, error) {
		run := &Run{Prompt: prompt, TestPrompt: testGuidance, Critic: reviewMode(review)}
		if review {
			run.Cases = []json.RawMessage{declared}
		}
		ran, err := host.runOn(ctx, adapter, run)
		if err != nil {
			return nil, err
		}
		out.authors = append(out.authors, ran.Events)
		proposals := proposalsOf(ran.Events)
		if len(proposals) == 0 {
			return nil, &missingProposal{}
		}
		held, err := json.Marshal(proposals[0].Document)
		if err != nil {
			return nil, err
		}
		// A Go proposal event holds the document as a map, which keeps no member order; the
		// TypeScript host's JSON.stringify writes the order the model wrote. The host writes it back
		// in the order the scenario's own draft has, where it is that draft, so validate is asked
		// about the bytes the model proposed.
		for _, name := range []string{"DRAFT_V1", "DRAFT_V2"} {
			if sameJSON(held, scenario.Documents[name]) {
				return compactJSON(scenario.Documents[name]), nil
			}
		}
		return held, nil
	}
	first, err := author(ctx, scenario.Policy, false)
	if err != nil {
		return nil, err
	}
	findingsOf := func(check validation) string {
		var parts []string
		for _, one := range check.Diagnostics {
			parts = append(parts, one.Code+" at "+one.InstancePath)
		}
		return strings.Join(parts, "; ")
	}
	result, err := runcontext.CorrectArtifact(ctx, runcontext.CorrectionPorts[json.RawMessage, validation]{
		Initial:        first,
		MaxCorrections: 1,
		Fingerprint:    func(candidate json.RawMessage) string { return string(candidate) },
		Check: func(ctx context.Context, candidate json.RawMessage) (validation, error) {
			args, _ := json.Marshal(map[string]string{"document": string(candidate)})
			answered, err := host.gate.CallTool(ctx, "validate", args)
			if err != nil {
				return validation{}, err
			}
			var check validation
			_ = json.Unmarshal([]byte(answered.Text), &check)
			out.checks = append(out.checks, check)
			return check, nil
		},
		Passed: func(check validation) bool { return check.Status == "valid" },
		Correct: func(ctx context.Context, _ json.RawMessage, check validation) (json.RawMessage, bool, error) {
			next, err := author(ctx, scenario.Policy+"\n\nThe Runtime's validate refused your draft: "+findingsOf(check)+". Correct the draft.", true)
			return next, err == nil, err
		},
		Checkpoint: func(context.Context, json.RawMessage, validation, int) error { return nil },
	})
	if err != nil {
		return nil, err
	}
	out.state, out.count = result.State, result.Corrections
	observed, err := host.session.Observations(ctx)
	if err != nil {
		return nil, err
	}
	out.requests, out.seen = observed.Endpoint, observed.Seen
	return out, nil
}

type missingProposal struct{}

func (*missingProposal) Error() string { return "an authoring run delivered no proposal" }

func documentIs(events []contract.RunEvent, want json.RawMessage) bool {
	proposals := proposalsOf(events)
	if len(proposals) == 0 {
		return false
	}
	held, _ := json.Marshal(proposals[0].Document)
	return sameJSON(held, want)
}

// CorrectionLoop is scenario 5: a refused draft, one correction request, the critic's comparison;
// and the critic's boundary on the same loop with step T7 put first in the critic's script.
var CorrectionLoop = ScenarioCheck{
	Scenario: "correction-loop",
	Names:    []string{"5 first", "5 correction", "5 requests", "5 comparison", "5 complete", "5 boundary"},
	Run: func(ctx context.Context, server *Server, answerAs string, adapter Adapter) ([]Finding, error) {
		scenario, err := server.Scenario(ctx)
		if err != nil {
			return nil, err
		}
		got, err := correctionLoop(ctx, server, answerAs, adapter, "")
		if err != nil {
			return []Finding{{"5 first", err.Error()}}, nil
		}
		var out []Finding
		f := &findings{check: "5 first"}
		if len(got.authors) == 0 || !documentIs(got.authors[0], scenario.Documents["DRAFT_V1"]) {
			f.fail("the first proposal is not DRAFT_V1")
		}
		if len(got.checks) == 0 || got.checks[0].Status != "invalid" || len(got.checks[0].Diagnostics) != 1 ||
			got.checks[0].Diagnostics[0].Code != "JPS-SEMANTIC-UNRESOLVED-OUTCOME" || got.checks[0].Diagnostics[0].InstancePath != "/rules/3/outcome" {
			f.fail("the first check was %s", comparisonJSON(got.checks))
		}
		out = append(out, f.list...)
		f = &findings{check: "5 correction"}
		var statuses []string
		for _, check := range got.checks {
			statuses = append(statuses, check.Status)
		}
		if got.state != "ready" || got.count != 1 || !slices.Equal(statuses, []string{"invalid", "valid"}) || len(got.authors) < 2 || !documentIs(got.authors[1], scenario.Documents["DRAFT_V2"]) {
			f.fail("the loop ended %s after %d corrections, checks %v", got.state, got.count, statuses)
		}
		var authoring []string
		for _, request := range got.requests {
			if !strings.Contains(string(request.Body), RefutationMarker) {
				authoring = append(authoring, string(request.Body))
			}
		}
		if len(authoring) != 2 || strings.Contains(authoring[0], "JPS-SEMANTIC-UNRESOLVED-OUTCOME") || !strings.Contains(authoring[1], "JPS-SEMANTIC-UNRESOLVED-OUTCOME at /rules/3/outcome") {
			f.fail("the %d authoring requests did not carry the diagnostic on the correction only", len(authoring))
		}
		out = append(out, f.list...)
		f = &findings{check: "5 requests"}
		if len(got.requests) != 5 {
			f.fail("%d model requests, not two authoring and the critic's three", len(got.requests))
		}
		if refused := refusals(got.seen); len(refused) > 0 {
			f.fail("the runtime refused %v", refused)
		}
		for _, call := range got.seen {
			if call.Name == "experimental_evaluate" && call.Args["rehearsal"] != true {
				f.fail("an evaluate without rehearsal")
			}
		}
		out = append(out, f.list...)
		f = &findings{check: "5 comparison"}
		var critiques []*contract.RunEventCritique
		if len(got.authors) > 1 {
			critiques = critiquesOf(got.authors[1])
		}
		if len(critiques) != 1 {
			f.fail("%d critiques", len(critiques))
		} else {
			critique := critiques[0]
			var checks []string
			for _, check := range critique.Checks {
				checks = append(checks, check.Tool+" "+check.Status)
			}
			ok := !critique.Refuted && slices.Equal(checks, []string{"validate valid", "experimental_evaluate evaluated"}) && len(critique.Cases) == 1
			if ok {
				row := critique.Cases[0]
				actual, _ := json.Marshal(row.Actual)
				ok = row.Case == "receipted-120" && row.Source == "declared" && row.Tool == "experimental_evaluate" && row.Agrees &&
					strings.Contains(string(actual), `"kind":"outcome"`) && strings.Contains(string(actual), `"outcomeId":"approve"`)
			}
			if !ok {
				f.fail("the critique was %s", comparisonJSON(critique))
			}
		}
		out = append(out, f.list...)
		f = &findings{check: "5 complete"}
		if len(critiques) == 1 && len(critiques[0].Incomplete) > 0 {
			f.fail("incomplete %v", critiques[0].Incomplete)
		}
		if len(got.authors) > 1 {
			proposals := proposalsOf(got.authors[1])
			if len(proposals) == 0 || proposals[0].Critique == nil || proposals[0].Critique.Refuted || len(proposals[0].Critique.Incomplete) > 0 {
				f.fail("the proposal's critique is not refuted: false with nothing incomplete")
			}
		}
		for _, events := range got.authors {
			if errs := errorsOf(events); len(errs) > 0 {
				f.fail("errors: %s", errs[0].Message)
			}
		}
		out = append(out, f.list...)
		f = &findings{check: "5 boundary"}
		bounded, err := correctionLoop(ctx, server, answerAs, adapter, "T7")
		if err != nil {
			f.fail("%v", err)
			return append(out, f.list...), nil
		}
		if len(bounded.authors) < 2 {
			f.fail("one authoring run")
			return append(out, f.list...), nil
		}
		// The refusals only: this runner's host keeps the gate's own rewrite notice for the critic's
		// evaluate, which the TypeScript scenario's host drops (its connection's onEvent is empty).
		guards := guardrailsOf(bounded.authors[1], "refused")
		if len(guards) != 1 || guards[0].Tool != "write_file" || !strings.Contains(guards[0].Detail, "write_file is not one of the tools the critic may call") {
			f.fail("the guardrails were %s", comparisonJSON(guards))
		}
		if slices.Contains(seenNames(bounded.seen), "write_file") || len(critiquesOf(bounded.authors[1])) != 1 {
			f.fail("write_file reached the runtime, or the pass did not end in its critique")
		}
		for _, events := range bounded.authors {
			if errs := errorsOf(events); len(errs) > 0 {
				f.fail("errors: %s", errs[0].Message)
			}
		}
		return append(out, f.list...), nil
	},
}

// compactJSON is a JSON text without its insignificant white space, its members in their order.
func compactJSON(raw json.RawMessage) json.RawMessage {
	var out bytes.Buffer
	if err := json.Compact(&out, raw); err != nil {
		return raw
	}
	return out.Bytes()
}
