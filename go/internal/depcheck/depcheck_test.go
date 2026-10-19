package depcheck

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// TestEveryPackageIsOnTheAllowList lists every package the module and its tests depend on, with
// its module and version, and fails on one whose module the allow-list does not admit at that
// version, and on any the deny list names.
func TestEveryPackageIsOnTheAllowList(t *testing.T) {
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Fatal("the go command is needed to list the module's dependencies:", err)
	}
	list := exec.Command(goTool, "list", "-deps", "-test", "-f", "{{.ImportPath}}\t{{if not .Standard}}{{with .Module}}{{.Path}}\t{{.Version}}{{end}}{{end}}", "./...")
	list.Dir = "../.."
	out, err := list.Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	var packages []string
	modules := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Split(line, "\t")
		if fields[0] == "" {
			continue
		}
		packages = append(packages, fields[0])
		module, version := "", ""
		if len(fields) >= 2 && fields[1] != "" {
			module = fields[1]
		}
		if len(fields) >= 3 {
			version = fields[2]
		}
		if problem := Admitted(module, version); problem != "" {
			t.Errorf("%s: %s", fields[0], problem)
		}
		if module != "" && module != Module {
			modules[module] = true
		}
		if hit := Matches(fields[0]); len(hit) > 0 {
			t.Errorf("%s is denied (%s)", fields[0], strings.Join(hit, ", "))
		}
	}
	if !slices.Contains(packages, "github.com/Judgment-Pack/judgment-pack-agent/go/gate") || !slices.Contains(packages, "net/http") ||
		!slices.Contains(packages, "github.com/cloudwego/eino/adk") {
		t.Fatalf("the listing does not hold the module's own packages and the adapter's: %d listed", len(packages))
	}
	for module := range Allowed {
		if !modules[module] {
			t.Errorf("%s is on the allow-list and no package of the module uses it", module)
		}
	}
	t.Logf("%d packages listed from %d modules beside the standard library and this one, all allowed", len(packages), len(modules))
}

// A module off the list, or at another version, is not admitted; the standard library and the
// module itself are.
func TestTheAllowListAdmitsByModuleAndVersion(t *testing.T) {
	for _, row := range []struct {
		module, version string
		admitted        bool
	}{
		{"", "", true},
		{Module, "", true},
		{"github.com/cloudwego/eino", "v0.9.21", true},
		{"github.com/cloudwego/eino", "v0.9.22", false},
		{"github.com/tmc/langchaingo", "v0.1.14", false},
		{"github.com/cloudwego/eino-ext/components/model/openai", "v0.1.0", false},
	} {
		if got := Admitted(row.module, row.version) == ""; got != row.admitted {
			t.Errorf("%s %s: admitted is %t", row.module, row.version, got)
		}
	}
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
