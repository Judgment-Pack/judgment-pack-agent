/**
 * What a write that reads alike a recorded one is to that record (#275). The published fingerprint does
 * not change; the decision is made from the exact text every row records, so a row a published release
 * wrote is judged by the same rule. contract/vectors/storage/durable-repeats.json states the cases, and
 * go/context/durable_writes_test.go holds the Go to the same ones.
 *
 * These hold the decision and the writer. storage/durableWrites.engines.test.ts holds a host write
 * through the real engines.
 */
import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { DurableRequestConflict, DurableRequestUnrecordable, HostOutcomeUnknown, RunWriter, compareDurableRequests, exactlyHeld, type DurableBinding } from './writer'
import { MemoryRunStore } from './run'
import { validDurable } from './durable'
import { RECORD_DEFAULTS, sha256 } from '../records'
import { canonicalJSON, exactArgumentsText } from '../context/chat'
import { readHistoryRequestJSON } from '../history/codec'
import type { ExecutionCheckpoint } from '../execution/state'

const vector = JSON.parse(readFileSync('../../contract/vectors/storage/durable-repeats.json', 'utf8')) as {
  cases: { label: string; recorded: string; request: string; decision: 'same' | 'different' | 'conflict' }[]
  goCases: { label: string; recorded: string; request: string; decision: 'same' | 'different' | 'conflict' }[]
  recordable: { text: string; recordable: boolean }[]
  resume: Record<string, { call: string; outcome: string; answer?: string }[]>
  items: { kind: string; scope: unknown; source: string; tool: string; fingerprint: string; request: string; requestDigest: string }[]
}
const published = (JSON.parse(readFileSync('../../contract/testdata/durable-writes/published-checkpoints.json', 'utf8')) as { checkpoints: Record<string, string> }).checkpoints
const exactCases = (JSON.parse(readFileSync('../../contract/vectors/chat/read-arguments.json', 'utf8')) as { cases: { label: string; text: string; go: { key?: string } }[] }).cases

const fingerprintOf = (id: string, text: string) => 'sha256:' + sha256(canonicalJSON({ kind: 'host', scope: { execution: id, role: 'author' }, source: 'source', tool: 'write', request: JSON.parse(text) }))
const checkpoint = (id: string): ExecutionCheckpoint => ({ version: 2, id, requestKey: 'a'.repeat(64), phase: 'work', status: 'running', unit: 'model-requests', used: 0, limit: 20, grants: [], plan: [], explanation: '', operations: [], context: { scope: 'fixture', entries: [] } })

/** A store with no history recorder, as v0.10.0 wrote under: it records what a later release cannot. */
const plainStore = () => {
  const store = new MemoryRunStore()
  return new Proxy(store, { get: (target, name) => { const value = Reflect.get(target, name) as unknown; return name === 'historyVersion' ? undefined : typeof value === 'function' ? value.bind(target) : value } })
}

async function setup(store: MemoryRunStore, id: string, resume = false) {
  const writer = new RunWriter(store, id, resume ? await store.load(id) : undefined), binding: DurableBinding = { writer, root: true, scope: { execution: id, role: 'author' } }
  writer.bind(() => writer.checkpoint ?? checkpoint(id), RECORD_DEFAULTS, () => undefined)
  const ran: string[] = []
  const write = (text: string, read = false, kind: 'host' | 'runtime' = 'host') => writer.invoke(binding, kind, 'source', 'write', text, read, async (raw) => { ran.push(raw); return { content: [{ type: 'text', text: 'ran ' + raw }] } })
  return { writer, binding, ran, write }
}
type Answer = { content: { text: string }[] }
const recordable = (text: string) => { try { readHistoryRequestJSON(text); return true } catch { return false } }

