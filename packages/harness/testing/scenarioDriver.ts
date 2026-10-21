/**
 * What the certified scenarios (`certification/scenarios/*.json`, agent#13) are driven with: a
 * scripted model's turns, read from a scenario's own file, put on each adapter's wire. Tests only,
 * like the rest of `testing/`.
 *
 * A scenario lists its turns by hand: a call (a tool and its arguments) or a statement. The
 * endpoint answers a request with the turn after the tool results the request already carries, as
 * the recorded scenario's scripted model does, so a loop that drops or repeats a result is answered
 * differently. The agent's run makes the same calls in the same order, at the engine's own seam
 * (`AgentRun`), by the name the offer gave each tool where it was offered and by its own name where
 * it was not. A delegation scenario lists the parent's turns and each child's, told by its brief.
 */
import { vi } from 'vitest'
import { PROPOSE_PACK_SCHEMA, PROPOSE_PACK_TOOL } from '../engines/contract'
import { ASSISTANT_TOOLS } from '../host'
import { bindModelCall, runAssistantSession } from '../session'
import { normalize } from '../thinking'
import { vercel } from '../engines/vercel'
import { codex } from '../engines/codex'
import { RECORDED_TOOLS } from '../certification/scriptedServer'
import {
  TEST_CASES_TOOL,
  gatedRuntime,
  runtimeAnswer,
  scriptedEndpoint,
  sseTurn,
  streamed,
  whole,
  wholeTurn,
  type EndpointRequest,
  type RuntimeCall,
  type RuntimeControl
} from './scripted'
import type { AgentRun } from '../agent'
import type { AgentAssistantSession, AssistantEvent, AssistantSession, Engine, HostTool } from '../engine'

/** One turn of a scripted model: one tool call, several in one answer (ADR-0008's second act), or the words that end the run. */
export type Turn =
  | { call: { tool: string; arguments: Record<string, unknown> }; usage?: Usage }
  | { calls: { tool: string; arguments: Record<string, unknown> }[]; usage?: Usage }
  | { say: string; report?: unknown; usage?: Usage }

/** What a provider says one answer cost, as it reports it. */
export interface Usage {
  input: number
  output: number
}

/** A host tool as a scenario lists it: its name, its effect and what it answers. */
export interface ScenarioHostTool {
  name: string
  effect?: 'read' | 'proposal' | 'workflow' | 'memory' | 'plan'
  delegation?: 'read' | 'research'
  /** How the chat's context may keep and reuse a result of this tool. */
  contextPolicy?: { immutable?: boolean; reference?: boolean; runtime?: boolean }
  description: string
  /** What a call answers, as text; absent, an empty answer. */
  result?: string
}

/** The host tools a scenario lists, each counting its own executions. */
export function hostToolsOf(listed: readonly ScenarioHostTool[], executed: Record<string, number>, received: { tool: string; arguments: Record<string, unknown> }[] = []): HostTool[] {
  return listed.map((tool) => ({
    name: tool.name,
    ...(tool.effect === undefined ? {} : { effect: tool.effect }),
    ...(tool.delegation === undefined ? {} : { delegation: tool.delegation }),
    ...(tool.contextPolicy === undefined ? {} : { contextPolicy: tool.contextPolicy }),
    description: tool.description,
    inputSchema:
      tool.name === PROPOSE_PACK_TOOL ? PROPOSE_PACK_SCHEMA : { type: 'object', properties: {}, additionalProperties: true },
    execute: async (args) => {
      executed[tool.name] = (executed[tool.name] ?? 0) + 1
      received.push({ tool: tool.name, arguments: args })
      return { content: [{ type: 'text', text: tool.result ?? '' }] }
    }
  }))
}

/** The tool results a request already carries, on the OpenAI-compatible wire. */
export const resultsIn = (request: EndpointRequest): number =>
  (request.body.messages ?? []).filter((message) => message.role === 'tool').length

