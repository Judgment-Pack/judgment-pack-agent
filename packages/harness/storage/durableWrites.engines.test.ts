/**
 * A host write under a run store, through the real engines (#275). A host write here runs what JSON.parse
 * read, as its canonical JSON, so two spellings of one value are one write: what runs is what is keyed.
 * What JSON.parse rounds or cannot hold is refused instead, since the write it would run is not the one
 * the model wrote. The runs drive the `vercel` and `deepagents` engines against a scripted endpoint whose
 * calls carry exactly the argument text given. storage/durableWrites.test.ts holds the writer.
 */
import { describe, expect, it } from 'vitest'
import { MemoryRunStore } from './run'
import { deepagents } from '../engines/deepagents'
import { vercel } from '../engines/vercel'
import { ofType, sseTurn, streamed, vercelTurn, type EndpointRequest } from '../testing/scripted'
import type { HostTool } from '../engine'

/** One streamed turn whose call carries exactly the argument text given. */
const callFrame = (id: string, name: string, text: string) =>
  sseTurn({ tool: { name, args: { placeholder: true }, id } }).replace(JSON.stringify(JSON.stringify({ placeholder: true })), JSON.stringify(text))

async function run(engine: typeof vercel, calls: readonly string[], read = false) {
  const executed: unknown[] = []
  const tool: HostTool = {
    name: 'change_record', ...(read ? { taxonomy: { operation: 'read', effect: 'read' } as const, effect: 'read' as const } : { taxonomy: { operation: 'own-state', effect: 'pure' } as const, effect: 'workflow' as const }),
    description: 'Change one record', inputSchema: { type: 'object' },
    execute: async (args) => { executed.push(args); return { content: [{ type: 'text', text: `ran ${executed.length}` }] } }
  }
  const record = await vercelTurn({
    engine,
    hostTools: [tool],
    answer: (request: EndpointRequest) => streamed(request.number <= calls.length ? callFrame(`call_${request.number}`, tool.name, calls[request.number - 1]!) : sseTurn({ text: 'Done.' })),
    session: { context: { currentRequest: { message: 'Change it.', input: 'Change it.' }, runStore: { store: new MemoryRunStore(), id: 'durable-writes' } } }
  })
  return { executed, answers: ofType(record.events, 'tool_result').map((event) => event.text), errors: ofType(record.events, 'error') }
}

describe.each([['vercel', vercel], ['deepagents', deepagents]] as const)('host writes under a run store, through %s (#275)', (_name, engine) => {
  it('refuses a write JSON.parse has rounded, rather than perform it on another id', async () => {
    const out = await run(engine, ['{"id":9007199254740993}', '{"id":9007199254740992}', '{"id":9007199254740991}'])
    expect(out.executed).toEqual([{ id: 9007199254740991 }])
    expect(out.answers[0]).toContain('DurableRequestUnrecordable')
    expect(out.answers[1]).toContain('DurableRequestUnrecordable')
    expect(out.answers[2]).toBe('ran 1')
    expect(out.errors).toEqual([])
  })

  it('refuses a write whose number JSON.parse read as an infinity, and the run goes on', async () => {
    const out = await run(engine, ['{"q":1e400}', '{"q":null}', '{"a":[1e400]}'])
    expect(out.executed).toEqual([{ q: null }])
    expect(out.answers[0]).toContain('DurableRequestUnrecordable')
    expect(out.answers[1]).toBe('ran 1')
    expect(out.answers[2]).toContain('DurableRequestUnrecordable')
    expect(out.errors).toEqual([])
  })

  // Each pair is one value once JSON.parse has read it, and the tool is run with that value's canonical
  // JSON, so a second call is a true repeat of what ran: the tool could not tell the two apart.
  it.each([
    ['another spelling of 1', '{"q":1.0}', '{"q":1}', { q: 1 }],
    ['negative zero', '{"q":-0}', '{"q":0}', { q: 0 }],
    ['a repeated name', '{"a":{"x":1},"a":{"y":2}}', '{"a":{"y":2}}', { a: { y: 2 } }],
    ['names that fold together, in both orders', '{"ID":1,"id":2}', '{"id":2,"ID":1}', { ID: 1, id: 2 }],
    ['the same bytes', '{"id":7}', '{"id":7}', { id: 7 }]
  ])('answers from the record what ran as one value: %s', async (_label, first, second, ran) => {
    const out = await run(engine, [first, second])
    expect(out.executed).toEqual([ran])
    expect(out.answers).toEqual(['ran 1', 'ran 1'])
    expect(out.errors).toEqual([])
  })

  it('still runs a read: nothing is answered from a record, so nothing is refused for what JSON.parse lost', async () => {
    const out = await run(engine, ['{"id":9007199254740993}', '{"id":9007199254740993}'], true)
    expect(out.executed).toEqual([{ id: 9007199254740992 }, { id: 9007199254740992 }])
    expect(out.errors).toEqual([])
  })

  it('still runs two different writes', async () => {
    const out = await run(engine, ['{"id":7}', '{"id":8}'])
    expect(out.answers).toEqual(['ran 1', 'ran 2'])
  })
})
