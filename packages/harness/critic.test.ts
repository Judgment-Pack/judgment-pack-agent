/**
 * The critic (agent#10), on both engines, through the real tool gate:
 *
 * - its tools are an allow-list of four reads, and every other name it calls
 *   is refused below the engine, before the gate and before a host tool runs;
 * - it compares the outcomes of what it runs with the outcomes the host
 *   declares, so a rehearsal that says `evaluated` with the wrong outcome
 *   refutes the proposal and the critique names the case;
 * - one default decides whether it runs, the policy table's, on both engines;
 * - the host may name a cheaper model for it per connection;
 * - what it cost is on the critique, and on the note where it did not finish.
 */
import { afterEach, describe, expect, it, vi } from 'vitest'
import policyTable from './contract/policy.json'
import { CONTEXT_DEFAULTS } from './context/policy'
import { ASSISTANT_TOOLS, configureHarness } from './host'
import { REFUSED_RUNTIME_TOOLS } from './toolGate'
import { bindAgentRun } from './agentTransport'
import {
  CRITIC_REHEARSALS,
  CRITIC_TOOLS,
  REFUTATION_MARKER,
  criticMatrix,
  criticMayCall,
  criticMessage,
  critiqueEvent,
  critiqueOnProposal,
  openCritique,
  outcomeAgrees,
  readCriticCases,
  rehearsalOf,
  verdictOf,
  type CriticCase,
  type CriticFigures
} from './refutation'
import { runAssistantSession } from './session'
import { normalize } from './thinking'
import { codex } from './engines/codex'
import { PROPOSE_PACK_SCHEMA } from './engines/contract'
import { questionTool } from './questions'
import { TASK_PROFILES, delegationTool } from './delegationContract'
import { RECORDED_TOOLS } from './certification/scriptedServer'
import {
  TEST_CASES_TOOL,
  codexTurn,
  gatedRuntime,
  ofType,
  runtimeAnswer,
  sseTurn,
  streamed,
  vercelTurn,
  whole,
  wholeTurn,
  type EndpointRequest,
  type RuntimeCall
} from './testing/scripted'
import runtimeRecording from './certification/runtime.json'
import scenarioFixture from './certification/scenario.json'
import outcomeRefuted from '../../contract/vectors/critic/outcome-refuted.json'
import testRunCompared from '../../contract/vectors/critic/test-run-compared.json'
import declaredCases from '../../contract/vectors/critic/declared-cases.json'
import criticTools from '../../contract/vectors/tool-offer/critic-tools.json'
import type { AgentRequest, AgentRun } from './agent'
import type { AssistantEvent, HostTool, McpToolResult } from './engine'

afterEach(() => {
  vi.unstubAllGlobals()
})

/** The proposed document, and the proposal that carries it. */
const DOCUMENT = { specVersion: '0.2.0-draft', id: 'https://example.com/expense', title: 'Expense' }
const PROPOSAL = 'Ready.\n\n```json\n' + JSON.stringify({ proposal: { document: DOCUMENT, unknowns: [] } }) + '\n```'
const FACTS = { claim: { amount: '120.00', receiptAttached: true } }
const APPROVE = { handoff: { state: 'none' }, kind: 'outcome', outcomeId: 'approve', reasons: [] }
const DECLINE = { handoff: { state: 'none' }, kind: 'outcome', outcomeId: 'decline', reasons: [] }
/** A runtime's rehearsal answer: evaluated, with this disposition. */
const evaluated = (disposition: unknown) => runtimeAnswer({ status: 'evaluated', rehearsal: true, disposition })
const isCritic = (request: EndpointRequest) => JSON.stringify(request.body).includes(REFUTATION_MARKER)

/** Host tools a critic must never reach, each with its own spy. */
function actionTools() {
  const execute = vi.fn(async (): Promise<McpToolResult> => ({ content: [{ type: 'text', text: 'done' }] }))
  const tool = (name: string, extra: Partial<HostTool> = {}): HostTool => ({ name, description: `${name} for the test`, inputSchema: { type: 'object' }, execute, ...extra })
  return {
    execute,
    tools: [
      tool('propose_dialect_action', { effect: 'proposal' }),
      tool('request_dialect_research', { effect: 'workflow' }),
      tool('propose_graph', { effect: 'proposal' }),
      tool('propose_pack', { effect: 'proposal', inputSchema: PROPOSE_PACK_SCHEMA }),
      tool('remember_memory', { effect: 'memory' }),
      tool('read_page', { effect: 'read' }),
      questionTool,
      delegationTool(TASK_PROFILES)
    ]
  }
}

/**
 * Every name a critic may not call: the runtime's other reads, tools the gate
 * refuses by name, a name nothing serves, every action and read the host
 * offers, and the context tools.
 */
