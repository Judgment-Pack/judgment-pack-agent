import { LANGUAGES } from '../../testing/host'
/**
 * The `vercel` engine's own promises, each measured where it is kept.
 *
 * The whole scenario — eight steps, both wire formats, both answer shapes, the
 * real runtime's tool definitions and its recorded answers, every network
 * global sealed — is `assistant/conformance/`, and it is what certifies this
 * adapter. What is here is the handful of properties that suite would only ever
 * exercise incidentally, and the ones that are about **this SDK** rather than
 * about the contract: the address discipline on the `fetch` the providers are
 * given, the credential that never leaves it, the two layers that put
 * `rehearsal: true` on an evaluate, the unhandled-rejection guard the ADR names,
 * and `end` exactly once on the paths the scenario never takes.
 */
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it, vi } from 'vitest'
import { ASSISTANT_ENGINES, ASSISTANT_TOOLS } from '../../host'
import { CERTIFICATION_IS_TOTAL, CERTIFIED_ENGINES, loadEngine } from '../index'
import { ChannelHasOneConsumer, eventChannel } from './channel'
import { vercel } from './index'
import { GOOGLE_OPTIONS, REHEARSAL_HOOK, claimPromises, runVercelLoop, sdkThinking } from './loop'
import { managedContext } from '../../context/managed'
import {
  ADDRESS_REFUSED,
  PLACEHOLDER_ORIGIN,
  placeholderBase,
  reframe,
  relayFetch,
  signatureLedger,
  signatureOf,
  signsReasoning,
  suffixOf,
  withoutTruncatedThinking
} from './relay'
import { normalize, wireFor } from '../../thinking'
import { REFUTATION_MARKER } from '../../refutation'
import { PROPOSAL_RECEIVED, PROPOSE_PACK_SCHEMA, PROPOSE_PACK_TOOL } from '../contract'
import toolOffer from '../../../../contract/schemas/tool-offer.json'
import type { streamText } from 'ai'
import type { AssistantEvent, AssistantSession, McpTool, ModelCall, McpToolResult } from '../../engine'
import type { ExecutionCheckpoint } from '../../execution/state'

const TOOLS: McpTool[] = [
  { name: 'validate', description: 'check a document', inputSchema: { type: 'object' } },
  {
    name: 'experimental_evaluate',
    description: 'rehearse an evaluation',
    inputSchema: { type: 'object' }
  }
]

const PROPOSAL_TEXT =
  'Here it is.\n\n```json\n' +
  JSON.stringify({ proposal: { kind: 'create', document: { id: 'p' }, unknowns: ['who signs'] } }) +
  '\n```\n'

/** One assistant turn, as the OpenAI-compatible wire streams it. */
function turn(step: {
  text?: string
  tool?: { name: string; args: unknown }
  /** What an endpoint that reasons on its own default behaviour sends. */
  reasoning?: string[]
}): string {
  const frame = (choices: unknown[]) =>
    `data: ${JSON.stringify({ id: 'c', object: 'chat.completion.chunk', model: 'm', choices })}\n\n`
  const lines = [frame([{ index: 0, delta: { role: 'assistant', content: null }, finish_reason: null }])]
  for (const piece of step.reasoning ?? []) {
    lines.push(frame([{ index: 0, delta: { reasoning_content: piece }, finish_reason: null }]))
  }
  // A turn may answer **and** call a tool, which is what an endpoint does when
  // it says what it is about to do — and it is the shape the "a tool-only turn
  // neither counts nor resets" rule needs to be measured against.
  if (step.tool && step.text !== undefined) {
    lines.push(frame([{ index: 0, delta: { content: step.text }, finish_reason: null }]))
  }
  if (step.tool) {
    lines.push(
      frame([
        {
          index: 0,
          delta: {
            tool_calls: [
              {
                index: 0,
                id: `call_${step.tool.name}`,
                type: 'function',
                function: { name: step.tool.name, arguments: JSON.stringify(step.tool.args) }
              }
            ]
          },
          finish_reason: null
        }
      ]),
      frame([{ index: 0, delta: {}, finish_reason: 'tool_calls' }])
    )
  } else {
    lines.push(
      frame([{ index: 0, delta: { content: step.text ?? '' }, finish_reason: null }]),
      frame([{ index: 0, delta: {}, finish_reason: 'stop' }])
    )
  }
  lines.push('data: [DONE]\n\n')
  return lines.join('')
}

/** One Anthropic SSE turn: a thinking block, then a tool call or the text. */
function anthropicTurn(step: {
  reasoning?: string
  signature?: string
  splitSignature?: boolean
  text?: string
  tool?: { name: string; args: unknown }
}): string {
  const lines: string[] = []
  const event = (name: string, object: Record<string, unknown>) =>
    lines.push(`event: ${name}\ndata: ${JSON.stringify({ type: name, ...object })}\n\n`)
  event('message_start', {
    message: { id: 'm', type: 'message', role: 'assistant', model: 'm', content: [], stop_reason: null, usage: { input_tokens: 1, output_tokens: 0 } }
  })
  let index = 0
  if (step.reasoning !== undefined) {
    event('content_block_start', { index, content_block: { type: 'thinking', thinking: '' } })
    event('content_block_delta', { index, delta: { type: 'thinking_delta', thinking: step.reasoning } })
    const signature = step.signature ?? 'c2ln'
    if (step.splitSignature === true) {
      const half = Math.floor(signature.length / 2)
      event('content_block_delta', { index, delta: { type: 'signature_delta', signature: signature.slice(0, half) } })
      event('content_block_delta', { index, delta: { type: 'signature_delta', signature: signature.slice(half) } })
    } else {
      event('content_block_delta', { index, delta: { type: 'signature_delta', signature } })
    }
    event('content_block_stop', { index })
    index += 1
  }
  if (step.tool !== undefined && step.text) {
    event('content_block_start', { index, content_block: { type: 'text', text: '' } })
    event('content_block_delta', { index, delta: { type: 'text_delta', text: step.text } })
    event('content_block_stop', { index })
    index += 1
  }
  if (step.tool !== undefined) {
    event('content_block_start', {
      index,
      content_block: { type: 'tool_use', id: `toolu_${step.tool.name}`, name: step.tool.name, input: {} }
    })
    event('content_block_delta', {
      index,
      delta: { type: 'input_json_delta', partial_json: JSON.stringify(step.tool.args) }
    })
  } else {
    event('content_block_start', { index, content_block: { type: 'text', text: '' } })
    event('content_block_delta', { index, delta: { type: 'text_delta', text: step.text ?? '' } })
  }
  event('content_block_stop', { index })
  event('message_delta', {
    delta: { stop_reason: step.tool === undefined ? 'end_turn' : 'tool_use', stop_sequence: null },
    usage: { output_tokens: 1 }
  })
  event('message_stop', {})
  return lines.join('')
}

/** One Gemini SSE turn: a thought part with its signature, then the answer. */
function geminiTurn(step: {
  reasoning?: string
  signature?: string
  text?: string
  tool?: { name: string; args: unknown }
}): string {
  const lines: string[] = []
  const frame = (parts: unknown[], final = false) =>
    lines.push(
      `data: ${JSON.stringify({ candidates: [{ content: { role: 'model', parts }, index: 0, ...(final ? {finishReason:'STOP'} : {}) }] })}\n\n`
    )
  if (step.reasoning !== undefined) {
    frame([
      { text: step.reasoning, thought: true, thoughtSignature: step.signature ?? 'c2ln' }
    ])
  }
  if (step.tool && step.text) frame([{text:step.text}])
  frame([
    step.tool !== undefined
      ? { functionCall: { name: step.tool.name, args: step.tool.args } }
      : { text: step.text ?? '' }
  ], true)
  return lines.join('')
}

interface Recorded {
  suffix: string
  headerNames: string[]
  body: Record<string, unknown>
}

/** A model capability that answers turn by turn and remembers what it was sent. */
function scriptedCall(turns: string[]): { call: ModelCall; seen: Recorded[] } {
  const seen: Recorded[] = []
  const call: ModelCall = async (suffix, request) => {
    const body = JSON.parse(request.body) as Record<string, unknown>
    seen.push({
      suffix,
      headerNames: Object.keys(request.headers ?? {}).map((name) => name.toLowerCase()),
      body
    })
    const answer = turns[Math.min(seen.length - 1, turns.length - 1)]!
    return new Response(answer, { status: 200, headers: { 'content-type': 'text/event-stream' } })
  }
  return { call, seen }
}

function session(
  call: ModelCall,
  overrides: Partial<AssistantSession> = {},
  callTool?: AssistantSession['callTool']
): AssistantSession {
  return {
    prompt: 'the runtime’s prompt',
    testPrompt: 'the runtime’s test_pack guidance',
    tools: TOOLS,
    callTool:
      callTool ??
      (async (): Promise<McpToolResult> => ({ content: [{ type: 'text', text: '{"status":"ok"}' }] })),
    hostTools: [],
    model: { family: 'openai-compatible', model: 'a-model', call },
    thinking: normalize('off', 'openai-compatible'),
    signal: new AbortController().signal,
    ...overrides
  }
}

async function drain(events: AsyncIterable<AssistantEvent>): Promise<AssistantEvent[]> {
  const seen: AssistantEvent[] = []
  for await (const event of events) seen.push(event)
  return seen
}

/** A promise, or a marker where it did not settle inside the bound. */
async function within(ms: number, work: Promise<unknown>): Promise<string> {
  return Promise.race([
    work.then(() => 'settled'),
    new Promise<string>((resolve) => setTimeout(() => resolve('STILL WAITING'), ms))
  ])
}

describe('the ordered channel, when the consumer stops listening', () => {
  it('settles the delivery it was yielding when the drain is returned', async () => {
    // The defect: `drain` takes an entry off the queue **before** yielding it,
    // so a consumer that stops at exactly that event leaves a delivery nothing
    // is holding — not the queue, and not the loop that never resumes. The
    // producer waited on it for ever.
    const channel = eventChannel()
    const first = channel.push({ type: 'end' })
    const second = channel.push({ type: 'end' })
    const drain = channel.drain()
    expect((await drain.next()).value).toEqual({ type: 'end' })
    await drain.return(undefined)
    expect(await within(500, first), 'the event that was in flight').toBe('settled')
    expect(await within(500, second), 'the events still queued behind it').toBe('settled')
  })

  it('refuses a second consumer by name, and takes nothing from the first', async () => {
    // One channel, one consumer. The in-flight slot is a single slot because
    // there is a single reader: two drains taking concurrently would overwrite
    // each other's, and the overwritten entry would be in neither the queue nor
    // the slot — a delivery nothing could ever settle.
    const channel = eventChannel()
    const first = channel.push({ type: 'end' })
    const second = channel.push({ type: 'end' })
    const one = channel.drain()
    const two = channel.drain()
    expect((await one.next()).value).toEqual({ type: 'end' })
    const refused = await two.next().then(
      () => undefined,
      (cause: unknown) => cause as Error
    )
    expect(refused?.name).toBe('ChannelHasOneConsumer')
    expect(refused).toBeInstanceOf(ChannelHasOneConsumer)
    expect(refused?.message).toContain('already has a consumer')
    // And the refusal left the first reader and both deliveries untouched.
    await one.return(undefined)
    expect(await within(500, first), 'the event the first reader held').toBe('settled')
    expect(await within(500, second), 'the event still queued behind it').toBe('settled')
  })

  it('settles a delivery abandoned before the drain ever ran', async () => {
    const channel = eventChannel()
    const waiting = channel.push({ type: 'end' })
    channel.abandon()
    expect(await within(500, waiting)).toBe('settled')
  })
})

describe('a session that was already over before the run began', () => {
  it.each(['off', 'ultra'] as const)('emits nothing and asks nothing, at tier %s', async (tier) => {
    // An aborted signal is a run that is over. It used to abort the SDK's
    // controller and nothing else, so the engine still said
    // `thinking_unavailable`, still built a provider, and still delivered
    // `end` — an engine reporting on a session that never happened.
    const controller = new AbortController()
    controller.abort()
    let requests = 0
    let tools = 0
    const call: ModelCall = async () => {
      requests += 1
      return new Response(turn({ text: PROPOSAL_TEXT }), {
        status: 200,
        headers: { 'content-type': 'text/event-stream' }
      })
    }
    const events = await drain(
      vercel.start(
        session(call, { signal: controller.signal, thinking: normalize(tier, 'openai-compatible') }, async () => {
          tools += 1
          return { content: [] }
        })
      )
    )
    expect(events).toEqual([])
    expect(requests, 'a request was made for a run that was already over').toBe(0)
    expect(tools, 'the runtime was asked by a run that was already over').toBe(0)
  })
})

describe('when the thing being awaited wins its race with the abort', () => {
  it('delivers nothing after the run closed, and no error and no end', async () => {
    // `withAbort` settles with the **value** when the work wins by a
    // microtask; the abort then runs while the loop is still holding it. What
    // stops the loop delivering is not the abort — it is that the run is
    // already marked closed and the delivery path reads that.
    const controller = new AbortController()
    const { call } = scriptedCall([
      turn({ tool: { name: 'validate', args: { document: {} } } }),
      turn({ text: PROPOSAL_TEXT })
    ])
    const events: AssistantEvent[] = []
    const iterator = vercel.start(
      session(call, { signal: controller.signal }, async () => {
        // The answer arrives, and the session is aborted in the same turn: the
        // value wins, and the abort lands a microtask later.
        const answer: McpToolResult = { content: [{ type: 'text', text: '{"status":"ok"}' }] }
        controller.abort()
        return answer
      })
    )[Symbol.asyncIterator]()
    const first = await iterator.next()
    expect(first.value).toMatchObject({ type: 'tool_call' })
    for (;;) {
      const step = await iterator.next()
      if (step.done === true) break
      events.push(step.value)
    }
    expect(events, 'an event was delivered after the run closed').toEqual([])
  })

  it('delivers no error where the awaited thing failed first', async () => {
    // The other half: the underlying promise **rejected** just before the
    // abort, so the loop's catch sees the original failure rather than a
    // cancellation — and would have reported an `error` and an `end` for a run
    // that was already over.
    const controller = new AbortController()
    const { call } = scriptedCall([
      turn({ tool: { name: 'validate', args: { document: {} } } }),
      turn({ text: PROPOSAL_TEXT })
    ])
    const events: AssistantEvent[] = []
    const iterator = vercel.start(
      session(call, { signal: controller.signal }, async () => {
        controller.abort()
        throw new Error('the socket went away')
      })
    )[Symbol.asyncIterator]()
    expect((await iterator.next()).value).toMatchObject({ type: 'tool_call' })
    for (;;) {
      const step = await iterator.next()
      if (step.done === true) break
      events.push(step.value)
    }
    expect(events).toEqual([])
  })
})

describe('the registry', () => {
  it('cannot make an engine loadable without certifying it', () => {
    // The loader table and the certified list used to be independent: adding a
    // third engine and its loader while forgetting the list compiled, let a
    // `desk.json` select it, and left `describe.each` never certifying it.
    expect(CERTIFICATION_IS_TOTAL).toBe(true)
    const total: typeof CERTIFICATION_IS_TOTAL = true
    expect(total).toBe(true)
    // @ts-expect-error — the assertion is a type equality, not a boolean: it
    // stops compiling the day the two sets differ, which is the whole guard.
    const wrong: typeof CERTIFICATION_IS_TOTAL = false
    expect(wrong).toBe(false)
    // And at runtime: every id the registry can load is one the suite runs.
    for (const id of ASSISTANT_ENGINES) {
      expect(CERTIFIED_ENGINES, `${id} is loadable but not certified`).toContain(id)
    }
    expect([...CERTIFIED_ENGINES].sort()).toEqual([...ASSISTANT_ENGINES].sort())
  })

  it('loads this adapter for the id a desk.json names, and not another', async () => {
    // An entry pointing at a second engine's module would pass every
    // conformance leg twice over and certify nothing — which is exactly what
    // the registry's old fallback used to do on purpose.
    const engine = await loadEngine('vercel')
    expect(engine.id).toBe('vercel')
    expect(engine).toBe(vercel)
  })

  // The two below stood in the withdrawn engine's own suite and are properties
  // of the **registry**, not of that engine: they came here with it rather than
  // going out with it.
  it('refuses an id no table registers, by name', async () => {
    // The loader takes its table as a parameter so the conformance session can
    // put its certification fixtures down the path a certified engine travels.
    // That parameter is also the way an id with no chunk can reach it, so the
    // refusal is the loader's own and says which id.
    await expect(loadEngine('not-an-engine')).rejects.toThrow(
      'no engine chunk is registered for not-an-engine'
    )
    await expect(loadEngine('vercel', {})).rejects.toThrow(
      'no engine chunk is registered for vercel'
    )
  })

  it('loads from the table it is given, which is how a fixture is certified', async () => {
    const stub = { id: 'vercel' as const, start: () => [] as never }
    await expect(loadEngine('anything', { anything: async () => stub })).resolves.toBe(stub)
  })
})

