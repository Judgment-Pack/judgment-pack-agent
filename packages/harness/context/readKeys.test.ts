/**
 * Which arguments a read may be cached under (#271). The keys are made from the arguments' JSON text,
 * so a value that text does not say exactly (1e400 reads as Infinity and writes as null) gets no key
 * and is kept in neither cache. contract/vectors/chat/read-arguments.json states the cases; Go reads
 * the same ones from the raw bytes (go/context/read_arguments_test.go).
 *
 * The runs drive the `vercel` and `deepagents` engines against a scripted endpoint whose calls carry
 * exactly the argument text given.
 */
import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { ChatContext, canonicalJSON, readCacheKey } from './chat'
import { contextDigest } from './ledger'
import { contextualSession } from './session'
import { deepagents } from '../engines/deepagents'
import { vercel } from '../engines/vercel'
import { normalize } from '../thinking'
import { ofType, sseTurn, streamed, vercelTurn, type EndpointRequest } from '../testing/scripted'
import type { AssistantSession, HostTool } from '../engine'

/** One streamed turn whose call carries exactly the argument text given. */
const callFrame = (id: string, name: string, text: string) =>
  sseTurn({ tool: { name, args: { placeholder: true }, id } }).replace(JSON.stringify(JSON.stringify({ placeholder: true })), JSON.stringify(text))

/** A read declared immutable, and what it was run with. */
function lookup() {
  const executed: unknown[] = []
  const tool: HostTool = {
    name: 'lookup', taxonomy: { operation: 'read', effect: 'read' } as const, effect: 'read', contextPolicy: { immutable: true },
    description: 'Read one record', inputSchema: { type: 'object' },
    execute: async (args) => { executed.push(args); return { content: [{ type: 'text', text: `answer ${executed.length}` }] } }
  }
  return { executed, tool }
}

async function run(engine: typeof vercel, chat: ChatContext | undefined, calls: readonly string[]) {
  const { executed, tool } = lookup()
  const record = await vercelTurn({
    engine,
    hostTools: [tool],
    answer: (request: EndpointRequest) => streamed(request.number <= calls.length ? callFrame(`call_${request.number}`, tool.name, calls[request.number - 1]!) : sseTurn({ text: 'Done.' })),
    session: { context: { currentRequest: { message: 'Read.', input: 'Read.' }, ...(chat ? { chat } : {}) } }
  })
  return { executed, answers: ofType(record.events, 'tool_result').map((event) => event.text) }
}

