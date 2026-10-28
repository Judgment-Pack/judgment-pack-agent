package runcontext

import (
	"context"
	"encoding/json"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"sync"
	"testing"
)

func TestConcurrentOriginalApprovalClaimRemainsPending(t *testing.T) {
	entered, held := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(held) })
	asked, ran := 0, 0
	identity := contract.ToolOfferIdentity{Kind: "connection", Label: "tracker"}
	tool := HostTool{Name: "create_issue", Description: "Create an issue.", InputSchema: json.RawMessage(`{"type":"object"}`), Taxonomy: &ToolTaxonomy{Operation: "act", Effect: "act"}, EffectClass: "act", Source: &ToolSource{Kind: "custom", ID: "tracker", Trust: "person", Version: "1", ToolListDigest: "sha256:d6523ad27b67175e7423219dc07eb056332d4b640030068e8c09549e91ca4d82", Identity: &identity, CurrentIdentity: func(context.Context) (contract.ToolOfferIdentity, error) {
		close(entered)
		<-held
		return identity, nil
	}}, Approve: func(ApprovalRequest) string { asked++; return "ask" }, Execute: func(context.Context, json.RawMessage) (ToolResult, error) { ran++; return ResultOf(nil, nil), nil }}
	session := modelSession()
	session.HostTools = []HostTool{tool}
	ready, e := ContextualSession(context.Background(), session)
	if e != nil {
		t.Fatal(e)
	}
	defer ready.Context.Close()
	gate := ready.Context.Sources
	first := make(chan string, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		answer, _, _ := gate.Decide(context.Background(), "tracker__create_issue", json.RawMessage(`{}`), "first-call", true)
		first <- answer
	}()
	defer func() { release(); <-finished }()
	<-entered
	if ready.Context.Execution.State().Approval != nil {
		t.Fatal("first approval persisted before its host observation completed")
	}
	answer, detail, _ := gate.Decide(context.Background(), "tracker__create_issue", json.RawMessage(`{}`), "second-call", true)
	if answer != "pending" || gate.Pending("second-call", "tracker__create_issue") != detail {
		t.Fatalf("concurrent original claim became %s instead of pending: %s", answer, detail)
	}
	if _, e = gate.Call(WithCallID(context.Background(), "second-call"), "tracker__create_issue", json.RawMessage(`{}`), tool.Execute); e == nil {
		t.Fatal("pending claim executed")
	}
	refused := int64(0)
	for _, row := range ready.Context.Execution.State().Sources {
		if row.Source == "tracker" {
			refused = row.Refused
		}
	}
	if asked != 0 || ran != 0 || refused != 1 {
		t.Fatalf("pending claim changed ownership: asked=%d ran=%d refused=%d", asked, ran, refused)
	}
	release()
	if answer = <-first; answer != "ask" {
		t.Fatalf("first original claim became %s", answer)
	}
	if asked != 1 || ready.Context.Execution.State().Approval == nil || ready.Context.Execution.State().Approval.CallID != "first-call" {
		t.Fatal("first original claim lost approval ownership")
	}
}
