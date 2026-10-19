import {RECOVER_REPEATED_WORK_INSTRUCTIONS,REPEATED_WORK_INSTRUCTIONS} from '../../execution/progress'
import {canFinishResponse,completionFailureCode,FINISH_RESPONSE_INSTRUCTIONS,modelCompletion,type ModelCompletion} from '../../completion'
import {failureEvent,authoredFailure,toolFailureResult,normalizeFailure,AIFailureError} from '../../failures'
import {ExecutionPaused} from '../../execution/state'
import {managedContext,contextOf} from '../../context/managed'
import { readDelegationRequest } from '../../delegationContract'
import { assistantLanguageInstructions } from '../../host'
/**
 * The `vercel` engine's loop: the Vercel AI SDK v7, behind the desk's contract.
 *
 * ADR-0001 makes this the default engine and says what the slot buys: the
 * desk's promises — propose-only, rehearsal-only, the tool allow-list, key
 * custody — are held **below** whatever runs the loop, so this adapter is a
 * translation and never a second opinion. What it translates is
 * `streamText`'s `stream` into the contract's events, in the same order the
 * contract declares them, and `session`'s two capabilities into the SDK's
 * two extension points: a `fetch` for the model, a tool set for the runtime.
 *
 * Four defects the ADR records against this SDK are guarded here, and the
 * fifth is chunk 4's:
 *
 * 1. **`experimental_refineToolInput` is experimental.** Written as a plain
 *    literal, an upstream rename produces zero errors under this desk's
 *    TypeScript and the rehearsal rewrite fails **open** — measured, in the
 *    bake-off, at 0 errors. `REHEARSAL_HOOK` is written as a key of the SDK's
 *    own options type, so the day `ai` stops declaring it this file stops
 *    compiling.
 * 2. **The refusal path leaks unhandled rejections.** `AI_NoOutputGeneratedError`
 *    reaches the page from a promise the result exposed and nobody claimed. It
 *    is handled by claiming result promises and disabling the unused browser
 *    telemetry path that leaves its completion promise unclaimed. Nothing on
 *    this page is suppressed — see `claimPromises`.
 * 3. **The SDK reads the answer it asked for.** An endpoint that answers whole
 *    to a request that asked to stream ends the run with no output at all. That
 *    is closed one layer down, in `relay.ts`.
 * 4. **A thinking signature split across two stream events is truncated**
 *    (`vercel/ai#19663`, reproduced on the shipped release). The desk cannot
 *    make the SDK reassemble one, so it **detects** the truncation instead: the
 *    fragments are ledgered as they arrive, the outgoing body is compared with
 *    them, a block whose signature came back as a fragment is removed rather
 *    than sent, and the session degrades once with the reason. See
 *    `relay.ts`'s `withoutTruncatedThinking`.
 * 5. **The tier is a call setting, applied per step.** `prepareStep` supplies
 *    it on every request with no agent to rebuild — but *what* it supplies is
 *    `assistant/thinking.ts`'s table, translated once by `sdkThinking` below,
 *    so the two engines put the same members on the wire.
 *
 * What is deliberately **not** here: `@ai-sdk/mcp` (the desk keeps its own MCP
 * client, and its gate is on that client's transport), and any writing at all.
 */
import { createAnthropic } from '@ai-sdk/anthropic'
import { createGoogleGenerativeAI } from '@ai-sdk/google'
import { createOpenAICompatible } from '@ai-sdk/openai-compatible'
import { readModelQuestion } from '../../questions'
import { questionFailureResult } from '../../questionFailure'
import { dynamicTool, jsonSchema, stepCountIs, streamText } from 'ai'
import {
  SYSTEM,
  CONVERSATION_SYSTEM,
  TEST_DESIGN_SYSTEM,
  BRIEF_SYSTEM, GRAPH_SYSTEM,
  streamingProse,
  hasProposalFence,
  eventIterator,
  extractProposal,
  proseOf,
  guardedCallTool,
  isCancelled,
  narrowingEvents,
  openRun,
  schemasShown,
  servedSchemaFor,
  textOf,
  withAbort,
  withDeadline,
  toolTimeout,
  limitText,
  wholeAnswer,
  reformatRequest,
  REFORMAT_INSTRUCTIONS,
  PROPOSE_PACK_TOOL,
  PROPOSE_PACK_INSTRUCTIONS,
  PROPOSAL_RECEIVED,
  offersProposeTool,
  readProposedPack,
  type Proposal
} from '../contract'
import { openThinking } from '../../thinking'
import {
  CRITIC_SYSTEM,
  MAX_CRITIC_TURNS,
  criticCannotRun,
  criticMessage,
  critiqueEvent,
  critiqueOnProposal,
  criticNote,
  openCritique
} from '../../refutation'
import { refusedSchemaKeyword, refusedSchemaSentence } from '../../geminiSchema'
import { eventChannel } from './channel'
import { placeholderBase, relayFetch, signatureLedger, signatureOf } from './relay'
import { retryingCall } from './retry'
import type { LanguageModel, ToolSet, ModelMessage } from 'ai'
import type { AssistantEngine, EndpointKind } from '../../host'
import type { AssistantEvent, AssistantSession, McpTool, McpToolResult, HostTool, ModelCall, ProposalNote } from '../../engine'
import type { CritiqueRecorder } from '../../refutation'
import type { ThinkingSlot } from '../../thinking'
import type { SignatureLedger } from './relay'

/**
 * The tool whose arguments the desk rehearses, named once.
 *
 * This is the one tool name in the adapter, and it is here because the SDK's
 * refinement hook is keyed by tool name. No schema is written anywhere in this
 * file: `session.tools` carries the runtime's own, and `jsonSchema()` passes
 * each through untouched, so the model is shown the contract the runtime
 * enforces or it is shown nothing.
 */
const REHEARSAL_TOOL = 'experimental_evaluate'

/**
 * **The single most important line in this adapter.**
 *
 * `streamText`'s options are declared with a `...settings` rest, so an options
 * object carrying a misspelled `experimental_refineToolInput` is accepted in
 * silence and the rehearsal rewrite is simply never applied. Naming the hook
 * once, as a key of the SDK's own options type, and using that constant as a
 * computed key makes the compiler the tripwire: the day `ai` renames or drops
 * the option, this line is an error rather than a guard that fails open.
 *
 * The ToolGate below it is why that is survivable rather than fatal — it is the
 * desk's own layer, on the wire, and `engines/vercel/engine.test.ts` proves the
 * rehearsal still reaches the runtime with this hook removed entirely.
 */
export const REHEARSAL_HOOK = 'experimental_refineToolInput' satisfies keyof Parameters<
  typeof streamText
>[0]

/**
 * The name the OpenAI-compatible provider is built under.
 *
 * It is also the key its `providerOptions` are read from — the SDK takes the
 * part of the provider id before the dot — so the two are one constant rather
 * than two strings somebody has to keep in step.
 */
