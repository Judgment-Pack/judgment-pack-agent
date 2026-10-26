import {typedMemory} from './testing/memory'
/**
 * The prompt is the adapter's (ADR-0005), the harness side.
 *
 * - **The host's parts.** `brief()`, `projectInstructions()` and `memoryFor(query)` are asked once
 *   per run, before its first request; each answers text or nothing, held to its bound and refused
 *   past it; each is placed in its layer of the system text, after the package's guidance, as the
 *   vector `budget/host-parts.json` states. The certification suite checks the placement on every
 *   wire and engine (`certification/conformance.test.ts`); here the reading and the assembler alone.
 * - **The request text.** Where the prompt does not fit its allowance, the person's message leads
 *   the preview; a delegated child's objective leads its first message and is its memory query.
 * - **No reserve.** `briefBytes` and `memoryBytes` are the parts' bounds and nothing is taken out of a
 *   run's input allowance for either: a part that is present is in the instructions and counted there.
 * - **The prompt's digest.** Every request body the adapter sent is digested in the checkpoint's
 *   `prompts`, in order, on a model engine and on the agent wire (`provenance/prompt-digest.json`).
 */
import {frameBlock} from './framing'
import { afterEach, describe, expect, it, vi } from 'vitest'
import hostPartsVector from '../../contract/vectors/budget/host-parts.json'
import digestVector from '../../contract/vectors/provenance/prompt-digest.json'
import { HOST_PARTS, BRIEF_HEADER, MEMORY_HEADER, PROJECT_INSTRUCTIONS_HEADER, hostSection, readHostParts, systemText } from './prompt'
import { configureHarness } from './host'
import { CONTEXT_DEFAULTS, contextPolicy, inputBytes, utf8Bytes } from './context/policy'
import { SessionContext } from './context/session'
import { contextDigest } from './context/ledger'
import { readExecution, type ExecutionCheckpoint } from './execution/state'
import { vercel } from './engines/vercel'
import { normalize } from './thinking'
import { bindAgentRun } from './agentTransport'
import { bindModelCall } from './session'
import { codexTurn, scriptedEndpoint, sseTurn, streamed, vercelTurn, type EndpointRequest } from './testing/scripted'
import { FINISH_RESPONSE_INSTRUCTIONS } from './completion'
import type { AgentRequest, AgentRun } from './agent'
import type { AgentAssistantSession, AssistantEvent, AssistantSession, ModelCall } from './engine'

afterEach(() => {
  vi.unstubAllGlobals()
  configureHarness({ brief: () => undefined, memoryFor: () => undefined, projectInstructions: () => undefined })
})

const lastCheckpoint = (events: AssistantEvent[]) =>
  (events.filter((event) => event.type === 'execution_checkpoint').at(-1) as { checkpoint: ExecutionCheckpoint }).checkpoint

