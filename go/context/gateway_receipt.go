package runcontext

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
)

const GatewayReceiptEnvelopeBytes = (8 << 20) + 4096

// GatewayReceiptTrust is provisioned independently of any response or model input.
type GatewayReceiptTrust struct {
	PublicKey string                  `json:"publicKey"`
	Authority string                  `json:"authority"`
	Adapter   *GatewayAdapterIdentity `json:"adapter,omitempty"`
}

// GatewayAdapterIdentity is an optional independent pin; omission leaves adapter identity unverified.
type GatewayAdapterIdentity struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Digest  string `json:"digest"`
}
type gatewayInvocationKey struct{}

// GatewayExecutionContext is the package-created per-acquisition receipt session.
type GatewayExecutionContext struct{ ReceiptSession string }

// GatewayInvocation returns a detached executor context, or false outside an admitted Gateway call.
func GatewayInvocation(ctx context.Context) (GatewayExecutionContext, bool) {
	v, ok := ctx.Value(gatewayInvocationKey{}).(GatewayExecutionContext)
	return v, ok
}
func beginGatewayInvocation(ctx context.Context) (context.Context, GatewayExecutionContext, error) {
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return ctx, GatewayExecutionContext{}, err
	}
	v := GatewayExecutionContext{ReceiptSession: "agent-" + hex.EncodeToString(nonce)}
	return context.WithValue(ctx, gatewayInvocationKey{}, v), v, nil
}
func GatewayTrustProblem(trust *GatewayReceiptTrust) string {
	if trust == nil || trust.Adapter != nil && (trust.Adapter.Name == "" || U16Length(trust.Adapter.Name) > 2048 || U16Length(trust.Adapter.Version) > 256 || !strings.HasPrefix(trust.Adapter.Digest, "sha256:") || !gatewayHex(strings.TrimPrefix(trust.Adapter.Digest, "sha256:"), 32)) || len(trust.Authority) == 0 || U16Length(trust.Authority) > 256 || !gatewayHex(trust.PublicKey, 32) {
		return "invalid pinned Gateway receipt trust"
	}
	return ""
}
func gatewayHex(s string, n int) bool {
	if len(s) != n*2 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func gatewayDigest(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}
func gatewayCommitment(salt any, label string, v any) (string, error) {
	text, ok := salt.(string)
	if !ok || !gatewayHex(text, 32) {
		return "", errors.New("invalid salt")
	}
	raw, _ := hex.DecodeString(text)
	return gatewayDigest(append(append(raw, []byte(label)...), []byte(descriptorCanonical(v))...)), nil
}
func gatewayReceiptError() error { return errors.New("Gateway receipt verification failed") }

// VerifyGatewayResult verifies signed receipt fields and exact artifact/call; descriptor taxonomy is separately approved.
func VerifyGatewayResult(ctx context.Context, raw ToolResult, trust *GatewayReceiptTrust, approval *DescriptorApproval, snapshotBytes []byte, tool string, args json.RawMessage, invocation GatewayExecutionContext, maxBytes int) (ToolResult, string, error) {
	fail := func() (ToolResult, string, error) { return ToolResult{}, "", gatewayReceiptError() }
	if ctx == nil || ctx.Err() != nil || GatewayTrustProblem(trust) != "" || approval == nil || invocation.ReceiptSession == "" || len(invocation.ReceiptSession) != 70 || !strings.HasPrefix(invocation.ReceiptSession, "agent-") || !gatewayHex(strings.TrimPrefix(invocation.ReceiptSession, "agent-"), 32) || maxBytes <= 0 || maxBytes > GatewayReceiptEnvelopeBytes || tool == "" || U16Length(tool) > 128 || len(args) > 1<<20 {
		return fail()
	}
	snapshot, err := ReadDescriptorSnapshot(snapshotBytes)
	if err != nil || DescriptorApprovalProblem(snapshot, approval, tool, approval.ToolTaxonomies[tool]) != "" {
		return fail()
	}
	// Bound the supplied tree before serialization; a caller may bypass the transport parser.
	if len(raw.Content) != 1 || raw.Content[0].Type() != "text" || len(raw.Content[0].Text()) > maxBytes || len(raw.additional) != 0 {
		return fail()
	}
	budget := 4*maxBytes + 1024
	if !gatewayBoundedValue(raw.Content[0].Value(), &budget, 0) || !gatewayBoundedValue(raw.StructuredContent, &budget, 0) {
		return fail()
	}
	rawJSON := raw.JSON()
	if len(rawJSON) > 4*maxBytes+1024 {
		return fail()
	}
	value, err := descriptorJSON(rawJSON, true)
	if err != nil {
		return fail()
	}
	outer, ok := value.(map[string]any)
	if !ok || !descriptorMembers(outer, []string{"content", "structuredContent"}, []string{"isError"}) || outer["isError"] == true {
		return fail()
	}
	content, ok := outer["content"].([]any)
	if !ok || len(content) != 1 {
		return fail()
	}
	part, ok := content[0].(map[string]any)
	if !ok || !descriptorMembers(part, []string{"type", "text"}, nil) || part["type"] != "text" {
		return fail()
	}
	text, ok := part["text"].(string)
	if !ok || len(text) > maxBytes {
		return fail()
	}
	parsed, err := descriptorJSON([]byte(text), true)
	if err != nil {
		return fail()
	}
	envelope, ok := parsed.(map[string]any)
	if !ok || !descriptorMembers(envelope, []string{"session", "result", "receipt", "salts"}, nil) || descriptorCanonical(envelope) != descriptorCanonical(outer["structuredContent"]) {
		return fail()
	}
	receipt, rok := envelope["receipt"].(map[string]any)
	salts, sok := envelope["salts"].(map[string]any)
	artifact, aok := envelope["result"].(map[string]any)
	if !rok || !sok || !aok {
		return fail()
	}
	if _, present := artifact["receipt"]; present {
		return fail()
	}
	if envelope["session"] != invocation.ReceiptSession || receipt["sessionId"] != invocation.ReceiptSession || receipt["callIndex"] != int64(0) || receipt["prevSignature"] != nil || receipt["receiptVersion"] != "3" || receipt["kind"] != "acquisition" || receipt["authority"] != trust.Authority || receipt["source"] != approval.Platform+"/"+approval.Operation {
		return fail()
	}
	if _, present := receipt["action"]; present {
		return fail()
	}
	signature, ok := receipt["signature"].(string)
	if !ok || !gatewayHex(signature, 64) {
		return fail()
	}
	key, _ := hex.DecodeString(trust.PublicKey)
	sig, _ := hex.DecodeString(signature)
	if receipt["keyId"] != gatewayDigest(key)[7:39] {
		return fail()
	}
	unsigned := map[string]any{}
	for k, v := range receipt {
		if k != "signature" {
			unsigned[k] = v
		}
	}
	if !ed25519.Verify(key, []byte("judgment-pack-gateway/receipt/3:"+descriptorCanonical(unsigned)), sig) {
		return fail()
	}
	arguments, err := descriptorJSON(args, true)
	if _, ok := arguments.(map[string]any); err != nil || !ok {
		return fail()
	}
	call := map[string]any{"tool": tool, "arguments": arguments}
	commitment, err := gatewayCommitment(salts["args"], "args:", call)
	if err != nil || receipt["argumentsCommitment"] != commitment || receipt["resultDigest"] != gatewayDigest([]byte(descriptorCanonical(artifact))) {
		return fail()
	}
	acquisition, ok := receipt["acquisition"].(map[string]any)
	if !ok || acquisition["shape"] != "mcp" {
		return fail()
	}
	adapter, ok := acquisition["adapter"].(map[string]any)
	if !ok || !descriptorMembers(adapter, []string{"name", "version", "digest"}, nil) {
		return fail()
	}
	name, nok := adapter["name"].(string)
	version, vok := adapter["version"].(string)
	digest, dok := adapter["digest"].(string)
	if !nok || !vok || !dok || name == "" || U16Length(name) > 2048 || U16Length(version) > 256 || !strings.HasPrefix(digest, "sha256:") || !gatewayHex(strings.TrimPrefix(digest, "sha256:"), 32) {
		return fail()
	}
	if pin := trust.Adapter; pin != nil && (name != pin.Name || version != pin.Version || digest != pin.Digest) {
		return fail()
	}
	statement, err := gatewayCommitment(salts["statement"], "statement:", descriptorCanonical(call))
	if err != nil || acquisition["statement"] != statement {
		return fail()
	}
	if approval.Endpoint != nil {
		serverRaw, _ := json.Marshal(snapshot.Server)
		server, err := descriptorJSON(serverRaw, true)
		if err != nil || acquisition["endpoint"] != *approval.Endpoint || acquisition["transport"] != "http" || descriptorCanonical(acquisition["server"]) != descriptorCanonical(server) {
			return fail()
		}
	} else {
		if _, present := acquisition["transport"]; present {
			return fail()
		}
		if _, present := acquisition["server"]; present {
			return fail()
		}
	}
	// Parse original signed artifact bytes, without replacing numeric lexemes through float64.
	artifactValue, err := ParseJSON([]byte(descriptorCanonical(artifact)))
	if err != nil {
		return fail()
	}
	if content, present := artifact["content"]; present {
		if _, ok := content.([]any); !ok {
			return fail()
		}
	}
	if flag, present := artifact["isError"]; present {
		if _, ok := flag.(bool); !ok {
			return fail()
		}
	}
	if ctx.Err() != nil {
		return fail()
	}
	result := ReadToolResult(artifactValue)
	result.Receipt = signature
	return result, signature, nil
}

// gatewayBoundedValue limits supplied trees before JSON serialization, including cycles/depth.
func gatewayBoundedValue(v *Value, budget *int, depth int) bool {
	if v == nil {
		return true
	}
	if depth > 128 || *budget < 1 {
		return false
	}
	*budget -= 1
	if v.IsString() {
		*budget -= len(v.Str())
	} else if v.IsArray() {
		for _, item := range v.Items() {
			if !gatewayBoundedValue(item, budget, depth+1) {
				return false
			}
		}
	} else if v.IsObject() {
		for _, key := range v.Keys() {
			*budget -= len(key) + 3
			if !gatewayBoundedValue(v.Get(key), budget, depth+1) {
				return false
			}
		}
	} else {
		*budget -= 32
	}
	return *budget >= 0
}
