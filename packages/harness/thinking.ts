import { capabilitiesFor } from './capabilities'
import { sourceMessage } from './host'
/**
 * The thinking tier, normalized in the desk.
 *
 * ADR-0001: *"The tier maps to provider parameters in one desk-owned table, per
 * endpoint family, and the engine receives the normalized result. The slot has
 * five states, three of them measured: `off`, `on`, `ultra`, **"this model
 * always thinks"** and **"unavailable for this endpoint"**. No framework
 * supplies the last two; they are desk code whichever engine runs."*
 *
 * So this module is the whole of what an engine is allowed to know about
 * thinking: **one table**, one closed list of refusals, one lifecycle, and one
 * sentence per state. An engine reads members off the slot and puts them on the
 * wire; it never chooses a parameter, never decides what a 400 meant, and never
 * writes a line about the tier.
 *
 * **`off` is expressed by omission on two families, and by a member on the
 * third.** Anthropic rejects `{"type":"disabled"}` on the models that always
 * think, and several OpenAI-compatible endpoints answer 400 to
 * `reasoning_effort: "none"`, so *send nothing* is the only spelling of off
 * those two accept. On the native Gemini wire the opposite is true: **omission
 * means thinking**, because the models that carry a `thinkingConfig` at all
 * reason by default, so "send nothing" would be asking for thinking by
 * accident. Off is therefore `thinkingBudget: 0` there — the field the API
 * reference documents for exactly this — and the difference is stated rather
 * than smoothed over, because it is the reason the table is per family.
 *
 * That, in turn, is why "this model always thinks" has two ways to be reached.
 * On the two omitting families it is **detected from reasoning arriving with
 * the tier off**: with nothing sent there is no request for an endpoint to
 * refuse. On the Gemini wire it may also be **the endpoint refusing to be
 * turned off** — a 400 naming the member, at every spelling this desk knows —
 * and that one is immediate, because a refusal is the endpoint saying so.
 *
 * Ported from the bake-off's `none` prototype (`fixture/THINKING-SPEC.md`, and
 * `none/src/thinking.ts`), and extended to the third family this desk
 * configures.
 */
import type { EndpointKind, ThinkingTier } from './host'
import type { AssistantEvent, AssistantSession } from './engine'
import type { ReasoningCapability, ReasoningKind } from './capabilities'

/**
 * The five states, three of which a person may select.
 *
 * `always` and `unavailable` are reports and not settings: `deskConfig` admits
 * `off`, `on` and `ultra` and nothing else, deliberately, because a state the
 * desk **discovers** is not a state anybody can ask for.
 */
export type ThinkingState = ThinkingTier | 'always' | 'unavailable'

/**
 * The endpoint's vocabulary. One slot tier, three spellings.
 *
 * The families are the desk's three, and two of them have two dialects each,
 * both for the same reason: the depth moved between model generations and
 * neither API has a capability endpoint, so the first real request is the probe
 * and the fallback is this list.
 *
 * On `anthropic`, current models take `thinking: {type: "adaptive"}` with the
 * depth in a **sibling** `output_config`, and 4.5-era models take a token
 * budget inside the thinking member and reject the adaptive spelling. On
 * `gemini`, `generationConfig.thinkingConfig` carries either a
 * `thinkingBudget` in tokens or a `thinkingLevel` out of a small enumeration —
 * the newer spelling — and a model that takes one answers 400 to the other.
 */
export type ThinkingDialect =
  | 'openai'
  | 'anthropic-adaptive'
  | 'anthropic-enabled'
  | 'gemini-budget'
  | 'gemini-level'

/** What one (family, tier) pair puts on the wire, and what that is called. */
export interface WireThinking {
  /** Top-level body members, merged into the request by whichever engine runs. */
  members: Record<string, unknown>
  /** The member a checker reads, so a leg can assert the parameter and not a shape. */
  expect: { path: string; value: unknown }
}

