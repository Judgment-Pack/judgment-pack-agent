/**
 * The capability record (agent#4): what one endpoint and model can do, and the
 * whole of what the loop reads where it decides.
 *
 * The loop used to decide by the three endpoint families: Anthropic thinks one
 * way, Gemini narrows its schemas, everything else is OpenAI-compatible and
 * takes native tool calls. An open-weight model served through the
 * OpenAI-compatible wire (Ollama, vLLM, LM Studio) is none of those three: it may
 * have a small window, no native tool calls, a reasoning parameter it ignores,
 * no vision, its reasoning in a differently named stream member. So the loop
 * reads this record instead, wherever it decides:
 *
 * - **thinking settings**: `reasoning.kind` picks the dialect the tier is spelled
 *   in, and `reasoning.ignored` (an endpoint that accepts the parameter and does
 *   nothing with it) sends none and says so once (`thinking.ts`);
 * - **schema narrowing**: `toolSchemas` says whether the endpoint takes the
 *   served JSON Schema or the subset the closed removal list of `geminiSchema.ts`
 *   produces;
 * - **whether to offer tools**: `nativeToolCalls` false describes the tools in
 *   the instructions and reads one fenced JSON call out of the answer instead
 *   (`engines/contract.ts`, `extractToolCall`); `parallelToolCalls` false asks
 *   the wire for one call per turn where the wire has a member for it;
 * - **how to read reasoning deltas**: `streamingQuirks.reasoningField` pins the
 *   member reasoning streams in, and `wholeJsonAnswer` asks an endpoint that
 *   answers whole for a whole answer (`engines/vercel/relay.ts`);
 * - **images**: `vision` passes an image part to the model, or substitutes the
 *   host's text rendering (`images.ts`);
 * - **the context budget**: `contextWindowTokens` and `outputTokens`, where the
 *   record carries them, replace the budget table's (`context/policy.ts`);
 * - **where the execution state goes** after the first request (agent#9):
 *   `statePlacement`, and `strictAlternation` for an endpoint that refuses a
 *   broken turn order (`statePlacement` in `engines/vercel/loop.ts`).
 *
 * The family stays what it is: the **wire** (which provider speaks the request,
 * where a signature rides, where a cache breakpoint goes). The record says what
 * the endpoint behind that wire does.
 *
 * Where a record comes from: the family's default (`FAMILY_CAPABILITIES`, the
 * three the Desk's families have today), then whatever the host answers for the
 * endpoint and model through `configureHarness`'s `endpointCapabilities` (its
 * probe result, member by member), then the session's own where the host put
 * one on it (`session.model.capabilities`, for an endpoint the host's function
 * cannot tell apart from another serving the same model).
 */
import { authoredFailure } from './failures'
import { endpointCapabilities } from './host'
import type { EndpointKind } from './host'

/** How the endpoint is asked to reason, if at all. */
export type ReasoningKind = 'anthropic-thinking' | 'gemini-thinking' | 'openai-effort' | 'none'
export const REASONING_KINDS: readonly ReasoningKind[] = ['anthropic-thinking', 'gemini-thinking', 'openai-effort', 'none']

/** The stream member an OpenAI-compatible endpoint puts its reasoning in. */
export type ReasoningField = 'reasoning' | 'reasoning_content'
export const REASONING_FIELDS: readonly ReasoningField[] = ['reasoning', 'reasoning_content']

/**
 * Where a model loop puts what changes between requests (the execution state)
 * after the first request, which always carries it as a second text part of
 * the person's own message (agent#9). `trailing-user`: one user message after
 * the history. `system-tail`: the end of the system text, after the cache
 * breakpoint on its head.
 */
export type StatePlacement = 'trailing-user' | 'system-tail'
export const STATE_PLACEMENTS: readonly StatePlacement[] = ['trailing-user', 'system-tail']

/** What the endpoint takes for a tool's parameters. */
export type ToolSchemas = 'json-schema' | 'gemini-subset'
export const TOOL_SCHEMAS: readonly ToolSchemas[] = ['json-schema', 'gemini-subset']

export interface ReasoningCapability {
  kind: ReasoningKind
  /** The endpoint accepts the parameter and does nothing with it: nothing is sent. */
  ignored?: boolean
}

