// Package runtimeactions holds private connector provenance, never a public grant API.
package runtimeactions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/wire"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"math"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode/utf16"
	"unicode/utf8"
)

type Owner = contract.CheckpointRuntimeExecutionOwner
type Binding = contract.CheckpointRuntimeApprovalBinding
type Request struct {
	Name    string                         `json:"name"`
	Tool    string                         `json:"tool"`
	Args    json.RawMessage                `json:"args"`
	Source  contract.ToolOfferSourceRecord `json:"source"`
	Runtime Binding                        `json:"runtime"`
}
type Port struct {
	Owner Owner
	// Current must observe the actual connection and prevent or signal retargeting through dispatch.
	Current func(context.Context) (Owner, error)
	Approve func(Request) string
}
type Connector struct {
	Port         Port
	InitialTools []contract.ToolOfferRuntimeTool
	List         func(context.Context) ([]contract.ToolOfferRuntimeTool, error)
	Prepare      func(string, json.RawMessage) (json.RawMessage, error)
	Dispatch     func(context.Context, string, json.RawMessage) (json.RawMessage, error)
	Open         func() bool
}

// Handle has no public state or execution methods. Only the actual gate creates it.
type Handle struct{ connector *Connector }

var ErrStale = errors.New("Runtime action approval is missing or stale. Nothing was sent.")
var pin = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func ValidOwner(o Owner) bool {
	for _, v := range []string{o.ArtifactDigest, o.ProcessID, o.ProjectID, o.ProjectLabel, o.SessionID, o.PrincipalDigest, o.PrincipalLabel, o.ScopeDigest, o.ScopeLabel} {
		if !utf8.ValidString(v) || len(utf16.Encode([]rune(v))) < 1 || len(utf16.Encode([]rune(v))) > 128 {
			return false
		}
		for _, r := range v {
			if r < 32 || r == 127 {
				return false
			}
		}
	}
	return pin.MatchString(o.ArtifactDigest) && pin.MatchString(o.PrincipalDigest) && pin.MatchString(o.ScopeDigest)
}
func ValidBinding(b Binding) bool { return ValidOwner(b.Owner) && pin.MatchString(b.AdmissionDigest) }
func Register(c Connector) (*Handle, error) {
	if !ValidOwner(c.Port.Owner) || c.Port.Current == nil || c.List == nil || c.Prepare == nil || c.Dispatch == nil || c.Open == nil {
		return nil, ErrStale
	}
	raw, err := json.Marshal(c.InitialTools)
	if err != nil {
		return nil, ErrStale
	}
	if json.Unmarshal(raw, &c.InitialTools) != nil {
		return nil, ErrStale
	}
	return &Handle{connector: &c}, nil
}

// Canonical uses the established wire JSON reader/writer, UTF-16 key ordering, and exact safe
// integers. Arrays retain order; omitted members stay omitted. No identifier normalization.
func Canonical(raw []byte) (string, error) {
	v, err := wire.Parse(raw)
	if err != nil {
		return "", ErrStale
	}
	var out strings.Builder
	var write func(*wire.Value) error
	write = func(v *wire.Value) error {
		if n, ok := v.Number(); ok && (math.IsNaN(n) || math.IsInf(n, 0) || math.Trunc(n) == n && math.Abs(n) > 9007199254740991) {
			return ErrStale
		}
		switch v.Kind() {
		case wire.KindArray:
			out.WriteByte('[')
			for i, item := range v.Items() {
				if i > 0 {
					out.WriteByte(',')
				}
				if err := write(item); err != nil {
					return err
				}
			}
			out.WriteByte(']')
		case wire.KindObject:
			keys := v.Keys()
			sort.Slice(keys, func(i, j int) bool {
				a, b := utf16.Encode([]rune(keys[i])), utf16.Encode([]rune(keys[j]))
				for k := 0; k < len(a) && k < len(b); k++ {
					if a[k] != b[k] {
						return a[k] < b[k]
					}
				}
				return len(a) < len(b)
			})
			out.WriteByte('{')
			for i, key := range keys {
				if i > 0 {
					out.WriteByte(',')
				}
				out.Write(wire.Str(key).JSON())
				out.WriteByte(':')
				if err := write(v.Get(key)); err != nil {
					return err
				}
			}
			out.WriteByte('}')
		default:
			out.Write(v.JSON())
		}
		return nil
	}
	if err := write(v); err != nil {
		return "", err
	}
	return out.String(), nil
}
func canonical(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", ErrStale
	}
	return Canonical(raw)
}
func digest(text string) string {
	sum := sha256.Sum256([]byte(text))
	return "sha256:" + hex.EncodeToString(sum[:])
}
func same(a, b any) bool {
	x, e := canonical(a)
	y, f := canonical(b)
	return e == nil && f == nil && x == y
}
func projection(tools []contract.ToolOfferRuntimeTool) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		p := map[string]any{"name": t.Name, "taxonomy": t.Meta}
		if len(t.InputSchema) > 0 {
			p["inputSchema"] = t.InputSchema
		}
		if t.Description != nil {
			p["description"] = *t.Description
		}
		out = append(out, p)
	}
	return out
}

type Attempt struct {
	mu           sync.Mutex
	connector    *Connector
	request      Request
	offer, scope string
	used         bool
	entered      bool
}

