/**
 * Tool sources (ADR-0006): tools from outside the Runtime, held at the boundary.
 *
 * A gateway connection's tools, a public MCP server's and a tool the person created all reach the
 * model the same way: as host tools. The host declares each with its source record and executes
 * every call under the person's grants; this package keeps no MCP client and never reaches a
 * source. What it holds is the boundary every engine enters (`context/session.ts`,
 * `contextualSession`), so no engine path reaches past it:
 *
 * - **the declaration** (`declareSources`): a source record the package can read or the run ends
 *   before any request (`configuration`); a tool named like a Runtime tool, a reserved name,
 *   `load_tool` or `propose_pack` withheld with a guardrail; an MCP source whose declared tools do
 *   not hash to the digest the person approved withheld whole;
 * - **the wire name** (`wireName`): `[A-Za-z0-9_-]`, at most 64 characters, the source's id first,
 *   a deterministic suffix where characters were replaced or the name was cut, and a name that
 *   still collides refused;
 * - **the description** (`offerSources`): bounded by `toolDescriptionBytes` and framed as retrieved
 *   text with the run's marker, never instruction; so is every descriptive string inside the input
 *   schema (`describedSchema`, an allowlist): a `description` is bounded and framed, only the
 *   structural keywords are offered (every other keyword is not), a name over 64 bytes or with white
 *   space is dropped, and an `enum` or `const` holding a long string is dropped, each with a
 *   guardrail naming where, since the host still validates what it executes;
 * - **the schema budget** (`offerSources`): past `offeredSchemas` or `offeredSchemaBytes`, the
 *   largest sources become catalogues until the rest fit (each tool its name, one framed line, an
 *   open schema) and `load_tool` loads one schema at a time, which then rides in the execution state after the
 *   history, so the request's tools and the stable prefix never change during the run; the catalogue
 *   itself is held to `cataloguedTools` and `catalogueBytes`, and the tools past them are withheld
 *   with a guardrail;
 * - **each call** (`SourceGate.call`): the source's changed tool list, a catalogued tool not yet
 *   loaded, a catalogued tool's arguments against the schema it loaded, `sourceCallsPerTurn` (the
 *   slot is claimed at the check, before any await, and given back on a refusal), the effect class (a
 *   proposal is held for the review card and never run; an act asks the host's approval reader and
 *   is refused without it), then the host's own execute under `sourceCallMilliseconds`;
 * - **the report** (`SourceGate.report`): calls, refusals, proposals, bytes and time per source in
 *   the checkpoint's `sources`, and the source record on the tool's `tool_call` and `tool_result`.
 */
import { canonicalJSON } from './context/chat'
import { answer, contextDigest } from './context/ledger'
import { prefix, utf8Bytes, type ContextPolicy } from './context/policy'
import { PROPOSE_PACK_TOOL, shownSchema, toolTimeout, withDeadline } from './engines/contract'
import { AIFailureError, authoredFailure, toolInputFailure } from './failures'
import { frameBlock } from './framing'
import { argumentProblem } from './sourceArguments'
import { ASSISTANT_TOOLS } from './host'
import { REFUSED_RUNTIME_TOOLS } from './toolGate'
import type { ToolSchemas } from './capabilities'
import type { AssistantEvent, HostTool, McpToolResult } from './engine'

/** The kinds a host may declare; `runtime` is the Runtime's own row in the report. */
export const DECLARED_SOURCE_KINDS = ['gateway', 'mcp', 'custom'] as const
export type SourceKind = 'runtime' | (typeof DECLARED_SOURCE_KINDS)[number]
export const TRUST_CLASSES = ['first-party', 'connected', 'public', 'person'] as const
export type TrustClass = (typeof TRUST_CLASSES)[number]
export const EFFECT_CLASSES = ['read', 'propose', 'act'] as const
export type EffectClass = (typeof EFFECT_CLASSES)[number]
export type Approval = 'none' | 'required'

/**
 * What the host's approval reader is asked about (`HostTool.approve`): the call as the model made
 * it. `name` is the wire name the model called, `tool` the tool's own name at its source, `source`
 * its record, and `args` a copy the reader may change without touching the call.
 */
export interface ApprovalRequest {
  name: string
  tool: string
  source: SourceRecord
  args: Record<string, unknown>
}

/** The contract's `tool-offer.json#/$defs/sourceRecord`. */
export interface SourceRecord {
  kind: SourceKind
  id: string
  trust: TrustClass
  version?: string
  toolListDigest?: string
  /** The tool's own name at its source: set by the package on the offer and the events. */
  tool?: string
  /** On a tool_result: the receipt id the host's answer gave. */
  receipt?: string
}

/**
 * A source as the host declares it on a host tool: the record without `tool` and `receipt`, and,
 * for an MCP source, the host's check of the list the server serves now (`currentDigest`), asked
 * before each call; a digest other than the pinned one refuses the source until the host declares
 * the new one, which only the person's re-approval gives it.
 */
export interface ToolSource extends Omit<SourceRecord, 'tool' | 'receipt'> {
  currentDigest?: () => string | Promise<string>
}

