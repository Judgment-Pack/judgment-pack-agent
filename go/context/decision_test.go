package runcontext

// Each act call's decision is its own (ADR-0008; the v0.5.0 release review, #83 rows 1 and 7).

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

// stateOf is a call's state argument.
func stateOf(raw json.RawMessage) string {
	var args struct {
		State string `json:"state"`
	}
	_ = json.Unmarshal(raw, &args)
	return args.State
}

// workItemTool is the declarations' update_work_item, its policy refusing the state Deleted and
// allowing any other (allowed is told of each allow), its runs counted.
func workItemTool(t *testing.T, allowed func(), ran *int) []HostTool {
	tools := declaredTools(t, vectorItems(t, "tool-offer/source-declarations.json"))
	for at := range tools {
		tools[at].Approve = func(call ApprovalRequest) string {
			if stateOf(call.Args) == "Deleted" {
				return "refuse"
			}
			allowed()
			return "allow"
		}
		tools[at].Execute = func(context.Context, json.RawMessage) (ToolResult, error) {
			*ran++
			return ResultOf(nil, nil), nil
		}
	}
	return tools
}

// The first call is allowed, then stopped by the host's Authorize before the source gate. The second
// call of the same tool, with other arguments, is refused and not run: by its own decision where the
// calls carry ids, and with the reason approval stale where they carry none.
func TestTheSecondCallIsRefusedWhenAuthorizeStopsTheFirst(t *testing.T) {
	want := readJSON(t, "contract/vectors/approval/stale-decision.json").Get("items").Items()[1]
	for _, ids := range []bool{true, false} {
		ran := 0
		failNext := false
		session := modelSession()
		session.HostTools = workItemTool(t, func() { failNext = true }, &ran)
		session.Context = &Input{Authorize: func(context.Context) error {
			if failNext {
				failNext = false
				return errors.New("not authorized this time")
			}
			return nil
		}}
		run, err := ContextualSession(context.Background(), session)
		if err != nil {
			t.Fatal(err)
		}
		var execute func(context.Context, json.RawMessage) (ToolResult, error)
		for _, tool := range run.Session.HostTools {
			if tool.Name == "ado__update_work_item" {
				execute = tool.Execute
			}
		}
		first, second := "", ""
		if ids {
			first, second = "call-1", "call-2"
		}
		done := json.RawMessage(`{"id":7,"state":"Done"}`)
		deleted := json.RawMessage(`{"id":7,"state":"Deleted"}`)
		if held := run.Context.Approval(context.Background(), "ado__update_work_item", done, first); held != nil {
			t.Fatal("the first call was held")
		}
		if _, err := execute(WithCallID(context.Background(), first), done); err == nil {
			t.Fatal("the host's Authorize did not stop the first call")
		}
		run.Context.Sources.TakeNotices()
		if held := run.Context.Approval(context.Background(), "ado__update_work_item", deleted, second); held != nil {
			t.Fatal("the second call was held")
		}
		if _, err := execute(WithCallID(context.Background(), second), deleted); err == nil {
			t.Fatalf("ids %t: the second call ran", ids)
		}
		notices := run.Context.Sources.TakeNotices()
		if len(notices) != 1 || notices[0].Guardrail == nil || notices[0].Guardrail.Reason == nil {
			t.Fatalf("ids %t: notices %+v", ids, notices)
		}
		reason := *notices[0].Guardrail.Reason
		if ids && reason != "approval refused" || !ids && (reason != want.Get("reason").Str() || notices[0].Guardrail.Detail != want.Get("detail").Str()) {
			t.Errorf("ids %t: the refusal said %q: %s", ids, reason, notices[0].Guardrail.Detail)
		}
		if ran != 0 {
			t.Fatalf("ids %t: the host ran %d calls", ids, ran)
		}
		run.Context.Close()
	}
}

// An approved call the gate no longer admits on the continued run (its tool not offered as the
// person saw it) is the gate's refusal, approval stale, never the policy's or the person's; it is
// not run.
func TestAContinuedCallTheGateNoLongerAdmitsIsApprovalStale(t *testing.T) {
	ran := 0
	tools := workItemTool(t, func() {}, &ran)
	gate, _, _ := sourceGate(t, tools, Defaults, false)
	gate.SetApprovals(&book{})
	approved := contract.CheckpointApproval{CallID: "call-a", Name: "tracker__gone", Tool: "gone",
		Source: contract.ToolOfferSourceRecord{Kind: "mcp", ID: "tracker", Trust: "public", Tool: ptr("gone")}, State: "approved"}
	_, didRun, err := gate.Approved(context.Background(), approved, func(context.Context, json.RawMessage) (ToolResult, error) {
		ran++
		return ResultOf(nil, nil), nil
	})
	if err != nil || didRun || ran != 0 {
		t.Fatalf("ran %t (%d), %v", didRun, ran, err)
	}
	notices := gate.TakeNotices()
	if len(notices) != 1 || notices[0].Guardrail == nil || notices[0].Guardrail.Reason == nil || *notices[0].Guardrail.Reason != "approval stale" {
		t.Fatalf("notices %+v", notices)
	}
}
