package runcontext

import (
	"os"
	"testing"
)

func TestHistoryReportTaskVersions(t *testing.T) {
	raw, e := os.ReadFile("../../contract/fixtures/history/report-task-versions.json")
	if e != nil {
		t.Fatal(e)
	}
	vectors, e := ParseJSON(raw)
	if e != nil {
		t.Fatal(e)
	}
	for _, row := range vectors.Get("cases").Items() {
		t.Run(row.Get("name").Str(), func(t *testing.T) {
			report := row.Get("report")
			display, de := readRunReportDisplay(report)
			native, ne := ParseRunReport([]byte(CanonicalJSON(report)))
			if row.Get("valid").Truth() {
				if de != nil || !archiveEqual(display, report) {
					t.Fatal("valid current/legacy task presentation refused", de)
				}
				if row.Get("native").Truth() && (ne != nil || !archiveEqual(native, report)) {
					t.Fatal("valid current/legacy native report refused", ne)
				}
			} else {
				if de == nil {
					t.Fatal("false old-source task display admitted")
				}
				if ne == nil {
					t.Fatal("false old-source task report admitted")
				}
			}
		})
	}
}
