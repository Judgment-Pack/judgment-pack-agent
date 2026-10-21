import {failureError} from './failures'
import { aiHeaders, type AITarget } from './host'
import { deskHeaders } from './host'
import { currentLanguage } from './host'
/**
 * One assistant session: its own connection, its own gate, one event stream.
 *
 * **The assistant gets its own MCP connection, and this is where that is
 * decided.** The desk's one client serves the page's own calls — `list_packs`,
 * `get_pack`, the what-if pane's `experimental_evaluate` — and every one of
 * them is outside the assistant's allow-list, so the gate cannot sit on that
 * transport without breaking the desk. A second `DeskWebSocketTransport` is
 * opened instead, wrapped by the ToolGate, with its own `Client`; the chassis
 * spawns one more `jpack mcp` behind it, and it lives exactly as long as the
 * session. That is the price of a guarantee held at the wire rather than at a
 * call site, and it is in the README.
 *
 * **The guardrail events are the desk's, not an engine's.** The gate reports
 * what it did, and this module turns each notice into the ADR's `guardrail`
 * event on the same ordered sink the engine's events go to — so a refusal
 * lands between the `tool_call` that provoked it and the `tool_result` the
 * engine got back, which is the order a reader needs to make sense of either.
 */
import { Client } from '@modelcontextprotocol/sdk/client/index.js'
import type { Transport } from '@modelcontextprotocol/sdk/shared/transport.js'
import { chassisUrl } from './host'
import { DeskWebSocketTransport } from './host'
import { socketProtocols, socketURL } from './host'
import {
  NO_SESSION_MESSAGE,
  NoSession,
  discardBody,
  forgetSession,
  refusalCode,
  sessionBearer,
  sessionEnded,
  whenSessionEnds
} from './host'
import { allowedTools, gateTransport, type GuardrailNotice } from './toolGate'
import type { EndpointKind } from './host'
import type {
  AssistantEvent,
  EngineSession,
  CallTool,
  Engine,
  McpTool,
  McpToolResult,
  ModelCall,
  ModelRequest
} from './engine'

/**
 * The relay's mount point. The `v1` belongs to this route, not to any endpoint.
 */
const RELAY_PREFIX = '/api/assistant/relay/v1'

/**
 * The headers a model request may carry, mirrored from the chassis' own
 * outbound allow-list (`relayedRequestHeaders` in `internal/desk/modelrelay.go`).
 *
 * **An allow-list, for the reason the chassis gives at length**: "every inbound
 * credential is stripped" cannot be held by a list of names somebody thought of
 * — `X-Auth-Token`, `X-Amz-Security-Token`, `Ocp-Apim-Subscription-Key` and
 * whatever a gateway invents next walk straight through a denylist. This is the
 * same claim one layer earlier, so that an engine's header never reaches even
 * this desk's own route unless the protocol needs it. `Authorization`,
 * `X-Api-Key` and `Cookie` are absent rather than deleted: there is no second
 * rule to keep in step with this one.
 */
const MODEL_REQUEST_HEADERS: readonly string[] = [
  'accept',
  'accept-language',
  'content-type',
  'anthropic-version',
  'anthropic-beta',
  'openai-beta',
  'openai-organization',
  'openai-project'
]

/** Bounds the suffix, as `maxRelaySuffix` does on the chassis side. */
const MAX_SUFFIX = 256

/**
 * The methods a colon may introduce, mirrored from `relayPathMethods` in
 * `internal/desk/modelrelay.go`.
 *
 * **Closed, and held equal to the chassis' own list by a test that reads the Go
 * source** (`assistant/enforcement.test.ts`), exactly as the header lists are
 * held. The part after a colon is a *verb*: an open list would let an engine ask
 * the configured endpoint to do something nobody wrote down, with the stored
 * credential attached — and a mirror that admitted one more method than the
 * chassis would be a refusal that only ever happened on the far side.
 */
const RELAY_PATH_METHODS: readonly string[] = [
  'generateContent',
  'streamGenerateContent',
  'countTokens'
]

/**
 * The one query pair a relayed request may carry **at all**, and the kinds that
 * may carry it — mirrored from `relayStreamPair` and `relayExtraQueryPair`.
 *
 * It used to be "beside the desk's token", when a relayed request carried this
 * chassis' `?token=`. Nothing authenticates on a query now, so this literal is
 * the whole of what a relayed query may be.
 *
 * **A literal and a table, both byte for byte.** The native Gemini wire asks for
 * a server-sent-event stream with a query parameter and has nowhere else to put
 * it; the other two protocols carry streaming in the request body and admit
 * none. The chassis refuses anything else outright, for the reason it gives at
 * length: no comparison this desk can write is the comparison every parser
 * downstream makes, so exactly one fixed literal is admitted and everything else
 * is refused rather than filtered.
 */
