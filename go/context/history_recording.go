package runcontext

import "context"

type PreparedHistoryObservation struct {
	Observation *Value
	Ref         HistoryObjectRef
	Objects     []HistoryObject
}

func prepareHistoryObservation(v *Value, objects []HistoryObject) (PreparedHistoryObservation, error) {
	raw := []byte(CanonicalJSON(v))
	decoded, err := ReadHistoryObservation(raw)
	if err != nil {
		return PreparedHistoryObservation{}, err
	}
	object, err := NewHistoryObject(raw)
	if err != nil {
		return PreparedHistoryObservation{}, err
	}
	objects = append(objects, object)
	return PreparedHistoryObservation{Observation: decoded, Ref: object.HistoryObjectRef, Objects: objects}, nil
}
func historyScopeJSON(scope DurableScope) *Value {
	raw, _ := historyJSONBytes(scope)
	v, _ := ParseJSON(raw)
	return v
}
func PrepareHistoryGenesis(scope DurableScope) (PreparedHistoryObservation, error) {
	return prepareHistoryObservation(ObjectOf("version", Number(1), "kind", String("genesis"), "scope", historyScopeJSON(scope), "profile", ObjectOf("version", Number(1), "profile", String("full-history"), "genesis", Bool(true), "originals", Bool(true), "compactions", Bool(true), "requests", Bool(true))), nil)
}
func historyRefJSON(ref HistoryObjectRef) *Value {
	return ObjectOf("digest", String(ref.Digest), "bytes", Number(float64(ref.Bytes)))
}
func historyRefsJSON(refs []HistoryObjectRef) *Value {
	out := Array()
	for _, ref := range refs {
		out.Append(historyRefJSON(ref))
	}
	return out
}

// PrepareHistoryOriginal takes public retained input only, preserving authoritative ordered source JSON.
func PrepareHistoryOriginal(scope DurableScope, boundary, sourceJSON, format string, ordinal int) (PreparedHistoryObservation, error) {
	if _, err := readHistorySourceJSON([]byte(sourceJSON)); err != nil {
		return PreparedHistoryObservation{}, err
	}
	manifest, objects, err := ChunkHistoryValue([]byte(sourceJSON))
	if err != nil {
		return PreparedHistoryObservation{}, err
	}
	objects = append(objects, manifest)
	return prepareHistoryObservation(ObjectOf("version", Number(1), "kind", String("original"), "scope", historyScopeJSON(scope), "boundary", String(boundary), "value", historyRefJSON(manifest.HistoryObjectRef), "format", String(format), "ordinal", Number(float64(ordinal))), objects)
}

// Original observations must be acknowledged before their public inputs can be lost.
func PrepareHistoryCompaction(scope DurableScope, boundary string, originals []HistoryObjectRef, replacementJSON, mode string, prior []HistoryObjectRef, legacyDigest string) (PreparedHistoryObservation, error) {
	if _, err := readHistorySourceJSON([]byte(replacementJSON)); err != nil {
		return PreparedHistoryObservation{}, err
	}
	manifest, objects, err := ChunkHistoryValue([]byte(replacementJSON))
	if err != nil {
		return PreparedHistoryObservation{}, err
	}
	objects = append(objects, manifest)
	return prepareHistoryObservation(ObjectOf("version", Number(1), "kind", String("compaction"), "scope", historyScopeJSON(scope), "boundary", String(boundary), "originals", historyRefsJSON(originals), "replacement", historyRefJSON(manifest.HistoryObjectRef), "mode", String(mode), "prior", historyRefsJSON(prior), "legacyDigest", String(legacyDigest)), objects)
}
func PrepareHistoryRequest(scope DurableScope, dispatchID, source, tool, sourceJSON string, compactions, originals []HistoryObjectRef) (PreparedHistoryObservation, error) {
	if _, err := readHistoryRequestJSON([]byte(sourceJSON)); err != nil {
		return PreparedHistoryObservation{}, err
	}
	manifest, objects, err := ChunkHistoryValue([]byte(sourceJSON))
	if err != nil {
		return PreparedHistoryObservation{}, err
	}
	objects = append(objects, manifest)
	return prepareHistoryObservation(ObjectOf("version", Number(1), "kind", String("request"), "scope", historyScopeJSON(scope), "dispatchId", String(dispatchID), "source", String(source), "tool", String(tool), "request", historyRefJSON(manifest.HistoryObjectRef), "compactions", historyRefsJSON(compactions), "originals", historyRefsJSON(originals)), objects)
}
func PrepareCaptureExpectation(identity HistoryCaptureIdentity, bindings ...map[string]string) (PreparedHistoryObservation, error) {
	value := ObjectOf("version", Number(1), "kind", String("capture-request"), "scope", historyScopeJSON(identity.Scope), "dispatchId", String(identity.DispatchID), "captureKind", String(identity.Kind), "source", String(identity.Source), "tool", String(identity.Tool), "requestDigest", String(identity.RequestDigest), "surface", String(identity.Surface))
	objects := []HistoryObject{}
	if len(bindings) > 0 {
		if identity.Kind != "agent" {
			return PreparedHistoryObservation{}, &HistoryInputInvalid{}
		}
		raw, err := canonicalRaw(bindings[0])
		if err != nil {
			return PreparedHistoryObservation{}, err
		}
		object, err := NewHistoryObject(raw)
		if err != nil {
			return PreparedHistoryObservation{}, err
		}
		objects = append(objects, object)
		value.Set("nativeBindings", historyRefJSON(object.HistoryObjectRef))
	}
	return prepareHistoryObservation(value, objects)
}

// Staging is private. Only the original writer's atomic commit can publish this ref before dispatch/eviction.
func StageHistoryObservation(ctx context.Context, store HistoryRunStore, runID string, value PreparedHistoryObservation) (HistoryObjectRef, error) {
	invalid := func() (HistoryObjectRef, error) { return HistoryObjectRef{}, &HistoryInputInvalid{} }
	if store == nil || store.HistoryVersion() != 1 || !validStorageID(runID) {
		return invalid()
	}
	held := map[string]HistoryObject{}
	objects := []HistoryObject{}
	total := 0
	for _, object := range value.Objects {
		copy, err := NewHistoryObject(object.Value)
		if err != nil || copy.HistoryObjectRef != object.HistoryObjectRef {
			return invalid()
		}
		if _, ok := held[copy.Digest]; !ok {
			held[copy.Digest] = copy
			objects = append(objects, copy)
			total += copy.Bytes
			if total > HistoryTotalBytes {
				return invalid()
			}
		}
	}
	object, ok := held[value.Ref.Digest]
	if !ok || object.Bytes != value.Ref.Bytes {
		return invalid()
	}
	if _, err := ReadHistoryObservation(object.Value); err != nil {
		return invalid()
	}
	batch := []HistoryObject{}
	size := 0
	for _, object := range objects {
		if len(batch) >= 256 || size+object.Bytes > StorageTransactionBytes {
			if err := store.StageHistoryObjects(ctx, HistoryResource{Kind: "run", ID: runID}, batch); err != nil {
				return HistoryObjectRef{}, err
			}
			batch = []HistoryObject{}
			size = 0
		}
		batch = append(batch, object)
		size += object.Bytes
	}
	if len(batch) > 0 {
		if err := store.StageHistoryObjects(ctx, HistoryResource{Kind: "run", ID: runID}, batch); err != nil {
			return HistoryObjectRef{}, err
		}
	}
	return value.Ref, nil
}
