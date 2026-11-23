package runcontext

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

// ADR-0030: the specification's admission read cases (RFC 0030 §2a, tables 4 and 5), copied byte for
// byte (a case's "note" member is commentary and is not read); this digest is the one the specification's conformance/delegation/README.md states.
const admissionReadsSHA256 = "a620f8e7280652a1c9402c4862ab3ebafa9b8286569a327fdbdd12492f5291e2"

type admissionReadsCase struct {
	Name        string            `json:"name"`
	Table       int               `json:"table"`
	Applies     map[string]string `json:"applies"`
	Senders     []string          `json:"senders"`
	ReadVersion string            `json:"readVersion"`
	Inputs      struct {
		Chain    *string           `json:"chain"`
		At       string            `json:"at"`
		Audience PrincipalAudience `json:"audience"`
		Call     *struct {
			Source, Tool, Effect string
			Count                bool
		} `json:"call"`
		Version *string         `json:"version"`
		Skills  json.RawMessage `json:"skills"`
		Load    *struct {
			Skill  PrincipalSkill
			Skills []PrincipalSkill
		} `json:"load"`
		Restore []PrincipalSkill `json:"restore"`
	} `json:"inputs"`
	Body   json.RawMessage `json:"body"`
	Answer json.RawMessage `json:"answer"`
	Expect struct {
		Sender  string          `json:"sender"`
		Reads   int             `json:"reads"`
		Loaded  json.RawMessage `json:"loaded"`
		Dropped []string        `json:"dropped"`
	} `json:"expect"`
}

type admissionReadsFixture struct {
	cases    []admissionReadsCase
	chain    json.RawMessage
	keys     map[string][]string
	software PrincipalSoftware
}

func loadAdmissionReads(t *testing.T) admissionReadsFixture {
	t.Helper()
	raw, err := os.ReadFile("../../contract/testdata/principal/admission-reads.json")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != admissionReadsSHA256 {
		t.Fatal("admission-reads.json is not the bytes the specification states")
	}
	citation, err := os.ReadFile("../../contract/testdata/principal/spec-citation.json")
	if err != nil {
		t.Fatal(err)
	}
	var cited struct {
		Files []struct {
			Copy, Sha256 string
			Cases        int
		}
	}
	if err := json.Unmarshal(citation, &cited); err != nil {
		t.Fatal(err)
	}
	var f admissionReadsFixture
	var file struct{ Cases []admissionReadsCase }
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	f.cases = file.Cases
	found := false
	for _, c := range cited.Files {
		found = found || c.Copy == "admission-reads.json" && c.Sha256 == admissionReadsSHA256 && c.Cases == len(f.cases)
	}
	if !found {
		t.Fatal("spec-citation.json does not cite admission-reads.json at its digest and case count")
	}
	statements, err := os.ReadFile("../../contract/testdata/principal/spec-ed25519-statements.json")
	if err != nil {
		t.Fatal(err)
	}
	var ed struct {
		Keys    map[string][]string
		Vectors []struct {
			Name  string
			Chain json.RawMessage
		}
	}
	if err := json.Unmarshal(statements, &ed); err != nil {
		t.Fatal(err)
	}
	f.keys = ed.Keys
	for _, v := range ed.Vectors {
		if v.Name == "a valid root" {
			f.chain = v.Chain
		}
	}
	f.software = PrincipalSoftware{Package: "judgment-pack-agent", Version: "0.13.0"}
	return f
}

func (f admissionReadsFixture) open(t *testing.T, version, at string, port PrincipalAdmissionPort, load PrincipalSkillLoadPort) *OpenedPrincipal {
	t.Helper()
	if port == nil {
		port = principalAdmitAll
	}
	clock := func() (time.Time, error) { return time.Parse(time.RFC3339Nano, at) }
	principal, err := OpenPrincipal(&RunPrincipal{Chain: f.chain, Keys: f.keys, Software: f.software, Audience: "agent-host-1", Admission: port, Clock: clock, ReadVersions: map[string]string{"desk": version}, SkillLoad: load})
	if err != nil {
		t.Fatal(err)
	}
	return principal
}

