/**
 * The critic's default is the policy table's, and both engines read it from
 * there (agent#10). This file runs on a table that says `adversarialReview:
 * true`, as a host whose own table carries the member would; `critic.test.ts`
 * runs on the package's, which says `false`.
 */
import { describe, expect, it, vi } from 'vitest'
import { CONTEXT_DEFAULTS } from './context/policy'
import { runAssistantSession } from './session'
import { REFUTATION_MARKER } from './refutation'
import { codex } from './engines/codex'
import { ofType, sseTurn, streamed, vercelTurn } from './testing/scripted'
import type { AgentRun } from './agent'
import type { AssistantEvent } from './engine'

vi.mock('./contract/policy.json', async (original) => {
  const table = ((await original()) as { default: Record<string, unknown> }).default
  return { default: { ...table, adversarialReview: true } }
})

const PROPOSAL = 'Ready.\n\n```json\n' + JSON.stringify({ proposal: { document: { id: 'p' }, unknowns: [] } }) + '\n```'

describe('a policy table that says adversarialReview: true (agent#10)', () => {
  it('is the harness’s default', () => {
    expect(CONTEXT_DEFAULTS.adversarialReview).toBe(true)
  })

  it.each([undefined, false] as const)('vercel: a session that says %s runs the critic only where it leaves it unset', async (review) => {
    let critic = 0
    const { events } = await vercelTurn({
      answer: (request) => {
        const reviewing = JSON.stringify(request.body).includes(REFUTATION_MARKER)
        if (reviewing) critic += 1
        return streamed(sseTurn({ text: reviewing ? 'Done.' : PROPOSAL }))
      },
      session: { testPrompt: 'Test the pack.', ...(review === undefined ? {} : { adversarialReview: review }) }
    })
    expect(critic > 0).toBe(review === undefined)
    expect(ofType(events, 'critique')).toHaveLength(review === undefined ? 1 : 0)
  })

  it.each([undefined, false] as const)('codex: a session that says %s runs the critic only where it leaves it unset', async (review) => {
    const phases: string[] = []
    const run: AgentRun = async (request, callbacks) => {
      phases.push(request.phase)
      await callbacks.event({ type: 'message', id: request.phase, text: request.phase === 'critic' ? 'Done.' : PROPOSAL, phase: 'final' })
    }
    const events: AssistantEvent[] = []
    await runAssistantSession(codex, {
      prompt: 'p', testPrompt: 'Test the pack.', tools: [], callTool: vi.fn(), hostTools: [],
      ...(review === undefined ? {} : { adversarialReview: review }),
      agent: { model: 'agent-model', run }, signal: new AbortController().signal
    }, (event) => events.push(event))
    expect(phases).toEqual(review === undefined ? ['author', 'critic'] : ['author'])
    expect(ofType(events, 'critique')).toHaveLength(review === undefined ? 1 : 0)
  })
})
