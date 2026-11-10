package continuationtests

// #309 on the durable agent wire: the invocation that continues a decided approval tells the model the call's result,
// whether it is the first request after the decision, a lost one sent again, or a fresh one after a takeover; the act
// runs once; a call id that repeats is told apart; and a delegated child is never run twice (#312's HIGH-1).

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

// dyingStore stands for a process that dies on the store's receipt of the first commit whose checkpoint holds what at
// says, or where kill is called: that commit is kept, nothing the process writes after it lands, and the process stops.
// revive is the next process taking over.
type dyingStore struct {
	*runcontext.MemoryRunStore
	mu     sync.Mutex
	at     func(contract.Checkpoint) bool
	cancel context.CancelFunc
	died   bool
}

var errDied = errors.New("the process died here")

func (s *dyingStore) kill() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.died = true
	if s.cancel != nil {
		s.cancel()
	}
}
func (s *dyingStore) revive() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.died, s.at, s.cancel = false, nil, nil
}
func (s *dyingStore) dead() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.died
}
func (s *dyingStore) dies(raw []byte) bool {
	s.mu.Lock()
	var held contract.Checkpoint
	if s.died || s.at == nil || json.Unmarshal(raw, &held) != nil || !s.at(held) {
		s.mu.Unlock()
		return false
	}
	s.mu.Unlock()
	s.kill()
	return true
}
func (s *dyingStore) Commit(ctx context.Context, input runcontext.RunCommit) (runcontext.RunReceipt, error) {
	if s.dead() {
		return runcontext.RunReceipt{}, errDied
	}
	receipt, err := s.MemoryRunStore.Commit(ctx, input)
	if err == nil && s.dies(input.Checkpoint) {
		return runcontext.RunReceipt{}, errDied
	}
	return receipt, err
}
func (s *dyingStore) CommitHistory(ctx context.Context, input runcontext.RunHistoryCommit) (runcontext.RunHistoryReceipt, error) {
	if s.dead() {
		return runcontext.RunHistoryReceipt{}, errDied
	}
	receipt, err := s.MemoryRunStore.CommitHistory(ctx, input)
	if err == nil && s.dies(input.Run.Checkpoint) {
		return runcontext.RunHistoryReceipt{}, errDied
	}
	return receipt, err
}

func newDyingStore() *dyingStore {
	return &dyingStore{MemoryRunStore: runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})}
}

func (s *scene) head(t *testing.T) contract.Checkpoint {
	t.Helper()
	head, err := s.store.Load(context.Background(), "run-c")
	if err != nil || head == nil {
		t.Fatal(head, err)
	}
	var saved contract.Checkpoint
	if err := json.Unmarshal(head.Checkpoint, &saved); err != nil {
		t.Fatal(err)
	}
	return saved
}

// told reports whether a prompt carries the decision's block: the line, the call's result and when it was decided.
func told(prompt string, approval contract.CheckpointApproval) bool {
	return strings.Contains(prompt, runcontext.ApprovalContinuation) && strings.Contains(prompt, answerWords) &&
		approval.DecidedAt != nil && strings.Contains(prompt, `"decidedAt":"`+*approval.DecidedAt+`"`)
}

type form struct {
	name                        string
	delegating, bare, taskSteps bool
}

var forms = []form{{name: "plain"}, {name: "through the coordinator", delegating: true}, {name: "plain, no current request", bare: true},
	{name: "through the coordinator, no current request", delegating: true, bare: true}, {name: "plain, task steps", taskSteps: true},
	{name: "through the coordinator, task steps", delegating: true, taskSteps: true}}