func jsonText(t *testing.T, raw json.RawMessage) *string {
	t.Helper()
	var text *string
	if err := json.Unmarshal(raw, &text); err != nil {
		t.Fatal(err)
	}
	return text
}

// skillTestGate is a run's skill gate over catalogued skills of one source, with loaded already loaded
// (restored, with no read), under principal where it is not nil.
func skillTestGate(t *testing.T, principal *OpenedPrincipal, catalogued, loaded []PrincipalSkill, perRun int) (*SkillGate, *SourceGate, *Execution, *[]contract.ProvenanceSourceFigures) {
	t.Helper()
	policy := Defaults
	if perRun > 0 {
		policy.SkillsPerRun = perRun
	}
	rows := []contract.ProvenanceSourceFigures{{Source: "skills"}}
	gate := NewSourceGate(&Declared{}, &policy, "marker", "json-schema", func() *[]contract.ProvenanceSourceFigures { return &rows }, nil)
	if principal != nil {
		gate.UsePrincipal(principal)
	}
	state := &Execution{}
	for _, held := range loaded {
		state.Skills = append(state.Skills, contract.ProvenanceSkillLoad{ID: held.ID, Digest: held.Digest, Bytes: 10, LoadedAt: "2026-10-30T00:30:00.000Z"})
	}
	var admitted []AdmittedSkill
	for _, skill := range catalogued {
		admitted = append(admitted, AdmittedSkill{Skill: Skill{Kind: "method", ID: skill.ID, Name: skill.ID, Description: "synthetic", Instructions: "synthetic", Source: SkillSource{Kind: "local", ID: "skills"}}, Digest: skill.Digest, Bytes: 10})
	}
	skills := NewSkillGate(admitted, policy, gate, state, "marker", false, nil)
	skills.Restore()
	return skills, gate, state, &rows
}

func loadedIDs(state *Execution) []string {
	var ids []string
	for _, load := range state.Skills {
		ids = append(ids, load.ID)
	}
	return ids
}

// lastSkillRefusal is the refusal the last guardrail names, or "".
func lastSkillRefusal(gate *SourceGate) (string, *contract.RunEventGuardrail) {
	for i := len(gate.notices) - 1; i >= 0; i-- {
		if g := gate.notices[i].Guardrail; g != nil {
			if g.Refusal != nil {
				return g.Refusal.Name, g
			}
			return "", g
		}
	}
	return "", nil
}

func TestAdmissionReadsSpecCases(t *testing.T) {
	f := loadAdmissionReads(t)
	ran := 0
	for _, c := range f.cases {
		if !slices.Contains(c.Senders, "library") {
			continue
		}
		ran++
		t.Run(c.Name, func(t *testing.T) {
			neverEmits := strings.HasPrefix(c.Applies["sender"], "never emits")
			if c.Table == 4 {
				admissionReadsCallCase(t, f, c, neverEmits)
				return
			}
			admissionReadsLoadCase(t, f, c, neverEmits)
		})
	}
	if ran != 25 {
		t.Fatalf("ran %d library cases, want 25", ran)
	}
}

func admissionReadsCallCase(t *testing.T, f admissionReadsFixture, c admissionReadsCase, neverEmits bool) {
	answer := jsonText(t, c.Answer)
	var bodies []string
	port := func(_ context.Context, read PrincipalAdmissionRead) ([]byte, error) {
		bodies = append(bodies, CanonicalJSON(read.Value()))
		return []byte(*answer), nil
	}
	var skills []PrincipalSkill
	if json.Unmarshal(c.Inputs.Skills, &skills) != nil {
		skills = nil
	}
	principal := f.open(t, c.ReadVersion, c.Inputs.At, port, nil)
	call := c.Inputs.Call
	got, err := principal.AdmitPrincipalCall(context.Background(), PrincipalCall{Audience: &c.Inputs.Audience, Source: call.Source, Tool: call.Tool, Effect: call.Effect, Version: c.Inputs.Version, Skills: func() []PrincipalSkill { return skills }}, call.Count)
	if err != nil {
		t.Fatal(err)
	}
	verdict := "admit"
	if got != nil {
		verdict = got.Name
	}
	if verdict != c.Expect.Sender || len(bodies) != c.Expect.Reads {
		t.Fatalf("got %s after %d reads, want %s after %d", verdict, len(bodies), c.Expect.Sender, c.Expect.Reads)
	}
	body := *jsonText(t, c.Body)
	switch {
	case neverEmits && bodies[0] == body:
		t.Fatalf("the package sent a body it never emits: %s", body)
	case neverEmits && !strings.Contains(bodies[0], `"readVersion":"`+c.ReadVersion+`"`):
		t.Fatalf("read %s is not at the configured version", bodies[0])
	case !neverEmits && bodies[0] != body:
		t.Fatalf("read\n%s\nwant\n%s", bodies[0], body)
	}
}

