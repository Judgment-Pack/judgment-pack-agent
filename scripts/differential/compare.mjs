import assert from 'node:assert/strict'
function bytes(left, right, path) {
  const a = Buffer.from(left), b = Buffer.from(right)
  let at = 0
  while (at < Math.min(a.length, b.length) && a[at] === b[at]) at++
  return `${path}: first byte=${at}, lengths=${a.length}/${b.length}, TS hex=${a.subarray(Math.max(0, at - 12), at + 24).toString('hex')}, Go hex=${b.subarray(Math.max(0, at - 12), at + 24).toString('hex')}`
}
/** Strings containing prompt and wire bytes are compared without parsing or rewriting them. */
export function compare(left, right, id) {
  try { assert.deepStrictEqual(left, right) } catch {
    const differences = []
    for (const name of ['prompt', 'encoded', 'text', 'finishReason', 'usage', 'events', 'replay']) {
      if (JSON.stringify(left[name]) !== JSON.stringify(right[name])) differences.push(name)
    }
    const details = []
    if (left.prompt !== right.prompt) details.push(bytes(left.prompt, right.prompt, 'prompt'))
    for (const key of ['encoded', 'replay']) if (left[key]?.body !== right[key]?.body) details.push(bytes(left[key]?.body ?? '', right[key]?.body ?? '', `${key}.body`))
    throw new Error(`${id}: differing ${differences.join(', ')}; ${details.join('; ')}; decoded TS=${JSON.stringify({ text: left.text, finishReason: left.finishReason, usage: left.usage, events: left.events }).slice(0, 500)}; decoded Go=${JSON.stringify({ text: right.text, finishReason: right.finishReason, usage: right.usage, events: right.events }).slice(0, 500)}`)
  }
}
