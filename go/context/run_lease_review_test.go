package runcontext

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

// #223 review fix round, the Go twin of the review cases in packages/harness/storage/lease.test.ts:
// the effect of a call decides the writer's read, every port refuses a stale fence, and an effect host
// is admitted for unattended runs only when proven.

func leaseSourceTool(kind, effectClass string, execute func(context.Context, json.RawMessage) (ToolResult, error)) HostTool {
	name, taxonomy := "submit", &ToolTaxonomy{Operation: "act", Effect: "act"}
	if effectClass == "read" {
		name, taxonomy = "lookup", &ToolTaxonomy{Operation: "read", Effect: "read"}
	}
	schema := json.RawMessage(`{"type":"object"}`)
	identity := contract.ToolOfferIdentity{Kind: "person", Label: "Reviewer"}
	digest := ToolListDigest([]ListedTool{{Taxonomy: taxonomy, Name: name, Description: "Submit one expense.", InputSchema: schema}})
	return HostTool{Name: name, Taxonomy: taxonomy, Description: "Submit one expense.", InputSchema: schema, EffectClass: effectClass,
		Source:  &ToolSource{Kind: kind, ID: "expenses", Trust: "connected", Version: "4", ToolListDigest: digest, Identity: &identity},
		Approve: func(ApprovalRequest) string { return "allow" }, Execute: execute}
}
func leaseToolSession(store RunStore, id string, tool HostTool, resume bool) Session {
	session := modelSession()
	session.Prompt = "Work"
	session.HostTools = []HostTool{tool}
	session.Context = &Input{CurrentRequest: &CurrentRequest{Message: "Work", Input: "Work"}, RunStore: &DurableRunInput{Store: store, ID: id, Resume: resume}}
	return session
}
func leaseToolCall(t *testing.T, run *Run, name string, value string) error {
	t.Helper()
	for _, tool := range run.Session.HostTools {
		if tool.Name == name {
			_, err := tool.Execute(WithCallID(context.Background(), "wire-"+value), json.RawMessage(`{"value":`+value+`}`))
			return err
		}
	}
	t.Fatalf("no wrapped tool %s", name)
	return nil
}
func leaseDone(context.Context, json.RawMessage) (ToolResult, error) {
	return ResultOf([]Content{TextContent("done")}, nil), nil
}

type countingHost struct {
	inner      *ReferenceEffectHost
	admissions atomic.Int64
}

func (h *countingHost) EffectFencing() string { return EffectFencingEnforced }
func (h *countingHost) Dispatch(ctx context.Context, admission EffectAdmission, perform func(context.Context) error) error {
	h.admissions.Add(1)
	return h.inner.Dispatch(ctx, admission, perform)
}
func hostRows(t *testing.T, store RunStore, id string) []string {
	t.Helper()
	head, _ := store.Load(context.Background(), id)
	value, _ := ParseJSON(head.Checkpoint)
	rows := []string{}
	for _, row := range value.Get("durable").Get("dispatches").Items() {
		if row.Get("kind").Str() == "host" {
			read := "effect"
			if row.Get("read").Truth() {
				read = "read"
			}
			rows = append(rows, row.Get("state").Str()+"/"+read)
		}
	}
	return rows
}

