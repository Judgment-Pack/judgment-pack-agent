package runcontext

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
	"unsafe"
)

// #265 M6: the wait conformance kit over the memory kind, a real restart over a fresh store instance, and
// stores that leave a guarantee out. The TypeScript twin is packages/harness/storage/waitConformance.test.ts.

// deepCopy copies a value with everything it points to, unexported fields included, so the copy shares
// no memory with the original (pointers shared within it stay shared within the copy). Functions are
// kept as they are: the store clock is the host's.
func deepCopy(dst, src reflect.Value, seen map[uintptr]reflect.Value) {
	switch src.Kind() {
	case reflect.Pointer:
		if src.IsNil() {
			return
		}
		if held, ok := seen[src.Pointer()]; ok {
			dst.Set(held)
			return
		}
		copied := reflect.New(src.Elem().Type())
		seen[src.Pointer()] = copied
		deepCopy(copied.Elem(), src.Elem(), seen)
		dst.Set(copied)
	case reflect.Map:
		if src.IsNil() {
			return
		}
		copied := reflect.MakeMapWithSize(src.Type(), src.Len())
		for entry := src.MapRange(); entry.Next(); {
			key, value := reflect.New(src.Type().Key()).Elem(), reflect.New(src.Type().Elem()).Elem()
			deepCopy(key, entry.Key(), seen)
			deepCopy(value, entry.Value(), seen)
			copied.SetMapIndex(key, value)
		}
		dst.Set(copied)
	case reflect.Slice:
		if src.IsNil() {
			return
		}
		copied := reflect.MakeSlice(src.Type(), src.Len(), src.Len())
		for i := range src.Len() {
			deepCopy(copied.Index(i), src.Index(i), seen)
		}
		dst.Set(copied)
	case reflect.Array:
		for i := range src.Len() {
			deepCopy(dst.Index(i), src.Index(i), seen)
		}
	case reflect.Struct:
		if !src.CanAddr() {
			held := reflect.New(src.Type()).Elem()
			held.Set(src)
			src = held
		}
		for i := range src.NumField() {
			from, to := src.Field(i), dst.Field(i)
			deepCopy(reflect.NewAt(to.Type(), unsafe.Pointer(to.UnsafeAddr())).Elem(), reflect.NewAt(from.Type(), unsafe.Pointer(from.UnsafeAddr())).Elem(), seen)
		}
	case reflect.Interface:
		if src.IsNil() {
			return
		}
		copied := reflect.New(src.Elem().Type()).Elem()
		deepCopy(copied, src.Elem(), seen)
		dst.Set(copied)
	default:
		dst.Set(src)
	}
}
func deepCopied[T any](value T) T {
	var out T
	deepCopy(reflect.ValueOf(&out).Elem(), reflect.ValueOf(&value).Elem(), map[uintptr]reflect.Value{})
	return out
}

// restartedStore is a restart: a fresh MemoryRunStore over a deep copy of everything the old one held
// (its runs, history journal, lease table and wait table). drop leaves wait-table members out, as a store
// that does not keep them would.
func restartedStore(old *MemoryRunStore, now func() time.Time, drop ...string) *MemoryRunStore {
	old.mu.Lock()
	defer old.mu.Unlock()
	fresh := NewMemoryRunStore(MemoryRunStoreOptions{Now: now})
	fresh.runs = deepCopied(old.runs)
	fresh.history = deepCopied(old.history)
	fresh.leases.rows = deepCopied(old.leases.rows)
	if !slices.Contains(drop, "rows") {
		fresh.waits.rows = deepCopied(old.waits.rows)
	}
	if !slices.Contains(drop, "events") {
		fresh.waits.events = deepCopied(old.waits.events)
	}
	fresh.waits.deliveries = deepCopied(old.waits.deliveries)
	if !slices.Contains(drop, "ceilings") {
		fresh.waits.ceilings = deepCopied(old.waits.ceilings)
	}
	if !slices.Contains(drop, "approvals") {
		fresh.waits.approvals = deepCopied(old.waits.approvals)
	}
	return fresh
}