describe('the address the SDK composes, and what this desk will send', () => {
  it('reduces the SDK’s absolute URL to the suffix the desk’s capability names', () => {
    expect(
      suffixOf(
        `${PLACEHOLDER_ORIGIN}/chat/completions`,
        placeholderBase('openai-compatible'),
        'openai-compatible'
      )
    ).toBe('chat/completions')
    expect(suffixOf(`${PLACEHOLDER_ORIGIN}/v1/messages`, placeholderBase('anthropic'), 'anthropic'))
      .toBe('v1/messages')
    // The Gemini wire puts the model and the method in the address, and asks
    // for its stream with the one query pair the relay admits.
    expect(
      suffixOf(
        `${PLACEHOLDER_ORIGIN}/v1beta/models/m:streamGenerateContent?alt=sse`,
        placeholderBase('gemini'),
        'gemini'
      )
    ).toBe('v1beta/models/m:streamGenerateContent?alt=sse')
    expect(
      suffixOf(
        `${PLACEHOLDER_ORIGIN}/v1beta/models/m:generateContent`,
        placeholderBase('gemini'),
        'gemini'
      )
    ).toBe('v1beta/models/m:generateContent')
  })

  it('refuses another origin, a query, a fragment and an empty path', () => {
    const base = placeholderBase('openai-compatible')
    for (const url of [
      'https://api.openai.com/v1/chat/completions',
      'http://127.0.0.1:8791/api/assistant/relay/v1/chat/completions',
      `${PLACEHOLDER_ORIGIN}/chat/completions?api-version=2024-02-01`,
      `${PLACEHOLDER_ORIGIN}/chat/completions#x`,
      `${PLACEHOLDER_ORIGIN}/`,
      'not a url'
    ]) {
      expect(suffixOf(url, base, 'openai-compatible'), url).toBeUndefined()
    }
    // And the Anthropic base is a real second check: a path under the same
    // origin that is not under the family's base does not travel either.
    expect(
      suffixOf(`${PLACEHOLDER_ORIGIN}/chat/completions`, placeholderBase('anthropic'), 'anthropic')
    ).toBeUndefined()
    // The one admitted pair is admitted on one family, byte for byte: every
    // other spelling of it, and the pair on another family, is refused here as
    // well as at the desk's own capability and at the chassis.
    const gemini = placeholderBase('gemini')
    expect(
      suffixOf(`${PLACEHOLDER_ORIGIN}/v1beta/models/m:generateContent?alt=sse`, gemini, 'anthropic')
    ).toBeUndefined()
    for (const query of ['?alt=json', '?alt=sse&alt=sse', '?alt=sse&x=1', '?ALT=sse', '?key=k']) {
      expect(
        suffixOf(`${PLACEHOLDER_ORIGIN}/v1beta/models/m:generateContent${query}`, gemini, 'gemini'),
        query
      ).toBeUndefined()
    }
  })

  it('sends nothing at all where the address is not the relay’s', async () => {
    let called = 0
    const call: ModelCall = async () => {
      called += 1
      return new Response('{}')
    }
    const fetch = relayFetch({ family: 'openai-compatible', call, signal: new AbortController().signal })
    await expect(fetch('https://api.openai.com/v1/chat/completions', { body: '{}' })).rejects.toThrow(
      ADDRESS_REFUSED
    )
    expect(called).toBe(0)
  })

  it('strips every header outside the protocol’s own, the placeholder key included', async () => {
    const seen: string[][] = []
    const call: ModelCall = async (_suffix, request) => {
      seen.push(Object.keys(request.headers ?? {}).map((name) => name.toLowerCase()))
      return new Response('{}', { headers: { 'content-type': 'application/json' } })
    }
    const fetch = relayFetch({ family: 'anthropic', call, signal: new AbortController().signal })
    await fetch(`${PLACEHOLDER_ORIGIN}/v1/messages`, {
      body: '{}',
      headers: {
        'content-type': 'application/json',
        'anthropic-version': '2023-06-01',
        'x-api-key': 'placeholder-the-desk-relay-injects-the-key',
        authorization: 'Bearer sk-nope',
        'user-agent': 'ai-sdk/anthropic'
      }
    })
    expect(seen).toEqual([['content-type', 'anthropic-version']])
  })

  it('drops the Google provider’s own key header, and needs no new allow-list entry', async () => {
    // `createGoogleGenerativeAI` sets `x-goog-api-key` from the placeholder and
    // a `user-agent` suffix of its own; neither is on the protocol allow-list,
    // so neither travels. **No `x-goog-api-client`** is set by this provider at
    // the pinned version — if a future one adds a header the wire needs, it goes
    // on this list *and* on the chassis', with the reason, and this row is where
    // that shows up.
    const seen: string[][] = []
    const call: ModelCall = async (_suffix, request) => {
      seen.push(Object.keys(request.headers ?? {}).map((name) => name.toLowerCase()))
      return new Response('{}', { headers: { 'content-type': 'application/json' } })
    }
    const fetch = relayFetch({ family: 'gemini', call, signal: new AbortController().signal })
    await fetch(`${PLACEHOLDER_ORIGIN}/v1beta/models/m:generateContent`, {
      body: '{}',
      headers: {
        'content-type': 'application/json',
        'x-goog-api-key': 'placeholder-the-desk-relay-injects-the-key',
        'x-goog-api-client': 'ai-sdk/google/4.0.64',
        'user-agent': 'ai-sdk/google'
      }
    })
    expect(seen).toEqual([['content-type']])
  })
})

describe('an endpoint that ignored `stream`', () => {
  // The SDK reads the answer it *asked for*: a JSON object fed to its
  // event-source parser yields no events at all. The desk requires the opposite,
  // so a whole answer is presented in the framing the same protocol defines.
  it('is presented to the SDK as the chunks the OpenAI-compatible wire defines', () => {
    const framed = reframe('openai-compatible', {
      id: 'c',
      choices: [
        {
          index: 0,
          message: {
            role: 'assistant',
            content: null,
            tool_calls: [{ id: 'call_1', type: 'function', function: { name: 'validate', arguments: '{}' } }]
          },
          finish_reason: 'tool_calls'
        }
      ]
    })
    const events = framed
      .split('\n\n')
      .filter((line) => line.startsWith('data: ') && !line.includes('[DONE]'))
      .map((line) => JSON.parse(line.slice(6)) as { choices: { delta?: Record<string, unknown> }[] })
    expect(framed).toContain('data: [DONE]')
    // The one member a chunk carries that a whole message does not.
    expect((events[0]!.choices[0]!.delta!.tool_calls as { index: number }[])[0]!.index).toBe(0)
  })

  it('is presented to the SDK as the one event the Gemini wire defines', () => {
    // The smallest of the three re-framings, and the only one that rewrites
    // nothing: this wire's stream is a sequence of whole answers, so an answer
    // with nothing to split across chunks is already one event. There is no
    // terminal sentinel to write — the stream ends when the body does.
    const whole = {
      candidates: [{ content: { role: 'model', parts: [{ text: 'hello' }] }, finishReason: 'STOP' }]
    }
    const framed = reframe('gemini', whole)
    expect(framed).toBe(`data: ${JSON.stringify(whole)}\n\n`)
    // Nothing of the answer is lost or renamed on the way through.
    expect(JSON.parse(framed.slice('data: '.length))).toEqual(whole)
  })

  it('is presented to the SDK as the events the Anthropic wire defines', () => {
    const framed = reframe('anthropic', {
      id: 'm',
      role: 'assistant',
      content: [{ type: 'text', text: 'hello' }],
      stop_reason: 'end_turn',
      usage: { output_tokens: 3 }
    })
    const names = [...framed.matchAll(/^event: (.+)$/gm)].map((match) => match[1])
    expect(names).toEqual([
      'message_start',
      'content_block_start',
      'content_block_delta',
      'content_block_stop',
      'message_delta',
      'message_stop'
    ])
    // `usage` is on `message_delta` because the protocol puts it there — and
    // because the SDK's own schema refuses the event without it.
    expect(framed).toContain('"usage":{"output_tokens":3}')
  })
})

describe('rehearsal, on two layers', () => {
  it('is a compile error the day the SDK renames the hook', () => {
    // The hook is `experimental_`, and `streamText`'s options carry a rest
    // parameter, so a misspelling is accepted in silence and the rewrite is
    // simply never applied. `REHEARSAL_HOOK` is written as a key of the SDK's
    // own options type; this is the same claim, asserted where a reader is.
    expect(REHEARSAL_HOOK).toBe('experimental_refineToolInput')
    const named: keyof Parameters<typeof streamText>[0] = REHEARSAL_HOOK
    expect(named).toBe('experimental_refineToolInput')
    // @ts-expect-error — a misspelling is not a key of the SDK's options, which
    // is the whole of the guard: delete the `satisfies` in `loop.ts` and an
    // upstream rename produces no error at all.
    const renamed: keyof Parameters<typeof streamText>[0] = 'experimental_refineToolInputRENAMED'
    expect(renamed).toBeTruthy()
  })

  it('hands the desk’s gate the call the model made, and the SDK the rewritten one', async () => {
    // Two layers, and they do different jobs. The hook rewrites what the SDK
    // carries — which is what the model is shown on its next turn — and the
    // ToolGate rewrites what leaves the page. `callTool` must receive the call
    // **as the model made it**, or the gate has nothing to report and the
    // guardrail line in the tab disappears.
    const asked: Record<string, unknown>[] = []
    const { call, seen } = scriptedCall([
      turn({ tool: { name: 'experimental_evaluate', args: { pack: {}, facts: {} } } }),
      turn({ text: PROPOSAL_TEXT })
    ])
    await drain(
      vercel.start(
        session(call, {}, async (name, args) => {
          asked.push({ name, ...args })
          return { content: [{ type: 'text', text: '{"status":"ok"}' }] }
        })
      )
    )
    // What the gate was handed: no rehearsal member, exactly as the model wrote
    // it. The gate is what puts one there, and what says so.
    expect(asked).toEqual([{ name: 'experimental_evaluate', pack: {}, facts: {} }])
    expect(Object.hasOwn(asked[0]!, 'rehearsal')).toBe(false)
    // And what the SDK carried into the next request: the rewritten input.
    const second = seen[1]!.body.messages as { role: string; tool_calls?: unknown[] }[]
    const echoed = second.find((message) => Array.isArray(message.tool_calls))!
    const carried = (echoed.tool_calls as { function: { arguments: string } }[])[0]!
    expect(JSON.parse(carried.function.arguments).rehearsal).toBe(true)
  })
})

describe('the tools this engine offers, and the schemas it writes', () => {
  it('offers exactly what the session handed it, with the runtime’s own schema', async () => {
    // K2, measured on the wire: the served schema arrives as served, member for
    // member, rather than re-typed through a schema library into this adapter's
    // reading of it.
    const served = {
      type: 'object',
      properties: { pack: { type: 'object' }, facts: { type: 'object' } },
      required: ['pack'],
      additionalProperties: false
    }
    const { call, seen } = scriptedCall([turn({ text: PROPOSAL_TEXT })])
    await drain(
      vercel.start(
        session(call, { tools: [{ name: 'validate', description: 'd', inputSchema: served }] })
      )
    )
    const tools = seen[0]!.body.tools as { function: { name: string; parameters: unknown } }[]
    expect(tools.map((tool) => tool.function.name)).toEqual(['list_context', 'read_context', 'read_window', 'search', 'validate'])
    expect(tools.find(t=>t.function.name==='validate')!.function.parameters).toEqual(served)
  })

  it('refuses a tool the runtime served without a schema, rather than inventing one', async () => {
    // The desk never writes a contract the runtime does not enforce. A
    // permissive `{"type":"object"}` written here would tell the model that
    // anything is acceptable for a tool whose real contract this desk does not
    // know — so the session ends instead, naming the tool.
    const { call, seen } = scriptedCall([turn({ text: PROPOSAL_TEXT })])
    const events = await drain(
      vercel.start(session(call, { tools: [{ name: 'a_new_tool', description: 'd' }] }))
    )
    expect(events.map((event) => event.type)).toEqual(['error', 'end'])
    expect((events[0] as { message: string }).message).toContain('a_new_tool')
    expect((events[0] as { message: string }).message).toContain('without an input schema')
    // And nothing was asked of the model at all: the refusal is before the loop.
    expect(seen).toHaveLength(0)
  })

  it('names one tool in its whole source: the one the ADR names', () => {
    // The model is shown the contract the runtime enforces or it is shown
    // nothing, so the adapter has nothing to say about any particular tool. The
    // one exception is the rehearsal tool, and it is there because the SDK's
    // refinement hook is keyed by tool name.
    const source = readFileSync(join(import.meta.dirname, 'loop.ts'), 'utf8')
    for (const tool of ASSISTANT_TOOLS) {
      if (tool === 'experimental_evaluate') continue
      expect(source, `the adapter names the tool ${tool}`).not.toContain(`'${tool}'`)
    }
  })
})

describe('exactly one end, on every path', () => {
  it('delivers the terminal event through the channel, in order, once', async () => {
    // Not from a `finally`: `end` is pushed and delivered like every other
    // event, so a direct consumer — `runAssistantSession`, and anything else
    // driving the contract — actually receives it.
    const { call } = scriptedCall([
      turn({ tool: { name: 'validate', args: { document: {} } } }),
      turn({ text: PROPOSAL_TEXT })
    ])
    const events = await drain(vercel.start(session(call)))
    expect(events.map((event) => event.type)).toEqual([
      'tool_call',
      'tool_result',
      'message', // "Here it is." — the model's prose beside the fence
      'proposal',
      'end'
    ])
    expect(events.filter((event) => event.type === 'end')).toHaveLength(1)
  })

  it('reports a refusal once and ends', async () => {
    const call: ModelCall = async () =>
      new Response(JSON.stringify({ error: 'no key stored', code: 'assistant-no-key' }), {
        status: 409,
        headers: { 'content-type': 'application/json' }
      })
    const events = await drain(vercel.start(session(call)))
    const errors = events.filter((event) => event.type === 'error')
    expect(errors).toHaveLength(1)
    expect(errors[0]).toMatchObject({failure:{code:'authentication',status:409}})
    // No address in it, not even the placeholder the SDK's own error carries.
    expect((errors[0] as { message: string }).message).not.toContain('relay.invalid')
    expect(events.filter((event) => event.type === 'end')).toHaveLength(1)
    expect(events[events.length - 1]!.type).toBe('end')
  })

  it('makes one request per turn: the SDK’s own retries are off', async () => {
    // The SDK retries twice by default, with a backoff, and its retryable set
    // includes 409 — the status the desk's own relay answers with when no key
    // is stored. Three requests and six seconds for a refusal a person has to
    // go and fix.
    let requests = 0
    const call: ModelCall = async () => {
      requests += 1
      return new Response('{"error":"no key stored"}', {
        status: 409,
        headers: { 'content-type': 'application/json' }
      })
    }
    await drain(vercel.start(session(call)))
    expect(requests).toBe(1)
  })

  it('says nothing at all, and ends, when the session is aborted', async () => {
    // The desk's own capability rejects an aborted call with a fresh
    // `AbortError` carrying a fixed sentence and no address; this stands in for
    // it, because a stub that ignored the signal would be testing a capability
    // the page does not have.
    const controller = new AbortController()
    const call: ModelCall = (_suffix, request) => {
      controller.abort()
      const stopped = () => new DOMException('the model request was aborted', 'AbortError')
      if (request.signal?.aborted === true) return Promise.reject(stopped())
      return new Promise<Response>((_resolve, reject) => {
        request.signal?.addEventListener('abort', () => reject(stopped()))
      })
    }
    const events = await drain(vercel.start(session(call, { signal: controller.signal })))
    // Nothing at all: a cancelled run is a session somebody ended rather than
    // a run that finished, and the terminal event belongs to the one that
    // finished. The stream ends; the run hook writes the page's own `end`.
    expect(events).toEqual([])
  })

  it('ends once where the final message carries no fenced block, keeping what it said', async () => {
    // One reformat request (the recovery), then the answer is delivered as it
    // was written, then the failure: nothing the model produced is lost.
    const { call, seen } = scriptedCall([turn({ text: 'I could not write one.' })])
    const events = await drain(vercel.start(session(call)))
    expect(events.map((event) => event.type)).toEqual(['response_recovery', 'message', 'error', 'end'])
    expect(events[1]).toEqual({ type: 'message', text: 'I could not write one.' })
    expect((events[2] as { message: string }).message).toContain('exactly one fenced JSON block')
    expect(seen).toHaveLength(2)
  })
})

