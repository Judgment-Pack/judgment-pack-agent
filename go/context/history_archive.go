package runcontext

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

type ArchiveRepresentation struct {
	Kind       string            `json:"kind"`
	Source     *HistoryObjectRef `json:"source,omitempty"`
	Redactions []string          `json:"redactions,omitempty"`
}
type ArchiveMapping struct {
	Source         HistoryObjectRef      `json:"source"`
	Exported       HistoryObjectRef      `json:"exported"`
	Kind           string                `json:"kind"`
	Representation ArchiveRepresentation `json:"representation"`
}
type ArchiveMember struct {
	HistoryObjectRef
	Source         HistoryObjectRef      `json:"source"`
	Kind           string                `json:"kind"`
	Representation ArchiveRepresentation `json:"representation"`
}
type RunArchiveManifest struct {
	Version  int                `json:"version"`
	RunID    string             `json:"runId"`
	Source   RunHistoryHead     `json:"source"`
	Coverage string             `json:"coverage"`
	Commits  []RunHistoryEntry  `json:"commits"`
	Metadata []HistoryObjectRef `json:"metadata"`
	Mappings []ArchiveMapping   `json:"mappings"`
	Members  []ArchiveMember    `json:"members"`
	Report   HistoryObjectRef   `json:"report"`
	Digest   string             `json:"digest"`
}
type archiveObject struct {
	Digest string `json:"digest"`
	Bytes  string `json:"bytes"`
}
type runArchive struct {
	Version  int                `json:"version"`
	Manifest RunArchiveManifest `json:"manifest"`
	Objects  []archiveObject    `json:"objects"`
}
type archiveView struct {
	Version       int              `json:"version"`
	SourceKind    string           `json:"sourceKind"`
	SourceVersion int              `json:"sourceVersion"`
	Source        HistoryObjectRef `json:"source"`
	Redactions    []string         `json:"redactions"`
	Document      json.RawMessage  `json:"document"`
}
type ArchiveDisplay struct {
	Document       *Value                `json:"document"`
	Representation ArchiveRepresentation `json:"representation"`
	Source         HistoryObjectRef      `json:"source"`
	Exported       HistoryObjectRef      `json:"exported"`
}
type ArchiveExportOptions struct {
	Members         PolicyMembers
	RequireComplete bool
}
type ArchiveExport struct {
	Bytes    []byte
	Digest   string
	Manifest RunArchiveManifest
}
type HistoryIncomplete struct{}

func (*HistoryIncomplete) Error() string {
	return "HistoryIncomplete: complete retained history is required."
}

var archiveProtected = regexp.MustCompile(`^(id|.*Id|scope|execution|task|role|source|sourceView|tool|name|boundary|revision|.*Revision|digest|.*Digest|sha256|requestKey|type|kind|version|schema|retainUntil|recordedAt|previous|previousDigest|eventSequence|sequence|commitSequence)$`)
var archiveMarker = regexp.MustCompile(`\[redacted: [a-z][a-z0-9-]{0,63}\]`)
var archiveKinds = []string{"checkpoint", "event", "observation", "report", "metadata", "candidate", "capture", "evidence", "source-manifest", "source-chunk", "request-manifest", "request-chunk", "chat-manifest", "chat-chunk", "corpus-manifest", "corpus-chunk"}

func archiveKind(kind string) bool {
	for _, k := range archiveKinds {
		if k == kind {
			return true
		}
	}
	return false
}
func archiveEqual(a, b any) bool {
	encode := func(v any) string {
		if x, ok := v.(*Value); ok {
			return CanonicalJSON(x)
		}
		return historyInputText(v)
	}
	return encode(a) == encode(b)
}
func archiveDelete(v *Value, key string) {
	for i, m := range v.o {
		if m.key == key {
			v.o = append(v.o[:i], v.o[i+1:]...)
			return
		}
	}
}
func archiveRef(v *Value) HistoryObjectRef {
	var out HistoryObjectRef
	json.Unmarshal(v.JSON(), &out)
	return out
}
func archiveVersion(kind string, v *Value) int {
	if kind != "checkpoint" && kind != "event" && kind != "report" {
		return 1
	}
	if snapshotCount(v.Get("version"), 0) {
		return int(v.Get("version").Num())
	}
	return 1
}

// Task identity locks apply only to typed native checkpoint/report/event paths.
func archiveTaskIdentity(kind string, value *Value) *Value {
	out := ObjectOf()
	dependency := func(step *Value, path string) {
		if step.IsObject() {
			out.Set(path+"/dependsOn", step.Get("dependsOn").Clone())
		}
	}
	steps := func(rows *Value, path string) {
		if rows.IsArray() {
			for i, step := range rows.Items() {
				dependency(step, path+"/"+strconv.Itoa(i))
			}
		}
	}
	var checkpoint func(*Value, string)
	checkpoint = func(v *Value, path string) {
		if !v.IsObject() {
			return
		}
		steps(v.Get("plan"), path+"/plan")
		state := v.Get("planState")
		if state.IsObject() {
			out.Set(path+"/planState/taskSchedule", state.Get("taskSchedule").Clone())
			out.Set(path+"/planState/taskEnvelope", state.Get("taskEnvelope").Clone())
			checkpoint(state.Get("pending").Get("candidate"), path+"/planState/pending/candidate")
			for i, row := range state.Get("history").Items() {
				for j, change := range row.Get("diff").Items() {
					for _, side := range []string{"before", "after"} {
						dependency(change.Get(side), path+"/planState/history/"+strconv.Itoa(i)+"/diff/"+strconv.Itoa(j)+"/"+side)
					}
				}
			}
		}
		for i, child := range v.Get("durable").Get("children").Items() {
			checkpoint(child.Get("checkpoint"), path+"/durable/children/"+strconv.Itoa(i)+"/checkpoint")
		}
	}
	if kind == "checkpoint" || kind == "report" {
		checkpoint(value, "")
	} else if kind == "event" {
		checkpoint(value.Get("event").Get("checkpoint"), "/event/checkpoint")
	}
	return out
}
func archiveTaskEqual(kind string, a, b *Value) error {
	if CanonicalJSON(archiveTaskIdentity(kind, a)) != CanonicalJSON(archiveTaskIdentity(kind, b)) {
		return &HistoryInputInvalid{}
	}
	return nil
}
func archiveTaskRedactions(kind string, value *Value, paths []string) bool {
	for _, path := range paths {
		for _, field := range archiveTaskIdentity(kind, value).o {
			if path == field.key || strings.HasPrefix(path, field.key+"/") {
				return false
			}
		}
	}
	return true
}