// memoryWaitKind is the factory of the durable memory kind; wrap stands for a store kind built over it.
func memoryWaitKind(wrap func(*MemoryRunStore) WaitRunStore, drop ...string) WaitStoreFactory {
	held := map[WaitRunStore]*MemoryRunStore{}
	return func(now func() time.Time, from WaitRunStore) WaitRunStore {
		store := NewMemoryRunStore(MemoryRunStoreOptions{Now: now})
		if from != nil {
			store = restartedStore(held[from], now, drop...)
		}
		out := wrap(store)
		held[out] = store
		return out
	}
}
func durableKind(store *MemoryRunStore) WaitRunStore { return durableWaitStore{store} }

// leasesAnyRun is a store kind whose AcquireLease forgot the waits.
type leasesAnyRun struct{ durableWaitStore }

func (s leasesAnyRun) AcquireLease(ctx context.Context, request LeaseAcquire) (lease RunLease, err error) {
	err = s.leaseTransition(ctx, func() error {
		lease, err = s.leases.Acquire(request, s.runs[request.Resource].head.Revision)
		return err
	})
	return lease, err
}

// anyKey is a store kind whose WakeWait takes whatever key it is shown.
type anyKey struct{ durableWaitStore }

func (s anyKey) WakeWait(ctx context.Context, evidence WakeEvidence) (WakeDisposition, error) {
	if digest, err := WakeKeyDigest(evidence.Key); err == nil {
		s.mu.Lock()
		if row := s.waits.find(evidence.Resource, evidence.Wait); row != nil {
			row.WakeKeyDigest = digest
		}
		s.mu.Unlock()
	}
	return s.durableWaitStore.WakeWait(ctx, evidence)
}

// #226: store kinds that leave a re-authorization guarantee out.
// anyLease is a store kind whose park takes any disposition as the park's own.
type anyLease struct{ durableWaitStore }

func (s anyLease) ParkWait(ctx context.Context, park RunWait, fence LeaseFence) (RunWait, AttentionEvent, error) {
	if park.Reauthorization != nil {
		held := *park.Reauthorization
		held.Fence = fence
		park.Reauthorization = &held
	}
	return s.durableWaitStore.ParkWait(ctx, park, fence)
}

// anyPrincipal is a store kind whose claim takes any principal as the parked one.
type anyPrincipal struct{ durableWaitStore }

func (s anyPrincipal) ResumeWait(ctx context.Context, claim WaitClaim) (RunWait, RunLease, error) {
	if held, _ := s.ReadWait(ctx, claim.Resource, claim.Wait); claim.Reauthorized != nil && held != nil && held.Reauthorization != nil {
		refresh := *claim.Reauthorized
		refresh.Principal = held.Reauthorization.Principal
		claim.Reauthorized = &refresh
	}
	return s.durableWaitStore.ResumeWait(ctx, claim)
}

// oldPermissions is a store kind whose claim takes the permissions the credentials held at the park.
type oldPermissions struct{ durableWaitStore }

func (s oldPermissions) ResumeWait(ctx context.Context, claim WaitClaim) (RunWait, RunLease, error) {
	if held, _ := s.ReadWait(ctx, claim.Resource, claim.Wait); claim.Reauthorized != nil && held != nil && held.Reauthorization != nil {
		refresh := *claim.Reauthorized
		refresh.Permissions = held.Reauthorization.Permissions
		claim.Reauthorized = &refresh
	}
	return s.durableWaitStore.ResumeWait(ctx, claim)
}

// noApproval is a store kind whose park does not attach the run's unsettled approval.
type noApproval struct{ durableWaitStore }

func (s noApproval) ParkWait(ctx context.Context, park RunWait, fence LeaseFence) (RunWait, AttentionEvent, error) {
	if park.Reauthorization == nil {
		return s.durableWaitStore.ParkWait(ctx, park, fence)
	}
	s.mu.Lock()
	saved := s.waits.approvals
	s.waits.approvals = map[string]waitApproval{}
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.waits.approvals = saved; s.mu.Unlock() }()
	return s.durableWaitStore.ParkWait(ctx, park, fence)
}