/** How the scripted endpoint sends a turn: as an event stream, or as one whole object. */
export type AnswerAs = 'stream' | 'whole'

/** What a turn says, as text: the words, and the report block a child ends its answer with. */
export const textOf = (turn: { say: string; report?: unknown }): string =>
  turn.report === undefined ? turn.say : `${turn.say}\n\n\`\`\`json\n${JSON.stringify({ report: turn.report })}\n\`\`\``

/** One turn as the endpoint's answer on the OpenAI-compatible wire, its usage on the last frame where the turn gives it. */
export function answerWith(turn: Turn, answerAs: AnswerAs, at: number): Response {
  const usage = turn.usage === undefined ? undefined : { prompt_tokens: turn.usage.input, completion_tokens: turn.usage.output }
  const step = 'call' in turn ? { tool: { name: turn.call.tool, args: turn.call.arguments, id: `call_${at + 1}` } }
    : 'calls' in turn ? { tools: turn.calls.map((call, index) => ({ name: call.tool, args: call.arguments, id: `call_${at + 1}_${index + 1}` })) }
      : { text: textOf(turn) }
  if (answerAs === 'whole') {
    const body = JSON.parse(wholeTurn(step)) as Record<string, unknown>
    if (usage !== undefined) body.usage = { ...usage, total_tokens: usage.prompt_tokens + usage.completion_tokens }
    return whole(JSON.stringify(body))
  }
  if (usage === undefined) return streamed(sseTurn(step))
  // The same turn, with the usage figures on a frame of their own after the last one, as a provider that reports them sends it.
  const frames = sseTurn(step).replace('data: [DONE]\n\n', `data: ${JSON.stringify({ id: 'c', object: 'chat.completion.chunk', model: 'm', choices: [], usage })}\n\ndata: [DONE]\n\n`)
  return streamed(frames)
}

/** The endpoint's answer for `turns`: the turn after the results the request already carries. */
export function answerFrom(turns: readonly Turn[], answerAs: AnswerAs): (request: EndpointRequest) => Response {
  return (request) => {
    const at = resultsIn(request)
    const turn = turns[at]
    if (turn === undefined) throw new Error(`the scenario has no turn ${at + 1}: a request carried ${at} results`)
    return answerWith(turn, answerAs, at)
  }
}

/** A delegation scenario's script: the parent's turns, and each child's by its objective. */
export interface Roles {
  parent: Turn[]
  children: { objective: string; turns: Turn[] }[]
}

/** The words a child's brief starts with (delegation.ts), by which a child is told from its parent. */
export const CHILD_BRIEF = 'Complete this read-only task'

/** What a scenario's placeholders stand for, learned as the run goes. */
export interface Known {
  /** The ledger ids of the references a child's brief named, in order. */
  references: string[]
  /** The ids of the tasks a delegate call answered, by title. */
  tasks: Record<string, string>
}

/** `$reference:N` and `$task:<title>` replaced by what the run has learned, wherever a string holds one whole. */
export function resolve<T>(value: T, known: Known): T {
  if (typeof value === 'string') {
    const reference = /^\$reference:(\d+)$/.exec(value)
    if (reference !== null) return (known.references[Number(reference[1])] ?? value) as T
    const task = /^\$task:(.+)$/.exec(value)
    if (task !== null) return (known.tasks[task[1]!] ?? value) as T
    return value
  }
  if (Array.isArray(value)) return value.map((item) => resolve(item, known)) as T
  if (value !== null && typeof value === 'object') {
    return Object.fromEntries(Object.entries(value).map(([key, inner]) => [key, resolve(inner, known)])) as T
  }
  return value
}

/** A turn with its placeholders resolved. */
export const resolved = (turn: Turn, known: Known): Turn => resolve(turn, known)

