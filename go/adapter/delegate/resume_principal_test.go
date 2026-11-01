package delegate_test

// ADR-0029: a delegated run stopped after its handoff is written, and resumed: its parent is opened first, so its
// resume check runs, and no child starts outside the delegation. The twin of principalPaths.test.ts.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
)

type resumeChains struct {
	Keys     map[string][]string `json:"keys"`
	Software struct {
		Package string `json:"package"`
		Version string `json:"version"`
	} `json:"software"`
	Audience string            `json:"audience"`
	Items    []json.RawMessage `json:"items"`
}

func readResumeChains(t *testing.T) resumeChains {
	t.Helper()
	raw, err := os.ReadFile("../../../contract/vectors/principal/chains.json")
	if err != nil {
		t.Fatal(err)
	}
	var v resumeChains
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// resumeFault loses the acknowledgement of the commit that writes the handoff at state, so the run stops
// with it written.
type resumeFault struct {
	runcontext.RunStore
	state string
	armed *bool
}

func (s resumeFault) Commit(ctx context.Context, in runcontext.RunCommit) (runcontext.RunReceipt, error) {
	receipt, err := s.RunStore.Commit(ctx, in)
	if err == nil && *s.armed {
		var cp struct {
			Handoff *struct {
				State string `json:"state"`
			} `json:"handoff"`
		}
		if json.Unmarshal(in.Checkpoint, &cp) == nil && cp.Handoff != nil && cp.Handoff.State == s.state {
			*s.armed = false
			return receipt, errors.New("lost acknowledgement")
		}
	}
	return receipt, err
}

type resumeFixture struct {
	mu         sync.Mutex
	dispatched int
	childRuns  int
	reads      []runcontext.PrincipalAdmissionRead
	host       runcontext.HostTool
	chains     resumeChains
}

func newResumeFixture(t *testing.T) *resumeFixture {
	t.Helper()
	f := &resumeFixture{chains: readResumeChains(t)}
	host := runcontext.HostTool{Name: "read_note", Description: "read_note", InputSchema: json.RawMessage(`{"type":"object"}`), Delegation: "read", EffectClass: "read", Taxonomy: &runcontext.ToolTaxonomy{Operation: "read", Effect: "read"},
		Execute: func(context.Context, json.RawMessage) (runcontext.ToolResult, error) {
			f.mu.Lock()
			f.dispatched++
			f.mu.Unlock()
			return runcontext.ResultOf([]runcontext.Content{runcontext.TextContent("note")}, nil), nil
		}}
	digest := runcontext.ToolListDigest([]runcontext.ListedTool{{Name: host.Name, Description: host.Description, InputSchema: host.InputSchema, Taxonomy: host.Taxonomy}})
	host.Source = &runcontext.ToolSource{Kind: "custom", ID: "notes", Trust: "person", Version: "1", ToolListDigest: digest, CurrentDigest: func(context.Context) (string, error) { return digest, nil }}
	f.host = host
	return f
}

func (f *resumeFixture) principal(chain int, answer string) *runcontext.RunPrincipal {
	v := f.chains
	return &runcontext.RunPrincipal{Chain: v.Items[chain], Keys: v.Keys, Software: runcontext.PrincipalSoftware{Package: v.Software.Package, Version: v.Software.Version}, Audience: v.Audience,
		Admission: func(_ context.Context, read runcontext.PrincipalAdmissionRead) ([]byte, error) {
			f.mu.Lock()
			f.reads = append(f.reads, read)
			f.mu.Unlock()
			return []byte(answer), nil
		},
		Clock: func() (time.Time, error) { return time.Parse(time.RFC3339Nano, "2026-10-30T01:00:00.000Z") }}
}

func (f *resumeFixture) children(_ context.Context, _ contract.DelegatedTaskProfile, emit func(contract.RunEvent)) (*gate.Calls, []contract.ToolOfferRuntimeTool, error) {
	child := gate.New(gate.Options{Granted: gate.Ceiling, Emit: emit})
	declared := f.host.Declaration()
	declared.Name = runcontext.WireName(f.host.Source.ID, f.host.Name)
	child.SetOffer(contract.ToolOffer{Tools: []contract.ToolOfferRuntimeTool{}, HostTools: []contract.ToolOfferHostTool{declared}})
	return child.Calls(), nil, nil
}

func (f *resumeFixture) stopped(t *testing.T, memory *runcontext.MemoryRunStore, state string, taskSteps bool, given *runcontext.RunPrincipal) {
	t.Helper()
	armed := true
	first := lifecycleRun(resumeFault{memory, state, &armed}, "handoff", false, func(ctx context.Context, request adapter.AgentRequest, cb adapter.AgentCallbacks) error {
		defer func() {
			if cb.ProviderRelease != nil {
				cb.ProviderRelease.Acknowledge(cb.ProviderRelease.Invocation)
			}
		}()
		return lifecycleDelegate(ctx, request, cb)
	}, func(contract.RunEvent) {})
	first.HostTools = []runcontext.HostTool{f.host}
	first.Children = f.children
	first.Context.TaskSteps = taskSteps
	first.Context.Principal = given
	lifecycleExecute(t, context.Background(), first)
	head, err := memory.Load(context.Background(), "handoff")
	if err != nil || head == nil {
		t.Fatal(err)
	}
	var saved contract.Checkpoint
	if err := json.Unmarshal(head.Checkpoint, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Handoff == nil || saved.Handoff.State != state || saved.Principal == nil {
		t.Fatalf("the run did not stop at %s: %+v", state, saved.Handoff)
	}
}

func (f *resumeFixture) resumed(t *testing.T, memory *runcontext.MemoryRunStore, taskSteps bool, given *runcontext.RunPrincipal) []contract.RunEvent {
	t.Helper()
	var events []contract.RunEvent
	resumed := lifecycleRun(memory, "handoff", true, func(ctx context.Context, request adapter.AgentRequest, cb adapter.AgentCallbacks) error {
		defer func() {
			if cb.ProviderRelease != nil {
				cb.ProviderRelease.Acknowledge(cb.ProviderRelease.Invocation)
			}
		}()
		parent := false
		for _, tool := range request.Tools {
			parent = parent || strings.HasPrefix(tool.Description, "delegate_tasks:")
		}
		if !parent {
			f.mu.Lock()
			f.childRuns++
			f.mu.Unlock()
			for _, tool := range request.Tools {
				if strings.HasPrefix(tool.Description, "notes__read_note:") {
					if _, err := cb.Tool(ctx, adapter.AgentToolCall{ID: "read-wire", Name: tool.Name, Arguments: json.RawMessage(`{}`)}); err != nil {
						return err
					}
				}
			}
		}
		return cb.Event(ctx, adapter.AgentEvent{Type: "message", ID: "reply", Text: "Findings.", Phase: "final"})
	}, func(event contract.RunEvent) {
		f.mu.Lock()
		events = append(events, event)
		f.mu.Unlock()
	})
	resumed.HostTools = []runcontext.HostTool{f.host}
	resumed.Children = f.children
	resumed.Context.TaskSteps = taskSteps
	resumed.Context.Principal = given
	lifecycleExecute(t, context.Background(), resumed)
	return events
}

func TestDelegatedRunResumedAtItsHandoff(t *testing.T) {
	const admit, revoked = `{"admission":"admit"}`, `{"admission":"refuse","refusal":"revoked"}`
	for _, state := range []string{"released", "children"} {
		for _, taskSteps := range []bool{false, true} {
			name := state
			if taskSteps {
				name += " with task steps"
			}
			t.Run(name+"/revoked: nothing is dispatched, and the port is read", func(t *testing.T) {
				f := newResumeFixture(t)
				memory := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
				f.stopped(t, memory, state, taskSteps, f.principal(0, admit))
				before := len(f.reads)
				f.resumed(t, memory, taskSteps, f.principal(0, revoked))
				if f.dispatched != 0 {
					t.Fatalf("dispatched %d", f.dispatched)
				}
				if len(f.reads) == before {
					t.Fatal("no admission read after the resume")
				}
			})
			for label, chain := range map[string]int{"another subject": 4, "no delegation at all": -1} {
				t.Run(name+"/"+label+": no child starts, and principal-changed is named", func(t *testing.T) {
					f := newResumeFixture(t)
					memory := runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{})
					f.stopped(t, memory, state, taskSteps, f.principal(0, admit))
					var given *runcontext.RunPrincipal
					if chain >= 0 {
						given = f.principal(chain, admit)
					}
					events := f.resumed(t, memory, taskSteps, given)
					if f.dispatched != 0 || f.childRuns != 0 {
						t.Fatalf("dispatched %d, child runs %d", f.dispatched, f.childRuns)
					}
					named := false
					for _, event := range events {
						named = named || event.Error != nil && strings.Contains(event.Error.Message, "principal-changed")
					}
					if !named {
						raw, _ := json.Marshal(events)
						t.Fatalf("not named: %.1200s", raw)
					}
				})
			}
		}
	}
}
