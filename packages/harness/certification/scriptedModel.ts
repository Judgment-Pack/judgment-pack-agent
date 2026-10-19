/**
 * The bake-off's scripted model, in TypeScript, as a `fetch` stub.
 *
 * A port of `fixture/scripted_model.py` from
 * `docs/experiments/2026-09-05-assistant-engine-bakeoff`: the same step logic
 * (`count_openai`, `count_anthropic`, `choose_step`), the same two wire
 * formats, the same SSE grammars, the same repeat detector — and, from
 * `fixture/THINKING-SPEC.md`, the thinking gates per wire format, the
 * deterministic reasoning text and signatures, the `-nothink` degrade, the
 * split-signature probe and the critic script. What is dropped is the run-id
 * bookkeeping, which existed so several candidates could share one HTTP server.
 *
 * **Server mode is not the same as client request.** With thinking on, a
 * request that carries no thinking parameter is answered exactly as it would be
 * with thinking off, and the row records `thinkingRequested: false`. That
 * asymmetry *is* the measurement: an engine that cannot configure the parameter
 * produces a run where every row says `false`.
 *
 * **It decides the next step from the conversation, not from a counter it
 * keeps.** n is the number of scenario tool results already present in the
 * request's own messages; the answer is step n+1. So a session that dropped a
 * result, replayed a turn, or rebuilt its message array wrongly gets a
 * different script — which is what makes this a test of the engine's message
 * handling and not only of its parsing.
 *
 * It also **records every request**: the header names, the tool names offered,
 * the message count and the URL, which is what `requests.jsonl` carried in the
 * experiment and what K1 and K2 are read off here.
 */
import scenario from './scenario.json'
import { GEMINI_SCHEMA_REMOVALS } from '../geminiSchema'
import type { EndpointKind } from '../host'

export interface ScenarioStep {
  id: string
  kind: string
  tool?: string
  arguments?: Record<string, unknown>
  text?: string
}

const STEPS = scenario.steps as ScenarioStep[]
const COUNTED = new Set(scenario.countedTools)

/** The literal the desk's critic carries. `assistant/refutation.ts` owns it. */
const MARKER = 'REFUTATION PASS'

/**
 * The thinking modes this endpoint can be stood up in.
 *
 * - `off` — phase A, byte-identical: no gate, no reasoning, nothing recorded
 *   but `thinkingRequested: false`.
 * - `on` — the gate, and deterministic reasoning behind it.
 * - `split` — `on`, with the Anthropic signature sent as two `signature_delta`
 *   events. Real Anthropic sends one; a re-chunking proxy is the case.
 * - `enabled-only` — refuses `thinking: {"type":"adaptive"}` with a 400 and
 *   accepts the token-budget spelling, which is the dialect fallback's leg.
 * - `nothink` — 400 `Unsupported parameter` to any request carrying a thinking
 *   parameter at all, which is the degrade's leg.
 * - `always` — a model that reasons whatever it was asked for, which is the
 *   `always` state's leg by **absence**: the desk asks for none, gets thought
 *   parts anyway, and the two-consecutive-turns rule settles it.
 * - `no-off` — a Gemini model that cannot be turned off: 400 to a request
 *   asking for a zero budget or the minimal level, and thought parts otherwise.
 *   That is the `always` state's leg by **refusal**, which is immediate — and
 *   the refusal is answered at *both* spellings, so a run reaches it only after
 *   the dialect fallback has been tried.
 * - `level-only` — a Gemini model that takes `thinkingLevel` and answers 400
 *   to `thinkingBudget`, which is that family's dialect fallback leg.
 * - `refuses-a-keyword` — an endpoint that refuses a **schema** keyword the
 *   desk's removal list does not name. The leg for the ruling that such a
 *   keyword is reported by name and never stripped.
 */
export type ThinkingMode =
  | 'off'
  | 'on'
  | 'split'
  | 'enabled-only'
  | 'nothink'
  | 'always'
  | 'no-off'
  | 'level-only'
  | 'refuses-a-keyword'

/** The reasoning one step carries, a pure function of its id. */
export function thinkText(step: { id: string; tool?: string }): string {
  return (
    `[scripted reasoning ${step.id}] The policy has three branches. ` +
    `Next I will call ${step.tool ?? 'no tool'} to make progress; ` +
    'I must not state a verdict of my own.'
  )
}

/**
 * The signature one step carries, a pure function of its id.
 *
 * Recomputed by the checker rather than compared against a value this endpoint
 * merely logged, so what a leg asserts is the signature that *should* have come
 * back and not the one this fixture happened to remember.
 */
export function thinkSignature(step: { id: string }): string {
  return btoa(`scripted-thinking-signature:${step.id}`)
}

/**
 * The sentence a model that thinks aloud says **before** it calls a tool.
 *
 * **A turn is only evidence about an endpoint if it produced an answer of its
 * own**, which is the desk's own rule and a deliberate one: a turn that said
 * nothing but called a tool is no evidence that an endpoint will not reason.
 * The scenario's first seven steps are pure tool calls, so an endpoint that
 * reasons on every one of them would never reach the two-consecutive-turns rule
 * at all — which is why the "always thinks" modes put a short answer beside the
 * call, the way a model that narrates its own work actually does.
 *
 * A pure function of the step id, like the reasoning text and the signature, so
 * a leg recomputes it rather than trusting what this fixture logged.
 */
export function chatterText(step: { id: string }): string {
  return `[scripted answer ${step.id}] Working through it now.`
}

/** The critic's three steps: a check, a rehearsal, and a sentence about them. */
function criticSteps(refuted: boolean): ScenarioStep[] {
  const validate = STEPS.find((step) => step.id === (refuted ? 'T4' : 'T5'))!
  const evaluate = STEPS.find((step) => step.id === 'T6')!
  return [
    // The document the desk proposed, or — on the refuted leg — the draft the
    // runtime already refused once. Both are recorded calls, so the runtime's
    // own answer comes back either way.
    { id: 'R1', kind: 'tool_call', tool: 'validate', arguments: validate.arguments },
    // **Written with no rehearsal member**, exactly as T6 is: the critic runs
    // inside the same ToolGate, so this arrives at the runtime rewritten or it
    // does not arrive at all.
    { id: 'R2', kind: 'tool_call', tool: 'experimental_evaluate', arguments: evaluate.arguments },
    {
      id: 'R3',
      kind: 'final_message',
      // **The prose disagrees with the runtime, on purpose, on both legs.** A
      // verdict read out of this sentence is the wrong verdict either way.
      text: refuted
        ? 'REFUTATION: none found. Everything checks out and the pack is ready.'
        : 'REFUTATION: I am fairly sure this pack is refuted and should not be used.'
    }
  ]
}

