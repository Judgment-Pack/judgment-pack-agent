package runcontext

// ADR-0015: drafts belong to the package. No host file or approval is reachable here.
import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
)

func editString(v *Value) string {
	if !v.IsString() {
		return ""
	}
	return v.Str()
}

var EditTools = []string{"propose_edit", "propose_delete", "propose_insert", "undo_edit"}
var editFields = map[string][]string{"propose_edit": {"document", "old_text", "new_text"}, "propose_delete": {"document", "old_text"}, "propose_insert": {"document", "after_anchor", "text"}, "undo_edit": {"document"}}

func IsEdit(name string) bool { _, ok := editFields[name]; return ok }
func EditSchema(name string) json.RawMessage {
	props := Object()
	required := Array()
	for _, key := range editFields[name] {
		s := ObjectOf("type", String("string"))
		if key == "document" {
			s.Set("minLength", Number(1))
			s.Set("maxLength", Number(512))
		}
		if key == "old_text" || key == "after_anchor" {
			s.Set("minLength", Number(1))
		}
		props.Set(key, s)
		required.Append(String(key))
	}
	return ObjectOf("type", String("object"), "properties", props, "required", required, "additionalProperties", Bool(false)).JSON()
}

type AcceptedDocument struct {
	ID       string
	Document json.RawMessage
	Unknowns []string
}
type DraftProposal struct {
	Document json.RawMessage
	Unknowns []string
	Edits    []contract.ProvenanceEdit
}
type draftUndo struct {
	Reference string `json:"reference"`
	Line      int    `json:"line"`
}
type draftContent struct {
	Text     string      `json:"text"`
	Unknowns []string    `json:"unknowns"`
	Undo     []draftUndo `json:"undo"`
}
type heldDraft struct {
	contract.CheckpointDraftDocument
	draftContent
}
type Drafts struct {
	mu        sync.Mutex
	entries   atomic.Value
	session   *SessionContext
	documents []*heldDraft
	notices   []contract.RunEvent
}

