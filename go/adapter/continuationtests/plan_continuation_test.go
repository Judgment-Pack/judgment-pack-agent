package continuationtests

// #317 and #312's MEDIUM-B on the agent wire: a native Plan pause whose decision's continuation delegates, or that
// follows an approval, goes on to the end, through a takeover too; a delegated child runs once and an act once.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

const acceptedPlan = `{"id":"original","revision":1,"objective":"Work","steps":["Inspect","Report"],"questions":[],"references":[],"status":"accepted","createdAt":"2026-01-01T00:00:00.000Z","acceptedAt":"2026-01-01T00:00:00.000Z"}`

// planPause is a parent that departs from the accepted plan, which pauses the run for the person's Plan decision.
func planPause(t *testing.T, budget bool) step {
	return func(ctx context.Context, request adapter.AgentRequest, cb adapter.AgentCallbacks, _ int) error {
		arguments := `{"explanation":"Remove accepted work","plan":[{"id":"step-2","step":"Report","status":"pending"}]}`
		if budget {
			arguments = `{"explanation":"Remove accepted work","plan":[{"id":"step-2","step":"Report","status":"pending","budget":{"requests":1}}]}`
		}
		for _, tool := range request.Tools {
			if strings.HasPrefix(tool.Description, "update_plan: ") {
				_, err := cb.Tool(ctx, adapter.AgentToolCall{ID: "pause", Name: tool.Name, Arguments: json.RawMessage(arguments)})
				return err
			}
		}
		t.Error("update_plan was not offered")
		return nil
	}
}

// planDecided prepares the person's acceptance of the pending Plan departure for the next run, as a host does.
func (s *scene) planDecided(t *testing.T) {
	t.Helper()
	saved := s.head(t)
	if saved.PlanState == nil || saved.PlanState.Pending == nil {
		t.Fatalf("no Plan pause: %v", s.errors())
	}
	pending := saved.PlanState.Pending
	held := s.input(true, &contract.CheckpointApprovalDecision{CallID: pending.CallID, Approved: true})
	held.Execution = &saved
	prepared, _, err := runcontext.PreparePlanDecision(context.Background(), held, runcontext.PlanDecisionExpectation{Revision: pending.Revision, CandidateRevision: pending.Candidate.PlanState.Revision})
	if err != nil {
		t.Fatal(err)
	}
	s.prepare = func(*runcontext.Input) *runcontext.Input { return prepared }
}

// #317: the Plan decision's continuation delegates; the process dies once the batch's answer is durable. The takeover
// goes on from the batch's answer: the delegating invocation, which ended at its handoff, refuses nothing and is not
// sent again, and the child runs once.
func TestAPlanContinuationThatDelegatesGoesOn(t *testing.T) {
	for _, taskSteps := range []bool{false, true} {
		name := "plain"
		if taskSteps {
			name = "task steps"
		}
		t.Run(name, func(t *testing.T) {
			store := newDyingStore()
			s := &scene{store: store, plan: acceptedPlan, taskSteps: taskSteps}
			_ = s.run(t, context.Background(), false, nil, true, planPause(t, taskSteps))
			s.planDecided(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			store.at = func(held contract.Checkpoint) bool { return held.Handoff != nil && held.Handoff.State == "answered" }
			store.cancel = cancel
			s.events = nil
			_ = s.run(t, ctx, true, nil, true, delegateOnce(t))
			if !store.dead() || s.children != 1 {
				t.Fatalf("the process did not die after the batch: children %d, %v", s.children, s.errors())
			}
			s.prepare = nil
			store.revive()
			s.events = nil
			if err := s.run(t, context.Background(), true, nil, true); err != nil {
				t.Fatal(err)
			}
			if s.children != 1 || len(s.errors()) != 0 || !s.finished() {
				t.Fatalf("the takeover: children %d, errors %v, finished %v", s.children, s.errors(), s.finished())
			}
		})
	}
}

// #312's MEDIUM-B: an approval earlier in the response, then a Plan pause. The Plan decision's continuation goes on: the
// approval pause's own invocation, which ended before the Plan pause's was sent, refuses nothing, and the act runs once.
func TestAPlanDecisionAfterAnApprovalGoesOn(t *testing.T) {
	for _, form := range forms[:2] {
		t.Run(form.name, func(t *testing.T) {
			s := &scene{store: runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{}), plan: acceptedPlan}
			decision := paused(t, s, form.delegating)
			_ = s.run(t, context.Background(), true, decision, form.delegating, planPause(t, false))
			if s.ran != 1 || len(s.parents) != 2 {
				t.Fatalf("the approved continuation: ran %d, %d parents, %v", s.ran, len(s.parents), s.errors())
			}
			s.planDecided(t)
			s.events = nil
			if err := s.run(t, context.Background(), true, nil, form.delegating); err != nil {
				t.Fatal(err)
			}
			// The invocation that ended at the Plan pause carried the approval's block; the Plan continuation is a fresh
			// request, and the decision is still the response's newest continuation, so it carries the block again.
			approval := s.head(t).Approval
			if s.ran != 1 || len(s.errors()) != 0 || !s.finished() || len(s.parents) != 3 || approval == nil || !told(s.parents[2], *approval) {
				t.Fatalf("the Plan continuation: ran %d, errors %v, finished %v, %d parents, told %v", s.ran, s.errors(), s.finished(), len(s.parents), approval != nil && len(s.parents) > 2 && told(s.parents[2], *approval))
			}
		})
	}
}

