package runcontext

// Tool sources (ADR-0006; the port of packages/harness/toolSources.ts): tools from outside the
// Runtime, held at the boundary every adapter enters.
//
// A gateway connection's tools, a public MCP server's and a tool the person created all reach the
// model as host tools: the host declares each with its source record and executes every call under
// the person's grants; the package keeps no MCP client and never reaches a source. ContextualSession
// reads the declarations (DeclareSources), names each tool for the wire (WireName), bounds and frames
// its description and holds the schema budget (OfferSources), and wraps each call in the source gate
// (SourceGate.Call): the source's changed tool list, a catalogued tool not yet loaded, the calls per
// turn, the effect class and its approval, and the source's deadline. The boundary carries the source
// record on the tool's events (SourceGate.Decorate) and the report in the checkpoint's sources.
//
// A Go host opens the gate with the names the boundary will offer (OfferedHostTools), as it adds the
// boundary's context tools: the gate holds a host tool to its phase by the offered name.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter/wire"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
	"github.com/Judgment-Pack/judgment-pack-agent/go/gate"
)

// ToolSource is a source as the host declares it on a host tool (ToolSource in toolSources.ts):
// the record, and for an MCP source the host's check of the list the server serves now, asked
// before each call. A digest other than the pinned one refuses the source until the host declares
// the new one, which only the person's re-approval gives it.
type ToolSource struct {
	Kind           string
	ID             string
	Trust          string
	Version        string
	ToolListDigest string
	CurrentDigest  func(ctx context.Context) (string, error)
}

// Record is the source as the contract's sourceRecord carries it, with the tool's own name.
func (s ToolSource) Record(tool string) contract.ToolOfferSourceRecord {
	out := contract.ToolOfferSourceRecord{Kind: s.Kind, ID: s.ID, Trust: s.Trust}
	if s.Version != "" {
		version := s.Version
		out.Version = &version
	}
	if s.ToolListDigest != "" {
		digest := s.ToolListDigest
		out.ToolListDigest = &digest
	}
	if tool != "" {
		own := tool
		out.Tool = &own
	}
	return out
}

// LoadTool is the harness tool that loads a catalogued tool's schema; a reserved name.
const LoadTool = "load_tool"

// LoadToolSchema is load_tool's schema: one name (LOAD_TOOL_SCHEMA).
const LoadToolSchema = `{"type":"object","properties":{"name":{"type":"string","description":"The catalogued tool's name, as the tools list shows it."}},"required":["name"]}`

// LoadToolDescription is load_tool's description.
const LoadToolDescription = "Load the input schema of one catalogued tool before calling it. Pass its name as the tools list shows it; only a catalogued tool's schema loads. A loaded schema stays in the execution state for the rest of this response."

// CatalogueSchema is a catalogued tool's schema, open, so the request's tools never change.
const CatalogueSchema = `{"type":"object"}`

// CatalogueLineBytes is the most UTF-8 bytes of a catalogued tool's one line.
const CatalogueLineBytes = 160

// Catalogued are the words before a catalogued tool's framed line (CATALOGUED).
const Catalogued = "Catalogued tool: call " + LoadTool + " with this name for its schema before calling it."

// Proposed is what the model is told of a proposed call (PROPOSED).
const Proposed = "Proposed for the person’s review: this call was not run. The person accepts or rejects it after this response, " +
	"and the host runs it only on acceptance. Do not repeat it; say in your answer what you proposed and why."

