import {expect,it} from 'vitest'
import {captureGlobal} from './captureGlobal'

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
