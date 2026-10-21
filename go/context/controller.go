package runcontext

// The execution controller (the port of packages/harness/execution/controller.ts): the response's
// request allowance, the operations it journaled, the read cache, the plan and budget tools, and
// the checkpoint every adapter's events carry.

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
)

// ExecutionInstructions is the stable part of the execution guidance (EXECUTION_INSTRUCTIONS).
const ExecutionInstructions = "TASK EXECUTION: For work requiring several stages, use update_plan to track concrete pending, in_progress and completed steps. Skip planning overhead for simple replies. Use the current accepted plan when present; do not repeatedly re-plan or ask the user to repeat an already authorized request. Update progress at meaningful boundaries, and preserve completed work across continuation. A task list is working state, not approval or proof: mark a step complete only when its required result exists, and distinguish a proposed change from a saved change. Keep a multi-stage task plan current; it estimates work but cannot grant more requests. An unsaved working object remains in the exact arguments of its completed operations. Continue from those retained arguments after validation or evaluation; rereading the saved workspace does not retrieve an unsaved proposal. Recheck the saved revision before applying changes. Use tool results already obtained; do not reread unchanged instructions or schemas or scan the entire conversation. On continuation, inspect the latest completed result and its exact arguments before doing more work. A newer saved workspace object takes precedence over an older chat draft when editing that object; preserve its identity. For a focused change, inspect the relevant contract once, make the smallest proposal, verify the affected behavior and return it. Do not restart discovery after context compaction. Before repeating a failed read, correct its arguments using the current context index. If blocked, state the exact missing information. When requests run low, finish the concrete result or call request_budget_increase with the reason and next concrete step. Desk owns budget extensions; this tool requests but never grants one. Never modify expected tests merely to match actual results. Current user instructions and host permissions remain authoritative."

// ToolAllowance is a tool-call cap as the request state states it (agent#13): response, a parent's
// and its children's together; task, one child's.
type ToolAllowance struct {
	Used  int
	Limit int
	Scope string
}

// Operation is one tool call as the host journaled it from the tool_call and tool_result events
// (Operation in execution/state.ts).
type Operation struct {
	ID     string
	Name   string
	Args   *Value
	Status string
	Result *ToolResult
}

// Value is the operation as a checkpoint carries it.
func (o *Operation) Value() *Value {
	out := ObjectOf("id", String(o.ID), "name", String(o.Name), "args", orNull(o.Args), "status", String(o.Status))
	if o.Result != nil {
		out.Set("result", o.Result.Value())
	}
	return out
}

// Execution is the execution state the controller holds and the checkpoint carries
// (ExecutionCheckpoint in execution/state.ts). Its values are ordered JSON values, so what the
// checkpoint weighs is what the TypeScript's weighs.
type Execution struct {
	OutputGuard map[string]int64
	Memories    *contract.ProvenanceMemories
	Version     int
	ID          string
	RequestKey  string
	Phase       string
	Status      string
	Unit        string
	Used        int
	Limit       int
	Grants      []contract.CheckpointGrant
	Plan        *Value
	Explanation string
	Operations  []*Operation
	Completions []contract.Provenance
	Context     contract.CheckpointContext
	Reason      *string
	// TestCalls is the turn's experimental_test_cases calls so far (agent#7), carried as Used is;
	// nil where the checkpoint carries none.
	TestCalls *int
	// Delegation is each delegated child's cost and time (agent#18), carried as Used is.
	Delegation *Value
	// Prompts is the digest of every request body the adapter sent (ADR-0005), in order, carried
	// as Used is; nil where none was sent.
	Prompts []contract.ProvenancePromptDigest
	// Sources are the run report's rows by source (ADR-0006), the Runtime's first, carried as Used
	// is; nil where the session declares no tool with a source.
	Sources []contract.ProvenanceSourceFigures
	// Approval is the act call the run paused at for the person's decision, and what became of it
	// (ADR-0008); nil where there is none.
	Approval *contract.CheckpointApproval
	// Skills are the skills loaded (ADR-0009), in load order, carried through a resume; nil where
	// none is loaded.
	Skills []contract.ProvenanceSkillLoad
	Draft  *contract.CheckpointDraft
	// Critic is the critic's decision (ADR-0010), or before it the model's ask; carried through a
	// resume; nil where neither was written.
	Policy        map[string]contract.PolicyMember
	CrossProvider []contract.ProvenanceCrossProvider
	Usage         *contract.Usage
	Compaction    *contract.ProvenanceCompactionReport
	Critic        *contract.CheckpointCritic
}

