package plantests

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	native "github.com/Judgment-Pack/judgment-pack-agent/go/adapter/agent"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
)

func nativePlanCurrentInput(t *testing.T, store *nativePlanBasicStore, held *runcontext.Input) *runcontext.Input {
	t.Helper()
	head, err := store.Load(context.Background(), "native-proof")
	if err != nil || head == nil {
		t.Fatal("missing retained native head", err)
	}
	var checkpoint contract.Checkpoint
	if err := json.Unmarshal(head.Checkpoint, &checkpoint); err != nil {
		t.Fatal(err)
	}
	resumed := *held
	resumed.Execution = &checkpoint
	resumed.Approval = nil
	return &resumed
}

func TestNativePlanActualPausedFixtureBaseline(t *testing.T) {
	store, run, held, expected, calls := nativePlanPausedFixture(t, "")
	prepared, events, err := runcontext.PreparePlanDecision(context.Background(), held, expected)
	if err != nil || len(events) != 1 || *calls != 1 {
		t.Fatalf("unfaulted native decision failed: err=%v events=%+v calls=%d", err, events, *calls)
	}
	if events := nativePlanResume(t, run, prepared); nativePlanHasError(events) || *calls != 2 {
		t.Fatalf("unfaulted native continuation failed: calls=%d events=%+v", *calls, events)
	}
	_ = nativePlanCurrentInput(t, store, held)
}

