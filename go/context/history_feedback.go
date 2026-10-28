package runcontext

import (
	"context"
	"encoding/json"
	"errors"
)

type RunFeedbackInput struct {
	RunID                    string
	ID                       string
	CommitID                 string
	ExpectedHistoryRevision  string
	ExpectedMetadataRevision *string
	Previous                 *string
	Target                   FeedbackTarget
	Rating                   string
	Remark                   *string
	Actor                    string
	RecordedAt               string
}
type PrepareRunFeedbackOptions struct {
	Members     PolicyMembers
	ProposeGood bool
}
type PreparedRunFeedback struct {
	Commit    HistoryMetadataCommit
	Source    RunHistoryHead
	Candidate *CorpusCandidate
}
type RunFeedbackPublication struct {
	CandidateDeferred bool
	Superseded        bool
	Head              RunHistoryHead
	Candidate         *CorpusCandidate
}
type RunFeedbackResult struct {
	RunFeedbackPublication
	Delivery string
}
type FeedbackOutcomeUnknown struct{}

func (*FeedbackOutcomeUnknown) Error() string {
	return "FeedbackOutcomeUnknown: metadata acknowledgement could not be verified."
}
func feedbackTextPrefix(text string) string {
	units := 0
	for i, r := range text {
		n := 1
		if r > 0xffff {
			n = 2
		}
		if units+n > 2000 {
			return text[:i]
		}
		units += n
	}
	return text
}

