/**
 * The chat-scoped context (agent#8): the ledger and the read cache kept across the runs of one
 * chat, the index of earlier results a run's first message carries, the byte bound held across the
 * runs by eviction, and disposal.
 *
 * The runs drive the `vercel` engine (and once the `codex` engine) against a scripted endpoint and
 * a runtime behind the real tool gate. The scripted model reads what the index does not list, so a
 * run whose first message lists a result does not ask for it again.
 */
import { afterEach, describe, expect, it, vi } from 'vitest'
import indexVector from '../../../contract/vectors/chat/turn-index.json'
import keyVector from '../../../contract/vectors/chat/cache-key.json'
import harnessDefaults from '../../../contract/vectors/budget/harness-defaults.json'
import { ChatContext, EVICTED, INDEX_NOTICE, INDEX_SOURCE, canonicalJSON, readCacheKey, type RuntimeIdentity } from './chat'
import { ContextLedger, contextDigest } from './ledger'
import { CONTEXT_DEFAULTS, ContextBudgetError, utf8Bytes } from './policy'
import { READ_TOOL } from './reader'
import { SessionContext } from './session'
import { OPENING, unframeData } from '../framing'
import { codexTurn, ofType, runtimeAnswer, sseTurn, streamed, vercelTurn, type EndpointRequest } from '../testing/scripted'
import { normalize } from '../thinking'
import type { AssistantSession, HostTool, McpToolResult } from '../engine'

afterEach(() => vi.unstubAllGlobals())

const RUNTIME: RuntimeIdentity = { identity: 'jpack', version: '0.19.0' }
const SCHEMA = { $id: 'https://example.com/schemas/pack.json', type: 'object', description: 'The pack schema. '.repeat(300) }
const INSTRUCTIONS = 'Read the pack schema before you write a pack, and keep every amount a decimal string.\n' + 'Authoring guidance. '.repeat(200)
const CASES = { cases: [{ id: 'small-claim', facts: { claim: { amount: '40' } }, expected: 'approve' }] }

/** The three reads a turn of authoring makes: the instructions, the schema and the pack's tests. */
const READS = [
  { name: 'get_authoring_instructions', args: {} },
  { name: 'get_schema', args: { spec_version: '0.2.0-draft' } },
  { name: 'read_pack_tests', args: { owner: 'packs/expense.pack.json' } }
] as const

/** How often each source answered: the Runtime's get_schema, and the two host tools that read. */
function sources() {
  const counts = { instructions: 0, tests: 0 }
  const hostTools: HostTool[] = [
    {
      name: 'get_authoring_instructions',
      effect: 'read',
      // The Runtime's authoring prompt: immutable for one Runtime, so keyed by it.
      contextPolicy: { immutable: true, reference: true, runtime: true },
      description: 'Read the runtime contract before creating or changing a pack.',
      inputSchema: { type: 'object', properties: {}, additionalProperties: false },
      execute: async () => {
        counts.instructions++
        return { content: [{ type: 'text', text: INSTRUCTIONS }] }
      }
    },
    {
      name: 'read_pack_tests',
      effect: 'read',
      // Saved cases change between turns: a mutable read.
      description: 'Read a pack and its saved cases.',
      inputSchema: { type: 'object', properties: { owner: { type: 'string' } }, required: ['owner'], additionalProperties: false },
      execute: async () => {
        counts.tests++
        return runtimeAnswer(CASES)
      }
    }
  ]
  return { counts, hostTools }
}

interface IndexData {
  steps: { tool: string; read: { id: string }; arguments: { sha256: string }; bytes: number; firstLine: string }[]
  omitted: number
  evicted: { tool: string; arguments: { sha256: string }; bytes: number }[]
  evictedOmitted: number
  olderResults: string
}

/** The JSON inside an index's data block. */
function indexData(text: string): IndexData {
  expect(text.startsWith(INDEX_NOTICE)).toBe(true)
  const marker = text.match(new RegExp(`^${OPENING} ([0-9a-f]{32}) from ${INDEX_SOURCE};`, 'm'))?.[1]
  expect(marker).toBeDefined()
  return JSON.parse(unframeData(text.slice(INDEX_NOTICE.length), marker!)!) as IndexData
}

