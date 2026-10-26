package runcontext

// ADR-0014: the package records proposals and recall; the host owns acceptance and storage.
import (
	"context"
	"encoding/json"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
)

var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

const Remember = "remember"
const RememberSchema = `{"type":"object","properties":{"kind":{"type":"string","enum":["fact","preference","decision","reference"]},"text":{"type":"string","minLength":1}},"required":["kind","text"],"additionalProperties":false}`

func memoryKind(s string) bool {
	return s == "fact" || s == "preference" || s == "decision" || s == "reference"
}
func memoryState(s string) bool {
	return s == "proposed" || s == "accepted" || s == "declined" || s == "revoked" || s == "expired"
}

// Match the schema's RFC3339 grammar before parsing: Go also accepts single-digit
// hours, comma fractions and out-of-range offsets that RFC3339 does not permit.
var memoryDatePattern = regexp.MustCompile(`^[0-9]{4}-(0[1-9]|1[0-2])-(0[1-9]|[12][0-9]|3[01])[Tt]([01][0-9]|2[0-3]):[0-5][0-9]:[0-5][0-9](\.[0-9]+)?([Zz]|[+-]([01][0-9]|2[0-3]):[0-5][0-9])$`)

func memoryDate(s string) bool {
	if !memoryDatePattern.MatchString(s) || strings.HasPrefix(s, "0000-") {
		return false
	}
	_, err := time.Parse(time.RFC3339Nano, strings.ToUpper(s))
	return err == nil
}
func memoryIdentifier(s string) bool {
	n := utf8.RuneCountInString(s)
	return n > 0 && n <= 100
}
func validMemory(m contract.Memory) bool {
	return memoryIdentifier(m.ID) && memoryKind(m.Kind) && m.Text != "" && memoryIdentifier(m.Source.Run) && (m.Source.ProposedBy == "model" || m.Source.ProposedBy == "person" || m.Source.ProposedBy == "host") && (m.Scope == "project" || m.Scope == "person") && digestPattern.MatchString(m.Digest) && memoryDate(m.CreatedAt) && (m.ExpiresAt == nil || memoryDate(*m.ExpiresAt)) && memoryState(m.State)
}
func ValidMemories(v *Value) bool {
	if !isObject(v) || !hasOnly(v, "proposed", "recalled") || !v.Get("proposed").IsArray() || !v.Get("recalled").IsArray() {
		return false
	}
	var book contract.ProvenanceMemories
	if json.Unmarshal(v.JSON(), &book) != nil || len(book.Proposed) > 256 || len(book.Recalled) > 256 {
		return false
	}
	ids := map[string]bool{}
	for _, m := range book.Proposed {
		if !validMemory(m) || m.State != "proposed" || ids[m.ID] {
			return false
		}
		ids[m.ID] = true
	}
	ids = map[string]bool{}
	for _, m := range book.Recalled {
		if !memoryIdentifier(m.ID) || !digestPattern.MatchString(m.Digest) || !memoryKind(m.Kind) || !memoryState(m.State) || ids[m.ID] {
			return false
		}
		ids[m.ID] = true
	}
	return true
}

type MemoryRecall struct {
	Now     time.Time
	Text    string
	Rows    []contract.ProvenanceMemoryRecall
	Records []contract.Memory
	Bytes   int
}

