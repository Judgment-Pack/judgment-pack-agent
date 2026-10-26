package certification

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
)

// NativeTerminationCheck is distinct from the canonical callback-refusal project claim.
// It is opt-in: a host must run its actual early-rejecting native binding to claim this outcome.
func NativeTerminationCheck(ctx context.Context, _ *Server, host AgentHost, _ func(Adapter) Adapter, inner Adapter) ([]Finding, error) {
	f := &findings{check: "agent project native termination"}
	if host.Bind == nil || host.Name == "" {
		f.fail("no actual named native host binding was supplied")
		return f.list, nil
	}
	store := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
	watch := &monitor{nativeTermination: true}
	executed := 0
	var events []contract.RunEvent
	read := runcontext.HostTool{Name: readTool, Description: "Read the supplied fixture note.", Effect: "read", Taxonomy: &runcontext.ToolTaxonomy{Operation: "read", Effect: "read"}, InputSchema: json.RawMessage(`{"type":"object","properties":{"note":{"type":"integer"}},"required":["note"],"additionalProperties":false}`), Execute: func(context.Context, json.RawMessage) (runcontext.ToolResult, error) {
		executed++
		return runcontext.ResultOf([]runcontext.Content{runcontext.TextContent("Fixture note read.")}, nil), nil
	}}
	g := gate.New(gate.Options{})
	g.SetOffer(contract.ToolOffer{HostTools: []contract.ToolOfferHostTool{read.Declaration()}})
	run := &Run{Prompt: agentRequest, ModelName: parentModel, Critic: "off", AllowConversation: true, Interactive: true, Gate: g.Calls(), HostTools: []runcontext.HostTool{read}, Host: runcontext.HostFuncs{ProjectInstructionsFunc: func(context.Context) (string, error) { return Injected, nil }}, Context: &runcontext.Input{RunStore: &runcontext.DurableRunInput{Store: store, ID: "native-project"}, CurrentRequest: &runcontext.CurrentRequest{Message: agentRequest, Input: agentRequest}}, Emit: func(event contract.RunEvent) { events = append(events, event) }, EnrollContext: func(_ context.Context, offer adapter.ContextOffer) error {
		return g.EnrollContext(offer.ID, offer.HostTools)
	}}
	script := func(adapter.AgentRequest, int) AgentPlay {
		return AgentPlay{Steps: []AgentStep{call(readTool, map[string]any{"note": 1}), call("write_file", map[string]any{"path": "packs/expense.pack.json"}), say("This clean continuation must never run.")}}
	}
	run.Agent = &adapter.Agent{Model: parentModel, Run: watch.wrap(host.Bind(script))}
	if err := inner.Run(ctx, run); err != nil {
		return nil, err
	}
	calls := watch.all()
	if len(calls) != 1 {
		f.fail("the actual binding was invoked %d times", len(calls))
		return f.list, nil
	}
	observed := calls[0]
	if observed.Err == nil || observed.NativeTermination == nil {
		f.fail("the invoked native host supplied no exact failed-call and release evidence")
		return f.list, nil
	}
	proof := observed.NativeTermination
	requestRaw, _ := json.Marshal(observed.Request)
	requestDigest := sha256.Sum256(requestRaw)
	if proof.RequestDigest != "sha256:"+hex.EncodeToString(requestDigest[:]) {
		f.fail("termination evidence request identity changed")
	}
	if proof.Call.ID != "call-item-2" || proof.Call.Name != "write_file" || string(proof.Call.Arguments) != `{"path":"packs/expense.pack.json"}` {
		f.fail("termination evidence described a different protocol call")
	}
	if len(observed.Answers) != 1 || observed.Answers[0].Tool != readTool || observed.Answers[0].IsError || executed != 1 {
		f.fail("the offered read or zero callback for the unoffered call was not observed")
	}
	for _, event := range events {
		if event.Message != nil || event.Guardrail != nil && event.Guardrail.Tool == "write_file" {
			f.fail("early termination was synthesized into canonical refusal or clean continuation")
		}
	}
	block := runcontext.ProjectInstructionsHeader + "\n" + Injected + "\nEND PROJECT INSTRUCTIONS"
	if !strings.Contains(observed.Request.Instructions, block) || strings.Contains(observed.Request.Prompt, Injected) {
		f.fail("project instructions were not held in the canonical instruction layer")
	}
	head, err := store.Load(ctx, "native-project")
	if err != nil {
		return nil, err
	}
	if head == nil {
		f.fail("no durable state was preserved")
		return f.list, nil
	}
	value, err := runcontext.ParseJSON(head.Checkpoint)
	if err != nil {
		return nil, err
	}
	cp, err := runcontext.ReadExecution(value)
	if err != nil {
		return nil, err
	}
	if cp.Durable == nil || len(cp.Durable.Dispatches) < 2 {
		f.fail("the invocation and earlier offered read were not retained")
	} else {
		for _, row := range cp.Durable.Dispatches {
			if row.Tool == "write_file" {
				f.fail("the unoffered invocation gained an executable durable claim")
			}
		}
	}
	return f.list, nil
}