type Message = { role: string; content?: unknown; tool_calls?: { function: { name: string; arguments: string } }[] }
/** The first user message's text parts, as the request carried them. */
function firstUserTexts(request: EndpointRequest): string[] {
  const first = (request.body.messages as Message[]).find((message) => message.role === 'user')!
  return typeof first.content === 'string' ? [first.content] : (first.content as { type: string; text?: string }[]).filter((part) => part.type === 'text').map((part) => part.text!)
}
/** The index a request's first message carries, where it carries one. */
function indexIn(request: EndpointRequest): IndexData | undefined {
  const text = firstUserTexts(request).find((part) => part.startsWith(INDEX_NOTICE))
  return text === undefined ? undefined : indexData(text)
}
const calledIn = (request: EndpointRequest) => (request.body.messages as Message[]).flatMap((message) => message.tool_calls ?? []).map((call) => call.function.name)

/**
 * The scripted model: it reads each of READS the index does not list (all of them where
 * `ignoreIndex`), one call per request, then, where `readBack`, reads the schema back once with
 * read_window from the id the index gives, then answers.
 */
function reader(options: { ignoreIndex?: boolean; readBack?: boolean; schemaVersion?: string } = {}) {
  return (request: EndpointRequest) => {
    const index = indexIn(request)
    const listed = new Set(options.ignoreIndex ? [] : (index?.steps ?? []).map((step) => step.tool))
    const called = calledIn(request)
    const next = READS.find((read) => !listed.has(read.name) && !called.includes(read.name))
    if (next !== undefined) {
      const args = next.name === 'get_schema' && options.schemaVersion !== undefined ? { spec_version: options.schemaVersion } : next.args
      return streamed(sseTurn({ tool: { name: next.name, args, id: `call_${next.name}` } }))
    }
    const schema = index?.steps.find((step) => step.tool === 'get_schema')
    if (options.readBack && schema !== undefined && !called.includes(READ_TOOL)) {
      return streamed(sseTurn({ tool: { name: READ_TOOL, args: { id: schema.read.id }, id: 'call_read_window' } }))
    }
    return streamed(sseTurn({ text: 'The pack does what the person asked.' }))
  }
}

const runtimeAnswers = (call: { name: string }) => (call.name === 'get_schema' ? runtimeAnswer(SCHEMA) : runtimeAnswer({ status: 'ok' }))

/** One turn of the chat: the person's message, the chat context (or none), and what the source reads were. */
async function turn(options: {
  chat?: ChatContext
  message: string
  hostTools: HostTool[]
  model?: ReturnType<typeof reader>
  runtime?: RuntimeIdentity | null
}) {
  const record = await vercelTurn({
    answer: options.model ?? reader(),
    handle: runtimeAnswers,
    hostTools: options.hostTools,
    session: {
      prompt: `TRANSCRIPT SO FAR\n\nTHE PERSON'S MESSAGE\n${options.message}`,
      context: {
        currentRequest: { message: options.message, input: options.message },
        ...(options.chat === undefined ? {} : { chat: options.chat }),
        ...(options.runtime === null ? {} : { runtime: options.runtime ?? RUNTIME })
      }
    }
  })
  return { ...record, runtimeReads: record.seen.filter((call) => call.name === 'get_schema').length }
}

const bodyBytes = (request: EndpointRequest) => utf8Bytes(JSON.stringify(request.body))

