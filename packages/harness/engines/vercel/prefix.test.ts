/**
 * A stable prompt prefix, with cache breakpoints (agent#9).
 *
 * The request is ordered system text, tools, the host's prompt, the history.
 * What changes between requests (the counters, the plan, the operations index,
 * the host's changing text, the step's own instruction) is one text headed
 * `STATE_HEADER`: on the first request a second text part of the person's own
 * message, on every wire; after that where the endpoint's `statePlacement`
 * says, one user message after the history (`trailing-user`) or the end of the
 * system text (`system-tail`). So each request repeats the one before it up to
 * the state, for each placement. The execution instructions are sent once.
 * Anthropic gets breakpoints after the system head and after the tools and, under `trailing-user`
 * only, after the request text (the first text part of the first user turn): three, or two under
 * `system-tail`, where the state sits before the messages and the bytes through the request text
 * grow on every request (agent#76); no other wire gets any. The tier is held for the run. The provider's
 * cache reads and writes are recorded with each completion.
 *
 * Measured on the recorded scenario, over the scripted model and the recorded
 * runtime the certification suite uses.
 */
import { afterEach, describe, expect, it, vi } from 'vitest'
import placementVector from '../../../../contract/vectors/budget/state-placement.json'
import { vercel } from './index'
import { STATE_HEADER, statePlacement } from './loop'
import { scenario, scriptedModel, scriptedRuntime } from '../../certification'
import { bindModelCall, openAssistantConnection, runAssistantSession } from '../../session'
import { EXECUTION_INSTRUCTIONS } from '../../execution/controller'
import { modelCompletion, readCompletion } from '../../completion'
import { REFUTATION_MARKER } from '../../refutation'
import { normalize } from '../../thinking'
import { configureHarness, type EndpointCapabilities, type EndpointKind, type StatePlacement } from '../../host'
import type { AssistantEvent, AssistantSession, ModelCall, McpTool } from '../../engine'
import type { ExecutionCheckpoint } from '../../execution/state'

afterEach(() => vi.unstubAllGlobals())

const APIS: EndpointKind[] = ['openai-compatible', 'anthropic', 'gemini']
const PLACEMENTS: StatePlacement[] = ['trailing-user', 'system-tail']

/** Run the recorded scenario on one wire and keep every request body the engine sent. */
export async function scenarioRun(
  api: EndpointKind,
  changes: Partial<AssistantSession> = {},
  endpoint: { capabilities?: EndpointCapabilities; thinking?: 'off' | 'on' } = {}
): Promise<{ bodies: string[]; events: AssistantEvent[] }> {
  configureHarness({ endpointCapabilities: () => endpoint.capabilities ?? {} })
  const thinking = endpoint.thinking ?? 'off'
  const model = scriptedModel({ api, answerAs: 'stream', thinking, refuted: false, signatures: 'thought' })
  vi.stubGlobal('fetch', model.fetch)
  const runtime = await scriptedRuntime()
  const events: AssistantEvent[] = []
  const connection = openAssistantConnection({ allowed: scenario.scenarioTools, onEvent: (event) => events.push(event), transport: runtime.transport })
  const bodies: string[] = []
  try {
    const ready = await connection.ready
    const bound = bindModelCall(api)
    const call: ModelCall = async (suffix, request) => {
      bodies.push(request.body)
      return bound(suffix, request)
    }
    await runAssistantSession(vercel, {
      prompt: scenario.policy,
      testPrompt: 'THE RUNTIME’S TEST_PACK GUIDANCE',
      tools: ready.tools,
      callTool: ready.callTool,
      hostTools: [],
      model: { family: api, model: 'scripted-model', call },
      thinking: normalize(thinking, api),
      adversarialReview: false,
      signal: new AbortController().signal,
      ...changes
    }, (event) => events.push(event))
  } finally {
    await connection.close()
    await runtime.close()
  }
  return { bodies, events }
}

