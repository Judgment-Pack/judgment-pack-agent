package eino_test

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/relay"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/delegate"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/eino"
	"github.com/Judgment-Pack/judgment-pack-agent/go/certification"
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

func legContext(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// The engine leg on every wire and answer shape, plain and under the tracing variables: every check
// the TypeScript `engine <id>` leg makes.
func TestTheEngineLeg(t *testing.T) {
	for _, api := range []string{"openai-compatible", "anthropic", "gemini"} {
		for _, answerAs := range []string{"stream", "whole"} {
			t.Run(api+"/"+answerAs, func(t *testing.T) {
				endpoint := certification.Endpoint{API: api, AnswerAs: answerAs}
				result, err := certification.ScenarioLeg(legContext(t), server, endpoint, eino.New())
				if err != nil {
					t.Fatal(err)
				}
				for _, finding := range result.Checks() {
					t.Error(finding)
				}
				traced, err := certification.ScenarioLegTraced(legContext(t), server, endpoint, eino.New())
				if err != nil {
					t.Fatal(err)
				}
				for _, finding := range traced.Checks() {
					t.Error("traced:", finding)
				}
			})
		}
	}
}

func runChecks(t *testing.T, checks []certification.Check) {
	for _, check := range checks {
		for _, endpoint := range check.Legs {
			t.Run(check.Name+"/"+endpoint.API+"/"+endpoint.AnswerAs, func(t *testing.T) {
				found, err := check.Run(legContext(t), server, endpoint, eino.New())
				if err != nil {
					t.Fatal(err)
				}
				for _, finding := range found {
					t.Error(finding)
				}
			})
		}
	}
}

// The text tool calls block.
func TestTheTextPath(t *testing.T) { runChecks(t, certification.TextChecks) }

// The strict alternation block.
func TestTheStrictTurnOrder(t *testing.T) { runChecks(t, certification.StrictChecks) }

// The thinking block: the tier, the reasoning, the signatures and the critic, on every leg.
func TestTheThinkingLegs(t *testing.T) {
	runChecks(t, certification.ThinkingChecks)
	runChecks(t, certification.MoreThinkingChecks)
}

// The Gemini wire and the Anthropic dialects.
func TestTheWireLegs(t *testing.T) { runChecks(t, certification.WireChecks) }

func runScenarios(t *testing.T, scenarios ...certification.ScenarioCheck) {
	for _, scenario := range scenarios {
		for _, answerAs := range []string{"stream", "whole"} {
			t.Run(scenario.Scenario+"/"+answerAs, func(t *testing.T) {
				found, err := scenario.Run(legContext(t), server, answerAs, eino.New())
				if err != nil {
					t.Fatal(err)
				}
				for _, finding := range found {
					t.Error(finding)
				}
			})
		}
	}
}

// The certified scenarios.
func TestTheScenarios(t *testing.T) {
	runScenarios(t, certification.Scenarios...)
}

func withDelegation(inner certification.Adapter) certification.Adapter {
	return delegate.With(inner, delegate.Options{})
}

// The delegation scenario, on the adapter wrapped by the coordinator. Two children run at once on
// one ChatContext, so CI runs this package under -race too.
func TestTheDelegationScenario(t *testing.T) {
	runScenarios(t, certification.Delegation(withDelegation))
}

// Scenario 8 through the delegation coordinator, the path a continued run takes (ADR-0008).
func TestTheApprovalPauseThroughTheCoordinator(t *testing.T) {
	runScenarios(t, certification.ApprovalPauseWith(withDelegation))
}

// A batch runs as many children at once as the connection's answer says: one, two and three.
func TestTheDelegationCapacity(t *testing.T) {
	runScenarios(t, certification.CapacityAt(withDelegation, 1), certification.CapacityAt(withDelegation, 2), certification.CapacityAt(withDelegation, 3))
}

func TestFallbackRebuildsGeminiModelAndBudget(t *testing.T) {
	calls := 0
	var events []contract.RunEvent
	run := &adapter.Run{Wire: "gemini", ModelName: "primary-model", Thinking: "high", Prompt: "Answer briefly.", AllowConversation: true, Critic: "off", Context: &runcontext.Input{Connection: "primary"}, Policy: runcontext.PolicyLayer{"models": map[string]any{"author": map[string]any{"fallback": []any{"backup"}}}}, Emit: func(event contract.RunEvent) { events = append(events, event) }}
	run.Model = func(context.Context, string, relay.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 503, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	}
	run.PolicyConnections = map[string]adapter.PolicyConnection{"backup": {Wire: "gemini", ModelName: "backup/model!", Capabilities: &contract.CapabilityEndpointCapabilities{Tiers: &contract.CapabilityEndpointCapabilitiesTiers{Low: "1024", Medium: "2048", High: "4096"}}, Model: func(_ context.Context, suffix string, request relay.Request) (*http.Response, error) {
		calls++
		var body struct {
			GenerationConfig struct{ ThinkingConfig struct{ ThinkingBudget int } }
		}
		if err := json.Unmarshal(request.Body, &body); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(suffix, "models/backup%2Fmodel%21:") || body.GenerationConfig.ThinkingConfig.ThinkingBudget != 4096 {
			t.Errorf("backup settings: %s %s", suffix, request.Body)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"candidates":[{"content":{"role":"model","parts":[{"text":"Finished."}]},"finishReason":"STOP"}]}`))}, nil
	}}}
	if err := eino.New().Run(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("backup calls: %d", calls)
	}
	for _, event := range events {
		if event.Error != nil {
			t.Fatalf("run error: %+v", event.Error)
		}
	}
}

func TestDroppedModelReasoningRequestBytesSurviveInterruptAndResume(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "contract", "vectors", "providers", "reasoning-omission.json"))
	if err != nil {
		t.Fatal(err)
	}
	var vector struct {
		Dropped []struct {
			Name, Role, Capability, Raw string
			Supported                   []any
			Resume                      bool
			Record                      *int
		}
	}
	if err := json.Unmarshal(raw, &vector); err != nil {
		t.Fatal(err)
	}
	response := func(family, text string) *http.Response {
		var body any
		if family == "gemini" {
			body = map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"role": "model", "parts": []any{map[string]any{"text": text}}}, "finishReason": "STOP"}}}
		} else {
			body = map[string]any{"id": "response", "object": "chat.completion", "model": "connected", "choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": text}, "finish_reason": "stop"}}}
		}
		raw, _ := json.Marshal(body)
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(string(raw)))}
	}
	emptyRuntime := func(_ context.Context, frame json.RawMessage) (json.RawMessage, error) {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.Unmarshal(frame, &request); err != nil {
			return nil, err
		}
		if request.Method == "notifications/initialized" {
			return nil, nil
		}
		return json.RawMessage(`{"jsonrpc":"2.0","id":` + string(request.ID) + `,"result":{"tools":[]}}`), nil
	}
	for _, row := range vector.Dropped {
		if row.Capability != "model" || !row.Resume {
			continue
		}
		t.Run(row.Name, func(t *testing.T) {
			family := "openai-compatible"
			if row.Record != nil && *row.Record == 1 {
				family = "gemini"
			}
			runOnce := func(saved *contract.Checkpoint, interrupt bool) ([][]byte, []contract.RunEvent) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				var bodies [][]byte
				var events []contract.RunEvent
				emit := func(event contract.RunEvent) {
					events = append(events, event)
					if interrupt && event.ExecutionCheckpoint != nil && event.ExecutionCheckpoint.Checkpoint.Status == "running" && len(bodies) == 0 {
						cancel()
					}
				}
				g := gate.New(gate.Options{Runtime: emptyRuntime, Granted: gate.Ceiling, Emit: emit})
				offer, openErr := g.Open(context.Background(), nil)
				if openErr != nil {
					t.Fatal(openErr)
				}
				calls := 0
				model := func(modelContext context.Context, _ string, request relay.Request) (*http.Response, error) {
					if err := modelContext.Err(); err != nil {
						return nil, err
					}
					calls++
					bodies = append(bodies, append([]byte(nil), request.Body...))
					text := "Done."
					if row.Role == "critic" && calls == 1 {
						text = "```json\n{\"proposal\":{\"document\":{\"id\":\"pack\"},\"unknowns\":[]}}\n```"
					} else if row.Role == "critic" {
						text = "No counterexample found."
					}
					return response(family, text), nil
				}
				policy := runcontext.PolicyLayer{"models": map[string]any{row.Role: map[string]any{"discovered": map[string]any{"reasoning": map[string]any{"supported": row.Supported}}}}}
				critic, testPrompt := "off", ""
				if row.Role == "critic" {
					critic, testPrompt = "on", "Refute it."
				}
				run := &adapter.Run{Wire: family, ModelName: "connected", Model: model, Thinking: row.Raw, Prompt: "Draft a pack.", TestPrompt: testPrompt, Critic: critic, Policy: policy, Gate: g.Calls(), Tools: offer.Tools, Emit: emit, AllowConversation: row.Role != "critic", Context: &runcontext.Input{CurrentRequest: &runcontext.CurrentRequest{Message: "Draft a pack.", Input: "Draft a pack."}, Execution: saved}}
				if runErr := eino.New().Run(ctx, run); runErr != nil {
					t.Fatal(runErr)
				}
				return bodies, events
			}
			baseline, _ := runOnce(nil, false)
			_, events := runOnce(nil, true)
			var saved *contract.Checkpoint
			for _, event := range events {
				if event.ExecutionCheckpoint != nil && event.ExecutionCheckpoint.Checkpoint.Status == "stopped" {
					checkpoint := event.ExecutionCheckpoint.Checkpoint
					saved = &checkpoint
				}
			}
			if saved == nil {
				t.Fatal("interrupted run emitted no stopped checkpoint")
			}
			resumed, _ := runOnce(saved, false)
			target := 0
			if row.Role == "critic" {
				target = 1
			}
			if len(baseline) <= target || len(resumed) <= target || string(baseline[target]) != string(resumed[target]) {
				t.Fatalf("target request changed across resume:\n%s\n%s", baseline[target], resumed)
			}
		})
	}
}
