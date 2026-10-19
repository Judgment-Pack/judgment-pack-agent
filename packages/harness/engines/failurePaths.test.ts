/**
 * The failure paths no other suite drives (agent#3, as shrunk): what each
 * engine reports when the connection drops before the first byte and after
 * it, when a stream stalls past the relay's idle bound, when the provider
 * refuses the context as too long, when the runtime's socket drops during a
 * tool call, and when the endpoint answers whole JSON where a stream was asked
 * for. The cases already covered elsewhere stay there: 429 with Retry-After
 * and request ids through the real SDK on three wires, errors after HTTP 200
 * and a Gemini mid-stream 503 (`failures.integration.test.ts`), the HTTP class
 * table (`failures.test.ts`), the retry rule (`vercel/bounds.test.ts`), and
 * whole-JSON success legs on every family (the certification suite).
 *
 * Every case runs one turn that has already completed a runtime call when the
 * failure comes, and asserts four things:
 *
 * - **the failure class** the run reports, and its outcome;
 * - **the recovery hint**, the catalogue's `recovery` for that class, which is
 *   what a host's response-recovery view reads;
 * - **that the checkpoint survives**: the run's last checkpoint keeps the
 *   completed call and is a valid one to resume from;
 * - **what the Retry action does**: a host offers Retry unless the recovery is
 *   `narrow` (and not before `retryAt`), and Retry starts the same request
 *   again from that checkpoint (`context.execution`, status `running`), as the
 *   Desk's `retryResponse` does. The completed call is kept, not repeated by
 *   the harness, and the request count carries on from where it stopped.
 *
 * The scripted endpoint is named for what it does; nothing here reaches a
 * provider or a live service.
 */
import { afterEach, describe, expect, it, vi } from 'vitest'
import { failureRecovery, retryReady, type AIFailure } from '../failures'
import { readExecution, type ExecutionCheckpoint } from '../execution/state'
import {
  codexTurn,
  ofType,
  runtimeAnswer,
  sseTurn,
  streamed,
  toolTexts,
  vercelTurn,
  whole,
  wholeTurn,
  type Bridge,
  type EndpointRequest,
  type RuntimeCall,
  type RuntimeControl,
  type TurnRecord
} from '../testing/scripted'
import type { AssistantEvent } from '../engine'

afterEach(() => {
  vi.unstubAllGlobals()
})

/** The one error a failed run reports. */
function failureOf(events: AssistantEvent[]): AIFailure {
  const errors = ofType(events, 'error')
  expect(errors).toHaveLength(1)
  expect(ofType(events, 'end')).toHaveLength(1)
  expect(events.at(-1)).toEqual({ type: 'end' })
  return errors[0]!.failure!
}

/** The run's last checkpoint, which a host saves and a Retry resumes from. */
function lastCheckpoint(events: AssistantEvent[]): ExecutionCheckpoint {
  const saved = events.flatMap((event) =>
    event.type === 'execution_checkpoint' || event.type === 'budget_paused' ? [event.checkpoint as ExecutionCheckpoint] : []
  )
  expect(saved.length).toBeGreaterThan(0)
  const last = saved.at(-1)!
  // A checkpoint the host can hand back: it reads as one.
  expect(readExecution(structuredClone(last))).toBeDefined()
  return last
}

/** Whether a host offers Retry for this failure, now: never for `narrow`, and not before `retryAt`. */
function retryOffered(failure: AIFailure, now = Date.now()): boolean {
  return failureRecovery(failure) !== 'narrow' && retryReady(failure, now)
}

/** Retry: the same request again, from the failed run's checkpoint. */
const resumeFrom = (checkpoint: ExecutionCheckpoint): ExecutionCheckpoint => ({ ...structuredClone(checkpoint), status: 'running' })

const operations = (checkpoint: ExecutionCheckpoint) => checkpoint.operations.map((operation) => [operation.name, operation.status])

/**
 * The healthy vercel turn: get_schema, then validate, then the answer. A case
 * replaces the endpoint's answer to the second request (or the runtime's
 * answer to validate) with its failure.
 */
const HEALTHY: Record<number, string> = {
  1: sseTurn({ tool: { name: 'get_schema', args: {} } }),
  2: sseTurn({ tool: { name: 'validate', args: { document: '{}' } } }),
  3: sseTurn({ text: 'The pack validates.' })
}
const healthy = (request: EndpointRequest): Response => streamed(HEALTHY[request.number] ?? HEALTHY[3]!)

