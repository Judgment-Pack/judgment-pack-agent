package runcontext

// The critic's switch (ADR-0010; the port of packages/harness/criticSwitch.ts): whether the critic
// runs on a run, why, and at which tier, by one rule every adapter reads.
//
// The switch is three-valued, off | auto | on, set in three layers with one precedence: the run's
// word (Session.Critic, the person's per-run control) over the project's (Session.CriticProject) over
// the policy table's default (CriticDefault, auto). Under on the critic runs on every proposal; under
// off never; under auto the package decides from facts it already holds and never from the person's
// or the model's prose: the run produced a proposal, and declared cases exist, or the proposal changes
// an existing pack, or the model asked through the reserved tool request_review. A human reviewer
// assigned, a graph-purpose run, Plan mode and a delegated child turn the critic off whatever the
// mode, each with its named reason. The decision is emitted once per run (critic_decided) and written
// into the checkpoint (critic). The three rules of the refutation pass (go/adapter/turn) do not
// change: the switch decides only whether the pass runs.
//
// AdversarialReview, the boolean before ADR-0010, is read for one release as an alias of the layer it
// sits in: on the session of the run's word, in the table of CriticDefault; true is on, false is off,
// and the new member wins where both are set.

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
)

// CriticModes are the switch's three values (tool-offer.json#/$defs/criticMode).
var CriticModes = []string{"off", "auto", "on"}

// CriticReasons are why the critic ran or did not (provenance.json#/$defs/criticReason), one enum.
var CriticReasons = []string{"person", "project", "table", "model-asked", "cases", "changes-existing", "no-proposal", "reviewer-assigned", "purpose", "phase", "child", "budget", "off"}

// ModelTiers are the model capability's thinking tiers and AgentTiers the agent capability's
// efforts, each lowest first (tool-offer.json#/$defs/criticReasoning).
var (
	ModelTiers = []string{"off", "low", "on", "medium", "ultra", "high"}
	AgentTiers = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}
)

// RequestReview is the reserved name the model asks for a review with
// (tool-offer.json#/$defs/requestReview).
const RequestReview = "request_review"

// CriticDecision is the decision for one run (provenance.json#/$defs/criticDecision): the report's
// row. Reasoning is the critic's tier, empty where the run states none; Trigger, under auto, the
// trigger that held, empty otherwise.
type CriticDecision struct {
	Mode      string
	Ran       bool
	Reason    string
	Reasoning string
	Trigger   string
}

// CriticSettings are what the session says about the critic: the run's and the project's words, the
// tier, and the alias.
type CriticSettings struct {
	ResolvedPolicy    PolicyMembers
	Critic            string
	CriticProject     string
	CriticReasoning   string
	AdversarialReview *bool
}

// CriticTable is what the policy table says: the default, the tier floor, and the alias.
type CriticTable struct {
	CriticDefault        string
	CriticReasoningFloor string
	AdversarialReview    *bool
}

// CriticForcedFacts are the facts that turn the critic off whatever the mode.
type CriticForcedFacts struct {
	// Child is a delegated child's run (its objective is set, agent#11).
	Child            bool
	Phase            string
	Purpose          string
	ReviewerAssigned bool
}

// CriticRunFacts are the run's own facts at the point the critic would start.
type CriticRunFacts struct {
	// Proposal: the run produced a proposal; there is something to refute.
	Proposal bool
	// Cases: the host declared cases for the critic to compare.
	Cases bool
	// ChangesExisting: the proposal changes a pack that already exists (the host's ExistingPack).
	ChangesExisting bool
	// ModelAsked: the model called request_review this run (the checkpoint's critic carries it).
	ModelAsked bool
	// Room: the allowance, or on the agent capability the invocations, leave the critic room.
	Room bool
}

func criticAlias(value *bool) string {
	if value == nil {
		return ""
	}
	if *value {
		return "on"
	}
	return "off"
}

// CriticMode is criticMode: the precedence run > project > table, in one place. The run's word
// (Critic, else the alias AdversarialReview), else the project's, else the table's default
// (CriticDefault, else the table's alias), else auto. It answers the mode and the layer that set it.
func CriticMode(session CriticSettings, table CriticTable) (mode, layer string) {
	members := session.ResolvedPolicy
	if members == nil {
		run, project := PolicyLayer{}, PolicyLayer{}
		mode := session.Critic
		if mode == "" {
			mode = criticAlias(session.AdversarialReview)
		}
		if mode != "" {
			run["critic"] = map[string]any{"default": mode}
		}
		if session.CriticProject != "" {
			project["critic"] = map[string]any{"default": session.CriticProject}
		}
		mode = table.CriticDefault
		if !slices.Contains(CriticModes, mode) {
			mode = criticAlias(table.AdversarialReview)
		}
		if mode == "" {
			mode = "auto"
		}
		resolved, err := ResolvePolicy(run, project, PolicyLayer{"critic": map[string]any{"default": mode}, "models": map[string]any{"author": map[string]any{"model": "current", "reasoning": "off"}}}, PolicyHost{Connections: []string{"current"}, Requests: 16})
		if err != nil {
			return "auto", "table"
		}
		members = resolved.Members
	}
	held := members["critic.default"]
	mode, _ = held.Value.(string)
	layer = held.Layer
	if layer == "run" {
		layer = "person"
	}
	return mode, layer
}