export const ENDPOINT_NAME = 'desk-endpoint'

/**
 * The key the Google provider reads its options from.
 *
 * Not the desk's to choose: the provider takes the part of its own id before
 * the dot, and its id is `google.generative-ai` whatever base URL it is built
 * with. Written once so that the translation below and the engine's own suite
 * name the same string.
 */
export const GOOGLE_OPTIONS = 'google'

/**
 * The desk's wire members, in the SDK's own vocabulary.
 *
 * **A translation, and never a second table.** `assistant/thinking.ts` decides
 * what `on` means for a family and which Anthropic spelling is in force; this
 * says how to ask *this SDK* to put exactly those members on the request. The
 * mapping is asserted against the table in the engine's own suite, so a
 * spelling that stopped producing the desk's members is a red test rather than
 * a session that quietly thought at some other depth.
 *
 * `{}` where the slot carries nothing, so a run at `off` — or after a degrade —
 * is byte-identical to one from before this chunk.
 */
export function sdkThinking(
  family: EndpointKind,
  members: Record<string, unknown> | null
): Record<string, unknown> {
  if (members === null) return {}
  if (family === 'anthropic') {
    const thinking = members.thinking as { type?: string; budget_tokens?: number } | undefined
    const effort = (members.output_config as { effort?: string } | undefined)?.effort
    const anthropic: Record<string, unknown> = {}
    if (thinking?.type === 'adaptive') anthropic.thinking = { type: 'adaptive' }
    else if (thinking?.type === 'enabled') {
      anthropic.thinking = { type: 'enabled', budgetTokens: thinking.budget_tokens }
    }
    // **The maximum travels with the budget.** Anthropic spends the thinking
    // budget out of `max_tokens`, so the two are one decision and the table
    // makes it; this is the SDK's spelling of the number the table chose.
    const maximum = members.max_tokens
    if (typeof maximum === 'number') {
      return { providerOptions: { anthropic }, maxOutputTokens: maximum }
    }
    // The depth is a sibling on the wire and a sibling here: the provider puts
    // `effort` into `output_config`, which is the member the desk's table names.
    if (effort !== undefined) anthropic.effort = effort
    return { providerOptions: { anthropic } }
  }
  if (family === 'gemini') {
    // **The table already named it `thinkingConfig`, which is the SDK's own
    // spelling as well as the wire's.** So this is the shortest of the three
    // translations: the members go under the provider key the Google provider
    // reads its options from, and the SDK puts them in `generationConfig`
    // exactly where each protocol's own wire puts them.
    return { providerOptions: { [GOOGLE_OPTIONS]: { thinkingConfig: members.thinkingConfig } } }
  }
  return { providerOptions: { [ENDPOINT_NAME]: { reasoningEffort: members.reasoning_effort } } }
}

/** One model, built for the family the desk configured and nothing else. */
function modelFor(
  session: AssistantSession,
  /** The session's model capability, wrapped in the one retry a refusal earns (`retry.ts`). */
  call: ModelCall,
  signal: AbortSignal,
  ledger: SignatureLedger,
  onTruncated: (reason: string) => void,
  slot: ThinkingSlot
): LanguageModel {
  const fetch = relayFetch({
    family: session.model.family,
    call,
    signal,
    ledger,
    onTruncated,
    // Read after `onTruncated` has told the slot, so the rebuilt request
    // carries what the desk asks for now: after a truncation, nothing.
    membersNow: () => slot.members()
  })
  const baseURL = placeholderBase(session.model.family)
  if (session.model.family === 'gemini') {
    return createGoogleGenerativeAI({
      baseURL,
      // `createGoogleGenerativeAI` reaches `loadApiKey` the way
      // `createAnthropic` does and **throws** in a browser with no key rather
      // than omitting the header. The placeholder never leaves `relayFetch`:
      // `x-goog-api-key` is not on its protocol allow-list, the desk's
      // capability drops it again, and the chassis strips whatever it is sent
      // before injecting the configured key.
      apiKey: 'placeholder-the-desk-relay-injects-the-key',
      fetch
    })(session.model.model)
  }
  if (session.model.family === 'anthropic') {
    return createAnthropic({
      baseURL,
      // `createAnthropic` reaches `loadApiKey`, which **throws** in a browser
      // with no key rather than omitting the header. The placeholder never
      // leaves `relayFetch`: `x-api-key` is not on its protocol allow-list, the
      // desk's capability drops it again, and the chassis strips whatever it is
      // sent before injecting the configured key.
      apiKey: 'placeholder-the-desk-relay-injects-the-key',
      fetch
    })(session.model.model)
  }
  // `createOpenAICompatible` simply omits `Authorization` when it has no key.
  // That is a convenience: the allow-list in `relay.ts` is the guard.
  return createOpenAICompatible({ name: ENDPOINT_NAME, baseURL, fetch }).chatModel(
    session.model.model
  )
}

/**
 * The runtime's tools, as the SDK's, with the runtime's own schemas — and the
 * desk's host tools beside them, each executed by the desk's own function
 * rather than dispatched over the gate.
 */
function toolsFor(
  family: EndpointKind,
  tools: McpTool[],
  execute: (name: string, input: unknown) => Promise<McpToolResult>,
  hostTools: HostTool[] = [],
  executeHost: (tool: HostTool, input: unknown) => Promise<McpToolResult> = async () => ({
    content: [],
    isError: true
  })
): ToolSet {
  const set: ToolSet = {}
  for (const tool of hostTools) {
    set[tool.name] = dynamicTool({
      description: tool.description,
      inputSchema: jsonSchema<unknown>(
        servedSchemaFor(family, tool) as Parameters<typeof jsonSchema>[0]
      ),
      execute: (input: unknown) => executeHost(tool, input)
    })
  }
  for (const tool of tools) {
    set[tool.name] = dynamicTool({
      description: tool.description ?? '',
      // The served schema, as served — minus, on the one family whose wire
      // takes an OpenAPI subset, the closed removal list
      // `assistant/geminiSchema.ts` documents. Nothing is re-typed through a
      // schema library and nothing is written here where the runtime served
      // none — `servedSchema` refuses that, and the session ends rather than
      // showing the model a contract this desk invented.
      inputSchema: jsonSchema<unknown>(
        servedSchemaFor(family, tool) as Parameters<typeof jsonSchema>[0]
      ),
      execute: (input: unknown) => execute(tool.name, input)
    })
  }
  return set
}


/**
 * Whether a stream part is a call to a tool this session never offered.
 *
 * The SDK refuses such a call itself — a tool it was not handed has no
 * `execute` to reach — which is a second layer and a welcome one. It is not the
 * desk's layer: the guardrail line a person reads comes from the ToolGate, and
 * a gate that never saw the call has nothing to report. So the attempt is put
 * through `session.callTool` as the model made it, the gate refuses it on the
 * wire, and the refusal is reported by the layer that actually holds.
 */