describe('a consumer that stops in the middle of a run', () => {
  it('closes the generator on the first return, and produces nothing after it', async () => {
    // Yielding the terminal `end` from a `finally` made the first `return()`
    // resolve `{ value: end, done: false }` with the generator still suspended,
    // and `for await`'s own closing discards that value — so a direct consumer
    // was never handed the terminal event at all, and the page only worked
    // because the run hook writes one of its own. A consumer that asks to stop
    // is owed no terminal event; what it is owed is a closed iterator.
    const { call } = scriptedCall([
      turn({ tool: { name: 'validate', args: { document: {} } } }),
      turn({ text: PROPOSAL_TEXT })
    ])
    const iterator = vercel.start(session(call))[Symbol.asyncIterator]()
    expect((await iterator.next()).value).toMatchObject({ type: 'tool_call' })
    expect(await iterator.return!(undefined)).toEqual({ value: undefined, done: true })
    expect(await iterator.next()).toEqual({ value: undefined, done: true })
  })

  it('closes on a return before the first next, having run nothing', async () => {
    const { call, seen } = scriptedCall([turn({ text: PROPOSAL_TEXT })])
    const iterator = vercel.start(session(call))[Symbol.asyncIterator]()
    expect(await within(2000, iterator.return!(undefined) as Promise<unknown>)).toBe('settled')
    expect(await iterator.next()).toEqual({ value: undefined, done: true })
    expect(seen, 'the model was never asked anything').toHaveLength(0)
  })

  it('stops a run whose model request only ends when it is aborted', async () => {
    // The sequence the outer shape exists for. An async generator serves
    // `next()` and `return()` from one queue, so the `return()` carrying the
    // abort was queued behind the very `next()` the abort would have released,
    // and both hung for ever. Measured on this engine before the iterator.
    let sawAbort = false
    let arrived = () => {}
    const entered = new Promise<void>((resolve) => {
      arrived = resolve
    })
    const call: ModelCall = (_suffix, request) =>
      new Promise<Response>((_resolve, reject) => {
        arrived()
        const stopped = () => {
          sawAbort = true
          reject(new DOMException('the model request was aborted', 'AbortError'))
        }
        if (request.signal?.aborted === true) stopped()
        else request.signal?.addEventListener('abort', stopped)
      })

    const settledInOrder: string[] = []
    const iterator = vercel.start(session(call))[Symbol.asyncIterator]()
    const pending = iterator.next().then((step) => {
      settledInOrder.push('next')
      return step
    })
    await entered
    const returned = iterator.return!(undefined).then((step) => {
      settledInOrder.push('return')
      return step
    })

    expect(await within(2000, pending), 'the pending next').toBe('settled')
    expect(await within(2000, returned), 'the return').toBe('settled')
    expect(await pending).toEqual({ value: undefined, done: true })
    expect(await returned).toEqual({ value: undefined, done: true })
    expect(settledInOrder).toEqual(['next', 'return'])
    expect(sawAbort, 'the request in flight observed the abort').toBe(true)
    // And it is closed: nothing more is produced, and nothing underneath is
    // asked again.
    expect(await iterator.next()).toEqual({ value: undefined, done: true })
    expect(await iterator.return!(undefined)).toEqual({ value: undefined, done: true })
  })

  /**
   * A run stopped while it is waiting on the **runtime**, three ways.
   *
   * The seam every review round found an interleaving in: something outside the
   * engine is being awaited, and the cleanup that would end it is queued behind
   * that await. Every one of these is bounded, so a regression fails rather
   * than hanging the suite.
   */
  describe.each([
    ['return()', (i: AsyncIterator<AssistantEvent>) => i.return!(undefined)],
    ['throw()', (i: AsyncIterator<AssistantEvent>) => i.throw!(new Error('gave up')).catch(() => undefined)]
  ] as const)('while a tool call is pending, %s', (_name, stop) => {
    it('settles the pending next first, then itself, and asks the runtime nothing more', async () => {
      let arrived = () => {}
      const entered = new Promise<void>((resolve) => {
        arrived = resolve
      })
      let calls = 0
      const { call } = scriptedCall([
        turn({ tool: { name: 'validate', args: { document: {} } } }),
        turn({ text: PROPOSAL_TEXT })
      ])
      const iterator = vercel.start(
        session(call, {}, async () => {
          calls += 1
          arrived()
          // The capability that never settles: an MCP frame on a socket nobody
          // is answering. Nothing about it honours a signal.
          return new Promise<McpToolResult>(() => {})
        })
      )[Symbol.asyncIterator]()

      expect((await iterator.next()).value).toMatchObject({ type: 'tool_call' })
      const pending = iterator.next()
      await entered
      const settledInOrder: string[] = []
      const held = pending.then(() => settledInOrder.push('next'))
      const ended = Promise.resolve(stop(iterator)).then(() => settledInOrder.push('stop'))

      expect(await within(2000, held), 'the pending next').toBe('settled')
      expect(await within(2000, ended), 'the stop').toBe('settled')
      expect(await pending).toEqual({ value: undefined, done: true })
      expect(settledInOrder).toEqual(['next', 'stop'])
      expect(calls, 'the runtime was asked exactly once, before the stop').toBe(1)
    })
  })

  it('settles a pending next when the session itself is aborted mid tool call', async () => {
    // The session's own signal reaches the same cancellation the consumer's
    // `return()` does — it used to abort the SDK and leave the channel alone,
    // so a `next()` waiting on a tool call that never settled waited for ever.
    const controller = new AbortController()
    let arrived = () => {}
    const entered = new Promise<void>((resolve) => {
      arrived = resolve
    })
    const { call } = scriptedCall([
      turn({ tool: { name: 'validate', args: { document: {} } } }),
      turn({ text: PROPOSAL_TEXT })
    ])
    const iterator = vercel.start(
      session(call, { signal: controller.signal }, async () => {
        arrived()
        return new Promise<McpToolResult>(() => {})
      })
    )[Symbol.asyncIterator]()
    expect((await iterator.next()).value).toMatchObject({ type: 'tool_call' })
    const pending = iterator.next()
    await entered
    controller.abort()
    expect(await within(2000, pending), 'the pending next').toBe('settled')
    expect(await pending).toEqual({ value: undefined, done: true })
  })

  it('lets no tools/call reach the runtime after the consumer has closed the run', async () => {
    // Two queued reads and a model answer that arrives late carrying a tool
    // call: the guard is read **before** the call is dispatched, so a closed
    // run has nowhere to send it.
    let asked: string[] = []
    let answer = () => {}
    const held = new Promise<void>((resolve) => {
      answer = resolve
    })
    let turnNumber = 0
    const call: ModelCall = async () => {
      turnNumber += 1
      if (turnNumber === 1) await held
      return new Response(
        turnNumber === 1
          ? turn({ tool: { name: 'validate', args: { document: {} } } })
          : turn({ text: PROPOSAL_TEXT }),
        { status: 200, headers: { 'content-type': 'text/event-stream' } }
      )
    }
    const iterator = vercel.start(
      session(call, {}, async (name) => {
        asked.push(name)
        return { content: [{ type: 'text', text: '{}' }] }
      })
    )[Symbol.asyncIterator]()
    const first = iterator.next()
    const second = iterator.next()
    void first.catch(() => undefined)
    void second.catch(() => undefined)
    expect(await within(2000, iterator.return!(undefined) as Promise<unknown>)).toBe('settled')
    // Now the model answers, with a tool call, into a run that is closed.
    answer()
    await new Promise((resolve) => setTimeout(resolve, 100))
    expect(asked, 'a tools/call reached the runtime after the run closed').toEqual([])
    expect(await first).toEqual({ value: undefined, done: true })
    expect(await second).toEqual({ value: undefined, done: true })
  })

  it('closes the run’s own iterator with done, not with an end yielded from a finally (agent#37)', async () => {
    // The wrapper every run is started through (`managedContext`) closes the loop's iterator itself
    // and discards what it answers, so the loop's own `return()` is read here, from inside that
    // wrapper. A generator with `end` yielded from its `finally` answers `{ done: false }` carrying
    // an event, and a consumer that reads the loop directly is left holding it.
    const { call } = scriptedCall([
      turn({ tool: { name: 'validate', args: { document: {} } } }),
      turn({ text: PROPOSAL_TEXT })
    ])
    const closed: IteratorResult<AssistantEvent>[] = []
    const events = managedContext(session(call), (held) => {
      const loop = runVercelLoop(held, 'vercel')[Symbol.asyncIterator]()
      return {
        [Symbol.asyncIterator]: () => ({
          next: () => loop.next(),
          return: async (value?: unknown) => {
            const answer = await loop.return!(value)
            closed.push(answer)
            return answer
          }
        })
      }
    })[Symbol.asyncIterator]()
    expect((await events.next()).value).toMatchObject({ type: 'tool_call', name: 'validate' })
    await events.return!(undefined)
    expect(closed).toEqual([{ value: undefined, done: true }])
  })

  it('does not dispatch the call of a tool it never offered once the consumer has stopped (agent#37)', async () => {
    // The refusal of an unoffered call is put through the gate as the model made it, and it is the
    // one call the loop makes outside the per-call deadline: the guard on `callTool` is what stops
    // it. The loop is waiting for the consumer to take the `tool_call` event when the consumer
    // leaves; the event is dropped, the wait ends, and the call must not reach the runtime.
    const dispatched: string[] = []
    const { call } = scriptedCall([
      turn({ tool: { name: 'write_file', args: { path: 'pack.json' } } }),
      turn({ text: PROPOSAL_TEXT })
    ])
    const iterator = vercel.start(
      session(call, {}, async (name) => {
        dispatched.push(name)
        return { content: [{ type: 'text', text: 'THE FILE IS SAVED' }] }
      })
    )[Symbol.asyncIterator]()
    expect((await iterator.next()).value).toMatchObject({ type: 'tool_call', name: 'write_file' })
    expect(await within(2000, iterator.return!(undefined))).toBe('settled')
    await new Promise((resolve) => setTimeout(resolve, 100))
    expect(dispatched, 'a tools/call reached the runtime after the consumer stopped').toEqual([])
  })

  it('returns promptly, and asks the runtime nothing more', async () => {
    // The whole path: the model asks for a tool, the adapter's `execute` waits
    // for the `tool_call` event to reach the consumer, and the consumer stops
    // there. The deadlock itself is proved on the channel above — this is the
    // shape it happens in, and the two things a viewer would notice: `return()`
    // settles, and a session nobody is watching asks the runtime nothing more.
    let asked = 0
    const { call, seen } = scriptedCall([
      turn({ tool: { name: 'validate', args: { document: {} } } }),
      turn({ text: PROPOSAL_TEXT })
    ])
    const iterator = vercel.start(
      session(call, {}, async () => {
        asked += 1
        return { content: [{ type: 'text', text: '{"status":"ok"}' }] }
      })
    )[Symbol.asyncIterator]()
    const first = await iterator.next()
    expect(first.value).toMatchObject({ type: 'tool_call', name: 'validate' })
    expect(asked, 'the tool has not been called yet').toBe(0)
    expect(await within(2000, iterator.return!(undefined))).toBe('settled')
    // Whatever the framework's pipeline does as it unwinds, it does not reach
    // the runtime, and it does not take another turn.
    await new Promise((resolve) => setTimeout(resolve, 50))
    expect(asked, 'the runtime was asked after the consumer stopped').toBe(0)
    expect(seen).toHaveLength(1)
  })
})

describe('the unhandled rejection the SDK’s refusal path leaks', () => {
  /**
   * Every rejection Node saw with no handler while `run` was in flight.
   *
   * `process`, not `window`: jsdom does not turn a Node-level unhandled
   * rejection into an `unhandledrejection` event, so a test that dispatched one
   * itself would be measuring its own dispatch. This is the real thing.
   */
  async function unhandled(run: () => Promise<void>): Promise<string[]> {
    const seen: string[] = []
    const watch = (reason: unknown) =>
      seen.push(String((reason as { name?: string } | undefined)?.name ?? reason))
    const others = process.listeners('unhandledRejection')
    process.removeAllListeners('unhandledRejection')
    process.on('unhandledRejection', watch)
    try {
      await run()
      // Node reports an unclaimed rejection a turn or two later, never in the
      // same tick, so the window has to outlast the run.
      for (let tick = 0; tick < 6; tick += 1) await new Promise((r) => setTimeout(r, 10))
    } finally {
      process.off('unhandledRejection', watch)
      for (const listener of others) process.on('unhandledRejection', listener as never)
    }
    return seen
  }

  /** The desk's own refusal: the relay answers 409 when no key is stored. */
  const refuses: ModelCall = async () =>
    new Response(JSON.stringify({ error: 'no key stored', code: 'assistant-no-key' }), {
      status: 409,
      headers: { 'content-type': 'application/json' }
    })

  it('is not leaked at all: the result’s promises are claimed as it is made', async () => {
    // Measured, not asserted: reading `result.text` and leaving it unclaimed
    // produces exactly one `AI_NoOutputGeneratedError` on this path. Claiming
    // every promise-valued member of the result produces none, and nothing is
    // suppressed anywhere to achieve it.
    const events: AssistantEvent[] = []
    const leaked = await unhandled(async () => {
      for await (const event of vercel.start(session(refuses))) events.push(event)
    })
    expect(leaked).toEqual([])
    expect(events.map((event) => event.type)).toEqual(['error', 'end'])
  })

  it('installs no page listener, so an unrelated rejection stays observable', async () => {
    // The guard this replaces suppressed **every** page rejection whose reason
    // merely carried that error name — an unrelated operation elsewhere in the
    // page, during a run, was hidden from the browser's own diagnostics.
    const iterator = vercel.start(session(scriptedCall([turn({ text: PROPOSAL_TEXT })]).call))[
      Symbol.asyncIterator
    ]()
    await iterator.next()
    const event = new Event('unhandledrejection', { cancelable: true })
    ;(event as { reason?: unknown }).reason = { name: 'AI_NoOutputGeneratedError' }
    globalThis.dispatchEvent(event)
    expect(event.defaultPrevented, 'something on this page suppressed it').toBe(false)
    await iterator.return!(undefined)
  })

  it('claims the promise-valued members the SDK exposes, and steps over the rest', () => {
    // The enumeration itself, on a stand-in: own properties and prototype
    // getters alike, a member that throws on being read stepped over, and
    // nothing claimed that is not a promise.
    let read = 0
    let streamReads = 0
    const stand = Object.create({
      get stream() { streamReads += 1; return new ReadableStream() },
      get textStream() { streamReads += 1; return new ReadableStream() },
      get inherited() {
        read += 1
        return Promise.reject(new Error('claimed'))
      },
      get throws(): unknown {
        throw new Error('a member that will not be read')
      }
    }) as Record<string, unknown>
    stand.own = Promise.reject(new Error('claimed'))
    stand.plain = 'not a promise'
    expect(claimPromises(stand)).toBe(2)
    expect(read).toBe(1)
    expect(streamReads).toBe(0)
  })
})

