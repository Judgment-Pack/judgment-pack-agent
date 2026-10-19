package certification

import (
	"encoding/json"
	"strings"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
)

// The run events an adapter under test writes around the gated call, and the proposal its final
// answer carries as the scenario writes it.

// ToolCall is a tool_call event: the call as the model wrote it.
func ToolCall(name string, args json.RawMessage) contract.RunEvent {
	return contract.RunEvent{ToolCall: &contract.ToolCall{Type: "tool_call", Name: name, Args: args}}
}

// ToolResult is a tool_result event for a gated call's result.
func ToolResult(name string, result gate.Result) contract.RunEvent { return result.Event(name) }

// Message is a message event.
func Message(text string) contract.RunEvent {
	return contract.RunEvent{Message: &contract.RunEventMessage{Type: "message", Text: text}}
}

// End is the end event.
func End() contract.RunEvent {
	return contract.RunEvent{End: &contract.RunEventEnd{Type: "end"}}
}

// Proposal reads the proposal a final answer carries in its fenced JSON block, as the scenario's
// last step writes it: {"proposal": {"document": …, "unknowns": […]}}. It returns nil where there
// is none.
func Proposal(text string) *contract.RunEventProposal {
	start := strings.Index(text, "```json")
	if start < 0 {
		return nil
	}
	rest := text[start+len("```json"):]
	end := strings.Index(rest, "```")
	if end < 0 {
		return nil
	}
	var block struct {
		Proposal struct {
			Document map[string]json.RawMessage `json:"document"`
			Unknowns []string                   `json:"unknowns"`
		} `json:"proposal"`
		Unknowns []string `json:"unknowns"`
	}
	if err := json.Unmarshal([]byte(rest[:end]), &block); err != nil || block.Proposal.Document == nil {
		return nil
	}
	unknowns := block.Proposal.Unknowns
	if unknowns == nil {
		unknowns = block.Unknowns
	}
	if unknowns == nil {
		unknowns = []string{}
	}
	return &contract.RunEventProposal{Type: "proposal", Document: block.Proposal.Document, Unknowns: unknowns}
}
