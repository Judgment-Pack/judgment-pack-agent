/**
 * The capability record (agent#4), held to the contract: the three families'
 * defaults are the contract's vectors, the reader is closed as the schema is,
 * a run's record is the family's, then the host's, then the session's, and the
 * contract's open-weight record runs the recorded scenario.
 */
import { afterEach, describe, expect, it, vi } from 'vitest'
import capabilitySchema from '../../contract/schemas/capability.json'
import anthropicVector from '../../contract/vectors/capability/anthropic.json'
import geminiVector from '../../contract/vectors/capability/gemini.json'
import openaiVector from '../../contract/vectors/capability/openai-compatible.json'
import openWeightVector from '../../contract/vectors/capability/open-weight-local.json'
import { CAPABILITY_MEMBERS, FAMILY_CAPABILITIES, WIRE_REASONING, capabilitiesFor, readCapabilities, type ModelCapabilities } from './capabilities'
import { configureHarness } from './host'
import { vercel } from './engines/vercel/index'
import { scenario, scriptedModel, scriptedRuntime } from './certification'
import { bindModelCall, openAssistantConnection, runAssistantSession } from './session'
import { normalize } from './thinking'
import type { AssistantEvent, ModelCall } from './engine'

afterEach(() => vi.unstubAllGlobals())

describe('the three families’ default records', () => {
  it('are the contract’s vectors, member for member', () => {
    expect(FAMILY_CAPABILITIES['openai-compatible']).toEqual(openaiVector.items[0])
    expect(FAMILY_CAPABILITIES.anthropic).toEqual(anthropicVector.items[0])
    expect(FAMILY_CAPABILITIES.gemini).toEqual(geminiVector.items[0])
  })

  it('each speak their own wire’s reasoning', () => {
    for (const [family, record] of Object.entries(FAMILY_CAPABILITIES)) {
      expect(WIRE_REASONING[family as keyof typeof WIRE_REASONING]).toContain(record.reasoning.kind)
    }
  })
})

describe('the record, read', () => {
  it('names exactly the members the contract states', () => {
    expect([...CAPABILITY_MEMBERS].sort()).toEqual(Object.keys(capabilitySchema.properties).sort())
    expect(Object.keys(capabilitySchema.$defs.endpointCapabilities.properties).sort()).toEqual([...CAPABILITY_MEMBERS].sort())
  })

  it('takes every record the contract carries', () => {
    for (const vector of [openaiVector, anthropicVector, geminiVector, openWeightVector]) {
      expect(readCapabilities(structuredClone(vector.items[0]))).toEqual(vector.items[0])
    }
  })

  it('is closed, and holds the window to the budget table’s bounds', () => {
    const base = structuredClone(FAMILY_CAPABILITIES['openai-compatible']) as ModelCapabilities
    const refused = (change: Record<string, unknown>) => () => readCapabilities({ ...base, ...change })
    expect(refused({ supportsTools: true })).toThrow(/names "supportsTools"/)
    expect(refused({ contextWindowTokens: 8192 })).toThrow(/come together/)
    expect(refused({ contextWindowTokens: 8192, outputTokens: 4097 })).toThrow(/half the window/)
    expect(refused({ contextWindowTokens: 2048, outputTokens: 1024 })).toThrow(/4096 to 2000000/)
    expect(refused({ reasoning: { kind: 'chat-template' } })).toThrow(/reasoning.kind must be one of/)
    expect(refused({ reasoning: { kind: 'none', budget: 1 } })).toThrow(/reasoning names "budget"/)
    expect(refused({ streamingQuirks: { reasoningField: 'thoughts', wholeJsonAnswer: false } })).toThrow(/reasoningField/)
    expect(refused({ toolSchemas: 'openapi' })).toThrow(/toolSchemas/)
    expect(refused({ statePlacement: 'leading-user' })).toThrow(/statePlacement/)
    expect(refused({ vision: 'yes' })).toThrow(/vision must be true or false/)
    // agent#17: the connection's capacity is not the record's: it is the connection's answer (modelsFor).
    expect(refused({ concurrentRequests: 3 })).toThrow(/concurrentRequests/)
    expect(() => readCapabilities(null)).toThrow(/not an object/)
  })
})

