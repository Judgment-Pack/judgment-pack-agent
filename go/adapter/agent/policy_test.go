package agent_test

import (
	"bytes"
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

func TestOmittedReasoningStaysOmittedAcrossResume(t *testing.T) {
	var firstRequest adapter.AgentRequest
	var firstEvents []contract.RunEvent
	runOnce := func(saved *contract.Checkpoint, sent *adapter.AgentRequest, events *[]contract.RunEvent) {
		emit := func(e contract.RunEvent) { *events = append(*events, e) }
		g := gate.New(gate.Options{Runtime: emptyRuntime, Granted: gate.Ceiling, Emit: emit})
		offer, err := g.Open(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		run := &adapter.Run{ModelName: "connected", Prompt: "Draft a pack.", TestPrompt: "", Critic: "off", Gate: g.Calls(), Tools: offer.Tools, Emit: emit, AllowConversation: true,
			Context: &runcontext.Input{CurrentRequest: &runcontext.CurrentRequest{Message: "Draft a pack.", Input: "Draft a pack."}, Execution: saved},
			Agent: &adapter.Agent{Model: "connected", Run: func(ctx context.Context, request adapter.AgentRequest, callbacks adapter.AgentCallbacks) error {
				*sent = request
				return callbacks.Event(ctx, adapter.AgentEvent{Type: "message", ID: "reply", Text: "Done.", Phase: "final"})
			}}}
		if err = agent.New().Run(context.Background(), run); err != nil {
			t.Fatal(err)
		}
	}
	runOnce(nil, &firstRequest, &firstEvents)
	if firstRequest.Effort != "" {
		t.Fatalf("omitted effort became %q", firstRequest.Effort)
	}
	raw, err := json.Marshal(firstRequest)
	if err != nil || bytes.Contains(raw, []byte(`"effort"`)) {
		t.Fatalf("request carries effort: %s (%v)", raw, err)
	}
	var saved *contract.Checkpoint
	for _, event := range firstEvents {
		if event.PolicyResolved != nil {
			member := event.PolicyResolved.Members["models.author.reasoning"]
			if len(member.Value) != 0 || member.Source == nil || *member.Source != "model-default" {
				t.Fatalf("resolved member: %+v", member)
			}
		}
		if saved == nil && event.ExecutionCheckpoint != nil {
			checkpoint := event.ExecutionCheckpoint.Checkpoint
			saved = &checkpoint
		}
	}
	if saved == nil {
		t.Fatal("no resumable checkpoint")
	}
	var resumedRequest adapter.AgentRequest
	var resumedEvents []contract.RunEvent
	runOnce(saved, &resumedRequest, &resumedEvents)
	firstJSON, _ := json.Marshal(firstRequest)
	resumedJSON, _ := json.Marshal(resumedRequest)
	if !bytes.Equal(firstJSON, resumedJSON) {
		t.Fatalf("resume request changed:\n%s\n%s", firstJSON, resumedJSON)
	}
	for _, event := range resumedEvents {
		if event.PolicyResolved != nil {
			member := event.PolicyResolved.Members["models.author.reasoning"]
			if member.Source == nil || *member.Source != "model-default" {
				t.Fatalf("resume source: %+v", member)
			}
		}
	}
}

func TestDroppedTableDefaultsStayOmittedAcrossAgentResume(t *testing.T) {
	raw, err := os.ReadFile("../../../contract/vectors/providers/reasoning-omission.json")
	if err != nil {
		t.Fatal(err)
	}
	var vector struct {
		Dropped []struct {
			Name, Role, Capability, Raw string
			Supported                   []string
		}
	}
	if err := json.Unmarshal(raw, &vector); err != nil {
		t.Fatal(err)
	}
	for _, row := range vector.Dropped {
		if row.Capability != "agent" {
			continue
		}
		t.Run(row.Name, func(t *testing.T) {
			runOnce := func(saved *contract.Checkpoint) ([]adapter.AgentRequest, []contract.RunEvent) {
				var sent []adapter.AgentRequest
				var events []contract.RunEvent
				emit := func(e contract.RunEvent) { events = append(events, e) }
				g := gate.New(gate.Options{Runtime: emptyRuntime, Granted: gate.Ceiling, Emit: emit})
				offer, openErr := g.Open(context.Background(), nil)
				if openErr != nil {
					t.Fatal(openErr)
				}
				policy := runcontext.PolicyLayer{"models": map[string]any{row.Role: map[string]any{"discovered": map[string]any{"reasoning": map[string]any{"supported": row.Supported}}}}}
				critic, testPrompt := "off", ""
				if row.Role == "critic" {
					critic, testPrompt = "on", "Refute it."
				}
				run := &adapter.Run{ModelName: "connected", Prompt: "Draft a pack.", TestPrompt: testPrompt, Critic: critic, Policy: policy, Gate: g.Calls(), Tools: offer.Tools, Emit: emit, AllowConversation: row.Role != "critic",
					Context: &runcontext.Input{CurrentRequest: &runcontext.CurrentRequest{Message: "Draft a pack.", Input: "Draft a pack."}, Execution: saved},
					Agent: &adapter.Agent{Model: "connected", Effort: row.Raw, Run: func(ctx context.Context, request adapter.AgentRequest, callbacks adapter.AgentCallbacks) error {
						sent = append(sent, request)
						text := "Done."
						if row.Role == "critic" {
							text = "```json\n{\"proposal\":{\"document\":{\"id\":\"pack\"},\"unknowns\":[]}}\n```"
							if request.Phase == "critic" {
								text = "No counterexample found."
							}
						}
						return callbacks.Event(ctx, adapter.AgentEvent{Type: "message", ID: request.Phase + "-reply", Text: text, Phase: "final"})
					}}}
				if runErr := agent.New().Run(context.Background(), run); runErr != nil {
					t.Fatal(runErr)
				}
				return sent, events
			}
			first, events := runOnce(nil)
			var saved *contract.Checkpoint
			for _, event := range events {
				if event.ExecutionCheckpoint != nil && saved == nil {
					checkpoint := event.ExecutionCheckpoint.Checkpoint
					saved = &checkpoint
				}
				if event.CriticDecided != nil && row.Role == "critic" && event.CriticDecided.Reasoning != nil {
					t.Fatalf("critic decision restored %q", *event.CriticDecided.Reasoning)
				}
			}
			if saved == nil {
				t.Fatal("no resumable checkpoint")
			}
			selected := first[0]
			if row.Role == "critic" {
				selected = first[1]
			}
			if selected.Effort != "" {
				t.Fatalf("request restored effort %q", selected.Effort)
			}
			member := saved.Policy["models."+row.Role+".reasoning"]
			if len(member.Value) != 0 || member.Source == nil || *member.Source != "model-default" {
				t.Fatalf("checkpoint member: %+v", member)
			}
			resumed, _ := runOnce(saved)
			firstJSON, _ := json.Marshal(first)
			resumedJSON, _ := json.Marshal(resumed)
			if !bytes.Equal(firstJSON, resumedJSON) {
				t.Fatalf("resume requests changed:\n%s\n%s", firstJSON, resumedJSON)
			}
		})
	}
}

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
