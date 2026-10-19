/**
 * Project memory after the first tool step (agent#11, the harness side).
 *
 * The Desk server recalls project memory for every relayed request from the request text. The
 * package guarantees that text can be found on every request of a run, on every wire: it is the
 * first text part of the first user turn, which nothing the loop does moves, replaces or drops.
 * The contract states the rule a server applies (`budget.json#/$defs/recallQuery`, written in code
 * in `certification/recall.ts`), with one vector per wire, each checked here against a run. Then
 * the rule is applied to every request of runs that add a user text after the person's message
 * in each way the loop can: a text-path result, the finishing instruction, the reformat request,
 * the compaction summary and a chat's index; and where the prompt is previewed and for a delegated
 * child, whose request text is sent as its own first text part. The certification suite applies it
 * to every request of the recorded scenario on every certified leg (21 legs in four families).
 *
 * And the memory reserve: `memoryBytes` is left out of a run's input allowance only while the host
 * has not said memory is off.
 */
import { afterEach, describe, expect, it, vi } from 'vitest'
import openaiVector from '../../../contract/vectors/recall/openai-compatible.json'
import anthropicVector from '../../../contract/vectors/recall/anthropic.json'
import geminiVector from '../../../contract/vectors/recall/gemini.json'
import childVector from '../../../contract/vectors/recall/delegated-child.json'
import { atPointer, recallQuery } from '../certification/recall'
import { scenario, scriptedModel, scriptedRuntime } from '../certification'
import { STATE_HEADER } from '../engines/vercel/loop'
import { vercel } from '../engines/vercel'
import { bindModelCall, openAssistantConnection, runAssistantSession } from '../session'
import { configureHarness, type EndpointCapabilities, type EndpointKind } from '../host'
import { normalize } from '../thinking'
import { ChatContext, INDEX_NOTICE } from '../context/chat'
import { CONTEXT_DEFAULTS, contextPolicy, inputBytes, utf8Bytes } from '../context/policy'
import { SessionContext } from '../context/session'
import { FINISH_RESPONSE_INSTRUCTIONS } from '../completion'
import { answer } from '../context/ledger'
import { runtimeAnswer, scriptedEndpoint, sseTurn, streamed, vercelTurn, type EndpointRequest } from '../testing/scripted'
import type { AssistantEvent, AssistantSession, Engine, EngineSession, ModelCall } from '../engine'

afterEach(() => vi.unstubAllGlobals())

type Json = Record<string, unknown>

/** The recorded scenario on one wire, every request body kept. */
async function scenarioBodies(api: EndpointKind, endpoint: { capabilities?: EndpointCapabilities; toolCalls?: 'native' | 'text' } = {}): Promise<Json[]> {
  configureHarness({ endpointCapabilities: () => endpoint.capabilities ?? {} })
  const model = scriptedModel({ api, answerAs: 'stream', thinking: 'off', refuted: false, signatures: 'thought', toolCalls: endpoint.toolCalls ?? 'native' })
  vi.stubGlobal('fetch', model.fetch)
  const runtime = await scriptedRuntime()
  const events: AssistantEvent[] = []
  const connection = openAssistantConnection({ allowed: scenario.scenarioTools, onEvent: (event) => events.push(event), transport: runtime.transport })
  const bodies: Json[] = []
  try {
    const ready = await connection.ready
    const bound = bindModelCall(api)
    const call: ModelCall = async (suffix, request) => {
      bodies.push(JSON.parse(request.body) as Json)
      return bound(suffix, request)
    }
    await runAssistantSession(vercel, {
      prompt: scenario.policy,
      testPrompt: '',
      tools: ready.tools,
      callTool: ready.callTool,
      hostTools: [],
      model: { family: api, model: 'scripted-model', call },
      thinking: normalize('off', api),
      adversarialReview: false,
      signal: new AbortController().signal
    }, (event) => events.push(event))
  } finally {
    await connection.close()
    await runtime.close()
  }
  expect(events.filter((event) => event.type === 'error')).toEqual([])
  return bodies
}

/**
 * A request's turns as the vectors abbreviate them: each turn's role, and for a user turn the kind
 * of each part (the request text, a tool result, the state), in order.
 */
function turns(api: EndpointKind, body: Json): string[] {
  const list = (api === 'gemini' ? body.contents : body.messages) as Json[]
  const kind = (part: Json): string => {
    const text = api === 'gemini' ? part.text : part.type === 'text' ? part.text : undefined
    if (typeof text === 'string') return text.startsWith(STATE_HEADER) ? 'state' : 'text'
    return 'functionResponse' in part || part.type === 'tool_result' ? 'result' : String(part.type ?? Object.keys(part)[0])
  }
  return list.map((turn) => {
    if (turn.role !== 'user') return String(turn.role)
    const held = api === 'gemini' ? turn.parts : turn.content
    const parts = typeof held === 'string' ? [{ type: 'text', text: held } as Json] : (held as Json[])
    return `user:${parts.map(kind).join('+')}`
  })
}

