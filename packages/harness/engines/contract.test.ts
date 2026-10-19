/**
 * The contract's own vocabulary, held where both engines read it.
 *
 * Most of what is here is the iterator every engine is wrapped in, because it
 * is the piece with a property neither engine's suite can state on its own: an
 * async generator serves `next()`, `return()` and `throw()` from **one queue**,
 * so a `return()` arriving while a `next()` is pending is not run until that
 * `next()` settles. A run waiting on a model request that ends only when it is
 * aborted could therefore never be stopped by the consumer that owned it — the
 * abort was inside the `return()` that was queued behind the very `next()` it
 * would have released. Both hung for ever, on both engines, measured.
 */
import { describe, expect, it } from 'vitest'
import { RunCancelled, eventIterator, openRun, servedSchema, withAbort } from './contract'
import { normalize } from '../thinking'
import type { AssistantEvent, AssistantSession } from '../engine'

/** A promise, or a marker where it did not settle inside the bound. */
async function within(ms: number, work: Promise<unknown>): Promise<string> {
  return Promise.race([
    work.then(
      () => 'settled',
      () => 'settled'
    ),
    new Promise<string>((resolve) => setTimeout(() => resolve('STILL WAITING'), ms))
  ])
}

/**
 * A generator that waits, exactly as a run waiting on a model request does, and
 * ends only when something outside it says so.
 */
function waiting(): {
  open: () => AsyncGenerator<AssistantEvent>
  release: () => void
  opened: () => number
  finallyRan: () => number
} {
  let unblock = () => {}
  let released = false
  let opens = 0
  let ran = 0
  const open = async function* (): AsyncGenerator<AssistantEvent> {
    opens += 1
    try {
      // A latch, so a release that arrives before the run started still lets it
      // through — the release is the thing outside deciding it may finish.
      if (!released) {
        await new Promise<void>((resolve) => {
          unblock = resolve
        })
      }
      yield { type: 'end' }
    } finally {
      ran += 1
    }
  }
  return {
    open,
    release: () => {
      released = true
      unblock()
    },
    opened: () => opens,
    finallyRan: () => ran
  }
}

/** The least session a gate needs. */
function sessionWith(signal: AbortSignal): AssistantSession {
  return {
    prompt: 'p',
    testPrompt: '',
    tools: [],
    callTool: async () => ({ content: [] }),
    hostTools: [],
    model: { family: 'openai-compatible', model: 'm', call: async () => new Response('{}') },
    thinking: normalize('off', 'openai-compatible'),
    signal
  }
}

describe('the run gate', () => {
  it('closes before it aborts, and before it releases', () => {
    const order: string[] = []
    const controller = new AbortController()
    let gate: ReturnType<typeof openRun>
    // eslint-disable-next-line prefer-const
    gate = openRun(sessionWith(controller.signal), () => {
      // **The order the whole class fix rests on.** By the time anything else
      // runs, the run is already closed: a loop released here can yield
      // whatever it likes and none of it is anybody's.
      order.push(`released closed=${String(gate.isClosed())} aborted=${String(gate.signal.aborted)}`)
    })
    expect(gate.isClosed()).toBe(false)
    gate.close()
    expect(order).toEqual(['released closed=true aborted=true'])
    expect(gate.isClosed()).toBe(true)
  })

  it('is already closed where the session was aborted before it opened', () => {
    const controller = new AbortController()
    controller.abort()
    let released = 0
    const gate = openRun(sessionWith(controller.signal), () => {
      released += 1
    })
    expect(gate.isClosed()).toBe(true)
    expect(gate.signal.aborted).toBe(true)
    expect(released).toBe(1)
  })

  it('closes once, however many ways it is reached', () => {
    const controller = new AbortController()
    let released = 0
    const gate = openRun(sessionWith(controller.signal), () => {
      released += 1
    })
    gate.close()
    gate.close()
    controller.abort()
    expect(released).toBe(1)
  })
})

