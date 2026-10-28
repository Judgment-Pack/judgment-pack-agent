package adapter_test

import (
	"errors"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	"testing"
)

func TestProviderNeverAdmittedIsDistinctAndInvocationBound(t *testing.T) {
	for _, mode := range []string{"valid", "callback", "unknown", "wrong", "duplicate", "mixed", "late"} {
		t.Run(mode, func(t *testing.T) {
			cap, finish := adapter.ProviderDispositionAttempt()
			err := adapter.ErrBusy
			reached := false
			switch mode {
			case "wrong":
				other, _ := adapter.ProviderDispositionAttempt()
				if cap.NotAdmitted(other.Invocation) {
					t.Fatal("wrong invocation accepted")
				}
			case "late":
				if finish(err, false) != adapter.ProviderDispositionUnknown {
					t.Fatal("missing proof accepted")
				}
				if cap.NotAdmitted(cap.Invocation) {
					t.Fatal("late proof accepted")
				}
				return
			default:
				if !cap.NotAdmitted(cap.Invocation) {
					t.Fatal("exact never-admitted token refused")
				}
			}
			switch mode {
			case "callback":
				reached = true
			case "unknown":
				err = errors.New("account outcome unknown")
			case "duplicate":
				if cap.NotAdmitted(cap.Invocation) {
					t.Fatal("duplicate proof accepted")
				}
			case "mixed":
				if cap.Acknowledge(cap.Invocation) {
					t.Fatal("never-admitted was also released")
				}
			}
			got := finish(err, reached)
			if mode == "valid" {
				if got != adapter.ProviderNeverAdmitted || got == adapter.ProviderReleased {
					t.Fatal("never-admitted classification lost or relabeled released")
				}
				if finish(err, false) != adapter.ProviderDispositionUnknown {
					t.Fatal("completed disposition receipt replayed")
				}
			} else if got != adapter.ProviderDispositionUnknown {
				t.Fatal("invalid disposition authorized account progression")
			}
		})
	}
	cap, finish := adapter.ProviderReleaseAttempt()
	cap.NotAdmitted(cap.Invocation)
	if finish() {
		t.Fatal("release-only API accepted a never-admitted disposition")
	}
}
