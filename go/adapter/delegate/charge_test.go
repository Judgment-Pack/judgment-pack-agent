package delegate

import (
	"testing"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/turn"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

// chargeWait bounds how long the fixture waits on a charge that must not block.
const chargeWait = 5 * time.Second

// agent#249: a child's wrapped model request advances the root count only under usageMu, the lock
// save holds from reading that count to the host's acknowledgement, so a sibling's charge cannot
// land between them. A charge inside an atomic claim does not take it.
func TestChildChargeWaitsForTheRootCheckpointInProgress(t *testing.T) {
	c := &coordinator{run: &adapter.Run{ModelName: "fixture", Emit: func(contract.RunEvent) {}}, shared: allowance{limitHere: 10}}
	budget := turn.TaskBudget{ModelRequests: 4}
	c.usageMu.Lock()
	claimed := make(chan error, 1)
	go func() { claimed <- c.chargeChild(&contract.DelegatedTask{ID: "atomic"}, budget, true) }()
	select {
	case err := <-claimed:
		if err != nil {
			c.usageMu.Unlock()
			t.Fatal(err)
		}
	case <-time.After(chargeWait):
		c.usageMu.Unlock()
		<-claimed
		t.Fatal("a charge inside an atomic claim waited for usageMu")
	}
	done := make(chan error, 1)
	go func() { done <- c.chargeChild(&contract.DelegatedTask{ID: "wrapped"}, budget) }()
	select {
	case err := <-done:
		c.usageMu.Unlock()
		t.Fatalf("the charge finished while a root checkpoint held usageMu (err %v, root count %d)", err, c.shared.used())
	case <-time.After(200 * time.Millisecond):
	}
	if used := c.shared.used(); used != 1 {
		c.usageMu.Unlock()
		t.Fatalf("root count %d while a root checkpoint held usageMu, want 1 (the atomic charge only)", used)
	}
	c.usageMu.Unlock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if used := c.shared.used(); used != 2 {
		t.Fatalf("root count %d after the wrapped charge, want 2", used)
	}
}