const REFUSED_FOR_THE_CRITIC = [
  'list_examples',
  'get_example',
  ...Object.keys(REFUSED_RUNTIME_TOOLS),
  'write_file',
  'propose_dialect_action',
  'request_dialect_research',
  'propose_graph',
  'propose_pack',
  'remember_memory',
  'read_page',
  'ask_user',
  'delegate_tasks',
  'list_context',
  'read_context',
  'read_window',
  'search',
  'update_plan',
  'request_budget_increase'
]

/** The runtime's answer for one call: a rehearsal's outcome, or `ok`. */
const answerWith = (outcome: unknown) => (call: RuntimeCall) =>
  call.name === 'experimental_evaluate' ? evaluated(outcome) : call.name === 'validate' ? runtimeAnswer({ status: 'valid', diagnostics: [] }) : runtimeAnswer({ status: 'ok' })

/**
 * One `vercel` turn whose loop proposes at once and whose critic makes `calls`,
 * one per request, then says it is done.
 */
async function vercelCritic(calls: { name: string; args: Record<string, unknown> }[], options: { hostTools?: HostTool[]; cases?: CriticCase[]; outcome?: unknown; answer?: (request: EndpointRequest) => Response; handle?: (call: RuntimeCall) => unknown } = {}) {
  let turn = 0
  const turnRecord = await vercelTurn({
    answer: options.answer ?? ((request) => {
      if (!isCritic(request)) return streamed(sseTurn({ text: PROPOSAL }))
      const next = calls[turn++]
      return streamed(next === undefined ? sseTurn({ text: 'Done.' }) : sseTurn({ tool: { name: next.name, args: next.args, id: `critic_${turn}` } }))
    }),
    handle: options.handle ?? answerWith(options.outcome ?? APPROVE),
    hostTools: options.hostTools ?? [],
    session: { testPrompt: 'Test the pack with the runtime.', adversarialReview: true, ...(options.cases === undefined ? {} : { criticCases: options.cases }) }
  })
  return turnRecord
}

/**
 * One `codex` turn through the real gate: the author proposes at once, and the
 * critic run makes `calls` by the author's aliases (or a bare name nothing
 * offered), then says it is done.
 */
async function codexCritic(calls: { name: string; args: Record<string, unknown> }[], options: { hostTools?: HostTool[]; cases?: CriticCase[]; outcome?: unknown; handle?: (call: RuntimeCall) => unknown } = {}) {
  const events: AssistantEvent[] = []
  const requests: AgentRequest[] = []
  const answers: { text: string; isError: boolean }[] = []
  const runtime = await gatedRuntime({
    tools: [...RECORDED_TOOLS, TEST_CASES_TOOL],
    allowed: [...ASSISTANT_TOOLS],
    handle: options.handle ?? answerWith(options.outcome ?? APPROVE),
    onEvent: (event) => events.push(event)
  })
  let authorTools: AgentRequest['tools'] = []
  const run: AgentRun = async (request, callbacks) => {
    requests.push(request)
    if (request.phase === 'author') {
      authorTools = request.tools
      await callbacks.event({ type: 'message', id: 'a', text: PROPOSAL, phase: 'final' })
      return
    }
    for (const [index, call] of calls.entries()) {
      const alias = authorTools.find((tool) => tool.description.startsWith(`${call.name}:`))?.name ?? call.name
      answers.push(await callbacks.tool({ id: `critic-${index}`, name: alias, arguments: call.args }, callbacks.signal))
    }
    await callbacks.event({ type: 'message', id: 'c', text: 'Done.', phase: 'final' })
  }
  try {
    await runAssistantSession(codex, {
      prompt: 'Check the expense pack.',
      testPrompt: 'Test the pack with the runtime.',
      tools: runtime.tools,
      callTool: runtime.callTool,
      hostTools: options.hostTools ?? [],
      allowConversation: true,
      adversarialReview: true,
      ...(options.cases === undefined ? {} : { criticCases: options.cases }),
      agent: { model: 'agent-model', run },
      signal: new AbortController().signal
    }, (event) => events.push(event))
  } finally {
    await runtime.close()
  }
  return { events, seen: runtime.seen, requests, answers }
}

/** The tool names a vercel request offered, by the OpenAI-compatible wire. */
const offeredIn = (request: EndpointRequest) =>
  ((request.body as { tools?: { function: { name: string } }[] }).tools ?? []).map((tool) => tool.function.name)

/**
 * Each refused call's events, in order: the call, the refusal, the result. The
 * checkpoints a tracked run saves between them are left out: the refusal sits
 * where the gate's own does, before the result's checkpoint.
 */
function refusalOf(events: AssistantEvent[], name: string) {
  const work = events.filter((event) => event.type !== 'execution_checkpoint')
  const at = work.findIndex((event) => event.type === 'tool_call' && event.name === name)
  return work.slice(at, at + 3)
}

