package runcontext

import (
	"context"
	"errors"
	"reflect"
	"slices"
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