func TestLeaseASourceActIsAnEffectNotARead(t *testing.T) {
	for _, kind := range []string{"gateway", "mcp", "custom"} {
		t.Run(kind+"-act", func(t *testing.T) {
			ctx := context.Background()
			clock := newLeaseClock()
			var armed atomic.Bool
			store := NewMemoryRunStore(MemoryRunStoreOptions{Now: clock.now, AfterCommit: func() error {
				if armed.CompareAndSwap(true, false) {
					clock.advance(2 * leaseMinute)
				}
				return nil
			}})
			host := &countingHost{inner: NewReferenceEffectHost(store)}
			id, name := kind+"-act", "expenses__submit"
			var once, again atomic.Int64
			a := leaseAcquire(t, store, id, "worker-a")
			first, err := ContextualSession(ctx, leaseToolSession(leaseView(t, store, FenceOf(a), host), id, leaseSourceTool(kind, "act", func(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
				once.Add(1)
				return leaseDone(ctx, raw)
			}), false))
			if err != nil {
				t.Fatal(err)
			}
			if err := leaseToolCall(t, first, name, "1"); err != nil || once.Load() != 1 || host.admissions.Load() != 1 {
				t.Fatalf("act not admitted by the effect host: %d %d %v", once.Load(), host.admissions.Load(), err)
			}
			if rows := hostRows(t, store, id); !reflect.DeepEqual(rows, []string{"result/effect"}) {
				t.Fatalf("act recorded as %v", rows)
			}
			// The lease expires between the claim commit and the dispatch: the host refuses, the act does not run.
			armed.Store(true)
			if err := leaseToolCall(t, first, name, "2"); err == nil || once.Load() != 1 || !reflect.DeepEqual(host.inner.Refusals(), []contract.RunLeaseRefusal{{Version: 1, Reason: "expired"}}) {
				t.Fatalf("an expired driver performed the act: %d %v %v", once.Load(), host.inner.Refusals(), err)
			}
			first.Context.Close()
			// A takeover re-issues the call whose outcome is unknown: it stays unknown, one physical dispatch in all.
			b := leaseAcquire(t, store, id, "worker-b")
			second, err := ContextualSession(ctx, leaseToolSession(leaseView(t, store, FenceOf(b), host), id, leaseSourceTool(kind, "act", func(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
				again.Add(1)
				return leaseDone(ctx, raw)
			}), true))
			if err != nil {
				t.Fatal(err)
			}
			var unknown *HostOutcomeUnknown
			if err := leaseToolCall(t, second, name, "2"); !errors.As(err, &unknown) {
				t.Fatalf("takeover dispatched an unknown act: %v", err)
			}
			if err := leaseToolCall(t, second, name, "1"); err != nil || again.Load() != 0 {
				t.Fatalf("takeover replayed a recorded act: %d %v", again.Load(), err)
			}
			second.Context.Close()
		})
		t.Run(kind+"-in-flight", func(t *testing.T) {
			ctx := context.Background()
			clock := newLeaseClock()
			store := NewMemoryRunStore(MemoryRunStoreOptions{Now: clock.now})
			host := NewReferenceEffectHost(store)
			id, name := kind+"-flight", "expenses__submit"
			var physical atomic.Int64
			started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
			a := leaseAcquire(t, store, id, "worker-a")
			first, err := ContextualSession(ctx, leaseToolSession(leaseView(t, store, FenceOf(a), host), id, leaseSourceTool(kind, "act", func(context.Context, json.RawMessage) (ToolResult, error) {
				physical.Add(1)
				close(started)
				<-release
				return ToolResult{}, errors.New("the process ended")
			}), false))
			if err != nil {
				t.Fatal(err)
			}
			go func() { defer close(done); _ = leaseToolCall(t, first, name, "1") }()
			<-started
			clock.advance(2 * leaseMinute)
			b := leaseAcquire(t, store, id, "worker-b")
			second, err := ContextualSession(ctx, leaseToolSession(leaseView(t, store, FenceOf(b), host), id, leaseSourceTool(kind, "act", func(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
				physical.Add(1)
				return leaseDone(ctx, raw)
			}), true))
			if err != nil {
				t.Fatal(err)
			}
			var unknown *HostOutcomeUnknown
			outcome := leaseToolCall(t, second, name, "1")
			close(release)
			<-done
			second.Context.Close()
			first.Context.Close()
			if !errors.As(outcome, &unknown) || physical.Load() != 1 {
				t.Fatalf("takeover dispatched an in-flight act again: %d %v", physical.Load(), outcome)
			}
		})
	}
	t.Run("source-read-stays-a-read", func(t *testing.T) {
		ctx := context.Background()
		store := NewMemoryRunStore(MemoryRunStoreOptions{})
		host := &countingHost{inner: NewReferenceEffectHost(store)}
		var reads atomic.Int64
		a := leaseAcquire(t, store, "source-read", "worker-a")
		run, err := ContextualSession(ctx, leaseToolSession(leaseView(t, store, FenceOf(a), host), "source-read", leaseSourceTool("custom", "read", func(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
			reads.Add(1)
			return leaseDone(ctx, raw)
		}), false))
		if err != nil {
			t.Fatal(err)
		}
		if err := leaseToolCall(t, run, "expenses__lookup", "1"); err != nil || reads.Load() != 1 || host.admissions.Load() != 0 {
			t.Fatalf("a source read reached the effect host: %d %d %v", reads.Load(), host.admissions.Load(), err)
		}
		if rows := hostRows(t, store, "source-read"); !reflect.DeepEqual(rows, []string{"result/read"}) {
			t.Fatalf("read recorded as %v", rows)
		}
		run.Context.Close()
	})
}

