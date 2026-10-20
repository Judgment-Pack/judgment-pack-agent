package delegate

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/turn"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
)

// batchState is what one batch's children share.
type batchState struct {
	ctx    context.Context
	parent context.Context
	slots  func() int
	mu     sync.Mutex
	kept   map[string]runcontext.ReusableResult
}

func strPtr(s string) *string { return &s }

func failureOf(code, source, outcome string) *contract.Failure {
	held := failure.New(code, source, outcome)
	return &held
}

// outOfRequests is why a child that ran out of requests stopped (review row 6).
func (c *coordinator) outOfRequests(task *contract.DelegatedTask, budget turn.TaskBudget) string {
	c.childMu.Lock()
	why := c.refusedBy[task.ID]
	c.childMu.Unlock()
	switch {
	case task.ModelRequests >= budget.ModelRequests || why == "budget":
		return "This task used its " + itoa(int(budget.ModelRequests)) + " model requests, its profile’s budget, and stopped without findings."
	case why == "reserve":
		return "The response’s shared request allowance reached the request kept for the parent’s answer; this task stopped without findings."
	}
	return "This task used the " + itoa(int(task.ModelRequests)) + " model requests it was granted, its share of the response’s shared allowance, and stopped without findings."
}

// child runs one child of a batch.
func (c *coordinator) child(p *planned, task *contract.DelegatedTask, batch *batchState, share int, key string) {
	if p.profile == nil {
		c.edit(func() {
			task.State = "failed"
			task.Problem = strPtr("The requested task profile is not available.")
			task.FinishedAt = strPtr(now())
			zero := int64(0)
			task.DurationMs = &zero
		})
		c.publish(task)
		return
	}
	profile := *p.profile
	m := &meter{started: time.Now()}
	c.childMu.Lock()
	c.childStarts[task.ID] = m.started
	c.childMu.Unlock()
	if key != "" {
		batch.mu.Lock()
		earlier, ok := batch.kept[key]
		batch.mu.Unlock()
		if !ok && (c.run.Context == nil || c.run.Context.DelegationReuse == nil || *c.run.Context.DelegationReuse) {
			if held := c.chat.Reusable(key); held != nil {
				earlier, ok = *held, true
			}
		}
		if ok {
			c.keep(batch.ctx, p, profile, task, earlier.Text, share, earlier.Questions, false, nil)
			c.edit(func() {
				task.Reused = strPtr(earlier.Task)
				task.Model = strPtr(earlier.Model)
				duration := ms(time.Since(m.started))
				task.DurationMs = &duration
				task.FinishedAt = strPtr(now())
			})
			c.publish(task)
			return
		}
	}
	timeout := time.Duration(p.budget.Milliseconds) * time.Millisecond
	childCtx, stop := context.WithTimeout(batch.ctx, timeout)
	defer stop()
	run := &childRun{}
	defer func() {
		c.shared.release(task)
		c.taskMu.Lock()
		for at := range task.Steps {
			if task.Steps[at].State == "running" {
				task.Steps[at].State = "interrupted"
			}
		}
		c.taskMu.Unlock()
		m.mu.Lock()
		var rows []contract.Provenance
		for _, context := range m.contexts {
			rows = append(rows, context.Execution.State.Completions...)
		}
		first := m.firstByte
		m.mu.Unlock()
		c.edit(func() {
			task.Tokens = tokenFigures(rows)
			if first != nil {
				at := ms(first.Sub(m.started))
				task.FirstByteMs = &at
			}
			duration := ms(time.Since(m.started))
			task.DurationMs = &duration
		})
		if run.streamed != "" {
			id := task.ID
			c.deliver(contract.RunEvent{MessageProgress: &contract.RunEventMessageProgress{Type: "message_progress", Text: "", TaskID: &id}})
		}
		c.edit(func() { task.FinishedAt = strPtr(now()) })
		c.publish(task)
	}()
	c.edit(func() { task.State = "running" })
	c.publish(task)
	m.started = time.Now()
	c.childMu.Lock()
	c.childStarts[task.ID] = m.started
	c.childMu.Unlock()
	// On the agent capability a child's run is one invocation, counted where it is admitted; the
	// provider's model loop is its own, and no request is granted.
	limit := 1
	if c.run.Agent == nil {
		limit = c.shared.grantFor(task, c.spent(task), p.budget, batch.slots())
	}
	if limit < 1 {
		c.edit(func() {
			task.State = "failed"
			task.Problem = strPtr("No model request is left for this task: the response keeps its last request for the parent’s answer.")
			task.Failure = failureOf("request-limit", "desk", "not-started")
		})
		return
	}
	childCtx, handBack := context.WithCancel(childCtx)
	defer handBack()
	c.childMu.Lock()
	c.childStops[task.ID] = handBack
	c.childMu.Unlock()
	run.err = c.runChild(childCtx, p, profile, task, p.request.Objective, c.brief(p, profile), limit, true, m, run)
	written := strings.TrimSpace(strings.Join(nonEmpty(run.text, run.progress), "\n\n"))
	c.childMu.Lock()
	handedBack := c.handedBack[task.ID]
	c.childMu.Unlock()
	timedOut := errors.Is(childCtx.Err(), context.DeadlineExceeded) && batch.ctx.Err() == nil && !handedBack
	limited := childCtx.Err() != nil && !handedBack
	c.taskMu.Lock()
	problem := task.Problem
	c.taskMu.Unlock()
	switch {
	case batch.parent.Err() != nil:
		c.edit(func() {
			task.State = "interrupted"
			task.Problem = strPtr("Stopped with the parent run.")
		})
	case handedBack:
		// A source call that needs the parent's research choice is a question for the parent; what
		// the child had written is kept, partial, and the task is complete (agent#16, review row 7).
		asked := handedBackText
		if problem != nil {
			asked = *problem
		}
		c.keep(batch.ctx, p, profile, task, written, share, append(append([]*runcontext.Value{}, run.questions...), openQuestion("question", asked, "")), true, nil)
		c.edit(func() {
			held := ""
			if task.Problem != nil && *task.Problem != asked {
				held = *task.Problem
			}
			task.Problem = strPtr(problems(asked, held))
		})
	case limited && (written != "" || len(run.questions) > 0):
		c.keep(childCtx, p, profile, task, strings.Join(nonEmpty(run.text, run.progress), "\n\n"), share, run.questions, true, nil)
		why := batchPartialProblem
		if timedOut {
			why = partialProblem
		}
		c.edit(func() {
			held := ""
			if task.Problem != nil {
				held = *task.Problem
			}
			task.Problem = strPtr(problems(why, held))
		})
	case timedOut:
		why := run.failed
		if why == "" {
			why = timeLimit
		}
		c.edit(func() {
			task.State = "failed"
			task.Problem = strPtr(why)
			task.Failure = failureOf("timeout", "desk", "unknown")
		})
	case limited:
		c.edit(func() {
			task.State = "failed"
			task.Problem = strPtr(batchLimit)
			task.Failure = failureOf("timeout", "desk", "unknown")
		})
	case run.err != nil && !errors.Is(run.err, runcontext.ErrExecutionPaused):
		held := failure.Normalize(run.err, "agent", time.Now())
		c.edit(func() {
			task.State = "failed"
			task.Failure = &held
			task.Problem = strPtr(runcontext.U16Slice(failure.Message(held), 0, 1000))
		})
	case run.failed != "" || strings.TrimSpace(run.text) == "" && len(run.questions) == 0:
		why := run.failed
		var failed *contract.Failure
		if why == "" {
			if run.paused || errors.Is(run.err, runcontext.ErrExecutionPaused) {
				c.taskMu.Lock()
				why = c.outOfRequests(task, p.budget)
				c.taskMu.Unlock()
				failed = failureOf("request-limit", "desk", "not-started")
			} else {
				why = "The task returned no complete findings."
			}
		}
		var report *runcontext.Value
		if len(run.questions) > 0 {
			report = hostReport(written, "")
			report.Set("openQuestions", runcontext.Array(run.questions...))
			report.Set("notDone", runcontext.Array(runcontext.String("The child’s run failed before it finished.")))
			shown, cut := FitReport(report, share)
			c.shownReports.Store(task.ID, shownReport{shown: shown, cut: cut})
		}
		c.edit(func() {
			task.State = "failed"
			if failed != nil {
				task.Failure = failed
			}
			task.Problem = strPtr(runcontext.U16Slice(why, 0, 1000))
			if report != nil {
				task.Report = reportOf(profile, report)
			}
		})
	default:
		c.keep(childCtx, p, profile, task, run.text, share, run.questions, false, m)
		// Kept for reuse only where the child made no counted tool call (agent#17, review row 1).
		c.taskMu.Lock()
		calls := task.ToolCalls
		model := c.run.ModelName
		if task.Model != nil {
			model = *task.Model
		}
		c.taskMu.Unlock()
		if key != "" && calls == 0 {
			kept := runcontext.ReusableResult{Task: task.ID, Model: model, Text: run.text, Questions: run.questions}
			batch.mu.Lock()
			batch.kept[key] = kept
			batch.mu.Unlock()
			c.batchMu.Lock()
			c.pending = append(c.pending, pendingReuse{key: key, kept: kept})
			c.batchMu.Unlock()
		}
	}
}

