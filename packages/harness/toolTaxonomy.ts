import {ASSISTANT_TOOLS} from './host'
/** ADR-0027. Owner declarations are separate from legacy execution bookkeeping. */
export const OPERATION_FAMILIES = ['discover', 'read', 'answer', 'check', 'transform', 'evaluate', 'propose', 'act', 'run', 'own-state', 'ask'] as const
export const TAXONOMY_EFFECTS = ['pure', 'read', 'read-external', 'propose', 'act'] as const
export const CHECK_KINDS = ['validate', 'check', 'verify', 'test', 'analyze', 'advise', 'rehearse'] as const
export type OperationFamily = typeof OPERATION_FAMILIES[number]
export type TaxonomyEffect = typeof TAXONOMY_EFFECTS[number]
export interface ToolTaxonomy { operation: OperationFamily; effect: TaxonomyEffect; checkKind?: typeof CHECK_KINDS[number]; experimental?: boolean }
export interface CapabilityProfile { effect: TaxonomyEffect; operations: OperationFamily[] }
export interface TaxonomyLimit { operations?: readonly OperationFamily[]; effects?: readonly TaxonomyEffect[] }
export type PermissionPreset = 'review' | 'work'
/** ADR-0027 amendment (#241): a rehearsal reads declared local state at most; never read-external or act. */
export const REHEARSE_EFFECTS: readonly TaxonomyEffect[] = ['pure', 'read']
/** A host's rehearse declaration opens its offered description with these words, so the model reads the result as non-binding. */
export const REHEARSAL_DESCRIPTION = 'Rehearsal, non-binding'
const compatible: Record<OperationFamily, readonly TaxonomyEffect[]> = {
  discover: ['pure','read','read-external'], read: ['read','read-external'], answer: ['pure','read','read-external'],
  check: ['pure','read','read-external'], transform: ['pure'], evaluate: ['pure','read','act'], propose: ['propose'], act: ['act'],
  run: ['read','read-external','propose','act'], 'own-state': ['pure','read'], ask: ['pure']
}
const object = (x: unknown): x is Record<string, unknown> => typeof x === 'object' && x !== null && !Array.isArray(x)
/** Runtime evidence is supplied only by the served-runtime path, never source trust metadata. */
export function taxonomyProblem(value: unknown, runtime = false): string {
  if (!object(value)) return 'mandatory tool taxonomy is missing or malformed'
  if (Object.keys(value).some(k => !['operation','effect','checkKind','experimental'].includes(k))) return 'tool taxonomy contains an unsupported member'
  if (!(OPERATION_FAMILIES as readonly unknown[]).includes(value.operation) || !(TAXONOMY_EFFECTS as readonly unknown[]).includes(value.effect)) return 'tool taxonomy operation or effect is undeclared'
  const t = value as unknown as ToolTaxonomy
  if (t.operation === 'check' && t.checkKind === 'rehearse' && !REHEARSE_EFFECTS.includes(t.effect)) return 'tool taxonomy rehearse admits only a pure or read effect'
  if (!compatible[t.operation].includes(t.effect)) return 'tool taxonomy operation contradicts its effect'
  if (t.operation === 'check' ? !(CHECK_KINDS as readonly unknown[]).includes(t.checkKind) : value.checkKind !== undefined) return 'tool taxonomy checkKind contradicts its operation'
  if (value.experimental !== undefined && typeof value.experimental !== 'boolean') return 'tool taxonomy experimental must be boolean'
  if (t.operation === 'evaluate' && !runtime) return 'only the gated runtime may declare evaluate'
  return ''
}
export function defaultApproval(effect: TaxonomyEffect): 'none' | 'required' { return effect === 'act' ? 'required' : 'none' }
export function taxonomyAnnotations(effect: TaxonomyEffect) {
  return {readOnlyHint: effect !== 'act', destructiveHint: effect === 'act', idempotentHint: effect !== 'act', openWorldHint: effect === 'read-external' || effect === 'act'}
}
export function taxonomyLimitProblem(value: unknown): string {
  if (value === undefined) return ''
  if (!object(value) || Object.keys(value).some(k=>!['operations','effects'].includes(k))) return 'host taxonomy limit is malformed'
  for (const [key, allowed] of [['operations',OPERATION_FAMILIES],['effects',TAXONOMY_EFFECTS]] as const) {
    const list=value[key]
    if (list !== undefined && (!Array.isArray(list) || list.some(v=>!(allowed as readonly unknown[]).includes(v)))) return 'host taxonomy limit is malformed'
  }
  return ''
}
/** Limits only withhold declared capabilities. They never relabel them or lower approval. */
export function taxonomyFits(t: ToolTaxonomy, limit: TaxonomyLimit = {}): boolean {
  return taxonomyProblem(t, true) === '' && taxonomyLimitProblem(limit) === '' && (t.operation === 'ask' || ((limit.operations === undefined || limit.operations.includes(t.operation)) && (limit.effects === undefined || limit.effects.includes(t.effect))))
}
export function presetLimit(preset: PermissionPreset): TaxonomyLimit {
  if (preset !== 'review' && preset !== 'work') return {operations: [], effects: []}
  return preset === 'review' ? {operations: ['discover','read','check','ask'], effects: ['pure','read','read-external']} : {operations: ['discover','read','check','propose','act','ask'], effects: [...TAXONOMY_EFFECTS]}
}
export function profileFits(profile: CapabilityProfile, limit: TaxonomyLimit): boolean {
  return (limit.effects === undefined || limit.effects.includes(profile.effect)) && profile.operations.every(op => op === 'ask' || limit.operations === undefined || limit.operations.includes(op))
}
/** Unknown references fail closed. Callers pass only tools admitted for this run. */
export function capabilityProfile(names: readonly string[], tools: ReadonlyMap<string, ToolTaxonomy>): CapabilityProfile {
  let effect: TaxonomyEffect = 'pure'
  const operations = new Set<OperationFamily>()
  for (const name of names) {
    const t = tools.get(name)
    if (t === undefined || taxonomyProblem(t, true)) throw new Error('skill references an unadmitted tool')
    if (TAXONOMY_EFFECTS.indexOf(t.effect) > TAXONOMY_EFFECTS.indexOf(effect)) effect = t.effect
    operations.add(t.operation)
  }
  return {effect, operations: OPERATION_FAMILIES.filter(op => operations.has(op))}
}
export function profileLabel(profile: CapabilityProfile): string {
  return ({pure:'pure',read:'reads local data','read-external':'reads external data',propose:'proposes for review',act:'acts; needs approval'} as const)[profile.effect]
}

