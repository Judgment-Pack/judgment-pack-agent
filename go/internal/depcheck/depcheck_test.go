package depcheck

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// TestNoPackageOfTheModuleIsADeniedClient lists every package the module and its tests depend on
// and fails on any the deny list names. The Eino adapter (ADR-0004, PR 3) is held to it as it
// lands.
func TestNoPackageOfTheModuleIsADeniedClient(t *testing.T) {
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Fatal("the go command is needed to list the module's dependencies:", err)
	}
	list := exec.Command(goTool, "list", "-deps", "-test", "./...")
	list.Dir = "../.."
	out, err := list.Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	packages := strings.Fields(string(out))
	if !slices.Contains(packages, "github.com/Judgment-Pack/judgment-pack-agent/go/gate") || !slices.Contains(packages, "net/http") {
		t.Fatalf("the listing does not hold the module's own packages: %d listed", len(packages))
	}
	for _, path := range packages {
		if hit := Matches(path); len(hit) > 0 {
			t.Errorf("%s is denied (%s)", path, strings.Join(hit, ", "))
		}
	}
	t.Logf("%d packages listed, none denied", len(packages))
}

// The matcher catches a denied package and the packages below it, and nothing that only shares a
// prefix of its name.
func TestTheDenyListMatchesByPath(t *testing.T) {
	for path, denied := range map[string]bool{
		"google.golang.org/genai":                                  true,
		"google.golang.org/genai/internal":                         true,
		"google.golang.org/genaix":                                 false,
		"github.com/cloudwego/eino-ext/components/model/openai":    true,
		"github.com/cloudwego/eino-ext/components/tool/browseruse": false,
		"github.com/cloudwego/eino/adk":                            false,
		"github.com/openai/openai-go/v3/option":                    true,
		"github.com/Judgment-Pack/judgment-pack-agent/go/contract": false,
	} {
		if got := len(Matches(path)) > 0; got != denied {
			t.Errorf("%s: denied is %t", path, got)
		}
	}
}
