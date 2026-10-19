/**
 * The loop's bounds (agent#2, the harness side), measured on the recorded
 * scenario: the certification's scripted model behind a `fetch` stub, and its
 * scripted runtime on an in-memory transport behind the real tool gate.
 *
 * Each case changes one thing about that session:
 *
 * - the endpoint answers 429 with `Retry-After: 1`: retried once, and the run
 *   completes;
 * - the endpoint answers 503: not retried;
 * - the endpoint closes the connection after the first chunk: not retried;
 * - the turn's clock runs out: the run pauses as `budget_paused` does;
 * - a tool does not answer within its deadline: the model reads a `timeout`
 *   failure and the run continues.
 *
 * The two time limits are set per test through `contextPolicy`, the one place
 * a session's policy is built.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { bindModelCall, openAssistantConnection, runAssistantSession } from '../../session'
import { scriptedModel, type ScriptedModel } from '../../certification/scriptedModel'
import { scriptedRuntime, type ServerObservation } from '../../certification/scriptedServer'
import scenario from '../../certification/scenario.json'
import { normalize } from '../../thinking'
import { vercel } from './index'
import type { AssistantEvent, McpToolResult } from '../../engine'
import type { ExecutionCheckpoint } from '../../execution/state'

const limits = vi.hoisted(() => ({ value: {} as { turnMilliseconds?: number; toolCallMilliseconds?: number } }))
vi.mock('../../context/policy', async (importOriginal) => {
  const original = await importOriginal<typeof import('../../context/policy')>()
  return {
    ...original,
    contextPolicy: (...args: Parameters<typeof original.contextPolicy>) => ({ ...original.contextPolicy(...args), ...limits.value })
  }
})

const FAMILY = 'openai-compatible' as const
const DRAFT_V2 = scenario.documents.DRAFT_V2 as unknown

/** One request as it reached the endpoint: its number, from 1, and its body. */
interface Arrival {
  number: number
  body: { messages?: { role?: string; content?: unknown }[] }
  signal: AbortSignal | undefined
}

interface Session {
  events: AssistantEvent[]
  /** Every request that reached the endpoint, the ones answered here included. */
  arrivals: Arrival[]
  /** The requests the scripted model itself answered. */
  model: ScriptedModel
  seen: ServerObservation[]
  elapsedMs: number
}

/**
 * The scenario, with the endpoint's answer to each request decided by
 * `endpoint`: it either answers itself or calls `scripted` for the scripted
 * model's answer. `callTool` may stand between the engine and the gated
 * runtime.
 */
async function runScenario(options: {
  endpoint?: (arrival: Arrival, scripted: () => Promise<Response>) => Promise<Response>
  callTool?: (name: string, args: Record<string, unknown>, runtime: (name: string, args: Record<string, unknown>) => Promise<McpToolResult>) => Promise<McpToolResult>
}): Promise<Session> {
  const model = scriptedModel({ api: FAMILY, answerAs: 'stream' })
  const arrivals: Arrival[] = []
  const endpoint = options.endpoint ?? ((_arrival, scripted) => scripted())
  vi.stubGlobal('fetch', async (input: unknown, init?: RequestInit) => {
    const arrival: Arrival = {
      number: arrivals.length + 1,
      body: JSON.parse(String(init?.body ?? '{}')),
      signal: init?.signal ?? undefined
    }
    arrivals.push(arrival)
    return endpoint(arrival, () => model.fetch(input as RequestInfo, init))
  })
  const runtime = await scriptedRuntime()
  const events: AssistantEvent[] = []
  const connection = openAssistantConnection({
    allowed: scenario.scenarioTools,
    onEvent: (event) => events.push(event),
    transport: runtime.transport
  })
  const started = Date.now()
  try {
    const ready = await connection.ready
    const callTool = options.callTool
    await runAssistantSession(
      vercel,
      {
        prompt: scenario.policy,
        testPrompt: 'the runtime’s test_pack guidance',
        tools: ready.tools,
        callTool: callTool === undefined ? ready.callTool : (name, args) => callTool(name, args, ready.callTool),
        hostTools: [],
        model: { family: FAMILY, model: 'scripted-model', call: bindModelCall(FAMILY) },
        thinking: normalize('off', FAMILY),
        signal: new AbortController().signal,
        // A current request makes the run keep its checkpoint, so the
        // completion record is on the stream.
        context: { currentRequest: { message: 'Draft the expense reimbursement pack.', input: scenario.policy } }
      },
      (event) => events.push(event)
    )
  } finally {
    await connection.close()
    await runtime.close()
  }
  return { events, arrivals, model, seen: runtime.seen, elapsedMs: Date.now() - started }
}