describe('the arguments a read may be cached under (#271)', () => {
  const vector = JSON.parse(readFileSync('../../contract/vectors/chat/read-arguments.json', 'utf8')) as {
    tool: string
    cases: { label: string; text: string; typescript: { canonical?: string; sha256?: string; key?: 'none' } }[]
  }

  it('keys each vector case as the contract states, and gives no key where its JSON text would not say it exactly', async () => {
    expect(vector.cases.length).toBeGreaterThan(0)
    for (const item of vector.cases) {
      // What an engine hands on for the text: empty is no arguments, and one JSON.parse refuses never arrives.
      let args: unknown
      try { args = item.text === '' ? {} : JSON.parse(item.text) } catch { args = undefined }
      const key = await readCacheKey(vector.tool, args as Record<string, unknown>, false)
      if (item.typescript.key === 'none') {
        expect(key, item.label).toBeUndefined()
      } else {
        expect(canonicalJSON({ tool: vector.tool, arguments: args }), item.label).toBe(item.typescript.canonical)
        expect(key, item.label).toBe(item.typescript.sha256)
        expect(await contextDigest(item.typescript.canonical!), item.label).toBe(item.typescript.sha256)
      }
    }
  })

  // A host can hand these on; a model cannot write them. None has a JSON text that says it.
  const notJSON: [string, unknown][] = [
    ['an infinity', { q: Infinity }],
    ['a negative infinity', { q: -Infinity }],
    ['NaN', { q: Number.NaN }],
    ['negative zero', { q: -0 }],
    ['a date', { q: new Date(0) }],
    ['a map', { q: new Map() }],
    ['undefined in an array', { q: [undefined] }],
    ['a hole in an array', { q: new Array(1) }],
    ['an undefined member', { q: undefined }],
    ['a big integer', { q: 1n }],
    ['a function', { q: () => 1 }],
    ['an infinity nested in an array', { a: [Infinity] }],
    ['an infinity nested in an object', { a: { b: Infinity } }],
    ['arguments that are an array', [1]],
    ['arguments that are null', null],
    ['arguments that are a date', new Date(0)]
  ]

  it.each(notJSON)('gives no key to %s', async (_label, args) => {
    expect(await readCacheKey('lookup', args as Record<string, unknown>, false)).toBeUndefined()
  })

  it('keys an object with no prototype as the same object with one', async () => {
    const bare = Object.assign(Object.create(null) as Record<string, unknown>, { id: 7 })
    expect(await readCacheKey('lookup', bare, false)).toBe(await readCacheKey('lookup', { id: 7 }, false))
    expect(await readCacheKey('lookup', { id: 7 }, false)).toBe(vector.cases[0]!.typescript.sha256)
  })

  describe.each([['vercel', vercel], ['deepagents', deepagents]] as const)('through %s', (_name, engine) => {
    describe.each([['the run\'s cache', false], ['the chat\'s cache', true]] as const)('in %s', (_where, withChat) => {
      const chat = () => withChat ? new ChatContext() : undefined

      it.each([
        ['out of range, one way and the other', '{"q":1e400}', '{"q":-1e400}'],
        ['out of range and null', '{"q":1e400}', '{"q":null}'],
        ['negative zero and zero', '{"q":-0}', '{"q":0}']
      ])('runs each of two calls whose JSON text is one: %s', async (_label, first, second) => {
        const out = await run(engine, chat(), [first, second])
        expect(out.answers).toEqual(['answer 1', 'answer 2'])
        expect(out.executed).toHaveLength(2)
      })

      it('still serves ordinary arguments, and what JSON.parse has read alike, without a call: what runs is what it read', async () => {
        const out = await run(engine, chat(), ['{"id":7,"ratio":1.5}', '{ "id" : 7, "ratio" : 1.50 }'])
        expect(out.answers).toEqual(['answer 1', 'answer 1'])
        expect(out.executed).toEqual([{ id: 7, ratio: 1.5 }])
      })
    })
  })

  it('caches no call whose arguments are not an object, which vercel hands on and deepagents refuses', async () => {
    const twice = ['[1]', '[1]'] as const
    for (const chat of [undefined, new ChatContext()]) {
      const out = await run(vercel, chat, twice)
      expect(out.answers).toEqual(['answer 1', 'answer 2'])
      expect(out.executed).toEqual([[1], [1]])
    }
    const refused = await run(deepagents as typeof vercel, undefined, twice)
    expect(refused.executed).toEqual([])
    expect(refused.answers[1]).toContain('not a JSON object')
  })

  // Below the engines: the wrapper hands its own execute the value itself, where the value is what differs.
  describe('the wrapper', () => {
    const open = (chat?: ChatContext) => contextualSession({
      prompt: 'Read.', testPrompt: '', tools: [], hostTools: [], callTool: async () => { throw Error('unused') },
      model: { family: 'openai-compatible', model: 'fixture', call: async () => { throw Error('unused') } },
      thinking: normalize('off', 'openai-compatible'), signal: new AbortController().signal, allowConversation: true, interactive: true, critic: 'off',
      context: { currentRequest: { message: 'Read.', input: 'Read.' }, ...(chat ? { chat } : {}) }
    } as AssistantSession)
    const scopes = [['the run\'s cache', undefined, undefined], ['the chat\'s cache', () => new ChatContext(), { runtime: false }], ['the run\'s cache on a chat', () => new ChatContext(), { runtime: true }]] as const

    it.each(scopes)('answers each call for its own value in %s', async (_where, chat, kept) => {
      const ready = await open(chat?.())
      const ran: unknown[] = []
      const read = ready.context.execution.wrapRead('lookup', async (args) => { ran.push(args.q); return { content: [{ type: 'text', text: String(args.q) }] } }, { immutable: true }, true, kept)
      for (const value of [Infinity, -Infinity, null, -0, 0]) await read({ q: value })
      expect(ran).toEqual([Infinity, -Infinity, null, -0, 0])
      ready.context.close()
    })

    it.each(scopes)('still serves ordinary arguments in %s', async (_where, chat, kept) => {
      const ready = await open(chat?.())
      let ran = 0
      const read = ready.context.execution.wrapRead('lookup', async () => { ran++; return { content: [{ type: 'text', text: 'answer' }] } }, { immutable: true }, true, kept)
      await read({ id: 7 })
      await read({ id: 7 })
      expect(ran).toBe(1)
      ready.context.close()
    })
  })
})
