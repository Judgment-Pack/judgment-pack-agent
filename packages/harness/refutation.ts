import { sourceMessage } from './host'
/**
 * The refutation pass: a second, adversarial run over the proposed document,
 * and the three rules the desk holds it to.
 *
 * ADR-0001: *"Implemented per engine on the framework's natural primitive, and
 * held to three desk rules the bake-off showed no prototype kept on its own: a
 * non-empty list of checks before 'not refuted' is rendered; the verdict is
 * never taken from the model's prose, only from the runtime's results; and the
 * critic runs inside the same ToolGate as the main loop."*
 *
 * The **primitive** is each engine's — a hand-written second loop, a second
 * `streamText` in `vercel`. Everything a person is shown is here, in one
 * module both import, for the reason the ToolGate's allow-list is one list: a
 * rule written twice is a rule two readers can disagree about, and this one is
 * *whether a document was refuted*.
 *
 * **The critic's calls go through the session's gate, which is what the third
 * rule means.** Neither engine builds the critic a dispatch of its own: it runs
 * on the same `guardedCallTool`, through the same ToolGate on the same
 * transport — so its `experimental_evaluate` is rewritten to a rehearsal before
 * the frame leaves the page, and it can no more reach `write_file` than the main
 * loop can. Since agent#10 it is offered only `CRITIC_TOOLS` and
 * `CRITIC_READERS`, and the shared boundary refuses any other name it calls
 * while the critic runs (`context/session.ts`, `admit` on the wrapped
 * `callTool` and host tools), before the ToolGate and before a host tool's
 * `execute`, with a `guardrail` `refused` event.
 *
 * **The verdict reads outcomes as well as statuses** (agent#10). Where the
 * host declares cases (the pack's saved tests, or cases the request states),
 * each rehearsal the critic runs over the proposed document is compared with
 * the case whose facts it ran, and each row of a test run with the case it
 * carried; a case whose outcome differs refutes the proposal, and so does a
 * declared case nothing compared, and the critique names each.
 *
 * **Nothing here writes.** The pass runs after the main loop produced a
 * proposal and *before* the proposal event is emitted, and every call it makes
 * is a read.
 */
import { failureMessage, normalizeFailure, type FailureSource } from './failures'
import type { AssistantEvent, ProposalNote } from './engine'

/**
 * **The critic's tools, an allow-list of reads** (agent#10), and the whole of
 * what it may call: `validate`; `experimental_evaluate`, which the gate makes a
 * rehearsal on the wire; `get_schema`; and `experimental_test_cases`, under its
 * own bounds (the gate's argument allow-list and byte bounds, and the turn's
 * `testCallsPerTurn`, which the critic shares with the loop). Every other name
 * is refused before it leaves the page: the runtime's other reads, every host
 * tool (a `propose_*` or `request_*` tool, memory, the plan, a question, a
 * delegation) and the context tools alike. The list replaces a deny-list that
 * left any host tool it did not name reachable.
 */
export const CRITIC_TOOLS = ['validate', 'experimental_evaluate', 'get_schema', 'experimental_test_cases'] as const
const CRITIC_TOOL_SET: ReadonlySet<string> = new Set(CRITIC_TOOLS)

/**
 * The harness's own context readers the critic may also call (agent#10 review
 * row 6): `read_window` and `search`, the context ledger's tools, whose names a
 * host tool may not take. They reach no Runtime tool and no action; the critic
 * reads its own retained first message with them where that message did not
 * fit its request.
 */
export const CRITIC_READERS = ['read_window', 'search'] as const
const CRITIC_READER_SET: ReadonlySet<string> = new Set(CRITIC_READERS)

/** Whether the critic may call a runtime tool of this name. */
export function criticMayCall(name: string): boolean {
  return CRITIC_TOOL_SET.has(name)
}

/** Whether the critic may call a host tool of this name: only the harness's own readers. */
export function criticMayRead(name: string): boolean {
  return CRITIC_READER_SET.has(name)
}

/** Why the critic's call of `name` did not leave the page: the guardrail's detail. */
export function criticRefusalDetail(name: string): string {
  return `${name} is not one of the tools the critic may call (${[...CRITIC_TOOLS, ...CRITIC_READERS].join(', ')}); the call did not leave the page and nothing was written`
}


/**
 * **Whether the critic runs, by one rule both engines read** (agent#10): the
 * session's `adversarialReview` where the host set it, and otherwise the
 * policy table's `adversarialReview`, which is `false`. So the critic runs
 * only where the host asks for it, at any reasoning tier and on either engine.
 */
export function criticRuns(session: { adversarialReview?: boolean }, policy: { adversarialReview: boolean }): boolean {
  return session.adversarialReview ?? policy.adversarialReview
}

/**
 * The literal a critic's request carries, so a scripted endpoint — and a
 * reader of a transcript — can tell the two conversations apart.
 *
 * From the bake-off's `THINKING-SPEC.md` §9.2, which made the pass a
 * measurement rather than a claim.
 */
