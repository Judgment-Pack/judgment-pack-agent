package certification

// Scenario 19 (ADR-0017): identity on the offer, every source event and the figures; a changed
// identity withheld; and the first service act asked even where the host policy says allow.

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

type identityFile struct {
	Request         string                     `json:"request"`
	Identity        contract.ToolOfferIdentity `json:"identity"`
	ChangedIdentity contract.ToolOfferIdentity `json:"changedIdentity"`
	Read            struct {
		Name, Description, Result string
		Arguments                 json.RawMessage `json:"arguments"`
	} `json:"read"`
	Act struct {
		Name, Description string
		Arguments         json.RawMessage `json:"arguments"`
	} `json:"act"`
	Turns   []sourcesTurn `json:"turns"`
	Changed []sourcesTurn `json:"changed"`
	Service []sourcesTurn `json:"service"`
}

var AgentIdentityNames = []string{"19 agent offer", "19 agent events", "19 agent figures", "19 agent changed", "19 agent service approval"}

func agentIdentityChecks(ctx context.Context, server *Server, host AgentHost, _ func(Adapter) Adapter, inner Adapter) ([]Finding, error) {
	var file identityFile
	if err := ScenarioFile("identity", &file); err != nil {
		return nil, err
	}
	state := &identityHost{}
	normalTools := identityTools(file, file.Identity, state)
	scenario, err := openScenario(ctx, server, ScenarioEndpoint{Scenario: "identity", AnswerAs: "stream"}, normalTools, true)
	if err != nil {
		return nil, err
	}
	type played struct {
		watch  *monitor
		events []contract.RunEvent
	}
	play := func(tools []runcontext.HostTool, turns []sourcesTurn) (played, error) {
		watch := &monitor{}
		run := &Run{ModelName: parentModel, Gate: scenario.gate.Calls(), Tools: scenario.offer.Tools, Prompt: file.Request, AllowConversation: true, Interactive: true,
			HostTools: tools, Context: requestInput(file.Request), Emit: scenario.emit,
			Agent: &adapter.Agent{Model: parentModel, Run: watch.wrap(host.Bind(func(adapter.AgentRequest, int) AgentPlay { return AgentPlay{Steps: sourcesSteps(turns)} }))}}
		if err := inner.Run(ctx, run); err != nil {
			return played{}, err
		}
		return played{watch: watch, events: scenario.take()}, nil
	}
	normal, err := play(normalTools, file.Turns)
	if err != nil {
		return nil, err
	}
	if err := scenario.session.Advance(ctx); err != nil {
		return nil, err
	}
	changed, err := play(identityTools(file, file.ChangedIdentity, state), file.Changed)
	if err != nil {
		return nil, err
	}
	if err := scenario.session.Advance(ctx); err != nil {
		return nil, err
	}
	service, err := play(identityTools(file, file.Identity, state), file.Service)
	if err != nil {
		return nil, err
	}

	var out []Finding
	f := &findings{check: "19 agent offer"}
	calls := normal.watch.admitted()
	if len(calls) != 1 {
		f.fail("%d agent runs", len(calls))
	} else {
		found := false
		for _, tool := range agentOffered(calls[0].Request) {
			if tool.Name == "expenses__"+file.Read.Name {
				found = strings.Contains(tool.Description, "\nruns as: "+file.Identity.Label+"\n")
			}
		}
		if !found {
			f.fail("no identity line")
		}
	}
	out = append(out, f.list...)
	f = &findings{check: "19 agent events"}
	count := 0
	for _, event := range normal.events {
		if event.ToolCall != nil && event.ToolCall.Name == "expenses__"+file.Read.Name {
			count++
			if event.ToolCall.Source == nil || event.ToolCall.Source.Identity == nil || *event.ToolCall.Source.Identity != file.Identity {
				f.fail("call identity")
			}
		}
		if event.ToolResult != nil && event.ToolResult.Name == "expenses__"+file.Read.Name {
			count++
			if event.ToolResult.Source == nil || event.ToolResult.Source.Identity == nil || *event.ToolResult.Source.Identity != file.Identity {
				f.fail("result identity")
			}
		}
	}
	if count != 2 {
		f.fail("%d events", count)
	}
	out = append(out, f.list...)
	f = &findings{check: "19 agent figures"}
	cp := lastCheckpointOf(normal.events)
	if cp == nil {
		f.fail("no checkpoint")
	} else {
		found := false
		for _, row := range cp.Sources {
			if row.Source == "expenses" && row.Calls == 1 && len(row.Identities) == 1 && row.Identities[0].Identity == file.Identity && row.Identities[0].Calls == 1 {
				found = true
			}
		}
		if !found {
			f.fail("figures %+v", cp.Sources)
		}
	}
	out = append(out, f.list...)
	f = &findings{check: "19 agent changed"}
	found := false
	for _, event := range changed.events {
		if event.Guardrail != nil && event.Guardrail.Reason != nil && *event.Guardrail.Reason == "identity changed" {
			found = true
		}
	}
	if !found {
		f.fail("no refusal")
	}
	out = append(out, f.list...)
	f = &findings{check: "19 agent service approval"}
	downgraded, asked := false, false
	for _, event := range service.events {
		if event.Guardrail != nil && event.Guardrail.Reason != nil && *event.Guardrail.Reason == "approval downgraded" {
			downgraded = true
		}
		if event.ApprovalRequested != nil {
			asked = true
		}
	}
	if !downgraded || !asked || state.read != 1 || state.act != 0 {
		f.fail("downgraded %t asked %t ran %d/%d", downgraded, asked, state.read, state.act)
	}
	out = append(out, f.list...)
	return out, nil
}