describe('the host’s parts of the prompt', () => {
  it('are the vector’s three, in their layers’ order, each bounded by a member of the policy table', () => {
    expect(HOST_PARTS.map(({ name, layer, injection, header, end, bound }) => ({ name, layer, injection, header, end, bound }))).toEqual(hostPartsVector.items)
    expect([BRIEF_HEADER, PROJECT_INSTRUCTIONS_HEADER, MEMORY_HEADER]).toEqual(hostPartsVector.items.map((item) => item.header))
    expect(CONTEXT_DEFAULTS.projectInstructionsBytes).toBe(4000)
    for (const part of HOST_PARTS) expect(CONTEXT_DEFAULTS[part.bound]).toBeGreaterThan(0)
  })

  it('are placed after the package’s guidance in layer order, before the tools described in text, and add nothing where there are none', () => {
    expect(systemText('GUIDANCE', {}, '\n\nTOOLS')).toBe('GUIDANCE\n\nTOOLS')
    expect(hostSection({})).toBe('')
    const parts = { memory: 'Recalled.', brief: 'The brief.', projectInstructions: 'Be brief.' }
    expect(systemText('GUIDANCE', parts, '\n\nTOOLS')).toBe(
      `GUIDANCE\n\n${BRIEF_HEADER}\nThe brief.\nEND PROJECT BRIEF\n\n${PROJECT_INSTRUCTIONS_HEADER}\nBe brief.\nEND PROJECT INSTRUCTIONS\n\n${MEMORY_HEADER}\nRecalled.\nEND PROJECT MEMORY\n\nTOOLS`
    )
    // A part left out leaves no block, and the others keep their order.
    expect(systemText('GUIDANCE', { memory: 'Recalled.', brief: 'The brief.' })).toBe(`GUIDANCE\n\n${BRIEF_HEADER}\nThe brief.\nEND PROJECT BRIEF\n\n${MEMORY_HEADER}\nRecalled.\nEND PROJECT MEMORY`)
  })

  it('are asked once each, memory with the query, and an answer of nothing leaves the part out', async () => {
    const asked: string[] = []
    let briefs = 0
    configureHarness({ brief: async () => { briefs++; return 'The brief.' }, projectInstructions: () => null, memoryFor: (query) => { asked.push(query); return undefined } })
    expect(await readHostParts('the person’s message', CONTEXT_DEFAULTS)).toEqual({ brief: 'The brief.' })
    expect(briefs).toBe(1)
    expect(asked).toEqual(['the person’s message'])
  })

  it('takes an empty objective as none: memory is asked with the person’s message, as in Go', async () => {
    const asked: string[] = []
    configureHarness({ memoryFor: (query) => { asked.push(query); return undefined } })
    const { contextualSession } = await import('./context/session')
    const ready = await contextualSession({ ...bare(), prompt: 'the host’s prompt', context: { objective: '', currentRequest: { message: 'the person’s message', input: '' } } })
    ready.context.close()
    expect(asked).toEqual(['the person’s message'])
  })

  it('does not ask for memory where the host said memory is off', async () => {
    const asked: string[] = []
    configureHarness({ memoryFor: (query) => { asked.push(query); return typedMemory('Recalled.') } })
    expect(await readHostParts('q', contextPolicy(undefined, 'scripted-model'), false)).toEqual({})
    expect(asked).toEqual([])
  })

  it('takes each part at its bound and refuses it one byte past, and refuses an answer that is not text', async () => {
    for (const part of HOST_PARTS) {
      const bound = CONTEXT_DEFAULTS[part.bound]
      // The compatibility record contributes 245 bytes beyond unescaped text.
      const overhead = part.name === 'memory' ? 245 : 0
      const answer = (bytes: number) => 'é'.repeat(Math.floor((bytes - overhead) / 2)) + 'x'.repeat((bytes - overhead) % 2)
      configureHarness({ [part.injection]: () => part.name === 'memory' ? typedMemory(answer(bound)) : answer(bound) })
      expect((await readHostParts('q', CONTEXT_DEFAULTS))[part.name]).toBe(part.name==='memory'?frameBlock('memory','kind: fact\nsource: {"proposedBy":"host","run":"host"}\nid: "legacy-memory"\n'+answer(bound),''):answer(bound))
      configureHarness({ [part.injection]: () => part.name === 'memory' ? typedMemory(answer(bound + 1)) : answer(bound + 1) })
      await expect(readHostParts('q', CONTEXT_DEFAULTS)).rejects.toMatchObject({ failure: { code: 'configuration' } })
      configureHarness({ [part.injection]: () => ({ text: 'not text' }) as unknown as string })
      await expect(readHostParts('q', CONTEXT_DEFAULTS)).rejects.toMatchObject({ failure: { code: 'configuration' } })
      configureHarness({ [part.injection]: () => undefined })
    }
  })

  it('fails a run whose host part is over its bound before any request is sent', async () => {
    configureHarness({ projectInstructions: () => 'x'.repeat(CONTEXT_DEFAULTS.projectInstructionsBytes + 1) })
    const turn = await vercelTurn({ answer: () => streamed(sseTurn({ text: 'Done.' })) })
    expect(turn.requests).toHaveLength(0)
    expect(turn.events.find((event) => event.type === 'error')).toMatchObject({ failure: { code: 'configuration' } })
  })

  it('reach every request of a run, in the system text, with memory asked once for the person’s message', async () => {
    const asked: string[] = []
    configureHarness({ brief: () => 'The brief.', projectInstructions: () => 'Be brief.', memoryFor: (query) => { asked.push(query); return typedMemory(`Recalled ${asked.length}.`) } })
    const turn = await vercelTurn({
      answer: (request) => (request.number === 1 ? streamed(sseTurn({ tool: { name: 'get_schema', args: {}, id: 'call_schema' } })) : streamed(sseTurn({ text: 'Done.' })))
    })
    expect(turn.events.filter((event) => event.type === 'error')).toEqual([])
    expect(turn.requests).toHaveLength(2)
    expect(asked).toEqual(['Check the expense pack.'])
    const section = hostSection({ brief: 'The brief.', projectInstructions: 'Be brief.', memory: frameBlock('memory','kind: fact\nsource: {"proposedBy":"host","run":"host"}\nid: "legacy-memory"\n'+'Recalled 1.',/BEGIN RETRIEVED DATA ([0-9a-f]{32}) from memory/.exec(String(turn.requests[0]!.body.messages![0]!.content))![1]!) })
    for (const request of turn.requests) {
      const system = request.body.messages![0]!
      expect(system.role).toBe('system')
      expect(String(system.content)).toContain(section)
      expect(JSON.stringify(request.body.messages!.slice(1))).not.toContain('Recalled')
    }
  })
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
    for (const request of turn.requests) {
      const first = request.body.messages!.find((one) => one.role === 'user')!
      const parts = (first.content as { text: string }[]).map((part) => part.text)
      expect(parts[0]).toBe(message)
      expect(parts[1]).toMatch(/^Earlier context is retained/)
    }
  })

  // agent#14 rewrote this case: the parent is one run, so the child's first request is read as the
  // child sends it, on the same scripted endpoint. ADR-0005: the child is a run, and asks memoryFor
  // once, with its objective.
  it('sends a delegated child’s objective as the first text part of its first message, and recalls memory for it', async () => {
    const { withDelegation } = await import('./delegation')
    const objective = 'Check whether a claim of exactly 1000 USD is escalated.'
    const asked: string[] = []
    configureHarness({ memoryFor: (query) => { asked.push(query); return undefined } })
    const isChild = (request: EndpointRequest) => JSON.stringify(request.body.messages).includes('Complete this read-only task')
    const endpoint = scriptedEndpoint((request) => isChild(request) ? streamed(sseTurn({ text: 'Checked.' }))
      : request.number === 1 ? streamed(sseTurn({ tool: { name: 'delegate_tasks', args: { tasks: [{ title: 'Check', profile: 'review', objective }] }, id: 'call_delegate' } }))
      : streamed(sseTurn({ text: 'Done.' })))
    vi.stubGlobal('fetch', endpoint.fetch)
    const events: AssistantEvent[] = []
    for await (const event of withDelegation(vercel).start({ ...bare(), prompt: 'Review the expense pack.', context: { currentRequest: { message: 'Review the expense pack.', input: '' } }, model: { family: 'openai-compatible', model: 'scripted-model', call: bindModelCall('openai-compatible') }, allowConversation: true, interactive: true, critic: 'off' })) events.push(event)
    expect(events.filter((event) => event.type === 'error')).toEqual([])
    const child = endpoint.requests.filter(isChild)
    expect(child).toHaveLength(1)
    const first = child[0]!.body.messages!.find((one) => one.role === 'user')!
    expect((first.content as { text: string }[])[0]!.text).toBe(objective)
    expect(asked).toEqual(['Review the expense pack.', objective])
  })
})

