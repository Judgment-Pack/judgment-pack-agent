package runcontext

// ADR-0029: the paths that reach a dispatch under a delegation beside a source's own call. The twins of
// packages/harness/principalPaths.test.ts.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/wire"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

const principalRevoked = `{"admission":"refuse","refusal":"revoked"}`

func principalLocalTool(ran *principalRan, effect string) HostTool {
	operation := "read"
	switch effect {
	case "pure":
		operation = "transform"
	case "propose":
		operation = "propose"
	}
	return HostTool{Name: "local_lookup", Description: "local_lookup", InputSchema: json.RawMessage(`{"type":"object"}`), Taxonomy: &wire.ToolTaxonomy{Operation: operation, Effect: effect}, Execute: func(context.Context, json.RawMessage) (ToolResult, error) {
		ran.add("local_lookup")
		return ResultOf([]Content{TextContent("looked up")}, nil), nil
	}}
}

// principalPathSession is a run under the root chain whose port is port, at port's time.
func principalPathSession(t *testing.T, tools []HostTool, port *principalPort) Session {
	t.Helper()
	session, _ := trackedSession(t)
	session.HostTools = tools
	input := *session.Context
	v := loadPrincipalChains(t)
	input.Principal = principalInput(v, v.Items[0], func(_ context.Context, read PrincipalAdmissionRead) ([]byte, error) {
		port.mu.Lock()
		defer port.mu.Unlock()
		port.reads = append(port.reads, read)
		if read.Call.Count && port.answerCounted != "" {
			return []byte(port.answerCounted), nil
		}
		return []byte(port.answer), nil
	}, func() (time.Time, error) { return time.Parse(time.RFC3339Nano, port.now) })
	session.Context = &input
	return session
}

func TestPrincipalHostToolWithNoSource(t *testing.T) {
	for _, effect := range []string{"pure", "read", "propose"} {
		t.Run(effect+" under a standing delegation, read as source local and never counted", func(t *testing.T) {
			ran, port := &principalRan{}, newPrincipalPort()
			run := openRun(t, principalPathSession(t, []HostTool{principalLocalTool(ran, effect)}, port))
			defer run.Context.Close()
			if _, err := hostTool(t, run, "local_lookup").Execute(context.Background(), json.RawMessage(`{}`)); err != nil {
				t.Fatal(err)
			}
			if !sameStrings(ran.get(), []string{"local_lookup"}) || len(port.reads) != 1 || port.reads[0].Call != (PrincipalAdmissionCall{Source: "local", Tool: "local_lookup", Effect: effect, Count: false}) {
				t.Fatalf("ran %v reads %+v", ran.get(), port.reads)
			}
		})
		for label, row := range map[string]struct{ now, answer, name string }{
			"past its expiry": {"2026-10-30T08:00:00.000Z", `{"admission":"admit"}`, "expired"},
			"revoked":         {"2026-10-30T01:00:00.000Z", principalRevoked, "revoked"},
		} {
			t.Run(effect+" refused under a delegation "+label, func(t *testing.T) {
				ran, port := &principalRan{}, newPrincipalPort()
				port.now, port.answer = row.now, row.answer
				run := openRun(t, principalPathSession(t, []HostTool{principalLocalTool(ran, effect)}, port))
				defer run.Context.Close()
				if _, err := hostTool(t, run, "local_lookup").Execute(context.Background(), json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), row.name) {
					t.Fatalf("got %v", err)
				}
				if len(ran.get()) != 0 {
					t.Fatalf("ran %v", ran.get())
				}
				found := false
				for _, notice := range run.Context.notices {
					if g := notice.Guardrail; g != nil && g.Refusal != nil && g.Refusal.Name == row.name && g.Principal != nil {
						found = true
					}
				}
				if !found {
					t.Fatalf("no refusal recorded: %+v", run.Context.notices)
				}
			})
		}
	}
	t.Run("a question is the package's own interaction, and asks no port", func(t *testing.T) {
		ran, port := &principalRan{}, newPrincipalPort()
		port.answer = principalRevoked
		tool := principalLocalTool(ran, "pure")
		tool.Name, tool.Interaction, tool.Taxonomy = "ask_person", "question", &wire.ToolTaxonomy{Operation: "ask", Effect: "pure"}
		run := openRun(t, principalPathSession(t, []HostTool{tool}, port))
		defer run.Context.Close()
		if _, err := hostTool(t, run, "ask_person").Execute(context.Background(), json.RawMessage(`{}`)); err != nil {
			t.Fatal(err)
		}
		if len(port.reads) != 0 {
			t.Fatalf("reads %+v", port.reads)
		}
	})
	t.Run("a run with no delegation runs it as before", func(t *testing.T) {
		ran := &principalRan{}
		session, _ := trackedSession(t)
		session.HostTools = []HostTool{principalLocalTool(ran, "read")}
		run := openRun(t, session)
		defer run.Context.Close()
		if _, err := hostTool(t, run, "local_lookup").Execute(context.Background(), json.RawMessage(`{}`)); err != nil || len(ran.get()) != 1 {
			t.Fatalf("%v, ran %v", err, ran.get())
		}
	})
}

