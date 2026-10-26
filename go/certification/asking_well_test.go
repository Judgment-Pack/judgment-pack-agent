package certification_test

import (
	"context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/agent"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/eino"
	"github.com/Judgment-Pack/judgment-pack-agent/go/certification"
	"testing"
	"time"
)

func TestAskingWellOnModel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	findings, err := certification.AskingWell.Run(ctx, server, "whole", eino.New())
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range findings {
		t.Error(f)
	}
}
func TestAskingWellOnAgent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	for _, c := range certification.AgentChecks {
		if c.Group != "scenario 24" {
			continue
		}
		findings, err := c.Run(ctx, server, certification.StandIn(), withDelegation, agent.New())
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range findings {
			t.Error(f)
		}
	}
}
