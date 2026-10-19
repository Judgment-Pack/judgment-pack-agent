import { describe, expect, it, vi } from 'vitest'
import inventory from '../../contract/desk/inventory.json'
import toolOffer from '../../contract/schemas/tool-offer.json'
import { ASSISTANT_ENGINES, ASSISTANT_TOOLS } from './host'

describe('the host contract values', () => {
  it('are the contract’s runtime tool ceiling, in its order: the Desk’s five, then the test tool (agent#7)', () => {
    expect([...ASSISTANT_TOOLS]).toEqual([...inventory.runtimeToolCeiling.values, 'experimental_test_cases'])
    expect([...ASSISTANT_TOOLS]).toEqual(toolOffer.$defs.runtimeCeiling.enum)
  })

  it('are the contract’s engine ids', () => {
    expect([...ASSISTANT_ENGINES]).toEqual(inventory.engines.values)
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
