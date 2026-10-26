package adapter_test

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	native "github.com/Judgment-Pack/judgment-pack-agent/go/adapter/agent"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/eino"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
	"github.com/Judgment-Pack/judgment-pack-agent/go/relay"
	"io"
	"net/http"
	"strings"
	"testing"
)

type originalCaptureSink struct {
	calls int
	files map[string][]byte
}

func (s *originalCaptureSink) WriteCorpus(_ string, files map[string][]byte) error {
	s.calls++
	s.files = files
	return nil
}
func originalCaptureManifest() runcontext.CorpusManifest {
	var m runcontext.CorpusManifest
	_ = json.Unmarshal([]byte(`{"id":"original-live","packageVersion":"0.11.0","policy":{},"files":{"requests":"requests.json","toolResults":"tool-results.json","events":"events.json","checkpoint":"checkpoint.json"},"expected":{"file":"expected.json"},"tolerance":[]}`), &m)
	return m
}
func originalCaptureRun(store runcontext.RunStore, sink *originalCaptureSink) *adapter.Run {
	return &adapter.Run{Wire: "openai-compatible", ModelName: "fixture", Thinking: "off", Prompt: "Answer.", AllowConversation: true, Critic: "off", Context: &runcontext.Input{Objective: "Read one source.", CurrentRequest: &runcontext.CurrentRequest{Message: "Answer", Input: "Fixed"}, RunStore: &runcontext.DurableRunInput{Store: store, ID: "original-live"}, Capture: &runcontext.LiveCaptureConfig{Manifest: originalCaptureManifest(), Sink: sink}}, Emit: func(contract.RunEvent) {}}
}
func TestOriginalLiveCaptureActualEino(t *testing.T) {
	store := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
	sink := &originalCaptureSink{}
	run := originalCaptureRun(store, sink)
	calls := 0
	tier := "off"
	run.Policy = runcontext.PolicyLayer{"version": 2, "routing": map[string]any{"mode": "rules", "tiers": []string{"off"}}}
	run.Route = &runcontext.RouteSelection{Route: "agent", Tier: &tier, Skills: []string{}, Tools: []string{}, Operations: "all", Planning: "withheld"}
	run.Context.Objective = ""
	run.Context.Connection = "provider"
	run.Context.ConnectionRevisions = map[string]string{"provider": "one"}
	run.Model = func(context.Context, string, relay.Request) (*http.Response, error) {
		calls++
		return usageReply(), nil
	}
	var events []contract.RunEvent
	run.Emit = func(e contract.RunEvent) { events = append(events, e) }
	if e := eino.New().Run(context.Background(), run); e != nil {
		t.Fatal(e)
	}
	if sink.calls != 1 || calls != 1 {
		refusals := []contract.RunEvent{}
		for _, event := range events {
			if event.Error != nil || event.Guardrail != nil {
				refusals = append(refusals, event)
			}
		}
		raw, _ := json.Marshal(refusals)
		t.Fatalf("original capture absent: sink=%d physical=%d events=%s", sink.calls, calls, raw)
	}
	view, e := runcontext.ReadRetainedRunHistory(context.Background(), store, "original-live")
	if e != nil {
		t.Fatal(e)
	}
	captured, e := runcontext.ReadCapturedCorpusView(view)
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(string(captured.Entry.Requests), "Done.") {
		t.Fatal("actual response absent")
	}
}
func TestOriginalLiveCaptureActualNativeCallbacks(t *testing.T) {
	store := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
	sink := &originalCaptureSink{}
	run := originalCaptureRun(store, sink)
	run.Model = nil
	tier := "low"
	run.Policy = runcontext.PolicyLayer{"version": 2, "routing": map[string]any{"mode": "rules", "tiers": []string{"low"}}}
	run.Route = &runcontext.RouteSelection{Route: "agent", Tier: &tier, Skills: []string{}, Tools: []string{}, Operations: "all", Planning: "withheld"}
	run.Context.Objective = ""
	run.Context.Connection = "provider"
	run.Context.ConnectionRevisions = map[string]string{"provider": "one"}
	run.Agent = &adapter.Agent{Model: "fixture", Run: func(ctx context.Context, request adapter.AgentRequest, cb adapter.AgentCallbacks) error {
		return cb.Event(ctx, adapter.AgentEvent{Type: "message", ID: "final", Text: "Native captured answer.", Phase: "final"})
	}}
	var events []contract.RunEvent
	run.Emit = func(e contract.RunEvent) { events = append(events, e) }
	if e := native.Run(context.Background(), run); e != nil {
		t.Fatal(e)
	}
	if sink.calls != 1 {
		refusals := []contract.RunEvent{}
		for _, event := range events {
			if event.Error != nil || event.Guardrail != nil {
				refusals = append(refusals, event)
			}
		}
		raw, _ := json.Marshal(refusals)
		t.Fatalf("native original capture absent: %s", raw)
	}
	view, e := runcontext.ReadRetainedRunHistory(context.Background(), store, "original-live")
	if e != nil {
		t.Fatal(e)
	}
	captured, e := runcontext.ReadCapturedCorpusView(view)
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(string(captured.Entry.Requests), "Native captured answer.") {
		t.Fatal("actual callback absent")
	}
}