/**
 * The tokens the desk leaves for the model's **answer**, beyond any thinking
 * budget.
 *
 * The built-in Anthropic provider asks for this many by default, and it is
 * enough for a whole pack and its explanation. It is here rather than only
 * there because the enabled dialect's budget and the request's maximum are one
 * decision — see the table.
 */
export const RESPONSE_TOKENS = 4096

/**
 * The two thinking budgets the Gemini dialect asks for, in tokens.
 *
 * **The desk's choice inside a documented field, and it is labelled as one.**
 * `thinkingConfig.thinkingBudget` is an integer token allowance in the API
 * reference; the *range* is per model and the reference does not state one that
 * holds across the family, so these are numbers chosen to be an ordinary
 * working depth and a deep one rather than numbers quoted from anywhere. A
 * model whose range excludes one of them answers 400 naming the member, and the
 * desk falls back to the level spelling and then degrades — which is the same
 * path a model that has no budget field at all takes. See the README.
 */
export const GEMINI_BUDGET: Readonly<Record<Exclude<ThinkingTier,'off'>, number>> = {
  low: 4096, medium: 8192, high: 24576,
  on: 8192,
  ultra: 24576
}

/**
 * The two thinking levels the other Gemini dialect asks for.
 *
 * The enumeration is `minimal`, `low`, `medium`, `high` — four values, of which
 * a given model advertises a subset — so `on` is the middle of it and `ultra`
 * the top. `minimal` is what `off` sends on this dialect: the enumeration has
 * no "none" in it, and the desk asks for the least this spelling can express
 * rather than inventing a fifth value.
 */
export const GEMINI_LEVEL: Readonly<Record<ThinkingTier, string>> = {
  low:'low', medium:'medium', high:'high',
  off: 'minimal',
  on: 'medium',
  ultra: 'high'
}

/**
 * **The table.** One place, both engines, every family.
 *
 * `null` for `off` on the two families where off is omission; a real member on
 * `gemini`, where omission means thinking and the only way to ask for none is
 * to ask. `high` and `xhigh` are the two efforts the desk's two tiers mean.
 *
 * **The enabled dialect sets two numbers, and it has to.** Anthropic's
 * `budget_tokens` must fit *below* the request's `max_tokens` — the budget is
 * spent out of the maximum — so a table that chose a budget of 8000 while the
 * provider asked for `max_tokens: 4096` produced a request every endpoint
 * requiring that dialect refuses. Both numbers are therefore this table's, per
 * tier: the budget, and the budget plus the desk's own response allowance. The
 * adaptive dialect needs neither, because there is no budget in it.
 *
 * **The Gemini members are named and not placed.** What comes back is
 * `{thinkingConfig: …}`, and each engine puts it where its wire wants it — under
 * `generationConfig` on the native wire, under `providerOptions.google` through
 * the SDK. The table says *what* the tier means; an engine says *where*, which
 * is the same division the other two families already keep.
 */
