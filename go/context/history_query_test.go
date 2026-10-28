package runcontext

import (
	"context"
	"errors"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"strings"
	"testing"
	"time"
)

func TestHistoryQueryActualEventsAndUnavailableProseRelation(t *testing.T) {
	ctx := context.Background()
	tick := 0
	store := NewMemoryRunStore(MemoryRunStoreOptions{Now: func() time.Time { tick++; return time.Date(2026, 10, 25, 0, 0, 30-tick, 0, time.UTC) }})
	source, _ := NewStoredChatScope(store, "chat", NewChatContext("shared"), nil)
	session, err := ContextualSession(ctx, historyStoredSession(store, source, "r", false))
	if err != nil {
		t.Fatal(err)
	}
	boundary := NewBoundary(session)
	historyAssertNoFailure(t, boundary.Open())
	for _, text := range []string{"job fake-123 is only prose", "second"} {
		events, _ := boundary.Event(contract.RunEvent{Message: &contract.RunEventMessage{Type: "message", Text: text}})
		historyAssertNoFailure(t, events)
	}
	events, _ := boundary.Event(contract.RunEvent{End: &contract.RunEventEnd{Type: "end"}})
	historyAssertNoFailure(t, events)
	boundary.Close()
	view, err := ReadRetainedRunHistory(ctx, store, "r")
	if err != nil {
		t.Fatal(err)
	}
	one := 1
	first, err := QueryHistoryEvents(view, HistoryEventQuery{Types: []string{"message"}, Limit: &one})
	if err != nil || len(first.Items) != 1 || !first.HasMore || first.Items[0].Event.Get("text").Str() != "job fake-123 is only prose" {
		t.Fatal("first query", err)
	}
	next, err := QueryHistoryEvents(view, HistoryEventQuery{Types: []string{"message"}, Limit: &one, AfterSequence: &first.NextSequence})
	if err != nil || len(next.Items) != 1 || next.HasMore || next.Items[0].Sequence <= first.NextSequence || next.Items[0].RecordedAt >= first.Items[0].RecordedAt {
		t.Fatal("sequence order follows clock", err)
	}
	text := "fake-123"
	matching, err := QueryHistoryEvents(view, HistoryEventQuery{Text: &text})
	if err != nil || len(matching.Items) != 1 {
		t.Fatal("text query", err)
	}
	from := first.Items[0].RecordedAt
	matching, err = QueryHistoryEvents(view, HistoryEventQuery{Types: []string{"message"}, From: &from})
	if err != nil || len(matching.Items) != 1 {
		t.Fatal("time query", err)
	}
	relation, err := FindHistoryRelations(view, "job", "fake-123")
	if err != nil || relation.Kind != "unavailable" || len(relation.Rows) != 0 {
		t.Fatal("prose granted a job relation", err)
	}
	first.Items[0].Event.Set("text", String("changed"))
	matching, _ = QueryHistoryEvents(view, HistoryEventQuery{Types: []string{"message"}})
	if matching.Items[0].Event.Get("text").Str() == "changed" {
		t.Fatal("query value alias")
	}
	literal := `"\uD800"`
	if _, err := QueryHistoryEvents(view, HistoryEventQuery{Text: &literal}); err != nil {
		t.Fatal("literal search mistaken for escaped JSON", err)
	}
	tooLong := strings.Repeat("😀", 1001)
	if _, err := QueryHistoryEvents(view, HistoryEventQuery{Text: &tooLong}); err == nil {
		t.Fatal("query text UTF16 bound bypassed")
	}
	tooMany := 101
	beyond := view.Head().EventCount + 1
	badTime := "yesterday"
	for _, bad := range []HistoryEventQuery{{Limit: &tooMany}, {AfterSequence: &beyond}, {Types: []string{"message", "message"}}, {From: &badTime}, {Step: &HistoryPlanStep{Execution: "r", Revision: "not-a-digest", ID: "step"}}} {
		var invalid *HistoryInputInvalid
		if _, err := QueryHistoryEvents(view, bad); !errors.As(err, &invalid) {
			t.Fatal("bad query accepted", err)
		}
	}
}