const RELAY_STREAM_PAIR = 'alt=sse'

function relayExtraQueryPair(family: EndpointKind): string {
  return family === 'gemini' ? RELAY_STREAM_PAIR : ''
}

/**
 * The path suffix rule, mirrored from `relaySuffixProblem` — and the query rule
 * beside it, mirrored from `relayQueryProblem` and `relayExtraQueryPair`.
 *
 * One or more segments of `[A-Za-z0-9._-]`: no dot segment, no empty segment,
 * no percent sign, no backslash. It is checked **here** as well as on the
 * chassis because the point of the capability is that the engine cannot address
 * anything the desk did not agree to — a refusal that only happened on the far
 * side would be a refusal after the request left the page.
 *
 * **Two closed exceptions, and they are exactly the chassis'.** A colon may
 * introduce one of `RELAY_PATH_METHODS` after a non-empty **final** segment, the
 * way the native Gemini wire addresses a method; and the suffix may end in
 * `?alt=sse`, once, on a `gemini` endpoint and on no other. A colon anywhere
 * else, a second colon, a method outside the list, any other query, a second
 * copy of the pair, or the pair on another family is refused here and nothing is
 * sent.
 *
 * The family is the **desk's**, taken from the configured endpoint where the
 * session is bound; an engine names a suffix and never a kind, so it cannot talk
 * its way into a pair its endpoint does not admit.
 */
export function suffixProblem(suffix: unknown, family: EndpointKind): string {
  // **A primitive string, or nothing.** The signature said `string` and the
  // runtime did not check: a string-like object can answer an innocuous
  // `length` and `split()` while this function is looking and a different
  // `toString()` when the URL is built, which turns this capability into an
  // authenticated POST to another same-origin chassis route. A boxed String, a
  // proxy, a `Symbol.toPrimitive` and a `Request` are all this shape. The type
  // is not what runs.
  if (typeof suffix !== 'string') {
    return `a model call's path must be a string; this one is a ${typeof suffix}`
  }
  if (suffix === '') return `a model call must name at least one path segment after ${RELAY_PREFIX}`
  if (suffix.length > MAX_SUFFIX) {
    return `a model call's path is at most ${MAX_SUFFIX} bytes; this one is ${suffix.length}`
  }
  // **The query is split off first and compared as a whole**, byte for byte
  // against one literal, because that is the one comparison with no second
  // reading — the chassis' own argument for admitting a pair rather than
  // filtering a query. `split` bounds it at two, so `a?b?c` keeps the second
  // `?` inside the candidate pair and is refused for not being the literal.
  const [path = '', ...rest] = suffix.split('?')
  if (rest.length > 0) {
    const admitted = relayExtraQueryPair(family)
    if (rest.length > 1 || rest[0] !== admitted || admitted === '') {
      return (
        `a model call may carry no query of its own; the only pair a ${family} endpoint ` +
        `admits is ${admitted === '' ? 'none' : admitted}, once`
      )
    }
  }
  if (path === '') return `a model call must name at least one path segment after ${RELAY_PREFIX}`
  const segments = path.split('/')
  for (const [index, raw] of segments.entries()) {
    let segment = raw
    if (segment === '') return 'a model call\'s path may not carry an empty segment'
    if (segment === '.' || segment === '..') {
      return 'a model call\'s path may not carry a dot segment'
    }
    // The exception, taken at the **first** colon and only in the last segment,
    // exactly as `relaySuffixProblem` takes it: `a:b:generateContent` leaves
    // `b:generateContent` as the method, which is on no list, so a second colon
    // refuses itself and there is no arithmetic to get wrong.
    const colon = segment.indexOf(':')
    if (colon !== -1) {
      const name = segment.slice(0, colon)
      const method = segment.slice(colon + 1)
      if (index !== segments.length - 1 || name === '' || !RELAY_PATH_METHODS.includes(method)) {
        return (
          `a colon in a model call's path may only introduce one of ` +
          `${RELAY_PATH_METHODS.join(', ')}, after a non-empty final segment`
        )
      }
      segment = name
    }
    if (!/^[A-Za-z0-9._-]+$/.test(segment)) {
      return (
        `a model call's path segments are letters, digits, ".", "_" and "-"; ` +
        `${JSON.stringify(segment)} is not one`
      )
    }
  }
  return ''
}