/** One Anthropic SSE turn: a thinking block (its signature split in two deltas where asked), then a tool call or the text. */
function anthropicTurn(step: { reasoning?: string; splitSignature?: boolean; text?: string; tool?: { name: string; args: unknown } }): string {
  const lines: string[] = []
  const event = (name: string, object: Record<string, unknown>) => lines.push(`event: ${name}\ndata: ${JSON.stringify({ type: name, ...object })}\n\n`)
  event('message_start', { message: { id: 'm', type: 'message', role: 'assistant', model: 'm', content: [], stop_reason: null, usage: { input_tokens: 1, output_tokens: 0 } } })
  let index = 0
  if (step.reasoning !== undefined) {
    const signature = 'c2lnbmF0dXJlLVQx'
    event('content_block_start', { index, content_block: { type: 'thinking', thinking: '' } })
    event('content_block_delta', { index, delta: { type: 'thinking_delta', thinking: step.reasoning } })
    for (const piece of step.splitSignature === true ? [signature.slice(0, 8), signature.slice(8)] : [signature]) event('content_block_delta', { index, delta: { type: 'signature_delta', signature: piece } })
    event('content_block_stop', { index })
    index += 1
  }
  if (step.tool !== undefined) {
    event('content_block_start', { index, content_block: { type: 'tool_use', id: `toolu_${step.tool.name}`, name: step.tool.name, input: {} } })
    event('content_block_delta', { index, delta: { type: 'input_json_delta', partial_json: JSON.stringify(step.tool.args) } })
  } else {
    event('content_block_start', { index, content_block: { type: 'text', text: '' } })
    event('content_block_delta', { index, delta: { type: 'text_delta', text: step.text ?? '' } })
  }
  event('content_block_stop', { index })
  event('message_delta', { delta: { stop_reason: step.tool === undefined ? 'end_turn' : 'tool_use', stop_sequence: null }, usage: { output_tokens: 1 } })
  event('message_stop', {})
  return lines.join('')
}

