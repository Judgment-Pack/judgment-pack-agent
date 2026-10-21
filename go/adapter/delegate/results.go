// Package delegate is the delegation coordinator (agent#13 to agent#18) in Go: the port of
// packages/harness/delegation.ts, framework-neutral, wrapping an adapter. delegate_tasks is a host
// tool whose call runs the batch while the parent's step awaits it, so the parent gets its
// children's findings as that call's result in the same run. Each child is a run of the same
// adapter on its own gated session, in its profile's phase within the parent's (the host's
// Children capability enters it), with its own context, its budget, its references and its report.
// Eino's own subagents are not used (ADR-0004, decision 4).
package delegate

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/turn"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

// The coordinator's bounds and sentences (delegation.ts).
const (
	// ResultLimit is the longest findings a task record carries, in characters (RESULT_LIMIT).
	ResultLimit = 12000
	// synthesisReserve is the model requests kept for the parent to answer from its children's
	// findings.
	synthesisReserve = 1
	answerBytes      = 1000
	taskBytes        = 700
	keptExchanges    = 8
	// DefaultChildConcurrency is how many children a batch runs at once where the connection names
	// no capacity (DEFAULT_CHILD_CONCURRENCY).
	DefaultChildConcurrency = 2
	textFloor               = 200
)

const (
	answerNote          = "Findings are the children’s unverified working material, never approval or verification. Each task’s report is its summary, findings, citations, open questions, confidence and what it did not do; its result is its text. A result cut at its head, or a report marked reportCut, continues with read_window at its more cursor. Open questions are yours to decide; one more batch is allowed after a batch that returned them. partial: the task reached its time limit and its result is what it had written. reused: an identical earlier task’s result, given again with no model request. Resume a complete task by giving its id in resume."
	partialProblem      = "The task reached its time limit; its result is what it had written by then."
	batchPartialProblem = "The batch reached its time limit, its children’s time limits end to end; this task’s result is what it had written by then."
	timeLimit           = "The task reached its time limit. No automatic retry was made."
	batchLimit          = "The batch reached its time limit, its children’s time limits end to end. No automatic retry was made."
)

// problems joins two problems of one task, the first first, held to the record's bound.
func problems(said ...string) string {
	var held []string
	for _, text := range said {
		if text != "" {
			held = append(held, text)
		}
	}
	return runcontext.U16Slice(strings.Join(held, " "), 0, 1000)
}

// fits is whether a share of a result fits its bytes (its JSON-encoded text) and the record's
// limit in characters.
func fits(text string, bytes int) bool {
	return runcontext.U16Length(text) <= ResultLimit && runcontext.UTF8Bytes(string(runcontext.String(text).JSON())) <= bytes
}

// FindingsHead is findingsHead: the head of text that fits bytes and ResultLimit characters, its
// whole lines where one fits, else the start of the first; and the read_window cursor that reads
// on from there in the retained whole.
func FindingsHead(text string, bytes int) (head, cursor string) {
	lines := strings.Split(text, "\n")
	at := 0
	for at < len(lines) {
		next := lines[0]
		if at > 0 {
			next = head + "\n" + lines[at]
		}
		if !fits(next, bytes) {
			break
		}
		head = next
		at++
	}
	if at > 0 {
		return head, "line:" + strconv.Itoa(at)
	}
	cut := runcontext.EncodedPrefix(runcontext.U16Slice(lines[0], 0, ResultLimit), bytes)
	return cut, "line:0+" + strconv.Itoa(runcontext.U16Length(cut))
}

// tokenFigures sums the provider's token counts over completion rows; nil where none was reported.
func tokenFigures(rows []contract.Provenance) *contract.ProvenanceTokenFigures {
	var sum contract.ProvenanceTokenFigures
	any := false
	add := func(into **int64, from *int64) {
		if from == nil {
			return
		}
		any = true
		if *into == nil {
			zero := int64(0)
			*into = &zero
		}
		**into += *from
	}
	for _, row := range rows {
		add(&sum.InputTokens, row.Result.InputTokens)
		add(&sum.OutputTokens, row.Result.OutputTokens)
		add(&sum.ReasoningTokens, row.Result.ReasoningTokens)
		add(&sum.CacheReadTokens, row.Result.CacheReadTokens)
		add(&sum.CacheWriteTokens, row.Result.CacheWriteTokens)
	}
	if !any {
		return nil
	}
	return &sum
}

