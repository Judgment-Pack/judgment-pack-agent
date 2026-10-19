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
 * **Why it holds on every request.** The adapter puts the request text first in the run's first
 * user message and never moves, replaces or drops that message: the execution state joins it only
 * as a later part (`placeState`), a chat's index of earlier results is its second part
 * (`context/chat.ts`), and compaction keeps it whole. Everything else the loop adds comes after it:
 * tool results (a text-path result is a user text headed `TOOL RESULT` or `TOOL ERROR`), the images
 * shown beside a result, the finishing and reformat instructions, the compaction summary, and the
 * state. So the newest user text is often not the request; the first user turn always is. The
 * critic's requests are a second conversation, whose first user turn is the critic's own.
 */
import { STATE_HEADER } from '../engines/vercel/loop'
import type { EndpointKind } from '../host'

/** What the rule found: the text, and the JSON pointer (RFC 6901) to it in the request body. */
export interface RecallQuery {
  pointer: string
  text: string
}

type Json = Record<string, unknown>

/** The rule, applied to one request body as the adapter sent it; null where no first user turn carries a text. */
export function recallQuery(api: EndpointKind, body: Json): RecallQuery | null {
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