describe('the critic’s tools are an allow-list of reads (agent#10)', () => {
  it('names four, the runtime’s reads about the document, and no other', () => {
    expect([...CRITIC_TOOLS]).toEqual(['validate', 'experimental_evaluate', 'get_schema', 'experimental_test_cases'])
    for (const name of CRITIC_TOOLS) expect(ASSISTANT_TOOLS as readonly string[]).toContain(name)
    for (const name of [...REFUSED_FOR_THE_CRITIC, 'toString', 'constructor', '__proto__', 'propose_anything', 'request_anything']) {
      expect(criticMayCall(name), name).toBe(false)
    }
  })

  it.each(REFUSED_FOR_THE_CRITIC)('vercel: the critic’s call of %s is refused below the engine, and nothing runs', async (name) => {
    const { execute, tools } = actionTools()
    const { events, seen, requests } = await vercelCritic([{ name, args: {} }], { hostTools: tools })
    // Offered: the allow-list of what the runtime serves, and no host tool.
    const critic = requests.filter(isCritic)
    expect(critic.length).toBeGreaterThan(0)
    for (const request of critic) expect(offeredIn(request)).toEqual(['get_schema', 'validate', 'experimental_evaluate', 'experimental_test_cases'])
    // Nothing reached the runtime or a host tool, and nothing paused the run.
    expect(seen).toEqual([])
    expect(execute).not.toHaveBeenCalled()
    expect(events.filter((event) => ['user_question', 'delegation_requested', 'error'].includes(event.type))).toEqual([])
    // The call, the boundary's refusal, the result the model read.
    expect(refusalOf(events, name)).toEqual([
      { type: 'tool_call', name, args: {} },
      { type: 'guardrail', tool: name, action: 'refused', detail: expect.stringContaining(`${name} is not one of the tools the critic may call`) },
      { type: 'tool_result', name, isError: true, text: expect.stringContaining('refused') }
    ])
    expect(ofType(events, 'proposal')).toHaveLength(1)
  })

  it.each(REFUSED_FOR_THE_CRITIC)('codex: the critic’s call of %s is refused below the engine, and nothing runs', async (name) => {
    const { execute, tools } = actionTools()
    const { events, seen, requests, answers } = await codexCritic([{ name, args: {} }], { hostTools: tools })
    const critic = requests.find((request) => request.phase === 'critic')!
    expect(critic.tools.map((tool) => tool.description.split(':')[0])).toEqual(['get_schema', 'validate', 'experimental_evaluate', 'experimental_test_cases'])
    expect(seen).toEqual([])
    expect(execute).not.toHaveBeenCalled()
    expect(events.filter((event) => ['user_question', 'delegation_requested', 'error'].includes(event.type))).toEqual([])
    expect(refusalOf(events, name)).toEqual([
      { type: 'tool_call', callId: 'critic-0', name, args: {} },
      { type: 'guardrail', tool: name, action: 'refused', detail: expect.stringContaining(`${name} is not one of the tools the critic may call`) },
      { type: 'tool_result', callId: 'critic-0', name, isError: true, text: expect.stringContaining('refused') }
    ])
    expect(answers).toEqual([{ isError: true, text: expect.stringContaining('refused') }])
    expect(ofType(events, 'proposal')).toHaveLength(1)
  })

  it.each(['vercel', 'codex'] as const)('%s: the critic’s own reads go through the same gate as the loop’s', async (engine) => {
    const calls = [
      { name: 'validate', args: { document: JSON.stringify(DOCUMENT) } },
      { name: 'experimental_evaluate', args: { pack: JSON.stringify(DOCUMENT), facts: JSON.stringify(FACTS) } }
    ]
    const { events, seen } = engine === 'vercel' ? await vercelCritic(calls) : await codexCritic(calls)
    expect(seen.map((call) => call.name)).toEqual(['validate', 'experimental_evaluate'])
    // The gate rewrote the critic's evaluate to a rehearsal on the wire, and said so.
    expect(seen[1]!.args.rehearsal).toBe(true)
    expect(ofType(events, 'guardrail')).toEqual([expect.objectContaining({ tool: 'experimental_evaluate', action: 'rewrote' })])
    expect(ofType(events, 'critique')[0]).toMatchObject({ refuted: false, checks: [{ tool: 'validate', status: 'valid' }, { tool: 'experimental_evaluate', status: 'evaluated' }] })
  })

  it.each(['vercel', 'codex'] as const)('%s: the critic’s test calls count against the turn’s cap with the loop’s', async (engine) => {
    const test = { name: 'experimental_test_cases', args: { pack: JSON.stringify(DOCUMENT), matrix: JSON.stringify(criticMatrix(CASES)) } }
    const run = engine === 'vercel' ? vercelCritic : codexCritic
    const { events, seen } = await run(engine === 'vercel' ? [test, test, test] : [test, test, test, test])
    const reached = seen.filter((call) => call.name === 'experimental_test_cases').length
    // vercel's critic has three tool turns (MAX_CRITIC_TURNS less its answer); codex's run makes four.
    expect(reached).toBe(3)
    if (engine === 'codex') expect(ofType(events, 'tool_result').at(-1)).toMatchObject({ isError: true, text: expect.stringContaining('testCallsPerTurn') })
  })
})

