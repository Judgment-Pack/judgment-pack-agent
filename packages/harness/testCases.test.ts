/**
 * The Runtime's test tool on the ceiling (agent#7, as corrected): only
 * `experimental_test_cases` is admitted, because it takes pack text and a
 * matrix and reads no project file; it leaves the page only with exactly the
 * two members the Runtime's schema names, each within the policy's bytes; one
 * turn may call it `testCallsPerTurn` times; and a report longer than
 * `toolResultBytes` is paged through the ledger, not dropped.
 *
 * Every case runs the real tool gate on an in-memory transport, and the ones
 * that run a turn drive the `vercel` engine with a scripted endpoint.
 */
import { afterEach, describe, expect, it, vi } from 'vitest'
import contractCall from '../../contract/schemas/tool-call.json'
import { ASSISTANT_TOOLS } from './host'
import {
  GateViolation,
  REFUSED_RUNTIME_TOOLS,
  TEST_ARGUMENTS,
  TEST_TOOL,
  allowedTools,
  gateTransport,
  inspectOutboundFrame,
  type GuardrailNotice
} from './toolGate'
import { CONTEXT_DEFAULTS } from './context/policy'
import { READ_TOOL } from './context/reader'
import { bindModelCall, runAssistantSession } from './session'
import { normalize } from './thinking'
import { vercel } from './engines/vercel'
import { withDelegation } from './delegation'
import { DELEGATE_TOOL } from './delegationContract'
import type { ExecutionCheckpoint } from './execution/state'
import {
  TEST_CASES_TOOL,
  TEST_CONFORMANCE_TOOL,
  gatedRuntime,
  runtimeAnswer,
  scriptedEndpoint,
  sseTurn,
  streamed,
  toolTexts,
  type EndpointRequest,
  type RuntimeCall
} from './testing/scripted'
import type { JSONRPCMessage } from '@modelcontextprotocol/sdk/types.js'
import type { Transport } from '@modelcontextprotocol/sdk/shared/transport.js'
import type { AssistantEvent, AssistantSession, Engine } from './engine'

const PACK = JSON.stringify({ specVersion: '0.4', id: 'expense', title: 'Expense' })
const MATRIX = JSON.stringify({ matrixVersion: '3', cases: [{ id: 'c1', facts: {}, expectedDisposition: { kind: 'decided' } }] })

function call(name: string, args: Record<string, unknown>): JSONRPCMessage {
  return { jsonrpc: '2.0', id: 1, method: 'tools/call', params: { name, arguments: args } } as unknown as JSONRPCMessage
}

/** The gate over a transport that keeps what reached the wire. */
function gated(options: { allowed?: readonly string[]; bounds?: { testPackBytes: number; testMatrixBytes: number } } = {}) {
  const wire: Record<string, unknown>[] = []
  const notices: GuardrailNotice[] = []
  const transport: Transport = {
    async start() {},
    async close() {},
    async send(message: JSONRPCMessage) {
      wire.push(JSON.parse(JSON.stringify(message)) as Record<string, unknown>)
    }
  }
  gateTransport(transport, {
    allowed: allowedTools(options.allowed ?? [...ASSISTANT_TOOLS]),
    onGuardrail: (notice) => notices.push(notice),
    ...(options.bounds === undefined ? {} : { bounds: options.bounds })
  })
  return { transport, wire, notices }
}

const ofType = <T extends AssistantEvent['type']>(events: AssistantEvent[], type: T) =>
  events.filter((event): event is Extract<AssistantEvent, { type: T }> => event.type === type)

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('the ceiling', () => {
  it('is the Desk’s five and experimental_test_cases, and nothing else', () => {
    expect([...ASSISTANT_TOOLS]).toEqual([
      'get_schema',
      'list_examples',
      'get_example',
      'validate',
      'experimental_evaluate',
      'experimental_test_cases'
    ])
    expect(TEST_TOOL).toBe('experimental_test_cases')
  })

  it.each(Object.keys(REFUSED_RUNTIME_TOOLS))('refuses %s on the wire, even where a file grants it', async (name) => {
    const { transport, wire, notices } = gated({ allowed: [...ASSISTANT_TOOLS, name] })
    const args = name === 'test_conformance' ? { suite: '../../private/manifest.json' } : {}
    await expect(transport.send(call(name, args))).rejects.toThrow(GateViolation)
    expect(wire).toEqual([])
    expect(notices).toEqual([expect.objectContaining({ tool: name, action: 'refused' })])
    // Its reason is written down where the ceiling is applied.
    expect(REFUSED_RUNTIME_TOOLS[name as keyof typeof REFUSED_RUNTIME_TOOLS].length).toBeGreaterThan(40)
  })
})