/** The harness tool that loads a catalogued tool's schema; a reserved name. */
export const LOAD_TOOL = 'load_tool'
/** `load_tool`'s schema: one name. In the subset every family's wire carries. */
export const LOAD_TOOL_SCHEMA = {
  type: 'object',
  properties: { name: { type: 'string', description: "The catalogued tool's name, as the tools list shows it." } },
  required: ['name']
} as const
/** A catalogued tool's schema, open, so the request's tools never change when one is loaded. */
export const CATALOGUE_SCHEMA = { type: 'object' } as const
/** The most UTF-8 bytes of a catalogued tool's one line. */
export const CATALOGUE_LINE_BYTES = 160
/** A source's id: a letter, then letters, digits and hyphens, at most 24 characters. */
export const SOURCE_ID = /^[A-Za-z][A-Za-z0-9-]{0,23}$/
const DIGEST = /^sha256:[0-9a-f]{64}$/
const WIRE_CHARACTER = /^[A-Za-z0-9_-]$/
const WIRE_LIMIT = 64

/**
 * The names a tool with a source may not bear as its own (ADR-0006): the Runtime's tools, on the
 * ceiling and off it, reach the model only through the gate, and the harness's own names are the
 * harness's.
 */
export const HELD_NAMES: ReadonlySet<string> = new Set([
  ...ASSISTANT_TOOLS,
  ...Object.keys(REFUSED_RUNTIME_TOOLS),
  'list_context', 'read_context', 'read_window', 'search', 'update_plan', 'request_budget_increase',
  LOAD_TOOL,
  PROPOSE_PACK_TOOL
])

/** What the model is told of a proposed call: it was held for the person and not run. */
export const PROPOSED =
  'Proposed for the person’s review: this call was not run. The person accepts or rejects it after this response, ' +
  'and the host runs it only on acceptance. Do not repeat it; say in your answer what you proposed and why.'

/**
 * The wire name of one tool of one source (`tool-offer.json#/$defs/wireName`): the source's id, two
 * underscores and the tool's own name with every character outside `[A-Za-z0-9_-]` replaced by `_`;
 * where anything was replaced or the result passes 64 characters, its first 55, an underscore and
 * the first 8 hex digits of the SHA-256 of the id, a line feed and the own name. Stable: the same
 * source and name always give the same wire name, whatever else is declared.
 */
export async function wireName(source: string, tool: string): Promise<string> {
  const cleaned = [...tool].map((c) => (WIRE_CHARACTER.test(c) ? c : '_')).join('')
  const base = `${source}__${cleaned}`
  if (cleaned === tool && base.length <= WIRE_LIMIT) return base
  return `${base.slice(0, 55)}_${(await contextDigest(`${source}\n${tool}`)).slice(0, 8)}`
}

/** One tool of a source as its list's digest reads it. */
export interface ListedTool {
  name: string
  description?: string
  inputSchema?: unknown
}

/**
 * The digest of a source's tool list (`tool-offer.json#/$defs/toolListDigest`): `sha256:` and the
 * SHA-256 of the canonical JSON of its tools, each `{name, description, inputSchema}` (description
 * `''` where it has none), ordered by name in UTF-16 code units. A host computes it with this when
 * the person approves a server, and again for `currentDigest`.
 */
export async function toolListDigest(tools: readonly ListedTool[]): Promise<string> {
  const list = tools
    .map((tool) => ({ name: tool.name, description: tool.description ?? '', inputSchema: tool.inputSchema }))
    .sort((a, b) => (a.name < b.name ? -1 : a.name > b.name ? 1 : 0))
  return `sha256:${await contextDigest(canonicalJSON(list))}`
}

/** A tool with a source, as the boundary holds it after the declaration. */
export interface SourceEntry {
  wire: string
  /** The record as the offer and the events carry it, `tool` set. */
  record: SourceRecord
  effectClass: EffectClass
  approval: Approval
  /** The host's declaration, as it declared it. */
  declared: HostTool
  /** The description as the model may be shown it: bounded, unframed. */
  description: string
  /** Offered as a catalogue entry: its schema is loaded by `load_tool`. */
  catalogued: boolean
}

/** A declaration the package withheld, and why: the guardrail event it earns. */
export interface Withheld {
  tool: string
  detail: string
}

/** What the declaration made of the host's tools. */
export interface Declared {
  /** The host's tools, each tool with a source replaced by its offered form, the withheld left out. */
  hostTools: HostTool[]
  entries: Map<string, SourceEntry>
  /** Each declared source's record, once, in the order declared. */
  sources: SourceRecord[]
  withheld: Withheld[]
}

const object = (value: unknown): value is Record<string, unknown> =>
  typeof value === 'object' && value !== null && !Array.isArray(value)

