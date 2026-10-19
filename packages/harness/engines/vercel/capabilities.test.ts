/**
 * The capability record drives the loop (agent#4): each member, the decision it
 * names, on the wire. The family stays the wire; the record says what the
 * endpoint behind it does.
 */
import { describe, expect, it } from 'vitest'
import { vercel } from './index'
import { FAMILY_CAPABILITIES, type ModelCapabilities } from '../../capabilities'
import { normalize } from '../../thinking'
import type { EndpointKind, ThinkingTier } from '../../host'
import type { AssistantEvent, AssistantSession, HostTool, McpTool, McpToolResult, ModelCall } from '../../engine'

const SERVED = {
  $schema: 'https://json-schema.org/draft/2020-12/schema',
  type: 'object',
  properties: { pack: { type: 'object' }, mode: { const: 'strict' } },
  additionalProperties: false
}
const TOOLS: McpTool[] = [{ name: 'validate', description: 'check a document', inputSchema: SERVED }]
const PROPOSAL = 'Here it is.\n\n```json\n' + JSON.stringify({ proposal: { kind: 'create', document: { id: 'p' }, unknowns: [] } }) + '\n```\n'
const textCall = (tool: string, args: unknown = {}) => '```json\n' + JSON.stringify({ tool, arguments: args }) + '\n```'

type Delta = Record<string, unknown>
/** One OpenAI-compatible streamed turn, from its deltas. */
function streamed(deltas: Delta[], finish = 'stop'): string {
  const frame = (choices: unknown[]) => `data: ${JSON.stringify({ id: 'c', object: 'chat.completion.chunk', model: 'm', choices })}\n\n`
  return [frame([{ index: 0, delta: { role: 'assistant', content: null }, finish_reason: null }]),
    ...deltas.map((delta) => frame([{ index: 0, delta, finish_reason: null }])),
    frame([{ index: 0, delta: {}, finish_reason: finish }]), 'data: [DONE]\n\n'].join('')
}
const text = (content: string, extra: Delta = {}) => streamed([{ ...extra, content }])
const native = (name: string, args: unknown = {}) => streamed([{ tool_calls: [{ index: 0, id: `call_${name}`, type: 'function', function: { name, arguments: JSON.stringify(args) } }] }], 'tool_calls')

/** One Anthropic streamed turn: a tool call or text. */
function anthropic(step: { text?: string; tool?: string }): string {
  const lines: string[] = []
  const event = (name: string, object: Record<string, unknown>) => lines.push(`event: ${name}\ndata: ${JSON.stringify({ type: name, ...object })}\n\n`)
  event('message_start', { message: { id: 'm', type: 'message', role: 'assistant', model: 'm', content: [], stop_reason: null, usage: { input_tokens: 1, output_tokens: 0 } } })
  if (step.tool) {
    event('content_block_start', { index: 0, content_block: { type: 'tool_use', id: `toolu_${step.tool}`, name: step.tool, input: {} } })
    event('content_block_delta', { index: 0, delta: { type: 'input_json_delta', partial_json: '{}' } })
  } else {
    event('content_block_start', { index: 0, content_block: { type: 'text', text: '' } })
    event('content_block_delta', { index: 0, delta: { type: 'text_delta', text: step.text ?? '' } })
  }
  event('content_block_stop', { index: 0 })
  event('message_delta', { delta: { stop_reason: step.tool ? 'tool_use' : 'end_turn', stop_sequence: null }, usage: { output_tokens: 1 } })
  event('message_stop', {})
  return lines.join('')
}

/** One Gemini streamed turn. */
const gemini = (text: string) => `data: ${JSON.stringify({ candidates: [{ content: { role: 'model', parts: [{ text }] }, index: 0, finishReason: 'STOP' }] })}\n\n`

