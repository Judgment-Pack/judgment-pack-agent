/**
 * The desk-owned tier table, its one fallback, and the five states.
 *
 * Everything here is the **desk's** rather than either engine's: the table, the
 * closed list of refusals, the dialect fallback, and the two states a tier
 * cannot express. The engines' own suites measure what reaches the wire; this
 * measures what the desk decided to put there.
 */
import { describe, expect, it } from 'vitest'
import {
  RESPONSE_TOKENS,
  ALWAYS_FROM_ABSENCE,
  GEMINI_BUDGET,
  THINKING_ALWAYS,
  THINKING_UNAVAILABLE,
  firstDialect,
  isTruncatedSignature,
  nextDialect,
  normalize,
  openThinking,
  stateFromEvents,
  thinkingLine,
  unsupportedThinking,
  wireFor
} from './thinking'
import type { AssistantEvent } from './engine'
import type { EndpointKind, ThinkingTier } from './host'

const slot = (tier: ThinkingTier, family: EndpointKind = 'openai-compatible') =>
  openThinking({
    reasoning: normalize(tier, family),
    model: { family, model: 'a-model', call: async () => new Response('{}') }
  } as Pick<import('./engine').RequestModelValues, 'reasoning' | 'model'>)

describe('the table, per family and tier', () => {
  it('expresses off by omission on the two dialects that accept omission', () => {
    expect(wireFor('off', 'openai')).toBeNull()
    expect(wireFor('off', 'anthropic-adaptive')).toBeNull()
    expect(wireFor('off', 'anthropic-enabled')).toBeNull()
  })

  it('expresses off by a member on the Gemini wire, where omission means thinking', () => {
    // **The one place off is sent rather than omitted**, and the reason is the
    // wire's own default: a model with a thinkingConfig reasons unless told
    // not to, so "send nothing" would be asking for thinking by accident.
    expect(wireFor('off', 'gemini-budget')!.members).toEqual({
      thinkingConfig: { thinkingBudget: 0 }
    })
    expect(wireFor('off', 'gemini-level')!.members).toEqual({
      thinkingConfig: { thinkingLevel: 'minimal' }
    })
  })

  it('asks for thought summaries and a depth on the Gemini wire, in both spellings', () => {
    // `includeThoughts` is what makes the summaries arrive at all; the budget
    // or the level is how deep they go. Both, or the tier asks for thinking
    // nobody can read.
    expect(wireFor('on', 'gemini-budget')!.members).toEqual({
      thinkingConfig: { includeThoughts: true, thinkingBudget: GEMINI_BUDGET.on }
    })
    expect(wireFor('ultra', 'gemini-budget')!.members).toEqual({
      thinkingConfig: { includeThoughts: true, thinkingBudget: GEMINI_BUDGET.ultra }
    })
    expect(wireFor('on', 'gemini-level')!.members).toEqual({
      thinkingConfig: { includeThoughts: true, thinkingLevel: 'medium' }
    })
    expect(wireFor('ultra', 'gemini-level')!.members).toEqual({
      thinkingConfig: { includeThoughts: true, thinkingLevel: 'high' }
    })
    expect(GEMINI_BUDGET.ultra).toBeGreaterThan(GEMINI_BUDGET.on)
    // The member is named and not placed: the path a checker reads is the one
    // each engine writes it at, and the members themselves carry no wrapper.
    expect(wireFor('on', 'gemini-budget')!.expect.path).toBe('generationConfig.thinkingConfig')
  })

  it('tries the budget spelling first and the level second, and then stops', () => {
    expect(firstDialect('gemini')).toBe('gemini-budget')
    expect(nextDialect('gemini-budget')).toBe('gemini-level')
    expect(nextDialect('gemini-level')).toBeNull()
  })

  it('sends reasoning_effort on the OpenAI-compatible wire', () => {
    expect(wireFor('on', 'openai')!.members).toEqual({ reasoning_effort: 'high' })
    expect(wireFor('ultra', 'openai')!.members).toEqual({ reasoning_effort: 'xhigh' })
    expect(wireFor('on', 'openai')!.expect).toEqual({ path: 'reasoning_effort', value: 'high' })
  })

  it('sends the adaptive spelling first, with the depth in its sibling', () => {
    // The depth lives in `output_config`, not in the thinking member: that is
    // the current models' shape and it is why there are two dialects at all.
    expect(wireFor('on', 'anthropic-adaptive')!.members).toEqual({
      thinking: { type: 'adaptive' },
      output_config: { effort: 'high' }
    })
    expect(wireFor('ultra', 'anthropic-adaptive')!.members).toEqual({
      thinking: { type: 'adaptive' },
      output_config: { effort: 'xhigh' }
    })
  })

  it('falls back to a token budget, with the maximum that budget requires', () => {
    // **Two numbers, one decision.** Anthropic spends the thinking budget out
    // of `max_tokens`, so a table that chose a budget of 8000 while the request
    // asked for 4096 produced a request every enabled-dialect endpoint refuses.
    expect(wireFor('on', 'anthropic-enabled')!.members).toEqual({
      thinking: { type: 'enabled', budget_tokens: 8000 },
      max_tokens: 8000 + RESPONSE_TOKENS
    })
    expect(wireFor('ultra', 'anthropic-enabled')!.members).toEqual({
      thinking: { type: 'enabled', budget_tokens: 16000 },
      max_tokens: 16000 + RESPONSE_TOKENS
    })
    // Strictly above, on every tier, which is what the endpoint enforces.
    for (const tier of ['on', 'ultra'] as const) {
      const members = wireFor(tier, 'anthropic-enabled')!.members as {
        thinking: { budget_tokens: number }
        max_tokens: number
      }
      expect(members.max_tokens, tier).toBeGreaterThan(members.thinking.budget_tokens)
    }
  })

  it('leaves the maximum alone on the dialect that has no budget', () => {
    expect(Object.keys(wireFor('on', 'anthropic-adaptive')!.members).sort()).toEqual([
      'output_config',
      'thinking'
    ])
    expect(Object.keys(wireFor('on', 'openai')!.members)).toEqual(['reasoning_effort'])
  })

  it('starts each family at its own dialect and offers exactly one fallback', () => {
    expect(firstDialect('anthropic')).toBe('anthropic-adaptive')
    expect(firstDialect('openai-compatible')).toBe('openai')
    expect(nextDialect('anthropic-adaptive')).toBe('anthropic-enabled')
    expect(nextDialect('anthropic-enabled')).toBeNull()
    expect(nextDialect('openai')).toBeNull()
  })

  it('normalizes the tier into the wire the family takes', () => {
    expect(normalize('on', 'anthropic')).toEqual({
      tier: 'on',
      wire: wireFor('on', 'anthropic-adaptive'),
      state: 'on'
    })
    expect(normalize('off', 'openai-compatible')).toEqual({ tier: 'off', wire: null, state: 'off' })
  })
})