describe('a two-turn chat', () => {
  it('reads each instruction, schema and test set once: the second turn reads none of them again', async () => {
    const chat = new ChatContext()
    const { counts, hostTools } = sources()
    const first = await turn({ chat, message: 'Write the expense pack.', hostTools, model: reader({ readBack: true }) })
    expect([first.runtimeReads, counts.instructions, counts.tests]).toEqual([1, 1, 1])
    expect(indexIn(first.requests[0]!)).toBeUndefined()

    const second = await turn({ chat, message: 'Add a rule for claims above 1000 USD.', hostTools, model: reader({ readBack: true }) })
    // Turn two: no read reaches the Runtime or a host source.
    expect([second.runtimeReads, counts.instructions, counts.tests]).toEqual([0, 1, 1])
    // Its first message carries the request text, then the index of what turn one kept.
    const texts = firstUserTexts(second.requests[0]!)
    expect(texts[0]).toContain('Add a rule for claims above 1000 USD.')
    expect(indexIn(second.requests[0]!)!.steps.map((step) => step.tool)).toEqual(READS.map((read) => read.name))
    // The one call it makes is read_window, answered from the chat's ledger.
    expect(ofType(second.events, 'tool_call').map((event) => event.name)).toEqual([READ_TOOL])
    const read = ofType(second.events, 'tool_result')[0]!
    expect(read.isError).toBe(false)
    expect(read.text).toContain('The pack schema.')
  })

  it('sends fewer bytes in its second turn than a run with no chat context, which reads all three again', async () => {
    const today = sources()
    await turn({ message: 'Write the expense pack.', hostTools: today.hostTools })
    const todaySecond = await turn({ message: 'Add a rule for claims above 1000 USD.', hostTools: today.hostTools })
    expect([todaySecond.runtimeReads, today.counts.instructions, today.counts.tests]).toEqual([1, 2, 2])

    const chat = new ChatContext()
    const kept = sources()
    await turn({ chat, message: 'Write the expense pack.', hostTools: kept.hostTools })
    const keptSecond = await turn({ chat, message: 'Add a rule for claims above 1000 USD.', hostTools: kept.hostTools })
    expect([keptSecond.runtimeReads, kept.counts.instructions, kept.counts.tests]).toEqual([0, 1, 1])

    // The turn without a chat context makes three requests to read again before it answers; the
    // turn with one answers on its first. The index is what its first request adds.
    expect(todaySecond.requests).toHaveLength(4)
    expect(keptSecond.requests).toHaveLength(1)
    const index = firstUserTexts(keptSecond.requests[0]!)[1]!
    expect(bodyBytes(keptSecond.requests[0]!)).toBeLessThan(bodyBytes(todaySecond.requests[0]!) + utf8Bytes(JSON.stringify(index)) + 64)
    const total = (requests: EndpointRequest[]) => requests.reduce((sum, request) => sum + bodyBytes(request), 0)
    expect(total(keptSecond.requests)).toBeLessThan(total(todaySecond.requests) / 3)
    // The request that answers carries no result read again.
    expect(bodyBytes(keptSecond.requests.at(-1)!)).toBeLessThan(bodyBytes(todaySecond.requests.at(-1)!))
  })

  it('keeps the ledger after a run ends, and clears it only when the host disposes of the chat', async () => {
    const chat = new ChatContext()
    const { hostTools } = sources()
    await turn({ chat, message: 'Write the expense pack.', hostTools })
    const results = [...chat.entries.values()].filter((entry) => entry.kind === 'tool').map((entry) => entry.label)
    expect(results).toEqual(expect.arrayContaining(READS.map((read) => read.name)))
    expect(chat.turnCount).toBe(1)

    chat.dispose()
    expect(chat.entries.size).toBe(0)
    expect(chat.isDisposed).toBe(true)
    const refused = await turn({ chat, message: 'Add a rule.', hostTools })
    expect(refused.requests).toHaveLength(0)
    expect(ofType(refused.events, 'error')[0]!.message).toMatch(/disposed|could not complete/)
    expect(refused.events.at(-1)).toEqual({ type: 'end' })
  })

  it('carries the chat index on the agent wire too, and serves the schema from the cache there', async () => {
    const chat = new ChatContext()
    const prompts: string[] = []
    const script = async (bridge: Parameters<Parameters<typeof codexTurn>[0]['script']>[0]) => {
      prompts.push(bridge.request.prompt)
      bridge.started()
      await bridge.call('get_schema', { spec_version: '0.2.0-draft' })
      bridge.say('The schema is read.')
      bridge.end()
    }
    const first = await codexTurn({ script, handle: runtimeAnswers, session: { context: { currentRequest: { message: 'Write it.', input: 'Write it.' }, chat, runtime: RUNTIME } } })
    const second = await codexTurn({ script, handle: runtimeAnswers, session: { context: { currentRequest: { message: 'Change it.', input: 'Change it.' }, chat, runtime: RUNTIME } } })
    expect(first.seen.map((call) => call.name)).toEqual(['get_schema'])
    expect(second.seen).toEqual([])
    expect(prompts[0]).not.toContain(INDEX_NOTICE)
    // After the request text: the line, then the block to its closing line.
    const prompt = prompts[1]!
    const at = prompt.indexOf(INDEX_NOTICE)
    expect(at).toBeGreaterThan(prompt.indexOf('Check the expense pack.'))
    const closing = prompt.indexOf('\nEND RETRIEVED DATA ', at)
    const end = prompt.indexOf('\n', closing + 1)
    expect(indexData(prompt.slice(at, end < 0 ? undefined : end)).steps.map((step) => step.tool)).toEqual(['get_schema'])
  })
})

