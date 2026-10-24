package adapter

import "sync"

// ProviderReleaseInvocation is a live identity; a serialized record cannot recreate it.
type ProviderReleaseInvocation struct{ identity byte }
type ProviderRelease struct {
	Invocation  *ProviderReleaseInvocation
	Acknowledge func(*ProviderReleaseInvocation) bool
}

// ProviderReleaseAttempt is harness-owned. The host acknowledges the exact token only after
// actual account release. Duplicate, wrong and late acknowledgements never authorize dispatch.
func ProviderReleaseAttempt() (*ProviderRelease, func() bool) {
	token := &ProviderReleaseInvocation{identity: 1}
	var mu sync.Mutex
	live, acknowledged, invalid := true, false, false
	capability := &ProviderRelease{Invocation: token, Acknowledge: func(given *ProviderReleaseInvocation) bool {
		mu.Lock()
		defer mu.Unlock()
		if !live || given != token || acknowledged {
			invalid = true
			return false
		}
		acknowledged = true
		return true
	}}
	return capability, func() bool { mu.Lock(); defer mu.Unlock(); live = false; return acknowledged && !invalid }
}