// CriticForcedOff is criticForcedOff: the forced-off reason that holds, in the order child, phase,
// purpose, reviewer-assigned; or "".
func CriticForcedOff(facts CriticForcedFacts) string {
	switch {
	case facts.Child:
		return "child"
	case facts.Phase == "plan":
		return "phase"
	case facts.Purpose == "graph":
		return "purpose"
	case facts.ReviewerAssigned:
		return "reviewer-assigned"
	}
	return ""
}

// DecideCritic is criticDecision: the decision every adapter reads (it replaces CriticRuns). From the
// session's words, the table, the forced-off facts and the run's facts it answers the mode, whether
// the critic runs, the reason and, under auto, the trigger. Nothing here reads prose.
func DecideCritic(session CriticSettings, table CriticTable, forced CriticForcedFacts, run CriticRunFacts) CriticDecision {
	mode, layer := CriticMode(session, table)
	if off := CriticForcedOff(forced); off != "" {
		return CriticDecision{Mode: mode, Reason: off}
	}
	if mode == "off" {
		return CriticDecision{Mode: mode, Reason: "off"}
	}
	if !run.Proposal {
		return CriticDecision{Mode: mode, Reason: "no-proposal"}
	}
	trigger := ""
	if mode == "auto" {
		switch {
		case run.ModelAsked:
			trigger = "model-asked"
		case run.Cases:
			trigger = "cases"
		case run.ChangesExisting:
			trigger = "changes-existing"
		default:
			return CriticDecision{Mode: mode, Reason: layer}
		}
	}
	reason := layer
	if trigger != "" {
		reason = trigger
	}
	if !run.Room {
		return CriticDecision{Mode: mode, Reason: "budget", Trigger: trigger}
	}
	return CriticDecision{Mode: mode, Ran: true, Reason: reason, Trigger: trigger}
}

// CriticTier is criticTier: the critic's tier, at least the author's unless the host says otherwise.
// The host's CriticReasoning where it names one, honoured as it is, lower than the author's included;
// else the floor, author: the author's own tier on the run's capability. "" where neither is stated.
func CriticTier(author, asked, floor string) string {
	if asked != "" {
		return asked
	}
	if floor == "" || floor == "author" {
		return author
	}
	return ""
}

// TierFits is whether a tier is in the enum of the run's capability.
func TierFits(tier string, agent bool) bool {
	if agent {
		return slices.Contains(AgentTiers, tier)
	}
	return slices.Contains(ModelTiers, tier)
}

// ReadCriticSettings is readCriticSettings: the session's critic members, read closed before any
// request: each mode one of the three, the tier one of its capability's enum. It answers the sentence
// a configuration failure carries.
func ReadCriticSettings(session CriticSettings, agent bool) error {
	for _, member := range []struct{ name, value string }{{"critic", session.Critic}, {"criticProject", session.CriticProject}} {
		if member.value != "" && !slices.Contains(CriticModes, member.value) {
			return errors.New("The session's " + member.name + " must be off, auto or on.")
		}
	}
	if session.CriticReasoning != "" && !TierFits(session.CriticReasoning, agent) {
		tiers := ModelTiers
		if agent {
			tiers = AgentTiers
		}
		return errors.New("The session's criticReasoning must be one of " + strings.Join(tiers, ", ") + " on this capability.")
	}
	return nil
}

// The tool's description and input schema, the contract's (tool-offer.json#/$defs/requestReview), and
// what the model reads where its ask is taken.
const (
	RequestReviewDescription = "Ask for an independent review of the proposal this response makes. Call it at most once, before you propose, where a second check would help: a critic then checks the proposal with the runtime’s own tools before it is shown. Its findings come from the runtime’s results; your reason is not shown to it."
	RequestReviewSchema      = `{"type":"object","properties":{"reason":{"type":"string","description":"Why a review of this run’s proposal is wanted, in one sentence."}}}`
	ReviewTaken              = "A review was asked for: the critic will check the proposal this response makes before it is shown. Its verdict comes from the runtime’s results."
)

