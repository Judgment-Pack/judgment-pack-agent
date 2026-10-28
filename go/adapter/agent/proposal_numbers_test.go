package agent_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/agent"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
)

func TestProposalNumericAgentTerminalRecovery(t *testing.T) {
	fence := func(document string) string { return "```json\n{\"proposal\":{\"document\":" + document + "}}\n```" }
	for _, row := range []struct{ name, source, detail string }{
		{"numeric loss", fence(`{"id":"p","n":9007199254740993}`), "numerical value changes"},
		{"exponent bound", fence(`{"id":"p","n":0e10000000}`), "work bound"},
		{"depth bound", fence(`{"id":"p","n":` + strings.Repeat("[", 128) + "0" + strings.Repeat("]", 128) + "}"), "work bound"},
		{"depth beyond generic parser limit", fence(`{"id":"p","n":` + strings.Repeat("[", 10001) + "0" + strings.Repeat("]", 10001) + "}"), "work bound"},
		{"malformed positive control", "```json\n{\"proposal\":{\"document\":oops}}\n```", ""},
	} {
		t.Run(row.name, func(t *testing.T) {
			calls := 0
			tool := runcontext.HostTool{Name: "read_note", Taxonomy: &runcontext.ToolTaxonomy{Operation: "read", Effect: "read"}, Effect: "read", Description: "Reads.", InputSchema: json.RawMessage(`{"type":"object"}`)}
			run, events := oneTool(t, tool, func(ctx context.Context, _ adapter.AgentRequest, cb adapter.AgentCallbacks) error {
				calls++
				text := row.source
				if calls > 1 {
					text = fence(`{"id":"p","n":2}`)
				}
				return cb.Event(ctx, adapter.AgentEvent{Type: "message", ID: "m", Text: text, Phase: "final"})
			})
			if err := agent.New().Run(context.Background(), run); err != nil {
				t.Fatal(err)
			}
			accepted, recovery, refused := 0, 0, 0
			for _, event := range *events {
				if event.Proposal != nil {
					accepted++
					if string(event.Proposal.Document["n"]) != "2" {
						t.Fatal("unexpected reformatted proposal")
					}
				}
				if event.ResponseRecovery != nil {
					recovery++
				}
				if event.Error != nil {
					refused++
					if event.Error.Failure == nil || event.Error.Failure.Code != "invalid-response" || !strings.Contains(event.Error.Message, row.detail) {
						t.Fatalf("wrong terminal refusal: %+v", event.Error)
					}
				}
			}
			if row.detail == "" {
				if calls != 2 || accepted != 1 || recovery != 1 || refused != 0 {
					t.Fatalf("malformed reformat calls=%d proposals=%d recovery=%d errors=%d", calls, accepted, recovery, refused)
				}
			} else if calls != 1 || accepted != 0 || recovery != 0 || refused != 1 {
				t.Fatalf("terminal source refusal calls=%d proposals=%d recovery=%d errors=%d; want 1/0/0/1", calls, accepted, recovery, refused)
			}
		})
	}
}
