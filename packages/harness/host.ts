/**
 * The host's side of the harness: every value and capability the harness read
 * from a Desk feature before it moved into this package.
 *
 * Two kinds of thing live here, and nothing else.
 *
 * - **Contract values and types.** The runtime tool ceiling, the engine ids,
 *   the endpoint families and the thinking tiers, spelled as the wire contract
 *   spells them (`contract/desk/inventory.json`, `contract/schemas/`) and held
 *   to it by `host.test.ts`. A host's own types for these are structurally the
 *   same, so a host passes its values in unchanged.
 * - **Injection points.** What only a host can do: hold the session and its
 *   bearer, build its own addresses and headers, open its socket, choose the
 *   reply language, read its files and preparations, and supply the parts of
 *   the prompt only it holds (the project brief, the project's own
 *   instructions, recalled memory; ADR-0005). Each is a binding the host
 *   replaces once, with `configureHarness`, before it starts a run. One the
 *   host has not supplied throws and names itself; nothing here guesses a
 *   host's answer. The three prompt parts, `endpointCapabilities` and
 *   `modelsFor` are the exceptions: each has an answer before the host
 *   supplies it, nothing.
 *
 * Every moved file that read a Desk module reads this one instead, under the
 * same names, so the moved code is the Desk's code with its import lines
 * changed and nothing else.
 */
import type { Transport } from '@modelcontextprotocol/sdk/shared/transport.js'
import type { ChatPlan } from './planning/model'
import type { HeldImage } from './images'

/** The endpoint families a model connection speaks (the Desk's `EndpointKind`). */
export type EndpointKind = 'openai-compatible' | 'anthropic' | 'gemini'

/** The depths a run may ask a model to reason at (the Desk's `ThinkingTier`). */
export type ThinkingTier = 'off' | 'on' | 'ultra'

/**
 * The runtime tool ceiling: the six tools an assistant may ever be allowed,
 * every one a read. The tool gate intersects a session's allow-list with this.
 * The contract's `runtimeCeiling` (`contract/schemas/tool-offer.json`): the
 * Desk's five (`runtimeToolCeiling` in `contract/desk/inventory.json`), then
 * `experimental_test_cases`, which the harness admits under an argument
 * allow-list and a per-turn cap (agent#7; see `toolGate.ts` for the tools that
 * stay refused and why).
 */
export const ASSISTANT_TOOLS = [
  'get_schema',
  'list_examples',
  'get_example',
  'validate',
  'experimental_evaluate',
  'experimental_test_cases'
] as const
export type AssistantTool = (typeof ASSISTANT_TOOLS)[number]

/**
 * The engines this package carries: the contract's `engines`, the Desk's two (`vercel`, `codex`,
 * `contract/desk/inventory.json`) and then the one this package adds (`deepagents`, ADR-0003).
 */
export const ASSISTANT_ENGINES = ['vercel', 'codex', 'deepagents'] as const
export type AssistantEngine = (typeof ASSISTANT_ENGINES)[number]

/**
 * A reply language, as the host names it (a BCP 47 identifier). The host owns
 * the list and the instruction text; the harness passes the value through.
 */
export type Language = string

/** Which configured AI connection a model or agent call is bound to. */
export interface AITarget {
  connectionId?: string
  connectionRevision?: string
  connectionName?: string
}

/** A chat, as Plan mode reads and writes it: the members `planning/tools.ts` touches. */
export interface Chat {
  id: string
  interaction?: 'plan' | 'work'
  plans?: ChatPlan[]
  pack?: { path: string }
  graph?: { path: string }
  input?: { id: string }
}

/** The host's chat store, as Plan mode uses it. */
export interface ChatStore {
  getSnapshot(): { chats: Chat[]; drafts: Chat[] }
  update(id: string, patch: { interaction?: 'plan' | 'work'; plans?: ChatPlan[] }): void
  checkpoint(signal?: AbortSignal): Promise<unknown>
  readonly running: string | undefined
}

/** A saved file's revision, as Plan mode pins it. */
export interface FileRevision {
  sha256: string
}

/** A saved input preparation's revision, as Plan mode pins it. */
export interface PreparationReply {
  sha256: string
}

/**
 * Desk-authored text, filled from its named values: `{{name}}` becomes
 * `values.name`. The harness's own messages pass through this as they did
 * through the Desk's `sourceMessage`; none of them carries a `count`, so the
 * Desk's plural table never applied to them and the text is the same.
 */
export function sourceMessage(source: string, values: Record<string, unknown> = {}): string {
  return source.replace(/\{\{(\w+)\}\}/g, (token, name: string) =>
    Object.hasOwn(values, name) ? String(values[name]) : token)
}