// ReviewRefusal is reviewRefusal: why an ask was refused, the guardrail's detail and the failed
// result's text (reason review refused).
func ReviewRefusal(why string) string {
	switch why {
	case "asked":
		return RequestReview + " was already called in this response, and a response asks for one review; nothing more was recorded."
	case "off":
		return RequestReview + " is refused: the critic is off for this run, and the model's ask cannot turn it on; nothing was recorded."
	case "on":
		return RequestReview + " is not needed: the critic is on for this run and checks every proposal; nothing was recorded."
	case "outside":
		return RequestReview + " is offered only to the author's run, not to the critic or the reformat run; nothing was recorded."
	case "child":
		return RequestReview + " is not offered to a delegated child; nothing was recorded."
	case "phase":
		return RequestReview + " is not offered in Plan mode, where no critic runs; nothing was recorded."
	case "purpose":
		return RequestReview + " is not offered on a graph-purpose run, where no critic runs; nothing was recorded."
	default:
		return RequestReview + " is not offered where a reviewer is assigned, where no critic runs; nothing was recorded."
	}
}

// CriticSwitch is one run's switch, held by the shared boundary: the mode and the layer it came from,
// the forced-off reason, the ask (the checkpoint's critic) and the decision, written once. The
// adapters read Decide at the point the critic would start; the boundary offers request_review where
// Offered says so, and the delegation coordinator holds the critic's invocation back where Reserved
// says so.
type CriticSwitch struct {
	Mode   string
	Layer  string
	Forced string

	// mu serialises admission, checkpoint access and refusal queue append/drain (#113).
	mu       sync.Mutex
	settings CriticSettings
	table    CriticTable
	facts    CriticForcedFacts
	state    func() *Execution
	notices  []contract.RunEvent
}

// NewCriticSwitch holds one run's switch over the execution state it writes its member into.
func NewCriticSwitch(settings CriticSettings, table CriticTable, forced CriticForcedFacts, state func() *Execution) *CriticSwitch {
	mode, layer := CriticMode(settings, table)
	return &CriticSwitch{Mode: mode, Layer: layer, Forced: CriticForcedOff(forced), settings: settings, table: table, facts: forced, state: state}
}

// Offered is whether request_review is offered: under auto, on a run nothing forces the critic off.
func (s *CriticSwitch) Offered() bool { return s.Forced == "" && s.Mode == "auto" }

// Reserved is whether one agent invocation is held back for the critic: under auto and on, where
// nothing forces it off.
func (s *CriticSwitch) Reserved() bool { return s.Forced == "" && s.Mode != "off" }

// Asked is whether the model's ask was taken this run, a restored one's included.
func (s *CriticSwitch) Asked() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.askedLocked()
}

// askedLocked reads the checkpoint while mu is held.
func (s *CriticSwitch) askedLocked() bool {
	held := s.state().Critic
	return held != nil && held.Trigger != nil && *held.Trigger == "model-asked"
}

// Ask is the model's ask: the refusal's sentence where it is refused (review refused), or "" where
// it is taken and recorded. authorPhase is false outside the author's run.
func (s *CriticSwitch) Ask(authorPhase bool) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.Forced != "":
		return ReviewRefusal(s.Forced)
	case s.Mode != "auto":
		return ReviewRefusal(s.Mode)
	case !authorPhase:
		return ReviewRefusal("outside")
	case s.askedLocked():
		return ReviewRefusal("asked")
	}
	trigger := "model-asked"
	s.state().Critic = &contract.CheckpointCritic{Mode: s.Mode, Trigger: &trigger}
	return ""
}

// Refuse is the guardrail a refused ask earns, put on the stream before the call's result, and the
// failure the model reads: tool-failed, not-started.
func (s *CriticSwitch) Refuse(detail string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	reason := "review refused"
	s.notices = append(s.notices, contract.RunEvent{Guardrail: &contract.RunEventGuardrail{Type: "guardrail", Tool: RequestReview, Action: "refused", Detail: detail, Reason: &reason}})
	return failure.NewError(contract.Failure{Version: 1, Code: "tool-failed", Source: "tool", Outcome: "not-started", Detail: &detail})
}

// TakeNotices are the guardrail events refused asks earned since the last take.
func (s *CriticSwitch) TakeNotices() []contract.RunEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.notices
	s.notices = nil
	return out
}

// Decide is the decision, once the run's facts are known: written into the checkpoint's critic (over
// a carried ask) and answered with the tier, for the adapter to emit as critic_decided.
func (s *CriticSwitch) Decide(run CriticRunFacts, author string) CriticDecision {
	s.mu.Lock()
	defer s.mu.Unlock()
	run.ModelAsked = s.askedLocked()
	decision := DecideCritic(s.settings, s.table, s.facts, run)
	decision.Reasoning = CriticTier(author, s.settings.CriticReasoning, s.table.CriticReasoningFloor)
	s.state().Critic = decision.Record()
	return decision
}