describe('what the model said about its own reasoning', () => {
  it('reaches the contract as reasoning events, whatever the tier is', async () => {
    // The tier is what this desk *asks* for, and this chunk asks for nothing.
    // A model that always thinks reasons anyway, and the SDK surfaces that as
    // its own `reasoning-start` / `-delta` / `-end` part types.
    const { call } = scriptedCall([
      turn({ reasoning: ['I check the schema ', 'before I propose.'], text: PROPOSAL_TEXT })
    ])
    const events = await drain(vercel.start(session(call)))
    expect(events.map((event) => event.type)).toEqual([
      'reasoning',
      'reasoning',
      'reasoning',
      'message',
      'proposal',
      'end'
    ])
    expect(events.slice(0, 3)).toEqual([
      { type: 'reasoning', text: 'I check the schema ', done: false },
      { type: 'reasoning', text: 'before I propose.', done: false },
      // The whole passage on `done`, so a reader has it rather than the pieces.
      { type: 'reasoning', text: 'I check the schema before I propose.', done: true }
    ])
    // **And no capability is claimed from one turn.** One unsolicited passage
    // is a turn, not a model that always thinks.
    expect(events.some((event) => event.type === 'thinking_unavailable')).toBe(false)
  })

  it('reports a model that always thinks after two answers of it', async () => {
    // A turn that only called a tool neither counts nor resets, in either
    // tier, so the two that count are the two that answered — with a tool-only
    // turn between them, which must not wipe the count.
    const { call } = scriptedCall([
      turn({
        reasoning: ['I look first.'],
        text: 'Let me look.',
        tool: { name: 'validate', args: { document: '{}' } }
      }),
      turn({ tool: { name: 'validate', args: { document: '{}' } } }),
      turn({ reasoning: ['And then I propose.'], text: PROPOSAL_TEXT })
    ])
    const events = await drain(vercel.start(session(call)))
    const notices = events.filter(
      (event): event is Extract<AssistantEvent, { type: 'thinking_unavailable' }> =>
        event.type === 'thinking_unavailable'
    )
    expect(notices).toHaveLength(1)
    expect(notices[0]!.detail).toContain('always thinks')
  })

  it('says nothing where the endpoint reasoned about nothing', async () => {
    const { call } = scriptedCall([turn({ text: PROPOSAL_TEXT })])
    const events = await drain(vercel.start(session(call)))
    expect(events.some((event) => event.type === 'reasoning')).toBe(false)
  })
})

describe('the thinking tier, through the SDK’s own call settings', () => {
  /**
   * **The translation, held to the desk's table.**
   *
   * `sdkThinking` is the one place this adapter turns the desk's wire members
   * into the SDK's vocabulary, and the assertion below is that what comes out
   * the other end of the SDK is what the table asked for — measured on the
   * body, not on the option object.
   */
  it.each([
    ['openai-compatible', 'on'],
    ['openai-compatible', 'ultra'],
    ['anthropic', 'on'],
    ['anthropic', 'ultra']
  ] as const)('puts the desk’s own members on a %s request at %s', async (family, tier) => {
    const answer =
      family === 'anthropic' ? anthropicTurn({ text: PROPOSAL_TEXT }) : turn({ text: PROPOSAL_TEXT })
    const { call, seen } = scriptedCall([answer])
    await drain(
      vercel.start(
        session(call, {
          model: { family, model: 'a-model', call },
          thinking: normalize(tier, family)
        })
      )
    )
    const table = normalize(tier, family).wire!.members
    for (const [member, value] of Object.entries(table)) {
      expect(seen[0]!.body[member], `${family} ${tier} ${member}`).toEqual(value)
    }
    // The fallback dialect's pair, through the SDK's own call settings.
    const fallback = wireFor(tier, 'anthropic-enabled')!.members as { max_tokens: number }
    if (family === 'anthropic') {
      expect(sdkThinking(family, wireFor(tier, 'anthropic-enabled')!.members)).toMatchObject({
        maxOutputTokens: fallback.max_tokens
      })
    }
  })

  it('sends no tier member at all where the tier is off', async () => {
    const { call, seen } = scriptedCall([turn({ text: PROPOSAL_TEXT })])
    await drain(vercel.start(session(call)))
    expect(Object.keys(seen[0]!.body)).not.toContain('reasoning_effort')
    expect(Object.keys(seen[0]!.body)).not.toContain('thinking')
  })

  it('puts the desk’s own members on a gemini request, in the place that wire wants', async () => {
    // **The table names the member and the engine places it.** On this wire the
    // tier lives inside `generationConfig`, which is where the SDK's own
    // provider option puts it — so what is asserted is the composed body, not
    // the option object, exactly as the other two families are.
    const { call, seen } = scriptedCall([geminiTurn({ text: PROPOSAL_TEXT })])
    await drain(
      vercel.start(
        session(call, {
          model: { family: 'gemini', model: 'a-model', call },
          thinking: normalize('on', 'gemini')
        })
      )
    )
    const config = (seen[0]!.body.generationConfig ?? {}) as Record<string, unknown>
    expect(config.thinkingConfig).toEqual(normalize('on', 'gemini').wire!.members.thinkingConfig)
    // And the address is the wire's, with the one query pair the relay admits.
    expect(seen[0]!.suffix).toBe('v1beta/models/a-model:streamGenerateContent?alt=sse')
  })

  it('translates the Gemini spelling into the SDK’s own provider option', () => {
    expect(sdkThinking('gemini', wireFor('ultra', 'gemini-budget')!.members)).toEqual({
      providerOptions: {
        [GOOGLE_OPTIONS]: { thinkingConfig: { includeThoughts: true, thinkingBudget: 24576 } }
      }
    })
    expect(sdkThinking('gemini', wireFor('off', 'gemini-level')!.members)).toEqual({
      providerOptions: { [GOOGLE_OPTIONS]: { thinkingConfig: { thinkingLevel: 'minimal' } } }
    })
    // **Not the desk's key to choose.** The provider reads its options from the
    // part of its own id before the dot, and that id is fixed by the SDK.
    expect(GOOGLE_OPTIONS).toBe('google')
  })

  it('translates each of the desk’s three dialects and nothing else', () => {
    expect(sdkThinking('openai-compatible', null)).toEqual({})
    expect(sdkThinking('openai-compatible', { reasoning_effort: 'xhigh' })).toEqual({
      providerOptions: { deskEndpoint: { reasoningEffort: 'xhigh' } }
    })
    expect(
      sdkThinking('anthropic', { thinking: { type: 'adaptive' }, output_config: { effort: 'high' } })
    ).toEqual({ providerOptions: { anthropic: { thinking: { type: 'adaptive' }, effort: 'high' } } })
    expect(
      sdkThinking('anthropic', {
        thinking: { type: 'enabled', budget_tokens: 16000 },
        max_tokens: 20096
      })
    ).toEqual({
      providerOptions: { anthropic: { thinking: { type: 'enabled', budgetTokens: 16000 } } },
      // The SDK's spelling of the number the desk's table chose beside the
      // budget: Anthropic spends the budget out of the request's maximum.
      maxOutputTokens: 20096
    })
  })

  it('degrades once on a 400 that names the member, and completes', async () => {
    const seen: Record<string, unknown>[] = []
    const call: ModelCall = async (_suffix, request) => {
      // The critic is a second conversation and is not what this measures.
      if (request.body.includes(REFUTATION_MARKER)) {
        return new Response(turn({ text: 'REFUTATION: nothing to report.' }), {
          status: 200,
          headers: { 'content-type': 'text/event-stream' }
        })
      }
      seen.push(JSON.parse(request.body) as Record<string, unknown>)
      if (seen.length === 1) {
        return new Response(
          JSON.stringify({ error: { message: 'Unsupported parameter: reasoning_effort' } }),
          { status: 400, headers: { 'content-type': 'application/json' } }
        )
      }
      return new Response(turn({ reasoning: ['x'], text: PROPOSAL_TEXT }), {
        status: 200,
        headers: { 'content-type': 'text/event-stream' }
      })
    }
    const events = await drain(
      vercel.start(session(call, { thinking: normalize('on', 'openai-compatible') }))
    )
    expect(seen).toHaveLength(2)
    expect(seen[0]!.reasoning_effort).toBe('high')
    // The retry is the identical request without the member.
    expect(Object.keys(seen[1]!)).not.toContain('reasoning_effort')
    const notices = events.filter((event) => event.type === 'thinking_unavailable')
    expect(notices).toHaveLength(1)
    expect(events.some((event) => event.type === 'proposal')).toBe(true)
    expect(events[events.length - 1]!.type).toBe('end')
  })

  it('falls back once to the other Anthropic spelling, and says nothing about it', async () => {
    const seen: Record<string, unknown>[] = []
    const call: ModelCall = async (_suffix, request) => {
      if (request.body.includes(REFUTATION_MARKER)) {
        return new Response(anthropicTurn({ text: 'REFUTATION: nothing to report.' }), {
          status: 200,
          headers: { 'content-type': 'text/event-stream' }
        })
      }
      seen.push(JSON.parse(request.body) as Record<string, unknown>)
      if (seen.length === 1) {
        return new Response(
          JSON.stringify({ error: { message: 'Adaptive thinking is not supported by this model' } }),
          { status: 400, headers: { 'content-type': 'application/json' } }
        )
      }
      return new Response(anthropicTurn({ reasoning: 'I read it.', text: PROPOSAL_TEXT }), {
        status: 200,
        headers: { 'content-type': 'text/event-stream' }
      })
    }
    const events = await drain(
      vercel.start(
        session(call, {
          model: { family: 'anthropic', model: 'a-model', call },
          thinking: normalize('on', 'anthropic')
        })
      )
    )
    expect(seen).toHaveLength(2)
    expect(seen[0]!.thinking).toEqual({ type: 'adaptive' })
    expect(seen[1]!.thinking).toEqual({ type: 'enabled', budget_tokens: 8000 })
    expect(events.some((event) => event.type === 'thinking_unavailable')).toBe(false)
  })

  it('reports unavailable after two answers with no reasoning, and not after one', async () => {
    // The critic's own turn is this session's second answer, so the second
    // observation lands there on a session that proposes at once and asks for
    // the critic (agent#10: the tier alone no longer starts one).
    const { call, seen } = scriptedCall([turn({ text: PROPOSAL_TEXT })])
    const events = await drain(
      vercel.start(session(call, { thinking: normalize('on', 'openai-compatible'), adversarialReview: true }))
    )
    const notices = events.filter((event) => event.type === 'thinking_unavailable')
    expect(notices).toHaveLength(1)
    expect((notices[0] as { detail: string }).detail).toContain('no reasoning block')
    expect(events.some((event) => event.type === 'proposal')).toBe(true)
    // The first request still carried the tier: nothing was concluded from one
    // turn.
    expect(seen[0]!.body.reasoning_effort).toBe('high')
  })

  it('does not conclude anything from a turn that only called a tool', async () => {
    const { call } = scriptedCall([
      turn({ tool: { name: 'validate', args: { document: '{}' } } }),
      turn({ reasoning: ['I did think.'], text: PROPOSAL_TEXT })
    ])
    const events = await drain(
      vercel.start(session(call, { thinking: normalize('on', 'openai-compatible') }))
    )
    expect(events.some((event) => event.type === 'thinking_unavailable')).toBe(false)
  })
})