describe('what counts as a refusal of the tier', () => {
  const effort = { reasoning_effort: 'high' }
  const adaptive = { thinking: { type: 'adaptive' }, output_config: { effort: 'high' } }
  const budget = { thinking: { type: 'enabled', budget_tokens: 8000 }, max_tokens: 12096 }

  it('reads only a 400, and never a 422', () => {
    // The status every provider documents for a member it will not take. A 422
    // is something else, whatever its prose says.
    expect(unsupportedThinking(400, 'Unsupported parameter: reasoning_effort', effort)).toBe(true)
    expect(unsupportedThinking(422, 'Unsupported parameter: reasoning_effort', effort)).toBe(false)
    expect(unsupportedThinking(422, 'thinking is not supported', adaptive)).toBe(false)
    expect(unsupportedThinking(500, 'Unsupported parameter: reasoning_effort', effort)).toBe(false)
  })

  it('requires the message to name a member this desk actually sent', () => {
    // **The conjunction, which is the whole of the finding.** `Unsupported
    // parameter` and `Extra inputs are not permitted` are what an endpoint says
    // about *any* member; on their own they turned a real failure into a silent
    // degrade and a misleading "thinking is unavailable" line.
    expect(unsupportedThinking(400, 'Unsupported parameter: temperature', effort)).toBe(false)
    expect(unsupportedThinking(400, 'Extra inputs are not permitted: tools', adaptive)).toBe(false)
    expect(unsupportedThinking(400, 'messages: at least one message is required', effort)).toBe(
      false
    )
    expect(unsupportedThinking(400, 'the pack is invalid: rule 3 names no outcome', effort)).toBe(
      false
    )
  })

  it('accepts each documented refusal, because each names the member', () => {
    for (const [message, members] of [
      ['Unsupported parameter: reasoning_effort', effort],
      ['This model does not support the reasoning_effort parameter', effort],
      ['Unknown parameter: thinking', adaptive],
      ['Unrecognized request argument supplied: thinking', adaptive],
      ['thinking: Extra inputs are not permitted', adaptive],
      ['thinking.type.enabled: Extended thinking is not supported for this model', budget],
      ['Adaptive thinking is not supported by this model', adaptive],
      ['output_config.effort: not supported on this model', adaptive],
      ['thinking.budget_tokens: must be less than max_tokens', budget]
    ] as [string, Record<string, unknown>][]) {
      expect(unsupportedThinking(400, message, members), message).toBe(true)
    }
  })

  it('reads a member one level down as well as at the top', () => {
    // `budget_tokens` and `effort` live inside the members the desk sends, and
    // an endpoint names the member it actually refused.
    expect(unsupportedThinking(400, 'budget_tokens is out of range', budget)).toBe(true)
    expect(unsupportedThinking(400, 'budget_tokens is out of range', effort)).toBe(false)
  })

  it('does not read a member the desk did not send', () => {
    // The desk sent `reasoning_effort`; a refusal about `thinking` is about
    // somebody else's request.
    expect(unsupportedThinking(400, 'thinking is not supported here', effort)).toBe(false)
  })

  it('does not read a refusal about the size of an answer as one about thinking', () => {
    // `max_tokens` is raised *because* a budget was asked for, but a refusal
    // about it alone is about how long an answer may be.
    expect(unsupportedThinking(400, 'max_tokens exceeds the model maximum', budget)).toBe(false)
  })
})

