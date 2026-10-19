package runcontext

// What every test here reads: the contract's vectors and schemas, from the repository root.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repositoryRoot walks up from the working directory to the directory holding contract/vectors.
func repositoryRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "contract", "vectors")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no directory holding contract/vectors above the working directory")
		}
		dir = parent
	}
}

// readJSON reads one file under the repository root as a value.
func readJSON(t *testing.T, name string) *Value {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repositoryRoot(t), filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	value, err := ParseJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

// vectorItems are one vector's items.
func vectorItems(t *testing.T, name string) []*Value {
	t.Helper()
	return readJSON(t, "contract/vectors/"+name).Get("items").Items()
}

// equalJSON reports two values with the same JSON.
func equalJSON(a, b *Value) bool { return a.Text() == b.Text() }

// refused reports an error whose sentence carries the text given, as the TypeScript tests match a
// thrown message.
func refused(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected a refusal naming %q", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("the refusal was %q, which does not carry %q", err.Error(), want)
	}
}