describe('the split signature this SDK truncates (vercel/ai#19663)', () => {
  it('reassembles the fragments the desk saw, and refuses the fragment', () => {
    const ledger = signatureLedger()
    ledger.fragment('0', 'c2lnbmF0')
    ledger.fragment('0', 'dXJlLVQx')
    expect(ledger.signed().map((one) => one.signature)).toEqual(['c2lnbmF0dXJlLVQx'])
    const body = JSON.stringify({
      messages: [
        {
          role: 'assistant',
          content: [
            { type: 'thinking', thinking: 'I read it.', signature: 'dXJlLVQx' },
            { type: 'text', text: 'hello' }
          ]
        }
      ]
    })
    const filtered = withoutTruncatedThinking('anthropic', body, ledger)
    expect(filtered.truncated).toContain('19663')
    const sent = JSON.parse(filtered.body) as { messages: { content: { type: string }[] }[] }
    expect(sent.messages[0]!.content.map((block) => block.type)).toEqual(['text'])
  })

  it('rebuilds the request from the slot rather than filtering the composed one', () => {
    const ledger = signatureLedger()
    ledger.fragment('0', 'c2lnbmF0dXJlLVQx')
    const body = JSON.stringify({
      model: 'm',
      max_tokens: 12096,
      thinking: { type: 'enabled', budget_tokens: 8000 },
      output_config: { effort: 'high' },
      messages: [
        {
          role: 'assistant',
          content: [
            { type: 'thinking', thinking: 'I read it.', signature: 'dXJlLVQx' },
            { type: 'text', text: 'hello' }
          ]
        }
      ]
    })
    // The slot has degraded by the time this is read, so it asks for nothing.
    const rebuilt = withoutTruncatedThinking('anthropic', body, ledger, () => null)
    const sent = JSON.parse(rebuilt.body) as Record<string, unknown>
    expect(rebuilt.truncated).toContain('19663')
    expect(Object.keys(sent)).not.toContain('thinking')
    expect(Object.keys(sent)).not.toContain('output_config')
    // `max_tokens` stays: the protocol requires one on every request, and one
    // larger than a degraded session needs is legal.
    expect(sent.max_tokens).toBe(12096)
    expect(sent.model).toBe('m')
  })

  it('leaves a whole signature exactly where it was', () => {
    const ledger = signatureLedger()
    ledger.fragment('0', 'c2lnbmF0dXJlLVQx')
    const body = JSON.stringify({
      messages: [
        { role: 'assistant', content: [{ type: 'thinking', thinking: 'x', signature: 'c2lnbmF0dXJlLVQx' }] }
      ]
    })
    const filtered = withoutTruncatedThinking('anthropic', body, ledger)
    expect(filtered.truncated).toBe('')
    expect(filtered.body).toBe(body)
  })

  it('says the endpoint has no thinking before the degraded request is answered', async () => {
    // **The slot changes at the transition, so the line must too.** The rebuilt
    // request carries no thinking member at all; a notice held until that
    // request produced a stream part left the tab saying `thinking on` about a
    // request that carried none — for as long as it took, or for ever. This
    // holds the second request open and asserts the line has already arrived.
    let holding = 0
    const call: ModelCall = async (_suffix, request) => {
      const body = JSON.parse(request.body) as Record<string, unknown>
      if (!('thinking' in body)) {
        // The rebuilt request: never answered.
        holding += 1
        return new Promise<Response>(() => {})
      }
      return new Response(
        anthropicTurn({
          reasoning: 'I will check it.',
          signature: 'c2lnbmF0dXJlLVQx',
          splitSignature: true,
          tool: { name: 'validate', args: { document: '{}' } }
        }),
        { status: 200, headers: { 'content-type': 'text/event-stream' } }
      )
    }
    const iterator = vercel
      .start(
        session(call, {
          model: { family: 'anthropic', model: 'a-model', call },
          thinking: normalize('on', 'anthropic')
        })
      )
      [Symbol.asyncIterator]()
    const seen: AssistantEvent[] = []
    for (;;) {
      const step = await iterator.next()
      if (step.done === true) break
      seen.push(step.value)
      if (step.value.type === 'thinking_unavailable') break
    }
    // It arrived — **before the degraded request was even dispatched**, which
    // is stronger than "before it was answered". Under the shape this replaces
    // the notice waited for a stream part from that request, and this request
    // never produces one: the loop above would have run to the test's timeout
    // with the tab still saying `thinking on`.
    const notice = seen.find((event) => event.type === 'thinking_unavailable')
    expect(notice, 'the notice waited for a request that never answered').toBeDefined()
    expect((notice as { detail: string }).detail).toContain('19663')
    expect(holding, 'the notice came after the request went out').toBe(0)
    // And the run ends when the consumer stops listening, holding nothing.
    await iterator.return?.()
  }, 20000)

  it('starts a new signature at each turn, because the block ids repeat', () => {
    // **The turn boundary, which is not decoration.** On the Anthropic wire a
    // reasoning part is keyed by its index in the message, so every turn starts
    // again at `0`. A ledger with no boundary concatenates one turn's signature
    // onto the next and then reports the next turn's *whole* signature as a
    // fragment of the pair — measured, on a session's third turn.
    const ledger = signatureLedger()
    ledger.fragment('0', 'c2lnLVQx')
    ledger.boundary()
    ledger.fragment('0', 'c2lnLVQy')
    expect(ledger.signed().map((one) => one.signature)).toEqual(['c2lnLVQx', 'c2lnLVQy'])
  })

  it('lets a later block whose own signature is shorter survive', () => {
    // **Block identity, by position.** The ledger holds one signature per
    // signed block in the order they were sent, and the history carries the
    // same blocks in the same order. A global membership test threw the second
    // block away because its signature happened to be a prefix of the first's.
    const ledger = signatureLedger()
    ledger.fragment('0', 'abcdef')
    ledger.boundary()
    ledger.fragment('0', 'abc')
    const body = JSON.stringify({
      messages: [
        { role: 'assistant', content: [{ type: 'thinking', thinking: 'one', signature: 'abcdef' }] },
        { role: 'user', content: [{ type: 'text', text: 'and?' }] },
        { role: 'assistant', content: [{ type: 'thinking', thinking: 'two', signature: 'abc' }] }
      ]
    })
    const looked = withoutTruncatedThinking('anthropic', body, ledger, () => null)
    expect(looked.truncated).toBe('')
    expect(looked.body).toBe(body)
  })

  it('still catches the block that did come back as a fragment', () => {
    const ledger = signatureLedger()
    ledger.fragment('0', 'abcdef')
    ledger.boundary()
    ledger.fragment('0', 'abc')
    // The FIRST block came back halved; the second is its own whole signature.
    const body = JSON.stringify({
      messages: [
        { role: 'assistant', content: [{ type: 'thinking', thinking: 'one', signature: 'abc' }] },
        { role: 'assistant', content: [{ type: 'thinking', thinking: 'two', signature: 'abc' }] }
      ]
    })
    const looked = withoutTruncatedThinking('anthropic', body, ledger, () => null)
    expect(looked.truncated).toContain('19663')
    const sent = JSON.parse(looked.body) as { messages: { content: { type: string }[] }[] }
    expect(sent.messages[0]!.content).toEqual([])
    expect(sent.messages[1]!.content).toHaveLength(1)
  })

  it('keeps two blocks that were signed the same, because they are two blocks', () => {
    // **Collapsing duplicates lost a block's identity.** With `abcdef`,
    // `abcdef`, `uvwxyz` the ledger became two entries, so a third block that
    // came back as `uvw` was compared against the second entry — or against
    // nothing — and was sent.
    const ledger = signatureLedger()
    ledger.fragment('0', 'abcdef')
    ledger.boundary()
    ledger.fragment('0', 'abcdef')
    ledger.boundary()
    ledger.fragment('0', 'uvwxyz')
    expect(ledger.signed().map((one) => one.signature)).toEqual(['abcdef', 'abcdef', 'uvwxyz'])
    const body = JSON.stringify({
      messages: [
        { role: 'assistant', content: [{ type: 'thinking', thinking: 'one', signature: 'abcdef' }] },
        { role: 'assistant', content: [{ type: 'thinking', thinking: 'two', signature: 'abcdef' }] },
        { role: 'assistant', content: [{ type: 'thinking', thinking: 'three', signature: 'uvw' }] }
      ]
    })
    const looked = withoutTruncatedThinking('anthropic', body, ledger, () => null)
    expect(looked.truncated).toContain('19663')
    const sent = JSON.parse(looked.body) as { messages: { content: unknown[] }[] }
    expect(sent.messages[0]!.content).toHaveLength(1)
    expect(sent.messages[1]!.content).toHaveLength(1)
    expect(sent.messages[2]!.content).toEqual([])
  })

  it('compares the critic’s fresh conversation against its own blocks only', () => {
    // The critic's history carries none of the main loop's blocks, correctly.
    // Comparing its first block against the loop's first signature would be
    // comparing two different conversations' positions.
    const ledger = signatureLedger()
    ledger.fragment('0', 'abcdef')
    ledger.boundary()
    ledger.conversation()
    expect(ledger.signed()).toEqual([])
    const body = JSON.stringify({
      messages: [
        { role: 'assistant', content: [{ type: 'thinking', thinking: 'critic', signature: 'abc' }] }
      ]
    })
    // Nothing signed in this conversation yet, so nothing is compared.
    expect(withoutTruncatedThinking('anthropic', body, ledger, () => null).truncated).toBe('')
    ledger.fragment('0', 'zzzzzz')
    expect(ledger.signed().map((one) => one.signature)).toEqual(['zzzzzz'])
    const short = JSON.stringify({
      messages: [
        { role: 'assistant', content: [{ type: 'thinking', thinking: 'critic', signature: 'zzz' }] }
      ]
    })
    expect(withoutTruncatedThinking('anthropic', short, ledger, () => null).truncated).toContain('19663')
  })

  it('does not double a signature the SDK repeated whole', () => {
    // The SDK re-emits the same value where the endpoint sent one event, and a
    // ledger that appended blindly would invent a truncation nobody caused.
    const ledger = signatureLedger()
    ledger.fragment('0', 'c2ln')
    ledger.fragment('0', 'c2ln')
    expect(ledger.signed().map((one) => one.signature)).toEqual(['c2ln'])
  })

  /**
   * **The measurement, end to end — and it is written to fail if the SDK is
   * ever fixed in silence.**
   *
   * The fixture splits one signature across two `signature_delta` events, which
   * is what a re-chunking proxy does. If this SDK ever reassembles them, the
   * desk detects no truncation, no notice is emitted, and this case goes red —
   * which is the point: the guard would then be dead code, and the desk should
   * find out from its own suite rather than from an endpoint.
   */
  it('detects the truncation on a real session and degrades once', async () => {
    const seen: Record<string, unknown>[] = []
    const call: ModelCall = async (_suffix, request) => {
      if (request.body.includes(REFUTATION_MARKER)) {
        return new Response(anthropicTurn({ text: 'REFUTATION: nothing to report.' }), {
          status: 200,
          headers: { 'content-type': 'text/event-stream' }
        })
      }
      seen.push(JSON.parse(request.body) as Record<string, unknown>)
      if (seen.length === 1) {
        return new Response(
          anthropicTurn({
            reasoning: 'I will check it.',
            signature: 'c2lnbmF0dXJlLVQx',
            splitSignature: true,
            tool: { name: 'validate', args: { document: '{}' } }
          }),
          { status: 200, headers: { 'content-type': 'text/event-stream' } }
        )
      }
      return new Response(anthropicTurn({ reasoning: 'Done.', text: PROPOSAL_TEXT }), {
        status: 200,
        headers: { 'content-type': 'text/event-stream' }
      })
    }
    const events = await drain(
      vercel.start(
        session(call, {
          model: { family: 'anthropic', model: 'a-model', call },
          thinking: normalize('on', 'anthropic')
        })
      )
    )
    const notices = events.filter(
      (event): event is Extract<AssistantEvent, { type: 'thinking_unavailable' }> =>
        event.type === 'thinking_unavailable'
    )
    expect(
      notices.map((notice) => notice.detail),
      'the SDK reassembled the split signature — the guard is now dead code'
    ).toHaveLength(1)
    expect(notices[0]!.detail).toContain('19663')
    // And no malformed block left the page: the second request carries no
    // thinking block at all rather than one with half a signature.
    const carried = (seen[1]!.messages as { role: string; content: unknown }[])
      .flatMap((message) => (Array.isArray(message.content) ? message.content : []))
      .filter((block) => (block as { type?: string }).type === 'thinking')
    expect(carried).toEqual([])
    // **And it does not ask for thinking either.** A request that both asks for
    // thinking and has dropped a block the endpoint signed is a continuation a
    // real endpoint may refuse: the body was composed before the slot degraded,
    // so it is rebuilt from what the slot says afterwards rather than filtered.
    expect(Object.keys(seen[1]!)).not.toContain('thinking')
    expect(Object.keys(seen[1]!)).not.toContain('output_config')
    // The session still completes: a degrade is not a refusal.
    expect(events[events.length - 1]!.type).toBe('end')
    expect(events.some((event) => event.type === 'proposal')).toBe(true)
  })
})

describe('what the author is told when the endpoint refuses', () => {
  // Browser Stop/refusal regressions are also exercised by
  // scripts/chat-response-check.mjs; a jsdom test cannot reproduce the SDK's
  // non-Node telemetry path.
  it('reports typed status without the endpoint’s prose, and ends once', async () => {
    const call: ModelCall = async () =>
      new Response(JSON.stringify({ error: { message: 'this endpoint refuses everything' } }), {
        status: 400,
        headers: { 'content-type': 'application/json' }
      })
    const events = await drain(vercel.start(session(call)))
    expect(events.map((event) => event.type)).toEqual(['error', 'end'])
    const said = (events[0] as { message: string }).message
    expect(events[0]).toMatchObject({failure:{code:'invalid-request',status:400}})
    expect(said).not.toContain('this endpoint refuses everything')
    // …and no address in it: the SDK's error carries the placeholder origin,
    // which says nothing useful and reads as a real host.
    expect(said).not.toContain('relay.invalid')
    expect(said).not.toContain('http')
  })
})

describe('the refutation pass, on this SDK’s second streamText', () => {
  const VALID = JSON.stringify({ status: 'valid', diagnostics: [] })
  const INVALID = JSON.stringify({
    status: 'invalid',
    diagnostics: [{ code: 'JPS-SEMANTIC-UNRESOLVED-OUTCOME' }]
  })

  /**
   * A session whose main loop proposes at once and whose critic runs a script.
   * `review`: whether the session asks for the critic; agent#10 made that the
   * one thing that starts it, at any tier.
   */
  function criticised(options: {
    tier: 'off' | 'on' | 'ultra'
    review?: boolean
    criticCalls?: { name: string; args: Record<string, unknown> }[]
    criticSays: string
    answer: string
  }): { session: AssistantSession; asked: { name: string; args: unknown }[]; critic: string[] } {
    const asked: { name: string; args: unknown }[] = []
    const critic: string[] = []
    let criticTurn = 0
    const call: ModelCall = async (_suffix, request) => {
      const stream = (text: string) =>
        new Response(text, { status: 200, headers: { 'content-type': 'text/event-stream' } })
      if (request.body.includes(REFUTATION_MARKER)) {
        critic.push(request.body)
        criticTurn += 1
        const next = (options.criticCalls ?? [])[criticTurn - 1]
        return stream(
          next === undefined ? turn({ text: options.criticSays }) : turn({ tool: next })
        )
      }
      return stream(turn({ text: PROPOSAL_TEXT }))
    }
    return {
      session: session(
        call,
        { thinking: normalize(options.tier, 'openai-compatible'), ...(options.review === undefined ? {} : { adversarialReview: options.review }) },
        async (name, args) => {
          asked.push({ name, args })
          return { content: [{ type: 'text', text: options.answer }] }
        }
      ),
      asked,
      critic
    }
  }

  it('does not run where the session does not ask for it, at tier off or on (agent#10)', async () => {
    for (const tier of ['off', 'on'] as const) {
      const one = criticised({ tier, criticSays: 'x', answer: VALID })
      const events = await drain(vercel.start(one.session))
      expect(one.critic, tier).toEqual([])
      expect(events.some((event) => event.type === 'critique'), tier).toBe(false)
    }
  })

  it('runs after the proposal exists and before it is shown', async () => {
    const one = criticised({
      tier: 'on',
      review: true,
      criticCalls: [{ name: 'validate', args: { document: '{}' } }],
      criticSays: 'REFUTATION: none found.',
      answer: VALID
    })
    const events = await drain(vercel.start(one.session))
    const kinds = events.map((event) => event.type)
    expect(kinds).toContain('critique')
    expect(kinds.indexOf('critique')).toBeLessThan(kinds.indexOf('proposal'))
    // The critic's own call travelled the session's `callTool` — the same
    // capability, through the same gate, as the main loop's.
    expect(one.asked).toEqual([{ name: 'validate', args: { document: '{}' } }])
    expect(kinds.filter((kind) => kind === 'tool_call')).toHaveLength(1)
  })

  it('takes the verdict from the runtime and not from the critic’s prose', async () => {
    const one = criticised({
      tier: 'on',
      review: true,
      criticCalls: [{ name: 'validate', args: { document: '{}' } }],
      criticSays: 'REFUTATION: this pack is broken and must not be used.',
      answer: VALID
    })
    const events = await drain(vercel.start(one.session))
    const critique = events.find(
      (event): event is Extract<AssistantEvent, { type: 'critique' }> => event.type === 'critique'
    )!
    expect(critique.refuted).toBe(false)
    expect(critique.checks).toEqual([{ tool: 'validate', status: 'valid' }])
  })

  it('reports refuted where the runtime refused, whatever the critic said', async () => {
    const one = criticised({
      tier: 'on',
      review: true,
      criticCalls: [{ name: 'validate', args: { document: '{}' } }],
      criticSays: 'REFUTATION: none found. Everything checks out.',
      answer: INVALID
    })
    const events = await drain(vercel.start(one.session))
    const critique = events.find(
      (event): event is Extract<AssistantEvent, { type: 'critique' }> => event.type === 'critique'
    )!
    expect(critique.refuted).toBe(true)
    const proposal = events.find(
      (event): event is Extract<AssistantEvent, { type: 'proposal' }> => event.type === 'proposal'
    )!
    expect(proposal.critique).toEqual({ refuted: true })
    expect(proposal.document).toBeDefined()
  })

  it('says the critic ran no check, and puts no line on the proposal', async () => {
    const one = criticised({ tier: 'on', review: true, criticSays: 'I had a look.', answer: VALID })
    const events = await drain(vercel.start(one.session))
    const critique = events.find(
      (event): event is Extract<AssistantEvent, { type: 'critique' }> => event.type === 'critique'
    )!
    expect(critique.checks).toEqual([])
    expect(critique.text).toContain('no runtime check')
    const proposal = events.find(
      (event): event is Extract<AssistantEvent, { type: 'proposal' }> => event.type === 'proposal'
    )!
    expect(proposal.critique).toBeUndefined()
  })

  it('rehearses the critic’s evaluate through the same hook as the main loop’s', async () => {
    // The critic gets the **same** tool set and the same refinement hook, so an
    // evaluate it asks for without a rehearsal member is rewritten before it is
    // executed — and the desk's gate below rewrites it again on the wire.
    const one = criticised({
      tier: 'on',
      review: true,
      criticCalls: [{ name: 'experimental_evaluate', args: { pack: '{}', facts: '{}' } }],
      criticSays: 'REFUTATION: none found.',
      answer: JSON.stringify({ status: 'evaluated', rehearsal: true })
    })
    const events = await drain(vercel.start(one.session))
    expect(one.asked).toEqual([
      { name: 'experimental_evaluate', args: { pack: '{}', facts: '{}' } }
    ])
    const critique = events.find(
      (event): event is Extract<AssistantEvent, { type: 'critique' }> => event.type === 'critique'
    )!
    expect(critique.checks).toEqual([{ tool: 'experimental_evaluate', status: 'evaluated' }])
    // **`evaluated` is not `valid`, and it does not refute.** One word over
    // both tools would report every session ever run as refuted.
    expect(critique.refuted).toBe(false)
  })

  it('hands the critic the runtime’s testing prompt and the proposed document', async () => {
    const one = criticised({ tier: 'on', review: true, criticSays: 'done', answer: VALID })
    await drain(vercel.start(one.session))
    expect(one.critic[0]).toContain('test_pack guidance')
    expect(one.critic[0]).toContain(REFUTATION_MARKER)
    expect(one.critic[0]).toContain('A pack')
  })

  it('ends where the run does, and says nothing after it', async () => {
    const controller = new AbortController()
    const call: ModelCall = async (_suffix, request) => {
      if (request.body.includes(REFUTATION_MARKER)) {
        controller.abort()
        return new Promise<Response>(() => {})
      }
      return new Response(turn({ text: PROPOSAL_TEXT }), {
        status: 200,
        headers: { 'content-type': 'text/event-stream' }
      })
    }
    const events = await drain(
      vercel.start(
        session(call, {
          signal: controller.signal,
          thinking: normalize('on', 'openai-compatible'),
          adversarialReview: true
        })
      )
    )
    expect(events.map((event) => event.type)).not.toContain('critique')
    expect(events.map((event) => event.type)).not.toContain('proposal')
    expect(events.map((event) => event.type)).not.toContain('end')
  })
})

