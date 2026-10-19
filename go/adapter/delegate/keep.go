package delegate

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/turn"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

type shownReport struct {
	shown *runcontext.Value
	cut   bool
}

// keep is a child's result, kept (agent#13, agent#16): its report, read in its profile's shape from
// its one report block or written by the coordinator, with what its run asked of the parent as open
// questions; and its text. The parent's answer shows the report first, whole where it fits the
// task's share, then as much of the text as the share leaves; past that the whole is retained in the
// chat's ledger and the record carries the head and the read_window cursor that reads on.
func (c *coordinator) keep(ctx context.Context, p *planned, profile turn.TaskProfile, task *contract.DelegatedTask, text string, share int, questions []*runcontext.Value, partial bool, summarizeWith *meter) {
	bodies, prose := turn.ReportBlocks(text)
	kind := "analyze"
	if profile.Report != nil {
		kind = *profile.Report
	}
	var report *runcontext.Value
	problem := ""
	switch {
	case len(bodies) == 1:
		var block map[string]json.RawMessage
		if err := json.Unmarshal([]byte(bodies[0]), &block); err != nil {
			problem = "it is not JSON"
		} else if _, why := turn.ReadReport(kind, block["report"]); why != "" {
			problem = why
		} else {
			report, _ = runcontext.ParseJSON(block["report"])
		}
	case len(bodies) > 1:
		problem = "the answer carried " + itoa(len(bodies)) + " report blocks, and one is read"
	}
	if report == nil {
		report = hostReport(prose, problem)
	}
	if len(questions) > 0 {
		held := append(append([]*runcontext.Value{}, questions...), report.Get("openQuestions").Items()...)
		if len(held) > turn.ReportBounds.OpenQuestions {
			held = held[:turn.ReportBounds.OpenQuestions]
		}
		report.Set("openQuestions", runcontext.Array(held...))
	}
	if partial && len(report.Get("notDone").Items()) < turn.ReportBounds.NotDone {
		report.Set("notDone", runcontext.Array(append(report.Get("notDone").Items(), runcontext.String("The task stopped before it finished."))...))
	}
	parts := []string{}
	if prose != "" {
		parts = append(parts, prose)
	}
	for _, body := range bodies {
		parts = append(parts, "```json\n"+body+"\n```")
	}
	if len(questions) > 0 {
		parts = append(parts, "```json\n"+string(runcontext.ObjectOf("openQuestions", runcontext.Array(questions...)).JSON())+"\n```")
	}
	whole := strings.Join(parts, "\n\n")
	room := share - textFloor
	if room < 0 {
		room = 0
	}
	fitted, cut := FitReport(report, room)
	room = share - reportBytes(fitted)
	shown := ""
	hasShown := strings.TrimSpace(prose) != ""
	if hasShown {
		shown = prose
	}
	var more *contract.DelegatedTaskResultCursor
	cursorAt := func(id, cursor string) *contract.DelegatedTaskResultCursor {
		return &contract.DelegatedTaskResultCursor{ReadWith: "read_window", ID: id, Cursor: cursor, TotalCharacters: int64(runcontext.U16Length(whole))}
	}
	if cut || hasShown && !fits(shown, room) {
		full, err := c.ledger.Retain(runcontext.Document{ID: "task-result-" + task.ID, Kind: "tool", Label: "Findings of delegated task “" + task.Title + "”", Value: runcontext.String(whole)}, false)
		if err != nil {
			full = nil
		}
		if full != nil {
			c.chat.Reveal(p.visible, full.ID)
		}
		if hasShown && !fits(shown, room) {
			head, cursor := "", ""
			if room >= textFloor {
				head, cursor = FindingsHead(prose, room)
			}
			hasShown = strings.TrimSpace(head) != ""
			shown = head
			if full != nil {
				if !hasShown {
					cursor = "line:0"
				}
				more = cursorAt(full.ID, cursor)
			} else {
				problem := "Only the head of these findings was kept: the retained-context bound left no room for the rest."
				c.edit(func() { task.Problem = &problem })
			}
			if summarizeWith != nil && !partial && p.request.Summarize != nil && *p.request.Summarize && full != nil && c.shared.grantFor(task, c.spent(task), p.budget, 1) >= 1 {
				// The child's own summary within the share, where the task asked and its budget allows.
				limit := room * 8 / 10
				prompt := profile.Instructions + "\n\n" + turn.ChildRules + " " + "Summarize the findings below for the parent task in at most " + itoa(limit) +
					" characters: keep every source reference and limitation, and say what the summary leaves out. The parent can still read the whole findings. Answer with the summary alone and call no tool." +
					"\n" + string(runcontext.ObjectOf("objective", runcontext.String(p.request.Objective), "findings", runcontext.String(prose)).JSON())
				ran := &childRun{}
				_ = c.runChild(ctx, p, profile, task, "Summarize your findings for: "+p.request.Objective, prompt, 1, false, summarizeWith, ran)
				if ran.failed == "" && strings.TrimSpace(ran.text) != "" && fits(ran.text, room) {
					shown, hasShown = ran.text, true
					more = cursorAt(full.ID, "line:0")
					yes := true
					c.edit(func() { task.Summarized = &yes })
				}
			}
		} else if full != nil {
			more = cursorAt(full.ID, "line:0")
		}
	}
	c.edit(func() {
		task.State = "complete"
		if hasShown {
			task.Result = &shown
		}
		task.More = more
		task.Report = reportOf(profile, report)
		if partial {
			yes := true
			task.Partial = &yes
		}
	})
	c.shownReports.Store(task.ID, shownReport{shown: fitted, cut: cut})
	thread := p.thread
	if thread == nil {
		thread = &runcontext.TaskThread{Profile: profile.ID, Title: task.Title, Visible: p.visible}
	}
	result := report.Get("summary").Str()
	if hasShown {
		result = shown
	}
	exchange := runcontext.ObjectOf("objective", runcontext.String(p.request.Objective), "result", runcontext.String(result))
	if more != nil {
		value, _ := runcontext.ParseJSON(mustJSON(more))
		exchange.Set("more", value)
	}
	if partial {
		exchange.Set("partial", runcontext.Bool(true))
	}
	thread.Exchanges = append(thread.Exchanges, exchange)
	if len(thread.Exchanges) > keptExchanges {
		thread.Exchanges = thread.Exchanges[len(thread.Exchanges)-keptExchanges:]
	}
	_ = c.chat.KeepTask([]string{task.ID}, thread)
}
