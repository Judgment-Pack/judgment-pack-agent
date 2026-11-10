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
			if s.ran != 1 || len(s.errors()) != 0 || !s.finished() || len(s.parents) != 3 {
				t.Fatalf("the Plan continuation: ran %d, errors %v, finished %v, %d parents", s.ran, s.errors(), s.finished(), len(s.parents))
			}
		})
	}
}
