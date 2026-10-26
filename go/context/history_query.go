package runcontext

import (
	"encoding/json"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

type HistoryPlanStep struct {
	Execution string `json:"execution"`
	Revision  string `json:"revision"`
	ID        string `json:"id"`
}
type HistoryEventQuery struct {
	AfterSequence *int
	Limit         *int
	Types         []string
	Tool          *string
	Step          *HistoryPlanStep
	From          *string
	Through       *string
	Text          *string
}
type HistoryEventRow struct {
	Sequence       int                    `json:"sequence"`
	CommitSequence int                    `json:"commitSequence"`
	RecordedAt     string                 `json:"recordedAt"`
	Scope          DurableScope           `json:"scope"`
	Event          *Value                 `json:"event"`
	Source         HistoryObjectRef       `json:"source"`
	Representation *ArchiveRepresentation `json:"representation,omitempty"`
}
type HistoryEventPage struct {
	Revision     string            `json:"revision"`
	Items        []HistoryEventRow `json:"items"`
	NextSequence int               `json:"nextSequence"`
	HasMore      bool              `json:"hasMore"`
}

func historyStepMatches(v *Value, step *HistoryPlanStep) bool {
	if v.Get("execution").Str() != step.Execution || v.Get("revision").Str() != step.Revision {
		return false
	}
	switch v.Get("type").Str() {
	case "plan_revised":
		for _, change := range v.Get("diff").Items() {
			if change.Get("id").Str() == step.ID {
				return true
			}
		}
	case "plan_departed":
		for _, change := range v.Get("departures").Items() {
			if change.Get("step").Str() == step.ID {
				return true
			}
		}
	case "plan_completion_refused":
		return v.Get("step").Str() == step.ID
	}
	return false
}

// QueryHistoryEvents filters exact public stored events. Store sequence owns order even when time moves backward.
func QueryHistoryEvents(view HistoryQueryView, q HistoryEventQuery) (HistoryEventPage, error) {
	out := HistoryEventPage{Items: []HistoryEventRow{}}
	invalid := func() (HistoryEventPage, error) { return HistoryEventPage{}, &HistoryInputInvalid{} }
	if view == nil {
		return invalid()
	}
	head := view.Head()
	if !validStorageID(head.RunID) || !validStorageID(head.Revision) || head.CommitCount < 1 {
		return invalid()
	}
	out.Revision = head.Revision
	after, limit := 0, 100
	if q.AfterSequence != nil {
		after = *q.AfterSequence
	}
	if q.Limit != nil {
		limit = *q.Limit
	}
	if after < 0 || after > head.EventCount || limit < 1 || limit > 100 || len(q.Types) > 64 || q.Tool != nil && !validStorageID(*q.Tool) || q.From != nil && !historyTimestamp(String(*q.From)) || q.Through != nil && !historyTimestamp(String(*q.Through)) || q.From != nil && q.Through != nil && *q.From > *q.Through || q.Text != nil && (len(utf16.Encode([]rune(*q.Text))) > 2000 || !utf8.ValidString(*q.Text)) {
		return invalid()
	}
	types := map[string]bool{}
	for _, kind := range q.Types {
		if !validStorageID(kind) || types[kind] {
			return invalid()
		}
		types[kind] = true
	}
	if q.Step != nil && (!validStorageID(q.Step.Execution) || !historySHA256.MatchString(q.Step.Revision) || !validStorageID(q.Step.ID)) {
		return invalid()
	}
	matches := []HistoryEventRow{}
	for _, commit := range view.Commits() {
		for index, source := range commit.Events {
			sequence := commit.EventStart + index
			if sequence <= after || q.From != nil && commit.RecordedAt < *q.From || q.Through != nil && commit.RecordedAt > *q.Through {
				continue
			}
			scope, event, representation, err := view.historyEventDisplay(source)
			if err != nil {
				return invalid()
			}
			if q.Types != nil && !types[event.Get("type").Str()] || q.Tool != nil && event.Get("name").Str() != *q.Tool && event.Get("tool").Str() != *q.Tool || q.Step != nil && !historyStepMatches(event, q.Step) || q.Text != nil && !strings.Contains(CanonicalJSON(event), *q.Text) {
				continue
			}
			matches = append(matches, HistoryEventRow{Sequence: sequence, CommitSequence: commit.Sequence, RecordedAt: commit.RecordedAt, Scope: scope, Event: event.Clone(), Source: source, Representation: representation})
		}
	}
	size := 0
	for index, row := range matches {
		if index >= limit {
			break
		}
		raw, err := historyJSONBytes(map[string]any{"sequence": row.Sequence, "commitSequence": row.CommitSequence, "recordedAt": row.RecordedAt, "scope": row.Scope, "event": json.RawMessage(row.Event.JSON()), "source": row.Source, "representation": row.Representation})
		if err != nil {
			return invalid()
		}
		if size+len(raw) > StorageTransactionBytes {
			break
		}
		size += len(raw)
		out.Items = append(out.Items, row)
	}
	out.NextSequence = after
	if len(out.Items) > 0 {
		out.NextSequence = out.Items[len(out.Items)-1].Sequence
	}
	out.HasMore = len(out.Items) < len(matches)
	return out, nil
}

type HistoryRelationRow struct {
	CommitSequence int              `json:"commitSequence"`
	RecordedAt     string           `json:"recordedAt"`
	Source         HistoryObjectRef `json:"source"`
	Observation    *Value           `json:"observation"`
}
type HistoryRelations struct {
	Kind string               `json:"kind"`
	Rows []HistoryRelationRow `json:"rows"`
}

// FindHistoryRelations reports only explicit host evidence, never a job ID inferred from prose.
func FindHistoryRelations(view HistoryQueryView, kind, id string) (HistoryRelations, error) {
	out := HistoryRelations{Kind: "unavailable", Rows: []HistoryRelationRow{}}
	if view == nil || (kind != "job" && kind != "artifact" && kind != "dialect") || !validStorageID(id) {
		return out, &HistoryInputInvalid{}
	}
	for _, commit := range view.Commits() {
		for _, source := range commit.Observations {
			observation, err := view.historyObservationDisplay(source)
			if err != nil {
				return out, err
			}
			if observation.Get("kind").Str() == "relation" && observation.Get("target").Get("kind").Str() == kind && observation.Get("target").Get("id").Str() == id {
				out.Rows = append(out.Rows, HistoryRelationRow{CommitSequence: commit.Sequence, RecordedAt: commit.RecordedAt, Source: source, Observation: observation})
			}
		}
	}
	if len(out.Rows) > 0 {
		out.Kind = "observed"
	}
	return out, nil
}
