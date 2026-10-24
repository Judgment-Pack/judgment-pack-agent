package runcontext

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

type descriptorVector struct {
	SnapshotText  string `json:"snapshotText"`
	Digest        string `json:"digest"`
	ChangedText   string `json:"changedText"`
	ChangedDigest string `json:"changedDigest"`
	Refusal       struct {
		AgentCode       string `json:"agentCode"`
		AgentOutcome    string `json:"agentOutcome"`
		AgentDetail     string `json:"agentDetail"`
		GatewayContains string `json:"gatewayContains"`
	} `json:"refusal"`
	Invalid []struct {
		Name string `json:"name"`
		Old  string `json:"old"`
		New  string `json:"new"`
	} `json:"invalid"`
}

func snapshotVector(t *testing.T) descriptorVector {
	t.Helper()
	data, err := os.ReadFile("../../contract/vectors/tool-offer/descriptor-snapshot.json")
	if err != nil {
		t.Fatal(err)
	}
	var vector descriptorVector
	if err := json.Unmarshal(data, &vector); err != nil {
		t.Fatal(err)
	}
	return vector
}
func TestDescriptorSnapshotSharedGatewayVector(t *testing.T) {
	vector := snapshotVector(t)
	for _, row := range []struct{ text, digest string }{{vector.SnapshotText, vector.Digest}, {vector.ChangedText, vector.ChangedDigest}} {
		digest, err := DescriptorSnapshotDigest([]byte(row.text))
		if err != nil || digest != row.digest {
			t.Fatalf("digest %q, want %q: %v", digest, row.digest, err)
		}
	}
	snapshot, err := ReadDescriptorSnapshot([]byte(vector.SnapshotText))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(*snapshot.Tools["search_tickets"].InputSchemaText, `"minimum": 1.0`) {
		t.Fatal("original schema text changed")
	}
	for _, pair := range [][2]string{{"2026-10-24T01:02:03Z", "2026-10-24T01:02:04Z"}, {"tickets-mcp", "other-mcp"}, {"tickets.example", "other.example"}, {`"minimum\": 1.0`, `"minimum\": 1`}} {
		changed := strings.Replace(vector.SnapshotText, pair[0], pair[1], 1)
		if changed == vector.SnapshotText {
			t.Fatal("mutation did not apply")
		}
		digest, err := DescriptorSnapshotDigest([]byte(changed))
		if err != nil || digest == vector.Digest {
			t.Fatalf("full byte change was not pinned: %v", err)
		}
	}
}
func TestDescriptorSnapshotStrictRefusals(t *testing.T) {
	vector := snapshotVector(t)
	for _, row := range vector.Invalid {
		t.Run(row.Name, func(t *testing.T) {
			if _, err := ReadDescriptorSnapshot([]byte(strings.Replace(vector.SnapshotText, row.Old, row.New, 1))); err == nil {
				t.Fatal("malformed snapshot accepted")
			}
		})
	}
	for _, data := range [][]byte{nil, []byte(vector.SnapshotText + "\n"), []byte(vector.SnapshotText + "{}"), []byte(strings.Replace(vector.SnapshotText, "Search tickets", strings.Repeat("x", DescriptorSnapshotBytes), 1)), {0xff}, append([]byte{0xef, 0xbb, 0xbf}, []byte(vector.SnapshotText)...)} {
		if _, err := ReadDescriptorSnapshot(data); err == nil {
			t.Fatal("malformed snapshot bytes accepted")
		}
	}
}
func TestDescriptorSnapshotSchemaTextIsOpaque(t *testing.T) {
	vector := snapshotVector(t)
	text := strings.Replace(vector.SnapshotText, `{\"type\":\"object\"}`, "not JSON", 1)
	snapshot, err := ReadDescriptorSnapshot([]byte(text))
	if err != nil || snapshot.Tools["close"].InputSchemaText == nil || *snapshot.Tools["close"].InputSchemaText != "not JSON" {
		t.Fatalf("schema was interpreted by snapshot reader: %v", err)
	}
}
func TestDescriptorSchemaStrictJSON(t *testing.T) {
	if _, err := descriptorSchema(`{"type":"object","minimum":1.0,"maximum":1e2}`); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{`{"type":"object","type":"string"}`, `{"x":"\ud800"}`, `{"x":"\udc00"}`, `{"x":"a\"}`, `{"x":true}{}`, `{"minimum":1e999}`, `null`} {
		if _, err := descriptorSchema(text); err == nil {
			t.Fatalf("invalid schema accepted: %s", text)
		}
	}
}
func TestDescriptorSnapshotPlatformAndEndpointForms(t *testing.T) {
	vector := snapshotVector(t)
	for _, name := range []string{"tickets", "café", "𐀀"} {
		if _, err := ReadDescriptorSnapshot([]byte(strings.Replace(vector.SnapshotText, `"platform":"tickets"`, `"platform":"`+name+`"`, 1))); err != nil {
			t.Fatal(err)
		}
	}
	for _, endpoint := range []string{"https://example.test", "https://[::ffff:192.0.2.1]:443/path%20name", "http://127.1.2.3:80", "http://[::1]/mcp"} {
		if _, err := ReadDescriptorSnapshot([]byte(strings.Replace(vector.SnapshotText, "https://tickets.example/mcp", endpoint, 1))); err != nil {
			t.Fatalf("endpoint %s: %v", endpoint, err)
		}
	}
	for _, endpoint := range []string{"https://user@example.test", "https://example.test:0", "https://example.test:01", "https://example.test/%zz", "https://[::ffff:192.000.2.1]", "http://localhost", "https://example.test?x=1"} {
		if _, err := ReadDescriptorSnapshot([]byte(strings.Replace(vector.SnapshotText, "https://tickets.example/mcp", endpoint, 1))); err == nil {
			t.Fatalf("invalid endpoint %s", endpoint)
		}
	}
}

func TestDescriptorIntegerDomain(t *testing.T) {
	for _, text := range []string{"1.0", "1e0", "9007199254740992"} {
		if _, err := descriptorJSON([]byte(text), true); err == nil {
			t.Fatal("raw number admitted", text)
		}
	}
	if _, err := descriptorJSON([]byte("-0"), true); err != nil {
		t.Fatal(err)
	}
}
