package delegate_test

// ADR-0029, through the real coordinator: a delegated child runs under the link the issuing host narrowed
// from its parent's, and a child it cannot have is refused by name with nothing of it started. The twin of
// packages/harness/principalChild.test.ts.

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

type principalChains struct {
	Keys     map[string][]string `json:"keys"`
	Software struct {
		Package string `json:"package"`
		Version string `json:"version"`
	} `json:"software"`
	Audience string `json:"audience"`
	Digests  struct {
		Root  string `json:"root"`
		Child string `json:"child"`
	} `json:"digests"`
	Items   []json.RawMessage `json:"items"`
	Refused []struct {
		Chain json.RawMessage `json:"chain"`
	} `json:"refused"`
}

func readPrincipalChains(t *testing.T) principalChains {
	t.Helper()
	raw, err := os.ReadFile("../../../contract/vectors/principal/chains.json")
	if err != nil {
		t.Fatal(err)
	}
	var v principalChains
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func (v principalChains) input(chain json.RawMessage, port runcontext.PrincipalChildPort) *runcontext.RunPrincipal {
	return &runcontext.RunPrincipal{Chain: chain, Keys: v.Keys, Software: runcontext.PrincipalSoftware{Package: v.Software.Package, Version: v.Software.Version}, Audience: v.Audience,
		Admission: func(context.Context, runcontext.PrincipalAdmissionRead) ([]byte, error) {
			return []byte(`{"admission":"admit"}`), nil
		},
		Clock: func() (time.Time, error) { return time.Parse(time.RFC3339Nano, "2026-10-30T01:00:00.000Z") }, Child: port}
}

// principalDrive runs one parent that delegates one task, and returns what the run published and whether any child reached the model.
func principalDrive(t *testing.T, principal *runcontext.RunPrincipal) (events []contract.RunEvent, childRuns, childCalls int) {
	t.Helper()
	var mu sync.Mutex
	host := runcontext.HostTool{Name: "read_note", Description: "read_note", InputSchema: json.RawMessage(`{"type":"object"}`), Delegation: "read", EffectClass: "read", Taxonomy: &runcontext.ToolTaxonomy{Operation: "read", Effect: "read"},
		Execute: func(context.Context, json.RawMessage) (runcontext.ToolResult, error) {
			mu.Lock()
			childCalls++
			mu.Unlock()
			return runcontext.ResultOf([]runcontext.Content{runcontext.TextContent("note")}, nil), nil
		}}
	digest := runcontext.ToolListDigest([]runcontext.ListedTool{{Name: host.Name, Description: host.Description, InputSchema: host.InputSchema, Taxonomy: host.Taxonomy}})
	host.Source = &runcontext.ToolSource{Kind: "custom", ID: "notes", Trust: "person", Version: "1", ToolListDigest: digest, CurrentDigest: func(context.Context) (string, error) { return digest, nil }}
	parents := 0
	provider := func(ctx context.Context, request adapter.AgentRequest, cb adapter.AgentCallbacks) error {
		// A child is offered no delegation tool; its parent always is.
		parent := false
		for _, tool := range request.Tools {
			parent = parent || strings.HasPrefix(tool.Description, "delegate_tasks:")
		}
		if !parent {
			mu.Lock()
			childRuns++
			mu.Unlock()
			for _, tool := range request.Tools {
				if strings.HasPrefix(tool.Description, "notes__read_note:") {
					if _, err := cb.Tool(ctx, adapter.AgentToolCall{ID: "read-wire", Name: tool.Name, Arguments: json.RawMessage(`{}`)}); err != nil {
						return err
					}
				}
			}
			return cb.Event(ctx, adapter.AgentEvent{Type: "message", Text: "One finding."})
		}
		parents++
		if parents == 1 {
			return lifecycleDelegate(ctx, request, cb)
		}
		return cb.Event(ctx, adapter.AgentEvent{Type: "message", Text: "done"})
	}
	run := lifecycleRun(runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{}), "delegating", false, provider, func(event contract.RunEvent) {
		mu.Lock()
		events = append(events, event)
		mu.Unlock()
	})
	run.Context.RunStore = nil
	run.Children = func(_ context.Context, _ contract.DelegatedTaskProfile, emit func(contract.RunEvent)) (*gate.Calls, []contract.ToolOfferRuntimeTool, error) {
		child := gate.New(gate.Options{Granted: gate.Ceiling, Emit: emit})
		declared := host.Declaration()
		declared.Name = runcontext.WireName(host.Source.ID, host.Name)
		child.SetOffer(contract.ToolOffer{Tools: []contract.ToolOfferRuntimeTool{}, HostTools: []contract.ToolOfferHostTool{declared}})
		return child.Calls(), nil, nil
	}
	run.HostTools = []runcontext.HostTool{host}
	run.Context.Principal = principal
	lifecycleExecute(t, context.Background(), run)
	return events, childRuns, childCalls
}