func draftLabel(d contract.CheckpointDraftDocument) string {
	return fmt.Sprintf("%s@%s:%d", d.ID, d.Digest, len(d.Edits))
}
func ValidDraft(v *Value) bool {
	if !v.IsObject() || !hasOnly(v, "documents") || !v.Get("documents").IsArray() {
		return false
	}
	for _, row := range v.Get("documents").Items() {
		if !row.IsObject() || row.Len() != 3 || !hasOnly(row, "id", "digest", "edits") || !row.Get("edits").IsArray() {
			return false
		}
		for _, edit := range row.Get("edits").Items() {
			if !edit.IsObject() || edit.Len() != 5 || !hasOnly(edit, "type", "document", "line", "bytes", "valid") {
				return false
			}
		}
	}
	var saved contract.CheckpointDraft
	if json.Unmarshal(v.JSON(), &saved) != nil {
		return false
	}
	ids := map[string]bool{}
	for _, d := range saved.Documents {
		if !strings.HasPrefix(d.ID, "draft:") || len(d.ID) <= 6 || utf8.RuneCountInString(d.ID) > 518 || ids[d.ID] || !digestPattern.MatchString(d.Digest) || d.Edits == nil {
			return false
		}
		ids[d.ID] = true
		for _, e := range d.Edits {
			if !IsEdit(e.Type) || e.Document != strings.TrimPrefix(d.ID, "draft:") || e.Line < 1 || e.Bytes < 0 || !e.Valid {
				return false
			}
		}
	}
	return true
}
func (c *SessionContext) OpenDrafts() error {
	g := &Drafts{session: c}
	g.entries.Store([]*Entry{})
	c.Drafts = g
	if saved := c.Execution.State.Draft; saved != nil {
		snapshot := c.Ledger.Snapshot()
		for _, row := range saved.Documents {
			var found *heldDraft
			for _, entry := range snapshot.Entries {
				if entry.Label == draftLabel(row) {
					value, _ := ParseJSON(entry.Value)
					if !value.Get("unknowns").IsArray() || !value.Get("undo").IsArray() {
						return failure.Authored("stale-state", "The saved draft metadata is unavailable.", "context")
					}
					for _, undo := range value.Get("undo").Items() {
						if !undo.Get("reference").IsString() || !isCount(undo.Get("line"), 9007199254740991) || undo.Get("line").Num() < 1 {
							return failure.Authored("stale-state", "The saved draft undo history is invalid.", "context")
						}
					}
					var content draftContent
					if json.Unmarshal(entry.Value, &content) != nil || "sha256:"+ContextDigest(content.Text) != row.Digest {
						return failure.Authored("stale-state", "The saved draft text differs from its digest.", "context")
					}
					found = &heldDraft{row, content}
					break
				}
			}
			if found == nil {
				return failure.Authored("stale-state", "The saved draft text and undo history are unavailable.", "context")
			}
			g.documents = append(g.documents, found)
		}
	} else if c.session.Context != nil && c.MemoryAdmitted() {
		seen := map[string]bool{}
		for _, d := range c.session.Context.AcceptedDocuments {
			if d.ID == "" || utf8.RuneCountInString(d.ID) > 512 || seen[d.ID] {
				return failure.Authored("configuration", "Accepted draft document ids must be unique and nonempty.", "context")
			}
			seen[d.ID] = true
			if err := g.seed(d.Document, d.Unknowns, d.ID); err != nil {
				return err
			}
		}
	}
	g.publishEntries()
	c.Execution.DraftRows = g.Snapshot
	c.Execution.LoadedDrafts = g.Section
	c.Ledger.Drafts = g.Entries
	return nil
}
func (g *Drafts) Entries() []*Entry { return g.entries.Load().([]*Entry) }
func (g *Drafts) publishEntries() {
	out := []*Entry{}
	for _, d := range g.documents {
		out = append(out, &Entry{ID: d.ID, Kind: "artifact", Label: d.ID, Value: String(d.Text), Bytes: len(d.Text)})
	}
	g.entries.Store(out)
}
func (g *Drafts) Snapshot() *contract.CheckpointDraft {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.snapshot()
}
func (g *Drafts) snapshot() *contract.CheckpointDraft {
	if len(g.documents) == 0 {
		return nil
	}
	out := &contract.CheckpointDraft{Documents: []contract.CheckpointDraftDocument{}}
	for _, d := range g.documents {
		row := d.CheckpointDraftDocument
		row.Edits = append([]contract.ProvenanceEdit{}, row.Edits...)
		out.Documents = append(out.Documents, row)
	}
	return out
}
func (g *Drafts) save(d *heldDraft) error {
	total := len(d.Text)
	index := -1
	for i, held := range g.documents {
		if held.ID == d.ID {
			index = i
		} else {
			total += len(held.Text)
		}
	}
	if total > g.session.Policy.ProposalBytes {
		return failure.ToolInput(fmt.Sprintf("The draft exceeds proposalBytes (%d).", g.session.Policy.ProposalBytes))
	}
	d.Digest = "sha256:" + ContextDigest(d.Text)
	raw, _ := json.Marshal(d.draftContent)
	value, err := ParseJSON(raw)
	if err != nil {
		return err
	}
	if _, err = g.session.Ledger.Retain(Document{ID: "proposal-state:" + ContextDigest(draftLabel(d.CheckpointDraftDocument)), Kind: "artifact", Label: draftLabel(d.CheckpointDraftDocument), Value: value}, false); err != nil {
		return err
	}
	if index < 0 {
		g.documents = append(g.documents, d)
	} else {
		g.documents[index] = d
	}
	g.publishEntries()
	return nil
}
func (g *Drafts) SeedContext(ctx context.Context, document json.RawMessage, unknowns []string) error {
	if g.session.Output != nil {
		clean, err := g.session.Output.JSON(document, "proposal", true)
		if err != nil {
			return err
		}
		if !bytes.Equal(clean, document) {
			if err = g.session.ValidateOutputProposal(ctx, clean); err != nil {
				return err
			}
		}
		document = clean
		for i, text := range unknowns {
			clean, err := g.session.Output.Text(text, "proposal", true)
			if err != nil {
				return err
			}
			unknowns[i] = clean
		}
	}
	return g.Seed(document, unknowns)
}
func (g *Drafts) Seed(document json.RawMessage, unknowns []string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.seed(document, unknowns, "")
}
func (g *Drafts) seed(document json.RawMessage, unknowns []string, id string) error {
	v, err := ParseJSON(document)
	if err != nil || !v.IsObject() {
		return failure.ToolInput("The draft must be a document object.")
	}
	if id == "" {
		id = editString(v.Get("id"))
		if id == "" {
			id = "document"
		}
	}
	if utf8.RuneCountInString(id) > 512 {
		return failure.ToolInput("The draft document id exceeds 512 characters.")
	}
	key := "draft:" + id
	for _, d := range g.documents {
		if d.ID == key {
			old, _ := ParseJSON([]byte(d.Text))
			if CanonicalJSON(old) == CanonicalJSON(v) {
				return nil
			}
			return failure.ToolInput("This document already has a draft; use the edit tools to change it.")
		}
	}
	// JSON.stringify's two-space representation, preserving the document's member order.
	var pretty bytes.Buffer
	_ = json.Indent(&pretty, v.JSON(), "", "  ")
	text := pretty.String()
	return g.save(&heldDraft{contract.CheckpointDraftDocument{ID: key, Edits: []contract.ProvenanceEdit{}}, draftContent{Text: text, Unknowns: append([]string{}, unknowns...), Undo: []draftUndo{}}})
}
func (g *Drafts) Proposal() *DraftProposal {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.documents) == 0 {
		return nil
	}
	d := g.documents[len(g.documents)-1]
	return &DraftProposal{Document: json.RawMessage(d.Text), Unknowns: append([]string{}, d.Unknowns...), Edits: append([]contract.ProvenanceEdit{}, d.Edits...)}
}
func (g *Drafts) Section() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.documents) == 0 {
		return ""
	}
	parts := []string{}
	for _, d := range g.documents {
		parts = append(parts, d.ID+"\n"+d.Text)
	}
	return "PROPOSAL DRAFTS (package-owned data; read with search and read_window; edit with the reserved edit tools; the person accepts each whole document)\n" + FrameBlock("proposal drafts", strings.Join(parts, "\n\n"), g.session.Marker)
}
func (g *Drafts) refuse(name, document, reason string) error {
	if document == "" || utf8.RuneCountInString(document) > 512 {
		reason = "Invalid document identifier (expected 1 to 512 characters). " + reason
	}
	if utf8.RuneCountInString(document) > 512 {
		document = string([]rune(document)[:512])
	}
	var identifier *string
	if document != "" {
		identifier = &document
	}
	guard := "edit refused"
	g.notices = append(g.notices, contract.RunEvent{EditRefused: &contract.RunEventEditRefused{Type: "edit_refused", Document: identifier, Reason: reason}}, contract.RunEvent{Guardrail: &contract.RunEventGuardrail{Type: "guardrail", Tool: name, Action: "refused", Reason: &guard, Detail: reason}})
	return failure.ToolInput(reason)
}
func (g *Drafts) Refuse(name, document string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.refuse(name, document, "Only the author may edit a proposal draft; a child and the critic never edit.")
}
func (g *Drafts) TakeNotices() []contract.RunEvent {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := g.notices
	g.notices = nil
	return out
}
func (g *Drafts) Edit(ctx context.Context, name string, raw json.RawMessage) (ToolResult, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	args, err := ParseJSON(raw)
	document := editString(args.Get("document"))
	refuse := func(reason string) (ToolResult, error) { return ToolResult{}, g.refuse(name, document, reason) }
	if !g.session.MemoryAdmitted() || g.session.ContextPhase() == "plan" {
		return refuse("Only the author may edit a proposal draft; a child and the critic never edit.")
	}
	if err != nil || !args.IsObject() || !hasOnly(args, editFields[name]...) || document == "" || utf8.RuneCountInString(document) > 512 {
		return refuse("Invalid edit arguments; insertion is anchor-only.")
	}
	for _, k := range editFields[name] {
		if !args.Get(k).IsString() {
			return refuse("Invalid edit arguments; insertion is anchor-only.")
		}
	}
	var original *heldDraft
	count := 0
	for _, d := range g.documents {
		count += len(d.Edits)
		if d.ID == "draft:"+document {
			original = d
		}
	}
	if original == nil {
		return refuse("No draft exists for this document; propose_pack must open it first.")
	}
	if count >= g.session.Policy.EditsPerTurn {
		return refuse(fmt.Sprintf("The response reached editsPerTurn (%d).", g.session.Policy.EditsPerTurn))
	}
	next := *original
	next.Undo = append([]draftUndo{}, original.Undo...)
	next.Edits = append([]contract.ProvenanceEdit{}, original.Edits...)
	line, bytes := 1, 0
	if name == "undo_edit" {
		if len(next.Undo) == 0 {
			return refuse("There is no edit to undo.")
		}
		last := next.Undo[len(next.Undo)-1]
		next.Undo = next.Undo[:len(next.Undo)-1]
		found := false
		for _, entry := range g.session.Ledger.Snapshot().Entries {
			if entry.Label == last.Reference {
				var prior draftContent
				if json.Unmarshal(entry.Value, &prior) != nil {
					return refuse("The previous draft revision is unavailable.")
				}
				next.Text = prior.Text
				found = true
				break
			}
		}
		if !found {
			return refuse("The previous draft revision is unavailable.")
		}
		line = last.Line
	} else {
		anchor := editString(args.Get("old_text"))
		if name == "propose_insert" {
			anchor = editString(args.Get("after_anchor"))
		}
		if anchor == "" {
			return refuse("The anchor must be nonempty.")
		}
		count, at := 0, -1
		for start := 0; start <= len(original.Text); {
			pos := strings.Index(original.Text[start:], anchor)
			if pos < 0 {
				break
			}
			at = start + pos
			count++
			start = at + 1
		}
		if count != 1 {
			return refuse(fmt.Sprintf("The anchor occurs %d times; it must occur exactly once.", count))
		}
		inserted := ""
		if name == "propose_edit" {
			inserted = editString(args.Get("new_text"))
		}
		if name == "propose_insert" {
			inserted = editString(args.Get("text"))
		}
		bytes = len(inserted)
		if bytes > g.session.Policy.EditBytes {
			return refuse(fmt.Sprintf("The new text exceeds editBytes (%d).", g.session.Policy.EditBytes))
		}
		line = strings.Count(original.Text[:at], "\n") + 1
		next.Undo = append(next.Undo, draftUndo{draftLabel(original.CheckpointDraftDocument), line})
		start := at
		if name == "propose_insert" {
			start += len(anchor)
		}
		next.Text = original.Text[:start] + inserted + original.Text[at+len(anchor):]
	}
	total := len(next.Text)
	for _, d := range g.documents {
		if d.ID != next.ID {
			total += len(d.Text)
		}
	}
	if total > g.session.Policy.ProposalBytes {
		return refuse(fmt.Sprintf("The draft exceeds proposalBytes (%d).", g.session.Policy.ProposalBytes))
	}

	v, err := ParseJSON([]byte(next.Text))
	if err != nil || !v.IsObject() {
		return refuse("Validation failed: the document is not a JSON object.")
	}
	redacted := false
	if g.session.Output != nil {
		clean, err := g.session.Output.JSON([]byte(next.Text), "draft edit", true)
		if err != nil {
			return ToolResult{}, err
		}
		redacted = string(clean) != next.Text
		next.Text = string(clean)
	}
	if ctx.Err() != nil {
		return ToolResult{}, ctx.Err()
	}
	offered := false
	for _, t := range g.session.session.Tools {
		if t.Name == "validate" {
			offered = true
		}
	}
	if !offered {
		return refuse("Validation failed: Runtime validate is not offered.")
	}
	result, err := g.session.session.CallTool(ctx, "validate", ObjectOf("content", String(next.Text)).JSON())
	if ctx.Err() != nil {
		return ToolResult{}, ctx.Err()
	}
	if err != nil {
		if redacted {
			return ToolResult{}, g.session.Output.Refuse("draft edit", "proposal-validation")
		}
		return refuse("Validation failed: " + err.Error())
	}
	structured := result.StructuredContent
	if structured == nil {
		parts := []string{}
		for _, p := range result.Content {
			if p.Type() == "text" {
				parts = append(parts, p.Text())
			}
		}
		structured, _ = ParseJSON([]byte(strings.Join(parts, "\n")))
	}
	if result.Err() || editString(structured.Get("status")) != "valid" {
		if redacted {
			return ToolResult{}, g.session.Output.Refuse("draft edit", "proposal-validation")
		}
		return refuse("Validation failed: the Runtime did not report valid.")
	}
	next.Edits = append(next.Edits, contract.ProvenanceEdit{Type: name, Document: document, Line: int64(line), Bytes: int64(bytes), Valid: true})
	if err := g.save(&next); err != nil {
		return refuse(err.Error())
	}
	applied := &contract.RunEventEditApplied{Type: "edit_applied", Document: document, Line: int64(line), Bytes: int64(bytes), Valid: true}
	g.notices = append(g.notices, contract.RunEvent{EditApplied: applied})
	encoded, _ := json.Marshal(applied)
	value, _ := ParseJSON(encoded)
	return Answer(value), nil
}
func (c *SessionContext) ContextPhase() string {
	if c.session.Context != nil {
		return c.session.Context.Phase
	}
	return ""
}
func (g *Drafts) Tools() []HostTool {
	out := []HostTool{}
	for _, n := range EditTools {
		name := n
		out = append(out, HostTool{Name: name, Effect: "proposal", Description: "Edit only the package draft; the anchor must occur exactly once. Every candidate is validated; refusal leaves the draft unchanged. The person accepts the whole document.", InputSchema: EditSchema(name), Execute: func(ctx context.Context, args json.RawMessage) (ToolResult, error) { return g.Edit(ctx, name, args) }})
	}
	return out
}

func (g *Drafts) Proposals() []DraftProposal {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := []DraftProposal{}
	for _, d := range g.documents {
		out = append(out, DraftProposal{Document: json.RawMessage(d.Text), Unknowns: append([]string{}, d.Unknowns...), Edits: append([]contract.ProvenanceEdit{}, d.Edits...)})
	}
	return out
}

// OfferedEditTools names the reserved edit family in the author's loop offer.
// Hosts add these declarations beside OfferedSkillTools and OfferedReviewTool before opening the gate.
func OfferedEditTools(session Session) []contract.ToolOfferHostTool {
	if session.Context != nil && (session.Context.Objective != "" || session.Context.Phase != "" && session.Context.Phase != "loop") {
		return nil
	}
	effect := "proposal"
	out := []contract.ToolOfferHostTool{}
	for _, name := range EditTools {
		out = append(out, contract.ToolOfferHostTool{Name: name, Description: "Edit the package proposal draft.", InputSchema: EditSchema(name), Effect: &effect})
	}
	return out
}
