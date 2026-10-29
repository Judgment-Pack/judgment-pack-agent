package runcontext

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

// The Go twin of packages/harness/storage/lease.test.ts (#223).

type leaseVectorFile struct {
	Schema    string            `json:"schema"`
	Items     []json.RawMessage `json:"items"`
	Refused   []json.RawMessage `json:"refused"`
	Canonical string            `json:"canonical"`
	Epochs    struct {
		First     string           `json:"first"`
		Largest   string           `json:"largest"`
		Next      [][2]string      `json:"next"`
		Exhausted string           `json:"exhausted"`
		Order     [][3]interface{} `json:"order"`
	} `json:"epochs"`
}

func readLeaseVector(t *testing.T, name string) leaseVectorFile {
	t.Helper()
	raw, err := os.ReadFile("../../contract/vectors/lease/" + name)
	if err != nil {
		t.Fatal(err)
	}
	var vector leaseVectorFile
	if err := json.Unmarshal(raw, &vector); err != nil {
		t.Fatal(err)
	}
	return vector
}
func refusedMember(t *testing.T, raw json.RawMessage, member string) (string, json.RawMessage) {
	t.Helper()
	var row map[string]json.RawMessage
	if err := json.Unmarshal(raw, &row); err != nil {
		t.Fatal(err)
	}
	var problem string
	_ = json.Unmarshal(row["problem"], &problem)
	return problem, row[member]
}
func sameJSON(t *testing.T, value any, raw json.RawMessage) bool {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := ParseJSON(encoded)
	b, _ := ParseJSON(raw)
	return CanonicalJSON(a) == CanonicalJSON(b)
}
func leaseReason(err error) string {
	var refused *LeaseRefused
	if errors.As(err, &refused) {
		return refused.Reason
	}
	var capability *LeaseCapabilityRefused
	if errors.As(err, &capability) {
		return capability.Reason
	}
	var unknown *StorageOutcomeUnknown
	if errors.As(err, &unknown) && unknown.cause != nil {
		return leaseReason(unknown.cause)
	}
	return ""
}

