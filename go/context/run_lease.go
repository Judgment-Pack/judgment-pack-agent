package runcontext

// Run leases and fencing (ADR-0025 decision 1, #223; contract/schemas/run-lease.json). The vocabulary is
// the ADR's: a lease has an owner, an authoritative expiry decided by the store clock and an exact,
// monotonically increasing epoch. The fence (resource, owner, epoch) is the evidence presented at every
// checkpoint commit and checked by the effect host at every admission. A lease grants neither tool
// permission nor approval. The TypeScript twin is packages/harness/storage/lease.ts.
import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
)

const LeaseRecordVersion = 1

// LeaseEpochMax is the largest epoch: 18 decimal digits, exact in a signed 64-bit integer and never
// carried as a JSON number.
const LeaseEpochMax = "999999999999999999"

// LeaseDurationMaxMs bounds a requested lease or renewal duration, in milliseconds of the store clock.
const LeaseDurationMaxMs = 86_400_000

const LeaseLifetimeDurable = "durable"
const LeaseLifetimeProcess = "process"
const EffectFencingEnforced = "enforced"
const EffectFencingUnsupported = "unsupported"

// The wire records are the generated contract types of run-lease.json.
type RunLease = contract.RunLease
type LeaseFence = contract.RunLeaseFence
type EffectOperation = contract.RunLeaseOperation
type EffectAdmission = contract.RunLeaseEffectAdmission

// LeaseAcquire asks for a lease of one resource. An empty ExpectedRevision expects the resource to have
// no revision yet, as RunCommit's does.
type LeaseAcquire struct {
	Resource, Owner, ExpectedRevision string
	DurationMs                        int64
}

var LeaseRefusalReasons = []string{"invalid", "no-lease", "held", "revision-changed", "stale-epoch", "other-owner", "expired", "released", "epoch-exhausted", "resource-mismatch", "already-admitted"}
var LeaseCapabilityReasons = []string{"store-unsupported", "store-lifetime", "effect-host-unfenced"}

// LeaseRefused is a named lease or fence refusal. Nothing was committed or admitted, except
// already-admitted: an effect host admitted that idempotency identity before, so its outcome is unknown
// and it is never performed again.
type LeaseRefused struct{ Reason string }

func (e *LeaseRefused) Error() string { return "LeaseRefused: " + e.Reason + "." }
func (e *LeaseRefused) Unwrap() error {
	f := failure.New("stale-state", "storage", "not-started")
	if e.Reason == "already-admitted" {
		f = failure.New("unknown", "tool", "unknown")
	}
	detail := e.Error()
	f.Detail = &detail
	return failure.NewError(f)
}
func (e *LeaseRefused) Record() contract.RunLeaseRefusal {
	return contract.RunLeaseRefusal{Version: LeaseRecordVersion, Reason: e.Reason}
}

// LeaseCapabilityRefused: a store or effect host that cannot give the guarantee refuses unattended
// admission by name; it never pretends.
type LeaseCapabilityRefused struct{ Reason string }

func (e *LeaseCapabilityRefused) Error() string { return "LeaseCapabilityRefused: " + e.Reason + "." }
func (e *LeaseCapabilityRefused) Unwrap() error {
	f := failure.New("configuration", "storage", "not-started")
	detail := e.Error()
	f.Detail = &detail
	return failure.NewError(f)
}
func (e *LeaseCapabilityRefused) Record() contract.RunLeaseCapabilityRefusal {
	return contract.RunLeaseCapabilityRefusal{Version: LeaseRecordVersion, Reason: e.Reason}
}

func leaseInvalid() error { return &LeaseRefused{Reason: "invalid"} }

