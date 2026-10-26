package runcontext

import (
	"encoding/base64"
	"encoding/json"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"reflect"
	"strings"
)

// ReplayUnavailable is a fixed availability result, never permission to call a live provider.
type ReplayUnavailable struct{}

func (*ReplayUnavailable) Error() string {
	return "ReplayUnavailable: a complete supported capture could not be verified."
}

// CaptureReadView supplies read-only native evidence, not a writer or a live capture issuer.
type CaptureReadView interface {
	Head() RunHistoryHead
	Commits() []RunHistoryEntry
	Metadata() []HistoryMetadataEntry
	ReadObject(HistoryObjectRef) ([]byte, error)
}
type capturePort struct {
	head     RunHistoryHead
	commits  []RunHistoryEntry
	metadata []HistoryMetadataEntry
	read     func(HistoryObjectRef, ...string) ([]byte, error)
}

func (p capturePort) Head() RunHistoryHead                          { return p.head }
func (p capturePort) Commits() []RunHistoryEntry                    { return p.commits }
func (p capturePort) Metadata() []HistoryMetadataEntry              { return p.metadata }
func (p capturePort) ReadObject(r HistoryObjectRef) ([]byte, error) { return p.read(r) }
func (p capturePort) readRoleObject(r HistoryObjectRef, kind string) ([]byte, error) {
	return p.read(r, kind)
}

type captureEvent struct {
	scope    DurableScope
	event    contract.RunEvent
	value    *Value
	sequence int
}
type captureClaim struct {
	claim DurableDispatch
	first int
}
type captureExpectation struct {
	identity HistoryCaptureIdentity
	sequence int
}
type captureRequest struct {
	scope                    DurableScope
	id, digest, source, tool string
	sequence                 int
}

func captureDisplayMatches(a, b *Value) bool { _, e := archiveDiff(a, b, "", false); return e == nil }
func validCaptureAgentBody(raw string) bool {
	v, e := readHistoryRequestJSON([]byte(raw))
	if e != nil || !snapshotObject(v, []string{"steps"}) || !v.Get("steps").IsArray() {
		return false
	}
	for _, s := range v.Get("steps").Items() {
		if snapshotObject(s, []string{"event"}) {
			x := s.Get("event")
			if snapshotObject(x, []string{"type", "id", "text"}, "phase") && oneOf(x.Get("type"), "text", "message") && x.Get("id").IsString() && x.Get("text").IsString() && (!x.Has("phase") || oneOf(x.Get("phase"), "commentary", "final")) {
				continue
			}
			if snapshotObject(x, []string{"type", "attempt"}) && x.Get("type").Str() == "retry" && snapshotCount(x.Get("attempt"), 1) {
				continue
			}
			return false
		} else if snapshotObject(s, []string{"tool"}) {
			x := s.Get("tool")
			if !snapshotObject(x, []string{"id", "name", "arguments"}) || !x.Get("id").IsString() || !x.Get("name").IsString() || !x.Get("arguments").IsObject() {
				return false
			}
		} else {
			return false
		}
	}
	return true
}

// ReadCapturedCorpus verifies actual files against pinned journal evidence. It never dispatches or resumes.
func ReadCapturedCorpus(view CaptureReadView) (*CorpusEntry, error) {
	return readCapturedCorpus(view, false, false, nil)
}

type captureDisplayPort struct {
	read     func(HistoryObjectRef, string) ([]byte, bool, error)
	metadata []HistoryMetadataEntry
}

const nativeBindingsHeader = "x-jp-native-tool-bindings"

