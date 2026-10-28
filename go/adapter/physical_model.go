package adapter

import (
	"context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/internal/physicaldispatch"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/relay"
	"net/http"
)

// BindPhysicalModel is usable only by package-owned adapters: its capability type is internal.
func BindPhysicalModel(run *Run, model *physicaldispatch.Model) {
	run.physicalModel = model
	run.Model = model.Call
}
func BindPolicyPhysicalModel(binding PolicyConnection, model *physicaldispatch.Model) PolicyConnection {
	binding.physicalModel = model
	binding.Model = model.Call
	return binding
}

// DispatchPhysicalModel applies the original canonical attempt below a known package retry wrapper.
// Callbacks receive only the exact admitted underlying relay capability, never its owner or store.
func (s *RequestSession) DispatchPhysicalModel(ctx context.Context, binding *runcontext.RunBinding, role, suffix string, request relay.Request, attempt func(relay.Call) (*http.Response, error)) (*http.Response, error) {
	model := s.physicalModel
	if s.connection != nil {
		id := s.connection(role)
		if selected, ok := s.connections[id]; ok {
			model = selected.physicalModel
		} else if id != s.identityConnection && id != s.modelName {
			return nil, &runcontext.ContinuationIdentityChanged{}
		}
	}
	if model == nil {
		return attempt(s.CallModel)
	}
	return model.Dispatch(ctx, suffix, request, binding, attempt)
}

// DispatchSinglePhysicalModel refuses opaque package wrappers instead of entering a retry path.
// Supported root model bindings send one canonical call; a closure name never proves identity.
func (s *RequestSession) DispatchSinglePhysicalModel(ctx context.Context, binding *runcontext.RunBinding, role string, attempt func(relay.Call) (*http.Response, error)) (*http.Response, error) {
	if binding == nil || binding.Writer == nil || ctx.Err() != nil {
		return nil, &runcontext.ContinuationIdentityChanged{}
	}
	model := s.physicalModel
	if s.connection != nil {
		if selected, ok := s.connections[s.connection(role)]; ok {
			model = selected.physicalModel
		}
	}
	if model != nil {
		return nil, &runcontext.UsageBudgetUnsupported{}
	}
	return attempt(s.CallModel)
}