var leaseEpochPattern = regexp.MustCompile(`^[1-9][0-9]{0,17}$`)
var leaseExpiryPattern = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}\.[0-9]{3}Z$`)
var leaseDigestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

const leaseExpiryLayout = "2006-01-02T15:04:05.000Z"

// ValidLeaseEpoch: a canonical base-10 string from 1 to LeaseEpochMax. A sign, fraction, exponent,
// leading zero or space is not an epoch.
func ValidLeaseEpoch(epoch string) bool { return leaseEpochPattern.MatchString(epoch) }

// NextLeaseEpoch: 1 for a resource never leased (""), otherwise exactly one more. Never reused or
// decremented; past LeaseEpochMax it refuses as epoch-exhausted.
func NextLeaseEpoch(previous string) (string, error) {
	if previous == "" {
		return "1", nil
	}
	if !ValidLeaseEpoch(previous) {
		return "", leaseInvalid()
	}
	if previous == LeaseEpochMax {
		return "", &LeaseRefused{Reason: "epoch-exhausted"}
	}
	value, _ := new(big.Int).SetString(previous, 10)
	return value.Add(value, big.NewInt(1)).String(), nil
}

// CompareLeaseEpochs orders two epochs exactly.
func CompareLeaseEpochs(a, b string) (int, error) {
	if !ValidLeaseEpoch(a) || !ValidLeaseEpoch(b) {
		return 0, leaseInvalid()
	}
	x, _ := new(big.Int).SetString(a, 10)
	y, _ := new(big.Int).SetString(b, 10)
	return x.Cmp(y), nil
}

func leaseExpiryMillis(expiry string) (int64, bool) {
	if !leaseExpiryPattern.MatchString(expiry) {
		return 0, false
	}
	at, err := time.Parse(leaseExpiryLayout, expiry)
	if err != nil || at.UTC().Format(leaseExpiryLayout) != expiry {
		return 0, false
	}
	return at.UnixMilli(), true
}
func leaseExpiryAt(ms int64) (string, error) {
	text := time.UnixMilli(ms).UTC().Format(leaseExpiryLayout)
	if _, ok := leaseExpiryMillis(text); !ok {
		return "", leaseInvalid()
	}
	return text, nil
}

// leaseObject decodes one closed version 1 object with exactly these members. An integral version
// spelling such as 1.0 is the same version, as ADR-0024 reads schema-owned integers.
func leaseObject(raw []byte, members ...string) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&object); err != nil || object == nil || decoder.More() || len(object) != len(members) {
		return nil, leaseInvalid()
	}
	for _, member := range members {
		if _, ok := object[member]; !ok {
			return nil, leaseInvalid()
		}
	}
	if version, ok := object["version"]; ok {
		var number float64
		if json.Unmarshal(version, &number) != nil || number != LeaseRecordVersion {
			return nil, leaseInvalid()
		}
	}
	return object, nil
}
func leaseString(object map[string]json.RawMessage, member string) (string, error) {
	var value string
	if raw := object[member]; len(raw) == 0 || raw[0] != '"' || json.Unmarshal(raw, &value) != nil {
		return "", leaseInvalid()
	}
	return value, nil
}
func validLeaseFence(fence LeaseFence) bool {
	return fence.Version == LeaseRecordVersion && validStorageID(fence.Resource) && validStorageID(fence.Owner) && ValidLeaseEpoch(fence.Epoch)
}
func validRunLease(lease RunLease) bool {
	_, ok := leaseExpiryMillis(lease.Expiry)
	return lease.Version == LeaseRecordVersion && validStorageID(lease.Resource) && validStorageID(lease.Owner) && ValidLeaseEpoch(lease.Epoch) && ok && (lease.State == "held" || lease.State == "released")
}
func validEffectOperation(op EffectOperation) bool {
	return validStorageID(op.Run) && validStorageID(op.Operation) && validStorageID(op.Source) && validStorageID(op.Tool) && leaseDigestPattern.MatchString(op.RequestDigest)
}

// ReadLeaseFence reads a version 1 fence record.
func ReadLeaseFence(raw []byte) (LeaseFence, error) {
	object, err := leaseObject(raw, "version", "resource", "owner", "epoch")
	if err != nil {
		return LeaseFence{}, err
	}
	fence := LeaseFence{Version: LeaseRecordVersion}
	if fence.Resource, err = leaseString(object, "resource"); err != nil {
		return LeaseFence{}, err
	}
	if fence.Owner, err = leaseString(object, "owner"); err != nil {
		return LeaseFence{}, err
	}
	if fence.Epoch, err = leaseString(object, "epoch"); err != nil {
		return LeaseFence{}, err
	}
	if !validLeaseFence(fence) {
		return LeaseFence{}, leaseInvalid()
	}
	return fence, nil
}

// ReadRunLease reads a version 1 lease record.
func ReadRunLease(raw []byte) (RunLease, error) {
	object, err := leaseObject(raw, "version", "resource", "owner", "epoch", "expiry", "state")
	if err != nil {
		return RunLease{}, err
	}
	lease := RunLease{Version: LeaseRecordVersion}
	for member, target := range map[string]*string{"resource": &lease.Resource, "owner": &lease.Owner, "epoch": &lease.Epoch, "expiry": &lease.Expiry, "state": &lease.State} {
		if *target, err = leaseString(object, member); err != nil {
			return RunLease{}, err
		}
	}
	if !validRunLease(lease) {
		return RunLease{}, leaseInvalid()
	}
	return lease, nil
}

func readLeaseReason(raw []byte, reasons []string) (string, error) {
	object, err := leaseObject(raw, "version", "reason")
	if err != nil {
		return "", err
	}
	reason, err := leaseString(object, "reason")
	if err != nil || !slices.Contains(reasons, reason) {
		return "", leaseInvalid()
	}
	return reason, nil
}

// ReadLeaseRefusal reads a version 1 refusal record.
func ReadLeaseRefusal(raw []byte) (contract.RunLeaseRefusal, error) {
	reason, err := readLeaseReason(raw, LeaseRefusalReasons)
	return contract.RunLeaseRefusal{Version: LeaseRecordVersion, Reason: reason}, err
}

// ReadLeaseCapabilityRefusal reads a version 1 capability refusal record.
func ReadLeaseCapabilityRefusal(raw []byte) (contract.RunLeaseCapabilityRefusal, error) {
	reason, err := readLeaseReason(raw, LeaseCapabilityReasons)
	return contract.RunLeaseCapabilityRefusal{Version: LeaseRecordVersion, Reason: reason}, err
}

// FenceOf is the fence a lease's holder presents.
func FenceOf(lease RunLease) LeaseFence {
	return LeaseFence{Version: LeaseRecordVersion, Resource: lease.Resource, Owner: lease.Owner, Epoch: lease.Epoch}
}

// EffectIdempotencyKey is the SHA-256 of the canonical JSON of the operation alone. It carries no epoch,
// so it is the same after a takeover.
func EffectIdempotencyKey(op EffectOperation) (string, error) {
	if !validEffectOperation(op) {
		return "", leaseInvalid()
	}
	canonical := CanonicalJSON(ObjectOf("run", String(op.Run), "operation", String(op.Operation), "source", String(op.Source), "tool", String(op.Tool), "requestDigest", String(op.RequestDigest)))
	return durableDigest([]byte(canonical)), nil
}

// NewEffectAdmission is what an effect host receives for one operation under one fence.
func NewEffectAdmission(fence LeaseFence, op EffectOperation) (EffectAdmission, error) {
	if !validLeaseFence(fence) {
		return EffectAdmission{}, leaseInvalid()
	}
	key, err := EffectIdempotencyKey(op)
	if err != nil {
		return EffectAdmission{}, err
	}
	return EffectAdmission{Version: LeaseRecordVersion, Fence: fence, Operation: op, IdempotencyKey: key}, nil
}
func checkEffectAdmission(admission EffectAdmission) error {
	held, err := NewEffectAdmission(admission.Fence, admission.Operation)
	if err != nil || admission.Version != LeaseRecordVersion || held.IdempotencyKey != admission.IdempotencyKey {
		return leaseInvalid()
	}
	return nil
}

// ReadEffectAdmission reads a version 1 admission record and refuses a key that is not its operation's.
func ReadEffectAdmission(raw []byte) (EffectAdmission, error) {
	object, err := leaseObject(raw, "version", "fence", "operation", "idempotencyKey")
	if err != nil {
		return EffectAdmission{}, err
	}
	fence, err := ReadLeaseFence(object["fence"])
	if err != nil {
		return EffectAdmission{}, err
	}
	operation, err := leaseObject(object["operation"], "run", "operation", "source", "tool", "requestDigest")
	if err != nil {
		return EffectAdmission{}, err
	}
	var op EffectOperation
	for member, target := range map[string]*string{"run": &op.Run, "operation": &op.Operation, "source": &op.Source, "tool": &op.Tool, "requestDigest": &op.RequestDigest} {
		if *target, err = leaseString(operation, member); err != nil {
			return EffectAdmission{}, err
		}
	}
	key, err := leaseString(object, "idempotencyKey")
	if err != nil {
		return EffectAdmission{}, err
	}
	admission := EffectAdmission{Version: LeaseRecordVersion, Fence: fence, Operation: op, IdempotencyKey: key}
	if err := checkEffectAdmission(admission); err != nil {
		return EffectAdmission{}, err
	}
	return admission, nil
}

// LeaseStore is the lease port a store kind implements once per resource kind (desk #609). Every
// transition is atomic and checked against the store's own clock; a caller's clock never extends a lease.
type LeaseStore interface {
	LeaseVersion() int
	// LeaseLifetime is durable when leases and epochs survive the store's process, process when they
	// end with it (the memory kind).
	LeaseLifetime() string
	// AcquireLease compares ExpectedRevision with the resource's current revision and refuses held while
	// an unexpired lease is held.
	AcquireLease(context.Context, LeaseAcquire) (RunLease, error)
	// RenewLease: current owner and epoch only, before expiry. An expired lease is never revived.
	RenewLease(context.Context, LeaseFence, int64) (RunLease, error)
	// ReleaseLease retains the epoch, so the next acquisition is strictly greater.
	ReleaseLease(context.Context, LeaseFence) (RunLease, error)
	ReadLease(context.Context, string) (*RunLease, error)
	// CheckFence is the authority an effect host checks a fence against.
	CheckFence(context.Context, LeaseFence) error
}

// LeaseRunStore commits with the fence checked in the same transaction as the revision.
type LeaseRunStore interface {
	RunStore
	LeaseStore
	CommitFenced(context.Context, RunCommit, LeaseFence) (RunReceipt, error)
}

// LeaseHistoryRunStore is required of a lease store that also has the history capability.
type LeaseHistoryRunStore interface {
	LeaseRunStore
	HistoryRunStore
	CommitHistoryFenced(context.Context, RunHistoryCommit, LeaseFence) (RunHistoryReceipt, error)
	// CommitContextSnapshotFenced: a leased run's chat snapshot commits only under its current fence (the
	// fence names the run; the snapshot names its chat), so a driver that lost its lease cannot release the
	// turn its takeover resumes.
	CommitContextSnapshotFenced(context.Context, ContextSnapshotUpdate, string, LeaseFence) (ContextSnapshotHead, error)
}

// LeaseTable holds the lease transitions in memory. MemoryRunStore calls it under its own lock; a host
// store kind implements the same transitions against its own durable rows. It is not safe for
// concurrent use on its own.
type LeaseTable struct {
	rows map[string]RunLease
	now  func() time.Time
}

func NewLeaseTable(now func() time.Time) *LeaseTable {
	if now == nil {
		now = time.Now
	}
	return &LeaseTable{rows: map[string]RunLease{}, now: now}
}
func (t *LeaseTable) millis() (int64, error) {
	at := t.now()
	if at.IsZero() {
		return 0, leaseInvalid()
	}
	return at.UnixMilli(), nil
}
func (t *LeaseTable) Read(resource string) (*RunLease, error) {
	if !validStorageID(resource) {
		return nil, leaseInvalid()
	}
	row, ok := t.rows[resource]
	if !ok {
		return nil, nil
	}
	return &row, nil
}
func (t *LeaseTable) Acquire(request LeaseAcquire, currentRevision string) (RunLease, error) {
	return t.acquireWithin(request, currentRevision, nil)
}

// acquireWithin is Acquire with a ceiling (#225, store-clock milliseconds, nil for none): the latest the
// lease may expire, the run's deadline or an approval's expiry. The caller refuses before it when the
// ceiling is not after the store's time.
func (t *LeaseTable) acquireWithin(request LeaseAcquire, currentRevision string, ceiling *int64) (RunLease, error) {
	if !validStorageID(request.Resource) || !validStorageID(request.Owner) || request.ExpectedRevision != "" && !validStorageID(request.ExpectedRevision) || request.DurationMs < 1 || request.DurationMs > LeaseDurationMaxMs {
		return RunLease{}, leaseInvalid()
	}
	now, err := t.millis()
	if err != nil {
		return RunLease{}, err
	}
	if request.ExpectedRevision != currentRevision {
		return RunLease{}, &LeaseRefused{Reason: "revision-changed"}
	}
	row, held := t.rows[request.Resource]
	if held && row.State == "held" {
		if expiry, _ := leaseExpiryMillis(row.Expiry); now < expiry {
			return RunLease{}, &LeaseRefused{Reason: "held"}
		}
	}
	epoch, err := NextLeaseEpoch(row.Epoch)
	if err != nil {
		return RunLease{}, err
	}
	expiry, err := leaseExpiryAt(within(now+request.DurationMs, ceiling))
	if err != nil {
		return RunLease{}, err
	}
	lease := RunLease{Version: LeaseRecordVersion, Resource: request.Resource, Owner: request.Owner, Epoch: epoch, Expiry: expiry, State: "held"}
	t.rows[lease.Resource] = lease
	return lease, nil
}

// Check returns the named reason unless the fence is current: this epoch, this owner, held and
// unexpired by the store clock. An expiry equal to the store's time is expired.
func (t *LeaseTable) Check(fence LeaseFence) (RunLease, error) {
	if !validLeaseFence(fence) {
		return RunLease{}, leaseInvalid()
	}
	now, err := t.millis()
	if err != nil {
		return RunLease{}, err
	}
	row, ok := t.rows[fence.Resource]
	switch expiry, _ := leaseExpiryMillis(row.Expiry); {
	case !ok:
		return RunLease{}, &LeaseRefused{Reason: "no-lease"}
	case row.Epoch != fence.Epoch:
		return RunLease{}, &LeaseRefused{Reason: "stale-epoch"}
	case row.Owner != fence.Owner:
		return RunLease{}, &LeaseRefused{Reason: "other-owner"}
	case row.State == "released":
		return RunLease{}, &LeaseRefused{Reason: "released"}
	case now >= expiry:
		return RunLease{}, &LeaseRefused{Reason: "expired"}
	}
	return row, nil
}
func (t *LeaseTable) Renew(fence LeaseFence, durationMs int64) (RunLease, error) {
	return t.RenewWithin(fence, durationMs, nil)
}

// within is at, bounded by a ceiling (nil for none). A ceiling is a pointer, never a sentinel: the
// Unix epoch is a deadline like any other (#266 M2).
func within(at int64, ceiling *int64) int64 {
	if ceiling != nil && *ceiling < at {
		return *ceiling
	}
	return at
}

// RenewWithin (#225): a renewal never passes the ceiling (store-clock milliseconds, nil for none):
// renewing a lease renews neither the run's deadline nor an approval.
func (t *LeaseTable) RenewWithin(fence LeaseFence, durationMs int64, ceiling *int64) (RunLease, error) {
	if durationMs < 1 || durationMs > LeaseDurationMaxMs {
		return RunLease{}, leaseInvalid()
	}
	row, err := t.Check(fence)
	if err != nil {
		return RunLease{}, err
	}
	now, err := t.millis()
	if err != nil {
		return RunLease{}, err
	}
	if row.Expiry, err = leaseExpiryAt(within(now+durationMs, ceiling)); err != nil {
		return RunLease{}, err
	}
	t.rows[row.Resource] = row
	return row, nil
}

// Lower (#225) shortens a held lease to a deadline set while it is held; it never lengthens one.
func (t *LeaseTable) Lower(resource string, ceiling int64) {
	row, ok := t.rows[resource]
	if expiry, _ := leaseExpiryMillis(row.Expiry); ok && row.State == "held" && expiry > ceiling {
		if text, err := leaseExpiryAt(ceiling); err == nil {
			row.Expiry = text
			t.rows[resource] = row
		}
	}
}
func (t *LeaseTable) Release(fence LeaseFence) (RunLease, error) {
	row, err := t.Check(fence)
	if err != nil {
		return RunLease{}, err
	}
	row.State = "released"
	t.rows[row.Resource] = row
	return row, nil
}

func (m *MemoryRunStore) leaseTransition(ctx context.Context, run func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	return run()
}

// MemoryRunStore follows the lease transitions within its own process only: LeaseLifetime is process,
// so it never qualifies for unattended admission and a new instance starts with no leases.
func (m *MemoryRunStore) LeaseVersion() int     { return LeaseRecordVersion }
func (m *MemoryRunStore) LeaseLifetime() string { return LeaseLifetimeProcess }

// AcquireLease: #265 M2 and #225, a run a wait holds is leased only by its resume's claim, and no lease
// outlives the run's deadline.
func (m *MemoryRunStore) AcquireLease(ctx context.Context, request LeaseAcquire) (lease RunLease, err error) {
	err = m.leaseTransition(ctx, func() error {
		if validStorageID(request.Resource) {
			if err := m.waits.AdmitAcquire(request.Resource); err != nil {
				return err
			}
		}
		lease, err = m.leases.acquireWithin(request, m.runs[request.Resource].head.Revision, m.waits.Ceiling(request.Resource))
		return err
	})
	return lease, err
}
func (m *MemoryRunStore) RenewLease(ctx context.Context, fence LeaseFence, durationMs int64) (lease RunLease, err error) {
	err = m.leaseTransition(ctx, func() error {
		lease, err = m.leases.RenewWithin(fence, durationMs, m.waits.Ceiling(fence.Resource))
		return err
	})
	return lease, err
}
func (m *MemoryRunStore) ReleaseLease(ctx context.Context, fence LeaseFence) (lease RunLease, err error) {
	err = m.leaseTransition(ctx, func() error { lease, err = m.leases.Release(fence); return err })
	return lease, err
}
func (m *MemoryRunStore) ReadLease(ctx context.Context, resource string) (lease *RunLease, err error) {
	err = m.leaseTransition(ctx, func() error { lease, err = m.leases.Read(resource); return err })
	return lease, err
}
func (m *MemoryRunStore) CheckFence(ctx context.Context, fence LeaseFence) error {
	return m.leaseTransition(ctx, func() error { _, err := m.leases.Check(fence); return err })
}
func (m *MemoryRunStore) CommitFenced(ctx context.Context, input RunCommit, fence LeaseFence) (RunReceipt, error) {
	receipt, _, err := m.commitTransaction(ctx, input, nil, &fence)
	return receipt, err
}
func (m *MemoryRunStore) CommitHistoryFenced(ctx context.Context, input RunHistoryCommit, fence LeaseFence) (RunHistoryReceipt, error) {
	extra, err := freezeHistoryExtra(input)
	if err != nil {
		return RunHistoryReceipt{}, err
	}
	_, receipt, err := m.commitTransaction(ctx, input.Run, &extra, &fence)
	return receipt, err
}

// checkCommitFence is called under the store lock, at admission and again at publication.
func (m *MemoryRunStore) checkCommitFence(runID string, fence *LeaseFence) error {
	if fence == nil {
		return nil
	}
	if !validLeaseFence(*fence) {
		return leaseInvalid()
	}
	if fence.Resource != runID {
		return &LeaseRefused{Reason: "resource-mismatch"}
	}
	_, err := m.leases.Check(*fence)
	return err
}

// EffectHost is the effect-host port. The host itself receives the fence and checks it at its own
// admission boundary, then performs the effect: a client-side check or a store compare-and-set alone
// does not stop a duplicate external effect.
type EffectHost interface {
	EffectFencing() string
	Dispatch(context.Context, EffectAdmission, func(context.Context) error) error
}

// LeaseAuthority is where an effect host checks a fence: the store's own lease authority, or an
// enforcement service in front of it.
type LeaseAuthority interface {
	CheckFence(context.Context, LeaseFence) error
}

// ReferenceEffectHost serializes admission: the fence is checked against the authority and the
// idempotency identity is recorded before the effect is performed. An identity admitted before is
// refused as already-admitted whatever the epoch: its outcome is unknown until its own host reconciles
// it, and a takeover never replays it.
type ReferenceEffectHost struct {
	self        *ReferenceEffectHost // identity: a copy shares the key map without the lock, and is not evidence
	mu          sync.Mutex
	authority   LeaseAuthority
	admitted    map[string]bool
	refusals    []contract.RunLeaseRefusal
	credentials CredentialCheck
}

func NewReferenceEffectHost(authority LeaseAuthority) *ReferenceEffectHost {
	return NewReferenceEffectHostWith(authority, ReferenceEffectHostOptions{})
}

// ReferenceEffectHostOptions (#226): with Credentials, the host asks the check at its admission boundary,
// after the fence and the idempotency identity and before it admits the operation; a refusal is returned
// as NewReauthorizationRequired gives it, nothing admitted and nothing performed.
type ReferenceEffectHostOptions struct {
	Credentials CredentialCheck
}

func NewReferenceEffectHostWith(authority LeaseAuthority, options ReferenceEffectHostOptions) *ReferenceEffectHost {
	host := &ReferenceEffectHost{authority: authority, admitted: map[string]bool{}, credentials: options.Credentials}
	host.self = host
	return host
}

// CheckEffectHostConformance (#223 review, #253, #258) is the effect-host conformance. The kit owns the lease
// authority it hands the factory and moves its clock. The host must refuse, without performing, every time
// the authority is unavailable, which the kit makes it several times in a row before the first dispatch,
// after a success and between dispatches; admit a current fence once, also once the authority is back; and
// refuse, without performing, an identity it admitted before (also under a takeover's fence), another owner,
// a stale or forged epoch, a fence for another run than the operation's, a key that is not its operation's,
// an expired lease and a released one. The TypeScript twin is effectHostConformance.
func CheckEffectHostConformance(ctx context.Context, create func(LeaseAuthority) EffectHost) error {
	kit := newKitAuthority()
	return exerciseEffectHost(ctx, create(kit), kit)
}

// authorityFailures (#258) is how many times in a row the kit fails the authority for one operation retried,
// and then for as many new ones, at each point it fails it. No finite count proves a host never degrades;
// this one fails a breaker of the usual sizes.
const authorityFailures = 5

// kitAuthority is the conformance kit's lease authority: its own table, clock and failure switch.
type kitAuthority struct {
	mu      sync.Mutex
	at      time.Time
	failing bool
	table   *LeaseTable
}

func newKitAuthority() *kitAuthority {
	kit := &kitAuthority{at: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	kit.table = NewLeaseTable(func() time.Time { return kit.at })
	return kit
}
func (k *kitAuthority) CheckFence(_ context.Context, fence LeaseFence) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.failing {
		return errors.New("the lease authority is unavailable")
	}
	_, err := k.table.Check(fence)
	return err
}
func (k *kitAuthority) do(run func()) {
	k.mu.Lock()
	defer k.mu.Unlock()
	run()
}

// exerciseEffectHost runs the checks on the exact host instance given, against the kit's authority.
func exerciseEffectHost(ctx context.Context, host EffectHost, kit *kitAuthority) error {
	fail := func(property string) error { return &StorageConformanceFailure{Property: property} }
	if host == nil || host.EffectFencing() != EffectFencingEnforced {
		return fail("host.declared")
	}
	op := func(operation, run string) EffectOperation {
		return EffectOperation{Run: run, Operation: operation, Source: "host", Tool: "effect", RequestDigest: "sha256:" + strings.Repeat("b", 64)}
	}
	performed := 0
	perform := func(context.Context) error { performed++; return nil }
	admission := func(fence LeaseFence, operation EffectOperation) EffectAdmission {
		held, _ := NewEffectAdmission(fence, operation)
		return held
	}
	refuses := func(held EffectAdmission, reason string) bool {
		before := performed
		var refused *LeaseRefused
		err := host.Dispatch(ctx, held, perform)
		if reason == "" {
			return err != nil && performed == before
		}
		return errors.As(err, &refused) && refused.Reason == reason && performed == before
	}
	acquire := func(owner string) (fence LeaseFence, err error) {
		kit.do(func() {
			var lease RunLease
			lease, err = kit.table.Acquire(LeaseAcquire{Resource: "host-run", Owner: owner, DurationMs: 1000}, "")
			fence = FenceOf(lease)
		})
		return fence, err
	}
	a, err := acquire("worker-a")
	if err != nil {
		return fail("host.operation")
	}
	// #258: unavailable ownership evidence closes dispatch every time, however often and whenever the
	// authority errors.
	unavailable := func(point string) bool {
		kit.do(func() { kit.failing = true })
		defer kit.do(func() { kit.failing = false })
		for n := 0; n < authorityFailures; n++ {
			if !refuses(admission(a, op(point+"-retried", "host-run")), "") {
				return false
			}
		}
		for n := 0; n < authorityFailures; n++ {
			if !refuses(admission(a, op(point+"-"+strconv.Itoa(n), "host-run")), "") {
				return false
			}
		}
		return true
	}
	if !unavailable("before-any-dispatch") {
		return fail("host.authority-unavailable")
	}
	if err := host.Dispatch(ctx, admission(a, op("op-1", "host-run")), perform); err != nil || performed != 1 {
		return fail("host.admit.current")
	}
	if !unavailable("after-success") {
		return fail("host.authority-unavailable.after-success")
	}
	if !refuses(admission(a, op("op-1", "host-run")), "already-admitted") {
		return fail("host.already-admitted")
	}
	other := a
	other.Owner = "worker-x"
	if !refuses(admission(other, op("op-2", "host-run")), "other-owner") {
		return fail("host.other-owner")
	}
	forged := a
	forged.Epoch = "2"
	if !refuses(admission(forged, op("op-3", "host-run")), "stale-epoch") {
		return fail("host.forged-epoch")
	}
	if !refuses(admission(a, op("op-4", "other-run")), "resource-mismatch") {
		return fail("host.resource-mismatch")
	}
	mismatched := admission(a, op("op-5", "host-run"))
	mismatched.IdempotencyKey = admission(a, op("op-6", "host-run")).IdempotencyKey
	if !refuses(mismatched, "invalid") {
		return fail("host.invalid")
	}
	if !unavailable("between-dispatches") {
		return fail("host.authority-unavailable.between-dispatches")
	}
	if err := host.Dispatch(ctx, admission(a, op("op-10", "host-run")), perform); err != nil || performed != 2 {
		return fail("host.admit.recovered")
	}
	kit.do(func() { kit.at = kit.at.Add(time.Second) })
	if !refuses(admission(a, op("op-7", "host-run")), "expired") {
		return fail("host.expired")
	}
	b, err := acquire("worker-b")
	if err != nil {
		return fail("host.operation")
	}
	if !refuses(admission(a, op("op-8", "host-run")), "stale-epoch") {
		return fail("host.stale-epoch")
	}
	if !refuses(admission(b, op("op-1", "host-run")), "already-admitted") {
		return fail("host.takeover.already-admitted")
	}
	kit.do(func() { _, err = kit.table.Release(b) })
	if err != nil || !refuses(admission(b, op("op-9", "host-run")), "released") {
		return fail("host.released")
	}
	if performed != 2 {
		return fail("host.performed-once")
	}
	return nil
}

// boundAuthority answers from the kit until certification passes, then from the real authority for good.
type boundAuthority struct {
	live      atomic.Bool
	kit       *kitAuthority
	authority LeaseAuthority
}

func (b *boundAuthority) CheckFence(ctx context.Context, fence LeaseFence) error {
	if b.live.Load() {
		return b.authority.CheckFence(ctx, fence)
	}
	return b.kit.CheckFence(ctx, fence)
}

// CertifiedEffectHost is the evidence unattended admission accepts for a host other than the reference
// host: the exact instance the kit exercised and the authority it was certified for. Only CertifyEffectHost
// makes one; it holds no registry entry, so a released host is collected like any other value.
type CertifiedEffectHost struct {
	self      *CertifiedEffectHost // identity: a copy of the value is not the evidence
	host      EffectHost
	authority LeaseAuthority
}

func (c *CertifiedEffectHost) EffectFencing() string { return EffectFencingEnforced }
func (c *CertifiedEffectHost) Dispatch(ctx context.Context, admission EffectAdmission, perform func(context.Context) error) error {
	if c == nil || c.self != c || c.host == nil {
		return &LeaseCapabilityRefused{Reason: "effect-host-unfenced"}
	}
	return c.host.Dispatch(ctx, admission, perform)
}

// CertifyEffectHost (#253) certifies an effect host over authority (for unattended admission, the run's own
// store). The factory is called once, over an authority the kit controls until the checks pass and that
// then answers from authority for good; the exact instance the kit exercised is the one admitted.
func CertifyEffectHost(ctx context.Context, create func(LeaseAuthority) EffectHost, authority LeaseAuthority) (*CertifiedEffectHost, error) {
	if authority == nil {
		return nil, &LeaseCapabilityRefused{Reason: "effect-host-unfenced"}
	}
	bound := &boundAuthority{kit: newKitAuthority(), authority: authority}
	host := create(bound)
	if err := exerciseEffectHost(ctx, host, bound.kit); err != nil {
		return nil, err
	}
	bound.live.Store(true)
	certified := &CertifiedEffectHost{host: host, authority: authority}
	certified.self = certified
	return certified, nil
}

// sameAuthority is identity: the same pointer, or the same comparable value of the same type.
func sameAuthority(authority LeaseAuthority, store RunStore) bool {
	if authority == nil || store == nil {
		return false
	}
	a, s := reflect.ValueOf(authority), reflect.ValueOf(store)
	if a.Type() != s.Type() {
		return false
	}
	if a.Kind() == reflect.Pointer {
		return a.Pointer() == s.Pointer()
	}
	return a.Type().Comparable() && any(authority) == any(store)
}

// provenAuthority is the authority a host is proven to enforce against, or nil.
func provenAuthority(host EffectHost) LeaseAuthority {
	switch h := host.(type) {
	case *ReferenceEffectHost:
		if h != nil && h.self == h && h.admitted != nil {
			return h.authority
		}
	case *CertifiedEffectHost:
		if h != nil && h.self == h && h.host != nil {
			return h.authority
		}
	}
	return nil
}

func (h *ReferenceEffectHost) EffectFencing() string { return EffectFencingEnforced }

// Refusals is every refusal this host gave, as its versioned record, in order.
func (h *ReferenceEffectHost) Refusals() []contract.RunLeaseRefusal {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.refusals)
}
func (h *ReferenceEffectHost) admit(ctx context.Context, admission EffectAdmission) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	err := ctx.Err()
	if err == nil {
		err = checkEffectAdmission(admission)
	}
	if err == nil && admission.Fence.Resource != admission.Operation.Run {
		err = &LeaseRefused{Reason: "resource-mismatch"}
	}
	if err == nil {
		err = h.authority.CheckFence(ctx, admission.Fence)
	}
	if err == nil && h.admitted[admission.IdempotencyKey] {
		err = &LeaseRefused{Reason: "already-admitted"}
	}
	// #226: the credential check is at the admission boundary: a refusal admits nothing and performs nothing.
	// It can take longer than the lease has left, so the abort and the fence are looked at again before
	// anything is admitted.
	if err == nil && h.credentials != nil {
		refusal, checkErr := h.credentials(ctx, admission.Operation)
		if err = ctx.Err(); err == nil {
			if checkErr != nil {
				err = &CredentialCheckFailed{}
			} else if err = h.authority.CheckFence(ctx, admission.Fence); err == nil && refusal != nil {
				err = NewReauthorizationRequired(ctx, admission, *refusal)
			}
		}
	}
	if err == nil {
		h.admitted[admission.IdempotencyKey] = true
	}
	if refused, ok := err.(*LeaseRefused); ok {
		h.refusals = append(h.refusals, refused.Record())
	}
	return err
}
func (h *ReferenceEffectHost) Dispatch(ctx context.Context, admission EffectAdmission, perform func(context.Context) error) error {
	if err := h.admit(ctx, admission); err != nil {
		return err
	}
	return perform(ctx)
}

// fencedRunStore is a view of a lease store whose every checkpoint commit presents its fence, checked by
// the store in the same transaction. A non-read host or runtime dispatch through a writer on it is
// admitted by host with the same fence; with no host it refuses effect-host-unfenced.
type fencedRunStore struct {
	store LeaseRunStore
	fence LeaseFence
	host  EffectHost
	// #226: the trusted re-authorization refusal the view's last dispatch ended in, and a resumed
	// re-authorization wait's grant.
	mu              sync.Mutex
	reauthorization *ReauthorizationRequired
	grant           *reauthGrant
}

func (f *fencedRunStore) Durability() string { return f.store.Durability() }
func (f *fencedRunStore) Load(ctx context.Context, id string) (*RunHead, error) {
	return f.store.Load(ctx, id)
}
func (f *fencedRunStore) Commit(ctx context.Context, input RunCommit) (RunReceipt, error) {
	return f.store.CommitFenced(ctx, input, f.fence)
}
func (f *fencedRunStore) ReadEvents(ctx context.Context, id, after string, limit int) (RunEventPage, error) {
	return f.store.ReadEvents(ctx, id, after, limit)
}

// fencedHistoryRunStore keeps the history capability of a lease history store, fencing both commits.
type fencedHistoryRunStore struct {
	HistoryRunStore
	fenced *fencedRunStore
	store  LeaseHistoryRunStore
}

func (f *fencedHistoryRunStore) Commit(ctx context.Context, input RunCommit) (RunReceipt, error) {
	return f.fenced.Commit(ctx, input)
}
func (f *fencedHistoryRunStore) CommitHistory(ctx context.Context, input RunHistoryCommit) (RunHistoryReceipt, error) {
	return f.store.CommitHistoryFenced(ctx, input, f.fenced.fence)
}
func (f *fencedHistoryRunStore) CommitContextSnapshot(ctx context.Context, input ContextSnapshotUpdate, commitID string) (ContextSnapshotHead, error) {
	return f.store.CommitContextSnapshotFenced(ctx, input, commitID, f.fenced.fence)
}

// FencedRunStore returns the view a durable run uses as its RunStore under a lease. The store keeps its
// own durability; the view adds no lifetime it does not have. A store without the lease port, or with
// history but no fenced history commit, refuses store-unsupported; a host that does not enforce the
// fence refuses effect-host-unfenced. host may be nil for a run with no non-read effects.
func FencedRunStore(store RunStore, fence LeaseFence, host EffectHost) (RunStore, error) {
	lease, ok := store.(LeaseRunStore)
	if !ok || lease.LeaseVersion() != LeaseRecordVersion || store.Durability() == RunStoreSingleWriterCallback {
		return nil, &LeaseCapabilityRefused{Reason: "store-unsupported"}
	}
	history, withHistory := store.(HistoryRunStore)
	leaseHistory, fencedHistory := store.(LeaseHistoryRunStore)
	if withHistory && history.HistoryVersion() == 1 && !fencedHistory {
		return nil, &LeaseCapabilityRefused{Reason: "store-unsupported"}
	}
	if host != nil && host.EffectFencing() != EffectFencingEnforced {
		return nil, &LeaseCapabilityRefused{Reason: "effect-host-unfenced"}
	}
	if !validLeaseFence(fence) {
		return nil, leaseInvalid()
	}
	view := &fencedRunStore{store: lease, fence: fence, host: host}
	if withHistory && history.HistoryVersion() == 1 {
		return &fencedHistoryRunStore{HistoryRunStore: history, fenced: view, store: leaseHistory}, nil
	}
	return view, nil
}

// AdmitUnattended admits only a store whose leases outlive its process and an effect host proven to
// enforce the fence against that same store: a reference host built over it, or a host CertifyEffectHost
// certified over it. Anything else refuses by name before a dispatch; in particular the memory and
// callback kinds never qualify.
func AdmitUnattended(store RunStore, host EffectHost) error {
	lease, ok := store.(LeaseRunStore)
	if !ok || lease.LeaseVersion() != LeaseRecordVersion || store.Durability() == RunStoreSingleWriterCallback {
		return &LeaseCapabilityRefused{Reason: "store-unsupported"}
	}
	if lease.LeaseLifetime() != LeaseLifetimeDurable {
		return &LeaseCapabilityRefused{Reason: "store-lifetime"}
	}
	// #253: the host's evidence must be its own, and its authority the run's own store.
	if host == nil || host.EffectFencing() != EffectFencingEnforced || !sameAuthority(provenAuthority(host), store) {
		return &LeaseCapabilityRefused{Reason: "effect-host-unfenced"}
	}
	return nil
}

// UnattendedRunStore is AdmitUnattended, then the fenced view the unattended run uses as its RunStore.
func UnattendedRunStore(store RunStore, fence LeaseFence, host EffectHost) (RunStore, error) {
	if err := AdmitUnattended(store, host); err != nil {
		return nil, err
	}
	return FencedRunStore(store, fence, host)
}

func leaseViewOf(store RunStore) *fencedRunStore {
	switch view := store.(type) {
	case *fencedRunStore:
		return view
	case *fencedHistoryRunStore:
		return view.fenced
	}
	return nil
}

// fencedEffect admits one non-read effect under the fence of the store the writer writes through. #226:
// the admission handed to the effect host is in flight while its dispatch runs (its context carries the
// attempt); a re-authorization refusal minted for it, once, before anything was performed, that the
// dispatch returns, from an effect host proven for the run's store, is trusted and kept on the view
// (ReauthorizationOf).
func fencedEffect(ctx context.Context, store RunStore, op EffectOperation, perform func(context.Context) error) error {
	view := leaseViewOf(store)
	if view == nil {
		return perform(ctx)
	}
	if view.host == nil {
		return &LeaseCapabilityRefused{Reason: "effect-host-unfenced"}
	}
	admission, err := NewEffectAdmission(view.fence, op)
	if err != nil {
		return err
	}
	attempt := &dispatchAttempt{admission: admission, live: true}
	err = view.host.Dispatch(context.WithValue(ctx, dispatchAttemptKey{}, attempt), admission, func(ctx context.Context) error {
		attempt.mu.Lock()
		// #226: a perform after the dispatch ended never runs, and a refusal followed by one is no proof: the
		// host that minted it did not stand by it.
		if !attempt.live {
			minted := attempt.minted
			attempt.invalid = true
			attempt.mu.Unlock()
			if minted != nil {
				minted.revoke()
			}
			return &LeaseRefused{Reason: "invalid"}
		}
		attempt.performed = true
		attempt.mu.Unlock()
		return perform(ctx)
	})
	attempt.mu.Lock()
	attempt.live = false
	minted, refusal, trusted := attempt.minted, attempt.refusal, attempt.minted != nil && !attempt.performed && !attempt.invalid
	attempt.mu.Unlock()
	if returned, ok := err.(*ReauthorizationRequired); ok && trusted && returned == minted && sameAuthority(provenAuthority(view.host), view.store) {
		minted.trust(ReauthDisposition{Version: ReauthRecordVersion, Resource: admission.Fence.Resource, Operation: admission.Operation, IdempotencyKey: admission.IdempotencyKey, Fence: admission.Fence, Cause: refusal.Cause, Connection: refusal.Connection, Principal: refusal.Principal, Permissions: slices.Clone(refusal.Permissions), Requires: slices.Clone(refusal.Requires)})
		view.mu.Lock()
		view.reauthorization = minted
		view.mu.Unlock()
	}
	return err
}