interface Sent { suffix: string; body: Record<string, unknown> }
/** A model capability that answers turn by turn, as a stream or whole, and keeps what it was sent. */
function scripted(turns: string[], whole?: (index: number) => unknown): { call: ModelCall; sent: Sent[] } {
  const sent: Sent[] = []
  const call: ModelCall = async (suffix, request) => {
    sent.push({ suffix, body: JSON.parse(request.body) as Record<string, unknown> })
    if (whole) return new Response(JSON.stringify(whole(sent.length - 1)), { status: 200, headers: { 'content-type': 'application/json' } })
    const at = Math.min(sent.length - 1, turns.length - 1)
    return new Response(turns[at]!, { status: 200, headers: { 'content-type': 'text/event-stream' } })
  }
  return { call, sent }
}

function session(
  family: EndpointKind,
  model: ModelCall,
  capabilities: Partial<ModelCapabilities> = {},
  changes: Partial<AssistantSession> & { tier?: ThinkingTier } = {}
): AssistantSession {
  const { tier, ...rest } = changes
  return {
    prompt: 'the runtime’s prompt', testPrompt: '', tools: TOOLS, hostTools: [],
    callTool: async (): Promise<McpToolResult> => ({ content: [{ type: 'text', text: '{"status":"valid"}' }] }),
    model: { family, model: 'a-model', call: model, capabilities: { ...FAMILY_CAPABILITIES[family], ...capabilities } },
    thinking: normalize(tier ?? 'off', family), signal: new AbortController().signal,
    ...rest
  }
}

async function drain(events: AsyncIterable<AssistantEvent>): Promise<AssistantEvent[]> {
  const seen: AssistantEvent[] = []
  for await (const event of events) seen.push(event)
  return seen
}
const of = <T extends AssistantEvent['type']>(events: AssistantEvent[], type: T) => events.filter((event): event is Extract<AssistantEvent, { type: T }> => event.type === type)
/** Every text the request's user turns carry, part by part: the state rides as a second text part (agent#9). */
const userTexts = (body: Record<string, unknown>) => ((body.messages ?? []) as { role: string; content: unknown }[]).filter((message) => message.role === 'user')
  .flatMap((message) => typeof message.content === 'string' ? [message.content] : (message.content as { type: string; text?: string }[]).flatMap((part) => part.type === 'text' ? [part.text ?? ''] : []))
const systemText = (body: Record<string, unknown>) => String(((body.messages ?? []) as { role: string; content: unknown }[]).find((message) => message.role === 'system')?.content ?? '')

describe('thinking settings read the record', () => {
  it('spells the tier in the record’s reasoning kind', async () => {
    const { call, sent } = scripted([text(PROPOSAL)])
    await drain(vercel.start(session('openai-compatible', call, {}, { tier: 'on' })))
    expect(sent[0]!.body.reasoning_effort).toBe('high')
  })

  it('sends no member where the record names no reasoning parameter, and says so once before the first request', async () => {
    const { call, sent } = scripted([native('validate'), text(PROPOSAL)])
    const events = await drain(vercel.start(session('openai-compatible', call, { reasoning: { kind: 'none' } }, { tier: 'on' })))
    for (const request of sent) expect(request.body).not.toHaveProperty('reasoning_effort')
    expect(of(events, 'thinking_unavailable').map((event) => event.detail)).toEqual(['thinking is unavailable for this endpoint: its capability record names no reasoning parameter'])
    expect(events[0]!.type).toBe('thinking_unavailable')
    expect(of(events, 'proposal')).toHaveLength(1)
  })

  it('sends no member where the record says the endpoint ignores it, and says that', async () => {
    const { call, sent } = scripted([text(PROPOSAL)])
    const events = await drain(vercel.start(session('openai-compatible', call, { reasoning: { kind: 'openai-effort', ignored: true } }, { tier: 'ultra' })))
    expect(sent[0]!.body).not.toHaveProperty('reasoning_effort')
    expect(of(events, 'thinking_unavailable')[0]!.detail).toContain('ignores the reasoning parameter')
  })

  it('refuses a reasoning kind the session’s wire does not speak, before any request', async () => {
    const { call, sent } = scripted([text(PROPOSAL)])
    const events = await drain(vercel.start(session('openai-compatible', call, { reasoning: { kind: 'anthropic-thinking' } }, { tier: 'on' })))
    expect(sent).toEqual([])
    expect(of(events, 'error')[0]!.failure?.code).toBe('configuration')
    expect(of(events, 'error')[0]!.message).toContain('not spoken on the openai-compatible wire')
  })
})