func TestLeaseAStaleDriverCannotReleaseTheTurnItsTakeoverResumes(t *testing.T) {
	ctx := context.Background()
	clock := newLeaseClock()
	store := NewMemoryRunStore(MemoryRunStoreOptions{Now: clock.now})
	host := NewReferenceEffectHost(store)
	a := leaseAcquire(t, store, "chat-run", "worker-a")
	viewA := leaseView(t, store, FenceOf(a), host).(HistoryRunStore)
	scopeA, err := NewStoredChatScope(viewA, "chat-lease-scope", NewChatContext("shared"), nil)
	if err != nil {
		t.Fatal(err)
	}
	session := historyStoredSession(viewA, scopeA, "chat-run", false)
	var calls atomic.Int64
	session.HostTools = []HostTool{{Name: "save_object", Taxonomy: &ToolTaxonomy{Operation: "own-state", Effect: "pure"}, Effect: "workflow", Description: "Save", InputSchema: json.RawMessage(`{}`), Execute: func(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
		calls.Add(1)
		return leaseDone(ctx, raw)
	}}}
	first, err := ContextualSession(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	if err := leaseToolCall(t, first, "save_object", "1"); err != nil {
		t.Fatal(err)
	}
	bound, _ := store.ReadHistoryHead(ctx, "chat-run")
	clock.advance(2 * leaseMinute)
	b := leaseAcquire(t, store, "chat-run", "worker-b")
	first.Context.Close()
	if _, err := scopeA.Close(ctx); leaseReason(err) != "stale-epoch" {
		t.Fatalf("a driver that lost its lease released the chat turn: %v", err)
	}
	if latest, _ := store.LoadContextSnapshot(ctx, "chat-lease-scope"); latest == nil || latest.Revision != bound.Context.Revision {
		t.Fatal("the chat snapshot changed")
	}
	viewB := leaseView(t, store, FenceOf(b), host).(HistoryRunStore)
	head, _ := store.ReadHistoryHead(ctx, "chat-run")
	restored, err := RestoreStoredChatScope(ctx, viewB, *head.Context)
	if err != nil {
		t.Fatalf("takeover restore: %v", err)
	}
	resumed := historyStoredSession(viewB, restored, "chat-run", true)
	resumed.HostTools = session.HostTools
	second, err := ContextualSession(ctx, resumed)
	if err != nil {
		t.Fatalf("takeover resume: %v", err)
	}
	if err := leaseToolCall(t, second, "save_object", "2"); err != nil || calls.Load() != 2 {
		t.Fatalf("takeover call: %d %v", calls.Load(), err)
	}
	second.Context.Close()
}

// brokenLeaseStore overrides one port of the memory kind so it no longer enforces the fence.
type brokenLeaseStore struct {
	*MemoryRunStore
	broken string
}

func (s brokenLeaseStore) current(ctx context.Context, fence LeaseFence, owner bool) LeaseFence {
	if row, _ := s.ReadLease(ctx, fence.Resource); row != nil && (owner || row.Owner == fence.Owner) {
		fence.Epoch, fence.Owner = row.Epoch, row.Owner
	}
	return fence
}
func (s brokenLeaseStore) CheckFence(ctx context.Context, fence LeaseFence) error {
	if s.broken == "check" {
		fence = s.current(ctx, fence, true)
	}
	return s.MemoryRunStore.CheckFence(ctx, fence)
}
func (s brokenLeaseStore) CommitHistoryFenced(ctx context.Context, input RunHistoryCommit, fence LeaseFence) (RunHistoryReceipt, error) {
	if s.broken == "history" {
		return s.CommitHistory(ctx, input)
	}
	return s.MemoryRunStore.CommitHistoryFenced(ctx, input, fence)
}
func (s brokenLeaseStore) CommitContextSnapshotFenced(ctx context.Context, input ContextSnapshotUpdate, commitID string, fence LeaseFence) (ContextSnapshotHead, error) {
	if s.broken == "snapshot" {
		return s.CommitContextSnapshot(ctx, input, commitID)
	}
	return s.MemoryRunStore.CommitContextSnapshotFenced(ctx, input, commitID, fence)
}
func (s brokenLeaseStore) RenewLease(ctx context.Context, fence LeaseFence, ms int64) (RunLease, error) {
	if s.broken == "renew" {
		fence = s.current(ctx, fence, false)
	}
	return s.MemoryRunStore.RenewLease(ctx, fence, ms)
}
func (s brokenLeaseStore) ReleaseLease(ctx context.Context, fence LeaseFence) (RunLease, error) {
	if s.broken == "release" {
		fence = s.current(ctx, fence, true)
	}
	return s.MemoryRunStore.ReleaseLease(ctx, fence)
}

type declaredHost struct{ perform bool }

func (declaredHost) EffectFencing() string { return EffectFencingEnforced }
func (declaredHost) Dispatch(ctx context.Context, _ EffectAdmission, perform func(context.Context) error) error {
	return perform(ctx)
}

type resourceBlindHost struct{ inner *ReferenceEffectHost }

func (resourceBlindHost) EffectFencing() string { return EffectFencingEnforced }
func (h resourceBlindHost) Dispatch(ctx context.Context, admission EffectAdmission, perform func(context.Context) error) error {
	admission.Operation.Run = admission.Fence.Resource
	admission.IdempotencyKey, _ = EffectIdempotencyKey(admission.Operation)
	return h.inner.Dispatch(ctx, admission, perform)
}

type forgetfulHost struct{ authority LeaseAuthority }

func (forgetfulHost) EffectFencing() string { return EffectFencingEnforced }
func (h forgetfulHost) Dispatch(ctx context.Context, admission EffectAdmission, perform func(context.Context) error) error {
	if err := h.authority.CheckFence(ctx, admission.Fence); err != nil {
		return err
	}
	return perform(ctx)
}

type wrappedReferenceHost struct{ inner *ReferenceEffectHost }

func (wrappedReferenceHost) EffectFencing() string { return EffectFencingEnforced }
func (h wrappedReferenceHost) Dispatch(ctx context.Context, admission EffectAdmission, perform func(context.Context) error) error {
	return h.inner.Dispatch(ctx, admission, perform)
}

func TestLeaseKitsRefuseWhatDoesNotEnforceTheFence(t *testing.T) {
	ctx := context.Background()
	for _, broken := range []string{"check", "history", "snapshot", "renew", "release"} {
		err := CheckRunStoreLeaseConformance(ctx, func(now func() time.Time) LeaseRunStore {
			return brokenLeaseStore{MemoryRunStore: NewMemoryRunStore(MemoryRunStoreOptions{Now: now}), broken: broken}
		})
		var failed *StorageConformanceFailure
		if !errors.As(err, &failed) {
			t.Fatalf("the lease conformance passed a store whose %s does not enforce the fence", broken)
		}
	}
	durable := durableLeaseFixture{NewMemoryRunStore(MemoryRunStoreOptions{})}
	for name, create := range map[string]func(LeaseAuthority) EffectHost{
		"performs without checking": func(LeaseAuthority) EffectHost { return declaredHost{} },
		"skips the resource check":  func(a LeaseAuthority) EffectHost { return resourceBlindHost{NewReferenceEffectHost(a)} },
		"forgets admitted identities": func(a LeaseAuthority) EffectHost {
			return forgetfulHost{a}
		},
	} {
		var failed *StorageConformanceFailure
		if err := CheckEffectHostConformance(ctx, create); !errors.As(err, &failed) {
			t.Fatalf("the effect-host conformance passed a host that %s", name)
		}
		if err := AdmitUnattended(durable, create(durable)); leaseReason(err) != "effect-host-unfenced" {
			t.Fatalf("unattended admission trusted a host that %s: %v", name, err)
		}
		if _, err := CertifyEffectHost(ctx, create, durable); !errors.As(err, &failed) {
			t.Fatalf("certified a host that %s", name)
		}
	}
	if err := CheckEffectHostConformance(ctx, func(a LeaseAuthority) EffectHost { return NewReferenceEffectHost(a) }); err != nil {
		t.Fatal(err)
	}
	wrap := func(a LeaseAuthority) EffectHost { return wrappedReferenceHost{NewReferenceEffectHost(a)} }
	if err := AdmitUnattended(durable, wrap(durable)); leaseReason(err) != "effect-host-unfenced" {
		t.Fatalf("a declared host was admitted: %v", err)
	}
	certified, err := CertifyEffectHost(ctx, wrap, durable)
	if err != nil {
		t.Fatal(err)
	}
	if err := AdmitUnattended(durable, certified); err != nil {
		t.Fatal(err)
	}
}

func TestLeasePinnedSafeguards(t *testing.T) {
	ctx := context.Background()
	t.Run("runtime-effect-through-the-host", func(t *testing.T) {
		clock := newLeaseClock()
		var armed atomic.Bool
		store := NewMemoryRunStore(MemoryRunStoreOptions{Now: clock.now, AfterCommit: func() error {
			if armed.CompareAndSwap(true, false) {
				clock.advance(2 * leaseMinute)
			}
			return nil
		}})
		host := NewReferenceEffectHost(store)
		a := leaseAcquire(t, store, "runtime-act", "worker-a")
		binding, err := leaseDriver(t, leaseView(t, store, FenceOf(a), host), "runtime-act", false)
		if err != nil {
			t.Fatal(err)
		}
		var calls atomic.Int64
		armed.Store(true)
		_, err = InvokeDurable(ctx, binding, "runtime", "runtime", "act_on_runtime", []byte(`{"value":1}`), false, leaseCounter(&calls), nil, nil)
		if err == nil || calls.Load() != 0 || !reflect.DeepEqual(host.Refusals(), []contract.RunLeaseRefusal{{Version: 1, Reason: "expired"}}) {
			t.Fatalf("a runtime effect bypassed the effect host: %d %v %v", calls.Load(), host.Refusals(), err)
		}
	})
	t.Run("host-resource-mismatch", func(t *testing.T) {
		store := NewMemoryRunStore(MemoryRunStoreOptions{})
		host := NewReferenceEffectHost(store)
		a := leaseAcquire(t, store, "run-x", "worker-a")
		admission, _ := NewEffectAdmission(FenceOf(a), EffectOperation{Run: "run-y", Operation: "op", Source: "host", Tool: "t", RequestDigest: "sha256:" + strings.Repeat("c", 64)})
		performed := false
		if err := host.Dispatch(ctx, admission, func(context.Context) error { performed = true; return nil }); leaseReason(err) != "resource-mismatch" || performed {
			t.Fatalf("a fence for another run was admitted: %v", err)
		}
		if !reflect.DeepEqual(host.Refusals(), []contract.RunLeaseRefusal{{Version: 1, Reason: "resource-mismatch"}}) {
			t.Fatal(host.Refusals())
		}
	})
}