export const REFUTATION_MARKER = 'REFUTATION PASS'

/**
 * **The one sentence the desk adds, and the whole of it.**
 *
 * The instructions the critic works from are the runtime's own `test_pack`
 * prompt, handed over verbatim; this says the three things that prompt cannot
 * know — that there is a proposal about to be shown, that the job is to try to
 * break it with the runtime's tools, and that the critic states no verdict of
 * its own. A longer sentence would be this desk having a second opinion about
 * how a pack is tested.
 */
export const CRITIC_SENTENCE =
  `${REFUTATION_MARKER}. A pack has just been drafted and is about to be shown to the ` +
  'author. Try to refute it: run the runtime’s own checks against the document below and ' +
  'report exactly what the runtime said. You never state a verdict of your own, you never ' +
  'write a file, and every evaluation you run is a rehearsal.'

/** What the critic is told about itself, above the runtime's prompt. */
export const CRITIC_SYSTEM =
  `${REFUTATION_MARKER}. You are the judgment-pack desk’s critic. You check a proposed ` +
  'document against the runtime and quote what the runtime returned. You propose nothing, ' +
  'decide nothing, and write nothing.'

/**
 * How many turns a critic gets.
 *
 * Four: a check, a rehearsal, and a sentence about them, with one spare. The
 * pass is a bounded read over a document that already exists, not a second
 * authoring session, and a critic that wanted twenty turns would be one.
 */
export const MAX_CRITIC_TURNS = 4

/**
 * The tools whose answers count as a **check**, and the runtime's own word for
 * "this one did not refuse the document".
 *
 * **Two words and not one.** The brief's rule reads `status !== 'valid'`, and
 * over `validate` alone that is right; but a rehearsal `experimental_evaluate`
 * answers `"status": "evaluated"`, so the same rule over both would report
 * every session ever run as refuted. The runtime has one word per command for
 * "this went through", and this is the table of them — one place, so that the
 * verdict cannot mean two things in two files.
 *
 * A tool that is not here is not a check: `get_schema`, `list_examples` and
 * `get_example` are questions about the runtime rather than about the document,
 * and a critic that only asked those has run no check at all.
 */
export const SETTLED_STATUS: Readonly<Record<string, string>> = {
  validate: 'valid',
  experimental_evaluate: 'evaluated'
}

/**
 * The names this table holds, and **only** the names it holds.
 *
 * `tool in SETTLED_STATUS` was the reading, and an object literal inherits
 * `toString`, `constructor`, `valueOf` and the rest from `Object.prototype` —
 * so a critic that called a tool named `toString` produced a *check*, from a
 * table that names no such tool. `Object.hasOwn` reads the table and nothing
 * behind it.
 */
export function isCheckTool(tool: string): boolean {
  return Object.hasOwn(SETTLED_STATUS, tool)
}

/** One runtime answer the critic caused, as the verdict reads it. */
export interface Check {
  tool: string
  /** The runtime's own `status`, verbatim. */
  status: string
  /** How many diagnostics it carried, for the quote. */
  diagnostics: number
}

/**
 * The sentence a proposal is shown under when the critic reached no check.
 *
 * ADR-0001's first rule: **a non-empty list of checks before "not refuted" is
 * rendered.** A critic that talked for four turns and asked the runtime nothing
 * has produced no evidence, and "not refuted" would be this desk reporting the
 * absence of a check as a clean bill of health.
 */
export const NO_CHECKS =
  sourceMessage("the critic ran no runtime check, so nothing here says whether the document holds")

/**
 * The sentence the pass reports where the runtime advertises no `test_pack`.
 *
 * The instructions a critic works from are the **runtime's**; the desk adds one
 * sentence to them and has no second opinion of its own to fall back on. So a
 * runtime with no testing prompt gets no critic rather than a critic working
 * from the desk's sentence alone — and the proposal is shown, without a
 * refutation line, because a pass that did not run refuted nothing.
 */
export const NO_TEST_PROMPT =
  sourceMessage("the refutation pass did not run: this desk has no test_pack prompt from the runtime to work from, and the critic works from the runtime’s instructions rather than from words of this desk’s own")

/** The verdict, and the words on either side of it. */
export interface Critique {
  /** Computed from the runtime's own statuses and outcomes. Never from prose. */
  refuted: boolean
  checks: Check[]
  /** The runtime's words, quoted. */
  text: string
  /** The critic's own words, kept beside the verdict and never deciding it. */
  modelText: string
  /** Each declared case a rehearsal or a test run compared, where the host declared any (agent#10). */
  cases?: CaseComparison[]
  /**
   * The declared cases nothing compared, by id (agent#10 review row 1). Each
   * counts as unsettled, so a critique with any is never "not refuted".
   */
  incomplete?: string[]
  /** What the critic cost: its model and its requests (agent#10). */
  figures?: CriticFigures
}

