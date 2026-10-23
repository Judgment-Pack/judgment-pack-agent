package runcontext

import (
	"context"
	"encoding/json"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"reflect"
	"strings"
	"testing"
)

const metricDigest = "sha256:dd407b2b50d5735761059db743e2d628f0f6b17585ec025089e82380986dcff9"

func lifecycleRow(state string) contract.ProvenanceMemoryRecall {
	return contract.ProvenanceMemoryRecall{ID: "metric", Digest: metricDigest, Kind: "preference", State: state}
}
func lifecycleUpdate() contract.MemoryLifecycleUpdate {
	return contract.MemoryLifecycleUpdate{ID: "metric", Digest: metricDigest, Kind: "preference", State: "revoked"}
}
func lifecycleRecord() contract.Memory {
	return contract.Memory{ID: "metric", Digest: metricDigest, Kind: "preference", Text: "Use metric units.", Source: contract.MemorySource{Run: "fixture-host", ProposedBy: "host"}, Scope: "project", CreatedAt: "2026-10-01T00:00:00Z", State: "accepted"}
}
func lifecycleGate(t *testing.T, rows []contract.ProvenanceMemoryRecall) *MemoryGate {
	t.Helper()
	c := &SessionContext{Execution: &ExecutionController{}, Policy: &Defaults}
	c.Execution.MutateCheckpoint(CheckpointPatch{"ID": "fixture-run"})
	g := &MemoryGate{session: c, book: contract.ProvenanceMemories{Proposed: []contract.Memory{}, Recalled: rows}}
	c.Memories = g
	if err := c.Execution.MutateCheckpoint(CheckpointPatch{"Memories": &g.book}); err != nil {
		t.Fatal(err)
	}
	return g
}
func TestMemoryLifecycleSharedAnswers(t *testing.T) {
	vector := readJSON(t, "contract/vectors/memory/lifecycle-answer.json")
	for _, answer := range vector.Get("items").Items() {
		var input any
		if err := json.Unmarshal(answer.JSON(), &input); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadMemoryLifecycle(input); err != nil {
			t.Fatal(err)
		}
	}
	for _, item := range vector.Get("refused").Items() {
		var input any
		json.Unmarshal(item.Get("answer").JSON(), &input)
		if _, err := ReadMemoryLifecycle(input); err == nil {
			t.Fatalf("admitted %s", item.Get("name").Str())
		}
	}
	oversized := []contract.MemoryLifecycleUpdate{}
	for i := 0; i < 257; i++ {
		update := lifecycleUpdate()
		update.ID = string(rune(1000 + i))
		oversized = append(oversized, update)
	}
	for _, answer := range []any{oversized, false, map[string]any{}, []any{nil}} {
		if _, err := ReadMemoryLifecycle(answer); err == nil {
			t.Fatal("malformed lifecycle admitted")
		}
	}
	if answer, err := ReadMemoryLifecycle(nil); err != nil || len(answer) != 0 {
		t.Fatal("nil is no answer")
	}
}
func TestMemoryLifecycleSharedTransitions(t *testing.T) {
	vector := readJSON(t, "contract/vectors/memory/lifecycle-transitions.json")
	for _, input := range vector.Get("cases").Items() {
		t.Run(input.Get("name").Str(), func(t *testing.T) {
			prior := []contract.ProvenanceMemoryRecall{lifecycleRow(input.Get("prior").Str())}
			records := []contract.Memory{}
			if full := input.Get("full"); full != nil {
				record := lifecycleRecord()
				record.State = full.Str()
				if kind := input.Get("fullKind"); kind != nil {
					record.Kind = kind.Str()
				}
				if text := input.Get("fullText"); text != nil {
					record.Text = text.Str()
				}
				if digest := input.Get("fullDigest"); digest != nil {
					record.Digest = digest.Str()
				}
				if expires := input.Get("expiresAt"); expires != nil {
					stamp := expires.Str()
					record.ExpiresAt = &stamp
				}
				records = append(records, record)
			}
			recall, err := RecallMemory(records, 4000, "marker")
			if err != nil {
				t.Fatal(err)
			}
			updates := []contract.MemoryLifecycleUpdate{}
			if state := input.Get("state"); state != nil {
				update := lifecycleUpdate()
				update.State = state.Str()
				if id := input.Get("id"); id != nil {
					update.ID = id.Str()
				}
				if digest := input.Get("digest"); digest != nil {
					update.Digest = digest.Str()
				}
				if kind := input.Get("kind"); kind != nil {
					update.Kind = kind.Str()
				}
				updates = append(updates, update)
			}
			updates, err = ReadMemoryLifecycle(updates)
			if err != nil {
				t.Fatal(err)
			}
			next, err := nextMemoryRecall(prior, recall, updates)
			if result := input.Get("result"); result.IsString() {
				if err != nil || !reflect.DeepEqual(next, []contract.ProvenanceMemoryRecall{lifecycleRow(result.Str())}) {
					t.Fatalf("next=%+v error=%v", next, err)
				}
			} else if err == nil {
				t.Fatal("invalid transition admitted")
			}
		})
	}
}
func TestMemoryLifecycleAtomicityProposalPreservationAndStaleState(t *testing.T) {
	second := lifecycleRow("accepted")
	second.ID = "second"
	g := lifecycleGate(t, []contract.ProvenanceMemoryRecall{lifecycleRow("accepted"), second})
	prior := g.recalledSnapshot()
	revision := g.session.Execution.CheckpointRevision()
	recall, _ := RecallMemory([]contract.Memory{}, 4000, "marker")
	for _, last := range []contract.MemoryLifecycleUpdate{{ID: "unknown", Digest: metricDigest, Kind: "preference", State: "revoked"}, {ID: "second", Digest: "sha256:" + strings.Repeat("0", 64), Kind: "preference", State: "revoked"}, {ID: "second", Digest: metricDigest, Kind: "fact", State: "revoked"}} {
		next, err := nextMemoryRecall(prior, recall, []contract.MemoryLifecycleUpdate{lifecycleUpdate(), last})
		if err == nil {
			err = g.commitRecall(prior, next, recall)
		}
		if err == nil || g.session.Execution.CheckpointRevision() != revision || !reflect.DeepEqual(g.recalledSnapshot(), prior) || len(g.notices) != 0 {
			t.Fatal("valid-first invalid-later answer leaked")
		}
	}
	// The proposal owner advances while lifecycle preparation is awaiting its host.
	done := make(chan error, 1)
	go func() {
		_, err := g.Tool().Execute(context.Background(), json.RawMessage(`{"kind":"fact","text":"A fact."}`))
		done <- err
	}()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	next, err := nextMemoryRecall(prior, recall, []contract.MemoryLifecycleUpdate{lifecycleUpdate()})
	if err != nil {
		t.Fatal(err)
	}
	if err = g.commitRecall(prior, next, recall); err != nil {
		t.Fatal(err)
	}
	if len(g.session.Execution.State().Memories.Proposed) != 1 {
		t.Fatal("concurrent proposal lost")
	}
	revision = g.session.Execution.CheckpointRevision()
	if err = g.commitRecall(next, next, recall); err != nil || g.session.Execution.CheckpointRevision() != revision {
		t.Fatal("terminal replay dirtied state")
	}
	if err = g.commitRecall(prior, next, recall); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatal("stale recalled state admitted")
	}
}
func TestMemoryLifecycleCannotCreateIdentityAndBoundsFinalBook(t *testing.T) {
	recall, _ := RecallMemory([]contract.Memory{lifecycleRecord()}, 4000, "marker")
	if _, err := nextMemoryRecall(nil, recall, []contract.MemoryLifecycleUpdate{lifecycleUpdate()}); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatal("same-answer identity created by lifecycle")
	}
	rows := []contract.ProvenanceMemoryRecall{}
	for i := 0; i < 256; i++ {
		row := lifecycleRow("accepted")
		row.ID = string(rune(1000 + i))
		rows = append(rows, row)
	}
	g := lifecycleGate(t, rows)
	revision := g.session.Execution.CheckpointRevision()
	next, err := nextMemoryRecall(rows, recall, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = g.commitRecall(rows, next, recall); err == nil || len(g.recalledSnapshot()) != 256 || g.session.Execution.CheckpointRevision() != revision || len(g.notices) != 0 {
		t.Fatal("full report partially committed")
	}
}
func TestMemoryLifecyclePublicResumeDeletedTextAndDisabledRecall(t *testing.T) {
	s := modelSession()
	s.Critic = "off"
	s.Context = &Input{Phase: "plan"}
	s.Host = HostFuncs{MemoryRecordsForFunc: func(context.Context, string) ([]contract.Memory, error) {
		return []contract.Memory{lifecycleRecord()}, nil
	}}
	run, err := ContextualSession(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := run.Context.Snapshot("stopped")
	run.Context.Close()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(saved)
	value, err := ParseJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := ReadExecution(value)
	if err != nil {
		t.Fatal(err)
	}
	for _, enabled := range []bool{true, false} {
		textCalls, lifeCalls := 0, 0
		var retained []contract.MemoryLifecycleUpdate
		s.Context = &Input{Phase: "plan", Execution: checkpoint, Memory: &enabled}
		s.Host = HostFuncs{MemoryRecordsForFunc: func(context.Context, string) ([]contract.Memory, error) {
			textCalls++
			if !enabled {
				t.Error("disabled text read")
			}
			return []contract.Memory{}, nil
		}, MemoryLifecycleFunc: func(_ context.Context, metadata []contract.ProvenanceMemoryRecall) (any, error) {
			lifeCalls++
			if !reflect.DeepEqual(metadata, []contract.ProvenanceMemoryRecall{lifecycleRow("accepted")}) {
				t.Error("wrong canonical metadata")
			}
			data, _ := json.Marshal(metadata)
			if strings.Contains(string(data), "Use metric units.") {
				t.Error("text in lifecycle request")
			}
			metadata[0].Digest = "sha256:" + strings.Repeat("0", 64)
			retained = []contract.MemoryLifecycleUpdate{lifecycleUpdate()}
			return retained, nil
		}}
		resumed, err := ContextualSession(context.Background(), s)
		if err != nil {
			t.Fatal(err)
		}
		retained[0].State = "expired"
		wantCalls := 0
		if enabled {
			wantCalls = 1
		}
		if textCalls != wantCalls || lifeCalls != 1 || strings.Contains(resumed.Context.System("guidance", ""), "Use metric units.") || !reflect.DeepEqual(resumed.Context.Memories.recalledSnapshot(), []contract.ProvenanceMemoryRecall{lifecycleRow("revoked")}) {
			t.Fatal("deleted-text resume failed")
		}
		for _, event := range resumed.Context.Memories.TakeNotices() {
			if event.MemoryRecalled != nil {
				t.Fatal("lifecycle fabricated prompt recall")
			}
		}
		again, err := resumed.Context.Snapshot("stopped")
		resumed.Context.Close()
		if err != nil {
			t.Fatal(err)
		}
		raw, _ = json.Marshal(again)
		value, _ = ParseJSON(raw)
		reloaded, err := ReadExecution(value)
		if err != nil || reloaded.Memories.Recalled[0].State != "revoked" {
			t.Fatal("terminal report did not reload")
		}
	}
}
func TestMemoryLifecycleCanonicalRefusalsAndAliases(t *testing.T) {
	for _, answer := range []any{"", []any{map[string]any{}}, []contract.MemoryLifecycleUpdate{lifecycleUpdate()}, make([]contract.MemoryLifecycleUpdate, 257)} {
		s := modelSession()
		s.Host = HostFuncs{MemoryLifecycleFunc: func(context.Context, []contract.ProvenanceMemoryRecall) (any, error) { return answer, nil }}
		if opened, err := ContextualSession(context.Background(), s); err == nil {
			opened.Context.Close()
			t.Fatal("invalid lifecycle admitted")
		} else {
			failures := 0
			for _, event := range FailedToOpen(err) {
				if event.Error != nil {
					failures++
					if event.Error.Failure == nil || event.Error.Failure.Code != "configuration" || event.Error.Failure.Outcome != "not-started" {
						t.Fatalf("wrong failure contract %+v; cause %v", event.Error.Failure, err)
					}
					raw, e := json.Marshal(event)
					if e != nil {
						t.Fatal(e)
					}
					var roundtrip contract.RunEvent
					if json.Unmarshal(raw, &roundtrip) != nil {
						t.Fatal("malformed error event")
					}
				}
				if event.MemoryRecalled != nil {
					t.Fatal("success notice before refusal")
				}
			}
			if failures != 1 {
				t.Fatal("missing canonical failure")
			}
		}
	}
	vector := readJSON(t, "contract/vectors/compatibility/retired-aliases.json")
	for _, input := range vector.Get("cases").Items() {
		switch input.Get("alias").Str() {
		case "critic":
			s := modelSession()
			legacy := input.Get("value").Truth()
			s.AdversarialReview = &legacy
			s.Critic = "off"
			if _, err := ContextualSession(context.Background(), s); err == nil || !strings.Contains(err.Error(), "use critic") {
				t.Fatal("critic alias admitted")
			}
		case "memory":
			if _, err := RecallMemory(input.Get("value").Str(), 4000, "marker"); err == nil || !strings.Contains(err.Error(), "closed Memory records") {
				t.Fatal("text alias admitted")
			}
		case "budget":
			var value any
			json.Unmarshal(input.Get("value").JSON(), &value)
			if _, err := ResolvePolicy(nil, nil, PolicyLayer{"readBytes": value, "budget": map[string]any{"readBytes": 512}}, PolicyHost{}); err == nil || !strings.Contains(err.Error(), "budget.readBytes") {
				t.Fatal("flat budget admitted")
			}
		}
	}
	// A retired string callback is refused on presence, even alongside its replacement.
	for _, text := range []string{"", "old text"} {
		host := HostFuncs{MemoryForFunc: func(context.Context, string) (string, error) { t.Error("retired callback invoked"); return text, nil }, MemoryRecordsForFunc: func(context.Context, string) ([]contract.Memory, error) { return nil, nil }}
		if _, err := ReadHostParts(context.Background(), host, "q", Defaults, true); err == nil || !strings.Contains(err.Error(), "MemoryRecordsForFunc") {
			t.Fatal("retired callback binding admitted")
		}
	}
}

func TestMemoryDisabledWithoutLifecyclePreservesAbsentLiveBook(t *testing.T) {
	disabled := false
	s := modelSession()
	s.Context = &Input{Memory: &disabled}
	s.Host = HostFuncs{MemoryRecordsForFunc: func(context.Context, string) ([]contract.Memory, error) {
		t.Fatal("disabled recall invoked")
		return nil, nil
	}}
	run, err := ContextualSession(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	defer run.Context.Close()
	if run.Context.Execution.State().Memories != nil {
		t.Fatal("disabled no-update recall materialized an absent live book")
	}
}
