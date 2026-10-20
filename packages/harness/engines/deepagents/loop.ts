/**
 * The `deepagents` engine's loop: LangChain's Deep Agents (`deepagents/browser`, `createDeepAgent`)
 * behind the wire contract (ADR-0001, step 3; ADR-0003). A run whose environment turns LangChain's
 * tracing on is refused before anything else (`tracingAsked`): the tracer is the one way out
 * LangChain has beside the relay.
 *
 * The agent graph Deep Agents builds (a model node, a tools node, middleware hooks around both) runs
 * each stretch of the turn that the `vercel` adapter runs as one `streamText` call; the turn around
 * those stretches (the text tool path, the request that finishes a response cut short, the one
 * reformat request, the critic, the proposal) is the contract's and follows `engines/contract.ts` as
 * the `vercel` adapter does. Everything the run can reach goes through seams the host owns:
 *
 * - **The model is the host's relay, keyless.** The chat model (`model.ts`) sends every request through
 *   `session.model.call`, wrapped in the one retry a refusal earns (`../vercel/retry.ts`).
 * - **The gate is the only tool boundary.** Deep Agents' own middleware (its filesystem tools, the
 *   `task` subagent tool, summarization and the tool-call patcher) is replaced by name with middleware
 *   that does nothing, so none of it is offered or run. The tools the agent is given are declarations
 *   only; every call the model makes is answered by this adapter's `wrapToolCall` hook, which puts a
 *   name outside the phase's offer through the gate and refuses it, and dispatches every other name
 *   through the package's `callTool` (and so `admit` and the gate) or the host tool's own function,
 *   with the contract's events. LangChain's own tool node, and its argument validation, are never
 *   reached: a call the gate must see is never refused before it.
 * - **The package keeps the context.** The ledger, the chat context, the read cache, the index and the
 *   compaction are `context/session.ts`'s, asked on every request from the `wrapModelCall` hook, which
 *   places the execution state, closes the tools on the final request and records each step's
 *   completion. The host's parts of the prompt are `context/session.ts`'s too, asked once per run
 *   before the first request and carried in the system text (ADR-0005).
 *
 * Delegation is the package's (`delegation.ts`): `delegate_tasks` is a host tool awaited inside the
 * step, as on `vercel`. Deep Agents' subagents are off; ADR-0003 records why.
 */
import { createDeepAgent, StateBackend } from 'deepagents/browser'
import { createMiddleware } from 'langchain'
import { DynamicStructuredTool } from '@langchain/core/tools'
import { AIMessage, HumanMessage, SystemMessage, ToolMessage, type BaseMessage } from '@langchain/core/messages'
import { getEnvironmentVariable } from '@langchain/core/utils/env'
import type { ModelMessage } from 'ai'
import { RECOVER_REPEATED_WORK_INSTRUCTIONS, REPEATED_WORK_INSTRUCTIONS } from '../../execution/progress'
import { canFinishResponse, completionFailureCode, FINISH_RESPONSE_INSTRUCTIONS, modelCompletion, type ModelCompletion } from '../../completion'
import { failureEvent, authoredFailure, toolFailureResult, toolInputFailure, AIFailureError } from '../../failures'
import { ExecutionPaused, MAX_REQUEST_ALLOWANCE } from '../../execution/state'
import { EXECUTION_INSTRUCTIONS } from '../../execution/controller'
import { managedContext, contextOf } from '../../context/managed'
import { readDelegationRequest } from '../../delegationContract'
import { assistantLanguageInstructions } from '../../host'
import { DATA_RULE, readingTool } from '../../framing'
import { GateViolation } from '../../toolGate'
import { APPROVAL_HELD } from '../../toolSources'
import { readModelQuestion } from '../../questions'
import { questionFailureResult } from '../../questionFailure'
import { separateImages, type HeldImage, type ImageRouting } from '../../images'
import { capabilitiesFor, type ModelCapabilities } from '../../capabilities'
import { openThinking } from '../../thinking'
import { refusedSchemaKeyword, refusedSchemaSentence } from '../../geminiSchema'
import {
  CRITIC_SYSTEM,
  MAX_CRITIC_TURNS,
  criticCannotRun,
  criticMayCall,
  criticMayRead,
  criticMessage,
  criticRuns,
  compareTheRest,
  critiqueEvent,
  critiqueOnProposal,
  criticNote,
  modelCriticFigures,
  proposalNote,
  openCritique
} from '../../refutation'
import {
  SYSTEM,
  CONVERSATION_SYSTEM,
  TEST_DESIGN_SYSTEM,
  BRIEF_SYSTEM,
  GRAPH_SYSTEM,
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
  shownSchema,
  extractToolCall,
  textToolInstructions,
  textToolResultHeader,
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
  twoProposals,
  refusedUnoffered,
  STATE_HEADER,
  type Proposal
} from '../contract'
import { eventChannel } from '../vercel/channel'
import { retryingCall } from '../vercel/retry'
import { RelayChatModel, type RelaySettings } from './model'
import { humanWithImages, messageText, REPLAY, UNPARSED, type Replay, type WireUsage } from './wire'
import type { AssistantEngine, EndpointCapabilities, EndpointKind, StatePlacement, ThinkingTier } from '../../host'
import type { AssistantEvent, AssistantSession, HostTool, McpTool, McpToolResult, ProposalNote } from '../../engine'
import type { CritiqueRecorder } from '../../refutation'

/**
 * Deep Agents' own middleware, by the names `createDeepAgent` mounts it under (deepagents 1.14.1,
 * `agent.ts`): the filesystem tools (`ls`, `read_file`, `write_file`, `edit_file`, `delete`, `glob`,
 * `grep`, `execute`), the `task` subagent tool, conversation summarization and the tool-call patcher.
 * A custom middleware of the same name replaces each in place (`mergeMiddlewareStack`), so these
 * replacements, which do nothing, are what leaves every one of them off. The prompt caching middleware
 * mounts only for a model Deep Agents recognises as Anthropic's by its class, and this adapter's model
 * is its own; skills, memory, async subagents and human-in-the-loop mount only when asked for, and are
 * not.
 */
export const BUILT_INS_OFF = ['FilesystemMiddleware', 'subAgentMiddleware', 'SummarizationMiddleware', 'patchToolCallsMiddleware'] as const

/** The name the adapter's own boundary middleware is mounted under. */
export const BOUNDARY = 'JudgmentPackBoundary'

/** What a phase of the run answers, behind the one boundary middleware. */
interface Phase {
  /** The names this phase offered the model; any other is refused at the boundary. */
  offered: ReadonlySet<string>
  /** One model request: prepare it, send it, read what came back. */
  step(request: StepRequest, send: (request: StepRequest) => Promise<AIMessage>): Promise<AIMessage>
  /** One tool call: the gate's dispatch, as a tool message. */
  call(call: { id?: string; name: string; args?: unknown }): Promise<ToolMessage>
  /** Set after a step whose request closed the tools, or that reached the step limit: the next model request is not made. */
  stop: boolean
}

