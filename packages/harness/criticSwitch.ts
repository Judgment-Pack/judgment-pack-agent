/**
 * **The critic's switch** (ADR-0010): whether the critic runs on a run, why, and at which tier, by one
 * rule every engine reads.
 *
 * The switch is three-valued, `off | auto | on`, set in three layers with one precedence: the run's
 * word (`session.critic`, the person's per-run control) over the project's (`session.criticProject`)
 * over the policy table's default (`criticDefault`, `auto`). Under `on` the critic runs on every
 * proposal; under `off` never; under `auto` the package decides from facts it already holds and never
 * from the person's or the model's prose: the run produced a proposal, and declared cases exist, or
 * the proposal changes an existing pack, or the model asked through the reserved tool
 * `request_review`. A human reviewer assigned, a graph-purpose run, Plan mode and a delegated child
 * turn the critic off whatever the mode, each with its named reason. The decision is emitted once per
 * run (`critic_decided`) and written into the checkpoint (`critic`), so a person and a later reader
 * see why a review ran or did not. The three rules of the refutation pass (`refutation.ts`) do not
 * change: the switch decides only whether the pass runs.
 *
 * `adversarialReview`, the boolean before ADR-0010, is read for one release as an alias of the
 * layer it sits in: on the session of the run's word, in the table of `criticDefault`; `true` is
 * `on`, `false` is `off`, and the new member wins where both are set.
 */
import {resolvePolicy,type PolicyMembers,type PolicyLayer} from './policy'
import type { ThinkingTier } from './host'
import type { CodexEffort } from './agent'

/** The switch's three values (`tool-offer.json#/$defs/criticMode`). */
export type CriticMode = 'off' | 'auto' | 'on'
export const CRITIC_MODES: readonly CriticMode[] = ['off', 'auto', 'on']
/** The layer a mode was set in: the run's word (the person's), the project's, the policy table's. */
export type CriticLayer = 'person' | 'project' | 'table'
/** What turns the critic on under `auto`, in the order the decision reads them. */
export type CriticTrigger = 'model-asked' | 'cases' | 'changes-existing'
/** What turns the critic off whatever the mode, in the order the decision reads them. */
export type CriticForcedOff = 'child' | 'phase' | 'purpose' | 'reviewer-assigned'
/** Why the critic ran or did not (`provenance.json#/$defs/criticReason`), one enum. */
export type CriticReason = CriticLayer | CriticTrigger | CriticForcedOff | 'no-proposal' | 'budget' | 'off'
export const CRITIC_REASONS: readonly CriticReason[] = ['person', 'project', 'table', 'model-asked', 'cases', 'changes-existing', 'no-proposal', 'reviewer-assigned', 'purpose', 'phase', 'child', 'budget', 'off']
/** The model capability's tiers and the agent capability's efforts, each lowest first. */
export const MODEL_TIERS: readonly ThinkingTier[] = ['off', 'on', 'ultra']
export const AGENT_TIERS: readonly CodexEffort[] = ['none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max']
/** A tier in the enum of a run's capability (`tool-offer.json#/$defs/criticReasoning`). */
export type CriticTier = ThinkingTier | CodexEffort

/** The decision for one run (`provenance.json#/$defs/criticDecision`): the report's row. */
export interface CriticDecision {
  mode: CriticMode
  ran: boolean
  reason: CriticReason
  /** The critic's tier; absent where the run states none (an agent capability with no effort). */
  reasoning?: CriticTier
  /** Under `auto`, the trigger that held; kept where the critic was skipped for budget. */
  trigger?: CriticTrigger
}
/** The checkpoint's member (`checkpoint.json` `critic`): the decision, or before it the model's ask alone. */
export type CriticRecord = { mode: CriticMode; trigger?: CriticTrigger; ran?: boolean; reason?: CriticReason; reasoning?: CriticTier }

/** The reserved name the model asks for a review with (`tool-offer.json#/$defs/requestReview`). */
export const REQUEST_REVIEW = 'request_review'

