package delegate

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/internal/physicaldispatch"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/turn"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
	"github.com/Judgment-Pack/judgment-pack-agent/go/relay"
)

// meter is what the coordinator measures of one child (agent#18) and the mark its one retry is
// held to (agent#17): when it started, when its first answer's first byte arrived, and the contexts
// of its runs.
type meter struct {
	mu        sync.Mutex
	started   time.Time
	firstByte *time.Time
	contexts  []*runcontext.SessionContext
}

func (m *meter) mark() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.firstByte == nil {
		at := time.Now()
		m.firstByte = &at
	}
}

func (m *meter) marked() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.firstByte != nil
}

// markedBody notes the first byte read from an answer's body.
type markedBody struct {
	io.ReadCloser
	m *meter
}

func (b markedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.m.mark()
	}
	return n, err
}

func (c *coordinator) recoveringProvider(ctx context.Context, task *contract.DelegatedTask, kind string) bool {
	if c.run.Context == nil || c.run.Context.Persistence == nil {
		return false
	}
	binding := *c.run.Context.Persistence
	if task != nil {
		binding.Scope = runcontext.DurableScope{Execution: task.ID, Role: "author", Task: task.ID}
	}
	if scope, ok := runcontext.DurableDispatchScope(ctx); ok && scope.Execution == binding.Scope.Execution && scope.Task == binding.Scope.Task {
		binding.Scope.Role = scope.Role
	}
	return runcontext.RecoveringDurableRequest(&binding, kind)
}

// parentModel is the parent's relay capability, its own requests counted (agent#18); its execution
// claims them where it is attached.
func (c *coordinator) parentModel(call relay.Call) relay.Call {
	return func(ctx context.Context, suffix string, request relay.Request) (*http.Response, error) {
		if request.Method != http.MethodGet && !c.taskStepsEnabled() && !c.recoveringProvider(ctx, nil, "model") {
			c.batchMu.Lock()
			c.parentCount++
			c.batchMu.Unlock()
		}
		return call(ctx, suffix, request)
	}
}

// chargeChild is one model request of a child's: within its budget and the response's, the shared
// allowance checkpointed before it leaves.
func (c *coordinator) chargeChild(task *contract.DelegatedTask, budget turn.TaskBudget, atomic ...bool) error {
	c.taskMu.Lock()
	over := task.ModelRequests >= budget.ModelRequests
	c.taskMu.Unlock()
	if over {
		c.childMu.Lock()
		c.refusedBy[task.ID] = "budget"
		c.childMu.Unlock()
		return runcontext.ErrExecutionPaused
	}
	if err := c.shared.childRequest(task); err != nil {
		c.childMu.Lock()
		c.refusedBy[task.ID] = "reserve"
		c.childMu.Unlock()
		return err
	}
	c.taskMu.Lock()
	task.ModelRequests++
	c.taskMu.Unlock()
	if len(atomic) > 0 && atomic[0] {
		return nil
	}
	c.emitMu.Lock()
	held := c.parentCheckpoint
	c.emitMu.Unlock()
	if held != nil {
		state := *held
		state, _, _ = runcontext.MutateCheckpoint(state, runcontext.CheckpointPatch{"Status": "running"})
		state, _, _ = runcontext.MutateCheckpoint(state, runcontext.CheckpointPatch{"Used": int64(c.shared.used())})
		durable := true
		c.deliver(contract.RunEvent{ExecutionCheckpoint: &contract.RunEventExecutionCheckpoint{Type: "execution_checkpoint", Durable: &durable, Checkpoint: c.withFigures(state)}})
	}
	c.publish(task)
	return nil
}

// accountChild persists the ledger after both the child and shared request counts advance.
func (c *coordinator) accountChild(ctx context.Context, task *contract.DelegatedTask, m *meter) error {
	m.mu.Lock()
	var child *runcontext.SessionContext
	if len(m.contexts) > 0 {
		child = m.contexts[len(m.contexts)-1]
	}
	m.mu.Unlock()
	if child == nil {
		return c.durable(ctx)
	}
	c.usageMu.Lock()
	c.shared.mu.Lock()
	if parent := c.shared.parent; parent != nil {
		parent.Execution.MergeChildUsage(child.Execution.State().ID, child.Execution.Usage(), delegatedRole(parent.ResolvedPolicy, task.Profile, parent.Subagents))
	}
	c.shared.mu.Unlock()
	c.usageMu.Unlock()
	return c.durable(ctx)
}

