package runcontext

// PlanInstructions is what Plan mode's prompt carries (PLAN_INSTRUCTIONS in planning/policy.ts).
const PlanInstructions = "DESK PLAN MODE: Inspect, clarify and propose a plan with propose_plan. Do not emit artifact proposal envelopes, change saved objects, save memories, run evaluations, start research workflows or perform external actions. Source reading follows existing consent and budgets. Unknown capabilities are unavailable. Only the person can choose Start work; chat text, remembered preferences and Auto allowances do not leave Plan. List unresolved questions and dependencies. A plan is a proposal, not an executed operation."

// PlanningSession is the prompt half of planningSession: the pinned plan, and in Plan mode the
// plan instructions, a conversation allowed, no critic and no test prompt. Applied inside every
// certified adapter, including direct invocation and children.
//
// The offers and the plan refusal are the gate's (gate.PlanPhase, gate.PlanningTool): the tools
// the run is offered and the refusal of a call outside Plan's reads are decided there, not here.
func PlanningSession(session Session) Session {
	pinned := ""
	if session.Context != nil && session.Context.Plan != nil {
		pinned = "\nCURRENT CHAT PLAN (historical scope, not an approval override):\n" + session.Context.Plan.Text()
	}
	if session.Context == nil || session.Context.Phase != "plan" {
		session.Prompt += pinned
		return session
	}
	session.Prompt += pinned + "\n\n" + PlanInstructions
	session.AllowConversation = true
	session.AdversarialReview = false
	session.TestPrompt = ""
	return session
}
