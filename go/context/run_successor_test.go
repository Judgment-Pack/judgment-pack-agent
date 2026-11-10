package runcontext

// #340: a successor run of a complete run's last question; #341: a claimed question wait continues a complete head.
// The TypeScript twin is packages/harness/storage/successor.test.ts; the run itself is driven in
// go/adapter/completetests/successor_test.go.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
)

const successorPredecessor = "run-asks"

// successorRequestKey is the controller's request key for an empty prompt, the work phase and no purpose: the request
// every open below names.
const successorRequestKey = "40ae386a1126225cab5654adabd759ef7173753342a493aede15ab6653d8e18a"

// prepare is an open that names its request, as a session's does.
func prepare(ctx context.Context, in *Input) (*Input, error) {
	return PrepareDurableInputFor(ctx, in, DurableRequest{})
}

type successorScene struct {
	clock    *leaseClock
	store    durableWaitStore
	fence    LeaseFence
	question contract.Question
	answer   contract.QuestionDraft
	revision string
}

// successorStore is a complete head whose log ends with the question question-1, an earlier question-0 before it, and
// an allowance raised by a grant past 20 requests.
func successorStore(t *testing.T, edit func(map[string]any)) *successorScene {
	t.Helper()
	ctx := context.Background()
	clock := newLeaseClock()
	store := durableWaitStore{NewMemoryRunStore(MemoryRunStoreOptions{Now: clock.now})}
	lease := leaseAcquire(t, store.MemoryRunStore, successorPredecessor, "worker-a")
	question := contract.Question{Title: "Buyer", Question: "Who should we focus on as the first buyer?", Selection: "single", Options: []contract.QuestionOption{{ID: "operations", Label: "Head of operations", Description: "Owns recurring exception approvals."}, {ID: "risk", Label: "Risk or compliance lead", Description: "Owns oversight."}}}
	question.RecommendedOptionID = contract.Nullable[string]{Set: true, Value: "operations"}
	question.Recommendation = contract.Nullable[string]{Set: true, Value: "A starting hypothesis."}
	envelope := func(id string, q contract.Question) []byte {
		raw, err := json.Marshal(map[string]any{"version": 1, "scope": map[string]any{"execution": successorPredecessor, "role": "author"}, "event": map[string]any{"type": "user_question", "id": id, "question": q}})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	earlier := question
	earlier.Title = "Earlier"
	checkpoint := pausedCheckpoint(t, func(c map[string]any) {
		c["id"], c["status"], c["used"], c["limit"], c["requestKey"] = successorPredecessor, "complete", 20, 30, successorRequestKey
		c["grants"] = []any{map[string]any{"from": 20, "to": 30, "at": "2026-01-01T00:00:00.000Z"}}
		delete(c, "approval")
		if edit != nil {
			edit(c)
		}
	})
	receipt, err := store.CommitFenced(ctx, RunCommit{RunID: successorPredecessor, CommitID: "asked", Checkpoint: checkpoint, Events: [][]byte{envelope("question-0", earlier), envelope("question-1", question)}}, FenceOf(lease))
	if err != nil {
		t.Fatal(err)
	}
	// The run that asked has ended: its worker's lease is released.
	if _, err := store.ReleaseLease(ctx, FenceOf(lease)); err != nil {
		t.Fatal(err)
	}
	return &successorScene{clock: clock, store: store, fence: FenceOf(lease), question: question, answer: contract.QuestionDraft{Selected: []string{"risk"}, Text: ""}, revision: receipt.Revision}
}

func (s *successorScene) input(store RunStore, question string, resume bool) *Input {
	answer := s.answer
	return &Input{RunStore: &DurableRunInput{Store: store, ID: SuccessorRunID(successorPredecessor, question), Resume: resume, Successor: &RunSuccessor{Predecessor: successorPredecessor, Question: question, Answer: answer}}}
}

// park parks the question under a worker's lease of the head, as the run that asked it would.
func (s *successorScene) park(t *testing.T) {
	t.Helper()
	if _, _, err := s.parkAs(t, "wait-q", "question-1"); err != nil {
		t.Fatal(err)
	}
}

func (s *successorScene) parkAs(t *testing.T, wait, question string) (RunWait, AttentionEvent, error) {
	t.Helper()
	s.fence = FenceOf(leaseAcquire(t, s.store.MemoryRunStore, successorPredecessor, "worker-a"))
	timeout := waitTime(s.clock, 2*waitHour)
	input := ParkInput{Store: s.store, Fence: s.fence, Wait: wait, Continuation: "continue-q", Condition: WaitCondition{Event: &contract.RunWaitEventCondition{Kind: "question-answer", Subject: question}}, TimeoutAt: &timeout, WakeKeyDigest: waitKeyDigest(t, waitKey), Release: []WaitRelease{}, Question: question}
	return ParkRun(context.Background(), withWaitPolicy(input))
}

// wake delivers the person's answer to the question wait; the wait is answered and not yet claimed.
func (s *successorScene) wake(t *testing.T) {
	t.Helper()
	actor := "person-reviewer"
	answer := s.answer
	s.clock.advance(leaseMinute)
	if _, err := WakeRun(context.Background(), s.store, WakeEvidence{Version: 1, Resource: successorPredecessor, Wait: "wait-q", Evidence: "answer-1", Kind: "event", Event: &contract.RunWaitEventCondition{Kind: "question-answer", Subject: "question-1"}, Key: waitKey, Answer: &answer, Actor: &actor}); err != nil {
		t.Fatal(err)
	}
}

func (s *successorScene) claim(t *testing.T) ResumedWait {
	t.Helper()
	ctx := context.Background()
	if held, err := s.store.ReadWait(ctx, successorPredecessor, "wait-q"); err != nil || held == nil || held.State == "parked" {
		s.wake(t)
	}
	resumed, err := ResumeParked(ctx, withCurrent(t, ResumeInput{Store: s.store, Resource: successorPredecessor, Wait: "wait-q", Evidence: "answer-1", Owner: "worker-b", DurationMs: waitHour}))
	if err != nil {
		t.Fatal(err)
	}
	return resumed
}

func (s *successorScene) logged(t *testing.T, run string) int {
	t.Helper()
	page, err := s.store.ReadEvents(context.Background(), run, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	return len(page.Items)
}

// successorRefusal is the exact failure a refusal reaches a host as.
func successorRefusal(t *testing.T, err error) string {
	t.Helper()
	var refused *SuccessorRefused
	var named *failure.Error
	if !errors.As(err, &refused) || !errors.As(err, &named) {
		t.Fatalf("not a SuccessorRefused: %v", err)
	}
	raw, _ := json.Marshal(named.Failure)
	value, _ := ParseJSON(raw)
	return CanonicalJSON(value)
}

func successorFailure(reason string) string {
	code := "stale-state"
	if reason == "invalid" || reason == "unbound" || reason == "budgeted" {
		code = "configuration"
	}
	return `{"code":"` + code + `","detail":"SuccessorRefused: ` + reason + `.","outcome":"not-started","source":"context","version":1}`
}

func TestASuccessorRunIDIsDerivedFromThePredecessorAndItsQuestion(t *testing.T) {
	raw, err := os.ReadFile("../../contract/vectors/checkpoint/successor.json")
	if err != nil {
		t.Fatal(err)
	}
	var vector struct {
		Derivations []struct{ Predecessor, Question, ID string }
		Reasons     []string
		Items       []json.RawMessage
		Answer      struct {
			Marker   string
			Question contract.Question
			Answered struct {
				Answer contract.QuestionDraft
				Text   string
			}
			Unanswered struct{ Text string }
		}
	}
	if err := json.Unmarshal(raw, &vector); err != nil {
		t.Fatal(err)
	}
	if len(vector.Derivations) != 4 || len(vector.Reasons) != 8 || len(vector.Items) != 9 {
		t.Fatal("the vector is short")
	}
	// A storage id holds no control character, so the NUL-separated derivation is one id per pair.
	if vector.Derivations[2].ID == vector.Derivations[3].ID || vector.Derivations[0].ID == vector.Derivations[1].ID {
		t.Fatal("two questions share a successor")
	}
	for _, row := range vector.Derivations {
		if got := SuccessorRunID(row.Predecessor, row.Question); got != row.ID {
			t.Fatalf("%s/%s: %s", row.Predecessor, row.Question, got)
		}
	}
	for i, reason := range vector.Reasons {
		events := FailedToOpen(&SuccessorRefused{Reason: reason})
		got, _ := json.Marshal(events[0])
		want, _ := ParseJSON(vector.Items[i])
		have, _ := ParseJSON(got)
		if len(events) != 3 || CanonicalJSON(have) != CanonicalJSON(want) || successorRefusal(t, &SuccessorRefused{Reason: reason}) != successorFailure(reason) {
			t.Fatalf("%s: %s", reason, got)
		}
	}
	answer := vector.Answer.Answered.Answer
	if text, err := QuestionAnswerText(vector.Answer.Question, &answer, vector.Answer.Marker); err != nil || text != vector.Answer.Answered.Text {
		t.Fatalf("answered: %v\n%s", err, text)
	}
	if text, err := QuestionAnswerText(vector.Answer.Question, nil, vector.Answer.Marker); err != nil || text != vector.Answer.Unanswered.Text {
		t.Fatalf("unanswered: %v\n%s", err, text)
	}
}

func TestASuccessorCarriesThePredecessorsAllowanceAndNothingElse(t *testing.T) {
	ctx := context.Background()
	s := successorStore(t, nil)
	id := SuccessorRunID(successorPredecessor, "question-1")
	logged := s.logged(t, successorPredecessor)
	prepared, err := prepare(ctx, s.input(s.store, "question-1", false))
	if err != nil {
		t.Fatal(err)
	}
	head, err := s.store.Load(ctx, id)
	if err != nil || head == nil {
		t.Fatal(head, err)
	}
	predecessor, _ := s.store.Load(ctx, successorPredecessor)
	raw, err := os.ReadFile("../../contract/vectors/checkpoint/successor.json")
	if err != nil {
		t.Fatal(err)
	}
	var vector struct {
		Made struct{ Predecessor, Checkpoint json.RawMessage }
	}
	if err := json.Unmarshal(raw, &vector); err != nil {
		t.Fatal(err)
	}
	canonical := func(raw []byte) string {
		value, err := ParseJSON(raw)
		if err != nil {
			t.Fatal(err)
		}
		return CanonicalJSON(value)
	}
	if canonical(predecessor.Checkpoint) != canonical(vector.Made.Predecessor) {
		t.Fatal("the fixture is not the vector's predecessor")
	}
	if got := canonical(head.Checkpoint); got != canonical(vector.Made.Checkpoint) || got != string(head.Checkpoint) {
		t.Fatalf("the successor's first checkpoint\n%s\nnot\n%s", head.Checkpoint, canonical(vector.Made.Checkpoint))
	}
	if len(prepared.questionAnswers) != 1 || prepared.questionAnswers[0].answer.Selected[0] != "risk" || prepared.questionAnswers[0].question.Title != "Buyer" || prepared.Execution.ID != id {
		t.Fatalf("%+v", prepared.questionAnswers)
	}
	// The predecessor's head and log are read, never written.
	if predecessor.Revision != s.revision || s.logged(t, successorPredecessor) != logged {
		t.Fatal("the predecessor moved")
	}
	// A second successor of the same question is the one already made.
	if _, err := prepare(ctx, s.input(s.store, "question-1", false)); successorRefusal(t, err) != successorFailure("succeeded") {
		t.Fatal(err)
	}
	// The successor continues as a successor; the predecessor resumes no other way, and its question is not parked.
	if again, err := prepare(ctx, s.input(s.store, "question-1", true)); err != nil || len(again.questionAnswers) != 1 {
		t.Fatal(err)
	}
	if _, err := prepare(ctx, &Input{RunStore: &DurableRunInput{Store: s.store, ID: successorPredecessor, Resume: true}}); !errors.As(err, new(*RunComplete)) {
		t.Fatal(err)
	}
	if _, _, err := s.parkAs(t, "wait-q", "question-1"); waitReasonOf(err) != "nothing-pending" {
		t.Fatalf("a succeeded question parked: %v", err)
	}
	// The predecessor's lease was released after the create.
	if lease, err := s.store.ReadLease(ctx, successorPredecessor); err != nil || lease == nil || lease.Owner != "worker-a" {
		t.Fatalf("%+v %v", lease, err)
	}
}

func TestASuccessorIsRefusedByName(t *testing.T) {
	ctx := context.Background()
	blank := contract.QuestionDraft{Selected: []string{}, Text: "  "}
	offered := contract.QuestionDraft{Selected: []string{"elsewhere"}, Text: ""}
	cases := []struct {
		name, reason string
		edit         func(map[string]any)
		input        func(s *successorScene) *Input
	}{
		{"an empty predecessor", "invalid", nil, func(s *successorScene) *Input {
			in := s.input(s.store, "question-1", false)
			in.RunStore.Successor.Predecessor = ""
			return in
		}},
		{"an execution beside it", "invalid", nil, func(s *successorScene) *Input {
			in := s.input(s.store, "question-1", false)
			in.Execution = &contract.Checkpoint{}
			return in
		}},
		{"a question continuation beside it", "invalid", nil, func(s *successorScene) *Input {
			in := s.input(s.store, "question-1", false)
			in.QuestionContinuation = "wait-q"
			return in
		}},
		{"a job continuation beside it", "invalid", nil, func(s *successorScene) *Input {
			in := s.input(s.store, "question-1", false)
			in.JobContinuation = &RunJob{Job: "job-1"}
			return in
		}},
		{"a usage budget beside it", "invalid", nil, func(s *successorScene) *Input {
			in := s.input(s.store, "question-1", false)
			in.UsageBudget = &UsageBudgetInput{}
			return in
		}},
		{"a chat context beside it", "invalid", nil, func(s *successorScene) *Input {
			in := s.input(s.store, "question-1", false)
			in.RunStore.Context = &StoredChatScope{}
			return in
		}},
		{"a store that is one writer's callback", "invalid", nil, func(s *successorScene) *Input {
			callback := NewCheckpointCallbackRunStore(func(context.Context, contract.Checkpoint) error { return nil }, func([]byte) (contract.Checkpoint, error) { return contract.Checkpoint{}, nil })
			return s.input(callback, "question-1", false)
		}},
		{"an id not derived", "unbound", nil, func(s *successorScene) *Input {
			in := s.input(s.store, "question-1", false)
			in.RunStore.ID = "successor-chosen"
			return in
		}},
		{"an earlier question", "unbound", nil, func(s *successorScene) *Input { return s.input(s.store, "question-0", false) }},
		{"a blank answer", "unbound", nil, func(s *successorScene) *Input {
			s.answer = blank
			return s.input(s.store, "question-1", false)
		}},
		{"an option not offered", "unbound", nil, func(s *successorScene) *Input {
			s.answer = offered
			return s.input(s.store, "question-1", false)
		}},
		{"a predecessor not there", "not-complete", nil, func(s *successorScene) *Input {
			in := s.input(s.store, "question-1", false)
			in.RunStore.Successor.Predecessor = "run-none"
			in.RunStore.ID = SuccessorRunID("run-none", "question-1")
			return in
		}},
		{"a predecessor still running", "not-complete", func(c map[string]any) { c["status"] = "running" }, func(s *successorScene) *Input { return s.input(s.store, "question-1", false) }},
		{"a predecessor head of another run", "not-complete", func(c map[string]any) { c["id"] = "run-other" }, func(s *successorScene) *Input { return s.input(s.store, "question-1", false) }},
		{"a budgeted predecessor", "budgeted", func(c map[string]any) { c["version"], c["usageBudget"] = 3, successorBudget(t) }, func(s *successorScene) *Input { return s.input(s.store, "question-1", false) }},
		{"an exhausted predecessor", "exhausted", func(c map[string]any) { c["used"] = 30 }, func(s *successorScene) *Input { return s.input(s.store, "question-1", false) }},
		{"an exhausted provider-managed predecessor", "exhausted", func(c map[string]any) {
			c["unit"], c["used"], c["limit"], c["grants"] = "provider-managed", 1, 1, []any{}
		}, func(s *successorScene) *Input { return s.input(s.store, "question-1", false) }},
		{"a question with its own wait", "parked", nil, func(s *successorScene) *Input {
			s.park(t)
			return s.input(s.store, "question-1", false)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := successorStore(t, c.edit)
			in := c.input(s)
			_, err := prepare(ctx, in)
			if got := successorRefusal(t, err); got != successorFailure(c.reason) {
				t.Fatalf("%s: %s", c.name, got)
			}
			if err.Error() != "SuccessorRefused: "+c.reason+"." {
				t.Fatal(err.Error())
			}
			// Nothing of the successor is written.
			if head, err := s.store.Load(ctx, in.RunStore.ID); err != nil || head != nil {
				t.Fatalf("a refused successor was written: %v", err)
			}
		})
	}
}

func TestAClaimedQuestionWaitContinuesACompleteHead(t *testing.T) {
	ctx := context.Background()
	s := successorStore(t, nil)
	s.park(t)
	continuation := func(wait string) *Input {
		return &Input{RunStore: &DurableRunInput{Store: s.store, ID: successorPredecessor, Resume: true}, QuestionContinuation: wait}
	}
	// Parked, or answered and not yet claimed: a complete head is not resumed.
	if _, err := prepare(ctx, continuation("wait-q")); !errors.As(err, new(*RunComplete)) {
		t.Fatal(err)
	}
	s.wake(t)
	if _, err := prepare(ctx, continuation("wait-q")); !errors.As(err, new(*RunComplete)) {
		t.Fatal(err)
	}
	resumed := s.claim(t)
	prepared, err := prepare(ctx, continuation("wait-q"))
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.questionAnswers) != 1 || prepared.questionAnswers[0].answer == nil || prepared.questionAnswers[0].answer.Selected[0] != "risk" || prepared.questionAnswers[0].question.Title != "Buyer" {
		t.Fatalf("%+v", prepared.questionAnswers)
	}
	// Another wait, no continuation, or none at all: refused as before.
	for _, in := range []*Input{continuation("wait-other"), {RunStore: &DurableRunInput{Store: s.store, ID: successorPredecessor, Resume: true}}} {
		if _, err := prepare(ctx, in); !errors.As(err, new(*RunComplete)) {
			t.Fatal(err)
		}
	}
	if _, err := prepare(ctx, &Input{QuestionContinuation: "wait-q"}); waitReasonOf(err) != "invalid" {
		t.Fatal(err)
	}
	// The question was answered through its wait: no successor of it.
	if _, err := prepare(ctx, s.input(s.store, "question-1", false)); successorRefusal(t, err) != successorFailure("parked") {
		t.Fatal(err)
	}
	// Once the head moves, the continuation delivered again is refused.
	head, _ := s.store.Load(ctx, successorPredecessor)
	if _, err := s.store.CommitFenced(ctx, RunCommit{RunID: successorPredecessor, ExpectedRevision: head.Revision, CommitID: "moved", Checkpoint: head.Checkpoint}, resumed.Fence); err != nil {
		t.Fatal(err)
	}
	if _, err := prepare(ctx, continuation("wait-q")); !errors.As(err, new(*RunComplete)) {
		t.Fatal(err)
	}
}

// A question continuation names a storage id, and continues a running head only as its claimed wait.
func TestAQuestionContinuationOfARunningHeadNeedsItsClaimedWait(t *testing.T) {
	ctx := context.Background()
	s := successorStore(t, func(c map[string]any) { c["status"] = "running" })
	for wait, reason := range map[string]string{"wait-q": "mismatch", "wait\nq": "invalid"} {
		in := &Input{RunStore: &DurableRunInput{Store: s.store, ID: successorPredecessor, Resume: true}, QuestionContinuation: wait}
		if _, err := prepare(ctx, in); waitReasonOf(err) != reason {
			t.Fatalf("%q: %v", wait, err)
		}
	}
}

// hookedStore runs before (and can stand in for) the successor's create commit.
type hookedStore struct {
	durableWaitStore
	before func(RunCommit) error
}

func (h hookedStore) CommitHistory(ctx context.Context, input RunHistoryCommit) (RunHistoryReceipt, error) {
	if strings.HasPrefix(input.Run.RunID, "successor-") && h.before != nil {
		if err := h.before(input.Run); err != nil {
			return RunHistoryReceipt{}, err
		}
	}
	return h.durableWaitStore.CommitHistory(ctx, input)
}
func (h hookedStore) Commit(ctx context.Context, input RunCommit) (RunReceipt, error) {
	if strings.HasPrefix(input.RunID, "successor-") && h.before != nil {
		if err := h.before(input); err != nil {
			return RunReceipt{}, err
		}
	}
	return h.durableWaitStore.Commit(ctx, input)
}

// crashedStore lands the successor's create commit, or not, and then fails as a crash would.
type crashedStore struct {
	durableWaitStore
	landed bool
}

func (c crashedStore) CommitHistory(ctx context.Context, input RunHistoryCommit) (RunHistoryReceipt, error) {
	if !strings.HasPrefix(input.Run.RunID, "successor-") {
		return c.durableWaitStore.CommitHistory(ctx, input)
	}
	if c.landed {
		_, _ = c.durableWaitStore.CommitHistory(ctx, input)
	}
	return RunHistoryReceipt{}, errors.New("the process stopped")
}
func (c crashedStore) Commit(ctx context.Context, input RunCommit) (RunReceipt, error) {
	if !strings.HasPrefix(input.RunID, "successor-") {
		return c.durableWaitStore.Commit(ctx, input)
	}
	if c.landed {
		_, _ = c.durableWaitStore.Commit(ctx, input)
	}
	return RunReceipt{}, errors.New("the process stopped")
}

func TestASuccessorInterruptedWhileItIsMadeResumes(t *testing.T) {
	ctx := context.Background()
	for _, landed := range []bool{false, true} {
		s := successorStore(t, nil)
		id := SuccessorRunID(successorPredecessor, "question-1")
		if _, err := prepare(ctx, s.input(crashedStore{s.store, landed}, "question-1", false)); err == nil {
			t.Fatal("the crash was not reported")
		}
		head, err := s.store.Load(ctx, id)
		if err != nil || (head != nil) != landed {
			t.Fatalf("landed %v: head %v, %v", landed, head, err)
		}
		if !landed {
			// Nothing was made: the host makes it again.
			if _, err := prepare(ctx, s.input(s.store, "question-1", false)); err != nil {
				t.Fatal(err)
			}
			continue
		}
		// Made: making it again is refused by name, and the host continues the one made, with the answer.
		if _, err := prepare(ctx, s.input(s.store, "question-1", false)); successorRefusal(t, err) != successorFailure("succeeded") {
			t.Fatal(err)
		}
		prepared, err := prepare(ctx, s.input(s.store, "question-1", true))
		if err != nil || len(prepared.questionAnswers) != 1 || prepared.Execution.Used != 20 || prepared.Execution.Limit != 30 {
			t.Fatal(err)
		}
	}
}

func TestASuccessorAndAQuestionWaitAtOnceBothFailClosed(t *testing.T) {
	ctx := context.Background()
	s := successorStore(t, nil)
	// While the successor is made it holds the predecessor's lease: a worker that would park the question cannot lease
	// the head, and the successor is made.
	held := hookedStore{durableWaitStore: s.store, before: func(RunCommit) error {
		_, err := s.store.AcquireLease(ctx, LeaseAcquire{Resource: successorPredecessor, Owner: "worker-a", ExpectedRevision: s.revision, DurationMs: leaseMinute})
		if leaseReason(err) != "held" {
			t.Fatalf("the head was leased while its successor was made: %v", err)
		}
		return nil
	}}
	if _, err := prepare(ctx, s.input(held, "question-1", false)); err != nil {
		t.Fatal(err)
	}
	// A park that lands while the successor is made all the same (here under the successor's own lease, as a store
	// that does not serialize them would let it): after its create, the successor is refused, and the wait's
	// continuation because the successor exists.
	s = successorStore(t, nil)
	raced := hookedStore{durableWaitStore: s.store, before: func(RunCommit) error {
		lease, err := s.store.ReadLease(ctx, successorPredecessor)
		if err != nil || lease == nil || lease.Owner != SuccessorRunID(successorPredecessor, "question-1") {
			t.Fatalf("the successor is not made under the predecessor's lease: %+v %v", lease, err)
		}
		timeout := waitTime(s.clock, 2*waitHour)
		input := ParkInput{Store: s.store, Fence: FenceOf(*lease), Wait: "wait-q", Continuation: "continue-q", Condition: WaitCondition{Event: &contract.RunWaitEventCondition{Kind: "question-answer", Subject: "question-1"}}, TimeoutAt: &timeout, WakeKeyDigest: waitKeyDigest(t, waitKey), Release: []WaitRelease{}, Question: "question-1"}
		if _, _, err := ParkRun(ctx, withWaitPolicy(input)); err != nil {
			t.Fatal(err)
		}
		return nil
	}}
	if _, err := prepare(ctx, s.input(raced, "question-1", false)); successorRefusal(t, err) != successorFailure("parked") {
		t.Fatal(err)
	}
	// Both exist now, and neither continues: the successor is refused on every open, and the wait's continuation
	// because the successor exists.
	if head, err := s.store.Load(ctx, SuccessorRunID(successorPredecessor, "question-1")); err != nil || head == nil {
		t.Fatal(head, err)
	}
	if _, err := prepare(ctx, s.input(s.store, "question-1", true)); successorRefusal(t, err) != successorFailure("parked") {
		t.Fatal(err)
	}
	s.claim(t)
	if _, err := prepare(ctx, &Input{RunStore: &DurableRunInput{Store: s.store, ID: successorPredecessor, Resume: true}, QuestionContinuation: "wait-q"}); successorRefusal(t, err) != successorFailure("succeeded") {
		t.Fatal(err)
	}
}

// parkHookedStore makes the question's successor while the question is being parked, before the park's commit lands, as a
// writer that does not serialize them would.
type parkHookedStore struct {
	durableWaitStore
	during func()
}

func (p parkHookedStore) ParkWait(ctx context.Context, park RunWait, fence LeaseFence) (RunWait, AttentionEvent, error) {
	p.during()
	return p.durableWaitStore.ParkWait(ctx, park, fence)
}

// LOW-1: a park reads the successor again after its commit.
func TestAQuestionParkedWhileItsSuccessorIsMadeIsRefused(t *testing.T) {
	ctx := context.Background()
	s := successorStore(t, nil)
	id := SuccessorRunID(successorPredecessor, "question-1")
	raced := parkHookedStore{durableWaitStore: s.store, during: func() {
		if _, err := s.store.Commit(ctx, RunCommit{RunID: id, CommitID: "made", Checkpoint: []byte(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}}
	s.fence = FenceOf(leaseAcquire(t, s.store.MemoryRunStore, successorPredecessor, "worker-a"))
	timeout := waitTime(s.clock, 2*waitHour)
	input := ParkInput{Store: raced, Fence: s.fence, Wait: "wait-q", Continuation: "continue-q", Condition: WaitCondition{Event: &contract.RunWaitEventCondition{Kind: "question-answer", Subject: "question-1"}}, TimeoutAt: &timeout, WakeKeyDigest: waitKeyDigest(t, waitKey), Release: []WaitRelease{}, Question: "question-1"}
	if _, _, err := ParkRun(ctx, withWaitPolicy(input)); successorRefusal(t, err) != successorFailure("succeeded") {
		t.Fatal(err)
	}
	// The wait stands, and its continuation is refused because the successor exists.
	if held, err := s.store.ReadWait(ctx, successorPredecessor, "wait-q"); err != nil || held == nil {
		t.Fatal(held, err)
	}
}

// plainStore is a store with no lease or wait port, as a host's single store kind may be: nothing leases its runs.
type plainStore struct{ RunStore }

func TestTwoSuccessorsOfOneQuestionAtOnceAreOneRun(t *testing.T) {
	ctx := context.Background()
	// Another process makes the successor after this one read that it was not there, before this one's create lands.
	// Under the predecessor's lease the other is refused held, and this one is made.
	s := successorStore(t, nil)
	raced := hookedStore{durableWaitStore: s.store, before: func(RunCommit) error {
		if _, err := prepare(ctx, s.input(s.store, "question-1", false)); leaseReason(err) != "held" {
			t.Fatalf("a second successor was made under the first one's lease: %v", err)
		}
		return nil
	}}
	if _, err := prepare(ctx, s.input(raced, "question-1", false)); err != nil {
		t.Fatal(err)
	}
	// On a store with no leases the create is the one: the later is refused succeeded.
	s = successorStore(t, nil)
	raced = hookedStore{durableWaitStore: s.store, before: func(RunCommit) error {
		_, err := prepare(ctx, s.input(plainStore{s.store}, "question-1", false))
		return err
	}}
	if _, err := prepare(ctx, s.input(plainStore{raced}, "question-1", false)); successorRefusal(t, err) != successorFailure("succeeded") {
		t.Fatal(err)
	}
	if head, err := s.store.Load(ctx, SuccessorRunID(successorPredecessor, "question-1")); err != nil || head == nil {
		t.Fatal(head, err)
	}
}

// A successor is made under the predecessor's request cap or none, as the restore holds it, before it is written.
func TestASuccessorIsMadeOnlyUnderThePredecessorsRequestCap(t *testing.T) {
	ctx := context.Background()
	capped := func(c map[string]any) { c["used"], c["limit"], c["requestCap"], c["grants"] = 3, 5, 5, []any{} }
	for _, c := range []struct {
		name  string
		edit  func(map[string]any)
		cap   *int
		taken bool
	}{{"none for none", nil, nil, true}, {"the same cap", capped, capOf(5), true}, {"a cap where none was", nil, capOf(5), false}, {"none where one was", capped, nil, false}, {"a higher cap", capped, capOf(6), false}, {"a lower cap", capped, capOf(4), false}} {
		t.Run(c.name, func(t *testing.T) {
			s := successorStore(t, c.edit)
			in := s.input(s.store, "question-1", false)
			in.RequestCap = c.cap
			_, err := prepare(ctx, in)
			head, _ := s.store.Load(ctx, in.RunStore.ID)
			if c.taken != (err == nil) || c.taken != (head != nil) {
				t.Fatalf("%v, written %v", err, head != nil)
			}
			if !c.taken && err.Error() != "The fixed request cap must match the admitted API execution and cannot change after initialization." {
				t.Fatal(err)
			}
		})
	}
}

func capOf(v int) *int { return &v }

// A delegated predecessor's successor carries its principal (checkpoint 4), and is not made under another principal or
// none.
func TestASuccessorIsMadeOnlyUnderThePredecessorsPrincipal(t *testing.T) {
	ctx := context.Background()
	raw, err := os.ReadFile("../../contract/vectors/checkpoint/members.json")
	if err != nil {
		t.Fatal(err)
	}
	var members struct{ Items []map[string]any }
	if err := json.Unmarshal(raw, &members); err != nil {
		t.Fatal(err)
	}
	principal := members.Items[0]["principal"]
	s := successorStore(t, func(c map[string]any) { c["version"], c["principal"] = 4, principal })
	made, _, _, err := makeSuccessor(ctx, s.store, s.input(s.store, "question-1", false), SuccessorRunID(successorPredecessor, "question-1"), *s.input(s.store, "question-1", false).RunStore.Successor, &DurableRequest{})
	if err != nil || made.Version != 4 || made.Principal == nil || made.Principal.RootActor != "b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1" {
		t.Fatalf("%+v %v", made, err)
	}
	in := s.input(s.store, "question-1", false)
	if _, err := prepare(ctx, in); err == nil || err.Error() != "The run continues under another principal than it began under (principal-changed). Nothing was sent." {
		t.Fatal(err)
	}
	if head, err := s.store.Load(ctx, in.RunStore.ID); err != nil || head != nil {
		t.Fatalf("a successor under another principal was written: %v", err)
	}
}

// successorBudget is a usage budget journal as a checkpoint carries it.
func successorBudget(t *testing.T) any {
	t.Helper()
	raw, err := os.ReadFile("../../contract/vectors/checkpoint/members.json")
	if err != nil {
		t.Fatal(err)
	}
	var members struct{ Items []map[string]any }
	if err := json.Unmarshal(raw, &members); err != nil {
		t.Fatal(err)
	}
	for _, item := range members.Items {
		if budget, ok := item["usageBudget"]; ok {
			return budget
		}
	}
	t.Fatal("no usage budget in the members vector")
	return nil
}

// MEDIUM-1: a "successor-" id is a successor's, made or opened only with its successor input.
func TestASuccessorIDIsOpenedOnlyAsASuccessor(t *testing.T) {
	ctx := context.Background()
	s := successorStore(t, nil)
	id := SuccessorRunID(successorPredecessor, "question-1")
	for _, resume := range []bool{false, true} {
		if _, err := prepare(ctx, &Input{RunStore: &DurableRunInput{Store: s.store, ID: id, Resume: resume}}); successorRefusal(t, err) != successorFailure("unbound") {
			t.Fatalf("resume %v: %v", resume, err)
		}
	}
	if head, err := s.store.Load(ctx, id); err != nil || head != nil {
		t.Fatalf("an ordinary run was made at a successor's id: %v", err)
	}
	// So the question is still the predecessor's to park, and its successor still the one made from it.
	s.park(t)
}

// LOW-2: a successor is made only for its predecessor's request and phase, the controller's own refusal otherwise,
// before anything is written; an open that does not name its request makes none.
func TestASuccessorIsMadeOnlyForThePredecessorsRequest(t *testing.T) {
	ctx := context.Background()
	s := successorStore(t, nil)
	in := s.input(s.store, "question-1", false)
	if _, err := PrepareDurableInput(ctx, in); successorRefusal(t, err) != successorFailure("invalid") {
		t.Fatal(err)
	}
	if head, err := s.store.Load(ctx, in.RunStore.ID); err != nil || head != nil {
		t.Fatalf("a successor for no named request was written: %v", err)
	}
	for _, c := range []struct {
		name    string
		request DurableRequest
		phase   string
	}{{"another prompt", DurableRequest{Prompt: "Write another note."}, ""}, {"a purpose", DurableRequest{Purpose: "brief"}, ""}, {"another phase", DurableRequest{}, "synthesis"}} {
		t.Run(c.name, func(t *testing.T) {
			s := successorStore(t, nil)
			in := s.input(s.store, "question-1", false)
			in.Phase = c.phase
			_, err := PrepareDurableInputFor(ctx, in, c.request)
			if err == nil || err.Error() != "The request or execution phase changed. Start a new response with the current context." {
				t.Fatal(err)
			}
			if head, err := s.store.Load(ctx, in.RunStore.ID); err != nil || head != nil {
				t.Fatalf("a successor for another request was written: %v", err)
			}
		})
	}
}

// admittingStore stands for a store that holds a claimed job wait of the predecessor at its head (#330), or an open
// objective of it (#330) where goal is set: each admits a continuation of the complete head.
type admittingStore struct {
	durableWaitStore
	job, revision string
	goal          *RunGoal
}

func (a admittingStore) ExportWaits(ctx context.Context, run string) (WaitExport, error) {
	held, err := a.durableWaitStore.ExportWaits(ctx, run)
	if err == nil && a.job != "" && run == successorPredecessor {
		held.Waits = append(held.Waits, RunWait{Version: 1, Resource: run, Wait: "wait-job", Reason: "job", State: "resumed", Pending: contract.RunWaitPending{Job: &contract.RunWaitPendingJob{Kind: "job", Job: a.job, Generation: JobGeneration}}, Checkpoint: contract.RunWaitCheckpointBinding{Revision: a.revision}})
	}
	return held, err
}
func (a admittingStore) ReadGoal(ctx context.Context, run string) (*RunGoal, error) {
	if a.goal != nil && run == successorPredecessor {
		return a.goal, nil
	}
	return a.durableWaitStore.ReadGoal(ctx, run)
}

// objectiveCheckpoint is a complete head holding a step of its accepted Plan unfinished, as an objective's leg finds it.
func objectiveCheckpoint(t *testing.T) func(map[string]any) {
	t.Helper()
	raw, err := os.ReadFile("../../contract/vectors/plan/native-checkpoints.json")
	if err != nil {
		t.Fatal(err)
	}
	var vector struct{ Items []map[string]any }
	if err := json.Unmarshal(raw, &vector); err != nil {
		t.Fatal(err)
	}
	planState := vector.Items[1]["planState"].(map[string]any)
	delete(planState, "pending")
	return func(c map[string]any) {
		c["version"], c["plan"], c["planState"] = 3, vector.Items[1]["plan"], planState
	}
}

func objectiveGoal() *RunGoal {
	stamp := contract.RunGoalStamp{Command: "bind", Actor: "person-reviewer", At: "2026-10-01T00:00:00.000Z"}
	return &RunGoal{Version: 1, Kind: "goal", Resource: successorPredecessor, Revision: 1, Journal: []contract.RunGoalEntry{}, Objectives: []contract.RunGoalObjective{{ID: "objective-1", State: "active", Created: stamp,
		Plan: &contract.RunGoalPlan{ID: "accepted-plan", Revision: "sha256:a8e181bf218df2f98469173636720266f4e0da8fe4f55d8f13d9eb80e5891233", Steps: []contract.RunGoalStep{{Step: "Inspect", Index: 0}}, Bound: stamp}}}}
}

// LOW-4: once the head's last question has its successor, the head resumes no other way, whichever admission would
// have continued it: a claimed job wait's continuation, or a leg of an open objective (the claimed question wait's is
// TestASuccessorAndAQuestionWaitAtOnceBothFailClosed's).
func TestAHeadWhoseQuestionHasItsSuccessorResumesNoOtherWay(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name  string
		edit  func(*testing.T) func(map[string]any)
		admit func(s *successorScene) (admittingStore, *Input)
	}{
		{"a claimed job wait's continuation", func(*testing.T) func(map[string]any) { return nil }, func(s *successorScene) (admittingStore, *Input) {
			store := admittingStore{durableWaitStore: s.store, job: "job-1", revision: s.revision}
			return store, &Input{RunStore: &DurableRunInput{Store: store, ID: successorPredecessor, Resume: true}, JobContinuation: &RunJob{Job: "job-1"}}
		}},
		{"a leg of an open objective", objectiveCheckpoint, func(s *successorScene) (admittingStore, *Input) {
			store := admittingStore{durableWaitStore: s.store, goal: objectiveGoal()}
			return store, &Input{RunStore: &DurableRunInput{Store: store, ID: successorPredecessor, Resume: true}}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := successorStore(t, c.edit(t))
			store, continuation := c.admit(s)
			if _, err := prepare(ctx, continuation); err != nil {
				t.Fatalf("not admitted: %v", err)
			}
			if _, err := prepare(ctx, s.input(store, "question-1", false)); err != nil {
				t.Fatal(err)
			}
			if _, err := prepare(ctx, continuation); successorRefusal(t, err) != successorFailure("succeeded") {
				t.Fatal(err)
			}
		})
	}
}

// HIGH-1: an admitted continuation of the predecessor and its successor do not both spend its allowance. The
// continuation is a claimed job wait's, under its claim's lease of the head.
func TestAnAdmittedContinuationAndASuccessorDoNotBothSpendTheAllowance(t *testing.T) {
	ctx := context.Background()
	s := successorStore(t, nil)
	store := admittingStore{durableWaitStore: s.store, job: "job-1", revision: s.revision}
	claim := FenceOf(leaseAcquire(t, s.store.MemoryRunStore, successorPredecessor, "worker-b"))
	continuation := &Input{RunStore: &DurableRunInput{Store: store, ID: successorPredecessor, Resume: true}, JobContinuation: &RunJob{Job: "job-1"}}
	if _, err := prepare(ctx, continuation); err != nil {
		t.Fatal(err)
	}
	// While the continuation holds the head's lease, no successor is made.
	id := SuccessorRunID(successorPredecessor, "question-1")
	_, err := prepare(ctx, s.input(store, "question-1", false))
	if leaseReason(err) != "held" || err.Error() != "LeaseRefused: held." {
		t.Fatal(err)
	}
	if head, err := s.store.Load(ctx, id); err != nil || head != nil {
		t.Fatalf("a successor was made beside a live continuation: %v", err)
	}
	// Once its lease has lapsed the successor is made at a new epoch: the continuation commits nothing more, and is
	// not opened again.
	s.clock.advance(leaseMinute)
	if _, err := prepare(ctx, s.input(store, "question-1", false)); err != nil {
		t.Fatal(err)
	}
	head, _ := s.store.Load(ctx, successorPredecessor)
	if _, err := s.store.CommitFenced(ctx, RunCommit{RunID: successorPredecessor, ExpectedRevision: head.Revision, CommitID: "spent", Checkpoint: head.Checkpoint}, claim); leaseReason(err) != "stale-epoch" {
		t.Fatalf("the continuation committed beside its successor: %v", err)
	}
	if _, err := prepare(ctx, continuation); successorRefusal(t, err) != successorFailure("succeeded") {
		t.Fatal(err)
	}
}

// HIGH-1: the predecessor is read again after the create; a commit that moved it meanwhile refuses the successor.
func TestASuccessorWhosePredecessorMovedWhileItWasMadeIsRefused(t *testing.T) {
	ctx := context.Background()
	s := successorStore(t, nil)
	moved := hookedStore{durableWaitStore: s.store, before: func(RunCommit) error {
		head, _ := s.store.Load(ctx, successorPredecessor)
		_, err := s.store.Commit(ctx, RunCommit{RunID: successorPredecessor, ExpectedRevision: head.Revision, CommitID: "unfenced", Checkpoint: head.Checkpoint})
		return err
	}}
	if _, err := prepare(ctx, s.input(moved, "question-1", false)); successorRefusal(t, err) != successorFailure("moved") {
		t.Fatal(err)
	}
}

// MEDIUM-2: a predecessor past its deadline, or closed, leases to no one, so it has no successor; nothing is written.
func TestASuccessorIsNotMadeOfARunPastItsDeadline(t *testing.T) {
	ctx := context.Background()
	s := successorStore(t, nil)
	if _, err := LimitRun(ctx, s.store, RunLimit{Version: 1, Resource: successorPredecessor, Run: waitTime(s.clock, leaseMinute)}); err != nil {
		t.Fatal(err)
	}
	s.clock.advance(leaseMinute)
	in := s.input(s.store, "question-1", false)
	if _, err := prepare(ctx, in); waitReasonOf(err) != "deadline-passed" || err.Error() != "WaitRefused: deadline-passed." {
		t.Fatal(err)
	}
	if head, err := s.store.Load(ctx, in.RunStore.ID); err != nil || head != nil {
		t.Fatalf("a successor of a run past its deadline was written: %v", err)
	}
}

// MEDIUM-2: a predecessor's records keep their retainUntil in the successor, and expired records have no successor.
func TestASuccessorKeepsThePredecessorsRetention(t *testing.T) {
	ctx := context.Background()
	record := func(until string) func(map[string]any) {
		return func(c map[string]any) {
			raw, _ := json.Marshal(c)
			value, _ := ParseJSON(raw)
			written, err := WriteRecord(CheckpointSchema, value, RecordsPolicy{}, nil, time.Now(), until)
			if err != nil {
				t.Fatal(err)
			}
			var out map[string]any
			if err := json.Unmarshal(written.JSON(), &out); err != nil {
				t.Fatal(err)
			}
			for key := range c {
				delete(c, key)
			}
			for key, v := range out {
				c[key] = v
			}
		}
	}
	until := time.Now().Add(48 * time.Hour).UTC().Format("2006-01-02T15:04:05.000Z")
	s := successorStore(t, record(until))
	in := s.input(s.store, "question-1", false)
	prepared, err := prepare(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	head, _ := s.store.Load(ctx, in.RunStore.ID)
	value, _ := ParseJSON(head.Checkpoint)
	saved, err := ReadExecution(value)
	if err != nil || saved.RetainUntil == nil || *saved.RetainUntil != until || saved.Schema == nil || *saved.Schema != CheckpointSchema || prepared.Execution.RetainUntil == nil || *prepared.Execution.RetainUntil != until {
		t.Fatalf("%s %v", head.Checkpoint, err)
	}
	s = successorStore(t, record("2020-01-01T00:00:00.000Z"))
	in = s.input(s.store, "question-1", false)
	if _, err := prepare(ctx, in); !errors.As(err, new(*RecordsExpired)) {
		t.Fatal(err)
	}
	if head, err := s.store.Load(ctx, in.RunStore.ID); err != nil || head != nil {
		t.Fatalf("a successor of expired records was written: %v", err)
	}
}

// LOW-4: the members a successor carries are read at their versions; a kept approval text is not carried.
func TestASuccessorIsWrittenAtTheVersionOfWhatItCarries(t *testing.T) {
	for version, want := range map[int64]int64{1: 2, 2: 2, 3: 3, 4: 4, 5: 3, 6: 4} {
		if got := successorVersion(version); got != want {
			t.Fatalf("%d: %d", version, got)
		}
	}
	// A turn's test calls are the turn's: the successor's first turn counts its own.
	ctx := context.Background()
	s := successorStore(t, func(c map[string]any) { c["version"], c["testCalls"] = 3, 2 })
	made, _, _, err := makeSuccessor(ctx, s.store, s.input(s.store, "question-1", false), SuccessorRunID(successorPredecessor, "question-1"), *s.input(s.store, "question-1", false).RunStore.Successor, &DurableRequest{})
	if err != nil || made.TestCalls != nil || made.Version != 3 {
		t.Fatalf("%+v %v", made, err)
	}
}