func nativeCaptureBindings(request, headers *Value) (*Value, error) {
	if !headers.Has(nativeBindingsHeader) {
		for _, tool := range request.Get("tools").Items() {
			name := tool.Get("name").Str()
			if strings.HasPrefix(name, "desk_host_") || strings.HasPrefix(name, "desk_runtime_") {
				return nil, &ReplayUnavailable{}
			}
		}
		return nil, nil
	}
	raw := headers.Get(nativeBindingsHeader)
	if !raw.IsString() {
		return nil, &ReplayUnavailable{}
	}
	bindings, e := readHistoryJSON([]byte(raw.Str()), HistoryObjectBytes, true)
	if e != nil || !bindings.IsObject() || !request.Get("tools").IsArray() || bindings.Len() != request.Get("tools").Len() {
		return nil, &ReplayUnavailable{}
	}
	aliases, names := map[string]bool{}, map[string]bool{}
	for _, tool := range request.Get("tools").Items() {
		alias := tool.Get("name")
		if !alias.IsString() || aliases[alias.Str()] || !bindings.Has(alias.Str()) {
			return nil, &ReplayUnavailable{}
		}
		name := bindings.Get(alias.Str())
		if !name.IsString() || name.Str() == "" || names[name.Str()] {
			return nil, &ReplayUnavailable{}
		}
		aliases[alias.Str()] = true
		names[name.Str()] = true
	}
	return bindings, nil
}
func readCapturedCorpus(view CaptureReadView, original, display bool, port *captureDisplayPort) (*CorpusEntry, error) {
	matches := captureDisplayMatches
	if original {
		matches = func(a, b *Value) bool { return archiveEqual(a, b) }
	}
	unavailable := func() (*CorpusEntry, error) { return nil, &ReplayUnavailable{} }
	if view == nil || reflect.ValueOf(view).Kind() == reflect.Pointer && reflect.ValueOf(view).IsNil() {
		return unavailable()
	}
	head, e := ReadRunHistoryHead([]byte(historyInputText(view.Head())))
	if e != nil || head.Checkpoint == nil {
		return unavailable()
	}
	cache := map[string][]byte{}
	changed := map[string]bool{}
	counted := map[string]bool{}
	total := 0
	get := func(ref HistoryObjectRef, kind string) ([]byte, error) {
		if !validHistoryRef(ref) {
			return nil, &ReplayUnavailable{}
		}
		key := archiveRoleKey(ref.Digest, kind)
		if old, ok := cache[key]; ok {
			if !changed[key] && len(old) != ref.Bytes {
				return nil, &ReplayUnavailable{}
			}
			return old, nil
		}
		if _, seen := counted[ref.Digest]; !seen {
			total += ref.Bytes
			counted[ref.Digest] = true
		}
		if total > HistoryTotalBytes {
			return nil, &ReplayUnavailable{}
		}
		var raw []byte
		var e error
		if port != nil {
			var exact bool
			raw, exact, e = port.read(ref, kind)
			changed[key] = !exact
		} else if typed, ok := view.(interface {
			readRoleObject(HistoryObjectRef, string) ([]byte, error)
		}); ok {
			raw, e = typed.readRoleObject(ref, kind)
		} else {
			raw, e = view.ReadObject(ref)
		}
		raw = append([]byte(nil), raw...)
		if e != nil || len(raw) > HistoryObjectBytes || !changed[key] && (len(raw) != ref.Bytes || HistoryBytesDigest(raw) != ref.Digest) {
			return nil, &ReplayUnavailable{}
		}
		cache[key] = append([]byte(nil), raw...)
		return raw, nil
	}
	value := func(ref HistoryObjectRef) (*Value, error) {
		raw, e := get(ref, "checkpoint")
		if e != nil {
			return nil, e
		}
		return readHistoryJSON(raw, HistoryObjectBytes, true, true)
	}
	payload := func(ref HistoryObjectRef, prefix string) (*Value, error) {
		raw, e := get(ref, prefix+"-manifest")
		if e != nil {
			return nil, e
		}
		m, e := ReadHistoryChunkManifest(raw)
		if e != nil {
			return nil, e
		}
		full := []byte{}
		derived := false
		for _, p := range m.Parts {
			raw, e = get(p, prefix+"-chunk")
			if e != nil || len(full)+len(raw) > HistoryTotalBytes {
				return nil, &ReplayUnavailable{}
			}
			full = append(full, raw...)
			derived = derived || changed[archiveRoleKey(p.Digest, prefix+"-chunk")]
		}
		if !derived && (len(full) != m.Bytes || HistoryBytesDigest(full) != m.Digest) {
			return nil, &ReplayUnavailable{}
		}
		if prefix == "request" {
			return readHistoryRequestJSON(full)
		}
		return readHistoryJSON(full, HistoryTotalBytes, true, true)
	}
	checkpointDocument := func(v *Value, derived bool) (*contract.Checkpoint, error) {
		if !display || !derived {
			return ReadExecution(v)
		}
		if e := archiveValidate("checkpoint", v, true); e != nil {
			return nil, e
		}
		var cp contract.Checkpoint
		if e := json.Unmarshal(v.JSON(), &cp); e != nil {
			return nil, e
		}
		return &cp, nil
	}
	cpValue, e := value(*head.Checkpoint)
	if e != nil {
		return unavailable()
	}
	checkpoint, e := checkpointDocument(cpValue, changed[archiveRoleKey(head.Checkpoint.Digest, "checkpoint")])
	if e != nil || checkpoint == nil || checkpoint.ID != head.RunID || (checkpoint.Status != "complete" && checkpoint.Status != "stopped") {
		return unavailable()
	}
	commits := view.Commits()
	if len(commits) != head.CommitCount {
		return unavailable()
	}
	claims := map[string]captureClaim{}
	expectations := []captureExpectation{}
	requests := []captureRequest{}
	events := []captureEvent{}
	previous := ""
	eventAt := 0
	for index, input := range commits {
		c, e := ReadRunHistoryEntry([]byte(historyInputText(input)))
		if e != nil || c.Sequence != index+1 || !archiveEqual(c.PreviousDigest, func() *string {
			if previous == "" {
				return nil
			}
			p := previous
			return &p
		}()) || c.EventStart != eventAt+1 {
			return unavailable()
		}
		previous = c.Digest
		eventAt += c.EventCount
		if c.Checkpoint != nil {
			v, e := value(*c.Checkpoint)
			if e != nil {
				return unavailable()
			}
			cp, e := checkpointDocument(v, changed[archiveRoleKey(c.Checkpoint.Digest, "checkpoint")])
			if e != nil || cp == nil || cp.ID != head.RunID {
				return unavailable()
			}
			if cp.Durable != nil {
				var book DurableBook
				if json.Unmarshal(v.Get("durable").JSON(), &book) != nil {
					return unavailable()
				}
				for _, claim := range book.Dispatches {
					old, ok := claims[claim.ID]
					if ok && !archiveEqual([]any{old.claim.Scope, old.claim.Kind, old.claim.Source, old.claim.Tool, old.claim.Request, old.claim.RequestDigest}, []any{claim.Scope, claim.Kind, claim.Source, claim.Tool, claim.Request, claim.RequestDigest}) {
						return unavailable()
					}
					first := c.Sequence
					if ok {
						first = old.first
					}
					claims[claim.ID] = captureClaim{claim, first}
				}
			}
		}
		for _, ref := range c.Observations {
			raw, e := get(ref, "observation")
			if e != nil {
				return unavailable()
			}
			o, e := ReadHistoryObservation(raw)
			if e != nil {
				return unavailable()
			}
			if o.Get("kind").Str() == "capture-request" {
				var identity HistoryCaptureIdentity
				json.Unmarshal(o.JSON(), &identity)
				identity.Kind = o.Get("captureKind").Str()
				expectations = append(expectations, captureExpectation{identity, c.Sequence})
			} else if o.Get("kind").Str() == "request" {
				ref := archiveRef(o.Get("request"))
				raw, e := get(ref, "request-manifest")
				if e != nil {
					return unavailable()
				}
				m, e := ReadHistoryChunkManifest(raw)
				if e != nil {
					return unavailable()
				}
				if _, e = payload(ref, "request"); e != nil {
					return unavailable()
				}
				requests = append(requests, captureRequest{liveHistoryScope(o.Get("scope")), o.Get("dispatchId").Str(), m.Digest, o.Get("source").Str(), o.Get("tool").Str(), c.Sequence})
			}
		}
		for _, ref := range c.Events {
			raw, e := get(ref, "event")
			if e != nil {
				return unavailable()
			}
			held, e := ReadStoredRunEvent(raw)
			if e != nil {
				return unavailable()
			}
			v, e := ParseJSON(raw)
			if e != nil {
				return unavailable()
			}
			events = append(events, captureEvent{held.Scope, held.Event, v.Get("event"), c.Sequence})
		}
	}
	last := commits[len(commits)-1]
	if previous != head.Root || eventAt != head.EventCount || last.ExecutionRevision != head.ExecutionRevision || !archiveEqual(last.Checkpoint, head.Checkpoint) {
		return unavailable()
	}
	rows := view.Metadata()
	if port != nil {
		rows = port.metadata
	}
	count := 0
	var metadataRoot *string
	if head.Metadata != nil {
		count = head.Metadata.Count
		metadataRoot = head.Metadata.Root
	}
	if len(rows) != count {
		return unavailable()
	}
	var priorMetadata *string
	for index, row := range rows {
		if row.Sequence != index+1 || !archiveEqual(row.PreviousDigest, priorMetadata) || row.CommitSequence < 1 || row.CommitSequence > len(commits) || commits[row.CommitSequence-1].ExecutionRevision != row.ExecutionRevision {
			return unavailable()
		}
		sum := row.Digest
		priorMetadata = &sum
	}
	if !archiveEqual(priorMetadata, metadataRoot) {
		return unavailable()
	}
	var attachment *HistoryMetadataEntry
	for _, input := range rows {
		row := input
		if port == nil {
			var e error
			row, e = ReadHistoryMetadataEntry([]byte(historyInputText(input)))
			if e != nil {
				return unavailable()
			}
		}
		if row.Record.Kind == "capture" {
			copy := row
			attachment = &copy
		}
	}
	if attachment == nil || attachment.ExecutionRevision != head.ExecutionRevision {
		return unavailable()
	}
	raw, e := get(*attachment.Record.Capture, "capture")
	if e != nil {
		return unavailable()
	}
	capture, e := ReadRunCapture(raw)
	if e != nil || capture.RunID != head.RunID || capture.ExecutionRevision != head.ExecutionRevision || !archiveEqual(capture.Checkpoint, head.Checkpoint) {
		return unavailable()
	}
	eligible := 0
	for _, v := range claims {
		if v.claim.Kind == "model" || v.claim.Kind == "agent" {
			eligible++
		}
	}
	if len(capture.Requests) != eligible || len(expectations) != eligible {
		return unavailable()
	}
	seen := map[string]bool{}
	for i, x := range expectations {
		a := x.identity
		held, ok := claims[a.DispatchID]
		expected := a
		expected.CaptureIndex = i
		requestCount := 0
		for _, r := range requests {
			if r.id == a.DispatchID && r.scope == a.Scope && r.digest == a.RequestDigest && r.source == a.Source && r.tool == a.Tool && r.sequence == x.sequence {
				requestCount++
			}
		}
		if !ok || seen[a.DispatchID] || held.first != x.sequence || held.claim.State != "result" || !held.claim.Read || !archiveEqual(capture.Requests[i], expected) || !archiveEqual([]any{a.Scope, a.Kind, a.Source, a.Tool, a.RequestDigest}, []any{held.claim.Scope, held.claim.Kind, held.claim.Source, held.claim.Tool, held.claim.RequestDigest}) || requestCount != 1 {
			return unavailable()
		}
		seen[a.DispatchID] = true
	}
	files := map[string]*Value{}
	for _, f := range capture.Files {
		v, e := payload(f.Manifest, "corpus")
		if e != nil {
			return unavailable()
		}
		files[f.Name] = v
	}
	var manifest CorpusManifest
	var portable contract.CorpusEntry
	if !snapshotObject(files["manifest.json"], []string{"id", "packageVersion", "policy", "files", "expected", "tolerance"}) || json.Unmarshal(files["manifest.json"].JSON(), &portable) != nil {
		return unavailable()
	}
	if json.Unmarshal(files["manifest.json"].JSON(), &manifest) != nil || validateCorpusManifest(manifest) != nil {
		return unavailable()
	}
	rawRequests, rawTools, rawEvents := files["requests.json"], files["tool-results.json"], files["events.json"]
	if !rawRequests.IsArray() || rawRequests.Len() != len(expectations) || !rawTools.IsArray() || !rawEvents.IsArray() {
		return unavailable()
	}
	capturedCP, e := checkpointDocument(files["checkpoint.json"], display)
	if e != nil || capturedCP == nil || !captureCheckpointMatches(cpValue, files["checkpoint.json"]) || original && !archiveEqual(cpValue, files["checkpoint.json"]) {
		return unavailable()
	}
	nativeTools := map[string]*Value{}
	for i, r := range rawRequests.Items() {
		if !snapshotObject(r, []string{"suffix", "request", "response"}) || !r.Get("suffix").IsString() || !r.Get("request").IsObject() || !snapshotObject(r.Get("response"), []string{"status", "headers", "body"}) || !snapshotCount(r.Get("response").Get("status"), 100) || r.Get("response").Get("status").Num() > 599 || !r.Get("response").Get("headers").IsObject() || !r.Get("response").Get("body").IsString() {
			return unavailable()
		}
		for _, header := range r.Get("response").Get("headers").o {
			if !header.val.IsString() {
				return unavailable()
			}
		}
		claim := claims[expectations[i].identity.DispatchID].claim
		request, e := ParseJSON([]byte(claim.Request))
		if e != nil {
			return unavailable()
		}
		if claim.Kind == "agent" {
			if r.Get("suffix").Str() != "agent.run" || !matches(request, r.Get("request")) || !validCaptureAgentBody(r.Get("response").Get("body").Str()) {
				return unavailable()
			}
			aliases, e := nativeCaptureBindings(request, r.Get("response").Get("headers"))
			if e != nil {
				return unavailable()
			}
			body, _ := readHistoryRequestJSON([]byte(r.Get("response").Get("body").Str()))
			for _, step := range body.Get("steps").Items() {
				if tool := step.Get("tool"); tool != nil {
					key := historyInputText([]any{expectations[i].identity.Scope, tool.Get("id").Str()})
					if nativeTools[key] != nil {
						return unavailable()
					}
					bound := tool.Clone()
					name := tool.Get("name").Str()
					if aliases == nil {
						if strings.HasPrefix(name, "desk_host_") || strings.HasPrefix(name, "desk_runtime_") {
							return unavailable()
						}
					} else {
						if !aliases.Has(name) {
							return unavailable()
						}
						bound.Set("name", aliases.Get(name))
					}
					nativeTools[key] = bound
				}
			}
		} else {
			if r.Get("response").Get("headers").Has(nativeBindingsHeader) {
				return unavailable()
			}
			q := r.Get("request")
			if !snapshotObject(q, []string{"body"}, "method", "headers") || !q.Get("body").IsString() || q.Has("method") && !oneOf(q.Get("method"), "GET", "POST") || q.Has("headers") && !q.Get("headers").IsObject() {
				return unavailable()
			}
			if q.Has("headers") {
				for _, m := range q.Get("headers").o {
					if !m.val.IsString() {
						return unavailable()
					}
				}
			}
			descriptor := ObjectOf("suffix", r.Get("suffix"), "body", q.Get("body"))
			if q.Has("method") {
				descriptor.Set("method", q.Get("method"))
			}
			if !matches(request, descriptor) {
				return unavailable()
			}
		}
	}
	rootEvents := Array()
	for _, r := range events {
		if r.scope.Execution == head.RunID && r.scope.Task == "" && r.scope.Role == "author" && !oneOf(r.value.Get("type"), "end", "recorded") && (capture.EventProjection != "transcript-v1" || r.value.Get("type").Str() != "execution_checkpoint") {
			rootEvents.Append(r.value)
		}
	}
	cleanEvents := []contract.RunEvent{}
	for _, v := range rawEvents.Items() {
		var event contract.RunEvent
		if json.Unmarshal(v.JSON(), &event) != nil {
			return unavailable()
		}
		cleanEvents = append(cleanEvents, event)
	}
	if !matches(rootEvents, rawEvents) {
		return unavailable()
	}
	expected, e := CorpusExpected(cleanEvents)
	if e != nil || historyInputText(expected) != CanonicalJSON(files["expected.json"]) {
		return unavailable()
	}
	pending := map[string]*Value{}
	tools := Array()
	for _, r := range events {
		event := r.value
		if event.Get("type").Str() == "tool_call" {
			if !historyID(event.Get("callId")) || !event.Get("args").IsObject() {
				return unavailable()
			}
			key := historyInputText([]any{r.scope, event.Get("callId").Str()})
			if pending[key] != nil {
				return unavailable()
			}
			if native := nativeTools[key]; native != nil {
				if native.Get("name").Str() != event.Get("name").Str() || !matches(native.Get("arguments"), event.Get("args")) {
					return unavailable()
				}
				delete(nativeTools, key)
			}
			pending[key] = event
		} else if event.Get("type").Str() == "tool_result" {
			if !historyID(event.Get("callId")) {
				return unavailable()
			}
			key := historyInputText([]any{r.scope, event.Get("callId").Str()})
			call := pending[key]
			if call == nil || call.Get("name").Str() != event.Get("name").Str() {
				return unavailable()
			}
			delete(pending, key)
			result := ObjectOf("content", Array(ObjectOf("type", String("text"), "text", event.Get("text"))), "isError", event.Get("isError"))
			if event.Has("structured") {
				result.Set("structuredContent", event.Get("structured"))
			}
			tools.Append(ObjectOf("name", event.Get("name"), "args", call.Get("args"), "result", result))
		}
	}
	if len(pending) != 0 || len(nativeTools) != 0 || !matches(tools, rawTools) {
		return unavailable()
	}
	return &CorpusEntry{Manifest: manifest, Requests: json.RawMessage(CanonicalJSON(rawRequests)), ToolResults: json.RawMessage(CanonicalJSON(rawTools)), Events: cleanEvents, Checkpoint: json.RawMessage(CanonicalJSON(files["checkpoint.json"])), Expected: expected}, nil
}