const VECTORS = [openaiVector, anthropicVector, geminiVector]

describe('where the request text sits, one vector per wire', () => {
  it('has one vector per wire, each resolving on its own body to its pointer and text', () => {
    expect(VECTORS.map((vector) => vector.items[0]!.family)).toEqual(['openai-compatible', 'anthropic', 'gemini'])
    for (const vector of VECTORS) {
      const item = vector.items[0]!
      expect(recallQuery(item.family as EndpointKind, item.body as Json)).toEqual({ pointer: item.pointer, text: item.text })
      expect(atPointer(item.body, item.pointer)).toBe(item.text)
      expect(item.text).toBe(scenario.policy)
    }
  })

  it.each(VECTORS.map((vector) => vector.items[0]!))('is what a run sends on its third request ($family)', async (item) => {
    const api = item.family as EndpointKind
    const third = (await scenarioBodies(api))[item.request - 1]!
    expect(turns(api, third)).toEqual(turns(api, item.body as Json))
    expect(recallQuery(api, third)).toEqual({ pointer: item.pointer, text: item.text })
    // The rule's pointer is where the text sits whatever else the request carries.
    expect(atPointer(third, item.pointer)).toBe(scenario.policy)
  })
})

/** Every request's rule result, where it is not `text`. */
function misses(api: EndpointKind, bodies: Json[], text: string): string[] {
  return bodies.flatMap((body, at) => {
    const found = recallQuery(api, body)
    return found?.text === text ? [] : [`request ${at + 1}: ${found === null ? 'nothing' : found.pointer}`]
  })
}
const APIS: EndpointKind[] = ['openai-compatible', 'anthropic', 'gemini']
const texts = (requests: EndpointRequest[]) => requests.map((request) => request.body as Json)