func RecallMemory(value any, bound int, marker string) (MemoryRecall, error) {
	out := MemoryRecall{Rows: []contract.ProvenanceMemoryRecall{}, Now: time.Now()}
	fail := func(detail string) (MemoryRecall, error) {
		return MemoryRecall{}, failure.Authored("configuration", "The host's memoryFor answer "+detail+"; nothing was sent.", "context")
	}
	if value == nil {
		return out, nil
	}
	if _, ok := value.(string); ok {
		return fail("has an unknown bare-text alias; use a list of closed Memory records")
	}

	records, ok := value.([]contract.Memory)
	if !ok || len(records) > 256 {
		return fail("is not a list of closed memory records")
	}
	ids := map[string]bool{}
	blocks := []string{}
	for _, m := range records {
		if !validMemory(m) || ids[m.ID] {
			return fail("has an invalid record or duplicate id")
		}
		ids[m.ID] = true
		if m.Digest != "sha256:"+ContextDigest(m.Text) {
			return fail("has a mismatched digest")
		}
		if m.State != "accepted" {
			continue
		}
		if m.ExpiresAt != nil {
			expiry, _ := time.Parse(time.RFC3339Nano, strings.ToUpper(*m.ExpiresAt))
			if !expiry.After(out.Now) {
				continue
			}
		}
		raw, _ := json.Marshal(m)
		v, _ := ParseJSON(raw)
		out.Bytes += len(CanonicalJSON(v))
		if out.Bytes > bound {
			return fail("is over memoryBytes (" + strconv.Itoa(bound) + ")")
		}
		source, _ := json.Marshal(m.Source)
		sv, _ := ParseJSON(source)
		blocks = append(blocks, FrameBlock("memory", "kind: "+m.Kind+"\nsource: "+CanonicalJSON(sv)+"\nid: "+QuoteJSON(m.ID)+"\n"+m.Text, marker))
		out.Rows = append(out.Rows, contract.ProvenanceMemoryRecall{ID: m.ID, Digest: m.Digest, Kind: m.Kind, State: m.State})
	}
	out.Text = strings.Join(blocks, "\n\n")
	out.Records = records
	return out, nil
}

type MemoryGate struct {
	mu      sync.Mutex
	book    contract.ProvenanceMemories
	notices []contract.RunEvent
	session *SessionContext
}

func (c *SessionContext) OpenMemories() {
	g := &MemoryGate{session: c, book: contract.ProvenanceMemories{Proposed: []contract.Memory{}, Recalled: []contract.ProvenanceMemoryRecall{}}}
	if c.Execution.State().Memories != nil {
		raw, _ := json.Marshal(c.Execution.State().Memories)
		_ = json.Unmarshal(raw, &g.book)
	}
	c.Memories = g
	c.Execution.MemoryRows = g.Snapshot
}
func (g *MemoryGate) Snapshot() *contract.ProvenanceMemories {
	g.mu.Lock()
	defer func() { g.session.Execution.MutateCheckpoint(CheckpointPatch{"Memories": &g.book}); g.mu.Unlock() }()
	var copy contract.ProvenanceMemories
	raw, _ := json.Marshal(g.book)
	_ = json.Unmarshal(raw, &copy)
	return &copy
}
func (g *MemoryGate) TakeNotices() []contract.RunEvent {
	g.mu.Lock()
	defer func() { g.session.Execution.MutateCheckpoint(CheckpointPatch{"Memories": &g.book}); g.mu.Unlock() }()
	out := g.notices
	g.notices = nil
	return out
}
func lifecycleFailure(detail string) error {
	err := failure.Authored("configuration", "The host's memoryLifecycle answer "+detail+"; nothing was sent.", "context")
	err.Failure.Outcome = "not-started"
	return err
}

