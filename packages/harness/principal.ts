/**
 * Agent principals and delegated authority (agent #260, ADR-0029; spec RFC 0030, a Draft).
 *
 * A delegated run carries the chain of delegation statements its issuing host supplied when it
 * started the run, and the package refuses, before dispatch and with a named reason, every call of a
 * tool with a source that the chain does not admit. This module is the part that depends only on the
 * bytes, the pinned keys and the host's answers:
 *
 * - **the statement** (`verifyChain`): RFC 0030's steps 1 to 7, in order, stopping at the first
 *   failure, exactly as the reference gateway's SPEC.md §11.1 reads them: `malformed`,
 *   `unsupported-version`, `malformed`, `key-mismatch`, `signature-mismatch`, `chain-broken`,
 *   `widened`. The canonical form is the gateway's `canon` (member names by code point, integers
 *   only); a signature is over `judgment-pack/delegation/1:` and `canon` of the link without its
 *   signature, under a key pinned for the link's host with the link's algorithm: Ed25519 where the
 *   link holds no `alg`, held to the gateway's §8.4 key rule, and ES256 where it holds
 *   `alg: "ES256"`, held to the gateway's §1.5 rules, low S included (RFC 0030 §2, *Signing with a
 *   P-256 key*). A chain's root may bind it to one scope identifier (`scope.scopeId`), which every
 *   link repeats (§3, clause 9);
 * - **the run's principal** (`openPrincipal`): the chain, its leaf's software against the software
 *   the host states the run runs (`software-mismatch`), and the record member every delegated
 *   record carries (`principal`: actor, subject, the leaf by id and digest, the chain's digests);
 * - **admission** (`admitCall`): immediately before a call is dispatched, the request check of a
 *   bound chain (`scope-id-mismatch`: a gateway source fixed to another scope identifier, or none),
 *   then, at the host's time, steps 8 (`not-yet-valid`, `expired`), the package's own part of step 11 (`out-of-scope` with reason
 *   `source`, `tool`, `effect` or `limit-unknown`), and the host admission port's answer for the
 *   issuing host's state (inventory, issuance, revocation, `calls` counters and the subject's
 *   standing), read afresh for every admission: no package-side cache holds an answer. A port that
 *   gives no valid answer in time refuses the call (`admission-unknown`).
 *
 * The model never chooses, sees the bytes of, or changes the chain: it reaches the package only
 * through the host's `ContextInput.principal`.
 */
import {readDescriptorJSON} from './descriptorSnapshot'
import {authoredFailure} from './failures'
import {sha256} from './records'
import graphicRanges from './descriptorGraphicRanges.json'

export const DELEGATION_CONTEXT = 'judgment-pack/delegation/1:'
/** RFC 0030 open questions 3 and 21, as the reference gateway enforces them. */
export const DELEGATION_BOUNDS = {depth: 4, lifetimeMilliseconds: 24 * 60 * 60 * 1000, statementBytes: 65536, entries: 256, tools: 256, limits: 16} as const
/** How long the host's admission port may take before the call is refused as admission-unknown. */
export const ADMISSION_MILLISECONDS = 5000

export const EFFECTS = ['pure', 'read', 'read-external', 'propose', 'act'] as const
export type DelegationEffect = typeof EFFECTS[number]
/** The source an acting Runtime call is admitted under, as the source record it carries names itself. */
export const RUNTIME_SOURCE = 'runtime'
/** The source a host tool with no source names to the issuing host. A scope never matches it; its read is never counted, and asks only whether the chain stands. */
export const LOCAL_SOURCE = 'local'
export const SCOPE_REASONS = ['source', 'tool', 'effect', 'limit-unknown', 'limit'] as const
export type ScopeReason = typeof SCOPE_REASONS[number]
export type RefusalName = 'malformed' | 'unsupported-version' | 'key-mismatch' | 'signature-mismatch' | 'chain-broken' | 'widened' | 'not-yet-valid' | 'expired' | 'agent-unregistered' | 'software-mismatch' | 'not-issued' | 'revoked' | 'out-of-scope' | 'subject-mismatch' | 'subject-unauthorized' | 'admission-unknown' | 'principal-changed' | 'delegation-not-permitted' | 'not-parent-actor' | 'scope-id-mismatch' | 'tool-version-refused' | 'skill-refused' | 'read-version-unsupported' | 'read-malformed'
/** A named refusal (`run-principal.json#/$defs/refusal`); `reason` is present exactly for out-of-scope. */
export interface PrincipalRefusal {name: RefusalName; reason?: ScopeReason}

export interface PrincipalReference {kind: 'person' | 'service'; issuer: string; subject: string}
export type Software = {package: string; version: string} | {harness: string}
export interface Audience {kind: 'gateway' | 'agent-host'; name: string}
/** The record member every delegated record carries (`run-principal.json`). */
export interface PrincipalRecord {
  actor: {agent: string; host: string; software: Software}
  /** The root link's agent: with the subject, what a resume and a takeover must keep. */
  rootActor: string
  subject: PrincipalReference
  delegation: {id: string; digest: string}
  chain: string[]
}
/** A loaded skill as a read names it (`run-principal.json#/$defs/skillRef`). */
export interface SkillRef {id: string; digest: string}
/** The run's loaded skills, taken under the skill gate's lock when a read is made. */
export type PrincipalSkills = () => readonly SkillRef[] | Promise<readonly SkillRef[]>
/** At read version "2" (ADR-0030) a call also carries the approved descriptor digest (`version`, or null) and the loaded skills, sorted by id. */
export interface AdmissionCall {source: string; tool: string; effect: DelegationEffect; count: boolean; version?: string | null; skills?: SkillRef[]}
/** What the host's admission port is handed (`run-principal.json#/$defs/admissionRead`, or `admissionReadV2` at "2"). */
export interface AdmissionRead {readVersion: '1' | '2'; host: string; audience: Audience; chain: string[]; call: AdmissionCall; at: string}
/** The port's answer (`run-principal.json#/$defs/admissionAnswer`, or `admissionAnswerV2` and `unreadAnswer` at "2"). */
export type AdmissionAnswer = {admission: 'admit'} | {admission: 'refuse'; refusal: string; reason?: string} | {refusal: 'read-version-unsupported' | 'read-malformed'}
/** What the skill-load port is asked before a skill loads (`run-principal.json#/$defs/skillLoadRead`): the skill, and the skills loaded before it. */
export interface SkillLoadRead {readVersion: '2'; host: string; audience: Audience; chain: string[]; load: {skill: SkillRef; skills: SkillRef[]}; at: string}
export type SkillLoadAnswer = {load: 'admit'} | {load: 'refuse'; refusal: string} | {refusal: 'read-version-unsupported' | 'read-malformed'}
export type SkillLoadPort = (read: SkillLoadRead, signal: AbortSignal) => SkillLoadAnswer | Promise<SkillLoadAnswer>
export type AdmissionPort = (read: AdmissionRead, signal: AbortSignal) => AdmissionAnswer | Promise<AdmissionAnswer>
/** What the host's child port is asked for one delegated child: the parent's chain, the task, its profile and the tools with a source it is offered. */
export interface ChildLinkRequest {readVersion: '1'; host: string; chain: string[]; task: string; profile: string; tools: {source: string; tool: string}[]}
/** The child port's answer: the child's whole chain as JSON text (the parent's, and the link the issuing host issued below it), or the host's refusal. */
export type ChildLinkAnswer = {chain: string} | {refusal: 'delegation-not-permitted' | 'not-parent-actor'}
export type ChildLinkPort = (request: ChildLinkRequest, signal: AbortSignal) => ChildLinkAnswer | Promise<ChildLinkAnswer>