// Value is the state as the checkpoint's JSON, in the order the TypeScript writes it.
func (e *Execution) Value() *Value {
	grants := Array()
	for _, grant := range e.Grants {
		grants.Append(ObjectOf("from", Number(float64(grant.From)), "to", Number(float64(grant.To)), "at", String(grant.At)))
	}
	operations := Array()
	for _, operation := range e.Operations {
		operations.Append(operation.Value())
	}
	entries := Array()
	for _, entry := range e.Context.Entries {
		held := ObjectOf("id", String(entry.ID), "kind", String(entry.Kind), "label", String(entry.Label))
		value, err := ParseJSON(entry.Value)
		if err != nil {
			value = Null()
		}
		held.Set("value", value)
		if entry.Revision != nil {
			held.Set("revision", String(*entry.Revision))
		}
		held.Set("bytes", Number(float64(entry.Bytes)))
		held.Set("digest", String(entry.Digest))
		entries.Append(held)
	}
	out := ObjectOf(
		"version", Number(float64(e.Version)), "id", String(e.ID), "requestKey", String(e.RequestKey),
		"phase", String(e.Phase), "status", String(e.Status), "unit", String(e.Unit),
		"used", Number(float64(e.Used)), "limit", Number(float64(e.Limit)), "grants", grants,
		"plan", orArray(e.Plan), "explanation", String(e.Explanation), "operations", operations)
	if e.Completions != nil {
		rows := Array()
		for _, row := range e.Completions {
			raw, err := json.Marshal(row)
			if err != nil {
				continue
			}
			held, err := ParseJSON(raw)
			if err != nil {
				continue
			}
			rows.Append(held)
		}
		out.Set("completions", rows)
	}
	out.Set("context", ObjectOf("scope", String(e.Context.Scope), "entries", entries))
	if e.Reason != nil {
		out.Set("reason", String(*e.Reason))
	}
	if e.TestCalls != nil {
		out.Set("testCalls", Number(float64(*e.TestCalls)))
	}
	if e.Delegation != nil {
		out.Set("delegation", e.Delegation)
	}
	if e.Prompts != nil {
		rows := Array()
		for _, row := range e.Prompts {
			rows.Append(ObjectOf("request", Number(float64(row.Request)), "sha256", String(row.Sha256)))
		}
		out.Set("prompts", rows)
	}
	if e.Sources != nil {
		rows := Array()
		for _, row := range e.Sources {
			held := ObjectOf("source", String(row.Source), "kind", String(row.Kind), "calls", Number(float64(row.Calls)),
				"refused", Number(float64(row.Refused)), "proposed", Number(float64(row.Proposed)),
				"bytes", Number(float64(row.Bytes)), "milliseconds", Number(float64(row.Milliseconds)))
			if row.Skills != nil {
				held.Set("skills", Number(float64(*row.Skills)))
			}
			rows.Append(held)
		}
		out.Set("sources", rows)
	}
	if e.Approval != nil {
		if raw, err := json.Marshal(e.Approval); err == nil {
			if held, err := ParseJSON(raw); err == nil {
				out.Set("approval", held)
			}
		}
	}
	if e.OutputGuard != nil {
		raw, _ := json.Marshal(e.OutputGuard)
		v, _ := ParseJSON(raw)
		out.Set("outputGuard", v)
	}
	if e.Memories != nil {
		raw, _ := json.Marshal(e.Memories)
		v, _ := ParseJSON(raw)
		out.Set("memories", v)
	}
	if e.Draft != nil {
		raw, _ := json.Marshal(e.Draft)
		value, _ := ParseJSON(raw)
		out.Set("draft", value)
	}
	if len(e.Skills) > 0 {
		rows := Array()
		for _, load := range e.Skills {
			rows.Append(ObjectOf("id", String(load.ID), "digest", String(load.Digest), "bytes", Number(float64(load.Bytes)), "loadedAt", String(load.LoadedAt)))
		}
		out.Set("skills", rows)
	}
	if e.Policy != nil {
		raw, _ := json.Marshal(e.Policy)
		value, _ := ParseJSON(raw)
		out.Set("policy", value)
	}
	if len(e.CrossProvider) > 0 {
		data, _ := json.Marshal(e.CrossProvider)
		value, _ := ParseJSON(data)
		out.Set("crossProvider", value)
	}
	if e.Usage != nil {
		data, _ := json.Marshal(e.Usage)
		value, _ := ParseJSON(data)
		out.Set("usage", value)
	}
	if e.Compaction != nil {
		data, _ := json.Marshal(e.Compaction)
		value, _ := ParseJSON(data)
		out.Set("compaction", value)
	}
	if e.Critic != nil {
		held := ObjectOf("mode", String(e.Critic.Mode))
		if e.Critic.Ran != nil {
			held.Set("ran", Bool(*e.Critic.Ran))
		}
		for _, member := range []struct {
			name  string
			value *string
		}{{"reason", e.Critic.Reason}, {"reasoning", e.Critic.Reasoning}, {"trigger", e.Critic.Trigger}} {
			if member.value != nil {
				held.Set(member.name, String(*member.value))
			}
		}
		out.Set("critic", held)
	}
	return out
}

func orArray(v *Value) *Value {
	if v == nil {
		return Array()
	}
	return v
}

// Checkpoint is the state as contract.Checkpoint, which checkpoint.json validates.
func (e *Execution) Checkpoint() (contract.Checkpoint, error) {
	var out contract.Checkpoint
	err := json.Unmarshal(e.Value().JSON(), &out)
	return out, err
}

