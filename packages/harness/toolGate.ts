import {AIFailureError,failure} from './failures'
/**
 * The ToolGate: the desk's promises about what the assistant may reach, held
 * **on the wire** rather than anywhere an engine could be wrong.
 *
 * ADR-0001 puts this below the engine slot deliberately. Every outbound
 * `tools/call` frame is checked by name against the session's allow-list and
 * refused otherwise; `experimental_evaluate` is rewritten to carry
 * `rehearsal: true` before the frame leaves the page. A loop that asked for a
 * tool it was never offered, an SDK retry, a second caller nobody has written
 * yet — each is stopped at the same place, because the place is the transport
 * and not a branch in whatever is running above it.
 *
 * **This is a `Transport`, not a wrapper around `callTool`.** The bake-off
 * measured both layers and reported that only this one survives the loop above
 * it being wrong: `Client`'s public surface offers no outbound middleware over
 * a WebSocket (`setRequestHandler` serves inbound requests; the fetch
 * middleware is for HTTP transports), so a gate on the call site is a gate one
 * `client.callTool` away from being bypassed. There is exactly one door and it
 * is `send`.
 *
 * **The allow-list is the configured subset, intersected with the six.** The
 * desk's closed list (`ASSISTANT_TOOLS`) is the ceiling and `endpoint.tools`
 * is what this desk's file granted; a name in neither cannot be reached by
 * writing a longer list somewhere else.
 *
 * **One tool's arguments are allow-listed as well** (agent#7):
 * `experimental_test_cases` leaves only with exactly the two members the
 * Runtime's schema names, `pack` and `matrix`, each JSON text within the
 * policy's bytes (`testPackBytes`, `testMatrixBytes`). It takes pack text and a
 * matrix and reads no project file, which is why it is the one test tool on the
 * ceiling; any other member, a path among them, is refused on the wire. The
 * Runtime tools kept off the ceiling, and why, are `REFUSED_RUNTIME_TOOLS`.
 *
 * **Order is allow-list, then rewrite**, and it matters in one case only:
 * `experimental_evaluate` that the file did not grant is *refused*, not
 * rewritten and sent. Rewriting first would turn a tool nobody granted into a
 * tool this desk politely corrected on its way out.
 *
 * `inspectOutboundFrame` is exported on its own so a test can call it with a
 * frame and no socket — the prototype's `assertOutboundFrame`, kept as a pure
 * function for exactly that reason.
 */
import type { Transport } from '@modelcontextprotocol/sdk/shared/transport.js'
import { JSONRPCMessageSchema } from '@modelcontextprotocol/sdk/types.js'
import type { JSONRPCMessage } from '@modelcontextprotocol/sdk/types.js'
import { ASSISTANT_TOOLS } from './host'
import { CONTEXT_DEFAULTS, utf8Bytes, type TestBounds } from './context/policy'

/** The one method this gate has an opinion about. */
export const TOOLS_CALL = 'tools/call'

/** The one tool whose arguments this gate rewrites, and the member it writes. */
export const REHEARSAL_TOOL = 'experimental_evaluate'
export const REHEARSAL_MEMBER = 'rehearsal'

/**
 * The Runtime's test tool the ceiling admits (agent#7), and the members its
 * arguments may carry: exactly the two its served schema names, each JSON text.
 */
export const TEST_TOOL = 'experimental_test_cases'
export const TEST_ARGUMENTS = ['pack', 'matrix'] as const

/** The policy's byte bounds on the test tool's two members. */
export type TestArgumentBounds = Pick<TestBounds, 'testPackBytes' | 'testMatrixBytes'>

/**
 * The Runtime tools that stay off the ceiling, each with its reason.
 *
 * Nothing here is a rule: the ceiling is a closed list, so a name not on it is
 * refused whatever this says. It is written down where the ceiling is applied
 * because each of these was proposed for it (agent#7) and the reason it was
 * refused is the part a later proposal has to answer.
 */
export const REFUSED_RUNTIME_TOOLS = {
  test_conformance:
    'its suite argument is "a local suite directory or manifest.json", a path the runtime reads on the machine ' +
    'that serves it: admitting it would give the model a file read, and the gate checks names, not paths',
  experimental_test_packs:
    'it tests the saved project tree, not the draft the model is proposing, and saved suites are not held by the ' +
    'rehearsal rule that holds experimental_evaluate',
  experimental_test_graphs:
    'it tests the saved graphs, not the draft, under the same rule as experimental_test_packs',
  get_pack: 'it reads the saved project tree; the draft travels in the arguments of the tools that test it',
  list_packs: 'it lists the saved project tree, for the same reason as get_pack'
} as const satisfies Record<string, string>

