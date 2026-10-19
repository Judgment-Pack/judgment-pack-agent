package wire

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The contract's vectors, read from the repository this package sits in: the tests hold the port
// to them rather than to a copy written here.

// repositoryRoot walks up from the working directory to the directory holding contract/vectors.
func repositoryRoot(t *testing.T) string {
	t.Helper()
	at, err := os.Getwd()
	if err != nil {
		t.Fatalf("the working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(at, "contract", "vectors")); err == nil {
			return at
		}
		parent := filepath.Dir(at)
		if parent == at {
			t.Fatal("no directory above this one holds contract/vectors")
		}
		at = parent
	}
}

// vectorFile is one contract file, read as a value.
func vectorFile(t *testing.T, path ...string) *Value {
	t.Helper()
	text, err := os.ReadFile(filepath.Join(append([]string{repositoryRoot(t)}, path...)...))
	if err != nil {
		t.Fatalf("reading %v: %v", path, err)
	}
	value, err := Parse(text)
	if err != nil {
		t.Fatalf("reading %v as JSON: %v", path, err)
	}
	return value
}

// vectorItems are one vector file's items.
func vectorItems(t *testing.T, path ...string) []*Value {
	t.Helper()
	return vectorFile(t, path...).Get("items").Items()
}

// equalJSON compares two values by the text JSON.stringify writes of each.
func equalJSON(t *testing.T, what string, got, want *Value) {
	t.Helper()
	if got.String() != want.String() {
		t.Errorf("%s:\n got %s\nwant %s", what, got.String(), want.String())
	}
}

// equalText compares two strings.
func equalText(t *testing.T, what, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("%s:\n got %q\nwant %q", what, got, want)
	}
}

// contains reports a substring, for the sentences a refusal carries.
func contains(t *testing.T, what, held, wanted string) {
	t.Helper()
	if !strings.Contains(held, wanted) {
		t.Errorf("%s: %q does not carry %q", what, held, wanted)
	}
}

// recorder is a listener that keeps what it was told, in order.
type recorder struct {
	text      []string
	reasoning []string
	ended     []string
}

func (r *recorder) Text(delta string) error { r.text = append(r.text, delta); return nil }
func (r *recorder) Reasoning(delta string) error {
	r.reasoning = append(r.reasoning, delta)
	return nil
}
func (r *recorder) ReasoningEnd(text string) error {
	r.ended = append(r.ended, text)
	return nil
}

func (r *recorder) prose() string { return strings.Join(r.text, "") }