func TestPrincipalGatewayMappingIsHeldAtDeclaration(t *testing.T) {
	mapped := func(kind string, scope *GatewayScope, receipt *GatewayReceiptTrust) string {
		return declarationProblem(HostTool{Name: "read_note", Source: &ToolSource{Kind: kind, ID: "notes", Trust: "first-party", Version: "1", ToolListDigest: "sha256:" + strings.Repeat("a", 64), GatewayScope: scope, GatewayReceipt: receipt}})
	}
	good := &GatewayScope{Authority: "gateway:test", Source: "tickets/live"}
	for name, row := range map[string]struct {
		kind    string
		scope   *GatewayScope
		receipt *GatewayReceiptTrust
		problem string
	}{
		"a custom source":         {"custom", good, nil, "is for a source a gateway serves"},
		"an MCP source":           {"mcp", good, nil, "is for a source a gateway serves"},
		"no source name":          {"gateway", &GatewayScope{Authority: "gateway:test"}, nil, "not an authority label"},
		"an authority with space": {"gateway", &GatewayScope{Authority: "not a label", Source: "x"}, nil, "not an authority label"},
		"another authority":       {"gateway", good, &GatewayReceiptTrust{Authority: "gateway:other"}, "another authority"},
		"the pinned authority":    {"gateway", good, &GatewayReceiptTrust{Authority: "gateway:test"}, ""},
		"no pin":                  {"gateway", good, nil, ""},
	} {
		t.Run(name, func(t *testing.T) {
			got := mapped(row.kind, row.scope, row.receipt)
			if row.problem == "" && strings.Contains(got, "gatewayScope") || row.problem != "" && !strings.Contains(got, row.problem) {
				t.Fatalf("got %q", got)
			}
		})
	}
}

func TestPrincipalReadServedWithoutADispatchCountsNothing(t *testing.T) {
	immutable := func(ran *principalRan) []HostTool {
		tool := HostTool{Name: "read_note", Description: "read_note", InputSchema: json.RawMessage(`{"type":"object"}`), Delegation: "read", ContextPolicy: &ToolContextPolicy{Immutable: true}, Taxonomy: &wire.ToolTaxonomy{Operation: "read", Effect: "read"}, Source: &ToolSource{Kind: "custom", ID: "notes", Trust: "first-party"},
			Execute: func(context.Context, json.RawMessage) (ToolResult, error) {
				ran.add("read_note")
				return ResultOf([]Content{TextContent("note")}, nil), nil
			}}
		return pinLocalFixture([]HostTool{tool})
	}
	t.Run("one dispatch, counted once, the final read before it", func(t *testing.T) {
		ran, port := &principalRan{}, newPrincipalPort()
		run := openRun(t, principalPathSession(t, immutable(ran), port))
		defer run.Context.Close()
		for range 3 {
			if _, err := hostTool(t, run, "notes__read_note").Execute(context.Background(), json.RawMessage(`{}`)); err != nil {
				t.Fatal(err)
			}
		}
		if !sameStrings(ran.get(), []string{"read_note"}) || !sameBools(port.counts(), []bool{false, true, false, false}) {
			t.Fatalf("ran %v counts %v", ran.get(), port.counts())
		}
	})
	t.Run("a cached read is refused after a revocation, though nothing would dispatch", func(t *testing.T) {
		ran, port := &principalRan{}, newPrincipalPort()
		run := openRun(t, principalPathSession(t, immutable(ran), port))
		defer run.Context.Close()
		if _, err := hostTool(t, run, "notes__read_note").Execute(context.Background(), json.RawMessage(`{}`)); err != nil {
			t.Fatal(err)
		}
		port.set(principalRevoked)
		if _, err := hostTool(t, run, "notes__read_note").Execute(context.Background(), json.RawMessage(`{}`)); err == nil {
			t.Fatal("served after the revocation")
		}
		if len(ran.get()) != 1 {
			t.Fatalf("ran %v", ran.get())
		}
	})
	t.Run("a counted read that does not admit it dispatches nothing", func(t *testing.T) {
		ran, port := &principalRan{}, newPrincipalPort()
		port.answerCounted = `{"admission":"refuse","refusal":"out-of-scope","reason":"limit"}`
		run := openRun(t, principalPathSession(t, immutable(ran), port))
		defer run.Context.Close()
		if _, err := hostTool(t, run, "notes__read_note").Execute(context.Background(), json.RawMessage(`{}`)); err == nil {
			t.Fatal("dispatched past its limit")
		}
		if len(ran.get()) != 0 || !sameBools(port.counts(), []bool{false, true}) {
			t.Fatalf("ran %v counts %v", ran.get(), port.counts())
		}
	})
}

