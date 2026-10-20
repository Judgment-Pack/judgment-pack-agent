package delegate

import (
	"crypto/rand"
	"encoding/hex"
	"strconv"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

func itoa(n int) string { return strconv.Itoa(n) }

func uuid() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	t := hex.EncodeToString(b[:])
	return t[0:8] + "-" + t[8:12] + "-" + t[12:16] + "-" + t[16:20] + "-" + t[20:32]
}

func now() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000Z") }

func ms(d time.Duration) int64 {
	if d < 0 {
		return 0
	}
	return int64(d / time.Millisecond)
}

// childFigures is a task's figures as the checkpoint carries them (agent#18). The caller holds
// taskMu.
func (c *coordinator) childFigures(task *contract.DelegatedTask, number int) contract.ProvenanceChildFigures {
	out := contract.ProvenanceChildFigures{Task: task.ID, Batch: int64(number), Profile: task.Profile, Model: task.Model, State: task.State,
		ToolCalls: task.ToolCalls, Tokens: task.Tokens, FirstByteMs: task.FirstByteMs}
	switch {
	case task.DurationMs != nil:
		out.DurationMs = *task.DurationMs
	default:
		c.childMu.Lock()
		started, ok := c.childStarts[task.ID]
		c.childMu.Unlock()
		if ok {
			out.DurationMs = ms(time.Since(started))
		}
	}
	if task.Partial != nil && *task.Partial {
		out.Partial = task.Partial
	}
	if task.Reused != nil {
		yes := true
		out.Reused = &yes
	}
	if c.run.Agent != nil {
		// On an agent capability the child's invocations, counted where each was admitted; no model
		// request or token figure is the coordinator's to give, and the member is absent, not 0
		// (agent#71).
		runs := int64(c.childRuns(task.ID))
		out.AgentRuns = &runs
	} else {
		requests := task.ModelRequests
		out.ModelRequests = &requests
	}
	return out
}

func totalOf(children []contract.ProvenanceChildFigures, duration int64) contract.ProvenanceFiguresTotal {
	total := contract.ProvenanceFiguresTotal{DurationMs: duration}
	for _, child := range children {
		// Model requests only: a child that reports none (an agent capability's) adds none, and a
		// total no child has a count for carries none.
		if child.ModelRequests != nil {
			if total.ModelRequests == nil {
				zero := int64(0)
				total.ModelRequests = &zero
			}
			*total.ModelRequests += *child.ModelRequests
		}
		total.ToolCalls += child.ToolCalls
		total.Tokens = addTokens(total.Tokens, child.Tokens)
		if child.AgentRuns != nil {
			if total.AgentRuns == nil {
				zero := int64(0)
				total.AgentRuns = &zero
			}
			*total.AgentRuns += *child.AgentRuns
		}
	}
	return total
}

// batchFigures are the finished batches' figures and the running one's, each child in its state.
func (c *coordinator) batchFigures() []contract.ProvenanceBatchFigures {
	c.batchMu.Lock()
	defer c.batchMu.Unlock()
	out := append([]contract.ProvenanceBatchFigures{}, c.figures...)
	if c.running != nil {
		var children []contract.ProvenanceChildFigures
		c.taskMu.Lock()
		for _, task := range c.running.tasks {
			children = append(children, c.childFigures(task, c.running.number))
		}
		c.taskMu.Unlock()
		out = append(out, contract.ProvenanceBatchFigures{Batch: int64(c.running.number), Children: children, Total: totalOf(children, ms(time.Since(c.running.started)))})
	}
	return out
}

// withFigures is the parent's checkpoint with the delegation figures, once a batch has started; on
// an agent capability from the first admitted invocation, so the response's count survives a
// restart (ADR-0007).
func (c *coordinator) withFigures(checkpoint contract.Checkpoint) contract.Checkpoint {
	batches := c.batchFigures()
	agent := c.run.Agent != nil
	runs := 0
	if agent {
		c.runsMu.Lock()
		runs = c.runs.parent
		c.runsMu.Unlock()
	}
	if len(batches) == 0 && (!agent || runs == 0) {
		return checkpoint
	}
	if batches == nil {
		batches = []contract.ProvenanceBatchFigures{}
	}
	c.batchMu.Lock()
	requests := c.parentCount
	c.batchMu.Unlock()
	parent := contract.ProvenanceParentFigures{Model: c.run.ModelName}
	if agent {
		// The parent's own invocations; the provider reports no request or token count, and none is
		// written (agent#71).
		held := int64(runs)
		parent.AgentRuns = &held
	} else {
		held := int64(requests)
		parent.ModelRequests = &held
		parent.Tokens = tokenFigures(checkpoint.Completions)
	}
	checkpoint.Delegation = &contract.ProvenanceDelegationReport{Batches: batches, Parent: parent}
	return checkpoint
}

// decorate is the parent's checkpoint as the coordinator writes it: the delegation figures and, on
// an agent capability, the handoff in progress.
func (c *coordinator) decorate(checkpoint contract.Checkpoint) contract.Checkpoint {
	checkpoint = c.withFigures(checkpoint)
	checkpoint.Handoff = c.handoffRecord()
	return checkpoint
}
