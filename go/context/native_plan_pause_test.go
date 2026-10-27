package runcontext

import (
	"context"
	"strings"
	"testing"

	"github.com/Judgment-Pack/judgment-pack-agent/go/internal/providerrelease"
)

func TestNativePlanReleaseCompositionKeepsExactOwnerChecks(t *testing.T) {
	for _, mode := range []string{"released", "wrong", "duplicate", "mixed", "late"} {
		t.Run(mode, func(t *testing.T) {
			route, finishRoute := providerrelease.NewReleaseAttempt()
			pause, finishPause := providerrelease.NewReleaseAttempt()
			delegated, finishDelegated := providerrelease.NewReleaseAttempt()
			joined := providerrelease.Compose(route, pause, delegated)
			if joined.Invocation == route.Invocation || joined.Invocation == pause.Invocation || joined.Invocation == delegated.Invocation {
				t.Fatal("composition exposed an original owner's token")
			}
			switch mode {
			case "wrong":
				foreign, _ := providerrelease.NewReleaseAttempt()
				if joined.Acknowledge(foreign.Invocation) {
					t.Fatal("foreign release token accepted")
				}
			case "late":
				if finishRoute() || finishPause() || finishDelegated() {
					t.Fatal("unacknowledged invocation released")
				}
				if joined.Acknowledge(joined.Invocation) {
					t.Fatal("late acknowledgement revived closed owners")
				}
				return
			default:
				if !joined.Acknowledge(joined.Invocation) {
					t.Fatal("exact joined acknowledgement refused")
				}
				if mode == "duplicate" && joined.Acknowledge(joined.Invocation) {
					t.Fatal("duplicate joined acknowledgement accepted")
				}
				if mode == "mixed" && joined.NotAdmitted(joined.Invocation) {
					t.Fatal("mixed joined disposition accepted")
				}
			}
			wanted := mode == "released"
			for name, finish := range map[string]func() bool{"route": finishRoute, "pause": finishPause, "delegation": finishDelegated} {
				if released := finish(); released != wanted {
					t.Fatalf("%s release=%v wanted=%v", name, released, wanted)
				}
			}
		})
	}
}

func TestNativePlanPausePrivateCauseCannotBeImported(t *testing.T) {
	c := nativeRouteContext(t, "native-plan-private")
	if err := c.MarkNativePlanPause(context.Background()); err == nil {
		t.Fatal("ordinary context manufactured a native Plan pause")
	}
	foreign := nativeRouteContext(t, "native-plan-foreign")
	binding := c.Persistence("author")
	slot := &nativePlanAttempt{owner: c, writer: binding.Writer, scope: binding.Scope, open: true}
	private := context.WithValue(context.Background(), nativePlanAttemptKey{}, slot)
	if err := foreign.MarkNativePlanPause(private); err == nil {
		t.Fatal("foreign SessionContext adopted another invocation's pause slot")
	}
	// A serialized-looking pending state or cancellation is not a live marked,
	// released and acknowledged claim; neither can avoid ordinary dispatch closure.
	binding.Writer.mu.Lock()
	accepted := nativePlanPauseAcknowledgedLocked(private, binding, DurableDispatch{})
	binding.Writer.mu.Unlock()
	if accepted {
		t.Fatal("unclaimed slot acquired the private pause cancellation exemption")
	}
	closed, stop := context.WithCancel(private)
	stop()
	binding.Writer.mu.Lock()
	accepted = nativePlanPauseAcknowledgedLocked(closed, binding, DurableDispatch{})
	binding.Writer.mu.Unlock()
	if accepted {
		t.Fatal("caller cancellation acquired a pause exemption")
	}
}

// A refusal by any already-closed owner must invalidate every still-live peer.
// This includes nested composition and each closure order, not merely all-late proof.
func TestNativePlanReleaseCompositionRejectsPartiallyLateProof(t *testing.T) {
	for closed := 0; closed < 3; closed++ {
		for _, nested := range []bool{false, true} {
			owners := make([]*providerrelease.Capability, 3)
			finishes := make([]func() bool, 3)
			for i := range owners {
				owners[i], finishes[i] = providerrelease.NewReleaseAttempt()
			}
			joined := providerrelease.Compose(owners...)
			if nested {
				joined = providerrelease.Compose(providerrelease.Compose(owners[2], owners[0]), owners[1])
			}
			if finishes[closed]() {
				t.Fatal("unacknowledged owner released before composite proof")
			}
			if joined.Acknowledge(joined.Invocation) {
				t.Fatal("partially late composed proof accepted")
			}
			for i, finish := range finishes {
				if finish() {
					t.Fatalf("invalid composed proof released live owner%d after owner%d closed (nested=%v)", i, closed, nested)
				}
			}
		}
	}
}

func TestNativePlanReleaseCompositionAndFinishAreAtomic(t *testing.T) {
	for iteration := 0; iteration < 48; iteration++ {
		first, finishFirst := providerrelease.NewReleaseAttempt()
		second, finishSecond := providerrelease.NewReleaseAttempt()
		third, finishThird := providerrelease.NewReleaseAttempt()
		joined := providerrelease.Compose(providerrelease.Compose(third, first), second)
		start := make(chan struct{})
		acknowledged := make(chan bool, 1)
		finished := make(chan bool, 1)
		go func() { <-start; acknowledged <- joined.Acknowledge(joined.Invocation) }()
		go func() { <-start; finished <- finishFirst() }()
		close(start)
		ack, firstReleased := <-acknowledged, <-finished
		secondReleased, thirdReleased := finishSecond(), finishThird()
		if firstReleased != ack || secondReleased != ack || thirdReleased != ack {
			t.Fatalf("composed acknowledgement/finish was partial: ack=%v owners=%v/%v/%v", ack, firstReleased, secondReleased, thirdReleased)
		}
	}
}

func TestNativePlanJournalRejectsAmbiguousJSON(t *testing.T) {
	valid := `{"version":1,"scope":{"execution":"run","role":"author"},"event":{"type":"end","version":1}}`
	if err := nativePlanJSONDepth([]byte(valid)); err != nil {
		t.Fatal("ordinary journal JSON refused", err)
	}
	for _, malformed := range []string{
		`{"version":1,"version":1,"scope":{"execution":"run","role":"author"},"event":{"type":"end","version":1}}`,
		`{"version":1,"scope":{"execution":"run","role":"author"},"event":{"type":"error","type":"end","version":1}}`,
		valid + valid,
		"{\"value\":\"" + string([]byte{0xff}) + "\"}",
		strings.Repeat("[", 65) + "0" + strings.Repeat("]", 65),
	} {
		if err := nativePlanJSONDepth([]byte(malformed)); err == nil {
			t.Fatal("ambiguous or excessive-depth journal JSON acquired authority")
		}
	}
}
