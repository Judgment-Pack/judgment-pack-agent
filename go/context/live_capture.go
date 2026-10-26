package runcontext

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/internal/historycapture"
)

// LiveCaptureConfig opts the original history writer into capture. It supplies no resume proof.
// The original resolved policy controls redaction; incomplete coverage is explicitly unavailable.
type LiveCaptureConfig struct {
	Manifest CorpusManifest
	Sink     CorpusSink
}
type liveCapturePending struct {
	claim DurableDispatch
	ref   HistoryObjectRef
}
type captureRootAuthority struct{ writer *RunWriter }
type liveCapture struct {
	rootAuthority         *captureRootAuthority
	collector             *historycapture.Collector
	root                  *historycapture.Root
	tokens                map[string]*historycapture.Token
	pending               []liveCapturePending
	config                LiveCaptureConfig
	unavailable, finished bool
}

func (w *RunWriter) enrollCapture(config *LiveCaptureConfig) error {
	if config == nil {
		return nil
	}
	raw, err := json.Marshal(config.Manifest)
	if err != nil || len(raw) > HistoryObjectBytes {
		return &ReplayUnavailable{}
	}
	var manifest CorpusManifest
	if _, err = readHistoryJSON(raw, HistoryObjectBytes, false, true); err != nil || json.Unmarshal(raw, &manifest) != nil || validateCorpusManifest(manifest) != nil {
		return &ReplayUnavailable{}
	}
	store, ok := w.Store.(HistoryRunStore)
	if !ok || w.history == nil || w.capture != nil || config.Sink == nil {
		return &ReplayUnavailable{}
	}
	c, root, e := historycapture.New(w, w.ID, historycapture.Scope{Execution: w.ID, Role: "author"})
	if e != nil {
		return e
	}
	if e = c.BindPublisher(w, store); e != nil {
		return e
	}
	held := &liveCapture{collector: c, root: root, tokens: map[string]*historycapture.Token{}, config: LiveCaptureConfig{Manifest: manifest, Sink: config.Sink}}
	for _, claim := range w.book.Dispatches {
		if claim.Kind == "model" || claim.Kind == "agent" {
			held.unavailable = true
		}
	}
	w.capture = held
	return nil
}
func liveCaptureIdentity(claim DurableDispatch) HistoryCaptureIdentity {
	surface := "session-model"
	if claim.Kind == "agent" {
		surface = "native-aggregate"
	}
	return HistoryCaptureIdentity{Scope: claim.Scope, DispatchID: claim.ID, Kind: claim.Kind, Source: claim.Source, Tool: claim.Tool, RequestDigest: claim.RequestDigest, Surface: surface}
}
func (w *RunWriter) prepareCaptureLocked(ctx context.Context, state contract.Checkpoint) {
	c := w.capture
	if c == nil || c.unavailable || c.finished {
		return
	}
	if err := w.history.initialize(ctx); err != nil || !w.history.enabled {
		c.unavailable = true
		return
	}
	for _, claim := range w.book.Dispatches {
		if claim.Kind != "model" && claim.Kind != "agent" || c.tokens[claim.ID] != nil {
			continue
		}
		pending := false
		for _, row := range c.pending {
			if row.claim.ID == claim.ID {
				pending = true
			}
		}
		if pending {
			continue
		}
		if claim.State != "claimed" || !claim.Read {
			c.unavailable = true
			return
		}
		prepared, e := PrepareCaptureExpectation(liveCaptureIdentity(claim))
		if e != nil {
			c.unavailable = true
			return
		}
		ref, e := w.history.add(ctx, prepared)
		if e != nil {
			c.unavailable = true
			return
		}
		c.pending = append(c.pending, liveCapturePending{claim, ref})
	}
}
func (w *RunWriter) admitCaptureLocked(history RunHistoryReceipt, commitID string, prepared *preparedLiveHistory) {
	c := w.capture
	if c == nil || c.unavailable || c.finished {
		return
	}
	for _, row := range c.pending {
		ordinal := -1
		for i, ref := range prepared.observations {
			if ref == row.ref {
				ordinal = i
				break
			}
		}
		if ordinal < 0 {
			c.unavailable = true
			return
		}
		i := liveCaptureIdentity(row.claim)
		token, e := c.collector.Admit(w, historycapture.Admission{Identity: historycapture.Identity{Scope: historycapture.Scope{Execution: i.Scope.Execution, Role: i.Scope.Role, Task: i.Scope.Task}, DispatchID: i.DispatchID, Kind: i.Kind, Source: i.Source, Tool: i.Tool, RequestDigest: i.RequestDigest, Surface: i.Surface}, Sequence: history.History.CommitCount, Ordinal: ordinal, CommitID: commitID, Expectation: historycapture.Ref{Digest: row.ref.Digest, Bytes: row.ref.Bytes}, Request: row.claim.Request})
		if e != nil {
			c.unavailable = true
			return
		}
		c.tokens[row.claim.ID] = token
	}
	c.pending = nil
}
func (w *RunWriter) recordCaptureLocked(id string, frame any) {
	c := w.capture
	if c == nil || c.unavailable || c.finished {
		return
	}
	raw, e := canonicalRaw(frame)
	if e != nil || c.tokens[id] == nil {
		c.unavailable = true
		return
	}
	if e = c.collector.Record(w, c.tokens[id], raw); e != nil {
		c.unavailable = true
	}
}
func (w *RunWriter) ackCaptureLocked(id string) {
	c := w.capture
	if c == nil || c.unavailable || c.finished {
		return
	}
	if c.tokens[id] == nil {
		c.unavailable = true
		return
	}
	if e := c.collector.Acknowledge(w, c.tokens[id]); e != nil {
		c.unavailable = true
	}
}