/** One request the page made, as the checker reads it. */
export interface RecordedRequest {
  url: string
  /** Lower-cased header names only. A value is never recorded, not even a length. */
  headerNames: string[]
  api: EndpointKind
  streamRequested: boolean
  toolNames: string[]
  messageCount: number
  /** The step this answer carried. */
  step: string
  /** How many scenario tool results the request's own messages carried. */
  results: number
  /**
   * The body's own top-level members, sorted.
   *
   * Recorded so a leg can say **what the engine actually sent** rather than
   * only that the session worked: the two engines differ here — one puts
   * `stream_options` on an OpenAI-compatible request and the other a
   * `tool_choice` — and the conformance session holds them to what the two wire
   * formats define rather than to each other's spelling.
   */
  bodyMembers: string[]
  /** THINKING-SPEC §5. The request carried a parameter this endpoint reads as "on". */
  thinkingRequested: boolean
  /** The first match, with its path, verbatim. */
  thinkingParam: { path: string; value: unknown } | null
  /** True where this request is the desk's critic rather than its main loop. */
  refutation: boolean
  /** Anthropic only: every distinct signature carried back, in order. */
  signaturesCarried: string[]
  /** What this endpoint has emitted so far, recomputed from the step ids. */
  signaturesExpected: string[]
  /** Expected minus carried: the T-b gating row. */
  signaturesMissing: string[]
  /** Carried values that are a strict fragment of an expected one (#19663). */
  signaturesTruncated: string[]
  /** Signatures on a block with no thinking text at all (langchainjs#10744). */
  signaturesMalformed: string[]
  /** OpenAI only: the reasoning member names carried back, per assistant message. */
  reasoningIn: string[]
  /**
   * Gemini only: every schema keyword the declarations carried, at every depth.
   *
   * Recorded rather than only refused, so a leg can say **what the desk showed
   * the model** — the removal list is a ruling about the contract a model is
   * shown, and a row that only said "the request was accepted" would not
   * establish it.
   */
  schemaKeywords: string[]
  /**
   * Gemini only: each declaration's `parameters`, whole, in the order they were
   * declared.
   *
   * **The keyword list is not enough and that was the defect.** A leg that read
   * only which words appeared could say "none of the removal list is here" while
   * the engine underneath had quietly dropped `pattern`, `maximum` and every
   * conditional — the two engines showing the model two different contracts. So
   * the schema itself is recorded and a leg asserts **deep equality** with what
   * the desk says that engine shows.
   */
  schemas: unknown[]
  /**
   * Gemini only: every `role: "model"` turn this request carried back, whole.
   *
   * **What a leg needs to say "exactly as it arrived".** The signature lists
   * answer three questions about signatures; this answers the one that
   * subsumes them — whether the parts the endpoint sent are the parts that came
   * back, in the same order, with the same text and the same members. A client
   * that joined two parts, dropped an empty one or reordered a pair fails on
   * this whatever its signatures look like.
   */
  modelParts: unknown[][]
}

/** One thinking parameter this endpoint recognises, with where it was found. */
type ThinkingParam = { path: string; value: unknown } | null

const OFF_EFFORTS = new Set(['none', 'off', 'disabled'])

/** THINKING-SPEC §3.1, deliberately tolerant: five spellings, one slot tier. */
function openAiThinkingParam(body: Record<string, unknown>): ThinkingParam {
  const look = (holder: Record<string, unknown>, prefix: string): ThinkingParam => {
    const effort = holder.reasoning_effort
    if (typeof effort === 'string' && !OFF_EFFORTS.has(effort)) {
      return { path: `${prefix}reasoning_effort`, value: effort }
    }
    const reasoning = holder.reasoning
    if (reasoning !== undefined && reasoning !== null && reasoning !== false && reasoning !== 'none') {
      return { path: `${prefix}reasoning`, value: reasoning }
    }
    const thinking = holder.thinking as { type?: unknown } | undefined
    if (thinking !== undefined && thinking !== null && typeof thinking === 'object') {
      if (thinking.type === 'enabled' || thinking.type === 'adaptive') {
        return { path: `${prefix}thinking`, value: thinking }
      }
    }
    const template = holder.chat_template_kwargs as { enable_thinking?: unknown } | undefined
    if (template !== undefined && template !== null && Boolean(template.enable_thinking)) {
      return { path: `${prefix}chat_template_kwargs.enable_thinking`, value: template.enable_thinking }
    }
    return null
  }
  const top = look(body, '')
  if (top !== null) return top
  const extra = body.extra_body
  if (extra !== null && typeof extra === 'object') {
    return look(extra as Record<string, unknown>, 'extra_body.')
  }
  return null
}

/** THINKING-SPEC §2.1: a dict whose `type` asks for thinking, and nothing else. */
function anthropicThinkingParam(body: Record<string, unknown>): ThinkingParam {
  const thinking = body.thinking as { type?: unknown } | undefined
  if (thinking === undefined || thinking === null || typeof thinking !== 'object') return null
  if (thinking.type !== 'enabled' && thinking.type !== 'adaptive') return null
  return { path: 'thinking', value: thinking }
}

/** The `thinkingConfig` on a Gemini request, whatever it asks for, or null. */
function geminiThinkingConfig(body: Record<string, unknown>): Record<string, unknown> | null {
  const config = body.generationConfig as { thinkingConfig?: unknown } | undefined
  const thinking = config?.thinkingConfig
  if (thinking === undefined || thinking === null || typeof thinking !== 'object') return null
  return thinking as Record<string, unknown>
}

/**
 * A Gemini request that asks the model **to** think, and nothing else.
 *
 * **A zero budget and the minimal level are not thinking requests**, and this
 * asymmetry is the whole reason the desk sends them: on this wire omission
 * means thinking, so "off" is a member, and an endpoint reads that member as
 * the request not to reason. A reader that counted any `thinkingConfig` as a
 * tier parameter would report every off-tier run as having asked for thinking.
 */