type Json = Record<string, unknown>
type Part = { role: string; part: Json }
/**
 * One request as the provider's cache sees it, in prefix order, one entry per
 * part: Anthropic sends tools, then the system blocks, then the messages;
 * Gemini the system instruction's parts, then the tools, then the contents;
 * OpenAI-compatible the tools, then the messages, the system first. A message's
 * text is one part whether the wire spells it as a string or as parts.
 */
function parts(api: EndpointKind, body: string): Part[] {
  const sent = JSON.parse(body) as Json
  const of = (role: string, content: unknown, rest: Json = {}): Part[] =>
    [...(typeof content === 'string' ? [{ type: 'text', text: content }] : Array.isArray(content) ? (content as Json[]) : []), ...(Object.keys(rest).length ? [rest] : [])]
      .map((part) => ({ role, part }))
  if (api === 'anthropic') {
    return [{ role: 'tools', part: { tools: sent.tools } }, ...of('system', sent.system),
      ...(sent.messages as { role: string; content: unknown }[]).flatMap((message) => of(message.role, message.content))]
  }
  if (api === 'gemini') {
    return [...of('system', (sent.systemInstruction as { parts?: unknown } | undefined)?.parts), { role: 'tools', part: { tools: sent.tools } },
      ...(sent.contents as { role: string; parts: unknown }[]).flatMap((content) => of(content.role, content.parts))]
  }
  return [{ role: 'tools', part: { tools: sent.tools } },
    ...(sent.messages as Json[]).flatMap(({ role, content, ...rest }) => of(String(role), content, rest))]
}
const textOf = (part: Part) => (typeof part.part.text === 'string' ? part.part.text : '')
const carries = (part: Part) => textOf(part).includes(STATE_HEADER)
const segments = (list: Part[]) => list.map((part) => `${part.role}:${JSON.stringify(part.part)}`)
/** The same parts with the state taken out: a text that is all state goes, a text that ends in it loses it. */
function stateless(list: Part[]): Part[] {
  return list.flatMap((part) => {
    const at = textOf(part).indexOf(STATE_HEADER)
    if (at < 0) return [part]
    if (at === 0) return []
    return [{ role: part.role, part: { ...part.part, text: textOf(part).slice(0, at - 1) } }]
  })
}

/** How many leading segments two requests share byte for byte, and their size. */
export function sharedPrefix(a: string[], b: string[]): { segments: number; bytes: number } {
  let at = 0
  while (at < a.length && at < b.length && a[at] === b[at]) at += 1
  return { segments: at, bytes: a.slice(0, at).reduce((sum, segment) => sum + new TextEncoder().encode(segment).length, 0) }
}

/** Every string in a request body, so a text can be counted wherever the wire put it. */
function strings(value: unknown): string[] {
  if (typeof value === 'string') return [value]
  if (Array.isArray(value)) return value.flatMap(strings)
  if (value && typeof value === 'object') return Object.values(value).flatMap(strings)
  return []
}
const occurrences = (body: string, text: string) => strings(JSON.parse(body)).join('\n').split(text).length - 1