describe('the test tool’s arguments, on the wire', () => {
  it('are the members the Runtime’s schema names, as the contract states them', () => {
    const served = TEST_CASES_TOOL.inputSchema as { properties: Record<string, unknown>; required: string[]; additionalProperties: boolean }
    expect(Object.keys(served.properties).sort()).toEqual([...TEST_ARGUMENTS].sort())
    expect([...served.required].sort()).toEqual([...TEST_ARGUMENTS].sort())
    expect(served.additionalProperties).toBe(false)
    const stated = contractCall.$defs.testCasesArguments
    expect(Object.keys(stated.properties).sort()).toEqual([...TEST_ARGUMENTS].sort())
    expect([...stated.required].sort()).toEqual([...TEST_ARGUMENTS].sort())
    expect(stated.additionalProperties).toBe(false)
  })

  it('lets exactly pack and matrix leave, unchanged and with no notice', async () => {
    const { transport, wire, notices } = gated()
    await transport.send(call(TEST_TOOL, { pack: PACK, matrix: MATRIX }))
    expect(wire).toEqual([{ jsonrpc: '2.0', id: 1, method: 'tools/call', params: { name: TEST_TOOL, arguments: { pack: PACK, matrix: MATRIX } } }])
    expect(notices).toEqual([])
  })

  it.each([
    ['a path-like suite', { suite: '../../.ssh/id_ed25519' }],
    ['a path', { path: '/srv/project/packs/expense.json' }],
    ['a project directory', { project: '.' }],
    ['a member named like the pack', { Pack: PACK }]
  ])('refuses a call that also carries %s, and nothing leaves', async (_label, extra) => {
    const { transport, wire, notices } = gated()
    const refused = await transport.send(call(TEST_TOOL, { pack: PACK, matrix: MATRIX, ...extra })).catch((error: unknown) => error)
    expect(refused).toBeInstanceOf(GateViolation)
    expect((refused as GateViolation).failure).toMatchObject({ code: 'permission', source: 'tool', outcome: 'not-started' })
    expect(wire).toEqual([])
    expect(notices).toHaveLength(1)
    expect(notices[0]!.detail).toContain(`takes exactly pack and matrix, and this call also carried ${JSON.stringify(Object.keys(extra)[0])}`)
    // The notice names members, never what the pack says.
    expect(notices[0]!.detail).not.toContain('expense')
  })

  it.each([
    ['no matrix', { pack: PACK }, 'matrix is missing'],
    ['a matrix that is an object', { pack: PACK, matrix: { cases: [] } }, 'matrix is not a string'],
    ['no arguments at all', undefined, 'pack is missing']
  ])('refuses a call with %s', async (_label, args, said) => {
    const { transport, wire, notices } = gated()
    const frame = { jsonrpc: '2.0', id: 1, method: 'tools/call', params: { name: TEST_TOOL, ...(args === undefined ? {} : { arguments: args }) } } as unknown as JSONRPCMessage
    await expect(transport.send(frame)).rejects.toThrow(GateViolation)
    expect(wire).toEqual([])
    expect(notices[0]!.detail).toContain(said)
  })

  it('holds each member to the policy’s bytes, counted in UTF-8', async () => {
    const bounds = { testPackBytes: 64, testMatrixBytes: 32 }
    const { transport, wire, notices } = gated({ bounds })
    // 32 bytes of matrix in 16 characters: the bound is bytes.
    await expect(transport.send(call(TEST_TOOL, { pack: 'p', matrix: 'é'.repeat(16) }))).resolves.toBeUndefined()
    await expect(transport.send(call(TEST_TOOL, { pack: 'p', matrix: 'é'.repeat(17) }))).rejects.toThrow(GateViolation)
    await expect(transport.send(call(TEST_TOOL, { pack: 'x'.repeat(65), matrix: '{}' }))).rejects.toThrow(GateViolation)
    expect(wire).toHaveLength(1)
    expect(notices.map((notice) => notice.detail)).toEqual([
      expect.stringContaining('matrix is 34 bytes, over the 32 the policy allows (testMatrixBytes)'),
      expect.stringContaining('pack is 65 bytes, over the 64 the policy allows (testPackBytes)')
    ])
  })

  it('takes its default bounds from the policy', () => {
    expect(CONTEXT_DEFAULTS).toMatchObject({ testCallsPerTurn: 3, testPackBytes: 65_536, testMatrixBytes: 65_536 })
    const over = inspectOutboundFrame(call(TEST_TOOL, { pack: 'x'.repeat(65_537), matrix: '{}' }), allowedTools([TEST_TOOL]))
    expect(over.verdict).toBe('refused')
    const within = inspectOutboundFrame(call(TEST_TOOL, { pack: 'x'.repeat(65_536), matrix: '{}' }), allowedTools([TEST_TOOL]))
    expect(within.verdict).toBe('send')
  })
})

