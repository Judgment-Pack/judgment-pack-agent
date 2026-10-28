package certification

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	native "github.com/Judgment-Pack/judgment-pack-agent/go/adapter/agent"
)

func nativeEvidenceFixture(mode string) AgentHost {
	return AgentHost{Name: "disposable stand-in early-protocol fixture", StandIn: true, Bind: func(script AgentScript) adapter.AgentRun {
		return func(ctx context.Context, request adapter.AgentRequest, cb adapter.AgentCallbacks) error {
			if mode == "canonical" {
				return Play(ctx, request, cb, script(request, 1))
			}
			if mode != "missing-release" {
				defer cb.ProviderRelease.Acknowledge(cb.ProviderRelease.Invocation)
			}
			for index, step := range script(request, 1).Steps {
				if step.Tool == "" {
					continue
				}
				name := OfferedName(request, step.Tool)
				call := adapter.AgentToolCall{ID: fmt.Sprintf("call-item-%d", index+1), Name: name, Arguments: step.Arguments}
				offered := false
				for _, tool := range request.Tools {
					if tool.Name == name {
						offered = true
					}
				}
				if !offered {
					if mode == "fabricated" {
						return errors.New("NativeProtocolTerminated: fabricated")
					}
					if mode == "callback" {
						_, _ = cb.Tool(ctx, call)
					}
					if mode == "different-call" {
						call.Arguments = json.RawMessage(`{"path":"other"}`)
					}
					return cb.NativeTermination.Refuse(cb.NativeTermination.Invocation, call)
				}
				if _, err := cb.Tool(ctx, call); err != nil {
					return err
				}
			}
			return nil
		}
	}}
}
func TestActualNativeTerminationClaimPreservesCanonicalClaim(t *testing.T) {
	for _, mode := range []string{"valid", "canonical", "missing-release", "fabricated", "callback", "different-call"} {
		t.Run(mode, func(t *testing.T) {
			findings, err := NativeTerminationCheck(context.Background(), nil, nativeEvidenceFixture(mode), nil, native.New())
			if err != nil {
				t.Fatal(err)
			}
			if mode == "valid" {
				if len(findings) != 0 {
					t.Fatalf("valid early termination failed: %+v", findings)
				}
			} else if len(findings) == 0 {
				t.Fatal("invalid early termination silently certified")
			}
		})
	}
	var out bytes.Buffer
	passed, failed := CertifyAgent(context.Background(), &out, nil, nativeEvidenceFixture("valid"), func(inner Adapter) Adapter { return inner }, native.New(), "project native termination")
	if passed != 1 || failed != 0 || !strings.Contains(out.String(), "stand-in") {
		t.Fatalf("opt-in claim or stand-in label absent: %s", out.String())
	}
	for _, check := range AgentChecks {
		if check.Group == "project native termination" && !check.OptIn {
			t.Fatal("native claim merged into ordinary canonical certification")
		}
	}
}
