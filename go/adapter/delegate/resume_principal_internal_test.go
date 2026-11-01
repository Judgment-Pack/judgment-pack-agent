package delegate

// ADR-0029: a run with a delegation it has not opened starts no child, whatever opened or did not open
// before the child's start. The real paths open the parent first, so this reaches the hold directly.

import (
	"context"
	"strings"
	"testing"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/turn"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

func TestChildStartsOnlyUnderAnOpenedDelegation(t *testing.T) {
	c := &coordinator{run: &adapter.Run{Context: &runcontext.Input{Principal: &runcontext.RunPrincipal{}}}}
	err := c.runChild(context.Background(), &planned{}, turn.TaskProfile{}, &contract.DelegatedTask{}, "", "", 0, false, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "delegation is not open") {
		t.Fatalf("got %v", err)
	}
}