/** Why a declaration cannot be read, or `''`. */
function declarationProblem(tool: HostTool): string {
  const source = tool.source as unknown
  if (!object(source)) return 'its source is not an object'
  const known = ['kind', 'id', 'trust', 'version', 'toolListDigest', 'currentDigest']
  const extra = Object.keys(source).filter((member) => !known.includes(member))
  if (extra.length > 0) return `its source carries ${JSON.stringify(extra[0]!.slice(0, 64))}, which a source record does not name`
  if (source.kind === 'runtime') return "its source is the Runtime, whose tools come through tools/list behind the gate and are never declared"
  if (!(DECLARED_SOURCE_KINDS as readonly unknown[]).includes(source.kind)) return 'its source kind is not gateway, mcp or custom'
  if (typeof source.id !== 'string' || !SOURCE_ID.test(source.id)) return "its source id is not a letter then up to 23 letters, digits and hyphens"
  if (!(TRUST_CLASSES as readonly unknown[]).includes(source.trust)) return 'its trust class is not first-party, connected, public or person'
  if (source.version !== undefined && (typeof source.version !== 'string' || source.version.length < 1 || source.version.length > 64)) return 'its source version is not 1 to 64 characters'
  if (source.toolListDigest !== undefined && (typeof source.toolListDigest !== 'string' || !DIGEST.test(source.toolListDigest))) return 'its tool list digest is not sha256: and 64 lower-case hex digits'
  if (source.version === undefined && source.toolListDigest === undefined) return 'its source names neither a version nor a tool list digest'
  if (source.kind === 'mcp' && source.toolListDigest === undefined) return 'its source is an MCP server whose tool list is not pinned by digest'
  if (source.currentDigest !== undefined && typeof source.currentDigest !== 'function') return "its source's currentDigest is not a function"
  if (typeof tool.name !== 'string' || tool.name.length < 1 || tool.name.length > 128) return 'its name is not 1 to 128 characters'
  if (typeof tool.description !== 'string') return 'its description is not text'
  if (!object(tool.inputSchema)) return 'its input schema is not a JSON object'
  if (tool.effect !== undefined) return 'it declares effect, which the package sets from effectClass for a tool with a source'
  if (tool.interaction !== undefined) return 'it declares an interaction, and a tool with a source is never a question or a delegation'
  if (tool.effectClass !== undefined && !(EFFECT_CLASSES as readonly unknown[]).includes(tool.effectClass)) return 'its effect class is not read, propose or act'
  if (tool.approval !== undefined && tool.approval !== 'none' && tool.approval !== 'required') return 'its approval is not none or required'
  if (tool.delegation !== undefined && tool.effectClass !== 'read') return 'it is offered to delegated children without being declared a read'
  if (tool.approve !== undefined && typeof tool.approve !== 'function') return 'its approval reader is not a function'
  return ''
}

/** The effect a tool of each class has on the offer: Plan mode and a child admit reads only. */
const EFFECT_OF: Record<EffectClass, HostTool['effect']> = { read: 'read', propose: 'proposal', act: undefined }
/** The approval each class has unless the host declares one. */
export const APPROVAL_DEFAULT: Record<EffectClass, Approval> = { read: 'none', propose: 'none', act: 'required' }

/**
 * Read the host's tools with a source, map each to its wire name, and withhold what may not be
 * offered (ADR-0006). A declaration the package cannot read ends the run before any request
 * (`configuration`); a held name, a wire name that still collides and an MCP source whose declared
 * tools are not the list the person approved are withheld, each with a guardrail. `served` are the
 * Runtime tools' names the session offers.
 */
export async function declareSources(hostTools: readonly HostTool[], served: readonly string[]): Promise<Declared> {
  const withheld: Withheld[] = []
  const sources = new Map<string, SourceRecord>()
  const bySource = new Map<string, HostTool[]>()
  const declared: { tool: HostTool; wire: string }[] = []
  for (const tool of hostTools) {
    if (tool.source === undefined) continue
    const problem = declarationProblem(tool)
    if (problem !== '') {
      throw authoredFailure('configuration', `The host tool ${JSON.stringify(String(tool.name).slice(0, 64))} has a source declaration this package cannot read: ${problem}. Nothing was sent.`, 'context')
    }
    const source = tool.source
    const record: SourceRecord = { kind: source.kind, id: source.id, trust: source.trust, ...(source.version === undefined ? {} : { version: source.version }), ...(source.toolListDigest === undefined ? {} : { toolListDigest: source.toolListDigest }) }
    const earlier = sources.get(source.id)
    if (earlier !== undefined && canonicalJSON(earlier) !== canonicalJSON(record)) {
      throw authoredFailure('configuration', `The source ${source.id} is declared twice with different records. Nothing was sent.`, 'context')
    }
    sources.set(source.id, record)
    bySource.set(source.id, [...(bySource.get(source.id) ?? []), tool])
    declared.push({ tool, wire: await wireName(source.id, tool.name) })
  }
  // The person approved the list, not each tool: an MCP source whose declared tools hash to another digest is withheld whole.
  const unapproved = new Set<string>()
  for (const [id, record] of sources) {
    if (record.kind !== 'mcp') continue
    const digest = await toolListDigest(bySource.get(id)!)
    if (digest !== record.toolListDigest) {
      unapproved.add(id)
      withheld.push({ tool: id, detail: `the tools ${id} declares are not the list the person approved (approved ${record.toolListDigest}, declared ${digest}); none of its tools is offered until the person approves the new list` })
    }
  }
  const taken = new Map<string, number>()
  const others = new Set([...served, ...hostTools.filter((tool) => tool.source === undefined).map((tool) => tool.name), ...HELD_NAMES])
  for (const { wire } of declared) taken.set(wire, (taken.get(wire) ?? 0) + 1)
  const entries = new Map<string, SourceEntry>()
  const offered = new Map<HostTool, HostTool>()
  for (const { tool, wire } of declared) {
    const id = tool.source!.id
    if (unapproved.has(id)) continue
    if (HELD_NAMES.has(tool.name)) {
      withheld.push({ tool: wire, detail: `${id} serves a tool named ${tool.name}, a name the Runtime or the harness keeps; it is not offered, and a Runtime's tools reach the model only through the gate` })
      continue
    }
    if (taken.get(wire)! > 1 || others.has(wire)) {
      withheld.push({ tool: wire, detail: `the wire name ${wire} of ${id}'s ${JSON.stringify(tool.name.slice(0, 64))} is also another offered tool's; neither is offered` })
      continue
    }
    const effectClass = tool.effectClass ?? 'act'
    const record: SourceRecord = { ...sources.get(id)!, tool: tool.name }
    const { effectClass: _class, approval: _approval, approve: _approve, source: _source, ...rest } = tool
    const shown: HostTool = { ...rest, name: wire, source: { ...record }, effectClass, approval: tool.approval ?? APPROVAL_DEFAULT[effectClass], ...(EFFECT_OF[effectClass] === undefined ? {} : { effect: EFFECT_OF[effectClass] }) }
    offered.set(tool, shown)
    entries.set(wire, { wire, record, effectClass, approval: shown.approval!, declared: tool, description: tool.description, catalogued: false })
  }
  return {
    hostTools: hostTools.flatMap((tool) => (tool.source === undefined ? [tool] : offered.has(tool) ? [offered.get(tool)!] : [])),
    entries,
    sources: [...sources.values()],
    withheld
  }
}