const lastCheckpoint = (events: AssistantEvent[]): ExecutionCheckpoint => {
  const checkpoints = events.flatMap((event) =>
    event.type === 'execution_checkpoint' || event.type === 'budget_paused' ? [event.checkpoint as ExecutionCheckpoint] : []
  )
  expect(checkpoints.length).toBeGreaterThan(0)
  return checkpoints.at(-1)!
}

const ofType = <T extends AssistantEvent['type']>(events: AssistantEvent[], type: T) =>
  events.filter((event): event is Extract<AssistantEvent, { type: T }> => event.type === type)

beforeEach(() => {
  limits.value = {}
})
afterEach(() => {
  vi.unstubAllGlobals()
})

describe('model request retries', () => {
  it('the endpoint answers 429 with Retry-After 1: retried once, and the run completes', async () => {
    const { events, arrivals, model, elapsedMs } = await runScenario({
      endpoint: async (arrival, scripted) =>
        arrival.number === 1
          ? new Response(JSON.stringify({ error: { message: 'Rate limit reached', type: 'rate_limit_error', code: 'rate_limit_exceeded' } }), {
              status: 429,
              headers: { 'content-type': 'application/json', 'retry-after': '1' }
            })
          : scripted()
    })
    // The scenario's eight requests, and the one refused before them.
    expect(model.requests).toHaveLength(8)
    expect(arrivals).toHaveLength(9)
    expect(elapsedMs).toBeGreaterThanOrEqual(1_000)
    expect(ofType(events, 'retry')).toEqual([{ type: 'retry', attempt: 1 }])
    expect(ofType(events, 'error')).toEqual([])
    expect(ofType(events, 'proposal')).toHaveLength(1)
    expect(ofType(events, 'proposal')[0]!.document).toEqual(DRAFT_V2)
    expect(ofType(events, 'end')).toHaveLength(1)

    const completions = lastCheckpoint(events).completions ?? []
    const refused = completions.filter((row) => row.failure !== undefined)
    expect(refused).toHaveLength(1)
    expect(refused[0]).toMatchObject({
      request: 1,
      phase: 'work',
      attempt: 1,
      failure: { code: 'rate-limited', outcome: 'refused', status: 429, source: 'model' }
    })
    expect(refused[0]!.waitMs).toBeGreaterThanOrEqual(1_000)
    expect(refused[0]!.waitMs).toBeLessThanOrEqual(2_000)
    // The attempt that answered carries its number.
    expect(completions[1]).toMatchObject({ request: 2, phase: 'work', attempt: 2 })
    expect(completions[1]).not.toHaveProperty('failure')
  })

  it('the endpoint answers 503: not retried', async () => {
    const { events, arrivals, model } = await runScenario({
      endpoint: async () =>
        new Response(JSON.stringify({ error: { message: 'The service is unavailable', type: 'server_error', code: null } }), {
          status: 503,
          headers: { 'content-type': 'application/json' }
        })
    })
    expect(arrivals).toHaveLength(1)
    expect(model.requests).toHaveLength(0)
    expect(ofType(events, 'retry')).toEqual([])
    expect(ofType(events, 'proposal')).toEqual([])
    const errors = ofType(events, 'error')
    expect(errors).toHaveLength(1)
    expect(errors[0]!.failure).toMatchObject({ outcome: 'unknown', status: 503, source: 'model' })
    expect(ofType(events, 'end')).toHaveLength(1)
    expect((lastCheckpoint(events).completions ?? []).filter((row) => row.failure !== undefined)).toEqual([])
  })

  it('the endpoint closes the connection after the first chunk: not retried', async () => {
    const { events, arrivals, model } = await runScenario({
      endpoint: async (_arrival, scripted) => {
        // The scripted answer's first server-sent event, then the body ends
        // with the error a dropped connection gives.
        const first = (await (await scripted()).text()).split('\n\n')[0] + '\n\n'
        const body = new ReadableStream<Uint8Array>({
          start(controller) {
            controller.enqueue(new TextEncoder().encode(first))
          },
          pull(controller) {
            controller.error(new TypeError('terminated'))
          }
        })
        return new Response(body, { status: 200, headers: { 'content-type': 'text/event-stream' } })
      }
    })
    expect(arrivals).toHaveLength(1)
    expect(model.requests).toHaveLength(1)
    expect(ofType(events, 'retry')).toEqual([])
    expect(ofType(events, 'proposal')).toEqual([])
    const errors = ofType(events, 'error')
    expect(errors).toHaveLength(1)
    expect(errors[0]!.failure?.outcome).toBe('unknown')
    expect(ofType(events, 'end')).toHaveLength(1)
    expect((lastCheckpoint(events).completions ?? []).filter((row) => row.failure !== undefined)).toEqual([])
  })
})