func TestLeaseSharedVectorsBothReaders(t *testing.T) {
	leases := readLeaseVector(t, "lease.json")
	for _, item := range leases.Items {
		lease, err := ReadRunLease(item)
		if err != nil || !sameJSON(t, lease, item) {
			t.Fatalf("lease %s: %v", item, err)
		}
	}
	for _, row := range leases.Refused {
		problem, record := refusedMember(t, row, "record")
		if _, err := ReadRunLease(record); leaseReason(err) != "invalid" {
			t.Fatalf("lease refused %q: %v", problem, err)
		}
	}
	fences := readLeaseVector(t, "fence.json")
	for _, item := range fences.Items {
		fence, err := ReadLeaseFence(item)
		if err != nil || !sameJSON(t, fence, item) {
			t.Fatalf("fence %s: %v", item, err)
		}
	}
	for _, row := range fences.Refused {
		problem, fence := refusedMember(t, row, "fence")
		if _, err := ReadLeaseFence(fence); leaseReason(err) != "invalid" {
			t.Fatalf("fence refused %q: %v", problem, err)
		}
	}
	admissions := readLeaseVector(t, "admission.json")
	var keys []string
	for _, item := range admissions.Items {
		admission, err := ReadEffectAdmission(item)
		if err != nil || !sameJSON(t, admission, item) {
			t.Fatalf("admission %s: %v", item, err)
		}
		operation, _ := json.Marshal(admission.Operation)
		parsed, _ := ParseJSON(operation)
		if CanonicalJSON(parsed) != admissions.Canonical {
			t.Fatalf("canonical operation %s", CanonicalJSON(parsed))
		}
		key, err := EffectIdempotencyKey(admission.Operation)
		if err != nil || key != admission.IdempotencyKey {
			t.Fatalf("idempotency key %s %v", key, err)
		}
		keys = append(keys, key)
	}
	if len(keys) != 2 || keys[0] != keys[1] {
		t.Fatalf("the takeover's key changed with the epoch: %v", keys)
	}
	for _, row := range admissions.Refused {
		problem, admission := refusedMember(t, row, "admission")
		if _, err := ReadEffectAdmission(admission); leaseReason(err) != "invalid" {
			t.Fatalf("admission refused %q: %v", problem, err)
		}
	}
	refusals := readLeaseVector(t, "refusal.json")
	var reasons []string
	for _, item := range refusals.Items {
		record, err := ReadLeaseRefusal(item)
		if err != nil || !sameJSON(t, (&LeaseRefused{Reason: record.Reason}).Record(), item) {
			t.Fatalf("refusal %s: %v", item, err)
		}
		reasons = append(reasons, record.Reason)
	}
	if !slices.Equal(reasons, LeaseRefusalReasons) {
		t.Fatalf("refusal reasons %v", reasons)
	}
	for _, item := range refusals.Refused {
		if _, err := ReadLeaseRefusal(item); leaseReason(err) != "invalid" {
			t.Fatalf("refusal refused %s", item)
		}
	}
	capabilities := readLeaseVector(t, "capability.json")
	reasons = nil
	for _, item := range capabilities.Items {
		record, err := ReadLeaseCapabilityRefusal(item)
		if err != nil || !sameJSON(t, (&LeaseCapabilityRefused{Reason: record.Reason}).Record(), item) {
			t.Fatalf("capability %s: %v", item, err)
		}
		reasons = append(reasons, record.Reason)
	}
	if !slices.Equal(reasons, LeaseCapabilityReasons) {
		t.Fatalf("capability reasons %v", reasons)
	}
	for _, item := range capabilities.Refused {
		if _, err := ReadLeaseCapabilityRefusal(item); leaseReason(err) != "invalid" {
			t.Fatalf("capability refused %s", item)
		}
	}
}

func TestLeaseEpochExactBoundedEncoding(t *testing.T) {
	fences := readLeaseVector(t, "fence.json")
	epochs := fences.Epochs
	if LeaseEpochMax != epochs.Largest {
		t.Fatal("largest epoch")
	}
	if first, err := NextLeaseEpoch(""); err != nil || first != epochs.First {
		t.Fatalf("first epoch %q %v", first, err)
	}
	for _, pair := range epochs.Next {
		if next, err := NextLeaseEpoch(pair[0]); err != nil || next != pair[1] {
			t.Fatalf("next of %s: %s %v", pair[0], next, err)
		}
	}
	if _, err := NextLeaseEpoch(epochs.Exhausted); leaseReason(err) != "epoch-exhausted" {
		t.Fatalf("overflow refusal: %v", err)
	}
	for _, row := range epochs.Order {
		order, err := CompareLeaseEpochs(row[0].(string), row[1].(string))
		if err != nil || order != int(row[2].(float64)) {
			t.Fatalf("order %v: %d %v", row, order, err)
		}
	}
	valid := ""
	_ = json.Unmarshal(fences.Items[0], &struct {
		Epoch *string `json:"epoch"`
	}{&valid})
	for _, row := range fences.Refused {
		problem, raw := refusedMember(t, row, "fence")
		var fence map[string]json.RawMessage
		_ = json.Unmarshal(raw, &fence)
		var epoch string
		if json.Unmarshal(fence["epoch"], &epoch) != nil || epoch == valid {
			continue
		}
		if _, err := NextLeaseEpoch(epoch); epoch != "" && leaseReason(err) != "invalid" {
			t.Fatalf("non-integer or overflowed epoch %q (%s): %v", epoch, problem, err)
		}
	}
}

type revivingLeaseStore struct{ *MemoryRunStore }