/** The bounds `offerSources` and the gate hold (budget.json). */
export type SourceBounds = Pick<ContextPolicy, 'toolDescriptionBytes' | 'offeredSchemas' | 'offeredSchemaBytes' | 'cataloguedTools' | 'catalogueBytes' | 'sourceResultBytes' | 'sourceCallsPerTurn' | 'sourceCallMilliseconds' | 'toolCallMilliseconds' | 'toolResultBytes'>

/** A description at most `bytes`, and a note saying where it was cut. */
export function boundedDescription(text: string, bytes: number): string {
  if (utf8Bytes(text) <= bytes) return text
  return `${prefix(text, bytes)} [cut at ${bytes} bytes]`
}

/** The source's text, framed: the opening line says whose it is and that it is data. */
export function framedDescription(entry: Pick<SourceEntry, 'wire' | 'record'>, text: string, marker: string): string {
  return frameBlock(`the description of tool ${JSON.stringify(entry.wire)} from source ${JSON.stringify(entry.record.id)}`, text, marker)
}

/** The words before a catalogued tool's framed line. */
export const CATALOGUED = `Catalogued tool: call ${LOAD_TOOL} with this name for its schema before calling it.`

/** The most UTF-8 bytes of a string an `enum` or a `const` of a source's schema may hold and still be offered. */
export const ENUM_MEMBER_BYTES = 64
/** The most UTF-8 bytes of a property name, a `required` entry or a definition's name that is offered. */
export const NAME_BYTES = 64
/** How deep a source's schema is read; a branch past it is offered empty. */
const SCHEMA_DEPTH = 16
/** The keywords whose value is a number: kept where it is one. */
const NUMBER_KEYWORDS = new Set(['minimum', 'maximum', 'exclusiveMinimum', 'exclusiveMaximum', 'multipleOf', 'minLength', 'maxLength', 'minItems', 'maxItems', 'minProperties', 'maxProperties'])
/** The keywords that map a name to a schema: the names that are short and plain stay, each schema is read. */
const SCHEMA_MAPS = new Set(['properties', '$defs', 'definitions'])
/** The keywords whose value is a list of schemas. */
const SCHEMA_LISTS = new Set(['oneOf', 'anyOf', 'allOf'])
/** The JSON Schema types, and the formats a source's schema may name: any other string is not offered. */
const TYPES = new Set(['string', 'number', 'integer', 'boolean', 'object', 'array', 'null'])
const FORMATS = new Set(['date', 'date-time', 'time', 'email', 'hostname', 'ipv4', 'ipv6', 'uri', 'uuid', 'int32', 'int64', 'float', 'double'])
/** A reference within the schema: `#` and at most eight short pointer steps. */
const REFERENCE = /^#(\/[A-Za-z0-9_$.~%-]{1,64}){0,8}$/

/** Whether a code point is white space or a control character, in a set both languages hold alike. */
function blank(point: number): boolean {
  return point <= 0x20 || (point >= 0x7f && point <= 0xa0) || point === 0x1680 || (point >= 0x2000 && point <= 0x200a) ||
    point === 0x2028 || point === 0x2029 || point === 0x202f || point === 0x205f || point === 0x3000 || point === 0xfeff
}

/** Whether a name may be offered: not empty, at most `NAME_BYTES` bytes, and no white space or line break. */
function plainName(name: string): boolean {
  return name !== '' && utf8Bytes(name) <= NAME_BYTES && ![...name].some((character) => blank(character.codePointAt(0)!))
}

/** Whether `value` may stay in an offered `enum` or `const`: a short, one-line string, or a short value. */
function plainValue(value: unknown): boolean {
  if (typeof value === 'string') return utf8Bytes(value) <= ENUM_MEMBER_BYTES && !/[\r\n]/.test(value)
  return utf8Bytes(JSON.stringify(value) ?? '') <= ENUM_MEMBER_BYTES
}

/** Something the offer left out of a source's schema that the model would need to know of: where, and what. */
export interface SchemaNote {
  path: string
  what: string
}