func TestRepeatAdmissionCountsOnlyPhysicalModelRequests(t *testing.T) {
	store := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
	run := originalCaptureRun(store, &originalCaptureSink{})
	run.Context.Capture = nil
	run.Policy = runcontext.PolicyLayer{"version": 2, "repeatTools": map[string]any{"mode": "on", "stopAt": 1}}
	tool := runcontext.HostTool{Name: "lookup", Description: "Read.", Effect: "read", Taxonomy: &runcontext.ToolTaxonomy{Operation: "read", Effect: "read"}, InputSchema: json.RawMessage(`{"type":"object","properties":{}}`), Execute: func(context.Context, json.RawMessage) (runcontext.ToolResult, error) {
		return runcontext.ReadToolResult(runcontext.MustParseJSON(`{"content":[{"type":"text","text":"Value."}]}`)), nil
	}}
	g := gate.New(gate.Options{})
	g.SetOffer(contract.ToolOffer{Tools: []contract.ToolOfferRuntimeTool{}, HostTools: []contract.ToolOfferHostTool{tool.Declaration()}})
	run.Gate = g.Calls()
	run.HostTools = []runcontext.HostTool{tool}
	calls := 0
	run.Model = func(context.Context, string, relay.Request) (*http.Response, error) {
		calls++
		body := fmt.Sprintf(`{"id":"answer","object":"chat.completion","model":"fixture","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"wire-%d","type":"function","function":{"name":"lookup","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":2,"completion_tokens":1}}`, calls)
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	}
	if err := eino.New().Run(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	cp := usageHead(t, store, "original-live")
	if calls != 2 || cp.Get("status").Str() != "stopped" {
		t.Fatalf("physical calls=%d state=%s", calls, cp.Get("status").Str())
	}
	if cp.Get("used").Num() != float64(calls) {
		t.Fatalf("blocked physical call consumed logical request: used=%v physical=%d", cp.Get("used").Num(), calls)
	}
	requests := 0.0
	for _, row := range cp.Get("usage").Get("rows").Items() {
		if row.Get("source").Str() == "model" {
			requests += row.Get("requests").Num()
		}
	}
	if requests != float64(calls) {
		t.Fatalf("blocked physical call increased model request usage: usage=%v physical=%d", requests, calls)
	}
}

func replayOriginalCapture(t *testing.T, entry runcontext.CorpusEntry, nativeRun bool, withTool bool) {
	t.Helper()
	var frames []struct {
		Suffix   string `json:"suffix"`
		Response struct {
			Status  int               `json:"status"`
			Headers map[string]string `json:"headers"`
			Body    string            `json:"body"`
		} `json:"response"`
	}
	if e := json.Unmarshal(entry.Requests, &frames); e != nil {
		t.Fatal(e)
	}
	at := 0
	events := []contract.RunEvent{}
	replay := &adapter.Run{Wire: "openai-compatible", ModelName: "fixture", Thinking: "off", Prompt: "Answer.", AllowConversation: true, Critic: "off", Emit: func(e contract.RunEvent) { events = append(events, e) }}
	tools, e := runcontext.NewScriptedCorpusTools(entry)
	if e != nil {
		t.Fatal(e)
	}
	if withTool {
		tool := runcontext.HostTool{Name: "lookup", Description: "Read.", Effect: "read", Taxonomy: &runcontext.ToolTaxonomy{Operation: "read", Effect: "read"}, InputSchema: json.RawMessage(`{"type":"object","properties":{}}`), Execute: func(_ context.Context, args json.RawMessage) (runcontext.ToolResult, error) {
			return runcontext.ReadToolResult(runcontext.MustParseJSON(string(tools.Call("lookup", args)))), nil
		}}
		g := gate.New(gate.Options{})
		g.SetOffer(contract.ToolOffer{Tools: []contract.ToolOfferRuntimeTool{}, HostTools: []contract.ToolOfferHostTool{tool.Declaration()}})
		replay.Gate = g.Calls()
		replay.HostTools = []runcontext.HostTool{tool}
	}
	if nativeRun {
		replay.Agent = &adapter.Agent{Model: "fixture", Run: func(ctx context.Context, _ adapter.AgentRequest, callbacks adapter.AgentCallbacks) error {
			if at >= len(frames) {
				return fmt.Errorf("request recording exhausted")
			}
			frame := frames[at]
			at++
			return adapter.ReplayAgentBody(ctx, json.RawMessage(frame.Response.Body), callbacks)
		}}
		e = native.Run(context.Background(), replay)
	} else {
		replay.Model = func(context.Context, string, relay.Request) (*http.Response, error) {
			if at >= len(frames) {
				return nil, fmt.Errorf("request recording exhausted")
			}
			frame := frames[at]
			at++
			headers := http.Header{}
			for name, value := range frame.Response.Headers {
				headers.Set(name, value)
			}
			return &http.Response{StatusCode: frame.Response.Status, Header: headers, Body: io.NopCloser(strings.NewReader(frame.Response.Body))}, nil
		}
		e = eino.New().Run(context.Background(), replay)
	}
	if e != nil {
		t.Fatal(e)
	}
	if at != len(frames) {
		t.Fatalf("request recording not exhausted: used=%d recorded=%d", at, len(frames))
	}
	if e = tools.Complete(); e != nil {
		t.Fatal(e)
	}
	actual, e := runcontext.CorpusExpected(events)
	if e != nil {
		t.Fatal(e)
	}
	if e = runcontext.CompareCorpus(entry.Expected, actual, entry.Manifest.Tolerance); e != nil {
		t.Fatal("fresh adapter emitted different outcomes", e)
	}
}
func TestOriginalLiveCaptureDefaultModelAndFreshReplay(t *testing.T) {
	store := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
	sink := &originalCaptureSink{}
	run := originalCaptureRun(store, sink)
	run.Context.Objective = ""
	run.Model = func(context.Context, string, relay.Request) (*http.Response, error) { return usageReply(), nil }
	if e := eino.New().Run(context.Background(), run); e != nil {
		t.Fatal(e)
	}
	if sink.calls != 1 {
		t.Fatal("default model capture absent")
	}
	retained, e := runcontext.ReadRetainedRunHistory(context.Background(), store, "original-live")
	if e != nil {
		t.Fatal(e)
	}
	captured, e := runcontext.ReadCapturedCorpusView(retained)
	if e != nil {
		t.Fatal(e)
	}
	replayOriginalCapture(t, captured.Entry, false, false)
}
func originalNativeToolCapture(t *testing.T) (*runcontext.MemoryRunStore, *runcontext.RetainedRunHistory) {
	t.Helper()
	store := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
	sink := &originalCaptureSink{}
	run := originalCaptureRun(store, sink)
	run.Context.Objective = ""
	tool := runcontext.HostTool{Name: "lookup", Description: "Read.", Effect: "read", Taxonomy: &runcontext.ToolTaxonomy{Operation: "read", Effect: "read"}, InputSchema: json.RawMessage(`{"type":"object","properties":{}}`), Execute: func(context.Context, json.RawMessage) (runcontext.ToolResult, error) {
		return runcontext.ReadToolResult(runcontext.MustParseJSON(`{"content":[{"type":"text","text":"Value."}]}`)), nil
	}}
	g := gate.New(gate.Options{})
	g.SetOffer(contract.ToolOffer{Tools: []contract.ToolOfferRuntimeTool{}, HostTools: []contract.ToolOfferHostTool{tool.Declaration()}})
	run.Gate = g.Calls()
	run.HostTools = []runcontext.HostTool{tool}
	run.Agent = &adapter.Agent{Model: "fixture", Run: func(ctx context.Context, request adapter.AgentRequest, cb adapter.AgentCallbacks) error {
		alias := ""
		for _, offered := range request.Tools {
			if strings.HasPrefix(offered.Description, "lookup:") {
				alias = offered.Name
			}
		}
		if alias == "" {
			return fmt.Errorf("fixture lookup not offered")
		}
		if _, e := cb.Tool(ctx, adapter.AgentToolCall{ID: "lookup-call", Name: alias, Arguments: json.RawMessage(`{}`)}); e != nil {
			return e
		}
		return cb.Event(ctx, adapter.AgentEvent{Type: "message", ID: "final", Text: "Native captured answer.", Phase: "final"})
	}}
	if e := native.Run(context.Background(), run); e != nil {
		t.Fatal(e)
	}
	if sink.calls != 1 {
		t.Fatal("native tool capture absent")
	}
	retained, e := runcontext.ReadRetainedRunHistory(context.Background(), store, "original-live")
	if e != nil {
		t.Fatal(e)
	}
	return store, retained
}
func TestOriginalLiveCaptureNativeToolAndFreshReplay(t *testing.T) {
	_, retained := originalNativeToolCapture(t)
	captured, e := runcontext.ReadCapturedCorpusView(retained)
	if e != nil {
		t.Fatal(e)
	}
	replayOriginalCapture(t, captured.Entry, true, true)
}