func nonEmpty(parts ...string) []string {
	var out []string
	for _, part := range parts {
		if strings.TrimSpace(part) != "" {
			out = append(out, part)
		}
	}
	return out
}

// runBatch is one batch: planned whole before any task exists, its children run within their
// limits and the batch's.
func (c *coordinator) runBatch(ctx context.Context, request contract.DelegatedTaskRequest) ([]*contract.DelegatedTask, error) {
	if err := ctx.Err(); err != nil {
		return nil, failure.Cancelled
	}
	var plans []*planned
	for _, item := range request.Tasks {
		p, err := c.plan(item)
		if err != nil {
			return nil, err
		}
		plans = append(plans, p)
	}
	if err := c.claimBatch(); err != nil {
		return nil, err
	}
	c.batchMu.Lock()
	c.pending = nil
	c.batchMu.Unlock()
	c.deliver(contract.RunEvent{MessageProgress: &contract.RunEventMessageProgress{Type: "message_progress", Text: ""}})
	tasks := c.recordTasks(plans)
	keys := make([]string, len(plans))
	for i, p := range plans {
		if p.request.Resume == nil && p.profile != nil {
			keys[i] = reuseKey(p, *tasks[i].Model)
		}
	}
	firstOf := make([]int, len(keys))
	for i, key := range keys {
		firstOf[i] = i
		if key != "" {
			for j := 0; j < i; j++ {
				if keys[j] == key {
					firstOf[i] = j
					break
				}
			}
		}
	}
	var order []int
	for i := range tasks {
		if firstOf[i] == i {
			order = append(order, i)
		}
	}
	for i := range tasks {
		if firstOf[i] != i {
			order = append(order, i)
		}
	}
	started := time.Now()
	c.batchMu.Lock()
	number := 1
	if len(c.figures) > 0 {
		number = int(c.figures[len(c.figures)-1].Batch) + 1
	}
	c.running = &runningBatch{tasks: tasks, number: number, started: started}
	c.batchMu.Unlock()
	beside := 0
	for _, task := range tasks {
		beside += taskBytes + runcontext.UTF8Bytes(string(runcontext.String(task.Objective).JSON()))
		references := runcontext.Array()
		for _, id := range task.References {
			references.Append(runcontext.String(id))
		}
		beside += runcontext.UTF8Bytes(string(references.JSON()))
	}
	share := (c.policy.ToolResultBytes - answerBytes - beside) / len(tasks)
	if share < 1000 {
		share = 1000
	}
	var total time.Duration
	for _, p := range plans {
		total += time.Duration(p.budget.Milliseconds) * time.Millisecond
	}
	batchCtx, stop := context.WithTimeout(ctx, total)
	defer stop()
	concurrency := c.concurrency()
	var mu sync.Mutex
	next, finished := 0, 0
	done := make([]chan struct{}, len(tasks))
	for i := range done {
		done[i] = make(chan struct{})
	}
	batch := &batchState{ctx: batchCtx, parent: ctx, kept: map[string]runcontext.ReusableResult{}, slots: func() int {
		mu.Lock()
		defer mu.Unlock()
		left := len(tasks) - finished
		if left < concurrency {
			return left
		}
		return concurrency
	}}
	var workers sync.WaitGroup
	for range concurrency {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for {
				mu.Lock()
				if next >= len(order) || batchCtx.Err() != nil {
					mu.Unlock()
					return
				}
				i := order[next]
				next++
				mu.Unlock()
				if firstOf[i] != i {
					<-done[firstOf[i]]
				}
				c.child(plans[i], tasks[i], batch, share, keys[i])
				close(done[i])
				mu.Lock()
				finished++
				mu.Unlock()
			}
		}()
	}
	workers.Wait()
	var children []contract.ProvenanceChildFigures
	c.taskMu.Lock()
	for _, task := range tasks {
		children = append(children, c.childFigures(task, number))
	}
	c.taskMu.Unlock()
	c.batchMu.Lock()
	c.figures = append(c.figures, contract.ProvenanceBatchFigures{Batch: int64(number), Children: children, Total: totalOf(children, ms(time.Since(started)))})
	c.running = nil
	for _, task := range tasks {
		if report, ok := c.shownReports.Load(task.ID); ok && len(report.(shownReport).shown.Get("openQuestions").Items()) > 0 {
			c.extraBatch = true
		}
	}
	c.batchMu.Unlock()
	return tasks, nil
}

