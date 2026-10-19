// Package standin is the certification's stand-in for the host's side of a Go run until the Go
// gate exists (ADR-0004): the runtime's handshake and a gate that keeps the two rules the
// scenario leg measures, with the texts the contract's scenario vector states.
//
//   - A name outside the offered runtime tools is refused before any frame leaves: a guardrail
//     with action "refused", and a failed result the model is shown.
//   - experimental_evaluate without an own rehearsal: true is rewritten to carry one: a guardrail
//     with action "rewrote".
//
// It is not the gate. The gate (packages/harness/toolGate.ts) also holds the ceiling, the
// test-call cap, narrowing and the frame inspection; ADR-0004 ports it to Go. The spikes and the
// runner's own reference test use this package so that what they measure is the framework, not a
// gate written for each.
package standin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/Judgment-Pack/judgment-pack-agent/go/certification"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

// rpc sends one JSON-RPC request and decodes its result.
func rpc(ctx context.Context, runtime certification.RuntimeCall, id int64, method string, params any, into any) error {
	frame, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		return err
	}
	reply, err := runtime(ctx, frame)
	if err != nil {
		return err
	}
	var answer struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(reply, &answer); err != nil {
		return err
	}
	if answer.Error != nil {
		return &RPCError{Code: answer.Error.Code, Message: answer.Error.Message}
	}
	if into == nil {
		return nil
	}
	return json.Unmarshal(answer.Result, into)
}

// RPCError is a JSON-RPC error the runtime answered.
type RPCError struct {
	Code    int
	Message string
}

func (e *RPCError) Error() string { return fmt.Sprintf("runtime error %d: %s", e.Code, e.Message) }

// ListTools runs the handshake and returns the runtime's tools as tools/list served them.
func ListTools(ctx context.Context, runtime certification.RuntimeCall) ([]contract.ToolOfferRuntimeTool, error) {
	if err := rpc(ctx, runtime, 1, "initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "judgment-pack-agent-go-certification", "version": "0"},
	}, nil); err != nil {
		return nil, err
	}
	if _, err := runtime(ctx, json.RawMessage(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)); err != nil {
		return nil, err
	}
	var listed struct {
		Tools []contract.ToolOfferRuntimeTool `json:"tools"`
	}
	if err := rpc(ctx, runtime, 2, "tools/list", map[string]any{}, &listed); err != nil {
		return nil, err
	}
	return listed.Tools, nil
}

// Gate is the stand-in gate over one runtime transport.
type Gate struct {
	Runtime certification.RuntimeCall
	// Offered is the runtime tools the run may call, in the order the refusal names them.
	Offered []string
	// Emit receives the guardrail events, written while the call is in flight.
	Emit   func(contract.RunEvent)
	nextID atomic.Int64
}

// Result is a runtime call's answer as the model is shown it.
type Result struct {
	Text    string
	IsError bool
}

// The texts the contract's scenario vector states (contract/vectors/scenario/expense-reimbursement.json).
const (
	rewroteDetail = "the call carried no rehearsal member that survived serialization; it was rewritten to an own " +
		"rehearsal: true before the frame left the page, so the runtime appends no audit record (runtime ADR-0018, ADR-0028)"
	refusedTail = "; the call did not leave the page and nothing was written"
	refusedNote = ". This assistant proposes; it never writes a file and never calls a tool it was not offered."
)

func guardrail(tool, action, detail string) contract.RunEvent {
	return contract.RunEvent{Guardrail: &contract.RunEventGuardrail{Type: "guardrail", Tool: tool, Action: action, Detail: detail}}
}

// Call puts one call through the gate and, where it passes, to the runtime.
func (g *Gate) Call(ctx context.Context, name string, args json.RawMessage) (Result, error) {
	offered := false
	for _, allowed := range g.Offered {
		if allowed == name {
			offered = true
		}
	}
	if !offered {
		detail := fmt.Sprintf("%s is not one of the tools this assistant may call (%s)%s", name, strings.Join(g.Offered, ", "), refusedTail)
		g.Emit(guardrail(name, "refused", detail))
		return Result{Text: "refused: refused on the wire: " + detail + refusedNote, IsError: true}, nil
	}
	var members map[string]json.RawMessage
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	if err := json.Unmarshal(args, &members); err != nil {
		return Result{Text: "the call's arguments are not an object: " + err.Error(), IsError: true}, nil
	}
	if members == nil {
		members = map[string]json.RawMessage{}
	}
	if name == "experimental_evaluate" && string(members["rehearsal"]) != "true" {
		members["rehearsal"] = json.RawMessage("true")
		g.Emit(guardrail(name, "rewrote", rewroteDetail))
	}
	var answer struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	err := rpc(ctx, g.Runtime, 1000+g.nextID.Add(1), "tools/call", map[string]any{"name": name, "arguments": members}, &answer)
	var refused *RPCError
	if errors.As(err, &refused) {
		return Result{Text: refused.Message, IsError: true}, nil
	}
	if err != nil {
		return Result{}, err
	}
	var texts []string
	for _, part := range answer.Content {
		if part.Type == "text" {
			texts = append(texts, part.Text)
		}
	}
	return Result{Text: strings.Join(texts, "\n"), IsError: answer.IsError}, nil
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

// Events for the adapter's own writing.

// ToolCall is a tool_call event.
func ToolCall(name string, args json.RawMessage) contract.RunEvent {
	return contract.RunEvent{ToolCall: &contract.ToolCall{Type: "tool_call", Name: name, Args: args}}
}

// ToolResult is a tool_result event.
func ToolResult(name string, result Result) contract.RunEvent {
	return contract.RunEvent{ToolResult: &contract.ToolResult{Type: "tool_result", Name: name, IsError: result.IsError, Text: result.Text}}
}

// Message is a message event.
func Message(text string) contract.RunEvent {
	return contract.RunEvent{Message: &contract.RunEventMessage{Type: "message", Text: text}}
}

// End is the end event.
func End() contract.RunEvent {
	return contract.RunEvent{End: &contract.RunEventEnd{Type: "end"}}
}