/** The request `wrapModelCall` is handed, as far as the boundary changes it. */
interface StepRequest {
  messages: BaseMessage[]
  systemMessage: SystemMessage
  tools: unknown[]
  toolChoice?: unknown
  modelSettings?: Record<string, unknown>
  [member: string]: unknown
}

/**
 * The one middleware this adapter adds: the boundary. Its hooks belong to whichever phase is running.
 *
 * `beforeModel` ends the stretch where the step before it closed the tools or reached its limit (the
 * `vercel` adapter's `stopWhen`); `wrapModelCall` is the step; `wrapToolCall` is the gate's dispatch.
 */
function boundaryMiddleware(phase: () => Phase) {
  return createMiddleware({
    name: BOUNDARY,
    beforeModel: {
      canJumpTo: ['end'],
      hook: () => (phase().stop ? { jumpTo: 'end' as const } : undefined)
    },
    wrapModelCall: async (request, handler) =>
      phase().step(request as unknown as StepRequest, (prepared) => handler(prepared as never) as Promise<AIMessage>),
    wrapToolCall: async (request) => phase().call(request.toolCall)
  })
}

/** The middleware that leaves each of Deep Agents' own off: one that does nothing, under its name. */
function builtInsOff() {
  return BUILT_INS_OFF.map((name) => createMiddleware({ name }))
}

/**
 * The tools the agent is given: declarations only. The name, the description and the schema the
 * endpoint's record says the model is shown; a function that refuses, because every call is answered
 * by the boundary's `wrapToolCall` before LangChain's tool node is reached.
 */
function declared(tools: { name: string; description?: string; schema: unknown }[]): DynamicStructuredTool[] {
  return tools.map(
    (tool) =>
      new DynamicStructuredTool({
        name: tool.name,
        description: tool.description ?? '',
        schema: tool.schema as Record<string, unknown>,
        func: async () => {
          throw new Error(`${tool.name} was reached past the adapter’s boundary; nothing was run`)
        }
      })
  )
}

/** The text of a step's answer. */
const said = (message: AIMessage): string => messageText(message)

/** The completion one step's answer reported, in the contract's shape. */
function completionOf(message: AIMessage): ModelCompletion {
  const metadata = (message.response_metadata ?? {}) as { finishReason?: unknown; usage?: WireUsage }
  return modelCompletion(metadata.finishReason, metadata.usage)
}

/**
 * The run's history in the shape the package's budget and compaction read (`SessionContext.messages`,
 * which counts and archives the `ai` package's message shape), and the way back. Each projected
 * message maps to the message it stands for; the one the compaction adds (its summary) is a new user
 * message. Images are left out of the projection, as the budget counts text.
 */
function projected(messages: BaseMessage[]): { messages: ModelMessage[]; back: Map<ModelMessage, BaseMessage> } {
  const back = new Map<ModelMessage, BaseMessage>()
  const out: ModelMessage[] = []
  for (const message of messages) {
    const type = message.getType()
    let model: ModelMessage
    if (type === 'ai') {
      const ai = message as AIMessage
      const replay = (ai.additional_kwargs as Record<string, unknown> | undefined)?.[REPLAY] as Replay | undefined
      const reasoning =
        replay === undefined
          ? ''
          : replay.family === 'openai-compatible'
            ? replay.reasoning
            : JSON.stringify(replay.family === 'anthropic' ? replay.blocks.filter((block) => block.type !== 'text' && block.type !== 'tool_use') : replay.parts.filter((part) => part.thought === true || typeof part.thoughtSignature === 'string'))
      const text = said(ai)
      model = {
        role: 'assistant',
        content: [
          ...(text === '' ? [] : [{ type: 'text' as const, text }]),
          ...(reasoning === '' || reasoning === '[]' ? [] : [{ type: 'reasoning' as const, text: reasoning }]),
          ...(ai.tool_calls ?? []).map((call) => ({ type: 'tool-call' as const, toolCallId: call.id ?? '', toolName: call.name, input: call.args }))
        ]
      }
    } else if (type === 'tool') {
      const result = message as ToolMessage
      model = { role: 'tool', content: [{ type: 'tool-result', toolCallId: result.tool_call_id, toolName: result.name ?? '', output: { type: 'text', value: messageText(result) } }] }
    } else {
      const content = message.content
      model =
        typeof content === 'string'
          ? { role: 'user', content }
          : { role: 'user', content: (content as { type?: string; text?: string }[]).filter((part) => part.type === 'text').map((part) => ({ type: 'text' as const, text: part.text ?? '' })) }
    }
    back.set(model, message)
    out.push(model)
  }
  return { messages: out, back }
}

/** The projection's result, back as the run's messages. */
function unprojected(messages: ModelMessage[], back: Map<ModelMessage, BaseMessage>): BaseMessage[] {
  return messages.map((message) => back.get(message) ?? new HumanMessage(typeof message.content === 'string' ? message.content : JSON.stringify(message.content)))
}

/** A system message of the head and, where the placement puts it there, the state after it. */
function systemMessage(head: string, tail?: string): SystemMessage {
  return new SystemMessage({ content: [{ type: 'text', text: head }, ...(tail === undefined ? [] : [{ type: 'text', text: tail }])] })
}

/**
 * Where this endpoint gets the state after the first request: the `vercel` adapter's rule
 * (`budget.json#/$defs/stateLocation`), decided once per run so the prefix does not move.
 */
function statePlacement(family: EndpointKind, tier: ThinkingTier, capabilities: EndpointCapabilities): StatePlacement {
  if (capabilities.strictAlternation === true) return 'system-tail'
  if (capabilities.statePlacement !== undefined) return capabilities.statePlacement
  return family === 'anthropic' && tier !== 'off' ? 'system-tail' : 'trailing-user'
}

/** The state's text: the header, then whatever is current for this request. */
function stateText(...parts: (string | undefined)[]): string {
  return [STATE_HEADER, ...parts].filter(Boolean).join('\n')
}

/**
 * One request's messages and system text, with the state placed. Where the history ends with a user
 * message (the request on the first request, an instruction on a later one) the state is a second
 * text part of it; after a tool result it is one user message after the history, or the end of the
 * system text, as the placement says. The history itself is never changed.
 */
function placeState(placement: StatePlacement, history: BaseMessage[], system: string, state: string): { system: SystemMessage; messages: BaseMessage[] } {
  const last = history.at(-1)
  if (last !== undefined && last.getType() === 'human') {
    const parts = typeof last.content === 'string' ? [{ type: 'text', text: last.content }] : [...(last.content as Record<string, unknown>[])]
    const carried = new HumanMessage({ content: [...parts, { type: 'text', text: state }] as never })
    return { system: systemMessage(system), messages: [...history.slice(0, -1), carried] }
  }
  if (placement === 'trailing-user') return { system: systemMessage(system), messages: [...history, new HumanMessage(state)] }
  return { system: systemMessage(system, state), messages: history }
}

