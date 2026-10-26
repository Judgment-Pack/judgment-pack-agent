package runcontext

import (
	"context"
	"encoding/json"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"os"
	"strings"
	"testing"
	"time"
)

func extendedPlanRows(t *testing.T) []*Value {
	t.Helper()
	raw, err := os.ReadFile("../../contract/vectors/plan/extended-checkpoints.json")
	if err != nil {
		t.Fatal(err)
	}
	return MustParseJSON(string(raw)).Get("items").Items()
}
func planProofFixture() (*DurableBook, DurableScope) {
	scope := DurableScope{Execution: "extended-plan", Role: "author"}
	return &DurableBook{Dispatches: []DurableDispatch{{ID: "dispatch-ok", Scope: scope, Kind: "host", Source: "host", Tool: "inspect", State: "result", Read: true, Result: json.RawMessage(`{"content":[]}`)}}, Observations: []DurableObservation{{ID: "observed-operation", Scope: scope, Kind: "operation", DispatchID: "dispatch-ok"}, {ID: "observed-artifact", Scope: scope, Kind: "artifact-read", DispatchID: "dispatch-ok", Artifact: &ArtifactObservation{Kind: "artifact", ID: "artifact-1", Digest: "sha256:" + strings.Repeat("b", 64)}}}}, scope
}
func planClosureArgs(rows []*Value) *Value {
	return ObjectOf("id", String("step-1"), "expectedRevision", rows[1].Get("planState").Get("revision").Clone(), "outcome", String("completed"), "evidence", rows[2].Get("plan").Items()[0].Get("evidence").Clone())
}
func TestPlanExtendedVectorsAndClosure(t *testing.T) {
	rows := extendedPlanRows(t)
	for _, r := range rows {
		cp, err := ReadExecution(r)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(cp)
		if CanonicalJSON(MustParseJSON(string(raw))) != CanonicalJSON(r) {
			t.Fatal("extended checkpoint changed")
		}
	}
	raw, err := os.ReadFile("../../contract/vectors/plan/extended-events.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range MustParseJSON(string(raw)).Get("items").Items() {
		var event contract.RunEvent
		if err = json.Unmarshal(e.JSON(), &event); err != nil {
			t.Fatal(err)
		}
		out, _ := json.Marshal(event)
		if CanonicalJSON(MustParseJSON(string(out))) != CanonicalJSON(e) {
			t.Fatal("extended event changed")
		}
	}
	book, scope := planProofFixture()
	plan, state, reason := closePlan(rows[1].Get("plan"), rows[1].Get("planState"), planClosureArgs(rows), book, scope)
	if reason != "" || CanonicalJSON(plan) != CanonicalJSON(rows[2].Get("plan")) || CanonicalJSON(state) != CanonicalJSON(rows[2].Get("planState")) {
		t.Fatalf("closure differs: %s", reason)
	}
	args := ObjectOf("id", String("step-2"), "expectedRevision", rows[3].Get("planState").Get("revision"), "outcome", String("not-needed"), "reason", String("The recorded inspection already includes the report."))
	plan, state, reason = closePlan(rows[3].Get("plan"), rows[3].Get("planState"), args, nil, scope)
	if reason != "" || CanonicalJSON(plan) != CanonicalJSON(rows[4].Get("plan")) || CanonicalJSON(state) != CanonicalJSON(rows[4].Get("planState")) {
		t.Fatalf("not-needed differs: %s", reason)
	}
}
func TestPlanEvidenceRefusalsAreAtomic(t *testing.T) {
	rows := extendedPlanRows(t)
	for _, tc := range []struct {
		reason string
		change func(*DurableBook)
	}{
		{"unknown-operation", func(b *DurableBook) { b.Dispatches = nil }},
		{"unsuccessful-operation", func(b *DurableBook) { b.Dispatches[0].State = "unknown" }},
		{"unsuccessful-operation", func(b *DurableBook) { b.Dispatches[0].Result = json.RawMessage(`{"isError":true}`) }},
		{"unsuccessful-operation", func(b *DurableBook) { b.Dispatches[0].Kind = "model" }},
		{"unsuccessful-operation", func(b *DurableBook) { b.Observations = nil }},
		{"wrong-scope", func(b *DurableBook) { b.Dispatches[0].Scope.Role = "critic" }},
		{"wrong-scope", func(b *DurableBook) { b.Dispatches[0].Scope.Task = "other-child" }},
		{"unobserved-revision", func(b *DurableBook) { b.Observations[1].Artifact.ID = "same-digest-other-id" }},
		{"unobserved-revision", func(b *DurableBook) { b.Observations[1].Artifact.Digest = "sha256:" + strings.Repeat("c", 64) }},
	} {
		book, scope := planProofFixture()
		tc.change(book)
		before := CanonicalJSON(rows[1])
		_, _, reason := closePlan(rows[1].Get("plan"), rows[1].Get("planState"), planClosureArgs(rows), book, scope)
		if reason != tc.reason || CanonicalJSON(rows[1]) != before || planText(rows[1].Get("plan").Items()[0].Get("status")) != "in_progress" {
			t.Fatalf("atomic refusal want %s got %s", tc.reason, reason)
		}
	}
	_, scope := planProofFixture()
	_, _, reason := closePlan(rows[1].Get("plan"), rows[1].Get("planState"), planClosureArgs(rows), nil, scope)
	if reason != "evidence-unavailable" {
		t.Fatal(reason)
	}
}
func TestPlanArtifactOnlyFailureRefuses(t *testing.T) {
	rows := extendedPlanRows(t)
	book, scope := planProofFixture()
	book.Dispatches[0].Result = json.RawMessage(`{"isError":true}`)
	args := planClosureArgs(rows)
	args.Get("evidence").Set("operations", Array())
	_, _, reason := closePlan(rows[1].Get("plan"), rows[1].Get("planState"), args, book, scope)
	if reason != "unobserved-revision" {
		t.Fatal(reason)
	}
}
func TestPlanClosureInputRevisionAndActiveGuards(t *testing.T) {
	rows := extendedPlanRows(t)
	book, scope := planProofFixture()
	for _, tc := range []struct {
		reason string
		change func(*Value)
	}{
		{"invalid-input", func(a *Value) { a.Set("evidence", Object()) }},
		{"invalid-input", func(a *Value) { a.Set("evidence", ObjectOf("operations", Array(String("")), "artifacts", Array())) }},
		{"invalid-input", func(a *Value) {
			a.Set("evidence", ObjectOf("operations", Array(String("dispatch-ok"), String("dispatch-ok")), "artifacts", Array()))
		}},
		{"invalid-input", func(a *Value) { a.Set("reason", String("Also a reason")) }},
		{"invalid-input", func(a *Value) { a.Set("outcome", String("not-needed")); a.Set("reason", String("Unneeded")) }},
		{"missing-evidence", func(a *Value) { a.Set("evidence", ObjectOf("operations", Array(), "artifacts", Array())) }},
		{"revision-changed", func(a *Value) { a.Set("expectedRevision", rows[0].Get("planState").Get("revision")) }},
		{"active-step-required", func(a *Value) { a.Set("id", String("step-2")) }},
	} {
		a := planClosureArgs(rows)
		tc.change(a)
		_, _, reason := closePlan(rows[1].Get("plan"), rows[1].Get("planState"), a, book, scope)
		if reason != tc.reason {
			t.Fatalf("want %s got %s", tc.reason, reason)
		}
	}
	oversized := Array()
	for i := 0; i < 21; i++ {
		oversized.Append(String("operation-" + Number(float64(i)).Text()))
	}
	a := planClosureArgs(rows)
	a.Set("evidence", ObjectOf("operations", oversized, "artifacts", Array()))
	_, _, reason := closePlan(rows[1].Get("plan"), rows[1].Get("planState"), a, book, scope)
	if reason != "invalid-input" {
		t.Fatal(reason)
	}
	a = planClosureArgs(rows)
	a.Set("expectedRevision", rows[2].Get("planState").Get("revision"))
	_, _, reason = closePlan(rows[2].Get("plan"), rows[2].Get("planState"), a, book, scope)
	if reason != "active-step-required" {
		t.Fatal(reason)
	}
}
func TestPlanTerminalCarryAndSemanticGuards(t *testing.T) {
	rows := extendedPlanRows(t)
	current := rows[4]
	input := Array()
	for _, s := range current.Get("plan").Items() {
		input.Append(ObjectOf("id", s.Get("id"), "step", s.Get("step"), "status", s.Get("status")))
	}
	input.Append(ObjectOf("step", String("Follow up"), "status", String("pending")))
	plan, _, err := revisePlan(current.Get("plan"), current.Get("planState"), input, "Extend")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if CanonicalJSON(plan.Items()[i]) != CanonicalJSON(current.Get("plan").Items()[i]) {
			t.Fatal("terminal proof changed")
		}
	}
	for _, change := range []func(*Value){func(v *Value) { v.Items()[0].Set("step", String("Reword")) }, func(v *Value) { v.Items()[0].Set("status", String("pending")) }} {
		v := input.Clone()
		change(v)
		if _, _, err = revisePlan(current.Get("plan"), current.Get("planState"), v, "Refuse"); err == nil || !strings.Contains(err.Error(), "PlanCompletionActionRequired") {
			t.Fatal(err)
		}
	}
	for _, change := range []func(*Value){func(v *Value) { v.Get("plan").Items()[0].Get("origin").Set("index", Number(19)) }, func(v *Value) {
		v.Get("planState").Get("history").Items()[0].Get("diff").Items()[0].Get("before").Get("origin").Set("plan", String("other-accepted-plan"))
	}, func(v *Value) { v.Get("planState").Get("accepted").Get("steps").a[0] = String("Changed") }, func(v *Value) {
		v.Get("planState").Get("pending").Set("revision", rows[0].Get("planState").Get("revision"))
	}, func(v *Value) {
		v.Get("planState").Get("pending").Get("candidate").Get("planState").Set("pending", Object())
	}, func(v *Value) {
		v.Get("planState").Get("pending").Get("candidate").Get("planState").Set("revision", rows[0].Get("planState").Get("revision"))
	}} {
		v := rows[5].Clone()
		change(v)
		if _, err = ReadExecution(v); err == nil {
			t.Fatal("malformed lineage/candidate accepted")
		}
	}
}
func TestPlanDriftApprovalResumeOnce(t *testing.T) {
	ctx := context.Background()
	rows := extendedPlanRows(t)
	for _, approved := range []bool{true, false} {
		session, _ := trackedSession(t)
		store := NewMemoryRunStore(MemoryRunStoreOptions{})
		session.Context.RunStore = &DurableRunInput{Store: store, ID: "extended-plan"}
		a := rows[0].Get("planState").Get("accepted")
		session.Context.Plan = ObjectOf("id", a.Get("id"), "revision", a.Get("sourceRevision"), "objective", a.Get("objective"), "steps", a.Get("steps"), "questions", Array(), "references", Array(), "status", String("accepted"), "createdAt", a.Get("acceptedAt"), "acceptedAt", a.Get("acceptedAt"))
		run := openRun(t, session)
		controller := run.Context.Execution
		original := controller.State().Plan.Clone()
		id := planText(original.Items()[1].Get("id"))
		args := ObjectOf("explanation", String("Remove accepted work"), "plan", Array(ObjectOf("id", String(id), "step", String("Report"), "status", String("pending"))))
		if _, err := hostTool(t, run, "update_plan").Execute(ctx, args.JSON()); err != nil {
			t.Fatal(err)
		}
		if CanonicalJSON(controller.State().Plan) != CanonicalJSON(original) || !controller.ApprovalAsked() {
			t.Fatal("drift changed plan before decision")
		}
		pending := controller.State().PlanState.Get("pending").Clone()
		run.Context.Close()
		session.Context.RunStore.Resume = true
		session.Context.Approval = &contract.CheckpointApprovalDecision{CallID: "different", Approved: approved}
		if _, err := ContextualSession(ctx, session); err == nil {
			t.Fatal("wrong decision accepted")
		}
		session.Context.Approval.CallID = planText(pending.Get("callId"))
		continued := openRun(t, session)
		want := 2
		if approved {
			want = 1
		}
		if continued.Context.Execution.State().Plan.Len() != want || continued.Context.Execution.State().PlanState.Has("pending") || continued.Context.Execution.State().PlanState.Get("decisions").Len() != 1 {
			t.Fatal("decision not applied exactly once")
		}
		notices := continued.Context.TakeNotices()
		count := 0
		for _, e := range notices {
			if e.PlanDriftDecided != nil {
				count++
			}
		}
		if count != 1 {
			t.Fatal("decision event missing")
		}
		head, _ := store.Load(ctx, "extended-plan")
		if MustParseJSON(string(head.Checkpoint)).Get("planState").Get("decisions").Len() != 1 {
			t.Fatal("decision not persisted before dispatch")
		}
		continued.Context.Close()
		if _, err := ContextualSession(ctx, session); err == nil {
			t.Fatal("repeated decision accepted")
		}
	}
}

