package eino_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/eino"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/turn"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
	"github.com/Judgment-Pack/judgment-pack-agent/go/relay"
)

func routeClassifierRun(store runcontext.RunStore) *adapter.Run {
	run := observationRun(store)
	run.Context.RunStore.ID = "classifier-barrier"
	run.Prompt = "Answer the fixed question."
	tier := "off"
	choice := runcontext.RouteSelection{Route: "agent", Tier: &tier, Skills: []string{}, Tools: []string{}, Operations: "all", Planning: "withheld"}
	run.Policy = runcontext.PolicyLayer{"version": 2, "routing": map[string]any{"mode": "auto", "tiers": []string{"off", "on"}, "classifier": map[string]any{"labels": map[string]any{"cheap": choice}, "maxOutputTokens": 32, "timeoutMilliseconds": 1000}}}
	return run
}
func routeCheckpoint(t *testing.T, store runcontext.RunStore) *contract.Checkpoint {
	t.Helper()
	head, err := store.Load(context.Background(), "classifier-barrier")
	if err != nil || head == nil {
		t.Fatalf("checkpoint absent: %v", err)
	}
	value, err := runcontext.ParseJSON(head.Checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := runcontext.ReadExecution(value)
	if err != nil || saved == nil {
		t.Fatalf("checkpoint invalid: %v", err)
	}
	return saved
}

func TestTurnRouteEinoClassifierKnownOutcomes(t *testing.T) {
	for _, outcome := range []string{"label", "unknown", "refusal", "final-author"} {
		t.Run(outcome, func(t *testing.T) {
			memory := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
			run := routeClassifierRun(memory)
			if outcome == "final-author" {
				one := 1
				run.Context.RequestCap = &one
			}
			bodies := []string{}
			run.Model = func(_ context.Context, _ string, r relay.Request) (*http.Response, error) {
				bodies = append(bodies, string(r.Body))
				if len(bodies) == 1 && outcome != "final-author" {
					if outcome == "refusal" {
						return &http.Response{StatusCode: 429, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"Rate limited","type":"rate_limit_error"}}`))}, nil
					}
					label := "cheap"
					if outcome == "unknown" {
						label = "not_declared"
					}
					return observationReply(`{"label":"`+label+`"}`, nil), nil
				}
				return observationReply("Done.", nil), nil
			}
			if err := eino.New().Run(context.Background(), run); err != nil {
				t.Fatal(err)
			}
			cp := routeCheckpoint(t, memory)
			want := 2
			if outcome == "final-author" {
				want = 1
			}
			if len(bodies) != want || cp.Used != int64(want) {
				t.Fatalf("physical/charged calls=%d/%d, want %d", len(bodies), cp.Used, want)
			}
			status := map[string]string{"label": "label", "unknown": "unknown-label", "refusal": "refused", "final-author": "unsupported"}[outcome]
			if cp.TurnRoute == nil || cp.TurnRoute.Classifier == nil || cp.TurnRoute.Classifier.Status != status {
				t.Fatalf("classifier outcome lost: %+v", cp.TurnRoute)
			}
			decided := "default"
			if outcome == "label" {
				decided = "classifier"
			}
			if cp.TurnRoute.DecidedBy != decided {
				t.Fatalf("wrong decision %s", cp.TurnRoute.DecidedBy)
			}
			if outcome == "final-author" {
				if strings.Contains(bodies[0], "choose_turn_route") {
					t.Fatal("classifier consumed final author allowance")
				}
				return
			}
			var body map[string]any
			json.Unmarshal([]byte(bodies[0]), &body)
			if body["max_tokens"] != float64(32) && body["max_completion_tokens"] != float64(32) {
				t.Fatal("classifier physical output ceiling lost")
			}
			if !strings.Contains(bodies[0], "choose_turn_route") || !strings.Contains(bodies[0], "cheap") {
				t.Fatal("closed classifier declaration absent")
			}
			if len(cp.Prompts) == 0 || cp.TurnRoute.Classifier.PromptDigest != "sha256:"+cp.Prompts[0].Sha256 {
				t.Fatal("actual classifier prompt identity lost")
			}
			if len(cp.Completions) == 0 || cp.Completions[0].Role == nil || *cp.Completions[0].Role != "classifier" {
				t.Fatal("classifier completion role lost")
			}
			found := false
			for _, row := range cp.Usage.Rows {
				if row.Role == "classifier" && row.Requests == 1 {
					found = true
					if outcome != "refusal" && (row.Tokens == nil || row.Tokens.InputTokens == nil || *row.Tokens.InputTokens != 12 || row.Tokens.OutputTokens == nil || *row.Tokens.OutputTokens != 4) {
						t.Fatal("classifier usage lost")
					}
				}
			}
			if !found {
				t.Fatal("classifier ledger row missing")
			}
		})
	}
}

type classifierAckStore struct {
	runcontext.RunStore
	fault string
	armed bool
}

func (s *classifierAckStore) Commit(ctx context.Context, value runcontext.RunCommit) (runcontext.RunReceipt, error) {
	receipt, err := s.RunStore.Commit(ctx, value)
	if err != nil {
		return receipt, err
	}
	var cp struct {
		Durable struct {
			Dispatches []struct {
				Scope struct{ Role string }
				State string
			}
		}
		UsageBudget struct{ Entries []any }
	}
	json.Unmarshal(value.Checkpoint, &cp)
	if s.fault == "pre-deadline" && s.armed && len(cp.UsageBudget.Entries) > 0 {
		s.armed = false
		<-ctx.Done()
		return receipt, ctx.Err()
	}
	match := s.fault == "journal" && len(cp.UsageBudget.Entries) > 0
	for _, d := range cp.Durable.Dispatches {
		if d.Scope.Role == "classifier" && (s.fault == "claim" && d.State == "claimed" || s.fault == "outcome" && d.State == "result") {
			match = true
		}
	}
	if s.armed && match {
		s.armed = false
		return receipt, errors.New("fixture lost classifier " + s.fault + " ack")
	}
	return receipt, nil
}
func TestTurnRouteEinoClassifierUncertaintyNeverDefaultsOrReplays(t *testing.T) {
	for _, fault := range []string{"reserve", "journal", "claim", "outcome", "transport", "pre-deadline", "timeout", "cancel"} {
		t.Run(fault, func(t *testing.T) {
			memory := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
			store := &classifierAckStore{RunStore: memory, fault: fault, armed: true}
			armed := true
			budget, err := runcontext.NewMemoryUsageBudgetStore("classifier-budget", runcontext.MemoryUsageBudgetOptions{AfterChange: func() error {
				if fault == "reserve" && armed {
					armed = false
					return errors.New("fixture lost classifier reserve ack")
				}
				return nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			sends := 0
			entered := make(chan struct{})
			var physicalDeadline time.Duration
			input := func(resume bool) *adapter.Run {
				run := routeClassifierRun(store)
				run.Context.RunStore.Resume = resume
				run.Context.UsageBudget = &runcontext.UsageBudgetInput{Store: budget, Scope: runcontext.UsageBudgetScope{Resource: "classifier-budget", ID: "shared", Revision: "one", Limits: runcontext.UsageAmounts{"modelRequests": 4}}}
				run.Model = func(callCtx context.Context, _ string, _ relay.Request) (*http.Response, error) {
					sends++
					if sends > 1 {
						return observationReply("UNEXPECTED DEFAULT", nil), nil
					}
					if fault == "transport" {
						return nil, errors.New("fixture unknown classifier transport")
					}
					if fault == "timeout" {
						close(entered)
						deadline, ok := callCtx.Deadline()
						if !ok {
							t.Fatal("classifier physical deadline missing")
						}
						physicalDeadline = time.Until(deadline)
						<-callCtx.Done()
						return nil, callCtx.Err()
					}
					if fault == "cancel" {
						cancel()
						<-callCtx.Done()
						return nil, callCtx.Err()
					}
					return observationReply(`{"label":"cheap"}`, nil), nil
				}
				if fault == "timeout" {
					run.Policy["routing"].(map[string]any)["classifier"].(map[string]any)["timeoutMilliseconds"] = 5000
				}
				return run
			}
			_ = eino.New().Run(ctx, input(false))
			physical := 1
			if fault == "reserve" || fault == "journal" || fault == "claim" || fault == "pre-deadline" {
				physical = 0
			}
			if sends != physical {
				t.Fatalf("uncertain classifier dispatched default: calls=%d, want %d", sends, physical)
			}
			if fault == "reserve" && armed || (fault == "journal" || fault == "claim" || fault == "outcome" || fault == "pre-deadline") && store.armed {
				t.Fatal("declared acknowledgement fault did not fire")
			}
			cp := routeCheckpoint(t, memory)
			if cp.TurnRoute == nil || cp.TurnRoute.Reason != "classifier-pending" {
				t.Fatalf("uncertainty became route decision: %+v", cp.TurnRoute)
			}
			before := len(cp.UsageBudget.Entries)
			beforeIDs := make([]string, before)
			for i, row := range cp.UsageBudget.Entries {
				beforeIDs[i] = row.Reservation.ID
			}
			_ = eino.New().Run(context.Background(), input(true))
			if sends != physical {
				t.Fatalf("explicit resume repeated physical classifier/default: calls=%d, want %d", sends, physical)
			}
			restored := routeCheckpoint(t, memory)
			if len(restored.UsageBudget.Entries) != before {
				t.Fatal("explicit resume reset classifier reservations")
			}
			for i, row := range restored.UsageBudget.Entries {
				if row.Reservation.ID != beforeIDs[i] {
					t.Fatal("explicit resume replaced classifier reservation identity")
				}
			}
			if fault == "timeout" {
				select {
				case <-entered:
				default:
					t.Fatal("post-dispatch timeout never entered provider")
				}
			}
			if fault == "timeout" && (physicalDeadline <= 0 || physicalDeadline > 5*time.Second) {
				t.Fatalf("physical classifier deadline unbounded: %s", physicalDeadline)
			}
		})
	}
}

func TestTurnRouteEinoDirectPlannerAndPhaseCeilings(t *testing.T) {
	for _, phase := range []string{"planner", "plan", "child"} {
		t.Run(phase, func(t *testing.T) {
			memory := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
			run := routeClassifierRun(memory)
			tier := "off"
			run.Route = &runcontext.RouteSelection{Route: "direct", Tier: &tier, Skills: []string{}, Tools: []string{}, Operations: "all", Planning: "withheld"}
			run.Policy["routing"] = map[string]any{"mode": "rules", "tiers": []string{"off", "on"}}
			run.Critic = "on"
			run.TestPrompt = "Check the artifact."
			if phase == "planner" {
				run.Critic = "off"
			}
			if phase == "planner" {
				budget, err := runcontext.NewMemoryUsageBudgetStore("planner-budget", runcontext.MemoryUsageBudgetOptions{})
				if err != nil {
					t.Fatal(err)
				}
				run.Context.UsageBudget = &runcontext.UsageBudgetInput{Store: budget, Scope: runcontext.UsageBudgetScope{Resource: "planner-budget", ID: "shared", Revision: "one", Limits: runcontext.UsageAmounts{"modelRequests": 4}}}
			}
			if phase == "plan" {
				run.Context.Phase = "plan"
			}
			if phase == "child" {
				run.Context.Objective = "Inspect only assigned evidence."
				two := 2
				run.Context.RequestCap = &two
			}
			executes := 0
			run.HostTools = []runcontext.HostTool{{Name: "lookup", Description: "Read one value.", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`), Effect: "read", Taxonomy: &runcontext.ToolTaxonomy{Operation: "read", Effect: "read"}, Execute: func(context.Context, json.RawMessage) (runcontext.ToolResult, error) {
				executes++
				return runcontext.ToolResult{}, nil
			}}, {Name: "external_write", Description: "Write an external value.", InputSchema: json.RawMessage(`{"type":"object"}`), Source: &runcontext.ToolSource{Kind: "gateway", ID: "route-actions", Trust: "connected", Version: "one", Identity: &contract.ToolOfferIdentity{Kind: "connection", Label: "route-actions"}}, Taxonomy: &runcontext.ToolTaxonomy{Operation: "act", Effect: "act"}, Execute: func(context.Context, json.RawMessage) (runcontext.ToolResult, error) {
				t.Fatal("route expanded Plan/child effect authority")
				return runcontext.ToolResult{}, nil
			}}}
			action := &run.HostTools[1]
			action.Source.ToolListDigest = runcontext.ToolListDigest([]runcontext.ListedTool{{Name: action.Name, Description: action.Description, InputSchema: action.InputSchema, Taxonomy: action.Taxonomy}})
			if phase == "child" {
				run.HostTools[0].Delegation = "read"
			}
			g := gate.New(gate.Options{})
			offer := contract.ToolOffer{Tools: []contract.ToolOfferRuntimeTool{}, HostTools: []contract.ToolOfferHostTool{run.HostTools[0].Declaration(), run.HostTools[1].Declaration()}}
			g.SetOffer(contract.ToolOffer{})
			if phase == "plan" {
				g.Enter(gate.PlanPhase(contract.ToolOffer{}))
			}
			if phase == "child" {
				// This is direct Eino admission of an already projected child input, not
				// delegated assembly. The real coordinator/profile path is also covered
				// by delegation.test.ts: "offers each profile exactly its tools ...".
				childPhase := gate.ChildPhase(turn.TaskProfiles[1], offer, gate.LoopPhase(offer))
				projected := childPhase.Offer(offer)
				hosts := []runcontext.HostTool{}
				for _, original := range run.HostTools {
					for _, allowed := range projected.HostTools {
						if original.Name == allowed.Name {
							hosts = append(hosts, original)
						}
					}
				}
				run.HostTools = hosts
				g.Enter(childPhase)
			}
			run.Gate = g.Calls()
			run.EnrollContext = func(_ context.Context, offered adapter.ContextOffer) error {
				return g.EnrollContext(offered.ID, offered.HostTools)
			}
			bodies := []string{}
			widened := 0
			failures := []contract.RunEvent{}
			run.Emit = func(e contract.RunEvent) {
				if e.RouteWidened != nil {
					widened++
				}
				if e.Error != nil {
					failures = append(failures, e)
				}
			}
			toolReply := func(name, args string) *http.Response {
				return observationReply("", []any{map[string]any{"index": 0, "id": "fixed-call", "type": "function", "function": map[string]any{"name": name, "arguments": args}}})
			}
			run.Model = func(_ context.Context, _ string, r relay.Request) (*http.Response, error) {
				bodies = append(bodies, string(r.Body))
				var body struct {
					Tools []struct{ Function struct{ Name string } }
				}
				json.Unmarshal(r.Body, &body)
				offered := map[string]bool{}
				for _, tool := range body.Tools {
					offered[tool.Function.Name] = true
				}
				if phase == "child" {
					if !offered["lookup"] || offered["delegate_tasks"] || offered["route-actions__external_write"] {
						t.Fatalf("child route changed admitted tools: %v", offered)
					}
					return observationReply("Done.", nil), nil
				}
				cp := routeCheckpoint(t, memory)
				if len(bodies) == 1 {
					if len(body.Tools) != 0 {
						t.Fatal("direct route offered tools before widening")
					}
					if len(cp.Plan) != 0 {
						t.Fatal("withheld planner ran before full admission")
					}
					if phase == "planner" {
						return toolReply("update_plan", `{"explanation":"Inspect in order.","plan":[{"step":"Inspect supplied evidence","status":"pending"}]}`), nil
					}
					return toolReply("lookup", "{}"), nil
				}
				if cp.TurnRoute == nil || cp.TurnRoute.Route != "agent" || !cp.TurnRoute.Widened {
					t.Fatal("fresh full request preceded acknowledged widening")
				}
				if phase == "plan" && offered["route-actions__external_write"] {
					t.Fatal("widening expanded Plan effect authority")
				}
				if len(bodies) == 2 {
					if phase == "planner" {
						if !offered["update_plan"] || len(cp.Plan) != 0 {
							t.Fatal("planner offer/state before fresh request invalid")
						}
						return toolReply("update_plan", `{"explanation":"Inspect in order.","plan":[{"step":"Inspect supplied evidence","status":"pending"}]}`), nil
					}
					return toolReply("route-actions__external_write", "{}"), nil
				}
				return observationReply("Done.", nil), nil
			}
			_ = eino.New().Run(context.Background(), run)
			if len(failures) != 0 {
				raw, _ := json.Marshal(failures)
				t.Fatalf("actual interaction failed: physical=%d widened=%d events=%s", len(bodies), widened, raw)
			}
			cp := routeCheckpoint(t, memory)
			if phase == "child" {
				if refused := g.Calls().AdmitHost("route-actions__external_write"); refused == nil || !refused.IsError {
					t.Fatal("routing lifted actual ChildPhase act refusal")
				}
				if len(bodies) != 1 || cp.TurnRoute != nil || cp.RequestCap == nil || *cp.RequestCap != 2 {
					t.Fatal("routing changed child request ceiling or route")
				}
				return
			}
			if len(bodies) != 3 || widened != 1 || cp.TurnRoute == nil || !cp.TurnRoute.Widened {
				t.Fatalf("widening not once-only: physical=%d events=%d", len(bodies), widened)
			}
			if phase == "planner" && len(cp.Plan) != 1 {
				t.Fatal("fresh admitted planner did not retain its plan")
			}
			if phase == "planner" {
				if cp.UsageBudget == nil || len(cp.UsageBudget.Entries) != 3 {
					t.Fatal("planner widening did not obtain fresh usage admissions")
				}
				seen := map[string]bool{}
				for _, row := range cp.UsageBudget.Entries {
					if seen[row.Reservation.ID] || row.Reservation.Role != "author" {
						t.Fatal("planner widening reused or changed reservation authority")
					}
					seen[row.Reservation.ID] = true
				}
			}
			if executes != 0 {
				t.Fatal("withheld tool executed on the narrow request")
			}
		})
	}
}
