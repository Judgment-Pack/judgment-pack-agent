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
	// #368: str reads a member that other files check as a string, and notes one that is missing or not a string; each
	// condition that read one refuses before it acts, so a missing member is HistoryInputInvalid, never a panic and never
	// an empty string compared as a value.
	malformed := false
	str := func(v *Value) string {
		if !v.IsString() {
			malformed = true
			return ""
		}
		return v.Str()
	}
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
		if kind := str(relation.Get("kind")); malformed {
			return invalid()
		} else if kind != "relation" {
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
		if str(evidence.Get("dispatchId")) != str(relation.Get("dispatchId")) || malformed || CanonicalJSON(evidence.Get("scope")) != CanonicalJSON(relation.Get("scope")) || CanonicalJSON(evidence.Get("target")) != CanonicalJSON(relation.Get("target")) {
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
			match := str(candidate.Get("id")) == str(evidence.Get("dispatchId")) && CanonicalJSON(candidate.Get("scope")) == CanonicalJSON(evidence.Get("scope"))
			if malformed {
				return invalid()
			}
			if match {
				claim = candidate
				break
			}
		}
		if claim == nil || !oneOf(claim.Get("kind"), "host", "runtime") || str(claim.Get("state")) != "result" || !claim.Has("result") || str(claim.Get("requestDigest")) != str(evidence.Get("requestDigest")) || malformed || (derived == nil || !derived(ref)) && HistoryBytesDigest([]byte(CanonicalJSON(claim.Get("result")))) != evidence.Get("resultDigest").Str() {
			return invalid()
		}
		var observed *Value
		for _, candidate := range book.Get("observations").Items() {
			match := str(candidate.Get("id")) == str(evidence.Get("observationId")) && str(candidate.Get("kind")) == "operation" && str(candidate.Get("dispatchId")) == str(evidence.Get("dispatchId")) && str(candidate.Get("source")) == str(claim.Get("source")) && CanonicalJSON(candidate.Get("scope")) == CanonicalJSON(evidence.Get("scope"))
			if malformed {
				return invalid()
			}
			if match {
				observed = candidate
				break
			}
		}
		if observed == nil {
			return invalid()
		}
		committed := false
		for _, commit := range commits {
			committed = committed || commit.CommitID == str(observed.Get("commitId")) && commit.Sequence <= row.sequence
		}
		if malformed || !committed {
			return invalid()
		}
		var request *Value
		for _, candidate := range observations {
			r := candidate.value
			match := candidate.sequence <= row.sequence && str(r.Get("kind")) == "request" && str(r.Get("dispatchId")) == str(evidence.Get("dispatchId")) && str(r.Get("source")) == str(claim.Get("source")) && str(r.Get("tool")) == str(claim.Get("tool")) && CanonicalJSON(r.Get("scope")) == CanonicalJSON(evidence.Get("scope"))
			if malformed {
				return invalid()
			}
			if match {
				request = r
				break
			}
		}
		if request == nil {
			return invalid()
		}
		manifest, e := read(archiveRef(request.Get("request")), "request-manifest")
		if e != nil || str(manifest.Get("digest")) != str(evidence.Get("requestDigest")) || malformed {
			return invalid()
		}
	}
	return nil
}
