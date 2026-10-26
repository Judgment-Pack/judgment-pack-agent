package runcontext

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

type receiptVector struct {
	Authority, Session, Tool string
	Args                     json.RawMessage
	Artifact                 map[string]any
	Adapter                  GatewayAdapterIdentity
	Cases                    []struct{ Name, Change string }
}

func receiptVectors(t *testing.T) receiptVector {
	t.Helper()
	raw, e := os.ReadFile("../../contract/testdata/taxonomy/gateway-receipt-v3.json")
	if e != nil {
		t.Fatal(e)
	}
	var v receiptVector
	if e = json.Unmarshal(raw, &v); e != nil {
		t.Fatal(e)
	}
	return v
}

// Test canonicalization is independent of the consumer implementation. Fixtures use ASCII keys.
func receiptJSON(v any) []byte {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	var tree any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&tree); err != nil {
		panic(err)
	}
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	if err := e.Encode(tree); err != nil {
		panic(err)
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n"))
}
func receiptHash(v []byte) string { h := sha256.Sum256(v); return "sha256:" + hex.EncodeToString(h[:]) }
func testCommit(label string, v any) string {
	return receiptHash(append(make([]byte, 32), append([]byte(label), receiptJSON(v)...)...))
}
func receiptSnapshot(endpoint *string) []byte {
	root := map[string]any{"snapshotVersion": "2", "operation": "live", "platform": "tickets", "binding": "tickets@sha256:" + strings.Repeat("a", 64), "capturedAt": "2026-10-25T00:00:00Z", "policy": 1, "server": map[string]string{"name": "fixture", "version": "1"}, "tools": map[string]any{"query": map[string]any{"taxonomy": map[string]string{"operation": "read", "effect": "read-external"}}, "sibling": map[string]any{"taxonomy": map[string]string{"operation": "read", "effect": "read-external"}}}}
	if endpoint != nil {
		root["endpoint"] = *endpoint
	}
	return receiptJSON(root)
}
func receiptApproval(endpoint *string) *DescriptorApproval {
	return &DescriptorApproval{Platform: "tickets", Binding: "tickets@sha256:" + strings.Repeat("a", 64), Operation: "live", Endpoint: endpoint, ToolTaxonomies: map[string]*ToolTaxonomy{"query": {Operation: "read", Effect: "read-external"}, "sibling": {Operation: "read", Effect: "read-external"}}}
}
func receiptSign(receipt map[string]any, key ed25519.PrivateKey) {
	delete(receipt, "signature")
	receipt["signature"] = hex.EncodeToString(ed25519.Sign(key, append([]byte("judgment-pack-gateway/receipt/3:"), receiptJSON(receipt)...)))
}
func receiptOuter(t *testing.T, envelope map[string]any) ToolResult {
	t.Helper()
	v, e := ParseJSON(receiptJSON(map[string]any{"content": []any{map[string]string{"type": "text", "text": string(receiptJSON(envelope))}}, "structuredContent": envelope}))
	if e != nil {
		t.Fatal(e)
	}
	return ReadToolResult(v)
}
func syntheticReceipt(t *testing.T, v receiptVector, endpoint *string) (map[string]any, *GatewayReceiptTrust, ed25519.PrivateKey) {
	t.Helper()
	key := ed25519.NewKeyFromSeed(make([]byte, 32))
	pub := key.Public().(ed25519.PublicKey)
	var args any
	json.Unmarshal(v.Args, &args)
	call := map[string]any{"tool": v.Tool, "arguments": args}
	acq := map[string]any{"shape": "mcp", "statement": testCommit("statement:", string(receiptJSON(call))), "adapter": v.Adapter, "endpoint": nil, "snapshot": nil, "peerIdentity": nil, "schema": nil, "upstreamToken": nil, "observedAt": "2026-10-25T00:00:00Z"}
	if endpoint != nil {
		acq["endpoint"] = *endpoint
		acq["transport"] = "http"
		acq["server"] = map[string]string{"name": "fixture", "version": "1"}
	}
	receipt := map[string]any{"receiptVersion": "3", "sessionId": v.Session, "callIndex": 0, "prevSignature": nil, "kind": "acquisition", "authority": v.Authority, "keyId": receiptHash(pub)[7:39], "source": "tickets/live", "argumentsCommitment": testCommit("args:", call), "resultDigest": receiptHash(receiptJSON(v.Artifact)), "acquisition": acq}
	receiptSign(receipt, key)
	return map[string]any{"session": v.Session, "result": v.Artifact, "receipt": receipt, "salts": map[string]string{"args": strings.Repeat("0", 64), "statement": strings.Repeat("0", 64)}}, &GatewayReceiptTrust{PublicKey: hex.EncodeToString(pub), Authority: v.Authority, Adapter: &v.Adapter}, key
}
func TestGatewayReceiptSharedRefusals(t *testing.T) {
	v := receiptVectors(t)
	endpoint := "https://example.invalid/mcp"
	snapshot := receiptSnapshot(&endpoint)
	approval := receiptApproval(&endpoint)
	for _, remote := range []bool{true, false} {
		t.Run(fmt.Sprint("remote=", remote), func(t *testing.T) {
			ep := &endpoint
			if !remote {
				ep = nil
			}
			snapshot = receiptSnapshot(ep)
			approval = receiptApproval(ep)
			envelope, trust, _ := syntheticReceipt(t, v, ep)
			result, _, err := VerifyGatewayResult(context.Background(), receiptOuter(t, envelope), trust, approval, snapshot, v.Tool, v.Args, GatewayExecutionContext{v.Session}, GatewayReceiptEnvelopeBytes)
			if err != nil || string(result.JSON()) == "{}" {
				t.Fatal("valid receipt refused", err)
			}
		})
	}
	snapshot = receiptSnapshot(&endpoint)
	approval = receiptApproval(&endpoint)
	for _, row := range v.Cases {
		t.Run(row.Name, func(t *testing.T) {
			envelope, trust, key := syntheticReceipt(t, v, &endpoint)
			r := envelope["receipt"].(map[string]any)
			a := r["acquisition"].(map[string]any)
			args := v.Args
			limit := GatewayReceiptEnvelopeBytes
			switch row.Change {
			case "signature":
				r["signature"] = strings.Repeat("0", 128)
			case "key":
				trust.PublicKey = strings.Repeat("1", 64)
			case "keyId":
				r["keyId"] = strings.Repeat("0", 32)
				receiptSign(r, key)
			case "authority":
				r["authority"] = "gateway:other"
				receiptSign(r, key)
			case "source":
				r["source"] = "tickets/write"
				receiptSign(r, key)
			case "session":
				r["sessionId"] = "agent-" + strings.Repeat("1", 64)
				envelope["session"] = r["sessionId"]
				receiptSign(r, key)
			case "index":
				r["callIndex"] = 1
				receiptSign(r, key)
			case "previous":
				r["prevSignature"] = strings.Repeat("1", 128)
				receiptSign(r, key)
			case "args":
				args = json.RawMessage(`{"id":8}`)
			case "result":
				envelope["result"] = map[string]any{"content": []any{map[string]string{"type": "text", "text": "other"}}}
			case "statement":
				a["statement"] = "sha256:" + strings.Repeat("0", 64)
				receiptSign(r, key)
			case "endpoint":
				a["endpoint"] = "https://other.invalid/mcp"
				receiptSign(r, key)
			case "server":
				a["server"] = map[string]string{"name": "other", "version": "1"}
				receiptSign(r, key)
			case "adapter":
				a["adapter"] = map[string]string{"name": "other", "version": "0", "digest": v.Adapter.Digest}
				receiptSign(r, key)
			case "action":
				r["kind"] = "action"
				receiptSign(r, key)
			case "number":
				envelope["result"] = map[string]any{"n": json.Number("9007199254740992")}
			case "oversize":
				limit = 10
			}
			raw := receiptOuter(t, envelope)
			if row.Change == "twins" {
				raw.StructuredContent = ObjectOf("different", Bool(true))
			}
			if row.Change == "duplicate" {
				raw.Content = []Content{TextContent(strings.Replace(raw.Content[0].Text(), `"session":`, `"session":"duplicate","session":`, 1))}
			}
			if _, _, err := VerifyGatewayResult(context.Background(), raw, trust, approval, snapshot, v.Tool, args, GatewayExecutionContext{v.Session}, limit); err == nil {
				t.Fatalf("receipt safeguard admitted %s", row.Name)
			}
		})
	}
	envelope, trust, _ := syntheticReceipt(t, v, &endpoint)
	raw := receiptOuter(t, envelope)
	for _, limit := range []int{0, -1, GatewayReceiptEnvelopeBytes + 1, int(^uint(0) >> 1)} {
		if _, _, e := VerifyGatewayResult(context.Background(), raw, trust, approval, snapshot, v.Tool, v.Args, GatewayExecutionContext{v.Session}, limit); e == nil {
			t.Fatal("invalid limit admitted")
		}
	}
	for _, a := range []*DescriptorApproval{nil, {}, {ToolTaxonomies: map[string]*ToolTaxonomy{"query": nil}}} {
		if _, _, e := VerifyGatewayResult(context.Background(), raw, trust, a, snapshot, v.Tool, v.Args, GatewayExecutionContext{v.Session}, GatewayReceiptEnvelopeBytes); e == nil {
			t.Fatal("malformed public approval admitted")
		}
	}
	for _, session := range []string{"", "agent-", "unbound"} {
		if _, _, e := VerifyGatewayResult(context.Background(), raw, trust, approval, snapshot, v.Tool, v.Args, GatewayExecutionContext{session}, GatewayReceiptEnvelopeBytes); e == nil {
			t.Fatal("missing invocation admitted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, e := VerifyGatewayResult(ctx, raw, trust, approval, snapshot, v.Tool, v.Args, GatewayExecutionContext{v.Session}, GatewayReceiptEnvelopeBytes); e == nil {
		t.Fatal("cancelled verification admitted")
	}
}

type liveReceiptFixture struct {
	URL, SnapshotText, SnapshotDigest, Tool string
	Trust                                   GatewayReceiptTrust
	Approval                                DescriptorApproval
	Stdio                                   *liveReceiptFixture
}

func liveReceipt(t *testing.T) liveReceiptFixture {
	t.Helper()
	path := os.Getenv("JP_AGENT_GATEWAY_FIXTURE")
	if path == "" {
		t.Skip("actual frozen Gateway producer not requested")
	}
	raw, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	var v liveReceiptFixture
	if e = json.Unmarshal(raw, &v); e != nil {
		t.Fatal(e)
	}
	return v
}
func callLiveGateway(ctx context.Context, v liveReceiptFixture, args json.RawMessage, session string) (ToolResult, error) {
	client := &http.Client{Timeout: 3 * time.Second}
	transport := ""
	request := func(body any) (map[string]json.RawMessage, error) {
		req, e := http.NewRequestWithContext(ctx, "POST", v.URL, bytes.NewReader(receiptJSON(body)))
		if e != nil {
			return nil, e
		}
		req.Header.Set("Content-Type", "application/json")
		if transport != "" {
			req.Header.Set("Mcp-Session-Id", transport)
		}
		response, e := client.Do(req)
		if e != nil {
			return nil, e
		}
		defer response.Body.Close()
		if transport == "" {
			transport = response.Header.Get("Mcp-Session-Id")
		}
		raw, e := io.ReadAll(io.LimitReader(response.Body, GatewayReceiptEnvelopeBytes*4))
		if e != nil {
			return nil, e
		}
		if response.StatusCode != 200 && response.StatusCode != 202 {
			return nil, fmt.Errorf("frontend status %d: %s", response.StatusCode, raw)
		}
		var out map[string]json.RawMessage
		if len(raw) > 0 {
			e = json.Unmarshal(raw, &out)
		}
		return out, e
	}
	if _, e := request(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "agent-receipt-test", "version": "1"}}}); e != nil {
		return ToolResult{}, e
	}
	if _, e := request(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}); e != nil {
		return ToolResult{}, e
	}
	var arguments any
	json.Unmarshal(args, &arguments)
	params := map[string]any{"name": v.Approval.Platform + "." + v.Tool, "arguments": arguments}
	if session != "" {
		params["_meta"] = map[string]string{"io.judgment-pack/session": session}
	}
	out, e := request(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": params})
	if e != nil {
		return ToolResult{}, e
	}
	result, e := ParseJSON(out["result"])
	if e != nil {
		return ToolResult{}, e
	}
	return ReadToolResult(result), nil
}
func gatewaySource(t *testing.T, v liveReceiptFixture, run func(context.Context, json.RawMessage) (ToolResult, error), policy Policy) (*SourceGate, HostTool, *[]contract.ProvenanceSourceFigures) {
	t.Helper()
	tool := HostTool{Name: v.Tool, Taxonomy: v.Approval.ToolTaxonomies[v.Tool], Description: "approved", InputSchema: json.RawMessage(`{"type":"object"}`), Execute: run, Source: &ToolSource{Kind: "mcp", ID: "tickets", Trust: "connected", ToolListDigest: v.SnapshotDigest, DescriptorSnapshot: []byte(v.SnapshotText), DescriptorApproval: &v.Approval, GatewayReceipt: &v.Trust, VerifyReceipt: func(context.Context, ReceiptEvidence) (bool, error) {
		t.Error("custom fallback ran after Gateway verification failure")
		return true, nil
	}, CurrentDescriptorSnapshot: func(context.Context) ([]byte, error) { return []byte(v.SnapshotText), nil }}}
	declared, e := DeclareSources([]HostTool{tool}, nil)
	if e != nil || len(declared.HostTools) != 1 {
		t.Fatalf("real producer admission: %v", e)
	}
	rows := []contract.ProvenanceSourceFigures{}
	gate := NewSourceGate(declared, &policy, "marker", "json-schema", func() *[]contract.ProvenanceSourceFigures { return &rows }, nil)
	return gate, declared.HostTools[0], &rows
}
func TestGatewayReceiptActualProducerSourceGate(t *testing.T) {
	fixtures := liveReceipt(t)
	if fixtures.Stdio == nil {
		t.Fatal("actual stdio producer fixture missing")
	}
	for name, v := range map[string]liveReceiptFixture{"remote": fixtures, "stdio": *fixtures.Stdio} {
		t.Run(name, func(t *testing.T) { testGatewayReceiptActualProducerSourceGate(t, v) })
	}
}
func testGatewayReceiptActualProducerSourceGate(t *testing.T, v liveReceiptFixture) {
	ctx := context.Background()
	args := json.RawMessage(`{"id":7}`)
	var old ToolResult
	var sessions []string
	var mu sync.Mutex
	parent := context.WithValue(ctx, struct{}{}, "retained")
	run := func(ctx context.Context, args json.RawMessage) (ToolResult, error) {
		inv, ok := GatewayInvocation(ctx)
		if !ok || ctx.Value(struct{}{}) != "retained" {
			return ToolResult{}, errors.New("invocation context missing")
		}
		mu.Lock()
		sessions = append(sessions, inv.ReceiptSession)
		mu.Unlock()
		raw, e := callLiveGateway(ctx, v, args, inv.ReceiptSession)
		old = raw
		return raw, e
	}
	gate, offered, rows := gatewaySource(t, v, run, Defaults)
	if _, e := gate.Call(parent, offered.Name, args, offered.Execute); e != nil {
		t.Fatal("actual producer receipt refused", e)
	}
	// The real signature is also checked against independently wrong key/binding/arguments/result.
	for _, kind := range []string{"key", "binding", "args", "result"} {
		trust := v.Trust
		approval := v.Approval
		given := args
		raw := old.Clone()
		switch kind {
		case "key":
			trust.PublicKey = strings.Repeat("1", 64)
		case "binding":
			approval.Binding = "other@sha256:" + strings.Repeat("0", 64)
		case "args":
			given = json.RawMessage(`{"id":8}`)
		case "result":
			var e map[string]any
			json.Unmarshal([]byte(old.Content[0].Text()), &e)
			e["result"] = map[string]any{"content": []any{map[string]string{"type": "text", "text": "changed"}}}
			raw = receiptOuter(t, e)
		}
		if _, _, err := VerifyGatewayResult(ctx, raw, &trust, &approval, []byte(v.SnapshotText), v.Tool, given, GatewayExecutionContext{sessions[0]}, GatewayReceiptEnvelopeBytes); err == nil {
			t.Fatalf("real producer accepted different %s", kind)
		}
	}
	if _, ok := GatewayInvocation(parent); ok {
		t.Fatal("parent context mutated")
	}
	if _, e := gate.Call(parent, offered.Name, args, func(context.Context, json.RawMessage) (ToolResult, error) { return old, nil }); e == nil || !strings.Contains(e.Error(), "receipt verification failed") {
		t.Fatal("prior acquisition replay admitted", e)
	}
	missing := func(ctx context.Context, args json.RawMessage) (ToolResult, error) {
		return callLiveGateway(ctx, v, args, "")
	}
	if _, e := gate.Call(parent, offered.Name, args, missing); e == nil || !strings.Contains(e.Error(), "receipt verification failed") {
		t.Fatal("missing session mapping admitted", e)
	}
	for _, row := range *rows {
		if row.Source == "tickets" && (row.Calls != 3 || row.Refused != 2) {
			t.Fatalf("unknown calls lost accounting: %+v", row)
		}
	}
	// Invalid receipt deadlines remain unknown once execution returned; no automatic retry.
	policy := Defaults
	policy.SourceCallMilliseconds = 5
	gate, offered, _ = gatewaySource(t, v, func(ctx context.Context, args json.RawMessage) (ToolResult, error) {
		time.Sleep(10 * time.Millisecond)
		return old, nil
	}, policy)
	if _, e := gate.Call(parent, offered.Name, args, offered.Execute); e == nil {
		t.Fatal("expired source call admitted")
	}
	_ = sessions
}
func TestGatewayReceiptActualProducerConcurrentSwap(t *testing.T) {
	v := liveReceipt(t)
	parent := context.Background()
	args := json.RawMessage(`{"id":7}`)
	// Two concurrent same-argument acquisitions must not accept each other's freshly signed receipts.
	gate, offered, _ := gatewaySource(t, v, nil, Defaults)
	arrived := make(chan struct{}, 2)
	release := make(chan struct{})
	var held [2]ToolResult
	var sessions2 [2]string
	var errs [2]error
	completed := make(chan int, 2)
	for i := 0; i < 2; i++ {
		go func(i int) {
			_, errs[i] = gate.Call(parent, offered.Name, args, func(ctx context.Context, args json.RawMessage) (ToolResult, error) {
				inv, _ := GatewayInvocation(ctx)
				sessions2[i] = inv.ReceiptSession
				raw, e := callLiveGateway(ctx, v, args, inv.ReceiptSession)
				if e != nil {
					return ToolResult{}, e
				}
				held[i] = raw
				arrived <- struct{}{}
				<-release
				return held[1-i], nil
			})
			completed <- i
		}(i)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-arrived:
		case <-time.After(5 * time.Second):
			t.Fatal("concurrent producer did not answer")
		}
	}
	close(release)
	for i := 0; i < 2; i++ {
		select {
		case <-completed:
		case <-time.After(2 * time.Second):
			t.Fatal("concurrent verification did not finish")
		}
	}
	if sessions2[0] == sessions2[1] || sessions2[0] == "" {
		t.Fatal("concurrent invocations share receipt session")
	}
	for _, e := range errs {
		if e == nil || !strings.Contains(e.Error(), "receipt verification failed") {
			t.Fatal("concurrent same-args receipt swap admitted", e)
		}
	}
}
func TestGatewayReceiptAcquisitionAdmission(t *testing.T) {
	v := receiptVectors(t)
	endpoint := "https://example.invalid/mcp"
	_, trust, _ := syntheticReceipt(t, v, &endpoint)
	tool := taxonomyLocal()
	tool.Taxonomy = &ToolTaxonomy{Operation: "act", Effect: "act"}
	tool.Source = &ToolSource{Kind: "mcp", ID: "tickets", Trust: "connected", ToolListDigest: receiptHash(receiptSnapshot(&endpoint)), DescriptorSnapshot: receiptSnapshot(&endpoint), DescriptorApproval: receiptApproval(&endpoint), GatewayReceipt: trust, CurrentDescriptorSnapshot: func(context.Context) ([]byte, error) { return receiptSnapshot(&endpoint), nil }}
	if _, e := DeclareSources([]HostTool{tool}, nil); e == nil || !strings.Contains(e.Error(), "requires read-external") {
		t.Fatal("acquisition-only verifier admitted on action", e)
	}
}

