package certification

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/turn"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
	"github.com/Judgment-Pack/judgment-pack-agent/go/relay"
)

var proposalEditNames = []string{"15 proposal edits"}
var AgentProposalEditNames = []string{"15 agent proposal edits"}

func proposalEditFindings(ctx context.Context, inner Adapter, agent bool) ([]Finding, error) {
	name := proposalEditNames[0]
	if agent {
		name = AgentProposalEditNames[0]
	}
	f := findings{check: name}
	vectorSteps := func(file string) ([]memoryStep, error) {
		_, items, err := ReadVector("edits/" + file)
		if err != nil {
			return nil, err
		}
		var out []memoryStep
		for _, item := range items {
			var call struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			if err := json.Unmarshal(item, &call); err != nil {
				return nil, err
			}
			out = append(out, memoryStep{name: call.Name, args: call.Arguments})
		}
		return out, nil
	}
	steps, err := vectorSteps("sequence.json")
	if err != nil {
		return nil, err
	}
	var initial struct {
		DocumentJson string `json:"documentJson"`
	}
	if err := json.Unmarshal(steps[0].args, &initial); err != nil {
		return nil, err
	}
	steps = append(steps, memoryStep{say: "The revised draft is ready."})
	editStep := steps[3]
	play := func(steps []memoryStep, input *runcontext.Input, critic bool, ending ...string) (memoryRun, error) {
		out := memoryRun{}
		var mu sync.Mutex
		if input == nil {
			input = &runcontext.Input{}
		}
		input.CurrentRequest = &runcontext.CurrentRequest{Message: "Revise the draft.", Input: "Revise the draft."}
		emit := func(e contract.RunEvent) { mu.Lock(); defer mu.Unlock(); out.events = append(out.events, e) }
		runtime := func(_ context.Context, frame json.RawMessage) (json.RawMessage, error) {
			var request struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
				Params struct {
					Name      string                     `json:"name"`
					Arguments map[string]json.RawMessage `json:"arguments"`
				} `json:"params"`
			}
			if err := json.Unmarshal(frame, &request); err != nil {
				return nil, err
			}
			if request.Method == "notifications/initialized" {
				return nil, nil
			}
			result := any(map[string]any{})
			if request.Method == "tools/list" {
				result = map[string]any{"tools": []any{map[string]any{"name": "validate", "description": "Validate the document.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"content": map[string]any{"type": "string"}}, "required": []string{"content"}}}}}
			}
			if request.Method == "tools/call" {
				var text string
				_ = json.Unmarshal(request.Params.Arguments["content"], &text)
				var doc map[string]any
				_ = json.Unmarshal([]byte(text), &doc)
				status := "invalid"
				if doc["specVersion"] == "1" {
					status = "valid"
				}
				result = map[string]any{"structuredContent": map[string]any{"status": status}}
			}
			return json.Marshal(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
		}
		propose := runcontext.HostTool{Name: "propose_pack", Taxonomy: &runcontext.ToolTaxonomy{Operation: "propose", Effect: "propose"}, Description: "Propose a pack.", Effect: "proposal", InputSchema: turn.ProposePackSchema, Execute: func(context.Context, json.RawMessage) (runcontext.ToolResult, error) {
			return runcontext.ToolResult{}, fmt.Errorf("host proposal executed")
		}}
		g := gate.New(gate.Options{Runtime: runtime, Granted: gate.Ceiling, Emit: emit})
		offer, err := g.Open(ctx, append([]contract.ToolOfferHostTool{{Name: "propose_pack", Taxonomy: contract.ToolTaxonomy{Operation: "propose", Effect: "propose"}, Description: "Propose a pack.", InputSchema: turn.ProposePackSchema}, {Name: "ask_user", Taxonomy: contract.ToolTaxonomy{Operation: "ask", Effect: "pure"}, InputSchema: json.RawMessage(`{"type":"object"}`)}}, append(boundaryTools(true), runcontext.OfferedEditTools(runcontext.Session{Context: input})...)...))
		if err != nil {
			return memoryRun{}, err
		}
		run := Run{ModelName: "fixture", Wire: "openai-compatible", Thinking: "off", Prompt: "Revise the draft.", TestPrompt: "Review the entire proposal.", AllowConversation: true, Interactive: true, Critic: "off", Gate: g.Calls(), Tools: offer.Tools, HostTools: []runcontext.HostTool{propose, {Name: "ask_user", Description: "Ask the person.", Interaction: "question", InputSchema: json.RawMessage(`{"type":"object"}`)}}, Context: input, Emit: emit}

		if critic {
			run.Critic = "on"
		}
		review := []memoryStep{editStep, {say: "Reviewed the whole proposal."}}
		parent, criticAt := 0, 0
		run.Model = func(_ context.Context, _ string, request relay.Request) (*http.Response, error) {
			system, _ := systemSplit("openai-compatible", request.Body)
			out.systems = append(out.systems, system)
			var step memoryStep
			if strings.Contains(string(request.Body), RefutationMarker) {
				if criticAt < len(review) {
					step = review[criticAt]
				}
				criticAt++
			} else {
				if parent >= len(steps) && len(ending) > 0 && ending[0] == "failure" {
					return nil, fmt.Errorf("Interrupted after edit")
				}
				if parent < len(steps) {
					step = steps[parent]
				}
				parent++
			}
			message := map[string]any{"role": "assistant", "content": step.say}
			reason := "stop"
			if step.name != "" {
				message["content"] = nil
				message["tool_calls"] = []any{map[string]any{"id": "call", "type": "function", "function": map[string]any{"name": step.name, "arguments": string(step.args)}}}
				reason = "tool_calls"
			}
			raw, _ := json.Marshal(map[string]any{"id": "answer", "object": "chat.completion", "choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": reason}}})
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
		}
		if agent {
			run.Model = nil
			run.Agent = &adapter.Agent{Model: "fixture", Effort: "low", Run: func(ctx context.Context, r adapter.AgentRequest, c adapter.AgentCallbacks) error {
				out.systems = append(out.systems, r.Instructions)
				given := steps
				if r.Phase == "critic" {
					given = review
				}
				for _, step := range given {
					if step.name != "" {
						wireName := step.name
						for _, t := range r.Tools {
							if strings.HasPrefix(t.Description, step.name+":") {
								wireName = t.Name
								break
							}
						}
						if _, err := c.Tool(ctx, adapter.AgentToolCall{ID: "call", Name: wireName, Arguments: step.args}); err != nil {
							return err
						}
					} else {
						if err := c.Event(ctx, adapter.AgentEvent{Type: "message", ID: "answer", Text: step.say, Phase: "final"}); err != nil {
							return err
						}
					}
				}
				if len(ending) > 0 && ending[0] == "failure" {
					return fmt.Errorf("Interrupted after edit")
				}
				return nil
			}}
		}
		err = inner.Run(ctx, &run)
		return out, err
	}
	accepted, err := play([]memoryStep{steps[3], {say: "Two documents."}}, &runcontext.Input{AcceptedDocuments: []runcontext.AcceptedDocument{
		{ID: "pack", Document: json.RawMessage(initial.DocumentJson)},
		{ID: "second", Document: json.RawMessage(`{"specVersion":"1","id":"second","title":"Second"}`)},
	}}, false)
	if err != nil {
		return nil, err
	}
	proposals := 0
	for _, e := range accepted.events {
		if e.Proposal != nil {
			proposals++
		}
	}
	if proposals != 2 {
		f.fail("the pack set lost a document")
	}
	for _, prefix := range accepted.systems {
		if strings.Contains(prefix, "PROPOSAL DRAFTS") {
			f.fail("draft placed in the stable prefix")
		}
	}
	for _, ending := range []string{"failure", "waiting"} {
		interruptedSteps := []memoryStep{steps[3]}
		if ending == "waiting" {
			interruptedSteps = append(interruptedSteps, memoryStep{name: "ask_user", args: json.RawMessage(`{"title":"Continue","question":"What next?","selection":"single","options":[],"recommendedOptionId":null,"recommendation":null}`)})
		}
		interrupted, err := play(interruptedSteps, &runcontext.Input{AcceptedDocuments: []runcontext.AcceptedDocument{{ID: "pack", Document: json.RawMessage(initial.DocumentJson)}, {ID: "second", Document: json.RawMessage(`{"specVersion":"1","id":"second","title":"Second"}`)}}}, false, ending)
		if err != nil {
			return nil, err
		}
		count, edited, waiting := 0, false, false
		for _, event := range interrupted.events {
			if event.UserQuestion != nil {
				waiting = true
			}
			if event.Proposal == nil {
				continue
			}
			count++
			if string(event.Proposal.Document["id"]) == `"pack"` {
				edited = string(event.Proposal.Document["title"]) == `"After"` && len(event.Proposal.Edits) == 1
			}
			if ending == "failure" && event.Proposal.Note == nil {
				f.fail("interrupted draft lacks note")
			}
		}
		if count != 2 || !edited || ending == "waiting" && !waiting {
			f.fail("%s lost draft set or edits: count=%d edited=%v waiting=%v", ending, count, edited, waiting)
		}
	}
	deleteInsert, err := vectorSteps("delete-insert.json")
	if err != nil {
		return nil, err
	}
	changed, err := play(append(deleteInsert, memoryStep{say: "Edited."}), &runcontext.Input{AcceptedDocuments: []runcontext.AcceptedDocument{{ID: "pack", Document: json.RawMessage(initial.DocumentJson)}}}, false)
	if err != nil {
		return nil, err
	}
	var expectedEdits []contract.ProvenanceEdit
	for _, file := range []string{"propose_delete-provenance.json", "propose_insert-provenance.json"} {
		_, items, err := ReadVector("edits/" + file)
		if err != nil {
			return nil, err
		}
		var edit contract.ProvenanceEdit
		if err := json.Unmarshal(items[0], &edit); err != nil {
			return nil, err
		}
		expectedEdits = append(expectedEdits, edit)
	}
	matched := false
	for _, event := range changed.events {
		if event.Proposal != nil {
			got, _ := json.Marshal(event.Proposal.Edits)
			want, _ := json.Marshal(expectedEdits)
			matched = string(got) == string(want) && string(event.Proposal.Document["title"]) == `"After!"`
		}
	}
	if !matched {
		f.fail("successful deletion/insertion differs from shared provenance")
	}

	ran, err := play(steps, nil, false)
	if err != nil {
		return nil, err
	}
	saved := ran.saved()
	if saved == nil || saved.Draft == nil {
		f.fail("no draft checkpoint: %+v", ran.events)
		return f.list, nil
	}
	applied, ambiguous, invalid, read, searched, proposed := 0, false, false, false, false, false
	readUpdated := false
	for _, e := range ran.events {
		if e.Error != nil {
			f.fail("run error: %+v", e.Error)
		}
		if e.EditApplied != nil {
			applied++
		}
		if e.EditRefused != nil {
			ambiguous = ambiguous || strings.Contains(e.EditRefused.Reason, "2 times")
			invalid = invalid || strings.Contains(e.EditRefused.Reason, "Validation failed")
		}
		if e.ToolResult != nil {
			read = read || (e.ToolResult.Name == "read_window" && strings.Contains(e.ToolResult.Text, "Before"))
			readUpdated = readUpdated || (e.ToolResult.Name == "read_window" && strings.Contains(e.ToolResult.Text, "After"))
			searched = searched || (e.ToolResult.Name == "search" && strings.Contains(e.ToolResult.Text, "draft:pack"))
		}
		if e.Proposal != nil {
			raw, _ := json.Marshal(e.Proposal.Document)
			actual, _ := runcontext.ParseJSON(raw)
			_, expectedItems, err := ReadVector("edits/proposal.json")
			if err != nil {
				return nil, err
			}
			expected := runcontext.MustParseJSON(string(expectedItems[0]))
			var expectedEdits []contract.ProvenanceEdit
			_ = json.Unmarshal([]byte(runcontext.CanonicalJSON(expected.Get("edits"))), &expectedEdits)
			for _, file := range []string{"propose_insert-provenance.json", "undo.json"} {
				_, items, err := ReadVector("edits/" + file)
				if err != nil {
					return nil, err
				}
				for _, raw := range items {
					var edit contract.ProvenanceEdit
					if err := json.Unmarshal(raw, &edit); err != nil {
						return nil, err
					}
					expectedEdits = append(expectedEdits, edit)
				}
			}
			edits, _ := json.Marshal(e.Proposal.Edits)
			want, _ := json.Marshal(expectedEdits)
			proposed = runcontext.CanonicalJSON(actual) == runcontext.CanonicalJSON(expected.Get("document")) && string(edits) == string(want)
		}
	}
	if !readUpdated {
		f.fail("the reader returned a cached draft after an edit")
	}
	if applied != 3 || !ambiguous || !invalid || !read || !searched || !proposed {
		f.fail("edit path: applied=%d ambiguous=%v invalid=%v read=%v search=%v proposal=%v", applied, ambiguous, invalid, read, searched, proposed)
	}
	for _, s := range ran.systems {
		if s != ran.systems[0] || strings.Contains(s, "PROPOSAL DRAFTS") {
			f.fail("stable prefix changed")
		}
	}
	resume, err := vectorSteps("resume.json")
	if err != nil {
		return nil, err
	}
	continued, err := play(append(resume, memoryStep{say: "Continued."}), &runcontext.Input{Execution: saved}, false)
	if err != nil {
		return nil, err
	}
	if cp := continued.saved(); cp == nil || cp.Draft == nil || len(cp.Draft.Documents[0].Edits) != 4 {
		f.fail("resume lost the draft or edits")
	}
	continuedProposal := false
	for _, e := range continued.events {
		if e.Proposal != nil && string(e.Proposal.Document["title"]) == `"Continued"` {
			continuedProposal = true
		}
	}
	if !continuedProposal {
		f.fail("resume did not emit the continued draft")
	}
	for _, child := range []bool{true, false} {
		var r memoryRun
		if child {
			r, err = play([]memoryStep{steps[3], {say: "Data only."}}, &runcontext.Input{Objective: "Read the draft."}, false)
		} else {
			r, err = play([]memoryStep{steps[0], {say: "Review this."}}, nil, true)
		}
		if err != nil {
			return nil, err
		}
		refused := false
		for _, e := range r.events {
			if e.Guardrail != nil && e.Guardrail.Reason != nil && *e.Guardrail.Reason == "edit refused" {
				refused = true
			}
			if e.EditApplied != nil {
				f.fail("child or critic edited")
			}
		}
		if !refused {
			f.fail("child=%v edit refusal missing", child)
		}
	}
	return f.list, nil
}

var ProposalEdits = ScenarioCheck{Scenario: "proposal-edits", Names: proposalEditNames, Run: func(ctx context.Context, _ *Server, _ string, a Adapter) ([]Finding, error) {
	return proposalEditFindings(ctx, a, false)
}}

func agentProposalEditChecks(ctx context.Context, _ *Server, _ AgentHost, _ func(Adapter) Adapter, a Adapter) ([]Finding, error) {
	return proposalEditFindings(ctx, a, true)
}