/**
 * What the host supplies for a delegated run (`ContextInput.principal`). `chain` is the JSON text of
 * the chain, root first, as the issuing host issued it; `keys` the issuing hosts' pinned public keys
 * by host, each with its algorithm (`KeyPin`: an Ed25519 key as 64 lowercase hexadecimal characters,
 * a P-256 key as `{alg: "ES256", key}`, its 33-byte compressed point in 66); `software` the software the host
 * states the run runs (RFC 0030 §1 rule 3); `audience` the flat token this host declares for the
 * host sources it serves; `admission` the port that answers from the issuing host's state; `clock`
 * the host's authoritative time (the system clock where absent). `child` asks the issuing host for the narrowed link
 * of a delegated child (RFC 0030 §3); with none, a delegated run starts no child.
 */
export interface RunPrincipal {
  chain: string
  keys: Readonly<Record<string, readonly KeyPin[]>>
  software: Software
  audience: string
  admission: AdmissionPort
  clock?: () => Date | number
  child?: ChildLinkPort
  /** ADR-0030: the read version the package sends, by issuing host; a host not named is read at "1". */
  readVersions?: Readonly<Record<string, '1' | '2'>>
  /** ADR-0030: the port a skill load is read through at "2"; required there when skills are catalogued. */
  skillLoad?: SkillLoadPort
}
/** What a lease and a wait hold of a delegated run's principals (`run-principal.json#/$defs/binding`), in the form of the gateway's reservation scope: a takeover or a resume keeps them. */
export interface PrincipalBinding {sub: PrincipalReference; rootActor: string; host: string}

/** A chain or a principal the package refuses, by name. Its message repeats no statement and no key. */
export class DelegationRefused extends Error {
  constructor(readonly refusal: PrincipalRefusal, detail = '') {
    super(`Delegation refused: ${refusal.name}${refusal.reason ? ` (${refusal.reason})` : ''}${detail ? `: ${detail}` : ''}.`)
    this.name = 'DelegationRefused'
  }
}
/** A `ContextInput.principal` the package cannot read at all (not a delegation refusal). */
export class PrincipalInputInvalid extends Error {
  constructor(detail: string) { super(`The run's principal is invalid: ${detail}.`); this.name = 'PrincipalInputInvalid' }
}

const object = (v: unknown): v is Record<string, unknown> => typeof v === 'object' && v !== null && !Array.isArray(v)
const hasOnly = (v: Record<string, unknown>, members: string[]) => Object.keys(v).length === members.length && members.every(m => Object.hasOwn(v, m))
const OPAQUE = /^[0-9a-f]{32}$/
const DIGEST = /^sha256:[0-9a-f]{64}$/
const TIME = /^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}\.[0-9]{3}Z$/
const LIMIT = /^x-[A-Za-z0-9._-]{1,128}$/
export const flatToken = (v: unknown): v is string => typeof v === 'string' && /^[A-Za-z0-9._-]{1,128}$/.test(v) && v !== '.' && v !== '..'
const gatewayName = (v: unknown): v is string => typeof v === 'string' && /^[!-~]{1,256}$/.test(v)
const limitName = (v: string) => v === 'calls' || LIMIT.test(v) && v !== 'x-.' && v !== 'x-..'
/** Code-point order: for strings of scalar values, the order of their UTF-8 bytes. */
export function codePointCompare(a: string, b: string): number {
  const left = [...a], right = [...b]
  for (let i = 0; i < Math.min(left.length, right.length); i++) {
    const difference = left[i]!.codePointAt(0)! - right[i]!.codePointAt(0)!
    if (difference) return difference
  }
  return left.length - right.length
}

/** The gateway's `canon` (SPEC.md §1.1) of a value read by the strict reader: integers only, names by code point. */
export function canon(v: unknown): string {
  if (Array.isArray(v)) return '[' + v.map(canon).join(',') + ']'
  if (object(v)) return '{' + Object.keys(v).sort(codePointCompare).map(k => JSON.stringify(k) + ':' + canon(v[k])).join(',') + '}'
  if (typeof v === 'number') {
    if (!Number.isSafeInteger(v)) throw new DelegationRefused({name: 'malformed'}, 'a number outside the integer domain')
    return Object.is(v, -0) ? '0' : String(v)
  }
  if (v === null || typeof v === 'string' || typeof v === 'boolean') return JSON.stringify(v)
  throw new DelegationRefused({name: 'malformed'}, 'a value outside the canonical domain')
}
const utf8 = (s: string) => new TextEncoder().encode(s)
const hex = (bytes: Uint8Array) => Array.from(bytes, b => b.toString(16).padStart(2, '0')).join('')
const unhex = (s: string) => Uint8Array.from(s.match(/../g) ?? [], x => parseInt(x, 16))
/** `sha256:` and the hex SHA-256 of canon of the whole statement, signature included. */
export const statementDigest = (link: unknown) => `sha256:${sha256(canon(link))}`

// The gateway's §8.4 key rule: canonical, not of small order, on the curve.
const P = (1n << 255n) - 19n
const L = (1n << 252n) + 27742317777372353535851937790883648493n
const modpow = (base: bigint, exponent: bigint, modulus: bigint) => {
  let result = 1n
  base %= modulus
  while (exponent > 0n) { if (exponent & 1n) result = result * base % modulus; base = base * base % modulus; exponent >>= 1n }
  return result
}
const D = (P - 121665n) * modpow(121666n, P - 2n, P) % P
const SMALL_ORDER = new Set([
  '0100000000000000000000000000000000000000000000000000000000000000',
  'ecffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f',
  '0000000000000000000000000000000000000000000000000000000000000000',
  '0000000000000000000000000000000000000000000000000000000000000080',
  '26e8958fc2b227b045c3f489f2ef98f0d5dfac05d3c63339b13802886d53fc05',
  '26e8958fc2b227b045c3f489f2ef98f0d5dfac05d3c63339b13802886d53fc85',
  'c7176a703d4dd84fba3c0b760d10670f2a2053fa2c39ccc64ec7fd7792ac037a',
  'c7176a703d4dd84fba3c0b760d10670f2a2053fa2c39ccc64ec7fd7792ac03fa',
])
const littleEndian = (bytes: Uint8Array) => bytes.reduceRight((n, b) => (n << 8n) | BigInt(b), 0n)
/** Why 32 bytes (64 lowercase hex characters) are not a key a reader may pin, or undefined. */
export function keyProblem(key: string): 'key-not-canonical' | 'key-small-order' | 'key-not-on-curve' | 'key-malformed' | undefined {
  if (!/^[0-9a-f]{64}$/.test(key)) return 'key-malformed'
  const bytes = unhex(key)
  const sign = (bytes[31]! & 0x80) !== 0
  const y = littleEndian(bytes) & ((1n << 255n) - 1n)
  if (y >= P || sign && (y === 1n || y === P - 1n)) return 'key-not-canonical'
  if (SMALL_ORDER.has(key)) return 'key-small-order'
  const u = (y * y - 1n + P) % P
  const v = (D * y % P * y + 1n) % P
  const w = u * modpow(v, P - 2n, P) % P
  if (w !== 0n && modpow(w, (P - 1n) / 2n, P) !== 1n) return 'key-not-on-curve'
  return undefined
}
/** A key's identifier: the first 32 characters of the hex SHA-256 of its bytes (32 for Ed25519, the compressed point's 33 for P-256). */
export async function keyIdentifier(key: string): Promise<string> {
  return hex(new Uint8Array(await crypto.subtle.digest('SHA-256', unhex(key)))).slice(0, 32)
}
async function signatureVerifies(key: string, signature: string, message: string): Promise<boolean> {
  const bytes = unhex(signature)
  if (littleEndian(bytes.subarray(32)) >= L) return false
  try {
    const imported = await crypto.subtle.importKey('raw', unhex(key), {name: 'Ed25519'}, false, ['verify'])
    return await crypto.subtle.verify('Ed25519', imported, bytes, utf8(message))
  } catch { return false }
}