// ReadArchiveCapturedCorpus never weakens native readers: every required source member must remain exact.
func ReadArchiveCapturedCorpus(view *ImportedRunHistory) (*CorpusEntry, error) {
	if view == nil || reflect.ValueOf(view).Kind() == reflect.Pointer && reflect.ValueOf(view).IsNil() {
		return nil, &ReplayUnavailable{}
	}
	m := view.Manifest()
	get := func(ref HistoryObjectRef, kinds ...string) ([]byte, error) {
		if len(kinds) != 1 {
			return nil, &ReplayUnavailable{}
		}
		d, e := view.ReadDisplay(ref, kinds[0])
		if e != nil || d.Representation.Kind != "exact" {
			return nil, &ReplayUnavailable{}
		}
		if d.Document.IsString() {
			return base64.StdEncoding.Strict().DecodeString(d.Document.Str())
		}
		return []byte(CanonicalJSON(d.Document)), nil
	}
	metadata := []HistoryMetadataEntry{}
	for _, ref := range m.Metadata {
		raw, e := get(ref, "metadata")
		if e != nil {
			return nil, &ReplayUnavailable{}
		}
		row, e := ReadHistoryMetadataEntry(raw)
		if e != nil {
			return nil, &ReplayUnavailable{}
		}
		metadata = append(metadata, row)
	}
	return ReadCapturedCorpus(capturePort{m.Source, m.Commits, metadata, get})
}

