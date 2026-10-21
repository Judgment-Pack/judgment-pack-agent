package certification

import (
	"context"
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
			if len(passed) != 1 || !ran {
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
