package eino

import (
	"encoding/json"
	"strings"

	"github.com/cloudwego/eino/schema"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/turn"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/wire"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
)

// The turn around the framework's stretches (the deepagents loop's runOnce, which follows
// engines/contract.ts as the vercel adapter does): the text tool path, the request that finishes a
// response cut short, the one reformat request, the critic and the proposal.

// finalInstructions is the state's instruction on the turn's last request.
const finalInstructions = "This is the final request within this turn budget. Tools are unavailable. Answer the current question using results already obtained. If the available information is insufficient, explain exactly what is known and what remains missing. Do not invent results, claim unsaved changes, or turn a diagnostic question into a new proposal."

// turnState is one attempt at the turn.
type turnState struct {
	r *runState

	recording    *turn.Recorder
	toolsClosed  bool
	progressStep string
	turnLimit    int
	toolProposal *turn.Proposal

	system       string
	instructions string
	schemas      *runcontext.Value
	offered      map[string]bool

	messages     []*schema.Message
	lastMessages []*schema.Message
	lastAnswer   *schema.Message
	completion   *contract.ProvenanceCompletion
	lastCalls    int
	final        string
	finalText    string
	synthesis    bool
	reformatting bool
	steps        int
	stepInRun    int
	streamed     error
}

// offeredNames are the names the run offers in its phase: the session's Runtime tools and host
// tools, the boundary's context tools among them. In Plan mode only Plan's reads and planning
// tools, as the gate's PlanPhase holds them (the model is shown what the gate admits).
func (r *runState) offeredTools() ([]runcontext.RuntimeTool, []runcontext.HostTool) {
	runtime := r.session.Tools
	hosts := r.session.HostTools
	if r.run.Context != nil && r.run.Context.Phase == "plan" {
		var keptRuntime []runcontext.RuntimeTool
		for _, tool := range runtime {
			if containsName(planReads(), tool.Name) {
				keptRuntime = append(keptRuntime, tool)
			}
		}
		var keptHosts []runcontext.HostTool
		for _, tool := range hosts {
			if containsName(contextToolNames(), tool.Name) || planningTool(tool) {
				keptHosts = append(keptHosts, tool)
			}
		}
		runtime, hosts = keptRuntime, keptHosts
	}
	return runtime, hosts
}

func containsName(list []string, name string) bool {
	for _, held := range list {
		if held == name {
			return true
		}
	}
	return false
}

// served is the Runtime tool the host's offer served by this name.
func (r *runState) served(name string) (contract.ToolOfferRuntimeTool, bool) {
	for _, tool := range r.run.Tools {
		if tool.Name == name {
			return tool, true
		}
	}
	return contract.ToolOfferRuntimeTool{}, false
}

// mcpTool is a tool as the schema rules read it.
func mcpTool(name, description string, schema json.RawMessage) wire.McpTool {
	tool := wire.McpTool{Name: name, Description: description}
	if len(schema) > 0 && string(schema) != "null" {
		tool.InputSchema, _ = wire.Parse(schema)
	}
	return tool
}

// toolList is the run's tools in the order a request shows them: host tools, then Runtime tools.
func (r *runState) toolList(names map[string]bool) ([]wire.McpTool, error) {
	runtime, hosts := r.offeredTools()
	var out []wire.McpTool
	for _, tool := range hosts {
		if names == nil || names[tool.Name] {
			out = append(out, mcpTool(tool.Name, tool.Description, tool.InputSchema))
		}
	}
	for _, tool := range runtime {
		if names != nil && !names[tool.Name] {
			continue
		}
		served, _ := r.served(tool.Name)
		description := ""
		if served.Description != nil {
			description = *served.Description
		}
		out = append(out, mcpTool(tool.Name, description, served.InputSchema))
	}
	return out, nil
}

// wireTools are the tools as a request shows them: each with the schema the record says the model
// is shown.
func (r *runState) wireTools(tools []wire.McpTool) ([]wire.WireTool, error) {
	out := make([]wire.WireTool, 0, len(tools))
	for _, tool := range tools {
		shown, err := wire.ShownSchema(r.capabilities.ToolSchemas, tool)
		if err != nil {
			return nil, err
		}
		out = append(out, wire.WireTool{Name: tool.Name, Description: tool.Description, Schema: shown})
	}
	return out, nil
}

// described is the tools of a phase as the instructions describe them, for an endpoint with no
// native tool calls.
func (r *runState) described(names map[string]bool) (string, error) {
	tools, err := r.toolList(names)
	if err != nil {
		return "", err
	}
	var held []turn.DescribedTool
	for _, tool := range tools {
		shown, err := wire.ShownSchema(r.capabilities.ToolSchemas, tool)
		if err != nil {
			return "", err
		}
		held = append(held, turn.DescribedTool{Name: tool.Name, Description: tool.Description, Schema: json.RawMessage(shown.JSON())})
	}
	return turn.TextToolInstructions(held), nil
}

// declarations are the names and descriptions Eino's tools node is given for a phase.
func declarations(tools []wire.McpTool) [][2]string {
	out := make([][2]string, 0, len(tools))
	for _, tool := range tools {
		out = append(out, [2]string{tool.Name, tool.Description})
	}
	return out
}

// schemasValue is what a native request's tools cost the budget: each tool's name, description
// and served schema, as the TypeScript counts them.
func schemasValue(tools []wire.McpTool) *runcontext.Value {
	out := runcontext.Array()
	for _, tool := range tools {
		item := runcontext.ObjectOf("name", runcontext.String(tool.Name), "description", runcontext.String(tool.Description))
		if tool.InputSchema != nil {
			if parsed, err := runcontext.ParseJSON(tool.InputSchema.JSON()); err == nil {
				item.Set("inputSchema", parsed)
			}
		}
		out.Append(item)
	}
	return out
}

func trimmed(text string) bool { return strings.TrimSpace(text) != "" }

// errorFor is the failure an answer that is not a finished one earns.
func (t *turnState) unfinished() error {
	finished := t.completion
	reason := ""
	if finished != nil {
		reason = finished.FinishReason
	}
	stalled := t.r.sc.Execution.Progress.Stalled() && reason != "length" && reason != "content-filter" && reason != "error"
	code, source := failure.CompletionFailureCode(finished), "model"
	if stalled {
		code, source = "repeated-work", "desk"
	}
	f := failure.New(code, source, "unknown")
	f.Completion = finished
	return failure.NewError(f)
}
