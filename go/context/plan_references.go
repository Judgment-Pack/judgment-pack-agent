package runcontext

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
)

// PlanReference is the existing Go pin/prompt counterpart, not a new ChatStore/action API.
// Digest is a content hash, never the opaque CAS token returned by a host storage write.
type PlanReference struct {
	Kind           string `json:"kind"`
	ID             string `json:"id"`
	Digest         string `json:"digest,omitempty"`
	LegacyRevision string `json:"revision,omitempty"`
}
type ResolvedPlanReference struct {
	Kind    string
	ID      string
	Digest  string
	Content []byte
}

// Host-scoped resolver capabilities; no path, URL, RuntimeDir or identity is inferred by Agent.
type PlanResolver struct {
	Resolve func(context.Context, string, string) (ResolvedPlanReference, error)
	Legacy  func(context.Context, PlanReference) (ResolvedPlanReference, error)
}
type LegacyPlanReferenceUnresolved struct{}

func (*LegacyPlanReferenceUnresolved) Error() string {
	return "LegacyPlanReferenceUnresolved: the host must resolve this historical reference."
}
func (*LegacyPlanReferenceUnresolved) FailureCode() string { return "stale-state" }

type PlanReferenceChanged struct{}

func (*PlanReferenceChanged) Error() string {
	return "PlanReferenceChanged: a referenced object changed; review a new plan revision."
}
func (*PlanReferenceChanged) FailureCode() string { return "stale-state" }
func ResolvePlanReference(ctx context.Context, reference PlanReference, resolver PlanResolver) (PlanReference, error) {
	if err := ctx.Err(); err != nil {
		return PlanReference{}, err
	}
	var found ResolvedPlanReference
	var err error
	legacy := reference.Kind == "file" || reference.LegacyRevision != ""
	if legacy {
		if resolver.Legacy == nil {
			return PlanReference{}, &LegacyPlanReferenceUnresolved{}
		}
		found, err = resolver.Legacy(ctx, reference)
	} else {
		if resolver.Resolve == nil {
			return PlanReference{}, &LegacyPlanReferenceUnresolved{}
		}
		found, err = resolver.Resolve(ctx, reference.Kind, reference.ID)
	}
	if err != nil {
		return PlanReference{}, err
	}
	if err = ctx.Err(); err != nil {
		return PlanReference{}, err
	}
	if !validStorageID(found.ID) || (found.Kind != "artifact" && found.Kind != "dialect") || !digest64.MatchString(found.Digest) || !legacy && (found.ID != reference.ID || found.Kind != reference.Kind) {
		return PlanReference{}, &PlanReferenceChanged{}
	}
	pin := reference.Digest
	if legacy {
		pin = reference.LegacyRevision
	}
	if pin != "" && pin != found.Digest {
		return PlanReference{}, &PlanReferenceChanged{}
	}
	if found.Content != nil {
		sum := sha256.Sum256(found.Content)
		if len(found.Content) > StorageRecordBytes || hex.EncodeToString(sum[:]) != found.Digest {
			return PlanReference{}, &PlanReferenceChanged{}
		}
	}
	return PlanReference{Kind: found.Kind, ID: found.ID, Digest: found.Digest}, nil
}