describe('the record a run reads', () => {
  const model = { family: 'openai-compatible' as const, model: 'a-model' }

  it('is the family’s default where the host knows nothing', () => {
    expect(capabilitiesFor(model)).toEqual(FAMILY_CAPABILITIES['openai-compatible'])
  })

  it('takes the host’s answer member by member over it, and the session’s over that', () => {
    const asked: unknown[] = []
    configureHarness({ endpointCapabilities: (endpoint) => { asked.push(endpoint); return { vision: true, strictAlternation: true } } })
    expect(capabilitiesFor(model)).toEqual({ ...FAMILY_CAPABILITIES['openai-compatible'], vision: true, strictAlternation: true })
    expect(asked).toEqual([model])
    expect(capabilitiesFor({ ...model, capabilities: { vision: false, nativeToolCalls: false } })).toEqual({
      ...FAMILY_CAPABILITIES['openai-compatible'], vision: false, strictAlternation: true, nativeToolCalls: false
    })
  })

  it('is refused where its reasoning is not spoken on the session’s wire, or the host’s answer is not an object', () => {
    configureHarness({ endpointCapabilities: () => ({ reasoning: { kind: 'gemini-thinking' } }) })
    expect(() => capabilitiesFor(model)).toThrow(/not spoken on the openai-compatible wire/)
    configureHarness({ endpointCapabilities: () => null as never })
    expect(() => capabilitiesFor(model)).toThrow(/other than an object/)
  })
})

describe('the contract’s open-weight record', () => {
  it('runs the recorded scenario: tools described and called in text, no reasoning parameter, its window', async () => {
    const record = openWeightVector.items[0] as ModelCapabilities
    configureHarness({ endpointCapabilities: () => record })
    const model = scriptedModel({ api: 'openai-compatible', answerAs: 'stream', thinking: 'off', refuted: false, signatures: 'thought', toolCalls: 'text' })
    vi.stubGlobal('fetch', model.fetch)
    const runtime = await scriptedRuntime()
    const events: AssistantEvent[] = []
    const bodies: Record<string, unknown>[] = []
    const connection = openAssistantConnection({ allowed: scenario.scenarioTools, onEvent: (event) => events.push(event), transport: runtime.transport })
    try {
      const ready = await connection.ready
      const bound = bindModelCall('openai-compatible')
      const call: ModelCall = async (suffix, request) => { bodies.push(JSON.parse(request.body) as Record<string, unknown>); return bound(suffix, request) }
      await runAssistantSession(vercel, {
        prompt: scenario.policy, testPrompt: '', tools: ready.tools, callTool: ready.callTool, hostTools: [],
        model: { family: 'openai-compatible', model: 'scripted-model', call },
        thinking: normalize('on', 'openai-compatible'), adversarialReview: false, signal: new AbortController().signal
      }, (event) => events.push(event))
    } finally {
      await connection.close()
      await runtime.close()
    }
    expect(events.filter((event) => event.type === 'error')).toEqual([])
    expect(events.find((event) => event.type === 'proposal')).toMatchObject({ document: scenario.documents.DRAFT_V2 })
    expect(model.requests.map((request) => request.step)).toEqual(['T1', 'T2', 'T3', 'T4', 'T5', 'T6', 'T7', 'T8'])
    for (const body of bodies) {
      expect(body).not.toHaveProperty('tools')
      expect(body).not.toHaveProperty('reasoning_effort')
      expect(body).not.toHaveProperty('parallel_tool_calls')
      expect(body.max_tokens).toBe(4096)
    }
    expect(events.filter((event) => event.type === 'thinking_unavailable').map((event) => (event as { detail: string }).detail)).toEqual([
      'thinking is unavailable for this endpoint: its capability record says the endpoint ignores the reasoning parameter'
    ])
    expect(runtime.seen.find((call) => call.name === 'experimental_evaluate')!.args.rehearsal).toBe(true)
    expect(runtime.seen.map((call) => call.name)).not.toContain('write_file')
  })
})
