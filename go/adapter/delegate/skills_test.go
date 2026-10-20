package delegate_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/delegate"
	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/turn"
	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
	"github.com/Judgment-Pack/judgment-pack-agent/go/relay"
)

// A delegated child is handed no skills (ADR-0009, #93): the parent declares one and is offered its
// catalogue and the loaders; the child's run carries no skills, so the boundary opened on it gives
// its system text no catalogue and its offer no load_skill.
func TestADelegatedChildIsHandedNoSkills(t *testing.T) {
	skill := runcontext.Skill{ID: "expense-policy", Name: "expense_policy", Description: "Apply the expense policy.", Instructions: "Check each line against the policy.",
		Source: runcontext.SkillSource{Kind: "custom", ID: "policies", Trust: "person", Version: "4"}}
	skill.PinnedDigest = runcontext.SkillDigest(skill)
	type seen struct {
		system string
		tools  []string
	}
	var mu sync.Mutex
	var runs []seen
	inner := adapter.Func(func(ctx context.Context, run *adapter.Run) error {
		session := runcontext.Session{Prompt: run.Prompt, HostTools: run.HostTools, Skills: run.Skills, Context: run.Context,
			Model: &runcontext.ModelSession{Family: "openai-compatible", Model: "scripted-model"},
			CallTool: func(context.Context, string, json.RawMessage) (runcontext.ToolResult, error) {
				return runcontext.ToolResult{}, nil
			}}
		opened, err := runcontext.ContextualSession(ctx, session)
		if err != nil {
			return err
		}
		defer opened.Context.Close()
		var names []string
		var delegateTool *runcontext.HostTool
		for at, tool := range opened.Session.HostTools {
			names = append(names, tool.Name)
			if tool.Name == turn.DelegateTool {
				delegateTool = &opened.Session.HostTools[at]
			}
		}
		mu.Lock()
		runs = append(runs, seen{system: opened.Context.System("GUIDANCE", ""), tools: names})
		mu.Unlock()
		if delegateTool != nil {
			if _, err := delegateTool.Execute(ctx, json.RawMessage(`{"tasks":[{"title":"Look","objective":"Look the rate up.","profile":"review"}]}`)); err != nil {
				return err
			}
		}
		run.Emit(contract.RunEvent{Message: &contract.RunEventMessage{Type: "message", Text: "done"}})
		run.Emit(contract.RunEvent{End: &contract.RunEventEnd{Type: "end"}})
		return nil
	})
	run := &adapter.Run{AllowConversation: true, Interactive: true, Prompt: "Draft the pack.", Skills: []runcontext.Skill{skill},
		Model: func(context.Context, string, relay.Request) (*http.Response, error) {
			return nil, errors.New("no model call here")
		}, Emit: func(contract.RunEvent) {},
		Children: func(_ context.Context, profile contract.DelegatedTaskProfile, emit func(contract.RunEvent)) (*gate.Calls, []contract.ToolOfferRuntimeTool, error) {
			child := gate.New(gate.Options{Granted: gate.Ceiling, Emit: emit})
			return child.Calls(), nil, nil
		}}
	if err := delegate.With(inner, delegate.Options{}).Run(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("%d runs, not the parent's and the child's", len(runs))
	}
	parent, child := runs[0], runs[1]
	if !strings.Contains(parent.system, "\n"+runcontext.SkillsHeader+"\n") || !slices.Contains(parent.tools, runcontext.LoadSkill) || !slices.Contains(parent.tools, turn.DelegateTool) {
		t.Fatalf("the parent was not offered its skills: %v", parent.tools)
	}
	if strings.Contains(child.system, "SKILLS (") || slices.Contains(child.tools, runcontext.LoadSkill) || slices.Contains(child.tools, runcontext.UnloadSkill) {
		t.Fatalf("the child was handed skills: %v", child.tools)
	}
}