/**
 * One turn on the scripted endpoint, the runtime serving the six and
 * test_conformance behind the real gate. `script` answers request n.
 */
async function runTurn(options: {
  script: (number: number, texts: string[], request: EndpointRequest) => string | Response
  handle?: (call: RuntimeCall) => unknown
  allowed?: readonly string[]
  /** The engine the turn runs on; `vercel` where absent. */
  engine?: Engine
  session?: Partial<AssistantSession>
}) {
  const events: AssistantEvent[] = []
  const endpoint = scriptedEndpoint((request) => {
    const answer = options.script(request.number, toolTexts(request), request)
    return typeof answer === 'string' ? streamed(answer) : answer
  })
  vi.stubGlobal('fetch', endpoint.fetch)
  const runtime = await gatedRuntime({
    tools: [TEST_CASES_TOOL, TEST_CONFORMANCE_TOOL],
    allowed: options.allowed ?? [...ASSISTANT_TOOLS],
    handle: options.handle ?? (() => runtimeAnswer({ status: 'passed', cases: 1 })),
    onEvent: (event) => events.push(event)
  })
  try {
    await runAssistantSession(
      options.engine ?? vercel,
      {
        prompt: 'Test the draft, fix what fails, and test it again.',
        testPrompt: '',
        tools: runtime.tools,
        callTool: runtime.callTool,
        hostTools: [],
        allowConversation: true,
        model: { family: 'openai-compatible', model: 'scripted-model', call: bindModelCall('openai-compatible') },
        thinking: normalize('off', 'openai-compatible'),
        signal: new AbortController().signal,
        context: { currentRequest: { message: 'Test the draft.', input: PACK } },
        ...options.session
      },
      (event) => events.push(event)
    )
  } finally {
    await runtime.close()
  }
  return { events, requests: endpoint.requests, seen: runtime.seen }
}