// PrepareRunFeedback constructs sanitized review material before the metadata transaction.
func PrepareRunFeedback(ctx context.Context, store HistoryRunStore, input RunFeedbackInput, options PrepareRunFeedbackOptions) (PreparedRunFeedback, error) {
	invalid := func() (PreparedRunFeedback, error) { return PreparedRunFeedback{}, &HistoryInputInvalid{} }
	view, e := ReadRetainedRunHistory(ctx, store, input.RunID)
	if e != nil {
		return PreparedRunFeedback{}, e
	}
	source := view.Head()
	if source.Revision != input.ExpectedHistoryRevision || !archiveEqual(source.MetadataRevision(), input.ExpectedMetadataRevision) {
		return PreparedRunFeedback{}, &HistoryChanged{}
	}
	if source.Checkpoint == nil {
		return invalid()
	}
	raw, e := view.ReadObject(*source.Checkpoint)
	if e != nil {
		return invalid()
	}
	value, e := ParseJSON(raw)
	if e != nil {
		return invalid()
	}
	checkpoint, e := ReadExecution(value)
	if e != nil || checkpoint == nil {
		return invalid()
	}
	var prior *RunFeedback
	for _, row := range view.Metadata() {
		if row.Record.Kind == "feedback" && row.Record.Feedback.ID == input.ID {
			f := *row.Record.Feedback
			prior = &f
		}
	}
	sequence := 1
	var previous *string
	if prior != nil {
		sequence = prior.Sequence + 1
		digest := HistoryBytesDigest([]byte(historyInputText(*prior)))
		previous = &digest
	}
	if !archiveEqual(input.Previous, previous) || prior != nil && !archiveEqual(prior.Target, input.Target) {
		return invalid()
	}
	feedback := RunFeedback{Version: 1, ID: input.ID, RunID: input.RunID, Sequence: sequence, Previous: historyClone(input.Previous), Target: historyClone(input.Target), Rating: input.Rating, Remark: historyClone(input.Remark), Actor: input.Actor, RecordedAt: input.RecordedAt}
	decoded, e := ReadRunFeedback([]byte(historyInputText(feedback)))
	if e != nil {
		return invalid()
	}
	feedback = *decoded
	if feedback.Target.Kind == "assistant-message" {
		after, limit := feedback.Target.EventSequence-1, 1
		page, e := QueryHistoryEvents(view, HistoryEventQuery{AfterSequence: &after, Limit: &limit})
		if e != nil || len(page.Items) != 1 || page.Items[0].Sequence != feedback.Target.EventSequence || page.Items[0].Source.Digest != feedback.Target.EventDigest || page.Items[0].Event.Get("type").Str() != "message" {
			return invalid()
		}
	}
	var candidate *CorpusCandidate
	var candidateRef *HistoryObjectRef
	candidateRequested := feedback.Rating == "bad" || options.ProposeGood
	if candidateRequested && (checkpoint.Status == "complete" || checkpoint.Status == "stopped") {
		exported, e := ExportRunArchive(ctx, store, input.RunID, ArchiveExportOptions{Members: options.Members})
		if e != nil {
			return PreparedRunFeedback{}, e
		}
		if exported.Manifest.Source.Revision != source.Revision {
			return PreparedRunFeedback{}, &HistoryChanged{}
		}
		archived, e := ReadRunArchive(exported.Bytes, exported.Digest)
		if e != nil {
			return invalid()
		}
		members := PolicyMembers{}
		for k, v := range options.Members {
			members[k] = v
		}
		members["outputGuard"] = PolicyMember{Value: "redact"}
		guard, e := NewOutputGuard(members, func() map[string]int64 { return map[string]int64{} })
		if e != nil {
			return invalid()
		}
		cleanRaw, e := guard.JSON(json.RawMessage(historyInputText(feedback)), "tool argument", false)
		if e != nil {
			return invalid()
		}
		var cleanFeedback RunFeedback
		if json.Unmarshal(cleanRaw, &cleanFeedback) != nil {
			return invalid()
		}
		before, after := feedback, cleanFeedback
		before.Remark = nil
		after.Remark = nil
		if !archiveEqual(before, after) {
			return invalid()
		}
		limit := 20
		q := HistoryEventQuery{Types: []string{"message"}, Limit: &limit}
		if feedback.Target.Kind == "assistant-message" {
			after := feedback.Target.EventSequence - 1
			one := 1
			q.AfterSequence = &after
			q.Limit = &one
		}
		page, e := QueryHistoryEvents(archived, q)
		if e != nil {
			return invalid()
		}
		messages := []CorpusCandidateMessage{}
		for _, row := range page.Items {
			messages = append(messages, CorpusCandidateMessage{row.Sequence, feedbackTextPrefix(row.Event.Get("text").Str())})
		}
		report, e := archived.ReadDisplay(archived.manifest.Report, "report")
		if e != nil {
			return invalid()
		}
		feedbackDigest := HistoryBytesDigest([]byte(historyInputText(feedback)))
		held := CorpusCandidate{Version: 1, ID: CorpusCandidateIdentity(input.RunID, feedbackDigest, exported.Digest), Status: "pending-review", RunID: input.RunID, FeedbackDigest: feedbackDigest, ArchiveDigest: exported.Digest, SourceView: source.Revision, Feedback: cleanFeedback, Review: CorpusCandidateReview{Report: json.RawMessage(CanonicalJSON(report.Document)), Messages: messages}, Replay: CorpusCandidateReplay{Kind: "unavailable", Reason: "not-recorded"}}
		candidate, e = ReadCorpusCandidate([]byte(historyInputText(held)))
		if e != nil || validateCandidateFeedback(candidate, feedback) != nil {
			return invalid()
		}
		object, e := NewHistoryObject([]byte(historyInputText(*candidate)))
		if e != nil {
			return invalid()
		}
		if e = store.StageHistoryObjects(ctx, HistoryResource{Kind: "run", ID: input.RunID}, []HistoryObject{object}); e != nil {
			return PreparedRunFeedback{}, e
		}
		ref := object.HistoryObjectRef
		candidateRef = &ref
	}
	return PreparedRunFeedback{Commit: HistoryMetadataCommit{RunID: input.RunID, ExpectedHistoryRevision: source.Revision, ExpectedMetadataRevision: historyClone(input.ExpectedMetadataRevision), CommitID: input.CommitID, Record: HistoryMetadata{Version: 1, Kind: "feedback", Feedback: &feedback, Candidate: candidateRef, CandidateRequested: candidateRequested}}, Source: source, Candidate: candidate}, nil
}
func (h RunHistoryHead) MetadataRevision() *string {
	if h.Metadata == nil {
		return nil
	}
	s := h.Metadata.Revision
	return &s
}

