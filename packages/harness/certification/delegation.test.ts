/**
 * A certification check every adapter runs (agent#17, the review of agent#39 row 5): a delegation
 * batch runs as many children at once as the run's connection serves requests, the host's answer
 * for that connection (`modelsFor`'s `concurrentRequests`, asked with `context.connection`), and an
 * agent capability runs one child at a time whatever the answer says. The adapter is loaded down the
 * registry's own path and wrapped by the delegation coordinator, with a scripted model capability or
 * agent behind it.
 */
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { AgentRun } from '../agent'
import { withDelegation } from '../delegation'
import type { AssistantEvent, EngineSession } from '../engine'
import { AGENT_ENGINES, MODEL_ENGINES, loadEngine } from '../engines'
import { configureHarness } from '../host'
import { normalize } from '../thinking'

afterEach(() => configureHarness({ modelsFor: () => ({}) }))

const TASKS = ['A', 'B', 'C'].map((title) => ({ title, objective: `Check part ${title}.`, profile: 'analyze' }))
/** The connection the run is bound to, and the host's answer for it: three requests at once. */
const CONNECTION = { connectionId: 'three-at-once' }
const answerForConnection = () =>
  configureHarness({ modelsFor: (run) => (run.connection?.connectionId === CONNECTION.connectionId ? { concurrentRequests: 3 } : {}) })

const frame = (choices: unknown[]) => `data: ${JSON.stringify({ id: 'c', object: 'chat.completion.chunk', model: 'm', choices })}\n\n`
const streamed = (body: string) => new Response(body, { headers: { 'content-type': 'text/event-stream' } })
const turn = (step: { text?: string; tool?: { name: string; args: unknown } }) =>
  streamed(
    frame([{ index: 0, delta: { role: 'assistant', content: null }, finish_reason: null }]) +
      (step.tool
        ? frame([{ index: 0, delta: { tool_calls: [{ index: 0, id: 'call_batch', type: 'function', function: { name: step.tool.name, arguments: JSON.stringify(step.tool.args) } }] }, finish_reason: null }]) +
          frame([{ index: 0, delta: {}, finish_reason: 'tool_calls' }])
        : frame([{ index: 0, delta: { content: step.text ?? '' }, finish_reason: null }]) + frame([{ index: 0, delta: {}, finish_reason: 'stop' }])) +
      'data: [DONE]\n\n'
  )
const isChild = (body: string) => body.includes('Complete this read-only task')
async function drain(stream: AsyncIterable<AssistantEvent>) {
  const events: AssistantEvent[] = []
  for await (const event of stream) events.push(event)
  return events
}
const session = {
  prompt: 'Check the expense pack.',
  testPrompt: '',
  tools: [],
  callTool: vi.fn(),
  hostTools: [],
  allowConversation: true,
  interactive: true,
  adversarialReview: false,
  context: { connection: CONNECTION }
}

describe.each(MODEL_ENGINES)('engine %s · delegation', (engineId) => {
  it('runs as many children at once as the connection’s answer says', async () => {
    answerForConnection()
    let active = 0
    let peak = 0
    const call = vi.fn(async (_suffix: string, request: { body: string }) => {
      if (isChild(request.body)) {
        active++
        peak = Math.max(peak, active)
        await new Promise((resolve) => setTimeout(resolve, 30))
        active--
        return turn({ text: 'Checked.' })
      }
      return request.body.includes('"role":"tool"') ? turn({ text: 'All three check.' }) : turn({ tool: { name: 'delegate_tasks', args: { tasks: TASKS } } })
    })
    const engine = await loadEngine(engineId)
    const events = await drain(
      withDelegation(engine).start({ ...session, signal: new AbortController().signal, model: { family: 'openai-compatible', model: 'fixture', call }, thinking: normalize('off', 'openai-compatible') } as EngineSession)
    )
    expect(events.filter((event) => event.type === 'error')).toEqual([])
    expect(peak).toBe(3)
  })
})

describe.each(AGENT_ENGINES)('agent engine %s', (engineId) => {
  it('runs one delegated child at a time, whatever the connection’s answer says', async () => {
    answerForConnection()
    let active = 0
    let peak = 0
    const run = vi.fn<AgentRun>(async (request, callbacks) => {
      const delegate = request.tools.find((tool) => tool.description.startsWith('delegate_tasks:'))
      if (delegate && !request.prompt.includes('DELEGATION RESULT')) {
        await callbacks.tool({ id: 'call', name: delegate.name, arguments: { tasks: TASKS } }, callbacks.signal)
        throw new Error('the parent yields at its delegate call')
      }
      if (!delegate) {
        active++
        peak = Math.max(peak, active)
        await new Promise((resolve) => setTimeout(resolve, 30))
        active--
      }
      await callbacks.event({ type: 'message', id: 'm', text: delegate ? 'All three check.' : 'Checked.', phase: 'final' })
    })
    const engine = await loadEngine(engineId)
    const events = await drain(withDelegation(engine).start({ ...session, signal: new AbortController().signal, agent: { model: 'fixture', run } } as EngineSession))
    expect(events.filter((event) => event.type === 'error')).toEqual([])
    expect(peak).toBe(1)
  })
})
