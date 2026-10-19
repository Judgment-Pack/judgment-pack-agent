package eino

import (
	"context"
	"errors"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

// The agent each stretch of the turn runs: Eino's ChatModelAgent under a Runner, its model the
// relay chat model, its tools declarations only, and one middleware, the boundary, whose hooks
// belong to whichever phase is running. Every call the model makes is answered by the boundary,
// on both of Eino's entry points: WrapInvokableToolCall for a name the phase declared, and the
// tools node's UnknownToolsHandler for any other (Eino sends an unknown name past the
// middleware). ExecuteSequentially runs one turn's calls one at a time, in the model's order, so
// each call's events stay between its call and its result.

// phase is what one phase of the run answers behind the boundary.
type phase struct {
	// offered are the names this phase offered the model; any other is refused at dispatch.
	offered map[string]bool
	// step is one model request: prepare it, send it, read what came back.
	step func(ctx context.Context, input []*schema.Message, send sender) (*schema.Message, error)
	// call is one tool call: the gate's dispatch, its text as the tool message carries it.
	call func(ctx context.Context, name, callID, arguments string) (string, error)
}

// sender sends one prepared request through the framework's model chain.
type sender func(ctx context.Context, request *stepRequest, messages []*schema.Message) (*schema.Message, error)

// boundary is the one middleware this adapter mounts.
type boundary struct {
	*adk.BaseChatModelAgentMiddleware
	current func() *phase
}

// WrapModel makes each model request the phase's step.
func (b *boundary) WrapModel(_ context.Context, inner model.BaseModel[*schema.Message], _ *adk.ModelContext) (model.BaseModel[*schema.Message], error) {
	return &stepModel{inner: inner, current: b.current}, nil
}

// WrapInvokableToolCall answers a declared name's call with the phase's dispatch; the framework's
// own tool is never run.
func (b *boundary) WrapInvokableToolCall(_ context.Context, _ adk.InvokableToolCallEndpoint, tCtx *adk.ToolContext) (adk.InvokableToolCallEndpoint, error) {
	return func(ctx context.Context, arguments string, _ ...tool.Option) (string, error) {
		return b.current().call(ctx, tCtx.Name, tCtx.CallID, arguments)
	}, nil
}

// unknown answers a name the phase did not declare, through the same dispatch.
func (b *boundary) unknown(ctx context.Context, name, input string) (string, error) {
	return b.current().call(ctx, name, compose.GetToolCallID(ctx), input)
}

// stepModel is the framework's model as the boundary hands it on: each Generate is the phase's
// step, which sends through the model chain beneath it.
type stepModel struct {
	inner   model.BaseModel[*schema.Message]
	current func() *phase
}

func (s *stepModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return s.current().step(ctx, input, func(ctx context.Context, request *stepRequest, messages []*schema.Message) (*schema.Message, error) {
		return s.inner.Generate(context.WithValue(ctx, stepKey{}, request), messages, opts...)
	})
}

func (s *stepModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	message, err := s.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

// declared is a tool the model is shown and the framework never runs: the boundary answers it.
type declared struct{ name, description string }

func (d *declared) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: d.name, Desc: d.description}, nil
}

func (d *declared) InvokableRun(context.Context, string, ...tool.Option) (string, error) {
	return "", errors.New(d.name + " was reached past the adapter's boundary; nothing was run")
}

// stretch runs one stretch of the turn: the agent from these messages until the model stops
// calling tools. Eino's own retries, checkpoint, interrupt and subagents are not configured.
func (r *runState) stretch(ctx context.Context, names [][2]string, model *relayModel, current func() *phase, input []*schema.Message) error {
	edge := &boundary{BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{}, current: current}
	tools := make([]tool.BaseTool, 0, len(names))
	for _, held := range names {
		tools = append(tools, &declared{name: held[0], description: held[1]})
	}
	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        "judgment-pack",
		Description: "The Judgment Pack assistant's loop.",
		Model:       model,
		ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{
			Tools:               tools,
			UnknownToolsHandler: edge.unknown,
			ExecuteSequentially: true,
		}},
		Handlers:      []adk.ChatModelAgentMiddleware{edge},
		MaxIterations: 10_000,
	})
	if err != nil {
		return err
	}
	runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent})
	events := runner.Run(ctx, input)
	for {
		event, ok := events.Next()
		if !ok {
			return nil
		}
		if event.Err != nil {
			return event.Err
		}
	}
}