// executionOf reads a validated checkpoint into the state the controller holds.
func executionOf(saved *contract.Checkpoint) (*Execution, error) {
	raw, err := json.Marshal(saved)
	if err != nil {
		return nil, err
	}
	value, err := ParseJSON(raw)
	if err != nil {
		return nil, err
	}
	out := &Execution{Version: int(saved.Version), ID: saved.ID, RequestKey: saved.RequestKey,
		Phase: saved.Phase, Status: saved.Status, Unit: saved.Unit, Used: int(saved.Used),
		Limit: int(saved.Limit), Grants: saved.Grants, Plan: value.Get("plan"),
		Explanation: saved.Explanation, Completions: saved.Completions, Context: saved.Context,
		Reason: saved.Reason, Delegation: value.Get("delegation"), Prompts: saved.Prompts, Sources: saved.Sources, Approval: saved.Approval, Skills: saved.Skills, Draft: saved.Draft, Memories: saved.Memories, Critic: saved.Critic, CrossProvider: saved.CrossProvider, Compaction: saved.Compaction, OutputGuard: saved.OutputGuard, Usage: saved.Usage, Policy: saved.Policy}
	if saved.TestCalls != nil {
		calls := int(*saved.TestCalls)
		out.TestCalls = &calls
	}
	for _, held := range value.Get("operations").Items() {
		operation := &Operation{ID: held.Get("id").Str(), Name: held.Get("name").Str(),
			Args: held.Get("args"), Status: held.Get("status").Str()}
		if result := held.Get("result"); result != nil {
			read := ReadToolResult(result)
			operation.Result = &read
		}
		out.Operations = append(out.Operations, operation)
	}
	return out, nil
}

// ExecutionController is ExecutionController: one response's request allowance, journal, read
// cache and plan tools.
type ExecutionController struct {
	// Progress is the repeated-work tracker.
	Progress *ProgressTracker
	// State is the execution state this response carries.
	State *Execution
	// PauseRequested is set by request_budget_increase: the adapter pauses after the call's result.
	PauseRequested bool
	// ToolAllowance is the tool-call cap this run works under, where a delegation coordinator holds
	// one (agent#13): the response's, shared by a parent and its children, or a child's own budget.
	// Stated in the request state as toolCalls.
	ToolAllowance func() *ToolAllowance
	// LoadedSchemas are the schemas load_tool loaded this run (ADR-0006), as one data block, or "";
	// stated after the request state.
	LoadedSchemas func() string
	// LoadedSkills are the skills loaded (ADR-0009), as one block headed at the project
	// instructions' rank, or ""; stated last, after the loaded schemas.
	LoadedSkills func() string
	LoadedDrafts func() string
	DraftRows    func() *contract.CheckpointDraft
	// SkillLoads are the checkpoint's loaded set (ADR-0009), copied under the skill gate's lock.
	SkillLoads func() []contract.ProvenanceSkillLoad
	MemoryRows func() *contract.ProvenanceMemories
	OutputRows func() map[string]int64
	// SourceRows are the run report's rows by source (ADR-0006), copied under the lock a child's
	// gate shares with this run's: a snapshot reads them through it, since a delegated child's
	// calls count in them while this run's checkpoint is written.
	SourceRows func() []contract.ProvenanceSourceFigures
	// ApprovalStep is what the restored approval asks of this run before its first request
	// (ADR-0008): "execute", the person approved the call and it runs once; "continue", its result
	// (or the refused result) is written and the continuation carries it; "write", as continue, the
	// state written first; "" for none.
	ApprovalStep string

	session  Session
	ledger   *ContextLedger
	restored bool
	attempt  int
	// criticMu is the switch's lock: it also guards operation writers and snapshot serialisation.
	criticMu *sync.Mutex

	operationsReference *Value
	restoredResults     []*Value
	inFlight            map[string]string
	cache               map[string]ToolResult
	usageConnection     string
	usageRole           string
	usageStarted        time.Time
	usageBound          bool
	usageSources        map[string]contract.ProvenanceSourceFigures
}

// NewExecutionController is a controller for one run.
func NewExecutionController(session Session, ledger *ContextLedger) *ExecutionController {
	return &ExecutionController{Progress: NewProgressTracker(), session: session, ledger: ledger,
		attempt: 1, inFlight: map[string]string{}, cache: map[string]ToolResult{}, usageRole: "author", usageStarted: time.Now(), usageSources: map[string]contract.ProvenanceSourceFigures{}}
}

