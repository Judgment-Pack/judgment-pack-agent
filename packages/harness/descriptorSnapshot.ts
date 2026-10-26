/** Gateway descriptor snapshots, pinned at their complete canonical UTF-8 bytes (agent#168).
 * This canonical form is intentionally separate from Agent record canonicalization.
 * Both readers use Unicode 15.0.0 ranges from scripts/descriptor-graphic-ranges.go,
 * matching Gateway commit 17168fc11cac3a9f4947564dfb65b2f239827681.
 */
import graphicRanges from './descriptorGraphicRanges.json'
import {taxonomyProblem, type ToolTaxonomy} from './toolTaxonomy'

export const DESCRIPTOR_SNAPSHOT_BYTES = 320 << 10
export interface DescriptorTool { description?: string; inputSchemaText?: string; taxonomy?: ToolTaxonomy; serverAnnotations?: Record<string, boolean> }
export interface DescriptorSnapshot {
  snapshotVersion?: '2'
  operation?: 'live' | 'write'
  binding: string
  capturedAt: string
  endpoint?: string
  platform: string
  policy: 1
  server?: { name: string; version: string }
  tools: Record<string, DescriptorTool>
}

const object = (v: unknown): v is Record<string, unknown> => typeof v === 'object' && v !== null && !Array.isArray(v)
function fail(detail: string): never { throw new Error(`descriptor snapshot: ${detail}`) }
const exact = (v: Record<string, unknown>, required: string[], optional: string[] = []): void => {
  if (required.some(k => !Object.hasOwn(v, k)) || Object.keys(v).some(k => !required.includes(k) && !optional.includes(k))) fail('missing or unknown member')
}
const scalarString = (v: string): boolean => {
  for (let i = 0; i < v.length; i++) {
    const c = v.charCodeAt(i)
    if (c >= 0xd800 && c <= 0xdbff) { const next = v.charCodeAt(++i); if (!(next >= 0xdc00 && next <= 0xdfff)) return false }
    else if (c >= 0xdc00 && c <= 0xdfff) return false
  }
  return true
}

/** Strict raw JSON; schema texts use JSON numbers, snapshots use the Gateway integer domain. */
export function readDescriptorJSON(text: string, integers = false): unknown {
  let at = 0
  const ws = () => { while (/[\t\n\r ]/.test(text[at] ?? '\0')) at++ }
  const string = (): string => {
    const start = at++
    while (at < text.length) {
      const c = text[at++]
      if (c === '\\') at++
      else if (c === '"') {
        let v: unknown
        try { v = JSON.parse(text.slice(start, at)) } catch { return fail('invalid string') }
        if (typeof v !== 'string' || !scalarString(v)) fail('lone surrogate')
        return v
      }
    }
    return fail('unterminated string')
  }
  const value = (depth: number): unknown => {
    ws()
    const c = text[at]
    if (c === '"') return string()
    if (c === '{' || c === '[') {
      if (depth > 128) fail('nesting exceeds the snapshot shape')
      at++; ws()
      if (c === '[') {
        const list: unknown[] = []
        if (text[at] === ']') { at++; return list }
        while (true) { list.push(value(depth + 1)); ws(); const end = text[at++]; if (end === ']') return list; if (end !== ',') fail('invalid array') }
      }
      const result: Record<string, unknown> = Object.create(null)
      if (text[at] === '}') { at++; return result }
      while (true) {
        ws(); if (text[at] !== '"') fail('invalid member name')
        const key = string()
        if (Object.hasOwn(result, key)) fail('duplicate member')
        ws(); if (text[at++] !== ':') fail('missing colon')
        result[key] = value(depth + 1); ws()
        const end = text[at++]; if (end === '}') return result; if (end !== ',') fail('invalid object')
      }
    }
    for (const [literal, v] of [['true', true], ['false', false], ['null', null]] as const) {
      if (text.startsWith(literal, at)) { at += literal.length; return v }
    }
    const match = /^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?/.exec(text.slice(at))
    if (!match) return fail('invalid value')
    at += match[0].length
    const number = Number(match[0])
    if (!Number.isFinite(number) || integers && (!/^-?(?:0|[1-9][0-9]*)$/.test(match[0]) || !Number.isSafeInteger(number))) fail('number outside the canonical integer domain')
    return number
  }
  const result = value(0); ws()
  if (at !== text.length) fail('trailing content')
  return result
}