func checkPrincipalEvent(t *testing.T, event contract.RunEvent) {
	t.Helper()
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if err := contract.CheckRecord("event", raw); err != nil {
		t.Fatal(err)
	}
}

func lastDelegatedTask(t *testing.T, events []contract.RunEvent) *contract.RunEventDelegatedTask {
	t.Helper()
	for at := len(events) - 1; at >= 0; at-- {
		if events[at].DelegatedTask != nil {
			return events[at].DelegatedTask
		}
	}
	t.Fatal("no delegated_task")
	return nil
}

func delegationRefusal(events []contract.RunEvent) *contract.RunEventGuardrail {
	for _, event := range events {
		if event.Guardrail != nil && event.Guardrail.Reason != nil && *event.Guardrail.Reason == "delegation refused" {
			return event.Guardrail
		}
	}
	return nil
}

func chainWithLink(t *testing.T, root, link json.RawMessage) []byte {
	t.Helper()
	var parent, below []json.RawMessage
	if err := json.Unmarshal(root, &parent); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(link, &below); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal([]json.RawMessage{parent[0], below[1]})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestDelegatedChildRunsUnderItsNarrowedLink(t *testing.T) {
	v := readPrincipalChains(t)
	var requests []runcontext.PrincipalChildRequest
	port := func(_ context.Context, request runcontext.PrincipalChildRequest) ([]byte, error) {
		requests = append(requests, request)
		text, _ := json.Marshal(string(chainWithLink(t, v.Items[0], v.Items[1])))
		return []byte(`{"chain":` + string(text) + `}`), nil
	}
	principal := v.input(v.Items[0], port)
	admitted := principal.Admission
	var chains [][]string
	principal.Admission = func(ctx context.Context, read runcontext.PrincipalAdmissionRead) ([]byte, error) {
		if read.Call.Source == "notes" {
			chains = append(chains, read.Chain)
		}
		return admitted(ctx, read)
	}
	events, childRuns, childCalls := principalDrive(t, principal)
	if childRuns != 1 || childCalls != 1 {
		t.Fatalf("child runs %d, calls %d", childRuns, childCalls)
	}
	// The child's call is admitted under its own link, not its parent's reach.
	if len(chains) == 0 {
		t.Fatal("no admission")
	}
	for _, chain := range chains {
		if len(chain) != 2 || chain[0] != v.Digests.Root || chain[1] != v.Digests.Child {
			t.Fatalf("admitted under %v", chain)
		}
	}
	if len(requests) != 1 || requests[0].Task == "" || requests[0].Profile != "review" || len(requests[0].Tools) != 1 || requests[0].Tools[0] != (runcontext.PrincipalChildTool{Source: "notes", Tool: "read_note"}) {
		t.Fatalf("requests %+v", requests)
	}
	last := lastDelegatedTask(t, events)
	if last.Version == nil || *last.Version != 3 || last.Task.State != "complete" || last.Principal == nil || last.Principal.Actor.Agent != "b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2" || last.Principal.RootActor != "b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1" ||
		len(last.Principal.Chain) != 2 || last.Principal.Chain[0] != v.Digests.Root || last.Principal.Chain[1] != v.Digests.Child {
		t.Fatalf("last %+v", last)
	}
	checkPrincipalEvent(t, contract.RunEvent{DelegatedTask: last})
}

func TestDelegatedChildAWidenedLinkIsRefused(t *testing.T) {
	v := readPrincipalChains(t)
	port := func(context.Context, runcontext.PrincipalChildRequest) ([]byte, error) {
		text, _ := json.Marshal(string(v.Refused[0].Chain))
		return []byte(`{"chain":` + string(text) + `}`), nil
	}
	events, childRuns, childCalls := principalDrive(t, v.input(v.Items[0], port))
	if childRuns != 0 || childCalls != 0 {
		t.Fatalf("child runs %d, calls %d", childRuns, childCalls)
	}
	refusal := delegationRefusal(events)
	if refusal == nil || refusal.Version == nil || *refusal.Version != 3 || refusal.Refusal == nil || refusal.Refusal.Name != "widened" || refusal.Principal == nil || len(refusal.Principal.Chain) != 1 || refusal.Principal.Chain[0] != v.Digests.Root {
		t.Fatalf("refusal %+v", refusal)
	}
	checkPrincipalEvent(t, contract.RunEvent{Guardrail: refusal})
	last := lastDelegatedTask(t, events)
	if last.Task.State != "failed" || last.Task.Problem == nil || !strings.Contains(*last.Task.Problem, "widened") || last.Task.Failure == nil || last.Task.Failure.Code != "permission" || last.Task.Failure.Outcome != "not-started" {
		t.Fatalf("task %+v", last.Task)
	}
}

func TestDelegatedChildNoPortOrALinkThatPermitsNone(t *testing.T) {
	v := readPrincipalChains(t)
	never := func(context.Context, runcontext.PrincipalChildRequest) ([]byte, error) {
		t.Error("asked")
		return nil, errors.New("never asked")
	}
	for name, principal := range map[string]*runcontext.RunPrincipal{"no port": v.input(v.Items[0], nil), "a link that permits none": v.input(v.Items[1], never)} {
		events, childRuns, childCalls := principalDrive(t, principal)
		last := lastDelegatedTask(t, events)
		if childRuns != 0 || childCalls != 0 || last.Task.State != "failed" || last.Task.Problem == nil || !strings.Contains(*last.Task.Problem, "delegation-not-permitted") {
			t.Fatalf("%s: %d %+v", name, childRuns, last.Task)
		}
	}
}

func TestUndelegatedRunDelegatesAsBefore(t *testing.T) {
	events, childRuns, childCalls := principalDrive(t, nil)
	if childRuns != 1 || childCalls != 1 {
		t.Fatalf("child runs %d, calls %d", childRuns, childCalls)
	}
	raw, err := json.Marshal(events)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"principal"`) {
		t.Fatalf("principal on an undelegated run: %s", raw)
	}
	if last := lastDelegatedTask(t, events); last.Version == nil || *last.Version != 1 || last.Principal != nil || last.Task.State != "complete" {
		t.Fatalf("task %+v", last)
	}
}

type countingStore struct {
	runcontext.RunStore
	loads, commits *int
}

func (s countingStore) Load(ctx context.Context, id string) (*runcontext.RunHead, error) {
	*s.loads++
	return s.RunStore.Load(ctx, id)
}

func (s countingStore) Commit(ctx context.Context, in runcontext.RunCommit) (runcontext.RunReceipt, error) {
	*s.commits++
	return s.RunStore.Commit(ctx, in)
}

func TestARefusedChainIsRefusedBeforeTheStoreIsTouched(t *testing.T) {
	v := readPrincipalChains(t)
	loads, commits := 0, 0
	store := countingStore{runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{}), &loads, &commits}
	var events []contract.RunEvent
	run := lifecycleRun(store, "refused", false, func(context.Context, adapter.AgentRequest, adapter.AgentCallbacks) error {
		t.Error("the provider was reached")
		return nil
	}, func(event contract.RunEvent) { events = append(events, event) })
	bad := v.input(v.Items[0], nil)
	bad.Software.Version = "other"
	run.Context.Principal = bad
	lifecycleExecute(t, context.Background(), run)
	errors := []string{}
	for _, event := range events {
		if event.Error != nil {
			errors = append(errors, event.Error.Message)
		}
	}
	if loads != 0 || commits != 0 || len(errors) != 1 || !strings.Contains(errors[0], "software-mismatch") {
		t.Fatalf("loads %d, commits %d, errors %v", loads, commits, errors)
	}
}
