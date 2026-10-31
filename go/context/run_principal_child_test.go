package runcontext

// ADR-0029: a delegated child gets the link the issuing host narrowed from its parent's, and a run's
// lease, fence and resume keep its principals; the twins of packages/harness/principalChild.test.ts.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

func principalLinks(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	v := loadPrincipalChains(t)
	statements := func(raw json.RawMessage) []json.RawMessage {
		var out []json.RawMessage
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	return map[string]json.RawMessage{
		"root":              statements(v.Items[0])[0],
		"child":             statements(v.Items[1])[1],
		"widened":           statements(v.Refused[0].Chain)[1],
		"grandchild":        statements(v.Refused[1].Chain)[2],
		"otherActor":        statements(v.Items[3])[0],
		"otherSubject":      statements(v.Items[4])[0],
		"relay":             statements(v.Items[6])[1],
		"relayLeaf":         statements(v.Items[7])[2],
		"otherActorChild":   statements(v.Items[8])[1],
		"otherSubjectChild": statements(v.Items[9])[1],
	}
}

// principalChildClock is the host's time within every chain of the vector.
func principalChildClock() (time.Time, error) {
	return time.Parse(time.RFC3339Nano, "2026-10-30T01:00:00.000Z")
}

func principalChainText(t *testing.T, names []string) []byte {
	t.Helper()
	links := principalLinks(t)
	out := []json.RawMessage{}
	for _, name := range names {
		out = append(out, links[name])
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestPrincipalChildVectors(t *testing.T) {
	v := loadPrincipalChains(t)
	var vectors struct {
		Items []json.RawMessage `json:"items"`
		Cases []struct {
			Name   string          `json:"name"`
			Parent []string        `json:"parent"`
			Answer json.RawMessage `json:"answer"`
			Expect json.RawMessage `json:"expect"`
			Asked  bool            `json:"asked"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(principalVector(t, "child.json").JSON(), &vectors); err != nil {
		t.Fatal(err)
	}
	if len(vectors.Cases) == 0 {
		t.Fatal("no cases")
	}
	for _, c := range vectors.Cases {
		t.Run(c.Name, func(t *testing.T) {
			var requests []PrincipalChildRequest
			input := principalInput(v, principalChainText(t, c.Parent), principalAdmitAll, principalChildClock)
			if string(c.Answer) != `"none"` {
				input.Child = func(_ context.Context, request PrincipalChildRequest) ([]byte, error) {
					requests = append(requests, request)
					if string(c.Answer) == `"throw"` {
						return nil, errors.New("port down")
					}
					var named struct {
						Chain []string `json:"chain"`
					}
					if json.Unmarshal(c.Answer, &named) == nil && named.Chain != nil {
						var rest map[string]json.RawMessage
						_ = json.Unmarshal(c.Answer, &rest)
						text, _ := json.Marshal(string(principalChainText(t, named.Chain)))
						rest["chain"] = text
						return json.Marshal(rest)
					}
					return c.Answer, nil
				}
			}
			parent, err := OpenPrincipal(input)
			if err != nil {
				t.Fatal(err)
			}
			child, err := NarrowChild(context.Background(), parent, "task-1", "review", []PrincipalChildTool{{"notes", "read_note"}})
			var opened *OpenedPrincipal
			if err == nil {
				opened, err = OpenChildPrincipal(parent, child)
			}
			var want string
			if json.Unmarshal(c.Expect, &want) == nil {
				var refused *DelegationRefused
				if !errors.As(err, &refused) || refused.Refusal.Name != want {
					t.Fatalf("got %v, want %s", err, want)
				}
			} else {
				var record struct {
					Agent     string `json:"agent"`
					RootActor string `json:"rootActor"`
					Links     int    `json:"links"`
				}
				if err != nil || json.Unmarshal(c.Expect, &record) != nil {
					t.Fatal(err)
				}
				got := opened.Record
				if got.Agent != record.Agent || got.RootActor != record.RootActor || len(got.Chain) != record.Links || got.Chain[0] != parent.Record.Chain[0] {
					t.Fatalf("record %+v", got)
				}
			}
			if !c.Asked {
				if len(requests) != 0 {
					t.Fatalf("asked %+v", requests)
				}
				return
			}
			expected, err := ParseJSON(vectors.Items[0])
			if err != nil || len(requests) != 1 || CanonicalJSON(requests[0].Value()) != CanonicalJSON(expected) {
				t.Fatalf("requests %+v, %v", requests, err)
			}
		})
	}
}

func TestPrincipalChildPortThatNeverAnswers(t *testing.T) {
	v := loadPrincipalChains(t)
	release := make(chan struct{})
	defer close(release)
	input := principalInput(v, v.Items[0], principalAdmitAll, principalChildClock)
	input.Child = func(context.Context, PrincipalChildRequest) ([]byte, error) {
		<-release
		return nil, errors.New("too late")
	}
	parent, err := OpenPrincipal(input)
	if err != nil {
		t.Fatal(err)
	}
	var refused *DelegationRefused
	if _, err := NarrowChild(context.Background(), parent, "task-1", "review", nil); !errors.As(err, &refused) || refused.Refusal.Name != "admission-unknown" {
		t.Fatalf("got %v", err)
	}
}

func principalBindings(t *testing.T) []PrincipalBinding {
	t.Helper()
	var out []PrincipalBinding
	if err := json.Unmarshal(principalVector(t, "binding.json").Get("items").JSON(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestPrincipalBindingVectors(t *testing.T) {
	vector := principalVector(t, "binding.json")
	for _, item := range vector.Get("items").Items() {
		if _, err := readPrincipalBinding(item.JSON()); err != nil {
			t.Fatalf("refused %s", CanonicalJSON(item))
		}
	}
	for _, row := range vector.Get("refused").Items() {
		if _, err := readPrincipalBinding(row.Get("binding").JSON()); err == nil {
			t.Fatalf("admitted: %s", row.Get("problem").Str())
		}
	}
	items := principalBindings(t)
	same := items[0]
	if !SamePrincipalBinding(&same, &items[0]) || !SamePrincipalBinding(nil, nil) || SamePrincipalBinding(&same, nil) {
		t.Fatal("same")
	}
	for i := range items[1:] {
		if SamePrincipalBinding(&same, &items[1+i]) {
			t.Fatalf("%+v", items[1+i])
		}
	}
}

func TestPrincipalLeaseBinding(t *testing.T) {
	items := principalBindings(t)
	binding, otherActor, otherSubject, otherKind := items[0], items[1], items[2], items[3]
	t.Run("binds at the first lease; a takeover is a new epoch of the same principals", func(t *testing.T) {
		at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		table := NewLeaseTable(func() time.Time { return at })
		request := func(owner string, principal *PrincipalBinding) LeaseAcquire {
			return LeaseAcquire{Resource: "run-delegated", Owner: owner, DurationMs: 1000, Principal: principal}
		}
		first, err := table.Acquire(request("worker-a", &binding), "")
		if err != nil || first.Version != 2 || first.Epoch != "1" || !SamePrincipalBinding(first.Principal, &binding) {
			t.Fatalf("%+v %v", first, err)
		}
		if fence := FenceOf(first); fence.Version != 2 || !SamePrincipalBinding(fence.Principal, &binding) {
			t.Fatalf("%+v", fence)
		}
		at = at.Add(time.Second)
		for _, other := range []*PrincipalBinding{&otherActor, &otherSubject, &otherKind, nil} {
			if _, err := table.Acquire(request("worker-b", other), ""); leaseReason(err) != "principal-changed" {
				t.Fatalf("%+v: %v", other, err)
			}
		}
		taken, err := table.Acquire(request("worker-b", &binding), "")
		if err != nil || taken.Epoch != "2" || taken.Owner != "worker-b" {
			t.Fatalf("%+v %v", taken, err)
		}
		forged := FenceOf(taken)
		forged.Principal = &otherActor
		if _, err := table.Check(forged); leaseReason(err) != "principal-changed" {
			t.Fatal(err)
		}
		if _, err := table.Check(LeaseFence{Version: 1, Resource: taken.Resource, Owner: taken.Owner, Epoch: taken.Epoch}); leaseReason(err) != "principal-changed" {
			t.Fatal(err)
		}
		// The two forms are one: a version 2 fence with no principals, and a version 1 fence with some, are unreadable.
		if _, err := table.Check(LeaseFence{Version: 2, Resource: taken.Resource, Owner: taken.Owner, Epoch: taken.Epoch}); leaseReason(err) != "invalid" {
			t.Fatal(err)
		}
		if _, err := table.Check(LeaseFence{Version: 1, Resource: taken.Resource, Owner: taken.Owner, Epoch: taken.Epoch, Principal: &binding}); leaseReason(err) != "invalid" {
			t.Fatal(err)
		}
		if _, err := table.Release(FenceOf(taken)); err != nil {
			t.Fatal(err)
		}
		if _, err := table.Acquire(request("worker-c", &otherActor), ""); leaseReason(err) != "principal-changed" {
			t.Fatal(err)
		}
	})
	t.Run("an acquisition with an unreadable binding is refused, and takes nothing", func(t *testing.T) {
		at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		table := NewLeaseTable(func() time.Time { return at })
		if _, err := table.Acquire(LeaseAcquire{Resource: "run-delegated", Owner: "worker-a", DurationMs: 1000, Principal: &PrincipalBinding{RootActor: "x"}}, ""); leaseReason(err) != "invalid" {
			t.Fatal(err)
		}
		lease, err := table.Acquire(LeaseAcquire{Resource: "run-delegated", Owner: "worker-a", DurationMs: 1000, Principal: &binding}, "")
		if err != nil || lease.Epoch != "1" {
			t.Fatalf("%+v %v", lease, err)
		}
	})
	t.Run("a version 1 lease cannot host a delegated run", func(t *testing.T) {
		at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		table := NewLeaseTable(func() time.Time { return at })
		plain, err := table.Acquire(LeaseAcquire{Resource: "run-plain", Owner: "worker-a", DurationMs: 1000}, "")
		if err != nil || plain.Version != 1 || plain.Principal != nil {
			t.Fatalf("%+v %v", plain, err)
		}
		at = at.Add(time.Second)
		if _, err := table.Acquire(LeaseAcquire{Resource: "run-plain", Owner: "worker-b", DurationMs: 1000, Principal: &binding}, ""); leaseReason(err) != "principal-changed" {
			t.Fatal(err)
		}
	})
	t.Run("the effect host refuses a dispatch under other principals, performing nothing", func(t *testing.T) {
		store := NewMemoryRunStore(MemoryRunStoreOptions{Now: func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }})
		lease, err := store.AcquireLease(context.Background(), LeaseAcquire{Resource: "run-delegated", Owner: "worker-a", DurationMs: 1000, Principal: &binding})
		if err != nil {
			t.Fatal(err)
		}
		host := NewReferenceEffectHost(store)
		operation := EffectOperation{Run: "run-delegated", Operation: "op-1", Source: "notes", Tool: "write_note", RequestDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
		forged := FenceOf(lease)
		forged.Principal = &otherActor
		admission, err := NewEffectAdmission(forged, operation)
		if err != nil {
			t.Fatal(err)
		}
		performed := 0
		perform := func(context.Context) error { performed++; return nil }
		if err := host.Dispatch(context.Background(), admission, perform); leaseReason(err) != "principal-changed" || performed != 0 {
			t.Fatalf("%v, performed %d", err, performed)
		}
		if refusals := host.Refusals(); len(refusals) != 1 || refusals[0].Reason != "principal-changed" {
			t.Fatalf("%+v", refusals)
		}
		admission, err = NewEffectAdmission(FenceOf(lease), operation)
		if err != nil {
			t.Fatal(err)
		}
		if err := host.Dispatch(context.Background(), admission, perform); err != nil || performed != 1 {
			t.Fatalf("%v, performed %d", err, performed)
		}
	})
	t.Run("a store that keeps no principals cannot host a delegated lease, and one that ignores them fails the kit", func(t *testing.T) {
		fence := LeaseFence{Version: 2, Resource: "run-delegated", Owner: "worker-a", Epoch: "1", Principal: &binding}
		if _, err := FencedRunStore(noPrincipalStore{NewMemoryRunStore(MemoryRunStoreOptions{})}, fence, nil); err == nil {
			t.Fatal("hosted")
		}
		if _, err := FencedRunStore(NewMemoryRunStore(MemoryRunStoreOptions{}), fence, nil); err != nil {
			t.Fatal(err)
		}
		err := CheckRunStoreLeaseConformance(context.Background(), func(now func() time.Time) LeaseRunStore {
			return ignoresPrincipalStore{NewMemoryRunStore(MemoryRunStoreOptions{Now: now})}
		})
		var failed *StorageConformanceFailure
		if !errors.As(err, &failed) || failed.Property != "lease.principal.version" {
			t.Fatalf("%v", err)
		}
	})
	t.Run("a store that forgives the fence's principals on its history commits fails the kit by name", func(t *testing.T) {
		for prefix, create := range map[string]func(*MemoryRunStore) LeaseRunStore{
			"lease.principal.history.":  func(store *MemoryRunStore) LeaseRunStore { return forgivesHistory{store} },
			"lease.principal.snapshot.": func(store *MemoryRunStore) LeaseRunStore { return forgivesSnapshot{store} },
		} {
			err := CheckRunStoreLeaseConformance(context.Background(), func(now func() time.Time) LeaseRunStore {
				return create(NewMemoryRunStore(MemoryRunStoreOptions{Now: now}))
			})
			var failed *StorageConformanceFailure
			if !errors.As(err, &failed) || !strings.HasPrefix(failed.Property, prefix) {
				t.Fatalf("%s: %v", prefix, err)
			}
		}
	})
}

// rowFence is a fence as a store that forgives what it was given would read it: the row's principals.
func rowFence(ctx context.Context, store *MemoryRunStore, fence LeaseFence) LeaseFence {
	row, err := store.ReadLease(ctx, fence.Resource)
	if err != nil || row == nil {
		return fence
	}
	fence.Version, fence.Principal = row.Version, row.Principal
	return fence
}

type forgivesHistory struct{ *MemoryRunStore }

func (s forgivesHistory) CommitHistoryFenced(ctx context.Context, commit RunHistoryCommit, fence LeaseFence) (RunHistoryReceipt, error) {
	return s.MemoryRunStore.CommitHistoryFenced(ctx, commit, rowFence(ctx, s.MemoryRunStore, fence))
}

type forgivesSnapshot struct{ *MemoryRunStore }

func (s forgivesSnapshot) CommitContextSnapshotFenced(ctx context.Context, update ContextSnapshotUpdate, commitID string, fence LeaseFence) (ContextSnapshotHead, error) {
	return s.MemoryRunStore.CommitContextSnapshotFenced(ctx, update, commitID, rowFence(ctx, s.MemoryRunStore, fence))
}

type noPrincipalStore struct{ *MemoryRunStore }

func (noPrincipalStore) LeasePrincipals() bool { return false }

type ignoresPrincipalStore struct{ *MemoryRunStore }

func (s ignoresPrincipalStore) AcquireLease(ctx context.Context, request LeaseAcquire) (RunLease, error) {
	request.Principal = nil
	return s.MemoryRunStore.AcquireLease(ctx, request)
}

func TestPrincipalSessionFence(t *testing.T) {
	v := loadPrincipalChains(t)
	clock := func() (time.Time, error) { return time.Parse(time.RFC3339Nano, "2026-10-30T01:00:00.000Z") }
	opened, err := OpenPrincipal(principalInput(v, v.Items[0], principalAdmitAll, clock))
	if err != nil {
		t.Fatal(err)
	}
	holds := opened.Record.Binding()
	other := principalBindings(t)[1]
	open := func(fence *PrincipalBinding, delegated bool) error {
		store := NewMemoryRunStore(MemoryRunStoreOptions{Now: func() time.Time { return time.Date(2026, 10, 30, 1, 0, 0, 0, time.UTC) }})
		lease, err := store.AcquireLease(context.Background(), LeaseAcquire{Resource: "run-1", Owner: "worker-a", DurationMs: 60000, Principal: fence})
		if err != nil {
			t.Fatal(err)
		}
		view, err := FencedRunStore(store, FenceOf(lease), nil)
		if err != nil {
			t.Fatal(err)
		}
		chain := -1
		if delegated {
			chain = 0
		}
		session := principalSession(t, &principalRan{}, chain, nil)
		session.Context.RunStore = &DurableRunInput{Store: view, ID: "run-1"}
		run, err := ContextualSession(context.Background(), session)
		if run != nil {
			run.Context.Close()
		}
		return err
	}
	if err := open(holds, true); err != nil {
		t.Fatal(err)
	}
	if err := open(nil, false); err != nil {
		t.Fatal(err)
	}
	for name, row := range map[string]struct {
		fence     *PrincipalBinding
		delegated bool
	}{"a delegated run under a version 1 lease": {nil, true}, "a lease of others": {&other, true}, "an undelegated run under a delegated lease": {holds, false}} {
		if err := open(row.fence, row.delegated); err == nil || !strings.Contains(err.Error(), "principal-changed") {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

// dropsPrincipalClaim is a store whose claim answers a lease without the principals it was asked to hold.
type dropsPrincipalClaim struct{ durableWaitStore }

func (s dropsPrincipalClaim) ResumeWait(ctx context.Context, claim WaitClaim) (RunWait, RunLease, error) {
	wait, lease, err := s.durableWaitStore.ResumeWait(ctx, claim)
	lease.Version, lease.Principal = 1, nil
	return wait, lease, err
}

func principalParked(t *testing.T, store RunStore, memory *MemoryRunStore, binding *PrincipalBinding) (ResumeInput, ResumeEvidence) {
	t.Helper()
	ctx := context.Background()
	var record any
	if err := json.Unmarshal(principalVector(t, "record.json").Get("items").Items()[0].JSON(), &record); err != nil {
		t.Fatal(err)
	}
	lease, err := memory.AcquireLease(ctx, LeaseAcquire{Resource: "run-approval", Owner: "worker-a", DurationMs: waitHour, Principal: binding})
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := pausedCheckpoint(t, func(c map[string]any) { c["version"], c["principal"] = 4, record })
	if _, err := memory.CommitFenced(ctx, RunCommit{RunID: "run-approval", CommitID: "pause", Checkpoint: checkpoint}, FenceOf(lease)); err != nil {
		t.Fatal(err)
	}
	wait, _, err := ParkRun(ctx, withWaitPolicy(ParkInput{Store: store, Fence: FenceOf(lease), Wait: "wait-1", Continuation: "continue-1", Condition: WaitCondition{Event: &contract.RunWaitEventCondition{Kind: "approval-decision", Subject: "call_2_1"}}, WakeKeyDigest: waitKeyDigest(t, waitKey), Release: []WaitRelease{}}))
	if err != nil {
		t.Fatal(err)
	}
	if !SamePrincipalBinding(wait.Pins.Principal, binding) {
		t.Fatalf("pins %+v", wait.Pins.Principal)
	}
	if _, err := WakeRun(ctx, store, decisionEvidence()); err != nil {
		t.Fatal(err)
	}
	current := *unchangedEvidence(t, store, "run-approval", "wait-1")
	current.Principal = clonePrincipalBinding(binding)
	return ResumeInput{Store: store, Resource: "run-approval", Wait: "wait-1", Evidence: "decision-1", Owner: "worker-b", DurationMs: waitHour, Approval: &contract.CheckpointApprovalDecision{CallID: "call_2_1", Approved: true}}, current
}

func TestPrincipalWaitHoldsItsPrincipals(t *testing.T) {
	ctx := context.Background()
	v := loadPrincipalChains(t)
	opened, err := OpenPrincipal(principalInput(v, v.Items[0], principalAdmitAll, nil))
	if err != nil {
		t.Fatal(err)
	}
	binding := opened.Record.Binding()
	t.Run("a park pins them, a resume needs them in its evidence, and its claim lease keeps them", func(t *testing.T) {
		clock := newLeaseClock()
		store := durableWaitStore{NewMemoryRunStore(MemoryRunStoreOptions{Now: clock.now})}
		input, current := principalParked(t, store, store.MemoryRunStore, binding)
		none := current
		none.Principal = nil
		input.Current = &none
		if _, err := ResumeParked(ctx, input); resumeReasonOf(err) != "resume:evidence-missing" {
			t.Fatalf("none: %v", err)
		}
		other := current
		other.Principal = clonePrincipalBinding(binding)
		other.Principal.RootActor = strings.Repeat("b2", 16)
		input.Current = &other
		if _, err := ResumeParked(ctx, input); resumeReasonOf(err) != "resume:principal-changed" {
			t.Fatalf("other: %v", err)
		}
		if lease, _ := store.ReadLease(ctx, "run-approval"); lease.State != "released" || lease.Epoch != "1" {
			t.Fatalf("a refused resume took a lease: %+v", lease)
		}
		input.Current = &current
		resumed, err := ResumeParked(ctx, input)
		if err != nil {
			t.Fatal(err)
		}
		if resumed.Lease.Version != 2 || resumed.Lease.Epoch != "2" || resumed.Lease.Owner != "worker-b" || !SamePrincipalBinding(resumed.Lease.Principal, binding) || !SamePrincipalBinding(resumed.Fence.Principal, binding) {
			t.Fatalf("resumed %+v", resumed.Lease)
		}
	})
	t.Run("a claim the store answers without them is refused", func(t *testing.T) {
		clock := newLeaseClock()
		durable := durableWaitStore{NewMemoryRunStore(MemoryRunStoreOptions{Now: clock.now})}
		input, current := principalParked(t, dropsPrincipalClaim{durable}, durable.MemoryRunStore, binding)
		input.Current = &current
		if _, err := ResumeParked(ctx, input); waitReasonOf(err) != "invalid" {
			t.Fatalf("got %v", err)
		}
	})
}

type principalParentLink struct{ parent *OpenedPrincipal }

func (principalParentLink) Attach(*SessionContext) {}

func (l principalParentLink) ParentPrincipal() *OpenedPrincipal { return l.parent }

func TestPrincipalSessionBoundary(t *testing.T) {
	v := loadPrincipalChains(t)
	clock := func() (time.Time, error) { return time.Parse(time.RFC3339Nano, "2026-10-30T01:00:00.000Z") }
	parent, err := OpenPrincipal(principalInput(v, v.Items[0], principalAdmitAll, clock))
	if err != nil {
		t.Fatal(err)
	}
	t.Run("a session's child chain is held to its parent, whoever issued it", func(t *testing.T) {
		session := principalSession(t, &principalRan{}, 3, nil)
		input := *session.Context
		input.Link = principalParentLink{parent: parent}
		session.Context = &input
		run, err := ContextualSession(context.Background(), session)
		if run != nil {
			run.Context.Close()
		}
		if err == nil || !strings.Contains(err.Error(), "chain-broken") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("a session on a fenced store is held to it even past a durable root's hold", func(t *testing.T) {
		store := NewMemoryRunStore(MemoryRunStoreOptions{Now: func() time.Time { return time.Date(2026, 10, 30, 1, 0, 0, 0, time.UTC) }})
		lease, err := store.AcquireLease(context.Background(), LeaseAcquire{Resource: "run-held", Owner: "worker-a", DurationMs: 60000, Principal: parent.Record.Binding()})
		if err != nil {
			t.Fatal(err)
		}
		view, err := FencedRunStore(store, FenceOf(lease), nil)
		if err != nil {
			t.Fatal(err)
		}
		session := principalSession(t, &principalRan{}, 0, nil)
		input := *session.Context
		input.RunStore = &DurableRunInput{Store: view, ID: "run-held"}
		prepared, err := PrepareDurableInput(context.Background(), &input)
		if err != nil {
			t.Fatal(err)
		}
		held := *prepared
		held.Principal = nil
		session.Context = &held
		run, err := ContextualSession(context.Background(), session)
		if run != nil {
			run.Context.Close()
		}
		if err == nil || !strings.Contains(err.Error(), "principal-changed") {
			t.Fatalf("got %v", err)
		}
	})
}
