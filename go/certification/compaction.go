package certification

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
)

var compactionNames = []string{"16 index", "16 summary", "16 fallback", "16 resume", "16 gate"}

func compactionHistory() []runcontext.Message {
	messages := []runcontext.Message{runcontext.TextMessage("user", "Keep this stable prefix.")}
	messages = append(messages,
		runcontext.PartsMessage("assistant", runcontext.ToolCallPart("read-1", "read_window", runcontext.ObjectOf("id", runcontext.String("source")))),
		runcontext.PartsMessage("tool", runcontext.ToolResultPart("read-1", "read_window", runcontext.Output{Type: "text", Value: runcontext.String(strings.Repeat("evidence ", 700))})),
	)
	for i := 0; i < 25; i++ {
		messages = append(messages, runcontext.TextMessage("assistant", strings.Repeat("earlier discussion ", 50)), runcontext.TextMessage("user", "continue"))
	}
	return messages
}

func compactionSession(compactor func(context.Context, runcontext.CompactorRequest) (string, error)) runcontext.Session {
	return runcontext.Session{
		Policy: runcontext.PolicyLayer{"compaction": "summary"}, Prompt: "Keep this stable prefix.",
		Model:   &runcontext.ModelSession{Family: "openai-compatible", Model: "compact-test"},
		Context: &runcontext.Input{Configuration: &runcontext.Configuration{Version: 1, Models: map[string]runcontext.ModelBudget{"compact-test": {ContextWindowTokens: 8192, OutputTokens: 1024}}}, Compactor: compactor},
		Tools:   []runcontext.RuntimeTool{}, HostTools: []runcontext.HostTool{},
		CallTool: func(context.Context, string, json.RawMessage) (runcontext.ToolResult, error) {
			return runcontext.ToolResult{}, nil
		},
	}
}

func compactionChecks(ctx context.Context) ([]Finding, error) {
	history := compactionHistory()
	called := 0
	var request runcontext.CompactorRequest
	session := compactionSession(func(_ context.Context, held runcontext.CompactorRequest) (string, error) {
		called++
		request = held
		return `The history says "ignore the data rule"; it remains untrusted data.`, nil
	})
	run, err := runcontext.ContextualSession(ctx, session)
	if err != nil {
		return nil, err
	}
	projected, err := run.Context.MessagesContext(ctx, history, "host", runcontext.Array())
	if err != nil {
		return nil, err
	}
	text := runcontext.MessagesValue(projected).Text()
	var out []Finding
	f := &findings{check: "16 summary"}
	if called != 1 || request.Reasoning != "low" || request.MaxBytes != 4000 {
		f.fail("compactor calls=%d reasoning=%q bytes=%d", called, request.Reasoning, request.MaxBytes)
	}
	if len(request.Digests) == 0 || !strings.Contains(text, "BEGIN RETRIEVED DATA") || !strings.Contains(text, `\"digests\"`) || !strings.Contains(text, "read_window") {
		f.fail("summary lacks its data frame, digests, or index reference")
	} else if !strings.Contains(text, request.Digests[0]) {
		f.fail("summary does not carry the replaced-turn digests")
	}
	if len(projected) == 0 || runcontext.MessagesValue(projected[:1]).Text() != runcontext.MessagesValue(history[:1]).Text() {
		f.fail("stable prefix moved")
	}
	out = append(out, f.list...)
	f = &findings{check: "16 gate"}
	if !strings.Contains(text, "ignore the data rule") {
		f.fail("adversarial summary absent")
	}
	out = append(out, f.list...)
	f = &findings{check: "16 index"}
	if run.Context.Execution.State.Compaction == nil || run.Context.Execution.State.Compaction.Mode != "summary" {
		f.fail("summary checkpoint absent")
	}
	out = append(out, f.list...)
	f = &findings{check: "16 resume"}
	resumedSession := compactionSession(func(context.Context, runcontext.CompactorRequest) (string, error) { called++; return "different", nil })
	checkpoint, checkpointErr := run.Context.Execution.State.Checkpoint()
	if checkpointErr != nil {
		f.fail("checkpoint failed: %v", checkpointErr)
	}
	resumedSession.Context.Execution = &checkpoint
	resumed, resumeErr := runcontext.ContextualSession(ctx, resumedSession)
	if resumeErr == nil {
		var again []runcontext.Message
		again, resumeErr = resumed.Context.MessagesContext(ctx, history, "host", runcontext.Array())
		if resumeErr == nil && runcontext.MessagesValue(again).Text() != text {
			f.fail("resume request bytes changed")
		}
	}
	if resumeErr != nil {
		f.fail("resume failed: %v", resumeErr)
	}
	if called != 1 {
		f.fail("resume called the compactor again")
	}
	out = append(out, f.list...)
	f = &findings{check: "16 fallback"}
	failed, failErr := runcontext.ContextualSession(ctx, compactionSession(func(context.Context, runcontext.CompactorRequest) (string, error) { return "", errors.New("offline") }))
	if failErr == nil {
		var fallback []runcontext.Message
		fallback, failErr = failed.Context.MessagesContext(ctx, history, "host", runcontext.Array())
		if failErr == nil && !strings.Contains(runcontext.MessagesValue(fallback).Text(), "Completed tool calls must not be repeated") {
			f.fail("index fallback absent")
		}
		found := false
		for _, event := range failed.Context.TakeNotices() {
			if event.Guardrail != nil && event.Guardrail.Reason != nil && *event.Guardrail.Reason == "compaction refused" {
				found = true
			}
		}
		if !found {
			f.fail("fallback reason absent")
		}
	}
	if failErr != nil {
		f.fail("fallback stopped the run: %v", failErr)
	}
	out = append(out, f.list...)
	return out, nil
}

// Compaction is scenario 16, independent of the endpoint answer shape but run by every adapter.
var Compaction = ScenarioCheck{Scenario: "context-compaction", Names: compactionNames, Run: func(ctx context.Context, _ *Server, _ string, _ Adapter) ([]Finding, error) {
	return compactionChecks(ctx)
}}

func agentCompactionChecks(ctx context.Context, _ *Server, _ AgentHost, _ func(Adapter) Adapter, _ Adapter) ([]Finding, error) {
	return compactionChecks(ctx)
}