/**
 * One case whose outcome the host declares (agent#10): one of the pack's saved
 * tests, or a case the request declares. The members are a row of the
 * runtime's matrix (`matrixVersion` 3), so the critic can run them all with
 * `experimental_test_cases` as they are, plus `source`, which the matrix does
 * not carry.
 */
export interface CriticCase {
  source: 'declared' | 'test'
  id: string
  facts: Record<string, unknown>
  evidenceAvailability?: Record<string, unknown>
  supportedExtensions?: string[]
  /** The §8.3 disposition the case expects; each member it states must equal the outcome's. */
  expectedDisposition?: Record<string, unknown>
  /** Or the §8.4 evaluation-error class it expects, and optionally the phase. */
  expectedErrorClass?: string
  expectedErrorPhase?: string
  expectedHandoffTarget?: unknown
  origin?: string
  focus?: string
  specSection?: string
  cites?: unknown
}

/** An outcome, as a comparison reports it: a disposition, or an evaluation error. */
export type CaseOutcome = { disposition: Record<string, unknown> } | { errorClass: string; phase?: string }

/**
 * One comparison of a declared case's outcome with the runtime's, the critic's
 * structured note (`run-event.json`, `$defs/caseComparison`). `actual` is
 * absent where the runtime reported no outcome for the case.
 */
export interface CaseComparison {
  case: string
  source: CriticCase['source']
  tool: 'experimental_evaluate' | 'experimental_test_cases'
  agrees: boolean
  expected: CaseOutcome
  actual?: CaseOutcome
}

/**
 * What one critic pass cost (`provenance.json`, `$defs/criticFigures`): the
 * model it ran on and its requests, in the unit its engine counts. `vercel`
 * counts model requests, each claimed against the allowance, and sums the
 * token counts its completions reported; `codex` counts agent runs, and the
 * bridge reports no tokens.
 */
export interface CriticFigures {
  model: string
  unit: 'model-requests' | 'agent-runs'
  requests: number
  /** The runtime calls the harness made itself for declared cases the critic left uncompared (review row 8). */
  harnessCalls?: number
  inputTokens?: number
  outputTokens?: number
  reasoningTokens?: number
  cacheReadTokens?: number
  cacheWriteTokens?: number
}

const CASE_MEMBERS = new Set([
  'source', 'id', 'facts', 'evidenceAvailability', 'supportedExtensions', 'expectedDisposition',
  'expectedErrorClass', 'expectedErrorPhase', 'expectedHandoffTarget', 'origin', 'focus', 'specSection', 'cites'
])
const isObject = (value: unknown): value is Record<string, unknown> =>
  value !== null && typeof value === 'object' && !Array.isArray(value)

/**
 * The host's declared cases, read closed, or a throw naming what is wrong.
 *
 * A row of the runtime's matrix and `source`, nothing else: a unique `id`, a
 * `facts` object, and exactly one expectation, a disposition object with a
 * `kind` or an error class. At most 200.
 */
export function readCriticCases(value: unknown): CriticCase[] {
  if (!Array.isArray(value) || value.length > 200) throw new Error('The declared cases must be an array of at most 200 cases.')
  const seen = new Set<string>()
  return value.map((item, at) => {
    const name = isObject(item) && typeof item.id === 'string' ? `case ${JSON.stringify(item.id.slice(0, 80))}` : `case ${at + 1}`
    const fail = (why: string): never => { throw new Error(`The declared ${name} ${why}.`) }
    if (!isObject(item)) return fail('is not an object')
    const extra = Object.keys(item).find((member) => !CASE_MEMBERS.has(member))
    if (extra !== undefined) fail(`carries ${JSON.stringify(extra.slice(0, 64))}, which a declared case does not have`)
    if (item.source !== 'declared' && item.source !== 'test') fail('has no source: declared or test')
    if (typeof item.id !== 'string' || item.id === '' || item.id.length > 200) fail('has no id of 1 to 200 characters')
    if (seen.has(item.id as string)) fail('repeats an id')
    seen.add(item.id as string)
    if (!isObject(item.facts)) fail('has no facts object')
    if (item.evidenceAvailability !== undefined && !isObject(item.evidenceAvailability)) fail('has an evidenceAvailability that is not an object')
    if (item.supportedExtensions !== undefined && (!Array.isArray(item.supportedExtensions) || item.supportedExtensions.some((entry) => typeof entry !== 'string'))) fail('has supportedExtensions that are not strings')
    const disposition = item.expectedDisposition !== undefined
    const error = item.expectedErrorClass !== undefined
    if (disposition === error) fail('must expect exactly one of a disposition and an error class')
    if (disposition && (!isObject(item.expectedDisposition) || typeof item.expectedDisposition.kind !== 'string')) fail('expects a disposition that is not an object with a kind')
    if (error && (typeof item.expectedErrorClass !== 'string' || item.expectedErrorClass === '')) fail('expects an error class that is not a name')
    if (item.expectedErrorPhase !== undefined && (!error || typeof item.expectedErrorPhase !== 'string')) fail('states an error phase without an error class')
    return item as unknown as CriticCase
  })
}

