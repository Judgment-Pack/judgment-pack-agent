package agent_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/agent"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
)

func TestPolicyLegacyCollisionsRecordAndWireAgree(t *testing.T) {
	raw, err := os.ReadFile("../../../contract/vectors/policy/legacy-collisions.json")
	if err != nil {
		t.Fatal(err)
	}
	var vector struct {
		Items []struct {
			CriticReasoning string
			Policy          runcontext.PolicyLayer
		}
		Expected []struct{ Author, Critic string }
		Cases    []struct{ Effort string }
	}
	if err := json.Unmarshal(raw, &vector); err != nil {
		t.Fatal(err)
	}
	for i, input := range vector.Items {
		t.Run(vector.Expected[i].Author+"/"+vector.Expected[i].Critic, func(t *testing.T) {
			want := vector.Expected[i]
			var events []contract.RunEvent
			var sent []adapter.AgentRequest
			emit := func(e contract.RunEvent) { events = append(events, e) }
			g := gate.New(gate.Options{Runtime: emptyRuntime, Granted: gate.Ceiling, Emit: emit})
			offer, err := g.Open(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			run := &adapter.Run{ModelName: "connected", Prompt: "Draft a pack.", TestPrompt: "Review the pack.", Critic: "on", CriticReasoning: input.CriticReasoning, Policy: input.Policy, Gate: g.Calls(), Tools: offer.Tools, Emit: emit,
				Context: &runcontext.Input{CurrentRequest: &runcontext.CurrentRequest{Message: "Draft a pack.", Input: "Draft a pack."}},
				Agent: &adapter.Agent{Model: "connected", Effort: vector.Cases[i].Effort, Run: func(ctx context.Context, r adapter.AgentRequest, cb adapter.AgentCallbacks) error {
					sent = append(sent, r)
					text := "No counterexample found."
					if r.Phase == "author" {
						text = "```json\n{\"proposal\":{\"document\":{\"id\":\"pack\"},\"unknowns\":[]}}\n```"
					}
					return cb.Event(ctx, adapter.AgentEvent{Type: "message", ID: "reply", Text: text, Phase: "final"})
				}}}
			if err := agent.New().Run(context.Background(), run); err != nil {
				t.Fatal(err)
			}
			if len(sent) != 2 || sent[0].Phase != "author" || sent[0].Effort != want.Author || sent[1].Phase != "critic" || sent[1].Effort != want.Critic {
				t.Fatalf("wire: %+v", sent)
			}
			resolved, decided, saved := false, false, false
			check := func(m map[string]contract.PolicyMember) {
				t.Helper()
				if string(m["models.author.reasoning"].Value) != `"`+want.Author+`"` || string(m["models.critic.reasoning"].Value) != `"`+want.Critic+`"` || m["models.critic.reasoning"].Layer != "run" {
					t.Fatalf("record: %+v", m)
				}
			}
			for _, e := range events {
				if e.Error != nil {
					t.Fatalf("error: %+v", e.Error)
				}
				if e.PolicyResolved != nil {
					resolved = true
					check(e.PolicyResolved.Members)
				}
				if e.ExecutionCheckpoint != nil {
					saved = true
					check(e.ExecutionCheckpoint.Checkpoint.Policy)
				}
				if e.CriticDecided != nil {
					decided = true
					if e.CriticDecided.Reasoning == nil || *e.CriticDecided.Reasoning != want.Critic {
						t.Fatalf("critic: %+v", e.CriticDecided)
					}
				}
			}
			if !resolved || !decided || !saved {
				t.Fatal("missing policy, critic or checkpoint record")
			}
		})
	}
}
