/** Capture a scripted transport synchronously, then restore the exact host global before any await. */
export function captureGlobal<T>(key: 'fetch' | 'WebSocket', value: unknown, capture: () => T): T {
  const original = Object.getOwnPropertyDescriptor(globalThis, key)
  try {
    Object.defineProperty(globalThis, key, { configurable: true, writable: true, value })
    return capture()
  } finally {
    if (original) Object.defineProperty(globalThis, key, original)
    else Reflect.deleteProperty(globalThis, key)
  }
}