var (
	sourceID      = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9-]{0,23}$`)
	sourceDigest  = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	wireCharacter = regexp.MustCompile(`^[A-Za-z0-9_-]$`)
)

// HeldNames are the names a tool with a source may not bear as its own (HELD_NAMES): the Runtime's
// tools, on the ceiling and off it, reach the model only through the gate, and the harness's own
// names are the harness's.
var HeldNames = func() map[string]bool {
	out := map[string]bool{LoadTool: true, "propose_pack": true}
	for _, name := range gate.Ceiling {
		out[name] = true
	}
	for _, refused := range gate.RefusedRuntimeTools {
		out[refused.Name] = true
	}
	for _, name := range gate.ReservedNames {
		out[name] = true
	}
	return out
}()

// WireName is the wire name of one tool of one source (wireName): the source's id, two
// underscores and the tool's own name with every character outside [A-Za-z0-9_-] replaced by _;
// where anything was replaced or the result passes 64 characters, its first 55, an underscore and
// the first 8 hex digits of the SHA-256 of the id, a line feed and the own name.
func WireName(source, tool string) string {
	var cleaned strings.Builder
	for _, r := range tool {
		if wireCharacter.MatchString(string(r)) {
			cleaned.WriteRune(r)
		} else {
			cleaned.WriteByte('_')
		}
	}
	base := source + "__" + cleaned.String()
	if cleaned.String() == tool && len(base) <= 64 {
		return base
	}
	if len(base) > 55 {
		base = base[:55]
	}
	return base + "_" + ContextDigest(source + "\n" + tool)[:8]
}

// ListedTool is one tool of a source as its list's digest reads it.
type ListedTool struct {
	Name        string
	Description string
	InputSchema json.RawMessage
}

// ToolListDigest is toolListDigest: sha256: and the SHA-256 of the canonical JSON of the tools,
// each {name, description, inputSchema}, ordered by name in UTF-16 code units.
func ToolListDigest(tools []ListedTool) string {
	held := append([]ListedTool{}, tools...)
	sort.SliceStable(held, func(i, j int) bool { return lessUTF16(held[i].Name, held[j].Name) })
	list := Array()
	for _, tool := range held {
		item := ObjectOf("name", String(tool.Name), "description", String(tool.Description))
		if len(tool.InputSchema) > 0 {
			if schema, err := ParseJSON(tool.InputSchema); err == nil {
				item.Set("inputSchema", schema)
			}
		}
		list.Append(item)
	}
	return "sha256:" + ContextDigest(CanonicalJSON(list))
}

// SourceEntry is a tool with a source as the boundary holds it after the declaration.
type SourceEntry struct {
	Wire        string
	Record      contract.ToolOfferSourceRecord
	EffectClass string
	Approval    string
	Declared    HostTool
	// Description is what the model may be shown of it: bounded, unframed.
	Description string
	Catalogued  bool
}

// Withheld is a declaration the package withheld, and why: the guardrail it earns.
type Withheld struct {
	Tool   string
	Detail string
}

// Declared is what the declaration made of the host's tools.
type Declared struct {
	HostTools []HostTool
	Entries   map[string]*SourceEntry
	Sources   []ToolSource
	Withheld  []Withheld
}

// approvalDefault is the approval each effect class has unless the host declares one.
var approvalDefault = map[string]string{"read": "none", "propose": "none", "act": "required"}

// effectOf is the effect a tool of each class has on the offer: Plan mode and a child admit reads only.
var effectOf = map[string]string{"read": "read", "propose": "proposal", "act": ""}

func declarationProblem(tool HostTool) string {
	s := tool.Source
	switch {
	case s.Kind == "runtime":
		return "its source is the Runtime, whose tools come through tools/list behind the gate and are never declared"
	case s.Kind != "gateway" && s.Kind != "mcp" && s.Kind != "custom":
		return "its source kind is not gateway, mcp or custom"
	case !sourceID.MatchString(s.ID):
		return "its source id is not a letter then up to 23 letters, digits and hyphens"
	case s.Trust != "first-party" && s.Trust != "connected" && s.Trust != "public" && s.Trust != "person":
		return "its trust class is not first-party, connected, public or person"
	case U16Length(s.Version) > 64:
		return "its source version is not 1 to 64 characters"
	case s.ToolListDigest != "" && !sourceDigest.MatchString(s.ToolListDigest):
		return "its tool list digest is not sha256: and 64 lower-case hex digits"
	case s.Version == "" && s.ToolListDigest == "":
		return "its source names neither a version nor a tool list digest"
	case s.Kind == "mcp" && s.ToolListDigest == "":
		return "its source is an MCP server whose tool list is not pinned by digest"
	case tool.Name == "" || U16Length(tool.Name) > 128:
		return "its name is not 1 to 128 characters"
	}
	if schema, err := ParseJSON(tool.InputSchema); err != nil || !schema.IsObject() {
		return "its input schema is not a JSON object"
	}
	switch {
	case tool.Effect != "":
		return "it declares effect, which the package sets from effectClass for a tool with a source"
	case tool.Interaction != "":
		return "it declares an interaction, and a tool with a source is never a question or a delegation"
	case tool.EffectClass != "" && tool.EffectClass != "read" && tool.EffectClass != "propose" && tool.EffectClass != "act":
		return "its effect class is not read, propose or act"
	case tool.Approval != "" && tool.Approval != "none" && tool.Approval != "required":
		return "its approval is not none or required"
	case tool.Delegation != "" && tool.EffectClass != "read":
		return "it is offered to delegated children without being declared a read"
	}
	return ""
}

// DeclareSources is declareSources: read the host's tools with a source, name each for the wire,
// and withhold what may not be offered. A declaration the package cannot read ends the run before
// any request (configuration); a held name, a wire name that still collides and an MCP source whose
// declared tools are not the list the person approved are withheld, each with a guardrail. served
// are the Runtime tools' names the session offers.
func DeclareSources(tools []HostTool, served []string) (*Declared, error) {
	out := &Declared{Entries: map[string]*SourceEntry{}}
	sources := map[string]ToolSource{}
	bySource := map[string][]ListedTool{}
	type held struct {
		tool HostTool
		wire string
	}
	var declared []held
	for _, tool := range tools {
		if tool.Source == nil {
			continue
		}
		if problem := declarationProblem(tool); problem != "" {
			name := tool.Name
			if U16Length(name) > 64 {
				name = U16Slice(name, 0, 64)
			}
			return nil, failure.Authored("configuration", "The host tool "+QuoteJSON(name)+" has a source declaration this package cannot read: "+problem+". Nothing was sent.", "context")
		}
		record := *tool.Source
		if earlier, ok := sources[record.ID]; ok {
			if earlier.Kind != record.Kind || earlier.Trust != record.Trust || earlier.Version != record.Version || earlier.ToolListDigest != record.ToolListDigest {
				return nil, failure.Authored("configuration", "The source "+record.ID+" is declared twice with different records. Nothing was sent.", "context")
			}
		} else {
			sources[record.ID] = *tool.Source
			out.Sources = append(out.Sources, *tool.Source)
		}
		bySource[record.ID] = append(bySource[record.ID], ListedTool{Name: tool.Name, Description: tool.Description, InputSchema: tool.InputSchema})
		declared = append(declared, held{tool: tool, wire: WireName(record.ID, tool.Name)})
	}
	unapproved := map[string]bool{}
	for _, source := range out.Sources {
		if source.Kind != "mcp" {
			continue
		}
		if digest := ToolListDigest(bySource[source.ID]); digest != source.ToolListDigest {
			unapproved[source.ID] = true
			out.Withheld = append(out.Withheld, Withheld{Tool: source.ID, Detail: "the tools " + source.ID + " declares are not the list the person approved (approved " +
				source.ToolListDigest + ", declared " + digest + "); none of its tools is offered until the person approves the new list"})
		}
	}
	taken := map[string]int{}
	for _, d := range declared {
		taken[d.wire]++
	}
	others := map[string]bool{}
	for _, name := range served {
		others[name] = true
	}
	for _, tool := range tools {
		if tool.Source == nil {
			others[tool.Name] = true
		}
	}
	for name := range HeldNames {
		others[name] = true
	}
	offered := map[int]HostTool{}
	for at, d := range declared {
		id := d.tool.Source.ID
		if unapproved[id] {
			continue
		}
		if HeldNames[d.tool.Name] {
			out.Withheld = append(out.Withheld, Withheld{Tool: d.wire, Detail: id + " serves a tool named " + d.tool.Name +
				", a name the Runtime or the harness keeps; it is not offered, and a Runtime's tools reach the model only through the gate"})
			continue
		}
		if taken[d.wire] > 1 || others[d.wire] {
			name := d.tool.Name
			if U16Length(name) > 64 {
				name = U16Slice(name, 0, 64)
			}
			out.Withheld = append(out.Withheld, Withheld{Tool: d.wire, Detail: "the wire name " + d.wire + " of " + id + "'s " + QuoteJSON(name) +
				" is also another offered tool's; neither is offered"})
			continue
		}
		class := d.tool.EffectClass
		if class == "" {
			class = "act"
		}
		approval := d.tool.Approval
		if approval == "" {
			approval = approvalDefault[class]
		}
		shown := d.tool
		shown.Name = d.wire
		shown.SourceTool = d.tool.Name
		shown.EffectClass = class
		shown.Approval = approval
		shown.Effect = effectOf[class]
		shown.Approve = nil
		offered[at] = shown
		out.Entries[d.wire] = &SourceEntry{Wire: d.wire, Record: d.tool.Source.Record(d.tool.Name), EffectClass: class, Approval: approval,
			Declared: d.tool, Description: d.tool.Description}
	}
	at := 0
	for _, tool := range tools {
		if tool.Source == nil {
			out.HostTools = append(out.HostTools, tool)
			continue
		}
		if shown, ok := offered[at]; ok {
			out.HostTools = append(out.HostTools, shown)
		}
		at++
	}
	return out, nil
}

// BoundedDescription is a description at most bytes, and a note saying where it was cut.
func BoundedDescription(text string, bytes int) string {
	if UTF8Bytes(text) <= bytes {
		return text
	}
	return Prefix(text, bytes) + " [cut at " + itoa(bytes) + " bytes]"
}

// FramedDescription is the source's text framed: the opening line says whose it is and that it is data.
func FramedDescription(wire, source, text, marker string) string {
	return FrameBlock("the description of tool "+QuoteJSON(wire)+" from source "+QuoteJSON(source), text, marker)
}

// OfferSources is offerSources: each description bounded and framed with the run's marker. Where the
// tools with a source pass OfferedSchemas or OfferedSchemaBytes, sources are made catalogues, the
// largest first (by its tools offered, then by their bytes, then by id), until the rest fit; a
// catalogued source's tools are offered as catalogue entries. Whether any source became one is
// answered; the boundary then offers load_tool.
func OfferSources(tools []HostTool, entries map[string]*SourceEntry, policy Policy, marker string) ([]HostTool, bool) {
	type size struct{ count, bytes int }
	framed := map[string]string{}
	sizes := map[string]*size{}
	var ids []string
	for _, tool := range tools {
		entry := entries[tool.Name]
		if entry == nil {
			continue
		}
		entry.Description = BoundedDescription(entry.Declared.Description, policy.ToolDescriptionBytes)
		text := FramedDescription(entry.Wire, entry.Record.ID, entry.Description, marker)
		framed[tool.Name] = text
		held := sizes[entry.Record.ID]
		if held == nil {
			held = &size{}
			sizes[entry.Record.ID] = held
			ids = append(ids, entry.Record.ID)
		}
		held.count++
		schema, err := ParseJSON(tool.InputSchema)
		if err != nil {
			schema = Null()
		}
		held.bytes += UTF8Bytes(ObjectOf("name", String(tool.Name), "description", String(text), "inputSchema", schema).Text())
	}
	count, bytes := 0, 0
	for _, held := range sizes {
		count += held.count
		bytes += held.bytes
	}
	sort.SliceStable(ids, func(i, j int) bool {
		a, b := sizes[ids[i]], sizes[ids[j]]
		if a.count != b.count {
			return a.count > b.count
		}
		if a.bytes != b.bytes {
			return a.bytes > b.bytes
		}
		return lessUTF16(ids[i], ids[j])
	})
	catalogued := map[string]bool{}
	for _, id := range ids {
		if count <= policy.OfferedSchemas && bytes <= policy.OfferedSchemaBytes {
			break
		}
		catalogued[id] = true
		count -= sizes[id].count
		bytes -= sizes[id].bytes
	}
	out := make([]HostTool, 0, len(tools))
	for _, tool := range tools {
		entry := entries[tool.Name]
		if entry == nil {
			out = append(out, tool)
			continue
		}
		if !catalogued[entry.Record.ID] {
			tool.Description = framed[tool.Name]
			out = append(out, tool)
			continue
		}
		entry.Catalogued = true
		line := Prefix(strings.TrimSpace(strings.SplitN(entry.Description, "\n", 2)[0]), CatalogueLineBytes)
		tool.Description = Catalogued + "\n" + FramedDescription(entry.Wire, entry.Record.ID, line, marker)
		tool.InputSchema = json.RawMessage(CatalogueSchema)
		out = append(out, tool)
	}
	return out, len(catalogued) > 0
}

// OfferedHostTools are the declarations a Go host opens the gate with for tools with a source: the
// host's tools as the boundary will offer them (wire names, withheld ones left out) and load_tool
// where the schema budget makes any source a catalogue. served are the Runtime tools' names.
func OfferedHostTools(tools []HostTool, served []string, policy Policy) ([]contract.ToolOfferHostTool, error) {
	declared, err := DeclareSources(tools, served)
	if err != nil {
		return nil, err
	}
	shown, catalogue := OfferSources(declared.HostTools, declared.Entries, policy, "")
	out := make([]contract.ToolOfferHostTool, 0, len(shown)+1)
	for _, tool := range shown {
		out = append(out, tool.Declaration())
	}
	if catalogue {
		out = append(out, contract.ToolOfferHostTool{Name: LoadTool, Description: LoadToolDescription, InputSchema: json.RawMessage(LoadToolSchema)})
	}
	return out, nil
}

// SourceGate is the boundary's hold on the tools with a source for one run (SourceGate in
// toolSources.ts): each call's checks in order, the run report, load_tool and the loaded schemas,
// and the record on the events. Its methods are safe for concurrent use.
type SourceGate struct {
	Entries map[string]*SourceEntry
	sources []ToolSource
	policy  *Policy
	marker  string
	family  string
	rows    func() *[]contract.ProvenanceSourceFigures

	mu          sync.Mutex
	changed     map[string]bool
	loaded      []loadedSchema
	loadedBytes int
	marks       map[string][]mark
	notices     []contract.RunEvent
}

type loadedSchema struct {
	name   string
	schema *Value
}

type mark struct {
	receipt  string
	proposed bool
}

// NewSourceGate holds a run's declared sources; rows are the execution state's report rows, which
// it completes with the Runtime's row and each declared source's.
func NewSourceGate(declared *Declared, policy *Policy, marker, family string, rows func() *[]contract.ProvenanceSourceFigures) *SourceGate {
	g := &SourceGate{Entries: declared.Entries, sources: declared.Sources, policy: policy, marker: marker, family: family, rows: rows,
		changed: map[string]bool{}, marks: map[string][]mark{}}
	held := rows()
	want := []contract.ProvenanceSourceFigures{{Source: "runtime", Kind: "runtime"}}
	for _, source := range declared.Sources {
		want = append(want, contract.ProvenanceSourceFigures{Source: source.ID, Kind: source.Kind})
	}
	for _, row := range want {
		found := false
		for _, kept := range *held {
			if kept.Source == row.Source {
				found = true
			}
		}
		if !found {
			*held = append(*held, row)
		}
	}
	for _, withheld := range declared.Withheld {
		g.notices = append(g.notices, contract.RunEvent{Guardrail: &contract.RunEventGuardrail{Type: "guardrail", Tool: withheld.Tool, Action: "refused", Detail: withheld.Detail}})
	}
	return g
}

func (g *SourceGate) row(source string) *contract.ProvenanceSourceFigures {
	held := g.rows()
	for at := range *held {
		if (*held)[at].Source == source {
			return &(*held)[at]
		}
	}
	*held = append(*held, contract.ProvenanceSourceFigures{Source: source})
	return &(*held)[len(*held)-1]
}

func (g *SourceGate) mark(wire string, held mark) { g.marks[wire] = append(g.marks[wire], held) }

// refuse is a refusal that earns a guardrail: the notice, put on the stream before the call's
// result, and the permission failure the model reads.
func (g *SourceGate) refuse(tool, detail string) error {
	g.notices = append(g.notices, contract.RunEvent{Guardrail: &contract.RunEventGuardrail{Type: "guardrail", Tool: tool, Action: "refused", Detail: detail}})
	return failure.NewError(contract.Failure{Version: 1, Code: "permission", Source: "tool", Outcome: "not-started", Detail: &detail})
}

func (g *SourceGate) refused(entry *SourceEntry, err error) error {
	g.row(entry.Record.ID).Refused++
	g.mark(entry.Wire, mark{})
	return err
}

// TakeNotices are the guardrail events the source gate's refusals earned since the last take.
func (g *SourceGate) TakeNotices() []contract.RunEvent {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := g.notices
	g.notices = nil
	return out
}

// ResultLimit is the largest result of name the model is shown whole, where it is a tool with a
// source; zero otherwise.
func (g *SourceGate) ResultLimit(name string) int {
	if g.Entries[name] == nil {
		return 0
	}
	return min(g.policy.SourceResultBytes, g.policy.ToolResultBytes)
}

func (g *SourceGate) changedDetail(entry *SourceEntry) string {
	digest := ""
	if entry.Record.ToolListDigest != nil {
		digest = *entry.Record.ToolListDigest
	}
	return "the tool list " + entry.Record.ID + " serves is no longer the one the person approved (" + digest +
		"); its tools are refused until the person approves the new list, and this call was not run"
}

// Call is one call of a tool with a source, after the phase admitted it (SourceGate.call): refused,
// with the reason the model reads, where the source's tool list changed, the tool is catalogued and
// not loaded, the source has made SourceCallsPerTurn calls, or the call needs an approval the host's
// reader did not give; held for the review card where it is a proposal; otherwise run under the
// source's deadline and counted in the report.
func (g *SourceGate) Call(ctx context.Context, name string, args json.RawMessage, run func(context.Context, json.RawMessage) (ToolResult, error)) (ToolResult, error) {
	entry := g.Entries[name]
	if entry == nil {
		return run(ctx, args)
	}
	id := entry.Record.ID
	g.mu.Lock()
	row := g.row(id)
	switch {
	case g.changed[id]:
		err := g.refused(entry, g.refuse(name, g.changedDetail(entry)))
		g.mu.Unlock()
		return ToolResult{}, err
	case entry.Catalogued && !g.isLoaded(name):
		err := g.refused(entry, failure.ToolInput(name+" is catalogued: call "+LoadTool+" with its name first and call it with the schema that loads. This call was not run."))
		g.mu.Unlock()
		return ToolResult{}, err
	case int(row.Calls+row.Proposed) >= g.policy.SourceCallsPerTurn:
		limit := g.policy.SourceCallsPerTurn
		plural := "s"
		if limit == 1 {
			plural = ""
		}
		detail := fmt.Sprintf("This turn has called %s's tools %d time%s, the most one turn may (sourceCallsPerTurn); this call was not run. Use the results already obtained, or say what remains.", id, limit, plural)
		err := g.refused(entry, failure.NewError(contract.Failure{Version: 1, Code: "tool-failed", Source: "tool", Outcome: "not-started", Detail: &detail}))
		g.mu.Unlock()
		return ToolResult{}, err
	}
	g.mu.Unlock()
	if check := entry.Declared.Source.CurrentDigest; check != nil && entry.Record.Kind == "mcp" {
		now, err := check(ctx)
		if err != nil || entry.Record.ToolListDigest == nil || now != *entry.Record.ToolListDigest {
			g.mu.Lock()
			g.changed[id] = true
			err := g.refused(entry, g.refuse(name, g.changedDetail(entry)))
			g.mu.Unlock()
			return ToolResult{}, err
		}
	}
	if entry.EffectClass == "propose" {
		g.mu.Lock()
		g.row(id).Proposed++
		g.mark(name, mark{proposed: true})
		g.mu.Unlock()
		return ResultOf([]Content{TextContent(Proposed)}, nil), nil
	}
	if entry.Approval == "required" {
		approved := false
		if entry.Declared.Approve != nil {
			copied := append(json.RawMessage(nil), args...)
			held, err := entry.Declared.Approve(ctx, copied)
			approved = err == nil && held
		}
		if ctx.Err() != nil {
			return ToolResult{}, failure.Cancelled
		}
		if !approved {
			g.mu.Lock()
			err := g.refused(entry, g.refuse(name, name+" acts on "+id+" and needs the person's approval for each call, which this call did not get; it was not run and nothing was changed"))
			g.mu.Unlock()
			return ToolResult{}, err
		}
	}
	limit := time.Duration(min(g.policy.SourceCallMilliseconds, g.policy.ToolCallMilliseconds)) * time.Millisecond
	started := time.Now()
	result, err := withSourceDeadline(ctx, limit, func(bounded context.Context) (ToolResult, error) { return run(bounded, args) })
	g.mu.Lock()
	defer g.mu.Unlock()
	row = g.row(id)
	row.Calls++
	row.Milliseconds += time.Since(started).Milliseconds()
	if err != nil {
		g.mark(name, mark{})
		return ToolResult{}, err
	}
	receipt := result.Receipt
	result.Receipt = ""
	row.Bytes += int64(UTF8Bytes(string(result.JSON())))
	if U16Length(receipt) > 128 {
		receipt = ""
	}
	g.mark(name, mark{receipt: receipt})
	return result, nil
}

// withSourceDeadline is WithDeadline (adapter/turn) for the source's deadline: past it the call is
// answered as a timeout, the host's function handed the cancelled context.
func withSourceDeadline(ctx context.Context, limit time.Duration, work func(context.Context) (ToolResult, error)) (ToolResult, error) {
	if ctx.Err() != nil {
		return ToolResult{}, failure.Cancelled
	}
	bounded, stop := context.WithTimeout(ctx, limit)
	defer stop()
	held, err := work(bounded)
	if err != nil {
		if errors.Is(bounded.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
			detail := "The tool did not answer within " + limitText(limit) + " and is no longer waited for; it may still complete. Its outcome is unknown; check it before repeating the call."
			return ToolResult{}, failure.NewError(contract.Failure{Version: 1, Code: "timeout", Source: "tool", Outcome: "unknown", Detail: &detail})
		}
		if bounded.Err() != nil {
			return ToolResult{}, failure.Cancelled
		}
		return ToolResult{}, err
	}
	return held, nil
}

// limitText is limitText: milliseconds as minutes or seconds where they are whole.
func limitText(limit time.Duration) string {
	ms := limit.Milliseconds()
	count := func(n int64, unit string) string {
		text := jsNumber(float64(n)) + " " + unit
		if n != 1 {
			text += "s"
		}
		return text
	}
	if ms >= 60_000 && ms%60_000 == 0 {
		return count(ms/60_000, "minute")
	}
	if ms >= 1_000 && ms%1_000 == 0 {
		return count(ms/1_000, "second")
	}
	return count(ms, "millisecond")
}

// Runtime is one Runtime call, in the Runtime's row: a call the gate refused (its answer the
// refusal the model is told) is counted refused.
func (g *SourceGate) Runtime(call func() (ToolResult, error)) (ToolResult, error) {
	started := time.Now()
	result, err := call()
	g.mu.Lock()
	defer g.mu.Unlock()
	row := g.row("runtime")
	row.Milliseconds += time.Since(started).Milliseconds()
	switch {
	case err == nil && result.Err() && strings.HasPrefix(textOf(result), "refused: "):
		row.Refused++
	case err == nil:
		row.Calls++
		row.Bytes += int64(UTF8Bytes(string(result.JSON())))
	default:
		row.Calls++
	}
	return result, err
}

// RuntimeRefused counts a call of a name the run was not offered, refused at the gate before any
// Runtime call (the adapter's refusedUnoffered), in the Runtime's row.
func (g *SourceGate) RuntimeRefused() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.row("runtime").Refused++
}

func textOf(result ToolResult) string {
	var parts []string
	for _, part := range result.Content {
		if part.Type() == "text" {
			parts = append(parts, part.Text())
		}
	}
	return strings.Join(parts, "\n")
}

func (g *SourceGate) isLoaded(name string) bool {
	for _, held := range g.loaded {
		if held.name == name {
			return true
		}
	}
	return false
}

// LoadToolHost is load_tool, offered where the schema budget made a source a catalogue: one
// catalogued tool's schema per call.
func (g *SourceGate) LoadToolHost() HostTool {
	return HostTool{
		Name: LoadTool, Effect: "read", Description: LoadToolDescription, InputSchema: json.RawMessage(LoadToolSchema),
		Execute: func(ctx context.Context, args json.RawMessage) (ToolResult, error) {
			var asked struct {
				Name any `json:"name"`
			}
			_ = json.Unmarshal(args, &asked)
			name, _ := asked.Name.(string)
			entry := g.Entries[name]
			if entry == nil || !entry.Catalogued {
				shown := fmt.Sprint(asked.Name)
				if asked.Name == nil {
					shown = "undefined"
				}
				if U16Length(shown) > 64 {
					shown = U16Slice(shown, 0, 64)
				}
				return ToolResult{}, failure.ToolInput(LoadTool + " loads the schema of a catalogued tool, and " + QuoteJSON(shown) + " is not one; nothing was loaded.")
			}
			schema, err := g.shownSchema(entry.Declared.InputSchema)
			if err != nil {
				return ToolResult{}, err
			}
			g.mu.Lock()
			if !g.isLoaded(name) {
				bytes := UTF8Bytes(schema.Text())
				if g.loadedBytes+bytes > g.policy.OfferedSchemaBytes {
					g.mu.Unlock()
					detail := fmt.Sprintf("Loading %s would pass the %d bytes of schemas one response may load (offeredSchemaBytes); nothing was loaded. Use the tools already loaded.", name, g.policy.OfferedSchemaBytes)
					return ToolResult{}, failure.NewError(contract.Failure{Version: 1, Code: "tool-failed", Source: "tool", Outcome: "not-started", Detail: &detail})
				}
				g.loaded = append(g.loaded, loadedSchema{name: name, schema: schema})
				g.loadedBytes += bytes
			}
			g.mu.Unlock()
			record, err := json.Marshal(entry.Record)
			if err != nil {
				return ToolResult{}, err
			}
			source, err := ParseJSON(record)
			if err != nil {
				return ToolResult{}, err
			}
			return Answer(ObjectOf("name", String(name), "source", source, "description", String(entry.Description), "inputSchema", schema)), nil
		},
	}
}

// shownSchema is the schema as the run's family is shown it: on the Gemini family's wire, narrowed.
func (g *SourceGate) shownSchema(raw json.RawMessage) (*Value, error) {
	if g.family == "gemini" {
		narrowed, err := wire.Parse(raw)
		if err != nil {
			return nil, err
		}
		raw = wire.WithoutUnsupportedKeywords(narrowed).JSON()
	}
	return ParseJSON(raw)
}

// LoadedSection is the loaded schemas, as one data block for the execution state, or "".
func (g *SourceGate) LoadedSection() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.loaded) == 0 {
		return ""
	}
	list := Array()
	for _, held := range g.loaded {
		list.Append(ObjectOf("name", String(held.name), "inputSchema", held.schema))
	}
	return FrameBlock("the schemas "+LoadTool+" loaded", list.Text(), g.marker)
}

// Decorate is one event with the record its tool's source gives it: a tool_call's, a
// tool_result's with its receipt or its proposal.
func (g *SourceGate) Decorate(event contract.RunEvent) contract.RunEvent {
	switch {
	case event.ToolCall != nil:
		entry := g.Entries[event.ToolCall.Name]
		if entry == nil {
			return event
		}
		held := *event.ToolCall
		record := entry.Record
		held.Source = &record
		event.ToolCall = &held
	case event.ToolResult != nil:
		entry := g.Entries[event.ToolResult.Name]
		if entry == nil {
			return event
		}
		g.mu.Lock()
		var taken mark
		if queue := g.marks[event.ToolResult.Name]; len(queue) > 0 {
			taken = queue[0]
			g.marks[event.ToolResult.Name] = queue[1:]
		}
		g.mu.Unlock()
		held := *event.ToolResult
		record := entry.Record
		if taken.receipt != "" {
			receipt := taken.receipt
			record.Receipt = &receipt
		}
		held.Source = &record
		if taken.proposed {
			proposed := true
			held.Proposed = &proposed
		}
		event.ToolResult = &held
	}
	return event
}

// ValidSourceFigures are the run report's rows, read closed (validSourceFigures).
func ValidSourceFigures(rows *Value) bool {
	if !rows.IsArray() || rows.Len() > 64 {
		return false
	}
	for _, row := range rows.Items() {
		if !isObject(row) || !hasOnly(row, "source", "kind", "calls", "refused", "proposed", "bytes", "milliseconds") ||
			!row.Get("source").IsString() || !sourceID.MatchString(row.Get("source").Str()) ||
			!oneOf(row.Get("kind"), "runtime", "gateway", "mcp", "custom") {
			return false
		}
		for _, member := range []string{"calls", "refused", "proposed", "bytes", "milliseconds"} {
			if held := row.Get(member); !isInteger(held) || held.Num() < 0 {
				return false
			}
		}
	}
	return true
}
