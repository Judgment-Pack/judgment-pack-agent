package adapter_test

// ADR-0029: an acting Runtime call is admitted as any act is, on both Go engines: refused outside the
// scope, past expiry and after revocation, before it is asked and again before it runs, counted only
// immediately before the physical call. The twin of principalPaths.test.ts.

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"testing"
	"time"

	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
)

type runtimePrincipalPort struct {
	mu    sync.Mutex
	reads []runcontext.PrincipalAdmissionRead
}

func (p *runtimePrincipalPort) counts() []bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := []bool{}
	for _, read := range p.reads {
		out = append(out, read.Call.Count)
	}
	return out
}

func runtimePrincipal(t *testing.T, chain int, now string, port *runtimePrincipalPort, answer func(runcontext.PrincipalAdmissionRead) string) *runcontext.RunPrincipal {
	t.Helper()
	raw, err := os.ReadFile("../../contract/vectors/principal/chains.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Keys     map[string][]string `json:"keys"`
		Software struct {
			Package string `json:"package"`
			Version string `json:"version"`
		} `json:"software"`
		Audience string            `json:"audience"`
		Items    []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return &runcontext.RunPrincipal{Chain: v.Items[chain], Keys: v.Keys, Software: runcontext.PrincipalSoftware{Package: v.Software.Package, Version: v.Software.Version}, Audience: v.Audience,
		Admission: func(_ context.Context, read runcontext.PrincipalAdmissionRead) ([]byte, error) {
			port.mu.Lock()
			defer port.mu.Unlock()
			port.reads = append(port.reads, read)
			return []byte(answer(read)), nil
		},
		Clock: func() (time.Time, error) { return time.Parse(time.RFC3339Nano, now) }}
}

const (
	runtimeNow     = "2026-10-30T01:00:00.000Z"
	runtimeExpired = "2026-10-30T08:00:00.000Z"
	runtimeScoped  = 5 // chains.json: the root that names source runtime
	runtimeAdmit   = `{"admission":"admit"}`
	runtimeRevoked = `{"admission":"refuse","refusal":"revoked"}`
)

func runtimeRefusals(events []contract.RunEvent) []string {
	out := []string{}
	for _, event := range events {
		if g := event.Guardrail; g != nil && g.Refusal != nil && g.Reason != nil && *g.Reason == "delegation refused" && g.Principal != nil {
			out = append(out, g.Refusal.Name)
		}
	}
	return out
}

func TestRuntimeActionUnderADelegation(t *testing.T) {
	admit := func(runcontext.PrincipalAdmissionRead) string { return runtimeAdmit }
	revoked := func(runcontext.PrincipalAdmissionRead) string { return runtimeRevoked }
	for _, engine := range []string{"eino", "native"} {
		for label, row := range map[string]struct {
			now    string
			answer func(runcontext.PrincipalAdmissionRead) string
			name   string
		}{"past its expiry": {runtimeExpired, admit, "expired"}, "revoked": {runtimeNow, revoked, "revoked"}} {
			t.Run(engine+"/allowed by the host, refused under a delegation "+label, func(t *testing.T) {
				f := newRuntimeActionFixture(t)
				f.approve = func(gate.RuntimeApprovalRequest) string { return "allow" }
				port := &runtimePrincipalPort{}
				f.principal = runtimePrincipal(t, runtimeScoped, row.now, port, row.answer)
				events, err := f.run(t, runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{}), engine, false, nil, true)
				if err != nil {
					t.Fatal(err)
				}
				if len(f.calls) != 0 {
					t.Fatalf("%d physical calls", len(f.calls))
				}
				if got := runtimeRefusals(events); len(got) != 1 || got[0] != row.name {
					t.Fatalf("refusals %v", got)
				}
			})
		}
		t.Run(engine+"/outside the scope it is refused, and not put to the person", func(t *testing.T) {
			f := newRuntimeActionFixture(t)
			port := &runtimePrincipalPort{}
			f.principal = runtimePrincipal(t, 0, runtimeNow, port, admit)
			events, err := f.run(t, runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{}), engine, false, nil, true)
			if err != nil {
				t.Fatal(err)
			}
			for _, event := range events {
				if event.ApprovalRequested != nil {
					t.Fatal("asked")
				}
			}
			if got := runtimeRefusals(events); len(f.calls) != 0 || len(got) != 1 || got[0] != "out-of-scope" {
				t.Fatalf("%d calls, refusals %v", len(f.calls), got)
			}
			if len(port.reads) != 1 || port.reads[0].Call != (runcontext.PrincipalAdmissionCall{Source: "runtime", Tool: "experimental_evaluate", Effect: "act", Count: false}) {
				t.Fatalf("reads %+v", port.reads)
			}
		})
		t.Run(engine+"/under a chain that names it, it runs once, counted once before the physical call", func(t *testing.T) {
			f := newRuntimeActionFixture(t)
			f.approve = func(gate.RuntimeApprovalRequest) string { return "allow" }
			port := &runtimePrincipalPort{}
			f.principal = runtimePrincipal(t, runtimeScoped, runtimeNow, port, admit)
			events, err := f.run(t, runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{}), engine, false, nil, true)
			if err != nil {
				t.Fatal(err)
			}
			got := port.counts()
			if len(f.calls) != 1 || len(runtimeRefusals(events)) != 0 || len(got) != 2 || got[0] || !got[1] {
				t.Fatalf("%d calls, counts %v, refusals %v", len(f.calls), got, runtimeRefusals(events))
			}
		})
		for label, answer := range map[string]func(runcontext.PrincipalAdmissionRead) string{
			"revoked before the continuation": revoked,
			"revoked as the continuation dispatches": func(read runcontext.PrincipalAdmissionRead) string {
				if read.Call.Count {
					return runtimeRevoked
				}
				return runtimeAdmit
			},
		} {
			t.Run(engine+"/carries no approval past the delegation: "+label, func(t *testing.T) {
				f := newRuntimeActionFixture(t)
				store := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
				port := &runtimePrincipalPort{}
				f.principal = runtimePrincipal(t, runtimeScoped, runtimeNow, port, admit)
				first, err := f.run(t, store, engine, false, nil, true)
				if err != nil {
					t.Fatal(err)
				}
				asked := runtimeActionAsked(t, first)
				f.principal = runtimePrincipal(t, runtimeScoped, runtimeNow, port, answer)
				events, err := f.run(t, store, engine, true, &contract.CheckpointApprovalDecision{CallID: asked.CallID, Approved: true}, true)
				if err != nil {
					t.Fatal(err)
				}
				if len(f.calls) != 0 {
					t.Fatalf("%d physical calls", len(f.calls))
				}
				if got := runtimeRefusals(events); len(got) != 1 || got[0] != "revoked" {
					raw, _ := json.Marshal(events)
					t.Fatalf("refusals %v in %.1500s", got, raw)
				}
				// Revoked before the continuation, the check before it prepares refuses it: nothing is counted.
				if counts := port.counts(); label == "revoked before the continuation" && counts[len(counts)-1] {
					t.Fatalf("counts %v", counts)
				}
			})
		}
	}
}
