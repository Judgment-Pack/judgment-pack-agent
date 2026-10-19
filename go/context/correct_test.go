package runcontext

// The verify and correct loop (the port of packages/harness/execution/correctArtifact.test.ts and
// checkArtifact.test.ts).

import (
	"context"
	"strings"
	"testing"
)

// candidate is the artifact the loop corrects: its kind, its revision and its typed facts.
type candidate struct {
	kind     string
	revision int
	facts    *Value
}

func TestTheLoopChecksAndCheckpointsBeforeEachCorrection(t *testing.T) {
	for _, kind := range []string{"pack", "dialect", "future-artifact"} {
		events := []string{}
		facts := MustParseJSON(`{"decimal":"24","zero":0,"flag":false,"nothing":null}`)
		initial := candidate{kind: kind, revision: 1, facts: facts}
		result, err := CorrectArtifact(context.Background(), CorrectionPorts[candidate, bool]{
			Initial: initial, MaxCorrections: 2,
			Fingerprint: func(held candidate) string { return itoa(held.revision) },
			Check: func(ctx context.Context, held candidate) (bool, error) {
				events = append(events, "check"+itoa(held.revision))
				return held.revision == 2, nil
			},
			Passed: func(check bool) bool { return check },
			Checkpoint: func(ctx context.Context, held candidate, check bool, corrections int) error {
				events = append(events, "saved"+itoa(held.revision))
				return nil
			},
			Correct: func(ctx context.Context, held candidate, check bool) (candidate, bool, error) {
				events = append(events, "correct"+itoa(held.revision))
				held.revision = 2
				return held, true, nil
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Join(events, ",") != "check1,saved1,correct1,check2,saved2" {
			t.Errorf("%s: the events are %v", kind, events)
		}
		if result.State != CorrectionReady || result.Corrections != 1 || !equalJSON(result.Candidate.facts, facts) {
			t.Errorf("%s: the result is %+v", kind, result)
		}
		if initial.revision != 1 || !equalJSON(initial.facts, facts) {
			t.Errorf("%s: the initial candidate was changed", kind)
		}
	}
}

func TestTheLoopStopsOnCyclesLimitsAndPolicyAnswers(t *testing.T) {
	run := func(next func(int) (int, bool), max int) (CorrectionResult[int, bool], int, int) {
		checks, corrections := 0, 0
		result, err := CorrectArtifact(context.Background(), CorrectionPorts[int, bool]{
			Initial: 1, MaxCorrections: max,
			Fingerprint: itoa,
			Check: func(ctx context.Context, held int) (bool, error) {
				checks++
				return false, nil
			},
			Passed:     func(check bool) bool { return check },
			Checkpoint: func(ctx context.Context, held int, check bool, at int) error { return nil },
			Correct: func(ctx context.Context, held int, check bool) (int, bool, error) {
				corrections++
				out, ok := next(held)
				return out, ok, nil
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		return result, checks, corrections
	}
	cycle, checks, _ := run(func(held int) (int, bool) {
		if held == 1 {
			return 2, true
		}
		return 1, true
	}, 5)
	if cycle.State != CorrectionStalled || checks != 2 {
		t.Errorf("a cycle gave %s after %d checks", cycle.State, checks)
	}
	limited, _, corrections := run(func(held int) (int, bool) { return held + 1, true }, 2)
	if limited.State != CorrectionLimit || corrections != 2 {
		t.Errorf("the limit gave %s after %d corrections", limited.State, corrections)
	}
	policy, _, corrections := run(func(held int) (int, bool) { return 0, false }, 5)
	if policy.State != CorrectionAnswered || corrections != 1 {
		t.Errorf("a policy answer gave %s after %d corrections", policy.State, corrections)
	}
}

func TestTheLoopPropagatesStorageAndCancellation(t *testing.T) {
	ports := func() CorrectionPorts[int, bool] {
		return CorrectionPorts[int, bool]{Initial: 1, MaxCorrections: 2, Fingerprint: itoa,
			Check:      func(ctx context.Context, held int) (bool, error) { return false, nil },
			Passed:     func(check bool) bool { return check },
			Checkpoint: func(ctx context.Context, held int, check bool, at int) error { return nil },
			Correct:    func(ctx context.Context, held int, check bool) (int, bool, error) { return held + 1, true, nil }}
	}
	failure := &authoredError{"the check could not run"}
	held := ports()
	held.Check = func(ctx context.Context, at int) (bool, error) { return false, failure }
	if _, err := CorrectArtifact(context.Background(), held); err != failure {
		t.Errorf("the check's failure answered %v", err)
	}
	held = ports()
	held.Checkpoint = func(ctx context.Context, at int, check bool, corrections int) error { return failure }
	if _, err := CorrectArtifact(context.Background(), held); err != failure {
		t.Errorf("the checkpoint's failure answered %v", err)
	}
	// A run that ends while the check is in flight starts no correction.
	corrections := 0
	ctx, cancel := context.WithCancel(context.Background())
	held = ports()
	held.Check = func(context.Context, int) (bool, error) {
		cancel()
		return false, nil
	}
	held.Correct = func(ctx context.Context, at int, check bool) (int, bool, error) {
		corrections++
		return at + 1, true, nil
	}
	if _, err := CorrectArtifact(ctx, held); err != context.Canceled {
		t.Errorf("a cancelled run answered %v", err)
	}
	if corrections != 0 {
		t.Error("a cancelled run started a correction")
	}
	// A correction limit that is not a count is refused.
	held = ports()
	held.MaxCorrections = -1
	if _, err := CorrectArtifact(context.Background(), held); err == nil {
		t.Error("a negative correction limit was admitted")
	}
}

func TestTheCheckRetainsTheCandidateAndItsFindings(t *testing.T) {
	events := []string{}
	held := MustParseJSON(`{"version":"9007199254740993.0.0","value":false,"missing":null}`)
	finding := ObjectOf("status", String("refused"), "findings", Array(ObjectOf("code", String("invalid"), "path", String("/value"))))
	result, err := CheckArtifact(context.Background(), CheckPorts[*Value, *Value]{
		Candidate:   held,
		Fingerprint: func(c *Value) string { return c.Text() },
		Retain: func(ctx context.Context, c *Value, check **Value) error {
			if c != held {
				t.Error("the candidate was copied")
			}
			if check == nil {
				events = append(events, "candidate")
				return nil
			}
			events = append(events, "findings")
			if *check != finding {
				t.Error("the findings were copied")
			}
			return nil
		},
		Check: func(ctx context.Context, c *Value) (*Value, error) {
			if c != held {
				t.Error("the candidate was copied")
			}
			events = append(events, "check")
			return finding, nil
		},
		Passed: func(check *Value) bool { return false },
	})
	if err != nil {
		t.Fatal(err)
	}
	if result != finding {
		t.Error("the findings were not returned")
	}
	if strings.Join(events, ",") != "candidate,check,findings" {
		t.Errorf("the events are %v", events)
	}
}

func TestTheCheckDoesNotDispatchWhenRetainingFails(t *testing.T) {
	checks, retains := 0, 0
	failure := &authoredError{"Persistence unavailable"}
	ports := CheckPorts[int, bool]{Candidate: 1, Fingerprint: itoa,
		Retain: func(ctx context.Context, held int, check *bool) error {
			retains++
			if retains == 1 || retains == 3 {
				return failure
			}
			return nil
		},
		Check: func(ctx context.Context, held int) (bool, error) {
			checks++
			return false, nil
		},
		Passed: func(check bool) bool { return check }}
	if _, err := CheckArtifact(context.Background(), ports); err != failure {
		t.Errorf("the first retain answered %v", err)
	}
	if checks != 0 {
		t.Error("a check was dispatched although retaining failed")
	}
	// The second call retains the candidate and fails on the findings, after the check.
	if _, err := CheckArtifact(context.Background(), ports); err != failure {
		t.Errorf("the findings' retain answered %v", err)
	}
	if checks != 1 {
		t.Errorf("%d checks were dispatched", checks)
	}
}

func TestTheCheckPreservesInterruption(t *testing.T) {
	events := []string{}
	ctx, cancel := context.WithCancel(context.Background())
	_, err := CheckArtifact(ctx, CheckPorts[string, bool]{Candidate: "candidate", Fingerprint: func(c string) string { return c },
		Retain: func(ctx context.Context, held string, check *bool) error {
			events = append(events, "retained")
			return nil
		},
		Check: func(ctx context.Context, held string) (bool, error) {
			events = append(events, "check")
			cancel()
			return false, nil
		},
		Passed: func(check bool) bool { return check }})
	if err != context.Canceled {
		t.Errorf("the cancelled check answered %v", err)
	}
	if strings.Join(events, ",") != "retained,check" {
		t.Errorf("the events are %v", events)
	}
}
