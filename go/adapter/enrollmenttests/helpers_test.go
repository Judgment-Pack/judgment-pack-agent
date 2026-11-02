package enrollmenttests

// #298: the enrollment tests are a test binary of their own (docs/ci-evidence.md, #254), since a test binary cannot import
// another's test files. These are copied from package delegate's: chains, the resume fixture, the lifecycle run, and the
// declaration its task-steps tests read.

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
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/turn"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
)

const childAdmit = `{"admission":"admit"}`

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

func reauthGate(tool runcontext.HostTool) *gate.Calls {
	g := gate.New(gate.Options{})
	declared := tool.Declaration()
	declared.Name = runcontext.WireName(tool.Source.ID, tool.Name)
	g.SetOffer(contract.ToolOffer{Tools: []contract.ToolOfferRuntimeTool{}, HostTools: []contract.ToolOfferHostTool{turn.DelegationTool(turn.TaskProfiles), declared}})
	return g.Calls()
}

func lifecycleRun(store runcontext.RunStore, id string, resume bool, provider adapter.AgentRun, emit func(contract.RunEvent)) *adapter.Run {
	g := gate.New(gate.Options{})
	g.SetOffer(contract.ToolOffer{Tools: []contract.ToolOfferRuntimeTool{}, HostTools: []contract.ToolOfferHostTool{turn.DelegationTool(turn.TaskProfiles)}})
	return &adapter.Run{Gate: g.Calls(), ModelName: "fixture", Prompt: "Review", Critic: "off", AllowConversation: true, Interactive: true,
		Context: &runcontext.Input{CurrentRequest: &runcontext.CurrentRequest{Message: "Review", Input: "Review"}, RunStore: &runcontext.DurableRunInput{Store: store, ID: id, Resume: resume}}, Emit: emit,
		Children: func(context.Context, contract.DelegatedTaskProfile, func(contract.RunEvent)) (*gate.Calls, []contract.ToolOfferRuntimeTool, error) {
			return nil, nil, nil
		},
		Agent: &adapter.Agent{Model: "fixture", Run: provider}}
}
func lifecycleDelegate(ctx context.Context, request adapter.AgentRequest, cb adapter.AgentCallbacks) error {
	for _, tool := range request.Tools {
		if strings.HasPrefix(tool.Description, "delegate_tasks:") {
			_, err := cb.Tool(ctx, adapter.AgentToolCall{ID: "delegate-wire", Name: tool.Name, Arguments: json.RawMessage(`{"tasks":[{"title":"Review","profile":"review","objective":"Check claims."}]}`)})
			return err
		}
	}
	return errors.New("fixture did not receive delegation tool")
}
func task183Definition(t *testing.T) runcontext.Subagent {
	t.Helper()
	raw, err := os.ReadFile("../../../contract/vectors/subagents/declaration.json")
	if err != nil {
		t.Fatal(err)
	}
	var file struct{ Items []runcontext.Subagent }
	if err = json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	return file.Items[0]
}