describe('schema narrowing reads the record', () => {
  it('shows an endpoint that takes the Gemini subset the narrowed schema, on any wire', async () => {
    const { call, sent } = scripted([text(PROPOSAL)])
    await drain(vercel.start(session('openai-compatible', call, { toolSchemas: 'gemini-subset' })))
    const shown = (sent[0]!.body.tools as { function: { name: string; parameters: Record<string, unknown> } }[]).find((tool) => tool.function.name === 'validate')!.function.parameters
    expect(shown).toEqual({ type: 'object', properties: { pack: { type: 'object' }, mode: {} } })
  })

  it('shows the served schema whole where the record says JSON Schema, Gemini wire included', async () => {
    const { call, sent } = scripted([gemini(PROPOSAL)])
    await drain(vercel.start(session('gemini', call, { toolSchemas: 'json-schema' })))
    const declarations = (sent[0]!.body.tools as { functionDeclarations: { name: string; parametersJsonSchema?: unknown; parameters?: unknown }[] }[])[0]!.functionDeclarations
    const validate = declarations.find((declared) => declared.name === 'validate')!
    expect(validate.parametersJsonSchema ?? validate.parameters).toEqual(SERVED)
  })
})

describe('whether to offer tools reads the record', () => {
  it('offers native tools, and asks for no single call, by default', async () => {
    const { call, sent } = scripted([text(PROPOSAL)])
    await drain(vercel.start(session('openai-compatible', call)))
    expect((sent[0]!.body.tools as unknown[]).length).toBeGreaterThan(0)
    expect(sent[0]!.body).not.toHaveProperty('parallel_tool_calls')
  })

  it('asks for one call per turn where the record says no parallel calls, on a request that offers tools', async () => {
    const { call, sent } = scripted([native('validate'), text(PROPOSAL)])
    await drain(vercel.start(session('openai-compatible', call, { parallelToolCalls: false })))
    expect(sent.map((request) => request.body.parallel_tool_calls)).toEqual([false, false])
    const anthropicModel = scripted([anthropic({ tool: 'validate' }), anthropic({ text: PROPOSAL })])
    await drain(vercel.start(session('anthropic', anthropicModel.call, { parallelToolCalls: false })))
    for (const request of anthropicModel.sent) expect(request.body.tool_choice).toMatchObject({ disable_parallel_tool_use: true })
  })

  it('describes the tools in the instructions and sends none where the record says no native tool calls', async () => {
    const { call, sent } = scripted([text(textCall('validate', { pack: { id: 'p' } })), text(PROPOSAL)])
    const calls: { name: string; args: unknown }[] = []
    const events = await drain(vercel.start(session('openai-compatible', call, { nativeToolCalls: false }, {
      callTool: async (name, args) => { calls.push({ name, args }); return { content: [{ type: 'text', text: '{"status":"valid"}' }] } }
    })))
    for (const request of sent) {
      expect(request.body).not.toHaveProperty('tools')
      expect(request.body).not.toHaveProperty('tool_choice')
      expect(systemText(request.body)).toContain('TOOLS\nThis endpoint takes no native tool calls')
      expect(systemText(request.body)).toContain(`- validate: check a document\n  input schema: ${JSON.stringify(SERVED)}`)
      for (const name of ['list_context', 'read_context', 'read_window', 'search']) expect(systemText(request.body)).toContain(`- ${name}: `)
    }
    // The call written in text ran, through callTool, with the model's arguments.
    expect(calls).toEqual([{ name: 'validate', args: { pack: { id: 'p' } } }])
    expect(of(events, 'tool_call').map((event) => event.name)).toEqual(['validate'])
    // Its result went back as the next message, headed as a result.
    expect(userTexts(sent[1]!.body).some((said) => said.startsWith('TOOL RESULT from validate (from the host'))).toBe(true)
    expect(of(events, 'proposal')).toHaveLength(1)
  })

  it('refuses a tool named in text that was never offered, through the gate, whatever the gate answers', async () => {
    // A callTool with no gate behind it answers anything: the refusal must not depend on it.
    const calls: string[] = []
    const { call, sent } = scripted([text(textCall('write_file', { path: 'pack.json' })), text(PROPOSAL)])
    const events = await drain(vercel.start(session('openai-compatible', call, { nativeToolCalls: false }, {
      callTool: async (name) => { calls.push(name); return { content: [{ type: 'text', text: 'THE FILE IS SAVED' }] } }
    })))
    const result = of(events, 'tool_result').find((event) => event.name === 'write_file')!
    expect(result.isError).toBe(true)
    expect(result.text).toContain('never calls a tool it was not offered')
    expect(userTexts(sent[1]!.body).some((said) => said.startsWith('TOOL ERROR from write_file') && said.includes('refused'))).toBe(true)
    expect(userTexts(sent[1]!.body).join('\n')).not.toContain('THE FILE IS SAVED')
    // It was put through callTool, where the real gate refuses it, as a native unoffered call is.
    expect(calls).toEqual(['write_file'])
  })

  it('refuses a host tool the critic was not offered, named in its text, without running it', async () => {
    let asked = 0
    const ask: HostTool = { name: 'ask_user', interaction: 'question', description: 'ask the person', inputSchema: { type: 'object' }, execute: async () => { asked += 1; return { content: [] } } }
    const turns = [text(PROPOSAL), text('Asking first.\n' + textCall('ask_user', { question: 'which?' })), text('REFUTATION: none found.')]
    const { call, sent } = scripted(turns)
    const events = await drain(vercel.start(session('openai-compatible', call, { nativeToolCalls: false }, { hostTools: [ask], adversarialReview: true, testPrompt: 'the runtime’s test_pack guidance' })))
    expect(asked).toBe(0)
    expect(of(events, 'user_question')).toEqual([])
    expect(of(events, 'tool_result').find((event) => event.name === 'ask_user')!.isError).toBe(true)
    // The critic was described its own tools: the loop's less the ones a review may not use.
    expect(systemText(sent[1]!.body)).not.toContain('- ask_user: ')
    expect(of(events, 'critique')).toHaveLength(1)
  })

  it('tells the model a call it could not read was not made, and goes on', async () => {
    const malformed = '```json\n' + JSON.stringify({ tool: 'validate', arguments: {}, also: 1 }) + '\n```'
    const twice = textCall('validate') + '\n' + textCall('validate')
    const { call, sent } = scripted([text(malformed), text(twice), text(PROPOSAL)])
    const calls: string[] = []
    const events = await drain(vercel.start(session('openai-compatible', call, { nativeToolCalls: false }, {
      callTool: async (name) => { calls.push(name); return { content: [{ type: 'text', text: '{}' }] } }
    })))
    expect(calls).toEqual([])
    expect(userTexts(sent[1]!.body).find((said) => said.startsWith('TOOL ERROR from the tool call'))).toContain('nothing else, not "also"')
    expect(userTexts(sent[2]!.body).find((said) => said.includes('this answer made 2'))).toBeDefined()
    expect(of(events, 'proposal')).toHaveLength(1)
  })
})