const CASES: CriticCase[] = [
  { source: 'declared', id: 'receipted-120', facts: FACTS, expectedDisposition: { kind: 'outcome', outcomeId: 'approve' } },
  { source: 'test', id: 'unreceipted-120', facts: { claim: { amount: '120.00', receiptAttached: false } }, expectedDisposition: { kind: 'outcome', outcomeId: 'decline' } }
]

describe('the critic compares outcomes with the declared ones (agent#10)', () => {
  const rehearse = (facts: unknown, extra: Record<string, unknown> = {}) => ({ pack: JSON.stringify(DOCUMENT), facts: JSON.stringify(facts), ...extra })
  const saw = (cases: CriticCase[], args: Record<string, unknown>, disposition: unknown) => {
    const recorder = openCritique({ cases, document: DOCUMENT })
    const answer = evaluated(disposition)
    recorder.saw('experimental_evaluate', answer.content![0]!.text!, args, answer.structuredContent)
    return recorder.critique('')
  }

  it('refutes a rehearsal that says evaluated with an outcome the declared case does not have', () => {
    const critique = saw(CASES, rehearse(FACTS), DECLINE)
    expect(critique.checks).toEqual([{ tool: 'experimental_evaluate', status: 'evaluated', diagnostics: 0 }])
    expect(critique.refuted).toBe(true)
    expect(critique.cases).toEqual([
      { case: 'receipted-120', source: 'declared', tool: 'experimental_evaluate', agrees: false, expected: { disposition: { kind: 'outcome', outcomeId: 'approve' } }, actual: { disposition: DECLINE } }
    ])
    expect(critique.notCompared).toEqual(['unreceipted-120'])
    expect(critique.text).toContain('case receipted-120 (declared) disagrees: declared {"kind":"outcome","outcomeId":"approve"}, experimental_evaluate gave {"kind":"outcome","outcomeId":"decline"}')
    expect(critique.text).toContain('not compared: unreceipted-120')
    // The status rule alone, which is what refuted meant before, says nothing here.
    expect(verdictOf(critique.checks)).toBe(false)
  })

  it('agrees where every member the case states is the outcome’s, and keys in any order', () => {
    const critique = saw(CASES, rehearse({ claim: { receiptAttached: true, amount: '120.00' } }), APPROVE)
    expect(critique.refuted).toBe(false)
    expect(critique.cases!.map((item) => item.agrees)).toEqual([true])
    expect(critique.text).toContain('1 declared case(s) agree')
  })

  it('compares nothing a rehearsal did not run over the proposed document with the case’s own inputs', () => {
    // Another pack, a pack_id, other evidence, other extensions, other facts.
    for (const args of [
      { ...rehearse(FACTS), pack: JSON.stringify({ ...DOCUMENT, title: 'Another' }) },
      { pack_id: 'expense', facts: JSON.stringify(FACTS) },
      rehearse(FACTS, { evidence: JSON.stringify({ receipt: 'present' }) }),
      rehearse(FACTS, { supported_extensions: ['x-extension'] }),
      rehearse({ claim: { amount: '12.00' } })
    ]) {
      const critique = saw(CASES, args, DECLINE)
      expect(critique.cases, JSON.stringify(args)).toEqual([])
      expect(critique.refuted).toBe(false)
    }
    // An evidence document that leaves every requirement unknown is the case's none.
    expect(saw(CASES, rehearse(FACTS, { evidence: '{}' }), DECLINE).refuted).toBe(true)
  })

  it('compares an expected evaluation error by its class and, where stated, its phase', () => {
    const cases: CriticCase[] = [{ source: 'test', id: 'bad-version', facts: FACTS, expectedErrorClass: 'pack-not-conformant', expectedErrorPhase: 'preflight' }]
    const recorder = openCritique({ cases, document: DOCUMENT })
    recorder.saw('experimental_evaluate', 'the pack is not conformant', rehearse(FACTS), { status: 'error', evaluationError: { class: 'pack-not-conformant', phase: 'preflight' } })
    expect(recorder.critique('').cases).toEqual([expect.objectContaining({ agrees: true, actual: { errorClass: 'pack-not-conformant', phase: 'preflight' } })])
    expect(outcomeAgrees({ errorClass: 'malformed-input' }, { errorClass: 'pack-not-conformant' })).toBe(false)
    expect(outcomeAgrees({ errorClass: 'malformed-input' }, { disposition: APPROVE })).toBe(false)
    expect(outcomeAgrees({ disposition: { kind: 'outcome' } }, { errorClass: 'malformed-input' })).toBe(false)
  })

  it('reads a test run’s rows of the declared cases, sent as declared, and nothing the critic changed', () => {
    const report = (status: string) => ({
      status: status === 'passed' ? 'passed' : 'failed',
      packs: [{ rows: [
        { id: 'receipted-120', status: 'passed', expected: JSON.stringify(APPROVE), actual: JSON.stringify(APPROVE) },
        { id: 'unreceipted-120', status, expected: JSON.stringify(DECLINE), actual: JSON.stringify(APPROVE) }
      ] }]
    })
    const matrix = criticMatrix(CASES)
    const run = (sent: unknown, status: string) => {
      const recorder = openCritique({ cases: CASES, document: DOCUMENT })
      recorder.saw('experimental_test_cases', JSON.stringify(report(status)), { pack: JSON.stringify(DOCUMENT), matrix: JSON.stringify(sent) }, report(status))
      return recorder.critique('')
    }
    const failed = run(matrix, 'failed')
    expect(failed.refuted).toBe(true)
    expect(failed.cases).toEqual([
      expect.objectContaining({ case: 'receipted-120', tool: 'experimental_test_cases', agrees: true }),
      { case: 'unreceipted-120', source: 'test', tool: 'experimental_test_cases', agrees: false, expected: { disposition: { kind: 'outcome', outcomeId: 'decline' } }, actual: { disposition: APPROVE } }
    ])
    // A test run is not a status check: only its rows of declared cases count.
    expect(failed.checks).toEqual([])
    expect(run(matrix, 'passed').refuted).toBe(false)
    // The critic rewrote the second case's expectation: that row compares nothing.
    const changed = { ...matrix, cases: [matrix.cases[0], { ...matrix.cases[1], expectedDisposition: { kind: 'outcome', outcomeId: 'approve' } }] }
    expect(run(changed, 'passed').cases!.map((item) => item.case)).toEqual(['receipted-120'])
    expect(run(changed, 'passed').notCompared).toEqual(['unreceipted-120'])
  })

  it('hands the critic the declared cases as the runtime’s matrix, without their source', () => {
    const message = criticMessage('GUIDANCE', DOCUMENT, CASES)
    expect(message).toContain('"matrixVersion": "3"')
    expect(message).toContain('"id": "unreceipted-120"')
    expect(message).not.toContain('"source"')
    expect(message.indexOf('"matrixVersion"')).toBeGreaterThan(message.indexOf('"title": "Expense"'))
    expect(criticMessage('GUIDANCE', DOCUMENT)).not.toContain('matrixVersion')
  })

  it.each([
    [[{ source: 'declared', id: 'a', facts: {} }], 'exactly one of a disposition and an error class'],
    [[{ source: 'declared', id: 'a', facts: {}, expectedDisposition: { kind: 'outcome' }, expectedErrorClass: 'x' }], 'exactly one'],
    [[{ source: 'other', id: 'a', facts: {}, expectedErrorClass: 'x' }], 'no source'],
    [[{ source: 'test', id: 'a', facts: [], expectedErrorClass: 'x' }], 'no facts object'],
    [[{ source: 'test', id: 'a', facts: {}, expectedErrorClass: 'x' }, { source: 'test', id: 'a', facts: {}, expectedErrorClass: 'x' }], 'repeats an id'],
    [[{ source: 'test', id: 'a', facts: {}, expectedErrorClass: 'x', path: '/etc' }], 'carries "path"'],
    [[{ source: 'test', id: 'a', facts: {}, expectedDisposition: { outcomeId: 'approve' } }], 'not an object with a kind']
  ])('refuses a declared case the reader does not read (%#)', (cases, why) => {
    expect(() => readCriticCases(cases)).toThrow(why)
  })

  it.each(['vercel', 'codex'] as const)('%s: a run whose declared case the reader refuses ends before any request', async (engine) => {
    const bad = [{ source: 'declared', id: 'a', facts: {} }] as unknown as CriticCase[]
    const outcome = engine === 'vercel' ? await vercelCritic([], { cases: bad }) : await codexCritic([], { cases: bad })
    expect(ofType(outcome.events, 'error')).toEqual([expect.objectContaining({ failure: expect.objectContaining({ code: 'configuration', detail: expect.stringContaining('The declared case "a"') }) })])
    expect('requests' in outcome ? outcome.requests : []).toEqual([])
  })

  it.each(['vercel', 'codex'] as const)('%s: a seeded wrong outcome is refuted, and the critique and the proposal say so', async (engine) => {
    // The runtime answers the critic's rehearsal with decline; the declared case says approve.
    const calls = [{ name: 'experimental_evaluate', args: rehearse(FACTS) }]
    const { events } = engine === 'vercel' ? await vercelCritic(calls, { cases: CASES, outcome: DECLINE }) : await codexCritic(calls, { cases: CASES, outcome: DECLINE })
    const [critique] = ofType(events, 'critique')
    // The critic's own rehearsal compared the first case; the harness rehearsed the second.
    expect(critique).toMatchObject({
      refuted: true,
      checks: [{ tool: 'experimental_evaluate', status: 'evaluated' }, { tool: 'experimental_evaluate', status: 'evaluated' }],
      cases: [
        { case: 'receipted-120', source: 'declared', tool: 'experimental_evaluate', agrees: false, actual: { disposition: DECLINE } },
        { case: 'unreceipted-120', source: 'test', tool: 'experimental_evaluate', agrees: true, actual: { disposition: DECLINE } }
      ],
      notCompared: []
    })
    expect(critique!.text).toContain('case receipted-120 (declared) disagrees')
    expect(ofType(events, 'proposal')[0]!.critique).toEqual({ refuted: true })
    // The critic was handed the cases.
    const agreed = engine === 'vercel' ? await vercelCritic(calls, { cases: CASES.slice(0, 1) }) : await codexCritic(calls, { cases: CASES.slice(0, 1) })
    expect(ofType(agreed.events, 'critique')[0]).toMatchObject({ refuted: false, cases: [{ agrees: true }] })
  })

  it.each(['vercel', 'codex'] as const)('%s: each declared case the critic’s calls left uncompared is rehearsed once, through the gate', async (engine) => {
    // The critic only validates; the harness rehearses both cases, and the
    // runtime gives decline for the receipted claim, which the case says approves.
    const calls = [{ name: 'validate', args: { document: JSON.stringify(DOCUMENT) } }]
    const handle = (call: RuntimeCall) => call.name === 'experimental_evaluate' ? evaluated(DECLINE) : runtimeAnswer({ status: 'valid', diagnostics: [] })
    const { events, seen } = engine === 'vercel' ? await vercelCritic(calls, { cases: CASES, handle }) : await codexCritic(calls, { cases: CASES, handle })
    const rehearsals = seen.filter((call) => call.name === 'experimental_evaluate')
    expect(rehearsals.map((call) => JSON.parse(String(call.args.facts)))).toEqual(CASES.map((item) => item.facts))
    expect(rehearsals.every((call) => call.args.rehearsal === true && call.args.pack === JSON.stringify(DOCUMENT))).toBe(true)
    // Each is a call and a result on the stream, and the gate had nothing to rewrite.
    expect(ofType(events, 'tool_call').filter((event) => event.name === 'experimental_evaluate')).toHaveLength(2)
    expect(ofType(events, 'guardrail')).toEqual([])
    const [critique] = ofType(events, 'critique')
    expect(critique).toMatchObject({
      refuted: true,
      cases: [
        { case: 'receipted-120', agrees: false, actual: { disposition: DECLINE } },
        { case: 'unreceipted-120', agrees: true }
      ],
      notCompared: []
    })
  })

  it.each(['vercel', 'codex'] as const)('%s: at most CRITIC_REHEARSALS of them, the rest named as not compared', async (engine) => {
    const many: CriticCase[] = Array.from({ length: CRITIC_REHEARSALS + 2 }, (_, at) => ({ source: 'test', id: `case-${at}`, facts: { claim: { amount: String(at) } }, expectedDisposition: { kind: 'outcome', outcomeId: 'approve' } }))
    const { seen, events } = engine === 'vercel' ? await vercelCritic([], { cases: many }) : await codexCritic([], { cases: many })
    expect(seen.filter((call) => call.name === 'experimental_evaluate')).toHaveLength(CRITIC_REHEARSALS)
    expect(ofType(events, 'critique')[0]!.notCompared).toEqual(['case-8', 'case-9'])
  })

  it('compares nothing twice: a case the critic’s own rehearsal compared is not rehearsed again', async () => {
    const calls = [{ name: 'experimental_evaluate', args: rehearse(FACTS) }]
    const { seen } = await codexCritic(calls, { cases: CASES })
    expect(seen.filter((call) => call.name === 'experimental_evaluate').map((call) => JSON.parse(String(call.args.facts)))).toEqual([FACTS, CASES[1]!.facts])
  })

  it('rehearses a case’s evidence and extensions as it declares them', () => {
    expect(rehearsalOf({ source: 'test', id: 'a', facts: FACTS, evidenceAvailability: { receipt: 'present' }, supportedExtensions: ['x'], expectedErrorClass: 'malformed-input' }, DOCUMENT)).toEqual({
      pack: JSON.stringify(DOCUMENT), facts: JSON.stringify(FACTS), evidence: JSON.stringify({ receipt: 'present' }), supported_extensions: ['x'], rehearsal: true
    })
  })

  it('vercel: the critic is shown the declared cases in its first message', async () => {
    const { requests } = await vercelCritic([], { cases: CASES })
    expect(JSON.stringify(requests.find(isCritic)!.body)).toContain('unreceipted-120')
    expect(JSON.stringify(requests.find((request) => !isCritic(request))!.body)).not.toContain('unreceipted-120')
  })
})