describe('where the state goes', () => {
  it('follows the endpoint: strict alternation and Anthropic thinking get the system tail, the rest a trailing user message', () => {
    expect(statePlacement('openai-compatible', 'on', {})).toBe('trailing-user')
    expect(statePlacement('gemini', 'ultra', {})).toBe('trailing-user')
    expect(statePlacement('anthropic', 'off', {})).toBe('trailing-user')
    expect(statePlacement('anthropic', 'on', {})).toBe('system-tail')
    expect(statePlacement('anthropic', 'ultra', {})).toBe('system-tail')
    expect(statePlacement('openai-compatible', 'off', { strictAlternation: true })).toBe('system-tail')
    expect(statePlacement('gemini', 'off', { statePlacement: 'system-tail' })).toBe('system-tail')
    expect(statePlacement('anthropic', 'on', { statePlacement: 'trailing-user' })).toBe('trailing-user')
    // An endpoint that refuses the trailing form never gets it.
    expect(statePlacement('openai-compatible', 'off', { strictAlternation: true, statePlacement: 'trailing-user' })).toBe('system-tail')
  })

  it.each(placementVector.items)('is where the contract’s vector says: $family, $placement, request $request', async (item) => {
    expect(item.header).toBe(STATE_HEADER)
    const family = item.family as EndpointKind
    expect(statePlacement(family, item.thinking === 'on' ? 'on' : 'off', item.capabilities as EndpointCapabilities)).toBe(item.placement)
    const { bodies } = await scenarioRun(family, {}, { capabilities: item.capabilities as EndpointCapabilities, thinking: item.thinking as 'off' | 'on' })
    const body = JSON.parse(bodies[item.request - 1]!) as unknown
    const held = item.pointer.slice(1).split('/').reduce<unknown>((value, key) => (value as Record<string, unknown>)[key], body)
    expect(typeof held).toBe('string')
    // The state is the text at the pointer from its header, which starts a line, to the end; nowhere else.
    const at = (held as string).indexOf(STATE_HEADER)
    expect(at === 0 || (held as string)[at - 1] === '\n').toBe(true)
    expect(occurrences(bodies[item.request - 1]!, STATE_HEADER)).toBe(1)
  })
})