export interface StreamingQuirks {
  /**
   * The member reasoning streams in, and is carried back in, on the
   * OpenAI-compatible wire; the other member is not read as reasoning. Null: not
   * pinned, read as the SDK reads it (`reasoning_content`, else `reasoning`).
   */
  reasoningField: ReasoningField | null
  /** The endpoint answers whole whatever it is asked: it is asked for a whole answer. */
  wholeJsonAnswer: boolean
}

/** One endpoint and model, as the loop reads it. The contract's `capability.json`. */
export interface ModelCapabilities {
  /** The model's window in tokens; with `outputTokens`, replaces the budget table's. */
  contextWindowTokens?: number
  /** The output reserve in tokens, at most half the window. */
  outputTokens?: number
  nativeToolCalls: boolean
  parallelToolCalls: boolean
  vision: boolean
  reasoning: ReasoningCapability
  streamingQuirks: StreamingQuirks
  toolSchemas: ToolSchemas
  /**
   * Where the state goes after the first request (agent#9). Absent: by family
   * and tier (`system-tail` for Anthropic with a thinking tier on,
   * `trailing-user` otherwise).
   */
  statePlacement?: StatePlacement
  /**
   * The endpoint refuses two user turns in a row and a user text after a tool
   * result, so it gets `system-tail` whatever `statePlacement` says (agent#9).
   */
  strictAlternation?: boolean
  /**
   * A model the same connection serves for delegated children (agent#13, agent#15): a child whose
   * profile does not ask for the parent's model runs on it, the request carrying its name on the
   * same model capability. Absent, children run on the session's model. Its own record is the
   * host's `endpointCapabilities` answer for it over the family's default.
   */
  childModel?: string
}

/**
 * The reasoning kinds each wire speaks. A record that names another is refused
 * for that wire: its members would be spelled for a provider that is not the
 * one sending the request.
 */
export const WIRE_REASONING: Readonly<Record<EndpointKind, readonly ReasoningKind[]>> = {
  'openai-compatible': ['openai-effort', 'none'],
  anthropic: ['anthropic-thinking', 'none'],
  gemini: ['gemini-thinking', 'none']
}

/**
 * The three families the Desk knows today, as records: what the loop did for
 * each before it read a record, so a host that supplies these changes nothing.
 * No window or output cap (the budget table's stand), native and parallel tool
 * calls (nothing extra is sent), the family's own reasoning dialect, no
 * streaming quirk, and the Gemini schema subset on Gemini alone. Vision: the
 * Anthropic and Gemini models the Desk configures take images; an
 * OpenAI-compatible endpoint is not known to, so an image reaches it as the
 * host's text rendering until its own record says otherwise.
 */
export const FAMILY_CAPABILITIES: Readonly<Record<EndpointKind, Readonly<ModelCapabilities>>> = Object.freeze({
  'openai-compatible': Object.freeze({
    nativeToolCalls: true,
    parallelToolCalls: true,
    vision: false,
    reasoning: Object.freeze({ kind: 'openai-effort' as const }),
    streamingQuirks: Object.freeze({ reasoningField: null, wholeJsonAnswer: false }),
    toolSchemas: 'json-schema' as const
  }),
  anthropic: Object.freeze({
    nativeToolCalls: true,
    parallelToolCalls: true,
    vision: true,
    reasoning: Object.freeze({ kind: 'anthropic-thinking' as const }),
    streamingQuirks: Object.freeze({ reasoningField: null, wholeJsonAnswer: false }),
    toolSchemas: 'json-schema' as const
  }),
  gemini: Object.freeze({
    nativeToolCalls: true,
    parallelToolCalls: true,
    vision: true,
    reasoning: Object.freeze({ kind: 'gemini-thinking' as const }),
    streamingQuirks: Object.freeze({ reasoningField: null, wholeJsonAnswer: false }),
    toolSchemas: 'gemini-subset' as const
  })
})

/** Every member a record may carry, and the whole of them. The contract's `capability.json` properties. */
export const CAPABILITY_MEMBERS: readonly (keyof ModelCapabilities)[] = [
  'contextWindowTokens', 'outputTokens', 'nativeToolCalls', 'parallelToolCalls', 'vision',
  'reasoning', 'streamingQuirks', 'toolSchemas', 'statePlacement', 'strictAlternation', 'childModel'
]

const object = (value: unknown): value is Record<string, unknown> =>
  value !== null && typeof value === 'object' && !Array.isArray(value)

