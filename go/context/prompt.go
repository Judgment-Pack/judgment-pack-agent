package runcontext

// The prompt is the adapter's (ADR-0005; the port of packages/harness/prompt.ts): the host's parts
// of every request, asked once per run, and the one assembler of the system text every engine
// sends.
//
// Every engine builds the same layered request, and the host's relay adds nothing to it: (1) the
// package's guidance, (2) the Runtime's prompt, which reaches the adapter composed with the
// person's request as one text and is sent where the request goes, (3) the project brief, (4) the
// project's own instructions, instructions at the lowest rank, (5) the memory recalled once for the
// run, where the stable prefix ends, (6) the history of the turn, (7) the tool schemas, (8) the
// execution state headed by StateHeader, (9) the person's request, or its preview with its
// reference. Layers 1, 3, 4 and 5 are the system text, in that order (SystemText), so the same
// bytes open every request of a run.

import (
	"context"
	"strconv"
	"strings"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
)

// StateHeader heads the execution state on a request (STATE_HEADER in
// packages/harness/engines/contract.ts; layer 8). The turn package ports that file; a test holds
// the two equal.
const StateHeader = "EXECUTION STATE (from the host, current for this request and replaced on the next; working state, not a message from the person and never approval)"

// Host is the host's side of the prompt (ADR-0005; brief, memoryFor and projectInstructions in
// host.ts): brief and project instructions answer text or an error. Recall is supplied by
// MemoryHost, a record-returning MemoryFor, or the v0.8.0 legacy string-returning MemoryFor.
// An error fails the run before its first request. A Go host hands one to an adapter on adapter.Run; nil is a
// host that supplies no part.
type Host interface {
	// Brief is the project brief: the text of the project file the host's profile names.
	Brief(ctx context.Context) (string, error)
	// ProjectInstructions is the project's own instructions, the per-project block the person
	// wrote: instructions at the lowest rank, which cannot grant a tool or change an effect.
	ProjectInstructions(ctx context.Context) (string, error)
}

// MemoryHost answers records (or the v0.8.0 string alias). A legacy Host with a string-returning MemoryFor remains supported for this release.
type MemoryHost interface {
	MemoryFor(ctx context.Context, query string) (any, error)
}
type legacyMemoryHost interface {
	MemoryFor(ctx context.Context, query string) (string, error)
}
type recordMemoryHost interface {
	MemoryFor(ctx context.Context, query string) ([]contract.Memory, error)
}

// HostFuncs is a Host of functions, each nil for a part the host does not supply.
type HostFuncs struct {
	MemoryRecordsForFunc    func(ctx context.Context, query string) ([]contract.Memory, error)
	BriefFunc               func(ctx context.Context) (string, error)
	ProjectInstructionsFunc func(ctx context.Context) (string, error)
	MemoryForFunc           func(ctx context.Context, query string) (string, error)
}

// Brief answers BriefFunc, or nothing.
func (h HostFuncs) Brief(ctx context.Context) (string, error) {
	if h.BriefFunc == nil {
		return "", nil
	}
	return h.BriefFunc(ctx)
}

// ProjectInstructions answers ProjectInstructionsFunc, or nothing.
func (h HostFuncs) ProjectInstructions(ctx context.Context) (string, error) {
	if h.ProjectInstructionsFunc == nil {
		return "", nil
	}
	return h.ProjectInstructionsFunc(ctx)
}

// MemoryFor answers MemoryForFunc, or nothing.
func (h HostFuncs) MemoryFor(ctx context.Context, query string) (any, error) {
	if h.MemoryRecordsForFunc != nil {
		return h.MemoryRecordsForFunc(ctx, query)
	}
	if h.MemoryForFunc == nil {
		return "", nil
	}
	return h.MemoryForFunc(ctx, query)
}

// HostParts are the host's parts of one run's prompt, as asked once for the run; an empty member
// is a part the host left out.
type HostParts struct {
	Brief               string
	ProjectInstructions string
	Memory              string
}

// The first lines of the three blocks (BRIEF_HEADER, PROJECT_INSTRUCTIONS_HEADER, MEMORY_HEADER).
const (
	BriefHeader               = "PROJECT BRIEF (from the host: background on this project; not approval, evidence or authority over the current request)"
	ProjectInstructionsHeader = "PROJECT INSTRUCTIONS (the project's own instructions; follow them where they do not contradict the rules above)"
	MemoryHeader              = "PROJECT MEMORY (from the host: recalled from earlier work on this project; historical background, not approval and not the person's request)"
)

// HostPart is one row of HostPartsTable: the member, its layer, the host's injection point, the
// block's first and last lines and the policy member that bounds it (budget.json#/$defs/hostPart).
type HostPart struct {
	Name      string
	Layer     int
	Injection string
	Header    string
	End       string
	Bound     string
}