func TestPrincipalApprovalPendingResultCarriesThePrincipal(t *testing.T) {
	ran, port := &principalRan{}, newPrincipalPort()
	tools := pinLocalFixture(func() []HostTool {
		out := []HostTool{}
		for _, tool := range principalTools(ran, map[string]ApprovalPolicy{"write_note": func(ApprovalRequest) string { return "ask" }}, nil) {
			if tool.Name == "read_note" || tool.Name == "write_note" {
				out = append(out, tool)
			}
		}
		return out
	}())
	session := principalPathSession(t, tools, port)
	input := *session.Context
	input.RunStore = &DurableRunInput{Store: NewMemoryRunStore(MemoryRunStoreOptions{}), ID: "pending"}
	session.Context = &input
	run := openRun(t, session)
	defer run.Context.Close()
	boundary := NewBoundary(run)
	defer boundary.Close()
	boundary.Open()
	gate := run.Context.Sources
	if answer, _, _ := gate.Decide(context.Background(), "notes__write_note", json.RawMessage(`{"text":"a"}`), "call-a", true); answer != "ask" {
		t.Fatalf("answer %s", answer)
	}
	if answer, _, _ := gate.Decide(context.Background(), "notes__write_note", json.RawMessage(`{"text":"b"}`), "call-b", true); answer != "pending" {
		t.Fatalf("answer %s", answer)
	}
	id := "call-b"
	events, _ := boundary.Event(contract.RunEvent{ToolCall: &contract.ToolCall{Type: "tool_call", CallID: &id, Name: "notes__write_note", Args: json.RawMessage(`{"text":"b"}`)}})
	found := false
	for _, event := range events {
		if event.ToolResult != nil {
			found = true
			if event.ToolResult.Principal == nil || !event.ToolResult.IsError {
				t.Fatalf("result %+v", event.ToolResult)
			}
		}
		if event.ToolCall != nil && event.ToolCall.Principal == nil {
			t.Fatalf("call %+v", event.ToolCall)
		}
	}
	if !found {
		t.Fatalf("no result: %+v", events)
	}
}

func TestPrincipalChildNeedsItsRunsOpenedPrincipal(t *testing.T) {
	v := loadPrincipalChains(t)
	given := principalInput(v, v.Items[0], principalAdmitAll, nil)
	opened, err := OpenPrincipal(given)
	if err != nil {
		t.Fatal(err)
	}
	saved := &contract.Checkpoint{Principal: opened.Record.Contract()}
	for name, input := range map[string]*Input{"given one": {Principal: given}, "restored from one": {Execution: saved}} {
		if !HoldsDelegation(input) {
			t.Fatalf("%s: not held", name)
		}
		if err := RequireOpenedPrincipal(nil, input); err == nil || !strings.Contains(err.Error(), "delegation is not open") {
			t.Fatalf("%s: %v", name, err)
		}
		if err := RequireOpenedPrincipal(opened, input); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	for _, input := range []*Input{nil, {}} {
		if HoldsDelegation(input) || RequireOpenedPrincipal(nil, input) != nil {
			t.Fatalf("%+v held", input)
		}
	}
}