/** The first server-sent event of a streamed answer, and then whatever `after` does to the body. */
function firstChunkThen(text: string, after: (controller: ReadableStreamDefaultController<Uint8Array>) => void): Response {
  const first = text.split('\n\n')[0] + '\n\n'
  const body = new ReadableStream<Uint8Array>({
    start(controller) {
      controller.enqueue(new TextEncoder().encode(first))
    },
    pull(controller) {
      after(controller)
    }
  })
  return new Response(body, { status: 200, headers: { 'content-type': 'text/event-stream' } })
}

/** The relay's idle bound, shortened: the Desk's relay cuts a stream two minutes after its last write. */
const IDLE_BOUND_MS = 300

async function vercelFailure(options: {
  answer?: (request: EndpointRequest) => Response | Promise<Response>
  handle?: (call: RuntimeCall, control: RuntimeControl) => unknown
}): Promise<TurnRecord & { requests: EndpointRequest[] }> {
  return vercelTurn({
    answer: options.answer ?? healthy,
    handle: options.handle ?? (() => runtimeAnswer({ status: 'valid' }))
  })
}

/** Retry on vercel: the same turn from the checkpoint, the endpoint healthy now. */
async function vercelRetry(checkpoint: ExecutionCheckpoint) {
  const retried = await vercelTurn({
    answer: (request) => streamed(request.number === 1 ? HEALTHY[2]! : HEALTHY[3]!),
    handle: () => runtimeAnswer({ status: 'valid' }),
    session: { context: { currentRequest: { message: 'Check the expense pack.', input: 'The expense pack.' }, execution: resumeFrom(checkpoint) } }
  })
  return retried
}

