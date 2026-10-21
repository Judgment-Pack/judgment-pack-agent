package certification

// Scenario 22's repository-corpus replay. Inputs come only from the six files in each entry; no
// adapter or provider capability is accepted by this path, so a replay cannot reach a live provider.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

var CorpusScenarioNames = []string{"22 replay", "22 tamper", "22 round-trip"}
var AgentCorpusNames = []string{"22 agent replay", "22 agent tamper", "22 agent round-trip"}

var CorpusScenario = ScenarioCheck{Scenario: "evaluation-corpus", Names: CorpusScenarioNames, Run: func(_ context.Context, _ *Server, _ string, _ Adapter) ([]Finding, error) {
	root, err := RepositoryRoot()
	if err != nil {
		return nil, err
	}
	passed, err := ReplayCorpus(filepath.Join(root, "corpus"))
	if err != nil {
		return nil, err
	}
	var out []Finding
	if len(passed) != 2 {
		out = append(out, Finding{Check: "22 replay", Detail: "expected two seed entries"})
	}
	expected := map[string]any{"gateDecisions": []any{map[string]any{"action": "refused"}}, "outcomes": []any{}, "refusals": []any{}}
	actual := map[string]any{"gateDecisions": []any{map[string]any{"action": "rewrote"}}, "outcomes": []any{}, "refusals": []any{}}
	if problem := runcontext.CompareCorpus(expected, actual, nil); problem == nil || problem.Error() != "/gateDecisions/0/action differs" {
		out = append(out, Finding{Check: "22 tamper", Detail: "differing member not named"})
	}
	return out, nil
}}

func agentCorpusChecks(ctx context.Context, server *Server, _ AgentHost, _ func(Adapter) Adapter, inner Adapter) ([]Finding, error) {
	findings, err := CorpusScenario.Run(ctx, server, "agent", inner)
	for at := range findings {
		findings[at].Check = "22 agent " + strings.TrimPrefix(findings[at].Check, "22 ")
	}
	return findings, err
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