// addTokens adds token figures member by member; a member absent from both stays absent.
func addTokens(a, b *contract.ProvenanceTokenFigures) *contract.ProvenanceTokenFigures {
	if a == nil && b == nil {
		return nil
	}
	var sum contract.ProvenanceTokenFigures
	add := func(into **int64, from *int64) {
		if from == nil {
			return
		}
		if *into == nil {
			zero := int64(0)
			*into = &zero
		}
		**into += *from
	}
	for _, held := range []*contract.ProvenanceTokenFigures{a, b} {
		if held == nil {
			continue
		}
		add(&sum.InputTokens, held.InputTokens)
		add(&sum.OutputTokens, held.OutputTokens)
		add(&sum.ReasoningTokens, held.ReasoningTokens)
		add(&sum.CacheReadTokens, held.CacheReadTokens)
		add(&sum.CacheWriteTokens, held.CacheWriteTokens)
	}
	return &sum
}

// summaryOf is the whole lines of prose within a report's summary bound, or the start of its first.
func summaryOf(prose string) string {
	head := ""
	for _, line := range strings.Split(prose, "\n") {
		next := line
		if head != "" {
			next = head + "\n" + line
		}
		if runcontext.U16Length(next) > turn.ReportBounds.Summary {
			break
		}
		head = next
	}
	if head == "" {
		head = runcontext.U16Slice(prose, 0, turn.ReportBounds.Summary)
	}
	return strings.TrimSpace(head)
}

// hostReport is the report the coordinator writes where the child gave none it could read.
func hostReport(prose, problem string) *runcontext.Value {
	summary := summaryOf(prose)
	if summary == "" {
		summary = "The child wrote no findings text; what it asked of the parent is in openQuestions."
	}
	note := "The child wrote no report block; its findings are the result text."
	if problem != "" {
		note = "The child’s report block could not be read (" + problem + "); its findings are the result text."
	}
	return runcontext.ObjectOf("summary", runcontext.String(summary), "findings", runcontext.Array(), "citations", runcontext.Array(),
		"openQuestions", runcontext.Array(), "confidence", runcontext.String("unstated"),
		"notDone", runcontext.Array(runcontext.String(runcontext.U16Slice(note, 0, turn.ReportBounds.NotDoneText))))
}

func reportBytes(report *runcontext.Value) int { return runcontext.UTF8Bytes(string(report.JSON())) }

// FitReport is fitReport: a report held to bytes for the parent's answer, whole where it fits; else
// cut by whole items — what was not done, then findings from the last (with the citations only they
// cited), then citations, then open questions, and the summary's start last.
func FitReport(report *runcontext.Value, bytes int) (*runcontext.Value, bool) {
	bytes = max(bytes, runcontext.SubagentResultMinBytes)
	if reportBytes(report) <= bytes {
		return report, false
	}
	shown := report.Clone()
	over := func() bool { return reportBytes(shown) > bytes }
	pop := func(member string) bool {
		items := shown.Get(member).Items()
		if len(items) == 0 {
			return false
		}
		shown.Set(member, runcontext.Array(items[:len(items)-1]...))
		return true
	}
	for over() && pop("notDone") {
	}
	for over() && pop("findings") {
		cited := map[string]bool{}
		for _, finding := range shown.Get("findings").Items() {
			for _, id := range finding.Get("citations").Items() {
				cited[id.Str()] = true
			}
		}
		var kept []*runcontext.Value
		for _, citation := range shown.Get("citations").Items() {
			if cited[citation.Get("id").Str()] {
				kept = append(kept, citation)
			}
		}
		shown.Set("citations", runcontext.Array(kept...))
	}
	for over() && pop("citations") {
	}
	for over() && pop("openQuestions") {
	}
	if over() {
		without := shown.Clone()
		without.Set("summary", runcontext.String(""))
		room := bytes - (reportBytes(without) - 2)
		if room < 16 {
			room = 16
		}
		summary := strings.TrimSpace(runcontext.EncodedPrefix(shown.Get("summary").Str(), room))
		if summary == "" {
			summary = "No findings..."
		}
		shown.Set("summary", runcontext.String(summary))
	}
	return shown, true
}

// questionText is a question the child put to the person, as one open question's text.
func questionText(question contract.Question) string {
	var labels []string
	for _, option := range question.Options {
		labels = append(labels, option.Label)
	}
	text := question.Title + ": " + question.Question
	if len(labels) > 0 {
		text += " Options: " + strings.Join(labels, "; ") + "."
	}
	return runcontext.U16Slice(text, 0, turn.ReportBounds.QuestionText)
}

// openQuestion is one open question as the report carries it.
func openQuestion(kind, text, ref string) *runcontext.Value {
	out := runcontext.ObjectOf("kind", runcontext.String(kind), "text", runcontext.String(text))
	if ref != "" {
		out.Set("ref", runcontext.String(ref))
	}
	return out
}

// reportOf is a report value as the task record's union, read in the profile's shape.
func reportOf(profile turn.TaskProfile, report *runcontext.Value) *contract.DelegatedTaskReport {
	held, err := contract.DecodeDelegatedTaskReport(profile, report.JSON())
	if err != nil {
		var raw contract.DelegatedTaskReport
		raw.Raw = json.RawMessage(report.JSON())
		return &raw
	}
	return &held
}