/**
 * How many of the declared cases the critic's own calls left uncompared the
 * harness rehearses itself, one call each, after the critic's turns (agent#10).
 * The rest stay `incomplete`. Bounded so that a large saved matrix cannot
 * spend the turn's shared tool calls on one pass.
 */
export const CRITIC_REHEARSALS = 8

/**
 * The rehearsal that compares one declared case (agent#10): the proposed
 * document over the case's own facts, evidence and extensions, declared a
 * rehearsal, as `experimental_evaluate` takes them.
 */
export function rehearsalOf(item: CriticCase, document: unknown): Record<string, unknown> {
  return {
    pack: JSON.stringify(document),
    facts: JSON.stringify(item.facts),
    ...(item.evidenceAvailability === undefined ? {} : { evidence: JSON.stringify(item.evidenceAvailability) }),
    ...(item.supportedExtensions === undefined ? {} : { supported_extensions: item.supportedExtensions }),
    rehearsal: true
  }
}

/**
 * **The comparison the critic's own calls left open, made by the harness**
 * (agent#10). After the critic's turns, the declared cases nothing compared are
 * run once through `call`, the same gated dispatch the critic's calls take:
 * one `experimental_test_cases` call over those stating a whole disposition or
 * an error class, where the critic is offered it, then one rehearsal each of
 * what is left, at most `CRITIC_REHEARSALS`, where it is offered
 * `experimental_evaluate`. A case the runtime gives no outcome for stays
 * `incomplete`. No model request is made.
 */
export async function compareTheRest(
  recorder: CritiqueRecorder,
  document: unknown,
  offered: ReadonlySet<string>,
  call: (name: string, args: Record<string, unknown>) => Promise<unknown>
): Promise<void> {
  // Only the cases the test tool can compare (review row 2); the rest by rehearsal.
  const whole = recorder.uncompared().filter(testable)
  if (whole.length > 0 && offered.has('experimental_test_cases')) {
    await call('experimental_test_cases', { pack: JSON.stringify(document), matrix: JSON.stringify(criticMatrix(whole)) })
  }
  if (!offered.has('experimental_evaluate')) return
  for (const item of recorder.uncompared().slice(0, CRITIC_REHEARSALS)) await call('experimental_evaluate', rehearsalOf(item, document))
}

/**
 * Whether a case can go to `experimental_test_cases` (agent#10 review row 2):
 * it expects an error class, or states the whole §8.3 disposition (`kind`,
 * `reasons`, `handoff`), which the runtime compares byte for byte. A case that
 * states part of a disposition (a request's `kind` and `outcomeId`) comes back
 * a mismatch there, so it is compared by rehearsal only.
 */
export function testable(item: CriticCase): boolean {
  if (item.expectedErrorClass !== undefined) return true
  const disposition = item.expectedDisposition ?? {}
  return ['kind', 'reasons', 'handoff'].every((member) => Object.hasOwn(disposition, member))
}

/** The declared cases as the runtime's matrix, which `experimental_test_cases` takes as it is. */
export function criticMatrix(cases: readonly CriticCase[]): { matrixVersion: '3'; cases: Record<string, unknown>[] } {
  return { matrixVersion: '3', cases: cases.map(({ source: _source, ...row }) => row) }
}

/** Stable-key JSON, so two values compare by content and not by member order. */
function canonical(value: unknown): string {
  if (value === null || typeof value !== 'object') return JSON.stringify(value) ?? 'null'
  if (Array.isArray(value)) return `[${value.map(canonical).join(',')}]`
  const entries = Object.entries(value as Record<string, unknown>).sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0))
  return `{${entries.map(([key, inner]) => `${JSON.stringify(key)}:${canonical(inner)}`).join(',')}}`
}

/** JSON text read back to its value, or undefined where it is not JSON text. */
function parsed(text: unknown): unknown {
  if (typeof text !== 'string') return undefined
  try {
    return JSON.parse(text) as unknown
  } catch {
    return undefined
  }
}

/** The runtime's answer as its JSON payload: the structured content, or the text read as JSON. */
function payloadOf(text: string, structured: unknown): Record<string, unknown> | null {
  if (isObject(structured)) return structured
  const value = parsed(text)
  return isObject(value) ? value : null
}

/** The expectation a declared case states, as a comparison reports it. */
function expectationOf(item: CriticCase): CaseOutcome {
  return item.expectedErrorClass !== undefined
    ? { errorClass: item.expectedErrorClass, ...(item.expectedErrorPhase === undefined ? {} : { phase: item.expectedErrorPhase }) }
    : { disposition: item.expectedDisposition! }
}

/**
 * Whether an outcome holds what a case expects: every member the expected
 * disposition states equal to the outcome's, or the expected error class (and
 * phase, where stated). A disposition where an error was expected, or the
 * reverse, never agrees.
 */