const codePointOrder = (a: string, b: string): number => {
  const left = [...a], right = [...b]
  for (let i = 0; i < Math.min(left.length, right.length); i++) {
    const difference = left[i]!.codePointAt(0)! - right[i]!.codePointAt(0)!
    if (difference) return difference
  }
  return left.length - right.length
}
const canonical = (v: unknown): string => {
  if (Array.isArray(v)) return `[${v.map(canonical).join(',')}]`
  if (object(v)) return `{${Object.keys(v).sort(codePointOrder).map(k => `${JSON.stringify(k)}:${canonical(v[k])}`).join(',')}}`
  return JSON.stringify(v)
}
const graphic = (c: number): boolean => {
  let low = 0, high = graphicRanges.length - 1
  while (low <= high) {
    const middle = (low + high) >>> 1, range = graphicRanges[middle]!
    if (c < range[0]!) high = middle - 1
    else if (c > range[1]!) low = middle + 1
    else return true
  }
  return false
}
const platform = (s: unknown): s is string => typeof s === 'string' && s.length > 0 && !/[\/=\0]/.test(s) && !/^\s|\s$/u.test(s) && [...s].every(c => graphic(c.codePointAt(0)!))
const binding = (s: unknown): boolean => typeof s === 'string' && /^(?!\.{1,2}@)[^/\\@]+@sha256:[0-9a-f]{64}$/.test(s)

