package runcontext

import (
	"encoding/json"
)

// CorpusCandidate is redacted material awaiting human review, never a replay entry or corpus admission.
type CorpusCandidate struct {
	Version        int                   `json:"version"`
	ID             string                `json:"id"`
	Status         string                `json:"status"`
	RunID          string                `json:"runId"`
	FeedbackDigest string                `json:"feedbackDigest"`
	ArchiveDigest  string                `json:"archiveDigest"`
	SourceView     string                `json:"sourceView"`
	Feedback       RunFeedback           `json:"feedback"`
	Review         CorpusCandidateReview `json:"review"`
	Replay         CorpusCandidateReplay `json:"replay"`
}
type CorpusCandidateReview struct {
	Report   json.RawMessage          `json:"report"`
	Messages []CorpusCandidateMessage `json:"messages"`
}
type CorpusCandidateMessage struct {
	Sequence int    `json:"sequence"`
	Text     string `json:"text"`
}
type CorpusCandidateReplay struct {
	Kind   string `json:"kind"`
	Reason string `json:"reason"`
}
type CorpusCandidateSink interface {
	WriteCandidate(id string, candidate CorpusCandidate) error
}

func CorpusCandidateIdentity(runID, feedbackDigest, archiveDigest string) string {
	return HistoryBytesDigest([]byte(historyInputText(map[string]string{"runId": runID, "feedbackDigest": feedbackDigest, "archiveDigest": archiveDigest})))
}
func ReadCorpusCandidate(raw []byte) (*CorpusCandidate, error) {
	invalid := func() (*CorpusCandidate, error) { return nil, &HistoryInputInvalid{} }
	v, e := readCanonicalHistoryJSON(raw, HistoryObjectBytes)
	if e != nil || !snapshotObject(v, []string{"version", "id", "status", "runId", "feedbackDigest", "archiveDigest", "sourceView", "feedback", "review", "replay"}) || v.Get("version").Num() != 1 || v.Get("status").Str() != "pending-review" || !historyID(v.Get("runId")) || !historyID(v.Get("sourceView")) || !historySHA256.MatchString(v.Get("feedbackDigest").Str()) || !historySHA256.MatchString(v.Get("archiveDigest").Str()) || v.Get("id").Str() != CorpusCandidateIdentity(v.Get("runId").Str(), v.Get("feedbackDigest").Str(), v.Get("archiveDigest").Str()) || !snapshotObject(v.Get("review"), []string{"report", "messages"}) || !v.Get("review").Get("messages").IsArray() || v.Get("review").Get("messages").Len() > 20 || !snapshotObject(v.Get("replay"), []string{"kind", "reason"}) || v.Get("replay").Get("kind").Str() != "unavailable" || v.Get("replay").Get("reason").Str() != "not-recorded" {
		return invalid()
	}
	if _, e = ReadRunFeedback([]byte(CanonicalJSON(v.Get("feedback")))); e != nil || v.Get("feedback").Get("runId").Str() != v.Get("runId").Str() {
		return invalid()
	}
	if report, err := readRunReportDisplay(v.Get("review").Get("report")); err != nil || report.Get("id").Str() != v.Get("runId").Str() {
		return invalid()
	}
	previous := 0
	for _, row := range v.Get("review").Get("messages").Items() {
		if !snapshotObject(row, []string{"sequence", "text"}) || !snapshotCount(row.Get("sequence"), 1) || int(row.Get("sequence").Num()) <= previous || !row.Get("text").IsString() || len(u16(row.Get("text").Str())) > 2000 {
			return invalid()
		}
		previous = int(row.Get("sequence").Num())
	}
	if target := v.Get("feedback").Get("target"); target.Get("kind").Str() == "assistant-message" && (v.Get("review").Get("messages").Len() != 1 || v.Get("review").Get("messages").Items()[0].Get("sequence").Num() != target.Get("eventSequence").Num()) {
		return invalid()
	}
	var out CorpusCandidate
	if json.Unmarshal(raw, &out) != nil {
		return invalid()
	}
	return &out, nil
}
func validateCandidateFeedback(candidate *CorpusCandidate, feedback RunFeedback) error {
	before, after := feedback, candidate.Feedback
	before.Remark = nil
	after.Remark = nil
	if candidate.FeedbackDigest != HistoryBytesDigest([]byte(historyInputText(feedback))) || candidate.RunID != feedback.RunID || !archiveEqual(before, after) || (candidate.Feedback.Remark == nil) != (feedback.Remark == nil) {
		return &HistoryInputInvalid{}
	}
	return nil
}
