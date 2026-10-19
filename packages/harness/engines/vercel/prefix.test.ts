/**
 * A stable prompt prefix, with cache breakpoints (agent#9).
 *
 * The request is ordered system text, tools, the host's prompt, the history,
 * and then, in one message at the end, what changes between requests: the
 * counters, the plan, the operations index, the host's changing text and the
 * step's own instruction. So a second step's request repeats the first's byte
 * for byte up to the history. The execution instructions are sent once.
 * Anthropic gets two breakpoints, after the system text and after the tools,
 * and no other wire gets any. The tier is held for the run. The provider's
 * cache reads and writes are recorded with each completion.
 *
 * Measured on the recorded scenario, over the scripted model and the recorded
 * runtime the certification suite uses.
 */
import { afterEach, describe, expect, it, vi } from 'vitest'
import { vercel } from './index'
import { STATE_HEADER } from './loop'
import { scenario, scriptedModel, scriptedRuntime } from '../../certification'
import { bindModelCall, openAssistantConnection, runAssistantSession } from '../../session'
import { EXECUTION_INSTRUCTIONS } from '../../execution/controller'
import { modelCompletion, readCompletion } from '../../completion'
import { normalize } from '../../thinking'
import type { EndpointKind } from '../../host'
import type { AssistantEvent, AssistantSession, ModelCall, McpTool } from '../../engine'
import type { ExecutionCheckpoint } from '../../execution/state'

afterEach(() => vi.unstubAllGlobals())

const APIS: EndpointKind[] = ['openai-compatible', 'anthropic', 'gemini']

/** Run the recorded scenario on one wire and keep every request body the engine sent. */
async function scenarioRun(api: EndpointKind, changes: Partial<AssistantSession> = {}): Promise<{ bodies: string[]; events: AssistantEvent[] }> {
  const model = scriptedModel({ api, answerAs: 'stream', thinking: 'off', refuted: false, signatures: 'thought' })
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
      thinking: normalize('off', api),
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
/**
 * One request as the provider's cache sees it, in prefix order, one segment per
 * block: the tools and the system text first (Anthropic puts tools before the
 * system; Gemini the system instruction before the tools; OpenAI-compatible
 * sends the system as the first message), then every message part in order.
 */
function segments(api: EndpointKind, body: string): string[] {
  const sent = JSON.parse(body) as Json
  if (api === 'anthropic') {
    const parts = (sent.messages as { role: string; content: unknown }[]).flatMap((message) =>
      (typeof message.content === 'string' ? [{ type: 'text', text: message.content }] : (message.content as unknown[])).map((part) => `${message.role}:${JSON.stringify(part)}`))
    return [JSON.stringify(sent.tools), JSON.stringify(sent.system), ...parts]
  }
  if (api === 'gemini') {
    const parts = (sent.contents as { role: string; parts: unknown[] }[]).flatMap((content) => content.parts.map((part) => `${content.role}:${JSON.stringify(part)}`))
    return [JSON.stringify(sent.systemInstruction), JSON.stringify(sent.tools), ...parts]
  }
  return [JSON.stringify(sent.tools), ...(sent.messages as unknown[]).map((message) => JSON.stringify(message))]
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

describe.each(APIS)('the %s wire', (api) => {
  it('repeats the first request byte for byte up to the history, and puts what changes last', async () => {
    const { bodies, events } = await scenarioRun(api)
    expect(events.some((event) => event.type === 'proposal')).toBe(true)
    const [first, second] = [segments(api, bodies[0]!), segments(api, bodies[1]!)]
    // Tools, system text and the host's prompt: three segments, identical.
    const shared = sharedPrefix(first, second)
    expect(shared.segments, 'the prefix the second step shares with the first').toBe(3)
    expect(first[2]).toContain(scenario.policy)
    // Where they part is where the history begins: the first step's state against the second's call.
    expect(first[3]).toContain(STATE_HEADER)
    expect(second[3]).not.toContain(STATE_HEADER)
    // Every request ends with its state, and repeats the one before it up to that state.
    for (const [at, body] of bodies.entries()) {
      const sent = segments(api, body)
      expect(sent.at(-1), `request ${at} ends with its state`).toContain(STATE_HEADER)
      if (at === 0) continue
      const before = segments(api, bodies[at - 1]!)
      expect(sharedPrefix(before, sent).segments, `request ${at} repeats request ${at - 1} up to its state`).toBe(before.length - 1)
    }
  })

  it('sends the execution instructions and the execution state once each', async () => {
    const { bodies } = await scenarioRun(api, { allowConversation: true })
    for (const body of bodies) {
      expect(occurrences(body, 'TASK EXECUTION:')).toBe(1)
      expect(occurrences(body, '"requests":{"unit":"model-requests"')).toBe(1)
    }
    // The instructions are in the stable system text; the state is not.
    const system = segments(api, bodies[0]!)[api === 'gemini' ? 0 : 1]!
    expect(system).toContain(JSON.stringify(EXECUTION_INSTRUCTIONS).slice(1, 40))
    expect(system).not.toContain('model-requests')
  })

  it('carries the host’s changing text at the end of every request, never in the prefix', async () => {
    const snapshot = 'CHAT DRAFT SNAPSHOT (may predate the saved workspace)\n{"id":"draft-7"}'
    const { bodies } = await scenarioRun(api, { context: { currentState: snapshot } })
    for (const body of bodies) {
      const sent = segments(api, body)
      expect(sent.at(-1)).toContain('draft-7')
      expect(sent.slice(0, -1).join('')).not.toContain('draft-7')
    }
  })

  it(api === 'anthropic' ? 'marks two cache breakpoints, after the system text and after the tools' : 'carries no cache breakpoint', async () => {
    const { bodies } = await scenarioRun(api)
    for (const body of bodies) {
      if (api !== 'anthropic') {
        expect(body).not.toContain('cache_control')
        expect(body).not.toContain('cacheControl')
        continue
      }
      const sent = JSON.parse(body) as { system: Json[]; tools: Json[]; messages: unknown[] }
      expect(sent.system.at(-1)!.cache_control).toEqual({ type: 'ephemeral' })
      expect(sent.tools.at(-1)!.cache_control).toEqual({ type: 'ephemeral' })
      expect(body.split('"cache_control"').length - 1).toBe(2)
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