// ES256 (RFC 0030 §2, *Signing with a P-256 key*; the gateway's §1.5): ECDSA over P-256 with SHA-256, cofactor 1.
const P256 = 0xffffffff00000001000000000000000000000000ffffffffffffffffffffffffn
const P256_B = 0x5ac635d8aa3a93e7b3ebbd55769886bc651d06b0cc53b0f63bce3c3e27d2604bn
const P256_N = 0xffffffff00000000ffffffffffffffffbce6faada7179e84f3b9cac2fc632551n
const P256_HALF_N = (P256_N - 1n) / 2n
const bigEndian = (bytes: Uint8Array) => bytes.reduce((n, b) => (n << 8n) | BigInt(b), 0n)
const be32 = (n: bigint) => Uint8Array.from({length: 32}, (_, i) => Number((n >> BigInt(8 * (31 - i))) & 0xffn))
/**
 * A P-256 key's uncompressed point (`04`, x, y) from its compressed one, or why it is not a key a reader may pin: 33 bytes
 * (66 lowercase hexadecimal characters), the first `02` (y even) or `03` (y odd), x below p, and x³ − 3x + b a square
 * modulo p, so the point exists. The uncompressed form and every other length are refused. p ≡ 3 (mod 4), so the
 * square root is a power.
 */
export function p256Point(key: string): Uint8Array | 'key-malformed' | 'key-not-canonical' | 'key-not-on-curve' {
  if (!/^0[23][0-9a-f]{64}$/.test(key)) return 'key-malformed'
  const bytes = unhex(key)
  const x = bigEndian(bytes.subarray(1))
  if (x >= P256) return 'key-not-canonical'
  const rhs = ((x * x % P256 * x - 3n * x + P256_B) % P256 + P256) % P256
  let y = modpow(rhs, (P256 + 1n) / 4n, P256)
  if (y * y % P256 !== rhs) return 'key-not-on-curve'
  if ((y & 1n) !== BigInt(bytes[0]! & 1)) y = (P256 - y) % P256
  const point = new Uint8Array(65)
  point[0] = 4
  point.set(be32(x), 1)
  point.set(be32(y), 33)
  return point
}
/** ES256 over a message: r||s with 1 ≤ r ≤ n − 1 and 1 ≤ s ≤ (n − 1)/2 (low S, which plain ECDSA does not hold), and ECDSA with SHA-256 under the point. */
async function es256Verifies(point: Uint8Array, signature: string, message: string): Promise<boolean> {
  const bytes = unhex(signature)
  const r = bigEndian(bytes.subarray(0, 32)), s = bigEndian(bytes.subarray(32))
  if (r < 1n || r > P256_N - 1n || s < 1n || s > P256_HALF_N) return false
  try {
    const imported = await crypto.subtle.importKey('raw', Uint8Array.from(point), {name: 'ECDSA', namedCurve: 'P-256'}, false, ['verify'])
    return await crypto.subtle.verify({name: 'ECDSA', hash: 'SHA-256'}, imported, bytes, utf8(message))
  } catch { return false }
}

/**
 * Whether a value is a scope identifier in the gateway's §1.2a form, never the marker `unscoped` (RFC 0030 §2, *Binding a
 * chain to one scope identifier*): a string of 1 to 128 bytes of UTF-8, one line, holding no code point display policy 1
 * refuses (`Cc`, `Bidi_Control`, `Default_Ignorable_Code_Point`, `Co`, `Cn`, `Noncharacter_Code_Point`, Unicode
 * 15.0.0). It is opaque: compared as written, never folded, trimmed or normalized. `Cn` is read from the package's own
 * Unicode 15.0.0 table (a graphic code point is assigned), with the format and separator classes, whose members are
 * assigned too, so a code point a later Unicode assigns is still refused.
 */
