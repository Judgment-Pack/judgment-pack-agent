package runcontext

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

type principalGatewayVectors struct {
	Keys    map[string][]string `json:"keys"`
	Vectors []struct {
		Name   string          `json:"name"`
		Chain  json.RawMessage `json:"chain"`
		Expect string          `json:"expect"`
	} `json:"vectors"`
}

func principalOutcome(t *testing.T, chain []byte, keys map[string][]string) string {
	t.Helper()
	pinned, err := PinPrincipalKeys(keys)
	if err != nil {
		t.Fatal(err)
	}
	_, err = VerifyPrincipalChain(chain, pinned)
	var refused *DelegationRefused
	switch {
	case err == nil:
		return "ok"
	case errors.As(err, &refused):
		return refused.Refusal.Name
	}
	t.Fatal(err)
	return ""
}

// The reference gateway's statement vectors, cited, not regenerated: the copy is checked against
// the citation's digest and every vector read to the same expectation as the gateway's readers.
func TestPrincipalGatewayStatementVectors(t *testing.T) {
	data, err := os.ReadFile("../../contract/testdata/principal/gateway-statements.json")
	if err != nil {
		t.Fatal(err)
	}
	var citation struct {
		SHA256 string `json:"sha256"`
	}
	raw, err := os.ReadFile("../../contract/testdata/principal/citation.json")
	if err != nil || json.Unmarshal(raw, &citation) != nil {
		t.Fatal("citation unreadable")
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != citation.SHA256 {
		t.Fatal("the copy is not the cited file")
	}
	var vectors principalGatewayVectors
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatal(err)
	}
	if len(vectors.Vectors) == 0 {
		t.Fatal("no vectors")
	}
	for _, v := range vectors.Vectors {
		t.Run(v.Name, func(t *testing.T) {
			if got := principalOutcome(t, v.Chain, vectors.Keys); got != v.Expect {
				t.Fatalf("got %s, want %s", got, v.Expect)
			}
		})
	}
}

func TestPrincipalKeyRule(t *testing.T) {
	for key, want := range map[string]string{
		"0000000000000000000000000000000000000000000000000000000000000000": "key-small-order",
		"edffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f": "key-not-canonical",
		"0100000000000000000000000000000000000000000000000000000000000080": "key-not-canonical",
		"0200000000000000000000000000000000000000000000000000000000000000": "key-not-on-curve",
		"00028d283e279178a6ce561e45f1b89f2a6ac45690fbb5ac48313ad61ab04584": "",
	} {
		if got := PrincipalKeyProblem(key); got != want {
			t.Errorf("%s: got %q, want %q", key, got, want)
		}
	}
}

type principalChainVectors struct {
	Keys     map[string][]string `json:"keys"`
	Software struct {
		Package string `json:"package"`
		Version string `json:"version"`
	} `json:"software"`
	Audience string `json:"audience"`
	Digests  struct {
		Root  string `json:"root"`
		Child string `json:"child"`
	} `json:"digests"`
	Items   []json.RawMessage `json:"items"`
	Refused []struct {
		Problem string          `json:"problem"`
		Chain   json.RawMessage `json:"chain"`
		Refusal string          `json:"refusal"`
	} `json:"refused"`
}

