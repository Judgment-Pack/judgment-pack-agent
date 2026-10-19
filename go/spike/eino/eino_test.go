// The Eino spike (ADR-0004): CloudWeGo Eino's ADK, one ChatModelAgent on its ReAct loop under a
// Runner, run on the certification's scenario leg against the shared scripted server, with the
// model as the host's relay capability and every tool call answered by one middleware that puts it
// through the gate the run carries (go/gate). Not shipped; what it measured is in the ADR.
package eino_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/eino-contrib/jsonschema"

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

// measured is what one run observed about the framework, beside the leg's own checks.
type measured struct {
	mu sync.Mutex
	// Tool calls the middleware's WrapInvokableToolCall answered, and those only the
	// UnknownToolsHandler saw.
	viaMiddleware, viaUnknownHandler []string
	// Runtime tools whose schema did not survive Eino's ToolInfo unchanged.
	schemaChanged []string
	// The tools Eino handed the model on each request, by name.
	requestTools [][]string
}

func (m *measured) note(list *[]string, name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	*list = append(*list, name)
}

// relayModel is the model: the host's relay capability on the OpenAI-compatible wire, keyless.
type relayModel struct {
	call   certification.ModelCall
	stream bool
	tools  []*schema.ToolInfo
	m      *measured
}

func (r *relayModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	copied := *r
	copied.tools = tools
	return &copied, nil
}

func (r *relayModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	tools := model.GetCommonOptions(&model.Options{Tools: r.tools}, opts...).Tools
	var declared []openai.Tool
	var names []string
	for _, info := range tools {
		parameters, err := info.ParamsOneOf.ToJSONSchema()
		if err != nil {
			return nil, err
		}
		raw, err := json.Marshal(parameters)
		if err != nil {
			return nil, err
		}
		declared = append(declared, openai.Tool{Type: "function", Function: openai.Declaration{Name: info.Name, Description: info.Desc, Parameters: raw}})
		names = append(names, info.Name)
	}
	r.m.mu.Lock()
	r.m.requestTools = append(r.m.requestTools, names)
	r.m.mu.Unlock()
	var messages []openai.Message
	for _, message := range input {
		out := openai.Message{Role: string(message.Role), ToolCallID: message.ToolCallID}
		if message.Content != "" || len(message.ToolCalls) == 0 {
			out.Content = openai.Text(message.Content)
		}
		for _, call := range message.ToolCalls {
			out.ToolCalls = append(out.ToolCalls, openai.ToolCall{ID: call.ID, Type: "function", Function: openai.Function{Name: call.Function.Name, Arguments: call.Function.Arguments}})
		}
		messages = append(messages, out)
	}
	answer, err := openai.Complete(ctx, r.call, messages, declared, r.stream)
	if err != nil {
		return nil, err
	}
	out := &schema.Message{Role: schema.Assistant}
	if answer.Content != nil {
		out.Content = *answer.Content
	}
	for _, call := range answer.ToolCalls {
		out.ToolCalls = append(out.ToolCalls, schema.ToolCall{ID: call.ID, Type: "function", Function: schema.FunctionCall{Name: call.Function.Name, Arguments: call.Function.Arguments}})
	}
	return out, nil
}

func (r *relayModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	message, err := r.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

// declared is a tool the model is shown and the framework never runs: the boundary answers it.
type declared struct{ info *schema.ToolInfo }

func (d *declared) Info(context.Context) (*schema.ToolInfo, error) { return d.info, nil }

func (d *declared) InvokableRun(context.Context, string, ...tool.Option) (string, error) {
	return "", errors.New("a declared tool was run past the boundary")
}

// boundary answers every tool call: a host tool by the host, anything else through the gate, with
// the contract's events around it.
type boundary struct {
	*adk.BaseChatModelAgentMiddleware
	run *certification.Run
	m   *measured
}

func (b *boundary) dispatch(ctx context.Context, name, arguments string) (string, error) {
	args := json.RawMessage(arguments)
	b.run.Emit(certification.ToolCall(name, args))
	var result gate.Result
	var err error
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
		result, err = b.run.Gate.CallTool(ctx, name, args)
	}
	if err != nil {
		return "", err
	}
	b.run.Emit(certification.ToolResult(name, result))
	return result.Text, nil
}

