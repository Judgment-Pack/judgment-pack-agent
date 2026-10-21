/**
 * The search pattern (agent#5): a regular expression in the subset RE2 accepts,
 * matched in time linear in the text it reads.
 *
 * **Why not `RegExp`.** The browser's engine backtracks. Refusing the features
 * RE2 refuses (backreferences, lookaround) makes a pattern RE2's, but a
 * backtracking engine still runs `(a+)+$`, `(x|x)*y` or merely `.*.*z` in time
 * exponential or cubic in the line, and a model writing a pattern over a long
 * line would stop the page. So the pattern is parsed here, compiled to a small
 * program and run as a Pike VM, the construction RE2 uses: every position of
 * the text is read once, by at most one thread per instruction. The work is
 * counted, and a search that reaches its bound stops and says where to go on.
 *
 * The subset: literals and escaped punctuation; `.`; classes with ranges and
 * negation; `\d \D \w \W \s \S`; `\b \B`; `^` and `$` (a line's start and end);
 * groups `( )`, `(?: )` and `(?<name> )`; alternation; the quantifiers `* + ?
 * {n} {n,} {n,m}`, greedy or lazy; the control escapes `\t \n \r \f \v \0`,
 * `\xHH`, `\uHHHH` and `\u{H…}`. Refused, each with its reason: backreferences
 * (`\1`…`\9`, `\k<name>`), lookahead and lookbehind, Unicode property classes,
 * inline flags, a pattern over `PATTERN_LENGTH` characters, a repetition over
 * `PATTERN_REPEAT`, and a pattern whose compiling takes more than
 * `PATTERN_PROGRAM` steps (every node built and every repetition counted, so
 * nested repetitions of an empty group are refused at once) or whose program
 * holds more than `PATTERN_PROGRAM` instructions.
 *
 * Two readings differ from the browser's `RegExp` with the `u` flag, on
 * purpose: `$` also matches just before a line's final carriage return (a line
 * of a CRLF text ends in one), and the work is bounded. With `ignoreCase`,
 * letters fold through the closure of their case mappings, as the browser's
 * `iu` does: `σ`, `ς` and `Σ` are one letter, and so are `k`, `K` and the
 * Kelvin sign, and `s`, `S` and `ſ`; the dotless `ı` and the dotted `İ` have no
 * simple case folding and match only themselves.
 */

/** The longest pattern a search takes. */
export const PATTERN_STEPS = 4_000_000
export const PATTERN_LENGTH = 256
/** The largest count a repetition may name, as RE2 bounds it. */
export const PATTERN_REPEAT = 1000
/** The most instructions a compiled pattern may hold. */
export const PATTERN_PROGRAM = 2000

/** A pattern the search refuses, with the reason. */
export class PatternError extends Error {
  constructor(message: string) {
    super(message)
    this.name = 'PatternError'
  }
}

type Test = (code: number) => boolean
type Node =
  | { t: 'char'; test: Test; literal?: number }
  | { t: 'seq'; items: Node[] }
  | { t: 'alt'; options: Node[] }
  | { t: 'repeat'; node: Node; min: number; max: number }
  | { t: 'assert'; kind: Assertion }
type Assertion = 'bol' | 'eol' | 'word' | 'notword'

const CHAR = 0
const SPLIT = 1
const JMP = 2
const ASSERT = 3
const MATCH = 4

/** A compiled pattern: its program, and a literal every match contains, for a quick refusal of a line. */
export interface CompiledPattern {
  source: string
  ignoreCase: boolean
  ops: number[]
  x: number[]
  y: number[]
  tests: (Test | undefined)[]
  asserts: (Assertion | undefined)[]
  /** A run of literal characters every match contains, to skip a line without it; null where there is none. */
  required: string | null
  /** What `\b` and `\B` read as a word character: with ignoreCase, folded as `\w` is. */
  word: Test
}

const WHITESPACE = new Set([0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x20, 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff])
const isSpace: Test = (c) => WHITESPACE.has(c) || (c >= 0x2000 && c <= 0x200a)
const isDigit: Test = (c) => c >= 0x30 && c <= 0x39
const isWord: Test = (c) => (c >= 0x30 && c <= 0x39) || (c >= 0x41 && c <= 0x5a) || (c >= 0x61 && c <= 0x7a) || c === 0x5f
const isLineEnd = (c: number) => c === 0x0a || c === 0x0d || c === 0x2028 || c === 0x2029
const SYNTAX = '^$\\.*+?()[]{}|/-'

