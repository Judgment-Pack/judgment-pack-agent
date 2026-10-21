package certification

import (
	"context"
	"encoding/json"
	"strings"
	"sync"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

var steerNames = []string{"21 steer boundary"}
var AgentSteerNames = []string{"21 agent steer boundary"}

func certifySteerPauseAndResume(ctx context.Context, f *findings) error {
	queue := make(chan runcontext.SteerInput, 1)
	queue <- runcontext.SteerInput{Text: "not the decision"}
	opened, err := runcontext.ContextualSession(ctx, runcontext.Session{Prompt: "task", AllowConversation: true, Context: &runcontext.Input{CurrentRequest: &runcontext.CurrentRequest{Message: "task", Input: "task"}, Steer: queue}, Agent: &runcontext.AgentSession{Model: "agent"}})
	if err != nil {
		return err
	}
	defer opened.Context.Close()
	opened.Context.Execution.State.Approval = &contract.CheckpointApproval{State: "asked"}
	opened.Context.Execution.State.Steers = &contract.ProvenanceSteerReport{Queued: []contract.ProvenanceQueuedSteer{{Text: "accepted before pause", Bytes: 21}}, Delivered: []contract.ProvenanceDeliveredSteer{}}
	refused := opened.Context.Execution.QueueSteers(*opened.Context.Policy)
	if len(refused) != 1 || refused[0].Guardrail == nil || refused[0].Guardrail.Reason == nil || *refused[0].Guardrail.Reason != "steer refused" || len(opened.Context.Execution.State.Steers.Queued) != 1 {
		f.fail("pause refusal or queued steer: %#v, %#v", refused, opened.Context.Execution.State.Steers)
	}
	opened.Context.Execution.State.Approval.State = "refused"
	text, delivered := opened.Context.Execution.TakeSteers(opened.Context.Marker, 2, *opened.Context.Policy)
	if !strings.Contains(text, "\naccepted before pause\n") || len(delivered) != 1 || delivered[0].Steered == nil || delivered[0].Steered.AtRequest != 2 {
		f.fail("resume did not deliver the checkpointed queue: %q, %#v", text, delivered)
	}
	return nil
}

func steerChecks(ctx context.Context, server *Server, answerAs string, inner Adapter) ([]Finding, error) {
	var file scenarioFile
	if err := ScenarioFile("chat-host-tools", &file); err != nil {
		return nil, err
	}
	ledger := &HostLedger{}
	hostTools := HostToolsOf(file.HostTools, ledger)
	host, err := openScenario(ctx, server, ScenarioEndpoint{Scenario: "chat-host-tools", AnswerAs: answerAs}, hostTools, true)
	if err != nil {
		return nil, err
	}
	queue := make(chan runcontext.SteerInput, 2)
	var once sync.Once
	host.watch(func(event contract.RunEvent) {
		if event.ToolCall != nil {
			once.Do(func() { queue <- runcontext.SteerInput{Text: "first"}; queue <- runcontext.SteerInput{Text: "second"} })
		}
	})
	input := requestInput(file.Request)
	input.Steer = queue
	ran, err := host.runOn(ctx, inner, &Run{Prompt: file.Request, AllowConversation: true, HostTools: hostTools, Context: input})
	if err != nil {
		return nil, err
	}
	f := findings{check: steerNames[0]}
	if len(ran.Requests) < 2 {
		f.fail("%d requests, want at least two", len(ran.Requests))
		return f.list, nil
	}
	first, second := string(ran.Requests[0].Body), string(ran.Requests[1].Body)
	if strings.Contains(first, "the person's new instruction") || !strings.Contains(second, "the person's new instruction") || strings.Index(second, "first") >= strings.Index(second, "second") {
		f.fail("steers were not framed once in order at the second boundary")
	}
	steered := ofType(ran.Events, func(e contract.RunEvent) *contract.RunEventSteered { return e.Steered })
	if len(steered) != 2 || steered[0].AtRequest != 2 || steered[1].AtRequest != 2 {
		f.fail("steered events: %#v", steered)
	}
	var a, b struct {
		Messages []struct{ Role, Content string } `json:"messages"`
	}
	_ = json.Unmarshal(ran.Requests[0].Body, &a)
	_ = json.Unmarshal(ran.Requests[1].Body, &b)
	if len(a.Messages) > 0 && len(b.Messages) > 0 && a.Messages[0].Role == "system" && b.Messages[0].Role == "system" && a.Messages[0].Content != b.Messages[0].Content {
		f.fail("stable system prefix changed")
	}
	if err := certifySteerPauseAndResume(ctx, &f); err != nil {
		return nil, err
	}
	return f.list, nil
}

var Steering = ScenarioCheck{Scenario: "steering", Names: steerNames, Run: steerChecks}

func agentSteerChecks(ctx context.Context, _ *Server, host AgentHost, with func(Adapter) Adapter, inner Adapter) ([]Finding, error) {
	w, err := newAgentWorld(ctx, host, false)
	if err != nil {
		return nil, err
	}
	queue := make(chan runcontext.SteerInput, 2)
	script := func(_ adapter.AgentRequest, number int) AgentPlay {
		if number == 1 {
			queue <- runcontext.SteerInput{Text: "Use the new title."}
			return AgentPlay{Steps: []AgentStep{say("not a proposal")}}
		}
		return AgentPlay{Steps: []AgentStep{say("```json\n{\"proposal\":{\"document\":{\"id\":\"steered\"},\"unknowns\":[]}}\n```")}}
	}
	w.run(ctx, with, inner, script, nil, nil, func(run *Run) {
		run.AllowConversation = false
		run.Interactive = false
		run.Context.Steer = queue
	})
	calls := w.watch.admitted()
	events := w.take()
	f := findings{check: AgentSteerNames[0]}
	if len(calls) != 2 {
		f.fail("%d invocations, want two", len(calls))
	} else {
		if strings.Contains(calls[0].Request.Prompt, "the person's new instruction") || !strings.Contains(calls[1].Request.Prompt, "from the person's new instruction") {
			f.fail("the steer was not placed on the next invocation's prompt")
		}
		if calls[0].Request.Instructions != calls[1].Request.Instructions {
			f.fail("the stable instructions changed")
		}
	}
	steered := ofType(events, func(e contract.RunEvent) *contract.RunEventSteered { return e.Steered })
	if len(steered) != 1 || steered[0].Bytes != 18 || steered[0].AtRequest != 2 {
		f.fail("steered events: %#v", steered)
	}
	if err := certifySteerPauseAndResume(ctx, &f); err != nil {
		return nil, err
	}
	return f.list, nil
}
