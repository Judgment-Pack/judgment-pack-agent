package gate

import "github.com/Judgment-Pack/judgment-pack-agent/go/contract"

// CriticTools are the critic's Runtime tools (CRITIC_TOOLS in refutation.ts, agent#10) and
// CriticReaders the harness's two context readers it may also call (CRITIC_READERS): the whole of
// what the critic may call. contract/vectors/tool-offer/critic-tools.json.
var (
	CriticTools   = []string{"validate", "experimental_evaluate", "get_schema", "experimental_test_cases"}
	CriticReaders = []string{"read_window", "search"}
)

// PlanRuntimeReads are the Runtime tools Plan mode admits (PLAN_RUNTIME_READS in
// planning/policy.ts, agent#13): the ceiling less experimental_evaluate and experimental_test_cases.
var PlanRuntimeReads = func() []string {
	var reads []string
	for _, name := range Ceiling {
		if name != RehearsalTool && name != TestTool {
			reads = append(reads, name)
		}
	}
	return reads
}()

// ContextTools are the names the host keeps for its own context tools (tool-offer.json
// $defs/reservedName): the boundary adds them to a run in every phase but the critic's (which is
// offered the two readers) and the reformat run's (which is offered none).
var ContextTools = []string{"list_context", "read_context", "read_window", "search", "update_plan", "request_budget_increase"}

// SkillTools are the skill loaders (ADR-0009): load_skill and unload_skill, which the boundary offers
// where the run catalogues a skill, in every phase but a child's, the critic's included (a skill
// grants no tool).
var SkillTools = []string{"load_skill", "unload_skill"}

// ReservedNames are every name the host keeps for the package's own tools (tool-offer.json
// $defs/reservedName): the context tools, load_tool (ADR-0006), the catalogue loader the boundary
// offers beside them only where the schema budget makes a tool source a catalogue, the skill
// loaders (ADR-0009), and request_review (ADR-0010).
var ReservedNames = append(append(append(append([]string{}, ContextTools...), "load_tool"), SkillTools...), ReviewTool, "remember", "propose_edit", "propose_delete", "propose_insert", "undo_edit")

// ReviewTool is request_review (ADR-0010): the model's ask for a review, which the boundary offers in
// the author's phase where the critic's mode is auto; never to the critic, a child, Plan mode or the
// reformat run, whose phases do not admit it.
const ReviewTool = "request_review"

// PhaseKind names a phase of a run.
type PhaseKind string

// The phases a run moves through.
const (
	PhaseLoop     PhaseKind = "loop"
	PhaseCritic   PhaseKind = "critic"
	PhasePlan     PhaseKind = "plan"
	PhaseReformat PhaseKind = "reformat"
	PhaseChild    PhaseKind = "child"
)

// Phase is what one phase of a run is offered, of the session's tools: its Runtime tools and its
// host tools, each in the session's order. A name a phase did not offer is refused before any
// call (Gate.CallTool, Gate.AdmitHost).
type Phase struct {
	Kind PhaseKind
	// Profile is the delegation profile's id, for a child.
	Profile string
	Runtime []string
	Host    []string
}

// Offer is the phase's share of an offer: the tools it names, as offered, in the offer's order.
func (p Phase) Offer(offer contract.ToolOffer) contract.ToolOffer {
	out := contract.ToolOffer{Tools: []contract.ToolOfferRuntimeTool{}, HostTools: []contract.ToolOfferHostTool{}}
	for _, tool := range offer.Tools {
		if contains(p.Runtime, tool.Name) {
			out.Tools = append(out.Tools, tool)
		}
	}
	for _, tool := range offer.HostTools {
		if contains(p.Host, tool.Name) {
			out.HostTools = append(out.HostTools, tool)
		}
	}
	return out
}

func names(offer contract.ToolOffer, runtime func(contract.ToolOfferRuntimeTool) bool, host func(contract.ToolOfferHostTool) bool) ([]string, []string) {
	r, h := []string{}, []string{}
	for _, tool := range offer.Tools {
		if runtime(tool) {
			r = append(r, tool.Name)
		}
	}
	for _, tool := range offer.HostTools {
		if host(tool) {
			h = append(h, tool.Name)
		}
	}
	return r, h
}

