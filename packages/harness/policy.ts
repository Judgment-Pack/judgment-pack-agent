/** Host-only policy admission (ADR-0011). No prompt, result or model tool can supply a layer. */
import schema from './contract/policy-schema.json'
import { AIFailureError } from './failures'

export type PolicyConnection = {model: import('./engine').AssistantSession['model']} | {agent: import('./engine').AgentAssistantSession['agent']}
export type ProviderFamily = 'openai' | 'anthropic' | 'gemini' | 'codex'
export interface ProviderRecord { connection: string; family: ProviderFamily }
export interface CrossProvider { continued?: boolean; role: string; relation: 'same' | 'other'; author: ProviderRecord; provider: ProviderRecord }
export type PolicyLayerName = 'run' | 'project' | 'table'
export type TaskClass = 'routine' | 'material' | 'boundary'
export interface PolicyRole { model?: string; reasoning?: string; provider?: string }
export interface PolicyLayer {
  records?: Partial<import('./records').RecordsPolicy>
  budget?: Record<string, number>
  critic?: { default?: 'off' | 'auto' | 'on'; reasoningFloor?: 'author' }
  models?: { author?: PolicyRole; critic?: PolicyRole; taskClasses?: Partial<Record<TaskClass, string>>; [role: string]: PolicyRole | Partial<Record<TaskClass, string>> | undefined }
  concurrency?: { children?: number; requests?: number }
  pace?: { requestsPerDay?: number | null; invocationsPerDay?: number | null; bytesPerDay?: number | null; warnAt?: number }
  taskClass?: TaskClass
}
export type PolicyMembers = Record<string, { value: unknown; layer: PolicyLayerName }>
export type PaceUsed = Partial<Record<'requestsPerDay' | 'invocationsPerDay' | 'bytesPerDay', number>>
export interface PolicyHost {
  /** Connected IDs, not names obtained from model text. */
  connections: readonly string[]
  families?: Readonly<Record<string, ProviderFamily>>
  authorConnection?: string
  agent: boolean
  requests: number
  paceUsed?: PaceUsed
}
export class PolicyRefusal extends AIFailureError {
  checkpoint?: import('./execution/state').ExecutionCheckpoint
  constructor(readonly member: string, readonly layer: PolicyLayerName, detail: string, pace = false) {
    super({version: 1, code: pace ? 'pace exceeded' : 'configuration', source: 'context', outcome: 'not-started', detail: `${member} (${layer}): ${detail}`})
  }
  event() { return {type: 'guardrail' as const, tool: this.member, action: 'refused' as const, reason: 'policy refused' as const, detail: this.message} }
}
type Shape = { $ref?: string; type?: string | string[]; properties?: Record<string, Shape>; patternProperties?: Record<string, Shape>; enum?: unknown[]; const?: unknown; minimum?: number; maximum?: number; minLength?: number; maxLength?: number; default?: unknown }
const object = (v: unknown): v is Record<string, unknown> => v !== null && typeof v === 'object' && !Array.isArray(v)
const deref = (s: Shape): Shape => s.$ref === '#/$defs/role' ? schema.$defs.role as Shape : s
function read(value: unknown, shape: Shape, path: string, layer: PolicyLayerName, out: Record<string, unknown>) {
  shape = deref(shape)
  const refuse = () => { throw new PolicyRefusal(path, layer, 'invalid or unknown policy member') }
  if (shape.type === 'object') {
    if (!object(value)) return refuse()
    for (const [key, v] of Object.entries(value)) {
      const next = shape.properties?.[key] ?? Object.entries(shape.patternProperties ?? {}).find(([pattern]) => new RegExp(pattern).test(key))?.[1]
      if (!next) throw new PolicyRefusal(path ? `${path}.${key}` : key, layer, 'unknown policy member')
      read(v, next, path ? `${path}.${key}` : key, layer, out)
    }
    return
  }
  if (shape.enum && !shape.enum.includes(value) || shape.const !== undefined && shape.const !== value) return refuse()
  const types = Array.isArray(shape.type) ? shape.type : [shape.type]
  if (value === null && types.includes('null')) { out[path] = null; return }
  if (types.includes('integer') || types.includes('number')) {
    if (typeof value !== 'number' || !Number.isFinite(value) || types.includes('integer') && !Number.isSafeInteger(value) || shape.minimum !== undefined && value < shape.minimum || shape.maximum !== undefined && value > shape.maximum) return refuse()
  } else if (types.includes('string') && (typeof value !== 'string' || value.length < (shape.minLength ?? 0) || value.length > (shape.maxLength ?? Infinity))) return refuse()
  out[path] = value
}
function defaults(shape: Shape, path = '', out: Record<string, unknown> = {}) {
  if (shape.default !== undefined) out[path] = shape.default
  for (const [name, child] of Object.entries(shape.properties ?? {})) defaults(child, path ? `${path}.${name}` : name, out)
  return out
}
/** Read the old budget table for one release. The nested spelling wins. */
export function policyAlias(value: PolicyLayer | Record<string, unknown>): PolicyLayer {
  const {criticDefault, criticReasoningFloor, adversarialReview, ...rest} = value as Record<string, unknown>
  const budget: Record<string, unknown> = {}
  for (const key of Object.keys(schema.properties.budget.properties)) if (Object.hasOwn(rest, key)) { budget[key] = rest[key]; delete rest[key] }
  return {...rest, ...(Object.keys(budget).length || rest.budget ? {budget: {...budget, ...(rest.budget as object)}} : {}),
    ...(criticDefault !== undefined || criticReasoningFloor !== undefined || adversarialReview !== undefined || rest.critic ? {critic: {
      ...(criticDefault !== undefined ? {default: criticDefault} : adversarialReview !== undefined ? {default: adversarialReview ? 'on' : 'off'} : {}),
      ...(criticReasoningFloor !== undefined ? {reasoningFloor: criticReasoningFloor} : {}), ...(rest.critic as object)}} : {})} as PolicyLayer
}
/** One precedence, every section. Returns detached records; callers retain these in the checkpoint. */
export function resolvePolicy(run: PolicyLayer, project: PolicyLayer, table: PolicyLayer | Record<string, unknown>, host: PolicyHost): {members: PolicyMembers; warning?: string} {
  const layers: [PolicyLayerName, PolicyLayer][] = [['table', policyAlias(table)], ['project', project], ['run', run]]
  const members: PolicyMembers = Object.fromEntries(Object.entries(defaults(schema as Shape)).map(([key, value]) => [key, {value, layer: 'table'}]))
  for (const [layer, input] of layers) {
    const flat: Record<string, unknown> = {}
    read(input, schema as Shape, '', layer, flat)
    for (const [name, value] of Object.entries(flat)) members[name] = {value, layer}
  }
  const need = (name: string) => {
    const member = members[name]
    if (!member) throw new PolicyRefusal(name, 'table', 'no default or host value')
    return member
  }
  const refuse = (name: string, reason: string, pace = false): never => { throw new PolicyRefusal(name, need(name).layer, reason, pace) }
  const tiers = host.agent ? ['none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max'] : ['off', 'low', 'on', 'medium', 'ultra', 'high']
  const rank=(value:string)=>host.agent?tiers.indexOf(value):({off:0,low:1,on:2,medium:2,ultra:3,high:3}[value]??-1)
  const task = String(need('taskClass').value)
  const mapped = members[`models.taskClasses.${task}`]
  if (mapped && (!members['models.author.reasoning'] || members['models.author.reasoning'].layer === 'table')) members['models.author.reasoning'] = {...mapped}
  const author = need('models.author.reasoning')
  const roles = new Set(['author', 'critic', ...Object.keys(members).filter(k => k.startsWith('models.') && !k.startsWith('models.taskClasses.')).map(k => k.slice(7, k.lastIndexOf('.')))])
  const base = host.authorConnection ?? String(need('models.author.model').value)
  const family = (id: string) => host.families?.[id] ?? (host.agent ? 'codex' : 'openai')
  let authorConnection = base
  for (const role of ['author', ...[...roles].filter(role => role !== 'author').sort()]) {
    const name = `models.${role}.provider`
    const legacy = members[`models.${role}.model`]
    if(legacy&&!host.connections.includes(String(legacy.value)))refuse(`models.${role}.model`,'model connection is not available')
    members[name] ??= {value: 'same', layer: 'table'}
    const choice = String(members[name]!.value)
    const selected = choice === 'same' ? authorConnection : choice === 'other'
      ? host.connections.find(id => family(id) !== family(authorConnection)) : choice
    if (!selected || !host.connections.includes(selected)) refuse(name, 'no connected provider satisfies the role')
    members[name] = {value: selected, layer: members[name]!.layer}
    if (role === 'author') authorConnection = selected!
  }
  for (const role of roles) {
    for (const field of ['model', 'reasoning']) {
      const name = `models.${role}.${field}`
      if (!members[name] && role !== 'author') members[name] = {value: need(`models.author.${field}`).value, layer: 'table'}
      const member = need(name)
      if (field === 'model' && !host.connections.includes(String(member.value))) refuse(name, 'model connection is not available')
      if (field === 'reasoning') {
        if (!tiers.includes(String(member.value))) refuse(name, 'tier is not supported on this capability')
        if (/^(child|subagent)\./.test(role) && rank(String(member.value)) > rank(String(author.value))) {
          const allowance = (project.models?.[role] as PolicyRole | undefined)?.reasoning
          if (allowance === undefined || rank(String(member.value)) > rank(allowance)) refuse(name, "tier exceeds the author's without project authorization")
        }
      }
    }
  }
  if (host.agent && need('concurrency.children').value !== 1) refuse('concurrency.children', 'agent children are sequential')
  if (Number(need('concurrency.requests').value) > host.requests) refuse('concurrency.requests', 'exceeds host capacity')
  let warning: string | undefined
  for (const key of ['requestsPerDay', 'invocationsPerDay', 'bytesPerDay'] as const) {
    const name = `pace.${key}`, limit = need(name).value, used = host.paceUsed?.[key] ?? 0
    if (!Number.isSafeInteger(used) || used < 0) refuse(name, 'invalid host period total')
    if (limit === null) continue
    // A new run needs positive allowance; equality would overspend on its first unit.
    if (used >= Number(limit)) refuse(name, 'project period budget exceeded', true)
    if (used >= Number(limit) * Number(need('pace.warnAt').value)) warning ??= `${name} (${need(name).layer}): project period warning threshold reached`
  }
  return {members, ...(warning ? {warning} : {})}
}

/** A host must start a new run to change family: prefix and cache cannot cross this boundary. */
export function failoverRefusal(role: string, from: ProviderRecord, to: ProviderRecord) {
  if (from.family === to.family) return undefined
  return new PolicyRefusal(`models.${role}.provider`, 'run', 'cross-family failover refused: the prefix and cache do not carry').event()
}
