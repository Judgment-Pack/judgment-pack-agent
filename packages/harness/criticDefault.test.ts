/**
 * The critic's default is the policy table's, and every engine reads it from
 * there (agent#10; ADR-0010). This file runs on a table that states no
 * `criticDefault` and says `critic: 'on'`, as a host's table from
 * before ADR-0010 would: the alias reads as `on`. `critic.test.ts` runs on the
 * package's table, which says `criticDefault: auto`.
 */
import { describe, expect, it, vi } from 'vitest'
import { CONTEXT_DEFAULTS } from './context/policy'
import { runAssistantSession } from './session'
import { REFUTATION_MARKER } from './refutation'
import { criticMode } from './criticSwitch'
import { codex } from './engines/codex'
import { ofType, sseTurn, streamed, vercelTurn } from './testing/scripted'
import type { AgentRun } from './agent'
import type { AssistantEvent } from './engine'

vi.mock('./contract/policy.json', async (original) => {
  const { critic: _critic, ...table } = ((await original()) as { default: Record<string, unknown> }).default
  return { default: { ...table, critic: {default:'on',reasoningFloor:'author'} } }
})

const PROPOSAL = 'Ready.\n\n```json\n' + JSON.stringify({ proposal: { document: { id: 'p' }, unknowns: [] } }) + '\n```'

describe('a canonical policy table with critic on', () => {
  it('is the harness’s default, read as on at the table’s layer', () => {
    expect(CONTEXT_DEFAULTS.criticDefault).toBe('on')
    expect(criticMode({}, CONTEXT_DEFAULTS)).toEqual({ mode: 'on', layer: 'table' })
    // The table's criticDefault wins over its alias where both are set.
    expect(criticMode({}, { ...CONTEXT_DEFAULTS, criticDefault: 'off' })).toEqual({ mode: 'off', layer: 'table' })
  })

  it.each([undefined, false] as const)('vercel: a session that says %s runs the critic only where it leaves it unset', async (review) => {
    let critic = 0
    const { events } = await vercelTurn({
      answer: (request) => {
        const reviewing = JSON.stringify(request.body).includes(REFUTATION_MARKER)
        if (reviewing) critic += 1
        return streamed(sseTurn({ text: reviewing ? 'Done.' : PROPOSAL }))
      },
      session: { testPrompt: 'Test the pack.', ...(review === undefined ? {} : { critic: review ? 'on' : 'off' }) }
    })
    expect(critic > 0).toBe(review === undefined)
    expect(ofType(events, 'critique')).toHaveLength(review === undefined ? 1 : 0)
    expect(ofType(events, 'critic_decided')).toEqual([review === undefined
      ? { type: 'critic_decided', mode: 'on', ran: true, reason: 'table', reasoning: 'off' }
      : { type: 'critic_decided', mode: 'off', ran: false, reason: 'off', reasoning: 'off' }])
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
      ...(review === undefined ? {} : { critic: review ? 'on' : 'off' }),
      agent: { model: 'agent-model', run }, signal: new AbortController().signal
    }, (event) => events.push(event))
    expect(phases).toEqual(review === undefined ? ['author', 'critic'] : ['author'])
    expect(ofType(events, 'critique')).toHaveLength(review === undefined ? 1 : 0)
  })
})