/** What the session says about the critic: the run's and the project's words, the tier, the alias. */
export interface CriticSettings {
  policy?: PolicyLayer
  policyProject?: PolicyLayer
  resolvedPolicy?: PolicyMembers
  critic?: CriticMode
  criticProject?: CriticMode
  criticReasoning?: CriticTier
  adversarialReview?: boolean
}
/** What the policy table says: the default, the tier floor, and the alias. */
export interface CriticTable {
  criticDefault?: string
  criticReasoningFloor?: string
  adversarialReview?: boolean
}
/** The facts that turn the critic off whatever the mode. */
export interface CriticForcedFacts {
  /** A delegated child's run (its objective is set, agent#11). */
  child: boolean
  phase?: 'plan' | 'work'
  purpose?: string
  reviewerAssigned?: boolean
}
/** The run's own facts at the point the critic would start. */
export interface CriticRunFacts {
  /** The run produced a proposal: there is something to refute. */
  proposal: boolean
  /** The host declared cases for the critic to compare (`criticCases`). */
  cases: boolean
  /** The proposal changes a pack that already exists (the host's `existingPack`). */
  changesExisting: boolean
  /** The model called `request_review` this run (the checkpoint's critic carries it through a resume). */
  modelAsked: boolean
  /** The allowance, or on the agent capability the invocations, leave the critic room to run. */
  room: boolean
}

const alias = (value: boolean | undefined): CriticMode | undefined => (value === undefined ? undefined : value ? 'on' : 'off')
const isMode = (value: unknown): value is CriticMode => typeof value === 'string' && (CRITIC_MODES as readonly string[]).includes(value)

/**
 * **The precedence, run > project > table**, in one place: the run's word (`critic`, else the alias
 * `adversarialReview`), else the project's, else the table's default (`criticDefault`, else the
 * table's alias), else `auto`. Answers the mode and the layer that set it.
 */
export function criticMode(session: CriticSettings, table: CriticTable): { mode: CriticMode; layer: CriticLayer } {
  const members = session.resolvedPolicy ?? resolvePolicy(
    {...session.policy,critic:{...(session.critic !== undefined || session.adversarialReview !== undefined ? {default:session.critic ?? alias(session.adversarialReview)} : {}),...session.policy?.critic}},
    {...session.policyProject,critic:{...(session.criticProject !== undefined ? {default:session.criticProject} : {}),...session.policyProject?.critic}},
    {...table,models:{author:{model:'current',reasoning:'off'}}},
    {connections:['current'],agent:false,requests:16}).members
  const held = members['critic.default']!
  return {mode:held.value as CriticMode,layer:held.layer === 'run' ? 'person' : held.layer}
}

/** The forced-off reason that holds, in the order child, phase, purpose, reviewer-assigned; or none. */
export function criticForcedOff(facts: CriticForcedFacts): CriticForcedOff | undefined {
  if (facts.child) return 'child'
  if (facts.phase === 'plan') return 'phase'
  if (facts.purpose === 'graph') return 'purpose'
  if (facts.reviewerAssigned === true) return 'reviewer-assigned'
  return undefined
}

/**
 * **The decision, one function every engine reads** (it replaces `criticRuns`). From the session's
 * words, the table, the forced-off facts and the run's facts it answers the mode, whether the critic
 * runs, the reason and, under `auto`, the trigger. Nothing here reads prose: every input is a value
 * the host passed or the package holds.
 */
export function criticDecision(session: CriticSettings, table: CriticTable, forced: CriticForcedFacts, run: CriticRunFacts): CriticDecision {
  const { mode, layer } = criticMode(session, table)
  const off = criticForcedOff(forced)
  if (off !== undefined) return { mode, ran: false, reason: off }
  if (mode === 'off') return { mode, ran: false, reason: 'off' }
  if (!run.proposal) return { mode, ran: false, reason: 'no-proposal' }
  let trigger: CriticTrigger | undefined
  if (mode === 'auto') {
    trigger = run.modelAsked ? 'model-asked' : run.cases ? 'cases' : run.changesExisting ? 'changes-existing' : undefined
    if (trigger === undefined) return { mode, ran: false, reason: layer }
  }
  const reason: CriticReason = trigger ?? layer
  if (!run.room) return { mode, ran: false, reason: 'budget', ...(trigger === undefined ? {} : { trigger }) }
  return { mode, ran: true, reason, ...(trigger === undefined ? {} : { trigger }) }
}