type captureSinkCount struct {
	target CorpusSink
	bytes  int
}

func (s *captureSinkCount) WriteCorpus(id string, files map[string][]byte) error {
	for _, raw := range files {
		s.bytes += len(raw)
	}
	return s.target.WriteCorpus(id, files)
}
func (w *RunWriter) finishCapture(ctx context.Context, binding RunBinding, members PolicyMembers) *contract.RunEvent {
	w.mu.Lock()
	defer w.mu.Unlock()
	c := w.capture
	if c == nil || binding.captureRoot == nil || binding.captureRoot != c.rootAuthority || !binding.Root || binding.Writer != w || binding.Scope != (DurableScope{Execution: w.ID, Role: "author"}) {
		return nil
	}
	refuse := func() *contract.RunEvent {
		reason := "record refused"
		return &contract.RunEvent{Guardrail: &contract.RunEventGuardrail{Type: "guardrail", Tool: "record", Action: "refused", Reason: &reason, Detail: "ReplayUnavailable: a complete supported capture could not be verified."}}
	}
	if c.finished {
		return nil
	}
	c.finished = true
	if c.unavailable || w.fatal != nil || w.latest == nil || w.revision == "" || len(w.acknowledgedCheckpoint) == 0 || (w.latest.Status != "complete" && w.latest.Status != "stopped") {
		return refuse()
	}
	for _, child := range w.book.Children {
		if child.Checkpoint.Status != "complete" && child.Checkpoint.Status != "stopped" {
			return refuse()
		}
	}
	seal, e := c.collector.Seal(w, c.root, historycapture.Terminal{RunID: w.ID, ExecutionRevision: w.revision, Checkpoint: historycapture.Ref{Digest: HistoryBytesDigest(w.acknowledgedCheckpoint), Bytes: len(w.acknowledgedCheckpoint)}, Status: w.latest.Status})
	if e != nil {
		return refuse()
	}
	manifest := historyClone(c.config.Manifest)
	raw, _ := json.Marshal(members)
	_ = json.Unmarshal(raw, &manifest.Policy)
	store := w.Store.(HistoryRunStore)
	prepared, e := PrepareCapturePublication(ctx, store, seal, CapturePublicationOptions{CommitID: newStorageIdentity(), Manifest: manifest, Members: members})
	if e != nil {
		return refuse()
	}
	sink := &captureSinkCount{target: c.config.Sink}
	result, e := CommitCapturePublication(ctx, store, prepared, sink)
	if e != nil || result.Capture != "committed" || result.Delivery != "delivered" {
		return refuse()
	}
	event := recordedEvent(manifest.ID, sink.bytes)
	return &event
}

// A private native context slot is never passed to the provider. The adapter records the
// actual callback sequence; only InvokeDurable binds that slot to an acknowledged claim.
type nativeCaptureKey struct{}
type nativeCaptureSlot struct {
	writer   *RunWriter
	scope    DurableScope
	id       string
	request  string
	recorded bool
}

func NativeCaptureEnabled(ctx context.Context) bool {
	slot, _ := ctx.Value(nativeCaptureKey{}).(*nativeCaptureSlot)
	return slot != nil
}

func RecordNativeCapture(ctx context.Context, body json.RawMessage, aliases map[string]string) {
	slot, _ := ctx.Value(nativeCaptureKey{}).(*nativeCaptureSlot)
	if slot == nil {
		return
	}
	w := slot.writer
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.capture == nil {
		return
	}
	if slot.id == "" || slot.recorded {
		w.capture.unavailable = true
		return
	}
	slot.recorded = true
	var request any
	if json.Unmarshal([]byte(slot.request), &request) != nil {
		w.capture.unavailable = true
		return
	}
	bindings, e := canonicalRaw(aliases)
	if e != nil {
		w.capture.unavailable = true
		return
	}
	w.recordCaptureLocked(slot.id, map[string]any{"suffix": "agent.run", "request": request, "response": map[string]any{"status": 200, "headers": map[string]string{"x-jp-native-tool-bindings": string(bindings)}, "body": string(body)}})
}

type modelCaptureBody struct {
	io.ReadCloser
	raw              []byte
	complete, failed bool
}

func (b *modelCaptureBody) Read(p []byte) (int, error) {
	n, e := b.ReadCloser.Read(p)
	if !b.failed {
		if len(b.raw)+n > HistoryTotalBytes {
			b.failed = true
			b.raw = nil
		} else {
			b.raw = append(b.raw, p[:n]...)
		}
	}
	if e == io.EOF {
		b.complete = true
	} else if e != nil {
		b.failed = true
	}
	return n, e
}
func (w *RunWriter) observeCaptureModel(handle *modelCompletion, response *http.Response, body *modelCaptureBody) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.capture == nil {
		return
	}
	if response == nil || body == nil || body.failed || !body.complete {
		w.capture.unavailable = true
		return
	}
	request, e := ParseJSON(handle.request)
	if e != nil {
		w.capture.unavailable = true
		return
	}
	suffix := request.Get("suffix").Str()
	archiveDelete(request, "suffix")
	headers := map[string]string{}
	for name, values := range response.Header {
		headers[strings.ToLower(name)] = strings.Join(values, ", ")
	}
	w.recordCaptureLocked(handle.id, map[string]any{"suffix": suffix, "request": json.RawMessage(request.JSON()), "response": map[string]any{"status": response.StatusCode, "headers": headers, "body": string(body.raw)}})
}