describe('the read cache', () => {
  it('never serves a mutable read: the tests are read on every turn that asks, the schema once', async () => {
    const chat = new ChatContext()
    const { counts, hostTools } = sources()
    const first = await turn({ chat, message: 'Write the pack.', hostTools, model: reader({ ignoreIndex: true }) })
    const second = await turn({ chat, message: 'Change the pack.', hostTools, model: reader({ ignoreIndex: true }) })
    expect(counts.tests).toBe(2)
    expect(counts.instructions).toBe(1)
    expect([first.runtimeReads, second.runtimeReads]).toEqual([1, 0])
    // The cached answer is the answer: what the model read on turn two is what turn one read.
    const schemaText = (record: typeof first) => ofType(record.events, 'tool_result').find((event) => event.name === 'get_schema')!.text
    expect(schemaText(second)).toBe(schemaText(first))
  })

  it('misses on a changed argument, and asks the Runtime', async () => {
    const chat = new ChatContext()
    const { hostTools } = sources()
    await turn({ chat, message: 'Write the pack.', hostTools, model: reader({ ignoreIndex: true }) })
    const changed = await turn({ chat, message: 'Use the older draft.', hostTools, model: reader({ ignoreIndex: true, schemaVersion: '0.1.0-draft' }) })
    expect(changed.seen.filter((call) => call.name === 'get_schema').map((call) => call.args)).toEqual([{ spec_version: '0.1.0-draft' }])
  })

  it('keys a Runtime-served read by the Runtime: another version misses, and without one nothing is kept across runs', async () => {
    const chat = new ChatContext()
    const { counts, hostTools } = sources()
    await turn({ chat, message: 'Write the pack.', hostTools, model: reader({ ignoreIndex: true }) })
    const upgraded = await turn({ chat, message: 'Again.', hostTools, model: reader({ ignoreIndex: true }), runtime: { identity: 'jpack', version: '0.20.0' } })
    expect(upgraded.runtimeReads).toBe(1)
    expect(counts.instructions).toBe(2)
    const unnamed = new ChatContext()
    const before = counts.instructions
    const one = await turn({ chat: unnamed, message: 'Write the pack.', hostTools, model: reader({ ignoreIndex: true }), runtime: null })
    const two = await turn({ chat: unnamed, message: 'Again.', hostTools, model: reader({ ignoreIndex: true }), runtime: null })
    expect([one.runtimeReads, two.runtimeReads]).toEqual([1, 1])
    expect(counts.instructions - before).toBe(2)
    // Kept all the same: the index lists them.
    expect(indexIn(two.requests[0]!)!.steps.map((step) => step.tool)).toEqual(READS.map((read) => read.name))
  })

  it('reaches nothing another chat holds', async () => {
    const one = new ChatContext()
    const other = new ChatContext()
    const { hostTools } = sources()
    await turn({ chat: one, message: 'Write the pack.', hostTools })
    const schemaId = [...one.entries.values()].find((entry) => entry.label === 'get_schema')!.id
    const elsewhere = await turn({
      chat: other,
      message: 'Write the pack.',
      hostTools,
      model: (request) =>
        calledIn(request).includes(READ_TOOL)
          ? streamed(sseTurn({ text: 'Nothing to read.' }))
          : streamed(sseTurn({ tool: { name: READ_TOOL, args: { id: schemaId }, id: 'call_read_window' } }))
    })
    expect(indexIn(elsewhere.requests[0]!)).toBeUndefined()
    const read = ofType(elsewhere.events, 'tool_result')[0]!
    expect(read.isError).toBe(true)
    expect(read.text).toContain('unavailable')
    expect(other.entries.has(schemaId)).toBe(false)
  })
})

describe('the cache key, as the contract states it', () => {
  it('is the SHA-256 of the canonical JSON of each vector item', async () => {
    for (const [at, item] of keyVector.items.entries()) {
      expect(canonicalJSON(item)).toBe(keyVector.keys[at]!.canonical)
      const runtime = (item as { runtime?: RuntimeIdentity }).runtime
      expect(await readCacheKey(item.tool, item.arguments, runtime !== undefined, runtime)).toBe(keyVector.keys[at]!.sha256)
    }
  })

  it('carries the arguments: two calls that differ only in them have two keys', async () => {
    const one = await readCacheKey('get_schema', { spec_version: '0.2.0-draft' }, true, RUNTIME)
    const two = await readCacheKey('get_schema', { spec_version: '0.1.0-draft' }, true, RUNTIME)
    expect(one).not.toBe(two)
    expect(await readCacheKey('get_schema', {}, true, undefined)).toBeUndefined()
  })
})