/**
 * Where a model loop puts the execution state after the first request, and
 * what the host knows about one endpoint and model: both are the capability
 * record's (`capabilities.ts`, the contract's `capability.json`), re-exported
 * here where agent#9 first stated them. The host answers with any members of
 * the record; the harness completes the rest from the family's default record.
 */
export type { StatePlacement } from './capabilities'
export type EndpointCapabilities = Partial<import('./capabilities').ModelCapabilities>

/**
 * One part of the prompt the host supplies (ADR-0005): its text, or nothing (`undefined`, `null` or
 * the empty string), now or as a promise. The adapter places it in its layer of the request; the
 * host never rewrites a request behind the relay.
 */
export type HostPart = string | null | undefined | Promise<string | null | undefined>

/** What a host supplies. Every member is an injection point; see the package README. */
export interface HarnessHost {
  /** The reply-language section a run's instructions end with (was `i18n/assistantLanguage`). */
  assistantLanguageInstructions(locale?: Language): string
  /** The page's language, where a session names none (was `i18n/locales`). */
  currentLanguage(): Language

  /** The session id a request carries, or a throw of `NoSession` (was `mcp/session`). */
  sessionBearer(): Promise<string>
  /** The sentence the session ended with, or null while it has not (was `mcp/session`). */
  sessionEnded(): string | null
  /** Be told when the session ends; the result unsubscribes (was `mcp/session`). */
  whenSessionEnds(listener: () => void): () => void
  /** Forget the session, because the host refused it (was `mcp/session`). */
  forgetSession(reason?: string): void
  /** The code the host marked one of its own refusals with, or null (was `mcp/session`). */
  refusalCode(answered: Response): string | null
  /** Let go of an answer nothing will read (was `mcp/session`). */
  discardBody(answered: Response): Promise<void>
  /** The error a call without a session throws (was `mcp/session`). */
  NoSession: new (message?: string) => Error
  /** What that error says by default (was `mcp/session`). */
  NO_SESSION_MESSAGE: string

  /** One host URL from a path and its own query parameters (was `files/client`). */
  chassisUrl(path: string, params?: Record<string, string>): string
  /** The headers that scope a request to the host's open desk (was `desks/scope`). */
  deskHeaders(): Record<string, string>
  /** A socket address scoped to the host's open desk (was `desks/scope`). */
  deskSocketURL(url: string): string
  /** The headers that bind a call to a configured AI connection (was `assistant/aiConnections`). */
  aiHeaders(id?: string, revision?: string): Record<string, string>
  /** The host's MCP socket address (was `mcp/McpProvider`). */
  socketURL(): string
  /** The subprotocols a socket offers for a session id (was `mcp/McpProvider`). */
  socketProtocols(id: string): string[]
  /** The host's MCP socket transport (was `mcp/transport`). */
  DeskWebSocketTransport: new (url: string, protocols: string[]) => Transport

  /** An authenticated request to the host (was `files/client`). */
  deskFetch(input: string, init?: RequestInit): Promise<Response>
  /** A host answer read as JSON, or its refusal thrown (was `files/client`). */
  answer<T>(response: Response): Promise<T>
  /** One saved file's current revision (was `files/client`). */
  readFile(path: string, signal?: AbortSignal): Promise<FileRevision>
  /** One request to the host's input-preparation API (was `dialects/client`). */
  dialectAPI<T>(path?: string, method?: string, body?: unknown, revision?: string, signal?: AbortSignal): Promise<T>

  /**
   * What the host knows about one endpoint and model: its capability record,
   * or any members of it (agent#9 placed the state by two; agent#4 made it the
   * whole record). Unlike the others this has an answer before the host
   * supplies it, nothing known, and the harness completes whatever the host
   * leaves out from the family's default record (`FAMILY_CAPABILITIES` in
   * `capabilities.ts`, the Desk's three families as records).
   */
  endpointCapabilities(endpoint: { family: EndpointKind; model: string }): EndpointCapabilities
  /**
   * An image as text, for a model whose record says it is not shown images: the
   * host's render adapter. New with agent#5.
   */
  renderImageToText(image: HeldImage, signal: AbortSignal): Promise<string>
  /**
   * The models a run's connection names for its subordinate work: `child`, the model delegated
   * children run on (agent#15), and `critic`, the critic's (agent#10). Each is a cheaper model the same
   * connection serves, named by the host from its connection settings, or absent or null for the
   * run's own model. Asked once per run, with the connection the run is bound to (the run's
   * `context.connection`), its engine, its family (a model engine's) and its model; a host keyed on
   * the connection answers each connection for itself. Like `endpointCapabilities` it has an answer
   * before the host supplies it: none. A child's or the critic's requests go through the same
   * capability as the run's, so a model the connection does not serve is refused there.
   *
   * `concurrentRequests` (agent#17): how many model requests the connection serves at once (the
   * host's relay slots for it, a provider's concurrency limit): a delegation batch runs that many
   * children at a time, at most its three tasks; absent or null, two. A connection setting, answered
   * here with the connection rather than on the capability record, which is keyed on the family and
   * model and so cannot tell two connections serving one model apart. An agent capability runs one
   * child at a time whatever this says.
   */
  modelsFor(run: RunIdentity): { critic?: string | null; child?: string | null; concurrentRequests?: number | null }