describe('the five states', () => {
  it('is the tier while nothing has happened', () => {
    expect(slot('off').state()).toBe('off')
    expect(slot('on').state()).toBe('on')
    expect(slot('ultra').state()).toBe('ultra')
  })

  /** One turn: reasoning or not, and whether it produced an answer. */
  const turn = (it0: ReturnType<typeof slot>, reasoned: boolean, hadText = true) => {
    if (reasoned) it0.sawReasoning()
    return it0.turnEnded(hadText)
  }

  it('reports "always" only after two consecutive turns reason, and once', () => {
    // **One turn is a turn, not a capability.** A single unsolicited passage at
    // tier off used to label the model "always thinks" for the session.
    const it0 = slot('off')
    expect(it0.members()).toBeNull()
    expect(turn(it0, true)).toBeNull()
    expect(it0.state()).toBe('off')
    const second = turn(it0, true)
    expect(second!.type).toBe('thinking_unavailable')
    expect((second as { detail: string }).detail).toContain(THINKING_ALWAYS)
    expect(it0.state()).toBe('always')
    // Once. A third reasoning turn is not a second line.
    expect(turn(it0, true)).toBeNull()
  })

  it('does not report "always" for one reasoning turn among quiet ones', () => {
    const it0 = slot('off')
    expect(turn(it0, false)).toBeNull()
    expect(turn(it0, true)).toBeNull()
    expect(turn(it0, false)).toBeNull()
    expect(turn(it0, true)).toBeNull()
    expect(it0.state()).toBe('off')
  })

  it('needs the turns to be consecutive, and a quiet answer starts the count again', () => {
    const it0 = slot('off')
    turn(it0, true)
    turn(it0, false)
    turn(it0, true)
    expect(it0.state()).toBe('off')
    expect(turn(it0, true)).not.toBeNull()
    expect(it0.state()).toBe('always')
  })

  it('does not let a tool-only turn wipe the off-tier count either', () => {
    // **The rule, applied in both tiers.** A turn that only called a tool
    // neither counts nor resets: an endpoint that reasons about the answer it
    // is composing and says nothing while it is fetching still always thinks.
    const it0 = slot('off')
    expect(turn(it0, true)).toBeNull()
    expect(turn(it0, false, false)).toBeNull()
    expect(turn(it0, false, false)).toBeNull()
    expect(it0.state()).toBe('off')
    const said = turn(it0, true)
    expect(said, 'the tool call in the middle wiped the count').not.toBeNull()
    expect(it0.state()).toBe('always')
  })

  it('falls back once on Anthropic and degrades on the second refusal', () => {
    const it0 = slot('on', 'anthropic')
    expect(it0.members()).toEqual({ thinking: { type: 'adaptive' }, output_config: { effort: 'high' } })
    const retry = it0.refused(400, 'Adaptive thinking is not supported by this model')
    expect(retry.kind).toBe('retry')
    // Silent: the desk asked in the other spelling, and the session still thinks.
    expect(it0.state()).toBe('on')
    expect(it0.members()).toEqual({
      thinking: { type: 'enabled', budget_tokens: 8000 },
      max_tokens: 8000 + RESPONSE_TOKENS
    })
    const degraded = it0.refused(400, 'thinking: Extra inputs are not permitted')
    expect(degraded.kind).toBe('degrade')
    expect((degraded as { event: AssistantEvent }).event!.type).toBe('thinking_unavailable')
    expect(it0.state()).toBe('unavailable')
    expect(it0.members()).toBeNull()
  })

  it('degrades on the first refusal where the family has no second dialect', () => {
    const it0 = slot('on')
    const degraded = it0.refused(400, 'Unsupported parameter: reasoning_effort')
    expect(degraded.kind).toBe('degrade')
    expect(it0.state()).toBe('unavailable')
  })

  it('says the degrade once, however many times it is asked', () => {
    const it0 = slot('on')
    expect((it0.refused(400, 'Unsupported parameter: reasoning_effort') as { event: unknown }).event)
      .not.toBeNull()
    const again = it0.refused(400, 'Unsupported parameter: reasoning_effort')
    // The tier is gone, so the second refusal is not about the tier at all.
    expect(again.kind).toBe('other')
  })

  it('leaves a refusal that is not about the tier to the engine', () => {
    expect(slot('on').refused(400, 'messages: at least one message is required').kind).toBe('other')
    expect(slot('on').refused(429, 'rate limited').kind).toBe('other')
  })

  it('reports unavailable only after two consecutive answers carry no reasoning', () => {
    const it0 = slot('on')
    expect(turn(it0, false)).toBeNull()
    expect(it0.state()).toBe('on')
    const said = turn(it0, false)
    expect((said as { detail: string }).detail).toContain(THINKING_UNAVAILABLE)
    expect(it0.state()).toBe('unavailable')
  })

  it('does not count a turn that only called a tool', () => {
    // **A tool-only turn is no evidence that an endpoint will not reason.** A
    // quiet first tool turn followed by a turn that reasons must not have
    // stripped the tier from every request in between.
    const it0 = slot('on')
    expect(turn(it0, false, false)).toBeNull()
    expect(turn(it0, false, false)).toBeNull()
    expect(turn(it0, false, false)).toBeNull()
    expect(it0.state()).toBe('on')
    expect(turn(it0, true)).toBeNull()
    expect(it0.state()).toBe('on')
  })

  it('starts the count again where a turn does reason', () => {
    const it0 = slot('on')
    turn(it0, false)
    turn(it0, true)
    turn(it0, false)
    expect(it0.state()).toBe('on')
    expect(turn(it0, false)).not.toBeNull()
    expect(it0.state()).toBe('unavailable')
  })

  it('says nothing about a quiet turn when the tier is off', () => {
    const it0 = slot('off')
    expect(turn(it0, false)).toBeNull()
    expect(turn(it0, false)).toBeNull()
    expect(it0.state()).toBe('off')
  })

  it('degrades once on a truncated signature', () => {
    const it0 = slot('on', 'anthropic')
    const said = it0.truncated('the signature came back as a prefix of what arrived')
    expect((said as { detail: string }).detail).toContain('prefix')
    expect(it0.state()).toBe('unavailable')
    expect(it0.truncated('again')).toBeNull()
  })
})