/** A ledger over one run's turn of the fixture's chat. */
function fixtureRun(chat: ChatContext, retainedBytes = CONTEXT_DEFAULTS.retainedBytes) {
  const store = chat.openTurn()
  return { store, ledger: new ContextLedger({ ...CONTEXT_DEFAULTS, retainedBytes }, store) }
}
type FixtureResult = (typeof indexVector.fixture.results)[number]
const answerOf = (result: FixtureResult): McpToolResult => runtimeAnswer(result.value)
async function keep(run: ReturnType<typeof fixtureRun>, result: FixtureResult) {
  await run.store.keep(run.ledger, result.tool, result.arguments as Record<string, unknown>, answerOf(result))
}
const MARKER = 'a'.repeat(32)

describe('the index of earlier results', () => {
  it('is what the hand-written vector states, for the next run', async () => {
    const chat = new ChatContext({ scope: indexVector.fixture.scope })
    const first = fixtureRun(chat)
    for (const result of indexVector.fixture.results.slice(0, 3)) await keep(first, result)
    // A run sees nothing of its own turn in the index.
    expect(first.store.index(CONTEXT_DEFAULTS, MARKER)).toBe('')
    first.ledger.release()
    const second = fixtureRun(chat)
    expect(indexData(second.store.index(CONTEXT_DEFAULTS, MARKER))).toEqual(indexVector.items[0])
  })

  it('evicts the oldest entries no open run references, and says so in the next index', async () => {
    const chat = new ChatContext({ scope: indexVector.fixture.scope })
    const bound = indexVector.fixture.retainedBytes
    const first = fixtureRun(chat, bound + 10_000)
    for (const result of indexVector.fixture.results.slice(0, 3)) await keep(first, result)
    first.ledger.release()
    const [instructions, schema, tests] = [...chat.entries.keys()]
    const second = fixtureRun(chat, bound)
    second.store.index(CONTEXT_DEFAULTS, MARKER)
    await keep(second, indexVector.fixture.results[3]!)
    // The two oldest went, in the order they were retained; the third was enough room.
    expect([...chat.entries.keys()]).toEqual([tests, expect.stringContaining(':result-get_example-')])
    expect(chat.size).toBeLessThanOrEqual(bound)
    // A read of an evicted entry says why it is gone.
    expect(() => second.ledger.read({ id: schema })).toThrow(EVICTED)
    expect(() => second.ledger.readUnits({ id: instructions })).toThrow(EVICTED)
    second.ledger.release()
    const third = fixtureRun(chat, bound)
    expect(indexData(third.store.index(CONTEXT_DEFAULTS, MARKER))).toEqual(indexVector.items[1])
    // Each eviction is listed once.
    third.ledger.release()
    expect(indexData(fixtureRun(chat, bound).store.index(CONTEXT_DEFAULTS, MARKER)).evicted).toEqual([])
  })

  it('evicts by age: an entry a later run read outlives one it did not, and an open run’s entries are kept', async () => {
    const chat = new ChatContext()
    const document = (id: string) => ({ id, kind: 'tool' as const, label: id, value: 'x'.repeat(200) })
    // Each entry is 202 bytes: three fit under 700, four do not.
    const first = fixtureRun(chat, 700)
    const a = await first.ledger.retain(document('a'))
    const b = await first.ledger.retain(document('b'))
    first.ledger.release()
    const second = fixtureRun(chat, 700)
    // Read in turn two: a is now younger than b.
    second.ledger.read({ id: a.id })
    const c = await second.ledger.retain(document('c'))
    const d = await second.ledger.retain(document('d'))
    expect([...chat.entries.keys()]).toEqual([a.id, c.id, d.id])
    expect(chat.entries.has(b.id)).toBe(false)
    // Everything left is the open run's: nothing more can be evicted, so the bound refuses.
    await expect(second.ledger.retain(document('e'))).rejects.toBeInstanceOf(ContextBudgetError)
    second.ledger.release()
    const third = fixtureRun(chat, 700)
    const e = await third.ledger.retain(document('e'))
    // Of a, c and d (all turn two's), the first retained goes first.
    expect([...chat.entries.keys()]).toEqual([c.id, d.id, e.id])
  })

  it('is bounded by the policy: the newest rows, the rest counted', async () => {
    const chat = new ChatContext()
    const first = fixtureRun(chat)
    for (let at = 0; at < 30; at++) await first.store.keep(first.ledger, 'get_example', { name: `example-${at}` }, runtimeAnswer({ example: at }))
    first.ledger.release()
    const second = fixtureRun(chat)
    const listed = indexData(second.store.index({ turnIndexEntries: 20, turnIndexBytes: 1_000_000 }, MARKER))
    expect(listed.steps).toHaveLength(20)
    expect(listed.omitted).toBe(10)
    expect(listed.steps[0]!.firstLine).toBe('{"example":10}')
    const none = indexData(second.store.index({ turnIndexEntries: 0, turnIndexBytes: 1_000_000 }, MARKER))
    expect([none.steps.length, none.omitted]).toEqual([0, 30])
    second.ledger.release()
    const text = fixtureRun(chat).store.index({ turnIndexEntries: 20, turnIndexBytes: 3000 }, MARKER)
    expect(utf8Bytes(text)).toBeLessThanOrEqual(3000)
    const bounded = indexData(text)
    expect(bounded.steps.length).toBeLessThan(20)
    expect(bounded.steps.length + bounded.omitted).toBe(30)
    expect(bounded.steps.at(-1)!.firstLine).toBe('{"example":29}')
  })

  it('is counted in the request budget: the prompt’s allowance is less by its bytes', async () => {
    const session = { prompt: '', testPrompt: '', tools: [], callTool: async () => ({}), hostTools: [], model: { family: 'openai-compatible', model: 'scripted-model', call: async () => new Response() }, thinking: normalize('off', 'openai-compatible'), signal: new AbortController().signal } as AssistantSession
    const context = new SessionContext(session)
    let fits = 'p'
    // The longest prompt that is sent whole with nothing beside it.
    for (let step = 1 << 17; step > 0; step >>= 1) if ((await context.prompt(fits + 'p'.repeat(step), 'instructions', [])) === fits + 'p'.repeat(step)) fits += 'p'.repeat(step)
    expect(await context.prompt(fits, 'instructions', [])).toBe(fits)
    expect(await context.prompt(fits, 'instructions', [], undefined, 'an index')).not.toBe(fits)
    context.close()
  })
})

