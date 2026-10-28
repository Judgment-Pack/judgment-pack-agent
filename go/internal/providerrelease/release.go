package providerrelease

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
)

// Invocation is a live identity; a serialized record cannot recreate it.
type Invocation struct{ identity byte }
type Capability struct {
	Invocation  *Invocation
	Acknowledge func(*Invocation) bool
	// NotAdmitted is distinct from release and requires zero invocation callbacks.
	NotAdmitted func(*Invocation) bool
	owners      []*attemptOwner
	malformed   bool
}
type Disposition string

const (
	Released      Disposition = "released"
	NeverAdmitted Disposition = "not-admitted"
	Unknown       Disposition = "unknown"
)

type attemptOwner struct {
	mu            sync.Mutex
	order         uint64
	live, invalid bool
	disposition   Disposition
}

var nextOwner atomic.Uint64

func capabilityFor(token *Invocation, owners []*attemptOwner, malformed bool) *Capability {
	// Every composite uses this same private order. No host function runs while an
	// owner is locked, and finish cannot observe a partially recorded disposition.
	record := func(given *Invocation, value Disposition) bool {
		for _, owner := range owners {
			owner.mu.Lock()
		}
		defer func() {
			for i := len(owners) - 1; i >= 0; i-- {
				owners[i].mu.Unlock()
			}
		}()
		valid := given == token && !malformed
		for _, owner := range owners {
			if !owner.live || owner.invalid || owner.disposition != Unknown {
				valid = false
			}
		}
		if !valid {
			for _, owner := range owners {
				owner.invalid = true
			}
			return false
		}
		for _, owner := range owners {
			owner.disposition = value
		}
		return true
	}
	return &Capability{Invocation: token, Acknowledge: func(given *Invocation) bool { return record(given, Released) }, NotAdmitted: func(given *Invocation) bool { return record(given, NeverAdmitted) }, owners: owners, malformed: malformed}
}

// NewDispositionAttempt binds either release or never-admitted proof to one live attempt.
// Duplicate, mixed, wrong and late proof refuses. Unknown/started outcomes still require release.
func NewDispositionAttempt() (*Capability, func(error, bool) Disposition) {
	token := &Invocation{identity: 1}
	owner := &attemptOwner{order: nextOwner.Add(1), live: true, disposition: Unknown}
	return capabilityFor(token, []*attemptOwner{owner}, false), func(err error, callbackReached bool) Disposition {
		owner.mu.Lock()
		defer owner.mu.Unlock()
		if !owner.live {
			return Unknown
		}
		owner.live = false
		if owner.invalid {
			return Unknown
		}
		if owner.disposition == NeverAdmitted {
			classified := failure.Normalize(err, "agent", time.Now())
			if err == nil || callbackReached || classified.Source != "agent" || classified.Outcome != "not-started" {
				return Unknown
			}
		}
		return owner.disposition
	}
}

// NewReleaseAttempt retains the release-only contract for existing callers.
func NewReleaseAttempt() (*Capability, func() bool) {
	capability, finish := NewDispositionAttempt()
	return capability, func() bool { return finish(nil, false) == Released }
}

// Compose atomically records one disposition in every original private owner.
// Any refused owner invalidates the complete proof before another finish can use it.
func Compose(capabilities ...*Capability) *Capability {
	provided := []*Capability{}
	for _, capability := range capabilities {
		if capability != nil {
			provided = append(provided, capability)
		}
	}
	if len(provided) == 0 {
		return nil
	}
	if len(provided) == 1 {
		return provided[0]
	}
	owners := []*attemptOwner{}
	seen := map[*attemptOwner]bool{}
	malformed := false
	for _, capability := range provided {
		if capability.malformed || len(capability.owners) == 0 {
			malformed = true
		}
		for _, owner := range capability.owners {
			if owner == nil || seen[owner] {
				malformed = true
				continue
			}
			seen[owner] = true
			owners = append(owners, owner)
		}
	}
	sort.Slice(owners, func(i, j int) bool { return owners[i].order < owners[j].order })
	return capabilityFor(&Invocation{identity: 1}, owners, malformed)
}
