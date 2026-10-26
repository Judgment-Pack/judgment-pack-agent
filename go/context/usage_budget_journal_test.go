package runcontext

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
)

func usageFactsFixture() usageDispatchFacts {
	body := `{"max_tokens":5}`
	descriptor, _ := json.Marshal(map[string]string{"body": body})
	maximum := int64(5)
	return usageDispatchFacts{DurableScope: DurableScope{Execution: "run-1", Role: "author"}, Source: "provider-1", Tool: "model-1", Descriptor: string(descriptor), UsageBoundRequest: UsageBoundRequest{Kind: "model", Family: "openai-compatible", Connection: "connection-1", ConnectionRevision: "revision-1", Model: "model-1", Request: []byte(body), EncodedOutputMaximum: &maximum}}
}
func usageOwnerFixture(t *testing.T, store UsageBudgetStore, retained *UsageBudgetJournal) *usageJournalOwner {
	t.Helper()
	v := budgetVector(t)
	owner, err := newUsageJournalOwner(&UsageBudgetInput{Store: store, Scope: v.Scope, Bounder: func(context.Context, UsageBoundRequest) (UsageAmounts, error) {
		return UsageAmounts{"inputTokens": 10, "outputTokens": 5}, nil
	}}, "root-1", retained)
	if err != nil {
		t.Fatal(err)
	}
	return owner
}
func TestUsageBudgetOwnerExactFreshTicketAndActualTotals(t *testing.T) {
	v := budgetVector(t)
	store := budgetMemory(t, v, MemoryUsageBudgetOptions{})
	owner := usageOwnerFixture(t, store, nil)
	f := usageFactsFixture()
	ctx := context.Background()
	ticket, err := owner.reserve(ctx, "dispatch-1", f)
	if err != nil {
		t.Fatal(err)
	}
	totals, err := UsageJournalTotals(owner.journal)
	if err != nil || totals["inputTokens"].Known != 0 || totals["inputTokens"].Complete {
		t.Fatal("reservation fabricated actual input usage", err)
	}
	changed := f
	changed.Descriptor = "different"
	if _, err = owner.consume(ticket, changed); err == nil {
		t.Fatal("different exact request consumed ticket")
	}
	id, err := owner.consume(ticket, f)
	if err != nil || id != "dispatch-1" {
		t.Fatal("fresh exact ticket not consumed", err)
	}
	if _, err = owner.consume(ticket, f); err == nil {
		t.Fatal("ticket reused")
	}
	if err = owner.observe(ctx, id, UsageAmounts{"modelRequests": 1, "inputTokens": 4}, v.Evidence); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*UsageJournalEntry){func(row *UsageJournalEntry) { row.Observed["modelRequests"] = 0 }, func(row *UsageJournalEntry) { row.Observed = nil }, func(row *UsageJournalEntry) { row.Settlement.Observed["inputTokens"] = 3 }} {
		bad := usageCopy(owner.journal)
		mutate(&bad.Entries[0])
		if validUsageJournal(bad) {
			t.Fatal("contradictory retained usage accepted")
		}
	}
	totals, err = UsageJournalTotals(owner.journal)
	if err != nil || totals["modelRequests"] != (UsageTotal{1, true}) || totals["inputTokens"] != (UsageTotal{4, true}) || totals["outputTokens"] != (UsageTotal{0, false}) || totals["providerInvocations"] != (UsageTotal{0, true}) {
		t.Fatalf("actual subtotal/completeness: %+v %v", totals, err)
	}
	restored := usageOwnerFixture(t, store, &owner.journal)
	if err = restored.reconcile(ctx); err != nil {
		t.Fatal("known retained facts did not reconcile", err)
	}
	empty := budgetMemory(t, v, MemoryUsageBudgetOptions{})
	other := usageOwnerFixture(t, empty, &owner.journal)
	if err = other.reconcile(ctx); err == nil {
		t.Fatal("same-resource empty replacement reset retained usage")
	}
}
func TestUsageBudgetOwnerLostAckAndCrossOwnerCannotDispatch(t *testing.T) {
	v := budgetVector(t)
	ctx := context.Background()
	lost := errors.New("lost Reserve acknowledgment")
	store := budgetMemory(t, v, MemoryUsageBudgetOptions{AfterChange: func() error { return lost }})
	owner := usageOwnerFixture(t, store, nil)
	if _, err := owner.reserve(ctx, "dispatch-1", usageFactsFixture()); !errors.Is(err, lost) {
		t.Fatal(err)
	}
	restored := usageOwnerFixture(t, store, &owner.journal)
	if err := restored.reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	dispatches := 0
	if _, err := restored.reserve(ctx, "dispatch-1", usageFactsFixture()); err == nil {
		dispatches++
	}
	if dispatches != 0 {
		t.Fatal("lost Reserve acknowledgment authorized dispatch")
	}
	good := budgetMemory(t, v, MemoryUsageBudgetOptions{})
	first := usageOwnerFixture(t, good, nil)
	ticket, err := first.reserve(ctx, "dispatch-1", usageFactsFixture())
	if err != nil {
		t.Fatal(err)
	}
	other := usageOwnerFixture(t, good, nil)
	if _, err = other.reserve(ctx, "dispatch-1", usageFactsFixture()); err == nil {
		t.Fatal("created=false authorized dispatch")
	}
	if _, err = other.consume(ticket, usageFactsFixture()); err == nil {
		t.Fatal("another original owner accepted live ticket")
	}
}
func TestUsageBudgetFeatureReportSharedStrictVector(t *testing.T) {
	raw, err := os.ReadFile("../../contract/vectors/usage-budget/report.json")
	if err != nil {
		t.Fatal(err)
	}
	var vector struct{ Items []json.RawMessage }
	if err = json.Unmarshal(raw, &vector); err != nil {
		t.Fatal(err)
	}
	report, err := ParseRunReport(vector.Items[0])
	if err != nil || report.Get("version").Num() != 2 || report.Get("usageTotals").Get("inputTokens").Get("known").Num() != 4 {
		t.Fatal("closed feature report failed", err)
	}
	for _, mutate := range []func(*Value){func(v *Value) { v.Set("version", Number(3)) }, func(v *Value) { v.Set("extra", Object()) }, func(v *Value) { v.Get("usageTotals").Get("inputTokens").Set("known", Number(-1)) }, func(v *Value) { v.Get("usageTotals").Get("inputTokens").Set("extra", Number(0)) }, func(v *Value) { v.Get("usageTotals").Set("inputTokens", ObjectOf("known", Number(4))) }} {
		value := report.Clone()
		mutate(value)
		if _, err = ReadRunReport(value); err == nil {
			t.Fatal("malformed feature report admitted")
		}
	}
}

