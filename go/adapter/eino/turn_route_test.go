package eino_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/eino"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/relay"
)

func TestTurnRouteActualEinoResolvedAndClassifier(t *testing.T) {
	for _, mode := range []string{"host", "widen", "classifier"} {
		t.Run(mode, func(t *testing.T) {
			store := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
			run := observationRun(store)
			run.Context.RunStore.ID = "route-eino"
			run.Prompt = "Answer."
			tier := "off"
			choice := runcontext.RouteSelection{Route: "agent", Tier: &tier, Skills: []string{}, Tools: []string{}, Operations: "all", Planning: "withheld"}
			run.Route = &choice
			run.Policy = runcontext.PolicyLayer{"version": 2, "routing": map[string]any{"mode": "rules", "tiers": []string{"off", "on"}}}
			executes := 0
			run.HostTools = []runcontext.HostTool{{Name: "lookup", Description: "Read one value.", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`), Effect: "read", Taxonomy: &runcontext.ToolTaxonomy{Operation: "read", Effect: "read"}, Execute: func(context.Context, json.RawMessage) (runcontext.ToolResult, error) {
				executes++
				return runcontext.ToolResult{}, nil
			}}}
			if mode == "classifier" {
				run.Route = nil
				run.Policy["routing"] = map[string]any{"mode": "auto", "tiers": []string{"off", "on"}, "classifier": map[string]any{"labels": map[string]any{"cheap": choice}, "maxOutputTokens": 32, "timeoutMilliseconds": 1000}}
			}
			var failures []contract.RunEvent
			run.Emit = func(e contract.RunEvent) {
				if e.Error != nil {
					failures = append(failures, e)
				}
			}
			bodies := []string{}
			run.Model = func(_ context.Context, _ string, request relay.Request) (*http.Response, error) {
				bodies = append(bodies, string(request.Body))
				if len(bodies) == 1 {
					if mode == "classifier" {
						return observationReply(`{"label":"cheap"}`, nil), nil
					}
					if mode == "widen" {
						return observationReply("", []any{map[string]any{"index": 0, "id": "withheld", "type": "function", "function": map[string]any{"name": "lookup", "arguments": "{}"}}}), nil
					}
				}
				return observationReply("Done.", nil), nil
			}
			if err := eino.New().Run(context.Background(), run); err != nil {
				t.Fatal(err)
			}
			if len(failures) > 0 {
				raw, _ := json.Marshal(failures)
				t.Fatalf("actual route failed %s", raw)
			}
			want := 1
			if mode != "host" {
				want = 2
			}
			if len(bodies) != want || executes != 0 {
				t.Fatalf("physical/execute counts=%d/%d, wanted%d/0", len(bodies), executes, want)
			}
			if mode == "classifier" {
				var body map[string]any
				json.Unmarshal([]byte(bodies[0]), &body)
				if body["max_tokens"] != float64(32) && body["max_completion_tokens"] != float64(32) {
					t.Fatalf("classifier ceiling lost: %s", bodies[0])
				}
			} else if strings.Contains(bodies[0], "lookup") {
				t.Fatal("withheld tool reached first request")
			}
			if mode == "widen" && !strings.Contains(bodies[1], "lookup") {
				t.Fatal("widened full offer absent")
			}
			head, err := store.Load(context.Background(), "route-eino")
			if err != nil || head == nil {
				t.Fatal(err)
			}
			value, err := runcontext.ParseJSON(head.Checkpoint)
			if err != nil {
				t.Fatal(err)
			}
			saved, err := runcontext.ReadExecution(value)
			if err != nil || saved == nil || saved.Version != 3 || saved.TurnRoute == nil {
				t.Fatalf("missing admitted route %v", err)
			}
			if mode == "classifier" {
				if saved.TurnRoute.DecidedBy != "classifier" || saved.TurnRoute.Classifier == nil || saved.TurnRoute.Classifier.PromptDigest != "sha256:"+saved.Prompts[0].Sha256 {
					t.Fatalf("classifier identity not retained: %+v", saved.TurnRoute)
				}
				found := false
				for _, row := range saved.Usage.Rows {
					if row.Role == "classifier" && row.Requests == 1 && row.Tokens != nil && row.Tokens.InputTokens != nil && *row.Tokens.InputTokens == 12 {
						found = true
					}
				}
				if !found {
					t.Fatal("actual classifier usage line missing")
				}
			}
		})
	}
}

func TestTurnRouteEinoInvalidOutputCeilingNeverDispatches(t *testing.T) {
	for _, fixture := range []struct {
		name    string
		ceiling int
	}{{"negative", -1}, {"above-reserve", 1000000000}} {
		t.Run(fixture.name, func(t *testing.T) {
			store := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
			run := routeClassifierRun(store)
			run.Policy["budget"] = map[string]any{"outputTokens": 1024}
			run.RequestOutputCeiling = fixture.ceiling
			calls, ends := 0, 0
			var failures []*contract.RunEventError
			run.Emit = func(event contract.RunEvent) {
				if event.Error != nil {
					failures = append(failures, event.Error)
				}
				if event.End != nil {
					ends++
				}
			}
			run.Model = func(context.Context, string, relay.Request) (*http.Response, error) {
				calls++
				return observationReply(`{"label":"cheap"}`, nil), nil
			}
			if err := eino.New().Run(context.Background(), run); err != nil {
				t.Fatal(err)
			}
			const detail = "The physical output ceiling is outside the admitted output reserve."
			if len(failures) != 1 || failures[0].Failure == nil || failures[0].Failure.Code != "configuration" || failures[0].Failure.Source != "context" || failures[0].Failure.Detail == nil || *failures[0].Failure.Detail != detail || ends != 1 {
				t.Fatalf("missing output-ceiling configuration refusal or terminal event: failures=%+v ends=%d", failures, ends)
			}
			if calls != 0 {
				t.Fatalf("invalid output ceiling %d dispatched %d model requests before configuration refusal", fixture.ceiling, calls)
			}
			checkpoint := routeCheckpoint(t, store)
			if checkpoint.Used != 0 || checkpoint.Durable != nil && len(checkpoint.Durable.Dispatches) != 0 {
				t.Fatal("invalid output ceiling consumed a request or retained a physical dispatch claim")
			}
		})
	}
}