/**
 * A source's input schema as the model may be shown it (ADR-0006): the text the source wrote inside
 * it is as untrusted as its description, so only what the package knows to be structure is offered
 * (an allowlist, never a list of what is refused). Offered: `type` (the seven JSON types), `format`
 * (a short fixed set), `$ref` (a short pointer within the schema), the numeric bounds, `uniqueItems`,
 * `additionalProperties`, `items`, `oneOf`, `anyOf`, `allOf`, `properties`, `$defs` and `definitions`
 * (a name over `NAME_BYTES` bytes, or with white space or a line break, is dropped with its schema),
 * `required` (its entries the same), `enum` and `const` (dropped where a string is over
 * `ENUM_MEMBER_BYTES` bytes or has a line break, or where `enum` is no list) and `description`, which
 * is cut to `bytes` and, where `frame` is given, framed as retrieved text. Every other keyword, its
 * value whatever it is, is not offered: `title`, `$comment`, `default`, `examples`, `pattern`, the
 * `x-` extensions and the rest. A branch deeper than `SCHEMA_DEPTH` is offered empty. What is dropped
 * that the model would need (an `enum`, a `const`, a name) is said in `notes`.
 */
export function describedSchema(node: unknown, bytes: number, frame?: (text: string) => string, notes?: SchemaNote[]): unknown {
  const note = (path: string, what: string) => notes?.push({ path, what })
  const walk = (value: unknown, depth: number, path: string): unknown => {
    if (typeof value === 'boolean') return value
    if (!object(value) || depth >= SCHEMA_DEPTH) return {}
    const kept: [string, unknown][] = []
    for (const [key, member] of Object.entries(value)) {
      const at = `${path}/${key}`
      if (key === 'description') {
        if (typeof member === 'string') {
          const cut = boundedDescription(member, bytes)
          kept.push([key, frame === undefined ? cut : frame(cut)])
        }
      } else if (key === 'type') {
        const types = Array.isArray(member) ? member : [member]
        if (types.length > 0 && types.every((type) => typeof type === 'string' && TYPES.has(type))) kept.push([key, member])
      } else if (key === 'format') {
        if (typeof member === 'string' && FORMATS.has(member)) kept.push([key, member])
      } else if (key === '$ref') {
        if (typeof member === 'string' && REFERENCE.test(member)) kept.push([key, member])
      } else if (key === 'enum') {
        if (Array.isArray(member) && member.every(plainValue)) kept.push([key, member])
        else note(path, 'enum')
      } else if (key === 'const') {
        if (plainValue(member)) kept.push([key, member])
        else note(path, 'const')
      } else if (NUMBER_KEYWORDS.has(key)) {
        if (typeof member === 'number' && Number.isFinite(member)) kept.push([key, member])
      } else if (key === 'uniqueItems') {
        if (typeof member === 'boolean') kept.push([key, member])
      } else if (key === 'additionalProperties') {
        if (typeof member === 'boolean' || object(member)) kept.push([key, walk(member, depth + 1, at)])
      } else if (key === 'items') {
        if (Array.isArray(member)) kept.push([key, member.map((item, index) => walk(item, depth + 1, `${at}/${index}`))])
        else if (typeof member === 'boolean' || object(member)) kept.push([key, walk(member, depth + 1, at)])
      } else if (SCHEMA_LISTS.has(key)) {
        if (Array.isArray(member)) kept.push([key, member.map((item, index) => walk(item, depth + 1, `${at}/${index}`))])
      } else if (SCHEMA_MAPS.has(key)) {
        if (object(member)) {
          const named: [string, unknown][] = []
          for (const [name, schema] of Object.entries(member)) {
            if (plainName(name)) named.push([name, walk(schema, depth + 1, `${at}/${name}`)])
            else note(at, 'name')
          }
          kept.push([key, Object.fromEntries(named)])
        }
      } else if (key === 'required') {
        if (Array.isArray(member)) {
          const names = member.filter((name): name is string => typeof name === 'string' && plainName(name))
          if (names.length < member.length) note(path, 'required')
          kept.push([key, names])
        }
      }
      // Every other keyword is not offered.
    }
    return Object.fromEntries(kept)
  }
  return walk(node, 0, '')
}

/** The words a note of `describedSchema` earns as a guardrail: what was left out, where, and that the host still validates. */
function schemaNoteDetail(wire: string, note: SchemaNote): string {
  const where = note.path === '' ? 'the schema' : note.path
  const what = note.what === 'name' ? `a property or definition name there is over ${NAME_BYTES} bytes or holds white space, and it is not offered with its schema`
    : note.what === 'required' ? `a required entry there is over ${NAME_BYTES} bytes or holds white space, and it is not offered`
    : `its ${note.what} holds a string over ${ENUM_MEMBER_BYTES} bytes or a line break, or is no list, and it is not offered`
  return `in the input schema of ${wire}, at ${where}: ${what}; the host still validates the arguments it executes`
}

/**
 * The offer of the tools with a source (ADR-0006): each description, and each descriptive string of
 * its input schema, bounded and framed with the run's marker. Where they pass `offeredSchemas` or
 * `offeredSchemaBytes`, sources are made catalogues, the largest first (by its tools offered, then
 * by their bytes, then by id), until the rest fit: a catalogued source's tools are offered as
 * catalogue entries (`CATALOGUED`, the first line framed, `CATALOGUE_SCHEMA`), at most
 * `cataloguedTools` of them and `catalogueBytes` bytes in all; the tools past either are withheld,
 * out of `entries` too, and said in `withheld` (one note for each source). `withheld` also holds a
 * note for each thing `describedSchema` left out of a tool's schema that the model would need. Whether
 * any source became a catalogue is returned; the boundary then offers `load_tool`.
 */
