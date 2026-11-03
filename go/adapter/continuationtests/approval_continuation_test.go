package continuationtests

// ADR-0008 on the agent wire: an approved continuation that delegates goes on past its batch (#305), and a decision is
// taken by the response's first open only.

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	native "github.com/Judgment-Pack/judgment-pack-agent/go/adapter/agent"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/delegate"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/turn"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
)

const answerWords = "THE-APPROVED-ANSWER"

// scene is one durable run with an act tool the person is asked about once.
type scene struct {
	mu         sync.Mutex
	store      runcontext.RunStore
	asked, ran int
	parents    []string
	children   int
	events     []contract.RunEvent
}

func (s *scene) tool() runcontext.HostTool {
	tool := runcontext.HostTool{Name: "write_note", Description: "write_note", InputSchema: json.RawMessage(`{"type":"object"}`), EffectClass: "act", Approval: "required", Taxonomy: &runcontext.ToolTaxonomy{Operation: "act", Effect: "act"},
		Approve: func(runcontext.ApprovalRequest) string {
			s.mu.Lock()
			defer s.mu.Unlock()
			s.asked++
			if s.asked == 1 {
				return "ask"
			}
			return "allow"
		},
		Execute: func(context.Context, json.RawMessage) (runcontext.ToolResult, error) {
			s.mu.Lock()
			s.ran++
			s.mu.Unlock()
			return runcontext.ResultOf([]runcontext.Content{runcontext.TextContent(answerWords)}, nil), nil
		}}
	digest := runcontext.ToolListDigest([]runcontext.ListedTool{{Name: tool.Name, Description: tool.Description, InputSchema: tool.InputSchema, Taxonomy: tool.Taxonomy}})
	tool.Source = &runcontext.ToolSource{Kind: "custom", ID: "writes", Trust: "person", Version: "1", ToolListDigest: digest, CurrentDigest: func(context.Context) (string, error) { return digest, nil }}
	return tool
}

// step is what the parent does in one invocation, given what it was asked and which invocation it is.
type step func(ctx context.Context, request adapter.AgentRequest, cb adapter.AgentCallbacks, at int) error

func call(prefix, args string) step {
	return func(ctx context.Context, request adapter.AgentRequest, cb adapter.AgentCallbacks, _ int) error {
		for _, tool := range request.Tools {
			if strings.HasPrefix(tool.Description, prefix+":") {
				_, err := cb.Tool(ctx, adapter.AgentToolCall{ID: "wire-" + prefix, Name: tool.Name, Arguments: json.RawMessage(args)})
				return err
			}
		}
		return nil
	}
}

func (s *scene) run(t *testing.T, ctx context.Context, resume bool, decision *contract.CheckpointApprovalDecision, delegating bool, steps ...step) error {
	t.Helper()
	g := gate.New(gate.Options{})
	g.SetOffer(contract.ToolOffer{})
	var child *gate.Gate
	var parentHosts []contract.ToolOfferHostTool
	parents := 0
	run := &adapter.Run{Wire: "openai-compatible", ModelName: "fixture", Gate: g.Calls(), Thinking: "off", Prompt: "Write the note.", AllowConversation: true, Interactive: true, Critic: "off", HostTools: []runcontext.HostTool{s.tool()},
		EnrollContext: func(_ context.Context, offer adapter.ContextOffer) error {
			if offer.Mode == gate.PhaseChild {
				return child.EnrollContext(offer.ID, offer.HostTools)
			}
			parentHosts = offer.HostTools
			return g.EnrollContext(offer.ID, offer.HostTools)
		},
		Context: &runcontext.Input{CurrentRequest: &runcontext.CurrentRequest{Message: "Write the note.", Input: "note"}, RunStore: &runcontext.DurableRunInput{Store: s.store, ID: "run-c", Resume: resume}, Approval: decision},
		Emit: func(event contract.RunEvent) {
			s.mu.Lock()
			s.events = append(s.events, event)
			s.mu.Unlock()
		},
		Children: func(_ context.Context, profile contract.DelegatedTaskProfile, emit func(contract.RunEvent)) (*gate.Calls, []contract.ToolOfferRuntimeTool, error) {
			child = gate.New(gate.Options{Granted: gate.Ceiling, Emit: emit})
			child.SetOffer(contract.ToolOffer{})
			child.Enter(gate.ChildPhase(profile, contract.ToolOffer{}, gate.LoopPhase(contract.ToolOffer{HostTools: parentHosts})))
			return child.Calls(), nil, nil
		}}
	run.Agent = &adapter.Agent{Model: "fixture", Run: func(ctx context.Context, request adapter.AgentRequest, cb adapter.AgentCallbacks) error {
		if cb.ProviderRelease != nil {
			defer cb.ProviderRelease.Acknowledge(cb.ProviderRelease.Invocation)
		}
		parent := !delegating || strings.Contains(request.Prompt, turn.DelegationSpent)
		for _, tool := range request.Tools {
			parent = parent || strings.HasPrefix(tool.Description, "delegate_tasks:")
		}
		if !parent {
			s.mu.Lock()
			s.children++
			s.mu.Unlock()
			return cb.Event(ctx, adapter.AgentEvent{Type: "message", ID: "child", Text: "One finding.", Phase: "final"})
		}
		at := parents
		parents++
		s.mu.Lock()
		s.parents = append(s.parents, request.Prompt)
		s.mu.Unlock()
		if at < len(steps) && steps[at] != nil {
			if err := steps[at](ctx, request, cb, at); err != nil {
				return err
			}
		}
		return cb.Event(ctx, adapter.AgentEvent{Type: "message", ID: "final", Text: "Done.", Phase: "final"})
	}}
	var implementation adapter.Adapter = native.New()
	if delegating {
		implementation = delegate.With(native.New(), delegate.Options{})
	}
	return implementation.Run(ctx, run)
}