// CommitPreparedRunFeedback verifies the exact receipt and immutable row before exposing delivery material.
func CommitPreparedRunFeedback(ctx context.Context, store HistoryRunStore, prepared PreparedRunFeedback) (RunFeedbackPublication, error) {
	invalid := func() (RunFeedbackPublication, error) { return RunFeedbackPublication{}, &HistoryInputInvalid{} }
	source := historyClone(prepared.Source)
	commit := historyClone(prepared.Commit)
	if commit.RunID != source.RunID || commit.ExpectedHistoryRevision != source.Revision || !archiveEqual(commit.ExpectedMetadataRevision, source.MetadataRevision()) || commit.Record.Kind != "feedback" {
		return invalid()
	}
	head, e := store.CommitHistoryMetadata(ctx, commit)
	if e != nil {
		var conflict *StorageConflict
		var bad *HistoryInputInvalid
		if errors.As(e, &conflict) || errors.As(e, &bad) {
			return RunFeedbackPublication{}, e
		}
		return RunFeedbackPublication{}, &FeedbackOutcomeUnknown{}
	}
	unknown := func() (RunFeedbackPublication, error) { return RunFeedbackPublication{}, &FeedbackOutcomeUnknown{} }
	before, after := source, head
	before.Revision = ""
	before.Metadata = nil
	after.Revision = ""
	after.Metadata = nil
	count := 0
	var previous *string
	if source.Metadata != nil {
		count = source.Metadata.Count
		previous = source.Metadata.Root
	}
	if !archiveEqual(before, after) || head.Revision == source.Revision || head.Metadata == nil || head.Metadata.Count != count+1 || source.Metadata != nil && head.Metadata.Revision == source.Metadata.Revision {
		return unknown()
	}
	checked, e := ReadRunHistoryHead([]byte(historyInputText(head)))
	if e != nil || !archiveEqual(checked, head) {
		return unknown()
	}
	page, e := store.ReadHistoryMetadataPage(ctx, source.RunID, head.Revision, count, 1)
	if e != nil || page.Revision != head.Revision || page.AfterSequence != count || len(page.Items) != 1 || page.NextSequence != count+1 || page.HasMore || page.Total != head.Metadata.Count {
		return unknown()
	}
	row, e := ReadHistoryMetadataEntry([]byte(historyInputText(page.Items[0])))
	if e != nil || row.Sequence != count+1 || !archiveEqual(row.PreviousDigest, previous) || head.Metadata.Root == nil || row.Digest != *head.Metadata.Root || row.ExecutionRevision != source.ExecutionRevision || row.CommitSequence != source.CommitCount || !archiveEqual(row.Record, commit.Record) {
		return unknown()
	}
	var candidate *CorpusCandidate
	if commit.Record.Candidate != nil {
		ref := *commit.Record.Candidate
		object, e := store.ReadHistoryObject(ctx, HistoryResource{Kind: "run", ID: source.RunID}, head.Revision, ref.Digest, 0, HistoryObjectBytes)
		if e != nil || object.Digest != ref.Digest || object.Total != ref.Bytes || object.Offset != 0 || object.NextOffset != ref.Bytes || object.HasMore || len(object.Value) != ref.Bytes || HistoryBytesDigest(object.Value) != ref.Digest {
			return unknown()
		}
		candidate, e = ReadCorpusCandidate(object.Value)
		if e != nil || validateCandidateFeedback(candidate, *commit.Record.Feedback) != nil {
			return unknown()
		}
	}
	current, e := ReadRetainedRunHistory(ctx, store, source.RunID)
	if e != nil {
		return unknown()
	}
	var latest *RunFeedback
	for _, row := range current.Metadata() {
		if row.Record.Kind == "feedback" && row.Record.Feedback.ID == commit.Record.Feedback.ID {
			latest = row.Record.Feedback
		}
	}
	if latest == nil || historyInputText(*latest) != historyInputText(*commit.Record.Feedback) {
		return RunFeedbackPublication{Head: head, Superseded: true}, nil
	}
	return RunFeedbackPublication{Head: head, Candidate: candidate, CandidateDeferred: commit.Record.CandidateRequested && candidate == nil}, nil
}
func WriteRunFeedback(ctx context.Context, store HistoryRunStore, input RunFeedbackInput, options PrepareRunFeedbackOptions, sink CorpusCandidateSink) (RunFeedbackResult, error) {
	prepared, e := PrepareRunFeedback(ctx, store, input, options)
	if e != nil {
		return RunFeedbackResult{}, e
	}
	published, e := CommitPreparedRunFeedback(ctx, store, prepared)
	if e != nil {
		return RunFeedbackResult{}, e
	}
	delivery := "not-requested"
	if published.Superseded {
		delivery = "superseded"
	} else if published.CandidateDeferred {
		delivery = "deferred"
	}
	if published.Candidate != nil {
		delivery = "pending"
		if sink != nil {
			if e = sink.WriteCandidate(published.Candidate.ID, historyClone(*published.Candidate)); e != nil {
				delivery = "unknown"
			} else {
				delivery = "delivered"
			}
		}
	}
	return RunFeedbackResult{published, delivery}, nil
}
