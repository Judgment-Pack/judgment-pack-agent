package delegate

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
)

func TestSubagentResultMinimum(t *testing.T) {
	raw, err := os.ReadFile("../../../contract/vectors/subagents/result-minimum.json")
	if err != nil {
		t.Fatal(err)
	}
	var vector struct {
		Items       []json.RawMessage
		ResultBytes int
	}
	if err := json.Unmarshal(raw, &vector); err != nil {
		t.Fatal(err)
	}
	report := runcontext.MustParseJSON(string(vector.Items[0]))
	if reportBytes(report) != vector.ResultBytes || runcontext.SubagentResultMinBytes != vector.ResultBytes {
		t.Fatal("report minimum differs from the vector")
	}
	for _, summary := range []string{strings.Repeat("Child evidence.", 50), strings.Repeat(" ", 100) + "tail", strings.Repeat("\n", 100) + "tail", strings.Repeat("\x01", 100), strings.Repeat("😀", 100)} {
		report.Set("summary", runcontext.String(summary))
		shown, _ := FitReport(report, 0)
		if strings.TrimSpace(shown.Get("summary").Str()) == "" || reportBytes(shown) > vector.ResultBytes {
			t.Fatalf("report exceeds admitted bound: %s", shown.JSON())
		}
	}
}