/**
 * A call the gate would not let out of the page.
 *
 * It carries the tool by name because the caller has to be able to say which
 * call was refused without reading a sentence back apart, and because the
 * engine turns this into a result the model is told about.
 */
export class GateViolation extends AIFailureError {
  readonly tool: string

  constructor(tool: string, message: string) {
    super({...failure('permission','tool','not-started'),detail:message})
    this.name = 'GateViolation'
    this.tool = tool
  }
}

/**
 * One thing the gate did, in the ADR's own vocabulary.
 *
 * `detail` says what was there and never what the pack said: a rewrite's
 * detail names the `rehearsal` member's previous value and nothing else, so a
 * guardrail line in the pane cannot become a place a draft leaks into a log.
 */
export interface GuardrailNotice {
  tool: string
  action: 'rewrote' | 'refused'
  detail: string
}

/**
 * What `inspectOutboundFrame` decided about one frame.
 *
 * **There is no `pass`.** Every frame that leaves is the canonical one this
 * gate built, never the object it was handed: a decision made about an object
 * and then applied to that same object is a decision the object can change
 * afterwards. `notice` is null wherever nothing needed reporting.
 */
export type FrameVerdict =
  | { verdict: 'send'; notice: GuardrailNotice | null; frame: JSONRPCMessage }
  | { verdict: 'refused'; notice: GuardrailNotice; violation: GateViolation }

/**
 * The allow-list for a session: what the file granted, intersected with the
 * six the desk will ever serve.
 *
 * The intersection is not defensive tidying. The Desk's `decodeDeskConfig`
 * refuses a name outside its own list by name, so a file that reaches here
 * cannot carry a name beyond it — but this function is also called with a list a
 * test wrote, and a gate whose
 * ceiling depends on a decoder somewhere else having run is a gate with a
 * premise instead of a rule.
 */
export function allowedTools(configured: readonly string[]): ReadonlySet<string> {
  const ceiling = new Set<string>(ASSISTANT_TOOLS)
  return new Set(configured.filter((name) => ceiling.has(name)))
}

/** A plain object, and nothing else: not an array, not null, not a primitive. */
function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

/**
 * One value as **plain JSON data**, or nothing.
 *
 * `JSON.stringify` then `JSON.parse` is the whole of it, and every property of
 * that round trip is load-bearing here:
 *
 * - it invokes `toJSON` **once**, where there is one, so the value this gate
 *   inspects is the value a serializer would produce rather than the object
 *   standing in front of it. An enumerable `toJSON` copied onto a rebuilt
 *   arguments object is what defeated the previous rewrite: the rebuild carried
 *   `rehearsal: true` and the wire did not;
 * - it reads every getter **once**, so a property cannot answer the check and
 *   the wire differently;
 * - it drops functions, accessors, symbol keys and the prototype, so what comes
 *   out has no behaviour left to surprise anybody with;
 * - and it throws on a cycle, which is a refusal rather than an exception
 *   thrown at the socket.
 *
 * Undefined is returned for anything that does not survive — a cycle, a
 * `BigInt`, a bare `undefined` — and every caller treats that as a refusal.
 */
function canonical(value: unknown): unknown {
  let text: string | undefined
  try {
    text = JSON.stringify(value)
  } catch {
    return undefined
  }
  if (text === undefined) return undefined
  try {
    return JSON.parse(text) as unknown
  } catch {
    return undefined
  }
}

/**
 * Why the test tool's arguments may not leave, or `''` where they may.
 *
 * The sentence names members and sizes and never a value: the pack and the
 * matrix travel in these members and neither is ever quoted into a notice. A
 * member the model invented is named, cut to sixty-four characters, at most
 * five of them.
 */
function testArgumentsProblem(supplied: unknown, bounds: TestArgumentBounds): string {
  const args = (supplied ?? {}) as Record<string, unknown>
  const allowed = new Set<string>(TEST_ARGUMENTS)
  const extra = Object.keys(args).filter((member) => !allowed.has(member))
  if (extra.length > 0) {
    const named = extra.slice(0, 5).map((member) => JSON.stringify(member.slice(0, 64))).join(', ')
    return (
      `${TEST_TOOL} takes exactly ${TEST_ARGUMENTS.join(' and ')}, and this call also carried ${named}` +
      `${extra.length > 5 ? ' and more' : ''}: no path or other member reaches the runtime through this tool`
    )
  }
  const limits: Record<(typeof TEST_ARGUMENTS)[number], [number, string]> = {
    pack: [bounds.testPackBytes, 'testPackBytes'],
    matrix: [bounds.testMatrixBytes, 'testMatrixBytes']
  }
  for (const member of TEST_ARGUMENTS) {
    const value = args[member]
    if (typeof value !== 'string') {
      return `${TEST_TOOL} takes ${member} as JSON text, and this call's ${member} is ${value === undefined ? 'missing' : 'not a string'}`
    }
    const [limit, policy] = limits[member]
    const bytes = utf8Bytes(value)
    if (bytes > limit) {
      return `${TEST_TOOL}'s ${member} is ${bytes} bytes, over the ${limit} the policy allows (${policy})`
    }
  }
  return ''
}