describe('the per-turn cap on test calls', () => {
  it('stops a scripted model that calls the test tool on every step after testCallsPerTurn calls', async () => {
    // The endpoint answers the first eight requests with a test call on a pack
    // it changed each time (so the repeated-work detector, which compares
    // arguments, never fires), then with prose.
    const { events, requests, seen } = await runTurn({
      script: (number) =>
        number <= 8
          ? sseTurn({ tool: { name: TEST_TOOL, args: { pack: PACK.replace('Expense', `Expense v${number}`), matrix: MATRIX }, id: `call_${number}` } })
          : sseTurn({ text: 'The draft passes its cases.' })
    })
    // Three reached the runtime; the other five were answered by the harness.
    expect(seen.map((call) => call.name)).toEqual([TEST_TOOL, TEST_TOOL, TEST_TOOL])
    const results = ofType(events, 'tool_result').filter((result) => result.name === TEST_TOOL)
    expect(results).toHaveLength(8)
    expect(results.slice(0, 3).every((result) => !result.isError)).toBe(true)
    for (const capped of results.slice(3)) {
      expect(capped).toMatchObject({ isError: true, structured: { failure: { code: 'tool-failed', source: 'tool', outcome: 'not-started' } } })
      expect(capped.text).toContain('called experimental_test_cases 3 times, the most one turn may (testCallsPerTurn)')
    }
    // The model read the refusal: the request after the fourth call carries it.
    expect(toolTexts(requests[4]!).at(-1)).toContain('the most one turn may')
    // The refused calls never reached the gate, so no guardrail was needed.
    expect(ofType(events, 'guardrail')).toEqual([])
    expect(ofType(events, 'error')).toEqual([])
    expect(ofType(events, 'message').at(-1)?.text).toBe('The draft passes its cases.')
    expect(ofType(events, 'end')).toHaveLength(1)
  })

  /**
   * A delegation is one turn of the person's: the parent pauses, the children
   * run, the parent continues as a new run. The count carries across, through
   * the checkpoint where the run keeps one and through the coordinator's own
   * count where it does not.
   */
  it.each([
    ['with its checkpoint', { context: { currentRequest: { message: 'Test the draft.', input: PACK } } }],
    ['with no checkpoint, the coordinator’s count alone', { context: undefined }]
  ])('carries the count across a delegation (%s): the parent’s three calls and the continuation’s fourth are one turn', async (_how, session) => {
    const roleOf = (request: EndpointRequest) => {
      const said = JSON.stringify(request.body.messages)
      return said.includes('PARENT CONTINUATION') ? 'continuation' : said.includes('Complete this read-only task') ? 'child' : 'parent'
    }
    const { events, requests, seen } = await runTurn({
      engine: withDelegation(vercel),
      session: { interactive: true, ...session },
      script: (_number, texts, request) => {
        const role = roleOf(request)
        if (role === 'parent') {
          if (texts.length < 3) return sseTurn({ tool: { name: TEST_TOOL, args: { pack: PACK.replace('Expense', `Expense p${texts.length}`), matrix: MATRIX }, id: `call_p${texts.length}` } })
          return sseTurn({ tool: { name: DELEGATE_TOOL, args: { tasks: [{ title: 'Read the cases', objective: 'Say which case is weakest.', context: MATRIX, profile: 'analyze' }] }, id: 'call_delegate' } })
        }
        if (role === 'child') return sseTurn({ text: 'Case c1 is the weakest.' })
        if (texts.length === 0) return sseTurn({ tool: { name: TEST_TOOL, args: { pack: PACK.replace('Expense', 'Expense c1'), matrix: MATRIX }, id: 'call_c0' } })
        return sseTurn({ text: 'The draft passes its cases.' })
      }
    })
    // The child ran, and the continuation's call was the turn's fourth.
    expect(ofType(events, 'delegated_task').at(-1)?.task).toMatchObject({ state: 'complete' })
    expect(seen.map((call) => call.name)).toEqual([TEST_TOOL, TEST_TOOL, TEST_TOOL])
    const fourth = ofType(events, 'tool_result').filter((result) => result.name === TEST_TOOL).at(-1)!
    expect(fourth).toMatchObject({ isError: true, structured: { failure: { code: 'tool-failed', source: 'tool', outcome: 'not-started' } } })
    expect(fourth.text).toContain('the most one turn may (testCallsPerTurn)')
    // The continuation's model read it.
    const after = requests.filter((request) => roleOf(request) === 'continuation').at(-1)!
    expect(toolTexts(after).at(-1)).toContain('the most one turn may (testCallsPerTurn)')
    expect(ofType(events, 'error')).toEqual([])
  })

  it('carries the count through Retry: a run resumed from its checkpoint goes on counting', async () => {
    // Three test calls, then the endpoint answers 503: the run fails with its checkpoint.
    const failed = await runTurn({
      script: (number) =>
        number <= 3
          ? sseTurn({ tool: { name: TEST_TOOL, args: { pack: PACK.replace('Expense', `Expense r${number}`), matrix: MATRIX }, id: `call_r${number}` } })
          : new Response(JSON.stringify({ error: { message: 'unavailable' } }), { status: 503, headers: { 'content-type': 'application/json' } })
    })
    expect(failed.seen).toHaveLength(3)
    const saved = ofType(failed.events, 'execution_checkpoint').at(-1)!.checkpoint as ExecutionCheckpoint
    expect(saved.testCalls).toBe(3)
    // Retry: the same request from that checkpoint.
    const retried = await runTurn({
      session: { context: { currentRequest: { message: 'Test the draft.', input: PACK }, execution: { ...structuredClone(saved), status: 'running' } } },
      script: (number) =>
        number === 1
          ? sseTurn({ tool: { name: TEST_TOOL, args: { pack: PACK.replace('Expense', 'Expense r4'), matrix: MATRIX }, id: 'call_r4' } })
          : sseTurn({ text: 'The reports above stand.' })
    })
    expect(retried.seen).toEqual([])
    expect(ofType(retried.events, 'tool_result').at(-1)).toMatchObject({ name: TEST_TOOL, isError: true, structured: { failure: { code: 'tool-failed' } } })
    expect(ofType(retried.events, 'error')).toEqual([])
  })

  it('counts the turn, not the session: a new turn may test again', async () => {
    for (let turn = 0; turn < 2; turn++) {
      const { seen } = await runTurn({
        script: (number) =>
          number <= 4
            ? sseTurn({ tool: { name: TEST_TOOL, args: { pack: PACK.replace('Expense', `Expense ${turn}.${number}`), matrix: MATRIX } } })
            : sseTurn({ text: 'Done.' })
      })
      expect(seen).toHaveLength(3)
    }
  })
})

