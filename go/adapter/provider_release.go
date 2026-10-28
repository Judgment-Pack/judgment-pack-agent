package adapter

import "github.com/Judgment-Pack/judgment-pack-agent/go/internal/providerrelease"

// Public capability names retain their original fields and exact invocation semantics.
type ProviderReleaseInvocation = providerrelease.Invocation
type ProviderRelease = providerrelease.Capability
type ProviderDisposition = providerrelease.Disposition

const (
	ProviderReleased           = providerrelease.Released
	ProviderNeverAdmitted      = providerrelease.NeverAdmitted
	ProviderDispositionUnknown = providerrelease.Unknown
)

func ProviderDispositionAttempt() (*ProviderRelease, func(error, bool) ProviderDisposition) {
	return providerrelease.NewDispositionAttempt()
}
func ProviderReleaseAttempt() (*ProviderRelease, func() bool) {
	return providerrelease.NewReleaseAttempt()
}