describe('the request text on every request of a run with tool calls', () => {
  it.each(APIS)('holds on %s with the state trailing, in the system tail, and on the text path', async (api) => {
    for (const endpoint of [{}, { capabilities: { statePlacement: 'system-tail' as const } }, { capabilities: { nativeToolCalls: false }, toolCalls: 'text' as const }]) {
      const bodies = await scenarioBodies(api, endpoint)
      expect(bodies.length).toBeGreaterThan(7)
      expect(misses(api, bodies, scenario.policy)).toEqual([])
    }
  })

  it('holds where the newest user text is not the person’s: a text-path result after the request', async () => {
    const bodies = await scenarioBodies('openai-compatible', { capabilities: { nativeToolCalls: false }, toolCalls: 'text' })
    const newest = (body: Json) => [...(body.messages as Json[])].reverse().find((message) => message.role === 'user' && typeof message.content === 'string' && !String(message.content).startsWith(STATE_HEADER))
    // From the second request on, a user text that is a result comes after the person's message.
    expect(String(newest(bodies[2]!)!.content)).toMatch(/^TOOL RESULT from /)
    expect(misses('openai-compatible', bodies, scenario.policy)).toEqual([])
  })

  const PROMPT = 'Check the expense pack.'

  it('holds on the finishing request, whose instruction is a user text after the history', async () => {
    const turn = await vercelTurn({
      answer: (request) =>
        request.number === 1
          ? streamed(sseTurn({ tool: { name: 'get_schema', args: {}, id: 'call_schema' } }))
          : request.number === 2
            ? streamed(sseTurn({ text: '' }))
            : streamed(sseTurn({ text: 'The pack is unchanged.' }))
    })
    expect(turn.requests).toHaveLength(3)
    const last = (turn.requests[2]!.body.messages ?? []).at(-1)!
    expect(JSON.stringify(last.content)).toContain(FINISH_RESPONSE_INSTRUCTIONS)
    expect(misses('openai-compatible', texts(turn.requests), PROMPT)).toEqual([])
  })

  it('holds on the reformat request, a user text after the answer it could not read', async () => {
    const pack = { specVersion: '0.2.0-draft', id: 'expense' }
    const turn = await vercelTurn({
      answer: (request) =>
        request.number === 1
          ? streamed(sseTurn({ text: 'Here it is.\n```json\n{"proposal": {\n```' }))
          : streamed(sseTurn({ text: '```json\n' + JSON.stringify({ proposal: { kind: 'create', document: pack, unknowns: [] } }) + '\n```' })),
      session: { allowConversation: false }
    })
    expect(turn.requests).toHaveLength(2)
    expect(turn.events.some((event) => event.type === 'proposal')).toBe(true)
    expect(misses('openai-compatible', texts(turn.requests), PROMPT)).toEqual([])
  })

  it('holds after compaction, whose summary is a user text after the request', async () => {
    const large = 'x'.repeat(9000)
    const turn = await vercelTurn({
      answer: (request) =>
        request.number <= 6
          ? streamed(sseTurn({ tool: { name: 'read_source', args: { page: request.number }, id: `call_${request.number}` } }))
          : streamed(sseTurn({ text: 'Read.' })),
      hostTools: [{ name: 'read_source', effect: 'read', description: 'Read one page of a source.', inputSchema: { type: 'object', properties: { page: { type: 'integer' } }, required: ['page'], additionalProperties: false }, execute: async (args) => answer({ page: args.page, text: large }) }],
      session: { context: { currentRequest: { message: PROMPT, input: PROMPT }, configuration: { version: 1, models: { 'scripted-model': { contextWindowTokens: 24576, outputTokens: 4096 } } } } }
    })
    const compacted = turn.requests.filter((request) => JSON.stringify(request.body).includes('Earlier completed steps were compacted'))
    expect(compacted.length).toBeGreaterThan(0)
    expect(misses('openai-compatible', texts(turn.requests), PROMPT)).toEqual([])
  })

  it('holds with a chat’s index in the first message: the index is the second part', async () => {
    const chat = new ChatContext()
    const run = () =>
      vercelTurn({
        answer: (request) =>
          request.number === 1 && !JSON.stringify(request.body).includes(INDEX_NOTICE.slice(0, 40))
            ? streamed(sseTurn({ tool: { name: 'get_schema', args: {}, id: 'call_schema' } }))
            : request.number === 1 ? streamed(sseTurn({ tool: { name: 'list_examples', args: {}, id: 'call_list' } })) : streamed(sseTurn({ text: 'Done.' })),
        handle: () => runtimeAnswer({ status: 'ok' }),
        session: { context: { currentRequest: { message: PROMPT, input: PROMPT }, chat, runtime: { identity: 'jpack', version: '0.19.0' } } }
      })
    await run()
    const second = await run()
    const first = second.requests[0]!.body.messages!.find((message) => message.role === 'user')!
    expect((first.content as { text: string }[])[1]!.text.startsWith(INDEX_NOTICE)).toBe(true)
    expect(recallQuery('openai-compatible', second.requests[0]!.body as Json)!.pointer).toBe('/messages/1/content/0/text')
    expect(misses('openai-compatible', texts(second.requests), PROMPT)).toEqual([])
  })
})

/** A session with no tools, and the host's word on memory where given. */
const session = (memory?: boolean): AssistantSession => ({
  prompt: '', testPrompt: '', tools: [], callTool: async () => ({}), hostTools: [],
  model: { family: 'openai-compatible', model: 'scripted-model', call: async () => new Response() },
  thinking: normalize('off', 'openai-compatible'), signal: new AbortController().signal,
  ...(memory === undefined ? {} : { context: { memory } })
})