/** Response content metadata plus bounded diagnostics. Routing, cookies and
 * credentials never enter the model adapter. Retry timing and request IDs are
 * necessary for the shared error contract and survive the response facade. */
const MODEL_ANSWER_HEADERS: readonly string[] = ['content-type', 'content-length']
const MODEL_DIAGNOSTIC_HEADERS: readonly string[] = ['request-id','x-request-id','retry-after']

/** Fixed cancellation text, with no routing details. */
const CALL_ABORTED = 'the model request was aborted'

/**
 * A model call this session may make, bound by the desk.
 *
 * Four things happen here that an engine must not be trusted to do:
 *
 * - **the address is built here**, out of the mount point and a suffix this
 *   function validated;
 * - **the answer is a facade**, constructed by this desk. A browser `Response`
 *   carries the requested URL on `.url`, no `redirected` history is kept, and
 *   only the headers this desk copied onto it travel. What that withholds is
 *   *this desk's routing* rather than a secret — since the session became an id
 *   the page holds and sends on a header, there is no credential in any address
 *   for an engine to read, and an engine that constructed `/ws` would still have
 *   to present that id. So the facade is defence in depth and is stated as
 *   that: what an engine is *handed* is held by the member set in
 *   `enforcement.test.ts`, and what an engine *reaches for* is caught by the
 *   conformance suite, whose sealed globals make that a failing test rather than
 *   something the running desk prevents. The same reasoning covers the failure
 *   path: the error is replaced, because a fetch `TypeError` quotes the URL and
 *   an engine is handed no address;
 * - **the headers are an allow-list**, in both directions, so nothing
 *   resembling a credential travels even as far as this desk's own route and
 *   nothing but the protocol's own comes back;
 * - **`fetch` is captured when the session is bound**, not read at call time,
 *   so the conformance session can replace every network global with a
 *   throwing sentinel for the whole of an engine's leg. An engine that reaches
 *   for one fails; this call still works.
 *
 * `family` is the configured endpoint's wire protocol, and it is a **parameter
 * of the binding rather than of the call**: it decides whether the one query
 * pair the relay admits may travel, and an engine that could name it would be
 * an engine choosing what its endpoint admits. It is required rather than
 * defaulted, because a default is a grant nobody wrote down.
 */
