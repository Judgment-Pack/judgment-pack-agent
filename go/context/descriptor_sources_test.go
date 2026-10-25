package runcontext

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
)

func snapshotHostTool(t *testing.T) HostTool {
	t.Helper()
	v := snapshotVector(t)
	return HostTool{Name: "search_tickets", Description: "Unpinned host text", InputSchema: json.RawMessage(`{"type":"object"}`), EffectClass: "read", Source: &ToolSource{Kind: "mcp", ID: "tickets", Trust: "public", ToolListDigest: v.Digest, DescriptorSnapshot: []byte(v.SnapshotText), CurrentDescriptorSnapshot: func(context.Context) ([]byte, error) { return []byte(v.SnapshotText), nil }}, Execute: func(context.Context, json.RawMessage) (ToolResult, error) {
		return ResultOf([]Content{TextContent("found")}, nil), nil
	}}
}
func snapshotEventValid(t *testing.T, event contract.RunEvent) {
	t.Helper()
	data, err := json.Marshal(contract.StampEvent(event))
	if err != nil {
		t.Fatal(err)
	}
	if err := contract.CheckRecord("event", data); err != nil {
		t.Fatalf("invalid event: %v", err)
	}
}
func TestSnapshotSourceAdmissionAndCapturedOffer(t *testing.T) {
	tool := snapshotHostTool(t)
	out, err := DeclareSources([]HostTool{tool}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.HostTools) != 1 || out.HostTools[0].Name != "tickets__search_tickets" || out.HostTools[0].Description != "Search tickets" || !strings.Contains(string(out.HostTools[0].InputSchema), `"minimum": 1.0`) {
		t.Fatalf("offer %+v", out)
	}
	for _, kind := range []string{"missing name", "wrong pin"} {
		t.Run(kind, func(t *testing.T) {
			first := snapshotHostTool(t)
			first.Name = "close"
			second := snapshotHostTool(t)
			if kind == "missing name" {
				second.Name = "unapproved"
			} else {
				first.Source.ToolListDigest = snapshotVector(t).ChangedDigest
				second.Source.ToolListDigest = first.Source.ToolListDigest
			}
			out, err := DeclareSources([]HostTool{first, second}, nil)
			if err != nil || len(out.HostTools) != 0 || len(out.Withheld) != 1 {
				t.Fatalf("not withheld whole: %+v %v", out, err)
			}
			notice := out.Withheld[0]
			snapshotEventValid(t, contract.RunEvent{Guardrail: &contract.RunEventGuardrail{Type: "guardrail", Tool: notice.Tool, Action: "refused", Detail: notice.Detail}})
		})
	}
}
func TestSnapshotSourceNeverFallsBackToLegacy(t *testing.T) {
	vector := snapshotVector(t)
	for _, data := range [][]byte{{}, []byte(`{}`), []byte(vector.SnapshotText + "\n"), []byte(strings.Replace(vector.SnapshotText, "Search tickets", strings.Repeat("x", DescriptorSnapshotBytes), 1))} {
		tool := snapshotHostTool(t)
		tool.Source.DescriptorSnapshot = data
		_, err := DeclareSources([]HostTool{tool}, nil)
		var authored *failure.Error
		if !errors.As(err, &authored) || authored.Failure.Code != "configuration" {
			t.Fatalf("malformed new pin selected legacy: %v", err)
		}
		snapshotEventValid(t, failure.Event(err, "context"))
	}
	for _, kind := range []string{"missing observer", "legacy observer", "orphan observer", "not MCP", "mixed modes", "different bytes"} {
		t.Run(kind, func(t *testing.T) {
			tool := snapshotHostTool(t)
			tools := []HostTool{tool}
			switch kind {
			case "missing observer":
				tool.Source.CurrentDescriptorSnapshot = nil
			case "legacy observer":
				tool.Source.CurrentDigest = func(context.Context) (string, error) { return vector.Digest, nil }
			case "orphan observer":
				tool.Source.DescriptorSnapshot = nil
			case "not MCP":
				tool.Source.Kind = "custom"
			case "mixed modes":
				legacy := snapshotHostTool(t)
				legacy.Name = "close"
				legacy.Source.DescriptorSnapshot = nil
				legacy.Source.CurrentDescriptorSnapshot = nil
				tools = append(tools, legacy)
			case "different bytes":
				other := snapshotHostTool(t)
				other.Name = "close"
				other.Source.DescriptorSnapshot = []byte(vector.ChangedText)
				tools = append(tools, other)
			}
			_, err := DeclareSources(tools, nil)
			if err == nil {
				t.Fatal("invalid snapshot declaration accepted")
			}
			snapshotEventValid(t, failure.Event(err, "context"))
		})
	}
}
func TestSnapshotSourceCurrentObservationRefusesAndLatchesWholeSource(t *testing.T) {
	vector := snapshotVector(t)
	for _, kind := range []string{"changed", "empty", "missing", "oversized", "error"} {
		t.Run(kind, func(t *testing.T) {
			broken := false
			ran := 0
			tool := snapshotHostTool(t)
			tool.Source.CurrentDescriptorSnapshot = func(context.Context) ([]byte, error) {
				if !broken {
					return []byte(vector.SnapshotText), nil
				}
				switch kind {
				case "changed":
					return []byte(vector.ChangedText), nil
				case "empty":
					return []byte{}, nil
				case "missing":
					return []byte(`{}`), nil
				case "oversized":
					return make([]byte, DescriptorSnapshotBytes+1), nil
				default:
					return nil, errors.New("untrusted observation detail")
				}
			}
			tool.Execute = func(context.Context, json.RawMessage) (ToolResult, error) { ran++; return ResultOf(nil, nil), nil }
			other := tool
			other.Name = "close"
			gate, tools, _ := sourceGate(t, []HostTool{tool, other}, Defaults, false)
			if _, err := callThrough(gate, tools, "tickets__search_tickets", `{}`); err != nil {
				t.Fatal(err)
			}
			broken = true
			if _, err := callThrough(gate, tools, "tickets__search_tickets", `{}`); err == nil {
				t.Fatal("changed observation ran")
			}
			broken = false
			if _, err := callThrough(gate, tools, "tickets__close", `{}`); err == nil {
				t.Fatal("source latch cleared")
			}
			if ran != 1 {
				t.Fatalf("ran %d", ran)
			}
			for _, event := range gate.TakeNotices() {
				snapshotEventValid(t, event)
				if event.Guardrail.Detail != vector.Refusal.AgentDetail {
					t.Fatal("shared refusal differs", event.Guardrail.Detail)
				}
				if len(event.Guardrail.Detail) >= 1024 || strings.Contains(event.Guardrail.Detail, "untrusted observation detail") {
					t.Fatal("unbounded/reflected refusal")
				}
			}
		})
	}
}
func TestSnapshotSourceFreezesObserverAndRetainsHostValidator(t *testing.T) {
	vector := snapshotVector(t)
	ran := 0
	changed := false
	tool := snapshotHostTool(t)
	tool.Name = "�"
	tool.InputSchema = json.RawMessage(`{"type":"object","required":["ticket"]}`)
	tool.Source.CurrentDescriptorSnapshot = func(context.Context) ([]byte, error) {
		if changed {
			return []byte(vector.ChangedText), nil
		}
		return []byte(vector.SnapshotText), nil
	}
	tool.Execute = func(context.Context, json.RawMessage) (ToolResult, error) { ran++; return ResultOf(nil, nil), nil }
	gate, tools, _ := sourceGate(t, []HostTool{tool}, Defaults, false)
	var name string
	for key, shown := range tools {
		name = key
		if string(shown.InputSchema) != `{"type":"object"}` {
			t.Fatal("absent schema did not project open")
		}
	}
	for i := range tool.Source.DescriptorSnapshot {
		tool.Source.DescriptorSnapshot[i] = 0
	}
	tool.Source.CurrentDescriptorSnapshot = func(context.Context) ([]byte, error) { return []byte(vector.SnapshotText), nil }
	tool.Source.CurrentDigest = func(context.Context) (string, error) { return vector.Digest, nil }
	for i := range tool.InputSchema {
		tool.InputSchema[i] = ' '
	}
	if _, err := callThrough(gate, tools, name, `{}`); err == nil {
		t.Fatal("host validator weakened")
	}
	if _, err := callThrough(gate, tools, name, `{"ticket":"T-1"}`); err != nil {
		t.Fatal(err)
	}
	changed = true
	if _, err := callThrough(gate, tools, name, `{"ticket":"T-1"}`); err == nil {
		t.Fatal("observer replaced after admission")
	}
	if ran != 1 {
		t.Fatalf("ran %d", ran)
	}
}
func TestSnapshotSourceDecisionAndApprovedContinuation(t *testing.T) {
	vector := snapshotVector(t)
	for _, kind := range []string{"changed", "error", "malformed"} {
		t.Run(kind, func(t *testing.T) {
			changed := true
			approved := 0
			ran := 0
			tool := snapshotHostTool(t)
			tool.EffectClass = "act"
			tool.Source.CurrentDescriptorSnapshot = func(context.Context) ([]byte, error) {
				if changed {
					switch kind {
					case "changed":
						return []byte(vector.ChangedText), nil
					case "error":
						return nil, errors.New("observer unavailable")
					default:
						return []byte(`{}`), nil
					}
				}
				return []byte(vector.SnapshotText), nil
			}
			tool.Approve = func(ApprovalRequest) string { approved++; return "ask" }
			tool.Execute = func(context.Context, json.RawMessage) (ToolResult, error) { ran++; return ResultOf(nil, nil), nil }
			other := tool
			other.Name = "close"
			first, tools, _ := sourceGate(t, []HostTool{tool, other}, Defaults, false)
			book := &book{}
			first.SetApprovals(book)
			if answer, _, _ := first.Decide(context.Background(), "tickets__search_tickets", json.RawMessage(`{}`), "snapshot-decide", true); answer != "checked" {
				t.Fatal(answer)
			}
			changed = false
			if _, err := callThrough(first, tools, "tickets__close", `{}`); err == nil {
				t.Fatal("decision observation latch cleared")
			}
			if approved != 0 {
				t.Fatal("changed source reached approval policy")
			}
			continued := contract.CheckpointApproval{CallID: "continued-close", Name: "tickets__close", Tool: "close", Source: first.Entries["tickets__close"].Record, Args: map[string]json.RawMessage{}, State: "approved"}
			if _, executed, err := first.Approved(context.Background(), continued, tools["tickets__close"].Execute); err != nil || executed {
				t.Fatalf("decision failure bypassed continued source tool: %v %v", executed, err)
			}
			paused, _, _ := sourceGate(t, []HostTool{tool}, Defaults, false)
			paused.SetApprovals(book)
			if answer, _, _ := paused.Decide(context.Background(), "tickets__search_tickets", json.RawMessage(`{}`), "snapshot-approved", true); answer != "ask" {
				t.Fatal(answer)
			}
			raw, _ := json.Marshal(book.approval)
			var restored contract.CheckpointApproval
			if err := json.Unmarshal(raw, &restored); err != nil {
				t.Fatal(err)
			}
			restored.State = "approved"
			changed = true
			resumed, offered, _ := sourceGate(t, []HostTool{tool}, Defaults, false)
			if _, executed, err := resumed.Approved(context.Background(), restored, offered["tickets__search_tickets"].Execute); err != nil || executed {
				t.Fatalf("resumed executed %v: %v", executed, err)
			}
			if ran != 0 {
				t.Fatalf("ran %d", ran)
			}
			for _, event := range resumed.TakeNotices() {
				snapshotEventValid(t, event)
			}
		})
	}
}
func TestSnapshotSourceOverlappingObservationCannotClearFailure(t *testing.T) {
	vector := snapshotVector(t)
	started := make(chan struct{})
	release := make(chan struct{})
	var mu sync.Mutex
	checks := 0
	ran := 0
	tool := snapshotHostTool(t)
	tool.Source.CurrentDescriptorSnapshot = func(context.Context) ([]byte, error) {
		mu.Lock()
		checks++
		number := checks
		mu.Unlock()
		if number == 1 {
			close(started)
			<-release
			return []byte(vector.SnapshotText), nil
		}
		return []byte(vector.ChangedText), nil
	}
	tool.Execute = func(context.Context, json.RawMessage) (ToolResult, error) {
		mu.Lock()
		ran++
		mu.Unlock()
		return ResultOf(nil, nil), nil
	}
	other := tool
	other.Name = "close"
	gate, tools, _ := sourceGate(t, []HostTool{tool, other}, Defaults, false)
	done := make(chan error, 1)
	go func() { _, err := callThrough(gate, tools, "tickets__search_tickets", `{}`); done <- err }()
	<-started
	if _, err := callThrough(gate, tools, "tickets__close", `{}`); err == nil {
		t.Fatal("changed overlapping observation ran")
	}
	close(release)
	if err := <-done; err == nil {
		t.Fatal("older observation cleared failure")
	}
	mu.Lock()
	defer mu.Unlock()
	if ran != 0 {
		t.Fatalf("ran %d", ran)
	}
}
func TestSnapshotSourceActualSessionRefusesBeforeHostCall(t *testing.T) {
	vector := snapshotVector(t)
	ran := 0
	authorized := 0
	tool := snapshotHostTool(t)
	tool.Source.CurrentDescriptorSnapshot = func(context.Context) ([]byte, error) { return []byte(vector.ChangedText), nil }
	tool.Execute = func(context.Context, json.RawMessage) (ToolResult, error) { ran++; return ResultOf(nil, nil), nil }
	session := modelSession()
	session.HostTools = []HostTool{tool}
	session.Context = &Input{Authorize: func(context.Context) error { authorized++; return nil }}
	run, err := ContextualSession(context.Background(), session)
	if err != nil {
		t.Fatal(err)
	}
	defer run.Context.Close()
	var call HostTool
	for _, offered := range run.Session.HostTools {
		if offered.Name == "tickets__search_tickets" {
			call = offered
		}
	}
	if call.Execute == nil {
		t.Fatal("snapshot tool missing")
	}
	if _, err := call.Execute(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Fatal("changed snapshot reached host")
	}
	if ran != 0 || authorized == 0 {
		t.Fatalf("ran %d, authorized %d", ran, authorized)
	}
	for _, event := range run.Context.Sources.TakeNotices() {
		snapshotEventValid(t, event)
	}
}