// CorpusReplayView carries read-only display material; Entry.Checkpoint is always an explicit wrapper.
type CorpusReplayView struct {
	Checkpoint CorpusCheckpointView `json:"checkpoint"`
	Entry      CorpusEntry          `json:"entry"`
}

func corpusReplayView(entry *CorpusEntry, source HistoryObjectRef) (*CorpusReplayView, error) {
	document, e := ParseJSON(entry.Checkpoint)
	if e != nil {
		return nil, &ReplayUnavailable{}
	}
	checkpoint, e := newCorpusCheckpointView(source, document)
	if e != nil {
		return nil, e
	}
	raw, e := canonicalBytes(checkpoint)
	if e != nil {
		return nil, &ReplayUnavailable{}
	}
	copy := *entry
	copy.Checkpoint = json.RawMessage(raw)
	return &CorpusReplayView{checkpoint, copy}, nil
}

// ReadCapturedCorpusView validates retained capture relationships without issuing resume authority.
func ReadCapturedCorpusView(view CaptureReadView) (*CorpusReplayView, error) {
	entry, e := readCapturedCorpus(view, false, true, nil)
	if e != nil {
		return nil, e
	}
	head := view.Head()
	if head.Checkpoint == nil {
		return nil, &ReplayUnavailable{}
	}
	return corpusReplayView(entry, *head.Checkpoint)
}