describe('the same signature rules, on the Gemini wire', () => {
  it('reads a signature from the provider metadata this SDK puts it under', () => {
    // **Two wires sign reasoning and the SDK names each one differently.** A
    // reader that only knew the Anthropic spelling would ledger nothing here,
    // and a truncation would then be invisible rather than detected.
    expect(
      signatureOf('gemini', { providerMetadata: { google: { thoughtSignature: 'c2ln' } } })
    ).toBe('c2ln')
    expect(
      signatureOf('anthropic', { providerMetadata: { anthropic: { signature: 'c2ln' } } })
    ).toBe('c2ln')
    // Each family reads its own and no other's.
    expect(
      signatureOf('gemini', { providerMetadata: { anthropic: { signature: 'c2ln' } } })
    ).toBeUndefined()
    expect(
      signatureOf('anthropic', { providerMetadata: { google: { thoughtSignature: 'c2ln' } } })
    ).toBeUndefined()
    // And the wire with no signatures has no ledger to keep.
    expect(signsReasoning('openai-compatible')).toBe(false)
    expect(signsReasoning('gemini')).toBe(true)
    expect(
      signatureOf('openai-compatible', { providerMetadata: { google: { thoughtSignature: 'x' } } })
    ).toBeUndefined()
  })

  it('removes a thought part whose signature came back short, and rebuilds the request', () => {
    // The same rule as the Anthropic arm, read against this wire's own shape:
    // `contents[].parts[]` of `{thought: true, thoughtSignature}`, and the tier
    // one level down in `generationConfig`.
    const ledger = signatureLedger()
    ledger.fragment('0', 'c2lnbmF0dXJlLVQx')
    const body = JSON.stringify({
      contents: [
        {
          role: 'model',
          parts: [
            { text: 'I read it.', thought: true, thoughtSignature: 'dXJlLVQx' },
            { text: 'hello' }
          ]
        }
      ],
      generationConfig: { thinkingConfig: { includeThoughts: true, thinkingBudget: 8192 } },
      systemInstruction: { parts: [{ text: 'system' }] }
    })
    const rebuilt = withoutTruncatedThinking('gemini', body, ledger, () => null)
    expect(rebuilt.truncated).toContain('19663')
    const sent = JSON.parse(rebuilt.body) as {
      contents: { parts: Record<string, unknown>[] }[]
      generationConfig: Record<string, unknown>
      systemInstruction: unknown
    }
    expect(sent.contents[0]!.parts).toEqual([{ text: 'hello' }])
    // **Rebuilt and not filtered**: the tier comes off, one level down, and
    // `generationConfig` itself stays because it is the request's own settings
    // rather than the tier.
    expect(sent.generationConfig).toEqual({})
    expect(sent.systemInstruction).toEqual({ parts: [{ text: 'system' }] })
  })

  it('compares a signature the wire put on the function call, not only on a summary', () => {
    // **The placement that matters most in function calling**, and the one the
    // first version of this scanner could not see: Gemini signs the first
    // `functionCall` part of a turn, so a scanner that read only
    // `thought === true` never compared it — and a truncated one would have
    // gone back unnoticed.
    const ledger = signatureLedger()
    ledger.fragment('call-1', 'c2lnbmF0dXJlLVQx')
    const body = JSON.stringify({
      contents: [
        {
          role: 'model',
          parts: [
            { functionCall: { name: 'validate', args: {} }, thoughtSignature: 'dXJlLVQx' },
            { functionCall: { name: 'validate', args: {} } }
          ]
        }
      ]
    })
    const rebuilt = withoutTruncatedThinking('gemini', body, ledger, () => null)
    expect(rebuilt.truncated).toContain('19663')
    const sent = JSON.parse(rebuilt.body) as { contents: { parts: unknown[] }[] }
    // The damaged part is removed; the unsigned parallel call beside it stays.
    expect(sent.contents[0]!.parts).toHaveLength(1)
    expect(sent.contents[0]!.parts[0]).toEqual({ functionCall: { name: 'validate', args: {} } })
  })

  it('leaves a whole signature on a function call exactly where it was', () => {
    const ledger = signatureLedger()
    ledger.fragment('call-1', 'c2lnbmF0dXJlLVQx')
    const body = JSON.stringify({
      contents: [
        {
          role: 'model',
          parts: [
            {
              functionCall: { name: 'validate', args: {} },
              thoughtSignature: 'c2lnbmF0dXJlLVQx'
            }
          ]
        }
      ]
    })
    const looked = withoutTruncatedThinking('gemini', body, ledger, () => null)
    expect(looked.truncated).toBe('')
    expect(looked.body).toBe(body)
  })

  it('leaves a whole Gemini signature exactly where it was', () => {
    const ledger = signatureLedger()
    ledger.fragment('0', 'c2lnbmF0dXJlLVQx')
    const body = JSON.stringify({
      contents: [
        {
          role: 'model',
          parts: [{ text: 'x', thought: true, thoughtSignature: 'c2lnbmF0dXJlLVQx' }]
        }
      ]
    })
    const looked = withoutTruncatedThinking('gemini', body, ledger, () => null)
    expect(looked.truncated).toBe('')
    expect(looked.body).toBe(body)
  })

  it('puts back what the slot asks for now, one level down', () => {
    const ledger = signatureLedger()
    ledger.fragment('0', 'abcdef')
    const body = JSON.stringify({
      contents: [
        { role: 'model', parts: [{ text: 'x', thought: true, thoughtSignature: 'abc' }] }
      ],
      generationConfig: { thinkingConfig: { thinkingBudget: 8192 } }
    })
    const rebuilt = withoutTruncatedThinking('gemini', body, ledger, () => ({
      thinkingConfig: { thinkingBudget: 0 }
    }))
    const sent = JSON.parse(rebuilt.body) as { generationConfig: Record<string, unknown> }
    expect(sent.generationConfig).toEqual({ thinkingConfig: { thinkingBudget: 0 } })
  })
})

describe('a schema refusal is one wire’s business, on this engine too', () => {
  const TOOLS_WITH_PROPERTIES: McpTool[] = [
    {
      name: 'validate',
      description: 'check a document',
      inputSchema: { type: 'object', properties: { document: { type: 'string' } } }
    }
  ]

  const failure = async (
    family: 'openai-compatible' | 'anthropic' | 'gemini',
    message: string
  ): Promise<string> => {
    const call: ModelCall = async () =>
      new Response(JSON.stringify({ error: { message, code: 400 } }), {
        status: 400,
        headers: { 'content-type': 'application/json' }
      })
    const events = await drain(
      vercel.start(
        session(call, {
          tools: TOOLS_WITH_PROPERTIES,
          model: { family, model: 'a-model', call },
          thinking: normalize('off', family)
        })
      )
    )
    const errors = events.filter(
      (event): event is Extract<AssistantEvent, { type: 'error' }> => event.type === 'error'
    )
    expect(errors).toHaveLength(1)
    return errors[0]!.message
  }

  it.each([
    ['openai-compatible' as const, 'Unsupported response type: json_object'],
    ['anthropic' as const, 'properties is not permitted here']
  ])('classifies a %s 400 without exposing provider prose', async (family, message) => {
    const said = await failure(family, message)
    expect(said).not.toContain(message)
    expect(said).toContain('request was rejected')
    expect(said).not.toContain('removal list')
    expect(said).not.toContain('OpenAPI')
  })

  it('classifies the same refusal on the wire that has a schema subset', async () => {
    const said = await failure('gemini', 'Unknown name "properties" at parameters')
    expect(said).toContain('"properties"')
    expect(said).toContain('closed')
  })
})

describe('host tools: the desk’s own, executed on the page', () => {
  const searchTool = (execute: (args: Record<string, unknown>, signal: AbortSignal) => Promise<McpToolResult>) => ({
    name: 'search_sources',
    description: 'search',
    inputSchema: { type: 'object', properties: { query: { type: 'string' } }, required: ['query'] },
    execute
  })

  it('runs a host tool through its own function and reports the two events, never the gate', async () => {
    const { call, seen } = scriptedCall([
      turn({ tool: { name: 'search_sources', args: { query: 'federal skilled worker' } } }),
      turn({ text: PROPOSAL_TEXT })
    ])
    const executed: unknown[] = []
    let gated = 0
    const events = await drain(
      vercel.start(
        session(
          call,
          {
            hostTools: [
              searchTool(async (args) => {
                executed.push(args)
                return { content: [{ type: 'text', text: 'src-1: a hit' }], structuredContent: { sourceId: 'src-1' } }
              })
            ]
          },
          async () => {
            gated += 1
            return { content: [] }
          }
        )
      )
    )
    expect(executed).toEqual([{ query: 'federal skilled worker' }])
    expect(gated).toBe(0)
    expect(events.map((event) => event.type)).toEqual(['tool_call', 'tool_result', 'message', 'proposal', 'end'])
    expect(events[0]).toEqual({ type: 'tool_call', name: 'search_sources', args: { query: 'federal skilled worker' } })
    expect(events[1]).toMatchObject({ type: 'tool_result', name: 'search_sources', isError: false, text: 'src-1: a hit' })
    // The model was offered the host tool beside the runtime's, under the desk's schema.
    const offered = (seen[0]!.body.tools as { function: { name: string } }[]).map((tool) => tool.function.name)
    expect(offered).toEqual(['search_sources', 'list_context', 'read_context', 'read_window', 'search', 'validate', 'experimental_evaluate'])
  })

  it('reports a host tool that threw as a refused result, and goes on', async () => {
    const { call } = scriptedCall([
      turn({ tool: { name: 'search_sources', args: { query: 'x' } } }),
      turn({ text: PROPOSAL_TEXT })
    ])
    const events = await drain(
      vercel.start(
        session(call, {
          hostTools: [
            searchTool(async () => {
              throw new Error('the budget of 8 searches is spent')
            })
          ]
        })
      )
    )
    expect(events[1]).toMatchObject({ type: 'tool_result', name: 'search_sources', isError: true })
    expect(events[1]).toMatchObject({structured:{failure:{code:'tool-failed',source:'tool'}}})
    expect((events[1] as {text:string}).text).not.toContain('the budget of 8 searches')
    expect(events.at(-1)).toEqual({ type: 'end' })
  })

  it('stops a host tool with the run, and delivers nothing after', async () => {
    // The tool_call event is delivered before the tool runs, and the consumer
    // aborts in that gap: `withAbort` never starts work for a closed run, so
    // the tool is not called at all, no result is reported, and the run ends
    // without a proposal.
    const controller = new AbortController()
    const { call } = scriptedCall([
      turn({ tool: { name: 'search_sources', args: { query: 'x' } } }),
      turn({ text: PROPOSAL_TEXT })
    ])
    let started = 0
    const iterator = vercel
      .start(
        session(call, {
          signal: controller.signal,
          hostTools: [
            searchTool(async () => {
              started += 1
              return { content: [{ type: 'text', text: 'late' }] }
            })
          ]
        })
      )
      [Symbol.asyncIterator]()
    const first = await iterator.next()
    expect(first.value).toMatchObject({ type: 'tool_call', name: 'search_sources' })
    controller.abort()
    const rest: AssistantEvent[] = []
    const drained = await within(
      3000,
      (async () => {
        for (;;) {
          const step = await iterator.next()
          if (step.done === true) break
          rest.push(step.value)
          if (rest.length > 8) throw new Error('the run kept delivering after the abort')
        }
      })()
    )
    expect(drained).toBe('settled')
    expect(started).toBe(0)
    expect(rest.filter((event) => event.type === 'proposal' || event.type === 'tool_result')).toEqual([])
  })

  it('refuses a host tool named like a runtime tool, before the model is asked', async () => {
    const { call, seen } = scriptedCall([turn({ text: PROPOSAL_TEXT })])
    const events = await drain(
      vercel.start(session(call, { hostTools: [searchTool(async () => ({ content: [] })), { ...searchTool(async () => ({ content: [] })), name: 'validate' }] }))
    )
    expect(seen).toHaveLength(0)
    expect(events.map((event) => event.type)).toEqual(['error', 'end'])
    expect((events[0] as { message: string }).message).toContain('named like a tool the runtime serves')
  })

  it('offers only shared context host tools where the session hands none', async () => {
    const { call, seen } = scriptedCall([turn({ text: PROPOSAL_TEXT })])
    await drain(vercel.start(session(call)))
    const offered = (seen[0]!.body.tools as { function: { name: string } }[]).map((tool) => tool.function.name)
    expect(offered).toEqual(['list_context', 'read_context', 'read_window', 'search', 'validate', 'experimental_evaluate'])
  })
})


describe('opt-in conversational authoring', () => {
  it('allows a clarification without fabricating a proposal or running a critic', async () => {
    const { call, seen } = scriptedCall([turn({ text: 'Which outcomes should this decision allow?' })])
    const events = await drain(vercel.start(session(call, { allowConversation: true })))
    expect(events).toEqual([{ type: 'message', text: 'Which outcomes should this decision allow?' }, { type: 'end' }])
    expect(seen).toHaveLength(1)
  })
  it('retains explicit proposal extraction and refuses ambiguous proposals', async () => {
    const valid = scriptedCall([turn({ text: PROPOSAL_TEXT })])
    const events = await drain(vercel.start(session(valid.call, { allowConversation: true })))
    expect(events.find(event => event.type === 'proposal')).toMatchObject({ document: { id: 'p' } })
    const ambiguous = scriptedCall([turn({ text: PROPOSAL_TEXT + PROPOSAL_TEXT })])
    const refused = await drain(vercel.start(session(ambiguous.call, { allowConversation: true })))
    // The reformat request repeats the ambiguity, so the answer is kept whole
    // and the failure follows it.
    expect(refused.map(event => event.type)).toEqual(['response_recovery', 'message', 'error', 'end'])
    expect(refused.find(event => event.type === 'error')).toMatchObject({ failure: { code: 'invalid-response' } })
  })
})

describe('interactive conversation presentation', () => {
  it('streams answer prose and preserves an ordinary fenced code example as a message', async () => {
    const answer = 'Here is an example:\n\n```json\n{"case": "example"}\n```'
    const scripted = scriptedCall([turn({ text: answer })])
    const events = await drain(vercel.start(session(scripted.call, { allowConversation: true, interactive: true })))
    expect(events.some(event => event.type === 'message_progress' && event.text.startsWith('Here is an example:'))).toBe(true)
    expect(events.filter(event => event.type === 'message')).toEqual([{ type: 'message', text: answer }])
    expect(events.some(event => event.type === 'proposal' || event.type === 'error')).toBe(false)
    expect(scripted.seen).toHaveLength(1)
  })

  it('does not expose a partial proposal as message text', async () => {
    const scripted = scriptedCall([turn({ text: 'Here is the draft.\n\n' + PROPOSAL_TEXT })])
    const events = await drain(vercel.start(session(scripted.call, { allowConversation: true, interactive: true, adversarialReview: false })))
    expect(events.filter(event => event.type === 'message_progress').every(event => !event.text.includes('"proposal"'))).toBe(true)
    expect(events.some(event => event.type === 'proposal')).toBe(true)
  })
})
it('correlates repeated tool calls in an interactive session', async () => {
  const scripted = scriptedCall([turn({ tool: { name: 'validate', args: { pack: 'a' } } }), turn({ tool: { name: 'validate', args: { pack: 'b' } } }), turn({ text: 'Checked.' })])
  const events = await drain(vercel.start(session(scripted.call, { interactive: true, allowConversation: true })))
  const calls = events.filter(event => event.type === 'tool_call')
  const results = events.filter(event => event.type === 'tool_result')
  expect(calls).toHaveLength(2)
  expect(calls[0]!.callId).toBeTruthy()
  expect(calls[0]!.callId).not.toBe(calls[1]!.callId)
  expect(results.map(event => event.callId)).toEqual(calls.map(event => event.callId))
})
it('does not let an empty progress reset consume the thinking fallback', async () => {
  let calls = 0
  const call: ModelCall = async () => ++calls === 1
    ? new Response(JSON.stringify({ error: { message: 'Unsupported parameter: reasoning_effort' } }), { status: 400, headers: { 'content-type': 'application/json' } })
    : new Response(turn({ text: 'Hello.' }), { headers: { 'content-type': 'text/event-stream' } })
  const events = await drain(vercel.start(session(call, { interactive: true, allowConversation: true, thinking: normalize('on', 'openai-compatible') })))
  expect(calls).toBe(2)
  expect(events.some(event => event.type === 'message' && event.text === 'Hello.')).toBe(true)
})
it('separates adversarial review from requested thinking effort', async () => {
  const plain = scriptedCall([turn({ text: PROPOSAL_TEXT })])
  const events = await drain(vercel.start(session(plain.call, { thinking: normalize('on', 'openai-compatible'), adversarialReview: false })))
  expect(plain.seen).toHaveLength(1)
  expect(events.some(event => event.type === 'critique')).toBe(false)
  const reviewed = scriptedCall([turn({ text: PROPOSAL_TEXT }), turn({ text: 'Reviewed.' })])
  const checked = await drain(vercel.start(session(reviewed.call, { thinking: normalize('off', 'openai-compatible'), adversarialReview: true })))
  expect(reviewed.seen).toHaveLength(2)
  expect(checked.some(event => event.type === 'critique')).toBe(true)
})