/**
 * **The critic's tier, at least the author's unless the host says otherwise.** The host's
 * `criticReasoning` where it names one, honoured as it is, lower than the author's included; else the
 * floor, `author`: the author's own tier on the run's capability. Absent where neither is stated.
 */
export function criticTier(author: CriticTier | undefined, asked: CriticTier | undefined, floor: string | undefined): CriticTier | undefined {
  if (asked !== undefined) return asked
  return floor === undefined || floor === 'author' ? author : undefined
}

/** Whether a tier is in the enum of the run's capability. */
export function tierFits(tier: CriticTier, agent: boolean): boolean {
  return agent ? (AGENT_TIERS as readonly string[]).includes(tier) : (MODEL_TIERS as readonly string[]).includes(tier)
}

/**
 * The session's critic members, read closed before any request: each mode one of the three, the tier
 * one of its capability's enum, the two facts booleans. Throws the sentence a configuration failure
 * carries.
 */
export function readCriticSettings(session: CriticSettings & { existingPack?: unknown; reviewerAssigned?: unknown }, agent: boolean): void {
  for (const member of ['critic', 'criticProject'] as const) {
    const value = session[member]
    if (value !== undefined && !isMode(value)) throw new Error(`The session's ${member} must be off, auto or on.`)
  }
  if (session.criticReasoning !== undefined && (typeof session.criticReasoning !== 'string' || !tierFits(session.criticReasoning, agent))) {
    throw new Error(`The session's criticReasoning must be one of ${(agent ? AGENT_TIERS : MODEL_TIERS).join(', ')} on this capability.`)
  }
  for (const member of ['adversarialReview', 'existingPack', 'reviewerAssigned'] as const) {
    const value = (session as Record<string, unknown>)[member]
    if (value !== undefined && typeof value !== 'boolean') throw new Error(`The session's ${member} must be true or false.`)
  }
}

/** The tool's description and input schema, the contract's (`tool-offer.json#/$defs/requestReview`). */
export const REQUEST_REVIEW_DESCRIPTION = 'Ask for an independent review of the proposal this response makes. Call it at most once, before you propose, where a second check would help: a critic then checks the proposal with the runtime’s own tools before it is shown. Its findings come from the runtime’s results; your reason is not shown to it.'
export const REQUEST_REVIEW_SCHEMA = Object.freeze({ type: 'object', properties: { reason: { type: 'string', description: 'Why a review of this run’s proposal is wanted, in one sentence.' } } })
/** What the model reads where its ask is taken. */
export const REVIEW_TAKEN = 'A review was asked for: the critic will check the proposal this response makes before it is shown. Its verdict comes from the runtime’s results.'

/**
 * Why an ask was refused: the guardrail's detail and the failed result's text (reason
 * `review refused`).
 */
export function reviewRefusal(why: 'asked' | 'off' | 'on' | 'outside' | CriticForcedOff): string {
  switch (why) {
    case 'asked': return `${REQUEST_REVIEW} was already called in this response, and a response asks for one review; nothing more was recorded.`
    case 'off': return `${REQUEST_REVIEW} is refused: the critic is off for this run, and the model's ask cannot turn it on; nothing was recorded.`
    case 'on': return `${REQUEST_REVIEW} is not needed: the critic is on for this run and checks every proposal; nothing was recorded.`
    case 'outside': return `${REQUEST_REVIEW} is offered only to the author's run, not to the critic or the reformat run; nothing was recorded.`
    case 'child': return `${REQUEST_REVIEW} is not offered to a delegated child; nothing was recorded.`
    case 'phase': return `${REQUEST_REVIEW} is not offered in Plan mode, where no critic runs; nothing was recorded.`
    case 'purpose': return `${REQUEST_REVIEW} is not offered on a graph-purpose run, where no critic runs; nothing was recorded.`
    case 'reviewer-assigned': return `${REQUEST_REVIEW} is not offered where a reviewer is assigned, where no critic runs; nothing was recorded.`
  }
}