/**
 * Every code point's case-equivalence class, as the closure of its single-code-
 * point lower and upper case mappings, built once on the first search that
 * ignores case. The dotless `ı` (U+0131) takes no part: its mapping to `I` is
 * Turkic, not a simple case folding, and the browser's `iu` matches it only to
 * itself. Code points up to U+1FFFF cover every cased script.
 */
let FOLDS: Map<number, readonly number[]> | null = null
function foldTable(): Map<number, readonly number[]> {
  if (FOLDS !== null) return FOLDS
  const parent = new Map<number, number>()
  const find = (code: number): number => {
    let root = code
    while (parent.has(root) && parent.get(root) !== root) root = parent.get(root)!
    let at = code
    while (at !== root) {
      const up = parent.get(at)!
      parent.set(at, root)
      at = up
    }
    return root
  }
  const union = (a: number, b: number) => {
    if (!parent.has(a)) parent.set(a, a)
    if (!parent.has(b)) parent.set(b, b)
    const left = find(a)
    const right = find(b)
    if (left !== right) parent.set(left, right)
  }
  for (let code = 0; code <= 0x1ffff; code += 1) {
    if ((code >= 0xd800 && code <= 0xdfff) || code === 0x0131) continue
    const text = String.fromCodePoint(code)
    for (const mapped of [text.toLowerCase(), text.toUpperCase()]) {
      if (mapped === text) continue
      const points = [...mapped]
      if (points.length !== 1) continue
      const other = points[0]!.codePointAt(0)!
      if (other !== 0x0131) union(code, other)
    }
  }
  const classes = new Map<number, number[]>()
  for (const code of parent.keys()) {
    const root = find(code)
    const members = classes.get(root) ?? []
    members.push(code)
    classes.set(root, members)
  }
  FOLDS = new Map()
  for (const members of classes.values()) for (const code of members) FOLDS.set(code, members)
  return FOLDS
}

/** One code point's case-equivalence class: itself, and every letter its case mappings reach. */
export function folded(code: number): readonly number[] {
  return foldTable().get(code) ?? [code]
}

