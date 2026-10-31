package runcontext

// Re-authorization for unattended runs (ADR-0025 decision 4, #226; contract/schemas/run-reauth.json). The
// package validates and records: only a trusted, invocation-bound host disposition (the run's proven
// effect host refused one exact admission at its boundary, never admitting it, because the connection's
// credentials expired or were revoked) parks a run as needs_reauthorization; an outcome that may have been
// admitted keeps its unknown outcome and is reconciled. Re-authorization is never retry permission. The
// host owns credentials, their refresh and the person's action; no credential, token or provider error
// enters a checkpoint, wait, prompt, report or export. The TypeScript twin is
// packages/harness/storage/reauth.ts with the effect-host boundary in packages/harness/storage/lease.ts.
import (
	"bytes"
	"context"
	"encoding/json"
	"regexp"
	"slices"
	"sync"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
)

const ReauthRecordVersion = 1

// ReauthDisposition is the trusted, invocation-bound disposition (run-reauth.json): the exact admission an
// effect host refused, never admitted, and why.
type ReauthDisposition = contract.RunReauth

// ReauthAction is the explicit host or person action a re-authorization resume presents.
type ReauthAction = contract.RunReauthAction

// Reauthorized is what a re-authorization wait's claim records.
type Reauthorized = contract.RunReauthReauthorized

var ReauthRefusalReasons = []string{"invalid", "untrusted", "used", "stale", "unbound", "permission-insufficient"}

// ReauthorizationRefused is a named re-authorization refusal. Nothing was parked, claimed or dispatched.
type ReauthorizationRefused struct{ Reason string }

func (e *ReauthorizationRefused) Error() string { return "ReauthorizationRefused: " + e.Reason + "." }
func (e *ReauthorizationRefused) Unwrap() error {
	f := failure.New("stale-state", "storage", "not-started")
	detail := e.Error()
	f.Detail = &detail
	return failure.NewError(f)
}
func (e *ReauthorizationRefused) Record() contract.RunReauthRefusal {
	return contract.RunReauthRefusal{Version: ReauthRecordVersion, Reason: e.Reason}
}
func reauthRefused(reason string) error { return &ReauthorizationRefused{Reason: reason} }
func reauthInvalid() error              { return reauthRefused("invalid") }

// CredentialRefusal is why an effect host refused an operation at its admission boundary before admitting
// it: the connection's credentials expired or were revoked. A bounded reference to the connection, the
// SHA-256 of the principal identity the credentials acted as, the scope labels they held and the ones the
// operation needs. Never a credential, a token or a provider's error.
type CredentialRefusal struct {
	Cause       string
	Connection  string
	Principal   string
	Permissions []string
	Requires    []string
}

// CredentialCheck is a reference effect host's credential check for one operation, before it admits it:
// nil while the connection's credentials are current, or the refusal. A check that errors admits nothing
// and proves nothing.
type CredentialCheck func(context.Context, EffectOperation) (*CredentialRefusal, error)

var reauthConnectionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$`)
var reauthLabelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)

// ValidScopeLabels: at most 64 labels, each 1 to 128 characters, sorted in UTF-8 byte order without
// repeats.
func ValidScopeLabels(labels []string) bool {
	if labels == nil || len(labels) > 64 {
		return false
	}
	for i, label := range labels {
		if !reauthLabelPattern.MatchString(label) || i > 0 && labels[i-1] >= label {
			return false
		}
	}
	return true
}

// ValidConnectionReference: 1 to 64 characters, a letter or digit then letters, digits, '.', '_', ':' or
// '-'.
func ValidConnectionReference(connection string) bool {
	return reauthConnectionPattern.MatchString(connection)
}
func validCredentialRefusal(r CredentialRefusal) bool {
	return (r.Cause == "expired" || r.Cause == "revoked") && ValidConnectionReference(r.Connection) && leaseDigestPattern.MatchString(r.Principal) && ValidScopeLabels(r.Permissions) && ValidScopeLabels(r.Requires)
}

// ReauthorizationRequired is an effect host's refusal of one admission because the connection's
// credentials expired or were revoked. The operation was not admitted and nothing was performed. Its
// failure is typed (authentication, tool, not-started) and its detail fixed: it carries no credential,
// token or provider error. It is a re-authorization proof only when the package trusts it
// (ReauthorizationOf); otherwise it is an ordinary failure, and the operation's outcome stays unknown.
type ReauthorizationRequired struct {
	mu          sync.Mutex
	disposition *ReauthDisposition
	used        bool
}

func (e *ReauthorizationRequired) Error() string {
	return "ReauthorizationRequired: the connection needs re-authorization; the operation was not admitted."
}
func (e *ReauthorizationRequired) Unwrap() error {
	f := failure.New("authentication", "tool", "not-started")
	detail := e.Error()
	f.Detail = &detail
	return failure.NewError(f)
}

// dispatchAttempt is one admission the package has in flight: the one fencedEffect handed an effect host,
// while its dispatch runs.
type dispatchAttempt struct {
	mu        sync.Mutex
	admission EffectAdmission
	live      bool
	performed bool
	invalid   bool
	minted    *ReauthorizationRequired
	refusal   CredentialRefusal
}
type dispatchAttemptKey struct{}

// NewReauthorizationRequired (#226): an effect host refuses the exact admission it was handed, at its
// admission boundary and before it admits the operation, because the connection's credentials expired or
// were revoked; it then returns what this returns and never performs. ctx is the dispatch's own. The
// refusal is trusted only when it is minted once, within the dispatch the package has in flight for that
// admission, before anything is performed and with a well-formed refusal, and the dispatch then returns
// it, from an effect host proven for the run's store (AdmitUnattended). Another context, another
// admission, a second mint, a mint after the dispatch ended or after the operation was performed, and a
// malformed refusal give an untrusted refusal: an ordinary failure whose operation keeps its unknown
// outcome.
func NewReauthorizationRequired(ctx context.Context, admission EffectAdmission, refusal CredentialRefusal) error {
	minted := &ReauthorizationRequired{}
	attempt, _ := ctx.Value(dispatchAttemptKey{}).(*dispatchAttempt)
	if attempt == nil {
		return minted
	}
	attempt.mu.Lock()
	defer attempt.mu.Unlock()
	if !attempt.live || attempt.performed || attempt.minted != nil || attempt.admission != admission || !validCredentialRefusal(refusal) {
		attempt.invalid = true
		return minted
	}
	attempt.minted = minted
	attempt.refusal = CredentialRefusal{Cause: refusal.Cause, Connection: refusal.Connection, Principal: refusal.Principal, Permissions: slices.Clone(refusal.Permissions), Requires: slices.Clone(refusal.Requires)}
	return minted
}

// trust makes a minted refusal a proof, with its disposition.
func (e *ReauthorizationRequired) trust(disposition ReauthDisposition) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.disposition = &disposition
}

// trustedReauthorization is whether the writer's dispatch ended in a trusted re-authorization refusal: the
// run stops there.
func trustedReauthorization(err error) bool {
	refused, ok := err.(*ReauthorizationRequired)
	if !ok || refused == nil {
		return false
	}
	refused.mu.Lock()
	defer refused.mu.Unlock()
	return refused.disposition != nil
}

// reauthDispositionOf is the trusted disposition of a refusal not yet used by a park, as a copy, and
// whether it was trusted at all.
func reauthDispositionOf(refused *ReauthorizationRequired) (*ReauthDisposition, bool) {
	if refused == nil {
		return nil, false
	}
	refused.mu.Lock()
	defer refused.mu.Unlock()
	if refused.disposition == nil {
		return nil, false
	}
	if refused.used {
		return nil, true
	}
	copied := copyDisposition(*refused.disposition)
	return &copied, true
}
func (e *ReauthorizationRequired) use() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.used = true
}
func copyDisposition(d ReauthDisposition) ReauthDisposition {
	d.Permissions = slices.Clone(d.Permissions)
	d.Requires = slices.Clone(d.Requires)
	return d
}

// ReauthorizationOf (#226) is the trusted re-authorization refusal the run's last non-read dispatch
// through this fenced view ended in, not yet used by a park, or nil. The host hands it to ParkRun; it
// parks one wait.
func ReauthorizationOf(store RunStore) *ReauthorizationRequired {
	view := leaseViewOf(store)
	if view == nil {
		return nil
	}
	view.mu.Lock()
	held := view.reauthorization
	view.mu.Unlock()
	if d, trusted := reauthDispositionOf(held); !trusted || d == nil {
		return nil
	}
	return held
}

// reauthGrant is a resumed re-authorization wait's grant: the one never-admitted dispatch its continuation
// may send again.
type reauthGrant struct {
	operation EffectOperation
	used      bool
}

// attachReauthorizedDispatch attaches a resumed re-authorization wait's grant to the fenced view its
// continuation runs on: the view's fence must be the claim's, and the operation this run's.
func attachReauthorizedDispatch(store RunStore, operation EffectOperation, fence LeaseFence) error {
	view := leaseViewOf(store)
	if view == nil || !validEffectOperation(operation) || view.fence != fence || operation.Run != fence.Resource {
		return leaseInvalid()
	}
	view.mu.Lock()
	defer view.mu.Unlock()
	if view.grant != nil {
		return leaseInvalid()
	}
	view.grant = &reauthGrant{operation: operation}
	return nil
}

// takeReauthorizedDispatch: the writer asks before it sends a dispatch again that its run holds as
// unknown. Only the one operation a resumed re-authorization wait granted on this view, whose recorded
// failure is the typed never-admitted refusal, and only once: anything else stays unknown and is never
// sent again.
func takeReauthorizedDispatch(store RunStore, operation EffectOperation, recorded *contract.Failure) bool {
	view := leaseViewOf(store)
	if view == nil || recorded == nil || recorded.Code != "authentication" || recorded.Source != "tool" || recorded.Outcome != "not-started" {
		return false
	}
	view.mu.Lock()
	defer view.mu.Unlock()
	if view.grant == nil || view.grant.used || view.grant.operation != operation {
		return false
	}
	view.grant.used = true
	return true
}

func reauthMembers(raw json.RawMessage, required []string) (waitMembers, error) {
	m, err := waitShape(raw, required, nil)
	if err != nil {
		return nil, reauthInvalid()
	}
	return m, nil
}
func reauthLabels(m waitMembers, name string) ([]string, error) {
	var labels []string
	if err := json.Unmarshal(m[name], &labels); err != nil || !ValidScopeLabels(labels) {
		return nil, reauthInvalid()
	}
	return labels, nil
}
func reauthString(m waitMembers, name string, valid func(string) bool) (string, error) {
	text, err := m.str(name)
	if err != nil || !valid(text) {
		return "", reauthInvalid()
	}
	return text, nil
}

// ReadReauthDisposition reads a version 1 disposition and holds every rule the schema cannot state: one
// run, the operation's own key.
func ReadReauthDisposition(raw []byte) (ReauthDisposition, error) {
	m, err := reauthMembers(raw, []string{"version", "resource", "operation", "idempotencyKey", "fence", "cause", "connection", "principal", "permissions", "requires"})
	if err != nil {
		return ReauthDisposition{}, err
	}
	if m.version() != nil {
		return ReauthDisposition{}, reauthInvalid()
	}
	d := ReauthDisposition{Version: ReauthRecordVersion}
	if d.Resource, err = reauthString(m, "resource", validStorageID); err != nil {
		return ReauthDisposition{}, err
	}
	op, err := reauthMembers(m["operation"], []string{"run", "operation", "source", "tool", "requestDigest"})
	if err != nil {
		return ReauthDisposition{}, err
	}
	for _, field := range []struct {
		name string
		to   *string
	}{{"run", &d.Operation.Run}, {"operation", &d.Operation.Operation}, {"source", &d.Operation.Source}, {"tool", &d.Operation.Tool}} {
		if *field.to, err = reauthString(op, field.name, validStorageID); err != nil {
			return ReauthDisposition{}, err
		}
	}
	if d.Operation.RequestDigest, err = reauthString(op, "requestDigest", leaseDigestPattern.MatchString); err != nil {
		return ReauthDisposition{}, err
	}
	if d.IdempotencyKey, err = reauthString(m, "idempotencyKey", leaseDigestPattern.MatchString); err != nil {
		return ReauthDisposition{}, err
	}
	if d.Fence, err = ReadLeaseFence(m["fence"]); err != nil {
		return ReauthDisposition{}, reauthInvalid()
	}
	if d.Cause, err = reauthString(m, "cause", func(s string) bool { return s == "expired" || s == "revoked" }); err != nil {
		return ReauthDisposition{}, err
	}
	if d.Connection, err = reauthString(m, "connection", ValidConnectionReference); err != nil {
		return ReauthDisposition{}, err
	}
	if d.Principal, err = reauthString(m, "principal", leaseDigestPattern.MatchString); err != nil {
		return ReauthDisposition{}, err
	}
	if d.Permissions, err = reauthLabels(m, "permissions"); err != nil {
		return ReauthDisposition{}, err
	}
	if d.Requires, err = reauthLabels(m, "requires"); err != nil {
		return ReauthDisposition{}, err
	}
	key, err := EffectIdempotencyKey(d.Operation)
	if err != nil || key != d.IdempotencyKey || d.Operation.Run != d.Resource || d.Fence.Resource != d.Resource {
		return ReauthDisposition{}, reauthInvalid()
	}
	return d, nil
}

// checkReauthDisposition holds a typed disposition to the reader.
func checkReauthDisposition(d ReauthDisposition) (ReauthDisposition, error) {
	raw, err := json.Marshal(d)
	if err != nil {
		return ReauthDisposition{}, reauthInvalid()
	}
	return ReadReauthDisposition(raw)
}

// ReadReauthAction reads a version 1 action record.
func ReadReauthAction(raw []byte) (ReauthAction, error) {
	m, err := reauthMembers(raw, []string{"version", "resource", "wait", "connection", "action", "principal", "permissions", "actor", "at"})
	if err != nil {
		return ReauthAction{}, err
	}
	if m.version() != nil {
		return ReauthAction{}, reauthInvalid()
	}
	a := ReauthAction{Version: ReauthRecordVersion}
	if a.Resource, err = reauthString(m, "resource", validStorageID); err != nil {
		return ReauthAction{}, err
	}
	if a.Wait, err = reauthString(m, "wait", validStorageID); err != nil {
		return ReauthAction{}, err
	}
	if a.Connection, err = reauthString(m, "connection", ValidConnectionReference); err != nil {
		return ReauthAction{}, err
	}
	if a.Action, err = reauthString(m, "action", func(s string) bool { return s == "refresh" || s == "replacement" }); err != nil {
		return ReauthAction{}, err
	}
	if a.Principal, err = reauthString(m, "principal", leaseDigestPattern.MatchString); err != nil {
		return ReauthAction{}, err
	}
	if a.Permissions, err = reauthLabels(m, "permissions"); err != nil {
		return ReauthAction{}, err
	}
	if a.Actor, err = reauthString(m, "actor", validStorageID); err != nil {
		return ReauthAction{}, err
	}
	if a.At, err = m.time("at"); err != nil {
		return ReauthAction{}, reauthInvalid()
	}
	return a, nil
}

// ReadReauthorized reads what a re-authorization wait's claim recorded.
func ReadReauthorized(raw []byte) (Reauthorized, error) {
	m, err := reauthMembers(raw, []string{"actor", "at", "principal", "permissions", "digest"})
	if err != nil {
		return Reauthorized{}, err
	}
	r := Reauthorized{}
	if r.Actor, err = reauthString(m, "actor", validStorageID); err != nil {
		return Reauthorized{}, err
	}
	if r.At, err = m.time("at"); err != nil {
		return Reauthorized{}, reauthInvalid()
	}
	if r.Principal, err = reauthString(m, "principal", leaseDigestPattern.MatchString); err != nil {
		return Reauthorized{}, err
	}
	if r.Permissions, err = reauthLabels(m, "permissions"); err != nil {
		return Reauthorized{}, err
	}
	if r.Digest, err = reauthString(m, "digest", leaseDigestPattern.MatchString); err != nil {
		return Reauthorized{}, err
	}
	return r, nil
}

// checkReauthorized holds a typed record to the reader.
func checkReauthorized(r Reauthorized) (Reauthorized, error) {
	raw, err := json.Marshal(r)
	if err != nil {
		return Reauthorized{}, reauthInvalid()
	}
	return ReadReauthorized(raw)
}

// ReadReauthRefusal reads a named re-authorization refusal.
func ReadReauthRefusal(raw []byte) (contract.RunReauthRefusal, error) {
	m, err := reauthMembers(raw, []string{"version", "reason"})
	if err != nil {
		return contract.RunReauthRefusal{}, err
	}
	reason, err := m.str("reason")
	if m.version() != nil || err != nil || !slices.Contains(ReauthRefusalReasons, reason) {
		return contract.RunReauthRefusal{}, reauthInvalid()
	}
	return contract.RunReauthRefusal{Version: ReauthRecordVersion, Reason: reason}, nil
}

// ReauthDispositionDigest is the SHA-256 of a disposition's canonical JSON.
func ReauthDispositionDigest(d ReauthDisposition) (string, error) {
	held, err := checkReauthDisposition(d)
	if err != nil {
		return "", err
	}
	return waitCanonicalDigest(held), nil
}

// ReauthActionDigest is the SHA-256 of an action record's canonical JSON.
func ReauthActionDigest(a ReauthAction) (string, error) {
	raw, err := json.Marshal(a)
	if err != nil {
		return "", reauthInvalid()
	}
	held, err := ReadReauthAction(raw)
	if err != nil {
		return "", err
	}
	return waitCanonicalDigest(held), nil
}

// permitted: every label requires names is one permissions holds.
func permitted(requires, permissions []string) bool {
	for _, label := range requires {
		if !slices.Contains(permissions, label) {
			return false
		}
	}
	return true
}

// ReauthWait is what re-authorization reads of a wait: its run, identity, park time, pending connection
// and trusted disposition.
type ReauthWait struct {
	Resource        string
	Wait            string
	ParkedAt        string
	Connection      string
	Reauthorization *ReauthDisposition
}

// Reauthorize (#226) checks the host's or the person's explicit action against a woken re-authorization
// wait, before anything of its continuation is claimed. The action is this wait's and this connection's;
// a replacement, or a refresh whose principal is not the one the credentials acted as, is a changed
// identity (ResumeRefused identity-changed: a new admitted run is needed, ADR-0019), never relabelled as a
// refresh; an action from before the park is stale; current permissions that do not cover what the
// operation requires make it inadmissible. It returns what the claim records. The store checks the
// principal, the permissions and the action's time against its own clock again in the claim's transaction.
func Reauthorize(wait ReauthWait, input ReauthAction) (Reauthorized, error) {
	raw, err := json.Marshal(input)
	if err != nil {
		return Reauthorized{}, reauthInvalid()
	}
	action, err := ReadReauthAction(raw)
	if err != nil {
		return Reauthorized{}, err
	}
	d := wait.Reauthorization
	if d == nil || action.Resource != wait.Resource || action.Wait != wait.Wait || action.Connection != d.Connection || action.Connection != wait.Connection {
		return Reauthorized{}, reauthInvalid()
	}
	if action.Action != "refresh" || action.Principal != d.Principal {
		return Reauthorized{}, resumeRefused("identity-changed")
	}
	if waitTimeMillis(action.At) < waitTimeMillis(wait.ParkedAt) {
		return Reauthorized{}, reauthRefused("stale")
	}
	if !permitted(d.Requires, action.Permissions) {
		return Reauthorized{}, reauthRefused("permission-insufficient")
	}
	digest, err := ReauthActionDigest(action)
	if err != nil {
		return Reauthorized{}, err
	}
	return Reauthorized{Actor: action.Actor, At: action.At, Principal: action.Principal, Permissions: slices.Clone(action.Permissions), Digest: digest}, nil
}

// HoldsNeverAdmitted (#226): the run's head holds the operation as the never-admitted dispatch the
// disposition names: its last dispatch, a host or Runtime effect whose outcome is unknown and whose
// recorded failure is the typed refusal (authentication, tool, not-started), with the disposition's source,
// tool and request digest. Nothing was dispatched after it.
func HoldsNeverAdmitted(checkpoint []byte, d ReauthDisposition) bool {
	// Exact member names: Go's struct decoding would match them case-insensitively.
	object := func(raw json.RawMessage) map[string]json.RawMessage {
		var m map[string]json.RawMessage
		if json.Unmarshal(raw, &m) != nil {
			return nil
		}
		return m
	}
	is := func(m map[string]json.RawMessage, name, want string) bool {
		var text string
		return m != nil && json.Unmarshal(m[name], &text) == nil && m[name] != nil && text == want
	}
	head := object(checkpoint)
	durable := object(head["durable"])
	var rows []json.RawMessage
	if durable == nil || json.Unmarshal(durable["dispatches"], &rows) != nil || len(rows) == 0 {
		return false
	}
	row := object(rows[len(rows)-1])
	failed := object(row["failure"])
	op := d.Operation
	return is(row, "id", op.Operation) && (is(row, "kind", "host") || is(row, "kind", "runtime")) && string(bytes.TrimSpace(row["read"])) == "false" && is(row, "state", "unknown") && is(row, "source", op.Source) && is(row, "tool", op.Tool) && is(row, "requestDigest", op.RequestDigest) && is(failed, "code", "authentication") && is(failed, "source", "tool") && is(failed, "outcome", "not-started")
}

// ReauthorizedRunStore (#226) is the fenced view a resumed reauthorization wait's continuation runs on:
// unattended admission of the store and effect host, the claim's fence, and the one grant the claim gives:
// the never-admitted operation may be dispatched again, under its own identity, once on this view, if the
// run asks for that exact request; nothing else that the run holds as unknown is. The store's own records
// decide it: the wait must be a reauthorization wait resumed under this fence with its refresh recorded,
// and the fence the run's held lease. The head's compare-and-set and the effect host's idempotency
// identity keep a second view from sending it twice.
func ReauthorizedRunStore(ctx context.Context, store RunStore, host EffectHost, resumed ResumedWait) (RunStore, error) {
	held, err := waitStoreOf(store)
	if err != nil {
		return nil, err
	}
	fence := resumed.Fence
	if !validLeaseFence(fence) {
		return nil, leaseInvalid()
	}
	record, err := held.ReadWait(ctx, fence.Resource, resumed.Wait.Wait)
	if err != nil {
		return nil, err
	}
	if record == nil {
		return nil, waitRefused("no-wait")
	}
	wait, err := readStoreWait(*record)
	if err != nil || wait.Reason != "reauthorization" || wait.State != "resumed" || wait.Reauthorized == nil || wait.Resource != fence.Resource || wait.Resumed == nil || wait.Resumed.Owner != fence.Owner || wait.Resumed.Epoch != fence.Epoch {
		return nil, waitInvalid()
	}
	lease, err := held.ReadLease(ctx, fence.Resource)
	if err != nil {
		return nil, err
	}
	if lease == nil || lease.Owner != fence.Owner || lease.Epoch != fence.Epoch || lease.State != "held" {
		return nil, &LeaseRefused{Reason: "stale-epoch"}
	}
	view, err := UnattendedRunStore(store, fence, host)
	if err != nil {
		return nil, err
	}
	if err := attachReauthorizedDispatch(view, wait.Reauthorization.Operation, fence); err != nil {
		return nil, err
	}
	return view, nil
}