func loadPrincipalChains(t *testing.T) principalChainVectors {
	t.Helper()
	raw, err := os.ReadFile("../../contract/vectors/principal/chains.json")
	if err != nil {
		t.Fatal(err)
	}
	var v principalChainVectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func principalInput(v principalChainVectors, chain json.RawMessage, port PrincipalAdmissionPort, clock func() (time.Time, error)) *RunPrincipal {
	return &RunPrincipal{Chain: chain, Keys: v.Keys, Software: PrincipalSoftware{Package: v.Software.Package, Version: v.Software.Version}, Audience: v.Audience, Admission: port, Clock: clock}
}

func principalAdmitAll(context.Context, PrincipalAdmissionRead) ([]byte, error) {
	return []byte(`{"admission":"admit"}`), nil
}

func TestPrincipalAgentHostChains(t *testing.T) {
	v := loadPrincipalChains(t)
	root, err := OpenPrincipal(principalInput(v, v.Items[0], principalAdmitAll, nil))
	if err != nil {
		t.Fatal(err)
	}
	if root.Record.Agent != "b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1" || root.Record.Host != "desk" || root.Record.DelegationID != "33333333333333333333333333333333" || root.Record.DelegationHash != v.Digests.Root || len(root.Record.Chain) != 1 || root.Record.Chain[0] != v.Digests.Root {
		t.Fatalf("root record %+v", root.Record)
	}
	if root.Record.Subject != (PrincipalReference{"person", "https://login.example.com/", "approver@example.com"}) {
		t.Fatalf("subject %+v", root.Record.Subject)
	}
	child, err := OpenPrincipal(principalInput(v, v.Items[1], principalAdmitAll, nil))
	if err != nil {
		t.Fatal(err)
	}
	if child.Record.Agent != "b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2" || child.Record.DelegationHash != v.Digests.Child || len(child.Record.Chain) != 2 || child.Record.Chain[0] != v.Digests.Root || child.Record.Chain[1] != v.Digests.Child {
		t.Fatalf("child record %+v", child.Record)
	}
	for _, refused := range v.Refused {
		t.Run(refused.Problem, func(t *testing.T) {
			if got := principalOutcome(t, refused.Chain, v.Keys); got != refused.Refusal {
				t.Fatalf("got %s, want %s", got, refused.Refusal)
			}
		})
	}
	input := principalInput(v, v.Items[0], principalAdmitAll, nil)
	input.Software.Version = "0.12.0"
	var refused *DelegationRefused
	if _, err := OpenPrincipal(input); !errors.As(err, &refused) || refused.Refusal.Name != "software-mismatch" {
		t.Fatalf("software: %v", err)
	}
}

type principalAdmissionCase struct {
	Name  string `json:"name"`
	Chain string `json:"chain"`
	Now   *string
	Call  struct {
		Audience *PrincipalAudience `json:"audience"`
		Source   string             `json:"source"`
		Tool     string             `json:"tool"`
		Effect   string             `json:"effect"`
	} `json:"call"`
	Count  bool            `json:"count"`
	Answer json.RawMessage `json:"answer"`
	Expect json.RawMessage `json:"expect"`
	Read   *int            `json:"read"`
}

func TestPrincipalAdmissionVectors(t *testing.T) {
	v := loadPrincipalChains(t)
	raw, err := os.ReadFile("../../contract/vectors/principal/admission.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors struct {
		Items []json.RawMessage        `json:"items"`
		Cases []principalAdmissionCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatal(err)
	}
	for _, c := range vectors.Cases {
		t.Run(c.Name, func(t *testing.T) {
			var reads []PrincipalAdmissionRead
			port := func(_ context.Context, read PrincipalAdmissionRead) ([]byte, error) {
				reads = append(reads, read)
				if string(c.Answer) == `"throw"` {
					return nil, errors.New("port down")
				}
				return c.Answer, nil
			}
			clock := func() (time.Time, error) {
				if c.Now == nil {
					return time.Time{}, errors.New("no clock")
				}
				return time.Parse(time.RFC3339Nano, *c.Now)
			}
			chain := v.Items[0]
			if c.Chain == "child" {
				chain = v.Items[1]
			}
			principal, err := OpenPrincipal(principalInput(v, chain, port, clock))
			if err != nil {
				t.Fatal(err)
			}
			got, err := principal.AdmitPrincipalCall(context.Background(), PrincipalCall{Audience: c.Call.Audience, Source: c.Call.Source, Tool: c.Call.Tool, Effect: c.Call.Effect}, c.Count)
			if err != nil {
				t.Fatal(err)
			}
			want := "admit"
			if string(c.Expect) != `"admit"` {
				var refusal struct{ Name, Reason string }
				if err := json.Unmarshal(c.Expect, &refusal); err != nil {
					t.Fatal(err)
				}
				want = refusal.Name + "/" + refusal.Reason
			}
			have := "admit"
			if got != nil {
				have = got.Name + "/" + got.Reason
			}
			if have != want {
				t.Fatalf("got %s, want %s", have, want)
			}
			if c.Read == nil {
				if len(reads) != 0 {
					t.Fatalf("a read was made: %+v", reads)
				}
				return
			}
			if len(reads) != 1 {
				t.Fatalf("reads: %+v", reads)
			}
			expected, err := ParseJSON(vectors.Items[*c.Read])
			if err != nil {
				t.Fatal(err)
			}
			if CanonicalJSON(reads[0].Value()) != CanonicalJSON(expected) {
				t.Fatalf("read %s, want %s", CanonicalJSON(reads[0].Value()), CanonicalJSON(expected))
			}
		})
	}
}

func TestPrincipalAdmissionPortTimeout(t *testing.T) {
	v := loadPrincipalChains(t)
	port := func(ctx context.Context, _ PrincipalAdmissionRead) ([]byte, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	clock := func() (time.Time, error) { return time.Parse(time.RFC3339Nano, "2026-10-30T01:00:00.000Z") }
	principal, err := OpenPrincipal(principalInput(v, v.Items[0], port, clock))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := principal.AdmitPrincipalCall(ctx, PrincipalCall{Audience: &PrincipalAudience{"agent-host", "desk"}, Source: "notes", Tool: "read_note", Effect: "read"}, true); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("an ended call: %v", err)
	}
}

func principalVector(t *testing.T, name string) *Value {
	t.Helper()
	raw, err := os.ReadFile("../../contract/vectors/principal/" + name)
	if err != nil {
		t.Fatal(err)
	}
	value, err := ParseJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestPrincipalRecordVectors(t *testing.T) {
	records := principalVector(t, "record.json")
	for _, item := range records.Get("items").Items() {
		if !validPrincipalValue(item) {
			t.Fatalf("refused %s", CanonicalJSON(item))
		}
	}
	for _, row := range records.Get("refused").Items() {
		if validPrincipalValue(row.Get("record")) {
			t.Fatalf("admitted: %s", row.Get("problem").Str())
		}
	}
	v := loadPrincipalChains(t)
	answers := principalVector(t, "answer.json")
	for _, group := range []struct {
		key      string
		admitted bool
	}{{"items", true}, {"refused", false}} {
		for _, answer := range answers.Get(group.key).Items() {
			raw := []byte(CanonicalJSON(answer))
			principal, err := OpenPrincipal(principalInput(v, v.Items[0], func(context.Context, PrincipalAdmissionRead) ([]byte, error) { return raw, nil }, func() (time.Time, error) { return time.Parse(time.RFC3339Nano, "2026-10-30T01:00:00.000Z") }))
			if err != nil {
				t.Fatal(err)
			}
			got, err := principal.AdmitPrincipalCall(context.Background(), PrincipalCall{Audience: &PrincipalAudience{"agent-host", "desk"}, Source: "notes", Tool: "read_note", Effect: "read"}, true)
			if err != nil || group.admitted == (got != nil && got.Name == "admission-unknown") {
				t.Fatalf("answer %s: %+v %v", raw, got, err)
			}
		}
	}
	for _, event := range principalVector(t, "events.json").Get("items").Items() {
		if err := contract.CheckRecord("event", event.JSON()); err != nil {
			t.Fatalf("event %s: %v", CanonicalJSON(event), err)
		}
	}
	reports := principalVector(t, "report.json")
	for _, report := range reports.Get("items").Items() {
		if _, err := validateRunReport(report.Clone()); err != nil {
			t.Fatalf("report: %v", err)
		}
	}
	for _, row := range reports.Get("refused").Items() {
		if _, err := validateRunReport(row.Get("report").Clone()); err == nil {
			t.Fatalf("admitted: %s", row.Get("problem").Str())
		}
	}
}