func TestUsageBudgetReportCurrentAbsoluteEndPattern(t *testing.T) {
	pattern := runReportV2.Get("definitions").Get("memory.json#").Get("properties").Get("createdAt").Get("pattern").Str()
	for _, text := range []string{"2026-10-25T12:00:00Z", "2026-10-25T12:00:00.000Z", "2026-10-25T12:00:00+01:00"} {
		if !reportPattern(pattern, text) {
			t.Fatalf("valid current timestamp refused: %q", text)
		}
	}
	for _, text := range []string{"2026-10-25T12:00:00Z\n", "2026-10-25T12:00:00.000Z\r\n", "2026-10-25T12:00:00Z\u2028", "2026-10-25T12:00:00Zsuffix"} {
		if reportPattern(pattern, text) {
			t.Fatalf("absolute-end timestamp suffix admitted: %q", text)
		}
	}
}

func TestUsageBudgetExactProviderNumbersAndEncodedCeilings(t *testing.T) {
	for _, token := range []string{"9007199254740991.1", "9007199254740992", "4.0000000000000001", "1e-999", "-1", "null", "\"4\""} {
		if _, err := usageResponseTokens("openai-compatible", false, `{"usage":{"prompt_tokens":`+token+`},"tool":{"fraction":1.25}}`); err == nil {
			t.Fatalf("accepted unsafe provider count %s", token)
		}
	}
	for _, token := range []string{"4", "4.0", "40e-1"} {
		out, err := usageResponseTokens("openai-compatible", false, `{"usage":{"prompt_tokens":`+token+`},"tool":{"fraction":1.25}}`)
		if err != nil || out["inputTokens"] != 4 {
			t.Fatalf("exact count %s: %v %v", token, out, err)
		}
	}
	for _, body := range []string{`{"max_tokens":5.0000000000000001}`, `{"max_tokens":9007199254740991.1}`, `{"max_tokens":5,"max_tokens":900}`, `{"max_tokens":0}`} {
		if _, err := encodedUsageOutputMaximum("openai-compatible", []byte(body)); err == nil {
			t.Fatalf("accepted unsafe ceiling %s", body)
		}
	}
	if n, err := encodedUsageOutputMaximum("openai-compatible", []byte(`{"max_tokens":5,"max_completion_tokens":9,"max_output_tokens":7}`)); err != nil || n != 9 {
		t.Fatalf("conflicting ceiling %v %v", n, err)
	}
}
