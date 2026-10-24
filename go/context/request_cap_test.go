package runcontext

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
)

func capSession(t *testing.T, cap *int, saved *contract.Checkpoint) Session {
	t.Helper()
	s, _ := trackedSession(t)
	s.Context.RequestCap = cap
	s.Context.Execution = saved
	return s
}
func capRead(t *testing.T, saved contract.Checkpoint) *contract.Checkpoint {
	t.Helper()
	raw, err := json.Marshal(saved)
	if err != nil {
		t.Fatal(err)
	}
	value, err := ParseJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	read, err := ReadExecution(value)
	if err != nil {
		t.Fatal(err)
	}
	return read
}
func requireCapFailure(t *testing.T, err error, code string) {
	t.Helper()
	var typed *failure.Error
	if !errors.As(err, &typed) || typed.Failure.Code != code || typed.Failure.Source != "context" || typed.Failure.Outcome != "not-started" {
		t.Fatalf("expected %s context refusal, got %v", code, err)
	}
	raw, e := json.Marshal(FailedToOpen(err)[0])
	if e != nil {
		t.Fatal(e)
	}
	actual, e := ParseJSON(raw)
	if e != nil {
		t.Fatal(e)
	}
	index := 0
	if code == "stale-state" {
		index = 1
	}
	expected := vectorItems(t, "checkpoint/request-cap-refusal.json")[index].Clone().Set("version", Number(1))
	if CanonicalJSON(actual) != CanonicalJSON(expected) {
		t.Fatal("refusal event differs from the shared schema-validated vector")
	}
}
func TestRequestCapSharedVectorsAndMalformedRecords(t *testing.T) {
	for _, v := range vectorItems(t, "checkpoint/request-cap.json") {
		if _, err := ReadExecution(v); err != nil {
			t.Fatal(err)
		}
	}
	base := vectorItems(t, "checkpoint/request-cap.json")[1]
	for _, bad := range []*Value{Null(), Number(0), Number(-1), Number(21), Number(1.5), String("1"), Bool(true), Array(), Object()} {
		v := base.Clone()
		v.Set("requestCap", bad)
		if _, err := ReadExecution(v); err == nil {
			t.Fatalf("accepted malformed cap %s", bad.Text())
		}
	}
	for _, change := range []func(*Value){func(v *Value) { v.Set("unit", String("provider-managed")) }, func(v *Value) { v.Set("limit", Number(2)) }, func(v *Value) { v.Set("used", Number(2)) }, func(v *Value) {
		v.Set("grants", Array(ObjectOf("from", Number(1), "to", Number(2), "at", String("2026-10-24T00:00:00Z"))))
	}} {
		v := base.Clone()
		change(v)
		if _, err := ReadExecution(v); err == nil {
			t.Fatal("accepted changed fixed cap record")
		}
	}
	legacy := vectorItems(t, "checkpoint/request-cap.json")[0].Clone()
	legacy.Set("limit", Number(21))
	legacy.Set("grants", Array(ObjectOf("from", Number(20), "to", Number(21), "at", String("2026-10-24T00:00:00Z"))))
	if _, err := ReadExecution(legacy); err != nil {
		t.Fatal("legacy bounded grant changed", err)
	}
}
func TestRequestCapIdentityRoundtripAndNoExtension(t *testing.T) {
	for _, cap := range []int{1, 20} {
		t.Run(string(rune('a'+cap)), func(t *testing.T) {
			input := capSession(t, &cap, nil)
			opened := openRun(t, input)
			defer opened.Context.Close()
			if cap == 20 {
				if err := opened.Context.Execution.Claim(); err != nil {
					t.Fatal(err)
				}
			}
			saved, err := opened.Context.Snapshot("stopped")
			if err != nil {
				t.Fatal(err)
			}
			if saved.RequestCap == nil {
				t.Fatal("cap lost in snapshot")
			}
			read := capRead(t, saved)
			if read.RequestCap == nil || *read.RequestCap != int64(cap) || read.Limit != int64(cap) {
				t.Fatal("cap lost in projection")
			}
			resumed := openRun(t, capSession(t, &cap, &saved))
			defer resumed.Context.Close()
			again, err := resumed.Context.Snapshot("running")
			if err != nil {
				t.Fatal(err)
			}
			if again.Used != saved.Used {
				t.Fatal("consumed usage lost on partial resume")
			}
			if again.RequestCap == nil || *again.RequestCap != int64(cap) {
				t.Fatal("cap lost on resume")
			}
			for _, expected := range []*int{nil, new(int)} {
				_, err := ContextualSession(context.Background(), capSession(t, expected, &saved))
				code := "stale-state"
				if expected != nil {
					code = "configuration"
				}
				requireCapFailure(t, err, code)
			}
			other := 2
			if cap == 20 {
				other = 1
			}
			_, err = ContextualSession(context.Background(), capSession(t, &other, &saved))
			requireCapFailure(t, err, "stale-state")
			without := saved
			without.RequestCap = nil
			if cap == 20 {
				if _, err := ContextualSession(context.Background(), capSession(t, &cap, &without)); err == nil {
					t.Fatal("accepted removed cap20 member")
				}
			}
			changed := saved
			value := int64(other)
			changed.RequestCap = &value
			changed.Limit = value
			if _, err := ContextualSession(context.Background(), capSession(t, &cap, &changed)); err == nil {
				t.Fatal("accepted coherently changed cap and limit")
			}
			paused := saved
			paused.Status = "paused"
			if _, err := ExtendExecution(paused, false, time.Now()); err == nil {
				t.Fatal("extended fixed cap")
			}
			stopped := saved
			stopped.Used = stopped.Limit
			if CanExtendInterruptedExecution(&stopped) {
				t.Fatal("offered fixed cap extension")
			}
			if _, err := ContextualSession(context.Background(), capSession(t, &cap, &stopped)); !errors.Is(err, ErrExecutionPaused) {
				t.Fatalf("spent resume: %v", err)
			}
			legacy := openRun(t, capSession(t, nil, nil))
			defer legacy.Context.Close()
			if legacy.Context.Execution.State().RequestKey != opened.Context.Execution.State().RequestKey {
				t.Fatal("legacy request hash changed")
			}
			old, err := legacy.Context.Snapshot("stopped")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ContextualSession(context.Background(), capSession(t, &cap, &old)); err == nil {
				t.Fatal("accepted cap input with uncapped record")
			}
		})
	}
}
func TestRequestCapCopiesAdmissionAndMutationCannotWiden(t *testing.T) {
	cap := 1
	s := capSession(t, &cap, nil)
	s.Context.Authorize = func(context.Context) error { cap = 20; return nil }
	opened := openRun(t, s)
	defer opened.Context.Close()
	if opened.Context.Execution.State().Limit != 1 {
		t.Fatal("host pointer changed accepted cap")
	}
	s.Context.RequestCap = nil
	for _, patch := range []CheckpointPatch{{"RequestCap": nil, "Limit": 20}, {"Limit": 20}, {"Unit": "provider-managed"}, {"Used": 2}, {"Grants": []contract.CheckpointGrant{{From: 1, To: 2, At: "2026-10-24T00:00:00Z"}}}} {
		if opened.Context.Execution.MutateCheckpoint(patch) == nil {
			t.Fatal("mutation widened admitted cap")
		}
	}
	if err := opened.Context.Execution.Claim(); err != nil {
		t.Fatal(err)
	}
	requireCapFailure(t, opened.Context.Execution.Initialize(), "configuration")
	if err := opened.Context.Execution.MutateCheckpoint(CheckpointPatch{"Used": 0}); err == nil {
		t.Fatal("consumed cap was reset")
	}
	if err := opened.Context.Execution.Claim(); !errors.Is(err, ErrExecutionPaused) {
		t.Fatal("second claim escaped cap", err)
	}
}
func TestRequestCapLimitToRulesFailClosed(t *testing.T) {
	one := 1
	valid := openRun(t, capSession(t, nil, nil))
	defer valid.Context.Close()
	valid.Context.Execution.LimitTo(1)
	valid.Context.Execution.LimitTo(1)
	saved, err := valid.Context.Snapshot("stopped")
	if err != nil {
		t.Fatal(err)
	}
	capRead(t, saved)
	for _, kind := range []string{"zero", "negative", "oversized", "different", "tighten", "postclaim", "granted", "restored", "native"} {
		t.Run(kind, func(t *testing.T) {
			s := capSession(t, nil, nil)
			if kind == "native" {
				s.Model = nil
				s.Agent = &AgentSession{Model: "fixture"}
			}
			if kind == "restored" {
				s = capSession(t, &one, &saved)
			}
			opened := openRun(t, s)
			defer opened.Context.Close()
			c := opened.Context.Execution
			next := 1
			switch kind {
			case "zero":
				next = 0
			case "negative":
				next = -1
			case "oversized":
				next = 21
			case "different":
				c.LimitTo(1)
				next = 2
			case "tighten":
				c.LimitTo(20)
			case "postclaim":
				if err := c.Claim(); err != nil {
					t.Fatal(err)
				}
			case "granted":
				c.MutateCheckpoint(CheckpointPatch{"Limit": 30, "Grants": []contract.CheckpointGrant{{From: 20, To: 30, At: "2026-10-24T00:00:00Z"}}})
			}
			c.LimitTo(next)
			c.LimitTo(1)
			if err := c.Claim(); err == nil {
				t.Fatal("ignored invalid helper allowed dispatch")
			}
			if _, err := c.Snapshot("running"); err == nil {
				t.Fatal("invalid helper published checkpoint")
			}
		})
	}
	if err := valid.Context.Execution.Claim(); err != nil {
		t.Fatal(err)
	}
	requireCapFailure(t, valid.Context.Execution.Initialize(), "configuration")
}
func TestRequestCapDurableClaimAndRefusalEvents(t *testing.T) {
	cap := 1
	var saved contract.Checkpoint
	s := capSession(t, &cap, nil)
	s.Context.Checkpoint = func(_ context.Context, cp contract.Checkpoint) error { saved = cp; return nil }
	opened := openRun(t, s)
	defer opened.Context.Close()
	if err := opened.Context.ClaimRequest(context.Background(), []byte("{}")); err != nil {
		t.Fatal(err)
	}
	if saved.Used != 1 || saved.RequestCap == nil || *saved.RequestCap != 1 || len(saved.Prompts) != 1 {
		t.Fatal("claim not durable before handoff")
	}
	capRead(t, saved)
	if _, err := ContextualSession(context.Background(), capSession(t, &cap, &saved)); !errors.Is(err, ErrExecutionPaused) {
		t.Fatal("interrupted claimed request could replay", err)
	}
	for _, bad := range []int{0, -1, 21} {
		_, err := ContextualSession(context.Background(), capSession(t, &bad, nil))
		var typed *failure.Error
		if !errors.As(err, &typed) {
			t.Fatalf("missing typed configuration refusal: %v", err)
		}
		if _, ok := failure.Read(typed.Failure); !ok || typed.Failure.Code != "configuration" || typed.Failure.Source != "context" || typed.Failure.Outcome != "not-started" {
			t.Fatal("invalid refusal", typed)
		}
		events := FailedToOpen(err)
		if events[0].Error == nil || events[len(events)-1].End == nil {
			t.Fatal("refusal did not reach event boundary")
		}
	}
	native := capSession(t, &cap, nil)
	native.Model = nil
	native.Agent = &AgentSession{Model: "fixture"}
	if _, err := ContextualSession(context.Background(), native); err == nil {
		t.Fatal("native cap admitted")
	}
	native.Context.RequestCap = nil
	openedNative := openRun(t, native)
	defer openedNative.Context.Close()
	cp, err := openedNative.Context.Snapshot("stopped")
	if err != nil {
		t.Fatal(err)
	}
	if cp.Unit != "provider-managed" || cp.Limit != 1 || cp.RequestCap != nil {
		t.Fatal("native default changed")
	}
	capRead(t, cp)
}

func TestRequestCapUnprojectableClaimStopsHandoff(t *testing.T) {
	cap := 1
	for _, fixed := range []*int{&cap, nil} {
		s := capSession(t, fixed, nil)
		refusing := false
		s.Context.Checkpoint = func(context.Context, contract.Checkpoint) error {
			if refusing {
				t.Fatal("refused records persisted")
			}
			return nil
		}
		opened := openRun(t, s)
		refusing = true
		opened.Context.Execution.recordError = &RecordsRefused{&OutputRefused{Pattern: "fixture"}}
		err := opened.Context.ClaimRequest(context.Background(), []byte("{}"))
		opened.Context.Close()
		if fixed == nil {
			if err != nil {
				t.Fatal("legacy records refusal changed", err)
			}
			continue
		}
		var typed *failure.Error
		if !errors.As(err, &typed) || typed.Failure.Code != "storage" || typed.Failure.Outcome != "not-started" {
			t.Fatal("unprojectable capped claim admitted handoff", err)
		}
	}
}