// Initialize is initialize: the state a new response starts from, or a saved one resumed, held to
// the same request, phase and connection type.
func (c *ExecutionController) Initialize() error {
	var request *Value
	if c.session.Context != nil && c.session.Context.CurrentRequest != nil {
		request = c.session.Context.CurrentRequest.Value()
	} else {
		request = String(c.session.Prompt)
	}
	phase := "work"
	if c.session.Context != nil && c.session.Context.Phase != "" {
		phase = c.session.Context.Phase
	}
	purpose := Null()
	if c.session.Purpose != "" {
		purpose = String(c.session.Purpose)
	}
	requestKey := ContextDigest(ObjectOf("request", request, "phase", String(phase), "purpose", purpose).Text())
	var saved *contract.Checkpoint
	if c.session.Context != nil && c.session.Context.Execution != nil {
		raw, err := json.Marshal(c.session.Context.Execution)
		if err != nil {
			return errSavedExecution
		}
		value, err := ParseJSON(raw)
		if err != nil {
			return errSavedExecution
		}
		saved, err = ReadExecution(value)
		if err != nil {
			return err
		}
	}
	unit := "provider-managed"
	limit := 1
	if c.session.Model != nil {
		unit = "model-requests"
		limit = MaxTurns
	}
	if saved != nil {
		if saved.RequestKey != requestKey || saved.Phase != phase {
			return failure.ToolInput("The request or execution phase changed. Start a new response with the current context.")
		}
		// ADR-0008: a paused act call continues only with the person's decision for that call.
		var decision *contract.CheckpointApprovalDecision
		if c.session.Context != nil {
			decision = c.session.Context.Approval
		}
		if decision != nil && (decision.CallID == "" || U16Length(decision.CallID) > 256 || decision.Note != nil && U16Length(*decision.Note) > 2000) {
			return failure.Authored("configuration", "The approval decision is not one this package reads: a callId, approved, and an optional note of at most 2,000 characters. Nothing was decided or run.", "context")
		}
		asked := saved.Approval != nil && saved.Approval.State == "asked"
		if decision != nil && (saved.Approval == nil || decision.CallID != saved.Approval.CallID) {
			return failure.Authored("stale-state", "This decision is for a call this response is not waiting on; nothing was decided or run. Continue it with the decision on the call it asked about.", "context")
		}
		// A decision on an act call already decided (refused, executed, answered) is no decision this
		// response waits for.
		if decision != nil && !asked {
			return failure.Authored("stale-state", "This response’s act call was already decided; nothing was decided or run. Continue it without a decision.", "context")
		}
		if asked && decision == nil {
			return failure.Authored("stale-state", "This response waits for the person’s decision on "+saved.Approval.Name+"; continue it with that decision.", "context")
		}
		if (saved.Status == "paused" && !asked) || saved.Used >= saved.Limit {
			return ErrExecutionPaused
		}
		if saved.Unit != unit {
			return failure.ToolInput("Continue this response with the same connection type, or start a new response.")
		}
		if err := c.ledger.Restore(saved.Context); err != nil {
			return err
		}
		state, err := executionOf(saved)
		if err != nil {
			return errSavedExecution
		}
		c.Progress.Seed(state.Operations)
		for _, operation := range state.Operations {
			if operation.Status == "running" {
				operation.Status = "interrupted"
			}
		}
		state.Status = "running"
		c.State = state
		for _, source := range state.Sources {
			c.usageSources[source.Source] = source
		}
		c.restored = true
		if approval := state.Approval; approval != nil {
			held := *approval
			switch approval.State {
			case "asked":
				decidedAt := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
				held.DecidedAt = &decidedAt
				if decision.Note != nil {
					note := *decision.Note
					held.Note = &note
				}
				if decision.Approved {
					held.State = "approved"
					c.ApprovalStep = "execute"
				} else {
					held.State = "refused"
					held.Result = contractResult(RefusedResult(held.Name))
					c.ApprovalStep = "write"
				}
			case "approved":
				// Approved, and its result never written: it may have run, and it is not run again.
				held.State = "executed"
				held.Result = contractResult(UnknownOutcome(held.Name))
				c.ApprovalStep = "write"
			case "executed", "refused":
				c.ApprovalStep = "continue"
			}
			state.Approval = &held
		}
		return nil
	}
	c.State = &Execution{Version: 1, ID: randomUUID(), RequestKey: requestKey, Phase: phase,
		Status: "running", Unit: unit, Used: 0, Limit: limit, Grants: []contract.CheckpointGrant{},
		Plan: Array(), Explanation: "", Operations: []*Operation{},
		Context: contract.CheckpointContext{Scope: c.ledger.Scope(), Entries: []contract.CheckpointRetainedEntry{}}}
	if c.session.Context != nil && c.session.Context.Plan != nil {
		plan := c.session.Context.Plan
		status := plan.Get("status")
		if status.IsString() && (status.Str() == "accepted" || status.Str() == "started") {
			steps := Array()
			for _, step := range plan.Get("steps").Items() {
				steps.Append(ObjectOf("step", step, "status", String("pending")))
			}
			c.State.Plan = steps
		}
	}
	return nil
}

// ApprovalAsked reports an act call of this run waiting for the person's decision (ADR-0008): no
// request is sent while one does.
func (c *ExecutionController) ApprovalAsked() bool {
	return c.State.Approval != nil && c.State.Approval.State == "asked"
}

// contractResult is a tool result as the checkpoint's approval carries it.
func contractResult(result ToolResult) *contract.ToolResultMCPToolResult {
	var out contract.ToolResultMCPToolResult
	if json.Unmarshal(result.JSON(), &out) != nil {
		return nil
	}
	return &out
}

// Remaining is how many model requests the response may still make.
func (c *ExecutionController) Remaining() int { return c.State.Limit - c.State.Used }

// UseConnection selects the ledger row for later requests. A failover calls it before resending.
func (c *ExecutionController) UseConnection(connection, role string) {
	if c.usageBound && (connection != c.usageConnection || role != c.usageRole) {
		c.recordUsageElapsed()
		c.recordSourceUsage(c.State.Sources)
	}
	if !c.usageBound {
		c.usageStarted = time.Now()
	}
	c.usageConnection, c.usageRole = connection, role
	c.usageBound = true
	if c.State.Usage == nil {
		c.State.Usage = &contract.Usage{}
	}
	source := "model"
	if c.State.Unit == "provider-managed" {
		source = "agent"
	}
	c.usageRow(source, connection, role)
}

func (c *ExecutionController) recordUsageElapsed() {
	source := "model"
	if c.State.Unit == "provider-managed" {
		source = "agent"
	}
	now := time.Now()
	c.usageRow(source, c.usageConnection, c.usageRole).Milliseconds += now.Sub(c.usageStarted).Milliseconds()
	c.usageStarted = now
}

func (c *ExecutionController) recordSourceUsage(sources []contract.ProvenanceSourceFigures) {
	if !c.usageBound {
		return
	}
	for _, source := range sources {
		before := c.usageSources[source.Source]
		calls, milliseconds, bytes := source.Calls-before.Calls, source.Milliseconds-before.Milliseconds, source.Bytes-before.Bytes
		if calls < 0 {
			calls = 0
		}
		if milliseconds < 0 {
			milliseconds = 0
		}
		if bytes < 0 {
			bytes = 0
		}
		if calls != 0 || milliseconds != 0 || bytes != 0 {
			row := c.usageRow(source.Source, c.usageConnection, c.usageRole)
			row.Invocations += calls
			row.Milliseconds += milliseconds
			row.Bytes += bytes
		}
		c.usageSources[source.Source] = source
	}
}

