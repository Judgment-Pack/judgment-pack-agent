import type { Language } from './host'
/**
 * The engine contract, as ADR-0001 writes it.
 *
 * An engine is a module the page loads for one session. It receives everything
 * the desk has already decided — the runtime's prompt, the runtime's own tool
 * definitions, a bound `callTool`, the relay's base URL, the tier, an abort
 * signal — and returns a stream of events. Accept and Reject are the desk's
 * actions on the proposal, not engine calls.
 *
 * **Deliberately smaller than any framework's API.** What a framework offers
 * beyond this — its own approval pause, its own memory, its own file tools —
 * stays inside its adapter or is not used. That is the whole point of the slot:
 * the desk's promises are held below the loop, so swapping the loop changes
 * none of them.
 *
 * **`Engine`, not `AssistantEngine`.** The ADR writes the interface as
 * `AssistantEngine`, and this page already spells `AssistantEngine` as the
 * *identifier* a `desk.json` may name (`config/deskConfig.ts`). One name with
 * two meanings in one page is worse than a deviation recorded here: the id
 * keeps the name it has in configuration, and the adapter is `Engine`.
 *
 * **The session carries capabilities and never addresses.** `callTool` is a
 * function with no other member, so an engine cannot reach the transport the
 * ToolGate sits on, cannot open a second one, and cannot call a tool by any
 * route the gate does not see. `model.call` is the same shape for model
 * traffic. The member set is asserted whole in
 * `assistant/enforcement.test.ts`, because the guarantee is "there is nothing
 * else here" rather than "the obvious escape hatch is absent".
 *
 * **`model` is a capability, not a base URL — a deliberate deviation from
 * ADR-0001's contract sketch**, which wrote
 * `model: { family, baseUrl, model }` with `baseUrl` "the chassis relay". The
 * relay used to authenticate with this chassis' session token in the query, so
 * a `baseUrl` an engine could read was **this desk's credential in the engine's
 * hands** — an adapter holding it could open `/ws?token=…` itself with
 * `globalThis.WebSocket` and drive a third, ungated MCP connection.
 *
 * **That reading of the deviation is out of date, and the deviation stands.**
 * No address is a credential any more: the session id is held by the page and
 * sent on a header, so `/ws` spelled by page code is just a URL. Withholding it
 * was never the load-bearing part in any case — the id lives in
 * `sessionStorage`, which is same-origin readable, so an adapter that wanted it
 * could always have read it. What the capability actually buys is that the desk
 * decides **what** an engine can reach — one mount point, a validated path
 * suffix, no header outside the relay's own allow-list — rather than handing
 * over an address and hoping. So the engine is still handed no URL:
 * `model.call(suffix, init)` is a capability the desk binds, and it is the only
 * way an engine reaches a model at all.
 *
 * **What holds that, and what does not.** The member set below is asserted
 * whole, so an engine is *handed* nothing but the capability. It is not
 * prevented from reaching a global: in production an engine runs with the real
 * `fetch` and the real `WebSocket`, and one that spelled `/ws` itself would be
 * admitted on the session this page holds, which page code can read out of
 * `sessionStorage` exactly as it could once read the token there. The sealed
 * globals belong to the conformance suite, which runs every engine's leg with
 * `fetch`, `WebSocket`, `XMLHttpRequest` and `EventSource` replaced by throwing
 * sentinels — so an engine that reaches for one **fails the suite**. That is
 * regression coverage, not runtime isolation: an engine opening its own
 * connection is a bug this repository catches before it ships, not one the
 * running desk prevents.
 *
 * The conformance session holds that structurally rather than by inspection:
 * every leg runs with `fetch`, `WebSocket`, `XMLHttpRequest` and `EventSource`
 * replaced by throwing sentinels for the duration of the engine's run, so an
 * engine that reaches for any of them fails the leg. When the `vercel` adapter
 * lands, this capability is what is passed as the provider's `fetch` option,
 * so the shape survives the next chunk.
 */
import type { AssistantEngine, EndpointKind } from './host'
import type { NormalizedThinking } from './thinking'

/** One tool exactly as `tools/list` served it. Nothing is re-declared. */
export interface McpTool {
  name: string
  description?: string
  inputSchema?: unknown
}

/** One `tools/call` answer, in the shape the wire carries it. */
export interface McpToolResult {
  content?: { type?: string; text?: string }[]
  isError?: boolean
  structuredContent?: unknown
}