function geminiThinkingParam(body: Record<string, unknown>): ThinkingParam {
  const thinking = geminiThinkingConfig(body)
  if (thinking === null) return null
  const budget = thinking.thinkingBudget
  const level = thinking.thinkingLevel
  const asks =
    thinking.includeThoughts === true ||
    (typeof budget === 'number' && budget !== 0) ||
    (typeof level === 'string' && level !== 'minimal')
  return asks ? { path: 'generationConfig.thinkingConfig', value: thinking } : null
}

/** Whether a Gemini request asked the endpoint **not** to think. */
function geminiAsksForNone(body: Record<string, unknown>): boolean {
  const thinking = geminiThinkingConfig(body)
  if (thinking === null) return false
  return thinking.thinkingBudget === 0 || thinking.thinkingLevel === 'minimal'
}

interface OpenAiMessage {
  role?: string
  tool_call_id?: string
  name?: string
  tool_calls?: { id?: string; function?: { name?: string } }[]
}

/**
 * Count results for counted tools in an OpenAI message list.
 *
 * A result is a `role: "tool"` message, and its tool is resolved through the
 * `tool_call_id` an earlier assistant message announced. A result whose call
 * cannot be resolved is counted anyway: nothing else in this scenario makes
 * tool results.
 */
export function countOpenAi(messages: unknown[]): number {
  const namesById = new Map<string, string>()
  let n = 0
  for (const raw of messages ?? []) {
    const message = raw as OpenAiMessage
    if (message?.role === 'assistant') {
      for (const call of message.tool_calls ?? []) {
        const id = call.id
        const name = call.function?.name
        if (id && name) namesById.set(id, name)
      }
    } else if (message?.role === 'tool' || message?.role === 'function') {
      const name = namesById.get(String(message.tool_call_id)) ?? message.name
      if (name === undefined || COUNTED.has(name)) n += 1
    }
  }
  return n
}

interface AnthropicBlock {
  type?: string
  id?: string
  name?: string
  tool_use_id?: string
}

/** Count `tool_result` blocks for counted tools in an Anthropic message list. */
export function countAnthropic(messages: unknown[]): number {
  const namesById = new Map<string, string>()
  let n = 0
  for (const raw of messages ?? []) {
    const content = (raw as { content?: unknown }).content
    if (!Array.isArray(content)) continue
    for (const item of content) {
      const block = item as AnthropicBlock
      if (block?.type === 'tool_use') {
        if (block.id && block.name) namesById.set(block.id, block.name)
      } else if (block?.type === 'tool_result') {
        const name = namesById.get(String(block.tool_use_id))
        if (name === undefined || COUNTED.has(name)) n += 1
      }
    }
  }
  return n
}

/** One Gemini `Part`, as far as this endpoint reads one. */
interface GeminiPart {
  text?: string
  thought?: boolean
  thoughtSignature?: string
  functionCall?: { name?: string }
  functionResponse?: { name?: string }
}

/**
 * Count `functionResponse` parts for counted tools in a Gemini `contents` list.
 *
 * The wire names the tool on the response itself, so there is no id to resolve
 * — which is why this reader is the shortest of the three. A response naming a
 * tool the scenario does not count is not a step.
 */
export function countGemini(contents: unknown[]): number {
  let n = 0
  for (const raw of contents ?? []) {
    const parts = (raw as { parts?: unknown }).parts
    if (!Array.isArray(parts)) continue
    for (const item of parts) {
      const part = item as GeminiPart
      const name = part?.functionResponse?.name
      if (part?.functionResponse === undefined) continue
      if (name === undefined || COUNTED.has(name)) n += 1
    }
  }
  return n
}

/**
 * The step at result-count n, with the experiment's repeat detector.
 *
 * A harness that dropped a refused call without returning any result would
 * re-trigger the same step for ever; after three identical repeats the script
 * advances, so a run that stalls ends rather than hanging the suite.
 */
export function chooseStep(state: { lastBase?: number; repeats: number }, n: number): ScenarioStep {
  const base = Math.min(n + 1, STEPS.length)
  if (state.lastBase === base) state.repeats += 1
  else {
    state.lastBase = base
    state.repeats = 1
  }
  const offset = state.repeats > 3 ? state.repeats - 3 : 0
  return STEPS[Math.min(base + offset, STEPS.length) - 1]!
}

/**
 * What a request carried back about thinking, from its own messages.
 *
 * The three lists are THINKING-SPEC §5's, and each names a real defect shape:
 * a signature the client dropped, one it truncated (`vercel/ai#19663`), and one
 * it kept beside a block whose text it lost (`langchainjs#10744`).
 */
function anthropicCarried(
  messages: unknown[],
  emitted: readonly string[]
): { carried: string[]; truncated: string[]; malformed: string[] } {
  const carried: string[] = []
  const truncated: string[] = []
  const malformed: string[] = []
  for (const message of messages ?? []) {
    const content = (message as { content?: unknown }).content
    if (!Array.isArray(content)) continue
    for (const item of content) {
      const block = item as { type?: string; thinking?: unknown; signature?: unknown }
      if (typeof block?.signature !== 'string' || block.signature === '') continue
      const signature = block.signature
      if (!carried.includes(signature)) carried.push(signature)
      if (typeof block.thinking !== 'string' || block.thinking === '') malformed.push(signature)
      const whole = emitted.find(
        (expected) =>
          expected !== signature &&
          expected.length > signature.length &&
          (expected.startsWith(signature) || expected.endsWith(signature))
      )
      if (whole !== undefined) truncated.push(signature)
    }
  }
  return { carried, truncated, malformed }
}

/**
 * What a Gemini request carried back about thinking, from its own `contents`.
 *
 * The same three lists the Anthropic reader computes and for the same reasons —
 * a signature the client dropped, one it truncated, and one it kept beside a
 * part that can no longer carry it. The wire's own rule is that signed parts are
 * resent exactly as they were received, so all three are refusals rather than
 * observations, and the endpoint below enforces that.
 *
 * **A signature rides on one of two kinds of part, and the first version of this
 * knew only one of them.** Gemini signs a thought summary, and — in function
 * calling, which is the whole of what this desk does — it signs the **first
 * `functionCall` part** of a turn, leaving later parallel calls unsigned. A
 * validator that called every signature outside a thought part malformed had it
 * exactly backwards, and would have refused the shape the wire actually sends.
 */