/** Only the actual gated Runtime may declare evaluation; acting evaluation uses exact durable approval. */
export function runtimeTaxonomyProblem(value: unknown, name: string): string {
  if (!(ASSISTANT_TOOLS as readonly string[]).includes(name)) return 'runtime tool is outside the gated ceiling'
  const problem = taxonomyProblem(value, true)
  if (problem) return problem
  const t = value as ToolTaxonomy
  if (!['discover','read','check','transform','evaluate'].includes(t.operation) || !['pure','read',...(name==='experimental_evaluate'?['act']:[])].includes(t.effect)) return 'runtime taxonomy exceeds its local computation contract'
  if ((name === 'experimental_evaluate') !== (t.operation === 'evaluate')) return 'runtime evaluation taxonomy contradicts its gated name'
  return ''
}

/** Summaries retain all constituent families and the strongest effect, independent of map order. */
export function summarizeTaxonomies(tools: ReadonlyMap<string, ToolTaxonomy>): {taxonomy: ToolTaxonomy; profile: CapabilityProfile} {
 if (!tools.size) throw Error('empty declared group')
 const profile=capabilityProfile([...tools.keys()],tools), values=[...tools.values()], first=values[0]!
 const same=values.every(t=>t.operation===first.operation && t.checkKind===first.checkKind)
 const taxonomy: ToolTaxonomy={operation:same?first.operation:'run',effect:profile.effect,...(same&&first.checkKind!==undefined?{checkKind:first.checkKind}:{}),...(values.some(t=>t.experimental===true)?{experimental:true}:{})}
 if(taxonomyProblem(taxonomy,true)) throw Error('invalid derived group')
 return {taxonomy,profile}
}
/** Explicit mapping from canonical flat MCP metadata; unrelated transport metadata grants nothing. */
export function taxonomyFromMcpMetadata(meta: unknown, runtime=false): ToolTaxonomy {
 if (!object(meta)) throw Error('missing MCP owner metadata')
 const t={operation:meta.operation,effect:meta.effect,...(Object.hasOwn(meta,'checkKind')?{checkKind:meta.checkKind}:{}),...(Object.hasOwn(meta,'experimental')?{experimental:meta.experimental}:{})}
 if(taxonomyProblem(t,runtime)) throw Error('invalid MCP owner metadata')
 return t as ToolTaxonomy
}
