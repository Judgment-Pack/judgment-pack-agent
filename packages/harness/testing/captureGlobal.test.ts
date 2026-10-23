import {expect,it} from 'vitest'
import {captureGlobal} from './captureGlobal'
import {bindModelCall} from '../session'
import {bindAgentRun} from '../agentTransport'
import type {ModelCall} from '../engine'
import {codexTurn,vercelTurn} from './scripted'
import {answerWith,runOnModelEngine} from './scenarioDriver'

it('restores the exact data descriptor after capturing a transport', () => {
  const original = Object.getOwnPropertyDescriptor(globalThis, 'fetch')!
  try {
    Object.defineProperty(globalThis, 'fetch', {value: original.value, configurable: true, enumerable: false, writable: false})
    const before = Object.getOwnPropertyDescriptor(globalThis, 'fetch')
    const scripted = (() => Promise.resolve(new Response('scripted'))) as typeof fetch
    expect(captureGlobal('fetch', scripted, () => globalThis.fetch)).toBe(scripted)
    expect(Object.getOwnPropertyDescriptor(globalThis, 'fetch')).toEqual(before)
  } finally {
    Object.defineProperty(globalThis, 'fetch', original)
  }
})

it('restores the exact accessor descriptor when binding throws', () => {
  const original = Object.getOwnPropertyDescriptor(globalThis, 'fetch')!
  try {
    const before = {get: () => original.value as typeof fetch, set: () => {}, configurable: true, enumerable: true}
    Object.defineProperty(globalThis, 'fetch', before)
    const failure = new Error('binding failed')
    expect(() => captureGlobal('fetch', () => {}, () => {throw failure})).toThrow(failure)
    expect(Object.getOwnPropertyDescriptor(globalThis, 'fetch')).toEqual(before)
  } finally {
    Object.defineProperty(globalThis, 'fetch', original)
  }
})

it('removes a temporary global that was originally absent', () => {
  const original = Object.getOwnPropertyDescriptor(globalThis, 'WebSocket')
  try {
    Reflect.deleteProperty(globalThis, 'WebSocket')
    const scripted = class {}
    expect(captureGlobal('WebSocket', scripted, () => globalThis.WebSocket)).toBe(scripted)
    expect(Object.hasOwn(globalThis, 'WebSocket')).toBe(false)
    expect(() => captureGlobal('WebSocket', scripted, () => {throw Error('binding failed')})).toThrow('binding failed')
    expect(Object.hasOwn(globalThis, 'WebSocket')).toBe(false)
  } finally {
    if (original) Object.defineProperty(globalThis, 'WebSocket', original)
  }
})

it('keeps independently captured transports separate without mutating globals during asynchronous work', async () => {
  const before = Object.getOwnPropertyDescriptor(globalThis, 'fetch')
  const first = captureGlobal('fetch', () => Promise.resolve(new Response('first')), () => globalThis.fetch.bind(globalThis))
  const second = captureGlobal('fetch', () => Promise.resolve(new Response('second')), () => globalThis.fetch.bind(globalThis))
  expect(Object.getOwnPropertyDescriptor(globalThis, 'fetch')).toEqual(before)
  const [a, b] = await Promise.all([first('ignored'), second('ignored')])
  expect(await a.text()).toBe('first')
  expect(await b.text()).toBe('second')
  expect(Object.getOwnPropertyDescriptor(globalThis, 'fetch')).toEqual(before)
})

// These fixtures accept recording/decorating transports that bind synchronously
// on invocation. Their returned promise must never extend the global override.