describe('the refutation pass is not the tier’s to start (agent#10)', () => {
  it('has no gate on the slot: one rule, the policy table’s, decides on both engines', () => {
    // The tier used to decide (on at on and ultra, off at off, on at a degraded
    // endpoint by a ruling). `refutation.ts`'s `criticRuns` decides now, and
    // the slot has nothing to say about it at any tier or state.
    for (const tier of ['off', 'on', 'ultra'] as const) expect('runsRefutation' in slot(tier)).toBe(false)
  })
})

describe('a signature that came back short', () => {
  it('catches a strict prefix and a strict suffix of the one it was given', () => {
    expect(isTruncatedSignature('abcdef', 'abc')).toBe(true)
    expect(isTruncatedSignature('abcdef', 'def')).toBe(true)
  })

  it('passes a signature carried whole', () => {
    expect(isTruncatedSignature('abcdef', 'abcdef')).toBe(false)
  })

  it('is about one block and its own signature, and no other', () => {
    // **The predicate has to establish truncation.** A later block whose own
    // signature is legitimately shorter is not a fragment of an earlier block's
    // — and a search over every signature ever ledgered said it was.
    expect(isTruncatedSignature('abc', 'abc')).toBe(false)
    expect(isTruncatedSignature('xyz', 'abc')).toBe(false)
    expect(isTruncatedSignature('', 'abc')).toBe(false)
    expect(isTruncatedSignature('abcdef', '')).toBe(false)
  })
})

