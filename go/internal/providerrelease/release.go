package providerrelease

import (
	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
	"sync"
	"time"
)

// Invocation is a live identity; a serialized record cannot recreate it.
type Invocation struct{ identity byte }
type Capability struct {
	Invocation  *Invocation
	Acknowledge func(*Invocation) bool
	// NotAdmitted is a distinct disposition, never a release of another account owner.
	// It is valid only with an exact typed not-started return and zero invocation callbacks.
	NotAdmitted func(*Invocation) bool
}
type Disposition string

const (
	Released      Disposition = "released"
	NeverAdmitted Disposition = "not-admitted"
	Unknown       Disposition = "unknown"
)

// NewDispositionAttempt binds either release or never-admitted proof to one live attempt.
// Duplicate, mixed, wrong and late proof refuses. Unknown/started outcomes still require release.
func NewDispositionAttempt() (*Capability, func(error, bool) Disposition) {
	token := &Invocation{identity: 1}
	var mu sync.Mutex
	live, invalid := true, false
	disposition := Unknown
	record := func(given *Invocation, value Disposition) bool {
		mu.Lock()
		defer mu.Unlock()
		if !live || given != token || disposition != Unknown {
			invalid = true
			return false
		}
		disposition = value
		return true
	}
	capability := &Capability{Invocation: token, Acknowledge: func(given *Invocation) bool { return record(given, Released) }, NotAdmitted: func(given *Invocation) bool { return record(given, NeverAdmitted) }}
	return capability, func(err error, callbackReached bool) Disposition {
		mu.Lock()
		defer mu.Unlock()
		if !live {
			return Unknown
		}
		live = false
		if invalid {
			return Unknown
		}
		if disposition == NeverAdmitted {
			classified := failure.Normalize(err, "agent", time.Now())
			if err == nil || callbackReached || classified.Source != "agent" || classified.Outcome != "not-started" {
				return Unknown
			}
		}
		return disposition
	}
}

// NewReleaseAttempt retains the release-only contract for existing callers.
// A never-admitted disposition is deliberately not reported as an account release.
func NewReleaseAttempt() (*Capability, func() bool) {
	capability, finish := NewDispositionAttempt()
	return capability, func() bool { return finish(nil, false) == Released }
}
