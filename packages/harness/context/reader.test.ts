/**
 * One read, one result (agent#1).
 *
 * A reading tool that holds its window to `readWindowBytes` is answered whole:
 * its result never becomes a ledger reference and a preview the model has to
 * page back through `read_context`. A result that still exceeds the limit is
 * stubbed as before. And the ledger's own reader, `read`, pages a retained item
 * by line or page with a cursor, each window one result.
 */
import { describe, expect, it } from 'vitest'
import hostToolsVector from '../../../contract/vectors/tool-offer/host-tools.json'
import { normalize } from '../thinking'
import type { AssistantSession, HostTool } from '../engine'
import { CONTEXT_DEFAULTS, READ_ENVELOPE_BYTES, encodedPrefix, readWindowBytes, utf8Bytes, type ContextPolicy } from './policy'
import { READ_TOOL, type ReadResult } from './reader'
import { SessionContext } from './session'

function session(): AssistantSession {
  return {
    prompt: 'Current request', testPrompt: '', tools: [], hostTools: [], callTool: async () => ({ content: [] }),
    model: { family: 'openai-compatible', model: 'small', call: async () => { throw Error('No model call in reader unit tests') } },
    thinking: normalize('off', 'openai-compatible'), signal: new AbortController().signal
  }
}
const signal = new AbortController().signal

/** A page of text that costs more to encode than it is long: prose, escapes, emoji, a control character. */
const PAGE = Array.from({ length: 900 }, (_, i) =>
  `Line ${i}: a claim of "50 USD" or less is approved \\ unless 😀 é\t${i % 7 === 0 ? '\u0001' : ''}`).join('\n')

/**
 * A page reader shaped like the Desk's link reader: a short header, then one
 * window of the page's text, in a text result beside a small structured member.
 * `window` is the bound under test: the read window, or the Desk's old 20,000
 * characters.
 */
function pageReader(page: string, window: (text: string) => string): HostTool {
  return {
    name: 'read_page', effect: 'read', description: 'Read the fetched page', inputSchema: { type: 'object' },
    execute: async (args) => {
      const offset = Number(args.offset ?? 0)
      const text = window(page.slice(offset))
      const next = offset + text.length < page.length ? offset + text.length : null
      const header = `retrieved: https://example.org/policy\ntitle: Expense policy\n${page.length} characters; showing ${offset}–${offset + text.length}` +
        (next === null ? '' : ` (more: call read_page with offset ${next})`)
      return { content: [{ type: 'text', text: `${header}\n\n${text}` }], structuredContent: { pages: [1], nextOffset: next } }
    }
  }
}

describe('the read window', () => {
  it('is the result limit less the envelope, from the policy', () => {
    expect(readWindowBytes(CONTEXT_DEFAULTS)).toBe(CONTEXT_DEFAULTS.toolResultBytes - READ_ENVELOPE_BYTES)
    expect(readWindowBytes({ toolResultBytes: 30000 })).toBe(30000 - READ_ENVELOPE_BYTES)
  })

  it('cuts text to the longest prefix whose JSON encoding fits, escapes and pairs included', () => {
    const text = ('😀é\u0001"\\\n\t x\uD800').repeat(400)
    for (const bytes of [2, 3, 7, 64, 1000, 4097]) {
      const cut = encodedPrefix(text, bytes)
      expect(utf8Bytes(JSON.stringify(cut))).toBeLessThanOrEqual(bytes)
      // The longest: one more code point would not fit.
      const next = text.slice(0, cut.length + (text.codePointAt(cut.length)! > 0xffff ? 2 : 1))
      expect(utf8Bytes(JSON.stringify(next))).toBeGreaterThan(bytes)
      // A surrogate pair is never split.
      expect(/[\uD800-\uDBFF]$/.test(cut) && /^[\uDC00-\uDFFF]/.test(text.slice(cut.length))).toBe(false)
    }
    expect(encodedPrefix('short', 100)).toBe('short')
  })
})

describe('one read, one result', () => {
  it('answers a reading tool that honours readWindowBytes whole, with no stub, read after read', async () => {
    const context = new SessionContext(session())
    const reader = pageReader(PAGE, (text) => encodedPrefix(text, readWindowBytes(context.policy)))
    let offset: number | null = 0
    let read = ''
    let reads = 0
    while (offset !== null) {
      const answered = await reader.execute({ offset }, signal)
      const shown = await context.result(reader.name, answered)
      reads += 1
      // The model is shown what the tool answered, not a reference to it.
      expect(shown).toEqual(answered)
      expect(JSON.stringify(shown)).not.toContain('contextId')
      expect(utf8Bytes(JSON.stringify(shown))).toBeLessThanOrEqual(context.policy.toolResultBytes)
      const body = shown.content![0]!.text!
      read += body.slice(body.indexOf('\n\n') + 2)
      offset = (shown.structuredContent as { nextOffset: number | null }).nextOffset
    }
    expect(read).toBe(PAGE)
    // Every read was a page of the text, not a preview: nothing was retained to page back.
    expect(context.ledger.list().items).toHaveLength(0)
    expect(reads).toBeLessThanOrEqual(Math.ceil(utf8Bytes(JSON.stringify(PAGE)) / (readWindowBytes(context.policy) - 2)) + 1)
  })

  it('still stubs a result over the limit: a reference and a preview, the original retained', async () => {
    const context = new SessionContext(session())
    // The Desk's link reader before its swap: 20,000 characters a window.
    const reader = pageReader(PAGE, (text) => text.slice(0, 20000))
    const answered = await reader.execute({ offset: 0 }, signal)
    const shown = await context.result(reader.name, answered)
    const reference = shown.structuredContent as { contextId: string; readWith: string; preview: string }
    expect(reference).toMatchObject({ operationReturned: true, readWith: 'read_context' })
    expect(reference.preview.length).toBeLessThanOrEqual(1000)
    expect(utf8Bytes(JSON.stringify(shown))).toBeLessThanOrEqual(context.policy.toolResultBytes)
    expect(context.ledger.read({ id: reference.contextId, path: '/content/0/text', limit: 128 })).toMatchObject({ type: 'text' })
  })
})

