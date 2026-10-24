package runcontext

import (
	"context"
	"strings"
	"testing"
)

func TestPlanReferenceOpaqueResolutionAndExplicitLegacy(t *testing.T) {
	ctx := context.Background()
	pin := strings.Repeat("a", 64)
	resolver := PlanResolver{Resolve: func(_ context.Context, kind, id string) (ResolvedPlanReference, error) {
		return ResolvedPlanReference{Kind: kind, ID: id, Digest: pin}, nil
	}}
	ref, err := ResolvePlanReference(ctx, PlanReference{Kind: "artifact", ID: "opaque-artifact", Digest: pin}, resolver)
	if err != nil || ref.ID != "opaque-artifact" {
		t.Fatal(err)
	}
	if _, err = ResolvePlanReference(ctx, PlanReference{Kind: "file", ID: "legacy-locator", LegacyRevision: pin}, resolver); err == nil {
		t.Fatal("legacy locator inferred as opaque identity")
	}
	resolver.Legacy = func(_ context.Context, old PlanReference) (ResolvedPlanReference, error) {
		return ResolvedPlanReference{Kind: "artifact", ID: "migrated-opaque", Digest: old.LegacyRevision}, nil
	}
	ref, err = ResolvePlanReference(ctx, PlanReference{Kind: "file", ID: "legacy-locator", LegacyRevision: pin}, resolver)
	if err != nil || ref.ID != "migrated-opaque" || ref.LegacyRevision != "" {
		t.Fatal(err)
	}
	if _, err = ResolvePlanReference(ctx, PlanReference{Kind: "artifact", ID: "opaque-artifact", Digest: strings.Repeat("b", 64)}, resolver); err == nil {
		t.Fatal("changed digest accepted")
	}
}