  /**
   * The project brief (ADR-0005): the text of the project file the host's profile names, or nothing.
   * Asked once per run, before its first request; the adapter places it in the third layer of every
   * request, after the package's guidance and before the project instructions, headed as background
   * that is not approval, evidence or authority (`BRIEF_HEADER`, `prompt.ts`). At most `briefBytes`;
   * a longer answer is refused and the run fails before any request (`configuration`). Unsupplied,
   * nothing.
   */
  brief(): HostPart
  /**
   * Recalled project memory for `query` (ADR-0005): the text, or nothing. Asked once per run, before
   * its first request, with the person's message (`context.currentRequest.message`; a delegated
   * child's objective; the host's prompt where neither is given), and not asked where the host said
   * memory is off (`context.memory: false`). Placed in the fifth layer, the last of the stable
   * prefix, so it is the same on every request of the run. At most `memoryBytes`; a longer answer is
   * refused as the brief's is. Unsupplied, nothing.
   */
  memoryFor(query: string): HostPart
  /**
   * The project's own instructions (ADR-0005): the per-project block the person wrote, or nothing.
   * Instructions at the lowest rank, not data: placed in the fourth layer, after the package's
   * guidance and the brief, headed "the project's own instructions; follow them where they do not
   * contradict the rules above" (`PROJECT_INSTRUCTIONS_HEADER`). They cannot grant a tool or change an
   * effect: the offer and the gate are code. At most `projectInstructionsBytes`; a longer answer is
   * refused as the brief's is. Asked once per run. Unsupplied, nothing.
   */
  projectInstructions(): HostPart
}

/** What `modelsFor` is asked with: the run's connection, engine, family (a model engine's) and model. */
export interface RunIdentity { connection?: AITarget; engine: AssistantEngine; family?: EndpointKind; model: string }
/**
 * What a run's connection names for its subordinate work: the models it runs on (each the host's
 * answer, or the run's own model) and, where the host answered one, how many children run at once.
 */
export interface RunModels { critic: string; child: string; concurrentRequests?: number }

/** What an injection point does until the host supplies it. */
function unsupplied(name: keyof HarnessHost): never {
  throw new Error(
    `the host has not supplied ${name}: call configureHarness with it before starting a run`
  )
}

/** Building the error a call without a session throws is itself a use of the host's class. */
class UnsuppliedNoSession extends Error {
  constructor() {
    super()
    unsupplied('NoSession')
  }
}

export let assistantLanguageInstructions: HarnessHost['assistantLanguageInstructions'] = () =>
  unsupplied('assistantLanguageInstructions')
export let currentLanguage: HarnessHost['currentLanguage'] = () => unsupplied('currentLanguage')
export let sessionBearer: HarnessHost['sessionBearer'] = () => unsupplied('sessionBearer')
export let sessionEnded: HarnessHost['sessionEnded'] = () => unsupplied('sessionEnded')
export let whenSessionEnds: HarnessHost['whenSessionEnds'] = () => unsupplied('whenSessionEnds')
export let forgetSession: HarnessHost['forgetSession'] = () => unsupplied('forgetSession')
export let refusalCode: HarnessHost['refusalCode'] = () => unsupplied('refusalCode')
export let discardBody: HarnessHost['discardBody'] = () => unsupplied('discardBody')
export let NoSession: HarnessHost['NoSession'] = UnsuppliedNoSession
/** A sentence, read only to build a `NoSession`, which throws until the host supplies it. */
export let NO_SESSION_MESSAGE: HarnessHost['NO_SESSION_MESSAGE'] = 'no session'
export let chassisUrl: HarnessHost['chassisUrl'] = () => unsupplied('chassisUrl')
export let deskHeaders: HarnessHost['deskHeaders'] = () => unsupplied('deskHeaders')
export let deskSocketURL: HarnessHost['deskSocketURL'] = () => unsupplied('deskSocketURL')
export let aiHeaders: HarnessHost['aiHeaders'] = () => unsupplied('aiHeaders')
export let socketURL: HarnessHost['socketURL'] = () => unsupplied('socketURL')
export let socketProtocols: HarnessHost['socketProtocols'] = () => unsupplied('socketProtocols')
export let DeskWebSocketTransport: HarnessHost['DeskWebSocketTransport'] = class {
  constructor() {
    unsupplied('DeskWebSocketTransport')
  }
} as unknown as HarnessHost['DeskWebSocketTransport']
export let deskFetch: HarnessHost['deskFetch'] = () => unsupplied('deskFetch')
export let answer: HarnessHost['answer'] = () => unsupplied('answer')
export let readFile: HarnessHost['readFile'] = () => unsupplied('readFile')
export let dialectAPI: HarnessHost['dialectAPI'] = () => unsupplied('dialectAPI')
export let endpointCapabilities: HarnessHost['endpointCapabilities'] = () => ({})
export let renderImageToText: HarnessHost['renderImageToText'] = () => unsupplied('renderImageToText')
export let modelsFor: HarnessHost['modelsFor'] = () => ({})
export let brief: HarnessHost['brief'] = () => undefined
export let memoryFor: HarnessHost['memoryFor'] = () => undefined
export let projectInstructions: HarnessHost['projectInstructions'] = () => undefined

