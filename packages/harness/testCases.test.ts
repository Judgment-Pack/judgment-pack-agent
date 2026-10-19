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
import { bindModelCall, runAssistantSession } from './session'
import { normalize } from './thinking'
import { vercel } from './engines/vercel'
import {
  TEST_CASES_TOOL,
  TEST_CONFORMANCE_TOOL,
  gatedRuntime,
  runtimeAnswer,
  scriptedEndpoint,
  sseTurn,
  streamed,
  toolTexts,
  type RuntimeCall
} from './testing/scripted'
import type { JSONRPCMessage } from '@modelcontextprotocol/sdk/types.js'
import type { Transport } from '@modelcontextprotocol/sdk/shared/transport.js'
import type { AssistantEvent } from './engine'

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
  script: (number: number, texts: string[]) => string
  handle?: (call: RuntimeCall) => unknown
  allowed?: readonly string[]
}) {
  const events: AssistantEvent[] = []
  const endpoint = scriptedEndpoint((request) => streamed(options.script(request.number, toolTexts(request))))
  vi.stubGlobal('fetch', endpoint.fetch)
  const runtime = await gatedRuntime({
    tools: [TEST_CASES_TOOL, TEST_CONFORMANCE_TOOL],
    allowed: options.allowed ?? [...ASSISTANT_TOOLS],
    handle: options.handle ?? (() => runtimeAnswer({ status: 'passed', cases: 1 })),
    onEvent: (event) => events.push(event)
  })
  try {
    await runAssistantSession(
      vercel,
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
        context: { currentRequest: { message: 'Test the draft.', input: PACK } }
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
      expect(capped).toMatchObject({ isError: true, structured: { failure: { code: 'request-limit', source: 'tool', outcome: 'not-started' } } })
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
  it('is retained and paged through read_context, never dropped, while the host sees it whole', async () => {
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
          return sseTurn({ tool: { name: 'read_context', args: { id: referenceId, path: '/structuredContent/failures/399' } } })
        }
        return sseTurn({ text: 'Case 399 fails.' })
      }
    })
    const shown = toolTexts(requests[1]!).at(-1)!
    expect(new TextEncoder().encode(shown).length).toBeLessThan(CONTEXT_DEFAULTS.toolResultBytes)
    expect(shown).toContain('"readWith":"read_context"')
    expect(shown).toContain('"operationReturned":true')
    expect(shown).not.toContain('case-399')
    expect(referenceId).not.toBe('')
    // The section came back on request.
    const page = toolTexts(requests[2]!).at(-1)!
    expect(page).toContain('case-399')
    // The host's event carries the whole report.
    const result = ofType(events, 'tool_result').find((event) => event.name === TEST_TOOL)!
    expect(result.structured).toEqual(report)
    expect(ofType(events, 'error')).toEqual([])
  })
})
