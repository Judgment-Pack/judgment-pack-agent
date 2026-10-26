package runcontext

// History sidecars are host evidence, never execution authority or prompt input.
type FeedbackTarget struct {
	Kind          string `json:"kind"`
	EventSequence int    `json:"eventSequence,omitempty"`
	EventDigest   string `json:"eventDigest,omitempty"`
}
type RunFeedback struct {
	Version    int            `json:"version"`
	ID         string         `json:"id"`
	RunID      string         `json:"runId"`
	Sequence   int            `json:"sequence"`
	Previous   *string        `json:"previous"`
	Target     FeedbackTarget `json:"target"`
	Rating     string         `json:"rating"`
	Remark     *string        `json:"remark,omitempty"`
	Actor      string         `json:"actor"`
	RecordedAt string         `json:"recordedAt"`
}
type HistoryCaptureIdentity struct {
	Surface       string       `json:"surface"`
	Scope         DurableScope `json:"scope"`
	DispatchID    string       `json:"dispatchId"`
	Kind          string       `json:"kind"`
	Source        string       `json:"source"`
	Tool          string       `json:"tool"`
	RequestDigest string       `json:"requestDigest"`
	CaptureIndex  int          `json:"captureIndex"`
}
type HistoryCaptureFile struct {
	Name     string           `json:"name"`
	Manifest HistoryObjectRef `json:"manifest"`
}
type RunCapture struct {
	EventProjection   string                   `json:"eventProjection,omitempty"`
	Version           int                      `json:"version"`
	RunID             string                   `json:"runId"`
	ExecutionRevision string                   `json:"executionRevision"`
	Checkpoint        HistoryObjectRef         `json:"checkpoint"`
	Requests          []HistoryCaptureIdentity `json:"requests"`
	Files             []HistoryCaptureFile     `json:"files"`
	Redacted          bool                     `json:"redacted"`
}
type HistoryMetadata struct {
	CandidateRequested bool              `json:"candidateRequested,omitempty"`
	Candidate          *HistoryObjectRef `json:"candidate,omitempty"`
	Version            int               `json:"version"`
	Kind               string            `json:"kind"`
	Capture            *HistoryObjectRef `json:"capture,omitempty"`
	Feedback           *RunFeedback      `json:"feedback,omitempty"`
}
type HistoryMetadataEntry struct {
	ExecutionRevision string          `json:"executionRevision"`
	CommitSequence    int             `json:"commitSequence"`
	Version           int             `json:"version"`
	Sequence          int             `json:"sequence"`
	RecordedAt        string          `json:"recordedAt"`
	PreviousDigest    *string         `json:"previousDigest"`
	Record            HistoryMetadata `json:"record"`
	Digest            string          `json:"digest"`
}
type HistoryMetadataHead struct {
	Version           int     `json:"version"`
	RunID             string  `json:"runId"`
	Revision          string  `json:"revision"`
	ExecutionRevision string  `json:"executionRevision"`
	Count             int     `json:"count"`
	Root              *string `json:"root"`
}
type HistoryMetadataPage struct {
	Revision      string
	AfterSequence int
	Items         []HistoryMetadataEntry
	NextSequence  int
	HasMore       bool
	Total         int
}
type HistoryMetadataCommit struct {
	RunID                    string
	ExpectedHistoryRevision  string
	ExpectedMetadataRevision *string
	CommitID                 string
	Record                   HistoryMetadata
}

// HistoryFeedbackView keeps the original feedback digest as a source label after redaction.
type HistoryFeedbackView struct {
	SourceDigest string      `json:"sourceDigest"`
	Record       RunFeedback `json:"record"`
}
