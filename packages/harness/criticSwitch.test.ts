/**
 * The critic's switch (ADR-0010), held to the contract's vectors: the decision for each case of the
 * `critic/switch-*.json` vectors, the session members read closed, the reserved tool's declaration,
 * the model's ask taken once and refused after, under off and outside the author's phase, and the
 * checkpoint's member read and carried. The engines' behaviour on every adapter is certification
 * scenario 10 (`certification/scenarios.test.ts`).
 */
import { afterEach, describe, expect, it, vi } from 'vitest'
import { CONTEXT_DEFAULTS } from './context/policy'
import { contextualSession } from './context/session'
import { readExecution } from './execution/state'
import { normalize } from './thinking'
import {
  AGENT_TIERS,
  CRITIC_MODES,
  CRITIC_REASONS,
  CriticSwitch,
  MODEL_TIERS,
  REQUEST_REVIEW,
  REVIEW_TAKEN,
  criticDecidedEvent,
  criticDecision,
  criticForcedOff,
  criticMode,
  criticTier,
  readCriticSettings,
  reviewRefusal,
  validCriticRecord,
  type CriticForcedFacts,
  type CriticRecord,
  type CriticRunFacts,
  type CriticSettings,
  type CriticTable,
  type CriticTier
} from './criticSwitch'
import modes from '../../contract/vectors/critic/switch-modes.json'
import triggers from '../../contract/vectors/critic/switch-triggers.json'
import forcedOff from '../../contract/vectors/critic/switch-forced-off.json'
import budget from '../../contract/vectors/critic/switch-budget.json'
import tier from '../../contract/vectors/critic/switch-tier.json'
import alias from '../../contract/vectors/critic/switch-alias.json'
import sessions from '../../contract/vectors/critic/switch-sessions.json'
import requestReview from '../../contract/vectors/critic/request-review.json'
import refused from '../../contract/vectors/critic/review-refused.json'
import decidedEvents from '../../contract/vectors/critic/decided-events.json'
import checkpoints from '../../contract/vectors/critic/checkpoint.json'
import reviewAsked from '../../contract/vectors/critic/review-asked.json'
import { REFUTATION_MARKER } from './refutation'
import { sseTurn, streamed, vercelTurn, type EndpointRequest } from './testing/scripted'
import type { AssistantEvent, AssistantSession, McpToolResult } from './engine'

afterEach(() => vi.unstubAllGlobals())

interface SwitchCase {
  name: string
  session: CriticSettings
  table: CriticTable
  forced: CriticForcedFacts
  run: CriticRunFacts
  author?: CriticTier
  capability: 'model' | 'agent'
}
const VECTORS = { 'switch-modes': modes, 'switch-triggers': triggers, 'switch-forced-off': forcedOff, 'switch-budget': budget, 'switch-tier': tier, 'switch-alias': alias }

describe('the decision, held to the vectors (ADR-0010)', () => {
  for (const [name, vector] of Object.entries(VECTORS)) {
    const cases = vector.cases as unknown as SwitchCase[]
    it.each(cases.map((one, at) => [one.name, one, vector.items[at]] as const))(`critic/${name}.json: %s`, (_name, one, item) => {
      const decided = criticDecision(one.session, one.table, one.forced, one.run)
      const reasoning = criticTier(one.author, one.session.criticReasoning, one.table.criticReasoningFloor)
      expect({ ...decided, ...(reasoning === undefined ? {} : { reasoning }) }).toEqual(item)
      // The session's members are read closed on the run's capability.
      expect(() => readCriticSettings(one.session, one.capability === 'agent')).not.toThrow()
    })
  }

  it('reads the precedence run > project > table and nothing else', () => {
    expect(criticMode({ critic: 'off', criticProject: 'on' }, { criticDefault: 'on' })).toEqual({ mode: 'off', layer: 'person' })
    expect(criticMode({ criticProject: 'off' }, { criticDefault: 'on' })).toEqual({ mode: 'off', layer: 'project' })
    expect(criticMode({}, { criticDefault: 'on' })).toEqual({ mode: 'on', layer: 'table' })
    expect(criticMode({}, CONTEXT_DEFAULTS)).toEqual({ mode: 'auto', layer: 'table' })
    // A default the table states that is not a mode is no default: auto.
    expect(criticMode({}, { criticDefault: 'sometimes' })).toEqual({ mode: 'auto', layer: 'table' })
  })

  it('never reads prose: the same facts give the same decision whatever the model or the person wrote', () => {
    const run = { proposal: true, cases: false, changesExisting: false, modelAsked: false, room: true }
    const said = criticDecision({ critic: 'auto' }, CONTEXT_DEFAULTS, { child: false }, run)
    expect(said).toEqual({ mode: 'auto', ran: false, reason: 'person' })
    // The decision function takes no text at all: its inputs are the session's words, the table and facts.
    expect(criticDecision.length).toBe(4)
  })

  it('forces the critic off in the order child, phase, purpose, reviewer', () => {
    expect(criticForcedOff({ child: true, phase: 'plan', purpose: 'graph', reviewerAssigned: true })).toBe('child')
    expect(criticForcedOff({ child: false, phase: 'plan', purpose: 'graph', reviewerAssigned: true })).toBe('phase')
    expect(criticForcedOff({ child: false, phase: 'work', purpose: 'graph', reviewerAssigned: true })).toBe('purpose')
    expect(criticForcedOff({ child: false, purpose: 'brief', reviewerAssigned: true })).toBe('reviewer-assigned')
    expect(criticForcedOff({ child: false, purpose: 'test-design' })).toBeUndefined()
  })

  it('the tier floor: the author’s tier where the host names none, the host’s where it names one, lower included', () => {
    expect(criticTier('ultra', undefined, 'author')).toBe('ultra')
    expect(criticTier('ultra', 'off', 'author')).toBe('off')
    expect(criticTier(undefined, undefined, 'author')).toBeUndefined()
    expect(criticTier('high', 'max', undefined)).toBe('max')
  })
})

