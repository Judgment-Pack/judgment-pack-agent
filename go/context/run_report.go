package runcontext

import (
	"encoding/json"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

const MaxRunReportBytes = 4 * 1024 * 1024
const MaxRunReportEvents = 400

// Each intended source is copied whole, in recorded array order.
var runReportSources = []string{"id", "phase", "status", "unit", "used", "limit", "requestCap", "grants", "reason", "explanation", "prompts", "sources", "sourceIdentities", "skills", "critic", "delegation", "usage", "childUsage", "failover", "completions", "plan", "planState"}
var runReportExcluded = []string{"schema", "digest", "retainUntil", "records", "requestKey", "operations", "context", "testCalls", "durable", "handoff", "approval", "outputGuard", "memories", "draft", "policy", "crossProvider", "compaction", "steers", "usageBudget", "turnRoute", "repeatTools"}

type RunReportInputInvalid struct{}

func (*RunReportInputInvalid) Error() string {
	return "RunReportInputInvalid: a valid complete or stopped checkpoint is required."
}

type RunReportPrefixInvalid struct{}

func (*RunReportPrefixInvalid) Error() string {
	return "RunReportPrefixInvalid: the supplied event prefix does not match the terminal head."
}

type RunReportOverflow struct{}

func (*RunReportOverflow) Error() string {
	return "RunReportOverflow: the report or retained event prefix exceeds its bounds."
}

func terminalRunReport(value *Value) (*contract.Checkpoint, error) {
	if value == nil {
		return nil, &RunReportInputInvalid{}
	}
	cp, err := ReadExecution(value)
	if err != nil || cp == nil || (cp.Status != "complete" && cp.Status != "stopped") {
		return nil, &RunReportInputInvalid{}
	}
	return cp, nil
}
func runReportValue(checkpoint contract.Checkpoint) (*Value, error) {
	raw, err := json.Marshal(checkpoint)
	if err != nil {
		return nil, &RunReportInputInvalid{}
	}
	value, err := ParseJSON(raw)
	if err != nil {
		return nil, &RunReportInputInvalid{}
	}
	return value, nil
}

func projectRepeatTools(value *Value) (*Value, error) {
	if value.Get("version").Num() != 3 || !value.Has("repeatTools") {
		return nil, nil
	}
	policy := value.Get("policy")
	if policy.Get("repeatTools.mode").Get("value").Str() != "on" || !policy.Has("repeatTools.remindAt") || !policy.Has("repeatTools.stopAt") {
		return nil, &RunReportInputInvalid{}
	}
	actual := Object()
	for _, key := range []string{"repeatTools.mode", "repeatTools.remindAt", "repeatTools.stopAt"} {
		actual.Set(key, policy.Get(key).Clone())
	}
	rows := Array()
	for _, row := range value.Get("repeatTools").Get("executions").Items() {
		summary := Object()
		for _, key := range []string{"execution", "remindAt", "stopAt", "count", "reminded", "reminderDelivered", "stopped"} {
			summary.Set(key, row.Get(key).Clone())
		}
		rows.Append(summary)
	}
	return ObjectOf("executions", rows, "policy", actual), nil
}

// ProjectRunReport makes no clock/store calls and invents no absent provider facts or totals.
func ProjectRunReport(checkpoint contract.Checkpoint) (*Value, error) {
	value, err := runReportValue(checkpoint)
	if err != nil {
		return nil, err
	}
	if _, err = terminalRunReport(value); err != nil {
		return nil, err
	}
	out := ObjectOf("version", Number(1), "checkpointVersion", value.Get("version").Clone())
	if checkpoint.Version == 3 {
		out.Set("version", Number(2))
		if route := value.Get("turnRoute"); route != nil {
			out.Set("route", route.Clone())
		}
		if checkpoint.UsageBudget != nil {
			journal, err := readUsageJournal(value.Get("usageBudget"))
			if err != nil {
				return nil, err
			}
			totals, err := UsageJournalTotals(*journal)
			if err != nil {
				return nil, err
			}
			raw, _ := json.Marshal(totals)
			held, err := ParseJSON(raw)
			if err != nil {
				return nil, err
			}
			out.Set("usageTotals", held)
		}
	}
	repeats, e := projectRepeatTools(value)
	if e != nil {
		return nil, e
	}
	if repeats != nil {
		out.Set("repeatTools", repeats)
	}
	for _, name := range runReportSources {
		if member := value.Get(name); member != nil {
			out.Set(name, member.Clone())
		}
	}
	if _, err = CanonicalRunReport(out); err != nil {
		return nil, err
	}
	return out, nil
}

// CanonicalRunReport returns the same canonical UTF-8 text as TypeScript. It serializes without
// validating retained input; use ReadRunReport or ParseRunReport for that boundary.
func CanonicalRunReport(report *Value) (string, error) {
	text := CanonicalJSON(report)
	if UTF8Bytes(text) > MaxRunReportBytes {
		return "", &RunReportOverflow{}
	}
	return text, nil
}

// BuildRunReport validates supplied pages against a terminal head. Cursors are opaque.
// The caller fetches from the beginning, then each returned cursor, within one host-bound
// resource. Pages have no predecessor proof: validation cannot prove completeness or
// authenticate the host. Corroborating events never expose raw journal payloads.
func BuildRunReport(checkpoint contract.Checkpoint, head RunHead, pages []RunEventPage) (*Value, error) {
	report, err := ProjectRunReport(checkpoint)
	if err != nil {
		return nil, err
	}
	value, err := runReportValue(checkpoint)
	if err != nil {
		return nil, err
	}
	expected := CanonicalJSON(value)
	invalid := func() (*Value, error) { return nil, &RunReportPrefixInvalid{} }
	if head.RunID != checkpoint.ID || !validStorageID(head.RunID) || !validStorageID(head.Revision) || !validStorageID(head.CommitID) || !validStorageID(head.Cursor) || len(head.Checkpoint) == 0 || len(head.Checkpoint) > MaxRunReportBytes {
		return invalid()
	}
	saved, err := ParseJSON(head.Checkpoint)
	if err != nil {
		return invalid()
	}
	if _, err = terminalRunReport(saved); err != nil || CanonicalJSON(saved) != expected {
		return invalid()
	}
	if len(pages) == 0 {
		return invalid()
	}
	if len(pages) > MaxRunReportEvents {
		return nil, &RunReportOverflow{}
	}
	count, total := 0, 0
	last, root := "", ""
	cursors := map[string]bool{}
	for at, page := range pages {
		if len(page.Items) == 0 || len(page.Items) > StoragePageLimit || page.HasMore != (at < len(pages)-1) {
			return invalid()
		}
		for _, row := range page.Items {
			count++
			if count > MaxRunReportEvents {
				return nil, &RunReportOverflow{}
			}
			if !validStorageID(row.Cursor) || cursors[row.Cursor] {
				return invalid()
			}
			cursors[row.Cursor] = true
			last = row.Cursor
			total += len(row.Bytes)
			if total > MaxRunReportBytes {
				return nil, &RunReportOverflow{}
			}
			held, e := ReadStoredRunEvent(row.Bytes)
			if e != nil {
				return invalid()
			}
			var eventCheckpoint *contract.Checkpoint
			var eventValue *Value
			if held.Event.ExecutionCheckpoint != nil {
				eventCheckpoint = &held.Event.ExecutionCheckpoint.Checkpoint
			} else if held.Event.BudgetPaused != nil {
				eventCheckpoint = &held.Event.BudgetPaused.Checkpoint
			}
			if eventCheckpoint != nil {
				eventValue, e = runReportValue(*eventCheckpoint)
				if e != nil {
					return invalid()
				}
				if _, e = ReadExecution(eventValue); e != nil || eventCheckpoint.ID != held.Scope.Execution {
					return invalid()
				}
			}
			if held.Scope.Task == "" {
				if held.Scope.Execution != checkpoint.ID {
					return invalid()
				}
				if eventCheckpoint != nil {
					root = CanonicalJSON(eventValue)
				}
			} else if held.Scope.Execution != held.Scope.Task {
				return invalid()
			}
		}
		if page.NextCursor != last {
			return invalid()
		}
	}
	if last != head.Cursor || root != expected {
		return invalid()
	}
	return report, nil
}