export function outcomeAgrees(expected: CaseOutcome, actual: CaseOutcome): boolean {
  if ('disposition' in expected) {
    if (!('disposition' in actual)) return false
    const wanted = asSets(expected.disposition)
    const got = asSets(actual.disposition)
    return Object.entries(wanted).every(([member, value]) => canonical(value) === canonical(got[member]))
  }
  if (!('errorClass' in actual) || actual.errorClass !== expected.errorClass) return false
  return expected.phase === undefined || actual.phase === expected.phase
}

/**
 * A disposition with its two §8.3 sets in one order (review row 3): `reasons`
 * and `handoff.triggeredBy` are sets, which the runtime serializes sorted and
 * compares as sets, so a case may list them in any order.
 */
function asSets(disposition: Record<string, unknown>): Record<string, unknown> {
  const sorted = (value: unknown) => (Array.isArray(value) ? [...value].sort((a, b) => (canonical(a) < canonical(b) ? -1 : canonical(a) > canonical(b) ? 1 : 0)) : value)
  const handoff = disposition.handoff
  return {
    ...disposition,
    ...(Object.hasOwn(disposition, 'reasons') ? { reasons: sorted(disposition.reasons) } : {}),
    ...(isObject(handoff) && Object.hasOwn(handoff, 'triggeredBy') ? { handoff: { ...handoff, triggeredBy: sorted(handoff.triggeredBy) } } : {})
  }
}

/** The evidence and extensions a rehearsal ran under, normalized as the runtime reads them. */
function sameInputs(item: CriticCase, args: Record<string, unknown>): boolean {
  if (!isObject(parsed(args.facts)) || canonical(parsed(args.facts)) !== canonical(item.facts)) return false
  // An omitted evidence document and an empty one both leave every requirement unknown.
  const evidence = args.evidence === undefined ? {} : parsed(args.evidence)
  if (!isObject(evidence) || canonical(evidence) !== canonical(item.evidenceAvailability ?? {})) return false
  const extensions = (value: unknown) => canonical(Array.isArray(value) ? [...value].map(String).sort() : [])
  return extensions(args.supported_extensions) === extensions(item.supportedExtensions)
}

/** The pack a call evaluated is the proposed document, read as JSON. Never a pack_id. */
function isTheDocument(pack: unknown, document: unknown): boolean {
  const value = parsed(pack)
  return value !== undefined && canonical(value) === canonical(document)
}

/**
 * The comparisons one rehearsal makes: each declared case whose facts,
 * evidence and extensions it ran, over the proposed document, against the
 * outcome the runtime gave.
 */
function compareRehearsal(cases: readonly CriticCase[], document: unknown, args: Record<string, unknown>, payload: Record<string, unknown>): CaseComparison[] {
  if (!isTheDocument(args.pack, document)) return []
  const failure = payload.evaluationError
  const actual: CaseOutcome | null =
    payload.status === 'evaluated' && isObject(payload.disposition)
      ? { disposition: payload.disposition }
      : isObject(failure) && typeof failure.class === 'string'
        ? { errorClass: failure.class, ...(typeof failure.phase === 'string' ? { phase: failure.phase } : {}) }
        : null
  if (actual === null) return []
  return cases.filter((item) => sameInputs(item, args)).map((item) => {
    const expected = expectationOf(item)
    return { case: item.id, source: item.source, tool: 'experimental_evaluate', agrees: outcomeAgrees(expected, actual), expected, actual }
  })
}

/**
 * The comparisons one test run makes: each row of a declared case, where the
 * matrix the critic sent carried that case exactly as the host declared it and
 * the pack was the proposed document. The runtime compared the row; its
 * status is the verdict and its actual outcome is reported. A row of a case
 * the critic changed or wrote itself compares nothing.
 */
function compareTestRun(cases: readonly CriticCase[], document: unknown, args: Record<string, unknown>, payload: Record<string, unknown>): CaseComparison[] {
  if (!isTheDocument(args.pack, document)) return []
  const matrix = parsed(args.matrix)
  const sent = isObject(matrix) && Array.isArray(matrix.cases) ? matrix.cases.filter(isObject) : []
  const packs = Array.isArray(payload.packs) ? payload.packs.filter(isObject) : []
  const rows = packs.flatMap((pack) => (Array.isArray(pack.rows) ? pack.rows.filter(isObject) : []))
  const found: CaseComparison[] = []
  for (const row of rows) {
    // A case stating part of a disposition is never compared by a test run (review row 2).
    const item = cases.find((candidate) => candidate.id === row.id && testable(candidate))
    if (item === undefined) continue
    const declared = criticMatrix([item]).cases[0]
    if (!sent.some((candidate) => candidate.id === item.id && canonical(candidate) === canonical(declared))) continue
    const disposition = parsed(row.actual)
    const actual: CaseOutcome | undefined =
      typeof row.actualErrorClass === 'string' && row.actualErrorClass !== ''
        ? { errorClass: row.actualErrorClass, ...(typeof row.actualErrorPhase === 'string' && row.actualErrorPhase !== '' ? { phase: row.actualErrorPhase } : {}) }
        : isObject(disposition) ? { disposition } : undefined
    found.push({ case: item.id, source: item.source, tool: 'experimental_test_cases', agrees: row.status === 'passed', expected: expectationOf(item), ...(actual === undefined ? {} : { actual }) })
  }
  return found
}

