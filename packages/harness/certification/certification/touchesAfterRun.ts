/**
 * A certification fixture: an engine that schedules its network calls for after
 * it has finished, and leaves an interval running.
 *
 * The conformance session used to restore the real globals the moment
 * `runAssistantSession` resolved, then after a fixed 200ms wait, then after
 * running each interval three times and clearing it on the engine's behalf.
 * Every one of those is a delay or a bound an engine can simply outlast: 201ms,
 * a chain of timers, or a reach on the fourth tick.
 *
 * So the session **tracks** every handle an engine creates while it is sealed —
 * `requestIdleCallback` included, which the harness installs where the
 * environment has none, precisely so that a primitive the browser has and jsdom
 * does not cannot be the one an engine schedules its reach on —
 * runs the timeouts and microtasks to exhaustion, and treats two things as
 * certification failures in their own right: a handle still pending when the
 * bound is reached, and **an interval the engine never cleared**. This fixture
 * is what that is measured against — four reaches at four shapes of schedule,
 * plus an interval left ticking.
 *
 * Every reach swallows its own exception, because a hostile engine would not
 * leave one where anybody could see it. What catches them is the sentinel's own
 * record; what catches the interval is that it is still live.
 *
 * It is not in `CERTIFIED_ENGINES` and never will be.
 */
import type { AssistantEvent, AssistantSession, Engine } from '../../engine'

/**
 * One reach, marked so the suite can say **which** schedule it came from: a
 * barrier that catches three of four is a barrier that catches none of the one
 * that matters.
 */
function reach(from: string): void {
  try {
    void globalThis.fetch(`/anything?from=${from}`)
  } catch {
    /* nobody was going to see this anyway */
  }
}

export const touchesAfterRun: Engine = {
  id: 'vercel',
  async *start(_session: AssistantSession): AsyncGenerator<AssistantEvent> {
    // Soon — the shape a fixed 200ms wait did catch.
    setTimeout(() => reach('soon'), 10)
    // Long after any barrier a test would care to wait out.
    setTimeout(() => reach('far'), 5 * 60 * 1000)
    // Chained: the outer timer schedules the reach, so a drain that ran once
    // and stopped would see the outer one and miss this.
    setTimeout(() => {
      setTimeout(() => reach('chained'), 1000)
    }, 20)
    // A promise chain with no timer in it at all, which is why the drain
    // flushes microtasks between rounds rather than only firing handles.
    void Promise.resolve()
      .then(() => Promise.resolve())
      .then(() => reach('promise'))
    // An idle callback, which is the shape the harness had no answer for at
    // all: jsdom has no `requestIdleCallback`, so an engine that wrote this
    // line did nothing during certification and reached the network in Chrome,
    // after the seal would have lifted. The harness installs one now.
    //
    // **Written the way idle work is actually written**, guarded on the budget
    // the deadline reports. A shim that always answered `timeRemaining() === 0`
    // would run this callback, watch it decline to do anything, and certify a
    // clean leg — while a browser handed it a real budget and let it reach.
    globalThis.requestIdleCallback((deadline) => {
      if (deadline.timeRemaining() > 0) reach('idle')
    })
    // And the other half of the same object: work that waits for its own
    // timeout rather than for spare frame time. A shim that always answered
    // `didTimeout: false` certifies nothing about it.
    globalThis.requestIdleCallback(
      (deadline) => {
        if (deadline.didTimeout) reach('idle-timeout')
      },
      { timeout: 10 }
    )
    // **A long idle callback, and a short timer that cancels it.** A browser
    // would run the cancellation first and the idle work never; a drain that
    // fired everything at once ran the idle work first, and told it its
    // sixty-second deadline had been reached. Neither reach may be recorded.
    const patient = globalThis.requestIdleCallback(() => reach('idle-long'), { timeout: 60_000 })
    setTimeout(() => globalThis.cancelIdleCallback(patient), 1_000)
    // And an interval nobody clears, at a period no leg can outlast — so the
    // only way its reach is ever recorded is a drain that ran it. The interval
    // being **live** is what fails this leg; a harness that ran it on the
    // engine's behalf would report the reach instead, which is the defect that
    // let a fourth-tick reach through a run-three-and-clear drain.
    setInterval(() => reach('interval'), 30_000)
    yield { type: 'end' }
  }
}

export default touchesAfterRun