describe('the session’s members, read closed (ADR-0010)', () => {
  it('critic/switch-sessions.json: each session is read on its capability', () => {
    const [composer, reviewer, agent] = sessions.items as unknown as (CriticSettings & { existingPack?: boolean; reviewerAssigned?: boolean })[]
    expect(() => readCriticSettings(composer!, false)).not.toThrow()
    expect(() => readCriticSettings(reviewer!, false)).not.toThrow()
    expect(() => readCriticSettings(agent!, true)).not.toThrow()
  })

  it.each([
    [{ critic: 'sometimes' }, false, 'critic must be off, auto or on'],
    [{ criticProject: true }, false, 'criticProject must be off, auto or on'],
    [{ criticReasoning: 'high' }, false, 'criticReasoning must be one of off, on, ultra'],
    [{ criticReasoning: 'ultra' }, true, 'criticReasoning must be one of none, minimal, low, medium, high, xhigh, max'],
    [{ existingPack: 'yes' }, false, 'existingPack must be true or false'],
    [{ reviewerAssigned: 1 }, false, 'reviewerAssigned must be true or false'],
    [{ adversarialReview: 'on' }, true, 'adversarialReview must be true or false']
  ] as const)('refuses %j', (session, agent, said) => {
    expect(() => readCriticSettings(session as never, agent)).toThrow(said)
  })

  it('a session the reader refuses ends the run before any request, a configuration failure', async () => {
    const call = vi.fn()
    await expect(contextualSession({ ...modelSession(), criticReasoning: 'max' } as never)).rejects.toMatchObject({ failure: { code: 'configuration', source: 'context' } })
    expect(call).not.toHaveBeenCalled()
  })

  it('every mode, reason and tier the contract names is this module’s', () => {
    expect([...CRITIC_MODES]).toEqual(['off', 'auto', 'on'])
    expect(CRITIC_REASONS).toHaveLength(13)
    expect([...MODEL_TIERS, ...AGENT_TIERS]).toEqual(['off', 'on', 'ultra', 'none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max'])
  })
})

/** A model session at a tier, with no tools, for the boundary alone. */
function modelSession(patch: Partial<AssistantSession> = {}): AssistantSession {
  return {
    prompt: 'Check the expense pack.', testPrompt: '', tools: [], hostTools: [], callTool: vi.fn(),
    model: { family: 'openai-compatible', model: 'scripted-model', call: vi.fn() }, thinking: normalize('on', 'openai-compatible'),
    signal: new AbortController().signal, ...patch
  }
}
const textOf = (result: McpToolResult) => result.content?.map((part) => ('text' in part ? part.text : '')).join('') ?? ''