function parse(source: string, ignoreCase: boolean): Node {
  let at = 0
  const fail = (reason: string): never => {
    throw new PatternError(reason)
  }
  const peek = () => source[at]
  // Literals and classes fold alike: a code point matches where any letter of its class does.
  const caseless = (test: Test): Test => (ignoreCase ? (c) => folded(c).some(test) : test)
  const literal = (code: number): Node => {
    if (!ignoreCase) return { t: 'char', test: (c) => c === code, literal: code }
    const set = new Set(folded(code))
    return { t: 'char', test: (c) => set.has(c), literal: code }
  }
  // With ignoreCase a word character is one whose case class holds one, as the
  // browser's `iu` reads `\w` (so `ſ` and the Kelvin sign are word characters),
  // and `\W` is that set's complement.
  const word: Test = caseless(isWord)
  const hex = (digits: number): number => {
    const text = source.slice(at, at + digits)
    if (text.length !== digits || !/^[0-9A-Fa-f]+$/.test(text)) fail('an escape that names a character by its code needs that many hexadecimal digits')
    at += digits
    return parseInt(text, 16)
  }
  /** One escape after its backslash: a code point, or a class test. */
  const escape = (inClass: boolean): number | Test | Assertion => {
    const c = source[at]
    if (c === undefined) return fail('a pattern cannot end in a backslash')
    at += 1
    if (c >= '1' && c <= '9') return fail('backreferences are not supported')
    if (c === 'k') return fail('backreferences are not supported')
    if (c === 'p' || c === 'P') return fail('Unicode property classes are not supported')
    if (c === 'd') return isDigit
    if (c === 'D') return (x) => !isDigit(x)
    if (c === 'w') return word
    if (c === 'W') return (x) => !word(x)
    if (c === 's') return isSpace
    if (c === 'S') return (x) => !isSpace(x)
    if (c === 'b') return inClass ? 0x08 : 'word'
    if (c === 'B') return inClass ? fail('\\B has no meaning in a class') : 'notword'
    if (c === 't') return 0x09
    if (c === 'n') return 0x0a
    if (c === 'r') return 0x0d
    if (c === 'f') return 0x0c
    if (c === 'v') return 0x0b
    if (c === '0') {
      if (isDigit(source.charCodeAt(at))) fail('backreferences are not supported')
      return 0
    }
    if (c === 'x') return hex(2)
    if (c === 'u') {
      if (peek() !== '{') return hex(4)
      const end = source.indexOf('}', at)
      if (end < 0 || end - at < 2 || end - at > 7) fail('\\u{…} names a code point in one to six hexadecimal digits')
      at += 1
      const code = hex(end - at)
      at += 1
      if (code > 0x10ffff) fail('\\u{…} names a code point up to 10FFFF')
      return code
    }
    if (SYNTAX.includes(c)) return c.charCodeAt(0)
    return fail(`\\${c} is not an escape this search knows`)
  }
  const codeAt = (): number => {
    const code = source.codePointAt(at)!
    at += code > 0xffff ? 2 : 1
    return code
  }
  const characterClass = (): Node => {
    // After the `[`.
    let negated = false
    if (peek() === '^') {
      negated = true
      at += 1
    }
    const tests: Test[] = []
    let first = true
    for (;;) {
      const c = peek()
      if (c === undefined) fail('a character class is not closed')
      if (c === ']' && !first) {
        at += 1
        break
      }
      if (c === ']' && first) fail('an empty character class matches nothing')
      first = false
      let low: number | Test
      if (c === '\\') {
        at += 1
        const held = escape(true)
        if (typeof held === 'string') fail('an assertion has no meaning in a class')
        low = held as number | Test
      } else low = codeAt()
      if (typeof low === 'number' && peek() === '-' && source[at + 1] !== ']' && source[at + 1] !== undefined) {
        at += 1
        let high: number | Test
        if (peek() === '\\') {
          at += 1
          const held = escape(true)
          if (typeof held !== 'number') fail('a class range runs between two characters')
          high = held as number
        } else high = codeAt()
        if (typeof high !== 'number' || high < low) fail('a class range runs from a lower character to a higher one')
        const from = low
        const to = high as number
        tests.push((x) => x >= from && x <= to)
        continue
      }
      if (typeof low === 'number') {
        const code = low
        tests.push((x) => x === code)
      } else tests.push(low)
    }
    const inside = caseless((x) => tests.some((test) => test(x)))
    return { t: 'char', test: negated ? (x) => !inside(x) : inside }
  }
  const quantifier = (): { min: number; max: number } | null => {
    const c = peek()
    if (c === '*') return (at += 1, { min: 0, max: Infinity })
    if (c === '+') return (at += 1, { min: 1, max: Infinity })
    if (c === '?') return (at += 1, { min: 0, max: 1 })
    if (c !== '{') return null
    const held = /^\{(\d{1,5})(,(\d{0,5}))?\}/.exec(source.slice(at))
    if (!held) return fail('a brace is a repetition {n}, {n,} or {n,m}; write \\{ for the character')
    at += held[0].length
    const min = Number(held[1])
    const max = held[2] === undefined ? min : held[3] === '' ? Infinity : Number(held[3])
    if (min > PATTERN_REPEAT || (max !== Infinity && max > PATTERN_REPEAT)) fail(`a repetition counts at most ${PATTERN_REPEAT}`)
    if (max < min) fail('a repetition {n,m} needs n at most m')
    return { min, max }
  }
  const atom = (): Node | null => {
    const c = peek()
    if (c === undefined || c === '|' || c === ')') return null
    if (c === '(') {
      at += 1
      if (source.startsWith('?=', at) || source.startsWith('?!', at) || source.startsWith('?<=', at) || source.startsWith('?<!', at)) {
        fail('lookahead and lookbehind are not supported')
      }
      if (source.startsWith('?:', at)) at += 2
      else if (source.startsWith('?<', at)) {
        const name = /^\?<([A-Za-z_][A-Za-z0-9_]*)>/.exec(source.slice(at))
        if (!name) fail('a named group is written (?<name>…)')
        at += name![0].length
      } else if (peek() === '?') fail('inline flags and other (? groups are not supported')
      const inner = alternation()
      if (peek() !== ')') fail('a group is not closed')
      at += 1
      // A group is never itself an assertion, so `(?:^)*` repeats a group, as the browser's engine allows.
      return inner.t === 'assert' ? { t: 'seq', items: [inner] } : inner
    }
    if (c === '[') {
      at += 1
      return characterClass()
    }
    if (c === '.') {
      at += 1
      return { t: 'char', test: (x) => !isLineEnd(x) }
    }
    if (c === '^') return (at += 1, { t: 'assert', kind: 'bol' })
    if (c === '$') return (at += 1, { t: 'assert', kind: 'eol' })
    if (c === '\\') {
      at += 1
      const held = escape(false)
      if (typeof held === 'string') return { t: 'assert', kind: held }
      if (typeof held === 'number') return literal(held)
      return { t: 'char', test: caseless(held) }
    }
    if (c === '*' || c === '+' || c === '?') fail('a repetition needs something before it to repeat')
    if (c === '{' || c === '}' || c === ']') fail(`write \\${c} for the character ${c}`)
    return literal(codeAt())
  }
  const sequence = (): Node => {
    const items: Node[] = []
    for (;;) {
      const held = atom()
      if (held === null) break
      const count = quantifier()
      if (count === null) {
        items.push(held)
        continue
      }
      if (held.t === 'assert') fail('an assertion cannot be repeated')
      if (peek() === '?') at += 1
      if (peek() === '*' || peek() === '+' || peek() === '?' || peek() === '{') fail('a repetition cannot itself be repeated without a group')
      items.push({ t: 'repeat', node: held, ...count })
    }
    return items.length === 1 ? items[0]! : { t: 'seq', items }
  }
  const alternation = (): Node => {
    const options = [sequence()]
    while (peek() === '|') {
      at += 1
      options.push(sequence())
    }
    return options.length === 1 ? options[0]! : { t: 'alt', options }
  }
  const tree = alternation()
  if (at < source.length) fail(peek() === ')' ? 'a parenthesis closes a group that was never opened' : 'the pattern could not be read to its end')
  return tree
}