func archiveDiff(a, b *Value, path string, locked bool) ([]string, error) {
	if CanonicalJSON(a) == CanonicalJSON(b) {
		return []string{}, nil
	}
	if a.IsString() && b.IsString() && !locked && archiveMarker.MatchString(b.Str()) {
		return []string{path}, nil
	}
	out := []string{}
	if a.IsArray() && b.IsArray() && a.Len() == b.Len() {
		for i, x := range a.Items() {
			p, e := archiveDiff(x, b.Items()[i], path+"/"+strconv.Itoa(i), locked)
			if e != nil {
				return nil, e
			}
			out = append(out, p...)
		}
		return out, nil
	}
	ak, bk := a.Keys(), b.Keys()
	sort.Strings(ak)
	sort.Strings(bk)
	if a.IsObject() && b.IsObject() && archiveEqual(ak, bk) {
		for _, key := range a.Keys() {
			p, e := archiveDiff(a.Get(key), b.Get(key), path+"/"+strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1"), locked || archiveProtected.MatchString(key))
			if e != nil {
				return nil, e
			}
			out = append(out, p...)
		}
		return out, nil
	}
	return nil, &HistoryInputInvalid{}
}
func archiveDeclarations(v *Value, paths []string, chunk bool) bool {
	if chunk {
		return len(paths) == 1 && paths[0] == "" && v.IsString()
	}
	if len(paths) < 1 || len(paths) > 10000 {
		return false
	}
	previous := ""
	for i, path := range paths {
		if len(path) > 2000 || i > 0 && path <= previous || path != "" && !strings.HasPrefix(path, "/") {
			return false
		}
		previous = path
		value := v
		for _, bit := range strings.Split(path, "/")[1:] {
			for i := 0; i < len(bit); i++ {
				if bit[i] == '~' {
					if i+1 >= len(bit) || (bit[i+1] != '0' && bit[i+1] != '1') {
						return false
					}
					i++
				}
			}
			key := strings.ReplaceAll(strings.ReplaceAll(bit, "~1", "/"), "~0", "~")
			if archiveProtected.MatchString(key) {
				return false
			}
			if value.IsArray() {
				index, err := strconv.Atoi(key)
				if err != nil || index < 0 || index >= value.Len() {
					return false
				}
				value = value.Items()[index]
			} else if value.IsObject() && value.Has(key) {
				value = value.Get(key)
			} else {
				return false
			}
		}
		if !value.IsString() || !archiveMarker.MatchString(value.Str()) {
			return false
		}
	}
	return true
}
func archiveValidate(kind string, v *Value, derived bool) error {
	raw := []byte(CanonicalJSON(v))
	switch kind {
	case "checkpoint":
		if derived {
			return contract.CheckRecord("checkpoint", raw)
		}
		cp, e := ReadExecution(v)
		if e != nil || cp == nil {
			return &HistoryInputInvalid{}
		}
	case "event":
		if !snapshotObject(v, []string{"version", "scope", "event"}) || v.Get("version").Num() != 1 || !historyScopeValue(v.Get("scope")) {
			return &HistoryInputInvalid{}
		}
		if derived {
			return contract.CheckRecord("event", []byte(CanonicalJSON(v.Get("event"))))
		}
		_, e := ReadStoredRunEvent(raw)
		return e
	case "report":
		if derived {
			_, e := readRunReportDisplay(v)
			return e
		}
		_, e := ReadRunReport(v)
		return e
	case "metadata":
		if derived {
			body := v.Clone()
			archiveDelete(body, "digest")
			v = v.Clone()
			v.Set("digest", String(HistoryBytesDigest([]byte(CanonicalJSON(body)))))
			raw = []byte(CanonicalJSON(v))
		}
		_, e := ReadHistoryMetadataEntry(raw)
		return e
	case "observation":
		_, e := ReadHistoryObservation(raw)
		return e
	case "candidate":
		_, e := ReadCorpusCandidate(raw)
		return e
	case "capture":
		_, e := ReadRunCapture(raw)
		return e
	default:
		if strings.HasSuffix(kind, "-manifest") {
			_, e := readHistoryChunkManifest(raw)
			return e
		}
		if strings.HasSuffix(kind, "-chunk") {
			if !v.IsString() {
				return &HistoryInputInvalid{}
			}
		} else if kind != "evidence" {
			return &HistoryInputInvalid{}
		}
	}
	return nil
}
func archiveUnwrap(m ArchiveMapping, raw []byte) (*Value, error) {
	if m.Representation.Kind == "exact" {
		if strings.HasSuffix(m.Kind, "-chunk") {
			return String(base64.StdEncoding.EncodeToString(raw)), nil
		}
		return readHistoryJSON(raw, HistoryObjectBytes, true, true)
	}
	v, e := readHistoryJSON(raw, HistoryObjectBytes, true, true)
	if e != nil || !snapshotObject(v, []string{"version", "sourceKind", "sourceVersion", "source", "redactions", "document"}) || v.Get("version").Num() != 1 || v.Get("sourceKind").Str() != m.Kind || !snapshotCount(v.Get("sourceVersion"), 0) || !archiveEqual(archiveRef(v.Get("source")), m.Source) || !archiveEqual(v.Get("redactions"), m.Representation.Redactions) || int(v.Get("sourceVersion").Num()) != archiveVersion(m.Kind, v.Get("document")) || !archiveDeclarations(v.Get("document"), m.Representation.Redactions, strings.HasSuffix(m.Kind, "-chunk")) || !archiveTaskRedactions(m.Kind, v.Get("document"), m.Representation.Redactions) {
		return nil, &HistoryInputInvalid{}
	}
	return v.Get("document"), nil
}

// ImportedRunHistory exposes detached display facts, never storage, restore or resume authority.
type ImportedRunHistory struct {
	manifest  RunArchiveManifest
	objects   map[string][]byte
	mappings  map[string]ArchiveMapping
	canonical []byte
}

func (h *ImportedRunHistory) Head() RunHistoryHead         { return historyClone(h.manifest.Source) }
func (h *ImportedRunHistory) Commits() []RunHistoryEntry   { return historyClone(h.manifest.Commits) }
func (h *ImportedRunHistory) Manifest() RunArchiveManifest { return historyClone(h.manifest) }
func (h *ImportedRunHistory) CanonicalBytes() []byte       { return bytes.Clone(h.canonical) }
func archiveRoleKey(digest, kind string) string            { return digest + "|" + kind }
func (h *ImportedRunHistory) ReadDisplay(ref HistoryObjectRef, kinds ...string) (ArchiveDisplay, error) {
	if h == nil {
		return ArchiveDisplay{}, &HistoryInputInvalid{}
	}
	if len(kinds) > 1 {
		return ArchiveDisplay{}, &HistoryInputInvalid{}
	}
	key := ""
	if len(kinds) == 1 {
		key = archiveRoleKey(ref.Digest, kinds[0])
	} else {
		for k, row := range h.mappings {
			if row.Source.Digest == ref.Digest {
				if key != "" {
					return ArchiveDisplay{}, &HistoryInputInvalid{}
				}
				key = k
			}
		}
	}
	m, ok := h.mappings[key]
	if !ok || m.Source != ref {
		return ArchiveDisplay{}, &HistoryInputInvalid{}
	}
	v, e := archiveUnwrap(m, h.objects[m.Exported.Digest])
	if e != nil {
		return ArchiveDisplay{}, e
	}
	return ArchiveDisplay{v.Clone(), historyClone(m.Representation), ref, m.Exported}, nil
}

// HistoryQueryView cannot confer execution authority. Implementations provide separate validated display reads.
type HistoryQueryView interface {
	Head() RunHistoryHead
	Commits() []RunHistoryEntry
	historyEventDisplay(HistoryObjectRef) (DurableScope, *Value, *ArchiveRepresentation, error)
	historyObservationDisplay(HistoryObjectRef) (*Value, error)
}

func (h *RetainedRunHistory) historyEventDisplay(ref HistoryObjectRef) (DurableScope, *Value, *ArchiveRepresentation, error) {
	raw, e := h.ReadObject(ref)
	if e != nil {
		return DurableScope{}, nil, nil, e
	}
	envelope, e := ReadStoredRunEvent(raw)
	if e != nil {
		return DurableScope{}, nil, nil, e
	}
	v, e := ParseJSON(raw)
	return envelope.Scope, v.Get("event"), nil, e
}
func (h *RetainedRunHistory) historyObservationDisplay(ref HistoryObjectRef) (*Value, error) {
	raw, e := h.ReadObject(ref)
	if e != nil {
		return nil, e
	}
	return ReadHistoryObservation(raw)
}
func (h *ImportedRunHistory) historyEventDisplay(ref HistoryObjectRef) (DurableScope, *Value, *ArchiveRepresentation, error) {
	d, e := h.ReadDisplay(ref, "event")
	if e != nil {
		return DurableScope{}, nil, nil, &HistoryInputInvalid{}
	}
	var scope DurableScope
	if json.Unmarshal(d.Document.Get("scope").JSON(), &scope) != nil {
		return DurableScope{}, nil, nil, &HistoryInputInvalid{}
	}
	return scope, d.Document.Get("event"), &d.Representation, nil
}
func (h *ImportedRunHistory) historyObservationDisplay(ref HistoryObjectRef) (*Value, error) {
	d, e := h.ReadDisplay(ref, "observation")
	if e != nil {
		return nil, &HistoryInputInvalid{}
	}
	return ReadHistoryObservation([]byte(CanonicalJSON(d.Document)))
}

// ExportRunArchive privately validates originals and uses a detached whole-record output guard.
func ExportRunArchive(ctx context.Context, store HistoryRunStore, runID string, options ArchiveExportOptions) (result ArchiveExport, err error) {
	defer func() {
		if recover() != nil {
			result = ArchiveExport{}
			err = &HistoryInputInvalid{}
		}
	}()
	invalid := func() (ArchiveExport, error) { return ArchiveExport{}, &HistoryInputInvalid{} }
	view, err := ReadRetainedRunHistory(ctx, store, runID)
	if err != nil {
		return result, err
	}
	head := view.Head()
	if options.RequireComplete && head.Coverage != "complete" {
		return result, &HistoryIncomplete{}
	}
	members := PolicyMembers{}
	for k, v := range options.Members {
		members[k] = v
	}
	members["outputGuard"] = PolicyMember{Value: "redact"}
	guard, err := NewOutputGuard(members, func() map[string]int64 { return map[string]int64{} })
	if err != nil {
		return invalid()
	}
	clean := func(v *Value) (*Value, error) {
		raw, e := guard.JSON(v.JSON(), "tool argument", false)
		if e != nil {
			return nil, e
		}
		return ParseJSON(raw)
	}
	type source struct {
		ref      HistoryObjectRef
		kind     string
		raw      []byte
		document *Value
		clean    *Value
	}
	sources := map[string]*source{}
	add := func(ref HistoryObjectRef, kind string, raw []byte, document *Value) (*source, error) {
		if !validHistoryRef(ref) || len(raw) != ref.Bytes || HistoryBytesDigest(raw) != ref.Digest {
			return nil, &HistoryInputInvalid{}
		}
		if document == nil {
			if strings.HasSuffix(kind, "-chunk") {
				document = String(base64.StdEncoding.EncodeToString(raw))
			} else {
				var e error
				document, e = readHistoryJSON(raw, HistoryObjectBytes, true, true)
				if e != nil {
					return nil, e
				}
			}
		}
		if e := archiveValidate(kind, document, false); e != nil {
			return nil, e
		}
		if old := sources[archiveRoleKey(ref.Digest, kind)]; old != nil {
			if old.ref != ref || CanonicalJSON(old.document) != CanonicalJSON(document) {
				return nil, &HistoryInputInvalid{}
			}
			return old, nil
		}
		row := &source{ref: ref, kind: kind, raw: bytes.Clone(raw), document: document}
		sources[archiveRoleKey(ref.Digest, kind)] = row
		return row, nil
	}
	addRef := func(ref HistoryObjectRef, kind string) error {
		raw, e := view.ReadObject(ref)
		if e != nil {
			return e
		}
		_, e = add(ref, kind, raw, nil)
		return e
	}
	payload := func(ref HistoryObjectRef, prefix string) error {
		raw, e := view.ReadObject(ref)
		if e != nil {
			return e
		}
		manifest, e := readHistoryChunkManifest(raw)
		if e != nil {
			return e
		}
		if _, e = add(ref, prefix+"-manifest", raw, nil); e != nil {
			return e
		}
		parts := [][]byte{}
		full := []byte{}
		for _, part := range manifest.Parts {
			raw, e = view.ReadObject(part)
			if e != nil {
				return e
			}
			parts = append(parts, raw)
			full = append(full, raw...)
		}
		if HistoryBytesDigest(full) != manifest.Digest {
			return &HistoryInputInvalid{}
		}
		var document *Value
		switch prefix {
		case "request":
			document, e = readHistoryRequestJSON(full)
		case "source":
			document, e = readHistorySourceJSON(full)
		default:
			document, e = readHistoryJSON(full, HistoryTotalBytes, true, true)
		}
		if e != nil {
			return e
		}
		sanitized, e := clean(document)
		if e != nil {
			return e
		}
		changed := CanonicalJSON(document) != CanonicalJSON(sanitized)
		if !changed {
			for i, part := range manifest.Parts {
				row, e := add(part, prefix+"-chunk", parts[i], nil)
				if e != nil {
					return e
				}
				row.clean = row.document.Clone()
			}
			return nil
		}
		if _, e = archiveDiff(document, sanitized, "", false); e != nil {
			return e
		}
		rest := CanonicalJSON(sanitized)
		for i, part := range manifest.Parts {
			low, high := 0, len(rest)
			for low < high {
				mid := (low + high + 1) / 2
				if len(CanonicalJSON(String(rest[:mid]))) <= HistoryObjectBytes-1024 {
					low = mid
				} else {
					high = mid - 1
				}
			}
			end := low
			for end > 0 && !utf8.ValidString(rest[:end]) {
				end--
			}
			segment := String(rest[:end])
			rest = rest[end:]
			row, e := add(part, prefix+"-chunk", parts[i], nil)
			if e != nil {
				return e
			}
			if row.clean != nil && CanonicalJSON(row.clean) != CanonicalJSON(segment) {
				return &HistoryInputInvalid{}
			}
			row.clean = segment
		}
		if rest != "" {
			return &HistoryInputInvalid{}
		}
		return nil
	}
	for _, row := range view.Commits() {
		if row.Checkpoint != nil {
			if e := addRef(*row.Checkpoint, "checkpoint"); e != nil {
				return result, e
			}
		}
		if row.Context != nil {
			if e := payload(row.Context.Manifest, "chat"); e != nil {
				return result, e
			}
		}
		for _, ref := range row.Events {
			if e := addRef(ref, "event"); e != nil {
				return result, e
			}
		}
		for _, ref := range row.Observations {
			if e := addRef(ref, "observation"); e != nil {
				return result, e
			}
			o := sources[archiveRoleKey(ref.Digest, "observation")].document
			switch o.Get("kind").Str() {
			case "original":
				err = payload(archiveRef(o.Get("value")), "source")
			case "compaction":
				err = payload(archiveRef(o.Get("replacement")), "source")
			case "request":
				err = payload(archiveRef(o.Get("request")), "request")
			case "capture-request":
				if o.Has("nativeBindings") {
					err = addRef(archiveRef(o.Get("nativeBindings")), "evidence")
				}
			case "relation":
				err = addRef(archiveRef(o.Get("evidence")), "evidence")
			}
			if err != nil {
				return result, err
			}
		}
	}
	metadata := []HistoryObjectRef{}
	for _, row := range view.Metadata() {
		raw, e := historyJSONBytes(row)
		if e != nil {
			return result, e
		}
		ref := HistoryObjectRef{HistoryBytesDigest(raw), len(raw)}
		if _, e = add(ref, "metadata", raw, nil); e != nil {
			return result, e
		}
		metadata = append(metadata, ref)
		if row.Record.Kind == "feedback" && row.Record.Candidate != nil {
			if e = addRef(*row.Record.Candidate, "candidate"); e != nil {
				return result, e
			}
		}
		if row.Record.Kind == "capture" {
			if e = addRef(*row.Record.Capture, "capture"); e != nil {
				return result, e
			}
			capture, e := ReadRunCapture(sources[archiveRoleKey(row.Record.Capture.Digest, "capture")].raw)
			if e != nil {
				return result, e
			}
			for _, file := range capture.Files {
				if e = payload(file.Manifest, "corpus"); e != nil {
					return result, e
				}
			}
		}
	}
	if head.Checkpoint == nil {
		return invalid()
	}
	cp, e := ReadExecution(sources[archiveRoleKey(head.Checkpoint.Digest, "checkpoint")].document)
	if e != nil || cp == nil {
		return invalid()
	}
	report, e := ProjectHistoryRunReport(view)
	if e != nil {
		return result, e
	}
	reportRaw := []byte(CanonicalJSON(report))
	reportRef := HistoryObjectRef{HistoryBytesDigest(reportRaw), len(reportRaw)}
	if _, e = add(reportRef, "report", reportRaw, report); e != nil {
		return result, e
	}
	headers := MustParseJSON(historyInputText(map[string]any{"head": head, "commits": view.Commits()}))
	sanitized, e := clean(headers)
	if e != nil || CanonicalJSON(headers) != CanonicalJSON(sanitized) {
		return invalid()
	}
	keys := []string{}
	for key := range sources {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	mappings := []ArchiveMapping{}
	archiveMembers := []ArchiveMember{}
	objects := map[string][]byte{}
	for _, key := range keys {
		row := sources[key]
		sanitized := row.clean
		if sanitized == nil {
			sanitized, e = clean(row.document)
			if e != nil {
				return result, e
			}
		}
		if e = archiveTaskEqual(row.kind, row.document, sanitized); e != nil {
			return result, e
		}
		redactions := []string{}
		if strings.HasSuffix(row.kind, "-chunk") {
			if CanonicalJSON(row.document) != CanonicalJSON(sanitized) {
				redactions = []string{""}
			}
		} else {
			redactions, e = archiveDiff(row.document, sanitized, "", false)
			if e != nil {
				return result, e
			}
		}
		sort.Strings(redactions)
		rep := ArchiveRepresentation{Kind: "exact"}
		raw := row.raw
		if len(redactions) > 0 {
			ref := row.ref
			rep = ArchiveRepresentation{Kind: "redacted-view", Source: &ref, Redactions: redactions}
			raw, e = historyJSONBytes(archiveView{1, row.kind, archiveVersion(row.kind, row.document), row.ref, redactions, json.RawMessage(CanonicalJSON(sanitized))})
			if e != nil {
				return result, e
			}
		}
		if len(raw) > HistoryObjectBytes {
			return invalid()
		}
		exported := HistoryObjectRef{HistoryBytesDigest(raw), len(raw)}
		if old := objects[exported.Digest]; old != nil && !bytes.Equal(old, raw) {
			return invalid()
		}
		objects[exported.Digest] = raw
		mappings = append(mappings, ArchiveMapping{row.ref, exported, row.kind, rep})
		archiveMembers = append(archiveMembers, ArchiveMember{exported, row.ref, row.kind, rep})
	}
	sort.Slice(archiveMembers, func(i, j int) bool {
		return archiveRoleKey(archiveMembers[i].Source.Digest, archiveMembers[i].Kind) < archiveRoleKey(archiveMembers[j].Source.Digest, archiveMembers[j].Kind)
	})
	manifest := RunArchiveManifest{Version: 1, RunID: runID, Source: head, Coverage: head.Coverage, Commits: view.Commits(), Metadata: metadata, Mappings: mappings, Members: archiveMembers, Report: reportRef}
	body := MustParseJSON(historyInputText(manifest))
	archiveDelete(body, "digest")
	manifest.Digest = HistoryBytesDigest([]byte(CanonicalJSON(body)))
	archive := runArchive{Version: 1, Manifest: manifest, Objects: []archiveObject{}}
	blobKeys := []string{}
	for digest := range objects {
		blobKeys = append(blobKeys, digest)
	}
	sort.Strings(blobKeys)
	for _, digest := range blobKeys {
		archive.Objects = append(archive.Objects, archiveObject{digest, base64.StdEncoding.EncodeToString(objects[digest])})
	}
	raw, e := historyJSONBytes(archive)
	if e != nil || len(raw) > HistoryTotalBytes {
		return invalid()
	}
	if _, e = ReadRunArchive(raw, ""); e != nil {
		return result, e
	}
	final, e := store.ReadHistoryHead(ctx, runID)
	if e != nil {
		return result, e
	}
	if final == nil || !archiveEqual(final, head) {
		return result, &HistoryChanged{}
	}
	return ArchiveExport{bytes.Clone(raw), HistoryBytesDigest(raw), historyClone(manifest)}, nil
}

// ReadRunArchive verifies canonical bytes, a caller pin, every member and the complete reference closure before exposing a view.
// Self-consistent unsigned digests establish integrity, not host authenticity.
func ReadRunArchive(raw []byte, expectedDigest string) (result *ImportedRunHistory, err error) {
	defer func() {
		if recover() != nil {
			result = nil
			err = &HistoryInputInvalid{}
		}
	}()
	invalid := func() (*ImportedRunHistory, error) { return nil, &HistoryInputInvalid{} }
	if len(raw) < 1 || len(raw) > HistoryTotalBytes || expectedDigest != "" && (!historySHA256.MatchString(expectedDigest) || HistoryBytesDigest(raw) != expectedDigest) {
		return invalid()
	}
	v, e := readHistoryJSON(raw, HistoryTotalBytes, true, true)
	if e != nil || !snapshotObject(v, []string{"version", "manifest", "objects"}) || v.Get("version").Num() != 1 {
		return invalid()
	}
	mv := v.Get("manifest")
	if !snapshotObject(mv, []string{"version", "runId", "source", "coverage", "commits", "metadata", "mappings", "members", "report", "digest"}) || mv.Get("version").Num() != 1 {
		return invalid()
	}
	var archive runArchive
	if json.Unmarshal(raw, &archive) != nil {
		return invalid()
	}
	m := archive.Manifest
	body := mv.Clone()
	archiveDelete(body, "digest")
	if !historySHA256.MatchString(m.Digest) || HistoryBytesDigest([]byte(CanonicalJSON(body))) != m.Digest || !validStorageID(m.RunID) {
		return invalid()
	}
	head, e := ReadRunHistoryHead([]byte(CanonicalJSON(mv.Get("source"))))
	if e != nil || head.RunID != m.RunID || m.Coverage != head.Coverage || !mv.Get("commits").IsArray() || len(m.Commits) != head.CommitCount || !mv.Get("metadata").IsArray() || !mv.Get("mappings").IsArray() || len(m.Mappings) < 1 || len(m.Mappings) > 50000 || !mv.Get("members").IsArray() || len(m.Members) != len(m.Mappings) || !v.Get("objects").IsArray() || len(archive.Objects) < 1 || len(archive.Objects) > len(m.Members) || !validHistoryRef(m.Report) {
		return invalid()
	}
	count := 0
	if head.Metadata != nil {
		count = head.Metadata.Count
	}
	if len(m.Metadata) != count {
		return invalid()
	}
	out := &ImportedRunHistory{manifest: m, objects: map[string][]byte{}, mappings: map[string]ArchiveMapping{}, canonical: bytes.Clone(raw)}
	previous := ""
	total := 0
	for i, row := range archive.Objects {
		rv := v.Get("objects").Items()[i]
		if !snapshotObject(rv, []string{"digest", "bytes"}) || !rv.Get("bytes").IsString() || row.Digest <= previous || !historySHA256.MatchString(row.Digest) || len(row.Bytes) > ((HistoryObjectBytes+2)/3)*4 {
			return invalid()
		}
		value, e := base64.StdEncoding.Strict().DecodeString(row.Bytes)
		if e != nil || len(value) < 1 || len(value) > HistoryObjectBytes || base64.StdEncoding.EncodeToString(value) != row.Bytes || HistoryBytesDigest(value) != row.Digest || total+len(value) > HistoryTotalBytes {
			return invalid()
		}
		out.objects[row.Digest] = value
		previous = row.Digest
		total += len(value)
	}
	repValid := func(value *Value, rep ArchiveRepresentation, source *HistoryObjectRef) bool {
		if rep.Kind == "exact" {
			return snapshotObject(value, []string{"kind"})
		}
		return rep.Kind == "redacted-view" && snapshotObject(value, []string{"kind", "source", "redactions"}) && rep.Source != nil && validHistoryRef(*rep.Source) && (source == nil || *source == *rep.Source) && value.Get("redactions").IsArray()
	}
	sourceSizes := map[string]int{}
	previous = ""
	for i, row := range m.Mappings {
		rv := mv.Get("mappings").Items()[i]
		key := archiveRoleKey(row.Source.Digest, row.Kind)
		priorSize, known := sourceSizes[row.Source.Digest]
		original, present := out.objects[row.Source.Digest]
		if !snapshotObject(rv, []string{"source", "exported", "kind", "representation"}) || !historyRefValue(rv.Get("source")) || !historyRefValue(rv.Get("exported")) || key <= previous || !archiveKind(row.Kind) || !repValid(rv.Get("representation"), row.Representation, &row.Source) || row.Representation.Kind == "exact" && row.Source != row.Exported || known && priorSize != row.Source.Bytes || present && len(original) != row.Source.Bytes {
			return invalid()
		}
		sourceSizes[row.Source.Digest] = row.Source.Bytes
		out.mappings[key] = row
		previous = key
	}
	previous = ""
	declared := map[string]bool{}
	for i, row := range m.Members {
		rv := mv.Get("members").Items()[i]
		key := archiveRoleKey(row.Source.Digest, row.Kind)
		mapping, ok := out.mappings[key]
		if !snapshotObject(rv, []string{"digest", "bytes", "source", "kind", "representation"}) || !validHistoryRef(row.HistoryObjectRef) || !historyRefValue(rv.Get("source")) || key <= previous || !archiveKind(row.Kind) || !repValid(rv.Get("representation"), row.Representation, &row.Source) || len(out.objects[row.Digest]) != row.Bytes || !ok || mapping.Source != row.Source || mapping.Exported != row.HistoryObjectRef || !archiveEqual(mapping.Representation, row.Representation) {
			return invalid()
		}
		previous = key
		declared[row.Digest] = true
	}
	if len(declared) != len(out.objects) {
		return invalid()
	}
	used := map[string]bool{}
	documents := map[string]*Value{}
	payloads := map[string]*Value{}
	logical := map[string]*Value{}
	get := func(ref HistoryObjectRef, kind string) (*Value, error) {
		key := archiveRoleKey(ref.Digest, kind)
		row, ok := out.mappings[key]
		if !validHistoryRef(ref) || !ok || row.Source != ref || len(out.objects[row.Exported.Digest]) != row.Exported.Bytes {
			return nil, &HistoryInputInvalid{}
		}
		if old := documents[key]; old != nil {
			used[key] = true
			return old, nil
		}
		document, e := archiveUnwrap(row, out.objects[row.Exported.Digest])
		if e != nil {
			return nil, e
		}
		if e = archiveValidate(kind, document, row.Representation.Kind == "redacted-view"); e != nil {
			return nil, e
		}
		used[key] = true
		documents[key] = document
		return document, nil
	}
	payload := func(ref HistoryObjectRef, prefix string) (*Value, error) {
		cacheKey := prefix + ref.Digest
		if old := payloads[cacheKey]; old != nil {
			return old, nil
		}
		manifestValue, e := get(ref, prefix+"-manifest")
		if e != nil {
			return nil, e
		}
		manifest, e := readHistoryChunkManifest([]byte(CanonicalJSON(manifestValue)))
		if e != nil {
			return nil, e
		}
		full := []byte{}
		changed := false
		for _, part := range manifest.Parts {
			document, e := get(part, prefix+"-chunk")
			if e != nil {
				return nil, e
			}
			mapping := out.mappings[archiveRoleKey(part.Digest, prefix+"-chunk")]
			if mapping.Representation.Kind == "exact" {
				full = append(full, out.objects[mapping.Exported.Digest]...)
			} else {
				changed = true
				full = append(full, []byte(document.Str())...)
			}
			if len(full) > HistoryTotalBytes {
				return nil, &HistoryInputInvalid{}
			}
		}
		if len(full) < 1 || !changed && (len(full) != manifest.Bytes || HistoryBytesDigest(full) != manifest.Digest) || changed && !archiveMarker.Match(full) {
			return nil, &HistoryInputInvalid{}
		}
		var document *Value
		switch prefix {
		case "source":
			document, e = readHistorySourceJSON(full)
		case "request":
			document, e = readHistoryRequestJSON(full)
		default:
			document, e = readHistoryJSON(full, HistoryTotalBytes, true, true)
		}
		if e != nil {
			return nil, e
		}
		if prefix == "chat" && !changed {
			if _, e = ReadChatContextSnapshot(full); e != nil {
				return nil, e
			}
		}
		if changed {
			original := []byte{}
			complete := true
			for _, part := range manifest.Parts {
				raw, ok := out.objects[part.Digest]
				if !ok {
					complete = false
					break
				}
				original = append(original, raw...)
			}
			if complete {
				if len(original) != manifest.Bytes || HistoryBytesDigest(original) != manifest.Digest {
					return nil, &HistoryInputInvalid{}
				}
				var source *Value
				switch prefix {
				case "source":
					source, e = readHistorySourceJSON(original)
				case "request":
					source, e = readHistoryRequestJSON(original)
				default:
					source, e = readHistoryJSON(original, HistoryTotalBytes, true, true)
				}
				if e != nil {
					return nil, e
				}
				if _, e = archiveDiff(source, document, "", false); e != nil {
					return nil, e
				}
				for _, kind := range []string{"checkpoint", "report", "event"} {
					if _, ok := out.mappings[archiveRoleKey(manifest.Digest, kind)]; ok {
						if e = archiveTaskEqual(kind, source, document); e != nil {
							return nil, e
						}
					}
				}

			}
		}
		if other := logical[manifest.Digest]; other != nil {
			if _, e = archiveDiff(other, document, "", false); e != nil {
				if _, e = archiveDiff(document, other, "", false); e != nil {
					return nil, e
				}
			}
		} else {
			logical[manifest.Digest] = document
		}
		payloads[cacheKey] = document
		return document, nil
	}
	type observation struct {
		value    *Value
		sequence int
	}
	observations := map[string]observation{}
	events := 0
	prior := ""
	eventRefs := []HistoryObjectRef{}
	for i, input := range m.Commits {
		row, e := ReadRunHistoryEntry([]byte(CanonicalJSON(mv.Get("commits").Items()[i])))
		if e != nil || !archiveEqual(row, input) || row.Sequence != i+1 || row.EventStart != events+1 {
			return invalid()
		}
		p := ""
		if row.PreviousDigest != nil {
			p = *row.PreviousDigest
		}
		if p != prior {
			return invalid()
		}
		events += row.EventCount
		prior = row.Digest
		if row.Checkpoint != nil {
			cp, e := get(*row.Checkpoint, "checkpoint")
			if e != nil || cp.Get("id").Str() != m.RunID {
				return invalid()
			}
		}
		if row.Context != nil {
			saved, e := payload(row.Context.Manifest, "chat")
			if e != nil || !saved.IsObject() || !snapshotCount(saved.Get("turns"), 0) || row.Context.Turn == nil || *row.Context.Turn > int(saved.Get("turns").Num()) || !saved.Get("open").IsArray() {
				return invalid()
			}
			open := false
			for _, turn := range saved.Get("open").Items() {
				open = open || turn.Get("number").Num() == float64(*row.Context.Turn)
			}
			if open == row.Context.Closed || row.Context.PrivateScope != "" && (!row.Context.Closed || saved.Get("scope").Str() == row.Context.PrivateScope) {
				return invalid()
			}
		}
		for _, ref := range row.Events {
			envelope, e := get(ref, "event")
			if e != nil {
				return invalid()
			}
			scope, event := envelope.Get("scope"), envelope.Get("event")
			if !scope.Has("task") && scope.Get("execution").Str() != m.RunID || scope.Has("task") && scope.Get("execution").Str() != scope.Get("task").Str() || (event.Get("type").Str() == "execution_checkpoint" || event.Get("type").Str() == "budget_paused") && event.Get("checkpoint").Get("id").Str() != scope.Get("execution").Str() {
				return invalid()
			}
			eventRefs = append(eventRefs, ref)
		}
		for _, ref := range row.Observations {
			value, e := get(ref, "observation")
			if e != nil {
				return invalid()
			}
			if _, ok := observations[ref.Digest]; !ok {
				observations[ref.Digest] = observation{value, row.Sequence}
			}
		}
	}
	last := m.Commits[len(m.Commits)-1]
	if events != head.EventCount || prior != head.Root || last.ExecutionRevision != head.ExecutionRevision || !archiveEqual(last.Checkpoint, head.Checkpoint) || !archiveEqual(last.Context, head.Context) {
		return invalid()
	}
	active, done := map[string]bool{}, map[string]bool{}
	var visit func(string, int, string, *Value, int) error
	visit = func(digest string, depth int, kind string, scope *Value, before int) error {
		row, ok := observations[digest]
		if !ok || depth > 64 || active[digest] || kind != "" && row.value.Get("kind").Str() != kind || scope != nil && CanonicalJSON(scope) != CanonicalJSON(row.value.Get("scope")) || before > 0 && row.sequence >= before {
			return &HistoryInputInvalid{}
		}
		if done[digest] {
			return nil
		}
		active[digest] = true
		o := row.value
		switch o.Get("kind").Str() {
		case "original":
			_, e = payload(archiveRef(o.Get("value")), "source")
		case "compaction":
			if o.Get("originals").Len() < 1 {
				return &HistoryInputInvalid{}
			}
			for _, ref := range o.Get("originals").Items() {
				if _, e := get(archiveRef(ref), "observation"); e != nil {
					return e
				}
				if e := visit(archiveRef(ref).Digest, depth+1, "original", o.Get("scope"), row.sequence); e != nil {
					return e
				}
			}
			if _, e = payload(archiveRef(o.Get("replacement")), "source"); e != nil {
				return e
			}
			for _, ref := range o.Get("prior").Items() {
				if _, e := get(archiveRef(ref), "observation"); e != nil {
					return e
				}
				if e := visit(archiveRef(ref).Digest, depth+1, "compaction", o.Get("scope"), row.sequence); e != nil {
					return e
				}
			}
		case "request":
			if _, e = payload(archiveRef(o.Get("request")), "request"); e != nil {
				return e
			}
			for _, pair := range []struct{ key, kind string }{{"compactions", "compaction"}, {"originals", "original"}} {
				for _, ref := range o.Get(pair.key).Items() {
					if _, e := get(archiveRef(ref), "observation"); e != nil {
						return e
					}
					if e := visit(archiveRef(ref).Digest, depth+1, pair.kind, o.Get("scope"), row.sequence+1); e != nil {
						return e
					}
				}
			}
		case "capture-request":
			if o.Has("nativeBindings") {
				_, e = get(archiveRef(o.Get("nativeBindings")), "evidence")
			}
		case "relation":
			_, e = get(archiveRef(o.Get("evidence")), "evidence")
		}
		if e != nil {
			return e
		}
		delete(active, digest)
		done[digest] = true
		return nil
	}
	for digest := range observations {
		if e = visit(digest, 0, "", nil, 0); e != nil {
			return invalid()
		}
	}
	report, e := get(m.Report, "report")
	if e != nil {
		return invalid()
	}
	feedbackViews := report.Get("feedback")
	feedbackPosition := 0
	metadataPrior := ""
	feedback := map[string]HistoryFeedbackView{}
	for i, ref := range m.Metadata {
		value, e := get(ref, "metadata")
		if e != nil {
			return invalid()
		}
		var row HistoryMetadataEntry
		if json.Unmarshal(value.JSON(), &row) != nil {
			return invalid()
		}
		previous := ""
		if row.PreviousDigest != nil {
			previous = *row.PreviousDigest
		}
		if row.Sequence != i+1 || previous != metadataPrior || row.CommitSequence < 1 || row.CommitSequence > len(m.Commits) || m.Commits[row.CommitSequence-1].ExecutionRevision != row.ExecutionRevision {
			return invalid()
		}
		metadataPrior = row.Digest
		if row.Record.Kind == "capture" {
			cv, e := get(*row.Record.Capture, "capture")
			if e != nil {
				return invalid()
			}
			capture, e := ReadRunCapture([]byte(CanonicalJSON(cv)))
			if e != nil || capture.RunID != m.RunID || capture.ExecutionRevision != row.ExecutionRevision {
				return invalid()
			}
			for _, file := range capture.Files {
				material, problem := payload(file.Manifest, "corpus")
				if problem != nil {
					return invalid()
				}
				if file.Name == "checkpoint.json" {
					native, problem := get(capture.Checkpoint, "checkpoint")
					if problem != nil {
						return invalid()
					}
					if _, problem = archiveDiff(native, material, "", false); problem != nil {
						return invalid()
					}
					if archiveTaskEqual("checkpoint", native, material) != nil {
						return invalid()
					}

				}
			}

		} else {
			f := *row.Record.Feedback
			if feedbackViews == nil || !feedbackViews.IsArray() || feedbackPosition >= feedbackViews.Len() {
				return invalid()
			}
			var display HistoryFeedbackView
			rv := feedbackViews.Items()[feedbackPosition]
			feedbackPosition++
			if json.Unmarshal(rv.JSON(), &display) != nil || !archiveEqual(display.Record, f) || !historySHA256.MatchString(display.SourceDigest) || out.mappings[archiveRoleKey(ref.Digest, "metadata")].Representation.Kind == "exact" && display.SourceDigest != HistoryBytesDigest([]byte(historyInputText(f))) {
				return invalid()
			}
			if row.Record.Candidate != nil {
				cv, err := get(*row.Record.Candidate, "candidate")
				if err != nil {
					return invalid()
				}
				candidate, err := ReadCorpusCandidate([]byte(CanonicalJSON(cv)))
				if err != nil || candidate.FeedbackDigest != display.SourceDigest || candidate.RunID != m.RunID {
					return invalid()
				}
				a, b := candidate.Feedback, f
				if (a.Remark == nil) != (b.Remark == nil) {
					return invalid()
				}
				a.Remark, b.Remark = nil, nil
				if !archiveEqual(a, b) {
					return invalid()
				}
			}
			old, ok := feedback[f.ID]
			sequence := 1
			var expected *string
			if ok {
				sequence = old.Record.Sequence + 1
				digest := old.SourceDigest
				expected = &digest
			}
			if f.RunID != m.RunID || f.Sequence != sequence || !archiveEqual(f.Previous, expected) || ok && !archiveEqual(old.Record.Target, f.Target) {
				return invalid()
			}
			if f.Target.Kind == "assistant-message" {
				index := f.Target.EventSequence - 1
				if index < 0 || index >= len(eventRefs) || eventRefs[index].Digest != f.Target.EventDigest {
					return invalid()
				}
				event, e := get(eventRefs[index], "event")
				if e != nil || event.Get("event").Get("type").Str() != "message" {
					return invalid()
				}
			}
			feedback[f.ID] = display
		}
	}
	root := ""
	if head.Metadata != nil && head.Metadata.Root != nil {
		root = *head.Metadata.Root
	}
	if root != metadataPrior {
		return invalid()
	}
	if feedbackPosition != feedbackViews.Len() {
		return invalid()
	}
	if e != nil || head.Checkpoint == nil {
		return invalid()
	}
	checkpoint, e := get(*head.Checkpoint, "checkpoint")
	if e != nil || report.Get("id").Str() != m.RunID || report.Get("status").Str() != checkpoint.Get("status").Str() || !oneOf(checkpoint.Get("status"), "complete", "stopped") || report.Get("checkpointVersion").Num() != checkpoint.Get("version").Num() {
		return invalid()
	}
	if m.Coverage != "legacy-incomplete" && m.Coverage != "recording-incomplete" {
		return invalid()
	}
	for _, name := range runReportSources {
		if CanonicalJSON(report.Get(name)) != CanonicalJSON(checkpoint.Get(name)) {
			return invalid()
		}
	}
	repeats, problem := projectRepeatTools(checkpoint)
	if problem != nil || !archiveEqual(report.Get("repeatTools"), repeats) {
		return nil, &HistoryInputInvalid{}
	}
	expectedVersion := 1
	if checkpoint.Get("version").Num() == 3 || feedbackPosition > 0 {
		expectedVersion = 2
	}
	if report.Get("version").Num() != float64(expectedVersion) {
		return invalid()
	}
	var route *Value
	var totals *Value
	if checkpoint.Get("version").Num() == 3 {
		route = checkpoint.Get("turnRoute")
		if source := checkpoint.Get("usageBudget"); source != nil {
			var journal UsageBudgetJournal
			if json.Unmarshal(source.JSON(), &journal) != nil {
				return invalid()
			}
			sum, e := UsageJournalTotals(journal)
			if e != nil {
				return invalid()
			}
			totals = MustParseJSON(historyInputText(sum))
		}
	}
	if CanonicalJSON(report.Get("route")) != CanonicalJSON(route) || CanonicalJSON(report.Get("usageTotals")) != CanonicalJSON(totals) {
		return invalid()
	}

	if e := validateHistoryRelations(m.Commits, get, func(ref HistoryObjectRef) bool {
		return out.mappings[archiveRoleKey(ref.Digest, "checkpoint")].Representation.Kind == "redacted-view"
	}); e != nil {
		return invalid()
	}
	if len(used) != len(out.mappings) {
		return invalid()
	}
	return out, nil
}