// ReadArchiveCapturedCorpusView consumes only validated role-specific imported display members.
func ReadArchiveCapturedCorpusView(view *ImportedRunHistory) (*CorpusReplayView, error) {
	if view == nil {
		return nil, &ReplayUnavailable{}
	}
	m := view.Manifest()
	read := func(ref HistoryObjectRef, kind string) ([]byte, bool, error) {
		d, e := view.ReadDisplay(ref, kind)
		if e != nil {
			return nil, false, &ReplayUnavailable{}
		}
		exact := d.Representation.Kind == "exact"
		if strings.HasSuffix(kind, "-chunk") {
			if exact {
				raw, e := base64.StdEncoding.Strict().DecodeString(d.Document.Str())
				return raw, true, e
			}
			return []byte(d.Document.Str()), false, nil
		}
		return []byte(CanonicalJSON(d.Document)), exact, nil
	}
	metadata := []HistoryMetadataEntry{}
	for _, ref := range m.Metadata {
		raw, _, e := read(ref, "metadata")
		if e != nil {
			return nil, &ReplayUnavailable{}
		}
		var row HistoryMetadataEntry
		if json.Unmarshal(raw, &row) != nil {
			return nil, &ReplayUnavailable{}
		}
		metadata = append(metadata, row)
	}
	native := capturePort{m.Source, m.Commits, metadata, func(ref HistoryObjectRef, kinds ...string) ([]byte, error) {
		if len(kinds) != 1 {
			return nil, &ReplayUnavailable{}
		}
		raw, _, e := read(ref, kinds[0])
		return raw, e
	}}
	entry, e := readCapturedCorpus(native, false, true, &captureDisplayPort{read, metadata})
	if e != nil || m.Source.Checkpoint == nil {
		return nil, &ReplayUnavailable{}
	}
	return corpusReplayView(entry, *m.Source.Checkpoint)
}
