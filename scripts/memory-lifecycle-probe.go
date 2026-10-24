// Public native consumer for the packed browser/native memory lifecycle probe.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"os"
)

func main() {
	if err := probe(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func probe() error {
	ctx := context.Background()
	const digest = "sha256:dd407b2b50d5735761059db743e2d628f0f6b17585ec025089e82380986dcff9"
	if "sha256:"+runcontext.ContextDigest("Use metric units.") != digest {
		return fmt.Errorf("fixture digest changed")
	}
	record := contract.Memory{ID: "fixture-metric", Digest: digest, Kind: "preference", Text: "Use metric units.", Source: contract.MemorySource{Run: "fixture-host", ProposedBy: "host"}, Scope: "project", CreatedAt: "2026-10-01T00:00:00Z", State: "accepted"}
	session := runcontext.Session{Prompt: "Current request", Critic: "off", Model: &runcontext.ModelSession{Family: "openai-compatible", Model: "fixture", Thinking: "off"}, Context: &runcontext.Input{Phase: "plan"}, Host: runcontext.HostFuncs{MemoryRecordsForFunc: func(context.Context, string) ([]contract.Memory, error) { return []contract.Memory{record}, nil }}}
	first, err := runcontext.ContextualSession(ctx, session)
	if err != nil {
		return err
	}
	saved, err := first.Context.Snapshot("stopped")
	first.Context.Close()
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(saved)
	value, err := runcontext.ParseJSON(raw)
	if err != nil {
		return err
	}
	checkpoint, err := runcontext.ReadExecution(value)
	if err != nil {
		return err
	}
	disabled := false
	calls := 0
	session.Context = &runcontext.Input{Phase: "plan", Execution: checkpoint, Memory: &disabled}
	session.Host = runcontext.HostFuncs{MemoryRecordsForFunc: func(context.Context, string) ([]contract.Memory, error) {
		return nil, fmt.Errorf("disabled text callback invoked")
	}, MemoryLifecycleFunc: func(_ context.Context, rows []contract.ProvenanceMemoryRecall) (any, error) {
		calls++
		if len(rows) != 1 || rows[0].ID != record.ID || rows[0].Digest != digest {
			return nil, fmt.Errorf("wrong prior metadata")
		}
		return []contract.MemoryLifecycleUpdate{{ID: record.ID, Digest: digest, Kind: "preference", State: "revoked"}}, nil
	}}
	resumed, err := runcontext.ContextualSession(ctx, session)
	if err != nil {
		return err
	}
	revoked, err := resumed.Context.Snapshot("stopped")
	resumed.Context.Close()
	if err != nil {
		return err
	}
	if calls != 1 || len(revoked.Memories.Recalled) != 1 || revoked.Memories.Recalled[0].State != "revoked" || revoked.Memories.Recalled[0].Digest != digest {
		return fmt.Errorf("native metadata resume failed")
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"accepted": saved, "revoked": revoked})
}