// childRetry is whether a child's refused request is sent once more, and after how long
// (childRetry, agent#17): a 5xx other than 504 of the retryable class or the overload code, which
// the engine's own rule would not retry, within the wait a turn allows.
func childRetry(answered *http.Response) (*contract.Failure, time.Duration, bool) {
	if answered.StatusCode < 500 || answered.StatusCode > 599 || answered.StatusCode == 504 {
		return nil, 0, false
	}
	body, _ := io.ReadAll(io.LimitReader(answered.Body, 1<<16))
	answered.Body = io.NopCloser(bytes.NewReader(body))
	refusal := &statusError{status: answered.StatusCode, body: string(body), header: answered.Header}
	held := failure.Normalize(refusal, "model", time.Now())
	code := failure.RefusalCode(string(body))
	if failure.CanRetryRefusal(held, code) {
		return nil, 0, false
	}
	retryable := false
	for _, class := range failure.RetryableRefusalClasses {
		retryable = retryable || held.Code == class
	}
	for _, overload := range failure.RetryableOverloadCodes {
		retryable = retryable || code == overload
	}
	if !retryable {
		return nil, 0, false
	}
	after := time.Duration(0)
	if held.RetryAt != nil {
		if at, err := time.Parse(time.RFC3339Nano, *held.RetryAt); err == nil {
			after = time.Until(at)
		}
	}
	if after > 30*time.Second {
		return nil, 0, false
	}
	if after < time.Second {
		after = time.Second
	}
	return &held, after, true
}

type statusError struct {
	status int
	body   string
	header http.Header
}

func (e *statusError) Error() string               { return "refused" }
func (e *statusError) StatusCode() int             { return e.status }
func (e *statusError) ResponseBody() string        { return e.body }
func (e *statusError) ResponseHeader() http.Header { return e.header }

// childModel is a child's relay capability: each request charged to its task and the response,
// one retry of a transient refusal before its first byte, and its first byte noted.
func (c *coordinator) childModel(task *contract.DelegatedTask, budget turn.TaskBudget, m *meter, call relay.Call) relay.Call {
	return c.childPhysicalModel(task, budget, m, call).Call
}
func (c *coordinator) childPhysicalModel(task *contract.DelegatedTask, budget turn.TaskBudget, m *meter, call relay.Call) *physicaldispatch.Model {
	invoke := func(ctx context.Context, suffix string, request relay.Request, physical physicaldispatch.Attempt) (*http.Response, error) {
		send := func() (*http.Response, error) {
			if physical != nil {
				return physical(call)
			}
			return call(ctx, suffix, request)
		}
		posted := request.Method != http.MethodGet
		if posted && !c.taskStepsEnabled() && !c.recoveringProvider(ctx, task, "model") {
			if err := c.chargeChild(task, budget); err != nil {
				return nil, err
			}
			if err := c.accountChild(ctx, task, m); err != nil {
				return nil, err
			}
		}
		answered, err := send()
		if err != nil {
			return nil, err
		}
		if !posted {
			return answered, nil
		}
		c.taskMu.Lock()
		retried := task.Retried != nil
		c.taskMu.Unlock()
		if (!c.taskStepsEnabled() || physical != nil) && answered.StatusCode/100 != 2 && !m.marked() && !retried {
			if held, wait, ok := childRetry(answered); ok && c.mayRetry(task, budget, m) {
				c.taskMu.Lock()
				task.Retried = &contract.DelegatedTaskRetried{Failure: *held, WaitMs: ms(wait)}
				c.taskMu.Unlock()
				_ = answered.Body.Close()
				select {
				case <-time.After(wait):
				case <-ctx.Done():
					return nil, failure.Cancelled
				}
				if physical == nil && len(m.contexts) > 0 {
					_ = m.contexts[len(m.contexts)-1].Execution.Claim()
				}
				if !c.taskStepsEnabled() {
					if err := c.chargeChild(task, budget); err != nil {
						return nil, err
					}
					if err := c.accountChild(ctx, task, m); err != nil {
						return nil, err
					}
				}
				answered, err = send()
				if err != nil {
					return nil, err
				}
			}
		}
		if answered.StatusCode/100 == 2 {
			answered.Body = markedBody{ReadCloser: answered.Body, m: m}
		}
		return answered, nil
	}
	var expected *runcontext.RunBinding
	if c.run.Context != nil && c.run.Context.Persistence != nil {
		expected = &runcontext.RunBinding{Writer: c.run.Context.Persistence.Writer, Scope: runcontext.DurableScope{Execution: task.ID, Role: "author", Task: task.ID}}
	}
	return physicaldispatch.New(func(ctx context.Context, suffix string, request relay.Request) (*http.Response, error) {
		return invoke(ctx, suffix, request, nil)
	}, invoke, expected)
}

