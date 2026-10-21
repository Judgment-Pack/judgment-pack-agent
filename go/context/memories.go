package runcontext

// ADR-0014: the package records proposals and recall; the host owns acceptance and storage.
import (
	"context"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

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
func memoryDate(s string) bool { _, err := time.Parse(time.RFC3339Nano, s); return err == nil }
func validMemory(m contract.Memory) bool {
	return len(m.ID) > 0 && len(m.ID) <= 100 && memoryKind(m.Kind) && m.Text != "" && len(m.Source.Run) > 0 && len(m.Source.Run) <= 100 && (m.Source.ProposedBy == "model" || m.Source.ProposedBy == "person" || m.Source.ProposedBy == "host") && (m.Scope == "project" || m.Scope == "person") && digestPattern.MatchString(m.Digest) && memoryDate(m.CreatedAt) && (m.ExpiresAt == nil || memoryDate(*m.ExpiresAt)) && memoryState(m.State)
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
		if m.ID == "" || len(m.ID) > 100 || !digestPattern.MatchString(m.Digest) || !memoryKind(m.Kind) || !memoryState(m.State) || ids[m.ID] {
			return false
		}
		ids[m.ID] = true
	}
	return true
}

type MemoryRecall struct {
	Text    string
	Rows    []contract.ProvenanceMemoryRecall
	Records []contract.Memory
	Bytes   int
}

func RecallMemory(value any, bound int, marker string) (MemoryRecall, error) {
	out := MemoryRecall{Rows: []contract.ProvenanceMemoryRecall{}}
	fail := func(detail string) (MemoryRecall, error) {
		return MemoryRecall{}, failure.Authored("configuration", "The host's memoryFor answer "+detail+"; nothing was sent.", "context")
	}
	if value == nil {
		return out, nil
	}
	if text, ok := value.(string); ok {
		if len(text) > bound {
			return fail("is over memoryBytes (" + strconv.Itoa(bound) + ")")
		}
		out.Bytes = len(text)
		if text != "" {
			out.Text = FrameBlock("memory kind fact source host (legacy text alias)", text, marker)
		}
		return out, nil
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
			expiry, _ := time.Parse(time.RFC3339Nano, *m.ExpiresAt)
			if !expiry.After(time.Now()) {
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
	if c.Execution.State.Memories != nil {
		raw, _ := json.Marshal(c.Execution.State.Memories)
		_ = json.Unmarshal(raw, &g.book)
	}
	c.Memories = g
	c.Execution.MemoryRows = g.Snapshot
}
func (g *MemoryGate) Snapshot() *contract.ProvenanceMemories {
	g.mu.Lock()
	defer g.mu.Unlock()
	var copy contract.ProvenanceMemories
	raw, _ := json.Marshal(g.book)
	_ = json.Unmarshal(raw, &copy)
	return &copy
}
func (g *MemoryGate) TakeNotices() []contract.RunEvent {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := g.notices
	g.notices = nil
	return out
}
func (g *MemoryGate) RecordRecall(recall MemoryRecall) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	for at, previous := range g.book.Recalled {
		for _, current := range recall.Records {
			if previous.ID == current.ID {
				if current.State != "accepted" {
					g.book.Recalled[at].State = current.State
				}
				if current.State == "accepted" && current.ExpiresAt != nil {
					expiry, _ := time.Parse(time.RFC3339Nano, *current.ExpiresAt)
					if !expiry.After(time.Now()) {
						g.book.Recalled[at].State = "expired"
					}
				}
			}
		}
	}
	for _, row := range recall.Rows {
		found := false
		for at, previous := range g.book.Recalled {
			if row.ID == previous.ID {
				g.book.Recalled[at] = row
				found = true
				break
			}
		}
		if !found {
			g.book.Recalled = append(g.book.Recalled, row)
		}
	}
	if len(g.book.Recalled) > 256 {
		return failure.Authored("configuration", "The recalled memory report is full.", "context")
	}
	if recall.Bytes > 0 || len(recall.Rows) > 0 {
		ids := []string{}
		digests := []string{}
		for _, row := range recall.Rows {
			ids = append(ids, row.ID)
			digests = append(digests, row.Digest)
		}
		g.notices = append(g.notices, contract.RunEvent{MemoryRecalled: &contract.RunEventMemoryRecalled{Type: "memory_recalled", Ids: ids, Digests: digests, Bytes: int64(recall.Bytes)}})
	}
	return nil
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
	defer g.mu.Unlock()
	detail := "Only the author may propose memory."
	_ = g.refuse(detail)
	return detail
}
func (g *MemoryGate) Tool() HostTool {
	return HostTool{Name: Remember, Description: "Propose a memory for the person to accept or decline. This records a proposal only; nothing is stored or accepted.", InputSchema: json.RawMessage(RememberSchema), Effect: "plan", Execute: func(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
		g.mu.Lock()
		defer g.mu.Unlock()
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
		m := contract.Memory{ID: DataMarker(), Kind: args.Get("kind").Str(), Text: args.Get("text").Str(), Source: contract.MemorySource{Run: g.session.Execution.State.ID, ProposedBy: "model"}, Scope: "project", Digest: "sha256:" + ContextDigest(args.Get("text").Str()), CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), State: "proposed"}
		g.book.Proposed = append(g.book.Proposed, m)
		g.notices = append(g.notices, contract.RunEvent{MemoryProposed: &contract.RunEventMemoryProposed{Type: "memory_proposed", Memory: m}})
		return ToolResult{Content: []Content{TextContent(`{"proposed":` + QuoteJSON(m.ID) + `,"state":"proposed"}`)}}, nil
	}}
}
