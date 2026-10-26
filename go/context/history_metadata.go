package runcontext

import (
	"encoding/json"
	"time"
)

func historyID(v *Value) bool { return v != nil && v.IsString() && validStorageID(v.Str()) }
func historyRefValue(v *Value) bool {
	if !snapshotObject(v, []string{"digest", "bytes"}) || !v.Get("digest").IsString() || !snapshotCount(v.Get("bytes"), 1) {
		return false
	}
	return validHistoryRef(HistoryObjectRef{Digest: v.Get("digest").Str(), Bytes: int(v.Get("bytes").Num())})
}
func historyScopeValue(v *Value) bool {
	return snapshotObject(v, []string{"execution", "role"}, "task") && historyID(v.Get("execution")) && historyID(v.Get("role")) && (!v.Has("task") || historyID(v.Get("task")))
}
func historyTimestamp(v *Value) bool {
	if v == nil || !v.IsString() {
		return false
	}
	parsed, err := time.Parse("2006-01-02T15:04:05.000Z", v.Str())
	return err == nil && parsed.Format("2006-01-02T15:04:05.000Z") == v.Str()
}
func validFeedbackValue(v *Value) bool {
	if !snapshotObject(v, []string{"version", "id", "runId", "sequence", "previous", "target", "rating", "actor", "recordedAt"}, "remark") || v.Get("version").Kind() != KindNumber || v.Get("version").Num() != 1 || !historyID(v.Get("id")) || !historyID(v.Get("runId")) || !snapshotCount(v.Get("sequence"), 1) || v.Get("previous").Kind() != KindNull && !historySHA256.MatchString(v.Get("previous").Str()) || !oneOf(v.Get("rating"), "good", "bad") || !historyID(v.Get("actor")) || !historyTimestamp(v.Get("recordedAt")) || v.Has("remark") && !v.Get("remark").IsString() {
		return false
	}
	target := v.Get("target")
	return snapshotObject(target, []string{"kind"}) && target.Get("kind").Str() == "run" || snapshotObject(target, []string{"kind", "eventSequence", "eventDigest"}) && target.Get("kind").Str() == "assistant-message" && snapshotCount(target.Get("eventSequence"), 1) && historySHA256.MatchString(target.Get("eventDigest").Str())
}
func ReadRunFeedback(raw []byte) (*RunFeedback, error) {
	v, err := readCanonicalHistoryJSON(raw, HistoryObjectBytes)
	if err != nil || !validFeedbackValue(v) {
		return nil, &HistoryInputInvalid{}
	}
	var out RunFeedback
	if err = json.Unmarshal(raw, &out); err != nil {
		return nil, &HistoryInputInvalid{}
	}
	return &out, nil
}
func ReadHistoryMetadata(raw []byte) (*HistoryMetadata, error) {
	v, err := readCanonicalHistoryJSON(raw, HistoryObjectBytes)
	if err != nil {
		return nil, err
	}
	valid := snapshotObject(v, []string{"version", "kind", "capture"}) && v.Get("version").Num() == 1 && v.Get("kind").Str() == "capture" && historyRefValue(v.Get("capture")) || snapshotObject(v, []string{"version", "kind", "feedback"}, "candidate", "candidateRequested") && v.Get("version").Num() == 1 && v.Get("kind").Str() == "feedback" && validFeedbackValue(v.Get("feedback")) && (!v.Has("candidate") || v.Has("candidateRequested") && v.Get("candidateRequested").Kind() == KindBool && v.Get("candidateRequested").Truth() && historyRefValue(v.Get("candidate"))) && (!v.Has("candidateRequested") || v.Get("candidateRequested").Kind() == KindBool && v.Get("candidateRequested").Truth())
	if !valid {
		return nil, &HistoryInputInvalid{}
	}
	var out HistoryMetadata
	if err = json.Unmarshal(raw, &out); err != nil {
		return nil, &HistoryInputInvalid{}
	}
	return &out, nil
}
func ReadRunCapture(raw []byte) (*RunCapture, error) {
	v, err := readCanonicalHistoryJSON(raw, HistoryObjectBytes)
	if err != nil {
		return nil, err
	}
	if !snapshotObject(v, []string{"version", "runId", "executionRevision", "checkpoint", "requests", "files", "redacted"}, "eventProjection") || v.Has("eventProjection") && (!v.Get("eventProjection").IsString() || v.Get("eventProjection").Str() != "transcript-v1") || v.Get("version").Num() != 1 || !historyID(v.Get("runId")) || !historyID(v.Get("executionRevision")) || !historyRefValue(v.Get("checkpoint")) || v.Get("redacted").Kind() != KindBool || !v.Get("redacted").Truth() || !v.Get("requests").IsArray() || v.Get("requests").Len() > HistoryMaxEvents || !v.Get("files").IsArray() {
		return nil, &HistoryInputInvalid{}
	}
	names := []string{"checkpoint.json", "events.json", "expected.json", "manifest.json", "requests.json", "tool-results.json"}
	if v.Get("files").Len() != len(names) {
		return nil, &HistoryInputInvalid{}
	}
	for i, row := range v.Get("files").Items() {
		if !snapshotObject(row, []string{"name", "manifest"}) || row.Get("name").Str() != names[i] || !historyRefValue(row.Get("manifest")) {
			return nil, &HistoryInputInvalid{}
		}
	}
	seen := map[string]bool{}
	for i, row := range v.Get("requests").Items() {
		if !snapshotObject(row, []string{"scope", "dispatchId", "kind", "source", "tool", "requestDigest", "surface", "captureIndex"}) || !historyScopeValue(row.Get("scope")) || !historyID(row.Get("dispatchId")) || seen[row.Get("dispatchId").Str()] || !oneOf(row.Get("kind"), "model", "agent") || !historyID(row.Get("source")) || !historyID(row.Get("tool")) || !historySHA256.MatchString(row.Get("requestDigest").Str()) || row.Get("kind").Str() == "model" && row.Get("surface").Str() != "session-model" || row.Get("kind").Str() == "agent" && row.Get("surface").Str() != "native-aggregate" || !snapshotCount(row.Get("captureIndex"), 0) || int(row.Get("captureIndex").Num()) != i {
			return nil, &HistoryInputInvalid{}
		}
		seen[row.Get("dispatchId").Str()] = true
	}
	var out RunCapture
	if err = json.Unmarshal(raw, &out); err != nil {
		return nil, &HistoryInputInvalid{}
	}
	return &out, nil
}