func (s *scene) errors() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []string{}
	for _, event := range s.events {
		if event.Error != nil {
			out = append(out, event.Error.Message)
		}
	}
	return out
}

func (s *scene) finished() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, event := range s.events {
		if event.Message != nil && event.Message.Text == "Done." {
			return true
		}
	}
	return false
}

// paused runs the first invocation to the pause and answers the person's decision.
func paused(t *testing.T, s *scene, delegating bool) *contract.CheckpointApprovalDecision {
	t.Helper()
	if err := s.run(t, context.Background(), false, nil, delegating, call("writes__write_note", `{}`)); err != nil {
		t.Fatal(err)
	}
	head, err := s.store.Load(context.Background(), "run-c")
	if err != nil || head == nil {
		t.Fatal(head, err)
	}
	var saved contract.Checkpoint
	if err := json.Unmarshal(head.Checkpoint, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Approval == nil || saved.Approval.State != "asked" || s.ran != 0 {
		t.Fatalf("not paused: %+v, ran %d, %v", saved.Approval, s.ran, s.errors())
	}
	proof := runcontext.ApprovalShownDigest(*saved.Approval)
	return &contract.CheckpointApprovalDecision{CallID: saved.Approval.CallID, Approved: true, ShownDigest: &proof}
}

// #305: a continuation that delegates goes on past its batch: the decision is the first open's, not the next parent's.
func TestAnApprovedContinuationThatDelegatesGoesOn(t *testing.T) {
	s := &scene{store: runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})}
	decision := paused(t, s, true)
	delegateOnce := func(ctx context.Context, request adapter.AgentRequest, cb adapter.AgentCallbacks, _ int) error {
		for _, tool := range request.Tools {
			if strings.HasPrefix(tool.Description, "delegate_tasks:") {
				_, err := cb.Tool(ctx, adapter.AgentToolCall{ID: "delegate-wire", Name: tool.Name, Arguments: json.RawMessage(`{"tasks":[{"title":"Check","profile":"review","objective":"Check the note."}]}`)})
				return err
			}
		}
		t.Error("delegate_tasks was not offered")
		return nil
	}
	if err := s.run(t, context.Background(), true, decision, true, delegateOnce); err != nil {
		t.Fatal(err)
	}
	tasks := 0
	for _, event := range s.events {
		if event.DelegatedTask != nil && (event.DelegatedTask.Task.State == "complete" || event.DelegatedTask.Task.State == "failed") {
			tasks++
		}
	}
	// The child ran, the batch ended, and the parent's next invocation answered: no "already decided".
	if s.ran != 1 || s.children != 1 || tasks != 1 || len(s.errors()) != 0 || !s.finished() {
		t.Fatalf("ran %d, children %d, tasks ended %d, errors %v, finished %v", s.ran, s.children, tasks, s.errors(), s.finished())
	}
}

// #312's review, LOW-3: a decision given again for a call already decided is refused, as it was before #305's reset, through
// the coordinator and plain alike. Only a later parent open of the response that took it goes on without it.
func TestADecisionGivenAgainIsRefused(t *testing.T) {
	for _, delegating := range []bool{false, true} {
		name := "plain"
		if delegating {
			name = "through the coordinator"
		}
		t.Run(name, func(t *testing.T) {
			s := &scene{store: runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})}
			decision := paused(t, s, delegating)
			if err := s.run(t, context.Background(), true, decision, delegating); err != nil {
				t.Fatal(err)
			}
			if s.ran != 1 || len(s.errors()) != 0 {
				t.Fatalf("the decision: ran %d, errors %v", s.ran, s.errors())
			}
			s.events = nil
			if err := s.run(t, context.Background(), true, decision, delegating); err != nil {
				t.Fatal(err)
			}
			// #328: the decision's continuation finished the run, so the head is complete and the resume is refused
			// RunComplete before the decision is read: nothing runs again.
			if errors := s.errors(); s.ran != 1 || len(errors) != 1 || !strings.Contains(errors[0], "RunComplete") {
				t.Fatalf("given again: ran %d, errors %v", s.ran, errors)
			}
		})
	}
}
