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
 * - **The memory reserve**, as before: `memoryBytes` is left out of a run's input allowance only
 *   while the host has not said memory is off.
 * - **The prompt's digest.** Every request body the adapter sent is digested in the checkpoint's
 *   `prompts`, in order, on a model engine and on the agent wire (`provenance/prompt-digest.json`).
 */
import { afterEach, describe, expect, it, vi } from 'vitest'
import hostPartsVector from '../../contract/vectors/budget/host-parts.json'
import digestVector from '../../contract/vectors/provenance/prompt-digest.json'
import memoryVector from '../../contract/vectors/budget/memory.json'
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
import type { AgentRun } from './agent'
import type { AssistantEvent, AssistantSession, ModelCall } from './engine'

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
    configureHarness({ brief: async () => { briefs++; return 'The brief.' }, projectInstructions: () => null, memoryFor: (query) => { asked.push(query); return '' } })
    expect(await readHostParts('the person’s message', CONTEXT_DEFAULTS)).toEqual({ brief: 'The brief.' })
    expect(briefs).toBe(1)
    expect(asked).toEqual(['the person’s message'])
  })

  it('does not ask for memory where the host said memory is off', async () => {
    const asked: string[] = []
    configureHarness({ memoryFor: (query) => { asked.push(query); return 'Recalled.' } })
    expect(await readHostParts('q', contextPolicy(undefined, 'scripted-model', undefined, false), false)).toEqual({})
    expect(asked).toEqual([])
  })

  it('takes each part at its bound and refuses it one byte past, and refuses an answer that is not text', async () => {
    for (const part of HOST_PARTS) {
      const bound = CONTEXT_DEFAULTS[part.bound]
      const answer = (bytes: number) => 'é'.repeat(Math.floor(bytes / 2)) + 'x'.repeat(bytes % 2)
      configureHarness({ [part.injection]: () => answer(bound) })
      expect((await readHostParts('q', CONTEXT_DEFAULTS))[part.name]).toBe(answer(bound))
      configureHarness({ [part.injection]: () => answer(bound + 1) })
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
    configureHarness({ brief: () => 'The brief.', projectInstructions: () => 'Be brief.', memoryFor: (query) => { asked.push(query); return `Recalled ${asked.length}.` } })
    const turn = await vercelTurn({
      answer: (request) => (request.number === 1 ? streamed(sseTurn({ tool: { name: 'get_schema', args: {}, id: 'call_schema' } })) : streamed(sseTurn({ text: 'Done.' })))
    })
    expect(turn.events.filter((event) => event.type === 'error')).toEqual([])
    expect(turn.requests).toHaveLength(2)
    expect(asked).toEqual(['Check the expense pack.'])
    const section = hostSection({ brief: 'The brief.', projectInstructions: 'Be brief.', memory: 'Recalled 1.' })
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
    for await (const event of withDelegation(vercel).start({ ...bare(), prompt: 'Review the expense pack.', context: { currentRequest: { message: 'Review the expense pack.', input: '' } }, model: { family: 'openai-compatible', model: 'scripted-model', call: bindModelCall('openai-compatible') }, allowConversation: true, interactive: true, adversarialReview: false })) events.push(event)
    expect(events.filter((event) => event.type === 'error')).toEqual([])
    const child = endpoint.requests.filter(isChild)
    expect(child).toHaveLength(1)
    const first = child[0]!.body.messages!.find((one) => one.role === 'user')!
    expect((first.content as { text: string }[])[0]!.text).toBe(objective)
    expect(asked).toEqual(['Review the expense pack.', objective])
  })
})

/** A model session with nothing in it, and the host's word on memory where given. */
const bare = (memory?: boolean): AssistantSession => ({
  prompt: '', testPrompt: '', tools: [], callTool: async () => ({}), hostTools: [],
  model: { family: 'openai-compatible', model: 'scripted-model', call: async () => new Response() },
  thinking: normalize('off', 'openai-compatible'), signal: new AbortController().signal,
  ...(memory === undefined ? {} : { context: { memory } })
})

describe('the memory reserve', () => {
  it('is reserved only when memory may be on: off frees it, on and unsaid keep today’s', () => {
    expect(contextPolicy(undefined, 'scripted-model', undefined, false).memoryBytes).toBe(0)
    expect(contextPolicy(undefined, 'scripted-model', undefined, true).memoryBytes).toBe(CONTEXT_DEFAULTS.memoryBytes)
    expect(contextPolicy(undefined, 'scripted-model').memoryBytes).toBe(CONTEXT_DEFAULTS.memoryBytes)
    expect(CONTEXT_DEFAULTS.memoryBytes).toBe(4000)
  })

  // budget/memory.json: the policy for each word the host gives, memory on, not said, off.
  it('gives the policy the memory vector states for each word the host gives on memory', () => {
    const [on, unsaid, off] = memoryVector.items
    expect(contextPolicy(undefined, 'scripted-model', undefined, true)).toEqual(on!)
    expect(contextPolicy(undefined, 'scripted-model')).toEqual(unsaid!)
    expect(contextPolicy(undefined, 'scripted-model', undefined, false)).toEqual(off!)
    expect([on!.memoryBytes, unsaid!.memoryBytes, off!.memoryBytes]).toEqual([4000, 4000, 0])
  })

  it('leaves a run with memory off the full input allowance, and one with it on the allowance less the reserve', () => {
    const instructions = 'instructions', tools: unknown[] = []
    // The allowance with no memory reserve: the input bytes less the instructions, the tools,
    // the brief and the 1,536 bytes the budget keeps for the request's own framing.
    const full = inputBytes(CONTEXT_DEFAULTS) - utf8Bytes(instructions) - utf8Bytes(JSON.stringify(tools)) - CONTEXT_DEFAULTS.briefBytes - 1536
    const prompt = (bytes: number) => 'p'.repeat(bytes)
    const off = new SessionContext(bare(false))
    expect(() => off.check(instructions, prompt(full), tools)).not.toThrow()
    expect(() => off.check(instructions, prompt(full + 1), tools)).toThrow()
    for (const on of [new SessionContext(bare(true)), new SessionContext(bare())]) {
      expect(() => on.check(instructions, prompt(full - 4000), tools)).not.toThrow()
      expect(() => on.check(instructions, prompt(full - 4000 + 1), tools)).toThrow()
      on.close()
    }
    off.close()
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
      sent.push(JSON.stringify(request))
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

  it('is refused on a checkpoint whose digests are not in order or not digests', () => {
    const turn = { version: 1, id: 'x', requestKey: 'a'.repeat(64), phase: 'work', status: 'running', unit: 'model-requests', used: 2, limit: 20, grants: [], plan: [], explanation: '', operations: [], context: { scope: 's', entries: [] } }
    expect(readExecution({ ...turn, prompts: [{ request: 1, sha256: 'b'.repeat(64) }, { request: 2, sha256: 'c'.repeat(64) }] })!.prompts).toHaveLength(2)
    expect(() => readExecution({ ...turn, prompts: [{ request: 2, sha256: 'b'.repeat(64) }, { request: 2, sha256: 'c'.repeat(64) }] })).toThrow()
    expect(() => readExecution({ ...turn, prompts: [{ request: 1, sha256: 'B'.repeat(64) }] })).toThrow()
    expect(() => readExecution({ ...turn, prompts: [{ request: 1, sha256: 'b'.repeat(64), body: '{}' }] })).toThrow()
  })
})