/** The brief a child was sent: the JSON line its prompt ends with. */
export function briefIn(prompt: string): { objective: string; references?: { id: string; label: string }[] } {
  const at = prompt.lastIndexOf('\n{"objective"')
  return JSON.parse(prompt.slice(at + 1).split('\n')[0]!) as { objective: string; references?: { id: string; label: string }[] }
}

/** The tasks a delegate call answered, read out of the framed text that carries them (a tool message or a continuation's block). */
export function tasksIn(text: string): { id: string; title: string }[] {
  const found: { id: string; title: string }[] = []
  for (const block of text.split('BEGIN RETRIEVED DATA ').slice(1)) {
    const open = block.indexOf('\n')
    const close = block.lastIndexOf('\nEND RETRIEVED DATA ')
    if (!block.slice(0, open).includes('"delegate_tasks"')) continue
    try {
      const inner = JSON.parse(block.slice(open + 1, close < 0 ? undefined : close)) as { structuredContent?: { tasks?: { id: string; title: string }[] }; tasks?: { id: string; title: string }[] }
      found.push(...(inner.structuredContent?.tasks ?? inner.tasks ?? []))
    } catch {
      // not a delegate answer
    }
  }
  return found
}

/** The first user message's text parts of a request on the OpenAI-compatible wire. */
export function openingTexts(request: EndpointRequest): string[] {
  const message = (request.body.messages ?? []).find((turn) => turn.role === 'user')
  if (message === undefined) return []
  return typeof message.content === 'string' ? [message.content] : ((message.content as { text?: string }[] | undefined) ?? []).map((part) => part.text ?? '')
}

/** A request a delegated child sent, told by its brief. */
export const isChildRequest = (request: EndpointRequest): boolean => openingTexts(request).some((text) => text.includes(CHILD_BRIEF))

/**
 * The endpoint's answer for a delegation scenario: a child's turn by its objective and the results
 * its own request carries, the parent's by the results its request carries. The ids a delegate call
 * answered are learned from the parent's tool messages as they arrive.
 */
export function rolesAnswer(roles: Roles, answerAs: AnswerAs, known: Known): (request: EndpointRequest) => Response {
  return (request) => {
    const at = resultsIn(request)
    if (isChildRequest(request)) {
      const brief = briefIn(openingTexts(request).find((text) => text.includes(CHILD_BRIEF))!)
      known.references = (brief.references ?? []).map((reference) => reference.id)
      const turns = roles.children.find((child) => child.objective === brief.objective)?.turns
      if (turns === undefined) throw new Error(`the scenario has no child with the objective ${JSON.stringify(brief.objective)}`)
      const turn = turns[at]
      if (turn === undefined) throw new Error(`the child ${JSON.stringify(brief.objective)} has no turn ${at + 1}`)
      return answerWith(resolved(turn, known), answerAs, at)
    }
    for (const message of request.body.messages ?? []) {
      if (message.role === 'tool') for (const task of tasksIn(String(message.content))) known.tasks[task.title] = task.id
    }
    const turn = roles.parent[at]
    if (turn === undefined) throw new Error(`the parent has no turn ${at + 1}`)
    return answerWith(resolved(turn, known), answerAs, at)
  }
}

/**
 * The agent's runs for a delegation scenario. A child run is told by its brief and gives its turns.
 * A parent run goes on from where the last one stopped and ends at its delegate call, as the engine
 * ends a run there; the next run begins with what the call answered.
 */
