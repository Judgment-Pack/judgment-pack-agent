/**
 * The certified scenarios (agent#13): six scripted scenarios beside the bake-off's, each written by
 * hand as `scenario.json` is (`certification/scenarios/*.json`) and run on every adapter of its kind
 * by `npm run certify -- --adapter <id>`:
 *
 * 1. chat mode with host tools: a read, a proposal that stops at the review card, a refused name;
 * 2. delegation: a batch of two children, their findings, and a resumed child;
 * 3. memory: the request text on every request of a run over three tool steps, and the reserve;
 * 4. plan mode: the offer, the refused actions, and the plan;
 * 5. the correction loop: a refused draft, one correction request, the critic's comparison;
 * 6. a multi-turn transcript: the chat's index and ledger across three runs.
 *
 * The model engine runs each on the OpenAI-compatible wire, once with each answer shape (a stream,
 * a whole object), through a scripted endpoint. The agent engine runs each at its own seam
 * (`AgentRun`), as `engines/codex/codex.test.ts` does, with the scripted agent calling each tool by
 * the name its offer gave it. Where an adapter cannot run a check as the other does, the check says
 * how it differs, in its own name: the disagreement is recorded, not skipped.
 *
 * The model's turns are the scenarios' own words; nothing here was recorded from a model.
 */
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import { AGENT_ENGINES, MODEL_ENGINES, loadEngine } from '../engines'
import { PROPOSE_PACK_TOOL } from '../engines/contract'
import { CONTINUATION, withDelegation } from '../delegation'
import { DELEGATE_TOOL, readReport, validDelegatedTask, type DelegatedTask, type ReportKind } from '../delegationContract'
import { validDelegationFigures, type ExecutionCheckpoint } from '../execution/state'
import { ChatContext, INDEX_NOTICE, INDEX_SOURCE } from '../context/chat'
import { OPENING, unframeData } from '../framing'
import { correctArtifact } from '../execution/correctArtifact'
import { REFUTATION_MARKER, type CriticCase } from '../refutation'
import { bindModelCall, openAssistantConnection, runAssistantSession } from '../session'
import { scriptedRuntime } from './scriptedServer'
import scenario from './scenario.json'
import correction from './scenarios/correction-loop.json'
import multiTurn from './scenarios/multi-turn.json'
import { CONTEXT_DEFAULTS, inputBytes, utf8Bytes } from '../context/policy'
import { SessionContext } from '../context/session'
import { PLAN_RUNTIME_READS } from '../planning/policy'
import { ASSISTANT_TOOLS, type EndpointKind } from '../host'
import { REHEARSAL_TOOL, TEST_TOOL } from '../toolGate'
import { normalize } from '../thinking'
import type { AgentRun } from '../agent'
import { recallQuery } from './recall'
import {
  CHILD_BRIEF,
  agentRunFrom,
  answerFrom,
  briefIn,
  hostToolsOf,
  isChildRequest,
  offeredNames,
  answerWith,
  openingTexts,
  resultsIn,
  rolesAnswer,
  rolesRun,
  runOnAgentEngine,
  runOnModelEngine,
  tasksIn,
  type AnswerAs,
  type Known,
  type Ran,
  type Roles,
  type Turn
} from '../testing/scenarioDriver'
import { ofType, runtimeAnswer, scriptedEndpoint, type RuntimeCall } from '../testing/scripted'
import type { AssistantEvent, Engine, EngineSession } from '../engine'
import taskSchema from '../../../contract/schemas/delegated-task.json'
import chatHostTools from './scenarios/chat-host-tools.json'
import delegation from './scenarios/delegation.json'
import memory from './scenarios/memory.json'
import planMode from './scenarios/plan-mode.json'

afterEach(() => vi.unstubAllGlobals())

/** The legs of a model engine: each answer shape the scripted endpoint sends. */
const LEGS: AnswerAs[] = ['stream', 'whole']

/** The Runtime's six tools the gate serves, and the six the harness adds to every conversation. */
const RUNTIME_SIX = ['get_schema', 'list_examples', 'get_example', 'validate', 'experimental_evaluate', 'experimental_test_cases']
const CONTEXT_SIX = ['list_context', 'read_context', 'read_window', 'search', 'update_plan', 'request_budget_increase']
const sorted = (names: string[]) => [...names].sort()

const request = (message: string) => ({ currentRequest: { message, input: message } })

/** The tasks a finished run published, each as its last state. */
function tasksOf(events: AssistantEvent[]): DelegatedTask[] {
  const last = new Map<string, DelegatedTask>()
  for (const event of events) if (event.type === 'delegated_task') last.set(event.task.id, structuredClone(event.task))
  return [...last.values()]
}

/** The execution checkpoint a run ended on. */
const lastCheckpoint = (events: AssistantEvent[]): ExecutionCheckpoint =>
  (events.filter((event) => event.type === 'execution_checkpoint').at(-1) as { checkpoint: ExecutionCheckpoint }).checkpoint

// ---------------------------------------------------------------------------------------------
// 1. Chat mode with host tools
// ---------------------------------------------------------------------------------------------

