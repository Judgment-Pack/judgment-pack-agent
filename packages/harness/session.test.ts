/**
 * The session's own connection, and what it is allowed to be.
 *
 * The conformance suite drives this module end to end; what is here is the
 * three properties that suite cannot see because it injects a transport: that
 * the session opens **its own**, that closing the session closes it, and that
 * the relay base the engine is handed carries the desk's token and nothing
 * else.
 */
import type { Transport } from '@modelcontextprotocol/sdk/shared/transport.js'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { assistantTransport, bindModelCall, openAssistantConnection, runAssistantSession, suffixProblem } from './session'
import { NoSession, configureHarness } from './host'

/** A session id of the shape the chassis mints, for the tests below. */
const A_SESSION = 'a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6'
import { scriptedRuntime, currentContractReplayTools } from './certification/scriptedServer'
import type { AssistantEvent, AssistantSession, Engine } from './engine'
import { normalize } from './thinking'

afterEach(() => {
  vi.unstubAllGlobals()
  window.sessionStorage.clear()
})

describe('the model capability the desk binds', () => {
  /** Install a recording fetch and return what it was called with. */
  function recordingFetch(): { calls: { url: string; init: RequestInit }[] } {
    const calls: { url: string; init: RequestInit }[] = []
    vi.stubGlobal('fetch', async (url: unknown, init: RequestInit) => {
      calls.push({ url: String(url), init })
      return new Response('{}', { status: 200, headers: { 'content-type': 'application/json' } })
    })
    return { calls }
  }

  it('builds the address itself, and puts no parameter of its own on it', async () => {
    const { calls } = recordingFetch()
    await bindModelCall('openai-compatible')('chat/completions', { body: '{}' })
    const url = new URL(calls[0]!.url, 'http://desk.invalid')
    expect(url.pathname).toBe('/api/assistant/relay/v1/chat/completions')
    // The relay refuses a query carrying anything of the page's own, outright.
    expect([...url.searchParams.entries()]).toEqual([])
    expect(calls[0]!.init.method).toBe('POST')
    // And the credential is the session, on the header where it belongs.
    const headers = calls[0]!.init.headers as Record<string, string>
    expect(headers.Authorization).toMatch(/^Bearer /)
    expect(calls[0]!.init.credentials).toBe('omit')
  })

  it('names the AI connection and its revision on a relayed call, in headers and never on the address', async () => {
    const { calls } = recordingFetch()
    const target = { connectionId: 'ai-' + '1'.repeat(24), connectionRevision: 'd'.repeat(64) }
    await bindModelCall('openai-compatible', target)('chat/completions', { body: '{}' })
    const headers = calls[0]!.init.headers as Record<string, string>
    expect(headers['X-Assistant-Connection']).toBe(target.connectionId)
    expect(headers['X-Assistant-Revision']).toBe(target.connectionRevision)
    expect([...new URL(calls[0]!.url, 'http://desk.invalid').searchParams.entries()]).toEqual([])
    // A call bound with no connection names none.
    await bindModelCall('openai-compatible')('chat/completions', { body: '{}' })
    expect(Object.keys(calls[1]!.init.headers as Record<string, string>)).not.toContain('X-Assistant-Connection')
  })

  it('puts the Anthropic suffix after the same mount point', async () => {
    const { calls } = recordingFetch()
    await bindModelCall('openai-compatible')('v1/messages', { body: '{}' })
    expect(new URL(calls[0]!.url, 'http://desk.invalid').pathname).toBe(
      '/api/assistant/relay/v1/v1/messages'
    )
  })

  it('carries the protocol headers and drops everything else', async () => {
    // An allow-list, mirrored from the chassis' own. A credential header this
    // desk has never heard of does not travel, because it is not on the list.
    const { calls } = recordingFetch()
    await bindModelCall('openai-compatible')('chat/completions', {
      body: '{}',
      headers: {
        'content-type': 'application/json',
        'anthropic-version': '2023-06-01',
        authorization: 'Bearer smuggled',
        'x-api-key': 'smuggled',
        cookie: 'smuggled',
        'x-auth-token': 'smuggled',
        'ocp-apim-subscription-key': 'smuggled'
      }
    })
    // **The allow-list, plus this desk's own session and nothing else.** An
    // `authorization` the *engine* wrote is dropped by the allow-list; the one
    // that travels is written here, after it, and is the page's bearer.
    const headers = calls[0]!.init.headers as Record<string, string>
    expect(Object.keys(headers).sort()).toEqual([
      'Authorization',
      'anthropic-version',
      'content-type'
    ])
    expect(headers['content-type']).toBe('application/json')
    expect(headers['anthropic-version']).toBe('2023-06-01')
    expect(headers.Authorization).toMatch(/^Bearer /)
    expect(headers.Authorization).not.toContain('smuggled')
  })

  it('refuses a suffix outside the relay’s own segment rule, before anything is sent', async () => {
    const { calls } = recordingFetch()
    const call = bindModelCall('openai-compatible')
    for (const bad of [
      '',
      '/',
      'chat//completions',
      '../admin',
      'chat/../../admin',
      'chat/completions?stream=true',
      'chat%2Fcompletions',
      'chat\\completions',
      'chat completions',
      '.',
      '..',
      'a'.repeat(257)
    ]) {
      await expect(call(bad, { body: '{}' }), bad).rejects.toThrow()
    }
    expect(calls).toEqual([])
  })

  it('states the rule as a function, so a refusal can be read without a socket', () => {
    expect(suffixProblem('chat/completions', 'openai-compatible')).toBe('')
    expect(suffixProblem('v1/messages', 'anthropic')).toBe('')
    expect(suffixProblem('chat/completions?x=1', 'openai-compatible')).not.toBe('')
    expect(suffixProblem('%2e%2e/admin', 'openai-compatible')).not.toBe('')
  })

  it('admits the Gemini method after a colon, in the final segment and nowhere else', () => {
    // The chassis' one closed exception, mirrored: the part after a colon is a
    // verb, so it is one of three names and it comes last. A mirror that
    // admitted a fourth would be a refusal that only happened on the far side.
    expect(suffixProblem('v1beta/models/gemini-2.5-pro:streamGenerateContent', 'gemini')).toBe('')
    expect(suffixProblem('v1beta/models/m:generateContent', 'gemini')).toBe('')
    expect(suffixProblem('v1beta/models/m:countTokens', 'gemini')).toBe('')
    for (const bad of [
      'v1beta/models/m:embedContent',
      'v1beta/models/m:generateContent/parts',
      'v1beta/models/m:countTokens/x',
      'v1beta/models/:generateContent',
      'v1beta/models/a:b:generateContent',
      'v1beta/models/m%3AgenerateContent'
    ]) {
      expect(suffixProblem(bad, 'gemini'), bad).not.toBe('')
    }
    // And the rule is about the **path**, not the kind: the colon is admitted
    // on every family, exactly as the chassis admits it.
    expect(suffixProblem('v1beta/models/m:generateContent', 'anthropic')).toBe('')
  })

  it('admits alt=sse once, on a gemini endpoint and on no other', () => {
    expect(suffixProblem('v1beta/models/m:streamGenerateContent?alt=sse', 'gemini')).toBe('')
    for (const [bad, family] of [
      ['v1beta/models/m:streamGenerateContent?alt=sse', 'anthropic'],
      ['chat/completions?alt=sse', 'openai-compatible'],
      ['v1beta/models/m:streamGenerateContent?alt=json', 'gemini'],
      ['v1beta/models/m:streamGenerateContent?ALT=sse', 'gemini'],
      ['v1beta/models/m:streamGenerateContent?%61lt=sse', 'gemini'],
      ['v1beta/models/m:streamGenerateContent?alt=sse&alt=sse', 'gemini'],
      ['v1beta/models/m:streamGenerateContent?alt=sse&x=1', 'gemini'],
      ['v1beta/models/m:streamGenerateContent?alt=sse;x=1', 'gemini'],
      ['v1beta/models/m:streamGenerateContent?alt=sse?alt=sse', 'gemini'],
      ['?alt=sse', 'gemini']
    ] as [string, 'openai-compatible' | 'anthropic' | 'gemini'][]) {
      expect(suffixProblem(bad, family), `${family} ${bad}`).not.toBe('')
    }
  })

  it('writes the one admitted pair into the address, and sends nothing else', async () => {
    const { calls } = recordingFetch()
    await bindModelCall('gemini')('v1beta/models/m:streamGenerateContent?alt=sse', { body: '{}' })
    const url = new URL(calls[0]!.url, 'http://desk.invalid')
    expect(url.pathname).toBe('/api/assistant/relay/v1/v1beta/models/m:streamGenerateContent')
    // The pair, and it is now the whole of what a relayed query may be.
    expect([...url.searchParams.entries()]).toEqual([['alt', 'sse']])
  })

  it('sends nothing at all where the pair is asked for on another family', async () => {
    const { calls } = recordingFetch()
    await expect(
      bindModelCall('anthropic')('v1beta/models/m:streamGenerateContent?alt=sse', { body: '{}' })
    ).rejects.toThrow(/no query of its own/)
    expect(calls).toEqual([])
  })

  /**
   * Everything an adversarial engine can reach from one value, as strings.
   *
   * Own and inherited properties, whatever each of them stringifies to, the
   * text of any function it can reach, and the headers if there are any. It is
   * a sweep and not a proof — a getter that only answers on the third read
   * would evade it — but it is the shape the finding asked for, and it fails
   * on the defect it was written against.
   */
  function everythingReachable(value: unknown, depth = 0): string {
    if (depth > 3 || value === null || value === undefined) return String(value)
    const parts: string[] = []
    try {
      parts.push(String(value))
    } catch {
      /* a value that refuses to be a string tells us nothing */
    }
    if (typeof value === 'function') {
      try {
        parts.push(Function.prototype.toString.call(value))
      } catch {
        /* likewise */
      }
      return parts.join(' ')
    }
    if (typeof value !== 'object') return parts.join(' ')
    if (value instanceof Headers) {
      value.forEach((header, name) => parts.push(`${name}: ${header}`))
    }
    const seen = new Set<string>()
    for (
      let level: object | null = value;
      level !== null && level !== Object.prototype;
      level = Object.getPrototypeOf(level) as object | null
    ) {
      for (const key of Object.getOwnPropertyNames(level)) {
        if (seen.has(key)) continue
        seen.add(key)
        if (key === 'constructor') continue
        let read: unknown
        try {
          read = (value as Record<string, unknown>)[key]
        } catch {
          continue
        }
        parts.push(key, everythingReachable(read, depth + 1))
        // A stream is an object like any other: whatever somebody decorated it
        // with is reachable through whoever holds it.
        if (read instanceof ReadableStream) {
          for (const own of Object.getOwnPropertyNames(read)) {
            try {
              parts.push(own, String((read as unknown as Record<string, unknown>)[own]))
            } catch {
              /* a property that refuses to be read tells us nothing */
            }
          }
        }
      }
    }
    return parts.join(' ')
  }

  it('returns a facade an engine cannot read the relay address off', async () => {
    // A browser Response carries the requested URL on `.url`. That URL used to
    // be the relay address with this chassis' session token in it, so returning
    // the one fetch produced handed the engine everything it needed to open
    // /ws?token=… on a connection no gate is on. It carries no credential now,
    // and the facade is kept as defence in depth over this desk's own routing
    // — but the session id must still not be reachable through what comes
    // back, which is what this asserts.
    configureHarness({ sessionBearer: async () => 'a-secret-session-id' })
    vi.stubGlobal('fetch', async (url: unknown) => {
      // A response that knows where it came from, exactly as a browser's does.
      const real = new Response('{"ok":true}', {
        status: 200,
        headers: { 'content-type': 'application/json', 'x-echo': String(url), 'x-request-id':'req_fixture', 'retry-after':'2', 'request-id':'https://private.example' }
      })
      Object.defineProperty(real, 'url', { value: String(url), configurable: true })
      return real
    })
    const answered = await bindModelCall('openai-compatible')('chat/completions', { body: '{}' })
    const reachable = everythingReachable(answered)
    expect(answered.url).toBe('')
    expect(reachable).not.toContain('a-secret-session-id')
    expect(reachable).not.toContain('/api/assistant/relay')
    expect(reachable).not.toContain('token=')
    // What a loop does need is still there.
    expect(answered.status).toBe(200)
    expect(answered.headers.get('content-type')).toBe('application/json')
    expect(await answered.text()).toBe('{"ok":true}')
    // And the header the endpoint used to smuggle it back is not.
    expect(answered.headers.get('x-echo')).toBeNull()
    expect(answered.headers.get('x-request-id')).toBe('req_fixture')
    expect(answered.headers.get('retry-after')).toBe('2')
    expect(answered.headers.get('request-id')).toBeNull()
  })

  // Mutation row "the answer's headers are not filtered": only the two answer headers and the valid
  // diagnostic ones travel; anything else the endpoint sets, whatever its name, does not.
  it('carries the answer’s content headers and the valid diagnostic ones, and no other header', async () => {
    configureHarness({ sessionBearer: async () => 'a-secret-session-id' })
    vi.stubGlobal('fetch', async () => new Response('{}', {
      status: 200,
      headers: {
        'content-type': 'application/json',
        'content-length': '2',
        'set-cookie': 'session=abc',
        'x-auth-token': 'a-token',
        'x-request-id': 'req_fixture',
        'retry-after': 'soon, not a number or a date'
      }
    }))
    const answered = await bindModelCall('openai-compatible')('chat/completions', { body: '{}' })
    const names = [...answered.headers.keys()].sort()
    expect(names).toEqual(['content-length', 'content-type', 'x-request-id'].sort())
    expect(answered.headers.get('set-cookie')).toBeNull()
    expect(answered.headers.get('x-auth-token')).toBeNull()
    expect(answered.headers.get('retry-after')).toBeNull()
  })

  it('gives back a body stream of its own, not the one the answer arrived on', async () => {
    // A Response built from a ReadableStream keeps that very object as its
    // body, so a stream somebody decorated — a captured fetch, a patched
    // prototype — was reachable through the facade as `facade.body.leak`.
    configureHarness({ sessionBearer: async () => 'a-secret-session-id' })
    let upstream: ReadableStream<Uint8Array> | null = null
    vi.stubGlobal('fetch', async (url: unknown) => {
      const real = new Response('{"ok":true}', {
        status: 200,
        headers: { 'content-type': 'application/json' }
      })
      upstream = real.body
      // The decoration, on the stream itself.
      Object.defineProperty(real.body, 'leak', {
        value: `${String(url)}`,
        enumerable: true,
        configurable: true
      })
      return real
    })
    const answered = await bindModelCall('openai-compatible')('chat/completions', { body: '{}' })
    expect(answered.body).not.toBe(upstream)
    expect((answered.body as unknown as Record<string, unknown>).leak).toBeUndefined()
    expect(everythingReachable(answered)).not.toContain('a-secret-session-id')
    // And it is still the answer: the bytes come through the fresh stream.
    expect(await answered.text()).toBe('{"ok":true}')
  })

  it('carries no relay address through clone(), a reader, or an async iterator', async () => {
    // The walk reads `clone` as a function and never called it, so a clone's
    // own `url` and its own body were never looked at. Every door out of the
    // facade is opened here.
    configureHarness({ sessionBearer: async () => 'a-secret-session-id' })
    vi.stubGlobal('fetch', async (url: unknown) => {
      const real = new Response('{"ok":true}', {
        status: 200,
        headers: { 'content-type': 'application/json' }
      })
      Object.defineProperty(real, 'url', { value: String(url), configurable: true })
      Object.defineProperty(real.body, 'leak', {
        value: String(url),
        enumerable: true,
        configurable: true
      })
      return real
    })
    const answered = await bindModelCall('openai-compatible')('chat/completions', { body: '{}' })

    // A clone is a second Response built from the same facade: it must be as
    // empty of an address as the one it came from.
    const copy = answered.clone()
    expect(copy.url).toBe('')
    expect(everythingReachable(copy)).not.toContain('a-secret-session-id')
    expect(everythingReachable(copy)).not.toContain('/api/assistant/relay')

    // The reader, and the locked-state transition around it.
    expect(answered.body!.locked).toBe(false)
    const reader = answered.body!.getReader()
    expect(answered.body!.locked).toBe(true)
    expect(everythingReachable(reader)).not.toContain('a-secret-session-id')
    expect(everythingReachable(reader)).not.toContain('/api/assistant/relay')
    // A stream has no async iterator in this runtime unless one is defined;
    // whatever is there is walked rather than assumed absent.
    const iterator = (answered.body as unknown as Record<symbol, unknown>)[Symbol.asyncIterator]
    expect(everythingReachable(iterator)).not.toContain('a-secret-session-id')
    reader.releaseLock()

    // And the clone's own body, which is a different stream again.
    expect(copy.body).not.toBe(answered.body)
    expect((copy.body as unknown as Record<string, unknown>).leak).toBeUndefined()
    expect(await copy.text()).toBe('{"ok":true}')
  })

  it('delivers the first chunk before the last is written, and cancels upstream', async () => {
    // A facade that buffered would still pass every identity check and would
    // break streaming for every engine. And a facade that dropped the link
    // upstream would leave a relayed request running after the engine gave up.
    let write: ((chunk: string) => void) | null = null
    let cancelled: unknown = 'not cancelled'
    vi.stubGlobal('fetch', async () => {
      const upstream = new ReadableStream<Uint8Array>({
        start(controller) {
          const encoder = new TextEncoder()
          write = (chunk: string) => controller.enqueue(encoder.encode(chunk))
        },
        cancel(reason: unknown) {
          cancelled = reason
        }
      })
      return new Response(upstream, {
        status: 200,
        headers: { 'content-type': 'text/event-stream' }
      })
    })
    const answered = await bindModelCall('openai-compatible')('chat/completions', { body: '{}' })
    const reader = answered.body!.getReader()
    write!('first')
    const first = await reader.read()
    expect(new TextDecoder().decode(first.value)).toBe('first')
    // The last chunk has not been written yet, and the first is already read:
    // nothing between here and the endpoint is holding the answer.
    write!('second')
    const second = await reader.read()
    expect(new TextDecoder().decode(second.value)).toBe('second')

    // Cancelling the facade's body reaches the stream it was piped from.
    await reader.cancel('the engine stopped reading')
    await new Promise((resolve) => setTimeout(resolve, 10))
    expect(cancelled).toBe('the engine stopped reading')
  })

  it('reports an abort as a fresh AbortError, with no message or cause of its own', async () => {
    // A rejection named AbortError can carry the request URL in its message and
    // again in its cause. Rethrowing it whole handed the engine the address by
    // another door; the classification is all that travels.
    configureHarness({ sessionBearer: async () => 'a-secret-session-id' })
    vi.stubGlobal('fetch', async (url: unknown) => {
      const inner = new Error(`aborted while fetching ${String(url)}`)
      inner.name = 'AbortError'
      const outer = new Error(`aborted while fetching ${String(url)}`, { cause: inner })
      outer.name = 'AbortError'
      throw outer
    })
    const failure = (await bindModelCall('openai-compatible')('chat/completions', { body: '{}' }).catch(
      (error: unknown) => error
    )) as Error & { cause?: unknown }
    expect(failure.name).toBe('AbortError')
    expect(failure.cause).toBeUndefined()
    expect(everythingReachable(failure)).not.toContain('a-secret-session-id')
    expect(everythingReachable(failure)).not.toContain('/api/assistant/relay')
  })

  it('replaces the error a failed call throws, because a TypeError quotes the URL', async () => {
    configureHarness({ sessionBearer: async () => 'a-secret-session-id' })
    vi.stubGlobal('fetch', async (url: unknown) => {
      throw new TypeError(`Failed to fetch ${String(url)}`)
    })
    const failure = await bindModelCall('openai-compatible')('chat/completions', { body: '{}' }).catch(
      (error: unknown) => error
    )
    const reachable = everythingReachable(failure)
    expect(reachable).not.toContain('a-secret-session-id')
    expect(reachable).not.toContain('/api/assistant/relay')
    expect(failure).toMatchObject({failure:{code:'connection-lost',outcome:'unknown'}})
  })

  it('reports an abort as itself, so a loop can tell stopped from failed', async () => {
    vi.stubGlobal('fetch', async () => {
      const error = new Error('aborted')
      error.name = 'AbortError'
      throw error
    })
    const failure = await bindModelCall('openai-compatible')('chat/completions', { body: '{}' }).catch(
      (error: unknown) => error
    )
    expect((failure as Error).name).toBe('AbortError')
  })

  it('carries a body-less status without trying to give it a body', async () => {
    vi.stubGlobal('fetch', async () => new Response(null, { status: 204 }))
    const answered = await bindModelCall('openai-compatible')('chat/completions', { body: '{}' })
    expect(answered.status).toBe(204)
  })

  it.each([
    ['a boxed String', () => new String('chat/completions')],
    [
      'an object with a helpful split and a different toString',
      () => ({
        length: 4,
        split: () => ['safe'],
        toString: () => '../../../../api/assistant/probe'
      })
    ],
    [
      'an object with Symbol.toPrimitive',
      () => ({
        length: 4,
        split: () => ['safe'],
        [Symbol.toPrimitive]: () => '../../api/assistant/key'
      })
    ],
    ['a proxy over a safe string', () => new Proxy({ length: 4, split: () => ['safe'] }, {})],
    ['a Request', () => new Request('http://desk.invalid/api/assistant/probe')],
    ['a number', () => 7],
    ['null', () => null],
    ['an array of segments', () => ['chat', 'completions']]
  ])('refuses %s as a suffix, and sends nothing', async (_what, make) => {
    // The TypeScript signature says `string`; the type is not what runs. A
    // string-like object answers an innocuous `split()` while the validator is
    // looking and a different `toString()` when the URL is built.
    const { calls } = recordingFetch()
    await expect(
      bindModelCall('openai-compatible')(make() as unknown as string, { body: '{}' })
    ).rejects.toThrow(/must be a string/)
    expect(calls).toEqual([])
  })

  describe('what ends the desk’s session, and what does not (agent#37)', () => {
    const marked = (status: number, code?: string) =>
      new Response('{}', { status, headers: code === undefined ? {} : { 'X-Jpack-Desk-Refusal': code } })
    /** Install an answer, bind a call, and report what it did and whether the session was forgotten. */
    async function relayed(answer: () => Response): Promise<{ outcome: unknown; forgotten: number; discarded: number }> {
      let forgotten = 0
      let discarded = 0
      configureHarness({
        forgetSession: () => {
          forgotten += 1
        },
        discardBody: async () => {
          discarded += 1
        }
      })
      vi.stubGlobal('fetch', async () => answer())
      const outcome = await bindModelCall('openai-compatible')('chat/completions', { body: '{}' }).then(
        (answered) => answered,
        (error: unknown) => error
      )
      return { outcome, forgotten, discarded }
    }

    it('ends the session on a 401 the desk marked unauthorized, and lets go of the answer', async () => {
      const { outcome, forgotten, discarded } = await relayed(() => marked(401, 'unauthorized'))
      expect(outcome).toBeInstanceOf(NoSession)
      expect(forgotten).toBe(1)
      expect(discarded).toBe(1)
    })

    it('does not end the session on a 401 the endpoint answered, which carries no mark', async () => {
      // An expired model key: the endpoint's own 401, relayed with its status. Ending the session
      // over it would log a person out of their desk.
      const { outcome, forgotten } = await relayed(() => marked(401))
      expect(outcome).toMatchObject({ status: 401 })
      expect(outcome).not.toBeInstanceOf(Error)
      expect(forgotten).toBe(0)
    })

    it.each(['no-handoff', 'handoff-spent', 'handoff-expired'])(
      'does not end the session on a 401 the desk marked %s, which answers a question this route never asks',
      async (code) => {
        const { outcome, forgotten } = await relayed(() => marked(401, code))
        expect(outcome).toMatchObject({ status: 401 })
        expect(outcome).not.toBeInstanceOf(Error)
        expect(forgotten).toBe(0)
      }
    )

    it.each([
      ['an opaque redirect', 0, false],
      ['a status 0 that was redirected', 0, true]
    ])('reports %s as an upstream that did not answer, and hands the engine nothing', async (_what, status, redirected) => {
      // `redirect: 'manual'` gives an opaqueredirect Response: type set, status 0, no body.
      const { outcome, forgotten } = await relayed(() => {
        const answered = new Response(null, { status: 204 })
        Object.defineProperty(answered, 'status', { value: status })
        if (redirected) Object.defineProperty(answered, 'redirected', { value: true })
        else Object.defineProperty(answered, 'type', { value: 'opaqueredirect' })
        return answered
      })
      expect(outcome).toMatchObject({ failure: { code: 'connection-lost' } })
      expect(outcome).not.toHaveProperty('status')
      expect(forgotten).toBe(0)
    })
  })

  it('captures fetch when it is bound, not when it is called', async () => {
    // This is what lets the conformance session seal every network global for
    // the duration of an engine's run: the desk's capability still works, and
    // an engine that reaches for a global does not.
    const { calls } = recordingFetch()
    const call = bindModelCall('openai-compatible')
    vi.stubGlobal('fetch', () => {
      throw new Error('the engine reached for globalThis.fetch')
    })
    await call('chat/completions', { body: '{}' })
    expect(calls).toHaveLength(1)
  })
})

