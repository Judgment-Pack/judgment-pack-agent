package gate

import (
	"context"
	"encoding/json"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/internal/runtimeactions"
)

// RuntimeExecutionOwner identifies the actual immutable host execution, never a cache identity.
type RuntimeExecutionOwner = runtimeactions.Owner
type RuntimeActionPort = runtimeactions.Port
type RuntimeApprovalRequest = runtimeactions.Request

func (g *Gate) acting(name string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, tool := range g.offer.Tools {
		if tool.Name == name {
			return tool.Meta.Effect == "act"
		}
	}
	return false
}
func (g *Gate) bindRuntimeActions(listed []contract.ToolOfferRuntimeTool) error {
	if g.runtimeActions == nil {
		return nil
	}
	port := *g.runtimeActions
	g.mu.Lock()
	bound := g.runtimeOwner != nil
	g.mu.Unlock()
	if bound {
		return runtimeactions.ErrStale
	}
	owner, err := runtimeactions.Register(runtimeactions.Connector{
		Port: port, InitialTools: listed,
		List: g.listRuntime,
		Prepare: func(name string, args json.RawMessage) (json.RawMessage, error) {
			phase := g.current()
			if phase.Kind != PhaseLoop || !contains(phase.Runtime, name) || !g.acting(name) {
				return nil, runtimeactions.ErrStale
			}
			decided := Inspect(g.frame(name, args), g.allowed, g.bounds)
			if !decided.Send {
				return nil, decided.Violation
			}
			var frame struct {
				Params struct {
					Arguments json.RawMessage `json:"arguments"`
				} `json:"params"`
			}
			if json.Unmarshal(decided.Frame, &frame) != nil {
				return nil, runtimeactions.ErrStale
			}
			return frame.Params.Arguments, nil
		},
		Dispatch: func(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
			phase := g.current()
			if phase.Kind != PhaseLoop || !contains(phase.Runtime, name) || !g.acting(name) {
				return nil, runtimeactions.ErrStale
			}
			result, err := g.send(ctx, name, args)
			if err != nil {
				return nil, err
			}
			out := map[string]any{"content": []map[string]any{{"type": "text", "text": result.Text}}, "isError": result.IsError}
			if len(result.Structured) > 0 {
				out["structuredContent"] = result.Structured
			}
			return json.Marshal(out)
		},
		Open: func() bool { return true },
	})
	if err != nil {
		return err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.runtimeOwner != nil {
		return runtimeactions.ErrStale
	}
	g.runtimeOwner = owner
	return nil
}

func (g *Gate) listRuntime(ctx context.Context) ([]contract.ToolOfferRuntimeTool, error) {
	tools := []contract.ToolOfferRuntimeTool{}
	names := map[string]bool{}
	cursors := map[string]bool{}
	cursor := ""
	for page := 0; page < 64; page++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var listed struct {
			Tools      []contract.ToolOfferRuntimeTool `json:"tools"`
			NextCursor string                          `json:"nextCursor"`
		}
		if err := g.rpc(ctx, "tools/list", params, &listed); err != nil {
			return nil, err
		}
		for _, tool := range listed.Tools {
			if names[tool.Name] || len(tools) >= 4096 {
				return nil, runtimeactions.ErrStale
			}
			names[tool.Name] = true
			tools = append(tools, tool)
		}
		if listed.NextCursor == "" {
			return tools, nil
		}
		if cursors[listed.NextCursor] {
			return nil, runtimeactions.ErrStale
		}
		cursors[listed.NextCursor] = true
		cursor = listed.NextCursor
	}
	return nil, runtimeactions.ErrStale
}

// RuntimeActionOwner carries private actual-connector provenance into the context. The returned
// handle has no exported fields, constructors or execution methods and is never an approval.
func RuntimeActionOwner(c *Calls) *runtimeactions.Handle {
	if c == nil {
		return nil
	}
	c.g.mu.Lock()
	defer c.g.mu.Unlock()
	return c.g.runtimeOwner
}