// mayRetry is whether a child's one retry may take a request now: its budget's, the response's, and
// never its own run's last.
func (c *coordinator) mayRetry(task *contract.DelegatedTask, budget turn.TaskBudget, m *meter) bool {
	left := 2
	if len(m.contexts) > 0 {
		left = m.contexts[len(m.contexts)-1].Execution.Remaining()
	}
	c.taskMu.Lock()
	under := task.ModelRequests < budget.ModelRequests
	c.taskMu.Unlock()
	return under && c.shared.remaining() > synthesisReserve && left > 1
}

// wrapHostTools are a run's host tools with each call counted against the response and a child's
// task, and a child's steps recorded on its task.
func (c *coordinator) wrapHostTools(tools []runcontext.HostTool, task *contract.DelegatedTask, budget *turn.TaskBudget) []runcontext.HostTool {
	out := make([]runcontext.HostTool, 0, len(tools))
	for _, tool := range tools {
		if tool.Interaction == "delegation" {
			out = append(out, tool)
			continue
		}
		held := tool
		inner := tool.Execute
		held.Execute = func(ctx context.Context, args json.RawMessage) (runcontext.ToolResult, error) {
			if err := ctx.Err(); err != nil {
				return runcontext.ToolResult{}, failure.Cancelled
			}
			if err := c.claimTool(task, budget); err != nil {
				return runcontext.ToolResult{}, err
			}
			at := -1
			if task != nil {
				c.taskMu.Lock()
				task.Steps = append(task.Steps, contract.DelegatedTaskStepsItem{Name: held.Name, State: "running"})
				at = len(task.Steps) - 1
				c.taskMu.Unlock()
				c.publish(task)
			}
			result, err := inner(ctx, args)
			if task != nil {
				c.taskMu.Lock()
				switch {
				case err != nil && (failure.IsCancelled(err) || ctx.Err() != nil):
					task.Steps[at].State = "interrupted"
				case err != nil || result.Err():
					task.Steps[at].State = "failed"
				default:
					task.Steps[at].State = "complete"
				}
				c.taskMu.Unlock()
				c.publish(task)
				if err == nil && result.StructuredContent != nil && result.StructuredContent.Get("researchSuggestion").Truth() {
					text := handedBackText
					c.taskMu.Lock()
					task.Problem = &text
					c.taskMu.Unlock()
					c.childMu.Lock()
					c.handedBack[task.ID] = true
					stop := c.childStops[task.ID]
					c.childMu.Unlock()
					if stop != nil {
						stop()
					}
					return runcontext.ToolResult{}, failure.Cancelled
				}
			}
			return result, err
		}
		out = append(out, held)
	}
	return out
}

func (c *coordinator) taskStepsEnabled() bool {
	return c.run.Context != nil && c.run.Context.Persistence != nil && c.run.Context.Persistence.Writer.TaskStepsEnabled()
}
