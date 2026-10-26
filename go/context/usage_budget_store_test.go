package runcontext

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
)

type usageBudgetVector struct {
	Resource    string
	Scope       UsageBudgetScope
	First       UsageReservation
	Reported    UsageAmounts
	SecondUpper UsageAmounts
	Evidence    string
}

func budgetVector(t *testing.T) usageBudgetVector {
	t.Helper()
	raw, err := os.ReadFile("../../contract/vectors/usage-budget/admission.json")
	if err != nil {
		t.Fatal(err)
	}
	var vector usageBudgetVector
	if err = json.Unmarshal(raw, &vector); err != nil {
		t.Fatal(err)
	}
	return vector
}
func budgetMemory(t *testing.T, v usageBudgetVector, options MemoryUsageBudgetOptions) *MemoryUsageBudgetStore {
	t.Helper()
	store, err := NewMemoryUsageBudgetStore(v.Resource, options)
	if err != nil {
		t.Fatal(err)
	}
	return store
}
func TestUsageBudgetSharedVectorAtomicPartialAndExactLimit(t *testing.T) {
	v := budgetVector(t)
	store := budgetMemory(t, v, MemoryUsageBudgetOptions{})
	ctx := context.Background()
	fresh, err := store.Reserve(ctx, v.First)
	first := fresh.Receipt
	if !fresh.Created {
		t.Fatal("fresh reservation did not carry live creation metadata")
	}
	if err != nil {
		t.Fatal(err)
	}
	settled, err := store.Settle(ctx, UsageSettlement{Scope: v.Scope, ID: v.First.ID, ReservationDigest: first.ReservationDigest, CommitID: "reported-1", Evidence: v.Evidence, Kind: "reported", Observed: v.Reported})
	if err != nil || settled.State != "partial" {
		t.Fatalf("missing output was not unknown/reserved: %+v %v", settled, err)
	}
	if _, invented := settled.Observed["outputTokens"]; invented {
		t.Fatal("missing provider output invented")
	}
	second := usageCopy(v.First)
	second.ID = "request-2"
	second.Root = "root-2"
	second.Upper = v.SecondUpper
	if _, err = store.Reserve(ctx, second); err != nil {
		t.Fatalf("exact shared-scope limit refused: %v", err)
	}
	third := usageCopy(v.First)
	third.ID = "request-3"
	var exceeded *UsageBudgetExceeded
	if _, err = store.Reserve(ctx, third); !errors.As(err, &exceeded) || exceeded.Dimension != "modelRequests" {
		t.Fatalf("outstanding sibling requests not charged: %v", err)
	}
	same, err := store.Reserve(ctx, v.First)
	if err != nil || same.Created || same.Receipt.Revision != first.Revision {
		t.Fatal("exact reservation identity not idempotent")
	}
	changed := usageCopy(v.First)
	changed.RequestDigest = v.Evidence
	var mismatch *StorageCommitMismatch
	if _, err = store.Reserve(ctx, changed); !errors.As(err, &mismatch) {
		t.Fatal("same id changed request accepted")
	}
	head, err := store.ReadReservation(ctx, v.Scope, v.First.ID)
	if err != nil || head == nil || head.Receipt.State != "partial" {
		t.Fatal("retained partial result not readable")
	}
	head.Request.Upper["outputTokens"] = 0
	retained, _ := store.ReadReservation(ctx, v.Scope, v.First.ID)
	if retained.Request.Upper["outputTokens"] != 5 {
		t.Fatal("observational read aliased budget authority")
	}
}
func TestUsageBudgetConcurrentScopesIncludeExternalAndOutstanding(t *testing.T) {
	v := budgetVector(t)
	v.Scope.Limits = UsageAmounts{"modelRequests": 5}
	v.First.Scope = v.Scope
	v.First.Upper = UsageAmounts{"modelRequests": 1}
	store := budgetMemory(t, v, MemoryUsageBudgetOptions{Seeds: []UsageBudgetSeed{{Scope: v.Scope, Committed: UsageAmounts{"modelRequests": 3}}}})
	var accepted atomic.Int32
	var wait sync.WaitGroup
	for i := 0; i < 10; i++ {
		wait.Add(1)
		go func(i int) {
			defer wait.Done()
			request := usageCopy(v.First)
			request.ID = string(rune('a' + i))
			request.Root = request.ID
			if _, err := store.Reserve(context.Background(), request); err == nil {
				accepted.Add(1)
			} else {
				var exceeded *UsageBudgetExceeded
				if !errors.As(err, &exceeded) {
					t.Errorf("unexpected reservation error: %v", err)
				}
			}
		}(i)
	}
	wait.Wait()
	if accepted.Load() != 2 {
		t.Fatalf("non-atomic shared-scope admission: accepted=%d, want2", accepted.Load())
	}
}
func TestUsageBudgetLostAckMissingExternalAndNeverAdmitted(t *testing.T) {
	v := budgetVector(t)
	ctx := context.Background()
	lost := errors.New("fixture lost budget acknowledgment")
	once := false
	store := budgetMemory(t, v, MemoryUsageBudgetOptions{AfterChange: func() error {
		if !once {
			once = true
			return lost
		}
		return nil
	}})
	if _, err := store.Reserve(ctx, v.First); !errors.Is(err, lost) {
		t.Fatal("lost acknowledgment fixture not injected")
	}
	head, err := store.ReadReservation(ctx, v.Scope, v.First.ID)
	if err != nil || head == nil || head.Receipt.State != "reserved" {
		t.Fatal("unknown acknowledgment refunded or forgot reservation")
	}
	retained, err := store.Reserve(ctx, v.First)
	if err != nil || retained.Created {
		t.Fatal("lost-ack retry fabricated fresh admission metadata")
	}
	next := usageCopy(v.First)
	next.ID = "other"
	next.Upper = UsageAmounts{"modelRequests": 1, "inputTokens": 11, "outputTokens": 5}
	var exceeded *UsageBudgetExceeded
	if _, err = store.Reserve(ctx, next); !errors.As(err, &exceeded) {
		t.Fatal("unknown reservation stopped consuming its bound")
	}
	released, err := store.Settle(ctx, UsageSettlement{Scope: v.Scope, ID: v.First.ID, ReservationDigest: head.Receipt.ReservationDigest, CommitID: "not-admitted", Evidence: v.Evidence, Kind: "never-admitted"})
	if err != nil || released.State != "never-admitted" {
		t.Fatal("authoritative no-admission settlement failed")
	}
	if _, err = store.Reserve(ctx, next); err != nil {
		t.Fatalf("acknowledged no-admission did not free only its bound: %v", err)
	}
	unknown := budgetMemory(t, v, MemoryUsageBudgetOptions{Seeds: []UsageBudgetSeed{{Scope: v.Scope, Committed: UsageAmounts{"modelRequests": 0}}}})
	var unavailable *UsageBudgetUnknown
	if _, err = unknown.Reserve(ctx, v.First); !errors.As(err, &unavailable) {
		t.Fatal("unreported externally committed tokens treated as zero")
	}
}

func TestUsageBudgetMissingTokensRemainReserved(t *testing.T) {
	v := budgetVector(t)
	v.Scope.Limits = UsageAmounts{"outputTokens": 5}
	v.First.Scope = v.Scope
	v.First.Upper = UsageAmounts{"modelRequests": 1, "outputTokens": 5}
	store := budgetMemory(t, v, MemoryUsageBudgetOptions{})
	ctx := context.Background()
	first, err := store.Reserve(ctx, v.First)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Settle(ctx, UsageSettlement{Scope: v.Scope, ID: v.First.ID, ReservationDigest: first.Receipt.ReservationDigest, CommitID: "partial-count", Evidence: v.Evidence, Kind: "reported", Observed: UsageAmounts{"modelRequests": 1}}); err != nil {
		t.Fatal(err)
	}
	next := usageCopy(v.First)
	next.ID = "next"
	if _, err = store.Reserve(ctx, next); err == nil {
		t.Fatal("missing provider tokens released allowance")
	}
}