export function wireFor(tier: ThinkingTier, dialect: ThinkingDialect, tiers?: Record<'low'|'medium'|'high',string>): WireThinking | null {
  const geminiPath = 'generationConfig.thinkingConfig'
  if (tier === 'off') {
    // Off is omission on the two families that accept it as one, and a member
    // on the one that does not. See the module comment.
    if (dialect === 'gemini-budget') {
      return {
        members: { thinkingConfig: { thinkingBudget: 0 } },
        expect: { path: geminiPath, value: { thinkingBudget: 0 } }
      }
    }
    if (dialect === 'gemini-level') {
      return {
        members: { thinkingConfig: { thinkingLevel: GEMINI_LEVEL.off } },
        expect: { path: geminiPath, value: { thinkingLevel: GEMINI_LEVEL.off } }
      }
    }
    return null
  }
  const setting = tiers?.[tier === 'ultra' || tier === 'high' ? 'high' : tier === 'low' ? 'low' : 'medium']
  const effort = setting ?? (tier === 'ultra' || tier === 'high' ? 'xhigh' : tier === 'low' ? 'low' : 'high')
  switch (dialect) {
    case 'gemini-budget': {
      // `includeThoughts` is what makes the thought summaries arrive at all;
      // the budget is how deep they go. Both, or the tier asks for thinking
      // nobody can read.
      const thinkingConfig = { includeThoughts: true, thinkingBudget: setting === undefined ? GEMINI_BUDGET[tier] : Number(setting) }
      return { members: { thinkingConfig }, expect: { path: geminiPath, value: thinkingConfig } }
    }
    case 'gemini-level': {
      const thinkingConfig = { includeThoughts: true, thinkingLevel: GEMINI_LEVEL[tier] }
      return { members: { thinkingConfig }, expect: { path: geminiPath, value: thinkingConfig } }
    }
    case 'openai':
      return {
        members: { reasoning_effort: effort },
        expect: { path: 'reasoning_effort', value: effort }
      }
    case 'anthropic-adaptive':
      return {
        members: { thinking: { type: 'adaptive' }, output_config: { effort } },
        expect: { path: 'thinking', value: { type: 'adaptive' } }
      }
    case 'anthropic-enabled': {
      const budget = tier === 'ultra' || tier === 'high' ? 16000 : tier === 'low' ? 4000 : 8000
      return {
        members: {
          thinking: { type: 'enabled', budget_tokens: budget },
          // Strictly above the budget, or the endpoint refuses the request.
          max_tokens: budget + RESPONSE_TOKENS
        },
        expect: { path: 'thinking', value: { type: 'enabled', budget_tokens: budget } }
      }
    }
  }
}

/**
 * Every member the table can add to a request in order to **ask for thinking**.
 *
 * A closed list, because it is what a request is stripped of when the slot
 * degrades: a request that both asks for thinking and no longer carries a
 * signed block it was given is a request an endpoint may refuse outright.
 * `max_tokens` is deliberately not here — it is a member the protocol requires
 * on every Anthropic request, and a maximum left larger than the degraded
 * session needs is legal and harmless, where a *missing* one is not a request
 * at all.
 */
export const TIER_MEMBERS: readonly string[] = [
  'reasoning_effort',
  'thinking',
  'output_config',
  'thinkingConfig'
]

/** The dialect a family is tried at first. */
export function firstDialect(family: EndpointKind): ThinkingDialect {
  switch (family) {
    case 'anthropic':
      return 'anthropic-adaptive'
    case 'gemini':
      return 'gemini-budget'
    default:
      return 'openai'
  }
}

/**
 * The dialect a capability record's reasoning kind is tried at first (agent#4):
 * the same first spelling `firstDialect` gives the family that speaks it. A
 * record whose kind is `none` asks for nothing, and has no dialect.
 */
export function dialectFor(kind: Exclude<ReasoningKind, 'none'>): ThinkingDialect {
  switch (kind) {
    case 'anthropic-thinking':
      return 'anthropic-adaptive'
    case 'gemini-thinking':
      return 'gemini-budget'
    case 'openai-effort':
      return 'openai'
  }
}

/**
 * The one fallback there is, or nothing.
 *
 * ADR-0001 puts "the dialect fallback between thinking spellings" in the desk
 * on every engine. It is **one** step per family: adaptive then the token
 * budget on Anthropic, the token budget then the level on Gemini, and after
 * that the endpoint has no thinking this desk knows how to ask for.
 */
export function nextDialect(dialect: ThinkingDialect): ThinkingDialect | null {
  if (dialect === 'anthropic-adaptive') return 'anthropic-enabled'
  if (dialect === 'gemini-budget') return 'gemini-level'
  return null
}

/**
 * The member names a thinking refusal may be **about**, and the whole of them.
 *
 * A closed list, and it is one half of the test below. `max_tokens` is not on
 * it: the desk raises the maximum *because* it asked for a budget, but a
 * refusal about `max_tokens` alone is a refusal about the size of an answer and
 * not about thinking.
 */