/**
 * A thinking run on the Anthropic wire whose answer carries a signature split in two (vercel/ai#19663),
 * consumed by a reader that takes a moment over the notice that says the tier is gone. Each request
 * records, as it is received, whether that reader had already finished with the notice.
 */
async function truncatedRun(answers: string[], overrides: Partial<AssistantSession> = {}) {
  const received: { body: Record<string, unknown>; noticed: boolean }[] = []
  let noticed = false
  const call: ModelCall = async (_suffix, request) => {
    received.push({ body: JSON.parse(request.body) as Record<string, unknown>, noticed })
    const answer = answers[Math.min(received.length - 1, answers.length - 1)]!
    return new Response(answer, { status: 200, headers: { 'content-type': 'text/event-stream' } })
  }
  const events: AssistantEvent[] = []
  for await (const event of vercel.start({ ...bare(), prompt: 'Check the expense pack.', tools: [{ name: 'validate',_meta:{operation:'check',effect:'pure',checkKind:'validate'} as const, description: 'check a document', inputSchema: { type: 'object' } }], callTool: async () => ({ content: [{ type: 'text', text: '{"status":"ok"}' }] }), model: { family: 'anthropic', model: 'a-model', call }, thinking: normalize('on', 'anthropic'), critic: 'off', ...overrides })) {
    events.push(event)
    if (event.type === 'thinking_unavailable') {
      await new Promise((resolve) => setTimeout(resolve, 50))
      noticed = true
    }
  }
  return { received, events }
}

/** The text of every message a request carries, in order. */
const messageText = (body: Record<string, unknown>) => JSON.stringify(body.messages)

/** The first text part of a request's first user turn. */
const firstText = (body: Record<string, unknown>) => {
  const first = (body.messages as { role: string; content: unknown }[]).find((message) => message.role === 'user')!
  return typeof first.content === 'string' ? first.content : (first.content as { text?: string }[])[0]!.text
}

