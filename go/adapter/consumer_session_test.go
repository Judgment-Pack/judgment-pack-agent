package adapter_test

import (
	"context"
	"testing"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	native "github.com/Judgment-Pack/judgment-pack-agent/go/adapter/agent"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

func TestPreparedOpeningProjectionReadsTrustedHostOnlyOnce(t *testing.T) {
	calls := 0
	raw := &adapter.Run{OpeningProjection: &runcontext.OpeningProjection{PreviewPaths: []string{"/preview"}, Project: func(context.Context, runcontext.OpeningProjectionInput) (map[string]string, error) {
		calls++
		return map[string]string{"/preview": "kept"}, nil
	}}}
	session := adapter.NewRequestSession(raw, nil)
	raw.OpeningProjection.PreviewPaths[0] = "/protected"
	first, err := session.OpeningProjection().Project(context.Background(), runcontext.OpeningProjectionInput{})
	if err != nil {
		t.Fatal(err)
	}
	first["/preview"] = "mutated returned map"
	session.OpeningProjection().PreviewPaths[0] = "/protected"
	second, err := session.OpeningProjection().Project(context.Background(), runcontext.OpeningProjectionInput{})
	if err != nil || calls != 1 || second["/preview"] != "kept" || session.OpeningProjection().PreviewPaths[0] != "/preview" {
		t.Fatal("retry repeated trusted host read or retained mutable declaration/result alias")
	}
}

func TestNativeOutputCeilingRefusesBeforeCapability(t *testing.T) {
	calls, refused := 0, false
	run := &adapter.Run{RequestOutputCeiling: 1, ModelName: "fixture", Prompt: "Ping", AllowConversation: true, Critic: "off", Context: &runcontext.Input{RunStore: &runcontext.DurableRunInput{ID: "native-ceiling", Store: runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})}, CurrentRequest: &runcontext.CurrentRequest{Message: "Ping", Input: "Ping"}}, Agent: &adapter.Agent{Model: "fixture", Run: func(ctx context.Context, _ adapter.AgentRequest, cb adapter.AgentCallbacks) error {
		calls++
		return cb.Event(ctx, adapter.AgentEvent{Type: "message", ID: "ping", Text: "P", Phase: "final"})
	}}, Emit: func(e contract.RunEvent) {
		if e.Error != nil && e.Error.Failure != nil && e.Error.Failure.Code == "configuration" {
			refused = true
		}
	}}
	if err := native.New().Run(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if calls != 0 || !refused {
		t.Fatal("opaque native route claimed physical output enforcement")
	}
}