// recordTasks are a planned batch's task records, each published queued.
func (c *coordinator) recordTasks(plans []*planned) []*contract.DelegatedTask {
	tasks := make([]*contract.DelegatedTask, len(plans))
	for i, p := range plans {
		budget := p.budget
		task := &contract.DelegatedTask{ID: uuid(), ParentRunID: c.parentRunID, Title: p.request.Title, Objective: p.request.Objective, Profile: p.request.Profile,
			State: "queued", CreatedAt: now(), Steps: []contract.DelegatedTaskStepsItem{}, Budget: &budget, Resumes: p.request.Resume}
		if p.profile != nil {
			task.Model = strPtr(c.childModelName(*p.profile))
		}
		for _, entry := range p.references {
			task.References = append(task.References, entry.ID)
		}
		tasks[i] = task
		c.publish(task)
	}
	return tasks
}

// batchAnswer is what the parent is answered for a batch: each task's state, its report and its
// text or why it has neither, and its limits.
func (c *coordinator) batchAnswer(tasks []*contract.DelegatedTask) runcontext.ToolResult {
	list := runcontext.Array()
	for _, task := range tasks {
		item := runcontext.ObjectOf("id", runcontext.String(task.ID), "title", runcontext.String(task.Title), "objective", runcontext.String(task.Objective), "profile", runcontext.String(task.Profile))
		if task.References != nil {
			references := runcontext.Array()
			for _, id := range task.References {
				references.Append(runcontext.String(id))
			}
			item.Set("references", references)
		}
		item.Set("state", runcontext.String(task.State))
		if task.Resumes != nil {
			item.Set("resumes", runcontext.String(*task.Resumes))
		}
		if held, ok := c.shownReports.Load(task.ID); ok {
			report := held.(shownReport)
			item.Set("report", report.shown)
			if report.cut {
				item.Set("reportCut", runcontext.Bool(true))
			}
		}
		if task.Result != nil {
			item.Set("result", runcontext.String(*task.Result))
		}
		if task.More != nil {
			value, _ := runcontext.ParseJSON(mustJSON(task.More))
			item.Set("more", value)
		}
		if task.Summarized != nil && *task.Summarized {
			item.Set("summarized", runcontext.Bool(true))
		}
		if task.Partial != nil && *task.Partial {
			item.Set("partial", runcontext.Bool(true))
		}
		if task.Reused != nil {
			item.Set("reused", runcontext.String(*task.Reused))
		}
		if task.Problem != nil {
			item.Set("problem", runcontext.String(*task.Problem))
		}
		item.Set("used", runcontext.ObjectOf("modelRequests", runcontext.Number(float64(task.ModelRequests)), "toolCalls", runcontext.Number(float64(task.ToolCalls))))
		if task.Budget != nil {
			value, _ := runcontext.ParseJSON(mustJSON(task.Budget))
			item.Set("budget", value)
		}
		list.Append(item)
	}
	return runcontext.Answer(runcontext.ObjectOf("tasks", list, "note", runcontext.String(answerNote)))
}

// concurrency is how many children a batch runs at once (agent#17): as many as the run's connection
// serves requests at once, the concurrentRequests of the host's answer for it, and
// DefaultChildConcurrency where the answer names none.
func (c *coordinator) concurrency() int {
	if c.run.Agent != nil {
		// The provider account of an agent capability is exclusive: one child at a time.
		return 1
	}
	if c.run.Models == nil {
		return DefaultChildConcurrency
	}
	named, err := c.run.Models()
	if err != nil || named.ConcurrentRequests == nil {
		return DefaultChildConcurrency
	}
	return max(1, *named.ConcurrentRequests)
}
