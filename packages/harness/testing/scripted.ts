/**
 * Scripted endpoints for the tests that drive one failure, one bound or one
 * page at a time. Also emitted for the exported certification scenario server.
 *
 * - `scriptedEndpoint`: the relay's model endpoint on the OpenAI-compatible
 *   wire, as a `fetch` stub that answers each request with whatever the test
 *   decides: a streamed turn, a whole JSON answer, a refusal, a stream that
 *   stops. `bindModelCall` captures it, so the engine reaches it the way it
 *   reaches the Desk's relay.
 * - `gatedRuntime`: a runtime that serves the tools a test names, on an
 *   in-memory transport behind the real tool gate (`openAssistantConnection`),
 *   answering each call with the test's handler; `drop()` closes its socket.
 * - `scriptedBridge`: the Desk server's agent socket (`/api/agent/run`) for the
 *   codex engine, as a `WebSocket` stand-in the real agent transport
 *   (`bindAgentRun`) dials: a script starts the run, says things, calls tools
 *   by their real names and ends the run, or drops the socket.
 */
import { InMemoryTransport } from '@modelcontextprotocol/sdk/inMemory.js'
import type { JSONRPCMessage } from '@modelcontextprotocol/sdk/types.js'
import { captureGlobal } from './captureGlobal'
import { bindModelCall, openAssistantConnection, runAssistantSession } from '../session'
import { bindAgentRun } from '../agentTransport'
import { ASSISTANT_TOOLS } from '../host'
import { normalize } from '../thinking'
import { vercel } from '../engines/vercel'
import { codex } from '../engines/codex'
import { RECORDED_TOOLS } from '../certification/scriptedServer'
import type { AgentRequest } from '../agent'
import type { AgentAssistantSession, AssistantEvent, AssistantSession, Engine, HostTool, McpTool, McpToolResult } from '../engine'

/**
 * `experimental_test_cases` as `jpack mcp` serves it: its description and its
 * input schema exactly, two members, both required, nothing else admitted.
 */
export const TEST_CASES_TOOL: McpTool = {
  name: 'experimental_test_cases',
  description:
    'Rehearse a matrix against exact supplied pack bytes. Uses the same canonical comparison and coverage as ' +
    'experimental_test_packs. Reads no project files, writes nothing, and appends no audit record. This ' +
    'experimental surface may change.',
  inputSchema: {
    type: 'object',
    properties: {
      pack: { type: 'string', description: 'Exact pack JSON text.' },
      matrix: {
        type: 'string',
        description:
          "Matrix JSON text in the project's supported matrix format. At least one case with an independently " +
          'established expectation is required.'
      }
    },
    required: ['pack', 'matrix'],
    additionalProperties: false
  }
}

/** `test_conformance` as `jpack mcp` serves it: the one whose `suite` is a path. */
export const TEST_CONFORMANCE_TOOL: McpTool = {
  name: 'test_conformance',
  description: 'Run a version-pinned JPS conformance corpus; the bundled corpus by default.',
  inputSchema: {
    type: 'object',
    properties: {
      spec_version: { type: 'string', description: 'Optional exact JPS version; defaults to the bundled draft.' },
      suite: {
        type: 'string',
        description: 'Optional path to a local suite directory or manifest.json; omit for the bundled corpus.'
      }
    },
    additionalProperties: false
  }
}

/** A runtime answer in the shape `jpack mcp` gives one: the JSON text and the same value structured. */
export function runtimeAnswer(value: unknown): McpToolResult {
  return { content: [{ type: 'text', text: JSON.stringify(value) }], structuredContent: value }
}

/** One tool call of a turn, as the wire names it. */
type ScriptedCall = { name: string; args: unknown; id?: string }

/** One assistant turn on the OpenAI-compatible wire, streamed: text, one tool call, or several in one answer (`tools`). */
export function sseTurn(step: { text?: string; tool?: ScriptedCall; tools?: ScriptedCall[] }): string {
  const frame = (choices: unknown[]) =>
    `data: ${JSON.stringify({ id: 'c', object: 'chat.completion.chunk', model: 'm', choices })}\n\n`
  const lines = [frame([{ index: 0, delta: { role: 'assistant', content: null }, finish_reason: null }])]
  const calls = step.tools ?? (step.tool === undefined ? undefined : [step.tool])
  if (calls !== undefined) {
    for (const [index, tool] of calls.entries()) {
      const call = {
        index,
        id: tool.id ?? `call_${tool.name}`,
        type: 'function',
        function: { name: tool.name, arguments: JSON.stringify(tool.args) }
      }
      lines.push(frame([{ index: 0, delta: { tool_calls: [call] }, finish_reason: null }]))
    }
    lines.push(frame([{ index: 0, delta: {}, finish_reason: 'tool_calls' }]))
  } else {
    lines.push(
      frame([{ index: 0, delta: { content: step.text ?? '' }, finish_reason: null }]),
      frame([{ index: 0, delta: {}, finish_reason: 'stop' }])
    )
  }
  lines.push('data: [DONE]\n\n')
  return lines.join('')
}

