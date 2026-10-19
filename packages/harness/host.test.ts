import { describe, expect, it, vi } from 'vitest'
import inventory from '../../contract/desk/inventory.json'
import toolOffer from '../../contract/schemas/tool-offer.json'
import provenance from '../../contract/schemas/provenance.json'
import { ASSISTANT_ENGINES, ASSISTANT_TOOLS } from './host'

describe('the host contract values', () => {
  it('are the contract’s runtime tool ceiling, in its order: the Desk’s five, then the test tool (agent#7)', () => {
    expect([...ASSISTANT_TOOLS]).toEqual([...inventory.runtimeToolCeiling.values, 'experimental_test_cases'])
    expect([...ASSISTANT_TOOLS]).toEqual(toolOffer.$defs.runtimeCeiling.enum)
  })

  it('are the contract’s engine ids: the Desk’s two, then the one this package adds (ADR-0003)', () => {
    expect([...ASSISTANT_ENGINES]).toEqual([...inventory.engines.values, 'deepagents'])
    expect([...ASSISTANT_ENGINES]).toEqual(provenance.$defs.engine.enum)
  })
})

describe('an injection point the host has not supplied', () => {
  it('throws and names itself rather than guessing', async () => {
    // A fresh module, so the test ground's host is not in it.
    vi.resetModules()
    const fresh = await import('./host')
    expect(() => fresh.chassisUrl('/api')).toThrow(/has not supplied chassisUrl/)
    await expect(async () => fresh.sessionBearer()).rejects.toThrow(/has not supplied sessionBearer/)
    expect(() => new fresh.NoSession('ended')).toThrow(/has not supplied NoSession/)
    expect(() => fresh.renderImageToText({ kind: 'image', mediaType: 'image/png', bytes: 'QUJD' }, new AbortController().signal)).toThrow(/has not supplied renderImageToText/)
    fresh.configureHarness({ chassisUrl: (path) => `host:${path}` })
    expect(fresh.chassisUrl('/api')).toBe('host:/api')
  })

  it('answers nothing known for endpoint capabilities, until the host says more', async () => {
    vi.resetModules()
    const fresh = await import('./host')
    const endpoint = { family: 'anthropic' as const, model: 'a-model' }
    expect(fresh.endpointCapabilities(endpoint)).toEqual({})
    fresh.configureHarness({ endpointCapabilities: () => ({ strictAlternation: true }) })
    expect(fresh.endpointCapabilities(endpoint)).toEqual({ strictAlternation: true })
  })
})

describe('the models a run’s connection names (review of agent#35, row 5)', () => {
  it('answers none until the host says more, and then each connection for itself', async () => {
    vi.resetModules()
    const fresh = await import('./host')
    const run = { connection: { connectionId: 'first' }, engine: 'vercel' as const, family: 'openai-compatible' as const, model: 'a-model' }
    expect(fresh.runModels(run)).toEqual({ critic: 'a-model', child: 'a-model' })
    fresh.configureHarness({ modelsFor: (asked) => asked.connection?.connectionId === 'first' ? { critic: 'a-small-model', child: null } : {} })
    expect(fresh.runModels(run)).toEqual({ critic: 'a-small-model', child: 'a-model' })
    expect(fresh.runModels({ ...run, connection: { connectionId: 'second' } })).toEqual({ critic: 'a-model', child: 'a-model' })
  })

  it('refuses an answer that is not an object, and a member that is not a model name', async () => {
    vi.resetModules()
    const fresh = await import('./host')
    const run = { engine: 'codex' as const, model: 'an-agent-model' }
    for (const child of ['', 'two\nlines', 'm'.repeat(129), 7]) {
      fresh.configureHarness({ modelsFor: () => ({ child } as unknown as { child: string }) })
      expect(() => fresh.runModels(run)).toThrow('the host named a child model that is not a model name')
    }
    fresh.configureHarness({ modelsFor: () => 'a-model' as unknown as { child: string } })
    expect(() => fresh.runModels(run)).toThrow('the host answered modelsFor with something other than an object')
  })

  it('reads how many requests the connection serves at once, a whole number from 1 to 16 (agent#17)', async () => {
    vi.resetModules()
    const fresh = await import('./host')
    const run = { connection: { connectionId: 'first' }, engine: 'vercel' as const, family: 'openai-compatible' as const, model: 'a-model' }
    fresh.configureHarness({ modelsFor: (asked) => asked.connection?.connectionId === 'first' ? { concurrentRequests: 3 } : { concurrentRequests: null } })
    expect(fresh.runModels(run)).toEqual({ critic: 'a-model', child: 'a-model', concurrentRequests: 3 })
    expect(fresh.runModels({ ...run, connection: { connectionId: 'second' } })).toEqual({ critic: 'a-model', child: 'a-model' })
    for (const concurrentRequests of [0, 17, 2.5, '3']) {
      fresh.configureHarness({ modelsFor: () => ({ concurrentRequests } as unknown as { concurrentRequests: number }) })
      expect(() => fresh.runModels(run)).toThrow('concurrentRequests that is not a whole number from 1 to 16')
    }
  })
})