/** An outcome as a person reads it: the members the case states, or the error class. */
function outcomeText(outcome: CaseOutcome | undefined, shape?: CaseOutcome): string {
  if (outcome === undefined) return 'no outcome'
  if ('errorClass' in outcome) return `error class ${outcome.errorClass}${outcome.phase === undefined ? '' : ` (${outcome.phase})`}`
  const members = shape !== undefined && 'disposition' in shape ? Object.keys(shape.disposition) : Object.keys(outcome.disposition)
  return canonical(Object.fromEntries(members.map((member) => [member, outcome.disposition[member]])))
}

/** The comparisons, as a person is shown them. */
export function comparisonText(cases: readonly CaseComparison[], incomplete: readonly string[]): string {
  const disagree = cases.filter((item) => !item.agrees)
  const agree = new Set(cases.filter((item) => item.agrees).map((item) => item.case))
  const parts = disagree.map((item) =>
    `case ${item.case} (${item.source}) disagrees: declared ${outcomeText(item.expected)}, ${item.tool} gave ${outcomeText(item.actual, item.expected)}`)
  if (agree.size > 0) parts.push(`${agree.size} declared case(s) agree`)
  if (incomplete.length > 0) parts.push(`incomplete, ${incomplete.length} declared case(s) not compared, so nothing here says the proposal holds: ${incomplete.join(', ')}`)
  return `compared with the declared outcomes: ${parts.join('; ')}`
}

/** The runtime's `status` and diagnostic count out of one tool answer. */
export function statusOf(text: string): { status: string; diagnostics: number } | null {
  let payload: { status?: unknown; diagnostics?: unknown }
  try {
    payload = JSON.parse(text) as { status?: unknown; diagnostics?: unknown }
  } catch {
    return null
  }
  if (typeof payload?.status !== 'string' || payload.status === '') return null
  return {
    status: payload.status,
    diagnostics: Array.isArray(payload.diagnostics) ? payload.diagnostics.length : 0
  }
}

/**
 * **The verdict, from the runtime's results and from nothing else.**
 *
 * ADR-0001's second rule. `refuted` is true exactly where the critic caused at
 * least one check and at least one of them came back as something other than
 * the runtime's own word for "this went through". A critic whose prose says
 * "REFUTED" over a `validate` the runtime called `valid` produces
 * `refuted: false`, and a critic whose prose says "none found" over a
 * `validate` the runtime called `invalid` produces `refuted: true`.
 */
export function verdictOf(checks: readonly Check[], cases: readonly CaseComparison[] = [], incomplete: readonly string[] = []): boolean {
  // agent#10: a rehearsal that says "evaluated" with an outcome other than the
  // one a declared case states refutes too, so a semantic error is visible;
  // and a declared case nothing compared is unsettled (review row 1), so a
  // critique that did not reach every declared case is never "not refuted".
  return (checks.length > 0 || cases.length > 0 || incomplete.length > 0) &&
    (checks.some((check) => check.status !== SETTLED_STATUS[check.tool]) || cases.some((item) => !item.agrees) || incomplete.length > 0)
}

/** The runtime's answers, quoted — the text a person is shown as the verdict. */
export function quoteOf(checks: readonly Check[]): string {
  if (checks.length === 0) return NO_CHECKS
  return `quoted from the runtime: ${checks
    .map(
      (check) =>
        `${check.tool} → "status": "${check.status}", ${check.diagnostics} diagnostic(s)`
    )
    .join('; ')}`
}

/**
 * What the desk saw the critic cause, collected as it happens.
 *
 * The engine hands it every tool answer that came back **through the gate**;
 * this decides which of them is a check. An engine cannot add a check by
 * saying so, and cannot suppress one by not mentioning it — it reports every
 * answer it received, and the filtering is here.
 */
export interface CritiqueRecorder {
  /**
   * One answer **the runtime gave** the critic.
   *
   * The contract is in the name: an engine calls this only where its own
   * `callTool` returned — where the frame passed the ToolGate, reached
   * `jpack mcp`, and came back. A call the gate refused never gets here, and
   * neither does one a cancelled run abandoned. `args` are the arguments the
   * call left with and `structured` the answer's structured content, which the
   * outcome comparison reads (agent#10).
   */
  saw(tool: string, text: string, args?: Record<string, unknown>, structured?: unknown): void
  /** The declared cases no answer has compared yet (agent#10). */
  uncompared(): CriticCase[]
  /** The critique, once the critic has stopped calling tools. */
  critique(modelText: string): Critique
}

/**
 * What the comparison needs (agent#10): the cases the host declares and the
 * document the critic is checking. With no cases there is nothing to compare,
 * and the critique is the checks alone.
 */
