package continuationtests

// ADR-0008 on the agent wire: after the person's decision the approved call runs once, and the invocation that continues
// the response carries the continuation block (#309); a continuation that delegates goes on past its batch (#305).

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
	parents := 0
	run := &adapter.Run{Wire: "openai-compatible", ModelName: "fixture", Gate: g.Calls(), Thinking: "off", Prompt: "Write the note.", AllowConversation: true, Interactive: true, Critic: "off", HostTools: []runcontext.HostTool{s.tool()},
		EnrollContext: func(_ context.Context, offer adapter.ContextOffer) error {
			if offer.Mode == gate.PhaseChild {
				return nil
			}
			return g.EnrollContext(offer.ID, offer.HostTools)
		},
		Context: &runcontext.Input{CurrentRequest: &runcontext.CurrentRequest{Message: "Write the note.", Input: "note"}, RunStore: &runcontext.DurableRunInput{Store: s.store, ID: "run-c", Resume: resume}, Approval: decision},
		Emit: func(event contract.RunEvent) {
			s.mu.Lock()
			s.events = append(s.events, event)
			s.mu.Unlock()
		},
		Children: func(context.Context, contract.DelegatedTaskProfile, func(contract.RunEvent)) (*gate.Calls, []contract.ToolOfferRuntimeTool, error) {
			child := gate.New(gate.Options{Granted: gate.Ceiling})
			child.SetOffer(contract.ToolOffer{})
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

// #309: the continuing invocation is told the approved call's result, and the act runs once.
func TestTheContinuingInvocationCarriesTheApprovedResult(t *testing.T) {
	for _, delegating := range []bool{false, true} {
		name := "plain"
		if delegating {
			name = "through the coordinator"
		}
		t.Run(name, func(t *testing.T) {
			s := &scene{store: runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})}
			decision := paused(t, s, delegating)
			before := len(s.parents)
			if err := s.run(t, context.Background(), true, decision, delegating); err != nil {
				t.Fatal(err)
			}
			continued := s.parents[before:]
			if s.ran != 1 || len(continued) != 1 || !strings.Contains(continued[0], runcontext.ApprovalContinuation) || !strings.Contains(continued[0], answerWords) || !strings.Contains(continued[0], decision.CallID) {
				t.Fatalf("ran %d, invocations %d, errors %v, prompt %.400q", s.ran, len(continued), s.errors(), continued)
			}
			if len(s.errors()) != 0 || !s.finished() {
				t.Fatalf("errors %v, finished %v", s.errors(), s.finished())
			}
		})
	}
}

// The replay of a lost invocation is kept: a continuing invocation whose outcome is unknown is sent again as recorded.
func TestALostContinuingInvocationIsSentAgainAsRecorded(t *testing.T) {
	s := &scene{store: runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})}
	decision := paused(t, s, false)
	ctx, cancel := context.WithCancel(context.Background())
	lose := func(context.Context, adapter.AgentRequest, adapter.AgentCallbacks, int) error {
		cancel()
		return context.Canceled
	}
	_ = s.run(t, ctx, true, decision, false, lose)
	before := len(s.parents)
	if before < 2 || !strings.Contains(s.parents[before-1], answerWords) {
		t.Fatalf("the continuation was not sent: %v, errors %v", len(s.parents), s.errors())
	}
	lost := s.parents[before-1]
	if err := s.run(t, context.Background(), true, nil, false); err != nil {
		t.Fatal(err)
	}
	again := s.parents[before:]
	if s.ran != 1 || len(again) != 1 || again[0] != lost {
		t.Fatalf("ran %d, the request sent again is not the recorded one: %d, errors %v", s.ran, len(again), s.errors())
	}
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
	// The batch ran and ended, and the parent's next invocation answered: no "already decided".
	if s.ran != 1 || tasks != 1 || len(s.errors()) != 0 || !s.finished() {
		t.Fatalf("ran %d, tasks ended %d, errors %v, finished %v", s.ran, tasks, s.errors(), s.finished())
	}
}