export function bindModelCall(family: EndpointKind, target?:AITarget): ModelCall {
  const send = globalThis.fetch.bind(globalThis)
  const boundDeskHeaders=deskHeaders()
  return async (suffix: string, request: ModelRequest): Promise<Response> => {
    const problem = suffixProblem(suffix, family)
    if (problem !== '') throw new Error(problem)
    // **The desk's own session, on the desk's own route.** This is a gated
    // chassis endpoint like any other, so it carries the bearer the page holds
    // — and it did not, which meant every model listing and every generation
    // turn answered 401 the moment the session stopped being a cookie. It is
    // awaited per call rather than captured at bind time, so a capability bound
    // before the page finished its exchange waits for the id instead of binding
    // an empty one; after the exchange it resolves from memory. A page with no
    // session throws here and **sends nothing**, which is what makes the
    // no-session state terminal for the assistant too.
    const id = await sessionBearer()
    const headers: Record<string, string> = {}
    for (const [name, value] of Object.entries(request.headers ?? {})) {
      if (MODEL_REQUEST_HEADERS.includes(name.toLowerCase())) headers[name] = value
    }
    // **The desk builds the address, pair included.** The suffix has already
    // been held to the chassis' own rule, so what is split here is a path and at
    // most the one admitted literal — which is now the *whole* of what a relayed
    // query may be, since nothing authenticates on a query any more.
    const [path = '', pair] = suffix.split('?')
    const extra = pair === undefined ? {} : Object.fromEntries([pair.split('=') as [string, string]])
    let answered: Response
    try {
      answered = await send(chassisUrl(`${RELAY_PREFIX}/${path}`, extra), {
        // **`omit`, and the bearer written on**, exactly as `deskFetch` does.
        // Nothing ambient authorizes anything on this desk, and the launch
        // handoff — the one cookie there is — belongs on the exchange and on no
        // other request.
        credentials: 'omit',
        // **A model API never redirects the browser relaying to it.** An
        // endpoint answering `307 Location: /api/session` made this very fetch
        // repeat the request — method, body and this desk's bearer included —
        // against the exchange, whose marked refusal the classifier below then
        // read as "this desk refused my session". The chassis strips `Location`
        // now; this is the second layer, and it is the one that holds if a
        // redirect ever reaches here by another route.
        redirect: 'manual',
        // `POST` unless the caller named the one other method this capability
        // admits. A `GET` carries no body: `fetch` refuses one that does, and
        // the model listing is the only caller that asks for either.
        method: request.method ?? 'POST',
        headers: { ...headers, ...boundDeskHeaders, ...aiHeaders(target?.connectionId,target?.connectionRevision), Authorization: `Bearer ${id}` },
        body: request.method === 'GET' ? undefined : request.body,
        signal: request.signal
      })
    } catch (cause) {
      // **An abort is reported as an abort and never as itself.** A loop has to
      // be able to tell "stopped" from "failed", so the classification travels
      // — and nothing else does: a rejection named `AbortError` can carry the
      // request URL in its own message and again in its `cause`, and rethrowing
      // it whole handed the engine the address by another door. A fresh
      // `DOMException` carries the name, the fixed sentence, and no cause.
      if ((cause as Error)?.name === 'AbortError') {
        throw new DOMException(CALL_ABORTED, 'AbortError')
      }
      throw failureError('connection-lost','model')
    }
    // **An opaque redirect is not an answer.** `redirect: 'manual'` gives a
    // `Response` of type `opaqueredirect` with status 0 and no body, and an
    // engine handed that would be handed nothing it could read — so it is
    // reported as what it is, an upstream that did not answer.
    if (answered.type === 'opaqueredirect' || (answered.status === 0 && answered.redirected)) {
      throw failureError('connection-lost','model')
    }
    // **A 401 is two different things on this one route, and one header tells
    // them apart.** Every other chassis call has a single meaning for a 401 —
    // this desk refused the session — but the relay forwards the *endpoint's*
    // status verbatim, and an endpoint that does not accept the stored key
    // answers 401 too. Ending the page's session over that would log somebody
    // out of their desk because their model key expired.
    //
    // The chassis marks every refusal it authors with `X-Jpack-Desk-Refusal`
    // and **strips that header from every upstream answer**, so its presence is
    // the discriminator and nothing an endpoint sends can wear it. What this
    // replaces read a *cloned body* to decide, which was wrong twice over: a
    // clone has tee semantics, so an oversized chunked refusal deadlocked the
    // reader classifying it, and an endpoint could write the envelope itself.
    //
    // **And the code, not just the mark.** The exchange's own codes —
    // `no-handoff`, `handoff-spent`, `handoff-expired` — are answers to a
    // question this route never asks, and a classifier that ended the session
    // on any marked 401 turned an upstream redirect into a forgotten valid
    // session.
    if (answered.status === 401 && refusalCode(answered) === 'unauthorized') {
      // Nothing reads this answer now, so the request is let go of rather than
      // left in flight behind an unconsumed stream. See `discardBody`.
      await discardBody(answered)
      forgetSession()
      throw new NoSession()
    }
    return facade(answered)
  }
}

/**
 * The answer the engine gets: this desk's, built from the endpoint's.
 *
 * A body with no `url`, no `redirected`, and a filtered header copy. The status
 * and the reason phrase travel because a loop has to be able to tell a refusal
 * from an answer.
 *
 * **The body is a fresh stream, not the one the answer arrived on.** A
 * `Response` built from a `ReadableStream` keeps that very object as its body,
 * so a stream carrying a property somebody put on it — a captured `fetch`, a
 * patched prototype — was reachable through the facade as `facade.body.leak`.
 * Piping through an identity `TransformStream` yields a different object whose
 * only content is the bytes. A body-less answer stays body-less: `null` is not
 * a stream and the constructor refuses one on these statuses anyway.
 */
function facade(answered: Response): Response {
  const carried = new Headers()
  answered.headers.forEach((value, name) => {
    if (MODEL_ANSWER_HEADERS.includes(name.toLowerCase())) carried.set(name, value)
    else if(MODEL_DIAGNOSTIC_HEADERS.includes(name.toLowerCase())&&value.length<=128){
      const valid=name.toLowerCase()==='retry-after'? /^\d+(?:\.\d+)?$/.test(value)||Number.isFinite(Date.parse(value)):/^[-a-zA-Z0-9_]{1,128}$/.test(value)
      if(valid)carried.set(name,value)
    }
  })
  // A body is not allowed on these statuses, and the constructor throws rather
  // than ignoring one.
  const empty = answered.status === 204 || answered.status === 205 || answered.status === 304
  const body =
    empty || answered.body === null ? null : answered.body.pipeThrough(new TransformStream())
  return new Response(body, {
    status: answered.status,
    statusText: answered.statusText,
    headers: carried
  })
}

