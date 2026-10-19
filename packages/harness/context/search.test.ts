/**
 * search (agent#5): a regular expression over what the execution retains, in
 * the subset RE2 accepts, matched in linear time; each match located and shown
 * with its numbered lines; held to one result, with a cursor. And the reader's
 * default window of about two hundred numbered lines.
 */
import { describe, expect, it } from 'vitest'
import hostToolsVector from '../../../contract/vectors/tool-offer/host-tools.json'
import toolResult from '../../../contract/schemas/tool-result.json'
import searchVector from '../../../contract/vectors/tool-result/search.json'
import imageReadVector from '../../../contract/vectors/tool-result/image-read.json'
import attachmentsVector from '../../../contract/vectors/tool-result/attachments.json'
import { normalize } from '../thinking'
import { PATTERN_LENGTH, PatternError, compilePattern, firstMatch, folded } from './pattern'
import { SEARCH_NOT_SHOWN, SEARCH_STEPS, SEARCH_TOOL, searchInputSchema, type SearchResult } from './search'
import { READ_TOOL, READ_WINDOW_LINES, type ReadResult } from './reader'
import { SessionContext } from './session'
import { readWindowBytes, utf8Bytes } from './policy'
import type { AssistantSession, HostTool } from '../engine'

function session(): AssistantSession {
  return {
    prompt: 'Current request', testPrompt: '', tools: [], hostTools: [], callTool: async () => ({ content: [] }),
    model: { family: 'openai-compatible', model: 'small', call: async () => { throw Error('No model call in search unit tests') } },
    thinking: normalize('off', 'openai-compatible'), signal: new AbortController().signal
  }
}
const signal = new AbortController().signal
const tool = (context: SessionContext, name: string): HostTool => context.ledger.tools().find((held) => held.name === name)!
/** One search, as the model is shown it: one result, within the limit, never a reference. */
async function search(context: SessionContext, args: Record<string, unknown>): Promise<SearchResult> {
  const shown = await context.result(SEARCH_TOOL, await tool(context, SEARCH_TOOL).execute(args, signal))
  expect(JSON.stringify(shown)).not.toContain('contextId')
  expect(utf8Bytes(JSON.stringify(shown))).toBeLessThanOrEqual(context.policy.toolResultBytes)
  return shown.structuredContent as SearchResult
}
const refused = (context: SessionContext, args: Record<string, unknown>) => tool(context, SEARCH_TOOL).execute(args, signal)

const POLICY = ['Expense policy', 'A claim of 50 USD or less is approved.', 'A claim over 50 USD needs a receipt.', 'Travel is reimbursed at cost.', 'Meals are capped at 30 USD a day.'].join('\n')
const CLAIMS = ['Claims this month', 'claim 1: 42 USD, lunch', 'claim 2: 180 USD, hotel', 'claim 3: 12 EUR, taxi'].join('\n')