describe.each(APIS)('the %s wire', (api) => {
  describe.each(PLACEMENTS)('with the state placed %s', (placement) => {
    it('carries the state in the person’s message first, then where the placement says, once a request', async () => {
      const { bodies, events } = await scenarioRun(api, {}, { capabilities: { statePlacement: placement } })
      expect(events.some((event) => event.type === 'proposal')).toBe(true)
      expect(bodies.length).toBeGreaterThan(2)
      for (const [at, body] of bodies.entries()) {
        const sent = parts(api, body)
        const state = sent.filter(carries)
        expect(state, `request ${at + 1} carries one state`).toHaveLength(1)
        const where = sent.indexOf(state[0]!)
        if (at === 0) {
          // The person's message, its second text part: no second user turn.
          expect(state[0]!.role).toBe('user')
          expect(textOf(state[0]!).startsWith(STATE_HEADER)).toBe(true)
          expect(textOf(sent[where - 1]!)).toContain(scenario.policy)
          expect(sent[where - 1]!.role).toBe('user')
          continue
        }
        if (placement === 'trailing-user') {
          expect(where, `request ${at + 1} ends with its state`).toBe(sent.length - 1)
          expect(state[0]!.role).toBe('user')
        } else {
          expect(state[0]!.role, `request ${at + 1} carries its state in the system text`).toBe('system')
          expect(sent.slice(where + 1).some((part) => part.role === 'system')).toBe(false)
        }
      }
    })

    it('repeats the request before it byte for byte up to the state', async () => {
      const { bodies } = await scenarioRun(api, {}, { capabilities: { statePlacement: placement } })
      const raw = bodies.map((body) => segments(parts(api, body)))
      const without = bodies.map((body) => segments(stateless(parts(api, body))))
      for (let at = 1; at < bodies.length; at += 1) {
        expect(without[at]!.slice(0, without[at - 1]!.length), `request ${at + 1} repeats request ${at}`).toEqual(without[at - 1])
      }
      // Byte for byte, before the state is taken out: the head every request shares.
      const first = parts(api, bodies[0]!)
      const prompt = first.findIndex((part) => textOf(part).includes(scenario.policy))
      const shared = sharedPrefix(raw[0]!, raw[1]!).segments
      if (placement === 'trailing-user') expect(shared, 'tools, system text and the person’s prompt').toBe(prompt + 1)
      else if (api !== 'openai-compatible') expect(shared, 'at least the system head').toBeGreaterThanOrEqual(1)
      // The system head is the same in every request (on the OpenAI-compatible
      // wire a system tail is one string with it, compared above without the state).
      if (placement === 'trailing-user' || api !== 'openai-compatible') {
        for (const body of raw) expect(body.find((segment) => segment.startsWith('system:'))).toBe(raw[0]!.find((segment) => segment.startsWith('system:')))
      }
    })

    it('sends the execution instructions and the execution state once each', async () => {
      const { bodies } = await scenarioRun(api, { allowConversation: true }, { capabilities: { statePlacement: placement } })
      for (const body of bodies) {
        expect(occurrences(body, 'TASK EXECUTION:')).toBe(1)
        expect(occurrences(body, '"requests":{"unit":"model-requests"')).toBe(1)
      }
      // The instructions are in the stable system head; the state is not.
      const head = textOf(parts(api, bodies[0]!).find((part) => part.role === 'system')!)
      expect(head).toContain(EXECUTION_INSTRUCTIONS.slice(0, 40))
      expect(head).not.toContain('model-requests')
    })

    it('carries the host’s changing text inside the state, never in the prefix', async () => {
      const snapshot = 'CHAT DRAFT SNAPSHOT (may predate the saved workspace)\n{"id":"draft-7"}'
      const { bodies } = await scenarioRun(api, { context: { currentState: snapshot } }, { capabilities: { statePlacement: placement } })
      for (const body of bodies) {
        expect(occurrences(body, 'draft-7')).toBe(1)
        expect(segments(stateless(parts(api, body))).join('')).not.toContain('draft-7')
      }
    })

    it(api === 'anthropic' ? (placement === 'trailing-user' ? 'marks three cache breakpoints, after the system head, after the tools and after the request text' : 'marks two cache breakpoints, after the system head and after the tools, and none on the request text, whose prefix grows with the state') : 'carries no cache breakpoint', async () => {
      const { bodies } = await scenarioRun(api, {}, { capabilities: { statePlacement: placement } })
      for (const body of bodies) {
        if (api !== 'anthropic') {
          expect(body).not.toContain('cache_control')
          expect(body).not.toContain('cacheControl')
          continue
        }
        const sent = JSON.parse(body) as { system: Json[]; tools: Json[]; messages: { role: string; content: Json[] }[] }
        expect(sent.system[0]!.cache_control).toEqual({ type: 'ephemeral' })
        expect(sent.tools.at(-1)!.cache_control).toEqual({ type: 'ephemeral' })
        // The request text is the first text part of the first user turn: under trailing-user it carries
        // the third breakpoint; under system-tail the state is a second system block before the messages,
        // so the bytes through it grow on every request and it carries none.
        const opening = sent.messages[0]!
        expect(opening.role).toBe('user')
        if (placement === 'trailing-user') expect(opening.content[0]).toEqual({ type: 'text', text: scenario.policy, cache_control: { type: 'ephemeral' } })
        else expect(opening.content[0]).toEqual({ type: 'text', text: scenario.policy })
        expect(body.split('"cache_control"').length - 1).toBe(placement === 'trailing-user' ? 3 : 2)
      }
    })

    if (api === 'anthropic' && placement === 'trailing-user') {
      it('puts the request text inside the cached prefix and the state after it, and that is all that moves', async () => {
        const { bodies } = await scenarioRun(api, {}, { capabilities: { statePlacement: placement } })
        const first = JSON.parse(bodies[0]!) as { messages: { content: Json[] }[] }
        // The state is a part after the request text on the first request.
        expect(String(first.messages[0]!.content[1]!.text).startsWith(STATE_HEADER)).toBe(true)
        expect(first.messages[0]!.content[1]!.cache_control).toBeUndefined()
        // What the breakpoint adds to the cached prefix: the request text, which no request changes.
        expect(new TextEncoder().encode(scenario.policy).length).toBe(230)
        for (const body of bodies) expect((JSON.parse(body) as { messages: { content: Json[] }[] }).messages[0]!.content[0]!.text).toBe(scenario.policy)
      })
    }
  })
})