export function scopeIdentifier(v: unknown): v is string {
  if (typeof v !== 'string' || v === 'unscoped') return false
  const length = utf8(v).length
  if (length < 1 || length > 128) return false
  for (const c of v) {
    if (/[\p{Cc}\p{Bidi_Control}\p{Default_Ignorable_Code_Point}\p{Co}\p{Noncharacter_Code_Point}]/u.test(c)) return false
    if (!graphic(c.codePointAt(0)!) && !/[\p{Cf}\p{Zl}\p{Zp}]/u.test(c)) return false
  }
  return true
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

interface Entry {audience: Audience; source: string; tools: string[]; effects: string[]; limits: Record<string, number>}
/** One link as verified. */
export interface Link {
  value: Record<string, unknown>
  /** The signature's algorithm: ES256 where the link holds `alg`, Ed25519 where it holds none. */
  alg: DelegationAlg
  /** The scope identifier the link is bound to, or absent for an unbound link. */
  scopeId?: string
  id: string; host: string; keyId: string; sub: PrincipalReference; agent: string; software: Software
  entries: Entry[]; mayDelegate: boolean; issuedAt: number; expiresAt: number
  parent?: {id: string; digest: string}; depth: number; signature: string; digest: string
}
/** A chain that passed steps 1 to 7, root first; `scopeId` the scope identifier it is bound to, the root's, which clause 9 holds every link to. */
export interface VerifiedChain {links: Link[]; digests: string[]; scopeId?: string}
export type DelegationAlg = 'Ed25519' | 'ES256'
/** One pinned key as the host configures it, in the gateway's pin form: an Ed25519 key's 64 lowercase hexadecimal characters, or a P-256 key's compressed point with its algorithm. */
export type KeyPin = string | {alg: 'ES256'; key: string}
/** A pinned key with the algorithm it was pinned with: only a link of that algorithm is verified under it. */
export interface PinnedKey {alg: DelegationAlg; key: string; point?: Uint8Array}
/** Pinned keys: by host, by key identifier, each with its algorithm. */
export type PinnedKeys = ReadonlyMap<string, ReadonlyMap<string, PinnedKey>>

/**
 * Reads each host's keys, holding every key to its algorithm's key rule; a key that fails it is refused, never pinned.
 * The algorithm is the pin's: a string is an Ed25519 key, `{alg: "ES256", key}` a P-256 key. A key's length or form is
 * held to that algorithm and never read as one.
 */
export async function pinKeys(keys: unknown): Promise<PinnedKeys> {
  if (!object(keys)) throw new PrincipalInputInvalid('keys is not an object of hosts')
  const out = new Map<string, Map<string, PinnedKey>>()
  for (const [host, list] of Object.entries(keys)) {
    if (!flatToken(host) || !Array.isArray(list) || list.length === 0 || list.length > 16) throw new PrincipalInputInvalid('keys names a host that is not a flat token, or a host with no keys')
    const held = new Map<string, PinnedKey>()
    for (const pin of list) {
      let pinned: PinnedKey
      if (typeof pin === 'string') {
        const problem = keyProblem(pin)
        if (problem !== undefined) throw new PrincipalInputInvalid(`a pinned key is refused (${problem})`)
        pinned = {alg: 'Ed25519', key: pin}
      } else if (object(pin) && hasOnly(pin, ['alg', 'key']) && pin.alg === 'ES256' && typeof pin.key === 'string') {
        const point = p256Point(pin.key)
        if (typeof point === 'string') throw new PrincipalInputInvalid(`a pinned key is refused (${point})`)
        pinned = {alg: 'ES256', key: pin.key, point}
      } else throw new PrincipalInputInvalid('a pinned key is refused (key-malformed)')
      const id = await keyIdentifier(pinned.key)
      if (held.has(id) && held.get(id)!.alg !== pinned.alg) throw new PrincipalInputInvalid('two pinned keys of a host share a key identifier')
      held.set(id, pinned)
    }
    out.set(host, held)
  }
  return out
}

function parseTime(v: unknown): number | undefined {
  if (typeof v !== 'string' || !TIME.test(v)) return undefined
  const at = Date.parse(v)
  return Number.isFinite(at) && new Date(at).toISOString() === v ? at : undefined
}
const formatTime = (at: number) => new Date(at).toISOString()
function principalReference(v: unknown): PrincipalReference | undefined {
  if (!object(v) || !hasOnly(v, ['kind', 'issuer', 'subject'])) return undefined
  if ((v.kind !== 'person' && v.kind !== 'service') || typeof v.issuer !== 'string' || typeof v.subject !== 'string' || !v.issuer || !v.subject) return undefined
  return {kind: v.kind, issuer: v.issuer, subject: v.subject}
}
function software(v: unknown): Software | undefined {
  if (object(v) && hasOnly(v, ['package', 'version'])) return typeof v.package === 'string' && typeof v.version === 'string' && v.package && v.version ? {package: v.package, version: v.version} : undefined
  if (object(v) && hasOnly(v, ['harness'])) return typeof v.harness === 'string' && DIGEST.test(v.harness) ? {harness: v.harness} : undefined
  return undefined
}
function sortedStrings(v: unknown, max: number): string[] | undefined {
  if (!Array.isArray(v) || v.length === 0 || v.length > max) return undefined
  for (let i = 0; i < v.length; i++) if (typeof v[i] !== 'string' || i > 0 && codePointCompare(v[i - 1], v[i]) >= 0) return undefined
  return [...v] as string[]
}
function entry(v: unknown): Entry | undefined {
  if (!object(v) || !hasOnly(v, ['audience', 'source', 'tools', 'effects', 'limits'])) return undefined
  const a = v.audience
  if (!object(a) || !hasOnly(a, ['kind', 'name'])) return undefined
  if (!(a.kind === 'gateway' && gatewayName(a.name) || a.kind === 'agent-host' && flatToken(a.name))) return undefined
  if (typeof v.source !== 'string' || !v.source) return undefined
  const tools = sortedStrings(v.tools, DELEGATION_BOUNDS.tools)
  if (!tools) return undefined
  if (!Array.isArray(v.effects) || v.effects.length === 0) return undefined
  for (let i = 0; i < v.effects.length; i++) {
    const strength = EFFECTS.indexOf(v.effects[i] as DelegationEffect)
    if (strength < 0 || i > 0 && strength <= EFFECTS.indexOf(v.effects[i - 1] as DelegationEffect)) return undefined
  }
  if (!object(v.limits) || Object.keys(v.limits).length > DELEGATION_BOUNDS.limits) return undefined
  for (const [name, bound] of Object.entries(v.limits)) if (!limitName(name) || typeof bound !== 'number' || !Number.isSafeInteger(bound) || bound < 0) return undefined
  return {audience: {kind: a.kind as Audience['kind'], name: a.name as string}, source: v.source, tools, effects: [...v.effects] as string[], limits: {...v.limits} as Record<string, number>}
}
const entryCompare = (a: Entry, b: Entry) => codePointCompare(a.audience.kind, b.audience.kind) || codePointCompare(a.audience.name, b.audience.name) || codePointCompare(a.source, b.source)
const MEMBERS = ['delegationVersion', 'id', 'host', 'keyId', 'sub', 'act', 'scope', 'mayDelegate', 'issuedAt', 'expiresAt', 'parent', 'depth', 'signature']

/** Step 3 for one link: its structure, or why not. */
function parseLink(v: Record<string, unknown>): Link | string {
  for (const m of MEMBERS) if (!Object.hasOwn(v, m)) return `member ${m} is missing`
  if (typeof v.id !== 'string' || !OPAQUE.test(v.id)) return 'id is not an opaque identifier'
  if (!flatToken(v.host)) return 'host is not a flat token'
  if (typeof v.keyId !== 'string' || !OPAQUE.test(v.keyId)) return 'keyId is not a key identifier'
  const sub = principalReference(v.sub)
  if (!sub) return 'sub is not a principal reference'
  if (!object(v.act) || !hasOnly(v.act, ['agent', 'software'])) return 'act is not {agent, software}'
  if (typeof v.act.agent !== 'string' || !OPAQUE.test(v.act.agent)) return 'act.agent is not an opaque identifier'
  const soft = software(v.act.software)
  if (!soft) return 'act.software is not a software identity'
  if (Object.hasOwn(v, 'alg') && v.alg !== 'ES256') return 'alg is not the string "ES256"'
  if (!object(v.scope) || !(hasOnly(v.scope, ['sources']) || hasOnly(v.scope, ['sources', 'scopeId']))) return 'scope is not {sources} or {sources, scopeId}'
  if (Object.hasOwn(v.scope, 'scopeId') && !scopeIdentifier(v.scope.scopeId)) return 'scope.scopeId is not a scope identifier'
  const sources = v.scope.sources
  if (!Array.isArray(sources) || sources.length === 0 || sources.length > DELEGATION_BOUNDS.entries) return 'scope.sources is not an array of 1 to 256 entries'
  const entries: Entry[] = []
  for (let i = 0; i < sources.length; i++) {
    const e = entry(sources[i])
    if (!e) return `scope.sources[${i}] is not a scope entry`
    if (i > 0 && entryCompare(entries[i - 1]!, e) >= 0) return 'scope.sources is out of order or repeats an audience and source'
    entries.push(e)
  }
  if (typeof v.mayDelegate !== 'boolean') return 'mayDelegate is not a boolean'
  const issuedAt = parseTime(v.issuedAt), expiresAt = parseTime(v.expiresAt)
  if (issuedAt === undefined) return 'issuedAt is not a time'
  if (expiresAt === undefined) return 'expiresAt is not a time'
  if (expiresAt <= issuedAt) return 'expiresAt is not after issuedAt'
  if (expiresAt - issuedAt > DELEGATION_BOUNDS.lifetimeMilliseconds) return 'the link lives more than 24 hours'
  if (typeof v.depth !== 'number' || !Number.isSafeInteger(v.depth) || v.depth < 0) return 'depth is not a non-negative integer'
  let parent: Link['parent']
  if (v.parent === null) { if (v.depth !== 0) return 'a link with no parent has a depth other than 0' }
  else {
    if (!object(v.parent) || !hasOnly(v.parent, ['id', 'digest'])) return 'parent is neither null nor {id, digest}'
    if (typeof v.parent.id !== 'string' || !OPAQUE.test(v.parent.id) || typeof v.parent.digest !== 'string' || !DIGEST.test(v.parent.digest)) return 'parent is not of its form'
    if (v.depth === 0) return 'a link with a parent has a depth of 0'
    parent = {id: v.parent.id, digest: v.parent.digest}
  }
  if (typeof v.signature !== 'string' || !/^[0-9a-f]{128}$/.test(v.signature)) return 'signature is not 128 lowercase hexadecimal characters'
  const text = canon(v)
  if (utf8(text).length > DELEGATION_BOUNDS.statementBytes) return 'the link is over 65536 bytes'
  const scopeId = Object.hasOwn(v.scope, 'scopeId') ? v.scope.scopeId as string : undefined
  return {value: v, alg: v.alg === 'ES256' ? 'ES256' : 'Ed25519', ...(scopeId !== undefined ? {scopeId} : {}), id: v.id, host: v.host, keyId: v.keyId, sub, agent: v.act.agent, software: soft, entries, mayDelegate: v.mayDelegate, issuedAt, expiresAt, ...(parent ? {parent} : {}), depth: v.depth, signature: v.signature, digest: `sha256:${sha256(text)}`}
}
const signingInput = (link: Link) => DELEGATION_CONTEXT + canon(Object.fromEntries(Object.entries(link.value).filter(([name]) => name !== 'signature')))
const sameReference = (a: PrincipalReference, b: PrincipalReference) => a.kind === b.kind && a.issuer === b.issuer && a.subject === b.subject

/** Clauses 4 to 8 for a child and its parent: why the child is wider, or undefined. */
export function widening(child: Link, parent: Link): string | undefined {
  if (!sameReference(child.sub, parent.sub)) return 'changes the subject'
  if (child.issuedAt < parent.issuedAt || child.expiresAt > parent.expiresAt) return "is valid outside its parent's validity"
  for (const e of child.entries) {
    const p = parent.entries.find(pe => pe.audience.kind === e.audience.kind && pe.audience.name === e.audience.name && pe.source === e.source)
    if (!p) return 'admits a source its parent does not'
    if (e.tools.some(t => !p.tools.includes(t))) return 'admits a tool its parent does not'
    if (e.effects.some(effect => !p.effects.includes(effect))) return 'admits an effect its parent does not'
    for (const [name, bound] of Object.entries(p.limits)) if (!Object.hasOwn(e.limits, name) || e.limits[name]! > bound) return 'drops or raises a limit of its parent'
  }
  if (!parent.mayDelegate) return 'is below a link that permits no child'
  // Clause 9: the binding is the root's, and each child repeats it; a bound link under an unbound one is refused too.
  if (child.scopeId !== parent.scopeId) return parent.scopeId === undefined ? 'is bound under an unbound parent' : child.scopeId === undefined ? 'is unbound under a bound parent' : 'is bound to another scope identifier than its parent'
  return undefined
}

/**
 * Steps 1 to 7 over a chain's JSON text, in order, stopping at the first failure; within a step the
 * links are taken root first. Throws `DelegationRefused`.
 */
export async function verifyChain(text: string, keys: PinnedKeys): Promise<VerifiedChain> {
  const refuse = (name: RefusalName, detail: string): never => { throw new DelegationRefused({name}, detail) }
  // Step 1: the strict reader refuses a duplicate member and a value outside the canonical domain.
  let value: unknown
  try { value = readDescriptorJSON(text, true) } catch { return refuse('malformed', 'the chain is not canonical-domain JSON') }
  if (!Array.isArray(value) || value.length === 0 || value.length > DELEGATION_BOUNDS.depth + 1) return refuse('malformed', 'a chain is an array of one to five statements')
  for (let i = 0; i < value.length; i++) {
    if (!object(value[i])) return refuse('malformed', `link ${i} is not an object`)
    if (typeof value[i].delegationVersion !== 'string') return refuse('malformed', `link ${i} has no string delegationVersion`)
  }
  const objects = value as Record<string, unknown>[]
  // Step 2.
  for (let i = 0; i < objects.length; i++) if (objects[i]!.delegationVersion !== '1') return refuse('unsupported-version', `link ${i} is of a delegation version this package does not read`)
  // Step 3.
  const links: Link[] = []
  for (let i = 0; i < objects.length; i++) {
    const link = parseLink(objects[i]!)
    if (typeof link === 'string') return refuse('malformed', `link ${i}: ${link}`)
    links.push(link)
  }
  // Step 4: the key the link names among those pinned for its host with the link's algorithm; one of the other algorithm is not it.
  for (let i = 0; i < links.length; i++) if (keys.get(links[i]!.host)?.get(links[i]!.keyId)?.alg !== links[i]!.alg) return refuse('key-mismatch', `link ${i} is signed by a key not pinned for its host with its algorithm`)
  // Step 5: by the link's algorithm, which step 4 held to the pin's.
  for (let i = 0; i < links.length; i++) {
    const link = links[i]!, pinned = keys.get(link.host)!.get(link.keyId)!
    const verifies = link.alg === 'ES256' && pinned.alg === 'ES256' ? await es256Verifies(pinned.point!, link.signature, signingInput(link)) : link.alg === 'Ed25519' && pinned.alg === 'Ed25519' && await signatureVerifies(pinned.key, link.signature, signingInput(link))
    if (!verifies) return refuse('signature-mismatch', `link ${i}'s signature does not verify`)
  }
  // Step 6: clauses 0 to 3.
  if (links[0]!.parent !== undefined || links[0]!.depth !== 0) return refuse('chain-broken', 'the chain does not start at a root')
  for (let k = 1; k < links.length; k++) {
    const c = links[k]!, p = links[k - 1]!
    if (c.parent?.id !== p.id || c.parent.digest !== p.digest) return refuse('chain-broken', `link ${k} does not name link ${k - 1} as its parent`)
    if (c.depth !== p.depth + 1 || c.depth > DELEGATION_BOUNDS.depth) return refuse('chain-broken', `link ${k}'s depth is not its parent's plus one, within the maximum`)
    if (c.host !== p.host) return refuse('chain-broken', `link ${k} names another host than its parent`)
  }
  // Step 7: clauses 4 to 8.
  for (let k = 1; k < links.length; k++) {
    const why = widening(links[k]!, links[k - 1]!)
    if (why !== undefined) return refuse('widened', `link ${k} ${why}`)
  }
  return {links, digests: links.map(link => link.digest), ...(links[0]!.scopeId !== undefined ? {scopeId: links[0]!.scopeId} : {})}
}

/** A delegated run's principal, opened: the verified chain, the record member, and the host's ports. */
export interface OpenedPrincipal {
  readonly chain: VerifiedChain
  readonly record: PrincipalRecord
  readonly audience: string
  readonly keys: PinnedKeys
  readonly software: Software
  readonly admission: AdmissionPort
  readonly clock: () => Date | number
  /** The chain's JSON text, root first, as the issuing host supplied it. */
  readonly text: string
  /** The host's input, which a child's principal is opened from with its own chain. */
  readonly input: RunPrincipal
  /** The read version the root's issuing host is read at (ADR-0030). */
  readonly readVersion: '1' | '2'
  readonly skillLoad?: SkillLoadPort
}

function readSoftware(v: unknown): Software {
  const held = software(v)
  if (!held) throw new PrincipalInputInvalid('software is neither {package, version} nor {harness}')
  return held
}
/** Whether a value is a principal record member of its exact form (`run-principal.json`). */
export function validPrincipalRecord(v: unknown): v is PrincipalRecord {
  if (!object(v) || !hasOnly(v, ['actor', 'rootActor', 'subject', 'delegation', 'chain'])) return false
  if (typeof v.rootActor !== 'string' || !OPAQUE.test(v.rootActor)) return false
  const a = v.actor, d = v.delegation
  if (!object(a) || !hasOnly(a, ['agent', 'host', 'software']) || typeof a.agent !== 'string' || !OPAQUE.test(a.agent) || !flatToken(a.host) || !software(a.software)) return false
  if (!principalReference(v.subject)) return false
  if (!object(d) || !hasOnly(d, ['id', 'digest']) || typeof d.id !== 'string' || !OPAQUE.test(d.id) || typeof d.digest !== 'string' || !DIGEST.test(d.digest)) return false
  return Array.isArray(v.chain) && v.chain.length >= 1 && v.chain.length <= DELEGATION_BOUNDS.depth + 1 && v.chain.every(x => typeof x === 'string' && DIGEST.test(x)) && v.chain[v.chain.length - 1] === d.digest
}
/** The record member of a verified chain: the leaf's actor and link, the chain's subject and digests. */
export function principalRecord(chain: VerifiedChain): PrincipalRecord {
  const leaf = chain.links[chain.links.length - 1]!
  return {actor: {agent: leaf.agent, host: leaf.host, software: structuredClone(leaf.software)}, rootActor: chain.links[0]!.agent, subject: {...leaf.sub}, delegation: {id: leaf.id, digest: leaf.digest}, chain: [...chain.digests]}
}

/**
 * Opens the host's `ContextInput.principal`: the input's form (`PrincipalInputInvalid`), the pinned
 * keys held to the key rule, steps 1 to 7 over the chain, and the leaf's software against the
 * software the host states the run runs (`software-mismatch`). Throws `DelegationRefused`.
 */
export async function openPrincipal(input: RunPrincipal): Promise<OpenedPrincipal> {
  if (!object(input)) throw new PrincipalInputInvalid('it is not an object')
  const known = ['chain', 'keys', 'software', 'audience', 'admission', 'clock', 'child', 'readVersions', 'skillLoad']
  if (Object.keys(input).some(name => !known.includes(name))) throw new PrincipalInputInvalid('it carries a member the package does not name')
  if (typeof input.chain !== 'string') throw new PrincipalInputInvalid("chain is not the chain's JSON text")
  if (!flatToken(input.audience)) throw new PrincipalInputInvalid('audience is not a flat token')
  if (typeof input.admission !== 'function') throw new PrincipalInputInvalid('admission is not a function')
  if (input.clock !== undefined && typeof input.clock !== 'function') throw new PrincipalInputInvalid('clock is not a function')
  if (input.child !== undefined && typeof input.child !== 'function') throw new PrincipalInputInvalid('child is not a function')
  if (input.skillLoad !== undefined && typeof input.skillLoad !== 'function') throw new PrincipalInputInvalid('skillLoad is not a function')
  if (input.readVersions !== undefined) {
    if (!object(input.readVersions)) throw new PrincipalInputInvalid('readVersions is not an object')
    for (const host of Object.keys(input.readVersions).sort(codePointCompare)) {
      const version: unknown = input.readVersions[host]
      if (!flatToken(host) || (version !== '1' && version !== '2')) throw new PrincipalInputInvalid(`readVersions names ${JSON.stringify(String(version).slice(0, 16))} for the host ${JSON.stringify(host.slice(0, 128))}; a host is read at "1" or "2"`)
    }
  }
  const runSoftware = readSoftware(input.software)
  const keys = await pinKeys(input.keys)
  const chain = await verifyChain(input.chain, keys)
  const leaf = chain.links[chain.links.length - 1]!
  if (canon(leaf.software) !== canon(runSoftware)) throw new DelegationRefused({name: 'software-mismatch'}, "the leaf's software is not the software the host states the run runs")
  const root = chain.links[0]!.host
  const readVersion = input.readVersions !== undefined && Object.hasOwn(input.readVersions, root) ? input.readVersions[root]! : '1'
  return Object.freeze({chain, record: principalRecord(chain), audience: input.audience, keys, software: runSoftware, admission: input.admission, clock: input.clock ?? (() => Date.now()), text: input.chain, input, readVersion, ...(input.skillLoad ? {skillLoad: input.skillLoad} : {})})
}

/** The binding a lease and a wait hold of a principal record. */
export function principalBinding(record: PrincipalRecord): PrincipalBinding {
  return {sub: {...record.subject}, rootActor: record.rootActor, host: record.actor.host}
}
/** Whether a value is a principal binding of its exact form. */
export function validPrincipalBinding(v: unknown): v is PrincipalBinding {
  return object(v) && hasOnly(v, ['sub', 'rootActor', 'host']) && principalReference(v.sub) !== undefined && typeof v.rootActor === 'string' && OPAQUE.test(v.rootActor) && flatToken(v.host)
}
/** Two bindings, or their absences, are the same principals: all members equal. */
export const sameBinding = (a: PrincipalBinding | undefined, b: PrincipalBinding | undefined) => (a === undefined || b === undefined) ? a === b : sameReference(a.sub, b.sub) && a.rootActor === b.rootActor && a.host === b.host

/**
 * The narrowed principal of a delegated child (RFC 0030 §3). A child exists only below a link that permits one
 * (`delegation-not-permitted`, and with no child port, which issues none) and only with the link the issuing host
 * issued for it (a port that gives no valid answer in time is `admission-unknown`). The model never names or supplies
 * the link. Throws `DelegationRefused`. `openChildPrincipal` verifies what this returns.
 */
export async function narrowChild(parent: OpenedPrincipal, request: {task: string; profile: string; tools: {source: string; tool: string}[]}, signal?: AbortSignal, skills?: PrincipalSkills): Promise<RunPrincipal> {
  const leaf = parent.chain.links[parent.chain.links.length - 1]!
  if (!leaf.mayDelegate) throw new DelegationRefused({name: 'delegation-not-permitted'}, 'the link this run acts under permits no child')
  const port = parent.input.child
  if (port === undefined) throw new DelegationRefused({name: 'delegation-not-permitted'}, 'the host issues no child link')
  // Nothing of a child starts from a delegation that no longer stands: a resumed batch has no earlier check to rely on.
  const standing = await admitStanding(parent, 'delegate_tasks', 'pure', signal, skills)
  if (standing !== undefined) throw new DelegationRefused(standing, 'the delegation this run acts under no longer stands')
  const controller = new AbortController()
  const abort = () => controller.abort()
  signal?.addEventListener('abort', abort, {once: true})
  let timer: ReturnType<typeof setTimeout> | undefined
  let answer: unknown
  try {
    answer = await Promise.race([
      Promise.resolve().then(() => port({readVersion: '1', host: leaf.host, chain: [...parent.chain.digests], task: request.task, profile: request.profile, tools: structuredClone(request.tools)}, controller.signal)),
      new Promise((_, reject) => { timer = setTimeout(() => reject(new Error('child link timed out')), ADMISSION_MILLISECONDS) }),
      ...(signal ? [new Promise((_, reject) => { if (signal.aborted) reject(signal.reason); signal.addEventListener('abort', () => reject(signal.reason), {once: true}) })] : []),
    ])
  } catch (error) {
    if (signal?.aborted) throw error
    throw new DelegationRefused({name: 'admission-unknown'}, 'the issuing host gave no child link')
  } finally {
    if (timer !== undefined) clearTimeout(timer)
    signal?.removeEventListener('abort', abort)
    controller.abort()
  }
  if (object(answer) && hasOnly(answer, ['refusal']) && (answer.refusal === 'delegation-not-permitted' || answer.refusal === 'not-parent-actor')) throw new DelegationRefused({name: answer.refusal})
  if (!object(answer) || !hasOnly(answer, ['chain']) || typeof answer.chain !== 'string') throw new DelegationRefused({name: 'admission-unknown'}, 'the issuing host gave no child link')
  return {...parent.input, chain: answer.chain}
}
/**
 * Opens a child's principal under its parent's: steps 1 to 7 over the child's whole chain (a link wider than its parent
 * is `widened`), and the chain must be the parent's with exactly one link below it (`chain-broken`): never a chain a
 * child or a tool result supplies, and never its parent's whole reach under another name.
 */
export async function openChildPrincipal(parent: OpenedPrincipal, input: RunPrincipal): Promise<OpenedPrincipal> {
  const opened = await openPrincipal(input)
  const held = parent.chain.digests, digests = opened.chain.digests
  if (digests.length !== held.length + 1 || held.some((digest, at) => digests[at] !== digest)) throw new DelegationRefused({name: 'chain-broken'}, "the child's chain is not its parent's with one link below it")
  return opened
}

/** Opens a run's principal for its session: a refused chain or an unreadable input ends the run before anything is sent, by name (`configuration`). */
export async function openRunPrincipal(input: RunPrincipal, parent?: OpenedPrincipal): Promise<OpenedPrincipal> {
  try { return parent ? await openChildPrincipal(parent, input) : await openPrincipal(input) } catch (error) {
    if (error instanceof DelegationRefused) throw authoredFailure('configuration', `The run's delegation is refused (${error.refusal.name}). Nothing was sent.`, 'context')
    if (error instanceof PrincipalInputInvalid) throw authoredFailure('configuration', error.message + ' Nothing was sent.', 'context')
    throw error
  }
}

/** Whether a run has a delegation to open: one given to it, or a checkpoint it is restored from that carries one. */
export const holdsDelegation = (input: {principal?: RunPrincipal; execution?: {principal?: PrincipalRecord}} | undefined): boolean => input?.principal !== undefined || input?.execution?.principal !== undefined
/** A delegated run's child starts under the run's opened principal, never without it: this holds on every path that reaches a child, whatever opened or did not open before it. */
export function requireOpenedPrincipal(opened: OpenedPrincipal | undefined, input: Parameters<typeof holdsDelegation>[0]): void {
  if (opened === undefined && holdsDelegation(input)) throw authoredFailure('stale-state', "The run's delegation is not open, so no child of it starts. Nothing was sent.", 'context')
}
/** What a resume under other principals says, in both languages' session and at a durable root. */
export const PRINCIPAL_CHANGED = 'The run continues under another principal than it began under (principal-changed). Nothing was sent.'

/**
 * The call the package admits: the audience its source answers to (null or undefined for a gateway source with no
 * mapping), its source, tool and effect. `scopeId`, for a call to a gateway source only, is the scope identifier the
 * host's mapping fixes the source to, or null where it names none; it is absent for a host source, which has none.
 */
export interface PrincipalCall {audience: Audience | null | undefined; source: string; tool: string; effect: string | undefined; scopeId?: string | null; /** ADR-0030: the approved snapshot's descriptor digest for the tool, or null. */ version?: string | null; skills?: PrincipalSkills}

/**
 * The request check of the chain's binding (RFC 0030 §2, *Binding a chain to one scope identifier*), made before step 8
 * and any admission read: a call to a gateway source is sent with the chain's scope identifier, or with none under an
 * unbound chain, so a gateway source the host's mapping fixes to another scope identifier, to none under a bound chain,
 * or to one under an unbound chain, is refused before the call leaves the harness. A host source (no `scopeId`) is
 * unaffected. It reads no issuing host, counts nothing and dispatches nothing.
 */
export function scopeIdRefusal(chain: VerifiedChain, call: Pick<PrincipalCall, 'scopeId'>): PrincipalRefusal | undefined {
  return call.scopeId !== undefined && (chain.scopeId ?? null) !== (call.scopeId ?? null) ? {name: 'scope-id-mismatch'} : undefined
}

/** The package's own part of step 11 over every link: the first failing reason, within a link before the next. */
export function scopeReason(chain: VerifiedChain, call: PrincipalCall): ScopeReason | undefined {
  for (const link of chain.links) {
    const e = call.audience == null ? undefined : link.entries.find(entry => entry.audience.kind === call.audience!.kind && entry.audience.name === call.audience!.name && entry.source === call.source)
    if (!e) return 'source'
    if (!call.tool || !e.tools.includes(call.tool)) return 'tool'
    if (call.effect === undefined || !e.effects.includes(call.effect)) return 'effect'
    if (Object.keys(e.limits).some(name => name !== 'calls')) return 'limit-unknown'
  }
  return undefined
}

const BEFORE_SCOPE: readonly string[] = ['expired', 'agent-unregistered', 'software-mismatch', 'not-issued', 'revoked']
const HOST_REFUSALS: readonly string[] = [...BEFORE_SCOPE, 'subject-mismatch', 'subject-unauthorized']
/** Step 12 refusals only a read at "2" can be answered (ADR-0030). */
const STEP_TWELVE_V2: readonly string[] = ['tool-version-refused', 'skill-refused']
/** A read the issuing host did not read: reported where steps 8 to 10 are, never resent at "1". */
const UNREAD: readonly string[] = ['read-version-unsupported', 'read-malformed']
const SKILL_LOAD_REFUSALS: readonly string[] = [...BEFORE_SCOPE, 'skill-refused']
const skillRefs = (skills: readonly SkillRef[]): SkillRef[] => skills.map(skill => ({id: skill.id, digest: skill.digest})).sort((a, b) => codePointCompare(a.id, b.id))

/** The host's time in the record form, or undefined where it cannot be read. */
function hostTime(clock: () => Date | number): number | undefined {
  try {
    const at = clock()
    const ms = at instanceof Date ? at.getTime() : at
    return typeof ms === 'number' && Number.isFinite(ms) && ms >= 0 && ms < 253402300800000 ? Math.floor(ms) : undefined
  } catch { return undefined }
}

/** The port's answer held to its exact form, or admission-unknown. */
function readAnswer(answer: unknown, version: '1' | '2' = '1'): PrincipalRefusal | undefined | 'unknown' {
  if (!object(answer)) return 'unknown'
  if (version === '2' && hasOnly(answer, ['refusal']) && UNREAD.includes(answer.refusal as string)) return {name: answer.refusal as RefusalName}
  if (answer.admission === 'admit' && hasOnly(answer, ['admission'])) return undefined
  if (answer.admission !== 'refuse') return 'unknown'
  if (answer.refusal === 'out-of-scope') return hasOnly(answer, ['admission', 'refusal', 'reason']) && SCOPE_REASONS.includes(answer.reason as ScopeReason) ? {name: 'out-of-scope', reason: answer.reason as ScopeReason} : 'unknown'
  return hasOnly(answer, ['admission', 'refusal']) && (HOST_REFUSALS.includes(answer.refusal as string) || version === '2' && STEP_TWELVE_V2.includes(answer.refusal as string)) ? {name: answer.refusal as RefusalName} : 'unknown'
}

/** The skill-load port's answer held to its exact form, or admission-unknown. */
function readSkillLoadAnswer(answer: unknown): PrincipalRefusal | undefined | 'unknown' {
  if (!object(answer)) return 'unknown'
  if (hasOnly(answer, ['load']) && answer.load === 'admit') return undefined
  if (hasOnly(answer, ['refusal']) && UNREAD.includes(answer.refusal as string)) return {name: answer.refusal as RefusalName}
  return hasOnly(answer, ['load', 'refusal']) && answer.load === 'refuse' && SKILL_LOAD_REFUSALS.includes(answer.refusal as string) ? {name: answer.refusal as RefusalName} : 'unknown'
}

/** A call's read at the root host's read version: at "2" with the version and the loaded skills, taken now. */
async function callRead(principal: OpenedPrincipal, audience: Audience, call: AdmissionCall, version: string | null | undefined, skills: PrincipalSkills | undefined, at: number): Promise<AdmissionRead> {
  const base = {host: principal.chain.links[0]!.host, audience: {...audience}, chain: [...principal.chain.digests]}
  if (principal.readVersion !== '2') return {readVersion: '1', ...base, call, at: formatTime(at)}
  return {readVersion: '2', ...base, call: {...call, version: version ?? null, skills: skillRefs(skills ? await skills() : [])}, at: formatTime(at)}
}

const askPort = (principal: OpenedPrincipal, read: AdmissionRead, signal?: AbortSignal): Promise<unknown> => ask(port => principal.admission(structuredClone(read), port), signal)

async function ask(port: (signal: AbortSignal) => unknown, signal?: AbortSignal): Promise<unknown> {
  const controller = new AbortController()
  const abort = () => controller.abort()
  signal?.addEventListener('abort', abort, {once: true})
  let timer: ReturnType<typeof setTimeout> | undefined
  try {
    return await Promise.race([
      Promise.resolve().then(() => port(controller.signal)),
      new Promise((_, reject) => { timer = setTimeout(() => reject(new Error('admission timed out')), ADMISSION_MILLISECONDS) }),
      ...(signal ? [new Promise((_, reject) => { if (signal.aborted) reject(signal.reason); signal.addEventListener('abort', () => reject(signal.reason), {once: true}) })] : []),
    ])
  } finally {
    if (timer !== undefined) clearTimeout(timer)
    signal?.removeEventListener('abort', abort)
    controller.abort()
  }
}

/**
 * One admission (RFC 0030 §2, *When admission happens*): at the host's time, step 8, the package's
 * own part of step 11, and the port's answer for steps 9, 10, the calls limit and 12, read afresh.
 * `count` asks the issuing host to count this dispatch: only the admission immediately before a
 * dispatch this package is the enforcing host of, and only when its own checks held. A refusal of
 * steps 8 to 10 the port answers, or a read that cannot be made, is reported before the package's
 * own, and the package's own before the port's step 11 or 12. Returns the refusal, or undefined
 * where the call is admitted. A call aborted while it waits throws the abort.
 */
/**
 * Whether the chain stands, for a call a scope cannot name: a host tool with no source, whose reach is local by its
 * declaration (an `act` or `read-external` tool must have a source). Steps 8 to 10 and 12 apply and scope does not:
 * validity at the host's time, then the port's answer, read afresh, under `source: local` with counting unset.
 */
export async function admitStanding(principal: OpenedPrincipal, tool: string, effect: string | undefined, signal?: AbortSignal, skills?: PrincipalSkills): Promise<PrincipalRefusal | undefined> {
  const at = hostTime(principal.clock)
  if (at === undefined) return {name: 'admission-unknown'}
  const links = principal.chain.links
  if (links.some(link => at < link.issuedAt)) return {name: 'not-yet-valid'}
  if (links.some(link => at >= link.expiresAt)) return {name: 'expired'}
  if (effect === undefined || !(EFFECTS as readonly string[]).includes(effect)) return {name: 'out-of-scope', reason: 'effect'}
  const read = await callRead(principal, {kind: 'agent-host', name: principal.audience}, {source: LOCAL_SOURCE, tool, effect: effect as DelegationEffect, count: false}, null, skills, at)
  let answer: PrincipalRefusal | undefined | 'unknown'
  try { answer = readAnswer(await askPort(principal, read, signal), principal.readVersion) } catch (error) {
    if (signal?.aborted) throw error
    answer = 'unknown'
  }
  return answer === 'unknown' ? {name: 'admission-unknown'} : answer
}

/** An acting Runtime call's admission: its source record under the run's agent-host audience, effect `act`. The host dispatches it, so the final admission counts. */
export const admitRuntimeCall = (principal: OpenedPrincipal, tool: string, count: boolean, signal?: AbortSignal, skills?: PrincipalSkills): Promise<PrincipalRefusal | undefined> =>
  admitCall(principal, {audience: {kind: 'agent-host', name: principal.audience}, source: RUNTIME_SOURCE, tool, effect: 'act', skills}, count, signal)

/** Whether a resume runs under other principals than it began under: another subject, root actor or issuing host, or a delegation only one side has. A re-issued or narrowed chain of the same three is not. */
export function principalsChanged(saved: PrincipalRecord | undefined, current: PrincipalRecord | undefined): boolean {
  if (saved === undefined || current === undefined) return saved !== current
  return canon(saved.subject) !== canon(current.subject) || saved.rootActor !== current.rootActor || saved.actor.host !== current.actor.host
}

export async function admitCall(principal: OpenedPrincipal, call: PrincipalCall, count: boolean, signal?: AbortSignal): Promise<PrincipalRefusal | undefined> {
  const scope = scopeIdRefusal(principal.chain, call)
  if (scope !== undefined) return scope
  const at = hostTime(principal.clock)
  if (at === undefined) return {name: 'admission-unknown'}
  const links = principal.chain.links
  if (links.some(link => at < link.issuedAt)) return {name: 'not-yet-valid'}
  if (links.some(link => at >= link.expiresAt)) return {name: 'expired'}
  const own = scopeReason(principal.chain, call)
  const ownRefusal: PrincipalRefusal | undefined = own === undefined ? undefined : {name: 'out-of-scope', reason: own}
  // Nothing names the call to the issuing host where its audience or effect cannot be stated.
  if (call.audience == null || call.effect === undefined || !(EFFECTS as readonly string[]).includes(call.effect) || !call.tool || !call.source) return ownRefusal ?? {name: 'out-of-scope', reason: 'source'}
  const read = await callRead(principal, call.audience, {source: call.source, tool: call.tool, effect: call.effect as DelegationEffect, count: count && ownRefusal === undefined}, call.version, call.skills, at)
  let answer: PrincipalRefusal | undefined | 'unknown'
  try { answer = readAnswer(await askPort(principal, read, signal), principal.readVersion) } catch (error) {
    if (signal?.aborted) throw error
    answer = 'unknown'
  }
  if (answer === 'unknown') return {name: 'admission-unknown'}
  if (answer !== undefined && (BEFORE_SCOPE.includes(answer.name) || UNREAD.includes(answer.name))) return answer
  if (ownRefusal) return ownRefusal
  return answer
}

/**
 * A skill load's admission at read version "2" (ADR-0030): step 8 at the host's time with no read, then the
 * skill-load port's answer for the skill and the skills loaded before it. No port, or no valid answer in time, is
 * admission-unknown. Returns the refusal, or undefined where the load is admitted.
 */
export async function admitSkillLoad(principal: OpenedPrincipal, skill: SkillRef, loaded: readonly SkillRef[], signal?: AbortSignal): Promise<PrincipalRefusal | undefined> {
  const at = hostTime(principal.clock)
  if (at === undefined) return {name: 'admission-unknown'}
  const links = principal.chain.links
  if (links.some(link => at < link.issuedAt)) return {name: 'not-yet-valid'}
  if (links.some(link => at >= link.expiresAt)) return {name: 'expired'}
  const port = principal.skillLoad
  if (port === undefined) return {name: 'admission-unknown'}
  const read: SkillLoadRead = {readVersion: '2', host: links[0]!.host, audience: {kind: 'agent-host', name: principal.audience}, chain: [...principal.chain.digests], load: {skill: {id: skill.id, digest: skill.digest}, skills: skillRefs(loaded.filter(held => held.id !== skill.id))}, at: formatTime(at)}
  let answer: PrincipalRefusal | undefined | 'unknown'
  try { answer = readSkillLoadAnswer(await ask(abort => port(structuredClone(read), abort), signal)) } catch (error) {
    if (signal?.aborted) throw error
    answer = 'unknown'
  }
  return answer === 'unknown' ? {name: 'admission-unknown'} : answer
}

/** The words the model and the host are told of a refused call; they repeat no statement, key or credential. */
export function refusalDetail(tool: string, refusal: PrincipalRefusal): string {
  const named = refusal.name + (refusal.reason ? ` (${refusal.reason})` : '')
  return `${tool} was not run: the delegation this run acts under does not admit it (${named}). Nothing was changed. Do not retry it; say what could not be done.`
}
