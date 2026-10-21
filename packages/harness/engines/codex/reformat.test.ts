/**
 * The Codex adapter's reformat run (agent#29): a proposal that cannot be read
 * earns one tool-free agent run that is shown the answer and asked for the same
 * proposal in the shape the contract reads, as the `vercel` engine's reformat
 * request is. Driven through the real agent transport against a scripted Desk
 * server socket: each agent run is a new run on the bridge, as each is a new
 * thread there.
 */
import { afterEach, describe, expect, it, vi } from 'vitest'
import { REFORMAT_INSTRUCTIONS } from '../contract'
import { codex, reformatPrompt } from './index'
import { runAssistantSession } from '../../session'
import { ASSISTANT_TOOLS } from '../../host'
import { RECORDED_TOOLS } from '../../certification/scriptedServer'
import { TEST_CASES_TOOL, codexTurn, gatedRuntime, ofType, runtimeAnswer, type Bridge } from '../../testing/scripted'
import type { AgentRequest, AgentRun } from '../../agent'
import type { AssistantEvent, McpTool } from '../../engine'

afterEach(() => {
  vi.unstubAllGlobals()
})

const fence = (body: string) => '```json\n' + body + '\n```'
const envelope = (id: string) => JSON.stringify({ proposal: { document: { id }, unknowns: ['who signs'] } })
const TWO_FENCES = 'Here are two drafts.\n\n' + fence(envelope('first')) + '\n\n' + fence(envelope('second'))
const ONE_FENCE = fence(envelope('second'))

/** A bridge whose first run answers `first` and whose later runs answer `then`. */
function answers(first: string, then: (bridge: Bridge) => void) {
  return async (bridge: Bridge, run: number) => {
    bridge.started()
    if (run === 1) {
      bridge.say(first)
      bridge.end()
      return
    }
    then(bridge)
  }
}

