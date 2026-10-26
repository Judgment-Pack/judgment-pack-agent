// Package historycapture is private capture bookkeeping. Only original-writer hooks retain an issuer.
// It supplies no dispatch, store, resume or acknowledgement authority to external callers.
package historycapture

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"unicode/utf8"
)

var ErrUnavailable = errors.New("ReplayUnavailable: a complete supported capture could not be verified.")

const MaxBytes = 64 * 1024 * 1024

type Scope struct {
	Execution string `json:"execution"`
	Role      string `json:"role"`
	Task      string `json:"task,omitempty"`
}
type Ref struct {
	Digest string `json:"digest"`
	Bytes  int    `json:"bytes"`
}
type Identity struct {
	Scope         Scope  `json:"scope"`
	DispatchID    string `json:"dispatchId"`
	Kind          string `json:"kind"`
	Source        string `json:"source"`
	Tool          string `json:"tool"`
	RequestDigest string `json:"requestDigest"`
	Surface       string `json:"surface"`
}
type Admission struct {
	Identity    Identity `json:"identity"`
	Sequence    int      `json:"sequence"`
	Ordinal     int      `json:"ordinal"`
	CommitID    string   `json:"commitId"`
	Expectation Ref      `json:"expectation"`
	Request     string   `json:"request"`
}
type Terminal struct {
	RunID             string `json:"runId"`
	ExecutionRevision string `json:"executionRevision"`
	Checkpoint        Ref    `json:"checkpoint"`
	Status            string `json:"status"`
}
type Token struct {
	owner *Collector
	index int
}
type Root struct{ owner *Collector }
type row struct {
	Admission    Admission
	Frame        json.RawMessage
	acknowledged bool
}
type CapturedRow struct {
	Admission Admission       `json:"admission"`
	Frame     json.RawMessage `json:"frame"`
}
type Material struct {
	Terminal Terminal      `json:"terminal"`
	Rows     []CapturedRow `json:"rows"`
}
type Seal struct {
	material Material
	identity *Seal
	store    any
	consumed bool
	mu       *sync.Mutex
}
type Collector struct {
	tokens         map[*Token]int
	mu             sync.Mutex
	owner          any
	store          any
	runID          string
	scope          Scope
	root           *Root
	rows           []row
	seen           map[string]bool
	failed, closed bool
	bytes          int
}

func id(s string) bool {
	if !utf8.ValidString(s) || len(s) < 1 {
		return false
	}
	units := 0
	for _, r := range s {
		units++
		if r > 0xffff {
			units++
		}
		if units > 256 {
			return false
		}
		if r < 32 {
			return false
		}
	}
	return true
}
func digest(s string) string {
	v := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(v[:])
}
func ref(r Ref) bool {
	if r.Bytes < 1 || r.Bytes > 4*1024*1024 || len(r.Digest) != 71 || r.Digest[:7] != "sha256:" {
		return false
	}
	_, e := hex.DecodeString(r.Digest[7:])
	return e == nil && strings.ToLower(r.Digest) == r.Digest
}
func New(owner any, runID string, scope Scope) (*Collector, *Root, error) {
	v := reflect.ValueOf(owner)
	if !v.IsValid() || v.Kind() != reflect.Pointer || v.IsNil() || !id(runID) || scope.Execution != runID || scope.Role != "author" || scope.Task != "" {
		return nil, nil, ErrUnavailable
	}
	c := &Collector{owner: owner, runID: runID, scope: scope, seen: map[string]bool{}, tokens: map[*Token]int{}}
	c.root = &Root{c}
	return c, c.root, nil
}

// BindPublisher enrolls the exact original store pointer once, before any admission.
func (c *Collector) BindPublisher(owner, store any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e := c.check(owner); e != nil {
		return e
	}
	v := reflect.ValueOf(store)
	if !v.IsValid() || v.Kind() != reflect.Pointer || v.IsNil() || c.store != nil || len(c.rows) > 0 {
		return c.fail()
	}
	c.store = store
	return nil
}
func (c *Collector) check(owner any) error {
	if owner != c.owner || c.failed || c.closed {
		c.failed = true
		return ErrUnavailable
	}
	return nil
}
func (c *Collector) fail() error { c.failed = true; return ErrUnavailable }