/** The checks every adapter makes of scenario 1, over one run of it. */
function chatHostToolsChecks(run: () => Ran, executed: () => Record<string, number>, answers: () => { text: string; isError: boolean }[] | null) {
  it('1 · the offer is the two host tools beside the Runtime’s six and the harness’s six, and no third name', () => {
    const offered = offeredNames(run())
    expect(sorted(offered)).toEqual(sorted([...RUNTIME_SIX, ...CONTEXT_SIX, 'read_policy', PROPOSE_PACK_TOOL]))
    expect(offered).not.toContain('save_pack')
  })

  it('1 · the gate refuses a third name between its call and its result, and it never reaches the runtime', () => {
    const { events, seen } = run()
    const refused = events.filter((event): event is Extract<AssistantEvent, { type: 'guardrail' }> => event.type === 'guardrail' && event.action === 'refused')
    expect(refused.map((event) => event.tool)).toEqual(['save_pack'])
    const flow = events.filter((event) => event.type !== 'execution_checkpoint')
    const at = flow.indexOf(refused[0]!)
    expect(flow[at - 1]).toMatchObject({ type: 'tool_call', name: 'save_pack' })
    expect(flow[at + 1]).toMatchObject({ type: 'tool_result', name: 'save_pack', isError: true })
    expect(seen).toEqual([])
    // The model is told. On the agent engine its answer is the gate's refusal. Recorded disagreement: on the model engine the SDK
    // answers the call first, with its own no-such-tool error naming the name, and the model reads that.
    const told = answers()
    if (told === null) {
      const answered = (run().requests.at(-1)!.body.messages ?? []).find((message) => message.role === 'tool' && message.tool_call_id === 'call_2')
      expect(String(answered?.content)).toContain("unavailable tool 'save_pack'")
    } else {
      expect(told[1]).toMatchObject({ isError: true, text: expect.stringContaining('never calls a tool it was not offered') })
    }
  })

  it('1 · the proposal reaches the review card, and nothing is acted on', () => {
    const { events, seen } = run()
    expect(ofType(events, 'tool_call').map((event) => event.name)).toEqual(['read_policy', 'save_pack', PROPOSE_PACK_TOOL])
    expect(ofType(events, 'proposal')).toEqual([
      expect.objectContaining({ document: { id: 'expense-receipts', title: 'Expense receipts' }, unknowns: ['Whether a receipt must be itemized.'] })
    ])
    // The read ran once; the host's own propose_pack was never reached: the proposal is the card, not a write.
    expect(executed()).toEqual({ read_policy: 1 })
    expect(seen).toEqual([])
    expect(ofType(events, 'error')).toEqual([])
  })

  it('1 · the answer is delivered, once, and the run ends once', () => {
    const { events } = run()
    expect(ofType(events, 'message')).toEqual([{ type: 'message', text: (chatHostTools.turns.at(-1) as { say: string }).say }])
    expect(events.filter((event) => event.type === 'end')).toHaveLength(1)
  })
}

// ---------------------------------------------------------------------------------------------
// 2. Delegation
// ---------------------------------------------------------------------------------------------

/** What the delegation scenario left, response by response. */
interface Delegated {
  ran: Ran[]
  known: Known
  chat: ChatContext
  /** Runs of the parent's engine started with the delegation tool offered, and without it (a child's). */
  parents: number
  children: number
  answers: { who: string; tool: string; text: string; isError: boolean }[]
}

const KINDS: Record<string, ReportKind> = { review: 'review', research: 'research' }
const REVIEW_OFFER = ['read_policy', ...CONTEXT_SIX]
const RESEARCH_OFFER = ['read_policy', 'read_rates', ...CONTEXT_SIX]