func (b *boundary) WrapInvokableToolCall(_ context.Context, _ adk.InvokableToolCallEndpoint, tCtx *adk.ToolContext) (adk.InvokableToolCallEndpoint, error) {
	return func(ctx context.Context, arguments string, _ ...tool.Option) (string, error) {
		b.m.note(&b.m.viaMiddleware, tCtx.Name)
		return b.dispatch(ctx, tCtx.Name, arguments)
	}, nil
}

func (b *boundary) unknown(ctx context.Context, name, input string) (string, error) {
	b.m.note(&b.m.viaUnknownHandler, name)
	return b.dispatch(ctx, name, input)
}

func toolInfo(name, description string, raw json.RawMessage, m *measured) (*schema.ToolInfo, error) {
	var parsed jsonschema.Schema
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, err
	}
	info := &schema.ToolInfo{Name: name, Desc: description, ParamsOneOf: schema.NewParamsOneOfByJSONSchema(&parsed)}
	back, err := info.ParamsOneOf.ToJSONSchema()
	if err != nil {
		return nil, err
	}
	encoded, _ := json.Marshal(back)
	var left, right any
	_ = json.Unmarshal(raw, &left)
	_ = json.Unmarshal(encoded, &right)
	if !reflect.DeepEqual(left, right) {
		m.note(&m.schemaChanged, name)
	}
	return info, nil
}

