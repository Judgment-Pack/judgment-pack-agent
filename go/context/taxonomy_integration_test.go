package runcontext

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

type pairedGatewayVector struct {
	Binding, Platform, Operation, Endpoint, SnapshotText, SnapshotDigest string
	Tools                                                                map[string]*ToolTaxonomy
	Refusals                                                             []struct{ Name, Find, Replace string }
	Groups                                                               []struct {
		Name       string
		Tools      map[string]*ToolTaxonomy
		Taxonomy   *ToolTaxonomy
		Operations []string
	}
}

func pairedGateway(t *testing.T) pairedGatewayVector {
	t.Helper()
	raw, err := os.ReadFile("../../contract/testdata/taxonomy/gateway-v2.json")
	if err != nil {
		t.Fatal(err)
	}
	var v pairedGatewayVector
	if json.Unmarshal(raw, &v) != nil {
		t.Fatal("vector")
	}
	return v
}
func pairedApproval(v pairedGatewayVector) *DescriptorApproval {
	return &DescriptorApproval{Platform: v.Platform, Binding: v.Binding, Operation: v.Operation, Endpoint: &v.Endpoint, ToolTaxonomies: v.Tools}
}
func TestTaxonomyIntegrationGatewayV2SharedIdentity(t *testing.T) {
	v := pairedGateway(t)
	s, err := ReadDescriptorSnapshot([]byte(v.SnapshotText))
	if err != nil {
		t.Fatal(err)
	}
	digest, err := DescriptorSnapshotDigest([]byte(v.SnapshotText))
	if err != nil || digest != v.SnapshotDigest {
		t.Fatal("snapshot bytes")
	}
	if DescriptorApprovalProblem(s, pairedApproval(v), "search", v.Tools["search"]) != "" {
		t.Fatal("approved source refused")
	}
	for _, r := range v.Refusals {
		t.Run(r.Name, func(t *testing.T) {
			changed := strings.Replace(v.SnapshotText, r.Find, r.Replace, 1)
			s, err := ReadDescriptorSnapshot([]byte(changed))
			if err == nil && DescriptorApprovalProblem(s, pairedApproval(v), "", nil) == "" {
				t.Fatal("unapproved source identity admitted")
			}
		})
	}
	for _, r := range v.Groups {
		summary, profile, err := SummarizeTaxonomies(r.Tools)
		if err != nil || !reflect.DeepEqual(summary, r.Taxonomy) || profile.Effect != r.Taxonomy.Effect || !reflect.DeepEqual(profile.Operations, r.Operations) {
			t.Fatalf("strongest complete group %s: %v", r.Name, err)
		}
	}
}
func TestTaxonomyIntegrationGatewayV2ActualAdmission(t *testing.T) {
	v := pairedGateway(t)
	tool := HostTool{Name: "write", Taxonomy: v.Tools["write"], Description: "host constraint", InputSchema: json.RawMessage(`{"type":"object"}`), Execute: func(context.Context, json.RawMessage) (ToolResult, error) { return ToolResult{}, nil }, Source: &ToolSource{Kind: "mcp", ID: "tickets", Trust: "connected", ToolListDigest: v.SnapshotDigest, DescriptorSnapshot: []byte(v.SnapshotText), DescriptorApproval: pairedApproval(v), CurrentDescriptorSnapshot: func(context.Context) ([]byte, error) { return []byte(v.SnapshotText), nil }, VerifyReceipt: func(context.Context, ReceiptEvidence) (bool, error) { return false, nil }}}
	out, err := DeclareSources([]HostTool{tool}, nil)
	if err != nil || len(out.HostTools) != 1 {
		t.Fatalf("matching selected leaf refused: %v", err)
	}
	wrong := tool
	wrong.Taxonomy = &ToolTaxonomy{Operation: "read", Effect: "read-external"}
	if _, err := DeclareSources([]HostTool{wrong}, nil); err == nil {
		t.Fatal("changed selected taxonomy admitted")
	}
	missing := tool
	source := *tool.Source
	source.DescriptorApproval = nil
	missing.Source = &source
	if _, err := DeclareSources([]HostTool{missing}, nil); err == nil {
		t.Fatal("missing independent approval admitted")
	}
	for _, key := range []string{"operation", "platform", "binding", "endpoint", "owners"} {
		t.Run(key, func(t *testing.T) {
			source := *tool.Source
			approval := *source.DescriptorApproval
			source.DescriptorApproval = &approval
			changed := tool
			changed.Source = &source
			switch key {
			case "operation":
				approval.Operation = "write"
			case "platform":
				approval.Platform = "other"
			case "binding":
				approval.Binding = "other@sha256:" + strings.Repeat("0", 64)
			case "endpoint":
				endpoint := "https://other.test/mcp"
				approval.Endpoint = &endpoint
			case "owners":
				approval.ToolTaxonomies = map[string]*ToolTaxonomy{"search": v.Tools["search"]}
			}
			if _, err := DeclareSources([]HostTool{changed}, nil); err == nil {
				t.Fatal("changed independent identity admitted")
			}
		})
	}
}
