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
 * the compaction summary and a chat's index. The certification suite applies it to every request
 * of the recorded scenario on every leg.
 *
 * And the memory reserve: `memoryBytes` is left out of a run's input allowance only while the host
 * has not said memory is off.
 */
import { afterEach, describe, expect, it, vi } from 'vitest'
import openaiVector from '../../../contract/vectors/recall/openai-compatible.json'
import anthropicVector from '../../../contract/vectors/recall/anthropic.json'
import geminiVector from '../../../contract/vectors/recall/gemini.json'
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
import { ContextLedger, answer } from '../context/ledger'
import { runtimeAnswer, sseTurn, streamed, vercelTurn, type EndpointRequest } from '../testing/scripted'
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
  const texts = (requests: EndpointRequest[]) => requests.map((request) => request.body as Json)

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

describe('the memory reserve', () => {
  const session = (memory?: boolean): AssistantSession => ({
    prompt: '', testPrompt: '', tools: [], callTool: async () => ({}), hostTools: [],
    model: { family: 'openai-compatible', model: 'scripted-model', call: async () => new Response() },
    thinking: normalize('off', 'openai-compatible'), signal: new AbortController().signal,
    ...(memory === undefined ? {} : { context: { memory } })
  })

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

  // agent#14 and agent#15 rewrote this case: the parent is one run, and a child is handed a view of
  // the parent's chat that shows it its references alone, never the chat itself.
  it('reaches a delegated child: the child works under its parent’s word on memory, and gets a view of the chat, not the chat', async () => {
    const { withDelegation } = await import('../delegation')
    const chat = new ChatContext()
    // An earlier run of the chat kept one entry.
    const earlier = chat.openTurn()
    const kept = await new ContextLedger(CONTEXT_DEFAULTS, earlier).retain({ id: 'earlier-note', kind: 'history', label: 'An earlier run’s note', value: 'Private to the parent.' })
    earlier.release()
    const sessions: EngineSession[] = []
    const engine: Engine = {
      id: 'vercel',
      async *start(started) {
        sessions.push(started)
        const delegate = started.hostTools.find((tool) => tool.name === 'delegate_tasks')
        if (delegate) await delegate.execute({ tasks: [{ title: 'Check', profile: 'review', objective: 'Check the claims.' }] }, started.signal)
        yield { type: 'message', text: 'Checked.' }
        yield { type: 'end' }
      }
    }
    const parent = { ...session(false), allowConversation: true, interactive: true, adversarialReview: false, context: { memory: false, chat } }
    for await (const event of withDelegation(engine).start(parent)) void event
    expect(sessions).toHaveLength(2)
    // The parent: memory as the host said, and the chat. The child: memory as the parent said, and a view of the chat.
    expect(sessions[0]!.context).toMatchObject({ memory: false, chat })
    expect(sessions[1]!.context).toMatchObject({ memory: false })
    expect(sessions[1]!.context?.chat).toBeDefined()
    expect(sessions[1]!.context?.chat).not.toBe(chat)
    // The view opens a turn that sees nothing it was not given, and makes no index; the chat still holds it.
    const view = sessions[1]!.context!.chat!.openTurn()
    expect([...view.entries.keys()]).toEqual([])
    expect(view.use(kept.id)).toBeUndefined()
    expect(view.missing(kept.id)).toContain('not among this task’s references')
    expect(view.index(CONTEXT_DEFAULTS, 'marker')).toBe('')
    view.release()
    expect(chat.entries.has(kept.id)).toBe(true)
  })
})