/** The refusal every malformed frame gets, in one place. */
function refuse(tool: string, detail: string): FrameVerdict {
  return {
    verdict: 'refused',
    notice: { tool, action: 'refused', detail },
    violation: new GateViolation(tool, `refused on the wire: ${detail}`)
  }
}

/**
 * What must happen to one outbound frame before it may leave the page.
 *
 * **The frame is canonicalized to bytes before anything is decided, and what
 * leaves is the canonical frame.** That is the whole shape of this function,
 * and it is the lesson the chassis' relay learned over four review rounds: a
 * classification made about a mutable object is a classification the object can
 * change out from under you. A `method` getter can answer `undefined` while the
 * gate is looking and `tools/call` while `JSON.stringify` is; a response-shaped
 * object with a `toJSON` can serialize as a request. So the object is serialized
 * once, the bytes are parsed back to plain data, and every rule below is applied
 * to that plain data — which has no getters, no `toJSON`, no functions, no
 * symbol keys and no prototype left to disagree with.
 *
 * What is checked, in order:
 *
 * 1. the frame survives a JSON round trip at all (a cycle does not) and is a
 *    plain object;
 * 2. it satisfies the **SDK's own `JSONRPCMessageSchema`** — a request, a
 *    notification, a response or an error, each whole. That is the check this
 *    replaces a hand-written one with, and the reason is that the hand-written
 *    one was overclaimed: `{"id": null, "result": 7}`, an `error` that is a
 *    string, and a fractional id all satisfied "an id and exactly one of result
 *    and error" and none of them is a JSON-RPC message. The schema is the same
 *    one the client validates *inbound* frames against, so what this desk will
 *    send is what its own SDK will accept;
 * 3. a method that is not `tools/call` but is a spelling of it to some other
 *    reader — `Tools/Call`, ` tools/call ` — is refused, on the chassis' own
 *    reasoning about its query;
 * 4. for `tools/call`: the tool is on the session's allow-list,
 *    `experimental_test_cases` carries exactly `pack` and `matrix` within the
 *    policy's bytes, and `experimental_evaluate` gets an own `rehearsal: true`
 *    written last onto the canonical arguments.
 */
