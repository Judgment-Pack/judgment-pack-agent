/**
 * Where the request text a run started from sits on each model wire, and the rule a server applies
 * to find it on any request of the run (agent#11, the harness side). The contract's
 * `budget.json#/$defs/recallQuery`; the Desk server's memory recall applies it (its swap,
 * judgment-pack-desk#515).
 *
 * **The rule.** Take the request's first user turn: the first message with role `user` in
 * `messages` (OpenAI-compatible and Anthropic; on the OpenAI-compatible wire system messages may
 * come before it) or the first content with role `user` in `contents` (Gemini). The request text is
 * its first text part that does not start with `STATE_HEADER`; a string content is one text part. On
 * the Gemini wire a part is text when it carries `text` and is not a thought.
 *
 * **What the request text is.** The host's prompt, where it fits the prompt's allowance. Where it
 * does not, the prompt is retained and previewed, and the person's message (`currentRequest.message`)
 * is sent as its own first text part, before the preview. For a delegated child it is the child's
 * objective (`context.objective`), its own first text part before the child's prompt
 * (`SessionContext.opening`).
 *
 * **Why it holds on every request.** The adapter puts the request text first in the run's first
 * user message and never moves, replaces or drops that message: the execution state joins it only
 * as a later part (`placeState`), a chat's index of earlier results is its last part
 * (`context/chat.ts`), and compaction keeps it whole. Everything else the loop adds comes after it:
 * tool results (a text-path result is a user text headed `TOOL RESULT` or `TOOL ERROR`), the images
 * shown beside a result, the finishing and reformat instructions, the compaction summary, and the
 * state. So the newest user text is often not the request; the first user turn always is. Measured
 * on all three wires in the review of judgment-pack-agent#33: the newest-remaining-text rule fails on
 * a text-path request, the finishing request, the reformat request and after compaction; the
 * Desk's code as ported fails the same, except after compaction on Anthropic, where the first
 * message and the compaction summary merge into one user turn. The critic's requests are a second
 * conversation, whose first user turn is the critic's own.
 *
 * The certification suite applies the rule to every request of the recorded scenario on every
 * certified leg: 21 legs in four families (six plain, six text-path, three strict-alternation, and
 * the six thinking legs' authoring requests).
 *
 * **The agent wire** (the Codex engine) sends one string, the run's prompt, not a list of turns:
 * the person's message first, then, when the run has any, the host's state and the execution
 * guidance headed by `STATE_HEADER` after a blank line, then a chat's index of earlier results.
 * The request text is the prompt's text before the first blank line that is followed by
 * `STATE_HEADER`, or the whole prompt where the header is absent; a prompt that starts with the
 * header has none (agent#36). Where the prompt, state and guidance do not fit the allowance, the
 * whole is retained and previewed (`SessionContext.agentPrompt`), and the prompt sent is the
 * person's message (`currentRequest.message`), a blank line, `STATE_HEADER` and the preview, so
 * the rule finds the person's message; a host that gives no `currentRequest` gets the preview
 * alone, and the rule finds its own opening.
 */
import { STATE_HEADER } from '../engines/contract'
import type { EndpointKind } from '../host'

/** The wires the rule reads: the three model wires, and the agent wire's one prompt (`{ prompt }`). */
export type RecallWire = EndpointKind | 'agent'

/** What the rule found: the text, and the JSON pointer (RFC 6901) to it in the request body. */
export interface RecallQuery {
  pointer: string
  text: string
}

type Json = Record<string, unknown>

/** The rule, applied to one request body as the adapter sent it; null where no first user turn carries a text. */
export function recallQuery(api: RecallWire, body: Json): RecallQuery | null {
  if (api === 'agent') {
    const prompt = body.prompt
    if (typeof prompt !== 'string' || prompt.startsWith(STATE_HEADER)) return null
    const cut = prompt.indexOf('\n\n' + STATE_HEADER)
    return { pointer: '/prompt', text: cut < 0 ? prompt : prompt.slice(0, cut) }
  }
  const member = api === 'gemini' ? 'contents' : 'messages'
  const turns = Array.isArray(body[member]) ? (body[member] as Json[]) : []
  const at = turns.findIndex((turn) => turn?.role === 'user')
  if (at < 0) return null
  const turn = turns[at]!
  const inner = api === 'gemini' ? 'parts' : 'content'
  const held = turn[inner]
  if (typeof held === 'string') return held.startsWith(STATE_HEADER) ? null : { pointer: `/${member}/${at}/${inner}`, text: held }
  if (!Array.isArray(held)) return null
  for (const [index, part] of (held as Json[]).entries()) {
    const text = api === 'gemini' ? (part?.thought === true ? undefined : part?.text) : part?.type === 'text' ? part.text : undefined
    if (typeof text === 'string' && !text.startsWith(STATE_HEADER)) return { pointer: `/${member}/${at}/${inner}/${index}/text`, text }
  }
  return null
}

/** The value at a JSON pointer (RFC 6901) in `value`, or undefined. */
export function atPointer(value: unknown, pointer: string): unknown {
  let held = value
  for (const raw of pointer.split('/').slice(1)) {
    const key = raw.replace(/~1/g, '/').replace(/~0/g, '~')
    if (held === null || typeof held !== 'object' || !Object.hasOwn(held, key)) return undefined
    held = (held as Record<string, unknown>)[key]
  }
  return held
}