/** The longest run of literal characters the top-level sequence requires, or null. */
function requiredLiteral(tree: Node): string | null {
  const items = tree.t === 'seq' ? tree.items : [tree]
  let best = ''
  let run = ''
  for (const item of items) {
    if (item.t === 'char' && item.literal !== undefined) {
      run += String.fromCodePoint(item.literal)
      if (run.length > best.length) best = run
    } else if (item.t === 'assert') {
      continue
    } else run = ''
  }
  return best === '' ? null : best
}

/** Whether a node compiles to no instruction at all: an empty group, or a repetition or sequence of nothing. */
function emitsNothing(node: Node): boolean {
  if (node.t === 'seq') return node.items.every(emitsNothing)
  if (node.t === 'repeat') return emitsNothing(node.node)
  return false
}

/** How many times a repetition builds its body: its minimum, then each optional copy, or one loop. */
const iterations = (node: Extract<Node, { t: 'repeat' }>) => (node.max === Infinity ? node.min + 1 : node.max)

/**
 * The compile steps a node takes when built, every node and every repetition's
 * every iteration counted, saturating just past the bound so nesting cannot
 * overflow it. Computed over the tree, so a pattern it refuses is refused in
 * the time the tree takes to walk.
 */
function compileSteps(node: Node): number {
  const limit = PATTERN_PROGRAM + 1
  switch (node.t) {
    case 'char':
    case 'assert':
      return 1
    case 'seq':
      return Math.min(limit, node.items.reduce((sum, item) => sum + compileSteps(item), 1))
    case 'alt':
      return Math.min(limit, node.options.reduce((sum, option) => sum + compileSteps(option), 1))
    case 'repeat':
      return Math.min(limit, 1 + iterations(node) * compileSteps(node.node))
  }
}

