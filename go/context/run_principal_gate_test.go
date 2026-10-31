package runcontext

// ADR-0029: a delegated run's source gate admits each call under its chain before dispatch, and
// refuses by name; the twins of packages/harness/principalGate.test.ts.

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/wire"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

type principalPort struct {
	mu     sync.Mutex
	answer string
	reads  []PrincipalAdmissionRead
	now    string
}

func (p *principalPort) set(answer string) { p.mu.Lock(); p.answer = answer; p.mu.Unlock() }
func (p *principalPort) counts() []bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := []bool{}
	for _, read := range p.reads {
		out = append(out, read.Call.Count)
	}
	return out
}

func newPrincipalPort() *principalPort {
	return &principalPort{answer: `{"admission":"admit"}`, now: "2026-10-30T01:00:00.000Z"}
}

type principalRan struct {
	mu   sync.Mutex
	list []string
}

func (r *principalRan) add(name string) { r.mu.Lock(); r.list = append(r.list, name); r.mu.Unlock() }
func (r *principalRan) get() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string{}, r.list...)
}

func principalTools(ran *principalRan, approve map[string]ApprovalPolicy, execute map[string]func(context.Context, json.RawMessage) (ToolResult, error)) []HostTool {
	tool := func(name, effect string, source ToolSource) HostTool {
		operation := effect
		if effect == "read-external" {
			operation = "read"
		}
		out := HostTool{Name: name, Description: name, InputSchema: json.RawMessage(`{"type":"object"}`), Taxonomy: &wire.ToolTaxonomy{Operation: operation, Effect: effect}, Source: &source}
		out.Execute = func(context.Context, json.RawMessage) (ToolResult, error) {
			ran.add(name)
			return ResultOf([]Content{TextContent("done")}, nil), nil
		}
		if effect == "act" {
			out.Approve = func(ApprovalRequest) string { return "allow" }
		}
		if policy, ok := approve[name]; ok {
			out.Approve = policy
		}
		if run, ok := execute[name]; ok {
			out.Execute = run
		}
		return out
	}
	notes := ToolSource{Kind: "custom", ID: "notes", Trust: "first-party"}
	external := func(name string, source ToolSource) HostTool {
		source.VerifyReceipt = func(context.Context, ReceiptEvidence) (bool, error) { return true, nil }
		out := tool(name, "read-external", source)
		out.Execute = func(context.Context, json.RawMessage) (ToolResult, error) {
			ran.add(name)
			result := ResultOf([]Content{TextContent("done")}, nil)
			result.Receipt = "r-1"
			return result, nil
		}
		return out
	}
	tools := []HostTool{tool("read_note", "read", notes), tool("write_note", "act", notes), tool("delete_note", "act", notes),
		external("query", ToolSource{Kind: "gateway", ID: "tickets", Trust: "connected", GatewayScope: &GatewayScope{Authority: "gateway:test", Source: "tickets/live"}}),
		external("lookup", ToolSource{Kind: "gateway", ID: "records", Trust: "connected"})}
	return pinLocalFixture(tools)
}

type principalBook struct{ current *contract.CheckpointApproval }

func (b *principalBook) Current() *contract.CheckpointApproval { return b.current }
func (b *principalBook) Ask(record contract.CheckpointApproval) bool {
	b.current = &record
	return true
}
func (b *principalBook) Closed() string { return "" }

func delegatedGate(t *testing.T, port *principalPort, tools []HostTool, chain int) (*SourceGate, map[string]HostTool, *[]contract.ProvenanceSourceFigures) {
	t.Helper()
	gate, byName, rows := sourceGate(t, tools, Defaults, false)
	v := loadPrincipalChains(t)
	opened, err := OpenPrincipal(principalInput(v, v.Items[chain], func(_ context.Context, read PrincipalAdmissionRead) ([]byte, error) {
		port.mu.Lock()
		defer port.mu.Unlock()
		port.reads = append(port.reads, read)
		if port.answer == "throw" {
			return nil, errors.New("port down")
		}
		return []byte(port.answer), nil
	}, func() (time.Time, error) { return time.Parse(time.RFC3339Nano, port.now) }))
	if err != nil {
		t.Fatal(err)
	}
	gate.UsePrincipal(opened)
	return gate, byName, rows
}