// #309: the request that continues the response after the decision carries the approved call's result, and the call
// runs once.
func TestTheApprovedResultReachesTheModel(t *testing.T) {
	for _, form := range forms {
		t.Run(form.name, func(t *testing.T) {
			s := &scene{store: runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{}), bare: form.bare, taskSteps: form.taskSteps}
			decision := paused(t, s, form.delegating)
			if err := s.run(t, context.Background(), true, decision, form.delegating); err != nil {
				t.Fatal(err)
			}
			approval := s.head(t).Approval
			if s.ran != 1 || len(s.errors()) != 0 || !s.finished() || len(s.parents) != 2 || approval == nil || !told(s.parents[1], *approval) {
				t.Fatalf("ran %d, errors %v, finished %v, %d parents, the continuation told: %v", s.ran, s.errors(), s.finished(), len(s.parents), approval != nil && len(s.parents) > 1 && told(s.parents[1], *approval))
			}
		})
	}
}

// #309: a continuation lost after it was sent (the process dies while the provider holds it) is sent again as recorded,
// byte for byte; under task steps it is refused HostOutcomeUnknown, as any re-send there is. The call never runs again.
func TestALostContinuationIsSentAgainAsRecorded(t *testing.T) {
	for _, form := range forms {
		t.Run(form.name, func(t *testing.T) {
			store := newDyingStore()
			s := &scene{store: store, bare: form.bare, taskSteps: form.taskSteps}
			decision := paused(t, s, form.delegating)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			store.cancel = cancel
			lost := func(context.Context, adapter.AgentRequest, adapter.AgentCallbacks, int) error {
				store.kill()
				return errDied
			}
			_ = s.run(t, ctx, true, decision, form.delegating, lost)
			if !store.dead() || s.ran != 1 || s.finished() || len(s.parents) != 2 {
				t.Fatalf("the lost continuation: ran %d, finished %v, %d parents", s.ran, s.finished(), len(s.parents))
			}
			store.revive()
			s.events = nil
			_ = s.run(t, context.Background(), true, nil, form.delegating)
			if form.taskSteps {
				if errs := s.errors(); s.ran != 1 || s.finished() || len(s.parents) != 2 || len(errs) != 1 || !strings.Contains(errs[0], "HostOutcomeUnknown") {
					t.Fatalf("under task steps: ran %d, finished %v, %d parents, errors %v", s.ran, s.finished(), len(s.parents), errs)
				}
				return
			}
			if s.ran != 1 || len(s.errors()) != 0 || !s.finished() || len(s.parents) != 3 || s.parents[2] != s.parents[1] {
				t.Fatalf("the takeover: ran %d, errors %v, finished %v, %d parents, sent again as recorded %v", s.ran, s.errors(), s.finished(), len(s.parents), len(s.parents) == 3 && s.parents[2] == s.parents[1])
			}
		})
	}
}

// #312's MEDIUM-2: a takeover once the approval is answered (the continuation not yet claimed through the coordinator;
// claimed and finished with the response unfinished plain) sends a fresh request that still carries the block.
func TestATakeoverAfterTheAnswerStillTellsTheModel(t *testing.T) {
	for _, form := range forms[:4] {
		t.Run(form.name, func(t *testing.T) {
			store := newDyingStore()
			s := &scene{store: store, bare: form.bare}
			decision := paused(t, s, form.delegating)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			store.at = func(held contract.Checkpoint) bool { return held.Approval != nil && held.Approval.State == "answered" }
			store.cancel = cancel
			_ = s.run(t, ctx, true, decision, form.delegating)
			if !store.died || s.ran != 1 {
				t.Fatalf("the process did not die after the answer: ran %d, %v", s.ran, s.errors())
			}
			sent := len(s.parents)
			store.revive()
			s.events = nil
			if err := s.run(t, context.Background(), true, nil, form.delegating); err != nil {
				t.Fatal(err)
			}
			approval := s.head(t).Approval
			if s.ran != 1 || len(s.errors()) != 0 || !s.finished() || len(s.parents) != sent+1 || approval == nil || !told(s.parents[sent], *approval) {
				t.Fatalf("the takeover: ran %d, errors %v, finished %v, %d parents after %d, told %v", s.ran, s.errors(), s.finished(), len(s.parents), sent, approval != nil && len(s.parents) > sent && told(s.parents[sent], *approval))
			}
		})
	}
}

