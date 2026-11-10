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

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
)

const successorPredecessor = "run-asks"

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
		c["id"], c["status"], c["used"], c["limit"] = successorPredecessor, "complete", 20, 30
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
	return &successorScene{clock: clock, store: store, fence: FenceOf(lease), question: question, answer: contract.QuestionDraft{Selected: []string{"risk"}, Text: ""}, revision: receipt.Revision}
}

func (s *successorScene) input(store RunStore, question string, resume bool) *Input {
	answer := s.answer
	return &Input{RunStore: &DurableRunInput{Store: store, ID: SuccessorRunID(successorPredecessor, question), Resume: resume, Successor: &RunSuccessor{Predecessor: successorPredecessor, Question: question, Answer: answer}}}
}

func (s *successorScene) park(t *testing.T) {
	t.Helper()
	timeout := waitTime(s.clock, 2*waitHour)
	input := ParkInput{Store: s.store, Fence: s.fence, Wait: "wait-q", Continuation: "continue-q", Condition: WaitCondition{Event: &contract.RunWaitEventCondition{Kind: "question-answer", Subject: "question-1"}}, TimeoutAt: &timeout, WakeKeyDigest: waitKeyDigest(t, waitKey), Release: []WaitRelease{}, Question: "question-1"}
	if _, _, err := ParkRun(context.Background(), withWaitPolicy(input)); err != nil {
		t.Fatal(err)
	}
}

