package runtimeactions

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestRuntimeActionCanonicalShared(t *testing.T) {
	raw, err := os.ReadFile("../../../contract/testdata/runtime-actions/canonical.json")
	if err != nil {
		t.Fatal(err)
	}
	var vector struct {
		Rows []struct{ Name, Input, Canonical, Digest string }
	}
	if json.Unmarshal(raw, &vector) != nil {
		t.Fatal("bad fixture")
	}
	for _, row := range vector.Rows {
		text, err := Canonical([]byte(row.Input))
		if err != nil || text != row.Canonical || digest(text) != row.Digest {
			t.Fatalf("%s: text=%s digest=%s error=%v", row.Name, text, digest(text), err)
		}
	}
	for _, raw := range []string{`{"value":9007199254740992}`, `{"value":1e1000}`} {
		if _, err := Canonical([]byte(raw)); err == nil {
			t.Fatal("ambiguous numeric approval accepted")
		}
	}
}
func TestRuntimeActionOwnerClosedAndBounded(t *testing.T) {
	pin := "sha256:" + strings.Repeat("a", 64)
	owner := Owner{ArtifactDigest: pin, ProcessID: "launch-one", ProjectID: "project-one", ProjectLabel: "Example project", SessionID: "server-session-one", PrincipalDigest: pin, PrincipalLabel: "Project editor", ScopeDigest: pin, ScopeLabel: "Evaluate example project"}
	if !ValidOwner(owner) {
		t.Fatal("valid owner refused")
	}
	for _, change := range []func(*Owner){func(o *Owner) { o.PrincipalLabel = "" }, func(o *Owner) { o.SessionID = strings.Repeat("x", 129) }, func(o *Owner) { o.ProjectLabel = "line\nbreak" }} {
		bad := owner
		change(&bad)
		if ValidOwner(bad) {
			t.Fatal("malformed owner admitted")
		}
	}
	var forged Owner
	raw, _ := json.Marshal(owner)
	raw = append(raw[:len(raw)-1], []byte(`,"approved":true}`)...)
	if json.Unmarshal(raw, &forged) == nil {
		t.Fatal("unknown owner member accepted")
	}
}