export function rolesRun(roles: Roles, known: Known, answers: { who: string; tool: string; text: string; isError: boolean }[] = []): AgentRun {
  let cursor = 0
  return async (request, callbacks) => {
    const call = async (turn: Extract<Turn, { call: unknown }>, at: number, who: string) => {
      const offered = request.tools.find((tool) => tool.description.startsWith(`${turn.call.tool}:`))
      const answer = await callbacks.tool({ id: `call-${at}`, name: offered?.name ?? turn.call.tool, arguments: turn.call.arguments }, callbacks.signal)
      answers.push({ who, tool: turn.call.tool, ...answer })
      return answer
    }
    if (request.prompt.includes(CHILD_BRIEF)) {
      const brief = briefIn(request.prompt)
      known.references = (brief.references ?? []).map((reference) => reference.id)
      const turns = roles.children.find((child) => child.objective === brief.objective)?.turns
      if (turns === undefined) throw new Error(`the scenario has no child with the objective ${JSON.stringify(brief.objective)}`)
      for (const [at, listed] of turns.entries()) {
        const turn = resolved(listed, known)
        if ('call' in turn) await call(turn, at, brief.objective)
        else if ('say' in turn) await callbacks.event({ type: 'message', id: `say-${at}`, text: textOf(turn), phase: 'final' })
      }
      return
    }
    for (const task of tasksIn(request.prompt)) known.tasks[task.title] = task.id
    while (cursor < roles.parent.length) {
      const turn = resolved(roles.parent[cursor]!, known)
      const at = cursor++
      if ('call' in turn) {
        await call(turn, at, 'parent')
        if (turn.call.tool === 'delegate_tasks') return
      } else if ('say' in turn) {
        await callbacks.event({ type: 'message', id: `say-${at}`, text: textOf(turn), phase: 'final' })
      }
    }
  }
}

/**
 * The agent's run for `turns`, at the engine's own seam (`AgentRun`): each call by the name the
 * offer gave it where the agent was offered the tool, and by its own name where it was not, which
 * is what the gate then refuses (agent#25). Each statement is the final message. The answers the
 * agent was given are kept, in order.
 */
export function agentRunFrom(turns: readonly Turn[], answers: { text: string; isError: boolean }[] = []): AgentRun {
  return async (request, callbacks) => {
    let at = 0
    for (const turn of turns) {
      if ('call' in turn) {
        const offered = request.tools.find((tool) => tool.description.startsWith(`${turn.call.tool}:`))
        answers.push(await callbacks.tool({ id: `call-${++at}`, name: offered?.name ?? turn.call.tool, arguments: turn.call.arguments }, callbacks.signal))
      } else if ('calls' in turn) {
        // Several calls at once, as an agent that calls tools in parallel makes them; a call the run
        // stopped at answers nothing, and the run goes no further once it is stopped.
        const made = await Promise.allSettled(turn.calls.map((call) => {
          const offered = request.tools.find((tool) => tool.description.startsWith(`${call.tool}:`))
          return callbacks.tool({ id: `call-${++at}`, name: offered?.name ?? call.tool, arguments: call.arguments }, callbacks.signal)
        }))
        for (const settled of made) if (settled.status === 'fulfilled') answers.push(settled.value)
        if (callbacks.signal.aborted) return
      } else {
        await callbacks.event({ type: 'message', id: `say-${++at}`, text: textOf(turn), phase: 'final' })
      }
    }
  }
}

/** What one scenario run left: the stream, what reached the runtime and the requests or runs. */
export interface Ran {
  events: AssistantEvent[]
  /** What reached the runtime, after the gate. */
  seen: RuntimeCall[]
  /** The model requests of the run, on the model engine. */
  requests: EndpointRequest[]
  /** The agent runs of the run, on the agent engine. */
  runs: Parameters<AgentRun>[0][]
}

/** Legacy scenarios retain their handwritten pre-ADR-0020 streams; scenario 20 owns usage checks. */
const legacyEvents = (events: AssistantEvent[]) => events.filter((event) => event.type !== 'usage')

interface Options {
  engine?: Engine
  hostTools?: HostTool[]
  handle?: (call: RuntimeCall, control: RuntimeControl) => unknown
  /** The runtime tools the run is offered; the recorded five and the test tool where absent. */
  tools?: typeof RECORDED_TOOLS
  session?: Partial<AssistantSession> & Partial<AgentAssistantSession>
  /** Break the run off after the event this answers true for, as a host that stops there does (ADR-0008's restart). */
  stopAt?: (event: AssistantEvent) => boolean
}