describe('a test report longer than toolResultBytes', () => {
  it('is retained and paged through the ledger’s readers, never dropped, while the host sees it whole', async () => {
    const failures = Array.from({ length: 400 }, (_, index) => ({ caseId: `case-${index}`, expected: 'decided', actual: 'escalated', note: 'mismatch '.repeat(4) }))
    const report = { status: 'failed', cases: 400, failures }
    let referenceId = ''
    const { events, requests } = await runTurn({
      handle: () => runtimeAnswer(report),
      script: (number, texts) => {
        if (number === 1) return sseTurn({ tool: { name: TEST_TOOL, args: { pack: PACK, matrix: MATRIX } } })
        if (number === 2) {
          // What the model was shown: a reference envelope inside the data
          // block, not the report.
          const shown = texts.at(-1)!
          referenceId = /"contextId":"([^"]+)"/.exec(shown)?.[1] ?? ''
          // The reader the envelope names, from the start, one window.
          return sseTurn({ tool: { name: READ_TOOL, args: { id: referenceId } } })
        }
        if (number === 3) {
          // One section by its pointer.
          return sseTurn({ tool: { name: 'read_context', args: { id: referenceId, path: '/structuredContent/failures/399' } } })
        }
        return sseTurn({ text: 'Case 399 fails.' })
      }
    })
    const shown = toolTexts(requests[1]!).at(-1)!
    expect(new TextEncoder().encode(shown).length).toBeLessThan(CONTEXT_DEFAULTS.toolResultBytes)
    expect(shown).toContain(`"readWith":"${READ_TOOL}"`)
    expect(shown).toContain('"operationReturned":true')
    expect(shown).not.toContain('case-399')
    expect(referenceId).not.toBe('')
    // A window of it came back, with the cursor that reads on, and then the section asked for.
    const window = toolTexts(requests[2]!).at(-1)!
    expect(new TextEncoder().encode(window).length).toBeLessThan(CONTEXT_DEFAULTS.toolResultBytes + 1024)
    expect(window).toContain('case-0')
    expect(window).toMatch(/\\?"more\\?":\\?"line:/)
    const section = toolTexts(requests[3]!).at(-1)!
    expect(section).toContain('case-399')
    // The host's event carries the whole report.
    const result = ofType(events, 'tool_result').find((event) => event.name === TEST_TOOL)!
    expect(result.structured).toEqual(report)
    expect(ofType(events, 'error')).toEqual([])
  })
})