func nativePlanResume(t *testing.T, run *adapter.Run, input *runcontext.Input) []contract.RunEvent {
	t.Helper()
	g := gate.New(gate.Options{})
	g.SetOffer(contract.ToolOffer{})
	run.Context, run.Gate = input, g.Calls()
	run.EnrollContext = func(_ context.Context, offer adapter.ContextOffer) error {
		return g.EnrollContext(offer.ID, offer.HostTools)
	}
	var events []contract.RunEvent
	run.Emit = func(event contract.RunEvent) { events = append(events, event) }
	if err := native.New().Run(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	return events
}

func nativePlanHasError(events []contract.RunEvent) bool {
	for _, event := range events {
		if event.Error != nil {
			return true
		}
	}
	return false
}

func TestNativePlanActualLostDecisionACK(t *testing.T) {
	store, run, held, expected, calls := nativePlanPausedFixture(t, "")
	base, commits := store.RunStore, 0
	store.commit = func(ctx context.Context, input runcontext.RunCommit) (runcontext.RunReceipt, error) {
		decision := false
		for _, raw := range input.Events {
			var envelope runcontext.StoredRunEnvelope
			if err := json.Unmarshal(raw, &envelope); err != nil {
				t.Fatal(err)
			}
			decision = decision || envelope.Event.PlanDriftDecided != nil
		}
		receipt, err := base.Commit(ctx, input)
		if decision && err == nil {
			commits++
			return runcontext.RunReceipt{}, errors.New("fixture lost decision acknowledgement")
		}
		return receipt, err
	}
	_, _, err := runcontext.PreparePlanDecision(context.Background(), held, expected)
	var unknown *runcontext.StorageOutcomeUnknown
	if !errors.As(err, &unknown) || commits != 1 || *calls != 1 {
		t.Fatalf("lost decision ACK did not retain uncertainty: %v commits=%d calls=%d", err, commits, *calls)
	}
	if _, _, err = runcontext.PreparePlanDecision(context.Background(), held, expected); err == nil || commits != 1 || *calls != 1 {
		t.Fatal("uncertain native decision automatically replayed")
	}
	head, err := store.Load(context.Background(), "native-proof")
	if err != nil {
		t.Fatal(err)
	}
	value := runcontext.MustParseJSON(string(head.Checkpoint))
	if value.Get("status").Str() != "running" || value.Get("planState").Get("decisions").Len() != 1 {
		t.Fatal("native decision missing from retained head")
	}
	reconciled := nativePlanCurrentInput(t, store, held)
	if events := nativePlanResume(t, run, reconciled); nativePlanHasError(events) || *calls != 2 || commits != 1 {
		t.Fatalf("native retained-read reconciliation failed: calls=%d commits=%d events=%+v", *calls, commits, events)
	}
	page, err := store.ReadEvents(context.Background(), "native-proof", "", runcontext.StoragePageLimit)
	if err != nil {
		t.Fatal(err)
	}
	decisions := 0
	for _, item := range page.Items {
		var envelope runcontext.StoredRunEnvelope
		if err := json.Unmarshal(item.Bytes, &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.Event.PlanDriftDecided != nil {
			decisions++
		}
	}
	if decisions != 1 {
		t.Fatalf("native lost-ACK durable decision count=%d", decisions)
	}
}

func TestNativePlanActualDecidedJournalRefusals(t *testing.T) {
	for _, fault := range []string{"missing-pause", "missing-decision", "wrong-request"} {
		t.Run(fault, func(t *testing.T) {
			store, run, held, expected, calls := nativePlanPausedFixture(t, "")
			prepared, _, err := runcontext.PreparePlanDecision(context.Background(), held, expected)
			if err != nil {
				t.Fatal(err)
			}
			if events := nativePlanResume(t, run, prepared); nativePlanHasError(events) || *calls != 2 {
				t.Fatalf("valid decided continuation failed: calls=%d events=%+v", *calls, events)
			}
			base := store.RunStore
			store.read = func(ctx context.Context, id, after string, limit int) (runcontext.RunEventPage, error) {
				page, err := base.ReadEvents(ctx, id, after, limit)
				if err != nil {
					return page, err
				}
				items := []runcontext.StoredRunEvent{}
				for _, item := range page.Items {
					var envelope map[string]any
					if err := json.Unmarshal(item.Bytes, &envelope); err != nil {
						t.Fatal(err)
					}
					event := envelope["event"].(map[string]any)
					if fault == "missing-decision" && event["type"] == "plan_drift_decided" {
						continue
					}
					hasPause := false
					if checkpoint, ok := event["checkpoint"].(map[string]any); ok {
						if durable, ok := checkpoint["durable"].(map[string]any); ok {
							for _, raw := range durable["dispatches"].([]any) {
								row := raw.(map[string]any)
								if row["planPause"] != nil {
									hasPause = true
									if fault == "wrong-request" {
										row["requestDigest"] = "sha256:" + strings.Repeat("a", 64)
									}
								}
							}
						}
					}
					if fault == "missing-pause" && hasPause {
						continue
					}
					item.Bytes, _ = json.Marshal(envelope)
					items = append(items, item)
				}
				page.Items = items
				return page, nil
			}
			resumed := nativePlanCurrentInput(t, store, held)
			if events := nativePlanResume(t, run, resumed); !nativePlanHasError(events) || *calls != 2 {
				t.Fatalf("incomplete decided proof restored native exemption: calls=%d events=%+v", *calls, events)
			}
		})
	}
}

func TestNativePlanActualNextUnknownRefuses(t *testing.T) {
	store, run, held, expected, calls := nativePlanPausedFixture(t, "")
	prepared, _, err := runcontext.PreparePlanDecision(context.Background(), held, expected)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runcontext.PrepareDurableRequest(prepared.Persistence, "agent", "fixture", "native", []byte(`{}`)); err != nil {
		t.Fatal("exact original unknown was not exempt", err)
	}
	run.Agent.Run = func(context.Context, adapter.AgentRequest, adapter.AgentCallbacks) error {
		*calls++
		return errors.New("unrelated next provider unknown")
	}
	if events := nativePlanResume(t, run, prepared); !nativePlanHasError(events) || *calls != 2 {
		t.Fatalf("next provider did not retain unknown outcome: calls=%d", *calls)
	}
	head, err := store.Load(context.Background(), "native-proof")
	if err != nil {
		t.Fatal(err)
	}
	value := runcontext.MustParseJSON(string(head.Checkpoint))
	unknown := 0
	for _, row := range value.Get("durable").Get("dispatches").Items() {
		if row.Get("kind").Str() == "agent" && row.Get("state").Str() == "unknown" {
			unknown++
		}
	}
	if unknown != 2 {
		t.Fatalf("native unknown claims=%d", unknown)
	}
	resumed := nativePlanCurrentInput(t, store, held)
	admitted, err := runcontext.PrepareDurableInput(context.Background(), resumed)
	if err != nil {
		t.Fatal("fresh next-unknown recovery failed before request preparation", err)
	}
	_, err = runcontext.PrepareDurableRequest(admitted.Persistence, "agent", "fixture", "native", []byte(`{}`))
	var outcome *runcontext.HostOutcomeUnknown
	if !errors.As(err, &outcome) {
		t.Fatalf("single-claim exemption forgave distinct unknown: %v", err)
	}
	if events := nativePlanResume(t, run, resumed); !nativePlanHasError(events) || *calls != 2 {
		t.Fatalf("released Plan decision forgave next unknown: calls=%d events=%+v", *calls, events)
	}
}

func TestNativePlanInterruptedTerminalNextUnknownRefuses(t *testing.T) {
	for _, taskSteps := range []bool{false, true} {
		name := "task-steps-off"
		if taskSteps {
			name = "task-steps-on"
		}
		t.Run(name, func(t *testing.T) {
			store, run, held, expected, calls := nativePlanPausedFixtureWithTaskSteps(t, "", taskSteps)
			read := func() *runcontext.Value {
				t.Helper()
				head, err := store.Load(context.Background(), "native-proof")
				if err != nil || head == nil {
					t.Fatal("missing retained head", err)
				}
				return runcontext.MustParseJSON(string(head.Checkpoint))
			}
			old := read().Get("durable").Get("dispatches").Items()[0].Clone()
			if old.Get("state").Str() != "unknown" || old.Get("planPause") == nil {
				t.Fatal("original invocation did not retain released Plan pause")
			}
			prepared, decisions, err := runcontext.PreparePlanDecision(context.Background(), held, expected)
			if err != nil || len(decisions) != 1 || *calls != 1 {
				t.Fatalf("legitimate decision failed before continuation: %v events=%+v calls=%d", err, decisions, *calls)
			}
			base, refuseTerminal, terminalRefusals := store.RunStore, true, 0
			store.commit = func(ctx context.Context, input runcontext.RunCommit) (runcontext.RunReceipt, error) {
				if refuseTerminal && len(input.Checkpoint) > 0 {
					var cp contract.Checkpoint
					if err := json.Unmarshal(input.Checkpoint, &cp); err != nil {
						t.Fatal(err)
					}
					if cp.Status == "stopped" {
						terminalRefusals++
						return runcontext.RunReceipt{}, errors.New("fixture interrupted terminal checkpoint")
					}
				}
				return base.Commit(ctx, input)
			}
			run.Agent.Run = func(_ context.Context, _ adapter.AgentRequest, cb adapter.AgentCallbacks) error {
				*calls++
				defer func() {
					if cb.ProviderRelease == nil || !cb.ProviderRelease.Acknowledge(cb.ProviderRelease.Invocation) {
						t.Error("exact continuation release refused")
					}
				}()
				return errors.New("fixture later native outcome unknown")
			}
			resume := func(input *runcontext.Input) ([]contract.RunEvent, error) {
				t.Helper()
				g := gate.New(gate.Options{})
				g.SetOffer(contract.ToolOffer{})
				run.Context, run.Gate = input, g.Calls()
				run.EnrollContext = func(_ context.Context, offer adapter.ContextOffer) error {
					return g.EnrollContext(offer.ID, offer.HostTools)
				}
				var events []contract.RunEvent
				run.Emit = func(event contract.RunEvent) { events = append(events, event) }
				err := native.New().Run(context.Background(), run)
				return events, err
			}
			_, err = resume(prepared)
			var storageUnknown *runcontext.StorageOutcomeUnknown
			if err != nil && !errors.As(err, &storageUnknown) {
				t.Fatal("unexpected terminal interruption", err)
			}
			if *calls != 2 || terminalRefusals == 0 {
				t.Fatalf("later unknown did not reach interrupted terminal write: calls=%d refused=%d", *calls, terminalRefusals)
			}
			retained := read()
			var physical []*runcontext.Value
			for _, row := range retained.Get("durable").Get("dispatches").Items() {
				if row.Get("kind").Str() == "agent" {
					physical = append(physical, row)
				}
			}
			if retained.Get("status").Str() != "running" || retained.Get("planState").Get("pending") != nil || retained.Get("plan").Len() != 1 {
				t.Fatal("interruption lost acknowledged running decision")
			}
			decision := retained.Get("planState").Get("decisions")
			if decision.Len() != 1 || !decision.Items()[0].Get("approved").Truth() || decision.Items()[0].Get("nativeInvocation").Get("dispatchId").Str() != old.Get("id").Str() || decision.Items()[0].Get("nativeInvocation").Get("requestDigest").Str() != old.Get("requestDigest").Str() {
				t.Fatal("legitimate decision lost exact original invocation")
			}
			if len(physical) != 2 || physical[0].Text() != old.Text() || physical[1].Get("kind").Str() != "agent" || physical[1].Get("state").Str() != "unknown" || physical[1].Get("id").Str() == old.Get("id").Str() || physical[1].Get("scope").Text() != old.Get("scope").Text() {
				t.Fatal("distinct unknown claims were not retained exactly")
			}
			refuseTerminal = false
			events, err := resume(nativePlanCurrentInput(t, store, held))
			if err != nil {
				t.Fatal(err)
			}
			if *calls != 2 {
				t.Fatalf("distinct later unknown must refuse before provider dispatch: calls=%d want=2", *calls)
			}
			if !nativePlanHasError(events) {
				t.Fatal("later unknown did not produce refusal event")
			}
			final := read().Get("durable").Get("dispatches")
			if final.Text() != retained.Get("durable").Get("dispatches").Text() {
				t.Fatal("refusal changed retained unknown audit rows")
			}
		})
	}
}