describe('the contract’s critic vectors are what the harness writes (agent#10)', () => {
  const recorded = (step: string) => runtimeRecording.calls.find((call) => call.step === step)! as { arguments: Record<string, unknown>; result: McpToolResult }
  const scenarioFacts = scenarioFixture.documents.FACTS as Record<string, unknown>
  const draft = scenarioFixture.documents.DRAFT_V2

  it('critic/outcome-refuted.json: the critique of a seeded wrong outcome', () => {
    const recorder = openCritique({
      cases: [
        { source: 'declared', id: 'receipted-120', facts: scenarioFacts, expectedDisposition: { kind: 'outcome', outcomeId: 'decline' } },
        { source: 'test', id: 'small-40', facts: { claim: { type: 'expense-reimbursement', amount: '40.00', receiptAttached: false } }, expectedDisposition: { kind: 'outcome', outcomeId: 'approve' } }
      ],
      document: draft
    })
    for (const step of ['T5', 'T6']) {
      const call = recorded(step)
      recorder.saw(step === 'T5' ? 'validate' : 'experimental_evaluate', call.result.content![0]!.text!, call.arguments, call.result.structuredContent)
    }
    const item = outcomeRefuted.items[0]!
    expect(critiqueEvent({ ...recorder.critique('I could not refute it.'), figures: item.figures as CriticFigures })).toEqual(item)
    expect(outcomeRefuted.items[1]).toMatchObject({ type: 'proposal', critique: critiqueOnProposal(recorder.critique('')).critique })
  })

  it('critic/test-run-compared.json: a test run’s rows of the declared cases', () => {
    const cases = declaredCases.items.slice(0, 2) as CriticCase[]
    const recorder = openCritique({ cases, document: draft })
    const decision = JSON.stringify({ handoff: { state: 'none' }, kind: 'outcome', outcomeId: 'approve', reasons: [] })
    const report = { status: 'failed', packs: [{ rows: [
      { id: 'receipted-120', status: 'passed', expected: decision, actual: decision },
      { id: 'unreceipted-120', status: 'failed', expected: JSON.stringify({ kind: 'outcome', outcomeId: 'decline' }), actual: decision }
    ] }] }
    recorder.saw('experimental_test_cases', JSON.stringify(report), { pack: JSON.stringify(draft), matrix: JSON.stringify(criticMatrix(cases)) }, report)
    const item = testRunCompared.items[0]!
    expect(critiqueEvent({ ...recorder.critique(''), figures: item.figures as CriticFigures })).toEqual(item)
  })

  it('critic/declared-cases.json: cases the reader reads, as the matrix the critic is shown', () => {
    expect(readCriticCases(declaredCases.items)).toEqual(declaredCases.items)
    expect(criticMatrix(declaredCases.items as CriticCase[]).cases.every((row) => !('source' in row))).toBe(true)
  })

  it('tool-offer/critic-tools.json is the allow-list', () => {
    expect(criticTools.items).toEqual([...CRITIC_TOOLS])
  })
})