func TestPlanActualHostEvidenceAndResume(t *testing.T) {
	ctx := context.Background()
	session, _ := trackedSession(t)
	store := NewMemoryRunStore(MemoryRunStoreOptions{})
	session.Context.RunStore = &DurableRunInput{Store: store, ID: "extended-plan"}
	session.HostTools = []HostTool{{Name: "inspect", Taxonomy:&ToolTaxonomy{Operation:"read",Effect:"read"}, Effect: "read", Description: "Inspect", InputSchema: json.RawMessage(`{"type":"object"}`), Execute: func(context.Context, json.RawMessage) (ToolResult, error) {
		return ResultOf([]Content{TextContent("Inspected")}, nil), nil
	}, Artifacts: func(ToolResult, json.RawMessage) []ArtifactObservation {
		return []ArtifactObservation{{Kind: "artifact", ID: "artifact-1", Digest: "sha256:" + strings.Repeat("b", 64)}}
	}}}
	run := openRun(t, session)
	controller := run.Context.Execution
	if _, err := hostTool(t, run, "update_plan").Execute(ctx, json.RawMessage(`{"explanation":"Inspect","plan":[{"step":"Inspect","status":"in_progress"}]}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := hostTool(t, run, "inspect").Execute(ctx, json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	book, _ := controller.PlanProof()
	id := ""
	for _, d := range book.Dispatches {
		if d.Tool == "inspect" {
			id = d.ID
		}
	}
	if id == "" || !strings.Contains(controller.RequestState(), id) {
		t.Fatal("acknowledged operation unavailable to model")
	}
	args := ObjectOf("id", controller.State().Plan.Items()[0].Get("id"), "expectedRevision", controller.State().PlanState.Get("revision"), "outcome", String("completed"), "evidence", ObjectOf("operations", Array(String(id)), "artifacts", Array(ObjectOf("kind", String("artifact"), "id", String("artifact-1"), "digest", String("sha256:"+strings.Repeat("b", 64))))))
	if _, err := hostTool(t, run, "complete_plan_step").Execute(ctx, args.JSON()); err != nil {
		t.Fatal(err)
	}
	evidence := CanonicalJSON(controller.State().Plan.Items()[0].Get("evidence"))
	run.Context.Close()
	session.Context.RunStore.Resume = true
	again := openRun(t, session)
	defer again.Context.Close()
	if CanonicalJSON(again.Context.Execution.State().Plan.Items()[0].Get("evidence")) != evidence {
		t.Fatal("recorded evidence lost on resume")
	}
}
func TestPlanCompetingClosures(t *testing.T) {
	ctx := context.Background()
	session, _ := trackedSession(t)
	run := openRun(t, session)
	defer run.Context.Close()
	rows := extendedPlanRows(t)
	c := run.Context.Execution
	if err := c.MutateCheckpoint(CheckpointPatch{"Plan": rows[1].Get("plan"), "PlanState": rows[1].Get("planState")}); err != nil {
		t.Fatal(err)
	}
	c.PlanProof = planProofFixture
	done := make(chan error, 2)
	args := planClosureArgs(rows)
	other := ObjectOf("id", args.Get("id"), "expectedRevision", args.Get("expectedRevision"), "outcome", String("not-needed"), "reason", String("No further work"))
	go func() { _, err := c.CompletionTool().Execute(ctx, args.JSON()); done <- err }()
	go func() { _, err := c.CompletionTool().Execute(ctx, other.JSON()); done <- err }()
	first, second := <-done, <-done
	if (first == nil) == (second == nil) || planNumber(c.State().PlanState.Get("sequence")) != 2 || planText(c.State().PlanState.Get("refusals").Items()[0].Get("reason")) != "revision-changed" {
		t.Fatalf("closures not serialized: %v %v", first, second)
	}
}
func TestPlanCallbackEvidenceAndRefusalResume(t *testing.T) {
	session, _ := trackedSession(t)
	run := openRun(t, session)
	defer run.Context.Close()
	c := run.Context.Execution
	rows := extendedPlanRows(t)
	c.MutateCheckpoint(CheckpointPatch{"Plan": rows[1].Get("plan"), "PlanState": rows[1].Get("planState")})
	c.PlanProof = nil
	if _, err := c.CompletionTool().Execute(context.Background(), planClosureArgs(rows).JSON()); err == nil || !strings.Contains(err.Error(), "PlanEvidenceUnavailable") {
		t.Fatal(err)
	}
	for i := 0; i < 35; i++ {
		if _, err := c.CompletionTool().Execute(context.Background(), json.RawMessage(`{}`)); err == nil {
			t.Fatal("empty completion accepted")
		}
	}
	if c.State().PlanState.Get("refusals").Len() != 32 || planNumber(c.State().PlanState.Get("refusalsDiscarded")) != 4 || planNumber(c.State().PlanState.Get("sequence")) != 1 || planText(c.State().Plan.Items()[0].Get("status")) != "in_progress" {
		t.Fatal("refusal altered active plan or retention")
	}
	cp, err := c.Snapshot("stopped")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(cp)
	if _, err = ReadExecution(MustParseJSON(string(raw))); err != nil {
		t.Fatal(err)
	}
}

func TestPlanPinValidationBeforeWorkAndResume(t *testing.T) {
	ctx := context.Background()
	session, _ := trackedSession(t)
	store := NewMemoryRunStore(MemoryRunStoreOptions{})
	session.Context.RunStore = &DurableRunInput{Store: store, ID: "extended-plan"}
	a := extendedPlanRows(t)[0].Get("planState").Get("accepted")
	session.Context.Plan = ObjectOf("id", a.Get("id"), "revision", a.Get("sourceRevision"), "objective", a.Get("objective"), "steps", a.Get("steps"), "questions", Array(), "references", Array(ObjectOf("kind", String("artifact"), "id", String("dependency"), "digest", String(strings.Repeat("b", 64)))), "status", String("accepted"), "createdAt", a.Get("acceptedAt"), "acceptedAt", a.Get("acceptedAt"))
	if _, err := ContextualSession(ctx, session); err == nil || !strings.Contains(err.Error(), "PlanResolverRequired") {
		t.Fatal(err)
	}
	changed := true
	session.Context.PlanResolver = &PlanResolver{Resolve: func(context.Context, string, string) (ResolvedPlanReference, error) {
		digest := strings.Repeat("b", 64)
		if changed {
			digest = strings.Repeat("c", 64)
		}
		return ResolvedPlanReference{Kind: "artifact", ID: "dependency", Digest: digest}, nil
	}}
	if _, err := ContextualSession(ctx, session); err == nil || !strings.Contains(err.Error(), "PlanReferenceChanged") {
		t.Fatal(err)
	}
	changed = false
	run := openRun(t, session)
	c := run.Context.Execution
	args := ObjectOf("explanation", String("Track accepted work"), "plan", Array())
	for _, s := range c.State().Plan.Items() {
		args.Get("plan").Append(ObjectOf("id", s.Get("id"), "step", s.Get("step"), "status", s.Get("status")))
	}
	if _, err := c.Tool().Execute(ctx, args.JSON()); err != nil {
		t.Fatal(err)
	}
	run.Context.Close()
	changed = true
	session.Context.RunStore.Resume = true
	if _, err := ContextualSession(ctx, session); err == nil || !strings.Contains(err.Error(), "PlanReferenceChanged") {
		t.Fatal(err)
	}
}

func TestPlanStoreCASAndRolloverLineage(t *testing.T) {
	ctx := context.Background()
	session, _ := trackedSession(t)
	store := NewMemoryRunStore(MemoryRunStoreOptions{})
	session.Context.RunStore = &DurableRunInput{Store: store, ID: "extended-plan"}
	first := openRun(t, session)
	if _, err := first.Context.Execution.Tool().Execute(ctx, json.RawMessage(`{"explanation":"Initial","plan":[{"step":"Inspect","status":"in_progress"}]}`)); err != nil {
		t.Fatal(err)
	}
	first.Context.Close()
	session.Context.RunStore.Resume = true
	a, b := openRun(t, session), openRun(t, session)
	id := a.Context.Execution.State().Plan.Items()[0].Get("id")
	done := make(chan error, 2)
	for i, run := range []*Run{a, b} {
		go func(index int, r *Run) {
			args := ObjectOf("explanation", String("Editor"+Number(float64(index)).Text()), "plan", Array(ObjectOf("id", id, "step", String("Inspect"), "status", String("in_progress"), "detail", String(Number(float64(index)).Text()))))
			_, err := r.Context.Execution.Tool().Execute(ctx, args.JSON())
			done <- err
		}(i, run)
	}
	one, two := <-done, <-done
	if (one == nil) == (two == nil) {
		t.Fatalf("CAS not enforced: %v %v", one, two)
	}
	head, _ := store.Load(ctx, "extended-plan")
	if planNumber(MustParseJSON(string(head.Checkpoint)).Get("planState").Get("sequence")) != 2 {
		t.Fatal("CAS loser overwrote plan")
	}
	a.Context.Close()
	b.Context.Close()
	// The accepted snapshot and origin survive eviction of the initial plan history.
	rows := extendedPlanRows(t)
	cSession, _ := trackedSession(t)
	run := openRun(t, cSession)
	defer run.Context.Close()
	c := run.Context.Execution
	c.MutateCheckpoint(CheckpointPatch{"Plan": rows[0].Get("plan"), "PlanState": rows[0].Get("planState")})
	accepted := CanonicalJSON(c.State().PlanState.Get("accepted"))
	for i := 0; i < 35; i++ {
		plan, state, err := revisePlan(c.State().Plan, c.State().PlanState, c.State().Plan, "No content change")
		if err != nil {
			t.Fatal(err)
		}
		c.MutateCheckpoint(CheckpointPatch{"Plan": plan, "PlanState": state})
	}
	if c.State().PlanState.Get("history").Len() != 32 || planNumber(c.State().PlanState.Get("discarded")) != 3 || CanonicalJSON(c.State().PlanState.Get("accepted")) != accepted {
		t.Fatal("accepted lineage lost to history eviction")
	}
	cp, err := c.Snapshot("stopped")
	if err != nil {
		t.Fatal(err)
	}
	cSession.Context.Execution = &cp
	again := openRun(t, cSession)
	defer again.Context.Close()
	if CanonicalJSON(again.Context.Execution.State().PlanState.Get("accepted")) != accepted || again.Context.Execution.State().PlanState.Get("accepted").Get("validatedReferences") == nil {
		t.Fatal("accepted content/pin checks lost on resume")
	}
}

type planAckStore struct {
	RunStore
	entered chan struct{}
	release chan struct{}
}

func (s *planAckStore) Commit(ctx context.Context, tx RunCommit) (RunReceipt, error) {
	for _, raw := range tx.Events {
		v, _ := ParseJSON(raw)
		if planText(v.Get("event").Get("type")) == "plan_revised" {
			close(s.entered)
			select {
			case <-s.release:
			case <-ctx.Done():
				return RunReceipt{}, ctx.Err()
			}
		}
	}
	return s.RunStore.Commit(ctx, tx)
}
func TestPlanReturnsOnlyAfterAtomicAcknowledgement(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	store := &planAckStore{RunStore: NewMemoryRunStore(MemoryRunStoreOptions{}), entered: make(chan struct{}), release: make(chan struct{})}
	session, _ := trackedSession(t)
	session.Context.RunStore = &DurableRunInput{Store: store, ID: "extended-plan"}
	run := openRun(t, session)
	defer run.Context.Close()
	prior, _ := store.Load(ctx, "extended-plan")
	done := make(chan error, 1)
	go func() {
		_, err := run.Context.Execution.Tool().Execute(ctx, json.RawMessage(`{"explanation":"Track","plan":[{"step":"Inspect","status":"pending"}]}`))
		done <- err
	}()
	select {
	case <-store.entered:
	case <-ctx.Done():
		t.Fatal("plan acknowledgement seam not reached")
	}
	select {
	case err := <-done:
		t.Fatalf("returned before acknowledgement: %v", err)
	default:
	}
	headBefore, _ := store.Load(ctx, "extended-plan")
	if (headBefore == nil) != (prior == nil) || headBefore != nil && headBefore.Revision != prior.Revision {
		t.Fatal("unacknowledged plan replaced original head")
	}
	close(store.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	head, _ := store.Load(ctx, "extended-plan")
	if head == nil || head.Cursor == "" {
		t.Fatal("atomic plan checkpoint/event missing")
	}
}

func TestPlanManagedApprovalDeliveredOnce(t *testing.T) {
	ctx := context.Background()
	session, _ := trackedSession(t)
	store := NewMemoryRunStore(MemoryRunStoreOptions{})
	session.Context.RunStore = &DurableRunInput{Store: store, ID: "extended-plan"}
	a := extendedPlanRows(t)[0].Get("planState").Get("accepted")
	session.Context.Plan = ObjectOf("id", a.Get("id"), "revision", a.Get("sourceRevision"), "objective", a.Get("objective"), "steps", a.Get("steps"), "questions", Array(), "references", Array(), "status", String("accepted"), "createdAt", a.Get("acceptedAt"), "acceptedAt", a.Get("acceptedAt"))
	effects := 0
	session.HostTools = []HostTool{{Name: "effect", Taxonomy:&ToolTaxonomy{Operation:"own-state",Effect:"pure"}, Description: "Effect", InputSchema: json.RawMessage(`{"type":"object"}`), Execute: func(context.Context, json.RawMessage) (ToolResult, error) {
		effects++
		return ResultOf([]Content{}, nil), nil
	}}}
	run := openRun(t, session)
	defer run.Context.Close()
	if _, err := hostTool(t, run, "update_plan").Execute(ctx, json.RawMessage(`{"explanation":"Narrow accepted work","plan":[{"id":"step-2","step":"Report","status":"pending"}]}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := hostTool(t, run, "effect").Execute(ctx, json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), "PlanApprovalRequired") {
		t.Fatal(err)
	}
	boundary := NewBoundary(run)
	out, _ := boundary.Event(contract.RunEvent{ToolResult: &contract.ToolResult{Type: "tool_result", Name: "update_plan", Text: "approval-required"}})
	last, _ := boundary.Event(contract.RunEvent{End: &contract.RunEventEnd{Type: "end"}})
	out = append(out, last...)
	count := 0
	for _, e := range out {
		if e.ApprovalRequested != nil {
			count++
			if e.ApprovalRequested.Purpose == nil || *e.ApprovalRequested.Purpose != "plan-drift" {
				t.Fatal("plan approval acquired tool authority")
			}
		}
	}
	if count != 1 || effects != 0 {
		t.Fatalf("approval deliveries %d effects %d", count, effects)
	}
	page, _ := store.ReadEvents(ctx, "extended-plan", "", 100)
	count = 0
	for _, row := range page.Items {
		v, _ := ParseJSON(row.Bytes)
		if planText(v.Get("event").Get("type")) == "approval_requested" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("plan approval committed %d times", count)
	}
}

func TestPlanPendingCandidateBindsExactTransition(t *testing.T) {
	for _, change := range []func(*Value){func(s *Value) {
		s.Get("planState").Get("pending").Get("candidate").Get("planState").Set("nextId", Number(9))
	}, func(s *Value) {
		history := s.Get("planState").Get("pending").Get("candidate").Get("planState").Get("history")
		history.Items()[history.Len()-1].Set("explanation", String("Another explanation"))
	}} {
		saved := extendedPlanRows(t)[5].Clone()
		change(saved)
		if _, err := ReadExecution(saved); err == nil {
			t.Fatal("pending approval bound different allocator/history")
		}
	}
}

func TestPlanRepeatedDepartureAsksAgain(t *testing.T) {
	ctx := context.Background()
	session, _ := trackedSession(t)
	store := NewMemoryRunStore(MemoryRunStoreOptions{})
	session.Context.RunStore = &DurableRunInput{Store: store, ID: "extended-plan"}
	session.Context.PlanDrift = []string{"changed"}
	a := extendedPlanRows(t)[0].Get("planState").Get("accepted")
	session.Context.Plan = ObjectOf("id", a.Get("id"), "revision", a.Get("sourceRevision"), "objective", a.Get("objective"), "steps", a.Get("steps"), "questions", Array(), "references", Array(), "status", String("accepted"), "createdAt", a.Get("acceptedAt"), "acceptedAt", a.Get("acceptedAt"))
	edit := func(text string) *Value {
		return ObjectOf("explanation", String("Adjust accepted inspection"), "plan", Array(ObjectOf("id", String("step-1"), "step", String(text), "status", String("pending")), ObjectOf("id", String("step-2"), "step", String("Report"), "status", String("pending"))))
	}
	first := openRun(t, session)
	if _, err := first.Context.Execution.Tool().Execute(ctx, edit("Inspect revision A").JSON()); err != nil {
		t.Fatal(err)
	}
	one := planText(first.Context.Execution.State().PlanState.Get("pending").Get("callId"))
	if one == "" {
		t.Fatal("initial text departure was not held")
	}
	first.Context.Close()
	session.Context.RunStore.Resume = true
	session.Context.Approval = &contract.CheckpointApprovalDecision{CallID: one, Approved: true}
	again := openRun(t, session)
	defer again.Context.Close()
	if _, err := again.Context.Execution.Tool().Execute(ctx, edit("Inspect revision B").JSON()); err != nil {
		t.Fatal(err)
	}
	two := again.Context.Execution.State().PlanState.Get("pending")
	if planText(two.Get("callId")) == "" || planText(two.Get("callId")) == one || planText(again.Context.Execution.State().Plan.Items()[0].Get("step")) != "Inspect revision A" || planText(two.Get("candidate").Get("plan").Items()[0].Get("step")) != "Inspect revision B" {
		t.Fatal("repeated departure bypassed a new approval")
	}
}
func TestPlanDefaultReorderRecordsWithoutApproval(t *testing.T) {
	session, _ := trackedSession(t)
	run := openRun(t, session)
	defer run.Context.Close()
	rows := extendedPlanRows(t)
	c := run.Context.Execution
	c.MutateCheckpoint(CheckpointPatch{"Plan": rows[0].Get("plan"), "PlanState": rows[0].Get("planState")})
	before := c.State().Plan.Clone()
	active := before.Clone()
	active.Items()[0].Set("status", String("in_progress"))
	if changedPlanDepartures(before, c.State().PlanState, active).Len() != 0 {
		t.Fatal("status became departure")
	}
	next := Array(before.Items()[1], before.Items()[0])
	args := ObjectOf("explanation", String("Reorder work"), "plan", Array())
	for _, s := range next.Items() {
		args.Get("plan").Append(ObjectOf("id", s.Get("id"), "step", s.Get("step"), "status", s.Get("status")))
	}
	if _, err := c.Tool().Execute(context.Background(), args.JSON()); err != nil {
		t.Fatal(err)
	}
	if c.ApprovalAsked() || c.State().PlanState.Get("departures").Len() != 1 || c.State().PlanState.Get("departures").Items()[0].Get("changes").Len() != 2 {
		t.Fatal("default reorder should record only")
	}
}

func TestPlanIndependentAcceptanceAndOperationAcknowledgement(t *testing.T) {
	rows := extendedPlanRows(t)
	altered := rows[0].Clone()
	altered.Get("planState").Get("accepted").Get("steps").a[0] = String("Changed acceptance")
	if _, err := ReadExecution(altered); err == nil {
		t.Fatal("accepted snapshot content digest was not verified")
	}
	for _, change := range []func(*DurableBook){func(b *DurableBook) { b.Observations = nil }, func(b *DurableBook) { b.Dispatches[0].State = "unknown" }} {
		book, scope := planProofFixture()
		change(book)
		args := planClosureArgs(rows)
		args.Get("evidence").Set("artifacts", Array())
		_, _, reason := closePlan(rows[1].Get("plan"), rows[1].Get("planState"), args, book, scope)
		if reason != "unsuccessful-operation" {
			t.Fatal(reason)
		}
	}
}