/** A pattern, checked and compiled, or a `PatternError` saying why it is refused. */
export function compilePattern(source: string, ignoreCase = false): CompiledPattern {
  if (typeof source !== 'string' || source.length === 0) throw new PatternError('a pattern is a non-empty string')
  if (source.length > PATTERN_LENGTH) throw new PatternError(`a pattern is at most ${PATTERN_LENGTH} characters`)
  const tree = parse(source, ignoreCase)
  const program: CompiledPattern = {
    source, ignoreCase, ops: [], x: [], y: [], tests: [], asserts: [], required: null,
    word: ignoreCase ? (code) => folded(code).some(isWord) : isWord
  }
  // **Every build and every repetition's every iteration is charged**, a
  // repetition of nothing too: a nested repetition of an empty group emits no
  // instruction, so the instruction bound alone never stopped it compiling.
  let steps = 0
  const charge = (count: number) => {
    steps += count
    if (steps > PATTERN_PROGRAM) throw new PatternError(`the pattern takes more than ${PATTERN_PROGRAM} steps to compile; use smaller repetitions`)
  }
  const emit = (op: number, x = 0, y = 0, test?: Test, kind?: Assertion): number => {
    if (program.ops.length >= PATTERN_PROGRAM) throw new PatternError(`the pattern compiles to more than ${PATTERN_PROGRAM} instructions; use smaller repetitions`)
    program.ops.push(op)
    program.x.push(x)
    program.y.push(y)
    program.tests.push(test)
    program.asserts.push(kind)
    return program.ops.length - 1
  }
  const build = (node: Node): void => {
    charge(1)
    switch (node.t) {
      case 'char':
        emit(CHAR, 0, 0, node.test)
        return
      case 'assert':
        emit(ASSERT, 0, 0, undefined, node.kind)
        return
      case 'seq':
        for (const item of node.items) build(item)
        return
      case 'alt': {
        const jumps: number[] = []
        node.options.forEach((option, index) => {
          if (index === node.options.length - 1) {
            build(option)
            return
          }
          const split = emit(SPLIT)
          program.x[split] = program.ops.length
          build(option)
          jumps.push(emit(JMP))
          program.y[split] = program.ops.length
        })
        for (const jump of jumps) program.x[jump] = program.ops.length
        return
      }
      case 'repeat': {
        // **A repetition of nothing collapses to nothing**: it matches only the
        // empty string, as nothing does. Its iterations are still charged.
        if (emitsNothing(node.node)) {
          charge(iterations(node) * compileSteps(node.node))
          return
        }
        for (let index = 0; index < node.min; index += 1) build(node.node)
        if (node.max === Infinity) {
          const split = emit(SPLIT)
          program.x[split] = program.ops.length
          build(node.node)
          emit(JMP, split)
          program.y[split] = program.ops.length
          return
        }
        const exits: number[] = []
        for (let index = node.min; index < node.max; index += 1) {
          const split = emit(SPLIT)
          program.x[split] = program.ops.length
          exits.push(split)
          build(node.node)
        }
        for (const split of exits) program.y[split] = program.ops.length
        return
      }
    }
  }
  build(tree)
  emit(MATCH)
  // Only a case-sensitive pattern skips a line by its literal: a folded letter
  // (the Kelvin sign for k, ſ for s) has no lower case a line search would find.
  program.required = ignoreCase ? null : requiredLiteral(tree)
  return program
}

/** The work a search may do, counted in thread steps, shared by every line it reads. */
export interface PatternBudget {
  steps: number
}

/** One search's matcher: the pattern, and the thread lists it reuses for every line. */
export type Matcher = (line: string, budget: PatternBudget) => number | null

/**
 * A matcher for one search: where the pattern first matches in a line, as a
 * string index; -1 where it does not; null where the budget ran out before the
 * line was read through.
 *
 * A Pike VM: the threads alive at one position are a list in priority order,
 * at most one per instruction, each with the position it started at. Threads
 * carried from earlier positions come before the one started here, so the list
 * is ordered by start, and the first thread to reach the match has the earliest
 * start of those still alive. Nothing is ever read twice.
 *
 * **The lists are allocated once per search** and reset by a generation count
 * that carries across lines, and **each line is charged the program's size**
 * before it is read: so millions of short lines stop at the work bound as one
 * long one does, rather than costing their setup uncounted.
 */
