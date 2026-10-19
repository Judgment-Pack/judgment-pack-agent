import { describe, expect, it } from 'vitest'
import { scriptedModel, thinkSignature } from './scriptedModel'

/**
 * The scripted Gemini endpoint's own rule about where a signature may ride.
 *
 * The engine under test never reaches this rule with an empty signed thought:
 * the SDK drops one before the desk sees it (`SDK_DROPS_EMPTY_SIGNED_THOUGHTS`),
 * so the conformance legs cannot notice a validator that calls that shape
 * malformed. This test speaks to the endpoint directly, as a client that
 * replays the wire faithfully would.
 */
const URL_OF = 'https://endpoint.invalid/v1beta/models/m:generateContent'
const ASKS = { generationConfig: { thinkingConfig: { includeThoughts: true, thinkingBudget: 1024 } } }

async function post(model: { fetch: typeof fetch }, body: unknown): Promise<Response> {
  return model.fetch(URL_OF, { method: 'POST', body: JSON.stringify(body) })
}

/** First turn, then a continuation whose model turn is `rewrite` of what came back. */
async function continuation(rewrite: (parts: Record<string, unknown>[]) => Record<string, unknown>[]) {
  const model = scriptedModel({ api: 'gemini', answerAs: 'whole', thinking: 'on', signatures: 'empty' })
  const first = await post(model, { contents: [{ role: 'user', parts: [{ text: 'go' }] }], ...ASKS })
  expect(first.status).toBe(200)
  const answered = (await first.json()) as {
    candidates: { content: { parts: Record<string, unknown>[] } }[]
  }
  const parts = answered.candidates[0]!.content.parts
  const call = parts.find((part) => part.functionCall !== undefined) as
    | { functionCall: { name: string } }
    | undefined
  if (call === undefined) throw new Error('the first scripted step is not a tool call')
  const second = await post(model, {
    contents: [
      { role: 'user', parts: [{ text: 'go' }] },
      { role: 'model', parts: rewrite(parts) },
      {
        role: 'user',
        parts: [{ functionResponse: { name: call.functionCall.name, response: { ok: true } } }]
      }
    ],
    ...ASKS
  })
  return { second, request: model.requests[1]! }
}

describe('the scripted Gemini endpoint, replayed by hand', () => {
  it('accepts an empty signed thought replayed exactly as it was sent', async () => {
    const { second, request } = await continuation((parts) => parts)
    const signature = thinkSignature({ id: 'T1' })
    // The fixture really sent the shape under test: signed, thought, no text.
    expect(request.modelParts[0]!.slice(0, 2)).toEqual([
      { text: '', thought: true, thoughtSignature: signature },
      { text: '', thought: true, thoughtSignature: `${signature}.2` }
    ])
    if (second.status !== 200) {
      throw new Error(`an empty signed thought was refused: ${await second.text()}`)
    }
    expect(request.signaturesMalformed).toEqual([])
    expect(request.signaturesMissing).toEqual([])
  })

  it('still calls a signature on a plain text part malformed', async () => {
    // The rule is not gone, only exact: a signature rides on a thought part or
    // a function call, and nowhere else.
    const signature = thinkSignature({ id: 'T1' })
    const { second, request } = await continuation((parts) =>
      parts.map((part) =>
        part.thoughtSignature === signature ? { text: 'plain', thoughtSignature: signature } : part
      )
    )
    expect(request.signaturesMalformed).toEqual([signature])
    expect(second.status).toBe(400)
  })
})
