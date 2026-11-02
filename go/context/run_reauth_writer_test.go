package runcontext

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
)

// The writer's half of #226 at the writer's own port, with no engine: the typed never-admitted failure on a
// row means a refusal the package trusted for that very dispatch (#278).

const reauthWriterKey = "wk.reauth.writer.Q7z2Xm9LpV4cR8tNb3Yh6Df1G"

func reauthWriterRequest() []byte { return []byte(`{"title":"Mileage"}`) }

// reauthWriterClaim is a run whose write the reference host refused, parked, woken and claimed: the
// credentials current again, the continuation not yet run.
func reauthWriterClaim(t *testing.T) (durableWaitStore, *ReferenceEffectHost, ResumedWait) {
	t.Helper()
	ctx := context.Background()
	clock := newLeaseClock()
	store := durableWaitStore{NewMemoryRunStore(MemoryRunStoreOptions{Now: clock.now})}
	principal := "sha256:" + strings.Repeat("7", 64)
	var refuse atomic.Bool
	refuse.Store(true)
	host := NewReferenceEffectHostWith(store, ReferenceEffectHostOptions{Credentials: func(context.Context, EffectOperation) (*CredentialRefusal, error) {
		if refuse.Load() {
			return &CredentialRefusal{Cause: "expired", Connection: "tracker-conn", Principal: principal, Permissions: []string{"issues:write"}, Requires: []string{"issues:write"}}, nil
		}
		return nil, nil
	}})
	lease, err := store.AcquireLease(ctx, LeaseAcquire{Resource: "run-r", Owner: "worker-a", DurationMs: waitHour})
	if err != nil {
		t.Fatal(err)
	}
	fence := FenceOf(lease)
	view, err := UnattendedRunStore(store, fence, host)
	if err != nil {
		t.Fatal(err)
	}
	ran := []string{}
	if _, err := InvokeDurable(ctx, durableWriter(t, view, "run-r", false), "host", "tracker", "create_issue", reauthWriterRequest(), false, runs(&ran), nil, nil); err == nil || len(ran) != 0 {
		t.Fatalf("the refusal: %v, ran %q", err, ran)
	}
	digest, err := WakeKeyDigest(reauthWriterKey)
	if err != nil {
		t.Fatal(err)
	}
	event := contract.RunWaitEventCondition{Kind: "reauthorized", Subject: "tracker-conn"}
	if _, _, err := ParkRun(ctx, ParkInput{Store: store, Fence: fence, Wait: "wait-r", Continuation: "continue-r", Condition: WaitCondition{Event: &event}, Model: waitModel, WakeKeyDigest: digest, Release: []WaitRelease{}, Reauthorization: ReauthorizationOf(view)}); err != nil {
		t.Fatal(err)
	}
	clock.advance(leaseMinute)
	if _, err := WakeRun(ctx, store, WakeEvidence{Version: 1, Resource: "run-r", Wait: "wait-r", Evidence: "refreshed-1", Kind: "event", Event: &event, Key: reauthWriterKey}); err != nil {
		t.Fatal(err)
	}
	action := ReauthAction{Version: 1, Resource: "run-r", Wait: "wait-r", Connection: "tracker-conn", Action: "refresh", Principal: principal, Permissions: []string{"issues:write"}, Actor: "person-owner", At: waitTime(clock, 0)}
	resumed, err := ResumeParked(ctx, withCurrent(t, ResumeInput{Store: store, Resource: "run-r", Wait: "wait-r", Evidence: "refreshed-1", Owner: "worker-b", DurationMs: waitHour, Reauthorization: &action}))
	if err != nil {
		t.Fatal(err)
	}
	refuse.Store(false)
	return store, host, resumed
}

func reauthWriterRow(t *testing.T, store RunStore, tool string) DurableDispatch {
	t.Helper()
	writer, _ := writerFixture(t, store, "run-r", true)
	for i := len(writer.book.Dispatches) - 1; i >= 0; i-- {
		if row := writer.book.Dispatches[i]; row.Tool == tool {
			return row
		}
	}
	t.Fatalf("no row for %s", tool)
	return DurableDispatch{}
}

func typedNeverAdmitted() error { return failure.Err("authentication", "tool", "not-started") }

// A re-send admitted and then failing with the typed failure inside another error took the effect. Its row
// says only unknown, so no later view of the claim can send it again.
func TestReauthAWrappedTypedFailureIsNoGrant(t *testing.T) {
	ctx := context.Background()
	for name, fails := range map[string]func() error{
		"a typed failure nested in the error": func() error { return fmt.Errorf("wrapped: %w", typedNeverAdmitted()) },
		"the exported class nested in it":     func() error { return fmt.Errorf("wrapped: %w", &ReauthorizationRequired{}) },
		"joined with another error":           func() error { return errors.Join(errors.New("first"), typedNeverAdmitted()) },
	} {
		t.Run(name, func(t *testing.T) {
			store, host, resumed := reauthWriterClaim(t)
			executed := 0
			execute := func([]byte) (ToolResult, error) {
				executed++
				if executed == 1 {
					return ToolResult{}, fails()
				}
				return ResultOf([]Content{TextContent("again")}, nil), nil
			}
			write := func() error {
				view, err := ReauthorizedRunStore(ctx, store, host, resumed)
				if err != nil {
					t.Fatal(err)
				}
				_, err = InvokeDurable(ctx, durableWriter(t, view, "run-r", true), "host", "tracker", "create_issue", reauthWriterRequest(), false, execute, nil, nil)
				return err
			}
			if err := write(); err == nil {
				t.Fatal("the failure did not reach the caller")
			}
			row := reauthWriterRow(t, store, "create_issue")
			if executed != 1 || row.State != "unknown" || row.Failure == nil || row.Failure.Code != "authentication" || row.Failure.Outcome != "unknown" || row.Failure.Detail != nil {
				t.Fatalf("executed %d row %+v failure %+v", executed, row, row.Failure)
			}
			var answered *HostOutcomeUnknown
			if err := write(); !errors.As(err, &answered) || executed != 1 {
				t.Fatalf("a second view of the claim: %v, executed %d", err, executed)
			}
		})
	}
}

// The typed failure on a row means that refusal and nothing else, on a read's row too, though grants and
// parks skip those.
func TestReauthAReadsTypedFailureSaysOnlyUnknown(t *testing.T) {
	ctx := context.Background()
	for name, fails := range map[string]func() error{"as itself": typedNeverAdmitted, "nested in the error": func() error { return fmt.Errorf("wrapped: %w", typedNeverAdmitted()) }} {
		t.Run(name, func(t *testing.T) {
			store := durableWaitStore{NewMemoryRunStore(MemoryRunStoreOptions{})}
			lease, err := store.AcquireLease(ctx, LeaseAcquire{Resource: "run-r", Owner: "worker-a", DurationMs: waitHour})
			if err != nil {
				t.Fatal(err)
			}
			view, err := UnattendedRunStore(store, FenceOf(lease), NewReferenceEffectHost(store))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := InvokeDurable(ctx, durableWriter(t, view, "run-r", false), "host", "tracker", "read_issue", []byte(`{}`), true, func([]byte) (ToolResult, error) { return ToolResult{}, fails() }, nil, nil); err == nil {
				t.Fatal("the failure did not reach the caller")
			}
			row := reauthWriterRow(t, store, "read_issue")
			if !row.Read || row.Failure == nil || row.Failure.Code != "authentication" || row.Failure.Outcome != "unknown" || row.Failure.Detail != nil {
				t.Fatalf("row %+v failure %+v", row, row.Failure)
			}
		})
	}
}