function refuse(detail: string): never {
  throw authoredFailure('configuration', `the model's capability record is invalid: ${detail}; nothing was run`)
}

function closed(value: Record<string, unknown>, members: readonly string[], where: string): void {
  const extra = Object.keys(value).find((key) => !members.includes(key))
  if (extra !== undefined) refuse(`${where} names ${JSON.stringify(extra)}, which the record does not have`)
}

/**
 * A capability record, read closed: every member it names is checked, and a
 * member it does not name is refused, as `contextConfiguration` refuses one. The
 * window bounds are the budget table's (`budget.json#/$defs/modelBudget`).
 */
export function readCapabilities(value: unknown): ModelCapabilities {
  if (!object(value)) refuse('it is not an object')
  closed(value, CAPABILITY_MEMBERS, 'the record')
  for (const flag of ['nativeToolCalls', 'parallelToolCalls', 'vision'] as const) {
    if (typeof value[flag] !== 'boolean') refuse(`${flag} must be true or false`)
  }
  const window = value.contextWindowTokens
  const output = value.outputTokens
  if ((window === undefined) !== (output === undefined)) refuse('contextWindowTokens and outputTokens come together')
  if (window !== undefined) {
    if (!Number.isInteger(window) || Number(window) < 4096 || Number(window) > 2000000) refuse('contextWindowTokens must be an integer from 4096 to 2000000')
    if (!Number.isInteger(output) || Number(output) < 1024 || Number(output) > Number(window) / 2) refuse('outputTokens must be an integer from 1024 to half the window')
  }
  const reasoning = value.reasoning
  if (!object(reasoning)) refuse('reasoning must be an object')
  closed(reasoning, ['kind', 'ignored'], 'reasoning')
  if (!REASONING_KINDS.includes(reasoning.kind as ReasoningKind)) refuse(`reasoning.kind must be one of ${REASONING_KINDS.join(', ')}`)
  if (reasoning.ignored !== undefined && typeof reasoning.ignored !== 'boolean') refuse('reasoning.ignored must be true or false')
  const quirks = value.streamingQuirks
  if (!object(quirks)) refuse('streamingQuirks must be an object')
  closed(quirks, ['reasoningField', 'wholeJsonAnswer'], 'streamingQuirks')
  if (quirks.reasoningField !== null && !REASONING_FIELDS.includes(quirks.reasoningField as ReasoningField)) refuse('streamingQuirks.reasoningField must be reasoning, reasoning_content or null')
  if (typeof quirks.wholeJsonAnswer !== 'boolean') refuse('streamingQuirks.wholeJsonAnswer must be true or false')
  if (!TOOL_SCHEMAS.includes(value.toolSchemas as ToolSchemas)) refuse(`toolSchemas must be one of ${TOOL_SCHEMAS.join(', ')}`)
  if (value.statePlacement !== undefined && !STATE_PLACEMENTS.includes(value.statePlacement as StatePlacement)) refuse(`statePlacement must be one of ${STATE_PLACEMENTS.join(', ')}`)
  if (value.strictAlternation !== undefined && typeof value.strictAlternation !== 'boolean') refuse('strictAlternation must be true or false')
  if (value.childModel !== undefined && (typeof value.childModel !== 'string' || !value.childModel.trim() || value.childModel.length > 200 || /[\u0000-\u001f]/.test(value.childModel))) refuse('childModel must be a model name of at most 200 characters')
  return value as unknown as ModelCapabilities
}

/**
 * The record one model session runs with: the family's default, then the
 * host's answer for the endpoint and model, then the session's own, member by
 * member. Read closed, and refused where its reasoning kind is not one the
 * session's wire speaks.
 */
export function capabilitiesFor(model: { family: EndpointKind; model: string; capabilities?: Partial<ModelCapabilities> }): ModelCapabilities {
  const answered = endpointCapabilities({ family: model.family, model: model.model })
  if (!object(answered)) refuse('the host answered for the endpoint with something other than an object')
  const record = readCapabilities({ ...FAMILY_CAPABILITIES[model.family], ...answered, ...(model.capabilities ?? {}) })
  if (!WIRE_REASONING[model.family].includes(record.reasoning.kind)) {
    refuse(`reasoning.kind ${record.reasoning.kind} is not spoken on the ${model.family} wire (${WIRE_REASONING[model.family].join(' or ')})`)
  }
  return record
}