func TestGatewayReceiptSnapshotGuardAndRestoredAction(t *testing.T) {
	v := receiptVectors(t)
	endpoint := "https://example.invalid/mcp"
	_, trust, _ := syntheticReceipt(t, v, &endpoint)
	snapshot := receiptSnapshot(&endpoint)
	current := snapshot
	calls := 0
	tool := HostTool{Name: v.Tool, Taxonomy: &ToolTaxonomy{Operation: "read", Effect: "read-external"}, Description: "approved", InputSchema: json.RawMessage(`{"type":"object"}`), Execute: func(context.Context, json.RawMessage) (ToolResult, error) { calls++; return ToolResult{}, nil }, Source: &ToolSource{Kind: "mcp", ID: "tickets", Trust: "connected", ToolListDigest: receiptHash(snapshot), DescriptorSnapshot: snapshot, DescriptorApproval: receiptApproval(&endpoint), GatewayReceipt: trust, CurrentDescriptorSnapshot: func(context.Context) ([]byte, error) { return current, nil }}}
	declared, err := DeclareSources([]HostTool{tool}, nil)
	if err != nil {
		t.Fatal(err)
	}
	rows := []contract.ProvenanceSourceFigures{}
	policy := Defaults
	gate := NewSourceGate(declared, &policy, "marker", "json-schema", func() *[]contract.ProvenanceSourceFigures { return &rows }, nil)
	offered := declared.HostTools[0]
	// The selected subset is one leaf; an unselected sibling's taxonomy still belongs to the byte pin.
	current = bytes.Replace(snapshot, []byte(`"sibling":{"taxonomy":{"effect":"read-external","operation":"read"}}`), []byte(`"sibling":{"taxonomy":{"effect":"act","operation":"act"}}`), 1)
	if bytes.Equal(current, snapshot) {
		t.Fatal("sibling mutation not applied")
	}
	if _, err := gate.Call(context.Background(), offered.Name, json.RawMessage(`{}`), offered.Execute); err == nil || calls != 0 {
		t.Fatal("full snapshot sibling drift reached executor", err, calls)
	}
	current = snapshot
	declared, err = DeclareSources([]HostTool{tool}, nil)
	if err != nil {
		t.Fatal(err)
	}
	gate = NewSourceGate(declared, &policy, "marker", "json-schema", func() *[]contract.ProvenanceSourceFigures { return &rows }, nil)
	entry := gate.Entries[offered.Name]
	entry.EffectClass = "act"
	entry.Approval = "required"
	entry.Declared.Taxonomy = &ToolTaxonomy{Operation: "act", Effect: "act"}
	if _, err := gate.Call(context.Background(), offered.Name, json.RawMessage(`{}`), offered.Execute); err == nil || calls != 0 {
		t.Fatal("restored acquisition-only action reached executor", err, calls)
	}
	record := contract.CheckpointApproval{Name: offered.Name, Source: entry.Record, CallID: "restored", State: "approved"}
	if _, ran, err := gate.Approved(context.Background(), record, offered.Execute); err != nil || ran || calls != 0 {
		t.Fatal("approved acquisition-only action reached executor", err, ran, calls)
	}
}