describe('one await on the world outside an engine', () => {
  it('starts no work at all on a closed run', async () => {
    const controller = new AbortController()
    controller.abort()
    let started = 0
    await expect(
      withAbort(() => {
        started += 1
        return Promise.resolve('never asked')
      }, controller.signal)
    ).rejects.toThrow(RunCancelled)
    // The defect a thunk closes: taking a promise meant the call had already
    // been made by the time the signal was read.
    expect(started, 'work was started on a closed run').toBe(0)
  })

  it('settles when the run does, whatever the work decides to do', async () => {
    const controller = new AbortController()
    const waiting = withAbort(() => new Promise<string>(() => {}), controller.signal)
    controller.abort()
    await expect(waiting).rejects.toThrow(RunCancelled)
  })

  it('notices a run that closed while the work ran, even when the work then answers', async () => {
    // The abort fires inside the work, before `withAbort` has a listener, and the
    // work goes on to resolve with a value. A listener alone never hears it; only
    // the read after the listener exists does. Without it the closed run is
    // handed an answer it must never see.
    const controller = new AbortController()
    const closing = withAbort(() => {
      controller.abort()
      return Promise.resolve('an answer for a run that is over')
    }, controller.signal)
    await expect(closing).rejects.toThrow(RunCancelled)
  })

  it('carries the work’s own answer, and its own failure', async () => {
    const live = new AbortController().signal
    await expect(withAbort(() => Promise.resolve('answered'), live)).resolves.toBe('answered')
    await expect(withAbort(() => Promise.reject(new Error('failed')), live)).rejects.toThrow(
      'failed'
    )
  })
})

/**
 * One iterator over a waiting source, with a gate whose release ends the wait.
 *
 * The engines build exactly this shape: a gate the consumer, the session and
 * the run's own end all close, and a source that only finishes when it does.
 */
function iterate(source: ReturnType<typeof waiting>, onClose?: () => void) {
  let closes = 0
  const gate = openRun(sessionWith(new AbortController().signal), () => {
    closes += 1
    onClose?.()
    source.release()
  })
  return { iterator: eventIterator({ gate, open: source.open }), gate, closes: () => closes }
}

