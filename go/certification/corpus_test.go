package certification

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScenario22EveryGoAdapterReplaysCorpus(t *testing.T) {
	root, err := RepositoryRoot()
	if err != nil {
		t.Fatal(err)
	}
	for _, adapter := range []string{"eino", "agent"} {
		t.Run(adapter, func(t *testing.T) {
			passed, err := ReplayCorpus(filepath.Join(root, "corpus"))
			if err != nil {
				t.Fatal(err)
			}
			if len(passed) != 2 {
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
	target, err := os.MkdirTemp(filepath.Join(root, ".jpdev"), "corpus-tamper-")
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
				raw = []byte(strings.Replace(string(raw), `"refused"`, `"rewrote"`, 1))
			}
			if err := os.WriteFile(filepath.Join(dir, file.Name()), raw, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	_, err = ReplayCorpus(target)
	if err == nil || !strings.Contains(err.Error(), "/gateDecisions/0/action differs") {
		t.Fatalf("tamper: %v", err)
	}
}
