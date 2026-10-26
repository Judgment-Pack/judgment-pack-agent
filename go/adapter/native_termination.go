package adapter

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
	"sync"
)

type NativeTerminationInvocation struct{ identity byte }

// NativeTermination is a live certification capability. The actual bound native host may
// report an unoffered protocol call before invoking its executable canonical Tool callback.
// It grants no execution or continuation authority and does not replace release proof.
type NativeTermination struct {
	Invocation *NativeTerminationInvocation
	Refuse     func(*NativeTerminationInvocation, AgentToolCall) error
}
type NativeTerminationEvidence struct {
	RequestDigest string
	Call          AgentToolCall
}
type nativeTerminationState struct {
	mu            sync.Mutex
	live, invalid bool
	token         *NativeTerminationInvocation
	digest        string
	offered       map[string]bool
	callbacks     map[string]bool
	call          *AgentToolCall
}
type NativeProtocolTerminated struct{ state *nativeTerminationState }

func (*NativeProtocolTerminated) Error() string {
	return "NativeProtocolTerminated: an unoffered call ended the invocation before its canonical callback."
}
func (e *NativeProtocolTerminated) Unwrap() error {
	return failure.Authored("invalid-response", e.Error(), "agent")
}

// NativeTerminationAttempt is harness-owned. Its observer records actual canonical callbacks;
// finish accepts only this invocation's error receipt after actual provider/process release.
func NativeTerminationAttempt(request AgentRequest) (*NativeTermination, func(AgentToolCall), func(error, bool) (NativeTerminationEvidence, bool)) {
	raw, _ := json.Marshal(request)
	digest := sha256.Sum256(raw)
	state := &nativeTerminationState{live: true, token: &NativeTerminationInvocation{1}, digest: "sha256:" + hex.EncodeToString(digest[:]), offered: map[string]bool{}, callbacks: map[string]bool{}}
	for _, tool := range request.Tools {
		state.offered[tool.Name] = true
	}
	cap := &NativeTermination{Invocation: state.token, Refuse: func(token *NativeTerminationInvocation, call AgentToolCall) error {
		state.mu.Lock()
		defer state.mu.Unlock()
		var args map[string]json.RawMessage
		if !state.live || state.invalid || token != state.token || state.call != nil || call.ID == "" || len(call.ID) > 256 || call.Name == "" || len(call.Name) > 64 || state.offered[call.Name] || state.callbacks[call.ID] || len(call.Arguments) > ObservationMaxArguments || json.Unmarshal(call.Arguments, &args) != nil || args == nil {
			state.invalid = true
			return failure.Authored("configuration", "The native termination evidence does not match this live unoffered call.", "agent")
		}
		call.Arguments = bytes.Clone(call.Arguments)
		state.call = &call
		return &NativeProtocolTerminated{state: state}
	}}
	observe := func(call AgentToolCall) {
		state.mu.Lock()
		defer state.mu.Unlock()
		state.callbacks[call.ID] = true
		if state.call != nil && state.call.ID == call.ID {
			state.invalid = true
		}
	}
	finish := func(err error, released bool) (NativeTerminationEvidence, bool) {
		state.mu.Lock()
		defer state.mu.Unlock()
		if !state.live {
			return NativeTerminationEvidence{}, false
		}
		state.live = false
		var receipt *NativeProtocolTerminated
		if state.invalid || state.call == nil || !released || !errors.As(err, &receipt) || receipt.state != state || state.callbacks[state.call.ID] {
			return NativeTerminationEvidence{}, false
		}
		call := *state.call
		call.Arguments = bytes.Clone(call.Arguments)
		return NativeTerminationEvidence{RequestDigest: state.digest, Call: call}, true
	}
	return cap, observe, finish
}