describe('the iterator every engine is wrapped in', () => {
  it('cancels before it waits, so a pending next settles and then the return does', async () => {
    // The exact sequence: `next()` is in flight and waiting; `return()` arrives.
    // Cancelling has to happen **before** anything is awaited, or the queued
    // `return()` never runs and neither promise ever settles.
    const source = waiting()
    let cancelled = 0
    const { iterator } = iterate(source, () => {
      cancelled += 1
    })
    const settledInOrder: string[] = []
    const pending = iterator.next().then(() => settledInOrder.push('next'))
    // Give the generator a turn to reach its wait.
    await Promise.resolve()
    const returned = iterator.return!().then(() => settledInOrder.push('return'))

    expect(await within(1000, pending), 'the pending next').toBe('settled')
    expect(await within(1000, returned), 'the return').toBe('settled')
    // The pending `next()` settles first. It is waiting on what the cancel
    // released, and the `return()` waits on the generator closing behind it.
    expect(settledInOrder).toEqual(['next', 'return'])
    expect(cancelled, 'cancelled exactly once').toBe(1)
    expect(source.finallyRan(), 'the generator was closed exactly once').toBe(1)
  })

  it('answers a pending next with done rather than an event', async () => {
    const source = waiting()
    const { iterator } = iterate(source)
    const pending = iterator.next()
    await Promise.resolve()
    void iterator.return!()
    expect(await pending).toEqual({ value: undefined, done: true })
  })

  it('cancels once however many times it is asked to', async () => {
    const source = waiting()
    let cancelled = 0
    const { iterator } = iterate(source, () => {
      cancelled += 1
    })
    void iterator.next()
    await Promise.resolve()
    expect(await iterator.return!()).toEqual({ value: undefined, done: true })
    expect(await iterator.return!()).toEqual({ value: undefined, done: true })
    expect(cancelled).toBe(1)
    expect(source.finallyRan()).toBe(1)
  })

  it('is done after a return, without asking what is underneath', async () => {
    const source = waiting()
    const { iterator } = iterate(source)
    void iterator.next()
    await Promise.resolve()
    await iterator.return!()
    const opened = source.opened()
    expect(await iterator.next()).toEqual({ value: undefined, done: true })
    expect(await iterator.next()).toEqual({ value: undefined, done: true })
    expect(source.opened(), 'nothing underneath was touched again').toBe(opened)
  })

  it('opens nothing at all where the consumer leaves before its first next', async () => {
    const source = waiting()
    let cancelled = 0
    const { iterator } = iterate(source, () => {
      cancelled += 1
    })
    expect(await within(1000, iterator.return!() as Promise<unknown>)).toBe('settled')
    expect(source.opened(), 'the run was never started').toBe(0)
    expect(cancelled).toBe(1)
  })

  it('cancels and rethrows on throw, including while a next is pending', async () => {
    const source = waiting()
    let cancelled = 0
    const { iterator } = iterate(source, () => {
      cancelled += 1
    })
    const pending = iterator.next()
    await Promise.resolve()
    const thrown = iterator.throw!(new Error('the consumer gave up'))
    expect(await within(1000, thrown.then(() => undefined, () => undefined))).toBe('settled')
    await expect(thrown).rejects.toThrow('the consumer gave up')
    expect(await pending).toEqual({ value: undefined, done: true })
    expect(cancelled).toBe(1)
    expect(source.finallyRan()).toBe(1)
  })

  it('reports done and closes once when the run ends by itself', async () => {
    const source = waiting()
    let cancelled = 0
    const { iterator } = iterate(source, () => {
      cancelled += 1
    })
    const pending = iterator.next()
    source.release()
    expect(await pending).toEqual({ value: { type: 'end' }, done: false })
    expect(await iterator.next()).toEqual({ value: undefined, done: true })
    expect(cancelled, 'the run cleans up on the ordinary path too').toBe(1)
    expect(source.finallyRan()).toBe(1)
  })

  it('is its own iterable, so `for await` drives it', async () => {
    const source = waiting()
    const { iterator } = iterate(source)
    expect(iterator[Symbol.asyncIterator]()).toBe(iterator)
    source.release()
    const seen: AssistantEvent[] = []
    for await (const event of iterator) seen.push(event)
    expect(seen).toEqual([{ type: 'end' }])
  })
})

describe('the schema an engine may show the model', () => {
  it('is the served one, and a refusal where the runtime served none', () => {
    expect(servedSchema({ name: 'validate', inputSchema: { type: 'object' } })).toEqual({
      type: 'object'
    })
    expect(() => servedSchema({ name: 'a_new_tool' })).toThrow('a_new_tool')
    expect(() => servedSchema({ name: 'a_new_tool' })).toThrow('without an input schema')
  })
})

it('does not misclassify an ordinary code example as a pack proposal', async () => {
  const { hasProposalFence, streamingProse } = await import('./contract')
  expect(hasProposalFence('```json\n{"x":1}\n```')).toBe(false)
  expect(hasProposalFence('```json\n{"proposal":{"document":{}}}\n```')).toBe(true)
  expect(streamingProse('Answer.\n\n``')).toBe('Answer.')
  expect(streamingProse('Answer.\n\n```json\n{"proposal":')).toBe('Answer.')
})