func optional(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// Record is the decision as the checkpoint keeps it.
func (d CriticDecision) Record() *contract.CheckpointCritic {
	ran, reason := d.Ran, d.Reason
	return &contract.CheckpointCritic{Mode: d.Mode, Ran: &ran, Reason: &reason, Reasoning: optional(d.Reasoning), Trigger: optional(d.Trigger)}
}

// CriticDecidedEvent is criticDecidedEvent: the contract's event, built in one place from a decision.
func CriticDecidedEvent(d CriticDecision) contract.RunEvent {
	return contract.RunEvent{CriticDecided: &contract.RunEventCriticDecided{Type: "critic_decided", Mode: d.Mode, Ran: d.Ran, Reason: d.Reason, Reasoning: optional(d.Reasoning), Trigger: optional(d.Trigger)}}
}

// ValidCriticRecord is validCriticRecord: the checkpoint's member, read closed (the generated
// decoder refuses an unnamed member; this holds the values and the pairing of ran and reason).
func ValidCriticRecord(record *contract.CheckpointCritic) bool {
	if record == nil {
		return true
	}
	if !slices.Contains(CriticModes, record.Mode) || (record.Ran == nil) != (record.Reason == nil) {
		return false
	}
	if record.Reason != nil && !slices.Contains(CriticReasons, *record.Reason) {
		return false
	}
	if record.Reasoning != nil && !slices.Contains(ModelTiers, *record.Reasoning) && !slices.Contains(AgentTiers, *record.Reasoning) {
		return false
	}
	return record.Trigger == nil || slices.Contains([]string{"model-asked", "cases", "changes-existing"}, *record.Trigger)
}

// ReviewHost is request_review as the boundary offers it: the first call of the run is recorded (the
// checkpoint's critic), any other is refused with the guardrail review refused. authorPhase says
// whether the run is in the author's phase when the call is made.
func (s *CriticSwitch) ReviewHost(authorPhase func() bool) HostTool {
	return HostTool{Name: RequestReview, Effect: "plan", Description: RequestReviewDescription, InputSchema: json.RawMessage(RequestReviewSchema),
		Execute: func(_ context.Context, args json.RawMessage) (ToolResult, error) {
			var asked map[string]json.RawMessage
			if err := json.Unmarshal(nonEmptyArgs(args), &asked); err != nil {
				return ToolResult{}, failure.ToolInput(RequestReview + " takes one optional reason, a string; nothing was recorded.")
			}
			if reason, ok := asked["reason"]; ok {
				var text string
				if json.Unmarshal(reason, &text) != nil {
					return ToolResult{}, failure.ToolInput(RequestReview + " takes one optional reason, a string; nothing was recorded.")
				}
			}
			if refused := s.Ask(authorPhase()); refused != "" {
				return ToolResult{}, s.Refuse(refused)
			}
			value, _ := ParseJSON([]byte(`{"asked":true,"note":` + QuoteJSON(ReviewTaken) + `}`))
			return Answer(value), nil
		}}
}

// OfferedReviewTool is the declaration a Go host opens the gate with for request_review (ADR-0010):
// one where the session's switch offers it (the mode auto, and nothing forcing the critic off), none
// otherwise. The boundary adds the tool itself (ContextualSession); a host that opens the gate with
// its offer adds this beside the context tools, as it adds OfferedSkillTools'.
func OfferedReviewTool(session Session, policy Policy) []contract.ToolOfferHostTool {
	forced := CriticForcedFacts{Purpose: session.Purpose, ReviewerAssigned: session.ReviewerAssigned}
	if session.Context != nil {
		forced.Child = session.Context.Objective != ""
		forced.Phase = session.Context.Phase
	}
	settings := CriticSettings{Critic: session.Critic, CriticProject: session.CriticProject, CriticReasoning: session.CriticReasoning, AdversarialReview: session.AdversarialReview}
	table := CriticTable{CriticDefault: policy.CriticDefault, CriticReasoningFloor: policy.CriticReasoningFloor, AdversarialReview: policy.AdversarialReview}
	held := NewCriticSwitch(settings, table, forced, func() *Execution { return &Execution{} })
	if !held.Offered() {
		return nil
	}
	plan := "plan"
	return []contract.ToolOfferHostTool{{Name: RequestReview, Description: RequestReviewDescription, InputSchema: json.RawMessage(RequestReviewSchema), Effect: &plan}}
}
