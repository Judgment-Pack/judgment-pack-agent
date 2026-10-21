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
	"strings"
	"testing"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/delegate"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/eino"
	"github.com/Judgment-Pack/judgment-pack-agent/go/certification"
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
	run.PolicyConnections = map[string]adapter.PolicyConnection{"backup": {Wire: "gemini", ModelName: "backup-model", Capabilities: &contract.CapabilityEndpointCapabilities{Tiers: &contract.CapabilityEndpointCapabilitiesTiers{Low: "1024", Medium: "2048", High: "4096"}}, Model: func(_ context.Context, suffix string, request relay.Request) (*http.Response, error) {
		calls++
		var body struct {
			GenerationConfig struct{ ThinkingConfig struct{ ThinkingBudget int } }
		}
		if err := json.Unmarshal(request.Body, &body); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(suffix, "models/backup-model:") || body.GenerationConfig.ThinkingConfig.ThinkingBudget != 4096 {
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
