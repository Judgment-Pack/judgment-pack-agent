package runcontext

// The progress tracker (the port of packages/harness/execution/progress.ts): completed work
// compared without interpreting its topic, tools or result meaning.

// RepeatedWorkLimit is how many consecutive repetitions stall a response.
const RepeatedWorkLimit = 3

// RecoverRepeatedWorkInstructions is the one recovery request a stalled response earns.
const RecoverRepeatedWorkInstructions = "Several consecutive operations repeated completed work and returned unchanged results. This is one recovery request: choose a different concrete action using the retained working arguments and results, or finish with the exact remaining blocker. Repeating a completed call with the same arguments will be refused without executing it. A saved workspace read is not the unsaved working proposal. Do not restart discovery or request more allowance merely to repeat work. All existing permissions and approval requirements still apply."

// RepeatedWorkInstructions is what a response that kept repeating itself is told.
const RepeatedWorkInstructions = "Several consecutive operations repeated the same arguments and returned unchanged results. Further tool calls are unavailable for this response. Use the retained results to finish the current request or state the concrete remaining blocker. Do not claim a saved change, successful tests or task completion without the corresponding results. Do not request more budget merely to repeat these operations."

// ProgressTracker is ExecutionProgressTracker: what the response has already done, compared as
// canonical JSON so a different spelling of the same arguments is the same work.
type ProgressTracker struct {
	seen      map[string]bool
	repeated  int
	requests  map[string]bool
	recovered bool
	step      string
}

// NewProgressTracker is a tracker at the start of a phase.
func NewProgressTracker() *ProgressTracker {
	tracker := &ProgressTracker{}
	tracker.BeginPhase()
	return tracker
}

// BeginPhase starts a new phase (a run, or the critic's own).
func (p *ProgressTracker) BeginPhase() {
	p.seen = map[string]bool{}
	p.requests = map[string]bool{}
	p.repeated = 0
	p.recovered = false
	p.step = "work"
}

// NextStep is what this request is: work, the one recovery request, or the finish.
func (p *ProgressTracker) NextStep(remaining int) string {
	switch {
	case !p.Stalled():
		p.step = "work"
	case !p.recovered && remaining > 1:
		p.step = "recover"
	default:
		p.step = "finish"
	}
	if p.step == "recover" {
		p.recovered = true
	}
	return p.step
}

// Rejects reports a call the recovery request refuses: the same work again.
func (p *ProgressTracker) Rejects(name string, args *Value) bool {
	return p.step == "recover" && p.requests[CanonicalJSON(ObjectOf("name", String(name), "args", orNull(args)))]
}

// Seed takes the operations a restored checkpoint carries, without their repetition count.
func (p *ProgressTracker) Seed(operations []*Operation) {
	p.BeginPhase()
	for _, operation := range operations {
		p.Observe(operation)
	}
	p.repeated = 0
}

// Observe takes one completed operation.
func (p *ProgressTracker) Observe(operation *Operation) {
	if operation.Status == "running" || operation.Status == "interrupted" || operation.Result == nil {
		p.repeated = 0
		return
	}
	// A host refusal is not new evidence or completed work. Keep the stalled state so one refused
	// recovery call cannot open another discovery loop.
	if operation.Status == "failed" {
		if held := operation.Result.StructuredContent.Get("failure"); held != nil {
			if f, ok := readFailureValue(held); ok && f.Code == "repeated-work" && f.Source == "desk" && f.Outcome == "not-started" {
				return
			}
		}
	}
	p.requests[CanonicalJSON(ObjectOf("name", String(operation.Name), "args", orNull(operation.Args)))] = true
	key := CanonicalJSON(ObjectOf("name", String(operation.Name), "args", orNull(operation.Args), "result", operation.Result.Value()))
	if p.seen[key] {
		p.repeated++
	} else {
		p.repeated = 0
	}
	p.seen[key] = true
}

// Stalled reports a response that kept repeating completed work.
func (p *ProgressTracker) Stalled() bool { return p.repeated >= RepeatedWorkLimit }

// Guidance is the note the request state carries where results have repeated, and "" otherwise.
func (p *ProgressTracker) Guidance() string {
	if p.repeated >= 2 {
		return "Repeated unchanged results detected. Reuse the completed operations; make a materially different next step or finish the response. Avoid rereading or revalidating the same data."
	}
	return ""
}