func is(value *string, want ...string) bool { return value != nil && contains(want, *value) }

// LoopPhase is the loop's: everything the session offers.
func LoopPhase(offer contract.ToolOffer) Phase {
	r, h := names(offer, func(contract.ToolOfferRuntimeTool) bool { return true }, func(contract.ToolOfferHostTool) bool { return true })
	return Phase{Kind: PhaseLoop, Runtime: r, Host: h}
}

// CriticPhase is the critic's (agent#10): of the session's Runtime tools those on CriticTools, and
// of its host tools only CriticReaders and, where the run catalogues skills, the skill loaders
// (ADR-0009).
func CriticPhase(offer contract.ToolOffer) Phase {
	r, h := names(offer,
		func(tool contract.ToolOfferRuntimeTool) bool { return contains(CriticTools, tool.Name) },
		func(tool contract.ToolOfferHostTool) bool {
			return contains(CriticReaders, tool.Name) || contains(SkillTools, tool.Name)
		})
	return Phase{Kind: PhaseCritic, Runtime: r, Host: h}
}

// PlanningTool is planningTool in planning/policy.ts: a host tool Plan mode keeps. Where it
// declares an effect, read or plan; otherwise a question, a delegation, or a tool a delegated
// child may read or research with.
func PlanningTool(tool contract.ToolOfferHostTool) bool {
	if tool.Effect != nil {
		return is(tool.Effect, "read", "plan")
	}
	return is(tool.Interaction, "question", "delegation") || is(tool.Delegation, "read", "research")
}

// PlanPhase is Plan mode's (agent#13): of the session's Runtime tools those on PlanRuntimeReads;
// of its host tools the planning ones (PlanningTool) and the host's context tools.
func PlanPhase(offer contract.ToolOffer) Phase {
	r, h := names(offer,
		func(tool contract.ToolOfferRuntimeTool) bool { return contains(PlanRuntimeReads, tool.Name) },
		func(tool contract.ToolOfferHostTool) bool {
			// ADR-0010: Plan mode runs no critic, so the ask for one is not admitted there.
			return tool.Name != ReviewTool && (contains(ContextTools, tool.Name) || PlanningTool(tool))
		})
	return Phase{Kind: PhasePlan, Runtime: r, Host: h}
}

// ReformatPhase is the tool-free reformat run's: nothing.
func ReformatPhase() Phase {
	return Phase{Kind: PhaseReformat, Runtime: []string{}, Host: []string{}}
}

// ChildPhase is a delegated child's (agent#15, delegation.ts runChild), started from its parent's
// phase: of the session's Runtime tools those its profile names (runtimeTools, none where absent);
// of its host tools those with no interaction whose delegation member is a capability the profile
// lists, and the host's context tools; and of these only what the parent's phase offers, so a child
// started in Plan mode holds Plan's reads and no more.
func ChildPhase(profile contract.DelegatedTaskProfile, offer contract.ToolOffer, parent Phase) Phase {
	r, h := names(offer,
		func(tool contract.ToolOfferRuntimeTool) bool {
			return contains(profile.RuntimeTools, tool.Name) && contains(parent.Runtime, tool.Name)
		},
		func(tool contract.ToolOfferHostTool) bool {
			if !contains(parent.Host, tool.Name) {
				return false
			}
			if contains(ContextTools, tool.Name) {
				return true
			}
			return tool.Interaction == nil && tool.Delegation != nil && contains(profile.Capabilities, *tool.Delegation)
		})
	return Phase{Kind: PhaseChild, Profile: profile.ID, Runtime: r, Host: h}
}

// CriticRefusalDetail is why the critic's call of name did not leave (criticRefusalDetail).
func CriticRefusalDetail(name string) string {
	return name + " is not one of the tools the critic may call (" + joinNames(append(append([]string{}, CriticTools...), CriticReaders...)) +
		"); the call did not leave the page and nothing was written"
}

func joinNames(list []string) string {
	out := ""
	for i, name := range list {
		if i > 0 {
			out += ", "
		}
		out += name
	}
	return out
}