for (const fixture of ['turn', 'scenario'] as const) {
  const run = (label: string, call?: ModelCall) => {
    const answer = () => answerWith({say: label}, 'stream', 0)
    const session = {critic: 'off' as const, ...(call ? {model: {family: 'openai-compatible' as const, model: 'scripted-model', call}} : {})}
    return fixture === 'turn' ? vercelTurn({answer, session}) : runOnModelEngine(answer, {session})
  }

  it(`${fixture}: restores fetch while a custom call is pending and keeps the default call independent`, async () => {
    const before = Object.getOwnPropertyDescriptor(globalThis, 'fetch')
    let entered!: () => void
    const invoked = new Promise<void>(resolve => {entered = resolve})
    let release!: () => void
    const pending = new Promise<void>(resolve => {release = resolve})
    let captured: typeof fetch | undefined
    const custom = run('CUSTOM', (suffix, request) => {
      captured = globalThis.fetch
      const bound = bindModelCall('openai-compatible')
      entered()
      return pending.then(() => bound(suffix, request))
    })
    try {
      await invoked
      expect(captured).not.toBe(before?.value)
      expect(Object.getOwnPropertyDescriptor(globalThis, 'fetch')).toEqual(before)
      const ordinary = await run('DEFAULT')
      expect(ordinary.events.filter(event => event.type === 'error')).toEqual([])
      expect(ordinary.events.some(event => event.type === 'message' && event.text === 'DEFAULT')).toBe(true)
      expect(ordinary.requests).toHaveLength(1)
      expect(Object.getOwnPropertyDescriptor(globalThis, 'fetch')).toEqual(before)
    } finally {
      release()
      await custom
    }
    const recorded = await custom
    expect(recorded.events.filter(event => event.type === 'error')).toEqual([])
    expect(recorded.events.some(event => event.type === 'message' && event.text === 'CUSTOM')).toBe(true)
    expect(recorded.requests).toHaveLength(1)
    expect(Object.getOwnPropertyDescriptor(globalThis, 'fetch')).toEqual(before)
  })

  it(`${fixture}: restores fetch when a custom call throws synchronously`, async () => {
    const before = Object.getOwnPropertyDescriptor(globalThis, 'fetch')
    let captured: typeof fetch | undefined
    const result = await run('unused', () => {
      captured = globalThis.fetch
      throw Error('expected custom call failure')
    })
    expect(captured).not.toBe(before?.value)
    expect(result.events.some(event => event.type === 'error')).toBe(true)
    expect(Object.getOwnPropertyDescriptor(globalThis, 'fetch')).toEqual(before)
  })
}

it('restores WebSocket while a custom agent run is pending and keeps the default run independent', async () => {
  const before = Object.getOwnPropertyDescriptor(globalThis, 'WebSocket')
  let entered!: () => void
  const invoked = new Promise<void>(resolve => {entered = resolve})
  let release!: () => void
  const pending = new Promise<void>(resolve => {release = resolve})
  let captured: typeof WebSocket | undefined
  const script = (label: string): Parameters<typeof codexTurn>[0]['script'] => async bridge => {
    bridge.started()
    bridge.say(label)
    bridge.end()
  }
  const custom = codexTurn({script: script('CUSTOM'), session: {critic: 'off', agent: {model: 'scripted-agent', run: (request, callbacks) => {
    captured = globalThis.WebSocket
    const bound = bindAgentRun('scripted-agent')
    entered()
    return pending.then(() => bound(request, callbacks))
  }}}})
  try {
    await invoked
    expect(captured).not.toBe(before?.value)
    expect(Object.getOwnPropertyDescriptor(globalThis, 'WebSocket')).toEqual(before)
    const ordinary = await codexTurn({script: script('DEFAULT'), session: {critic: 'off'}})
    expect(ordinary.events.filter(event => event.type === 'error')).toEqual([])
    expect(ordinary.events.some(event => event.type === 'message' && event.text === 'DEFAULT')).toBe(true)
    expect(ordinary.runs).toHaveLength(1)
    expect(Object.getOwnPropertyDescriptor(globalThis, 'WebSocket')).toEqual(before)
  } finally {
    release()
    await custom
  }
  const recorded = await custom
  expect(recorded.events.filter(event => event.type === 'error')).toEqual([])
  expect(recorded.events.some(event => event.type === 'message' && event.text === 'CUSTOM')).toBe(true)
  expect(recorded.runs).toHaveLength(1)
  expect(Object.getOwnPropertyDescriptor(globalThis, 'WebSocket')).toEqual(before)
})

it('restores WebSocket when a custom agent run throws synchronously', async () => {
  const before = Object.getOwnPropertyDescriptor(globalThis, 'WebSocket')
  let captured: typeof WebSocket | undefined
  const result = await codexTurn({script: async () => {}, session: {critic: 'off', agent: {model: 'scripted-agent', run: () => {
    captured = globalThis.WebSocket
    throw Error('expected custom agent failure')
  }}}})
  expect(captured).not.toBe(before?.value)
  expect(result.events.some(event => event.type === 'error')).toBe(true)
  expect(Object.getOwnPropertyDescriptor(globalThis, 'WebSocket')).toEqual(before)
})