func (s revivingLeaseStore) RenewLease(ctx context.Context, fence LeaseFence, durationMs int64) (RunLease, error) {
	lease, err := s.MemoryRunStore.RenewLease(ctx, fence, durationMs)
	if leaseReason(err) == "expired" {
		held, _ := s.ReadLease(ctx, fence.Resource)
		return *held, nil
	}
	return lease, err
}

func TestMemoryRunStoreLeaseConformance(t *testing.T) {
	ctx := context.Background()
	if err := CheckRunStoreLeaseConformance(ctx, func(now func() time.Time) LeaseRunStore {
		return NewMemoryRunStore(MemoryRunStoreOptions{Now: now})
	}); err != nil {
		t.Fatal(err)
	}
	err := CheckRunStoreLeaseConformance(ctx, func(now func() time.Time) LeaseRunStore {
		return revivingLeaseStore{NewMemoryRunStore(MemoryRunStoreOptions{Now: now})}
	})
	var failed *StorageConformanceFailure
	if !errors.As(err, &failed) || failed.Property != "lease.renew.no-revival" {
		t.Fatalf("a reviving renewal passed conformance: %v", err)
	}
}

const leaseMinute = int64(60_000)

type leaseClock struct{ at atomic.Int64 }

func newLeaseClock() *leaseClock {
	c := &leaseClock{}
	c.at.Store(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli())
	return c
}
func (c *leaseClock) now() time.Time   { return time.UnixMilli(c.at.Load()).UTC() }
func (c *leaseClock) advance(ms int64) { c.at.Add(ms) }

func leaseAcquire(t *testing.T, store *MemoryRunStore, resource, owner string) RunLease {
	t.Helper()
	ctx := context.Background()
	revision := ""
	if head, _ := store.Load(ctx, resource); head != nil {
		revision = head.Revision
	}
	lease, err := store.AcquireLease(ctx, LeaseAcquire{Resource: resource, Owner: owner, ExpectedRevision: revision, DurationMs: leaseMinute})
	if err != nil {
		t.Fatal(err)
	}
	return lease
}
func leaseView(t *testing.T, store RunStore, fence LeaseFence, host EffectHost) RunStore {
	t.Helper()
	view, err := FencedRunStore(store, fence, host)
	if err != nil {
		t.Fatal(err)
	}
	return view
}

// leaseDriver opens a writer through a view; a refused open is returned as the driver's error.
func leaseDriver(t *testing.T, store RunStore, id string, resume bool) (*RunBinding, error) {
	t.Helper()
	ctx := context.Background()
	var head *RunHead
	if resume {
		var err error
		if head, err = store.Load(ctx, id); err != nil {
			return nil, err
		}
	}
	writer, err := NewRunWriter(store, id, head, nil)
	if err != nil {
		return nil, err
	}
	state := writerCheckpoint(id)
	if saved := writer.Checkpoint(); saved != nil {
		state = *saved
	}
	writer.Bind(func() (contract.Checkpoint, error) { return state, nil }, RecordDefaults, nil)
	return &RunBinding{Writer: writer, Scope: DurableScope{Execution: id, Role: "author"}, Root: true}, nil
}
func leaseCall(binding *RunBinding, value string, execute func([]byte) (ToolResult, error)) error {
	_, err := InvokeDurable(context.Background(), binding, "host", "host", "save_object", []byte(`{"value":`+value+`}`), false, execute, nil, nil)
	return err
}
func leaseCounter(calls *atomic.Int64) func([]byte) (ToolResult, error) {
	return func([]byte) (ToolResult, error) {
		calls.Add(1)
		return ReadToolResult(MustParseJSON(`{"content":[{"type":"text","text":"saved"}]}`)), nil
	}
}

