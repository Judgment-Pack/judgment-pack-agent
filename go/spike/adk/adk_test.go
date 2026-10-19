// The ADK for Go spike (ADR-0004): Google's Agent Development Kit for Go, one llmagent under a
// runner with an in-memory session service, run on the certification's scenario leg against the
// shared scripted server, with the model as the host's relay capability (a model.LLM on the
// OpenAI-compatible wire) and every tool call put through the gate the run carries (go/gate). Not shipped; what it
// measured is in the ADR.
package adk_test

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"google.golang.org/adk/agent"
	"google.golang.org/adk/agent/llmagent"
	"google.golang.org/adk/model"
	"google.golang.org/adk/runner"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/adk/tool/toolconfirmation"
	"google.golang.org/genai"

	"github.com/Judgment-Pack/judgment-pack-agent/go/certification"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
	"github.com/Judgment-Pack/judgment-pack-agent/go/spike/internal/openai"
)

var server *certification.Server

func TestMain(m *testing.M) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	started, err := certification.StartServer(ctx)
	cancel()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	server = started
	code := m.Run()
	server.Close()
	os.Exit(code)
}

type measured struct {
	mu sync.Mutex
	// Calls the tools' own handlers answered, and those only the tool-error callback saw.
	viaHandler, viaErrorCallback []string
	// Tools whose declaration did not carry the served schema unchanged.
	schemaChanged []string
	// Function responses that came back wrapped in a map, as the model would be shown them.
	wrappedResponses []string
}

func (m *measured) note(list *[]string, name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	*list = append(*list, name)
}

// relayLLM is the model: the host's relay capability on the OpenAI-compatible wire, keyless.
type relayLLM struct {
	call   certification.ModelCall
	stream bool
	m      *measured
	served map[string]json.RawMessage
}

func (r *relayLLM) Name() string { return "scripted-model" }

func (r *relayLLM) GenerateContent(ctx context.Context, request *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		response, err := r.generate(ctx, request)
		yield(response, err)
	}
}

func (r *relayLLM) generate(ctx context.Context, request *model.LLMRequest) (*model.LLMResponse, error) {
	var tools []openai.Tool
	if request.Config != nil {
		for _, group := range request.Config.Tools {
			for _, declaration := range group.FunctionDeclarations {
				parameters, err := json.Marshal(declaration.ParametersJsonSchema)
				if err != nil {
					return nil, err
				}
				if served, ok := r.served[declaration.Name]; ok && !sameJSON(served, parameters) {
					r.m.note(&r.m.schemaChanged, declaration.Name)
				}
				tools = append(tools, openai.Tool{Type: "function", Function: openai.Declaration{Name: declaration.Name, Description: declaration.Description, Parameters: parameters}})
			}
		}
	}
	var messages []openai.Message
	if request.Config != nil && request.Config.SystemInstruction != nil {
		text := ""
		for _, part := range request.Config.SystemInstruction.Parts {
			text += part.Text
		}
		if text != "" {
			messages = append(messages, openai.Message{Role: "system", Content: openai.Text(text)})
		}
	}
	for _, content := range request.Contents {
		assistant := openai.Message{Role: "assistant"}
		for _, part := range content.Parts {
			switch {
			case part.FunctionCall != nil:
				args, _ := json.Marshal(part.FunctionCall.Args)
				assistant.ToolCalls = append(assistant.ToolCalls, openai.ToolCall{ID: part.FunctionCall.ID, Type: "function", Function: openai.Function{Name: part.FunctionCall.Name, Arguments: string(args)}})
			case part.FunctionResponse != nil:
				// ADK hands a tool's answer back as a map; the wire wants the text the tool gave.
				text, ok := part.FunctionResponse.Response["result"].(string)
				if !ok {
					encoded, _ := json.Marshal(part.FunctionResponse.Response)
					text = string(encoded)
				}
				r.m.note(&r.m.wrappedResponses, part.FunctionResponse.Name)
				messages = append(messages, openai.Message{Role: "tool", ToolCallID: part.FunctionResponse.ID, Content: openai.Text(text)})
			case part.Text != "":
				if content.Role == "model" {
					assistant.Content = openai.Text(part.Text)
				} else {
					messages = append(messages, openai.Message{Role: "user", Content: openai.Text(part.Text)})
				}
			}
		}
		if content.Role == "model" && (assistant.Content != nil || len(assistant.ToolCalls) > 0) {
			messages = append(messages, assistant)
		}
	}
	answer, err := openai.Complete(ctx, r.call, messages, tools, r.stream)
	if err != nil {
		return nil, err
	}
	out := &genai.Content{Role: "model"}
	if answer.Content != nil && *answer.Content != "" {
		out.Parts = append(out.Parts, &genai.Part{Text: *answer.Content})
	}
	for _, call := range answer.ToolCalls {
		var args map[string]any
		if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
			return nil, err
		}
		out.Parts = append(out.Parts, &genai.Part{FunctionCall: &genai.FunctionCall{ID: call.ID, Name: call.Function.Name, Args: args}})
	}
	return &model.LLMResponse{Content: out, TurnComplete: true}, nil
}