describe('vercel', () => {
  describe.each([
    {
      when: 'the connection to the relay drops before the first byte',
      answer: (request: EndpointRequest) =>
        request.number === 2 ? Promise.reject(new TypeError('Failed to fetch')) : healthy(request),
      requests: 2
    },
    {
      when: 'the endpoint drops the connection before its first byte, and the relay answers 502',
      answer: (request: EndpointRequest) =>
        request.number === 2
          ? whole(JSON.stringify({ error: 'the configured endpoint could not be reached: connection reset', code: 'assistant-relay-upstream' }), 502)
          : healthy(request),
      requests: 2
    }
  ])('$when', ({ answer, requests: expected }) => {
    it('reports connection-lost, keeps the checkpoint, and Retry resumes it', async () => {
      const { events, requests, seen } = await vercelFailure({ answer })
      const failure = failureOf(events)
      expect(failure).toMatchObject({ code: 'connection-lost', source: 'model', outcome: 'unknown' })
      expect(failureRecovery(failure)).toBe('review')
      // Never sent again: the outcome is unknown, and only a refusal is retried.
      expect(requests).toHaveLength(expected)
      expect(ofType(events, 'retry')).toEqual([])
      const checkpoint = lastCheckpoint(events)
      expect(checkpoint.status).toBe('stopped')
      expect(operations(checkpoint)).toEqual([['get_schema', 'complete']])
      expect(seen.map((call) => call.name)).toEqual(['get_schema'])

      expect(retryOffered(failure)).toBe(true)
      const retried = await vercelRetry(checkpoint)
      expect(ofType(retried.events, 'error')).toEqual([])
      expect(ofType(retried.events, 'message').at(-1)?.text).toBe('The pack validates.')
      const resumed = lastCheckpoint(retried.events)
      expect(operations(resumed)).toEqual([['get_schema', 'complete'], ['validate', 'complete']])
      expect(resumed.used).toBe(checkpoint.used + 2)
      // The completed call was not made again.
      expect(retried.seen.map((call) => call.name)).toEqual(['validate'])
    })
  })

  describe.each([
    {
      when: 'the endpoint closes the stream after the first chunk',
      after: (controller: ReadableStreamDefaultController<Uint8Array>) => controller.error(new TypeError('terminated'))
    },
    {
      when: 'the stream stalls past the relay’s idle bound, and the relay cuts it',
      after: (controller: ReadableStreamDefaultController<Uint8Array>) =>
        new Promise<void>((resolve) =>
          setTimeout(() => {
            controller.error(new TypeError('network error'))
            resolve()
          }, IDLE_BOUND_MS)
        )
    }
  ])('$when', ({ after }) => {
    it('reports the broken stream once, never sends it again, keeps the checkpoint, and Retry resumes it', async () => {
      const started = Date.now()
      const { events, requests } = await vercelFailure({
        answer: (request) => (request.number === 2 ? firstChunkThen(HEALTHY[2]!, after) : healthy(request))
      })
      const failure = failureOf(events)
      expect(failure).toMatchObject({ code: 'unknown', source: 'model', outcome: 'unknown' })
      expect(failureRecovery(failure)).toBe('review')
      expect(requests).toHaveLength(2)
      expect(ofType(events, 'retry')).toEqual([])
      expect(Date.now() - started).toBeLessThan(10_000)
      const checkpoint = lastCheckpoint(events)
      expect(checkpoint.status).toBe('stopped')
      expect(operations(checkpoint)).toEqual([['get_schema', 'complete']])

      expect(retryOffered(failure)).toBe(true)
      const retried = await vercelRetry(checkpoint)
      expect(ofType(retried.events, 'error')).toEqual([])
      expect(operations(lastCheckpoint(retried.events))).toEqual([['get_schema', 'complete'], ['validate', 'complete']])
    })
  })

  describe.each([
    {
      when: 'the endpoint answers 413',
      refusal: () => whole(JSON.stringify({ error: { message: 'Request too large', type: 'invalid_request_error' } }), 413)
    },
    {
      when: 'the endpoint answers 400 context_length_exceeded',
      refusal: () =>
        whole(JSON.stringify({ error: { message: 'This model’s maximum context length is exceeded.', type: 'invalid_request_error', code: 'context_length_exceeded' } }), 400)
    }
  ])('$when', ({ refusal }) => {
    it('reports context-limit with its checkpoint, shrinks and resends nothing, and offers no Retry', async () => {
      const { events, requests } = await vercelFailure({
        answer: (request) => (request.number === 2 ? refusal() : healthy(request))
      })
      const failure = failureOf(events)
      expect(failure).toMatchObject({ code: 'context-limit', source: 'model', outcome: 'refused' })
      expect(failureRecovery(failure)).toBe('narrow')
      // No automatic shrink-and-resend in this release (docs/design/ai-error-handling.md):
      // one request, refused, reported.
      expect(requests).toHaveLength(2)
      expect(ofType(events, 'retry')).toEqual([])
      expect(ofType(events, 'response_recovery')).toEqual([])
      const checkpoint = lastCheckpoint(events)
      expect(checkpoint.status).toBe('stopped')
      expect(operations(checkpoint)).toEqual([['get_schema', 'complete']])
      // Retry would send the same context again; a host asks for a smaller request instead.
      expect(retryOffered(failure)).toBe(false)
    })
  })

  it('the runtime’s socket drops during a tool call: the model reads a failed result, the run goes on, and the checkpoint keeps it', async () => {
    const { events, requests, seen } = await vercelFailure({
      handle: (call, control) => {
        if (call.name === 'validate') {
          control.drop()
          return undefined
        }
        return runtimeAnswer({ status: 'ok' })
      }
    })
    expect(ofType(events, 'error')).toEqual([])
    const failed = ofType(events, 'tool_result').find((result) => result.name === 'validate')!
    expect(failed).toMatchObject({ isError: true, structured: { failure: { code: 'tool-failed', source: 'tool', outcome: 'unknown' } } })
    expect(failureRecovery((failed.structured as { failure: AIFailure }).failure)).toBe('review')
    // The model's next request carries it, and the turn ends with an answer.
    expect(toolTexts(requests[2]!).at(-1)).toContain('could not complete this operation')
    expect(ofType(events, 'message').at(-1)?.text).toBe('The pack validates.')
    expect(seen.map((call) => call.name)).toEqual(['get_schema', 'validate'])
    const checkpoint = lastCheckpoint(events)
    expect(checkpoint.status).toBe('complete')
    expect(operations(checkpoint)).toEqual([['get_schema', 'complete'], ['validate', 'failed']])
    // No failed run, so no Retry: the next turn opens a new connection.
  })

  it('the endpoint answers whole JSON where a stream was asked for: the answer is read whole, and nothing fails', async () => {
    const { events, requests } = await vercelFailure({
      answer: (request) => {
        expect(request.body.stream).toBe(true)
        if (request.number === 2) return whole(wholeTurn({ tool: { name: 'validate', args: { document: '{}' } } }))
        return healthy(request)
      }
    })
    expect(ofType(events, 'error')).toEqual([])
    expect(requests).toHaveLength(3)
    expect(ofType(events, 'tool_result').map((result) => [result.name, result.isError])).toEqual([['get_schema', false], ['validate', false]])
    expect(ofType(events, 'message').at(-1)?.text).toBe('The pack validates.')
    expect(lastCheckpoint(events).status).toBe('complete')
  })

  it('the endpoint answers whole JSON where a stream was asked for and drops the connection after its first byte: connection-lost, never sent again', async () => {
    const { events, requests } = await vercelFailure({
      answer: (request) => {
        if (request.number !== 2) return healthy(request)
        const first = wholeTurn({ text: 'The pack validates.' }).slice(0, 40)
        const body = new ReadableStream<Uint8Array>({
          start(controller) {
            controller.enqueue(new TextEncoder().encode(first))
          },
          pull(controller) {
            controller.error(new TypeError('terminated'))
          }
        })
        return new Response(body, { status: 200, headers: { 'content-type': 'application/json' } })
      }
    })
    const failure = failureOf(events)
    expect(failure).toMatchObject({ code: 'connection-lost', source: 'model', outcome: 'unknown' })
    expect(failureRecovery(failure)).toBe('review')
    expect(requests).toHaveLength(2)
    expect(ofType(events, 'retry')).toEqual([])
    expect(operations(lastCheckpoint(events))).toEqual([['get_schema', 'complete']])
    expect(retryOffered(failure)).toBe(true)
  })

  it('the endpoint answers whole JSON cut short where a stream was asked for: invalid-response, the checkpoint kept, and Retry resumes it', async () => {
    const { events, requests } = await vercelFailure({
      answer: (request) => (request.number === 2 ? whole(wholeTurn({ text: 'The pack' }).slice(0, 60)) : healthy(request))
    })
    const failure = failureOf(events)
    expect(failure).toMatchObject({ code: 'invalid-response', source: 'model' })
    expect(failureRecovery(failure)).toBe('retry')
    expect(requests).toHaveLength(2)
    const checkpoint = lastCheckpoint(events)
    expect(operations(checkpoint)).toEqual([['get_schema', 'complete']])
    expect(retryOffered(failure)).toBe(true)
    const retried = await vercelRetry(checkpoint)
    expect(ofType(retried.events, 'error')).toEqual([])
  })
})