function geminiCarried(
  contents: unknown[],
  emitted: readonly string[]
): { carried: string[]; truncated: string[]; malformed: string[] } {
  const carried: string[] = []
  const truncated: string[] = []
  const malformed: string[] = []
  for (const content of contents ?? []) {
    const parts = (content as { parts?: unknown }).parts
    if (!Array.isArray(parts)) continue
    for (const item of parts) {
      const part = item as GeminiPart
      if (typeof part?.thoughtSignature !== 'string' || part.thoughtSignature === '') continue
      const signature = part.thoughtSignature
      if (!carried.includes(signature)) carried.push(signature)
      // The two parts a signature may ride on: a **thought part**, whose text
      // may legitimately be empty — the wire emits a signed thought with no
      // summary when the summary was not streamed or there is none — and a
      // **function call**. Anywhere else is a block this endpoint refuses the
      // continuation over.
      //
      // **The `text !== ''` this used to require was wrong**, and it mattered:
      // an endpoint that sends an empty signed thought would have had its own
      // shape refused when the client replayed it faithfully. What catches a
      // client that *lost* the text of a signed part is not this list — it is
      // the legs that compare the whole of `contents[].parts[]` against what
      // was sent, which is a stronger reading of the same rule.
      const onThought = part.thought === true
      const onCall = part.functionCall !== undefined
      if (!onThought && !onCall) malformed.push(signature)
      const whole = emitted.find(
        (expected) =>
          expected !== signature &&
          expected.length > signature.length &&
          (expected.startsWith(signature) || expected.endsWith(signature))
      )
      if (whole !== undefined) truncated.push(signature)
    }
  }
  return { carried, truncated, malformed }
}

/** Every model turn in one `contents` list, whole and in order. */
function geminiModelParts(contents: unknown[]): unknown[][] {
  return (contents ?? [])
    .filter((content) => (content as { role?: string }).role === 'model')
    .map((content) => ((content as { parts?: unknown[] }).parts ?? []))
}

/** The schema actually sent, using either Gemini declaration format. */
function geminiSchemas(tools: unknown[]): unknown[] {
  return (tools ?? []).flatMap((tool) =>
    ((tool as { functionDeclarations?: Record<string, unknown>[] }).functionDeclarations ?? []).map(
      (declared) => {
        if ('parametersJsonSchema' in declared) {
          if ('parameters' in declared) throw new Error('Gemini schema fields are mutually exclusive')
          return declared.parametersJsonSchema
        }
        return declared.parameters
      }
    )
  )
}

/**
 * Every schema keyword the declarations on one Gemini request carried.
 *
 * **Property names are excluded**, because they are the author's words: a
 * document member called `const` is not the keyword `const`, and an endpoint
 * that refused the request over one would be refusing a name it invented.
 */
function geminiSchemaKeywords(tools: unknown[]): string[] {
  const found = new Set<string>()
  const walk = (value: unknown, inNameMap: boolean): void => {
    if (Array.isArray(value)) {
      for (const item of value) walk(item, false)
      return
    }
    if (value === null || typeof value !== 'object') return
    for (const [key, inner] of Object.entries(value as Record<string, unknown>)) {
      if (!inNameMap) found.add(key)
      walk(inner, key === 'properties' || key === '$defs')
    }
  }
  for (const schema of geminiSchemas(tools)) walk(schema, false)
  return [...found].sort()
}

/** Which reasoning member names an OpenAI-compatible request carried back. */
function openAiCarried(messages: unknown[]): string[] {
  const names: string[] = []
  for (const message of messages ?? []) {
    const held = message as Record<string, unknown>
    if (held?.role !== 'assistant') continue
    for (const name of ['reasoning_content', 'reasoning']) {
      const said = held[name]
      if (typeof said === 'string' && said !== '' && !names.includes(name)) names.push(name)
    }
  }
  return names
}

function openAiAnswer(step: ScenarioStep, reasoning: boolean) {
  const message =
    step.kind === 'tool_call'
      ? {
          role: 'assistant',
          content: null,
          tool_calls: [
            {
              id: `call_${step.id}`,
              type: 'function',
              function: { name: step.tool, arguments: JSON.stringify(step.arguments ?? {}) }
            }
          ]
        }
      : { role: 'assistant', content: step.text }
  // **Both vendor names, identical text.** DeepSeek and vLLM < 0.18 send
  // `reasoning_content`; vLLM >= 0.18 and OpenRouter send `reasoning`. A
  // fixture that emitted one would score an engine on the fixture's choice.
  const thinking = reasoning
    ? { reasoning_content: thinkText(step), reasoning: thinkText(step) }
    : {}
  return {
    id: `chatcmpl-${step.id}`,
    object: 'chat.completion',
    model: 'scripted-model',
    choices: [
      {
        index: 0,
        message: { ...message, ...thinking },
        finish_reason: step.kind === 'tool_call' ? 'tool_calls' : 'stop',
        logprobs: null
      }
    ],
    usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 }
  }
}

function anthropicAnswer(step: ScenarioStep, reasoning: boolean) {
  const content: unknown[] =
    step.kind === 'tool_call'
      ? [{ type: 'tool_use', id: `toolu_${step.id}`, name: step.tool, input: step.arguments ?? {} }]
      : [{ type: 'text', text: step.text }]
  // The thinking block is **first**, before the tool use and before the text,
  // which is where the protocol puts it.
  if (reasoning) {
    content.unshift({
      type: 'thinking',
      thinking: thinkText(step),
      signature: thinkSignature(step)
    })
  }
  return {
    id: `msg_${step.id}`,
    type: 'message',
    role: 'assistant',
    model: 'scripted-model',
    content,
    stop_reason: step.kind === 'tool_call' ? 'tool_use' : 'end_turn',
    stop_sequence: null,
    usage: { input_tokens: 1, output_tokens: 1 }
  }
}

/**
 * One whole Gemini answer.
 *
 * The thought part comes **first**, before the call and before the text, which
 * is where the wire puts it — and it carries its signature on itself, because
 * on this wire a signature is a member of the part it belongs to rather than a
 * separate event.
 */