func sameJSON(a, b []byte) bool {
	var left, right any
	if json.Unmarshal(a, &left) != nil || json.Unmarshal(b, &right) != nil {
		return false
	}
	return reflect.DeepEqual(left, right)
}

type boundary struct {
	run *certification.Run
	m   *measured
}

func (b *boundary) dispatch(ctx context.Context, name string, args map[string]any) (map[string]any, error) {
	encoded, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	b.run.Emit(certification.ToolCall(name, encoded))
	var result gate.Result
	host := false
	for _, offered := range b.run.HostTools {
		host = host || offered.Name == name
	}
	if host {
		if refused := b.run.Gate.AdmitHost(name); refused != nil {
			result = *refused
		} else {
			result = gate.Result{Text: name + " has nothing to read in this run", IsError: true}
		}
	} else {
		result, err = b.run.Gate.CallTool(ctx, name, encoded)
	}
	if err != nil {
		return nil, err
	}
	b.run.Emit(certification.ToolResult(name, result))
	return map[string]any{"result": result.Text}, nil
}

func (b *boundary) tool(name, description string, raw json.RawMessage) (tool.Tool, error) {
	var parsed jsonschema.Schema
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, err
	}
	return functiontool.New(functiontool.Config{Name: name, Description: description, InputSchema: &parsed},
		func(ctx tool.Context, args map[string]any) (map[string]any, error) {
			b.m.note(&b.m.viaHandler, name)
			return b.dispatch(ctx, name, args)
		})
}

// unknown answers a name the agent has no tool for: ADK reports it to the tool-error callbacks.
func (b *boundary) unknown(ctx tool.Context, called tool.Tool, args map[string]any, _ error) (map[string]any, error) {
	b.m.note(&b.m.viaErrorCallback, called.Name())
	return b.dispatch(ctx, called.Name(), args)
}

func adkAdapter(stream bool, m *measured) certification.Adapter {
	return certification.AdapterFunc(func(ctx context.Context, run *certification.Run) error {
		served := run.Tools
		edge := &boundary{run: run, m: m}
		schemas := map[string]json.RawMessage{}
		var tools []tool.Tool
		for _, host := range run.HostTools {
			declared, err := edge.tool(host.Name, host.Description, host.InputSchema)
			if err != nil {
				return fmt.Errorf("host tool %s: %w", host.Name, err)
			}
			schemas[host.Name] = host.InputSchema
			tools = append(tools, declared)
		}
		for _, runtimeTool := range served {
			description := ""
			if runtimeTool.Description != nil {
				description = *runtimeTool.Description
			}
			declared, err := edge.tool(runtimeTool.Name, description, runtimeTool.InputSchema)
			if err != nil {
				return fmt.Errorf("runtime tool %s: %w", runtimeTool.Name, err)
			}
			schemas[runtimeTool.Name] = runtimeTool.InputSchema
			tools = append(tools, declared)
		}
		loop, err := llmagent.New(llmagent.Config{
			Name:                 "judgment_pack",
			Model:                &relayLLM{call: run.Model, stream: stream, m: m, served: schemas},
			Tools:                tools,
			OnToolErrorCallbacks: []llmagent.OnToolErrorCallback{edge.unknown},
		})
		if err != nil {
			return err
		}
		sessions := session.InMemoryService()
		created, err := sessions.Create(ctx, &session.CreateRequest{AppName: "judgment-pack", UserID: "person"})
		if err != nil {
			return err
		}
		r, err := runner.New(runner.Config{AppName: "judgment-pack", Agent: loop, SessionService: sessions})
		if err != nil {
			return err
		}
		final := ""
		for event, err := range r.Run(ctx, "person", created.Session.ID(), genai.NewContentFromText(run.Prompt, genai.RoleUser), agent.RunConfig{}) {
			if err != nil {
				return err
			}
			if event.Content != nil && event.Content.Role == "model" {
				for _, part := range event.Content.Parts {
					if part.Text != "" {
						final = part.Text
					}
				}
			}
		}
		run.Emit(certification.Message(final))
		if proposal := certification.Proposal(final); proposal != nil {
			run.Emit(contract.RunEvent{Proposal: proposal})
		}
		run.Emit(certification.End())
		return nil
	})
}

