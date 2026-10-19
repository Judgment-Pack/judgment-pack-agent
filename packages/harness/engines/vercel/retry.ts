/**
 * The one automatic retry this engine makes of a model request: a refusal.
 *
 * The SDK's own retries stay off (`maxRetries: 0` in `loop.ts`), because its
 * retryable set includes statuses a person has to go and fix and statuses whose
 * request may already have run and been charged. This wraps the desk's model
 * capability instead, below the SDK and above the relay, and holds one rule:
 *
 * - **only a refusal the failure catalogue classes `refused`** — a 4xx other
 *   than 408, which did nothing and charged nothing — and only one named in
 *   `failures.ts` (`canRetryRefusal`: the 429 `rate-limited` class, and a 4xx
 *   carrying the overload code `overloaded_error`). Never an `unknown`
 *   outcome: a 5xx, 408, 504 or a transport failure may have run;
 * - **never after any response byte.** The retry is decided on a response that
 *   was not `ok`; a response that was is handed to the SDK as it came, and a
 *   stream that breaks after its first byte is the SDK's failure, reported;
 * - **at most twice**, waiting the longer of the backoff and the provider's
 *   `Retry-After`, and not at all where `Retry-After` asks for longer than one
 *   turn should wait — that refusal is reported with its `retryAt`;
 * - **each attempt is a model request**: it goes through the capability, so it
 *   is counted against the allowance and checkpointed like any other, and each
 *   refused one is handed to `onRefused` for the completion record. A retry the
 *   caller cannot afford (`mayRetry`: the last request is the turn's
 *   tool-free final answer) is not made, and the refusal is reported.
 */
import { RunCancelled, withAbort } from '../contract'
import { canRetryRefusal, normalizeFailure, refusalCode, type AIFailure } from '../../failures'
import type { ModelCall } from '../../engine'

/** Retries after the first attempt: three attempts in all. */
export const MODEL_RETRIES = 2
/** The wait before retry one and retry two, where the provider asks for less. */
export const RETRY_BACKOFF_MS: readonly number[] = [1_000, 2_000]
/** The longest `Retry-After` this engine waits out inside a turn. */
export const RETRY_WAIT_LIMIT_MS = 30_000

/** One refused attempt, as the completion record keeps it. */
export interface RefusedAttempt {
  /** Which attempt at this request was refused, from 1. */
  attempt: number
  failure: AIFailure
  /** The wait before the next attempt; absent where none follows. */
  waitMs?: number
}

/** A wait the run's signal ends, holding no timer once it has. */
export function waitFor(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    if (signal.aborted) {
      reject(new RunCancelled())
      return
    }
    const stop = () => {
      clearTimeout(timer)
      reject(new RunCancelled())
    }
    const timer = setTimeout(() => {
      signal.removeEventListener('abort', stop)
      resolve()
    }, ms)
    signal.addEventListener('abort', stop, { once: true })
  })
}

/** The wait before retry `attempt`, or nothing where the provider asks for too long. */
function delayFor(failure: AIFailure, attempt: number, now: number): number | undefined {
  const after = failure.retryAt === undefined ? 0 : Date.parse(failure.retryAt) - now
  if (after > RETRY_WAIT_LIMIT_MS) return undefined
  return Math.max(after, RETRY_BACKOFF_MS[attempt - 1] ?? RETRY_BACKOFF_MS[RETRY_BACKOFF_MS.length - 1]!)
}

export function retryingCall(
  call: ModelCall,
  options: {
    /** The run's own signal: every wait and every read is bounded by it. */
    signal: AbortSignal
    onRefused?: (refused: RefusedAttempt) => void | Promise<void>
    /** Said before every attempt, from 1, so a record is never left holding an earlier call's number. */
    onAttempt?: (attempt: number) => void
    /** Whether a retry may take another request now; a refusal is reported where it may not. */
    mayRetry?: () => boolean
    wait?: (ms: number, signal: AbortSignal) => Promise<void>
    now?: () => number
  }
): ModelCall {
  const wait = options.wait ?? waitFor
  const now = options.now ?? Date.now
  return async (suffix, request) => {
    for (let attempt = 1; ; attempt += 1) {
      options.onAttempt?.(attempt)
      const answered = await call(suffix, request)
      if (answered.ok) return answered
      // The refusal's own envelope, read from a copy, so the SDK still reads
      // the original where it is handed on. Classified by status, headers and
      // the codes the catalogue aliases; its prose is never kept.
      const body = await withAbort(() => answered.clone().text(), options.signal).catch((cause: unknown) => {
        if ((cause as { name?: unknown })?.name === 'RunCancelled') throw cause
        return ''
      })
      const failure = normalizeFailure(
        { statusCode: answered.status, responseBody: body, responseHeaders: answered.headers },
        'model',
        now()
      )
      if (!canRetryRefusal(failure, refusalCode(body))) return answered
      const waitMs = attempt <= MODEL_RETRIES && (options.mayRetry?.() ?? true) ? delayFor(failure, attempt, now()) : undefined
      await options.onRefused?.({ attempt, failure, ...(waitMs === undefined ? {} : { waitMs }) })
      if (waitMs === undefined) return answered
      void answered.body?.cancel().catch(() => undefined)
      await wait(waitMs, options.signal)
    }
  }
}