/**
 * The parts one answer carries, in the endpoint's own order, under one
 * signature topology.
 *
 * Shared by the whole and the streamed builders so the two cannot disagree
 * about where a signature sits — which is the property under test.
 */
type SignatureTopology = 'thought' | 'call' | 'parallel' | 'empty'

function geminiParts(
  step: ScenarioStep,
  reasoning: boolean,
  chatter: boolean,
  topology: SignatureTopology
): { summary: Record<string, unknown>[]; answer: Record<string, unknown>[] } {
  const signature = thinkSignature(step)
  const calls = step.kind === 'tool_call'
  if (topology === 'empty') {
    // Two of them, each signed, neither with a byte to read.
    const summary = reasoning
      ? [
          { text: '', thought: true, thoughtSignature: signature },
          { text: '', thought: true, thoughtSignature: `${signature}.2` }
        ]
      : []
    const answer: Record<string, unknown>[] = []
    if (chatter && calls) answer.push({ text: chatterText(step) })
    answer.push(
      calls
        ? { functionCall: { name: step.tool, args: step.arguments ?? {} } }
        : { text: step.text ?? '' }
    )
    return { summary, answer }
  }
  // A final message has no call to sign, so the summary carries the signature
  // whatever the topology is: there is nowhere else for it to go.
  const onSummary = reasoning && (topology === 'thought' || !calls)
  const summary = reasoning
    ? [
        {
          text: thinkText(step),
          thought: true,
          ...(onSummary ? { thoughtSignature: signature } : {})
        }
      ]
    : []
  const answer: Record<string, unknown>[] = []
  if (chatter && calls) answer.push({ text: chatterText(step) })
  if (!calls) {
    answer.push({ text: step.text ?? '' })
    return { summary, answer }
  }
  const call = { functionCall: { name: step.tool, args: step.arguments ?? {} } }
  answer.push(onSummary ? call : { ...call, thoughtSignature: signature })
  // **The second of a parallel pair is unsigned**, which is the documented
  // shape: one signature per turn, on the first call.
  if (topology === 'parallel') answer.push({ ...call })
  return { summary, answer }
}

function geminiAnswer(
  step: ScenarioStep,
  reasoning: boolean,
  chatter: boolean,
  topology: SignatureTopology
) {
  const built = geminiParts(step, reasoning, chatter, topology)
  const parts: unknown[] = [...built.summary, ...built.answer]
  return {
    candidates: [
      {
        content: { role: 'model', parts },
        finishReason: 'STOP',
        index: 0
      }
    ],
    usageMetadata: {
      promptTokenCount: 1,
      candidatesTokenCount: 1,
      ...(reasoning ? { thoughtsTokenCount: 1 } : {}),
      totalTokenCount: 2
    },
    modelVersion: 'scripted-model',
    responseId: `resp_${step.id}`
  }
}

/**
 * One streamed Gemini answer: several `GenerateContentResponse` events.
 *
 * **The thought summary arrives in pieces and its signature arrives on the
 * last of them**, which is the shape a client has to get right: a client that
 * kept the pieces as separate parts would send back a signature on a part with
 * no text, and the wire's rule is that thought parts come back exactly as they
 * were received. Every streaming leg on this family runs through that, rather
 * than it being a mode somebody has to remember to select.
 *
 * There is no terminal sentinel, because this wire has none: the stream ends
 * when the body does.
 */
function geminiStream(
  step: ScenarioStep,
  reasoning: boolean,
  chatter: boolean,
  topology: SignatureTopology
): string {
  const lines: string[] = []
  const frame = (parts: unknown[], last = false) =>
    lines.push(
      `data: ${JSON.stringify({
        candidates: [
          { content: { role: 'model', parts }, index: 0, ...(last ? { finishReason: 'STOP' } : {}) }
        ],
        modelVersion: 'scripted-model',
        responseId: `resp_${step.id}`,
        ...(last
          ? {
              usageMetadata: {
                promptTokenCount: 1,
                candidatesTokenCount: 1,
                ...(reasoning ? { thoughtsTokenCount: 1 } : {}),
                totalTokenCount: 2
              }
            }
          : {})
      })}\n\n`
    )
  const built = geminiParts(step, reasoning, chatter, topology)
  for (const summary of built.summary) {
    const whole = String(summary.text ?? '')
    if (whole === '') {
      // Nothing to split: an empty signed thought is one part on the wire and
      // one part in the frame.
      frame([summary])
      continue
    }
    const pieces = chunks(whole, 48)
    pieces.forEach((piece, at) => {
      const last = at === pieces.length - 1
      frame([
        {
          text: piece,
          thought: true,
          // On the final piece only, and only where this topology signs the
          // summary at all: the signature belongs to the bytes it was computed
          // over, and a client that joined the pieces before it would carry it
          // back over text this endpoint never signed.
          ...(last && summary.thoughtSignature !== undefined
            ? { thoughtSignature: summary.thoughtSignature }
            : {})
        }
      ])
    })
  }
  if (step.kind === 'tool_call') {
    built.answer.forEach((part, at) => frame([part], at === built.answer.length - 1))
  } else {
    const pieces = chunks(String(built.answer[built.answer.length - 1]!.text ?? ''), 96)
    pieces.forEach((piece, at) => frame([{ text: piece }], at === pieces.length - 1))
  }
  return lines.join('')
}

function chunks(text: string, size: number): string[] {
  if (!text) return ['']
  const out: string[] = []
  for (let index = 0; index < text.length; index += size) out.push(text.slice(index, index + size))
  return out
}

