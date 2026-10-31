package runcontext

// Agent principals and delegated authority (agent #260, ADR-0029; spec RFC 0030, a Draft): the port
// of packages/harness/principal.ts.
//
// A delegated run carries the chain of delegation statements its issuing host supplied when it
// started the run, and the package refuses, before dispatch and with a named reason, every call of a
// tool with a source that the chain does not admit. This file is the part that depends only on the
// bytes, the pinned keys and the host's answers: RFC 0030's steps 1 to 7 as the reference gateway's
// SPEC.md §11.1 reads them (VerifyPrincipalChain), the run's principal and its record member
// (OpenPrincipal), and one admission at the host's time with the host admission port's answer read
// afresh (AdmitPrincipalCall). The model never chooses, sees the bytes of, or changes the chain.

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"slices"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

// PrincipalDelegationContext is the signing prefix of a version 1 statement.
const PrincipalDelegationContext = "judgment-pack/delegation/1:"

// The bounds of RFC 0030's open questions 3 and 21, as the reference gateway enforces them.
const (
	PrincipalMaxDepth       = 4
	PrincipalMaxLifetime    = 24 * time.Hour
	PrincipalStatementBytes = 65536
	principalMaxEntries     = 256
	principalMaxTools       = 256
	principalMaxLimits      = 16
)

// PrincipalAdmissionTimeout is how long the host's admission port may take before the call is
// refused as admission-unknown.
const PrincipalAdmissionTimeout = 5 * time.Second

// PrincipalEffects are ADR-0027's effect classes in strength order.
var PrincipalEffects = []string{"pure", "read", "read-external", "propose", "act"}

// PrincipalScopeReasons are step 11's reasons, in the order they are judged.
var PrincipalScopeReasons = []string{"source", "tool", "effect", "limit-unknown", "limit"}

// PrincipalRefusal is a named refusal (run-principal.json#/$defs/refusal); Reason is set exactly for
// out-of-scope.
type PrincipalRefusal struct {
	Name   string
	Reason string
}

// Value is the refusal as the records carry it.
func (r PrincipalRefusal) Value() *Value {
	out := ObjectOf("name", String(r.Name))
	if r.Reason != "" {
		out.Set("reason", String(r.Reason))
	}
	return out
}

// DelegationRefused is a chain or a principal the package refuses, by name. Its message repeats no
// statement and no key.
type DelegationRefused struct {
	Refusal PrincipalRefusal
	Detail  string
}

func (e *DelegationRefused) Error() string {
	text := "Delegation refused: " + e.Refusal.Name
	if e.Refusal.Reason != "" {
		text += " (" + e.Refusal.Reason + ")"
	}
	if e.Detail != "" {
		text += ": " + e.Detail
	}
	return text + "."
}

// ErrPrincipalInputInvalid is a Input.Principal the package cannot read at all.
var ErrPrincipalInputInvalid = errors.New("The run's principal is invalid.")

func principalRefuse(name, format string, args ...any) error {
	return &DelegationRefused{Refusal: PrincipalRefusal{Name: name}, Detail: fmt.Sprintf(format, args...)}
}

// PrincipalReference names a person or a service.
type PrincipalReference struct {
	Kind, Issuer, Subject string
}

// Value is the reference as the records carry it.
func (p PrincipalReference) Value() *Value {
	return ObjectOf("kind", String(p.Kind), "issuer", String(p.Issuer), "subject", String(p.Subject))
}

// PrincipalSoftware is a software identity: Package and Version, or Harness.
type PrincipalSoftware struct {
	Package, Version, Harness string
}

// Value is the identity as the records carry it.
func (s PrincipalSoftware) Value() *Value {
	if s.Harness != "" {
		return ObjectOf("harness", String(s.Harness))
	}
	return ObjectOf("package", String(s.Package), "version", String(s.Version))
}

// PrincipalAudience is the enforcing host a scope entry answers to: gateway or agent-host.
type PrincipalAudience struct {
	Kind, Name string
}

// PrincipalRecord is the record member every delegated record carries (run-principal.json).
type PrincipalRecord struct {
	Agent, Host string
	// RootActor is the root link's agent: with the subject, what a resume and a takeover must keep.
	RootActor      string
	Software       PrincipalSoftware
	Subject        PrincipalReference
	DelegationID   string
	DelegationHash string
	Chain          []string
}

// Value is the member as the records carry it.
func (r PrincipalRecord) Value() *Value {
	chain := Array()
	for _, digest := range r.Chain {
		chain.Append(String(digest))
	}
	return ObjectOf(
		"actor", ObjectOf("agent", String(r.Agent), "host", String(r.Host), "software", r.Software.Value()),
		"rootActor", String(r.RootActor),
		"subject", r.Subject.Value(),
		"delegation", ObjectOf("id", String(r.DelegationID), "digest", String(r.DelegationHash)),
		"chain", chain,
	)
}

// PrincipalAdmissionCall is the call an admission read names.
type PrincipalAdmissionCall struct {
	Source, Tool, Effect string
	Count                bool
}