// ReadMemoryLifecycle checks closed host input before any report publication.
func ReadMemoryLifecycle(answer any) ([]contract.MemoryLifecycleUpdate, error) {
	if answer == nil {
		return nil, nil
	}
	raw, err := json.Marshal(answer)
	if err != nil {
		return nil, lifecycleFailure("is not a list of closed lifecycle rows")
	}
	if string(raw) == "null" {
		return nil, nil
	}
	value, err := ParseJSON(raw)
	if err != nil || !value.IsArray() || len(value.Items()) > 256 {
		return nil, lifecycleFailure("is not a list of closed lifecycle rows")
	}
	rows := []contract.MemoryLifecycleUpdate{}
	ids := map[string]bool{}
	for _, row := range value.Items() {
		if !isObject(row) || !hasOnly(row, "id", "digest", "kind", "state") || !row.Get("id").IsString() || !memoryIdentifier(row.Get("id").Str()) || !row.Get("digest").IsString() || !digestPattern.MatchString(row.Get("digest").Str()) || !row.Get("kind").IsString() || !memoryKind(row.Get("kind").Str()) || !row.Get("state").IsString() || (row.Get("state").Str() != "revoked" && row.Get("state").Str() != "expired") {
			return nil, lifecycleFailure("is not a list of closed lifecycle rows")
		}
		if ids[row.Get("id").Str()] {
			return nil, lifecycleFailure("has duplicate ids")
		}
		ids[row.Get("id").Str()] = true
		var update contract.MemoryLifecycleUpdate
		if json.Unmarshal(row.JSON(), &update) != nil {
			return nil, lifecycleFailure("is not a list of closed lifecycle rows")
		}
		rows = append(rows, update)
	}
	return rows, nil
}
func nextMemoryRecall(prior []contract.ProvenanceMemoryRecall, recall MemoryRecall, lifecycle []contract.MemoryLifecycleUpdate) ([]contract.ProvenanceMemoryRecall, error) {
	next := append([]contract.ProvenanceMemoryRecall{}, prior...)
	before := map[string]contract.ProvenanceMemoryRecall{}
	indexes := map[string]int{}
	full := map[string]string{}
	now := recall.Now
	if now.IsZero() {
		now = time.Now()
	}
	transition := func(previous contract.ProvenanceMemoryRecall, target string) error {
		if previous.State != target && !(previous.State == "accepted" && (target == "revoked" || target == "expired")) {
			return lifecycleFailure("has an invalid transition")
		}
		return nil
	}
	for i, row := range prior {
		before[row.ID] = row
		indexes[row.ID] = i
	}
	for _, record := range recall.Records {
		target := record.State
		if target == "accepted" && record.ExpiresAt != nil {
			expiry, _ := time.Parse(time.RFC3339Nano, strings.ToUpper(*record.ExpiresAt))
			if !expiry.After(now) {
				target = "expired"
			}
		}
		full[record.ID] = target
		previous, known := before[record.ID]
		if !known {
			continue
		}
		if previous.Digest != record.Digest || previous.Kind != record.Kind {
			return nil, lifecycleFailure("has a mismatched digest or kind in full-record recall")
		}
		if err := transition(previous, target); err != nil {
			return nil, err
		}
		next[indexes[record.ID]].State = target
	}
	for _, update := range lifecycle {
		previous, known := before[update.ID]
		if !known {
			return nil, lifecycleFailure("names an unknown recalled identity")
		}
		if previous.Digest != update.Digest || previous.Kind != update.Kind {
			return nil, lifecycleFailure("has a mismatched digest or kind")
		}
		if err := transition(previous, update.State); err != nil {
			return nil, err
		}
		if target, present := full[update.ID]; present && target != update.State {
			return nil, lifecycleFailure("contradicts full-record recall")
		}
		next[indexes[update.ID]].State = update.State
	}
	for _, row := range recall.Rows {
		if _, known := before[row.ID]; !known {
			next = append(next, row)
		}
	}
	return next, nil
}
func (g *MemoryGate) recalledSnapshot() []contract.ProvenanceMemoryRecall {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]contract.ProvenanceMemoryRecall{}, g.book.Recalled...)
}
func validMemoryBook(book *contract.ProvenanceMemories) bool {
	raw, err := json.Marshal(book)
	if err != nil {
		return false
	}
	value, err := ParseJSON(raw)
	return err == nil && ValidMemories(value)
}
func (g *MemoryGate) commitRecall(expected, next []contract.ProvenanceMemoryRecall, recall MemoryRecall) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !reflect.DeepEqual(append([]contract.ProvenanceMemoryRecall{}, g.book.Recalled...), append([]contract.ProvenanceMemoryRecall{}, expected...)) {
		return lifecycleFailure("is stale because the recalled report changed")
	}
	raw, _ := json.Marshal(g.book)
	var candidate contract.ProvenanceMemories
	if json.Unmarshal(raw, &candidate) != nil {
		return lifecycleFailure("produces an invalid memory report")
	}
	candidate.Recalled = append([]contract.ProvenanceMemoryRecall{}, next...)
	if !validMemoryBook(&candidate) {
		return lifecycleFailure("produces an invalid or full memory report")
	}
	if err := g.session.Execution.MutateCheckpoint(CheckpointPatch{"Memories": &candidate}, func(saved *Execution) error {
		if !validMemoryBook(saved.Memories) {
			return lifecycleFailure("produces an invalid or full memory report")
		}
		return nil
	}); err != nil {
		return err
	}
	g.book = candidate
	if recall.Bytes > 0 || len(recall.Rows) > 0 {
		ids, digests := []string{}, []string{}
		for _, row := range recall.Rows {
			ids = append(ids, row.ID)
			digests = append(digests, row.Digest)
		}
		g.notices = append(g.notices, contract.RunEvent{MemoryRecalled: &contract.RunEventMemoryRecalled{Type: "memory_recalled", Ids: ids, Digests: digests, Bytes: int64(recall.Bytes)}})
	}
	return nil
}
func (g *MemoryGate) RecordRecall(recall MemoryRecall) error {
	prior := g.recalledSnapshot()
	next, err := nextMemoryRecall(prior, recall, nil)
	if err != nil {
		return err
	}
	return g.commitRecall(prior, next, recall)
}
func (c *SessionContext) MemoryAdmitted() bool {
	return c.objective == "" && c.session.ChildDefinition == nil && !c.CriticReading && !c.Reformatting
}
func (g *MemoryGate) refuse(detail string) error {
	reason := "memory refused"
	g.notices = append(g.notices, contract.RunEvent{Guardrail: &contract.RunEventGuardrail{Type: "guardrail", Tool: Remember, Action: "refused", Detail: detail, Reason: &reason}})
	return failure.ToolInput(detail)
}
func (c *SessionContext) RefuseMemory() string {
	g := c.Memories
	g.mu.Lock()
	defer func() { g.session.Execution.MutateCheckpoint(CheckpointPatch{"Memories": &g.book}); g.mu.Unlock() }()
	detail := "Only the author may propose memory."
	_ = g.refuse(detail)
	return detail
}
func (g *MemoryGate) Tool() HostTool {
	return HostTool{Name: Remember, Taxonomy: &ToolTaxonomy{Operation: "propose", Effect: "propose"}, Description: "Propose a memory for the person to accept or decline. This records a proposal only; nothing is stored or accepted.", InputSchema: json.RawMessage(RememberSchema), Effect: "plan", Execute: func(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
		g.mu.Lock()
		defer func() { g.session.Execution.MutateCheckpoint(CheckpointPatch{"Memories": &g.book}); g.mu.Unlock() }()
		if !g.session.MemoryAdmitted() {
			return ToolResult{}, g.refuse("Only the author may propose memory.")
		}
		args, err := ParseJSON(raw)
		if err != nil || !isObject(args) || !hasOnly(args, "kind", "text") || !memoryKind(args.Get("kind").Str()) || !args.Get("text").IsString() || args.Get("text").Str() == "" || len(args.Get("text").Str()) > g.session.Policy.MemoryProposalBytes {
			return ToolResult{}, g.refuse("Memory proposal has an invalid kind, text or byte bound.")
		}
		if len(g.book.Proposed) >= g.session.Policy.MemoryProposalsPerRun {
			return ToolResult{}, g.refuse("Memory proposals exceed memoryProposalsPerRun.")
		}
		m := contract.Memory{ID: DataMarker(), Kind: args.Get("kind").Str(), Text: args.Get("text").Str(), Source: contract.MemorySource{Run: g.session.Execution.State().ID, ProposedBy: "model"}, Scope: "project", Digest: "sha256:" + ContextDigest(args.Get("text").Str()), CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), State: "proposed"}
		g.book.Proposed = append(g.book.Proposed, m)
		g.notices = append(g.notices, contract.RunEvent{MemoryProposed: &contract.RunEventMemoryProposed{Type: "memory_proposed", Memory: m}})
		return ToolResult{Content: []Content{TextContent(`{"proposed":` + QuoteJSON(m.ID) + `,"state":"proposed"}`)}}, nil
	}}
}
