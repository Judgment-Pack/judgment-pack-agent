package principaltests

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	native "github.com/Judgment-Pack/judgment-pack-agent/go/adapter/agent"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
)

// A repeat record on a delegated run's checkpoint (ADR-0029, #283). The checkpoint is version 4; the record's custody
// asks of it what the record needs, which version 4 has. An acting tool's effect is performed once and its result is
// acknowledged, and a read goes on to the next call.

type repeatHead struct {
	Version int64           `json:"version"`
	Status  string          `json:"status"`
	Repeat  json.RawMessage `json:"repeatTools"`
	Durable struct {
		Dispatches []struct {
			Kind             string  `json:"kind"`
			Tool             string  `json:"tool"`
			State            string  `json:"state"`
			RepeatResultJSON *string `json:"repeatResultJson"`
		} `json:"dispatches"`
	} `json:"durable"`
}

func repeatHeadOf(t *testing.T, store runcontext.RunStore, id string) repeatHead {
	t.Helper()
	head, err := store.Load(context.Background(), id)
	if err != nil || head == nil {
		t.Fatal(head, err)
	}
	var out repeatHead
	if err := json.Unmarshal(head.Checkpoint, &out); err != nil {
		t.Fatal(err)
	}
	rows := out.Durable.Dispatches[:0]
	for _, row := range out.Durable.Dispatches {
		if row.Kind == "host" {
			rows = append(rows, row)
		}
	}
	out.Durable.Dispatches = rows
	return out
}

func repeatPolicyOn() runcontext.PolicyLayer {
	return runcontext.PolicyLayer{"version": 2, "repeatTools": map[string]any{"mode": "on", "stopAt": 3}}
}

func TestARepeatRecordOnADelegatedCheckpointAcknowledgesTheEffect(t *testing.T) {
	for _, engine := range hostEngines {
		for _, row := range []struct {
			name      string
			delegated bool
			version   int64
		}{{"undelegated", false, 3}, {"delegated", true, 4}} {
			t.Run(engine+"/"+row.name, func(t *testing.T) {
				store := waitStore{runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})}
				executed := &atomic.Int32{}
				hostPolicy = repeatPolicyOn()
				hostDelegation = nil
				if row.delegated {
					hostDelegation = runtimePrincipal(t, 0, runtimeNow, &runtimePrincipalPort{}, func(runcontext.PrincipalAdmissionRead) string { return runtimeAdmit })
				}
				t.Cleanup(func() { hostPolicy, hostDelegation = nil, nil })
				run := runHostTool(t, store, "run-r", engine, false, nil, delegatedWriteTool(executed), []string{hostArgs})
				for _, event := range run.events {
					if event.Error != nil {
						t.Fatalf("a repeat record stopped the run: %+v", *event.Error)
					}
				}
				head := repeatHeadOf(t, store, "run-r")
				if executed.Load() != 1 || head.Version != row.version || head.Status != "complete" || len(head.Repeat) == 0 {
					t.Fatalf("executed %d, version %d, status %q, repeat %s", executed.Load(), head.Version, head.Status, head.Repeat)
				}
				if rows := head.Durable.Dispatches; len(rows) != 1 || rows[0].State != "result" || rows[0].RepeatResultJSON == nil {
					t.Fatalf("the dispatch was not acknowledged with its record: %+v", rows)
				}
			})
		}
	}
}

func TestARepeatRecordOnADelegatedCheckpointGoesOnToTheNextRead(t *testing.T) {
	store := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
	executes := 0
	lookup := runcontext.HostTool{Name: "lookup", Description: "Read one value.", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`), Effect: "read", Taxonomy: &runcontext.ToolTaxonomy{Operation: "read", Effect: "read"}, Execute: func(context.Context, json.RawMessage) (runcontext.ToolResult, error) {
		executes++
		return runcontext.ResultOf([]runcontext.Content{runcontext.TextContent("Value.")}, nil), nil
	}}
	g := gate.New(gate.Options{})
	g.SetOffer(contract.ToolOffer{HostTools: []contract.ToolOfferHostTool{lookup.Declaration()}})
	var events []contract.RunEvent
	run := &adapter.Run{Wire: "openai-compatible", ModelName: "fixture", Thinking: "off", Prompt: "Read the value.", AllowConversation: true, Critic: "off", HostTools: []runcontext.HostTool{lookup}, Gate: g.Calls(), Policy: repeatPolicyOn(), Emit: func(event contract.RunEvent) { events = append(events, event) }, EnrollContext: func(_ context.Context, offer adapter.ContextOffer) error {
		return g.EnrollContext(offer.ID, offer.HostTools)
	}, Context: &runcontext.Input{RunStore: &runcontext.DurableRunInput{Store: store, ID: "run-read"}, CurrentRequest: &runcontext.CurrentRequest{Message: "Read", Input: "Fixed"}}}
	run.Context.Principal = runtimePrincipal(t, 0, runtimeNow, &runtimePrincipalPort{}, func(runcontext.PrincipalAdmissionRead) string { return runtimeAdmit })
	run.Agent = &adapter.Agent{Model: "fixture", Run: func(ctx context.Context, request adapter.AgentRequest, cb adapter.AgentCallbacks) error {
		if cb.ProviderRelease != nil {
			defer cb.ProviderRelease.Acknowledge(cb.ProviderRelease.Invocation)
		}
		name := ""
		for _, tool := range request.Tools {
			if strings.HasPrefix(tool.Description, "lookup:") {
				name = tool.Name
			}
		}
		for _, args := range []string{`{}`, `{"a":1}`} {
			if _, err := cb.Tool(ctx, adapter.AgentToolCall{ID: "wire-" + args, Name: name, Arguments: json.RawMessage(args)}); err != nil {
				return err
			}
		}
		return cb.Event(ctx, adapter.AgentEvent{Type: "message", Text: "Done.", Phase: "final"})
	}}
	if err := native.New().Run(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	head := repeatHeadOf(t, store, "run-read")
	if executes != 2 || head.Version != 4 || head.Status != "complete" || len(head.Durable.Dispatches) != 2 {
		t.Fatalf("executes %d, version %d, status %q, rows %+v, events %+v", executes, head.Version, head.Status, head.Durable.Dispatches, events)
	}
}
