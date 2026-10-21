package certification

// Scenario 22's repository-corpus replay. Inputs come only from the six files in each entry; no
// adapter or provider capability is accepted by this path, so a replay cannot reach a live provider.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
	"github.com/Judgment-Pack/judgment-pack-agent/go/relay"
)

var CorpusScenarioNames = []string{"22 replay", "22 tamper", "22 round-trip"}
var AgentCorpusNames = []string{"22 agent replay", "22 agent tamper", "22 agent round-trip"}

var CorpusScenario = ScenarioCheck{Scenario: "evaluation-corpus", Names: CorpusScenarioNames, Run: func(ctx context.Context, _ *Server, _ string, a Adapter) ([]Finding, error) {
	root, err := RepositoryRoot()
	if err != nil {
		return nil, err
	}
	passed, err := ReplayCorpusAdapter(ctx, filepath.Join(root, "corpus"), a, false, nil)
	if err != nil {
		return nil, err
	}
	var out []Finding
	if len(passed) != 2 {
		out = append(out, Finding{Check: "22 replay", Detail: "expected both model seed entries"})
	}
	expected := map[string]any{"gateDecisions": []any{map[string]any{"action": "refused"}}, "outcomes": []any{}, "refusals": []any{}}
	actual := map[string]any{"gateDecisions": []any{map[string]any{"action": "rewrote"}}, "outcomes": []any{}, "refusals": []any{}}
	if problem := runcontext.CompareCorpus(expected, actual, nil); problem == nil || problem.Error() != "/gateDecisions/0/action differs" {
		out = append(out, Finding{Check: "22 tamper", Detail: "differing member not named"})
	}
	return out, nil
}}

func agentCorpusChecks(ctx context.Context, _ *Server, host AgentHost, _ func(Adapter) Adapter, inner Adapter) ([]Finding, error) {
	root, err := RepositoryRoot()
	if err != nil {
		return nil, err
	}
	passed, err := ReplayCorpusAdapter(ctx, filepath.Join(root, "corpus"), inner, true, host.Bind)
	var findings []Finding
	if err == nil && len(passed) != 1 {
		findings = append(findings, Finding{Check: "22 agent replay", Detail: "expected the agent seed entry"})
	}
	for at := range findings {
		findings[at].Check = "22 agent " + strings.TrimPrefix(findings[at].Check, "22 agent ")
	}
	return findings, err
}

type corpusRequest struct {
	Suffix   string `json:"suffix"`
	Response struct {
		Status  int               `json:"status"`
		Headers map[string]string `json:"headers"`
		Body    string            `json:"body"`
	} `json:"response"`
}

func runCorpusAdapter(ctx context.Context, entry runcontext.CorpusEntry, a Adapter, agentCapability bool, bind func(AgentScript) adapter.AgentRun) error {
	var requests []corpusRequest
	if err := json.Unmarshal(entry.Requests, &requests); err != nil {
		return err
	}
	at := 0
	events := []contract.RunEvent{}
	run := &Run{Prompt: "Replay this recorded run.", TestPrompt: "", AllowConversation: true, Critic: "off", ModelName: "scripted-model", Thinking: "off", Emit: func(event contract.RunEvent) { events = append(events, event) }}
	tools, err := runcontext.NewScriptedCorpusTools(entry)
	if err != nil {
		return err
	}
	var offer contract.ToolOffer
	_, vectors, err := ReadVector("tool-offer/scenario-offer.json")
	if err != nil {
		return err
	}
	if len(vectors) != 1 || json.Unmarshal(vectors[0], &offer) != nil {
		return fmt.Errorf("corpus replay: invalid recorded tool offer")
	}
	gated := gate.New(gate.Options{Runtime: func(_ context.Context, message json.RawMessage) (json.RawMessage, error) {
		var frame struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
			Params struct {
				Name string          `json:"name"`
				Args json.RawMessage `json:"arguments"`
			} `json:"params"`
		}
		if err := json.Unmarshal(message, &frame); err != nil {
			return nil, err
		}
		result := tools.Call(frame.Params.Name, frame.Params.Args)
		return json.Marshal(map[string]any{"jsonrpc": "2.0", "id": frame.ID, "result": json.RawMessage(result)})
	}, Granted: gate.Ceiling, Emit: run.Emit})
	gated.SetOffer(offer)
	run.Gate, run.Tools = gated.Calls(), offer.Tools
	run.Wire = "openai-compatible"
	if agentCapability {
		if bind == nil {
			return fmt.Errorf("agent corpus replay has no recording-backed binding")
		}
		run.Model = nil
		run.Agent = &adapter.Agent{Model: "scripted-agent", Run: bind(func(_ adapter.AgentRequest, _ int) AgentPlay {
			if at >= len(requests) {
				return AgentPlay{Fail: fmt.Errorf("corpus request %d differs", at)}
			}
			row := requests[at]
			at++
			var body struct {
				Events []adapter.AgentEvent `json:"events"`
			}
			if err := json.Unmarshal([]byte(row.Response.Body), &body); err != nil {
				return AgentPlay{Fail: err}
			}
			steps := make([]AgentStep, 0, len(body.Events))
			for _, event := range body.Events {
				if event.Type == "message" {
					steps = append(steps, AgentStep{Say: event.Text})
				} else if event.Type == "text" {
					steps = append(steps, AgentStep{Commentary: event.Text})
				}
			}
			return AgentPlay{Steps: steps}
		})}
	} else {
		run.Model = relay.Call(func(_ context.Context, _ string, _ relay.Request) (*http.Response, error) {
			if at >= len(requests) {
				return nil, fmt.Errorf("corpus request %d differs", at)
			}
			row := requests[at]
			at++
			header := http.Header{}
			for name, value := range row.Response.Headers {
				header.Set(name, value)
			}
			return &http.Response{StatusCode: row.Response.Status, Header: header, Body: io.NopCloser(bytes.NewBufferString(row.Response.Body))}, nil
		})
	}
	if err := a.Run(ctx, run); err != nil {
		return err
	}
	if at != len(requests) {
		return fmt.Errorf("corpus requests: used %d, recorded %d", at, len(requests))
	}
	if err := tools.Complete(); err != nil {
		return err
	}
	actual, err := runcontext.CorpusExpected(events)
	if err != nil {
		return err
	}
	if err := runcontext.CompareCorpus(entry.Expected, actual, entry.Manifest.Tolerance); err != nil {
		if drift, ok := err.(*runcontext.CorpusDrift); ok {
			drift.ID = entry.Manifest.ID
		}
		return err
	}
	return nil
}