/** The checks every adapter makes of scenario 2. */
function delegationChecks(get: () => Delegated, adapter: 'model' | 'agent') {
  const final = () => get().ran.flatMap((ran) => tasksOf(ran.events))
  const byTitle = (title: string) => final().find((task) => task.title === title)!

  it('2 · the parent runs once per response on the model engine; the agent engine starts its next run with the call’s result', () => {
    const { parents, children, ran } = get()
    if (adapter === 'model') {
      // Two responses, one parent run each; three children (the two of the batch, the resumed one).
      expect([parents, children]).toEqual([2, 3])
      for (const one of ran) expect(one.requests.filter((sent) => !isChildRequest(sent)).length).toBeGreaterThan(1)
    } else {
      // Recorded disagreement: the agent's run ends at the call. Response one: the call's run, two children, the next run; response two: the call's run, one child, the next run.
      expect(ran.map((one) => one.runs.length)).toEqual([4, 3])
      expect(ran[0]!.runs.filter((run) => run.prompt.includes(CONTINUATION))).toHaveLength(1)
      expect(ran[1]!.runs.filter((run) => run.prompt.includes(CONTINUATION))).toHaveLength(1)
    }
  })

  it('2 · the parent receives both children’s findings: the awaited call’s result on the model engine, the next run’s result and the chat’s index on the agent engine', () => {
    const [first] = get().ran
    const findings = ['The mileage section names no rate and no unit.', 'The 2026 rate is 0.58 per mile.']
    if (adapter === 'model') {
      const parent = first!.requests.filter((sent) => !isChildRequest(sent))
      // The same run's next request: the delegate call, then its result as a native tool message.
      const result = (parent.at(-1)!.body.messages ?? []).filter((message) => message.role === 'tool').map((message) => String(message.content)).find((text) => text.includes('from tool "delegate_tasks"'))!
      for (const text of findings) expect(result).toContain(text)
      expect(tasksIn(result).map((task) => task.title)).toEqual(['Review section 4', 'Research the rate'])
    } else {
      const next = first!.runs.find((run) => run.prompt.includes(CONTINUATION))!
      for (const text of findings) expect(next.prompt).toContain(text)
      // The run before it read read_policy; the next run lists that read in its index, and does not copy its text.
      expect(next.prompt).toContain(INDEX_NOTICE.slice(0, 60))
      expect(next.prompt).toMatch(/"tool":"read_policy","read":\{"id":"[^"]+:result-read_policy-/)
      expect(tasksIn(next.prompt).map((task) => task.title)).toEqual(['Review section 4', 'Research the rate'])
    }
  })

  it('2 · the children’s offers are closed: reads and the context tools only, no delegation, proposal, action or Runtime tool', () => {
    const { ran } = get()
    const childOffers = ran[0]!.requests.length > 0
      ? ran[0]!.requests.filter(isChildRequest).map((sent) => ({ objective: briefIn(openingTexts(sent).find((text) => text.includes(CHILD_BRIEF))!).objective, names: (sent.body as { tools?: { function: { name: string } }[] }).tools!.map((tool) => tool.function.name) }))
      : ran[0]!.runs.filter((run) => run.prompt.includes(CHILD_BRIEF)).map((run) => ({ objective: briefIn(run.prompt).objective, names: run.tools.map((tool) => tool.description.slice(0, tool.description.indexOf(':'))) }))
    const offers = new Map(childOffers.map((offer) => [offer.objective, sorted(offer.names)]))
    expect(offers.get('Review the mileage section of the policy for gaps.')).toEqual(sorted(REVIEW_OFFER))
    expect(offers.get('Find the published mileage rate for 2026.')).toEqual(sorted(RESEARCH_OFFER))
    for (const names of offers.values()) {
      for (const closed of [DELEGATE_TOOL, ...RUNTIME_SIX]) expect(names).not.toContain(closed)
    }
    // The parent itself was offered the delegation tool.
    expect(offeredNames(ran[0]!)).toContain(DELEGATE_TOOL)
  })

  it('2 · a reference resolves within the chat: the brief names the chat’s own entry, and the child reads it', () => {
    const { ran, chat, answers } = get()
    const review = byTitle('Review section 4')
    expect(review.references).toEqual([expect.stringMatching(new RegExp(`^${chat.scope}:policy:[0-9a-f]{16}$`))])
    expect(chat.entries.has(review.references![0]!)).toBe(true)
    const brief = adapter === 'model'
      ? briefIn(openingTexts(ran[0]!.requests.find((sent) => isChildRequest(sent) && JSON.stringify(sent.body.messages).includes('Review the mileage section'))!).find((text) => text.includes(CHILD_BRIEF))!)
      : briefIn(ran[0]!.runs.find((run) => run.prompt.includes('Review the mileage section'))!.prompt)
    expect(brief.references).toEqual([expect.objectContaining({ id: review.references![0], label: 'Expense policy' })])
    // The brief carries the id, not the text; the child's read_window answer carries the text.
    const read = adapter === 'model'
      ? ran[0]!.requests.flatMap((sent) => (sent.body.messages ?? []).filter((message) => message.role === 'tool').map((message) => String(message.content))).find((text) => text.includes('from tool "read_window"'))!
      : answers.find((answer) => answer.tool === 'read_window')!.text
    expect(read).toContain('Parking is paid up to 20 USD a day.')
    expect(JSON.stringify(brief)).not.toContain('Parking is paid')
    // The research child named no reference and was given none.
    expect(byTitle('Research the rate').references).toBeUndefined()
  })

  it('2 · each result is read in its profile’s shape, and validates against the contract', () => {
    for (const title of ['Review section 4', 'Research the rate', 'Rate unit']) {
      const task = byTitle(title)
      expect(task.state).toBe('complete')
      expect(validDelegatedTask(task)).toBe(true)
      expect(readReport(KINDS[task.profile]!, task.report)).toEqual({ report: task.report })
      for (const key of Object.keys(task)) expect(Object.keys(taskSchema.properties)).toContain(key)
    }
    expect(byTitle('Review section 4').report!.findings.map((finding) => (finding as { severity: string }).severity)).toEqual(['gap', 'gap'])
    expect(byTitle('Research the rate').report!.findings[0]!.citations).toEqual(['read_rates'])
  })

  it('2 · the figures are present: the children’s cost, the batch’s total and the parent’s own, and the answer’s figure', () => {
    const { ran } = get()
    for (const title of ['Review section 4', 'Research the rate', 'Rate unit']) {
      const task = byTitle(title)
      expect(task.model).toBe(adapter === 'model' ? 'scripted-model' : 'scripted-agent')
      expect(task.durationMs).toBeGreaterThanOrEqual(0)
      expect(task.firstByteMs).toBeGreaterThanOrEqual(0)
    }
    const figures = lastCheckpoint(ran[0]!.events).delegation!
    expect(validDelegationFigures(figures)).toBe(true)
    expect(figures.batches).toHaveLength(1)
    expect(figures.batches[0]!.children.map((child) => child.profile)).toEqual(['review', 'research'])
    expect(figures.batches[0]!.total.toolCalls).toBe(1)
    if (adapter === 'model') {
      // The usage the endpoint reported: review 300+380 in, 20+40 out; research 310+400 in, 22+36 out.
      expect(byTitle('Review section 4').tokens).toMatchObject({ inputTokens: 680, outputTokens: 60 })
      expect(byTitle('Research the rate').tokens).toMatchObject({ inputTokens: 710, outputTokens: 58 })
      expect(figures.batches[0]!.total).toMatchObject({ modelRequests: 4, tokens: { inputTokens: 1390, outputTokens: 118 } })
      expect(figures.parent).toMatchObject({ model: 'scripted-model', modelRequests: 3, tokens: { inputTokens: 2030, outputTokens: 158 } })
      expect(lastCheckpoint(ran[1]!.events).delegation!.parent).toMatchObject({ modelRequests: 2, tokens: { inputTokens: 1600, outputTokens: 80 } })
    } else {
      // Recorded disagreement: the agent wire reports no usage and no model request; its runs are counted instead.
      expect(byTitle('Review section 4').tokens).toBeUndefined()
      expect(figures.batches[0]!.total).toMatchObject({ modelRequests: 0, agentRuns: 2 })
      expect(figures.parent).toMatchObject({ model: 'scripted-agent', modelRequests: 0, agentRuns: 2 })
    }
    // The figure the children found reaches the person.
    expect(ofType(ran[0]!.events, 'message').at(-1)!.text).toContain('0.58 per mile')
  })

  it('2 · a follow-up resumes the research child with its earlier work, and the answer follows', () => {
    const { ran } = get()
    const research = byTitle('Research the rate')
    const resumed = byTitle('Rate unit')
    expect(resumed.resumes).toBe(research.id)
    const brief = adapter === 'model'
      ? briefIn(openingTexts(ran[1]!.requests.find((sent) => isChildRequest(sent))!).find((text) => text.includes(CHILD_BRIEF))!)
      : briefIn(ran[1]!.runs.find((run) => run.prompt.includes(CHILD_BRIEF))!.prompt)
    expect(brief).toMatchObject({ objective: 'Is the published rate per mile or per kilometre?', earlierWork: [{ objective: 'Find the published mileage rate for 2026.', result: 'The published 2026 rate is 0.58 per mile.' }] })
    expect(ofType(ran[1]!.events, 'message').at(-1)!.text).toContain('per mile, not per kilometre')
    expect(ran.flatMap((one) => ofType(one.events, 'error'))).toEqual([])
  })
}