describe('response language at the model boundary', () => {
  it.each(LANGUAGES)('passes $id to both author and adversarial reviewer while keeping protocol values', async locale => {
    const requests: string[] = []
    const call: ModelCall = async (_suffix, request) => {
      requests.push(request.body)
      return new Response(turn({ text: request.body.includes(REFUTATION_MARKER) ? 'Review complete.' : PROPOSAL_TEXT }),
        { status: 200, headers: { 'content-type': 'text/event-stream' } })
    }
    const events = await drain(vercel.start(session(call, { replyLanguage: locale.id, thinking: normalize('on', 'openai-compatible'), adversarialReview: true })))
    expect(requests.length).toBe(2)
    for (const body of requests) {
      expect(body).toContain(locale.instruction)
      expect(body).toContain('Never translate tool names, JSON/schema keys')
      expect(body).toContain('unless the user explicitly requests another language')
    }
    const proposal = events.find(event => event.type === 'proposal')
    expect(proposal?.document).toEqual({ id: 'p' })
  })
})

it('emits a draft artifact when a conversational reply includes indented probe examples', async () => {
  const text = 'Checks:\n```text\nValid\n```\n1. Probe\n     ```json\n     {"status":"evaluated"}\n     ```\n' + PROPOSAL_TEXT
  const scripted = scriptedCall([turn({ text })])
  const events = await drain(vercel.start(session(scripted.call, { allowConversation: true, interactive: true, adversarialReview: false })))
  expect(events.filter(event => event.type === 'proposal')).toEqual([{ type: 'proposal', document: { id: 'p' }, unknowns: ['who signs'] }])
  expect(events.some(event => event.type === 'error')).toBe(false)
  expect(events.find(event => event.type === 'message')?.text).toContain('"status":"evaluated"')
  expect(events.find(event => event.type === 'message')?.text).not.toContain('"proposal"')
})

it('graph authoring uses its proposal envelope and host-tool instructions', async () => {
  const { call, seen } = scriptedCall([turn({ text: PROPOSAL_TEXT })])
  const events = await drain(vercel.start(session(call, { purpose: 'graph', adversarialReview: false })))
  expect(JSON.stringify(seen[0])).toContain('graph authoring assistant')
  expect(JSON.stringify(seen[0])).toContain('graph_validate and graph_explain')
  expect(events.some(event => event.type === 'proposal')).toBe(true)
})

it.each(['openai-compatible','anthropic','gemini'] as const)('pauses %s at a question without another model request or a fake answer',async family=>{
 const {questionTool}=await import('../../questions')
 const {interviewQuestion}=await import('../../testing/question')
 const wire=family==='anthropic'?anthropicTurn:family==='gemini'?geminiTurn:turn
 const scripted=scriptedCall([wire({text:'Here are the tradeoffs.',tool:{name:'ask_user',args:interviewQuestion}})])
 const events=await drain(vercel.start(session(scripted.call,{allowConversation:true,interactive:true,hostTools:[questionTool],model:{family,model:'fixture-model',call:scripted.call},thinking:normalize('off',family)})))
 expect(scripted.seen).toHaveLength(1)
 const questionAt=events.findIndex(e=>e.type==='user_question')
 expect(events.slice(0,questionAt).some(e=>e.type==='message_progress'&&e.text==='Here are the tradeoffs.')).toBe(true)
 expect(events.filter(e=>e.type==='user_question'),JSON.stringify(events)).toEqual([{type:'user_question',id:expect.any(String),question:interviewQuestion}])
 expect(events.filter(e=>e.type==='error'||e.type==='proposal'||e.type==='tool_result')).toEqual([])
 expect(events.filter(e=>e.type==='end')).toHaveLength(1)
})

it('does not dispatch later host work in the same model response after a question', async () => {
  const { questionTool } = await import('../../questions')
  const { interviewQuestion } = await import('../../testing/question')
  const execute = vi.fn(async () => ({content: []}))
  const frame = (delta: unknown, finish_reason: string | null) => `data: ${JSON.stringify({id:'c',object:'chat.completion.chunk',model:'m',choices:[{index:0,delta,finish_reason}]})}\n\n`
  const tools = [{name:'ask_user',args:interviewQuestion},{name:'later_work',args:{}}]
  const wire = frame({tool_calls:tools.map((tool,index)=>({index,id:`call-${index}`,type:'function',function:{name:tool.name,arguments:JSON.stringify(tool.args)}}))},null) + frame({},'tool_calls') + 'data: [DONE]\n\n'
  const scripted = scriptedCall([wire])
  const events = await drain(vercel.start(session(scripted.call,{interactive:true,allowConversation:true,hostTools:[questionTool,{name:'later_work',description:'Later work',inputSchema:{type:'object'},execute}]})))
  expect(execute).not.toHaveBeenCalled()
  expect(events.filter(e => e.type === 'user_question')).toHaveLength(1)
  expect(scripted.seen).toHaveLength(1)
})

it.each(['openai-compatible','anthropic','gemini'] as const)('corrects a malformed question through the %s tool result before pausing',async family=>{
 const {questionTool}=await import('../../questions')
 const {interviewQuestion}=await import('../../testing/question')
 const wire=family==='anthropic'?anthropicTurn:family==='gemini'?geminiTurn:turn
 const bad={...interviewQuestion,options:[interviewQuestion.options[0],interviewQuestion.options[0]]}
 const scripted=scriptedCall([wire({tool:{name:'ask_user',args:bad}}),wire({tool:{name:'ask_user',args:interviewQuestion}})])
 const events=await drain(vercel.start(session(scripted.call,{allowConversation:true,interactive:true,hostTools:[questionTool],model:{family,model:'fixture-model',call:scripted.call},thinking:normalize('off',family)})))
 expect(scripted.seen).toHaveLength(2)
 expect(events.find(e=>e.type==='tool_result')).toMatchObject({name:'ask_user',isError:true,structured:{questionFailure:'options'}})
 expect(events.filter(e=>e.type==='user_question')).toHaveLength(1)
 expect(events.some(e=>e.type==='error')).toBe(false)
})

it.each(['openai-compatible','anthropic','gemini'] as const)('opens a %s question with omitted optional presentation fields',async family=>{
 const {questionTool}=await import('../../questions')
 const {interviewQuestion}=await import('../../testing/question')
 const wire=family==='anthropic'?anthropicTurn:family==='gemini'?geminiTurn:turn
 const {recommendation:_reason,recommendedOptionId:_id,...base}=interviewQuestion
 const question={...base,options:base.options.map(({id,label})=>({id,label}))}
 const scripted=scriptedCall([wire({tool:{name:'ask_user',args:question}})])
 const events=await drain(vercel.start(session(scripted.call,{allowConversation:true,interactive:true,hostTools:[questionTool],model:{family,model:'fixture-model',call:scripted.call},thinking:normalize('off',family)})))
 expect(events.filter(e=>e.type==='user_question')).toHaveLength(1)
 expect(events.find(e=>e.type==='user_question')).toMatchObject({question:{recommendedOptionId:null,recommendation:null,options:base.options.map(({id,label})=>({id,label,description:''}))}})
 expect(events.some(e=>e.type==='tool_result'||e.type==='error')).toBe(false)
 expect(scripted.seen).toHaveLength(1)
})

it.each(['openai-compatible','anthropic','gemini'] as const)('bounds a long UTF-8 prompt on the actual %s wire and keeps the latest request',async family=>{
 const wire=family==='anthropic'?anthropicTurn:family==='gemini'?geminiTurn:turn
 const scripted=scriptedCall([wire({text:'Ready'})])
 const events=await drain(vercel.start(session(scripted.call,{prompt:'Original task\n'+('Earlier material 😀 " '.repeat(24000))+'\nLATEST REQUEST: map current dialect',allowConversation:true,interactive:true,adversarialReview:false,model:{family,model:'fixture-model',call:scripted.call},thinking:normalize('off',family)})))
 expect(events.filter(e=>e.type==='error')).toEqual([])
 expect(scripted.seen).toHaveLength(1)
 const body=JSON.stringify(scripted.seen[0]!.body)
 expect(new TextEncoder().encode(body).length).toBeLessThan(180000)
 expect(body).toContain('LATEST REQUEST: map current dialect')
 expect(body).toContain('read_context')
 expect(body).toContain('Complete current request')
})


// Execute real SDK tool turns, rather than answering the first projected request.
// Previously all 20 turns could read context and leave result.text empty.
it.each(['openai-compatible','anthropic','gemini'] as const)('reserves the final %s request for synthesis after repeated retained-context reads',async family=>{
 const {MAX_TURNS}=await import('../contract')
 const wire=family==='anthropic'?anthropicTurn:family==='gemini'?geminiTurn:turn
 const seen:Record<string,unknown>[]=[]
 const message='Why does the no-routing case return unresolved?'
 const call:ModelCall=async(_suffix,request)=>{
  const body=JSON.parse(request.body);seen.push(body)
  const json=JSON.stringify(body)
  expect(json).toContain(message)
  const id=json.match(/[0-9a-f-]{36}:current-request:[0-9a-f]{16}/)?.[0]
  expect(id).toBeDefined()
  const final=seen.length===MAX_TURNS
  if(final){
   expect(json).toContain('This is the final request within this turn budget')
   expect(body.tools??[]).toEqual([])
  }
  return new Response(wire(final?{text:'The missing-size exception precedes the no-routing rule. No changes were saved.'}:{tool:{name:'read_context',args:{id,path:'/input',offset:(seen.length-1)*128,limit:256}}}),{status:200,headers:{'content-type':'text/event-stream'}})
 }
 const events=await drain(vercel.start(session(call,{prompt:'Earlier messages '.repeat(20000),context:{currentRequest:{message,input:'Facts and observed results. '.repeat(1000)}},allowConversation:true,interactive:true,adversarialReview:false,model:{family,model:'fixture',call},thinking:normalize('off',family)})))
 expect(seen).toHaveLength(MAX_TURNS)
 expect(events.filter(e=>e.type==='tool_result')).toHaveLength(MAX_TURNS-1)
 expect(events.filter(e=>e.type==='tool_result').every(e=>!e.isError)).toBe(true)
 expect(events.filter(e=>e.type==='message')).toEqual([{type:'message',text:'The missing-size exception precedes the no-routing rule. No changes were saved.'}])
 expect(events.filter(e=>e.type==='error')).toEqual([])
 expect(events.filter(e=>e.type==='end')).toHaveLength(1)
})
it.each(['empty','tool'] as const)('reports exhaustion accurately and refuses final-step work when the provider returns %s',async response=>{
 const {MAX_TURNS}=await import('../contract')
 let calls=0
 const execute=vi.fn(async()=>({content:[]}))
 const call:ModelCall=async(_suffix,request)=>{
  calls++
  const id=request.body.match(/[0-9a-f-]{36}:current-request:[0-9a-f]{16}/)?.[0]
  const final=calls===MAX_TURNS
  const step=final?(response==='empty'?{text:''}:{tool:{name:'propose_change',args:{}}}):{tool:{name:'read_context',args:{id,path:'/message',limit:128+calls}}}
  return new Response(geminiTurn(step),{status:200,headers:{'content-type':'text/event-stream'}})
 }
 const events=await drain(vercel.start(session(call,{prompt:'history '.repeat(40000),context:{currentRequest:{message:'Explain the mismatch',input:'Omitted size facts'}},hostTools:[{name:'propose_change',description:'Propose change',effect:'proposal',inputSchema:{type:'object'},execute}],allowConversation:true,interactive:true,adversarialReview:false,model:{family:'gemini',model:'fixture',call},thinking:normalize('off','gemini')})))
 expect(calls).toBe(MAX_TURNS);expect(execute).not.toHaveBeenCalled()
 expect(events.filter(e=>e.type==='message'||e.type==='proposal')).toEqual([])
 expect(events.filter(e=>e.type==='error')).toEqual([])
 expect(events.find(e=>e.type==='budget_paused')).toMatchObject({checkpoint:{status:'paused',used:20,limit:20}})
 expect(events.filter(e=>e.type==='end')).toHaveLength(1)
})

it.each(['openai-compatible','anthropic','gemini'] as const)('resumes %s after budget approval with exact tool results and task progress',async family=>{
 const {extendExecution,readExecution}=await import('../../execution/state')
 const wire=family==='anthropic'?anthropicTurn:family==='gemini'?geminiTurn:turn
 let calls=0
 const executionInput={message:'Fix the exception, test it and propose the change.',input:'Current pack revision 4; keep expectations unchanged.'}
 const call:ModelCall=async(_suffix,request)=>{
  calls++;const body=JSON.parse(request.body),text=JSON.stringify(body).replaceAll('\\"','"')
  const step=calls===1?{tool:{name:'update_plan',args:{explanation:'Inspect then verify the proposed fix',plan:[{step:'Inspect exception',status:'completed'},{step:'Verify fix',status:'in_progress'}]}}}
   :calls<20?{tool:{name:'get_schema',args:{section:calls}}}
   :calls===20?{text:''}
   :{text:'The saved check result supports the fix. The proposed change still needs review.'}
  if(calls===21){expect(text).toContain('Verify fix');expect(text).toContain('retainedOperations');expect(text).toContain('"used":20');expect(text).toContain('"limit":30')}
  return new Response(wire(step),{status:200,headers:{'content-type':'text/event-stream'}})
 }
 const callTool=vi.fn(async()=>({content:[{type:'text',text:'EXACT CONTRACT: preserve decimals as strings'}]}))
 const input=session(call,{prompt:executionInput.input,context:{currentRequest:executionInput},tools:[{name:'get_schema',inputSchema:{type:'object',properties:{}}}],callTool,allowConversation:true,interactive:true,adversarialReview:false,model:{family,model:'fixture',call},thinking:normalize('off',family)})
 const first=await drain(vercel.start(input)),pause=first.find(e=>e.type==='budget_paused')
 expect(pause?.type).toBe('budget_paused');if(pause?.type!=='budget_paused')return
 expect(callTool).toHaveBeenCalledTimes(18)
 const saved=readExecution(JSON.parse(JSON.stringify(pause.checkpoint)))!
 expect(saved.operations.some(op=>JSON.stringify(op.result).includes('EXACT CONTRACT'))).toBe(true)
 const second=await drain(vercel.start({...input,context:{...input.context,execution:extendExecution(saved)}}))
 expect(calls).toBe(21);expect(callTool).toHaveBeenCalledTimes(18)
 expect(second.filter(e=>e.type==='error')).toEqual([])
 expect(second.find(e=>e.type==='message')).toMatchObject({text:expect.stringContaining('proposed change still needs review')})
 expect(second.filter(e=>e.type==='execution_checkpoint').at(-1)).toMatchObject({checkpoint:{status:'complete',used:21,limit:30,grants:[{from:20,to:30}]}})
})

it('honors a model-requested budget pause without spending the reserved final request',async()=>{
 let calls=0
 const call:ModelCall=async()=>{
  calls++
  return new Response(turn({tool:calls===19?{name:'request_budget_increase',args:{reason:'Boundary checks remain.',nextStep:'Verify the two boundaries.'}}:{name:'get_schema',args:{section:calls}}}),{status:200,headers:{'content-type':'text/event-stream'}})
 }
 const input=session(call,{allowConversation:true,interactive:true,adversarialReview:false,context:{currentRequest:{message:'Verify the repair',input:'Current pack'}},tools:[{name:'get_schema',inputSchema:{type:'object',properties:{}}}],callTool:async()=>({content:[{type:'text',text:'Contract'}]})})
 const events=await drain(vercel.start(input))
 expect(calls).toBe(19)
 expect(events.find(e=>e.type==='budget_paused')).toMatchObject({checkpoint:{used:19,limit:20,reason:'Boundary checks remain. Next: Verify the two boundaries.'}})
 expect(events.filter(e=>e.type==='end')).toHaveLength(1)
 expect(events.filter(e=>e.type==='error')).toEqual([])
})

