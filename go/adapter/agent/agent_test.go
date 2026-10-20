package agent_test

import (
	"context"
	"testing"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/agent"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/delegate"
	"github.com/Judgment-Pack/judgment-pack-agent/go/certification"
)

func withDelegation(inner adapter.Adapter) adapter.Adapter {
	return delegate.With(inner, delegate.Options{})
}

// The agent capability's checks on the package's stand-in, the fake exclusive account: every group
// but scenarios 3, 7 and 8, which read the shared server's recorded runtime and run in go/certification.
func TestTheAgentChecksHoldOnTheStandIn(t *testing.T) {
	for _, check := range certification.AgentChecks {
		if check.Group == "scenario 3" || check.Group == "scenario 7" || check.Group == "scenario 8" {
			continue
		}
		t.Run(check.Group, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			findings, err := check.Run(ctx, nil, certification.StandIn(), withDelegation, agent.New())
			if err != nil {
				t.Fatal(err)
			}
			for _, finding := range findings {
				t.Error(finding)
			}
		})
	}
}
