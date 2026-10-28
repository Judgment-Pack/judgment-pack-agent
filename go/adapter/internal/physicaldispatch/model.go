// Package physicaldispatch is restricted to package-owned adapter wrappers.
// Capability identity is explicit; no closure code pointer or host context marker grants admission.
package physicaldispatch

import (
	"context"
	"errors"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/relay"
	"net/http"
	"sync"
)

type Attempt func(relay.Call) (*http.Response, error)
type Model struct {
	expected *runcontext.RunBinding
	call     relay.Call
	invoke   func(context.Context, string, relay.Request, Attempt) (*http.Response, error)
}

func New(call relay.Call, invoke func(context.Context, string, relay.Request, Attempt) (*http.Response, error), binding *runcontext.RunBinding) *Model {
	var expected *runcontext.RunBinding
	if binding != nil {
		held := *binding
		expected = &held
	}
	return &Model{call: call, invoke: invoke, expected: expected}
}
func (m *Model) Call(ctx context.Context, suffix string, request relay.Request) (*http.Response, error) {
	return m.call(ctx, suffix, request)
}
func (m *Model) Dispatch(ctx context.Context, suffix string, request relay.Request, binding *runcontext.RunBinding, attempt Attempt) (*http.Response, error) {
	if m == nil || m.call == nil || m.invoke == nil || attempt == nil || m.expected == nil || binding == nil || *m.expected != *binding {
		return nil, &runcontext.ContinuationIdentityChanged{}
	}
	var mu sync.Mutex
	live, busy := true, false
	defer func() { mu.Lock(); live = false; mu.Unlock() }()
	return m.invoke(ctx, suffix, request, func(send relay.Call) (*http.Response, error) {
		mu.Lock()
		if !live || busy || ctx.Err() != nil {
			mu.Unlock()
			return nil, errors.New("expired physical model dispatcher")
		}
		busy = true
		mu.Unlock()
		defer func() { mu.Lock(); busy = false; mu.Unlock() }()
		return attempt(send)
	})
}