describe('search', () => {
  it('finds a pattern across two items, each match located, numbered and shown with its lines', async () => {
    const context = new SessionContext(session())
    const policy = await context.ledger.retain({ id: 'policy', kind: 'artifact', label: 'The policy', value: POLICY })
    const claims = await context.ledger.retain({ id: 'claims', kind: 'artifact', label: 'The claims', value: CLAIMS })
    const found = await search(context, { pattern: '\\b\\d+ USD\\b' })
    expect(found.matches.map(({ id, line, offset, column }) => ({ id, line, offset, column }))).toEqual([
      { id: policy.id, line: 2, offset: 1, column: 11 },
      { id: policy.id, line: 3, offset: 2, column: 13 },
      { id: policy.id, line: 5, offset: 4, column: 20 },
      { id: claims.id, line: 2, offset: 1, column: 9 },
      { id: claims.id, line: 3, offset: 2, column: 9 }
    ])
    // Two lines either side, numbered as read numbers them, and the item's label.
    expect(found.matches[0]).toMatchObject({ label: 'The policy', text: '1\tExpense policy\n2\tA claim of 50 USD or less is approved.\n3\tA claim over 50 USD needs a receipt.\n4\tTravel is reimbursed at cost.' })
    expect(found.matches[4]!.text).toBe('1\tClaims this month\n2\tclaim 1: 42 USD, lunch\n3\tclaim 2: 180 USD, hotel\n4\tclaim 3: 12 EUR, taxi')
    expect(found.more).toBeNull()
    // The line a match names is the line read shows under that number.
    const read = await tool(context, READ_TOOL).execute({ id: claims.id, offset: found.matches[4]!.offset, limit: 1 }, signal)
    expect((read.structuredContent as ReadResult).text).toBe('3\tclaim 2: 180 USD, hotel')
  })

  it('searches one item by id, or only the attachments the host retained, and ignores case where asked', async () => {
    const context = new SessionContext(session())
    // The host's documents for the run: what the boundary retains as attachments.
    await context.initialize([{ id: 'claims', kind: 'artifact', label: 'The claims', value: CLAIMS }], true)
    const policy = await context.ledger.retain({ id: 'policy', kind: 'artifact', label: 'The policy', value: POLICY })
    const attached = await search(context, { pattern: 'claim', scope: 'attachments' })
    expect(new Set(attached.matches.map((match) => match.label))).toEqual(new Set(['The claims']))
    expect(attached.scope).toBe('attachments')
    const one = await search(context, { pattern: 'claim', id: policy.id })
    expect(one.matches.map((match) => match.line)).toEqual([2, 3])
    expect(one.scope).toEqual({ id: policy.id })
    const caseless = await search(context, { pattern: 'CLAIM \\d', ignoreCase: true })
    expect(caseless.matches.map((match) => match.line)).toEqual([2, 3, 4])
  })

  it('names the page a match is on in a paged item', async () => {
    const context = new SessionContext(session())
    await context.ledger.retain({ id: 'document', kind: 'artifact', label: 'An attachment', value: { pages: ['Page one.\nNo match.', 'Page two: the receipt.', 'Page three.\nAnother receipt.'] } })
    const found = await search(context, { pattern: 'receipt' })
    expect(found.matches.map(({ line, page }) => ({ line, page }))).toEqual([{ line: 3, page: 2 }, { line: 5, page: 3 }])
  })

  it('stops at its limit or at one result, and its cursor goes on from exactly the next match', async () => {
    const context = new SessionContext(session())
    const lines = Array.from({ length: 300 }, (_, i) => `row ${i}: ${i % 3 === 0 ? 'MATCH' : 'none'} ${'pad '.repeat(30)}`)
    await context.ledger.retain({ id: 'rows', kind: 'tool', label: 'Rows', value: lines.join('\n') })
    const first = await search(context, { pattern: 'MATCH', limit: 2 })
    expect(first.matches.map((match) => match.line)).toEqual([1, 4])
    expect(first.more).toMatch(/^0:6:[0-9a-f]{8}$/)
    const second = await search(context, { pattern: 'MATCH', limit: 2, cursor: first.more })
    expect(second.matches.map((match) => match.line)).toEqual([7, 10])
    // Without a limit it stops where one more match would not fit in one result.
    const all: number[] = []
    let cursor: string | null | undefined
    let searches = 0
    do {
      const found: SearchResult = await search(context, { pattern: 'MATCH', limit: 100, ...(cursor ? { cursor } : {}) })
      all.push(...found.matches.map((match) => match.line))
      cursor = found.more
      searches += 1
    } while (cursor)
    expect(all).toEqual(lines.flatMap((_, i) => (i % 3 === 0 ? [i + 1] : [])))
    expect(searches).toBeGreaterThan(1)
    // A cursor belongs to its own search.
    await expect(refused(context, { pattern: 'none', cursor: first.more })).rejects.toThrow(/belongs to another search/)
  })

  it('refuses what RE2 refuses, a pattern over the bound, and arguments it does not take', async () => {
    const context = new SessionContext(session())
    await context.ledger.retain({ id: 'policy', kind: 'artifact', label: 'The policy', value: POLICY })
    await expect(refused(context, { pattern: '(USD) \\1' })).rejects.toThrow(/backreferences are not supported/)
    await expect(refused(context, { pattern: '(?<cur>USD) \\k<cur>' })).rejects.toThrow(/backreferences are not supported/)
    await expect(refused(context, { pattern: 'claim(?= of)' })).rejects.toThrow(/lookahead and lookbehind are not supported/)
    await expect(refused(context, { pattern: '(?<!over )50' })).rejects.toThrow(/lookahead and lookbehind are not supported/)
    await expect(refused(context, { pattern: '\\p{L}+' })).rejects.toThrow(/Unicode property classes/)
    await expect(refused(context, { pattern: '(?i)claim' })).rejects.toThrow(/inline flags/)
    await expect(refused(context, { pattern: 'a'.repeat(PATTERN_LENGTH + 1) })).rejects.toThrow(`at most ${PATTERN_LENGTH} characters`)
    await expect(refused(context, { pattern: 'a{1001}' })).rejects.toThrow(/at most 1000/)
    await expect(refused(context, { pattern: '(abc){999}' })).rejects.toThrow(/steps to compile/)
    await expect(refused(context, { pattern: '[x]a{0,998}b{0,10}' })).rejects.toThrow(/instructions/)
    await expect(refused(context, { pattern: 'claim', id: 'x', scope: 'all' })).rejects.toThrow(/not both/)
    await expect(refused(context, { pattern: 'claim', regex: true })).rejects.toThrow(/does not take "regex"/)
    await expect(refused(context, { pattern: 'claim', limit: 101 })).rejects.toThrow(/limit from 1 to 100/)
    // At the bound itself, a pattern is taken.
    await expect(refused(context, { pattern: 'a'.repeat(PATTERN_LENGTH) })).resolves.toBeDefined()
  })

  it('refuses nested repetitions of an empty group in milliseconds, charging every step of compiling', async () => {
    const context = new SessionContext(session())
    await context.ledger.retain({ id: 'policy', kind: 'artifact', label: 'The policy', value: POLICY })
    for (const pattern of ['((((){1000}){1000}){1000}){1000}', '(((){1000}){1000}){1000}', '(?:(?:(?:){1000}){1000}){1000}']) {
      const started = performance.now()
      await expect(refused(context, { pattern }), pattern).rejects.toThrow(/steps to compile/)
      expect(performance.now() - started, pattern).toBeLessThan(50)
    }
    // A repetition of nothing collapses to nothing: it compiles to no instruction.
    expect(compilePattern('(?:){1000}claim').ops.length).toBe(compilePattern('claim').ops.length)
    expect(compilePattern('a{1000}').ops.length).toBe(1001)
  })

  it('charges each line its setup, so millions of short lines stop at the bound in a stated time', async () => {
    const context = new SessionContext(session())
    const entry = await context.ledger.retain({ id: 'empty', kind: 'tool', label: 'Three million empty lines', value: '\n'.repeat(2_999_999) })
    // 1,998 instructions, and no literal a line must contain.
    const started = performance.now()
    const stopped = await search(context, { pattern: '[x]a{0,998}', id: entry.id })
    const elapsed = performance.now() - started
    expect(stopped.note).toContain('work bound')
    // Charged the program's size per line, a search reads a few thousand of them, not millions.
    expect(Number(stopped.more!.split(':')[1])).toBeLessThan(10_000)
    expect(elapsed).toBeLessThan(3000)
  })

  it('passes over an image, and stops at its work bound with a cursor rather than stopping the page', async () => {
    const context = new SessionContext(session())
    await context.ledger.retain({ id: 'scan', kind: 'artifact', label: 'A scan', value: { kind: 'image', mediaType: 'image/png', bytes: 'QUJD' } })
    const long = Array.from({ length: 40 }, () => 'a'.repeat(20_000)).join('\n')
    await context.ledger.retain({ id: 'long', kind: 'tool', label: 'Long lines', value: long })
    const started = performance.now()
    // A class rather than a literal at the end, so no line is passed over unread.
    const stopped = await search(context, { pattern: '[a-z]*[a-z]*[a-z]*[q!]' })
    expect(performance.now() - started).toBeLessThan(5000)
    expect(stopped.matches).toEqual([])
    expect(stopped.note).toContain('work bound')
    // The image is not among the items a search reads: the long item is the first.
    expect(stopped.more).toMatch(/^0:\d+:/)
    // And it goes on from there.
    const next = await search(context, { pattern: '[a-z]*[a-z]*[a-z]*[q!]', cursor: stopped.more })
    expect(Number(next.more?.split(':')[1] ?? 40)).toBeGreaterThan(Number(stopped.more!.split(':')[1]))
  })

  it('refuses a pattern that cannot read one line within its bound', async () => {
    const context = new SessionContext(session())
    const entry = await context.ledger.retain({ id: 'one', kind: 'tool', label: 'One line', value: 'a'.repeat(1_000_000) })
    await expect(refused(context, { pattern: '[a-z]*[a-z]*[a-z]*[a-z]*[q!]', id: entry.id })).rejects.toThrow(/more work than one search does on line 1/)
  })

  it('takes exactly the arguments the contract states', () => {
    const { description, ...schema } = toolResult.$defs.searchArguments
    expect(description).toContain('exactly the input schema')
    expect(description).toContain('just before a line’s final carriage return')
    expect(tool(new SessionContext(session()), SEARCH_TOOL).description).toContain('just before a line’s final carriage return')
    expect(schema).toEqual(searchInputSchema())
  })

  it('answers what the contract’s vectors state, rendered again from the code', async () => {
    const context = new SessionContext(session())
    context.ledger.scope = '7b3e1c52-0d6f-4a8e-9c21-5f4d2a6b8e90'
    await context.initialize([attachmentsVector.items[1]!].map((item) => ({ ...item, kind: 'artifact' as const, id: 'receipt-scan', label: 'A scanned receipt' })), true)
    await context.ledger.retain({ id: 'policy', kind: 'artifact', label: 'The policy', value: POLICY })
    expect([
      (await tool(context, SEARCH_TOOL).execute({ pattern: '\\b\\d+ USD\\b', limit: 3 }, signal)).structuredContent,
      (await tool(context, SEARCH_TOOL).execute({ pattern: 'usd', ignoreCase: true, scope: 'attachments' }, signal)).structuredContent
    ]).toEqual(searchVector.items.slice(0, 2))
    const image = attachmentsVector.items[2]!
    await context.ledger.retain({ ...image, kind: 'artifact', id: 'receipt-scan', label: 'A scanned receipt' })
    const entry = context.ledger.list().items.find((item) => item.label === 'A scanned receipt' && item.contextId.endsWith(imageReadVector.items[0]!.id.slice(-16)))!
    expect((await tool(context, READ_TOOL).execute({ id: entry.contextId }, signal)).structuredContent).toEqual(imageReadVector.items[0])
    // The hand-written items: a GIF read, and a search that names it.
    const gif = await context.ledger.retain({ id: 'receipt-gif', kind: 'artifact', label: 'A receipt sent as a GIF', value: { kind: 'image', mediaType: 'image/gif', bytes: 'R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7' } })
    const read = await tool(context, READ_TOOL).execute({ id: gif.id }, signal)
    expect(read.structuredContent).toEqual(imageReadVector.items[1])
    expect(read.content).toEqual([{ type: 'text', text: imageReadVector.items[1]!.notShown }])
    expect((await tool(context, SEARCH_TOOL).execute({ pattern: 'Hotel' }, signal)).structuredContent).toEqual(searchVector.items[2])
  })

  it('is declared to the model exactly as the contract’s host-tool vector states it', () => {
    const context = new SessionContext(session())
    const { name, effect, contextPolicy, description, inputSchema } = tool(context, SEARCH_TOOL)
    expect({ name, effect, ...(contextPolicy ? { contextPolicy } : {}), description, inputSchema }).toEqual(
      hostToolsVector.items.find((item) => item.name === SEARCH_TOOL)
    )
  })
})