// Provider text is inspected only for the existing bounded capability-negotiation rules. It is never
// used as a user-facing failure or persisted diagnostic.
function endpointSentence(body: unknown): string {
  if (typeof body !== 'string' || body.length > 16384) return ''
  try {
    const value = JSON.parse(body) as { error?: unknown; message?: unknown }
    const error = value?.error as { message?: unknown } | string | undefined
    return typeof error === 'string' ? error : typeof error?.message === 'string' ? error.message : typeof value?.message === 'string' ? value.message : ''
  } catch {
    return ''
  }
}

/**
 * The environment variables by which LangChain and LangSmith turn their tracer on (`@langchain/core`,
 * `isTracingEnabled`; LangSmith's `isEnvTracingEnabled`; the older `LANGCHAIN_TRACING`). Where a host
 * has a `process.env` (Node, a test runner, a page with a `process` shim), one of them set makes
 * LangChain's tracer send each run, prompts and answers included, through the global `fetch` with the
 * environment's LangSmith key: a way out beside the relay capability (review of #47, row 1). A browser
 * page has no `process`, and LangChain reads none of them there.
 */
export const TRACING_VARIABLES = ['LANGSMITH_TRACING_V2', 'LANGCHAIN_TRACING_V2', 'LANGSMITH_TRACING', 'LANGCHAIN_TRACING'] as const

/** The tracing variable the environment sets, if any: any value but empty or `false` counts. */
export function tracingAsked(): string | undefined {
  return TRACING_VARIABLES.find((name) => {
    const value = getEnvironmentVariable(name)
    return value !== undefined && value !== '' && value.toLowerCase() !== 'false'
  })
}

/**
 * The refusal a run gets where the environment turns tracing on.
 *
 * **Refused, not forced off, because forcing it off does not hold on every path.** LangChain decides
 * whether to trace from the LangSmith run tree current in the run's async context, and reads the
 * environment wherever there is none; LangGraph and LangChain start parts of a run in contexts that
 * carry none (`runWithConfig(..., avoidCreatingRootRunTree)`). Measured: with a run tree whose
 * tracing is off set around the whole run, the tracer still appeared inside the model node and
 * reached the global `fetch`. So a run starts only where the environment asks for no tracing, and
 * otherwise ends at once with this, before an agent is built or a request made.
 */
function tracingRefusal(variable: string): AIFailureError {
  return authoredFailure(
    'configuration',
    `the environment turns LangChain's tracing on (${variable}), which would send the run through the global fetch, beside the relay; the run was refused and nothing was sent. Unset the LangSmith and LangChain tracing variables to run this engine`
  )
}

/**
 * The original failure out of whatever LangChain and LangGraph wrapped it in: a middleware's error
 * carries what it caught as its `cause`.
 */
function unwrapped(cause: unknown): unknown {
  let held = cause
  for (let depth = 0; depth < 6; depth += 1) {
    const name = (held as { name?: unknown } | null)?.name
    if (name !== 'MiddlewareError' && name !== 'NodeError') return held
    const inner = (held as { cause?: unknown }).cause
    if (inner === undefined || inner === null) return held
    held = inner
  }
  return held
}

export function runDeepAgents(session: AssistantSession, id: AssistantEngine): AsyncIterable<AssistantEvent> {
  return managedContext(session, (held) => runDeepAgentsLoop(held, id))
}

