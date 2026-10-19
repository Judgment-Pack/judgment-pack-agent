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
		ModelRequests: task.ModelRequests, ToolCalls: task.ToolCalls, Tokens: task.Tokens, FirstByteMs: task.FirstByteMs}
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
	return out
}

func totalOf(children []contract.ProvenanceChildFigures, duration int64) contract.ProvenanceFiguresTotal {
	total := contract.ProvenanceFiguresTotal{DurationMs: duration}
	for _, child := range children {
		total.ModelRequests += child.ModelRequests
		total.ToolCalls += child.ToolCalls
		total.Tokens = addTokens(total.Tokens, child.Tokens)
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

// withFigures is the parent's checkpoint with the delegation figures, once a batch has started.
func (c *coordinator) withFigures(checkpoint contract.Checkpoint) contract.Checkpoint {
	batches := c.batchFigures()
	if len(batches) == 0 {
		return checkpoint
	}
	c.batchMu.Lock()
	requests := c.parentCount
	c.batchMu.Unlock()
	parent := contract.ProvenanceParentFigures{Model: c.run.ModelName, ModelRequests: int64(requests), Tokens: tokenFigures(checkpoint.Completions)}
	checkpoint.Delegation = &contract.ProvenanceDelegationReport{Batches: batches, Parent: parent}
	return checkpoint
}