describe('one default for both engines (agent#10)', () => {
  it('is the policy table’s adversarialReview, false, and the harness reads it from there', () => {
    expect(policyTable.adversarialReview).toBe(false)
    expect(CONTEXT_DEFAULTS.adversarialReview).toBe(false)
  })

  it('vercel: no critic where the session leaves it unset, at any tier; one where it asks', async () => {
    for (const tier of ['off', 'on'] as const) {
      const requests: EndpointRequest[] = []
      const { events } = await vercelTurn({
        answer: (request) => { requests.push(request); return streamed(sseTurn({ text: isCritic(request) ? 'Done.' : PROPOSAL })) },
        session: { testPrompt: 'Test the pack.', thinking: normalize(tier, 'openai-compatible') }
      })
      expect(requests.filter(isCritic), tier).toEqual([])
      expect(ofType(events, 'critique'), tier).toEqual([])
    }
    const asked = await vercelCritic([])
    expect(asked.requests.filter(isCritic).length).toBeGreaterThan(0)
  })

  it('codex: no critic run where the session leaves it unset; one where it asks', async () => {
    const run = vi.fn<AgentRun>(async (_request, callbacks) => {
      await callbacks.event({ type: 'message', id: 'a', text: PROPOSAL, phase: 'final' })
    })
    const events: AssistantEvent[] = []
    await runAssistantSession(codex, { prompt: 'p', testPrompt: 'Test the pack.', tools: [], callTool: vi.fn(), hostTools: [], agent: { model: 'agent-model', run }, signal: new AbortController().signal }, (event) => events.push(event))
    expect(run).toHaveBeenCalledOnce()
    expect(ofType(events, 'critique')).toEqual([])
    const asked = await codexCritic([])
    expect(asked.requests.map((request) => request.phase)).toEqual(['author', 'critic'])
  })
})