export function runDeepAgentsLoop(session: AssistantSession, id: AssistantEngine): AsyncIterable<AssistantEvent> {
  // The channel and the gate, in the order the `vercel` adapter opens them (see its loop).
  let closeRun = () => {}
  const channel = eventChannel({ onAbandon: () => closeRun() })
  const gate = openRun(session, () => channel.abandon())
  closeRun = gate.close
  const context = contextOf(session)
  const callTool = guardedCallTool(session, gate.signal)
  const capabilities: ModelCapabilities = context.capabilities ?? capabilitiesFor(session.model)
  const nativeTools = capabilities.nativeToolCalls
  const schemaOf = (tool: McpTool) => shownSchema(capabilities.toolSchemas, tool)
  const slot = openThinking(session, capabilities.reasoning)
  /** The tier's members, held for an attempt; a refusal of the tier starts a new attempt. */
  let tier: Record<string, unknown> | null = null
  const placement = statePlacement(session.model.family, session.thinking.tier, capabilities)
  const offered = new Set([...session.tools, ...session.hostTools].map((tool) => tool.name))
  const collision = session.hostTools.find((tool) => session.tools.some((served) => served.name === tool.name))
  const inputPause = new AbortController()
  const turnClock = new AbortController()
  let overTime = false
  let turnTimer: ReturnType<typeof setTimeout> | undefined
  const executionSignal = AbortSignal.any([gate.signal, inputPause.signal, turnClock.signal])
  let awaitingInput = false
  /**
   * Images a result carried (agent#5): a model whose record says `vision` is shown each result's images
   * after the results they belong to; any other is shown the host's rendering inside the result.
   */
  const routing: ImageRouting = {
    vision: capabilities.vision && (!nativeTools || capabilities.strictAlternation !== true || session.model.family === 'anthropic'),
    resolve: (reference) => context.ledger.image(reference),
    signal: executionSignal
  }

  const runOnce = async (produced: { count: number }): Promise<void> => {
    const deliver = async (event: AssistantEvent): Promise<void> => {
      if (event.type !== 'message_progress' || event.text !== '') produced.count += 1
      await channel.push(event)
    }
    /** The critic's recorder, while the critic is running: the loop's own dispatch also shows it the answers. */
    let recording: CritiqueRecorder | null = null
    let toolsClosed = false
    let progressStep: 'work' | 'recover' | 'finish' = 'work'
    const turnLimit = context.execution.remaining
    const finalInstructions =
      'This is the final request within this turn budget. Tools are unavailable. Answer the current question using results already obtained. If the available information is insufficient, explain exactly what is known and what remains missing. Do not invent results, claim unsaved changes, or turn a diagnostic question into a new proposal.'
    // Refusals are journaled as not-started operations, with the exact attempted arguments. They never
    // reach a host tool or the runtime.
    const rejectToolStep = async (name: string, args: unknown): Promise<McpToolResult | undefined> => {
      if (!toolsClosed && !context.execution.progress.rejects(name, args)) return
      const repetition = context.execution.progress.stalled
      const result = toolFailureResult(
        new AIFailureError({
          version: 1,
          code: repetition ? 'repeated-work' : 'invalid-response',
          source: 'desk',
          outcome: 'not-started',
          detail: toolsClosed
            ? 'This request is reserved for a final answer. This tool call was not executed. Use completed results or explain what remains.'
            : 'This call repeats completed work with the same arguments and was not executed. Use its retained result, choose a different concrete action, or explain the remaining blocker.'
        })
      )
      const callId = crypto.randomUUID()
      await deliver({ type: 'tool_call', callId, name, args })
      await deliver({ type: 'tool_result', callId, name, isError: true, text: result.content[0]!.text, structured: result.structuredContent })
      return result
    }

    /** A proposal delivered through `propose_pack`; the fence is the fallback. */
    let toolProposal: Proposal | null = null
    /** What one call gives the model: its bounded result, that result as a data block where a reading tool answered, and the images a vision model is shown. */
    type Ran = { result: McpToolResult; images: HeldImage[]; framed?: string }
    const bare = (result: McpToolResult): Ran => ({ result, images: [] })
    const asData = (name: string, result: McpToolResult, images: HeldImage[]): Ran => ({ result, images, framed: context.frame(name, JSON.stringify(result)) })
    const outcome = (result: McpToolResult) => ({ text: textOf(result), isError: Boolean(result.isError), structured: result.structuredContent })

    /** One runtime call, wherever the model made it: natively, or in text (agent#4). */
    const runRuntime = async (name: string, input: unknown, origin?: 'harness'): Promise<Ran> => {
      // The gate is handed the call the model made, unchanged: the gate is the layer that rewrites a
      // call (the rehearsal flag) and the one that reports it.
      const args = (input ?? {}) as Record<string, unknown>
      if (awaitingInput) throw new DOMException('Waiting for input', 'AbortError')
      executionSignal.throwIfAborted()
      const refusal = await rejectToolStep(name, args)
      if (refusal) return bare(refusal)
      const correlation = { ...(session.interactive ? { callId: crypto.randomUUID() } : {}), ...(origin === undefined ? {} : { origin }) }
      await deliver({ type: 'tool_call', ...correlation, name, args })
      let answer: McpToolResult
      try {
        const limit = context.policy.toolCallMilliseconds
        answer = await withDeadline(() => callTool(name, args), executionSignal, limit, () => toolTimeout(limit))
      } catch (cause) {
        if (isCancelled(cause)) return bare({ content: [{ type: 'text', text: 'the session was stopped before this call was made; nothing was written' }], isError: true })
        const result = toolFailureResult(cause)
        await deliver({ type: 'tool_result', ...correlation, name, isError: true, text: result.content[0]!.text, structured: result.structuredContent })
        return bare(result)
      }
      const reported = outcome(answer)
      await deliver({ type: 'tool_result', ...correlation, name, isError: reported.isError, text: reported.text, ...(reported.structured === undefined ? {} : { structured: reported.structured }) })
      // Only what the runtime answered is a check; a refusal above never reaches the recorder.
      recording?.saw(name, reported.text, args, reported.structured)
      const separated = await separateImages(answer, routing)
      return asData(name, await context.result(name, separated.result), separated.images)
    }

    /** One host tool call, wherever the model made it. */
    const runHost = async (tool: HostTool, input: unknown): Promise<Ran> => {
      const args = (input ?? {}) as Record<string, unknown>
      if (awaitingInput) throw new DOMException('Waiting for input', 'AbortError')
      executionSignal.throwIfAborted()
      const refusal = await rejectToolStep(tool.name, args)
      if (refusal) return bare(refusal)
      // ADR-0008: an act call of a tool with a source always carries an id, whether or not the run's
      // events carry one, so the decision taken before its call is its own, never another call's.
      const correlation = session.interactive || context.sources?.needsApproval(tool.name) ? { callId: crypto.randomUUID() } : {}
      // ADR-0008: an act call the host's policy asks about is held for the person, never run: its
      // approval_requested in place of the call, and the run's next request is refused.
      const held = await context.approval(tool.name, args, correlation.callId)
      if (held !== undefined) {
        await deliver(held)
        return bare({ content: [{ type: 'text', text: APPROVAL_HELD }], isError: true })
      }
      // A question pauses the turn and is reported as user_question; every other call, the delegation
      // among them (agent#14), is a call and a result.
      if (tool.interaction !== 'question') await deliver({ type: 'tool_call', ...correlation, name: tool.name, args })
      let answer: McpToolResult
      try {
        // agent#10: no host tool is the critic's; refused before anything of the host's runs.
        context.admit(tool.name, 'host')
        if (tool.interaction === 'delegation') {
          // An awaited call inside this step (agent#14): the coordinator runs the batch while the call is
          // open, bounded by its children's time limits and by this run's signal, not by the tool-call deadline.
          readDelegationRequest(args)
          answer = await withAbort(() => tool.execute(args, executionSignal), executionSignal)
        } else if (tool.interaction === 'question') {
          // The question pause (ADR-0003, decision 2): the question is read closed first, so a malformed one
          // is a failed result the model corrects in this turn; a well-formed one ends the turn, unanswered.
          const question = readModelQuestion(args)
          if (awaitingInput) throw new DOMException('Waiting for input', 'AbortError')
          awaitingInput = true
          await deliver({ type: 'user_question', id: crypto.randomUUID(), question })
          inputPause.abort()
          throw new DOMException('Waiting for input', 'AbortError')
        } else if (tool.name === PROPOSE_PACK_TOOL) {
          // The proposal, delivered as a tool call, read by the contract's reader; the host's own
          // `execute` is never reached.
          toolProposal = readProposedPack(args)
          answer = { content: [{ type: 'text', text: PROPOSAL_RECEIVED }] }
        } else {
          const limit = context.policy.toolCallMilliseconds
          answer = await withDeadline((signal) => tool.execute(args, signal, correlation.callId), executionSignal, limit, () => toolTimeout(limit))
        }
      } catch (cause) {
        if (isCancelled(cause)) {
          if (awaitingInput) throw cause
          return bare({ content: [{ type: 'text', text: 'the session was stopped before this call was made; nothing was written' }], isError: true })
        }
        const result = tool.interaction === 'question' && !(cause instanceof GateViolation) ? questionFailureResult(cause) : toolFailureResult(cause)
        await deliver({ type: 'tool_result', ...correlation, name: tool.name, isError: true, text: result.content[0]!.text, structured: result.structuredContent })
        return bare(result)
      }
      const reported = outcome(answer)
      await deliver({ type: 'tool_result', ...correlation, name: tool.name, isError: reported.isError, text: reported.text, ...(reported.structured === undefined ? {} : { structured: reported.structured }) })
      const separated = await separateImages(answer, routing)
      const bounded = await context.result(tool.name, separated.result)
      // A reading tool's answer is data (agent#12); propose_pack's acknowledgement is the adapter's own.
      return readingTool(tool) && tool.name !== PROPOSE_PACK_TOOL ? asData(tool.name, bounded, separated.images) : { result: bounded, images: separated.images }
    }

    /**
     * A call to a tool this phase never offered, put through the gate as the model made it, so the
     * layer that holds is the one that reports, and refused to the model whatever the gate answered.
     * The native path and the text path alike.
     */
    const refuseUnoffered = async (name: string, input: unknown): Promise<McpToolResult> => {
      await deliver({ type: 'tool_call', name, args: input ?? {} })
      const text = await refusedUnoffered(callTool, name, (input ?? {}) as Record<string, unknown>)
      await deliver({ type: 'tool_result', name, isError: true, text })
      return { content: [{ type: 'text', text }], isError: true }
    }

    /** One call, wherever it was made, through the same dispatch and so the same gate: only a name this phase offered is looked up. */
    const dispatch = async (name: string, args: Record<string, unknown>, offeredHere: ReadonlySet<string>): Promise<Ran> => {
      if (Object.hasOwn(args, UNPARSED)) {
        // Arguments that were not a JSON object: nothing is run, and the model is told why.
        const result = toolFailureResult(toolInputFailure('The call’s arguments are not a JSON object; nothing was run. Call the tool again with an object of its arguments.'))
        const callId = crypto.randomUUID()
        await deliver({ type: 'tool_call', callId, name, args: { [UNPARSED]: args[UNPARSED] } })
        await deliver({ type: 'tool_result', callId, name, isError: true, text: result.content[0]!.text, structured: result.structuredContent })
        return bare(result)
      }
      if (!offeredHere.has(name)) {
        const rejected = await rejectToolStep(name, args)
        return bare(rejected ?? (await refuseUnoffered(name, args)))
      }
      const host = session.hostTools.find((tool) => tool.name === name)
      return host !== undefined ? runHost(host, args) : runRuntime(name, args)
    }

    /** What the model is shown of one call, as the tool message the agent carries. */
    const toolMessage = (call: { id?: string; name: string }, ran: Ran): ToolMessage =>
      new ToolMessage({
        tool_call_id: call.id ?? '',
        name: call.name,
        // A reading tool's answer as its data block (agent#12); any other, the bounded answer as JSON.
        content: ran.framed ?? JSON.stringify(ran.result),
        status: ran.result.isError === true ? 'error' : 'success',
        ...(ran.images.length > 0 ? { artifact: { images: ran.images } } : {})
      })

    /** The messages that carry a text call's answer back: the model's own answer, then the result, as the next user message. */
    const textTurn = (answer: AIMessage, name: string, ran: Ran): BaseMessage[] => {
      const text = `${textToolResultHeader(name, ran.result.isError === true)}\n${ran.framed ?? JSON.stringify(ran.result)}`
      return [answer, ran.images.length > 0 ? humanWithImages(text, ran.images) : new HumanMessage(text)]
    }

    // One model capability and one retry rule, shared by the loop and the critic.
    const call = retryingCall(session.model.call, {
      signal: executionSignal,
      onRefused: async (refused) => {
        context.execution.recordRefusal(recording !== null ? 'review' : synthesis ? 'synthesis' : 'work', refused)
        if (refused.waitMs !== undefined) await channel.push({ type: 'retry', attempt: refused.attempt })
      },
      onAttempt: (attempt) => context.execution.beginAttempt(attempt),
      // The last request of the allowance is the turn's tool-free final answer: a retry never takes it.
      mayRetry: () => context.execution.remaining > 1
    })

    let publicText = ''
    /** The passage of reasoning in progress, said whole when it ends. */
    const listener = {
      text: async (delta: string) => {
        if (!session.interactive) return
        publicText += delta
        await deliver({ type: 'message_progress', text: streamingProse(publicText) })
      },
      reasoning: async (delta: string) => {
        if (delta !== '') await deliver({ type: 'reasoning', text: delta, done: false })
      },
      reasoningEnd: async (text: string) => {
        await deliver({ type: 'reasoning', text, done: true })
        slot.sawReasoning()
      }
    }
    const relayModel = (name: string) =>
      new RelayChatModel({
        family: session.model.family,
        model: name,
        call,
        signal: executionSignal,
        maxOutputTokens: () => context.policy.outputTokens,
        reasoningField: capabilities.streamingQuirks.reasoningField,
        wholeAnswer: capabilities.streamingQuirks.wholeJsonAnswer,
        images: { count: context.policy.imagesPerRequest, bytes: context.policy.imageBytesPerRequest },
        listener,
        ids: () => `call-${crypto.randomUUID()}`
      })
    const model = relayModel(session.model.model)

    /** A failure a step met, kept as it was thrown: LangChain wraps what a middleware rethrows. */
    let streamed: unknown = null
    const instructions =
      (session.purpose === 'graph' ? GRAPH_SYSTEM : session.purpose === 'brief' ? BRIEF_SYSTEM : session.purpose === 'test-design' ? TEST_DESIGN_SYSTEM : session.allowConversation ? CONVERSATION_SYSTEM : SYSTEM) +
      (offersProposeTool(session.hostTools) ? PROPOSE_PACK_INSTRUCTIONS : '') +
      DATA_RULE +
      assistantLanguageInstructions(session.replyLanguage)
    // The tools a native request carries, counted against the budget; on an endpoint without native
    // tool calls they are in the system text instead.
    const schemas = nativeTools ? [...session.tools, ...session.hostTools].map(({ name, description, inputSchema }) => ({ name, description, inputSchema })) : []
    const described = (names: ReadonlySet<string>) =>
      textToolInstructions([...session.hostTools, ...session.tools].filter((tool) => names.has(tool.name)).map((tool) => ({ name: tool.name, description: tool.description, schema: schemaOf(tool) })))
    const toolSection = nativeTools ? '' : '\n\n' + described(offered)
    tier = slot.members()
    // The enabled dialect spends its budget out of the maximum, so the reserve is the table's.
    context.reserveOutput(tier?.max_tokens)
    /** What one request asks besides its messages: the tier, and one call per turn where the record says so. */
    const settings = (toolsOpen: boolean): RelaySettings => ({ tier, oneCallPerTurn: !capabilities.parallelToolCalls && nativeTools && toolsOpen, placement })
    // The stable prefix (ADR-0005): the package's guidance, then the host's parts, then the tools
    // described in text where the endpoint takes no native tool calls.
    const system = context.system(instructions + '\n' + EXECUTION_INSTRUCTIONS, toolSection)
    // The request text is the first text part of the first user message on every request.
    const opening = await context.opening(session.prompt, context.system(instructions, toolSection) + '\n' + context.execution.guidance(), schemas, session.context?.currentRequest)
    let messages: BaseMessage[] = [new HumanMessage({ content: opening.length === 1 ? opening[0]! : opening.map((text) => ({ type: 'text', text })) })]
    /** The history the last step was sent, without its state. */
    let lastMessages = messages
    /** The answer the last step returned. */
    let lastAnswer: AIMessage | null = null
    let completion: ModelCompletion | undefined
    let lastToolCalls = 0
    let finalText = ''
    /** The text of the step in progress, and whether a step has started. */
    let final = ''
    let synthesis = false
    let reformatting = false
    let steps = 0
    let proposal: Proposal | null = null
    /** The answer whose proposal could not be read: delivered whole before the run reports why. */
    let unread: { text: string; cause: unknown } | null = null
    const keepUnread = async (): Promise<void> => {
      const text = unread === null ? '' : wholeAnswer(unread.text)
      unread = null
      if (text !== '' && !gate.isClosed()) await deliver({ type: 'message', text })
    }

    /** The budgeted history: the package's compaction, over the projection, mapped back. */
    const budgeted = async (history: BaseMessage[], instructionsNow: string): Promise<BaseMessage[]> => {
      const view = projected(history)
      return unprojected(await context.messages(view.messages, instructionsNow, schemas), view.back)
    }

    // ------------------------------------------------------------------------------------------
    // The loop's phase
    // ------------------------------------------------------------------------------------------
    /** Steps started in the agent run in progress. */
    let stepInRun = 0
    const loopPhase: Phase = {
      offered,
      stop: false,
      async step(request, send) {
        // A step boundary is the end of the turn before it (the thinking slot's evidence).
        if (steps > 0) {
          const noticed = slot.turnEnded(final !== '')
          if (noticed !== null) await deliver(noticed)
        }
        const index = nativeTools ? stepInRun : steps
        stepInRun += 1
        steps += 1
        lastToolCalls = 0
        completion = undefined
        final = ''
        publicText = ''
        if (session.interactive) await deliver({ type: 'message_progress', text: '' })
        if (synthesis) await deliver({ type: 'response_recovery' })
        await context.execution.retainProgress(false)
        progressStep = synthesis ? 'finish' : context.execution.progress.nextStep(context.execution.remaining)
        if (progressStep === 'finish' && context.execution.progress.stalled && !synthesis) await deliver({ type: 'response_recovery' })
        toolsClosed = synthesis || progressStep === 'finish' || index >= turnLimit - 1 || context.execution.remaining <= 1
        const state = stateText(
          context.execution.requestState(),
          session.context?.currentState,
          reformatting ? REFORMAT_INSTRUCTIONS : synthesis ? FINISH_RESPONSE_INSTRUCTIONS : progressStep === 'recover' ? RECOVER_REPEATED_WORK_INSTRUCTIONS : progressStep === 'finish' ? REPEATED_WORK_INSTRUCTIONS : toolsClosed ? finalInstructions : ''
        )
        // Budgeted as one request: the system text and the state together.
        lastMessages = await budgeted(request.messages, system + '\n' + state)
        const placed = placeState(placement, lastMessages, system, state)
        const answer = await sendStep(request, send, placed, toolsClosed, offered)
        completion = completionOf(answer)
        context.execution.recordCompletion(completion, synthesis ? 'synthesis' : 'work')
        final = said(answer)
        lastToolCalls = answer.tool_calls?.length ?? 0
        lastAnswer = answer
        // The `vercel` adapter's `stopWhen`: the step limit, or a request that closed the tools.
        this.stop = toolsClosed || stepInRun >= (synthesis ? 1 : turnLimit)
        return answer
      },
      async call(toolCall) {
        const ran = await dispatch(toolCall.name, (toolCall.args ?? {}) as Record<string, unknown>, offered)
        if (awaitingInput) throw new DOMException('Waiting for input', 'AbortError')
        return toolMessage(toolCall, ran)
      }
    }

    /**
     * One request out: the offer checked, the state placed, the tools closed where they are, and the
     * model's answer, or the failure kept as it was thrown.
     */
    const sendStep = async (
      request: StepRequest,
      send: (request: StepRequest) => Promise<AIMessage>,
      placed: { system: SystemMessage; messages: BaseMessage[] },
      closed: boolean,
      offeredHere: ReadonlySet<string>
    ): Promise<AIMessage> => {
      // **The gate's offer is the whole of the agent's.** A tool any other middleware added to the
      // request (a Deep Agents built-in left on) ends the run before anything is sent.
      const extra = (request.tools as { name?: unknown }[]).map((tool) => String(tool.name ?? '')).filter((name) => !offeredHere.has(name))
      if (extra.length > 0) throw authoredFailure('configuration', `the agent offered ${extra.join(', ')} beside the gate’s offer; nothing was sent`)
      try {
        return await send({
          ...request,
          messages: placed.messages,
          systemMessage: placed.system,
          tools: closed && nativeTools ? [] : request.tools,
          ...(closed && nativeTools ? { toolChoice: 'none' } : {}),
          modelSettings: { ...(request.modelSettings ?? {}), judgmentPack: settings(!closed) }
        })
      } catch (cause) {
        streamed ??= unwrapped(cause)
        throw cause
      }
    }

    let phase: Phase = loopPhase
    const nativeDeclarations = nativeTools
      ? declared([...session.hostTools, ...session.tools].map((tool) => ({ name: tool.name, description: tool.description, schema: schemaOf(tool) })))
      : []
    const agentFor = (chatModel: RelayChatModel, tools: DynamicStructuredTool[]) =>
      createDeepAgent({
        model: chatModel,
        tools,
        middleware: [...builtInsOff(), boundaryMiddleware(() => phase)],
        // Deep Agents' in-memory state backend, its default, named: with the file tools off it holds nothing.
        backend: (config) => new StateBackend(config)
      })
    const loopAgent = agentFor(model, nativeDeclarations)

    /** One stretch of the turn: the agent graph, from these messages until the model stops calling tools. */
    const stretch = async (agent: ReturnType<typeof agentFor>, input: BaseMessage[]): Promise<void> => {
      stepInRun = 0
      phase.stop = false
      try {
        await withAbort(() => agent.invoke({ messages: input }, { signal: executionSignal, recursionLimit: 10_000 }), executionSignal)
      } catch (cause) {
        if (awaitingInput) throw new DOMException('Waiting for input', 'AbortError')
        throw streamed ?? unwrapped(cause)
      }
    }

    try {
      for (;;) {
        completion = undefined
        lastToolCalls = 0
        lastAnswer = null
        await stretch(loopAgent, messages)
        if (streamed !== null) throw streamed
        // The last turn's own accounting.
        const noticed = slot.turnEnded(final !== '')
        if (noticed !== null) await deliver(noticed)
        finalText = final
        const answered = lastAnswer as AIMessage | null
        if (!nativeTools && answered !== null) {
          // The text path (agent#4): an answer that makes one tool call in a fenced JSON block is a tool
          // turn, through the same dispatch and so the same gate; its result goes back as the next message.
          const text = extractToolCall(finalText)
          if (text !== null) {
            lastToolCalls = 1
            if ('problem' in text) {
              if (!toolsClosed && !synthesis) {
                messages = [...lastMessages, answered, new HumanMessage(`${textToolResultHeader('the tool call', true)}\nThe call was not made: ${text.problem}.`)]
                continue
              }
            } else if (toolsClosed || synthesis) {
              // Tools are closed for this request: refused as a native call would be, and the run fails below.
              await rejectToolStep(text.call.tool, text.call.arguments)
            } else {
              const ran = await dispatch(text.call.tool, text.call.arguments, offered)
              if (awaitingInput) throw new DOMException('Waiting for input', 'AbortError')
              messages = [...lastMessages, ...textTurn(answered, text.call.tool, ran)]
              continue
            }
          }
        }
        // A proposal delivered through the tool is a finished answer, whatever the text after it says.
        if (toolProposal === null && canFinishResponse(completion, finalText, lastToolCalls, context.execution.remaining, synthesis || (toolsClosed && context.execution.progress.stalled))) {
          synthesis = true
          // Under `system-tail` the instruction travels in the state alone.
          messages = placement === 'system-tail' ? lastMessages : [...lastMessages, new HumanMessage(FINISH_RESPONSE_INSTRUCTIONS)]
          continue
        }
        const finished = completion as ModelCompletion | undefined
        if (toolProposal === null && !context.execution.progress.stalled && !finalText.trim() && steps >= turnLimit && finished?.finishReason !== 'content-filter' && finished?.finishReason !== 'length' && finished?.finishReason !== 'error') throw new ExecutionPaused()
        if (toolProposal === null && ((toolsClosed && lastToolCalls > 0) || !finalText.trim() || finished?.finishReason === 'length' || finished?.finishReason === 'content-filter' || finished?.finishReason === 'error')) {
          const stalled = context.execution.progress.stalled && !['length', 'content-filter', 'error'].includes(finished?.finishReason ?? '')
          throw new AIFailureError({ version: 1, code: stalled ? 'repeated-work' : completionFailureCode(finished), source: stalled ? 'desk' : 'model', outcome: 'unknown', ...(finished ? { completion: finished } : {}) })
        }
        if (toolProposal !== null || !session.allowConversation || hasProposalFence(finalText)) {
          try {
            if (toolProposal !== null && hasProposalFence(finalText)) {
              toolProposal = null
              throw twoProposals()
            }
            proposal = toolProposal ?? extractProposal(finalText)
          } catch (cause) {
            // One reformat request, within the allowance: tool-free, shown its own answer.
            if (unread === null && context.execution.remaining > 0 && answered !== null) {
              unread = { text: finalText, cause }
              reformatting = synthesis = true
              messages = [...lastMessages, answered, new HumanMessage(reformatRequest(cause))]
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
      // A proposal made through propose_pack is finished work: a later request of the turn that fails
      // does not take it with it.
      const made = toolProposal as Proposal | null
      if (made !== null && !gate.signal.aborted && !gate.isClosed()) {
        toolProposal = null
        if (awaitingInput) {
          await deliver({ type: 'proposal', document: made.document, unknowns: made.unknowns })
          throw cause
        }
        await deliver({ type: 'proposal', document: made.document, unknowns: made.unknowns, note: proposalNote('turn-unfinished', overTime ? turnTimeFailure() : cause, 'model') })
        return
      }
      throw cause
    }
    if (proposal === null && unread !== null) {
      const first = unread as { text: string; cause: unknown }
      await keepUnread()
      const prose = proseOf(finalText)
      if (prose !== '') await deliver({ type: 'message', text: prose })
      throw first.cause
    }
    if (proposal === null) {
      await deliver({ type: 'message', text: proseOf(finalText) })
      if (context.execution.remaining <= 0 && context.execution.state.plan.some((step) => step.status !== 'completed')) throw new ExecutionPaused()
      return
    }
    const prose = (unread === null ? '' : proseOf((unread as { text: string }).text)) || proseOf(finalText)
    if (prose !== '') await deliver({ type: 'message', text: prose })

    // ------------------------------------------------------------------------------------------
    // The critic: a second agent on the same relay, the same boundary and the same dispatch, offered
    // its allow-list, after the proposal exists and before it is delivered.
    // ------------------------------------------------------------------------------------------
    let critique = null as ReturnType<CritiqueRecorder['critique']> | null
    let note: ProposalNote | null = null
    const review = criticRuns(session, context.policy)
    const cannot = review ? criticCannotRun(session.testPrompt) : null
    if (cannot !== null) {
      critique = cannot
      await deliver(critiqueEvent(cannot))
    } else if (review) {
      const recorder = openCritique({ cases: context.criticCases, document: proposal.document })
      recording = recorder
      toolsClosed = false
      context.beginCriticPhase()
      const before = { used: context.execution.state.used, rows: (context.execution.state.completions ?? []).length }
      let criticModelId = session.model.model
      let harnessCalls = 0
      const figures = () => ({ ...modelCriticFigures(criticModelId, before, context.execution.state), ...(harnessCalls === 0 ? {} : { harnessCalls }) })
      let criticText = ''
      const criticOffered = new Set(
        [...session.hostTools, ...session.tools]
          .map((tool) => tool.name)
          .filter((name) => (criticMayCall(name) ? session.tools.some((tool) => tool.name === name) : criticMayRead(name) && session.hostTools.some((tool) => tool.name === name)))
      )
      const criticSystem = context.system(CRITIC_SYSTEM + DATA_RULE + assistantLanguageInstructions(session.replyLanguage), nativeTools ? '' : '\n\n' + described(criticOffered))
      let criticMessages: BaseMessage[] = []
      let criticHeld = criticMessages
      let criticSteps = 0
      let criticAnswer: AIMessage | null = null
      let unfinished: unknown = null
      try {
        // A first message that does not fit even as a retained preview refuses the pass with a note.
        criticMessages = [new HumanMessage(await context.prompt(criticMessage(session.testPrompt, proposal.document, context.criticCases), criticSystem, schemas))]
        criticHeld = criticMessages
        criticModelId = context.models(id).critic
        const criticModel = criticModelId === session.model.model ? model : relayModel(criticModelId)
        const criticDeclarations = nativeTools
          ? declared([...session.hostTools, ...session.tools].filter((tool) => criticOffered.has(tool.name)).map((tool) => ({ name: tool.name, description: tool.description, schema: schemaOf(tool) })))
          : []
        const criticPhase: Phase = {
          offered: criticOffered,
          stop: false,
          async step(request, send) {
            // The critic's turns are this session's turns.
            const noticed = slot.turnEnded(criticText !== '')
            if (noticed !== null) await deliver(noticed)
            criticText = ''
            const at = nativeTools ? stepInRun : criticSteps
            stepInRun += 1
            progressStep = context.execution.progress.nextStep(Math.min(context.execution.remaining, MAX_CRITIC_TURNS - at))
            toolsClosed = progressStep === 'finish' || at >= MAX_CRITIC_TURNS - 1
            const step = progressStep === 'recover' ? RECOVER_REPEATED_WORK_INSTRUCTIONS : progressStep === 'finish' ? REPEATED_WORK_INSTRUCTIONS : toolsClosed ? finalInstructions : ''
            // The critic's step note carries the header like every state, and is placed the same way.
            const stepNote = step ? stateText(step) : ''
            const held = await budgeted(request.messages, criticSystem + (stepNote ? '\n' + stepNote : ''))
            criticHeld = held
            const placed = stepNote ? placeState(placement, held, criticSystem, stepNote) : { system: systemMessage(criticSystem), messages: held }
            const answer = await sendStep(request, send, placed, toolsClosed, criticOffered)
            const done = completionOf(answer)
            context.execution.recordCompletion(done, 'review')
            if (['length', 'content-filter', 'error'].includes(done.finishReason)) streamed ??= new AIFailureError({ version: 1, code: completionFailureCode(done), source: 'model', outcome: 'unknown', completion: done })
            criticText = said(answer)
            criticAnswer = answer
            this.stop = toolsClosed || stepInRun >= MAX_CRITIC_TURNS
            return answer
          },
          async call(toolCall) {
            const ran = await dispatch(toolCall.name, (toolCall.args ?? {}) as Record<string, unknown>, criticOffered)
            if (awaitingInput) throw new DOMException('Waiting for input', 'AbortError')
            return toolMessage(toolCall, ran)
          }
        }
        phase = criticPhase
        const criticAgent = agentFor(criticModel, criticDeclarations)
        for (;;) {
          criticAnswer = null
          await stretch(criticAgent, criticMessages)
          if (nativeTools || streamed !== null) break
          // The text path: a call in the critic's answer runs through the same dispatch, within the
          // tools the critic was offered, and it asks again.
          const text = extractToolCall(criticText)
          const answered = criticAnswer as AIMessage | null
          if (text === null || answered === null) break
          if ('problem' in text) {
            if (toolsClosed) break
            criticMessages = [...criticHeld, answered, new HumanMessage(`${textToolResultHeader('the tool call', true)}\nThe call was not made: ${text.problem}.`)]
          } else if (toolsClosed) {
            await rejectToolStep(text.call.tool, text.call.arguments)
            break
          } else {
            const ran = await dispatch(text.call.tool, text.call.arguments, criticOffered)
            if (awaitingInput) throw new DOMException('Waiting for input', 'AbortError')
            criticMessages = [...criticHeld, ...textTurn(answered, text.call.tool, ran)]
          }
          criticSteps += 1
        }
        if (streamed === null) {
          // agent#10: the declared cases the critic's own calls left uncompared, run once each by the
          // harness through the same dispatch, gate and recorder; no model request.
          toolsClosed = false
          await compareTheRest(recorder, proposal.document, criticOffered, (name, args) => {
            harnessCalls += 1
            return runRuntime(name, args, 'harness')
          })
        }
        const ended = slot.turnEnded(criticText !== '')
        if (ended !== null) await deliver(ended)
      } catch (cause) {
        // A person's Stop ends the run here as anywhere; the turn's wall clock and everything else
        // leave the finished proposal standing.
        if (gate.signal.aborted || (isCancelled(cause) && !overTime)) throw cause
        unfinished = cause
      } finally {
        recording = null
        phase = loopPhase
        context.endCriticPhase()
      }
      unfinished ??= streamed
      if (unfinished === null) {
        critique = { ...recorder.critique(criticText), figures: figures() }
        await deliver(critiqueEvent(critique))
      } else {
        note = criticNote(overTime ? turnTimeFailure() : unfinished, 'model', figures())
      }
    }

    await deliver({ type: 'proposal', document: proposal.document, unknowns: proposal.unknowns, ...critiqueOnProposal(critique), ...(note === null ? {} : { note }) })
  }

  /** What the turn's wall clock passing is, as a failure the catalogue can say. */
  const turnTimeFailure = (): AIFailureError =>
    authoredFailure('timeout', `The turn reached its wall-clock limit of ${limitText(context.policy.turnMilliseconds)}.`, 'model')

  /** The status and the endpoint's own sentence out of a refusal, for the thinking slot's classifier, or nothing. */
  const refusalOf = (cause: unknown): { status: number; message: string } | null => {
    const error = unwrapped(cause) as { statusCode?: unknown; responseBody?: unknown }
    if (typeof error?.statusCode !== 'number') return null
    return { status: error.statusCode, message: endpointSentence(error.responseBody) }
  }

  /** The refusal a served schema earned, where the endpoint takes a schema subset and names a keyword this run sent. */
  const schemaRefusal = (cause: unknown): Error | null => {
    if (capabilities.toolSchemas !== 'gemini-subset') return null
    const refusal = refusalOf(cause)
    if (refusal === null) return null
    const keyword = refusedSchemaKeyword(refusal.status, refusal.message, schemasShown(capabilities.toolSchemas, session.tools))
    return keyword === '' ? null : authoredFailure('invalid-request', refusedSchemaSentence(keyword), 'model')
  }

  /** The session, with the one retry a tier refusal earns, only before anything was delivered. */
  const drive = async (): Promise<void> => {
    turnTimer = setTimeout(() => {
      overTime = true
      turnClock.abort()
    }, context.policy.turnMilliseconds)
    // LangChain's tracer is the one way out beside the relay; an environment that turns it on refuses the run.
    const tracing = tracingAsked()
    if (tracing !== undefined) throw tracingRefusal(tracing)
    if (collision !== undefined) throw authoredFailure('configuration', `host tool ${collision.name} is named like a tool the runtime serves; nothing was run`)
    for (const notice of narrowingEvents(id, session.model.family, [...session.tools, ...session.hostTools])) await channel.push(notice)
    const opened = slot.opening()
    if (opened !== null) await channel.push(opened)
    for (let attempt = 1; ; attempt += 1) {
      const produced = { count: 0 }
      try {
        await runOnce(produced)
        return
      } catch (thrown) {
        const cause = unwrapped(thrown)
        if (awaitingInput) return
        if (overTime && !gate.signal.aborted) {
          context.execution.state.reason = `${turnTimeFailure().message} Completed work is kept. Continuing takes a grant of more requests, so a run already at the ${MAX_REQUEST_ALLOWANCE}-request limit cannot be continued; start a new response instead.`
          throw new ExecutionPaused()
        }
        const refusal = refusalOf(cause)
        if (refusal === null || produced.count > 0 || attempt >= 3) throw schemaRefusal(cause) ?? cause
        const verdict = slot.refused(refusal.status, refusal.message)
        if (verdict.kind === 'other') throw schemaRefusal(cause) ?? cause
        if (verdict.kind === 'degrade' && verdict.event !== null) await channel.push(verdict.event)
      }
    }
  }

  const finish = async (): Promise<void> => {
    clearTimeout(turnTimer)
    await channel.push({ type: 'end' })
    channel.close()
  }

  const open = (): AsyncGenerator<AssistantEvent> => {
    const loop = drive().catch(async (thrown: unknown) => {
      const cause = unwrapped(thrown)
      if (isCancelled(cause) || gate.signal.aborted) return
      if (cause instanceof ExecutionPaused) {
        await channel.push({ type: 'budget_paused', checkpoint: context.execution.snapshot('paused') })
        return
      }
      await channel.push(failureEvent(cause, 'model'))
    })
    void loop.then(finish, finish)
    return channel.drain()
  }

  return eventIterator({ gate, open })
}