/**
 * The only way an engine reaches the runtime.
 *
 * A function and not an object: an object could grow a member, and the member
 * it would grow is the one that skips the gate.
 */
export type CallTool = (name: string, args: Record<string, unknown>) => Promise<McpToolResult>

/**
 * A tool the **desk** executes on the engine's behalf, beside the runtime's.
 *
 * The runtime's tools reach it over the ToolGate; a host tool never leaves the
 * page — the desk's own code runs it, under the desk's own budgets, and hands
 * the model an `McpToolResult` in the shape a runtime answer takes so an engine
 * treats the two alike. Today these are the research tools: a search and a
 * read through the configured gateway, and a citation into what was read.
 * None of them writes anything, and the slot is a member of the session so a
 * run that hands an engine none is a run in which none exists — an engine
 * cannot reach for one it was not handed, exactly as with `callTool`.
 *
 * The schema is the desk's, written in the subset every family's wire carries
 * (an object of string and integer properties, `required`, and nothing else),
 * so the narrowing notice a served schema can earn never fires for one.
 */
export interface HostTool {
  /** Context retention is independent of topic, provider and action permissions. */
  contextPolicy?: import('./context/toolPolicy').ContextToolPolicy
  /** Host-declared effect, never supplied by model arguments. */
  effect?: 'read' | 'proposal' | 'workflow' | 'memory' | 'plan'
  /** Desk interaction; adapters pause before returning any user answer. */
  interaction?: 'question' | 'delegation'
  /** Explicit read-only capability offered to children; absent means parent-only. */
  delegation?: 'read' | 'research'
  /** Host-owned presentation, frozen when this tool is offered; never model arguments. */
  presentation?: { provider: string }
  name: string
  description: string
  inputSchema: unknown
  execute: (args: Record<string, unknown>, signal: AbortSignal) => Promise<McpToolResult>
}

/**
 * What an engine may put in one model request, and the whole of it.
 *
 * A body, a signal, and headers the **protocol** needs. There is no URL member
 * because the address is the desk's, no method because every call on both wire
 * formats is a POST, and no credential because the page holds none.
 */
export interface ModelRequest {
  /** Protocol headers only. Anything outside the relay's allow-list is dropped. */
  headers?: Record<string, string>
  /**
   * `POST` where it is absent, which is what every model call is.
   *
   * **A closed pair rather than any method a caller names.** The relay
   * forwards the method verbatim, so an open member would let whoever holds a
   * capability ask the configured endpoint to *do* something nobody wrote
   * down, with the machine-held credential attached — the same argument that
   * makes the path's colon methods a closed list. `GET` is here because each
   * protocol's model listing is one, and the listing goes over this capability
   * for the reason everything else does: the page names a suffix, and the desk
   * builds the address.
   */
  method?: 'GET' | 'POST'
  /**
   * Required, because every model call has one. A `GET` states the empty
   * string and the desk sends none: `fetch` refuses a body on a `GET`, and an
   * optional member here would make a body something a caller could forget on
   * a `POST` rather than something it must decide.
   */
  body: string
  signal?: AbortSignal
}

/**
 * The only way an engine reaches a model.
 *
 * `suffix` is a path suffix — `chat/completions`, `v1/messages` — held to the
 * relay's own segment rule. It is not a URL and it may not carry a query of its
 * own: the desk builds the address, and the relay attaches the model credential
 * on the far side.
 */
export type ModelCall = (suffix: string, request: ModelRequest) => Promise<Response>

