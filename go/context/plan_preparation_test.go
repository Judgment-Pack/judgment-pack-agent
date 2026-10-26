package runcontext

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

type preparationChat struct {
	calls int
	chat  *ChatContext
}

func (c *preparationChat) OpenTurn() (*ChatTurn, error) { c.calls++; return c.chat.OpenTurn() }

type preparationStore struct {
	RunStore
	decisions int
	lose      bool
	before    func()
}

func (s *preparationStore) Commit(ctx context.Context, in RunCommit) (RunReceipt, error) {
	decision := false
	for _, raw := range in.Events {
		var e StoredRunEnvelope
		_ = json.Unmarshal(raw, &e)
		if e.Event.PlanDriftDecided != nil {
			decision = true
		}
	}
	if decision {
		s.decisions++
		if s.before != nil {
			s.before()
		}
	}
	r, e := s.RunStore.Commit(ctx, in)
	if e == nil && decision && s.lose {
		return RunReceipt{}, errors.New("fixture lost acknowledgement")
	}
	return r, e
}
func pendingPreparation(t *testing.T, refs bool) (Session, *Input, PlanDecisionExpectation, *preparationStore, *preparationChat) {
	t.Helper()
	ctx := context.Background()
	session, _ := trackedSession(t)
	store := &preparationStore{RunStore: NewMemoryRunStore(MemoryRunStoreOptions{})}
	chat := &preparationChat{chat: NewChatContext("preparation")}
	session.Context.Chat = chat
	session.Context.RunStore = &DurableRunInput{Store: store, ID: "prepare-plan"}
	a := extendedPlanRows(t)[0].Get("planState").Get("accepted")
	session.Context.Plan = ObjectOf("id", a.Get("id"), "revision", a.Get("sourceRevision"), "objective", a.Get("objective"), "steps", a.Get("steps"), "questions", Array(), "references", Array(), "status", String("accepted"), "createdAt", a.Get("acceptedAt"), "acceptedAt", a.Get("acceptedAt"))
	if refs {
		session.Context.Plan.Set("references", Array(ObjectOf("kind", String("artifact"), "id", String("dependency"), "digest", String(strings.Repeat("b", 64)))))
		session.Context.PlanResolver = &PlanResolver{Resolve: func(context.Context, string, string) (ResolvedPlanReference, error) {
			return ResolvedPlanReference{Kind: "artifact", ID: "dependency", Digest: strings.Repeat("b", 64)}, nil
		}}
	}
	run := openRun(t, session)
	// Actual completed host dispatch must survive this metadata-only transition exactly.
	_, err := InvokeDurable(ctx, run.Session.Context.Persistence, "host", "fixture", "inspect", []byte(`{}`), true, func([]byte) (ToolResult, error) { return ResultOf([]Content{TextContent("exact evidence")}, nil), nil }, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	id := run.Context.Execution.State().Plan.Items()[1].Get("id")
	_, err = hostTool(t, run, "update_plan").Execute(ctx, ObjectOf("explanation", String("Remove accepted work"), "plan", Array(ObjectOf("id", id, "step", String("Report"), "status", String("pending")))).JSON())
	if err != nil {
		t.Fatal(err)
	}
	cp := run.Session.Context.Persistence.Writer.Checkpoint()
	input := *run.Session.Context
	input.Execution = cp
	raw, _ := json.Marshal(cp)
	pending := MustParseJSON(string(raw)).Get("planState").Get("pending")
	expected := PlanDecisionExpectation{Revision: planText(pending.Get("revision")), CandidateRevision: planText(pending.Get("candidate").Get("planState").Get("revision"))}
	input.Approval = &contract.CheckpointApprovalDecision{CallID: planText(pending.Get("callId")), Approved: true}
	run.Context.Close()
	return session, &input, expected, store, chat
}
func TestPreparePlanDecisionCommitsExactStateWithoutSession(t *testing.T) {
	for _, approve := range []bool{true, false} {
		t.Run(map[bool]string{true: "accept", false: "reject"}[approve], func(t *testing.T) {
			session, input, expected, store, chat := pendingPreparation(t, false)
			input.Approval.Approved = approve
			head, loadErr := store.Load(context.Background(), "prepare-plan")
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			var before contract.Checkpoint
			if err := json.Unmarshal(head.Checkpoint, &before); err != nil {
				t.Fatal(err)
			}
			turns := chat.calls
			prepared, events, err := PreparePlanDecision(context.Background(), input, expected)
			if err != nil {
				t.Fatal(err)
			}
			if len(events) != 1 || events[0].PlanDriftDecided == nil || store.decisions != 1 || prepared.Approval != nil || prepared.Persistence != input.Persistence || chat.calls != turns {
				t.Fatal("preparation did not acknowledge exactly once without a session")
			}
			after := prepared.Execution
			if !reflect.DeepEqual(before.Durable, after.Durable) || !reflect.DeepEqual(before.Operations, after.Operations) || !reflect.DeepEqual(before.Policy, after.Policy) || before.Limit != after.Limit || !reflect.DeepEqual(before.Grants, after.Grants) {
				t.Fatal("decision changed journal, policy or allowance")
			}
			if after.Status != "running" {
				t.Fatal("acknowledged decision did not open dispatch")
			}
			want := 2
			if approve {
				want = 1
			}
			if len(after.Plan) != want {
				t.Fatal("wrong canonical decision")
			}
			if _, _, err = PreparePlanDecision(context.Background(), input, expected); err == nil || store.decisions != 1 {
				t.Fatal("decision replayed")
			}
			// The actual next context opens its only turn and cannot decide a second time.
			session.Context = prepared
			opened := openRun(t, session)
			defer opened.Context.Close()
			if chat.calls != turns+1 || opened.Context.Execution.State().PlanState.Get("decisions").Len() != 1 || store.decisions != 1 {
				t.Fatal("adapter initialization repeated preparation")
			}
			prepared.Execution.Status = "paused"
			if input.Persistence.Writer.Checkpoint().Status != "running" {
				t.Fatal("returned checkpoint aliases writer")
			}
		})
	}
}
func TestPreparePlanDecisionRefusesStaleOrUnauthorizedInputs(t *testing.T) {
	for _, name := range []string{"revision", "candidate", "call", "original", "reference", "simultaneous", "policy", "missing-original"} {
		t.Run(name, func(t *testing.T) {
			_, input, expected, store, _ := pendingPreparation(t, name == "reference")
			switch name {
			case "revision":
				expected.Revision = "stale"
			case "candidate":
				expected.CandidateRevision = "stale"
			case "call":
				input.Approval.CallID = "stale"
			case "original":
				input.Plan = input.Plan.Clone()
				input.Plan.Set("objective", String("changed"))
			case "missing-original":
				input.Plan = nil
			case "reference":
				input.PlanResolver = &PlanResolver{Resolve: func(context.Context, string, string) (ResolvedPlanReference, error) {
					return ResolvedPlanReference{Kind: "artifact", ID: "dependency", Digest: strings.Repeat("c", 64)}, nil
				}}
			case "simultaneous":
				input.Execution.Approval = &contract.CheckpointApproval{CallID: "tool", Name: "tool", Tool: "tool", State: "asked"}
			case "policy":
				delete(input.Execution.Policy, "outputPatterns")
			}
			if _, _, err := PreparePlanDecision(context.Background(), input, expected); err == nil {
				t.Fatal("invalid preparation accepted")
			}
			if store.decisions != 0 {
				t.Fatal("refusal published a decision")
			}
		})
	}
}
func TestPreparePlanDecisionUnknownAckRequiresRetainedRead(t *testing.T) {
	session, input, expected, store, _ := pendingPreparation(t, false)
	store.lose = true
	_, _, err := PreparePlanDecision(context.Background(), input, expected)
	var unknown *StorageOutcomeUnknown
	if !errors.As(err, &unknown) || store.decisions != 1 {
		t.Fatalf("want persistence uncertainty: %v", err)
	}
	if _, _, err = PreparePlanDecision(context.Background(), input, expected); err == nil || store.decisions != 1 {
		t.Fatal("uncertain decision automatically replayed")
	}
	head, err := store.Load(context.Background(), "prepare-plan")
	if err != nil {
		t.Fatal(err)
	}
	v := MustParseJSON(string(head.Checkpoint))
	if planText(v.Get("status")) != "running" || v.Get("planState").Get("decisions").Len() != 1 {
		t.Fatal("retained decision unavailable for reconciliation")
	}
	// Reconcile by accepting the retained read, retiring the uncertain writer and
	// resuming without Approval. There is no second decision publication or retry.
	var acknowledged contract.Checkpoint
	if err = json.Unmarshal(head.Checkpoint, &acknowledged); err != nil {
		t.Fatal(err)
	}
	resumed := *session.Context
	resumed.Execution = &acknowledged
	resumed.Approval = nil
	resumed.RunStore = &DurableRunInput{ID: "prepare-plan", Store: store, Resume: true}
	session.Context = &resumed
	opened := openRun(t, session)
	defer opened.Context.Close()
	if store.decisions != 1 || opened.Context.Execution.State().PlanState.Get("decisions").Len() != 1 {
		t.Fatal("retained-read reconciliation replayed a decision")
	}
}
func TestPreparePlanDecisionCompetingWriterCAS(t *testing.T) {
	_, input, expected, store, _ := pendingPreparation(t, false)
	head, _ := store.Load(context.Background(), "prepare-plan")
	store.before = func() {
		store.before = nil
		_, err := store.RunStore.Commit(context.Background(), RunCommit{RunID: "prepare-plan", ExpectedRevision: head.Revision, CommitID: "fixture-competitor", Checkpoint: head.Checkpoint})
		if err != nil {
			t.Fatal(err)
		}
	}
	_, _, err := PreparePlanDecision(context.Background(), input, expected)
	var conflict *StorageConflict
	if !errors.As(err, &conflict) {
		t.Fatalf("competing CAS accepted: %v", err)
	}
}
func TestPendingPlanDecisionRequiresPublisher(t *testing.T) {
	c := &ExecutionController{planDecisionEvent: &contract.RunEvent{PlanDriftDecided: &contract.RunEventPlanDriftDecided{Type: "plan_drift_decided"}}}
	if err := c.PublishPlanDecision(context.Background()); err == nil {
		t.Fatal("missing publisher claimed decision success")
	}
}

func TestPreparePlanDecisionPreservesRetainedClaimsAndLexicalEvidence(t *testing.T) {
	_, input, expected, store, _ := pendingPreparation(t, false)
	head, _ := store.Load(context.Background(), "prepare-plan")
	var cp contract.Checkpoint
	if err := json.Unmarshal(head.Checkpoint, &cp); err != nil {
		t.Fatal(err)
	}
	cp.Durable.Dispatches[0].Result = json.RawMessage(`{"opaque":9007199254740993,"content":[]}`)
	claim := cp.Durable.Dispatches[0]
	claim.ID = "claimed-fixture"
	claim.State = "claimed"
	claim.Result = nil
	cp.Durable.Dispatches = append(cp.Durable.Dispatches, claim)
	raw, _ := json.Marshal(cp)
	value := MustParseJSON(string(raw))
	digest := RecordDigest(value)
	cp.Digest = &digest
	raw, _ = json.Marshal(cp)
	if _, err := store.Commit(context.Background(), RunCommit{RunID: "prepare-plan", ExpectedRevision: head.Revision, CommitID: "retained-crash-fixture", Checkpoint: raw}); err != nil {
		t.Fatal(err)
	}
	input.Execution = &cp
	input.Persistence = nil
	input.Checkpoint = nil
	input.RunStore = &DurableRunInput{ID: "prepare-plan", Store: store, Resume: true}
	prepared, _, err := PreparePlanDecision(context.Background(), input, expected)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Execution.Durable.Dispatches[2].State != "claimed" || string(prepared.Execution.Durable.Dispatches[0].Result) != string(cp.Durable.Dispatches[0].Result) {
		t.Fatal("decision normalized retained journal or lexical evidence")
	}
	// Recovery's private in-memory uncertainty is separate from the pure decision record.
	if prepared.Persistence.Writer.book.Dispatches[2].State != "unknown" {
		t.Fatal("ordinary recovery was weakened")
	}
	after, _ := store.Load(context.Background(), "prepare-plan")
	var beforeMembers, afterMembers map[string]json.RawMessage
	_ = json.Unmarshal(raw, &beforeMembers)
	_ = json.Unmarshal(after.Checkpoint, &afterMembers)
	if !reflect.DeepEqual(beforeMembers["durable"], afterMembers["durable"]) {
		t.Fatal("decision transaction changed raw retained journal")
	}
}

func TestPreparePlanDecisionRetainedPolicyAndSimultaneousApprovalRefuse(t *testing.T) {
	for _, mode := range []string{"missing-policy", "invalid-policy", "simultaneous"} {
		t.Run(mode, func(t *testing.T) {
			_, input, expected, store, _ := pendingPreparation(t, false)
			head, _ := store.Load(context.Background(), "prepare-plan")
			var cp contract.Checkpoint
			_ = json.Unmarshal(head.Checkpoint, &cp)
			switch mode {
			case "missing-policy":
				delete(cp.Policy, "outputPatterns")
			case "invalid-policy":
				member := cp.Policy["records.redactOnStore"]
				member.Value = json.RawMessage(`"invalid"`)
				cp.Policy["records.redactOnStore"] = member
			case "simultaneous":
				cp.Approval = &contract.CheckpointApproval{CallID: "tool", Name: "tool", Tool: "tool", State: "asked", Args: map[string]json.RawMessage{}, Source: contract.ToolOfferSourceRecord{Kind: "custom", ID: "fixture", Trust: "person"}}
			}
			raw, _ := json.Marshal(cp)
			value := MustParseJSON(string(raw))
			digest := RecordDigest(value)
			cp.Digest = &digest
			raw, _ = json.Marshal(cp)
			if _, err := ReadExecution(MustParseJSON(string(raw))); err != nil {
				t.Fatalf("fixture invalid before preparation: %v", err)
			}
			if _, err := store.Commit(context.Background(), RunCommit{RunID: "prepare-plan", ExpectedRevision: head.Revision, CommitID: "refusal-fixture", Checkpoint: raw}); err != nil {
				t.Fatal(err)
			}
			input.Execution = &cp
			input.Persistence = nil
			input.Checkpoint = nil
			input.RunStore = &DurableRunInput{ID: "prepare-plan", Store: store, Resume: true}
			if _, _, err := PreparePlanDecision(context.Background(), input, expected); err == nil || store.decisions != 0 {
				t.Fatal("retained policy or simultaneous tool approval was accepted")
			}
		})
	}
}
func TestPreparePlanDecisionRetainsConcurrentOriginalWriterUpdate(t *testing.T) {
	_, input, expected, store, _ := pendingPreparation(t, false)
	calls := 0
	input.Authorize = func(ctx context.Context) error {
		calls++
		if calls == 2 {
			_, err := input.Persistence.Writer.Event(ctx, *input.Persistence, contract.RunEvent{Guardrail: &contract.RunEventGuardrail{Type: "guardrail", Tool: "fixture", Action: "refused", Detail: "retained concurrent notice"}})
			return err
		}
		return nil
	}
	_, _, err := PreparePlanDecision(context.Background(), input, expected)
	var conflict *StorageConflict
	if !errors.As(err, &conflict) || store.decisions != 0 || input.Persistence.Writer.Failed() {
		t.Fatalf("legitimate original-writer update lost: %v", err)
	}
	page, err := store.ReadEvents(context.Background(), "prepare-plan", "", StoragePageLimit)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range page.Items {
		var envelope StoredRunEnvelope
		_ = json.Unmarshal(row.Bytes, &envelope)
		if envelope.Event.Guardrail != nil && envelope.Event.Guardrail.Detail == "retained concurrent notice" {
			found = true
		}
	}
	if !found {
		t.Fatal("original writer update overwritten")
	}
}
