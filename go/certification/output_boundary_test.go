package certification_test

import (
	"context"
	"testing"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/agent"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/eino"
	"github.com/Judgment-Pack/judgment-pack-agent/go/certification"
)

func TestOutputBoundaryOnAgent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	for _, check := range certification.AgentChecks {
		if check.Group != "scenario 17" {
			continue
		}
		findings, err := check.Run(ctx, server, certification.StandIn(), withDelegation, agent.New())
		if err != nil {
			t.Fatal(err)
		}
		for _, finding := range findings {
			t.Error(finding)
		}
	}
}

func TestOutputBoundaryOnModel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	findings, err := certification.OutputBoundary.Run(ctx, server, "whole", eino.New())
	if err != nil {
		t.Fatal(err)
	}
	for _, finding := range findings {
		t.Error(finding)
	}
}