// The finishing and the reformat requests (ported from the recall rule's cases, without the rule): the
// request text still opens the first user turn of each. And a thinking answer whose signature came
// back split (vercel/ai#19663) is degraded when the request that would carry it back is composed:
// the notice is on the stream, taken by its reader, before that request leaves, and neither it nor
// the finishing or reformat request after it asks for a tier.
describe('the finishing and the reformat requests after a truncated answer', () => {
  const truncated = anthropicTurn({ reasoning: 'I will check it.', splitSignature: true, tool: { name: 'validate', args: { document: '{}' } } })

  it('the notice is taken before the degraded request leaves, and the finishing request opens with the request text', async () => {
    const { received, events } = await truncatedRun([truncated, anthropicTurn({ text: '' }), anthropicTurn({ text: 'The pack is unchanged.' })], { allowConversation: true })
    expect(events.filter((event) => event.type === 'error')).toEqual([])
    expect(events.filter((event) => event.type === 'thinking_unavailable')).toHaveLength(1)
    expect(received).toHaveLength(3)
    // The second request carries the short signature back: it is the degraded one, and the reader had the notice first.
    expect(received.map((request) => request.noticed)).toEqual([false, true, true])
    expect(received[0]!.body).toHaveProperty('thinking')
    expect(received[1]!.body).not.toHaveProperty('thinking')
    // The third is the finishing request (its instruction in the state, here the system tail), and the request text still leads.
    expect(JSON.stringify(received[2]!.body)).toContain(JSON.stringify(FINISH_RESPONSE_INSTRUCTIONS).slice(1, 60))
    expect(received[2]!.body).not.toHaveProperty('thinking')
    for (const request of received) expect(firstText(request.body)).toBe('Check the expense pack.')
  })

  it('the notice is taken before the degraded request leaves, and the reformat request opens with the request text', async () => {
    const { received, events } = await truncatedRun([
      truncated,
      anthropicTurn({ text: 'Here it is.\n```json\n{"proposal": {\n```' }),
      anthropicTurn({ text: '```json\n' + JSON.stringify({ proposal: { kind: 'create', document: { specVersion: '0.2.0-draft', id: 'expense' }, unknowns: [] } }) + '\n```' })
    ], { allowConversation: false })
    expect(events.filter((event) => event.type === 'error')).toEqual([])
    expect(events.some((event) => event.type === 'proposal')).toBe(true)
    expect(events.filter((event) => event.type === 'thinking_unavailable')).toHaveLength(1)
    expect(received).toHaveLength(3)
    expect(received.map((request) => request.noticed)).toEqual([false, true, true])
    expect(received[1]!.body).not.toHaveProperty('thinking')
    // The third is the reformat request, a user text after the answer it could not read.
    expect(messageText(received[2]!.body)).toContain('could not be read')
    expect(received[2]!.body).not.toHaveProperty('thinking')
    for (const request of received) expect(firstText(request.body)).toBe('Check the expense pack.')
  })
})

/** A model session with nothing in it, and the host's word on memory where given. */
const bare = (memory?: boolean): AssistantSession => ({
  prompt: '', testPrompt: '', tools: [], callTool: async () => ({}), hostTools: [],
  model: { family: 'openai-compatible', model: 'scripted-model', call: async () => new Response() },
  thinking: normalize('off', 'openai-compatible'), signal: new AbortController().signal,
  ...(memory === undefined ? {} : { context: { memory } })
})

describe('the input allowance', () => {
  it('keeps briefBytes and memoryBytes as the parts’ bounds, and the host’s word on memory does not change the policy', () => {
    expect(CONTEXT_DEFAULTS.briefBytes).toBe(8000)
    expect(CONTEXT_DEFAULTS.memoryBytes).toBe(4000)
    expect(contextPolicy(undefined, 'scripted-model')).toEqual(CONTEXT_DEFAULTS)
  })

  it('takes no reserve for the brief or the memory, whatever the host says of memory', () => {
    const instructions = 'instructions', tools: unknown[] = []
    // The input bytes less the instructions, the tools and the 1,536 bytes the budget keeps for the
    // request's own framing: the allowance of a run with no part, and of one with every part (the
    // parts are in the instructions, counted where they sit).
    const full = inputBytes(CONTEXT_DEFAULTS) - utf8Bytes(instructions) - utf8Bytes(JSON.stringify(tools)) - 1536
    const prompt = (bytes: number) => 'p'.repeat(bytes)
    for (const said of [true, undefined, false]) {
      const held = new SessionContext(bare(said))
      expect(() => held.check(instructions, prompt(full), tools)).not.toThrow()
      expect(() => held.check(instructions, prompt(full + 1), tools)).toThrow()
      // What it gains: the allowance used to take the brief's 8,000 bytes and, unless the host said memory is off, the memory's 4,000 out of it.
      const before = inputBytes(CONTEXT_DEFAULTS) - utf8Bytes(instructions) - utf8Bytes(JSON.stringify(tools)) - 8000 - (said === false ? 0 : 4000) - 1536
      expect(full - before).toBe(said === false ? 8000 : 12000)
      expect(() => held.check(instructions, prompt(before + 1), tools)).not.toThrow()
      held.close()
    }
  })
})

