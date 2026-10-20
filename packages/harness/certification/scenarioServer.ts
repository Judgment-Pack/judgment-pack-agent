/**
 * The certified scenarios (agent#13) as the shared server serves them (`scripts/scripted-server.mjs`,
 * a session created with `scenario`), for an adapter in another language: the scenario's scripted
 * endpoint on the OpenAI-compatible wire and its runtime, built from the same scenario files and the
 * same answer functions `scenarios.test.ts` drives the TypeScript adapters with. Nothing here decides
 * a step the TypeScript scenarios do not decide the same way.
 *
 * The host's own half of a scenario (its host tools, the chat context, the correction loop's host
 * checks, the delegation coordinator) is the adapter's runner's, as it is in `scenarios.test.ts`.
 */
import scenario from './scenario.json'
import chatHostTools from './scenarios/chat-host-tools.json'
import correction from './scenarios/correction-loop.json'
import delegation from './scenarios/delegation.json'
import memory from './scenarios/memory.json'
import multiTurn from './scenarios/multi-turn.json'
import planMode from './scenarios/plan-mode.json'
import toolSources from './scenarios/tool-sources.json'
import { RECORDED_TOOLS, recordedRuntime, type RecordedRuntime, type ServerObservation } from './scriptedServer'
import { REFUTATION_MARKER } from '../refutation'
import { TEST_CASES_TOOL, runtimeAnswer, type EndpointRequest } from '../testing/scripted'
import { answerFrom, answerWith, resultsIn, rolesAnswer, type AnswerAs, type Known, type Roles, type Turn } from '../testing/scenarioDriver'

/** The scenarios a session can play. */
export const SCENARIOS = ['chat-host-tools', 'delegation', 'memory', 'plan-mode', 'correction-loop', 'multi-turn', 'tool-sources'] as const
export type ScenarioName = (typeof SCENARIOS)[number]

const DRAFTS = scenario.documents as unknown as Record<string, unknown>

/** A turn of the correction loop's author script as the words the model sends: its sentence, then the proposal in a fenced block. */
export const authorText = (turn: { proposal: string; say: string }) =>
  `${turn.say}\n\n\`\`\`json\n${JSON.stringify({ proposal: { kind: 'create', document: DRAFTS[turn.proposal], unknowns: scenario.unknowns } })}\n\`\`\``

/** A turn of the correction loop's critic script: a recorded step of the bake-off's scenario, or a sentence. */
export function criticTurn(turn: { call?: { recorded: string }; say?: string }): Turn {
  const recorded = turn.call?.recorded
  if (recorded === undefined) return { say: turn.say! }
  const step = scenario.steps.find((one) => one.id === recorded)!
  return { call: { tool: step.tool!, arguments: step.arguments as Record<string, unknown> } }
}

/** The correction loop's endpoint: an author request is answered with the author's next draft, a critic request (the marker) with the critic's turn after the results it carries. */
export function correctionAnswer(answerAs: AnswerAs, beforeCritic?: { call: { recorded: string } }): (request: EndpointRequest) => Response {
  const critics = beforeCritic === undefined ? correction.critic : [beforeCritic, ...correction.critic]
  let authored = 0
  return (incoming) => {
    const text = JSON.stringify(incoming.body)
    if (text.includes(REFUTATION_MARKER)) return answerWith(criticTurn(critics[resultsIn(incoming)]!), answerAs, resultsIn(incoming))
    const turn = correction.author[authored]
    if (turn === undefined) throw new Error(`the correction loop has no author turn ${authored + 1}`)
    authored += 1
    return answerWith({ say: authorText(turn) }, answerAs, 0)
  }
}

