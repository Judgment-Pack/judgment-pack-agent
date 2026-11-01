package runcontext

import (
	"encoding/json"
	"errors"
	"math"
	"sync"
	"testing"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

func TestCheckpointMutationAtomicRevisionAndDetachedReads(t *testing.T) {
	c := &ExecutionController{}
	context := contract.CheckpointContext{Scope: "mutation", Entries: []contract.CheckpointRetainedEntry{}}
	if err := c.MutateCheckpoint(CheckpointPatch{"Context": context, "Used": 0}); err != nil {
		t.Fatal(err)
	}
	if c.CheckpointRevision() != 1 {
		t.Fatal("initial revision")
	}
	c.MutateCheckpoint(CheckpointPatch{"Used": 0})
	if c.CheckpointRevision() != 1 {
		t.Fatal("no-op dirtied state")
	}
	steers := &contract.ProvenanceSteerReport{Queued: []contract.ProvenanceQueuedSteer{{Text: "continue", Bytes: 8}}, Delivered: []contract.ProvenanceDeliveredSteer{}}
	c.MutateCheckpoint(CheckpointPatch{"Steers": steers, "Used": 1})
	if c.CheckpointRevision() != 2 {
		t.Fatal("multi-member patch revised twice")
	}
	steers.Queued[0].Text = "changed"
	view := c.State()
	view.Steers.Queued[0].Text = "leaked"
	view.Context.Scope = "leaked"
	if c.State().Steers.Queued[0].Text != "continue" || c.State().Context.Scope != "mutation" {
		t.Fatal("mutable alias escaped")
	}
	next := c.State().Steers
	next.Queued[0].Text = "next"
	next.Queued[0].Bytes = 4
	c.MutateCheckpoint(CheckpointPatch{"Steers": next})
	if c.CheckpointRevision() != 3 {
		t.Fatal("nested change missed")
	}
	err := c.MutateCheckpoint(CheckpointPatch{"Used": 2}, func(candidate *Execution) error { candidate.Context.Scope = "leaked"; return errors.New("invalid") })
	if err == nil || c.CheckpointRevision() != 3 || c.State().Used != 1 || c.State().Context.Scope != "mutation" {
		t.Fatal("rejected patch changed state")
	}
	for _, patch := range []CheckpointPatch{{"NoSuchMember": 1}, {"Used": "invalid"}, {"Used": CheckpointIncrement(math.MaxInt64)}} {
		if c.MutateCheckpoint(patch) == nil || c.CheckpointRevision() != 3 {
			t.Fatal("invalid patch committed")
		}
	}
	reason := ""
	c.MutateCheckpoint(CheckpointPatch{"Reason": &reason})
	if c.State().Reason == nil {
		t.Fatal("empty reason omitted")
	}
	c.MutateCheckpoint(CheckpointPatch{"Reason": nil})
	if c.State().Reason != nil {
		t.Fatal("reason not removed")
	}
}

func memberCheckpoint(t *testing.T) (*Value, *contract.Checkpoint) {
	t.Helper()
	value := readJSON(t, "contract/vectors/checkpoint/members.json").Get("items").Items()[0].Clone()
	saved, err := ReadExecution(value)
	if err != nil {
		t.Fatal(err)
	}
	return value, saved
}
func memberController(t *testing.T, saved *contract.Checkpoint) *ExecutionController {
	t.Helper()
	session := Session{Prompt: "checkpoint request", Model: &ModelSession{Family: "gemini", Model: "fixture"}, Context: &Input{Execution: saved}}
	if saved != nil && saved.RequestCap != nil {
		cap := int(*saved.RequestCap)
		session.Context.RequestCap = &cap
	}
	c := NewExecutionController(session, NewContextLedger(&Defaults, nil), nil)
	if err := c.Initialize(); err != nil {
		t.Fatal(err)
	}
	return c
}
func memberOwnership(t *testing.T) *Value {
	t.Helper()
	schema := readJSON(t, "contract/schemas/checkpoint.json").Get("properties")
	vector := readJSON(t, "contract/vectors/checkpoint/members.json")
	owners := vector.Get("probeOwners")
	fixture := vector.Get("items").Items()[0]
	if owners.Len() != schema.Len() || fixture.Len() != schema.Len() {
		t.Fatal("schema member coverage inventory differs")
	}
	for _, name := range schema.Keys() {
		owner := owners.Get(name)
		if owner == nil || fixture.Get(name) == nil {
			t.Fatalf("uncovered schema member %s", name)
		}
		expected := "controller"
		switch name {
		case "schema", "digest", "retainUntil", "records":
			expected = "envelope"
		case "handoff":
			expected = "delegation"
		}
		if owner.Str() != expected {
			t.Fatalf("%s: wrong owning probe", name)
		}
	}
	return owners
}
func TestCheckpointMembersInterruptResume(t *testing.T) {
	owners := memberOwnership(t)
	value, saved := memberCheckpoint(t)
	first := memberController(t, saved)
	interrupted, err := first.Snapshot("stopped")
	if err != nil {
		t.Fatal(err)
	}
	resumed := memberController(t, &interrupted)
	continued, err := resumed.Snapshot("running")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(continued)
	result, err := ParseJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range owners.Keys() {
		if owners.Get(name).Str() != "controller" || name == "status" {
			continue
		}
		if name == "version" {
			// ADR-0029: the fixture is a delegated run's checkpoint 4.
			if value.Get(name).Num() != 4 || interrupted.Version != 4 || continued.Version != 4 {
				t.Fatal("current checkpoint version changed across interruption/resume")
			}
			continue
		}
		if CanonicalJSON(result.Get(name)) != CanonicalJSON(value.Get(name)) {
			t.Errorf("%s changed across interruption/resume: %s versus %s", name, CanonicalJSON(result.Get(name)), CanonicalJSON(value.Get(name)))
		}
	}
	if interrupted.Status != "stopped" || continued.Status != saved.Status {
		t.Fatal("status projection did not resume running")
	}
	// Snapshot is the owning envelope writer. Its storage pass regenerates digest and redactions.
	if continued.Schema == nil || *continued.Schema != CheckpointSchema || continued.RetainUntil == nil || *continued.RetainUntil != *saved.RetainUntil || continued.Records == nil || len(continued.Records.Redactions) != 0 {
		t.Fatal("envelope projection did not survive resume")
	}
	if continued.Digest == nil || *continued.Digest != RecordDigest(result) {
		t.Fatal("snapshot digest is not the continued record digest")
	}
	legacy := Object()
	for _, key := range value.Keys() {
		if owners.Get(key).Str() != "envelope" {
			legacy.Set(key, value.Get(key).Clone())
		}
	}
	legacySaved, err := ReadExecution(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if legacySaved.Schema != nil || legacySaved.Digest != nil || legacySaved.RetainUntil != nil || legacySaved.Records != nil {
		t.Fatal("legacy envelope omission changed")
	}
	renewed, err := memberController(t, legacySaved).Snapshot("stopped")
	if err != nil {
		t.Fatal(err)
	}
	if renewed.Schema == nil || renewed.Digest == nil || renewed.RetainUntil == nil || renewed.Records == nil {
		t.Fatal("legacy snapshot envelope not regenerated")
	}
}
func TestCheckpointMembersOmissionVersusDefaults(t *testing.T) {
	owners := memberOwnership(t)
	schema := readJSON(t, "contract/schemas/checkpoint.json")
	required := map[string]bool{}
	for _, item := range schema.Get("required").Items() {
		required[item.Str()] = true
	}
	for _, name := range owners.Keys() {
		if required[name] || owners.Get(name).Str() != "controller" {
			continue
		}
		t.Run(name, func(t *testing.T) {
			value, _ := memberCheckpoint(t)
			without := Object()
			for _, key := range value.Keys() {
				if key != name {
					without.Set(key, value.Get(key).Clone())
				}
			}
			// ADR-0029: a checkpoint without its principal is an undelegated run's, version 3.
			if name == "principal" {
				without.Set("version", Number(3))
			}
			if name == "planState" {
				step := value.Get("plan").Items()[0]
				without.Set("plan", Array(ObjectOf("step", step.Get("step"), "status", step.Get("status"), "detail", step.Get("detail"))))
			}
			value = without
			original := CanonicalJSON(value)
			saved, err := ReadExecution(value)
			if err != nil {
				t.Fatal(err)
			}
			c := memberController(t, saved)
			if name == "planState" {
				state := c.State().Value()
				if CanonicalJSON(value) != original || saved.PlanState != nil || state.Get("planState").Get("sequence").Num() != 0 || state.Get("planState").Get("history").Len() != 0 || state.Get("planState").Get("revision").Str() != readJSON(t, "contract/vectors/checkpoint/members.json").Get("items").Items()[0].Get("planState").Get("revision").Str() || state.Get("plan").Items()[0].Get("id").Str() != "step-1" {
					t.Fatal("legacy plan read changed or current-write migration was not deterministic")
				}
				interrupted, err := c.Snapshot("stopped")
				if err != nil {
					t.Fatal(err)
				}
				continued := memberController(t, &interrupted)
				if CanonicalJSON(continued.State().PlanState) != CanonicalJSON(c.State().PlanState) {
					t.Fatal("migrated plan state changed on second resume")
				}
				return
			}
			if c.State().Value().Get(name) != nil {
				t.Fatalf("omitted %s synthesized on resume", name)
			}
		})
	}
	_, saved := memberCheckpoint(t)
	resumed := memberController(t, saved)
	if resumed.State().Reason == nil || *resumed.State().Reason != "" || resumed.State().TestCalls == nil || *resumed.State().TestCalls != 0 {
		t.Fatal("empty or zero default omitted")
	}
	value := resumed.State().Value()
	if value.Get("policy").Get("models.author.reasoning").Get("value") != nil {
		t.Fatal("omitted policy value synthesized")
	}
}

func TestCheckpointConcurrentCounterUpdates(t *testing.T) {
	c := &ExecutionController{}
	c.MutateCheckpoint(CheckpointPatch{"Used": 0})
	const writers = 16
	const each = 100
	var group sync.WaitGroup
	for i := 0; i < writers; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for n := 0; n < each; n++ {
				if err := c.MutateCheckpoint(CheckpointPatch{"Used": CheckpointIncrement(1)}); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	group.Wait()
	if c.State().Used != writers*each || c.CheckpointRevision() != 1+writers*each {
		t.Fatalf("lost concurrent update: %d, revision %d", c.State().Used, c.CheckpointRevision())
	}
}

func TestCheckpointReorderedObjectIsNoop(t *testing.T) {
	c := &ExecutionController{}
	first, _ := ParseJSON([]byte(`{"first":1,"second":{"a":true,"b":null}}`))
	second, _ := ParseJSON([]byte(`{"second":{"b":null,"a":true},"first":1}`))
	c.MutateCheckpoint(CheckpointPatch{"Delegation": first})
	revision := c.CheckpointRevision()
	c.MutateCheckpoint(CheckpointPatch{"Delegation": second})
	if c.CheckpointRevision() != revision || c.State().Delegation.Text() != first.Text() {
		t.Fatal("reordered object dirtied or rewrote state")
	}
	if c.MutateCheckpoint(CheckpointPatch{"Version": CheckpointIncrement(1)}) == nil || c.CheckpointRevision() != revision {
		t.Fatal("invalid increment committed")
	}
}

func TestCheckpointSuccessfulValidatorCannotChangeCommittedCandidate(t *testing.T) {
	c := &ExecutionController{}
	c.MutateCheckpoint(CheckpointPatch{"Used": 1})
	err := c.MutateCheckpoint(CheckpointPatch{"Used": 2}, func(candidate *Execution) error {
		candidate.Used = 9
		return nil
	})
	if err != nil || c.State().Used != 2 || c.CheckpointRevision() != 2 {
		t.Fatalf("successful validator changed committed candidate: used=%d revision=%d error=%v", c.State().Used, c.CheckpointRevision(), err)
	}
}

func TestCheckpointSourcePublicationHoldsOwnerLock(t *testing.T) {
	policy := Defaults
	declared := &Declared{Entries: map[string]*SourceEntry{}}
	makeContext := func() *SessionContext {
		execution := &ExecutionController{}
		if err := execution.MutateCheckpoint(CheckpointPatch{"Used": 0}); err != nil {
			t.Fatal(err)
		}
		return &SessionContext{Policy: &policy, Execution: execution, Marker: "source-publication"}
	}
	parent := makeContext()
	parent.OpenSources(declared, false, "", nil)
	start := make(chan struct{})
	var group sync.WaitGroup
	group.Add(2)
	go func() {
		defer group.Done()
		<-start
		for i := 0; i < 1200; i++ {
			parent.Sources.RuntimeRefused()
		}
	}()
	go func() {
		defer group.Done()
		<-start
		for i := 0; i < 120; i++ {
			makeContext().OpenSources(declared, false, "", parent.Sources)
		}
	}()
	close(start)
	group.Wait()
	if rows := parent.Execution.State().Sources; len(rows) != 1 || rows[0].Refused != 1200 {
		t.Fatalf("shared source publication lost counts: %+v", rows)
	}
}
