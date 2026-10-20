package certification_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/certification"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
)

var server *certification.Server

func TestMain(m *testing.M) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	started, err := certification.StartServer(ctx)
	cancel()
	if err != nil {
		fmt.Fprintln(os.Stderr, "the scripted server did not start:", err)
		os.Exit(1)
	}
	server = started
	code := m.Run()
	server.Close()
	os.Exit(code)
}

func context30s(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// The relay capability writes the session once, passes only the allow-listed headers, and lands
// where the TypeScript legs land: the scripted model records what the host's capability sent.
func TestTheRelayCapabilityReachesTheScriptedModel(t *testing.T) {
	ctx := context30s(t)
	session, err := server.NewSession(ctx, certification.Endpoint{API: "openai-compatible", AnswerAs: "whole"})
	if err != nil {
		t.Fatal(err)
	}
	response, err := session.ModelCall()(ctx, "chat/completions", certification.ModelRequest{
		Headers: map[string]string{"Content-Type": "application/json", "X-Api-Key": "not-sent", "Authorization": "not-sent"},
		Body:    []byte(`{"model":"scripted-model","messages":[{"role":"user","content":"hello"}]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if !strings.Contains(string(body), `"name":"get_schema"`) {
		t.Fatalf("the first answer is not the scenario's first step: %s", body)
	}
	observed, err := session.Observations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(observed.Requests) != 1 {
		t.Fatalf("%d requests recorded", len(observed.Requests))
	}
	request := observed.Requests[0]
	if request.URL != certification.RelayPrefix+"/chat/completions" {
		t.Fatalf("the request went to %q", request.URL)
	}
	if !slices.Equal(request.HeaderNames, []string{"authorization", "content-type"}) {
		t.Fatalf("the request carried %v", request.HeaderNames)
	}
	if request.Step != "T1" {
		t.Fatalf("the request was answered with step %s", request.Step)
	}
	if _, err := session.ModelCall()(ctx, "../chat/completions", certification.ModelRequest{}); err == nil {
		t.Fatal("a dot segment reached the relay")
	}
	if _, err := session.ModelCall()(ctx, "chat/completions?key=x", certification.ModelRequest{}); err == nil {
		t.Fatal("a query reached the relay on the OpenAI-compatible wire")
	}
}

// The recorded runtime answers tools/list, through the gate, with the runtime's five, and records
// an evaluate sent past the gate as the refusal K3a measures.
func TestTheRecordedRuntimeRecordsWhatReachesIt(t *testing.T) {
	ctx := context30s(t)
	session, err := server.NewSession(ctx, certification.Endpoint{API: "openai-compatible", AnswerAs: "whole"})
	if err != nil {
		t.Fatal(err)
	}
	offer, err := gate.New(gate.Options{Runtime: gate.Call(session.RuntimeCall()), Granted: gate.Ceiling}).Open(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range offer.Tools {
		names = append(names, tool.Name)
	}
	if !slices.Equal(names, []string{"get_schema", "list_examples", "get_example", "validate", "experimental_evaluate"}) {
		t.Fatalf("tools/list served %v", names)
	}
	reply, err := session.RuntimeCall()(ctx, json.RawMessage(`{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"experimental_evaluate","arguments":{"pack":"{}"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(reply), `"error"`) {
		t.Fatalf("an unguarded evaluate was answered: %s", reply)
	}
	observed, err := session.Observations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(observed.Seen) != 1 || !strings.Contains(observed.Seen[0].Refusal, "without rehearsal: true") {
		t.Fatalf("the runtime recorded %+v", observed.Seen)
	}
}

// An adapter that does nothing fails every check the leg makes: the checks are not vacuous.
func TestAnIdleAdapterFailsTheLeg(t *testing.T) {
	ctx := context30s(t)
	idle := certification.AdapterFunc(func(context.Context, *certification.Run) error { return nil })
	result, err := certification.ScenarioLeg(ctx, server, certification.Endpoint{API: "openai-compatible", AnswerAs: "whole"}, idle)
	if err != nil {
		t.Fatal(err)
	}
	failed := map[string]bool{}
	for _, finding := range result.Checks() {
		failed[finding.Check] = true
	}
	for _, check := range []string{"scenario", "K1a", "K1", "K3a", "K3b", "K3c", "sequence", "steps"} {
		if !failed[check] {
			t.Errorf("an idle adapter passed %s", check)
		}
	}
}

// The reference loop: a plain Go loop on the OpenAI-compatible wire, no framework, every Runtime
// call through the gated call the run carries. It passes every check of the leg, plain and under
// the tracing variables, so a framework's adapter that fails one has failed it in the framework.
func TestAReferenceLoopPassesTheLeg(t *testing.T) {
	ctx := context30s(t)
	result, err := certification.ScenarioLeg(ctx, server, certification.Endpoint{API: "openai-compatible", AnswerAs: "whole"}, certification.AdapterFunc(referenceLoop))
	if err != nil {
		t.Fatal(err)
	}
	for _, finding := range result.Checks() {
		t.Error(finding)
	}
	traced, err := certification.ScenarioLegTraced(ctx, server, certification.Endpoint{API: "openai-compatible", AnswerAs: "whole"}, certification.AdapterFunc(referenceLoop))
	if err != nil {
		t.Fatal(err)
	}
	for _, finding := range traced.Checks() {
		t.Error("traced:", finding)
	}
}

// An adapter that sends the tracing key on a request fails K1b, and only on the traced leg.
func TestAKeySentUnderTracingFailsK1b(t *testing.T) {
	ctx := context30s(t)
	leaking := certification.AdapterFunc(func(ctx context.Context, run *certification.Run) error {
		run.Prompt += " " + os.Getenv("LANGSMITH_API_KEY")
		return referenceLoop(ctx, run)
	})
	traced, err := certification.ScenarioLegTraced(ctx, server, certification.Endpoint{API: "openai-compatible", AnswerAs: "whole"}, leaking)
	if err != nil {
		t.Fatal(err)
	}
	failed := map[string]bool{}
	for _, finding := range traced.Checks() {
		failed[finding.Check] = true
	}
	if !failed["K1b"] {
		t.Fatalf("the key reached the relay and K1b held: %v", traced.Checks())
	}
	if _, set := os.LookupEnv("LANGSMITH_API_KEY"); set {
		t.Fatal("the traced leg left its variables in the environment")
	}
}

// A loop that places none of the host's parts, and asks the host for none, fails the placement
// check (ADR-0005): the check is not vacuous.
func TestALoopThatDropsTheHostsPartsFailsThePlacement(t *testing.T) {
	ctx := context30s(t)
	findings, err := certification.PromptChecks[0].Run(ctx, server, certification.Endpoint{API: "openai-compatible", AnswerAs: "whole"}, certification.AdapterFunc(referenceLoop))
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) == 0 {
		t.Fatal("a loop with no system text passed the placement check")
	}
	for _, finding := range findings {
		if finding.Check != "parts" {
			t.Errorf("the finding is %s", finding)
		}
	}
}

type openAIMessage struct {
	Role       string           `json:"role"`
	Content    *string          `json:"content"`
	ToolCalls  []openAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
}

type openAIToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

func referenceLoop(ctx context.Context, run *certification.Run) error {
	var declared []map[string]any
	hosts := map[string]bool{}
	boundaryTools, err := certification.BoundaryHostTools()
	if err != nil {
		return err
	}
	for _, tool := range boundaryTools {
		hosts[tool.Name] = true
		declared = append(declared, map[string]any{"type": "function", "function": map[string]any{"name": tool.Name, "description": tool.Description, "parameters": tool.InputSchema}})
	}
	for _, tool := range run.Tools {
		declared = append(declared, map[string]any{"type": "function", "function": map[string]any{"name": tool.Name, "description": tool.Description, "parameters": tool.InputSchema}})
	}
	prompt := run.Prompt
	messages := []openAIMessage{{Role: "user", Content: &prompt}}
	for step := 0; step < 20; step++ {
		body, _ := json.Marshal(map[string]any{"model": "scripted-model", "messages": messages, "tools": declared, "stream": false})
		response, err := run.Model(ctx, "chat/completions", certification.ModelRequest{Headers: map[string]string{"content-type": "application/json", "accept": "application/json"}, Body: body})
		if err != nil {
			return err
		}
		raw, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil {
			return err
		}
		var answer struct {
			Choices []struct {
				Message openAIMessage `json:"message"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(raw, &answer); err != nil || len(answer.Choices) == 0 {
			return fmt.Errorf("an answer that is not a completion: %s", raw)
		}
		message := answer.Choices[0].Message
		if len(message.ToolCalls) == 0 {
			text := ""
			if message.Content != nil {
				text = *message.Content
			}
			run.Emit(certification.Message(text))
			if proposal := certification.Proposal(text); proposal != nil {
				run.Emit(contract.RunEvent{Proposal: proposal})
			}
			run.Emit(certification.End())
			return nil
		}
		messages = append(messages, message)
		for _, call := range message.ToolCalls {
			name, args := call.Function.Name, json.RawMessage(call.Function.Arguments)
			run.Emit(certification.ToolCall(name, args))
			var result gate.Result
			if hosts[name] {
				if refused := run.Gate.AdmitHost(name); refused != nil {
					result = *refused
				} else {
					result = gate.Result{Text: name + " has nothing to read in this run", IsError: true}
				}
			} else if result, err = run.Gate.CallTool(ctx, name, args); err != nil {
				return err
			}
			run.Emit(certification.ToolResult(name, result))
			text := result.Text
			messages = append(messages, openAIMessage{Role: "tool", ToolCallID: call.ID, Content: &text})
		}
	}
	return fmt.Errorf("the loop did not finish in 20 requests")
}

// An adapter that reaches for the process's default transport fails K1a, and the reach is
// refused: the relay capability is the only way out.
func TestADefaultTransportReachFailsK1a(t *testing.T) {
	ctx := context30s(t)
	reaching := certification.AdapterFunc(func(ctx context.Context, run *certification.Run) error {
		request, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://model.invalid/v1/chat/completions", nil)
		if response, err := http.DefaultClient.Do(request); err == nil {
			response.Body.Close()
			return fmt.Errorf("the default transport answered")
		}
		return referenceLoop(ctx, run)
	})
	result, err := certification.ScenarioLeg(ctx, server, certification.Endpoint{API: "openai-compatible", AnswerAs: "whole"}, reaching)
	if err != nil {
		t.Fatal(err)
	}
	var checks []string
	for _, finding := range result.Checks() {
		checks = append(checks, finding.Check)
	}
	if !slices.Equal(checks, []string{"K1a"}) {
		t.Fatalf("the reach was reported as %v", result.Checks())
	}
}

// The run carries the adapter's handle on the gate and nothing of the host's: no field of Run is the
// host's gate, and no field's type can set the offer, enter a phase or reach the transport.
var _ *gate.Calls = (&certification.Run{}).Gate

func TestTheRunCarriesOnlyTheAdapterHandle(t *testing.T) {
	run := reflect.TypeOf(certification.Run{})
	host := reflect.TypeOf(&gate.Gate{})
	for i := range run.NumField() {
		field := run.Field(i)
		if field.Type == host || field.Type == host.Elem() {
			t.Errorf("Run.%s is the host's gate", field.Name)
		}
		for _, method := range []string{"Enter", "SetOffer", "Transport", "Open"} {
			if _, ok := field.Type.MethodByName(method); ok {
				t.Errorf("Run.%s can %s", field.Name, method)
			}
		}
	}
	if field, ok := run.FieldByName("Gate"); !ok || field.Type != reflect.TypeOf(&gate.Calls{}) {
		t.Fatal("Run.Gate is not the adapter's handle")
	}
}