const THINKING_MEMBERS: readonly string[] = [
  'thinking',
  'reasoning_effort',
  'output_config',
  'budget_tokens',
  'thinkingConfig',
  'thinkingBudget',
  'thinkingLevel',
  'includeThoughts'
]

/** Every name this desk actually sent, one level down as well as at the top. */
function namesSent(members: Record<string, unknown>): string[] {
  const names: string[] = []
  for (const [name, value] of Object.entries(members)) {
    if (!names.includes(name)) names.push(name)
    if (value !== null && typeof value === 'object' && !Array.isArray(value)) {
      for (const inner of Object.keys(value as Record<string, unknown>)) {
        if (!names.includes(inner)) names.push(inner)
      }
    }
  }
  return names.filter((name) => THINKING_MEMBERS.includes(name))
}

/**
 * Is this refusal about the thinking parameter this desk sent, or about the
 * request at large?
 *
 * **Two conditions, and both are required.** A 400 — and only a 400, because
 * that is the status every provider documents for a member it will not take —
 * whose message **names a member this desk actually added**, out of a closed
 * list of the members that ask for thinking at all.
 *
 * The prose alone is not enough, and that was the defect: `Unsupported
 * parameter` and `Extra inputs are not permitted` are the sentences an endpoint
 * writes about *any* member, so `400 Unsupported parameter: temperature`
 * degraded the tier, said "thinking is unavailable for this endpoint", and hid
 * a real failure behind it. A refusal that names none of the desk's own
 * thinking members is an ordinary model error and is reported as one.
 *
 * **Nothing here quotes the body beyond matching it.** The sentence a person
 * reads is the desk's, with the status in it.
 */
export function unsupportedThinking(
  status: number,
  message: string,
  members: Record<string, unknown>
): boolean {
  if (status !== 400) return false
  return namesSent(members).some((name) => message.includes(name))
}

// ADR-0001's open question "whether the refutation pass should still run its
// validate check on a degraded endpoint" no longer arises here: since agent#10
// the tier does not decide whether the critic runs. One rule does, on both
// engines, from the policy table (refutation.ts, criticRuns), and a critic the
// host asks for runs whatever state the tier is in.

/**
 * How many consecutive turns an **inference from absence** takes.
 *
 * A refusal is the endpoint telling the desk something and is believed at once.
 * "This model always thinks" and "this endpoint has no thinking" inferred from
 * what a single turn did or did not carry are not that: they are one
 * observation promoted to a session-wide capability. Two consecutive turns
 * agreeing is the smallest thing that is not one turn — and where the endpoint
 * really has no thinking, two is one extra request.
 */
export const PERMANENCE = 2

/**
 * Whether **this** block's signature came back shorter than it went out.
 *
 * `vercel/ai#19663`: an Anthropic thinking signature split across two stream
 * events is truncated, and what is carried back on the next turn is one half of
 * it. Real Anthropic sends one `signature_delta`; a re-chunking proxy might
 * not, and a malformed thinking block is a request the endpoint refuses.
 *
 * **One block against the one signature it was given, and not against every
 * signature ever seen.** The comparison used to be a search over the whole
 * ledger, so a later block whose own signature was legitimately shorter — and
 * happened to be a prefix of an earlier one — was thrown away as a fragment.
 * Production signatures make that collision unlikely; the predicate is what has
 * to establish truncation, and a global membership test does not.
 *
 * The rule is a **strict** prefix or suffix: a signature carried back whole is
 * equal to what was sent and is not truncated.
 */
export function isTruncatedSignature(sent: string, carried: string): boolean {
  if (carried === '' || sent === '' || sent === carried) return false
  return sent.length > carried.length && (sent.startsWith(carried) || sent.endsWith(carried))
}

/**
 * The sentence for each state, and the whole of what the desk says about the
 * tier.
 *
 * Exported because the tab's status line, the event stream and the state
 * reader below all have to agree — a second spelling of "this did not happen"
 * is how a reader learns to skip the line.
 */
