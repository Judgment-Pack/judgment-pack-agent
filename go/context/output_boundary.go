package runcontext

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

func (c *SessionContext) ValidateOutputProposal(ctx context.Context, document json.RawMessage) error {
	if ctx == nil {
		ctx = context.Background()
	}
	offered := false
	for _, t := range c.session.Tools {
		if t.Name == "validate" {
			offered = true
		}
	}
	if offered && c.session.CallTool != nil {
		result, err := c.session.CallTool(ctx, "validate", ObjectOf("content", String(string(document))).JSON())
		if err == nil && (result.IsError == nil || !*result.IsError) {
			v := result.StructuredContent
			if v == nil {
				var text string
				for _, part := range result.Content {
					if part.Type() == "text" {
						text += part.Text()
					}
				}
				v, _ = ParseJSON([]byte(text))
			}
			if editString(v.Get("status")) == "valid" {
				return nil
			}
		}
	}
	return c.Output.Refuse("proposal", "proposal-validation")
}
func (b *Boundary) guardOutput(event contract.RunEvent) (contract.RunEvent, error) {
	g := b.run.Context.Output
	if g == nil {
		return event, nil
	}
	var err error
	if event.Reasoning != nil {
		event.Reasoning.Text, err = g.Text(event.Reasoning.Text, "message", true)
	}
	if event.Message != nil {
		event.Message.Text, err = g.Text(event.Message.Text, "message", true)
	}
	if event.ToolCall != nil {
		event.ToolCall.Args, err = g.JSON(event.ToolCall.Args, OutputWhere(event.ToolCall.Name), g.Mode == "refuse")
	}
	if event.UserQuestion != nil {
		raw, _ := json.Marshal(event.UserQuestion.Question)
		clean, e := g.JSON(raw, "tool argument", true)
		err = e
		if err == nil {
			err = json.Unmarshal(clean, &event.UserQuestion.Question)
		}
	}
	if event.DelegationRequested != nil {
		raw, _ := json.Marshal(event.DelegationRequested.Request)
		clean, e := g.JSON(raw, "tool argument", true)
		err = e
		if err == nil {
			err = json.Unmarshal(clean, &event.DelegationRequested.Request)
		}
	}
	if event.Proposal != nil {
		raw, _ := json.Marshal(event.Proposal)
		clean, e := g.JSON(raw, "proposal", true)
		err = e
		if err == nil && !bytes.Equal(raw, clean) {
			err = json.Unmarshal(clean, event.Proposal)
			if err == nil {
				document, _ := json.Marshal(event.Proposal.Document)
				err = b.run.Context.ValidateOutputProposal(b.run.Context.outputContext, document)
			}
		}
	}
	return event, err
}