describe('request_review, the model’s ask (ADR-0010)', () => {
  it('critic/request-review.json: the boundary offers it as declared, after the context tools, under auto', async () => {
    const { session } = await contextualSession(modelSession())
    const names = session.hostTools.map((tool) => tool.name)
    expect(names.at(-1)).toBe(REQUEST_REVIEW)
    const offered = session.hostTools.find((tool) => tool.name === REQUEST_REVIEW)!
    expect({ name: offered.name, description: offered.description, inputSchema: offered.inputSchema, effect: offered.effect }).toEqual(requestReview.items[0])
  })

  it.each([
    ['off', { critic: 'off' as const }],
    ['on', { critic: 'on' as const }],
    ['a child', { context: { objective: 'Read the policy.' } }],
    ['Plan mode', { context: { phase: 'plan' as const } }],
    ['a graph-purpose run', { purpose: 'graph' as const }],
    ['a reviewer assigned', { reviewerAssigned: true }]
  ])('is not offered under %s', async (_name, patch) => {
    const { session } = await contextualSession(modelSession(patch as Partial<AssistantSession>))
    expect(session.hostTools.map((tool) => tool.name)).not.toContain(REQUEST_REVIEW)
  })

  it('takes the first ask, refuses the second with the guardrail review refused, and records only the first', async () => {
    const { session, context } = await contextualSession(modelSession())
    const tool = session.hostTools.find((one) => one.name === REQUEST_REVIEW)!
    const answer = await tool.execute({ reason: 'The pack changes the approval threshold.' }, new AbortController().signal)
    expect(JSON.parse(textOf(answer))).toEqual({ asked: true, note: REVIEW_TAKEN })
    expect(context.execution.state.critic).toEqual({ mode: 'auto', trigger: 'model-asked' })
    await expect(tool.execute({}, new AbortController().signal)).rejects.toMatchObject({ failure: { code: 'tool-failed', source: 'tool', outcome: 'not-started', detail: refused.items[1]!.detail } })
    expect(context.takeNotices()).toEqual([refused.items[1]])
    expect(context.execution.state.critic).toEqual({ mode: 'auto', trigger: 'model-asked' })
  })

  it('refuses an ask that is not a reason, records nothing', async () => {
    const { session, context } = await contextualSession(modelSession())
    const tool = session.hostTools.find((one) => one.name === REQUEST_REVIEW)!
    await expect(tool.execute({ reason: 7 }, new AbortController().signal)).rejects.toMatchObject({ failure: { code: 'invalid-tool-input' } })
    expect(context.execution.state.critic).toBeUndefined()
  })

  it('refuses the name under off, from the critic and from the reformat run, with the guardrail, before anything runs', async () => {
    const off = await contextualSession(modelSession({ critic: 'off' }))
    expect(() => off.context.admit(REQUEST_REVIEW, 'runtime')).toThrow()
    expect(off.context.takeNotices()).toEqual([refused.items[4]])
    const auto = await contextualSession(modelSession())
    auto.context.beginCriticPhase()
    expect(() => auto.context.admit(REQUEST_REVIEW, 'host')).toThrow()
    auto.context.endCriticPhase()
    auto.context.beginReformatPhase()
    expect(() => auto.context.admit(REQUEST_REVIEW, 'host')).toThrow()
    auto.context.endReformatPhase()
    const notices = auto.context.takeNotices()
    expect(notices.map((notice) => notice.type === 'guardrail' ? [notice.reason, notice.detail] : null)).toEqual([
      ['review refused', reviewRefusal('outside')], ['review refused', reviewRefusal('outside')]
    ])
    expect(auto.context.execution.state.critic).toBeUndefined()
    // In the author's phase, where it is offered, the boundary admits it.
    expect(() => auto.context.admit(REQUEST_REVIEW, 'host')).not.toThrow()
  })

  it('refuses the ask under off and on even in the author’s phase, and records nothing (defence in depth)', () => {
    for (const critic of ['off', 'on'] as const) {
      let held: CriticRecord | undefined
      const one = new CriticSwitch({ critic }, CONTEXT_DEFAULTS, { child: false }, { get: () => held, set: (value) => { held = value } })
      expect(one.offered).toBe(false)
      expect(one.ask(true)).toBe(reviewRefusal(critic))
      expect(held).toBeUndefined()
    }
  })

  it('a switch whose ask was carried refuses a second ask and decides with the trigger', () => {
    let held: CriticRecord | undefined = { mode: 'auto', trigger: 'model-asked' }
    const one = new CriticSwitch({}, CONTEXT_DEFAULTS, { child: false }, { get: () => held, set: (value) => { held = value } })
    expect(one.ask(true)).toBe(reviewRefusal('asked'))
    expect(one.decide({ proposal: true, cases: false, changesExisting: false, room: true }, { author: 'on' })).toEqual({ mode: 'auto', ran: true, reason: 'model-asked', reasoning: 'on', trigger: 'model-asked' })
    expect(held).toEqual({ mode: 'auto', ran: true, reason: 'model-asked', reasoning: 'on', trigger: 'model-asked' })
  })

  it('holds the critic’s invocation back under auto and on, never under off or where it is forced off', () => {
    const switched = (settings: CriticSettings, forced: CriticForcedFacts = { child: false }) => new CriticSwitch(settings, CONTEXT_DEFAULTS, forced, { get: () => undefined, set: () => {} })
    expect(switched({}).reserved).toBe(true)
    expect(switched({ critic: 'on' }).reserved).toBe(true)
    expect(switched({ critic: 'off' }).reserved).toBe(false)
    expect(switched({ critic: 'on' }, { child: false, reviewerAssigned: true }).reserved).toBe(false)
  })
})