// PrincipalAdmissionRead is what the host's admission port is handed
// (run-principal.json#/$defs/admissionRead).
type PrincipalAdmissionRead struct {
	Host     string
	Audience PrincipalAudience
	Chain    []string
	Call     PrincipalAdmissionCall
	At       string
}

// Value is the read as JSON, its members in canonical order.
func (r PrincipalAdmissionRead) Value() *Value {
	chain := Array()
	for _, digest := range r.Chain {
		chain.Append(String(digest))
	}
	return ObjectOf(
		"at", String(r.At),
		"audience", ObjectOf("kind", String(r.Audience.Kind), "name", String(r.Audience.Name)),
		"call", ObjectOf("count", Bool(r.Call.Count), "effect", String(r.Call.Effect), "source", String(r.Call.Source), "tool", String(r.Call.Tool)),
		"chain", chain,
		"host", String(r.Host),
		"readVersion", String("1"),
	)
}

// PrincipalAdmissionPort answers one admission read from the issuing host's state, read afresh:
// its answer is the JSON of {"admission": "admit"}, {"admission": "refuse", "refusal": R} or
// {"admission": "refuse", "refusal": "out-of-scope", "reason": X}. An error, another form, or no
// answer within PrincipalAdmissionTimeout refuses the call as admission-unknown.
type PrincipalAdmissionPort func(ctx context.Context, read PrincipalAdmissionRead) ([]byte, error)

// RunPrincipal is what the host supplies for a delegated run (Input.Principal): the chain's JSON
// text, root first; the issuing hosts' pinned Ed25519 public keys (64 lowercase hexadecimal
// characters) by host; the software the host states the run runs; the flat token this host
// declares for the host sources it serves; the admission port; and the host's authoritative time
// (the system clock where nil).
type RunPrincipal struct {
	Chain     []byte
	Keys      map[string][]string
	Software  PrincipalSoftware
	Audience  string
	Admission PrincipalAdmissionPort
	Clock     func() (time.Time, error)
}

