package runcontext

// ReadHistoryRelationEvidence reads a closed host assertion; association requires retained journal validation.
func ReadHistoryRelationEvidence(raw []byte) (*Value, error) {
	v, e := readCanonicalHistoryJSON(raw, HistoryObjectBytes)
	if e != nil || !snapshotObject(v, []string{"version", "kind", "scope", "dispatchId", "checkpoint", "observationId", "requestDigest", "resultDigest", "target"}) || v.Get("version").Num() != 1 || v.Get("kind").Str() != "observed-relation" || !historyScopeValue(v.Get("scope")) || !historyID(v.Get("dispatchId")) || !historyID(v.Get("observationId")) || !historyRefValue(v.Get("checkpoint")) || !historySHA256.MatchString(v.Get("requestDigest").Str()) || !historySHA256.MatchString(v.Get("resultDigest").Str()) || !snapshotObject(v.Get("target"), []string{"kind", "id"}) || !oneOf(v.Get("target").Get("kind"), "job", "artifact", "dialect") || !historyID(v.Get("target").Get("id")) {
		return nil, &HistoryInputInvalid{}
	}
	return v, nil
}

// Source associations distinguish an explicit host relation from a provider-success claim.
// A redacted checkpoint keeps its original digest as a source label; sanitized result bytes do not claim that hash.
func validateHistoryRelations(commits []RunHistoryEntry, read func(HistoryObjectRef, string) (*Value, error), derived func(HistoryObjectRef) bool) error {
	invalid := func() error { return &HistoryInputInvalid{} }
	type observation struct {
		sequence int
		value    *Value
	}
	observations := []observation{}
	for _, commit := range commits {
		for _, ref := range commit.Observations {
			value, e := read(ref, "observation")
			if e != nil {
				return e
			}
			observations = append(observations, observation{commit.Sequence, value})
		}
	}
	for _, row := range observations {
		relation := row.value
		if relation.Get("kind").Str() != "relation" {
			continue
		}
		evidence, e := read(archiveRef(relation.Get("evidence")), "evidence")
		if e != nil {
			return e
		}
		evidence, e = ReadHistoryRelationEvidence([]byte(CanonicalJSON(evidence)))
		if e != nil {
			return e
		}
		if evidence.Get("dispatchId").Str() != relation.Get("dispatchId").Str() || CanonicalJSON(evidence.Get("scope")) != CanonicalJSON(relation.Get("scope")) || CanonicalJSON(evidence.Get("target")) != CanonicalJSON(relation.Get("target")) {
			return invalid()
		}

		ref := archiveRef(evidence.Get("checkpoint"))
		found := false
		for _, commit := range commits {
			found = found || commit.Sequence <= row.sequence && commit.Checkpoint != nil && *commit.Checkpoint == ref
		}
		if !found {
			return invalid()
		}
		checkpoint, e := read(ref, "checkpoint")
		if e != nil {
			return e
		}
		book := checkpoint.Get("durable")
		var claim *Value
		for _, candidate := range book.Get("dispatches").Items() {
			if candidate.Get("id").Str() == evidence.Get("dispatchId").Str() && CanonicalJSON(candidate.Get("scope")) == CanonicalJSON(evidence.Get("scope")) {
				claim = candidate
				break
			}
		}
		if claim == nil || !oneOf(claim.Get("kind"), "host", "runtime") || claim.Get("state").Str() != "result" || !claim.Has("result") || claim.Get("requestDigest").Str() != evidence.Get("requestDigest").Str() || (derived == nil || !derived(ref)) && HistoryBytesDigest([]byte(CanonicalJSON(claim.Get("result")))) != evidence.Get("resultDigest").Str() {
			return invalid()
		}
		var observed *Value
		for _, candidate := range book.Get("observations").Items() {
			if candidate.Get("id").Str() == evidence.Get("observationId").Str() && candidate.Get("kind").Str() == "operation" && candidate.Get("dispatchId").Str() == evidence.Get("dispatchId").Str() && candidate.Get("source").Str() == claim.Get("source").Str() && CanonicalJSON(candidate.Get("scope")) == CanonicalJSON(evidence.Get("scope")) {
				observed = candidate
				break
			}
		}
		if observed == nil {
			return invalid()
		}
		committed := false
		for _, commit := range commits {
			committed = committed || commit.CommitID == observed.Get("commitId").Str() && commit.Sequence <= row.sequence
		}
		if !committed {
			return invalid()
		}
		var request *Value
		for _, candidate := range observations {
			r := candidate.value
			if candidate.sequence <= row.sequence && r.Get("kind").Str() == "request" && r.Get("dispatchId").Str() == evidence.Get("dispatchId").Str() && r.Get("source").Str() == claim.Get("source").Str() && r.Get("tool").Str() == claim.Get("tool").Str() && CanonicalJSON(r.Get("scope")) == CanonicalJSON(evidence.Get("scope")) {
				request = r
				break
			}
		}
		if request == nil {
			return invalid()
		}
		manifest, e := read(archiveRef(request.Get("request")), "request-manifest")
		if e != nil || manifest.Get("digest").Str() != evidence.Get("requestDigest").Str() {
			return invalid()
		}
	}
	return nil
}