function openAiStream(step: ScenarioStep, reasoning: boolean): string {
  const id = `chatcmpl-${step.id}`
  const lines: string[] = []
  const frame = (choices: unknown[]) =>
    lines.push(
      `data: ${JSON.stringify({ id, object: 'chat.completion.chunk', model: 'scripted-model', choices })}\n\n`
    )
  frame([{ index: 0, delta: { role: 'assistant', content: null }, finish_reason: null }])
  // After the role frame and before the tool-call frames, under both names.
  if (reasoning) {
    for (const piece of chunks(thinkText(step), 48)) {
      frame([
        { index: 0, delta: { reasoning_content: piece, reasoning: piece }, finish_reason: null }
      ])
    }
  }
  if (step.kind === 'tool_call') {
    const args = JSON.stringify(step.arguments ?? {})
    frame([
      {
        index: 0,
        delta: {
          tool_calls: [
            {
              index: 0,
              id: `call_${step.id}`,
              type: 'function',
              function: { name: step.tool, arguments: '' }
            }
          ]
        },
        finish_reason: null
      }
    ])
    // Split across frames, exactly as an endpoint does: an engine that read
    // only the first frame's arguments would send an empty document.
    for (const piece of chunks(args, 256)) {
      frame([
        { index: 0, delta: { tool_calls: [{ index: 0, function: { arguments: piece } }] }, finish_reason: null }
      ])
    }
    frame([{ index: 0, delta: {}, finish_reason: 'tool_calls' }])
  } else {
    for (const piece of chunks(step.text ?? '', 96)) {
      frame([{ index: 0, delta: { content: piece }, finish_reason: null }])
    }
    frame([{ index: 0, delta: {}, finish_reason: 'stop' }])
  }
  lines.push('data: [DONE]\n\n')
  return lines.join('')
}

/**
 * **`message_delta` carries `usage`, because the protocol says it does.**
 *
 * The fixture used to send `{"type":"message_delta","delta":{"stop_reason":…}}`
 * and nothing else, which no Anthropic endpoint sends: the documented event
 * carries the message's running output-token count, and `stop_sequence` beside
 * the stop reason. The built-in engine never noticed, because it reads only
 * `content_block_*` and `message_stop` — so the gap was invisible until an
 * engine that validates the whole event grammar ran the same leg and refused
 * it. That is the fixture being wrong about the wire, not an engine being
 * fussy, and it is fixed here rather than branched around.
 */
function anthropicStream(step: ScenarioStep, reasoning: boolean, split: boolean): string {
  const lines: string[] = []
  const event = (name: string, object: unknown) =>
    lines.push(`event: ${name}\ndata: ${JSON.stringify(object)}\n\n`)
  event('message_start', {
    type: 'message_start',
    message: {
      id: `msg_${step.id}`,
      type: 'message',
      role: 'assistant',
      model: 'scripted-model',
      content: [],
      stop_reason: null,
      usage: { input_tokens: 1, output_tokens: 0 }
    }
  })
  event('ping', { type: 'ping' })
  // The thinking block is inserted at index 0 and everything else shifts, which
  // is the shape THINKING-SPEC §2.4 writes.
  let at = 0
  if (reasoning) {
    event('content_block_start', {
      type: 'content_block_start',
      index: at,
      content_block: { type: 'thinking', thinking: '' }
    })
    for (const piece of chunks(thinkText(step), 48)) {
      event('content_block_delta', {
        type: 'content_block_delta',
        index: at,
        delta: { type: 'thinking_delta', thinking: piece }
      })
    }
    const signature = thinkSignature(step)
    // **One event is the default**, because that is what Anthropic sends and
    // `vercel/ai#19663` was closed on exactly that argument. `split` is the
    // re-chunking proxy, and it is a separate leg rather than a gating row.
    const fragments = split
      ? [signature.slice(0, Math.floor(signature.length / 2)), signature.slice(Math.floor(signature.length / 2))]
      : [signature]
    for (const fragment of fragments) {
      event('content_block_delta', {
        type: 'content_block_delta',
        index: at,
        delta: { type: 'signature_delta', signature: fragment }
      })
    }
    event('content_block_stop', { type: 'content_block_stop', index: at })
    at += 1
  }
  if (step.kind === 'tool_call') {
    event('content_block_start', {
      type: 'content_block_start',
      index: at,
      content_block: { type: 'tool_use', id: `toolu_${step.id}`, name: step.tool, input: {} }
    })
    for (const piece of chunks(JSON.stringify(step.arguments ?? {}), 256)) {
      event('content_block_delta', {
        type: 'content_block_delta',
        index: at,
        delta: { type: 'input_json_delta', partial_json: piece }
      })
    }
    event('content_block_stop', { type: 'content_block_stop', index: at })
    event('message_delta', {
      type: 'message_delta',
      delta: { stop_reason: 'tool_use', stop_sequence: null },
      usage: { output_tokens: 1 }
    })
  } else {
    event('content_block_start', {
      type: 'content_block_start',
      index: at,
      content_block: { type: 'text', text: '' }
    })
    for (const piece of chunks(step.text ?? '', 96)) {
      event('content_block_delta', {
        type: 'content_block_delta',
        index: at,
        delta: { type: 'text_delta', text: piece }
      })
    }
    event('content_block_stop', { type: 'content_block_stop', index: at })
    event('message_delta', {
      type: 'message_delta',
      delta: { stop_reason: 'end_turn', stop_sequence: null },
      usage: { output_tokens: 1 }
    })
  }
  event('message_stop', { type: 'message_stop' })
  return lines.join('')
}

export interface ScriptedModel {
  /** Install as `globalThis.fetch`. */
  fetch: typeof fetch
  requests: RecordedRequest[]
}

/**
 * One scripted endpoint, as a `fetch` stub.
 *
 * `answerAs` decides what comes back over the wire: `stream` writes SSE and
 * `whole` writes one JSON object, whatever the request asked for. That is the
 * fourth leg of the conformance matrix — an endpoint that ignores `stream` is
 * a gateway that buffers, and the engine has to read the answer it got rather
 * than the answer it asked for.
 */