export function matcher(pattern: CompiledPattern): Matcher {
  const { ops, x, y, tests, asserts, word } = pattern
  const size = ops.length
  let current = { pcs: new Int32Array(size), starts: new Int32Array(size), length: 0 }
  let next = { pcs: new Int32Array(size), starts: new Int32Array(size), length: 0 }
  const seen = new Int32Array(size).fill(-1)
  const stack: number[] = []
  let generation = 0
  return (line, budget) => {
    budget.steps -= size
    if (budget.steps <= 0) return null
    if (pattern.required !== null && !line.includes(pattern.required)) return -1
    const end = line.length
    // Word characters are all in the Basic Multilingual Plane, so a code unit tells.
    const wordBefore = (pos: number) => pos > 0 && word(line.charCodeAt(pos - 1))
    const wordAt = (pos: number) => pos < end && word(line.charCodeAt(pos))
    const holds = (kind: Assertion, pos: number): boolean => {
      if (kind === 'bol') return pos === 0
      // Also just before a final carriage return: a line of a CRLF text ends in one.
      if (kind === 'eol') return pos === end || (pos === end - 1 && line.charCodeAt(pos) === 0x0d)
      const boundary = wordBefore(pos) !== wordAt(pos)
      return kind === 'word' ? boundary : !boundary
    }
    /** Follow the empty moves from one instruction at one position, adding the threads they reach. */
    const add = (list: typeof current, at0: number, start: number, pos: number) => {
      stack.push(at0)
      while (stack.length > 0) {
        const at = stack.pop()!
        if (seen[at] === generation) continue
        seen[at] = generation
        budget.steps -= 1
        const op = ops[at]!
        if (op === JMP) stack.push(x[at]!)
        // The preferred branch is followed first, so it is pushed last.
        else if (op === SPLIT) stack.push(y[at]!, x[at]!)
        else if (op === ASSERT) {
          if (holds(asserts[at]!, pos)) stack.push(at + 1)
        } else {
          list.pcs[list.length] = at
          list.starts[list.length] = start
          list.length += 1
        }
      }
    }
    current.length = 0
    let matched = -1
    generation += 1
    for (let pos = 0; ; ) {
      if (budget.steps <= 0) return null
      // The thread that starts here, after every thread carried here: the lowest priority.
      if (matched < 0) add(current, 0, pos, pos)
      // Nothing alive: done where a match is in hand or the line is read; otherwise on to the next position.
      if (current.length === 0 && (matched >= 0 || pos >= end)) return matched
      const code = pos < end ? line.codePointAt(pos)! : -1
      const width = code > 0xffff ? 2 : 1
      generation += 1
      next.length = 0
      for (let index = 0; index < current.length; index += 1) {
        const pc = current.pcs[index]!
        const start = current.starts[index]!
        budget.steps -= 1
        if (ops[pc] === MATCH) {
          // Every thread after this one started here or later: none can match earlier.
          matched = matched < 0 ? start : Math.min(matched, start)
          break
        }
        if (matched >= 0 && start >= matched) continue
        if (code >= 0 && tests[pc]!(code)) add(next, pc + 1, start, pos + width)
      }
      if (pos >= end) return matched
      const swap = current
      current = next
      next = swap
      pos += width
    }
  }
}

/** Where the pattern first matches in one line, with a matcher of its own: see `matcher`. */
export function firstMatch(pattern: CompiledPattern, line: string, budget: PatternBudget): number | null {
  return matcher(pattern)(line, budget)
}

/** Union of overlapping matches, in one bounded Pike pass. Overlapping matches
 * form one hole; no suffix is rescanned and no backtracking engine is used. */
export function matchRanges(pattern: CompiledPattern, text: string, budget: PatternBudget): [number, number][] | null {
  const {ops,x,y,tests,asserts,word}=pattern, size=ops.length
  const ranges:[number,number][]=[]
  let current=new Map<number,number>()
  const holds=(kind:Assertion|undefined,pos:number)=>kind==='bol'?pos===0:kind==='eol'?pos===text.length||pos===text.length-1&&text.charCodeAt(pos)===13:(pos>0&&word(text.charCodeAt(pos-1)))!==(pos<text.length&&word(text.charCodeAt(pos)))===(kind==='word')
  const add=(set:Map<number,number>,pc:number,start:number,pos:number)=>{
    const stack=[pc]
    while(stack.length){
      const at=stack.pop()!
      if((set.get(at)??Infinity)<=start)continue
      set.set(at,start);budget.steps--
      if(ops[at]===JMP)stack.push(x[at]!)
      else if(ops[at]===SPLIT)stack.push(y[at]!,x[at]!)
      else if(ops[at]===ASSERT&&holds(asserts[at],pos))stack.push(at+1)
    }
  }
  budget.steps-=size
  for(let pos=0;;){
    if(budget.steps<=0)return null
    add(current,0,pos,pos)
    const code=pos<text.length?text.codePointAt(pos)!:-1,width=code>0xffff?2:1
    const next=new Map<number,number>()
    for(const pc of [...current.keys()].sort((a,b)=>a-b)){
      const start=current.get(pc)!
      budget.steps--
      if(ops[pc]===MATCH&&start<=pos){
        let from=start
        while(ranges.length&&(ranges.at(-1)![1]>from||ranges.at(-1)![0]===from))from=Math.min(from,ranges.pop()![0])
        ranges.push([from,pos])
      }else if(ops[pc]===CHAR&&code>=0&&tests[pc]!(code))add(next,pc+1,start,pos+width)
    }
    if(budget.steps<=0)return null
    if(pos===text.length)return ranges
    current=next;pos+=width
  }
}