/** One OpenAI-compatible assistant turn, streamed: optional text, then a tool call or the end. */
function turn(step: { text?: string; tool?: { name: string; args: unknown } }): string {
  const frame = (choices: unknown[]) => `data: ${JSON.stringify({ id: 'c', object: 'chat.completion.chunk', model: 'm', choices })}\n\n`
  const lines = [frame([{ index: 0, delta: { role: 'assistant', content: null }, finish_reason: null }])]
  if (step.text !== undefined) lines.push(frame([{ index: 0, delta: { content: step.text }, finish_reason: null }]))
  if (step.tool) {
    lines.push(frame([{ index: 0, delta: { tool_calls: [{ index: 0, id: `call_${lines.length}`, type: 'function', function: { name: step.tool.name, arguments: JSON.stringify(step.tool.args) } }] }, finish_reason: null }]))
  }
  lines.push(frame([{ index: 0, delta: {}, finish_reason: step.tool ? 'tool_calls' : 'stop' }]), 'data: [DONE]\n\n')
  return lines.join('')
}

/** One Anthropic turn, streamed, reporting the cache figures the provider returns. */
function anthropicTurn(text: string, usage: Json): string {
  const lines: string[] = []
  const event = (name: string, object: Json) => lines.push(`event: ${name}\ndata: ${JSON.stringify({ type: name, ...object })}\n\n`)
  event('message_start', { message: { id: 'm', type: 'message', role: 'assistant', model: 'm', content: [], stop_reason: null, usage } })
  event('content_block_start', { index: 0, content_block: { type: 'text', text: '' } })
  event('content_block_delta', { index: 0, delta: { type: 'text_delta', text } })
  event('content_block_stop', { index: 0 })
  event('message_delta', { delta: { stop_reason: 'end_turn', stop_sequence: null }, usage: { output_tokens: 5 } })
  event('message_stop', {})
  return lines.join('')
}

const PROPOSAL = 'Here it is.\n\n```json\n' + JSON.stringify({ proposal: { kind: 'create', document: { id: 'p' }, unknowns: [] } }) + '\n```\n'
const TOOLS: McpTool[] = [{ name: 'validate', description: 'check a document', inputSchema: { type: 'object' } }]

function session(call: ModelCall, changes: Partial<AssistantSession> = {}): AssistantSession {
  return {
    prompt: 'the runtime’s prompt', testPrompt: 'the runtime’s test_pack guidance', tools: TOOLS,
    callTool: async () => ({ content: [{ type: 'text', text: '{"status":"ok"}' }] }), hostTools: [],
    model: { family: 'openai-compatible', model: 'a-model', call }, thinking: normalize('off', 'openai-compatible'),
    signal: new AbortController().signal, ...changes
  }
}
async function drain(events: AsyncIterable<AssistantEvent>): Promise<AssistantEvent[]> {
  const seen: AssistantEvent[] = []
  for await (const event of events) seen.push(event)
  return seen
}

describe('the critic’s step note', () => {
  it.each(PLACEMENTS)('carries the state header, placed %s like every state', async (placement) => {
    configureHarness({ endpointCapabilities: () => ({ statePlacement: placement }) })
    const critic = [
      turn({ tool: { name: 'validate', args: { document: '{"id":"a"}' } } }),
      turn({ tool: { name: 'validate', args: { document: '{"id":"b"}' } } }),
      turn({ tool: { name: 'validate', args: { document: '{"id":"c"}' } } }),
      turn({ text: 'REFUTATION: nothing to report.' })
    ]
    const reviews: string[] = []
    const call: ModelCall = async (_suffix, request) => {
      const reviewing = request.body.includes(REFUTATION_MARKER)
      if (reviewing) reviews.push(request.body)
      const answer = reviewing ? critic[Math.min(reviews.length - 1, critic.length - 1)]! : turn({ text: PROPOSAL })
      return new Response(answer, { status: 200, headers: { 'content-type': 'text/event-stream' } })
    }
    const events = await drain(vercel.start(session(call, { adversarialReview: true })))
    expect(events.some((event) => event.type === 'proposal')).toBe(true)
    // The critic's last request is its final one: tools closed, a step note sent.
    expect(reviews.length).toBe(4)
    const texts = strings(JSON.parse(reviews.at(-1)!)).filter((text) => text.includes(STATE_HEADER))
    expect(texts, 'the note, headed').toHaveLength(1)
    const note = texts[0]!.slice(texts[0]!.indexOf(STATE_HEADER) + STATE_HEADER.length)
    expect(note.trim().length).toBeGreaterThan(0)
    if (placement === 'trailing-user') expect(texts[0]!.startsWith(STATE_HEADER)).toBe(true)
    // The critic's earlier requests had no note, and so no header.
    for (const body of reviews.slice(0, -1)) expect(body).not.toContain(STATE_HEADER)
  })
})