/**
 * The transport this session runs on: **a new one, every time**, carrying the
 * session id in its subprotocol offer.
 *
 * `id` is a parameter rather than something read here, and that is the whole of
 * why: the page has **one** actor that holds the credential, and a second
 * reader of it is a second thing that can be wrong about which id is current.
 * The caller awaits `bootstrap()` and passes what it resolved.
 *
 * Not a shared or memoised transport. Two sessions sharing a transport would share a
 * `jpack mcp` and a gate, and closing either would take the other's connection
 * with it; sharing the *desk's* would put the gate in front of the page's own
 * calls. The guarantee is one connection per session, and it is held by this
 * function returning a fresh object.
 */
export function assistantTransport(id: string): Transport {
  if (id === '') throw new NoSession()
  return new DeskWebSocketTransport(socketURL(), socketProtocols(id))
}

type NoticeGuard = (event: AssistantEvent) => AssistantEvent | undefined
const noticeGuards = new WeakMap<CallTool, NoticeGuard>()
/** Bind delivery to this run's output policy without changing the gate's verdict. */
export function guardConnectionNotices(call: CallTool, guard: NoticeGuard): () => void {
  noticeGuards.set(call, guard)
  return () => { if (noticeGuards.get(call) === guard) noticeGuards.delete(call) }
}

/** What a connection is once it has finished setting itself up. */
export interface AssistantConnectionReady {
  /** The allow-listed tools, exactly as `tools/list` served them. */
  tools: McpTool[]
  /** Bound through the gate. The only thing an engine is handed. */
  callTool: CallTool
}

/**
 * A connection, **owned from the instant it is asked for**.
 *
 * The handle is returned synchronously and `ready` settles later, which is the
 * whole point: `close()` exists before `initialize` has been answered, so a
 * caller can release a connection whose setup is still in flight. The shape
 * this replaces returned a promise, and a caller could not store anything until
 * it resolved — so a `tools/list` that hung or rejected left a socket open, a
 * `jpack mcp` running, and nothing anywhere holding a reference to either.
 */
export interface AssistantConnection {
  ready: Promise<AssistantConnectionReady>
  /** Idempotent, and safe at any point in the setup. */
  close(): Promise<void>
}

/**
 * Open the assistant's connection, gated.
 *
 * `transport` is injectable so a test — the conformance session included — can
 * drive the whole path, gate and client and all, without a socket. Nothing in
 * the page passes it; the page passes `sessionId` instead, and a caller that
 * passes neither is refused with `NoSession` rather than opening a socket that
 * offers nothing. `signal` is the run's; an abort closes whatever exists
 * and rejects `ready`, rather than leaving a setup running against a session
 * nobody is watching any more.
 */
