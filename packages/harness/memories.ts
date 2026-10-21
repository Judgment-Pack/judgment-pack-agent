/** ADR-0014: proposals are run records; acceptance and storage belong to the host. */
import { contextDigest, answer } from './context/ledger'
import { canonicalJSON } from './context/chat'
import { utf8Bytes, type ContextPolicy } from './context/policy'
import { authoredFailure } from './failures'
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
/** The string alias is supported for v0.8.0 only. */
export type MemoryAnswer = Memory[] | string | null | undefined
export const REMEMBER = 'remember'
export const REMEMBER_SCHEMA = { type: 'object', properties: { kind: { type: 'string', enum: MEMORY_KINDS }, text: { type: 'string', minLength: 1 } }, required: ['kind', 'text'], additionalProperties: false } as const
const object = (v: unknown): v is Record<string, unknown> => v !== null && typeof v === 'object' && !Array.isArray(v)
const exact = (v: Record<string, unknown>, keys: string[]) => Object.keys(v).every(k => keys.includes(k))
const identifier = (v: unknown): v is string => typeof v === 'string' && v.length > 0 && v.length <= 100
const digest = (v: unknown) => typeof v === 'string' && /^sha256:[0-9a-f]{64}$/.test(v)
const date = (v: unknown) => typeof v === 'string' && /^\d{4}-\d\d-\d\dT/.test(v) && Number.isFinite(Date.parse(v))
const kind = (v: unknown) => (MEMORY_KINDS as readonly unknown[]).includes(v)
const state = (v: unknown) => typeof v === 'string' && ['proposed', 'accepted', 'declined', 'revoked', 'expired'].includes(v)
export function validMemory(v: unknown): v is Memory {
  return object(v) && exact(v, ['id','kind','text','source','scope','digest','createdAt','expiresAt','state']) && identifier(v.id) && kind(v.kind) && typeof v.text === 'string' && v.text.length > 0 && object(v.source) && exact(v.source,['run','proposedBy']) && identifier(v.source.run) && typeof v.source.proposedBy === 'string' && ['model','person','host'].includes(v.source.proposedBy) && typeof v.scope === 'string' && ['project','person'].includes(v.scope) && digest(v.digest) && date(v.createdAt) && (v.expiresAt === undefined || date(v.expiresAt)) && state(v.state)
}
export function validMemories(v: unknown): v is Memories {
  return object(v) && exact(v,['proposed','recalled']) && Array.isArray(v.proposed) && v.proposed.length <= 256 && v.proposed.every(m => validMemory(m) && m.state === 'proposed') && Array.isArray(v.recalled) && v.recalled.length <= 256 && v.recalled.every(m => object(m) && exact(m,['id','digest','kind','state']) && identifier(m.id) && digest(m.digest) && kind(m.kind) && state(m.state)) && new Set(v.proposed.map(m=>m.id)).size === v.proposed.length && new Set(v.recalled.map(m=>m.id)).size === v.recalled.length
}
export async function recallMemory(value: unknown, bound: number, marker: string): Promise<{ text?: string; rows: MemoryRecall[]; records: Memory[]; bytes: number }> {
  const fail = (detail: string): never => { throw authoredFailure('configuration', `The host's memoryFor answer ${detail}; nothing was sent.`, 'context') }
  if (value === undefined || value === null || value === '') return { rows: [], records: [], bytes: 0 }
  if (typeof value === 'string') {
    if (utf8Bytes(value) > bound) fail(`is over memoryBytes (${bound})`)
    return { text: frameBlock('memory kind fact source host (legacy text alias)', value, marker), rows: [], records: [], bytes: utf8Bytes(value) }
  }
  if (!Array.isArray(value) || value.length > 256 || !value.every(validMemory)) return fail('is not a list of closed memory records')
  if (new Set(value.map(m=>m.id)).size !== value.length) fail('has duplicate ids')
  const records: Memory[] = structuredClone(value as Memory[])
  for (const memory of records) {
    if (memory.digest !== `sha256:${await contextDigest(memory.text)}`) fail('has a mismatched digest')
  }
  const now = Date.now()
  const accepted = records.filter(m => m.state === 'accepted' && (m.expiresAt === undefined || Date.parse(m.expiresAt) > now))
  const bytes = accepted.reduce((sum,m)=>sum+utf8Bytes(canonicalJSON(m)),0)
  if (bytes > bound) fail(`is over memoryBytes (${bound})`)
  const text = accepted.map(m => frameBlock('memory', `kind: ${m.kind}\nsource: ${canonicalJSON(m.source)}\nid: ${JSON.stringify(m.id)}\n${m.text}`, marker)).join('\n\n')
  return { ...(text ? { text } : {}), rows: accepted.map(({id,digest,kind,state})=>({id,digest,kind,state})), records, bytes }
}
export function recordRecall(checkpoint: ExecutionCheckpoint, recall: Awaited<ReturnType<typeof recallMemory>>): void {
  const book = checkpoint.memories ??= { proposed: [], recalled: [] }
  for (const previous of book.recalled) {
    const current = recall.records.find(m=>m.id === previous.id)
    if (current && current.state !== 'accepted') previous.state = current.state
    if (current?.state === 'accepted' && current.expiresAt && Date.parse(current.expiresAt) <= Date.now()) previous.state = 'expired'
  }
  for (const row of recall.rows) {
    const previous = book.recalled.find(m=>m.id === row.id)
    if (previous) Object.assign(previous,row)
    else book.recalled.push(row)
  }
  if (book.recalled.length > 256) throw authoredFailure('configuration','The recalled memory report is full.','context')
}
export function rememberTool(checkpoint: () => ExecutionCheckpoint, policy: ContextPolicy, refused: (detail: string) => Error, notify: (event: AssistantEvent) => void): HostTool {
  return { name: REMEMBER, description: 'Propose a memory for the person to accept or decline. This records a proposal only; nothing is stored or accepted.', inputSchema: REMEMBER_SCHEMA, effect: 'plan',
    async execute(args) {
      if (!exact(args,['kind','text']) || !kind(args.kind) || typeof args.text !== 'string' || args.text.length === 0 || utf8Bytes(args.text) > policy.memoryProposalBytes) throw refused('Memory proposal has an invalid kind, text or byte bound.')
      const hash = `sha256:${await contextDigest(args.text)}`
      const saved = checkpoint()
      const book = saved.memories ??= { proposed: [], recalled: [] }
      if (book.proposed.length >= policy.memoryProposalsPerRun) throw refused('Memory proposals exceed memoryProposalsPerRun.')
      const memory: Memory = { id: crypto.randomUUID(), kind: args.kind as Memory['kind'], text: args.text, source: {run:saved.id,proposedBy:'model'}, scope:'project',digest:hash,createdAt:new Date().toISOString(),state:'proposed' }
      book.proposed.push(memory)
      notify({ type:'memory_proposed',memory:structuredClone(memory) })
      return answer({ proposed: memory.id, state:'proposed' })
    }
  }
}