describe('a run’s checkpoint and the chat', () => {
  it('carries the entries the run retained or read, not the chat’s whole ledger, and restores into the same chat', async () => {
    const chat = new ChatContext({ scope: indexVector.fixture.scope })
    const first = fixtureRun(chat)
    for (const result of indexVector.fixture.results.slice(0, 3)) await keep(first, result)
    first.ledger.release()
    const [instructions, schema] = [...chat.entries.keys()]
    const second = fixtureRun(chat)
    second.ledger.read({ id: schema })
    const saved = second.ledger.snapshot()
    expect(saved.scope).toBe(chat.scope)
    expect(saved.entries.map((entry) => entry.id)).toEqual([schema])
    second.ledger.release()
    // Resumed in the same chat after the entry went: it is retained again; nothing else changes.
    chat.entries.delete(schema!)
    const resumed = fixtureRun(chat)
    await resumed.ledger.restore(saved)
    expect(chat.entries.has(schema!)).toBe(true)
    expect(chat.entries.has(instructions!)).toBe(true)
    resumed.ledger.release()
    // Resumed under another chat context: a ledger of the run's own, the other chat untouched.
    const other = new ChatContext()
    const elsewhere = fixtureRun(other)
    await elsewhere.ledger.restore(saved)
    expect(elsewhere.ledger.chat).toBeUndefined()
    expect(elsewhere.ledger.read({ id: schema })).toMatchObject({ contextId: schema })
    expect(other.entries.size).toBe(0)
  })

  it('opens a turn per run and closes it when the run ends', async () => {
    const chat = new ChatContext()
    const { hostTools } = sources()
    await turn({ chat, message: 'Write the pack.', hostTools })
    await turn({ chat, message: 'Again.', hostTools })
    expect(chat.turnCount).toBe(2)
    // With both runs ended, no turn holds anything: a bound of three bytes evicts every entry to
    // make room for one of three, rather than refusing it.
    const run = fixtureRun(chat, 3)
    await run.ledger.retain({ id: 'new', kind: 'tool', label: 'new', value: 'n' })
    expect([...chat.entries.values()].map((entry) => entry.label)).toEqual(['new'])
  })
})

describe('the harness defaults', () => {
  it('are the contract vector, the index bounds among them', () => {
    expect({ ...CONTEXT_DEFAULTS }).toEqual(harnessDefaults.items[0])
  })

  it('compute a digest the same way the ledger does', async () => {
    expect(await contextDigest('')).toBe('e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855')
  })
})