/**
 * The codex engine's paths run through the real agent transport against a
 * scripted Desk server socket. A whole-JSON answer has no counterpart: the
 * agent wire is JSON frames, and the Codex bridge owns the model's stream.
 */
describe('codex', () => {
  /** A run that has completed get_schema, then does `fail`. */
  const afterOneCall = (fail: (bridge: Bridge) => void | Promise<void>) => async (bridge: Bridge) => {
    bridge.started()
    await bridge.call('get_schema', {})
    await fail(bridge)
  }
  /** Retry's run: the same request again, the bridge healthy now. */
  const healthyRetry = async (bridge: Bridge) => {
    bridge.started()
    await bridge.call('validate', { document: '{}' })
    bridge.say('The pack validates.')
    bridge.end()
  }

  async function codexFailure(fail: (bridge: Bridge) => void | Promise<void>, handle?: (call: RuntimeCall, control: RuntimeControl) => unknown) {
    return codexTurn({ script: afterOneCall(fail), ...(handle === undefined ? {} : { handle }) })
  }

  async function codexRetry(checkpoint: ExecutionCheckpoint) {
    return codexTurn({
      script: healthyRetry,
      session: { context: { currentRequest: { message: 'Check the expense pack.', input: 'The expense pack.' }, execution: resumeFrom(checkpoint) } }
    })
  }

  it('the socket drops before the first byte: connection-lost, and Retry starts the request again', async () => {
    const { events, runs } = await codexTurn({ script: async (bridge) => bridge.drop() })
    const failure = failureOf(events)
    expect(failure).toMatchObject({ code: 'connection-lost', source: 'agent', outcome: 'unknown' })
    expect(failureRecovery(failure)).toBe('review')
    expect(runs).toHaveLength(1)
    const checkpoint = lastCheckpoint(events)
    expect(checkpoint.status).toBe('stopped')
    expect(checkpoint.operations).toEqual([])
    expect(retryOffered(failure)).toBe(true)
    const retried = await codexTurn({
      script: async (bridge) => {
        bridge.started()
        bridge.say('The pack validates.')
        bridge.end()
      },
      session: { context: { currentRequest: { message: 'Check the expense pack.', input: 'The expense pack.' }, execution: resumeFrom(checkpoint) } }
    })
    expect(ofType(retried.events, 'error')).toEqual([])
  })

  it.each([
    ['after the first byte', (bridge: Bridge) => {
      bridge.say('Checking', { type: 'text' })
      bridge.drop()
    }],
    ['during a streamed message', (bridge: Bridge) => {
      bridge.say('The pack', { type: 'text' })
      bridge.say(' vali', { type: 'text' })
      bridge.drop()
    }]
  ])('the socket drops %s: connection-lost, the checkpoint kept, and Retry resumes it', async (_when, fail) => {
    const { events, runs } = await codexFailure(fail)
    const failure = failureOf(events)
    expect(failure).toMatchObject({ code: 'connection-lost', source: 'agent', outcome: 'unknown' })
    expect(failureRecovery(failure)).toBe('review')
    expect(runs).toHaveLength(1)
    const checkpoint = lastCheckpoint(events)
    expect(operations(checkpoint)).toEqual([['get_schema', 'complete']])
    expect(retryOffered(failure)).toBe(true)
    const retried = await codexRetry(checkpoint)
    expect(ofType(retried.events, 'error')).toEqual([])
    expect(operations(lastCheckpoint(retried.events))).toEqual([['get_schema', 'complete'], ['validate', 'complete']])
  })

  it('the run stalls past the Codex bridge’s bound, and the bridge ends it: timeout, the checkpoint kept, and Retry resumes it', async () => {
    const { events } = await codexFailure(async (bridge) => {
      bridge.say('Checking', { type: 'text' })
      await new Promise((resolve) => setTimeout(resolve, 200))
      // What the Desk server sends when the bridge's run deadline passes.
      bridge.end('limit', { version: 1, code: 'timeout', source: 'agent', outcome: 'unknown' })
    })
    const failure = failureOf(events)
    expect(failure).toMatchObject({ code: 'timeout', source: 'agent', outcome: 'unknown' })
    expect(failureRecovery(failure)).toBe('review')
    const checkpoint = lastCheckpoint(events)
    expect(operations(checkpoint)).toEqual([['get_schema', 'complete']])
    expect(retryOffered(failure)).toBe(true)
    const retried = await codexRetry(checkpoint)
    expect(ofType(retried.events, 'error')).toEqual([])
  })

  it('the Desk server refuses the context as too long: context-limit, the checkpoint kept, and no Retry', async () => {
    const { events, runs } = await codexTurn({
      script: async (bridge) => {
        bridge.started()
        // What the Desk server sends when the prompt is over the input budget.
        bridge.end('context-limit', { version: 1, code: 'context-limit', source: 'context', outcome: 'not-started' })
      }
    })
    const failure = failureOf(events)
    expect(failure).toMatchObject({ code: 'context-limit', source: 'context', outcome: 'not-started' })
    expect(failureRecovery(failure)).toBe('narrow')
    expect(runs).toHaveLength(1)
    expect(lastCheckpoint(events).status).toBe('stopped')
    expect(retryOffered(failure)).toBe(false)
  })

  it('the runtime’s socket drops during a tool call: the agent reads a failed result, the run goes on, and the checkpoint keeps it', async () => {
    let answered: { text: string; isError: boolean } | undefined
    const { events, seen } = await codexFailure(
      async (bridge) => {
        answered = await bridge.call('validate', { document: '{}' })
        bridge.say('The pack could not be validated.')
        bridge.end()
      },
      (call, control) => {
        if (call.name === 'validate') {
          control.drop()
          return undefined
        }
        return runtimeAnswer({ status: 'ok' })
      }
    )
    expect(ofType(events, 'error')).toEqual([])
    expect(answered).toMatchObject({ isError: true })
    const failed = ofType(events, 'tool_result').find((result) => result.name === 'validate')!
    expect(failed).toMatchObject({ isError: true, structured: { failure: { code: 'tool-failed', source: 'tool', outcome: 'unknown' } } })
    expect(seen.map((call) => call.name)).toEqual(['get_schema', 'validate'])
    const checkpoint = lastCheckpoint(events)
    expect(checkpoint.status).toBe('complete')
    expect(operations(checkpoint)).toEqual([['get_schema', 'complete'], ['validate', 'failed']])
  })
})