describe('nothing finished is lost (agent#6)', () => {
  const stream = (text: string) => new Response(text, { status: 200, headers: { 'content-type': 'text/event-stream' } })
  const fence = (body: string, label = 'json') => '```' + label + '\n' + body + '\n```'
  const envelope = JSON.stringify({ proposal: { kind: 'create', document: { id: 'p' }, unknowns: [] } })

  it.each([
    ['two fences', 'Two drafts follow.\n\n' + fence(envelope) + '\n\n' + fence(envelope), 'carried 2'],
    ['none', 'I read the schema and the policy, and I am not sure which outcome applies.', 'carried 0'],
    ['a YAML fence', 'Here is the pack.\n\n' + fence('proposal:\n  document:\n    id: p', 'yaml'), 'not marked json'],
    ['an unterminated fence', 'Here is the pack.\n\n```json\n' + envelope.slice(0, -2), 'not closed'],
    ['malformed JSON in a marked fence', 'Here is the pack.\n\n' + fence('{"proposal":{"kind":"create","document":{"id":"p",},"unknowns":[]}}'), 'not valid JSON']
  ])('%s: keeps the prose and the raw fence, and reports invalid-response after one reformat request', async (_shape, answer, detail) => {
    const { call, seen } = scriptedCall([turn({ text: answer })])
    const events = await drain(vercel.start(session(call)))
    expect(events.map((event) => event.type)).toEqual(['response_recovery', 'message', 'error', 'end'])
    // The whole answer, as written: prose and fence alike.
    expect(events[1]).toEqual({ type: 'message', text: answer.trim() })
    expect(events[2]).toMatchObject({ failure: { code: 'invalid-response', source: 'desk' } })
    expect((events[2] as { message: string }).message).toContain(detail)
    // One reformat request, tool-free, shown its own answer.
    expect(seen).toHaveLength(2)
    const last = (seen[1]!.body.messages as { role: string; content: unknown }[]).slice(-2)
    expect(last[0]).toMatchObject({ role: 'assistant' })
    expect(JSON.stringify(last[0]!.content)).toContain(answer.split('\n')[0]!)
    expect(JSON.stringify(last[1])).toContain('could not be read')
    expect(seen[1]!.body.tool_choice ?? 'none').toBe('none')
  })

  it('recovers the proposal on the reformat request and keeps the first answer’s prose', async () => {
    const broken = 'I validated the draft; the runtime reports it valid.\n\n' + fence('{"proposal":{"document":{"id":"p"},"unknowns":[],}}')
    const { call, seen } = scriptedCall([turn({ text: broken }), turn({ text: fence(JSON.stringify({ proposal: { document: { id: 'p' }, unknowns: ['who signs'] } })) })])
    const events = await drain(vercel.start(session(call)))
    expect(events.map((event) => event.type)).toEqual(['response_recovery', 'message', 'proposal', 'end'])
    expect(events[1]).toEqual({ type: 'message', text: 'I validated the draft; the runtime reports it valid.' })
    expect(events[2]).toMatchObject({ document: { id: 'p' }, unknowns: ['who signs'] })
    expect(seen).toHaveLength(2)
  })

  it.each([
    ['a provider error', () => new Response(JSON.stringify({ error: { type: 'overloaded_error', message: 'busy' } }), { status: 503, headers: { 'content-type': 'application/json' } }), 'unavailable'],
    ['a length cutoff', () => stream(turn({ text: 'The first check' }).replace('"finish_reason":"stop"', '"finish_reason":"length"')), 'output-limit']
  ])('delivers a valid proposal when the critic fails with %s, with a note instead of an error', async (_how, critic, code) => {
    let criticRequests = 0
    const call: ModelCall = async (_suffix, request) => {
      if (request.body.includes(REFUTATION_MARKER)) {
        criticRequests += 1
        return critic()
      }
      return stream(turn({ text: PROPOSAL_TEXT }))
    }
    const events = await drain(vercel.start(session(call, { thinking: normalize('on', 'openai-compatible'), adversarialReview: true })))
    expect(criticRequests).toBe(1)
    expect(events.filter((event) => event.type === 'error')).toEqual([])
    expect(events.some((event) => event.type === 'critique')).toBe(false)
    const proposal = events.find((event) => event.type === 'proposal')
    expect(proposal).toMatchObject({
      document: { id: 'p' },
      unknowns: ['who signs'],
      note: { kind: 'critic-unfinished', failure: { code } }
    })
    expect(proposal).not.toHaveProperty('critique')
    expect((proposal as { note: { message: string } }).note.message).toContain('The critic did not finish')
    expect(events.at(-1)).toEqual({ type: 'end' })
  })

  describe('a proposal through the propose_pack tool', () => {
    const offer = (execute = vi.fn(async (): Promise<McpToolResult> => ({ content: [] }))) => ({
      execute,
      tool: { name: PROPOSE_PACK_TOOL, effect: 'proposal' as const, description: 'Propose the pack for review.', inputSchema: PROPOSE_PACK_SCHEMA, execute }
    })

    it('is accepted as the proposal, with the document as an object, and the instructions say to use it', async () => {
      const { execute, tool } = offer()
      const { call, seen } = scriptedCall([
        turn({ tool: { name: PROPOSE_PACK_TOOL, args: { document: { id: 'p' }, unknowns: ['who signs'] } } }),
        turn({ text: 'Proposed for your review.' })
      ])
      const events = await drain(vercel.start(session(call, { hostTools: [tool] })))
      expect(events.map((event) => event.type)).toEqual(['tool_call', 'tool_result', 'message', 'proposal', 'end'])
      expect(events[1]).toMatchObject({ name: PROPOSE_PACK_TOOL, isError: false, text: PROPOSAL_RECEIVED })
      expect(events[3]).toEqual({ type: 'proposal', document: { id: 'p' }, unknowns: ['who signs'] })
      // The engine reads the call; the host's own function is never reached.
      expect(execute).not.toHaveBeenCalled()
      expect(JSON.stringify(seen[0]!.body)).toContain(`calling ${PROPOSE_PACK_TOOL}`)
      expect(JSON.stringify(seen[0]!.body.tools)).toContain(PROPOSE_PACK_TOOL)
    })

    it('needs no closing text, and tells the model to correct a document sent as a string', async () => {
      const { tool } = offer()
      const { call, seen } = scriptedCall([
        turn({ tool: { name: PROPOSE_PACK_TOOL, args: { document: JSON.stringify({ id: 'p' }) } } }),
        turn({ tool: { name: PROPOSE_PACK_TOOL, args: { document: { id: 'p' } } } }),
        turn({ text: '' })
      ])
      const events = await drain(vercel.start(session(call, { hostTools: [tool] })))
      expect(events.map((event) => event.type)).toEqual(['tool_call', 'tool_result', 'tool_call', 'tool_result', 'proposal', 'end'])
      expect(events[1]).toMatchObject({ isError: true, structured: { failure: { code: 'invalid-tool-input' } } })
      expect((events[1] as { text: string }).text).toContain('not as a JSON string')
      expect(events[4]).toEqual({ type: 'proposal', document: { id: 'p' }, unknowns: [] })
      expect(seen).toHaveLength(3)
    })

    it('reads the name and input schema the contract states', () => {
      const stated = toolOffer.$defs.proposePack.allOf[1]!.properties!
      expect(PROPOSE_PACK_TOOL).toBe(stated.name.const)
      expect(PROPOSE_PACK_SCHEMA).toEqual(stated.inputSchema.const)
    })

    it('says nothing about the tool where the host did not offer it', async () => {
      const { call, seen } = scriptedCall([turn({ text: PROPOSAL_TEXT })])
      await drain(vercel.start(session(call)))
      expect(JSON.stringify(seen[0]!.body)).not.toContain(PROPOSE_PACK_TOOL)
    })

    it.each([
      ['the endpoint answers 503', () => new Response(JSON.stringify({ error: { type: 'server_error', message: 'down' } }), { status: 503, headers: { 'content-type': 'application/json' } }), 'unavailable'],
      ['the endpoint answers 429 with Retry-After: 120', () => new Response(JSON.stringify({ error: { type: 'rate_limit_error', message: 'slow down' } }), { status: 429, headers: { 'content-type': 'application/json', 'retry-after': '120' } }), 'rate-limited']
    ])('delivers the proposal with a note when the summary request after the call fails: %s', async (_how, refusal, code) => {
      const { tool } = offer()
      let requests = 0
      const call: ModelCall = async () =>
        ++requests === 1 ? stream(turn({ tool: { name: PROPOSE_PACK_TOOL, args: { document: { id: 'p' }, unknowns: ['who signs'] } } })) : refusal()
      const events = await drain(vercel.start(session(call, { hostTools: [tool] })))
      expect(requests).toBe(2)
      expect(events.map((event) => event.type)).toEqual(['tool_call', 'tool_result', 'proposal', 'end'])
      expect(events[2]).toMatchObject({ document: { id: 'p' }, unknowns: ['who signs'], note: { kind: 'turn-unfinished', failure: { code, source: 'model' } } })
      expect((events[2] as { note: { message: string } }).note.message).toContain('The turn stopped after this proposal was made')
    })

    it('refuses a kind other than create, as the fence does', async () => {
      const { tool } = offer()
      const { call } = scriptedCall([
        turn({ tool: { name: PROPOSE_PACK_TOOL, args: { kind: 'update', document: { id: 'p' } } } }),
        turn({ tool: { name: PROPOSE_PACK_TOOL, args: { kind: 'create', document: { id: 'p' } } } }),
        turn({ text: '' })
      ])
      const events = await drain(vercel.start(session(call, { hostTools: [tool] })))
      expect(events.map((event) => event.type)).toEqual(['tool_call', 'tool_result', 'tool_call', 'tool_result', 'proposal', 'end'])
      expect(events[1]).toMatchObject({ isError: true, structured: { failure: { code: 'invalid-tool-input' } } })
      expect((events[1] as { text: string }).text).toContain('Only a new pack can be proposed')
      expect(events[4]).toEqual({ type: 'proposal', document: { id: 'p' }, unknowns: [] })
    })

    it('refuses a tool proposal beside a fenced one as ambiguous, as two fences are', async () => {
      const { tool } = offer()
      const answer = 'Final.\n\n' + fence(JSON.stringify({ proposal: { document: { id: 'fence' }, unknowns: [] } }))
      const { call, seen } = scriptedCall([
        turn({ tool: { name: PROPOSE_PACK_TOOL, args: { document: { id: 'tool' } } } }),
        turn({ text: answer }),
        turn({ text: 'I cannot say which one.' })
      ])
      const events = await drain(vercel.start(session(call, { hostTools: [tool] })))
      expect(seen).toHaveLength(3)
      expect(events.map((event) => event.type)).toEqual(['tool_call', 'tool_result', 'response_recovery', 'message', 'error', 'end'])
      // Neither proposal is chosen silently: the answer is kept whole, the fence with it.
      expect(events[3]).toEqual({ type: 'message', text: answer })
      expect(events[4]).toMatchObject({ failure: { code: 'invalid-response' } })
      expect((events[4] as { message: string }).message).toContain('through propose_pack and another in a fenced JSON block')
    })
  })

  it.each([
    ['two fences', 'Two drafts follow.\n\n' + fence(envelope) + '\n\n' + fence(envelope), 'carried 2'],
    ['malformed JSON in a marked fence', 'Here is the pack.\n\n' + fence('{"proposal":{"kind":"create","document":{"id":"p",},"unknowns":[]}}'), 'not valid JSON']
  ])('in a conversation, %s: the first answer is kept whole when the reformat request comes back as prose', async (_shape, answer, detail) => {
    const { call, seen } = scriptedCall([turn({ text: answer }), turn({ text: 'Sorry, I cannot decide between them.' })])
    const events = await drain(vercel.start(session(call, { allowConversation: true })))
    expect(seen).toHaveLength(2)
    expect(events.map((event) => event.type)).toEqual(['response_recovery', 'message', 'message', 'error', 'end'])
    expect(events[1]).toEqual({ type: 'message', text: answer.trim() })
    expect(events[2]).toEqual({ type: 'message', text: 'Sorry, I cannot decide between them.' })
    expect(events[3]).toMatchObject({ failure: { code: 'invalid-response' } })
    expect((events[3] as { message: string }).message).toContain(detail)
  })
})

describe('the allowance and the retry (agent#2)', () => {
  const stream = (text: string) => new Response(text, { status: 200, headers: { 'content-type': 'text/event-stream' } })
  const refused = () => new Response(JSON.stringify({ error: { type: 'rate_limit_error', message: 'slow down' } }), { status: 429, headers: { 'content-type': 'application/json' } })
  const request = { message: 'Draft the pack.', input: 'The policy.' }
  /** A checkpoint for this request with `used` of its twenty requests spent. */
  async function spent(used: number): Promise<ExecutionCheckpoint> {
    for await (const event of vercel.start(session(async () => stream(turn({ text: PROPOSAL_TEXT })), { context: { currentRequest: request } }))) {
      if (event.type === 'execution_checkpoint') return { ...event.checkpoint, used }
    }
    throw new Error('no checkpoint')
  }
  /** A model that calls a tool while tools are offered, and proposes once they are closed. */
  const model = (first?: () => Response): { call: ModelCall; bodies: Record<string, unknown>[] } => {
    const bodies: Record<string, unknown>[] = []
    const call: ModelCall = async (_suffix, sent) => {
      const body = JSON.parse(sent.body) as Record<string, unknown>
      bodies.push(body)
      if (bodies.length === 1 && first) return first()
      const closed = body.tool_choice === 'none' || !Array.isArray(body.tools) || body.tools.length === 0
      return stream(closed ? turn({ text: PROPOSAL_TEXT }) : turn({ tool: { name: 'validate', args: { document: { id: `d${bodies.length}` } } } }))
    }
    return { call, bodies }
  }

  it('four requests remaining and one 429 retried: the run still ends with its proposal', async () => {
    const execution = await spent(16)
    const { call, bodies } = model(refused)
    const events = await drain(vercel.start(session(call, { context: { currentRequest: request, execution } })))
    expect(bodies).toHaveLength(4)
    expect(events.filter((event) => event.type === 'retry')).toEqual([{ type: 'retry', attempt: 1 }])
    expect(events.filter((event) => event.type === 'budget_paused' || event.type === 'error')).toEqual([])
    expect(events.find((event) => event.type === 'proposal')).toMatchObject({ document: { id: 'p' } })
    // The last request was the tool-free final answer.
    expect(bodies.at(-1)!.tool_choice ?? 'none').toBe('none')
  })

  it('one request remaining and a 429: no retry takes it, and the refusal is reported', async () => {
    const execution = await spent(19)
    const { call, bodies } = model(refused)
    const events = await drain(vercel.start(session(call, { context: { currentRequest: request, execution } })))
    expect(bodies).toHaveLength(1)
    expect(events.filter((event) => event.type === 'retry')).toEqual([])
    expect(events.find((event) => event.type === 'error')).toMatchObject({ failure: { code: 'rate-limited', outcome: 'refused' } })
  })

  it('starts each call at attempt 1, so a later row never carries an earlier call’s attempt', async () => {
    // A 429 retried, then a tier refusal on the retry, which starts the session
    // again: the request that then answers is a new call's first attempt.
    let requests = 0
    const call: ModelCall = async () => {
      requests += 1
      if (requests === 1) return refused()
      if (requests === 2) return new Response(JSON.stringify({ error: { message: 'Unsupported parameter: reasoning_effort' } }), { status: 400, headers: { 'content-type': 'application/json' } })
      return stream(turn({ text: PROPOSAL_TEXT }))
    }
    const events = await drain(vercel.start(session(call, { thinking: normalize('on', 'openai-compatible'), adversarialReview: false, context: { currentRequest: request } })))
    expect(requests).toBe(3)
    expect(events.some((event) => event.type === 'proposal')).toBe(true)
    const checkpoints = events.flatMap((event) => (event.type === 'execution_checkpoint' ? [event.checkpoint] : []))
    const completions = checkpoints.at(-1)!.completions ?? []
    expect(completions[0]).toMatchObject({ request: 1, attempt: 1, failure: { code: 'rate-limited' } })
    expect(completions.at(-1)).toEqual({ request: 3, phase: 'work', result: expect.objectContaining({ finishReason: 'stop' }) })
  })
})