func TestLeaseTheStoreChecksTheFenceAgainAtPublication(t *testing.T) {
	ctx := context.Background()
	clock := newLeaseClock()
	var armed atomic.Bool
	store := NewMemoryRunStore(MemoryRunStoreOptions{Now: clock.now, BeforeCommit: func() error {
		if armed.CompareAndSwap(true, false) {
			clock.advance(2 * leaseMinute)
		}
		return nil
	}})
	a := leaseAcquire(t, store, "publication", "worker-a")
	armed.Store(true)
	if _, err := store.CommitFenced(ctx, RunCommit{RunID: "publication", CommitID: "late", Checkpoint: []byte(`{}`)}, FenceOf(a)); leaseReason(err) != "expired" {
		t.Fatalf("published under a fence that expired before publication: %v", err)
	}
	if head, _ := store.Load(ctx, "publication"); head != nil {
		t.Fatal("published")
	}
}

func TestLeaseCompetingDriversOnlyTheHolderCommitsAndDispatches(t *testing.T) {
	ctx := context.Background()
	clock := newLeaseClock()
	store := NewMemoryRunStore(MemoryRunStoreOptions{Now: clock.now})
	host := NewReferenceEffectHost(store)
	var calls atomic.Int64
	a := leaseAcquire(t, store, "competing", "worker-a")
	if _, err := store.AcquireLease(ctx, LeaseAcquire{Resource: "competing", Owner: "worker-b", DurationMs: leaseMinute}); leaseReason(err) != "held" {
		t.Fatalf("second driver acquired: %v", err)
	}
	first, err := leaseDriver(t, leaseView(t, store, FenceOf(a), host), "competing", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := leaseCall(first, "1", leaseCounter(&calls)); err != nil || calls.Load() != 1 {
		t.Fatalf("holder: %d %v", calls.Load(), err)
	}
	before, _ := store.Load(ctx, "competing")
	forged := FenceOf(a)
	forged.Owner = "worker-b"
	second, err := leaseDriver(t, leaseView(t, store, forged, host), "competing", true)
	if err == nil {
		err = leaseCall(second, "2", leaseCounter(&calls))
	}
	if leaseReason(err) != "other-owner" {
		t.Fatalf("competing driver: %v", err)
	}
	admission, _ := NewEffectAdmission(forged, EffectOperation{Run: "competing", Operation: "forged-op", Source: "host", Tool: "save_object", RequestDigest: "sha256:" + strings.Repeat("0", 64)})
	if err := host.Dispatch(ctx, admission, func(context.Context) error { calls.Add(1); return nil }); leaseReason(err) != "other-owner" {
		t.Fatalf("effect host admitted a forged fence: %v", err)
	}
	after, _ := store.Load(ctx, "competing")
	if calls.Load() != 1 || after.Revision != before.Revision {
		t.Fatalf("competing driver wrote or dispatched: %d", calls.Load())
	}
}

func TestLeaseTakeoverStaleAndExpiredCommitsRefuse(t *testing.T) {
	ctx := context.Background()
	clock := newLeaseClock()
	store := NewMemoryRunStore(MemoryRunStoreOptions{Now: clock.now})
	host := NewReferenceEffectHost(store)
	var calls atomic.Int64
	a := leaseAcquire(t, store, "takeover", "worker-a")
	first, err := leaseDriver(t, leaseView(t, store, FenceOf(a), host), "takeover", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := leaseCall(first, "1", leaseCounter(&calls)); err != nil {
		t.Fatal(err)
	}
	clock.advance(leaseMinute)
	if err := leaseCall(first, "2", leaseCounter(&calls)); leaseReason(err) != "expired" {
		t.Fatalf("expired commit: %v", err)
	}
	head, _ := store.Load(ctx, "takeover")
	b := leaseAcquire(t, store, "takeover", "worker-b")
	if order, _ := CompareLeaseEpochs(b.Epoch, a.Epoch); order != 1 {
		t.Fatalf("takeover epoch %s after %s", b.Epoch, a.Epoch)
	}
	stale, err := leaseDriver(t, leaseView(t, store, FenceOf(a), host), "takeover", true)
	if err == nil {
		err = leaseCall(stale, "3", leaseCounter(&calls))
	}
	if leaseReason(err) != "stale-epoch" {
		t.Fatalf("stale driver: %v", err)
	}
	if _, err := store.CommitFenced(ctx, RunCommit{RunID: "takeover", ExpectedRevision: head.Revision, CommitID: "stale-direct", Checkpoint: []byte(`{}`)}, FenceOf(a)); leaseReason(err) != "stale-epoch" {
		t.Fatalf("stale direct commit: %v", err)
	}
	if after, _ := store.Load(ctx, "takeover"); calls.Load() != 1 || after.Revision != head.Revision {
		t.Fatalf("stale or expired driver wrote or dispatched: %d", calls.Load())
	}
	second, err := leaseDriver(t, leaseView(t, store, FenceOf(b), host), "takeover", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := leaseCall(second, "1", leaseCounter(&calls)); err != nil || calls.Load() != 1 {
		t.Fatalf("takeover replayed a recorded result: %d %v", calls.Load(), err)
	}
	if err := leaseCall(second, "4", leaseCounter(&calls)); err != nil || calls.Load() != 2 {
		t.Fatalf("takeover new operation: %d %v", calls.Load(), err)
	}
}

func TestLeaseEffectHostRefusesAFenceThatExpiredAfterTheClaim(t *testing.T) {
	ctx := context.Background()
	clock := newLeaseClock()
	var armed atomic.Bool
	store := NewMemoryRunStore(MemoryRunStoreOptions{Now: clock.now, AfterCommit: func() error {
		if armed.CompareAndSwap(true, false) {
			clock.advance(2 * leaseMinute)
		}
		return nil
	}})
	host := NewReferenceEffectHost(store)
	var calls atomic.Int64
	a := leaseAcquire(t, store, "effect-host", "worker-a")
	first, err := leaseDriver(t, leaseView(t, store, FenceOf(a), host), "effect-host", false)
	if err != nil {
		t.Fatal(err)
	}
	armed.Store(true)
	if err := leaseCall(first, "1", leaseCounter(&calls)); err == nil {
		t.Fatal("dispatch under an expired fence")
	}
	if calls.Load() != 0 || !reflect.DeepEqual(host.Refusals(), []contract.RunLeaseRefusal{{Version: 1, Reason: "expired"}}) {
		t.Fatalf("the effect host did not refuse: %d %v", calls.Load(), host.Refusals())
	}
	head, _ := store.Load(ctx, "effect-host")
	value, _ := ParseJSON(head.Checkpoint)
	if claims := value.Get("durable").Get("dispatches").Items(); len(claims) != 1 || claims[0].Get("state").Str() != "claimed" {
		t.Fatalf("claim %s", head.Checkpoint)
	}
}

func TestLeaseUnknownInFlightOutcomeSurvivesRestartWithoutDuplicateDispatch(t *testing.T) {
	ctx := context.Background()
	clock := newLeaseClock()
	store := NewMemoryRunStore(MemoryRunStoreOptions{Now: clock.now})
	host := NewReferenceEffectHost(store)
	var calls atomic.Int64
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	a := leaseAcquire(t, store, "in-flight", "worker-a")
	first, err := leaseDriver(t, leaseView(t, store, FenceOf(a), host), "in-flight", false)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		defer close(done)
		_ = leaseCall(first, "1", func([]byte) (ToolResult, error) {
			calls.Add(1)
			close(started)
			<-release
			return ToolResult{}, errors.New("the process ended")
		})
	}()
	<-started
	// The process ends here: nothing more is committed for it.
	head, _ := store.Load(ctx, "in-flight")
	value, _ := ParseJSON(head.Checkpoint)
	claim := value.Get("durable").Get("dispatches").Items()[0]
	if claim.Get("state").Str() != "claimed" {
		t.Fatalf("claim %s", head.Checkpoint)
	}
	clock.advance(2 * leaseMinute)
	b := leaseAcquire(t, store, "in-flight", "worker-b")
	second, err := leaseDriver(t, leaseView(t, store, FenceOf(b), host), "in-flight", true)
	if err != nil {
		t.Fatal(err)
	}
	var unknown *HostOutcomeUnknown
	if err := leaseCall(second, "1", leaseCounter(&calls)); !errors.As(err, &unknown) {
		t.Fatalf("takeover replayed an unknown effect: %v", err)
	}
	operation := EffectOperation{Run: "in-flight", Operation: claim.Get("id").Str(), Source: claim.Get("source").Str(), Tool: claim.Get("tool").Str(), RequestDigest: claim.Get("requestDigest").Str()}
	before, _ := NewEffectAdmission(FenceOf(a), operation)
	after, _ := NewEffectAdmission(FenceOf(b), operation)
	if before.IdempotencyKey != after.IdempotencyKey {
		t.Fatal("the idempotency identity changed with the epoch")
	}
	if err := host.Dispatch(ctx, after, func(context.Context) error { calls.Add(1); return nil }); leaseReason(err) != "already-admitted" {
		t.Fatalf("the effect host admitted the same operation again: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("duplicate dispatch: %d", calls.Load())
	}
	close(release)
	<-done
	if calls.Load() != 1 {
		t.Fatalf("duplicate dispatch after release: %d", calls.Load())
	}
}

func TestLeaseRestoreDoesNotReviveAnExpiredLease(t *testing.T) {
	ctx := context.Background()
	clock := newLeaseClock()
	store := NewMemoryRunStore(MemoryRunStoreOptions{Now: clock.now})
	host := NewReferenceEffectHost(store)
	var calls atomic.Int64
	a := leaseAcquire(t, store, "restored", "worker-a")
	first, err := leaseDriver(t, leaseView(t, store, FenceOf(a), host), "restored", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := leaseCall(first, "1", leaseCounter(&calls)); err != nil {
		t.Fatal(err)
	}
	clock.advance(leaseMinute)
	if _, err := store.RenewLease(ctx, FenceOf(a), leaseMinute); leaseReason(err) != "expired" {
		t.Fatalf("renewal revived an expired lease: %v", err)
	}
	restored, err := leaseDriver(t, leaseView(t, store, FenceOf(a), host), "restored", true)
	if err == nil {
		err = leaseCall(restored, "2", leaseCounter(&calls))
	}
	if leaseReason(err) != "expired" || calls.Load() != 1 {
		t.Fatalf("restored driver: %d %v", calls.Load(), err)
	}
	if next := leaseAcquire(t, store, "restored", "worker-a"); next.Epoch != "2" {
		t.Fatalf("next epoch %s", next.Epoch)
	}
}

type durableLeaseFixture struct{ *MemoryRunStore } // stands for a host store kind

func (durableLeaseFixture) LeaseLifetime() string { return LeaseLifetimeDurable }

type unfencedHost struct{}

func (unfencedHost) EffectFencing() string { return EffectFencingUnsupported }
func (unfencedHost) Dispatch(ctx context.Context, _ EffectAdmission, perform func(context.Context) error) error {
	return perform(ctx)
}

func TestLeaseUnattendedAdmissionRefusesByName(t *testing.T) {
	memory := NewMemoryRunStore(MemoryRunStoreOptions{})
	host := NewReferenceEffectHost(memory)
	callback := NewCheckpointCallbackRunStore(func(context.Context, []byte) error { return nil }, func(raw []byte) ([]byte, error) { return raw, nil })
	fence := LeaseFence{Version: 1, Resource: "r", Owner: "o", Epoch: "1"}
	cases := []struct {
		name   string
		err    error
		reason string
	}{
		{"memory", AdmitUnattended(memory, host), "store-lifetime"},
		{"callback", AdmitUnattended(callback, host), "store-unsupported"},
		{"plain", AdmitUnattended(struct{ RunStore }{memory}, host), "store-unsupported"},
		{"no host", AdmitUnattended(durableLeaseFixture{memory}, nil), "effect-host-unfenced"},
		{"unfenced host", AdmitUnattended(durableLeaseFixture{memory}, unfencedHost{}), "effect-host-unfenced"},
	}
	for _, c := range cases {
		if leaseReason(c.err) != c.reason {
			t.Fatalf("%s: %v", c.name, c.err)
		}
	}
	if err := AdmitUnattended(durableLeaseFixture{memory}, host); err != nil {
		t.Fatal(err)
	}
	if view, err := UnattendedRunStore(durableLeaseFixture{memory}, fence, host); err != nil || view.Durability() != RunStoreMemory {
		t.Fatalf("unattended view: %v", err)
	}
	if _, err := UnattendedRunStore(memory, fence, host); leaseReason(err) != "store-lifetime" {
		t.Fatal(err)
	}
	if _, err := FencedRunStore(callback, fence, nil); leaseReason(err) != "store-unsupported" {
		t.Fatal(err)
	}
	if _, err := FencedRunStore(memory, fence, unfencedHost{}); leaseReason(err) != "effect-host-unfenced" {
		t.Fatal(err)
	}
}

func TestLeaseFencedRunWithoutEffectHostRefusesAnEffect(t *testing.T) {
	store := NewMemoryRunStore(MemoryRunStoreOptions{})
	var calls atomic.Int64
	a := leaseAcquire(t, store, "no-host", "worker-a")
	first, err := leaseDriver(t, leaseView(t, store, FenceOf(a), nil), "no-host", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := leaseCall(first, "1", leaseCounter(&calls)); leaseReason(err) != "effect-host-unfenced" || calls.Load() != 0 {
		t.Fatalf("an unfenced effect ran: %d %v", calls.Load(), err)
	}
}

func TestLeaseDisabledByteParity(t *testing.T) {
	var shape func(*Value) any
	shape = func(v *Value) any {
		switch {
		case v == nil:
			return nil
		case v.IsObject():
			out := map[string]any{}
			for _, key := range v.Keys() {
				out[key] = shape(v.Get(key))
			}
			return out
		case v.IsArray():
			out := []any{}
			for _, item := range v.Items() {
				out = append(out, shape(item))
			}
			return out
		}
		return v.Kind()
	}
	record := func(fenced bool) (any, []any, string) {
		ctx := context.Background()
		store := NewMemoryRunStore(MemoryRunStoreOptions{})
		var view RunStore = store
		if fenced {
			view = leaseView(t, store, FenceOf(leaseAcquire(t, store, "parity", "worker-a")), NewReferenceEffectHost(store))
		}
		binding, err := leaseDriver(t, view, "parity", false)
		var calls atomic.Int64
		if err == nil {
			err = leaseCall(binding, "1", leaseCounter(&calls))
		}
		if err != nil {
			t.Fatal(err)
		}
		head, _ := store.Load(ctx, "parity")
		page, _ := store.ReadEvents(ctx, "parity", "", 100)
		checkpoint, _ := ParseJSON(head.Checkpoint)
		text := []string{string(head.Checkpoint)}
		events := []any{}
		for _, item := range page.Items {
			event, _ := ParseJSON(item.Bytes)
			events = append(events, shape(event))
			text = append(text, string(item.Bytes))
		}
		return shape(checkpoint), events, strings.Join(text, "\n")
	}
	plainCheckpoint, plainEvents, _ := record(false)
	fencedCheckpoint, fencedEvents, text := record(true)
	if !reflect.DeepEqual(plainCheckpoint, fencedCheckpoint) || !reflect.DeepEqual(plainEvents, fencedEvents) {
		t.Fatal("a fenced run changed the stored record shapes")
	}
	for _, word := range []string{"epoch", "fence", "lease", "worker-a"} {
		if strings.Contains(text, word) {
			t.Fatalf("a fenced run stored %q", word)
		}
	}
}