func (c *ExecutionController) usageRow(source, connection, role string) *contract.UsageRow {
	if c.State.Usage == nil {
		c.State.Usage = &contract.Usage{}
	}
	for i := range c.State.Usage.Rows {
		row := &c.State.Usage.Rows[i]
		if row.Source == source && row.Connection == connection && row.Role == role {
			return row
		}
	}
	c.State.Usage.Rows = append(c.State.Usage.Rows, contract.UsageRow{Source: source, Connection: connection, Role: role})
	return &c.State.Usage.Rows[len(c.State.Usage.Rows)-1]
}

// Usage returns the closed run ledger, adding observed source rows and this run's elapsed time.
func (c *ExecutionController) Usage() contract.Usage {
	if c.usageBound {
		c.recordUsageElapsed()
	}
	c.recordSourceUsage(c.State.Sources)
	data, _ := json.Marshal(c.State.Usage)
	var out contract.Usage
	_ = json.Unmarshal(data, &out)
	if out.Rows == nil {
		out.Rows = []contract.UsageRow{}
	}
	return out
}

// Claim takes one model request, or answers ExecutionPaused at the ceiling.
func (c *ExecutionController) Claim() error {
	if c.Remaining() <= 0 {
		return ErrExecutionPaused
	}
	c.State.Used++
	c.usageRow("model", c.usageConnection, c.usageRole).Requests++
	return nil
}

// LimitTo is limitTo: a first allowance below MaxTurns, for a delegated child (agent#15) — its
// profile's request budget, held further to what the response's shared allowance can still give
// it, so its loop plans its final answer within what it may spend. A restored execution keeps its
// own.
func (c *ExecutionController) LimitTo(requests int) {
	if c.restored {
		return
	}
	limit := c.State.Limit
	if requests < limit {
		limit = requests
	}
	if limit < 1 {
		limit = 1
	}
	c.State.Limit = limit
}

// Guidance is the instructions and the state together, as an agent's prompt carries them.
func (c *ExecutionController) Guidance() string {
	return ExecutionInstructions + "\n" + c.RequestState()
}

// RequestState is the part of the guidance that changes between requests: the repetition note,
// the request counters, the plan, the operations index. A model loop sends it after the history,
// at the end of every request, and ExecutionInstructions once in its stable system text.
func (c *ExecutionController) RequestState() string {
	var tools *ToolAllowance
	if c.ToolAllowance != nil {
		tools = c.ToolAllowance()
	}
	state := Object()
	if c.State.Unit == "model-requests" {
		state.Set("requests", ObjectOf("unit", String(c.State.Unit), "used", Number(float64(c.State.Used)),
			"limit", Number(float64(c.State.Limit)), "remaining", Number(float64(c.Remaining()))))
	} else {
		state.Set("requests", ObjectOf("unit", String(c.State.Unit),
			"note", String("The subscription provider manages its internal model loop; tool calls are not model-request counts.")))
	}
	if tools != nil {
		remaining := tools.Limit - tools.Used
		if remaining < 0 {
			remaining = 0
		}
		note := "Tool calls this task may make; the context tools are not counted."
		if tools.Scope == "response" {
			note = "Tool calls this response may make, yours and your delegated children’s together; the context tools are not counted."
		}
		state.Set("toolCalls", ObjectOf("unit", String("tool-calls"), "used", Number(float64(tools.Used)),
			"limit", Number(float64(tools.Limit)), "remaining", Number(float64(remaining)),
			"scope", String(tools.Scope), "note", String(note)))
	}
	state.Set("tasks", orArray(c.State.Plan))
	state.Set("explanation", String(c.State.Explanation))
	if c.operationsReference != nil {
		state.Set("retainedOperations", c.operationsReference)
	}
	if len(c.restoredResults) > 0 {
		rows := Array()
		for _, row := range c.restoredResults {
			rows.Append(row)
		}
		state.Set("resumeFromCompletedResults", rows)
	}
	state.Set("completedOperationCount", Number(float64(len(c.State.Operations))))
	loaded := ""
	if c.LoadedSchemas != nil {
		if section := c.LoadedSchemas(); section != "" {
			loaded = "\n" + section
		}
	}
	if c.LoadedSkills != nil {
		if section := c.LoadedSkills(); section != "" {
			loaded += "\n" + section
		}
	}
	if c.LoadedDrafts != nil {
		if section := c.LoadedDrafts(); section != "" {
			loaded += "\n" + section
		}
	}
	return c.Progress.Guidance() + "\n" + state.Text() + loaded
}