// TestADKScenarioLeg runs the scenario leg on the OpenAI-compatible wire, whole and streamed, and
// records what mapped.
func TestADKScenarioLeg(t *testing.T) {
	for _, answerAs := range []string{"whole", "stream"} {
		t.Run(answerAs, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			m := &measured{}
			result, err := certification.ScenarioLeg(ctx, server, certification.Endpoint{API: "openai-compatible", AnswerAs: answerAs}, adkAdapter(answerAs == "stream", m))
			if err != nil {
				t.Fatal(err)
			}
			for _, finding := range result.Checks() {
				t.Errorf("leg check: %s", finding)
			}
			t.Logf("tool calls through the tools' handlers: %v", m.viaHandler)
			t.Logf("tool calls only the tool-error callback saw: %v", m.viaErrorCallback)
			t.Logf("tools whose declared schema differs from the served one: %v", m.schemaChanged)
			t.Logf("function responses unwrapped from a map: %d", len(m.wrappedResponses))
			t.Logf("requests: %d", len(result.Observations.Requests))
			if !reflect.DeepEqual(m.viaErrorCallback, []string{"write_file"}) {
				t.Errorf("the tool-error callback saw %v", m.viaErrorCallback)
			}
		})
	}
}

// TestADKApprovalPause measures the person-in-the-loop pause: a tool that requires confirmation
// asks for it, the run ends with the request in the session, and a later turn carrying the
// confirmation runs the tool.
func TestADKApprovalPause(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	call, err := server.NewSession(ctx, certification.Endpoint{API: "openai-compatible", AnswerAs: "whole"})
	if err != nil {
		t.Fatal(err)
	}
	ran := false
	confirmed, err := functiontool.New(functiontool.Config{Name: "get_schema", Description: "needs an approval", RequireConfirmation: true},
		func(tool.Context, map[string]any) (map[string]any, error) {
			ran = true
			return map[string]any{"result": "approved and run"}, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	m := &measured{}
	loop, err := llmagent.New(llmagent.Config{Name: "judgment_pack", Model: &relayLLM{call: call.ModelCall(), m: m}, Tools: []tool.Tool{confirmed}})
	if err != nil {
		t.Fatal(err)
	}
	sessions := session.InMemoryService()
	created, err := sessions.Create(ctx, &session.CreateRequest{AppName: "judgment-pack", UserID: "person"})
	if err != nil {
		t.Fatal(err)
	}
	r, err := runner.New(runner.Config{AppName: "judgment-pack", Agent: loop, SessionService: sessions})
	if err != nil {
		t.Fatal(err)
	}
	confirmationID := ""
	for event, err := range r.Run(ctx, "person", created.Session.ID(), genai.NewContentFromText("policy", genai.RoleUser), agent.RunConfig{}) {
		if err != nil {
			t.Fatal(err)
		}
		if event.Content == nil {
			continue
		}
		for _, part := range event.Content.Parts {
			if part.FunctionCall != nil && part.FunctionCall.Name == toolconfirmation.FunctionCallName {
				confirmationID = part.FunctionCall.ID
			}
		}
	}
	if confirmationID == "" || ran {
		t.Fatalf("no confirmation asked before the tool ran (id %q, ran %v)", confirmationID, ran)
	}
	answer := &genai.Content{Role: string(genai.RoleUser), Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{
		Name: toolconfirmation.FunctionCallName, ID: confirmationID, Response: map[string]any{"confirmed": true},
	}}}}
	for _, err := range r.Run(ctx, "person", created.Session.ID(), answer, agent.RunConfig{}) {
		if err != nil {
			t.Logf("after the confirmation: %v", err)
			break
		}
	}
	if !ran {
		t.Fatal("the confirmed tool did not run")
	}
}

// fakeLLM answers from a script, in process: the probes below measure the framework's own
// dispatch, not a wire.
type fakeLLM struct {
	mu    sync.Mutex
	turns []*genai.Content
	n     int
}

func (f *fakeLLM) Name() string { return "probe" }