describe('one model per run', () => {
  it('keeps asking at the run’s tier after the slot concludes, from absence, that the endpoint does not reason', async () => {
    const answers = [
      turn({ text: 'Checking the draft.', tool: { name: 'validate', args: { document: '{}' } } }),
      turn({ text: 'Checking it again, changed.', tool: { name: 'validate', args: { document: '{"id":"p"}' } } }),
      // The slot concludes when the turn after the second quiet answer starts,
      // so the request after that is the first one a per-step tier would change.
      turn({ text: 'One more check.', tool: { name: 'validate', args: { document: '{"id":"q"}' } } }),
      turn({ text: PROPOSAL })
    ]
    const sent: Json[] = []
    const call: ModelCall = async (_suffix, request) => {
      sent.push(JSON.parse(request.body) as Json)
      return new Response(answers[Math.min(sent.length - 1, answers.length - 1)], { status: 200, headers: { 'content-type': 'text/event-stream' } })
    }
    const events = await drain(vercel.start(session(call, { thinking: normalize('on', 'openai-compatible'), adversarialReview: false })))
    // The slot concluded after two quiet answers, and said so…
    expect(events.filter((event) => event.type === 'thinking_unavailable')).toHaveLength(1)
    expect(sent).toHaveLength(4)
    // …and nothing sent in this run changed: every request asks at the same tier.
    for (const body of sent) expect(body.reasoning_effort).toBe('high')
    expect(events.some((event) => event.type === 'proposal')).toBe(true)
  })
})

describe('the provider’s cache figures', () => {
  it('are read from the SDK’s usage and kept by the completion record', () => {
    const completion = modelCompletion('stop', { inputTokens: 120, outputTokens: 5, inputTokenDetails: { cacheReadTokens: 100, cacheWriteTokens: 20, noCacheTokens: 0 } })
    expect(completion).toEqual({ finishReason: 'stop', inputTokens: 120, outputTokens: 5, cacheReadTokens: 100, cacheWriteTokens: 20 })
    expect(readCompletion(completion)).toEqual(completion)
    expect(readCompletion({ finishReason: 'stop', cacheReadTokens: -1 })).toBeUndefined()
    // A provider that reports none leaves them out.
    expect(modelCompletion('stop', { inputTokens: 3, outputTokens: 1 })).toEqual({ finishReason: 'stop', inputTokens: 3, outputTokens: 1 })
  })

  it('are recorded from an Anthropic answer in the run’s checkpoint', async () => {
    const call: ModelCall = async () =>
      new Response(anthropicTurn(PROPOSAL, { input_tokens: 40, output_tokens: 0, cache_read_input_tokens: 1800, cache_creation_input_tokens: 64 }), { status: 200, headers: { 'content-type': 'text/event-stream' } })
    const events = await drain(vercel.start(session(call, {
      model: { family: 'anthropic', model: 'a-model', call },
      thinking: normalize('off', 'anthropic'),
      context: { currentRequest: { message: 'Write the pack', input: '' } }
    })))
    const checkpoints = events.filter((event): event is Extract<AssistantEvent, { type: 'execution_checkpoint' }> => event.type === 'execution_checkpoint')
    const completions = (checkpoints.at(-1)!.checkpoint as ExecutionCheckpoint).completions!
    expect(completions[0]!.result).toMatchObject({ finishReason: 'stop', cacheReadTokens: 1800, cacheWriteTokens: 64 })
  })
})
