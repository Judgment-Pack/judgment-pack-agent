/**
 * One ordered channel between a framework's callbacks and the contract's
 * iterator.
 *
 * A loop written as an async generator all the way down has `yield` as the
 * only ordering there is. A framework loop is not: `streamText` calls a tool's
 * `execute` from inside its own pipeline, and an adapter cannot `yield` from
 * there. What it can do is put the event on this channel and **wait until the
 * consumer has taken it**, which is the property the whole event order rests on:
 *
 *   `tool_call` delivered → `session.callTool` → *the desk's gate reports here*
 *   → `tool_result` delivered
 *
 * The guardrail event is the **desk's** and travels on the desk's own sink, not
 * on this channel (`assistant/session.ts` puts it there). So it lands between
 * the two only if the `tool_call` has already reached the consumer when the
 * gate fires — a queue that merely buffers would put the refusal *before* the
 * call it refused, which is the order a reader cannot make sense of.
 *
 * `push` therefore resolves after the consumer's `for await` body has run for
 * that event, and not before. There is no deadlock in that: the consumer pulls
 * from `drain()` while the framework's pipeline is driven from a separate task,
 * so a blocked `execute` is backpressure and not a cycle.
 *
 * **`abandon()` exists because a consumer may stop listening.** A pane that
 * unmounts, a `break`, a `return()` on the iterator: whatever is waiting on a
 * delivery that will never happen must be released, or the framework's task
 * runs for ever behind a page nobody is looking at.
 *
 * **And the event being yielded when that happens is the one `abandon()` cannot
 * see.** `drain` takes an entry off the queue *before* yielding it, so a
 * consumer that stops at exactly that event leaves a delivery nothing is
 * holding: not the queue, because it was taken off; not the resumed loop,
 * because it never resumes. `drive()` would wait on that `push` for ever and
 * `runVercel`'s `finally` would wait on `drive()`. So the generator settles the
 * entry it was yielding from a `finally` of its own, on **every** exit path —
 * its own return, a consumer's `return()`, a throw — and abandons the rest with
 * it.
 */
import type { AssistantEvent } from '../../engine'

interface Waiting {
  event: AssistantEvent
  delivered: () => void
}

/**
 * A second consumer, refused.
 *
 * **One channel, one consumer, by contract.** The in-flight slot is a single
 * slot because there is a single reader: two drains taking concurrently would
 * overwrite each other's, and the entry that was overwritten would be in
 * neither the queue nor the slot — a delivery nothing could ever settle, which
 * is the defect one layer up from the one this channel already fixed. Tracking
 * a slot per drain would *support* a second reader instead, and supporting one
 * would be inventing a use nobody has: the events of a run are an ordered
 * stream and splitting them between two readers has no meaning.
 *
 * So the second reader is refused **before it takes anything**, by name, and
 * the first reader and every queued push are left exactly as they were.
 */
export class ChannelHasOneConsumer extends Error {
  constructor() {
    super(
      'this channel already has a consumer: a run is one ordered stream of events, ' +
        'and a second reader would take deliveries the first can no longer settle'
    )
    this.name = 'ChannelHasOneConsumer'
  }
}

export interface EventChannel {
  /** Put one event on the channel; resolves once the consumer has taken it. */
  push(event: AssistantEvent): Promise<void>
  /** No more events. `drain` returns once what is queued has been delivered. */
  close(): void
  /** Nobody is listening any more: release every pending delivery at once. */
  abandon(): void
  /**
   * The one consumer's stream. A second one throws `ChannelHasOneConsumer` on
   * its first `next()`, having taken nothing.
   */
  drain(): AsyncGenerator<AssistantEvent>
}

export function eventChannel(options: { onAbandon?: () => void } = {}): EventChannel {
  const queue: Waiting[] = []
  let wake: (() => void) | null = null
  let closed = false
  let abandoned = false
  /** The entry `drain` has yielded and not yet resumed from. */
  let inFlight: Waiting | null = null
  /** True once a drain has begun. There is one, for the life of the channel. */
  let consuming = false

  const stir = () => {
    const wakeNow = wake
    wake = null
    wakeNow?.()
  }

  /** Release everything nobody will ever deliver, the in-flight one included. */
  const releaseAll = () => {
    const first = !abandoned
    abandoned = true
    closed = true
    // **Before anything is released, and this order is the whole of it.** What
    // resumes on a released delivery is the producer, and it resumes on a
    // microtask — earlier than any `finally` further out could run. So the
    // owner is told the run is over *first*, and the producer finds an ended
    // run rather than a session it should carry on with.
    if (first) options.onAbandon?.()
    const held = inFlight
    inFlight = null
    held?.delivered()
    while (queue.length > 0) queue.shift()!.delivered()
    stir()
  }

  return {
    push(event: AssistantEvent): Promise<void> {
      if (abandoned || closed) return Promise.resolve()
      return new Promise<void>((resolve) => {
        queue.push({ event, delivered: resolve })
        stir()
      })
    },
    close(): void {
      closed = true
      stir()
    },
    abandon: releaseAll,
    async *drain(): AsyncGenerator<AssistantEvent> {
      // **Before the `try`, so this refusal releases nothing.** A second reader
      // that ran the cleanup below would settle the first reader's in-flight
      // delivery on its way out — which is the failure it is here to prevent.
      if (consuming) throw new ChannelHasOneConsumer()
      consuming = true
      try {
        for (;;) {
          while (queue.length > 0) {
            if (abandoned) return
            const next = queue.shift()!
            inFlight = next
            yield next.event
            inFlight = null
            // After the consumer's own body has run for this event, which is what
            // makes the desk's interleaved guardrail land in the right place.
            next.delivered()
          }
          if (closed) return
          await new Promise<void>((resolve) => {
            wake = resolve
          })
        }
      } finally {
        // **Every exit, not only the loop's own.** A consumer that stopped at an
        // event — `break`, `return()`, a throw in its body — never resumes the
        // `yield` above, so the entry it took off the queue is a delivery
        // nothing else can settle. Settling it here is what keeps the producer
        // from waiting on it for ever.
        releaseAll()
      }
    }
  }
}
