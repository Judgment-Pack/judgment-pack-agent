package runcontext

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"testing"
)

// hostHistory is what a degrading host remembers of its dispatches.
type hostHistory struct {
	failures, inARow, admitted int
	tries                      map[string]int
	last                       string
}

// degradingHost is a host over the reference host, which breaks the rule only when the authority errors
// and opens says so. These are the shapes #258 names: nothing here is a host anyone should write.
type degradingHost struct {
	inner   *ReferenceEffectHost
	opens   func(hostHistory) bool
	history hostHistory
}

func (h *degradingHost) EffectFencing() string { return EffectFencingEnforced }
func (h *degradingHost) Dispatch(ctx context.Context, admission EffectAdmission, perform func(context.Context) error) error {
	err := h.inner.Dispatch(ctx, admission, perform)
	var refused *LeaseRefused
	switch {
	case err == nil:
		h.history.inARow = 0
		h.history.admitted++
		h.history.last = "performed"
		return nil
	case errors.As(err, &refused):
		h.history.last = "refused"
		return err
	}
	h.history.failures++
	h.history.inARow++
	h.history.tries[admission.Operation.Operation]++
	if h.opens(h.history) {
		return perform(ctx)
	}
	h.history.last = "unavailable"
	return err
}

func degrades(opens func(hostHistory) bool) func(LeaseAuthority) EffectHost {
	return func(a LeaseAuthority) EffectHost {
		return &degradingHost{inner: NewReferenceEffectHost(a), opens: opens, history: hostHistory{tries: map[string]int{}}}
	}
}

// recordingHost delegates to the reference host and records every dispatch the authority failed.
type recordingHost struct {
	inner    *ReferenceEffectHost
	failures *[]string
}

func (h recordingHost) EffectFencing() string { return EffectFencingEnforced }
func (h recordingHost) Dispatch(ctx context.Context, admission EffectAdmission, perform func(context.Context) error) error {
	err := h.inner.Dispatch(ctx, admission, perform)
	var refused *LeaseRefused
	if err != nil && !errors.As(err, &refused) {
		*h.failures = append(*h.failures, admission.Operation.Operation)
	}
	return err
}

// closingHost stays closed once the authority failed repeatedly: safe, and of no use.
type closingHost struct {
	inner    *ReferenceEffectHost
	failures *int
}

func (h closingHost) EffectFencing() string { return EffectFencingEnforced }
func (h closingHost) Dispatch(ctx context.Context, admission EffectAdmission, perform func(context.Context) error) error {
	if *h.failures > 10 {
		perform = func(context.Context) error { return errors.New("closed") }
	}
	err := h.inner.Dispatch(ctx, admission, perform)
	var refused *LeaseRefused
	if err != nil && !errors.As(err, &refused) {
		*h.failures++
	}
	return err
}

func TestLeaseHostKitFailsTheAuthorityRepeatedly(t *testing.T) {
	ctx := context.Background()
	durable := durableLeaseFixture{NewMemoryRunStore(MemoryRunStoreOptions{})}
	const before, afterSuccess, between = "host.authority-unavailable", "host.authority-unavailable.after-success", "host.authority-unavailable.between-dispatches"
	for _, row := range []struct {
		name, property string
		opens          func(hostHistory) bool
	}{
		{"refuses-only-the-first-failure", before, func(h hostHistory) bool { return h.failures > 1 }},
		{"performs-after-three-failures-in-a-row", before, func(h hostHistory) bool { return h.inARow > 3 }},
		{"performs-on-the-fourth-try-of-one-operation", before, func(h hostHistory) bool {
			for _, tries := range h.tries {
				if tries > 3 {
					return true
				}
			}
			return false
		}},
		{"performs-once-three-different-operations-failed", before, func(h hostHistory) bool { return len(h.tries) > 3 }},
		{"performs-until-the-authority-has-answered-once", before, func(h hostHistory) bool { return h.admitted == 0 }},
		{"performs-on-the-good-answer-just-before", afterSuccess, func(h hostHistory) bool { return h.last == "performed" }},
		{"performs-right-after-it-refused-an-operation", between, func(h hostHistory) bool { return h.last == "refused" }},
	} {
		t.Run(row.name, func(t *testing.T) {
			var failed *StorageConformanceFailure
			if err := CheckEffectHostConformance(ctx, degrades(row.opens)); !errors.As(err, &failed) || failed.Property != row.property {
				t.Fatalf("the kit said %v, not %s", err, row.property)
			}
			failed = nil
			if _, err := CertifyEffectHost(ctx, degrades(row.opens), durable); !errors.As(err, &failed) || failed.Property != row.property {
				t.Fatalf("certification said %v, not %s", err, row.property)
			}
		})
	}
	t.Run("stays-closed", func(t *testing.T) {
		var failed *StorageConformanceFailure
		failures := 0
		create := func(a LeaseAuthority) EffectHost { return closingHost{NewReferenceEffectHost(a), &failures} }
		if err := CheckEffectHostConformance(ctx, create); !errors.As(err, &failed) || failed.Property != "host.admit.recovered" {
			t.Fatalf("a host that never admits again: %v", err)
		}
	})
	t.Run("every-failure-is-a-refusal", func(t *testing.T) {
		failures := []string{}
		create := func(a LeaseAuthority) EffectHost { return recordingHost{NewReferenceEffectHost(a), &failures} }
		if err := CheckEffectHostConformance(ctx, create); err != nil {
			t.Fatal(err)
		}
		want := []string{}
		for _, point := range []string{"before-any-dispatch", "after-success", "between-dispatches"} {
			for n := 0; n < 5; n++ {
				want = append(want, point+"-retried")
			}
			for n := 0; n < 5; n++ {
				want = append(want, point+"-"+strconv.Itoa(n))
			}
		}
		if !reflect.DeepEqual(failures, want) {
			t.Fatalf("the authority failed for %v, not %v", failures, want)
		}
		if err := CheckEffectHostConformance(ctx, func(a LeaseAuthority) EffectHost { return NewReferenceEffectHost(a) }); err != nil {
			t.Fatal(err)
		}
	})
}