// ---------------------------------------------------------------------------------------------
// 3. Memory
// ---------------------------------------------------------------------------------------------

type Json = Record<string, unknown>
const memoryAnswers = (call: { name: string }) => runtimeAnswer((memory.runtimeAnswers as Record<string, unknown>)[call.name] ?? {})

/** A session of either kind with no tools and no work, and the host's word on memory where given. */
function bareSession(kind: 'model' | 'agent', memoryFlag?: boolean): EngineSession {
  const base = { prompt: '', testPrompt: '', tools: [], callTool: async () => ({}), hostTools: [], signal: new AbortController().signal, ...(memoryFlag === undefined ? {} : { context: { memory: memoryFlag } }) }
  return kind === 'model'
    ? { ...base, model: { family: 'openai-compatible', model: 'scripted-model', call: async () => new Response() }, thinking: normalize('off', 'openai-compatible') }
    : { ...base, agent: { model: 'scripted-agent', run: async () => undefined } }
}

/** The checks both adapters make of the memory reserve: only the host's word that memory is off frees it. */
function reserveChecks(kind: 'model' | 'agent') {
  it('3 · memory on, and memory not said, keep the 4,000-byte reserve out of the input allowance', () => {
    // The allowance a run holds with no reserve: the input bytes less the instructions, the tools, the brief and the 1,536 bytes kept for the request's framing.
    const instructions = 'instructions'
    const full = inputBytes(CONTEXT_DEFAULTS) - utf8Bytes(instructions) - utf8Bytes('[]') - CONTEXT_DEFAULTS.briefBytes - 1536
    for (const said of [true, undefined]) {
      const on = new SessionContext(bareSession(kind, said))
      expect(() => on.check(instructions, 'p'.repeat(full - 4000), [])).not.toThrow()
      expect(() => on.check(instructions, 'p'.repeat(full - 4000 + 1), [])).toThrow()
      on.close()
    }
  })

  it('3 · memory off gives the run the full allowance', () => {
    const instructions = 'instructions'
    const full = inputBytes(CONTEXT_DEFAULTS) - utf8Bytes(instructions) - utf8Bytes('[]') - CONTEXT_DEFAULTS.briefBytes - 1536
    const off = new SessionContext(bareSession(kind, false))
    expect(() => off.check(instructions, 'p'.repeat(full), [])).not.toThrow()
    expect(() => off.check(instructions, 'p'.repeat(full + 1), [])).toThrow()
    off.close()
  })
}

// ---------------------------------------------------------------------------------------------
// 4. Plan mode
// ---------------------------------------------------------------------------------------------

/** The checks every adapter makes of scenario 4, over one run of it. */
function planModeChecks(run: () => Ran, received: () => { tool: string; arguments: Record<string, unknown> }[], executed: () => Record<string, number>) {
  const PLAN_OFFER = [...PLAN_RUNTIME_READS, 'read_policy', 'propose_plan', ...CONTEXT_SIX]

  it('4 · the offer holds no action tool: not the proposal, the save, the memory, the evaluation or the test run', () => {
    const offered = offeredNames(run())
    expect(sorted(offered)).toEqual(sorted(PLAN_OFFER))
    for (const action of [PROPOSE_PACK_TOOL, 'save_recipe', 'remember_note', REHEARSAL_TOOL, TEST_TOOL]) expect(offered).not.toContain(action)
  })

  it('4 · the Runtime reads offered are the gate’s own ceiling less the evaluation and the test run', () => {
    const offered = offeredNames(run()).filter((name) => RUNTIME_SIX.includes(name))
    expect(sorted(offered)).toEqual(sorted(ASSISTANT_TOOLS.filter((name) => name !== REHEARSAL_TOOL && name !== TEST_TOOL)))
    expect(sorted(offered)).toEqual(['get_example', 'get_schema', 'list_examples', 'validate'])
  })

  it('4 · a call to an action tool is refused, visible as a call and its failed result, and reaches neither the runtime nor the host', () => {
    const { events, seen } = run()
    const flow = events.filter((event) => event.type !== 'execution_checkpoint')
    // The refusals are the plan boundary's own (a tool call that fails with its reason), not the gate's guardrail event.
    for (const name of [REHEARSAL_TOOL, 'save_recipe']) {
      const at = flow.findIndex((event) => event.type === 'tool_call' && event.name === name)
      expect(at).toBeGreaterThan(-1)
      expect(flow[at + 1]).toMatchObject({ type: 'tool_result', name, isError: true, text: expect.stringContaining('Plan mode does not permit this operation') })
    }
    expect(seen).toEqual([])
    expect(executed().save_recipe).toBeUndefined()
  })

  it('4 · the plan is delivered through the plan tool, no proposal is shown for review, and the answer ends the run', () => {
    const { events } = run()
    expect(received().filter((call) => call.tool === 'propose_plan')).toEqual([
      { tool: 'propose_plan', arguments: expect.objectContaining({ objective: 'Add a receipt rule', steps: ['Read the receipts section', 'Draft the rule', 'Test the rule against the saved cases'] }) }
    ])
    expect(ofType(events, 'tool_result').find((event) => event.name === 'propose_plan')).toMatchObject({ isError: false })
    expect(ofType(events, 'proposal')).toEqual([])
    expect(ofType(events, 'message')).toEqual([{ type: 'message', text: (planMode.turns.at(-1) as { say: string }).say }])
    expect(ofType(events, 'error')).toEqual([])
    expect(events.filter((event) => event.type === 'end')).toHaveLength(1)
  })
}