describe('the decision over two requests that read alike (#275)', () => {
  it.each(vector.cases)('$label: $decision', async ({ recorded, request, decision }) => {
    expect(canonicalJSON(JSON.parse(recorded)), 'the two texts read alike').toBe(canonicalJSON(JSON.parse(request)))
    expect(compareDurableRequests(recorded, request)).toBe(decision)
    // Through the writer; what this release cannot record is put in the book as a published release left it.
    const { writer, ran, write } = await setup(plainStore(), 'repeats')
    if (!recordable(recorded)) {
      const result = { content: [{ type: 'text', text: 'ran ' + recorded }] }
      ;(writer as unknown as { book: { dispatches: unknown[] } }).book.dispatches.push({ id: 'published-row', scope: { execution: 'repeats', role: 'author' }, kind: 'host', source: 'source', tool: 'write', fingerprint: fingerprintOf('repeats', recorded), request: recorded, requestDigest: 'sha256:' + sha256(recorded), state: 'result', read: false, result })
      ran.push(recorded)
    } else await write(recorded)
    const outcome = write(request)
    if (decision === 'same') {
      expect(((await outcome) as Answer).content[0]!.text).toBe('ran ' + recorded)
      expect(ran).toEqual([recorded])
    } else if (decision === 'conflict') {
      await expect(outcome).rejects.toBeInstanceOf(DurableRequestConflict)
      expect(ran).toEqual([recorded])
    } else if (!recordable(request)) {
      await expect(outcome).rejects.toBeInstanceOf(DurableRequestUnrecordable)
      expect(ran).toEqual([recorded])
    } else {
      expect(((await outcome) as Answer).content[0]!.text).toBe('ran ' + request)
      expect(ran).toEqual([recorded, request])
    }
  })

  it('cannot say what text it cannot read is', () => {
    expect(compareDurableRequests('{bad', '{worse')).toBe('conflict')
    expect(compareDurableRequests('{bad', '{bad')).toBe('same')
  })

  it.each(vector.recordable)('$text: recordable $recordable', async ({ text, recordable: held }) => {
    for (const kind of ['host', 'runtime'] as const) for (const [read, store] of [[false, new MemoryRunStore()], [true, new MemoryRunStore()], [false, plainStore()], [true, plainStore()]] as const) {
      const { ran, write, writer } = await setup(store, 'recordable')
      if (held) {
        await write(text, read, kind)
        expect(ran).toEqual([text])
      } else {
        await expect(write(text, read, kind)).rejects.toBeInstanceOf(DurableRequestUnrecordable)
        expect(ran).toEqual([])
        expect(writer.checkpoint?.durable?.dispatches ?? []).toEqual([])
        // The writer goes on: the next call runs and is recorded.
        await write('{"next":1}', read, kind)
        expect(ran).toEqual(['{"next":1}'])
      }
    }
  })

  // JSON.parse keeps a lone surrogate, so these never share a fingerprint here; the text twin holds Go's rule all the same.
  it.each(vector.goCases.filter((row) => row.decision !== 'same'))('$label: not one write, though Go reads them alike', ({ recorded, request }) => {
    expect(compareDurableRequests(recorded, request)).not.toBe('same')
    expect(canonicalJSON(JSON.parse(recorded)), 'JSON.parse keeps them apart').not.toBe(canonicalJSON(JSON.parse(request)))
  })

  it('reads a book closed: a row with another fingerprint does not read', () => {
    const book = (JSON.parse(published['v0.12.0']!) as { durable: { dispatches: { kind: string; fingerprint: string }[] } }).durable
    expect(validDurable(book)).toBe(true)
    const row = book.dispatches.find((held) => held.kind === 'host')!, honest = row.fingerprint
    row.fingerprint = 'sha256:' + 'a'.repeat(64)
    expect(validDurable(book)).toBe(false)
    row.fingerprint = honest
    expect(validDurable(book)).toBe(true)
  })

  it('reads the exactness of text as Go does: every row of the read-argument vector', () => {
    for (const row of exactCases.filter((item) => item.text !== '')) expect(exactArgumentsText(row.text), row.label).toBe(row.go.key !== 'none')
  })

  it('keeps the fingerprint its published form: the digests the rows carry', () => {
    for (const row of vector.items) {
      const value = JSON.parse(row.request) as unknown
      const digest = 'sha256:' + sha256(canonicalJSON({ kind: row.kind, scope: row.scope, source: row.source, tool: row.tool, request: value }))
      expect(digest, row.request).toBe(row.fingerprint)
      expect('sha256:' + sha256(row.request), row.request).toBe(row.requestDigest)
    }
  })

  it('names both refusals as a mistake in the input, and says nothing ran', () => {
    for (const [name, error] of [['DurableRequestConflict', new DurableRequestConflict()], ['DurableRequestUnrecordable', new DurableRequestUnrecordable()]] as const) {
      expect(error.name).toBe(name)
      expect(error.failure).toEqual(expect.objectContaining({ code: 'invalid-tool-input', source: 'tool', outcome: 'not-started' }))
      expect(error.failure.detail?.startsWith(name + ': ')).toBe(true)
    }
  })

  it('tells what JSON.parse can have held exactly', () => {
    for (const held of [{ id: 9007199254740991 }, { q: 1.5 }, { q: -0 }, { q: 1e300 * 0 }, { a: [1, { b: 'x' }] }, null, 'x', true]) expect(exactlyHeld(held), JSON.stringify(held)).toBe(true)
    for (const lost of [{ id: 9007199254740992 }, { id: -9007199254740992 }, { q: 1e21 }, { q: Infinity }, { q: -Infinity }, { q: Number.NaN }, { a: [Infinity] }, { a: { b: 1e21 } }, [9007199254740992]]) expect(exactlyHeld(lost), JSON.stringify(lost)).toBe(false)
  })
})

describe('a resume over a checkpoint a published release wrote (#275)', () => {
  it.each([['v0.12.0', 'history store'], ['v0.10.0', 'history store'], ['v0.12.0', 'plain store'], ['v0.10.0', 'plain store']] as const)('%s on a %s: reads, judges each row by the bytes it recorded, and runs no write twice', async (tag, kind) => {
    const store = kind === 'plain store' ? plainStore() : new MemoryRunStore()
    await store.commit({ runId: 'published-writes', expectedRevision: null, commitId: 'published', checkpoint: new TextEncoder().encode(published[tag]!), events: [] })
    const { ran, write, writer } = await setup(store, 'published-writes', true)
    for (const row of writer.checkpoint!.durable!.dispatches) expect(row.fingerprint).toBe(fingerprintOf('published-writes', row.request))
    const want: string[] = []
    for (const [at, call] of vector.resume[tag]!.entries()) {
      const label = `call ${at + 1}, ${call.call}`
      const outcome = write(call.call)
      if (call.outcome === 'run' || call.outcome === 'record') {
        expect(((await outcome) as Answer).content[0]!.text, label).toBe(call.answer)
        if (call.outcome === 'run') want.push(call.call)
      } else await expect(outcome, label).rejects.toBeInstanceOf({ conflict: DurableRequestConflict, unrecordable: DurableRequestUnrecordable, unknown: HostOutcomeUnknown }[call.outcome]!)
      expect(ran, label).toEqual(want)
    }
    expect(want.length).toBeGreaterThan(0)
    // Resumed again: everything that ran has its record, and nothing runs a second time.
    const again = await setup(store, 'published-writes', true)
    for (const call of vector.resume[tag]!) await again.write(call.call).catch(() => undefined)
    expect(again.ran).toEqual([])
  })
})