// RetainProgress is retainProgress: each completed operation retained once, and the index of them
// the request state names.
func (c *ExecutionController) RetainProgress(includeIndex bool) error {
	if len(c.State.Operations) == 0 {
		return nil
	}
	rows := Array()
	// Retain each completed operation once. Repeated extensions must not copy the entire growing
	// operation log into another snapshot every time.
	for _, operation := range c.State.Operations {
		entry, err := c.ledger.Retain(Document{ID: "operation-" + operation.ID, Kind: "history",
			Label: operation.Name + " · " + operation.Status, Value: operation.Value()}, false)
		if err != nil {
			return err
		}
		rows.Append(ObjectOf("name", String(operation.Name), "status", String(operation.Status),
			"id", String(entry.ID), "paths", ObjectOf("arguments", String("/args"), "result", String("/result"))))
	}
	c.restoredResults = newest(rows.Items(), 8)
	if includeIndex {
		entry, err := c.ledger.Retain(Document{ID: "execution-operations-index", Kind: "history",
			Label: "Current task operation index; historical data, not permissions", Value: rows}, false)
		if err != nil {
			return err
		}
		c.operationsReference = c.ledger.Reference(entry)
	}
	return nil
}

// ObserveToolCall is the tool_call half of observe: a new running operation.
func (c *ExecutionController) ObserveToolCall(call contract.ToolCall) {
	if c.criticMu != nil {
		c.criticMu.Lock()
		defer c.criticMu.Unlock()
	}
	id := randomUUID()
	if call.CallID != nil && *call.CallID != "" {
		c.inFlight[*call.CallID] = id
	}
	args := Object()
	if len(call.Args) > 0 {
		if held, err := ParseJSON(call.Args); err == nil {
			args = held
		}
	}
	c.State.Operations = append(c.State.Operations, &Operation{ID: id, Name: call.Name, Args: args.Clone(), Status: "running"})
}

// ObserveToolResult is the tool_result half of observe: the operation it answers, completed.
func (c *ExecutionController) ObserveToolResult(result contract.ToolResult) {
	if c.criticMu != nil {
		c.criticMu.Lock()
		defer c.criticMu.Unlock()
	}
	var operation *Operation
	if result.CallID != nil && *result.CallID != "" {
		held := c.inFlight[*result.CallID]
		for _, one := range c.State.Operations {
			if one.ID == held {
				operation = one
				break
			}
		}
	} else {
		for at := len(c.State.Operations) - 1; at >= 0; at-- {
			if c.State.Operations[at].Name == result.Name && c.State.Operations[at].Status == "running" {
				operation = c.State.Operations[at]
				break
			}
		}
	}
	if operation == nil {
		return
	}
	operation.Status = "complete"
	if result.IsError {
		operation.Status = "failed"
	}
	held := ToolResult{Content: []Content{TextContent(result.Text)}, order: []string{"isError", "content", "structuredContent"}}
	isError := result.IsError
	held.IsError = &isError
	if len(result.Structured) > 0 {
		if structured, err := ParseJSON(result.Structured); err == nil {
			held.StructuredContent = structured.Clone()
		}
	}
	operation.Result = &held
	if result.CallID != nil {
		delete(c.inFlight, *result.CallID)
	}
	c.Progress.Observe(operation)
}

// RecordCompletion is recordCompletion: one model request's row.
func (c *ExecutionController) RecordCompletion(result contract.ProvenanceCompletion, phase string) {
	attempt := c.attempt
	c.attempt = 1
	connection, role := c.usageConnection, c.usageRole
	row := contract.Provenance{Request: int64(c.State.Used), Phase: phase, Result: result, Connection: &connection, Role: &role}
	if attempt > 1 {
		held := int64(attempt)
		row.Attempt = &held
	}
	c.State.Completions = append(c.State.Completions, row)
	usage := c.usageRow("model", connection, role)
	tokens := usage.Tokens
	if tokens == nil {
		tokens = &contract.UsageTokens{}
	}
	present := false
	for _, pair := range []struct {
		from *int64
		to   **int64
	}{{result.InputTokens, &tokens.InputTokens}, {result.OutputTokens, &tokens.OutputTokens}, {result.ReasoningTokens, &tokens.ReasoningTokens}, {result.CacheReadTokens, &tokens.CacheReadTokens}, {result.CacheWriteTokens, &tokens.CacheWriteTokens}} {
		if pair.from != nil {
			value := *pair.from
			if *pair.to != nil {
				value += **pair.to
			}
			*pair.to = &value
			present = true
		}
	}
	if present {
		usage.Tokens = tokens
	}
}

// RecordPrompt is recordPrompt (ADR-0005): the digest of one request body the adapter is about to
// send, recorded before it is sent: the SHA-256 of the body, numbered after the last one recorded.
// The newest MaxPromptDigests are kept.
func (c *ExecutionController) RecordPrompt(body []byte) {
	// ADR-0008: the continuation request carries the approval's result; once it is sent, the approval is answered.
	if held := c.State.Approval; held != nil && (held.State == "executed" || held.State == "refused") {
		answered := *held
		answered.State = "answered"
		c.State.Approval = &answered
	}
	next := int64(1)
	if held := len(c.State.Prompts); held > 0 {
		next = c.State.Prompts[held-1].Request + 1
	}
	c.State.Prompts = append(c.State.Prompts, contract.ProvenancePromptDigest{Request: next, Sha256: ContextDigest(string(body))})
	source := "model"
	if c.State.Unit == "provider-managed" {
		source = "agent"
	}
	usage := c.usageRow(source, c.usageConnection, c.usageRole)
	usage.Bytes += int64(len(body))
	if source == "agent" {
		usage.Invocations++
	}
	if extra := len(c.State.Prompts) - MaxPromptDigests; extra > 0 {
		c.State.Prompts = append([]contract.ProvenancePromptDigest{}, c.State.Prompts[extra:]...)
	}
}

