package certification

import (
	"context"
	"encoding/json"
	"fmt"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	agentadapter "github.com/Judgment-Pack/judgment-pack-agent/go/adapter/agent"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/eino"
)

func TestScenario22EveryGoAdapterReplaysCorpus(t *testing.T) {
	root, err := RepositoryRoot()
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		name    string
		adapter Adapter
		agent   bool
		host    AgentHost
	}{{name: "eino", adapter: eino.New()}, {name: "agent", adapter: agentadapter.New(), agent: true, host: StandIn()}} {
		t.Run(row.name, func(t *testing.T) {
			ran := false
			counted := AdapterFunc(func(ctx context.Context, run *Run) error { ran = true; return row.adapter.Run(ctx, run) })
			var bind func(AgentScript) adapter.AgentRun
			if row.agent {
				bind = row.host.Bind
			}
			passed, err := ReplayCorpusAdapter(context.Background(), filepath.Join(root, "corpus"), counted, row.agent, bind)
			if err != nil {
				t.Fatal(err)
			}
			want := 1
			if !row.agent {
				want = 2
			}
			if len(passed) != want || !ran {
				t.Fatalf("replayed %d entries", len(passed))
			}
		})
	}
}

func TestScenario22TamperNamesMember(t *testing.T) {
	root, err := RepositoryRoot()
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "corpus")
	target, err := os.MkdirTemp(root, ".corpus-tamper-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(target) })
	entries, err := os.ReadDir(source)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(target, entry.Name())
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		files, err := os.ReadDir(filepath.Join(source, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range files {
			raw, err := os.ReadFile(filepath.Join(source, entry.Name(), file.Name()))
			if err != nil {
				t.Fatal(err)
			}
			if entry.Name() == "model-policy-refusal" && file.Name() == "expected.json" {
				raw = []byte(strings.Replace(string(raw), `"outcomes":[]`, `"outcomes":[{"type":"proposal"}]`, 1))
			}
			if err := os.WriteFile(filepath.Join(dir, file.Name()), raw, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	_, err = ReplayCorpus(target)
	if err == nil || !strings.Contains(err.Error(), "/outcomes/length differs") {
		t.Fatalf("tamper: %v", err)
	}
}

type corpusMemorySink map[string][]byte

func (s corpusMemorySink) WriteCorpus(_ string, files map[string][]byte) error {
	for name, body := range files {
		s[name] = body
	}
	return nil
}

func TestAgentRecordingAdapterRoundTrip(t *testing.T) {
	ctx := context.Background()
	var requests []corpusRequest
	var events []contract.RunEvent
	toolResults := []map[string]any{}
	calls := map[string]json.RawMessage{}
	source := adapter.RecordAgentRun(func(ctx context.Context, request adapter.AgentRequest, callbacks adapter.AgentCallbacks) error {
		if err := callbacks.Event(ctx, adapter.AgentEvent{Type: "text", ID: "intro", Text: "Inspecting."}); err != nil {
			return err
		}
		name := ""
		for _, tool := range request.Tools {
			if tool.Name == "get_schema" || strings.HasPrefix(tool.Description, "get_schema:") {
				name = tool.Name
			}
		}
		if name == "" {
			return fmt.Errorf("schema tool not offered")
		}
		if _, err := callbacks.Tool(ctx, adapter.AgentToolCall{ID: "schema-call", Name: name, Arguments: json.RawMessage(`{}`)}); err != nil {
			return err
		}
		return callbacks.Event(ctx, adapter.AgentEvent{Type: "message", ID: "intro", Text: "Inspection complete.", Phase: "final"})
	}, func(request adapter.AgentRequest, body json.RawMessage) {
		raw, _ := json.Marshal(request)
		row := corpusRequest{Suffix: "agent.run", Request: raw}
		row.Response.Status = 200
		row.Response.Headers = map[string]string{}
		row.Response.Body = string(body)
		requests = append(requests, row)
	})
	emit := func(event contract.RunEvent) {
		events = append(events, event)
		if call := event.ToolCall; call != nil {
			calls[call.Name] = append(json.RawMessage(nil), call.Args...)
		}
		if result := event.ToolResult; result != nil {
			toolResults = append(toolResults, map[string]any{"name": result.Name, "args": calls[result.Name], "result": map[string]any{"content": []any{map[string]any{"type": "text", "text": result.Text}}, "isError": result.IsError}})
		}
	}
	var offer contract.ToolOffer
	_, vectors, err := ReadVector("tool-offer/scenario-offer.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(vectors[0], &offer); err != nil {
		t.Fatal(err)
	}
	gated := gate.New(gate.Options{Granted: gate.Ceiling, Emit: emit, Runtime: func(_ context.Context, raw json.RawMessage) (json.RawMessage, error) {
		var frame struct {
			ID any `json:"id"`
		}
		if err := json.Unmarshal(raw, &frame); err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{"jsonrpc": "2.0", "id": frame.ID, "result": map[string]any{"content": []any{map[string]any{"type": "text", "text": "Schema response."}}, "isError": false}})
	}})
	gated.SetOffer(offer)
	run := &Run{Agent: &adapter.Agent{Model: "scripted-agent", Run: source}, Prompt: "Inspect.", Critic: "off", AllowConversation: true, Gate: gated.Calls(), Tools: offer.Tools, Emit: emit}
	if err := agentadapter.New().Run(ctx, run); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 1 || len(toolResults) != 1 {
		t.Fatalf("recorded requests/tools: %d/%d", len(requests), len(toolResults))
	}
	var body struct {
		Steps []adapter.AgentRecordingStep `json:"steps"`
	}
	if err := json.Unmarshal([]byte(requests[0].Response.Body), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Steps) != 3 || body.Steps[0].Event == nil || body.Steps[1].Tool == nil || body.Steps[2].Event == nil {
		t.Fatal("callback order lost")
	}
	manifest := runcontext.CorpusManifest{ID: "agent-recorded-round-trip", PackageVersion: "0.8.0", Policy: map[string]any{}, Tolerance: []string{}}
	manifest.Files.Requests = "requests.json"
	manifest.Files.ToolResults = "tool-results.json"
	manifest.Files.Events = "events.json"
	manifest.Files.Checkpoint = "checkpoint.json"
	manifest.Expected.File = "expected.json"
	requestBytes, _ := json.Marshal(requests)
	resultBytes, _ := json.Marshal(toolResults)
	guard, _ := runcontext.NewOutputGuard(runcontext.PolicyMembers{}, func() map[string]int64 { return map[string]int64{} })
	files := corpusMemorySink{}
	written, err := runcontext.WriteCorpusEntry(runcontext.CorpusEntry{Manifest: manifest, Requests: requestBytes, ToolResults: resultBytes, Events: events, Checkpoint: json.RawMessage(`{}`)}, corpusOutputGuard{guard}, files)
	if err != nil || written.Recorded == nil {
		t.Fatalf("record: %v %+v", err, written)
	}
	entry := runcontext.CorpusEntry{Requests: files["requests.json"], ToolResults: files["tool-results.json"], Checkpoint: files["checkpoint.json"]}
	if err := json.Unmarshal(files["manifest.json"], &entry.Manifest); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(files["events.json"], &entry.Events); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(files["expected.json"], &entry.Expected); err != nil {
		t.Fatal(err)
	}
	if err := runCorpusAdapter(ctx, entry, agentadapter.New(), true, StandIn().Bind); err != nil {
		t.Fatal(err)
	}
}

type corpusOutputGuard struct{ guard *runcontext.OutputGuard }

func (g corpusOutputGuard) JSON(raw json.RawMessage, where string, record bool) (json.RawMessage, error) {
	return g.guard.JSON(raw, where, record)
}

func TestSharedAgentCallbackRepresentation(t *testing.T) {
	raw, err := os.ReadFile("../../contract/vectors/corpus/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var vector struct {
		AgentResponse json.RawMessage `json:"agentResponse"`
	}
	if err := json.Unmarshal(raw, &vector); err != nil {
		t.Fatal(err)
	}
	var captured json.RawMessage
	replay := adapter.RecordAgentRun(func(ctx context.Context, _ adapter.AgentRequest, callbacks adapter.AgentCallbacks) error {
		return adapter.ReplayAgentBody(ctx, vector.AgentResponse, callbacks)
	}, func(_ adapter.AgentRequest, body json.RawMessage) { captured = body })
	count := 0
	if err := replay(context.Background(), adapter.AgentRequest{}, adapter.AgentCallbacks{Event: func(context.Context, adapter.AgentEvent) error { count++; return nil }, Tool: func(context.Context, adapter.AgentToolCall) (adapter.AgentToolAnswer, error) {
		count++
		return adapter.AgentToolAnswer{Text: "Schema response."}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	if count != 3 || runcontext.CanonicalJSON(runcontext.MustParseJSON(string(captured))) != runcontext.CanonicalJSON(runcontext.MustParseJSON(string(vector.AgentResponse))) {
		t.Fatalf("callback wire changed: %s", captured)
	}
}