export function scriptedModel(options: {
  api: EndpointKind
  answerAs: 'stream' | 'whole'
  /** THINKING-SPEC's mode for this endpoint. `off` is phase A, unchanged. */
  thinking?: ThinkingMode
  /**
   * Whether the critic's `validate` is asked about the draft the runtime
   * already refused.
   *
   * This is the **`refuted: true` branch** ADR-0001 records as never executed
   * outside a verifier mutation: the critic causes a real `validate` over
   * DRAFT_V1, the runtime says `invalid`, and the desk renders a refutation.
   */
  refuted?: boolean
  /**
   * Where this endpoint puts its signatures, which is a different axis from
   * whether it thinks.
   *
   * - `thought` — on the thought summary, the call unsigned. What a model that
   *   streams summaries and calls one tool at a time sends.
   * - `call` — the summary unsigned and the signature on the **functionCall**
   *   part, which is the documented placement for function calling: one signed
   *   call per sequential step.
   * - `parallel` — two calls in one turn, the **first** signed and the second
   *   not, which is the documented placement for parallel calls.
   * - `empty` — two signed thought parts with **no text at all**, which the wire
   *   emits when a summary was not streamed. There is nothing for a reader to
   *   see and the signature is the only evidence the model reasoned, so this is
   *   the shape the `always` inference and the exact replay are both measured
   *   against.
   */
  signatures?: 'thought' | 'call' | 'parallel' | 'empty'
}): ScriptedModel {
  const requests: RecordedRequest[] = []
  const state = { lastBase: undefined as number | undefined, repeats: 0 }
  const mode: ThinkingMode = options.thinking ?? 'off'
  const topology = options.signatures ?? 'thought'
  const critic = criticSteps(options.refuted === true)
  /**
   * The step ids this endpoint has emitted reasoning for, **per conversation**.
   *
   * The refutation pass is a second conversation with its own history, and it
   * carries none of the main loop's thinking blocks — correctly. A single list
   * would have demanded the loop's signatures back from the critic and refused
   * a session that did exactly the right thing.
   */
  const emitted: { loop: string[]; critic: string[] } = { loop: [], critic: [] }

  /** The 400 an endpoint with no thinking answers, in each protocol's shape. */
  const refuse = (message: string): Response => {
    const body =
      options.api === 'anthropic'
        ? { type: 'error', error: { type: 'invalid_request_error', message } }
        : options.api === 'gemini'
          ? { error: { code: 400, message, status: 'INVALID_ARGUMENT' } }
          : { error: { message, type: 'invalid_request_error', param: null, code: null } }
    return new Response(JSON.stringify(body), {
      status: 400,
      headers: { 'content-type': 'application/json' }
    })
  }

  const stub = async (input: unknown, init?: RequestInit): Promise<Response> => {
    const url = String(input)
    const headers = (init?.headers ?? {}) as Record<string, string>
    const raw = String(init?.body ?? '{}')
    const body = JSON.parse(raw) as {
      messages?: unknown[]
      contents?: unknown[]
      tools?: { name?: string; function?: { name?: string } }[]
      stream?: boolean
      max_tokens?: unknown
      [member: string]: unknown
    }
    // **`contents` on one wire and `messages` on the other two**, and the same
    // list either way: the conversation this request carried back.
    const messages = (options.api === 'gemini' ? body.contents : body.messages) ?? []
    const param =
      options.api === 'anthropic'
        ? anthropicThinkingParam(body)
        : options.api === 'gemini'
          ? geminiThinkingParam(body)
          : openAiThinkingParam(body)

    // **The refutation pass is a second conversation**, and the marker is how
    // this endpoint tells the two apart. THINKING-SPEC §9.2.
    const refutation = raw.includes(MARKER)
    const n =
      options.api === 'anthropic'
        ? countAnthropic(messages)
        : options.api === 'gemini'
          ? countGemini(messages)
          : countOpenAi(messages)
    const step = refutation
      ? critic[Math.min(n, critic.length - 1)]!
      : chooseStep(state, n)

    const lane = refutation ? emitted.critic : emitted.loop
    const expected = lane.map((id) => thinkSignature({ id }))
    const carried =
      options.api === 'anthropic'
        ? anthropicCarried(messages, expected)
        : options.api === 'gemini'
          ? geminiCarried(messages, expected)
          : { carried: [], truncated: [], malformed: [] }
    // **The Gemini wire asks to stream in the address, not in the body.** A
    // reader that looked for a `stream` member would record every streamed call
    // on this family as a whole one.
    //
    // Read off the **path** and not off the whole address, so the method this
    // endpoint was called at is decided by the resource it names and by nothing
    // in the query — and so that this is visibly not the thing
    // `enforcement.test.ts` (4) forbids: no configured endpoint is compared to
    // anything here, and this fixture *is* the endpoint.
    const streamRequested =
      options.api === 'gemini'
        ? new URL(url, 'http://desk.invalid').pathname.endsWith(':streamGenerateContent')
        : Boolean(body.stream)
    const toolNames =
      options.api === 'gemini'
        ? (body.tools ?? []).flatMap((tool) =>
            ((tool as { functionDeclarations?: { name?: string }[] }).functionDeclarations ?? []).map(
              (declared) => String(declared.name ?? '')
            )
          )
        : (body.tools ?? []).map((tool) => String(tool.function?.name ?? tool.name ?? ''))
    requests.push({
      url,
      headerNames: Object.keys(headers).map((name) => name.toLowerCase()),
      api: options.api,
      streamRequested,
      toolNames,
      messageCount: messages.length,
      step: step.id,
      results: n,
      bodyMembers: Object.keys(body as Record<string, unknown>).sort(),
      thinkingRequested: param !== null,
      thinkingParam: param,
      refutation,
      signaturesCarried: carried.carried,
      signaturesExpected: expected,
      signaturesMissing: expected.filter((signature) => !carried.carried.includes(signature)),
      signaturesTruncated: carried.truncated,
      signaturesMalformed: carried.malformed,
      reasoningIn: options.api === 'openai-compatible' ? openAiCarried(messages) : [],
      schemaKeywords: options.api === 'gemini' ? geminiSchemaKeywords(body.tools ?? []) : [],
      schemas: options.api === 'gemini' ? geminiSchemas(body.tools ?? []) : [],
      modelParts: options.api === 'gemini' ? geminiModelParts(messages) : []
    })

    // **The schema subset, checked before anything is answered.** Gemini's
    // `parameters` is an OpenAPI subset, and this is the endpoint half of the
    // desk's removal ruling: a declaration carrying one of the removed keywords
    // is refused here, so a leg that passes has shown the model a contract this
    // wire actually accepts rather than merely one the desk believes it
    // trimmed.
    if (options.api === 'gemini') {
      const offending = geminiSchemaKeywords(body.tools ?? []).find((keyword) =>
        GEMINI_SCHEMA_REMOVALS.includes(keyword)
      )
      if (offending !== undefined) {
        return refuse(
          `Invalid JSON payload received. Unknown name "${offending}" at ` +
            `'tools[0].function_declarations[0].parameters': Cannot find field.`
        )
      }
    }
    // The other half of the same ruling: a keyword the removal list does **not**
    // name is refused by this endpoint too, and the desk must report it rather
    // than widen the list.
    if (mode === 'refuses-a-keyword') {
      const named = geminiSchemaKeywords(body.tools ?? []).includes('properties')
      if (named) {
        return refuse(
          `Invalid JSON payload received. Unknown name "properties" at ` +
            `'tools[0].function_declarations[0].parameters': Cannot find field.`
        )
      }
    }

    // **The refusals, before anything is answered.** `nothink` is the degrade's
    // leg — an endpoint that has no thinking at all — `enabled-only` is the
    // dialect fallback's, an endpoint that has thinking under the other
    // spelling, and `no-off` is a model that cannot be turned off, which is the
    // `always` state reached by a refusal.
    if (mode === 'no-off' && geminiAsksForNone(body)) {
      // **Refused at both spellings.** A model that answered only the budget
      // spelling would be a model that takes the level one, which is the
      // dialect fallback's case and not this one — so the leg would reach
      // `always` for the wrong reason.
      const config = geminiThinkingConfig(body) ?? {}
      const named = config.thinkingBudget === 0 ? 'thinkingBudget' : 'thinkingLevel'
      return refuse(`${named}: thinking cannot be disabled for this model`)
    }
    if (mode === 'nothink' && param !== null) {
      // **Each protocol names the member it was actually sent.** The desk's
      // classifier requires that — a 400 whose prose names none of the members
      // this desk added is an ordinary model error, deliberately — so a fixture
      // that wrote one wire's member name on every wire would be testing the
      // desk's tolerance rather than its rule.
      return refuse(
        options.api === 'anthropic'
          ? 'thinking: Extra inputs are not permitted'
          : options.api === 'gemini'
            ? 'Invalid JSON payload received. Unknown name "thinkingConfig" at ' +
              "'generation_config': Cannot find field."
            : 'Unsupported parameter: reasoning_effort'
      )
    }
    if (mode === 'level-only') {
      const config = geminiThinkingConfig(body)
      if (config !== null && config.thinkingBudget !== undefined) {
        return refuse(
          'Invalid JSON payload received. Unknown name "thinkingBudget" at ' +
            "'generation_config.thinking_config': Cannot find field."
        )
      }
    }
    if (mode === 'enabled-only' && options.api === 'anthropic') {
      const asked = param?.value as { type?: unknown; budget_tokens?: unknown } | undefined
      if (asked?.type === 'adaptive') {
        return refuse('Adaptive thinking is not supported by this model')
      }
      // **The endpoint enforces what the protocol requires.** The thinking
      // budget is spent out of the request's maximum, so a budget at or above
      // `max_tokens` is a request this endpoint refuses — which is what a real
      // one does, and what a fixture that merely accepted the member could not
      // have shown.
      if (typeof asked?.budget_tokens === 'number') {
        const maximum = body.max_tokens
        if (typeof maximum !== 'number' || asked.budget_tokens >= maximum) {
          return refuse(
            `thinking.budget_tokens: must be less than max_tokens (budget ${asked.budget_tokens}, ` +
              `max_tokens ${String(maximum)})`
          )
        }
      }
    }

    // **A continuation that asks for thinking must carry what it was given.**
    // Anthropic's rule is that thinking blocks come back complete and
    // unmodified while thinking is on, so an endpoint refuses a request that
    // both asks for it and has dropped or altered a block it signed. The split
    // probe is where a client is most likely to produce exactly that, which is
    // why the check lives on this mode: a desk that merely *filtered* the
    // damaged block out of an otherwise unchanged request fails here.
    if (mode === 'split' && param !== null && n >= 1) {
      const missing = expected.filter((signature) => !carried.carried.includes(signature))
      if (missing.length > 0 || carried.truncated.length > 0 || carried.malformed.length > 0) {
        return refuse(
          'thinking blocks must be preserved while thinking is enabled: this request asks for ' +
            'thinking and does not carry back every block this endpoint signed'
        )
      }
    }

    // **A continuation that asks for thinking must carry back what it was
    // given, on this wire too** — Google's own rule is that thought parts are
    // resent exactly as they were received, so the check that lives on the
    // Anthropic split probe lives on **every** thinking leg here. An engine that
    // dropped, truncated or emptied a signed part is refused rather than
    // quietly answered.
    if (options.api === 'gemini' && param !== null && n >= 1) {
      const missing = expected.filter((signature) => !carried.carried.includes(signature))
      if (missing.length > 0 || carried.truncated.length > 0 || carried.malformed.length > 0) {
        return refuse(
          'thought parts must be resent exactly as they were received: this request asks for ' +
            'thinking and does not carry back every part this endpoint signed'
        )
      }
    }

    // Thinking output must not change the step logic: neither a thinking block
    // nor a reasoning member is a tool result, so `chooseStep` is untouched.
    //
    // **`always` is the exception, and it is the whole of that mode**: a model
    // that reasons whatever it was asked for. The desk asked for none and gets
    // thought parts anyway, which is what the two-consecutive-turns rule is
    // there to settle.
    const reasoning =
      mode === 'always' || mode === 'no-off'
        ? true
        : mode !== 'off' && mode !== 'nothink' && param !== null
    // See `chatterText`: the two modes that measure an endpoint which reasons
    // unasked have to produce turns that carry an answer, or the desk's own
    // "a tool-only turn is no evidence" rule means the leg never concludes.
    const chatter = mode === 'always' || mode === 'no-off'
    if (reasoning && !lane.includes(step.id)) lane.push(step.id)

    if (options.answerAs === 'whole') {
      const answer =
        options.api === 'anthropic'
          ? anthropicAnswer(step, reasoning)
          : options.api === 'gemini'
            ? geminiAnswer(step, reasoning, chatter, topology)
            : openAiAnswer(step, reasoning)
      return new Response(JSON.stringify(answer), {
        status: 200,
        headers: { 'content-type': 'application/json' }
      })
    }
    const text =
      options.api === 'anthropic'
        ? anthropicStream(step, reasoning, mode === 'split')
        : options.api === 'gemini'
          ? geminiStream(step, reasoning, chatter, topology)
          : openAiStream(step, reasoning)
    return new Response(text, {
      status: 200,
      headers: { 'content-type': 'text/event-stream' }
    })
  }

  return { fetch: stub as unknown as typeof fetch, requests }
}