function unofferedTool(part: { type: string }, offered: ReadonlySet<string>): string | undefined {
  if (part.type !== 'tool-error') return undefined
  const name = (part as { toolName?: unknown }).toolName
  if (typeof name !== 'string' || offered.has(name)) return undefined
  return name
}

/** What one tool call produced, in the shape the contract's event carries. */
function outcome(result: McpToolResult): { text: string; isError: boolean; structured?: unknown } {
  return {
    text: textOf(result),
    isError: Boolean(result.isError),
    structured: result.structuredContent
  }
}

/**
 * Claim the SDK result's promise-valued members before consuming its stream.
 * Reading a result getter creates a promise that can reject on a failed call.
 * Enumerate own properties and prototype getters so new SDK result fields do
 * not silently become unclaimed. Stream getters are excluded: reading those
 * creates unused tee/transform branches rather than promises.
 *
 * A separate browser-only leak in SDK 7.0.93 was traced to telemetry:
 * streamText passes `_totalUsage.promise.then(...)` into
 * openTelemetryChannelSpanContext, whose non-Node return leaves it unclaimed.
 * Both calls below explicitly disable that unused telemetry path. This fixes
 * Stop and endpoint-refusal errors at their source; no page-wide rejection
 * listener suppresses errors. The real-browser response check covers both.
 */
export function claimPromises(result: object): number {
  const names = new Set<string>()
  for (
    let held: object | null = result;
    held !== null;
    held = Object.getPrototypeOf(held) as object | null
  ) {
    for (const name of Object.getOwnPropertyNames(held)) names.add(name)
  }
  let claimed = 0
  for (const name of names) {
    // Stream getters create a new tee/transform, not a promise. Opening an
    // unconsumed stream just to inspect it adds a branch with no consumer.
    if (name === 'constructor' || name === 'stream' || name.endsWith('Stream')) continue
    let value: unknown
    try {
      value = (result as Record<string, unknown>)[name]
    } catch {
      // A member that throws on being read is not a promise to claim.
      continue
    }
    if (value === null || typeof value !== 'object') continue
    if (typeof (value as { then?: unknown }).then !== 'function') continue
    claimed += 1
    void Promise.resolve(value as PromiseLike<unknown>).catch(() => undefined)
  }
  return claimed
}

// Provider text is inspected only for the existing bounded capability-negotiation
// rules. It is never used as a user-facing failure or persisted diagnostic.
function endpointSentence(body:unknown):string {
 if(typeof body!=='string'||body.length>16384)return ''
 try{const value=JSON.parse(body);return typeof value?.error==='string'?value.error:typeof value?.error?.message==='string'?value.error.message:typeof value?.message==='string'?value.message:''}catch{return ''}
}

/**
 * Run one session, as a stream of events.
 *
 * `end` is emitted exactly once, on the channel like every other event and
 * never from a `finally`, whatever happened above it — including an abort,
 * which ends the session and says nothing else, because a viewer who pressed
 * Stop has not been told about a failure. A consumer that stops listening is
 * owed no terminal event and gets none: its `return()` closes the generator.
 */
export function runVercel(session:AssistantSession,id:AssistantEngine):AsyncIterable<AssistantEvent> {
 return managedContext(session,held=>runVercelLoop(held,id))
}