/** Why a run was broken off: not a failure of the run. */
class BrokenOff extends Error {}

/** The event sink of a run that `stopAt` may break off. */
function sinkOf(events: AssistantEvent[], stopAt?: (event: AssistantEvent) => boolean) {
  return (event: AssistantEvent) => {
    events.push(event)
    if (stopAt?.(event)) throw new BrokenOff()
  }
}

/** One run on the model engine through a scripted endpoint, the runtime behind the real gate. */
export async function runOnModelEngine(answer: (request: EndpointRequest) => Response | Promise<Response>, options: Options = {}): Promise<Ran> {
  const events: AssistantEvent[] = []
  const endpoint = scriptedEndpoint(answer)
  vi.stubGlobal('fetch', endpoint.fetch)
  const runtime = await gatedRuntime({
    tools: [...(options.tools ?? RECORDED_TOOLS), TEST_CASES_TOOL],
    allowed: [...ASSISTANT_TOOLS],
    handle: options.handle ?? (() => runtimeAnswer({ status: 'ok' })),
    onEvent: (event) => events.push(event)
  })
  try {
    await runAssistantSession(
      options.engine ?? vercel,
      {
        prompt: 'Check the expense pack.',
        testPrompt: '',
        tools: runtime.tools,
        callTool: runtime.callTool,
        hostTools: options.hostTools ?? [],
        allowConversation: true,
        model: { family: 'openai-compatible', model: 'scripted-model', call: bindModelCall('openai-compatible') },
        thinking: normalize('off', 'openai-compatible'),
        signal: new AbortController().signal,
        ...options.session
      } as AssistantSession,
      sinkOf(events, options.stopAt)
    ).catch((cause: unknown) => { if (!(cause instanceof BrokenOff)) throw cause })
  } finally {
    await runtime.close()
  }
  return { events: legacyEvents(events), seen: runtime.seen, requests: endpoint.requests, runs: [] }
}

/** One run on the agent engine, the agent's runs scripted at the engine's own seam, the runtime behind the real gate. */
export async function runOnAgentEngine(run: AgentRun, options: Options = {}): Promise<Ran> {
  const events: AssistantEvent[] = []
  const runs: Ran['runs'] = []
  const runtime = await gatedRuntime({
    tools: [...(options.tools ?? RECORDED_TOOLS), TEST_CASES_TOOL],
    allowed: [...ASSISTANT_TOOLS],
    handle: options.handle ?? (() => runtimeAnswer({ status: 'ok' })),
    onEvent: (event) => events.push(event)
  })
  try {
    await runAssistantSession(
      options.engine ?? codex,
      {
        prompt: 'Check the expense pack.',
        testPrompt: '',
        tools: runtime.tools,
        callTool: runtime.callTool,
        hostTools: options.hostTools ?? [],
        allowConversation: true,
        agent: {
          model: 'scripted-agent',
          run: async (request, callbacks) => {
            runs.push(request)
            return run(request, callbacks)
          }
        },
        signal: new AbortController().signal,
        ...options.session
      } as AgentAssistantSession,
      sinkOf(events, options.stopAt)
    ).catch((cause: unknown) => { if (!(cause instanceof BrokenOff)) throw cause })
  } finally {
    await runtime.close()
  }
  return { events: legacyEvents(events), seen: runtime.seen, requests: [], runs }
}

/** The real names of the tools a model request or an agent run was offered, in the order given. */
export function offeredNames(ran: Ran, at = 0): string[] {
  const request = ran.requests[at]
  if (request !== undefined) return ((request.body as { tools?: { function: { name: string } }[] }).tools ?? []).map((tool) => tool.function.name)
  return (ran.runs[at]?.tools ?? []).map((tool) => tool.description.slice(0, tool.description.indexOf(':')))
}