/** A runtime serving the recorded five and the test tool, answering each call with `handle`, recording each call. */
function scenarioRuntime(handle: (call: { name: string; args: Record<string, unknown> }) => unknown): RecordedRuntime {
  const seen: ServerObservation[] = []
  const tools = [...RECORDED_TOOLS, TEST_CASES_TOOL]
  return {
    seen,
    answer(message) {
      const frame = message as { id?: string | number; method?: string; params?: { name?: string; arguments?: Record<string, unknown> } }
      if (frame.id === undefined || frame.method === undefined) return null
      const reply = (result: unknown) => ({ jsonrpc: '2.0', id: frame.id, result }) as never
      if (frame.method === 'initialize') return reply({ protocolVersion: '2025-06-18', capabilities: { tools: {} }, serverInfo: { name: 'scenario-runtime', version: '0' } })
      if (frame.method === 'tools/list') return reply({ tools })
      if (frame.method !== 'tools/call') return { jsonrpc: '2.0', id: frame.id, error: { code: -32601, message: `the scenario runtime does not serve ${frame.method}` } } as never
      const name = String(frame.params?.name ?? '')
      const args = frame.params?.arguments ?? {}
      seen.push({ name, args, refusal: tools.some((tool) => tool.name === name) ? '' : `${name} is not served` })
      return reply(handle({ name, args }))
    }
  }
}

/** One scenario session: the endpoint's answer for each request, the requests as the endpoint read them, and the runtime. */
export interface ScenarioSession {
  answer(request: EndpointRequest): Response
  requests: EndpointRequest[]
  runtime: RecordedRuntime
  /** Moves a scenario of several responses (delegation) or turns (multi-turn) to its next one. */
  advance(): void
}

/**
 * A session of one scenario. `beforeCritic`: the correction loop with one recorded step put first in
 * the critic's script (the critic's boundary check, step T7).
 */
export function scenarioSession(name: ScenarioName, answerAs: AnswerAs, options: { beforeCritic?: string } = {}): ScenarioSession {
  const requests: EndpointRequest[] = []
  let position = 0
  let answer: (request: EndpointRequest) => Response
  let runtime: RecordedRuntime
  const ok = () => runtimeAnswer({ status: 'ok' })
  switch (name) {
    case 'chat-host-tools':
      answer = answerFrom(chatHostTools.turns as unknown as Turn[], answerAs)
      runtime = scenarioRuntime(ok)
      break
    case 'memory':
      answer = answerFrom(memory.turns as unknown as Turn[], answerAs)
      runtime = scenarioRuntime((call) => runtimeAnswer((memory.runtimeAnswers as Record<string, unknown>)[call.name] ?? {}))
      break
    case 'plan-mode':
      answer = answerFrom(planMode.turns as unknown as Turn[], answerAs)
      runtime = scenarioRuntime(ok)
      break
    case 'multi-turn':
      answer = (request) => answerFrom(multiTurn.turns[Math.min(position, multiTurn.turns.length - 1)]!.model as unknown as Turn[], answerAs)(request)
      runtime = scenarioRuntime((call) => runtimeAnswer(call.name === 'get_schema' ? multiTurn.sources.schema : { status: 'ok' }))
      break
    case 'delegation': {
      const known: Known = { references: [], tasks: {} }
      answer = (request) => rolesAnswer({ parent: delegation.responses[position]!.parent, children: delegation.children } as unknown as Roles, answerAs, known)(request)
      runtime = scenarioRuntime(ok)
      break
    }
    case 'tool-sources':
      // ADR-0006: the first run's turns, then, once advanced (the person approved the new list), the second's.
      answer = (request) => answerFrom((position === 0 ? toolSources.turns : toolSources.afterApproval) as unknown as Turn[], answerAs)(request)
      runtime = scenarioRuntime(ok)
      break
    case 'correction-loop':
      answer = correctionAnswer(answerAs, options.beforeCritic === undefined ? undefined : { call: { recorded: options.beforeCritic } })
      runtime = recordedRuntime()
      break
    default:
      throw new Error(`no scenario ${String(name)}`)
  }
  return {
    requests,
    runtime,
    advance: () => {
      position += 1
    },
    answer(request) {
      requests.push(request)
      return answer(request)
    }
  }
}
