package agent

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
)

// agent#342: the host's error is read as the package's own pause only where the run claimed a wait or a handoff, the pause
// stopped the invocation, and the caller's context is live.
func TestOnlyThePackagesOwnPauseIsReadAsThePause(t *testing.T) {
	live := context.Background()
	stopped, stop := context.WithCancel(live)
	stop()
	storage := errors.New("storage refused")
	for _, c := range []struct {
		name               string
		err, paused        error
		invocation, caller context.Context
		want               bool
	}{
		{"the context's error after a wait", context.Canceled, errWaiting, stopped, live, true},
		{"the context's error after a handoff", context.Canceled, errHandoff, stopped, live, true},
		{"a wrapped cancellation", fmt.Errorf("stream: %w", failure.Cancelled), errWaiting, stopped, live, true},
		{"the Tool error itself", errWaiting, errWaiting, stopped, live, true},
		{"the caller cancelled", context.Canceled, errWaiting, stopped, stopped, false},
		{"the invocation was not stopped", context.Canceled, errWaiting, live, live, false},
		{"a storage failure", context.Canceled, storage, stopped, live, false},
		{"no pause", context.Canceled, nil, stopped, live, false},
		{"another error", errors.New("provider failed"), errWaiting, stopped, live, false},
	} {
		if got := pausedBy(c.err, c.paused, c.invocation, c.caller); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}