export interface CritiqueOptions {
  cases?: readonly CriticCase[]
  document?: unknown
}

export function openCritique(options: CritiqueOptions = {}): CritiqueRecorder {
  const checks: Check[] = []
  const declared = options.cases ?? []
  const compared: CaseComparison[] = []
  return {
    /**
     * **A check is a thing the runtime said, and nothing else.**
     *
     * Two ways this used to manufacture one, and both are closed here. A
     * refusal by the desk's own gate arrived as `{isError: true}` with the
     * gate's sentence in it, and was recorded as a check with the status
     * `refused` — so a critic that asked for a `validate` the file never
     * granted made the tab say *the runtime refuted this proposal* about a
     * call that never left the page. And the table was read with `in`, so a
     * tool named `toString` was a check the table does not name.
     *
     * Now: only answers that came back through the gate reach this at all (see
     * `saw`), only names the table holds are read, and a status comes only out
     * of the runtime's own JSON. An answer with no `status` is not a check —
     * including an in-band `isError` from the runtime, which is the runtime
     * declining to *answer* rather than declining the document.
     */
    saw(tool, text, args, structured) {
      // agent#10: the outcomes, compared with the declared cases, from the
      // runtime's own answer and the arguments the call left with.
      if (declared.length > 0 && args !== undefined) {
        const payload = payloadOf(text, structured)
        if (payload !== null && tool === 'experimental_evaluate') compared.push(...compareRehearsal(declared, options.document, args, payload))
        if (payload !== null && tool === 'experimental_test_cases') compared.push(...compareTestRun(declared, options.document, args, payload))
      }
      if (!isCheckTool(tool)) return
      const said = statusOf(text)
      if (said === null) return
      checks.push({ tool, status: said.status, diagnostics: said.diagnostics })
    },
    uncompared() {
      const reached = new Set(compared.map((item) => item.case))
      return declared.filter((item) => !reached.has(item.id))
    },
    critique(modelText) {
      if (declared.length === 0) return { refuted: verdictOf(checks), checks: [...checks], text: quoteOf(checks), modelText }
      const reached = new Set(compared.map((item) => item.case))
      const incomplete = declared.filter((item) => !reached.has(item.id)).map((item) => item.id)
      const quoted = checks.length > 0 || compared.length > 0
        ? [...(checks.length > 0 ? [quoteOf(checks)] : []), comparisonText(compared, incomplete)].join('; ')
        : `${NO_CHECKS}; ${comparisonText(compared, incomplete)}`
      return { refuted: verdictOf(checks, compared, incomplete), checks: [...checks], text: quoted, modelText, cases: [...compared], incomplete }
    }
  }
}

/**
 * The critique a pass that **could not run** produces, or null where it can.
 *
 * The instructions a critic works from are the runtime's; this desk adds one
 * sentence to them and has none of its own to fall back on. A run that reached
 * the pass without the runtime's testing prompt — a runtime that advertises no
 * `test_pack` — therefore reports that, rather than putting a critic in front
 * of a document with only the desk's sentence to go on. Zero checks, so the
 * proposal is shown without a refutation line.
 */
export function criticCannotRun(testPrompt: string): Critique | null {
  if (testPrompt.trim() !== '') return null
  return { refuted: false, checks: [], text: NO_TEST_PROMPT, modelText: '' }
}

/**
 * The critic's first message: the runtime's testing prompt, the desk's one
 * sentence, and the document.
 *
 * The document is **fenced**, which is the shape the runtime's own prompts use
 * for caller-supplied material, so the critic can tell what it was handed from
 * the instructions around it.
 *
 * `testPrompt` is the runtime's `test_pack` prompt as `prompts/get` served it,
 * and it is never empty here: a pass with no runtime prompt does not run at all
 * (`criticCannotRun`). It is read **without** its `pack` argument, and the
 * reason is honest rather than convenient: the document does not exist when a
 * session's prompts are read, and a prompt fetched mid-run for each proposal
 * would be a second `prompts/get` inside a pass whose whole point is that it
 * costs one call. The document is handed over below instead, verbatim.
 */
export function criticMessage(testPrompt: string, document: unknown, cases: readonly CriticCase[] = []): string {
  const fenced = `\`\`\`json\n${JSON.stringify(document, null, 2)}\n\`\`\``
  const guidance = testPrompt.trim() === '' ? '' : `${testPrompt}\n\n`
  // agent#10: the cases whose outcomes the host declares. Those the test tool
  // can compare as the runtime's matrix, to run as it is; the cases stating
  // part of a disposition apart, for rehearsal only (review row 2).
  const whole = cases.filter(testable)
  const partial = cases.filter((item) => !testable(item))
  const block = (value: unknown) => `\`\`\`json\n${JSON.stringify(value, null, 2)}\n\`\`\``
  const matrix = whole.length === 0 ? '' : `\n\n${CRITIC_CASES_SENTENCE}\n\n${block(criticMatrix(whole))}`
  const rehearse = partial.length === 0 ? '' : `\n\n${CRITIC_REHEARSE_SENTENCE}\n\n${block(criticMatrix(partial).cases)}`
  return `${guidance}${CRITIC_SENTENCE}\n\n${fenced}${matrix}${rehearse}`
}

