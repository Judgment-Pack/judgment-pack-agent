package runcontext

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
	"strings"
	"testing"
)

func TestCallbackProfileAcknowledgesLegacyCheckpointWithoutJournal(t *testing.T) {
	ctx := context.Background()
	s := capSession(t, nil, nil)
	saves := 0
	s.Context.Checkpoint = func(_ context.Context, cp contract.Checkpoint) error {
		saves++
		if cp.Durable != nil {
			t.Fatal("legacy callback received a full journal")
		}
		return nil
	}
	opened := openRun(t, s)
	defer opened.Context.Close()
	if opened.Session.Context.Persistence != nil {
		t.Fatal("callback profile acquired full journal authority")
	}
	before := saves
	if err := opened.Context.ClaimRequest(ctx, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if saves != before+1 {
		t.Fatal("legacy request claim did not wait for checkpoint")
	}
	cp, err := opened.Context.Snapshot("stopped")
	if err != nil {
		t.Fatal(err)
	}
	next := capSession(t, nil, &cp)
	next.Context.Checkpoint = s.Context.Checkpoint
	resumed := openRun(t, next)
	defer resumed.Context.Close()
	if resumed.Context.Execution.State().Used != 1 {
		t.Fatal("legacy callback continuation lost request accounting")
	}
}
func TestCallbackProfileRejectsFullJournalImport(t *testing.T) {
	cp := writerCheckpoint("strict-import")
	cp.Durable = &contract.CheckpointDurableBook{Dispatches: []contract.CheckpointDurableDispatch{}, Observations: []contract.CheckpointDurableObservation{}, Children: []contract.CheckpointDurableBookChildrenItem{}}
	s := capSession(t, nil, &cp)
	s.Context.Checkpoint = func(context.Context, contract.Checkpoint) error { return nil }
	_, err := PrepareDurableInput(context.Background(), s.Context)
	var refused *ContinuationNotRecoverable
	if !errors.As(err, &refused) {
		t.Fatal("callback accepted a full journal")
	}
	store := NewCheckpointCallbackRunStore(func(context.Context, contract.Checkpoint) error { return nil }, func(raw []byte) (contract.Checkpoint, error) {
		var cp contract.Checkpoint
		err := json.Unmarshal(raw, &cp)
		return cp, err
	})
	if _, err = NewRunWriter(store, "strict-import", nil, nil); !errors.As(err, &refused) {
		t.Fatal("callback adapter constructed a full journal writer")
	}
	s.Context.Checkpoint = nil
	s.Context.RunStore = &DurableRunInput{Store: store, ID: "strict-import"}
	if _, err = PrepareDurableInput(context.Background(), s.Context); !errors.As(err, &refused) {
		t.Fatal("configured callback adapter accepted a full journal")
	}
}

// A host may acknowledge a checkpoint and cancel before that acknowledgement is released.
// Both cancellation positions must refuse opening; an approved act never escapes the first.
func TestCallbackApprovalCancellationBeforeAcknowledgementRelease(t *testing.T) {
	for _, at := range []string{"approved", "executed"} {
		t.Run(at, func(t *testing.T) {
			ran := 0
			session, saved := pausedSession(t, &ran)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var states []string
			session.Context = &Input{CurrentRequest: session.Context.CurrentRequest, Execution: &saved, Approval: &contract.CheckpointApprovalDecision{CallID: "call-a", Approved: true}, Checkpoint: func(_ context.Context, state contract.Checkpoint) error {
				states = append(states, state.Approval.State)
				if state.Approval.State == at {
					cancel()
				}
				return nil
			}}
			opened, err := ContextualSession(ctx, session)
			if opened != nil {
				opened.Context.Close()
			}
			if opened != nil || !failure.IsCancelled(err) {
				t.Fatalf("cancelled acknowledgement released an opened run: opened=%v err=%v", opened != nil, err)
			}
			want, wantRan := "approved", 0
			if at == "executed" {
				want, wantRan = "approved,executed", 1
			}
			if strings.Join(states, ",") != want || ran != wantRan {
				t.Fatalf("cancel at %s: acknowledged %v, executions %d", at, states, ran)
			}
		})
	}
}

// The writer must also refuse release if a store publishes a valid receipt and the
// root is cancelled before Commit returns. Later saves retain that cancellation.
func TestCallbackProfileCancellationAfterCommittedReceiptIsSticky(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	commits := 0
	store := NewMemoryRunStore(MemoryRunStoreOptions{AfterCommit: func() error { commits++; cancel(); return nil }})
	save := legacyCheckpointSave(store, "callback-receipt-cancel", nil)
	cp := writerCheckpoint("callback-receipt-cancel")
	if err := save(ctx, cp); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled committed receipt released successfully: %v", err)
	}
	if err := save(context.Background(), cp); !errors.Is(err, context.Canceled) {
		t.Fatalf("writer lost sticky receipt cancellation: %v", err)
	}
	if commits != 1 {
		t.Fatalf("cancelled writer committed again: %d", commits)
	}
}