describe('the request text where the prompt is previewed, and for a delegated child', () => {
  it('sends the person’s message as the first text part when the prompt is previewed (a 400-turn transcript at a 32,768-token window)', async () => {
    const message = 'Add a rule that sends a claim above 1000 USD to finance.'
    const transcript = Array.from({ length: 400 }, (_, at) => `PERSON: Turn ${at}: look at the expense pack again.\n\nASSISTANT: Turn ${at}: the pack holds; nothing changed.`).join('\n\n')
    const prompt = `TRANSCRIPT SO FAR\n${transcript}\n\nTHE PERSON'S MESSAGE\n${message}`
    const turn = await vercelTurn({
      answer: (request) => (request.number === 1 ? streamed(sseTurn({ tool: { name: 'get_schema', args: {}, id: 'call_schema' } })) : streamed(sseTurn({ text: 'Done.' }))),
      session: { prompt, context: { currentRequest: { message, input: message }, configuration: { version: 1, models: { 'scripted-model': { contextWindowTokens: 32768, outputTokens: 8192 } } } } }
    })
    expect(turn.events.filter((event) => event.type === 'error')).toEqual([])
    expect(turn.requests).toHaveLength(2)
    const first = turn.requests[0]!.body.messages!.find((message) => message.role === 'user')!
    const parts = (first.content as { text: string }[]).map((part) => part.text)
    expect(parts[0]).toBe(message)
    expect(parts[1]).toMatch(/^Earlier context is retained/)
    expect(misses('openai-compatible', texts(turn.requests), message)).toEqual([])
  })

  it('sends a delegated child’s objective as the first text part of its first message, as the vector states', async () => {
    const { withDelegation } = await import('../delegation')
    const item = childVector.items[0]!
    const captured: EngineSession[] = []
    const engine: Engine = {
      id: 'vercel',
      async *start(started) {
        captured.push(started)
        if (captured.length === 1) yield { type: 'delegation_requested', request: { tasks: [{ title: 'Check', profile: 'review', objective: item.text, context: 'Supplied work. '.repeat(300) }] } }
        else yield { type: 'message', text: 'Checked.' }
        yield { type: 'end' }
      }
    }
    for await (const event of withDelegation(engine).start({ ...session(), allowConversation: true, interactive: true, adversarialReview: false })) void event
    const child = captured[1] as AssistantSession
    expect(child.context?.objective).toBe(item.text)
    const endpoint = scriptedEndpoint(() => streamed(sseTurn({ text: 'Checked.' })))
    vi.stubGlobal('fetch', endpoint.fetch)
    const call = bindModelCall('openai-compatible')
    const events: AssistantEvent[] = []
    // The coordinator's run is over and the child's signal with it: the same session, run again on a live one.
    for await (const event of vercel.start({ ...child, signal: new AbortController().signal, model: { ...child.model, call } })) events.push(event)
    expect(events.filter((event) => event.type === 'error')).toEqual([])
    const body = endpoint.requests[0]!.body as Json
    expect(turns('openai-compatible', body)).toEqual(turns('openai-compatible', item.body as Json))
    expect(recallQuery('openai-compatible', body)).toEqual({ pointer: item.pointer, text: item.text })
    expect(recallQuery('openai-compatible', item.body as Json)).toEqual({ pointer: item.pointer, text: item.text })
  })
})

describe('the memory reserve', () => {
  it('is reserved only when memory may be on: off frees it, on and unsaid keep today’s', () => {
    expect(contextPolicy(undefined, 'scripted-model', undefined, false).memoryBytes).toBe(0)
    expect(contextPolicy(undefined, 'scripted-model', undefined, true).memoryBytes).toBe(CONTEXT_DEFAULTS.memoryBytes)
    expect(contextPolicy(undefined, 'scripted-model').memoryBytes).toBe(CONTEXT_DEFAULTS.memoryBytes)
    expect(CONTEXT_DEFAULTS.memoryBytes).toBe(4000)
  })

  it('leaves a run with memory off the full input allowance, and one with it on the allowance less the reserve', () => {
    const instructions = 'instructions', tools: unknown[] = []
    // The allowance with no memory reserve: the input bytes less the instructions, the tools,
    // the brief and the 1,536 bytes the budget keeps for the request's own framing.
    const full = inputBytes(CONTEXT_DEFAULTS) - utf8Bytes(instructions) - utf8Bytes(JSON.stringify(tools)) - CONTEXT_DEFAULTS.briefBytes - 1536
    const prompt = (bytes: number) => 'p'.repeat(bytes)
    const off = new SessionContext(session(false))
    expect(() => off.check(instructions, prompt(full), tools)).not.toThrow()
    expect(() => off.check(instructions, prompt(full + 1), tools)).toThrow()
    for (const on of [new SessionContext(session(true)), new SessionContext(session())]) {
      expect(() => on.check(instructions, prompt(full - 4000), tools)).not.toThrow()
      expect(() => on.check(instructions, prompt(full - 4000 + 1), tools)).toThrow()
      on.close()
    }
    off.close()
  })

  it('reaches a delegated child: the child works under its parent’s word on memory, and gets no chat context', async () => {
    const { withDelegation } = await import('../delegation')
    const chat = new ChatContext()
    const sessions: EngineSession[] = []
    const engine: Engine = {
      id: 'vercel',
      async *start(started) {
        sessions.push(started)
        if (sessions.length === 1) yield { type: 'delegation_requested', request: { tasks: [{ title: 'Check', profile: 'review', objective: 'Check the claims.', context: 'Supplied.' }] } }
        else yield { type: 'message', text: 'Checked.' }
        yield { type: 'end' }
      }
    }
    const parent = { ...session(false), allowConversation: true, interactive: true, adversarialReview: false, context: { memory: false, chat } }
    for await (const event of withDelegation(engine).start(parent)) void event
    expect(sessions).toHaveLength(3)
    // The child: memory as the parent said, no chat context. The parent's continuation: both.
    expect(sessions[1]!.context).toMatchObject({ memory: false })
    expect(sessions[1]!.context?.chat).toBeUndefined()
    expect(sessions[2]!.context).toMatchObject({ memory: false, chat })
  })
})