func ReplayCorpus(root string) ([]string, error) {
	rows, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	names := []string{}
	for _, row := range rows {
		if row.IsDir() {
			names = append(names, row.Name())
		}
	}
	sort.Strings(names)
	passed := []string{}
	for _, name := range names {
		dir := filepath.Join(root, name)
		read := func(file string) ([]byte, error) { return os.ReadFile(filepath.Join(dir, file)) }
		manifestRaw, err := read("manifest.json")
		if err != nil {
			return passed, err
		}
		var manifest runcontext.CorpusManifest
		if err = json.Unmarshal(manifestRaw, &manifest); err != nil {
			return passed, err
		}
		if manifest.ID != name {
			return passed, fmt.Errorf("%s: manifest id differs from directory", name)
		}
		requests, err := read(manifest.Files.Requests)
		if err != nil {
			return passed, err
		}
		tools, err := read(manifest.Files.ToolResults)
		if err != nil {
			return passed, err
		}
		eventRaw, err := read(manifest.Files.Events)
		if err != nil {
			return passed, err
		}
		checkpoint, err := read(manifest.Files.Checkpoint)
		if err != nil {
			return passed, err
		}
		expectedRaw, err := read(manifest.Expected.File)
		if err != nil {
			return passed, err
		}
		var events []contract.RunEvent
		if err = json.Unmarshal(eventRaw, &events); err != nil {
			return passed, fmt.Errorf("%s events: %w", name, err)
		}
		var expected map[string]any
		if err = json.Unmarshal(expectedRaw, &expected); err != nil {
			return passed, err
		}
		entry := runcontext.CorpusEntry{Manifest: manifest, Requests: requests, ToolResults: tools, Events: events, Checkpoint: checkpoint, Expected: expected}
		if err := runcontext.ReplayCorpus(entry); err != nil {
			return passed, err
		}
		passed = append(passed, name)
	}
	return passed, nil
}

// ReplayCorpusAdapter runs the seed for one capability through the release adapter against only
// recording-backed model or agent answers, then compares the newly emitted events.
func ReplayCorpusAdapter(ctx context.Context, root string, a Adapter, agentCapability bool, bind func(AgentScript) adapter.AgentRun) ([]string, error) {
	rows, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	names := []string{}
	for _, row := range rows {
		if row.IsDir() {
			want := strings.HasPrefix(row.Name(), "agent-")
			if want == agentCapability {
				names = append(names, row.Name())
			}
		}
	}
	sort.Strings(names)
	passed := []string{}
	for _, name := range names {
		dir := filepath.Join(root, name)
		read := func(file string) ([]byte, error) { return os.ReadFile(filepath.Join(dir, file)) }
		manifestRaw, err := read("manifest.json")
		if err != nil {
			return passed, err
		}
		var manifest runcontext.CorpusManifest
		if err = json.Unmarshal(manifestRaw, &manifest); err != nil {
			return passed, err
		}
		if manifest.ID != name {
			return passed, fmt.Errorf("%s: manifest id differs from directory", name)
		}
		requests, err := read(manifest.Files.Requests)
		if err != nil {
			return passed, err
		}
		tools, err := read(manifest.Files.ToolResults)
		if err != nil {
			return passed, err
		}
		eventRaw, err := read(manifest.Files.Events)
		if err != nil {
			return passed, err
		}
		checkpoint, err := read(manifest.Files.Checkpoint)
		if err != nil {
			return passed, err
		}
		expectedRaw, err := read(manifest.Expected.File)
		if err != nil {
			return passed, err
		}
		var events []contract.RunEvent
		if err = json.Unmarshal(eventRaw, &events); err != nil {
			return passed, err
		}
		var expected map[string]any
		if err = json.Unmarshal(expectedRaw, &expected); err != nil {
			return passed, err
		}
		entry := runcontext.CorpusEntry{Manifest: manifest, Requests: requests, ToolResults: tools, Events: events, Checkpoint: checkpoint, Expected: expected}
		if err := runCorpusAdapter(ctx, entry, a, agentCapability, bind); err != nil {
			return passed, err
		}
		passed = append(passed, name)
	}
	return passed, nil
}