describe('the ledger’s reader, read', () => {
  const tool = (context: SessionContext) => context.ledger.tools().find((held) => held.name === READ_TOOL)!
  const read = async (context: SessionContext, args: Record<string, unknown>) => {
    const shown = await context.result(READ_TOOL, await tool(context).execute(args, signal))
    expect(JSON.stringify(shown)).not.toContain('contextId')
    expect(utf8Bytes(JSON.stringify(shown))).toBeLessThanOrEqual(context.policy.toolResultBytes)
    return shown.structuredContent as ReadResult
  }

  it('pages a retained item by line with a cursor, each window one result, back to the exact text', async () => {
    const context = new SessionContext(session())
    // A line far longer than the window sits in the middle: it is read in part, across reads.
    const long = 'x"'.repeat(9000)
    const text = PAGE + '\n' + long + '\n' + PAGE
    const entry = await context.ledger.retain({ id: 'page', kind: 'tool', label: 'A long page', value: text })
    const lines = text.split('\n')
    let rebuilt = ''
    let args: Record<string, unknown> = { id: entry.id }
    let windows = 0
    for (;;) {
      const window = await read(context, args)
      windows += 1
      expect(window).toMatchObject({ type: 'read', id: entry.id, unit: 'line', total: lines.length })
      // A window that continues a line part way through carries no separator before it.
      rebuilt += (windows > 1 && window.column === undefined ? '\n' : '') + window.text
      if (window.more === null) break
      expect(window.more).toMatch(/^line:\d+(\+\d+)?$/)
      args = { id: entry.id, cursor: window.more }
    }
    expect(rebuilt).toBe(text)
    expect(windows).toBeGreaterThan(3)
    // The read results were answered whole: nothing new was retained to page back through.
    expect(context.ledger.list().items).toHaveLength(1)
  })

  it('reads a line range by offset and limit, and says where to read on', async () => {
    const context = new SessionContext(session())
    const entry = await context.ledger.retain({ id: 'lines', kind: 'request', label: 'Lines', value: 'zero\none\ntwo\nthree\nfour\nfive' })
    expect(await read(context, { id: entry.id, offset: 2, limit: 2 })).toEqual({
      type: 'read', id: entry.id, unit: 'line', offset: 2, count: 2, total: 6, text: 'two\nthree', more: 'line:4'
    })
    expect(await read(context, { id: entry.id, cursor: 'line:4' })).toMatchObject({ offset: 4, count: 2, text: 'four\nfive', more: null })
  })

  it('reads a paged document by page, pages separated by a form feed', async () => {
    const context = new SessionContext(session())
    const pages = ['Page one.', 'Page two.\nSecond line.', 'Page three.']
    const entry = await context.ledger.retain({ id: 'document', kind: 'artifact', label: 'An attachment', value: { pages } })
    expect(await read(context, { id: entry.id, unit: 'page', limit: 1 })).toMatchObject({ offset: 0, count: 1, total: 3, text: 'Page one.', more: 'page:1' })
    expect(await read(context, { id: entry.id, cursor: 'page:1' })).toMatchObject({ count: 2, text: 'Page two.\nSecond line.\fPage three.', more: null })
    expect(await read(context, { id: entry.id, offset: 1, limit: 2 })).toMatchObject({ unit: 'line', text: 'Page two.\nSecond line.' })
  })

  it('refuses what it cannot read, saying how to read it', async () => {
    const context = new SessionContext(session())
    const entry = await context.ledger.retain({ id: 'text', kind: 'request', label: 'Text', value: 'one\ntwo' })
    const refused = (args: Record<string, unknown>) => tool(context).execute(args, signal)
    await expect(refused({ id: entry.id, unit: 'page' })).rejects.toThrow(/no pages; read it by line/)
    await expect(refused({ id: entry.id, cursor: 'line:1', offset: 1 })).rejects.toThrow(/without unit or offset/)
    await expect(refused({ id: entry.id, cursor: 'line:one' })).rejects.toThrow(/cursor a previous read returned/)
    await expect(refused({ id: entry.id, offset: 9 })).rejects.toThrow(/holds 2 lines/)
    await expect(refused({ id: entry.id, limit: 0 })).rejects.toThrow(/limit from 1/)
    await expect(refused({ id: 'elsewhere' })).rejects.toThrow(/unavailable/)
  })

  it('is declared to the model exactly as the contract’s host-tool vector states it', () => {
    const context = new SessionContext(session())
    const { name, effect, contextPolicy, description, inputSchema } = tool(context)
    expect({ name, effect, contextPolicy, description, inputSchema }).toEqual(
      hostToolsVector.items.find((item) => item.name === READ_TOOL)
    )
  })

  it('holds its window to the policy it was given, not to the default', async () => {
    const policy: ContextPolicy = { ...CONTEXT_DEFAULTS, toolResultBytes: 3000 }
    const context = new SessionContext(session())
    Object.assign(context.policy, policy)
    const entry = await context.ledger.retain({ id: 'page', kind: 'tool', label: 'Page', value: PAGE })
    const window = await read(context, { id: entry.id })
    expect(utf8Bytes(JSON.stringify(window.text))).toBeLessThanOrEqual(readWindowBytes(policy))
    expect(window.more).not.toBeNull()
  })
})

