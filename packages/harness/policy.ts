import {readOutputPatterns,readOutputAllow,type OutputPattern} from './outputGuard'
/** Host-only policy admission (ADR-0011). No prompt, result or model tool can supply a layer. */
import schema from './contract/policy-schema.json'
import { AIFailureError } from './failures'

export type PolicyConnection = {model: import('./engine').AssistantSession['model']} | {agent: import('./engine').AgentAssistantSession['agent']}
export type ProviderFamily = 'openai' | 'anthropic' | 'gemini' | 'codex'
export interface ProviderRecord { connection: string; family: ProviderFamily }
export interface CrossProvider { continued?: boolean; role: string; relation: 'same' | 'other'; author: ProviderRecord; provider: ProviderRecord }
export type PolicyLayerName = 'run' | 'project' | 'table'
export type TaskClass = 'routine' | 'material' | 'boundary'
export type FailoverClass = 'unavailable' | 'rate-limited' | 'timeout'
export type ReasoningValue = 'off' | 'on' | 'ultra' | 'none' | 'minimal' | 'low' | 'medium' | 'high' | 'xhigh' | 'max'
export interface DiscoveredReasoning { default?: ReasoningValue; supported: ReasoningValue[] }
export interface PolicyRole { model?: string; reasoning?: string; discovered?: {reasoning: DiscoveredReasoning}; provider?: string; fallback?: string[] }
export interface PolicyLayer {
  compaction?: 'index' | 'summary'
  outputGuard?: 'off' | 'redact' | 'refuse'
  outputPatterns?: OutputPattern[]
  outputAllow?: string[]
  records?: Partial<import('./records').RecordsPolicy>
  budget?: Record<string, number>
  critic?: { default?: 'off' | 'auto' | 'on'; reasoningFloor?: 'author' }
  models?: { author?: PolicyRole; critic?: PolicyRole; compactor?: PolicyRole; taskClasses?: Partial<Record<TaskClass, string>>; [role: string]: PolicyRole | Partial<Record<TaskClass, string>> | undefined }
  failover?: { classes?: FailoverClass[] }
  concurrency?: { children?: number; requests?: number }
  pace?: { requestsPerDay?: number | null; invocationsPerDay?: number | null; bytesPerDay?: number | null; warnAt?: number }
  taskClass?: TaskClass
}
export type PolicyMemberSource = 'layer' | 'model-default' | 'discovered'
export type PolicyMember = { value?: unknown; layer: PolicyLayerName; source?: PolicyMemberSource }
export type PolicyMembers = Record<string, PolicyMember>
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
  event() { return {type: 'guardrail' as const, tool: this.member, action: 'refused' as const, reason: this.member.includes('.fallback') ? 'failover refused' as const : 'policy refused' as const, detail: this.message} }
}
type Shape = { $ref?: string; type?: string | string[]; properties?: Record<string, Shape>; patternProperties?: Record<string, Shape>; required?: string[]; items?: Shape; enum?: unknown[]; const?: unknown; minimum?: number; maximum?: number; minLength?: number; maxLength?: number; minItems?: number; maxItems?: number; uniqueItems?: boolean; default?: unknown }
const object = (v: unknown): v is Record<string, unknown> => v !== null && typeof v === 'object' && !Array.isArray(v)
const deref = (s: Shape): Shape => s.$ref?.startsWith('#/$defs/') ? (schema.$defs as Record<string, Shape>)[s.$ref.slice(8)]! : s
function read(value: unknown, shape: Shape, path: string, layer: PolicyLayerName, out: Record<string, unknown>) {
  if(path==='outputPatterns'||path==='outputAllow'){try{out[path]=path==='outputPatterns'?readOutputPatterns(value):readOutputAllow(value);return}catch{throw new PolicyRefusal(path,layer,'invalid output guard configuration')}}
  shape = deref(shape)
  const refuse = () => { throw new PolicyRefusal(path, layer, 'invalid or unknown policy member') }
  if (shape.type === 'object') {
    if (!object(value)) return refuse()
    for (const key of shape.required ?? []) if (!Object.hasOwn(value, key)) return refuse()
    for (const [key, v] of Object.entries(value)) {
      const next = shape.properties?.[key] ?? Object.entries(shape.patternProperties ?? {}).find(([pattern]) => new RegExp(pattern).test(key))?.[1]
      if (!next) throw new PolicyRefusal(path ? `${path}.${key}` : key, layer, 'unknown policy member')
      read(v, next, path ? `${path}.${key}` : key, layer, out)
    }
    return
  }
  if (shape.type === 'array') {
    if (!Array.isArray(value) || value.length < (shape.minItems ?? 0) || value.length > (shape.maxItems ?? Infinity) || shape.uniqueItems && new Set(value.map(item => JSON.stringify(item))).size !== value.length) return refuse()
    for (let at = 0; at < value.length; at++) read(value[at], shape.items ?? {}, `${path}[${at}]`, layer, {})
    out[path] = structuredClone(value)
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
  const members: PolicyMembers = Object.fromEntries(Object.entries(defaults(schema as Shape)).map(([key, value]) => [key, {value, layer: 'table', source: 'layer'}]))
  for (const [layer, input] of layers) {
    const flat: Record<string, unknown> = {}
    read(input, schema as Shape, '', layer, flat)
    for (const [name, value] of Object.entries(flat)) members[name] = {value, layer, source: 'layer'}
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
  const resolveReasoning = (role: string, inherited?: PolicyMember): PolicyMember => {
    const name = `models.${role}.reasoning`
    const explicit = members[name]
    const discoveredDefault = members[`models.${role}.discovered.reasoning.default`]
    const discoveredSupported = members[`models.${role}.discovered.reasoning.supported`]
    if (discoveredDefault && (!Array.isArray(discoveredSupported?.value) || !discoveredSupported.value.includes(discoveredDefault.value))) {
      throw new PolicyRefusal(name, discoveredDefault.layer, `discovered default ${JSON.stringify(discoveredDefault.value)} is not advertised as supported`)
    }
    const selected = explicit ?? (inherited ? {...inherited, layer: 'table' as const} : undefined)
    if (selected) {
      if (Array.isArray(discoveredSupported?.value) && !discoveredSupported.value.includes(selected.value)) {
        if (selected.layer === 'table') {
          if (discoveredDefault) return {value: discoveredDefault.value, layer: discoveredDefault.layer, source: 'discovered'}
          return {layer: 'table', source: 'model-default'}
        }
        const model = members[`models.${role}.model`]?.value ?? role
        throw new PolicyRefusal(name, selected.layer, `reasoning value ${JSON.stringify(selected.value)} is not supported by model ${JSON.stringify(model)}`)
      }
      return selected
    }
    if (discoveredDefault) return {value: discoveredDefault.value, layer: discoveredDefault.layer, source: 'discovered'}
    return {layer: 'table', source: 'model-default'}
  }
  const roleOf = (name: string) => name.startsWith('models.') && !name.startsWith('models.taskClasses.')
    ? name.slice(7).replace(/\.(?:model|reasoning|provider|fallback|discovered\..*)$/, '') : undefined
  const roles = new Set(['author', 'critic', ...Object.keys(members).map(roleOf).filter((role): role is string => role !== undefined)])
  const base = host.authorConnection ?? String(need('models.author.model').value)
  const family = (id: string) => host.families?.[id] ?? (host.agent ? 'codex' : 'openai')
  let authorConnection = base
  for (const role of ['author', ...[...roles].filter(role => role !== 'author').sort()]) {
    const name = `models.${role}.provider`
    const legacy = members[`models.${role}.model`]
    if(legacy&&!host.connections.includes(String(legacy.value)))refuse(`models.${role}.model`,'model connection is not available')
    members[name] ??= {value: 'same', layer: 'table', source: 'layer'}
    const choice = String(members[name]!.value)
    const selected = choice === 'same' ? authorConnection : choice === 'other'
      ? host.connections.find(id => family(id) !== family(authorConnection)) : choice
    if (!selected || !host.connections.includes(selected)) refuse(name, 'no connected provider satisfies the role')
    members[name] = {value: selected, layer: members[name]!.layer, source: members[name]!.source}
    const fallbackName = `models.${role}.fallback`
    members[fallbackName] ??= {value: [], layer: 'table', source: 'layer'}
    const fallbacks = members[fallbackName]!.value
    if (!Array.isArray(fallbacks)) refuse(fallbackName, 'fallback must be an array')
    for (const id of fallbacks as unknown[]) {
      if (typeof id !== 'string' || !host.connections.includes(id)) refuse(fallbackName, 'fallback connection is not available')
      if (family(id as string) !== family(selected!)) refuse(fallbackName, 'cross-family fallback is refused')
      if (id === selected) refuse(fallbackName, 'fallback repeats the selected connection')
    }
    if (role === 'author') authorConnection = selected!
  }
  members['models.author.reasoning'] = resolveReasoning('author')
  const author = members['models.author.reasoning']!
  for (const role of roles) {
    for (const field of ['model', 'reasoning']) {
      const name = `models.${role}.${field}`
      if (!members[name] && role !== 'author' && field === 'model') members[name] = {value: need(`models.author.${field}`).value, layer: 'table', source: 'layer'}
      if (field === 'reasoning' && role !== 'author') members[name] = resolveReasoning(role, author)
      const member = need(name)
      if (field === 'model' && !host.connections.includes(String(member.value))) refuse(name, 'model connection is not available')
      if (field === 'reasoning') {
        if (member.value !== undefined && !tiers.includes(String(member.value))) refuse(name, 'tier is not supported on this capability')
        if (member.value !== undefined && /^(child|subagent)\./.test(role)) {
          const allowance = (project.models?.[role] as PolicyRole | undefined)?.reasoning
          if (member.source === 'layer' && author.value !== undefined && rank(String(member.value)) > rank(String(author.value)) && (allowance === undefined || rank(String(member.value)) > rank(allowance))) refuse(name, "tier exceeds the author's without project authorization")
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