it('finds one explicit proposal after indented examples without consuming its fence', async () => {
  const { extractProposal, hasProposalFence, proseOf } = await import('./contract')
  const examples = 'Runtime report:\n```text\nstatus: valid\n```\n1. Probe\n     ```json\n     {"status":"evaluated"}\n     ```\n'
  const proposal = '```json\n{"proposal":{"kind":"create","document":{"id":"p"},"unknowns":[]}}\n```'
  expect(hasProposalFence(examples + proposal)).toBe(true)
  expect(extractProposal(examples + proposal).document).toEqual({ id: 'p' })
  expect(proseOf(examples + proposal)).toBe(examples.trim())
  expect(() => extractProposal(examples + proposal + '\n' + proposal)).toThrow('carried 2')
  expect(hasProposalFence('```json\n{"example":{"proposal":{}}}\n```')).toBe(false)
  expect(hasProposalFence('```text\n{"proposal":{"document":{}}}\n```')).toBe(false)
  expect(hasProposalFence('```json\n{"proposal":{"document":{}}}')).toBe(false)
  expect(() => extractProposal('```json\n{"proposal":{"document":null}}\n```')).toThrow('document object')
})


describe('streamed Markdown projection', () => {
  it('streams unfinished ordinary code and subsequent formatted sections before the answer ends', async () => {
    const {streamingProse} = await import('./contract')
    const code='Example:\n\n```typescript\nconst count = 1;'
    expect(streamingProse(code)).toBe(code)
    expect(streamingProse(code+'\n``')).toBe(code)
    const body=code+'\n```\n\n## Results\n\n- First\n- Second\n\n> A quote\n\n| Item | Value |\n| --- | --- |\n| Count | 1 |'
    expect(streamingProse(body)).toBe(body)
    expect(streamingProse(body+'\n\n```json\n{"proposal":{"document":')).toBe(body)
  })
  it('streams explicitly marked JSON examples token by token, including nested proposal examples', async () => {
    const {streamingProse, hasProposalFence} = await import('./contract')
    const prefix='```json example\n{"example":{"proposal":{"document":'
    expect(streamingProse(prefix)).toBe(prefix)
    expect(hasProposalFence(prefix+'{}}}}\n```')).toBe(false)
  })
  it('releases legacy JSON examples once their value is complete, without waiting for the closing fence or final reply', async () => {
    const {streamingProse} = await import('./contract')
    const prefix='Example:\n\n```json\n'
    expect(streamingProse(prefix+'{"status":')).toBe('Example:')
    expect(streamingProse(prefix+'{"status":"valid"}')).toBe(prefix+'{"status":"valid"}')
    const nested=prefix+'{"example":{"proposal":{}}}'
    expect(streamingProse(nested+'\n```')).toBe(nested+'\n```')
  })
  it.each([
    '{"proposal":{"document":{"id":"p"}}}',
    '{"metadata":1,"proposal":{"document":{"id":"p"}}}',
    '{"propo\\u0073al":{"document":{"id":"p"}}}'
  ])('never flashes an incomplete legacy proposal: %s', async body => {
    const {streamingProse} = await import('./contract')
    const answer='Before.\n\n```json\n'+body+'\n```\n\nAfter.'
    const begin=answer.indexOf('```')
    for(let n=begin;n<=answer.indexOf('After.');n++)expect(streamingProse(answer.slice(0,n))).toBe('Before.')
    expect(streamingProse(answer)).toBe('Before.\n\n\nAfter.')
  })
  it('handles tildes, longer fences, CRLF, indented examples, and inline backticks without freezing prose', async () => {
    const {streamingProse,proseOf} = await import('./contract')
    for(const code of ['~~~sql\r\nselect 1;\r\n~~~','````text\n```\n````','    ```python\n    print(1)\n    ```','Inline ``a ` b`` continues.']) {
      const body=code+'\n\nNext paragraph.'
      expect(streamingProse(body)).toBe(body)
      expect(streamingProse(body+'\n```json\n{"proposal":{"document":{}}}\n```')).toBe(proseOf(body+'\n```json\n{"proposal":{"document":{}}}\n```'))
    }
  })
})
