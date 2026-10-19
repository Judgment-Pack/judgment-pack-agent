/**
 * The `deepagents` adapter's chat model: a LangChain chat model whose only reach to a model is the
 * host's relay capability (ADR-0003, decision 1).
 *
 * It holds no key, no address and no `fetch`. LangChain itself has one other way out, its LangSmith
 * tracer, which an environment's tracing variables turn on and which sends through the global `fetch`
 * with the environment's key; a run whose environment sets one is refused before an agent is built
 * (`tracingAsked`, `loop.ts`), so on every run this engine starts, the relay is the only way out. A request is a path suffix, a body and the protocol's
 * own headers, handed to `session.model.call` (wrapped in the one retry a refusal earns), which the
 * host binds to its relay and its bearer; the relay attaches the provider's credential on the far
 * side. The body is the one `wire.ts` writes for the session's family, and the answer is read back
 * there, as a stream or whole.
 *
 * **The events of an answer are said where it is read.** The listener the run hands this model puts
 * the prose, the reasoning and the end of each passage on the run's ordered channel as the answer
 * arrives, before the step's tool calls are dispatched (see `loop.ts` for why LangGraph's own stream
 * is not the source of these events).
 *
 * What the boundary decides for one request (the system text and where the execution state sits, the
 * tier, one call per turn) reaches the model as the call options it binds with its tools
 * (`judgmentPack`), so nothing about a request is decided here.
 */
import { BaseChatModel, type BaseChatModelCallOptions, type BindToolsInput } from '@langchain/core/language_models/chat_models'
import { SystemMessage, type BaseMessage } from '@langchain/core/messages'
import type { ChatResult } from '@langchain/core/outputs'
import { withAbort } from '../contract'
import { answerMessage, encodeRequest, readAnswer, RelayRefusal, type WireListener, type WireTool } from './wire'
import type { EndpointKind } from '../../host'
import type { ModelCall } from '../../engine'
import type { ReasoningField } from '../../capabilities'

/** What the boundary decides for one request, bound with the request's tools. */
export interface RelaySettings {
  /** The tier's members from the host's table, or none. */
  tier: Record<string, unknown> | null
  /** One call per turn on a request that offers tools, where the record says so (agent#4). */
  oneCallPerTurn: boolean
}

export interface RelayCallOptions extends BaseChatModelCallOptions {
  judgmentPack?: RelaySettings
}

/** What one run's model is built with. */
export interface RelayModelFields {
  family: EndpointKind
  /** The model name the request names; the critic's and a child's may differ on the same connection. */
  model: string
  /** The host's capability, already wrapped in the retry rule. The only way out. */
  call: ModelCall
  /** The run's own signal: every request and every read is bounded by it. */
  signal: AbortSignal
  /** The output reserve, read on each request (a tier may raise it once, `reserveOutput`). */
  maxOutputTokens: () => number
  reasoningField: ReasoningField | null
  wholeAnswer: boolean
  images: { count: number; bytes: number }
  listener: WireListener
  /** A fresh call id, for a wire whose calls carry none (Gemini). */
  ids: () => string
}

/** A tool as the boundary declares it: the schema the record says the model is shown, as `schema`. */
function wireTool(tool: BindToolsInput): WireTool {
  const held = tool as { name?: unknown; description?: unknown; schema?: unknown }
  return { name: String(held.name ?? ''), description: String(held.description ?? ''), schema: held.schema }
}

/** The system text the boundary set: its head, and the execution state where it sits at the end. */
function systemOf(messages: BaseMessage[]): { system: { head: string; tail?: string }; rest: BaseMessage[] } {
  const first = messages[0]
  if (first === undefined || !SystemMessage.isInstance(first)) return { system: { head: '' }, rest: messages }
  const content = first.content
  if (typeof content === 'string') return { system: { head: content }, rest: messages.slice(1) }
  const texts = (content as { type?: string; text?: string }[]).filter((part) => part.type === 'text').map((part) => part.text ?? '')
  return { system: { head: texts[0] ?? '', ...(texts.length > 1 ? { tail: texts.slice(1).join('\n') } : {}) }, rest: messages.slice(1) }
}

export class RelayChatModel extends BaseChatModel<RelayCallOptions> {
  static lc_name(): string {
    return 'RelayChatModel'
  }

  lc_serializable = false

  readonly #relay: RelayModelFields

  constructor(relay: RelayModelFields) {
    super({})
    this.#relay = relay
  }

  _llmType(): string {
    return 'judgment-pack-relay'
  }

  /** The tools are the boundary's declarations; their schemas are what the wire shows. */
  override bindTools(tools: BindToolsInput[], kwargs?: Partial<RelayCallOptions>) {
    return this.withConfig({ tools, ...(kwargs ?? {}) } as Partial<RelayCallOptions>)
  }

  async _generate(messages: BaseMessage[], options: this['ParsedCallOptions']): Promise<ChatResult> {
    const settings = options.judgmentPack
    if (settings === undefined) throw new Error('a model request reached the relay without the boundary’s settings; nothing was sent')
    const relay = this.#relay
    const { system, rest } = systemOf(messages)
    const tools = ((options as { tools?: BindToolsInput[] }).tools ?? []).map(wireTool)
    const encoded = encodeRequest({
      family: relay.family,
      model: relay.model,
      system,
      messages: rest,
      tools,
      oneCallPerTurn: settings.oneCallPerTurn && tools.length > 0,
      tier: settings.tier,
      maxOutputTokens: relay.maxOutputTokens(),
      reasoningField: relay.family === 'openai-compatible' ? relay.reasoningField : null,
      wholeAnswer: relay.wholeAnswer,
      images: relay.images
    })
    const signal = options.signal === undefined ? relay.signal : AbortSignal.any([relay.signal, options.signal])
    // Bounded by the run's own signal, and a thunk, so a closed run makes no request at all.
    const answered = await withAbort(() => relay.call(encoded.suffix, { headers: encoded.headers, body: encoded.body, signal }), signal)
    if (!answered.ok) {
      const body = await withAbort(() => answered.text(), signal).catch((cause: unknown) => {
        if ((cause as { name?: unknown })?.name === 'RunCancelled') throw cause
        return ''
      })
      throw new RelayRefusal(answered.status, body, answered.headers)
    }
    const answer = await readAnswer(relay.family, answered, relay.listener, signal, relay.family === 'openai-compatible' ? relay.reasoningField : null, relay.ids)
    const message = answerMessage(answer)
    return { generations: [{ text: answer.text, message }] }
  }
}
