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
	play := func(steps []memoryStep, input *runcontext.Input, critic bool) (memoryRun, error) {
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
		propose := runcontext.HostTool{Name: "propose_pack", Description: "Propose a pack.", Effect: "proposal", InputSchema: turn.ProposePackSchema, Execute: func(context.Context, json.RawMessage) (runcontext.ToolResult, error) {
			return runcontext.ToolResult{}, fmt.Errorf("host proposal executed")
		}}
		g := gate.New(gate.Options{Runtime: runtime, Granted: gate.Ceiling, Emit: emit})
		offer, err := g.Open(ctx, append([]contract.ToolOfferHostTool{{Name: "propose_pack", Description: "Propose a pack.", InputSchema: turn.ProposePackSchema}}, boundaryTools(true)...))
		if err != nil {
			return memoryRun{}, err
		}
		run := Run{ModelName: "fixture", Wire: "openai-compatible", Thinking: "off", Prompt: "Revise the draft.", TestPrompt: "Review the entire proposal.", AllowConversation: true, Interactive: true, Critic: "off", Gate: g.Calls(), Tools: offer.Tools, HostTools: []runcontext.HostTool{propose}, Context: input, Emit: emit}

		if critic {
			run.Critic = "on"
		}
		review := []memoryStep{{name: "propose_edit", args: json.RawMessage(`{"document":"pack","old_text":"Before","new_text":"After"}`)}, {say: "Reviewed the whole proposal."}}
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
				return nil
			}}
		}
		err = inner.Run(ctx, &run)
		return out, err
	}
	steps := []memoryStep{
		{name: "propose_pack", args: json.RawMessage(`{"document":{"specVersion":"1","id":"pack","title":"Before","notes":"same same"}}`)},
		{name: "search", args: json.RawMessage(`{"id":"draft:pack","pattern":"Before"}`)},
		{name: "read_window", args: json.RawMessage(`{"id":"draft:pack","offset":3,"limit":1}`)},
		{name: "propose_edit", args: json.RawMessage(`{"document":"pack","old_text":"Before","new_text":"After"}`)},
		{name: "read_window", args: json.RawMessage(`{"id":"draft:pack","offset":3,"limit":1}`)},
		{name: "propose_edit", args: json.RawMessage(`{"document":"pack","old_text":"same","new_text":"x"}`)},
		{name: "propose_delete", args: json.RawMessage(`{"document":"pack","old_text":"  \"specVersion\": \"1\",\n"}`)},
		{name: "propose_insert", args: json.RawMessage(`{"document":"pack","after_anchor":"After","text":" again"}`)},
		{name: "undo_edit", args: json.RawMessage(`{"document":"pack"}`)},
		{say: "The revised draft is ready."},
	}

	accepted, err := play([]memoryStep{steps[3], {say: "Two documents."}}, &runcontext.Input{AcceptedDocuments: []runcontext.AcceptedDocument{
		{ID: "pack", Document: json.RawMessage(`{"specVersion":"1","id":"pack","title":"Before","notes":"same same"}`)},
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
			expected := runcontext.MustParseJSON(`{"specVersion":"1","id":"pack","title":"After","notes":"same same"}`)
			edits, _ := json.Marshal(e.Proposal.Edits)
			proposed = runcontext.CanonicalJSON(actual) == runcontext.CanonicalJSON(expected) && string(edits) == `[{"type":"propose_edit","document":"pack","line":4,"bytes":5,"valid":true},{"type":"propose_insert","document":"pack","line":4,"bytes":6,"valid":true},{"type":"undo_edit","document":"pack","line":4,"bytes":0,"valid":true}]`
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
	continued, err := play([]memoryStep{{name: "propose_edit", args: json.RawMessage(`{"document":"pack","old_text":"After","new_text":"Continued"}`)}, {say: "Continued."}}, &runcontext.Input{Execution: saved}, false)
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