describe('the Codex adapter’s reformat run (agent#29)', () => {
  it('gives an answer carrying two proposal fences one reformat run, tool-free, shown its own answer', async () => {
    const { events, runs } = await codexTurn({
      script: answers(TWO_FENCES, (bridge) => { bridge.say(ONE_FENCE); bridge.end() }),
      session: { allowConversation: false, interactive: false }
    })
    expect(runs).toHaveLength(2)
    const [author, reformat] = runs
    // One run on the bridge, as the author: the same instructions, no tool.
    expect(reformat!.phase).toBe('author')
    expect(reformat!.tools).toEqual([])
    expect(reformat!.instructions).toBe(author!.instructions)
    expect(reformat!.prompt).toContain(REFORMAT_INSTRUCTIONS)
    expect(reformat!.prompt).toContain('this one carried 2')
    expect(reformat!.prompt).toContain(TWO_FENCES)
    expect(events.filter((event) => event.type !== 'execution_checkpoint' && event.type !== 'policy_resolved').map((event) => event.type)).toEqual(['response_recovery', 'message', 'critic_decided', 'proposal', 'end'])
    // What the model said beside the document is the first answer's.
    expect(ofType(events, 'message')).toEqual([{ type: 'message', text: 'Here are two drafts.' }])
    expect(ofType(events, 'proposal')[0]).toEqual({ type: 'proposal', document: { id: 'second' }, unknowns: ['who signs'] })
  })

  it('recovers a proposal whose fence is not valid JSON', async () => {
    const broken = 'The draft.\n\n' + fence('{"proposal":{"document":{"id":"p",},"unknowns":[]}}')
    const { events, runs } = await codexTurn({
      script: answers(broken, (bridge) => { bridge.say(fence(envelope('p'))); bridge.end() }),
      session: { allowConversation: false }
    })
    expect(runs).toHaveLength(2)
    expect(runs[1]!.prompt).toContain('the fenced proposal block is not valid JSON')
    expect(ofType(events, 'proposal')[0]).toMatchObject({ document: { id: 'p' } })
    expect(ofType(events, 'error')).toEqual([])
  })

  it('asks once: a reformat that is unreadable too keeps the first answer whole and reports its failure', async () => {
    const { events, runs } = await codexTurn({
      script: answers(TWO_FENCES, (bridge) => { bridge.say(TWO_FENCES); bridge.end() }),
      session: { allowConversation: false }
    })
    expect(runs).toHaveLength(2)
    expect(events.filter((event) => event.type !== 'execution_checkpoint' && event.type !== 'policy_resolved').map((event) => event.type)).toEqual(['response_recovery', 'message', 'error', 'end'])
    expect(ofType(events, 'message')).toEqual([{ type: 'message', text: TWO_FENCES }])
    expect(ofType(events, 'error')[0]!.failure).toMatchObject({ code: 'invalid-response', source: 'desk', detail: expect.stringContaining('this one carried 2') })
  })

  it('keeps the first answer whole where the reformat run fails, and reports the run’s failure', async () => {
    const { events, runs } = await codexTurn({
      script: answers(TWO_FENCES, (bridge) => bridge.end('limit', { version: 1, code: 'timeout', source: 'agent', outcome: 'unknown' })),
      session: { allowConversation: false }
    })
    expect(runs).toHaveLength(2)
    expect(events.filter((event) => event.type !== 'execution_checkpoint' && event.type !== 'policy_resolved').map((event) => event.type)).toEqual(['response_recovery', 'message', 'error', 'end'])
    expect(ofType(events, 'message')).toEqual([{ type: 'message', text: TWO_FENCES }])
    expect(ofType(events, 'error')[0]!.failure).toMatchObject({ code: 'timeout', source: 'agent' })
  })

  it('in a conversation, a reformat that answers in prose: the first answer whole, the prose, then the first failure', async () => {
    const { events } = await codexTurn({
      script: answers(TWO_FENCES, (bridge) => { bridge.say('I cannot pick one of the two.'); bridge.end() }),
      session: { allowConversation: true }
    })
    expect(ofType(events, 'message')).toEqual([{ type: 'message', text: TWO_FENCES }, { type: 'message', text: 'I cannot pick one of the two.' }])
    expect(ofType(events, 'error')[0]!.failure).toMatchObject({ code: 'invalid-response' })
    expect(ofType(events, 'proposal')).toEqual([])
  })

  /**
   * A reformat run through the real tool gate (review row 5): the author's
   * answer is unreadable, and the reformat run calls `name` (the author's alias
   * for it where the author was offered it) before it answers.
   */
  async function reformatCalls(name: string, served: (tools: McpTool[]) => McpTool[] = (tools) => tools) {
    const events: AssistantEvent[] = []
    const runtime = await gatedRuntime({ tools: [...RECORDED_TOOLS, TEST_CASES_TOOL], allowed: [...ASSISTANT_TOOLS], handle: () => runtimeAnswer({ status: 'ok' }), onEvent: (event) => events.push(event) })
    let author: AgentRequest['tools'] = []
    let answer: { text: string; isError: boolean } | undefined
    const run: AgentRun = async (request, callbacks) => {
      if (request.tools.length > 0) {
        author = request.tools
        await callbacks.event({ type: 'message', id: 'a', text: TWO_FENCES, phase: 'final' })
        return
      }
      const alias = author.find((tool) => tool.description.startsWith(`${name}:`))?.name ?? name
      answer = await callbacks.tool({ id: 'r', name: alias, arguments: {} }, callbacks.signal)
      await callbacks.event({ type: 'message', id: 'b', text: ONE_FENCE, phase: 'final' })
    }
    try {
      await runAssistantSession(codex, {
        prompt: 'p', testPrompt: '', tools: served(runtime.tools), callTool: runtime.callTool, hostTools: [],
        agent: { model: 'agent-model', run }, signal: new AbortController().signal
      }, (event) => events.push(event))
    } finally {
      await runtime.close()
    }
    return { events, seen: runtime.seen, answer }
  }

  it('refuses a granted read the session serves but the reformat run was not offered, before any call, through the real gate (review row 5)', async () => {
    const { events, seen, answer } = await reformatCalls('list_examples')
    expect(seen).toEqual([])
    expect(ofType(events, 'guardrail')).toEqual([{ type: 'guardrail', tool: 'list_examples', action: 'refused', detail: expect.stringContaining('list_examples is served to this session but was not offered to this run') }])
    expect(answer).toMatchObject({ isError: true, text: expect.stringContaining('refused: list_examples is served to this session') })
    expect(ofType(events, 'tool_result')).toEqual([expect.objectContaining({ name: 'list_examples', isError: true })])
    expect(ofType(events, 'proposal')[0]).toMatchObject({ document: { id: 'second' } })
  })

  it('refuses every call during the reformat run at the boundary: a granted read the session does not serve, through the real gate (review row 5)', async () => {
    // The gate grants list_examples; the session was handed the runtime's tools without it.
    const { events, seen, answer } = await reformatCalls('list_examples', (tools) => tools.filter((tool) => tool.name !== 'list_examples'))
    expect(seen).toEqual([])
    expect(ofType(events, 'guardrail')).toEqual([{ type: 'guardrail', tool: 'list_examples', action: 'refused', detail: expect.stringContaining('list_examples is not offered to the reformat run, which is offered no tool') }])
    expect(answer).toMatchObject({ isError: true })
  })

  it('makes no reformat run for a readable proposal or a conversation’s prose', async () => {
    for (const [answer, conversation] of [[ONE_FENCE, false], ['Just prose.', true]] as const) {
      const { runs, events } = await codexTurn({
        script: async (bridge) => { bridge.started(); bridge.say(answer); bridge.end() },
        session: { allowConversation: conversation }
      })
      expect(runs).toHaveLength(1)
      expect(ofType(events, 'response_recovery')).toEqual([])
    }
  })

  it('shows the model its answer between two marked lines, after the request', () => {
    const prompt = reformatPrompt('ANSWER', new Error('x'))
    expect(prompt.indexOf(REFORMAT_INSTRUCTIONS)).toBe(0)
    expect(prompt).toContain('BEGIN PREVIOUS ANSWER\nANSWER\nEND PREVIOUS ANSWER')
  })
})