describe('reasoning deltas read the record', () => {
  it('reads the member the record pins: an empty reasoning_content no longer hides the real reasoning', async () => {
    const { call } = scripted([streamed([{ reasoning_content: '', reasoning: 'the real reasoning' }, { content: PROPOSAL }])])
    const events = await drain(vercel.start(session('openai-compatible', call, { streamingQuirks: { reasoningField: 'reasoning', wholeJsonAnswer: false } }, { tier: 'on' })))
    expect(of(events, 'reasoning').filter((event) => event.done).map((event) => event.text)).toEqual(['the real reasoning'])
    expect(of(events, 'proposal')).toHaveLength(1)
  })

  it('carries reasoning back under the pinned member on the next request', async () => {
    const { call, sent } = scripted([streamed([{ reasoning: 'thought about it' }, { tool_calls: [{ index: 0, id: 'call_v', type: 'function', function: { name: 'validate', arguments: '{}' } }] }], 'tool_calls'), text(PROPOSAL)])
    await drain(vercel.start(session('openai-compatible', call, { streamingQuirks: { reasoningField: 'reasoning', wholeJsonAnswer: false } }, { tier: 'on' })))
    const assistant = (sent[1]!.body.messages as Record<string, unknown>[]).find((message) => message.role === 'assistant')!
    expect(assistant.reasoning).toBe('thought about it')
    expect(assistant).not.toHaveProperty('reasoning_content')
  })

  it('reads as the SDK reads where nothing is pinned: an empty reasoning_content hides the other member', async () => {
    const { call } = scripted([streamed([{ reasoning_content: '', reasoning: 'the real reasoning' }, { content: PROPOSAL }])])
    const events = await drain(vercel.start(session('openai-compatible', call, {}, { tier: 'on' })))
    expect(of(events, 'reasoning').map((event) => event.text).join('')).not.toContain('the real reasoning')
    expect(of(events, 'proposal')).toHaveLength(1)
  })

  it('asks an endpoint that answers whole for a whole answer, and reads it', async () => {
    const answers = [
      { id: 'a', object: 'chat.completion', model: 'm', choices: [{ index: 0, message: { role: 'assistant', content: null, tool_calls: [{ id: 'call_v', type: 'function', function: { name: 'validate', arguments: '{}' } }] }, finish_reason: 'tool_calls' }] },
      { id: 'b', object: 'chat.completion', model: 'm', choices: [{ index: 0, message: { role: 'assistant', content: PROPOSAL }, finish_reason: 'stop' }] }
    ]
    const { call, sent } = scripted([], (at) => answers[at])
    const events = await drain(vercel.start(session('openai-compatible', call, { streamingQuirks: { reasoningField: null, wholeJsonAnswer: true } })))
    for (const request of sent) {
      expect(request.body.stream).toBe(false)
      expect(request.body).not.toHaveProperty('stream_options')
    }
    expect(of(events, 'tool_call').map((event) => event.name)).toEqual(['validate'])
    expect(of(events, 'proposal')).toHaveLength(1)
    // By default the request asks to stream.
    const streaming = scripted([text(PROPOSAL)])
    await drain(vercel.start(session('openai-compatible', streaming.call)))
    expect(streaming.sent[0]!.body.stream).toBe(true)
  })

  it('asks the Gemini wire for its whole-answer method', async () => {
    const { call, sent } = scripted([], () => ({ candidates: [{ content: { role: 'model', parts: [{ text: PROPOSAL }] }, finishReason: 'STOP', index: 0 }] }))
    const events = await drain(vercel.start(session('gemini', call, { streamingQuirks: { reasoningField: null, wholeJsonAnswer: true } })))
    expect(sent[0]!.suffix).toBe('v1beta/models/a-model:generateContent')
    expect(of(events, 'proposal')).toHaveLength(1)
  })
})