describe('the prompt’s digest', () => {
  it('is the SHA-256 of each body the vector holds', async () => {
    expect(digestVector.bodies).toHaveLength(digestVector.items.length)
    for (const [at, body] of digestVector.bodies.entries()) expect({ request: at + 1, sha256: await contextDigest(body) }).toEqual(digestVector.items[at])
  })

  it('is recorded in the checkpoint for every request body a model engine sent, in order, and read back', async () => {
    const sent: string[] = []
    // Bound at the first call, so it captures the scripted endpoint the turn stubs in.
    let bound: ModelCall | undefined
    const call: ModelCall = async (suffix, request) => {
      sent.push(request.body)
      bound ??= bindModelCall('openai-compatible')
      return bound(suffix, request)
    }
    const turn = await vercelTurn({
      answer: (request) => (request.number === 1 ? streamed(sseTurn({ tool: { name: 'get_schema', args: {}, id: 'call_schema' } })) : streamed(sseTurn({ text: 'Done.' }))),
      session: { model: { family: 'openai-compatible', model: 'scripted-model', call } }
    })
    expect(turn.events.filter((event) => event.type === 'error')).toEqual([])
    expect(sent).toHaveLength(2)
    const checkpoint = lastCheckpoint(turn.events)
    expect(checkpoint.prompts).toEqual(await Promise.all(sent.map(async (body, at) => ({ request: at + 1, sha256: await contextDigest(body) }))))
    expect(readExecution(checkpoint)!.prompts).toEqual(checkpoint.prompts)
  })

  it('is recorded for the agent request the codex engine handed the agent capability', async () => {
    const sent: string[] = []
    // Bound at the first run, so it captures the scripted bridge the turn stubs in.
    let run: AgentRun | undefined
    const recorded: AgentRun = async (request, callbacks) => {
      // The body as the transport sends it to the bridge: the coordinator's `delegated` mark is never sent.
      const { delegated: _delegated, ...wire } = request
      sent.push(JSON.stringify(wire))
      run ??= bindAgentRun('scripted-agent')
      return run(request, callbacks)
    }
    const turn = await codexTurn({
      script: async (bridge) => {
        bridge.started()
        bridge.say('The pack validates.')
        bridge.end()
      },
      session: { agent: { model: 'scripted-agent', run: recorded } }
    })
    expect(turn.events.filter((event) => event.type === 'error')).toEqual([])
    expect(sent).toHaveLength(1)
    expect(lastCheckpoint(turn.events).prompts).toEqual([{ request: 1, sha256: await contextDigest(sent[0]!) }])
  })

  it('on the agent wire is of the body the transport sends: a delegated child’s mark is not in it', async () => {
    const { contextualSession } = await import('./context/session')
    const handed: AgentRequest[] = []
    const agentSession: AgentAssistantSession = { prompt: 'Check the pack.', testPrompt: '', tools: [], callTool: async () => ({}), hostTools: [], signal: new AbortController().signal, agent: { model: 'scripted-agent', run: async (request) => { handed.push(request) } } }
    const ready = await contextualSession(agentSession)
    const request: AgentRequest = { prompt: 'Check the pack.', instructions: 'GUIDANCE', tools: [], phase: 'author', model: 'child-model', delegated: true }
    await ready.session.values.agent.run(request, { signal: new AbortController().signal, event: async () => {}, tool: async () => ({ text: '', isError: false }) })
    // The capability is still handed the mark (the transport reads it to admit the child's model); the digest leaves it out.
    expect(handed[0]!.delegated).toBe(true)
    const { delegated: _delegated, ...wire } = request
    expect(ready.context.execution.state.prompts).toEqual([{ request: 1, sha256: await contextDigest(JSON.stringify(wire)) }])
    ready.context.close()
  })

  it('is refused on a checkpoint whose digests are not in order or not digests', () => {
    const turn = { version: 1, id: 'x', requestKey: 'a'.repeat(64), phase: 'work', status: 'running', unit: 'model-requests', used: 2, limit: 20, grants: [], plan: [], explanation: '', operations: [], context: { scope: 's', entries: [] } }
    expect(readExecution({ ...turn, prompts: [{ request: 1, sha256: 'b'.repeat(64) }, { request: 2, sha256: 'c'.repeat(64) }] })!.prompts).toHaveLength(2)
    expect(() => readExecution({ ...turn, prompts: [{ request: 2, sha256: 'b'.repeat(64) }, { request: 2, sha256: 'c'.repeat(64) }] })).toThrow()
    expect(() => readExecution({ ...turn, prompts: [{ request: 1, sha256: 'B'.repeat(64) }] })).toThrow()
    expect(() => readExecution({ ...turn, prompts: [{ request: 1, sha256: 'b'.repeat(64), body: '{}' }] })).toThrow()
  })
})
