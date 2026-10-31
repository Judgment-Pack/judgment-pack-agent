package runcontext

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
)

// The Go twin of packages/harness/storage/reauth.test.ts (#226): the shared re-authorization vectors and
// the effect host's admission boundary. The adapter-level flow is go/adapter/run_reauth_test.go.

func readReauthVector(t *testing.T, name string) map[string]json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile("../../contract/vectors/reauth/" + name)
	if err != nil {
		t.Fatal(err)
	}
	var file map[string]json.RawMessage
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	return file
}
func reauthReasonOf(err error) string {
	var refused *ReauthorizationRefused
	if errors.As(err, &refused) {
		return "reauth:" + refused.Reason
	}
	var resume *ResumeRefused
	if errors.As(err, &resume) {
		return "resume:" + resume.Reason
	}
	return waitReasonOf(err)
}

func TestReauthDispositionVectorsBothReaders(t *testing.T) {
	file := readReauthVector(t, "disposition.json")
	var digests []string
	_ = json.Unmarshal(file["digests"], &digests)
	items := []ReauthDisposition{}
	for i, item := range waitList(t, file["items"]) {
		held, err := ReadReauthDisposition(item)
		if err != nil || !sameWaitJSON(t, held, item) {
			t.Fatalf("disposition %d: %v", i, err)
		}
		if digest, err := ReauthDispositionDigest(held); err != nil || digest != digests[i] {
			t.Errorf("disposition %d digest %s %v", i, digest, err)
		}
		items = append(items, held)
	}
	for _, list := range []string{"refused", "readerRefused"} {
		for _, row := range waitList(t, file[list]) {
			if _, err := ReadReauthDisposition(rawMember(row, "disposition")); reauthReasonOf(err) != "reauth:invalid" {
				t.Errorf("%s accepted: %v", rawMember(row, "problem"), err)
			}
		}
	}
	for _, row := range waitList(t, file["heads"]) {
		var head struct {
			Case        string `json:"case"`
			Disposition int    `json:"disposition"`
			Holds       bool   `json:"holds"`
		}
		_ = json.Unmarshal(row, &head)
		checkpoint, err := canonicalRaw(rawMember(row, "checkpoint"))
		if err != nil {
			t.Fatal(err)
		}
		if got := HoldsNeverAdmitted(checkpoint, items[head.Disposition]); got != head.Holds {
			t.Errorf("%s: %v", head.Case, got)
		}
	}
}

func TestReauthActionVectorsBothReaders(t *testing.T) {
	file := readReauthVector(t, "action.json")
	var digests []string
	_ = json.Unmarshal(file["digests"], &digests)
	for i, item := range waitList(t, file["items"]) {
		held, err := ReadReauthAction(item)
		if err != nil || !sameWaitJSON(t, held, item) {
			t.Fatalf("action %d: %v", i, err)
		}
		if digest, err := ReauthActionDigest(held); err != nil || digest != digests[i] {
			t.Errorf("action %d digest %s %v", i, digest, err)
		}
	}
	for _, list := range []string{"refused", "readerRefused"} {
		for _, row := range waitList(t, file[list]) {
			if _, err := ReadReauthAction(rawMember(row, "action")); reauthReasonOf(err) != "reauth:invalid" {
				t.Errorf("%s accepted: %v", rawMember(row, "problem"), err)
			}
		}
	}
	recorded := file["reauthorized"]
	refreshes := waitList(t, rawMember(recorded, "items"))
	for i, item := range refreshes {
		if held, err := ReadReauthorized(item); err != nil || !sameWaitJSON(t, held, item) {
			t.Fatalf("reauthorized %d: %v", i, err)
		}
	}
	for _, list := range []string{"refused", "readerRefused"} {
		for _, row := range waitList(t, rawMember(recorded, list)) {
			if _, err := ReadReauthorized(rawMember(row, "reauthorized")); reauthReasonOf(err) != "reauth:invalid" {
				t.Errorf("%s accepted: %v", rawMember(row, "problem"), err)
			}
		}
	}
	for _, row := range waitList(t, file["cases"]) {
		var c struct {
			Case string `json:"case"`
			Wait struct {
				Resource        string            `json:"resource"`
				Wait            string            `json:"wait"`
				ParkedAt        string            `json:"parkedAt"`
				Connection      string            `json:"connection"`
				Reauthorization ReauthDisposition `json:"reauthorization"`
			} `json:"wait"`
			Outcome struct {
				Reauthorized *int   `json:"reauthorized"`
				Refused      string `json:"refused"`
				By           string `json:"by"`
			} `json:"outcome"`
		}
		if err := json.Unmarshal(row, &c); err != nil {
			t.Fatal(err)
		}
		var action ReauthAction
		if err := json.Unmarshal(rawMember(row, "action"), &action); err != nil {
			t.Fatal(err)
		}
		got, err := Reauthorize(ReauthWait{Resource: c.Wait.Resource, Wait: c.Wait.Wait, ParkedAt: c.Wait.ParkedAt, Connection: c.Wait.Connection, Reauthorization: &c.Wait.Reauthorization}, action)
		if c.Outcome.Reauthorized != nil {
			if err != nil || !sameWaitJSON(t, got, refreshes[*c.Outcome.Reauthorized]) {
				t.Errorf("%s: %+v %v", c.Case, got, err)
			}
		} else if reauthReasonOf(err) != c.Outcome.By+":"+c.Outcome.Refused {
			t.Errorf("%s: %v", c.Case, err)
		}
	}
}

