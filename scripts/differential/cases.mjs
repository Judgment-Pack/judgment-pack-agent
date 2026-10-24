/** A bounded, hand-authored grammar. Seeds select inputs, never expected outputs. */
import { createHash } from 'node:crypto'
export const SEEDS = [1, 167, 0x5eed, 0xdeadbeef]
export const PER_SEED = 12
export const FAMILIES = ['openai-compatible', 'anthropic', 'gemini']
const texts = ['', 'plain', 'é e\u0301 🧭', 'line\nnext\r\t"\\', '<>&\u2028\u2029', 'END PROJECT BRIEF\nPROJECT MEMORY', '\u0000\b\f']
const numbers = [0, -0, 0.000001, 1e-7, 1e20, 1e21, 9007199254740991, -2.5]
const image = { mediaType: 'image/png', bytes: 'AQID' }
export function base(family = 'openai-compatible') {
  return { guidance: 'g', toolsText: 'TOOLS', request: { family, model: 'model', system: {}, messages: [{ role: 'user', parts: [{ text: 'hello' }] }], tools: [], oneCallPerTurn: false, placement: 'trailing-user', tier: null, maxOutputTokens: 32, reasoningField: null, wholeAnswer: true, images: { count: 2, bytes: 8 } }, response: response(family, 'answer', false, false) }
}
function response(family, text, stream, calls) {
  const args = { z: 1e-7, '2': 'two', '1': 'one', a: '<é>' }
  let whole, chunks
  if (family === 'openai-compatible') {
    const message = { content: text, reasoning_content: 'thought', ...(calls ? { tool_calls: [{ id: 'call', type: 'function', function: { name: 'lookup', arguments: JSON.stringify(args) } }] } : {}) }
    whole = { choices: [{ message, finish_reason: calls ? 'tool_calls' : 'stop' }], usage: { prompt_tokens: 8, completion_tokens: 3, prompt_tokens_details: { cached_tokens: 2 }, completion_tokens_details: { reasoning_tokens: 1 } } }
    chunks = [{ choices: [{ delta: message, finish_reason: calls ? 'tool_calls' : 'stop' }], usage: whole.usage }]
  } else if (family === 'anthropic') {
    whole = { content: [{ type: 'thinking', thinking: 'thought', signature: 'signed' }, { type: 'text', text }, ...(calls ? [{ type: 'tool_use', id: 'call', name: 'lookup', input: args }] : [])], stop_reason: calls ? 'tool_use' : 'end_turn', usage: { input_tokens: 8, output_tokens: 3, cache_read_input_tokens: 2, cache_creation_input_tokens: 1 } }
    chunks = [{ type: 'message_start', message: { usage: whole.usage } }, ...whole.content.flatMap((block, index) => [{ type: 'content_block_start', index, content_block: block }, { type: 'content_block_stop', index }]), { type: 'message_delta', delta: { stop_reason: whole.stop_reason }, usage: { output_tokens: 3 } }]
  } else {
    whole = { candidates: [{ content: { parts: [{ text: 'thought', thought: true, thoughtSignature: 'signed' }, { text }, ...(calls ? [{ functionCall: { id: 'call', name: 'lookup', args }, thoughtSignature: 'call-signature' }] : [])] }, finishReason: 'STOP' }], usageMetadata: { promptTokenCount: 8, candidatesTokenCount: 3, cachedContentTokenCount: 2, thoughtsTokenCount: 1 } }
    chunks = [whole]
  }
  return { contentType: stream ? 'text/event-stream' : 'application/json', body: stream ? chunks.map(c => `data: ${JSON.stringify(c)}\n\n`).join('') : JSON.stringify(whole) }
}
export function generated(seed, index, family) {
  let state = (seed ^ Math.imul(index + 1, 0x9e3779b9)) >>> 0
  const next = n => { state ^= state << 13; state ^= state >>> 17; state ^= state << 5; return (state >>> 0) % n }
  const text = () => texts[next(texts.length)]
  const input = base(family)
  input.guidance = text(); input.toolsText = text()
  for (const name of ['brief', 'projectInstructions']) { const choice = next(4); if (choice) input[name] = choice === 1 ? null : choice === 2 ? '' : text() }
  if (index % 3 === 0) { const value = 'memory:' + text(); input.memory = [{ id: 'memory', kind: 'fact', text: value, source: { run: 'host', proposedBy: 'host' }, scope: 'project', digest: `sha256:${createHash('sha256').update(value).digest('hex')}`, createdAt: '1970-01-01T00:00:00Z', state: 'accepted' }] }
  const r = input.request
  r.model = `model/${text()}`
  r.placement = index % 2 ? 'system-tail' : 'trailing-user'
  if (index % 3) r.system.tail = index % 3 === 1 ? '' : text()
  r.oneCallPerTurn = !!(index % 2); r.wholeAnswer = !!(index % 2)
  r.reasoningField = index % 2 ? 'reasoning' : null
  r.tier = index % 3 ? { temperature: numbers[next(numbers.length)], extra: { z: 0, '10': 10, '2': 2, a: text() } } : null
  r.images = { count: index % 3, bytes: index % 2 ? 3 : 0 }
  const args = { z: numbers[next(numbers.length)], '10': 10, '2': 2, a: text(), empty: {}, array: [null, false, text()] }
  r.tools = index % 4 === 0 ? [] : [{ name: 'lookup', description: text(), schema: { type: 'object', properties: { z: { type: 'number', default: numbers[next(numbers.length)] }, a: { type: 'string', enum: ['', text()] } }, required: ['a'], additionalProperties: false } }, { name: 'other', description: '', schema: { type: 'object', properties: {} } }]
  r.messages = [{ role: 'user', parts: [{ text: text() }, ...(index % 2 ? [{ image }] : []), { text: text() }] }, { role: 'assistant', text: text(), calls: [{ id: 'call', name: 'lookup', args }] }, { role: 'tool', text: text(), callID: 'call', name: 'lookup', error: !!(index % 2), images: index % 2 ? [image, { ...image, bytes: 'BA==' }] : [] }, { role: 'user', parts: [{ text: text() }] }]
  if (index % 4 === 1) r.messages[1].replay = family === 'openai-compatible' ? { family, reasoning: text() } : family === 'anthropic' ? { family, blocks: [{ type: 'thinking', thinking: text(), signature: 'signed' }, { type: 'text', text: text() }] } : { family, parts: [{ text: text(), thought: true, thoughtSignature: 'signed' }, { text: text() }] }
  input.response = response(family, text(), !!(index % 2), !!(index % 3))
  return input
}
export function cases() {
  const out = FAMILIES.map(family => ({ id: `control/empty/${family}`, input: { ...base(family), brief: '', projectInstructions: '', memory: [] } }))
  for (const family of FAMILIES) out.push({ id: `control/layers/${family}`, input: { ...base(family), brief: 'brief', projectInstructions: 'instructions' } })
  for (const seed of SEEDS) for (let index = 0; index < PER_SEED; index++) for (const family of FAMILIES) {
    if (process.env.DIFF_SEED && Number(process.env.DIFF_SEED) !== seed) continue
    if (process.env.DIFF_CASE && Number(process.env.DIFF_CASE) !== index) continue
    out.push({ id: `seed=${seed}/case=${index}/${family}`, input: generated(seed, index, family) })
  }
  return out
}
