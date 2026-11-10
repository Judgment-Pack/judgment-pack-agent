package delegate_test

// #343: the continuation a durable handoff gives its parent is the one its record holds, in the form the
// record guard (ADR-0018) leaves of it, as a resumed run would be given. The child's report, C:\Users<x,
// matches no pattern; the answer's JSON writes its "<" as \u003c, and in the continuation's frame that
// escape's backslash completes a Windows home path, which the record guard redacts. Given the unrecorded
// form, the parent's next request held what the record could not, and the run stopped there.
// recordedResults.test.ts holds the TypeScript.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/agent"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/delegate"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/turn"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
)

// answeredStorage keeps the continuation each answered handoff was stored with.
type answeredStorage struct {
	runcontext.RunStore
	continuations *[]string
}

func (s answeredStorage) Commit(ctx context.Context, in runcontext.RunCommit) (runcontext.RunReceipt, error) {
	var cp contract.Checkpoint
	if err := json.Unmarshal(in.Checkpoint, &cp); err != nil {
		return runcontext.RunReceipt{}, err
	}
	if cp.Handoff != nil && cp.Handoff.State == "answered" && cp.Handoff.Continuation != nil {
		*s.continuations = append(*s.continuations, *cp.Handoff.Continuation)
	}
	return s.RunStore.Commit(ctx, in)
}

func TestAHandoffsParentIsGivenTheContinuationItsRecordHolds(t *testing.T) {
	ctx := context.Background()
	var stored []string
	store := answeredStorage{runcontext.NewMemoryRunStore(runcontext.MemoryRunStoreOptions{}), &stored}
	g := gate.New(gate.Options{})
	g.SetOffer(contract.ToolOffer{Tools: []contract.ToolOfferRuntimeTool{}, HostTools: []contract.ToolOfferHostTool{turn.DelegationTool(turn.TaskProfiles)}})
	calls := 0
	continued := ""
	var events []contract.RunEvent
	run := &adapter.Run{Gate: g.Calls(), ModelName: "fixture", Prompt: "Review", Critic: "off", AllowConversation: true, Interactive: true,
		Context: &runcontext.Input{CurrentRequest: &runcontext.CurrentRequest{Message: "Review", Input: "Review"}, RunStore: &runcontext.DurableRunInput{Store: store, ID: "recorded-continuation"}},
		Emit:    func(e contract.RunEvent) { events = append(events, e) },
		Children: func(context.Context, contract.DelegatedTaskProfile, func(contract.RunEvent)) (*gate.Calls, []contract.ToolOfferRuntimeTool, error) {
			return nil, nil, nil
		},
		Agent: &adapter.Agent{Model: "fixture", Run: func(ctx context.Context, request adapter.AgentRequest, cb adapter.AgentCallbacks) error {
			defer func() {
				if cb.ProviderRelease != nil {
					cb.ProviderRelease.Acknowledge(cb.ProviderRelease.Invocation)
				}
			}()
			calls++
			switch calls {
			case 1:
				for _, tool := range request.Tools {
					if strings.HasPrefix(tool.Description, "delegate_tasks:") {
						_, _ = cb.Tool(ctx, adapter.AgentToolCall{ID: "delegate-wire", Name: tool.Name, Arguments: json.RawMessage(`{"tasks":[{"title":"Review","profile":"review","objective":"Check the supplied claims."}]}`)})
						return nil
					}
				}
				t.Fatal("no delegate tool")
			case 2:
				return cb.Event(ctx, adapter.AgentEvent{Type: "message", ID: "reply", Text: "C:\\Users<x", Phase: "final"})
			}
			continued = request.Prompt
			return cb.Event(ctx, adapter.AgentEvent{Type: "message", ID: "reply", Text: "Claims agree.", Phase: "final"})
		}}}
	if err := delegate.With(agent.New(), delegate.Options{}).Run(ctx, run); err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Error != nil {
			t.Fatalf("error: %s", event.Error.Message)
		}
	}
	if calls != 3 || len(stored) == 0 {
		t.Fatalf("invocations=%d, answered handoffs stored=%d", calls, len(stored))
	}
	recorded := stored[len(stored)-1]
	if !strings.Contains(recorded, "[redacted: local-home-path]") || strings.Contains(recorded, `Users\u003c`) {
		t.Fatalf("the record holds %q", recorded)
	}
	if !strings.Contains(continued, recorded) || strings.Contains(continued, `Users\u003c`) {
		t.Fatalf("the parent was given %q, the record holds %q", continued, recorded)
	}
	head, err := store.Load(ctx, "recorded-continuation")
	if err != nil || head == nil {
		t.Fatal("no stored run", err)
	}
	var saved contract.Checkpoint
	if err = json.Unmarshal(head.Checkpoint, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Status != "complete" || saved.Handoff == nil || saved.Handoff.Continuation == nil || *saved.Handoff.Continuation != recorded {
		t.Fatalf("final checkpoint: %s", head.Checkpoint)
	}
}