func (f *fakeLLM) GenerateContent(context.Context, *model.LLMRequest, bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		f.mu.Lock()
		turn := genai.NewContentFromText("done", genai.RoleModel)
		if f.n < len(f.turns) {
			turn = f.turns[f.n]
		}
		f.n++
		f.mu.Unlock()
		yield(&model.LLMResponse{Content: turn, TurnComplete: true}, nil)
	}
}

func runProbe(t *testing.T, llm model.LLM, tools []tool.Tool, onError []llmagent.OnToolErrorCallback) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	loop, err := llmagent.New(llmagent.Config{Name: "probe", Model: llm, Tools: tools, OnToolErrorCallbacks: onError})
	if err != nil {
		t.Fatal(err)
	}
	sessions := session.InMemoryService()
	created, err := sessions.Create(ctx, &session.CreateRequest{AppName: "probe", UserID: "person"})
	if err != nil {
		t.Fatal(err)
	}
	r, err := runner.New(runner.Config{AppName: "probe", Agent: loop, SessionService: sessions})
	if err != nil {
		t.Fatal(err)
	}
	for _, err := range r.Run(ctx, "person", created.Session.ID(), genai.NewContentFromText("go", genai.RoleUser), agent.RunConfig{}) {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func objectSchema(t *testing.T, raw string) *jsonschema.Schema {
	var parsed jsonschema.Schema
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatal(err)
	}
	return &parsed
}

// TestADKRunsOneTurnsCallsConcurrently measures how two calls in one model turn are run: ADK
// starts each in its own goroutine (internal/llminternal/base_flow.go), with no option to run
// them in order.
func TestADKRunsOneTurnsCallsConcurrently(t *testing.T) {
	var mu sync.Mutex
	running, most := 0, 0
	slow := func(tool.Context, map[string]any) (map[string]any, error) {
		mu.Lock()
		running++
		most = max(most, running)
		mu.Unlock()
		time.Sleep(50 * time.Millisecond)
		mu.Lock()
		running--
		mu.Unlock()
		return map[string]any{"result": "ok"}, nil
	}
	var tools []tool.Tool
	for _, name := range []string{"first", "second"} {
		declared, err := functiontool.New(functiontool.Config{Name: name, InputSchema: objectSchema(t, `{"type":"object"}`)}, slow)
		if err != nil {
			t.Fatal(err)
		}
		tools = append(tools, declared)
	}
	llm := &fakeLLM{turns: []*genai.Content{{Role: "model", Parts: []*genai.Part{
		{FunctionCall: &genai.FunctionCall{ID: "c1", Name: "first", Args: map[string]any{}}},
		{FunctionCall: &genai.FunctionCall{ID: "c2", Name: "second", Args: map[string]any{}}},
	}}}}
	runProbe(t, llm, tools, nil)
	t.Logf("calls of one turn running at once: %d", most)
	if most != 2 {
		t.Fatalf("measured %d at once; the ADR records 2", most)
	}
}

// TestADKValidatesArgumentsBeforeTheHandler measures where a call whose arguments do not fit the
// declared schema goes: functiontool converts them against the schema before the handler runs, so
// the handler (and a gate behind it) never sees the call; the tool-error callback does.
func TestADKValidatesArgumentsBeforeTheHandler(t *testing.T) {
	handled := false
	declared, err := functiontool.New(functiontool.Config{Name: "validate", InputSchema: objectSchema(t, `{"type":"object","properties":{"document":{"type":"string"}},"required":["document"]}`)},
		func(tool.Context, map[string]any) (map[string]any, error) {
			handled = true
			return map[string]any{"result": "ok"}, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	var reported []string
	onError := func(_ tool.Context, called tool.Tool, _ map[string]any, err error) (map[string]any, error) {
		reported = append(reported, called.Name()+": "+err.Error())
		return map[string]any{"result": "reported"}, nil
	}
	llm := &fakeLLM{turns: []*genai.Content{{Role: "model", Parts: []*genai.Part{
		{FunctionCall: &genai.FunctionCall{ID: "c1", Name: "validate", Args: map[string]any{"document": 5}}},
	}}}}
	runProbe(t, llm, []tool.Tool{declared}, []llmagent.OnToolErrorCallback{onError})
	t.Logf("handler ran: %v; reported to the tool-error callback: %v", handled, reported)
	if handled || len(reported) != 1 {
		t.Fatalf("handler ran %v, reported %v; the ADR records a call refused before the handler", handled, reported)
	}
}