// Admit is called only after the original writer verifies the exact claim/expectation journal ACK.
func (c *Collector) Admit(owner any, a Admission) (*Token, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e := c.check(owner); e != nil {
		return nil, e
	}
	i := a.Identity
	surface := "session-model"
	if i.Kind == "agent" {
		surface = "native-aggregate"
	}
	if !id(i.DispatchID) || !id(i.Source) || !id(i.Tool) || !id(i.Scope.Execution) || !id(i.Scope.Role) || i.Scope.Task != "" && !id(i.Scope.Task) || (i.Kind != "model" && i.Kind != "agent") || i.Surface != surface || a.Sequence < 1 || a.Ordinal < 0 || !id(a.CommitID) || !ref(a.Expectation) || len(a.Request) > 4*1024*1024 || digest(a.Request) != i.RequestDigest || len(c.rows) >= 400 || c.seen[i.DispatchID] || (i.Scope.Task == "" && i.Scope.Execution != c.runID) || (i.Scope.Task != "" && i.Scope.Execution != i.Scope.Task) {
		return nil, c.fail()
	}
	if len(c.rows) > 0 {
		p := c.rows[len(c.rows)-1].Admission
		if a.Sequence < p.Sequence || a.Sequence == p.Sequence && a.Ordinal <= p.Ordinal {
			return nil, c.fail()
		}
	}
	token := &Token{c, len(c.rows)}
	c.tokens[token] = len(c.rows)
	c.rows = append(c.rows, row{Admission: a})
	c.seen[i.DispatchID] = true
	return token, nil
}
func (c *Collector) held(t *Token) *row {
	index, known := c.tokens[t]
	if t == nil || !known || t.owner != c || index != t.index || t.index < 0 || t.index >= len(c.rows) {
		return nil
	}
	return &c.rows[t.index]
}
func (c *Collector) Record(owner any, t *Token, raw json.RawMessage) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e := c.check(owner); e != nil {
		return e
	}
	r := c.held(t)
	if r == nil || r.Frame != nil || !json.Valid(raw) || len(raw) < 1 || c.bytes+len(raw) > MaxBytes {
		return c.fail()
	}
	r.Frame = append(json.RawMessage(nil), raw...)
	c.bytes += len(raw)
	return nil
}

// Acknowledge is called only after the matching terminal result publication is verified.
func (c *Collector) Acknowledge(owner any, t *Token) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e := c.check(owner); e != nil {
		return e
	}
	r := c.held(t)
	if r == nil || r.acknowledged {
		return c.fail()
	}
	r.acknowledged = true
	return nil
}
func (c *Collector) Seal(owner any, root *Root, terminal Terminal) (*Seal, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e := c.check(owner); e != nil {
		return nil, e
	}
	if root != c.root || terminal.RunID != c.runID || !id(terminal.ExecutionRevision) || !ref(terminal.Checkpoint) || (terminal.Status != "complete" && terminal.Status != "stopped") {
		return nil, c.fail()
	}
	m := Material{Terminal: terminal, Rows: []CapturedRow{}}
	for _, r := range c.rows {
		if !r.acknowledged || r.Frame == nil {
			return nil, c.fail()
		}
		m.Rows = append(m.Rows, CapturedRow{r.Admission, append(json.RawMessage(nil), r.Frame...)})
	}
	c.closed = true
	seal := &Seal{material: m, store: c.store, mu: &sync.Mutex{}}
	seal.identity = seal
	return seal, nil
}
func (s *Seal) Material() (Material, error) {
	if s == nil || s.identity != s {
		return Material{}, ErrUnavailable
	}
	m := s.material
	m.Rows = append([]CapturedRow{}, m.Rows...)
	for i := range m.Rows {
		m.Rows[i].Frame = append(json.RawMessage(nil), m.Rows[i].Frame...)
	}
	return m, nil
}

// Consume pins publication to the enrolled store and is one-use. Inspection is never authority.
func (s *Seal) Consume(store any) (Material, error) {
	if s == nil || s.identity != s {
		return Material{}, ErrUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v := reflect.ValueOf(store)
	if !v.IsValid() || v.Kind() != reflect.Pointer || v.IsNil() || s.store == nil || s.store != store || s.consumed {
		return Material{}, ErrUnavailable
	}
	s.consumed = true
	return s.Material()
}