// losesRefresh is a factory whose restart loses the refresh a claim recorded.
func losesRefresh() WaitStoreFactory {
	inner := memoryWaitKind(durableKind)
	return func(now func() time.Time, from WaitRunStore) WaitRunStore {
		next := inner(now, from)
		if from != nil {
			store := next.(durableWaitStore)
			for _, rows := range store.waits.rows {
				for _, row := range rows {
					row.Reauthorized = nil
				}
			}
		}
		return next
	}
}

// #226 M4: store kinds that skip a documented re-authorization obligation.
// someUnknownRow is a store kind whose park takes any unknown row for the never-admitted dispatch.
type someUnknownRow struct{ durableWaitStore }

func (s someUnknownRow) ParkWait(ctx context.Context, park RunWait, fence LeaseFence) (RunWait, AttentionEvent, error) {
	if park.Reauthorization == nil {
		return s.durableWaitStore.ParkWait(ctx, park, fence)
	}
	s.mu.Lock()
	run := s.runs[park.Resource]
	bytesHeld := run.head.Checkpoint
	if !strings.Contains(string(bytesHeld), `"state":"unknown"`) {
		s.mu.Unlock()
		return RunWait{}, AttentionEvent{}, reauthRefused("unbound")
	}
	op := park.Reauthorization.Operation
	stand, _ := canonicalRaw(map[string]any{"durable": map[string]any{"dispatches": []any{map[string]any{"id": op.Operation, "kind": "host", "read": false, "state": "unknown", "source": op.Source, "tool": op.Tool, "requestDigest": op.RequestDigest, "failure": map[string]any{"code": "authentication", "source": "tool", "outcome": "not-started"}}}}})
	run.head.Checkpoint = stand
	s.runs[park.Resource] = run
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		held := s.runs[park.Resource]
		held.head.Checkpoint = bytesHeld
		s.runs[park.Resource] = held
		s.mu.Unlock()
	}()
	park.Checkpoint.Digest = CheckpointBytesDigest(stand)
	return s.durableWaitStore.ParkWait(ctx, park, fence)
}

// epochOnly is a store kind whose stale check compares the epoch only.
type epochOnly struct{ durableWaitStore }

func (s epochOnly) ParkWait(ctx context.Context, park RunWait, fence LeaseFence) (RunWait, AttentionEvent, error) {
	if park.Reauthorization != nil && park.Reauthorization.Fence.Epoch == fence.Epoch {
		held := *park.Reauthorization
		held.Fence = fence
		park.Reauthorization = &held
	}
	return s.durableWaitStore.ParkWait(ctx, park, fence)
}

// unbounded is a store kind whose reauthorization claim is not bounded by the attached approval.
type unbounded struct{ durableWaitStore }

func (s unbounded) ResumeWait(ctx context.Context, claim WaitClaim) (RunWait, RunLease, error) {
	if claim.Reauthorized == nil {
		return s.durableWaitStore.ResumeWait(ctx, claim)
	}
	s.mu.Lock()
	saved, held := s.waits.approvals[claim.Resource]
	delete(s.waits.approvals, claim.Resource)
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if held {
			s.waits.approvals[claim.Resource] = saved
		}
		s.mu.Unlock()
	}()
	return s.durableWaitStore.ResumeWait(ctx, claim)
}

// dropsApproval is a store kind whose LimitRun drops the attached approval time.
type dropsApproval struct{ durableWaitStore }

func (s dropsApproval) LimitRun(ctx context.Context, limit RunLimit) (RunLimit, error) {
	out, err := s.durableWaitStore.LimitRun(ctx, limit)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, row := range s.waits.rows[limit.Resource] {
		if row.Reason == "reauthorization" && row.State != "resumed" && row.Deadline != nil {
			row.Deadline.Approval = nil
		}
	}
	return out, err
}