describe('the decision event and the checkpoint’s member (ADR-0010)', () => {
  it('critic/decided-events.json: each event is the contract’s, built from its decision', () => {
    for (const item of decidedEvents.items) {
      const { type: _type, ...decision } = item
      expect(criticDecidedEvent(decision as never)).toEqual(item)
    }
  })

  it('critic/checkpoint.json: each checkpoint is read with its critic, closed', () => {
    for (const item of checkpoints.items) {
      expect(readExecution(item)?.critic).toEqual(item.critic)
      expect(validCriticRecord(item.critic)).toBe(true)
    }
    const [asked] = checkpoints.items
    for (const broken of [{ ...asked!.critic, mode: 'sometimes' }, { ...asked!.critic, ran: true }, { ...asked!.critic, verdict: 'refuted' }, { ...asked!.critic, reasoning: 'deep' }]) {
      expect(validCriticRecord(broken)).toBe(false)
      expect(() => readExecution({ ...asked, critic: broken })).toThrow()
    }
  })

  it('a resume carries the ask: the restored run refuses a second ask and decides with model-asked', async () => {
    const [asked] = checkpoints.items
    const restored = { ...asked!, requestKey: '' }
    // The request key is the digest of the request and the phase; the restored run is the same request.
    const probe = await contextualSession(modelSession({ context: { currentRequest: { message: 'Check the expense pack.', input: 'Check the expense pack.' } } }))
    restored.requestKey = probe.context.execution.state.requestKey
    const { session, context } = await contextualSession(modelSession({ context: { currentRequest: { message: 'Check the expense pack.', input: 'Check the expense pack.' }, execution: restored as never } }))
    expect(context.execution.state.critic).toEqual({ mode: 'auto', trigger: 'model-asked' })
    const tool = session.hostTools.find((one) => one.name === REQUEST_REVIEW)!
    await expect(tool.execute({}, new AbortController().signal)).rejects.toMatchObject({ failure: { detail: reviewRefusal('asked') } })
    expect(context.decideCritic(true)).toEqual({ mode: 'auto', ran: true, reason: 'model-asked', reasoning: 'on', trigger: 'model-asked' })
    expect(context.execution.snapshot().critic).toEqual({ mode: 'auto', ran: true, reason: 'model-asked', reasoning: 'on', trigger: 'model-asked' })
  })
})

describe('the model’s ask on the vercel engine’s stream (ADR-0010)', () => {
  const PROPOSAL = 'Ready.\n\n```json\n' + JSON.stringify({ proposal: { document: { id: 'p' }, unknowns: [] } }) + '\n```'
  const reviewing = (request: EndpointRequest) => JSON.stringify(request.body).includes(REFUTATION_MARKER)
  const askEvents = (events: AssistantEvent[]) => events.filter((event) =>
    ((event.type === 'tool_call' || event.type === 'tool_result') && event.name === REQUEST_REVIEW) ||
    (event.type === 'guardrail' && event.tool === REQUEST_REVIEW) || event.type === 'critic_decided')

  it('critic/review-asked.json and review-refused.json: the ask taken, a second refused, and the critic run on model-asked', async () => {
    const turns = [
      { name: REQUEST_REVIEW, args: { reason: 'The pack changes the approval threshold.' } },
      { name: REQUEST_REVIEW, args: {} }
    ]
    const { events } = await vercelTurn({
      answer: (request) => {
        if (reviewing(request)) return streamed(sseTurn({ text: 'Done.' }))
        const at = (request.body as { messages?: { role: string }[] }).messages?.filter((message) => message.role === 'tool').length ?? 0
        const next = turns[at]
        return streamed(next === undefined ? sseTurn({ text: PROPOSAL }) : sseTurn({ tool: { name: next.name, args: next.args, id: `ask_${at}` } }))
      },
      session: { testPrompt: 'Test the pack.' }
    })
    expect(askEvents(events)).toEqual([reviewAsked.items[0], reviewAsked.items[1], ...refused.items.slice(0, 3), reviewAsked.items[2]])
  })

  it('critic/review-refused.json: an ask under off, refused before anything runs, and no critic', async () => {
    let asked = false
    const { events, requests } = await vercelTurn({
      answer: () => {
        if (asked) return streamed(sseTurn({ text: PROPOSAL }))
        asked = true
        return streamed(sseTurn({ tool: { name: REQUEST_REVIEW, args: {}, id: 'ask_0' } }))
      },
      session: { testPrompt: 'Test the pack.', critic: 'off' }
    })
    expect(askEvents(events)).toEqual(refused.items.slice(3))
    expect(requests.some(reviewing)).toBe(false)
  })
})