describe('the assistant’s own connection', () => {
  it('opens a new transport every time, never a shared one', () => {
    // A memoised transport would be one `jpack mcp` and one gate shared between
    // sessions, and closing either would take the other's connection with it.
    // Sharing the *desk's* would put the gate in front of the page's own calls.
    const first = assistantTransport(A_SESSION)
    const second = assistantTransport(A_SESSION)
    expect(first).not.toBe(second)
  })

  it('refuses to build a transport for a page with no session', () => {
    // The offer is the only place a browser lets a page put a credential on a
    // handshake, so a transport built with no id would offer nothing and be
    // refused by the chassis with a status the browser withholds. Refusing
    // here says what is actually wrong.
    expect(() => assistantTransport('')).toThrow(NoSession)
  })

  it('carries the session id in the subprotocol offer and not on the URL', () => {
    const offered: { url: string; protocols?: string[] }[] = []
    class Spy {
      constructor(url: string, protocols?: string[]) {
        offered.push({ url, protocols })
      }
      close() {}
    }
    vi.stubGlobal('WebSocket', Spy)
    void assistantTransport(A_SESSION).start()
    expect(offered).toHaveLength(1)
    expect(offered[0]?.url).not.toContain(A_SESSION)
    expect(offered[0]?.url).not.toContain('?')
    expect(offered[0]?.protocols).toEqual(['jpack-desk', `jpack-desk-session.${A_SESSION}`])
  })

  it('does not open a socket merely by being built', () => {
    // The transport opens on `start()`, which `connect` calls. A constructor
    // that dialled would mean a tab that has never run a session still holds a
    // runtime subprocess.
    const opened: string[] = []
    class Spy {
      constructor(url: string) {
        opened.push(url)
      }
    }
    vi.stubGlobal('WebSocket', Spy)
    assistantTransport(A_SESSION)
    expect(opened).toEqual([])
  })

  it('serves only the allow-listed tools, out of the runtime’s own listing', async () => {
    const runtime = await scriptedRuntime({tools:currentContractReplayTools()})
    const connection = openAssistantConnection({
      allowed: ['get_schema', 'validate'],
      onEvent: () => {},
      transport: runtime.transport
    })
    await expect(connection.ready).resolves.toMatchObject({tools:[{name:'get_schema',_meta:{operation:'read',effect:'read'}},{name:'validate',_meta:{operation:'check',effect:'pure',checkKind:'validate'}}]})
    const ready = await connection.ready
    expect(ready.tools.map((tool) => tool.name)).toEqual(['get_schema', 'validate'])
    // The definitions are the runtime's own, schema included.
    expect(ready.tools[0]!.inputSchema).toBeDefined()
    await connection.close()
    await runtime.close()
  })

  it('closes the connection it opened', async () => {
    const runtime = await scriptedRuntime({tools:currentContractReplayTools()})
    let closed = 0
    const watched: Transport = {
      start: () => runtime.transport.start(),
      close: () => {
        closed += 1
        return runtime.transport.close()
      },
      send: (message) => runtime.transport.send(message),
      set onmessage(handler: Transport['onmessage']) {
        runtime.transport.onmessage = handler
      },
      set onclose(handler: Transport['onclose']) {
        runtime.transport.onclose = handler
      },
      set onerror(handler: Transport['onerror']) {
        runtime.transport.onerror = handler
      }
    }
    const connection = openAssistantConnection({
      allowed: ['validate'],
      onEvent: () => {},
      transport: watched
    })
    await expect(connection.ready).resolves.toMatchObject({tools:[{name:'validate',_meta:{operation:'check',effect:'pure',checkKind:'validate'}}]})
    await connection.close()
    expect(closed).toBe(1)
    await runtime.close()
  })

  /**
   * A transport whose answers a test decides, one method at a time.
   *
   * The three stages a setup can be stuck at — the socket, `initialize`,
   * `tools/list` — are separately controllable, because the finding was that
   * each of them left a connection nobody could close.
   */
  function stagedTransport(behaviour: {
    initialize?: 'answer' | 'hang'
    listTools?: 'answer' | 'hang' | 'reject'
  }): { transport: Transport; closed: () => number } {
    let closed = 0
    const transport: Transport = {
      async start() {},
      async close() {
        closed += 1
        // What a real transport does, and what makes a hung `initialize`
        // reject rather than hang on after the connection is closed.
        transport.onclose?.()
      },
      async send(message) {
        const frame = message as unknown as { id?: number; method?: string }
        if (frame.method === undefined) return
        const reply = (result: unknown) =>
          queueMicrotask(() =>
            transport.onmessage?.({ jsonrpc: '2.0', id: frame.id, result } as never)
          )
        if (frame.method === 'initialize') {
          if (behaviour.initialize === 'hang') return
          reply({
            protocolVersion: '2025-06-18',
            capabilities: { tools: {} },
            serverInfo: { name: 'staged', version: '0' }
          })
          return
        }
        if (frame.method === 'tools/list') {
          if (behaviour.listTools === 'hang') return
          if (behaviour.listTools === 'reject') {
            queueMicrotask(() =>
              transport.onmessage?.({
                jsonrpc: '2.0',
                id: frame.id,
                error: { code: -32603, message: 'the runtime would not list its tools' }
              } as never)
            )
            return
          }
          reply({ tools: [] })
        }
      }
    }
    return { transport, closed: () => closed }
  }

  it('closes the transport when tools/list refuses', async () => {
    // The finding, exactly: setup threw past a connected client that nothing
    // held a reference to, so the socket — and the jpack mcp behind it —
    // stayed up for the life of the tab.
    const staged = stagedTransport({ listTools: 'reject' })
    const connection = openAssistantConnection({
      allowed: ['validate'],
      onEvent: () => {},
      transport: staged.transport
    })
    await expect(connection.ready).rejects.toThrow(/would not list its tools/)
    expect(staged.closed()).toBe(1)
  })

  it.each([
    ['initialize', { initialize: 'hang' as const }],
    ['tools/list', { listTools: 'hang' as const }]
  ])('closes the transport when %s hangs and the caller stops', async (_stage, behaviour) => {
    // Stop, and unmount, and a navigation are all this: the run aborts, and
    // the connection has to go with it even though its setup never finished.
    const staged = stagedTransport(behaviour)
    const controller = new AbortController()
    const connection = openAssistantConnection({
      allowed: ['validate'],
      onEvent: () => {},
      transport: staged.transport,
      signal: controller.signal
    })
    const settled = connection.ready.then(
      () => 'resolved',
      (error: Error) => error.name
    )
    controller.abort()
    await expect(settled).resolves.toBe('AbortError')
    // Exactly once, not merely at least once: closing twice would mean the
    // abort path and the failure path are both firing and neither knows.
    expect(staged.closed()).toBe(1)
  })

  it.each([
    ['initialize', { initialize: 'hang' as const }],
    ['tools/list', { listTools: 'hang' as const }]
  ])('closes the transport when %s hangs and close() is called directly', async (_stage, behaviour) => {
    const staged = stagedTransport(behaviour)
    const connection = openAssistantConnection({
      allowed: ['validate'],
      onEvent: () => {},
      transport: staged.transport
    })
    // The handle exists before its setup finishes, which is the whole reason
    // the shape is synchronous.
    await connection.close()
    expect(staged.closed()).toBe(1)
  })

  it('closes nothing twice, however many times it is closed', async () => {
    const staged = stagedTransport({ listTools: 'hang' })
    const connection = openAssistantConnection({
      allowed: ['validate'],
      onEvent: () => {},
      transport: staged.transport
    })
    await Promise.all([connection.close(), connection.close(), connection.close()])
    expect(staged.closed()).toBe(1)
  })

  it('refuses a connection whose signal was already aborted, and opens nothing', async () => {
    const staged = stagedTransport({})
    const controller = new AbortController()
    controller.abort()
    const connection = openAssistantConnection({
      allowed: ['validate'],
      onEvent: () => {},
      transport: staged.transport,
      signal: controller.signal
    })
    await expect(connection.ready).rejects.toThrow()
    expect(staged.closed()).toBe(1)
  })

  it('delivers nothing once the session has ended, not even the result of a call already in flight (agent#37)', async () => {
    // A transport that answers initialize and tools/list at once, and holds the one tools/call until
    // the test lets it go. Its close does not fail the pending call, which is what a result that
    // "arrived a moment too late" is.
    let release: () => void = () => {}
    let held = false
    const transport: Transport = {
      async start() {},
      async close() {},
      async send(message) {
        const frame = message as unknown as { id?: number; method?: string }
        if (frame.method === undefined) return
        const reply = (result: unknown) => queueMicrotask(() => transport.onmessage?.({ jsonrpc: '2.0', id: frame.id, result } as never))
        if (frame.method === 'initialize') {
          reply({ protocolVersion: '2025-06-18', capabilities: { tools: {} }, serverInfo: { name: 'held', version: '0' } })
        } else if (frame.method === 'tools/list') {
          reply({ tools: [{ name: 'validate', inputSchema: { type: 'object' }, _meta:{operation:'check',effect:'pure',checkKind:'validate'} }] })
        } else if (frame.method === 'tools/call') {
          held = true
          release = () => reply({ content: [{ type: 'text', text: 'the answer that came too late' }] })
        }
      }
    }
    let endSession: () => void = () => {}
    let ended: string | null = null
    configureHarness({
      sessionEnded: () => ended,
      whenSessionEnds: (listener) => {
        endSession = listener
        return () => {}
      }
    })
    const connection = openAssistantConnection({ allowed: ['validate'], onEvent: () => {}, transport })
    const ready = await connection.ready
    const pending = ready.callTool('validate', {}).then(
      (result) => result,
      (error: unknown) => error
    )
    while (!held) await new Promise((resolve) => setTimeout(resolve, 1))
    // The chassis refuses the session while the call is out.
    ended = 'the desk refused this session'
    endSession()
    release()
    const outcome = await pending
    expect(outcome).toBeInstanceOf(NoSession)
    expect(JSON.stringify(outcome)).not.toContain('too late')
    // And a call made after the end is refused before it is sent.
    await expect(ready.callTool('validate', {})).rejects.toBeInstanceOf(NoSession)
    await connection.close()
  })

  it('reports a refusal as a guardrail event and lets nothing out of the page', async () => {
    const runtime = await scriptedRuntime({tools:currentContractReplayTools()})
    const events: AssistantEvent[] = []
    const connection = openAssistantConnection({
      allowed: ['validate'],
      onEvent: (event) => events.push(event),
      transport: runtime.transport
    })
    await expect(connection.ready).resolves.toMatchObject({tools:[{name:'validate',_meta:{operation:'check',effect:'pure',checkKind:'validate'}}]})
    const ready = await connection.ready
    await expect(ready.callTool('write_file', { path: 'p' })).rejects.toThrow(/write_file/)
    expect(runtime.seen).toEqual([])
    expect(events).toEqual([
      {
        type: 'guardrail',
        tool: 'write_file',
        action: 'refused',
        detail: expect.stringContaining('write_file') as unknown as string
      }
    ])
    await connection.close()
    await runtime.close()
  })
})


