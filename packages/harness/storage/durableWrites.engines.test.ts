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
import { toolListDigest } from '../toolSources'
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

/** One run of an act tool whose policy holds the call for the person, and what was asked and run. */
async function heldForApproval(engine: typeof vercel, text: string) {
  const executed: unknown[] = []
  const declared = { name: 'change_record', description: 'Change one record', inputSchema: { type: 'object' }, taxonomy: { operation: 'act', effect: 'act' } as const }
  const tool = {
    ...declared, effectClass: 'act', approve: () => 'ask',
    source: { kind: 'custom', id: 'tracker', trust: 'person', version: '1', toolListDigest: await toolListDigest([declared] as never), identity: { kind: 'connection', label: 'tracker' } },
    execute: async (args: unknown) => { executed.push(args); return { content: [{ type: 'text' as const, text: 'ran' }] } }
  } as unknown as HostTool
  const record = await vercelTurn({
    engine,
    hostTools: [tool],
    answer: (request: EndpointRequest) => streamed(request.number === 1 ? callFrame('call_1', 'tracker__change_record', text) : sseTurn({ text: 'Done.' })),
    session: { context: { currentRequest: { message: 'Change it.', input: 'Change it.' }, runStore: { store: new MemoryRunStore(), id: 'held' } } }
  })
  return { executed, asked: ofType(record.events, 'approval_requested'), answers: ofType(record.events, 'tool_result').map((event) => event.text) }
}

/** One Anthropic or Gemini streamed turn whose call carries exactly the argument text given, or the final answer. */
const familyTurn = (family: 'anthropic' | 'gemini', name: string, text?: string) => {
  if (family === 'gemini') return `data: {"candidates":[{"content":{"role":"model","parts":[${text === undefined ? '{"text":"Done."}' : `{"functionCall":{"name":${JSON.stringify(name)},"args":${text}}}`}]},"index":0,"finishReason":"STOP"}]}\n\n`
  const event = (kind: string, body: object) => `event: ${kind}\ndata: ${JSON.stringify({ type: kind, ...body })}\n\n`
  const block = text === undefined
    ? event('content_block_start', { index: 0, content_block: { type: 'text', text: '' } }) + event('content_block_delta', { index: 0, delta: { type: 'text_delta', text: 'Done.' } })
    : event('content_block_start', { index: 0, content_block: { type: 'tool_use', id: 'call_1', name, input: {} } }) + event('content_block_delta', { index: 0, delta: { type: 'input_json_delta', partial_json: text } })
  return event('message_start', { message: { id: 'm', type: 'message', role: 'assistant', model: 'm', content: [], stop_reason: null, usage: { input_tokens: 1, output_tokens: 0 } } }) + block + event('content_block_stop', { index: 0 }) + event('message_delta', { delta: { stop_reason: text === undefined ? 'end_turn' : 'tool_use' }, usage: { output_tokens: 1 } }) + event('message_stop', {})
}

// JSON.parse serves every wire here, so a host write is held to one rule whichever family wrote it.
describe.each([['vercel', vercel], ['deepagents', deepagents]] as const)('every family, through %s (#275)', (name, engine) => {
  it.each([['anthropic'], ['gemini']] as const)('%s: refuses what JSON.parse rounded or read as an infinity, and runs the rest', async (family) => {
    const calls = ['{"id":9007199254740993}', '{"q":1e400}', '{"id":9007199254740991}'], executed: unknown[] = []
    const tool: HostTool = {
      name: 'change_record', taxonomy: { operation: 'own-state', effect: 'pure' }, description: 'Change one record', inputSchema: { type: 'object' },
      execute: async (args) => { executed.push(args); return { content: [{ type: 'text', text: 'ran' }] } }
    }
    let number = 0
    const record = await vercelTurn({
      engine, hostTools: [tool],
      answer: () => streamed(sseTurn({ text: 'not this endpoint' })),
      session: {
        model: { family, model: 'fixture', call: async () => new Response(familyTurn(family, tool.name, calls[number++]), { status: 200, headers: { 'content-type': 'text/event-stream' } }) } as never,
        context: { currentRequest: { message: 'Change it.', input: 'Change it.' }, runStore: { store: new MemoryRunStore(), id: 'families' } }
      }
    })
    const answers = ofType(record.events, 'tool_result').map((event) => event.text)
    // The AI SDK's Gemini provider writes the call's arguments out itself, so 1e400 is already null when the
    // harness reads it, and nothing here can tell it from a null the model wrote. The guide says so.
    const written = name === 'vercel' && family === 'gemini'
    expect(executed).toEqual([...(written ? [{ q: null }] : []), { id: 9007199254740991 }])
    expect(answers[0]).toContain('DurableRequestUnrecordable')
    if (written) expect(answers[1]).toBe('ran')
    else expect(answers[1]).toContain('DurableRequestUnrecordable')
    expect(answers[2]).toBe('ran')
  })
})

describe.each([['vercel', vercel], ['deepagents', deepagents]] as const)('a call held for approval, through %s (#275)', (_name, engine) => {
  // JSON.parse has read these already, so what the person would approve is not what the model wrote. Nobody is asked.
  it.each([
    ['a number out of range', '{"q":1e400}'],
    ['past 2^53', '{"id":9007199254740993}'],
    ['a lone surrogate', '{"q":"\\ud800"}']
  ])('refuses before anyone is asked: %s', async (_label, text) => {
    const out = await heldForApproval(engine, text)
    expect(out.asked).toEqual([])
    expect(out.executed).toEqual([])
    expect(out.answers[0]).toContain('DurableRequestUnrecordable')
  })

  it('still asks for what it holds exactly', async () => {
    const out = await heldForApproval(engine, '{"id":7}')
    expect(out.asked).toHaveLength(1)
    expect(out.executed).toEqual([])
  })
})

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