var (
	principalOpaque = regexp.MustCompile(`^[0-9a-f]{32}$`)
	principalDigest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	principalTime   = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}\.[0-9]{3}Z$`)
	principalFlat   = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
	principalLimit  = regexp.MustCompile(`^x-[A-Za-z0-9._-]{1,128}$`)
	principalSig    = regexp.MustCompile(`^[0-9a-f]{128}$`)
	principalKey    = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

const principalTimeLayout = "2006-01-02T15:04:05.000Z"

// PrincipalFlatToken is [A-Za-z0-9._-]{1,128}, never . or ..
func PrincipalFlatToken(s string) bool { return principalFlat.MatchString(s) && s != "." && s != ".." }

func principalGatewayName(s string) bool {
	if len(s) == 0 || len(s) > 256 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x21 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

func principalLimitName(s string) bool {
	return s == "calls" || principalLimit.MatchString(s) && s != "x-." && s != "x-.."
}

func principalParseTime(v any) (time.Time, bool) {
	s, ok := v.(string)
	if !ok || !principalTime.MatchString(s) {
		return time.Time{}, false
	}
	t, err := time.Parse(principalTimeLayout, s)
	if err != nil || t.Format(principalTimeLayout) != s {
		return time.Time{}, false
	}
	return t, true
}

// PrincipalTime is an instant in the record form, to the millisecond.
func PrincipalTime(t time.Time) string {
	return t.UTC().Truncate(time.Millisecond).Format(principalTimeLayout)
}

// The gateway's §8.4 key rule: canonical, not of small order, on the curve.
var (
	principalP    = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 255), big.NewInt(19))
	principalL, _ = new(big.Int).SetString("7237005577332262213973186563042994240857116359379907606001950938285454250989", 10)
	principalD    = func() *big.Int {
		inv := new(big.Int).ModInverse(big.NewInt(121666), principalP)
		d := new(big.Int).Mul(new(big.Int).Sub(principalP, big.NewInt(121665)), inv)
		return d.Mod(d, principalP)
	}()
	principalSmallOrder = []string{
		"0100000000000000000000000000000000000000000000000000000000000000",
		"ecffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f",
		"0000000000000000000000000000000000000000000000000000000000000000",
		"0000000000000000000000000000000000000000000000000000000000000080",
		"26e8958fc2b227b045c3f489f2ef98f0d5dfac05d3c63339b13802886d53fc05",
		"26e8958fc2b227b045c3f489f2ef98f0d5dfac05d3c63339b13802886d53fc85",
		"c7176a703d4dd84fba3c0b760d10670f2a2053fa2c39ccc64ec7fd7792ac037a",
		"c7176a703d4dd84fba3c0b760d10670f2a2053fa2c39ccc64ec7fd7792ac03fa",
	}
)

func principalLittleEndian(b []byte) *big.Int {
	reversed := make([]byte, len(b))
	for i := range b {
		reversed[len(b)-1-i] = b[i]
	}
	return new(big.Int).SetBytes(reversed)
}

// PrincipalKeyProblem says why 64 hexadecimal characters are not a key a reader may pin, or "".
func PrincipalKeyProblem(key string) string {
	if !principalKey.MatchString(key) {
		return "key-malformed"
	}
	b, _ := hex.DecodeString(key)
	sign := b[31]&0x80 != 0
	low := append([]byte(nil), b...)
	low[31] &= 0x7f
	y := principalLittleEndian(low)
	pMinus1 := new(big.Int).Sub(principalP, big.NewInt(1))
	if y.Cmp(principalP) >= 0 || sign && (y.Cmp(big.NewInt(1)) == 0 || y.Cmp(pMinus1) == 0) {
		return "key-not-canonical"
	}
	if slices.Contains(principalSmallOrder, key) {
		return "key-small-order"
	}
	y2 := new(big.Int).Mul(y, y)
	u := new(big.Int).Sub(y2, big.NewInt(1))
	u.Mod(u, principalP)
	v := new(big.Int).Mul(principalD, y2)
	v.Add(v, big.NewInt(1))
	v.Mod(v, principalP)
	w := new(big.Int).Mul(u, new(big.Int).ModInverse(v, principalP))
	w.Mod(w, principalP)
	if w.Sign() != 0 {
		half := new(big.Int).Rsh(pMinus1, 1)
		if new(big.Int).Exp(w, half, principalP).Cmp(big.NewInt(1)) != 0 {
			return "key-not-on-curve"
		}
	}
	return ""
}

// PrincipalKeyID is a key's identifier: the first 32 characters of the hex SHA-256 of its bytes.
func PrincipalKeyID(key ed25519.PublicKey) string {
	sum := sha256.Sum256(key)
	return hex.EncodeToString(sum[:])[:32]
}

// PrincipalPinnedKeys are the pinned keys, by host, by key identifier.
type PrincipalPinnedKeys map[string]map[string]ed25519.PublicKey

// PinPrincipalKeys reads each host's keys, holding every key to the key rule; a key that fails it
// is refused, never pinned.
func PinPrincipalKeys(keys map[string][]string) (PrincipalPinnedKeys, error) {
	if keys == nil {
		return nil, fmt.Errorf("%w keys names no host", ErrPrincipalInputInvalid)
	}
	out := PrincipalPinnedKeys{}
	for host, list := range keys {
		if !PrincipalFlatToken(host) || len(list) == 0 || len(list) > 16 {
			return nil, fmt.Errorf("%w keys names a host that is not a flat token, or a host with no keys", ErrPrincipalInputInvalid)
		}
		held := map[string]ed25519.PublicKey{}
		for _, key := range list {
			if problem := PrincipalKeyProblem(key); problem != "" {
				return nil, fmt.Errorf("%w a pinned key is refused (%s)", ErrPrincipalInputInvalid, problem)
			}
			b, _ := hex.DecodeString(key)
			held[PrincipalKeyID(b)] = ed25519.PublicKey(b)
		}
		out[host] = held
	}
	return out, nil
}

func principalVerifies(key ed25519.PublicKey, signature string, message []byte) bool {
	b, err := hex.DecodeString(signature)
	if err != nil || len(b) != ed25519.SignatureSize {
		return false
	}
	if principalLittleEndian(b[32:]).Cmp(principalL) >= 0 {
		return false
	}
	return ed25519.Verify(key, message, b)
}

type principalEntry struct {
	audience PrincipalAudience
	source   string
	tools    []string
	effects  []string
	limits   map[string]int64
}

// PrincipalLink is one link as verified.
type PrincipalLink struct {
	value       map[string]any
	ID, Host    string
	KeyID       string
	Sub         PrincipalReference
	Agent       string
	Software    PrincipalSoftware
	entries     []principalEntry
	MayDelegate bool
	IssuedAt    time.Time
	ExpiresAt   time.Time
	ParentID    string
	ParentHash  string
	Depth       int64
	Signature   string
	Digest      string
}

// PrincipalChain is a chain that passed steps 1 to 7, root first.
type PrincipalChain struct {
	Links   []*PrincipalLink
	Digests []string
}

func principalOnly(v any, members ...string) (map[string]any, bool) {
	obj, ok := v.(map[string]any)
	if !ok || len(obj) != len(members) {
		return nil, false
	}
	for _, m := range members {
		if _, present := obj[m]; !present {
			return nil, false
		}
	}
	return obj, true
}

func principalReference(v any) (PrincipalReference, bool) {
	obj, ok := principalOnly(v, "kind", "issuer", "subject")
	if !ok {
		return PrincipalReference{}, false
	}
	kind, _ := obj["kind"].(string)
	issuer, ok1 := obj["issuer"].(string)
	subject, ok2 := obj["subject"].(string)
	if (kind != "person" && kind != "service") || !ok1 || !ok2 || issuer == "" || subject == "" {
		return PrincipalReference{}, false
	}
	return PrincipalReference{kind, issuer, subject}, true
}

func principalSoftware(v any) (PrincipalSoftware, bool) {
	if obj, ok := principalOnly(v, "package", "version"); ok {
		p, ok1 := obj["package"].(string)
		ver, ok2 := obj["version"].(string)
		return PrincipalSoftware{Package: p, Version: ver}, ok1 && ok2 && p != "" && ver != ""
	}
	if obj, ok := principalOnly(v, "harness"); ok {
		h, ok := obj["harness"].(string)
		return PrincipalSoftware{Harness: h}, ok && principalDigest.MatchString(h)
	}
	return PrincipalSoftware{}, false
}

func principalSorted(v any, max int) ([]string, bool) {
	arr, ok := v.([]any)
	if !ok || len(arr) == 0 || len(arr) > max {
		return nil, false
	}
	out := make([]string, 0, len(arr))
	for i, item := range arr {
		s, ok := item.(string)
		if !ok || i > 0 && out[i-1] >= s {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

func principalStrength(effect string) int { return slices.Index(PrincipalEffects, effect) }

func principalParseEntry(v any) (principalEntry, bool) {
	obj, ok := principalOnly(v, "audience", "source", "tools", "effects", "limits")
	if !ok {
		return principalEntry{}, false
	}
	var e principalEntry
	audience, ok := principalOnly(obj["audience"], "kind", "name")
	if !ok {
		return principalEntry{}, false
	}
	e.audience.Kind, _ = audience["kind"].(string)
	e.audience.Name, _ = audience["name"].(string)
	switch {
	case e.audience.Kind == "gateway" && principalGatewayName(e.audience.Name):
	case e.audience.Kind == "agent-host" && PrincipalFlatToken(e.audience.Name):
	default:
		return principalEntry{}, false
	}
	if e.source, ok = obj["source"].(string); !ok || e.source == "" {
		return principalEntry{}, false
	}
	if e.tools, ok = principalSorted(obj["tools"], principalMaxTools); !ok {
		return principalEntry{}, false
	}
	effects, ok := obj["effects"].([]any)
	if !ok || len(effects) == 0 {
		return principalEntry{}, false
	}
	for i, item := range effects {
		s, ok := item.(string)
		strength := principalStrength(s)
		if !ok || strength < 0 || i > 0 && strength <= principalStrength(e.effects[i-1]) {
			return principalEntry{}, false
		}
		e.effects = append(e.effects, s)
	}
	limits, ok := obj["limits"].(map[string]any)
	if !ok || len(limits) > principalMaxLimits {
		return principalEntry{}, false
	}
	e.limits = map[string]int64{}
	for name, bound := range limits {
		n, isInt := bound.(int64)
		if !principalLimitName(name) || !isInt || n < 0 {
			return principalEntry{}, false
		}
		e.limits[name] = n
	}
	return e, true
}

func principalEntryLess(a, b principalEntry) bool {
	if a.audience.Kind != b.audience.Kind {
		return a.audience.Kind < b.audience.Kind
	}
	if a.audience.Name != b.audience.Name {
		return a.audience.Name < b.audience.Name
	}
	return a.source < b.source
}

var principalMembers = []string{"delegationVersion", "id", "host", "keyId", "sub", "act", "scope", "mayDelegate", "issuedAt", "expiresAt", "parent", "depth", "signature"}

// principalParseLink holds one link to version 1's structure (step 3).
func principalParseLink(obj map[string]any) (*PrincipalLink, string) {
	for _, m := range principalMembers {
		if _, present := obj[m]; !present {
			return nil, "member " + m + " is missing"
		}
	}
	l := &PrincipalLink{value: obj}
	var ok bool
	if l.ID, ok = obj["id"].(string); !ok || !principalOpaque.MatchString(l.ID) {
		return nil, "id is not an opaque identifier"
	}
	if l.Host, ok = obj["host"].(string); !ok || !PrincipalFlatToken(l.Host) {
		return nil, "host is not a flat token"
	}
	if l.KeyID, ok = obj["keyId"].(string); !ok || !principalOpaque.MatchString(l.KeyID) {
		return nil, "keyId is not a key identifier"
	}
	if l.Sub, ok = principalReference(obj["sub"]); !ok {
		return nil, "sub is not a principal reference"
	}
	act, ok := principalOnly(obj["act"], "agent", "software")
	if !ok {
		return nil, "act is not {agent, software}"
	}
	if l.Agent, ok = act["agent"].(string); !ok || !principalOpaque.MatchString(l.Agent) {
		return nil, "act.agent is not an opaque identifier"
	}
	if l.Software, ok = principalSoftware(act["software"]); !ok {
		return nil, "act.software is not a software identity"
	}
	scope, ok := principalOnly(obj["scope"], "sources")
	if !ok {
		return nil, "scope is not {sources}"
	}
	sources, ok := scope["sources"].([]any)
	if !ok || len(sources) == 0 || len(sources) > principalMaxEntries {
		return nil, "scope.sources is not an array of 1 to 256 entries"
	}
	for i, sv := range sources {
		e, ok := principalParseEntry(sv)
		if !ok {
			return nil, fmt.Sprintf("scope.sources[%d] is not a scope entry", i)
		}
		if i > 0 && !principalEntryLess(l.entries[i-1], e) {
			return nil, "scope.sources is out of order or repeats an audience and source"
		}
		l.entries = append(l.entries, e)
	}
	if l.MayDelegate, ok = obj["mayDelegate"].(bool); !ok {
		return nil, "mayDelegate is not a boolean"
	}
	if l.IssuedAt, ok = principalParseTime(obj["issuedAt"]); !ok {
		return nil, "issuedAt is not a time"
	}
	if l.ExpiresAt, ok = principalParseTime(obj["expiresAt"]); !ok {
		return nil, "expiresAt is not a time"
	}
	if !l.ExpiresAt.After(l.IssuedAt) {
		return nil, "expiresAt is not after issuedAt"
	}
	if l.ExpiresAt.Sub(l.IssuedAt) > PrincipalMaxLifetime {
		return nil, "the link lives more than 24 hours"
	}
	if l.Depth, ok = obj["depth"].(int64); !ok || l.Depth < 0 {
		return nil, "depth is not a non-negative integer"
	}
	if obj["parent"] == nil {
		if l.Depth != 0 {
			return nil, "a link with no parent has a depth other than 0"
		}
	} else {
		parent, ok := principalOnly(obj["parent"], "id", "digest")
		if !ok {
			return nil, "parent is neither null nor {id, digest}"
		}
		l.ParentID, _ = parent["id"].(string)
		l.ParentHash, _ = parent["digest"].(string)
		if !principalOpaque.MatchString(l.ParentID) || !principalDigest.MatchString(l.ParentHash) {
			return nil, "parent is not of its form"
		}
		if l.Depth == 0 {
			return nil, "a link with a parent has a depth of 0"
		}
	}
	if l.Signature, ok = obj["signature"].(string); !ok || !principalSig.MatchString(l.Signature) {
		return nil, "signature is not 128 lowercase hexadecimal characters"
	}
	canonical := descriptorCanonical(obj)
	if len(canonical) > PrincipalStatementBytes {
		return nil, "the link is over 65536 bytes"
	}
	sum := sha256.Sum256([]byte(canonical))
	l.Digest = "sha256:" + hex.EncodeToString(sum[:])
	return l, ""
}

func (l *PrincipalLink) signingInput() []byte {
	covered := make(map[string]any, len(l.value))
	for name, v := range l.value {
		if name != "signature" {
			covered[name] = v
		}
	}
	return []byte(PrincipalDelegationContext + descriptorCanonical(covered))
}

// PrincipalWidening is clauses 4 to 8 for a child and its parent: why the child is wider, or "".
func PrincipalWidening(c, p *PrincipalLink) string {
	if c.Sub != p.Sub {
		return "changes the subject"
	}
	if c.IssuedAt.Before(p.IssuedAt) || c.ExpiresAt.After(p.ExpiresAt) {
		return "is valid outside its parent's validity"
	}
	for _, e := range c.entries {
		var parent *principalEntry
		for i := range p.entries {
			pe := &p.entries[i]
			if pe.audience == e.audience && pe.source == e.source {
				parent = pe
				break
			}
		}
		if parent == nil {
			return "admits a source its parent does not"
		}
		for _, t := range e.tools {
			if !slices.Contains(parent.tools, t) {
				return "admits a tool its parent does not"
			}
		}
		for _, effect := range e.effects {
			if !slices.Contains(parent.effects, effect) {
				return "admits an effect its parent does not"
			}
		}
		for name, bound := range parent.limits {
			mine, present := e.limits[name]
			if !present || mine > bound {
				return "drops or raises a limit of its parent"
			}
		}
	}
	if !p.MayDelegate {
		return "is below a link that permits no child"
	}
	return ""
}

// VerifyPrincipalChain applies steps 1 to 7 to a chain's JSON text, in order, stopping at the first
// failure; within a step the links are taken root first. Its error is a *DelegationRefused.
func VerifyPrincipalChain(text []byte, keys PrincipalPinnedKeys) (*PrincipalChain, error) {
	// Step 1: the strict reader refuses a duplicate member and a value outside the canonical domain.
	value, err := descriptorJSON(text, true)
	if err != nil {
		return nil, principalRefuse("malformed", "the chain is not canonical-domain JSON")
	}
	arr, ok := value.([]any)
	if !ok || len(arr) == 0 || len(arr) > PrincipalMaxDepth+1 {
		return nil, principalRefuse("malformed", "a chain is an array of one to five statements")
	}
	objects := make([]map[string]any, 0, len(arr))
	for i, item := range arr {
		obj, ok := item.(map[string]any)
		if !ok {
			return nil, principalRefuse("malformed", "link %d is not an object", i)
		}
		if _, ok := obj["delegationVersion"].(string); !ok {
			return nil, principalRefuse("malformed", "link %d has no string delegationVersion", i)
		}
		objects = append(objects, obj)
	}
	// Step 2.
	for i, obj := range objects {
		if obj["delegationVersion"] != "1" {
			return nil, principalRefuse("unsupported-version", "link %d is of a delegation version this package does not read", i)
		}
	}
	// Step 3.
	chain := &PrincipalChain{}
	for i, obj := range objects {
		l, why := principalParseLink(obj)
		if l == nil {
			return nil, principalRefuse("malformed", "link %d: %s", i, why)
		}
		chain.Links = append(chain.Links, l)
		chain.Digests = append(chain.Digests, l.Digest)
	}
	// Step 4.
	for i, l := range chain.Links {
		if _, pinned := keys[l.Host][l.KeyID]; !pinned {
			return nil, principalRefuse("key-mismatch", "link %d is signed by a key not pinned for its host", i)
		}
	}
	// Step 5.
	for i, l := range chain.Links {
		if !principalVerifies(keys[l.Host][l.KeyID], l.Signature, l.signingInput()) {
			return nil, principalRefuse("signature-mismatch", "link %d's signature does not verify", i)
		}
	}
	// Step 6: clauses 0 to 3.
	if root := chain.Links[0]; root.ParentID != "" || root.Depth != 0 {
		return nil, principalRefuse("chain-broken", "the chain does not start at a root")
	}
	for k := 1; k < len(chain.Links); k++ {
		c, p := chain.Links[k], chain.Links[k-1]
		switch {
		case c.ParentID != p.ID || c.ParentHash != p.Digest:
			return nil, principalRefuse("chain-broken", "link %d does not name link %d as its parent", k, k-1)
		case c.Depth != p.Depth+1 || c.Depth > PrincipalMaxDepth:
			return nil, principalRefuse("chain-broken", "link %d's depth is not its parent's plus one, within the maximum", k)
		case c.Host != p.Host:
			return nil, principalRefuse("chain-broken", "link %d names another host than its parent", k)
		}
	}
	// Step 7: clauses 4 to 8.
	for k := 1; k < len(chain.Links); k++ {
		if why := PrincipalWidening(chain.Links[k], chain.Links[k-1]); why != "" {
			return nil, principalRefuse("widened", "link %d %s", k, why)
		}
	}
	return chain, nil
}

// Record is the record member of a verified chain.
func (c *PrincipalChain) Record() PrincipalRecord {
	leaf := c.Links[len(c.Links)-1]
	return PrincipalRecord{Agent: leaf.Agent, Host: leaf.Host, RootActor: c.Links[0].Agent, Software: leaf.Software, Subject: leaf.Sub, DelegationID: leaf.ID, DelegationHash: leaf.Digest, Chain: append([]string(nil), c.Digests...)}
}

// OpenedPrincipal is a delegated run's principal, opened.
type OpenedPrincipal struct {
	Chain    *PrincipalChain
	Record   PrincipalRecord
	Audience string
	Keys     PrincipalPinnedKeys
	Software PrincipalSoftware
	port     PrincipalAdmissionPort
	clock    func() (time.Time, error)
	// Text is the chain's JSON text as the issuing host supplied it.
	Text []byte
}

// OpenPrincipal opens the host's Input.Principal: the input's form (ErrPrincipalInputInvalid), the
// pinned keys held to the key rule, steps 1 to 7, and the leaf's software against the software the
// host states the run runs (software-mismatch). A refusal is a *DelegationRefused.
func OpenPrincipal(input *RunPrincipal) (*OpenedPrincipal, error) {
	if input == nil {
		return nil, fmt.Errorf("%w it is absent", ErrPrincipalInputInvalid)
	}
	if !PrincipalFlatToken(input.Audience) {
		return nil, fmt.Errorf("%w audience is not a flat token", ErrPrincipalInputInvalid)
	}
	if input.Admission == nil {
		return nil, fmt.Errorf("%w admission is absent", ErrPrincipalInputInvalid)
	}
	software := input.Software
	if (software.Harness == "") == (software.Package == "" && software.Version == "") || software.Harness != "" && !principalDigest.MatchString(software.Harness) || software.Harness == "" && (software.Package == "" || software.Version == "") {
		return nil, fmt.Errorf("%w software is neither {package, version} nor {harness}", ErrPrincipalInputInvalid)
	}
	keys, err := PinPrincipalKeys(input.Keys)
	if err != nil {
		return nil, err
	}
	chain, err := VerifyPrincipalChain(input.Chain, keys)
	if err != nil {
		return nil, err
	}
	if chain.Links[len(chain.Links)-1].Software != software {
		return nil, principalRefuse("software-mismatch", "the leaf's software is not the software the host states the run runs")
	}
	clock := input.Clock
	if clock == nil {
		clock = func() (time.Time, error) { return time.Now(), nil }
	}
	return &OpenedPrincipal{Chain: chain, Record: chain.Record(), Audience: input.Audience, Keys: keys, Software: software, port: input.Admission, clock: clock, Text: append([]byte(nil), input.Chain...)}, nil
}

// PrincipalCall is the call the package admits: the audience its source answers to (nil for a
// gateway source with no mapping), its source, tool and owner-declared effect ("" where none).
type PrincipalCall struct {
	Audience *PrincipalAudience
	Source   string
	Tool     string
	Effect   string
}

// PrincipalScopeReason is the package's own part of step 11 over every link: the first failing
// reason, within a link before the next, or "".
func (c *PrincipalChain) PrincipalScopeReason(call PrincipalCall) string {
	for _, l := range c.Links {
		var entry *principalEntry
		if call.Audience != nil {
			for i := range l.entries {
				if e := &l.entries[i]; e.audience == *call.Audience && e.source == call.Source {
					entry = e
					break
				}
			}
		}
		switch {
		case entry == nil:
			return "source"
		case call.Tool == "" || !slices.Contains(entry.tools, call.Tool):
			return "tool"
		case call.Effect == "" || !slices.Contains(entry.effects, call.Effect):
			return "effect"
		}
		for name := range entry.limits {
			if name != "calls" {
				return "limit-unknown"
			}
		}
	}
	return ""
}

var (
	principalBeforeScope = []string{"expired", "agent-unregistered", "software-mismatch", "not-issued", "revoked"}
	principalHostRefusal = []string{"expired", "agent-unregistered", "software-mismatch", "not-issued", "revoked", "subject-mismatch", "subject-unauthorized"}
)

// principalAnswer holds the port's answer to its exact form; ok is false where it is not one.
func principalAnswer(data []byte) (refusal *PrincipalRefusal, ok bool) {
	value, err := descriptorJSON(data, true)
	if err != nil {
		return nil, false
	}
	obj, isObject := value.(map[string]any)
	if !isObject {
		return nil, false
	}
	verdict, _ := obj["admission"].(string)
	switch {
	case verdict == "admit" && len(obj) == 1:
		return nil, true
	case verdict != "refuse":
		return nil, false
	}
	name, _ := obj["refusal"].(string)
	if name == "out-of-scope" {
		reason, _ := obj["reason"].(string)
		if len(obj) != 3 || !slices.Contains(PrincipalScopeReasons, reason) {
			return nil, false
		}
		return &PrincipalRefusal{Name: name, Reason: reason}, true
	}
	if len(obj) != 2 || !slices.Contains(principalHostRefusal, name) {
		return nil, false
	}
	return &PrincipalRefusal{Name: name}, true
}

func (p *OpenedPrincipal) hostTime() (time.Time, bool) {
	var at time.Time
	var err error
	func() {
		defer func() {
			if recover() != nil {
				err = errors.New("clock panicked")
			}
		}()
		at, err = p.clock()
	}()
	if err != nil || at.IsZero() || at.Year() < 1970 || at.Year() > 9999 {
		return time.Time{}, false
	}
	return at.UTC().Truncate(time.Millisecond), true
}

func (p *OpenedPrincipal) askPort(ctx context.Context, read PrincipalAdmissionRead) ([]byte, error) {
	bounded, cancel := context.WithTimeout(ctx, PrincipalAdmissionTimeout)
	defer cancel()
	type answer struct {
		data []byte
		err  error
	}
	done := make(chan answer, 1)
	go func() {
		defer func() {
			if recover() != nil {
				done <- answer{err: errors.New("admission port panicked")}
			}
		}()
		data, err := p.port(bounded, read)
		done <- answer{data, err}
	}()
	select {
	case got := <-done:
		return got.data, got.err
	case <-bounded.Done():
		return nil, bounded.Err()
	}
}

// AdmitPrincipalCall is one admission (RFC 0030 §2, When admission happens): at the host's time,
// step 8, the package's own part of step 11, and the port's answer for steps 9, 10, the calls
// limit and 12, read afresh. count asks the issuing host to count this dispatch: only the admission
// immediately before a dispatch this package is the enforcing host of, and only when its own checks
// held. A refusal of steps 8 to 10 the port answers, or a read that cannot be made, is reported
// before the package's own, and the package's own before the port's step 11 or 12. It returns nil
// where the call is admitted; a call whose ctx ends while it waits returns ctx's error.
func (p *OpenedPrincipal) AdmitPrincipalCall(ctx context.Context, call PrincipalCall, count bool) (*PrincipalRefusal, error) {
	at, ok := p.hostTime()
	if !ok {
		return &PrincipalRefusal{Name: "admission-unknown"}, nil
	}
	for _, l := range p.Chain.Links {
		if at.Before(l.IssuedAt) {
			return &PrincipalRefusal{Name: "not-yet-valid"}, nil
		}
	}
	for _, l := range p.Chain.Links {
		if !at.Before(l.ExpiresAt) {
			return &PrincipalRefusal{Name: "expired"}, nil
		}
	}
	var own *PrincipalRefusal
	if reason := p.Chain.PrincipalScopeReason(call); reason != "" {
		own = &PrincipalRefusal{Name: "out-of-scope", Reason: reason}
	}
	if call.Audience == nil || principalStrength(call.Effect) < 0 || call.Tool == "" || call.Source == "" {
		if own == nil {
			own = &PrincipalRefusal{Name: "out-of-scope", Reason: "source"}
		}
		return own, nil
	}
	read := PrincipalAdmissionRead{Host: p.Chain.Links[0].Host, Audience: *call.Audience, Chain: append([]string(nil), p.Chain.Digests...), Call: PrincipalAdmissionCall{Source: call.Source, Tool: call.Tool, Effect: call.Effect, Count: count && own == nil}, At: PrincipalTime(at)}
	data, err := p.askPort(ctx, read)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return &PrincipalRefusal{Name: "admission-unknown"}, nil
	}
	answer, ok := principalAnswer(data)
	switch {
	case !ok:
		return &PrincipalRefusal{Name: "admission-unknown"}, nil
	case answer != nil && slices.Contains(principalBeforeScope, answer.Name):
		return answer, nil
	case own != nil:
		return own, nil
	}
	return answer, nil
}

// PrincipalRefusalDetail is what the model and the host are told of a refused call; it repeats no
// statement, key or credential.
func PrincipalRefusalDetail(tool string, refusal PrincipalRefusal) string {
	named := refusal.Name
	if refusal.Reason != "" {
		named += " (" + refusal.Reason + ")"
	}
	return tool + " was not run: the delegation this run acts under does not admit it (" + named + "). Nothing was changed. Do not retry it; say what could not be done."
}

func principalExact(v *Value, members ...string) bool {
	if !v.IsObject() || len(v.Keys()) != len(members) {
		return false
	}
	for _, m := range members {
		if !v.Has(m) {
			return false
		}
	}
	return true
}

func principalString(v *Value) (string, bool) {
	if v == nil || v.Kind() != KindString {
		return "", false
	}
	return v.Str(), true
}

// validPrincipalValue reports a principal record member of its exact form (run-principal.json).
func validPrincipalValue(v *Value) bool {
	if !principalExact(v, "actor", "rootActor", "subject", "delegation", "chain") {
		return false
	}
	if root, ok := principalString(v.Get("rootActor")); !ok || !principalOpaque.MatchString(root) {
		return false
	}
	actor, subject, delegation, chain := v.Get("actor"), v.Get("subject"), v.Get("delegation"), v.Get("chain")
	agent, ok1 := principalString(actor.Get("agent"))
	host, ok2 := principalString(actor.Get("host"))
	if !principalExact(actor, "agent", "host", "software") || !ok1 || !ok2 || !principalOpaque.MatchString(agent) || !PrincipalFlatToken(host) {
		return false
	}
	software := actor.Get("software")
	switch {
	case principalExact(software, "package", "version"):
		p, ok1 := principalString(software.Get("package"))
		ver, ok2 := principalString(software.Get("version"))
		if !ok1 || !ok2 || p == "" || ver == "" {
			return false
		}
	case principalExact(software, "harness"):
		h, ok := principalString(software.Get("harness"))
		if !ok || !principalDigest.MatchString(h) {
			return false
		}
	default:
		return false
	}
	kind, ok1 := principalString(subject.Get("kind"))
	issuer, ok2 := principalString(subject.Get("issuer"))
	sub, ok3 := principalString(subject.Get("subject"))
	if !principalExact(subject, "kind", "issuer", "subject") || !ok1 || !ok2 || !ok3 || (kind != "person" && kind != "service") || issuer == "" || sub == "" {
		return false
	}
	id, ok1 := principalString(delegation.Get("id"))
	digest, ok2 := principalString(delegation.Get("digest"))
	if !principalExact(delegation, "id", "digest") || !ok1 || !ok2 || !principalOpaque.MatchString(id) || !principalDigest.MatchString(digest) {
		return false
	}
	if chain == nil || !chain.IsArray() || chain.Len() < 1 || chain.Len() > PrincipalMaxDepth+1 {
		return false
	}
	for _, item := range chain.Items() {
		if s, ok := principalString(item); !ok || !principalDigest.MatchString(s) {
			return false
		}
	}
	last, _ := principalString(chain.Items()[chain.Len()-1])
	return last == digest
}

// Contract is the record member as the generated contract type.
func (r PrincipalRecord) Contract() *contract.RunPrincipal {
	var out contract.RunPrincipal
	if err := json.Unmarshal(r.Value().JSON(), &out); err != nil {
		return nil
	}
	return &out
}

// Contract is the refusal as the generated contract type.
func (r PrincipalRefusal) Contract() *contract.RunPrincipalRefusal {
	var out contract.RunPrincipalRefusal
	if err := json.Unmarshal(r.Value().JSON(), &out); err != nil {
		return nil
	}
	return &out
}