export function offerSources(hostTools: readonly HostTool[], entries: Map<string, SourceEntry>, bounds: SourceBounds, marker: string): { hostTools: HostTool[]; catalogue: boolean; withheld: Withheld[] } {
  const framed = new Map<string, string>()
  const schemas = new Map<string, unknown>()
  const schemaNotes: Withheld[] = []
  const sizes = new Map<string, { count: number; bytes: number }>()
  for (const tool of hostTools) {
    const entry = entries.get(tool.name)
    if (entry === undefined) continue
    entry.description = boundedDescription(entry.declared.description, bounds.toolDescriptionBytes)
    const text = framedDescription(entry, entry.description, marker)
    framed.set(tool.name, text)
    const notes: SchemaNote[] = []
    const schema = describedSchema(tool.inputSchema, bounds.toolDescriptionBytes, (cut) => frameBlock(`a description in the input schema of tool ${JSON.stringify(entry.wire)} from source ${JSON.stringify(entry.record.id)}`, cut, marker), notes)
    for (const held of notes) schemaNotes.push({ tool: entry.wire, detail: schemaNoteDetail(entry.wire, held) })
    schemas.set(tool.name, schema)
    const size = sizes.get(entry.record.id) ?? { count: 0, bytes: 0 }
    size.count++
    size.bytes += utf8Bytes(JSON.stringify({ name: tool.name, description: text, inputSchema: schema }))
    sizes.set(entry.record.id, size)
  }
  let count = [...sizes.values()].reduce((sum, size) => sum + size.count, 0)
  let bytes = [...sizes.values()].reduce((sum, size) => sum + size.bytes, 0)
  const largest = [...sizes].sort(([a, x], [b, y]) => y.count - x.count || y.bytes - x.bytes || (a < b ? -1 : a > b ? 1 : 0))
  const catalogued = new Set<string>()
  for (const [id, size] of largest) {
    if (count <= bounds.offeredSchemas && bytes <= bounds.offeredSchemaBytes) break
    catalogued.add(id)
    count -= size.count
    bytes -= size.bytes
  }
  let kept = 0
  let keptBytes = 0
  const cut = new Map<string, number>()
  const offered: HostTool[] = []
  for (const tool of hostTools) {
    const entry = entries.get(tool.name)
    if (entry === undefined) { offered.push(tool); continue }
    if (!catalogued.has(entry.record.id)) { offered.push({ ...tool, description: framed.get(tool.name)!, inputSchema: schemas.get(tool.name) }); continue }
    const line = prefix(entry.description.split('\n')[0]!.trim(), CATALOGUE_LINE_BYTES)
    const shown = { ...tool, description: `${CATALOGUED}\n${framedDescription(entry, line, marker)}`, inputSchema: CATALOGUE_SCHEMA }
    const size = utf8Bytes(JSON.stringify({ name: shown.name, description: shown.description, inputSchema: shown.inputSchema }))
    if (kept + 1 > bounds.cataloguedTools || keptBytes + size > bounds.catalogueBytes) {
      entries.delete(tool.name)
      cut.set(entry.record.id, (cut.get(entry.record.id) ?? 0) + 1)
      continue
    }
    kept++
    keptBytes += size
    entry.catalogued = true
    offered.push(shown)
  }
  const withheld = [...cut].map(([id, tools]) => ({ tool: id, detail: `the catalogue holds at most ${bounds.cataloguedTools} tools and ${bounds.catalogueBytes} bytes (cataloguedTools, catalogueBytes), so ${tools} of ${id}'s tools ${tools === 1 ? 'is' : 'are'} not offered; ${kept} are offered across the catalogue` }))
  return { catalogue: kept > 0, hostTools: offered, withheld: [...schemaNotes, ...withheld] }
}

export type { SourceFigures } from './execution/state'
import type { SourceFigures } from './execution/state'

/** What a tool's result event carries beside the record. */
interface Mark {
  receipt?: string
  proposed?: true
}

/** The words of a refusal that also earns a guardrail event. */
export type Refuse = (tool: string, detail: string) => Error

/**
 * The boundary's hold on the tools with a source for one run (ADR-0006): each call's checks in
 * order, the run report, `load_tool` and the loaded schemas, and the record on the events.
 *
 * A delegated child's gate has its parent's as `parent` (`ExecutionLink.sources`): the report's
 * rows, and so `sourceCallsPerTurn`, and the sources found changed, are the parent's, as the turn's
 * test-call cap is shared; a child's own gate keeps only what is its own, the schemas it loaded
 * and the marks of its calls' events.
 */