export const THINKING_ALWAYS = 'this model always thinks'

/**
 * The two ways the desk reaches that state, as the reasons it prints after it.
 *
 * **The head is the state and the reason is the evidence**, and they are two
 * strings because there are now two kinds of evidence. It used to be one
 * sentence with the evidence welded into it — "no thinking parameter was sent
 * at tier off and the endpoint returned reasoning anyway" — which stopped being
 * true the moment a family arrived where off *is* a parameter. A sentence that
 * describes the wrong observation is worse than a shorter one.
 */
export const ALWAYS_FROM_ABSENCE =
  sourceMessage("the tier asked for no thinking and the endpoint returned reasoning anyway")

export function alwaysFromRefusal(status: number): string {
  return sourceMessage("the endpoint answered {{value0}} to every spelling of the parameter that turns thinking off", { value0: status })
}

export const THINKING_UNAVAILABLE = 'thinking is unavailable for this endpoint'

/** The desk's own notice, with the reason after it. */
export function thinkingNotice(state: 'always' | 'unavailable', reason: string): string {
  if (state === 'always') return reason === ''
    ? sourceMessage('this model always thinks.')
    : sourceMessage('this model always thinks: {{message0}}', { message0: reason })
  return reason === ''
    ? sourceMessage('thinking is unavailable for this endpoint.')
    : sourceMessage('thinking is unavailable for this endpoint: {{message0}}', { message0: reason })
}

/** One line for the tab, naming the tier the file asked for and the real state. */
export function thinkingLine(tier: ThinkingTier, state: ThinkingState, msg = sourceMessage): string {
  const label = (value: ThinkingTier) => value === 'off' ? msg('off') : value === 'on' ? msg('on') : msg('ultra')
  if (state === 'always') return msg('thinking {{tier}} · this model always thinks', { tier: label(tier) })
  if (state === 'unavailable') return msg('thinking {{tier}} · unavailable for this endpoint', { tier: label(tier) })
  return msg('thinking {{tier}}', { tier: label(state) })
}

/**
 * The state a finished run reached, read off its own events.
 *
 * The pane sees events and nothing else, so the state it shows is derived from
 * them rather than remembered somewhere a second reader could disagree with.
 */
export function stateFromEvents(
  tier: ThinkingTier,
  events: readonly AssistantEvent[]
): ThinkingState {
  const notices = events.filter(
    (event): event is Extract<AssistantEvent, { type: 'thinking_unavailable' }> =>
      event.type === 'thinking_unavailable'
  )
  if (notices.some((notice) => notice.detail.startsWith(THINKING_ALWAYS))) return 'always'
  if (notices.length > 0) return 'unavailable'
  return tier
}

/** The normalized result the session carries: the tier, its wire, its state. */
export interface NormalizedThinking {
  tier: ThinkingTier
  /** What the first request carries, or null at `off`. */
  wire: WireThinking | null
  /** Where the slot starts. `always` and `unavailable` are only ever reached later. */
  state: ThinkingState
}

/**
 * The tier, normalized once, where the session is bound.
 *
 * Both bind sites — the run hook and the conformance session — call this, so
 * an engine is handed the same object on the page and in CI.
 */
export function normalize(tier: ThinkingTier, family: EndpointKind): NormalizedThinking {
  return { tier, wire: wireFor(tier, firstDialect(family)), state: tier }
}

/** What a refused request means, decided here and never in an engine. */
export type ThinkingRefusal =
  /** Not about the tier: the engine reports the failure as it always would. */
  | { kind: 'other' }
  /** Try the same request again at the next dialect. Nothing is said. */
  | { kind: 'retry' }
  /**
   * The endpoint has no thinking. The request is retried plain, and the notice
   * is emitted **once for the session** — `event` is null where the desk has
   * already said it, so an engine cannot announce a degrade twice by being
   * asked twice.
   */
  | { kind: 'degrade'; event: AssistantEvent | null }