// BeginAttempt says which attempt at a model request is being sent, before each one, so a new
// call starts again at 1.
func (c *ExecutionController) BeginAttempt(attempt int) { c.attempt = attempt }

// RecordRefusal is recordRefusal: a refused attempt at a model request, its own row.
func (c *ExecutionController) RecordRefusal(phase string, attempt int, f contract.Failure, waitMs *int) {
	connection, role := c.usageConnection, c.usageRole
	row := contract.Provenance{Request: int64(c.State.Used), Phase: phase, Connection: &connection, Role: &role,
		Result: contract.ProvenanceCompletion{FinishReason: "error"}}
	held := int64(attempt)
	row.Attempt = &held
	failed := f
	row.Failure = &failed
	if waitMs != nil {
		wait := int64(*waitMs)
		row.WaitMs = &wait
	}
	c.State.Completions = append(c.State.Completions, row)
}

// ClearReadCache empties the run's own read cache (the critic's isolation).
func (c *ExecutionController) ClearReadCache() { c.cache = map[string]ToolResult{} }

// KeptRead says a session tool that reads whose answers a chat-scoped context keeps: a runtime
// tool (Runtime true), or a host tool declared a read. Offered is false for a name the run's
// session does not offer.
type KeptRead struct {
	Runtime bool
	Offered *bool
}

// WrapRead is wrapRead: only immutable session reads are cached. Workflow writes and evaluations
// are never cached.
//
// kept (agent#8): where the run has a chat-scoped context, each answer is retained in the chat's
// ledger for later runs' read_window, search and index, and an immutable read is served from the
// chat's read cache, under ReadCacheKeyOf, without a call. A read whose result depends on the
// Runtime (every runtime tool, and a host tool that declares ContextPolicy.Runtime) is keyed by
// the Runtime the host names in Input.Runtime; without that it is cached for the run only, as
// before. The cache serves only a name the run's session offers (kept.Offered, said false for a
// name it does not): the call then reaches the offer check and the gate as any other, and no
// cached entry is added to the run's view.
func (c *ExecutionController) WrapRead(name string, execute func(ctx context.Context, args json.RawMessage) (ToolResult, error),
	policy *ToolContextPolicy, readOnly bool, kept *KeptRead) func(ctx context.Context, args json.RawMessage) (ToolResult, error) {
	// A mistaken hint on a write can never suppress or replay that operation.
	immutable := policy != nil && policy.Immutable && readOnly
	return func(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
		args := Object()
		if len(raw) > 0 {
			if held, err := ParseJSON(raw); err == nil && held.IsObject() {
				args = held
			}
		}
		if (name == ReadTool || name == "read_context") && strings.HasPrefix(editString(args.Get("id")), "draft:") {
			return execute(ctx, raw)
		}
		var chat *ChatTurn
		if kept != nil {
			chat = c.ledger.Chat()
		}
		chatKey, hasChatKey := "", false
		if chat != nil && immutable {
			runtimeBound := kept.Runtime || (policy != nil && policy.Runtime)
			var runtime *RuntimeIdentity
			if c.session.Context != nil {
				runtime = c.session.Context.Runtime
			}
			chatKey, hasChatKey = ReadCacheKeyOf(name, args, runtimeBound, runtime)
		}
		// The cache serves only a name the run's session offers: an unoffered name goes on to the
		// inner offer check and the gate, and a served entry never joins a child's visible set.
		if hasChatKey && !(kept.Offered != nil && !*kept.Offered) {
			if held, ok := chat.Cached(chatKey); ok {
				return held, nil
			}
		}
		key := name + ":" + args.Text()
		if !hasChatKey && immutable {
			if held, ok := c.cache[key]; ok {
				return held.Clone(), nil
			}
		}
		result, err := execute(ctx, raw)
		if err != nil {
			return result, err
		}
		if !result.Err() && chat != nil {
			if err := chat.Keep(c.ledger, name, args, result, chatKey, hasChatKey); err != nil {
				return result, err
			}
		}
		if !hasChatKey && immutable && !result.Err() {
			c.cache[key] = result.Clone()
		}
		return result, nil
	}
}

// Tool is update_plan: the host tool that tracks this task's execution progress.
func (c *ExecutionController) Tool() HostTool {
	return HostTool{
		Name:        "update_plan",
		Effect:      "plan",
		Description: "Track this task’s execution progress. Use for multi-stage work, not simple answers. This does not approve a plan, increase limits, save artifacts or prove success. Keep completed steps and explain material changes. At most one step is in progress.",
		InputSchema: json.RawMessage(ObjectOf("type", String("object"),
			"properties", ObjectOf(
				"explanation", ObjectOf("type", String("string"), "maxLength", Number(2000)),
				"plan", ObjectOf("type", String("array"), "minItems", Number(1), "maxItems", Number(20),
					"items", ObjectOf("type", String("object"),
						"properties", ObjectOf(
							"step", ObjectOf("type", String("string"), "maxLength", Number(1000)),
							"status", ObjectOf("type", String("string"), "enum", Array(String("pending"), String("in_progress"), String("completed"))),
							"detail", ObjectOf("type", String("string"), "maxLength", Number(1500))),
						"required", Array(String("step"), String("status")),
						"additionalProperties", Bool(false)))),
			"required", Array(String("explanation"), String("plan")),
			"additionalProperties", Bool(false)).JSON()),
		Execute: func(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
			if err := ctx.Err(); err != nil {
				return ToolResult{}, err
			}
			args, err := ParseJSON(raw)
			if err != nil {
				return ToolResult{}, failure.ToolInput(planRefused)
			}
			explanation := args.Get("explanation")
			if !ValidTaskSteps(args.Get("plan")) || !explanation.IsString() || U16Length(explanation.Str()) > 2000 {
				return ToolResult{}, failure.ToolInput(planRefused)
			}
			c.State.Plan = args.Get("plan").Clone()
			c.State.Explanation = explanation.Str()
			out := ObjectOf("status", String("progress-recorded"), "plan", c.State.Plan)
			if c.State.Unit == "model-requests" {
				out.Set("remaining", Number(float64(c.Remaining())))
			} else {
				out.Set("budget", String("provider-managed"))
			}
			out.Set("permissionsUnchanged", Bool(true))
			return Answer(out), nil
		},
	}
}