type identityHost struct{ read, act int }

func identityTools(file identityFile, current contract.ToolOfferIdentity, host *identityHost) []runcontext.HostTool {
	source := func() *runcontext.ToolSource {
		identity := file.Identity
		return &runcontext.ToolSource{Kind: "gateway", ID: "expenses", Trust: "connected", Version: "4", Identity: &identity,
			CurrentIdentity: func(context.Context) (contract.ToolOfferIdentity, error) { return current, nil }}
	}
	return []runcontext.HostTool{
		{Name: file.Read.Name, Description: file.Read.Description, InputSchema: json.RawMessage(`{"type":"object"}`), EffectClass: "read", Source: source(),
			Execute: func(context.Context, json.RawMessage) (runcontext.ToolResult, error) {
				host.read++
				return runcontext.ResultOf([]runcontext.Content{runcontext.TextContent(file.Read.Result)}, nil), nil
			}},
		{Name: file.Act.Name, Description: file.Act.Description, InputSchema: json.RawMessage(`{"type":"object"}`), EffectClass: "act", Source: source(),
			Approve: func(runcontext.ApprovalRequest) string { return "allow" }, Execute: func(context.Context, json.RawMessage) (runcontext.ToolResult, error) {
				host.act++
				return runcontext.ResultOf([]runcontext.Content{runcontext.TextContent("submitted")}, nil), nil
			}},
	}
}

var Identity = ScenarioCheck{
	Scenario: "identity",
	Names:    []string{"19 offer", "19 events", "19 figures", "19 changed", "19 service approval"},
	Run: func(ctx context.Context, server *Server, answerAs string, adapter Adapter) ([]Finding, error) {
		var file identityFile
		if err := ScenarioFile("identity", &file); err != nil {
			return nil, err
		}
		host := &identityHost{}
		normalTools := identityTools(file, file.Identity, host)
		scenario, err := openScenario(ctx, server, ScenarioEndpoint{Scenario: "identity", AnswerAs: answerAs}, normalTools, true)
		if err != nil {
			return nil, err
		}
		play := func(tools []runcontext.HostTool) (*ScenarioRun, error) {
			return scenario.runOn(ctx, adapter, &Run{Prompt: file.Request, AllowConversation: true, Interactive: true, HostTools: tools, Context: requestInput(file.Request)})
		}
		normal, err := play(normalTools)
		if err != nil {
			return nil, err
		}
		if err := scenario.session.Advance(ctx); err != nil {
			return nil, err
		}
		changed, err := play(identityTools(file, file.ChangedIdentity, host))
		if err != nil {
			return nil, err
		}
		if err := scenario.session.Advance(ctx); err != nil {
			return nil, err
		}
		service, err := play(identityTools(file, file.Identity, host))
		if err != nil {
			return nil, err
		}

		var out []Finding
		f := &findings{check: "19 offer"}
		if len(normal.Requests) == 0 {
			f.fail("no request")
		} else {
			found := false
			for _, tool := range shownTools(normal.Requests[0]) {
				if tool.Name == "expenses__"+file.Read.Name {
					found = strings.Contains(tool.Description, "\nruns as: "+file.Identity.Label+"\n") && onlyFramed(tool.Description, "runs as: "+file.Identity.Label)
				}
			}
			if !found {
				f.fail("the identity line was not framed on the offer")
			}
		}
		out = append(out, f.list...)

		f = &findings{check: "19 events"}
		count := 0
		for _, event := range normal.Events {
			var source *contract.ToolOfferSourceRecord
			if event.ToolCall != nil && event.ToolCall.Name == "expenses__"+file.Read.Name {
				source = event.ToolCall.Source
				count++
			}
			if event.ToolResult != nil && event.ToolResult.Name == "expenses__"+file.Read.Name {
				source = event.ToolResult.Source
				count++
			}
			if source != nil && (source.Identity == nil || *source.Identity != file.Identity) {
				f.fail("event identity %+v", source.Identity)
			}
		}
		if count != 2 {
			f.fail("%d source events", count)
		}
		out = append(out, f.list...)

		f = &findings{check: "19 figures"}
		checkpoint := lastCheckpointOf(normal.Events)
		if checkpoint == nil {
			f.fail("no checkpoint")
		} else {
			found := false
			for _, row := range checkpoint.Sources {
				if row.Source == "expenses" && row.Calls == 1 && len(row.Identities) == 1 && row.Identities[0].Identity == file.Identity && row.Identities[0].Calls == 1 {
					found = true
				}
			}
			if !found {
				f.fail("figures %+v", checkpoint.Sources)
			}
		}
		out = append(out, f.list...)

		f = &findings{check: "19 changed"}
		changedSeen := false
		for _, event := range changed.Events {
			if event.Guardrail != nil && event.Guardrail.Reason != nil && *event.Guardrail.Reason == "identity changed" {
				changedSeen = true
			}
		}
		if !changedSeen {
			f.fail("no identity changed guardrail")
		}
		out = append(out, f.list...)

		f = &findings{check: "19 service approval"}
		downgraded, asked := false, false
		for _, event := range service.Events {
			if event.Guardrail != nil && event.Guardrail.Reason != nil && *event.Guardrail.Reason == "approval downgraded" {
				downgraded = true
			}
			if event.ApprovalRequested != nil && event.ApprovalRequested.Source.Identity != nil && *event.ApprovalRequested.Source.Identity == file.Identity {
				asked = true
			}
		}
		if !downgraded || !asked || host.read != 1 || host.act != 0 {
			f.fail("downgraded %t, asked %t, ran %d/%d", downgraded, asked, host.read, host.act)
		}
		out = append(out, f.list...)
		return out, nil
	},
}