export function inspectOutboundFrame(
  message: JSONRPCMessage,
  allowed: ReadonlySet<string>,
  bounds: TestArgumentBounds = CONTEXT_DEFAULTS
): FrameVerdict {
  const unreadable = '(a frame this gate could not read)'
  const frame = canonical(message)
  if (!isRecord(frame)) {
    return refuse(
      unreadable,
      Array.isArray(frame)
        ? 'a JSON-RPC batch is not a frame this gate can check one call at a time, so it is ' +
            'refused whole; nothing left the page'
        : 'an outbound frame must be a single JSON-RPC object that survives serialization; ' +
            'this one is not, and nothing left the page'
    )
  }
  // **The SDK's own schema, not this desk's reading of the spec.** A request,
  // a notification, a response or an error, each whole — ids that are strings
  // or integers, results that are objects, errors with an integer code and a
  // string message. Anything else is refused before a rule of this desk's is
  // applied to it.
  if (!JSONRPCMessageSchema.safeParse(frame).success) {
    return refuse(
      unreadable,
      'an outbound frame must be a JSON-RPC message the SDK itself would accept — a request, ' +
        'a notification, a response or an error, each whole; this one is not, and nothing left ' +
        'the page'
    )
  }

  const method = frame.method
  if (method === undefined) {
    // A response or an error. The schema above has already established which,
    // and that it is complete.
    return { verdict: 'send', notice: null, frame: frame as unknown as JSONRPCMessage }
  }
  if (typeof method !== 'string') {
    return refuse(unreadable, "an outbound frame's method must be a string")
  }

  if (method !== TOOLS_CALL) {
    // Fail closed on a spelling that is `tools/call` to some other reader.
    if (method.trim().toLowerCase() === TOOLS_CALL) {
      return refuse(
        unreadable,
        `${JSON.stringify(method)} is a spelling of ${TOOLS_CALL} this desk will not send: a ` +
          `frame two readers disagree about is one it cannot check`
      )
    }
    if ('params' in frame && !isRecord(frame.params)) {
      return refuse(unreadable, "a frame's params must be an object where it has any")
    }
    return { verdict: 'send', notice: null, frame: frame as unknown as JSONRPCMessage }
  }

  const params = frame.params
  if (!isRecord(params)) {
    return refuse(unreadable, `a ${TOOLS_CALL} frame must carry a params object`)
  }
  const name = params.name
  if (typeof name !== 'string' || !allowed.has(name)) {
    const spelled = typeof name === 'string' ? name : String(name)
    return refuse(
      spelled,
      `${spelled} is not one of the tools this assistant may call ` +
        `(${[...allowed].join(', ') || 'none'}); the call did not leave the page and ` +
        `nothing was written`
    )
  }
  const supplied = params.arguments
  if (supplied !== undefined && !isRecord(supplied)) {
    return refuse(name, `a ${TOOLS_CALL} frame's arguments must be an object where it has any`)
  }

  if (name === TEST_TOOL) {
    const problem = testArgumentsProblem(supplied, bounds)
    if (problem !== '') return refuse(name, `${problem}; the call did not leave the page`)
  }

  if (name !== REHEARSAL_TOOL) {
    return { verdict: 'send', notice: null, frame: frame as unknown as JSONRPCMessage }
  }

  /**
   * **The rehearsal member is written onto the canonical data, last.**
   *
   * The arguments here have already been through the round trip with the rest
   * of the frame, so there is no `toJSON` left to invoke at serialization, no
   * getter left to re-read, and no inherited property that reads as `true` and
   * serializes as nothing. What is set is an own, plain `true` on plain data,
   * and that plain data is what the transport is handed.
   */
  const args = (supplied ?? {}) as Record<string, unknown>
  const already = Object.hasOwn(args, REHEARSAL_MEMBER) && args[REHEARSAL_MEMBER] === true
  const had = Object.hasOwn(args, REHEARSAL_MEMBER)
    ? `the call carried ${REHEARSAL_MEMBER}: ${JSON.stringify(args[REHEARSAL_MEMBER])}`
    : `the call carried no ${REHEARSAL_MEMBER} member that survived serialization`
  args[REHEARSAL_MEMBER] = true
  frame.params = { ...params, arguments: args }
  if (already) {
    // Nothing to report: the caller asked for exactly what it got. The frame is
    // still the canonical one, because canonical is what travels.
    return { verdict: 'send', notice: null, frame: frame as unknown as JSONRPCMessage }
  }
  // What was there, and only that. The pack and the facts travel in the same
  // object and neither is ever named here.
  return {
    verdict: 'send',
    notice: {
      tool: name,
      action: 'rewrote',
      detail:
        `${had}; it was rewritten to an own ${REHEARSAL_MEMBER}: true before the frame left ` +
        `the page, so the runtime appends no audit record (runtime ADR-0018, ADR-0028)`
    },
    frame: frame as unknown as JSONRPCMessage
  }
}

/**
 * Wrap a transport so nothing reaches the socket unexamined.
 *
 * `send` is replaced on the object it was handed rather than a proxy being
 * returned around it, because the SDK `Client` keeps its own reference to the
 * transport it was connected with: a decorator the client never sees is a gate
 * on a path nothing uses. The wrapped transport is returned as well, so a
 * caller reads as though it were a wrapper.
 *
 * A refusal **rejects** rather than dropping the call, so the caller learns
 * that it happened; the engine turns that rejection into a result the model
 * gets, which is what keeps a refused turn from being re-emitted for ever.
 */
export function gateTransport(
  inner: Transport,
  options: {
    allowed: ReadonlySet<string>
    onGuardrail?: (notice: GuardrailNotice) => void
    /** The test tool's byte bounds; the policy's (`CONTEXT_DEFAULTS`) where absent. */
    bounds?: TestArgumentBounds
  }
): Transport {
  const send = inner.send.bind(inner) as (
    message: JSONRPCMessage,
    sendOptions?: unknown
  ) => Promise<void>
  inner.send = (message: JSONRPCMessage, sendOptions?: unknown): Promise<void> => {
    const decided = inspectOutboundFrame(message, options.allowed, options.bounds)
    if (decided.verdict === 'refused') {
      options.onGuardrail?.(decided.notice)
      return Promise.reject(decided.violation)
    }
    if (decided.notice !== null) options.onGuardrail?.(decided.notice)
    // **The canonical frame, always.** Never the caller's object: what was
    // checked and what is sent have to be the same bytes.
    return send(decided.frame, sendOptions)
  }
  return inner
}
