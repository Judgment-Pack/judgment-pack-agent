package runcontext

import (
	"os"
	"strings"
	"testing"
)

func repeatReportVectors(t *testing.T) *Value {
	t.Helper()
	raw, e := os.ReadFile("../../contract/fixtures/report/repeat-tools.json")
	if e != nil {
		t.Fatal(e)
	}
	v, e := ParseJSON(raw)
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func TestHistoryReportActualRepeatSummary(t *testing.T) {
	v := repeatReportVectors(t)
	cp, e := ReadExecution(v.Get("checkpoint"))
	if e != nil {
		t.Fatal(e)
	}
	report, e := ProjectRunReport(*cp)
	if e != nil {
		t.Fatal(e)
	}
	if !archiveEqual(report.Get("repeatTools"), v.Get("repeatTools")) {
		t.Fatal("actual repeat summary omitted/expanded")
	}
	raw := CanonicalJSON(report)
	if strings.Contains(raw, "keyDigest") || strings.Contains(raw, "PRIVATE-POLICY") {
		t.Fatal("private repeat data escaped")
	}
	if _, e = ReadRunReport(report); e != nil {
		t.Fatal(e)
	}
	if _, e = readRunReportDisplay(report); e != nil {
		t.Fatal(e)
	}
	off := v.Get("checkpoint").Clone()
	off = without(off, "repeatTools", "policy")
	checkpoint, e := ReadExecution(off)
	if e != nil {
		t.Fatal(e)
	}
	absent, e := ProjectRunReport(*checkpoint)
	if e != nil || absent.Has("repeatTools") {
		t.Fatal("absent repeat invented", e)
	}
}
func TestHistoryReportRepeatRefusals(t *testing.T) {
	v := repeatReportVectors(t)
	for _, fault := range v.Get("refusals").Items() {
		t.Run(fault.Str(), func(t *testing.T) {
			cp, e := ReadExecution(v.Get("checkpoint"))
			if e != nil {
				t.Fatal(e)
			}
			report, e := ProjectRunReport(*cp)
			if e != nil {
				t.Fatal(e)
			}
			r := report.Get("repeatTools")
			row := r.Get("executions").Items()[0]
			switch fault.Str() {
			case "legacy":
				report.Set("checkpointVersion", Number(2))
			case "off":
				r.Get("policy").Get("repeatTools.mode").Set("value", String("off"))
			case "missing-policy":
				r.Set("policy", Null())
			case "threshold":
				row.Set("stopAt", Number(3))
			case "counter":
				row.Set("count", Number(0))
			case "delivered":
				row.Set("reminded", Bool(false))
			case "private-seen":
				row.Set("seen", Array())
			case "private-policy":
				r.Get("policy").Set("private", ObjectOf("value", String("secret"), "layer", String("run")))
			case "provenance":
				r.Get("policy").Get("repeatTools.mode").Set("layer", String("invented"))
			}
			if _, e = ReadRunReport(report); e == nil {
				t.Fatal("invented repeat report admitted")
			}
			if _, e = readRunReportDisplay(report); e == nil {
				t.Fatal("invented repeat display admitted")
			}
		})
	}
}
