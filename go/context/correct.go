package runcontext

// The host-owned verify and correct loop shared by artifact adapters (the port of
// packages/harness/execution/correctArtifact.ts and checkArtifact.ts). It never saves, grants
// permission, retries transport, changes expectations, or allocates model budget.

import "context"

// CorrectionState is where a correction loop stopped: ready (the check passed), limit (no
// corrections left), stalled (a candidate it had already tried) or answered (the corrector
// returned none).
type CorrectionState string

// The states a correction loop stops in.
const (
	CorrectionReady    CorrectionState = "ready"
	CorrectionLimit    CorrectionState = "limit"
	CorrectionStalled  CorrectionState = "stalled"
	CorrectionAnswered CorrectionState = "answered"
)

// CorrectionResult is what a correction loop answers: where it stopped, the candidate it stopped
// on, that candidate's check, and how many corrections it made.
type CorrectionResult[T any, C any] struct {
	State       CorrectionState
	Candidate   T
	Check       C
	Corrections int
}

// CorrectionPorts are what a correction loop is given (CorrectionPorts).
type CorrectionPorts[T any, C any] struct {
	Initial        T
	MaxCorrections int
	Fingerprint    func(candidate T) string
	Check          func(ctx context.Context, candidate T) (C, error)
	Passed         func(check C) bool
	// Correct answers the next candidate, or ok false where there is none to try.
	Correct func(ctx context.Context, candidate T, check C) (T, bool, error)
	// Checkpoint persists the exact candidate and check before another model request is possible.
	Checkpoint func(ctx context.Context, candidate T, check C, corrections int) error
}

var errCorrectionLimit = &authoredError{"Invalid correction limit."}

// CorrectArtifact is correctArtifact: check, checkpoint, correct, until the check passes, the
// corrections run out, the corrector answers none, or a candidate repeats.
func CorrectArtifact[T any, C any](ctx context.Context, ports CorrectionPorts[T, C]) (CorrectionResult[T, C], error) {
	var zero CorrectionResult[T, C]
	if ports.MaxCorrections < 0 {
		return zero, errCorrectionLimit
	}
	candidate := ports.Initial
	seen := map[string]bool{ports.Fingerprint(candidate): true}
	for corrections := 0; ; corrections++ {
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		check, err := ports.Check(ctx, candidate)
		if err != nil {
			return zero, err
		}
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		if err := ports.Checkpoint(ctx, candidate, check, corrections); err != nil {
			return zero, err
		}
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		result := CorrectionResult[T, C]{Candidate: candidate, Check: check, Corrections: corrections}
		if ports.Passed(check) {
			result.State = CorrectionReady
			return result, nil
		}
		if corrections >= ports.MaxCorrections {
			result.State = CorrectionLimit
			return result, nil
		}
		next, ok, err := ports.Correct(ctx, candidate, check)
		if err != nil {
			return zero, err
		}
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		if !ok {
			result.State = CorrectionAnswered
			return result, nil
		}
		key := ports.Fingerprint(next)
		if seen[key] {
			result.State = CorrectionStalled
			return result, nil
		}
		seen[key] = true
		candidate = next
	}
}

// CheckPorts are what one model-free check is given (the ports of checkArtifact).
type CheckPorts[T any, C any] struct {
	Candidate   T
	Fingerprint func(candidate T) string
	// Retain persists the candidate before the check, and the findings before they are returned;
	// check is nil on the first call.
	Retain func(ctx context.Context, candidate T, check *C) error
	Check  func(ctx context.Context, candidate T) (C, error)
	Passed func(check C) bool
}

// CheckArtifact is checkArtifact: a host-tool proposal is one attempt in the existing managed
// agent turn. Retain it before checking, and retain findings before returning corrective
// guidance. Further attempts use that turn's existing tools and allowance. This model-free check
// never starts a nested session or grants a write.
func CheckArtifact[T any, C any](ctx context.Context, ports CheckPorts[T, C]) (C, error) {
	var zero C
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if err := ports.Retain(ctx, ports.Candidate, nil); err != nil {
		return zero, err
	}
	result, err := CorrectArtifact(ctx, CorrectionPorts[T, C]{
		Initial: ports.Candidate, MaxCorrections: 0, Fingerprint: ports.Fingerprint,
		Check: ports.Check, Passed: ports.Passed,
		Checkpoint: func(ctx context.Context, candidate T, check C, corrections int) error {
			return ports.Retain(ctx, candidate, &check)
		},
		Correct: func(ctx context.Context, candidate T, check C) (T, bool, error) {
			var none T
			return none, false, nil
		},
	})
	if err != nil {
		return zero, err
	}
	return result.Check, nil
}
