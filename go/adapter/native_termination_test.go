package adapter_test

import (
	"encoding/json"
	"errors"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	"testing"
)

func TestNativeTerminationEvidenceIsLiveExactAndReleased(t *testing.T) {
	request := adapter.AgentRequest{Model: "fixture", Tools: []adapter.AgentTool{{Name: "offered", Description: "read_note: read", InputSchema: json.RawMessage(`{"type":"object"}`)}}}
	for _, mode := range []string{"valid", "offered", "wrong", "duplicate", "callback", "missing-release", "fabricated", "mismatched-error", "late"} {
		t.Run(mode, func(t *testing.T) {
			cap, observe, finish := adapter.NativeTerminationAttempt(request)
			call := adapter.AgentToolCall{ID: "unoffered-call", Name: "write_file", Arguments: json.RawMessage(`{"path":"fixture"}`)}
			token := cap.Invocation
			if mode == "offered" {
				call.Name = "offered"
			}
			if mode == "wrong" {
				other, _, _ := adapter.NativeTerminationAttempt(request)
				token = other.Invocation
			}
			err := cap.Refuse(token, call)
			switch mode {
			case "duplicate":
				_ = cap.Refuse(token, call)
			case "callback":
				observe(call)
			case "fabricated":
				err = errors.New("NativeProtocolTerminated")
			case "mismatched-error":
				other, _, _ := adapter.NativeTerminationAttempt(request)
				err = other.Refuse(other.Invocation, call)
			case "late":
				_, _ = finish(err, true)
				if err = cap.Refuse(token, call); err == nil {
					t.Fatal("late termination accepted")
				}
			}
			proof, ok := finish(err, mode != "missing-release")
			if mode == "valid" {
				if !ok || proof.Call.ID != call.ID || proof.RequestDigest == "" {
					t.Fatal("exact released termination was not recorded")
				}
				if _, again := finish(err, true); again {
					t.Fatal("termination evidence replayed")
				}
			} else if ok {
				t.Fatal("fabricated, mismatched or unreleased termination accepted")
			}
		})
	}
}