describe('the turn clock', () => {
  it('the turn’s clock runs out: the run pauses as budget_paused, with its checkpoint and the reason', async () => {
    limits.value = { turnMilliseconds: 1_000 }
    const { events, arrivals, seen, elapsedMs } = await runScenario({
      // The third request is never answered; it ends only when its signal does.
      endpoint: (arrival, scripted) =>
        arrival.number < 3
          ? scripted()
          : new Promise<Response>((_resolve, reject) => {
              arrival.signal?.addEventListener('abort', () => reject(arrival.signal!.reason), { once: true })
            })
    })
    expect(arrivals).toHaveLength(3)
    expect(elapsedMs).toBeGreaterThanOrEqual(1_000)
    expect(ofType(events, 'error')).toEqual([])
    expect(ofType(events, 'proposal')).toEqual([])
    const paused = ofType(events, 'budget_paused')
    expect(paused).toHaveLength(1)
    const checkpoint = paused[0]!.checkpoint as ExecutionCheckpoint
    expect(checkpoint.status).toBe('paused')
    expect(checkpoint.reason).toContain('wall-clock limit of 1 second.')
    expect(checkpoint.reason).toContain('Completed work is kept')
    // The two tool calls that finished before the clock ran out are kept.
    expect(seen.map((call) => call.name)).toEqual(['get_schema', 'list_examples'])
    expect(checkpoint.operations.map((operation) => [operation.name, operation.status])).toEqual([
      ['get_schema', 'complete'],
      ['list_examples', 'complete']
    ])
    expect(ofType(events, 'end')).toHaveLength(1)
  })
})

describe('the tool-call deadline', () => {
  it('the tool does not answer within its deadline: the model reads a timeout failure, and the run continues', async () => {
    limits.value = { toolCallMilliseconds: 300 }
    const { events, arrivals, seen } = await runScenario({
      // The runtime answers get_schema, and the answer is held for a second,
      // past the 300 ms deadline.
      callTool: async (name, args, runtime) => {
        const answer = await runtime(name, args)
        if (name === 'get_schema') await new Promise((resolve) => setTimeout(resolve, 1_000))
        return answer
      }
    })
    const late = ofType(events, 'tool_result').find((result) => result.name === 'get_schema')
    expect(late).toMatchObject({ isError: true, structured: { failure: { code: 'timeout', source: 'tool', outcome: 'unknown' } } })
    expect(late!.text).toContain('did not answer within 300 milliseconds')
    // The model's next request carries the failure as the tool's result.
    const next = arrivals[1]!.body.messages ?? []
    expect(next.some((message) => message.role === 'tool' && JSON.stringify(message.content).includes('did not answer within'))).toBe(true)
    // The run continued through the rest of the scenario.
    expect(seen.map((call) => call.name)).toContain('experimental_evaluate')
    expect(ofType(events, 'error')).toEqual([])
    expect(ofType(events, 'budget_paused')).toEqual([])
    expect(ofType(events, 'proposal')).toHaveLength(1)
    expect(ofType(events, 'proposal')[0]!.document).toEqual(DRAFT_V2)
    expect(ofType(events, 'end')).toHaveLength(1)
  })
})