function runVercelLoop(
  session: AssistantSession,
  /** The id the registry loaded this engine under. See `runBuiltin`. */
  id: AssistantEngine
): AsyncIterable<AssistantEvent> {
  // The run's own controller, chained to the session's: the SDK is given this
  // one so a consumer that walks away ends the run, and the session's abort
  // reaches it the moment the viewer presses Stop.
  /**
   * One gate, reached four ways.
   *
   * The consumer's `return()`, its `throw()`, the session's own signal and the
   * run's natural end all close it — once, synchronously, marking the run closed
   * *before* it aborts or releases anything, because what a released loop
   * produces next must already be nobody's. A session already aborted when this
   * is called closes it before a provider is built or a request is made.
   */
  /**
   * The channel and the gate, in the only order that works on the one path
   * this is all for.
   *
   * A session already aborted closes the gate the moment it is opened, and
   * closing it abandons the channel — so the channel has to exist first. But
   * abandoning calls back into the gate, which does not exist yet. Both are
   * true, and neither is a problem: during that first synchronous close the
   * hook below is still the no-op, which is exactly right, because the thing it
   * would have called is already closing.
   */
  let closeRun = () => {}
  const channel = eventChannel({ onAbandon: () => closeRun() })
  const gate = openRun(session, () => channel.abandon())
  closeRun = gate.close
  // The runtime, reachable only while the run is: read before dispatch, so
  // nothing reaches `jpack mcp` after the consumer has left, and bounded, so a
  // call in flight cannot hold the cleanup that is ending it.
  const context = contextOf(session)
  const callTool = guardedCallTool(session, gate.signal)
  // The tier, held by the desk. This adapter asks it for members and pushes
  // whatever notice it hands back; it never decides what a refusal meant.
  const slot = openThinking(session)
  const ledger = signatureLedger()
  /**
   * The truncation notice, **delivered at the transition rather than owed**.
   *
   * The slot goes unavailable the instant this is called and the request that
   * leaves is already the degraded one, so a notice held until the next stream
   * part arrived left the tab saying `thinking on` about a request that carried
   * none — for as long as that request took, or for ever if it hung. The relay
   * awaits this, so the line is on the stream before the request goes.
   */
  const onTruncated = async (reason: string): Promise<void> => {
    const said = slot.truncated(reason)
    if (said !== null) await channel.push(said)
  }
  // Both the runtime's and the desk's: an SDK refusal of a host tool's input
  // is the host tool's to report, never a call for the gate. A host tool
  // named like a runtime tool would shadow it in the SDK's set, so a
  // collision ends the run before the model is shown either.
  const offered = new Set([...session.tools, ...session.hostTools].map((tool) => tool.name))
  const collision = session.hostTools.find((tool) => session.tools.some((served) => served.name === tool.name))
  // What the model asked for, before the SDK's refinement touched it.
  //
  // One queue and not a map, because the refinement hook below is registered for
  // exactly one tool: every entry here is a call to it, and they are taken in
  // the order the SDK refines and then executes them, so a step carrying two
  // evaluates still pairs each call with its own arguments.
  const asked: unknown[] = []
  const inputPause = new AbortController()
  /**
   * The turn's wall clock (`context.policy.turnMilliseconds`). When it passes,
   * whatever is in flight is stopped and the run pauses as `budget_paused`
   * does, with its checkpoint: completed work is kept and the person decides.
   * A proposal already made is delivered, with a note where the critic was cut.
   */
  const turnClock = new AbortController()
  let overTime = false
  let turnTimer: ReturnType<typeof setTimeout> | undefined
  const executionSignal = AbortSignal.any([gate.signal, inputPause.signal, turnClock.signal])
  let awaitingInput = false

  /**
   * One whole attempt at the session.
   *
   * `produced` counts what this attempt already put on the stream, because the
   * only refusal this desk retries is one that arrived **before anything was
   * delivered** — a 400 on the very first request. A tier refusal after a tool
   * call has run is not a session to start again; it is reported like any other
   * failure.
   */
  const runOnce = async (produced: { count: number }): Promise<void> => {
    /**
     * One event on the stream, counted.
     *
     * The count is what makes the retry below safe: an attempt that delivered
     * nothing can be started again, and one that delivered anything cannot.
     */
    const deliver = async (event: AssistantEvent): Promise<void> => {
      if (event.type !== 'message_progress' || event.text !== '') produced.count += 1
      await channel.push(event)
    }
    /**
     * The critic's recorder, while the critic is running.
     *
     * The tool dispatch below is the **same** one the main loop uses — same
     * gate, same events, same rehearsal hook — so the pass does not get a
     * dispatch of its own to be outside anything with. What changes for the
     * length of the pass is that the answers are also shown to the recorder.
     */
    let recording: CritiqueRecorder | null = null
    let toolsClosed=false
    let progressStep:'work'|'recover'|'finish'='work'
    const turnLimit=context.execution.remaining
    const finalInstructions='\nThis is the final request within this turn budget. Tools are unavailable. Answer the current question using results already obtained. If the available information is insufficient, explain exactly what is known and what remains missing. Do not invent results, claim unsaved changes, or turn a diagnostic question into a new proposal.'
    // Refusals are journaled as not-started operations, including the exact
    // attempted arguments. They never reach a host tool or the runtime.
    const rejectToolStep=async(name:string,args:unknown):Promise<McpToolResult|undefined>=>{
      if(!toolsClosed&&!context.execution.progress.rejects(name,args))return
      const repetition=context.execution.progress.stalled
      const result=toolFailureResult(new AIFailureError({version:1,code:repetition?'repeated-work':'invalid-response',source:'desk',outcome:'not-started',detail:toolsClosed?'This request is reserved for a final answer. This tool call was not executed. Use completed results or explain what remains.':'This call repeats completed work with the same arguments and was not executed. Use its retained result, choose a different concrete action, or explain the remaining blocker.'}))
      const callId=crypto.randomUUID()
      await deliver({type:'tool_call',callId,name,args})
      await deliver({type:'tool_result',callId,name,isError:true,text:result.content[0]!.text,structured:result.structuredContent})
      return result
    }

    // A fresh attempt takes nothing from the one before it.
    asked.length = 0
    /** A proposal delivered through `propose_pack`; the fence is the fallback. */
    let toolProposal: Proposal | null = null
    const tools = toolsFor(session.model.family, session.tools, async (name, input) => {
      // **The desk's gate is handed the call the model made.** The hook below
      // has already rewritten what the SDK carries; what leaves the page is
      // rewritten by the ToolGate, which is the layer that reports it. A gate
      // handed a call somebody else already fixed reports nothing, and the
      // guardrail line in the tab is the only place a person learns that the
      // rehearsal flag was forced.
      const args = (name === REHEARSAL_TOOL && asked.length > 0 ? asked.shift() : input) as Record<
        string,
        unknown
      >
      if (awaitingInput) throw new DOMException('Waiting for input', 'AbortError')
      executionSignal.throwIfAborted()
      const refusal=await rejectToolStep(name,args)
      if(refusal)return refusal
      const correlation = session.interactive ? { callId: crypto.randomUUID() } : {}
      await deliver({ type: 'tool_call', ...correlation, name, args })
      let answer: McpToolResult
      try {
        // Bounded by the policy's per-call deadline as well as by the run: a
        // call past it is a failed result the model reads, not a failed run.
        const limit = context.policy.toolCallMilliseconds
        answer = await withDeadline(() => callTool(name, args), executionSignal, limit, () => toolTimeout(limit))
      } catch (cause) {
        // A run that ended while this was waiting — or before it was
        // dispatched — is the session unwinding, not a tool refusing. The
        // runtime was not asked and the model is told nothing more.
        if (isCancelled(cause)) {
          const text = 'the session was stopped before this call was made; nothing was written'
          return { content: [{ type: 'text', text }], isError: true }
        }
        const result=toolFailureResult(cause)
        await deliver({type:'tool_result',...correlation,name,isError:true,text:result.content[0]!.text,structured:result.structuredContent})
        return result
      }
      const said = outcome(answer)
      await deliver({
        type: 'tool_result',
        ...correlation,
        name,
        isError: said.isError,
        text: said.text,
        ...(said.structured === undefined ? {} : { structured: said.structured })
      })
      // **Only what the runtime answered**, and this is the only place it is
      // reached: the refusal path above returns before it, so a call the gate
      // refused is a `guardrail` line and never a check.
      recording?.saw(name, said.text)
      return context.result(name, answer)
    }, session.hostTools, async (tool, input) => {
      // A host tool runs on the page, under the run's own signal: a stopped run
      // stops it, and its answer is reported in the same two events a runtime
      // answer earns. It is never a check the critic counts — the runtime's
      // verdicts are the runtime's — and it never reaches the recorder.
      const args = (input ?? {}) as Record<string, unknown>
      if (awaitingInput) throw new DOMException('Waiting for input', 'AbortError')
      executionSignal.throwIfAborted()
      const refusal=await rejectToolStep(tool.name,args)
      if(refusal)return refusal
      const correlation = session.interactive ? { callId: crypto.randomUUID() } : {}
      if (!tool.interaction) await deliver({ type: 'tool_call', ...correlation, name: tool.name, args })
      let answer: McpToolResult
      try {
        if (tool.interaction === 'delegation') {
          const request = readDelegationRequest(args)
          awaitingInput = true
          await deliver({type:'delegation_requested',request})
          inputPause.abort()
          throw new DOMException('Delegation handoff', 'AbortError')
        }
        if (tool.interaction === 'question') {
          const question = readModelQuestion(args)
          if (awaitingInput) throw new DOMException('Waiting for input', 'AbortError')
          awaitingInput = true
          await deliver({type:'user_question', id:crypto.randomUUID(), question})
          inputPause.abort()
          throw new DOMException('Waiting for input', 'AbortError')
        }
        if (tool.name === PROPOSE_PACK_TOOL) {
          // **The proposal, delivered as a tool call.** Read here, by the
          // contract's reader; the host's own `execute` is never reached. The
          // latest call's document is the one proposed.
          toolProposal = readProposedPack(args)
          answer = { content: [{ type: 'text', text: PROPOSAL_RECEIVED }] }
        } else {
          const limit = context.policy.toolCallMilliseconds
          answer = await withDeadline((signal) => tool.execute(args, signal), executionSignal, limit, () => toolTimeout(limit))
        }
      } catch (cause) {
        if (isCancelled(cause)) {
          const text = 'the session was stopped before this call was made; nothing was written'
          return { content: [{ type: 'text', text }], isError: true }
        }
        if (tool.interaction === 'question') {
          const result = questionFailureResult(cause)
          await deliver({type:'tool_result',...correlation,name:tool.name,isError:true,text:result.content[0]!.text,structured:result.structuredContent})
          return result
        }
        const result=toolFailureResult(cause)
        await deliver({type:'tool_result',...correlation,name:tool.name,isError:true,text:result.content[0]!.text,structured:result.structuredContent})
        return result
      }
      const said = outcome(answer)
      await deliver({
        type: 'tool_result',
        ...correlation,
        name: tool.name,
        isError: said.isError,
        text: said.text,
        ...(said.structured === undefined ? {} : { structured: said.structured })
      })
      return context.result(tool.name, answer)
    })

    // One model and one refinement hook, shared by the loop and the critic:
    // the pass runs on the same everything, which is what makes "inside the
    // same ToolGate" structural rather than a habit.
    const call = retryingCall(session.model.call, {
      signal: executionSignal,
      // Each refused attempt is a row of the completion record; a retry is
      // said on the stream, as the codex engine says its bridge's.
      onRefused: async (refused) => {
        context.execution.recordRefusal(recording !== null ? 'review' : synthesis ? 'synthesis' : 'work', refused)
        if (refused.waitMs !== undefined) await channel.push({ type: 'retry', attempt: refused.attempt })
      }
    })
    const model = modelFor(session, call, executionSignal, ledger, onTruncated, slot)
    // See REHEARSAL_HOOK. The key is the constant, never a literal.
    const refine = {
      [REHEARSAL_TOOL]: (input: unknown) => {
        asked.push(input)
        const already = (input as { rehearsal?: unknown } | null)?.rehearsal === true
        return already ? input : { ...(input as object), rehearsal: true }
      }
    }

    let streamed: unknown = null
    let publicText = ''
    const instructions=(session.purpose === 'graph' ? GRAPH_SYSTEM : session.purpose === 'brief' ? BRIEF_SYSTEM : session.purpose === 'test-design' ? TEST_DESIGN_SYSTEM : session.allowConversation ? CONVERSATION_SYSTEM : SYSTEM) + (offersProposeTool(session.hostTools) ? PROPOSE_PACK_INSTRUCTIONS : '') + assistantLanguageInstructions(session.replyLanguage)
    const schemas=[...session.tools,...session.hostTools].map(({name,description,inputSchema})=>({name,description,inputSchema}))
    context.reserveOutput(sdkThinking(session.model.family,slot.members()).maxOutputTokens)
    const initialPrompt=await context.prompt(session.prompt,instructions+'\n'+context.execution.guidance(),schemas,session.context?.currentRequest)
    let messages:ModelMessage[]=[{role:'user',content:initialPrompt}]
    let lastMessages=messages
    let completion:ModelCompletion|undefined
    let lastToolCalls=0
    let finalText=''
    let synthesis=false
    /** The one reformat request a proposal that could not be read earns. */
    let reformatting=false
    let steps=0
    let proposal: Proposal | null = null
    /**
     * The answer whose proposal could not be read. **Nothing finished is lost:**
     * whatever the reformat request does, this is delivered whole — the prose
     * and the raw fence — before the run reports why.
     */
    let unread: { text: string; cause: unknown } | null = null
    const keepUnread = async (): Promise<void> => {
      const text = unread === null ? '' : wholeAnswer(unread.text)
      unread = null
      if (text !== '' && !gate.isClosed()) await deliver({ type: 'message', text })
    }
    try {
    for(;;){
      completion=undefined
      lastToolCalls=0
      const result = streamText({
        // See claimPromises: the unused browser tracing path leaks on failure.
        telemetry: { isEnabled: false },
        model,
        onChunk: async ({chunk}) => {
          if(chunk.type==='text-delta' && session.interactive){publicText+=chunk.text;await deliver({type:'message_progress',text:streamingProse(publicText)})}
        },
        instructions,
        tools,
        messages,
        maxOutputTokens:context.policy.outputTokens,
        stopWhen: [stepCountIs(synthesis?1:turnLimit),()=>toolsClosed],
        abortSignal: executionSignal,
        // **The tier, per step, with no agent to rebuild.** ADR-0001 credits this
        // SDK with exactly that, and the members are the desk's table's — read
        // fresh on every step, so a degrade or a dialect fallback takes effect on
        // the next request rather than at the next session.
        prepareStep: async ({messages,stepNumber}) => {
          publicText=''
          if(session.interactive)await deliver({type:'message_progress',text:''})
          if(synthesis)await deliver({type:'response_recovery'})
          await context.execution.retainProgress(false)
          progressStep=synthesis?'finish':context.execution.progress.nextStep(context.execution.remaining)
          if(progressStep==='finish'&&context.execution.progress.stalled&&!synthesis)await deliver({type:'response_recovery'})
          toolsClosed=synthesis||progressStep==='finish'||stepNumber>=turnLimit-1
          const stepInstructions=instructions+'\n'+context.execution.guidance()+(reformatting?'\n'+REFORMAT_INSTRUCTIONS:synthesis?'\n'+FINISH_RESPONSE_INSTRUCTIONS:progressStep==='recover'?'\n'+RECOVER_REPEATED_WORK_INSTRUCTIONS:progressStep==='finish'?'\n'+REPEATED_WORK_INSTRUCTIONS:toolsClosed?finalInstructions:'')
          lastMessages=await context.messages(messages,stepInstructions,schemas)
          return {...sdkThinking(session.model.family,slot.members()),
            ...(toolsClosed?{activeTools:[],toolChoice:'none' as const}:{}),
            instructions:stepInstructions,messages:lastMessages}
        },
        // **No retries, deliberately.** The SDK's default is two, with an
        // exponential backoff, and its retryable set includes 409 — which is the
        // status the *desk's own relay* answers with when no key is stored on
        // this machine. So a refusal a person has to go and fix became three
        // requests and six seconds of a spinner. This adapter makes one
        // request per turn and reports what came back; two engines that answer a
        // refusal differently is exactly what the slot exists to prevent.
        maxRetries: 0,
        // Google's chat schema currently omits error-only chunks. Inspect only
        // the error envelope; raw chunks never enter events or saved context.
        includeRawChunks: session.model.family === 'gemini',
        // `streamText` does not throw: a refused request surfaces here and again
        // as an `error` part on the stream. Both are read, and whichever arrives
        // first is the one reported.
        onError: (event: { error: unknown }) => {
          streamed ??= event.error
        },
        [REHEARSAL_HOOK]: refine
      })

      // **Before anything is awaited, and the only claim there is.** Every
      // promise-valued member of the result is claimed the moment the result
      // exists, so a call that fails produces no rejection the page cannot catch.
      // Nothing below reads one of those members without awaiting it, which is
      // the other half of the same rule. See `claimPromises`.
      claimPromises(result)

      // The **last** step's text, not every step's: a session that reasoned aloud
      // before calling a tool would otherwise have that prose concatenated onto
      // the message the proposal is read out of.
      let final = ''
      /** How many steps have started, so a turn boundary can be recognised. */
      /** One reasoning passage, accumulated so `done` can carry the whole of it. */
      let reasoning = ''
      // **Read through the run's signal, not only the SDK's.** `abortSignal`
      // makes the SDK end its own stream; this makes the *read* end whatever the
      // SDK decides to do, which is the same rule every other await in this loop
      // is held to.
      const parts = result.stream[Symbol.asyncIterator]()
      for (;;) {
        const step = await withAbort(() => parts.next(), executionSignal)
        if (step.done === true) break
        const part = step.value
        if (part.type === 'start-step') {
          // A step boundary is the end of the turn before it. ADR-0001's second
          // unmeasurable state — "no thinking block after the first turn" — is
          // decided here, by the desk, and said once.
          if (steps > 0) {
            // `final` still holds the step that just ended: whether it produced
            // an answer of its own is half the evidence a turn carries.
            const noticed = slot.turnEnded(final !== '')
            if (noticed !== null) await deliver(noticed)
          }
          // A turn boundary is where a reasoning block's signature is finished.
          // See `signatureLedger`.
          ledger.boundary()
          steps += 1
          lastToolCalls=0
          completion=undefined
          final = ''
          continue
        }
        if(part.type==='finish-step'){
          completion=modelCompletion(part.finishReason,part.usage)
          context.execution.recordCompletion(completion,synthesis?'synthesis':'work')
          continue
        }
        if (part.type === 'raw') {
          const raw=part.rawValue as {error?:unknown}|null
          if(session.model.family==='gemini'&&raw&&typeof raw==='object'&&raw.error!==undefined)streamed??=new AIFailureError({...normalizeFailure(raw.error,'model'),outcome:'unknown'})
          continue
        }
        if (part.type === 'error') {
          streamed ??= (part as { error: unknown }).error
          continue
        }
        // **Reasoning is reported whatever the tier is.** The tier is what this
        // desk *asks* for, and this chunk asks for nothing; but a model that
        // always thinks reasons anyway, and an endpoint that returns
        // `reasoning_content` on its own default behaviour is the ordinary case
        // rather than the exotic one. An adapter that dropped those parts would
        // be deciding, on the desk's behalf, that what the model said about its
        // own reasoning is not worth showing.
        if (part.type === 'reasoning-delta') {
          const text = (part as { text?: string }).text ?? ''
          reasoning += text
          // Every signature fragment, as it arrives, wherever this family's
          // provider puts one. See `signatureLedger` and `signatureOf`.
          const carried = signatureOf(session.model.family, part)
          if (carried !== undefined) {
            ledger.fragment(String((part as { id?: unknown }).id ?? ''), carried)
          }
          if (text !== '') await deliver({ type: 'reasoning', text, done: false })
          continue
        }
        if (part.type === 'reasoning-end') {
          // The whole of it, once, so a reader has the passage rather than the
          // pieces — which is the shape the contract's `done` marks.
          await deliver({ type: 'reasoning', text: reasoning, done: true })
          reasoning = ''
          slot.sawReasoning()
          continue
        }
        // **A signed function call is ledgered too.** On the Gemini wire the
        // signature that matters most rides on the first `functionCall` part of a
        // turn rather than on a summary, and the SDK surfaces it in the same
        // provider metadata. Read off `tool-call` and not off `tool-input-*`,
        // which repeat the same value three times before it.
        if (part.type === 'tool-call') {
          lastToolCalls++
          const onCall = signatureOf(session.model.family, part)
          if (onCall !== undefined) {
            ledger.fragment(String((part as { toolCallId?: unknown }).toolCallId ?? ''), onCall)
          }
        }
        if(part.type==='tool-error'&&await rejectToolStep(part.toolName,part.input??{}))continue
        const unoffered = unofferedTool(part, offered)
        if (unoffered !== undefined) {
          // The SDK refused it; the desk's gate must be the one to say so.
          const input = (part as { input?: unknown }).input
          await deliver({ type: 'tool_call', name: unoffered, args: input ?? {} })
          let refusal = 'the call did not leave the page and nothing was written'
          try {
            await callTool(unoffered, (input ?? {}) as Record<string, unknown>)
          } catch (cause) {
            // A cancelled run stops here rather than reporting a refusal that
            // never happened.
            if (isCancelled(cause)) throw cause
            refusal = (cause as Error).message
          }
          await deliver({
            type: 'tool_result',
            name: unoffered,
            isError: true,
            text:
              `refused: ${refusal}. This assistant proposes; it never writes a file and ` +
              `never calls a tool it was not offered.`
          })
          continue
        }
        if (part.type === 'text-delta') {
          final += (part as { text?: string }).text ?? ''
        }
      }
      if (streamed !== null) throw streamed
      // The last turn's own accounting.
      const noticed = slot.turnEnded(final !== '')
      if (noticed !== null) await deliver(noticed)

      // `result.text` mints a fresh promise on every read, so it is read here,
      // where it is awaited, and nowhere it would be left standing.
      finalText = final !== '' ? final : await withAbort(() => Promise.resolve(result.text), executionSignal)
      // A proposal delivered through the tool is a finished answer, whatever
      // the text after it says or does not say.
      if(toolProposal===null&&canFinishResponse(completion,finalText,lastToolCalls,context.execution.remaining,synthesis||(toolsClosed&&context.execution.progress.stalled))){
        synthesis=true
        // Reuse the projected history before the incomplete response, with its
        // completed tool pairs. Do not append the failed response's partial
        // JSON or private reasoning. New tool dispatch and SDK retries stay closed.
        messages=[...lastMessages,{role:'user',content:FINISH_RESPONSE_INSTRUCTIONS}]
        continue
      }
      if(toolProposal===null&&!context.execution.progress.stalled&&!finalText.trim()&&steps>=turnLimit&&completion?.finishReason!=='content-filter'&&completion?.finishReason!=='length'&&completion?.finishReason!=='error')throw new ExecutionPaused()
      if(toolProposal===null&&(toolsClosed&&lastToolCalls>0||!finalText.trim()||completion?.finishReason==='length'||completion?.finishReason==='content-filter'||completion?.finishReason==='error')){
        // A stalled loop is distinct from filtering, output truncation or a
        // provider error. Keep those original causes when they actually occur.
        const stalled=context.execution.progress.stalled&&!['length','content-filter','error'].includes(completion?.finishReason??'')
        throw new AIFailureError({version:1,code:stalled?'repeated-work':completionFailureCode(completion),source:stalled?'desk':'model',outcome:'unknown',...(completion?{completion}:{})})
      }
      if (toolProposal !== null || !session.allowConversation || hasProposalFence(finalText)) {
        try {
          proposal = toolProposal ?? extractProposal(finalText)
        } catch (cause) {
          // **One reformat request, within the allowance**: tool-free, shown
          // its own answer and asked for the same proposal in the shape the
          // contract reads. A second failure is reported, the first answer kept.
          if (unread === null && context.execution.remaining > 0) {
            unread = { text: finalText, cause }
            reformatting = synthesis = true
            messages = [...lastMessages, { role: 'assistant', content: finalText }, { role: 'user', content: reformatRequest(cause) }]
            continue
          }
          unread ??= { text: finalText, cause }
          throw unread.cause
        }
      }
      break
    }
    } catch (cause) {
      await keepUnread()
      throw cause
    }
    if (proposal === null) {
      await deliver({ type: 'message', text: proseOf(finalText) })
      if(context.execution.remaining<=0&&context.execution.state.plan.some(step=>step.status!=='completed'))throw new ExecutionPaused()
      return
    }
    // What the model said beside the document, as it said it: the first
    // answer's, where a reformat request recovered its proposal.
    const prose = (unread === null ? '' : proseOf((unread as { text: string }).text)) || proseOf(finalText)
    if (prose !== '') await deliver({ type: 'message', text: prose })

    /**
     * The refutation pass: a second `streamText`, which is this SDK's own
     * subagent shape, on the same model, the same tool set and the same
     * refinement hook.
     *
     * It runs after the proposal exists and **before** the proposal event is
     * delivered, and every call it makes is a read.
     */
    let critique = null as ReturnType<CritiqueRecorder['critique']> | null
    let note: ProposalNote | null = null
    // **No runtime prompt, no critic.** The instructions are the runtime's; this
    // desk adds one sentence and has none of its own to fall back on.
    const review = session.adversarialReview ?? slot.runsRefutation()
    const cannot = review ? criticCannotRun(session.testPrompt) : null
    if (cannot !== null) {
      critique = cannot
      await deliver(critiqueEvent(cannot))
    } else if (review) {
      const recorder = openCritique()
      recording = recorder
      toolsClosed=false
      // A fresh conversation: its history carries none of the loop's blocks.
      ledger.conversation()
      context.beginIsolatedPhase()
      let criticText = ''
      let criticReasoning = ''
      /** Why the critic did not finish: its failure, never raised ahead of the proposal. */
      let unfinished: unknown = null
      try {
        const critic = streamText({
          telemetry: { isEnabled: false },
          model,
          instructions: CRITIC_SYSTEM + assistantLanguageInstructions(session.replyLanguage),
          tools: Object.fromEntries(Object.entries(tools).filter(([name]) => !session.hostTools?.some(tool => tool.name === name && (tool.interaction || tool.effect === 'memory' || ['update_plan','request_budget_increase',PROPOSE_PACK_TOOL].includes(tool.name))))),
          messages: [
            { role: 'user', content: await context.prompt(criticMessage(session.testPrompt, proposal.document),CRITIC_SYSTEM,schemas) }
          ],
          stopWhen: [stepCountIs(MAX_CRITIC_TURNS),()=>toolsClosed],
          abortSignal: executionSignal,
          maxRetries: 0,
          // Google's chat schema currently omits error-only chunks. Inspect only
          // the error envelope; raw chunks never enter events or saved context.
          includeRawChunks: session.model.family === 'gemini',
          onError: (event: { error: unknown }) => {
            streamed ??= event.error
          },
          prepareStep: async ({messages,stepNumber}) => {
            progressStep=context.execution.progress.nextStep(Math.min(context.execution.remaining,MAX_CRITIC_TURNS-stepNumber))
            toolsClosed=progressStep==='finish'||stepNumber>=MAX_CRITIC_TURNS-1
            const instructions=CRITIC_SYSTEM+assistantLanguageInstructions(session.replyLanguage)+(progressStep==='recover'?'\n'+RECOVER_REPEATED_WORK_INSTRUCTIONS:progressStep==='finish'?'\n'+REPEATED_WORK_INSTRUCTIONS:toolsClosed?finalInstructions:'')
            return {...sdkThinking(session.model.family,slot.members()),...(toolsClosed?{activeTools:[],toolChoice:'none' as const}:{}),instructions,messages:await context.messages(messages,instructions,schemas)}
          },
          maxOutputTokens:context.policy.outputTokens,
          [REHEARSAL_HOOK]: refine
        })
        claimPromises(critic)
        const criticParts = critic.stream[Symbol.asyncIterator]()
        for (;;) {
          const step = await withAbort(() => criticParts.next(), executionSignal)
          if (step.done === true) break
          const part = step.value
          if (part.type === 'start-step') {
            ledger.boundary()
            // The critic's turns are this session's turns.
            const said = slot.turnEnded(criticText !== '')
            if (said !== null) await deliver(said)
            criticText = ''
            continue
          }
          if(part.type==='finish-step'){
            const completion=modelCompletion(part.finishReason,part.usage)
            context.execution.recordCompletion(completion,'review')
            if(['length','content-filter','error'].includes(completion.finishReason))streamed??=new AIFailureError({version:1,code:completionFailureCode(completion),source:'model',outcome:'unknown',completion})
            continue
          }
          if (part.type === 'raw') {
            const raw=part.rawValue as {error?:unknown}|null
            if(session.model.family==='gemini'&&raw&&typeof raw==='object'&&raw.error!==undefined)streamed??=new AIFailureError({...normalizeFailure(raw.error,'model'),outcome:'unknown'})
            continue
          }
          if (part.type === 'error') {
            streamed ??= (part as { error: unknown }).error
            continue
          }
          if (part.type === 'reasoning-delta') {
            const text = (part as { text?: string }).text ?? ''
            criticReasoning += text
            const carried = signatureOf(session.model.family, part)
            if (carried !== undefined) {
              ledger.fragment(String((part as { id?: unknown }).id ?? ''), carried)
            }
            if (text !== '') await deliver({ type: 'reasoning', text, done: false })
            continue
          }
          if (part.type === 'reasoning-end') {
            await deliver({ type: 'reasoning', text: criticReasoning, done: true })
            criticReasoning = ''
            slot.sawReasoning()
            continue
          }
          if (part.type === 'tool-call') {
            const onCall = signatureOf(session.model.family, part)
            if (onCall !== undefined) {
              ledger.fragment(String((part as { toolCallId?: unknown }).toolCallId ?? ''), onCall)
            }
            continue
          }
          if(part.type==='tool-error'&&await rejectToolStep(part.toolName,part.input??{}))continue
          if (part.type === 'text-delta') criticText += (part as { text?: string }).text ?? ''
        }
        const ended = slot.turnEnded(criticText !== '')
        if (ended !== null) await deliver(ended)
      } catch (cause) {
        // A person's Stop ends the run here as anywhere; the turn's wall clock
        // and everything else leave the finished proposal standing.
        if (gate.signal.aborted || (isCancelled(cause) && !overTime)) throw cause
        unfinished = cause
      } finally {
        // Whatever happened, the main loop's dispatch stops feeding a recorder
        // nobody is reading.
        recording = null
      }
      unfinished ??= streamed
      if (unfinished === null) {
        critique = recorder.critique(criticText)
        await deliver(critiqueEvent(critique))
      } else {
        note = criticNote(overTime ? turnTimeFailure() : unfinished, 'model')
      }
    }

    await deliver({
      type: 'proposal',
      document: proposal.document,
      unknowns: proposal.unknowns,
      ...critiqueOnProposal(critique),
      ...(note === null ? {} : { note })
    })
  }

  /** What the turn's wall clock passing is, as a failure the catalogue can say. */
  const turnTimeFailure = (): AIFailureError =>
    authoredFailure('timeout', `The turn reached its wall-clock limit of ${limitText(context.policy.turnMilliseconds)}.`)

  /**
   * The status and the endpoint's own sentence out of an SDK error, or nothing.
   *
   * The desk's thinking slot needs both to decide whether a refusal was about
   * the tier it sent. Neither is quoted past that: the sentence a person reads
   * comes from the shared failure catalogue, and the endpoint's body reaches the closed list of
   * patterns in `assistant/thinking.ts` and goes no further.
   */
  const refusalOf = (cause: unknown): { status: number; message: string } | null => {
    const error = cause as { statusCode?: unknown; responseBody?: unknown; cause?: unknown }
    const status =
      typeof error?.statusCode === 'number'
        ? error.statusCode
        : typeof (error?.cause as { statusCode?: unknown } | undefined)?.statusCode === 'number'
          ? ((error.cause as { statusCode: number }).statusCode)
          : undefined
    if (status === undefined) return null
    const body = error.responseBody ?? (error.cause as { responseBody?: unknown } | undefined)?.responseBody
    return { status, message: endpointSentence(body) }
  }

  /**
   * The refusal a served schema earned, or nothing.
   *
   * Two conditions and both required, exactly as the tier's classifier has: a
   * 400 whose message names a keyword this desk **actually sent**. Anything
   * else is the failure it already was, reported unchanged.
   */
  const schemaRefusal = (cause: unknown): Error | null => {
    // **Only on the family whose wire takes a schema subset.** The classifier
    // matches a substring, so on the other two families a 400 saying
    // "unsupported response type" names `type` and the
    // real failure was being rewritten into a sentence about a removal list that
    // has nothing to do with it.
    if (session.model.family !== 'gemini') return null
    const refusal = refusalOf(cause)
    if (refusal === null) return null
    const keyword = refusedSchemaKeyword(
      refusal.status,
      refusal.message,
      schemasShown(session.model.family, session.tools)
    )
    return keyword === '' ? null : authoredFailure('invalid-request',refusedSchemaSentence(keyword),'model')
  }

  /**
   * The session, with the one retry a tier refusal earns.
   *
   * **Only before anything has been delivered.** An endpoint that has no
   * thinking answers 400 to the first request, and starting that request again
   * costs one call and no work; an endpoint that refused the tier half way
   * through a session is not one whose session can be replayed, and it is
   * reported like any other failure. Three attempts at most — the tier as
   * configured, the other Anthropic spelling, and the plain request — and the
   * slot itself says `other` once it carries no members, so this cannot spin.
   */
  const drive = async (): Promise<void> => {
    turnTimer = setTimeout(() => {
      overTime = true
      turnClock.abort()
    }, context.policy.turnMilliseconds)
    // A host tool named like a runtime tool would shadow it in the SDK's
    // set: reported as this run's error, before the model is shown either.
    if (collision !== undefined) throw authoredFailure('configuration',`host tool ${collision.name} is named like a tool the runtime serves; nothing was run`)
    // **Said before the model is asked anything, and pushed rather than
    // delivered.** These are a property of the session and not of an attempt:
    // counting them as delivered would spend the one retry a tier refusal
    // earns, on every run that narrows a schema at all — and pushing them here
    // rather than from `open` keeps them in order with everything after them.
    for (const notice of narrowingEvents(id, session.model.family, [...session.tools, ...session.hostTools])) {
      await channel.push(notice)
    }
    for (let attempt = 1; ; attempt += 1) {
      const produced = { count: 0 }
      try {
        await runOnce(produced)
        return
      } catch (cause) {
        if (awaitingInput) return
        if (overTime && !gate.signal.aborted) {
          context.execution.state.reason = `${turnTimeFailure().message} Completed work is kept; continue to resume it.`
          throw new ExecutionPaused()
        }
        const refusal = refusalOf(cause)
        if (refusal === null || produced.count > 0 || attempt >= 3) throw schemaRefusal(cause) ?? cause
        const said = slot.refused(refusal.status, refusal.message)
        // **A schema keyword the removal list does not name is reported, not
        // stripped.** `assistant/geminiSchema.ts` holds the ruling; this is the
        // one line that carries it on this engine — a 400 naming a keyword this
        // desk actually sent becomes an error a person can act on, rather than
        // the desk widening its idea of the runtime's contract on being
        // refused.
        if (said.kind === 'other') throw schemaRefusal(cause) ?? cause
        if (said.kind === 'degrade' && said.event !== null) await channel.push(said.event)
      }
    }
  }

  /**
   * The contract's terminal event, on the same ordered stream as the rest.
   *
   * **Not delivered by the iterator's `return()` and not yielded from any
   * `finally`.** Both are the same mistake wearing different hats: a consumer
   * that has stopped listening is owed no terminal event, and an answer of
   * `{ done: false }` to a `return()` hands it a value `for await` discards. So
   * `end` travels the way every other event does — pushed, delivered in order,
   * and once — and where the consumer left early the channel is already
   * abandoned, this push resolves at once, and the event is dropped.
   */
  const finish = async (): Promise<void> => {
    clearTimeout(turnTimer)
    await channel.push({ type: 'end' })
    channel.close()
  }
  /**
   * The run itself starts on the consumer's first `next()`.
   *
   * A consumer that opens a session and leaves without asking for an event has
   * asked the model nothing, and this is what makes that true.
   */
  const open = (): AsyncGenerator<AssistantEvent> => {
    const loop = drive().catch(async (cause: unknown) => {
      // A cancelled run is the viewer stopping the session, or a consumer
      // walking away: it ends the run and there is no failure to report.
      if (isCancelled(cause) || gate.signal.aborted) return
      if(cause instanceof ExecutionPaused){await channel.push({type:'budget_paused',checkpoint:context.execution.snapshot('paused')});return}
      await channel.push(failureEvent(cause,'model'))
    })
    // Nothing else awaits this; a rejection out of the catch above would be
    // unhandled. It cannot reject, and this is what says so.
    void loop.then(finish, finish)
    return channel.drain()
  }

  return eventIterator({ gate, open })
}