// ---------------------------------------------------------------------------------------------
// 5. The correction loop
// ---------------------------------------------------------------------------------------------

/** What the loop left: each author run's events, the host's checks, and what the loop returned. */
interface Corrected {
  authors: AssistantEvent[][]
  /** What the host's validate said of each candidate, in order. */
  checks: { status: string; diagnostics: { code: string; instancePath: string }[] }[]
  outcome: { state: string; corrections: number }
  /** The model requests the engine sent (model engine) or the agent runs it started (agent engine). */
  modelRequests: { text: string }[] | null
  agentRuns: { prompt: string; phase: string }[] | null
  seen: { name: string; args: Record<string, unknown>; refusal: string }[]
}

const DRAFTS = scenario.documents as unknown as Record<string, unknown>
const TEST_GUIDANCE = 'THE RUNTIME’S TEST_PACK GUIDANCE, AS PROMPTS/GET SERVED IT'
const DECLARED: CriticCase[] = [
  { source: 'declared', id: correction.case.id, facts: scenario.documents.FACTS as Record<string, unknown>, expectedDisposition: { kind: 'outcome', outcomeId: correction.case.expectedOutcome } }
]

/** A turn of the author's script as the words the model sends: its sentence, then the proposal in a fenced block. */
const authorText = (turn: { proposal: string; say: string }) =>
  `${turn.say}\n\n\`\`\`json\n${JSON.stringify({ proposal: { kind: 'create', document: DRAFTS[turn.proposal], unknowns: scenario.unknowns } })}\n\`\`\``

/** A turn of the critic's script, a recorded step of the bake-off's scenario or a sentence. */
function criticTurn(turn: { call?: { recorded: string }; say?: string }): Turn {
  const recorded = turn.call?.recorded
  if (recorded === undefined) return { say: turn.say! }
  const step = scenario.steps.find((one) => one.id === recorded)!
  return { call: { tool: step.tool!, arguments: step.arguments as Record<string, unknown> } }
}

/** The findings the Runtime's validate gave, as the correction request carries them. */
const findingsOf = (check: Corrected['checks'][number]) => check.diagnostics.map((one) => `${one.code} at ${one.instancePath}`).join('; ')

async function correctionLoop(kind: 'model' | 'agent', engine: Engine, answerAs: AnswerAs): Promise<Corrected> {
  const runtime = await scriptedRuntime()
  const connection = openAssistantConnection({ allowed: scenario.scenarioTools, transport: runtime.transport, onEvent: () => undefined })
  const authors: AssistantEvent[][] = []
  const checks: Corrected['checks'] = []
  const sent: { text: string }[] = []
  const started: { prompt: string; phase: string }[] = []
  let attempt = 0
  const endpoint = scriptedEndpoint((incoming) => {
    const text = JSON.stringify(incoming.body)
    sent.push({ text })
    if (text.includes(REFUTATION_MARKER)) return answerWith(criticTurn(correction.critic[resultsIn(incoming)]!), answerAs, resultsIn(incoming))
    return answerWith({ say: authorText(correction.author[attempt]!) }, answerAs, 0)
  })
  const agent: AgentRun = async (incoming, callbacks) => {
    started.push({ prompt: incoming.prompt, phase: incoming.phase })
    if (incoming.phase === 'critic') {
      for (const [at, listed] of correction.critic.entries()) {
        const turn = criticTurn(listed)
        if ('call' in turn) {
          const offered = incoming.tools.find((tool) => tool.description.startsWith(`${turn.call.tool}:`))!
          await callbacks.tool({ id: `critic-${at}`, name: offered.name, arguments: structuredClone(turn.call.arguments) }, callbacks.signal)
        } else {
          await callbacks.event({ type: 'message', id: 'critic', text: turn.say, phase: 'final' })
        }
      }
      return
    }
    await callbacks.event({ type: 'message', id: `author-${attempt}`, text: authorText(correction.author[attempt]!), phase: 'final' })
  }
  if (kind === 'model') vi.stubGlobal('fetch', endpoint.fetch)
  try {
    const ready = await connection.ready
    const author = async (prompt: string, review: boolean): Promise<unknown> => {
      const events: AssistantEvent[] = []
      const common = {
        prompt,
        testPrompt: TEST_GUIDANCE,
        tools: ready.tools,
        callTool: ready.callTool,
        hostTools: [],
        adversarialReview: review,
        ...(review ? { criticCases: DECLARED } : {}),
        signal: new AbortController().signal
      }
      await runAssistantSession(
        engine,
        kind === 'model'
          ? { ...common, model: { family: 'openai-compatible' as const, model: 'scripted-model', call: bindModelCall('openai-compatible') }, thinking: normalize('off', 'openai-compatible') }
          : { ...common, agent: { model: 'scripted-agent', run: agent } },
        (event) => events.push(event)
      )
      authors.push(events)
      return ofType(events, 'proposal')[0]!.document
    }
    const check = async (document: unknown) => {
      const answered = await ready.callTool('validate', { document: JSON.stringify(document) })
      const body = JSON.parse(answered.content![0]!.text!) as { status: string; diagnostics?: { code: string; instancePath: string }[] }
      const seenCheck = { status: body.status, diagnostics: body.diagnostics ?? [] }
      checks.push(seenCheck)
      return seenCheck
    }
    const first = await author(scenario.policy, false)
    const outcome = await correctArtifact(
      {
        initial: first,
        maxCorrections: 1,
        fingerprint: (candidate) => JSON.stringify(candidate),
        check,
        passed: (found) => found.status === 'valid',
        correct: async (_candidate, found) => {
          attempt++
          return author(`${scenario.policy}\n\nThe Runtime's validate refused your draft: ${findingsOf(found)}. Correct the draft.`, true)
        },
        checkpoint: async () => undefined
      },
      new AbortController().signal
    )
    return {
      authors,
      checks,
      outcome: { state: outcome.state, corrections: outcome.corrections },
      modelRequests: kind === 'model' ? sent : null,
      agentRuns: kind === 'agent' ? started : null,
      seen: runtime.seen
    }
  } finally {
    vi.unstubAllGlobals()
    await connection.close()
    await runtime.close()
  }
}

