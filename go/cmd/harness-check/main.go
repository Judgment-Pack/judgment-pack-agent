// Command harness-check runs the Go certification against a shared scripted server: every leg the
// TypeScript suite runs on a model engine (the engine leg on three wires, streamed and whole, plain
// and under the tracing variables; the text tool path; the strict turn order; the thinking,
// refutation and wire legs) and the six certified scenarios, on the Eino adapter (go/adapter/eino)
// with the delegation coordinator (go/adapter/delegate). It prints one line per leg and exits 1 when
// any check does not hold, so a host's CI can run it at a locked revision.
//
//	harness-check                      start the repository's shared server (node, npm ci done)
//	harness-check -server http://127.0.0.1:PORT   use one already running
//
// Keyless and offline: the server is the repository's scripted model and recorded runtime.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/delegate"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/eino"
	"github.com/Judgment-Pack/judgment-pack-agent/go/certification"
)

func main() {
	address := flag.String("server", "", "a shared scripted server already running (its listening address); empty starts the repository's")
	only := flag.String("run", "", "run only the legs whose name holds this text")
	flag.Parse()
	os.Exit(run(*address, *only))
}

func run(address, only string) int {
	var server *certification.Server
	if address != "" {
		server = certification.ConnectServer(address)
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		started, err := certification.StartServer(ctx)
		cancel()
		if err != nil {
			fmt.Fprintln(os.Stderr, "harness-check: the scripted server did not start:", err)
			return 2
		}
		server = started
	}
	defer server.Close()
	with := func(inner certification.Adapter) certification.Adapter {
		return delegate.With(inner, delegate.Options{})
	}
	return certify(server, eino.New(), with, only)
}

// certify runs every leg and scenario on adapter against server: 0 where every check held, 1
// otherwise.
func certify(server *certification.Server, adapter certification.Adapter, with func(certification.Adapter) certification.Adapter, only string) int {
	failed, passed := 0, 0
	report := func(name string, findings []certification.Finding, err error) {
		if only != "" && !strings.Contains(name, only) {
			return
		}
		switch {
		case err != nil:
			failed++
			fmt.Printf("FAIL %s: %v\n", name, err)
		case len(findings) > 0:
			failed++
			fmt.Printf("FAIL %s\n", name)
			for _, finding := range findings {
				fmt.Printf("     %s\n", finding)
			}
		default:
			passed++
			fmt.Printf("PASS %s\n", name)
		}
	}
	leg := func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(context.Background(), 2*time.Minute)
	}
	for _, endpoint := range certification.AllLegs {
		name := "engine " + endpoint.API + "/" + endpoint.AnswerAs
		if only == "" || strings.Contains(name, only) {
			ctx, cancel := leg()
			result, err := certification.ScenarioLeg(ctx, server, endpoint, adapter)
			var findings []certification.Finding
			if err == nil {
				findings = result.Checks()
			}
			report(name, findings, err)
			traced, err := certification.ScenarioLegTraced(ctx, server, endpoint, adapter)
			findings = nil
			if err == nil {
				findings = traced.Checks()
			}
			report(name+" traced", findings, err)
			cancel()
		}
	}
	for _, check := range certification.LegChecks() {
		for _, endpoint := range check.Legs {
			name := check.Name + " " + endpoint.API + "/" + endpoint.AnswerAs
			if only != "" && !strings.Contains(name, only) {
				continue
			}
			ctx, cancel := leg()
			findings, err := check.Run(ctx, server, endpoint, adapter)
			cancel()
			report(name, findings, err)
		}
	}
	for _, scenario := range certification.AllScenarios(with) {
		for _, answerAs := range []string{"stream", "whole"} {
			name := "scenario " + scenario.Scenario + "/" + answerAs
			if only != "" && !strings.Contains(name, only) {
				continue
			}
			ctx, cancel := leg()
			findings, err := scenario.Run(ctx, server, answerAs, adapter)
			cancel()
			report(name, findings, err)
		}
	}
	fmt.Printf("%d legs passed, %d failed\n", passed, failed)
	if failed > 0 {
		return 1
	}
	return 0
}
