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
 * **The critic's tools are the session's, which is what the third rule means.**
 * Neither engine builds the critic a dispatch of its own: it runs on the same
 * `guardedCallTool`, over the same five tools, through the same ToolGate on the
 * same transport — so its `experimental_evaluate` is rewritten to a rehearsal
 * before the frame leaves the page, and it can no more reach `write_file` than
 * the main loop can. The desk exposes no ungated route to an engine at all, and
 * the mutation that removes the gate takes the critic with it.
 *
 * **Nothing here writes.** The pass runs after the main loop produced a
 * proposal and *before* the proposal event is emitted, and every call it makes
 * is a read.
 */
import { failureMessage, normalizeFailure, type FailureSource } from './failures'
import type { AssistantEvent, ProposalNote } from './engine'

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
  /** Computed from the runtime's own statuses. Never from prose. */
  refuted: boolean
  checks: Check[]
  /** The runtime's words, quoted. */
  text: string
  /** The critic's own words, kept beside the verdict and never deciding it. */
  modelText: string
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
export function verdictOf(checks: readonly Check[]): boolean {
  return checks.length > 0 && checks.some((check) => check.status !== SETTLED_STATUS[check.tool])
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
   * neither does one a cancelled run abandoned.
   */
  saw(tool: string, text: string): void
  /** The critique, once the critic has stopped calling tools. */
  critique(modelText: string): Critique
}

export function openCritique(): CritiqueRecorder {
  const checks: Check[] = []
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
    saw(tool, text) {
      if (!isCheckTool(tool)) return
      const said = statusOf(text)
      if (said === null) return
      checks.push({ tool, status: said.status, diagnostics: said.diagnostics })
    },
    critique(modelText) {
      return { refuted: verdictOf(checks), checks: [...checks], text: quoteOf(checks), modelText }
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
export function criticMessage(testPrompt: string, document: unknown): string {
  const fenced = `\`\`\`json\n${JSON.stringify(document, null, 2)}\n\`\`\``
  const guidance = testPrompt.trim() === '' ? '' : `${testPrompt}\n\n`
  return `${guidance}${CRITIC_SENTENCE}\n\n${fenced}`
}

/** The contract's own event, built in one place from the critique. */
export function critiqueEvent(critique: Critique): AssistantEvent {
  return {
    type: 'critique',
    refuted: critique.refuted,
    checks: critique.checks.map((check) => ({ tool: check.tool, status: check.status })),
    text: critique.text
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
/** The critic's note: see `proposalNote`. */
export function criticNote(cause: unknown, source: FailureSource): ProposalNote {
  return proposalNote('critic-unfinished', cause, source)
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
): { critique: { refuted: boolean } } | Record<string, never> {
  if (critique === null || critique.checks.length === 0) return {}
  return { critique: { refuted: critique.refuted } }
}