export interface AssistantSession {
  /** Shared, provider-independent context policy and current scoped source copies. */
  context?: import('./context/session').ContextInput
  /** Test design proposes reviewed cases rather than a replacement pack. */
  purpose?: 'test-design' | 'brief' | 'graph'
  /** Reply language captured for this turn; wire keys and source quotations remain exact. */
  replyLanguage?: Language
  /** The runtime's prompt text, from `prompts/get`. */
  prompt: string
  /**
   * The runtime's **testing** prompt, for the refutation pass, or `''`.
   *
   * A second string rather than a capability, and read where the session's
   * other prompt is read: an engine cannot ask the runtime for a prompt —
   * `callTool` is the whole of its reach and `prompts/get` is not a tool — and
   * giving it one would be a second door beside the gate. Empty where the
   * runtime advertises no `test_pack`, or where the tier is off and no critic
   * will run; the critic then works from the desk's one fixed sentence alone.
   */
  testPrompt: string
  /** Permit a prose-only clarification; proposals still require the same explicit fenced envelope. */
  allowConversation?: boolean
  /** Optional presentation events; adapters without them still deliver final messages. */
  interactive?: boolean
  /** Independent of reasoning effort; undefined preserves legacy authoring behavior. */
  adversarialReview?: boolean
  /** The allow-listed tools, exactly as `tools/list` served them. */
  tools: McpTool[]
  /** Bound through the ToolGate. */
  callTool: CallTool
  /** The desk's own tools, executed on the page. Empty where the desk offers none. */
  hostTools: HostTool[]
  /** A capability and a name. No address, and no credential. */
  model: { family: EndpointKind; model: string; call: ModelCall }
  /**
   * The tier, **normalized by the desk** — the tier the file asked for, the
   * wire members that expresses on this endpoint's family, and the state the
   * session starts in.
   *
   * ADR-0001: *"the tier maps to provider parameters in one desk-owned table,
   * per endpoint family, and the engine receives the normalized result."* So an
   * engine puts `wire.members` on the request and never decides what `on` means
   * for an endpoint; the table, the dialect fallback between the two Anthropic
   * spellings and the two states a tier cannot express are `assistant/thinking.ts`.
   */
  thinking: NormalizedThinking
  signal: AbortSignal
}

/**
 * What an engine may say. The ADR's union, verbatim.
 *
 * `guardrail` is on it because the pane renders one stream; the events
 * themselves are emitted by the **desk's** gate rather than by an engine, and
 * the runner interleaves them. An engine that emitted its own would be
 * reporting on a guard it does not hold.
 */
export type AssistantEvent =
  | {type:'execution_checkpoint'; durable?:boolean; checkpoint:import('./execution/state').ExecutionCheckpoint}
  | {type:'budget_paused'; checkpoint:import('./execution/state').ExecutionCheckpoint}
  | { type: 'retry'; attempt: number }
  | { type: 'response_recovery' }
  | { type: 'delegation_requested'; request: import('./delegationContract').DelegationRequest }
  | { type: 'delegated_task'; task: import('./delegationContract').DelegatedTask }
  | { type: 'user_question'; id: string; question: import('./questions').UserQuestion }
  | { type: 'reasoning'; text: string; done: boolean }
  /**
   * The model's own prose in its final turn, outside the proposal fence: what
   * it said to the person, as it said it. Never written by the desk, and absent
   * where the model wrote nothing but the fence.
   */
  | { type: 'message'; text: string }
  | { type: 'message_progress'; text: string }
  | { type: 'tool_call'; callId?: string; name: string; args: unknown; presentation?: { provider: string } }
  | { type: 'tool_result'; callId?: string; name: string; isError: boolean; text: string; structured?: unknown }
  /**
   * `rewrote` and `refused` are the ToolGate's, on the wire. `narrowed` is the
   * desk's own report on the **contract it showed the model**: on a wire whose
   * schema dialect cannot carry a keyword the runtime served, one line per tool
   * that lost something, before the model is asked anything. It is not a guard
   * that fired — nothing was stopped — but it is the same kind of sentence: the
   * desk saying what it did rather than leaving it to be discovered.
   */
  | { type: 'guardrail'; tool: string; action: 'rewrote' | 'refused' | 'narrowed'; detail: string }
  | { type: 'thinking_unavailable'; detail: string }
  | { type: 'critique'; refuted: boolean; checks: { tool: string; status: string }[]; text: string }
  /** `note` where something that ran after the proposal existed did not finish. */
  | { type: 'proposal'; document: unknown; unknowns: string[]; critique?: { refuted: boolean }; note?: ProposalNote }
  | { type: 'error'; message: string; failure?: import('./failures').AIFailure }
  | { type: 'end' }

/**
 * The critic started and did not finish (`refutation.ts`, `criticNote`): its
 * failure, classified, and a host-authored sentence. The proposal is delivered
 * all the same, with no `critique`.
 */
export interface ProposalNote {
  kind: 'critic-unfinished'
  message: string
  failure: import('./failures').AIFailure
}

export type AgentAssistantSession =Omit<AssistantSession, 'model' | 'thinking'> & {
  agent: { model: string; run: import('./agent').AgentRun }
  effort?: import('./agent').CodexEffort
}
export type EngineSession = AssistantSession | AgentAssistantSession

export interface Engine {
  readonly id: AssistantEngine
  start(session: EngineSession): AsyncIterable<AssistantEvent>
}
