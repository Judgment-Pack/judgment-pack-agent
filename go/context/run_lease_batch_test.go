package runcontext

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// #253, the Go twin of the #253 cases in packages/harness/storage/lease.test.ts: the kits test expiry
// on every fenced commit path, certification is bound to the exact instance it checked and to the run's
// own store, and nothing keeps a released host alive.

// expiryBlindStore checks owner, epoch and release on one fenced commit path, but not expiry.
type expiryBlindStore struct {
	*MemoryRunStore
	path string
}

func (s expiryBlindStore) current(ctx context.Context, fence LeaseFence) bool {
	row, _ := s.ReadLease(ctx, fence.Resource)
	return row != nil && row.State == "held" && row.Epoch == fence.Epoch && row.Owner == fence.Owner
}
func (s expiryBlindStore) CommitHistoryFenced(ctx context.Context, input RunHistoryCommit, fence LeaseFence) (RunHistoryReceipt, error) {
	if s.path == "history" && s.current(ctx, fence) {
		return s.CommitHistory(ctx, input)
	}
	return s.MemoryRunStore.CommitHistoryFenced(ctx, input, fence)
}
func (s expiryBlindStore) CommitContextSnapshotFenced(ctx context.Context, input ContextSnapshotUpdate, commitID string, fence LeaseFence) (ContextSnapshotHead, error) {
	if s.path == "snapshot" && s.current(ctx, fence) {
		return s.CommitContextSnapshot(ctx, input, commitID)
	}
	return s.MemoryRunStore.CommitContextSnapshotFenced(ctx, input, commitID, fence)
}

func TestLeaseKitsTestExpiryOnEveryFencedCommitPath(t *testing.T) {
	for _, path := range []string{"history", "snapshot"} {
		err := CheckRunStoreLeaseConformance(context.Background(), func(now func() time.Time) LeaseRunStore {
			return expiryBlindStore{MemoryRunStore: NewMemoryRunStore(MemoryRunStoreOptions{Now: now}), path: path}
		})
		var failed *StorageConformanceFailure
		if !errors.As(err, &failed) || failed.Property != "lease."+path+".expired.equal-time" {
			t.Fatalf("the lease conformance passed a store whose %s path skips expiry: %v", path, err)
		}
	}
}

type failOpenHost struct{ inner *ReferenceEffectHost }

func (failOpenHost) EffectFencing() string { return EffectFencingEnforced }
func (h failOpenHost) Dispatch(ctx context.Context, admission EffectAdmission, perform func(context.Context) error) error {
	err := h.inner.Dispatch(ctx, admission, perform)
	var refused *LeaseRefused
	if err != nil && !errors.As(err, &refused) {
		return perform(ctx)
	}
	return err
}

type valueHost struct{ name string }

func (valueHost) EffectFencing() string { return EffectFencingEnforced }
func (valueHost) Dispatch(ctx context.Context, _ EffectAdmission, perform func(context.Context) error) error {
	return perform(ctx)
}

type noopAuthority struct{}

func (noopAuthority) CheckFence(context.Context, LeaseFence) error { return nil }

type embeddedReference struct{ *ReferenceEffectHost }

