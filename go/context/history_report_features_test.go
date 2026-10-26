package runcontext

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"os"
	"testing"
)

func TestHistoryReportActualOptionalFeatureSections(t *testing.T) {
	for _, mode := range []string{"legacy", "budget"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			store := NewMemoryRunStore(MemoryRunStoreOptions{})
			source, _ := NewStoredChatScope(store, "chat", NewChatContext("shared"), nil)
			session := historyStoredSession(store, source, mode, false)
			if mode == "budget" {
				v := budgetVector(t)
				session.Context.UsageBudget = &UsageBudgetInput{Store: budgetMemory(t, v, MemoryUsageBudgetOptions{}), Scope: v.Scope, Bounder: func(context.Context, UsageBoundRequest) (UsageAmounts, error) {
					return UsageAmounts{"inputTokens": 1, "outputTokens": 1}, nil
				}}
			}
			actual, err := ContextualSession(ctx, session)
			if err != nil {
				t.Fatal(err)
			}
			boundary := NewBoundary(actual)
			historyAssertNoFailure(t, boundary.Open())
			events, _ := boundary.Event(contract.RunEvent{End: &contract.RunEventEnd{Type: "end"}})
			historyAssertNoFailure(t, events)
			boundary.Close()
			head, _ := store.Load(ctx, mode)
			before := bytes.Clone(head.Checkpoint)
			value, err := ParseJSON(before)
			if err != nil {
				t.Fatal(err)
			}
			checkpoint, err := ReadExecution(value)
			if err != nil {
				t.Fatal(err)
			}
			report, err := ProjectRunReport(*checkpoint)
			if err != nil {
				t.Fatal(err)
			}
			version, cpVersion := 2, 3
			if mode == "legacy" {
				version = 1
				cpVersion = 2
			}
			if report.Get("version").Num() != float64(version) || report.Get("checkpointVersion").Num() != float64(cpVersion) {
				t.Fatal("wrong report format")
			}
			if mode == "budget" {
				journal, err := readUsageJournal(value.Get("usageBudget"))
				if err != nil {
					t.Fatal(err)
				}
				totals, err := UsageJournalTotals(*journal)
				if err != nil || !report.Has("usageTotals") || CanonicalJSON(report.Get("usageTotals")) != historyInputText(totals) {
					t.Fatal("actual budget journal totals omitted or invented", err)
				}
			} else if checkpoint.UsageBudget != nil || report.Has("usageTotals") {
				t.Fatal("absent budget invented totals")
			}
			text, err := CanonicalRunReport(report)
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := ParseRunReport([]byte(text))
			if err != nil || CanonicalJSON(parsed) != text {
				t.Fatal("current report roundtrip", err)
			}
			after, _ := store.Load(ctx, mode)
			if !bytes.Equal(before, after.Checkpoint) {
				t.Fatal("report mutated execution bytes")
			}
		})
	}
}

func TestHistoryReportRetainedAbsentUsageSection(t *testing.T) {
	raw, err := os.ReadFile("../../contract/vectors/usage-budget/report.json")
	if err != nil {
		t.Fatal(err)
	}
	source := MustParseJSON(string(raw)).Get("items").Items()[0]
	without := Object()
	for _, key := range source.Keys() {
		if key != "usageTotals" {
			without.Set(key, source.Get(key).Clone())
		}
	}
	parsed, err := ReadRunReport(without)
	if err != nil || parsed.Has("usageTotals") || CanonicalJSON(parsed) != CanonicalJSON(without) {
		t.Fatal("absent usage section invented or refused", err)
	}
}
func TestHistoryReportClosedActualRouteDecision(t *testing.T) {
	raw, err := os.ReadFile("../../contract/fixtures/turn-route/decision.json")
	if err != nil {
		t.Fatal(err)
	}
	input := MustParseJSON(string(raw)).Get("cases").Items()[0].Get("input")
	var fields struct {
		Policy     RoutingPolicy          `json:"policy"`
		Catalogue  RouteCatalogue         `json:"catalogue"`
		Facts      RouteFacts             `json:"facts"`
		Binding    any                    `json:"binding"`
		Host       *RouteSelection        `json:"host"`
		Classifier *RouteClassifierRecord `json:"classifier"`
	}
	if err = json.Unmarshal(input.JSON(), &fields); err != nil {
		t.Fatal(err)
	}
	route, err := DecideTurnRoute(fields.Policy, fields.Catalogue, fields.Facts, fields.Binding, fields.Host, fields.Classifier)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile("../../contract/vectors/usage-budget/report.json")
	if err != nil {
		t.Fatal(err)
	}
	report := MustParseJSON(string(raw)).Get("items").Items()[0].Clone()
	report.Set("route", MustParseJSON(historyInputText(route)))
	if _, err = ReadRunReport(report); err != nil {
		t.Fatal("closed actual route report", err)
	}
	for _, mutate := range []func(*Value){func(v *Value) { v.Set("tools", Object()) }, func(v *Value) { v.Set("operations", Number(3)) }, func(v *Value) { v.Set("widened", Bool(false)).Set("widenedFor", String("undeclared")) }, func(v *Value) { v.Set("unknown", Bool(true)) }, func(v *Value) { v.Set("reason", String("bad\nlabel")) }} {
		bad := report.Clone()
		mutate(bad.Get("route"))
		if _, err = ReadRunReport(bad); err == nil {
			t.Fatal("malformed route report admitted")
		}
	}
	pattern := `^[^\u0000-\u001f]+$`
	if !reportPattern(pattern, "label") || reportPattern(pattern, "bad\nlabel") || reportPattern(pattern, "bad\x00label") {
		t.Fatal("trusted control pattern translation widened")
	}
}