describe('the critic’s model, per connection (agent#10)', () => {
  it('vercel: the critic’s requests name the host’s critic model; the loop’s name the run’s', async () => {
    const asked: unknown[] = []
    configureHarness({ criticModel: (run) => { asked.push(run); return 'cheaper-model' } })
    const { requests, events } = await vercelCritic([])
    expect(asked).toEqual([{ engine: 'vercel', family: 'openai-compatible', model: 'scripted-model' }])
    const model = (request: EndpointRequest) => (request.body as { model?: string }).model
    expect(requests.filter((request) => !isCritic(request)).map(model)).toEqual(['scripted-model'])
    expect(requests.filter(isCritic).map(model)).toEqual(['cheaper-model'])
    expect(ofType(events, 'critique')[0]!.figures).toMatchObject({ model: 'cheaper-model' })
  })

  it('vercel: a critic model the host names wrongly is the critic’s note, and the proposal stands', async () => {
    configureHarness({ criticModel: () => 'two\nlines' })
    const { requests, events } = await vercelCritic([])
    expect(requests.filter(isCritic)).toEqual([])
    expect(ofType(events, 'error')).toEqual([])
    expect(ofType(events, 'proposal')[0]).toMatchObject({ document: DOCUMENT, note: { kind: 'critic-unfinished' } })
  })

  it('codex: the critic run names the host’s critic model on the agent wire; the author run the bound one', async () => {
    configureHarness({ criticModel: (run) => (run.engine === 'codex' ? 'cheaper-agent' : null) })
    const { runs, events } = await codexTurn({
      script: async (bridge) => {
        bridge.started()
        bridge.say(bridge.request.phase === 'critic' ? 'Done.' : PROPOSAL)
        bridge.end()
      },
      session: { testPrompt: 'Test the pack.', adversarialReview: true }
    })
    expect(runs.map((request) => [request.phase, request.model])).toEqual([['author', 'scripted-agent'], ['critic', 'cheaper-agent']])
    expect(ofType(events, 'critique')[0]!.figures).toEqual({ model: 'cheaper-agent', unit: 'agent-runs', requests: 1 })
  })

  it('codex: the agent transport sends no model the host did not name', async () => {
    configureHarness({ criticModel: () => 'cheaper-agent' })
    const run = bindAgentRun('bound-agent')
    const callbacks = { signal: new AbortController().signal, event: async () => {}, tool: async () => ({ text: '', isError: false }) }
    const request = { prompt: 'p', instructions: 'i', tools: [] }
    await expect(run({ ...request, phase: 'critic', model: 'another-agent' }, callbacks)).rejects.toMatchObject({ failure: { code: 'configuration', source: 'agent', outcome: 'not-started' } })
    await expect(run({ ...request, phase: 'author', model: 'cheaper-agent' }, callbacks)).rejects.toMatchObject({ failure: { code: 'configuration' } })
  })
})