// notTerminal is a store kind whose reauthorization wait in that state does not close its run.
type notTerminal struct {
	durableWaitStore
	state string
}

func (s notTerminal) AcquireLease(ctx context.Context, request LeaseAcquire) (RunLease, error) {
	lease, err := s.durableWaitStore.AcquireLease(ctx, request)
	if waitReasonOf(err) == "closed" {
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, row := range s.waits.rows[request.Resource] {
			if row.Reason == "reauthorization" && row.State == s.state {
				return lease, waitRefused("wait-pending")
			}
		}
	}
	return lease, err
}

// lateWake is a store kind that takes an event after the timeout or the deadline of a reauthorization wait.
type lateWake struct {
	durableWaitStore
	reason string
}

func (s lateWake) WakeWait(ctx context.Context, evidence WakeEvidence) (WakeDisposition, error) {
	out, err := s.durableWaitStore.WakeWait(ctx, evidence)
	if waitReasonOf(err) == s.reason {
		if held, _ := s.ReadWait(ctx, evidence.Resource, evidence.Wait); held != nil && held.Reason == "reauthorization" {
			return WakeDisposition{Evidence: evidence.Evidence, Kind: evidence.Kind, At: "2026-01-01T00:00:00.000Z", Digest: "sha256:" + strings.Repeat("0", 64)}, nil
		}
	}
	return out, err
}

// restarting is a factory whose restart loses what lose removes.
func restarting(lose func(*RunWait)) WaitStoreFactory {
	inner := memoryWaitKind(durableKind)
	return func(now func() time.Time, from WaitRunStore) WaitRunStore {
		next := inner(now, from)
		if from != nil {
			for _, rows := range next.(durableWaitStore).waits.rows {
				for _, row := range rows {
					lose(row)
				}
			}
		}
		return next
	}
}

func TestWaitConformanceKitCertifiesTheMemoryKindRestartedOverACopy(t *testing.T) {
	ctx := context.Background()
	if err := CheckRunStoreWaitConformance(ctx, memoryWaitKind(durableKind)); err != nil {
		t.Fatal(err)
	}
	// The process-lifetime kind passes the same properties; its lifetime is declared, not certified.
	if err := CheckRunStoreWaitConformance(ctx, memoryWaitKind(func(store *MemoryRunStore) WaitRunStore { return store })); err != nil {
		t.Fatal(err)
	}
}