/** The same turn as one whole JSON answer, as an endpoint that ignores `stream` sends it. */
export function wholeTurn(step: { text?: string; tool?: ScriptedCall; tools?: ScriptedCall[] }): string {
  const calls = step.tools ?? (step.tool === undefined ? undefined : [step.tool])
  const message =
    calls !== undefined
      ? {
          role: 'assistant',
          content: null,
          tool_calls: calls.map((tool) => ({
            id: tool.id ?? `call_${tool.name}`,
            type: 'function',
            function: { name: tool.name, arguments: JSON.stringify(tool.args) }
          }))
        }
      : { role: 'assistant', content: step.text ?? '' }
  return JSON.stringify({
    id: 'c',
    object: 'chat.completion',
    created: 0,
    model: 'm',
    choices: [{ index: 0, message, finish_reason: calls !== undefined ? 'tool_calls' : 'stop' }],
    usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 }
  })
}

export const streamed = (text: string, status = 200): Response =>
  new Response(text, { status, headers: { 'content-type': 'text/event-stream' } })
export const whole = (text: string, status = 200, headers: Record<string, string> = {}): Response =>
  new Response(text, { status, headers: { 'content-type': 'application/json', ...headers } })

/** One request as it reached the scripted endpoint. */
export interface EndpointRequest {
  /** From 1. */
  number: number
  body: {
    stream?: boolean
    messages?: { role: string; content?: unknown; tool_call_id?: string; tool_calls?: unknown[] }[]
  }
  signal: AbortSignal | undefined
}

/** The text each tool result in a request showed the model, in order. */
export function toolTexts(request: EndpointRequest): string[] {
  return (request.body.messages ?? [])
    .filter((message) => message.role === 'tool')
    .map((message) => (typeof message.content === 'string' ? message.content : JSON.stringify(message.content)))
}

/** The system text a request carried. */
export function systemText(request: EndpointRequest): string {
  return (request.body.messages ?? [])
    .filter((message) => message.role === 'system')
    .map((message) => String(message.content))
    .join('\n')
}

/** The model endpoint as a `fetch` stub. Stub it before `bindModelCall` captures `fetch`. */
export function scriptedEndpoint(answer: (request: EndpointRequest) => Response | Promise<Response>): {
  fetch: typeof fetch
  requests: EndpointRequest[]
} {
  const requests: EndpointRequest[] = []
  const stub = async (_input: unknown, init?: RequestInit): Promise<Response> => {
    const request: EndpointRequest = {
      number: requests.length + 1,
      body: JSON.parse(String(init?.body ?? '{}')) as EndpointRequest['body'],
      signal: init?.signal ?? undefined
    }
    requests.push(request)
    return answer(request)
  }
  return { fetch: stub as unknown as typeof fetch, requests }
}

/** One `tools/call` as the runtime received it, after the gate. */
export interface RuntimeCall {
  name: string
  args: Record<string, unknown>
}

/** What a runtime handler can do besides answering. */
export interface RuntimeControl {
  /** Close the runtime's socket now, with the call in flight. */
  drop(): void
}

/**
 * A runtime serving `tools`, behind the real gate with `allowed` as the
 * session's allow-list. The handler's value is the call's result; a throw is a
 * JSON-RPC error; a handler that drops the socket answers nothing.
 */
