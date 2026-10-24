package runcontext

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func reportFixture(t *testing.T) *Value {
	t.Helper()
	raw, err := os.ReadFile("../../contract/vectors/report/reports.json")
	if err != nil {
		t.Fatal(err)
	}
	return MustParseJSON(string(raw))
}
func reportCheckpoint(t *testing.T, row *Value) contract.Checkpoint {
	t.Helper()
	value := Object()
	for _, key := range row.Keys() {
		if key != "version" && key != "checkpointVersion" {
			value.Set(key, row.Get(key).Clone())
		}
	}
	value.Set("version", row.Get("checkpointVersion").Clone())
	value.Set("requestKey", String(strings.Repeat("a", 64)))
	value.Set("operations", Array())
	value.Set("context", ObjectOf("scope", String("report-scope"), "entries", Array()))
	cp, err := ReadExecution(value)
	if err != nil {
		t.Fatal(err)
	}
	return *cp
}

type reportPrefixFixture struct {
	cp    contract.Checkpoint
	head  RunHead
	pages []RunEventPage
}

func reportPrefix(t *testing.T) reportPrefixFixture {
	t.Helper()
	cp := reportCheckpoint(t, reportFixture(t).Get("items").Items()[0])
	value, _ := runReportValue(cp)
	row := func(cursor string) StoredRunEvent {
		return StoredRunEvent{Cursor: cursor, Bytes: []byte(CanonicalJSON(ObjectOf("version", Number(1), "scope", ObjectOf("execution", String(cp.ID), "role", String("author")), "event", ObjectOf("type", String("execution_checkpoint"), "checkpoint", value.Clone(), "durable", Bool(true)))))}
	}
	return reportPrefixFixture{cp: cp, head: RunHead{RunReceipt: RunReceipt{Revision: "revision", CommitID: "commit", Cursor: "a"}, RunID: cp.ID, Checkpoint: value.JSON()}, pages: []RunEventPage{{Items: []StoredRunEvent{row("z")}, NextCursor: "z", HasMore: true}, {Items: []StoredRunEvent{row("a")}, NextCursor: "a"}}}
}
func TestRunReportSharedVectors(t *testing.T) {
	for _, expected := range reportFixture(t).Get("items").Items() {
		cp := reportCheckpoint(t, expected)
		before, _ := json.Marshal(cp)
		actual, err := ProjectRunReport(cp)
		if err != nil {
			t.Fatal(err)
		}
		text, err := CanonicalRunReport(actual)
		if err != nil || text != CanonicalJSON(expected) {
			t.Fatalf("shared report differs: %v\n%s\n%s", err, text, CanonicalJSON(expected))
		}
		after, _ := json.Marshal(cp)
		if string(before) != string(after) {
			t.Fatal("projector changed checkpoint")
		}
		value, _ := runReportValue(cp)
		for _, name := range runReportSources {
			if value.Has(name) && CanonicalJSON(actual.Get(name)) != CanonicalJSON(value.Get(name)) {
				t.Fatalf("omitted source %s", name)
			}
		}
	}
}
func TestRunReportSourceInventory(t *testing.T) {
	read := func(name string) *Value {
		t.Helper()
		raw, err := os.ReadFile("../../contract/schemas/" + name + ".json")
		if err != nil {
			t.Fatal(err)
		}
		return MustParseJSON(string(raw)).Get("properties")
	}
	checkpoint, report := read("checkpoint"), read("run-report")
	names := append(append(append([]string{}, runReportSources...), runReportExcluded...), "version")
	sort.Strings(names)
	keys := checkpoint.Keys()
	sort.Strings(keys)
	if !reflect.DeepEqual(names, keys) {
		t.Fatalf("checkpoint member inventory differs: %v vs %v", names, keys)
	}
	sources := append(append([]string{}, runReportSources...), "version", "checkpointVersion")
	sort.Strings(sources)
	keys = report.Keys()
	sort.Strings(keys)
	if !reflect.DeepEqual(sources, keys) {
		t.Fatal("report source inventory differs")
	}
	seen := map[string]bool{}
	for _, row := range reportFixture(t).Get("items").Items() {
		for _, key := range row.Keys() {
			seen[key] = true
		}
	}
	if len(seen) != len(keys) {
		t.Fatal("report vector missing member")
	}
	for _, key := range keys {
		if !seen[key] {
			t.Fatalf("missing report vector member %s", key)
		}
	}
}
func TestRunReportWholeSharedPlan(t *testing.T) {
	for _, filename := range []string{"checkpoints.json", "extended-checkpoints.json"} {
		raw, err := os.ReadFile("../../contract/vectors/plan/" + filename)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range MustParseJSON(string(raw)).Get("items").Items() {
			// Project each shared nested plan state in a terminal checkpoint shell.
			terminal := row.Clone()
			terminal.Set("status", String("stopped"))
			cp, err := ReadExecution(terminal)
			if err != nil {
				t.Fatal(err)
			}
			report, err := ProjectRunReport(*cp)
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"plan", "planState"} {
				if CanonicalJSON(report.Get(name)) != CanonicalJSON(row.Get(name)) {
					t.Fatalf("changed whole %s in %s", name, filename)
				}
			}
		}
	}
}
func TestRunReportPrefixOpaqueCursors(t *testing.T) {
	p := reportPrefix(t)
	actual, err := BuildRunReport(p.cp, p.head, p.pages)
	if err != nil {
		t.Fatal(err)
	}
	expected, _ := ProjectRunReport(p.cp)
	if CanonicalJSON(actual) != CanonicalJSON(expected) {
		t.Fatal("prefix changed report")
	}
	p.head.Checkpoint = []byte("  " + string(p.head.Checkpoint) + "\n")
	if _, err = BuildRunReport(p.cp, p.head, p.pages); err != nil {
		t.Fatal("head canonical comparison", err)
	}
}
func TestRunReportPrefixRefusals(t *testing.T) {
	mutations := []func(*reportPrefixFixture){
		func(p *reportPrefixFixture) { p.pages = nil }, func(p *reportPrefixFixture) { p.pages[0].HasMore = false }, func(p *reportPrefixFixture) { p.pages[1].HasMore = true },
		func(p *reportPrefixFixture) { p.pages[0].NextCursor = "wrong" }, func(p *reportPrefixFixture) {
			p.pages[1].Items[0].Cursor = "z"
			p.pages[1].NextCursor = "z"
			p.head.Cursor = "z"
		},
		func(p *reportPrefixFixture) { p.head.Cursor = "later" }, func(p *reportPrefixFixture) { p.head.RunID = "foreign" }, func(p *reportPrefixFixture) { p.head.Revision = "" },
		func(p *reportPrefixFixture) {
			v := MustParseJSON(string(p.head.Checkpoint))
			v.Set("reason", String("different"))
			p.head.Checkpoint = v.JSON()
		},
		func(p *reportPrefixFixture) {
			v := MustParseJSON(string(p.pages[1].Items[0].Bytes))
			v.Get("scope").Set("execution", String("foreign"))
			p.pages[1].Items[0].Bytes = v.JSON()
		},
		func(p *reportPrefixFixture) {
			v := MustParseJSON(string(p.pages[1].Items[0].Bytes))
			v.Get("event").Get("checkpoint").Set("reason", String("different"))
			p.pages[1].Items[0].Bytes = v.JSON()
		},
		func(p *reportPrefixFixture) {
			for i := range p.pages {
				v := MustParseJSON(string(p.pages[i].Items[0].Bytes))
				v.Get("event").Get("checkpoint").Set("status", String("running"))
				p.pages[i].Items[0].Bytes = v.JSON()
			}
		},
		func(p *reportPrefixFixture) {
			v := MustParseJSON(string(p.pages[0].Items[0].Bytes))
			v.Set("event", ObjectOf("type", String("unknown")))
			p.pages[0].Items[0].Bytes = v.JSON()
		},
		func(p *reportPrefixFixture) {
			v := MustParseJSON(string(p.pages[0].Items[0].Bytes))
			v.Get("event").Get("checkpoint").Set("used", Number(999))
			p.pages[0].Items[0].Bytes = v.JSON()
		},
		func(p *reportPrefixFixture) { p.pages[0].Items[0].Bytes = []byte{255} }, func(p *reportPrefixFixture) { p.pages[0].Items = nil },
	}
	for i, mutate := range mutations {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			p := reportPrefix(t)
			mutate(&p)
			_, err := BuildRunReport(p.cp, p.head, p.pages)
			var invalid *RunReportPrefixInvalid
			if !errors.As(err, &invalid) {
				t.Fatalf("expected bounded prefix refusal: %v", err)
			}
		})
	}
}
func TestRunReportOverflow(t *testing.T) {
	p := reportPrefix(t)
	raw := p.pages[0].Items[0].Bytes
	p.pages = nil
	for page := 0; page < 4; page++ {
		items := []StoredRunEvent{}
		for i := 0; i < 100; i++ {
			items = append(items, StoredRunEvent{Cursor: fmt.Sprintf("cursor-%d", page*100+i), Bytes: raw})
		}
		p.pages = append(p.pages, RunEventPage{Items: items, NextCursor: items[99].Cursor, HasMore: page < 3})
	}
	p.head.Cursor = "cursor-399"
	if _, err := BuildRunReport(p.cp, p.head, p.pages); err != nil {
		t.Fatal(err)
	}
	p.pages[3].HasMore = true
	p.pages = append(p.pages, RunEventPage{Items: []StoredRunEvent{{Cursor: "overflow", Bytes: raw}}, NextCursor: "overflow"})
	p.head.Cursor = "overflow"
	var overflow *RunReportOverflow
	if _, err := BuildRunReport(p.cp, p.head, p.pages); !errors.As(err, &overflow) {
		t.Fatalf("event-count overflow: %v", err)
	}
	big := reportPrefix(t)
	big.pages[0].Items[0].Bytes = make([]byte, MaxRunReportBytes+1)
	if _, err := BuildRunReport(big.cp, big.head, big.pages); !errors.As(err, &overflow) {
		t.Fatalf("byte overflow: %v", err)
	}
	report, _ := ProjectRunReport(p.cp)
	report.Set("explanation", String(strings.Repeat("x", MaxRunReportBytes)))
	if _, err := CanonicalRunReport(report); !errors.As(err, &overflow) {
		t.Fatalf("report overflow: %v", err)
	}
}
func TestRunReportNonterminal(t *testing.T) {
	for _, status := range []string{"running", "paused", "invalid"} {
		p := reportPrefix(t)
		p.cp.Status = status
		_, err := ProjectRunReport(p.cp)
		var invalid *RunReportInputInvalid
		if !errors.As(err, &invalid) {
			t.Fatalf("expected input refusal: %v", err)
		}
	}
}

func TestRunReportPrefixEvidenceLimits(t *testing.T) {
	p := reportPrefix(t)
	if _, err := BuildRunReport(p.cp, p.head, p.pages[1:]); err != nil {
		t.Fatal("page type cannot prove predecessor", err)
	}
	value := MustParseJSON(string(p.pages[0].Items[0].Bytes))
	value.Get("scope").Set("execution", String("child-not-in-retained-history"))
	value.Get("scope").Set("task", String("child-not-in-retained-history"))
	value.Get("event").Get("checkpoint").Set("id", String("child-not-in-retained-history"))
	p.pages[0].Items[0].Bytes = value.JSON()
	if _, err := BuildRunReport(p.cp, p.head, p.pages); err != nil {
		t.Fatal("child history may have been pruned", err)
	}
}
