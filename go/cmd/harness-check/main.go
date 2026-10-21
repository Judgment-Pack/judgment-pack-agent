// Command harness-check runs the Go certification against a shared scripted server: every leg the
// TypeScript suite runs on a model engine (the engine leg on three wires, streamed and whole, plain
// and under the tracing variables; the text tool path; the strict turn order; the thinking,
// refutation and wire legs) and the ten certified scenarios, on the Eino adapter (go/adapter/eino)
// with the delegation coordinator (go/adapter/delegate); then the agent capability's checks
// (ADR-0007) on the agent adapter (go/adapter/agent) with the same coordinator. It prints one line
// per leg and exits 1 when any check does not hold, so a host's CI can run it at a locked revision.
//
//	harness-check                      start the repository's shared server (node, npm ci done)
//	harness-check -server http://127.0.0.1:PORT   use one already running
//	harness-check -agent               the agent capability's checks alone
//
// The agent capability's checks here run against the package's stand-in, a fake exclusive account,
// and the command says so: passing it certifies no host's bridge. A host certifies its own provider
// bridge, account and tool handler by calling certification.CertifyAgent with its AgentHost from a
// command or test of its own.
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

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/agent"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/delegate"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/eino"
	"github.com/Judgment-Pack/judgment-pack-agent/go/certification"
)

func main() {
	address := flag.String("server", "", "a shared scripted server already running (its listening address); empty starts the repository's")
	only := flag.String("run", "", "run only the legs whose name holds this text")
	agentOnly := flag.Bool("agent", false, "run only the agent capability's checks, on the package's stand-in")
	flag.Parse()
	os.Exit(run(*address, *only, *agentOnly))
}

func run(address, only string, agentOnly bool) int {
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
	code := 0
	if !agentOnly {
		code = certify(server, eino.New(), with, only)
	}
	if agentCode := certifyAgent(server, certification.StandIn(), with, only); agentCode > code {
		code = agentCode
	}
	printMeasurement(certification.TakeMeasurement())
	fmt.Println("scenario 12, two-family measurement: adapter author critic policy requests checks")
	for _, row := range certification.TakeProviderMeasurement() {
		fmt.Printf("  %s %s %s %s %d %d\n", row.Adapter, row.Author, row.Critic, row.Policy, row.Requests, row.Checks)
	}
	return code
}

// printMeasurement prints scenario 10's measurement (ADR-0010) as a table: the same proposal at two
// author tiers, with and without the critic, one line per run. Recorded, not judged.
func printMeasurement(rows []certification.MeasuredRow) {
	if len(rows) == 0 {
		return
	}
	fmt.Println("scenario 10, the measurement (the same proposal at two author tiers, with and without the critic; no verdict)")
	columns := []string{"adapter", "leg", "author", "critic", "ran", "reasoning", "refuted", "checks", "criticRequests"}
	table := [][]string{columns}
	orDash := func(value string) string {
		if value == "" {
			return "-"
		}
		return value
	}
	for _, row := range rows {
		table = append(table, []string{row.Adapter, row.Leg, row.Author, row.Critic, fmt.Sprint(row.Ran), orDash(row.Reasoning), orDash(row.Refuted), fmt.Sprint(row.Checks), fmt.Sprint(row.CriticRequests)})
	}
	widths := make([]int, len(columns))
	for _, line := range table {
		for at, cell := range line {
			widths[at] = max(widths[at], len(cell))
		}
	}
	for _, line := range table {
		var cells []string
		for at, cell := range line {
			cells = append(cells, cell+strings.Repeat(" ", widths[at]-len(cell)))
		}
		fmt.Println("  " + strings.TrimRight(strings.Join(cells, "  "), " "))
	}
}

// certifyAgent runs the agent capability's checks against host: 0 where every check held, 1
// otherwise.
func certifyAgent(server *certification.Server, host certification.AgentHost, with func(certification.Adapter) certification.Adapter, only string) int {
	passed, failed := certification.CertifyAgent(context.Background(), os.Stdout, server, host, with, agent.New(), only)
	fmt.Printf("%d agent checks passed, %d failed", passed, failed)
	if host.StandIn {
		fmt.Printf(" (the package's stand-in only: no host's bridge was certified)")
	}
	fmt.Println()
	if failed > 0 {
		return 1
	}
	return 0
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