export async function gatedRuntime(options: {
  tools: McpTool[]
  allowed: readonly string[]
  handle: (call: RuntimeCall, control: RuntimeControl) => Promise<unknown> | unknown
  onEvent: (event: AssistantEvent) => void
}) {
  const [clientSide, serverSide] = InMemoryTransport.createLinkedPair()
  const seen: RuntimeCall[] = []
  const control: RuntimeControl = { drop: () => void serverSide.close() }
  serverSide.onmessage = (message: JSONRPCMessage) => {
    const frame = message as { id?: string | number; method?: string; params?: { name?: string; arguments?: Record<string, unknown> } }
    if (frame.id === undefined || frame.method === undefined) return
    const reply = (body: Record<string, unknown>) =>
      void serverSide.send({ jsonrpc: '2.0', id: frame.id, ...body } as unknown as JSONRPCMessage).catch(() => undefined)
    if (frame.method === 'initialize') {
      reply({ result: { protocolVersion: '2025-06-18', capabilities: { tools: {} }, serverInfo: { name: 'jpack', version: '0.0.0-test' } } })
      return
    }
    if (frame.method === 'tools/list') {
      reply({ result: { tools: options.tools } })
      return
    }
    if (frame.method !== 'tools/call') {
      reply({ error: { code: -32601, message: `not served: ${frame.method}` } })
      return
    }
    const call = { name: String(frame.params?.name ?? ''), args: frame.params?.arguments ?? {} }
    seen.push(call)
    void (async () => {
      try {
        const result = await options.handle(call, control)
        if (result !== undefined) reply({ result })
      } catch (cause) {
        reply({ error: { code: -32602, message: (cause as Error).message } })
      }
    })()
  }
  await serverSide.start()
  const connection = openAssistantConnection({ allowed: options.allowed, onEvent: options.onEvent, transport: clientSide })
  const ready = await connection.ready
  return {
    ...ready,
    seen,
    drop: control.drop,
    close: async () => {
      await connection.close()
      await serverSide.close().catch(() => undefined)
    }
  }
}

const hex = (seed: number) => seed.toString(16).padStart(48, '0')

/** The Desk server's side of one agent run, as a script drives it. */
export interface Bridge {
  /** The request the browser started the run with: prompt, instructions, aliased tools. */
  request: AgentRequest
  /** Send `started`. Every other frame needs it first. */
  started(): void
  /** One message event: `text` streams, `message` is whole (final by default). */
  say(text: string, options?: { type?: 'text' | 'message'; phase?: 'commentary' | 'final' }): void
  /** Call a tool by its real name; the browser's answer comes back. */
  call(name: string, args: Record<string, unknown>): Promise<{ text: string; isError: boolean }>
  /** The terminal frame: `''` for success, otherwise the server's error code and its failure. */
  end(error?: string, failure?: unknown): void
  /** Close the socket with no terminal frame. */
  drop(): void
}

/**
 * The agent socket, scripted: each run the browser starts runs `script` once,
 * with the run's request. Bind the scripted `WebSocket` synchronously before
 * `bindAgentRun` captures it.
 */
export function scriptedBridge(script: (bridge: Bridge, run: number) => Promise<void>): {
  WebSocket: typeof WebSocket
  runs: AgentRequest[]
  /** What a script threw; its socket was dropped so the run ends rather than waits. */
  errors: unknown[]
} {
  const runs: AgentRequest[] = []
  const errors: unknown[] = []
  let ids = 1
  class BridgeSocket {
    onopen: (() => void) | null = null
    onmessage: ((event: { data: string }) => void) | null = null
    onerror: (() => void) | null = null
    onclose: (() => void) | null = null
    bufferedAmount = 0
    private closed = false
    private runId = hex(ids++)
    private answers = new Map<string, (answer: { text: string; isError: boolean }) => void>()
    constructor() {
      queueMicrotask(() => this.onopen?.())
    }
    private receive(value: unknown) {
      if (!this.closed) this.onmessage?.({ data: JSON.stringify(value) })
    }
    send(text: string) {
      const value = JSON.parse(text) as { type: string; request?: AgentRequest; callId?: string; answer?: { text: string; isError: boolean } }
      if (value.type === 'tool-result') {
        this.answers.get(String(value.callId))?.(value.answer!)
        return
      }
      if (value.type !== 'start') return
      const request = value.request!
      runs.push(request)
      const bridge: Bridge = {
        request,
        started: () => this.receive({ type: 'started', runId: this.runId }),
        say: (said, options = {}) =>
          this.receive({ type: 'event', runId: this.runId, event: { type: options.type ?? 'message', id: hex(ids++), text: said, ...(options.type === 'text' ? {} : { phase: options.phase ?? 'final' }) } }),
        call: (name, args) =>
          new Promise((resolve) => {
            const alias = request.tools.find((tool) => tool.description.startsWith(`${name}:`))?.name ?? name
            const id = hex(ids++)
            this.answers.set(id, resolve)
            this.receive({ type: 'tool-call', runId: this.runId, call: { id, name: alias, arguments: args } })
          }),
        end: (error = '', failure) => {
          this.receive({ type: 'end', runId: this.runId, error, ...(failure === undefined ? {} : { failure }) })
          this.close()
        },
        drop: () => this.close()
      }
      void script(bridge, runs.length).catch((cause: unknown) => {
        errors.push(cause)
        this.close()
      })
    }
    close() {
      if (this.closed) return
      this.closed = true
      queueMicrotask(() => this.onclose?.())
    }
  }
  return { WebSocket: BridgeSocket as unknown as typeof WebSocket, runs, errors }
}