export class SourceGate {
  private readonly changed = new Set<string>()
  private readonly loaded = new Map<string, unknown>()
  private loadedBytes = 0
  /** The marks of the calls that have a call id, by it. */
  private readonly marks = new Map<string, Mark>()
  /** The marks of calls with none (a run whose events carry no call id), by wire name, in the order they completed. */
  private readonly queued = new Map<string, Mark[]>()
  constructor(
    readonly entries: ReadonlyMap<string, SourceEntry>,
    private readonly sources: readonly SourceRecord[],
    private readonly bounds: SourceBounds,
    private readonly marker: string,
    private readonly refuse: Refuse,
    /** The run report's rows: the execution state's, so a resume carries them. */
    private readonly rows: () => SourceFigures[],
    /** How the model's family is shown a loaded schema. */
    private readonly schemas: ToolSchemas = 'json-schema',
    /** The gate of the run that delegated this one, whose rows and changed sources it shares. */
    private readonly parent?: SourceGate
  ) {
    const held = this.tally()
    const want: SourceFigures[] = [{ source: 'runtime', kind: 'runtime', calls: 0, refused: 0, proposed: 0, bytes: 0, milliseconds: 0 }, ...sources.map((record) => ({ source: record.id, kind: record.kind, calls: 0, refused: 0, proposed: 0, bytes: 0, milliseconds: 0 }))]
    for (const row of want) if (!held.some((kept) => kept.source === row.source)) held.push(row)
  }

  /** The rows this run reports into: its own, or its delegating run's, which is the turn's. */
  private tally(): SourceFigures[] {
    return this.parent ? this.parent.tally() : this.rows()
  }

  /** The sources found changed, which a delegating run and its children find together. */
  private get alteredSources(): Set<string> {
    return this.parent ? this.parent.alteredSources : this.changed
  }

  /** Whether this run declares any tool with a source. */
  get declared(): boolean {
    return this.sources.length > 0
  }

  private row(source: string): SourceFigures {
    return this.tally().find((row) => row.source === source)!
  }

  private mark(wire: string, callId: string | undefined, mark: Mark): void {
    if (callId !== undefined) this.marks.set(callId, mark)
    else this.queued.set(wire, [...(this.queued.get(wire) ?? []), mark])
  }

  /** A refusal of one call: counted, its event marked, and thrown by the caller. */
  private refused(entry: SourceEntry, error: Error, callId: string | undefined): Error {
    this.row(entry.record.id).refused++
    this.mark(entry.wire, callId, {})
    return error
  }

  /** The largest result of `name` the model is shown whole, where it is a tool with a source. */
  resultLimit(name: string): number | undefined {
    return this.entries.has(name) ? Math.min(this.bounds.sourceResultBytes, this.bounds.toolResultBytes) : undefined
  }

  /**
   * One call of a tool with a source, after the phase admitted it: refused, with the reason the
   * model reads, where the source's tool list changed, the tool is catalogued and not loaded, a
   * catalogued tool's arguments do not match the schema it loaded, the source has made
   * `sourceCallsPerTurn` calls, or the call needs an approval the host's reader did not give; held
   * for the review card where it is a proposal; otherwise the host's execute under the source's
   * deadline, counted in the report. The call's slot under `sourceCallsPerTurn` is claimed at the
   * check, before the first await, so calls made together cannot pass the bound; a refusal after
   * it gives the slot back. `callId` is the id of the call's events, which its marks are kept by.
   */
  async call(tool: HostTool, args: Record<string, unknown>, signal: AbortSignal, run: (args: Record<string, unknown>, signal: AbortSignal) => Promise<McpToolResult>, callId?: string): Promise<McpToolResult> {
    const entry = this.entries.get(tool.name)
    if (entry === undefined) return run(args, signal)
    const id = entry.record.id
    const row = this.row(id)
    if (this.alteredSources.has(id)) throw this.refused(entry, this.refuse(tool.name, this.changedDetail(entry)), callId)
    if (entry.catalogued) {
      if (!this.loaded.has(tool.name)) {
        throw this.refused(entry, toolInputFailure(`${tool.name} is catalogued: call ${LOAD_TOOL} with its name first and call it with the schema that loads. This call was not run.`), callId)
      }
      const problem = argumentProblem(this.loaded.get(tool.name), args)
      if (problem !== undefined) {
        throw this.refused(entry, toolInputFailure(`${tool.name}'s arguments do not match the schema ${LOAD_TOOL} loaded: ${problem}. This call was not run.`), callId)
      }
    }
    if (row.calls + row.proposed >= this.bounds.sourceCallsPerTurn) {
      const limit = this.bounds.sourceCallsPerTurn
      throw this.refused(entry, new AIFailureError({ version: 1, code: 'tool-failed', source: 'tool', outcome: 'not-started', detail: `This turn has called ${id}'s tools ${limit} time${limit === 1 ? '' : 's'}, the most one turn may (sourceCallsPerTurn); this call was not run. Use the results already obtained, or say what remains.` }), callId)
    }
    // The slot, claimed with no await between the check and here; a proposal's is the proposed count.
    const proposal = entry.effectClass === 'propose'
    if (proposal) row.proposed++
    else row.calls++
    const giveBack = () => { if (proposal) row.proposed--; else row.calls-- }
    const check = entry.declared.source?.currentDigest
    if (check !== undefined && entry.record.kind === 'mcp') {
      let now: unknown
      try { now = await check() } catch { now = undefined }
      if (now !== entry.record.toolListDigest) {
        this.alteredSources.add(id)
        giveBack()
        throw this.refused(entry, this.refuse(tool.name, this.changedDetail(entry)), callId)
      }
    }
    if (proposal) {
      this.mark(tool.name, callId, { proposed: true })
      return { content: [{ type: 'text', text: PROPOSED }] }
    }
    if (entry.approval === 'required') {
      let approved = false
      try { approved = (await entry.declared.approve?.({ name: tool.name, tool: entry.record.tool!, source: { ...entry.record }, args: structuredClone(args) }, signal)) === true } catch { approved = false }
      if (signal.aborted) {
        giveBack()
        this.mark(tool.name, callId, {})
        signal.throwIfAborted()
      }
      if (!approved) {
        giveBack()
        throw this.refused(entry, this.refuse(tool.name, `${tool.name} acts on ${id} and needs the person's approval for each call, which this call did not get; it was not run and nothing was changed`), callId)
      }
    }
    const limit = Math.min(this.bounds.sourceCallMilliseconds, this.bounds.toolCallMilliseconds)
    const started = Date.now()
    let result: McpToolResult
    try {
      result = await withDeadline((bounded) => run(args, bounded), signal, limit, () => toolTimeout(limit))
    } catch (error) {
      row.milliseconds += Date.now() - started
      this.mark(tool.name, callId, {})
      throw error
    }
    row.milliseconds += Date.now() - started
    const { receipt, ...shown } = result as McpToolResult & { receipt?: unknown }
    row.bytes += utf8Bytes(JSON.stringify(shown) ?? '')
    this.mark(tool.name, callId, typeof receipt === 'string' && receipt.length >= 1 && receipt.length <= 128 ? { receipt } : {})
    return shown
  }