func (a *Attempt) Request() Request {
	raw, _ := json.Marshal(a.request)
	var out Request
	_ = json.Unmarshal(raw, &out)
	return out
}
func (a *Attempt) Descriptor(callID string) (string, error) {
	return canonical(map[string]any{"version": 1, "callId": callID, "name": a.request.Name, "args": a.request.Args, "source": a.request.Source, "runtime": a.request.Runtime})
}
func Prepare(ctx context.Context, key any, name string, args, admission json.RawMessage, restored *Binding) (*Attempt, error) {
	handle, ok := key.(*Handle)
	if !ok || handle == nil {
		return nil, ErrStale
	}
	c := handle.connector
	if c == nil || !c.Open() {
		return nil, ErrStale
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	owner, err := c.Port.Current(ctx)
	if err != nil || !ValidOwner(owner) || !same(owner, c.Port.Owner) {
		return nil, ErrStale
	}
	listed, err := c.List(ctx)
	if err != nil {
		return nil, err
	}
	found := false
	for _, t := range listed {
		if t.Name == name && t.Meta.Effect == "act" {
			found = true
		}
	}
	if !found {
		return nil, ErrStale
	}
	clean, err := c.Prepare(name, args)
	if err != nil {
		return nil, err
	}
	canon, err := Canonical(clean)
	if err != nil {
		return nil, err
	}
	clean = json.RawMessage(canon)
	offer, err := canonical(projection(listed))
	if err != nil {
		return nil, err
	}
	initial, err := canonical(projection(c.InitialTools))
	if err != nil || offer != initial {
		return nil, ErrStale
	}
	identity := &contract.ToolOfferIdentity{Kind: "connection", Label: owner.PrincipalLabel}
	ident, _ := canonical(identity)
	listDigest := digest(offer)
	identityDigest := digest(ident)
	source := contract.ToolOfferSourceRecord{Kind: "runtime", ID: "runtime", Trust: "first-party", Tool: &name, ToolListDigest: &listDigest, Identity: identity, IdentityDigest: &identityDigest}
	scope, err := Canonical(admission)
	if err != nil {
		return nil, err
	}
	var data map[string]json.RawMessage
	if json.Unmarshal([]byte(scope), &data) != nil {
		return nil, ErrStale
	}
	var admitted []map[string]json.RawMessage
	if json.Unmarshal(data["finalRuntimeOffer"], &admitted) != nil {
		return nil, ErrStale
	}
	names := map[string]bool{}
	selected := false
	for _, row := range admitted {
		var toolName string
		if json.Unmarshal(row["name"], &toolName) != nil || names[toolName] {
			return nil, ErrStale
		}
		names[toolName] = true
		if toolName == name {
			selected = true
		}
		matched := false
		for _, actual := range projection(listed) {
			if same(row, actual) {
				matched = true
				break
			}
		}
		if !matched {
			return nil, ErrStale
		}
	}
	if !selected {
		return nil, ErrStale
	}

	raw, _ := json.Marshal(owner)
	data["owner"] = raw
	raw, _ = json.Marshal(source)
	data["source"] = raw
	data["version"] = json.RawMessage("1")
	preimage, err := canonical(data)
	if err != nil {
		return nil, err
	}
	binding := Binding{Owner: owner, AdmissionDigest: digest(preimage)}
	if restored != nil && !same(binding, *restored) {
		return nil, ErrStale
	}
	return &Attempt{connector: c, request: Request{Name: name, Tool: name, Args: clean, Source: source, Runtime: binding}, offer: offer, scope: scope}, nil
}
func Policy(a *Attempt) string {
	if a == nil || a.connector == nil {
		return "refuse"
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.used {
		return "refuse"
	}
	answer := "ask"
	if a.connector.Port.Approve != nil {
		answer = policy(a.connector.Port.Approve, a.Request())
	}
	if answer != "ask" && answer != "allow" && answer != "refuse" {
		return "refuse"
	}
	return answer
}
func policy(fn func(Request) string, request Request) (answer string) {
	defer func() {
		if recover() != nil {
			answer = "refuse"
		}
	}()
	return fn(request)
}
func Claim(a *Attempt) error {
	if a == nil || a.connector == nil {
		return ErrStale
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.entered || a.used {
		return ErrStale
	}
	a.entered = true
	return nil
}
func Execute(ctx context.Context, a *Attempt, admission func() json.RawMessage, authorize func() error) (json.RawMessage, error) {
	if a == nil || a.connector == nil {
		return nil, ErrStale
	}
	scope, err := Canonical(admission())
	if err != nil || scope != a.scope {
		return nil, ErrStale
	}
	a.mu.Lock()
	used := a.used || !a.entered
	a.mu.Unlock()
	if used {
		return nil, ErrStale
	}
	if err = authorize(); err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	c := a.connector
	if !c.Open() {
		return nil, ErrStale
	}
	owner, err := c.Port.Current(ctx)
	if err != nil || !same(owner, a.request.Runtime.Owner) {
		return nil, ErrStale
	}
	listed, err := c.List(ctx)
	if err != nil {
		return nil, err
	}
	offer, err := canonical(projection(listed))
	if err != nil || offer != a.offer {
		return nil, ErrStale
	}
	args, err := c.Prepare(a.request.Name, append(json.RawMessage(nil), a.request.Args...))
	if err != nil {
		return nil, err
	}
	canon, err := Canonical(args)
	if err != nil || canon != string(a.request.Args) {
		return nil, ErrStale
	}
	if err = authorize(); err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	finalScope, scopeErr := Canonical(admission())
	a.mu.Lock()
	if scopeErr != nil || finalScope != a.scope || a.used || !c.Open() {
		a.mu.Unlock()
		return nil, ErrStale
	}
	a.used = true
	a.mu.Unlock()
	return c.Dispatch(ctx, a.request.Name, append(json.RawMessage(nil), a.request.Args...))
}