/**
 * One session's thinking slot: the table, the fallback and the degrade,
 * together, and each of them **once**.
 *
 * Held by whichever engine runs, constructed from the normalized result the
 * desk put on the session. Every transition is a method that returns the event
 * to emit, or null where the desk has already said it: an engine cannot
 * announce a degrade twice because it is not the thing counting.
 */
export interface ThinkingSlot {
  readonly tier: ThinkingTier
  /** The current state, which only ever moves away from the tier. */
  state(): ThinkingState
  /** The members this request carries, or null. */
  members(): Record<string, unknown> | null
  /** What the endpoint's refusal meant. */
  refused(status: number, message: string): ThinkingRefusal
  /**
   * Reasoning arrived in the turn now in progress. Says nothing on its own.
   *
   * The **turn** is what carries evidence about an endpoint, not a passage, so
   * this only marks; `turnEnded` is where a state can change.
   */
  sawReasoning(): void
  /**
   * One model turn finished, and whether it carried an answer of its own.
   *
   * **This is where permanence is decided, and it takes more than one turn.**
   * A single observation is a turn, not a capability: one unsolicited passage
   * at tier off used to label a model "always thinks" for the session, and one
   * quiet first turn at tier on used to strip the parameter from every request
   * after it — so a tool-only turn, an endpoint's one-off omission or a gateway
   * hiccup became a conclusion about the endpoint. A **400 naming the member**
   * is immediate, because that is the endpoint saying so; an inference from
   * *absence* is not.
   *
   * `hadText` is whether the turn produced an answer rather than only a tool
   * call: a turn that said nothing but called a tool is no evidence that an
   * endpoint will not reason, so it is not counted either way.
   */
  turnEnded(hadText: boolean): AssistantEvent | null
  /** A carried signature came back short. Degrades, once, with the reason. */
  truncated(reason: string): AssistantEvent | null
  /**
   * The notice owed before the first request, once, or null (agent#4): where the
   * capability record says the endpoint has no reasoning parameter, or ignores
   * the one it has, a tier other than off is unavailable from the start.
   */
  opening(): AssistantEvent | null
}

/** Why a record leaves the tier nothing to ask with, in the desk's own words. */
const NO_PARAMETER = sourceMessage('its capability record names no reasoning parameter')
const IGNORED_PARAMETER = sourceMessage('its capability record says the endpoint ignores the reasoning parameter')

/**
 * `reasoning` is the capability record's (agent#4): the dialect the tier is
 * spelled in is the kind's, and a kind of `none`, or a parameter the endpoint
 * ignores, sends no member at any tier. Absent, the family's first dialect, as
 * before there was a record.
 */