/**
 * The second sentence the desk adds, where the host declares cases
 * (agent#10): what they are, how to run them, and that the comparison is the
 * host's. It names the two tools and their arguments and no verdict.
 */
export const CRITIC_CASES_SENTENCE =
  'The matrix below declares the outcome each case must have, its whole disposition or its error class: the pack’s tests and the request’s own cases. Run it against the document above with experimental_test_cases (pack: the document as JSON text; matrix: this matrix as it is), or rehearse each case with experimental_evaluate (pack: the document as JSON text; facts and evidence: the case’s facts and evidenceAvailability as JSON text). The host compares each outcome with the declared one.'

/**
 * The third, for the cases that state part of a disposition (review row 2):
 * rehearsal only, since the test tool compares a whole disposition.
 */
export const CRITIC_REHEARSE_SENTENCE =
  'The cases below state part of the outcome they must have. Rehearse each one against the document above with experimental_evaluate (pack: the document as JSON text; facts and evidence: the case’s facts and evidenceAvailability as JSON text); experimental_test_cases does not take them. The host compares each outcome with the declared one.'

/** The contract's own event, built in one place from the critique. */
export function critiqueEvent(critique: Critique): AssistantEvent {
  return {
    type: 'critique',
    refuted: critique.refuted,
    checks: critique.checks.map((check) => ({ tool: check.tool, status: check.status })),
    text: critique.text,
    ...(critique.cases === undefined ? {} : { cases: critique.cases, incomplete: critique.incomplete ?? [] }),
    ...(critique.figures === undefined ? {} : { figures: critique.figures })
  }
}

/**
 * What the proposal carries where something after it did not finish: the
 * critic (its provider error, length cutoff, the allowance or the turn's wall
 * clock), or, for a proposal made through `propose_pack`, a later request of
 * the turn. The proposal is the finished work and is delivered; the failure is
 * a note on it, never an error raised ahead of it.
 */
export function proposalNote(kind: ProposalNote['kind'], cause: unknown, source: FailureSource): ProposalNote {
  const failure = normalizeFailure(cause, source)
  // Nothing pauses here: the run ends with the proposal, so the allowance's
  // own sentence ("Work paused …") would say what did not happen.
  const why = failure.code === 'request-limit' ? 'The request allowance was used up before it finished.' : failureMessage(failure)
  const what = kind === 'critic-unfinished'
    ? 'The critic did not finish, so it checked nothing on this proposal.'
    : 'The turn stopped after this proposal was made, so nothing after it ran: no closing summary and no critic check.'
  return { kind, message: `${what} ${why}`, failure }
}
/** The critic's note: see `proposalNote`. It carries what the critic cost before it stopped (agent#10). */
export function criticNote(cause: unknown, source: FailureSource, figures?: CriticFigures): ProposalNote {
  return { ...proposalNote('critic-unfinished', cause, source), ...(figures === undefined ? {} : { figures }) }
}

/**
 * What the critic cost on a model engine (agent#10): the requests it claimed
 * against the allowance, and the token counts its completion rows reported,
 * summed. `before` is the execution's state when the critic started.
 */
export function modelCriticFigures(
  model: string,
  before: { used: number; rows: number },
  after: { used: number; completions?: readonly { phase: string; result: object }[] }
): CriticFigures {
  const rows = (after.completions ?? []).slice(before.rows).filter((row) => row.phase === 'review')
  const figures: CriticFigures = { model, unit: 'model-requests', requests: Math.max(0, after.used - before.used) }
  for (const member of ['inputTokens', 'outputTokens', 'reasoningTokens', 'cacheReadTokens', 'cacheWriteTokens'] as const) {
    const counted = rows.map((row) => (row.result as Record<string, unknown>)[member]).filter((value): value is number => typeof value === 'number')
    if (counted.length > 0) figures[member] = counted.reduce((sum, value) => sum + value, 0)
  }
  return figures
}

/**
 * What the proposal event carries about the pass, or nothing.
 *
 * ADR-0001's first rule again, at the other end: with no checks there is no
 * refutation line on the proposal at all. The proposal is still shown — the
 * critic reaching nothing is not a reason to withhold a document — and the
 * stream says what happened.
 */
export function critiqueOnProposal(
  critique: Critique | null
): { critique: { refuted: boolean; incomplete?: string[] } } | Record<string, never> {
  // A compared case is evidence as a check is (agent#10), and a declared case
  // nothing compared is named here, never left to a plain not-refuted (review row 1).
  const incomplete = critique?.incomplete ?? []
  if (critique === null || (critique.checks.length === 0 && (critique.cases ?? []).length === 0 && incomplete.length === 0)) return {}
  return { critique: { refuted: critique.refuted, ...(incomplete.length === 0 ? {} : { incomplete }) } }
}