func (s *successorScene) claim(t *testing.T) ResumedWait {
	t.Helper()
	ctx := context.Background()
	actor := "person-reviewer"
	answer := s.answer
	s.clock.advance(leaseMinute)
	if _, err := WakeRun(ctx, s.store, WakeEvidence{Version: 1, Resource: successorPredecessor, Wait: "wait-q", Evidence: "answer-1", Kind: "event", Event: &contract.RunWaitEventCondition{Kind: "question-answer", Subject: "question-1"}, Key: waitKey, Answer: &answer, Actor: &actor}); err != nil {
		t.Fatal(err)
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
	if reason == "invalid" || reason == "unbound" || reason == "exceeds" {
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
	if len(vector.Derivations) != 4 || len(vector.Reasons) != 6 || len(vector.Items) != 7 {
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
	prepared, err := PrepareDurableInput(ctx, s.input(s.store, "question-1", false))
	if err != nil {
		t.Fatal(err)
	}
	head, err := s.store.Load(ctx, id)
	if err != nil || head == nil {
		t.Fatal(head, err)
	}
	predecessor, _ := s.store.Load(ctx, successorPredecessor)
	var held map[string]any
	_ = json.Unmarshal(predecessor.Checkpoint, &held)
	want, _ := canonicalRaw(map[string]any{"version": 2, "id": id, "requestKey": held["requestKey"], "phase": held["phase"], "status": "running", "unit": "model-requests", "used": 20, "limit": 30,
		"grants": []any{map[string]any{"from": 20, "to": 30, "at": "2026-01-01T00:00:00.000Z"}}, "plan": []any{}, "explanation": "", "operations": []any{}, "context": map[string]any{"scope": held["context"].(map[string]any)["scope"], "entries": []any{}}})
	got, _ := ParseJSON(head.Checkpoint)
	if CanonicalJSON(got) != string(want) {
		t.Fatalf("the successor's first checkpoint\n%s\nnot\n%s", CanonicalJSON(got), want)
	}
	if len(prepared.questionAnswers) != 1 || prepared.questionAnswers[0].answer.Selected[0] != "risk" || prepared.questionAnswers[0].question.Title != "Buyer" || prepared.Execution.ID != id {
		t.Fatalf("%+v", prepared.questionAnswers)
	}
	// The predecessor's head and log are read, never written.
	if predecessor.Revision != s.revision || s.logged(t, successorPredecessor) != logged {
		t.Fatal("the predecessor moved")
	}
	// A second successor of the same question is the one already made.
	if _, err := PrepareDurableInput(ctx, s.input(s.store, "question-1", false)); successorRefusal(t, err) != successorFailure("succeeded") {
		t.Fatal(err)
	}
	// The successor continues as a successor; the predecessor resumes no other way, and its question is not parked.
	if again, err := PrepareDurableInput(ctx, s.input(s.store, "question-1", true)); err != nil || len(again.questionAnswers) != 1 {
		t.Fatal(err)
	}
	if _, err := PrepareDurableInput(ctx, &Input{RunStore: &DurableRunInput{Store: s.store, ID: successorPredecessor, Resume: true}}); !errors.As(err, new(*RunComplete)) {
		t.Fatal(err)
	}
	timeout := waitTime(s.clock, 2*waitHour)
	park := ParkInput{Store: s.store, Fence: s.fence, Wait: "wait-q", Continuation: "continue-q", Condition: WaitCondition{Event: &contract.RunWaitEventCondition{Kind: "question-answer", Subject: "question-1"}}, TimeoutAt: &timeout, WakeKeyDigest: waitKeyDigest(t, waitKey), Release: []WaitRelease{}, Question: "question-1"}
	if _, _, err := ParkRun(ctx, withWaitPolicy(park)); waitReasonOf(err) != "nothing-pending" {
		t.Fatalf("a succeeded question parked: %v", err)
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
		{"more requests than it held", "exceeds", func(c map[string]any) {
			c["used"], c["limit"], c["requestCap"], c["grants"] = 3, 5, 5, []any{}
		}, func(s *successorScene) *Input {
			in := s.input(s.store, "question-1", false)
			cap := 6
			in.RequestCap = &cap
			return in
		}},
		{"a question with its own wait", "parked", nil, func(s *successorScene) *Input {
			s.park(t)
			return s.input(s.store, "question-1", false)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := successorStore(t, c.edit)
			in := c.input(s)
			_, err := PrepareDurableInput(ctx, in)
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
	// Parked, not yet claimed: a complete head is not resumed.
	if _, err := PrepareDurableInput(ctx, continuation("wait-q")); !errors.As(err, new(*RunComplete)) {
		t.Fatal(err)
	}
	resumed := s.claim(t)
	prepared, err := PrepareDurableInput(ctx, continuation("wait-q"))
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.questionAnswers) != 1 || prepared.questionAnswers[0].answer == nil || prepared.questionAnswers[0].answer.Selected[0] != "risk" || prepared.questionAnswers[0].question.Title != "Buyer" {
		t.Fatalf("%+v", prepared.questionAnswers)
	}
	// Another wait, no continuation, or none at all: refused as before.
	for _, in := range []*Input{continuation("wait-other"), {RunStore: &DurableRunInput{Store: s.store, ID: successorPredecessor, Resume: true}}} {
		if _, err := PrepareDurableInput(ctx, in); !errors.As(err, new(*RunComplete)) {
			t.Fatal(err)
		}
	}
	if _, err := PrepareDurableInput(ctx, &Input{QuestionContinuation: "wait-q"}); waitReasonOf(err) != "invalid" {
		t.Fatal(err)
	}
	// The question was answered through its wait: no successor of it.
	if _, err := PrepareDurableInput(ctx, s.input(s.store, "question-1", false)); successorRefusal(t, err) != successorFailure("parked") {
		t.Fatal(err)
	}
	// Once the head moves, the continuation delivered again is refused.
	head, _ := s.store.Load(ctx, successorPredecessor)
	if _, err := s.store.CommitFenced(ctx, RunCommit{RunID: successorPredecessor, ExpectedRevision: head.Revision, CommitID: "moved", Checkpoint: head.Checkpoint}, resumed.Fence); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareDurableInput(ctx, continuation("wait-q")); !errors.As(err, new(*RunComplete)) {
		t.Fatal(err)
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
		if _, err := PrepareDurableInput(ctx, s.input(crashedStore{s.store, landed}, "question-1", false)); err == nil {
			t.Fatal("the crash was not reported")
		}
		head, err := s.store.Load(ctx, id)
		if err != nil || (head != nil) != landed {
			t.Fatalf("landed %v: head %v, %v", landed, head, err)
		}
		if !landed {
			// Nothing was made: the host makes it again.
			if _, err := PrepareDurableInput(ctx, s.input(s.store, "question-1", false)); err != nil {
				t.Fatal(err)
			}
			continue
		}
		// Made: making it again is refused by name, and the host continues the one made, with the answer.
		if _, err := PrepareDurableInput(ctx, s.input(s.store, "question-1", false)); successorRefusal(t, err) != successorFailure("succeeded") {
			t.Fatal(err)
		}
		prepared, err := PrepareDurableInput(ctx, s.input(s.store, "question-1", true))
		if err != nil || len(prepared.questionAnswers) != 1 || prepared.Execution.Used != 20 || prepared.Execution.Limit != 30 {
			t.Fatal(err)
		}
	}
}

func TestASuccessorAndAQuestionWaitAtOnceBothFailClosed(t *testing.T) {
	ctx := context.Background()
	s := successorStore(t, nil)
	// The question is parked while the successor is being made: after the successor's read of the waits, before its
	// create lands.
	raced := hookedStore{durableWaitStore: s.store, before: func(RunCommit) error {
		s.park(t)
		return nil
	}}
	if _, err := PrepareDurableInput(ctx, s.input(raced, "question-1", false)); successorRefusal(t, err) != successorFailure("parked") {
		t.Fatal(err)
	}
	// Both exist now, and neither continues: the successor is refused on every open, and the wait's continuation
	// because the successor exists.
	if head, err := s.store.Load(ctx, SuccessorRunID(successorPredecessor, "question-1")); err != nil || head == nil {
		t.Fatal(head, err)
	}
	if _, err := PrepareDurableInput(ctx, s.input(s.store, "question-1", true)); successorRefusal(t, err) != successorFailure("parked") {
		t.Fatal(err)
	}
	s.claim(t)
	if _, err := PrepareDurableInput(ctx, &Input{RunStore: &DurableRunInput{Store: s.store, ID: successorPredecessor, Resume: true}, QuestionContinuation: "wait-q"}); successorRefusal(t, err) != successorFailure("succeeded") {
		t.Fatal(err)
	}
}