export function openThinking(session: Pick<AssistantSession, 'thinking' | 'model'>, reasoning?: ReasoningCapability): ThinkingSlot {
  const tier = session.thinking.tier
  /** The record leaves nothing to ask with: no parameter, or one the endpoint ignores. */
  const silent = reasoning !== undefined && (reasoning.kind === 'none' || reasoning.ignored === true)
  let dialect = reasoning === undefined || reasoning.kind === 'none' ? firstDialect(session.model.family) : dialectFor(reasoning.kind)
  let unavailable = silent && tier !== 'off'
  let always = false
  /**
   * The endpoint refused the member that asks it **not** to think, at every
   * spelling this desk knows.
   *
   * Separate from `always`, and the difference is what the next request
   * carries. `always` reached by *absence* changes nothing about what the desk
   * asks for: the file said off, the desk keeps asking for off, and the model
   * keeps thinking anyway. `always` reached by a *refusal* means there is no
   * longer anything to send, so the member comes off — which is the same rule
   * the degrade follows, and it is what keeps "the refused member is never sent
   * again" true on this family as well.
   */
  let offRefused = false
  let announced = false
  /** Reasoning seen in the turn now in progress. */
  let reasonedThisTurn = false
  /** Consecutive off-tier turns that reasoned, and on-tier answers that did not. */
  let reasoningRun = 0
  let quietRun = 0

  const notice = (state: 'always' | 'unavailable', reason: string): AssistantEvent | null => {
    if (announced) return null
    announced = true
    return { type: 'thinking_unavailable', detail: thinkingNotice(state, reason) }
  }

  // `wireFor` already answers null at `off` on the two families where off is
  // omission, so there is no tier test here: what this adds is the two states
  // in which the desk has stopped asking at all.
  const wire = () => (silent || unavailable || offRefused ? null : wireFor(tier, dialect, capabilitiesFor(session.model).tiers))

  return {
    tier,
    state: () => (always ? 'always' : unavailable ? 'unavailable' : tier),
    members: () => wire()?.members ?? null,
    refused(status, message) {
      const current = wire()
      if (current === null) return { kind: 'other' }
      if (!unsupportedThinking(status, message, current.members)) {
        return { kind: 'other' }
      }
      const next = nextDialect(dialect)
      if (next !== null) {
        // **The fallback is silent, because it is not a degrade.** The desk
        // asked in one spelling and asks again in the other; the session still
        // thinks, and a line about it would be a line about this desk's
        // vocabulary rather than about the endpoint.
        //
        // **And it runs at `off` too, before anything is concluded.** A 400
        // naming `thinkingBudget` may be "this model cannot be turned off" or
        // "this model spells it `thinkingLevel`", and those are not the same
        // endpoint. Trying the other spelling first is what tells them apart;
        // concluding on the first refusal would label a Gemini 3 model as one
        // that always thinks because the desk used a 2.5-era field name.
        dialect = next
        return { kind: 'retry' }
      }
      if (tier === 'off') {
        // **The endpoint will not be turned off, and said so.** That is the
        // `always` state reached by a refusal rather than by absence, and it is
        // immediate for the reason every refusal is: the endpoint is telling
        // the desk something rather than the desk inferring it. The member comes
        // off with it — there is nothing left to ask.
        always = true
        offRefused = true
        return { kind: 'degrade', event: notice('always', alwaysFromRefusal(status)) }
      }
      unavailable = true
      // The status, and the desk's own sentence. The endpoint's body is
      // matched against a closed list and never quoted past it.
      return {
        kind: 'degrade',
        event: notice('unavailable', sourceMessage("the endpoint answered {{value0}} to the tier parameter", { value0: status }))
      }
    },
    sawReasoning() {
      reasonedThisTurn = true
    },
    turnEnded(hadText) {
      const reasoned = reasonedThisTurn
      reasonedThisTurn = false
      // **A turn that only called a tool neither counts nor resets, in either
      // tier.** It is not evidence that an endpoint will not reason, and it is
      // not evidence that it will: several endpoints reason about the answer
      // they are composing and say nothing while they are fetching. The rule
      // was written for the on-tier counter and applied only there, so at tier
      // off a reasoning answer, a tool call and a second reasoning answer never
      // reached "always" — the tool call in the middle wiped the count.
      if (!hadText) return null
      if (tier === 'off') {
        // An answer that returned no reasoning is counter-evidence, so the run
        // starts again: "always" means every answer, not one of them.
        reasoningRun = reasoned ? reasoningRun + 1 : 0
        if (always || reasoningRun < PERMANENCE) return null
        always = true
        return notice('always', ALWAYS_FROM_ABSENCE)
      }
      if (unavailable) return null
      if (reasoned) {
        quietRun = 0
        return null
      }
      quietRun += 1
      if (quietRun < PERMANENCE) return null
      unavailable = true
      return notice(
        'unavailable',
        sourceMessage("{{value0}} consecutive turns carried an answer and no reasoning block", { value0: PERMANENCE })
      )
    },
    truncated(reason) {
      if (unavailable) return null
      unavailable = true
      return notice('unavailable', reason)
    },
    opening() {
      if (!silent || tier === 'off' || announced) return null
      return notice('unavailable', reasoning?.kind === 'none' ? NO_PARAMETER : IGNORED_PARAMETER)
    }
  }
}