describe('the context budget reads the record', () => {
  const window = { contextWindowTokens: 16384, outputTokens: 2048 }

  it('reserves the record’s output cap on every request, in place of the table’s', async () => {
    const small = scripted([text(PROPOSAL)])
    await drain(vercel.start(session('openai-compatible', small.call, window)))
    expect(small.sent[0]!.body.max_tokens).toBe(2048)
    const table = scripted([text(PROPOSAL)])
    await drain(vercel.start(session('openai-compatible', table.call)))
    expect(table.sent[0]!.body.max_tokens).toBe(8192)
  })

  it('holds the prompt to the record’s window: what the table’s window sends whole, the record’s retains and previews', async () => {
    const prompt = 'the policy: ' + 'x'.repeat(20_000) + ' THE END'
    const small = scripted([text(PROPOSAL)])
    await drain(vercel.start(session('openai-compatible', small.call, window, { prompt })))
    expect(userTexts(small.sent[0]!.body).join('\n')).toContain('The complete request is retained')
    expect(userTexts(small.sent[0]!.body).join('\n')).not.toContain(prompt)
    const table = scripted([text(PROPOSAL)])
    await drain(vercel.start(session('openai-compatible', table.call, {}, { prompt })))
    expect(userTexts(table.sent[0]!.body).join('\n')).toContain(prompt)
  })
})
