/**
 * The strict turn order (agent#9): a scripted endpoint behind a chat template that refuses two user
 * turns in a row or a user text after a tool result, as Mistral's does. Moved here from
 * `conformance.test.ts` so the shared server (`scripts/scripted-server.mjs`, the `strict` endpoint
 * option) refuses by the same rule the TypeScript legs do.
 */
import type { EndpointKind } from '../host'

/**
 * The turn order a strict chat template refuses, on each wire: two user turns
 * in a row, or a user text after a tool result. Such templates (Mistral's, for
 * one) check that user and assistant alternate.
 */
export function alternationRefused(api: EndpointKind, body: Record<string, unknown>): string {
  type Turn = { role: string; parts: { kind: 'text' | 'result' | 'other' }[] }
  const kind = (part: Record<string, unknown>): 'text' | 'result' | 'other' =>
    part.type === 'tool_result' || 'functionResponse' in part ? 'result' : part.type === 'text' || (api === 'gemini' && typeof part.text === 'string' && part.thought !== true) ? 'text' : 'other'
  const turns: Turn[] =
    api === 'gemini'
      ? ((body.contents ?? []) as { role: string; parts: Record<string, unknown>[] }[]).map((content) => ({ role: content.role, parts: content.parts.map((part) => ({ kind: kind(part) })) }))
      : ((body.messages ?? []) as { role: string; content: unknown }[])
          .filter((message) => message.role !== 'system')
          .map((message) => ({
            role: message.role,
            parts: message.role === 'tool' ? [{ kind: 'result' as const }] : typeof message.content === 'string' ? [{ kind: 'text' as const }] : ((message.content ?? []) as Record<string, unknown>[]).map((part) => ({ kind: kind(part) }))
          }))
  for (const [at, turn] of turns.entries()) {
    const before = turns[at - 1]
    if (turn.role === 'user' && before?.role === 'user') return `two user turns in a row at ${at}`
    if (turn.role === 'user' && before?.role === 'tool' && turn.parts.some((part) => part.kind === 'text')) return `a user text after a tool result at ${at}`
    const result = turn.parts.findIndex((part) => part.kind === 'result')
    if (result >= 0 && turn.parts.slice(result).some((part) => part.kind === 'text')) return `a user text after a tool result at ${at}`
  }
  return ''
}

/** A scripted endpoint behind a strict chat template: a 400 for a turn order it refuses. */
export function strictAlternation(api: EndpointKind, inner: typeof fetch): typeof fetch {
  return (async (input: RequestInfo | URL, init?: RequestInit) => {
    const refused = typeof init?.body === 'string' ? alternationRefused(api, JSON.parse(init.body) as Record<string, unknown>) : ''
    if (refused !== '') {
      return new Response(JSON.stringify({ error: { message: `Conversation roles must alternate user/assistant/user/assistant: ${refused}` } }), {
        status: 400,
        headers: { 'content-type': 'application/json' }
      })
    }
    return inner(input, init)
  }) as typeof fetch
}
