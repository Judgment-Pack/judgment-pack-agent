package certification

// Handwritten scripted providers prove delivered instructions and harness pause behavior,
// not general live model quality. No recommended option is supplied as a user answer.
import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
	"github.com/Judgment-Pack/judgment-pack-agent/go/relay"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

var askingWellNames = []string{"24 asking-well"}
var AgentAskingWellNames = []string{"24 agent asking-well"}

func askingWellFindings(ctx context.Context, inner Adapter, native bool) ([]Finding, error) {
	name := askingWellNames[0]
	if native {
		name = AgentAskingWellNames[0]
	}
	f := findings{check: name}
	root, err := RepositoryRoot()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(filepath.Join(root, "contract/testdata/asking-well/scenario.json"))
	if err != nil {
		return nil, err
	}
	var vector struct {
		OtherSkill      runcontext.Skill `json:"otherSkill"`
		Pin             string           `json:"pin"`
		Question        json.RawMessage  `json:"question"`
		ReadResult      string           `json:"readResult"`
		AvailableAnswer string           `json:"availableAnswer"`
	}
	if err = json.Unmarshal(raw, &vector); err != nil {
		return nil, err
	}
	for _, mode := range []string{"available", "missing", "plan", "replacement", "absent", "child", "capacity", "stale", "wrong-kind", "duplicate"} {
		events := []contract.RunEvent{}
		sent := []string{}
		reads, next := 0, 0
		actualAskOffered := false
		read := runcontext.HostTool{Name: "read_policy", Taxonomy: &runcontext.ToolTaxonomy{Operation: "read", Effect: "read"}, Effect: "read", Delegation: "read", Description: "Read available report policy.", InputSchema: json.RawMessage(`{"type":"object"}`), Execute: func(context.Context, json.RawMessage) (runcontext.ToolResult, error) {
			reads++
			return runcontext.ResultOf([]runcontext.Content{runcontext.TextContent(vector.ReadResult)}, nil), nil
		}}
		ask := runcontext.HostTool{Name: "ask_user", Taxonomy: &runcontext.ToolTaxonomy{Operation: "ask", Effect: "pure"}, Interaction: "question", Description: "Ask the person.", InputSchema: json.RawMessage(`{"type":"object"}`)}
		tools := []runcontext.HostTool{ask, read}
		if mode == "absent" || mode == "child" {
			tools = []runcontext.HostTool{read}
		}
		var skills []runcontext.Skill
		if mode == "capacity" {
			skills = []runcontext.Skill{vector.OtherSkill}
		}
		if mode == "replacement" || mode == "stale" || mode == "wrong-kind" || mode == "duplicate" {
			s := runcontext.AskingWellSkill()
			s.ID = "project-ask"
			s.Source = runcontext.SkillSource{Kind: "custom", ID: "project", Trust: "person", Version: "2"}
			s.Instructions = "Project method: read before asking."
			s.PinnedDigest = runcontext.SkillDigest(s, map[string]*runcontext.ToolTaxonomy{"ask_user": ask.Taxonomy})
			if mode == "stale" {
				s.PinnedDigest = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
			}
			if mode == "wrong-kind" {
				s.Kind = "knowledge"
			}
			skills = []runcontext.Skill{s}
			if mode == "duplicate" {
				skills = append(skills, s)
			}
		}
		steps := []memoryStep{{name: "load_skill", args: json.RawMessage(`{"name":"asking-well"}`)}}
		if mode == "available" {
			steps = append(steps, memoryStep{name: "read_policy", args: json.RawMessage(`{}`)}, memoryStep{say: vector.AvailableAnswer})
		} else {
			steps = append(steps, memoryStep{name: "ask_user", args: vector.Question}, memoryStep{say: "This must never become an answer."})
		}
		if mode == "absent" || mode == "child" {
			steps = []memoryStep{{say: "No question tool is available."}}
		}
		emit := func(e contract.RunEvent) { events = append(events, e) }
		runtime := func(_ context.Context, frame json.RawMessage) (json.RawMessage, error) {
			var req struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
			}
			if err := json.Unmarshal(frame, &req); err != nil {
				return nil, err
			}
			if req.Method == "notifications/initialized" {
				return nil, nil
			}
			result := any(map[string]any{})
			if req.Method == "tools/list" {
				result = map[string]any{"tools": []any{}}
			}
			return json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
		}
		g := gate.New(gate.Options{Runtime: runtime, Granted: gate.Ceiling, Emit: emit})
		hostOffer := []contract.ToolOfferHostTool{}
		for _, tool := range tools {
			hostOffer = append(hostOffer, tool.Declaration())
		}
		offer, err := g.Open(ctx, hostOffer)
		if err != nil {
			return nil, err
		}
		if mode == "plan" {
			g.Enter(gate.PlanPhase(offer))
		}
		if mode == "child" {
			g.Enter(gate.ChildPhase(contract.DelegatedTaskProfile{ID: "read-only", Capabilities: []string{"read"}}, offer, gate.LoopPhase(offer)))
		}
		input := &runcontext.Input{CurrentRequest: &runcontext.CurrentRequest{Message: "Prepare the report.", Input: "The report supports a policy review."}}
		if mode == "plan" {
			input.Phase = "plan"
		}
		if mode == "child" {
			input.Objective = "Read report policy."
		}
		run := Run{ModelName: "fixture", Wire: "openai-compatible", Thinking: "off", Prompt: "Prepare the report. Read available context before asking for missing policy.", AllowConversation: true, Interactive: true, Critic: "off", Gate: g.Calls(), Tools: offer.Tools, HostTools: tools, Skills: skills, Context: input, Emit: emit, EnrollContext: func(_ context.Context, offer adapter.ContextOffer) error {
			return g.EnrollContext(offer.ID, offer.HostTools)
		}}
		if mode == "capacity" {
			run.Policy = runcontext.PolicyLayer{"budget": map[string]any{"cataloguedSkills": 1}}
		}
		run.Model = func(_ context.Context, _ string, request relay.Request) (*http.Response, error) {
			sent = append(sent, string(request.Body))
			var body struct {
				Tools []struct {
					Function struct {
						Name string `json:"name"`
					} `json:"function"`
				} `json:"tools"`
			}
			_ = json.Unmarshal(request.Body, &body)
			for _, t := range body.Tools {
				if t.Function.Name == "ask_user" {
					actualAskOffered = true
				}
			}
			step := steps[len(steps)-1]
			if next < len(steps) {
				step = steps[next]
			}
			next++
			message := map[string]any{"role": "assistant", "content": step.say}
			reason := "stop"
			if step.name != "" {
				message["content"] = nil
				message["tool_calls"] = []any{map[string]any{"id": fmt.Sprint("call", next), "type": "function", "function": map[string]any{"name": step.name, "arguments": string(step.args)}}}
				reason = "tool_calls"
			}
			raw, _ := json.Marshal(map[string]any{"id": "answer", "object": "chat.completion", "choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": reason}}})
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
		}
		if native {
			run.Model = nil
			run.Agent = &adapter.Agent{Model: "fixture", Effort: "low", Run: func(ctx context.Context, r adapter.AgentRequest, c adapter.AgentCallbacks) error {
				sent = append(sent, r.Instructions)
				for _, t := range r.Tools {
					if strings.HasPrefix(t.Description, "ask_user:") {
						actualAskOffered = true
					}
				}
				for i, step := range steps {
					if step.name != "" {
						wireName := step.name
						for _, tool := range r.Tools {
							if strings.HasPrefix(tool.Description, step.name+":") {
								wireName = tool.Name
								break
							}
						}
						answer, err := c.Tool(ctx, adapter.AgentToolCall{ID: fmt.Sprint("call", i), Name: wireName, Arguments: step.args})
						if err != nil {
							return err
						}
						sent = append(sent, answer.Text)
					} else if err := c.Event(ctx, adapter.AgentEvent{Type: "message", ID: "answer", Text: step.say, Phase: "final"}); err != nil {
						return err
					}
				}
				return nil
			}}
		}
		err = inner.Run(ctx, &run)
		denied := mode == "capacity" || mode == "stale" || mode == "wrong-kind" || mode == "duplicate"
		if denied {
			if len(sent) != 0 {
				f.fail("%s reached provider before configuration refusal", mode)
			}
			configuration := false
			for _, e := range events {
				if e.Error != nil && e.Error.Failure != nil && e.Error.Failure.Code == "configuration" {
					configuration = true
				}
			}
			if !configuration && err == nil {
				f.fail("%s did not refuse configuration", mode)
			}
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", mode, err)
		}
		questions, messages, loads := 0, 0, 0
		for _, e := range events {
			if e.Error != nil {
				f.fail("%s error: %s", mode, e.Error.Message)
			}
			if e.UserQuestion != nil {
				questions++
				if string(mustJSON(e.UserQuestion.Question)) != string(mustCanonical(vector.Question)) {
					f.fail("%s question changed", mode)
				}
			}
			if e.Message != nil {
				messages++
				if mode == "available" && e.Message.Text != vector.AvailableAnswer {
					f.fail("available answer changed")
				}
			}
			if e.SkillLoaded != nil {
				loads++
				if e.SkillLoaded.Skill.Name != "asking-well" {
					f.fail("wrong method loaded")
				}
				if mode != "replacement" && e.SkillLoaded.Skill.Digest != vector.Pin {
					f.fail("actual current pin was not loaded")
				}
				trust := "first-party"
				if mode == "replacement" {
					trust = "person"
				}
				if e.SkillLoaded.Skill.Source.Trust != trust {
					f.fail("replacement provenance changed")
				}
			}
		}
		whole := strings.Join(sent, "\n")
		if mode == "absent" || mode == "child" {
			if strings.Contains(whole, "asking-well") || actualAskOffered || loads != 0 {
				f.fail("%s inserted absent method or question", mode)
			}
			continue
		}
		expectedInstructions := runcontext.AskingWellInstructions
		if mode == "replacement" {
			expectedInstructions = "Project method: read before asking."
		}
		if !strings.Contains(whole, expectedInstructions) {
			f.fail("%s actual loaded instructions not delivered", mode)
		}
		if loads != 1 {
			f.fail("%s expected one real skill load, got %d", mode, loads)
		}
		if mode == "available" {
			if reads != 1 || questions != 0 || messages != 1 {
				f.fail("available context must read once and ask zero times")
			}
		} else if questions != 1 || messages != 0 || reads != 0 {
			f.fail("%s recommendation became answer or did not await actual reply", mode)
		}
	}
	return f.list, nil
}
func mustCanonical(raw json.RawMessage) json.RawMessage {
	var x any
	_ = json.Unmarshal(raw, &x)
	out, _ := json.Marshal(x)
	return out
}
func mustJSON(v any) json.RawMessage { raw, _ := json.Marshal(v); return mustCanonical(raw) }

var AskingWell = ScenarioCheck{Scenario: "asking-well", Names: askingWellNames, Run: func(ctx context.Context, _ *Server, _ string, a Adapter) ([]Finding, error) {
	return askingWellFindings(ctx, a, false)
}}

func agentAskingWellChecks(ctx context.Context, _ *Server, _ AgentHost, _ func(Adapter) Adapter, a Adapter) ([]Finding, error) {
	return askingWellFindings(ctx, a, true)
}