describe('the line the tab shows', () => {
  it('names the tier alone where the endpoint did what was asked', () => {
    expect(thinkingLine('off', 'off')).toBe('thinking off')
    expect(thinkingLine('ultra', 'ultra')).toBe('thinking ultra')
  })

  it('names the tier and the real state where they differ', () => {
    expect(thinkingLine('off', 'always')).toContain('always thinks')
    expect(thinkingLine('on', 'unavailable')).toContain('unavailable for this endpoint')
    expect(thinkingLine('on', 'unavailable')).toContain('thinking on')
  })

  it('is read off the run’s own events', () => {
    const notice = (detail: string): AssistantEvent => ({ type: 'thinking_unavailable', detail })
    expect(stateFromEvents('on', [])).toBe('on')
    expect(stateFromEvents('on', [notice(`${THINKING_UNAVAILABLE}: the endpoint answered 400`)])).toBe(
      'unavailable'
    )
    expect(stateFromEvents('off', [notice(`${THINKING_ALWAYS}.`)])).toBe('always')
  })
})

describe('the Gemini wire, where off is a member the endpoint may refuse', () => {
  const gemini = (tier: ThinkingTier) => slot(tier, 'gemini')

  it('sends the off member, so a refusal of it is a thing the endpoint said', () => {
    const it0 = gemini('off')
    expect(it0.members()).toEqual({ thinkingConfig: { thinkingBudget: 0 } })
    expect(it0.state()).toBe('off')
  })

  it('tries the other spelling before concluding anything from a refusal at off', () => {
    // **A 400 naming `thinkingBudget` is two different endpoints.** It is "this
    // model cannot be turned off" and it is "this model spells it
    // `thinkingLevel`", and concluding on the first refusal would label a
    // newer model as one that always thinks because the desk used an older
    // field name. The fallback is silent, exactly as Anthropic's is.
    const it0 = gemini('off')
    const first = it0.refused(400, 'Unknown name "thinkingBudget" in generationConfig.thinkingConfig')
    expect(first.kind).toBe('retry')
    expect(it0.state()).toBe('off')
    expect(it0.members()).toEqual({ thinkingConfig: { thinkingLevel: 'minimal' } })
  })

  it('reports "always" when every spelling of off is refused, at once and once', () => {
    const it0 = gemini('off')
    expect(it0.refused(400, 'Unknown name "thinkingBudget"').kind).toBe('retry')
    const said = it0.refused(400, 'thinkingLevel: minimal is not supported by this model')
    expect(said.kind).toBe('degrade')
    expect((said as { event: AssistantEvent }).event).not.toBeNull()
    expect(((said as { event: { detail: string } }).event).detail).toContain(THINKING_ALWAYS)
    expect(((said as { event: { detail: string } }).event).detail).toContain('400')
    expect(it0.state()).toBe('always')
    // **And the member comes off with it**, which is the same rule the degrade
    // follows: there is nothing left to ask.
    expect(it0.members()).toBeNull()
    // A refusal after that is somebody else's business, and says nothing again.
    expect(it0.refused(400, 'thinkingBudget').kind).toBe('other')
  })

  it('reaches "always" from absence too, and keeps asking for what the file asked', () => {
    // The other road to the same state: the desk asked for a zero budget, the
    // endpoint took it and reasoned anyway, twice. Nothing was refused, so
    // nothing is withdrawn — the file said off and the desk goes on saying so.
    const it0 = gemini('off')
    it0.sawReasoning()
    expect(it0.turnEnded(true)).toBeNull()
    it0.sawReasoning()
    const second = it0.turnEnded(true)
    expect((second as { detail: string }).detail).toContain(THINKING_ALWAYS)
    expect((second as { detail: string }).detail).toContain(ALWAYS_FROM_ABSENCE)
    expect(it0.state()).toBe('always')
    expect(it0.members()).toEqual({ thinkingConfig: { thinkingBudget: 0 } })
  })

  it('degrades rather than reporting "always" where the tier asked to think', () => {
    const it0 = gemini('on')
    expect(it0.refused(400, 'Unknown name "thinkingBudget"').kind).toBe('retry')
    const said = it0.refused(400, 'thinkingLevel is not supported')
    expect(said.kind).toBe('degrade')
    expect(it0.state()).toBe('unavailable')
    expect(it0.members()).toBeNull()
  })

  it('reads a refusal that names none of its members as an ordinary failure', () => {
    // The lesson the classifier already carries: `Unsupported parameter` is a
    // sentence an endpoint writes about anything at all.
    const it0 = gemini('on')
    expect(it0.refused(400, 'The document was too large').kind).toBe('other')
    expect(it0.refused(429, 'thinkingBudget').kind).toBe('other')
    expect(it0.state()).toBe('on')
  })
})