export function openAssistantConnection(options: {
  allowed: readonly string[]
  onEvent: (event: AssistantEvent) => void
  transport?: Transport
  /**
   * The session id this connection's upgrade offers, from the caller's own
   * `await bootstrap()`. Ignored where `transport` is given, which is how the
   * conformance suite drives this path without a socket.
   */
  sessionId?: string
  signal?: AbortSignal
}): AssistantConnection {
  const allowed = allowedTools(options.allowed)
  const raw = options.transport ?? assistantTransport(options.sessionId ?? '')
  let callTool: CallTool
  const notify = (notice: GuardrailNotice) => {
    const event: AssistantEvent = {
      type: 'guardrail',
      tool: notice.tool,
      action: notice.action,
      detail: notice.detail
    }
    const guard = noticeGuards.get(callTool)
    const delivered = guard ? guard(event) : event
    if (delivered) options.onEvent(delivered)
  }
  const gated = gateTransport(raw, { allowed, onGuardrail: notify })
  const client = new Client({ name: 'judgment-pack-desk-assistant', version: '0.1.0' }, { capabilities: {} })

  let connected = false
  let shutting: Promise<void> | null = null
  /**
   * The session's end, once, and it closes this connection.
   *
   * **A socket that is already open notices nothing.** A `401` is met by the
   * caller that made the request; a connection established before the refusal
   * went on carrying frames for a session the chassis had refused — and this
   * one is the *gated* connection an engine drives, so it went on calling
   * tools. The one subscription in `mcp/session.ts` is what reaches it.
   */
  let ended = sessionEnded()
  const stopWatching = whenSessionEnds(() => {
    ended = sessionEnded() ?? NO_SESSION_MESSAGE
    void close()
  })
  /**
   * Close once, and never reject.
   *
   * The client's own `close` reaches the transport it was connected with, so
   * once `connect` has returned that is the whole of it. Before then the client
   * has adopted nothing this can rely on — and a transport that was started and
   * never adopted is exactly the socket this finding was about — so the raw one
   * is closed directly. Either way a real transport's `onclose` reaches the
   * client, which is what makes a hung `initialize` reject rather than hang on.
   *
   * Never rejects: a caller closing a connection has nothing left to do about a
   * failure to close it.
   */
  const close = (): Promise<void> => {
    shutting ??= (async () => {
      stopWatching()
      try {
        await (connected ? client.close() : raw.close())
      } catch {
        /* the socket is going away regardless */
      }
    })()
    return shutting
  }

  const aborted = () => {
    const error = new Error('the assistant connection was closed before it was ready')
    error.name = 'AbortError'
    return error
  }

  const ready = (async (): Promise<AssistantConnectionReady> => {
    // A run started after the session ended opens nothing at all.
    if (ended !== null) {
      await close()
      throw new NoSession(ended)
    }
    // An abort at any point closes what exists **and settles this promise**.
    // Closing alone is not enough: a transport that answers nothing answers a
    // close with nothing either, and `ready` would hang for the life of the
    // page with the caller still waiting on it.
    let onAbort = () => {}
    const stopped = new Promise<never>((_resolve, reject) => {
      onAbort = () => {
        void close()
        reject(aborted())
      }
    })
    // Nobody awaits `stopped` unless the race below does; without this an abort
    // on a completed setup would be an unhandled rejection.
    void stopped.catch(() => undefined)
    options.signal?.addEventListener('abort', onAbort, { once: true })
    try {
      if (options.signal?.aborted) throw aborted()
      await Promise.race([client.connect(gated), stopped])
      connected = true
      if (options.signal?.aborted) throw aborted()
      const listed = ((await Promise.race([client.listTools(), stopped])).tools ?? []) as McpTool[]
      if (options.signal?.aborted) throw aborted()
      // The tools the model is offered are the ones the runtime served,
      // filtered to what this desk's file granted — never a definition written
      // here.
      const tools = listed.filter((tool) => allowed.has(tool.name))
      return {
        tools,
        // The SDK's own result type is wider than the contract's — it carries
        // the task and meta members this desk never reads — so it is narrowed
        // here, once, rather than at each engine.
        //
        // **And it delivers nothing once the session has ended.** Closing the
        // socket is not on its own enough: a call already queued would reach a
        // closed transport and fail with whatever the SDK says about that,
        // which tells an engine nothing about why. This is the gate saying so.
        callTool: callTool = async (name, args) => {
          // **Checked twice, and the second one is the point.** A call that
          // was already in flight when the session ended resolves afterwards,
          // and delivering that result would be this desk handing an engine
          // the answer to a question it asked on a session the chassis had
          // refused. Nothing is delivered after the end — not a result that
          // arrived a moment too late, and not one that was queued a moment
          // too early.
          if (ended !== null) throw new NoSession(ended)
          const answered = (await client.callTool({ name, arguments: args })) as McpToolResult
          if (ended !== null) throw new NoSession(ended)
          return answered
        }
      }
    } catch (cause) {
      // **Nothing is left open on a failure.** A `tools/list` that answered
      // with a JSON-RPC error used to throw straight out of here, past a
      // connected client nobody held a reference to.
      await close()
      throw cause
    } finally {
      options.signal?.removeEventListener('abort', onAbort)
    }
  })()
  // A `ready` nobody awaits — an abort during setup — must not be an unhandled
  // rejection. The caller still gets the rejection when it awaits.
  void ready.catch(() => undefined)

  return { ready, close }
}

/**
 * Drive one engine, and put its events on the sink in the order they happen.
 *
 * The loop is `for await` and nothing else: an engine that never ends is
 * bounded by its own `maxTurns` and by the session's signal, and this does not
 * second-guess either.
 */
export async function runAssistantSession(
  engine: Engine,
  session: EngineSession,
  onEvent: (event: AssistantEvent) => unknown
): Promise<void> {
  const localizedSession = { ...session, replyLanguage: session.replyLanguage ?? currentLanguage() }
  for await (const event of engine.start(localizedSession)) await onEvent(event)
}