describe('session response language', () => {
  it('captures the language once and preserves an explicit session choice', async () => {
    const seen: AssistantSession[] = []
    const engine: Engine = {
      id: 'vercel',
      async *start(input) {
        if ('agent' in input) throw new Error('Unexpected agent session')
        seen.push(input)
        configureHarness({ currentLanguage: () => 'de' })
        yield { type: 'end' }
      }
    }
    const input: AssistantSession = {
      prompt: 'source guidance', testPrompt: '', tools: [], hostTools: [],
      model: { family: 'gemini', model: 'model-id', call: async () => new Response('{}') },
      callTool: async () => ({ content: [] }),
      thinking: normalize('off', 'gemini'), signal: new AbortController().signal
    }
    try {
      configureHarness({ currentLanguage: () => 'fr' })
      await runAssistantSession(engine, input, () => {})
      expect(seen[0]?.replyLanguage).toBe('fr')
      expect(input.replyLanguage).toBeUndefined()
      expect(seen[0]?.model).toBe(input.model)
      expect(seen[0]?.tools).toBe(input.tools)
      await runAssistantSession(engine, { ...input, replyLanguage: 'ja' }, () => {})
      expect(seen[1]?.replyLanguage).toBe('ja')
    } finally { configureHarness({ currentLanguage: () => 'en' }) }
  })
})
