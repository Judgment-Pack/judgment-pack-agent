/** Host-declared context behavior; never model arguments or action permission. */
export interface ContextToolPolicy {
 /** The same arguments return an immutable value for this execution only. */
 immutable?:boolean
 /** Prefer this reference material in the bounded working summary. */
 reference?:boolean
}
/** Runtime protocol contracts with known immutable semantics. Mutable object
 * reads, evaluations and workflow actions must not acquire caching by name. */
export function runtimeToolContextPolicy(name:string):ContextToolPolicy {
 return ['get_schema','get_dialect_contract','get_dialect_schema','get_dialect_reference'].includes(name)?{immutable:true,reference:true}:name==='get_artifact_version_options'?{immutable:true}:{}
}
