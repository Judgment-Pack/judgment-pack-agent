package certification_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/certification"
	"github.com/Judgment-Pack/judgment-pack-agent/go/certification/standin"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
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

// The recorded runtime answers tools/list with the runtime's five, and records an unguarded
// evaluate as the refusal K3a measures.
func TestTheRecordedRuntimeRecordsWhatReachesIt(t *testing.T) {
	ctx := context30s(t)
	session, err := server.NewSession(ctx, certification.Endpoint{API: "openai-compatible", AnswerAs: "whole"})
	if err != nil {
		t.Fatal(err)
	}
	tools, err := standin.ListTools(ctx, session.RuntimeCall())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range tools {
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
	for _, check := range []string{"K1", "K3a", "K3b", "K3c", "sequence"} {
		if !failed[check] {
			t.Errorf("an idle adapter passed %s", check)
		}
	}
}

// The reference loop: a plain Go loop on the OpenAI-compatible wire, no framework, behind the
// stand-in gate. It passes every check of the leg, so a framework's spike that fails one has
// failed it in the framework.
func TestAReferenceLoopPassesTheLeg(t *testing.T) {
	ctx := context30s(t)
	result, err := certification.ScenarioLeg(ctx, server, certification.Endpoint{API: "openai-compatible", AnswerAs: "whole"}, certification.AdapterFunc(referenceLoop))
	if err != nil {
		t.Fatal(err)
	}
	for _, finding := range result.Checks() {
		t.Error(finding)
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
	tools, err := standin.ListTools(ctx, run.Runtime)
	if err != nil {
		return err
	}
	var declared []map[string]any
	var offered []string
	for _, tool := range run.HostTools {
		declared = append(declared, map[string]any{"type": "function", "function": map[string]any{"name": tool.Name, "description": tool.Description, "parameters": tool.InputSchema}})
	}
	for _, tool := range tools {
		offered = append(offered, tool.Name)
		declared = append(declared, map[string]any{"type": "function", "function": map[string]any{"name": tool.Name, "description": tool.Description, "parameters": tool.InputSchema}})
	}
	gate := &standin.Gate{Runtime: run.Runtime, Offered: offered, Emit: run.Emit}
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
			run.Emit(standin.Message(text))
			if proposal := standin.Proposal(text); proposal != nil {
				run.Emit(contract.RunEvent{Proposal: proposal})
			}
			run.Emit(standin.End())
			return nil
		}
		messages = append(messages, message)
		for _, call := range message.ToolCalls {
			args := json.RawMessage(call.Function.Arguments)
			run.Emit(standin.ToolCall(call.Function.Name, args))
			result, err := gate.Call(ctx, call.Function.Name, args)
			if err != nil {
				return err
			}
			run.Emit(standin.ToolResult(call.Function.Name, result))
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