/** The checks every adapter makes of scenario 5. */
function correctionChecks(get: () => Corrected) {
  it('5 · the first proposal fails validate, and the Runtime names the diagnostic', () => {
    const { authors, checks } = get()
    expect(ofType(authors[0]!, 'proposal')[0]!.document).toEqual(scenario.documents.DRAFT_V1)
    expect(checks[0]).toMatchObject({ status: 'invalid', diagnostics: [{ code: 'JPS-SEMANTIC-UNRESOLVED-OUTCOME', instancePath: '/rules/3/outcome' }] })
  })

  it('5 · one correction request follows, carrying the diagnostic, and its proposal passes validate', () => {
    const { authors, checks, outcome, modelRequests, agentRuns } = get()
    expect(outcome).toEqual({ state: 'ready', corrections: 1 })
    expect(checks.map((one) => one.status)).toEqual(['invalid', 'valid'])
    expect(ofType(authors[1]!, 'proposal')[0]!.document).toEqual(scenario.documents.DRAFT_V2)
    const authoring = modelRequests !== null ? modelRequests.filter((one) => !one.text.includes(REFUTATION_MARKER)).map((one) => one.text) : agentRuns!.filter((one) => one.phase === 'author').map((one) => one.prompt)
    expect(authoring).toHaveLength(2)
    expect(authoring[0]).not.toContain('JPS-SEMANTIC-UNRESOLVED-OUTCOME')
    expect(authoring[1]).toContain('JPS-SEMANTIC-UNRESOLVED-OUTCOME at /rules/3/outcome')
  })

  it('5 · the request count: two authoring requests and the critic’s three on the model engine, three agent runs on the agent engine', () => {
    const { modelRequests, agentRuns, seen } = get()
    if (modelRequests !== null) expect(modelRequests).toHaveLength(5)
    else expect(agentRuns!.map((one) => one.phase)).toEqual(['author', 'author', 'critic'])
    // Every call that reached the runtime matched a recorded answer, and every evaluation was a rehearsal.
    expect(seen.filter((call) => call.refusal !== '')).toEqual([])
    expect(seen.filter((call) => call.name === 'experimental_evaluate').every((call) => call.args.rehearsal === true)).toBe(true)
  })

  it('5 · the critic’s comparison rows name the declared case and its agreement', () => {
    const critique = ofType(get().authors[1]!, 'critique')
    expect(critique).toHaveLength(1)
    expect(critique[0]).toMatchObject({
      refuted: false,
      checks: [{ tool: 'validate', status: 'valid' }, { tool: 'experimental_evaluate', status: 'evaluated' }],
      cases: [{ case: correction.case.id, source: 'declared', tool: 'experimental_evaluate', agrees: true, actual: { disposition: { kind: 'outcome', outcomeId: 'approve' } } }]
    })
  })

  it('5 · no case is marked incomplete where every declared case is compared', () => {
    const [critique] = ofType(get().authors[1]!, 'critique')
    expect(critique!.incomplete ?? []).toEqual([])
    expect(ofType(get().authors[1]!, 'proposal')[0]!.critique).toMatchObject({ refuted: false })
    expect(ofType(get().authors[1]!, 'proposal')[0]!.critique?.incomplete ?? []).toEqual([])
    expect(get().authors.flatMap((events) => ofType(events, 'error'))).toEqual([])
  })
}

// ---------------------------------------------------------------------------------------------
// 6. A multi-turn transcript
// ---------------------------------------------------------------------------------------------

interface IndexData {
  steps: { tool: string; read: { id: string } }[]
  omitted: number
}

/** The JSON inside an index's data block. */
function indexData(text: string): IndexData {
  const marker = text.match(new RegExp(`^${OPENING} ([0-9a-f]{32}) from ${INDEX_SOURCE};`, 'm'))?.[1]
  expect(marker).toBeDefined()
  return JSON.parse(unframeData(text.slice(INDEX_NOTICE.length), marker!)!) as IndexData
}

/** The index part of a run's first message, as the model engine sent it, or of the agent's prompt; null where there was none. */
function indexTextOf(ran: Ran): string | null {
  if (ran.requests.length > 0) return openingTexts(ran.requests[0]!).find((text) => text.startsWith(INDEX_NOTICE)) ?? null
  const prompt = ran.runs[0]!.prompt
  const at = prompt.indexOf(INDEX_NOTICE)
  if (at < 0) return null
  const closing = prompt.indexOf('\nEND RETRIEVED DATA ', at)
  const end = prompt.indexOf('\n', closing + 1)
  return prompt.slice(at, end < 0 ? undefined : end)
}

interface Transcript {
  chat: ChatContext
  ran: Ran[]
  /** How often each source answered, after each turn. */
  reads: { schema: number; instructions: number; tests: number }[]
  /** The ledger's size after each turn. */
  held: number[]
  /** A fourth run on the chat after it was disposed of. */
  after: Ran
}

const RUNTIME = { identity: 'jpack', version: '0.19.0' }

