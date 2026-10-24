package runcontext

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
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