describe('the pattern', () => {
  /** Patterns in the subset, and lines that do and do not match them, checked against the browser's own engine. */
  const PATTERNS = ['USD', '^A claim', 'approved\\.$', '\\d+ (USD|EUR)', '[A-Z][a-z]+ is', 'c[^l]', 'claim.*?\\d', '\\bat\\b', '\\Bai', 'x?y*z+', '(?:ab|cd){2,3}', 'a{2}', '[\\d,]+\\.\\d{2}', '(?<amount>\\d+) USD', 'é+', '\\u00e9', '\\x41', '😀', '.😀.', '[^\\s]+$', '(a|ab)(c|bcd)(d*)', '(a+)+$', '(x|x)*y', '\\s\\S\\w\\W\\d\\D']
  const LINES = ['A claim of 50 USD or less is approved.', 'abcdabcd and cdab', 'éé and 😀 and x😀y', 'aaaaaaaaaaaaaaaaaaaaaaaab', 'xxxxxxxxxxxxxxxxxxxxxxxxxy', '1,234.56 EUR', 'at bat atom', 'zzz', '', 'abcd', 'AI ai rain']
  it.each(PATTERNS)('%s matches where the browser’s engine does', (pattern) => {
    for (const ignoreCase of [false, true]) {
      const compiled = compilePattern(pattern, ignoreCase)
      const native = new RegExp(pattern, ignoreCase ? 'iu' : 'u')
      for (const line of LINES) {
        expect(firstMatch(compiled, line, { steps: SEARCH_STEPS }), `${pattern} on ${JSON.stringify(line)}`).toBe(line.search(native))
      }
    }
  })

  it('agrees with the browser’s engine on two thousand patterns drawn at random from the subset', () => {
    // A seeded generator, so a failure names a pattern that fails again.
    let seed = 20261018
    const next = (n: number) => { seed = (Math.imul(seed, 1103515245) + 12345) >>> 0; return (seed >>> 8) % n }
    const pick = <T,>(items: readonly T[]) => items[next(items.length)]!
    // Case-fold specials (σ ς Σ, k and the Kelvin sign, s and ſ, ı and I), a
    // carriage return, and astral characters, one of them cased (Deseret).
    const SPECIALS = ['σ', 'ς', 'Σ', 'k', '\u212a', 's', 'ſ', 'ı', 'I', 'İ', '\u{10400}', '\u{10428}', '😀']
    const ATOMS = ['a', 'b', 'c', '.', '[ab]', '[^a]', '[a-c1]', '[σk]', '[^ſ]', '\\d', '\\w', '\\W', '\\s', '\\b', '\\B', '^', '$', ' ', '1', '\\r', ...SPECIALS]
    const QUANTIFIERS = ['', '', '', '*', '+', '?', '{1,2}', '{2}', '*?', '+?', '{0,1}']
    // The pattern, and the browser's reading of it: its $ written as the
    // search's own, which also matches just before a final carriage return.
    const pattern = (depth: number): [string, string] => {
      let out = ''
      let reference = ''
      for (let count = 1 + next(3); count > 0; count--) {
        let atom: [string, string]
        if (depth < 2 && next(4) === 0) {
          const [left, leftReference] = pattern(depth + 1)
          const alternative = next(2) ? pattern(depth + 1) : null
          const head = next(2) ? '?:' : ''
          atom = [`(${head}${left}${alternative ? '|' + alternative[0] : ''})`, `(${head}${leftReference}${alternative ? '|' + alternative[1] : ''})`]
        } else {
          const picked = pick(ATOMS)
          atom = [picked, picked === '$' ? '(?=\\r?$)' : picked]
        }
        const quantifier = pick(QUANTIFIERS)
        out += atom[0] + quantifier
        reference += atom[1] + quantifier
      }
      return [out, reference]
    }
    const LINES = Array.from({ length: 24 }, () => Array.from({ length: next(12) }, () => pick(['a', 'b', 'c', ' ', '1', '_', 'é', '\r', ...SPECIALS])).join(''))
    let compared = 0
    for (let round = 0; round < 2000; round++) {
      const [source, reference] = pattern(0)
      const ignoreCase = next(2) === 0
      let native: RegExp | null = null
      try { native = new RegExp(reference, ignoreCase ? 'iu' : 'u') } catch { native = null }
      if (native === null) {
        expect(() => compilePattern(source, ignoreCase), `${source} is refused by both`).toThrow()
        continue
      }
      const compiled = compilePattern(source, ignoreCase)
      for (const line of LINES) {
        const expected = line.search(native)
        // V8 tries an assertion between the two halves of a surrogate pair under u; a match there is its, not the spec's.
        if (expected > 0 && /[\uDC00-\uDFFF]/.test(line[expected]!) && /[\uD800-\uDBFF]/.test(line[expected - 1]!)) continue
        expect(firstMatch(compiled, line, { steps: SEARCH_STEPS }), `${source}${ignoreCase ? ' (i)' : ''} on ${JSON.stringify(line)}`).toBe(expected)
        compared += 1
      }
    }
    expect(compared).toBeGreaterThan(20_000)
  })

  it('folds case through the closure of the case mappings, literals and classes alike, as the browser’s iu does', () => {
    const matches = (pattern: string, line: string) => firstMatch(compilePattern(pattern, true), line, { steps: SEARCH_STEPS }) === 0
    const cases: [string, string][] = [['σ', 'ς'], ['ς', 'Σ'], ['Σ', 'σ'], ['k', '\u212a'], ['\u212a', 'K'], ['s', 'ſ'], ['ſ', 'S'], ['ı', 'I'], ['ı', 'i'], ['İ', 'i'], ['I', 'i'], ['ß', 'ẞ'], ['µ', 'μ']]
    for (const [letter, other] of cases) {
      const browser = new RegExp(`^${letter}$`, 'iu').test(other)
      expect(matches(letter, other), `${letter} and ${other}`).toBe(browser)
      expect(matches(`[${letter}]`, other), `[${letter}] and ${other}`).toBe(browser)
      expect(matches(`[^${letter}]`, other), `[^${letter}] and ${other}`).toBe(!browser)
    }
    // ı, dotless, has no simple case folding: literal and class alike match only itself.
    expect([matches('ı', 'I'), matches('[ı]', 'I'), matches('ı', 'ı'), matches('[ı]', 'ı')]).toEqual([false, false, true, true])
    // \w folds too: ſ and the Kelvin sign are word characters under ignoreCase, and \b reads them so.
    expect(matches('\\w', 'ſ')).toBe(/^\w$/iu.test('ſ'))
    expect(firstMatch(compilePattern('\\bk', true), 'a\u212a', { steps: SEARCH_STEPS })).toBe('a\u212a'.search(/\bk/iu))
  })

  it('agrees with the browser’s iu on the case class of every cased code point', () => {
    const disagreements: string[] = []
    const letters = new Set<number>()
    for (let code = 0; code <= 0x1ffff; code++) {
      if (code >= 0xd800 && code <= 0xdfff) continue
      const text = String.fromCodePoint(code)
      if (text.toLowerCase() !== text || text.toUpperCase() !== text || folded(code).length > 1) letters.add(code)
    }
    for (const code of letters) {
      const browser = new RegExp(`^\\u{${code.toString(16)}}$`, 'iu')
      const text = String.fromCodePoint(code)
      const candidates = new Set<number>([...folded(code), ...[text.toLowerCase(), text.toUpperCase()].filter((mapped) => [...mapped].length === 1).map((mapped) => mapped.codePointAt(0)!)])
      for (const other of candidates) {
        if (browser.test(String.fromCodePoint(other)) !== folded(code).includes(other)) disagreements.push(`U+${code.toString(16)} U+${other.toString(16)}`)
      }
    }
    expect(letters.size).toBeGreaterThan(2000)
    expect(disagreements).toEqual([])
  })

  it('matches $ also just before a line’s final carriage return, as its description says', () => {
    expect(firstMatch(compilePattern('end$'), 'the end\r', { steps: SEARCH_STEPS })).toBe(4)
    expect(firstMatch(compilePattern('end$'), 'the end\rmore', { steps: SEARCH_STEPS })).toBe(-1)
    expect(searchInputSchema()).toBeDefined()
  })

  it('runs in time linear in the line on the patterns that make a backtracking engine exponential or cubic', () => {
    const line = 'a'.repeat(50_000) + '!'
    // No literal any match must contain, so the quick refusal of a line cannot answer for the machine.
    for (const pattern of ['(a+)+$', '(a|a)*[bc]', '(a*)*[bc]', '.*.*.*[z]', '(?:a|aa)+[c]']) {
      const budget = { steps: 50_000_000 }
      const started = performance.now()
      expect(firstMatch(compilePattern(pattern), line, budget), pattern).toBe(-1)
      expect(performance.now() - started, pattern).toBeLessThan(3000)
      // The work is bounded by the line's length times the program's.
      expect(50_000_000 - budget.steps, pattern).toBeLessThan(line.length * 64)
    }
  })

  it('refuses with a sentence a person can act on', () => {
    expect(() => compilePattern('(open')).toThrow(PatternError)
    expect(() => compilePattern('close)')).toThrow(/never opened/)
    expect(() => compilePattern('*a')).toThrow(/needs something before it/)
    expect(() => compilePattern('a**')).toThrow(/cannot itself be repeated/)
    expect(() => compilePattern('[z-a]')).toThrow(/lower character to a higher/)
    expect(() => compilePattern('a{3,2}')).toThrow(/n at most m/)
    expect(() => compilePattern('{')).toThrow(/write \\\{/)
    expect(() => compilePattern('\\q')).toThrow(/not an escape/)
    expect(() => compilePattern('')).toThrow(/non-empty/)
  })
})

describe('read, by line', () => {
  it('shows about two hundred numbered lines where the model names no limit, with a cursor to the rest', async () => {
    const context = new SessionContext(session())
    const lines = Array.from({ length: 500 }, (_, i) => `entry ${i}`)
    const entry = await context.ledger.retain({ id: 'log', kind: 'tool', label: 'A log', value: lines.join('\n') })
    const read = async (args: Record<string, unknown>) => (await context.result(READ_TOOL, await tool(context, READ_TOOL).execute(args, signal))).structuredContent as ReadResult
    const first = await read({ id: entry.id })
    expect(READ_WINDOW_LINES).toBe(200)
    expect(first).toMatchObject({ offset: 0, count: 200, total: 500, more: 'line:200' })
    expect(first.text.split('\n')).toEqual(lines.slice(0, 200).map((line, i) => `${i + 1}\t${line}`))
    const second = await read({ id: entry.id, cursor: first.more })
    expect(second).toMatchObject({ offset: 200, count: 200, more: 'line:400' })
    expect(second.text.split('\n')[0]).toBe('201\tentry 200')
    expect(await read({ id: entry.id, cursor: second.more })).toMatchObject({ offset: 400, count: 100, more: null })
    // A limit the model names is the window.
    expect(await read({ id: entry.id, limit: 3 })).toMatchObject({ count: 3, text: '1\tentry 0\n2\tentry 1\n3\tentry 2', more: 'line:3' })
  })

  it('reads an image item whole, as the image', async () => {
    const context = new SessionContext(session())
    const entry = await context.ledger.retain({ id: 'scan', kind: 'artifact', label: 'A scan', value: { kind: 'image', mediaType: 'image/png', bytes: 'QUJD' } })
    const answered = await tool(context, READ_TOOL).execute({ id: entry.id }, signal)
    expect(answered.structuredContent).toEqual({ type: 'image', id: entry.id, label: 'A scan', mediaType: 'image/png', bytes: 3 })
    expect(answered.content![1]).toEqual({ kind: 'image', mediaType: 'image/png', bytes: 'QUJD' })
    // By the id the host gave it, a result may name it.
    expect(context.ledger.image('scan')).toEqual({ kind: 'image', mediaType: 'image/png', bytes: 'QUJD' })
    expect(context.ledger.image(entry.id)).toEqual({ kind: 'image', mediaType: 'image/png', bytes: 'QUJD' })
    expect(context.ledger.image('elsewhere')).toBeNull()
  })

  it('reads an image it cannot show as the line that says why, never as an empty item, and search reports it the same way', async () => {
    const context = new SessionContext(session())
    await context.ledger.retain({ id: 'scan', kind: 'artifact', label: 'A scan', value: { kind: 'image', mediaType: 'image/png', bytes: 'QUJD' } })
    const cases: [string, unknown, RegExp][] = [
      ['gif', { kind: 'image', mediaType: 'image/gif', bytes: 'QUJD' }, /^\[image 1 \(image\/gif\): not shown, because an image must be image\/png, image\/jpeg, image\/webp\]$/],
      ['malformed', { kind: 'image', mediaType: 'image/png', bytes: 'not base64!' }, /^\[image 1 \(image\/png\): not shown, because an image’s bytes are base64\]$/],
      ['extra', { kind: 'image', mediaType: 'image/png', bytes: 'QUJD', caption: 'x' }, /^\[image 1: not shown, because an image part does not carry "caption"\]$/],
      ['unresolved', { kind: 'image', mediaType: 'image/png', reference: 'gone' }, /^\[image 1 \(image\/png\): not shown, because "gone" names no image this execution retains\]$/]
    ]
    const ids: string[] = []
    for (const [id, value, line] of cases) {
      const entry = await context.ledger.retain({ id, kind: 'artifact', label: `An attachment, ${id}`, value })
      ids.push(entry.id)
      const answered = await tool(context, READ_TOOL).execute({ id: entry.id }, signal)
      expect(answered.content).toHaveLength(1)
      expect(answered.content![0]!.text, id).toMatch(line)
      expect(answered.structuredContent).toMatchObject({ type: 'image', id: entry.id, notShown: answered.content![0]!.text })
    }
    // A reference that resolves is the image it names, whole.
    const pointing = await context.ledger.retain({ id: 'pointing', kind: 'artifact', label: 'The scan again', value: { kind: 'image', mediaType: 'image/png', reference: 'scan' } })
    const resolved = await tool(context, READ_TOOL).execute({ id: pointing.id }, signal)
    expect(resolved.structuredContent).toEqual({ type: 'image', id: pointing.id, label: 'The scan again', mediaType: 'image/png', bytes: 3 })
    expect(resolved.content![1]).toEqual({ kind: 'image', mediaType: 'image/png', bytes: 'QUJD' })
    // search names each one it cannot show, with the same line; the images it can show are images, not text.
    const found = await search(context, { pattern: 'anything' })
    expect(found.notShown!.map((item) => item.id)).toEqual(ids)
    for (const [index, [id, , line]] of cases.entries()) expect(found.notShown![index]!.text, id).toMatch(line)
  })

  it('names at most twenty images it cannot show, in a quarter of the window, on the first page only', async () => {
    const context = new SessionContext(session())
    await context.ledger.retain({ id: 'policy', kind: 'artifact', label: 'The policy', value: POLICY })
    for (let n = 0; n < 40; n++) {
      await context.ledger.retain({ id: `gone-${n}`, kind: 'artifact', label: `Attachment ${n}`, value: { kind: 'image', mediaType: 'image/png', reference: `r${n}-`.padEnd(400, 'x') } })
    }
    const first = await search(context, { pattern: 'USD', limit: 1 })
    expect(first.notShown!.length).toBeGreaterThan(0)
    expect(first.notShown!.length).toBeLessThanOrEqual(SEARCH_NOT_SHOWN)
    expect(utf8Bytes(JSON.stringify(first.notShown))).toBeLessThanOrEqual(readWindowBytes(context.policy) / 4)
    expect(first.matches).toHaveLength(1)
    const next = await search(context, { pattern: 'USD', limit: 1, cursor: first.more })
    expect(next.notShown).toBeUndefined()
    expect(next.matches).toHaveLength(1)
  })
})
