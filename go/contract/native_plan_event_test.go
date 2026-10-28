package contract

import "testing"

func TestNativePlanDecisionSelectsCurrentEventVersion(t *testing.T) {
	event := StampEvent(RunEvent{PlanDriftDecided: &RunEventPlanDriftDecided{NativeInvocation: &CheckpointNativePlanInvocation{DispatchID: "original", RequestDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}})
	if event.PlanDriftDecided.Version == nil || *event.PlanDriftDecided.Version != 2 {
		t.Fatal("native decision did not select current event2")
	}
	ordinary := StampEvent(RunEvent{PlanDriftDecided: &RunEventPlanDriftDecided{}})
	if ordinary.PlanDriftDecided.Version == nil || *ordinary.PlanDriftDecided.Version != 1 {
		t.Fatal("ordinary decision lost published event1 selection")
	}
}