function endpoint(s: unknown): boolean {
  if (typeof s !== 'string' || s.length > 2048 || !/^[\x21-\x7e]+$/.test(s) || /[?#]/.test(s)) return false
  const match = /^(https?):\/\/([^/]+)(?:\/(.*))?$/.exec(s)
  if (!match) return false
  const authority = match[2]!, path = match[3] ?? ''
  if (/%(?![0-9a-fA-F]{2})/.test(path)) return false
  const hostPort = /^(\[[0-9a-fA-F:.]+\]|[A-Za-z0-9._-]+)(?::([1-9][0-9]*))?$/.exec(authority)
  if (!hostPort || hostPort[2] !== undefined && Number(hostPort[2]) > 65535) return false
  const host = hostPort[1]!
  if (host.startsWith('[')) {
    if (host.includes('.')) {
      const ipv4 = host.slice(host.lastIndexOf(':') + 1, -1).split('.')
      if (ipv4.length !== 4 || ipv4.some(o => !/^(?:0|[1-9][0-9]{0,2})$/.test(o) || Number(o) > 255)) return false
    }
    try { if (new URL(`https://${host}`).hostname === '') return false } catch { return false }
  }
  if (match[1] === 'https') return true
  if (host === '[::1]') return true
  const octets = host.split('.')
  return octets.length === 4 && octets[0] === '127' && octets.every(o => /^(?:0|[1-9][0-9]{0,2})$/.test(o) && Number(o) <= 255)
}

/** Read Gateway's snapshot shape without interpreting or rewriting inputSchemaText. */
export function readDescriptorSnapshot(bytes: Uint8Array): DescriptorSnapshot {
  if (!(bytes instanceof Uint8Array) || bytes.byteLength > DESCRIPTOR_SNAPSHOT_BYTES) fail('invalid bytes or over 327680 bytes')
  let text: string
  try { text = new TextDecoder('utf-8', { fatal: true, ignoreBOM: true }).decode(bytes) } catch { return fail('invalid UTF-8') }
  const v = readDescriptorJSON(text, true)
  if (canonical(v) !== text) fail('not in canonical form')
  if (!object(v)) return fail('not an object')
  const v2 = Object.hasOwn(v, 'snapshotVersion')
  if (v2 && (v.snapshotVersion !== '2' || !['live','write'].includes(String(v.operation)))) fail('unsupported snapshot version or operation')
  exact(v, ['binding', 'capturedAt', 'platform', 'policy', 'tools', ...(v2 ? ['snapshotVersion','operation','server'] : [])], ['endpoint', ...(v2 ? [] : ['server'])])
  if (!platform(v.platform)) fail('platform is not a platform name')
  if (!binding(v.binding)) fail('binding is not name@sha256:<64 hex>')
  if (Object.hasOwn(v, 'endpoint') && !endpoint(v.endpoint)) fail('invalid remote endpoint')
  if (typeof v.capturedAt !== 'string' || !/^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$/.test(v.capturedAt) || !Number.isFinite(Date.parse(v.capturedAt)) || new Date(v.capturedAt).toISOString().slice(0, 19) + 'Z' !== v.capturedAt) fail('capturedAt is not UTC to the second')
  if (v.policy !== 1) fail('display policy is not 1')
  if (Object.hasOwn(v, 'server')) {
    if (!object(v.server)) fail('server is not an object')
    exact(v.server as Record<string, unknown>, ['name', 'version'])
    if (typeof v.server.name !== 'string' || typeof v.server.version !== 'string') fail('invalid server identity')
  }
  if (!object(v.tools)) fail('tools is not an object')
  for (const entry of Object.values(v.tools)) {
    if (!object(entry) || Object.keys(entry).length === 0) fail('tool has no captured candidates')
    exact(entry, v2 ? ['taxonomy'] : [], ['description', 'inputSchemaText', ...(v2 ? ['serverAnnotations'] : [])])
    for (const key of ['description','inputSchemaText']) if (Object.hasOwn(entry,key) && typeof entry[key] !== 'string') fail('tool candidate is not a string')
    if (v2) {
      if (taxonomyProblem(entry.taxonomy) || !['read-external','propose','act'].includes((entry.taxonomy as ToolTaxonomy).effect)) fail('missing or invalid owner taxonomy')
      if (Object.hasOwn(entry,'serverAnnotations')) {
        if (!object(entry.serverAnnotations) || Object.keys(entry.serverAnnotations).some(k=>!['readOnlyHint','destructiveHint','idempotentHint','openWorldHint'].includes(k)) || Object.values(entry.serverAnnotations).some(v=>typeof v!=='boolean')) fail('invalid server constraints')
        if ((entry.taxonomy as ToolTaxonomy).effect !== 'act' && (entry.serverAnnotations.destructiveHint === true || entry.serverAnnotations.readOnlyHint === false)) fail('server constraints contradict owner taxonomy')
      }
    }
  }
  return v as unknown as DescriptorSnapshot
}

/** The complete, validated, original snapshot bytes are the pin. */
export async function descriptorSnapshotDigest(bytes: Uint8Array): Promise<string> {
  readDescriptorSnapshot(bytes)
  const digest = await crypto.subtle.digest('SHA-256', Uint8Array.from(bytes))
  return `sha256:${Array.from(new Uint8Array(digest), b => b.toString(16).padStart(2, '0')).join('')}`
}

/** Independently approved engine11/bindingV2 context; never copied from the snapshot being checked. */
export interface DescriptorApproval {
  platform: string
  binding: string
  operation: 'live' | 'write'
  endpoint?: string
  toolTaxonomies: Record<string, ToolTaxonomy>
}
export function descriptorApprovalProblem(snapshot: DescriptorSnapshot, approval: DescriptorApproval | undefined, tool?: string, owner?: ToolTaxonomy): string {
  if (!object(approval) || Object.keys(approval).some(k=>!['platform','binding','operation','endpoint','toolTaxonomies'].includes(k)) || !platform(approval.platform) || !binding(approval.binding) || !['live','write'].includes(String(approval.operation)) || !object(approval.toolTaxonomies)) return 'missing independently approved V2 descriptor context'
  if (snapshot.snapshotVersion !== '2' || snapshot.platform !== approval.platform || snapshot.binding !== approval.binding || snapshot.operation !== approval.operation || snapshot.endpoint !== approval.endpoint) return 'descriptor operation or source differs from approval'
  const names = Object.keys(approval.toolTaxonomies)
  if (!names.length || names.length !== Object.keys(snapshot.tools).length) return 'descriptor owner projection differs from approval'
  for (const name of names) {
    const t=approval.toolTaxonomies[name]!, entry=snapshot.tools[name]
    if (!name || taxonomyProblem(t) || !['read-external','propose','act'].includes(t.effect) || entry === undefined || canonical(entry.taxonomy) !== canonical(t)) return 'descriptor owner projection differs from approval'
  }
  if (tool !== undefined && (snapshot.tools[tool] === undefined || owner === undefined || canonical(snapshot.tools[tool]!.taxonomy) !== canonical(owner))) return 'selected tool taxonomy differs from approval'
  return ''
}