/** What one scripted turn left behind. */
export interface TurnRecord {
  /** The run's events and the gate's, in the order they happened. */
  events: AssistantEvent[]
  /** What reached the runtime, after the gate. */
  seen: RuntimeCall[]
}

/** Legacy scenario assertions predate ADR-0020; scenario 20 certifies the terminal ledger itself. */
const legacyEvents = (events: AssistantEvent[]) => events.filter((event) => event.type !== 'usage')

const CURRENT_REQUEST = { message: 'Check the expense pack.', input: 'The expense pack.' }

/**
 * One `vercel` turn: the endpoint answers each request with `answer`, the
 * runtime serves the recorded five and the test tool behind the real gate.
 * A current request makes the run keep its checkpoint on the stream. `engine`
 * runs the same turn on another model engine (`deepagents`, ADR-0003).
 */
export async function vercelTurn(options: {
  answer: (request: EndpointRequest) => Response | Promise<Response>
  handle?: (call: RuntimeCall, control: RuntimeControl) => unknown
  hostTools?: HostTool[]
  session?: Partial<AssistantSession>
  engine?: Engine
}): Promise<TurnRecord & { requests: EndpointRequest[] }> {
  const events: AssistantEvent[] = []
  const endpoint = scriptedEndpoint(options.answer)
  const call = captureGlobal('fetch', endpoint.fetch, () => bindModelCall('openai-compatible'))
  const runtime = await gatedRuntime({
    tools: [...RECORDED_TOOLS, TEST_CASES_TOOL],
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
        model: { family: 'openai-compatible', model: 'scripted-model', call },
        thinking: normalize('off', 'openai-compatible'),
        signal: new AbortController().signal,
        context: { currentRequest: CURRENT_REQUEST },
        ...options.session
      },
      (event) => events.push(event)
    )
  } finally {
    await runtime.close()
  }
  return { events: legacyEvents(events), requests: endpoint.requests, seen: runtime.seen }
}

/**
 * One `codex` turn through the real agent transport: the scripted bridge runs
 * `script` for each agent run, and the runtime serves as `vercelTurn`'s does.
 */
export async function codexTurn(options: {
  script: (bridge: Bridge, run: number) => Promise<void>
  handle?: (call: RuntimeCall, control: RuntimeControl) => unknown
  hostTools?: HostTool[]
  session?: Partial<AgentAssistantSession>
}): Promise<TurnRecord & { runs: AgentRequest[] }> {
  const events: AssistantEvent[] = []
  const bridge = scriptedBridge(options.script)
  const run = captureGlobal('WebSocket', bridge.WebSocket, () => bindAgentRun('scripted-agent'))
  const runtime = await gatedRuntime({
    tools: [...RECORDED_TOOLS, TEST_CASES_TOOL],
    allowed: [...ASSISTANT_TOOLS],
    handle: options.handle ?? (() => runtimeAnswer({ status: 'ok' })),
    onEvent: (event) => events.push(event)
  })
  try {
    await runAssistantSession(
      codex,
      {
        prompt: 'Check the expense pack.',
        testPrompt: '',
        tools: runtime.tools,
        callTool: runtime.callTool,
        hostTools: options.hostTools ?? [],
        allowConversation: true,
        agent: { model: 'scripted-agent', run },
        signal: new AbortController().signal,
        context: { currentRequest: CURRENT_REQUEST },
        ...options.session
      },
      (event) => events.push(event)
    )
  } finally {
    await runtime.close()
  }
  if (bridge.errors.length > 0) throw bridge.errors[0]
  return { events: legacyEvents(events), seen: runtime.seen, runs: bridge.runs }
}

/** The events of one kind. */
export const ofType = <T extends AssistantEvent['type']>(events: AssistantEvent[], type: T) =>
  events.filter((event): event is Extract<AssistantEvent, { type: T }> => event.type === type)