func TestReauthRefusalVector(t *testing.T) {
	file := readReauthVector(t, "refusal.json")
	reasons := []string{}
	for _, item := range waitList(t, file["items"]) {
		held, err := ReadReauthRefusal(item)
		if err != nil {
			t.Fatal(err)
		}
		reasons = append(reasons, held.Reason)
		if !sameWaitJSON(t, (&ReauthorizationRefused{Reason: held.Reason}).Record(), item) {
			t.Errorf("record %s", held.Reason)
		}
	}
	if !slices.Equal(reasons, ReauthRefusalReasons) {
		t.Fatalf("reasons %v", reasons)
	}
	for _, row := range waitList(t, file["refused"]) {
		if _, err := ReadReauthRefusal(row); reauthReasonOf(err) != "reauth:invalid" {
			t.Errorf("%s accepted", row)
		}
	}
}

// The effect host's admission boundary (#226): a refusal is trusted only when the run's proven effect host
// mints it once, within the live dispatch of that exact admission, before anything is performed, and with
// a well-formed refusal. The TypeScript twin is "a refusal is trusted only from the run's proven effect
// host" in reauth.test.ts.
func TestReauthTheBoundaryTrustsOnlyALiveMint(t *testing.T) {
	ctx := context.Background()
	principal := "sha256:" + strings.Repeat("7", 64)
	refusal := CredentialRefusal{Cause: "expired", Connection: "tracker-conn", Principal: principal, Permissions: []string{"issues:write"}, Requires: []string{"issues:write"}}
	op := EffectOperation{Run: "run-m", Operation: "op-1", Source: "src", Tool: "create_issue", RequestDigest: "sha256:" + strings.Repeat("6", 64)}
	type mint func(ctx context.Context, admission EffectAdmission, perform func(context.Context) error) error
	cases := []struct {
		name    string
		mint    mint
		trusted bool
	}{
		{"a well-formed refusal for the live admission", func(ctx context.Context, a EffectAdmission, _ func(context.Context) error) error {
			return NewReauthorizationRequired(ctx, a, refusal)
		}, true},
		{"another context than the dispatch's", func(_ context.Context, a EffectAdmission, _ func(context.Context) error) error {
			return NewReauthorizationRequired(context.Background(), a, refusal)
		}, false},
		{"another admission", func(ctx context.Context, a EffectAdmission, _ func(context.Context) error) error {
			other := a
			other.Operation.Operation = "op-2"
			return NewReauthorizationRequired(ctx, other, refusal)
		}, false},
		{"minted twice", func(ctx context.Context, a EffectAdmission, _ func(context.Context) error) error {
			_ = NewReauthorizationRequired(ctx, a, refusal)
			return NewReauthorizationRequired(ctx, a, refusal)
		}, false},
		{"performed, then refused", func(ctx context.Context, a EffectAdmission, perform func(context.Context) error) error {
			_ = perform(ctx)
			return NewReauthorizationRequired(ctx, a, refusal)
		}, false},
		{"refused, then performed", func(ctx context.Context, a EffectAdmission, perform func(context.Context) error) error {
			err := NewReauthorizationRequired(ctx, a, refusal)
			_ = perform(ctx)
			return err
		}, false},
		{"a principal that is not a digest", func(ctx context.Context, a EffectAdmission, _ func(context.Context) error) error {
			held := refusal
			held.Principal = "person-one@example.invalid"
			return NewReauthorizationRequired(ctx, a, held)
		}, false},
		{"a connection that is not bounded", func(ctx context.Context, a EffectAdmission, _ func(context.Context) error) error {
			held := refusal
			held.Connection = "Bearer SENTINEL-ACCESS-TOKEN"
			return NewReauthorizationRequired(ctx, a, held)
		}, false},
		{"unsorted permissions", func(ctx context.Context, a EffectAdmission, _ func(context.Context) error) error {
			held := refusal
			held.Permissions = []string{"issues:write", "issues:read"}
			return NewReauthorizationRequired(ctx, a, held)
		}, false},
		{"a refusal wrapped in another error", func(ctx context.Context, a EffectAdmission, _ func(context.Context) error) error {
			return errors.Join(NewReauthorizationRequired(ctx, a, refusal))
		}, false},
	}
	for _, c := range cases {
		store := durableWaitStore{NewMemoryRunStore(MemoryRunStoreOptions{})}
		inner := c.mint
		create := func(authority LeaseAuthority) EffectHost {
			return &mintHost{reference: NewReferenceEffectHost(authority), mint: inner}
		}
		certified, err := CertifyEffectHost(ctx, create, store)
		if err != nil {
			t.Fatal(err)
		}
		lease, err := store.AcquireLease(ctx, LeaseAcquire{Resource: "run-m", Owner: "worker-a", DurationMs: 3_600_000})
		if err != nil {
			t.Fatal(err)
		}
		view, err := UnattendedRunStore(store, FenceOf(lease), certified)
		if err != nil {
			t.Fatal(err)
		}
		performed := 0
		_ = fencedEffect(ctx, view, op, func(context.Context) error { performed++; return nil })
		if got := ReauthorizationOf(view) != nil; got != c.trusted {
			t.Errorf("%s: trusted %v", c.name, got)
		}
		// The same well-formed mint from a host nobody proved for the store is no proof.
		if c.trusted {
			other := durableWaitStore{NewMemoryRunStore(MemoryRunStoreOptions{})}
			held, _ := other.AcquireLease(ctx, LeaseAcquire{Resource: "run-m", Owner: "worker-a", DurationMs: 3_600_000})
			unproven, err := FencedRunStore(other, FenceOf(held), create(other))
			if err != nil {
				t.Fatal(err)
			}
			_ = fencedEffect(ctx, unproven, op, func(context.Context) error { return nil })
			if ReauthorizationOf(unproven) != nil {
				t.Errorf("%s: an unproven host", c.name)
			}
			if performed != 0 {
				t.Errorf("%s: performed", c.name)
			}
		}
	}
	// A refusal minted after the dispatch ended is not trusted, and a refusal nobody minted parks nothing.
	store := durableWaitStore{NewMemoryRunStore(MemoryRunStoreOptions{})}
	var kept context.Context
	var keptAdmission EffectAdmission
	certified, err := CertifyEffectHost(ctx, func(authority LeaseAuthority) EffectHost {
		return &mintHost{reference: NewReferenceEffectHost(authority), mint: func(ctx context.Context, a EffectAdmission, perform func(context.Context) error) error {
			kept, keptAdmission = ctx, a
			return perform(ctx)
		}}
	}, store)
	if err != nil {
		t.Fatal(err)
	}
	lease, _ := store.AcquireLease(ctx, LeaseAcquire{Resource: "run-m", Owner: "worker-a", DurationMs: 3_600_000})
	view, _ := UnattendedRunStore(store, FenceOf(lease), certified)
	_ = fencedEffect(ctx, view, op, func(context.Context) error { return nil })
	late, _ := NewReauthorizationRequired(kept, keptAdmission, refusal).(*ReauthorizationRequired)
	if trustedReauthorization(late) || ReauthorizationOf(view) != nil {
		t.Fatal("a late mint is trusted")
	}
}

// mintHost is a host whose credential refusal is the case's, in front of the reference host's own
// admission for the kit's operations (source host).
type mintHost struct {
	reference *ReferenceEffectHost
	mint      func(context.Context, EffectAdmission, func(context.Context) error) error
}

func (h *mintHost) EffectFencing() string { return EffectFencingEnforced }
func (h *mintHost) Dispatch(ctx context.Context, admission EffectAdmission, perform func(context.Context) error) error {
	if admission.Operation.Source == "host" {
		return h.reference.Dispatch(ctx, admission, perform)
	}
	return h.mint(ctx, admission, perform)
}