  /** One Runtime call, in the Runtime's row. */
  async runtime<T>(call: () => Promise<T>): Promise<T> {
    if (!this.declared) return call()
    const row = this.row('runtime')
    const started = Date.now()
    try {
      const result = await call()
      row.calls++
      row.bytes += utf8Bytes(JSON.stringify(result) ?? '')
      return result
    } catch (error) {
      if ((error as Error)?.name === 'GateViolation') row.refused++
      else row.calls++
      throw error
    } finally {
      row.milliseconds += Date.now() - started
    }
  }

  private changedDetail(entry: SourceEntry): string {
    return `the tool list ${entry.record.id} serves is no longer the one the person approved (${entry.record.toolListDigest}); its tools are refused until the person approves the new list, and this call was not run`
  }

  /** `load_tool`, offered where the schema budget tripped: one catalogued tool's schema per call. */
  loadTool(): HostTool {
    return {
      name: LOAD_TOOL,
      effect: 'read',
      description: `Load the input schema of one catalogued tool before calling it. Pass its name as the tools list shows it; only a catalogued tool's schema loads. A loaded schema stays in the execution state for the rest of this response.`,
      inputSchema: LOAD_TOOL_SCHEMA,
      execute: async (args) => {
        const name = args.name
        const entry = typeof name === 'string' ? this.entries.get(name) : undefined
        if (entry === undefined || !entry.catalogued) {
          throw toolInputFailure(`${LOAD_TOOL} loads the schema of a catalogued tool, and ${JSON.stringify(String(name).slice(0, 64))} is not one; nothing was loaded.`)
        }
        // The loaded schema rides inside a block framed as a whole, so its descriptions are bounded and not framed again.
        const schema = shownSchema(this.schemas, { name: entry.wire, inputSchema: describedSchema(entry.declared.inputSchema, this.bounds.toolDescriptionBytes) })
        if (!this.loaded.has(entry.wire)) {
          const bytes = utf8Bytes(JSON.stringify(schema))
          if (this.loadedBytes + bytes > this.bounds.offeredSchemaBytes) {
            throw new AIFailureError({ version: 1, code: 'tool-failed', source: 'tool', outcome: 'not-started', detail: `Loading ${entry.wire} would pass the ${this.bounds.offeredSchemaBytes} bytes of schemas one response may load (offeredSchemaBytes); nothing was loaded. Use the tools already loaded.` })
          }
          this.loaded.set(entry.wire, schema)
          this.loadedBytes += bytes
        }
        return answer({ name: entry.wire, source: entry.record, description: entry.description, inputSchema: schema })
      }
    }
  }

  /** The loaded schemas, as one data block for the execution state, or `''`. */
  loadedSection(): string {
    if (this.loaded.size === 0) return ''
    return frameBlock(`the schemas ${LOAD_TOOL} loaded`, JSON.stringify([...this.loaded].map(([name, inputSchema]) => ({ name, inputSchema }))), this.marker)
  }

  /** One event with the record its tool's source gives it: a tool_call's, a tool_result's with its receipt or its proposal. */
  decorate(event: AssistantEvent): AssistantEvent {
    if (event.type !== 'tool_call' && event.type !== 'tool_result') return event
    const entry = this.entries.get(event.name)
    if (entry === undefined) return event
    if (event.type === 'tool_call') return { ...event, source: { ...entry.record } }
    const mark = this.take(event.name, event.callId) ?? {}
    return { ...event, source: { ...entry.record, ...(mark.receipt === undefined ? {} : { receipt: mark.receipt }) }, ...(mark.proposed ? { proposed: true as const } : {}) }
  }

  /** The mark of one result: its call's, by call id, or the next of the tool's where the event has none. */
  private take(wire: string, callId: string | undefined): Mark | undefined {
    if (callId === undefined) return this.queued.get(wire)?.shift()
    const mark = this.marks.get(callId)
    this.marks.delete(callId)
    return mark
  }
}