func TestWaitConformanceKitNamesTheGuaranteeAStoreLeavesOut(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name     string
		create   WaitStoreFactory
		property string
	}{
		{"a lease port that forgot the waits", memoryWaitKind(func(store *MemoryRunStore) WaitRunStore { return leasesAnyRun{durableWaitStore{store}} }), "wait.closure.lease"},
		{"a wake that takes any key", memoryWaitKind(func(store *MemoryRunStore) WaitRunStore { return anyKey{durableWaitStore{store}} }), "wait.key.other-wait"},
		{"a restart that loses the unsettled approval", memoryWaitKind(durableKind, "approvals"), "wait.restart.approval-ceiling"},
		{"a restart that loses the run's deadline", memoryWaitKind(durableKind, "ceilings"), "wait.restart.earliest"},
		{"a restart that loses the waits", memoryWaitKind(durableKind, "rows", "events"), "wait.restart.parked"},
		{"a factory that fails", func(func() time.Time, WaitRunStore) WaitRunStore { panic("unavailable") }, "wait.operation"},
		{"#226: a park that takes a proof minted under another lease", memoryWaitKind(func(store *MemoryRunStore) WaitRunStore { return anyLease{durableWaitStore{store}} }), "reauth.park.stale"},
		{"#226: a claim that takes any principal", memoryWaitKind(func(store *MemoryRunStore) WaitRunStore { return anyPrincipal{durableWaitStore{store}} }), "reauth.claim.identity"},
		{"#226: a claim that takes the parked permissions", memoryWaitKind(func(store *MemoryRunStore) WaitRunStore { return oldPermissions{durableWaitStore{store}} }), "reauth.claim.narrower"},
		{"#226: a park that does not attach the unsettled approval", memoryWaitKind(func(store *MemoryRunStore) WaitRunStore { return noApproval{durableWaitStore{store}} }), "reauth.approval.bound"},
		{"#226: a restart that loses the claim's refresh", losesRefresh(), "reauth.restart.claimed"},
		{"#226 M4: a park that takes any unknown row", memoryWaitKind(func(store *MemoryRunStore) WaitRunStore { return someUnknownRow{durableWaitStore{store}} }), "reauth.park.unbound.not-last"},
		{"#226 M4: a stale check of the epoch only", memoryWaitKind(func(store *MemoryRunStore) WaitRunStore { return epochOnly{durableWaitStore{store}} }), "reauth.park.stale-owner"},
		{"#226 M4: a restart that loses the attached approval", restarting(func(row *RunWait) {
			if row.Reason == "reauthorization" {
				row.Approval = nil
			}
		}), "reauth.restart.bound"},
		{"#226 M4: a restart that loses a resumed wait's disposition", restarting(func(row *RunWait) {
			if row.Reason == "reauthorization" && row.State == "resumed" {
				row.Reauthorization = nil
			}
		}), "reauth.restart.disposition"},
		{"#226 M4: a claim not bounded by the attached approval", memoryWaitKind(func(store *MemoryRunStore) WaitRunStore { return unbounded{durableWaitStore{store}} }), "reauth.approval.claim-bounded"},
		{"#226 M4: LimitRun drops the attached approval time", memoryWaitKind(func(store *MemoryRunStore) WaitRunStore { return dropsApproval{durableWaitStore{store}} }), "reauth.limit.later"},
		{"#226 M4: a timed-out wait does not close its run", memoryWaitKind(func(store *MemoryRunStore) WaitRunStore { return notTerminal{durableWaitStore{store}, "timed-out"} }), "reauth.timeout.closed"},
		{"#226 M4: an expired wait does not close its run", memoryWaitKind(func(store *MemoryRunStore) WaitRunStore { return notTerminal{durableWaitStore{store}, "expired"} }), "reauth.deadline.closed"},
		{"#226 M4: an event after the timeout wakes the wait", memoryWaitKind(func(store *MemoryRunStore) WaitRunStore { return lateWake{durableWaitStore{store}, "timed-out"} }), "reauth.timeout.late"},
		{"#226 M4: an event after the deadline wakes the wait", memoryWaitKind(func(store *MemoryRunStore) WaitRunStore { return lateWake{durableWaitStore{store}, "deadline-passed"} }), "reauth.deadline.late"},
	} {
		var failure *StorageConformanceFailure
		if err := CheckRunStoreWaitConformance(ctx, c.create); !errors.As(err, &failure) || failure.Property != c.property {
			t.Errorf("%s: %v", c.name, err)
		}
	}
}

// The restart helper is a real one: the copy shares no memory with the store it was taken from.
func TestWaitConformanceTheRestartSharesNothingWithTheOldStore(t *testing.T) {
	ctx := context.Background()
	_, store, _, parked := parkedApprovalStore(t, 0)
	fresh := durableWaitStore{restartedStore(store.MemoryRunStore, store.options.Now)}
	held, _ := fresh.ReadWait(ctx, "run-approval", "wait-1")
	if !sameWaitJSON(t, held, mustJSON(t, parked)) {
		t.Fatal("the restart lost the wait")
	}
	store.mu.Lock()
	store.waits.rows["run-approval"][0].State = "woken"
	run := store.runs["run-approval"]
	run.head.Checkpoint[0] = ' '
	store.mu.Unlock()
	if held, _ := fresh.ReadWait(ctx, "run-approval", "wait-1"); held.State != "parked" {
		t.Fatal("the restarted store shares the old one's wait")
	}
	if head, _ := fresh.Load(ctx, "run-approval"); head.Checkpoint[0] != '{' {
		t.Fatal("the restarted store shares the old one's checkpoint bytes")
	}
}