/**
 * One run's switch, held by the shared boundary (`context/session.ts`): the mode and the layer it
 * came from, the forced-off reason, the ask (the checkpoint's `critic`), and the decision, written
 * once. The engines read `decide` at the point the critic would start; the boundary offers
 * `request_review` where `offered` says so, and the delegation coordinator holds the critic's
 * invocation back where `reserved` says so.
 */
export class CriticSwitch {
  readonly mode: CriticMode
  readonly layer: CriticLayer
  readonly forced: CriticForcedOff | undefined
  constructor(
    private readonly settings: CriticSettings,
    private readonly table: CriticTable,
    private readonly forcedFacts: CriticForcedFacts,
    private readonly record: { get: () => CriticRecord | undefined; set: (value: CriticRecord) => void }
  ) {
    const held = criticMode(settings, table)
    this.mode = held.mode
    this.layer = held.layer
    this.forced = criticForcedOff(forcedFacts)
  }
  /** Whether `request_review` is offered: under `auto`, on a run nothing forces the critic off. */
  get offered(): boolean { return this.forced === undefined && this.mode === 'auto' }
  /** Whether one agent invocation is held back for the critic: under `auto` and `on`, where nothing forces it off. */
  get reserved(): boolean { return this.forced === undefined && this.mode !== 'off' }
  /** Whether the model's ask was taken this run, a restored one's included. */
  get asked(): boolean { return this.record.get()?.trigger === 'model-asked' }
  /**
   * The model's ask, in the author's phase. Answers the refusal's sentence where the ask is refused
   * (`review refused`), or null where it is taken and recorded.
   */
  ask(authorPhase: boolean): string | null {
    if (this.forced !== undefined) return reviewRefusal(this.forced)
    if (this.mode !== 'auto') return reviewRefusal(this.mode)
    if (!authorPhase) return reviewRefusal('outside')
    if (this.asked) return reviewRefusal('asked')
    this.record.set({ mode: this.mode, trigger: 'model-asked' })
    return null
  }
  /**
   * **The decision**, once the run's facts are known: written into the checkpoint's `critic` (over a
   * carried ask) and answered with the tier, for the engine to emit as `critic_decided`.
   */
  decide(run: Omit<CriticRunFacts, 'modelAsked'>, tiers: { author?: CriticTier }): CriticDecision {
    const decided = criticDecision(this.settings, this.table, this.forcedFacts, { ...run, modelAsked: this.asked })
    const reasoning = criticTier(tiers.author, this.settings.criticReasoning, this.table.criticReasoningFloor)
    // The contract's order: mode, ran, reason, reasoning, trigger.
    const decision: CriticDecision = { mode: decided.mode, ran: decided.ran, reason: decided.reason, ...(reasoning === undefined ? {} : { reasoning }), ...(decided.trigger === undefined ? {} : { trigger: decided.trigger }) }
    this.record.set({ ...decision })
    return decision
  }
}

/** The contract's event, built in one place from a decision. */
export function criticDecidedEvent(decision: CriticDecision): { type: 'critic_decided' } & CriticDecision {
  return { type: 'critic_decided', mode: decision.mode, ran: decision.ran, reason: decision.reason,
    ...(decision.reasoning === undefined ? {} : { reasoning: decision.reasoning }), ...(decision.trigger === undefined ? {} : { trigger: decision.trigger }) }
}

/** The checkpoint's member, read closed (`checkpoint.json` `critic`). */
export function validCriticRecord(value: unknown): value is CriticRecord {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) return false
  const held = value as Record<string, unknown>
  if (Object.keys(held).some((member) => !['mode', 'ran', 'reason', 'reasoning', 'trigger'].includes(member))) return false
  if (!isMode(held.mode)) return false
  if ((held.ran === undefined) !== (held.reason === undefined)) return false
  if (held.ran !== undefined && typeof held.ran !== 'boolean') return false
  if (held.reason !== undefined && !(CRITIC_REASONS as readonly unknown[]).includes(held.reason)) return false
  if (held.reasoning !== undefined && !([...MODEL_TIERS, ...AGENT_TIERS] as readonly unknown[]).includes(held.reasoning)) return false
  if (held.trigger !== undefined && !['model-asked', 'cases', 'changes-existing'].includes(String(held.trigger))) return false
  return true
}