/**
 * The host's `modelsFor` answer for one run, read: each member a model name, or the run's own model
 * where the host names none. An answer that is not an object, or a member that is not a model name
 * (empty, longer than 128 characters, or carrying a line break or NUL), is refused rather than sent;
 * so is a `concurrentRequests` that is not a whole number from 1 to 16.
 */
export function runModels(run: RunIdentity): RunModels {
  const named = modelsFor(run) as unknown
  if (named === null || typeof named !== 'object' || Array.isArray(named)) throw new Error('the host answered modelsFor with something other than an object')
  const pick = (member: 'critic' | 'child'): string => {
    const value = (named as Record<string, unknown>)[member]
    if (value === undefined || value === null) return run.model
    if (typeof value !== 'string' || value === '' || value.length > 128 || /[\r\n\0]/.test(value)) {
      throw new Error(`the host named a ${member} model that is not a model name: 1 to 128 characters, no line break or NUL`)
    }
    return value
  }
  const capacity = (named as Record<string, unknown>).concurrentRequests
  if (capacity !== undefined && capacity !== null && (!Number.isInteger(capacity) || Number(capacity) < 1 || Number(capacity) > 16)) {
    throw new Error('the host answered a concurrentRequests that is not a whole number from 1 to 16')
  }
  return { critic: pick('critic'), child: pick('child'), ...(capacity === undefined || capacity === null ? {} : { concurrentRequests: Number(capacity) }) }
}

/**
 * Supply the host's injection points. Members left out keep what they had, so
 * a host may wire them in parts; a run that reaches one never supplied throws.
 */
export function configureHarness(host: Partial<HarnessHost>): void {
  if (host.assistantLanguageInstructions) assistantLanguageInstructions = host.assistantLanguageInstructions
  if (host.currentLanguage) currentLanguage = host.currentLanguage
  if (host.sessionBearer) sessionBearer = host.sessionBearer
  if (host.sessionEnded) sessionEnded = host.sessionEnded
  if (host.whenSessionEnds) whenSessionEnds = host.whenSessionEnds
  if (host.forgetSession) forgetSession = host.forgetSession
  if (host.refusalCode) refusalCode = host.refusalCode
  if (host.discardBody) discardBody = host.discardBody
  if (host.NoSession) NoSession = host.NoSession
  if (host.NO_SESSION_MESSAGE !== undefined) NO_SESSION_MESSAGE = host.NO_SESSION_MESSAGE
  if (host.chassisUrl) chassisUrl = host.chassisUrl
  if (host.deskHeaders) deskHeaders = host.deskHeaders
  if (host.deskSocketURL) deskSocketURL = host.deskSocketURL
  if (host.aiHeaders) aiHeaders = host.aiHeaders
  if (host.socketURL) socketURL = host.socketURL
  if (host.socketProtocols) socketProtocols = host.socketProtocols
  if (host.DeskWebSocketTransport) DeskWebSocketTransport = host.DeskWebSocketTransport
  if (host.deskFetch) deskFetch = host.deskFetch
  if (host.answer) answer = host.answer
  if (host.readFile) readFile = host.readFile
  if (host.dialectAPI) dialectAPI = host.dialectAPI
  if (host.endpointCapabilities) endpointCapabilities = host.endpointCapabilities
  if (host.renderImageToText) renderImageToText = host.renderImageToText
  if (host.modelsFor) modelsFor = host.modelsFor
  if (host.brief) brief = host.brief
  if (host.memoryFor) memoryFor = host.memoryFor
  if (host.projectInstructions) projectInstructions = host.projectInstructions
}
