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
func pendingPreparation(t *testing.T, refs bool, modes ...string) (Session, *Input, PlanDecisionExpectation, *preparationStore, *preparationChat) {
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
	mode := ""
	if len(modes) > 0 {
		mode = modes[0]
		session.Context.TaskSteps = true
	}
	calls := 0
	if mode == "standalone" || mode == "coordinator" {
		session.Model = nil
		session.Agent = &AgentSession{Model: "fixture"}
		if mode == "standalone" {
			session.Context.TaskStepEnvelope = &TaskStepEnvelope{ID: "original-envelope", Revision: "sha256:" + strings.Repeat("d", 64), Unit: "provider-invocations", Limit: 6}
		}
		if mode == "coordinator" {
			admitted, err := PrepareDurableInput(ctx, session.Context)
			if err != nil {
				t.Fatal(err)
			}
			session.Context = admitted
			if err = admitted.Persistence.Writer.AttachNativeTaskEnvelope(*admitted.Persistence, NativeTaskEnvelopeSource{Limit: 6, ChildReserve: 1, Counts: func() map[string]int { return map[string]int{"prepare-plan": calls} }, Reserve: func(scope DurableScope) int {
				if scope.Task != "" {
					return 1
				}
				return 0
			}}); err != nil {
				t.Fatal(err)
			}
		}
	}
	run := openRun(t, session)
	if mode != "" {
		if err := task183Update(t, run, `[{"id":"step-1","step":"Inspect","status":"in_progress","budget":{"requests":2}},{"id":"step-2","step":"Report","status":"pending","budget":{"requests":1}}]`); err != nil {
			t.Fatal(err)
		}
		var err error
		if mode == "model" {
			err = task183Model(run, &calls)
		} else {
			err = task183Native(run, &calls)
		}
		if err != nil || calls != 1 {
			t.Fatalf("actual task dispatch missing: calls=%d err=%v", calls, err)
		}
	}
	// Actual completed host dispatch must survive this metadata-only transition exactly.
	_, err := InvokeDurable(ctx, run.Session.Context.Persistence, "host", "fixture", "inspect", []byte(`{}`), true, func([]byte) (ToolResult, error) { return ResultOf([]Content{TextContent("exact evidence")}, nil), nil }, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	id := run.Context.Execution.State().Plan.Items()[1].Get("id")
	proposal := Array(ObjectOf("id", id, "step", String("Report"), "status", String("pending")))
	if mode != "" {
		proposal = Array(ObjectOf("id", String("step-1"), "step", String("Inspect"), "status", String("in_progress"), "budget", ObjectOf("requests", Number(2))))
	}
	_, err = hostTool(t, run, "update_plan").Execute(ctx, ObjectOf("explanation", String("Remove accepted work"), "plan", proposal).JSON())
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

func TestPreparePlanDecisionFeatureCheckpoint3FreshWriter(t *testing.T) {
	for _, mode := range []string{"model", "standalone", "coordinator"} {
		for _, approved := range []bool{true, false} {
			t.Run(mode+map[bool]string{true: "/accept", false: "/reject"}[approved], func(t *testing.T) {
				session, input, expected, store, chat := pendingPreparation(t, false, mode)
				head, err := store.Load(context.Background(), "prepare-plan")
				if err != nil {
					t.Fatal(err)
				}
				before := MustParseJSON(string(head.Checkpoint))
				if before.Get("version").Num() != 3 || before.Get("planState").Get("taskSchedule").Get("rows").Items()[0].Get("claims").Len() != 1 {
					t.Fatal("fixture lacks actual feature-bearing task claim")
				}
				if mode == "coordinator" && before.Get("planState").Get("taskCoordinator") == nil {
					t.Fatal("original coordinator pin not acknowledged")
				}
				input.Persistence = nil
				input.Checkpoint = nil
				input.RunStore = &DurableRunInput{ID: "prepare-plan", Store: store, Resume: true}
				input.Approval.Approved = approved
				turns := chat.calls
				prepared, events, err := PreparePlanDecision(context.Background(), input, expected)
				if err != nil {
					t.Fatalf("valid feature checkpoint3 decision refused: %v", err)
				}
				if len(events) != 1 || events[0].PlanDriftDecided == nil || events[0].PlanDriftDecided.Version == nil || *events[0].PlanDriftDecided.Version != 2 || store.decisions != 1 || chat.calls != turns {
					t.Fatal("feature decision was not acknowledged once as event2 without a turn")
				}
				afterHead, _ := store.Load(context.Background(), "prepare-plan")
				after := MustParseJSON(string(afterHead.Checkpoint))
				if _, err = ReadExecution(after); err != nil {
					t.Fatalf("prepared feature checkpoint invalid: %v", err)
				}
				var oldMembers, newMembers map[string]json.RawMessage
				_ = json.Unmarshal(head.Checkpoint, &oldMembers)
				_ = json.Unmarshal(afterHead.Checkpoint, &newMembers)
				for name, raw := range oldMembers {
					if name != "plan" && name != "planState" && name != "explanation" && name != "status" && name != "digest" && !reflect.DeepEqual(raw, newMembers[name]) {
						t.Fatalf("decision changed retained %s", name)
					}
				}
				for _, name := range []string{"taskCoordinator", "taskEnvelope"} {
					if CanonicalJSON(before.Get("planState").Get(name)) != CanonicalJSON(after.Get("planState").Get(name)) {
						t.Fatalf("decision replaced original %s", name)
					}
				}
				beforeRow := before.Get("planState").Get("taskSchedule").Get("rows").Items()[0]
				if after.Get("planState").Get("taskSchedule") == nil || after.Get("planState").Get("taskSchedule").Get("rows").Len() == 0 {
					t.Fatal("decision lost required task schedule")
				}
				afterRow := after.Get("planState").Get("taskSchedule").Get("rows").Items()[0]
				if CanonicalJSON(beforeRow) != CanonicalJSON(afterRow) || planText(after.Get("planState").Get("taskSchedule").Get("binding").Get("planRevision")) != planText(after.Get("planState").Get("revision")) {
					t.Fatal("decision lost original task claims or schedule revision")
				}
				want := 2
				if approved {
					want = 1
				}
				if after.Get("plan").Len() != want || after.Get("planState").Get("taskSchedule").Get("rows").Len() != want {
					t.Fatal("wrong task decision or schedule")
				}
				page, _ := store.ReadEvents(context.Background(), "prepare-plan", "", StoragePageLimit)
				last := page.Items[len(page.Items)-1]
				stored, err := ReadStoredRunEvent(last.Bytes)
				if err != nil || !reflect.DeepEqual(stored.Event, events[0]) {
					t.Fatal("returned event differs from valid stored event", err)
				}
				if _, _, err = PreparePlanDecision(context.Background(), input, expected); err == nil || store.decisions != 1 {
					t.Fatal("fresh writer decision replayed")
				}
				if mode != "coordinator" {
					session.Context = prepared
					opened := openRun(t, session)
					opened.Context.Close()
					if chat.calls != turns+1 || store.decisions != 1 {
						t.Fatal("continuation redecided or reopened preparation")
					}
				}
			})
		}
	}
}
func TestPreparePlanDecisionRefusesUnsupportedVersions(t *testing.T) {
	for _, version := range []int64{1, 4} {
		t.Run(map[int64]string{1: "legacy", 4: "future"}[version], func(t *testing.T) {
			_, input, expected, store, _ := pendingPreparation(t, false)
			head, _ := store.Load(context.Background(), "prepare-plan")
			cp := input.Execution
			cp.Version = version
			if version == 1 {
				cp.Context.Entries = []contract.CheckpointRetainedEntry{}
				cp.Durable = nil
				cp.PlanState = nil
				cp.Plan = cp.Plan[:0]
			}
			raw, _ := json.Marshal(cp)
			v := MustParseJSON(string(raw))
			digest := RecordDigest(v)
			cp.Digest = &digest
			raw, _ = json.Marshal(cp)
			if version == 1 {
				if _, err := ReadExecution(MustParseJSON(string(raw))); err != nil {
					t.Fatal("valid legacy fixture refused before preparation", err)
				}
			}
			if _, err := store.Commit(context.Background(), RunCommit{RunID: "prepare-plan", ExpectedRevision: head.Revision, CommitID: "unsupported-fixture", Checkpoint: raw}); err != nil {
				t.Fatal(err)
			}
			input.Persistence = nil
			input.Checkpoint = nil
			input.RunStore = &DurableRunInput{ID: "prepare-plan", Store: store, Resume: true}
			if _, _, err := PreparePlanDecision(context.Background(), input, expected); err == nil || store.decisions != 0 {
				t.Fatal("unsupported checkpoint version decided")
			}
		})
	}
}

func TestTaskCoordinatorSemanticBounds(t *testing.T) {
	pin := ObjectOf("version", Number(1), "owner", String("native-delegation"), "run", String("run"), "execution", String("run"), "scopeDigest", String("sha256:"+strings.Repeat("a", 64)), "limit", Number(6), "childReserve", Number(2))
	if _, err := readTaskCoordinatorBinding(pin); err != nil {
		t.Fatal("valid coordinator pin refused", err)
	}
	for _, row := range []struct {
		field string
		value *Value
	}{{"childReserve", Number(7)}, {"childReserve", Number(0)}, {"limit", Number(0)}, {"owner", String("host")}, {"extra", Bool(true)}} {
		invalid := pin.Clone()
		invalid.Set(row.field, row.value)
		if _, err := readTaskCoordinatorBinding(invalid); err == nil {
			t.Fatalf("semantic coordinator guard missing for %s", row.field)
		}
	}
}
func TestTaskCoordinatorOriginalEnrollment(t *testing.T) {
	_, input, _, store, _ := pendingPreparation(t, false, "coordinator")
	head, _ := store.Load(context.Background(), "prepare-plan")
	input.Persistence = nil
	input.Checkpoint = nil
	input.RunStore = &DurableRunInput{ID: "prepare-plan", Store: store, Resume: true}
	admitted, err := PrepareDurableInput(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	source := NativeTaskEnvelopeSource{Limit: 6, ChildReserve: 1, Counts: func() map[string]int { return map[string]int{"prepare-plan": 1} }, Reserve: func(scope DurableScope) int {
		if scope.Task != "" {
			return 1
		}
		return 0
	}}
	for _, field := range []string{"limit", "reserve"} {
		changed := source
		if field == "limit" {
			changed.Limit = 7
		} else {
			changed.ChildReserve = 2
		}
		if err = admitted.Persistence.Writer.AttachNativeTaskEnvelope(*admitted.Persistence, changed); err == nil {
			t.Fatalf("changed original coordinator %s admitted", field)
		}
	}
	if err = admitted.Persistence.Writer.AttachNativeTaskEnvelope(*admitted.Persistence, source); err != nil {
		t.Fatal("exact original coordinator refused", err)
	}
	// Remove both immutable pins so semantic pending-candidate comparison is not the refusal.
	value := MustParseJSON(string(head.Checkpoint))
	removePin := func(state *Value) *Value {
		out := Object()
		for _, name := range state.Keys() {
			if name != "taskCoordinator" {
				out.Set(name, state.Get(name))
			}
		}
		return out
	}
	state := value.Get("planState")
	candidate := state.Get("pending").Get("candidate")
	candidate.Set("planState", removePin(candidate.Get("planState")))
	value.Set("planState", removePin(state))
	value.Set("digest", String(RecordDigest(value)))
	if _, err = ReadExecution(value); err != nil {
		t.Fatal("missing-pin fixture invalid before enrollment", err)
	}
	if _, err = store.Commit(context.Background(), RunCommit{RunID: "prepare-plan", ExpectedRevision: head.Revision, CommitID: "missing-coordinator-fixture", Checkpoint: value.JSON()}); err != nil {
		t.Fatal(err)
	}
	var cp contract.Checkpoint
	if err = json.Unmarshal(value.JSON(), &cp); err != nil {
		t.Fatal(err)
	}
	input.Execution = &cp
	missing, err := PrepareDurableInput(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if err = missing.Persistence.Writer.AttachNativeTaskEnvelope(*missing.Persistence, source); err == nil {
		t.Fatal("missing original coordinator pin adopted on restart")
	}
}
