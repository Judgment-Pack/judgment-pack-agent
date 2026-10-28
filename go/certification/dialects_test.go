package certification_test

import (
	"context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/agent"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/eino"
	"github.com/Judgment-Pack/judgment-pack-agent/go/certification"
	"testing"
	"time"
)

func TestDialectAuthoringOnModel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	findings, err := certification.DialectAuthoring.Run(ctx, server, "whole", eino.New())
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range findings {
		t.Error(f)
	}
}
func TestDialectAuthoringOnAgent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	for _, check := range certification.AgentChecks {
		if check.Group != "scenario 23" {
			continue
		}
		findings, err := check.Run(ctx, server, certification.StandIn(), withDelegation, agent.New())
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range findings {
			t.Error(f)
		}
	}
}