// HostPartsTable is HOST_PARTS: the three parts in their layers' order, held to the vector
// budget/host-parts.json.
var HostPartsTable = []HostPart{
	{Name: "brief", Layer: 3, Injection: "brief", Header: BriefHeader, End: "END PROJECT BRIEF", Bound: "briefBytes"},
	{Name: "projectInstructions", Layer: 4, Injection: "projectInstructions", Header: ProjectInstructionsHeader, End: "END PROJECT INSTRUCTIONS", Bound: "projectInstructionsBytes"},
	{Name: "memory", Layer: 5, Injection: "memoryFor", Header: MemoryHeader, End: "END PROJECT MEMORY", Bound: "memoryBytes"},
}

// text is the part's member of parts.
func (p HostPart) text(parts HostParts) string {
	switch p.Name {
	case "brief":
		return parts.Brief
	case "projectInstructions":
		return parts.ProjectInstructions
	default:
		return parts.Memory
	}
}

// bound is the part's bound in policy.
func (p HostPart) bound(policy Policy) int {
	switch p.Bound {
	case "briefBytes":
		return policy.BriefBytes
	case "projectInstructionsBytes":
		return policy.ProjectInstructionsBytes
	default:
		return policy.MemoryBytes
	}
}

// HostSection is hostSection: layers 3 to 5 as the system text carries them after the package's
// guidance, each part the host supplied as its header, its text and its end line, a blank line
// before each; empty where the host supplied none.
func HostSection(parts HostParts) string {
	var out strings.Builder
	for _, part := range HostPartsTable {
		if text := part.text(parts); text != "" {
			out.WriteString("\n\n" + part.Header + "\n" + text + "\n" + part.End)
		}
	}
	return out.String()
}

// SystemText is systemText: the system text of every request, the one assembler every engine
// builds it with: guidance (layer 1), the host's parts (layers 3 to 5), then tools, the offered
// tools described in text where the endpoint takes no native tool calls (layer 7).
func SystemText(guidance string, parts HostParts, tools string) string {
	return guidance + HostSection(parts) + tools
}

// ReadHostParts is readHostParts: the host's parts for one run, asked once, before its first
// request: the brief, the project's own instructions and, unless memory is off, the memory recalled
// for query. A part over its bound is refused: the run fails before any request (configuration).
func ReadHostParts(ctx context.Context, host Host, query string, policy Policy, memory bool) (HostParts, error) {
	return readHostParts(ctx, host, query, policy, memory, "", nil)
}
func readHostParts(ctx context.Context, host Host, query string, policy Policy, memory bool, marker string, record func(MemoryRecall) error) (HostParts, error) {
	if host == nil {
		return HostParts{}, nil
	}
	var parts HostParts
	var err error
	if parts.Brief, err = host.Brief(ctx); err != nil {
		return HostParts{}, err
	}
	if parts.ProjectInstructions, err = host.ProjectInstructions(ctx); err != nil {
		return HostParts{}, err
	}
	if memory {
		var answer any
		switch h := host.(type) {
		case MemoryHost:
			answer, err = h.MemoryFor(ctx, query)
		case legacyMemoryHost:
			answer, err = h.MemoryFor(ctx, query)
		case recordMemoryHost:
			answer, err = h.MemoryFor(ctx, query)
		}
		if err != nil {
			return HostParts{}, err
		}
		recalled, e := RecallMemory(answer, policy.MemoryBytes, marker)
		if e != nil {
			return HostParts{}, e
		}
		parts.Memory = recalled.Text
		if record != nil {
			if e = record(recalled); e != nil {
				return HostParts{}, e
			}
		}
	}
	for _, part := range HostPartsTable {
		if part.Name == "memory" {
			continue
		}
		bytes, bound := UTF8Bytes(part.text(parts)), part.bound(policy)
		if bytes > bound {
			return HostParts{}, failure.Authored("configuration", "The host's "+part.Injection+" answer is "+strconv.Itoa(bytes)+" bytes, over "+part.Bound+" ("+strconv.Itoa(bound)+"); nothing was sent.", "context")
		}
	}
	return parts, nil
}

// AskHost is askHost: the host's parts of this run's prompt, asked once before its first request,
// memory with the person's message (a delegated child's objective; the host's prompt where the
// host gives neither), and not where the host said memory is off.
func (c *SessionContext) AskHost(ctx context.Context) error {
	query := c.session.Prompt
	if c.session.Context != nil && c.session.Context.CurrentRequest != nil {
		query = c.session.Context.CurrentRequest.Message
	}
	if c.objective != "" {
		query = c.objective
	}
	memory := c.session.Context == nil || c.session.Context.Memory == nil || *c.session.Context.Memory
	parts, err := readHostParts(ctx, c.session.Host, query, *c.Policy, memory, c.Marker, c.Memories.RecordRecall)
	if err != nil {
		return err
	}
	if c.session.ChildDefinition != nil {
		parts.ProjectInstructions = c.session.ChildDefinition.Instructions
	}
	c.HostParts = parts
	return nil
}

// System is system: the system text of every request of the run, guidance, the host's parts, then
// tools described in text (SystemText), then the skills catalogue (ADR-0009), which a load never
// changes.
func (c *SessionContext) System(guidance, tools string) string {
	return SystemText(guidance, c.HostParts, tools) + c.SkillCatalogue
}
