package runcontext

// The execution checkpoint (the port of packages/harness/execution/state.ts): a local execution
// record, never permissions, evidence approval or private reasoning. The checkpoint itself is
// contract.Checkpoint (checkpoint.json); this file is what reads one closed, what extends its
// request allowance, and the pause its ceiling earns.

import (
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
)

// taskID is a delegated task's id, and digest64 a SHA-256 as hex.
var (
	taskID   = regexp.MustCompile(`^[a-f0-9-]{36}$`)
	digest64 = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

// MaxTurns is the model requests one response may make before it pauses (MAX_TURNS in
// engines/contract.ts, which the turn package ports; state_test.go holds it equal to the
// contract's desk-limits vector).
const MaxTurns = 20

// RequestIncrement is how many requests one grant adds.
const RequestIncrement = 10

// MaxRequestAllowance is the most requests any grant may reach.
const MaxRequestAllowance = 100

// MaxCheckpointBytes is the largest a serialized checkpoint may be.
const MaxCheckpointBytes = 4 * 1024 * 1024

// MaxPromptDigests is MAX_PROMPT_DIGESTS: the most prompt digests a checkpoint keeps, the newest,
// three attempts at each request of the largest allowance (ADR-0005).
const MaxPromptDigests = 300

// ExecutionPaused is the work paused at the approved request limit (ExecutionPaused), as an error
// of class request-limit.
type ExecutionPaused struct{}

// Error is the sentence the person reads.
func (e *ExecutionPaused) Error() string {
	return "Work paused at the approved request limit. Continue with an additional allowance, or keep the completed work."
}

// FailureCode is the class the error names (failure.Coded).
func (e *ExecutionPaused) FailureCode() string { return "request-limit" }

// ErrExecutionPaused is the one ExecutionPaused value; errors.Is finds it.
var ErrExecutionPaused = &ExecutionPaused{}

var errSavedExecution = &authoredError{"Saved execution progress is invalid. History was left untouched."}

// errNoAllowance is what an execution that cannot receive another request allowance is told.
var errNoAllowance = &authoredError{"This execution cannot receive another request allowance."}

func isObject(v *Value) bool { return v.IsObject() }

func isText(v *Value, max int) bool { return v.IsString() && U16Length(v.Str()) <= max }

// isCount reports a safe integer from 0 to max.
func isCount(v *Value, max float64) bool {
	return isInteger(v) && v.Num() >= 0 && v.Num() <= max && v.Num() <= 9007199254740991
}

// hasOnly reports an object whose every member the names list names.
func hasOnly(v *Value, names ...string) bool {
	for _, key := range v.Keys() {
		known := false
		for _, name := range names {
			if key == name {
				known = true
			}
		}
		if !known {
			return false
		}
	}
	return true
}

func oneOf(v *Value, names ...string) bool {
	if !v.IsString() {
		return false
	}
	for _, name := range names {
		if v.Str() == name {
			return true
		}
	}
	return false
}

var tokenKeys = []string{"inputTokens", "outputTokens", "reasoningTokens", "cacheReadTokens", "cacheWriteTokens"}

func validTokens(v *Value) bool {
	if !isObject(v) || !hasOnly(v, tokenKeys...) {
		return false
	}
	for _, key := range tokenKeys {
		if held := v.Get(key); held != nil && !isCount(held, 9007199254740991) {
			return false
		}
	}
	return true
}

// ValidDelegationFigures is validDelegationFigures: the checkpoint's delegation figures, read
// closed (agent#18).
func ValidDelegationFigures(v *Value) bool {
	const day = 86400000
	tokens := func(x *Value) bool { return x.Get("tokens") == nil || validTokens(x.Get("tokens")) }
	child := func(c *Value) bool {
		if !isObject(c) || !hasOnly(c, "task", "batch", "profile", "model", "state", "modelRequests", "toolCalls",
			"agentRuns", "tokens", "firstByteMs", "durationMs", "partial", "reused") {
			return false
		}
		task := c.Get("task")
		if !isText(task, 36) || !taskID.MatchString(task.Str()) {
			return false
		}
		if !isCount(c.Get("batch"), 100) || c.Get("batch").Num() < 1 || !isText(c.Get("profile"), 48) {
			return false
		}
		if model := c.Get("model"); model != nil && !isText(model, 200) {
			return false
		}
		if !oneOf(c.Get("state"), "queued", "running", "complete", "failed", "interrupted") {
			return false
		}
		if !isCount(c.Get("modelRequests"), 100) || !isCount(c.Get("toolCalls"), 100) {
			return false
		}
		if runs := c.Get("agentRuns"); runs != nil && !isCount(runs, 100) {
			return false
		}
		if !tokens(c) {
			return false
		}
		if first := c.Get("firstByteMs"); first != nil && !isCount(first, day) {
			return false
		}
		if !isCount(c.Get("durationMs"), day) {
			return false
		}
		if partial := c.Get("partial"); partial != nil && !partial.Truth() {
			return false
		}
		if reused := c.Get("reused"); reused != nil && !reused.Truth() {
			return false
		}
		return true
	}
	total := func(t *Value) bool {
		if !isObject(t) || !hasOnly(t, "modelRequests", "toolCalls", "agentRuns", "tokens", "durationMs") {
			return false
		}
		if !isCount(t.Get("modelRequests"), 9007199254740991) || !isCount(t.Get("toolCalls"), 9007199254740991) {
			return false
		}
		if runs := t.Get("agentRuns"); runs != nil && !isCount(runs, 9007199254740991) {
			return false
		}
		return tokens(t) && isCount(t.Get("durationMs"), day)
	}
	batch := func(b *Value) bool {
		if !isObject(b) || !hasOnly(b, "batch", "children", "total") {
			return false
		}
		if !isCount(b.Get("batch"), 100) || b.Get("batch").Num() < 1 {
			return false
		}
		children := b.Get("children")
		if !children.IsArray() || children.Len() < 1 || children.Len() > 3 {
			return false
		}
		for _, one := range children.Items() {
			if !child(one) {
				return false
			}
		}
		return total(b.Get("total"))
	}
	parent := func(p *Value) bool {
		if !isObject(p) || !hasOnly(p, "model", "modelRequests", "agentRuns", "tokens") {
			return false
		}
		if !isText(p.Get("model"), 200) || !isCount(p.Get("modelRequests"), 9007199254740991) {
			return false
		}
		if runs := p.Get("agentRuns"); runs != nil && !isCount(runs, 9007199254740991) {
			return false
		}
		return tokens(p)
	}
	if !isObject(v) || !hasOnly(v, "batches", "parent") {
		return false
	}
	batches := v.Get("batches")
	if !batches.IsArray() || batches.Len() > 20 {
		return false
	}
	for _, one := range batches.Items() {
		if !batch(one) {
			return false
		}
	}
	return parent(v.Get("parent"))
}

// ValidTaskSteps is validTaskSteps: one to twenty steps, at most one in progress.
func ValidTaskSteps(value *Value) bool {
	if !value.IsArray() || value.Len() == 0 || value.Len() > 20 {
		return false
	}
	running := 0
	for _, step := range value.Items() {
		if !isObject(step) || !isText(step.Get("step"), 1000) || strings.TrimSpace(step.Get("step").Str()) == "" {
			return false
		}
		if !oneOf(step.Get("status"), "pending", "in_progress", "completed") {
			return false
		}
		if detail := step.Get("detail"); detail != nil && !isText(detail, 1500) {
			return false
		}
		if step.Get("status").Str() == "in_progress" {
			running++
		}
	}
	return running <= 1
}

// ReadExecution is readExecution: the saved execution progress where it is one the reader admits,
// or the sentence saying it is not. A nil value is no saved progress at all.
func ReadExecution(value *Value) (*contract.Checkpoint, error) {
	if value == nil {
		return nil, nil
	}
	if !isObject(value) || UTF8Bytes(value.Text()) > MaxCheckpointBytes {
		return nil, errSavedExecution
	}
	version := value.Get("version")
	if version == nil || version.Kind() != KindNumber || version.Num() != 1 ||
		!isText(value.Get("id"), 100) || value.Get("id").Str() == "" ||
		!isText(value.Get("requestKey"), 64) || !digest64.MatchString(value.Get("requestKey").Str()) ||
		!oneOf(value.Get("phase"), "plan", "work") ||
		!oneOf(value.Get("status"), "running", "paused", "complete", "stopped") ||
		!oneOf(value.Get("unit"), "model-requests", "provider-managed") {
		return nil, errSavedExecution
	}
	used, limit := value.Get("used"), value.Get("limit")
	grants := value.Get("grants")
	if !isInteger(used) || used.Num() < 0 || !isInteger(limit) || limit.Num() < 1 || limit.Num() > MaxRequestAllowance ||
		used.Num() > limit.Num() || !grants.IsArray() || grants.Len() > 8 {
		return nil, errSavedExecution
	}
	chained := float64(MaxTurns)
	if value.Get("unit").Str() != "model-requests" {
		chained = 1
	}
	for _, grant := range grants.Items() {
		from, to, at := grant.Get("from"), grant.Get("to"), grant.Get("at")
		if !isObject(grant) || from == nil || from.Kind() != KindNumber || from.Num() != chained ||
			!isInteger(to) || to.Num() <= chained || to.Num() > MaxRequestAllowance ||
			!at.IsString() || !parsableDate(at.Str()) {
			return nil, errSavedExecution
		}
		chained = to.Num()
	}
	plan := value.Get("plan")
	if limit.Num() != chained || !plan.IsArray() || (plan.Len() > 0 && !ValidTaskSteps(plan)) ||
		!isText(value.Get("explanation"), 2000) {
		return nil, errSavedExecution
	}
	if reason := value.Get("reason"); reason != nil && !isText(reason, 2000) {
		return nil, errSavedExecution
	}
	if calls := value.Get("testCalls"); calls != nil && (!isInteger(calls) || calls.Num() < 0 || calls.Num() > 400) {
		return nil, errSavedExecution
	}
	if figures := value.Get("delegation"); figures != nil && !ValidDelegationFigures(figures) {
		return nil, errSavedExecution
	}
	if prompts := value.Get("prompts"); prompts != nil {
		if !prompts.IsArray() || prompts.Len() > MaxPromptDigests {
			return nil, errSavedExecution
		}
		last := 0.0
		for _, row := range prompts.Items() {
			request, sha := row.Get("request"), row.Get("sha256")
			if !isObject(row) || !hasOnly(row, "request", "sha256") || !isInteger(request) || request.Num() < 1 || request.Num() <= last ||
				!sha.IsString() || !digest64.MatchString(sha.Str()) {
				return nil, errSavedExecution
			}
			last = request.Num()
		}
	}
	if completions := value.Get("completions"); completions != nil {
		if !completions.IsArray() || completions.Len() > MaxRequestAllowance {
			return nil, errSavedExecution
		}
		for _, row := range completions.Items() {
			if !isObject(row) || !isInteger(row.Get("request")) || row.Get("request").Num() < 1 ||
				row.Get("request").Num() > used.Num() || !oneOf(row.Get("phase"), "work", "synthesis", "review") {
				return nil, errSavedExecution
			}
			if _, ok := readCompletionValue(row.Get("result")); !ok {
				return nil, errSavedExecution
			}
			if attempt := row.Get("attempt"); attempt != nil && (!isInteger(attempt) || attempt.Num() < 1 || attempt.Num() > 3) {
				return nil, errSavedExecution
			}
			if held := row.Get("failure"); held != nil {
				if _, ok := readFailureValue(held); !ok {
					return nil, errSavedExecution
				}
			}
			if wait := row.Get("waitMs"); wait != nil && (!isInteger(wait) || wait.Num() < 0 || wait.Num() > 86400000) {
				return nil, errSavedExecution
			}
		}
	}
	operations := value.Get("operations")
	if !operations.IsArray() || operations.Len() > 400 || !uniqueIDs(operations, "id") {
		return nil, errSavedExecution
	}
	for _, op := range operations.Items() {
		if !isObject(op) || !isText(op.Get("id"), 100) || !isText(op.Get("name"), 150) ||
			!oneOf(op.Get("status"), "running", "complete", "failed", "interrupted") {
			return nil, errSavedExecution
		}
		if result := op.Get("result"); result != nil {
			if !isObject(result) {
				return nil, errSavedExecution
			}
			if content := result.Get("content"); content != nil {
				if !content.IsArray() {
					return nil, errSavedExecution
				}
				for _, part := range content.Items() {
					if !isObject(part) {
						return nil, errSavedExecution
					}
					if kind := part.Get("type"); kind != nil && !kind.IsString() {
						return nil, errSavedExecution
					}
					if text := part.Get("text"); text != nil && !text.IsString() {
						return nil, errSavedExecution
					}
				}
			}
		}
	}
	held := value.Get("context")
	if !isObject(held) || !isText(held.Get("scope"), 100) || !held.Get("entries").IsArray() ||
		held.Get("entries").Len() > 800 || !uniqueIDs(held.Get("entries"), "id") {
		return nil, errSavedExecution
	}
	for _, e := range held.Get("entries").Items() {
		if !isObject(e) || !isText(e.Get("id"), 300) || !strings.HasPrefix(e.Get("id").Str(), held.Get("scope").Str()+":") ||
			!oneOf(e.Get("kind"), "history", "artifact", "request", "tool") || !isText(e.Get("label"), 1000) ||
			!e.Get("digest").IsString() || !digest64.MatchString(e.Get("digest").Str()) {
			return nil, errSavedExecution
		}
		if revision := e.Get("revision"); revision != nil && !isText(revision, 256) {
			return nil, errSavedExecution
		}
		if !isInteger(e.Get("bytes")) || e.Get("bytes").Num() < 0 || !e.Has("value") {
			return nil, errSavedExecution
		}
	}
	var out contract.Checkpoint
	if err := json.Unmarshal(value.JSON(), &out); err != nil {
		return nil, errSavedExecution
	}
	// The completions are rebuilt from the members the reader names, each checked.
	for at, row := range out.Completions {
		completion, _ := failure.ReadCompletion(row.Result)
		rebuilt := contract.Provenance{Request: row.Request, Phase: row.Phase, Result: completion,
			Attempt: row.Attempt, WaitMs: row.WaitMs}
		if row.Failure != nil {
			checked, _ := failure.Read(*row.Failure)
			rebuilt.Failure = &checked
		}
		out.Completions[at] = rebuilt
	}
	return &out, nil
}

func uniqueIDs(list *Value, member string) bool {
	seen := map[string]bool{}
	for _, item := range list.Items() {
		id := item.Get(member)
		key := ""
		if id != nil {
			key = string(id.JSON())
		}
		if seen[key] {
			return false
		}
		seen[key] = true
	}
	return true
}

func readCompletionValue(v *Value) (contract.ProvenanceCompletion, bool) {
	if !isObject(v) {
		return contract.ProvenanceCompletion{}, false
	}
	var held contract.ProvenanceCompletion
	if json.Unmarshal(v.JSON(), &held) != nil {
		return contract.ProvenanceCompletion{}, false
	}
	return failure.ReadCompletion(held)
}

func readFailureValue(v *Value) (contract.Failure, bool) {
	if !isObject(v) {
		return contract.Failure{}, false
	}
	var held contract.Failure
	if json.Unmarshal(v.JSON(), &held) != nil {
		return contract.Failure{}, false
	}
	return failure.Read(held)
}

// parsableDate reports a date Date.parse reads as a finite time.
func parsableDate(text string) bool {
	for _, layout := range []string{time.RFC3339, time.RFC3339Nano, "2006-01-02T15:04:05.000Z0700", "2006-01-02", "2006-01-02T15:04", "2006-01-02T15:04:05"} {
		if _, err := time.Parse(layout, text); err == nil {
			return true
		}
	}
	return false
}

// CanExtendInterruptedExecution is canExtendInterruptedExecution: a stopped API request can fail
// exactly at the ceiling; recovery still needs an explicit grant.
func CanExtendInterruptedExecution(saved *contract.Checkpoint) bool {
	return saved != nil && saved.Unit == "model-requests" && saved.Status == "stopped" &&
		saved.Used == saved.Limit && saved.Limit < MaxRequestAllowance
}

// ExtendExecution is extendExecution: an explicit UI action only. Increasing compute does not
// alter tool or action consent.
func ExtendExecution(checkpoint contract.Checkpoint, interrupted bool, now time.Time) (*contract.Checkpoint, error) {
	raw, err := json.Marshal(checkpoint)
	if err != nil {
		return nil, errSavedExecution
	}
	value, err := ParseJSON(raw)
	if err != nil {
		return nil, errSavedExecution
	}
	saved, err := ReadExecution(value)
	if err != nil {
		return nil, err
	}
	if (saved.Status != "paused" && !(interrupted && CanExtendInterruptedExecution(saved))) ||
		saved.Unit != "model-requests" || saved.Limit >= MaxRequestAllowance {
		return nil, errNoAllowance
	}
	limit := saved.Limit + RequestIncrement
	if limit > MaxRequestAllowance {
		limit = MaxRequestAllowance
	}
	out := *saved
	out.Limit = limit
	out.Status = "running"
	out.Reason = nil
	out.Grants = append(append([]contract.CheckpointGrant{}, saved.Grants...),
		contract.CheckpointGrant{From: saved.Limit, To: limit, At: now.UTC().Format("2006-01-02T15:04:05.000Z")})
	return &out, nil
}