func einoAdapter(stream bool, m *measured) certification.Adapter {
	return certification.AdapterFunc(func(ctx context.Context, run *certification.Run) error {
		served := run.Tools
		var tools []tool.BaseTool
		for _, host := range run.HostTools {
			info, err := toolInfo(host.Name, host.Description, host.InputSchema, m)
			if err != nil {
				return err
			}
			tools = append(tools, &declared{info})
		}
		for _, runtimeTool := range served {
			info, err := toolInfo(runtimeTool.Name, derefString(runtimeTool.Description), runtimeTool.InputSchema, m)
			if err != nil {
				return err
			}
			tools = append(tools, &declared{info})
		}
		edge := &boundary{run: run, m: m}
		agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
			Name:        "judgment-pack",
			Description: "The Judgment Pack assistant's loop.",
			Model:       &relayModel{call: run.Model, stream: stream, m: m},
			ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools:               tools,
				UnknownToolsHandler: edge.unknown,
				ExecuteSequentially: true,
			}},
			Handlers:      []adk.ChatModelAgentMiddleware{edge},
			MaxIterations: 20,
		})
		if err != nil {
			return err
		}
		runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent})
		events := runner.Query(ctx, run.Prompt)
		final := ""
		for {
			event, ok := events.Next()
			if !ok {
				break
			}
			if event.Err != nil {
				return event.Err
			}
			if event.Output != nil && event.Output.MessageOutput != nil && event.Output.MessageOutput.Role == schema.Assistant {
				message, err := event.Output.MessageOutput.GetMessage()
				if err != nil {
					return err
				}
				if len(message.ToolCalls) == 0 {
					final = message.Content
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

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// TestEinoScenarioLeg runs the scenario leg on the OpenAI-compatible wire, whole and streamed, and
// records what mapped.
func TestEinoScenarioLeg(t *testing.T) {
	for _, answerAs := range []string{"whole", "stream"} {
		t.Run(answerAs, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			m := &measured{}
			result, err := certification.ScenarioLeg(ctx, server, certification.Endpoint{API: "openai-compatible", AnswerAs: answerAs}, einoAdapter(answerAs == "stream", m))
			if err != nil {
				t.Fatal(err)
			}
			for _, finding := range result.Checks() {
				t.Errorf("leg check: %s", finding)
			}
			t.Logf("tool calls through the middleware: %v", m.viaMiddleware)
			t.Logf("tool calls only the unknown-tools handler saw: %v", m.viaUnknownHandler)
			t.Logf("runtime and host tools whose schema Eino's ToolInfo changed: %v", m.schemaChanged)
			t.Logf("requests: %d", len(result.Observations.Requests))
			// What was measured, held so a change in the framework shows here.
			if !reflect.DeepEqual(m.viaUnknownHandler, []string{"write_file"}) {
				t.Errorf("the unknown-tools handler saw %v", m.viaUnknownHandler)
			}
			for _, name := range m.viaMiddleware {
				if name == "write_file" {
					t.Error("the middleware saw the unoffered name")
				}
			}
		})
	}
}

// approvalTool pauses for a person and runs only once resumed with an approval.
type approvalTool struct{ ran *bool }

func (a *approvalTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "get_schema", Desc: "needs an approval", ParamsOneOf: schema.NewParamsOneOfByJSONSchema(&jsonschema.Schema{Type: "object"})}, nil
}

func (a *approvalTool) InvokableRun(ctx context.Context, _ string, _ ...tool.Option) (string, error) {
	wasInterrupted, _, _ := tool.GetInterruptState[any](ctx)
	if !wasInterrupted {
		return "", tool.Interrupt(ctx, "approve get_schema?")
	}
	isTarget, hasData, approved := tool.GetResumeContext[bool](ctx)
	if !isTarget || !hasData || !approved {
		return "", tool.Interrupt(ctx, "approve get_schema?")
	}
	*a.ran = true
	return "approved and run", nil
}

type memoryStore struct {
	mu   sync.Mutex
	data map[string][]byte
}

func (s *memoryStore) Get(_ context.Context, id string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.data[id]
	return value, ok, nil
}

func (s *memoryStore) Set(_ context.Context, id string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[id] = value
	return nil
}

// TestEinoApprovalPause measures the person-in-the-loop pause: a tool interrupts, the run ends with
// the interrupt and a checkpoint, and a resume with the approval runs the tool and continues.
func TestEinoApprovalPause(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	session, err := server.NewSession(ctx, certification.Endpoint{API: "openai-compatible", AnswerAs: "whole"})
	if err != nil {
		t.Fatal(err)
	}
	ran := false
	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name: "judgment-pack", Description: "approval probe",
		Model:         &relayModel{call: session.ModelCall(), m: &measured{}},
		ToolsConfig:   adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{Tools: []tool.BaseTool{&approvalTool{ran: &ran}}}},
		MaxIterations: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	store := &memoryStore{data: map[string][]byte{}}
	runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent, CheckPointStore: store})
	events := runner.Query(ctx, "policy", adk.WithCheckPointID("run-1"))
	var interruptID string
	for {
		event, ok := events.Next()
		if !ok {
			break
		}
		if event.Action != nil && event.Action.Interrupted != nil {
			for _, point := range event.Action.Interrupted.InterruptContexts {
				if point.IsRootCause {
					interruptID = point.ID
				}
			}
		}
	}
	if interruptID == "" || ran {
		t.Fatalf("no interrupt before the tool ran (interrupt %q, ran %v)", interruptID, ran)
	}
	t.Logf("checkpoint bytes held by the store: %d", len(store.data["run-1"]))
	resumed, err := runner.ResumeWithParams(ctx, "run-1", &adk.ResumeParams{Targets: map[string]any{interruptID: true}})
	if err != nil {
		t.Fatal(err)
	}
	for {
		event, ok := resumed.Next()
		if !ok {
			break
		}
		if event.Err != nil && !errors.Is(event.Err, context.Canceled) {
			t.Logf("after resume: %v", event.Err)
		}
	}
	if !ran {
		t.Fatal("the resumed run did not run the approved tool")
	}
}