func TestLeaseCertificationIsBoundToTheInstanceItChecked(t *testing.T) {
	ctx := context.Background()
	var failed *StorageConformanceFailure
	durable := durableLeaseFixture{NewMemoryRunStore(MemoryRunStoreOptions{})}
	other := durableLeaseFixture{NewMemoryRunStore(MemoryRunStoreOptions{})}
	performs := func(a LeaseAuthority) EffectHost { return wrappedReferenceHost{NewReferenceEffectHost(a)} }
	t.Run("authority-unavailable", func(t *testing.T) {
		failOpen := func(a LeaseAuthority) EffectHost { return failOpenHost{NewReferenceEffectHost(a)} }
		if err := CheckEffectHostConformance(ctx, failOpen); !errors.As(err, &failed) || failed.Property != "host.authority-unavailable" {
			t.Fatalf("a host that performs while the authority is unavailable passed: %v", err)
		}
		if _, err := CertifyEffectHost(ctx, failOpen, durable); !errors.As(err, &failed) {
			t.Fatal("certified a host that fails open")
		}
	})
	t.Run("factory-called-once", func(t *testing.T) {
		products := []EffectHost{}
		create := func(a LeaseAuthority) EffectHost {
			host := performs(a)
			if len(products) > 0 {
				host = valueHost{"second"}
			}
			products = append(products, host)
			return host
		}
		certified, err := CertifyEffectHost(ctx, create, durable)
		if err != nil || len(products) != 1 {
			t.Fatalf("certification called the factory %d times: %v", len(products), err)
		}
		if err := AdmitUnattended(durable, certified); err != nil {
			t.Fatal(err)
		}
		if err := AdmitUnattended(durable, products[0]); leaseReason(err) != "effect-host-unfenced" {
			t.Fatalf("the exercised instance itself was admitted without its evidence: %v", err)
		}
		// The certified host now answers from the real store: a fence it never issued is refused.
		admission, _ := NewEffectAdmission(LeaseFence{Version: 1, Resource: "r", Owner: "o", Epoch: "1"}, EffectOperation{Run: "r", Operation: "op", Source: "host", Tool: "t", RequestDigest: "sha256:" + strings.Repeat("d", 64)})
		performed := false
		if err := certified.Dispatch(ctx, admission, func(context.Context) error { performed = true; return nil }); leaseReason(err) != "no-lease" || performed {
			t.Fatalf("the certified host did not answer from the store: %v", err)
		}
	})
	t.Run("identity-not-equality", func(t *testing.T) {
		certified, err := CertifyEffectHost(ctx, func(LeaseAuthority) EffectHost { return valueHost{"equal"} }, durable)
		if err == nil {
			t.Fatal("certified a host that performs without checking")
		}
		certified, err = CertifyEffectHost(ctx, performs, durable)
		if err != nil {
			t.Fatal(err)
		}
		copied := *certified
		if err := AdmitUnattended(durable, &copied); leaseReason(err) != "effect-host-unfenced" {
			t.Fatalf("a copy of the evidence was admitted: %v", err)
		}
		if err := AdmitUnattended(durable, valueHost{"equal"}); leaseReason(err) != "effect-host-unfenced" {
			t.Fatalf("an equal value was admitted: %v", err)
		}
	})
	t.Run("authority-is-the-store", func(t *testing.T) {
		if err := AdmitUnattended(durable, NewReferenceEffectHost(noopAuthority{})); leaseReason(err) != "effect-host-unfenced" {
			t.Fatalf("a reference host over a no-op authority was admitted: %v", err)
		}
		if err := AdmitUnattended(durable, NewReferenceEffectHost(other)); leaseReason(err) != "effect-host-unfenced" {
			t.Fatalf("a reference host over another store was admitted: %v", err)
		}
		foreign, err := CertifyEffectHost(ctx, performs, other)
		if err != nil {
			t.Fatal(err)
		}
		if err := AdmitUnattended(durable, foreign); leaseReason(err) != "effect-host-unfenced" {
			t.Fatalf("a host certified over another store was admitted: %v", err)
		}
		if err := AdmitUnattended(durable, embeddedReference{NewReferenceEffectHost(durable)}); leaseReason(err) != "effect-host-unfenced" {
			t.Fatalf("a type embedding the reference host was admitted: %v", err)
		}
		if err := AdmitUnattended(durable, &ReferenceEffectHost{authority: durable}); leaseReason(err) != "effect-host-unfenced" {
			t.Fatalf("a reference host not made by NewReferenceEffectHost was admitted: %v", err)
		}
		if err := AdmitUnattended(durable, NewReferenceEffectHost(durable)); err != nil {
			t.Fatal(err)
		}
	})
}

func TestLeaseAdmittedHostsAreCollectable(t *testing.T) {
	ctx := context.Background()
	durable := durableLeaseFixture{NewMemoryRunStore(MemoryRunStoreOptions{})}
	var collected atomic.Int32
	func() {
		for range 50 {
			reference := NewReferenceEffectHost(durable)
			if err := AdmitUnattended(durable, reference); err != nil {
				t.Fatal(err)
			}
			runtime.AddCleanup(reference, func(*atomic.Int32) { collected.Add(1) }, &collected)
		}
		for range 50 {
			certified, err := CertifyEffectHost(ctx, func(a LeaseAuthority) EffectHost { return wrappedReferenceHost{NewReferenceEffectHost(a)} }, durable)
			if err != nil {
				t.Fatal(err)
			}
			if err := AdmitUnattended(durable, certified); err != nil {
				t.Fatal(err)
			}
			runtime.AddCleanup(certified, func(*atomic.Int32) { collected.Add(1) }, &collected)
		}
	}()
	for attempt := 0; attempt < 50 && collected.Load() < 100; attempt++ {
		runtime.GC()
		time.Sleep(10 * time.Millisecond)
	}
	if collected.Load() != 100 {
		t.Fatalf("admitted hosts were kept alive: %d of 100 collected", collected.Load())
	}
}