// #312's LOW-1: two decisions on calls with the same id and the same result (other arguments). The continuation of the first, paused on
// the second, is not taken for the second's: the second's result reaches the model, and each call runs once.
func TestARepeatedCallIDIsToldApart(t *testing.T) {
	for _, form := range forms[:2] {
		t.Run(form.name, func(t *testing.T) {
			s := &scene{store: runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{}), asks: 2}
			first := paused(t, s, form.delegating)
			if err := s.run(t, context.Background(), true, first, form.delegating, call("writes__write_note", `{"again":true}`)); err != nil {
				t.Fatal(err)
			}
			again := s.head(t).Approval
			if again == nil || again.State != "asked" || again.CallID != first.CallID || s.ran != 1 {
				t.Fatalf("the second call was not asked about under the same id: %+v, ran %d, %v", again, s.ran, s.errors())
			}
			// Decisions are dated to the millisecond: the second is taken in a later one.
			time.Sleep(5 * time.Millisecond)
			proof := runcontext.ApprovalShownDigest(*again)
			if err := s.run(t, context.Background(), true, &contract.CheckpointApprovalDecision{CallID: again.CallID, Approved: true, ShownDigest: &proof}, form.delegating); err != nil {
				t.Fatal(err)
			}
			approval := s.head(t).Approval
			if s.ran != 2 || len(s.errors()) != 0 || !s.finished() || len(s.parents) != 3 || s.parents[2] == s.parents[1] || approval == nil || !told(s.parents[2], *approval) {
				t.Fatalf("ran %d, errors %v, finished %v, %d parents, the second decision told %v", s.ran, s.errors(), s.finished(), len(s.parents), approval != nil && len(s.parents) == 3 && told(s.parents[2], *approval))
			}
		})
	}
}

func delegateOnce(t *testing.T) step {
	return func(ctx context.Context, request adapter.AgentRequest, cb adapter.AgentCallbacks, _ int) error {
		for _, tool := range request.Tools {
			if strings.HasPrefix(tool.Description, "delegate_tasks:") {
				_, err := cb.Tool(ctx, adapter.AgentToolCall{ID: "delegate-wire", Name: tool.Name, Arguments: json.RawMessage(`{"tasks":[{"title":"Check","profile":"review","objective":"Check the note."}]}`)})
				return err
			}
		}
		t.Error("delegate_tasks was not offered")
		return nil
	}
}

// #312's HIGH-1: the continuation that carried the decision delegated, and its batch's answer is durable when the
// process dies. The takeover sends the batch's answer, not the continuation again: the child runs once, the act once.
func TestATakeoverAfterTheBatchRunsTheChildOnce(t *testing.T) {
	store := newDyingStore()
	s := &scene{store: store}
	decision := paused(t, s, true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store.at = func(held contract.Checkpoint) bool { return held.Handoff != nil && held.Handoff.State == "answered" }
	store.cancel = cancel
	_ = s.run(t, ctx, true, decision, true, delegateOnce(t))
	if !store.died || s.ran != 1 || s.children != 1 {
		t.Fatalf("the process did not die after the batch: ran %d, children %d, %v", s.ran, s.children, s.errors())
	}
	sent := len(s.parents)
	store.revive()
	s.events = nil
	if err := s.run(t, context.Background(), true, nil, true); err != nil {
		t.Fatal(err)
	}
	if s.ran != 1 || s.children != 1 || len(s.errors()) != 0 || !s.finished() || len(s.parents) != sent+1 || strings.Contains(s.parents[sent], runcontext.ApprovalContinuation) || !strings.Contains(s.parents[sent], "One finding.") {
		t.Fatalf("the takeover: ran %d, children %d, errors %v, finished %v, %d parents after %d", s.ran, s.children, s.errors(), s.finished(), len(s.parents), sent)
	}
}
