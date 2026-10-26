import {mutateCheckpoint,type CheckpointPatch} from './execution/mutation'
/** ADR-0014: proposals are run records; acceptance and storage belong to the host. */
import { contextDigest, answer } from './context/ledger'
import { canonicalJSON } from './context/chat'
import { utf8Bytes, type ContextPolicy } from './context/policy'
import { AIFailureError, authoredFailure } from './failures'
import { frameBlock } from './framing'
import type { HostTool, AssistantEvent } from './engine'
import type { ExecutionCheckpoint } from './execution/state'

export const MEMORY_KINDS = ['fact', 'preference', 'decision', 'reference'] as const
export interface Memory {
  id: string
  kind: typeof MEMORY_KINDS[number]
  text: string
  source: { run: string; proposedBy: 'model' | 'person' | 'host' }
  scope: 'project' | 'person'
  digest: string
  createdAt: string
  expiresAt?: string
  state: 'proposed' | 'accepted' | 'declined' | 'revoked' | 'expired'
}
export interface MemoryRecall { id: string; digest: string; kind: Memory['kind']; state: Memory['state'] }
export interface Memories { proposed: Memory[]; recalled: MemoryRecall[] }
export type MemoryAnswer = Memory[] | null | undefined
/** Host-owned lifecycle reporting contains canonical metadata, never recalled prose. */
export interface MemoryLifecycleUpdate { id: string; digest: string; kind: Memory['kind']; state: 'revoked' | 'expired' }
export type MemoryLifecycleAnswer = readonly MemoryLifecycleUpdate[] | null | undefined
export const REMEMBER = 'remember'
export const REMEMBER_SCHEMA = { type: 'object', properties: { kind: { type: 'string', enum: MEMORY_KINDS }, text: { type: 'string', minLength: 1 } }, required: ['kind', 'text'], additionalProperties: false } as const
const object = (v: unknown): v is Record<string, unknown> => v !== null && typeof v === 'object' && !Array.isArray(v)
const exact = (v: Record<string, unknown>, keys: string[]) => Object.keys(v).every(k => keys.includes(k))
const identifier = (v: unknown): v is string => typeof v === 'string' && v.length > 0 && [...v].length <= 100
const digest = (v: unknown) => typeof v === 'string' && /^sha256:[0-9a-f]{64}$/.test(v)
// RFC3339 grammar plus calendar validation; Date.parse alone normalizes impossible days.
const date = (v: unknown): boolean => {
  if (typeof v !== 'string') return false
  const match = /^(\d{4})-(0[1-9]|1[0-2])-(0[1-9]|[12]\d|3[01])[Tt]([01]\d|2[0-3]):[0-5]\d:[0-5]\d(?:\.\d+)?(?:[Zz]|[+-](?:[01]\d|2[0-3]):[0-5]\d)$/.exec(v)
  if (!match || match[0] !== v) return false
  const year = Number(match[1]), month = Number(match[2]), day = Number(match[3])
  const leap = year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0)
  return year > 0 && day <= [31, leap ? 29 : 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31][month - 1]!
}
const kind = (v: unknown) => (MEMORY_KINDS as readonly unknown[]).includes(v)
const state = (v: unknown) => typeof v === 'string' && ['proposed', 'accepted', 'declined', 'revoked', 'expired'].includes(v)
export function validMemory(v: unknown): v is Memory {
  return object(v) && exact(v, ['id','kind','text','source','scope','digest','createdAt','expiresAt','state']) && identifier(v.id) && kind(v.kind) && typeof v.text === 'string' && v.text.length > 0 && object(v.source) && exact(v.source,['run','proposedBy']) && identifier(v.source.run) && typeof v.source.proposedBy === 'string' && ['model','person','host'].includes(v.source.proposedBy) && typeof v.scope === 'string' && ['project','person'].includes(v.scope) && digest(v.digest) && date(v.createdAt) && (v.expiresAt === undefined || date(v.expiresAt)) && state(v.state)
}
export function validMemories(v: unknown): v is Memories {
  return object(v) && exact(v,['proposed','recalled']) && Array.isArray(v.proposed) && v.proposed.length <= 256 && v.proposed.every(m => validMemory(m) && m.state === 'proposed') && Array.isArray(v.recalled) && v.recalled.length <= 256 && v.recalled.every(m => object(m) && exact(m,['id','digest','kind','state']) && identifier(m.id) && digest(m.digest) && kind(m.kind) && state(m.state)) && new Set(v.proposed.map(m=>m.id)).size === v.proposed.length && new Set(v.recalled.map(m=>m.id)).size === v.recalled.length
}
type RecalledMemory = { text?: string; rows: MemoryRecall[]; records: Memory[]; bytes: number; now: number }
// Validation evidence stays in this module; structural copies and edited results cannot mint it.
const validatedRecalls = new WeakMap<RecalledMemory, string>()
const preparedRecalls = new WeakMap<readonly MemoryRecall[], { prior: string; next: string }>()
const validatedRecall = (recall: RecalledMemory): RecalledMemory => {
  validatedRecalls.set(recall, canonicalJSON(recall))
  return recall
}
export async function recallMemory(value: unknown, bound: number, marker: string): Promise<RecalledMemory> {
  const fail = (detail: string): never => { throw authoredFailure('configuration', `The host's memoryFor answer ${detail}; nothing was sent.`, 'context') }
  const now = Date.now()
  if (value === undefined || value === null) return validatedRecall({ rows: [], records: [], bytes: 0, now })
  if (typeof value === 'string') return fail('has an unknown bare-text alias; use a list of closed Memory records')
  if (!Array.isArray(value) || value.length > 256 || !value.every(validMemory)) return fail('is not a list of closed memory records')
  if (new Set(value.map(m=>m.id)).size !== value.length) fail('has duplicate ids')
  const records: Memory[] = structuredClone(value as Memory[])
  for (const memory of records) {
    if (memory.digest !== `sha256:${await contextDigest(memory.text)}`) fail('has a mismatched digest')
  }
  const accepted = records.filter(m => m.state === 'accepted' && (m.expiresAt === undefined || Date.parse(m.expiresAt) > now))
  const bytes = accepted.reduce((sum,m)=>sum+utf8Bytes(canonicalJSON(m)),0)
  if (bytes > bound) fail(`is over memoryBytes (${bound})`)
  const text = accepted.map(m => frameBlock('memory', `kind: ${m.kind}\nsource: ${canonicalJSON(m.source)}\nid: ${JSON.stringify(m.id)}\n${m.text}`, marker)).join('\n\n')
  return validatedRecall({ ...(text ? { text } : {}), rows: accepted.map(({id,digest,kind,state})=>({id,digest,kind,state})), records, bytes, now })
}
const lifecycleFailure = (detail: string): never => { const error=authoredFailure('configuration', `The host's memoryLifecycle answer ${detail}; nothing was sent.`, 'context'); throw new AIFailureError({...error.failure,outcome:'not-started'}) }
export function readMemoryLifecycle(value: unknown): MemoryLifecycleUpdate[] {
  if (value === undefined || value === null) return []
  const keys = ['id','digest','kind','state']
  if (!Array.isArray(value) || value.length > 256 || !Array.from(value).every(row => object(row) && Reflect.ownKeys(row).length === 4 && keys.every(key => Object.hasOwn(row,key)))) return lifecycleFailure('is not a list of closed lifecycle rows')
  let rows: MemoryLifecycleUpdate[]
  try { rows = structuredClone(Array.from(value)) } catch { return lifecycleFailure('is not a list of closed lifecycle rows') }
  if (!rows.every(row => identifier(row.id) && digest(row.digest) && kind(row.kind) && (row.state === 'revoked' || row.state === 'expired'))) return lifecycleFailure('is not a list of closed lifecycle rows')
  if (new Set(rows.map(row => row.id)).size !== rows.length) return lifecycleFailure('has duplicate ids')
  return rows
}
/** Prepare validated recall against the prior book. Keep the original, unedited result for commit. */
export function nextMemoryRecall(prior: readonly MemoryRecall[], recall: Awaited<ReturnType<typeof recallMemory>>, lifecycle: readonly MemoryLifecycleUpdate[]): MemoryRecall[] {
  const validated = validatedRecalls.get(recall)
  if (validated === undefined) lifecycleFailure('does not contain an unedited validated recall')
  recall = structuredClone(recall)
  if (validated !== canonicalJSON(recall)) lifecycleFailure('does not contain an unedited validated recall')
  prior = structuredClone(prior)
  lifecycle = readMemoryLifecycle(lifecycle)
  const next = structuredClone(prior) as MemoryRecall[]
  const before = new Map(prior.map(row => [row.id, row]))
  const full = new Map(recall.records.map(record => [record.id, record]))
  const effective = (record: Memory): Memory['state'] => record.state === 'accepted' && record.expiresAt && Date.parse(record.expiresAt) <= recall.now ? 'expired' : record.state
  const transition = (previous: MemoryRecall, target: Memory['state']) => {
    if (previous.state !== target && !(previous.state === 'accepted' && (target === 'revoked' || target === 'expired'))) lifecycleFailure('has an invalid transition')
  }
  for (const record of recall.records) {
    const previous = before.get(record.id)
    if (!previous) continue
    if (previous.digest !== record.digest || previous.kind !== record.kind) lifecycleFailure('has a mismatched digest or kind in full-record recall')
    const target = effective(record)
    transition(previous, target)
    next.find(row => row.id === record.id)!.state = target
  }
  for (const update of lifecycle) {
    const previous = before.get(update.id)
    if (!previous) lifecycleFailure('names an unknown recalled identity')
    if (previous!.digest !== update.digest || previous!.kind !== update.kind) lifecycleFailure('has a mismatched digest or kind')
    transition(previous!, update.state)
    const record = full.get(update.id)
    if (record && effective(record) !== update.state) lifecycleFailure('contradicts full-record recall')
    next.find(row => row.id === update.id)!.state = update.state
  }
  for (const row of recall.rows) if (!before.has(row.id)) next.push(structuredClone(row))
  preparedRecalls.set(next, { prior: canonicalJSON(prior), next: canonicalJSON(next) })
  return next
}
/** Commit only the original prepared result; retain proposals made while host callbacks awaited. */
export function commitMemoryRecall(read: () => ExecutionCheckpoint, write: (patch: CheckpointPatch) => void, expected: readonly MemoryRecall[], next: readonly MemoryRecall[]): void {
  const current = read().memories ?? { proposed: [], recalled: [] }
  const expectedJSON = canonicalJSON(expected)
  if (canonicalJSON(current.recalled) !== expectedJSON) lifecycleFailure('is stale because the recalled report changed')
  const prepared = preparedRecalls.get(next)
  if (!prepared) lifecycleFailure('does not contain an unedited prepared recall')
  const recalled = structuredClone(next) as MemoryRecall[]
  if (prepared!.prior !== expectedJSON || prepared!.next !== canonicalJSON(recalled)) lifecycleFailure('does not contain an unedited prepared recall')
  const candidate = structuredClone({ proposed: current.proposed, recalled })
  if (!validMemories(candidate)) lifecycleFailure('produces an invalid or full memory report')
  write({ memories: candidate })
}
export function recordRecall(checkpoint: ExecutionCheckpoint, recall: Awaited<ReturnType<typeof recallMemory>>, write: (patch: CheckpointPatch) => void = patch => { mutateCheckpoint(checkpoint, patch) }): void {
  const prior = checkpoint.memories?.recalled ?? []
  commitMemoryRecall(() => checkpoint, write, prior, nextMemoryRecall(prior, recall, []))
}
export function rememberTool(checkpoint: () => ExecutionCheckpoint, policy: ContextPolicy, refused: (detail: string) => Error, notify: (event: AssistantEvent) => void,write:(patch:CheckpointPatch)=>void=patch=>{mutateCheckpoint(checkpoint(),patch)}): HostTool {
  return { name: REMEMBER,taxonomy:{operation:'propose',effect:'propose'}, description: 'Propose a memory for the person to accept or decline. This records a proposal only; nothing is stored or accepted.', inputSchema: REMEMBER_SCHEMA, effect: 'plan',
    async execute(args) {
      if (!exact(args,['kind','text']) || !kind(args.kind) || typeof args.text !== 'string' || args.text.length === 0 || utf8Bytes(args.text) > policy.memoryProposalBytes) throw refused('Memory proposal has an invalid kind, text or byte bound.')
      const hash = `sha256:${await contextDigest(args.text)}`
      const saved = checkpoint()
      const book = structuredClone(saved.memories ?? { proposed: [], recalled: [] })
      if (book.proposed.length >= policy.memoryProposalsPerRun) throw refused('Memory proposals exceed memoryProposalsPerRun.')
      const memory: Memory = { id: crypto.randomUUID(), kind: args.kind as Memory['kind'], text: args.text, source: {run:saved.id,proposedBy:'model'}, scope:'project',digest:hash,createdAt:new Date().toISOString(),state:'proposed' }
      book.proposed.push(memory)
      write({memories:book})
      notify({ type:'memory_proposed',memory:structuredClone(memory) })
      return answer({ proposed: memory.id, state:'proposed' })
    }
  }
}