func TestSnapshotSourceDecisionFailureSharedWithDelegatedGate(t *testing.T) {
	vector := snapshotVector(t)
	changed := true
	ran := 0
	tool := snapshotHostTool(t)
	tool.EffectClass = "act"
	tool.Source.CurrentDescriptorSnapshot = func(context.Context) ([]byte, error) {
		if changed {
			return []byte(vector.ChangedText), nil
		}
		return []byte(vector.SnapshotText), nil
	}
	tool.Execute = func(context.Context, json.RawMessage) (ToolResult, error) { ran++; return ResultOf(nil, nil), nil }
	parent, _, _ := sourceGate(t, []HostTool{tool}, Defaults, false)
	if answer, _, _ := parent.Decide(context.Background(), "tickets__search_tickets", json.RawMessage(`{}`), "parent-failure", true); answer != "checked" {
		t.Fatal(answer)
	}
	changed = false
	tool.EffectClass = "read"
	declared, err := DeclareSources([]HostTool{tool}, nil)
	if err != nil {
		t.Fatal(err)
	}
	policy := Defaults
	child := NewSourceGate(declared, &policy, sourceMarker, "json-schema", parent.rows, parent)
	if _, err := child.Call(context.Background(), "tickets__search_tickets", json.RawMessage(`{}`), declared.HostTools[0].Execute); err == nil {
		t.Fatal("delegated gate bypassed parent failure")
	}
	if ran != 0 {
		t.Fatalf("ran %d", ran)
	}
}