const planRefused = "Provide one to twenty task steps, at most one in progress, and a short explanation."

// BudgetTool is request_budget_increase: the host tool that pauses near the request limit and
// asks the person for more compute.
func (c *ExecutionController) BudgetTool() HostTool {
	return HostTool{
		Name:        "request_budget_increase",
		Effect:      "plan",
		Description: "Pause near the request limit and ask the person for more compute. Explain useful remaining work and the next concrete step. This never grants more requests or action permissions. Use ask_user for missing policy decisions instead.",
		InputSchema: json.RawMessage(ObjectOf("type", String("object"),
			"properties", ObjectOf(
				"reason", ObjectOf("type", String("string"), "maxLength", Number(1000)),
				"nextStep", ObjectOf("type", String("string"), "maxLength", Number(900))),
			"required", Array(String("reason"), String("nextStep")),
			"additionalProperties", Bool(false)).JSON()),
		Execute: func(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
			if err := ctx.Err(); err != nil {
				return ToolResult{}, err
			}
			args, err := ParseJSON(raw)
			if err != nil {
				return ToolResult{}, failure.ToolInput(budgetRefused)
			}
			reason, next := args.Get("reason"), args.Get("nextStep")
			if !reason.IsString() || strings.TrimSpace(reason.Str()) == "" || U16Length(reason.Str()) > 1000 ||
				!next.IsString() || strings.TrimSpace(next.Str()) == "" || U16Length(next.Str()) > 900 {
				return ToolResult{}, failure.ToolInput(budgetRefused)
			}
			if c.State.Unit != "model-requests" {
				return Answer(ObjectOf("status", String("unavailable"),
					"message", String("The subscription agent owns its internal request budget. Return findings or ask for missing information."))), nil
			}
			if c.Remaining() > 2 {
				return Answer(ObjectOf("status", String("within-allowance"), "remaining", Number(float64(c.Remaining())),
					"message", String("Continue useful work within the current allowance. Do not ask for an increase yet."))), nil
			}
			held := reason.Str() + " Next: " + next.Str()
			c.State.Reason = &held
			c.PauseRequested = true
			return Answer(ObjectOf("status", String("approval-required"),
				"message", String("Desk will pause and present the request. Do not perform more work until the person continues."))), nil
		},
	}
}

const budgetRefused = "Explain why more requests are needed and the next concrete step."

// Snapshot is snapshot: the state and the ledger's entries as one checkpoint, under the status
// given (the state's own where it is empty).
func (c *ExecutionController) Snapshot(status string) (contract.Checkpoint, error) {
	if c.criticMu != nil {
		// Lock order: adapter delivery lock, criticMu, then source/skill/ledger locks.
		// Keep the state protected through copying and serialisation.
		c.criticMu.Lock()
		defer c.criticMu.Unlock()
	}
	if status == "" {
		status = c.State.Status
	}
	sourceRows := c.State.Sources
	if c.SourceRows != nil {
		sourceRows = c.SourceRows()
		c.recordSourceUsage(sourceRows)
	}
	usage := c.Usage()
	c.State.Usage = &usage
	held := *c.State
	held.Operations = make([]*Operation, len(c.State.Operations))
	for i, operation := range c.State.Operations {
		copied := *operation
		copied.Args = operation.Args.Clone()
		if operation.Result != nil {
			result := operation.Result.Clone()
			copied.Result = &result
		}
		held.Operations[i] = &copied
	}
	held.Status = status
	held.Sources = sourceRows
	if c.DraftRows != nil {
		held.Draft = c.DraftRows()
	}
	if c.OutputRows != nil {
		held.OutputGuard = c.OutputRows()
	}
	if c.MemoryRows != nil {
		held.Memories = c.MemoryRows()
	}
	if c.SkillLoads != nil {
		held.Skills = c.SkillLoads()
	}
	held.Context = c.ledger.Snapshot()
	value := held.Value()
	if UTF8Bytes(value.Text()) > MaxCheckpointBytes {
		return contract.Checkpoint{}, failure.ToolInput("The execution checkpoint is full. Completed work remains visible; start a focused follow-up before further work.")
	}
	var out contract.Checkpoint
	if err := json.Unmarshal(value.JSON(), &out); err != nil {
		return contract.Checkpoint{}, err
	}
	return out, nil
}

// TestCalls is the turn's test calls so far, zero where the checkpoint carries none.
func (c *ExecutionController) TestCalls() int {
	if c.State.TestCalls == nil {
		return 0
	}
	return *c.State.TestCalls
}

// SetTestCalls records the turn's test calls, as the gate counts them.
func (c *ExecutionController) SetTestCalls(count int) { c.State.TestCalls = &count }
