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

var outputBoundaryNames = []string{"17 output boundary"}
var AgentOutputBoundaryNames = []string{"17 agent output boundary"}

func outputBoundaryFindings(ctx context.Context, inner Adapter, agent bool) ([]Finding, error) {
	name := outputBoundaryNames[0]
	if agent {
		name = AgentOutputBoundaryNames[0]
	}
	f := findings{check: name}
	const key = "AKIAABCDEFGHIJKLMNOP"
	const hole = "[redacted: aws-access-key]"
	play := func(steps []memoryStep, input *runcontext.Input, policy runcontext.PolicyLayer, options ...bool) (memoryRun, error) {
		out := memoryRun{}
		runCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		var mu sync.Mutex
		if input == nil {
			input = &runcontext.Input{}
		}
		input.Checkpoint = func(_ context.Context, checkpoint contract.Checkpoint) error {
			for _, operation := range checkpoint.Operations {
				if strings.Contains(operation.Name, key) {
					f.fail("saved checkpoint leaked undeclared name")
				}
			}
			return nil
		}
		if len(options) > 1 && options[1] {
			input.Checkpoint = nil
		}
		input.CurrentRequest = &runcontext.CurrentRequest{Message: "Revise the draft.", Input: "Revise the draft."}
		emit := func(e contract.RunEvent) {
			mu.Lock()
			defer mu.Unlock()
			out.events = append(out.events, e)
			if len(options) > 0 && options[0] && e.ExecutionCheckpoint != nil && e.ExecutionCheckpoint.Checkpoint.OutputGuard["aws-access-key"] > 0 {
				cancel()
			}
		}
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
				if strings.Contains(text, key) {
					f.fail("key reached Runtime validate")
				}
				result = map[string]any{"structuredContent": map[string]any{"status": status, "key": key}}
			}
			return json.Marshal(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
		}
		propose := runcontext.HostTool{Name: "propose_pack", Description: "Propose a pack.", Effect: "proposal", InputSchema: turn.ProposePackSchema, Execute: func(context.Context, json.RawMessage) (runcontext.ToolResult, error) {
			return runcontext.ToolResult{}, fmt.Errorf("host proposal executed")
		}}
		g := gate.New(gate.Options{Runtime: runtime, Granted: gate.Ceiling, Emit: emit})
		hostOffer := []contract.ToolOfferHostTool{{Name: "propose_pack", Description: "Propose a pack.", InputSchema: turn.ProposePackSchema}, {Name: "ask_user", InputSchema: json.RawMessage(`{"type":"object"}`)}}
		if len(options) > 1 && options[1] {
			hostOffer = append(hostOffer, contract.ToolOfferHostTool{Name: key, InputSchema: json.RawMessage(`{"type":"object"}`)})
		}
		offer, err := g.Open(ctx, append(hostOffer, append(boundaryTools(true), runcontext.OfferedEditTools(runcontext.Session{Context: input})...)...))
		if err != nil {
			return memoryRun{}, err
		}
		run := Run{ModelName: "fixture", Wire: "openai-compatible", Thinking: "off", Prompt: "Revise the draft.", TestPrompt: "Review the entire proposal.", AllowConversation: true, Interactive: true, Critic: "off", Gate: g.Calls(), Tools: offer.Tools, HostTools: []runcontext.HostTool{propose, {Name: "ask_user", Description: "Ask the person.", Interaction: "question", InputSchema: json.RawMessage(`{"type":"object"}`)}}, Context: input, Emit: emit}

		if len(options) > 1 && options[1] {
			run.HostTools = append(run.HostTools, runcontext.HostTool{Name: key, Description: "Declared host tool.", InputSchema: json.RawMessage(`{"type":"object"}`), Execute: func(context.Context, json.RawMessage) (runcontext.ToolResult, error) {
				return runcontext.ToolResult{}, nil
			}})
		}
		run.Policy = policy
		parent := 0
		run.Model = func(_ context.Context, _ string, request relay.Request) (*http.Response, error) {
			out.systems = append(out.systems, string(request.Body))
			step := memoryStep{}
			if parent < len(steps) {
				step = steps[parent]
			}
			parent++

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
				for _, step := range given {
					if step.name != "" {
						wireName := step.name
						for _, t := range r.Tools {
							if strings.HasPrefix(t.Description, step.name+":") {
								wireName = t.Name
								break
							}
						}
						answer, err := c.Tool(ctx, adapter.AgentToolCall{ID: "call", Name: wireName, Arguments: step.args})
						if err != nil {
							return err
						}
						out.systems = append(out.systems, answer.Text)
					} else {
						if err := c.Event(ctx, adapter.AgentEvent{Type: "message", ID: "answer", Text: step.say, Phase: "final"}); err != nil {
							return err
						}
					}
				}
				return nil
			}}
		}
		err = inner.Run(runCtx, &run)
		if len(options) > 0 && options[0] && runCtx.Err() != nil {
			err = nil
		}
		return out, err
	}
	for _, mode := range []string{"redact", "refuse"} {
		var policy runcontext.PolicyLayer
		if mode == "refuse" {
			policy = runcontext.PolicyLayer{"outputGuard": mode}
		}
		r, err := play([]memoryStep{{name: key, args: json.RawMessage(`{}`)}, {say: "Finished."}}, nil, policy)
		if err != nil {
			return nil, err
		}
		noticeAt, callAt, resultAt := -1, -1, -1
		guarded, labelled := false, false
		for i, event := range r.events {
			clean := event
			if event.ToolResult != nil {
				if strings.Contains(event.ToolResult.Name, key) {
					f.fail("%s result identifier leaked", mode)
				}
				clean.ToolResult = nil // Tool-result payloads are exempt.
			}
			if event.ExecutionCheckpoint != nil {
				checkpoint := event.ExecutionCheckpoint.Checkpoint
				for _, operation := range checkpoint.Operations {
					if strings.Contains(operation.Name, key) {
						f.fail("%s checkpoint operation leaked", mode)
					}
				}
				checkpoint.Operations = nil // Check the remaining report fields independently.
				raw, _ := json.Marshal(checkpoint)
				value, _ := runcontext.ParseJSON(raw)
				// Retained operation results remain exempt, as the live result does.
				for _, entry := range value.Get("context").Get("entries").Items() {
					operation := entry.Get("value")
					if operation != nil && operation.Get("name") != nil && operation.Get("status") != nil {
						operation.Set("result", runcontext.Null())
					}
				}
				if strings.Contains(value.Text(), key) {
					f.fail("%s checkpoint report leaked", mode)
				}
				clean.ExecutionCheckpoint = nil
			}
			raw, _ := json.Marshal(clean)
			if strings.Contains(string(raw), key) {
				f.fail("%s delivered event leaked", mode)
			}

			if event.ToolCall != nil && callAt < 0 {
				callAt = i
			}
			if event.ToolResult != nil && resultAt < 0 {
				resultAt = i
			}
			if e := event.OutputGuarded; e != nil && e.Where == "message" && e.Pattern == "aws-access-key" && e.Action == mode {
				labelled = true
			}
			if e := event.Guardrail; e != nil {
				noticeAt = i
				if strings.Contains(e.Tool, key) || strings.Contains(e.Detail, key) {
					f.fail("%s gate notice leaked the tool name", mode)
				}
				if mode == "redact" {
					guarded = e.Tool == hole && strings.Contains(e.Detail, hole) && e.Action == "refused"
				} else {
					guarded = e.Reason != nil && *e.Reason == "output refused"
				}
			}
		}
		count := int64(3)
		if mode == "refuse" {
			count = 2
		}
		if !guarded || !labelled || r.saved() == nil || r.saved().OutputGuard["aws-access-key"] != count {
			f.fail("%s gate notice guard or count missing", mode)
		}
		if r.saved() == nil || len(r.saved().Operations) == 0 || r.saved().Operations[0].Status != "failed" {
			f.fail("%s unknown call was not refused", mode)
		}
		if callAt < 0 || noticeAt <= callAt || resultAt <= noticeAt {
			f.fail("%s gate notice ordering changed", mode)
		}
		declared, e := play([]memoryStep{{name: key, args: json.RawMessage(`{}`)}, {say: "Finished."}}, nil, policy, false, true)
		if e != nil {
			return nil, e
		}
		found := false
		for _, event := range declared.events {
			if event.ToolCall != nil && event.ToolCall.Name == key {
				found = true
			}
			if event.OutputGuarded != nil {
				f.fail("%s declared tool name was guarded", mode)
			}
		}
		if !found || declared.saved() == nil || len(declared.saved().Operations) == 0 || declared.saved().Operations[0].Name != key {
			f.fail("%s declared name changed", mode)
		}
		if !strings.Contains(strings.Join(r.systems, "\n"), key) {
			f.fail("%s gate refusal result changed", mode)
		}
	}
	steps := []memoryStep{
		{name: "validate", args: json.RawMessage(`{"content":"{\"specVersion\":\"1\"}"}`)},
		{name: "validate", args: json.RawMessage(`{"content":"{\"specVersion\":\"1\",\"title\":\"AKIAABCDEFGHIJKLMNOP\"}"}`)},
		{name: "remember", args: json.RawMessage(`{"kind":"fact","text":"AKIAABCDEFGHIJKLMNOP"}`)},
		{name: "propose_pack", args: json.RawMessage(`{"document":{"specVersion":"1","title":"AKIAABCDEFGHIJKLMNOP"}}`)},
		{say: "Held " + key},
	}
	ran, err := play(steps, nil, nil)
	if err != nil {
		return nil, err
	}
	if !strings.Contains(strings.Join(ran.systems, "\n"), key) {
		f.fail("guard changed what the model reads")
	}
	message, proposal, memory := false, false, false
	for _, e := range ran.events {
		if e.Message != nil && e.Message.Text == "Held "+hole {
			message = true
		}
		if e.Proposal != nil {
			raw, _ := json.Marshal(e.Proposal.Document)
			proposal = strings.Contains(string(raw), hole) && !strings.Contains(string(raw), key)
		}
		if e.MemoryProposed != nil && e.MemoryProposed.Memory.Text == hole {
			memory = true
		}
	}
	if !message || !proposal || !memory {
		f.fail("outbound paths message=%v proposal=%v memory=%v", message, proposal, memory)
	}
	if ran.saved() == nil || ran.saved().OutputGuard["aws-access-key"] != 4 {
		f.fail("guard count not recorded")
	} else {
		resumed, e := play([]memoryStep{{say: key}}, &runcontext.Input{Execution: ran.saved()}, nil)
		if e != nil {
			return nil, e
		}
		if resumed.saved() == nil || resumed.saved().OutputGuard["aws-access-key"] != 5 {
			f.fail("resume lost guard counts")
		}
	}
	interrupted, e := play([]memoryStep{{name: "remember", args: json.RawMessage(`{"kind":"fact","text":"AKIAABCDEFGHIJKLMNOP"}`)}, {say: "not reached"}}, nil, nil, true)
	if e != nil {
		return nil, e
	}
	if interrupted.saved() == nil || interrupted.saved().OutputGuard["aws-access-key"] != 1 {
		f.fail("interrupted count missing")
	} else {
		resumed, e := play([]memoryStep{{say: key}}, &runcontext.Input{Execution: interrupted.saved()}, nil)
		if e != nil {
			return nil, e
		}
		if resumed.saved() == nil || resumed.saved().OutputGuard["aws-access-key"] != 2 {
			f.fail("interrupted resume lost count")
		}
	}
	for _, mode := range []string{"refuse", "allow"} {
		policy := runcontext.PolicyLayer{"outputGuard": "refuse"}
		if mode == "allow" {
			policy = runcontext.PolicyLayer{"outputAllow": []any{"aws-access-key"}}
		}
		r, e := play([]memoryStep{{say: key}}, nil, policy)
		if e != nil {
			return nil, e
		}
		seen := false
		for _, event := range r.events {
			if mode == "refuse" && event.Guardrail != nil && event.Guardrail.Reason != nil && *event.Guardrail.Reason == "output refused" {
				seen = true
			}
			if mode == "allow" && event.Message != nil && event.Message.Text == key {
				seen = true
			}
		}
		if !seen {
			f.fail("%s not applied", mode)
		}
	}
	refusedCall, e := play([]memoryStep{{name: "validate", args: json.RawMessage(`{"content":"AKIAABCDEFGHIJKLMNOP"}`)}, {say: "Not sent."}}, nil, runcontext.PolicyLayer{"outputGuard": "refuse"})
	if e != nil {
		return nil, e
	}
	if refusedCall.saved() == nil || refusedCall.saved().OutputGuard["aws-access-key"] != 1 {
		f.fail("refused tool count missing")
	}
	question, e := play([]memoryStep{{name: "ask_user", args: json.RawMessage(`{"title":"Question","question":"AKIAABCDEFGHIJKLMNOP","selection":"single","options":[],"recommendedOptionId":null,"recommendation":null}`)}}, nil, nil)
	if e != nil {
		return nil, e
	}
	heldQuestion := false
	for _, event := range question.events {
		if event.UserQuestion != nil {
			raw, _ := json.Marshal(event.UserQuestion)
			heldQuestion = strings.Contains(string(raw), hole) && !strings.Contains(string(raw), key)
		}
	}
	if !heldQuestion {
		f.fail("question arguments bypassed guard")
	}
	draft, e := play([]memoryStep{{name: "propose_pack", args: json.RawMessage(`{"document":{"specVersion":"1","title":"before"}}`)}, {name: "propose_edit", args: json.RawMessage(`{"document":"document","old_text":"before","new_text":"AKIAABCDEFGHIJKLMNOP"}`)}, {say: "Edited."}}, nil, nil)
	if e != nil {
		return nil, e
	}
	clean := false
	for _, event := range draft.events {
		if event.Proposal != nil {
			raw, _ := json.Marshal(event.Proposal.Document)
			clean = strings.Contains(string(raw), hole) && !strings.Contains(string(raw), key)
		}
	}
	if !clean || draft.saved() == nil || draft.saved().OutputGuard["aws-access-key"] != 1 {
		f.fail("draft guard or count missing")
	}
	return f.list, nil
}

var OutputBoundary = ScenarioCheck{Scenario: "output-boundary", Names: outputBoundaryNames, Run: func(ctx context.Context, _ *Server, _ string, a Adapter) ([]Finding, error) {
	return outputBoundaryFindings(ctx, a, false)
}}

func agentOutputBoundaryChecks(ctx context.Context, _ *Server, _ AgentHost, _ func(Adapter) Adapter, a Adapter) ([]Finding, error) {
	return outputBoundaryFindings(ctx, a, true)
}
