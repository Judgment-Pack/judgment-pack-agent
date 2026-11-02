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
	"sync/atomic"
	"testing"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/turn"
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
	asked      int
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
		Child: func(context.Context, runcontext.PrincipalChildRequest) ([]byte, error) {
			f.mu.Lock()
			f.asked++
			f.mu.Unlock()
			text, _ := json.Marshal(string(v.Items[1]))
			return []byte(`{"chain":` + string(text) + `}`), nil
		},
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
				before, askedBefore := len(f.reads), f.asked
				events := f.resumed(t, memory, taskSteps, f.principal(0, revoked))
				if f.dispatched != 0 || f.childRuns != 0 {
					t.Fatalf("dispatched %d, child runs %d", f.dispatched, f.childRuns)
				}
				if len(f.reads) == before {
					t.Fatal("no admission read after the resume")
				}
				// The child port is not asked of a delegation that no longer stands, and the refusal is
				// recorded and told to the parent.
				if f.asked != askedBefore {
					t.Fatalf("the child port was asked %d times", f.asked-askedBefore)
				}
				named, failed := false, false
				for _, event := range events {
					if g := event.Guardrail; g != nil && g.Refusal != nil && g.Refusal.Name == "revoked" && g.Principal != nil {
						named = true
					}
					if task := event.DelegatedTask; task != nil && task.Task.State == "failed" && task.Task.Problem != nil && strings.Contains(*task.Task.Problem, "revoked") && task.Task.Failure != nil && task.Task.Failure.Code == "permission" {
						failed = true
					}
				}
				if !named || !failed {
					raw, _ := json.Marshal(events)
					t.Fatalf("named %v, failed %v: %.1500s", named, failed, raw)
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

// reauthGate offers the parent its act and its delegation.
func reauthGate(tool runcontext.HostTool) *gate.Calls {
	g := gate.New(gate.Options{})
	declared := tool.Declaration()
	declared.Name = runcontext.WireName(tool.Source.ID, tool.Name)
	g.SetOffer(contract.ToolOffer{Tools: []contract.ToolOfferRuntimeTool{}, HostTools: []contract.ToolOfferHostTool{turn.DelegationTool(turn.TaskProfiles), declared}})
	return g.Calls()
}

// reauthStore stands for a host store kind whose leases and waits outlive its process.
type reauthStore struct{ *runcontext.MemoryRunStore }

func (reauthStore) LeaseLifetime() string { return runcontext.LeaseLifetimeDurable }
func (reauthStore) WaitLifetime() string  { return runcontext.WaitLifetimeDurable }

func TestChildUnderARunParkedForReauthorization(t *testing.T) {
	const admit, revoked, key = `{"admission":"admit"}`, `{"admission":"refuse","refusal":"revoked"}`, "wk.delegate.reauth.Q7z2Xm9LpV4cR8tNb3Yh6Df1G"
	ctx := context.Background()
	credential := "sha256:" + strings.Repeat("7", 64)
	setup := func(t *testing.T) (*resumeFixture, reauthStore, *runcontext.ReferenceEffectHost, runcontext.RunStore, runcontext.LeaseFence, *atomic.Bool, *atomic.Int32, runcontext.HostTool, *atomic.Int64) {
		t.Helper()
		f := newResumeFixture(t)
		at := &atomic.Int64{}
		at.Store(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli())
		store := reauthStore{runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{Now: func() time.Time { return time.UnixMilli(at.Load()).UTC() }})}
		refuse := &atomic.Bool{}
		refuse.Store(true)
		host := runcontext.NewReferenceEffectHostWith(store, runcontext.ReferenceEffectHostOptions{Credentials: func(context.Context, runcontext.EffectOperation) (*runcontext.CredentialRefusal, error) {
			if refuse.Load() {
				return &runcontext.CredentialRefusal{Cause: "expired", Connection: "notes-conn", Principal: credential, Permissions: []string{"notes:write"}, Requires: []string{"notes:write"}}, nil
			}
			return nil, nil
		}})
		opened, err := runcontext.OpenPrincipal(f.principal(0, admit))
		if err != nil {
			t.Fatal(err)
		}
		lease, err := store.AcquireLease(ctx, runcontext.LeaseAcquire{Resource: "handoff", Owner: "worker-a", DurationMs: 3_600_000, Principal: opened.Record.Binding()})
		if err != nil {
			t.Fatal(err)
		}
		view, err := runcontext.UnattendedRunStore(store, runcontext.FenceOf(lease), host)
		if err != nil {
			t.Fatal(err)
		}
		executed := &atomic.Int32{}
		tool := runcontext.HostTool{Name: "write_note", Description: "write_note", InputSchema: json.RawMessage(`{"type":"object"}`), EffectClass: "act", Approval: "required", Taxonomy: &runcontext.ToolTaxonomy{Operation: "act", Effect: "act"},
			Approve: func(runcontext.ApprovalRequest) string { return "allow" },
			Execute: func(context.Context, json.RawMessage) (runcontext.ToolResult, error) {
				executed.Add(1)
				return runcontext.ResultOf([]runcontext.Content{runcontext.TextContent("written")}, nil), nil
			}}
		digest := runcontext.ToolListDigest([]runcontext.ListedTool{{Name: tool.Name, Description: tool.Description, InputSchema: tool.InputSchema, Taxonomy: tool.Taxonomy}})
		tool.Source = &runcontext.ToolSource{Kind: "custom", ID: "notes", Trust: "person", Version: "1", ToolListDigest: digest, CurrentDigest: func(context.Context) (string, error) { return digest, nil }}
		first := lifecycleRun(view, "handoff", false, func(ctx context.Context, request adapter.AgentRequest, cb adapter.AgentCallbacks) error {
			if cb.ProviderRelease != nil {
				defer cb.ProviderRelease.Acknowledge(cb.ProviderRelease.Invocation)
			}
			for _, held := range request.Tools {
				if strings.HasPrefix(held.Description, "notes__write_note:") {
					_, err := cb.Tool(ctx, adapter.AgentToolCall{ID: "write-wire", Name: held.Name, Arguments: json.RawMessage(`{"text":"Mileage"}`)})
					return err
				}
			}
			return errors.New("the act was not offered")
		}, func(contract.RunEvent) {})
		first.HostTools = []runcontext.HostTool{tool}
		first.Gate = reauthGate(tool)
		first.Children = f.children
		first.Context.Principal = f.principal(0, admit)
		lifecycleExecute(t, ctx, first)
		if runcontext.ReauthorizationOf(view) == nil {
			t.Fatal("the effect host's refusal is not on the view")
		}
		if executed.Load() != 0 {
			t.Fatalf("the refused call ran %d times", executed.Load())
		}
		return f, store, host, view, runcontext.FenceOf(lease), refuse, executed, tool, at
	}
	park := func(t *testing.T, store reauthStore, view runcontext.RunStore, fence runcontext.LeaseFence) runcontext.RunWait {
		t.Helper()
		digest, _ := runcontext.WakeKeyDigest(key)
		wait, _, err := runcontext.ParkRun(ctx, runcontext.ParkInput{Store: store, Fence: fence, Wait: "wait-r", Continuation: "continue-r", Condition: runcontext.WaitCondition{Event: &contract.RunWaitEventCondition{Kind: "reauthorized", Subject: "notes-conn"}}, Model: runcontext.PinnedModel{Provider: "openai", Model: "fixture"}, WakeKeyDigest: digest, Release: []runcontext.WaitRelease{}, Reauthorization: runcontext.ReauthorizationOf(view)})
		if err != nil {
			t.Fatal(err)
		}
		return wait
	}
	continued := func(t *testing.T, store reauthStore, host *runcontext.ReferenceEffectHost, wait runcontext.RunWait, refuse *atomic.Bool, at *atomic.Int64) runcontext.RunStore {
		t.Helper()
		at.Add(60_000)
		if _, err := runcontext.WakeRun(ctx, store, runcontext.WakeEvidence{Version: 1, Resource: "handoff", Wait: "wait-r", Evidence: "refreshed-1", Kind: "event", Event: &contract.RunWaitEventCondition{Kind: "reauthorized", Subject: "notes-conn"}, Key: key}); err != nil {
			t.Fatal(err)
		}
		evidence := runcontext.ResumeEvidence{Version: 1, Resource: "handoff", Wait: "wait-r", Model: contract.RunResumeCurrentModel{Provider: "openai", Model: "fixture", Status: "available"}, Policy: contract.RunResumeCurrentPolicy{Digest: wait.Pins.Policy}, Plan: wait.Pins.Plan, Sources: wait.Pins.Sources, Artifacts: wait.Pins.Artifacts, Principal: wait.Pins.Principal}
		action := runcontext.ReauthAction{Version: 1, Resource: "handoff", Wait: "wait-r", Connection: "notes-conn", Action: "refresh", Principal: credential, Permissions: []string{"notes:write"}, Actor: "person-owner", At: "2026-01-01T00:00:30.000Z"}
		resumed, err := runcontext.ResumeParked(ctx, runcontext.ResumeInput{Store: store, Resource: "handoff", Wait: "wait-r", Evidence: "refreshed-1", Owner: "worker-b", DurationMs: 3_600_000, Current: &evidence, Reauthorization: &action})
		if err != nil {
			t.Fatal(err)
		}
		refuse.Store(false)
		view, err := runcontext.ReauthorizedRunStore(ctx, store, host, resumed)
		if err != nil {
			t.Fatal(err)
		}
		return view
	}
	drive := func(t *testing.T, view runcontext.RunStore, f *resumeFixture, tool runcontext.HostTool, answer string) []contract.RunEvent {
		t.Helper()
		var events []contract.RunEvent
		parents := 0
		resumed := lifecycleRun(view, "handoff", true, func(ctx context.Context, request adapter.AgentRequest, cb adapter.AgentCallbacks) error {
			if cb.ProviderRelease != nil {
				defer cb.ProviderRelease.Acknowledge(cb.ProviderRelease.Invocation)
			}
			parent := false
			for _, held := range request.Tools {
				parent = parent || strings.HasPrefix(held.Description, "delegate_tasks:")
			}
			if !parent {
				f.mu.Lock()
				f.childRuns++
				f.mu.Unlock()
				return cb.Event(ctx, adapter.AgentEvent{Type: "message", ID: "child", Text: "One finding.", Phase: "final"})
			}
			parents++
			if parents == 1 {
				return lifecycleDelegate(ctx, request, cb)
			}
			if parents == 2 {
				for _, held := range request.Tools {
					if strings.HasPrefix(held.Description, "notes__write_note:") {
						if _, err := cb.Tool(ctx, adapter.AgentToolCall{ID: "write-wire", Name: held.Name, Arguments: json.RawMessage(`{"text":"Mileage"}`)}); err != nil {
							return err
						}
					}
				}
			}
			return cb.Event(ctx, adapter.AgentEvent{Type: "message", ID: "reply", Text: "Done.", Phase: "final"})
		}, func(event contract.RunEvent) {
			f.mu.Lock()
			events = append(events, event)
			f.mu.Unlock()
		})
		resumed.HostTools = []runcontext.HostTool{tool}
		resumed.Gate = reauthGate(tool)
		resumed.Children = f.children
		resumed.Context.Principal = f.principal(0, answer)
		lifecycleExecute(t, ctx, resumed)
		return events
	}
	t.Run("no child starts while it is parked: the run cannot be leased", func(t *testing.T) {
		f, store, _, view, fence, _, _, _, _ := setup(t)
		wait := park(t, store, view, fence)
		opened, err := runcontext.OpenPrincipal(f.principal(0, admit))
		if err != nil {
			t.Fatal(err)
		}
		_, err = store.AcquireLease(ctx, runcontext.LeaseAcquire{Resource: "handoff", Owner: "worker-x", ExpectedRevision: wait.Checkpoint.Revision, DurationMs: 3_600_000, Principal: opened.Record.Binding()})
		var refused *runcontext.WaitRefused
		if !errors.As(err, &refused) || refused.Reason != "wait-pending" {
			t.Fatalf("got %v", err)
		}
		if f.asked != 0 || f.childRuns != 0 {
			t.Fatalf("asked %d, child runs %d", f.asked, f.childRuns)
		}
	})
	t.Run("after the re-authorization a child runs under its own link, and the re-send is the grant's alone", func(t *testing.T) {
		f, store, host, view, fence, refuse, executed, tool, at := setup(t)
		wait := park(t, store, view, fence)
		before := len(f.reads)
		events := drive(t, continued(t, store, host, wait, refuse, at), f, tool, admit)
		if f.asked != 1 || f.childRuns != 1 || executed.Load() != 1 {
			raw, _ := json.Marshal(events)
			t.Fatalf("asked %d, child runs %d, executed %d: %.1500s", f.asked, f.childRuns, executed.Load(), raw)
		}
		linked := false
		for _, event := range events {
			if task := event.DelegatedTask; task != nil && task.Task.State == "complete" && task.Principal != nil && len(task.Principal.Chain) == 2 {
				linked = true
			}
		}
		if !linked {
			t.Fatal("no child record under its own link")
		}
		counted := []string{}
		for _, read := range f.reads[before:] {
			if read.Call.Count {
				counted = append(counted, read.Call.Tool)
			}
		}
		if len(counted) != 1 || counted[0] != "write_note" {
			t.Fatalf("counted %v", counted)
		}
	})
	t.Run("a delegation revoked meanwhile starts no child, by name, and the re-send is refused with it", func(t *testing.T) {
		f, store, host, view, fence, refuse, executed, tool, at := setup(t)
		wait := park(t, store, view, fence)
		events := drive(t, continued(t, store, host, wait, refuse, at), f, tool, revoked)
		if f.asked != 0 || f.childRuns != 0 || executed.Load() != 0 {
			t.Fatalf("asked %d, child runs %d, executed %d", f.asked, f.childRuns, executed.Load())
		}
		named := false
		for _, event := range events {
			if g := event.Guardrail; g != nil && g.Refusal != nil && g.Refusal.Name == "revoked" {
				named = true
			}
		}
		if !named {
			raw, _ := json.Marshal(events)
			t.Fatalf("not named: %.1200s", raw)
		}
	})
}