func admissionReadsLoadCase(t *testing.T, f admissionReadsFixture, c admissionReadsCase, neverEmits bool) {
	var answers []string
	if c.Answer[0] == '[' {
		if err := json.Unmarshal(c.Answer, &answers); err != nil {
			t.Fatal(err)
		}
	} else if text := jsonText(t, c.Answer); text != nil {
		answers = []string{*text}
	}
	var bodies []string
	load := func(ctx context.Context, read PrincipalSkillLoadRead) ([]byte, error) {
		bodies = append(bodies, CanonicalJSON(read.Value()))
		if len(answers) < len(bodies) {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return []byte(answers[len(bodies)-1]), nil
	}
	var principal *OpenedPrincipal
	if c.Inputs.Chain != nil {
		principal = f.open(t, c.ReadVersion, c.Inputs.At, nil, load)
	}
	if c.Inputs.Restore != nil {
		skills, gate, state, rows := skillTestGate(t, principal, c.Inputs.Restore, c.Inputs.Restore, 0)
		if err := skills.HoldDelegation(context.Background()); err != nil {
			t.Fatal(err)
		}
		var expected []string
		if err := json.Unmarshal(c.Body, &expected); err != nil {
			t.Fatal(err)
		}
		var kept []string
		if err := json.Unmarshal(c.Expect.Loaded, &kept); err != nil {
			t.Fatal(err)
		}
		refusal, guardrail := lastSkillRefusal(gate)
		if refusal != "" {
			var refused []string
			for _, s := range c.Inputs.Restore {
				if !slices.Contains(kept, s.ID) {
					refused = append(refused, s.ID)
				}
			}
			// the restore's guardrail names the skill, in both languages
			if guardrail == nil || !slices.Contains(refused, guardrail.Tool) {
				t.Fatalf("restore: the guardrail names %v, not a refused skill of %v", guardrail, refused)
			}
		}
		if !slices.Equal(bodies, expected) || !slices.Equal(loadedIDs(state), kept) || refusal != c.Expect.Sender || len(skills.Current()) != len(kept) {
			t.Fatalf("restore: reads %v, kept %v, refusal %q", bodies, loadedIDs(state), refusal)
		}
		if held := (*rows)[0].Skills; held == nil || *held != 0 {
			t.Fatal("the restore claimed the skill budget")
		}
		return
	}
	catalogued := append([]PrincipalSkill{c.Inputs.Load.Skill}, c.Inputs.Load.Skills...)
	skills, gate, state, rows := skillTestGate(t, principal, catalogued, c.Inputs.Load.Skills, 0)
	_, err := skills.load(context.Background(), c.Inputs.Load.Skill.ID)
	verdict := "admit"
	if err != nil {
		verdict, _ = lastSkillRefusal(gate)
	}
	var loaded bool
	if err := json.Unmarshal(c.Expect.Loaded, &loaded); err != nil {
		t.Fatal(err)
	}
	if verdict != c.Expect.Sender || len(bodies) != c.Expect.Reads || slices.Contains(loadedIDs(state), c.Inputs.Load.Skill.ID) != loaded {
		t.Fatalf("got %s after %d reads, loaded %v", verdict, len(bodies), loadedIDs(state))
	}
	if held := (*rows)[0].Skills; held == nil || *held != 1 {
		t.Fatal("the load did not claim the skill budget")
	}
	if len(bodies) == 0 {
		return
	}
	body := *jsonText(t, c.Body)
	if neverEmits == (bodies[0] == body) {
		t.Fatalf("read\n%s\ncase body\n%s", bodies[0], body)
	}
}

// ADR-0030: at "1", and with no map, every read is as before it, byte for byte, whatever the call holds.
func TestReadVersionOneUnchanged(t *testing.T) {
	f := loadAdmissionReads(t)
	digest := "sha256:" + strings.Repeat("e", 64)
	var reads []string
	port := func(_ context.Context, read PrincipalAdmissionRead) ([]byte, error) {
		reads = append(reads, CanonicalJSON(read.Value()))
		return []byte(`{"admission":"admit"}`), nil
	}
	clock := func() (time.Time, error) { return time.Parse(time.RFC3339Nano, "2026-10-30T01:00:00.000Z") }
	call := PrincipalCall{Audience: &PrincipalAudience{Kind: "gateway", Name: "gateway:test"}, Source: "tickets/live", Tool: "query", Effect: "read-external", Version: &digest, Skills: func() []PrincipalSkill { return []PrincipalSkill{{ID: "expense-policy", Digest: digest}} }}
	for _, versions := range []map[string]string{nil, {"desk": "1"}, {"desk2": "2"}} {
		loads := 0
		principal, err := OpenPrincipal(&RunPrincipal{Chain: f.chain, Keys: f.keys, Software: f.software, Audience: "agent-host-1", Admission: port, Clock: clock, ReadVersions: versions, SkillLoad: func(context.Context, PrincipalSkillLoadRead) ([]byte, error) {
			loads++
			return []byte(`{"load":"admit"}`), nil
		}})
		if err != nil || principal.ReadVersion != "1" || principal.SkillLoadRead() {
			t.Fatalf("%v: %v", versions, err)
		}
		if _, err := principal.AdmitPrincipalCall(context.Background(), call, true); err != nil {
			t.Fatal(err)
		}
		if _, err := principal.AdmitPrincipalStanding(context.Background(), "read_context", "read", call.Skills); err != nil {
			t.Fatal(err)
		}
		skills, _, state, _ := skillTestGate(t, principal, []PrincipalSkill{{ID: "expense-policy", Digest: digest}}, nil, 0)
		if _, err := skills.load(context.Background(), "expense-policy"); err != nil || loads != 0 || len(state.Skills) != 1 {
			t.Fatalf("a load at \"1\": %v, %d skill-load reads", err, loads)
		}
	}
	want := `{"at":"2026-10-30T01:00:00.000Z","audience":{"kind":"gateway","name":"gateway:test"},"call":{"count":true,"effect":"read-external","source":"tickets/live","tool":"query"},"chain":["sha256:2da3f48b6ab326b9664ead97c1c777c63e059ea90ef78ee0fe391c1c4f88e429"],"host":"desk","readVersion":"1"}`
	if len(reads) != 6 || reads[0] != want || reads[2] != want || reads[4] != want || reads[1] != reads[3] || reads[1] != reads[5] {
		t.Fatalf("reads at \"1\" differ: %v", reads)
	}
	// An undelegated run makes no skill-load read at any version.
	skills, _, state, _ := skillTestGate(t, nil, []PrincipalSkill{{ID: "expense-policy", Digest: digest}}, nil, 0)
	if _, err := skills.load(context.Background(), "expense-policy"); err != nil || len(state.Skills) != 1 {
		t.Fatal(err)
	}
}

func TestReadVersionsOption(t *testing.T) {
	f := loadAdmissionReads(t)
	for _, versions := range []map[string]string{{"desk": "3"}, {"desk": ""}, {"desk": "2", "desk2": "02"}, {"a b": "2"}} {
		_, err := OpenPrincipal(&RunPrincipal{Chain: f.chain, Keys: f.keys, Software: f.software, Audience: "agent-host-1", Admission: principalAdmitAll, ReadVersions: versions})
		if !errors.Is(err, ErrPrincipalInputInvalid) {
			t.Fatalf("%v: %v", versions, err)
		}
	}
	principal := f.open(t, "2", "2026-10-30T01:00:00.000Z", nil, nil)
	if principal.ReadVersion != "2" || !principal.SkillLoadRead() || principal.HasSkillLoadPort() {
		t.Fatal("the root's host reads at \"2\"")
	}
	// "2" with a catalogued skill and no skill-load port is refused before any request; with none catalogued it is not.
	skills, _, _, _ := skillTestGate(t, principal, []PrincipalSkill{{ID: "expense-policy", Digest: "sha256:" + strings.Repeat("a", 64)}}, nil, 0)
	if err := skills.HoldDelegation(context.Background()); err == nil || !strings.Contains(err.Error(), "skill-load port") {
		t.Fatalf("no skill-load port: %v", err)
	}
	none, _, _, _ := skillTestGate(t, principal, nil, nil, 0)
	if err := none.HoldDelegation(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// The loader's order (ADR-0030): no read for a name not catalogued, an already loaded skill or a spent
// budget; the read before the checkpoint is written; a refusal keeps its claim, loads nothing and names
// itself on the guardrail; and a loop of refusals ends at the budget.
func TestSkillLoadReadPlace(t *testing.T) {
	f := loadAdmissionReads(t)
	a, b := PrincipalSkill{ID: "expense-policy", Digest: "sha256:" + strings.Repeat("a", 64)}, PrincipalSkill{ID: "issue-style", Digest: "sha256:" + strings.Repeat("b", 64)}
	var state *Execution
	reads := 0
	answer := `{"load":"admit"}`
	principal := f.open(t, "2", "2026-10-30T01:00:00.000Z", nil, func(_ context.Context, read PrincipalSkillLoadRead) ([]byte, error) {
		reads++
		if slices.Contains(loadedIDs(state), read.Skill.ID) {
			t.Error("the checkpoint was written before the read was answered")
		}
		return []byte(answer), nil
	})
	skills, gate, held, rows := skillTestGate(t, principal, []PrincipalSkill{a, b}, nil, 2)
	state = held
	if _, err := skills.load(context.Background(), "unknown"); err == nil || reads != 0 {
		t.Fatal("a name not catalogued was read")
	}
	if _, err := skills.load(context.Background(), a.ID); err != nil || reads != 1 || !slices.Equal(loadedIDs(state), []string{a.ID}) {
		t.Fatalf("admitted load: %v", err)
	}
	if _, err := skills.load(context.Background(), a.ID); err != nil || reads != 1 {
		t.Fatal("an already loaded skill was read")
	}
	answer = `{"load":"refuse","refusal":"skill-refused"}`
	if _, err := skills.load(context.Background(), b.ID); err == nil || reads != 2 || len(state.Skills) != 1 || len(skills.Current()) != 1 {
		t.Fatal("a refused load loaded")
	}
	name, guardrail := lastSkillRefusal(gate)
	if name != "skill-refused" || guardrail.Reason == nil || *guardrail.Reason != "delegation refused" || guardrail.Principal == nil || !strings.Contains(guardrail.Detail, "skill-refused") || !strings.Contains(guardrail.Detail, "Nothing was loaded") {
		t.Fatalf("guardrail: %+v", guardrail)
	}
	for _, event := range gate.notices {
		if event.SkillLoaded != nil && event.SkillLoaded.Skill.ID == b.ID {
			t.Fatal("skill_loaded was written for a refused load")
		}
	}
	if *(*rows)[0].Skills != 2 {
		t.Fatal("the refused load was not charged to the budget")
	}
	// The budget is spent: refused with no read.
	if _, err := skills.load(context.Background(), b.ID); err == nil || reads != 2 {
		t.Fatal("a spent budget was read")
	}
	if _, guardrail := lastSkillRefusal(gate); guardrail.Reason == nil || *guardrail.Reason != "skill budget" {
		t.Fatal("not refused for the budget")
	}
	// A model that loops on a refused skill causes at most skillsPerRun reads.
	loop, _, _, loopRows := skillTestGate(t, principal, []PrincipalSkill{b}, nil, 4)
	reads = 0
	for range 9 {
		_, _ = loop.load(context.Background(), b.ID)
	}
	if reads != 4 || *(*loopRows)[0].Skills != 4 {
		t.Fatalf("a loop of refusals made %d reads", reads)
	}
}

// ADR-0030: a call's read takes the loaded set under the skill gate's lock, so it waits for a skill-load
// read in flight and sees its load.
func TestSkillsTakenUnderTheGateLock(t *testing.T) {
	f := loadAdmissionReads(t)
	a := PrincipalSkill{ID: "expense-policy", Digest: "sha256:" + strings.Repeat("a", 64)}
	asked, release := make(chan struct{}), make(chan struct{})
	principal := f.open(t, "2", "2026-10-30T01:00:00.000Z", nil, func(context.Context, PrincipalSkillLoadRead) ([]byte, error) {
		close(asked)
		<-release
		return []byte(`{"load":"admit"}`), nil
	})
	skills, gate, _, _ := skillTestGate(t, principal, []PrincipalSkill{a}, nil, 0)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = skills.load(context.Background(), a.ID)
	}()
	<-asked
	seen := make(chan []PrincipalSkill, 1)
	go func() { seen <- gate.PrincipalSkills() }()
	select {
	case got := <-seen:
		t.Fatalf("the set was read while a load was half done: %v", got)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	wg.Wait()
	if got := <-seen; len(got) != 1 || got[0] != a {
		t.Fatalf("the read did not see the load: %v", got)
	}
}

// ADR-0030: the child link request stays at "1" at either setting, the child inherits its parent's
// version, and a child's gate names its own loaded set, never its parent's.
func TestChildReadsAndItsOwnSkills(t *testing.T) {
	f := loadAdmissionReads(t)
	parentSkill := PrincipalSkill{ID: "expense-policy", Digest: "sha256:" + strings.Repeat("a", 64)}
	var standing []PrincipalAdmissionRead
	var requests []string
	port := func(_ context.Context, read PrincipalAdmissionRead) ([]byte, error) {
		standing = append(standing, read)
		return []byte(`{"admission":"admit"}`), nil
	}
	input := &RunPrincipal{Chain: f.chain, Keys: f.keys, Software: f.software, Audience: "agent-host-1", Admission: port, ReadVersions: map[string]string{"desk": "2"},
		Clock: func() (time.Time, error) { return time.Parse(time.RFC3339Nano, "2026-10-30T01:00:00.000Z") },
		Child: func(_ context.Context, request PrincipalChildRequest) ([]byte, error) {
			requests = append(requests, CanonicalJSON(request.Value()))
			return []byte(`{"refusal":"not-parent-actor"}`), nil
		}}
	parent, err := OpenPrincipal(input)
	if err != nil {
		t.Fatal(err)
	}
	_, err = NarrowChild(context.Background(), parent, "task-1", "review", nil, func() []PrincipalSkill { return []PrincipalSkill{parentSkill} })
	var refused *DelegationRefused
	if !errors.As(err, &refused) || len(requests) != 1 || !strings.Contains(requests[0], `"readVersion":"1"`) {
		t.Fatalf("child link request: %v %v", err, requests)
	}
	if len(standing) != 1 || standing[0].ReadVersion != "2" || !slices.Equal(standing[0].Call.Skills, []PrincipalSkill{parentSkill}) {
		t.Fatalf("the parent's standing read: %+v", standing)
	}
	parentGate := &SourceGate{skillSet: func() []PrincipalSkill { return []PrincipalSkill{parentSkill} }}
	child := &SourceGate{parentGate: parentGate, principal: parent}
	if got := child.PrincipalSkills(); len(got) != 0 {
		t.Fatalf("a child named its parent's skills: %v", got)
	}
	own := PrincipalSkill{ID: "issue-style", Digest: "sha256:" + strings.Repeat("b", 64)}
	child.skillSet = func() []PrincipalSkill { return []PrincipalSkill{own} }
	if got := child.PrincipalSkills(); len(got) != 1 || got[0] != own {
		t.Fatalf("a child's own set: %v", got)
	}
}

func TestApprovedVersionFromTheSnapshot(t *testing.T) {
	raw, err := os.ReadFile("../../contract/testdata/descriptor-snapshot/v3-v4.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Versions []struct {
			SnapshotVersion, SnapshotText string
			DescriptorDigests             map[string]string
		}
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	tool := func(name string) *string { return &name }
	for _, version := range v.Versions {
		for name, digest := range version.DescriptorDigests {
			entry := &SourceEntry{Record: contract.ToolOfferSourceRecord{Tool: tool(name)}, Declared: HostTool{Source: &ToolSource{DescriptorSnapshot: []byte(version.SnapshotText),
				CurrentDescriptorSnapshot: func(context.Context) ([]byte, error) { return []byte("{}"), nil }}}}
			if got := approvedVersion(entry); got == nil || *got != digest {
				t.Fatalf("version %s, tool %s: %v", version.SnapshotVersion, name, got)
			}
		}
	}
	v2, err := os.ReadFile("../../contract/vectors/tool-offer/descriptor-snapshot.json")
	if err != nil {
		t.Fatal(err)
	}
	var shared struct{ SnapshotText string }
	_ = json.Unmarshal(v2, &shared)
	for _, entry := range []*SourceEntry{
		{Record: contract.ToolOfferSourceRecord{Tool: tool("search")}, Declared: HostTool{Source: &ToolSource{DescriptorSnapshot: []byte(shared.SnapshotText)}}},
		{Record: contract.ToolOfferSourceRecord{Tool: tool("search")}, Declared: HostTool{Source: &ToolSource{}}},
		{Record: contract.ToolOfferSourceRecord{Tool: tool("search")}},
	} {
		if got := approvedVersion(entry); got != nil {
			t.Fatalf("a version for an approval that records none: %v", *got)
		}
	}
}

func TestDescriptorSnapshotVersionsThreeAndFour(t *testing.T) {
	raw, err := os.ReadFile("../../contract/testdata/descriptor-snapshot/v3-v4.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Versions []struct {
			SnapshotVersion, SnapshotText, SnapshotDigest, Platform, Binding, Operation string
			Endpoint                                                                    *string
			Tools                                                                       map[string]*ToolTaxonomy
			DescriptorDigests                                                           map[string]string
			Refusals, Accepts                                                           []struct{ Name, Find, Replace string }
		}
	}
	if err := json.Unmarshal(raw, &v); err != nil || len(v.Versions) != 2 {
		t.Fatal(err)
	}
	for _, version := range v.Versions {
		approval := &DescriptorApproval{Platform: version.Platform, Binding: version.Binding, Operation: version.Operation, Endpoint: version.Endpoint, ToolTaxonomies: version.Tools}
		accept := func(text string) bool {
			snapshot, err := ReadDescriptorSnapshot([]byte(text))
			return err == nil && DescriptorApprovalProblem(snapshot, approval, "", nil) == ""
		}
		snapshot, err := ReadDescriptorSnapshot([]byte(version.SnapshotText))
		digest, _ := DescriptorSnapshotDigest([]byte(version.SnapshotText))
		if err != nil || !accept(version.SnapshotText) || digest != version.SnapshotDigest || snapshot.SnapshotVersion != version.SnapshotVersion || (version.SnapshotVersion == "4") != (snapshot.Executable != nil) {
			t.Fatalf("version %s refused: %v", version.SnapshotVersion, err)
		}
		for name, digest := range version.DescriptorDigests {
			if snapshot.Tools[name].DescriptorDigest != digest {
				t.Fatalf("version %s, tool %s: digest %s", version.SnapshotVersion, name, snapshot.Tools[name].DescriptorDigest)
			}
		}
		for _, edit := range version.Accepts {
			if strings.Count(version.SnapshotText, edit.Find) != 1 || !accept(strings.Replace(version.SnapshotText, edit.Find, edit.Replace, 1)) {
				t.Fatalf("version %s: %s refused", version.SnapshotVersion, edit.Name)
			}
		}
		for _, edit := range version.Refusals {
			if strings.Count(version.SnapshotText, edit.Find) != 1 || accept(strings.Replace(version.SnapshotText, edit.Find, edit.Replace, 1)) {
				t.Fatalf("version %s: %s accepted", version.SnapshotVersion, edit.Name)
			}
		}
	}
}

// The four names a run at "2" records are in the contract the record readers hold.
func TestReadVersionTwoRefusalsAreRecorded(t *testing.T) {
	for _, name := range []string{"tool-version-refused", "skill-refused", "read-version-unsupported", "read-malformed"} {
		reason := "delegation refused"
		three := int64(3)
		event := contract.RunEvent{Guardrail: &contract.RunEventGuardrail{Version: &three, Type: "guardrail", Tool: LoadSkill, Action: "refused", Detail: "synthetic", Reason: &reason, Refusal: PrincipalRefusal{Name: name}.Contract(),
			Principal: PrincipalRecord{Agent: strings.Repeat("a", 32), Host: "desk", RootActor: strings.Repeat("a", 32), Software: PrincipalSoftware{Package: "judgment-pack-agent", Version: "0.13.0"}, Subject: PrincipalReference{Kind: "person", Issuer: "https://login.example.com/", Subject: "approver@example.com"}, DelegationID: strings.Repeat("1", 32), DelegationHash: "sha256:" + strings.Repeat("2", 64), Chain: []string{"sha256:" + strings.Repeat("2", 64)}}.Contract()}}
		data, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		if err := contract.CheckRecord("event", data); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

// ADR-0030: a standing read at "2" the issuing host did not read is reported by its name after that one
// read, never resent at "1".
func TestStandingReadUnreadNeverResent(t *testing.T) {
	f := loadAdmissionReads(t)
	for _, name := range []string{"read-version-unsupported", "read-malformed"} {
		var versions []string
		principal := f.open(t, "2", "2026-10-30T01:00:00.000Z", func(_ context.Context, read PrincipalAdmissionRead) ([]byte, error) {
			versions = append(versions, read.ReadVersion)
			return []byte(`{"refusal":"` + name + `"}`), nil
		}, nil)
		got, err := principal.AdmitPrincipalStanding(context.Background(), "read_context", "read", nil)
		if err != nil || got == nil || got.Name != name || !slices.Equal(versions, []string{"2"}) {
			t.Fatalf("%s: got %+v, %v, after reads at %v", name, got, err, versions)
		}
	}
}

// A route's preload of a skill the delegation refuses skips it, with the guardrail that names it,
// and the run goes on (ADR-0030): the next skill still loads, and the refused one keeps its one claim
// and claims nothing more.
func TestRoutePreloadSkipsARefusedSkill(t *testing.T) {
	f := loadAdmissionReads(t)
	refused := PrincipalSkill{ID: "expense-policy", Digest: "sha256:" + strings.Repeat("a", 64)}
	next := PrincipalSkill{ID: "issue-style", Digest: "sha256:" + strings.Repeat("b", 64)}
	principal := f.open(t, "2", "2026-10-30T01:00:00.000Z", nil, func(_ context.Context, read PrincipalSkillLoadRead) ([]byte, error) {
		if read.Skill.ID == refused.ID {
			return []byte(`{"load":"refuse","refusal":"skill-refused"}`), nil
		}
		return []byte(`{"load":"admit"}`), nil
	})
	skills, gate, state, rows := skillTestGate(t, principal, []PrincipalSkill{refused, next}, nil, 0)
	for _, s := range []PrincipalSkill{refused, next} {
		if err := skills.preload(context.Background(), s.ID); err != nil {
			t.Fatalf("preload %s: %v", s.ID, err)
		}
	}
	if got := loadedIDs(state); !slices.Equal(got, []string{next.ID}) {
		t.Fatalf("loaded %v", got)
	}
	found := false
	for _, n := range gate.notices {
		if g := n.Guardrail; g != nil && g.Refusal != nil && g.Refusal.Name == "skill-refused" {
			found = true
		}
	}
	if !found {
		t.Fatal("no guardrail names the refused preload")
	}
	if held := (*rows)[0].Skills; held == nil || *held != 2 {
		t.Fatalf("the preloads claimed %v, want 2: one each", held)
	}
}