// diesInFlight is a parent whose invocation is sent and then lost with the process: its outcome is unknown.
func diesInFlight(store *dyingStore) step {
	return func(context.Context, adapter.AgentRequest, adapter.AgentCallbacks, int) error {
		store.kill()
		return errDied
	}
}

// The safety side of the native Plan exemption (#317): a handoff earlier in the response, then a Plan pause, and the Plan
// continuation is lost after it was sent. That invocation carries the response's newest continuation (the batch's answer),
// so its effect is unknown: the takeover refuses HostOutcomeUnknown and sends nothing, rather than passing over it and
// running the continuation twice.
func TestALostPlanContinuationAfterAHandoffIsRefused(t *testing.T) {
	for _, taskSteps := range []bool{false, true} {
		name := "through the coordinator"
		if taskSteps {
			name = "through the coordinator, task steps"
		}
		t.Run(name, func(t *testing.T) {
			store := newDyingStore()
			s := &scene{store: store, plan: acceptedPlan, taskSteps: taskSteps}
			_ = s.run(t, context.Background(), false, nil, true, delegateOnce(t), planPause(t, taskSteps))
			if s.children != 1 {
				t.Fatalf("no batch before the Plan pause: children %d, %v", s.children, s.errors())
			}
			s.planDecided(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			store.cancel = cancel
			s.events = nil
			before := len(s.parents)
			_ = s.run(t, ctx, true, nil, true, diesInFlight(store))
			if !store.dead() || len(s.parents) != before+1 {
				t.Fatalf("the Plan continuation was not lost in flight: dead %v, %d parents after %d, %v", store.dead(), len(s.parents), before, s.errors())
			}
			s.prepare = nil
			store.revive()
			s.events = nil
			sent := len(s.parents)
			_ = s.run(t, context.Background(), true, nil, true)
			if errs := s.errors(); len(s.parents) != sent || len(errs) != 1 || !strings.Contains(errs[0], "HostOutcomeUnknown") || s.children != 1 {
				t.Fatalf("the lost Plan continuation was passed over: %d new requests, children %d, errors %v", len(s.parents)-sent, s.children, errs)
			}
		})
	}
}

// The same with an approval earlier in the response: the Plan continuation carries the approval's block again, so the lost
// invocation is refused HostOutcomeUnknown, nothing is sent, and the act ran once.
func TestALostPlanContinuationAfterAnApprovalIsRefused(t *testing.T) {
	for _, form := range forms[:2] {
		t.Run(form.name, func(t *testing.T) {
			store := newDyingStore()
			s := &scene{store: store, plan: acceptedPlan}
			decision := paused(t, s, form.delegating)
			_ = s.run(t, context.Background(), true, decision, form.delegating, planPause(t, false))
			s.planDecided(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			store.cancel = cancel
			s.events = nil
			before := len(s.parents)
			_ = s.run(t, ctx, true, nil, form.delegating, diesInFlight(store))
			approval := s.head(t).Approval
			if !store.dead() || len(s.parents) != before+1 || approval == nil || !told(s.parents[before], *approval) {
				t.Fatalf("the Plan continuation was not lost in flight carrying the approval's block: %d parents after %d, %v", len(s.parents), before, s.errors())
			}
			s.prepare = nil
			store.revive()
			s.events = nil
			sent := len(s.parents)
			_ = s.run(t, context.Background(), true, nil, form.delegating)
			if errs := s.errors(); len(s.parents) != sent || len(errs) != 1 || !strings.Contains(errs[0], "HostOutcomeUnknown") || s.ran != 1 {
				t.Fatalf("the lost Plan continuation after an approval was passed over: %d new requests, ran %d, errors %v", len(s.parents)-sent, s.ran, errs)
			}
		})
	}
}