func lastRefusal(t *testing.T, gate *SourceGate) *contract.RunEventGuardrail {
	t.Helper()
	notices := gate.TakeNotices()
	if len(notices) == 0 || notices[len(notices)-1].Guardrail == nil {
		t.Fatalf("no refusal notice: %+v", notices)
	}
	return notices[len(notices)-1].Guardrail
}

func wantRefusal(t *testing.T, notice *contract.RunEventGuardrail, name, reason string) {
	t.Helper()
	if notice.Reason == nil || *notice.Reason != "delegation refused" || notice.Refusal == nil || notice.Refusal.Name != name || (reason == "") != (notice.Refusal.Reason == nil) || reason != "" && *notice.Refusal.Reason != reason || notice.Principal == nil {
		raw, _ := json.Marshal(notice)
		t.Fatalf("refusal %s, want %s/%s", raw, name, reason)
	}
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sameBools(a, b []bool) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestPrincipalGateAdmitsAndRefusesBeforeDispatch(t *testing.T) {
	t.Run("a call within the scope runs, counted once", func(t *testing.T) {
		port, ran := newPrincipalPort(), &principalRan{}
		gate, byName, _ := delegatedGate(t, port, principalTools(ran, nil, nil), 0)
		if _, err := callThrough(gate, byName, "notes__read_note", `{}`); err != nil {
			t.Fatal(err)
		}
		if !sameStrings(ran.get(), []string{"read_note"}) || !sameBools(port.counts(), []bool{true}) {
			t.Fatalf("ran %v counts %v", ran.get(), port.counts())
		}
	})
	t.Run("a tool the scope does not name is refused by name, counting nothing", func(t *testing.T) {
		port, ran := newPrincipalPort(), &principalRan{}
		gate, byName, rows := delegatedGate(t, port, principalTools(ran, nil, nil), 0)
		if _, err := callThrough(gate, byName, "notes__delete_note", `{}`); err == nil {
			t.Fatal("ran")
		}
		wantRefusal(t, lastRefusal(t, gate), "out-of-scope", "tool")
		if len(ran.get()) != 0 || !sameBools(port.counts(), []bool{false, false}) {
			t.Fatalf("ran %v counts %v", ran.get(), port.counts())
		}
		for _, row := range *rows {
			if row.Source == "notes" && (row.Calls != 0 || row.Refused != 1) {
				t.Fatalf("row %+v", row)
			}
		}
	})
	t.Run("after expiry, asking nothing", func(t *testing.T) {
		port, ran := newPrincipalPort(), &principalRan{}
		port.now = "2026-10-30T08:00:00.000Z"
		gate, byName, _ := delegatedGate(t, port, principalTools(ran, nil, nil), 0)
		if _, err := callThrough(gate, byName, "notes__read_note", `{}`); err == nil {
			t.Fatal("ran")
		}
		wantRefusal(t, lastRefusal(t, gate), "expired", "")
		if len(ran.get()) != 0 || len(port.counts()) != 0 {
			t.Fatalf("ran %v counts %v", ran.get(), port.counts())
		}
	})
	t.Run("an act call revoked before its approval counts nothing", func(t *testing.T) {
		port, ran := newPrincipalPort(), &principalRan{}
		port.answer = `{"admission":"refuse","refusal":"revoked"}`
		gate, byName, _ := delegatedGate(t, port, principalTools(ran, nil, nil), 0)
		if _, err := callThrough(gate, byName, "notes__write_note", `{}`); err == nil {
			t.Fatal("ran")
		}
		wantRefusal(t, lastRefusal(t, gate), "revoked", "")
		if len(ran.get()) != 0 || !sameBools(port.counts(), []bool{false, false}) {
			t.Fatalf("ran %v counts %v", ran.get(), port.counts())
		}
	})
	t.Run("after revocation, at once", func(t *testing.T) {
		port, ran := newPrincipalPort(), &principalRan{}
		gate, byName, _ := delegatedGate(t, port, principalTools(ran, nil, nil), 0)
		if _, err := callThrough(gate, byName, "notes__read_note", `{}`); err != nil {
			t.Fatal(err)
		}
		port.set(`{"admission":"refuse","refusal":"revoked"}`)
		if _, err := callThrough(gate, byName, "notes__read_note", `{}`); err == nil {
			t.Fatal("ran after revocation")
		}
		wantRefusal(t, lastRefusal(t, gate), "revoked", "")
		if !sameStrings(ran.get(), []string{"read_note"}) {
			t.Fatalf("ran %v", ran.get())
		}
	})
	t.Run("past the calls limit the issuing host counts", func(t *testing.T) {
		port, ran := newPrincipalPort(), &principalRan{}
		port.answer = `{"admission":"refuse","refusal":"out-of-scope","reason":"limit"}`
		gate, byName, _ := delegatedGate(t, port, principalTools(ran, nil, nil), 0)
		if _, err := callThrough(gate, byName, "notes__read_note", `{}`); err == nil {
			t.Fatal("ran")
		}
		wantRefusal(t, lastRefusal(t, gate), "out-of-scope", "limit")
	})
	t.Run("an admission that cannot be read", func(t *testing.T) {
		port, ran := newPrincipalPort(), &principalRan{}
		port.answer = "throw"
		gate, byName, _ := delegatedGate(t, port, principalTools(ran, nil, nil), 0)
		if _, err := callThrough(gate, byName, "notes__read_note", `{}`); err == nil {
			t.Fatal("ran")
		}
		wantRefusal(t, lastRefusal(t, gate), "admission-unknown", "")
	})
	t.Run("a gateway source against its gateway's entry without counting; one with no mapping refused", func(t *testing.T) {
		port, ran := newPrincipalPort(), &principalRan{}
		gate, byName, _ := delegatedGate(t, port, principalTools(ran, nil, nil), 0)
		if _, err := callThrough(gate, byName, "tickets__query", `{}`); err != nil {
			t.Fatal(err)
		}
		if read := port.reads[0]; read.Audience != (PrincipalAudience{"gateway", "gateway:test"}) || read.Call.Source != "tickets/live" || read.Call.Count {
			t.Fatalf("read %+v", read)
		}
		gate.TakeNotices()
		if _, err := callThrough(gate, byName, "records__lookup", `{}`); err == nil {
			t.Fatal("ran")
		}
		wantRefusal(t, lastRefusal(t, gate), "out-of-scope", "source")
		// A gateway source with no mapping names no audience: nothing is read for it.
		if len(port.counts()) != 1 {
			t.Fatalf("reads %v", port.counts())
		}
		if !sameStrings(ran.get(), []string{"query"}) {
			t.Fatalf("ran %v", ran.get())
		}
	})
	t.Run("never puts a call the delegation refuses to the person", func(t *testing.T) {
		port, ran := newPrincipalPort(), &principalRan{}
		asked := 0
		gate, _, _ := delegatedGate(t, port, principalTools(ran, map[string]ApprovalPolicy{"delete_note": func(ApprovalRequest) string { asked++; return "ask" }}, nil), 0)
		if answer, _, _ := gate.Decide(context.Background(), "notes__delete_note", json.RawMessage(`{}`), "call-1", true); answer != "checked" || asked != 0 {
			t.Fatalf("answer %s asked %d", answer, asked)
		}
		if !sameBools(port.counts(), []bool{false}) {
			t.Fatalf("counts %v", port.counts())
		}
	})
	t.Run("a revocation after the check and before the call refuses it", func(t *testing.T) {
		port, ran := newPrincipalPort(), &principalRan{}
		gate, byName, _ := delegatedGate(t, port, principalTools(ran, nil, nil), 0)
		if answer, _, _ := gate.Decide(context.Background(), "notes__write_note", json.RawMessage(`{}`), "call-2", true); answer != "allow" {
			t.Fatalf("answer %s", answer)
		}
		port.set(`{"admission":"refuse","refusal":"revoked"}`)
		if _, err := gate.Call(WithCallID(context.Background(), "call-2"), "notes__write_note", json.RawMessage(`{}`), byName["notes__write_note"].Execute); err == nil {
			t.Fatal("ran after revocation")
		}
		if len(ran.get()) != 0 || !sameBools(port.counts(), []bool{false, true}) {
			t.Fatalf("ran %v counts %v", ran.get(), port.counts())
		}
	})
	t.Run("no authority past the delegation on an approval's continuation", func(t *testing.T) {
		port, ran := newPrincipalPort(), &principalRan{}
		book := &principalBook{}
		gate, byName, _ := delegatedGate(t, port, principalTools(ran, map[string]ApprovalPolicy{"write_note": func(ApprovalRequest) string { return "ask" }}, nil), 0)
		gate.SetApprovals(book)
		if answer, _, _ := gate.Decide(context.Background(), "notes__write_note", json.RawMessage(`{"text":"x"}`), "call-3", true); answer != "ask" {
			t.Fatalf("answer %s", answer)
		}
		port.set(`{"admission":"refuse","refusal":"revoked"}`)
		record := *book.current
		record.State = "approved"
		_, ranIt, err := gate.Approved(context.Background(), record, byName["notes__write_note"].Execute)
		if err != nil || ranIt || len(ran.get()) != 0 || !sameBools(port.counts(), []bool{false, true}) {
			t.Fatalf("ran %v %v counts %v err %v", ranIt, ran.get(), port.counts(), err)
		}
	})
	t.Run("an effect already dispatched finishes when a revocation lands; the next call is refused", func(t *testing.T) {
		port, ran := newPrincipalPort(), &principalRan{}
		release, started := make(chan struct{}), make(chan struct{})
		gate, byName, _ := delegatedGate(t, port, principalTools(ran, nil, map[string]func(context.Context, json.RawMessage) (ToolResult, error){"read_note": func(context.Context, json.RawMessage) (ToolResult, error) {
			ran.add("read_note")
			close(started)
			<-release
			return ResultOf([]Content{TextContent("done")}, nil), nil
		}}), 0)
		done := make(chan error, 1)
		go func() { _, err := callThrough(gate, byName, "notes__read_note", `{}`); done <- err }()
		<-started
		port.set(`{"admission":"refuse","refusal":"revoked"}`)
		close(release)
		if err := <-done; err != nil {
			t.Fatalf("the dispatched call: %v", err)
		}
		if _, err := callThrough(gate, byName, "notes__read_note", `{}`); err == nil {
			t.Fatal("ran after revocation")
		}
		if !sameStrings(ran.get(), []string{"read_note"}) {
			t.Fatalf("ran %v", ran.get())
		}
	})
	t.Run("a delegated child's gate admits under its parent's principal", func(t *testing.T) {
		port, ran := newPrincipalPort(), &principalRan{}
		parent, _, _ := delegatedGate(t, port, principalTools(ran, nil, nil), 0)
		out, err := DeclareSources(principalTools(ran, nil, nil), nil)
		if err != nil {
			t.Fatal(err)
		}
		rows := &[]contract.ProvenanceSourceFigures{}
		policy := Defaults
		child := NewSourceGate(out, &policy, sourceMarker, "json-schema", func() *[]contract.ProvenanceSourceFigures { return rows }, parent)
		if child.Principal() != parent.Principal() {
			t.Fatal("the child holds another principal")
		}
		shown, _, _ := OfferSources(out.HostTools, out.Entries, policy, sourceMarker)
		for _, tool := range shown {
			if tool.Name == "notes__delete_note" {
				if _, err := child.Call(context.Background(), tool.Name, json.RawMessage(`{}`), tool.Execute); err == nil {
					t.Fatal("ran")
				}
			}
		}
		if len(ran.get()) != 0 {
			t.Fatalf("ran %v", ran.get())
		}
	})
	t.Run("an undelegated gate asks no admission", func(t *testing.T) {
		ran := &principalRan{}
		gate, byName, _ := sourceGate(t, principalTools(ran, nil, nil), Defaults, false)
		if gate.Principal() != nil {
			t.Fatal("a principal")
		}
		if _, err := callThrough(gate, byName, "notes__delete_note", `{}`); err != nil {
			t.Fatal(err)
		}
		if !sameStrings(ran.get(), []string{"delete_note"}) {
			t.Fatalf("ran %v", ran.get())
		}
	})
}