async function transcript(run: (model: Turn[], options: Parameters<typeof runOnModelEngine>[1]) => Promise<Ran>): Promise<Transcript> {
  const chat = new ChatContext()
  const executed: Record<string, number> = {}
  let schemaReads = 0
  const handle = (call: RuntimeCall) => {
    if (call.name === 'get_schema') schemaReads++
    return runtimeAnswer(call.name === 'get_schema' ? multiTurn.sources.schema : { status: 'ok' })
  }
  const hostTools = hostToolsOf(multiTurn.hostTools as never, executed)
  const ran: Ran[] = []
  const reads: Transcript['reads'] = []
  const held: number[] = []
  for (const turn of multiTurn.turns) {
    ran.push(await run(turn.model as unknown as Turn[], { handle, hostTools, session: { prompt: turn.request, context: { chat, runtime: RUNTIME, ...request(turn.request) } } }))
    reads.push({ schema: schemaReads, instructions: executed.get_authoring_instructions ?? 0, tests: executed.read_pack_tests ?? 0 })
    held.push(chat.entries.size)
  }
  chat.dispose()
  const after = await run(multiTurn.turns[0]!.model as unknown as Turn[], { handle, hostTools, session: { prompt: 'Once more.', context: { chat, runtime: RUNTIME, ...request('Once more.') } } })
  return { chat, ran, reads, held, after }
}

/** The checks every adapter makes of scenario 6. */
function transcriptChecks(get: () => Transcript) {
  it('6 · the first turn reads each source once; the second and third read none of them again', () => {
    expect(get().reads).toEqual([
      { schema: 1, instructions: 1, tests: 1 },
      { schema: 1, instructions: 1, tests: 1 },
      { schema: 1, instructions: 1, tests: 1 }
    ])
    // The reads were asked for again, and served from the chat: each turn called the schema and the instructions.
    for (const ran of get().ran) expect(ofType(ran.events, 'tool_call').map((event) => event.name)).toContain('get_schema')
    expect(get().ran.flatMap((ran) => ofType(ran.events, 'error'))).toEqual([])
  })

  it('6 · the index is present on turns two and three, lists what turn one kept, and stays within its bound', () => {
    const { ran } = get()
    expect(indexTextOf(ran[0]!)).toBeNull()
    for (const later of ran.slice(1)) {
      const text = indexTextOf(later)!
      expect(text).not.toBeNull()
      expect(indexData(text).steps.map((step) => step.tool)).toEqual(['get_schema', 'get_authoring_instructions', 'read_pack_tests'])
      expect(indexData(text).omitted).toBe(0)
      expect(utf8Bytes(text)).toBeLessThanOrEqual(CONTEXT_DEFAULTS.turnIndexBytes)
      expect(indexData(text).steps.length).toBeLessThanOrEqual(CONTEXT_DEFAULTS.turnIndexEntries)
    }
  })

  it('6 · the ledger survives each run, and is gone after dispose() with the later run refused', () => {
    const { chat, held, after } = get()
    expect(held.every((size) => size > 0)).toBe(true)
    expect(held.at(-1)!).toBeGreaterThanOrEqual(held[0]!)
    expect(chat.turnCount).toBe(3)
    expect(chat.entries.size).toBe(0)
    expect(chat.isDisposed).toBe(true)
    expect(after.requests.length + after.runs.length).toBe(0)
    expect(ofType(after.events, 'error')[0]!.message).toMatch(/disposed|could not complete/)
    expect(after.events.at(-1)).toEqual({ type: 'end' })
  })
}

// ---------------------------------------------------------------------------------------------
// The model engines
// ---------------------------------------------------------------------------------------------

describe.each(MODEL_ENGINES)('engine %s · scenarios', (engineId) => {
  describe.each(LEGS)('answered as %s', (answerAs) => {
    describe('1 · chat mode with host tools', () => {
      const executed: Record<string, number> = {}
      let ran: Ran
      beforeAll(async () => {
        ran = await runOnModelEngine(answerFrom(chatHostTools.turns as unknown as Turn[], answerAs), {
          engine: await loadEngine(engineId),
          hostTools: hostToolsOf(chatHostTools.hostTools as never, executed),
          session: { prompt: chatHostTools.request, context: request(chatHostTools.request) }
        })
      })
      afterAll(() => vi.unstubAllGlobals())
      chatHostToolsChecks(() => ran, () => executed, () => null)
    })

    describe('2 · delegation', () => {
      let state: Delegated
      beforeAll(async () => {
        const base = await loadEngine(engineId)
        const counts = { parents: 0, children: 0 }
        const counted: Engine = {
          id: base.id,
          start(session: EngineSession) {
            if (session.hostTools.some((tool) => tool.name === DELEGATE_TOOL)) counts.parents++
            else counts.children++
            return base.start(session)
          }
        }
        const chat = new ChatContext()
        const known: Known = { references: [], tasks: {} }
        const ran: Ran[] = []
        for (const response of delegation.responses) {
          ran.push(
            await runOnModelEngine(rolesAnswer({ parent: response.parent, children: delegation.children } as unknown as Roles, answerAs, known), {
              engine: withDelegation(counted),
              hostTools: hostToolsOf(delegation.hostTools as never, {}),
              session: { interactive: true, prompt: response.request, context: { chat, documents: delegation.documents as never, ...request(response.request) } }
            })
          )
        }
        state = { ran, known, chat, answers: [], ...counts }
      })
      afterAll(() => vi.unstubAllGlobals())
      delegationChecks(() => state, 'model')
    })

    describe('3 · memory', () => {
      let ran: Ran
      beforeAll(async () => {
        ran = await runOnModelEngine(answerFrom(memory.turns as unknown as Turn[], answerAs), {
          engine: await loadEngine(engineId),
          handle: memoryAnswers,
          session: { prompt: memory.request, context: { memory: true, ...request(memory.request) } }
        })
      })
      afterAll(() => vi.unstubAllGlobals())

      it('3 · the run makes three tool calls and answers on its fourth request', () => {
        expect(ofType(ran.events, 'tool_call').map((event) => event.name)).toEqual(['get_schema', 'list_examples', 'get_example'])
        expect(ran.seen.map((call) => call.name)).toEqual(['get_schema', 'list_examples', 'get_example'])
        expect(ran.requests).toHaveLength(4)
        expect(ofType(ran.events, 'error')).toEqual([])
      })

      it('3 · the recall rule finds the person’s message on every request', () => {
        const found = ran.requests.map((sent) => recallQuery('openai-compatible' as EndpointKind, sent.body as Json))
        expect(found.map((one) => one?.text)).toEqual(ran.requests.map(() => memory.request))
      })

      reserveChecks('model')
    })

    describe('4 · plan mode', () => {
      const received: { tool: string; arguments: Record<string, unknown> }[] = []
      const executed: Record<string, number> = {}
      let ran: Ran
      beforeAll(async () => {
        ran = await runOnModelEngine(answerFrom(planMode.turns as unknown as Turn[], answerAs), {
          engine: await loadEngine(engineId),
          hostTools: hostToolsOf(planMode.hostTools as never, executed, received),
          session: { prompt: planMode.request, context: { phase: 'plan', ...request(planMode.request) } }
        })
      })
      afterAll(() => vi.unstubAllGlobals())
      planModeChecks(() => ran, () => received, () => executed)
    })

    describe('5 · the correction loop', () => {
      let state: Corrected
      beforeAll(async () => {
        state = await correctionLoop('model', await loadEngine(engineId), answerAs)
      })
      correctionChecks(() => state)
    })

    describe('6 · a multi-turn transcript', () => {
      let state: Transcript
      beforeAll(async () => {
        const engine = await loadEngine(engineId)
        state = await transcript(async (model, options) => {
          try {
            return await runOnModelEngine(answerFrom(model, answerAs), { ...options, engine })
          } finally {
            vi.unstubAllGlobals()
          }
        })
      })
      transcriptChecks(() => state)
    })
  })
})