// fakeModel answers from a script, in process: the probes below measure the framework's own
// dispatch, not a wire.
type fakeModel struct {
	mu    sync.Mutex
	turns []*schema.Message
	n     int
}

func (f *fakeModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) { return f, nil }

func (f *fakeModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	turn := schema.AssistantMessage("done", nil)
	if f.n < len(f.turns) {
		turn = f.turns[f.n]
	}
	f.n++
	return turn, nil
}

func (f *fakeModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	message, err := f.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

type probeTool struct {
	name string
	run  func(context.Context, string) (string, error)
}

func (p *probeTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: p.name, ParamsOneOf: schema.NewParamsOneOfByJSONSchema(&jsonschema.Schema{Type: "object"})}, nil
}

func (p *probeTool) InvokableRun(ctx context.Context, arguments string, _ ...tool.Option) (string, error) {
	return p.run(ctx, arguments)
}

func runProbe(t *testing.T, turns []*schema.Message, tools []tool.BaseTool, sequential bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name: "probe", Description: "probe", Model: &fakeModel{turns: turns},
		ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{Tools: tools, ExecuteSequentially: sequential}},
	})
	if err != nil {
		t.Fatal(err)
	}
	events := adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent}).Query(ctx, "go")
	for {
		event, ok := events.Next()
		if !ok {
			return
		}
		if event.Err != nil {
			t.Fatal(event.Err)
		}
	}
}

// TestEinoRunsOneTurnsCallsInOrderWhenAsked measures how two calls in one model turn are run: in
// parallel by default, one after the other in the model's order with ExecuteSequentially.
func TestEinoRunsOneTurnsCallsInOrderWhenAsked(t *testing.T) {
	for _, sequential := range []bool{false, true} {
		var mu sync.Mutex
		running, most := 0, 0
		var order []string
		slow := func(name string) func(context.Context, string) (string, error) {
			return func(context.Context, string) (string, error) {
				mu.Lock()
				running++
				most = max(most, running)
				order = append(order, name)
				mu.Unlock()
				time.Sleep(50 * time.Millisecond)
				mu.Lock()
				running--
				mu.Unlock()
				return "ok", nil
			}
		}
		turns := []*schema.Message{schema.AssistantMessage("", []schema.ToolCall{
			{ID: "c1", Type: "function", Function: schema.FunctionCall{Name: "first", Arguments: "{}"}},
			{ID: "c2", Type: "function", Function: schema.FunctionCall{Name: "second", Arguments: "{}"}},
		})}
		runProbe(t, turns, []tool.BaseTool{&probeTool{"first", slow("first")}, &probeTool{"second", slow("second")}}, sequential)
		t.Logf("ExecuteSequentially %v: %d at once, started %v", sequential, most, order)
		want := 2
		if sequential {
			want = 1
		}
		if most != want || (sequential && !reflect.DeepEqual(order, []string{"first", "second"})) {
			t.Fatalf("ExecuteSequentially %v: %d at once, order %v", sequential, most, order)
		}
	}
}

// TestEinoHandsTheModelsArgumentsThrough measures what a tool is handed: the arguments as the
// model wrote them, byte for byte, with no schema check in between.
func TestEinoHandsTheModelsArgumentsThrough(t *testing.T) {
	written := `{"document": 5, "b":1,"a":2}`
	var received string
	turns := []*schema.Message{schema.AssistantMessage("", []schema.ToolCall{
		{ID: "c1", Type: "function", Function: schema.FunctionCall{Name: "validate", Arguments: written}},
	})}
	runProbe(t, turns, []tool.BaseTool{&probeTool{"validate", func(_ context.Context, arguments string) (string, error) {
		received = arguments
		return "ok", nil
	}}}, true)
	if received != written {
		t.Fatalf("the tool was handed %q, the model wrote %q", received, written)
	}
}