describe('what the critic cost, in the run’s figures (agent#10)', () => {
  it('vercel: the critique carries the critic’s model requests and its token counts', async () => {
    // Every answer whole, each reporting one input and one output token.
    let critic = 0
    const { events } = await vercelCritic([], {
      answer: (request) => {
        if (!isCritic(request)) return whole(wholeTurn({ text: PROPOSAL }))
        critic += 1
        return whole(critic === 1 ? wholeTurn({ tool: { name: 'validate', args: { document: '{}' } } }) : wholeTurn({ text: 'Done.' }))
      }
    })
    expect(critic).toBe(2)
    expect(ofType(events, 'critique')[0]!.figures).toEqual({ model: 'scripted-model', unit: 'model-requests', requests: 2, inputTokens: 2, outputTokens: 2, reasoningTokens: 0, cacheReadTokens: 0 })
    // The loop's request is not the critic's.
    const checkpoint = ofType(events, 'execution_checkpoint').at(-1)!.checkpoint
    expect(checkpoint.used).toBe(3)
    expect(checkpoint.completions!.filter((row) => row.phase === 'review')).toHaveLength(2)
  })

  it('vercel: a critic that did not finish says what it cost on its note', async () => {
    const { events } = await vercelCritic([], {
      answer: (request) => isCritic(request)
        ? new Response(JSON.stringify({ error: { message: 'busy' } }), { status: 503, headers: { 'content-type': 'application/json' } })
        : streamed(sseTurn({ text: PROPOSAL }))
    })
    expect(ofType(events, 'proposal')[0]).toMatchObject({ note: { kind: 'critic-unfinished', figures: { model: 'scripted-model', unit: 'model-requests', requests: 1 } } })
  })

  it('codex: the critique carries one agent run on the run’s model', async () => {
    const { events } = await codexCritic([])
    expect(ofType(events, 'critique')[0]!.figures).toEqual({ model: 'agent-model', unit: 'agent-runs', requests: 1 })
  })

  it('codex: a critic run that failed says what it cost on its note', async () => {
    const run: AgentRun = async (request, callbacks) => {
      if (request.phase === 'critic') throw new Error('the bridge stopped')
      await callbacks.event({ type: 'message', id: 'a', text: PROPOSAL, phase: 'final' })
    }
    const events: AssistantEvent[] = []
    await runAssistantSession(codex, { prompt: 'p', testPrompt: 'Test.', tools: [], callTool: vi.fn(), hostTools: [], adversarialReview: true, agent: { model: 'agent-model', run }, signal: new AbortController().signal }, (event) => events.push(event))
    expect(ofType(events, 'proposal')[0]).toMatchObject({ note: { kind: 'critic-unfinished', figures: { model: 'agent-model', unit: 'agent-runs', requests: 1 } } })
  })
})