// ---------------------------------------------------------------------------------------------
// The agent engines
// ---------------------------------------------------------------------------------------------

describe.each(AGENT_ENGINES)('agent engine %s · scenarios', (engineId) => {
  describe('1 · chat mode with host tools', () => {
    const executed: Record<string, number> = {}
    const answers: { text: string; isError: boolean }[] = []
    let ran: Ran
    beforeAll(async () => {
      ran = await runOnAgentEngine(agentRunFrom(chatHostTools.turns as unknown as Turn[], answers), {
        engine: await loadEngine(engineId),
        hostTools: hostToolsOf(chatHostTools.hostTools as never, executed),
        session: { prompt: chatHostTools.request, context: request(chatHostTools.request) }
      })
    })
    afterAll(() => vi.unstubAllGlobals())
    chatHostToolsChecks(() => ran, () => executed, () => answers)
  })

  describe('2 · delegation', () => {
    let state: Delegated
    beforeAll(async () => {
      const base = await loadEngine(engineId)
      const chat = new ChatContext()
      const known: Known = { references: [], tasks: {} }
      const answers: Delegated['answers'] = []
      const ran: Ran[] = []
      for (const response of delegation.responses) {
        ran.push(
          await runOnAgentEngine(rolesRun({ parent: response.parent, children: delegation.children } as unknown as Roles, known, answers), {
            engine: withDelegation(base),
            hostTools: hostToolsOf(delegation.hostTools as never, {}),
            session: { interactive: true, prompt: response.request, context: { chat, documents: delegation.documents as never, ...request(response.request) } }
          })
        )
      }
      state = { ran, known, chat, answers, parents: 0, children: 0 }
    })
    afterAll(() => vi.unstubAllGlobals())
    delegationChecks(() => state, 'agent')
  })

  describe('3 · memory', () => {
    const answers: { text: string; isError: boolean }[] = []
    let ran: Ran
    beforeAll(async () => {
      ran = await runOnAgentEngine(agentRunFrom(memory.turns as unknown as Turn[], answers), {
        engine: await loadEngine(engineId),
        handle: memoryAnswers,
        session: { prompt: memory.request, context: { memory: true, ...request(memory.request) } }
      })
    })
    afterAll(() => vi.unstubAllGlobals())

    it('3 · the run makes three tool calls inside its one agent run', () => {
      expect(ofType(ran.events, 'tool_call').map((event) => event.name)).toEqual(['get_schema', 'list_examples', 'get_example'])
      expect(ran.seen.map((call) => call.name)).toEqual(['get_schema', 'list_examples', 'get_example'])
      expect(ran.runs).toHaveLength(1)
      expect(ofType(ran.events, 'error')).toEqual([])
    })

    // Recorded disagreement: the agent engine sends no model request the Desk's server could recall for; its one request is the run's prompt, which opens with the person's message.
    it('3 · the person’s message opens the run’s one request (the agent wire has no later request to recall for)', () => {
      expect(ran.runs[0]!.prompt.startsWith(memory.request)).toBe(true)
    })

    reserveChecks('agent')
  })

  describe('4 · plan mode', () => {
    const received: { tool: string; arguments: Record<string, unknown> }[] = []
    const executed: Record<string, number> = {}
    let ran: Ran
    beforeAll(async () => {
      ran = await runOnAgentEngine(agentRunFrom(planMode.turns as unknown as Turn[]), {
        engine: await loadEngine(engineId),
        hostTools: hostToolsOf(planMode.hostTools as never, executed, received),
        session: { prompt: planMode.request, context: { phase: 'plan', ...request(planMode.request) } }
      })
    })
    afterAll(() => vi.unstubAllGlobals())
    planModeChecks(() => ran, () => received, () => executed)
  })

  describe('5 · the correction loop', () => {
    let state: Corrected
    beforeAll(async () => {
      state = await correctionLoop('agent', await loadEngine(engineId), 'stream')
    })
    correctionChecks(() => state)
  })

  describe('6 · a multi-turn transcript', () => {
    let state: Transcript
    beforeAll(async () => {
      const engine = await loadEngine(engineId)
      state = await transcript((model, options) => runOnAgentEngine(agentRunFrom(model), { ...options, engine }))
    })
    transcriptChecks(() => state)
  })
})
